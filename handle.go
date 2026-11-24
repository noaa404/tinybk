package main

// Element layer: refs, enumeration, location, field evidence, and label matching.
// A ref=@id id is a CDP backendNodeId and points to the same element across calls.
// After a reload or navigation, DOM.resolveNode rejects the old id, so refs fail
// closed and no server-side state is needed.
//
// All element scripts share bkNorm/bkFields/bkNames/bkLocate from elementHelpersJS.
// Scripts no longer depend on callers predefining el, r, hit, blocker, or blank.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// handleGroup is the object group for one invocation; release the whole group at the end.
const handleGroup = "tinybk-handles"

// enumSelector is the candidate set for click label=.
// map uses the wider mapSelector below for discovery.
const enumSelector = `a[href], button, input, select, textarea, [role="button"]`

// mapSelector is map's discovery set: enumSelector plus common interactive ARIA roles.
// Discovery can be broader; click still decides what it will execute.
const mapSelector = enumSelector + `, [role="link"], [role="checkbox"], [role="radio"], [role="option"], [role="tab"], [role="menuitem"], [role="menuitemcheckbox"], [role="menuitemradio"], [role="switch"], [role="treeitem"], [role="combobox"], [role="listbox"], [role="slider"], [role="spinbutton"], [role="textbox"], [role="searchbox"]`

