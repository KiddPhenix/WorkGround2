package control

import (
	"context"
	"strings"
	"testing"
)

type readOnlyGateRunner struct {
	values []bool
}

func (r *readOnlyGateRunner) Run(context.Context, string) error { return nil }

func (r *readOnlyGateRunner) SetPlanMode(value bool) {
	r.values = append(r.values, value)
}

func TestRuntimeReadOnlyDoesNotEnterPlanWorkflow(t *testing.T) {
	runner := &readOnlyGateRunner{}
	c := New(Options{Runner: runner})

	c.SetRuntimeReadOnly(true)
	if !c.RuntimeReadOnly() || c.PlanMode() {
		t.Fatalf("runtime read-only = %v, plan mode = %v", c.RuntimeReadOnly(), c.PlanMode())
	}
	if got := c.Compose("回答问题"); strings.HasPrefix(got, PlanModeMarker) {
		t.Fatalf("runtime read-only must not add plan marker: %q", got)
	}

	// Either policy keeps the shared executor gate closed. Clearing one must
	// not weaken the other, and clearing both finally reopens it.
	c.SetPlanMode(true)
	c.SetRuntimeReadOnly(false)
	if !c.PlanMode() || c.RuntimeReadOnly() || len(runner.values) == 0 || !runner.values[len(runner.values)-1] {
		t.Fatalf("plan mode must retain read-only gate: plan=%v runtime=%v gates=%v", c.PlanMode(), c.RuntimeReadOnly(), runner.values)
	}
	c.SetPlanMode(false)
	if runner.values[len(runner.values)-1] {
		t.Fatalf("read-only gate was not reopened: %v", runner.values)
	}
}
