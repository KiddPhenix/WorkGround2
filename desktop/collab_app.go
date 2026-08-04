package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/agent"
	"workground2/internal/collab"
	"workground2/internal/config"
	"workground2/internal/fileutil"
)

const (
	collaborationStateChannel = "collaboration:state"
	collaborationEventChannel = "collaboration:event"
)

// CollaborationState is the authoritative desktop projection consumed by the
// collaboration surface. Connection credentials are intentionally omitted.
type CollaborationState struct {
	Status      string                      `json:"status"`
	Mode        string                      `json:"mode,omitempty"`
	Host        string                      `json:"host,omitempty"`
	Port        int                         `json:"port,omitempty"`
	Room        string                      `json:"room,omitempty"`
	MemberID    string                      `json:"memberId,omitempty"`
	AgentID     string                      `json:"agentId,omitempty"`
	SessionID   string                      `json:"sessionId,omitempty"`
	Snapshot    collab.Snapshot             `json:"snapshot"`
	OutboxCount int                         `json:"outboxCount"`
	Outbox      []CollaborationOutboxView   `json:"outbox,omitempty"`
	LastError   string                      `json:"lastError,omitempty"`
	Retryable   bool                        `json:"retryable,omitempty"`
	Transfers   []CollaborationFileTransfer `json:"transfers,omitempty"`
	AgentConfig CollaborationAgentConfig    `json:"agentConfig"`
}

type CollaborationAgentConfig struct {
	Alias                string `json:"alias,omitempty"`
	AutoRespondQuestions bool   `json:"autoRespondQuestions"`
	AutoRespondRequests  bool   `json:"autoRespondRequests"`
	RecognitionMode      string `json:"recognitionMode"`
}

type UpdateCollaborationAgentConfigInput struct {
	SessionID string                   `json:"sessionId"`
	RequestID string                   `json:"requestId"`
	Config    CollaborationAgentConfig `json:"config"`
}

type CollaborationFileTransfer struct {
	ID          string `json:"id"`
	FileID      string `json:"fileId"`
	Direction   string `json:"direction"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Transferred int64  `json:"transferred"`
	Total       int64  `json:"total"`
	Destination string `json:"destination,omitempty"`
	Error       string `json:"error,omitempty"`
	Retryable   bool   `json:"retryable,omitempty"`
	Completed   []bool `json:"completed,omitempty"`
	PartPath    string `json:"partPath,omitempty"`
}

type ShareCollaborationFilesInput struct {
	SessionID string   `json:"sessionId"`
	Paths     []string `json:"paths"`
}

type ReceiveCollaborationFileInput struct {
	SessionID   string `json:"sessionId"`
	FileID      string `json:"fileId"`
	Destination string `json:"destination,omitempty"`
}

type CollaborationFileActionInput struct {
	SessionID string `json:"sessionId"`
	FileID    string `json:"fileId"`
}

type CollaborationOutboxView struct {
	RequestID string               `json:"requestId"`
	Type      string               `json:"type"`
	Status    string               `json:"status"`
	LastError string               `json:"lastError,omitempty"`
	Item      *collab.TimelineItem `json:"item,omitempty"`
}

// CollaborationInvite is returned only after an explicit export action. The
// normal Room state intentionally continues to omit the join token.
type CollaborationInvite struct {
	Hosts []string `json:"hosts"`
	Port  int      `json:"port"`
	Room  string   `json:"room"`
	Token string   `json:"token,omitempty"`
}

type HostCollaborationRoomInput struct {
	ListenHost  string `json:"listenHost"`
	Port        int    `json:"port"`
	Room        string `json:"room"`
	RoomName    string `json:"roomName,omitempty"`
	Description string `json:"description,omitempty"`
	Token       string `json:"token,omitempty"`
	MemberID    string `json:"memberId,omitempty"`
	MemberName  string `json:"memberName"`
	MemberRole  string `json:"memberRole,omitempty"`
	AgentID     string `json:"agentId,omitempty"`
	AgentName   string `json:"agentName,omitempty"`
	AgentRole   string `json:"agentRole,omitempty"`
	SessionID   string `json:"sessionId"`
}

type JoinCollaborationRoomInput struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Room       string `json:"room"`
	Token      string `json:"token,omitempty"`
	MemberID   string `json:"memberId,omitempty"`
	MemberName string `json:"memberName"`
	MemberRole string `json:"memberRole,omitempty"`
	AgentID    string `json:"agentId,omitempty"`
	AgentName  string `json:"agentName,omitempty"`
	AgentRole  string `json:"agentRole,omitempty"`
	SessionID  string `json:"sessionId"`
}

type PostCollaborationMessageInput struct {
	RequestID        string                  `json:"requestId"`
	SessionID        string                  `json:"sessionId"`
	Kind             string                  `json:"kind"`
	Text             string                  `json:"text"`
	Title            string                  `json:"title,omitempty"`
	ContributionKind collab.ContributionKind `json:"contributionKind,omitempty"`
	TargetMemberID   string                  `json:"targetMemberId,omitempty"`
	TargetItemID     string                  `json:"targetItemId,omitempty"`
	TargetIDs        []string                `json:"targetIds,omitempty"`
	ReferenceIDs     []string                `json:"referenceIds,omitempty"`
	Scope            []string                `json:"scope,omitempty"`
	RelatedItem      string                  `json:"relatedItem,omitempty"`
	Dependencies     []string                `json:"dependencies,omitempty"`
	ActionNeeded     bool                    `json:"actionNeeded,omitempty"`
	ReactionKind     string                  `json:"reactionKind,omitempty"`
}

type StartCollaborationAgentInput struct {
	RequestID      string   `json:"requestId"`
	SessionID      string   `json:"sessionId"`
	Instruction    string   `json:"instruction"`
	ReferenceIDs   []string `json:"referenceIds,omitempty"`
	AgentRequestID string   `json:"agentRequestId,omitempty"`
}

type ClassifyCollaborationIntentInput struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

type CollaborationIntentResult struct {
	Intent    string `json:"intent"`
	Source    string `json:"source"`
	Error     string `json:"error,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type RespondCollaborationRequestInput struct {
	RequestID      string `json:"requestId"`
	AgentRequestID string `json:"agentRequestId"`
	Action         string `json:"action"`
	Instruction    string `json:"instruction,omitempty"`
	SessionID      string `json:"sessionId"`
}

type CollaborationActionResult struct {
	RequestID string                `json:"requestId"`
	Code      string                `json:"code,omitempty"`
	RunID     string                `json:"runId,omitempty"`
	Receipt   collab.CommandReceipt `json:"receipt"`
	Item      *collab.TimelineItem  `json:"item,omitempty"`
	Duplicate bool                  `json:"duplicate,omitempty"`
	Queued    bool                  `json:"queued,omitempty"`
	Retryable bool                  `json:"retryable,omitempty"`
	Error     string                `json:"error,omitempty"`
}

