package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
		if p.ConnectionSecretRef != "" && c.getSecret != nil {
			return c.getSecret(p.ConnectionSecretRef)
		}
	}
	return ""
}

// persistReadMaxRetries is the maximum number of additional read attempts
// when a persist file appears truncated (empty or unexpected EOF).
var persistReadMaxRetries = 3

// persistReadRetryInterval is the wait between retry reads.
var persistReadRetryInterval = 5 * time.Millisecond

// readPersistFile reads a collaboration persist file and unmarshals it into v.
// It retries a bounded number of times when the file is empty or ends
// unexpectedly — symptoms of a concurrent writer that briefly truncated the
// file before the writer-side atomic fix. Transient read failures are retried
// as well because Windows may briefly deny a read during atomic replacement.
// Stable parse errors and missing files are returned immediately without retry.
func readPersistFile(path string, v interface{}) error {
	for attempt := 0; ; attempt++ {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) && attempt < persistReadMaxRetries {
				time.Sleep(persistReadRetryInterval)
				continue
			}
			return err
		}
		if len(data) == 0 {
			if attempt < persistReadMaxRetries {
				time.Sleep(persistReadRetryInterval)
				continue
			}
			return fmt.Errorf("persist file %s is empty after %d reads", path, persistReadMaxRetries+1)
		}
		// Validate before decoding into v. A truncated decode may partially
		// mutate the destination, and a later successful retry would otherwise
		// inherit fields that are absent from the complete document.
		if !json.Valid(data) {
			var raw json.RawMessage
			err := json.Unmarshal(data, &raw)
			if isTransientJSONError(err) && attempt < persistReadMaxRetries {
				time.Sleep(persistReadRetryInterval)
				continue
			}
			return err
		}
		if err := json.Unmarshal(data, v); err != nil {
			return err
		}
		return nil
	}
}

// isTransientJSONError reports whether err is a truncated-JSON error — empty
// input or unexpected EOF — that may resolve on a re-read once the writer
// completes. Stable syntax or type errors are not transient.
func isTransientJSONError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return strings.Contains(syntaxErr.Error(), "unexpected end of JSON input")
	}
	return false
}

