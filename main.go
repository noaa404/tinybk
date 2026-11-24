package main

// CLI entry point: parse globals (allowed anywhere) -> look up the command ->
// connect + lock -> execute -> one JSON output. Flags, help, and lock modes live in specs, not in command bodies.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// errHelp means help was printed to stdout; the call succeeded and produced no JSON.
var errHelp = errors.New("help printed")

// version is set by release builds via -ldflags "-X main.version=v0.1.0".
var version = "dev"

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func run(argv []string) int {
	res, action, err := execute(argv)
	if err != nil {
		if errors.Is(err, errHelp) {
			return 0
		}
		ce := asCmdError(err)
		emitError(action, ce)
		return ce.exit
	}
	if res == nil {
		return 0
	}
	if err := emitJSON(res); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write stdout: %v\n", err)
		return 1
	}
	return 0
}

func execute(argv []string) (any, string, error) {
	g, rest, err := parseGlobals(argv)
	if err != nil {
		return nil, "", err
	}
	if g.showVersion {
		fmt.Printf("tinybk %s\n", buildVersion())
		return nil, "", nil
	}
	if g.help {
		if len(rest) > 0 {
			if sp, _ := lookup(rest); sp != nil {
				printCmdHelp(sp)
				return nil, sp.action(), errHelp
			}
		}
		printHelp()
		return nil, "", errHelp
	}
	if len(rest) == 0 {
		return nil, "", usagef("missing command; run tinybk help")
	}
	sp, cmdArgs := lookup(rest)
	if sp == nil {
		name := rest[0]
		if strings.HasPrefix(name, "-") {
			return nil, name, usagef("unknown option %q; run tinybk help", name)
		}
		return nil, name, usagef("unknown command %q; run tinybk help", name)
	}
	action := sp.action()
	if g.target != "" && g.tabSet {
		return nil, action, usagef("--target and --tab are mutually exclusive: --target is a stable identity (id/prefix), --tab is a transient /json/list index; use one")
	}

	fv, args, err := sp.parseFlags(cmdArgs)
	if err != nil {
		return nil, action, err
	}
	if len(args) < sp.minArgs || (sp.maxArgs >= 0 && len(args) > sp.maxArgs) {
		return nil, action, usagef("%s", sp.usage())
	}

	e := &env{g: g, spec: sp, args: args, fv: fv}
	if err := e.prepare(); err != nil {
		return nil, action, err
	}
	defer e.close()

	out, err := sp.run(e)
	if err != nil {
		return nil, action, err
	}
	if a, ok := out.(interface{ setAction(string) }); ok {
		a.setAction(action)
	}
	if sp.scope == scopePage && e.s != nil {
		if s, ok := out.(interface{ setShort(string) }); ok {
			s.setShort(shortID(string(e.s.tabID)))
		}
	}
	return out, action, nil
}

// parseGlobals strips global flags (allowed anywhere) and returns the rest for the command.
// After --, nothing is parsed; this passes text beginning with -.
func parseGlobals(argv []string) (globals, []string, error) {
	g := globals{
		cdpURL:      defaultCDP,
		textLimit:   defaultTextLimit,
		lockTimeout: defaultLockTimeout,
	}
	var rest []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			rest = append(rest, argv[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			rest = append(rest, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		val, hasVal := "", false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, val, hasVal = name[:j], name[j+1:], true
		}
		switch name {
		case "h", "help":
			g.help = true
		case "version":
			g.showVersion = true
		case "shot":
			if hasVal {
				b, err := strconv.ParseBool(val)
				if err != nil {
					return g, nil, usagef("--shot accepts only true/false, got %q", val)
				}
				g.shot = b
			} else {
				g.shot = true
			}
		case "cdp", "target", "tab", "text", "lock-timeout", "shot-dir":
			v, err := takeValue(argv, &i, name, val, hasVal)
			if err != nil {
				return g, nil, err
			}
			switch name {
			case "cdp":
				g.cdpURL = v
			case "target":
				g.target = v
			case "tab":
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return g, nil, usagef("--tab must be a non-negative integer, got %q", v)
				}
				g.tab, g.tabSet = n, true
			case "text":
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return g, nil, usagef("--text must be a non-negative integer, got %q", v)
				}
				g.textLimit = n
			case "lock-timeout":
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return g, nil, usagef("--lock-timeout must be a non-negative integer, got %q", v)
				}
				g.lockTimeout = n
			case "shot-dir":
				g.shotDir = v
			}

		default:
			rest = append(rest, a)
		}
	}
	return g, rest, nil
}

