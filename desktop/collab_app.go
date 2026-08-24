package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/fileutil"
)

const (
	collaborationStateChannel  = "collaboration:state"
	collaborationEventChannel  = "collaboration:event"
	maxCollaborationAgentQueue = 20
	collaborationProtocolV1    = 1
	collaborationProtocolV2    = 2
)

// CollaborationState is the authoritative desktop projection consumed by the
// collaboration surface. Connection credentials are intentionally omitted.
type CollaborationState struct {
	Status           string                           `json:"status"`
	Mode             string                           `json:"mode,omitempty"`
	Host             string                           `json:"host,omitempty"`
	Port             int                              `json:"port,omitempty"`
	Room             string                           `json:"room,omitempty"`
	MemberID         string                           `json:"memberId,omitempty"`
	AgentID          string                           `json:"agentId,omitempty"`
	SessionID        string                           `json:"sessionId,omitempty"`
	Snapshot         collab.Snapshot                  `json:"snapshot"`
	OutboxCount      int                              `json:"outboxCount"`
	Outbox           []CollaborationOutboxView        `json:"outbox,omitempty"`
	LastError        string                           `json:"lastError,omitempty"`
	Retryable        bool                             `json:"retryable,omitempty"`
	Transfers        []CollaborationFileTransfer      `json:"transfers,omitempty"`
	AgentConfig      CollaborationAgentConfig         `json:"agentConfig"`
	AgentSources     CollaborationAgentSources        `json:"agentSources"`
	QueuedTasks      []CollaborationQueuedTask        `json:"queuedTasks,omitempty"`
	ToolApprovalMode string                           `json:"toolApprovalMode"`
	AgentPrompt      *CollaborationAgentPrompt        `json:"agentPrompt,omitempty"`
	Routes           []CollaborationRouteState        `json:"routes,omitempty"`
	Advertisement    *CollaborationAdvertisementState `json:"advertisement,omitempty"`
	CurrentRun       *CollaborationCurrentRun         `json:"currentRun,omitempty"`
	ProtocolVersion  int                              `json:"protocolVersion,omitempty"`
}

// CollaborationCurrentRun is the local owner-only projection of the Agent
// currently using this workspace. It is never synchronized through the Room.
type CollaborationCurrentRun struct {
	SessionID   string `json:"sessionId"`
	RunID       string `json:"runId"`
	Phase       string `json:"phase"`
	Instruction string `json:"instruction"`
	Progress    string `json:"progress,omitempty"`
	StartedAt   int64  `json:"startedAt,omitempty"`
	QueueCount  int    `json:"queueCount"`
}

type CollaborationRouteInput struct {
	ID              string `json:"id,omitempty"`
	Kind            string `json:"kind"`
	Host            string `json:"host,omitempty"`
	Port            int    `json:"port,omitempty"`
	RelayID         string `json:"relayId,omitempty"`
	URL             string `json:"url,omitempty"`
	TunnelID        string `json:"tunnelId,omitempty"`
	GuestCapability string `json:"guestCapability,omitempty"`
	Priority        int    `json:"priority,omitempty"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
}

type CollaborationRouteState struct {
	CollaborationRouteInput
	Status    string `json:"status"`
	Active    bool   `json:"active"`
	LatencyMS int64  `json:"latencyMs,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type RoomAdvertisementInput struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	Capacity        int      `json:"capacity,omitempty"`
	ShowOnlineCount bool     `json:"showOnlineCount,omitempty"`
}

type CollaborationAdvertisementRelayState struct {
	RelayID   string `json:"relayId"`
	Status    string `json:"status"`
	LastError string `json:"lastError,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type CollaborationAdvertisementState struct {
	Visibility string                                 `json:"visibility"`
	Revision   uint64                                 `json:"revision"`
	Relays     []CollaborationAdvertisementRelayState `json:"relays"`
}

type CollaborationQueuedTask struct {
	ID             string   `json:"id"`
	RequestID      string   `json:"requestId"`
	Instruction    string   `json:"instruction"`
	ReferenceIDs   []string `json:"referenceIds,omitempty"`
	AgentRequestID string   `json:"agentRequestId,omitempty"`
	QueuedAt       string   `json:"queuedAt"`
}

type CollaborationAgentConfig struct {
	Alias                        string   `json:"alias,omitempty"`
	AutoRespondQuestions         bool     `json:"autoRespondQuestions"`
	AutoRespondRequests          bool     `json:"autoRespondRequests"`
	AutoRespondAgents            bool     `json:"autoRespondAgents"`
	AgentResponseIntervalSeconds int      `json:"agentResponseIntervalSeconds"`
	AgentClockTurns              int      `json:"agentClockTurns"`
	AgentClockUnlimited          bool     `json:"agentClockUnlimited"`
	AgentClockWoundAt            string   `json:"agentClockWoundAt,omitempty"`
	RecognitionMode              string   `json:"recognitionMode"`
	ContextRefs                  []string `json:"contextRefs,omitempty"`
}

type CollaborationAgentSource struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope,omitempty"`
	RunAs       string `json:"runAs,omitempty"`
	Protected   bool   `json:"protected,omitempty"`
	Available   bool   `json:"available"`
}

type CollaborationAgentSources struct {
	Agents []CollaborationAgentSource `json:"agents"`
	Skills []CollaborationAgentSource `json:"skills"`
}

type UpdateCollaborationAgentConfigInput struct {
	SessionID string                   `json:"sessionId"`
	RequestID string                   `json:"requestId"`
	Config    CollaborationAgentConfig `json:"config"`
}

type UpdateCollaborationProfileInput struct {
	SessionID    string `json:"sessionId"`
	RequestID    string `json:"requestId"`
	MemberName   string `json:"memberName"`
	MemberAvatar string `json:"memberAvatar,omitempty"`
	AgentName    string `json:"agentName"`
	AgentAvatar  string `json:"agentAvatar,omitempty"`
}

type CollaborationFileTransfer struct {
	ID            string `json:"id"`
	FileID        string `json:"fileId"`
	Room          string `json:"room,omitempty"`
	RoomInstance  string `json:"roomInstance,omitempty"`
	OwnerID       string `json:"ownerId,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	ManifestHash  string `json:"manifestHash,omitempty"`
	ChunkSize     int64  `json:"chunkSize,omitempty"`
	ChunkCount    int    `json:"chunkCount,omitempty"`
	OfferRevision uint64 `json:"offerRevision,omitempty"`
	Direction     string `json:"direction"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	Transferred   int64  `json:"transferred"`
	Total         int64  `json:"total"`
	Destination   string `json:"destination,omitempty"`
	Error         string `json:"error,omitempty"`
	Retryable     bool   `json:"retryable,omitempty"`
	Completed     []bool `json:"completed,omitempty"`
	PartPath      string `json:"partPath,omitempty"`
	Automatic     bool   `json:"automatic,omitempty"`
	PausedByUser  bool   `json:"pausedByUser,omitempty"`
	AutoBlocked   bool   `json:"autoBlocked,omitempty"`
	AutoAttempts  int    `json:"autoAttempts,omitempty"`
	WorkspacePath string `json:"workspacePath,omitempty"`
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
	Version int                       `json:"version,omitempty"`
	Invite  string                    `json:"invite,omitempty"`
	Hosts   []string                  `json:"hosts"`
	Port    int                       `json:"port"`
	Room    string                    `json:"room"`
	Token   string                    `json:"token,omitempty"`
	HostKey string                    `json:"hostKey,omitempty"`
	Routes  []CollaborationRouteInput `json:"routes,omitempty"`
}

type HostCollaborationRoomInput struct {
	ListenHost      string                  `json:"listenHost"`
	Port            int                     `json:"port"`
	Room            string                  `json:"room"`
	RoomName        string                  `json:"roomName,omitempty"`
	Description     string                  `json:"description,omitempty"`
	Token           string                  `json:"token,omitempty"`
	MemberID        string                  `json:"memberId,omitempty"`
	MemberName      string                  `json:"memberName"`
	MemberAvatar    string                  `json:"memberAvatar,omitempty"`
	MemberRole      string                  `json:"memberRole,omitempty"`
	AgentID         string                  `json:"agentId,omitempty"`
	AgentName       string                  `json:"agentName,omitempty"`
	AgentAvatar     string                  `json:"agentAvatar,omitempty"`
	AgentRole       string                  `json:"agentRole,omitempty"`
	SessionID       string                  `json:"sessionId"`
	LANEnabled      *bool                   `json:"lanEnabled,omitempty"`
	RelayIDs        []string                `json:"relayIDs,omitempty"`
	PreferLAN       *bool                   `json:"preferLAN,omitempty"`
	Visibility      string                  `json:"visibility,omitempty"`
	Advertisement   *RoomAdvertisementInput `json:"advertisement,omitempty"`
	ProtocolVersion int                     `json:"protocolVersion,omitempty"`
}

type JoinCollaborationRoomInput struct {
	Host         string                    `json:"host"`
	Port         int                       `json:"port"`
	Room         string                    `json:"room"`
	Token        string                    `json:"token,omitempty"`
	MemberID     string                    `json:"memberId,omitempty"`
	MemberName   string                    `json:"memberName"`
	MemberAvatar string                    `json:"memberAvatar,omitempty"`
	MemberRole   string                    `json:"memberRole,omitempty"`
	AgentID      string                    `json:"agentId,omitempty"`
	AgentName    string                    `json:"agentName,omitempty"`
	AgentAvatar  string                    `json:"agentAvatar,omitempty"`
	AgentRole    string                    `json:"agentRole,omitempty"`
	SessionID    string                    `json:"sessionId"`
	Invite       string                    `json:"invite,omitempty"`
	Routes       []CollaborationRouteInput `json:"routes,omitempty"`
	HostKey      string                    `json:"hostKey,omitempty"`
	JoinRef      string                    `json:"joinRef,omitempty"`
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
	MentionMemberIDs []string                `json:"mentionMemberIds,omitempty"`
	MentionAgentIDs  []string                `json:"mentionAgentIds,omitempty"`
}

type StartCollaborationAgentInput struct {
	RequestID      string   `json:"requestId"`
	SessionID      string   `json:"sessionId"`
	Instruction    string   `json:"instruction"`
	ReferenceIDs   []string `json:"referenceIds,omitempty"`
	AgentRequestID string   `json:"agentRequestId,omitempty"`
	Automatic      bool     `json:"automatic,omitempty"`
	// ReadOnly forces the run into a text-only boundary: the executor refuses
	// write tools, commands, and other side-effecting calls at runtime. Set by
	// the scheduler for question-only mode, never by the frontend.
	ReadOnly bool `json:"readOnly,omitempty"`
}

type CancelCollaborationQueuedTaskInput struct {
	SessionID string `json:"sessionId"`
	TaskID    string `json:"taskId"`
}

type RespondCollaborationAgentRunInput struct {
	SessionID string           `json:"sessionId"`
	RunID     string           `json:"runId"`
	Allow     bool             `json:"allow"`
	Session   bool             `json:"session,omitempty"`
	Persist   bool             `json:"persist,omitempty"`
	Answering bool             `json:"answering,omitempty"`
	Answers   []QuestionAnswer `json:"answers,omitempty"`
}

type UpdateCollaborationToolApprovalModeInput struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode"`
}

type CollaborationAgentPromptOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type CollaborationAgentPromptQuestion struct {
	ID      string                           `json:"id"`
	Header  string                           `json:"header,omitempty"`
	Prompt  string                           `json:"prompt"`
	Options []CollaborationAgentPromptOption `json:"options"`
	Multi   bool                             `json:"multi,omitempty"`
}

type CollaborationAgentPrompt struct {
	RunID     string                             `json:"runId"`
	Kind      string                             `json:"kind"`
	ID        string                             `json:"id"`
	Tool      string                             `json:"tool,omitempty"`
	Subject   string                             `json:"subject,omitempty"`
	Reason    string                             `json:"reason,omitempty"`
	Questions []CollaborationAgentPromptQuestion `json:"questions,omitempty"`
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
	Automatic      bool   `json:"automatic,omitempty"`
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
	app                *App
	ownerSessionID     string
	ownerSessionPath   string
	ownerWorkspaceRoot string
	ownerSessionTitle  string
	roomInstance       string
	shareAuthority     string

	opMu               sync.Mutex
	autoReceiveMu      sync.Mutex
	mu                 sync.RWMutex
	state              CollaborationState
	fileOffers         map[string]collab.FileOffer
	fileOffersReady    bool
	conn               *collaborationConnection
	outbox             []collab.CommandEnvelope
	outboxFailures     map[string]string
	starts             map[string]collaborationStartRecord
	runs               map[string]*collaborationAgentRun
	queuedRuns         []*collaborationAgentRun
	queueWaiting       bool
	recoveredRuns      []collaborationPersistedRun
	leaveError         string
	recovery           collaborationPersistedState
	shares             map[string]collaborationSharedFile
	transfers          map[string]*CollaborationFileTransfer
	transferArchive    map[string]*CollaborationFileTransfer
	transferCancel     map[string]context.CancelFunc
	transferRun        map[string]uint64
	transferLocks      map[string]*sync.Mutex
	restoreOnce        sync.Once
	updateOnce         sync.Once
	updateCancel       context.CancelFunc
	updateDone         chan struct{}
	initialUpdateDelay func() time.Duration
	updateDelay        func() time.Duration
	streamRetryDelay   func(int, uint64) time.Duration
	ownedParts         map[string]os.FileInfo
	autoReceiveSem     chan struct{}
	autoRetryDelay     func(int) time.Duration
	autoRetryAfter     map[string]time.Time
	autoRetryTimer     *time.Timer
	autoRetryAt        time.Time
	autoScanActive     bool
	autoScanAgain      bool
	autoScanClosed     bool
	verifiedFiles      map[string]collaborationVerifiedFile
	relayChunkCache    map[string]collaborationRelayChunk
	relayChunkBytes    int64
	relayChunkClock    uint64
	fileOrigin         *collaborationFileOrigin

	persistPath       string
	legacyPersistPath string
	writeState        func(string, []byte, os.FileMode) error
	setSecret         func(string, string) error
	getSecret         func(string) string
	removeSecret      func(string) error
	validateAgent     func(string) error
	agentReady        func(string) (bool, error)
	waitAgentReady    func(context.Context, string) error
	prepareAgentInput func(string, []string, string) (string, error)
	submitAgent       func(string, string, string) error
	respondAgent      func(string, RespondCollaborationAgentRunInput) (bool, error)
	stopAgent         func(string) error
	prepareAutoAgent  func(string) (string, error)
	restoreAutoAgent  func(string, string)
	// prepareAgentReadOnly / restoreAgentReadOnly flip the owning Session's
	// scoped runtime tool gate around a text-only Room Agent run.
	prepareAgentReadOnly func(string) (bool, error)
	restoreAgentReadOnly func(string, bool)
	openHost             func(context.Context, HostCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error)
	openJoin             func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error)

	scheduler       *collaborationScheduler
	schedulerCancel context.CancelFunc
	startAgentHook  func(context.Context, StartCollaborationAgentInput) // test seam
}

type collaborationConnection struct {
	syncMu              sync.Mutex
	failoverMu          sync.Mutex
	failoverActive      bool
	peer                collaborationPeer
	filePeer            collaborationFilePeer
	host                *http.Server
	listener            net.Listener
	mode                string
	roomName            string
	description         string
	hostName            string
	port                int
	room                string
	memberID            string
	memberName          string
	memberAvatar        string
	memberRole          string
	agentID             string
	agentName           string
	agentAvatar         string
	agentRole           string
	sessionID           string
	joinToken           string
	connectionSession   string
	initialSnapshot     collab.Snapshot
	rejoined            bool
	sweep               func(context.Context) error
	cancel              context.CancelFunc
	done                chan struct{}
	closeOnce           sync.Once
	authority           *collaborationAuthority
	routes              []CollaborationRouteState
	routeError          string
	hostKey             string
	relayBindings       []collaborationRelayBinding
	advertisement       *CollaborationAdvertisementState
	lanEnabled          bool
	relayIDs            []string
	preferLAN           bool
	authorityKeyRef     string
	hostCapabilityRefs  map[string]string
	guestCapabilityRefs map[string]string
	protocolVersion     int
	roomInstanceKey     string
	shareAuthorityKey   string
	releaseLAN          func()
}

type collaborationRelayBinding interface {
	Close(context.Context) error
}

type collaborationPeer interface {
	Snapshot(context.Context) (collab.Snapshot, error)
	Events(context.Context, uint64) ([]collab.RoomEvent, error)
	Stream(context.Context, uint64, func(collab.RoomEvent) error) error
	Submit(context.Context, collab.CommandEnvelope) (collab.CommandReceipt, error)
	Heartbeat(context.Context, string) error
	Leave(context.Context, string) error
}

type collaborationFilePeer interface {
	RegisterFileOrigin(context.Context, string, collab.RegisterFileOriginInput) error
	fileTicket(context.Context, string) (collab.FileTransferTicket, error)
	fetchFileManifest(context.Context, string, int64, bool) (collab.FileTransferTicket, collaborationFileManifest, error)
	fetchFileChunk(context.Context, collab.FileTransferTicket, int) ([]byte, error)
}

type collaborationAgentRun struct {
	Room             string
	MemberID         string
	AgentID          string
	RunID            string
	CommandID        string
	SessionID        string
	AgentRequestID   string
	Instruction      string
	ReferenceIDs     []string
	ContextRefs      []string
	QueuedAt         string
	StartedAt        time.Time
	Text             strings.Builder
	PublishIndex     int
	Updates          chan collaborationRunUpdate
	Automatic        bool
	RestoreMode      string
	ReadOnly         bool
	RestoreReadOnly  bool
	ReadOnlyPrepared bool
	PromptOpen       bool
	Prompt           *CollaborationAgentPrompt
	StopRequested    bool
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
	Mode                  string                              `json:"mode,omitempty"`
	Host                  string                              `json:"host,omitempty"`
	Port                  int                                 `json:"port,omitempty"`
	Room                  string                              `json:"room,omitempty"`
	MemberID              string                              `json:"memberId,omitempty"`
	AgentID               string                              `json:"agentId,omitempty"`
	SessionID             string                              `json:"sessionId,omitempty"`
	SessionPath           string                              `json:"sessionPath,omitempty"`
	RoomName              string                              `json:"roomName,omitempty"`
	Description           string                              `json:"description,omitempty"`
	MemberName            string                              `json:"memberName,omitempty"`
	MemberAvatar          string                              `json:"memberAvatar,omitempty"`
	MemberRole            string                              `json:"memberRole,omitempty"`
	AgentName             string                              `json:"agentName,omitempty"`
	AgentAvatar           string                              `json:"agentAvatar,omitempty"`
	AgentRole             string                              `json:"agentRole,omitempty"`
	ConnectionSecretRef   string                              `json:"connectionSecretRef,omitempty"`
	JoinTokenSecretRef    string                              `json:"joinTokenSecretRef,omitempty"`
	AfterSequence         uint64                              `json:"afterSequence,omitempty"`
	Snapshot              collab.Snapshot                     `json:"snapshot,omitempty"`
	Outbox                []collab.CommandEnvelope            `json:"outbox,omitempty"`
	OutboxFailures        map[string]string                   `json:"outboxFailures,omitempty"`
	Starts                map[string]collaborationStartRecord `json:"starts,omitempty"`
	Runs                  []collaborationPersistedRun         `json:"runs,omitempty"`
	Queue                 []collaborationPersistedRun         `json:"queue,omitempty"`
	Shares                []collaborationSharedFile           `json:"shares,omitempty"`
	Transfers             []CollaborationFileTransfer         `json:"transfers,omitempty"`
	AgentConfig           CollaborationAgentConfig            `json:"agentConfig,omitempty"`
	LANEnabled            bool                                `json:"lanEnabled,omitempty"`
	ReachabilityVersion   int                                 `json:"reachabilityVersion,omitempty"`
	RelayIDs              []string                            `json:"relayIds,omitempty"`
	PreferLAN             bool                                `json:"preferLan,omitempty"`
	Routes                []CollaborationRouteState           `json:"routes,omitempty"`
	LastRouteID           string                              `json:"lastRouteId,omitempty"`
	AuthorityKeySecretRef string                              `json:"authorityKeySecretRef,omitempty"`
	HostCapabilityRefs    map[string]string                   `json:"hostCapabilityRefs,omitempty"`
	GuestCapabilityRefs   map[string]string                   `json:"guestCapabilityRefs,omitempty"`
	HostKey               string                              `json:"hostKey,omitempty"`
	Advertisement         *CollaborationAdvertisementState    `json:"advertisement,omitempty"`
	ProtocolVersion       int                                 `json:"protocolVersion,omitempty"`
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
	ContextRefs    []string `json:"contextRefs,omitempty"`
	QueuedAt       string   `json:"queuedAt,omitempty"`
	PublishIndex   int      `json:"publishIndex,omitempty"`
	Automatic      bool     `json:"automatic,omitempty"`
	ReadOnly       bool     `json:"readOnly,omitempty"`
}

func newDesktopCollaboration(app *App, sessionID string) *desktopCollaboration {
	sessionID = strings.TrimSpace(sessionID)
	sessionPath, sessionTitle, workspaceRoot := collaborationSessionOwner(app, sessionID)
	return newDesktopCollaborationForSession(app, sessionID, sessionPath, sessionTitle, workspaceRoot)
}

func collaborationSessionOwner(app *App, sessionID string) (sessionPath, sessionTitle, workspaceRoot string) {
	if app == nil {
		return "", "", ""
	}
	if tab := app.sessionByID(sessionID); tab != nil {
		app.mu.RLock()
		sessionPath = collaborationOwnerSessionPath(tab.currentSessionPath())
		sessionTitle = strings.TrimSpace(tab.TopicTitle)
		workspaceRoot = normalizeProjectRoot(tab.WorkspaceRoot)
		app.mu.RUnlock()
	}
	return sessionPath, sessionTitle, workspaceRoot
}

