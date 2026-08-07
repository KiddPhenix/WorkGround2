export type CollaborationConnectionStatus =
  | "disconnected"
  | "connecting"
  | "syncing"
  | "connected"
  | "reconnecting"
  | "failed";

export type CollaborationRunPhase = "idle" | "running" | "waiting_approval" | "stopping";

export interface CollaborationCurrentRun {
  sessionId: string;
  runId: string;
  phase: CollaborationRunPhase;
  instruction: string;
  progress?: string;
  startedAt?: number;
  queueCount: number;
}

export type CollaborationItemKind =
  | "chat"
  | "contribution"
  | "agent_command"
  | "agent_request"
  | "agent_result"
  | "file"
  | "reaction"
  | "system";

export type CollaborationSyncStatus = "synced" | "pending" | "failed";
export type CollaborationAgentStatus = "idle" | "running" | "waiting" | "completed" | "error" | "offline";
export type CollaborationIntentClass = "chat" | "uncertain" | "self_agent";
export type CollaborationRecognitionMode = "message" | "interval" | "off";
export type CollaborationToolApprovalMode = "ask" | "auto" | "yolo";
export type CollaborationRoomVisibility = "private" | "unlisted" | "public";
export type CollaborationRouteStatus = "disabled" | "connecting" | "connected" | "degraded" | "failed";
export type CollaborationAdvertisementStatus = "disabled" | "pending" | "published" | "failed" | "revoking" | "revoked";

export interface CollaborationRelayConfigItem {
  id: string;
  name?: string;
  url: string;
  enabled: boolean;
  priority: number;
  discovery: boolean;
  allowInsecure?: boolean;
  accessTokenEnv?: string;
}

export interface CollaborationRelayConfig {
  preferLAN: boolean;
  connectTimeoutSeconds?: number;
  routeStableSeconds?: number;
  relays: CollaborationRelayConfigItem[];
}

export interface CollaborationRouteInput {
  id?: string;
  protocolVersion?: 1 | 2;
  kind: "lan" | "relay";
  host?: string;
  port?: number;
  relayId?: string;
  url?: string;
  tunnelId?: string;
  guestCapability?: string;
  priority?: number;
}

export interface CollaborationRouteState extends CollaborationRouteInput {
  id: string;
  status: CollaborationRouteStatus;
  active: boolean;
  priority: number;
  latencyMs?: number;
  lastError?: string;
  retryable?: boolean;
}

export interface CollaborationAdvertisementRelayState {
  relayId: string;
  status: CollaborationAdvertisementStatus;
  lastError?: string;
  retryable?: boolean;
}

export interface CollaborationAdvertisementState {
  visibility: CollaborationRoomVisibility;
  revision: number;
  relays: CollaborationAdvertisementRelayState[];
}

export interface RoomAdvertisementInput {
  name: string;
  description?: string;
  tags?: string[];
  capacity?: number;
  showOnlineCount?: boolean;
}

export interface CollaborationRoomQueryInput {
  query?: string;
  tag?: string;
  relayIds?: string[];
  cursor?: string;
  limit?: number;
}

export interface CollaborationRoomQueryItem {
  publicRoomId: string;
  room: string;
  name: string;
  description?: string;
  tags?: string[];
  requiresToken: boolean;
  onlineCount?: number;
  capacity?: number;
  hostKey: string;
  routes: CollaborationRouteInput[];
  joinRef?: string;
  expiresAt?: string;
}

export interface CollaborationRoomQueryResult {
  rooms: CollaborationRoomQueryItem[];
  nextCursor?: string;
}

export interface CollaborationAgentPromptOption {
  label: string;
  description?: string;
}

export interface CollaborationAgentPromptQuestion {
  id: string;
  header?: string;
  prompt: string;
  options: CollaborationAgentPromptOption[];
  multi?: boolean;
}

export interface CollaborationAgentPrompt {
  runId: string;
  kind: "approval" | "ask";
  id: string;
  tool?: string;
  subject?: string;
  reason?: string;
  questions?: CollaborationAgentPromptQuestion[];
}

export interface CollaborationQuestionAnswer {
  questionId: string;
  selected: string[];
}

export interface CollaborationAgentRunResponse {
  allow?: boolean;
  session?: boolean;
  persist?: boolean;
  answering?: boolean;
  answers?: CollaborationQuestionAnswer[];
}

export interface CollaborationAgentConfig {
  alias: string;
  autoRespondQuestions: boolean;
  autoRespondRequests: boolean;
  autoRespondAgents: boolean;
  agentResponseIntervalSeconds: number;
  agentClockTurns: number;
  agentClockUnlimited: boolean;
  agentClockWoundAt?: string;
  recognitionMode: CollaborationRecognitionMode;
  contextRefs?: string[];
}

