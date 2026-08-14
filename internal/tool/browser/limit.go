package browsertool

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"workground2/internal/browser"
)

// stateLimitWarningCode is the StateWarning code marking shape-aware output
// truncation, so consumers can tell it apart from driver observation warnings
// (e.g. "frame_target_unavailable").
const stateLimitWarningCode = "output_truncated"

// LimitOutput fits a successful browser_state result to the agent's per-tool
// byte budget. The result is a JSON envelope whose element objects must stay
// whole (the model reuses their indices) and whose metadata — revision, URL,
// title, tabs — must survive, so a generic head/tail cut would garble it.
// Strategy, in order: compact the page text to the longest rune prefix that
// still fits with all elements kept, then retain only whole trailing-kept
// element objects as needed, then append a structured StateWarning reporting
// what was elided. When elements are trimmed, next_element_index and
// remaining_elements are set so the caller can page the rest from the same
// snapshot. Declines (ok=false) only for payloads that are not this envelope,
// or when even the bare envelope (no text, no elements) exceeds the budget — a
// case no shape-aware cut can fix, so the agent falls back to its generic
// truncation.
func (t *stateTool) LimitOutput(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, true
	}
	var resp ToolResponse[browser.PageState]
	if err := json.Unmarshal([]byte(s), &resp); err != nil || !resp.OK || resp.Result == nil {
		return "", false
	}
	st := *resp.Result
	origText := st.Text
	origElements := st.Elements
	origWarnings := st.Warnings
	textRunes := []rune(origText)
	textKeep := len(textRunes)
	elemKeep := len(origElements)

	// The structured warning is appended after fitting, so the fit targets
	// must reserve room for it: use the worst-case message length plus slack
	// so appending the real warning never pushes the encoding back over the
	// budget. The final re-fit loop below remains the backstop.
	warningReserve := len(stateLimitMessage(true, len(textRunes), 0, len(origElements), len(origElements))) + 64
	fitBudget := maxBytes - warningReserve
	if fitBudget < 0 {
		return "", false
	}

	// 1) Compact page text first: the longest rune prefix that still fits with
	// all elements kept. Cut on rune boundaries so a multibyte glyph is never
	// split.
	if textKeep > 0 {
		lo, hi := 0, textKeep
		for lo < hi {
			mid := (lo + hi + 1) / 2
			st.Text = string(textRunes[:mid])
			st.Elements = origElements
			if b, err := marshalState(st); err == nil && len(b) <= fitBudget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		textKeep = lo
	}
	st.Text = string(textRunes[:textKeep])

	// 2) Then retain only whole element objects: a prefix of the original
	// slice, so every surviving element keeps its original index.
	if elemKeep > 0 {
		lo, hi := 0, elemKeep
		for lo < hi {
			mid := (lo + hi + 1) / 2
			st.Elements = origElements[:mid]
			if b, err := marshalState(st); err == nil && len(b) <= fitBudget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		elemKeep = lo
	}
	st.Elements = origElements[:elemKeep]

	// 3) Mark the state truncated and report what was elided. If the warning
	// itself pushes the encoding over the budget, shed whole trailing elements
	// (then text runes) until the final result fits.
	elidedElements := len(origElements) - elemKeep
	textCompacted := textKeep < len(textRunes)
	for {
		st.Truncated = true
		// Pagination hints: when whole elements were trimmed to fit the
		// budget and the remaining elements are actually fetchable, tell the
		// caller where the next page begins — the original index of the first
		// elided element — and how many elements remain. If not even a single
		// element fits, there is no next page to advertise (requesting one
		// would loop forever).
		st.NextElementIndex = 0
		st.RemainingElements = 0
		if len(st.Elements) > 0 && len(st.Elements) < len(origElements) {
			st.NextElementIndex = origElements[len(st.Elements)].Index
			st.RemainingElements = len(origElements) - len(st.Elements)
		}
		st.Warnings = origWarnings
		if textCompacted || elidedElements > 0 {
			st.Warnings = append(st.Warnings, browser.StateWarning{
				Code:    stateLimitWarningCode,
				Message: stateLimitMessage(textCompacted, len(textRunes), utf8.RuneCountInString(st.Text), elidedElements, len(origElements)),
			})
		}
		b, err := marshalState(st)
		if err != nil {
			return "", false
		}
		if len(b) <= maxBytes {
			return string(b), true
		}
		if len(st.Elements) > 0 {
			st.Elements = st.Elements[:len(st.Elements)-1]
			elidedElements++
			continue
		}
		if len(st.Text) > 0 {
			st.Text = string([]rune(st.Text)[:utf8.RuneCountInString(st.Text)-1])
			textCompacted = true
			continue
		}
		return "", false // bare envelope itself exceeds the budget
	}
}

// marshalState encodes the success envelope around st.
func marshalState(st browser.PageState) ([]byte, error) {
	return json.Marshal(ToolResponse[browser.PageState]{OK: true, Result: &st})
}

// stateLimitMessage builds the concise structured warning describing what was
// elided. It stays short (a fixed prefix plus a couple of counts) so it
// always fits alongside the compacted payload.
func stateLimitMessage(textCompacted bool, origTextRunes, keptTextRunes, elidedElements, origElements int) string {
	parts := make([]string, 0, 2)
	if textCompacted {
		parts = append(parts, fmt.Sprintf("text compacted %d→%d runes", origTextRunes, keptTextRunes))
	}
	if elidedElements > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d elements elided", elidedElements, origElements))
	}
	return "output truncated: " + strings.Join(parts, "; ")
}