// Shared element helpers: normalization, fields, names, and location.
// Each script inlines them and does not depend on caller-provided variables.
const elementHelpersJS = `
  const bkNorm = (s) => (s || '').replace(/\u00a0/g, ' ').replace(/\s+/g, ' ').trim();
  const bkRole = (el) => {
    const explicit = (el.getAttribute('role') || '').trim().split(/\s+/)[0];
    if (explicit) return explicit.toLowerCase();
    const tag = el.tagName.toLowerCase();
    if (tag === 'a') return el.hasAttribute('href') ? 'link' : '';
    if (tag === 'button') return 'button';
    if (tag === 'select') return (el.multiple || el.size > 1) ? 'listbox' : 'combobox';
    if (tag === 'textarea') return 'textbox';
    if (tag === 'summary') return 'button';
    if (tag === 'input') {
      const t = (el.type || 'text').toLowerCase();
      if (t === 'checkbox') return 'checkbox';
      if (t === 'radio') return 'radio';
      if (t === 'range') return 'slider';
      if (t === 'number') return 'spinbutton';
      if (t === 'search') return 'searchbox';
      if (t === 'button' || t === 'submit' || t === 'reset' || t === 'image' || t === 'file') return 'button';
      if (t === 'hidden') return '';
      return 'textbox';
    }
    return '';
  };
  const bkNames = (el) => {
    const out = [];
    const add = (k, v) => {
      const n = bkNorm(v);
      if (n && !out.some((x) => x.v === n)) out.push({k: k, v: n});
    };
    add('aria', el.getAttribute('aria-label'));
    add('text', el.innerText);
    add('value', el.value);
    add('placeholder', el.placeholder);
    add('title', el.getAttribute('title'));
    return out;
  };
  const bkFields = (el) => ({
    tag: el.tagName.toLowerCase(),
    role: bkRole(el),
    text: ((el.innerText || el.value || el.placeholder || '') + '').trim().replace(/\s+/g, ' ').slice(0, 60),
    href: el.getAttribute('href') || '',
    id: el.id || '',
    name: el.getAttribute('name') || ''
  });
  const bkFrameInfo = (el) => {
    let x = 0, y = 0;
    const frames = [];
    let w = el.ownerDocument.defaultView;
    while (w && w !== w.top) {
      let fe = null;
      try { fe = w.frameElement; } catch (e) { return {x: 0, y: 0, frames: [], error: 'cross-origin iframe is not supported'}; }
      if (!fe) break;
      const fr = fe.getBoundingClientRect();
      const cs = fe.ownerDocument.defaultView.getComputedStyle(fe);
      x += fr.left + parseFloat(cs.borderLeftWidth || 0) + parseFloat(cs.paddingLeft || 0);
      y += fr.top + parseFloat(cs.borderTopWidth || 0) + parseFloat(cs.paddingTop || 0);
      frames.push(fe);
      w = fe.ownerDocument.defaultView;
    }
    return {x: x, y: y, frames: frames, error: ''};
  };
  const bkScrollFrames = (el) => {
    let w = el.ownerDocument.defaultView;
    while (w && w !== w.top) {
      let fe = null;
      try { fe = w.frameElement; } catch (e) { break; }
      if (!fe) break;
      fe.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
      w = fe.ownerDocument.defaultView;
    }
  };
  const bkEnum = (selector, withinSel, role, name, doc) => {
    const roleQ = (role || '').toLowerCase();
    const nameQ = bkNorm(name).toLowerCase();
    const roleAliases = {choice: ['radio', 'checkbox', 'option', 'menuitemcheckbox', 'menuitemradio', 'switch']};
    const wanted = roleQ ? roleQ.split(',').map((s) => s.trim()).filter(Boolean).reduce((a, r) => a.concat(roleAliases[r] || [r]), []) : [];
    const seen = new Set();
    const out = [];
    const add = (el) => {
      if (!el || seen.has(el)) return;
      if (el.closest('[aria-hidden="true"]')) return;
      const r = el.getBoundingClientRect();
      if (r.width <= 0 || r.height <= 0) return;
      if (wanted.length && !wanted.includes(bkRole(el))) return;
      if (nameQ && !bkNames(el).some((n) => bkNorm(n.v).toLowerCase().includes(nameQ))) return;
      seen.add(el);
      out.push(el);
    };
    const roots = withinSel ? Array.from(doc.querySelectorAll(withinSel)) : [doc];
    for (const root of roots) {
      if (root.nodeType === 1 && root.matches(selector)) add(root);
      for (const el of root.querySelectorAll(selector)) add(el);
    }
    return out;
  };
  const bkLocate = async (el, focus, doScroll) => {
    if (doScroll) {
      el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
      bkScrollFrames(el);
    }
    if (focus) el.focus();
    // Wait until the local rect and iframe offset are stable for two frames.
    // Fall back to 500ms when rAF is frozen.
    const snapshot = () => ({rect: el.getBoundingClientRect(), fi: bkFrameInfo(el)});
    const r = await new Promise((resolve) => {
      let last = '';
      let stable = 0;
      const tick = () => {
        const s = snapshot();
        const key = s.rect.x + ',' + s.rect.y + ',' + s.rect.width + ',' + s.rect.height + '|' + s.fi.x + ',' + s.fi.y + ',' + s.fi.frames.length + ',' + s.fi.error;
        stable = key === last ? stable + 1 : 0;
        last = key;
        if (stable >= 2) { resolve(s); return; }
        requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
      setTimeout(() => resolve(snapshot()), 500);
    });
    const localX = r.rect.x + r.rect.width / 2;
    const localY = r.rect.y + r.rect.height / 2;
    const topX = localX + r.fi.x;
    const topY = localY + r.fi.y;
    const innerDoc = el.ownerDocument;
    const innerAt = innerDoc.elementFromPoint(localX, localY);
    const innerHit = !!innerAt && (innerAt === el || el.contains(innerAt) || innerAt.contains(el));
    let topAt = innerAt;
    let topHit = innerHit;
    if (r.fi.frames.length) {
      const outer = r.fi.frames[r.fi.frames.length - 1];
      const topDoc = outer.ownerDocument;
      topAt = topDoc.elementFromPoint(topX, topY);
      topHit = !!topAt && (topAt === outer || outer.contains(topAt) || topAt.contains(outer));
    }
    const hit = !r.fi.error && innerHit && topHit;
    const blockerEl = !innerHit ? innerAt : (!topHit ? topAt : null);
    const blocker = r.fi.error || (blockerEl ? blockerEl.tagName.toLowerCase() + (blockerEl.id ? '#' + blockerEl.id : '') : 'element outside viewport');
    const blankEl = el.closest('a[target], area[target], form[target]');
    const blank = !!blankEl && (blankEl.getAttribute('target') || '').toLowerCase() === '_blank';
    return Object.assign(bkFields(el), {
      x: topX,
      y: topY,
      hit: hit,
      blocker: hit ? '' : blocker,
      blank: blank
    });
  };
`

