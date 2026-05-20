// Member status transition matrix + per-status action gating.
//
// The matrix encodes which (from → to) transitions are legal and
// whether they're "sensitive" — i.e. must run through the approval
// workflow engine instead of applying immediately. Action gating is
// the read side: "can this member currently do X?" Both live in the
// domain package so any host module (loans, savings, withdrawals) can
// consume them without taking a dependency on the member service.

package domain

import "fmt"

// Action codes the platform may gate by status. Add new codes here as
// modules ship; CanPerform falls through to "allowed" for unknown codes
// so we never silently block a new feature by forgetting to map it.
type Action string

const (
	ActionApplyLoan       Action = "apply_loan"
	ActionDeposit         Action = "deposit"
	ActionWithdraw        Action = "withdraw"
	ActionBuyShares       Action = "buy_shares"
	ActionTransferShares  Action = "transfer_shares"
	ActionBecomeGuarantor Action = "become_guarantor"
	ActionUpdateKYC       Action = "update_kyc"
	ActionPortalLogin     Action = "portal_login"
	ActionReceiveDividend Action = "receive_dividend"
	ActionLoanRepayment   Action = "loan_repayment" // payments are always allowed
)

// Transition holds the metadata for a single legal transition.
type Transition struct {
	From      MemberStatus
	To        MemberStatus
	Sensitive bool   // routes through workflow if true
	Note      string // human-readable description for UIs
}

// transitions is the canonical legal matrix. Anything not here is
// rejected by ValidateTransition.
var transitions = []Transition{
	// Onboarding outcomes (already handled by /approve and /reject).
	{From: StatusPending, To: StatusActive, Sensitive: false, Note: "Approve onboarding"},
	{From: StatusPending, To: StatusRejected, Sensitive: false, Note: "Reject onboarding"},
	{From: StatusPending, To: StatusExited, Sensitive: true, Note: "Withdraw application"},
	{From: StatusPending, To: StatusDeceased, Sensitive: true, Note: "Death during onboarding"},

	// Active → everywhere except pending / rejected.
	{From: StatusActive, To: StatusDormant, Sensitive: false, Note: "Inactivity threshold reached"},
	{From: StatusActive, To: StatusSuspended, Sensitive: true, Note: "Suspend (compliance / disciplinary)"},
	{From: StatusActive, To: StatusBlacklisted, Sensitive: true, Note: "Blacklist (fraud / regulatory)"},
	{From: StatusActive, To: StatusExited, Sensitive: true, Note: "Voluntary or administrative exit"},
	{From: StatusActive, To: StatusDeceased, Sensitive: true, Note: "Death notification"},

	// Dormant → active (reactivation) or terminal states.
	{From: StatusDormant, To: StatusActive, Sensitive: false, Note: "Reactivate from dormancy"},
	{From: StatusDormant, To: StatusSuspended, Sensitive: true, Note: "Suspend a dormant member"},
	{From: StatusDormant, To: StatusBlacklisted, Sensitive: true, Note: "Blacklist a dormant member"},
	{From: StatusDormant, To: StatusExited, Sensitive: true, Note: "Exit a dormant member"},
	{From: StatusDormant, To: StatusDeceased, Sensitive: true, Note: "Death of a dormant member"},

	// Suspended → active or terminal.
	{From: StatusSuspended, To: StatusActive, Sensitive: true, Note: "Reinstate after suspension"},
	{From: StatusSuspended, To: StatusBlacklisted, Sensitive: true, Note: "Escalate to blacklist"},
	{From: StatusSuspended, To: StatusExited, Sensitive: true, Note: "Exit while suspended"},
	{From: StatusSuspended, To: StatusDeceased, Sensitive: true, Note: "Death while suspended"},

	// Blacklisted → active (requires board approval); also to deceased.
	{From: StatusBlacklisted, To: StatusActive, Sensitive: true, Note: "Reinstate from blacklist (board approval)"},
	{From: StatusBlacklisted, To: StatusDeceased, Sensitive: true, Note: "Death of a blacklisted member"},

	// Exited → deceased only (no reactivation; create a new member instead).
	{From: StatusExited, To: StatusDeceased, Sensitive: true, Note: "Death of an exited member"},

	// Rejected is terminal — to reapply, create a new member record.

	// Deceased is terminal — no transitions out.
}

