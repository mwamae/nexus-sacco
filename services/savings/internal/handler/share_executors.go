// Maker-checker executors for share actions (stage 2).
//
// Each executor takes a typed payload + maker user id, performs the
// same validations and ledger writes as the original Purchase /
// Transfer / BonusIssue / PlaceLien inline path, and returns the
// resulting transaction id (or, for bonus issue, a count of impacted
// accounts). Share redemption is not a supported operation in this
// SACCO and has no executor — see share.go for the rationale.
//
// Handlers call MaybeQueue / Execute via the gate; the approval
// dispatcher in pending_approvals.go also calls these executors when
// replaying a queued action.

package handler

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/store"
)

// ─────────── Payloads ───────────

type SharePurchasePayload struct {
	CounterpartyID       uuid.UUID             `json:"counterparty_id"`
	Shares         int                   `json:"shares"`
	PaymentChannel domain.PaymentChannel `json:"payment_channel"`
	PaymentRef     string                `json:"payment_ref"`
	Narration      string                `json:"narration"`
}

type ShareTransferPayload struct {
	FromMemberID uuid.UUID `json:"from_member_id"`
	ToMemberID   uuid.UUID `json:"to_member_id"`
	Shares       int       `json:"shares"`
	Narration    string    `json:"narration"`
	Reason       string    `json:"reason"`
}

type ShareBonusPayload struct {
	PctOfHolding decimal.Decimal `json:"pct_of_holding"`
	Reason       string          `json:"reason"`
}

type ShareLienPayload struct {
	CounterpartyID      uuid.UUID `json:"counterparty_id"`
	Shares        int       `json:"shares"`
	Reason        string    `json:"reason"`
	ReferenceKind string    `json:"reference_kind"`
	ReferenceID   string    `json:"reference_id"`
}

// ─────────── Result shapes ───────────

type SharePostResult struct {
	Transaction domain.ShareTransaction  `json:"transaction"`
	Account     domain.ShareAccount      `json:"account"`
	Certificate *domain.ShareCertificate `json:"certificate,omitempty"`
}

type ShareTransferResult struct {
	From SharePostResult `json:"from"`
	To   SharePostResult `json:"to"`
}

type ShareBonusResult struct {
	IssuedToCount    int             `json:"issued_to_count"`
	TotalBonusShares int             `json:"total_bonus_shares"`
	PctApplied       decimal.Decimal `json:"pct_applied"`
}

// ─────────── Executors ───────────

func (h *ShareHandler) ExecuteSharePurchaseTx(
	ctx context.Context, tx pgx.Tx,
	p SharePurchasePayload, makerID uuid.UUID,
) (*SharePostResult, error) {
	policy, member, acct, err := h.loadContext(ctx, tx, p.CounterpartyID, true)
	if err != nil {
		return nil, err
	}
	if err := requireWriteEligible(member, "buy"); err != nil {
		return nil, err
	}
	if acct.Status != domain.AccountActive {
		return nil, domain.ErrAccountClosed
	}
	// Max-holding cap.
	if policy.MaxSharesPctOfCapital.GreaterThan(decimal.Zero) {
		total, err := h.Shares.TotalSharesIssuedTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		newMember := acct.SharesHeld + p.Shares
		newTotal := total + p.Shares
		if newTotal > 0 {
			pct := decimal.NewFromInt(int64(newMember)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(newTotal)))
			if pct.GreaterThan(policy.MaxSharesPctOfCapital) {
				return nil, domain.ErrExceedsMaxHolding
			}
		}
	}
	ch := p.PaymentChannel
	ref := strNilIfEmpty(p.PaymentRef)
	narr := strNilIfEmpty(p.Narration)
	txn, err := h.Shares.PostTxnTx(ctx, tx, store.PostInput{
		Account:        acct,
		TxnType:        domain.TxnPurchase,
		SharesDelta:    p.Shares,
		ParValueAtTxn:  policy.ParValue,
		PaymentChannel: &ch,
		PaymentRef:     ref,
		Narration:      narr,
		InitiatedBy:    makerID,
	})
	if err != nil {
		return nil, err
	}
	updated, err := h.Shares.GetAccountTx(ctx, tx, acct.ID)
	if err != nil {
		return nil, err
	}
	cert, err := h.Shares.IssueCertificateTx(ctx, tx, acct.ID, acct.CounterpartyID, makerID,
		updated.SharesHeld, policy.ParValue, policy.CertificatePrefix)
	if err != nil {
		return nil, err
	}
	_ = h.Counterparties.TouchActivityTx(ctx, tx, p.CounterpartyID)
	return &SharePostResult{Transaction: *txn, Account: *updated, Certificate: cert}, nil
}

// (Redeem executor removed — see comment in share.go: share capital
// cannot be redeemed; exiting members must transfer their shares to
// another active member via the Transfer executor below.)