// enumNodesJS and enumFrameNodesJS only filter and return element objects;
// handleFieldsRangeJS reads fields. Filtering only affects discovery; it never clicks.
func enumNodesJS(selector, within, role, name string) string {
	return `(() => {
` + elementHelpersJS + `
  return bkEnum(` + strconv.Quote(selector) + `, ` + strconv.Quote(within) + `, ` + strconv.Quote(role) + `, ` + strconv.Quote(name) + `, document);
})()`
}

// enumFrameNodesJS runs on a frame window; this.document is that frame's document.
// This MVP supports same-origin iframes only.
func enumFrameNodesJS(selector, within, role, name string) string {
	return `function () {
` + elementHelpersJS + `
  return bkEnum(` + strconv.Quote(selector) + `, ` + strconv.Quote(within) + `, ` + strconv.Quote(role) + `, ` + strconv.Quote(name) + `, this.document);
}`
}

// handleFieldsRangeJS reads display fields and all matchable names in one pass.
// Name order is the label priority: aria > text > value > placeholder > title.
func handleFieldsRangeJS(start, end int) string {
	return `function () {
` + elementHelpersJS + `  return this.slice(` + strconv.Itoa(start) + `, ` + strconv.Itoa(end) + `).map((el) => Object.assign(bkFields(el), {names: bkNames(el)}));
}`
}

// labelNamed is one name and its source (aria, text, value, placeholder, or title).
type labelNamed struct {
	Kind string `json:"k"` // aria, text, value, placeholder, or title
	Val  string `json:"v"`
}

// nodeHandle uniquely represents an element: identity, display fields, and
// matchable names. map projects it through mapItem(); label errors serialize
// the handle without Bid.
type nodeHandle struct {
	Ref   string            `json:"ref"`
	Bid   cdp.BackendNodeID `json:"-"`
	Tag   string            `json:"tag,omitempty"`
	Role  string            `json:"role,omitempty"`
	Text  string            `json:"text,omitempty"`
	Href  string            `json:"href,omitempty"`
	ID    string            `json:"id,omitempty"`
	Name  string            `json:"name,omitempty"`
	Names []labelNamed      `json:"names,omitempty"`
}

func (h nodeHandle) mapItem() *mapItem {
	return &mapItem{Ref: h.Ref, Tag: h.Tag, Role: h.Role, Text: h.Text, Href: h.Href, ID: h.ID, Name: h.Name}
}

func (h nodeHandle) primaryName() (string, string) {
	if len(h.Names) == 0 {
		return "", ""
	}
	return h.Names[0].Val, h.Names[0].Kind
}

// describe returns one candidate line, e.g. e12 <button> "Add to basket"(text).
func (h nodeHandle) describe() string {
	name, kind := h.primaryName()
	if name == "" {
		return fmt.Sprintf("%s <%s>", h.Ref, h.Tag)
	}
	return fmt.Sprintf("%s <%s> %q(%s)", h.Ref, h.Tag, name, kind)
}

// mapItem is map's slim projection; matching-only fields such as names stay out of output.
type mapItem struct {
	Ref  string `json:"ref"`
	Tag  string `json:"tag"`
	Role string `json:"role,omitempty"`
	Text string `json:"text,omitempty"`
	Href string `json:"href,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// refString encodes a handle as @ plus the base36 backendNodeId. @ marks identity;
// refs are never positional indexes.
func refString(bid cdp.BackendNodeID) string {
	return "@" + strconv.FormatInt(int64(bid), 36)
}

func parseBid(s string) (cdp.BackendNodeID, error) {
	v, err := strconv.ParseInt(s, 36, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid node id %q", s)
	}
	return cdp.BackendNodeID(v), nil
}

// parseRef parses the text after ref=; it must be @base36id.
// Reject legacy forms without @ (e5, 5) to avoid positional clicks.
func parseRef(rest string) (cdp.BackendNodeID, error) {
	if !strings.HasPrefix(rest, "@") {
		return 0, fmt.Errorf("ref is missing the @ identity marker")
	}
	return parseBid(rest[1:])
}

// elemInfo is an element summary returned as evidence of what was actually targeted.
type elemInfo struct {
	Tag   string `json:"tag,omitempty"`
	Text  string `json:"text,omitempty"`
	Href  string `json:"href,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Label string `json:"label,omitempty"`
	Blank bool   `json:"blank,omitempty"`
}