type CollaborationEventView struct {
	SessionID string               `json:"sessionId"`
	Event     collab.RoomEvent     `json:"event"`
	Item      *collab.TimelineItem `json:"item,omitempty"`
}

// desktopCollaboration is deliberately above Controller. It owns Room
// transport state, while the selected Controller continues to own the turn.
type desktopCollaboration struct {
	app            *App
	ownerSessionID string

	opMu           sync.Mutex
	mu             sync.RWMutex
	state          CollaborationState
	conn           *collaborationConnection
	outbox         []collab.CommandEnvelope
	outboxFailures map[string]string
	starts         map[string]collaborationStartRecord
	runs           map[string]*collaborationAgentRun
	recoveredRuns  []collaborationPersistedRun
	leaveError     string
	recovery       collaborationPersistedState
	shares         map[string]collaborationSharedFile
	transfers      map[string]*CollaborationFileTransfer
	transferCancel map[string]context.CancelFunc
	fileOrigin     *collaborationFileOrigin

	persistPath       string
	legacyPersistPath string
	writeState        func(string, []byte, os.FileMode) error
	setSecret         func(string, string) error
	getSecret         func(string) string
	removeSecret      func(string) error
	validateAgent     func(string) error
	agentReady        func(string) (bool, error)
	waitAgentReady    func(context.Context, string) error
	submitAgent       func(string, string, string) error
	openHost          func(context.Context, HostCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error)
	openJoin          func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error)
}

type collaborationConnection struct {
	peer              collaborationPeer
	filePeer          *httpCollaborationPeer
	host              *http.Server
	listener          net.Listener
	mode              string
	roomName          string
	description       string
	hostName          string
	port              int
	room              string
	memberID          string
	memberName        string
	memberRole        string
	agentID           string
	agentName         string
	agentRole         string
	sessionID         string
	joinToken         string
	connectionSession string
	initialSnapshot   collab.Snapshot
	rejoined          bool
	sweep             func(context.Context) error
	cancel            context.CancelFunc
	done              chan struct{}
	closeOnce         sync.Once
}

type collaborationPeer interface {
	Snapshot(context.Context) (collab.Snapshot, error)
	Events(context.Context, uint64) ([]collab.RoomEvent, error)
	Stream(context.Context, uint64, func(collab.RoomEvent) error) error
	Submit(context.Context, collab.CommandEnvelope) (collab.CommandReceipt, error)
	Heartbeat(context.Context, string) error
	Leave(context.Context, string) error
}

type collaborationAgentRun struct {
	Room           string
	MemberID       string
	AgentID        string
	RunID          string
	CommandID      string
	SessionID      string
	AgentRequestID string
	Instruction    string
	ReferenceIDs   []string
	Text           strings.Builder
	PublishIndex   int
	Updates        chan collaborationRunUpdate
}

type collaborationStartRecord struct {
	RunID       string `json:"runId"`
	Fingerprint string `json:"fingerprint"`
}

type collaborationRunUpdate struct {
	Status  collab.AgentRunStatus
	Summary string
	Error   string
	Final   bool
}

type collaborationPersistedState struct {
	Mode                string                              `json:"mode,omitempty"`
	Host                string                              `json:"host,omitempty"`
	Port                int                                 `json:"port,omitempty"`
	Room                string                              `json:"room,omitempty"`
	MemberID            string                              `json:"memberId,omitempty"`
	AgentID             string                              `json:"agentId,omitempty"`
	SessionID           string                              `json:"sessionId,omitempty"`
	RoomName            string                              `json:"roomName,omitempty"`
	Description         string                              `json:"description,omitempty"`
	MemberName          string                              `json:"memberName,omitempty"`
	MemberRole          string                              `json:"memberRole,omitempty"`
	AgentName           string                              `json:"agentName,omitempty"`
	AgentRole           string                              `json:"agentRole,omitempty"`
	ConnectionSecretRef string                              `json:"connectionSecretRef,omitempty"`
	JoinTokenSecretRef  string                              `json:"joinTokenSecretRef,omitempty"`
	AfterSequence       uint64                              `json:"afterSequence,omitempty"`
	Snapshot            collab.Snapshot                     `json:"snapshot,omitempty"`
	Outbox              []collab.CommandEnvelope            `json:"outbox,omitempty"`
	OutboxFailures      map[string]string                   `json:"outboxFailures,omitempty"`
	Starts              map[string]collaborationStartRecord `json:"starts,omitempty"`
	Runs                []collaborationPersistedRun         `json:"runs,omitempty"`
	Shares              []collaborationSharedFile           `json:"shares,omitempty"`
	Transfers           []CollaborationFileTransfer         `json:"transfers,omitempty"`
	AgentConfig         CollaborationAgentConfig            `json:"agentConfig,omitempty"`
}

type collaborationPersistedRun struct {
	Room           string   `json:"room"`
	MemberID       string   `json:"memberId"`
	AgentID        string   `json:"agentId"`
	RunID          string   `json:"runId"`
	CommandID      string   `json:"commandId"`
	SessionID      string   `json:"sessionId"`
	AgentRequestID string   `json:"agentRequestId,omitempty"`
	Instruction    string   `json:"instruction"`
	ReferenceIDs   []string `json:"referenceIds,omitempty"`
}

func newDesktopCollaboration(app *App, sessionID string) *desktopCollaboration {
	sessionID = strings.TrimSpace(sessionID)
	c := &desktopCollaboration{
		app:            app,
		ownerSessionID: sessionID,
		state:          CollaborationState{Status: "disconnected", SessionID: sessionID, AgentConfig: defaultCollaborationAgentConfig()},
		starts:         map[string]collaborationStartRecord{},
		runs:           map[string]*collaborationAgentRun{},
		shares:         map[string]collaborationSharedFile{},
		transfers:      map[string]*CollaborationFileTransfer{},
		transferCancel: map[string]context.CancelFunc{},
		outboxFailures: map[string]string{},
	}
	c.openHost = c.openHostedRoom
	c.openJoin = c.openJoinedRoom
	c.writeState = fileutil.AtomicWriteFile
	c.setSecret = func(key, value string) error { _, err := config.SetCredential(key, value); return err }
	c.getSecret = func(key string) string { return config.ResolveCredential(key).Value }
	c.removeSecret = config.RemoveCredential
	c.validateAgent = c.validateLocalController
	c.agentReady = app.collaborationAgentReady
	c.waitAgentReady = app.waitCollaborationAgentReady
	c.submitAgent = func(sessionID, display, input string) error {
		return app.SubmitDisplayToTab(sessionID, display, input)
	}
	root := strings.TrimSpace(config.MemoryUserDir())
	if root != "" {
		c.legacyPersistPath = filepath.Join(root, "desktop-collaboration-v1.json")
		stateDir := filepath.Join(root, "desktop-collaboration-v2")
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			c.state.LastError = "create collaboration state directory: " + err.Error()
			c.state.Retryable = true
		} else {
			c.persistPath = filepath.Join(stateDir, collaborationSessionStateName(sessionID))
			c.loadPersisted()
		}
	}
	return c
}

