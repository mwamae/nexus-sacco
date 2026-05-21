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
	merged := mergeAutoVars(in.Payload, in.TenantID)
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
func mergeAutoVars(payload map[string]any, _ uuid.UUID) map[string]any {
	merged := map[string]any{}
	// Stage 5 keeps these stubbed at strings; stage 8 will join against
	// tenants + tenant_branding to inject real values per tenant.
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