// jsLocated is the location-script result; null maps to nil in Go.
type jsLocated struct {
	elemInfo
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Hit     bool    `json:"hit"`
	Blocker string  `json:"blocker"`
}

func cdpCall[P, R any](ctx context.Context, cmd cdp.Command[P, R], p P) (R, error) {
	return cdp.Call(ctx, chromedp.FromContext(ctx).Target, cmd, p)
}

// callOn runs a function on an element and returns its JSON value; Promises are awaited.
func callOn(ctx context.Context, objID runtime.RemoteObjectID, fn string) ([]byte, error) {
	byVal := true
	awaitPromise := true
	out, err := cdpCall(ctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		ObjectID:            objID,
		FunctionDeclaration: fn,
		ReturnByValue:       &byVal,
		AwaitPromise:        &awaitPromise,
	})
	if err != nil {
		return nil, err
	}
	if out.ExceptionDetails != nil {
		return nil, fmt.Errorf("page script exception: %s", out.ExceptionDetails.Text)
	}
	if out.Result == nil {
		return nil, fmt.Errorf("page script returned no value")
	}
	return out.Result.Value, nil
}

// callOnRef runs a function and returns an object by reference (used for iframe windows).
func callOnRef(ctx context.Context, objID runtime.RemoteObjectID, fn string) (runtime.RemoteObjectID, error) {
	byVal := false
	awaitPromise := true
	out, err := cdpCall(ctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		ObjectID:            objID,
		FunctionDeclaration: fn,
		ReturnByValue:       &byVal,
		AwaitPromise:        &awaitPromise,
	})
	if err != nil {
		return "", err
	}
	if out.ExceptionDetails != nil {
		return "", fmt.Errorf("page script exception: %s", out.ExceptionDetails.Text)
	}
	if out.Result == nil || out.Result.ObjectID == "" {
		return "", fmt.Errorf("page script returned no object")
	}
	return out.Result.ObjectID, nil
}

// enumQuery describes an enumeration: selector chooses candidates; within/role/name
// only filter discovery. offset/limit affect returned items; total is the full
// filtered count.
type enumQuery struct {
	Selector string
	Within   string
	Role     string
	Name     string
	Offset   int
	Limit    int
}

// enumHandles is the existing click label= entry point: it uses the original target set
// and is not affected by map's discovery set or filters.
func enumHandles(ctx context.Context) ([]nodeHandle, error) {
	handles, _, err := enumHandlesQuery(ctx, enumQuery{Selector: enumSelector})
	return handles, err
}

// enumHandlesQuery enumerates the top page and creates handles for returned elements.
func enumHandlesQuery(ctx context.Context, q enumQuery) ([]nodeHandle, int, error) {
	q = normalizeEnumQuery(q)
	if q.Offset < 0 || q.Limit < 0 {
		return nil, 0, usagef("--offset/--limit must be non-negative integers")
	}
	no := false
	res, err := cdpCall(ctx, runtime.Evaluate, runtime.EvaluateParams{
		Expression:    enumNodesJS(q.Selector, q.Within, q.Role, q.Name),
		ObjectGroup:   handleGroup,
		ReturnByValue: &no,
	})
	if err != nil {
		return nil, 0, err
	}
	if res.ExceptionDetails != nil {
		return nil, 0, fmt.Errorf("enumerate elements failed: %s", res.ExceptionDetails.Text)
	}
	if res.Result == nil || res.Result.ObjectID == "" {
		return nil, 0, fmt.Errorf("enumeration result is not an object")
	}
	defer cdpCall(ctx, runtime.ReleaseObjectGroup,
		runtime.ReleaseObjectGroupParams{ObjectGroup: handleGroup})
	return enumHandlesFromArray(ctx, res.Result.ObjectID, q.Offset, q.Limit)
}

