// Domain entities surfaced across packages. Keep these dumb — no DB, no HTTP.

package domain

import (
	"time"

	"github.com/google/uuid"
)

type MemberStatus string

const (
	StatusPending   MemberStatus = "pending"
	StatusActive    MemberStatus = "active"
	StatusSuspended MemberStatus = "suspended"
	StatusClosed    MemberStatus = "closed"
	StatusRejected  MemberStatus = "rejected"
)

type IDDocKind string

const (
	IDNationalID IDDocKind = "national_id"
	IDPassport   IDDocKind = "passport"
	IDAlienID    IDDocKind = "alien_id"
)

type Gender string

const (
	GenderMale         Gender = "male"
	GenderFemale       Gender = "female"
	GenderOther        Gender = "other"
	GenderUndisclosed  Gender = "undisclosed"
)

type RelationKind string

const (
	RelNextOfKin    RelationKind = "next_of_kin"
	RelBeneficiary  RelationKind = "beneficiary"
)

type DocumentKind string

const (
	DocSignature     DocumentKind = "signature"
	DocPassportPhoto DocumentKind = "passport_photo"
	DocIDFront       DocumentKind = "id_front"
	DocIDBack        DocumentKind = "id_back"
)

type Member struct {
	ID       uuid.UUID    `json:"id"`
	TenantID uuid.UUID    `json:"tenant_id"`
	MemberNo string       `json:"member_no"`
	Status   MemberStatus `json:"status"`

	FullName    string    `json:"full_name"`
	IDDocKind   IDDocKind `json:"id_doc_kind"`
	IDDocNumber string    `json:"id_doc_number"`
	KraPIN      string    `json:"kra_pin,omitempty"`
	Gender      Gender    `json:"gender"`
	DateOfBirth *time.Time `json:"date_of_birth,omitempty"`

	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`

	County          string `json:"county,omitempty"`
	SubCounty       string `json:"sub_county,omitempty"`
	PhysicalAddress string `json:"physical_address,omitempty"`

	EmploymentStatus string `json:"employment_status,omitempty"`
	Employer         string `json:"employer,omitempty"`
	PayrollNo        string `json:"payroll_no,omitempty"`
	JobTitle         string `json:"job_title,omitempty"`

	ApprovedAt      *time.Time `json:"approved_at,omitempty"`
	ApprovedBy      *uuid.UUID `json:"approved_by,omitempty"`
	RejectionReason string     `json:"rejection_reason,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
}

type Relation struct {
	ID            uuid.UUID    `json:"id"`
	MemberID      uuid.UUID    `json:"member_id"`
	Kind          RelationKind `json:"kind"`
	FullName      string       `json:"full_name"`
	Relationship  string       `json:"relationship"`
	Phone         string       `json:"phone,omitempty"`
	Email         string       `json:"email,omitempty"`
	IDDocNumber   string       `json:"id_doc_number,omitempty"`
	SharePercent  *float64     `json:"share_percent,omitempty"`
	Position      int          `json:"position"`
}

type Document struct {
	ID          uuid.UUID    `json:"id"`
	MemberID    uuid.UUID    `json:"member_id"`
	Kind        DocumentKind `json:"kind"`
	StoragePath string       `json:"-"` // never expose raw storage path
	MIME        string       `json:"mime"`
	SizeBytes   int64        `json:"size_bytes"`
	UploadedAt  time.Time    `json:"uploaded_at"`
}
