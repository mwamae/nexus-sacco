// Fixed Assets HTTP surface.
//
//   GET    /v1/fixed-assets                          list (filter by status, category)
//   POST   /v1/fixed-assets                          register (auto-posts acquisition)
//   GET    /v1/fixed-assets/{id}                     detail
//   POST   /v1/fixed-assets/{id}/dispose             record disposal (auto-posts GL)
//
//   GET    /v1/depreciation-runs                     list
//   POST   /v1/depreciation-runs                     compute snapshot for an as-of date
//   GET    /v1/depreciation-runs/{id}                detail + per-asset lines
//   POST   /v1/depreciation-runs/{id}/post           post movement to GL
//
// Acquisition posting:  DR gross_asset / CR funded_from
// Depreciation posting: DR 5200 (per-asset expense_code) / CR 1590 (per-asset accumulated_code)
//   Posted as ONE journal entry per run with one line pair per
//   accumulated/expense account combination.
// Disposal posting: combination per spec — eliminates gross + accumulated,
//   recognises proceeds and gain/loss in one entry.

package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/posting"
	"github.com/nexussacco/accounting/internal/store"
)

type FixedAssetsHandler struct {
	DB     *db.Pool
	Assets *store.FixedAssetStore
	CoA    *store.CoAStore
	Engine *posting.Engine
	Logger *slog.Logger
}

// ─────────── Assets ───────────

func (h *FixedAssetsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.ListAssetsFilter{Status: q.Get("status"), Category: q.Get("category")}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.FixedAsset
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Assets.ListTx(r.Context(), tx, filter)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

type createAssetReq struct {
	AssetNo            string `json:"asset_no"`
	Name               string `json:"name"`
	Description        string `json:"description,omitempty"`
	Category           string `json:"category"`
	GLAssetCode        string `json:"gl_asset_code"`
	GLAccumulatedCode  string `json:"gl_accumulated_code,omitempty"`
	GLExpenseCode      string `json:"gl_expense_code,omitempty"`
	PurchaseDate       string `json:"purchase_date"`
	PurchaseCost       string `json:"purchase_cost"`
	SalvageValue       string `json:"salvage_value,omitempty"`
	UsefulLifeMonths   int    `json:"useful_life_months"`
	DepreciationMethod string `json:"depreciation_method,omitempty"`
	Location           string `json:"location,omitempty"`
	Custodian          string `json:"custodian,omitempty"`
	Supplier           string `json:"supplier,omitempty"`
	InvoiceRef         string `json:"invoice_ref,omitempty"`
	FundedFromCode     string `json:"funded_from_code,omitempty"` // CoA code for the credit leg of the acquisition
	Notes              string `json:"notes,omitempty"`
}

func (h *FixedAssetsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in createAssetReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.AssetNo == "" || in.Name == "" || in.Category == "" || in.GLAssetCode == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("asset_no, name, category, gl_asset_code are required"))
		return
	}
	purchDate, err := time.Parse("2006-01-02", in.PurchaseDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("purchase_date must be YYYY-MM-DD"))
		return
	}
	cost, err := decimal.NewFromString(in.PurchaseCost)
	if err != nil || cost.IsNegative() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("purchase_cost must be non-negative decimal"))
		return
	}
	salvage := decimal.Zero
	if in.SalvageValue != "" {
		salvage, err = decimal.NewFromString(in.SalvageValue)
		if err != nil || salvage.IsNegative() {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("salvage_value must be non-negative decimal"))
			return
		}
	}
	method := domain.MethodStraightLine
	if in.DepreciationMethod != "" {
		method = domain.DepreciationMethod(in.DepreciationMethod)
		if method != domain.MethodStraightLine && method != domain.MethodNone {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("depreciation_method must be straight_line or none"))
			return
		}
	}
	if method != domain.MethodNone && in.UsefulLifeMonths <= 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("useful_life_months must be > 0 for depreciable assets"))
		return
	}
	if in.GLAccumulatedCode == "" {
		in.GLAccumulatedCode = "1590"
	}
	if in.GLExpenseCode == "" {
		in.GLExpenseCode = "5200"
	}
	fundedFrom := in.FundedFromCode
	if fundedFrom == "" {
		fundedFrom = "1000"
	}

	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var created *domain.FixedAsset
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		a, err := h.Assets.CreateTx(r.Context(), tx, store.CreateAssetInput{
			AssetNo: in.AssetNo, Name: in.Name, Description: strPtr(in.Description),
			Category: in.Category,
			GLAssetCode: in.GLAssetCode, GLAccumulatedCode: in.GLAccumulatedCode, GLExpenseCode: in.GLExpenseCode,
			PurchaseDate: purchDate, PurchaseCost: cost, SalvageValue: salvage,
			UsefulLifeMonths: in.UsefulLifeMonths, DepreciationMethod: method,
			Location: strPtr(in.Location), Custodian: strPtr(in.Custodian),
			Supplier: strPtr(in.Supplier), InvoiceRef: strPtr(in.InvoiceRef),
			Notes: strPtr(in.Notes), CreatedBy: userID,
		})
		if err != nil {
			return err
		}

		// Post the acquisition.
		if cost.IsPositive() {
			entry, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
				EntryDate: purchDate, ValueDate: purchDate,
				EntryType:    domain.TypeAuto,
				SourceModule: "accounting.fixed-assets",
				SourceRef:    fmt.Sprintf("acquire-%s", a.ID),
				Narration:    fmt.Sprintf("Acquire %s (%s)", a.Name, a.AssetNo),
				Lines: []posting.Line{
					{AccountCode: in.GLAssetCode, Debit: cost, Narration: "Asset cost"},
					{AccountCode: fundedFrom, Credit: cost, Narration: "Funded from " + fundedFrom},
				},
				PostedBy: &userID,
			})
			if err != nil {
				return fmt.Errorf("post acquisition: %w", err)
			}
			if err := h.Assets.SetAcquisitionJEIDTx(r.Context(), tx, a.ID, entry.ID); err != nil {
				return err
			}
			a.AcquisitionJournalEntryID = &entry.ID
		}
		created = a
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, created)
}

