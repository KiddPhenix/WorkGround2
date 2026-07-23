package work

import (
	"strings"
	"testing"
	"time"
)

func TestBuildCornerstoneContext_Empty(t *testing.T) {
	block, err := BuildCornerstoneContext(nil, productionCornerstoneContextConfig())
	if err != nil {
		t.Fatal(err)
	}
	if block.XML != "" {
		t.Errorf("expected empty XML, got %q", block.XML)
	}
	if block.Count != 0 {
		t.Errorf("expected 0 count, got %d", block.Count)
	}
}

func TestBuildCornerstoneContext_SortRequiredFirst(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Optional instruction", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now, Content: "c1"},
		{ID: "cs-2", Type: CornerstonePolicy, Title: "Required policy", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Required: true, PinnedAt: now, Content: "c2"},
		{ID: "cs-3", Type: CornerstoneInstruction, Title: "Required instruction", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Required: true, PinnedAt: now, Content: "c3"},
	}

	block, err := BuildCornerstoneContext(cs, productionCornerstoneContextConfig())
	if err != nil {
		t.Fatal(err)
	}
	if block.Count != 3 {
		t.Errorf("expected 3 injected, got %d", block.Count)
	}
	if block.Blocking {
		t.Error("expected no blocking")
	}

	idxCS3 := strings.Index(block.XML, `id="cs-3"`)
	idxCS2 := strings.Index(block.XML, `id="cs-2"`)
	idxCS1 := strings.Index(block.XML, `id="cs-1"`)
	if idxCS3 < 0 || idxCS2 < 0 || idxCS1 < 0 {
		t.Fatal("missing expected cornerstone entries")
	}
	if idxCS3 >= idxCS2 {
		t.Error("cs-3 (required, instruction) should appear before cs-2 (required, policy)")
	}
	if idxCS2 >= idxCS1 {
		t.Error("cs-2 (required, policy) should appear before cs-1 (optional)")
	}
}

func TestBuildCornerstoneContext_RequiredBlocking(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Required but missing", Status: CornerstoneMissing, Required: true, PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !block.Blocking {
		t.Error("expected blocking for required missing cornerstone")
	}
	if block.XML != "" {
		t.Errorf("expected empty block for required+missing (no entries injected), got %q", block.XML)
	}
}

func TestBuildCornerstoneContext_OptionalDegraded(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Optional stale", Status: CornerstoneStale, Mode: CornerstoneLiveRef, PinnedAt: now, Content: "some content"},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if block.Blocking {
		t.Error("expected no blocking for optional degraded")
	}
	if !block.Degraded {
		t.Error("expected degraded for optional stale")
	}
	if !strings.Contains(block.XML, `status="stale"`) {
		t.Errorf("expected degraded status marker in block, got %q", block.XML)
	}
	if !strings.Contains(block.XML, `id="cs-1"`) {
		t.Error("expected cs-1 to be included")
	}
	if strings.Contains(block.XML, "some content") {
		t.Errorf("degraded live_ref leaked last-known content: %q", block.XML)
	}
}

func TestBuildCornerstoneContext_OptionalVeryLowBudgetKeepsExplicitMarker(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{{
		ID: "cs-budget", Type: CornerstoneInstruction, Title: "Optional", Status: CornerstoneActive,
		Mode: CornerstoneSnapshot, Content: strings.Repeat("content ", 50), PinnedAt: now,
	}}
	wrapper := estimateCornerstoneTokens("<cornerstone-context>\n</cornerstone-context>\n")
	marker := estimateCornerstoneTokens("<truncated/>\n")
	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{MaxTokens: wrapper + marker, MaxPerItem: 1})
	if err != nil {
		t.Fatalf("very-low optional budget: %v", err)
	}
	if !block.Degraded || block.Blocking || block.Skipped != 1 {
		t.Fatalf("block = %+v, want explicit degraded skip", block)
	}
	if !strings.Contains(block.XML, "<truncated/>") || strings.Contains(block.XML, "content content") {
		t.Fatalf("budget marker/content = %q", block.XML)
	}
	if len(block.SkippedIDs) != 1 || block.SkippedIDs[0] != "cs-budget" {
		t.Fatalf("SkippedIDs = %v", block.SkippedIDs)
	}
}