func takeValue(argv []string, i *int, name, inline string, has bool) (string, error) {
	if has {
		return inline, nil
	}
	if *i+1 >= len(argv) {
		return "", usagef("option --%s requires a value", name)
	}
	*i++
	return argv[*i], nil
}

// ---- command table ----

type pageScope int

const (
	scopeNone    pageScope = iota // no page required
	scopePage                     // attach to one page and lock with lock.page
	scopeAnyPage                  // attach to any page for browser-level commands
)

type lockSpec struct {
	page       lockMode // scopePage: lock mode for the target page
	lockTarget bool     // scopeAnyPage: first lock the page at args[targetArg]
	targetArg  int
	browser    bool // then lock the browser (tab new/close)
}

type flagKind int

const (
	flagBool flagKind = iota
	flagInt
	flagInt64
	flagString
)

type flagDef struct {
	name string
	kind flagKind
	def  any
	help string
}

type spec struct {
	name      string
	args      string
	usageText string
	desc      string
	flags     []flagDef
	examples  []string // full examples for command help
	details   string   // extra semantics/output notes for command help
	minArgs   int
	maxArgs   int // -1 means unlimited
	scope     pageScope
	lock      lockSpec
	run       func(*env) (any, error)
}

func (sp *spec) action() string {
	return strings.ReplaceAll(sp.name, " ", ".")
}

// synopsis returns the full command signature (without the usage prefix) with all flags.
// Main help uses it so new flags appear immediately; command help only adds explanations.
func (sp *spec) synopsis() string {
	if sp.usageText != "" {
		if rest, ok := strings.CutPrefix(sp.usageText, "Usage: tinybk "); ok {
			return strings.ReplaceAll(rest, " | tinybk ", " | ")
		}
		return sp.usageText
	}
	var b strings.Builder
	b.WriteString(sp.name)
	for _, f := range sp.flags {
		switch f.kind {
		case flagBool:
			fmt.Fprintf(&b, " [--%s]", f.name)
		case flagString:
			fmt.Fprintf(&b, " [--%s VALUE]", f.name)
		default:
			fmt.Fprintf(&b, " [--%s N]", f.name)
		}
	}
	if sp.args != "" {
		b.WriteString(" " + sp.args)
	}
	return b.String()
}

func (sp *spec) usage() string {
	if sp.usageText != "" {
		return sp.usageText
	}
	return "Usage: tinybk " + sp.synopsis()
}

// The command table is filled in init because specs reference cmdHelp and help is generated
// from the table; package-level initialization would create a cycle.
var specs []*spec
var specIndex map[string]*spec

