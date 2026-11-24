package main

// Help text: the command index, usage, and option defaults are generated from the command table.
// Only cross-command semantics remain here; the command list and defaults are not duplicated.

import (
	"fmt"
	"strings"
)

func printHelp() {
	fmt.Print(helpText())
}

func printCmdHelp(sp *spec) {
	fmt.Print(cmdHelpText(sp))
}

func helpText() string {
	var b strings.Builder
	b.WriteString("tinybk - one-shot browser automation for AI agents. One call, one action, JSON on stdout.\n\n")
	b.WriteString("Usage: tinybk [global options] <command> [args]\n")
	b.WriteString("       Global options may appear anywhere; command options go before positional args.\n")
	b.WriteString("       After --, nothing is parsed, so text starting with - can be passed through\n")
	b.WriteString("       (e.g. tinybk type -- \"#box\" \"--text\").\n\n")
	b.WriteString("Command details (semantics, examples, output fields):\n")
	b.WriteString("  tinybk help frames     iframe tree and --frame usage\n")
	b.WriteString("  tinybk help map        map search, filters, pagination, and output fields\n")
	b.WriteString("  tinybk help click      click CSS/ref/label semantics and output\n")
	b.WriteString("  tinybk help upload     file inputs, hidden inputs, and iframe uploads\n")
	b.WriteString("  tinybk help drag       drag source/target and limitations\n")
	b.WriteString("  tinybk help press      modifiers, named keys, and chords\n")
	b.WriteString("  tinybk help shot       screenshot directory, --shot, and --shot-dir\n")
	b.WriteString("  tinybk help wait       URL/text/element/stability waits for SPAs\n")
	b.WriteString("  Other commands: tinybk help <command>\n\n")
	b.WriteString("Global options:\n")
	fmt.Fprintf(&b, "  --cdp URL             CDP endpoint (default %s)\n", defaultCDP)
	b.WriteString("  --target ID           Stable tab id or a prefix of at least 5 chars\n")
	b.WriteString("  --tab N               Current /json/list index (not stable; use --target for multiple agents)\n")
	fmt.Fprintf(&b, "  --text N              Max observed text length (default %d; 0 disables text)\n", defaultTextLimit)
	fmt.Fprintf(&b, "  --lock-timeout ms     Per-tab lock timeout (default %d; 0 means no wait)\n", defaultLockTimeout)
	b.WriteString("  --shot                Attach a screenshot to observations; directory from --shot-dir\n")
	b.WriteString("  --shot-dir DIR        Screenshot directory (default $PWD/.tinybk/shots)\n")
	b.WriteString("  --version             Show version and exit\n")
	b.WriteString("  -h, --help            Show help; tinybk <command> -h shows command options\n\n")
	b.WriteString("Commands:\n")
	for _, sp := range specs {
		line := sp.synopsis()
		if textWidth(line) > 52 {
			fmt.Fprintf(&b, "  %s\n      %s\n", line, sp.desc)
		} else {
			fmt.Fprintf(&b, "  %s %s\n", padRight(line, 52), sp.desc)
		}
	}
	if specIndex["map"] != nil {
		b.WriteString("\nmap search examples:\n")
		b.WriteString("  tinybk map --within '#q3' --role radio\n")
		b.WriteString("  tinybk map --role button --name Login\n")
		b.WriteString("  Full details: tinybk help map\n")
	}
	b.WriteString("\n")
	b.WriteString(notesFmt)
	return b.String()
}

func cmdHelpText(sp *spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", sp.usage())
	if sp.desc != "" {
		fmt.Fprintf(&b, "  %s\n", sp.desc)
	}
	if len(sp.flags) > 0 {
		b.WriteString("\nOptions:\n")
		for _, f := range sp.flags {
			fmt.Fprintf(&b, "  --%-14s %s%s\n", f.name, f.help, defaultText(f.def))
		}
	}
	if len(sp.examples) > 0 {
		b.WriteString("\nExamples:\n")
		for _, ex := range sp.examples {
			fmt.Fprintf(&b, "  %s\n", ex)
		}
	}
	if sp.details != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(sp.details, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\nGlobal options and cross-command semantics: tinybk help\n")
	return b.String()
}

// textWidth estimates terminal display width (CJK characters count as 2).
func textWidth(s string) int {
	w := 0
	for _, r := range s {
		w++
		if r >= 0x1100 && (r <= 0x115F || r == 0x2329 || r == 0x232A ||
			(r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) ||
			(r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE30 && r <= 0xFE6F) ||
			(r >= 0xFF00 && r <= 0xFF60) || (r >= 0xFFE0 && r <= 0xFFE6) ||
			(r >= 0x20000 && r <= 0x3FFFD)) {
			w++
		}
	}
	return w
}

// padRight pads by terminal display width to align command lists.
func padRight(s string, width int) string {
	if w := textWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// defaultText annotates only non-zero defaults to avoid noise from false/0/empty.
func defaultText(v any) string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return ""
		}
	case bool:
		if !x {
			return ""
		}
	case int:
		if x == 0 {
			return ""
		}
	case int64:
		if x == 0 {
			return ""
		}
	}
	return fmt.Sprintf(" (default %v)", v)
}

func cmdHelp(args []string) {
	if len(args) == 0 {
		printHelp()
		return
	}
	if sp := specIndex[strings.Join(args, " ")]; sp != nil {
		printCmdHelp(sp)
		return
	}
	if sp, _ := lookup(args); sp != nil {
		printCmdHelp(sp)
		return
	}
	printHelp()
}

const notesFmt = `Key conventions:
  - Target: one page can omit --target; multiple pages require --target/--tab. Never guess on ambiguous matches.
  - Foreground: click/type/press require a visible target; activate background tabs first.
    see/map/eval/goto/back/wait work in background tabs.
  - ref: @... from map is a node identity, not a position; invalid after reload/navigation, so re-run map.
  - Locks: writes to one tab are serialized across processes; see/map share a lock; wait holds none;
    lock timeout returns lock_busy.
  - Output: action is the command name; short is the tab id prefix usable with --target/tab activate/close.
  - Exit codes: 0 success; 1 execution failure; 2 invalid command line; error JSON includes code.
`