func (h *FixedAssetsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var a *domain.FixedAsset
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		a, err = h.Assets.GetTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrAssetNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("asset not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, a)
}

// Dispose — record the disposal of an active asset.
//
// Posting (book_value = cost − accumulated_dep):
//   DR accumulated_dep                                    (eliminate)
//   DR proceeds_account (cash/bank) if proceeds > 0       (receive)
//   DR loss_account if proceeds < book_value
//   CR gross_asset_code                                   (eliminate)
//   CR gain_account if proceeds > book_value
type disposeReq struct {
	DisposalDate     string `json:"disposal_date"`
	Proceeds         string `json:"proceeds,omitempty"`
	ProceedsAccount  string `json:"proceeds_account,omitempty"` // default 1000
	GainAccount      string `json:"gain_account,omitempty"`     // default 4300
	LossAccount      string `json:"loss_account,omitempty"`     // default 5220
	Notes            string `json:"notes,omitempty"`
}

func (h *FixedAssetsHandler) Dispose(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in disposeReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.DisposalDate == "" {
		in.DisposalDate = time.Now().Format("2006-01-02")
	}
	disposalDate, err := time.Parse("2006-01-02", in.DisposalDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("disposal_date must be YYYY-MM-DD"))
		return
	}
	proceeds := decimal.Zero
	if in.Proceeds != "" {
		proceeds, err = decimal.NewFromString(in.Proceeds)
		if err != nil || proceeds.IsNegative() {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("proceeds must be non-negative decimal"))
			return
		}
	}
	if in.ProceedsAccount == "" {
		in.ProceedsAccount = "1000"
	}
	if in.GainAccount == "" {
		in.GainAccount = "4300"
	}
	if in.LossAccount == "" {
		in.LossAccount = "5220"
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var updated *domain.FixedAsset
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		a, err := h.Assets.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if a.Status != domain.AssetActive && a.Status != domain.AssetFullyDepreciated {
			return httpx.ErrConflict("asset is not in a disposable state (status: " + string(a.Status) + ")")
		}

		bookValue := a.PurchaseCost.Sub(a.AccumulatedDepreciation)
		gainLoss := proceeds.Sub(bookValue) // positive = gain, negative = loss

		// Build the journal entry. We may emit up to 5 lines.
		var lines []posting.Line
		if !a.AccumulatedDepreciation.IsZero() {
			lines = append(lines, posting.Line{
				AccountCode: a.GLAccumulatedCode,
				Debit:       a.AccumulatedDepreciation,
				Narration:   "Eliminate accumulated depreciation",
			})
		}
		if proceeds.IsPositive() {
			lines = append(lines, posting.Line{
				AccountCode: in.ProceedsAccount, Debit: proceeds, Narration: "Sale proceeds",
			})
		}
		if gainLoss.IsNegative() {
			lines = append(lines, posting.Line{
				AccountCode: in.LossAccount, Debit: gainLoss.Neg(), Narration: "Loss on disposal",
			})
		}
		// Credit: gross asset cost
		lines = append(lines, posting.Line{
			AccountCode: a.GLAssetCode, Credit: a.PurchaseCost, Narration: "Remove asset from books",
		})
		if gainLoss.IsPositive() {
			lines = append(lines, posting.Line{
				AccountCode: in.GainAccount, Credit: gainLoss, Narration: "Gain on disposal",
			})
		}

		entry, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
			EntryDate: disposalDate, ValueDate: disposalDate,
			EntryType:    domain.TypeAdjustment,
			SourceModule: "accounting.fixed-assets",
			SourceRef:    fmt.Sprintf("dispose-%s", a.ID),
			Narration:    fmt.Sprintf("Dispose %s (%s) — book %s, proceeds %s, gain/loss %s", a.Name, a.AssetNo, bookValue.StringFixed(2), proceeds.StringFixed(2), gainLoss.StringFixed(2)),
			Lines:        lines,
			PostedBy:     &userID,
		})
		if err != nil {
			return fmt.Errorf("post disposal: %w", err)
		}

		updated, err = h.Assets.RecordDisposalTx(r.Context(), tx, a.ID, proceeds, gainLoss, entry.ID, userID)
		return err
	})
	if err != nil {
		if e, ok := err.(*httpx.APIError); ok {
			httpx.WriteErr(w, r, e)
			return
		}
		if errors.Is(err, store.ErrAssetNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("asset not found"))
			return
		}
		if errors.Is(err, store.ErrAssetNotActive) {
			httpx.WriteErr(w, r, httpx.ErrConflict("asset is not active"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Depreciation runs ───────────

func (h *FixedAssetsHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.DepreciationRun
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Assets.ListRunsTx(r.Context(), tx, 50)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

type createDepRunReq struct {
	AsOfDate string `json:"as_of_date"`
	Notes    string `json:"notes,omitempty"`
}

func (h *FixedAssetsHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	var in createDepRunReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	asOf, err := time.Parse("2006-01-02", in.AsOfDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("as_of_date must be YYYY-MM-DD"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.DepreciationRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		assets, err := h.Assets.ActiveDepreciableAssetsTx(r.Context(), tx)
		if err != nil {
			return err
		}
		drafts := store.ComputeDepreciationDrafts(assets, asOf)
		run, err = h.Assets.CreateRunWithLinesTx(r.Context(), tx, store.CreateDepRunInput{
			AsOfDate: asOf, Notes: strPtr(in.Notes), CreatedBy: userID,
		}, drafts)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrDepAlreadyPosted) {
			httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, run)
}

func (h *FixedAssetsHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var run *domain.DepreciationRun
	var lines []domain.DepreciationRunLine
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		run, err = h.Assets.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		lines, err = h.Assets.ListRunLinesTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrDepRunNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("depreciation run not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"run": run, "lines": lines})
}

// PostRun posts a single journal entry that aggregates every line in
// the run by (expense_code, accumulated_code) pair. For SACCO defaults
// (5200 / 1590) this is one DR / one CR line.
func (h *FixedAssetsHandler) PostRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var updated *domain.DepreciationRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		run, err := h.Assets.GetRunTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if run.Status != domain.DepRunComputed {
			return httpx.ErrConflict("run is " + string(run.Status) + " — only computed runs can be posted")
		}
		if run.TotalDepreciation.IsZero() {
			// Nothing to post — finalise the run anyway so the period
			// gate (UNIQUE on posted per year-month) takes effect.
			updated, err = h.Assets.MarkRunPostedTx(r.Context(), tx, run.ID, uuid.Nil, userID)
			return err
		}
		lines, err := h.Assets.ListRunLinesTx(r.Context(), tx, run.ID)
		if err != nil {
			return err
		}
		// Aggregate by (expense_code, accumulated_code) — we need to read
		// each line's underlying asset to know which codes apply.
		type key struct{ Exp, Accum string }
		byKey := map[key]decimal.Decimal{}
		for _, ln := range lines {
			a, err := h.Assets.GetTx(r.Context(), tx, ln.AssetID)
			if err != nil {
				return err
			}
			k := key{Exp: a.GLExpenseCode, Accum: a.GLAccumulatedCode}
			byKey[k] = byKey[k].Add(ln.DepreciationAmount)
		}
		var postLines []posting.Line
		for k, amt := range byKey {
			postLines = append(postLines, posting.Line{
				AccountCode: k.Exp, Debit: amt, Narration: "Depreciation expense",
			})
			postLines = append(postLines, posting.Line{
				AccountCode: k.Accum, Credit: amt, Narration: "Accumulated depreciation",
			})
		}
		entry, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
			EntryDate: run.AsOfDate, ValueDate: run.AsOfDate,
			EntryType:    domain.TypeAuto,
			SourceModule: "accounting.fixed-assets",
			SourceRef:    fmt.Sprintf("depreciation-run-%s", run.ID),
			Narration: fmt.Sprintf("Depreciation for %04d-%02d (%d assets, %s)",
				run.PeriodYear, run.PeriodMonth, run.AssetsProcessed, run.TotalDepreciation.StringFixed(2)),
			Lines:    postLines,
			PostedBy: &userID,
		})
		if err != nil {
			return fmt.Errorf("post depreciation: %w", err)
		}
		updated, err = h.Assets.MarkRunPostedTx(r.Context(), tx, run.ID, entry.ID, userID)
		return err
	})
	if err != nil {
		if e, ok := err.(*httpx.APIError); ok {
			httpx.WriteErr(w, r, e)
			return
		}
		if errors.Is(err, store.ErrDepRunNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("depreciation run not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}
