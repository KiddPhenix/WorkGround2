import type {
  CollaborationAgentConfig,
  CollaborationIntentClass,
  CollaborationState,
  CollaborationTimelineItem,
  PendingIntent,
} from "./types";

export interface CollabViewState extends CollaborationState {
  agentConfig: CollaborationAgentConfig;
  selectedIds: string[];
  pendingIntents: Record<string, PendingIntent>;
  operation?: "host" | "join" | "sync" | "leave";
  actionError?: string;
}

export type CollabAction =
  | { type: "CONNECTING"; operation: "host" | "join" }
  | { type: "SYNCING"; reconnecting?: boolean }
  | { type: "STATE"; state: CollaborationState }
  | { type: "EVENT"; item: CollaborationTimelineItem }
  | { type: "FAILED"; error: string; retryable?: boolean }
  | { type: "ACTION_START" }
  | { type: "ACTION_FAILED"; error: string }
  | { type: "DISCONNECTED" }
  | { type: "TOGGLE_SELECT"; id: string }
  | { type: "CLEAR_SELECTION" }
  | { type: "PENDING_INTENT"; intent: PendingIntent }
  | { type: "PENDING_STATUS"; id: string; status: PendingIntent["status"]; error?: string }
  | { type: "UPDATE_PENDING"; id: string; instruction: string; deadline: number };

export const initialCollabState: CollabViewState = {
  status: "disconnected",
  members: [],
  timeline: [],
  toolApprovalMode: "ask",
  selectedIds: [],
  pendingIntents: {},
  agentConfig: { alias: "", autoRespondQuestions: false, autoRespondRequests: false, autoRespondAgents: false, agentResponseIntervalSeconds: 30, agentClockTurns: 12, agentClockUnlimited: false, recognitionMode: "off", contextRefs: [] },
};

function mergeTimeline(current: CollaborationTimelineItem[], incoming: CollaborationTimelineItem[]): CollaborationTimelineItem[] {
  const byId = new Map(current.map((item) => [item.id, item]));
  for (const item of incoming) byId.set(item.id, { ...byId.get(item.id), ...item });
  return [...byId.values()].sort((a, b) => a.sequence - b.sequence || a.createdAt.localeCompare(b.createdAt));
}

function cancelPending(pending: Record<string, PendingIntent>): Record<string, PendingIntent> {
  return Object.fromEntries(Object.entries(pending).map(([id, intent]) => [id, intent.status === "pending" ? { ...intent, status: "dismissed" as const } : intent]));
}

function sameRoom(a: CollaborationState["room"], b: CollaborationState["room"]): boolean {
  if (!a || !b) return !a && !b;
  return a.room === b.room && a.host.trim().toLowerCase() === b.host.trim().toLowerCase() && a.port === b.port;
}