func init() {
	specs = []*spec{
		{
			name: "goto", args: "<url>", desc: "Navigate to a URL (adds https:// when missing)",
			minArgs: 1, maxArgs: 1, scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) { return cmdGoto(e.s, e.args[0], e.g.obs()) },
		},
		{
			name: "back", desc: "Go back one page",
			scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) { return cmdBack(e.s) },
		},
		{
			name: "click", args: "<css|ref=@<id>|label=text>", desc: "Click by CSS, ref, or a name match",
			flags: []flagDef{
				{"force", flagBool, false, "Skip landing validation and click even if covered"},
				{"frame", flagString, "", "Match label= inside a same-origin iframe; get frame from tinybk frames"},
			},
			examples: []string{
				"tinybk click ref=@2f",
				"tinybk click label=Login",
				"tinybk click --frame <short> 'label=Confirm'",
			},
			details: `click requires a visible target page (activate background tabs first).
--frame matches label= inside a same-origin iframe; refs from map --frame carry frame context.
label= matches aria-label, innerText, value, placeholder, and title: exact first,
then prefix; multiple same-tier matches are returned as candidates; substrings are suggestions only.
The landing point is checked before clicking; covered elements fail unless --force is used.
Results include nav (url_changed/content_changed/ready_state/waited_ms); capped=true means the page was still changing and you should wait.
If the action opens a tab, new_tab (id/short/url/title) can be used as --target.`,
			minArgs: 1, maxArgs: 1, scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) {
				return cmdClick(e.s, e.args[0], e.g.obs(), e.fv.bool("force"), e.fv.str("frame"))
			},
		},
		{
			name: "type", args: "<css|ref=@<id>> <text>", desc: "Append text to an element",
			details: `type requires a visible target page and appends text; it does not clear existing content.
label= is not accepted: a name match may hit a non-input element. Use map and ref= or CSS instead.`,
			minArgs: 2, maxArgs: 2, scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) { return cmdType(e.s, e.args[0], e.args[1]) },
		},
		{
			name: "upload", args: "<css|ref=@<id>> <file>...", desc: "Set files on input[type=file]",
			flags: []flagDef{{"frame", flagString, "", "Find a file input by CSS inside a same-origin iframe"}},
			examples: []string{
				"tinybk upload '#resume' ./cv.pdf",
				"tinybk upload ref=@2f ./a.png ./b.png",
				"tinybk upload --frame <short> 'input[type=file]' ./a.png",
			},
			details: `upload sets input[type=file] through DOM.setFileInputFiles, bypassing the native picker.
Targets may be CSS or ref=@<id>; label= is not supported. Hidden inputs can be located by CSS.
File paths are resolved from the current working directory; input.files is read back and verified.
--frame only finds file inputs by CSS inside a same-origin iframe.`,
			minArgs: 2, maxArgs: -1, scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) {
				return cmdUpload(e.s, e.args[0], e.args[1:], e.fv.str("frame"))
			},
		},
		{
			name: "drag", args: "<css|ref=@<id>> <css|ref=@<id>>", desc: "Drag from a source element to a target element",
			flags: []flagDef{{"steps", flagInt, 16, "Mouse move steps"}},
			examples: []string{
				"tinybk drag '#item-1' '#item-3'",
				"tinybk drag --steps 20 ref=@2f ref=@3a",
			},
			details: `drag presses at the source center, moves in steps, and releases at the target center.
Only CSS/ref=@<id> is supported; label= is not supported. Source and target must both be in the viewport and pass hit checks.
Covers pointer/mouse drag only; HTML5 DataTransfer, file drop, and automatic scrolling are not supported.`,
			minArgs: 2, maxArgs: 2, scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) {
				return cmdDrag(e.s, e.args[0], e.args[1], e.fv.int("steps"), e.g.obs())
			},
		},
		{
			name: "press", args: "<key|mod+key>", desc: "Send a key or key chord",
			flags: []flagDef{{"times", flagInt, 1, "Repeat count"}},
			examples: []string{
				"tinybk press Enter",
				"tinybk press Control+A",
				"tinybk press Shift+Tab",
				"tinybk press Control+Shift+A",
				"tinybk press F2",
				"tinybk press F12",
				"tinybk press --times 5 ArrowDown",
			},
			details: `press requires a visible target page. If the key opens a tab, new_tab is returned.
Modifiers: Control/Ctrl, Shift, Alt/Option, Meta/Cmd/Command.
Named keys: Enter Tab Escape Backspace Delete Insert Space ArrowUp ArrowDown
ArrowLeft ArrowRight Home End PageUp PageDown F1..F12.
Join chords with +; one base key is sent per command. Held-key state is not kept across commands.
Browser-level shortcuts (Ctrl+L/T/W, F12, etc.) may be intercepted by Chrome and are not guaranteed to reach the page.`,
			minArgs: 1, maxArgs: 1, scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) {
				times := e.fv.int("times")
				if times <= 0 {
					return nil, usagef("--times must be a positive integer")
				}
				return cmdPress(e.s, e.args[0], times)
			},
		},
		{
			name: "scroll", args: "<top|bottom|±Npx>", desc: "Scroll the page",
			minArgs: 1, maxArgs: 1, scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) { return cmdScroll(e.s, e.args[0]) },
		},
		{
			name: "shot", args: "[dir]", desc: "Capture the viewport as a PNG",
			examples: []string{
				"tinybk shot",
				"tinybk shot ./artifacts/shots",
				"tinybk --shot-dir ./artifacts/shots shot",
			},
			details: `Writes to .tinybk/shots/ in the current working directory by default; [dir] is a directory, not a filename.
The global --shot-dir DIR also selects the directory. Returns an absolute path and byte count.
--shot can still attach screenshots to see/goto/click/press observations.`,
			minArgs: 0, maxArgs: 1, scope: scopePage, lock: lockSpec{page: lockShared},
			run: func(e *env) (any, error) {
				dir := e.arg(0)
				if dir == "" {
					dir = e.g.shotDir
				}
				return cmdShot(e.s, dir)
			},
		},
		{
			name: "see", desc: "Observe the page (URL, title, text, optional screenshot)",
			scope: scopePage, lock: lockSpec{page: lockShared},
			run: func(e *env) (any, error) { return cmdSee(e.s, e.g.obs()) },
		},
		{
			name: "map", desc: "Find interactive elements and refs",
			flags: []flagDef{
				{"frame", flagString, "", "Search inside a same-origin iframe; get frame from tinybk frames"},
				{"within", flagString, "", "Only enumerate nodes inside CSS matches"},
				{"role", flagString, "", "Filter by role; comma-separated; choice covers common option roles"},
				{"name", flagString, "", "Filter by name substring, case-insensitive"},
				{"limit", flagInt, 50, "Maximum items to return; 0 means no limit"},
				{"offset", flagInt, 0, "Skip the first N matches"},
			},
			examples: []string{
				"tinybk map",
				"tinybk map --frame <short> --role radio",
				"tinybk map --within '#q1' --role radio",
				"tinybk map --role checkbox",
				"tinybk map --role button --name Close",
				"tinybk map --role radio --limit 1 --offset 1",
			},
			details: `role:
  button, link, checkbox, radio, option, tab, menuitem, menuitemcheckbox,
  menuitemradio, switch, treeitem, combobox, listbox, slider, spinbutton,
  textbox, searchbox
  choice = radio + checkbox + option + menuitemcheckbox + menuitemradio + switch

--name matches aria-label, innerText, value, placeholder, and title by
case-insensitive substring. It only filters map output and does not make click label= fuzzy.

Output:
  frame         full frame id when --frame is used
  items         returned elements with ref/tag/role/text/id
  matched_total full match count after filters
  returned      number of items returned
  truncated     whether more items remain
  next_offset   next offset when truncated=true`,
			minArgs: 0, maxArgs: 0, scope: scopePage, lock: lockSpec{page: lockShared},
			run: func(e *env) (any, error) {
				return cmdMap(e.s, mapQuery{
					Frame:  e.fv.str("frame"),
					Within: e.fv.str("within"),
					Role:   e.fv.str("role"),
					Name:   e.fv.str("name"),
					Offset: e.fv.int("offset"),
					Limit:  e.fv.int("limit"),
				})
			},
		},
		{
			name: "frames", desc: "List the frame tree (iframes)",
			examples: []string{
				"tinybk frames",
				"tinybk map --frame <short> --role radio",
				"tinybk click --frame <short> 'label=Confirm'",
			},
			details: `frames lists the top frame and child iframes; frame/short values can be passed to
map/click --frame. This MVP supports same-origin iframes only; cross-origin frames are not supported yet.
After a page reload or frame navigation, old frame/ref values may be invalid; re-run frames/map.

Diagnostics:
  same_origin  whether the frame matches the top page origin
  oopif        whether a separate iframe target was detected (best effort)
  accessible   whether this MVP can enumerate/click the frame`,
			scope: scopePage, lock: lockSpec{page: lockShared},
			run: func(e *env) (any, error) { return cmdFrames(e.s) },
		},
		{
			name: "tab", desc: "List tabs",
			usageText: "Usage: tinybk tab | tinybk tab new [url] | tinybk tab activate <id|prefix> | tinybk tab close <id|prefix>",
			minArgs:   0, maxArgs: 0, scope: scopeNone,
			run: func(e *env) (any, error) { return cmdTabList(e.g) },
		},
		{
			name: "tab new", args: "[url]", desc: "Create a background tab",
			details: `tab new creates a background tab by default. Run tinybk tab activate <id|short> before sending real input.`,
			minArgs: 0, maxArgs: 1, scope: scopeAnyPage, lock: lockSpec{browser: true},
			run: func(e *env) (any, error) {
				url := ""
				if len(e.args) > 0 {
					url = e.args[0]
				}
				return cmdTabNew(e.s, e.g.cdpURL, url)
			},
		},
		{
			name: "tab close", args: "<id|prefix>", desc: "Close a tab",
			minArgs: 1, maxArgs: 1, scope: scopeAnyPage,
			lock: lockSpec{lockTarget: true, targetArg: 0, browser: true},
			run:  func(e *env) (any, error) { return cmdTabClose(e.s, e.g, e.page, e.args[0]) },
		},
		{
			name: "tab activate", args: "<id|prefix>", desc: "Activate a tab (changes the foreground)",
			minArgs: 1, maxArgs: 1, scope: scopeAnyPage,
			lock: lockSpec{lockTarget: true, targetArg: 0},
			run:  func(e *env) (any, error) { return cmdTabActivate(e.s, e.g, e.page, e.args[0]) },
		},
		{
			name: "wait", args: "<css>", desc: "Wait for a condition",
			usageText: "Usage: tinybk wait [--timeout ms] <css> | --url <substring> | --contains <substring> | --gone <css> | --stable <ms>",
			flags: []flagDef{
				{"timeout", flagInt64, int64(10000), "Timeout in milliseconds"},
				{"url", flagString, "", "Wait until the URL contains this substring"},
				{"contains", flagString, "", "Wait until body text contains this substring"},
				{"gone", flagString, "", "Wait until the CSS element disappears"},
				{"stable", flagInt64, int64(0), "Wait until the DOM is unchanged for N ms"},
			},
			details: `wait does not hold the page lock; another agent may change the page while it runs.
--url waits for a URL substring, --contains for body text, --gone for a CSS element to disappear, and --stable for DOM stability.
For SPAs, prefer wait --contains / wait <css> / wait --gone over sleep followed by see.`,
			minArgs: 0, maxArgs: 1, scope: scopePage, lock: lockSpec{page: lockNone},
			run: func(e *env) (any, error) {
				timeout := time.Duration(e.fv.int64("timeout")) * time.Millisecond
				if timeout <= 0 {
					return nil, usagef("--timeout must be a positive number of milliseconds")
				}
				conds := []waitCond{
					{mode: waitVisible, arg: e.arg(0)},
					{mode: waitURL, arg: e.fv.str("url")},
					{mode: waitText, arg: e.fv.str("contains")},
					{mode: waitGone, arg: e.fv.str("gone")},
					{mode: waitStable, quiet: time.Duration(e.fv.int64("stable")) * time.Millisecond},
				}
				var c waitCond
				set := 0
				for _, cand := range conds {
					if cand.arg != "" || cand.quiet > 0 {
						c = cand
						set++
					}
				}
				if set != 1 {
					return nil, usagef("%s", e.spec.usage())
				}
				return cmdWait(e.s, c, timeout)
			},
		},
		{
			name: "eval", args: "<js>", desc: "Evaluate JavaScript in the page",
			flags:   []flagDef{{"no-wait", flagBool, false, "Do not wait for the Promise to settle"}},
			details: `eval waits for Promises to settle by default, up to 10s. Use --no-wait to return immediately.`,
			minArgs: 1, maxArgs: -1, scope: scopePage, lock: lockSpec{page: lockExclusive},
			run: func(e *env) (any, error) {
				return cmdEval(e.s, strings.Join(e.args, " "), !e.fv.bool("no-wait"))
			},
		},
		{
			name: "doctor", desc: "Check CDP connectivity and list tabs",
			scope: scopeNone,
			run:   func(e *env) (any, error) { return cmdDoctor(e.g.cdpURL) },
		},
		{
			name: "help", args: "[command]", desc: "Show help",
			minArgs: 0, maxArgs: -1, scope: scopeNone,
			run: func(e *env) (any, error) {
				cmdHelp(e.args)
				return nil, errHelp
			},
		},
	}
	specIndex = make(map[string]*spec, len(specs))
	for _, sp := range specs {
		specIndex[sp.name] = sp
	}
}