// enumHandlesInFrame enumerates inside a same-origin iframe window.
func enumHandlesInFrame(ctx context.Context, frameID cdp.FrameID, q enumQuery) ([]nodeHandle, int, error) {
	q = normalizeEnumQuery(q)
	if q.Offset < 0 || q.Limit < 0 {
		return nil, 0, usagef("--offset/--limit must be non-negative integers")
	}
	winObj, err := frameWindowObject(ctx, frameID)
	if err != nil {
		return nil, 0, err
	}
	defer cdpCall(ctx, runtime.ReleaseObjectGroup,
		runtime.ReleaseObjectGroupParams{ObjectGroup: handleGroup})
	arrObj, err := callOnRef(ctx, winObj, enumFrameNodesJS(q.Selector, q.Within, q.Role, q.Name))
	if err != nil {
		return nil, 0, fmt.Errorf("enumerate iframe elements: %w", err)
	}
	return enumHandlesFromArray(ctx, arrObj, q.Offset, q.Limit)
}

func normalizeEnumQuery(q enumQuery) enumQuery {
	if q.Selector == "" {
		q.Selector = enumSelector
	}
	return q
}

// enumHandlesFromArray converts an element array into handles. It describes only
// elements in the returned page; total is the full filtered count.
func enumHandlesFromArray(ctx context.Context, arrayObj runtime.RemoteObjectID, offset, limit int) ([]nodeHandle, int, error) {
	own := true
	props, err := cdpCall(ctx, runtime.GetProperties, runtime.GetPropertiesParams{
		ObjectID:      arrayObj,
		OwnProperties: &own,
	})
	if err != nil {
		return nil, 0, err
	}
	var objs []runtime.RemoteObjectID
	for _, p := range props.Result {
		if p.Value == nil || p.Value.ObjectID == "" {
			continue // Primitive values such as array length have no objectId
		}
		objs = append(objs, p.Value.ObjectID)
	}

	total := len(objs)
	start := offset
	if start > total {
		start = total
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	bids := make([]cdp.BackendNodeID, 0, end-start)
	for _, objID := range objs[start:end] {
		d, err := cdpCall(ctx, dom.DescribeNode, dom.DescribeNodeParams{ObjectID: objID})
		if err != nil {
			return nil, 0, fmt.Errorf("get element node id: %w", err)
		}
		if d.Node == nil {
			return nil, 0, fmt.Errorf("element has no node info")
		}
		bids = append(bids, d.Node.BackendNodeID)
	}

	raw, err := callOn(ctx, arrayObj, handleFieldsRangeJS(start, end))
	if err != nil {
		return nil, 0, err
	}
	var out []nodeHandle
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, err
	}
	if len(out) != len(bids) {
		return nil, 0, fmt.Errorf("enumeration mismatch: %d identities / %d field records", len(bids), len(out))
	}
	for i := range out {
		out[i].Ref = refString(bids[i])
		out[i].Bid = bids[i]
	}
	return out, total, nil
}

// resolveBid resolves a backendNodeId back to a page element. After a reload or
// navigation, the DOM agent rejects the old id and the error is returned unchanged.
func resolveBid(ctx context.Context, bid cdp.BackendNodeID) (runtime.RemoteObjectID, error) {
	res, err := cdpCall(ctx, dom.ResolveNode, dom.ResolveNodeParams{
		BackendNodeID: bid,
		ObjectGroup:   handleGroup,
	})
	if err != nil {
		return "", err
	}
	if res.Object == nil || res.Object.ObjectID == "" {
		return "", fmt.Errorf("node no longer exists")
	}
	return res.Object.ObjectID, nil
}

// locateBidJS scrolls the element, optionally focuses it, and returns its viewport center and info.
func locateBidJS(focus, scroll bool) string {
	return `async function () {
` + elementHelpersJS + `  return await bkLocate(this, ` + strconv.FormatBool(focus) + `, ` + strconv.FormatBool(scroll) + `);
}`
}

// withBid resolves a node identity, runs fn on the element, and decodes the result into T.
func withBid[T any](ctx context.Context, bid cdp.BackendNodeID, fn string) (T, error) {
	var v T
	objID, err := resolveBid(ctx, bid)
	if err != nil {
		return v, err
	}
	defer cdpCall(ctx, runtime.ReleaseObjectGroup,
		runtime.ReleaseObjectGroupParams{ObjectGroup: handleGroup})
	raw, err := callOn(ctx, objID, fn)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v)
	return v, err
}

