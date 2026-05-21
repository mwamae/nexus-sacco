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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/auth"
	"github.com/nexussacco/notification/internal/bus"
	"github.com/nexussacco/notification/internal/config"
	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/handler"
	"github.com/nexussacco/notification/internal/otp"
	"github.com/nexussacco/notification/internal/pdf"
	"github.com/nexussacco/notification/internal/store"
	"github.com/nexussacco/notification/internal/worker"
)

func main() {
	migrate := flag.Bool("migrate", false, "run database migrations and exit")
	backfillNextRuns := flag.Bool("backfill-next-runs", false, "recompute next_run_at for every scheduled job using the tenant's timezone, then exit")
	flag.Parse()
	if *migrate || *backfillNextRuns {
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

	if *backfillNextRuns {
		n, err := backfillScheduledJobNextRuns(ctx, pool, logger)
		if err != nil {
			logger.Error("backfill next_run_at failed", "err", err)
			os.Exit(1)
		}
		logger.Info("backfill complete", "rows_updated", n)
		return
	}

	tenants := store.NewTenantStore(pool.Pool)
	events := store.NewEventStore(pool.Pool)
	templates := store.NewTemplateStore(pool.Pool)
	notifs := store.NewNotificationStore(pool.Pool)
	smtpStore := store.NewSMTPConfigStore(pool.Pool, cfg.JWTSecret)
	smsStore := store.NewSMSConfigStore(pool.Pool, cfg.JWTSecret)
	pdfStore := store.NewPDFStore(pool.Pool)
	otpStore := store.NewOTPStore(pool.Pool)
	otpSettingsStore := store.NewOTPSettingsStore(pool.Pool)
	campaignStore := store.NewCampaignStore(pool.Pool)
	schedulerStore := store.NewSchedulerStore(pool.Pool)
	audienceStore := store.NewAudienceStore(pool.Pool)

	pdfStorage, err := pdf.NewStorage(cfg.PDFStorageDir)
	if err != nil {
		logger.Error("pdf storage", "err", err)
		os.Exit(1)
	}
	pdfRenderer, err := pdf.NewRenderer(ctx)
	if err != nil {
		logger.Error("pdf renderer (chromedp)", "err", err)
		os.Exit(1)
	}
	defer pdfRenderer.Close()
	pdfGenerator := &pdf.Generator{
		DB:       pool,
		PDFs:     pdfStore,
		Renderer: pdfRenderer,
		Storage:  pdfStorage,
	}

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTIssuer)
	realtime := bus.New()

	notifyH := &handler.Handler{
		DB:            pool,
		Events:        events,
		Templates:     templates,
		Notifications: notifs,
		Tenants:       tenants,
		PDFs:          pdfStore,
		PDFGenerator:  pdfGenerator,
		PDFStorage:    pdfStorage,
		Bus:           realtime,
		InternalToken: cfg.InternalToken,
		Logger:        logger,
	}
	pdfH := &handler.PDFHandler{
		DB:            pool,
		PDFs:          pdfStore,
		Generator:     pdfGenerator,
		Storage:       pdfStorage,
		InternalToken: cfg.InternalToken,
		Logger:        logger,
	}
	sseH := &handler.SSEHandler{
		DB:     pool,
		Notifs: notifs,
		Bus:    realtime,
		Logger: logger,
	}
	smtpH := &handler.SMTPHandler{
		DB:     pool,
		SMTP:   smtpStore,
		Logger: logger,
	}
	smsH := &handler.SMSHandler{
		DB:     pool,
		SMS:    smsStore,
		Notifs: notifs,
		Logger: logger,
	}
	otpService := &otp.Service{
		DB:            pool,
		OTPs:          otpStore,
		Settings:      otpSettingsStore,
		Notifications: notifs,
		Templates:     templates,
		HashKey:       cfg.JWTSecret,
	}
	otpH := &handler.OTPHandler{
		DB:            pool,
		OTP:           otpService,
		OTPs:          otpStore,
		Settings:      otpSettingsStore,
		InternalToken: cfg.InternalToken,
		Logger:        logger,
	}

	// Stage 7 — campaign worker + scheduler. The registry maps a
	// job_key (stored on the row) to a Go handler function. Add new
	// jobs by writing a handler in worker/jobs.go and registering it
	// here; the DB row is the source of truth for cron schedule + on/off.
	jobRegistry := worker.NewJobRegistry()
	jobRegistry.Register("loan_repayment_reminders", worker.LoanRepaymentReminderHandler(notifs, templates))
	jobRegistry.Register("dormancy_warnings", worker.DormancyWarningHandler(notifs, templates))

	scheduler := worker.NewScheduler(pool, schedulerStore, notifs, tenants, jobRegistry, logger)
	campaignWorker := &worker.CampaignWorker{
		DB:           pool,
		Notifs:       notifs,
		Templates:    templates,
		Campaigns:    campaignStore,
		Audience:     audienceStore,
		TickInterval: 15 * time.Second,
		Logger:       logger,
	}

	campaignH := &handler.CampaignHandler{
		DB:        pool,
		Campaigns: campaignStore,
		Audience:  audienceStore,
		Templates: templates,
		Tenants:   tenants,
		Logger:    logger,
	}
	schedulerH := &handler.SchedulerHandler{
		DB:        pool,
		Sched:     schedulerStore,
		Tenants:   tenants,
		Scheduler: scheduler,
		Logger:    logger,
	}
	templateH := &handler.TemplateHandler{
		DB:        pool,
		Templates: templates,
		Events:    events,
		Logger:    logger,
	}

	router := handler.Routes(handler.Deps{
		Notify:      notifyH,
		SMTP:        smtpH,
		SMS:         smsH,
		SSE:         sseH,
		PDF:         pdfH,
		OTP:         otpH,
		Campaign:    campaignH,
		Scheduler:   schedulerH,
		Template:    templateH,
		TenantStore: tenants,
		Issuer:      issuer,
		AppDomain:   cfg.AppDomain,
		Logger:      logger,
	})

	// Workers — both drain their channel-specific queues continuously.
	emailWorker := &worker.EmailWorker{
		DB:           pool,
		Notifs:       notifs,
		SMTPStore:    smtpStore,
		PDFStorage:   pdfStorage,
		TickInterval: 10 * time.Second,
		BatchSize:    25,
		Logger:       logger,
	}
	go emailWorker.Run(ctx)

	smsWorker := &worker.SMSWorker{
		DB:           pool,
		Notifs:       notifs,
		SMSStore:     smsStore,
		TickInterval: 10 * time.Second,
		Logger:       logger,
	}
	go smsWorker.Run(ctx)

	go campaignWorker.Run(ctx)
	go scheduler.Run(ctx)

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

// backfillScheduledJobNextRuns recomputes next_run_at for every row
// in notification_scheduled_jobs using the tenant's configured
// timezone. One-shot fixer after the timezone-aware scheduler shipped
// — rows seeded under the old server-local interpretation get the
// wrong wall-clock anchor and need rewriting before the worker would
// catch up on its own.
func backfillScheduledJobNextRuns(ctx context.Context, pool *db.Pool, logger *slog.Logger) (int, error) {
	tenants := store.NewTenantStore(pool.Pool)
	schedStore := store.NewSchedulerStore(pool.Pool)
	notifs := store.NewNotificationStore(pool.Pool)
	sched := worker.NewScheduler(pool, schedStore, notifs, tenants, worker.NewJobRegistry(), logger)

	var jobs []domain.ScheduledJob
	err := pool.WithTenantTx(ctx, uuid.Nil, func(tx pgx.Tx) error {
		var err error
		jobs, err = schedStore.ListAllAcrossTenantsTx(ctx, tx)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("list jobs: %w", err)
	}
	logger.Info("backfill: jobs discovered", "count", len(jobs))

	// Cache tenant locations so we don't hit the DB once per job for
	// the same tenant.
	locCache := map[uuid.UUID]*time.Location{}
	tenantLoc := func(tid uuid.UUID) *time.Location {
		if l, ok := locCache[tid]; ok {
			return l
		}
		var tz string
		_ = pool.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
			var err error
			tz, err = tenants.TimezoneTx(ctx, tx)
			return err
		})
		loc, err := time.LoadLocation(tz)
		if err != nil || loc == nil {
			loc = time.UTC
		}
		locCache[tid] = loc
		return loc
	}

	now := time.Now()
	updated := 0
	for _, j := range jobs {
		loc := tenantLoc(j.TenantID)
		next, err := sched.NextFiringIn(j.CronExpr, now, loc)
		if err != nil {
			logger.Warn("backfill: invalid cron — skipping",
				"tenant", j.TenantID, "job_key", j.JobKey, "expr", j.CronExpr, "err", err)
			continue
		}
		err = pool.WithTenantTx(ctx, j.TenantID, func(tx pgx.Tx) error {
			_, exerr := tx.Exec(ctx,
				`UPDATE notification_scheduled_jobs SET next_run_at = $2, updated_at = now() WHERE id = $1`,
				j.ID, next)
			return exerr
		})
		if err != nil {
			return updated, fmt.Errorf("update job %s: %w", j.ID, err)
		}
		logger.Info("backfill: rewrote next_run_at",
			"tenant", j.TenantID, "job_key", j.JobKey,
			"cron", j.CronExpr, "tz", loc.String(),
			"old", j.NextRunAt, "new", next)
		updated++
	}
	return updated, nil
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
