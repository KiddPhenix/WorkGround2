package agent

import (
	"strings"

	"workground2/internal/event"
	"workground2/internal/provider"
)

// anchoredBootstrap implements the two-phase DeepSeek bootstrap, adapted from
// the dsh-anchored-standard preset mechanism: the first model request exposes
// a minimal tool catalog (bash + read_file + edit_file) and a shortened
// system prompt — the full prompt minus its trailing memory/skills injection
// — anchoring the model's trajectory on the minimal condition; once the
// session records its first assistant reply, the full catalog and the full
// system prompt return.
//
// The phase is derived from the durable session message log, so resume and
// reload reconstruct it, and it never demotes: once promoted, compaction or
// rewind cannot return the session to the bootstrap surface.
type anchoredBootstrap struct {
	// bootstrapSystemPrompt is the full system prompt with the trailing
	// memory/skills sections removed. It is a strict prefix of the full
	// prompt, so the provider's prefix cache keeps the leading tokens warm
	// when promotion appends the injected sections back.
	bootstrapSystemPrompt string

	// bootstrapTools is the model-visible catalog on the first request.
	bootstrapTools map[string]struct{}

	// promoted latches the promotion decision for this process. The durable
	// scan runs at most once per session per process, then O(1).
	promoted bool

	// degraded latches the one-time fallback when a bootstrap tool is missing
	// from the registry: both the catalog filter and the prompt swap are
	// skipped for the whole process, restoring the un-armed behavior.
	degraded bool

	// notifiedBootstrap / notifiedPromoted / notifiedNotice gate the one-shot
	// notices so a resumed session does not re-announce the phase every turn.
	notifiedBootstrap bool
	notifiedPromoted  bool
	notifiedNotice    bool
}

// anchoredArmed reports whether the two-phase bootstrap applies to THIS agent:
// the mechanism was configured at boot (bootstrap prompt provided) and the
// session's first system message actually differs from it (empty injected
// sections, or a session whose prompt is already the bootstrap prefix, make
// the swap a no-op). Scope is controlled at boot: only the main executor
// session receives the bootstrap prompt, so task executors and subagents with
// their own prompts are never filtered.
func (a *Agent) anchoredArmed() bool {
	if a.anchored == nil || a.anchored.bootstrapSystemPrompt == "" {
		return false
	}
	msgs := a.session.Messages
	if len(msgs) == 0 || msgs[0].Role != provider.RoleSystem {
		return false
	}
	return msgs[0].Content != a.anchored.bootstrapSystemPrompt
}

// anchoredPromoted reports whether the session has produced its first
// assistant reply. Once true it stays true for the process lifetime; a
// compaction summary also counts as evidence of prior content, so a session
// resumed after a fold does not fall back to the bootstrap surface.
func (a *Agent) anchoredPromoted() bool {
	if a.anchored == nil {
		return true
	}
	if a.anchored.promoted {
		return true
	}
	for _, m := range a.session.Messages {
		if m.Role == provider.RoleAssistant || m.Role == provider.RoleTool || isCompactionSummary(m) {
			a.anchored.promoted = true
			return true
		}
	}
	return false
}

// anchoredWebRequested reports whether any user message in the conversation
// asks for a URL/web page. The bootstrap surface hides the web tools, so a
// conversation that starts with a URL would leave the model unable to fetch
// it; such conversations skip the bootstrap and keep the full catalog.
func (a *Agent) anchoredWebRequested() bool {
	if a.anchored == nil {
		return false
	}
	for _, m := range a.session.Messages {
		if m.Role != provider.RoleUser {
			continue
		}
		if webRequested(m.Content) {
			return true
		}
	}
	return false
}

// webRequested reports whether the text names an http(s) or www URL.
func webRequested(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "http://") ||
		strings.Contains(lower, "https://") ||
		strings.Contains(lower, "www.")
}

// anchoredBootstrapActive reports whether the bootstrap surface applies to
// THIS request: armed, unpromoted, not degraded, and the conversation asks
// for no URL/web page. Notices for skips are emitted from
// maybeNoticeAnchored, so this stays free of side effects.
func (a *Agent) anchoredBootstrapActive() bool {
	return a.anchoredArmed() && !a.anchoredPromoted() && !a.anchored.degraded && !a.anchoredWebRequested()
}

// effectiveSchemas returns the tool schemas to send on this request: the
// bootstrap trio while unpromoted, the full catalog once promoted. A missing
// bootstrap tool degrades to the full catalog with a one-time notice instead
// of failing the request, so a composition drift can never brick a session.
func (a *Agent) effectiveSchemas() []provider.ToolSchema {
	full := a.tools.Schemas()
	if !a.anchoredBootstrapActive() {
		return full
	}
	kept := make([]provider.ToolSchema, 0, len(a.anchored.bootstrapTools))
	for _, s := range full {
		if _, ok := a.anchored.bootstrapTools[s.Name]; ok {
			kept = append(kept, s)
		}
	}
	if len(kept) != len(a.anchored.bootstrapTools) {
		a.anchored.degraded = true
		a.noticeAnchored("anchored bootstrap: bootstrap tool missing from registry; falling back to the full catalog for this session")
		return full
	}
	return kept
}

// effectiveMessages returns the message list to send on this request. While
// unpromoted, the system message is swapped — in a copy; the session's
// durable log always keeps the full prompt, so promotion and resume are
// idempotent — for the bootstrap prefix version. Once promoted the full
// prompt flows unchanged.
func (a *Agent) effectiveMessages() []provider.Message {
	msgs := a.session.Messages
	if !a.anchoredBootstrapActive() {
		return msgs
	}
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	out[0].Content = a.anchored.bootstrapSystemPrompt
	return out
}

// maybeNoticeAnchored announces the phase transitions once each: the
// bootstrap surface on the first unpromoted request, and the promotion back
// to the full surface on the first promoted request.
func (a *Agent) maybeNoticeAnchored() {
	if !a.anchoredArmed() || a.anchored.degraded {
		return
	}
	if a.anchoredWebRequested() {
		a.noticeAnchored("anchored bootstrap: skipped — conversation requests a URL/web page; full catalog exposed")
		return
	}
	if a.anchoredPromoted() {
		if !a.anchored.notifiedPromoted {
			a.anchored.notifiedPromoted = true
			a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: "anchored bootstrap: promoted — full tool catalog and system prompt restored"})
		}
		return
	}
	if !a.anchored.notifiedBootstrap {
		a.anchored.notifiedBootstrap = true
		a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: "anchored bootstrap: first request on minimal surface (bash + read_file + edit_file)"})
	}
}

// noticeAnchored emits a one-shot bootstrap warning (degradations).
func (a *Agent) noticeAnchored(text string) {
	if a.anchored == nil || a.anchored.notifiedNotice {
		return
	}
	a.anchored.notifiedNotice = true
	a.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: text})
}
