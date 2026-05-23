// Generator — coordinates template lookup, HTML rendering, chromedp
// PDF print, and pdf_documents storage. The single public entry-point
// is Generate(), called by both the admin "generate offer letter"
// action and the inline notify-with-pdf path.

package pdf

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/store"
)

type Generator struct {
	DB        *db.Pool
	PDFs      *store.PDFStore
	Renderer  *Renderer
	Storage   *Storage
	// TokenTTL is how long the per-doc download token is valid for.
	// Spec defaults to 72 hours.
	TokenTTL  time.Duration
}

type GenerateInput struct {
	TenantID         uuid.UUID
	DocumentType     string         // e.g. "OFFER_LETTER"
	SubjectMemberID  *uuid.UUID
	SubjectLoanID    *uuid.UUID
	SubjectAccountID *uuid.UUID
	SubjectLabel     string
	Payload          map[string]any // injected into the template
	GeneratedBy      *uuid.UUID
}

func (g *Generator) Generate(ctx context.Context, in GenerateInput) (*domain.PDFDocument, []byte, error) {
	if in.TenantID == uuid.Nil {
		return nil, nil, fmt.Errorf("pdf: tenant_id required")
	}
	if in.DocumentType == "" {
		return nil, nil, fmt.Errorf("pdf: document_type required")
	}

	// 1. Load active template inside tenant context.
	var tpl *domain.PDFTemplate
	err := g.DB.WithTenantTx(ctx, in.TenantID, func(tx pgx.Tx) error {
		t, err := g.PDFs.ActiveTemplateTx(ctx, tx, in.DocumentType)
		if err != nil {
			return err
		}
		tpl = t
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("lookup template: %w", err)
	}
	if tpl == nil {
		return nil, nil, fmt.Errorf("no active PDF template for %s in this tenant", in.DocumentType)
	}

	// 2. Render HTML with payload + tenant-wide auto-injected vars.
	merged, brandErr := g.mergeAutoVars(ctx, in.Payload, in.TenantID)
	if brandErr != nil {
		// Don't fail the render on a branding lookup error — log and
		// fall back to the placeholder strings.
		merged = fallbackBrandingVars(in.Payload)
	}
	rendered := store.RenderTemplate(tpl.HTMLBody, merged)

	// 3. Print to PDF via chromedp.
	size := PageSize(tpl.PageSize)
	if size == "" {
		size = A4
	}
	pdfBytes, err := g.Renderer.HTMLToPDF(ctx, rendered, size)
	if err != nil {
		return nil, nil, fmt.Errorf("render pdf: %w", err)
	}

	// 4. Write to disk.
	relPath, err := g.Storage.Write(in.TenantID, pdfBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("store pdf: %w", err)
	}

	// 5. Create the audit row.
	ttl := g.TokenTTL
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	token := randomToken(32)
	expires := time.Now().Add(ttl)
	tplID := tpl.ID
	tplVer := tpl.VersionNo
	var doc *domain.PDFDocument
	err = g.DB.WithTenantTx(ctx, in.TenantID, func(tx pgx.Tx) error {
		d, err := g.PDFs.CreateDocumentTx(ctx, tx, store.CreatePDFInput{
			DocumentType:     in.DocumentType,
			TemplateID:       &tplID,
			TemplateVersion:  &tplVer,
			SubjectMemberID:  in.SubjectMemberID,
			SubjectLoanID:    in.SubjectLoanID,
			SubjectAccountID: in.SubjectAccountID,
			SubjectLabel:     in.SubjectLabel,
			Payload:          merged,
			StoragePath:      relPath,
			FileSizeBytes:    len(pdfBytes),
			DownloadToken:    token,
			TokenExpiresAt:   expires,
			GeneratedBy:      in.GeneratedBy,
		})
		if err != nil {
			return err
		}
		doc = d
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("persist pdf row: %w", err)
	}
	return doc, pdfBytes, nil
}

// mergeAutoVars injects tenant-wide branding variables alongside the
// caller's payload, without overwriting any explicit field.
//
// Phase G: reads tenant_name from the tenants row and tenant_address
// from the HQ branch's physical_address (best-effort). Anything the
// caller's payload sets explicitly still wins — branding is purely
// auto-fill for templates that don't otherwise care.
func (g *Generator) mergeAutoVars(ctx context.Context, payload map[string]any, tenantID uuid.UUID) (map[string]any, error) {
	merged := fallbackBrandingVars(payload)
	merged["generated_date"] = time.Now().Format("2 January 2006")

	// Pull the real tenant name + (best-effort) HQ branch address. RLS
	// would block this if we ran it under the tenant context, since
	// the tenants row itself isn't visible — so we use a plain pool
	// query bypassing the WithTenantTx wrapper. Errors here are
	// non-fatal; the caller falls back to the stub.
	var name string
	var legalName *string
	if err := g.DB.QueryRow(ctx,
		`SELECT name, legal_name FROM tenants WHERE id = $1`, tenantID,
	).Scan(&name, &legalName); err != nil {
		return nil, err
	}
	if name != "" {
		merged["tenant_name"] = name
	}
	if legalName != nil && *legalName != "" {
		merged["tenant_legal_name"] = *legalName
	}

	// Address — first HQ branch's physical_address, falling back to
	// the first branch of any kind. Soft failure: missing is fine.
	var addr *string
	_ = g.DB.QueryRow(ctx, `
		SELECT physical_address FROM tenant_branches
		 WHERE tenant_id = $1 AND COALESCE(NULLIF(physical_address,''), '') <> ''
		 ORDER BY (kind = 'hq') DESC, position ASC LIMIT 1
	`, tenantID).Scan(&addr)
	if addr != nil && *addr != "" {
		merged["tenant_address"] = *addr
	}

	// Caller's explicit payload always wins (already merged in
	// fallbackBrandingVars). Re-apply at the end as belt-and-braces.
	for k, v := range payload {
		merged[k] = v
	}
	return merged, nil
}

// fallbackBrandingVars is the placeholder-only flavor used when the
// tenants lookup fails. Keeps the renderer working even if the row is
// missing (test rigs / pre-seed dev DBs).
func fallbackBrandingVars(payload map[string]any) map[string]any {
	merged := map[string]any{}
	merged["generated_date"] = time.Now().Format("2 January 2006")
	merged["tenant_name"] = "Your SACCO"
	merged["tenant_address"] = ""
	merged["footer_extra"] = ""
	for k, v := range payload {
		merged[k] = v
	}
	return merged
}

func randomToken(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a uuid — still 122 bits of entropy.
		return uuid.NewString()
	}
	return hex.EncodeToString(b)
}
