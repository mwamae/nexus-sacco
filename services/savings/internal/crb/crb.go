// Package crb — Credit Reference Bureau adapter interface + stub
// implementations for Metropol, TransUnion, and CRB Africa.
//
// IMPORTANT: the three real vendor adapters are intentionally stubs
// in this PR. Real wire-protocol clients need vendor sandbox
// credentials + current API docs (Metropol uses public/private key,
// TransUnion is OAuth2, CRB Africa is basic auth — confirm at
// integration time). Stubs return deterministic synthetic data so the
// surrounding flow (consent capture, response storage, score
// normalisation, application annotation, UI rendering) is exerciseable
// end-to-end in dev + sandbox tenants.
//
// Wiring real vendor calls: replace the stub method body with the
// HTTP client; the interface + normalisation layer + handler code
// don't change.

package crb

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// Provider is the per-CRB adapter contract. Pull returns a normalised
// Report; vendor-specific response shapes are reduced inside the
// adapter.
type Provider interface {
	Name() string
	Pull(ctx context.Context, in PullInput) (*Report, error)
}

type PullInput struct {
	NationalID string
	FullName   string
	Phone      string
}

type Listing struct {
	Lender     string          `json:"lender"`
	Type       string          `json:"type"`        // default | restructure | court_listing
	AmountKES  decimal.Decimal `json:"amount_kes"`
	DateListed time.Time       `json:"date_listed"`
	Status     string          `json:"status"`      // open | cleared
}

type Enquiry struct {
	Lender      string    `json:"lender"`
	EnquiryDate time.Time `json:"enquiry_date"`
	Purpose     string    `json:"purpose"`
}

type Report struct {
	Score              int             `json:"score"`               // 200-900 normalised
	Rating             string          `json:"rating"`              // A | B | C | D | E
	Listings           []Listing       `json:"listings"`
	Enquiries          []Enquiry       `json:"enquiries"`
	ActiveCredit       decimal.Decimal `json:"active_credit"`
	OutstandingBalance decimal.Decimal `json:"outstanding_balance"`
	Sandbox            bool            `json:"sandbox"`             // true when produced by a stub
	RawResponse        json.RawMessage `json:"raw_response"`
}

// Creds is the JSON shape we encrypt + store per (tenant, provider).
// Real adapters use whichever subset they need.
type Creds struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key,omitempty"`
}

// ─────────── Stubs ───────────
//
// Each stub returns deterministic data derived from the national ID
// so the same applicant gets the same score across pulls — useful for
// dev demos. Wire real adapters by replacing the body of Pull.

type stubBase struct {
	name    string
	rng     func(string) (int, int, int) // (score, listings_count, enquiries_count)
	creds   Creds
	httpx   *http.Client
}

func (s *stubBase) Name() string { return s.name }

func (s *stubBase) Pull(_ context.Context, in PullInput) (*Report, error) {
	if in.NationalID == "" {
		return nil, fmt.Errorf("national_id is required")
	}
	score, listings, enquiries := s.rng(in.NationalID)
	rating := normaliseRating(score)
	rep := &Report{
		Score:              score,
		Rating:             rating,
		ActiveCredit:       decimal.NewFromInt(int64((score * 1000) % 250000)),
		OutstandingBalance: decimal.NewFromInt(int64((score * 137) % 90000)),
		Sandbox:            true,
	}
	for i := 0; i < listings; i++ {
		rep.Listings = append(rep.Listings, Listing{
			Lender:     fmt.Sprintf("Sandbox Lender %d", i+1),
			Type:       []string{"default", "restructure", "court_listing"}[i%3],
			AmountKES:  decimal.NewFromInt(int64(5000 * (i + 1))),
			DateListed: time.Now().AddDate(0, -((i + 1) * 4), 0),
			Status:     []string{"open", "cleared"}[i%2],
		})
	}
	for i := 0; i < enquiries; i++ {
		rep.Enquiries = append(rep.Enquiries, Enquiry{
			Lender:      fmt.Sprintf("Sandbox Lender %d", i+1),
			EnquiryDate: time.Now().AddDate(0, 0, -(i+1)*15),
			Purpose:     "loan_application",
		})
	}
	rep.RawResponse, _ = json.Marshal(map[string]any{
		"provider": s.name,
		"sandbox":  true,
		"score":    score,
		"rating":   rating,
		"input":    in,
	})
	return rep, nil
}

// rngFromID — deterministic per-national-ID score generator.
// score in [350, 850]; listings_count in [0, 3]; enquiries_count in [0, 5].
func rngFromID(id string) (int, int, int) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	v := h.Sum32()
	score := 350 + int(v%501)
	listings := int(v%7) - 4
	if listings < 0 {
		listings = 0
	}
	enquiries := int((v / 7) % 6)
	return score, listings, enquiries
}

func normaliseRating(score int) string {
	switch {
	case score >= 780:
		return "A"
	case score >= 700:
		return "B"
	case score >= 620:
		return "C"
	case score >= 540:
		return "D"
	default:
		return "E"
	}
}

// Metropol — public/private key auth in real adapter.
func NewMetropolStub(creds Creds) Provider {
	return &stubBase{name: "metropol", rng: rngFromID, creds: creds,
		httpx: &http.Client{Timeout: 8 * time.Second}}
}

// TransUnion — OAuth2 client_credentials in real adapter.
func NewTransUnionStub(creds Creds) Provider {
	return &stubBase{name: "transunion", rng: func(id string) (int, int, int) {
		// TU tends to score slightly lower than Metropol on the same
		// borrower in real life — shift the stub to match the demo.
		s, l, e := rngFromID(id)
		s -= 20
		if s < 300 {
			s = 300
		}
		return s, l, e
	}, creds: creds, httpx: &http.Client{Timeout: 8 * time.Second}}
}

// CRB Africa — basic auth in real adapter.
func NewCRBAfricaStub(creds Creds) Provider {
	return &stubBase{name: "crb_africa", rng: func(id string) (int, int, int) {
		s, _, e := rngFromID(id)
		// CRB Africa tends to over-report listings.
		_, _ = s, e
		listings := (int(s) % 5)
		return s, listings, e
	}, creds: creds, httpx: &http.Client{Timeout: 8 * time.Second}}
}

// NewProvider — factory dispatching by code. Returns a stub today.
// When real adapters land, switch on creds.sandbox to choose stub vs
// HTTP client.
func NewProvider(code string, creds Creds) (Provider, error) {
	switch code {
	case "metropol":
		return NewMetropolStub(creds), nil
	case "transunion":
		return NewTransUnionStub(creds), nil
	case "crb_africa":
		return NewCRBAfricaStub(creds), nil
	}
	return nil, fmt.Errorf("crb: unknown provider %q", code)
}