export interface CollaborationAgentSource {
  id: string;
  kind: "agents" | "skill";
  name: string;
  path: string;
  description?: string;
  scope?: string;
  runAs?: string;
  protected?: boolean;
  available: boolean;
}

export interface CollaborationAgentSources {
  agents: CollaborationAgentSource[];
  skills: CollaborationAgentSource[];
}

export interface CollaborationQueuedTask {
  id: string;
  requestId: string;
  instruction: string;
  referenceIds: string[];
  agentRequestId?: string;
  queuedAt: string;
}

export interface CollaborationIntentResult {
  intent: CollaborationIntentClass;
  source: "rule" | "llm" | "fallback";
  error?: string;
  retryable?: boolean;
}

export interface CollaborationMember {
  id: string;
  name: string;
  avatar?: string;
  role?: string;
  online: boolean;
  isSelf?: boolean;
  agent: {
    id: string;
    name: string;
    avatar?: string;
    status: CollaborationAgentStatus;
    sessionId?: string;
  };
}

export interface CollaborationAgentHandoff {
  targetAgentId: string;
  instruction: string;
  referenceIds: string[];
  reason?: string;
  expectedOutcome?: string;
  requiresResponse: boolean;
}

export interface CollaborationTimelineItem {
  id: string;
  sequence: number;
  revision: number;
  kind: CollaborationItemKind;
  contributionKind?: string;
  actorId: string;
  actorName: string;
  actorAgent?: boolean;
  targetMemberId?: string;
  mentionMemberIds?: string[];
  mentionAgentIds?: string[];
  text: string;
  createdAt: string;
  referenceIds: string[];
  syncStatus?: CollaborationSyncStatus;
  localPending?: boolean;
  requestID?: string;
  requestStatus?: "waiting" | "accepted" | "rejected" | "completed";
  agentRunStatus?: "queued" | "running" | "waiting_approval" | "completed" | "failed" | "cancelled" | "interrupted";
  agentRunSummary?: string;
  agentRunOutput?: string;
  agentRunError?: string;
  agentCommandId?: string;
  agentRunId?: string;
  handoffs?: CollaborationAgentHandoff[];
  systemKind?: string;
  reactions?: Record<string, string[]>;
  fileName?: string;
  fileSize?: number;
  fileMime?: string;
  fileSHA256?: string;
  fileRevoked?: boolean;
}

export type CollaborationFileTransferStatus = "preparing" | "pending" | "available" | "unavailable" | "source_changed" | "revoked" | "negotiating" | "downloading" | "paused" | "waiting_sender" | "verifying" | "completed" | "failed";

export interface CollaborationFileTransfer {
  id: string;
  fileId: string;
  direction: "share" | "receive";
  name: string;
  status: CollaborationFileTransferStatus;
  transferred: number;
  total: number;
  destination?: string;
  error?: string;
  retryable?: boolean;
}

export interface CollaborationRoom {
  room: string;
  title?: string;
  description?: string;
  host: string;
  port: number;
  tokenRequired?: boolean;
  latestSequence: number;
}

export interface CollaborationWorkspaceOption {
  root: string;
  name: string;
}

export interface CollaborationState {
  status: CollaborationConnectionStatus;
  protocolVersion?: 1 | 2;
  mode?: "host" | "client";
  room?: CollaborationRoom;
  selfMemberId?: string;
  selfSessionId?: string;
  members: CollaborationMember[];
  timeline: CollaborationTimelineItem[];
  lastError?: string;
  retryable?: boolean;
  unsyncedCount?: number;
  transfers?: CollaborationFileTransfer[];
  agentConfig?: CollaborationAgentConfig;
  agentSources?: CollaborationAgentSources;
  queuedTasks?: CollaborationQueuedTask[];
  toolApprovalMode?: CollaborationToolApprovalMode;
  agentPrompt?: CollaborationAgentPrompt;
  routes?: CollaborationRouteState[];
  advertisement?: CollaborationAdvertisementState;
  currentRun?: CollaborationCurrentRun;
}

export interface CollaborationInvite {
  version?: 1 | 2;
  invite?: string;
  hosts: string[];
  port: number;
  room: string;
  token?: string;
  hostKey?: string;
  routes?: CollaborationRouteInput[];
}

export interface HostCollaborationRoomInput {
  listenHost: string;
  port: number;
  room: string;
  protocolVersion?: 1 | 2;
  roomName?: string;
  description?: string;
  token?: string;
  memberID?: string;
  memberName: string;
  memberAvatar?: string;
  memberRole?: string;
  agentID?: string;
  agentName: string;
  agentAvatar?: string;
  agentRole?: string;
  sessionID: string;
  lanEnabled?: boolean;
  relayIDs?: string[];
  preferLAN?: boolean;
  visibility?: CollaborationRoomVisibility;
  advertisement?: RoomAdvertisementInput;
}