func collaborationSessionStateName(sessionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	return hex.EncodeToString(sum[:16]) + ".json"
}

func (a *App) collaborationRuntime(sessionID string) (*desktopCollaboration, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	a.collaborationMu.Lock()
	defer a.collaborationMu.Unlock()
	if a.collaborations == nil {
		a.collaborations = make(map[string]*desktopCollaboration)
	}
	if runtime := a.collaborations[sessionID]; runtime != nil {
		return runtime, nil
	}
	runtime := newDesktopCollaboration(a, sessionID)
	a.collaborations[sessionID] = runtime
	return runtime, nil
}

func (a *App) closeCollaborations() {
	a.collaborationMu.Lock()
	runtimes := make([]*desktopCollaboration, 0, len(a.collaborations))
	for _, runtime := range a.collaborations {
		runtimes = append(runtimes, runtime)
	}
	a.collaborations = nil
	a.collaborationMu.Unlock()
	var wg sync.WaitGroup
	for _, runtime := range runtimes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.close()
		}()
	}
	wg.Wait()
}

func (a *App) GetCollaborationState(sessionID string) (CollaborationState, error) {
	runtime, err := a.collaborationRuntime(sessionID)
	if err != nil {
		return CollaborationState{}, err
	}
	return runtime.snapshot(), nil
}

func (a *App) UpdateCollaborationAgentConfig(input UpdateCollaborationAgentConfigInput) (CollaborationState, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationState{}, err
	}
	return runtime.updateAgentConfig(a.bootContext(), input)
}

func defaultCollaborationAgentConfig() CollaborationAgentConfig {
	return CollaborationAgentConfig{RecognitionMode: "off"}
}

func normalizeCollaborationAgentConfig(value CollaborationAgentConfig, fallbackAlias string) CollaborationAgentConfig {
	value.Alias = strings.TrimSpace(value.Alias)
	if value.Alias == "" {
		value.Alias = strings.TrimSpace(fallbackAlias)
	}
	switch value.RecognitionMode {
	case "message", "interval", "off":
	default:
		value.RecognitionMode = "off"
	}
	return value
}

func (c *desktopCollaboration) updateAgentConfig(ctx context.Context, input UpdateCollaborationAgentConfigInput) (CollaborationState, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	input.RequestID = strings.TrimSpace(input.RequestID)

	c.mu.RLock()
	current := normalizeCollaborationAgentConfig(c.state.AgentConfig, c.currentAgentNameLocked())
	c.mu.RUnlock()
	next := normalizeCollaborationAgentConfig(input.Config, current.Alias)
	if next.Alias == "" || len(next.Alias) > 256 {
		return c.snapshot(), fmt.Errorf("Agent alias is required and must not exceed 256 bytes")
	}
	if next == current {
		return c.snapshot(), nil
	}
	if next.Alias != current.Alias {
		if input.RequestID == "" {
			return c.snapshot(), fmt.Errorf("requestId is required when changing the Agent alias")
		}
		if _, err := c.submit(ctx, input.RequestID, collab.Command{Type: collab.CommandUpdateAgent, AgentUpdate: &collab.UpdateAgentInput{Name: next.Alias}}); err != nil {
			return c.snapshot(), err
		}
	}

	c.mu.Lock()
	c.state.AgentConfig = next
	if c.conn != nil {
		c.conn.agentName = next.Alias
	}
	for i := range c.state.Snapshot.Members {
		if c.state.Snapshot.Members[i].ID == c.state.MemberID {
			c.state.Snapshot.Members[i].Agent.Name = next.Alias
			break
		}
	}
	c.persistLocked()
	state := cloneCollaborationState(c.state)
	c.mu.Unlock()
	c.emitState()
	return state, nil
}

func (c *desktopCollaboration) currentAgentNameLocked() string {
	for _, member := range c.state.Snapshot.Members {
		if member.ID == c.state.MemberID {
			return member.Agent.Name
		}
	}
	if c.conn != nil {
		return c.conn.agentName
	}
	return c.recovery.AgentName
}

func (a *App) GetCollaborationInvite(sessionID string) (CollaborationInvite, error) {
	runtime, err := a.collaborationRuntime(sessionID)
	if err != nil {
		return CollaborationInvite{}, err
	}
	return runtime.invite()
}

func (a *App) HostCollaborationRoom(input HostCollaborationRoomInput) (CollaborationState, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationState{}, err
	}
	state, err := runtime.host(a.bootContext(), input)
	if err != nil {
		return state, err
	}
	if err := a.bindCollaborationSession(input.SessionID, collaborationSessionTitle(state, input.RoomName, input.Room)); err != nil {
		_ = runtime.leave(a.bootContext())
		return state, fmt.Errorf("bind collaboration session: %w", err)
	}
	return state, nil
}

func (a *App) JoinCollaborationRoom(input JoinCollaborationRoomInput) (CollaborationState, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationState{}, err
	}
	state, err := runtime.join(a.bootContext(), input)
	if err != nil {
		return state, err
	}
	if err := a.bindCollaborationSession(input.SessionID, collaborationSessionTitle(state, "", input.Room)); err != nil {
		_ = runtime.leave(a.bootContext())
		return state, fmt.Errorf("bind collaboration session: %w", err)
	}
	return state, nil
}

func collaborationSessionTitle(state CollaborationState, preferred, fallback string) string {
	for _, value := range []string{preferred, state.Snapshot.Room.Name, fallback, state.Room} {
		if title := strings.TrimSpace(value); title != "" {
			return title
		}
	}
	return "多人协作"
}

