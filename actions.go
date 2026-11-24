package main

// Actions run on an established session and return JSON-serializable results.
// Click and press wait a bounded time for navigation evidence; no wait is unbounded.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// requireInputTarget checks that the target page is visible before sending real input.
// This is a preflight check, not a guarantee: another process can switch tabs after it; delivery validation covers that window.
func (s *Session) requireInputTarget() error {
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	vis, err := chromedp.Run(ctx, chromedp.Evaluate[string]("document.visibilityState"))
	if err != nil {
		return fmt.Errorf("cannot verify target visibility (page may be navigating); action not executed: %w", err)
	}
	if vis != "visible" {
		// Chrome drops real input for background tabs. Fail explicitly instead of pretending success.
		sid := shortID(string(s.tabID))
		return fail(codeTabNotVisible,
			fmt.Sprintf("target tab %s is not in the foreground (visibility=%s); Chrome drops real input, so nothing was executed. Run tinybk tab activate %s and retry", sid, vis, sid),
			map[string]any{
				"short":      sid,
				"target_id":  string(s.tabID),
				"visibility": vis,
				"hint":       "tinybk tab activate " + sid,
			})
	}
	return nil
}

// interactionResult is returned by click/type: observation plus the element targeted.
// Value is set by type and is the final value read from the target element.
type interactionResult struct {
	Observation
	Target elemInfo `json:"target"`
	Value  string   `json:"value,omitempty"`
}

type mapResult struct {
	based
	Items        []*mapItem `json:"items"`
	Frame        string     `json:"frame,omitempty"`
	MatchedTotal int        `json:"matched_total"`
	Returned     int        `json:"returned"`
	Truncated    bool       `json:"truncated"`
	NextOffset   int        `json:"next_offset,omitempty"`
}

type tabResult struct {
	based
	Created *tabItem  `json:"created,omitempty"`
	Closed  *tabItem  `json:"closed,omitempty"`
	Tabs    []tabItem `json:"tabs"`
}

// tabActivateResult reports the activated tab and the document.visibilityState
// read after activation.
type tabActivateResult struct {
	based
	Activated  *tabItem  `json:"activated"`
	Visibility string    `json:"visibility"`
	Tabs       []tabItem `json:"tabs"`
}

type waitResult struct {
	based
	Waited int64  `json:"waited_ms"`
	URL    string `json:"url"`
	Title  string `json:"title"`
}

// scrollResult and evalResult contain action-specific data; the runner fills short.
type scrollResult struct {
	based
	ScrollY int64 `json:"scroll_y"`
}

type evalResult struct {
	based
	Result json.RawMessage `json:"result"`
}

type doctorResult struct {
	based
	CDP     string    `json:"cdp"`
	Browser string    `json:"browser,omitempty"`
	Tabs    []tabItem `json:"tabs"`
}

// launchHint is shown when doctor cannot reach a browser.
const launchHint = `/Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome \
  --remote-debugging-port=37922 \
  --user-data-dir="$PWD/.tinybk/profile" \
  --no-first-run --no-default-browser-check \
  --disable-backgrounding-occluded-windows --disable-renderer-backgrounding --disable-background-timer-throttling \
  "about:blank"`

// normalizeURL adds https:// when no scheme is present; about:, data:, etc. are preserved.
func normalizeURL(raw string) string {
	if u, err := url.Parse(raw); err != nil || u.Scheme == "" {
		return "https://" + raw
	}
	return raw
}

func cmdGoto(s *Session, rawURL string, o obsOpts) (any, error) {
	rawURL = normalizeURL(rawURL)
	ctx, cut := s.Timeout(navTimeout)
	defer cut()
	if _, err := chromedp.Run(ctx, chromedp.Navigate(rawURL)); err != nil {
		return nil, fmt.Errorf("navigate to %s: %w", rawURL, err)
	}
	obs := s.observeAfterAction(o)
	return &obs, nil
}

