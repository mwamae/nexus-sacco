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
	parentCtx  context.Context
	allocCtx   context.Context
	allocClose context.CancelFunc
	browserCtx context.Context
	browserCls context.CancelFunc
}

// NewRenderer boots a headless Chrome and keeps it warm. Call Close()
// on shutdown.
func NewRenderer(ctx context.Context) (*Renderer, error) {
	r := &Renderer{parentCtx: ctx}
	if err := r.bootLocked(); err != nil {
		return nil, err
	}
	return r, nil
}

// bootLocked (re)spawns the headless browser. Caller must hold r.mu.
func (r *Renderer) bootLocked() error {
	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
	)
	allocCtx, allocClose := chromedp.NewExecAllocator(r.parentCtx, opts...)
	browserCtx, browserCls := chromedp.NewContext(allocCtx)
	// Force browser start now so we surface launch errors here.
	if err := chromedp.Run(browserCtx); err != nil {
		browserCls()
		allocClose()
		return fmt.Errorf("chromedp boot: %w", err)
	}
	r.allocCtx, r.allocClose = allocCtx, allocClose
	r.browserCtx, r.browserCls = browserCtx, browserCls
	return nil
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
//
// If the warm browser has died (Chrome crashed, OS sleep killed it,
// etc.), the first render attempt fails; we re-boot once and retry.
func (r *Renderer) HTMLToPDF(ctx context.Context, html string, size PageSize) ([]byte, error) {
	if r == nil {
		return nil, errors.New("pdf renderer is nil")
	}
	if html == "" {
		return nil, errors.New("empty html")
	}
	w, h, landscape := paperInches(size, html)

	r.mu.Lock()
	defer r.mu.Unlock()

	buf, err := r.renderOnceLocked(html, w, h, landscape)
	if err == nil {
		return buf, nil
	}
	if !looksLikeDeadBrowser(err) {
		return nil, fmt.Errorf("render: %w", err)
	}
	// Browser died (Chrome crashed, machine slept, etc.) — re-spawn once.
	r.browserCls()
	r.allocClose()
	if bootErr := r.bootLocked(); bootErr != nil {
		return nil, fmt.Errorf("render: re-boot after dead browser: %w (original: %v)", bootErr, err)
	}
	buf, err = r.renderOnceLocked(html, w, h, landscape)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	return buf, nil
}

func (r *Renderer) renderOnceLocked(html string, w, h float64, landscape bool) ([]byte, error) {
	// One tab per render. chromedp.NewContext from a browser context
	// reuses the browser; the cancel only closes the tab.
	tabCtx, cancel := chromedp.NewContext(r.browserCtx)
	defer cancel()
	// Hard upper bound per render so a runaway template doesn't pin a tab.
	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, 30*time.Second)
	defer cancelTimeout()

	var buf []byte
	err := chromedp.Run(tabCtx,
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
	return buf, err
}

// looksLikeDeadBrowser returns true if the chromedp error suggests the
// browser process died and we should re-spawn. We match on common
// symptoms: closed pipe, connection refused, EOF from CDP, context
// errors fired before the per-render timeout could plausibly hit.
func looksLikeDeadBrowser(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, frag := range []string{
		"websocket: close",
		"connection reset",
		"broken pipe",
		"i/o timeout",
		"connect: connection refused",
		"EOF",
		"context canceled",  // browser ctx cancelled when alloc died
		"chrome failed to start",
		"target window already closed",
		"not connected to the dev tools",
	} {
		if stringsContains(msg, frag) {
			return true
		}
	}
	return false
}

func stringsContains(s, sub string) bool { return contains(s, sub) }

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
