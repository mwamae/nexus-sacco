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
		})
	})
	return r
}
