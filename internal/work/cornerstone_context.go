package work

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ── Cornerstone context injection ─────────────────────────────────────────

// Default budgets for production Cornerstone context injection.
const (
	// DefaultCornerstoneContextMaxTokens is the default total token budget for the
	// transient <cornerstone> block. Production callers must use a non-zero value.
	DefaultCornerstoneContextMaxTokens = 8000

	// DefaultCornerstoneContextMaxPerItem is the default per-entry token budget.
	DefaultCornerstoneContextMaxPerItem = 2500

	// DefaultCornerstoneContextInlineChars is the frozen P3 context threshold.
	// It is deliberately separate from the blob storage byte threshold.
	DefaultCornerstoneContextInlineChars = 2000
)

// CornerstoneContextConfig controls how active Cornerstones are rendered into
// a transient XML block for model context injection.
type CornerstoneContextConfig struct {
	// MaxTokens is the independent token budget for the cornerstone block.
	// Content is truncated when the budget is exhausted.
	MaxTokens int

	// MaxPerItem is the maximum token budget for a single cornerstone entry.
	// Remaining content is replaced by metadata and a summary.
	MaxPerItem int

	// LargeContentThreshold is the Unicode character count above which content is treated as
	// "large" and only metadata + summary are injected (content is fetched via
	// permission-constrained tools). Zero defaults to 2,000 Unicode characters.
	LargeContentThreshold int

	// BlobStore is optional. When non-nil, BlobDigest refs are checked for
	// existence and integrity. When nil, blob-stored cornerstones get
	// metadata-only treatment without storage validation.
	BlobStore BlobStore
}

// productionCornerstoneContextConfig returns a config with production budgets.
func productionCornerstoneContextConfig() CornerstoneContextConfig {
	return CornerstoneContextConfig{
		MaxTokens:  DefaultCornerstoneContextMaxTokens,
		MaxPerItem: DefaultCornerstoneContextMaxPerItem,
	}
}

// CornerstoneContextBlock is the result of building a cornerstone context block.
type CornerstoneContextBlock struct {
	// XML is the serialized <cornerstone> XML block, ready for injection.
	XML string

	// Assessment summarises the health of injected cornerstones.
	Assessment CornerstoneAssessment

	// Blocking is true when at least one required cornerstone is missing, denied,
	// stale, or invalid. The caller must not proceed with a partial run.
	Blocking bool

	// Degraded is true when at least one optional cornerstone has issues.
	// The run may proceed but the model sees explicit degraded markers.
	Degraded bool

	// Skipped is the count of cornerstones that were omitted due to budget,
	// large content, or errors. Each skipped entry produces an observable
	// degraded marker.
	Skipped int

	// Truncated is the count of entries that were truncated to fit the budget.
	Truncated int

	// Count is the number of cornerstones included in the block.
	Count int

	// ActiveCount is the deduplicated, non-tombstone count before budget
	// truncation. Cleanup/compaction notices use this value.
	ActiveCount int

	// SkippedIDs exposes stable IDs without content for logs and diagnostics.
	SkippedIDs []string

	// TokenEstimate uses the frozen deterministic fallback. It is not a
	// provider tokenizer count.
	TokenEstimate int
}

