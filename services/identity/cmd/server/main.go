// Identity service entry point.
//
// CLI flags:
//   -migrate    run pending migrations and exit
//   -seed       create the platform pseudo-tenant + super-admin and exit
//
// With no flags it runs the HTTP server.

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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	idauth "github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/config"
	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/email"
	"github.com/nexussacco/identity/internal/handler"
	"github.com/nexussacco/identity/internal/storage"
	"github.com/nexussacco/identity/internal/store"
	"github.com/nexussacco/shared/healthx"
)

// bootTime is captured at process start so /healthz can report
// uptime via (now - started_at).
var bootTime = time.Now().UTC()

// version is overridden at link time. Reported on /healthz so the
// system-health aggregator can confirm every replica is on the
// expected SHA after a rollout.
var version string

const platformTenantSlug = "platform"

func main() {
	migrate := flag.Bool("migrate", false, "run database migrations and exit")
	seed := flag.Bool("seed", false, "ensure platform super-admin exists and exit")
	flag.Parse()

	// Migrations must run as superuser (CREATE ROLE, etc.) so we tell
	// the pool to skip SET ROLE before loading the pool.
	if *migrate || *seed {
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

	switch {
	case *migrate:
		if err := pool.Migrate(ctx); err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		logger.Info("migrations applied")
		return
	case *seed:
		if err := pool.Migrate(ctx); err != nil {
			logger.Error("migrate", "err", err)
			os.Exit(1)
		}
		if err := seedPlatformAdmin(ctx, pool, cfg, logger); err != nil {
			logger.Error("seed", "err", err)
			os.Exit(1)
		}
		return
	}

	// Note: regular startup does NOT run migrations; the runtime role
	// (nexus_app) lacks DDL privileges. Run `make migrate` first.

	tenantStore := store.NewTenantStore(pool.Pool)
	userStore := store.NewUserStore(pool.Pool)
	roleStore := store.NewRoleStore(pool.Pool)
	sessionStore := store.NewSessionStore(pool.Pool)
	auditStore := store.NewAuditStore(pool.Pool)
	mfaStore := store.NewMFAStore(pool.Pool)
	resetStore := store.NewPasswordResetStore(pool.Pool)
	permissionStore := store.NewPermissionStore(pool.Pool)
	inviteStore := store.NewInviteStore(pool.Pool)
	settingsStore := store.NewSettingsStore(pool.Pool)

	stor, err := storage.NewLocalDisk(cfg.StorageDir)
	if err != nil {
		logger.Error("storage init", "err", err)
		os.Exit(1)
	}

	emailSender := email.New(email.Config{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		Username: cfg.SMTPUser,
		Password: cfg.SMTPPassword,
		From:     cfg.SMTPFrom,
		FromName: cfg.SMTPFromName,
		UseTLS:   cfg.SMTPUseTLS,
	})
	if emailSender.Enabled() {
		logger.Info("email enabled", "host", cfg.SMTPHost, "port", cfg.SMTPPort)
	} else {
		logger.Warn("email disabled — SMTP_HOST not set; OTPs will be logged instead")
	}

	platformTenant, err := tenantStore.BySlug(ctx, platformTenantSlug)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		logger.Error("load platform tenant", "err", err)
		os.Exit(1)
	}
	if platformTenant == nil {
		logger.Warn("platform tenant not found — run `make seed` to bootstrap")
	}

	issuer := idauth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAccessTTL)

	authH := &handler.AuthHandler{
		DB:               pool,
		Users:            userStore,
		Roles:            roleStore,
		Sessions:         sessionStore,
		MFA:              mfaStore,
		PasswordResets:   resetStore,
		Invites:          inviteStore,
		Audit:            auditStore,
		Issuer:           issuer,
		RefreshTTL:       cfg.JWTRefreshTTL,
		PasswordResetTTL: cfg.PasswordResetTTL,
		WebBaseURL:       cfg.WebBaseURL,
		Logger:           logger,
		Email:            emailSender,
		PlatformTenant:   platformTenant,
	}
	userH := &handler.UserHandler{
		DB: pool, Users: userStore, Roles: roleStore, Invites: inviteStore,
		Tenants: tenantStore, Audit: auditStore, Email: emailSender,
		WebBaseURL: cfg.WebBaseURL, InviteTTL: cfg.InviteTTL,
		Logger: logger, PlatformTenant: platformTenant,
	}
	tenantH := &handler.TenantHandler{
		DB: pool, Tenants: tenantStore, Users: userStore, Roles: roleStore,
		Invites: inviteStore, Audit: auditStore, Logger: logger,
		InviteTTL: cfg.InviteTTL,
		UserH:     userH,
		AuthH:     authH,
	}
	rbacH := &handler.RBACHandler{
		DB: pool, Roles: roleStore, Permissions: permissionStore,
		Audit: auditStore, Logger: logger, PlatformTenant: platformTenant,
	}
	settingsH := &handler.SettingsHandler{
		DB: pool, Settings: settingsStore, Audit: auditStore,
		Storage: stor, MaxUpload: cfg.MaxUploadBytes, Logger: logger,
	}
	auditH := &handler.AuditHandler{Audit: auditStore, Logger: logger}
	systemHealthH := &handler.SystemHealthHandler{
		DB:         pool,
		Logger:     logger,
		HTTPClient: &http.Client{Timeout: 1500 * time.Millisecond},
	}

	healthBuilder := &healthx.Builder{
		Service:   "identity",
		Version:   buildVersion(),
		StartedAt: bootTime,
		Probes: map[string]healthx.Probe{
			"database": healthx.DBPingProbe(pool.Pool),
		},
	}

	router := handler.Routes(handler.Deps{
		Auth: authH, Tenant: tenantH, User: userH, RBAC: rbacH, Settings: settingsH,
		AuditH:       auditH,
		SystemHealth: systemHealthH,
		TenantStore:  tenantStore, Issuer: issuer,
		AppDomain: cfg.AppDomain, Logger: logger,
		Health: healthBuilder.Handler(500 * time.Millisecond),
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		logger.Info("identity service listening", "addr", cfg.HTTPAddr, "app_domain", cfg.AppDomain, "env", cfg.Env)
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

// seedPlatformAdmin ensures the platform pseudo-tenant and a super-admin
// user exist. Idempotent — safe to re-run.
func seedPlatformAdmin(ctx context.Context, pool *db.Pool, cfg *config.Config, logger *slog.Logger) error {
	if cfg.PlatformAdminEmail == "" || cfg.PlatformAdminPassword == "" {
		return errors.New("PLATFORM_ADMIN_EMAIL and PLATFORM_ADMIN_PASSWORD must be set")
	}
	if len(cfg.PlatformAdminPassword) < 12 {
		return errors.New("PLATFORM_ADMIN_PASSWORD must be at least 12 characters")
	}

	tenants := store.NewTenantStore(pool.Pool)
	users := store.NewUserStore(pool.Pool)
	roles := store.NewRoleStore(pool.Pool)

	platformTenant, err := tenants.BySlug(ctx, platformTenantSlug)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("look up platform tenant: %w", err)
	}
	if platformTenant == nil {
		platformTenant, err = tenants.Create(ctx, store.CreateTenantInput{
			Slug: platformTenantSlug, Name: "Platform", Kind: domain.TenantKindSACCO,
			CountryCode: "KE", CurrencyCode: "KES",
		})
		if err != nil {
			return fmt.Errorf("create platform tenant: %w", err)
		}
		logger.Info("created platform tenant", "id", platformTenant.ID)
	}

	platformRole, err := roles.SystemRoleByCode(ctx, "platform_admin")
	if err != nil {
		return fmt.Errorf("look up platform_admin role: %w", err)
	}

	email := strings.ToLower(strings.TrimSpace(cfg.PlatformAdminEmail))

	return pool.WithTenantTx(ctx, platformTenant.ID, func(tx pgx.Tx) error {
		existing, err := users.ByEmailTx(ctx, tx, email)
		if err == nil && existing != nil {
			logger.Info("platform admin already exists", "email", existing.Email)
			return nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		hash, err := idauth.HashPassword(cfg.PlatformAdminPassword)
		if err != nil {
			return err
		}
		u, err := users.CreateTx(ctx, tx, store.CreateUserInput{
			TenantID:        platformTenant.ID,
			Email:           email,
			FullName:        "Platform Super Admin",
			PasswordHash:    hash,
			Status:          domain.UserStatusActive,
			IsPlatformAdmin: true,
		})
		if err != nil {
			return err
		}
		if err := roles.AssignTx(ctx, tx, u.ID, platformRole.ID, nil); err != nil {
			return err
		}
		logger.Info("seeded platform super-admin", "id", u.ID, "email", u.Email)
		return nil
	})
}

// buildVersion returns the link-time version, falling back to env
// BUILD_VERSION, falling back to "dev".
func buildVersion() string {
	if version != "" {
		return version
	}
	if v := os.Getenv("BUILD_VERSION"); v != "" {
		return v
	}
	return "dev"
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

// Silence unused.
var _ = uuid.Nil
