// HTML → PDF via headless Chrome (chromedp).
//
// One long-lived chromedp browser allocator; each render gets its own
// short-lived tab. The browser persists across renders, so the only
// per-render cost is opening a tab and printing — fast enough for
// inline PDF generation inside an API call.
//
// chromedp.Print's underlying CDP Page.printToPDF supports A4 / Letter
// / Legal via paperWidth/paperHeight inches. We map them in PageDims.

package pdf

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type Renderer struct {
	mu         sync.Mutex
	allocCtx   context.Context
	allocClose context.CancelFunc
	browserCtx context.Context
	browserCls context.CancelFunc
}

// NewRenderer boots a headless Chrome and keeps it warm. Call Close()
// on shutdown.
func NewRenderer(ctx context.Context) (*Renderer, error) {
	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
	)
	allocCtx, allocClose := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, browserCls := chromedp.NewContext(allocCtx)
	// Force browser start now so we surface launch errors at boot time.
	if err := chromedp.Run(browserCtx); err != nil {
		browserCls()
		allocClose()
		return nil, fmt.Errorf("chromedp boot: %w", err)
	}
	return &Renderer{
		allocCtx: allocCtx, allocClose: allocClose,
		browserCtx: browserCtx, browserCls: browserCls,
	}, nil
}

func (r *Renderer) Close() {
	if r == nil {
		return
	}
	r.browserCls()
	r.allocClose()
}

type PageSize string

const (
	A4     PageSize = "A4"
	Letter PageSize = "Letter"
	Legal  PageSize = "Legal"
)

// HTMLToPDF prints the given HTML to a PDF byte slice. landscape is
// derived from the @page CSS — but you can also force it here for
// pure programmatic renders.
func (r *Renderer) HTMLToPDF(ctx context.Context, html string, size PageSize) ([]byte, error) {
	if r == nil {
		return nil, errors.New("pdf renderer is nil")
	}
	if html == "" {
		return nil, errors.New("empty html")
	}
	w, h, landscape := paperInches(size, html)
	// One tab per render. chromedp.NewContext from a browser context
	// reuses the browser; the cancel only closes the tab.
	tabCtx, cancel := chromedp.NewContext(r.browserCtx)
	defer cancel()

	// Hard upper bound per render so a runaway template doesn't pin a tab.
	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, 30*time.Second)
	defer cancelTimeout()

	r.mu.Lock()
	defer r.mu.Unlock()

	var buf []byte
	err := chromedp.Run(tabCtx,
		// data: URLs avoid a tempfile + give us synchronous loading.
		chromedp.Navigate("data:text/html;charset=utf-8,"+escapeDataURL(html)),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			out, _, err := page.PrintToPDF().
				WithPrintBackground(true).
				WithMarginTop(0.4).WithMarginBottom(0.4).
				WithMarginLeft(0.4).WithMarginRight(0.4).
				WithPaperWidth(w).WithPaperHeight(h).
				WithLandscape(landscape).
				Do(ctx)
			if err != nil {
				return err
			}
			buf = out
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	return buf, nil
}

// paperInches returns (width, height, landscape).  Landscape is true
// if the HTML's @page directive contains "landscape".
func paperInches(size PageSize, html string) (w, h float64, landscape bool) {
	switch size {
	case Letter:
		w, h = 8.5, 11
	case Legal:
		w, h = 8.5, 14
	default: // A4
		w, h = 8.27, 11.69
	}
	if hasLandscapePragma(html) {
		landscape = true
	}
	return
}

func hasLandscapePragma(html string) bool {
	// Cheap substring check — good enough for our seed templates.
	return contains(html, "landscape")
}

func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// escapeDataURL percent-encodes the few characters that would otherwise
// break a data: URL. We intentionally don't go through net/url because
// it would double-encode characters Chrome already accepts (UTF-8).
func escapeDataURL(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '#', '%':
			out = append(out, '%', hexNibble(byte(r)>>4), hexNibble(byte(r)&0x0f))
		default:
			out = append(out, []byte(string(r))...)
		}
	}
	return string(out)
}

func hexNibble(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}
