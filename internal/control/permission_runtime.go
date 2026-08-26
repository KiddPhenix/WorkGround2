package control

import (
	"context"
	"encoding/json"
	"sync"

	"workground2/internal/permission"
)

// runtimePermissionGate keeps the agent-facing gate identity stable while its
// policy and approver are replaced at runtime.
type runtimePermissionGate struct {
	mu   sync.RWMutex
	gate *permission.Gate
}

func newRuntimePermissionGate(gate *permission.Gate) *runtimePermissionGate {
	return &runtimePermissionGate{gate: gate}
}

func (g *runtimePermissionGate) update(gate *permission.Gate) {
	g.mu.Lock()
	g.gate = gate
	g.mu.Unlock()
}

func (g *runtimePermissionGate) Check(ctx context.Context, tool string, args json.RawMessage, readOnly bool) (bool, string, error) {
	g.mu.RLock()
	gate := g.gate
	g.mu.RUnlock()
	if gate == nil {
		return true, "", nil
	}
	return gate.Check(ctx, tool, args, readOnly)
}

func (g *runtimePermissionGate) CheckSubject(ctx context.Context, tool, subject string, args json.RawMessage, readOnly bool) (bool, string, error) {
	g.mu.RLock()
	gate := g.gate
	g.mu.RUnlock()
	if gate == nil {
		return true, "", nil
	}
	return gate.CheckSubject(ctx, tool, subject, args, readOnly)
}

func clonePermissionPolicy(policy permission.Policy) permission.Policy {
	policy.Allow = append([]permission.Rule(nil), policy.Allow...)
	policy.Ask = append([]permission.Rule(nil), policy.Ask...)
	policy.Deny = append([]permission.Rule(nil), policy.Deny...)
	return policy
}

func (c *Controller) permissionPolicy() permission.Policy {
	c.mu.Lock()
	defer c.mu.Unlock()
	return clonePermissionPolicy(c.policy)
}
