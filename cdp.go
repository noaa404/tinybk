package main

// Connection layer: attach to a running Chrome tab and build observations.
// Connect only; never launch Chrome. Disconnect without closing the page.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// Session is one browser connection attached to a page tab.
type Session struct {
	tab      context.Context // no timeout; actions derive their own
	tabCut   context.CancelFunc
	allocCut context.CancelFunc
	browser  *chromedp.Browser // browser-level session for Target-domain commands
	tabID    target.ID
	cdpURL   string // used by listPages to compute tab indexes
}

// Three target-resolution failures: missing target with multiple tabs, prefix
// too short, and ambiguous prefix. All carry a code; callers branch on code,
// never on message text.
func errTargetRequired(pages []pageEntry) *cmdError {
	return fail(codeTargetRequired,
		fmt.Sprintf("%d tabs are open but no target was specified; use --target <id|prefix> (see tinybk tab for id/short)", len(pages)),
		map[string]any{"tabs": toTabs(pages)})
}

func errTargetTooShort(input string) *cmdError {
	return fail(codeTargetTooShort,
		fmt.Sprintf("target %q is too short: id prefix must be at least %d chars (the short from tinybk tab works directly)", input, targetPrefixMin),
		map[string]any{"target": input, "min_len": targetPrefixMin})
}

func errTargetAmbiguous(input string, cands []tabItem) *cmdError {
	var b strings.Builder
	fmt.Fprintf(&b, "prefix %q matches %d tabs; use a longer prefix (at least %d chars): ", input, len(cands), targetPrefixMin)
	for i, c := range cands {
		if i > 0 {
			b.WriteString("；")
		}
		fmt.Fprintf(&b, "%s <%s> %s", c.ID, c.URL, c.Title)
	}
	return fail(codeTargetAmbiguous, b.String(), map[string]any{"target": input, "tabs": cands})
}

func errTargetNotFound(input string, pages []pageEntry) *cmdError {
	return fail(codeTargetNotFound,
		fmt.Sprintf("tab %s not found; it may have been closed. Refresh id/prefix with tinybk tab", input),
		map[string]any{"target": input, "tabs": toTabs(pages)})
}

// resolveTarget finds a page by id or unique prefix. Prefix matching is case-insensitive.
// Prefixes shorter than targetPrefixMin are rejected; multiple matches return target_ambiguous.
func resolveTarget(pages []pageEntry, input string) (pageEntry, error) {
	if input == "" {
		return pageEntry{}, fmt.Errorf("target must not be empty")
	}
	if len(input) < targetPrefixMin {
		return pageEntry{}, errTargetTooShort(input)
	}
	want := strings.ToUpper(input)
	var hits []int
	for i, p := range pages {
		if strings.HasPrefix(strings.ToUpper(p.ID), want) {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 1:
		return pages[hits[0]], nil
	case 0:
		return pageEntry{}, errTargetNotFound(input, pages)
	default:
		cands := make([]tabItem, len(hits))
		for k, i := range hits {
			cands[k] = tabItemFromPage(pages[i], i)
		}
		return pageEntry{}, errTargetAmbiguous(input, cands)
	}
}

// Bound the "port responds but browser hangs" case so the process never hangs forever.
const (
	httpTimeout   = 3 * time.Second
	attachTimeout = 10 * time.Second
)

// cdpHTTP is the shared HTTP client for CDP endpoints; all requests share one timeout.
var cdpHTTP = &http.Client{Timeout: httpTimeout}

// Connect attaches to a target page over CDP.
// If targetID is set, resolve it as a stable identity. Otherwise use --tab only
// when explicitly given. With neither set, use the only page; with multiple pages,
// return target_required instead of guessing.
//
// Use WithTargetID so the first chromedp Run only attaches and never opens a new
// page. Do not call Page.bringToFront: it drags Chrome to the foreground and
// interrupts the user.
func Connect(cdpURL, targetID string, tabIndex int, tabExplicit bool) (*Session, error) {
	pages, err := listPages(cdpURL)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", cdpURL, err)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("browser has no page tabs (start it with at least one about:blank)")
	}

	var id target.ID
	switch {
	case targetID != "":
		p, err := resolveTarget(pages, targetID)
		if err != nil {
			return nil, err
		}
		id = target.ID(p.ID)
	case tabExplicit:
		if tabIndex < 0 {
			tabIndex = 0
		}
		if tabIndex >= len(pages) {
			return nil, fmt.Errorf("tab %d not found; %d tabs open", tabIndex, len(pages))
		}
		id = target.ID(pages[tabIndex].ID)
	case len(pages) == 1:
		id = target.ID(pages[0].ID)
	default:
		return nil, errTargetRequired(pages)
	}

	allocCtx, allocCut := remote.NewAllocator(context.Background(), cdpURL)
	tabCtx, tabCut := chromedp.NewContext(allocCtx,
		chromedp.WithTargetID(id),
		chromedp.WithDetachOnCancel(),
		// Silence internal event errors; log.Printf would corrupt the JSON written to stdout.
		chromedp.WithErrorf(func(string, ...any) {}))
	// The first Run uses a lightweight evaluate to confirm attach. Do not give it a derived timeout context:
	// the remote allocator binds the websocket lifetime to the first Run context
	// (see remote.Allocator.Allocate); an expired context drops the connection. Use an external
	// watchdog instead: on timeout, cancel the whole session and report the error.
	attached := make(chan error, 1)
	go func() {
		_, err := chromedp.Run(tabCtx, chromedp.Evaluate[string]("document.readyState"))
		attached <- err
	}()
	select {
	case err = <-attached:
	case <-time.After(attachTimeout):
		err = fmt.Errorf("no response after waiting %s", attachTimeout)
	}
	if err != nil {
		tabCut()
		allocCut()
		return nil, fmt.Errorf("attach tab %s: %w", id, err)
	}

	return &Session{
		tab:      tabCtx,
		tabCut:   tabCut,
		allocCut: allocCut,
		browser:  chromedp.FromContext(tabCtx).Browser,
		tabID:    id,
		cdpURL:   cdpURL,
	}, nil
}

