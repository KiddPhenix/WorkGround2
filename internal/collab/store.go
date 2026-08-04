package collab

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const journalName = "events.jsonl"
const keyName = ".host-key"

type journalRecord struct {
	Event          RoomEvent      `json:"event"`
	Receipt        CommandReceipt `json:"receipt"`
	RequestHash    string         `json:"requestHash"`
	TokenHash      string         `json:"tokenHash,omitempty"`
	Room           *Room          `json:"room,omitempty"`
	Member         *Member        `json:"member,omitempty"`
	SessionHash    string         `json:"sessionHash,omitempty"`
	Members        []Member       `json:"members,omitempty"`
	Timeline       *TimelineItem  `json:"timeline,omitempty"`
	Leave          bool           `json:"leave,omitempty"`
	Heartbeat      bool           `json:"heartbeat,omitempty"`
	RevokeSessions bool           `json:"revokeSessions,omitempty"`
}

type roomState struct {
	Room         Room
	TokenHash    string
	Members      map[string]Member
	Sessions     map[string]string // SHA-256(session) -> member ID
	Timeline     []TimelineItem
	Events       []RoomEvent
	Receipts     map[string]CommandReceipt
	Fingerprints map[string]string
	Requests     map[string]AgentRequest
	Runs         map[string]AgentRun
	Results      map[string]AgentResult
	ResultKeys   map[string]AgentResult
	Files        map[string]FileOffer
	Transient    map[string]transientRequest
	TransientIDs []string
}

type transientRequest struct {
	Fingerprint string
	Receipt     CommandReceipt
}

func newRoomState() *roomState {
	return &roomState{
		Members: map[string]Member{}, Sessions: map[string]string{},
		Receipts: map[string]CommandReceipt{}, Fingerprints: map[string]string{},
		Requests: map[string]AgentRequest{}, Runs: map[string]AgentRun{}, Results: map[string]AgentResult{}, ResultKeys: map[string]AgentResult{}, Files: map[string]FileOffer{},
		Transient: map[string]transientRequest{},
	}
}

// FileStore is an append-only room event store. The journal is authoritative;
// all public snapshots are rebuilt by replay after a restart.
type FileStore struct {
	mu    sync.RWMutex
	dir   string
	key   []byte
	rooms map[string]*roomState
}

// OpenFileStore opens or creates dir and replays every complete journal entry.
// An incomplete final line is ignored because it was never durably acknowledged.
func OpenFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fail(CodeInvalid, "store directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, retryable("create collaboration store", err)
	}
	key, err := loadOrCreateKey(dir)
	if err != nil {
		return nil, err
	}
	s := &FileStore{dir: dir, key: key, rooms: map[string]*roomState{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, retryable("read collaboration store", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), journalName)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, retryable("inspect room journal", err)
		}
		if err := s.loadJournal(path, entry.Name()); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func loadOrCreateKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, keyName)
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("collab: invalid host key length %d", len(key))
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, retryable("read host key", err)
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, retryable("generate host key", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateKey(dir)
	}
	if err != nil {
		return nil, retryable("create host key", err)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, retryable("write host key", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, retryable("sync host key", err)
	}
	if err := f.Close(); err != nil {
		return nil, retryable("close host key", err)
	}
	return key, nil
}

func (s *FileStore) loadJournal(path, expectedRoom string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return retryable("read room journal", err)
	}
	complete := data
	if len(complete) > 0 && complete[len(complete)-1] != '\n' {
		cut := 0
		if i := bytes.LastIndexByte(complete, '\n'); i >= 0 {
			cut = i + 1
		}
		complete = complete[:cut]
		if err := os.Truncate(path, int64(cut)); err != nil {
			return retryable("repair incomplete journal tail", err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
		if err != nil {
			return retryable("open repaired room journal", err)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return retryable("sync repaired room journal", err)
		}
		if err := f.Close(); err != nil {
			return retryable("close repaired room journal", err)
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(complete))
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var record journalRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("collab: corrupt journal %s line %d: %w", path, line, err)
		}
		if record.Event.Room != expectedRoom || !roomIDPattern.MatchString(record.Event.Room) {
			return fmt.Errorf("collab: journal %s line %d has invalid room %q", path, line, record.Event.Room)
		}
		state := s.rooms[record.Event.Room]
		if state == nil {
			state = newRoomState()
			s.rooms[record.Event.Room] = state
		}
		if err := applyRecord(state, record); err != nil {
			return fmt.Errorf("collab: replay %s line %d: %w", path, line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return retryable("scan room journal", err)
	}
	return nil
}

func (s *FileStore) append(record journalRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("collab: encode journal: %w", err)
	}
	dir := filepath.Join(s.dir, record.Event.Room)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return retryable("create room directory", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, journalName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return retryable("open room journal", err)
	}
	_, writeErr := f.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return retryable("persist room event", writeErr)
	}
	if closeErr != nil {
		return retryable("close room journal", closeErr)
	}
	state := s.rooms[record.Event.Room]
	if state == nil {
		state = newRoomState()
		s.rooms[record.Event.Room] = state
	}
	if err := applyRecord(state, record); err != nil {
		return fmt.Errorf("collab: apply committed event: %w", err)
	}
	return nil
}

