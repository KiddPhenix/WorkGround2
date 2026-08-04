import type {
  CollaborationIntentClass,
  CollaborationState,
  CollaborationTimelineItem,
  PendingIntent,
} from "./types";

export interface CollabViewState extends CollaborationState {
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
  selectedIds: [],
  pendingIntents: {},
};

function mergeTimeline(current: CollaborationTimelineItem[], incoming: CollaborationTimelineItem[]): CollaborationTimelineItem[] {
  const byId = new Map(current.map((item) => [item.id, item]));
  for (const item of incoming) byId.set(item.id, { ...byId.get(item.id), ...item });
  return [...byId.values()].sort((a, b) => a.sequence - b.sequence || a.createdAt.localeCompare(b.createdAt));
}

function cancelPending(pending: Record<string, PendingIntent>): Record<string, PendingIntent> {
  return Object.fromEntries(Object.entries(pending).map(([id, intent]) => [id, intent.status === "pending" ? { ...intent, status: "dismissed" as const } : intent]));
}

export function collabReducer(state: CollabViewState, action: CollabAction): CollabViewState {
  switch (action.type) {
    case "CONNECTING":
      return { ...state, status: "connecting", operation: action.operation, lastError: undefined };
    case "SYNCING":
      return { ...state, status: action.reconnecting ? "reconnecting" : "syncing", operation: "sync", lastError: undefined, pendingIntents: cancelPending(state.pendingIntents) };
    case "STATE":
      return {
        ...state,
        ...action.state,
        status: action.state.status || "connected",
        timeline: mergeTimeline(state.timeline.filter((item) => !item.localPending), action.state.timeline || []),
        members: action.state.members || [],
        operation: undefined,
        pendingIntents: action.state.status === "connected" ? state.pendingIntents : cancelPending(state.pendingIntents),
      };
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

export function selectedTimelineItems(state: CollabViewState): CollaborationTimelineItem[] {
  const selected = new Set(state.selectedIds);
  return state.timeline.filter((item) => selected.has(item.id));
}

export function ownMember(state: CollabViewState) {
  return state.members.find((member) => member.id === state.selfMemberId || member.isSelf);
}