// newDesktopCollaborationForSession also supports a persisted Room whose tab
// is not currently mounted. Room caches are keyed by the durable Session path,
// so restoring with only the Session ID would silently read a different file
// and leave the maintenance/unread runtime empty.
func newDesktopCollaborationForSession(app *App, sessionID, sessionPath, sessionTitle, workspaceRoot string) *desktopCollaboration {
	sessionID = strings.TrimSpace(sessionID)
	sessionPath = collaborationOwnerSessionPath(sessionPath)
	c := &desktopCollaboration{
		app:                app,
		ownerSessionID:     sessionID,
		ownerSessionPath:   sessionPath,
		ownerWorkspaceRoot: workspaceRoot,
		ownerSessionTitle:  sessionTitle,
		state:              CollaborationState{Status: "disconnected", SessionID: sessionID, AgentConfig: defaultCollaborationAgentConfig()},
		starts:             map[string]collaborationStartRecord{},
		runs:               map[string]*collaborationAgentRun{},
		shares:             map[string]collaborationSharedFile{},
		transfers:          map[string]*CollaborationFileTransfer{},
		transferArchive:    map[string]*CollaborationFileTransfer{},
		transferCancel:     map[string]context.CancelFunc{},
		transferRun:        map[string]uint64{},
		transferLocks:      map[string]*sync.Mutex{},
		ownedParts:         map[string]os.FileInfo{},
		autoReceiveSem:     make(chan struct{}, 2),
		autoRetryAfter:     map[string]time.Time{},
		verifiedFiles:      map[string]collaborationVerifiedFile{},
		outboxFailures:     map[string]string{},
	}
	c.openHost = c.openHostedRoom
	c.openJoin = c.openJoinedRoom
	c.writeState = fileutil.AtomicWriteFile
	c.setSecret = func(key, value string) error { _, err := config.SetCredential(key, value); return err }
	c.getSecret = func(key string) string { return config.ResolveCredential(key).Value }
	c.removeSecret = config.RemoveCredential
	c.validateAgent = c.validateCollaborationIdentity
	c.agentReady = app.collaborationAgentReady
	c.waitAgentReady = app.waitCollaborationAgentReady
	c.prepareAgentInput = app.prepareCollaborationAgentInput
	c.submitAgent = func(sessionID, display, input string) error {
		tab, ctrl := app.sessionAndCtrl(sessionID)
		if tab == nil || ctrl == nil {
			return fmt.Errorf("collaboration Agent workspace is not ready")
		}
		return app.SubmitDisplayToTab(tab.ID, display, input)
	}
	c.respondAgent = app.respondCollaborationAgent
	c.stopAgent = func(sessionID string) error {
		_, ctrl := app.sessionAndCtrl(sessionID)
		if ctrl == nil {
			return fmt.Errorf("collaboration Agent workspace is not ready")
		}
		ctrl.Cancel()
		return nil
	}
	c.prepareAutoAgent = app.prepareCollaborationAutoAgent
	c.restoreAutoAgent = app.restoreCollaborationAutoAgent
	c.prepareAgentReadOnly = app.prepareCollaborationAgentReadOnly
	c.restoreAgentReadOnly = app.restoreCollaborationAgentReadOnly
	c.scheduler = newCollaborationScheduler()
	root := strings.TrimSpace(config.MemoryUserDir())
	if root != "" {
		c.legacyPersistPath = filepath.Join(root, "desktop-collaboration-v1.json")
		stateDir := filepath.Join(root, "desktop-collaboration-v2")
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			c.state.LastError = "create collaboration state directory: " + err.Error()
			c.state.Retryable = true
		} else {
			c.persistPath = filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey(sessionID, sessionPath)))
			c.loadPersisted()
		}
	}
	c.observeUnread()
	return c
}

// collaborationOwnerSessionPath keeps a conflict-recovery branch attached to
// the Room owned by its original logical Session. Recovery branches are an
// implementation detail of durable session saving; opening one must not make
// the user's hosted Room appear to belong to a different Session.
func collaborationOwnerSessionPath(sessionPath string) string {
	current := sessionRuntimeKey(sessionPath)
	if current == "" {
		return ""
	}
	for range agent.SessionRecoveryMaxDepth {
		meta, ok, err := agent.LoadBranchMeta(current)
		if err != nil || !ok || !meta.Recovered {
			break
		}
		parentID := strings.TrimSpace(meta.ParentID)
		if parentID == "" || parentID != filepath.Base(parentID) || parentID == "." || parentID == ".." {
			break
		}
		parent := sessionRuntimeKey(filepath.Join(filepath.Dir(current), parentID+".jsonl"))
		if parent == "" || parent == current {
			break
		}
		if _, err := os.Stat(parent); err != nil {
			break
		}
		current = parent
	}
	return current
}

func collaborationPersistenceKey(sessionID, sessionPath string) string {
	if key := sessionRuntimeKey(sessionPath); key != "" {
		return "session-path:" + key
	}
	return strings.TrimSpace(sessionID)
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
	sessionPath, sessionTitle, workspaceRoot := collaborationSessionOwner(a, sessionID)
	a.collaborationMu.Lock()
	if a.collaborations == nil {
		a.collaborations = make(map[string]*desktopCollaboration)
	}
	if runtime := a.collaborations[sessionID]; runtime != nil {
		a.collaborationMu.Unlock()
		return runtime, nil
	}
	var replaced []*desktopCollaboration
	if sessionPath != "" {
		seen := make(map[*desktopCollaboration]bool)
		for ownerID, runtime := range a.collaborations {
			if runtime == nil || runtime.ownerSessionPath != sessionPath {
				continue
			}
			delete(a.collaborations, ownerID)
			if !seen[runtime] {
				seen[runtime] = true
				replaced = append(replaced, runtime)
			}
		}
	}
	runtime := newDesktopCollaborationForSession(a, sessionID, sessionPath, sessionTitle, workspaceRoot)
	a.collaborations[sessionID] = runtime
	a.collaborationMu.Unlock()
	for _, stale := range replaced {
		stale.close()
	}
	runtime.startUpdateLoop(a.bootContext())
	return runtime, nil
}

// collaborationRuntimeForPersisted restores an unmounted Room from its
// SessionPath-keyed cache. The project-tree sidecar is the authority that this
// cache still belongs to a live local Room; incomplete/orphaned historical
// cache files remain ignored.
func (a *App) collaborationRuntimeForPersisted(p collaborationPersistedState) (*desktopCollaboration, error) {
	sessionID := strings.TrimSpace(p.SessionID)
	sessionPath := collaborationOwnerSessionPath(p.SessionPath)
	if sessionID == "" || sessionPath == "" {
		return nil, fmt.Errorf("persisted collaboration Session identity is incomplete")
	}
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("load persisted collaboration Session: %w", err)
	}
	if !ok || meta.SessionKind != agent.SessionKindCollaboration {
		return nil, errCollaborationSessionUnregistered
	}

	a.collaborationMu.Lock()
	defer a.collaborationMu.Unlock()
	if a.collaborations == nil {
		a.collaborations = make(map[string]*desktopCollaboration)
	}
	if runtime := a.collaborations[sessionID]; runtime != nil {
		return runtime, nil
	}
	for _, runtime := range a.collaborations {
		if runtime != nil && runtime.ownerSessionPath == sessionPath {
			return runtime, nil
		}
	}
	runtime := newDesktopCollaborationForSession(
		a,
		sessionID,
		sessionPath,
		firstNonEmpty(strings.TrimSpace(meta.CustomTitle), strings.TrimSpace(meta.TopicTitle), strings.TrimSpace(p.RoomName)),
		normalizeProjectRoot(meta.WorkspaceRoot),
	)
	a.collaborations[sessionID] = runtime
	runtime.startUpdateLoop(a.bootContext())
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = a.closeCollaborationLAN(ctx)
	cancel()
}

// collaborationSessionPathsForTopic snapshots the live Room runtimes owned by
// one topic before that topic's metadata is removed. Branch metadata is read
// outside collaborationMu; runtime shutdown likewise happens outside the lock.
func (a *App) collaborationSessionPathsForTopic(topicID string) []string {
	topicID = strings.TrimSpace(topicID)
	if a == nil || topicID == "" {
		return nil
	}
	a.collaborationMu.Lock()
	paths := make([]string, 0, len(a.collaborations))
	for _, runtime := range a.collaborations {
		if runtime != nil {
			paths = append(paths, runtime.ownerSessionPath)
		}
	}
	a.collaborationMu.Unlock()

	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = collaborationOwnerSessionPath(path)
		if path == "" || seen[path] {
			continue
		}
		meta, ok, err := agent.LoadBranchMeta(path)
		if err != nil || !ok || meta.SessionKind != agent.SessionKindCollaboration || strings.TrimSpace(meta.TopicID) != topicID {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

// closeCollaborationRuntimesForSessionPaths removes matching Room runtimes
// from the App before closing them. Repeated calls are safe, and close never
// runs while collaborationMu is held, so a transport shutdown cannot block
// unrelated Room lookup or create a lock-order cycle.
func (a *App) closeCollaborationRuntimesForSessionPaths(sessionPaths []string) {
	if a == nil || len(sessionPaths) == 0 {
		return
	}
	targets := map[string]bool{}
	for _, path := range sessionPaths {
		if key := sessionRuntimeKey(collaborationOwnerSessionPath(path)); key != "" {
			targets[key] = true
		}
	}
	if len(targets) == 0 {
		return
	}

	a.collaborationMu.Lock()
	seen := map[*desktopCollaboration]bool{}
	runtimes := make([]*desktopCollaboration, 0, len(targets))
	for sessionID, runtime := range a.collaborations {
		if runtime == nil || !targets[sessionRuntimeKey(runtime.ownerSessionPath)] {
			continue
		}
		delete(a.collaborations, sessionID)
		if !seen[runtime] {
			seen[runtime] = true
			runtimes = append(runtimes, runtime)
		}
	}
	a.collaborationMu.Unlock()

	for _, runtime := range runtimes {
		runtime.close()
	}
}

// restoreCollaborationRuntimes scans the persisted collaboration states on
// disk and reinstantiates a desktopCollaboration runtime for each one, then
// attempts an async reconnection. This is called during Desktop startup so
// inactive collaboration Sessions resume their Personal Agent scheduler
// without requiring the Room tab to be mounted first.
//
// Each Session starts independently; a single failure does not block others.
// Errors are recorded in the runtime state and surfaced to the frontend when
// the user eventually opens that Room tab.
func (a *App) restoreCollaborationRuntimes() {
	available, err := collaborationPersistedStatesAvailable()
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "collaboration restore: inspect state dir: %v\n", err)
		}
		return
	}
	if !available {
		return
	}
	a.collaborationReconcileMu.Lock()
	a.collaborationReconcileEnabled = true
	a.collaborationReconcileMu.Unlock()
	a.reconcileCollaborationRuntimes()
}

func collaborationPersistedStatesAvailable() (bool, error) {
	root := strings.TrimSpace(config.MemoryUserDir())
	if root == "" {
		return false, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, "desktop-collaboration-v2"))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			return true, nil
		}
	}
	return false, nil
}

// scheduleCollaborationRuntimeReconcile coalesces project-tree changes and
// late session-directory cache fills. A cold ListProjectTree call may return a
// partial tree after its bounded timeout; the successful background load asks
// for another authority scan so a Host Room cannot remain dormant until its UI
// tab is opened. The pending bit prevents a completion that arrives during an
// active scan from being lost.
func (a *App) scheduleCollaborationRuntimeReconcile() {
	if a == nil {
		return
	}
	a.collaborationReconcileMu.Lock()
	if !a.collaborationReconcileEnabled {
		a.collaborationReconcileMu.Unlock()
		return
	}
	a.collaborationReconcilePending = true
	if a.collaborationReconcileRunning {
		a.collaborationReconcileMu.Unlock()
		return
	}
	a.collaborationReconcileRunning = true
	a.collaborationReconcileMu.Unlock()

	go a.collaborationRuntimeReconcileLoop()
}

