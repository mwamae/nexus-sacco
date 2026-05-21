// Cash & Float Management HTTP surface.
//
//   GET    /v1/tills                                    list tills
//   POST   /v1/tills                                    register a till
//   GET    /v1/tills/{id}                               till + open session
//   GET    /v1/tills/{id}/sessions                      session history
//
//   POST   /v1/till-sessions                            open a session
//                                                       (also posts vault→till GL for opening float)
//   GET    /v1/till-sessions/{id}                       session detail + transfers
//   POST   /v1/till-sessions/{id}/close                 close, compute variance, post GL
//
//   POST   /v1/cash-transfers                           any vault↔till or till↔till move
//   GET    /v1/cash-transfers                           recent transfers
//
//   GET    /v1/cash-position                            vault + till + variance snapshot

package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

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

type CashHandler struct {
	DB     *db.Pool
	Cash   *store.CashStore
	Engine *posting.Engine
	Logger *slog.Logger
}

// ─────────── Tills ───────────

func (h *CashHandler) ListTills(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	activeOnly := r.URL.Query().Get("active") == "true"
	var items []domain.Till
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Cash.ListTillsTx(r.Context(), tx, activeOnly)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

type createTillReq struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Branch   string  `json:"branch,omitempty"`
	MaxFloat *string `json:"max_float,omitempty"`
	Notes    string  `json:"notes,omitempty"`
}

func (h *CashHandler) CreateTill(w http.ResponseWriter, r *http.Request) {
	var in createTillReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Code == "" || in.Name == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("code and name are required"))
		return
	}
	var maxFloat *decimal.Decimal
	if in.MaxFloat != nil && *in.MaxFloat != "" {
		d, err := decimal.NewFromString(*in.MaxFloat)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("max_float must be a decimal"))
			return
		}
		maxFloat = &d
	}
	tid, _ := middleware.TenantIDFrom(r)
	var created *domain.Till
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		created, err = h.Cash.CreateTillTx(r.Context(), tx, store.CreateTillInput{
			Code: in.Code, Name: in.Name,
			Branch: strPtr(in.Branch), MaxFloat: maxFloat, Notes: strPtr(in.Notes),
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, created)
}

type tillDetailResp struct {
	Till           *domain.Till         `json:"till"`
	CurrentSession *domain.TillSession  `json:"current_session,omitempty"`
}

