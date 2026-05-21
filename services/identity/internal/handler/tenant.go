// Platform-admin tenant endpoints + tenant self-info.

package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/db"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/httpx"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/identity/internal/store"
)

type TenantHandler struct {
	DB      *db.Pool
	Tenants *store.TenantStore
	Users   *store.UserStore
	Roles   *store.RoleStore
	Invites *store.InviteStore
	Audit   *store.AuditStore
	Logger  *slog.Logger

	// InviteTTL is mirrored from UserHandler so the platform-side
	// invite endpoint (TenantHandler.InviteUser) can stamp the same
	// expiry as the tenant-side one.
	InviteTTL time.Duration

	// UserH lets the platform-side contact/staff endpoints reuse the
	// existing role-resolution + invite-email helpers on UserHandler
	// without duplicating them.
	UserH *UserHandler

	// AuthH is reached for password-reset issuance — the platform
	// "force reset" action delegates to AuthHandler.IssuePasswordResetFor
	// so the token rules + session revocation stay in one place.
	AuthH *AuthHandler
}

var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{1,38}[a-z0-9])?$`)

// ─────────── POST /v1/platform/tenants ───────────
//
// Creates a new tenant AND its initial owner user atomically.
// Requires a platform-admin token (enforced by middleware).

type branchDTO struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	County          string `json:"county"`
	SubCounty       string `json:"sub_county"`
	PhysicalAddress string `json:"physical_address"`
	Phone           string `json:"phone"`
}

type contactDTO struct {
	FullName string `json:"full_name"`
	Title    string `json:"title"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
}

type createTenantRequest struct {
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	LegalName      string `json:"legal_name"`
	Kind           string `json:"kind"`
	CountryCode    string `json:"country_code"`
	CurrencyCode   string `json:"currency_code"`
	LicenseNo      string `json:"license_no"`
	RegistrationNo string `json:"registration_no"`
	TaxPIN         string `json:"tax_pin"`
	BillingPlan    string `json:"billing_plan"`

	OwnerEmail    string `json:"owner_email"`
	OwnerName     string `json:"owner_name"`
	OwnerPhone    string `json:"owner_phone"`
	OwnerPassword string `json:"owner_password"`

	Branches []branchDTO  `json:"branches"`
	Contacts []contactDTO `json:"contacts"`
}

type createTenantResponse struct {
	Tenant   *domain.Tenant         `json:"tenant"`
	Owner    *domain.User           `json:"owner"`
	Branches []*domain.TenantBranch `json:"branches,omitempty"`
	Contacts []*domain.TenantContact `json:"contacts,omitempty"`
}