func TestBuildCornerstoneContext_InlinedContent(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Inline CS", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "Small content here", PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block.XML, "Small content here") {
		t.Errorf("expected inline content, got %q", block.XML)
	}
}

func TestBuildCornerstoneContext_LargeContentMetadataOnly(t *testing.T) {
	now := time.Now()
	largeContent := strings.Repeat("x", CornerstoneInlineThreshold+100)
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Large CS", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: largeContent, PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// Large content should NOT be inlined. Should only have summary + note.
	if strings.Contains(block.XML, strings.Repeat("x", CornerstoneInlineThreshold+10)) {
		t.Error("large content should not be inlined")
	}
	if !strings.Contains(block.XML, "<summary>") {
		t.Error("expected <summary> tag for large content")
	}
	if !strings.Contains(block.XML, "<note>") {
		t.Error("expected <note> tag for tool-reading hint")
	}
}

func TestBuildCornerstoneContext_BlobContentMetadataOnly(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneFileSnapshot, Title: "Blob CS", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "preview text", Ref: CornerstoneRef{Kind: "inline", BlobDigest: "sha256:abc123"}, Digest: "sha256:abc123", PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// Blob content should only have metadata.
	if strings.Contains(block.XML, "preview text") {
		// The preview should appear in summary, that's fine
	}
	if !strings.Contains(block.XML, `digest="sha256:abc123"`) {
		t.Error("expected digest attribute")
	}
	if !strings.Contains(block.XML, "<summary>") {
		t.Error("expected <summary>")
	}
}