// ValidateTransition returns nil when (from → to) is legal, or an
// explanatory error otherwise.
func ValidateTransition(from, to MemberStatus) error {
	if from == to {
		return fmt.Errorf("cannot transition to the same status")
	}
	if from == "" {
		return fmt.Errorf("from status is required")
	}
	if _, ok := lookupTransition(from, to); !ok {
		return fmt.Errorf("transition %s → %s is not allowed", from, to)
	}
	return nil
}

// IsSensitive reports whether the transition must go through the
// approval workflow engine. Returns false for unknown transitions.
func IsSensitive(from, to MemberStatus) bool {
	t, ok := lookupTransition(from, to)
	if !ok {
		return false
	}
	return t.Sensitive
}

// AllowedTransitionsFrom is what the UI calls to render the "Change
// status" dropdown for the current state.
func AllowedTransitionsFrom(from MemberStatus) []Transition {
	out := make([]Transition, 0, 4)
	for _, t := range transitions {
		if t.From == from {
			out = append(out, t)
		}
	}
	return out
}

// CanPerform returns whether a member in the given status may perform
// the given action. The defaults are deliberately conservative: only
// `active` members can do everything; payments toward existing loans
// are allowed regardless because the system shouldn't block someone
// from clearing arrears.
func CanPerform(status MemberStatus, action Action) bool {
	// Loan repayments + KYC updates are always allowed so members can
	// fix the very things that often unblock them.
	switch action {
	case ActionLoanRepayment, ActionUpdateKYC:
		return true
	}

	switch status {
	case StatusActive:
		return true

	case StatusPending:
		// Mid-onboarding members can update their KYC but not transact.
		return false

	case StatusDormant:
		// Reads-only by default. Deposits permitted (reactivation path),
		// but withdrawals / new loans blocked.
		switch action {
		case ActionDeposit:
			return true
		}
		return false

	case StatusSuspended:
		// Temporary block. Nothing initiated, deposits allowed so
		// members can clear obligations.
		switch action {
		case ActionDeposit:
			return true
		}
		return false

	case StatusBlacklisted, StatusExited, StatusDeceased, StatusRejected:
		// Hard stops. Nothing.
		return false
	}
	return false
}

// Visibility describes whether members in this status appear in
// standard "active register" lists or are filtered to admin-only views.
type Visibility string

const (
	VisibilityNormal   Visibility = "normal"     // shown everywhere
	VisibilityRestricted Visibility = "restricted" // only in admin / compliance views
	VisibilityArchive  Visibility = "archive"    // archived; opt-in to see
)

func VisibilityFor(status MemberStatus) Visibility {
	switch status {
	case StatusActive, StatusDormant, StatusPending:
		return VisibilityNormal
	case StatusSuspended, StatusBlacklisted:
		return VisibilityRestricted
	}
	return VisibilityArchive
}

// SystemBehavior is a human-readable summary used by the admin UI to
// explain "what does the system actually do with this member's
// accounts/shares/loans while they're in this status?" Keep terse — the
// detailed enforcement happens in CanPerform + per-module hooks.
func SystemBehavior(status MemberStatus) string {
	switch status {
	case StatusPending:
		return "Profile read-only. No transactions, no products. Cannot guarantee loans."
	case StatusActive:
		return "Full participation: savings, loans, shares, dividends, guarantorship."
	case StatusDormant:
		return "Deposits accepted; withdrawals + new loans blocked. Shares + balances retained. Reactivation requires a deposit + updated KYC."
	case StatusSuspended:
		return "All initiated transactions blocked. Existing loans accrue. Deposits permitted so the member can clear obligations. Review-date driven."
	case StatusBlacklisted:
		return "All activity hard-stopped. Cannot reapply, transact, or be a guarantor. Reinstatement requires board approval."
	case StatusExited:
		return "Final reconciliation done. Shares + savings disbursed. Loans cleared. Record retained for audit."
	case StatusDeceased:
		return "All activity frozen. Beneficiary notification fired. Estate claim workflow drives final disbursement."
	case StatusRejected:
		return "Onboarding rejected. To reapply the member must be onboarded as a new record."
	}
	return ""
}

func lookupTransition(from, to MemberStatus) (Transition, bool) {
	for _, t := range transitions {
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return Transition{}, false
}