func pollJS(ctx context.Context, js string, timeout time.Duration) error {
	return waitUntil(ctx, 50*time.Millisecond, timeout, func() (bool, error) {
		v, err := chromedp.Run(ctx, chromedp.Evaluate[bool](js))
		return err == nil && v, nil
	})
}

func cmdBack(s *Session) (any, error) {
	ctx, cut := s.Timeout(navTimeout)
	defer cut()
	// Avoid chromedp.NavigateBack: it waits for loadEventFired, which bfcache restores
	// never emit. Avoid chromedp.Poll too: tasks do not survive navigation. Poll from Go.
	var entryURL string
	back := chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(ctx, t, page.GetNavigationHistory, cdp.Empty{})
		if err != nil {
			return err
		}
		cur := res.CurrentIndex
		if cur <= 0 {
			return fmt.Errorf("no earlier history entry")
		}
		entryURL = res.Entries[cur-1].URL
		_, err = cdp.Call(ctx, t, page.NavigateToHistoryEntry,
			page.NavigateToHistoryEntryParams{EntryID: res.Entries[cur-1].ID})
		return err
	})
	if _, err := chromedp.Run(ctx, back); err != nil {
		return nil, fmt.Errorf("back: %w", err)
	}
	// Wait for the URL to become the history entry (allow a 3s redirect fallback), then wait for readyState.
	if err := pollJS(ctx, "location.href === "+strconv.Quote(entryURL), 3*time.Second); err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("back: %w", ctx.Err())
	}
	if err := pollJS(ctx, `document.readyState === "complete"`, navTimeout); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("back: %w", ctx.Err())
		}
		return nil, fmt.Errorf("back: timed out waiting for page ready")
	}
	obs := s.evidenceAfterAction()
	return &obs, nil
}

// pageIDs records the current page ids as a baseline for detecting tabs opened by an action.
// A failed snapshot returns nil; callers skip new-tab detection without failing the action.
func pageIDs(cdpURL string) map[string]bool {
	pages, err := listPages(cdpURL)
	if err != nil {
		return nil
	}
	ids := make(map[string]bool, len(pages))
	for _, p := range pages {
		ids[p.ID] = true
	}
	return ids
}

// newTabAfter waits up to wait for a tab that is not in the baseline.
// A tab still on about:blank at the deadline is returned as-is; returns nil if none appears.
func newTabAfter(ctx context.Context, cdpURL string, before map[string]bool, wait time.Duration) *tabItem {
	deadline := time.Now().Add(wait)
	var found *tabItem
	_ = waitUntil(ctx, 25*time.Millisecond, wait, func() (bool, error) {
		pages, err := listPages(cdpURL)
		if err != nil {
			return false, nil // A listing failure leaves the click result unchanged.
		}
		for i, p := range pages {
			if before[p.ID] {
				continue
			}
			if p.URL == "about:blank" && time.Now().Before(deadline) {
				return false, nil // Wait for the new tab to leave about:blank.
			}
			it := tabItemFromPage(p, i)
			found = &it
			return true, nil
		}
		return false, nil
	})
	return found
}

func cmdClick(s *Session, arg string, o obsOpts, force bool, frame string) (any, error) {
	t, err := parseTarget(arg)
	if err != nil {
		return nil, err
	}
	if err := s.requireInputTarget(); err != nil {
		return nil, err
	}
	l, err := locate(s, t, false, frame)
	if err != nil {
		return nil, err
	}
	if !force && !l.Hit {
		return nil, fmt.Errorf("click point is covered by %s (element may be obscured; add --force to skip validation)", l.Blocker)
	}
	before := pageIDs(s.cdpURL)
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	st := s.snapshot(ctx)
	if _, err := chromedp.Run(ctx, chromedp.MouseClickXY(l.X, l.Y)); err != nil {
		return nil, fmt.Errorf("click %s: %w", t, err)
	}
	nav := s.settleAfter(st)
	res := &interactionResult{Observation: s.observeAfterAction(o), Target: l.elemInfo}
	res.Nav = nav
	if before != nil {
		wait := time.Duration(0)
		if l.Blank {
			wait = 2 * time.Second
		}
		res.NewTab = newTabAfter(ctx, s.cdpURL, before, wait)
	}
	return res, nil
}

