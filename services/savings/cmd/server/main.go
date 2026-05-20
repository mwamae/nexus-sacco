// Savings service entry point.
//
// CLI flags:
//   -migrate   run pending migrations and exit
//
// With no flags it starts the HTTP server.

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

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/config"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/handler"
	"github.com/nexussacco/savings/internal/store"
)

func main() {
	migrate := flag.Bool("migrate", false, "run database migrations and exit")
	runSnapshot := flag.String("run-snapshot", "", "run the deposit daily-balance snapshot job for the named tenant slug (date optional via -snapshot-date)")
	snapshotDate := flag.String("snapshot-date", "", "snapshot date in YYYY-MM-DD (defaults to today)")
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
		logger.Info("savings migrations applied")
		return
	}

	tenants := store.NewTenantStore(pool.Pool)
	members := store.NewMemberStore(pool.Pool)
	shareStore := store.NewShareStore(pool.Pool)
	productStore := store.NewDepositProductStore(pool.Pool)
	depositStore := store.NewDepositStore(pool.Pool)

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)

	shareH := &handler.ShareHandler{
		DB:      pool,
		Tenants: tenants,
		Members: members,
		Shares:  shareStore,
		Logger:  logger,
	}
	productH := &handler.ProductHandler{
		DB:       pool,
		Products: productStore,
		Logger:   logger,
	}
	depositH := &handler.DepositHandler{
		DB:       pool,
		Tenants:  tenants,
		Members:  members,
		Products: productStore,
		Deposits: depositStore,
		Logger:   logger,
	}

	// CLI: run daily balance snapshot for a single tenant.
	if *runSnapshot != "" {
		t, err := tenants.BySlug(ctx, *runSnapshot)
		if err != nil {
			logger.Error("snapshot: tenant lookup", "slug", *runSnapshot, "err", err)
			os.Exit(1)
		}
		date := time.Now().UTC()
		if *snapshotDate != "" {
			d, err := time.Parse("2006-01-02", *snapshotDate)
			if err != nil {
				logger.Error("snapshot: invalid -snapshot-date", "err", err)
				os.Exit(1)
			}
			date = d
		}
		n, err := handler.RunDailySnapshot(ctx, depositH, t.ID, date)
		if err != nil {
			logger.Error("snapshot: failed", "err", err)
			os.Exit(1)
		}
		logger.Info("snapshot complete", "tenant", t.Slug, "date", date.Format("2006-01-02"), "accounts", n)
		return
	}

	router := handler.Routes(handler.Deps{
		Share:       shareH,
		Deposit:     depositH,
		Product:     productH,
		TenantStore: tenants,
		Issuer:      issuer,
		AppDomain:   cfg.AppDomain,
		Logger:      logger,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		logger.Info("savings service listening",
			"addr", cfg.HTTPAddr, "app_domain", cfg.AppDomain, "env", cfg.Env)
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