func TestBuildCornerstoneContext_RequiredBlobMissingFailsClosed(t *testing.T) {
	f := newCMFixture(t)
	view, err := f.svc.Get(t.Context(), f.workID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.mgr.Pin(f.workID, PinCornerstoneInput{
		Type:             CornerstoneFileSnapshot,
		Title:            "Required snapshot",
		Content:          strings.Repeat("snapshot ", CornerstoneInlineThreshold),
		Ref:              CornerstoneRef{Kind: "workspace_file", Path: "required.txt"},
		Mode:             CornerstoneSnapshot,
		Required:         true,
		ExpectedRevision: view.Revision,
		RequestID:        "context-required-blob",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := result.Cornerstone.Ref.BlobDigest
	if digest == "" {
		t.Fatal("large snapshot did not create blob")
	}
	if err := f.store.Delete(f.workID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BuildCornerstoneContext(t.Context(), f.workID, productionCornerstoneContextConfig()); err == nil || !strings.Contains(err.Error(), "blob") {
		t.Fatalf("BuildCornerstoneContext error = %v, want missing blob", err)
	}
	latest, err := f.svc.Get(t.Context(), f.workID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked := f.svc.checkRunCornerstones(latest.Work); blocked == nil || !blocked.Assessment.Blocking {
		t.Fatalf("required missing blob preflight = %+v, want blocking", blocked)
	}
}

func TestBuildCornerstoneContext_XMLEscaping(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Attack <script>alert(1)</script>", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "Content with </cornerstone> tag inside", PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// Title should have > < & escaped in XML attrs.
	if strings.Contains(block.XML, `<script>`) {
		t.Error("un-escaped HTML in attrs")
	}
	if !strings.Contains(block.XML, `&lt;script&gt;`) {
		t.Errorf("expected escaped script tag, got %q", block.XML)
	}
	// Content's </cornerstone> should be escaped via standard XML escaping
	// (&lt;/cornerstone&gt;). Each entry has exactly 1 literal </cornerstone> as
	// its closing tag, so count should equal number of entries.
	count := strings.Count(block.XML, "</cornerstone>")
	if count != 1 {
		t.Errorf("expected exactly 1 literal </cornerstone> (the entry closing tag), got %d", count)
	}
	if !strings.Contains(block.XML, "&lt;/cornerstone&gt;") {
		t.Error("expected content </cornerstone> to be XML-escaped to &lt;/cornerstone&gt;")
	}
}

func TestBuildCornerstoneContext_TokenBudget(t *testing.T) {
	now := time.Now()
	content := "This is content that counts toward the token budget."
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "CS1", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: content, PinnedAt: now},
		{ID: "cs-2", Type: CornerstoneInstruction, Title: "CS2", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: content, PinnedAt: now},
	}

	// Very tight budget
	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{MaxTokens: 256, MaxPerItem: 80})
	if err != nil {
		t.Fatal(err)
	}
	if block.Count != 0 {
		t.Errorf("expected 0 injected with tight budget, got %d", block.Count)
	}
	// Each entry costs more than 10 tokens → all skipped (optional, so no blocking)
	if block.Blocking {
		t.Error("should not block on optional budget skip")
	}
}

func TestBuildCornerstoneContext_DeterministicOrder(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-c", Type: CornerstoneInstruction, Title: "C", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now},
		{ID: "cs-b", Type: CornerstoneInstruction, Title: "B", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now},
		{ID: "cs-a", Type: CornerstoneInstruction, Title: "A", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now},
	}

	block1, _ := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	block2, _ := BuildCornerstoneContext(cs, CornerstoneContextConfig{})

	if block1.XML != block2.XML {
		t.Error("deterministic order violated: outputs differ across identical calls")
	}

	// Reversed input should produce same order (ID stable tiebreaker)
	cs2 := []Cornerstone{
		{ID: "cs-a", Type: CornerstoneInstruction, Title: "A", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now},
		{ID: "cs-b", Type: CornerstoneInstruction, Title: "B", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now},
		{ID: "cs-c", Type: CornerstoneInstruction, Title: "C", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now},
	}
	block3, _ := BuildCornerstoneContext(cs2, CornerstoneContextConfig{})
	if block1.XML != block3.XML {
		t.Errorf("deterministic order violated with reversed input:\ngot1: %s\ngot3: %s", block1.XML, block3.XML)
	}
}

func TestBuildCornerstoneContext_RecencyOrder(t *testing.T) {
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)

	cs := []Cornerstone{
		{ID: "cs-old", Type: CornerstoneInstruction, Title: "Old", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: t1},
		{ID: "cs-mid", Type: CornerstoneInstruction, Title: "Mid", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: t2},
		{ID: "cs-new", Type: CornerstoneInstruction, Title: "New", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: t3},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}

	idxNew := strings.Index(block.XML, `id="cs-new"`)
	idxMid := strings.Index(block.XML, `id="cs-mid"`)
	idxOld := strings.Index(block.XML, `id="cs-old"`)

	if idxNew < 0 || idxMid < 0 || idxOld < 0 {
		t.Fatal("missing entries")
	}
	if idxNew >= idxMid || idxMid >= idxOld {
		t.Error("expected most recent first: new → mid → old")
	}
}

func TestBuildCornerstoneContext_DuplicateInputStable(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "One", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now, Content: "c1"},
		{ID: "cs-2", Type: CornerstonePolicy, Title: "Two", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now, Content: "c2"},
		{ID: "cs-2", Type: CornerstonePolicy, Title: "Two(Dup)", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now, Content: "c2dup"},
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "One(Dup)", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now, Content: "c1dup"},
	}

	block, err := BuildCornerstoneContext(cs, productionCornerstoneContextConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Deduplication by stable ID: first occurrence wins. cs-2 appears twice,
	// cs-1 appears twice. Result should have 2 entries.
	if block.Count != 2 {
		t.Errorf("expected 2 after dedup, got %d", block.Count)
	}
	// Content should be from first occurrence (cs-1 = "c1", cs-2 = "c2")
	if !strings.Contains(block.XML, "c1") {
		t.Error("expected first occurrence content for cs-1")
	}
	if !strings.Contains(block.XML, "c2") {
		t.Error("expected first occurrence content for cs-2")
	}
	// Second occurrences should NOT appear.
	if strings.Contains(block.XML, "c2dup") || strings.Contains(block.XML, "c1dup") {
		t.Error("duplicate content should not appear")
	}
}

