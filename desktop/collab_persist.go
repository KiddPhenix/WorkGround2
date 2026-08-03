package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"workground2/internal/collab"
	"workground2/internal/fileutil"
)

func (c *desktopCollaboration) recoverInterruptedRunsLocked(conn *collaborationConnection) {
	remaining := c.recoveredRuns[:0]
	for _, run := range c.recoveredRuns {
		if run.Room != conn.room || run.MemberID != conn.memberID || run.AgentID != conn.agentID || run.SessionID != conn.sessionID {
			remaining = append(remaining, run)
			continue
		}
		requestID := run.CommandID + ":recovered:interrupted"
		if outboxContains(c.outbox, requestID) {
			continue
		}
		c.outbox = append(c.outbox, collab.CommandEnvelope{
			RequestID: requestID, Room: conn.room, MemberID: conn.memberID, QueuedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Command: collab.Command{Type: collab.CommandPublishAgentRun, AgentRun: &collab.PublishAgentRunInput{
				RunID: run.RunID, AgentID: conn.agentID, CommandID: run.CommandID,
				RequestRef: run.AgentRequestID, Instruction: sanitizeCollaborationText(run.Instruction),
				ReferenceIDs: append([]string(nil), run.ReferenceIDs...), Status: collab.RunInterrupted,
				Error: "Desktop restarted before this Agent run completed.",
			}},
		})
	}
	c.recoveredRuns = remaining
	if len(remaining) > 0 {
		c.state.LastError = "unfinished Agent runs belong to another collaboration Room and were not published"
		c.state.Retryable = true
	}
}

func (c *desktopCollaboration) resumeSession(host string, port int, room, memberID, sessionID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if strings.EqualFold(strings.TrimSpace(c.state.Host), strings.TrimSpace(host)) &&
		c.state.Port == port && c.state.Room == strings.TrimSpace(room) &&
		c.state.MemberID == memberID && c.state.SessionID == strings.TrimSpace(sessionID) && c.conn != nil {
		return c.conn.connectionSession
	}
	p := c.readPersisted()
	if strings.EqualFold(strings.TrimSpace(p.Host), strings.TrimSpace(host)) && p.Port == port &&
		p.Room == strings.TrimSpace(room) && p.MemberID == memberID && p.SessionID == strings.TrimSpace(sessionID) {
		if p.ConnectionSecretRef != "" {
			return c.getSecret(p.ConnectionSecretRef)
		}
	}
	return ""
}

