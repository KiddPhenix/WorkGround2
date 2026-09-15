package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/event"
	"workground2/internal/provider"
)

// ToolStopStatus distinguishes an accepted cancellation from an already settled call.
type ToolStopStatus string

const (
	ToolStopAccepted       ToolStopStatus = "accepted"
	ToolStopAlreadyStopped ToolStopStatus = "already_stopped"
	ToolStopFinished       ToolStopStatus = "finished"
)

type ToolStopResult struct {
	StopID string         `json:"stopId"`
	CallID string         `json:"callId,omitempty"`
	Status ToolStopStatus `json:"status"`
}

type ActiveToolInfo struct {
	StopID        string    `json:"stopId"`
	CallID        string    `json:"callId"`
	Name          string    `json:"name"`
	StartedAt     time.Time `json:"startedAt"`
	ElapsedMs     int64     `json:"elapsedMs"`
	StopRequested bool      `json:"stopRequested,omitempty"`
}

// ProgressSnapshot contains runtime-local provider usage and actual activity.
// Polling itself never advances ProgressSeq. Missing token counts mean unknown.
type ProgressSnapshot struct {
	ProgressSeq       int64            `json:"progressSeq"`
	Phase             string           `json:"phase"`
	CountsScope       string           `json:"countsScope"`
	UsageSeen         bool             `json:"usageSeen"`
	InputTokens       *int64           `json:"inputTokens,omitempty"`
	OutputTokens      *int64           `json:"outputTokens,omitempty"`
	UsageMode         string           `json:"usageMode"`
	UsageSource       string           `json:"usageSource,omitempty"`
	Provider          string           `json:"provider,omitempty"`
	UsageAt           time.Time        `json:"usageAt,omitzero"`
	LastTokenGrowthAt time.Time        `json:"lastTokenGrowthAt,omitzero"`
	LastProgressAt    time.Time        `json:"lastProgressAt,omitzero"`
	StartedAt         time.Time        `json:"startedAt,omitzero"`
	ActiveTools       []ActiveToolInfo `json:"activeTools,omitempty"`
}

const ProgressCountsScope = "provider-reported executor and compaction requests since runtime start; excludes unreported usage and subagents"

type activeTool struct {
	stopID, callID, name string
	started              time.Time
	stopped              bool
	cancel               context.CancelFunc
}

type progressState struct {
	epoch                                  uint64
	seq                                    int64
	phase                                  string
	started                                time.Time
	usageSeen                              bool
	input, output                          int64
	usageMode, usageSource, usageProvider  string
	usageAt, tokenGrowthAt, lastProgressAt time.Time
	calls                                  map[string]*activeTool
}

func (st *progressState) markProgressEvent(now time.Time) { st.seq++; st.lastProgressAt = now }

func (a *Agent) resetProgressTracking() {
	a.progressMu.Lock()
	old := a.progress.calls
	now := time.Now()
	a.progress = progressState{epoch: a.progress.epoch + 1, started: now, phase: "idle", usageMode: "unavailable", lastProgressAt: now, calls: make(map[string]*activeTool)}
	a.progressMu.Unlock()
	for _, call := range old {
		call.cancel()
	}
}

// beginToolCall publishes a unique cancellation capability only after the tool
// is ready to execute. Provider call IDs remain unchanged and can safely repeat.
func (a *Agent) beginToolCall(parent context.Context, callID, name string) (context.Context, func() bool) {
	ctx, cancel := context.WithCancel(parent)
	entry := &activeTool{stopID: "tool-" + rand.Text(), callID: callID, name: name, started: time.Now(), cancel: cancel}
	a.progressMu.Lock()
	if a.progress.calls == nil {
		a.progress.calls = make(map[string]*activeTool)
	}
	a.progress.calls[entry.stopID] = entry
	a.progress.phase = "tool"
	a.progress.markProgressEvent(entry.started)
	a.progressMu.Unlock()
	var once sync.Once
	var stopped bool
	finish := func() bool {
		once.Do(func() {
			a.progressMu.Lock()
			stopped = entry.stopped
			if a.progress.calls[entry.stopID] == entry {
				delete(a.progress.calls, entry.stopID)
				a.progress.markProgressEvent(time.Now())
				if len(a.progress.calls) == 0 && a.progress.phase == "tool" {
					a.progress.phase = "model"
				}
			}
			a.progressMu.Unlock()
			cancel()
		})
		return stopped
	}
	// A panicking sink must not leave a registered child context behind.
	func() {
		published := false
		defer func() {
			if !published {
				finish()
			}
		}()
		a.sink.Emit(event.Event{Kind: event.ToolProgress, Tool: event.Tool{ID: callID, Name: name, StopID: entry.stopID}})
		published = true
	}()
	return ctx, finish
}

