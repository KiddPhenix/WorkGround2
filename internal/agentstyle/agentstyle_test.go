package agentstyle

import (
	"strings"
	"testing"
)

func TestCatalogIsDeterministicAndComplete(t *testing.T) {
	got := Catalog()
	if len(got) != 10 {
		t.Fatalf("catalog length = %d, want 10", len(got))
	}
	wantIDs := []string{
		"paranoid", "schizoid", "schizotypal", "antisocial", "borderline",
		"histrionic", "narcissistic", "avoidant", "dependent", "obsessive_compulsive",
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Fatalf("catalog[%d].ID = %q, want %q", i, got[i].ID, id)
		}
		if got[i].Disorder == "" || got[i].StyleName == "" || got[i].Capability == "" {
			t.Fatalf("catalog[%d] has an empty label field: %+v", i, got[i])
		}
		if strings.Contains(got[i].Capability, "；") {
			t.Fatalf("catalog[%d] capability still contains a fallback clause: %q", i, got[i].Capability)
		}
	}
	// Immutable copy: mutating the returned slice must not touch the source.
	got[0].ID = "corrupted"
	if Catalog()[0].ID != "paranoid" {
		t.Fatal("Catalog() exposed mutable internal state")
	}
}

func TestCanonicalizeDedupesAndOrdersByCatalog(t *testing.T) {
	got, err := Canonicalize([]string{"borderline", "paranoid", "Borderline", "  paranoid  "})
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if want := []string{"paranoid", "borderline"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Canonicalize = %v, want %v", got, want)
	}
}

func TestCanonicalizeSurfacesUnknownIDs(t *testing.T) {
	if _, err := Canonicalize([]string{"paranoid", "not-a-style"}); err == nil {
		t.Fatal("expected error for unknown ID")
	} else if !strings.Contains(err.Error(), "not-a-style") {
		t.Fatalf("error %q does not name the unknown ID", err)
	}
}

func TestResolveIDsKeepsValidSubsetAndDedupesUnknown(t *testing.T) {
	known, unknown := ResolveIDs([]string{"borderline", "bogus", "paranoid", "BOGUS"})
	if want := []string{"paranoid", "borderline"}; strings.Join(known, ",") != strings.Join(want, ",") {
		t.Fatalf("known = %v, want %v", known, want)
	}
	if want := []string{"bogus"}; strings.Join(unknown, ",") != strings.Join(want, ",") {
		t.Fatalf("unknown = %v, want %v", unknown, want)
	}
}

func TestCanonicalizeEmpty(t *testing.T) {
	got, err := Canonicalize([]string{"", "   "})
	if err != nil {
		t.Fatalf("Canonicalize(empty): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Canonicalize(empty) = %v, want empty", got)
	}
}

func TestCompileIsDeterministicAndExact(t *testing.T) {
	ids := []string{"paranoid", "obsessive_compulsive"}
	a, err := Compile(ids)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	b, err := Compile([]string{"obsessive_compulsive", "paranoid", "paranoid"})
	if err != nil {
		t.Fatalf("Compile reordered: %v", err)
	}
	if a != b {
		t.Fatalf("Compile is not deterministic:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
	catalog := Catalog()
	want := "风格: " + catalog[0].Capability + "\n" + catalog[9].Capability
	if a != want {
		t.Fatalf("Compile = %q, want %q", a, want)
	}
	for _, hidden := range []string{"Agent 风格", "偏执型", "风险审查者", "强迫型人格", "严谨执行者", "#"} {
		if strings.Contains(a, hidden) {
			t.Fatalf("compiled block leaked Settings-only label %q:\n%s", hidden, a)
		}
	}
	for _, removed := range []string{"所有怀疑必须给出证据", "仍需考虑人的感受", "设置完成标准和时间上限"} {
		if strings.Contains(a, removed) {
			t.Fatalf("compiled block retained fallback clause %q:\n%s", removed, a)
		}
	}
}

func TestCompileEmpty(t *testing.T) {
	got, err := Compile(nil)
	if err != nil {
		t.Fatalf("Compile(nil): %v", err)
	}
	if got != "" {
		t.Fatalf("Compile(nil) = %q, want empty", got)
	}
}