func (h *TenantHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	req.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	req.OwnerEmail = strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	if !slugRE.MatchString(req.Slug) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("slug must be 3-40 chars, lowercase, [a-z0-9-]"))
		return
	}
	if req.Name == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("name is required"))
		return
	}
	if req.OwnerEmail == "" || req.OwnerName == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("owner_email and owner_name are required"))
		return
	}
	// owner_password is optional now. When omitted, the owner is
	// provisioned in pending state and gets an invitation email — the
	// "primary contact" flow from the spec. The platform admin never
	// sets passwords for tenant users.
	if req.OwnerPassword != "" && len(req.OwnerPassword) < 12 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("owner_password must be at least 12 characters when provided"))
		return
	}
	if req.Kind == "" {
		req.Kind = string(domain.TenantKindSACCO)
	}
	if !validTenantKind(req.Kind) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid kind"))
		return
	}
	if req.CountryCode == "" {
		req.CountryCode = "KE"
	}
	if req.CurrencyCode == "" {
		req.CurrencyCode = "KES"
	}
	plan := domain.BillingPlan(strings.ToLower(strings.TrimSpace(req.BillingPlan)))
	switch plan {
	case "", domain.BillingStarter, domain.BillingStandard, domain.BillingPremium, domain.BillingEnterprise:
		// ok (empty falls back in the store)
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid billing_plan"))
		return
	}

	// Branch / contact validation up front so we fail before inserting anything.
	branches := make([]store.BranchInput, 0, len(req.Branches))
	seenCodes := map[string]struct{}{}
	for i, b := range req.Branches {
		code := strings.ToUpper(strings.TrimSpace(b.Code))
		name := strings.TrimSpace(b.Name)
		if code == "" || name == "" {
			httpx.WriteErr(w, r, httpx.ErrBadRequest(fmt.Sprintf("branch %d: code and name are required", i+1)))
			return
		}
		if _, dup := seenCodes[code]; dup {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("duplicate branch code: "+code))
			return
		}
		seenCodes[code] = struct{}{}
		kind := domain.BranchKind(strings.ToLower(strings.TrimSpace(b.Kind)))
		switch kind {
		case "", domain.BranchHQ, domain.BranchBranch, domain.BranchAgency:
			// ok
		default:
			httpx.WriteErr(w, r, httpx.ErrBadRequest("branch "+code+": kind must be hq, branch, or agency"))
			return
		}
		branches = append(branches, store.BranchInput{
			Code: code, Name: name, Kind: kind,
			County: strings.TrimSpace(b.County), SubCounty: strings.TrimSpace(b.SubCounty),
			PhysicalAddress: strings.TrimSpace(b.PhysicalAddress), Phone: strings.TrimSpace(b.Phone),
		})
	}

	contacts := make([]store.ContactInput, 0, len(req.Contacts))
	for i, c := range req.Contacts {
		name := strings.TrimSpace(c.FullName)
		if name == "" {
			httpx.WriteErr(w, r, httpx.ErrBadRequest(fmt.Sprintf("contact %d: full_name is required", i+1)))
			return
		}
		contacts = append(contacts, store.ContactInput{
			FullName: name, Title: strings.TrimSpace(c.Title),
			Email: strings.ToLower(strings.TrimSpace(c.Email)),
			Phone: strings.TrimSpace(c.Phone),
		})
	}

	t, err := h.Tenants.Create(r.Context(), store.CreateTenantInput{
		Slug:           req.Slug,
		Name:           req.Name,
		LegalName:      req.LegalName,
		Kind:           domain.TenantKind(req.Kind),
		CountryCode:    strings.ToUpper(req.CountryCode),
		CurrencyCode:   strings.ToUpper(req.CurrencyCode),
		LicenseNo:      req.LicenseNo,
		RegistrationNo: req.RegistrationNo,
		TaxPIN:         strings.ToUpper(req.TaxPIN),
		BillingPlan:    plan,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.WriteErr(w, r, httpx.ErrConflict("tenant slug already taken"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}

	ownerRole, err := h.Roles.SystemRoleByCode(r.Context(), "tenant_owner")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// If the platform admin supplied a password, the owner starts
	// Active immediately. Otherwise the owner is Pending and we issue
	// a single-use invite token the user redeems to set their own
	// password (the canonical "primary contact" flow from the spec).
	var (
		passwordHash string
		ownerStatus  = domain.UserStatusPending
		rawInvite    string
	)
	if req.OwnerPassword != "" {
		ph, herr := auth.HashPassword(req.OwnerPassword)
		if herr != nil {
			httpx.WriteErr(w, r, herr)
			return
		}
		passwordHash = ph
		ownerStatus = domain.UserStatusActive
	}

	var (
		owner       *domain.User
		outBranches []*domain.TenantBranch
		outContacts []*domain.TenantContact
	)
	err = h.DB.WithTenantTx(r.Context(), t.ID, func(tx pgx.Tx) error {
		u, err := h.Users.CreateTx(r.Context(), tx, store.CreateUserInput{
			TenantID:     t.ID,
			Email:        req.OwnerEmail,
			Phone:        req.OwnerPhone,
			FullName:     req.OwnerName,
			PasswordHash: passwordHash,
			Status:       ownerStatus,
		})
		if err != nil {
			return err
		}
		owner = u
		if err := h.Roles.AssignTx(r.Context(), tx, u.ID, ownerRole.ID, nil); err != nil {
			return err
		}
		// Pending owner gets an invite token so the password-setup link
		// can be sent. Single tx so a failure to insert the invite
		// rolls back the whole tenant creation.
		if ownerStatus == domain.UserStatusPending {
			raw, hash, terr := store.NewInviteToken()
			if terr != nil {
				return terr
			}
			if ierr := h.Invites.CreateTx(r.Context(), tx, store.CreateInviteInput{
				TenantID:  t.ID,
				UserID:    u.ID,
				TokenHash: hash,
				InvitedBy: nil, // platform-driven, no actor user-id
				ExpiresAt: time.Now().Add(h.InviteTTL),
			}); ierr != nil {
				return ierr
			}
			rawInvite = raw
		}
		if len(branches) > 0 {
			if err := h.Tenants.ReplaceBranchesTx(r.Context(), tx, t.ID, branches); err != nil {
				return err
			}
			outBranches, err = h.Tenants.BranchesForTenantTx(r.Context(), tx, t.ID)
			if err != nil {
				return err
			}
		}
		if len(contacts) > 0 {
			if err := h.Tenants.ReplaceContactsTx(r.Context(), tx, t.ID, contacts); err != nil {
				return err
			}
			outContacts, err = h.Tenants.ContactsForTenantTx(r.Context(), tx, t.ID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	actorID, _ := middleware.UserIDFrom(r)
	_ = h.Audit.Write(r.Context(), store.AuditEntry{
		TenantID:   &t.ID,
		ActorID:    ptr(actorID),
		Action:     "tenant.created",
		TargetKind: "tenant",
		TargetID:   t.ID.String(),
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
		Metadata: map[string]any{
			"slug": t.Slug, "owner_id": owner.ID.String(),
			"branches": len(outBranches), "contacts": len(outContacts),
			"billing_plan": string(t.BillingPlan), "invite_sent": rawInvite != "",
		},
	})

	// Fire the activation email after the tx commits so a bad SMTP
	// doesn't block tenant creation. The notifier is "log and continue".
	if rawInvite != "" && h.UserH != nil {
		claims := middleware.ClaimsFrom(r)
		inviterName := ""
		if claims != nil {
			inviterName = claims.FullName
		}
		h.UserH.sendInviteEmail(t, owner, inviterName, rawInvite)
	}

	httpx.Created(w, createTenantResponse{
		Tenant:   t,
		Owner:    owner,
		Branches: outBranches,
		Contacts: outContacts,
	})
}

func validTenantKind(k string) bool {
	switch domain.TenantKind(k) {
	case domain.TenantKindSACCO, domain.TenantKindMicrofinance,
		domain.TenantKindDigitalLender, domain.TenantKindCooperative,
		domain.TenantKindChama:
		return true
	}
	return false
}

// ─────────── GET /v1/platform/tenants ───────────

func (h *TenantHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.Tenants.List(r.Context(), 200)
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, list)
}

// ─────────── GET /v1/tenant ───────────

func (h *TenantHandler) Current(w http.ResponseWriter, r *http.Request) {
	t := middleware.TenantFrom(r)
	if t == nil {
		httpx.WriteErr(w, r, httpx.ErrNotFound("no tenant"))
		return
	}
	httpx.OK(w, t)
}

func ptr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}