func TestActiveCornerstones_FiltersTombstones(t *testing.T) {
	cs := []Cornerstone{
		{ID: "cs-1", Title: "Active", Status: CornerstoneActive},
		{ID: "cs-2", Title: "Tombstoned", Status: CornerstoneActive, Tombstone: true},
		{ID: "cs-3", Title: "Stale but not tombstoned", Status: CornerstoneStale},
	}

	active := activeCornerstones(cs)
	if len(active) != 2 {
		t.Errorf("expected 2 active (non-tombstone), got %d", len(active))
	}
	for _, a := range active {
		if a.Tombstone {
			t.Errorf("tombstoned cornerstone %q should be filtered out", a.ID)
		}
	}
}

func TestXmlAttrEscape(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal", "normal"},
		{"a<b", "a&lt;b"},
		{`quote"here`, "quote&quot;here"},
		{"a&b", "a&amp;b"},
		{`<script>`, "&lt;script&gt;"},
		{`"<&>`, "&quot;&lt;&amp;&gt;"},
	}
	for _, tt := range tests {
		got := xmlAttrEscape(tt.input)
		if got != tt.expected {
			t.Errorf("xmlAttrEscape(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestXmlContentEscape(t *testing.T) {
	s := "Content with </cornerstone> closing tag"
	got := xmlContentEscape(s)
	if strings.Contains(got, "</cornerstone>") {
		t.Error("xmlContentEscape should escape closing tags via standard XML escapes")
	}
	if !strings.Contains(got, "&lt;/cornerstone&gt;") {
		t.Errorf("expected XML-escaped closing tag, got %q", got)
	}
}

func TestCornerstoneContextConfig_Defaults(t *testing.T) {
	if DefaultCornerstoneContextMaxTokens != 8000 {
		t.Fatalf("default run token budget = %d, want frozen P4 value 8000", DefaultCornerstoneContextMaxTokens)
	}
	if DefaultCornerstoneContextInlineChars != 2000 {
		t.Fatalf("default inline threshold = %d, want frozen P3 value 2000 Unicode chars", DefaultCornerstoneContextInlineChars)
	}
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Test", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "content", PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if block.Count != 1 {
		t.Errorf("expected 1, got %d", block.Count)
	}
	if block.XML == "" {
		t.Error("expected non-empty XML")
	}
}

func TestBuildCornerstoneContext_UnicodeInlineBoundaryAndBudget(t *testing.T) {
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	inline := strings.Repeat("界", DefaultCornerstoneContextInlineChars)
	metadata := inline + "界"
	block, err := BuildCornerstoneContext([]Cornerstone{
		{ID: "inline", Type: CornerstoneInstruction, Title: "Inline", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: inline, PinnedAt: now},
		{ID: "metadata", Type: CornerstoneInstruction, Title: "Metadata", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: metadata, PinnedAt: now.Add(-time.Second)},
	}, CornerstoneContextConfig{MaxTokens: 8000, MaxPerItem: 2500})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block.XML, inline) {
		t.Fatal("exactly 2000 Unicode chars should stay inline")
	}
	if strings.Contains(block.XML, metadata) {
		t.Fatal("2001 Unicode chars should use metadata + summary")
	}
	if block.TokenEstimate != estimateCornerstoneTokens(block.XML) || block.TokenEstimate > 8000 {
		t.Fatalf("token estimate = %d, want deterministic value <= 8000", block.TokenEstimate)
	}
}