export interface JoinCollaborationRoomInput {
  host: string;
  port: number;
  room: string;
  token?: string;
  memberID?: string;
  memberName: string;
  memberAvatar?: string;
  memberRole?: string;
  agentID?: string;
  agentName: string;
  agentAvatar?: string;
  agentRole?: string;
  sessionID: string;
  invite?: string;
  routes?: CollaborationRouteInput[];
  hostKey?: string;
  joinRef?: string;
}

export interface PostCollaborationMessageInput {
  requestID: string;
  sessionID?: string;
  kind: "chat" | "contribution" | "agent_request" | "reaction";
  text: string;
  targetMemberID?: string;
  targetItemID?: string;
  referenceIDs?: string[];
  contributionKind?: string;
  reactionKind?: string;
  mentionMemberIDs?: string[];
  mentionAgentIDs?: string[];
}

export interface StartCollaborationAgentInput {
  requestID: string;
  sessionID: string;
  instruction: string;
  referenceIDs: string[];
  agentRequestID?: string;
  automatic?: boolean;
}

export interface RespondCollaborationRequestInput {
  requestID: string;
  agentRequestID: string;
  action: "accept" | "reject";
  instruction?: string;
  sessionID: string;
  automatic?: boolean;
}

export interface UpdateCollaborationAgentConfigInput {
  requestID: string;
  config: CollaborationAgentConfig;
}

export interface UpdateCollaborationProfileInput {
  requestID: string;
  memberName: string;
  memberAvatar?: string;
  agentName: string;
  agentAvatar?: string;
}

export interface CollaborationActionResult {
  ok: boolean;
  requestID?: string;
  code?: string;
  item?: CollaborationTimelineItem;
  state?: CollaborationState;
  error?: string;
  retryable?: boolean;
  queued?: boolean;
  duplicate?: boolean;
  receipt?: { requestID?: string; eventIDs?: string[]; latestSequence?: number; duplicate?: boolean };
}

export interface PendingIntent {
  messageId: string;
  revision: number;
  instruction: string;
  deadline: number;
  status: "pending" | "dismissed" | "starting" | "failed";
  error?: string;
}

export interface CollaborationTransport {
  getState(): Promise<CollaborationState>;
  retry(): Promise<CollaborationState>;
  host(input: HostCollaborationRoomInput): Promise<CollaborationState>;
  join(input: JoinCollaborationRoomInput): Promise<CollaborationState>;
  invite(): Promise<CollaborationInvite>;
  leave(): Promise<void>;
  getRelayConfig?(): Promise<CollaborationRelayConfig>;
  setRelayConfig?(input: CollaborationRelayConfig): Promise<CollaborationRelayConfig>;
  listRooms?(input: CollaborationRoomQueryInput): Promise<CollaborationRoomQueryResult>;
  classifyIntent?(text: string): Promise<CollaborationIntentResult>;
  post(input: PostCollaborationMessageInput): Promise<CollaborationActionResult>;
  startAgent(input: StartCollaborationAgentInput): Promise<CollaborationActionResult>;
  cancelQueuedTask(taskId: string): Promise<CollaborationActionResult>;
  stopCurrentRun?(runId: string): Promise<void>;
  respondAgentRun?(runId: string, response: CollaborationAgentRunResponse): Promise<CollaborationActionResult>;
  respond(input: RespondCollaborationRequestInput): Promise<CollaborationActionResult>;
  updateAgentConfig(input: UpdateCollaborationAgentConfigInput): Promise<CollaborationState>;
  updateProfile(input: UpdateCollaborationProfileInput): Promise<CollaborationState>;
  updateToolApprovalMode?(mode: CollaborationToolApprovalMode): Promise<CollaborationState>;
  shareFiles(paths: string[]): Promise<CollaborationFileTransfer[]>;
  receiveFile(fileId: string): Promise<CollaborationFileTransfer>;
  pauseFile(fileId: string): Promise<CollaborationFileTransfer>;
  resumeFile(fileId: string): Promise<CollaborationFileTransfer>;
  revokeFile(fileId: string): Promise<CollaborationActionResult>;
  openFile(fileId: string): Promise<void>;
  revealFile(fileId: string): Promise<void>;
  subscribeState(listener: (state: CollaborationState) => void): () => void;
  subscribeEvent(listener: (item: CollaborationTimelineItem) => void): () => void;
}