// BuildCornerstoneContext renders active (non-tombstone), deduplicated
// Cornerstones into a deterministic transient <cornerstone> XML block for
// injection between the cache-stable system prefix and the current user turn.
//
// Deduplication: cornerstones are deduplicated by stable ID with a
// deterministic winner. The same values in any order produce identical output.
//
// Sorting: required first, then by type (stable order), then by PinnedAt
// descending, breaking ties with the stable ID.
//
// Budget: MaxTokens limits the total block; MaxPerItem limits individual entries.
// Large content (over threshold or blob-stored) gets metadata-only injection.
//
// Required cornerstones in non-active status (missing/denied/stale/invalid)
// produce a blocking error. Optional failures produce degraded markers but
// do not block execution.
func BuildCornerstoneContext(cornerstones []Cornerstone, config CornerstoneContextConfig) (CornerstoneContextBlock, error) {
	active := activeCornerstonesDeduped(cornerstones)
	if len(active) == 0 {
		return CornerstoneContextBlock{}, nil
	}

	sortCornerstonesForContext(active)

	maxTokens := config.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultCornerstoneContextMaxTokens
	}
	maxPerItem := config.MaxPerItem
	if maxPerItem <= 0 {
		maxPerItem = DefaultCornerstoneContextMaxPerItem
	}
	threshold := config.LargeContentThreshold
	if threshold <= 0 {
		threshold = DefaultCornerstoneContextInlineChars
	}

	assessment := AssessCornerstones(active)
	block := CornerstoneContextBlock{
		Assessment:  assessment,
		Blocking:    assessment.Blocking,
		Degraded:    assessment.Degraded,
		ActiveCount: len(active),
	}

	blocking := false
	var b strings.Builder
	injected := 0
	const contextOpen = "<cornerstone-context>\n"
	const contextClose = "</cornerstone-context>\n"
	wrapperCost := estimateCornerstoneTokens(contextOpen + contextClose)
	if maxTokens <= wrapperCost {
		return block, fmt.Errorf("cornerstone context token budget %d cannot fit the transient block wrapper (%d)", maxTokens, wrapperCost)
	}
	tokenBudget := maxTokens - wrapperCost

	for _, cs := range active {
		entry, err := buildCornerstoneEntry(cs, config, threshold, maxPerItem)
		if err != nil {
			if cs.Required {
				return block, fmt.Errorf("cornerstone %q (required, type=%s): %w", cs.ID, cs.Type, err)
			}
			// Optional failure → explicit degraded marker.
			block.Degraded = true
			block.Skipped++
			block.SkippedIDs = append(block.SkippedIDs, cs.ID)
			marker, cost, ok := fitCornerstoneMarker(cs, err.Error(), tokenBudget)
			if !ok {
				return block, fmt.Errorf("cornerstone context budget exhausted before degraded marker for %q", cs.ID)
			}
			b.WriteString(marker)
			tokenBudget -= cost
			continue
		}
		if entry.blocking {
			blocking = true
			continue
		}
		if entry.skip {
			// Budget skip for optional: emit degraded marker, never silently skip.
			block.Degraded = true
			block.Skipped++
			block.SkippedIDs = append(block.SkippedIDs, cs.ID)
			marker, cost, ok := fitCornerstoneMarker(cs, "entry exceeded per-item budget", tokenBudget)
			if !ok {
				return block, fmt.Errorf("cornerstone context budget exhausted before truncation marker for %q", cs.ID)
			}
			b.WriteString(marker)
			tokenBudget -= cost
			continue
		}
		if entry.tokenCost > tokenBudget {
			if cs.Required {
				return block, fmt.Errorf("cornerstone %q: entry exceeds remaining token budget (%d > %d)", cs.ID, entry.tokenCost, tokenBudget)
			}
			block.Degraded = true
			block.Skipped++
			block.SkippedIDs = append(block.SkippedIDs, cs.ID)
			marker, cost, ok := fitCornerstoneMarker(cs, "entry exceeded remaining context budget", tokenBudget)
			if !ok {
				return block, fmt.Errorf("cornerstone context budget exhausted before truncation marker for %q", cs.ID)
			}
			b.WriteString(marker)
			tokenBudget -= cost
			continue
		}
		if entry.truncated {
			block.Truncated++
		}
		if !cs.Required && cs.Status != CornerstoneActive {
			block.Skipped++
			block.SkippedIDs = append(block.SkippedIDs, cs.ID)
		}
		b.WriteString(entry.xml)
		injected++
		tokenBudget -= entry.tokenCost
	}

	if b.Len() > 0 {
		block.XML = contextOpen + b.String() + contextClose
	}
	block.Count = injected
	block.TokenEstimate = estimateCornerstoneTokens(block.XML)
	if blocking {
		block.Blocking = true
	}
	return block, nil
}

// activeCornerstonesDeduped returns non-tombstone cornerstones deduplicated by
// stable ID. Corrupt projections containing two values for one ID choose a
// deterministic winner instead of depending on replay/input order.
func activeCornerstonesDeduped(cs []Cornerstone) []Cornerstone {
	seen := make(map[string]int, len(cs))
	out := make([]Cornerstone, 0, len(cs))
	for _, c := range cs {
		if c.Tombstone {
			continue
		}
		if i, ok := seen[c.ID]; ok {
			if preferCornerstone(c, out[i]) {
				out[i] = c
			}
			continue
		}
		seen[c.ID] = len(out)
		out = append(out, c)
	}
	return out
}

func preferCornerstone(next, current Cornerstone) bool {
	if !next.UpdatedAt.Equal(current.UpdatedAt) {
		return next.UpdatedAt.After(current.UpdatedAt)
	}
	if !next.PinnedAt.Equal(current.PinnedAt) {
		return next.PinnedAt.After(current.PinnedAt)
	}
	if next.Content != current.Content {
		return next.Content < current.Content
	}
	if next.Title != current.Title {
		return next.Title < current.Title
	}
	nextJSON, _ := json.Marshal(next)
	currentJSON, _ := json.Marshal(current)
	return string(nextJSON) < string(currentJSON)
}