func locateByBid(ctx context.Context, bid cdp.BackendNodeID, focus, scroll bool) (*jsLocated, error) {
	return withBid[*jsLocated](ctx, bid, locateBidJS(focus, scroll))
}

// labelBidJS rechecks that the name still matches exactly or by prefix, then
// scrolls and optionally focuses. Identity is fixed; the recheck only catches
// page-side name changes. Null means the element no longer matches.
func labelBidJS(query string, focus, scroll bool) string {
	return `async function () {
` + elementHelpersJS + `  const q = bkNorm(` + strconv.Quote(query) + `).toLowerCase();
  const matched = bkNames(this).find((n) => {
    const v = n.v.toLowerCase();
    return v === q || v.indexOf(q) === 0;
  });
  if (!matched) return null;
  const o = await bkLocate(this, ` + strconv.FormatBool(focus) + `, ` + strconv.FormatBool(scroll) + `);
  o.label = matched.v;
  return o;
}`
}

func locateLabelByBid(ctx context.Context, bid cdp.BackendNodeID, query string, focus, scroll bool) (*jsLocated, error) {
	return withBid[*jsLocated](ctx, bid, labelBidJS(query, focus, scroll))
}

// valueBidJS reads the current value as type's input evidence.
const valueBidJS = `function () {
  return ((this.value ?? '') + '');
}`

func valueByBid(ctx context.Context, bid cdp.BackendNodeID) (string, error) {
	return withBid[string](ctx, bid, valueBidJS)
}

// valueCSSJS is value evidence for CSS targets; ref targets re-read by node identity instead.
func valueCSSJS(css string) string {
	return `(() => {
  const el = document.querySelector(` + strconv.Quote(css) + `);
  return el ? ((el.value ?? '') + '') : '';
})()`
}

// elementTarget is a click/type target: exactly one of node identity (ref),
// label= text, or CSS.
type elementTarget struct {
	bid   cdp.BackendNodeID
	css   string
	label string
}

func parseTarget(s string) (elementTarget, error) {
	if rest, ok := strings.CutPrefix(s, "label="); ok {
		q := strings.TrimSpace(rest)
		if q == "" {
			return elementTarget{}, usagef("label must not be empty; use label=text")
		}
		return elementTarget{label: q}, nil
	}
	if rest, ok := strings.CutPrefix(s, "ref="); ok {
		bid, err := parseRef(rest)
		if err != nil {
			return elementTarget{}, usagef("invalid target %q; use ref=@<id from map>, label=text, or a CSS selector", s)
		}
		return elementTarget{bid: bid}, nil
	}
	if s == "" {
		return elementTarget{}, usagef("target must not be empty")
	}
	return elementTarget{css: s}, nil
}

// parseTypeTarget is parseTarget but rejects label=: a name query may match
// a button or link, so typing could silently miss or hit the wrong element.
func parseTypeTarget(s string) (elementTarget, error) {
	if strings.HasPrefix(s, "label=") {
		return elementTarget{}, usagef("type does not support label=: a name match may hit a non-input element; use map and ref=@<id>, or a CSS selector")
	}
	return parseTarget(s)
}

func (t elementTarget) String() string {
	switch {
	case t.bid != 0:
		return "ref=" + refString(t.bid)
	case t.label != "":
		return fmt.Sprintf("label %q", t.label)
	default:
		return fmt.Sprintf("selector %q", t.css)
	}
}

// located combines the raw location result with node identity (0 for CSS targets).
type located struct {
	jsLocated
	bid cdp.BackendNodeID
}

// locateCSSJS locates an element by CSS and returns coordinates and info; null means missing.
// Use strconv.Quote for JS string literals: CSS backslashes (for example Tailwind's
// md\:flex) must be preserved; hand-rolled single-quote escaping would turn \: into :.
func locateCSSJS(css string, focus, scroll bool) string {
	return `(async () => {
` + elementHelpersJS + `  const el = document.querySelector(` + strconv.Quote(css) + `);
  if (!el) return null;
  return await bkLocate(el, ` + strconv.FormatBool(focus) + `, ` + strconv.FormatBool(scroll) + `);
})()`
}