// Close disconnects the session and leaves the tab open.
func (s *Session) Close() {
	s.tabCut()
	s.allocCut()
}

// Timeout returns a per-action timeout context.
func (s *Session) Timeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.tab, d)
}

// TargetInfo reads target information from the CDP Target domain.
// URL and title evidence comes from here without injecting JS into the page.
func (s *Session) TargetInfo(ctx context.Context) (*target.Info, error) {
	res, err := cdp.Call(ctx, s.browser, target.GetTargetInfo,
		target.GetTargetInfoParams{TargetID: s.tabID})
	if err != nil {
		return nil, err
	}
	if res.TargetInfo == nil {
		return nil, fmt.Errorf("tab %s no longer exists", s.tabID)
	}
	return res.TargetInfo, nil
}

// listPages reads all page tabs from /json/list (the order used by --tab N).
// This order is the browser's current order, not a stable identity; use id across calls.
func listPages(cdpURL string) ([]pageEntry, error) {
	resp, err := cdpHTTP.Get(strings.TrimSuffix(cdpURL, "/") + "/json/list")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var entries []pageEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	var pages []pageEntry
	for _, e := range entries {
		if e.Type == "page" {
			pages = append(pages, e)
		}
	}
	return pages, nil
}

// browserProduct reads /json/version for doctor.
// It does not use a page session, so it needs no target page and does not fail on multiple tabs.
func browserProduct(cdpURL string) (string, error) {
	resp, err := cdpHTTP.Get(strings.TrimSuffix(cdpURL, "/") + "/json/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		Browser string `json:"Browser"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Browser, nil
}

// Observation is the common result shape: URL/title always; text and screenshot
// on demand. observe_error means the action succeeded but observation failed;
// callers must not retry the action.
type Observation struct {
	based
	URL          string       `json:"url"`
	Title        string       `json:"title"`
	Text         string       `json:"text,omitempty"`
	TextTotal    int          `json:"text_total,omitempty"`
	TextError    string       `json:"text_error,omitempty"`
	Shot         string       `json:"shot,omitempty"`
	ShotError    string       `json:"shot_error,omitempty"`
	ObserveError string       `json:"observe_error,omitempty"`
	Nav          *navEvidence `json:"nav,omitempty"`
	// NewTab is set when the action opened a tab itself (click/press).
	NewTab *tabItem `json:"new_tab,omitempty"`
}

// pageText is the JS return shape for text capture: t is truncated text, n is full length.
type pageText struct {
	T   string `json:"t"`
	Len int    `json:"n"`
}

// pageState is a lightweight snapshot: href detects navigation, sig is a body-text
// signature (length + head/tail), and ready is readyState. Use textContent instead
// of innerText to avoid layout on every evaluation.
type pageState struct {
	Href  string `json:"href"`
	Sig   string `json:"sig"`
	Ready string `json:"ready"`
}

const pageStateJS = `(() => {
  const s = document.body ? (document.body.textContent || '') : '';
  return {href: location.href, sig: s.length + '|' + s.slice(0, 200) + '|' + s.slice(-200), ready: document.readyState};
})()`

// navEvidence records URL/body-text changes and whether rendering settled before
// returning. capped means the wait limit was reached while changes were still
// happening; callers should wait before asserting.
type navEvidence struct {
	URLChanged     bool   `json:"url_changed"`
	ContentChanged bool   `json:"content_changed"`
	ReadyState     string `json:"ready_state,omitempty"`
	Waited         int64  `json:"waited_ms"`
	Capped         bool   `json:"capped,omitempty"`
}

const (
	navWindow   = 200 * time.Millisecond // window for catching asynchronous navigation after an action
	settleQuiet = 200 * time.Millisecond // quiet period for body text
	settleCap   = 2 * time.Second        // max wait for rendering after navigation
	settleTick  = 25 * time.Millisecond  // snapshot polling interval
)

// snapshot reads one page snapshot. Evaluation failures during navigation return nil for the caller to handle.
func (s *Session) snapshot(ctx context.Context) *pageState {
	st, err := chromedp.Run(ctx, chromedp.Evaluate[*pageState](pageStateJS))
	if err != nil {
		return nil
	}
	return st
}

// navKey drops the URL fragment: in-page anchors only change # and must not trigger navigation waits.
func navKey(raw string) string {
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		return raw[:i]
	}
	return raw
}