// activeCornerstones returns non-tombstone cornerstones (no dedup; used in
// contexts where the caller owns uniqueness).
func activeCornerstones(cs []Cornerstone) []Cornerstone {
	out := make([]Cornerstone, 0, len(cs))
	for _, c := range cs {
		if c.Tombstone {
			continue
		}
		out = append(out, c)
	}
	return out
}

// sortCornerstonesForContext sorts cornerstones for deterministic injection:
// 1. required first
// 2. then by type (stable enum order)
// 3. then by PinnedAt descending (most recent first)
// 4. then by ID (stable tiebreaker)
func sortCornerstonesForContext(cs []Cornerstone) {
	sort.Slice(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Required != b.Required {
			return a.Required
		}
		if a.Type != b.Type {
			return cornerstoneTypeOrder[a.Type] < cornerstoneTypeOrder[b.Type]
		}
		if !a.PinnedAt.Equal(b.PinnedAt) {
			return a.PinnedAt.After(b.PinnedAt)
		}
		return a.ID < b.ID
	})
}

// cornerstoneTypeOrder defines a stable ordering for Cornerstone types.
var cornerstoneTypeOrder = map[CornerstoneType]int{
	CornerstoneInstruction:  0,
	CornerstonePolicy:       1,
	CornerstoneDecision:     2,
	CornerstoneConclusion:   3,
	CornerstoneSource:       4,
	CornerstoneParameter:    5,
	CornerstoneFileRef:      6,
	CornerstoneFileSnapshot: 7,
}

type cornerstoneEntry struct {
	xml       string
	blocking  bool
	skip      bool
	truncated bool
	tokenCost int
}

func buildCornerstoneEntry(cs Cornerstone, config CornerstoneContextConfig, threshold, maxPerItem int) (cornerstoneEntry, error) {
	zero := cornerstoneEntry{}

	// Required but not active → blocking. No entry to build; the assessment
	// already captured this, the caller checks the Blocking field.
	if cs.Required && cs.Status != CornerstoneActive {
		return cornerstoneEntry{blocking: true}, nil
	}
	if cs.Status != CornerstoneActive {
		entry := buildDegradedEntry(cs, cornerstoneStatusReason(cs))
		cost := estimateCornerstoneTokens(entry)
		if maxPerItem > 0 && cost > maxPerItem {
			return cornerstoneEntry{skip: true, truncated: true}, nil
		}
		return cornerstoneEntry{xml: entry, tokenCost: cost}, nil
	}

	// Determine if content should be inlined.
	isLarge := utf8.RuneCountInString(cs.Content) > threshold || cs.Ref.BlobDigest != ""
	if cs.Ref.BlobDigest != "" && config.BlobStore != nil {
		exists, err := config.BlobStore.Exists(cs.WorkID, cs.Ref.BlobDigest)
		if err != nil {
			return zero, fmt.Errorf("verify blob %q: %w", cs.Ref.BlobDigest, err)
		}
		if !exists {
			return zero, fmt.Errorf("blob %q is missing or invalid", cs.Ref.BlobDigest)
		}
	}

	var entry string
	if isLarge {
		entry = buildMetadataEntry(cs)
	} else {
		entry = buildInlineEntry(cs)
	}

	cost := estimateCornerstoneTokens(entry)

	// Per-item budget check.
	if maxPerItem > 0 && cost > maxPerItem {
		if cs.Required {
			return zero, fmt.Errorf("cornerstone %q: entry exceeds per-item budget (%d > %d)", cs.ID, cost, maxPerItem)
		}
		// Optional: truncated to per-item budget with explicit marker.
		entry = buildBudgetTruncatedEntry(cs, fmt.Sprintf("entry truncated to per-item budget (%d > %d)", cost, maxPerItem))
		cost = estimateCornerstoneTokens(entry)
		return cornerstoneEntry{skip: true, truncated: true}, nil
	}

	return cornerstoneEntry{xml: entry, tokenCost: cost}, nil
}

func cornerstoneStatusReason(cs Cornerstone) string {
	if strings.TrimSpace(cs.Error) != "" {
		return cs.Error
	}
	return fmt.Sprintf("cornerstone status is %s", cs.Status)
}

