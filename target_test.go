package main

// target_test.go - unit tests for target id prefix resolution.
// Prefixes are a convenience for humans and agents; correctness requires a unique match, never guessing.

import (
	"errors"
	"strings"
	"testing"
)

// wantCode asserts that an error is a cmdError with the given code.
func wantCode(t *testing.T, err error, want string) *cmdError {
	t.Helper()
	var ce *cmdError
	if !errors.As(err, &ce) {
		t.Fatalf("应为 cmdError，实际 %v", err)
	}
	if ce.code != want {
		t.Fatalf("code = %q，期望 %q（msg=%s）", ce.code, want, ce.msg)
	}
	return ce
}

func TestResolveTargetPrefix(t *testing.T) {
	idA1 := "AAAAA" + strings.Repeat("1", 27)
	idA2 := "AAAAA" + strings.Repeat("2", 27)
	idB := "BBBBB" + strings.Repeat("3", 27)
	pages := []pageEntry{
		{ID: idA1, Type: "page", URL: "https://a1", Title: "A1"},
		{ID: idA2, Type: "page", URL: "https://a2", Title: "A2"},
		{ID: idB, Type: "page", URL: "https://b", Title: "B"},
	}

	// unique 5-char prefix
	p, err := resolveTarget(pages, "BBBBB")
	if err != nil || p.ID != idB {
		t.Fatalf("BBBBB 应唯一命中 %s，实际 %+v err=%v", idB, p, err)
	}

	// case-insensitive
	p, err = resolveTarget(pages, "bbbbb")
	if err != nil || p.ID != idB {
		t.Fatalf("bbbbb 应大小写不敏感命中 %s，实际 %+v err=%v", idB, p, err)
	}

	// longer prefix disambiguates
	p, err = resolveTarget(pages, "AAAAA1")
	if err != nil || p.ID != idA1 {
		t.Fatalf("AAAAA1 应命中 %s，实际 %+v err=%v", idA1, p, err)
	}

	// full id
	p, err = resolveTarget(pages, idA1)
	if err != nil || p.ID != idA1 {
		t.Fatalf("完整 id 应精确命中，实际 %+v err=%v", p, err)
	}

	// 5-char prefix matches multiple: ambiguous error with candidates, no execution
	_, err = resolveTarget(pages, "AAAAA")
	amb := wantCode(t, err, codeTargetAmbiguous)
	cands, _ := amb.kv["tabs"].([]tabItem)
	if len(cands) != 2 {
		t.Fatalf("歧义应带 2 个候选，实际 %+v", amb.kv["tabs"])
	}

	// shorter than minimum: reject
	_, err = resolveTarget(pages, "AAAA")
	short := wantCode(t, err, codeTargetTooShort)
	if short.kv["min_len"] != targetPrefixMin {
		t.Fatalf("target_too_short 应带 min_len=%d，实际 %v", targetPrefixMin, short.kv)
	}

	// no match
	_, err = resolveTarget(pages, "CCCCC")
	wantCode(t, err, codeTargetNotFound)

	// short is the first 5 chars of the id and parses directly as a prefix (using the unique B page here)
	it := tabItemFromPage(pages[2], 2)
	if it.Short != "BBBBB" || !strings.HasPrefix(it.ID, it.Short) {
		t.Fatalf("short 应是 id 前 %d 位: %+v", targetPrefixMin, it)
	}
	if p, err := resolveTarget(pages, it.Short); err != nil {
		t.Fatalf("short 应能解析: %v", err)
	} else if p.ID != it.ID {
		t.Fatalf("short 应命中自身，实际 %s", p.ID)
	}
}
