package control

import (
	"context"
	"encoding/json"
	"fmt"

	"workground2/internal/agent"
	"workground2/internal/workgate"
)

// workGate returns the persistent work gate installed on this Controller, or
// nil when the session is unfenced (pure CLI sessions without an Assistant
// Store). It is read under c.mu so SetWorkGate swaps are safe.
func (c *Controller) workGate() workgate.Gate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gate
}

// workGateLocked reads c.gate while the caller already holds c.mu.
func (c *Controller) workGateLocked() workgate.Gate {
	return c.gate
}

// checkWorkGate fences a model turn before the executor starts. It returns the
// current gate epoch when work is allowed; a non-nil error refuses the turn. A
// nil gate (epoch 0) allows all work. New model turns are refused during
// QUIESCING, PAUSED, and RECOVERING — recovery-driven turns use checkResumeGate.
func (c *Controller) checkWorkGate() (int64, error) {
	g := c.workGate()
	if g == nil {
		return 0, nil
	}
	epoch := g.Epoch()
	if !g.Allowed() {
		return epoch, fmt.Errorf("work paused (epoch %d)", epoch)
	}
	return epoch, nil
}

// checkResumeGate fences a recovery-driven turn (ContinueTurn). It admits the
// RECOVERING state: resuming an interrupted round is how the host re-drives
// checkpointed work after resume_all, so it must not be refused like a new turn.
func (c *Controller) checkResumeGate() (int64, error) {
	g := c.workGate()
	if g == nil {
		return 0, nil
	}
	epoch := g.Epoch()
	if !g.AllowedResume() {
		return epoch, fmt.Errorf("work paused (epoch %d)", epoch)
	}
	return epoch, nil
}

// workEpoch returns the current gate epoch, or 0 when unfenced.
func (c *Controller) workEpoch() int64 {
	g := c.workGate()
	if g == nil {
		return 0
	}
	return g.Epoch()
}

// staleWorkError reports a turn result that finished under an older work
// generation — the pause/resume fence moved while it was in flight.
func staleWorkError(epoch int64) error {
	return fmt.Errorf("work epoch changed during turn (epoch %d); late result rejected", epoch)
}

// beginTurnEpoch captures the work-gate epoch the current turn started under
// (0 when unfenced) so persist writes during the turn can be refused once the
// fence moves. Must be called with c.mu held.
func (c *Controller) beginTurnEpochLocked(epoch int64) {
	c.turnEpoch = epoch
}

// clearTurnEpoch releases the turn fence once the turn's persist writes are
// done. Must be called with c.mu held.
func (c *Controller) clearTurnEpochLocked() {
	c.turnEpoch = 0
}

// clearTurnEpoch releases the turn fence (unlocked wrapper for the deferred
// synchronous turn paths).
func (c *Controller) clearTurnEpoch() {
	c.mu.Lock()
	c.clearTurnEpochLocked()
	c.mu.Unlock()
}

// checkTurnWriteGate refuses a persist write (transcript snapshot, branch meta,
// receipt) when the shared work fence moved while the turn was in flight or the
// gate is no longer accepting work. Control commands and unfenced sessions pass
// through unchanged (turnEpoch == 0).
func (c *Controller) checkTurnWriteGate() error {
	c.mu.Lock()
	turnEpoch := c.turnEpoch
	c.mu.Unlock()
	if turnEpoch == 0 {
		return nil
	}
	g := c.workGate()
	if g == nil {
		return nil
	}
	if cur := g.Epoch(); cur != turnEpoch {
		return staleWorkError(cur)
	}
	if !g.AllowedResume() {
		return fmt.Errorf("work paused (epoch %d); persist write refused", g.Epoch())
	}
	return nil
}

// executorWorkGate wraps the permission gate installed on the executor so every
// tool call first consults the shared persistent work gate. It is the
// Controller-side choke point for tool execution: the agent's executeOne checks
// its installed agent.Gate before running any call, so a paused gate turns the
// tool result into an explicit "work paused (epoch N)" error without panicking
// or silently dropping the call.
type executorWorkGate struct {
	inner agent.Gate
	c     *Controller
}

func (g *executorWorkGate) workAllowed() (bool, int64) {
	wg := g.c.workGate()
	if wg == nil {
		return true, 0
	}
	return wg.Allowed(), wg.Epoch()
}

func (g *executorWorkGate) Check(ctx context.Context, tool string, args json.RawMessage, readOnly bool) (bool, string, error) {
	if ok, epoch := g.workAllowed(); !ok {
		return false, fmt.Sprintf("work paused (epoch %d)", epoch), nil
	}
	if g.inner == nil {
		return true, "", nil
	}
	return g.inner.Check(ctx, tool, args, readOnly)
}

func (g *executorWorkGate) CheckSubject(ctx context.Context, tool, subject string, args json.RawMessage, readOnly bool) (bool, string, error) {
	if ok, epoch := g.workAllowed(); !ok {
		return false, fmt.Sprintf("work paused (epoch %d)", epoch), nil
	}
	if g.inner == nil {
		return true, "", nil
	}
	if sg, ok := g.inner.(agent.SubjectGate); ok {
		return sg.CheckSubject(ctx, tool, subject, args, readOnly)
	}
	return g.inner.Check(ctx, tool, args, readOnly)
}