func buildInlineEntry(cs Cornerstone) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<cornerstone type="%s" id="%s" title="%s"`, xmlAttrEscape(string(cs.Type)), xmlAttrEscape(cs.ID), xmlAttrEscape(cs.Title)))
	if cs.Required {
		b.WriteString(` required="true"`)
	}
	if cs.Mode != "" {
		b.WriteString(fmt.Sprintf(` mode="%s"`, xmlAttrEscape(string(cs.Mode))))
	}
	b.WriteString(">\n")
	b.WriteString(xmlContentEscape(cs.Content))
	b.WriteString("\n</cornerstone>\n")
	return b.String()
}

func buildMetadataEntry(cs Cornerstone) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<cornerstone type="%s" id="%s" title="%s"`, xmlAttrEscape(string(cs.Type)), xmlAttrEscape(cs.ID), xmlAttrEscape(cs.Title)))
	if cs.Required {
		b.WriteString(` required="true"`)
	}
	b.WriteString(fmt.Sprintf(` mode="%s"`, xmlAttrEscape(string(cs.Mode))))
	b.WriteString(fmt.Sprintf(` digest="%s"`, xmlAttrEscape(cs.Digest)))
	if cs.Ref.Kind != "" {
		b.WriteString(fmt.Sprintf(` ref-kind="%s"`, xmlAttrEscape(cs.Ref.Kind)))
	}
	if cs.Ref.Path != "" {
		b.WriteString(fmt.Sprintf(` path="%s"`, xmlAttrEscape(cs.Ref.Path)))
	}
	if cs.Ref.URL != "" {
		b.WriteString(fmt.Sprintf(` url="%s"`, xmlAttrEscape(cs.Ref.URL)))
	}
	b.WriteString(">\n")

	// Summary / preview (truncated).
	summary := truncateContentPreview(cs.Content)
	if summary != "" {
		b.WriteString(fmt.Sprintf("<summary>%s</summary>\n", xmlContentEscape(summary)))
	}
	// Hint that omitted content should be read via permission-constrained tools.
	b.WriteString("<note>Full content is omitted from transient context. Use the appropriate permission-constrained source or artifact tool when it is needed.</note>\n")
	b.WriteString("</cornerstone>\n")
	return b.String()
}

func buildDegradedEntry(cs Cornerstone, reason string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<cornerstone type="%s" id="%s" title="%s" status="%s" reason="%s" required="%v">`, xmlAttrEscape(string(cs.Type)), xmlAttrEscape(cs.ID), xmlAttrEscape(cs.Title), xmlAttrEscape(string(cs.Status)), xmlAttrEscape(reason), cs.Required))
	b.WriteString("\n")
	if cs.Content != "" {
		b.WriteString(fmt.Sprintf("<error>%s</error>\n", xmlContentEscape(reason)))
	}
	b.WriteString("<note>This cornerstone is unavailable. The run may be incomplete.</note>\n")
	b.WriteString("</cornerstone>\n")
	return b.String()
}

// buildBudgetTruncatedEntry returns a truncated entry with an explicit marker
// that says the entry was omitted/truncated due to budget limits. Never returns
// an empty string — the model must see the degraded state.
func buildBudgetTruncatedEntry(cs Cornerstone, reason string) string {
	return fmt.Sprintf(`<cornerstone type="%s" id="%s" status="truncated" reason="%s"/>`+"\n",
		xmlAttrEscape(string(cs.Type)), xmlAttrEscape(cs.ID), xmlAttrEscape(reason))
}

func fitCornerstoneMarker(cs Cornerstone, reason string, budget int) (string, int, bool) {
	candidates := []string{
		buildBudgetTruncatedEntry(cs, reason),
		fmt.Sprintf(`<cornerstone id="%s" status="truncated"/>`+"\n", xmlAttrEscape(cs.ID)),
		"<cornerstone status=\"truncated\"/>\n",
		"<truncated/>\n",
	}
	for _, marker := range candidates {
		cost := estimateCornerstoneTokens(marker)
		if cost <= budget {
			return marker, cost, true
		}
	}
	return "", 0, false
}

// estimateCornerstoneTokens is the deterministic fallback frozen by P4. It is
// intentionally conservative for ASCII and safe for multi-byte Unicode.
func estimateCornerstoneTokens(s string) int {
	runes := utf8.RuneCountInString(s)
	bytesQuarter := (len([]byte(s)) + 3) / 4
	if runes > bytesQuarter {
		return runes
	}
	return bytesQuarter
}

// ── XML escaping ──────────────────────────────────────────────────────────

func xmlAttrEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func xmlContentEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
