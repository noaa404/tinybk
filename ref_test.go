package main

// ref_test.go - unit tests for ref syntax: only @<base36 node identity> is accepted;
// positional forms are usage errors and must never degrade into positional clicks.

import (
	"errors"
	"testing"

	"github.com/chromedp/cdproto/cdp"
)

func TestRefSyntax(t *testing.T) {
	bid := cdp.BackendNodeID(42)
	raw := "ref=" + refString(bid)
	if raw != "ref=@16" {
		t.Fatalf("42 的 ref 应为 ref=@16，实际 %q", raw)
	}

	target, err := parseTarget(raw)
	if err != nil {
		t.Fatalf("合法 ref 应解析: %v", err)
	}
	if target.bid != bid || target.css != "" || target.label != "" {
		t.Fatalf("ref 目标应只有身份: %+v", target)
	}
	if target.String() != raw {
		t.Fatalf("String() 应回显原 ref，实际 %q", target.String())
	}

	for _, bad := range []string{"ref=e5", "ref=5", "ref=@", "ref=@0", "ref=@-1", "ref=@!"} {
		_, err := parseTarget(bad)
		var ce *cmdError
		if !errors.As(err, &ce) || ce.code != codeUsage {
			t.Fatalf("%q 应为 usage 错误，实际 %v", bad, err)
		}
	}
}
