// Mpesa service entry point — phase 1 of the Daraja integration.
// Boot order mirrors services/notification/cmd/server/main.go so any
// operator who has triaged one service's startup can read this one.

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

	"github.com/nexussacco/mpesa/internal/auth"
	"github.com/nexussacco/mpesa/internal/config"
	"github.com/nexussacco/mpesa/internal/crypto"
	"github.com/nexussacco/mpesa/internal/daraja"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/handler"
	"github.com/nexussacco/mpesa/internal/middleware"
	"github.com/nexussacco/mpesa/internal/savingsclient"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/mpesa/internal/workflowclient"
)

func main() {
	migrate := flag.Bool("migrate", false, "run database migrations and exit")
	flag.Parse()
	if *migrate {
		// Migrations create types/tables/policies that the nexus_app
		// role can't issue. Skip the post-connect SET ROLE so the
		// migrator runs as the privileged role on the DSN.
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
		logger.Info("mpesa migrations applied")
		return
	}

	sealer, err := crypto.NewSealer(cfg.KMSMasterKeyID, cfg.KMSMasterKey)
	if err != nil {
		logger.Error("crypto sealer", "err", err)
		os.Exit(1)
	}

	// Daraja environment defaults to sandbox; production paybills
	// still go through the sandbox client when ForceSandbox=true so
	// a dev environment can't reach the live API by accident.
	env := daraja.Sandbox
	if cfg.ForceSandbox {
		env = daraja.Sandbox
	}
	darajaClient := daraja.NewClient(cfg.DarajaBaseURL, env)

	tenants := store.NewTenantStore(pool.Pool)
	paybills := store.NewPaybillStore(pool.Pool)
	credentials := store.NewCredentialStore(pool.Pool)
	inboundEvents := store.NewInboundEventStore(pool.Pool)
	resolverLookups := store.NewResolverLookups(pool.Pool)
	audit := store.NewAuditStore(pool.Pool)

	paybillH := &handler.PaybillHandler{
		DB:          pool,
		Paybills:    paybills,
		Credentials: credentials,
		Sealer:      sealer,
		Daraja:      darajaClient,
		Logger:      logger,
	}
	webhookH := &handler.WebhookHandler{
		DB:             pool,
		Paybills:       paybills,
		InboundEvents:  inboundEvents,
		Resolver:       resolverLookups,
		Audit:          audit,
		WorkflowClient: workflowclient.New(),
		Logger:         logger,
	}
	inboundH := &handler.InboundEventsHandler{
		DB:     pool,
		Events: inboundEvents,
	}

	// Phase-4 B2C surface. SavingsClient is nil-safe — when the
	// base URL isn't configured the result handler still records
	// the outbound state; the reconciler picks up finalize later.
	outboundStore := store.NewOutboundRequestStore(pool.Pool)
	var finalize handler.FinalizeClient
	if cfg.SavingsBaseURL != "" {
		finalize = savingsclient.New(cfg.SavingsBaseURL, cfg.InternalToken)
	}
	b2cH := &handler.B2CHandler{
		DB:            pool,
		Paybills:      paybills,
		Outbound:      outboundStore,
		Audit:         audit,
		Workflow:      workflowclient.New(),
		Finalize:      finalize,
		InternalToken: cfg.InternalToken,
		Logger:        logger,
	}

	allowList, err := middleware.NewIPAllowList(cfg.TrustedIPs, logger)
	if err != nil {
		logger.Error("ip allow list", "err", err)
		os.Exit(1)
	}

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)

	r := handler.Routes(handler.Deps{
		Paybill:       paybillH,
		Webhook:       webhookH,
		InboundEvents: inboundH,
		B2C:           b2cH,
		TenantStore:   tenants,
		Issuer:        issuer,
		IPAllowList:   allowList,
		AppDomain:     cfg.AppDomain,
		Logger:        logger,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}
	go func() {
		logger.Info("mpesa listening", "addr", cfg.HTTPAddr, "env", cfg.Env, "force_sandbox", cfg.ForceSandbox)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = srv.Shutdown(shutdownCtx)
	logger.Info("mpesa shut down cleanly")
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
	opts := &slog.HandlerOptions{Level: lvl}
	if env == "development" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