func (a *App) collaborationRuntimeReconcileLoop() {
	for {
		a.collaborationReconcileMu.Lock()
		a.collaborationReconcilePending = false
		a.collaborationReconcileMu.Unlock()

		a.reconcileCollaborationRuntimes()

		a.collaborationReconcileMu.Lock()
		if !a.collaborationReconcilePending {
			a.collaborationReconcileRunning = false
			a.collaborationReconcileMu.Unlock()
			return
		}
		a.collaborationReconcileMu.Unlock()
	}
}

func (a *App) reconcileCollaborationRuntimes() {
	if a == nil {
		return
	}
	a.collaborationRestoreMu.Lock()
	defer a.collaborationRestoreMu.Unlock()
	a.restoreCollaborationRuntimesWith(a.startCollaborationRestore)
}

type collaborationRestoreStart func(*desktopCollaboration, string)

var errCollaborationSessionUnregistered = errors.New("persisted collaboration Session is no longer registered")

// collaborationSessionRegistered checks the durable Room ownership records
// directly. ListProjectTree is a UI projection with a bounded cache-backed
// scan; using it as startup authority can leave a valid Host dormant until the
// Room UI happens to trigger another runtime lookup.
func collaborationSessionRegistered(sessionPath string, projects desktopProjectFile) bool {
	ownerPath := collaborationOwnerSessionPath(sessionPath)
	if ownerPath == "" || collaborationSessionPathInTrash(ownerPath) {
		return false
	}
	info, err := os.Stat(ownerPath)
	if err != nil || info.IsDir() {
		return false
	}
	meta, ok, err := agent.LoadBranchMeta(ownerPath)
	if err != nil || !ok || meta.SessionKind != agent.SessionKindCollaboration {
		return false
	}
	topicID := strings.TrimSpace(meta.TopicID)
	if topicID == "" {
		return false
	}
	root := normalizeProjectRoot(meta.WorkspaceRoot)
	if strings.TrimSpace(meta.Scope) == "global" || (strings.TrimSpace(meta.Scope) == "" && root == "") {
		return containsDesktopString(projects.GlobalTopics, topicID) || containsDesktopString(projects.GlobalPinnedTopics, topicID)
	}
	if root == "" {
		return false
	}
	for _, project := range projects.Projects {
		if normalizeProjectRoot(project.Root) != root {
			continue
		}
		return containsDesktopString(project.Topics, topicID) || containsDesktopString(project.PinnedTopics, topicID)
	}
	return false
}

func collaborationSessionPathInTrash(sessionPath string) bool {
	for path := filepath.Clean(sessionPath); ; path = filepath.Dir(path) {
		if strings.EqualFold(filepath.Base(path), sessionTrashDir) {
			return true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
	}
}

func (a *App) restoreCollaborationRuntimesWith(start collaborationRestoreStart) {
	root := strings.TrimSpace(config.MemoryUserDir())
	if root == "" {
		return
	}
	stateDir := filepath.Join(root, "desktop-collaboration-v2")
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "collaboration restore: read state dir: %v\n", err)
		}
		return
	}
	// Build the current durable Topic registry once. The cache directory retains
	// old states for recovery, so a complete cache alone must never resurrect a
	// Room that was left, deleted, trashed, or removed from a topic.
	a.restoreCollaborationRuntimesWithRegistry(entries, stateDir, start, loadProjectsFile())
}

func (a *App) restoreCollaborationRuntimesWithRegistry(entries []os.DirEntry, stateDir string, start collaborationRestoreStart, projects desktopProjectFile) {
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		persistPath := filepath.Join(stateDir, entry.Name())
		a.restoreOneCollaborationWithRegistry(persistPath, start, projects)
	}
}

func (a *App) restoreOneCollaboration(persistPath string) {
	a.restoreOneCollaborationWith(persistPath, a.startCollaborationRestore)
}

func (a *App) restoreOneCollaborationWith(persistPath string, start collaborationRestoreStart) {
	a.restoreOneCollaborationWithRegistry(persistPath, start, loadProjectsFile())
}

func (a *App) restoreOneCollaborationWithRegistry(persistPath string, start collaborationRestoreStart, projects desktopProjectFile) {
	var persisted collaborationPersistedState
	if err := readPersistFile(persistPath, &persisted); err != nil {
		fmt.Fprintf(os.Stderr, "collaboration restore: read %s: %v\n", persistPath, err)
		return
	}
	if persisted.SessionID == "" {
		return
	}
	tab := a.sessionByID(persisted.SessionID)
	if persisted.Mode == "" || persisted.Host == "" || persisted.Room == "" {
		return
	}
	ownerPath := strings.TrimSpace(persisted.SessionPath)
	if ownerPath == "" && tab != nil {
		ownerPath = tab.currentSessionPath()
	}
	if !collaborationSessionRegistered(ownerPath, projects) {
		return
	}
	// Recovery branches are an implementation detail of durable Session saves.
	// Compare both sides through the same logical Room owner identity used by
	// newDesktopCollaboration, otherwise a recovered tab rejects the original
	// Host cache during startup and leaves the UI with a disconnected shell.
	livePath := ""
	if tab != nil {
		livePath = collaborationOwnerSessionPath(tab.currentSessionPath())
	}
	persistedPath := collaborationOwnerSessionPath(persisted.SessionPath)
	if livePath != "" && persistedPath != "" && livePath != persistedPath {
		return
	}
	// Skip sessions that have already been restored (idempotent).
	var runtime *desktopCollaboration
	var err error
	if tab != nil {
		runtime, err = a.collaborationRuntime(persisted.SessionID)
	} else {
		runtime, err = a.collaborationRuntimeForPersisted(persisted)
	}
	if err != nil {
		if errors.Is(err, errCollaborationSessionUnregistered) {
			return
		}
		fmt.Fprintf(os.Stderr, "collaboration restore: runtime %s: %v\n", persisted.SessionID, err)
		return
	}
	// Only restore sessions that are not currently connected.
	state := runtime.snapshot()
	if state.Status == "connected" || state.Status == "syncing" || state.Status == "connecting" {
		return
	}
	// The runtime constructor loads the SessionPath-keyed cache exactly once.
	// Loading it again here would duplicate recovered queue entries.
	p := runtime.repairPersisted(runtime.readPersisted())
	if p.Mode != "" && p.Host != "" && p.Room != "" && p.SessionID != "" {
		// Restore transport residency for every authoritative Room, regardless
		// of whether a tab or Agent Controller is mounted. Old builds could leave
		// both original-path and recovery-path caches; scheduleRestore keeps the
		// startup activation idempotent for that logical Room.
		runtime.scheduleRestore(func() {
			start(runtime, persisted.SessionID)
		})
	}
}

func (a *App) startCollaborationRestore(runtime *desktopCollaboration, sessionID string) {
	go func() {
		// Network residency is independent from Agent Controller readiness. The
		// scheduler resolves Agent readiness only when it actually has work.
		state, err := runtime.retry(a.bootContext())
		if err != nil {
			slog.Warn("desktop: collaboration startup restore failed",
				"session", strings.TrimSpace(sessionID),
				"room", state.Room,
				"host", state.Host,
				"port", state.Port,
				"retryable", state.Retryable,
				"err", err,
			)
		}
	}()
}

func (c *desktopCollaboration) scheduleRestore(start func()) {
	c.restoreOnce.Do(start)
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

func (a *App) UpdateCollaborationProfile(input UpdateCollaborationProfileInput) (CollaborationState, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationState{}, err
	}
	return runtime.updateProfile(a.bootContext(), input)
}

