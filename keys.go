package main

// Keyboard parsing: single characters, named keys, and one base key with
// Control/Ctrl, Shift, Alt/Option, or Meta/Cmd modifiers, e.g. Ctrl+Shift+Enter.
// press sends one chord per command and keeps no held-key state across commands.

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// keyChord is the structured target of one press command.
type keyChord struct {
	Modifiers []chromedp.Modifier
	Seq       string
}

// namedKeys lists keys recognized by press; order is the help text order.
var namedKeys = []struct{ name, seq string }{
	{"Enter", kb.Enter},
	{"Tab", kb.Tab},
	{"Escape", kb.Escape},
	{"Backspace", kb.Backspace},
	{"Delete", kb.Delete},
	{"Insert", kb.Insert},
	{"Space", " "},
	{"ArrowUp", kb.ArrowUp},
	{"ArrowDown", kb.ArrowDown},
	{"ArrowLeft", kb.ArrowLeft},
	{"ArrowRight", kb.ArrowRight},
	{"Home", kb.Home},
	{"End", kb.End},
	{"PageUp", kb.PageUp},
	{"PageDown", kb.PageDown},
	{"F1", kb.F1},
	{"F2", kb.F2},
	{"F3", kb.F3},
	{"F4", kb.F4},
	{"F5", kb.F5},
	{"F6", kb.F6},
	{"F7", kb.F7},
	{"F8", kb.F8},
	{"F9", kb.F9},
	{"F10", kb.F10},
	{"F11", kb.F11},
	{"F12", kb.F12},
}

var keyNames = func() string {
	names := make([]string, len(namedKeys))
	for i, k := range namedKeys {
		names[i] = k.name
	}
	return strings.Join(names, " ")
}()

var namedKeySeqs = func() map[string]string {
	m := make(map[string]string, len(namedKeys)+8)
	for _, k := range namedKeys {
		m[strings.ToLower(k.name)] = k.seq
	}
	// Common aliases are input conveniences; help still shows canonical names.
	m["return"] = m["enter"]
	m["esc"] = m["escape"]
	m["del"] = m["delete"]
	m["ins"] = m["insert"]
	m["pgup"] = m["pageup"]
	m["pgdn"] = m["pagedown"]
	m["up"] = m["arrowup"]
	m["down"] = m["arrowdown"]
	m["left"] = m["arrowleft"]
	m["right"] = m["arrowright"]
	return m
}()

var modifierAliases = map[string]chromedp.Modifier{
	"control": chromedp.ModifierCtrl,
	"ctrl":    chromedp.ModifierCtrl,
	"shift":   chromedp.ModifierShift,
	"alt":     chromedp.ModifierAlt,
	"option":  chromedp.ModifierAlt,
	"meta":    chromedp.ModifierMeta,
	"cmd":     chromedp.ModifierMeta,
	"command": chromedp.ModifierMeta,
}

func parseKeyChord(raw string) (keyChord, error) {
	if raw == "" {
		return keyChord{}, usagef("key must not be empty. %s", keyChordUsage())
	}

	parts := strings.Split(raw, "+")
	var mods []chromedp.Modifier
	seen := make(map[chromedp.Modifier]bool, 4)
	i := 0
	for i < len(parts)-1 {
		name := strings.ToLower(strings.TrimSpace(parts[i]))
		m, ok := modifierAliases[name]
		if !ok {
			break
		}
		if !seen[m] {
			seen[m] = true
			mods = append(mods, m)
		}
		i++
	}
	base := strings.Join(parts[i:], "+")
	if base == "" {
		return keyChord{}, usagef("chord is missing a base key: %q. %s", raw, keyChordUsage())
	}
	if _, isModifier := modifierAliases[strings.ToLower(strings.TrimSpace(base))]; isModifier {
		return keyChord{}, usagef("chord is missing a base key: %q. %s", raw, keyChordUsage())
	}
	if seq, ok := namedKeySeqs[strings.ToLower(base)]; ok {
		return keyChord{Modifiers: mods, Seq: seq}, nil
	}

	r := []rune(base)
	if len(r) != 1 {
		return keyChord{}, usagef("unknown key %q. %s", raw, keyChordUsage())
	}
	seq := base
	if len(mods) > 0 && unicode.IsLetter(r[0]) {
		// Letter shortcuts ignore input case: Shift decides uppercase; other modifiers
		// use the lowercase physical key, so Ctrl+A does not become Ctrl+Shift+A.
		if seen[chromedp.ModifierShift] {
			seq = string(unicode.ToUpper(r[0]))
		} else {
			seq = string(unicode.ToLower(r[0]))
		}
	}
	return keyChord{Modifiers: mods, Seq: seq}, nil
}

func keyChordUsage() string {
	return fmt.Sprintf("Modifiers: Control/Ctrl Shift Alt/Option Meta/Cmd/Command; named keys: %s; single chars and chords such as Control+A are also accepted", keyNames)
}