// lookup tries two-word command names (tab new) first, then one word.
func lookup(argv []string) (*spec, []string) {
	for n := 2; n >= 1; n-- {
		if len(argv) < n {
			continue
		}
		if sp := specIndex[strings.Join(argv[:n], " ")]; sp != nil {
			return sp, argv[n:]
		}
	}
	return nil, argv
}

// ---- execution environment: connections and locks are created here ----

type env struct {
	g     globals
	spec  *spec
	s     *Session
	page  *pageEntry // target resolved for scopeAnyPage with lockTarget
	args  []string
	fv    *flagValues
	locks []*fileLock
}

func (e *env) arg(i int) string {
	if i < len(e.args) {
		return e.args[i]
	}
	return ""
}

func (e *env) prepare() (err error) {
	defer func() {
		if err != nil {
			e.close()
		}
	}()
	wait := e.g.lockWait()
	switch e.spec.scope {
	case scopeNone:
		return nil
	case scopePage:
		s, err := Connect(e.g.cdpURL, e.g.target, e.g.tab, e.g.tabSet)
		if err != nil {
			return err
		}
		e.s = s
		l, err := acquireTargetLock(string(s.tabID), e.spec.lock.page, wait)
		if err != nil {
			return err
		}
		e.locks = append(e.locks, l)
	case scopeAnyPage:
		pages, err := listPages(e.g.cdpURL)
		if err != nil {
			return fmt.Errorf("connect %s: %w", e.g.cdpURL, err)
		}
		if len(pages) == 0 {
			return fmt.Errorf("browser has no page tabs (start it with at least one about:blank)")
		}
		// Attach before resolving the target to preserve old behavior and shorten the window where a target can be closed after lookup.
		s, err := Connect(e.g.cdpURL, pages[0].ID, 0, false)
		if err != nil {
			return err
		}
		e.s = s
		if e.spec.lock.lockTarget {
			p, err := resolveTarget(pages, e.args[e.spec.lock.targetArg])
			if err != nil {
				return err
			}
			e.page = &p
		}
		if e.page != nil {
			l, err := acquireTargetLock(e.page.ID, lockExclusive, wait)
			if err != nil {
				return err
			}
			e.locks = append(e.locks, l)
		}
		if e.spec.lock.browser {
			l, err := acquireBrowserLock(e.g.cdpURL, wait)
			if err != nil {
				return err
			}
			e.locks = append(e.locks, l)
		}
	}
	return nil
}

