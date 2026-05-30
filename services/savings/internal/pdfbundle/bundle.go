// pdfbundle merges per-document files into a single PDF for the
// Documents tab "Download bundle" action.
//
// For each input file:
//   - PDF source       → appended page-by-page via pdfcpu.MergeRaw
//   - image source     → embedded as a single full-page PDF (jpg/png)
//   - anything else    → "file omitted" placeholder generated inline
//
// A small cover page lists every included document. Output is streamed
// back to the HTTP response; nothing persists to disk.

package pdfbundle

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	// register decoders so image.Decode handles all the common kinds
	_ "image/jpeg"
	_ "image/png"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// DocSource — one input file. The handler builds this from
// loan_documents + the filestore path.
type DocSource struct {
	Kind         string
	Description  string
	UploadedAt   time.Time
	ReviewStatus string
	Filename     string
	MimeType     string
	// AbsolutePath — the on-disk path under filestore.BaseDir. The
	// bundler reads it. Empty value triggers a "file unavailable"
	// placeholder.
	AbsolutePath string
}

// Build writes the merged PDF to w. The cover page is generated from
// the docs slice.
func Build(w io.Writer, loanRef string, docs []DocSource) error {
	if len(docs) == 0 {
		return fmt.Errorf("pdfbundle: at least one document required")
	}

	// Generate the cover page as PDF (using pdfcpu's basic text API
	// — keeps the bundle dependency-free of HTML→PDF).
	cover, err := buildCoverPage(loanRef, docs)
	if err != nil {
		return fmt.Errorf("cover page: %w", err)
	}

	// Build the per-document PDFs.
	var pieces [][]byte
	pieces = append(pieces, cover)
	for _, d := range docs {
		body, err := pieceFor(d)
		if err != nil {
			placeholder, perr := buildPlaceholderPage(d, err)
			if perr != nil {
				return fmt.Errorf("placeholder for %s: %w", d.Filename, perr)
			}
			pieces = append(pieces, placeholder)
			continue
		}
		pieces = append(pieces, body)
	}

	// Merge everything.
	readers := make([]io.ReadSeeker, len(pieces))
	for i, p := range pieces {
		readers[i] = bytes.NewReader(p)
	}
	conf := model.NewDefaultConfiguration()
	if err := api.MergeRaw(readers, w, false, conf); err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	return nil
}

// pieceFor — return the PDF bytes for one document. PDF inputs pass
// through unchanged; image inputs become a single full-page PDF.
func pieceFor(d DocSource) ([]byte, error) {
	if d.AbsolutePath == "" {
		return nil, fmt.Errorf("absolute path empty")
	}
	body, err := os.ReadFile(d.AbsolutePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", d.AbsolutePath, err)
	}

	lowerMIME := strings.ToLower(d.MimeType)
	switch {
	case strings.Contains(lowerMIME, "pdf"),
		strings.EqualFold(filepath.Ext(d.Filename), ".pdf"):
		// Pass-through; validate as PDF by attempting to read the
		// document header — pdfcpu's MergeRaw will reject bad PDFs anyway.
		return body, nil
	case strings.HasPrefix(lowerMIME, "image/"):
		return embedImageAsPDF(body)
	default:
		// Other MIME (docx, xlsx) — handler should generate a placeholder.
		return nil, fmt.Errorf("unsupported mime %q for bundle embed", d.MimeType)
	}
}