// StopToolCall accepts a runtime-issued stop ID, never a provider call ID.
// It only requests cancellation; the real tool result signals completion.
func (a *Agent) StopToolCall(stopID string) ToolStopResult {
	a.progressMu.Lock()
	entry := a.progress.calls[stopID]
	result := ToolStopResult{StopID: stopID, Status: ToolStopFinished}
	if entry == nil {
		a.progressMu.Unlock()
		return result
	}
	result.CallID = entry.callID
	if entry.stopped {
		result.Status = ToolStopAlreadyStopped
		a.progressMu.Unlock()
		return result
	}
	entry.stopped = true
	result.Status = ToolStopAccepted
	a.progress.markProgressEvent(time.Now())
	a.progressMu.Unlock()
	entry.cancel()
	return result
}

func (a *Agent) ProgressSnapshot() ProgressSnapshot {
	a.progressMu.Lock()
	defer a.progressMu.Unlock()
	st := &a.progress
	snap := ProgressSnapshot{ProgressSeq: st.seq, Phase: st.phase, CountsScope: ProgressCountsScope,
		UsageSeen: st.usageSeen, UsageMode: st.usageMode, LastProgressAt: st.lastProgressAt,
		StartedAt: st.started, LastTokenGrowthAt: st.tokenGrowthAt, UsageAt: st.usageAt,
		UsageSource: st.usageSource, Provider: st.usageProvider}
	if st.usageSeen {
		input, output := st.input, st.output
		snap.InputTokens = &input
		snap.OutputTokens = &output
	}
	now := time.Now()
	for _, call := range st.calls {
		snap.ActiveTools = append(snap.ActiveTools, ActiveToolInfo{StopID: call.stopID, CallID: call.callID, Name: call.name,
			StartedAt: call.started, ElapsedMs: now.Sub(call.started).Milliseconds(), StopRequested: call.stopped})
	}
	sort.Slice(snap.ActiveTools, func(i, j int) bool { return snap.ActiveTools[i].StopID < snap.ActiveTools[j].StopID })
	if snap.Phase == "" {
		snap.Phase = "idle"
	}
	if snap.UsageMode == "" {
		snap.UsageMode = "unavailable"
	}
	return snap
}

// progressUsage reconciles cumulative usage frames within one Stream request.
// A terminal frame replaces that request's prior frame rather than adding it.
// Each retry gets its own recorder; usage not reported by a provider is unknown.
func (a *Agent) progressUsage(source string) func(*provider.Usage, bool) {
	a.progressMu.Lock()
	epoch := a.progress.epoch
	a.progress.usageMode = "awaiting_usage"
	a.progressMu.Unlock()
	var input, output int64
	seen := false
	providerName := ""
	if a.prov != nil {
		providerName = a.prov.Name()
	}
	return func(usage *provider.Usage, final bool) {
		a.progressMu.Lock()
		defer a.progressMu.Unlock()
		st := &a.progress
		if st.epoch != epoch {
			return
		}
		if usage != nil {
			nextInput, nextOutput := int64(usage.PromptTokens), int64(usage.CompletionTokens)
			di, do := nextInput-input, nextOutput-output
			st.input += di
			st.output += do
			input, output = nextInput, nextOutput
			st.usageSeen = true
			st.usageSource, st.usageProvider = source, providerName
			now := time.Now()
			if !seen || di != 0 || do != 0 {
				st.usageAt = now
				st.markProgressEvent(now)
			}
			if di > 0 || do > 0 {
				st.tokenGrowthAt = now
			}
			seen = true
		}
		mode := "awaiting_usage"
		if seen {
			mode = "stream_reported"
		}
		if final {
			if seen {
				mode = "response_complete"
			} else {
				mode = "unavailable"
			}
		}
		st.usageMode = mode
	}
}

func (a *Agent) recordProgressUsage(usage *provider.Usage, source string) {
	if usage != nil {
		a.progressUsage(source)(usage, true)
	}
}
func (a *Agent) recordProgress() {
	a.progressMu.Lock()
	a.progress.markProgressEvent(time.Now())
	a.progressMu.Unlock()
}
func (a *Agent) setProgressPhase(phase string) {
	a.progressMu.Lock()
	defer a.progressMu.Unlock()
	if a.progress.phase != phase {
		a.progress.phase = phase
		a.progress.markProgressEvent(time.Now())
	}
}

var errToolStoppedByUser = errors.New("tool stopped by user")

const stoppedResultPrefix = "Tool result: user manually stopped this tool call ("

// IsStoppedResult recognizes the persisted receipt before display archiving.
func IsStoppedResult(output string) bool { return strings.HasPrefix(output, stoppedResultPrefix) }

func stoppedToolMessage(name, partial string) string {
	text := stoppedResultPrefix + name + "). The result is unconfirmed; partial side effects may remain. Do not automatically retry the identical command or assume success/rollback. The turn remains active: inspect the current state and continue toward the user's goal using an alternative approach."
	if partial != "" {
		text += "\nPartial output before the stop:\n" + strings.TrimRight(partial, "\n")
	}
	return text
}