export function collabReducer(state: CollabViewState, action: CollabAction): CollabViewState {
  switch (action.type) {
    case "CONNECTING":
      return {
        ...state,
        status: "connecting",
        operation: action.operation,
        room: undefined,
        selfMemberId: undefined,
        members: [],
        timeline: [],
        selectedIds: [],
        pendingIntents: cancelPending(state.pendingIntents),
        lastError: undefined,
      };
    case "SYNCING":
      return { ...state, status: action.reconnecting ? "reconnecting" : "syncing", operation: "sync", lastError: undefined, pendingIntents: cancelPending(state.pendingIntents) };
    case "STATE": {
      const keepRoomState = action.state.status !== "connecting" && sameRoom(action.state.room, state.room);
      return {
        ...state,
        ...action.state,
        status: action.state.status || "connected",
        timeline: keepRoomState
          ? mergeTimeline(state.timeline.filter((item) => !item.localPending), action.state.timeline || [])
          : (action.state.timeline || []),
        members: action.state.members || [],
        agentConfig: action.state.agentConfig || state.agentConfig,
        selectedIds: keepRoomState ? state.selectedIds : [],
        operation: action.state.status === "connecting" ? state.operation : undefined,
        pendingIntents: keepRoomState && action.state.status === "connected" ? state.pendingIntents : cancelPending(state.pendingIntents),
      };
    }
    case "EVENT":
      return { ...state, timeline: mergeTimeline(state.timeline, [action.item]) };
    case "FAILED":
      return { ...state, status: "failed", lastError: action.error, retryable: action.retryable ?? true, operation: undefined, pendingIntents: cancelPending(state.pendingIntents) };
    case "ACTION_START":
      return { ...state, actionError: undefined };
    case "ACTION_FAILED":
      return { ...state, actionError: action.error };
    case "DISCONNECTED":
      return { ...initialCollabState };
    case "TOGGLE_SELECT":
      return {
        ...state,
        selectedIds: state.selectedIds.includes(action.id)
          ? state.selectedIds.filter((id) => id !== action.id)
          : [...state.selectedIds, action.id],
      };
    case "CLEAR_SELECTION":
      return { ...state, selectedIds: [] };
    case "PENDING_INTENT":
      if (state.timeline.some((item) => item.id === action.intent.messageId && item.revision !== action.intent.revision)) return state;
      if (state.pendingIntents[action.intent.messageId]?.revision === action.intent.revision) return state;
      return { ...state, pendingIntents: { ...state.pendingIntents, [action.intent.messageId]: action.intent } };
    case "PENDING_STATUS": {
      const pending = state.pendingIntents[action.id];
      if (!pending) return state;
      return {
        ...state,
        pendingIntents: {
          ...state.pendingIntents,
          [action.id]: { ...pending, status: action.status, error: action.error },
        },
      };
    }
    case "UPDATE_PENDING": {
      const pending = state.pendingIntents[action.id];
      if (!pending) return state;
      return {
        ...state,
        pendingIntents: {
          ...state.pendingIntents,
          [action.id]: { ...pending, instruction: action.instruction, deadline: action.deadline, status: "pending", error: undefined },
        },
      };
    }
  }
}

const directedAtOther = /(?:^|\s)@[\p{L}\p{N}_-]+|(?:小王|小李|大家|你们|你来|麻烦你)/u;
const completionStatement = /(?:已经|已|刚刚).{0,8}(?:完成|修好|解决|提交|更新)|(?:done|fixed|shipped|completed)\b/i;
const highIntent = /(?:帮我|请|麻烦|把|将).{0,20}(?:检查|修复|实现|修改|更新|运行|验证|分析|重命名|生成|提交|构建|测试|部署|发布|同步)|(?:检查|修复|实现|修改|更新|运行|验证|分析|重命名|生成|提交|构建|测试|部署|发布|同步).{0,8}(?:一下|这个|这些|刚才|现有)|\b(?:check|fix|implement|update|run|verify|analy[sz]e|rename|generate|commit|build|test|deploy|publish|sync)\b/i;
const uncertainIntent = /(?:是不是|是否|怎么|为什么|好像|可能).{0,20}(?:问题|错误|失败|不对|异常)|\b(?:issue|error|wrong|why|how)\b/i;

export function detectSelfAgentIntent(text: string): CollaborationIntentClass {
  return detectSelfAgentIntentRule(text).intent;
}

export interface CollaborationIntentRuleResult {
  intent: CollaborationIntentClass;
  covered: boolean;
}

export function detectSelfAgentIntentRule(text: string): CollaborationIntentRuleResult {
  const value = text.trim();
  if (!value || directedAtOther.test(value) || completionStatement.test(value)) return { intent: "chat", covered: true };
  if (highIntent.test(value)) return { intent: "self_agent", covered: true };
  if (uncertainIntent.test(value) || /[?？]$/.test(value)) return { intent: "uncertain", covered: true };
  return { intent: "chat", covered: false };
}