// bindCollaborationSession converts one blank session in-place so Room state
// keeps the same workspace, topic, session identity, and agent controller.
// Repeating the same bind is safe; other specialized session kinds are rejected.
func (a *App) bindCollaborationSession(sessionID, title string) error {
	sessionID = strings.TrimSpace(sessionID)
	title = strings.TrimSpace(title)
	if sessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	tab, _ := a.sessionAndCtrl(sessionID)
	if tab == nil {
		return fmt.Errorf("session %q is unavailable", sessionID)
	}

	a.mu.RLock()
	kind := tab.sessionKind
	readOnly := tab.ReadOnly
	sessionPath := strings.TrimSpace(tab.currentSessionPath())
	scope := tab.Scope
	workspaceRoot := tab.WorkspaceRoot
	topicID := tab.TopicID
	a.mu.RUnlock()
	if readOnly {
		return fmt.Errorf("session %q is read-only", sessionID)
	}
	if kind != "" && kind != agent.SessionKindNormal && kind != agent.SessionKindCollaboration {
		return fmt.Errorf("session kind %q cannot become collaboration", kind)
	}
	if kind != agent.SessionKindCollaboration && !blankTabSessionPathHasNoContent(tab) {
		return fmt.Errorf("session %q already has conversation content", sessionID)
	}
	if sessionPath == "" {
		return fmt.Errorf("session %q path is empty", sessionID)
	}
	if title == "" {
		title = "多人协作"
	}

	meta, err := agent.EnsureBranchMeta(sessionPath)
	if err != nil {
		return err
	}
	meta.SessionKind = agent.SessionKindCollaboration
	meta.SessionSource = "collaboration"
	meta.Scope = scope
	meta.WorkspaceRoot = workspaceRoot
	meta.TopicID = topicID
	meta.CustomTitle = title
	meta.TopicTitle = title
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, meta); err != nil {
		return err
	}
	if err := a.RenameSession(sessionPath, title); err != nil {
		return err
	}
	titleRoot := workspaceRoot
	if scope == "global" {
		titleRoot = ""
	}
	if err := ensureTopicIndexed(scope, titleRoot, topicID, title, topicTitleSourceAuto); err != nil {
		return err
	}

	a.mu.Lock()
	tab.sessionKind = agent.SessionKindCollaboration
	tab.TopicTitle = title
	a.saveTabsLocked()
	a.mu.Unlock()
	a.updateTopicSessionTitles(topicID, title)
	a.emitProjectTreeChanged()
	return nil
}

func (a *App) LeaveCollaborationRoom(sessionID string) error {
	runtime, err := a.collaborationRuntime(sessionID)
	if err != nil {
		return err
	}
	return runtime.leave(a.bootContext())
}

func (a *App) PostCollaborationMessage(input PostCollaborationMessageInput) (CollaborationActionResult, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationActionResult{}, err
	}
	return runtime.post(a.bootContext(), input)
}

// ClassifyCollaborationIntent is a read-only semantic fallback for Room
// messages that were not covered by deterministic frontend rules.
func (a *App) ClassifyCollaborationIntent(input ClassifyCollaborationIntentInput) CollaborationIntentResult {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return CollaborationIntentResult{Intent: string(agent.SemanticIntentChat), Source: "rule"}
	}
	tab := a.sessionByID(strings.TrimSpace(input.SessionID))
	if tab == nil {
		return CollaborationIntentResult{Intent: string(agent.SemanticIntentChat), Source: "fallback", Error: "collaboration Session is unavailable", Retryable: true}
	}
	a.mu.RLock()
	ctrl := tab.Ctrl
	readOnly := tab.ReadOnly
	startupErr := strings.TrimSpace(tab.StartupErr)
	a.mu.RUnlock()
	if readOnly {
		return CollaborationIntentResult{Intent: string(agent.SemanticIntentChat), Source: "fallback", Error: readOnlyChannelErr().Error()}
	}
	if ctrl == nil {
		if startupErr == "" {
			startupErr = "Session model is still starting"
		}
		return CollaborationIntentResult{Intent: string(agent.SemanticIntentChat), Source: "fallback", Error: startupErr, Retryable: true}
	}
	classifier, ok := ctrl.(interface {
		ClassifySemanticIntent(context.Context, string) (agent.SemanticIntent, error)
	})
	if !ok {
		return CollaborationIntentResult{Intent: string(agent.SemanticIntentChat), Source: "fallback", Error: "Session model does not support semantic intent classification"}
	}
	ctx, cancel := context.WithTimeout(a.bootContext(), 4*time.Second)
	defer cancel()
	intent, err := classifier.ClassifySemanticIntent(ctx, text)
	if err != nil {
		return CollaborationIntentResult{Intent: string(agent.SemanticIntentChat), Source: "fallback", Error: err.Error(), Retryable: true}
	}
	return CollaborationIntentResult{Intent: string(intent), Source: "llm"}
}

func (a *App) StartCollaborationAgent(input StartCollaborationAgentInput) (CollaborationActionResult, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationActionResult{}, err
	}
	return runtime.startAgent(a.bootContext(), input)
}

func (a *App) RespondCollaborationRequest(input RespondCollaborationRequestInput) (CollaborationActionResult, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationActionResult{}, err
	}
	return runtime.respond(a.bootContext(), input)
}

func (a *App) RetryCollaboration(sessionID string) (CollaborationState, error) {
	runtime, err := a.collaborationRuntime(sessionID)
	if err != nil {
		return CollaborationState{}, err
	}
	return runtime.retry(a.bootContext())
}

func (c *desktopCollaboration) snapshot() CollaborationState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	state := cloneCollaborationState(c.state)
	if state.SessionID == "" {
		state.SessionID = c.ownerSessionID
	}
	state.OutboxCount = len(c.outbox)
	state.Outbox = c.outboxViewsLocked()
	state.Transfers = c.fileTransfersLocked()
	return state
}

func (c *desktopCollaboration) outboxViewsLocked() []CollaborationOutboxView {
	result := make([]CollaborationOutboxView, 0, len(c.outbox))
	sequence := c.state.Snapshot.LatestSequence
	room := c.state.Room
	memberID := c.state.MemberID
	for _, env := range c.outbox {
		if env.Room != room || env.MemberID != memberID {
			continue
		}
		status := "pending"
		lastError := c.outboxFailures[env.RequestID]
		if lastError != "" {
			status = "failed"
		}
		item := collaborationQueuedItem(env, sequence+1)
		if item != nil {
			sequence++
		}
		result = append(result, CollaborationOutboxView{RequestID: env.RequestID, Type: string(env.Command.Type), Status: status, LastError: lastError, Item: item})
	}
	return result
}