func cmdType(s *Session, arg, text string) (any, error) {
	t, err := parseTypeTarget(arg)
	if err != nil {
		return nil, err
	}
	if err := s.requireInputTarget(); err != nil {
		return nil, err
	}
	l, err := locate(s, t, true, "") // Focus the target so keyboard input lands there.
	if err != nil {
		return nil, err
	}
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	if text != "" {
		if _, err := chromedp.Run(ctx, chromedp.KeyEvent(text)); err != nil {
			return nil, fmt.Errorf("type into %s: %w", t, err)
		}
	}
	obs := s.evidenceAfterAction()
	// Value is evidence; a read failure is non-fatal and leaves the field omitted.
	// Re-read by node identity when available instead of relying on CSS or an old index.
	var val string
	switch {
	case l.bid != 0:
		val, _ = valueByBid(ctx, l.bid)
	case t.css != "":
		val, _ = chromedp.Run(ctx, chromedp.Evaluate[string](valueCSSJS(t.css)))
	}
	return &interactionResult{Observation: obs, Target: l.elemInfo, Value: val}, nil
}

func cmdPress(s *Session, key string, times int) (any, error) {
	chord, err := parseKeyChord(key)
	if err != nil {
		return nil, err
	}
	if err := s.requireInputTarget(); err != nil {
		return nil, err
	}
	before := pageIDs(s.cdpURL)
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	st := s.snapshot(ctx)
	steps := make([]chromedp.Action[chromedp.Void], times)
	for i := range steps {
		steps[i] = chromedp.KeyEvent(chord.Seq, chromedp.KeyModifiers(chord.Modifiers...))
	}
	if err := chromedp.Do(ctx, steps...); err != nil {
		return nil, fmt.Errorf("press %s x%d: %w", key, times, err)
	}
	nav := s.settleAfter(st)
	obs := s.evidenceAfterAction()
	obs.Nav = nav
	if before != nil {
		// Presses can open a tab (form target=_blank, window.open) without changing the current URL.
		// Report it so the caller does not assume the action failed.
		obs.NewTab = newTabAfter(ctx, s.cdpURL, before, navWindow)
	}
	return &obs, nil
}

func cmdScroll(s *Session, arg string) (any, error) {
	var js string
	switch arg {
	case "top":
		js = "window.scrollTo(0, 0)"
	case "bottom":
		js = "window.scrollTo(0, document.body.scrollHeight)"
	default:
		n, err := strconv.Atoi(strings.TrimPrefix(arg, "+"))
		if err != nil {
			return nil, usagef("invalid scroll target %q; use top, bottom, or +/-N pixels", arg)
		}
		js = fmt.Sprintf("window.scrollBy(0, %d)", n)
	}
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	// Read scrollY in the same evaluation; it is direct evidence that scrolling took effect.
	y, err := chromedp.Run(ctx, chromedp.Evaluate[int64](js+"; Math.round(window.scrollY)"))
	if err != nil {
		return nil, fmt.Errorf("scroll: %w", err)
	}
	return &scrollResult{ScrollY: y}, nil
}

func cmdSee(s *Session, o obsOpts) (any, error) {
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	obs, err := s.Observe(ctx, o)
	if err != nil {
		return nil, err
	}
	return &obs, nil
}

type mapQuery struct {
	Frame  string
	Within string
	Role   string
	Name   string
	Offset int
	Limit  int
}