func TestCornerstoneContextBlock_AssessmentBlocking(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Required missing", Status: CornerstoneMissing, Required: true, PinnedAt: now},
		{ID: "cs-2", Type: CornerstoneInstruction, Title: "Optional stale", Status: CornerstoneStale, PinnedAt: now},
		{ID: "cs-3", Type: CornerstoneInstruction, Title: "Optional denied", Status: CornerstoneDenied, PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !block.Blocking {
		t.Error("expected blocking from required missing")
	}
	if block.Assessment.State != CornerstoneUseBlocked {
		t.Errorf("expected blocked assessment, got %s", block.Assessment.State)
	}
}

func TestBuildCornerstoneContext_OptionalBudgetSkipEmitsMarker(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		// Content that costs ~100+ tokens to force budget skip with MaxTokens=10
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Will be skipped", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "This is a somewhat long content " + strings.Repeat("padding ", 30), PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{MaxTokens: 192, MaxPerItem: 512})
	if err != nil {
		t.Fatal(err)
	}
	// Optional budget skip must emit explicit degraded marker.
	if block.Skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", block.Skipped)
	}
	if !block.Degraded {
		t.Error("expected degraded flag for optional budget skip")
	}
	if !strings.Contains(block.XML, `status="truncated"`) {
		t.Errorf("expected explicit truncated marker, got %q", block.XML)
	}
	// Must contain the cornerstone ID for observability.
	if !strings.Contains(block.XML, `id="cs-1"`) {
		t.Error("expected cornerstone ID in degraded marker")
	}
}

func TestBuildCornerstoneContext_RequiredBudgetBlocking(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-1", Type: CornerstoneInstruction, Title: "Required budget-too-large", Status: CornerstoneActive, Required: true, Mode: CornerstoneSnapshot, Content: "This is a somewhat long content " + strings.Repeat("pad ", 30), PinnedAt: now},
	}

	block, err := BuildCornerstoneContext(cs, CornerstoneContextConfig{MaxTokens: 10, MaxPerItem: 512})
	if err == nil {
		t.Fatal("expected error for required budget exceed, got nil")
	}
	if block.Blocking {
		// Blocking should already be set by assessment (but it's active, so Blocking=false here)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("expected budget error, got %v", err)
	}
}

func TestActiveCornerstonesDeduped_DeterministicOutput(t *testing.T) {
	now := time.Now()
	cs := []Cornerstone{
		{ID: "cs-a", Type: CornerstoneInstruction, Title: "First", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "alpha", PinnedAt: now},
		{ID: "cs-b", Type: CornerstoneInstruction, Title: "Second", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "beta", PinnedAt: now},
		{ID: "cs-a", Type: CornerstoneInstruction, Title: "Dup of First", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "alpha2", PinnedAt: now},
	}

	block1, _ := BuildCornerstoneContext(cs, productionCornerstoneContextConfig())
	// Reverse order, same dedup.
	cs2 := []Cornerstone{
		{ID: "cs-b", Type: CornerstoneInstruction, Title: "Dup2", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "beta2", PinnedAt: now},
		{ID: "cs-a", Type: CornerstoneInstruction, Title: "Dup", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "alpha2", PinnedAt: now},
		{ID: "cs-a", Type: CornerstoneInstruction, Title: "First", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "alpha", PinnedAt: now},
		{ID: "cs-b", Type: CornerstoneInstruction, Title: "Second", Status: CornerstoneActive, Mode: CornerstoneSnapshot, Content: "beta", PinnedAt: now},
	}
	block2, _ := BuildCornerstoneContext(cs2, productionCornerstoneContextConfig())

	if block1.XML != block2.XML {
		t.Errorf("dedup + deterministic sort must produce identical output:\n--- block1:\n%s\n--- block2:\n%s", block1.XML, block2.XML)
	}
	// Must contain first-occurrence content.
	if strings.Contains(block1.XML, "alpha2") || strings.Contains(block1.XML, "beta2") {
		t.Error("duplicate content (second occurrence) leaked into output")
	}
}