func collaborationQueuedItem(env collab.CommandEnvelope, sequence uint64) *collab.TimelineItem {
	id := "outbox:" + env.RequestID
	createdAt, _ := time.Parse(time.RFC3339Nano, env.QueuedAt)
	switch env.Command.Type {
	case collab.CommandPostChat:
		if env.Command.Chat == nil {
			return nil
		}
		return &collab.TimelineItem{ID: id, Sequence: sequence, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: id, AuthorID: env.MemberID, Text: env.Command.Chat.Text, Revision: 1, CreatedAt: createdAt}}
	case collab.CommandPublishContribution:
		if env.Command.Contribution == nil {
			return nil
		}
		value := env.Command.Contribution
		return &collab.TimelineItem{ID: id, Sequence: sequence, Type: collab.TimelineContribution, Contribution: &collab.Contribution{ID: id, AuthorID: env.MemberID, Kind: value.Kind, Title: value.Title, Body: value.Body, Scope: append([]string(nil), value.Scope...), TargetIDs: append([]string(nil), value.TargetIDs...), RelatedItem: value.RelatedItem, Dependencies: append([]string(nil), value.Dependencies...), ActionNeeded: value.ActionNeeded, Revision: 1, CreatedAt: createdAt}}
	case collab.CommandCreateAgentRequest:
		if env.Command.AgentRequest == nil {
			return nil
		}
		value := env.Command.AgentRequest
		return &collab.TimelineItem{ID: id, Sequence: sequence, Type: collab.TimelineAgentRequest, AgentRequest: &collab.AgentRequest{ID: id, AuthorID: env.MemberID, TargetMemberID: value.TargetMemberID, Instruction: value.Instruction, ReferenceIDs: append([]string(nil), value.ReferenceIDs...), Status: collab.RequestPending, CreatedAt: createdAt, UpdatedAt: createdAt}}
	case collab.CommandPublishAgentRun:
		if env.Command.AgentRun == nil {
			return nil
		}
		value := env.Command.AgentRun
		return &collab.TimelineItem{ID: id, Sequence: sequence, Type: collab.TimelineAgentRun, AgentRun: &collab.AgentRun{ID: value.RunID, OwnerID: env.MemberID, AgentID: value.AgentID, CommandID: value.CommandID, RequestRef: value.RequestRef, Instruction: value.Instruction, ReferenceIDs: append([]string(nil), value.ReferenceIDs...), Status: value.Status, Summary: value.Summary, Error: value.Error, UpdatedAt: createdAt}}
	case collab.CommandPublishAgentResult:
		if env.Command.AgentResult == nil {
			return nil
		}
		value := env.Command.AgentResult
		return &collab.TimelineItem{ID: id, Sequence: sequence, Type: collab.TimelineAgentResult, AgentResult: &collab.AgentResult{ID: value.ResultID, OwnerID: env.MemberID, AgentID: value.AgentID, RunID: value.RunID, Revision: value.Revision, Summary: value.Summary, ReferenceIDs: append([]string(nil), value.ReferenceIDs...), CreatedAt: createdAt}}
	case collab.CommandOfferFile:
		if env.Command.FileOffer == nil {
			return nil
		}
		value := env.Command.FileOffer
		return &collab.TimelineItem{ID: value.FileID, Sequence: sequence, Type: collab.TimelineFile, File: &collab.FileOffer{ID: value.FileID, OwnerID: env.MemberID, Name: value.Name, Size: value.Size, MIME: value.MIME, SHA256: value.SHA256, ManifestHash: value.ManifestHash, ChunkSize: value.ChunkSize, ChunkCount: value.ChunkCount, Revision: 1, CreatedAt: createdAt}}
	default:
		return nil
	}
}

func cloneCollaborationState(state CollaborationState) CollaborationState {
	state.Snapshot.Members = append([]collab.Member(nil), state.Snapshot.Members...)
	state.Snapshot.Timeline = append([]collab.TimelineItem(nil), state.Snapshot.Timeline...)
	state.Outbox = append([]CollaborationOutboxView(nil), state.Outbox...)
	state.Transfers = cloneCollaborationTransfers(state.Transfers)
	return state
}

func (c *desktopCollaboration) host(ctx context.Context, input HostCollaborationRoomInput) (CollaborationState, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	var err error
	input.ListenHost, err = normalizeCollaborationHost(input.ListenHost)
	if err != nil {
		return c.failState("failed", err, false), err
	}
	identity, err := c.localIdentity(input.MemberID, input.MemberName, input.MemberRole, input.AgentID, input.AgentName, input.AgentRole, input.SessionID, input.Room)
	if err != nil {
		return c.failState("failed", err, false), err
	}
	resume := c.resumeSession(input.ListenHost, input.Port, input.Room, identity.ID, input.SessionID)
	c.fenceCurrentConnection()
	c.setConnecting("host", input.ListenHost, input.Port, input.Room, identity, input.SessionID)
	conn, err := c.openHost(ctx, input, identity, resume)
	if err != nil {
		return c.failState("failed", err, collaborationErrorRetryable(err)), err
	}
	return c.installConnection(conn)
}

func (c *desktopCollaboration) join(ctx context.Context, input JoinCollaborationRoomInput) (CollaborationState, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	var err error
	input.Host, err = normalizeCollaborationHost(input.Host)
	if err != nil {
		return c.failState("failed", err, false), err
	}
	identity, err := c.localIdentity(input.MemberID, input.MemberName, input.MemberRole, input.AgentID, input.AgentName, input.AgentRole, input.SessionID, input.Room)
	if err != nil {
		return c.failState("failed", err, false), err
	}
	resume := c.resumeSession(input.Host, input.Port, input.Room, identity.ID, input.SessionID)
	scopedIdentity := scopedCollaborationIdentity(identity, input.Room, input.SessionID)
	if resume == "" {
		if scopedResume := c.resumeSession(input.Host, input.Port, input.Room, scopedIdentity.ID, input.SessionID); scopedResume != "" {
			identity, resume = scopedIdentity, scopedResume
		}
	}
	c.fenceCurrentConnection()
	c.setConnecting("client", input.Host, input.Port, input.Room, identity, input.SessionID)
	conn, err := c.openJoin(ctx, input, identity, resume)
	if err != nil && collaborationMemberResumeRequired(err) && identity.ID != scopedIdentity.ID {
		identity = scopedIdentity
		resume = c.resumeSession(input.Host, input.Port, input.Room, identity.ID, input.SessionID)
		c.setConnecting("client", input.Host, input.Port, input.Room, identity, input.SessionID)
		conn, err = c.openJoin(ctx, input, identity, resume)
	}
	if err != nil {
		return c.failState("failed", err, collaborationErrorRetryable(err)), err
	}
	return c.installConnection(conn)
}

func scopedCollaborationIdentity(identity collab.MemberDescriptor, room, sessionID string) collab.MemberDescriptor {
	scope := strings.TrimSpace(room) + "\x00" + strings.TrimSpace(sessionID)
	identity.ID = stableCollaborationID("member", identity.ID+"\x00"+scope)
	identity.Agent.ID = stableCollaborationID("agent", identity.Agent.ID+"\x00"+scope)
	return identity
}

func collaborationMemberResumeRequired(err error) bool {
	var protocol *collab.Error
	return errors.As(err, &protocol) && (protocol.Code == collab.CodeResumeNeeded ||
		(protocol.Code == collab.CodeUnauthorized && protocol.Message == collab.ResumeRequiredMessage))
}

func normalizeCollaborationHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") || strings.HasSuffix(value, "]") {
		if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") || len(value) < 3 {
			return "", fmt.Errorf("invalid bracketed collaboration host %q", value)
		}
		value = strings.TrimSpace(value[1 : len(value)-1])
		if net.ParseIP(strings.Split(value, "%")[0]) == nil {
			return "", fmt.Errorf("brackets are only valid around an IPv6 address")
		}
	}
	return value, nil
}

func (c *desktopCollaboration) invite() (CollaborationInvite, error) {
	c.mu.RLock()
	state := cloneCollaborationState(c.state)
	conn := c.conn
	token := ""
	if conn != nil {
		token = conn.joinToken
	}
	c.mu.RUnlock()
	if state.Mode != "host" || state.Room == "" || state.Port < 1 {
		return CollaborationInvite{}, fmt.Errorf("only the Room Host can export a collaboration connection")
	}
	if token == "" {
		persisted := c.readPersisted()
		if persisted.JoinTokenSecretRef != "" && c.getSecret != nil {
			token = c.getSecret(persisted.JoinTokenSecretRef)
		}
	}
	return CollaborationInvite{
		Hosts: collaborationLocalHosts(state.Host),
		Port:  state.Port,
		Room:  state.Room,
		Token: token,
	}, nil
}