// embedImageAsPDF — pdfcpu's api.ImportImagesFile takes a file path; we
// have an in-memory []byte, so write to a temp file. The temp is cleaned
// up on return.
func embedImageAsPDF(img []byte) ([]byte, error) {
	tmp, err := os.CreateTemp("", "loan-bundle-img-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(img); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	// Re-encode to detect the real format + add the right extension —
	// pdfcpu's ImportImages reads the extension to dispatch the
	// decoder.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	_ = cfg

	withExt := tmpName + "." + format
	if err := os.Rename(tmpName, withExt); err != nil {
		return nil, err
	}
	defer os.Remove(withExt)

	outPDF, err := os.CreateTemp("", "loan-bundle-img-*.pdf")
	if err != nil {
		return nil, err
	}
	outPDFName := outPDF.Name()
	outPDF.Close()
	defer os.Remove(outPDFName)

	conf := model.NewDefaultConfiguration()
	imp, err := api.Import("form:A4, pos:c, scale:0.9 abs", types.POINTS)
	if err != nil {
		return nil, fmt.Errorf("pdfcpu import opts: %w", err)
	}
	if err := api.ImportImagesFile([]string{withExt}, outPDFName, imp, conf); err != nil {
		return nil, fmt.Errorf("pdfcpu import: %w", err)
	}
	return os.ReadFile(outPDFName)
}

// buildCoverPage — a one-page PDF listing every doc. Built with pdfcpu's
// "Create" API from a small text annotation.
func buildCoverPage(loanRef string, docs []DocSource) ([]byte, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Loan documents bundle\n")
	fmt.Fprintf(&sb, "Reference: %s\n", loanRef)
	fmt.Fprintf(&sb, "Generated: %s\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&sb, "Documents (%d):\n\n", len(docs))
	for i, d := range docs {
		fmt.Fprintf(&sb, "%2d. %s — %s (uploaded %s, review: %s)\n",
			i+1, d.Kind, displayLabel(d), d.UploadedAt.UTC().Format("2006-01-02"), d.ReviewStatus)
	}
	return makeSingleTextPagePDF(sb.String())
}

func buildPlaceholderPage(d DocSource, err error) ([]byte, error) {
	body := fmt.Sprintf(
		"File omitted from bundle\n\nKind: %s\nDescription: %s\nFilename: %s\nMIME: %s\n\nReason: %s",
		d.Kind, displayLabel(d), d.Filename, d.MimeType, err.Error(),
	)
	return makeSingleTextPagePDF(body)
}

func displayLabel(d DocSource) string {
	if d.Description != "" {
		return d.Description
	}
	return d.Filename
}

// makeSingleTextPagePDF — minimal pure-Go PDF with one A4 page rendering
// the supplied multi-line string in Helvetica. Built byte-by-byte; no
// external dep needed beyond pdfcpu (which is imported anyway).
//
// pdfcpu doesn't expose a high-level "write text" helper in its public
// API; we hand-roll the smallest valid PDF that satisfies pdfcpu's merge
// reader. The output is a single-page A4 PDF with Helvetica 10pt text,
// 14pt leading, top-left origin, 1-inch margins.
func makeSingleTextPagePDF(text string) ([]byte, error) {
	const (
		pageW = 595
		pageH = 842
		marginL = 56
		marginT = 56
		lineH   = 14
	)
	var contents bytes.Buffer
	contents.WriteString("BT\n/F1 10 Tf\n")
	y := pageH - marginT
	for _, line := range strings.Split(text, "\n") {
		// pdf string escaping: \, (, )
		escaped := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(line)
		fmt.Fprintf(&contents, "1 0 0 1 %d %d Tm (%s) Tj\n", marginL, y, escaped)
		y -= lineH
		if y < marginT {
			break
		}
	}
	contents.WriteString("ET\n")
	contentStream := contents.String()

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n%\xff\xff\xff\xff\n")
	offsets := []int{0}

	// Object 1 — Catalog
	offsets = append(offsets, pdf.Len())
	pdf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	// Object 2 — Pages
	offsets = append(offsets, pdf.Len())
	pdf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	// Object 3 — Page
	offsets = append(offsets, pdf.Len())
	fmt.Fprintf(&pdf,
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n",
		pageW, pageH)

	// Object 4 — Content stream
	offsets = append(offsets, pdf.Len())
	fmt.Fprintf(&pdf, "4 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(contentStream), contentStream)

	// Object 5 — Font
	offsets = append(offsets, pdf.Len())
	pdf.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>\nendobj\n")

	// xref
	xrefPos := pdf.Len()
	pdf.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i < len(offsets); i++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[i])
	}
	// trailer
	fmt.Fprintf(&pdf, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefPos)

	return pdf.Bytes(), nil
}