func cmdMap(s *Session, q mapQuery) (any, error) {
	if q.Offset < 0 || q.Limit < 0 {
		return nil, usagef("--offset/--limit must be non-negative integers")
	}
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	var handles []nodeHandle
	var total int
	var err error
	frameID := ""
	if q.Frame != "" {
		fi, ferr := resolveFrame(ctx, q.Frame)
		if ferr != nil {
			return nil, ferr
		}
		frameID = fi.Frame
		handles, total, err = enumHandlesInFrame(ctx, cdp.FrameID(fi.Frame), enumQuery{
			Selector: mapSelector,
			Within:   q.Within,
			Role:     q.Role,
			Name:     q.Name,
			Offset:   q.Offset,
			Limit:    q.Limit,
		})
	} else {
		handles, total, err = enumHandlesQuery(ctx, enumQuery{
			Selector: mapSelector,
			Within:   q.Within,
			Role:     q.Role,
			Name:     q.Name,
			Offset:   q.Offset,
			Limit:    q.Limit,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("enumerate elements: %w", err)
	}
	items := make([]*mapItem, len(handles))
	for i, h := range handles {
		items[i] = h.mapItem()
	}
	start := q.Offset
	if start > total {
		start = total
	}
	truncated := start+len(items) < total
	res := &mapResult{
		Items:        items,
		Frame:        frameID,
		MatchedTotal: total,
		Returned:     len(items),
		Truncated:    truncated,
	}
	if truncated {
		res.NextOffset = start + len(items)
	}
	return res, nil
}

// waitForTab waits for a new target in /json/list. If wantURL is set and is not about:blank,
// wait until the tab leaves about:blank so it is not returned as created.url.
func waitForTab(ctx context.Context, cdpURL, id, wantURL string, timeout time.Duration) ([]pageEntry, *tabItem, error) {
	var pages []pageEntry
	var found *tabItem
	err := waitUntil(ctx, 20*time.Millisecond, timeout, func() (bool, error) {
		ps, err := listPages(cdpURL)
		if err != nil {
			return false, err
		}
		pages = ps
		for i, p := range ps {
			if p.ID != id {
				continue
			}
			settled := wantURL == "" || wantURL == "about:blank" || p.URL != "about:blank"
			if settled {
				it := tabItemFromPage(p, i)
				found = &it
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil && !errors.Is(err, errWaitTimeout) {
		return nil, nil, err
	}
	if found != nil {
		return pages, found, nil
	}
	// The deadline passed but the target exists: return its current state instead of
	// reporting a created page as failed while it is still about:blank.
	if idx := pageIndex(pages, id); idx >= 0 {
		it := tabItemFromPage(pages[idx], idx)
		return pages, &it, nil
	}
	return pages, nil, nil
}

// cmdTabNew creates a background tab without activating it. Callers must run
// tinybk tab activate <id> before sending real input. created.id is stable across calls.
func cmdTabNew(s *Session, cdpURL, rawURL string) (any, error) {
	if rawURL == "" {
		rawURL = "about:blank"
	} else {
		rawURL = normalizeURL(rawURL)
	}
	ctx, cut := s.Timeout(navTimeout)
	defer cut()
	background := true
	res, err := cdp.Call(ctx, s.browser, target.CreateTarget,
		target.CreateTargetParams{URL: rawURL, Background: &background})
	if err != nil {
		return nil, fmt.Errorf("create tab: %w", err)
	}
	pages, created, err := waitForTab(ctx, cdpURL, string(res.TargetID), rawURL, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("wait for new tab: %w", err)
	}
	if created == nil {
		return nil, fmt.Errorf("new tab %s did not appear in the tab list", res.TargetID)
	}
	return &tabResult{Created: created, Tabs: toTabs(pages)}, nil
}

// cmdTabList lists tabs without attaching to a page.
func cmdTabList(g globals) (any, error) {
	pages, err := listPages(g.cdpURL)
	if err != nil {
		return nil, fmt.Errorf("list tabs: %w", err)
	}
	return &tabResult{Tabs: toTabs(pages)}, nil
}

func pageIndex(pages []pageEntry, id string) int {
	for i, p := range pages {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// requirePage re-lists pages, verifies the target still exists, and returns the authoritative list and index.
func requirePage(g globals, page *pageEntry, input string) ([]pageEntry, int, error) {
	pages, err := listPages(g.cdpURL)
	if err != nil {
		return nil, -1, fmt.Errorf("list tabs: %w", err)
	}
	idx := pageIndex(pages, page.ID)
	if idx < 0 {
		return nil, -1, errTargetNotFound(input, pages)
	}
	return pages, idx, nil
}

// cmdTabClose closes a tab by id/prefix. The runner acquires target -> browser locks;
// this function only performs the authoritative check and close under the browser lock.
func cmdTabClose(s *Session, g globals, page *pageEntry, input string) (any, error) {
	pages, idx, err := requirePage(g, page, input)
	if err != nil {
		return nil, err
	}
	if len(pages) == 1 {
		return nil, fmt.Errorf("only one tab remains; closing it would close the browser")
	}
	closed := pages[idx]
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	if _, err := cdp.Call(ctx, s.browser, target.CloseTarget,
		target.CloseTargetParams{TargetID: target.ID(closed.ID)}); err != nil {
		return nil, fmt.Errorf("close tab %s: %w", input, err)
	}
	err = waitUntil(ctx, 20*time.Millisecond, time.Second, func() (bool, error) {
		ps, err := listPages(g.cdpURL)
		if err != nil {
			return false, fmt.Errorf("list tabs: %w", err)
		}
		pages = ps
		return pageIndex(ps, closed.ID) < 0, nil
	})
	if err != nil && !errors.Is(err, errWaitTimeout) {
		return nil, err
	}
	item := tabItemFromPage(closed, idx)
	return &tabResult{Closed: &item, Tabs: toTabs(pages)}, nil
}

// activationFail reports that ActivateTarget was sent but the tab stayed invisible.
func activationFail(fullID, vis string) *cmdError {
	return fail(codeActivationTimeout,
		fmt.Sprintf("requested activation of tab %s, but visibility is still %s after %s; check that the window is not minimized or covered", shortID(fullID), activateWait, vis),
		map[string]any{"short": shortID(fullID), "target_id": fullID, "visibility": vis})
}

// cmdTabActivate activates a tab and waits until it is actually visible.
// It is the only command that changes the foreground tab; click/type/press never do it implicitly.
// The runner acquires the target lock before this call.
func cmdTabActivate(s *Session, g globals, page *pageEntry, input string) (any, error) {
	fullID := page.ID
	if _, _, err := requirePage(g, page, input); err != nil {
		return nil, err
	}
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	if _, err := cdp.Call(ctx, s.browser, target.ActivateTarget,
		target.ActivateTargetParams{TargetID: target.ID(fullID)}); err != nil {
		return nil, fmt.Errorf("activate tab %s: %w", fullID, err)
	}

	// Read visibilityState through a separate session. /json/list order only reflects
	// the current list order; it does not prove the page is visible, which determines
	// whether input is delivered.
	ts, err := Connect(g.cdpURL, fullID, 0, false)
	if err != nil {
		return nil, fmt.Errorf("connect to tab after activation %s: %w", fullID, err)
	}
	defer ts.Close()
	var vis string
	err = waitUntil(ctx, 50*time.Millisecond, activateWait, func() (bool, error) {
		vctx, vcut := ts.Timeout(evalTimeout)
		v, verr := chromedp.Run(vctx, chromedp.Evaluate[string]("document.visibilityState"))
		vcut()
		if verr == nil {
			vis = v
			return v == "visible", nil
		}
		return false, nil
	})
	if err != nil {
		return nil, activationFail(fullID, vis)
	}

	pages, err := listPages(g.cdpURL)
	if err != nil {
		return nil, fmt.Errorf("list tabs: %w", err)
	}
	idx := pageIndex(pages, fullID)
	if idx < 0 {
		return nil, errTargetNotFound(input, pages)
	}
	tabs := toTabs(pages)
	return &tabActivateResult{Activated: &tabs[idx], Visibility: vis, Tabs: tabs}, nil
}

// waitMode selects the wait condition; the default waits for element visibility.
type waitMode int

const (
	waitVisible waitMode = iota // CSS element is visible
	waitGone                    // CSS element is gone
	waitURL                     // location.href contains the substring
	waitText                    // body text contains the substring
	waitStable                  // DOM has no mutations for the quiet period
)

// waitCond is one condition: mode selects arg or quiet.
type waitCond struct {
	mode  waitMode
	arg   string
	quiet time.Duration
}

func (c waitCond) String() string {
	switch c.mode {
	case waitGone:
		return fmt.Sprintf("%q disappears", c.arg)
	case waitURL:
		return fmt.Sprintf("URL contains %q", c.arg)
	case waitText:
		return fmt.Sprintf("body text contains %q", c.arg)
	case waitStable:
		return fmt.Sprintf("DOM stable for %dms", c.quiet.Milliseconds())
	default:
		return fmt.Sprintf("%q is visible", c.arg)
	}
}

func cmdWait(s *Session, c waitCond, timeout time.Duration) (any, error) {
	ctx, cut := s.Timeout(timeout)
	defer cut()
	start := time.Now()
	var err error
	switch c.mode {
	case waitGone:
		err = pollJS(ctx, fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return true;
  const r = el.getBoundingClientRect();
  return getComputedStyle(el).display === 'none' || getComputedStyle(el).visibility === 'hidden' || r.width === 0 || r.height === 0;
})()`, strconv.Quote(c.arg)), timeout)
	case waitURL:
		err = pollJS(ctx, fmt.Sprintf(`location.href.includes(%s)`, strconv.Quote(c.arg)), timeout)
	case waitText:
		err = pollJS(ctx, fmt.Sprintf(`document.body && document.body.innerText.includes(%s)`, strconv.Quote(c.arg)), timeout)
	case waitStable:
		err = waitDOMStable(ctx, c.quiet)
	default:
		_, err = chromedp.Run(ctx, chromedp.WaitVisible(chromedp.CSS(c.arg)))
	}
	if err != nil {
		return nil, fmt.Errorf("wait for %s: %w", c, err)
	}
	info, err := s.TargetInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("read tab state: %w", err)
	}
	return &waitResult{
		Waited: time.Since(start).Milliseconds(),
		URL:    info.URL,
		Title:  info.Title,
	}, nil
}

// waitDOMStable waits for a quiet period with no DOM mutations: nodes, text,
// and attributes all count. Continuous changes end when the context times out.
func waitDOMStable(ctx context.Context, quiet time.Duration) error {
	ms := quiet.Milliseconds()
	js := fmt.Sprintf(`new Promise((resolve) => {
  const done = () => { obs.disconnect(); resolve(true); };
  let timer = setTimeout(done, %d);
  const obs = new MutationObserver(() => { clearTimeout(timer); timer = setTimeout(done, %d); });
  obs.observe(document.documentElement, {childList: true, subtree: true, characterData: true, attributes: true});
})`, ms, ms)
	await := true
	_, err := chromedp.Run(ctx, chromedp.Evaluate[bool](js, func(p *runtime.EvaluateParams) {
		p.AwaitPromise = &await
	}))
	return err
}

func cmdEval(s *Session, js string, await bool) (any, error) {
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	// By default, wait for the Promise to settle: async expressions such as fetch or
	// an async IIFE return a Promise, and without waiting the result is just {}.
	// --no-wait returns immediately for background work or a current snapshot.
	out, err := chromedp.Run(ctx, chromedp.Evaluate[[]byte](js, func(p *runtime.EvaluateParams) {
		if await {
			p.AwaitPromise = &await
		}
	}))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("eval timed out (%s): page JS may still be running; use --no-wait for background work", evalTimeout)
		}
		return nil, fmt.Errorf("eval: %w", err)
	}
	if len(out) == 0 {
		out = []byte("null")
	}
	return &evalResult{Result: json.RawMessage(out)}, nil
}

func cmdDoctor(cdpURL string) (any, error) {
	pages, err := listPages(cdpURL)
	if err != nil {
		return nil, fmt.Errorf("%w\nStart a browser:\n  %s", err, launchHint)
	}
	res := &doctorResult{CDP: cdpURL, Tabs: toTabs(pages)}
	if v, err := browserProduct(cdpURL); err == nil {
		res.Browser = v
	}
	return res, nil
}