export function replayableSelfAgentItems(state: Pick<CollabViewState, "timeline" | "selfMemberId" | "pendingIntents">): CollaborationTimelineItem[] {
  if (!state.selfMemberId) return [];
  const handled = new Set(
    state.timeline
      .filter((item) => item.kind === "agent_command")
      .flatMap((item) => item.referenceIds),
  );
  return state.timeline.filter((item) => {
    const rule = detectSelfAgentIntentRule(item.text);
    return item.localPending &&
      item.kind === "chat" &&
      item.actorId === state.selfMemberId &&
      !state.pendingIntents[item.id] &&
      !handled.has(item.id) &&
      (!rule.covered || rule.intent !== "chat");
  });
}

export function nextAutomaticAgentItem(
  state: Pick<CollabViewState, "timeline" | "selfMemberId" | "agentConfig">,
  selfAgentId?: string,
): { kind: "question" | "request"; item: CollaborationTimelineItem } | undefined {
  const selfMemberId = state.selfMemberId;
  if (!selfMemberId || state.agentConfig.recognitionMode === "off") return undefined;
  const handled = new Set(
    state.timeline
      .filter((item) => item.kind === "agent_command")
      .flatMap((item) => item.referenceIds),
  );
  if (state.agentConfig.autoRespondRequests) {
    const request = state.timeline.find((item) => item.kind === "agent_request"
      && item.targetMemberId === selfMemberId
      && item.requestStatus === "waiting"
      && !handled.has(item.id));
    if (request) return { kind: "request", item: request };
  }
  if (state.agentConfig.autoRespondQuestions) {
    const question = state.timeline.find((item) => item.actorId !== selfMemberId
      && !item.actorAgent
      && !handled.has(item.id)
      && !item.mentionMemberIds?.includes(selfMemberId)
      && !(selfAgentId && item.mentionAgentIds?.includes(selfAgentId))
      && (item.kind === "contribution" && item.contributionKind === "question"
        || item.kind === "chat" && /[?？]\s*$/.test(item.text)));
    if (question) return { kind: "question", item: question };
  }
  return undefined;
}

const agentCollaborationBatchLimit = 8;
const agentCollaborationRequestPrefix = "agent-collab-";

function humanIntervention(item: CollaborationTimelineItem): boolean {
  if (item.kind === "system" || item.kind === "agent_result") return false;
  if (item.kind === "agent_command") return !item.agentCommandId?.startsWith(agentCollaborationRequestPrefix);
  return item.actorAgent !== true;
}

function timelineAfter(item: CollaborationTimelineItem, boundary?: CollaborationTimelineItem): boolean {
  if (!boundary) return true;
  if (item.sequence !== boundary.sequence) return item.sequence > boundary.sequence;
  if (item.createdAt !== boundary.createdAt) return item.createdAt > boundary.createdAt;
  return item.id > boundary.id;
}

function afterClockReset(item: CollaborationTimelineItem, resetItem: CollaborationTimelineItem | undefined, woundAt: number): boolean {
  if (!timelineAfter(item, resetItem)) return false;
  if (!Number.isFinite(woundAt)) return true;
  const itemTime = Date.parse(item.createdAt);
  return Number.isFinite(itemTime) && itemTime > woundAt;
}

export function agentCollaborationClock(
  state: Pick<CollabViewState, "timeline" | "agentConfig">,
): { limit: number; used: number; remaining: number; unlimited: boolean; resetItem?: CollaborationTimelineItem; woundAt?: string } {
  const limit = Math.min(100, Math.max(1, state.agentConfig.agentClockTurns || 12));
  const woundAt = Date.parse(state.agentConfig.agentClockWoundAt || "");
  const interventions = state.timeline.filter(humanIntervention).sort((a, b) => a.sequence - b.sequence || a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id));
  const resetItem = interventions[interventions.length - 1];
  const used = state.timeline.filter((item) => item.kind === "agent_command"
    && item.agentCommandId?.startsWith(agentCollaborationRequestPrefix)
    && afterClockReset(item, resetItem, woundAt)).length;
  return { limit, used, remaining: Math.max(0, limit - used), unlimited: state.agentConfig.agentClockUnlimited, resetItem, woundAt: Number.isFinite(woundAt) ? state.agentConfig.agentClockWoundAt : undefined };
}