func (e *env) close() {
	for i := len(e.locks) - 1; i >= 0; i-- {
		e.locks[i].Release()
	}
	if e.s != nil {
		e.s.Close()
	}
}

// ---- command flags ----

// flagValues reads values by name from the parsed FlagSet; defaults come from spec.flags.
type flagValues struct{ fs *flag.FlagSet }

func (v *flagValues) str(name string) string {
	if v.fs == nil {
		return ""
	}
	f := v.fs.Lookup(name)
	if f == nil {
		return ""
	}
	return f.Value.String()
}

func (v *flagValues) bool(name string) bool {
	b, _ := strconv.ParseBool(v.str(name))
	return b
}

func (v *flagValues) int(name string) int {
	n, _ := strconv.Atoi(v.str(name))
	return n
}

func (v *flagValues) int64(name string) int64 {
	n, _ := strconv.ParseInt(v.str(name), 10, 64)
	return n
}

func (sp *spec) parseFlags(args []string) (*flagValues, []string, error) {
	if len(sp.flags) == 0 {
		return &flagValues{}, args, nil
	}
	fs := flag.NewFlagSet(sp.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Build errors ourselves; do not leak FlagSet output to stderr.
	for _, d := range sp.flags {
		switch d.kind {
		case flagBool:
			fs.Bool(d.name, d.def.(bool), d.help)
		case flagInt:
			fs.Int(d.name, d.def.(int), d.help)
		case flagInt64:
			fs.Int64(d.name, d.def.(int64), d.help)
		case flagString:
			fs.String(d.name, d.def.(string), d.help)
		}
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, usagef("%v (run tinybk %s -h for usage)", err, sp.name)
	}
	return &flagValues{fs: fs}, fs.Args(), nil
}

// ---- output ----

func emitJSON(v any) error {
	return json.NewEncoder(os.Stdout).Encode(v)
}

func emitError(action string, ce *cmdError) {
	out := map[string]any{"error": ce.msg, "code": ce.code}
	if action != "" {
		out["action"] = action
	}
	for k, v := range ce.kv {
		if _, reserved := out[k]; !reserved {
			out[k] = v
		}
	}
	if err := emitJSON(out); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write stdout: %v\n", err)
	}
}
