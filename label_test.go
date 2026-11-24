package main

// label_test.go - unit tests for label= matching. Real browser behavior lives in e2e_test.go;
// these tests lock the decision rules (tier, ambiguity, normalization).

import "testing"

func lcand(ref string, names ...labelNamed) nodeHandle {
	return nodeHandle{Ref: ref, Tag: "a", Names: names}
}

func ln(kind, v string) labelNamed { return labelNamed{Kind: kind, Val: v} }

// Exact wins even when substrings match more elements.
func TestMatchExactBeatsPrefixAndSubstring(t *testing.T) {
	items := []nodeHandle{
		lcand("e0", ln("text", "Next steps")),      // prefix
		lcand("e1", ln("text", "Go to next page")), // substring
		lcand("e2", ln("text", "Next")),            // exact
	}
	m, err := matchLabel(items, "next")
	if err != nil {
		t.Fatal(err)
	}
	if m.Tier != labelTierExact {
		t.Fatalf("tier = %s, 期望 %s", m.Tier, labelTierExact)
	}
	if m.Candidate == nil || m.Candidate.Ref != "e2" {
		t.Fatalf("命中的不是整名元素: %+v", m.Candidate)
	}
	if m.Name != "Next" {
		t.Fatalf("证据名应为原样的 Next，实际 %q", m.Name)
	}
}

// Prefix wins over substring.
func TestMatchPrefixBeatsSubstring(t *testing.T) {
	items := []nodeHandle{
		lcand("e0", ln("text", "More stories")), // substring
		lcand("e1", ln("text", "More")),         // exact
		lcand("e2", ln("text", "More →")),       // prefix
	}
	m, _ := matchLabel(items, "more")
	if m.Tier != labelTierExact || m.Candidate == nil || m.Candidate.Ref != "e1" {
		t.Fatalf("整名 e1 应唯一命中: %+v", m)
	}
}

// A unique prefix tier must match without requiring an exact name.
func TestMatchPrefixUnique(t *testing.T) {
	items := []nodeHandle{
		lcand("e0", ln("text", "More →")),
		lcand("e1", ln("text", "Next →")),
	}
	m, _ := matchLabel(items, "Next")
	if m.Tier != labelTierPrefix || m.Candidate == nil || m.Candidate.Ref != "e1" {
		t.Fatalf("前缀 e1 应唯一命中: %+v", m)
	}
	if m.Name != "Next →" {
		t.Fatalf("证据名应为完整名字，实际 %q", m.Name)
	}
}

// Multiple same-tier elements: do not guess; return the whole tier as candidates.
func TestMatchAmbiguousReturnsAll(t *testing.T) {
	items := []nodeHandle{
		lcand("e0", ln("aria", "Search")),
		lcand("e1", ln("text", "Search")),
		lcand("e2", ln("text", "Search help")),
	}
	m, _ := matchLabel(items, "search")
	if m.Candidate != nil {
		t.Fatalf("歧义时不应给出 Candidate: %+v", m.Candidate)
	}
	if m.Tier != labelTierExact || m.AmbiguousTotal != 2 || len(m.Ambiguous) != 2 {
		t.Fatalf("应返回两个整名候选: %+v", m)
	}
}

// A match on any name is enough; primaryName is for display.
func TestMatchAnyNameSource(t *testing.T) {
	items := []nodeHandle{
		lcand("e0", ln("aria", "打开搜索"), ln("text", "")),
		lcand("e1", ln("text", "Search"), ln("placeholder", "搜索商品")),
	}
	m, _ := matchLabel(items, "搜索商品")
	if m.Candidate == nil || m.Candidate.Ref != "e1" || m.Tier != labelTierExact {
		t.Fatalf("placeholder 应可命中: %+v", m)
	}
	if name, kind := m.Candidate.primaryName(); name != "Search" || kind != "text" {
		t.Fatalf("主名优先级有误: %s/%s", name, kind)
	}
}

// Normalization: NBSP, repeated spaces, and case must not affect exact matching.
func TestMatchNormalization(t *testing.T) {
	items := []nodeHandle{lcand("e0", ln("text", "Sign\u00a0 In"))}
	m, _ := matchLabel(items, "  sign in ")
	if m.Tier != labelTierExact || m.Candidate == nil {
		t.Fatalf("归一化后应整名命中: %+v", m)
	}
}

// Substring matches never decide; they are suggestions only. This is a real-world lesson:
// otherwise go.dev's "search" silently hits "elastic/go-elasticsearch".
func TestMatchSubstringIsOnlySuggestion(t *testing.T) {
	items := []nodeHandle{
		lcand("e0", ln("text", "Go to next page")),
		lcand("e1", ln("text", "elastic/go-elasticsearch")),
	}
	m, _ := matchLabel(items, "next")
	if m.Tier != labelTierNone || m.Candidate != nil || len(m.Ambiguous) != 0 {
		t.Fatalf("子串不应产生命中: %+v", m)
	}
	if len(m.Suggestions) != 1 || m.Suggestions[0].Ref != "e0" {
		t.Fatalf("子串应作为建议返回: %+v", m.Suggestions)
	}
}

func TestMatchNone(t *testing.T) {
	items := []nodeHandle{lcand("e0", ln("text", "登录"))}
	m, err := matchLabel(items, "注册")
	if err != nil {
		t.Fatal(err)
	}
	if m.Tier != labelTierNone || m.Candidate != nil || len(m.Ambiguous) != 0 {
		t.Fatalf("不应命中: %+v", m)
	}
	if m.Total != 1 {
		t.Fatalf("total 应为 1: %d", m.Total)
	}
}

func TestMatchEmptyQuery(t *testing.T) {
	if _, err := matchLabel(nil, "   "); err == nil {
		t.Fatal("空查询应报错")
	}
}
