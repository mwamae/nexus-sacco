// Workflow service entry point.

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

	"github.com/nexussacco/workflow/internal/auth"
	"github.com/nexussacco/workflow/internal/config"
	"github.com/nexussacco/workflow/internal/db"
	"github.com/nexussacco/workflow/internal/handler"
	"github.com/nexussacco/workflow/internal/notifier"
	"github.com/nexussacco/workflow/internal/store"
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
		logger.Info("workflow migrations applied")
		return
	}

	tenants := store.NewTenantStore(pool.Pool)
	defs := store.NewDefinitionStore(pool.Pool)
	instances := store.NewInstanceStore(pool.Pool)
	actions := store.NewActionStore(pool.Pool)
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)

	notifyClient := notifier.New(cfg.NotificationURL, cfg.NotificationInternalToken, logger)

	defH := &handler.DefinitionHandler{DB: pool, Defs: defs, Logger: logger}
	instH := &handler.InstanceHandler{
		DB: pool, Defs: defs, Instances: instances, Actions: actions, Tenants: tenants,
		HTTP: &http.Client{Timeout: cfg.CallbackTimeout},
		CallbackTimeout: cfg.CallbackTimeout, Logger: logger,
		Notifier: notifyClient,
	}

	router := handler.Routes(handler.Deps{
		Definitions: defH, Instances: instH,
		TenantStore: tenants, Issuer: issuer,
		AppDomain: cfg.AppDomain, Logger: logger,
	})

	srv := &http.Server{
		Addr: cfg.HTTPAddr, Handler: router,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	go func() {
		logger.Info("workflow service listening", "addr", cfg.HTTPAddr, "app_domain", cfg.AppDomain, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			cancel()
		}
	}()
	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
}

func newLogger(level, env string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	if env == "development" {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
