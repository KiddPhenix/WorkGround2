package control

import (
	"workground2/internal/agent"
)

// ToolCallControl is the opt-in controller port for stopping one in-flight
// tool call without cancelling the whole turn, and for reading the session's
// cheap progress snapshot (cumulative token counters, phase, active tools).
//
// It stays a separate optional port (like TaskMemoryStatus) so lean SessionAPI
// test doubles and leaner frontends do not gain a mandatory method; the desktop
// app type-asserts to it when wiring the per-tool stop button and the
// `status` JSON `progress` object.
type ToolCallControl interface {
	// StopToolCall asks the session's executor to cancel exactly the tool call
	// with the runtime-issued stop ID. Outcomes: accepted (cancelling), already_stopped
	// (idempotent repeat), finished (the call already completed or is unknown).
	// The whole turn and sibling calls keep running.
	StopToolCall(stopID string) agent.ToolStopResult
	// ProgressSnapshot returns a lightweight, thread-safe copy of the session's
	// progress state. It never waits on the model or on a running tool.
	ProgressSnapshot() agent.ProgressSnapshot
}

// Compile-time proof that the concrete controller implements the optional port.
var _ ToolCallControl = (*Controller)(nil)

// StopToolCall implements ToolCallControl. A controller without an executor
// (still booting, or a test double) has nothing to stop and reports finished.
func (c *Controller) StopToolCall(stopID string) agent.ToolStopResult {
	if c.executor == nil {
		return agent.ToolStopResult{StopID: stopID, Status: agent.ToolStopFinished}
	}
	return c.executor.StopToolCall(stopID)
}

// ProgressSnapshot implements ToolCallControl.
func (c *Controller) ProgressSnapshot() agent.ProgressSnapshot {
	if c.executor == nil {
		return agent.ProgressSnapshot{Phase: "idle", CountsScope: agent.ProgressCountsScope, UsageMode: "unavailable"}
	}
	return c.executor.ProgressSnapshot()
}