func (c *desktopCollaboration) loadPersisted() {
	if strings.TrimSpace(c.persistPath) == "" {
		return
	}
	var p collaborationPersistedState
	err := readPersistFile(c.persistPath, &p)
	adoptedRecovery := false
	if os.IsNotExist(err) {
		var adoptErr error
		adoptedRecovery, adoptErr = c.adoptRecoveryV2Cache()
		if adoptErr != nil {
			c.state.LastError = "load collaboration state: " + adoptErr.Error()
			c.state.Retryable = true
			return
		}
		if adoptedRecovery {
			err = readPersistFile(c.persistPath, &p)
		}
	}
	adoptedV2 := false
	if os.IsNotExist(err) {
		var adoptErr error
		adoptedV2, adoptErr = c.adoptLegacyV2Cache()
		if adoptErr != nil {
			c.state.LastError = "load collaboration state: " + adoptErr.Error()
			c.state.Retryable = true
			return
		}
		if adoptedV2 {
			err = readPersistFile(c.persistPath, &p)
		}
	}
	migrated := false
	if os.IsNotExist(err) && strings.TrimSpace(c.legacyPersistPath) != "" {
		err = readPersistFile(c.legacyPersistPath, &p)
		migrated = err == nil
	}
	if err != nil {
		if !os.IsNotExist(err) {
			c.state.LastError = "load collaboration state: " + err.Error()
			c.state.Retryable = true
		}
		return
	}
	persistedSessionID := strings.TrimSpace(p.SessionID)
	p = c.repairPersisted(p)
	identityChanged := false
	if c.ownerSessionPath != "" {
		persistedPath := sessionRuntimeKey(p.SessionPath)
		persistedOwnerPath := collaborationOwnerSessionPath(persistedPath)
		if persistedPath != "" && persistedOwnerPath != c.ownerSessionPath {
			c.state.LastError = "load collaboration state: cached Room belongs to another session path"
			c.state.Retryable = true
			return
		}
		identityChanged = persistedPath != c.ownerSessionPath
		p.SessionPath = c.ownerSessionPath
	}
	if c.ownerSessionID != "" && persistedSessionID != c.ownerSessionID {
		if migrated && persistedSessionID != "" {
			// Legacy file belongs to a different session — skip
			// migration to prevent cross-session data contamination.
			return
		}
		// SessionPath is the durable Room owner. SessionID can rotate when
		// its tab runtime is rebuilt, so repair a stale value and persist the
		// current routing identity without rejecting the cached Room.
		identityChanged = true
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
	queue := p.Queue
	queueTruncated := false
	if len(queue) > maxCollaborationAgentQueue {
		queue = queue[:maxCollaborationAgentQueue]
		queueTruncated = true
	}
	for _, value := range queue {
		queuedAt := value.QueuedAt
		if queuedAt == "" {
			queuedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		c.queuedRuns = append(c.queuedRuns, &collaborationAgentRun{
			Room: value.Room, MemberID: value.MemberID, AgentID: value.AgentID,
			RunID: value.RunID, CommandID: value.CommandID, SessionID: value.SessionID,
			AgentRequestID: value.AgentRequestID, Instruction: value.Instruction,
			ReferenceIDs: append([]string(nil), value.ReferenceIDs...), ContextRefs: append([]string(nil), value.ContextRefs...), QueuedAt: queuedAt,
			PublishIndex: value.PublishIndex,
			Automatic:    value.Automatic,
			ReadOnly:     value.ReadOnly,
			Updates:      make(chan collaborationRunUpdate, 32),
		})
	}
	for _, share := range p.Shares {
		c.shares[share.FileID] = share
	}
	for i := range p.Transfers {
		transfer := p.Transfers[i]
		if transfer.Direction != "receive" {
			continue
		}
		if transfer.PartPath == "" && transfer.Destination != "" {
			transfer.PartPath = transfer.Destination + ".wg2part"
		}
		if transfer.Status == "downloading" || transfer.Status == "negotiating" || transfer.Status == "verifying" {
			if transfer.Automatic && !transfer.PausedByUser {
				transfer.Status = "waiting_sender"
				transfer.Error = "应用已重启，连接后将自动继续"
			} else {
				transfer.Status = "paused"
				transfer.Error = "应用已重启，可继续接收"
			}
			transfer.Retryable = true
		}
		if c.transfers == nil {
			c.transfers = map[string]*CollaborationFileTransfer{}
		}
		if previous := c.transfers[transfer.FileID]; previous != nil {
			if c.transferArchive == nil {
				c.transferArchive = map[string]*CollaborationFileTransfer{}
			}
			c.transferArchive[collaborationTransferArchiveKey(previous.RoomInstance, previous.FileID)] = previous
		}
		c.transfers[transfer.FileID] = &transfer
	}
	c.recovery = p
	snapshot := p.Snapshot
	if snapshot.LatestSequence == 0 {
		snapshot.LatestSequence = p.AfterSequence
	}
	c.state = CollaborationState{
		Status: "disconnected", Mode: p.Mode, Host: p.Host, Port: p.Port, Room: p.Room,
		MemberID: p.MemberID, AgentID: p.AgentID, SessionID: p.SessionID,
		Snapshot:      snapshot,
		OutboxCount:   len(c.outbox),
		AgentConfig:   normalizeCollaborationAgentConfig(p.AgentConfig, p.AgentName),
		Routes:        append([]CollaborationRouteState(nil), p.Routes...),
		Advertisement: p.Advertisement,
	}
	c.rebuildFileOffersLocked(c.state.Snapshot)
	if queueTruncated {
		c.state.LastError = "collaboration Agent queue exceeded 20 tasks and was truncated during recovery"
		c.state.Retryable = false
	}
	if p.Mode != "" && p.Host != "" && p.Room != "" && p.SessionID != "" {
		c.state.Status = "failed"
		c.state.Retryable = true
	}
	if migrated || adoptedRecovery || adoptedV2 || identityChanged {
		c.persistLocked()
	}
}

// adoptRecoveryV2Cache migrates a cache written before recovery branches were
// canonicalised to their original logical Session path. Candidates already
// prove the same owner through branch metadata, so choosing the newest complete
// Room record is deterministic and cannot attach an unrelated Room by title.
func (c *desktopCollaboration) adoptRecoveryV2Cache() (bool, error) {
	if c.ownerSessionPath == "" {
		return false, nil
	}
	dir := filepath.Dir(c.persistPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	targetName := filepath.Base(c.persistPath)
	type candidate struct {
		path    string
		modTime time.Time
	}
	var best candidate
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == targetName || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		var value collaborationPersistedState
		if readPersistFile(path, &value) != nil || !hasPersistedRoom(value) {
			continue
		}
		persistedPath := sessionRuntimeKey(value.SessionPath)
		if persistedPath == "" || persistedPath == c.ownerSessionPath || collaborationOwnerSessionPath(persistedPath) != c.ownerSessionPath {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			continue
		}
		if best.path == "" || info.ModTime().After(best.modTime) {
			best = candidate{path: path, modTime: info.ModTime()}
		}
	}
	if best.path == "" {
		return false, nil
	}
	if err := os.Rename(best.path, c.persistPath); err != nil {
		if _, statErr := os.Stat(c.persistPath); statErr == nil {
			return true, nil
		}
		return false, fmt.Errorf("migrate recovered Room cache: %w", err)
	}
	return true, nil
}

// adoptLegacyV2Cache moves the one old SessionID-keyed cache that can be
// unambiguously associated with the current collaboration Session to its
// stable SessionPath-keyed location. A title collision is surfaced instead of
// guessing, so opening one old Room can never attach another Room's state.
func (c *desktopCollaboration) adoptLegacyV2Cache() (bool, error) {
	if c.ownerSessionPath == "" || strings.TrimSpace(c.ownerSessionTitle) == "" {
		return false, nil
	}
	dir := filepath.Dir(c.persistPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	targetName := filepath.Base(c.persistPath)
	type candidate struct {
		path string
	}
	candidates := make([]candidate, 0, 1)
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == targetName || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var value collaborationPersistedState
		if json.Unmarshal(data, &value) != nil || sessionRuntimeKey(value.SessionPath) != "" || !hasPersistedRoom(value) {
			continue
		}
		if !strings.EqualFold(persistedRoomTitle(value), strings.TrimSpace(c.ownerSessionTitle)) {
			continue
		}
		candidates = append(candidates, candidate{path: path})
	}
	if len(candidates) == 0 {
		return false, nil
	}
	if len(candidates) > 1 {
		return false, fmt.Errorf("multiple old Room caches match session %q; reconnect once to bind this session safely", c.ownerSessionTitle)
	}
	if err := os.Rename(candidates[0].path, c.persistPath); err != nil {
		return false, fmt.Errorf("migrate old Room cache: %w", err)
	}
	return true, nil
}

func hasPersistedRoom(value collaborationPersistedState) bool {
	return strings.TrimSpace(value.Room) != "" || strings.TrimSpace(value.Snapshot.Room.ID) != ""
}

func persistedRoomTitle(value collaborationPersistedState) string {
	for _, title := range []string{value.RoomName, value.Snapshot.Room.Name} {
		if title = strings.TrimSpace(title); title != "" {
			return title
		}
	}
	return ""
}

func (c *desktopCollaboration) readPersisted() collaborationPersistedState {
	if strings.TrimSpace(c.persistPath) == "" {
		return collaborationPersistedState{}
	}
	var value collaborationPersistedState
	if readPersistFile(c.persistPath, &value) != nil {
		return collaborationPersistedState{}
	}
	return value
}

func (c *desktopCollaboration) persistLocked() {
	if strings.TrimSpace(c.persistPath) == "" {
		return
	}
	value := c.recovery
	if strings.TrimSpace(c.state.Room) == "" {
		value = collaborationPersistedState{}
	}
	value.Mode = c.state.Mode
	value.Host = c.state.Host
	value.Port = c.state.Port
	value.Room = c.state.Room
	value.MemberID = c.state.MemberID
	value.AgentID = c.state.AgentID
	value.SessionID = c.state.SessionID
	value.SessionPath = c.ownerSessionPath
	value.AfterSequence = c.state.Snapshot.LatestSequence
	value.Snapshot = cloneCollaborationState(CollaborationState{Snapshot: c.state.Snapshot}).Snapshot
	value.Outbox = persistedCollaborationOutbox(c.outbox)
	value.OutboxFailures = cloneStringMap(c.outboxFailures)
	value.Starts = cloneStartMap(c.starts)
	value.Runs = c.persistedRunsLocked()
	value.Queue = c.persistedQueueLocked()
	value.Shares = make([]collaborationSharedFile, 0, len(c.shares))
	for _, share := range c.shares {
		value.Shares = append(value.Shares, share)
	}
	value.Transfers = c.persistedFileTransfersLocked()
	value.AgentConfig = normalizeCollaborationAgentConfig(c.state.AgentConfig, value.AgentName)
	value.Routes = publicCollaborationRoutes(c.state.Routes)
	value.Advertisement = c.state.Advertisement
	value = c.repairPersisted(value)
	if c.conn != nil && c.conn.connectionSession != "" {
		value.RoomName, value.Description = c.conn.roomName, c.conn.description
		value.MemberName, value.MemberAvatar, value.MemberRole = c.conn.memberName, c.conn.memberAvatar, c.conn.memberRole
		value.AgentName, value.AgentAvatar, value.AgentRole = c.conn.agentName, c.conn.agentAvatar, c.conn.agentRole
		value.LANEnabled = c.conn.lanEnabled
		value.ReachabilityVersion = 1
		value.RelayIDs = append([]string(nil), c.conn.relayIDs...)
		value.PreferLAN = c.conn.preferLAN
		value.AuthorityKeySecretRef = c.conn.authorityKeyRef
		value.HostCapabilityRefs = cloneStringMap(c.conn.hostCapabilityRefs)
		value.GuestCapabilityRefs = cloneStringMap(c.conn.guestCapabilityRefs)
		value.HostKey = c.conn.hostKey
		value.ProtocolVersion = c.conn.protocolVersion
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
		return
	}
	c.recovery = value
}

func (c *desktopCollaboration) repairPersisted(value collaborationPersistedState) collaborationPersistedState {
	if value.ProtocolVersion == 0 {
		value.ProtocolVersion = collaborationProtocolV1
	}
	if value.ReachabilityVersion == 0 {
		value.LANEnabled = value.Mode == "host" && value.Port > 0
		value.PreferLAN = true
	}
	if strings.TrimSpace(value.Room) == "" {
		value.Room = strings.TrimSpace(value.Snapshot.Room.ID)
	}
	if strings.TrimSpace(value.RoomName) == "" {
		value.RoomName = strings.TrimSpace(value.Snapshot.Room.Name)
	}
	if strings.TrimSpace(value.Description) == "" {
		value.Description = strings.TrimSpace(value.Snapshot.Room.Description)
	}
	if owner := strings.TrimSpace(c.ownerSessionID); owner != "" {
		value.SessionID = owner
		for i := range value.Runs {
			value.Runs[i].SessionID = owner
		}
		for i := range value.Queue {
			value.Queue[i].SessionID = owner
		}
	}
	for _, member := range value.Snapshot.Members {
		if member.ID != value.MemberID {
			continue
		}
		if strings.TrimSpace(value.MemberName) == "" {
			value.MemberName = member.Name
		}
		if strings.TrimSpace(value.MemberAvatar) == "" {
			value.MemberAvatar = member.Avatar
		}
		if strings.TrimSpace(value.MemberRole) == "" {
			value.MemberRole = member.Role
		}
		if strings.TrimSpace(value.AgentID) == "" {
			value.AgentID = member.Agent.ID
		}
		if strings.TrimSpace(value.AgentName) == "" {
			value.AgentName = member.Agent.Name
		}
		if strings.TrimSpace(value.AgentAvatar) == "" {
			value.AgentAvatar = member.Agent.Avatar
		}
		if strings.TrimSpace(value.AgentRole) == "" {
			value.AgentRole = member.Agent.Role
		}
		break
	}
	if strings.TrimSpace(value.ConnectionSecretRef) == "" && value.Host != "" && value.Port > 0 && value.Room != "" && value.MemberID != "" {
		ref := collaborationSecretRef(value.Host, value.Port, value.Room, value.MemberID)
		if c.getSecret != nil && c.getSecret(ref) != "" {
			value.ConnectionSecretRef = ref
		}
	}
	if strings.TrimSpace(value.JoinTokenSecretRef) == "" && value.Host != "" && value.Port > 0 && value.Room != "" && value.MemberID != "" {
		ref := collaborationTokenRef(value.Host, value.Port, value.Room, value.MemberID)
		if c.getSecret != nil && c.getSecret(ref) != "" {
			value.JoinTokenSecretRef = ref
		}
	}
	value.AgentConfig = normalizeCollaborationAgentConfig(value.AgentConfig, value.AgentName)
	return value
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
			ReferenceIDs: append([]string(nil), run.ReferenceIDs...), ContextRefs: append([]string(nil), run.ContextRefs...), QueuedAt: run.QueuedAt, PublishIndex: run.PublishIndex,
			Automatic: run.Automatic, ReadOnly: run.ReadOnly,
		})
	}
	return result
}

func (c *desktopCollaboration) persistedQueueLocked() []collaborationPersistedRun {
	result := make([]collaborationPersistedRun, 0, len(c.queuedRuns))
	for _, run := range c.queuedRuns {
		if run == nil {
			continue
		}
		result = append(result, collaborationPersistedRun{
			Room: run.Room, MemberID: run.MemberID, AgentID: run.AgentID,
			RunID: run.RunID, CommandID: run.CommandID, SessionID: run.SessionID,
			AgentRequestID: run.AgentRequestID, Instruction: run.Instruction,
			ReferenceIDs: append([]string(nil), run.ReferenceIDs...), ContextRefs: append([]string(nil), run.ContextRefs...), QueuedAt: run.QueuedAt, PublishIndex: run.PublishIndex,
			Automatic: run.Automatic, ReadOnly: run.ReadOnly,
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