func (h *CashHandler) GetTill(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var resp tillDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		t, err := h.Cash.GetTillTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp.Till = t
		curr, err := h.Cash.CurrentSessionTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp.CurrentSession = curr
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrTillNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("till not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

func (h *CashHandler) ListTillSessions(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.TillSession
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Cash.ListSessionsByTillTx(r.Context(), tx, id, limit)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

// ─────────── Sessions ───────────

type openSessionReq struct {
	TillID       uuid.UUID `json:"till_id"`
	TellerUserID uuid.UUID `json:"teller_user_id"`
	OpeningFloat string    `json:"opening_float"`
	Notes        string    `json:"notes,omitempty"`
}

func (h *CashHandler) OpenSession(w http.ResponseWriter, r *http.Request) {
	var in openSessionReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.TillID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("till_id required"))
		return
	}
	if in.TellerUserID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("teller_user_id required"))
		return
	}
	openingFloat, err := decimal.NewFromString(in.OpeningFloat)
	if err != nil || !openingFloat.IsPositive() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("opening_float must be a positive decimal"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var session *domain.TillSession
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		till, err := h.Cash.GetTillTx(r.Context(), tx, in.TillID)
		if err != nil {
			return err
		}
		session, err = h.Cash.OpenSessionTx(r.Context(), tx, store.OpenSessionInput{
			TillID: in.TillID, TellerUserID: in.TellerUserID,
			OpeningFloat: openingFloat, Notes: strPtr(in.Notes), OpenedBy: userID,
		})
		if err != nil {
			return err
		}

		// Post the vault→till opening float entry.
		entry, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
			EntryType:    domain.TypeAuto,
			SourceModule: "accounting.cash-management",
			SourceRef:    fmt.Sprintf("open-session-%s", session.ID),
			Narration:    fmt.Sprintf("Opening float: %s → %s (%s)", till.VaultAccountCode, till.GLAccountCode, till.Code),
			Lines: []posting.Line{
				{AccountCode: till.GLAccountCode, Debit: openingFloat, Narration: "Float issued to " + till.Code},
				{AccountCode: till.VaultAccountCode, Credit: openingFloat, Narration: "Float drawn from vault"},
			},
			PostedBy: &userID,
		})
		if err != nil {
			return fmt.Errorf("post opening float: %w", err)
		}

		// Record the transfer.
		_, err = h.Cash.CreateTransferTx(r.Context(), tx, store.CreateTransferInput{
			TransferType:   domain.TransferOpeningFloat,
			ToTillID:       &in.TillID,
			SessionID:      &session.ID,
			Amount:         openingFloat,
			Narration:      strPtr(fmt.Sprintf("Opening float for session on till %s", till.Code)),
			JournalEntryID: &entry.ID,
			TransferredBy:  userID,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrTillNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("till not found"))
			return
		}
		if errors.Is(err, store.ErrSessionAlreadyOpen) {
			httpx.WriteErr(w, r, httpx.ErrConflict("till already has an open session"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, session)
}

type sessionDetailResp struct {
	Session   *domain.TillSession    `json:"session"`
	Transfers []domain.CashTransfer  `json:"transfers"`
}

func (h *CashHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var resp sessionDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		s, err := h.Cash.GetSessionTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp.Session = s
		resp.Transfers, err = h.Cash.ListTransfersTx(r.Context(), tx, &id, 100)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("session not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

type closeSessionReq struct {
	ActualClose string `json:"actual_close"`
	Notes       string `json:"notes,omitempty"`
}

func (h *CashHandler) CloseSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in closeSessionReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	actual, err := decimal.NewFromString(in.ActualClose)
	if err != nil || actual.IsNegative() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("actual_close must be a non-negative decimal"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var session *domain.TillSession
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		s, err := h.Cash.GetSessionTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if s.Status != domain.SessionOpen {
			return store.ErrSessionNotOpen
		}
		till, err := h.Cash.GetTillTx(r.Context(), tx, s.TillID)
		if err != nil {
			return err
		}

		variance := actual.Sub(s.ExpectedClose)
		var varianceJEID *uuid.UUID
		if !variance.IsZero() {
			// Post variance adjustment:
			//   variance < 0 (short): DR variance acct / CR till
			//   variance > 0 (over):  DR till / CR variance acct
			var lines []posting.Line
			if variance.IsNegative() {
				short := variance.Neg()
				lines = []posting.Line{
					{AccountCode: till.VarianceAccountCode, Debit: short, Narration: "Till short on close"},
					{AccountCode: till.GLAccountCode, Credit: short, Narration: "Till closed short"},
				}
			} else {
				over := variance
				lines = []posting.Line{
					{AccountCode: till.GLAccountCode, Debit: over, Narration: "Till closed over"},
					{AccountCode: till.VarianceAccountCode, Credit: over, Narration: "Cash over on till close"},
				}
			}
			entry, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
				EntryType:    domain.TypeAuto,
				SourceModule: "accounting.cash-management",
				SourceRef:    fmt.Sprintf("close-variance-%s", s.ID),
				Narration:    fmt.Sprintf("Till close variance for %s: expected %s, actual %s", till.Code, s.ExpectedClose.StringFixed(2), actual.StringFixed(2)),
				Lines:        lines,
				PostedBy:     &userID,
			})
			if err != nil {
				return fmt.Errorf("post variance: %w", err)
			}
			varianceJEID = &entry.ID
			_, err = h.Cash.CreateTransferTx(r.Context(), tx, store.CreateTransferInput{
				TransferType:   domain.TransferVarianceAdjustment,
				FromTillID:     &till.ID,
				SessionID:      &s.ID,
				Amount:         variance.Abs(),
				Narration:      strPtr(fmt.Sprintf("Variance %s on session close", variance.StringFixed(2))),
				JournalEntryID: &entry.ID,
				TransferredBy:  userID,
			})
			if err != nil {
				return err
			}
		}

		session, err = h.Cash.CloseSessionTx(r.Context(), tx, store.CloseSessionInput{
			SessionID: id, ActualClose: actual, Variance: variance,
			VarianceJournalEntryID: varianceJEID, ClosedBy: userID, Notes: in.Notes,
		})
		return err
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			httpx.WriteErr(w, r, httpx.ErrNotFound("session not found"))
		case errors.Is(err, store.ErrSessionNotOpen):
			httpx.WriteErr(w, r, httpx.ErrConflict("session is not open"))
		default:
			httpx.WriteErr(w, r, err)
		}
		return
	}
	httpx.OK(w, session)
}

// ─────────── Transfers ───────────

type transferReq struct {
	TransferType domain.CashTransferType `json:"transfer_type"`
	FromTillID   *uuid.UUID              `json:"from_till_id,omitempty"`
	ToTillID     *uuid.UUID              `json:"to_till_id,omitempty"`
	Amount       string                  `json:"amount"`
	Reference    string                  `json:"reference,omitempty"`
	Narration    string                  `json:"narration,omitempty"`
}