// locate scrolls an element into view and optionally focuses it before keyboard input.
func locate(s *Session, t elementTarget, focus bool, frame string) (located, error) {
	return locateWithScroll(s, t, focus, frame, true)
}

// locateWithScroll is the underlying locator. drag passes scroll=false so source and
// target are already in the viewport and locating one cannot scroll the other away.
func locateWithScroll(s *Session, t elementTarget, focus bool, frame string, scroll bool) (located, error) {
	switch {
	case t.label != "":
		return locateLabel(s, t, focus, frame, scroll)
	case t.bid != 0:
		ctx, cut := s.Timeout(evalTimeout)
		defer cut()
		res, err := locateByBid(ctx, t.bid, focus, scroll)
		if err != nil {
			return located{}, fmt.Errorf(
				"%s is stale (page may have reloaded or the element was removed); re-run map: %w", t, err)
		}
		if res == nil {
			return located{}, fmt.Errorf("%s not found; the page may have changed; re-run map", t)
		}
		return locatedFromJS(res, t.bid), nil
	default:
		if frame != "" {
			return located{}, usagef("--frame currently supports label= or ref= only; for CSS, run tinybk map --frame and use the returned ref")
		}
		ctx, cut := s.Timeout(evalTimeout)
		defer cut()
		res, err := chromedp.Run(ctx, chromedp.Evaluate[*jsLocated](
			locateCSSJS(t.css, focus, scroll), chromedp.EvalAwaitPromise))
		if err != nil {
			return located{}, fmt.Errorf("locate %s: %w", t, err)
		}
		if res == nil {
			return located{}, fmt.Errorf("%s not found; the page may have changed; re-run map", t)
		}
		return locatedFromJS(res, 0), nil
	}
}

// locatedFromJS wraps a location result; bid=0 means a CSS target.
func locatedFromJS(res *jsLocated, bid cdp.BackendNodeID) located {
	return located{jsLocated: *res, bid: bid}
}

// locateLabel enumerates candidates, matches in Go, then rechecks and locates
// by node identity. Once identity is fixed, page changes can only fail the
// recheck; they cannot cause a positional mis-click.
func locateLabel(s *Session, t elementTarget, focus bool, frame string, scroll bool) (located, error) {
	ctx, cut := s.Timeout(evalTimeout)
	defer cut()
	var handles []nodeHandle
	var err error
	if frame != "" {
		fi, ferr := resolveFrame(ctx, frame)
		if ferr != nil {
			return located{}, ferr
		}
		handles, _, err = enumHandlesInFrame(ctx, cdp.FrameID(fi.Frame), enumQuery{Selector: enumSelector})
	} else {
		handles, err = enumHandles(ctx)
	}
	if err != nil {
		return located{}, fmt.Errorf("enumerate elements: %w", err)
	}
	m, err := matchLabel(handles, t.label)
	if err != nil {
		return located{}, err
	}
	if m.Candidate == nil {
		return located{}, labelError(m)
	}
	res, err := locateLabelByBid(ctx, m.Candidate.Bid, t.label, focus, scroll)
	if err != nil {
		return located{}, fmt.Errorf("locate %s: %w", t, err)
	}
	if res == nil {
		return located{}, fmt.Errorf("the name of %s changed; the page may have updated; search again", t)
	}
	return locatedFromJS(res, m.Candidate.Bid), nil
}

// ---- label= matching: exact > prefix > no action; substrings are suggestions only ----

const (
	labelTierExact  = "exact"  // exact name match
	labelTierPrefix = "prefix" // query is a name prefix
	labelTierNone   = "none"
)

const labelExactN, labelPrefixN = 2, 1

// labelMatch is one lookup result. Candidate and Ambiguous are exclusive: multiple
// same-tier hits are not guessed and all go into Ambiguous. Suggestions appear only
// when Tier=none and are substring hits, not executable.
type labelMatch struct {
	Query          string       `json:"query"`
	Tier           string       `json:"tier"`
	Total          int          `json:"total"`          // total number of enumerated elements
	Name           string       `json:"name,omitempty"` // the actual matched name, as-is
	Candidate      *nodeHandle  `json:"candidate,omitempty"`
	Ambiguous      []nodeHandle `json:"ambiguous,omitempty"`
	AmbiguousTotal int          `json:"ambiguous_total,omitempty"`
	Suggestions    []nodeHandle `json:"suggestions,omitempty"`
}

