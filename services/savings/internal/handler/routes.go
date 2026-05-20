package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
)

type Deps struct {
	Share       *ShareHandler
	Deposit     *DepositHandler
	Product     *ProductHandler
	Interest    *InterestHandler
	Dividend    *DividendHandler
	TenantStore *store.TenantStore
	Issuer      *auth.TokenIssuer
	AppDomain   string
	Logger      *slog.Logger
}

func Routes(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(d.Logger))
	r.Use(middleware.Logging(d.Logger))
	r.Use(middleware.CORS("*"))
	r.Use(middleware.ResolveTenant(d.TenantStore, d.AppDomain))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticated(d.Issuer))
			r.Use(middleware.RequireTenant)

			// ─────────── Share policy + register ───────────
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-policy", d.Share.GetPolicy)
			r.With(middleware.RequirePermission("tenant:settings:edit")).
				Put("/share-policy", d.Share.UpdatePolicy)
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-accounts", d.Share.List)
			r.With(middleware.RequirePermission("shares:view")).
				Get("/share-accounts/summary", d.Share.Summary)
			r.With(middleware.RequirePermission("shares:bonus_issue")).
				Post("/share-accounts/bonus-issue", d.Share.BonusIssue)

			r.Route("/share-accounts/by-member/{member_id}", func(r chi.Router) {
				r.With(middleware.RequirePermission("shares:view")).Get("/", d.Share.GetByMember)
				r.With(middleware.RequirePermission("shares:view")).Get("/transactions", d.Share.HistoryByMember)
				r.With(middleware.RequirePermission("shares:view")).Get("/certificate", d.Share.CurrentCertificate)
				r.With(middleware.RequirePermission("shares:buy")).Post("/purchase", d.Share.Purchase)
				r.With(middleware.RequirePermission("shares:transfer")).Post("/transfer", d.Share.Transfer)
				r.With(middleware.RequirePermission("shares:redeem")).Post("/redeem", d.Share.Redeem)
				r.With(middleware.RequirePermission("shares:adjust")).Post("/adjust", d.Share.Adjust)
				r.With(middleware.RequirePermission("shares:lien")).Post("/lien", d.Share.PlaceLien)
			})
			r.With(middleware.RequirePermission("shares:lien")).
				Post("/share-liens/{lien_id}/release", d.Share.ReleaseLien)

			// ─────────── Deposit products (config) ───────────
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-products", d.Product.List)
			r.With(middleware.RequirePermission("deposits:configure")).
				Post("/deposit-products", d.Product.Create)
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-products/{product_id}", d.Product.Get)
			r.With(middleware.RequirePermission("deposits:configure")).
				Put("/deposit-products/{product_id}", d.Product.Update)
			r.With(middleware.RequirePermission("deposits:configure")).
				Delete("/deposit-products/{product_id}", d.Product.Delete)

			// ─────────── Deposit accounts ───────────
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-accounts", d.Deposit.ListAccounts)
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-accounts/summary", d.Deposit.Summary)
			r.With(middleware.RequirePermission("savings:transact")).
				Post("/deposit-accounts", d.Deposit.Open)
			r.With(middleware.RequirePermission("savings:view")).
				Get("/deposit-accounts/by-member/{member_id}", d.Deposit.AccountsByMember)

			r.Route("/deposit-accounts/{account_id}", func(r chi.Router) {
				r.With(middleware.RequirePermission("savings:view")).Get("/", d.Deposit.GetAccount)
				r.With(middleware.RequirePermission("savings:view")).Get("/statement", d.Deposit.Statement)
				r.With(middleware.RequirePermission("savings:transact")).Post("/deposit", d.Deposit.Deposit)
				r.With(middleware.RequirePermission("savings:transact")).Post("/withdraw", d.Deposit.Withdraw)
				r.With(middleware.RequirePermission("savings:transact")).Post("/withdrawal-notice", d.Deposit.GiveWithdrawalNotice)
				r.With(middleware.RequirePermission("savings:transact")).Post("/transfer", d.Deposit.TransferBetweenOwn)
				r.With(middleware.RequirePermission("deposits:reverse")).Post("/reverse", d.Deposit.Reverse)
				r.With(middleware.RequirePermission("savings:approve")).Post("/adjust", d.Deposit.Adjust)
			})

			// ─────────── Interest engine ───────────
			r.With(middleware.RequirePermission("interest:view")).Get("/interest-runs", d.Interest.ListRuns)
			r.With(middleware.RequirePermission("interest:run")).Post("/interest-runs", d.Interest.CreateRun)
			r.With(middleware.RequirePermission("interest:view")).Get("/interest-runs/{run_id}", d.Interest.GetRunWithLines)
			r.With(middleware.RequirePermission("interest:run")).Post("/interest-runs/{run_id}/compute", d.Interest.Compute)
			r.With(middleware.RequirePermission("interest:run")).Patch("/interest-run-lines/{line_id}", d.Interest.UpdateLinePayout)
			r.With(middleware.RequirePermission("interest:run")).Post("/interest-runs/{run_id}/submit", d.Interest.Submit)
			r.With(middleware.RequirePermission("interest:approve")).Post("/interest-runs/{run_id}/approve", d.Interest.Approve)
			r.With(middleware.RequirePermission("interest:post")).Post("/interest-runs/{run_id}/post", d.Interest.Post)
			r.With(middleware.RequirePermission("interest:post")).Post("/interest-runs/{run_id}/lock", d.Interest.Lock)
			r.With(middleware.RequirePermission("interest:run")).Post("/interest-runs/{run_id}/cancel", d.Interest.Cancel)

			// ─────────── WHT reports ───────────
			r.With(middleware.RequirePermission("interest:view")).Get("/wht-schedule", d.Interest.WHTSchedule)
			r.With(middleware.RequirePermission("interest:view")).Get("/wht-certificate/{member_id}", d.Interest.WHTCertificate)

			// ─────────── Dividend engine ───────────
			r.With(middleware.RequirePermission("dividends:view")).Get("/dividend-runs", d.Dividend.ListRuns)
			r.With(middleware.RequirePermission("dividends:run")).Post("/dividend-runs", d.Dividend.CreateRun)
			r.With(middleware.RequirePermission("dividends:view")).Get("/dividend-runs/{run_id}", d.Dividend.GetRun)
			r.With(middleware.RequirePermission("dividends:run")).Post("/dividend-runs/{run_id}/compute", d.Dividend.Compute)
			r.With(middleware.RequirePermission("dividends:run")).Patch("/dividend-run-lines/{line_id}", d.Dividend.UpdateLinePayout)
			r.With(middleware.RequirePermission("dividends:run")).Post("/dividend-runs/{run_id}/submit", d.Dividend.Submit)
			r.With(middleware.RequirePermission("dividends:approve")).Post("/dividend-runs/{run_id}/approve", d.Dividend.Approve)
			r.With(middleware.RequirePermission("dividends:approve")).Post("/dividend-runs/{run_id}/post", d.Dividend.Post)
			r.With(middleware.RequirePermission("dividends:approve")).Post("/dividend-runs/{run_id}/lock", d.Dividend.Lock)
			r.With(middleware.RequirePermission("dividends:run")).Post("/dividend-runs/{run_id}/cancel", d.Dividend.Cancel)
		})

		// Workflow callbacks — public-ish (no auth) since the workflow
		// service POSTs from the same network. Each validates by looking
		// up the run via workflow_instance_id.
		r.Post("/interest-runs/callback", d.Interest.WorkflowCallback)
		r.Post("/dividend-runs/callback", d.Dividend.WorkflowCallback)
	})
	return r
}