func (a *App) UpdateCollaborationToolApprovalMode(input UpdateCollaborationToolApprovalModeInput) (CollaborationState, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationState{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	switch mode {
	case control.ToolApprovalAsk, control.ToolApprovalAuto, control.ToolApprovalYolo:
	default:
		return runtime.snapshot(), fmt.Errorf("unsupported tool approval mode %q", input.Mode)
	}
	tab, ctrl := a.sessionAndCtrl(input.SessionID)
	if tab == nil || ctrl == nil {
		return runtime.snapshot(), fmt.Errorf("collaboration Agent workspace is not ready")
	}
	a.SetToolApprovalModeForTab(tab.ID, mode)
	state := runtime.snapshot()
	runtime.emitState()
	return state, nil
}

func (a *App) collaborationToolApprovalMode(sessionID string) string {
	tab := a.sessionByID(sessionID)
	mode := control.ToolApprovalAsk
	a.mu.RLock()
	if tab != nil {
		mode = normalizeToolApprovalMode(tab.toolApprovalMode)
	}
	a.mu.RUnlock()
	return mode
}

func collaborationAgentSourceID(kind, value string) string {
	return kind + ":" + strings.TrimSpace(value)
}

func (a *App) collaborationAgentSources(sessionID string) CollaborationAgentSources {
	result := CollaborationAgentSources{Agents: []CollaborationAgentSource{}, Skills: []CollaborationAgentSource{}}
	_, ctrl := a.sessionAndCtrl(sessionID)
	if ctrl == nil {
		return result
	}
	if set := ctrl.Memory(); set != nil {
		for _, doc := range set.Docs {
			if !strings.EqualFold(filepath.Base(doc.Path), "AGENTS.md") {
				continue
			}
			result.Agents = append(result.Agents, CollaborationAgentSource{
				ID: collaborationAgentSourceID("agents", doc.Path), Kind: "agents", Name: filepath.Base(doc.Path),
				Path: doc.Path, Description: string(doc.Scope), Scope: string(doc.Scope), Available: true,
			})
		}
	}
	for _, sk := range ctrl.Skills() {
		result.Skills = append(result.Skills, CollaborationAgentSource{
			ID: collaborationAgentSourceID("skill", sk.Name), Kind: "skill", Name: sk.Name,
			Path: sk.Path, Description: sk.Description, Scope: string(sk.Scope), RunAs: string(sk.RunAs), Protected: sk.IsProtected(), Available: true,
		})
	}
	return result
}

func (a *App) collaborationAgentSourcesWithRefs(sessionID string, refs []string) CollaborationAgentSources {
	result := a.collaborationAgentSources(sessionID)
	available := collaborationAgentSourceMap(result)
	for _, ref := range refs {
		if _, ok := available[ref]; ok {
			continue
		}
		switch {
		case strings.HasPrefix(ref, "agents:"):
			path := strings.TrimPrefix(ref, "agents:")
			result.Agents = append(result.Agents, CollaborationAgentSource{ID: ref, Kind: "agents", Name: filepath.Base(path), Path: path})
		case strings.HasPrefix(ref, "skill:"):
			name := strings.TrimPrefix(ref, "skill:")
			result.Skills = append(result.Skills, CollaborationAgentSource{ID: ref, Kind: "skill", Name: name, Path: "SKILL.md"})
		}
	}
	return result
}

func collaborationAgentSourceMap(sources CollaborationAgentSources) map[string]CollaborationAgentSource {
	result := make(map[string]CollaborationAgentSource, len(sources.Agents)+len(sources.Skills))
	for _, source := range sources.Agents {
		result[source.ID] = source
	}
	for _, source := range sources.Skills {
		result[source.ID] = source
	}
	return result
}

func (a *App) validateCollaborationAgentRefs(sessionID string, refs []string) error {
	available := collaborationAgentSourceMap(a.collaborationAgentSources(sessionID))
	for _, ref := range refs {
		if _, ok := available[ref]; !ok {
			return fmt.Errorf("Room Agent context source %q is unavailable", ref)
		}
	}
	return nil
}

func (a *App) prepareCollaborationAgentInput(sessionID string, refs []string, input string) (string, error) {
	if len(refs) == 0 {
		return input, nil
	}
	_, ctrl := a.sessionAndCtrl(sessionID)
	if ctrl == nil {
		return "", fmt.Errorf("collaboration Agent workspace is not ready")
	}
	available := collaborationAgentSourceMap(a.collaborationAgentSources(sessionID))
	agents := make([]string, 0, len(refs))
	skills := make([]string, 0, len(refs))
	for _, ref := range refs {
		source, ok := available[ref]
		if !ok {
			return "", fmt.Errorf("Room Agent context source %q is unavailable", ref)
		}
		switch source.Kind {
		case "agents":
			agents = append(agents, source.Path)
		case "skill":
			rendered, found := ctrl.RunSkill("/" + source.Name)
			if !found {
				return "", fmt.Errorf("Room Agent skill %q is unavailable", source.Name)
			}
			skills = append(skills, rendered)
		}
	}
	parts := make([]string, 0, len(skills)+2)
	if len(agents) > 0 {
		parts = append(parts, "<explicit-agents-md>\nThe following already-loaded AGENTS.md files are explicitly selected for this Room Agent run. Follow them for this task:\n- "+strings.Join(agents, "\n- ")+"\n</explicit-agents-md>")
	}
	parts = append(parts, skills...)
	parts = append(parts, input)
	return strings.Join(parts, "\n\n"), nil
}

func defaultCollaborationAgentConfig() CollaborationAgentConfig {
	return CollaborationAgentConfig{RecognitionMode: "interval", AgentResponseIntervalSeconds: 30, AgentClockTurns: 12}
}

func normalizeCollaborationAgentConfig(value CollaborationAgentConfig, fallbackAlias string) CollaborationAgentConfig {
	value.Alias = strings.TrimSpace(value.Alias)
	if value.Alias == "" {
		value.Alias = strings.TrimSpace(fallbackAlias)
	}
	switch value.RecognitionMode {
	case "message", "interval", "off":
	default:
		value.RecognitionMode = "interval"
	}
	if value.AgentResponseIntervalSeconds == 0 {
		value.AgentResponseIntervalSeconds = 30
	}
	if value.AgentResponseIntervalSeconds < 5 {
		value.AgentResponseIntervalSeconds = 5
	}
	if value.AgentResponseIntervalSeconds > 3600 {
		value.AgentResponseIntervalSeconds = 3600
	}
	if value.AgentClockTurns == 0 {
		value.AgentClockTurns = 12
	}
	if value.AgentClockTurns < 1 {
		value.AgentClockTurns = 1
	}
	if value.AgentClockTurns > 100 {
		value.AgentClockTurns = 100
	}
	value.AgentClockWoundAt = strings.TrimSpace(value.AgentClockWoundAt)
	if value.AgentClockWoundAt != "" {
		if woundAt, err := time.Parse(time.RFC3339Nano, value.AgentClockWoundAt); err == nil {
			value.AgentClockWoundAt = woundAt.UTC().Format(time.RFC3339Nano)
		} else {
			value.AgentClockWoundAt = ""
		}
	}
	refs := make([]string, 0, len(value.ContextRefs))
	seen := make(map[string]bool, len(value.ContextRefs))
	for _, ref := range value.ContextRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] || len(refs) >= 32 {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	value.ContextRefs = refs
	return value
}

func equalCollaborationAgentConfig(a, b CollaborationAgentConfig) bool {
	return a.Alias == b.Alias &&
		a.AutoRespondQuestions == b.AutoRespondQuestions &&
		a.AutoRespondRequests == b.AutoRespondRequests &&
		a.AutoRespondAgents == b.AutoRespondAgents &&
		a.AgentResponseIntervalSeconds == b.AgentResponseIntervalSeconds &&
		a.AgentClockTurns == b.AgentClockTurns &&
		a.AgentClockUnlimited == b.AgentClockUnlimited &&
		a.AgentClockWoundAt == b.AgentClockWoundAt &&
		a.RecognitionMode == b.RecognitionMode &&
		equalStrings(a.ContextRefs, b.ContextRefs)
}

func (c *desktopCollaboration) updateAgentConfig(ctx context.Context, input UpdateCollaborationAgentConfigInput) (CollaborationState, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	input.RequestID = strings.TrimSpace(input.RequestID)
	if len(input.Config.ContextRefs) > 32 {
		return c.snapshot(), fmt.Errorf("Room Agent supports at most 32 explicit context sources")
	}
	if woundAt := strings.TrimSpace(input.Config.AgentClockWoundAt); woundAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, woundAt); err != nil {
			return c.snapshot(), fmt.Errorf("Agent clock woundAt must be an RFC3339 timestamp")
		}
	}

	c.mu.RLock()
	current := normalizeCollaborationAgentConfig(c.state.AgentConfig, c.currentAgentNameLocked())
	c.mu.RUnlock()
	next := normalizeCollaborationAgentConfig(input.Config, current.Alias)
	if next.Alias == "" || len(next.Alias) > 256 {
		return c.snapshot(), fmt.Errorf("Agent alias is required and must not exceed 256 bytes")
	}
	if c.app != nil {
		if err := c.app.validateCollaborationAgentRefs(c.ownerSessionID, next.ContextRefs); err != nil {
			return c.snapshot(), err
		}
	}
	if equalCollaborationAgentConfig(next, current) {
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

func (c *desktopCollaboration) updateProfile(ctx context.Context, input UpdateCollaborationProfileInput) (CollaborationState, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.MemberName = strings.TrimSpace(input.MemberName)
	input.MemberAvatar = strings.TrimSpace(input.MemberAvatar)
	input.AgentName = strings.TrimSpace(input.AgentName)
	input.AgentAvatar = strings.TrimSpace(input.AgentAvatar)
	if input.RequestID == "" {
		return c.snapshot(), fmt.Errorf("requestId is required when changing the Room identity")
	}
	if input.MemberName == "" || input.AgentName == "" || len(input.MemberName) > 256 || len(input.AgentName) > 256 {
		return c.snapshot(), fmt.Errorf("member and Agent names are required and must not exceed 256 bytes")
	}

	c.mu.RLock()
	var current collab.Member
	for _, member := range c.state.Snapshot.Members {
		if member.ID == c.state.MemberID {
			current = member
			break
		}
	}
	c.mu.RUnlock()
	if current.Name == input.MemberName && current.Avatar == input.MemberAvatar && current.Agent.Name == input.AgentName && current.Agent.Avatar == input.AgentAvatar {
		return c.snapshot(), nil
	}
	memberAvatar, agentAvatar := input.MemberAvatar, input.AgentAvatar
	if _, err := c.submit(ctx, input.RequestID, collab.Command{Type: collab.CommandUpdateAgent, AgentUpdate: &collab.UpdateAgentInput{
		Name: input.AgentName, Avatar: &agentAvatar, MemberName: input.MemberName, MemberAvatar: &memberAvatar,
	}}); err != nil {
		return c.snapshot(), err
	}

	c.mu.Lock()
	c.state.AgentConfig.Alias = input.AgentName
	if c.conn != nil {
		c.conn.memberName, c.conn.memberAvatar = input.MemberName, input.MemberAvatar
		c.conn.agentName, c.conn.agentAvatar = input.AgentName, input.AgentAvatar
	}
	for i := range c.state.Snapshot.Members {
		if c.state.Snapshot.Members[i].ID == c.state.MemberID {
			c.state.Snapshot.Members[i].Name = input.MemberName
			c.state.Snapshot.Members[i].Avatar = input.MemberAvatar
			c.state.Snapshot.Members[i].Agent.Name = input.AgentName
			c.state.Snapshot.Members[i].Agent.Avatar = input.AgentAvatar
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
	ctx, cancel := context.WithTimeout(a.bootContext(), 12*time.Second)
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

// StopCollaborationAgentRun cancels the in-flight agent run for sessionID.
// The runID must match the currently active run; otherwise the call is
// rejected. Idempotent when no run is active or already stopping.
func (a *App) StopCollaborationAgentRun(sessionID, runID string) error {
	runtime, err := a.collaborationRuntime(sessionID)
	if err != nil {
		return err
	}
	return runtime.stopCurrentAgentRun(sessionID, runID)
}

func (c *desktopCollaboration) stopCurrentAgentRun(sessionID, runID string) error {
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return fmt.Errorf("sessionId and runId are required")
	}

	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	if c.state.SessionID != sessionID {
		c.mu.Unlock()
		return fmt.Errorf("sessionId does not match this member's Personal Agent")
	}
	run := c.runs[sessionID]
	if run == nil {
		c.mu.Unlock()
		return nil
	}
	if run.RunID != runID {
		c.mu.Unlock()
		return fmt.Errorf("runId does not match the current Agent run")
	}
	if run.StopRequested {
		c.mu.Unlock()
		return nil
	}
	previousPromptOpen := run.PromptOpen
	previousPrompt := cloneCollaborationAgentPrompt(run.Prompt)
	run.StopRequested = true
	run.PromptOpen = false
	run.Prompt = nil
	c.persistLocked()
	c.mu.Unlock()

	if c.stopAgent == nil {
		c.rollbackAgentStop(sessionID, run, previousPromptOpen, previousPrompt)
		c.emitState()
		return fmt.Errorf("collaboration Agent stop is unavailable")
	}
	if err := c.stopAgent(sessionID); err != nil {
		c.rollbackAgentStop(sessionID, run, previousPromptOpen, previousPrompt)
		c.emitState()
		return err
	}
	c.emitState()
	return nil
}

func (c *desktopCollaboration) rollbackAgentStop(sessionID string, run *collaborationAgentRun, promptOpen bool, prompt *CollaborationAgentPrompt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runs[sessionID] != run {
		return
	}
	run.StopRequested = false
	run.PromptOpen = promptOpen
	run.Prompt = prompt
	c.persistLocked()
}

func (a *App) CancelCollaborationQueuedTask(input CancelCollaborationQueuedTaskInput) (CollaborationActionResult, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationActionResult{}, err
	}
	return runtime.cancelQueuedTask(a.bootContext(), input)
}

func (a *App) RespondCollaborationAgentRun(input RespondCollaborationAgentRunInput) (CollaborationActionResult, error) {
	runtime, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationActionResult{}, err
	}
	return runtime.respondAgentRun(a.bootContext(), input)
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
	state := cloneCollaborationState(c.state)
	if state.SessionID == "" {
		state.SessionID = c.ownerSessionID
	}
	state.OutboxCount = len(c.outbox)
	state.Outbox = c.outboxViewsLocked()
	state.Transfers = c.fileTransfersLocked()
	state.QueuedTasks = c.queuedTaskViewsLocked()
	if run := c.runs[state.SessionID]; run != nil {
		if run.PromptOpen {
			state.AgentPrompt = cloneCollaborationAgentPrompt(run.Prompt)
		}
		state.CurrentRun = c.currentRunViewLocked(run)
	}
	c.mu.RUnlock()
	state.ToolApprovalMode = control.ToolApprovalAsk
	if c.app != nil {
		state.ToolApprovalMode = c.app.collaborationToolApprovalMode(state.SessionID)
		state.AgentSources = c.app.collaborationAgentSourcesWithRefs(state.SessionID, state.AgentConfig.ContextRefs)
	}
	return state
}

func (c *desktopCollaboration) currentRunViewLocked(run *collaborationAgentRun) *CollaborationCurrentRun {
	if run == nil {
		return nil
	}
	phase := "running"
	if run.StopRequested {
		phase = "stopping"
	} else if run.PromptOpen {
		phase = "waiting_approval"
	}
	queueCount := 0
	for _, queued := range c.queuedRuns {
		if queued != nil && queued.SessionID == run.SessionID {
			queueCount++
		}
	}
	view := &CollaborationCurrentRun{
		SessionID: run.SessionID, RunID: run.RunID, Phase: phase,
		Instruction: sanitizeCollaborationText(run.Instruction),
		Progress:    sanitizeCollaborationText(run.Text.String()), QueueCount: queueCount,
	}
	if !run.StartedAt.IsZero() {
		view.StartedAt = run.StartedAt.UnixMilli()
	}
	return view
}

func (c *desktopCollaboration) queuedTaskViewsLocked() []CollaborationQueuedTask {
	result := make([]CollaborationQueuedTask, 0, len(c.queuedRuns))
	for _, run := range c.queuedRuns {
		if run == nil {
			continue
		}
		result = append(result, CollaborationQueuedTask{
			ID: run.RunID, RequestID: run.CommandID, Instruction: run.Instruction,
			ReferenceIDs: append([]string(nil), run.ReferenceIDs...), AgentRequestID: run.AgentRequestID,
			QueuedAt: run.QueuedAt,
		})
	}
	return result
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
		return &collab.TimelineItem{ID: id, Sequence: sequence, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: id, AuthorID: env.MemberID, Text: env.Command.Chat.Text, MentionMemberIDs: append([]string(nil), env.Command.Chat.MentionMemberIDs...), MentionAgentIDs: append([]string(nil), env.Command.Chat.MentionAgentIDs...), ReferenceIDs: append([]string(nil), env.Command.Chat.ReferenceIDs...), Revision: 1, CreatedAt: createdAt}}
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
		return &collab.TimelineItem{ID: id, Sequence: sequence, Type: collab.TimelineAgentResult, AgentResult: &collab.AgentResult{ID: value.ResultID, OwnerID: env.MemberID, AgentID: value.AgentID, RunID: value.RunID, Revision: value.Revision, Summary: value.Summary, ReferenceIDs: append([]string(nil), value.ReferenceIDs...), Handoffs: cloneCollaborationHandoffs(value.Handoffs), CreatedAt: createdAt}}
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
	state.AgentConfig.ContextRefs = append([]string(nil), state.AgentConfig.ContextRefs...)
	state.AgentSources.Agents = append([]CollaborationAgentSource(nil), state.AgentSources.Agents...)
	state.AgentSources.Skills = append([]CollaborationAgentSource(nil), state.AgentSources.Skills...)
	state.AgentPrompt = cloneCollaborationAgentPrompt(state.AgentPrompt)
	if state.CurrentRun != nil {
		current := *state.CurrentRun
		state.CurrentRun = &current
	}
	state.Routes = append([]CollaborationRouteState(nil), state.Routes...)
	if state.Advertisement != nil {
		advertisement := *state.Advertisement
		advertisement.Relays = append([]CollaborationAdvertisementRelayState(nil), state.Advertisement.Relays...)
		state.Advertisement = &advertisement
	}
	return state
}

