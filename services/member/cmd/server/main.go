// Member service entry point.
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

	"github.com/nexussacco/member/internal/auth"
	"github.com/nexussacco/member/internal/config"
	"github.com/nexussacco/member/internal/db"
	"github.com/nexussacco/member/internal/handler"
	"github.com/nexussacco/member/internal/notifier"
	"github.com/nexussacco/member/internal/storage"
	"github.com/nexussacco/member/internal/store"
)

func main() {
	migrate := flag.Bool("migrate", false, "run database migrations and exit")
	runDormancy := flag.String("run-dormancy", "", "run the dormancy detector for the named tenant slug and exit")
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
		logger.Info("member migrations applied")
		return
	}

	stor, err := storage.NewLocalDisk(cfg.StorageDir)
	if err != nil {
		logger.Error("storage init", "err", err)
		os.Exit(1)
	}

	tenants := store.NewTenantStore(pool.Pool)
	members := store.NewMemberStore(pool.Pool)
	rels := store.NewRelationStore(pool.Pool)
	docs := store.NewDocumentStore(pool.Pool)
	auditStore := store.NewAuditStore(pool.Pool)

	orgs := store.NewOrgMemberStore(pool.Pool)
	orgDocs := store.NewOrgDocumentStore(pool.Pool)
	officials := store.NewOrgOfficialStore(pool.Pool)
	signatories := store.NewOrgSignatoryStore(pool.Pool)
	banking := store.NewOrgBankingStore(pool.Pool)
	contacts := store.NewOrgContactStore(pool.Pool)
	statusStore := store.NewStatusChangeStore(pool.Pool)

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)
	notifyClient := notifier.New(cfg.NotificationURL, cfg.NotificationInternalToken, logger)

	memH := &handler.MemberHandler{
		DB: pool, Members: members, Relations: rels, Documents: docs, Audit: auditStore,
		Storage: stor, MaxUpload: cfg.MaxUploadBytes, Logger: logger,
		Notifier: notifyClient,
	}
	orgH := &handler.OrgHandler{
		DB: pool, Orgs: orgs, Documents: orgDocs, Officials: officials,
		Signatories: signatories, Banking: banking, Contacts: contacts,
		Audit: auditStore, Storage: stor, MaxUpload: cfg.MaxUploadBytes, Logger: logger,
		Notifier: notifyClient,
	}
	statusH := &handler.StatusHandler{
		DB: pool, Members: members, Status: statusStore, Audit: auditStore,
		Storage: stor, MaxUpload: cfg.MaxUploadBytes, Logger: logger,
		WorkflowURL:         cfg.WorkflowURL,
		MemberSelfURL:       cfg.MemberSelfURL,
		WorkflowProcessKind: cfg.WorkflowProcessKind,
		DefaultDormancyDays: cfg.DefaultDormancyDays,
		HTTP:                &http.Client{Timeout: 10 * time.Second},
		Notifier:            notifyClient,
	}

	// CLI: run the dormancy detector for a single tenant and exit.
	// Useful as a cron handle — `member -run-dormancy=tujenge` from a
	// systemd timer / Kubernetes CronJob until a proper scheduler exists.
	if *runDormancy != "" {
		t, err := tenants.BySlug(ctx, *runDormancy)
		if err != nil {
			logger.Error("dormancy: tenant lookup", "slug", *runDormancy, "err", err)
			os.Exit(1)
		}
		n, err := handler.RunDormancyForTenant(ctx, statusH, t.ID)
		if err != nil {
			logger.Error("dormancy: run failed", "err", err)
			os.Exit(1)
		}
		logger.Info("dormancy run complete", "tenant", t.Slug, "applied", n)
		return
	}

	router := handler.Routes(handler.Deps{
		Member: memH, Org: orgH, Status: statusH, TenantStore: tenants, Issuer: issuer,
		AppDomain: cfg.AppDomain, Logger: logger,
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
		logger.Info("member service listening",
			"addr", cfg.HTTPAddr, "app_domain", cfg.AppDomain,
			"storage_dir", stor.Root, "env", cfg.Env)
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