function stableHash(value: string, seed: number): string {
  let current = seed >>> 0;
  for (let index = 0; index < value.length; index++) {
    current ^= value.charCodeAt(index);
    current = Math.imul(current, 16777619) >>> 0;
  }
  return current.toString(16).padStart(8, "0");
}

export function agentCollaborationRequestID(items: CollaborationTimelineItem[], agentId: string): string {
  const value = `${agentId}\0${items.map((item) => item.id).sort().join("\0")}`;
  return `${agentCollaborationRequestPrefix}${stableHash(value, 2166136261)}${stableHash(value, 2246822519)}`;
}

export function nextAgentCollaborationBatch(
  state: Pick<CollabViewState, "timeline" | "selfMemberId" | "agentConfig">,
  selfAgentId?: string,
  now = Date.now(),
): { items: CollaborationTimelineItem[]; handoffs: NonNullable<CollaborationTimelineItem["handoffs"]>; waitMs: number } | undefined {
  const selfMemberId = state.selfMemberId;
  if (!selfMemberId || !selfAgentId || !state.agentConfig.autoRespondAgents) return undefined;
  const clock = agentCollaborationClock(state);
  if (!clock.unlimited && clock.remaining === 0) return undefined;
  const byId = new Map(state.timeline.map((item) => [item.id, item]));
  const ownCommands = state.timeline.filter((item) => item.kind === "agent_command" && item.actorId === selfMemberId);
  const handled = new Set(ownCommands.flatMap((item) => item.referenceIds));
  const candidates = state.timeline.filter((item) => item.kind === "agent_result"
    && item.actorId !== selfMemberId
    && item.handoffs?.some((handoff) => handoff.targetAgentId === selfAgentId && handoff.requiresResponse)
    && !handled.has(item.id)
    && afterClockReset(item, clock.resetItem, Date.parse(clock.woundAt || "")))
    .sort((a, b) => a.sequence - b.sequence || a.createdAt.localeCompare(b.createdAt));
  if (candidates.length === 0) return undefined;
  const peerCommandTimes = ownCommands
    .filter((item) => item.referenceIds.some((id) => byId.get(id)?.kind === "agent_result"))
    .map((item) => Date.parse(item.createdAt))
    .filter(Number.isFinite);
  const lastStartedAt = peerCommandTimes.length > 0 ? Math.max(...peerCommandTimes) : 0;
  const intervalMs = Math.min(3600, Math.max(5, state.agentConfig.agentResponseIntervalSeconds || 30)) * 1000;
  const items = candidates.slice(-agentCollaborationBatchLimit);
  return {
    items,
    handoffs: items.flatMap((item) => item.handoffs?.filter((handoff) => handoff.targetAgentId === selfAgentId && handoff.requiresResponse) || []),
    waitMs: Math.max(0, lastStartedAt + intervalMs - now),
  };
}

export function visibleCollaborationTimeline(items: CollaborationTimelineItem[]): CollaborationTimelineItem[] {
  const runs = new Map(items.filter((item) => item.kind === "agent_command").map((item) => [item.id, item]));
  const results = new Map(items.filter((item) => item.kind === "agent_result" && item.agentRunId).map((item) => [item.agentRunId as string, item]));
  return items.flatMap((item) => {
    if (item.kind === "agent_result" && item.agentRunId && runs.has(item.agentRunId)) return [];
    if (item.kind !== "agent_command") return [item];
    const result = results.get(item.id);
    if (!result) return [item];
    return [{
      ...item,
      agentRunSummary: item.agentRunSummary,
      agentRunOutput: result.text,
      referenceIds: [...new Set([...item.referenceIds, ...result.referenceIds])],
      handoffs: result.handoffs,
    }];
  });
}

export function selectedTimelineItems(state: CollabViewState): CollaborationTimelineItem[] {
  const selected = new Set(state.selectedIds);
  return state.timeline.filter((item) => selected.has(item.id));
}

export function ownMember(state: CollabViewState) {
  return state.members.find((member) => member.id === state.selfMemberId || member.isSelf);
}