func (h *ShareHandler) ExecuteShareTransferTx(
	ctx context.Context, tx pgx.Tx,
	p ShareTransferPayload, makerID uuid.UUID,
) (*ShareTransferResult, error) {
	policy, fromMember, fromAcct, err := h.loadContext(ctx, tx, p.FromMemberID, false)
	if err != nil {
		return nil, err
	}
	if err := requireWriteEligible(fromMember, "transfer"); err != nil {
		return nil, err
	}
	if domain.AvailableShares(fromAcct) < p.Shares {
		if fromAcct.SharesPledged > 0 {
			return nil, domain.ErrLienBlocksAction
		}
		return nil, domain.ErrInsufficientShares
	}
	if fromAcct.SharesHeld-p.Shares < policy.MinSharesRequired {
		return nil, domain.ErrBelowMinHolding
	}
	_, toMember, toAcct, err := h.loadContext(ctx, tx, p.ToMemberID, true)
	if err != nil {
		return nil, err
	}
	if err := requireWriteEligible(toMember, "receive"); err != nil {
		return nil, err
	}
	if policy.MaxSharesPctOfCapital.GreaterThan(decimal.Zero) {
		total, err := h.Shares.TotalSharesIssuedTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		if total > 0 {
			pct := decimal.NewFromInt(int64(toAcct.SharesHeld + p.Shares)).
				Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(total)))
			if pct.GreaterThan(policy.MaxSharesPctOfCapital) {
				return nil, domain.ErrExceedsMaxHolding
			}
		}
	}
	internal := domain.ChannelInternal
	narration := p.Narration
	if narration == "" {
		narration = "Share transfer between members"
	}
	narr := &narration
	reason := strNilIfEmpty(p.Reason)

	outTxn, err := h.Shares.PostTxnTx(ctx, tx, store.PostInput{
		Account:             fromAcct,
		TxnType:             domain.TxnTransferOut,
		SharesDelta:         -p.Shares,
		ParValueAtTxn:       policy.ParValue,
		PaymentChannel:      &internal,
		Narration:           narr,
		CounterpartyAccount: toAcct,
		InitiatedBy:         makerID,
		AuthorizedBy:        &makerID,
		AuthorizationReason: reason,
	})
	if err != nil {
		return nil, err
	}
	fromAcct, err = h.Shares.GetAccountTx(ctx, tx, fromAcct.ID)
	if err != nil {
		return nil, err
	}
	inTxn, err := h.Shares.PostTxnTx(ctx, tx, store.PostInput{
		Account:             toAcct,
		TxnType:             domain.TxnTransferIn,
		SharesDelta:         p.Shares,
		ParValueAtTxn:       policy.ParValue,
		PaymentChannel:      &internal,
		Narration:           narr,
		CounterpartyAccount: fromAcct,
		CounterpartyTxnID:   &outTxn.ID,
		InitiatedBy:         makerID,
		AuthorizedBy:        &makerID,
		AuthorizationReason: reason,
	})
	if err != nil {
		return nil, err
	}
	if err := h.Shares.LinkCounterpartyTxnTx(ctx, tx, outTxn.ID, inTxn.ID); err != nil {
		return nil, err
	}
	toAcct, err = h.Shares.GetAccountTx(ctx, tx, toAcct.ID)
	if err != nil {
		return nil, err
	}
	fromCert, err := h.Shares.IssueCertificateTx(ctx, tx, fromAcct.ID, fromAcct.CounterpartyID, makerID,
		fromAcct.SharesHeld, policy.ParValue, policy.CertificatePrefix)
	if err != nil {
		return nil, err
	}
	toCert, err := h.Shares.IssueCertificateTx(ctx, tx, toAcct.ID, toAcct.CounterpartyID, makerID,
		toAcct.SharesHeld, policy.ParValue, policy.CertificatePrefix)
	if err != nil {
		return nil, err
	}
	_ = h.Counterparties.TouchActivityTx(ctx, tx, p.FromMemberID)
	_ = h.Counterparties.TouchActivityTx(ctx, tx, p.ToMemberID)
	return &ShareTransferResult{
		From: SharePostResult{Transaction: *outTxn, Account: *fromAcct, Certificate: fromCert},
		To:   SharePostResult{Transaction: *inTxn, Account: *toAcct, Certificate: toCert},
	}, nil
}

func (h *ShareHandler) ExecuteShareBonusTx(
	ctx context.Context, tx pgx.Tx,
	p ShareBonusPayload, makerID uuid.UUID,
) (*ShareBonusResult, error) {
	policy, err := h.Tenants.SharePolicyTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	accounts, err := h.Shares.ActiveAccountsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	internal := domain.ChannelInternal
	resp := &ShareBonusResult{PctApplied: p.PctOfHolding}
	for i := range accounts {
		a := accounts[i]
		bonus := p.PctOfHolding.
			Div(decimal.NewFromInt(100)).
			Mul(decimal.NewFromInt(int64(a.SharesHeld))).
			Floor()
		n := int(bonus.IntPart())
		if n <= 0 {
			continue
		}
		reason := p.Reason
		if _, err := h.Shares.PostTxnTx(ctx, tx, store.PostInput{
			Account:             &a,
			TxnType:             domain.TxnBonusIssue,
			SharesDelta:         n,
			ParValueAtTxn:       policy.ParValue,
			PaymentChannel:      &internal,
			Narration:           &reason,
			InitiatedBy:         makerID,
			AuthorizedBy:        &makerID,
			AuthorizationReason: &reason,
		}); err != nil {
			return nil, err
		}
		updated, err := h.Shares.GetAccountTx(ctx, tx, a.ID)
		if err != nil {
			return nil, err
		}
		if _, err := h.Shares.IssueCertificateTx(ctx, tx, a.ID, a.CounterpartyID, makerID,
			updated.SharesHeld, policy.ParValue, policy.CertificatePrefix); err != nil {
			return nil, err
		}
		resp.IssuedToCount++
		resp.TotalBonusShares += n
	}
	return resp, nil
}

func (h *ShareHandler) ExecuteShareLienTx(
	ctx context.Context, tx pgx.Tx,
	p ShareLienPayload, makerID uuid.UUID,
) (*domain.ShareLien, error) {
	acct, err := h.Shares.GetAccountByCounterpartyTx(ctx, tx, p.CounterpartyID)
	if err != nil {
		return nil, err
	}
	return h.Shares.PlaceLienTx(ctx, tx, acct.ID, p.Shares, p.Reason,
		strNilIfEmpty(p.ReferenceKind), strNilIfEmpty(p.ReferenceID), makerID)
}
