package collab

import (
	"encoding/json"
	"time"
)

type MemberStatus string

const (
	MemberOnline  MemberStatus = "online"
	MemberOffline MemberStatus = "offline"
)

type AgentStatus string

const (
	AgentOffline         AgentStatus = "offline"
	AgentIdle            AgentStatus = "idle"
	AgentRunning         AgentStatus = "running"
	AgentWaitingApproval AgentStatus = "waiting_approval"
	AgentError           AgentStatus = "error"
)

type AgentRunStatus string

const (
	RunQueued          AgentRunStatus = "queued"
	RunRunning         AgentRunStatus = "running"
	RunWaitingApproval AgentRunStatus = "waiting_approval"
	RunCompleted       AgentRunStatus = "completed"
	RunFailed          AgentRunStatus = "failed"
	RunCancelled       AgentRunStatus = "cancelled"
	RunInterrupted     AgentRunStatus = "interrupted"
)

type ContributionKind string

const (
	ContributionProposal    ContributionKind = "proposal"
	ContributionDecision    ContributionKind = "decision"
	ContributionDeliverable ContributionKind = "deliverable"
	ContributionIssue       ContributionKind = "issue"
	ContributionFixReady    ContributionKind = "fix_ready"
	ContributionVerified    ContributionKind = "verified"
	ContributionQuestion    ContributionKind = "question"
)

type TimelineType string

const (
	TimelineChat         TimelineType = "chat"
	TimelineContribution TimelineType = "contribution"
	TimelineAgentRequest TimelineType = "agent_request"
	TimelineAgentRun     TimelineType = "agent_run"
	TimelineAgentResult  TimelineType = "agent_result"
	TimelineFile         TimelineType = "file"
	TimelineReaction     TimelineType = "reaction"
	TimelineSystem       TimelineType = "system"
)

type CommandType string

const (
	CommandPostChat            CommandType = "chat.post"
	CommandPublishContribution CommandType = "contribution.publish"
	CommandAddReaction         CommandType = "reaction.add"
	CommandCreateAgentRequest  CommandType = "agent_request.create"
	CommandDecideAgentRequest  CommandType = "agent_request.decide"
	CommandPublishAgentRun     CommandType = "agent_run.publish"
	CommandPublishAgentResult  CommandType = "agent_result.publish"
	CommandUpdateAgent         CommandType = "agent.update"
	CommandOfferFile           CommandType = "file.offer"
	CommandRevokeFile          CommandType = "file.revoke"
)

type AgentRequestStatus string

const (
	RequestPending  AgentRequestStatus = "pending"
	RequestAccepted AgentRequestStatus = "accepted"
	RequestRejected AgentRequestStatus = "rejected"
)

type Room struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	TokenRequired  bool      `json:"tokenRequired"`
	CreatedAt      time.Time `json:"createdAt"`
	LatestSequence uint64    `json:"latestSequence"`
}

type AgentDescriptor struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Avatar       string      `json:"avatar,omitempty"`
	Role         string      `json:"role,omitempty"`
	Capabilities []string    `json:"capabilities,omitempty"`
	Status       AgentStatus `json:"status"`
}

type Member struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Avatar     string          `json:"avatar,omitempty"`
	Role       string          `json:"role,omitempty"`
	Agent      AgentDescriptor `json:"agent"`
	Status     MemberStatus    `json:"status"`
	JoinedAt   time.Time       `json:"joinedAt"`
	LastSeenAt time.Time       `json:"lastSeenAt"`
}

type ChatMessage struct {
	ID               string    `json:"id"`
	AuthorID         string    `json:"authorId"`
	Text             string    `json:"text"`
	MentionMemberIDs []string  `json:"mentionMemberIds,omitempty"`
	MentionAgentIDs  []string  `json:"mentionAgentIds,omitempty"`
	Revision         uint64    `json:"revision"`
	CreatedAt        time.Time `json:"createdAt"`
}

