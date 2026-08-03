import { app, onCollaborationEvent, onCollaborationState } from "../lib/bridge";
import { t } from "../lib/i18n";
import type {
  CollaborationActionResult,
  CollaborationMember,
  CollaborationState,
  CollaborationTimelineItem,
  CollaborationTransport,
  HostCollaborationRoomInput,
  JoinCollaborationRoomInput,
} from "./types";

function text(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function number(value: unknown, fallback = 0): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function bool(value: unknown, fallback = false): boolean {
  return typeof value === "boolean" ? value : fallback;
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" ? (value as Record<string, unknown>) : {};
}

function list(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

export function normalizeCollaborationMember(value: unknown): CollaborationMember {
  const raw = record(value);
  const agentRaw = record(raw.agent ?? raw.Agent);
  const id = text(raw.id ?? raw.ID ?? raw.memberId ?? raw.MemberID);
  return {
    id,
    name: text(raw.name ?? raw.Name ?? raw.memberName ?? raw.MemberName, id || t("collab.defaultMember")),
    role: text(raw.role ?? raw.Role) || undefined,
    online: text(raw.status ?? raw.Status, "online") === "online" && bool(raw.online ?? raw.Online, true),
    isSelf: bool(raw.isSelf ?? raw.IsSelf),
    agent: {
      id: text(agentRaw.id ?? agentRaw.ID ?? raw.agentId ?? raw.AgentID, `${id}-agent`),
      name: text(agentRaw.name ?? agentRaw.Name ?? raw.agentName ?? raw.AgentName, t("collab.defaultAgent")),
      status: (text(agentRaw.status ?? agentRaw.Status ?? raw.agentStatus ?? raw.AgentStatus, "idle").replace("waiting_approval", "waiting") || "idle") as CollaborationMember["agent"]["status"],
      sessionId: text(agentRaw.sessionId ?? agentRaw.SessionID ?? raw.sessionId ?? raw.SessionID) || undefined,
    },
  };
}

export function normalizeCollaborationItem(value: unknown, memberNames: Map<string, string> = new Map()): CollaborationTimelineItem {
  const raw = record(value);
  const wrappedItem = raw.item ?? raw.Item;
  if (wrappedItem) {
    const normalized = normalizeCollaborationItem(wrappedItem, memberNames);
    const event = record(raw.event ?? raw.Event);
    return {
      ...normalized,
      id: normalized.id || text(event.eventId ?? event.EventID),
      sequence: normalized.sequence || number(event.sequence ?? event.Sequence),
      actorId: normalized.actorId || text(event.actorId ?? event.ActorID),
      createdAt: normalized.createdAt || text(event.createdAt ?? event.CreatedAt, new Date().toISOString()),
    };
  }
  const payloadValue = raw.payload ?? raw.Payload;
  if (payloadValue !== undefined) {
    let decoded: unknown = payloadValue;
    try {
      if (typeof payloadValue === "string") decoded = JSON.parse(payloadValue);
      else if (Array.isArray(payloadValue) && payloadValue.every((entry) => typeof entry === "number")) decoded = JSON.parse(new TextDecoder().decode(new Uint8Array(payloadValue)));
      else if (payloadValue instanceof Uint8Array) decoded = JSON.parse(new TextDecoder().decode(payloadValue));
    } catch { /* malformed payload remains observable as the outer system event */ }
    const inner = record(decoded);
    if (Object.keys(inner).length > 0) {
      const normalized = normalizeCollaborationItem(inner, memberNames);
      return {
        ...normalized,
        id: normalized.id || text(raw.eventId ?? raw.EventID),
        sequence: normalized.sequence || number(raw.sequence ?? raw.Sequence),
        actorId: normalized.actorId || text(raw.actorId ?? raw.ActorID),
        actorName: normalized.actorName === t("collab.defaultMember") ? memberNames.get(text(raw.actorId ?? raw.ActorID)) || normalized.actorName : normalized.actorName,
        createdAt: normalized.createdAt || text(raw.createdAt ?? raw.CreatedAt, new Date().toISOString()),
      };
    }
  }
  const actor = record(raw.actor ?? raw.Actor);
  const typed = record(raw.chat ?? raw.Chat ?? raw.contribution ?? raw.Contribution ?? raw.agentRequest ?? raw.AgentRequest ?? raw.agentRun ?? raw.AgentRun ?? raw.agentResult ?? raw.AgentResult ?? raw.reaction ?? raw.Reaction ?? raw.system ?? raw.System);
  const rawKind = text(raw.kind ?? raw.Kind ?? raw.type ?? raw.Type, "chat");
  const kind = rawKind === "agent_run" ? "agent_command" : rawKind;
  const references = typed.referenceIds ?? typed.ReferenceIDs ?? raw.referenceIds ?? raw.ReferenceIDs ?? raw.references ?? raw.References;
  const id = text(raw.id ?? raw.ID ?? raw.eventId ?? raw.EventID);
  const actorId = text(typed.authorId ?? typed.AuthorID ?? typed.ownerId ?? typed.OwnerID ?? raw.actorId ?? raw.ActorID ?? actor.id ?? actor.ID ?? typed.memberId ?? typed.MemberID);
  const createdAt = text(typed.createdAt ?? typed.CreatedAt ?? typed.updatedAt ?? typed.UpdatedAt ?? raw.createdAt ?? raw.CreatedAt, new Date().toISOString());
  let content = text(typed.text ?? typed.Text ?? typed.body ?? typed.Body ?? typed.instruction ?? typed.Instruction ?? typed.summary ?? typed.Summary ?? typed.message ?? typed.Message ?? raw.text ?? raw.Text ?? raw.content ?? raw.Content);
  if (kind === "reaction" && !content) content = text(typed.kind ?? typed.Kind, "agree");
  if (kind === "system" && !content) content = text(typed.kind ?? typed.Kind, "system");
  return {
    id,
    sequence: number(raw.sequence ?? raw.Sequence),
    revision: number(raw.revision ?? raw.Revision, 1),
    kind: (kind || "chat") as CollaborationTimelineItem["kind"],
    contributionKind: text(typed.kind ?? typed.Kind ?? raw.contributionKind ?? raw.ContributionKind) || undefined,
    actorId,
    actorName: text(raw.actorName ?? raw.ActorName ?? actor.name ?? actor.Name, memberNames.get(actorId) || t("collab.defaultMember")),
    actorAgent: bool(raw.actorAgent ?? raw.ActorAgent, kind === "agent_command" || kind === "agent_result"),
    targetMemberId: text(typed.targetMemberId ?? typed.TargetMemberID ?? raw.targetMemberId ?? raw.TargetMemberID) || undefined,
    text: content,
    createdAt,
    referenceIds: list(references).map((item) => text(item)).filter(Boolean),
    syncStatus: (text(raw.syncStatus ?? raw.SyncStatus) || undefined) as CollaborationTimelineItem["syncStatus"],
    requestStatus: (text(typed.status ?? typed.Status ?? raw.requestStatus ?? raw.RequestStatus).replace("pending", "waiting") || undefined) as CollaborationTimelineItem["requestStatus"],
    reactions: record(raw.reactions ?? raw.Reactions) as Record<string, string[]>,
  };
}

export function normalizeCollaborationState(value: unknown): CollaborationState {
  const raw = record(value);
  const snapshot = record(raw.snapshot ?? raw.Snapshot);
  const roomRaw = record(snapshot.room ?? snapshot.Room);
  const roomName = text(raw.room ?? raw.Room ?? roomRaw.id ?? roomRaw.ID ?? roomRaw.room ?? roomRaw.Room ?? roomRaw.name ?? roomRaw.Name ?? raw.roomName ?? raw.RoomName);
  const members = list(snapshot.members ?? snapshot.Members ?? raw.members ?? raw.Members).map(normalizeCollaborationMember);
  const memberNames = new Map(members.map((member) => [member.id, member.name]));
  const selfMemberId = text(raw.selfMemberId ?? raw.SelfMemberID ?? raw.memberId ?? raw.MemberID);
  for (const member of members) member.isSelf = member.id === selfMemberId;
  return {
    status: (text(raw.status ?? raw.Status, roomName ? "connected" : "disconnected") || "disconnected") as CollaborationState["status"],
    room: roomName
      ? {
          room: roomName,
          title: text(roomRaw.name ?? roomRaw.Name ?? roomRaw.title ?? roomRaw.Title) || undefined,
          description: text(roomRaw.description ?? roomRaw.Description) || undefined,
          host: text(raw.host ?? raw.Host ?? roomRaw.host ?? roomRaw.Host),
          port: number(raw.port ?? raw.Port ?? roomRaw.port ?? roomRaw.Port),
          tokenRequired: bool(roomRaw.tokenRequired ?? roomRaw.TokenRequired),
          latestSequence: number(snapshot.latestSequence ?? snapshot.LatestSequence ?? roomRaw.latestSequence ?? roomRaw.LatestSequence ?? raw.latestSequence ?? raw.LatestSequence),
        }
      : undefined,
    selfMemberId: selfMemberId || undefined,
    selfSessionId: text(raw.selfSessionId ?? raw.SelfSessionID ?? raw.sessionId ?? raw.SessionID) || undefined,
    members,
    timeline: list(snapshot.timeline ?? snapshot.Timeline ?? raw.timeline ?? raw.Timeline ?? raw.items ?? raw.Items).map((item) => normalizeCollaborationItem(item, memberNames)).filter((item) => item.id),
    lastError: text(raw.lastError ?? raw.LastError ?? raw.error ?? raw.Error) || undefined,
    retryable: bool(raw.retryable ?? raw.Retryable, true),
    unsyncedCount: number(raw.outboxCount ?? raw.OutboxCount ?? raw.unsyncedCount ?? raw.UnsyncedCount),
  };
}

export function normalizeCollaborationAction(value: unknown): CollaborationActionResult {
  const raw = record(value);
  const receipt = record(raw.receipt ?? raw.Receipt);
  const queued = bool(raw.queued ?? raw.Queued);
  return {
    ok: queued || bool(raw.ok ?? raw.OK, !text(raw.error ?? raw.Error)),
    requestID: text(raw.requestID ?? raw.RequestID) || undefined,
    item: raw.item || raw.Item ? normalizeCollaborationItem(raw.item ?? raw.Item) : undefined,
    state: raw.state || raw.State ? normalizeCollaborationState(raw.state ?? raw.State) : undefined,
    error: text(raw.error ?? raw.Error) || undefined,
    retryable: bool(raw.retryable ?? raw.Retryable, true),
    queued,
    duplicate: bool(raw.duplicate ?? raw.Duplicate ?? receipt.duplicate ?? receipt.Duplicate),
    receipt: Object.keys(receipt).length ? {
      requestID: text(receipt.requestId ?? receipt.RequestID) || undefined,
      eventIDs: list(receipt.eventIds ?? receipt.EventIDs).map((item) => text(item)).filter(Boolean),
      latestSequence: number(receipt.latestSequence ?? receipt.LatestSequence),
      duplicate: bool(receipt.duplicate ?? receipt.Duplicate),
    } : undefined,
  };
}

export function createWailsCollaborationTransport(): CollaborationTransport {
  let names = new Map<string, string>();
  const normalizeState = (value: unknown) => {
    const state = normalizeCollaborationState(value);
    names = new Map(state.members.map((member) => [member.id, member.name]));
    return state;
  };
  return {
    getState: async () => normalizeState(await app.GetCollaborationState()),
    retry: async () => normalizeState(await app.RetryCollaboration()),
    host: async (input) => normalizeState(await app.HostCollaborationRoom(input)),
    join: async (input) => normalizeState(await app.JoinCollaborationRoom(input)),
    leave: () => app.LeaveCollaborationRoom(),
    post: async (input) => normalizeCollaborationAction(await app.PostCollaborationMessage(input)),
    startAgent: async (input) => normalizeCollaborationAction(await app.StartCollaborationAgent(input)),
    respond: async (input) => normalizeCollaborationAction(await app.RespondCollaborationRequest(input)),
    subscribeState: (listener) => onCollaborationState((payload) => listener(normalizeState(payload))),
    subscribeEvent: (listener) => onCollaborationEvent((payload) => listener(normalizeCollaborationItem(payload, names))),
  };
}

const mockStateListeners = new Set<(state: CollaborationState) => void>();
const mockEventListeners = new Set<(item: CollaborationTimelineItem) => void>();
let mockSequence = 4;
let mockState: CollaborationState = { status: "disconnected", members: [], timeline: [] };

const now = () => new Date().toISOString();
const sampleMembers = (selfName: string, agentName: string, sessionID: string): CollaborationMember[] => [
  { id: "planner", name: "林策划", role: "策划", online: true, agent: { id: "planner-agent", name: "策划 Agent", status: "idle" } },
  { id: "artist", name: "周美术", role: "美术", online: true, agent: { id: "artist-agent", name: "美术 Agent", status: "idle" } },
  { id: "self", name: selfName, role: "程序", online: true, isSelf: true, agent: { id: "self-agent", name: agentName, status: "idle", sessionId: sessionID } },
];

function sampleTimeline(): CollaborationTimelineItem[] {
  return [
    { id: "sample-1", sequence: 1, revision: 1, kind: "agent_result", actorId: "artist", actorName: "周美术", actorAgent: true, text: "已完成视觉方案与资源清单，关键资源预览见附件。", createdAt: now(), referenceIds: [] },
    { id: "sample-2", sequence: 2, revision: 1, kind: "contribution", contributionKind: "issue", actorId: "self", actorName: "陈程序", text: "联调自测发现 2 个问题：资源命名不符合规范，部分部件加载失败。", createdAt: now(), referenceIds: [], syncStatus: "synced" },
    { id: "sample-3", sequence: 3, revision: 1, kind: "chat", actorId: "planner", actorName: "林策划", text: "问题 1 需要统一命名规范；问题 2 建议增加兜底占位和降级展示。", createdAt: now(), referenceIds: [] },
    { id: "sample-4", sequence: 4, revision: 1, kind: "agent_result", actorId: "self", actorName: "陈程序", actorAgent: true, text: "检查完成：12 个角色资源中发现 3 个问题，建议统一重命名并更新资源映射表。", createdAt: now(), referenceIds: ["sample-2"] },
  ];
}

function emitMockState() {
  const snapshot = { ...mockState, members: [...mockState.members], timeline: [...mockState.timeline] };
  for (const listener of mockStateListeners) listener(snapshot);
}

function emitMockItem(item: CollaborationTimelineItem) {
  mockState = { ...mockState, timeline: [...mockState.timeline, item], room: mockState.room ? { ...mockState.room, latestSequence: item.sequence } : undefined };
  for (const listener of mockEventListeners) listener(item);
  emitMockState();
}

function connectMock(input: HostCollaborationRoomInput | JoinCollaborationRoomInput): CollaborationState {
  const host = "host" in input ? input.host : input.listenHost;
  mockState = {
    status: "connected",
    room: { room: input.room, title: "角色换装联调", description: "多人协作对话流", host, port: input.port, tokenRequired: Boolean(input.token?.trim()), latestSequence: 4 },
    selfMemberId: "self",
    selfSessionId: input.sessionID,
    members: sampleMembers(input.memberName || "陈程序", input.agentName || "程序 Agent", input.sessionID),
    timeline: sampleTimeline(),
  };
  mockSequence = 4;
  emitMockState();
  return mockState;
}

export function createMockCollaborationTransport(): CollaborationTransport {
  return {
    async getState() { return mockState; },
    async retry() { emitMockState(); return mockState; },
    async host(input) { return connectMock(input); },
    async join(input) { return connectMock(input); },
    async leave() { mockState = { status: "disconnected", members: [], timeline: [] }; emitMockState(); },
    async post(input) {
      const self = mockState.members.find((member) => member.isSelf);
      const item: CollaborationTimelineItem = {
        id: input.requestID,
        sequence: ++mockSequence,
        revision: 1,
        kind: input.kind,
        contributionKind: input.contributionKind,
        actorId: self?.id || "self",
        actorName: self?.name || "我",
        targetMemberId: input.targetMemberID,
        text: input.text,
        createdAt: now(),
        referenceIds: input.referenceIDs || [],
        requestStatus: input.kind === "agent_request" ? "waiting" : undefined,
        syncStatus: "synced",
      };
      emitMockItem(item);
      return { ok: true, requestID: input.requestID, item };
    },
    async startAgent(input) {
      const self = mockState.members.find((member) => member.isSelf);
      const item: CollaborationTimelineItem = {
        id: input.requestID,
        sequence: ++mockSequence,
        revision: 1,
        kind: "agent_command",
        actorId: self?.id || "self",
        actorName: self?.name || "我",
        text: input.instruction,
        createdAt: now(),
        referenceIds: input.referenceIDs,
        syncStatus: "synced",
      };
      emitMockItem(item);
      return { ok: true, requestID: input.requestID, item };
    },
    async respond(input) {
      if (input.action === "accept") {
        return this.startAgent({ requestID: input.requestID, sessionID: input.sessionID, instruction: input.instruction || "处理协作请求", referenceIDs: [input.agentRequestID], agentRequestID: input.agentRequestID });
      }
      return { ok: true, requestID: input.requestID };
    },
    subscribeState(listener) { mockStateListeners.add(listener); return () => mockStateListeners.delete(listener); },
    subscribeEvent(listener) { mockEventListeners.add(listener); return () => mockEventListeners.delete(listener); },
  };
}

export function defaultCollaborationTransport(): CollaborationTransport {
  const live = typeof window !== "undefined" && Boolean(window.go?.main?.App?.GetCollaborationState);
  return live ? createWailsCollaborationTransport() : createMockCollaborationTransport();
}
