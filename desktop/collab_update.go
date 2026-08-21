package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"
)

const (
	collaborationUpdateInterval      = 60 * time.Second
	collaborationUpdateJitter        = 12 * time.Second
	collaborationUpdateTimeout       = 30 * time.Second
	collaborationInitialUpdateMin    = 250 * time.Millisecond
	collaborationInitialUpdateSpread = 5 * time.Second
)

// startUpdateLoop installs the low-frequency reconciliation fallback for one
// Room runtime. The stream loop remains the fast path; this loop also covers a
// Room whose initial restore never produced a connection.
func (c *desktopCollaboration) startUpdateLoop(parent context.Context) {
	if c == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	c.updateOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		done := make(chan struct{})
		c.mu.Lock()
		c.updateCancel = cancel
		c.updateDone = done
		c.mu.Unlock()
		go c.updateLoop(ctx, done)
	})
}

func (c *desktopCollaboration) stopUpdateLoop() {
	if c == nil {
		return
	}
	c.mu.RLock()
	cancel := c.updateCancel
	c.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (c *desktopCollaboration) waitUpdateLoop() {
	if c == nil {
		return
	}
	c.mu.RLock()
	done := c.updateDone
	c.mu.RUnlock()
	if done != nil {
		<-done
	}
}

func (c *desktopCollaboration) updateLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	// Reconcile once on residency. Waiting for the first one-minute tick leaves
	// a restored Room icon backed only by its stale persisted snapshot, which
	// makes incoming unread delivery depend on a frontend-tab readiness race.
	// A small stable spread avoids reconnecting every restored Room at once.
	initial := collaborationInitialUpdateDelay(c.ownerSessionID)
	if c.initialUpdateDelay != nil {
		initial = c.initialUpdateDelay()
	}
	if initial > 0 {
		timer := time.NewTimer(initial)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
	updateCtx, cancel := context.WithTimeout(ctx, collaborationUpdateTimeout)
	_ = c.updateConnection(updateCtx)
	cancel()
	for {
		timer := time.NewTimer(c.nextUpdateDelay())
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		updateCtx, cancel := context.WithTimeout(ctx, collaborationUpdateTimeout)
		_ = c.updateConnection(updateCtx)
		cancel()
	}
}

func collaborationInitialUpdateDelay(sessionID string) time.Duration {
	sum := sha256.Sum256([]byte(sessionID))
	return collaborationInitialUpdateMin + time.Duration(binary.BigEndian.Uint64(sum[8:16])%uint64(collaborationInitialUpdateSpread))
}

func (c *desktopCollaboration) nextUpdateDelay() time.Duration {
	if c != nil && c.updateDelay != nil {
		if delay := c.updateDelay(); delay > 0 {
			return delay
		}
	}
	return collaborationRoomUpdateDelay(c.ownerSessionID)
}

// collaborationRoomUpdateDelay gives each Session a stable offset so an app
// restoring many Rooms does not make every recovery request at once.
func collaborationRoomUpdateDelay(sessionID string) time.Duration {
	sum := sha256.Sum256([]byte(sessionID))
	span := uint64(collaborationUpdateJitter*2 + 1)
	offset := time.Duration(binary.BigEndian.Uint64(sum[:8])%span) - collaborationUpdateJitter
	return collaborationUpdateInterval + offset
}

func (c *desktopCollaboration) updateConnection(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.RLock()
	conn := c.conn
	state := c.state
	c.mu.RUnlock()
	if conn != nil {
		// The stream and heartbeat loops are the healthy connection's fast path.
		// A maintenance tick must stay a pure health check: retryLocked also syncs
		// and, when conn is absent, performs a remote Join/Host. Re-entering it for
		// every healthy Room made the periodic fallback look like activation.
		if collaborationConnectionLoopRunning(conn) {
			return nil
		}
		// A stopped loop can be repaired in-place with the existing peer/session;
		// this does not issue a remote Join. Preserve terminal/manual failures.
		if state.Status == "failed" && !state.Retryable {
			return nil
		}
		c.ensureConnectionLoop(conn)
		return nil
	}
	if !state.Retryable || !completeCollaborationRecovery(state) {
		return nil
	}

	_, err := c.retryLocked(ctx, false)
	return err
}

func completeCollaborationRecovery(state CollaborationState) bool {
	return state.Mode != "" && state.Host != "" && state.Room != "" && state.SessionID != ""
}

// ensureConnectionLoop starts the stream/heartbeat pair once for the current
// connection. A failed route failover can leave a valid conn pointer behind
// after its stream loop exits; the low-frequency update uses this same helper
// to restart it without creating a second live loop.
//
// c.opMu must be held by the caller.
func (c *desktopCollaboration) ensureConnectionLoop(conn *collaborationConnection) bool {
	if c == nil || c.app == nil || conn == nil || collaborationConnectionLoopRunning(conn) {
		return false
	}
	c.mu.RLock()
	current := c.conn == conn
	c.mu.RUnlock()
	if !current {
		return false
	}
	if conn.cancel != nil {
		conn.cancel()
	}
	loopCtx, cancel := context.WithCancel(c.app.bootContext())
	conn.cancel = cancel
	conn.done = make(chan struct{})
	go c.connectionLoop(loopCtx, conn)
	return true
}

func collaborationConnectionLoopRunning(conn *collaborationConnection) bool {
	if conn == nil || conn.done == nil {
		return false
	}
	select {
	case <-conn.done:
		return false
	default:
		return true
	}
}