func collaborationLocalHosts(bindHost string) []string {
	bindHost = strings.Trim(strings.TrimSpace(bindHost), "[]")
	bindIP := net.ParseIP(strings.Split(bindHost, "%")[0])
	if bindHost != "" && (bindIP == nil || !bindIP.IsUnspecified()) {
		return []string{bindHost}
	}
	seen := map[string]bool{}
	var ipv4, ipv6 []string
	add := func(value string) {
		value = strings.Trim(strings.TrimSpace(value), "[]")
		ip := net.ParseIP(strings.Split(value, "%")[0])
		if ip == nil || ip.IsUnspecified() || seen[value] {
			return
		}
		seen[value] = true
		if ip.To4() != nil {
			ipv4 = append(ipv4, value)
		} else {
			ipv6 = append(ipv6, value)
		}
	}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			host, _, err := net.ParseCIDR(address.String())
			if err != nil || host.IsLinkLocalUnicast() || host.IsLinkLocalMulticast() {
				continue
			}
			add(host.String())
		}
	}
	add("127.0.0.1")
	sort.Strings(ipv4)
	sort.Strings(ipv6)
	return append(ipv4, ipv6...)
}

func (c *desktopCollaboration) leave(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.leaveCurrent(ctx)
}

func (c *desktopCollaboration) leaveCurrent(ctx context.Context) error {
	c.mu.RLock()
	conn := c.conn
	secretRef := collaborationSecretRef(c.state.Host, c.state.Port, c.state.Room, c.state.MemberID)
	tokenRef := collaborationTokenRef(c.state.Host, c.state.Port, c.state.Room, c.state.MemberID)
	c.mu.RUnlock()
	if conn == nil {
		return nil
	}
	leaveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	err := conn.peer.Leave(leaveCtx, newCollaborationRequestID("leave"))
	cancel()
	if err != nil {
		c.mu.Lock()
		c.leaveError = err.Error()
		c.mu.Unlock()
		c.failState("failed", err, collaborationErrorRetryable(err))
		return err
	}
	closeCtx, closeCancel := context.WithTimeout(ctx, 3*time.Second)
	err = conn.close(closeCtx, false)
	closeCancel()
	if err != nil {
		c.failState("failed", err, true)
		return err
	}
	if removeErr := c.removeSecret(secretRef); removeErr != nil {
		c.failState("failed", removeErr, true)
		return removeErr
	}
	if removeErr := c.removeSecret(tokenRef); removeErr != nil {
		c.failState("failed", removeErr, true)
		return removeErr
	}
	c.mu.Lock()
	c.leaveError = ""
	if c.conn == conn {
		c.conn = nil
	}
	c.state = CollaborationState{Status: "disconnected", SessionID: c.ownerSessionID, OutboxCount: len(c.outbox)}
	c.persistLocked()
	c.mu.Unlock()
	c.closeFileTransfers()
	c.emitState()
	return nil
}

func (c *desktopCollaboration) close() {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	conn := c.conn
	c.persistLocked()
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = conn.close(ctx, false)
		cancel()
	}
	c.closeFileTransfers()
}

func (c *desktopCollaboration) post(ctx context.Context, input PostCollaborationMessageInput) (CollaborationActionResult, error) {
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return CollaborationActionResult{}, fmt.Errorf("requestId is required")
	}
	if inputSessionID := strings.TrimSpace(input.SessionID); inputSessionID != "" && inputSessionID != c.ownerSessionID {
		return CollaborationActionResult{}, fmt.Errorf("sessionId does not match collaboration runtime")
	}
	command, err := collaborationPostCommand(input)
	if err != nil {
		return CollaborationActionResult{}, err
	}
	return c.submit(ctx, requestID, command)
}

func (c *desktopCollaboration) retry(ctx context.Context) (CollaborationState, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	conn := c.conn
	pendingLeave := c.leaveError != ""
	if pendingLeave {
		c.mu.Unlock()
		err := c.leaveCurrent(ctx)
		return c.snapshot(), err
	}
	for requestID := range c.outboxFailures {
		delete(c.outboxFailures, requestID)
	}
	c.state.Status = "reconnecting"
	c.state.LastError = ""
	c.state.Retryable = false
	if conn != nil {
		c.persistLocked()
	}
	c.mu.Unlock()
	if conn == nil {
		p := c.repairPersisted(c.readPersisted())
		if p.Mode == "" || p.Host == "" || p.Room == "" || p.SessionID == "" {
			return c.snapshot(), fmt.Errorf("collaboration connection must be joined again")
		}
		identity, err := c.localIdentity(p.MemberID, p.MemberName, p.MemberRole, p.AgentID, p.AgentName, p.AgentRole, p.SessionID, p.Room)
		if err != nil {
			return c.failState("failed", err, true), err
		}
		token := ""
		if p.JoinTokenSecretRef != "" {
			token = c.getSecret(p.JoinTokenSecretRef)
		}
		resume := ""
		if p.ConnectionSecretRef != "" {
			resume = c.getSecret(p.ConnectionSecretRef)
		}
		if p.Mode == "host" {
			conn, err = c.openHost(ctx, HostCollaborationRoomInput{
				ListenHost: p.Host, Port: p.Port, Room: p.Room, RoomName: p.RoomName, Description: p.Description, Token: token,
				MemberID: p.MemberID, MemberName: p.MemberName, MemberRole: p.MemberRole,
				AgentID: p.AgentID, AgentName: p.AgentName, AgentRole: p.AgentRole, SessionID: p.SessionID,
			}, identity, resume)
		} else {
			conn, err = c.openJoin(ctx, JoinCollaborationRoomInput{
				Host: p.Host, Port: p.Port, Room: p.Room, Token: token,
				MemberID: p.MemberID, MemberName: p.MemberName, MemberRole: p.MemberRole,
				AgentID: p.AgentID, AgentName: p.AgentName, AgentRole: p.AgentRole, SessionID: p.SessionID,
			}, identity, resume)
		}
		if err != nil {
			return c.failState("failed", err, collaborationErrorRetryable(err)), err
		}
		return c.installConnection(conn)
	}
	c.syncConnection(ctx, conn)
	return c.snapshot(), nil
}