type Contribution struct {
	ID           string            `json:"id"`
	AuthorID     string            `json:"authorId"`
	Kind         ContributionKind  `json:"kind"`
	Title        string            `json:"title,omitempty"`
	Body         string            `json:"body"`
	Scope        []string          `json:"scope,omitempty"`
	TargetIDs    []string          `json:"targetIds,omitempty"`
	RelatedItem  string            `json:"relatedItem,omitempty"`
	Dependencies []string          `json:"dependencies,omitempty"`
	ActionNeeded bool              `json:"actionNeeded,omitempty"`
	Revision     uint64            `json:"revision"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
}

type AgentRequest struct {
	ID             string             `json:"id"`
	AuthorID       string             `json:"authorId"`
	TargetMemberID string             `json:"targetMemberId"`
	Instruction    string             `json:"instruction"`
	ReferenceIDs   []string           `json:"referenceIds,omitempty"`
	Status         AgentRequestStatus `json:"status"`
	DecisionBy     string             `json:"decisionBy,omitempty"`
	DecisionNote   string             `json:"decisionNote,omitempty"`
	CreatedAt      time.Time          `json:"createdAt"`
	UpdatedAt      time.Time          `json:"updatedAt"`
}

type AgentRun struct {
	ID           string         `json:"id"`
	OwnerID      string         `json:"ownerId"`
	AgentID      string         `json:"agentId"`
	CommandID    string         `json:"commandId"`
	RequestRef   string         `json:"requestRef,omitempty"`
	Instruction  string         `json:"instruction,omitempty"`
	ReferenceIDs []string       `json:"referenceIds,omitempty"`
	Status       AgentRunStatus `json:"status"`
	Summary      string         `json:"summary,omitempty"`
	Error        string         `json:"error,omitempty"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

type AgentResult struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"ownerId"`
	AgentID      string    `json:"agentId"`
	RunID        string    `json:"runId"`
	Revision     uint64    `json:"revision"`
	Summary      string    `json:"summary"`
	ReferenceIDs []string  `json:"referenceIds,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

type FileOffer struct {
	ID           string     `json:"id"`
	OwnerID      string     `json:"ownerId"`
	Name         string     `json:"name"`
	Size         int64      `json:"size"`
	MIME         string     `json:"mime,omitempty"`
	SHA256       string     `json:"sha256"`
	ManifestHash string     `json:"manifestHash"`
	ChunkSize    int64      `json:"chunkSize"`
	ChunkCount   int        `json:"chunkCount"`
	Revision     uint64     `json:"revision"`
	CreatedAt    time.Time  `json:"createdAt"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
}

type Reaction struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"authorId"`
	TargetID  string    `json:"targetId"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"createdAt"`
}

type SystemEvent struct {
	Kind     string `json:"kind"`
	MemberID string `json:"memberId,omitempty"`
	Message  string `json:"message,omitempty"`
}

// TimelineItem is a typed union. Exactly one payload matching Type is set.
type TimelineItem struct {
	ID           string        `json:"id"`
	Sequence     uint64        `json:"sequence"`
	Type         TimelineType  `json:"type"`
	Chat         *ChatMessage  `json:"chat,omitempty"`
	Contribution *Contribution `json:"contribution,omitempty"`
	AgentRequest *AgentRequest `json:"agentRequest,omitempty"`
	AgentRun     *AgentRun     `json:"agentRun,omitempty"`
	AgentResult  *AgentResult  `json:"agentResult,omitempty"`
	File         *FileOffer    `json:"file,omitempty"`
	Reaction     *Reaction     `json:"reaction,omitempty"`
	System       *SystemEvent  `json:"system,omitempty"`
}

type RoomEvent struct {
	EventID     string          `json:"eventId"`
	Sequence    uint64          `json:"sequence"`
	Room        string          `json:"room"`
	Type        string          `json:"type"`
	ActorID     string          `json:"actorId,omitempty"`
	RequestID   string          `json:"requestId"`
	CausationID string          `json:"causationId,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	Payload     json.RawMessage `json:"payload"`
}

type Snapshot struct {
	Room           Room           `json:"room"`
	Members        []Member       `json:"members"`
	Timeline       []TimelineItem `json:"timeline"`
	LatestSequence uint64         `json:"latestSequence"`
}

type CreateRoomInput struct {
	RequestID   string `json:"requestId"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Token       string `json:"token,omitempty"`
}

type JoinInput struct {
	RequestID     string           `json:"requestId"`
	Room          string           `json:"room"`
	Token         string           `json:"token,omitempty"`
	Member        MemberDescriptor `json:"member"`
	ResumeSession string           `json:"resumeSession,omitempty"`
}

type MemberDescriptor struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Avatar string          `json:"avatar,omitempty"`
	Role   string          `json:"role,omitempty"`
	Agent  AgentDescriptor `json:"agent"`
}

type JoinResult struct {
	Member            Member `json:"member"`
	ConnectionSession string `json:"connectionSession"`
	LatestSequence    uint64 `json:"latestSequence"`
	Rejoined          bool   `json:"rejoined"`
}

type SessionInput struct {
	RequestID string `json:"requestId"`
	Room      string `json:"room"`
	MemberID  string `json:"memberId"`
	Session   string `json:"connectionSession"`
}