// labelAmbiguousCap limits returned candidates to keep output small.
const labelAmbiguousCap = 10

// matchLabel finds one element by name. Exact matches win over prefixes.
// Multiple hits at the best tier are returned as candidates, never guessed.
func matchLabel(items []nodeHandle, query string) (labelMatch, error) {
	q := normLabel(query)
	if q == "" {
		return labelMatch{}, usagef("label must not be empty; use label=text")
	}
	m := labelMatch{Query: query, Total: len(items), Tier: labelTierNone}

	best := 0
	name := ""
	var hits, subs []nodeHandle
	for _, c := range items {
		t := 0
		cname := ""
		sub := false
		for _, n := range c.Names {
			nt, ns := rankLabel(normLabel(n.Val), q)
			if nt > t {
				t, cname = nt, n.Val
			}
			sub = sub || ns
		}
		if sub && t == 0 {
			subs = append(subs, c)
		}
		switch {
		case t > best:
			best, hits, name = t, []nodeHandle{c}, cname
		case t == best && t > 0:
			hits = append(hits, c)
		}
	}

	switch best {
	case labelExactN:
		m.Tier = labelTierExact
	case labelPrefixN:
		m.Tier = labelTierPrefix
	default:
		m.Suggestions = capLabelCands(subs)
		return m, nil
	}

	m.Name = name
	if len(hits) == 1 {
		c := hits[0]
		m.Candidate = &c
		return m, nil
	}
	m.AmbiguousTotal = len(hits)
	m.Ambiguous = capLabelCands(hits)
	return m, nil
}

func capLabelCands(cs []nodeHandle) []nodeHandle {
	if len(cs) > labelAmbiguousCap {
		return cs[:labelAmbiguousCap]
	}
	return cs
}

// rankLabel returns the tier and whether the name contains the query; tier 0 is a substring hit.
func rankLabel(name, q string) (int, bool) {
	switch {
	case name == q:
		return labelExactN, true
	case strings.HasPrefix(name, q):
		return labelPrefixN, true
	case strings.Contains(name, q):
		return 0, true
	}
	return 0, false
}

// normLabel matches JS bkNorm: NBSP becomes space, whitespace is collapsed, text is lowercased.
// Any mismatch would degrade exact hits into substring hits.
func normLabel(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.ToLower(strings.TrimSpace(s))
}

// labelError adds structured candidates for ambiguous or missing labels; code distinguishes the two cases.
func labelError(m labelMatch) *cmdError {
	code := codeLabelNotFound
	if len(m.Ambiguous) > 0 {
		code = codeLabelAmbiguous
	}
	return fail(code, labelMatchMessage(m), map[string]any{"label": m})
}

// labelMatchMessage turns a match result into an actionable error message.
func labelMatchMessage(m labelMatch) string {
	switch {
	case len(m.Ambiguous) > 0:
		var b strings.Builder
		fmt.Fprintf(&b, "label %q has %d same-tier matches; not executed (use a more complete name or map and click by ref): ", m.Query, m.AmbiguousTotal)
		for i, c := range m.Ambiguous {
			if i > 0 {
				b.WriteString("；")
			}
			b.WriteString(c.describe())
		}
		if m.AmbiguousTotal > len(m.Ambiguous) {
			fmt.Fprintf(&b, "; ...and %d more", m.AmbiguousTotal-len(m.Ambiguous))
		}
		return b.String()
	case len(m.Suggestions) > 0:
		var b strings.Builder
		fmt.Fprintf(&b, "label %q has no exact or prefix match (%d elements enumerated); close matches: ", m.Query, m.Total)
		for i, c := range m.Suggestions {
			if i > 0 {
				b.WriteString("；")
			}
			b.WriteString(c.describe())
		}
		return b.String()
	default:
		return fmt.Sprintf("label %q matched nothing (%d elements enumerated)", m.Query, m.Total)
	}
}
