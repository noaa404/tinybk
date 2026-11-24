package main

// Unit tests for key-chord parsing; real browser events are covered by the e2e press tests.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

func hasModifier(mods []chromedp.Modifier, want chromedp.Modifier) bool {
	for _, m := range mods {
		if m == want {
			return true
		}
	}
	return false
}

func TestParseKeyChord(t *testing.T) {
	tests := []struct {
		in      string
		seq     string
		ctrl    bool
		shift   bool
		alt     bool
		meta    bool
		wantErr bool
	}{
		{in: "Enter", seq: kb.Enter},
		{in: "enter", seq: kb.Enter},
		{in: "F12", seq: kb.F12},
		{in: "f2", seq: kb.F2},
		{in: "Control+A", seq: "a", ctrl: true},
		{in: "Ctrl+A", seq: "a", ctrl: true},
		{in: "Ctrl+Shift+Enter", seq: kb.Enter, ctrl: true, shift: true},
		{in: "Shift+Tab", seq: kb.Tab, shift: true},
		{in: "Shift+a", seq: "A", shift: true},
		{in: "Cmd+C", seq: "c", meta: true},
		{in: "Meta+K", seq: "k", meta: true},
		{in: "Option+ArrowLeft", seq: kb.ArrowLeft, alt: true},
		{in: "Control++", seq: "+", ctrl: true},
		{in: "+", seq: "+"},
		{in: "", wantErr: true},
		{in: "Control", wantErr: true},
		{in: "Control+Shift", wantErr: true},
		{in: "Control+A+B", wantErr: true},
		{in: "Control+Foo", wantErr: true},
		{in: "Control+", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseKeyChord(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseKeyChord(%q) 应报错，实际 %+v", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseKeyChord(%q): %v", tt.in, err)
			}
			if got.Seq != tt.seq {
				t.Fatalf("parseKeyChord(%q).Seq = %q，期望 %q", tt.in, got.Seq, tt.seq)
			}
			if hasModifier(got.Modifiers, chromedp.ModifierCtrl) != tt.ctrl ||
				hasModifier(got.Modifiers, chromedp.ModifierShift) != tt.shift ||
				hasModifier(got.Modifiers, chromedp.ModifierAlt) != tt.alt ||
				hasModifier(got.Modifiers, chromedp.ModifierMeta) != tt.meta {
				t.Fatalf("parseKeyChord(%q).Modifiers = %v，期望 ctrl=%v shift=%v alt=%v meta=%v",
					tt.in, got.Modifiers, tt.ctrl, tt.shift, tt.alt, tt.meta)
			}
		})
	}
}

func TestKeyChordUsageListsModifiersAndFunctionKeys(t *testing.T) {
	usage := keyChordUsage()
	for _, want := range []string{"Control/Ctrl", "Shift", "Alt/Option", "Meta/Cmd", "F1", "F12"} {
		if !contains(usage, want) {
			t.Fatalf("按键提示缺少 %q: %s", want, usage)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