type SweepInput struct {
	RequestID string    `json:"requestId"`
	Room      string    `json:"room"`
	Before    time.Time `json:"before"`
}

type CommandEnvelope struct {
	RequestID string  `json:"requestId"`
	Room      string  `json:"room"`
	MemberID  string  `json:"memberId"`
	Session   string  `json:"connectionSession,omitempty"`
	QueuedAt  string  `json:"queuedAt,omitempty"`
	Command   Command `json:"command"`
}

// Command is a typed union. Exactly one input matching Type must be set.
type Command struct {
	Type            CommandType               `json:"type"`
	Chat            *PostChatInput            `json:"chat,omitempty"`
	Contribution    *PublishContributionInput `json:"contribution,omitempty"`
	Reaction        *AddReactionInput         `json:"reaction,omitempty"`
	AgentRequest    *CreateAgentRequestInput  `json:"agentRequest,omitempty"`
	RequestDecision *DecideAgentRequestInput  `json:"requestDecision,omitempty"`
	AgentRun        *PublishAgentRunInput     `json:"agentRun,omitempty"`
	AgentResult     *PublishAgentResultInput  `json:"agentResult,omitempty"`
	AgentUpdate     *UpdateAgentInput         `json:"agentUpdate,omitempty"`
	FileOffer       *OfferFileInput           `json:"fileOffer,omitempty"`
	FileRevoke      *RevokeFileInput          `json:"fileRevoke,omitempty"`
}

type PostChatInput struct {
	Text             string   `json:"text"`
	MentionMemberIDs []string `json:"mentionMemberIds,omitempty"`
	MentionAgentIDs  []string `json:"mentionAgentIds,omitempty"`
}

type PublishContributionInput struct {
	Kind         ContributionKind  `json:"kind"`
	Title        string            `json:"title,omitempty"`
	Body         string            `json:"body"`
	Scope        []string          `json:"scope,omitempty"`
	TargetIDs    []string          `json:"targetIds,omitempty"`
	RelatedItem  string            `json:"relatedItem,omitempty"`
	Dependencies []string          `json:"dependencies,omitempty"`
	ActionNeeded bool              `json:"actionNeeded,omitempty"`
	Revision     uint64            `json:"revision,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type AddReactionInput struct {
	TargetID string `json:"targetId"`
	Kind     string `json:"kind"`
}

type CreateAgentRequestInput struct {
	TargetMemberID string   `json:"targetMemberId"`
	Instruction    string   `json:"instruction"`
	ReferenceIDs   []string `json:"referenceIds,omitempty"`
}

type DecideAgentRequestInput struct {
	AgentRequestID string             `json:"agentRequestId"`
	Decision       AgentRequestStatus `json:"decision"`
	Note           string             `json:"note,omitempty"`
}

type PublishAgentRunInput struct {
	RunID        string         `json:"runId"`
	AgentID      string         `json:"agentId"`
	CommandID    string         `json:"commandId"`
	RequestRef   string         `json:"requestRef,omitempty"`
	Instruction  string         `json:"instruction,omitempty"`
	ReferenceIDs []string       `json:"referenceIds,omitempty"`
	Status       AgentRunStatus `json:"status"`
	Summary      string         `json:"summary,omitempty"`
	Error        string         `json:"error,omitempty"`
}

type PublishAgentResultInput struct {
	ResultID     string   `json:"resultId"`
	AgentID      string   `json:"agentId"`
	RunID        string   `json:"runId"`
	Revision     uint64   `json:"revision,omitempty"`
	Summary      string   `json:"summary"`
	ReferenceIDs []string `json:"referenceIds,omitempty"`
}

type UpdateAgentInput struct {
	Name         string  `json:"name"`
	Avatar       *string `json:"avatar,omitempty"`
	MemberName   string  `json:"memberName,omitempty"`
	MemberAvatar *string `json:"memberAvatar,omitempty"`
}

type OfferFileInput struct {
	FileID       string `json:"fileId"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	MIME         string `json:"mime,omitempty"`
	SHA256       string `json:"sha256"`
	ManifestHash string `json:"manifestHash"`
	ChunkSize    int64  `json:"chunkSize"`
	ChunkCount   int    `json:"chunkCount"`
}

type RevokeFileInput struct {
	FileID string `json:"fileId"`
}

type CommandReceipt struct {
	RequestID      string   `json:"requestId"`
	EventIDs       []string `json:"eventIds"`
	LatestSequence uint64   `json:"latestSequence"`
	Duplicate      bool     `json:"duplicate,omitempty"`
}