func applyRecord(state *roomState, record journalRecord) error {
	e := record.Event
	if state.Room.ID != "" && e.Sequence != state.Room.LatestSequence+1 {
		return fmt.Errorf("sequence %d follows %d", e.Sequence, state.Room.LatestSequence)
	}
	if state.Room.ID == "" && e.Sequence != 1 {
		return fmt.Errorf("first sequence is %d", e.Sequence)
	}
	if _, exists := state.Receipts[e.RequestID]; exists {
		return fmt.Errorf("duplicate request id %q in journal", e.RequestID)
	}
	if record.Room != nil {
		state.Room = *record.Room
		state.TokenHash = record.TokenHash
	}
	if record.RevokeSessions && record.Member != nil {
		for sessionHash, owner := range state.Sessions {
			if owner == record.Member.ID {
				delete(state.Sessions, sessionHash)
			}
		}
	}
	if record.Member != nil {
		member := *record.Member
		state.Members[member.ID] = member
		if record.SessionHash != "" {
			state.Sessions[record.SessionHash] = member.ID
		}
	}
	for _, member := range record.Members {
		state.Members[member.ID] = member
	}
	if record.Leave && record.Member != nil {
		member := *record.Member
		state.Members[member.ID] = member
	}
	if record.Heartbeat && record.Member != nil {
		state.Members[record.Member.ID] = *record.Member
	}
	if record.Timeline != nil {
		item := *record.Timeline
		updated := false
		if item.Type == TimelineAgentRequest || item.Type == TimelineAgentRun || item.Type == TimelineAgentResult || item.Type == TimelineFile {
			for i := range state.Timeline {
				if state.Timeline[i].ID == item.ID {
					state.Timeline[i] = item
					updated = true
					break
				}
			}
		}
		if !updated {
			state.Timeline = append(state.Timeline, item)
		}
		if item.AgentRequest != nil {
			state.Requests[item.AgentRequest.ID] = *item.AgentRequest
		}
		if item.AgentRun != nil {
			state.Runs[item.AgentRun.ID] = *item.AgentRun
			member := state.Members[item.AgentRun.OwnerID]
			switch item.AgentRun.Status {
			case RunQueued, RunRunning:
				member.Agent.Status = AgentRunning
			case RunWaitingApproval:
				member.Agent.Status = AgentWaitingApproval
			case RunFailed:
				member.Agent.Status = AgentError
			default:
				if member.Status == MemberOnline {
					member.Agent.Status = AgentIdle
				} else {
					member.Agent.Status = AgentOffline
				}
			}
			state.Members[member.ID] = member
		}
		if item.AgentResult != nil {
			state.Results[item.AgentResult.ID] = *item.AgentResult
			state.ResultKeys[resultKey(item.AgentResult.RunID, item.AgentResult.Revision)] = *item.AgentResult
		}
		if item.File != nil {
			state.Files[item.File.ID] = *item.File
		}
	}
	state.Room.LatestSequence = e.Sequence
	state.Events = append(state.Events, e)
	state.Receipts[e.RequestID] = cloneReceipt(record.Receipt)
	state.Fingerprints[e.RequestID] = record.RequestHash
	return nil
}

func (s *FileStore) room(id string) (*roomState, bool) { state, ok := s.rooms[id]; return state, ok }

func snapshotOf(state *roomState) Snapshot {
	members := make([]Member, 0, len(state.Members))
	for _, member := range state.Members {
		member.Agent.Capabilities = cloneStrings(member.Agent.Capabilities)
		members = append(members, member)
	}
	sort.Slice(members, func(i, j int) bool {
		return members[i].JoinedAt.Before(members[j].JoinedAt) || members[i].JoinedAt.Equal(members[j].JoinedAt) && members[i].ID < members[j].ID
	})
	timeline := make([]TimelineItem, len(state.Timeline))
	for i := range state.Timeline {
		timeline[i] = cloneTimelineItem(state.Timeline[i])
	}
	return Snapshot{Room: state.Room, Members: members, Timeline: timeline, LatestSequence: state.Room.LatestSequence}
}

func eventsAfter(state *roomState, after uint64) []RoomEvent {
	if after >= state.Room.LatestSequence {
		return []RoomEvent{}
	}
	i := sort.Search(len(state.Events), func(i int) bool { return state.Events[i].Sequence > after })
	result := make([]RoomEvent, len(state.Events)-i)
	for j, event := range state.Events[i:] {
		event.Payload = append(json.RawMessage(nil), event.Payload...)
		result[j] = event
	}
	return result
}

func cloneTimelineItem(item TimelineItem) TimelineItem {
	if item.Chat != nil {
		value := *item.Chat
		item.Chat = &value
	}
	if item.Contribution != nil {
		value := *item.Contribution
		value.Scope, value.TargetIDs, value.Dependencies = cloneStrings(value.Scope), cloneStrings(value.TargetIDs), cloneStrings(value.Dependencies)
		value.Metadata = cloneMap(value.Metadata)
		item.Contribution = &value
	}
	if item.AgentRequest != nil {
		value := *item.AgentRequest
		value.ReferenceIDs = cloneStrings(value.ReferenceIDs)
		item.AgentRequest = &value
	}
	if item.AgentRun != nil {
		value := *item.AgentRun
		value.ReferenceIDs = cloneStrings(value.ReferenceIDs)
		item.AgentRun = &value
	}
	if item.AgentResult != nil {
		value := *item.AgentResult
		value.ReferenceIDs = cloneStrings(value.ReferenceIDs)
		item.AgentResult = &value
	}
	if item.File != nil {
		value := *item.File
		if item.File.RevokedAt != nil {
			revokedAt := *item.File.RevokedAt
			value.RevokedAt = &revokedAt
		}
		item.File = &value
	}
	if item.Reaction != nil {
		value := *item.Reaction
		item.Reaction = &value
	}
	if item.System != nil {
		value := *item.System
		item.System = &value
	}
	return item
}

func cloneReceipt(value CommandReceipt) CommandReceipt {
	value.EventIDs = cloneStrings(value.EventIDs)
	return value
}

func cloneMember(value Member) Member {
	value.Agent.Capabilities = cloneStrings(value.Agent.Capabilities)
	return value
}
