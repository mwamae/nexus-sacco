// Package domain holds the business entities exposed across packages.
// Keep these dumb — no DB, no HTTP. They're just shapes.

package domain

import (
	"time"

	"github.com/google/uuid"
)

type TenantStatus string

const (
	TenantStatusActive    TenantStatus = "active"
	TenantStatusSuspended TenantStatus = "suspended"
	TenantStatusClosed    TenantStatus = "closed"
)

type TenantKind string

const (
	TenantKindSACCO         TenantKind = "sacco"
	TenantKindMicrofinance  TenantKind = "microfinance"
	TenantKindDigitalLender TenantKind = "digital_lender"
	TenantKindCooperative   TenantKind = "cooperative"
	TenantKindChama         TenantKind = "chama"
)

type Tenant struct {
	ID           uuid.UUID    `json:"id"`
	Slug         string       `json:"slug"`
	Name         string       `json:"name"`
	LegalName    string       `json:"legal_name,omitempty"`
	Kind         TenantKind   `json:"kind"`
	Status       TenantStatus `json:"status"`
	CountryCode  string       `json:"country_code"`
	CurrencyCode string       `json:"currency_code"`
	LicenseNo    string       `json:"license_no,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

type UserStatus string

const (
	UserStatusPending   UserStatus = "pending"
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
	UserStatusLocked    UserStatus = "locked"
	UserStatusClosed    UserStatus = "closed"
)

type User struct {
	ID               uuid.UUID  `json:"id"`
	TenantID         uuid.UUID  `json:"tenant_id"`
	Email            string     `json:"email"`
	Phone            string     `json:"phone,omitempty"`
	FullName         string     `json:"full_name"`
	Status           UserStatus `json:"status"`
	IsPlatformAdmin  bool       `json:"is_platform_admin"`
	EmailVerifiedAt  *time.Time `json:"email_verified_at,omitempty"`
	MFAEnabled       bool       `json:"mfa_enabled"`
	MFAMethod        string     `json:"mfa_method,omitempty"`
	LastLoginAt      *time.Time `json:"last_login_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type Role struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    *uuid.UUID `json:"tenant_id,omitempty"`
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	IsSystem    bool       `json:"is_system"`
	Permissions []string   `json:"permissions,omitempty"`
}

type RefreshToken struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	UserID     uuid.UUID
	TokenHash  []byte
	ParentID   *uuid.UUID
	UserAgent  string
	IP         string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}
