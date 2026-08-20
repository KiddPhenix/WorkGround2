package runhub

import "context"

// Runner is the adapter contract for driving one external-agent family (DSH,
// Codex, Claude). Implementations live outside the core and are registered with
// the Hub by Boot/Desktop; the core state machine never imports them.
type Runner interface {
	// Probe checks the adapter's filesystem, version and capability surface
	// without starting a process or touching the network.
	Probe(context.Context, Profile) (Capabilities, error)
	// Start launches one run and reports its lifecycle into sink.
	Start(context.Context, LaunchRequest, EventSink) (RunnerBinding, error)
	// Cancel stops a bound run by whatever precise mechanism the adapter owns.
	Cancel(context.Context, RunnerBinding) error
	// Open reveals the run in the adapter's own UI, when supported.
	Open(context.Context, RunnerBinding) error
	// Recover returns the best-known observation of a previously bound run.
	Recover(context.Context, RunnerBinding) (Observation, error)
}

// Profile selects and configures a runner. It is intentionally a small,
// explicit value so callers pass only what the runner needs.
type Profile struct {
	ID        string            `json:"id"`
	Workspace string            `json:"workspace,omitempty"`
	Config    map[string]string `json:"config,omitempty"`
}

// LaunchRequest is the concrete start instruction handed to a Runner.
type LaunchRequest struct {
	LaunchIntent
}

// EventSink is the transport-agnostic channel a Runner uses to hand normalized
// events back to the Hub. The Hub implements it directly.
type EventSink interface {
	Report(RunEvent) (Receipt, AgentRun)
}

// Observation is a point-in-time view returned by Recover.
type Observation struct {
	Binding  RunnerBinding `json:"binding"`
	State    RunState      `json:"state"`
	Activity Activity      `json:"activity,omitempty"`
	Summary  string        `json:"summary,omitempty"`
}
