// Package insurance — credit-life insurance adapter interface +
// stubs for Britam / APA / Jubilee / CIC.
//
// Stub adapters return a deterministic synthetic policy_no so the
// disbursement-with-insurance path is exerciseable end-to-end without
// vendor sandbox credentials. Wire real vendors by replacing the
// CreatePolicy body of each adapter.

package insurance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Provider is the per-insurer contract. CreatePolicy issues a credit-
// life policy for a freshly-disbursed loan. Returns the policy_no
// assigned by the insurer (synthetic in sandbox mode).
type Provider interface {
	Name() string
	CreatePolicy(ctx context.Context, in PolicyInput) (*PolicyResult, error)
}

type PolicyInput struct {
	LoanID         uuid.UUID
	MemberName     string
	NationalID     string
	Phone          string
	PrincipalAmount decimal.Decimal
	TermMonths     int
	EffectiveFrom  time.Time
	EffectiveTo    time.Time
}

type PolicyResult struct {
	PolicyNo       string                 `json:"policy_no"`
	CoverageAmount decimal.Decimal        `json:"coverage_amount"`
	Sandbox        bool                   `json:"sandbox"`
	VendorResponse map[string]any         `json:"vendor_response"`
}

// Creds is the per-(tenant, provider) credential shape, decrypted at
// adapter-creation time from insurance_providers.credentials_ciphertext.
type Creds struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	BaseURL      string `json:"base_url"`
}

// QuotePremium computes the premium for a loan given the provider's
// rate + min/max caps. Pure function — same inputs always produce
// the same output.
//
//   premium = principal × rate%
//   premium = MAX(premium, min_premium)
//   premium = MIN(premium, max_premium) [when max_premium != nil]
func QuotePremium(principal, ratePct, minPremium decimal.Decimal, maxPremium *decimal.Decimal) decimal.Decimal {
	premium := principal.Mul(ratePct).Div(decimal.NewFromInt(100)).Round(2)
	if premium.LessThan(minPremium) {
		premium = minPremium
	}
	if maxPremium != nil && premium.GreaterThan(*maxPremium) {
		premium = *maxPremium
	}
	return premium
}

// ─────────── Stubs ───────────

type stubBase struct {
	name  string
	creds Creds
}

func (s *stubBase) Name() string { return s.name }

func (s *stubBase) CreatePolicy(_ context.Context, in PolicyInput) (*PolicyResult, error) {
	policyNo, err := synthPolicyNo(s.name)
	if err != nil {
		return nil, err
	}
	return &PolicyResult{
		PolicyNo:       policyNo,
		CoverageAmount: in.PrincipalAmount, // 1:1 cover for credit-life
		Sandbox:        true,
		VendorResponse: map[string]any{
			"provider": s.name,
			"sandbox":  true,
			"policy_no": policyNo,
			"loan_id":  in.LoanID,
		},
	}, nil
}

func synthPolicyNo(provider string) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	prefix := provider
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	return fmt.Sprintf("%s-%s-%s",
		prefix,
		time.Now().UTC().Format("200601"),
		hex.EncodeToString(b),
	), nil
}

func NewBritamStub(creds Creds) Provider   { return &stubBase{name: "britam", creds: creds} }
func NewAPAStub(creds Creds) Provider      { return &stubBase{name: "apa", creds: creds} }
func NewJubileeStub(creds Creds) Provider  { return &stubBase{name: "jubilee", creds: creds} }
func NewCICStub(creds Creds) Provider      { return &stubBase{name: "cic", creds: creds} }
func NewCustomStub(creds Creds) Provider   { return &stubBase{name: "custom", creds: creds} }

func NewProvider(code string, creds Creds) (Provider, error) {
	switch code {
	case "britam":
		return NewBritamStub(creds), nil
	case "apa":
		return NewAPAStub(creds), nil
	case "jubilee":
		return NewJubileeStub(creds), nil
	case "cic":
		return NewCICStub(creds), nil
	case "custom":
		return NewCustomStub(creds), nil
	}
	return nil, fmt.Errorf("insurance: unknown provider code %q", code)
}