func (h *CashHandler) CreateTransfer(w http.ResponseWriter, r *http.Request) {
	var in transferReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	amount, err := decimal.NewFromString(in.Amount)
	if err != nil || !amount.IsPositive() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("amount must be positive"))
		return
	}
	// Validate the type + direction.
	switch in.TransferType {
	case domain.TransferVaultToTill:
		if in.ToTillID == nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("to_till_id required for vault_to_till"))
			return
		}
	case domain.TransferTillToVault:
		if in.FromTillID == nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("from_till_id required for till_to_vault"))
			return
		}
	case domain.TransferTillToTill:
		if in.FromTillID == nil || in.ToTillID == nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("from_till_id and to_till_id required for till_to_till"))
			return
		}
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("transfer_type must be one of vault_to_till, till_to_vault, till_to_till"))
		return
	}

	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	var transfer *domain.CashTransfer
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Resolve tills + figure out GL legs.
		var fromTill, toTill *domain.Till
		if in.FromTillID != nil {
			t, err := h.Cash.GetTillTx(r.Context(), tx, *in.FromTillID)
			if err != nil { return err }
			fromTill = t
		}
		if in.ToTillID != nil {
			t, err := h.Cash.GetTillTx(r.Context(), tx, *in.ToTillID)
			if err != nil { return err }
			toTill = t
		}

		// Build GL legs.
		var lines []posting.Line
		narr := in.Narration
		switch in.TransferType {
		case domain.TransferVaultToTill:
			if narr == "" { narr = "Vault → till " + toTill.Code }
			lines = []posting.Line{
				{AccountCode: toTill.GLAccountCode, Debit: amount, Narration: narr},
				{AccountCode: toTill.VaultAccountCode, Credit: amount, Narration: narr},
			}
		case domain.TransferTillToVault:
			if narr == "" { narr = "Till " + fromTill.Code + " → vault" }
			lines = []posting.Line{
				{AccountCode: fromTill.VaultAccountCode, Debit: amount, Narration: narr},
				{AccountCode: fromTill.GLAccountCode, Credit: amount, Narration: narr},
			}
		case domain.TransferTillToTill:
			// Same GL account — no journal needed. We still record the
			// operational movement.
		}

		// Optional active-session lookup for tracking session expected balances.
		var sessionID *uuid.UUID
		updateSessionDelta := func(tillID uuid.UUID, delta decimal.Decimal) error {
			cur, err := h.Cash.CurrentSessionTx(r.Context(), tx, tillID)
			if err != nil { return err }
			if cur != nil {
				if err := h.Cash.AdjustSessionExpectedTx(r.Context(), tx, cur.ID, delta); err != nil {
					return err
				}
				if sessionID == nil {
					id := cur.ID
					sessionID = &id
				}
			}
			return nil
		}
		switch in.TransferType {
		case domain.TransferVaultToTill:
			if err := updateSessionDelta(toTill.ID, amount); err != nil { return err }
		case domain.TransferTillToVault:
			if err := updateSessionDelta(fromTill.ID, amount.Neg()); err != nil { return err }
		case domain.TransferTillToTill:
			if err := updateSessionDelta(fromTill.ID, amount.Neg()); err != nil { return err }
			if err := updateSessionDelta(toTill.ID, amount); err != nil { return err }
		}

		// Post if there's GL impact.
		var journalEntryID *uuid.UUID
		if len(lines) > 0 {
			entry, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
				EntryType:    domain.TypeAuto,
				SourceModule: "accounting.cash-management",
				SourceRef:    fmt.Sprintf("%s-%s", in.TransferType, uuid.New()),
				Narration:    narr,
				Lines:        lines,
				PostedBy:     &userID,
			})
			if err != nil {
				return fmt.Errorf("post transfer: %w", err)
			}
			journalEntryID = &entry.ID
		}

		transfer, err = h.Cash.CreateTransferTx(r.Context(), tx, store.CreateTransferInput{
			TransferType:   in.TransferType,
			FromTillID:     in.FromTillID,
			ToTillID:       in.ToTillID,
			SessionID:      sessionID,
			Amount:         amount,
			Reference:      strPtr(in.Reference),
			Narration:      strPtr(in.Narration),
			JournalEntryID: journalEntryID,
			TransferredBy:  userID,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrTillNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("till not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, transfer)
}

func (h *CashHandler) ListTransfers(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.CashTransfer
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Cash.ListTransfersTx(r.Context(), tx, nil, limit)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

// ─────────── Cash position ───────────

func (h *CashHandler) CashPosition(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var pos *store.CashPosition
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		pos, err = h.Cash.CashPositionTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, pos)
}
