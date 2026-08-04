export type CollaborationConnectionStatus =
  | "disconnected"
  | "connecting"
  | "syncing"
  | "connected"
  | "reconnecting"
  | "failed";

export type CollaborationItemKind =
  | "chat"
  | "contribution"
  | "agent_command"
  | "agent_request"
  | "agent_result"
  | "reaction"
  | "system";

export type CollaborationSyncStatus = "synced" | "pending" | "failed";
export type CollaborationAgentStatus = "idle" | "running" | "waiting" | "completed" | "error" | "offline";
export type CollaborationIntentClass = "chat" | "uncertain" | "self_agent";

export interface CollaborationIntentResult {
  intent: CollaborationIntentClass;
  source: "rule" | "llm" | "fallback";
  error?: string;
  retryable?: boolean;
}

export interface CollaborationMember {
  id: string;
  name: string;
  role?: string;
  online: boolean;
  isSelf?: boolean;
  agent: {
    id: string;
    name: string;
    status: CollaborationAgentStatus;
    sessionId?: string;
  };
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
  text: string;
  createdAt: string;
  referenceIds: string[];
  syncStatus?: CollaborationSyncStatus;
  localPending?: boolean;
  requestID?: string;
  requestStatus?: "waiting" | "accepted" | "rejected" | "completed";
  agentRunStatus?: "queued" | "running" | "waiting_approval" | "completed" | "failed" | "cancelled" | "interrupted";
  agentRunSummary?: string;
  agentRunError?: string;
  systemKind?: string;
  reactions?: Record<string, string[]>;
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

export interface CollaborationState {
  status: CollaborationConnectionStatus;
  mode?: "host" | "client";
  room?: CollaborationRoom;
  selfMemberId?: string;
  selfSessionId?: string;
  members: CollaborationMember[];
  timeline: CollaborationTimelineItem[];
  lastError?: string;
  retryable?: boolean;
  unsyncedCount?: number;
}

export interface CollaborationInvite {
  hosts: string[];
  port: number;
  room: string;
  token?: string;
}

export interface HostCollaborationRoomInput {
  listenHost: string;
  port: number;
  room: string;
  roomName?: string;
  description?: string;
  token?: string;
  memberID?: string;
  memberName: string;
  memberRole?: string;
  agentID?: string;
  agentName: string;
  agentRole?: string;
  sessionID: string;
}

export interface JoinCollaborationRoomInput {
  host: string;
  port: number;
  room: string;
  token?: string;
  memberID?: string;
  memberName: string;
  memberRole?: string;
  agentID?: string;
  agentName: string;
  agentRole?: string;
  sessionID: string;
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
}

export interface StartCollaborationAgentInput {
  requestID: string;
  sessionID: string;
  instruction: string;
  referenceIDs: string[];
  agentRequestID?: string;
}

export interface RespondCollaborationRequestInput {
  requestID: string;
  agentRequestID: string;
  action: "accept" | "reject";
  instruction?: string;
  sessionID: string;
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
  classifyIntent?(text: string): Promise<CollaborationIntentResult>;
  post(input: PostCollaborationMessageInput): Promise<CollaborationActionResult>;
  startAgent(input: StartCollaborationAgentInput): Promise<CollaborationActionResult>;
  respond(input: RespondCollaborationRequestInput): Promise<CollaborationActionResult>;
  subscribeState(listener: (state: CollaborationState) => void): () => void;
  subscribeEvent(listener: (item: CollaborationTimelineItem) => void): () => void;
}