func cloneCollaborationAgentPrompt(value *CollaborationAgentPrompt) *CollaborationAgentPrompt {
	if value == nil {
		return nil
	}
	result := *value
	result.Questions = append([]CollaborationAgentPromptQuestion(nil), value.Questions...)
	for i := range result.Questions {
		result.Questions[i].Options = append([]CollaborationAgentPromptOption(nil), value.Questions[i].Options...)
	}
	return &result
}

func cloneCollaborationHandoffs(values []collab.AgentHandoff) []collab.AgentHandoff {
	if values == nil {
		return nil
	}
	result := make([]collab.AgentHandoff, len(values))
	for i, value := range values {
		value.ReferenceIDs = append([]string(nil), value.ReferenceIDs...)
		result[i] = value
	}
	return result
}

func (c *desktopCollaboration) host(ctx context.Context, input HostCollaborationRoomInput) (CollaborationState, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if input.ProtocolVersion == 0 {
		input.ProtocolVersion = collaborationProtocolV2
	}
	if input.ProtocolVersion != collaborationProtocolV1 && input.ProtocolVersion != collaborationProtocolV2 {
		err := fmt.Errorf("unsupported collaboration protocol version %d", input.ProtocolVersion)
		return c.failState("failed", err, false), err
	}
	input.Room = strings.TrimSpace(input.Room)
	if input.Room == "" {
		if input.ProtocolVersion == collaborationProtocolV1 {
			err := fmt.Errorf("room is required for collaboration V1")
			return c.failState("failed", err, false), err
		}
		if strings.TrimSpace(input.SessionID) == "" {
			err := fmt.Errorf("sessionId is required to generate room_id")
			return c.failState("failed", err, false), err
		}
		input.Room = stableCollaborationID("room", strings.TrimSpace(input.SessionID))
	}
	var err error
	input.ListenHost, err = normalizeCollaborationHost(input.ListenHost)
	if err != nil {
		return c.failState("failed", err, false), err
	}
	identity, err := c.localIdentity(input.MemberID, input.MemberName, input.MemberAvatar, input.MemberRole, input.AgentID, input.AgentName, input.AgentAvatar, input.AgentRole, input.SessionID, input.Room)
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
	if err := applyCollaborationInvite(&input); err != nil {
		return c.failState("failed", err, false), err
	}
	var err error
	input.Host, err = normalizeCollaborationHost(input.Host)
	if err != nil {
		return c.failState("failed", err, false), err
	}
	baseIdentity, err := c.localIdentity(input.MemberID, input.MemberName, input.MemberAvatar, input.MemberRole, input.AgentID, input.AgentName, input.AgentAvatar, input.AgentRole, input.SessionID, input.Room)
	if err != nil {
		return c.failState("failed", err, false), err
	}
	scopedIdentity := scopedCollaborationIdentity(baseIdentity, input.Room, input.SessionID)
	identity := scopedIdentity
	resume := c.resumeSession(input.Host, input.Port, input.Room, identity.ID, input.SessionID)
	if resume == "" {
		if legacyResume := c.resumeSession(input.Host, input.Port, input.Room, baseIdentity.ID, input.SessionID); legacyResume != "" {
			identity, resume = baseIdentity, legacyResume
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
	if state.Mode != "host" || state.Room == "" || conn == nil {
		return CollaborationInvite{}, fmt.Errorf("only the Room Host can export a collaboration connection")
	}
	if token == "" {
		persisted := c.readPersisted()
		if persisted.JoinTokenSecretRef != "" && c.getSecret != nil {
			token = c.getSecret(persisted.JoinTokenSecretRef)
		}
	}
	result := CollaborationInvite{
		Version: 1,
		Hosts:   collaborationLocalHosts(state.Host),
		Port:    state.Port,
		Room:    state.Room,
		Token:   token,
	}
	for _, route := range conn.routes {
		if route.Kind == "lan" && route.Status == "connected" {
			for _, host := range collaborationLocalHosts(route.Host) {
				result.Routes = append(result.Routes, CollaborationRouteInput{ID: "lan:" + host, Kind: "lan", Host: host, Port: route.Port, Priority: route.Priority, ProtocolVersion: route.ProtocolVersion})
			}
			continue
		}
		if route.Kind != "relay" || route.Status != "connected" {
			continue
		}
		value := route.CollaborationRouteInput
		if ref := conn.guestCapabilityRefs[route.RelayID]; ref != "" {
			value.GuestCapability = c.getSecret(ref)
		}
		if value.GuestCapability != "" {
			result.Routes = append(result.Routes, value)
		}
	}
	if len(result.Routes) > 0 && conn.hostKey != "" {
		result.Version, result.HostKey = 2, conn.hostKey
		encoded, err := buildCollaborationInviteV2(collaborationInviteV2{Room: state.Room, HostKey: conn.hostKey, Routes: result.Routes, RoomToken: token})
		if err != nil {
			return CollaborationInvite{}, err
		}
		result.Invite = encoded
	}
	if result.Version < 2 && (result.Port < 1 || result.Port > 65535) {
		return CollaborationInvite{}, fmt.Errorf("collaboration invite is not ready; relay routes or host key may still be connecting")
	}
	return result, nil
}

func collaborationLocalHosts(bindHost string) []string {
	bindHost = strings.Trim(strings.TrimSpace(bindHost), "[]")
	bindIP := net.ParseIP(strings.Split(bindHost, "%")[0])
	if bindHost != "" && (bindIP == nil || !bindIP.IsUnspecified()) {
		return []string{bindHost}
	}
	seen := map[string]bool{}
	var ipv4, ipv6, loopback []string
	add := func(value string) {
		value = strings.Trim(strings.TrimSpace(value), "[]")
		ip := net.ParseIP(strings.Split(value, "%")[0])
		if ip == nil || ip.IsUnspecified() || seen[value] {
			return
		}
		seen[value] = true
		if ip.IsLoopback() {
			loopback = append(loopback, value)
		} else if ip.To4() != nil {
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
	add("::1")
	sort.Strings(ipv4)
	sort.Strings(ipv6)
	sort.Strings(loopback)
	return append(append(ipv4, ipv6...), loopback...)
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
	c.queuedRuns = nil
	c.persistLocked()
	c.mu.Unlock()
	c.closeFileTransfers()
	if c.schedulerCancel != nil {
		c.schedulerCancel()
		c.schedulerCancel = nil
	}
	c.emitState()
	return nil
}

func (c *desktopCollaboration) close() {
	c.stopUpdateLoop()
	c.opMu.Lock()
	c.mu.Lock()
	conn := c.conn
	c.persistLocked()
	c.conn = nil
	c.mu.Unlock()
	if c.schedulerCancel != nil {
		c.schedulerCancel()
		c.schedulerCancel = nil
	}
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = conn.close(ctx, false)
		cancel()
	}
	c.closeFileTransfers()
	c.opMu.Unlock()
	c.waitUpdateLoop()
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
	return c.retryLocked(ctx, true)
}

// retryLocked refreshes or rebuilds the current Room connection. manual is
// deliberately explicit: only a user-requested retry may release commands
// that previously failed with a non-retryable response.
//
// c.opMu must be held by the caller.
func (c *desktopCollaboration) retryLocked(ctx context.Context, manual bool) (CollaborationState, error) {
	c.mu.Lock()
	conn := c.conn
	pendingLeave := c.leaveError != ""
	if pendingLeave {
		c.mu.Unlock()
		err := c.leaveCurrent(ctx)
		return c.snapshot(), err
	}
	if manual {
		for requestID := range c.outboxFailures {
			delete(c.outboxFailures, requestID)
		}
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
			err := fmt.Errorf("collaboration connection must be joined again")
			return c.failState("failed", err, true), err
		}
		identity, err := c.localIdentity(p.MemberID, p.MemberName, p.MemberAvatar, p.MemberRole, p.AgentID, p.AgentName, p.AgentAvatar, p.AgentRole, p.SessionID, p.Room)
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
			visibility := "private"
			if p.Advertisement != nil {
				visibility = p.Advertisement.Visibility
			}
			conn, err = c.openHost(ctx, HostCollaborationRoomInput{
				ListenHost: p.Host, Port: p.Port, Room: p.Room, RoomName: p.RoomName, Description: p.Description, Token: token,
				MemberID: p.MemberID, MemberName: p.MemberName, MemberAvatar: p.MemberAvatar, MemberRole: p.MemberRole,
				AgentID: p.AgentID, AgentName: p.AgentName, AgentAvatar: p.AgentAvatar, AgentRole: p.AgentRole, SessionID: p.SessionID,
				LANEnabled: boolPointer(p.LANEnabled), RelayIDs: append([]string(nil), p.RelayIDs...), PreferLAN: boolPointer(p.PreferLAN), Visibility: visibility, ProtocolVersion: p.ProtocolVersion,
			}, identity, resume)
		} else {
			routes := make([]CollaborationRouteInput, 0, len(p.Routes))
			for _, state := range p.Routes {
				route := state.CollaborationRouteInput
				if route.Kind == "relay" {
					if ref := p.GuestCapabilityRefs[route.RelayID]; ref != "" {
						route.GuestCapability = c.getSecret(ref)
					}
				}
				routes = append(routes, route)
			}
			conn, err = c.openJoin(ctx, JoinCollaborationRoomInput{
				Host: p.Host, Port: p.Port, Room: p.Room, Token: token,
				MemberID: p.MemberID, MemberName: p.MemberName, MemberAvatar: p.MemberAvatar, MemberRole: p.MemberRole,
				AgentID: p.AgentID, AgentName: p.AgentName, AgentAvatar: p.AgentAvatar, AgentRole: p.AgentRole, SessionID: p.SessionID,
				Routes: routes, HostKey: p.HostKey,
			}, identity, resume)
		}
		if err != nil {
			return c.failState("failed", err, collaborationErrorRetryable(err)), err
		}
		return c.installConnection(conn)
	}
	if conn.mode == "host" {
		c.retryRelayBindings(ctx, conn)
	}
	c.syncConnection(ctx, conn)
	c.ensureConnectionLoop(conn)
	return c.snapshot(), nil
}

func boolPointer(value bool) *bool { return &value }

func (c *desktopCollaboration) startAgent(ctx context.Context, input StartCollaborationAgentInput) (CollaborationActionResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.startAgentLocked(ctx, input)
}

func (c *desktopCollaboration) startAgentLocked(ctx context.Context, input StartCollaborationAgentInput) (CollaborationActionResult, error) {
	requestID := strings.TrimSpace(input.RequestID)
	sessionID := strings.TrimSpace(input.SessionID)
	instruction := strings.TrimSpace(input.Instruction)
	if requestID == "" || sessionID == "" || instruction == "" {
		return CollaborationActionResult{}, fmt.Errorf("requestId, sessionId, and instruction are required")
	}
	fingerprint := collaborationStartFingerprint(input)

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
	busy := c.runs[sessionID] != nil || len(c.queuedRuns) > 0
	if len(c.queuedRuns) >= maxCollaborationAgentQueue {
		c.mu.Unlock()
		return CollaborationActionResult{RequestID: requestID, Code: "agent_queue_full", Error: "Personal Agent queue already has 20 tasks"}, nil
	}
	c.mu.Unlock()
	if !busy {
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
		busy = !ready
	}

	queuedAt := time.Now().UTC().Format(time.RFC3339Nano)
	runID := stableCollaborationID("run", state.MemberID+"\x00"+requestID)
	run := &collaborationAgentRun{
		Room: state.Room, MemberID: state.MemberID, AgentID: state.AgentID,
		RunID: runID, CommandID: requestID, SessionID: sessionID,
		AgentRequestID: strings.TrimSpace(input.AgentRequestID), Instruction: instruction,
		ReferenceIDs: append([]string(nil), input.ReferenceIDs...), ContextRefs: append([]string(nil), state.AgentConfig.ContextRefs...), QueuedAt: queuedAt,
		Automatic: input.Automatic,
		ReadOnly:  input.ReadOnly,
		Updates:   make(chan collaborationRunUpdate, 32),
	}
	if busy {
		c.mu.Lock()
		c.starts[requestID] = collaborationStartRecord{RunID: runID, Fingerprint: fingerprint}
		c.queuedRuns = append(c.queuedRuns, run)
		c.persistLocked()
		c.mu.Unlock()

		result, err := c.publishRun(ctx, run, collab.RunQueued, "", "")
		if err != nil {
			c.mu.Lock()
			c.removeQueuedRunLocked(runID)
			delete(c.starts, requestID)
			c.persistLocked()
			c.mu.Unlock()
			c.emitState()
			return CollaborationActionResult{}, err
		}
		result.RunID = runID
		result.RequestID = requestID
		result.Queued = true
		c.emitState()
		go c.startNextQueuedAgent(sessionID)
		return result, nil
	}

	fullInput, err := collaborationAgentInput(state.Snapshot, run.AgentID, instruction, input.ReferenceIDs, c.roomAttachmentRefs(input.ReferenceIDs))
	if err != nil {
		return CollaborationActionResult{}, err
	}
	c.mu.Lock()
	c.starts[requestID] = collaborationStartRecord{RunID: runID, Fingerprint: fingerprint}
	run.StartedAt = time.Now().UTC()
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
	if err := c.launchAgent(ctx, run, fullInput); err != nil {
		return CollaborationActionResult{}, err
	}
	return queued, nil
}

func (c *desktopCollaboration) removeQueuedRunLocked(runID string) *collaborationAgentRun {
	for i, run := range c.queuedRuns {
		if run == nil || run.RunID != runID {
			continue
		}
		c.queuedRuns = append(c.queuedRuns[:i], c.queuedRuns[i+1:]...)
		return run
	}
	return nil
}

func (c *desktopCollaboration) cancelQueuedTask(ctx context.Context, input CancelCollaborationQueuedTaskInput) (CollaborationActionResult, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	taskID := strings.TrimSpace(input.TaskID)
	if sessionID == "" || taskID == "" {
		return CollaborationActionResult{}, fmt.Errorf("sessionId and taskId are required")
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	if c.state.SessionID != sessionID {
		c.mu.Unlock()
		return CollaborationActionResult{}, fmt.Errorf("sessionId does not match this member's Personal Agent")
	}
	run := c.removeQueuedRunLocked(taskID)
	if run == nil {
		c.mu.Unlock()
		return CollaborationActionResult{RequestID: taskID, RunID: taskID, Duplicate: true}, nil
	}
	c.persistLocked()
	c.mu.Unlock()
	c.emitState()
	result, err := c.publishRun(ctx, run, collab.RunCancelled, "", "cancelled by the Agent owner")
	result.RequestID = run.CommandID
	result.RunID = run.RunID
	return result, err
}

func (c *desktopCollaboration) respondAgentRun(ctx context.Context, input RespondCollaborationAgentRunInput) (CollaborationActionResult, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	runID := strings.TrimSpace(input.RunID)
	if sessionID == "" || runID == "" {
		return CollaborationActionResult{}, fmt.Errorf("sessionId and runId are required")
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	run := c.runs[sessionID]
	if run == nil {
		c.mu.Unlock()
		return CollaborationActionResult{RequestID: runID + ":respond", RunID: runID, Duplicate: true}, nil
	}
	if run.RunID != runID {
		c.mu.Unlock()
		return CollaborationActionResult{}, fmt.Errorf("Agent run changed: got %q, current %q", runID, run.RunID)
	}
	if !run.PromptOpen {
		c.mu.Unlock()
		return CollaborationActionResult{RequestID: runID + ":respond", RunID: runID, Duplicate: true}, nil
	}
	prompt := cloneCollaborationAgentPrompt(run.Prompt)
	run.PromptOpen = false
	run.Prompt = nil
	c.mu.Unlock()
	c.emitState()
	if c.respondAgent == nil {
		c.mu.Lock()
		if c.runs[sessionID] == run {
			run.PromptOpen = true
			run.Prompt = prompt
		}
		c.mu.Unlock()
		c.emitState()
		return CollaborationActionResult{}, fmt.Errorf("collaboration Agent confirmation is unavailable")
	}
	cancelled, err := c.respondAgent(sessionID, input)
	if err != nil {
		c.mu.Lock()
		if c.runs[sessionID] == run {
			run.PromptOpen = true
			run.Prompt = prompt
		}
		c.mu.Unlock()
		c.emitState()
		return CollaborationActionResult{}, err
	}
	if !cancelled {
		result, err := c.publishRun(ctx, run, collab.RunRunning, "", "")
		result.RequestID = runID + ":respond"
		result.RunID = runID
		return result, err
	}
	return CollaborationActionResult{RequestID: runID + ":respond", RunID: runID}, nil
}

func (a *App) respondCollaborationAgent(sessionID string, input RespondCollaborationAgentRunInput) (bool, error) {
	_, ctrl := a.sessionAndCtrl(sessionID)
	if ctrl == nil {
		return false, fmt.Errorf("collaboration Agent workspace is not ready")
	}
	pending, ok := ctrl.PendingInteraction()
	if !ok {
		return false, fmt.Errorf("collaboration Agent is no longer waiting for confirmation")
	}
	if pending.Kind == control.PendingInteractionApproval {
		if input.Answering {
			return false, fmt.Errorf("this Agent is waiting for tool approval, not an answer")
		}
		ctrl.Approve(pending.Approval.ID, input.Allow, input.Session, input.Persist)
		return false, nil
	}
	if pending.Kind != control.PendingInteractionAsk {
		return false, fmt.Errorf("unsupported pending interaction %q", pending.Kind)
	}
	if input.Answering {
		answers := make([]event.AskAnswer, len(input.Answers))
		for i, answer := range input.Answers {
			answers[i] = event.AskAnswer{QuestionID: answer.QuestionID, Selected: append([]string(nil), answer.Selected...)}
		}
		ctrl.AnswerQuestion(pending.Ask.ID, answers)
		return false, nil
	}
	if input.Allow {
		return false, fmt.Errorf("this Agent is waiting for an answer, not approval")
	}
	ctrl.Cancel()
	return true, nil
}

func (c *desktopCollaboration) waitForQueuedAgent(sessionID string) {
	if c.waitAgentReady == nil {
		return
	}
	c.mu.Lock()
	if c.queueWaiting {
		c.mu.Unlock()
		return
	}
	c.queueWaiting = true
	c.mu.Unlock()

	ctx := context.Background()
	if c.app != nil {
		ctx = c.app.bootContext()
	}
	go func() {
		err := c.waitAgentReady(ctx, sessionID)
		c.mu.Lock()
		c.queueWaiting = false
		if err != nil && ctx.Err() == nil {
			c.state.LastError = "queued Agent is waiting for workspace recovery: " + err.Error()
			c.state.Retryable = true
			c.persistLocked()
		}
		c.mu.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				c.emitState()
			}
			return
		}
		c.startNextQueuedAgent(sessionID)
	}()
}

func (c *desktopCollaboration) launchAgent(ctx context.Context, run *collaborationAgentRun, fullInput string) error {
	if c.prepareAgentInput != nil {
		prepared, err := c.prepareAgentInput(run.SessionID, run.ContextRefs, fullInput)
		if err != nil {
			c.failAgentRun(ctx, run, err)
			return err
		}
		fullInput = prepared
	}
	if run.ReadOnly {
		fullInput = collaborationReadOnlyAnswerInput(fullInput)
	}
	if _, err := c.publishRun(ctx, run, collab.RunRunning, "", ""); err != nil {
		c.failAgentRun(ctx, run, err)
		return err
	}
	if err := c.prepareAgentApproval(run); err != nil {
		c.failAgentRun(ctx, run, err)
		return err
	}
	if err := c.prepareAgentReadOnlyForRun(run); err != nil {
		c.restoreAgentApproval(run)
		c.failAgentRun(ctx, run, err)
		return err
	}
	if err := c.submitAgent(run.SessionID, run.Instruction, fullInput); err != nil {
		c.restoreAgentApproval(run)
		c.restoreAgentReadOnlyForRun(run)
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
	removed := false
	if c.runs[run.SessionID] == run {
		delete(c.runs, run.SessionID)
		removed = true
	}
	c.persistLocked()
	c.mu.Unlock()
	_, _ = c.publishRun(ctx, run, collab.RunFailed, "", sanitizeCollaborationText(err.Error()))
	if removed {
		go c.startNextQueuedAgent(run.SessionID)
	}
}

func (c *desktopCollaboration) respond(ctx context.Context, input RespondCollaborationRequestInput) (CollaborationActionResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
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
	if strings.TrimSpace(input.SessionID) != state.SessionID {
		return CollaborationActionResult{}, fmt.Errorf("sessionId does not match this member's Personal Agent")
	}
	request := collaborationAgentRequest(state.Snapshot, requestRef)
	if request == nil {
		return CollaborationActionResult{}, fmt.Errorf("agent request %q does not exist", requestRef)
	}
	if request.TargetMemberID != state.MemberID {
		return CollaborationActionResult{}, fmt.Errorf("agent request is assigned to another member")
	}
	if action == string(collab.RequestAccepted) {
		c.mu.RLock()
		queueFull := len(c.queuedRuns) >= maxCollaborationAgentQueue
		c.mu.RUnlock()
		if queueFull {
			return CollaborationActionResult{RequestID: requestID, Code: "agent_queue_full", Error: "Personal Agent queue already has 20 tasks"}, nil
		}
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
	started, err := c.startAgentLocked(ctx, StartCollaborationAgentInput{
		RequestID:      stableCollaborationID("agent_command", requestRef+"\x00"+state.MemberID),
		SessionID:      input.SessionID,
		Instruction:    instruction,
		ReferenceIDs:   request.ReferenceIDs,
		AgentRequestID: requestRef,
		Automatic:      input.Automatic,
	})
	if err != nil {
		return CollaborationActionResult{RequestID: requestID, Receipt: result.Receipt, Retryable: true, Error: err.Error()}, err
	}
	started.Receipt = result.Receipt
	return started, nil
}

func (a *App) prepareCollaborationAutoAgent(sessionID string) (string, error) {
	tab, ctrl := a.sessionAndCtrl(sessionID)
	if ctrl == nil {
		return "", fmt.Errorf("collaboration Agent workspace is not ready")
	}
	previous := ctrl.ToolApprovalMode()
	if previous != control.ToolApprovalAsk {
		return previous, nil
	}
	if tab != nil {
		tab.autoAgentActive.Store(true)
	}
	ctrl.SetToolApprovalMode(control.ToolApprovalAuto)
	return previous, nil
}

func (a *App) restoreCollaborationAutoAgent(sessionID, previous string) {
	tab, ctrl := a.sessionAndCtrl(sessionID)
	automatic := true
	if tab != nil {
		automatic = tab.autoAgentActive.Swap(false)
	}
	if ctrl == nil || !automatic || ctrl.ToolApprovalMode() != control.ToolApprovalAuto {
		return
	}
	if tab != nil {
		previous = normalizeToolApprovalMode(tab.toolApprovalMode)
	}
	ctrl.SetToolApprovalMode(previous)
}

// prepareCollaborationAgentReadOnly enables the owning Session's scoped
// runtime tool gate without entering the user-visible plan workflow.
func (a *App) prepareCollaborationAgentReadOnly(sessionID string) (bool, error) {
	_, ctrl := a.sessionAndCtrl(sessionID)
	if ctrl == nil {
		return false, fmt.Errorf("collaboration Agent workspace is not ready")
	}
	previous := ctrl.RuntimeReadOnly()
	ctrl.SetRuntimeReadOnly(true)
	return previous, nil
}

// restoreCollaborationAgentReadOnly restores the scoped runtime tool gate.
func (a *App) restoreCollaborationAgentReadOnly(sessionID string, previous bool) {
	_, ctrl := a.sessionAndCtrl(sessionID)
	if ctrl == nil {
		return
	}
	ctrl.SetRuntimeReadOnly(previous)
}

func (c *desktopCollaboration) emitState() {
	if c.app == nil || c.app.ctx == nil {
		return
	}
	c.app.runtimeEvents.Emit(c.app.ctx, collaborationStateChannel, c.snapshot())
}

func (c *desktopCollaboration) localIdentity(memberID, memberName, memberAvatar, memberRole, agentID, agentName, agentAvatar, agentRole, sessionID, room string) (collab.MemberDescriptor, error) {
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
		ID: strings.TrimSpace(memberID), Name: memberName, Avatar: strings.TrimSpace(memberAvatar), Role: strings.TrimSpace(memberRole),
		Agent: collab.AgentDescriptor{
			ID: strings.TrimSpace(agentID), Name: strings.TrimSpace(agentName), Avatar: strings.TrimSpace(agentAvatar), Role: strings.TrimSpace(agentRole),
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

// validateCollaborationIdentity keeps transport activation independent from a
// mounted Agent Controller. Foreground Host/Join still validates the live tab;
// a resident Room restored after restart instead proves that its durable owner
// Session is registered as collaboration metadata. Agent execution continues
// to use collaborationAgentReady and therefore cannot run without a Controller.
func (c *desktopCollaboration) validateCollaborationIdentity(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if c == nil || sessionID == "" || sessionID != strings.TrimSpace(c.ownerSessionID) {
		return fmt.Errorf("session %q does not own this collaboration runtime", sessionID)
	}
	if c.app != nil && c.app.sessionByID(sessionID) != nil {
		return c.validateLocalController(sessionID)
	}
	path := collaborationOwnerSessionPath(c.ownerSessionPath)
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil {
		return fmt.Errorf("load collaboration Session identity: %w", err)
	}
	if path == "" || !ok || meta.SessionKind != agent.SessionKindCollaboration {
		return fmt.Errorf("session %q is not a registered collaboration Session", sessionID)
	}
	return nil
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
		return false, nil
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

func collaborationRoomInstanceKey(conn *collaborationConnection) string {
	if conn == nil {
		return ""
	}
	return strings.TrimSpace(conn.roomInstanceKey)
}

func collaborationShareAuthorityKey(conn *collaborationConnection) string {
	if conn == nil {
		return ""
	}
	return strings.TrimSpace(conn.shareAuthorityKey)
}

func establishCollaborationRoomInstance(conn *collaborationConnection) (string, string) {
	if conn == nil || strings.TrimSpace(conn.room) == "" {
		return "", ""
	}
	if roomKey, shareKey := strings.TrimSpace(conn.roomInstanceKey), strings.TrimSpace(conn.shareAuthorityKey); roomKey != "" && shareKey != "" {
		return roomKey, shareKey
	}
	hostKey := strings.TrimSpace(conn.hostKey)
	trustedHostKey := hostKey != "" && (conn.mode == "host" || len(conn.relayBindings) > 0)
	if trustedHostKey {
		conn.roomInstanceKey = stableCollaborationID("room_instance", hostKey+"\x00"+strings.TrimSpace(conn.room))
		// Relay authenticates the host key, and a local host owns its key.
		conn.shareAuthorityKey = conn.roomInstanceKey
	} else {
		// LAN currently has no authenticated host identity. Both received state
		// and local source authority stay connection-scoped so a replacement host
		// cannot replay an old offer to expose a previous Room attachment.
		conn.roomInstanceKey = newCollaborationRequestID("room_instance")
		conn.shareAuthorityKey = newCollaborationRequestID("share_authority")
	}
	return conn.roomInstanceKey, conn.shareAuthorityKey
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
	c.rebuildFileOffersLocked(c.state.Snapshot)
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
	for fileID := range c.transferCancel {
		c.cancelFileTransferLocked(fileID)
		if transfer := c.transfers[fileID]; transfer != nil && transfer.Status != "completed" {
			transfer.Status, transfer.Error, transfer.Retryable = "waiting_sender", "Room 连接已切换", true
		}
	}
	c.persistLocked()
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
	roomInstance, shareAuthority := establishCollaborationRoomInstance(conn)
	c.mu.Lock()
	config := normalizeCollaborationAgentConfig(c.state.AgentConfig, conn.agentName)
	previous := c.conn
	c.autoScanClosed = false
	c.autoScanAgain = false
	c.conn = conn
	c.switchFileTransfersLocked(roomInstance)
	c.relayChunkCache = map[string]collaborationRelayChunk{}
	c.relayChunkBytes, c.relayChunkClock = 0, 0
	c.roomInstance = roomInstance
	c.shareAuthority = shareAuthority
	status := "connected"
	if conn.rejoined || len(c.outbox) > 0 || len(c.recoveredRuns) > 0 {
		status = "syncing"
	}
	c.state = CollaborationState{
		Status:          status,
		Mode:            conn.mode,
		Host:            conn.hostName,
		Port:            conn.port,
		Room:            conn.room,
		MemberID:        conn.memberID,
		AgentID:         conn.agentID,
		SessionID:       conn.sessionID,
		Snapshot:        conn.initialSnapshot,
		OutboxCount:     len(c.outbox),
		AgentConfig:     config,
		Routes:          publicCollaborationRoutes(conn.routes),
		Advertisement:   conn.advertisement,
		ProtocolVersion: conn.protocolVersion,
	}
	c.rebuildFileOffersLocked(c.state.Snapshot)
	if conn.routeError != "" {
		c.state.LastError = conn.routeError
		c.state.Retryable = true
	}
	c.recoverInterruptedRunsLocked(conn)
	c.state.OutboxCount = len(c.outbox)
	c.persistLocked()
	state := cloneCollaborationState(c.state)
	c.mu.Unlock()
	if previous != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = previous.close(closeCtx, false)
		cancel()
	}
	// Stop any previous scheduler before starting a new one.
	if c.schedulerCancel != nil {
		c.schedulerCancel()
		c.schedulerCancel = nil
	}
	if c.scheduler != nil {
		schedCtx, schedCancel := context.WithCancel(c.app.bootContext())
		c.schedulerCancel = schedCancel
		go c.scheduler.run(schedCtx, c)
	}

	c.ensureConnectionLoop(conn)
	go c.restoreFileOrigins(conn)
	c.signalAutoReceiveFiles()
	go c.resumeWaitingFileTransfers()
	c.emitState()
	c.observeUnread()
	go c.startNextQueuedAgent(conn.sessionID)
	if c.scheduler != nil {
		c.scheduler.signal(wakeSignal)
	}
	return state, nil
}
