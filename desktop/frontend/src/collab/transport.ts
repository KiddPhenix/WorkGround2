import { app, onCollaborationEvent, onCollaborationState } from "../lib/bridge";
import { t } from "../lib/i18n";
import type {
  CollaborationActionResult,
  CollaborationAgentConfig,
  CollaborationAgentPrompt,
  CollaborationInvite,
  CollaborationFileTransfer,
  CollaborationIntentResult,
  CollaborationMember,
  CollaborationRelayConfig,
  CollaborationRoomQueryResult,
  CollaborationRouteInput,
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

function normalizeAgentPrompt(value: unknown): CollaborationAgentPrompt | undefined {
  const raw = record(value);
  const kind = text(raw.kind ?? raw.Kind);
  const runId = text(raw.runId ?? raw.RunID);
  const id = text(raw.id ?? raw.ID);
  if (!runId || !id || (kind !== "approval" && kind !== "ask")) return undefined;
  return {
    runId,
    kind,
    id,
    tool: text(raw.tool ?? raw.Tool) || undefined,
    subject: text(raw.subject ?? raw.Subject) || undefined,
    reason: text(raw.reason ?? raw.Reason) || undefined,
    questions: kind === "ask" ? list(raw.questions ?? raw.Questions).map((value) => {
      const question = record(value);
      return {
        id: text(question.id ?? question.ID),
        header: text(question.header ?? question.Header) || undefined,
        prompt: text(question.prompt ?? question.Prompt),
        multi: bool(question.multi ?? question.Multi),
        options: list(question.options ?? question.Options).map((value) => {
          const option = record(value);
          return { label: text(option.label ?? option.Label), description: text(option.description ?? option.Description) || undefined };
        }).filter((option) => option.label),
      };
    }).filter((question) => question.id && question.prompt) : undefined,
  };
}

export function normalizeCollaborationMember(value: unknown): CollaborationMember {
  const raw = record(value);
  const agentRaw = record(raw.agent ?? raw.Agent);
  const id = text(raw.id ?? raw.ID ?? raw.memberId ?? raw.MemberID);
  return {
    id,
    name: text(raw.name ?? raw.Name ?? raw.memberName ?? raw.MemberName, id || t("collab.defaultMember")),
    avatar: text(raw.avatar ?? raw.Avatar ?? raw.memberAvatar ?? raw.MemberAvatar) || undefined,
    role: text(raw.role ?? raw.Role) || undefined,
    online: text(raw.status ?? raw.Status, "online") === "online" && bool(raw.online ?? raw.Online, true),
    isSelf: bool(raw.isSelf ?? raw.IsSelf),
    agent: {
      id: text(agentRaw.id ?? agentRaw.ID ?? raw.agentId ?? raw.AgentID, `${id}-agent`),
      name: text(agentRaw.name ?? agentRaw.Name ?? raw.agentName ?? raw.AgentName, t("collab.defaultAgent")),
      avatar: text(agentRaw.avatar ?? agentRaw.Avatar ?? raw.agentAvatar ?? raw.AgentAvatar) || undefined,
      status: (text(agentRaw.status ?? agentRaw.Status ?? raw.agentStatus ?? raw.AgentStatus, "idle").replace("waiting_approval", "waiting") || "idle") as CollaborationMember["agent"]["status"],
      sessionId: text(agentRaw.sessionId ?? agentRaw.SessionID ?? raw.sessionId ?? raw.SessionID) || undefined,
    },
  };
}

export function normalizeCollaborationItem(value: unknown, memberNames: Map<string, string> = new Map(), agentNames: Map<string, string> = new Map()): CollaborationTimelineItem {
  const raw = record(value);
  const wrappedItem = raw.item ?? raw.Item;
  if (wrappedItem) {
    const normalized = normalizeCollaborationItem(wrappedItem, memberNames, agentNames);
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
      const normalized = normalizeCollaborationItem(inner, memberNames, agentNames);
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
  const typed = record(raw.chat ?? raw.Chat ?? raw.contribution ?? raw.Contribution ?? raw.agentRequest ?? raw.AgentRequest ?? raw.agentRun ?? raw.AgentRun ?? raw.agentResult ?? raw.AgentResult ?? raw.file ?? raw.File ?? raw.reaction ?? raw.Reaction ?? raw.system ?? raw.System);
  const rawKind = text(raw.kind ?? raw.Kind ?? raw.type ?? raw.Type, "chat");
  const kind = rawKind === "agent_run" ? "agent_command" : rawKind;
  const references = typed.referenceIds ?? typed.ReferenceIDs ?? raw.referenceIds ?? raw.ReferenceIDs ?? raw.references ?? raw.References;
  const id = text(raw.id ?? raw.ID ?? raw.eventId ?? raw.EventID);
  const actorId = text(typed.authorId ?? typed.AuthorID ?? typed.ownerId ?? typed.OwnerID ?? raw.actorId ?? raw.ActorID ?? actor.id ?? actor.ID ?? typed.memberId ?? typed.MemberID);
  const actorAgent = bool(raw.actorAgent ?? raw.ActorAgent, kind === "agent_command" || kind === "agent_result");
  const explicitActorName = text(raw.actorName ?? raw.ActorName ?? actor.name ?? actor.Name);
  const createdAt = text(typed.createdAt ?? typed.CreatedAt ?? typed.updatedAt ?? typed.UpdatedAt ?? raw.createdAt ?? raw.CreatedAt, new Date().toISOString());
  let content = text(typed.text ?? typed.Text ?? typed.body ?? typed.Body ?? typed.instruction ?? typed.Instruction ?? typed.summary ?? typed.Summary ?? typed.message ?? typed.Message ?? raw.text ?? raw.Text ?? raw.content ?? raw.Content);
  if (kind === "reaction" && !content) content = text(typed.kind ?? typed.Kind, "agree");
  if (kind === "system" && !content) content = text(typed.kind ?? typed.Kind, "system");
  if (kind === "file" && !content) content = text(typed.name ?? typed.Name);
  return {
    id,
    sequence: number(raw.sequence ?? raw.Sequence),
    revision: number(typed.revision ?? typed.Revision ?? raw.revision ?? raw.Revision, 1),
    kind: (kind || "chat") as CollaborationTimelineItem["kind"],
    contributionKind: text(typed.kind ?? typed.Kind ?? raw.contributionKind ?? raw.ContributionKind) || undefined,
    actorId,
    actorName: actorAgent
      ? agentNames.get(actorId) || explicitActorName || memberNames.get(actorId) || t("collab.defaultAgent")
      : explicitActorName || memberNames.get(actorId) || t("collab.defaultMember"),
    actorAgent,
    targetMemberId: text(typed.targetMemberId ?? typed.TargetMemberID ?? raw.targetMemberId ?? raw.TargetMemberID) || undefined,
    mentionMemberIds: list(typed.mentionMemberIds ?? typed.MentionMemberIDs ?? raw.mentionMemberIds ?? raw.MentionMemberIDs).map((item) => text(item)).filter(Boolean),
    mentionAgentIds: list(typed.mentionAgentIds ?? typed.MentionAgentIDs ?? raw.mentionAgentIds ?? raw.MentionAgentIDs).map((item) => text(item)).filter(Boolean),
    text: content,
    createdAt,
    referenceIds: list(references).map((item) => text(item)).filter(Boolean),
    syncStatus: (text(raw.syncStatus ?? raw.SyncStatus) || undefined) as CollaborationTimelineItem["syncStatus"],
    requestStatus: (text(typed.status ?? typed.Status ?? raw.requestStatus ?? raw.RequestStatus).replace("pending", "waiting") || undefined) as CollaborationTimelineItem["requestStatus"],
    agentRunStatus: (kind === "agent_command" ? text(typed.status ?? typed.Status) || undefined : undefined) as CollaborationTimelineItem["agentRunStatus"],
    agentRunSummary: kind === "agent_command" ? text(typed.summary ?? typed.Summary) || undefined : undefined,
    agentRunError: kind === "agent_command" ? text(typed.error ?? typed.Error) || undefined : undefined,
    agentCommandId: kind === "agent_command" ? text(typed.commandId ?? typed.CommandID) || undefined : undefined,
    systemKind: kind === "system" ? text(typed.kind ?? typed.Kind) || undefined : undefined,
    reactions: record(raw.reactions ?? raw.Reactions) as Record<string, string[]>,
    fileName: kind === "file" ? text(typed.name ?? typed.Name) : undefined,
    fileSize: kind === "file" ? number(typed.size ?? typed.Size) : undefined,
    fileMime: kind === "file" ? text(typed.mime ?? typed.MIME) || undefined : undefined,
    fileSHA256: kind === "file" ? text(typed.sha256 ?? typed.SHA256) || undefined : undefined,
    fileRevoked: kind === "file" ? Boolean(typed.revokedAt ?? typed.RevokedAt) : undefined,
  };
}

function normalizeFileTransfer(value: unknown): CollaborationFileTransfer {
  const raw = record(value);
  return {
    id: text(raw.id ?? raw.ID),
    fileId: text(raw.fileId ?? raw.FileID),
    direction: text(raw.direction ?? raw.Direction) === "share" ? "share" : "receive",
    name: text(raw.name ?? raw.Name),
    status: text(raw.status ?? raw.Status, "failed") as CollaborationFileTransfer["status"],
    transferred: number(raw.transferred ?? raw.Transferred),
    total: number(raw.total ?? raw.Total),
    destination: text(raw.destination ?? raw.Destination) || undefined,
    error: text(raw.error ?? raw.Error) || undefined,
    retryable: bool(raw.retryable ?? raw.Retryable),
  };
}

export function normalizeCollaborationState(value: unknown): CollaborationState {
  const raw = record(value);
  const snapshot = record(raw.snapshot ?? raw.Snapshot);
  const roomRaw = record(snapshot.room ?? snapshot.Room);
  const roomName = text(raw.room ?? raw.Room ?? roomRaw.id ?? roomRaw.ID ?? roomRaw.room ?? roomRaw.Room ?? roomRaw.name ?? roomRaw.Name ?? raw.roomName ?? raw.RoomName);
  const members = list(snapshot.members ?? snapshot.Members ?? raw.members ?? raw.Members).map(normalizeCollaborationMember);
  const memberNames = new Map(members.map((member) => [member.id, member.name]));
  const agentNames = new Map<string, string>();
  for (const member of members) {
    agentNames.set(member.id, member.agent.name);
    if (member.agent.id) agentNames.set(member.agent.id, member.agent.name);
  }
  const selfMemberId = text(raw.selfMemberId ?? raw.SelfMemberID ?? raw.memberId ?? raw.MemberID);
  for (const member of members) member.isSelf = member.id === selfMemberId;
  const self = members.find((member) => member.id === selfMemberId);
  const configRaw = record(raw.agentConfig ?? raw.AgentConfig);
  const mode = text(configRaw.recognitionMode ?? configRaw.RecognitionMode, "off");
  const peerInterval = number(configRaw.agentResponseIntervalSeconds ?? configRaw.AgentResponseIntervalSeconds, 30);
  const clockTurns = number(configRaw.agentClockTurns ?? configRaw.AgentClockTurns, 12);
  const clockWoundAt = text(configRaw.agentClockWoundAt ?? configRaw.AgentClockWoundAt);
  const agentConfig: CollaborationAgentConfig = {
    alias: text(configRaw.alias ?? configRaw.Alias, self?.agent.name || t("collab.defaultAgent")),
    autoRespondQuestions: bool(configRaw.autoRespondQuestions ?? configRaw.AutoRespondQuestions),
    autoRespondRequests: bool(configRaw.autoRespondRequests ?? configRaw.AutoRespondRequests),
    autoRespondAgents: bool(configRaw.autoRespondAgents ?? configRaw.AutoRespondAgents),
    agentResponseIntervalSeconds: Math.min(3600, Math.max(5, peerInterval)),
    agentClockTurns: Math.min(100, Math.max(1, clockTurns)),
    agentClockUnlimited: bool(configRaw.agentClockUnlimited ?? configRaw.AgentClockUnlimited),
    agentClockWoundAt: Number.isFinite(Date.parse(clockWoundAt)) ? clockWoundAt : undefined,
    recognitionMode: mode === "message" || mode === "interval" ? mode : "off",
    contextRefs: list(configRaw.contextRefs ?? configRaw.ContextRefs).map((item) => text(item)).filter(Boolean),
  };

  const sourcesRaw = record(raw.agentSources ?? raw.AgentSources);
  const normalizeSource = (value: unknown) => {
    const source = record(value);
    const kind = text(source.kind ?? source.Kind);
    return {
      id: text(source.id ?? source.ID),
      kind: (kind === "skill" ? "skill" : "agents") as "agents" | "skill",
      name: text(source.name ?? source.Name),
      path: text(source.path ?? source.Path),
      description: text(source.description ?? source.Description) || undefined,
      scope: text(source.scope ?? source.Scope) || undefined,
      runAs: text(source.runAs ?? source.RunAs) || undefined,
      protected: bool(source.protected ?? source.Protected) || undefined,
      available: source.available === undefined && source.Available === undefined ? true : bool(source.available ?? source.Available),
    };
  };
  const approvalMode = text(raw.toolApprovalMode ?? raw.ToolApprovalMode, "ask");
  const timeline = list(snapshot.timeline ?? snapshot.Timeline ?? raw.timeline ?? raw.Timeline ?? raw.items ?? raw.Items)
    .map((item) => normalizeCollaborationItem(item, memberNames, agentNames))
    .filter((item) => item.id);
  const queued = list(raw.outbox ?? raw.Outbox).flatMap((value) => {
    const entry = record(value);
    const itemValue = entry.item ?? entry.Item;
    if (!itemValue) return [];
    const item = normalizeCollaborationItem(itemValue, memberNames, agentNames);
    if (!item.id) return [];
    return [{
      ...item,
      localPending: true,
      requestID: text(entry.requestId ?? entry.RequestID) || undefined,
      syncStatus: (text(entry.status ?? entry.Status) === "failed" ? "failed" : "pending") as CollaborationTimelineItem["syncStatus"],
    }];
  });
  const currentRunRaw = record(raw.currentRun ?? raw.CurrentRun);
  const currentRunPhase = text(currentRunRaw.phase ?? currentRunRaw.Phase);
  const currentRun = currentRunPhase === "running" || currentRunPhase === "waiting_approval" || currentRunPhase === "stopping"
    ? {
        sessionId: text(currentRunRaw.sessionId ?? currentRunRaw.SessionID),
        runId: text(currentRunRaw.runId ?? currentRunRaw.RunID),
        phase: currentRunPhase,
        instruction: text(currentRunRaw.instruction ?? currentRunRaw.Instruction),
        progress: text(currentRunRaw.progress ?? currentRunRaw.Progress) || undefined,
        startedAt: number(currentRunRaw.startedAt ?? currentRunRaw.StartedAt) || undefined,
        queueCount: number(currentRunRaw.queueCount ?? currentRunRaw.QueueCount),
      } as const
    : undefined;
  return {
    status: (text(raw.status ?? raw.Status, roomName ? "connected" : "disconnected") || "disconnected") as CollaborationState["status"],
    mode: (text(raw.mode ?? raw.Mode) || undefined) as CollaborationState["mode"],
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
    agentConfig,
    agentSources: {
      agents: list(sourcesRaw.agents ?? sourcesRaw.Agents).map(normalizeSource).filter((source) => source.id && source.name),
      skills: list(sourcesRaw.skills ?? sourcesRaw.Skills).map(normalizeSource).filter((source) => source.id && source.name),
    },
    toolApprovalMode: approvalMode === "auto" || approvalMode === "yolo" ? approvalMode : "ask",
    agentPrompt: normalizeAgentPrompt(raw.agentPrompt ?? raw.AgentPrompt),
    currentRun: currentRun?.runId ? currentRun : undefined,
    timeline: [...timeline, ...queued],
    lastError: text(raw.lastError ?? raw.LastError ?? raw.error ?? raw.Error) || undefined,
    retryable: bool(raw.retryable ?? raw.Retryable, true),
    unsyncedCount: number(raw.outboxCount ?? raw.OutboxCount ?? raw.unsyncedCount ?? raw.UnsyncedCount),
    transfers: list(raw.transfers ?? raw.Transfers).map(normalizeFileTransfer),
    queuedTasks: list(raw.queuedTasks ?? raw.QueuedTasks).map((value) => {
      const task = record(value);
      return {
        id: text(task.id ?? task.ID),
        requestId: text(task.requestId ?? task.RequestID),
        instruction: text(task.instruction ?? task.Instruction),
        referenceIds: list(task.referenceIds ?? task.ReferenceIDs).map((item) => text(item)).filter(Boolean),
        agentRequestId: text(task.agentRequestId ?? task.AgentRequestID) || undefined,
        queuedAt: text(task.queuedAt ?? task.QueuedAt),
      };
    }).filter((task) => task.id && task.instruction),
    routes: list(raw.routes ?? raw.Routes).map((value) => {
      const route = record(value);
      const status = text(route.status ?? route.Status);
      return {
        ...normalizeRouteInput(route),
        id: text(route.id ?? route.ID),
        status: (status === "disabled" || status === "connecting" || status === "connected" || status === "degraded" || status === "failed" ? status : "disabled") as NonNullable<CollaborationState["routes"]>[number]["status"],
        active: bool(route.active ?? route.Active),
        priority: number(route.priority ?? route.Priority),
        latencyMs: number(route.latencyMs ?? route.LatencyMS) || undefined,
        lastError: text(route.lastError ?? route.LastError) || undefined,
        retryable: bool(route.retryable ?? route.Retryable) || undefined,
      };
    }).filter((route) => route.id),
    advertisement: (() => {
      const advertisement = record(raw.advertisement ?? raw.Advertisement);
      if (Object.keys(advertisement).length === 0) return undefined;
      const visibility = text(advertisement.visibility ?? advertisement.Visibility);
      return {
        visibility: visibility === "public" || visibility === "unlisted" ? visibility : "private",
        revision: number(advertisement.revision ?? advertisement.Revision),
        relays: list(advertisement.relays ?? advertisement.Relays).map((value) => {
          const relay = record(value);
          const status = text(relay.status ?? relay.Status);
          return {
            relayId: text(relay.relayId ?? relay.RelayID),
            status: (status === "pending" || status === "published" || status === "failed" || status === "revoking" || status === "revoked" ? status : "disabled") as NonNullable<CollaborationState["advertisement"]>["relays"][number]["status"],
            lastError: text(relay.lastError ?? relay.LastError) || undefined,
            retryable: bool(relay.retryable ?? relay.Retryable) || undefined,
          };
        }).filter((relay) => relay.relayId),
      };
    })(),
  };
}

function normalizeCollaborationInvite(value: unknown): CollaborationInvite {
  const raw = record(value);
  return {
    version: number(raw.version ?? raw.Version) === 2 ? 2 : 1,
    invite: text(raw.invite ?? raw.Invite) || undefined,
    hosts: list(raw.hosts ?? raw.Hosts).map((item) => text(item)).filter(Boolean),
    port: number(raw.port ?? raw.Port),
    room: text(raw.room ?? raw.Room),
    token: text(raw.token ?? raw.Token) || undefined,
    hostKey: text(raw.hostKey ?? raw.HostKey) || undefined,
    routes: list(raw.routes ?? raw.Routes).map(normalizeRouteInput).filter((route) => route.kind === "lan" || route.relayId),
  };
}

function normalizeRouteInput(value: unknown): CollaborationRouteInput {
  const raw = record(value);
  return {
    id: text(raw.id ?? raw.ID) || undefined,
    kind: text(raw.kind ?? raw.Kind) === "relay" ? "relay" : "lan",
    host: text(raw.host ?? raw.Host) || undefined,
    port: number(raw.port ?? raw.Port) || undefined,
    relayId: text(raw.relayId ?? raw.RelayID) || undefined,
    url: text(raw.url ?? raw.URL) || undefined,
    tunnelId: text(raw.tunnelId ?? raw.TunnelID) || undefined,
    guestCapability: text(raw.guestCapability ?? raw.GuestCapability) || undefined,
    priority: number(raw.priority ?? raw.Priority) || undefined,
  };
}

function normalizeRelayConfig(value: unknown): CollaborationRelayConfig {
  const raw = record(value);
  return {
    preferLAN: bool(raw.preferLAN ?? raw.PreferLAN, true),
    connectTimeoutSeconds: number(raw.connectTimeoutSeconds ?? raw.ConnectTimeoutSeconds, 10),
    routeStableSeconds: number(raw.routeStableSeconds ?? raw.RouteStableSeconds, 60),
    relays: list(raw.relays ?? raw.Relays).map((value) => {
      const relay = record(value);
      return {
        id: text(relay.id ?? relay.ID),
        name: text(relay.name ?? relay.Name) || undefined,
        url: text(relay.url ?? relay.URL),
        enabled: bool(relay.enabled ?? relay.Enabled, true),
        priority: number(relay.priority ?? relay.Priority),
        discovery: bool(relay.discovery ?? relay.Discovery),
        allowInsecure: bool(relay.allowInsecure ?? relay.AllowInsecure) || undefined,
        accessTokenEnv: text(relay.accessTokenEnv ?? relay.AccessTokenEnv) || undefined,
      };
    }).filter((relay) => relay.id && relay.url),
  };
}

function normalizeRoomQuery(value: unknown): CollaborationRoomQueryResult {
  const raw = record(value);
  return {
    rooms: list(raw.rooms ?? raw.Rooms).map((value) => {
      const room = record(value);
      return {
        publicRoomId: text(room.publicRoomId ?? room.PublicRoomID),
        room: text(room.room ?? room.Room),
        name: text(room.name ?? room.Name),
        description: text(room.description ?? room.Description) || undefined,
        tags: list(room.tags ?? room.Tags).map((tag) => text(tag)).filter(Boolean),
        requiresToken: bool(room.requiresToken ?? room.RequiresToken),
        onlineCount: number(room.onlineCount ?? room.OnlineCount) || undefined,
        capacity: number(room.capacity ?? room.Capacity) || undefined,
        hostKey: text(room.hostKey ?? room.HostKey ?? room.hostKeyFingerprint ?? room.HostKeyFingerprint),
        routes: list(room.routes ?? room.Routes).map(normalizeRouteInput),
        joinRef: text(room.joinRef ?? room.JoinRef) || undefined,
        expiresAt: text(room.expiresAt ?? room.ExpiresAt) || undefined,
      };
    }).filter((room) => room.publicRoomId && room.room && room.hostKey && room.routes.length),
    nextCursor: text(raw.nextCursor ?? raw.NextCursor) || undefined,
  };
}

export function normalizeCollaborationAction(value: unknown, memberNames: Map<string, string> = new Map(), agentNames: Map<string, string> = new Map()): CollaborationActionResult {
  const raw = record(value);
  const receipt = record(raw.receipt ?? raw.Receipt);
  const queued = bool(raw.queued ?? raw.Queued);
  const item = raw.item || raw.Item ? normalizeCollaborationItem(raw.item ?? raw.Item, memberNames, agentNames) : undefined;
  return {
    ok: queued || bool(raw.ok ?? raw.OK, !text(raw.error ?? raw.Error)),
    requestID: text(raw.requestID ?? raw.RequestID) || undefined,
    code: text(raw.code ?? raw.Code) || undefined,
    item: item ? {
      ...item,
      localPending: queued || undefined,
      requestID: queued ? text(raw.requestId ?? raw.RequestID) || undefined : undefined,
      syncStatus: queued ? "pending" : item.syncStatus,
    } : undefined,
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

export function normalizeCollaborationIntent(value: unknown): CollaborationIntentResult {
  const raw = record(value);
  const intent = text(raw.intent ?? raw.Intent);
  const source = text(raw.source ?? raw.Source);
  return {
    intent: intent === "self_agent" || intent === "uncertain" || intent === "chat" ? intent : "chat",
    source: source === "rule" || source === "llm" || source === "fallback" ? source : "fallback",
    error: text(raw.error ?? raw.Error) || (intent ? undefined : "Semantic intent classifier returned an invalid result"),
    retryable: bool(raw.retryable ?? raw.Retryable),
  };
}

export function createWailsCollaborationTransport(sessionID: string): CollaborationTransport {
  const relayApp = app as typeof app & {
    GetCollaborationRelayConfig?: () => Promise<unknown>;
    Settings?: () => Promise<unknown>;
    SetCollaboration?: (input: unknown) => Promise<void>;
    ListCollaborationRooms?: (input: unknown) => Promise<unknown>;
  };
  let names = new Map<string, string>();
  let agents = new Map<string, string>();
  const normalizeState = (value: unknown) => {
    const state = normalizeCollaborationState(value);
    names = new Map(state.members.map((member) => [member.id, member.name]));
    agents = new Map();
    for (const member of state.members) {
      agents.set(member.id, member.agent.name);
      if (member.agent.id) agents.set(member.agent.id, member.agent.name);
    }
    return state;
  };
  return {
    getState: async () => normalizeState(await app.GetCollaborationState(sessionID)),
    retry: async () => normalizeState(await app.RetryCollaboration(sessionID)),
    host: async (input) => normalizeState(await app.HostCollaborationRoom(input)),
    join: async (input) => normalizeState(await app.JoinCollaborationRoom(input)),
    invite: async () => normalizeCollaborationInvite(await app.GetCollaborationInvite(sessionID)),
    leave: () => app.LeaveCollaborationRoom(sessionID),
    getRelayConfig: async () => relayApp.GetCollaborationRelayConfig
      ? normalizeRelayConfig(await relayApp.GetCollaborationRelayConfig())
      : relayApp.Settings ? normalizeRelayConfig(record(await relayApp.Settings()).collaboration) : { preferLAN: true, connectTimeoutSeconds: 10, routeStableSeconds: 60, relays: [] },
    setRelayConfig: async (input) => {
      if (!relayApp.SetCollaboration) throw new Error("Relay settings are unavailable");
      await relayApp.SetCollaboration({
        ...input,
        connectTimeoutSeconds: input.connectTimeoutSeconds ?? 10,
        routeStableSeconds: input.routeStableSeconds ?? 60,
      });
      return relayApp.Settings
        ? normalizeRelayConfig(record(await relayApp.Settings()).collaboration)
        : normalizeRelayConfig(input);
    },
    listRooms: async (input) => relayApp.ListCollaborationRooms ? normalizeRoomQuery(await relayApp.ListCollaborationRooms(input)) : { rooms: [] },
    classifyIntent: async (value) => normalizeCollaborationIntent(await app.ClassifyCollaborationIntent({ sessionID, text: value })),
    post: async (input) => normalizeCollaborationAction(await app.PostCollaborationMessage({ ...input, sessionID }), names, agents),
    startAgent: async (input) => normalizeCollaborationAction(await app.StartCollaborationAgent(input), names, agents),
    stopCurrentRun: async (runID) => { await app.StopCollaborationAgentRun(sessionID, runID); },
    cancelQueuedTask: async (taskID) => normalizeCollaborationAction(await app.CancelCollaborationQueuedTask({ sessionID, taskID }), names, agents),
    respondAgentRun: async (runID, response) => normalizeCollaborationAction(await app.RespondCollaborationAgentRun({ sessionID, runID, ...response, allow: response.allow ?? false }), names, agents),
    respond: async (input) => normalizeCollaborationAction(await app.RespondCollaborationRequest(input), names, agents),
    updateAgentConfig: async (input) => normalizeState(await app.UpdateCollaborationAgentConfig({ ...input, sessionID })),
    updateProfile: async (input) => normalizeState(await app.UpdateCollaborationProfile({ ...input, sessionID })),
    updateToolApprovalMode: async (mode) => normalizeState(await app.UpdateCollaborationToolApprovalMode({ sessionID, mode })),
    shareFiles: (paths) => app.ShareCollaborationFiles({ sessionID, paths }).then((values) => list(values).map(normalizeFileTransfer)),
    receiveFile: (fileID) => app.ReceiveCollaborationFile({ sessionID, fileID }).then(normalizeFileTransfer),
    pauseFile: (fileID) => app.PauseCollaborationFile({ sessionID, fileID }).then(normalizeFileTransfer),
    resumeFile: (fileID) => app.ResumeCollaborationFile({ sessionID, fileID }).then(normalizeFileTransfer),
    revokeFile: async (fileID) => normalizeCollaborationAction(await app.RevokeCollaborationFile({ sessionID, fileID }), names, agents),
    openFile: (fileID) => app.OpenCollaborationFile({ sessionID, fileID }),
    revealFile: (fileID) => app.RevealCollaborationFile({ sessionID, fileID }),
    subscribeState: (listener) => onCollaborationState((payload) => {
      const raw = record(payload);
      if (text(raw.sessionId ?? raw.SessionID) !== sessionID) return;
      listener(normalizeState(payload));
    }),
    subscribeEvent: (listener) => onCollaborationEvent((payload) => {
      const raw = record(payload);
      if (text(raw.sessionId ?? raw.SessionID) !== sessionID) return;
      listener(normalizeCollaborationItem(payload, names, agents));
    }),
  };
}

type MockCollaborationRuntime = {
  stateListeners: Set<(state: CollaborationState) => void>;
  eventListeners: Set<(item: CollaborationTimelineItem) => void>;
  sequence: number;
  state: CollaborationState;
};

const mockRuntimes = new Map<string, MockCollaborationRuntime>();

function mockRuntime(sessionID: string): MockCollaborationRuntime {
  let runtime = mockRuntimes.get(sessionID);
  if (!runtime) {
    runtime = { stateListeners: new Set(), eventListeners: new Set(), sequence: 4, state: { status: "disconnected", selfSessionId: sessionID, members: [], timeline: [], toolApprovalMode: "ask", agentConfig: { alias: "", autoRespondQuestions: false, autoRespondRequests: false, autoRespondAgents: false, agentResponseIntervalSeconds: 30, agentClockTurns: 12, agentClockUnlimited: false, recognitionMode: "off" } } };
    mockRuntimes.set(sessionID, runtime);
  }
  return runtime;
}

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

function emitMockState(runtime: MockCollaborationRuntime) {
  const snapshot = { ...runtime.state, members: [...runtime.state.members], timeline: [...runtime.state.timeline] };
  for (const listener of runtime.stateListeners) listener(snapshot);
}

function emitMockItem(runtime: MockCollaborationRuntime, item: CollaborationTimelineItem) {
  runtime.state = { ...runtime.state, timeline: [...runtime.state.timeline, item], room: runtime.state.room ? { ...runtime.state.room, latestSequence: item.sequence } : undefined };
  for (const listener of runtime.eventListeners) listener(item);
  emitMockState(runtime);
}

function connectMock(runtime: MockCollaborationRuntime, input: HostCollaborationRoomInput | JoinCollaborationRoomInput): CollaborationState {
  const host = "host" in input ? input.host : input.listenHost;
  runtime.state = {
    status: "connected",
    mode: "host" in input ? "client" : "host",
    room: { room: input.room, title: "角色换装联调", description: "多人协作对话流", host, port: input.port, tokenRequired: Boolean(input.token?.trim()), latestSequence: 4 },
    selfMemberId: "self",
    selfSessionId: input.sessionID,
    members: sampleMembers(input.memberName || "陈程序", input.agentName || "程序 Agent", input.sessionID),
    timeline: sampleTimeline(),
    toolApprovalMode: "ask",
    agentConfig: { alias: input.agentName || "程序 Agent", autoRespondQuestions: false, autoRespondRequests: false, autoRespondAgents: false, agentResponseIntervalSeconds: 30, agentClockTurns: 12, agentClockUnlimited: false, recognitionMode: "off" },
  };
  runtime.sequence = 4;
  emitMockState(runtime);
  return runtime.state;
}

export function createMockCollaborationTransport(sessionID = "preview-session"): CollaborationTransport {
  const runtime = mockRuntime(sessionID);
  return {
    async getState() { return runtime.state; },
    async retry() { emitMockState(runtime); return runtime.state; },
    async host(input) { return connectMock(runtime, input); },
    async join(input) { return connectMock(runtime, input); },
    async invite() {
      if (!runtime.state.room) throw new Error("collaboration Room is unavailable");
      return { hosts: [runtime.state.room.host || "127.0.0.1"], port: runtime.state.room.port, room: runtime.state.room.room };
    },
    async leave() { runtime.state = { status: "disconnected", selfSessionId: sessionID, members: [], timeline: [], agentConfig: runtime.state.agentConfig }; emitMockState(runtime); },
    async getRelayConfig() { return { preferLAN: true, connectTimeoutSeconds: 10, routeStableSeconds: 60, relays: [] }; },
    async setRelayConfig(input) { return input; },
    async listRooms() { return { rooms: [] }; },
    async classifyIntent() { return { intent: "chat", source: "llm" }; },
    async post(input) {
      const self = runtime.state.members.find((member) => member.isSelf);
      const item: CollaborationTimelineItem = {
        id: input.requestID,
        sequence: ++runtime.sequence,
        revision: 1,
        kind: input.kind,
        contributionKind: input.contributionKind,
        actorId: self?.id || "self",
        actorName: self?.name || "我",
        targetMemberId: input.targetMemberID,
        mentionMemberIds: input.mentionMemberIDs || [],
        mentionAgentIds: input.mentionAgentIDs || [],
        text: input.text,
        createdAt: now(),
        referenceIds: input.referenceIDs || [],
        requestStatus: input.kind === "agent_request" ? "waiting" : undefined,
        syncStatus: "synced",
      };
      emitMockItem(runtime, item);
      return { ok: true, requestID: input.requestID, item };
    },
    async startAgent(input) {
      const self = runtime.state.members.find((member) => member.isSelf);
      const item: CollaborationTimelineItem = {
        id: input.requestID,
        sequence: ++runtime.sequence,
        revision: 1,
        kind: "agent_command",
        actorId: self?.id || "self",
        actorName: self?.name || "我",
        text: input.instruction,
        createdAt: now(),
        referenceIds: input.referenceIDs,
        agentCommandId: input.requestID,
        agentRunStatus: "running",
        syncStatus: "synced",
      };
      emitMockItem(runtime, item);
      return { ok: true, requestID: input.requestID, item };
    },
    async cancelQueuedTask(taskId) {
      runtime.state = { ...runtime.state, queuedTasks: (runtime.state.queuedTasks || []).filter((task) => task.id !== taskId) };
      emitMockState(runtime);
      return { ok: true, requestID: taskId };
    },
    async stopCurrentRun(runId) {
      // Mock: mark CurrentRun as stopping, then idle after a brief delay
      if (runtime.state.currentRun?.runId === runId) {
        runtime.state = { ...runtime.state, currentRun: { ...runtime.state.currentRun, phase: "stopping" } };
        emitMockState(runtime);
        setTimeout(() => {
          runtime.state = { ...runtime.state, currentRun: undefined };
          emitMockState(runtime);
        }, 100);
      }
    },
    async respondAgentRun(runId, response) {
      const current = runtime.state.timeline.find((item) => item.id === runId);
      if (current) emitMockItem(runtime, { ...current, agentRunStatus: "running", agentRunSummary: response.answering ? "answer submitted" : response.allow ? "confirmation accepted" : "confirmation rejected" });
      runtime.state = { ...runtime.state, agentPrompt: undefined };
      emitMockState(runtime);
      return { ok: true, requestID: `${runId}:respond` };
    },
    async respond(input) {
      if (input.action === "accept") {
        return this.startAgent({ requestID: input.requestID, sessionID: input.sessionID, instruction: input.instruction || "处理协作请求", referenceIDs: [input.agentRequestID], agentRequestID: input.agentRequestID, automatic: input.automatic });
      }
      return { ok: true, requestID: input.requestID };
    },
    async updateAgentConfig(input) {
      runtime.state = {
        ...runtime.state,
        agentConfig: input.config,
        members: runtime.state.members.map((member) => member.isSelf ? { ...member, agent: { ...member.agent, name: input.config.alias } } : member),
      };
      emitMockState(runtime);
      return runtime.state;
    },
    async updateProfile(input) {
      runtime.state = {
        ...runtime.state,
        agentConfig: { ...runtime.state.agentConfig!, alias: input.agentName },
        members: runtime.state.members.map((member) => member.isSelf ? {
          ...member, name: input.memberName, avatar: input.memberAvatar,
          agent: { ...member.agent, name: input.agentName, avatar: input.agentAvatar },
        } : member),
      };
      emitMockState(runtime);
      return runtime.state;
    },
    async updateToolApprovalMode(mode) {
      runtime.state = { ...runtime.state, toolApprovalMode: mode };
      emitMockState(runtime);
      return runtime.state;
    },
    async shareFiles() { return []; },
    async receiveFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    async openFile() {},
    async revealFile() {},
    subscribeState(listener) { runtime.stateListeners.add(listener); return () => runtime.stateListeners.delete(listener); },
    subscribeEvent(listener) { runtime.eventListeners.add(listener); return () => runtime.eventListeners.delete(listener); },
  };
}

export function defaultCollaborationTransport(sessionID: string): CollaborationTransport {
  const live = typeof window !== "undefined" && Boolean(window.go?.main?.App?.GetCollaborationState);
  return live ? createWailsCollaborationTransport(sessionID) : createMockCollaborationTransport(sessionID);
}
