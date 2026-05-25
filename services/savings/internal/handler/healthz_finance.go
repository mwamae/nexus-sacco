// GET /healthz/finance — config-integrity probe for the GL posting
// pipeline. Mounted ABOVE auth so deploys can ping it from outside
// the JWT-issuing identity service.
//
// Currently checks one thing: every fee_catalog.gl_credit_code
// resolves to a real chart_of_accounts.code on the same tenant. A
// mismatch means a fee receipt will try to post to a nonexistent
// account and trip the dispatcher into hard-fail at 12 attempts.
//
// Returns 200 + {"status":"ok"} when clean.
// Returns 503 + {"status":"degraded", "broken":[{code, gl_credit_code, tenant_slug}]}
// when at least one row mismatches — body lists every offender so the
// deploy log surfaces actionable signal.

package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
)

type FinanceHealthHandler struct {
	DB     *db.Pool
	Logger *slog.Logger
}

type brokenFeeMapping struct {
	TenantSlug   string `json:"tenant_slug"`
	FeeCode      string `json:"fee_code"`
	GLCreditCode string `json:"gl_credit_code"`
}

type financeHealth struct {
	Status string             `json:"status"` // ok | degraded
	Broken []brokenFeeMapping `json:"broken"`
}

// CheckFinanceConfig runs the integrity probe directly. Iterates
// tenants and sets app.tenant_id per-tenant so RLS lets the fee_catalog
// + chart_of_accounts rows through — pool connections run as
// nexus_app and would otherwise return zero rows for a cross-tenant
// query. tenants itself has no RLS, so the outer list works without
// scoping.
//
// Used by both the HTTP handler and the startup hook so the two
// stay in sync.
func CheckFinanceConfig(ctx context.Context, pool *db.Pool) ([]brokenFeeMapping, error) {
	type tenantRow struct {
		ID   string
		Slug string
	}
	tenantRows, err := pool.Query(ctx, `SELECT id, slug FROM tenants ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	var tenants []tenantRow
	for tenantRows.Next() {
		var t tenantRow
		if err := tenantRows.Scan(&t.ID, &t.Slug); err != nil {
			tenantRows.Close()
			return nil, err
		}
		tenants = append(tenants, t)
	}
	tenantRows.Close()

	var broken []brokenFeeMapping
	for _, t := range tenants {
		// Per-tenant probe inside a short-lived tx so set_config
		// scopes to this tx only and doesn't leak across iterations.
		rows, err := perTenantProbe(ctx, pool, t.ID)
		if err != nil {
			return nil, err
		}
		for _, fc := range rows {
			broken = append(broken, brokenFeeMapping{
				TenantSlug:   t.Slug,
				FeeCode:      fc.code,
				GLCreditCode: fc.gl,
			})
		}
	}
	return broken, nil
}

type brokenCodePair struct {
	code, gl string
}

func perTenantProbe(ctx context.Context, pool *db.Pool, tenantID string) ([]brokenCodePair, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1::text, true)`, tenantID,
	); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT fc.code, fc.gl_credit_code
		  FROM fee_catalog fc
		 WHERE fc.is_active = true
		   AND NOT EXISTS (
		     SELECT 1 FROM chart_of_accounts ca
		      WHERE ca.tenant_id = fc.tenant_id
		        AND ca.code = fc.gl_credit_code
		        AND ca.is_active = true
		   )
		 ORDER BY fc.code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []brokenCodePair
	for rows.Next() {
		var b brokenCodePair
		if err := rows.Scan(&b.code, &b.gl); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (h *FinanceHealthHandler) Handle(w http.ResponseWriter, r *http.Request) {
	broken, err := CheckFinanceConfig(r.Context(), h.DB)
	if err != nil {
		h.Logger.Error("healthz/finance: probe failed", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(financeHealth{Status: "degraded"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(broken) == 0 {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(financeHealth{Status: "ok", Broken: []brokenFeeMapping{}})
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(financeHealth{Status: "degraded", Broken: broken})
}

// LogFinanceConfigOnBoot runs the same probe at server startup and
// emits an ERROR log line per broken row. Doesn't fail boot — the
// /healthz/finance endpoint surfaces the state for deploy automation
// to gate on (a probe failure halts the rollout without leaving the
// existing pods unreachable).
func LogFinanceConfigOnBoot(ctx context.Context, pool *db.Pool, logger *slog.Logger) {
	broken, err := CheckFinanceConfig(ctx, pool)
	if err != nil {
		// RLS may block this query at boot if we're not yet inside a
		// tenant scope. Fall through to a cross-tenant query via a
		// short-lived tx with set_config bypass — pool.Query already
		// runs without tenant scope so this branch is just a defensive
		// log. Don't crash boot for it.
		logger.Warn("healthz/finance: boot probe failed (will surface via /healthz/finance)", "err", err)
		return
	}
	if len(broken) == 0 {
		logger.Info("healthz/finance: fee_catalog ↔ chart_of_accounts clean")
		return
	}
	for _, b := range broken {
		logger.Error("healthz/finance: broken fee_catalog mapping",
			"tenant", b.TenantSlug,
			"fee_code", b.FeeCode,
			"gl_credit_code", b.GLCreditCode)
	}
	logger.Error("healthz/finance: deploy should fail this rollout — /healthz/finance returns 503",
		"broken_count", len(broken))
	_ = pgx.ErrNoRows // silence unused import if all branches above changed
}