func (c *desktopCollaboration) startAgent(ctx context.Context, input StartCollaborationAgentInput) (CollaborationActionResult, error) {
	requestID := strings.TrimSpace(input.RequestID)
	sessionID := strings.TrimSpace(input.SessionID)
	instruction := strings.TrimSpace(input.Instruction)
	if requestID == "" || sessionID == "" || instruction == "" {
		return CollaborationActionResult{}, fmt.Errorf("requestId, sessionId, and instruction are required")
	}
	fingerprint := collaborationStartFingerprint(input)
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	if existing := c.starts[requestID]; existing.RunID != "" {
		c.mu.Unlock()
		if existing.Fingerprint != fingerprint {
			return CollaborationActionResult{}, fmt.Errorf("requestId %q was already used with different Agent input", requestID)
		}
		return CollaborationActionResult{RequestID: requestID, RunID: existing.RunID, Duplicate: true}, nil
	}
	state := cloneCollaborationState(c.state)
	if state.SessionID != sessionID {
		c.mu.Unlock()
		return CollaborationActionResult{}, fmt.Errorf("sessionId does not match this member's Personal Agent")
	}
	if strings.TrimSpace(state.Room) == "" || strings.TrimSpace(state.MemberID) == "" || strings.TrimSpace(state.AgentID) == "" {
		c.mu.Unlock()
		return CollaborationActionResult{}, fmt.Errorf("collaboration Room has no cached identity; join it once before working offline")
	}
	if existingRun := collaborationRunForCommand(state.Snapshot, requestID); existingRun != nil {
		if existingRun.Instruction != instruction || existingRun.RequestRef != strings.TrimSpace(input.AgentRequestID) || !equalStrings(existingRun.ReferenceIDs, input.ReferenceIDs) {
			c.mu.Unlock()
			return CollaborationActionResult{}, fmt.Errorf("requestId %q was already used with different Agent input", requestID)
		}
		c.starts[requestID] = collaborationStartRecord{RunID: existingRun.ID, Fingerprint: fingerprint}
		c.persistLocked()
		c.mu.Unlock()
		return CollaborationActionResult{RequestID: requestID, RunID: existingRun.ID, Duplicate: true}, nil
	}
	if c.runs[sessionID] != nil {
		c.mu.Unlock()
		return CollaborationActionResult{
			RequestID: requestID,
			Code:      "agent_busy",
			Retryable: true,
			Error:     "Personal Agent already has a collaboration run",
		}, nil
	}
	c.mu.Unlock()

	ready := true
	var err error
	if c.agentReady != nil {
		ready, err = c.agentReady(sessionID)
	} else if c.validateAgent != nil {
		err = c.validateAgent(sessionID)
	}
	if err != nil {
		return CollaborationActionResult{}, err
	}
	contextText, err := collaborationContext(state.Snapshot, input.ReferenceIDs)
	if err != nil {
		return CollaborationActionResult{}, err
	}
	fullInput := instruction
	if contextText != "" {
		fullInput += "\n\n" + contextText
	}
	runID := stableCollaborationID("run", state.MemberID+"\x00"+requestID)
	run := &collaborationAgentRun{
		Room:           state.Room,
		MemberID:       state.MemberID,
		AgentID:        state.AgentID,
		RunID:          runID,
		CommandID:      requestID,
		SessionID:      sessionID,
		AgentRequestID: strings.TrimSpace(input.AgentRequestID),
		Instruction:    instruction,
		ReferenceIDs:   append([]string(nil), input.ReferenceIDs...),
		Updates:        make(chan collaborationRunUpdate, 32),
	}
	c.mu.Lock()
	c.starts[requestID] = collaborationStartRecord{RunID: runID, Fingerprint: fingerprint}
	c.runs[sessionID] = run
	c.persistLocked()
	c.mu.Unlock()

	queued, err := c.publishRun(ctx, run, collab.RunQueued, "", "")
	if err != nil {
		c.mu.Lock()
		delete(c.starts, requestID)
		delete(c.runs, sessionID)
		c.persistLocked()
		c.mu.Unlock()
		return CollaborationActionResult{}, err
	}
	queued.RunID = runID
	queued.RequestID = requestID
	if !ready {
		queued.Queued = true
		go c.resumeQueuedAgent(run, fullInput)
		return queued, nil
	}
	if err := c.launchAgent(ctx, run, fullInput); err != nil {
		return CollaborationActionResult{}, err
	}
	return queued, nil
}