func (c *desktopCollaboration) loadPersisted() {
	if strings.TrimSpace(c.persistPath) == "" {
		return
	}
	data, err := os.ReadFile(c.persistPath)
	migrated := false
	if os.IsNotExist(err) && strings.TrimSpace(c.legacyPersistPath) != "" {
		data, err = os.ReadFile(c.legacyPersistPath)
		migrated = err == nil
	}
	if err != nil {
		if !os.IsNotExist(err) {
			c.state.LastError = "load collaboration state: " + err.Error()
			c.state.Retryable = true
		}
		return
	}
	var p collaborationPersistedState
	if err := json.Unmarshal(data, &p); err != nil {
		c.state.LastError = "load collaboration state: " + err.Error()
		c.state.Retryable = true
		return
	}
	if c.ownerSessionID != "" && strings.TrimSpace(p.SessionID) != c.ownerSessionID {
		if migrated {
			return
		}
		c.state.LastError = "load collaboration state: persisted sessionId does not match runtime"
		c.state.Retryable = false
		return
	}
	if p.Starts != nil {
		c.starts = p.Starts
	}
	if p.OutboxFailures != nil {
		c.outboxFailures = p.OutboxFailures
	}
	c.outbox = append([]collab.CommandEnvelope(nil), p.Outbox...)
	for i := range c.outbox {
		if c.outbox[i].QueuedAt == "" {
			c.outbox[i].QueuedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
	c.recoveredRuns = append([]collaborationPersistedRun(nil), p.Runs...)
	snapshot := p.Snapshot
	if snapshot.LatestSequence == 0 {
		snapshot.LatestSequence = p.AfterSequence
	}
	c.state = CollaborationState{
		Status: "disconnected", Mode: p.Mode, Host: p.Host, Port: p.Port, Room: p.Room,
		MemberID: p.MemberID, AgentID: p.AgentID, SessionID: p.SessionID,
		Snapshot:    snapshot,
		OutboxCount: len(c.outbox),
	}
	if p.Mode != "" && p.Host != "" && p.Room != "" && p.SessionID != "" {
		c.state.Status = "failed"
		c.state.Retryable = true
	}
	if migrated {
		c.persistLocked()
	}
}

func (c *desktopCollaboration) readPersisted() collaborationPersistedState {
	if strings.TrimSpace(c.persistPath) == "" {
		return collaborationPersistedState{}
	}
	data, err := os.ReadFile(c.persistPath)
	if err != nil {
		return collaborationPersistedState{}
	}
	var value collaborationPersistedState
	if json.Unmarshal(data, &value) != nil {
		return collaborationPersistedState{}
	}
	return value
}

func (c *desktopCollaboration) persistLocked() {
	if strings.TrimSpace(c.persistPath) == "" {
		return
	}
	value := collaborationPersistedState{
		Mode:           c.state.Mode,
		Host:           c.state.Host,
		Port:           c.state.Port,
		Room:           c.state.Room,
		MemberID:       c.state.MemberID,
		AgentID:        c.state.AgentID,
		SessionID:      c.state.SessionID,
		AfterSequence:  c.state.Snapshot.LatestSequence,
		Snapshot:       cloneCollaborationState(CollaborationState{Snapshot: c.state.Snapshot}).Snapshot,
		Outbox:         persistedCollaborationOutbox(c.outbox),
		OutboxFailures: cloneStringMap(c.outboxFailures),
		Starts:         cloneStartMap(c.starts),
		Runs:           c.persistedRunsLocked(),
	}
	if c.conn != nil && c.conn.connectionSession != "" {
		value.RoomName, value.Description = c.conn.roomName, c.conn.description
		value.MemberName, value.MemberRole = c.conn.memberName, c.conn.memberRole
		value.AgentName, value.AgentRole = c.conn.agentName, c.conn.agentRole
		value.ConnectionSecretRef = collaborationSecretRef(c.state.Host, c.state.Port, c.state.Room, c.state.MemberID)
		if c.getSecret(value.ConnectionSecretRef) != c.conn.connectionSession {
			if err := c.setSecret(value.ConnectionSecretRef, c.conn.connectionSession); err != nil {
				c.state.LastError = "save collaboration credential: " + err.Error()
				c.state.Retryable = true
				return
			}
		}
		if c.conn.joinToken != "" {
			value.JoinTokenSecretRef = collaborationTokenRef(c.state.Host, c.state.Port, c.state.Room, c.state.MemberID)
			if c.getSecret(value.JoinTokenSecretRef) != c.conn.joinToken {
				if err := c.setSecret(value.JoinTokenSecretRef, c.conn.joinToken); err != nil {
					c.state.LastError = "save collaboration token: " + err.Error()
					c.state.Retryable = true
					return
				}
			}
		}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		c.state.LastError = "encode collaboration state: " + err.Error()
		c.state.Retryable = true
		return
	}
	write := c.writeState
	if write == nil {
		write = fileutil.AtomicWriteFile
	}
	if err := write(c.persistPath, data, 0o600); err != nil {
		c.state.LastError = "save collaboration state: " + err.Error()
		c.state.Retryable = true
	}
}

// persistedCollaborationOutbox preserves idempotency keys and command payloads,
// but never writes a live connection credential into the JSON recovery file.
func persistedCollaborationOutbox(input []collab.CommandEnvelope) []collab.CommandEnvelope {
	if len(input) == 0 {
		return nil
	}
	result := append([]collab.CommandEnvelope(nil), input...)
	for i := range result {
		result[i].Session = ""
	}
	return result
}

func (c *desktopCollaboration) persistedRunsLocked() []collaborationPersistedRun {
	result := make([]collaborationPersistedRun, 0, len(c.runs)+len(c.recoveredRuns))
	result = append(result, c.recoveredRuns...)
	for _, run := range c.runs {
		if run == nil {
			continue
		}
		result = append(result, collaborationPersistedRun{
			Room: run.Room, MemberID: run.MemberID, AgentID: run.AgentID,
			RunID: run.RunID, CommandID: run.CommandID, SessionID: run.SessionID,
			AgentRequestID: run.AgentRequestID, Instruction: run.Instruction,
			ReferenceIDs: append([]string(nil), run.ReferenceIDs...),
		})
	}
	return result
}

func collaborationSecretRef(host string, port int, room, memberID string) string {
	value := strings.ToLower(strings.TrimSpace(host)) + "\x00" + strconv.Itoa(port) + "\x00" + strings.TrimSpace(room) + "\x00" + strings.TrimSpace(memberID)
	sum := sha256.Sum256([]byte(value))
	return "WORKGROUND2_COLLAB_SESSION_" + strings.ToUpper(hex.EncodeToString(sum[:12]))
}

func collaborationTokenRef(host string, port int, room, memberID string) string {
	return collaborationSecretRef(host, port, room, memberID) + "_TOKEN"
}

func cloneStartMap(input map[string]collaborationStartRecord) map[string]collaborationStartRecord {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]collaborationStartRecord, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
