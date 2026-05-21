// Notification service entry point — Stage 1 of the central
// Notifications module. In-app channel only; SMS/email land in
// stages 2-3.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nexussacco/notification/internal/auth"
	"github.com/nexussacco/notification/internal/config"
	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/handler"
	"github.com/nexussacco/notification/internal/store"
	"github.com/nexussacco/notification/internal/worker"
)

func main() {
	migrate := flag.Bool("migrate", false, "run database migrations and exit")
	flag.Parse()
	if *migrate {
		_ = os.Setenv("DB_SKIP_SET_ROLE", "1")
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := newLogger(cfg.LogLevel, cfg.Env)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *migrate {
		if err := pool.Migrate(ctx); err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		logger.Info("notification migrations applied")
		return
	}

	tenants := store.NewTenantStore(pool.Pool)
	events := store.NewEventStore(pool.Pool)
	templates := store.NewTemplateStore(pool.Pool)
	notifs := store.NewNotificationStore(pool.Pool)
	smtpStore := store.NewSMTPConfigStore(pool.Pool, cfg.JWTSecret)

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)

	notifyH := &handler.Handler{
		DB:            pool,
		Events:        events,
		Templates:     templates,
		Notifications: notifs,
		Tenants:       tenants,
		InternalToken: cfg.InternalToken,
		Logger:        logger,
	}
	smtpH := &handler.SMTPHandler{
		DB:     pool,
		SMTP:   smtpStore,
		Logger: logger,
	}

	router := handler.Routes(handler.Deps{
		Notify:      notifyH,
		SMTP:        smtpH,
		TenantStore: tenants,
		Issuer:      issuer,
		AppDomain:   cfg.AppDomain,
		Logger:      logger,
	})

	// Email worker — drains the queued email deliveries continuously.
	emailWorker := &worker.EmailWorker{
		DB:           pool,
		Notifs:       notifs,
		SMTPStore:    smtpStore,
		TickInterval: 10 * time.Second,
		BatchSize:    25,
		Logger:       logger,
	}
	go emailWorker.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}

	go func() {
		logger.Info("notification listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = srv.Shutdown(shutdownCtx)
}

func newLogger(level, env string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: l}
	if env == "production" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