func (c *desktopCollaboration) resumeQueuedAgent(run *collaborationAgentRun, fullInput string) {
	if run == nil || c.waitAgentReady == nil {
		return
	}
	ctx := context.Background()
	if c.app != nil {
		ctx = c.app.bootContext()
	}
	if err := c.waitAgentReady(ctx, run.SessionID); err != nil {
		if ctx.Err() == nil {
			c.failAgentRun(ctx, run, err)
		}
		return
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.RLock()
	current := c.runs[run.SessionID]
	c.mu.RUnlock()
	if current != run {
		return
	}
	_ = c.launchAgent(ctx, run, fullInput)
}

func (c *desktopCollaboration) launchAgent(ctx context.Context, run *collaborationAgentRun, fullInput string) error {
	if _, err := c.publishRun(ctx, run, collab.RunRunning, "", ""); err != nil {
		c.failAgentRun(ctx, run, err)
		return err
	}
	if err := c.submitAgent(run.SessionID, run.Instruction, fullInput); err != nil {
		c.failAgentRun(ctx, run, err)
		return err
	}
	go c.runPublisher(run)
	return nil
}

func (c *desktopCollaboration) failAgentRun(ctx context.Context, run *collaborationAgentRun, err error) {
	if run == nil || err == nil {
		return
	}
	c.mu.Lock()
	if c.runs[run.SessionID] == run {
		delete(c.runs, run.SessionID)
	}
	c.persistLocked()
	c.mu.Unlock()
	_, _ = c.publishRun(ctx, run, collab.RunFailed, "", sanitizeCollaborationText(err.Error()))
}

func (c *desktopCollaboration) respond(ctx context.Context, input RespondCollaborationRequestInput) (CollaborationActionResult, error) {
	requestID := strings.TrimSpace(input.RequestID)
	requestRef := strings.TrimSpace(input.AgentRequestID)
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action == "accept" {
		action = string(collab.RequestAccepted)
	}
	if action == "reject" {
		action = string(collab.RequestRejected)
	}
	if requestID == "" || requestRef == "" || (action != string(collab.RequestAccepted) && action != string(collab.RequestRejected)) {
		return CollaborationActionResult{}, fmt.Errorf("requestId, agentRequestId, and accept/reject action are required")
	}
	state := c.snapshot()
	request := collaborationAgentRequest(state.Snapshot, requestRef)
	if request == nil {
		return CollaborationActionResult{}, fmt.Errorf("agent request %q does not exist", requestRef)
	}
	if request.TargetMemberID != state.MemberID {
		return CollaborationActionResult{}, fmt.Errorf("agent request is assigned to another member")
	}
	result, err := c.submit(ctx, requestID, collab.Command{
		Type: collab.CommandDecideAgentRequest,
		RequestDecision: &collab.DecideAgentRequestInput{
			AgentRequestID: requestRef,
			Decision:       collab.AgentRequestStatus(action),
			Note:           strings.TrimSpace(input.Instruction),
		},
	})
	if err != nil || result.Queued || action == string(collab.RequestRejected) {
		return result, err
	}
	instruction := strings.TrimSpace(input.Instruction)
	if instruction == "" {
		instruction = request.Instruction
	}
	started, err := c.startAgent(ctx, StartCollaborationAgentInput{
		RequestID:      stableCollaborationID("agent_command", requestRef+"\x00"+state.MemberID),
		SessionID:      input.SessionID,
		Instruction:    instruction,
		ReferenceIDs:   request.ReferenceIDs,
		AgentRequestID: requestRef,
	})
	if err != nil {
		return CollaborationActionResult{RequestID: requestID, Receipt: result.Receipt, Retryable: true, Error: err.Error()}, err
	}
	started.Receipt = result.Receipt
	return started, nil
}

func (c *desktopCollaboration) emitState() {
	if c.app == nil || c.app.ctx == nil {
		return
	}
	c.app.runtimeEvents.Emit(c.app.ctx, collaborationStateChannel, c.snapshot())
}

func (c *desktopCollaboration) localIdentity(memberID, memberName, memberRole, agentID, agentName, agentRole, sessionID, room string) (collab.MemberDescriptor, error) {
	sessionID = strings.TrimSpace(sessionID)
	memberName = strings.TrimSpace(memberName)
	room = strings.TrimSpace(room)
	if sessionID == "" || memberName == "" || room == "" {
		return collab.MemberDescriptor{}, fmt.Errorf("room, memberName, and explicit sessionId are required")
	}
	if err := c.validateAgent(sessionID); err != nil {
		return collab.MemberDescriptor{}, err
	}
	if strings.TrimSpace(memberID) == "" {
		memberID = stableCollaborationID("member", room+"\x00"+sessionID)
	}
	if strings.TrimSpace(agentID) == "" {
		agentID = stableCollaborationID("agent", room+"\x00"+sessionID)
	}
	if strings.TrimSpace(agentName) == "" {
		agentName = memberName + " Agent"
	}
	return collab.MemberDescriptor{
		ID:   strings.TrimSpace(memberID),
		Name: memberName,
		Role: strings.TrimSpace(memberRole),
		Agent: collab.AgentDescriptor{
			ID:     strings.TrimSpace(agentID),
			Name:   strings.TrimSpace(agentName),
			Role:   strings.TrimSpace(agentRole),
			Status: collab.AgentIdle,
		},
	}, nil
}

func (c *desktopCollaboration) validateLocalController(sessionID string) error {
	if c.app == nil {
		return fmt.Errorf("desktop application is unavailable")
	}
	_, err := c.app.collaborationAgentReady(sessionID)
	return err
}

func (a *App) collaborationAgentReady(sessionID string) (bool, error) {
	if a == nil {
		return false, fmt.Errorf("desktop application is unavailable")
	}
	tab := a.sessionByID(sessionID)
	if tab == nil {
		return false, fmt.Errorf("session %q does not exist", sessionID)
	}
	a.mu.RLock()
	ctrl := tab.Ctrl
	readOnly := tab.ReadOnly
	startupErr := strings.TrimSpace(tab.StartupErr)
	ready := tab.Ready
	a.mu.RUnlock()
	if readOnly {
		return false, readOnlyChannelErr()
	}
	if ctrl == nil {
		if startupErr != "" {
			return false, fmt.Errorf("workspace failed to start: %s", startupErr)
		}
		if ready {
			return false, fmt.Errorf("session controller is unavailable after startup")
		}
		return false, nil
	}
	if ctrl.RuntimeStatus().ActiveRuntimeWork {
		return false, fmt.Errorf("Personal Agent is already running")
	}
	return true, nil
}

func (a *App) waitCollaborationAgentReady(ctx context.Context, sessionID string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		ready, err := a.collaborationAgentReady(sessionID)
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func stableCollaborationID(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + "_" + hex.EncodeToString(sum[:12])
}

func (c *desktopCollaboration) setConnecting(mode, host string, port int, room string, identity collab.MemberDescriptor, sessionID string) {
	c.mu.Lock()
	config := normalizeCollaborationAgentConfig(c.state.AgentConfig, identity.Agent.Name)
	c.conn = nil // fence old connection so late stream/snapshot updates cannot overwrite the new state
	c.state = CollaborationState{
		Status:      "connecting",
		Mode:        mode,
		Host:        strings.TrimSpace(host),
		Port:        port,
		Room:        strings.TrimSpace(room),
		MemberID:    identity.ID,
		AgentID:     identity.Agent.ID,
		SessionID:   strings.TrimSpace(sessionID),
		OutboxCount: len(c.outbox),
		AgentConfig: config,
	}
	c.mu.Unlock()
	c.emitState()
}

// fenceCurrentConnection cancels the previous connection's context so its
// goroutines stop delivering stale events before a new Host/Join starts.
// Must be called while c.opMu is held.
func (c *desktopCollaboration) fenceCurrentConnection() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = conn.close(closeCtx, true)
	cancel()
}

func (c *desktopCollaboration) failState(status string, err error, retryable bool) CollaborationState {
	c.mu.Lock()
	c.state.Status = status
	c.state.LastError = err.Error()
	c.state.Retryable = retryable
	state := cloneCollaborationState(c.state)
	c.mu.Unlock()
	c.emitState()
	return state
}

func (c *desktopCollaboration) installConnection(conn *collaborationConnection) (CollaborationState, error) {
	if conn == nil || conn.peer == nil {
		return c.snapshot(), fmt.Errorf("collaboration connection is unavailable")
	}
	c.mu.Lock()
	config := normalizeCollaborationAgentConfig(c.state.AgentConfig, conn.agentName)
	previous := c.conn
	c.conn = conn
	status := "connected"
	if conn.rejoined || len(c.outbox) > 0 || len(c.recoveredRuns) > 0 {
		status = "syncing"
	}
	c.state = CollaborationState{
		Status:      status,
		Mode:        conn.mode,
		Host:        conn.hostName,
		Port:        conn.port,
		Room:        conn.room,
		MemberID:    conn.memberID,
		AgentID:     conn.agentID,
		SessionID:   conn.sessionID,
		Snapshot:    conn.initialSnapshot,
		OutboxCount: len(c.outbox),
		AgentConfig: config,
	}
	c.recoverInterruptedRunsLocked(conn)
	c.state.OutboxCount = len(c.outbox)
	c.persistLocked()
	state := cloneCollaborationState(c.state)
	c.mu.Unlock()
	if previous != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = previous.close(closeCtx, true)
		cancel()
	}
	loopCtx, cancel := context.WithCancel(c.app.bootContext())
	conn.cancel = cancel
	conn.done = make(chan struct{})
	go c.connectionLoop(loopCtx, conn)
	go c.restoreFileOrigins(conn)
	c.emitState()
	return state, nil
}