// errWaitTimeout means waitUntil reached its deadline.
var errWaitTimeout = errors.New("wait timed out")

// waitUntil calls fn every interval until it returns true, the context is done, or the timeout expires.
func waitUntil(ctx context.Context, interval, timeout time.Duration, fn func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := fn()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errWaitTimeout
		}
		if !sleepCtx(ctx, interval) {
			return ctx.Err()
		}
	}
}

// sleepCtx is a cancellable sleep; false means the context ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// settleAfter waits a bounded time after an action so a mixed state (new URL,
// old body) is not returned as the result. It first looks for navigation during
// navWindow, including URL changes inside setTimeout. If the URL did not change,
// it reports observed body-text changes without waiting for stability. If it did
// change, it waits until body text differs from before, readyState is complete,
// and the body stays unchanged for settleQuiet, up to settleCap.
// If before is nil (the pre-action snapshot failed), it returns nil.
func (s *Session) settleAfter(before *pageState) *navEvidence {
	if before == nil {
		return nil
	}
	ctx, cut := s.Timeout(obsTimeout)
	defer cut()
	start := time.Now()
	nav := &navEvidence{}
	var cur *pageState
	for {
		cur = s.snapshot(ctx)
		if cur != nil && navKey(cur.Href) != navKey(before.Href) {
			break
		}
		if time.Since(start) >= navWindow || !sleepCtx(ctx, settleTick) {
			// No full navigation, but body text may have changed in place (DOM update).
			// Report observed facts without waiting for stability.
			if cur != nil {
				nav.ContentChanged = cur.Sig != before.Sig
				nav.ReadyState = cur.Ready
			}
			nav.Waited = time.Since(start).Milliseconds()
			return nav
		}
	}

	nav.URLChanged = true
	last, stable, sawLoading := "", 0, false
	for time.Since(start) < navWindow+settleCap {
		cur = s.snapshot(ctx)
		if cur != nil {
			nav.ReadyState = cur.Ready
			nav.ContentChanged = cur.Sig != before.Sig
			if cur.Ready != "complete" {
				sawLoading = true
			}
			// For full loads, check whether readyState passed through loading; same-document
			// navigation such as pushState has no loading phase, so body-text changes are the only signal.
			if cur.Ready == "complete" && (nav.ContentChanged || sawLoading) {
				if cur.Sig == last {
					stable++
					if stable >= int(settleQuiet/settleTick) {
						nav.Waited = time.Since(start).Milliseconds()
						return nav
					}
				} else {
					last, stable = cur.Sig, 0
				}
			}
		}
		if !sleepCtx(ctx, settleTick) {
			break
		}
	}
	nav.Capped = true
	nav.Waited = time.Since(start).Milliseconds()
	return nav
}

// Observe builds an observation: URL/title from the Target domain, body text
// truncated by TextLimit with full length recorded, and an optional screenshot.
// Text-capture failures are non-fatal and go to text_error.
func (s *Session) Observe(ctx context.Context, o obsOpts) (Observation, error) {
	var obs Observation
	info, err := s.TargetInfo(ctx)
	if err != nil {
		return obs, fmt.Errorf("read tab state: %w", err)
	}
	obs.URL, obs.Title = info.URL, info.Title

	if o.TextLimit > 0 {
		pt, err := chromedp.Run(ctx, chromedp.Evaluate[pageText](fmt.Sprintf(
			`(() => { const s = document.body ? document.body.innerText : ""; return {t: s.slice(0, %d), n: s.length}; })()`,
			o.TextLimit)))
		if err != nil {
			obs.TextError = err.Error()
		} else {
			obs.Text = strings.TrimSpace(pt.T)
			obs.TextTotal = pt.Len
		}
	}
	if o.Shot {
		if path, err := s.shot(ctx, o.ShotDir); err != nil {
			obs.ShotError = err.Error()
		} else {
			obs.Shot = path
		}
	}
	return obs, nil
}

// observeAfterAction observes after a successful action. Observation failure must
// not turn the action into a failure, or callers will retry a click/input that
// already happened. Use a separate timeout budget.
func (s *Session) observeAfterAction(o obsOpts) Observation {
	ctx, cut := s.Timeout(obsTimeout)
	defer cut()
	obs, err := s.Observe(ctx, o)
	if err != nil {
		obs.ObserveError = err.Error()
	}
	return obs
}

// evidenceAfterAction is the lightweight observeAfterAction variant that reads only URL/title.
func (s *Session) evidenceAfterAction() Observation {
	return s.observeAfterAction(obsOpts{})
}

// shot captures the current viewport; --shot-dir selects the directory.
// An empty value uses .tinybk/shots in the current working directory.
func (s *Session) shot(ctx context.Context, dir string) (string, error) {
	path, _, err := captureShot(ctx, dir)
	return path, err
}
