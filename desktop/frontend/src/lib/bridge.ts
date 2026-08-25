// bridge is the single seam between the React app and the Go kernel. In the Wails
// shell it calls the bound App methods (window.go.main.App.*) and subscribes to
// the runtime event stream (window.runtime.EventsOn). In a plain browser (`pnpm
// dev` outside the shell) those globals are absent, so it falls back to a mock
// that streams a canned turn through the same contract — letting the whole UI be
// developed and laid out without rebuilding the Go side.

// @ts-ignore `wails generate module` creates this locally; fresh checkouts keep
// typecheck green by falling back to a disabled drift check below.
import type * as GeneratedApp from "../../wailsjs/go/main/App";
import type { WailsWorkBindings } from "../work/wailsAdapter";
import type {
  AssistantAttentionItem,
  AssistantCancelInput,
  AssistantCreateInput,
  AssistantMemory,
  AssistantMemoryInput,
  AssistantListResult,
  AssistantRecord,
  AssistantResolveAttentionInput,
  AssistantResumeInput,
  AssistantRoutine,
  AssistantRoutineInput,
  AssistantRun,
  AssistantRunNowInput,
  AssistantSnapshot,
  AssistantSubmitInputInput,
  AssistantUpdateInput,
} from "../custom/features/assistant/assistant.types";
import type {
  CollaborationActionResult,
  CollaborationInvite,
  CollaborationIntentResult,
  CollaborationState,
  HostCollaborationRoomInput,
  JoinCollaborationRoomInput,
  PostCollaborationMessageInput,
  RespondCollaborationRequestInput,
  CollaborationAgentRunResponse,
  CollaborationToolApprovalMode,
  CollaborationFileTransfer,
  CollaborationFilePreview,
  StartCollaborationAgentInput,
  UpdateCollaborationAgentConfigInput,
  UpdateCollaborationProfileInput,
} from "../collab/types";

import { addBreadcrumb } from "./breadcrumbs";
import { projectIconKey } from "./projectIcons";
import { t } from "./i18n";
import { providerRequiresKey } from "./providerModels";
import { DEFAULT_STATUS_BAR_ITEMS, normalizeStatusBarItems } from "./statusBarItems";
import { modeHasAutoApproveTools, modeWithAutoApproveTools, modeWithPlan, normalizeCollaborationMode, normalizeMode, normalizeTokenMode, normalizeToolApprovalMode } from "./types";

import type {
  AutoResearchFindingView,
  ArtifactView,
  AddOnPanelActionInput,
  AddOnPanelActionResult,
  AddOnDialogResult,
  AddOnPanelQueryResult,
  AutoResearchEvidenceView,
  AutoResearchStatusView,
  AICollaborationInjectResult,
  BalanceInfo,
  BotConnectionDiagnostic,
  BotInstallPollResult,
  BotInstallStartResult,
  BotRuntimeStatusView,
  BotSettingsView,
  CapabilitiesView,
  CheckpointMeta,
  CommandInfo,
  CollaborationSettingsView,
  ContextInfo,
  ContextPanelInfo,
  DecisionSkillExportResult,
  DirEntry,
  DesktopStartupSettingsView,
	DecisionCreateInput,
	DecisionResolveInput,
	DecisionStateView,
  DroppedItem,
  DrawAddonGenerateInput,
  DrawAddonProviderInput,
  DrawAddonProviderView,
  DrawAddonTaskView,
	DSHWorkbenchView,
  EffortInfo,
  FilePreview,
  HistoryMessage,
  HistoryPage,
  HookConfigView,
  HooksSettingsView,
  JobView,
  MCPServerInput,
  MemorySuggestion,
  MemorySuggestionsView,
  MemoryView,
  Meta,
  Mode,
  ModelInfo,
  LocalCLIOptionView,
  NetworkView,
  PluginInstallOptions,
  PluginView,
  ProjectNode,
  MarkUnreadReadInput,
  ResolvedSession,
  UnreadState,
  PromptHistoryEntry,
  PromptHistoryResult,
  ProviderView,
  QuestionAnswer,
  ServerView,
  SessionMeta,
  SessionBackgroundImageView,
  SessionBackgroundSettingsView,
  SettingsView,
  SkillsSettingsView,
  SkillShareDeleteOptions,
  SkillShareProfileInput,
  SkillShareProfileView,
  SkillShareSyncOptions,
  SkillShareTaskView,
  SkillShareUpdateOptions,
  SkillRootView,
  SkillSuggestion,
  SkillView,
  SlashArgsResult,
  TabMeta,
  TopicMeta,
  ToolApprovalMode,
  VocabularyMatch,
  VocabularyRefreshResult,
  UpdateDownloadResult,
  UpdateInfo,
  UpdateProgress,
  WireEvent,
  WorkspaceChangesView,
  WorkspaceWelcomeView,
  PinMemoryResult,
  PinnedMemoriesView,
  GitCommitView,
  GitCommitDetailView,
  WorkspaceView,
  BrowserPermissionsView,
  BrowserLaunchView,
} from "./types";

const GLOBAL_PROJECT_ORDER_KEY = "__global__";

function emptyBotAccess() {
  return {
    enabled: false,
    allowAll: false,
    pairingEnabled: false,
    users: [],
    groups: [],
    approvers: [],
    admins: [],
  };
}

function stripGoalResearchFlags(arg: string): string {
  const parts = arg.trim().split(/\s+/).filter(Boolean);
  while (parts.length > 0) {
    const flag = parts[0].toLowerCase();
    if (flag !== "--research" && flag !== "--auto-research" && flag !== "--deep" && flag !== "--simple" && flag !== "--no-research") break;
    parts.shift();
  }
  return parts.join(" ");
}

// AppBindings is derived from the Wails-generated Go → TS method signatures, so
// the compiler catches drift between the Go binding surface and the frontend mock.
// Run `wails generate module` after adding/renaming a bound method on App, then
// `pnpm typecheck` to verify the mock still satisfies the contract.
//
// Types for the new native-feel bindings — kept inline since they are
// bridge-specific and only used in AppBindings / the dev mock.
interface NativeConfirmRequest {
  title: string;
  message: string;
  detail: string;
  confirmLabel: string;
  cancelLabel: string;
  destructive: boolean;
}

interface DesktopWindowState {
  width: number;
  height: number;
  x: number;
  y: number;
  maximised: boolean;
}

export interface WidgetOption {
  label: string;
  description?: string;
  value: string;
  code?: "allow" | "deny";
}

// WidgetQuestion is one structured `ask` question projected from the backend
// (mirrors WireAskQuestion). The icon popup renders every standard ask — 1-4
// questions, 2-4 options, single/multi, custom answers — from these typed
// fields instead of parsing prompt text.
export interface WidgetQuestion {
  id: string;
  header?: string;
  prompt: string;
  options: WidgetOption[];
  multi?: boolean;
}

export interface WidgetMessage {
  id: string;
  revision: string;
  tabId: string;
  projectName: string;
  taskName: string;
  taskNameCode?: "current";
  kind: "choice" | "reply" | "result" | "error";
  stateLabel: string;
  stateCode?: "confirm" | "reply" | "action" | "complete";
  message: string;
  messageCode?: "complete_fallback" | "multi_question";
  messageCount?: number;
  interactionId?: string;
  questionId?: string;
  questions?: WidgetQuestion[];
  options: WidgetOption[];
  requiresWindow?: boolean;
}

export interface WidgetSnapshot {
  mode: boolean;
  current?: WidgetMessage;
  remainingCount: number;
  runningCount: number;
  waitingCount: number;
  completedCount: number;
  failedCount: number;
  backgroundCount: number;
  isIdle: boolean;
  info: WidgetInfo;
  version: string;
}

export interface WidgetInfo {
  totalTokens: number;
  tokenPartial: boolean;
  idleSince?: number;
  system: WidgetSystemInfo;
  models: WidgetModelInfo[];
}

export interface WidgetSystemInfo {
  available: boolean;
  network: "online" | "offline" | "unknown";
  cpu: number;
  memory: number;
  sampledAt?: number;
}

export interface WidgetModelInfo {
  provider?: string;
  model: string;
  brand: string;
}

export interface WidgetActionInput {
  itemId: string;
  revision: string;
  requestId: string;
	action: "answer" | "approve" | "deny" | "next" | "retry" | "open" | "later";
  values: string[];
  answers?: QuestionAnswer[];
}

export interface WidgetActionResult {
  status: "accepted" | "already_applied" | "stale" | "retryable_error" | "invalid";
  error?: string;
  snapshot: WidgetSnapshot;
}

export interface WidgetConversationInput {
  prompt: string;
  requestId: string;
  workspace?: string;
  /** Per-send model ref override (must be one of app.Models() refs); empty = user default. */
  model?: string;
  /** Per-send tool approval posture; empty = user default. */
  approvalMode?: string;
  /** Frontend-only optimistic icon labels; backend merges authoritative titles. */
  existingTitles?: string[];
}

export interface WidgetWorkspaceOption {
  scope: "auto" | "project" | "global";
  name: string;
  root?: string;
  icon?: string;
  pinned?: boolean;
  lastActivityAt?: number;
}

export interface WidgetConversationResult {
  status: "accepted" | "already_applied" | "retryable_error" | "invalid";
  error?: string;
  tabId?: string;
  sessionName?: string;
  workspaceRoot?: string;
  workspaceName?: string;
  routeReason?: string;
  routeReasonCode?: "global_fallback" | "recent" | "name_match" | "history" | "current" | "primary" | "manual";
  snapshot: WidgetSnapshot;
}

export type DesktopIconStatus = "idle" | "unread" | "thinking" | "running" | "needs_input" | "needs_confirm" | "done" | "failed";

export interface DesktopIconNotice {
  id: string; revision: string; kind: "message" | "needs_input" | "needs_confirm" | "completed" | "failed";
  priority: number; title: string; body: string; createdAt: number; tabId?: string; conversation?: string;
  readSequence?: number; interactionId?: string; questionId?: string; questions?: WidgetQuestion[]; options: WidgetOption[]; retryable?: boolean;
  attention?: "mention_member" | "mention_agent" | "mention_both";
  summaryStatus?: "ready" | "failed";
}

export interface DesktopIconPosition { row: "top" | "bottom"; zone: "conversation" | "running" | "workspace" | "fixed"; order: number; }
export interface DesktopIconRuntime { phase: string; summary: string; elapsedMs: number; updatedAt: number; }
// DesktopIconTaskRef is the typed session identity every task icon snapshot
// carries (scope/workspaceRoot/topicID/sessionPath). It is display data for
// the Agent Icon seed fallback; opening still routes through the backend.
export interface DesktopIconTaskRef {
  scope: string;
  workspaceRoot?: string;
  topicId?: string;
  sessionPath?: string;
}
export interface DesktopIconItem {
	id: string; kind: "room" | "person" | "task" | "workspace" | "fixed" | "external"; sourceId: string; title: string;
  subtitle?: string; icon?: string; status: DesktopIconStatus; unreadCount: number; activityCount?: number; notifications: DesktopIconNotice[];
  runtimeStatus?: DesktopIconRuntime; position: DesktopIconPosition; revision: string; retained?: boolean;
  // 纯展示字段（Agent Icon）：稳定身份 seed 与 workspace 图标键；旧 retained
  // 数据可能缺 sessionId，前端按 sessionRef/sessionPath 稳定回退。
  sessionId?: string;
  appearanceSeed?: string;
  workspaceIcon?: string;
  sessionRef?: DesktopIconTaskRef;
  conversationSequence?: number;
	actions?: Array<"launch" | "cancel" | "open" | "retry" | "resume" | "approve" | "send" | "remove">;
	sourceRevision?: number;
}
export interface DesktopIconDelegation {
  id: string; kind: "subagent" | "background" | "cli"; content: string; status: "running";
  sessionTitle: string; workspaceName?: string; updatedAt?: number; sessionRef?: DesktopIconTaskRef;
}
export interface DesktopIconSnapshot { items: DesktopIconItem[]; delegations: DesktopIconDelegation[]; delegationError?: string; revision: string; hoverStatusDelayMs: number; style: "pager" | "icons"; unreadRevision: number; error?: string; }
export interface DesktopIconSearchItem { id: string; kind: "session" | "room" | "person" | "task" | "workspace"; title: string; subtitle?: string; sourceId: string; lastActivityAt?: number; }
export interface DesktopIconSearchResult { items: DesktopIconSearchItem[]; error?: string; }
export interface DesktopIconActionInput { itemId: string; noticeId?: string; revision: string; requestId: string; action: string; values?: string[]; answers?: QuestionAnswer[]; position?: DesktopIconPosition; conversation?: string; readSequence?: number; }
export interface DesktopIconActionResult { status: "accepted" | "already_applied" | "stale" | "retryable_error" | "invalid"; error?: string; snapshot: DesktopIconSnapshot; }
export interface ExternalRunCapabilities { cancel: boolean; open: boolean; retry: boolean; resume: boolean; approve: boolean; send: boolean; }
export interface ExternalRunProjection {
	id: string; source: "dsh" | "codex" | "claude"; nativeSessionId?: string; ownership: "managed" | "observed"; workspace?: string;
	title?: string; state: "queued" | "starting" | "running" | "waiting_user" | "succeeded" | "failed" | "cancelled" | "interrupted" | "stale";
	activity?: "thinking" | "tool" | "responding" | "background" | "idle"; activityLabel?: string; summary?: string;
	capabilities: ExternalRunCapabilities; revision: number; createdAt: string; updatedAt: string;
}
export interface ExternalRunProfileView { id: string; ready: boolean; root?: string; version?: string; capabilities: ExternalRunCapabilities; missing?: Array<{ kind: string; detail: string }>; error?: string; }
export interface ExternalRunSnapshot { runs: ExternalRunProjection[]; dsh: ExternalRunProfileView; workspace?: string; revision: string; error?: string; }
export interface ExternalRunReceipt { status: "accepted" | "already_applied" | "stale" | "retryable_error" | "invalid"; runId?: string; eventId?: string; revision?: number; message?: string; }
export interface ExternalRunLaunchInput { requestId: string; workspace?: string; prompt: string; }
export interface ExternalRunLaunchResult { receipt: ExternalRunReceipt; run: ExternalRunProjection; snapshot: ExternalRunSnapshot; }
export interface ExternalRunCancelInput { runId: string; requestId: string; }
export interface ExternalRunActionResult { receipt: ExternalRunReceipt; run: ExternalRunProjection; snapshot: ExternalRunSnapshot; }
export interface DesktopIconRect { x: number; y: number; width: number; height: number; }
export interface DesktopIconHitRegionsInput { rects: DesktopIconRect[]; generation: number; }
// DesktopIconSurfaceInput is one monotonic native-canvas resize request. Width
// and height are the content's logical bounds, envelope is the safety margin
// added on every side, and generation is the coordinator's request token.
export interface DesktopIconSurfaceInput { width: number; height: number; envelope: number; generation: number; }
export interface DesktopIconSurfaceResult { width: number; height: number; x: number; y: number; generation: number; }
export interface CreateBlankSessionInput { scope: string; workspaceRoot: string; requestId: string; }
export interface DailyRoutine {
  id: string; workspaceRoot?: string; name: string; prompt: string; goal: string;
  successSteps?: string[]; failureLessons?: string[]; sourceSessionPath?: string;
  sourceRevision: string; createdAt: number; updatedAt: number;
}
export interface DailyRoutineResult { status: "accepted" | "already_applied" | "pending" | "retryable_error" | "invalid"; error?: string; routine?: DailyRoutine; tabId?: string; }
// DesktopIconDiagnosticsInput is one typed diagnostics record appended by the
// icon widget for an idle-hover trace. It carries measurements and stable
// widget markers only — never task content, prompts, icon titles or user
// paths — and every field is validated on the Go side.
export interface DesktopIconDiagnosticsInput {
  kind: "hover_start" | "hover_recovery";
  traceId: string;
  targetKind?: "icon" | "anchor";
  idleMs?: number;
  ts?: number;
  t0?: number;
  visibility?: string;
  focus?: boolean;
  viewportW?: number;
  viewportH?: number;
  dpr?: number;
  iconCount?: number;
  revision?: string;
  frames?: number;
  worstFrameGapMs?: number;
  avgFrameGapMs?: number;
  longTasks?: number;
  longTasksMaxMs?: number;
  longTasksTotalMs?: number;
  visibilityChanges?: number;
  domMutations?: number;
  layoutShifts?: number;
  endedBy?: "healthy" | "timeout" | "aborted";
  durationMs?: number;
}

// AppBindings is the hand-written contract between the React app and the Go
// kernel. It uses local types (types.ts) so components don't import generated
// model classes. _CheckGeneratedBindings catches drift: when a Go method is
// added or renamed, the generated types shift, and a key present in GeneratedApp
// but missing from AppBindings causes a type error here. Fix: add the new method
// to AppBindings, then run `pnpm typecheck` to verify.
export interface WorkArtifactFileIntent {
  workId: string;
  definitionRevision: number;
  slotId: string;
  slotRevision: number;
  artifactRefId: string;
}

export interface AppBindings extends WailsWorkBindings {
  Platform(): Promise<string>;
	DecisionState(): Promise<DecisionStateView>;
	CreateDecision(input: DecisionCreateInput): Promise<unknown>;
	ResolveDecision(input: DecisionResolveInput): Promise<DecisionStateView>;
	DeferDecision(id: string): Promise<DecisionStateView>;
	ResumeDecision(id: string): Promise<DecisionStateView>;
	CancelDecision(id: string): Promise<DecisionStateView>;
	SaveDecisionSettings(input: { externalMode: string; localOnlyUntil: string; smartGraceSec: number }): Promise<DecisionStateView>;
	SaveDecisionChannel(input: { id: string; name: string; kind: string; enabled: boolean; connectionId: string; domain: string; chatId: string; chatType: string }): Promise<DecisionStateView>;
	DeleteDecisionChannel(id: string): Promise<DecisionStateView>;
	TestDecisionChannel(id: string): Promise<void>;
	InstallDecisionSkill(): Promise<AICollaborationInjectResult>;
	ExportDecisionSkills(): Promise<DecisionSkillExportResult>;
  UnreadState(): Promise<UnreadState>;
  MarkUnreadRead(input: MarkUnreadReadInput): Promise<UnreadState>;
  ResolveLegacySessionUnread(conversationKey: string): Promise<ResolvedSession>;
  ResolveUnreadSession(conversationKey: string): Promise<ResolvedSession>;
  EnterWidgetMode(): Promise<WidgetSnapshot>;
  ExitWidgetMode(tabID: string): Promise<void>;
  IsWidgetMode(): Promise<boolean>;
	GetWidgetSnapshot(): Promise<WidgetSnapshot>;
	ApplyWidgetAction(input: WidgetActionInput): Promise<WidgetActionResult>;
	StartWidgetConversation(input: WidgetConversationInput): Promise<WidgetConversationResult>;
	OpenWidgetWorkspace(workspace: string, requestId: string): Promise<TabMeta>;
	ListWidgetWorkspaces(): Promise<WidgetWorkspaceOption[]>;
	GetDesktopIconSnapshot(): Promise<DesktopIconSnapshot>;
	GetExternalRunSnapshot(): Promise<ExternalRunSnapshot>;
	LaunchDSHRun(input: ExternalRunLaunchInput): Promise<ExternalRunLaunchResult>;
	CancelExternalRun(input: ExternalRunCancelInput): Promise<ExternalRunActionResult>;
	GetDesktopWorkspaceSlots(): Promise<number>;
	GetDesktopRoomPins(): Promise<string[]>;
	GetDesktopRoomIcons(): Promise<Record<string, string>>;
	DesktopIconSearch(query: string): Promise<DesktopIconSearchResult>;
	ApplyDesktopIconAction(input: DesktopIconActionInput): Promise<DesktopIconActionResult>;
	CreateDailyRoutine(input: { tabId?: string; sessionRef?: DesktopIconTaskRef; requestId: string }): Promise<DailyRoutineResult>;
	ListDailyRoutines(workspaceRoot: string): Promise<DailyRoutine[]>;
	RunDailyRoutine(input: { workspaceRoot: string; routineId: string; requestId: string }): Promise<DailyRoutineResult>;
	RenameDailyRoutine(input: { workspaceRoot: string; routineId: string; name: string }): Promise<DailyRoutineResult>;
	DeleteDailyRoutine(input: { workspaceRoot: string; routineId: string }): Promise<DailyRoutineResult>;
	SetDesktopIconHitRegions(input: DesktopIconHitRegionsInput): Promise<void>;
	SetDesktopIconSurface(input: DesktopIconSurfaceInput): Promise<DesktopIconSurfaceResult>;
	SetDesktopWorkspaceSlots(slots: number): Promise<void>;
	SetDesktopRoomPinned(topicID: string, pinned: boolean): Promise<void>;
	SetDesktopRoomIcon(topicID: string, icon: string): Promise<void>;
	WriteDesktopIconDiagnostics(input: DesktopIconDiagnosticsInput): Promise<void>;
	DesktopIconDiagnosticsPath(): Promise<string>;
	RefreshWidgetWindowRegion(): Promise<void>;
  MinimiseMainWindow(): Promise<void>;
  ToggleMaximiseMainWindow(): Promise<void>;
  IsMainWindowMaximised(): Promise<boolean>;
  CloseMainWindow(): Promise<void>;
  DismissMainWindow(): Promise<void>;
  GetCollaborationState(sessionID: string): Promise<CollaborationState>;
  RetryCollaboration(sessionID: string): Promise<CollaborationState>;
  HostCollaborationRoom(input: HostCollaborationRoomInput): Promise<CollaborationState>;
  JoinCollaborationRoom(input: JoinCollaborationRoomInput): Promise<CollaborationState>;
  ListCollaborationRooms?(input: unknown): Promise<unknown>;
  ProbeCollaborationRelay?(relayID: string): Promise<unknown>;
  GetCollaborationInvite(sessionID: string): Promise<CollaborationInvite>;
  LeaveCollaborationRoom(sessionID: string): Promise<void>;
  ClassifyCollaborationIntent(input: { sessionID: string; text: string }): Promise<CollaborationIntentResult>;
  PostCollaborationMessage(input: PostCollaborationMessageInput): Promise<CollaborationActionResult>;
  StartCollaborationAgent(input: StartCollaborationAgentInput): Promise<CollaborationActionResult>;
  StopCollaborationAgentRun(sessionID: string, runID: string): Promise<void>;
  CancelCollaborationQueuedTask(input: { sessionID: string; taskID: string }): Promise<CollaborationActionResult>;
  RespondCollaborationAgentRun(input: { sessionID: string; runID: string; allow: boolean } & CollaborationAgentRunResponse): Promise<CollaborationActionResult>;
  RespondCollaborationRequest(input: RespondCollaborationRequestInput): Promise<CollaborationActionResult>;
  UpdateCollaborationAgentConfig(input: UpdateCollaborationAgentConfigInput & { sessionID: string }): Promise<CollaborationState>;
  UpdateCollaborationProfile(input: UpdateCollaborationProfileInput & { sessionID: string }): Promise<CollaborationState>;
  UpdateCollaborationToolApprovalMode(input: { sessionID: string; mode: CollaborationToolApprovalMode }): Promise<CollaborationState>;
  ShareCollaborationFiles(input: { sessionID: string; paths: string[] }): Promise<CollaborationFileTransfer[]>;
  ReceiveCollaborationFile(input: { sessionID: string; fileID: string; destination?: string }): Promise<CollaborationFileTransfer>;
  PauseCollaborationFile(input: { sessionID: string; fileID: string }): Promise<CollaborationFileTransfer>;
  ResumeCollaborationFile(input: { sessionID: string; fileID: string }): Promise<CollaborationFileTransfer>;
  RevokeCollaborationFile(input: { sessionID: string; fileID: string }): Promise<CollaborationActionResult>;
  OpenCollaborationFile(input: { sessionID: string; fileID: string }): Promise<void>;
  RevealCollaborationFile(input: { sessionID: string; fileID: string }): Promise<void>;
  PreviewCollaborationFile(input: { sessionID: string; fileID: string }): Promise<CollaborationFilePreview>;
  // ── Heartbeat ──
  HeartbeatListTasks(): Promise<unknown>;
  HeartbeatReloadTasks(): Promise<unknown>;
  HeartbeatSaveTasks(tasks: unknown): Promise<void>;
  HeartbeatTriggerNow(id: string): Promise<void>;
  HeartbeatGenerateID(): Promise<string>;
  HeartbeatListConversions(): Promise<unknown>;
  HeartbeatConvertToAssistant(id: string): Promise<unknown>;
  // ── Assistant mode ──
  AssistantList(): Promise<AssistantListResult>;
  AssistantGet(assistantId: string): Promise<AssistantSnapshot>;
  AssistantCreate(input: AssistantCreateInput): Promise<AssistantSnapshot>;
  AssistantUpdate(input: AssistantUpdateInput): Promise<AssistantRecord>;
  AssistantPutRoutine(input: AssistantRoutineInput): Promise<AssistantRoutine>;
  AssistantApplyMemory(input: AssistantMemoryInput): Promise<AssistantMemory>;
  AssistantRunNow(input: AssistantRunNowInput): Promise<AssistantRun>;
  AssistantSubmitInput(input: AssistantSubmitInputInput): Promise<AssistantRun>;
  AssistantResolveAttention(input: AssistantResolveAttentionInput): Promise<AssistantAttentionItem>;
  AssistantResume(input: AssistantResumeInput): Promise<AssistantRun>;
  AssistantCancel(input: AssistantCancelInput): Promise<AssistantRun>;
  Submit(input: string): Promise<void>;
  SubmitToTab(tabID: string, input: string): Promise<void>;
  SubmitDisplay(display: string, input: string): Promise<void>;
  SubmitDisplayToTab(tabID: string, display: string, input: string): Promise<void>;
  SubmitEditedDisplayToTab(tabID: string, display: string, input: string, original: string): Promise<void>;
  SendWorkChat(tabID: string, workID: string, display: string, text: string): Promise<void>;
  RunShell(command: string): Promise<void>;
  RunShellForTab(tabID: string, command: string): Promise<void>;
  Steer(text: string): Promise<void>;
  SteerForTab(tabID: string, text: string): Promise<void>;
  Cancel(): Promise<void>;
  CancelTab(tabID: string): Promise<void>;
  Approve(id: string, allow: boolean, session: boolean, persist: boolean): Promise<void>;
  ApprovePending(allow: boolean): Promise<void>;
  ApproveTab(tabID: string, id: string, allow: boolean, session: boolean, persist: boolean): Promise<void>;
  AnswerQuestion(id: string, answers: QuestionAnswer[]): Promise<void>;
  AnswerQuestionForTab(tabID: string, id: string, answers: QuestionAnswer[]): Promise<void>;
  ReplayPendingPrompts(): Promise<void>;
  ReplayPendingPromptsForSession(sessionID: string): Promise<void>;
  SetPlanMode(on: boolean): Promise<void>;
  SetMode(mode: string): Promise<void>;
  SetModeForTab(tabID: string, mode: string): Promise<void>;
  SetAutoApproveTools(on: boolean): Promise<void>;
  SetCollaborationMode(mode: string): Promise<void>;
  SetCollaborationModeForTab(tabID: string, mode: string): Promise<void>;
  SetToolApprovalMode(mode: string): Promise<void>;
  SetToolApprovalModeForTab(tabID: string, mode: string): Promise<void>;
  SetGoal(goal: string): Promise<void>;
  SetGoalForTab(tabID: string, goal: string): Promise<void>;
  ClearGoal(): Promise<void>;
  ClearGoalForTab(tabID: string): Promise<void>;
  Compact(): Promise<void>;
  CompactForSession(sessionID: string): Promise<void>;
  NewSession(): Promise<void>;
  NewSessionForSession(sessionID: string): Promise<void>;
  ClearSession(): Promise<void>;
  ClearSessionForSession(sessionID: string): Promise<void>;
  History(): Promise<HistoryMessage[]>;
  HistoryForTab(tabID: string): Promise<HistoryMessage[]>;
  HistoryPage(beforeTurn: number, limit: number): Promise<HistoryPage>;
  HistoryPageForTab(tabID: string, beforeTurn: number, limit: number): Promise<HistoryPage>;
  HistoryCheckpointTurnsForTab(tabID: string): Promise<number[]>;
  Checkpoints(): Promise<CheckpointMeta[]>;
  CheckpointsForTab(tabID: string): Promise<CheckpointMeta[]>;
  Rewind(turn: number, scope: string): Promise<void>;
  RewindForSession(sessionID: string, turn: number, scope: string): Promise<void>;
  Fork(turn: number): Promise<TabMeta>;
  ForkForSession(sessionID: string, turn: number): Promise<TabMeta>;
  SummarizeFrom(turn: number): Promise<void>;
  SummarizeFromForSession(sessionID: string, turn: number): Promise<void>;
  SummarizeUpTo(turn: number): Promise<void>;
  SummarizeUpToForSession(sessionID: string, turn: number): Promise<void>;
  ListSessions(): Promise<SessionMeta[]>;
  ListSessionsForSession(sessionID: string): Promise<SessionMeta[]>;
  ListTrashedSessions(): Promise<SessionMeta[]>;
  ResumeSession(path: string): Promise<HistoryMessage[]>;
  ResumeSessionForTab(tabID: string, path: string): Promise<HistoryMessage[]>;
  ResumeSessionPage(path: string, limit: number): Promise<HistoryPage>;
  ResumeSessionPageForTab(tabID: string, path: string, limit: number): Promise<HistoryPage>;
  OpenChannelSessionForTab(tabID: string, path: string): Promise<HistoryMessage[]>;
  OpenChannelSessionPageForTab(tabID: string, path: string, limit: number): Promise<HistoryPage>;
  OpenChannelSession(path: string): Promise<TabMeta>;
  PreviewSession(path: string): Promise<HistoryMessage[]>;
  DeleteSession(path: string): Promise<void>;
  RestoreSession(path: string): Promise<void>;
  PurgeTrashedSession(path: string): Promise<void>;
  RenameSession(path: string, title: string): Promise<void>;
  ScanPromptHistory(nonce: string): Promise<PromptHistoryResult>;
  ListWorkspaces(): Promise<WorkspaceView[]>;
  PickWorkspace(): Promise<string>;
  SwitchWorkspace(path: string): Promise<string>;
  RemoveWorkspace(path: string): Promise<void>;
  ContextUsage(): Promise<ContextInfo>;
  ContextUsageForTab(tabID: string): Promise<ContextInfo>;
  Balance(): Promise<BalanceInfo>;
  BalanceForTab(tabID: string): Promise<BalanceInfo>;
  Jobs(): Promise<JobView[]>;
  JobsForTab(tabID: string): Promise<JobView[]>;
  ToolResultForTab(tabID: string, toolID: string): Promise<{ args: string; output: string } | null>;
  ArtifactsForTab(tabID: string): Promise<ArtifactView[]>;
  Meta(): Promise<Meta>;
  MetaForTab(tabID: string): Promise<Meta>;
  AutoResearchCurrent(): Promise<AutoResearchStatusView>;
  AutoResearchStatus(tabID: string): Promise<AutoResearchStatusView>;
  AutoResearchList(tabID: string): Promise<AutoResearchStatusView[]>;
  AutoResearchFindings(tabID: string, limit: number): Promise<AutoResearchFindingView[]>;
  AutoResearchOpenTask(tabID: string): Promise<void>;
  AutoResearchRecordEvidence(tabID: string, criterionID: string, input: AutoResearchEvidenceView): Promise<void>;
  Commands(): Promise<CommandInfo[]>;
  Capabilities(): Promise<CapabilitiesView>;
  MCPServers(): Promise<ServerView[]>;
  SkillsSettings(): Promise<SkillsSettingsView>;
  Plugins(): Promise<PluginView[]>;
  AddOnPanelSchema(name: string, panelID: string): Promise<string>;
  /** Generic AddOn panel query — replaces per-kind SkillShareProfiles / DrawAddonProviders. */
  AddOnPanelQuery(pluginName: string, panelID: string, adapter: string): Promise<AddOnPanelQueryResult>;
  /** Generic AddOn panel action — replaces per-kind Save/Sync/Delete/Generate methods. */
  AddOnPanelAction(pluginName: string, panelID: string, adapter: string, action: AddOnPanelActionInput): Promise<AddOnPanelActionResult>;
  /** AddOn dialog support — poll and dismiss dialogs triggered by plugin runtimes. */
  GetPendingAddOnDialog(): Promise<Record<string, unknown> | null>;
  DismissAddOnDialog(pluginName: string, panelID: string, submitted: boolean, form: Record<string, unknown>, actionID: string): Promise<void>;
  /** Block until a dialog is posted (long-poll). */
  WaitAddOnDialogChange(): Promise<void>;
  /** Query panel data for a pending dialog (auto-derives adapter). */
  AddOnDialogQuery(pluginName: string, panelID: string): Promise<AddOnPanelQueryResult>;
  /** Forward a dialog form action (auto-derives adapter). */
  AddOnDialogAction(pluginName: string, panelID: string, action: AddOnPanelActionInput): Promise<AddOnPanelActionResult>;
  /** Triggered from MCP panel/dialog handling; blocks until dismissed. */
  TriggerAddOnDialog(mcpServer: string, pluginName: string, panelID: string, message: string): Promise<AddOnDialogResult>;

  /** @deprecated Use AddOnPanelQuery/AddOnPanelAction instead. */
  SkillShareProfiles(): Promise<SkillShareProfileView[]>;
  SaveSkillShareProfile(input: SkillShareProfileInput, secretValue?: string): Promise<SkillShareProfileView>;
  SyncSkillShareProfile(id: string, options: SkillShareSyncOptions): Promise<SkillShareTaskView>;
  DeleteSkillShareProfile(id: string, options: SkillShareDeleteOptions): Promise<SkillShareProfileView>;
  RecoverSkillShareProfiles(): Promise<SkillShareTaskView[]>;
  FlowSkillShareProfiles(): Promise<SkillShareProfileView[]>;
  SaveFlowSkillShareProfile(input: SkillShareProfileInput, secretValue?: string): Promise<SkillShareProfileView>;
  SyncFlowSkillShareProfile(id: string, options: SkillShareSyncOptions): Promise<SkillShareTaskView>;
  DeleteFlowSkillShareProfile(id: string, options: SkillShareDeleteOptions): Promise<SkillShareProfileView>;
  RecoverFlowSkillShareProfiles(): Promise<SkillShareTaskView[]>;
  DrawAddonProviders(): Promise<DrawAddonProviderView[]>;
  SaveDrawAddonProvider(input: DrawAddonProviderInput, secretValue?: string): Promise<DrawAddonProviderView>;
  DeleteDrawAddonProvider(id: string): Promise<DrawAddonProviderView>;
  GenerateImageWithDrawAddon(input: DrawAddonGenerateInput): Promise<DrawAddonTaskView>;
  PlanPluginInstall(source: string, options: PluginInstallOptions): Promise<string>;
  InstallPlugin(source: string, options: PluginInstallOptions): Promise<string>;
  RemovePlugin(name: string): Promise<void>;
  SetPluginEnabled(name: string, enabled: boolean): Promise<void>;
  UpdatePlugin(name: string): Promise<string>;
  PluginDoctor(name: string): Promise<PluginView>;
	StartDSHWorkbench(name: string): Promise<DSHWorkbenchView>;
	DSHWorkbench(name: string): Promise<DSHWorkbenchView>;
	StopDSHWorkbench(name: string): Promise<DSHWorkbenchView>;
  AddMCPServer(input: MCPServerInput): Promise<number>;
  UpdateMCPServer(name: string, input: MCPServerInput): Promise<void>;
  RemoveMCPServer(name: string): Promise<void>;
  ReconnectMCPServer(name: string): Promise<void>;
  ClearMCPServerAuthentication(name: string): Promise<void>;
  TrustMCPServerTool(name: string, toolName: string): Promise<void>;
  TrustMCPServerTools(name: string, toolNames: string[]): Promise<void>;
  UntrustMCPServerTool(name: string, toolName: string): Promise<void>;
  PickSkillFolder(): Promise<string>;
  PickPluginArchive(): Promise<string>;
  PickPluginFolder(): Promise<string>;
  AddSkillPath(path: string): Promise<void>;
  RemoveSkillPath(path: string): Promise<void>;
  RefreshSkills(): Promise<void>;
  ReloadCommands(): Promise<void>;
  SetSkillEnabled(name: string, enabled: boolean): Promise<void>;
  SetMCPServerEnabled(name: string, enabled: boolean): Promise<void>;
  SetMCPServerTier(name: string, tier: string): Promise<void>;
  SlashArgs(input: string): Promise<SlashArgsResult>;
  CompleteVocabularyForTab(tabID: string, prefix: string, limit: number): Promise<VocabularyMatch[]>;
  RecordVocabularyUseForTab(tabID: string, id: string, useID: string): Promise<void>;
  ActivateSkillVocabularyForTab(tabID: string, name: string): Promise<VocabularyRefreshResult>;
  // Active-tab fallbacks used by the desktop widget (which has no tab of its
  // own until a conversation starts); same data sources as the main Composer.
  CompleteVocabulary(prefix: string, limit: number): Promise<VocabularyMatch[]>;
  RecordVocabularyUse(id: string, useID: string): Promise<void>;
  ListDir(rel: string): Promise<DirEntry[]>;
  ListDirForTab(tabID: string, rel: string): Promise<DirEntry[]>;
  ListDirForWorkspace(root: string, rel: string): Promise<DirEntry[]>;
  SearchFileRefs(query: string): Promise<DirEntry[]>;
  SearchFileRefsForTab(tabID: string, query: string): Promise<DirEntry[]>;
  SearchFileRefsForWorkspace(root: string, query: string): Promise<DirEntry[]>;
  ReadFile(rel: string): Promise<FilePreview>;
  ReadFileForTab(tabID: string, rel: string): Promise<FilePreview>;
  WorkspaceChanges(tabID: string): Promise<WorkspaceChangesView>;
  WorkspaceWelcome(tabID: string): Promise<WorkspaceWelcomeView>;
  PinnedMemoriesForTab(tabID: string): Promise<PinnedMemoriesView>;
  PinMemoryForTab(tabID: string, role: string, content: string, turn: number): Promise<PinMemoryResult>;
  SetPinnedMemoryPinnedForTab(tabID: string, id: string, pinned: boolean): Promise<boolean>;
  GitBranches(): Promise<string[]>;
  GitCheckout(branch: string): Promise<void>;
  WorkspaceGitHistory(tabID: string, path: string): Promise<GitCommitView[]>;
  WorkspaceGitCommitDetail(tabID: string, hash: string, path: string): Promise<GitCommitDetailView>;
  OpenWorkspacePath(rel: string): Promise<void>;
  OpenWorkspacePathForTab(tabID: string, rel: string): Promise<void>;
  OpenWorkArtifactForTab(tabID: string, input: WorkArtifactFileIntent): Promise<void>;
  OpenWorkArtifactURLForTab(tabID: string, input: WorkArtifactFileIntent): Promise<void>;
  RevealWorkspacePath(rel: string): Promise<void>;
  RevealWorkspacePathForTab(tabID: string, rel: string): Promise<void>;
  RevealWorkArtifactForTab(tabID: string, input: WorkArtifactFileIntent): Promise<void>;
  RevealPath(path: string): Promise<void>;
  SavePastedImage(dataUrl: string): Promise<string>;
  SaveClipboardImage(): Promise<string>;
  SavePastedFile(name: string, dataUrl: string): Promise<string>;
  PickExportFile(defaultFilename: string, mimeType: string): Promise<string>;
  SaveExportFile(path: string, payload: string, base64Encoded: boolean): Promise<void>;
  AttachDropped(path: string): Promise<DroppedItem>;
  AttachmentDataURL(path: string): Promise<string>;
  RequestHelpImageDataURL(path: string): Promise<string>;
  RequestHelpOpenImage(path: string): Promise<void>;
  RequestHelpRevealImage(path: string): Promise<void>;
  ArtifactImageDataURL(tabID: string, artifactID: string): Promise<string>;
  ArtifactOpenImage(tabID: string, artifactID: string): Promise<void>;
  ArtifactRevealImage(tabID: string, artifactID: string): Promise<void>;
  Models(): Promise<ModelInfo[]>;
  SetModel(name: string): Promise<void>;
  ModelsForTab(tabID: string): Promise<ModelInfo[]>;
  SetModelForTab(tabID: string, name: string): Promise<void>;
  Effort(): Promise<EffortInfo>;
  SetEffort(level: string): Promise<void>;
  EffortForTab(tabID: string): Promise<EffortInfo>;
  SetEffortForTab(tabID: string, level: string): Promise<void>;
  SetTokenMode(mode: string): Promise<void>;
  SetTokenModeForTab(tabID: string, mode: string): Promise<void>;
  Memory(): Promise<MemoryView>;
  MemorySuggestions(): Promise<MemorySuggestionsView>;
  AcceptMemorySuggestion(suggestion: MemorySuggestion): Promise<string>;
  AcceptSkillSuggestion(suggestion: SkillSuggestion): Promise<string>;
  MemoryForTab(tabID: string): Promise<MemoryView>;
  MemorySuggestionsForTab(tabID: string): Promise<MemorySuggestionsView>;
  AcceptMemorySuggestionForTab(tabID: string, suggestion: MemorySuggestion): Promise<string>;
  AcceptSkillSuggestionForTab(tabID: string, suggestion: SkillSuggestion): Promise<string>;
  Remember(scope: string, note: string): Promise<string>;
  RememberForTab(tabID: string, scope: string, note: string): Promise<string>;
  Forget(name: string): Promise<void>;
  ForgetForTab(tabID: string, name: string): Promise<void>;
  SaveDoc(path: string, body: string): Promise<string>;
  SaveDocForTab(tabID: string, path: string, body: string): Promise<string>;
  DesktopStartupSettings(): Promise<DesktopStartupSettingsView>;
  Settings(): Promise<SettingsView>;
  SessionBackgroundSettings(): Promise<SessionBackgroundSettingsView>;
  RefreshSessionBackgroundSettings(): Promise<SessionBackgroundSettingsView>;
  SetSessionBackgroundSettings(settings: SessionBackgroundSettingsView): Promise<void>;
  PickSessionBackgroundFiles(): Promise<string[]>;
  PickSessionBackgroundFolder(): Promise<string>;
  SessionBackground(tabID: string): Promise<SessionBackgroundImageView>;
  RotateSessionBackground(tabID: string): Promise<SessionBackgroundImageView>;
  TryDeferredConfigRebuild(): Promise<void>;
  HooksSettings(scope: string): Promise<HooksSettingsView>;
  SaveHooksSettings(scope: string, hooks: HookConfigView[]): Promise<void>;
  SaveHooksSettingsForRoot(scope: string, projectRoot: string, hooks: HookConfigView[]): Promise<void>;
  TrustProjectHooks(): Promise<void>;
  TrustProjectHooksForRoot(projectRoot: string): Promise<void>;
  SetDefaultModel(ref: string): Promise<void>;
  SetPlannerModel(ref: string): Promise<void>;
  SetSubagentModel(ref: string): Promise<void>;
  SetSubagentEffort(level: string): Promise<void>;
  SetMaxSubagentDepth(depth: number): Promise<void>;
  SetAutoPlan(mode: string): Promise<void>;
  SetDefaultToolApprovalMode(mode: string): Promise<void>;
  SaveProvider(p: ProviderView): Promise<void>;
  AddOfficialProviderAccess(kind: string, key: string, name: string): Promise<string>;
  FetchProviderModels(p: ProviderView): Promise<string[]>;
  DeleteProvider(name: string): Promise<void>;
  RemoveProviderAccess(name: string): Promise<void>;
  SetProviderKey(apiKeyEnv: string, value: string): Promise<string>;
  ClearProviderKey(apiKeyEnv: string): Promise<void>;
  SetPermissionMode(mode: string): Promise<void>;
  AddPermissionRule(list: string, rule: string): Promise<void>;
  RemovePermissionRule(list: string, rule: string): Promise<void>;
  SetBrowserPermissions(b: BrowserPermissionsView): Promise<void>;
  SetBrowserLaunch(b: BrowserLaunchView): Promise<void>;
  SetSandbox(bash: string, network: boolean, workspaceRoot: string, allowWrite: string[], shell: string): Promise<void>;
  SetNetwork(n: NetworkView): Promise<void>;
  SetCollaboration(c: CollaborationSettingsView): Promise<void>;
  SetBotSettings(b: BotSettingsView): Promise<void>;
  SetBotConnectionToolApprovalMode(connID: string, mode: string): Promise<void>;
  SetBotSecret(envName: string, value: string): Promise<void>;
  ClearBotSecret(envName: string): Promise<void>;
  StartBotConnectionInstall(provider: string, domain: string): Promise<BotInstallStartResult>;
  PollBotConnectionInstall(installID: string): Promise<BotInstallPollResult>;
  BotRuntimeStatus(): Promise<BotRuntimeStatusView>;
  DiagnoseBotConnection(id: string): Promise<BotConnectionDiagnostic>;
  TestBotConnection(id: string, target?: string): Promise<BotConnectionDiagnostic>;
  SetCloseBehavior(mode: string): Promise<void>;
  SetDisplayMode(mode: string): Promise<void>;
  SetDesktopComposerSubmitKey(mode: string): Promise<void>;
  SetStatusBarStyle(style: string): Promise<void>;
  SetStatusBarItems(items: string[]): Promise<void>;
  SetDesktopLanguage(lang: string): Promise<void>;
  SetDesktopAppearance(theme: string, style: string): Promise<void>;
  SetDesktopLayoutStyle(style: string): Promise<void>;
  SetDesktopZoomFactor(factor: number): Promise<void>;
  GetDesktopZoomFactor(): Promise<number>;
  RestartApplication(): Promise<void>;
  SetDesktopCheckUpdates(enabled: boolean): Promise<void>;
  SetDesktopTelemetry(enabled: boolean): Promise<void>;
  SetDesktopMetrics(enabled: boolean): Promise<void>;
  SetDesktopWidgetEnabled(enabled: boolean): Promise<void>;
  SetDesktopWidgetAlwaysOnTop(on: boolean): Promise<void>;
  SetDesktopWidgetShowDelegation(show: boolean): Promise<void>;
  SetDesktopWidgetShowExternalTools(show: boolean): Promise<void>;
  SetDesktopWidgetShowAssistant(show: boolean): Promise<void>;
  SetDesktopWidgetSkin(skin: string): Promise<void>;
  SetDesktopWidgetStyle(style: string): Promise<void>;
  SetDesktopHoverStatusDelayMs(delay: number): Promise<void>;
  SetMemoryCompilerEnabled(enabled: boolean): Promise<void>;
  SetExpandThinking(on: boolean): Promise<void>;
  MigrateDesktopPreferences(language: string, theme: string, style: string): Promise<void>;
  SetAgentParams(temperature: number, maxSteps: number, plannerMaxSteps: number, systemPrompt: string): Promise<void>;
  SetColdResumePrune(enabled: boolean): Promise<void>;
  SetReasoningLanguage(lang: string): Promise<void>;
  SetTrayLocale(locale: "en" | "zh" | "zh-TW"): Promise<void>;
  // SetBypass is the legacy Wails name for YOLO/full-access tool auto-approval
  // (ask questions and plan approvals still wait; deny rules still apply).
  // Runtime-only.
  SetBypass(on: boolean): Promise<void>;
  Version(): Promise<string>;
  AICollaborationPrompt(): Promise<string>;
  InjectAICollaborationPrompt(): Promise<AICollaborationInjectResult>;
  GetGlobalAgentsMD(): Promise<string>;
  SetGlobalAgentsMD(content: string): Promise<void>;
  CheckUpdate(): Promise<UpdateInfo | null>;
  DownloadUpdate(): Promise<UpdateDownloadResult | null>;
  InstallUpdate(): Promise<void>;
  ApplyUpdate(): Promise<void>;
  OpenDownloadPage(): Promise<void>;
  NeedsOnboarding(): Promise<boolean>;
  ConnectKey(apiKey: string): Promise<string>;
  ScanLocalCLIProviders(): Promise<LocalCLIOptionView[]>;
  ConnectLocalCLIProvider(id: string): Promise<void>;
  SkipOnboarding(): Promise<void>;
  // Crash overlay "Send report" (desktop/crash_app.go): scrubs user paths, attaches
  // version/os/arch, POSTs to the collection endpoint. Only ever sent on user click.
  ReportCrash(kind: string, detail: string): Promise<void>;
  ListTabs(): Promise<TabMeta[]>;
  ListRuntimeTabs(): Promise<TabMeta[]>;
  RetryTabStartup(tabID: string): Promise<void>;
  OpenProjectTab(workspaceRoot: string, topicID: string): Promise<TabMeta>;
  OpenGlobalTab(topicID: string): Promise<TabMeta>;
  OpenTopicSession(scope: string, workspaceRoot: string, topicID: string, sessionPath: string): Promise<TabMeta>;
  OpenLinkedSession(scope: string, workspaceRoot: string, topicID: string, sessionPath: string): Promise<TabMeta>;
  CreateWorkSession(input: { scope: string; workspaceRoot: string; requestId: string; tabId?: string }): Promise<{ tabMeta: TabMeta; workView?: unknown; duplicate: boolean; error?: string; recoverable: boolean }>;
  CreateReusableWorkSession(tabID: string, input: { flowId: string; values?: Record<string, unknown>; requestId: string }): Promise<import("../work/types").CreateReusableWorkSessionResult>;
  CreateBlankSession(input: CreateBlankSessionInput): Promise<TabMeta>;
  EnsureBlankTab(scope: string, workspaceRoot: string): Promise<TabMeta>;
  ActivateTopic(scope: string, workspaceRoot: string, topicID: string, sessionPath: string): Promise<TabMeta>;
  ActivateLinkedSession(scope: string, workspaceRoot: string, topicID: string, sessionPath: string): Promise<TabMeta>;
  EnsureBlankSurface(scope: string, workspaceRoot: string): Promise<TabMeta>;
  SetActiveTab(tabID: string): Promise<void>;
  ReorderTabs(tabIDs: string[]): Promise<void>;
  CloseTab(tabID: string): Promise<void>;
  ListProjectTree(): Promise<ProjectNode[]>;
  RenameProject(workspaceRoot: string, title: string): Promise<void>;
  SetProjectColor(workspaceRoot: string, color: string): Promise<void>;
  SetProjectIcon(workspaceRoot: string, icon: string): Promise<void>;
  SetProjectPinned(workspaceRoot: string, pinned: boolean): Promise<void>;
  ReorderProjects(workspaceRoots: string[]): Promise<void>;
  CreateTopic(scope: string, workspaceRoot: string, title: string): Promise<TopicMeta>;
  RenameTopic(topicID: string, title: string): Promise<void>;
  DeleteTopic(topicID: string): Promise<void>;
  TrashTopic(topicID: string): Promise<void>;
  SetTopicPinned(topicID: string, pinned: boolean): Promise<void>;
  SetSessionPinned(path: string, pinned: boolean): Promise<void>;
  ContextPanel(tabID: string): Promise<ContextPanelInfo>;
  CurrentSessionPath(): Promise<string>;
  // New native-feel bindings (added with the desktop native-feel plan).
  ConfirmAction(req: NativeConfirmRequest): Promise<boolean>;
  SaveWindowState(state: DesktopWindowState): Promise<void>;
}

// Compile-time drift check. Exclude<A, B> extracts keys in A that are missing
// from B. If that set is non-empty, AssertNever<non-never> fails with
// "Type 'X' does not satisfy the constraint 'never'".
// _CheckGenToApp errors mean a generated Go method has no TS counterpart.
// These compare method *names* only; full signature checking isn't possible here
// because local types (types.ts) use plain interfaces while generated types
// (models.ts) use classes with a convertValues prototype method. The structural
// mismatch would produce false positives. Method-arity and parameter-order drift
// are caught at the call sites by tsc when components invoke app.<method>(...).
type AssertNever<T extends never> = T;
type GeneratedAppKeys = keyof typeof GeneratedApp;
type GeneratedAppMissing =
  string extends GeneratedAppKeys ? true :
  number extends GeneratedAppKeys ? true :
  symbol extends GeneratedAppKeys ? true :
  false;
export type _CheckGenToApp = AssertNever<
  GeneratedAppMissing extends true ? never : Exclude<GeneratedAppKeys, keyof AppBindings>
>;

interface WailsRuntime {
  EventsOn(name: string, cb: (...data: unknown[]) => void): () => void;
  BrowserOpenURL(url: string): void;
  WindowSetSystemDefaultTheme?(): void;
  WindowSetLightTheme?(): void;
  WindowSetDarkTheme?(): void;
  WindowSetBackgroundColour?(r: number, g: number, b: number, a: number): void;
  WindowGetSize?(): Promise<{ w: number; h: number }>;
  WindowGetPosition?(): Promise<{ x: number; y: number }>;
  WindowIsMaximised?(): Promise<boolean>;
  ClipboardSetText?(text: string): Promise<boolean>;
  // Native OS file drop (desktop only); useDropTarget gates delivery to elements
  // carrying the --wails-drop-target CSS property. Absent in the browser dev mock.
  OnFileDrop?(cb: (x: number, y: number, paths: string[]) => void, useDropTarget: boolean): void;
  OnFileDropOff?(): void;
}

declare global {
  interface Window {
    runtime?: WailsRuntime;
    go?: { main?: { App?: AppBindings } };
  }
}

// Must match desktop/app.go's eventChannel constant.
const EVENT_CHANNEL = "agent:event";
const SESSION_BACKGROUND_EVENT = "session-background:changed";
const RECENT_NATIVE_FILE_DRAG_MS = 2000;
const WAILS_NON_FILE_DRAG_MESSAGE = "additional File object is not a file on the disk";
const UNCAUGHT_ERROR_PREFIX_RE = /^Uncaught(?:\s+\(in promise\))?(?:\s+\w*Error)?:\s*/i;
const WAILS_IPC_CONNECTING_RE = /Failed to execute 'send' on 'WebSocket': Still in CONNECTING state/i;
const WAILS_IPC_NULL_SEND_RE = /Cannot read properties of null \(reading 'send'\)/i;

// Resolve the Wails binding at CALL time, not module-load time: in dev the Wails
// runtime can inject window.go AFTER this module first evaluates, so snapshotting
// once would pin the browser mock for the whole session (and show fake data — the
// dev mock's model list leaking into the real app was exactly this bug).
function realApp(): AppBindings | undefined {
  return typeof window !== "undefined" ? window.go?.main?.App : undefined;
}

let mockSingleton: AppBindings | null = null;
function getMock(): AppBindings {
  if (!mockSingleton) mockSingleton = makeMockApp();
  return mockSingleton;
}

// onEvent subscribes to the agent's typed event stream; returns an unsubscribe.
export function onEvent(cb: (e: WireEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn(EVENT_CHANNEL, (payload) => cb(payload as WireEvent));
  }
  return mockSubscribe(cb);
}

const mockSessionBackgroundListeners = new Set<() => void>();

export function onSessionBackgroundChanged(cb: () => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn(SESSION_BACKGROUND_EVENT, cb);
  }
  mockSessionBackgroundListeners.add(cb);
  return () => mockSessionBackgroundListeners.delete(cb);
}

// onUpdaterProgress subscribes to the auto-updater's progress events (a separate
// channel from the agent stream); returns an unsubscribe. Must match the event
// name emitted in desktop/updater_app.go.
export function onUpdaterProgress(cb: (p: UpdateProgress) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("updater:progress", (p) => cb(p as UpdateProgress));
  }
  updaterListeners.add(cb);
  return () => {
    updaterListeners.delete(cb);
  };
}

function errorMessage(err: unknown): string {
  if (err && typeof err === "object" && "message" in err) {
    const msg = (err as { message?: unknown }).message;
    if (typeof msg === "string") return msg;
  }
  return String(err);
}

export function isWailsNonFileDragError(err: unknown, recentNativeFileDrag = false): boolean {
  const msg = errorMessage(err).trim().replace(UNCAUGHT_ERROR_PREFIX_RE, "");
  if (msg.includes(WAILS_NON_FILE_DRAG_MESSAGE)) return true;
  return recentNativeFileDrag && msg.toLowerCase() === "invalid argument";
}

export function isWailsNonFileDragErrorEvent(
  event: Pick<ErrorEvent, "error" | "message">,
  recentNativeFileDrag = false,
): boolean {
  if (isWailsNonFileDragError(event.error ?? event.message, recentNativeFileDrag)) return true;
  return event.error != null && isWailsNonFileDragError(event.message, recentNativeFileDrag);
}

export function isTransientWailsIPCError(err: unknown): boolean {
  const msg = errorMessage(err).trim().replace(UNCAUGHT_ERROR_PREFIX_RE, "");
  return WAILS_IPC_CONNECTING_RE.test(msg) || WAILS_IPC_NULL_SEND_RE.test(msg);
}

function dataTransferLooksLikeFileDrag(dt: DataTransfer | null): boolean {
  if (!dt) return false;
  if (dt.files?.length > 0) return true;
  return Array.from(dt.types ?? []).includes("Files");
}

let wailsDragSuppressionRefs = 0;
let wailsDragSuppressionUninstall: (() => void) | null = null;
let lastNativeFileDragAt = 0;

export function installWailsNonFileDragErrorSuppression(): () => void {
  if (typeof window === "undefined") return () => {};

  wailsDragSuppressionRefs += 1;
  if (!wailsDragSuppressionUninstall) {
    const markNativeFileDrag = (e: DragEvent) => {
      if (dataTransferLooksLikeFileDrag(e.dataTransfer)) lastNativeFileDragAt = Date.now();
    };
    const hasRecentNativeFileDrag = () => Date.now() - lastNativeFileDragAt <= RECENT_NATIVE_FILE_DRAG_MS;
    const suppressNonFileDragError = (e: ErrorEvent) => {
      if (isWailsNonFileDragErrorEvent(e, hasRecentNativeFileDrag()) || isTransientWailsIPCError(e.error ?? e.message)) {
        e.preventDefault();
      }
    };
    const suppressNonFileDragRejection = (e: PromiseRejectionEvent) => {
      if (isWailsNonFileDragError(e.reason, hasRecentNativeFileDrag()) || isTransientWailsIPCError(e.reason)) {
        e.preventDefault();
      }
    };

    window.addEventListener("dragenter", markNativeFileDrag, true);
    window.addEventListener("dragover", markNativeFileDrag, true);
    window.addEventListener("drop", markNativeFileDrag, true);
    window.addEventListener("error", suppressNonFileDragError);
    window.addEventListener("unhandledrejection", suppressNonFileDragRejection);
    wailsDragSuppressionUninstall = () => {
      window.removeEventListener("dragenter", markNativeFileDrag, true);
      window.removeEventListener("dragover", markNativeFileDrag, true);
      window.removeEventListener("drop", markNativeFileDrag, true);
      window.removeEventListener("error", suppressNonFileDragError);
      window.removeEventListener("unhandledrejection", suppressNonFileDragRejection);
      lastNativeFileDragAt = 0;
    };
  }

  let disposed = false;
  return () => {
    if (disposed) return;
    disposed = true;
    wailsDragSuppressionRefs = Math.max(0, wailsDragSuppressionRefs - 1);
    if (wailsDragSuppressionRefs === 0 && wailsDragSuppressionUninstall) {
      wailsDragSuppressionUninstall();
      wailsDragSuppressionUninstall = null;
    }
  };
}

type NativeDropListener = {
  target?: () => HTMLElement | null;
  callback: (paths: string[]) => void;
};

const nativeDropListeners = new Set<NativeDropListener>();
let nativeDropUninstall: (() => void) | null = null;

function ensureNativeDropBridge(): boolean {
  const rt = typeof window !== "undefined" ? window.runtime : undefined;
  if (!rt?.OnFileDrop) return false;
  if (nativeDropUninstall) return true;

  // Wails' internal ResolveFilePaths throws when a non-file object (e.g. the
  // window icon) is dragged onto the webview. The error is uncaught and crashes
  // the app. Intercept it here so only real file drops reach the callback.
  const uninstallDragSuppression = installWailsNonFileDragErrorSuppression();

  rt.OnFileDrop((x, y, paths) => {
    if (!Array.isArray(paths) || paths.length === 0) return;
    const listeners = [...nativeDropListeners];
    const targeted = listeners.find((listener) => {
      const element = listener.target?.();
      if (!element?.isConnected) return false;
      const rect = element.getBoundingClientRect();
      return x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom;
    });
    if (targeted) {
      targeted.callback(paths);
      return;
    }
    listeners.filter((listener) => !listener.target).forEach((listener) => listener.callback(paths));
  }, true);
  nativeDropUninstall = () => {
    rt.OnFileDropOff?.();
    uninstallDragSuppression();
    nativeDropUninstall = null;
  };
  return true;
}

function subscribeNativeDrop(listener: NativeDropListener): () => void {
  nativeDropListeners.add(listener);
  if (!ensureNativeDropBridge()) {
    nativeDropListeners.delete(listener);
    return () => {};
  }
  return () => {
    nativeDropListeners.delete(listener);
    if (nativeDropListeners.size === 0) nativeDropUninstall?.();
  };
}

// Global fallback used by the composer when no more-specific drop target owns
// the native OS drop.
export function onFilesDropped(cb: (paths: string[]) => void): () => void {
  return subscribeNativeDrop({ callback: cb });
}

// Targeted native file drop. The target wins over global listeners, preventing
// one file from being attached to both the Work input and the composer.
export function onFilesDroppedIn(
  target: () => HTMLElement | null,
  cb: (paths: string[]) => void,
): () => void {
  return subscribeNativeDrop({ target, callback: cb });
}

// onReady subscribes to the agent:ready event fired when boot.Build completes.
// The frontend re-fetches Meta/Context/History when this lands.
export function onReady(cb: (tabId?: string) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("agent:ready", (tabId?: unknown) => cb(typeof tabId === "string" ? tabId : undefined));
  }
  // In dev mock, fire immediately since there's no real boot sequence.
  cb();
  return () => {};
}

export interface SessionActivatedEvent {
  reason?: string;
  tabId?: string;
  sessionPath?: string;
}

export function onSessionActivated(cb: (event: SessionActivatedEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("session:activated", (payload?: unknown) => cb(sessionActivatedEvent(payload)));
  }
  return () => {};
}

export function onCollaborationState(cb: (payload: unknown) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("collaboration:state", (payload?: unknown) => cb(payload));
  }
  return () => {};
}

export function onDecisionState(cb: (state: DecisionStateView) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("decision:state", (payload?: unknown) => cb(payload as DecisionStateView));
  }
  return () => {};
}

export function onCollaborationEvent(cb: (payload: unknown) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("collaboration:event", (payload?: unknown) => cb(payload));
  }
  return () => {};
}

function sessionActivatedEvent(payload: unknown): SessionActivatedEvent {
  if (!payload || typeof payload !== "object") return {};
  const raw = payload as Record<string, unknown>;
  return {
    reason: typeof raw.reason === "string" ? raw.reason : undefined,
    tabId: typeof raw.tabId === "string" ? raw.tabId : undefined,
    sessionPath: typeof raw.sessionPath === "string" ? raw.sessionPath : undefined,
  };
}

export function onProjectTreeChanged(cb: () => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("project-tree:changed", () => cb());
  }
  return () => {};
}

const emptyUnreadState = (): UnreadState => ({
  available: false,
  summary: { revision: 0, totalUnread: 0, highPriorityCount: 0, conversations: [] },
});

export function onUnreadState(cb: (state: UnreadState) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("unread:state", (payload?: unknown) => cb(payload as UnreadState));
  }
  return () => {};
}

// app proxies each call to the live binding (or the dev mock only when truly
// outside the shell), so a late-injected window.go is picked up transparently.
function bridgeBreadcrumb(method: string): string {
  if (method === "ReportCrash") return "";
  if (/^(Submit|SubmitDisplay|RunShell|Steer|Cancel|Approve|AnswerQuestion|ReplayPendingPrompts)/.test(method))
    return `turn ${method}`;
  if (/^(SetModel|SetEffort|SetTokenMode|SetDefaultModel|SetPlannerModel|SetSubagentModel|SetSubagentEffort|SetMaxSubagentDepth)/.test(method))
    return `model ${method}`;
  if (/^(SetDesktop|SetCloseBehavior|SetDisplayMode|SetStatusBar|SetExpandThinking|SetAutoPlan|SetDefaultToolApprovalMode|SetMemoryCompilerEnabled|SetReasoningLanguage)/.test(method))
    return `settings ${method}`;
  if (/^(SaveProvider|AddOfficialProviderAccess|RemoveProviderAccess|DeleteProvider|SetProviderKey|ClearProviderKey|FetchProviderModels|ConnectKey)/.test(method))
    return `provider ${method}`;
  if (/^(CheckUpdate|DownloadUpdate|InstallUpdate|ApplyUpdate|OpenDownloadPage)/.test(method)) return `update ${method}`;
  if (/^(AddMCPServer|UpdateMCPServer|RemoveMCPServer|ReconnectMCPServer|ClearMCPServerAuthentication|TrustMCPServerTool|TrustMCPServerTools|UntrustMCPServerTool|SetMCPServer)/.test(method))
    return `mcp ${method}`;
  if (/^(AddSkillPath|RemoveSkillPath|RefreshSkills|SetSkillEnabled|AcceptSkillSuggestion)/.test(method))
    return `skill ${method}`;
  if (/^(MinimiseMainWindow|ToggleMaximiseMainWindow|IsMainWindowMaximised|CloseMainWindow|DismissMainWindow)$/.test(method)) return `window ${method}`;
  if (/^(OpenProjectTab|OpenGlobalTab|OpenTopicSession|OpenLinkedSession|CreateBlankSession|EnsureBlankTab|ActivateTopic|ActivateLinkedSession|EnsureBlankSurface|SetActiveTab|CloseTab|ReorderTabs|CreateTopic|RenameTopic|DeleteTopic|TrashTopic|RenameProject|RemoveWorkspace|SwitchWorkspace|PickWorkspace)/.test(method))
    return `nav ${method}`;
  return "";
}

export const app: AppBindings = new Proxy({} as AppBindings, {
  get(_t, prop) {
    const target = realApp() ?? getMock();
    const v = (target as unknown as Record<string, unknown>)[String(prop)];
    if (typeof v !== "function") return v;
    return (...args: unknown[]) => {
      const method = String(prop);
      const crumb = bridgeBreadcrumb(method);
      if (crumb) addBreadcrumb("bridge", crumb);
      try {
        const result = (v as (...a: unknown[]) => unknown).apply(target, args);
        if (result && typeof (result as Promise<unknown>).then === "function") {
          return (result as Promise<unknown>).catch((err) => {
            if (crumb) addBreadcrumb("bridge.error", method);
            throw err;
          });
        }
        return result;
      } catch (err) {
        if (crumb) addBreadcrumb("bridge.error", method);
        throw err;
      }
    };
  },
});

// openExternal opens a URL in the system browser (so links in rendered markdown
// don't navigate the webview away from the app). Falls back to window.open in the
// browser dev mock.
export function openExternal(url: string): void {
  if (typeof window !== "undefined" && window.runtime?.BrowserOpenURL) {
    window.runtime.BrowserOpenURL(url);
  } else if (typeof window !== "undefined") {
    window.open(url, "_blank", "noopener");
  }
}

// --- browser dev mock --------------------------------------------------------

const listeners = new Set<(e: WireEvent) => void>();
let mockScopedTabId: string | undefined;

function mockSubscribe(cb: (e: WireEvent) => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

function emit(e: WireEvent) {
  const event = mockScopedTabId && !e.tabId ? { ...e, tabId: mockScopedTabId } : e;
  listeners.forEach((l) => l(event));
}

export function mockToolApprovalModeAfterModeChange(current: string | undefined, nextMode: Mode): ToolApprovalMode {
  if (modeHasAutoApproveTools(nextMode)) return "yolo";
  const currentMode = normalizeToolApprovalMode(current);
  return currentMode === "yolo" ? "ask" : currentMode;
}

async function withMockTabScope<T>(tabId: string, fn: () => Promise<T>): Promise<T> {
  const previous = mockScopedTabId;
  mockScopedTabId = tabId || previous;
  try {
    return await fn();
  } finally {
    mockScopedTabId = previous;
  }
}

// Updater progress has its own listener set so the browser dev mock can stream a
// fake download/install flow through onUpdaterProgress.
const updaterListeners = new Set<(p: UpdateProgress) => void>();

function emitUpdater(p: UpdateProgress) {
  updaterListeners.forEach((l) => l(p));
}

function delay(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

function baseName(path: string): string {
  return path.replace(/[/\\]+$/, "").split(/[/\\]/).filter(Boolean).pop() ?? path;
}

function browserPlatformOverride(): "darwin" | "windows" | "linux" | "" {
  if (typeof window === "undefined" || window.runtime) return "";
  const value = new URLSearchParams(window.location.search).get("platform");
  return value === "darwin" || value === "windows" || value === "linux" ? value : "";
}

function mockScenario(): "demo" | "fresh" | "running" | "guidance" {
  if (typeof window === "undefined") return "demo";
  const value = new URLSearchParams(window.location.search).get("mock")?.trim().toLowerCase();
  if (value === "fresh" || value === "empty" || value === "first-run") return "fresh";
  if (value === "guidance" || value === "guide" || value === "steer") return "guidance";
  if (value === "running" || value === "busy" || value === "streaming") return "running";
  return "demo";
}

function mockDesktopZoomFactor(): number {
  if (typeof window === "undefined") return 1;
  const value = Number(new URLSearchParams(window.location.search).get("zoom"));
  return Number.isFinite(value) && value >= 0.5 && value <= 2 ? value : 1;
}

function mockWidgetSkin(): string {
  if (typeof window === "undefined") return "classic";
  const value = new URLSearchParams(window.location.search).get("skin")?.trim().toLowerCase() ?? "";
  return ["classic", "bp", "instant", "pet", "recorder"].includes(value) ? value : "classic";
}

function mockWidgetStyle(): "icons" {
  return "icons";
}

// mockDecisionSkillExportFn lets tests drive the browser mock's
// ExportDecisionSkills outcome (exported/canceled/failure) without touching
// Wails. Null (the default) uses the canned success result below.
let mockDecisionSkillExportFn: (() => Promise<DecisionSkillExportResult>) | null = null;

/** Test-only override for the decision-skill export browser mock. */
export function setMockDecisionSkillExport(fn: (() => Promise<DecisionSkillExportResult>) | null): void {
  mockDecisionSkillExportFn = fn;
}

function makeMockApp(): AppBindings {
  const scenario = mockScenario();
  const freshMock = scenario === "fresh";
  const guidanceMock = scenario === "guidance";
  const runningMock = scenario === "running" || guidanceMock;
  const widgetScenario = typeof window === "undefined"
    ? ""
    : new URLSearchParams(window.location.search).get("mock")?.trim().toLowerCase() ?? "";
  const desktopZoomFactor = mockDesktopZoomFactor();
  const desktopWidgetSkin = mockWidgetSkin();
	let desktopWidgetStyle = mockWidgetStyle();
	let desktopWorkspaceSlots = 4;
	let desktopRoomPins: string[] = [];
	let desktopRoomIcons: Record<string, string> = {};
  let widgetMode = widgetScenario.startsWith("widget-");
  let widgetRevision = 1;
	let widgetConversationStarted = false;
	let widgetChoiceAnswered = false;
  let cancelled = false;
  let pendingAskPreview = false;
  let pendingApprovalPreview = false;
  const globalWorkspaceRoot = "~/Library/Application Support/WorkGround2/global-workspace";
  let cwd = freshMock ? globalWorkspaceRoot : "~/projects/joyquant-db"; // mutable so PickWorkspace is visible in dev
  let workspaces = freshMock ? [] : ["~/projects/joyquant-db", "~/projects/joyquant-sys", "~/projects/WorkGround2", "~/projects/blade"];
  let mockEffort = "auto";
  const day = 86_400_000;
  const t0 = Date.now();

  const mockWidgetSnapshot = (): WidgetSnapshot => {
    const base = {
      mode: widgetMode,
      remainingCount: 3,
      runningCount: 4,
      waitingCount: 1,
      completedCount: 2,
      failedCount: 0,
      backgroundCount: 1,
      isIdle: false,
      info: {
        totalTokens: 12_840_000,
        tokenPartial: false,
        idleSince: t0 - 2_945_000,
        system: { available: true, network: "online" as const, cpu: 23, memory: 61, sampledAt: t0 },
        models: [
          { provider: "deepseek", model: "deepseek-chat", brand: "deepseek" },
          { provider: "anthropic", model: "claude-sonnet-4", brand: "anthropic" },
          { provider: "google", model: "gemini-2.5-pro", brand: "gemini" },
        ],
      },
      version: `mock-${widgetScenario}-${widgetRevision}`,
    };
		if (widgetConversationStarted || (widgetScenario === "widget-choice3" && widgetChoiceAnswered)) {
			return { ...base, remainingCount: 0, runningCount: 5, waitingCount: 0, completedCount: 0, current: undefined };
		}
    if (widgetScenario === "widget-idle") {
      return { ...base, remainingCount: 0, runningCount: 0, waitingCount: 0, completedCount: 0, backgroundCount: 0, current: undefined, isIdle: true };
    }
    if (widgetScenario === "widget-running") {
      return { ...base, remainingCount: 0, waitingCount: 0, completedCount: 0, current: undefined, runningCount: 2, isIdle: false };
    }
    if (widgetScenario === "widget-result") {
      return {
        ...base,
        remainingCount: 2,
        current: {
          id: "mock-result",
          revision: String(widgetRevision),
          tabId: "tab-wg2",
          projectName: "WorkGround2",
          taskName: "桌面小组件模式",
          kind: "result",
          stateLabel: "任务完成",
          message: "小组件模式已实现，构建与测试均已通过。",
          options: [],
        },
      };
    }
	if (widgetScenario === "widget-error") {
	  return {
		...base,
		failedCount: 1,
		current: {
		  id: "mock-error",
		  revision: String(widgetRevision),
		  tabId: "tab-wg2",
		  projectName: "WorkGround2",
		  taskName: "桌面构建",
		  kind: "error",
		  stateLabel: "需要处理",
		  message: "依赖暂时不可用，任务已保留现场，可以安全重试。",
		  options: [],
		},
	  };
	}
    if (widgetScenario === "widget-reply") {
      return {
        ...base,
        remainingCount: 1,
        current: {
          id: "mock-reply",
          revision: String(widgetRevision),
          tabId: "tab-wg2",
          projectName: "WorkGround2",
          taskName: "发布说明",
          kind: "reply",
          stateLabel: "等待回复",
          message: "这次更新要附带迁移说明吗？",
          interactionId: "ask-reply",
          questionId: "question-reply",
          options: [],
        },
      };
    }
    if (widgetScenario === "widget-choice3") {
      return {
        ...base,
        current: {
          id: "mock-choice3",
          revision: String(widgetRevision),
          tabId: "tab-wg2",
          projectName: "WorkGround2",
          taskName: "多语言设置",
          kind: "choice",
          stateLabel: "选择回复",
          message: "请选择文档的目标语言：",
          interactionId: "ask-choice3",
          questionId: "question-choice3",
          options: [
            { label: "中文", description: "简体中文文档", value: "中文" },
            { label: "英文", description: "English documentation", value: "英文" },
            { label: "日语", description: "日本語ドキュメント", value: "日语" },
          ],
        },
      };
    }
    return {
      ...base,
      current: {
        id: "mock-choice",
        revision: String(widgetRevision),
        tabId: "tab-wg2",
        projectName: "WorkGround2",
        taskName: "插件文档",
        kind: "choice",
        stateLabel: "选择回复",
        message: "英文版也一起更新？",
        interactionId: "ask-choice",
        questionId: "question-choice",
        options: [
          { label: "一起更新", description: "中英文保持同步", value: "一起更新" },
          { label: "仅更新中文", description: "英文版稍后处理", value: "仅更新中文" },
        ],
      },
    };
  };
	const mockDesktopIconSnapshot = (): DesktopIconSnapshot => ({
		style: desktopWidgetStyle, revision: `icons-${widgetRevision}`, hoverStatusDelayMs: settings?.hoverStatusDelayMs ?? 1200,
		unreadRevision: widgetRevision,
		delegations: [],
		items: [
			{ id: "conversation:room-design", kind: "room", sourceId: "room-design", title: "产品 Room", status: "unread", unreadCount: 2, position: { row: "top", zone: "conversation", order: 0 }, revision: `room-${widgetRevision}`, notifications: [{ id: "room-msg", revision: "2", kind: "message", priority: 9, title: "小组件讨论", body: "收到一条新消息", createdAt: t0, conversation: "room-design", readSequence: 2, attention: "mention_agent", options: [] }] },
			{ id: "task:tab-wg2", kind: "task", sourceId: "tab-wg2", title: "桌面图标模式", subtitle: "WorkGround2", status: widgetScenario === "widget-running" ? "running" : "thinking", unreadCount: 0, runtimeStatus: { phase: widgetScenario === "widget-running" ? "Running" : "Thinking", summary: widgetScenario === "widget-running" ? "read_file 执行中" : "正在核对真实状态投影", elapsedMs: 84_000, updatedAt: t0 }, position: { row: "bottom", zone: "running", order: 0 }, revision: `task-${widgetRevision}`, notifications: [] },
			{ id: "external:run-dsh-demo", kind: "external", sourceId: "run-dsh-demo", title: "DSH · WorkGround2", subtitle: "WorkGround2", status: "running", unreadCount: 0, runtimeStatus: { phase: "tool", summary: "DSH 正在执行", elapsedMs: 18_000, updatedAt: t0 }, position: { row: "bottom", zone: "running", order: 1 }, revision: `dsh-${widgetRevision}`, notifications: [], actions: ["cancel"] },
			...(desktopWorkspaceSlots > 0 ? [{ id: "workspace:~/projects/WorkGround2", kind: "workspace", sourceId: "~/projects/WorkGround2", title: "WorkGround2", status: "idle", unreadCount: 0, position: { row: "bottom", zone: "workspace", order: 0 }, revision: "workspace", notifications: [] } satisfies DesktopIconItem] : []),
			...(["new", "assistant", "delegate", "search"] as const)
				.filter((id) => id !== "assistant" || settings.widgetShowAssistant)
				.map((id, order) => ({ id: `fixed:${id}`, kind: "fixed" as const, sourceId: id, title: { new: "新建", assistant: "助手", delegate: "委托", search: "搜索" }[id], icon: id === "assistant" ? "bot" : id, status: "idle" as const, unreadCount: 0, position: { row: "bottom" as const, zone: "fixed" as const, order }, revision: `fixed-${id}`, notifications: [] })),
			{ id: "fixed:dsh", kind: "fixed", sourceId: "dsh", title: "DSH", subtitle: "0.1.0-rc.8 · 快速启动", icon: "terminal", status: "idle", unreadCount: 0, position: { row: "bottom", zone: "fixed", order: 3 }, revision: "fixed-dsh", notifications: [], actions: ["launch"] },
		],
	});
	const mockExternalRunSnapshot = (): ExternalRunSnapshot => ({
		workspace: "~/projects/WorkGround2",
		revision: `external-${widgetRevision}`,
		dsh: { id: "dsh-rc8", ready: true, root: "D:/Work/dsh", version: "0.1.0-rc.8", capabilities: { cancel: true, open: false, retry: false, resume: false, approve: false, send: false } },
		runs: [{ id: "run-dsh-demo", source: "dsh", ownership: "managed", workspace: "~/projects/WorkGround2", title: "DSH · WorkGround2", state: "running", activity: "tool", activityLabel: "DSH 正在执行", capabilities: { cancel: true, open: false, retry: false, resume: false, approve: false, send: false }, revision: widgetRevision, createdAt: new Date(t0 - 18_000).toISOString(), updatedAt: new Date(t0).toISOString() }],
	});
  // Mutable so MCP add/remove/retry are observable in browser dev.
  let capServers: ServerView[] = [
    {
      name: "github",
      transport: "stdio",
      status: "connected",
      configured: true,
      autoStart: true,
      tier: "background",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-github"],
      tools: 4,
      prompts: 2,
      resources: 0,
      trustedReadOnlyTools: ["pull_request_read"],
      toolList: [
        { name: "issue_read", description: "Read GitHub issue details and comments.", readOnlyHint: true },
        { name: "pull_request_read", description: "Read pull request metadata, files, and review threads.", readOnlyHint: true },
        { name: "search_issues", description: "Search issues and pull requests.", readOnlyHint: true },
        { name: "issue_write", description: "Create or update GitHub issues." },
      ],
    },
    {
      name: "linear",
      transport: "http",
      status: "initializing",
      configured: true,
      autoStart: true,
      tier: "background",
      url: "https://mcp.linear.app/mcp",
      authStatus: "possible",
      authUrl: "https://mcp.linear.app/mcp",
      tools: 8,
      prompts: 0,
      resources: 0,
      toolList: [
        { name: "list_issues", description: "List and filter Linear issues." },
        { name: "get_issue", description: "Fetch a Linear issue by id or key." },
        { name: "create_issue", description: "Create a Linear issue." },
        { name: "update_issue", description: "Update status, assignee, priority, or labels." },
        { name: "list_projects", description: "List Linear projects." },
        { name: "get_project", description: "Fetch project details." },
        { name: "list_teams", description: "List Linear teams." },
        { name: "search", description: "Search Linear workspace objects." },
      ],
    },
    { name: "figma", transport: "http", status: "failed", configured: true, autoStart: true, tier: "background", url: "https://mcp.figma.com/mcp", authStatus: "required", authUrl: "https://mcp.figma.com/mcp", tools: 0, prompts: 0, resources: 0, error: "connect: 401 unauthorized" },
  ];
  const capSkills: SkillView[] = [
    { name: "explore", description: "Investigate the codebase in an isolated subagent", scope: "builtin", runAs: "subagent", enabled: true },
    { name: "review", description: "Review the staged diff", scope: "project", runAs: "inline", enabled: false },
    { name: "init", description: "Scaffold a WorkGround2.md for this repo", scope: "builtin", runAs: "inline", enabled: true },
  ];
  let capSkillRoots: SkillRootView[] = [
    { dir: "~/projects/WorkGround2/.WorkGround2/skills", scope: "project", priority: 1, status: "missing", configured: false, removable: true, skills: 0 },
    {
      dir: "~/my-skills",
      scope: "custom",
      priority: 5,
      status: "ok",
      configured: true,
      removable: true,
      skills: 1,
      skillItems: [{ name: "review", description: "Review the staged diff", scope: "custom", runAs: "inline" }],
    },
    {
      dir: "~/.WorkGround2/skills",
      scope: "global",
      priority: 6,
      status: "ok",
      configured: false,
      removable: true,
      skills: 2,
      skillItems: [
        { name: "explore", description: "Investigate the codebase in an isolated subagent", scope: "global", runAs: "subagent" },
        { name: "init", description: "Scaffold a WorkGround2.md for this repo", scope: "global", runAs: "inline" },
      ],
    },
  ];
  let capPlugins: PluginView[] = [];
  let skillShareProfiles: SkillShareProfileView[] = [];
  let flowSkillShareProfiles: SkillShareProfileView[] = [];
  let drawAddonProviders: DrawAddonProviderView[] = [];
  const mockSkillShareSecretKey = (id: string) => `SKILLSHARE_${id.trim().toUpperCase().replace(/[^A-Z0-9]+/g, "_").replace(/^_+|_+$/g, "")}_GIT_PASSWORD`;
  const mockFlowSkillShareSecretKey = (id: string) => `FLOWSKILLSHARE_${id.trim().toUpperCase().replace(/[^A-Z0-9]+/g, "_").replace(/^_+|_+$/g, "")}_GIT_PASSWORD`;
  const mockDrawAddonSecretKey = (id: string) => `DRAWADDON_${id.trim().toUpperCase().replace(/[^A-Z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "PROVIDER"}_API_KEY`;
  const mockSkillSharePanelSchema = (title: string, namespace: string, source: string) => ({
    version: 1,
    title,
    storage: { namespace, source },
    sections: [{
      id: "sources",
      title,
      adapter: source,
      form: {
        actions: [
          { id: "recover", labelKey: "caps.skillShareRecover" },
          { id: "reset", labelKey: "caps.skillShareNew" },
          { id: "save", labelKey: "caps.skillShareSave", variant: "primary" },
        ],
        fields: [
          { key: "id", labelKey: "caps.skillShareId" },
          { key: "displayName", labelKey: "caps.skillShareDisplayName" },
          { key: "gitUrl", labelKey: "caps.skillShareGitUrl", span: 2 },
          { key: "branch", labelKey: "caps.skillShareBranch" },
          { key: "path", labelKey: "caps.skillSharePath" },
          { key: "pluginName", labelKey: "caps.skillSharePluginName" },
          { key: "username", labelKey: "caps.skillShareUsername" },
          { key: "secretRef", labelKey: "caps.skillShareSecretRef" },
          { key: "password", labelKey: "caps.skillSharePassword", type: "password", autoComplete: "new-password" },
          { key: "intervalSeconds", labelKey: "caps.skillShareInterval", type: "number", min: 0 },
          { key: "enabled", labelKey: "caps.skillShareEnabled", type: "checkbox" },
          { key: "auto", labelKey: "caps.skillShareAuto", type: "checkbox" },
          { key: "checkOnLogin", labelKey: "caps.skillShareCheckOnLogin", type: "checkbox" },
        ],
      },
      list: {
        titleKey: "caps.skillShareProfiles",
        emptyKey: "caps.skillShareNoProfiles",
        summaryKey: "caps.skillShareProfileSummary",
        titleField: "displayName",
        badgeFields: [{ path: "id" }, { path: "version" }],
        detailFields: [
          { path: "gitUrl", labelKey: "caps.skillShareGitUrl", span: 2 },
          { path: "branch", labelKey: "caps.skillShareBranch" },
          { path: "path", labelKey: "caps.skillSharePath" },
          { path: "pluginName", labelKey: "caps.skillSharePluginName" },
          { path: "secretRef", labelKey: "caps.skillShareSecretRef" },
          { path: "state.currentRevision", labelKey: "caps.skillShareRevision", span: 2 },
          { path: "state.lastCheckedAt", labelKey: "caps.skillShareLastChecked" },
          { path: "state.lastUpdatedAt", labelKey: "caps.skillShareLastUpdated" },
        ],
        actions: [
          { id: "edit", labelKey: "caps.skillShareEdit" },
          { id: "sync", labelKey: "caps.skillShareSync" },
          { id: "force", labelKey: "caps.skillShareForce" },
          { id: "delete", labelKey: "caps.skillShareDelete", confirmLabelKey: "caps.skillShareConfirmDelete", danger: true },
          { id: "delete-secret", labelKey: "caps.skillShareDeleteSecret", confirmLabelKey: "caps.skillShareConfirmDeleteSecret", danger: true },
        ],
      },
    }],
  });
  const mockDrawAddonPanelSchema = (title: string, namespace: string, source: string) => ({
    version: 1,
    title,
    storage: { namespace, source },
    sections: [{
      id: "providers",
      title,
      adapter: source,
      form: {
        actions: [
          { id: "reset", labelKey: "caps.drawAddonNew" },
          { id: "generate", labelKey: "caps.drawAddonGenerate" },
          { id: "save", labelKey: "caps.drawAddonSave", variant: "primary" },
        ],
        fields: [
          { key: "id", labelKey: "caps.drawAddonId" },
          { key: "displayName", labelKey: "caps.drawAddonDisplayName" },
          { key: "mode", labelKey: "caps.drawAddonMode", type: "select", options: [{ value: "api", labelKey: "caps.drawAddonModeApi" }, { value: "cli", labelKey: "caps.drawAddonModeCli" }] },
          { key: "apiKeyRef", labelKey: "caps.drawAddonApiKeyRef" },
          { key: "apiKey", labelKey: "caps.drawAddonApiKey", type: "password", autoComplete: "new-password" },
          { key: "baseUrl", labelKey: "caps.drawAddonBaseUrl", span: 2, visibleWhen: { key: "mode", equals: "api" } },
          { key: "model", labelKey: "caps.drawAddonModel", visibleWhen: { key: "mode", equals: "api" } },
          { key: "cliCommand", labelKey: "caps.drawAddonCliCommand", span: 2, visibleWhen: { key: "mode", equals: "cli" } },
          { key: "outputDir", labelKey: "caps.drawAddonOutputDir", visibleWhen: { key: "mode", equals: "cli" } },
          { key: "cliArgs", labelKey: "caps.drawAddonCliArgs", type: "textarea", span: 2, visibleWhen: { key: "mode", equals: "cli" } },
          { key: "prompt", labelKey: "caps.drawAddonPrompt", type: "textarea", span: 2 },
          { key: "enabled", labelKey: "caps.drawAddonEnabled", type: "checkbox" },
        ],
      },
      list: {
        titleKey: "caps.drawAddonProviders",
        emptyKey: "caps.drawAddonNoProviders",
        summaryKey: "caps.drawAddonProviderSummary",
        titleField: "displayName",
        badgeFields: [{ path: "mode" }, { path: "id" }],
        detailFields: [
          { path: "mode", labelKey: "caps.drawAddonMode" },
          { path: "authStatus", labelKey: "caps.drawAddonAuth" },
          { path: "model", labelKey: "caps.drawAddonModel" },
          { path: "baseUrl", labelKey: "caps.drawAddonBaseUrl", span: 2 },
          { path: "cliCommand", labelKey: "caps.drawAddonCliCommand", span: 2 },
          { path: "outputDir", labelKey: "caps.drawAddonOutputDir", span: 2 },
          { path: "apiKeyRef", labelKey: "caps.drawAddonApiKeyRef" },
          { path: "state.lastOutputPath", labelKey: "caps.drawAddonLastOutput", span: 2 },
          { path: "state.lastTaskId", labelKey: "caps.drawAddonLastTask", span: 2 },
        ],
        actions: [
          { id: "edit", labelKey: "caps.drawAddonEdit" },
          { id: "delete", labelKey: "caps.drawAddonDelete", confirmLabelKey: "caps.drawAddonConfirmDelete", danger: true },
        ],
      },
    }],
  });
  const cloneDrawAddonProvider = (provider: DrawAddonProviderView): DrawAddonProviderView => ({
    ...provider,
    cliArgs: [...(provider.cliArgs ?? [])],
    state: { ...provider.state },
  });
  const normalizeMockDrawAddonProvider = (input: DrawAddonProviderInput, secretValue = ""): DrawAddonProviderView => {
    const id = input.id.trim();
    const prev = drawAddonProviders.find((provider) => provider.id === id);
    const mode = input.mode === "cli" ? "cli" : "api";
    const apiKeyRef = (input.apiKeyRef || "").trim() || (secretValue.trim() ? mockDrawAddonSecretKey(id) : "");
    const ready = input.enabled && (mode === "cli" ? Boolean(input.cliCommand?.trim()) : Boolean(input.baseUrl?.trim() && input.model?.trim()));
    return {
      id,
      enabled: Boolean(input.enabled),
      displayName: input.displayName?.trim() || undefined,
      mode,
      baseUrl: input.baseUrl?.trim() || undefined,
      model: input.model?.trim() || undefined,
      apiKeyRef,
      authStatus: apiKeyRef ? "set" : "none",
      cliCommand: input.cliCommand?.trim() || undefined,
      cliArgs: [...(input.cliArgs ?? [])],
      outputDir: input.outputDir?.trim() || undefined,
      state: {
        status: input.enabled ? (ready ? "ready" : "unconfigured") : "disabled",
        lastTaskId: prev?.state.lastTaskId,
        lastStartedAt: prev?.state.lastStartedAt,
        lastFinishedAt: prev?.state.lastFinishedAt,
        lastOutputPath: prev?.state.lastOutputPath,
        lastError: "",
      },
    };
  };
  const normalizeMockSkillShareProfile = (input: SkillShareProfileInput, secretValue = ""): SkillShareProfileView => {
    const id = input.id.trim();
    const prev = skillShareProfiles.find((profile) => profile.id === id);
    const secretRef = (input.secretRef || "").trim() || (secretValue.trim() ? mockSkillShareSecretKey(id) : "");
    const rawInterval = input.update?.intervalSeconds;
    const update = {
      auto: input.update?.auto ?? true,
      checkOnLogin: input.update?.checkOnLogin ?? true,
      intervalSeconds: typeof rawInterval === "number" && Number.isFinite(rawInterval) ? rawInterval : 3600,
    };
    return {
      id,
      enabled: Boolean(input.enabled),
      displayName: input.displayName?.trim() || undefined,
      gitUrl: input.gitUrl.trim(),
      branch: input.branch?.trim() || "main",
      path: input.path?.trim() || ".",
      username: input.username?.trim() || undefined,
      secretRef,
      authStatus: secretRef ? "configured" : input.username?.trim() ? "username_only" : "anonymous",
      pluginName: input.pluginName?.trim() || prev?.pluginName,
      update,
      state: {
        status: input.enabled ? (prev?.state.currentRevision ? "ready" : "unconfigured") : "disabled",
        currentRevision: prev?.state.currentRevision,
        lastCheckedAt: prev?.state.lastCheckedAt,
        lastUpdatedAt: prev?.state.lastUpdatedAt,
        lastError: "",
      },
      manifestKind: prev?.manifestKind,
      version: prev?.version,
      skills: prev?.skills,
      hooks: prev?.hooks,
      mcpServers: prev?.mcpServers,
    };
  };
  const normalizeMockFlowSkillShareProfile = (input: SkillShareProfileInput, secretValue = ""): SkillShareProfileView => {
    const id = input.id.trim();
    const prev = flowSkillShareProfiles.find((profile) => profile.id === id);
    const secretRef = (input.secretRef || "").trim() || (secretValue.trim() ? mockFlowSkillShareSecretKey(id) : "");
    const rawInterval = input.update?.intervalSeconds;
    const update = {
      auto: input.update?.auto ?? true,
      checkOnLogin: input.update?.checkOnLogin ?? true,
      intervalSeconds: typeof rawInterval === "number" && Number.isFinite(rawInterval) ? rawInterval : 3600,
    };
    return {
      id,
      enabled: Boolean(input.enabled),
      displayName: input.displayName?.trim() || undefined,
      gitUrl: input.gitUrl.trim(),
      branch: input.branch?.trim() || "main",
      path: input.path?.trim() || ".",
      username: input.username?.trim() || undefined,
      secretRef,
      authStatus: secretRef ? "configured" : input.username?.trim() ? "username_only" : "anonymous",
      pluginName: input.pluginName?.trim() || prev?.pluginName,
      update,
      state: {
        status: input.enabled ? (prev?.state.currentRevision ? "ready" : "unconfigured") : "disabled",
        currentRevision: prev?.state.currentRevision,
        lastCheckedAt: prev?.state.lastCheckedAt,
        lastUpdatedAt: prev?.state.lastUpdatedAt,
        lastError: "",
      },
      manifestKind: prev?.manifestKind,
      version: prev?.version,
      skills: prev?.skills,
      hooks: prev?.hooks,
      mcpServers: prev?.mcpServers,
    };
  };
  const mockSwitchWorkspace = async (path: string) => {
    cwd = path || "~";
    workspaces = [cwd, ...workspaces.filter((p) => p !== cwd)].slice(0, 12);
    if (!mockProjectTree.some((node) => node.kind === "project" && node.root === cwd)) {
      mockProjectTree.unshift({
        key: `project_${cwd}`,
        kind: "project",
        label: baseName(cwd),
        root: cwd,
        children: [],
      });
    }
    return cwd;
  };
  // Mutable so delete/rename are observable in browser dev.
  const sessions: SessionMeta[] = [
    { path: "/mock/sessions/a.jsonl", preview: "fix the login bug in auth.go", turns: 12, createdAt: t0 - 2 * day, lastActivityAt: t0 - 3_600_000, modTime: t0 - 3_600_000, current: true, open: true },
    { path: "/mock/sessions/b.jsonl", preview: "refactor the payment module", turns: 5, createdAt: t0 - 3 * day, lastActivityAt: t0 - 6 * 3_600_000, modTime: t0 - 6 * 3_600_000, current: false, open: true },
    { path: "/mock/sessions/c.jsonl", preview: "write the README and badges", turns: 8, createdAt: t0 - 4 * day, lastActivityAt: t0 - day - 3_600_000, modTime: t0 - day - 3_600_000, current: false, open: false },
    { path: "/mock/sessions/d.jsonl", preview: "explain the plugin host design", turns: 3, createdAt: t0 - 5 * day, lastActivityAt: t0 - 4 * day, modTime: t0 - 4 * day, current: false, open: false },
  ];
  const trashedSessions: SessionMeta[] = [
    {
      path: "/mock/sessions/.trash/trash-dev-standard.jsonl",
      title: t("mock.trashDevStandardTitle"),
      preview: t("mock.trashDevStandardPreview"),
      turns: 4,
      createdAt: t0 - 8 * day,
      lastActivityAt: t0 - 7 * day,
      modTime: t0 - 7 * day,
      deletedAt: t0 - 20 * 60_000,
      current: false,
      open: false,
      scope: "project",
      workspaceRoot: "~/projects/joyquant-db",
      topicId: "topic_dev_standard",
      topicTitle: t("mock.trashDevStandardTitle"),
    },
    {
      path: "/mock/sessions/.trash/trash-p3a-review.jsonl",
      title: t("mock.trashP3aTitle"),
      preview: t("mock.trashP3aPreview"),
      turns: 7,
      createdAt: t0 - 6 * day,
      lastActivityAt: t0 - 5 * day,
      modTime: t0 - 5 * day,
      deletedAt: t0 - 2 * 3_600_000,
      current: false,
      open: false,
      scope: "project",
      workspaceRoot: "~/projects/joyquant-sys",
      topicId: "topic_p3a_pd",
      topicTitle: t("mock.trashP3aTitle"),
    },
    {
      path: "/mock/sessions/.trash/trash-global-product.jsonl",
      title: t("mock.trashGlobalProductTitle"),
      preview: t("mock.trashGlobalProductPreview"),
      turns: 2,
      createdAt: t0 - 4 * day,
      lastActivityAt: t0 - 3 * day,
      modTime: t0 - 3 * day,
      deletedAt: t0 - day,
      current: false,
      open: false,
      scope: "global",
      topicId: "topic_product",
      topicTitle: t("mock.trashGlobalProductTitle"),
    },
  ];
  if (freshMock) {
    sessions.splice(0);
    trashedSessions.splice(0);
  }
  // Mutable settings so the Settings panel's edits are observable in browser dev.
  const settings: SettingsView = {
    defaultModel: "deepseek",
    plannerModel: "",
    subagentModel: "",
    subagentEffort: "",
    autoPlan: "off",
    providers: [
      { name: "deepseek", builtIn: true, added: false, kind: "openai", baseUrl: "https://api.deepseek.com", modelsUrl: "", models: ["deepseek-v4-flash"], visionModels: [], visionModelsConfigured: false, default: "deepseek-v4-flash", apiKeyEnv: "DEEPSEEK_API_KEY", keySet: true, balanceUrl: "https://api.deepseek.com/user/balance", contextWindow: 1_000_000, reasoningProtocol: "", supportedEfforts: [], defaultEffort: "" },
    ],
    officialProviders: [
      { name: "deepseek", builtIn: true, added: false, kind: "openai", baseUrl: "https://api.deepseek.com", modelsUrl: "", models: ["deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"], visionModels: ["deepseek-v4-flash-vision-exp"], visionModelsConfigured: true, default: "deepseek-v4-flash", apiKeyEnv: "DEEPSEEK_API_KEY", keySet: true, balanceUrl: "https://api.deepseek.com/user/balance", contextWindow: 1_000_000, reasoningProtocol: "", supportedEfforts: [], defaultEffort: "" },
      { name: "openai", builtIn: true, added: false, kind: "openai", baseUrl: "https://api.openai.com/v1", modelsUrl: "", models: ["gpt-4o", "gpt-4o-mini", "gpt-4.1", "o4-mini", "o3-mini", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"], visionModels: ["gpt-4o", "gpt-4o-mini", "gpt-4.1", "o4-mini", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"], visionModelsConfigured: true, default: "gpt-4o", apiKeyEnv: "OPENAI_API_KEY", keySet: false, balanceUrl: "", contextWindow: 1_050_000, reasoningProtocol: "", supportedEfforts: [], defaultEffort: "" },
    ],
    permissions: { mode: "ask", allow: ["ls", "read_file"], ask: [], deny: ["Bash(rm:*)"], browser: { allowPasswordInput: true, allowFileUpload: true } },
    browserLaunch: { incognito: false },
    sandbox: { bash: "enforce", network: true, workspaceRoot: "", allowWrite: [], shell: "auto" },
    network: {
      proxyMode: "auto",
      proxyUrl: "",
      noProxy: "",
      proxy: { type: "socks5", server: "127.0.0.1", port: 7890, username: "", password: "" },
    },
    collaboration: { preferLAN: true, connectTimeoutSeconds: 10, routeStableSeconds: 60, relays: [] },
    agent: { temperature: 0.2, maxSteps: 0, plannerMaxSteps: 0, maxSubagentDepth: 2, systemPrompt: "You are WorkGround2, a coding agent.", coldResumePrune: true, reasoningLanguage: "auto" },
    bot: {
      enabled: !freshMock,
      model: "",
      toolApprovalMode: "ask",
      maxSteps: 25,
      debounceMs: 1500,
      allowlist: {
        enabled: true,
        allowAll: false,
        qqUsers: [],
        feishuUsers: freshMock ? [] : ["ou_mock_user_001"],
        weixinUsers: freshMock ? [] : ["wxid_mock_user_001"],
        qqGroups: [],
        feishuGroups: [],
        weixinGroups: [],
      },
      qq: { enabled: false, appId: "", appSecretEnv: "QQ_BOT_APP_SECRET", secretSet: false, sandbox: false },
      feishu: {
        enabled: false,
        domain: "feishu",
        appId: "",
        appSecretEnv: "FEISHU_BOT_APP_SECRET",
        secretSet: false,
        verificationToken: "",
        mode: "webhook",
        webhookPort: 8080,
        requireMention: true,
      },
      weixin: {
        enabled: false,
        accountId: "default",
        tokenEnv: "WEIXIN_BOT_TOKEN",
        tokenSet: false,
        apiBase: "https://ilinkai.weixin.qq.com",
      },
      connections: freshMock ? [] : [
        {
          id: "mock-lark-kun",
          provider: "feishu",
          domain: "lark",
          label: "kun",
          enabled: true,
          status: "connected",
          model: "",
          toolApprovalMode: "",
          workspaceRoot: "",
          access: emptyBotAccess(),
          credential: {
            appId: "cli_mock_lark",
            appSecretEnv: "FEISHU_BOT_APP_SECRET",
            accountId: "",
            tokenEnv: "",
            secretSet: true,
          },
          sessionMappings: [
            {
              remoteId: "ou_mock_user_001",
              sessionId: "topic:topic_product",
              sessionSource: "",
              chatType: "",
              userId: "",
              threadId: "",
              scope: "global",
              workspaceRoot: "",
              updatedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
            },
          ],
          endpoints: [],
          lastError: "",
          createdAt: new Date(Date.now() - 86_400_000).toISOString(),
          updatedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
        },
        {
          id: "mock-weixin-kun",
          provider: "weixin",
          domain: "weixin",
          label: "kun",
          enabled: true,
          status: "connected",
          model: "",
          toolApprovalMode: "",
          workspaceRoot: "",
          access: emptyBotAccess(),
          credential: {
            appId: "",
            appSecretEnv: "",
            accountId: "default",
            tokenEnv: "WEIXIN_BOT_TOKEN",
            secretSet: true,
          },
          sessionMappings: [
            {
              remoteId: "wxid_mock_user_001",
              sessionId: "topic:topic_ai",
              sessionSource: "",
              chatType: "",
              userId: "",
              threadId: "",
              scope: "global",
              workspaceRoot: "",
              updatedAt: new Date(Date.now() - 12 * 60_000).toISOString(),
            },
          ],
          endpoints: [],
          lastError: "",
          createdAt: new Date(Date.now() - 86_400_000).toISOString(),
          updatedAt: new Date(Date.now() - 12 * 60_000).toISOString(),
        },
      ],
    },
    desktopLanguage: "",
    desktopLayoutStyle: "workbench",
    desktopTheme: "auto",
    desktopThemeStyle: "iris",
    closeBehavior: "background",
    displayMode: "compact",
    composerSubmitKey: "enter",
    statusBarStyle: "text",
    statusBarItems: [...DEFAULT_STATUS_BAR_ITEMS],
    defaultToolApprovalMode: "ask",
    checkUpdates: true,
    telemetry: true,
    metrics: true,
    widgetEnabled: true,
    widgetAlwaysOnTop: true,
    widgetSkin: "classic",
		widgetStyle: "icons",
    widgetShowDelegation: false,
    widgetShowExternalTools: false,
    widgetShowAssistant: true,
		hoverStatusDelayMs: 1200,
    ownerDecisionEnabled: false, // master kill switch for the 主人决策 feature (default off)
    memoryCompilerEnabled: true,
    configPath: "~/projects/WorkGround2/WorkGround2.toml",
    providerKinds: ["cli", "openai"],
    autoApproveTools: false,
    bypass: false,
  };
  settings.widgetSkin = desktopWidgetSkin;
	settings.widgetStyle = desktopWidgetStyle;
  let sessionBackgroundSettings: SessionBackgroundSettingsView = {
		mode: "pattern",
    enabled: false,
    maskEnabled: true,
    randomOnOpen: true,
    rotateSeconds: 0,
    imageCount: 0,
    sources: [],
  };
  const mockLocalCLIOptions: LocalCLIOptionView[] = [
    {
      id: "codex",
      name: "Codex CLI",
      description: "Runs Codex CLI in exec mode and sends the model request on stdin.",
      command: "codex",
      args: ["exec", "--ignore-user-config", "--skip-git-repo-check", "--sandbox", "read-only", "--model", "gpt-5.6-sol"],
      protocol: "jsonl",
      model: "gpt-5.6-sol",
      models: ["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.6"],
      capabilities: ["web_search"],
      timeoutSeconds: 120,
      installed: true,
      version: "codex-cli mock",
      error: "",
    },
    {
      id: "gemini",
      name: "Gemini CLI",
      description: "Runs Gemini CLI and sends the model request on stdin.",
      command: "gemini",
      args: [],
      protocol: "text",
      model: "default",
      capabilities: [],
      timeoutSeconds: 120,
      installed: false,
      version: "",
      error: "",
    },
    {
      id: "kiro",
      name: "Kiro CLI",
      description: "Runs Kiro CLI chat in non-interactive mode.",
      command: "kiro-cli",
      args: ["chat", "--no-interactive"],
      protocol: "text",
      model: "default",
      capabilities: [],
      timeoutSeconds: 120,
      installed: false,
      version: "",
      error: "",
    },
  ];
  const hookEvents = ["PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop", "PostLLMCall", "SessionStart", "SessionEnd", "SubagentStop", "Notification", "PreCompact"];
  const hookSettings: Record<string, HooksSettingsView> = {
    global: {
      scope: "global",
      path: "~/.WorkGround2/settings.json",
      projectRoot: "",
      trusted: true,
      events: hookEvents,
      hooks: [
        { event: "Stop", command: "echo turn done", description: "Notify after each turn" },
      ],
    },
    project: {
      scope: "project",
      path: "./.WorkGround2/settings.json",
      projectRoot: "/mock/project",
      trusted: false,
      events: hookEvents,
      hooks: [],
    },
  };
  settings.providers = settings.providers.map((provider) =>
    provider.apiKeyEnv === "DEEPSEEK_API_KEY" ? { ...provider, keySet: !freshMock } : provider,
  );
  if (freshMock) {
    settings.configPath = "~/.config/WorkGround2/config.toml";
  }
  const mockNow = Date.now();
  const mockProjectTree: ProjectNode[] = freshMock ? [] : [
    {
      key: "project_~/projects/joyquant-db",
      kind: "project",
      label: t("mock.projectJoyquantDb"),
      root: "~/projects/joyquant-db",
      projectColor: "blue",
      children: [
        { key: "topic_dev_standard", kind: "topic", label: `● ${t("mock.topicDevStandard")}`, root: "~/projects/joyquant-db", topicId: "topic_dev_standard", projectColor: "blue", turns: 18, lastActivityAt: mockNow - 8 * 60_000, open: true, running: runningMock },
        { key: "topic_db_maint", kind: "topic", label: t("mock.topicDbMaint"), root: "~/projects/joyquant-db", topicId: "topic_db_maint", projectColor: "blue", turns: 7, lastActivityAt: mockNow - 2 * 60 * 60_000 },
        { key: "topic_env", kind: "topic", label: t("mock.topicEnv"), root: "~/projects/joyquant-db", topicId: "topic_env", projectColor: "blue", turns: 3, lastActivityAt: mockNow - 26 * 60 * 60_000 },
      ],
    },
    {
      key: "project_~/projects/joyquant-sys",
      kind: "project",
      label: t("mock.projectJoyquantSys"),
      root: "~/projects/joyquant-sys",
      projectColor: "purple",
      children: [
        { key: "topic_p3b_pd", kind: "topic", label: `● ${t("mock.topicP3b")}`, root: "~/projects/joyquant-sys", topicId: "topic_p3b_pd", projectColor: "purple", turns: 11, lastActivityAt: mockNow - 3 * 24 * 60 * 60_000, status: runningMock ? "streaming" : undefined },
        { key: "topic_p3a_pd", kind: "topic", label: t("mock.topicP3a"), root: "~/projects/joyquant-sys", topicId: "topic_p3a_pd", projectColor: "purple", turns: 9, lastActivityAt: mockNow - 4 * 24 * 60 * 60_000, status: runningMock ? "thinking" : undefined },
        { key: "topic_hotfix", kind: "topic", label: t("mock.topicHotfix"), root: "~/projects/joyquant-sys", topicId: "topic_hotfix", projectColor: "purple", turns: 4, lastActivityAt: mockNow - 5 * 24 * 60 * 60_000, status: runningMock ? "thinking" : undefined },
        { key: "topic_sys_coord", kind: "topic", label: t("mock.topicSysCoord"), root: "~/projects/joyquant-sys", topicId: "topic_sys_coord", projectColor: "purple", turns: 14, lastActivityAt: mockNow - 6 * 24 * 60 * 60_000, status: runningMock ? "waiting_confirmation" : undefined },
        { key: "topic_sys_standard", kind: "topic", label: t("mock.topicSysStandard"), root: "~/projects/joyquant-sys", topicId: "topic_sys_standard", projectColor: "purple", turns: 6, lastActivityAt: mockNow - 7 * 24 * 60 * 60_000, status: "paused" },
        { key: "topic_sys_exception", kind: "topic", label: t("mock.topicSysException"), root: "~/projects/joyquant-sys", topicId: "topic_sys_exception", projectColor: "purple", turns: 2, lastActivityAt: mockNow - 8 * 24 * 60 * 60_000, status: "error" },
      ],
    },
    {
      key: "global_folder",
      kind: "global_folder",
      label: "Global",
      root: globalWorkspaceRoot,
      children: [
        { key: "global_topic_product", kind: "global_topic", label: t("mock.topicProduct"), topicId: "topic_product", turns: 5, lastActivityAt: mockNow - 8 * 24 * 60 * 60_000 },
        { key: "global_topic_ai", kind: "global_topic", label: t("mock.topicAi"), topicId: "topic_ai", turns: 8, lastActivityAt: mockNow - 10 * 24 * 60 * 60_000 },
        { key: "global_topic_lab", kind: "global_topic", label: t("mock.topicLab"), topicId: "topic_lab", turns: 2, lastActivityAt: mockNow - 12 * 24 * 60 * 60_000 },
        { key: "global_topic_room", kind: "global_topic", label: "联调 Room", topicId: "topic_room", sessionKind: "collaboration", sessionPath: "~/wg2-sessions/topic_room.jsonl", turns: 12, lastActivityAt: mockNow - 30 * 60_000 },
      ],
    },
  ];
  const ensureMockGlobalFolder = (): ProjectNode => {
    let node = mockProjectTree.find((item) => item.kind === "global_folder");
    if (!node) {
      node = {
        key: "global_folder",
        kind: "global_folder",
        label: "Global",
        root: globalWorkspaceRoot,
        children: [],
      };
      mockProjectTree.push(node);
    }
    return node;
  };
  const mockProjectTreeForDisplay = () => {
    const pinnedProjects = mockProjectTree.filter((node) => node.kind === "project" && node.pinned);
    if (pinnedProjects.length === 0) return mockProjectTree;
    const rest = mockProjectTree.filter((node) => !(node.kind === "project" && node.pinned));
    return [...pinnedProjects, ...rest];
  };
  const cloneProjectTree = () => {
    if (mockProjectTree.length === 0) ensureMockGlobalFolder();
    return JSON.parse(JSON.stringify(mockProjectTreeForDisplay())) as ProjectNode[];
  };
  const projectChildren = (node: ProjectNode): ProjectNode[] => Array.isArray(node.children) ? node.children : [];
  const findMockTopic = (topicId: string): ProjectNode | null => {
    for (const parent of mockProjectTree) {
      const found = projectChildren(parent).find((child) => child.topicId === topicId);
      if (found) return found;
    }
    return null;
  };
  const setMockTopicPinned = (topicId: string, pinned: boolean) => {
    for (const parent of mockProjectTree) {
      const children = projectChildren(parent);
      const index = children.findIndex((child) => child.topicId === topicId);
      if (index < 0) continue;
      const topic = { ...children[index], pinned: pinned || undefined };
      if (!pinned) {
        parent.children = children.map((child, i) => (i === index ? topic : child));
        return;
      }
      const remaining = children.filter((_, i) => i !== index);
      parent.children = [topic, ...remaining];
      return;
    }
  };
  const setMockProjectPinned = (workspaceRoot: string, pinned: boolean) => {
    const index = mockProjectTree.findIndex((node) => node.kind === "project" && node.root === workspaceRoot);
    if (index < 0) return;
    mockProjectTree[index] = { ...mockProjectTree[index], pinned: pinned || undefined };
  };
  const deleteMockTopic = (topicId: string) => {
    for (const parent of mockProjectTree) {
      parent.children = projectChildren(parent).filter((child) => child.topicId !== topicId);
    }
  };
  const topicLabel = (topicId: string, fallback: string) => (findMockTopic(topicId)?.label || fallback).replace(/^●\s*/, "");
  const mockTopicStatus = (topicId: string) => findMockTopic(topicId)?.status ?? "";
  const mockTopicIsRunning = (topicId: string) => {
    const status = mockTopicStatus(topicId);
    return status === "streaming" || status === "thinking" || status === "waiting_confirmation";
  };
  const mockTopicIsBlank = (topicId: string) => {
    const topic = findMockTopic(topicId);
    return Boolean(topic && topic.label === t("mock.newSession") && !topic.turns && !topic.lastActivityAt && !topic.status);
  };
  const mockTopicRunsInScenario = (topicId: string) => runningMock && mockTopicIsRunning(topicId);
  const mockLongTranscriptHistory = (): HistoryMessage[] => {
    const out: HistoryMessage[] = [];
    for (let i = 1; i <= 18; i++) {
      out.push({
        role: "user",
        content: `第 ${i} 轮：检查聊天滚动定位，切换会话后应该自动停在最新消息底部。`,
      });
      if (i === 4) {
        out.push({ role: "phase", content: "复现切换会话后的滚动位置" });
      }
      if (i === 8) {
        const toolID = "mock-scroll-layout-check";
        out.push({
          role: "assistant",
          content: "我会先读取滚动容器尺寸，再确认是否存在动态高度变化导致的底部偏移。",
          reasoning: "旧实现只重置 stick 标志，没有主动等待布局稳定；AskCard、Approval、Todo 这类卡片可能在下一帧改变高度。",
          toolCalls: [{ id: toolID, name: "bash", arguments: JSON.stringify({ command: "npm run check:css && pnpm typecheck" }) }],
        });
        out.push({
          role: "tool",
          toolCallId: toolID,
          toolName: "bash",
          content: "CSS syntax check passed\nz-index token check passed\ntsc --noEmit passed\n",
        });
        continue;
      }
      if (i === 13) {
        out.push({ role: "notice", level: "info", content: "模拟提示：用户向上查看历史后，右下角应出现跳到底部按钮。" });
      }
      out.push({
        role: "assistant",
        content: [
          `第 ${i} 轮结果：当前滚动契约会在切换会话或 reveal 信号到达后执行强制贴底。`,
          "它会先立即设置 scrollTop 到 scrollHeight，再连续几个 animation frame 复查，避免动态内容把底部再次推走。",
          "如果用户主动向上滚动，普通 streaming 不会强行拉回；只有点击跳到底部按钮或显式切换会话才会重新贴底。",
        ].join("\n\n"),
      });
    }
    out.push({
      role: "compaction",
      content: "",
      trigger: "manual",
      messages: 36,
      summary: "Mock 长会话用于验证桌面端 Transcript 自动贴底、多帧布局修正和跳到底部按钮。",
      archive: "mock-scroll-preview",
    });
    out.push({
      role: "assistant",
      content: "最终状态：这条消息应该位于真实底部。向上滚动后，右下角会显示跳到底部按钮；点击按钮后应回到这里。",
    });
    return out;
  };
	  const mockTopicHistory = (topicId: string): HistoryMessage[] => {
	    switch (topicId) {
      case "topic_product":
        return [
          {
            role: "user",
            content: [
              "[[WorkGround2-im]]",
              "provider=lark",
              "label=Feishu / Lark",
              "sender=ou_mock_user_001",
              "chat=p2p 会话",
              "[[/WorkGround2-im]]",
              "你可以做什么",
            ].join("\n"),
          },
          {
            role: "assistant",
            content: "这是 Global 范围下的 IM 会话。我可以先处理不依赖项目文件的问答、计划和信息整理；需要进入项目时，再由桌面端显式绑定或迁移到项目话题。",
          },
        ];
      case "topic_ai":
        return [
          {
            role: "user",
            content: [
              "[[WorkGround2-im]]",
              "provider=weixin",
              "label=微信",
              "sender=wxid_mock_user_001",
              "chat=单聊",
              "[[/WorkGround2-im]]",
              "帮我整理一下今天要做的事",
            ].join("\n"),
          },
          {
            role: "assistant",
            content: "可以。我会先在 Global 范围里整理任务清单；如果某条任务需要读取项目文件，再切到你授权的项目话题处理。",
          },
        ];
      case "topic_dev_standard":
        return mockLongTranscriptHistory();
      case "topic_p3b_pd":
        return [
          { role: "user", content: "把 p3b P&D 的范围和风险重新整理成可执行计划。" },
          { role: "phase", content: "分析需求范围" },
        ];
      case "topic_p3a_pd":
        return [
          { role: "user", content: "复盘 p3a 的技术方案，先不要写文件，先说明你的判断。" },
        ];
      case "topic_hotfix":
        return [
          { role: "user", content: "检查 post-p3-hotfix 的回归风险，重点看最近的 shell 输出和 git 改动。" },
          { role: "assistant", content: "", reasoning: "我先定位最近一次 hotfix 的上下文，然后用只读命令检查状态；左侧保持“思考中”，工具细节在这里展开。" },
        ];
      case "topic_sys_coord":
        return [
          { role: "user", content: "准备执行 joyquant-sys 的同步脚本，但需要我确认后再运行。" },
          { role: "assistant", content: "", reasoning: "这个动作会运行脚本并可能刷新本地缓存，所以需要先等用户确认。" },
        ];
      case "topic_sys_standard":
        return [
          { role: "user", content: "继续制定 SYS 项目开发规范，先停在当前检查点。" },
          { role: "assistant", content: "已暂停在规范整理阶段。当前保留了目录约定、分支策略和待确认的发布检查项；继续时可以从这里恢复。" },
          { role: "notice", level: "info", content: "会话已暂停：未继续执行命令，等待用户恢复或切换任务。" },
        ];
      case "topic_sys_exception":
        return [
          { role: "user", content: "演练异常处理流程，看看失败时界面怎么提示。" },
          { role: "assistant", content: "我尝试校验恢复脚本时遇到异常，已停止继续执行。" },
          { role: "notice", level: "warn", content: "运行异常：恢复脚本缺少必要环境变量 JOYQUANT_SYS_TOKEN。请补齐配置后重试。" },
        ];
      default:
        return [];
	    }
	  };
	  const mockHistoryPage = (messages: HistoryMessage[], beforeTurn = 0, limit = 60): HistoryPage => {
	    const totalTurns = messages.reduce((count, message) => count + (message.role === "user" ? 1 : 0), 0);
	    const safeLimit = Math.max(1, Math.min(200, Math.floor(limit || 60)));
	    const endTurn = beforeTurn > 0 && beforeTurn <= totalTurns ? beforeTurn : totalTurns;
	    const startTurn = Math.max(0, endTurn - safeLimit);
	    let turn = -1;
	    const pageMessages = messages.filter((message) => {
	      if (message.role === "user") turn += 1;
	      if (turn < 0) return startTurn === 0;
	      return turn >= startTurn && turn < endTurn;
	    });
	    return { messages: pageMessages, startTurn, endTurn, totalTurns, hasOlder: startTurn > 0 };
	  };
	  const mockRuntimeInjected = new Set<string>();
  const queueMockTopicRuntime = (tab: TabMeta) => {
    if (!runningMock) return;
    const status = mockTopicStatus(tab.topicId);
    if (status !== "streaming" && status !== "thinking" && status !== "waiting_confirmation") return;
    const key = `${tab.id}:${tab.topicId}:${status}`;
    if (mockRuntimeInjected.has(key)) return;
    mockRuntimeInjected.add(key);
    window.setTimeout(() => {
      void withMockTabScope(tab.id, async () => {
        emitMockTurnStarted();
        await delay(120);
        if (tab.topicId === "topic_p3b_pd") {
          const text = "我会先把范围拆成三层：目标、依赖、风险。当前已经确认 p3b 的交付边界，接下来补充每个模块的验收口径...";
          for (const ch of text) {
            emit({ kind: "text", text: ch });
            await delay(5);
          }
          return;
        }
        if (tab.topicId === "topic_p3a_pd") {
          emit({ kind: "reasoning", text: "我正在对比 p3a 和 p3b 的差异：先看约束，再看变更风险，最后判断是否需要拆成独立任务。\n\n" });
          await delay(220);
          emit({ kind: "reasoning", text: "当前倾向：先保留 p3a 的兼容路径，不急于删除旧逻辑。" });
          return;
        }
        if (tab.topicId === "topic_hotfix") {
          const id = "mock-hotfix-shell";
          emit({ kind: "tool_dispatch", tool: { id, name: "bash", args: JSON.stringify({ command: "git status --short && npm test" }), readOnly: true } });
          await delay(180);
          emit({ kind: "tool_progress", tool: { id, name: "bash", readOnly: true, output: "$ git status --short\n M internal/sys/runner.go\n\n$ npm test\nrunning targeted regression tests...\n" } });
          return;
        }
        if (tab.topicId === "topic_sys_coord") {
          pendingApprovalPreview = true;
          emit({ kind: "reasoning", text: "我已经准备好执行同步脚本，但这个操作会影响本地 workspace，需要用户确认。" });
          await delay(160);
          emit({
            kind: "approval_request",
            approval: {
              id: "mock-sys-confirm",
              tool: "bash",
              subject: "npm run sync:joyquant-sys\n\n该命令会同步 SYS 项目配置并刷新本地缓存。",
            },
          });
        }
      });
    }, 180);
  };
  const setMockActiveTab = (tabId: string) => {
    mockTabs = mockTabs.map((tab) => ({ ...tab, active: tab.id === tabId }));
  };
  const currentMockTurnTabId = () => mockScopedTabId || mockTabs.find((tab) => tab.active)?.id;
  const setMockTabRunning = (tabId: string | undefined, running: boolean) => {
    if (!tabId) return;
    mockTabs = mockTabs.map((tab) => (tab.id === tabId ? { ...tab, running } : tab));
  };
  const emitMockTurnStarted = () => {
    setMockTabRunning(currentMockTurnTabId(), true);
    emit({ kind: "turn_started" });
  };
  const emitMockTurnDone = () => {
    setMockTabRunning(currentMockTurnTabId(), false);
    emit({ kind: "turn_done" });
  };
  const mockBlankCreates = new Map<string, { target: string; tabId: string }>();
  let mockTabs: TabMeta[] = freshMock ? [
    {
      id: "tab_global",
      scope: "global",
      workspaceRoot: globalWorkspaceRoot,
      workspaceName: "Global",
      workspacePath: globalWorkspaceRoot,
      topicId: "",
      topicTitle: "Global",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: true,
      cwd: globalWorkspaceRoot,
    },
  ] : [
    {
      id: "tab_joyquant_db",
      scope: "project",
      workspaceRoot: "~/projects/joyquant-db",
      workspaceName: "joyquant-db",
      workspacePath: "~/projects/joyquant-db",
      gitBranch: "main",
      topicId: "topic_dev_standard",
      topicTitle: t("mock.trashDevStandardTitle"),
      projectColor: "blue",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: !guidanceMock,
      cwd: "~/projects/joyquant-db",
    },
    {
      id: "tab_joyquant_sys",
      scope: "project",
      workspaceRoot: "~/projects/joyquant-sys",
      workspaceName: "joyquant-sys",
      workspacePath: "~/projects/joyquant-sys",
      gitBranch: "feature/p3b",
      topicId: "topic_p3b_pd",
      topicTitle: "p3b P&D",
      projectColor: "purple",
      label: "DeepSeek-R1",
      ready: true,
      running: runningMock && mockTopicIsRunning("topic_p3b_pd"),
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: guidanceMock,
      cwd: "~/projects/joyquant-sys",
    },
    {
      id: "tab_global",
      scope: "global",
      workspaceRoot: "",
      workspaceName: "Global",
      workspacePath: "~/projects/joyquant-db",
      topicId: "topic_global",
      topicTitle: "Global",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: false,
      cwd: "~/projects/joyquant-db",
    },
  ];
  const mockModelCatalog = [
    { ref: "deepseek/deepseek-v4-flash", provider: "deepseek", model: "deepseek-v4-flash" },
    { ref: "deepseek/deepseek-v4-pro", provider: "deepseek", model: "deepseek-v4-pro" },
  ];
  const defaultMockModelRef = mockModelCatalog[0].ref;
  const mockModelRef = (name: string): string => {
    const trimmed = name.trim();
    if (!trimmed || trimmed === "DeepSeek-R1") return defaultMockModelRef;
    const exact = mockModelCatalog.find((model) => model.ref === trimmed);
    if (exact) return exact.ref;
    const byModel = mockModelCatalog.find((model) => model.model === trimmed);
    return byModel?.ref ?? trimmed;
  };
  const mockModelLabel = (ref: string): string => mockModelCatalog.find((model) => model.ref === mockModelRef(ref))?.model ?? ref.split("/").pop() ?? ref;
  const mockTabModelRef = (tab?: TabMeta): string => mockModelRef(tab?.label ?? "");
  const setMockTabModel = (tabID: string | undefined, name: string) => {
    const ref = mockModelRef(name);
    const label = mockModelLabel(ref);
    let applied = false;
    mockTabs = mockTabs.map((tab) => {
      const match = tabID ? tab.id === tabID : tab.active;
      if (!match) return tab;
      applied = true;
      return { ...tab, label };
    });
    if (!applied && mockTabs.length > 0) {
      mockTabs = mockTabs.map((tab, index) => (index === 0 ? { ...tab, label } : tab));
    }
  };
  const workUnavailableError = () =>
    new Error(
      "Work features are not available in the browser dev mock. " +
      "Run the desktop app (wails dev / wails build) to use Work.",
    );
	let mockDecisionState: DecisionStateView = {
		available: true,
		revision: 1,
		queue: [],
		deferred: [],
		history: [],
		channels: [],
		settings: { externalMode: "smart", smartGraceSec: 30 },
	};
  const assistantNow = new Date();
  const assistantISO = (hour: number, minute: number) => {
    const value = new Date(assistantNow);
    value.setHours(hour, minute, 0, 0);
    return value.toISOString();
  };
  const mockAssistant: AssistantRecord = {
    id: "assistant-code-project",
    name: "代码项目助手",
    description: "持续关注项目健康度和发布准备情况",
    mission: "定期扫描项目修改、测试和构建结果，整理风险；发布条件满足时先询问我是否发布。",
    scope: "workspace",
    workspace_root: "~/projects/WorkGround2",
    lifecycle: "active",
    policy: {
      local_write: "allow",
      network: "deny",
      publish: "approve",
      delete: "approve",
      payment: "approve",
      secrets: "approve",
      private_data: "approve",
    },
    memory_revision: 1,
    revision: 1,
    created_at: assistantISO(8, 20),
    updated_at: assistantISO(10, 5),
  };
  const mockRoutine: AssistantRoutine = {
    id: "routine-release-check",
    assistant_id: mockAssistant.id,
    title: "发布准备检查",
    prompt: "检查最近提交、测试与构建产物，确认发布风险并总结下一步。",
    schedule: { kind: "daily", timezone: "Asia/Singapore", at: "18:00" },
    enabled: true,
    catch_up: "coalesce_latest",
    last_scheduled_for: assistantISO(9, 30),
    revision: 1,
    created_at: assistantISO(8, 20),
    updated_at: assistantISO(8, 20),
  };
  let mockAssistantSnapshots: AssistantSnapshot[] = freshMock ? [] : [{
    revision: 1,
    assistant: mockAssistant,
    routines: [mockRoutine],
    memory: {
      revision: 1,
      items: [{
        id: "memory-windows-process",
        kind: "strategy",
        body: "Windows 构建前，先确认旧进程已经退出。",
        evidence: "上次构建失败源于残留进程占用端口。",
        source_run: "run-memory",
        locked: false,
        revision: 1,
        created_at: assistantISO(10, 5),
        updated_at: assistantISO(10, 5),
      }],
    },
    runs: [
      {
        id: "run-scan",
        assistant_id: mockAssistant.id,
        routine_id: mockRoutine.id,
        request_id: "mock-run-scan",
        trigger: "scheduled",
        state: "succeeded",
        attempt: 1,
        max_attempts: 3,
        session_path: "/mock/sessions/assistant-scan.jsonl",
        scheduled_for: assistantISO(9, 30),
        started_at: assistantISO(9, 30),
        finished_at: assistantISO(9, 34),
        summary: "## 结论\n\n测试都通过了，但发布说明还缺一段升级提醒。所有单元测试与集成测试通过，构建产物已生成。\n\n![构建结果预览](https://example.com/assistant-build-report.png)\n\n## 取证证据\n\n变更包括任务取消逻辑修复、日志脱敏优化，以及 Windows 构建脚本的健壮性增强。CI 日志、构建产物与差异统计已归档。",
        revision: 1,
        created_at: assistantISO(9, 30),
        updated_at: assistantISO(9, 34),
      },
      {
        id: "run-memory",
        assistant_id: mockAssistant.id,
        routine_id: mockRoutine.id,
        request_id: "mock-run-memory",
        trigger: "scheduled",
        state: "succeeded",
        attempt: 1,
        max_attempts: 3,
        scheduled_for: assistantISO(10, 5),
        started_at: assistantISO(10, 5),
        finished_at: assistantISO(10, 6),
        summary: "已把上次构建失败的根因写入显式记忆，并加入构建脚本前置检查。",
        revision: 1,
        created_at: assistantISO(10, 5),
        updated_at: assistantISO(10, 6),
      },
    ],
    attention: [],
    plan: {
      revision: 2,
      responsibilities: [
        { id: "resp-scan", assistant_id: mockAssistant.id, alias: "scan", objective: "扫描修改与构建", done_criteria: "扫描报告已生成", next_action: "跑一次扫描", status: "done", depends_on: [], revision: 1, created_at: assistantISO(9, 30), updated_at: assistantISO(9, 34) },
        { id: "resp-release", assistant_id: mockAssistant.id, alias: "release-notes", objective: "补齐发布说明", done_criteria: "升级提醒已写入", next_action: "写入升级提醒", status: "ready", depends_on: ["resp-scan"], revision: 1, created_at: assistantISO(9, 30), updated_at: assistantISO(9, 34) },
      ],
    },
    artifacts: [
      { id: "artifact-scan", assistant_id: mockAssistant.id, resp_id: "resp-scan", run_id: "run-scan", title: "扫描报告", kind: "report", content: "测试通过", evidence: "CI 日志", revision: 1, created_at: assistantISO(9, 34) },
    ],
    opportunities: [
      { id: "opp-release", assistant_id: mockAssistant.id, resp_id: "resp-release", run_id: "run-scan", reason: "发布说明可补齐", revision: 1, created_at: assistantISO(9, 34) },
    ],
    updated_at: assistantISO(10, 6),
  }];
  const cloneAssistant = <T,>(value: T): T => structuredClone(value);
  const findAssistantSnapshot = (id: string): AssistantSnapshot => {
    const snapshot = mockAssistantSnapshots.find((item) => item.assistant.id === id);
    if (!snapshot) throw new Error("找不到助手");
    return snapshot;
  };
  const touchAssistantSnapshot = (snapshot: AssistantSnapshot) => {
    snapshot.revision += 1;
    snapshot.updated_at = new Date().toISOString();
  };
  return {
		async DecisionState() { return structuredClone(mockDecisionState); },
		async CreateDecision(input) {
			const now = new Date().toISOString();
			const notify = input.kind === "notify";
			const value = {
				id: `D-MOCK-${Date.now()}`,
				kind: notify ? "notify" as const : "ask" as const,
				status: notify ? "applied" as const : mockDecisionState.active ? "queued" as const : "presented" as const,
				queue_seq: mockDecisionState.revision + 1,
				created_at: now,
				presented_at: notify || !mockDecisionState.active ? now : undefined,
				applied_at: notify ? now : undefined,
				origin: { kind: "agent", agent_id: input.agentId, thread_id: input.threadId, workspace_root: input.workspaceRoot },
				presentation: {
					title: input.title, task_summary: input.taskSummary, why_now: input.whyNow,
					questions: input.questions.map((q) => ({ id: q.id, header: q.header, prompt: q.prompt, options: q.options, multi_select: q.multiSelect })),
					no_answer_policy: input.noAnswerPolicy,
				},
			};
			if (notify) mockDecisionState.history.unshift(value);
			else if (mockDecisionState.active) mockDecisionState.queue.push(value);
			else mockDecisionState.active = value;
			mockDecisionState.revision++;
			return value;
		},
		async ResolveDecision(input) {
			if (mockDecisionState.active?.id === input.decisionId) {
				mockDecisionState.history.unshift({ ...mockDecisionState.active, status: "applied", answer: { selections: input.selections.map((s) => ({ question_id: s.questionId, selected: s.selected })) }, responder: { kind: "desktop", label: input.responder } });
				mockDecisionState.active = mockDecisionState.queue.shift();
				if (mockDecisionState.active) mockDecisionState.active.status = "presented";
			}
			mockDecisionState.revision++;
			return structuredClone(mockDecisionState);
		},
		async DeferDecision(id) {
			if (mockDecisionState.active?.id === id) {
				mockDecisionState.deferred.push({ ...mockDecisionState.active, status: "deferred" });
				mockDecisionState.active = mockDecisionState.queue.shift();
			}
			return structuredClone(mockDecisionState);
		},
		async ResumeDecision(id) {
			const index = mockDecisionState.deferred.findIndex((item) => item.id === id);
			if (index >= 0) mockDecisionState.queue.push({ ...mockDecisionState.deferred.splice(index, 1)[0], status: "queued" });
			return structuredClone(mockDecisionState);
		},
		async CancelDecision(id) {
			if (mockDecisionState.active?.id === id) {
				mockDecisionState.history.unshift({ ...mockDecisionState.active, status: "cancelled" });
				mockDecisionState.active = mockDecisionState.queue.shift();
			}
			return structuredClone(mockDecisionState);
		},
		async SaveDecisionSettings(input) {
			mockDecisionState.settings = { externalMode: input.externalMode, localOnlyUntil: input.localOnlyUntil || undefined, smartGraceSec: input.smartGraceSec };
			return structuredClone(mockDecisionState);
		},
		async SaveDecisionChannel(input) {
			const channel = { id: input.id || `channel-${Date.now()}`, name: input.name, kind: input.kind, enabled: input.enabled, connection_id: input.connectionId, domain: input.domain, chat_id: input.chatId, chat_type: input.chatType };
			mockDecisionState.channels = [...mockDecisionState.channels.filter((item) => item.id !== channel.id), channel];
			return structuredClone(mockDecisionState);
		},
		async DeleteDecisionChannel(id) {
			mockDecisionState.channels = mockDecisionState.channels.filter((item) => item.id !== id);
			return structuredClone(mockDecisionState);
		},
		async TestDecisionChannel() {},
		async InstallDecisionSkill() { return { ok: true, path: "", skillPath: "~/.codex/skills/ask-workground2-owner" }; },
		async ExportDecisionSkills() {
			if (mockDecisionSkillExportFn) return mockDecisionSkillExportFn();
			return { exported: true, canceled: false, path: "C:\\Downloads\\workground2-owner-skills.zip" };
		},
    async GetCollaborationState() { return { status: "disconnected", members: [], timeline: [] }; },
    async RetryCollaboration() { return { status: "disconnected", members: [], timeline: [] }; },
    async HostCollaborationRoom() { throw new Error("Use the collaboration browser transport in preview mode"); },
    async JoinCollaborationRoom() { throw new Error("Use the collaboration browser transport in preview mode"); },
    async GetCollaborationInvite() { throw new Error("Use the collaboration browser transport in preview mode"); },
    async LeaveCollaborationRoom() {},
    async ClassifyCollaborationIntent() { return { intent: "chat", source: "fallback", error: "Semantic intent preview unavailable", retryable: true }; },
    async PostCollaborationMessage(input) { return { ok: false, requestID: input.requestID, error: "Collaboration preview transport unavailable", retryable: true }; },
    async StartCollaborationAgent(input) { return { ok: false, requestID: input.requestID, error: "Collaboration preview transport unavailable", retryable: true }; },
    async StopCollaborationAgentRun() { /* no-op in preview */ },
    async CancelCollaborationQueuedTask(input) { return { ok: false, requestID: input.taskID, error: "Collaboration preview transport unavailable", retryable: true }; },
    async RespondCollaborationAgentRun(input) { return { ok: false, requestID: `${input.runID}:respond`, error: "Collaboration preview transport unavailable", retryable: true }; },
    async RespondCollaborationRequest(input) { return { ok: false, requestID: input.requestID, error: "Collaboration preview transport unavailable", retryable: true }; },
    async UpdateCollaborationAgentConfig(input) { return { status: "disconnected", members: [], timeline: [], agentConfig: input.config }; },
    async UpdateCollaborationProfile(input) { return { status: "disconnected", members: [], timeline: [], agentConfig: { alias: input.agentName, autoRespondQuestions: false, autoRespondRequests: false, autoRespondAgents: false, agentResponseIntervalSeconds: 30, agentClockTurns: 12, agentClockUnlimited: false, recognitionMode: "interval" } }; },
    async UpdateCollaborationToolApprovalMode(input) { return { status: "disconnected", members: [], timeline: [], toolApprovalMode: input.mode }; },
    async ShareCollaborationFiles() { return []; },
    async ReceiveCollaborationFile() { throw new Error("File transfer preview unavailable"); },
    async PauseCollaborationFile() { throw new Error("File transfer preview unavailable"); },
    async ResumeCollaborationFile() { throw new Error("File transfer preview unavailable"); },
    async RevokeCollaborationFile() { return { ok: false, error: "File transfer preview unavailable", retryable: true }; },
    async OpenCollaborationFile() { throw new Error("File transfer preview unavailable"); },
    async RevealCollaborationFile() { throw new Error("File transfer preview unavailable"); },
    async PreviewCollaborationFile() { throw new Error("File preview unavailable"); },
    async EnterWidgetMode() {
      widgetMode = true;
      return mockWidgetSnapshot();
    },
    async ExitWidgetMode() {
      widgetMode = false;
    },
    async IsWidgetMode() {
      return widgetMode;
    },
    async GetWidgetSnapshot() {
      return mockWidgetSnapshot();
    },
    async ApplyWidgetAction(input) {
      const current = mockWidgetSnapshot().current;
      if (!current || current.id !== input.itemId || current.revision !== input.revision) {
        return { status: "stale", error: "消息已经变化，请按最新状态操作", snapshot: mockWidgetSnapshot() };
      }
		if (widgetScenario === "widget-choice3" && input.action === "answer") widgetChoiceAnswered = true;
      if (input.action === "open") widgetMode = false;
      widgetRevision += 1;
      return { status: "accepted", snapshot: mockWidgetSnapshot() };
    },
		async StartWidgetConversation(input) {
			await delay(420);
			if (!input.prompt.trim()) {
				return { status: "invalid", error: "请输入对话内容", snapshot: mockWidgetSnapshot() };
			}
			widgetConversationStarted = true;
			widgetRevision += 1;
			const ws = input.workspace || "auto";
			let workspaceName = "WorkGround2";
			let routeReason = "名称匹配";
			if (ws === "global") { workspaceName = "Global"; routeReason = "手动选择"; }
			else if (ws.startsWith("project:")) {
				const root = ws.slice("project:".length);
				if (root.includes("CICDBOT")) { workspaceName = "CICDBOT"; routeReason = "手动选择"; }
				else { workspaceName = root.split("/").pop() || root.split("\\").pop() || "Project"; routeReason = "手动选择"; }
			}
			return {
				status: "accepted",
				tabId: "tab-widget-new",
				workspaceRoot: `~/projects/${workspaceName}`,
				workspaceName,
				routeReason,
				snapshot: mockWidgetSnapshot(),
			};
		},
		async OpenWidgetWorkspace(workspace: string, requestId: string) {
			let scope = "global";
			let workspaceRoot = "";
			if (workspace.startsWith("project:")) {
				scope = "project";
				workspaceRoot = workspace.slice("project:".length);
			}
			widgetRevision += 1;
			widgetMode = false;
			return this.CreateBlankSession({ scope, workspaceRoot, requestId });
		},
		async ListWidgetWorkspaces() {
			return [
				{ scope: "auto", name: "自动" },
				{ scope: "project", name: "WorkGround2", root: "~/projects/WorkGround2" },
				{ scope: "project", name: "CICDBOT", root: "~/projects/CICDBOT" },
				{ scope: "global", name: "Global" },
			];
		},
		async GetDesktopIconSnapshot() { return mockDesktopIconSnapshot(); },
		async GetExternalRunSnapshot() { return mockExternalRunSnapshot(); },
		async LaunchDSHRun(input) {
			if (!input.requestId || !input.prompt.trim()) throw new Error("requestId and prompt are required");
			widgetRevision += 1;
			const snapshot = mockExternalRunSnapshot();
			return { receipt: { status: "accepted", runId: snapshot.runs[0].id, revision: snapshot.runs[0].revision }, run: snapshot.runs[0], snapshot };
		},
		async CancelExternalRun(input) {
			if (!input.runId || !input.requestId) throw new Error("runId and requestId are required");
			widgetRevision += 1;
			const snapshot = mockExternalRunSnapshot();
			const run = { ...snapshot.runs[0], state: "cancelled" as const, revision: widgetRevision };
			return { receipt: { status: "accepted", runId: run.id, revision: run.revision }, run, snapshot: { ...snapshot, runs: [run] } };
		},
		async GetDesktopWorkspaceSlots() { return desktopWorkspaceSlots; },
		async GetDesktopRoomPins() { return [...desktopRoomPins]; },
		async GetDesktopRoomIcons() { return { ...desktopRoomIcons }; },
		async DesktopIconSearch(query) {
			const needle = query.trim().toLowerCase();
			const items: DesktopIconSearchItem[] = [
				{ id: "search:task", kind: "task", title: "实现桌面图标模式", subtitle: "WorkGround2", sourceId: "mock-task" },
				{ id: "search:workspace", kind: "workspace", title: "WorkGround2", subtitle: "~/projects/WorkGround2", sourceId: "~/projects/WorkGround2" },
			];
			return { items: items.filter((item) => `${item.title} ${item.subtitle || ""}`.toLowerCase().includes(needle)) };
		},
		async ApplyDesktopIconAction(input) {
			const current = mockDesktopIconSnapshot();
			const item = current.items.find((candidate) => candidate.id === input.itemId);
			if (!item || item.revision !== input.revision) return { status: "stale", error: "图标状态已经变化", snapshot: current };
			widgetRevision += 1;
			return { status: "accepted", snapshot: mockDesktopIconSnapshot() };
		},
		async CreateDailyRoutine(input) {
			return { status: "accepted", routine: { id: `mock-${input.requestId}`, workspaceRoot: "~/projects/WorkGround2", name: "启动测试", goal: "运行测试", prompt: "启动一轮测试", sourceRevision: "mock", createdAt: Date.now(), updatedAt: Date.now() } };
		},
		async ListDailyRoutines(workspaceRoot) {
			return [{ id: "mock-routine", workspaceRoot, name: "启动测试", goal: "运行一轮测试", prompt: "启动一轮测试", successSteps: ["运行定向测试"], failureLessons: ["失败时保留日志并重试"], sourceRevision: "mock", createdAt: Date.now(), updatedAt: Date.now() }];
		},
		async RunDailyRoutine(input) { return { status: "accepted", tabId: `daily-${input.routineId}` }; },
		async RenameDailyRoutine(input) { return { status: "accepted", routine: { id: input.routineId, workspaceRoot: input.workspaceRoot, name: input.name, goal: "运行一轮测试", prompt: "启动一轮测试", sourceRevision: "mock", createdAt: Date.now(), updatedAt: Date.now() } }; },
		async DeleteDailyRoutine() { return { status: "accepted" }; },
		async SetDesktopIconHitRegions() {},
		async SetDesktopIconSurface(input) {
			// Mock mirrors the backend clamp: bounded by and anchored to the
			// bottom-right of a virtual 1920×1080 work area.
			const width = Math.min(1920, Math.max(640, input.width + input.envelope * 2));
			const height = Math.min(1080, Math.max(540, input.height + input.envelope * 2));
			return { width, height, x: Math.max(0, 1920 - width - 16), y: Math.max(0, 1080 - height - 24), generation: input.generation };
		},
		async SetDesktopWorkspaceSlots(slots: number) {
			if (!Number.isInteger(slots) || slots < 0 || slots > 4) throw new Error("desktop workspace slots must be between 0 and 4");
			desktopWorkspaceSlots = slots;
			widgetRevision += 1;
		},
		async SetDesktopRoomPinned(topicID: string, pinned: boolean) {
			const id = topicID.trim();
			if (!id) throw new Error("topicID is required");
			const current = desktopRoomPins.includes(id);
			if (current === pinned) return;
			if (pinned) {
				if (desktopRoomPins.length >= 7) throw new Error("desktop Room pin limit reached (7)");
				desktopRoomPins = [id, ...desktopRoomPins];
			} else {
				desktopRoomPins = desktopRoomPins.filter((candidate) => candidate !== id);
			}
			widgetRevision += 1;
		},
		async SetDesktopRoomIcon(topicID: string, icon: string) {
			const id = topicID.trim();
			if (!id) throw new Error("topicID is required");
			const raw = icon.trim().toLowerCase();
			const normalized = projectIconKey(raw);
			if (raw && !normalized) throw new Error(`unsupported Room icon ${icon}`);
			if (desktopRoomIcons[id] === normalized || (!normalized && !(id in desktopRoomIcons))) return;
			if (normalized) desktopRoomIcons = { ...desktopRoomIcons, [id]: normalized };
			else {
				const next = { ...desktopRoomIcons };
				delete next[id];
				desktopRoomIcons = next;
			}
			widgetRevision += 1;
		},
		async WriteDesktopIconDiagnostics() {},
		async DesktopIconDiagnosticsPath() {
			// Mirrors the Go-side stable per-user path suffix; the real binding
			// returns the actual absolute path under the user state dir.
			return "desktop-icon-diagnostics.ndjson";
		},
		async RefreshWidgetWindowRegion() {
			// no-op in mock — the real Wails binding calls the Go backend
		},
    async MinimiseMainWindow() {
      console.info("mock MinimiseMainWindow");
    },
    async ToggleMaximiseMainWindow() {
      console.info("mock ToggleMaximiseMainWindow");
    },
    async IsMainWindowMaximised() {
      return false;
    },
    async CloseMainWindow() {
      console.info("mock CloseMainWindow");
    },
    async DismissMainWindow() {
      console.info("mock DismissMainWindow");
    },
    async Platform() {
      const override = browserPlatformOverride();
      if (override) return override;
      // Mirror the OS the browser dev mock runs on.
      const ua = typeof navigator !== "undefined" ? navigator.userAgent : "";
      if (/Win/i.test(ua)) return "windows";
      if (/Mac/i.test(ua)) return "darwin";
      return "linux";
    },
        async Submit(input) {
          cancelled = false;
      emitMockTurnStarted();
      const trimmedInput = input.trim().toLowerCase();
      const goalMatch = /^\/goal(?:\s+([\s\S]*))?$/.exec(input.trim());
      if (goalMatch) {
        const arg = stripGoalResearchFlags((goalMatch[1] ?? "").trim());
        const lowered = arg.toLowerCase();
        const active = mockTabs.find((tab) => tab.active);
        if (!arg || lowered === "status") {
          emit({ kind: "notice", level: "info", text: active?.goal ? `goal: ${active.goal}` : "goal: none" });
          emitMockTurnDone();
          return;
        }
        if (["clear", "off", "stop", "done"].includes(lowered)) {
          mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: "", goalStatus: "stopped", collaborationMode: "normal" } : tab));
          emit({ kind: "notice", level: "info", text: "goal cleared" });
          emitMockTurnDone();
          return;
        }
        mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: arg, goalStatus: "running", collaborationMode: "goal" } : tab));
        emit({ kind: "notice", level: "info", text: `goal set: ${arg}` });
        await delay(350);
        if (cancelled) return;
        const reply = `Autonomous goal run started for: **${arg}**\n\nMock run completed.\n\n[goal:complete]`;
        emit({ kind: "message", text: reply });
        mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: "", goalStatus: "complete", collaborationMode: "normal" } : tab));
        emit({ kind: "notice", level: "info", text: "goal complete" });
        emitMockTurnDone();
        return;
      }
      if (trimmedInput === "/approve-preview" || trimmedInput === "approve preview" || trimmedInput === "approve预览") {
        pendingApprovalPreview = true;
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "approval_request",
          approval: {
            id: "mock-approval-preview",
            tool: "bash",
            subject: t("mock.approvalSubject"),
          },
        });
        return;
      }
      if (
        trimmedInput === "/plan-approve-preview" ||
        trimmedInput === "plan approve preview" ||
        trimmedInput === "plan approve预览"
      ) {
        pendingApprovalPreview = true;
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "approval_request",
          approval: {
            id: "mock-plan-approval-preview",
            tool: "exit_plan_mode",
            subject: "",
          },
        });
        return;
      }
      if (trimmedInput === "/ask-preview" || trimmedInput === "ask preview" || trimmedInput === "ask预览") {
        pendingAskPreview = true;
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "ask_request",
          ask: {
            id: "mock-ask-preview",
            questions: [
              {
                id: "q1",
                header: t("mock.askQ1Header"),
                prompt: t("mock.askQ1Prompt"),
                options: [
                  { label: t("mock.askQ1Opt1Label"), description: t("mock.askQ1Opt1Desc") },
                  { label: t("mock.askQ1Opt2Label"), description: t("mock.askQ1Opt2Desc") },
                  { label: t("mock.askQ1Opt3Label"), description: t("mock.askQ1Opt3Desc") },
                ],
              },
              {
                id: "q2",
                header: t("mock.askQ2Header"),
                prompt: t("mock.askQ2Prompt"),
                options: [
                  { label: t("mock.askQ2Opt1Label"), description: t("mock.askQ2Opt1Desc") },
                  { label: t("mock.askQ2Opt2Label"), description: t("mock.askQ2Opt2Desc") },
                  { label: t("mock.askQ2Opt3Label"), description: t("mock.askQ2Opt3Desc") },
                ],
              },
            ],
          },
        });
        return;
      }
      if (trimmedInput === "/todo-preview" || trimmedInput === "todo preview" || trimmedInput === "todo预览") {
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "tool_dispatch",
          tool: {
            id: "mock-todo-preview",
            name: "todo_write",
            args: JSON.stringify({
              todos: [
                { content: t("mock.todo1"), status: "completed" },
                { content: t("mock.todo2"), activeForm: t("mock.todo2ActiveForm"), status: "in_progress" },
                { content: t("mock.todo3"), status: "pending" },
              ],
            }),
            readOnly: false,
          },
        });
        await delay(150);
        emit({
          kind: "tool_result",
          tool: {
            id: "mock-todo-preview",
            name: "todo_write",
            args: JSON.stringify({
              todos: [
                { content: t("mock.todo1"), status: "completed" },
                { content: t("mock.todo2"), activeForm: t("mock.todo2ActiveForm"), status: "in_progress" },
                { content: t("mock.todo3"), status: "pending" },
              ],
            }),
            output: "todo list updated",
            readOnly: false,
            durationMs: 150,
          },
        });
        emitMockTurnDone();
        return;
      }
      if (trimmedInput === "/process-preview" || trimmedInput === "process preview" || trimmedInput === "过程预览") {
        await delay(200);
        if (cancelled) return;
        emit({ kind: "phase", text: "Preparing context" });
        await delay(120);
        emit({ kind: "notice", level: "info", text: "Loaded project instructions from AGENTS.md." });
        await delay(120);
        emit({ kind: "notice", level: "warn", text: "Network access is enabled; external results may change over time." });
        await delay(120);
        emit({ kind: "compaction_started", compaction: { trigger: "manual" } });
        await delay(320);
        emit({
          kind: "compaction_done",
          compaction: {
            trigger: "manual",
            messages: 6,
            summary: "Preserved the active task, relevant files, and UI decisions while trimming earlier exploratory context.",
          },
        });
        emit({ kind: "message", text: "Process card preview complete." });
        emitMockTurnDone();
        return;
      }
      // Simulate the server's pre-first-token latency so the deferred user bubble
      // and the "un-send on Esc before any reply" path are observable in browser
      // dev. Bail if cancelled during the wait — nothing was streamed yet.
      await delay(700);
      if (cancelled) return;
      const reply =
        `You said: **${input}**\n\n` +
        "This is the browser dev mock — the real reply comes from the kernel " +
        "inside the Wails shell. Here's a fenced block to exercise the editor seam:\n\n" +
        "```go\nfunc main() {\n    println(\"hello from the mock\")\n}\n```\n";
      for (const ch of reply) {
        if (cancelled) break;
        emit({ kind: "text", text: ch });
        await delay(6);
      }
      emit({ kind: "message", text: reply });
      emit({
        kind: "tool_dispatch",
        tool: {
          id: "t1",
          name: "edit_file",
          args: '{"path":"main.go","old_string":"println(\\"hi\\")","new_string":"println(\\"hello\\")"}',
          readOnly: false,
        },
      });
      await delay(350);
      emit({
        kind: "tool_result",
        tool: { id: "t1", name: "edit_file", output: "edited main.go", readOnly: false, durationMs: 350 },
      });
      emit({
        kind: "usage",
        usage: {
          promptTokens: 1280,
          completionTokens: 64,
          totalTokens: 1344,
          cacheHitTokens: 1024,
          cacheMissTokens: 256,
          sessionCacheHitTokens: 1024,
          sessionCacheMissTokens: 256,
        },
      });
          emitMockTurnDone();
        },
        async SubmitToTab(_tabID, input) {
          await withMockTabScope(_tabID, () => this.Submit(input));
        },
        async SubmitDisplay(_display, input) {
          await this.Submit(input);
        },
        async SubmitDisplayToTab(_tabID, display, input) {
          await withMockTabScope(_tabID, () => this.SubmitDisplay(display, input));
        },
        async SubmitEditedDisplayToTab(_tabID, display, input, _original) {
          await withMockTabScope(_tabID, () => this.SubmitDisplay(display, input));
        },
        async SendWorkChat(_tabID, _workID, display, text) {
          await withMockTabScope(_tabID, () => this.SubmitDisplay(display, text));
        },
        async RunShell(command) {
          cancelled = false;
          emitMockTurnStarted();
          await delay(100);
          if (cancelled) return;
          const id = `shell-${command.slice(0, 32)}`;
          emit({ kind: "tool_dispatch", tool: { id, name: "bash", args: JSON.stringify({ command }), readOnly: false } });
          await delay(200);
          if (cancelled) return;
          emit({ kind: "tool_progress", tool: { id, name: "bash", output: `$ ${command}\n(mock output)\n`, readOnly: false } });
          await delay(100);
          if (cancelled) return;
          emit({ kind: "tool_result", tool: { id, name: "bash", output: `$ ${command}\n(mock output)\n`, readOnly: false, durationMs: 300 } });
          emitMockTurnDone();
        },
        async RunShellForTab(_tabID, command) {
          await withMockTabScope(_tabID, () => this.RunShell(command));
        },
        async Steer(_text) {
          // Mock: emit a steer event as confirmation in the transcript.
          emit({ kind: "steer", text: _text });
        },
        async SteerForTab(_tabID, _text) {
          await this.Steer(_text);
        },
        async Cancel() {
          cancelled = true;
          emitMockTurnDone();
        },
        async CancelTab(_tabID) {
          await withMockTabScope(_tabID, () => this.Cancel());
        },
        async Approve(_id, allow, session, persist) {
          if (!pendingApprovalPreview) return;
          pendingApprovalPreview = false;
          const suffix = persist ? "grant saved" : session ? "grant active this session" : "allowed once";
          emit({
            kind: "message",
            text: `approval preview answered: ${allow ? suffix : "denied"}`,
          });
          emitMockTurnDone();
        },
        async ApprovePending(allow) {
          await this.Approve("pending", allow, false, false);
        },
        async ApproveTab(_tabID, id, allow, session, persist) {
          await withMockTabScope(_tabID, () => this.Approve(id, allow, session, persist));
        },
        async AnswerQuestion(_id, answers) {
      if (!pendingAskPreview) return;
      pendingAskPreview = false;
      const summary = answers
        .map((answer) => `${answer.questionId}: ${(answer.selected ?? []).join(", ") || "(no answer)"}`)
        .join("\n");
      emit({ kind: "message", text: `ask preview answered:\n\n${summary}` });
          emitMockTurnDone();
        },
        async AnswerQuestionForTab(_tabID, id, answers) {
          await withMockTabScope(_tabID, () => this.AnswerQuestion(id, answers));
        },
        async ReplayPendingPromptsForSession() {},
        async ReplayPendingPrompts() {},
        async ConfirmAction(req) {
          void req;
          return false;
        },
        async SetPlanMode(on) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetModeForTab(active.id, modeWithPlan(normalizeMode(active.mode), on));
        },
        async SetMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetModeForTab(active.id, mode);
        },
        async SetModeForTab(tabID, mode) {
          const nextMode = normalizeMode(mode);
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID
              ? {
                  ...tab,
                  mode: nextMode,
                  collaborationMode: normalizeCollaborationMode(undefined, tab.goal, nextMode),
                  toolApprovalMode: mockToolApprovalModeAfterModeChange(tab.toolApprovalMode, nextMode),
                }
              : tab,
          );
        },
        async SetCollaborationMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetCollaborationModeForTab(active.id, mode);
        },
        async SetCollaborationModeForTab(tabID, mode) {
          const next = normalizeCollaborationMode(mode);
          mockTabs = mockTabs.map((tab) => {
            if (tab.id !== tabID) return tab;
            const toolMode = normalizeToolApprovalMode(tab.toolApprovalMode, normalizeMode(tab.mode));
            return {
              ...tab,
              collaborationMode: next,
              goal: next === "normal" || next === "plan" ? "" : tab.goal,
              mode: modeWithPlan(modeWithAutoApproveTools(normalizeMode(tab.mode), toolMode === "yolo"), next === "plan"),
            };
          });
        },
        async SetToolApprovalMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetToolApprovalModeForTab(active.id, mode);
        },
        async SetToolApprovalModeForTab(tabID, mode) {
          const next = normalizeToolApprovalMode(mode);
          settings.autoApproveTools = next === "yolo";
          settings.bypass = next === "yolo";
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID
              ? {
                  ...tab,
                  toolApprovalMode: next,
                  mode: modeWithAutoApproveTools(normalizeMode(tab.mode), next === "yolo"),
                }
              : tab,
          );
        },
        async SetGoal(goal) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetGoalForTab(active.id, goal);
        },
        async SetGoalForTab(tabID, goal) {
          const nextGoal = goal.trim();
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID
              ? {
                  ...tab,
                  goal: nextGoal,
                  goalStatus: nextGoal ? "running" : "stopped",
                  collaborationMode: nextGoal ? "goal" : "normal",
                  mode: modeWithPlan(normalizeMode(tab.mode), false),
                }
              : tab,
          );
        },
        async ClearGoal() {
          await this.SetGoal("");
        },
        async ClearGoalForTab(tabID) {
          await this.SetGoalForTab(tabID, "");
        },
        async Compact() {},
        async CompactForSession() {},
        async NewSession() {},
        async NewSessionForSession() {},
        async ClearSession() {},
        async ClearSessionForSession() {},
    async Checkpoints() {
      return [
        { turn: 0, prompt: "你好呀", files: ["src/App.tsx"], fileCount: 1, turnFileCount: 1, time: Date.now() - 30_000, canCode: true, canConversation: true },
      ];
    },
    async CheckpointsForTab() {
      return this.Checkpoints();
    },
    async Rewind() {},
    async RewindForSession() {},
    async Fork() {
      const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
      const tab: TabMeta = {
        ...active,
        id: "tab_fork_" + Date.now(),
        topicId: "topic_fork_" + Date.now(),
        topicTitle: `${active.topicTitle || t("rewind.fork")} · fork`,
        active: true,
        running: false,
      };
      mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), tab];
      return { ...tab };
    },
    async ForkForSession(_sessionID, turn) {
      return this.Fork(turn);
    },
    async SummarizeFrom() {},
    async SummarizeFromForSession() {},
    async SummarizeUpTo() {},
    async SummarizeUpToForSession() {},
        async History() {
          return [];
        },
        async HistoryForTab(tabID?: string) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active);
          if (tab?.topicId) {
            queueMockTopicRuntime(tab);
            return mockTopicHistory(tab.topicId);
          }
          return this.History();
        },
        async HistoryPage(beforeTurn = 0, limit = 60) {
          return mockHistoryPage(await this.History(), beforeTurn, limit);
        },
        async HistoryPageForTab(tabID: string, beforeTurn = 0, limit = 60) {
          return mockHistoryPage(await this.HistoryForTab(tabID), beforeTurn, limit);
        },
        async HistoryCheckpointTurnsForTab(tabID: string) {
          const turns: number[] = [];
          for (const message of await this.HistoryForTab(tabID)) {
            if (message.role !== "user") continue;
            turns.push(message.checkpointTurn ?? turns.length);
          }
          return turns;
        },
    async ListSessions() {
      return sessions.map((s) => ({ ...s }));
    },
    async ListSessionsForSession() {
      return this.ListSessions();
    },
    async ListTrashedSessions() {
      return trashedSessions.map((s) => ({ ...s }));
    },
    async ResumeSession(path: string) {
      sessions.forEach((s) => {
        s.current = s.path === path;
        s.open = s.open || s.path === path;
      });
      return [
        { role: "user", content: `(mock) resumed ${path}` },
        { role: "assistant", content: "This is a mock resumed transcript — the real one comes from the kernel." },
      ];
    },
	    async ResumeSessionForTab(_tabID: string, path: string) {
	      return this.ResumeSession(path);
	    },
	    async ResumeSessionPage(path: string, limit = 60) {
	      return mockHistoryPage(await this.ResumeSession(path), 0, limit);
	    },
	    async ResumeSessionPageForTab(_tabID: string, path: string, limit = 60) {
	      return this.ResumeSessionPage(path, limit);
	    },
	    async OpenChannelSessionForTab(tabID: string, path: string) {
	      mockTabs = mockTabs.map((tab) => tab.id === tabID ? { ...tab, sessionPath: path, readOnly: true } : tab);
	      return this.ResumeSession(path);
	    },
	    async OpenChannelSessionPageForTab(tabID: string, path: string, limit = 60) {
	      return mockHistoryPage(await this.OpenChannelSessionForTab(tabID, path), 0, limit);
	    },
	    async OpenChannelSession(path: string) {
	      const existing = mockTabs.find((tab) => tab.sessionPath === path);
	      if (existing) {
	        const active = { ...existing, active: true, readOnly: true };
	        mockTabs = mockTabs.map((tab) => tab.id === existing.id ? active : { ...tab, active: false });
	        return { ...active };
	      }
	      const tab: TabMeta = {
	        id: "tab_" + Date.now(),
	        scope: "global",
	        workspaceRoot: "",
	        workspaceName: "Global",
	        workspacePath: cwd,
	        topicId: "",
	        topicTitle: "Channel",
	        sessionPath: path,
	        label: mockModelLabel(settings.defaultModel),
	        ready: true,
	        running: false,
	        readOnly: true,
	        mode: "normal",
	        collaborationMode: "normal",
	        toolApprovalMode: normalizeToolApprovalMode(settings.defaultToolApprovalMode),
	        tokenMode: "full",
	        active: true,
	        cwd: "",
	      };
	      mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), tab];
	      return { ...tab };
	    },
	    async PreviewSession(path: string) {
      const s = sessions.find((x) => x.path === path) ?? trashedSessions.find((x) => x.path === path);
      return [
        { role: "user", content: s?.preview || `(mock) preview ${path}` },
        { role: "phase", content: "Preparing read-only preview" },
        {
          role: "assistant",
          content: "This is a read-only mock preview. The active conversation is unchanged.",
          reasoning: "Preview reads the saved session without resuming it.",
        },
        { role: "notice", level: "info", content: "Preview mode keeps the active conversation untouched." },
        { role: "compaction", content: "", trigger: "manual", messages: 3, summary: "Mock preview preserved the latest task, tool result, and answer summary." },
      ];
    },
    async DeleteSession(path: string) {
      const i = sessions.findIndex((s) => s.path === path);
      if (i >= 0) {
        const [s] = sessions.splice(i, 1);
        trashedSessions.unshift({
          ...s,
          current: false,
          open: false,
          path: s.path.replace("/mock/sessions/", "/mock/sessions/.trash/"),
          deletedAt: Date.now(),
        });
      }
    },
    async RestoreSession(path: string) {
      const i = trashedSessions.findIndex((s) => s.path === path);
      if (i >= 0) {
        const [s] = trashedSessions.splice(i, 1);
        sessions.unshift({
          ...s,
          path: s.path.replace("/mock/sessions/.trash/", "/mock/sessions/"),
          deletedAt: undefined,
        });
      }
    },
    async PurgeTrashedSession(path: string) {
      const i = trashedSessions.findIndex((s) => s.path === path);
      if (i >= 0) trashedSessions.splice(i, 1);
    },
    async RenameSession(path: string, title: string) {
      const s = sessions.find((x) => x.path === path);
      if (s) s.title = title.trim() || undefined;
    },
	    async ScanPromptHistory(nonce: string) {
	      // Dev mock returns a static set of sample prompts for UI development.
	      const entries: PromptHistoryEntry[] = [
	        { text: "Explain the architecture of this project", at: Date.now() - 60000, sessionPath: "/mock/sessions/arch.jsonl", turn: 0 },
	        { text: "Fix the login button styling", at: Date.now() - 120000, sessionPath: "/mock/sessions/arch.jsonl", turn: 1 },
	        { text: "What is the capital of France?", at: Date.now() - 300000, sessionPath: "/mock/sessions/general.jsonl", turn: 0 },
	      ];
	      return { entries, nonce: "mock-" + nonce, olderCursor: "", hasOlder: false };
	    },
    async ListWorkspaces() {
      return mockProjectTree
        .filter((node) => node.kind === "project" && node.root)
        .map((node) => ({
          path: node.root!,
          name: node.label || baseName(node.root!),
          current: node.root === cwd,
        }));
    },
    async PickWorkspace() {
      // Browser dev has no native dialog; simulate picking a folder and re-root so
      // the topbar folder chip visibly changes.
      return mockSwitchWorkspace(cwd.endsWith("another-project") ? "~/projects/WorkGround2" : "~/projects/another-project");
    },
    async SwitchWorkspace(path: string) {
      return mockSwitchWorkspace(path);
    },
    async RemoveWorkspace(path: string) {
      workspaces = workspaces.filter((p) => p !== path);
      const index = mockProjectTree.findIndex((node) => node.root === path);
      if (index >= 0) mockProjectTree.splice(index, 1);
    },
        async ContextUsage() {
          return { used: 42124, window: 128000, sessionTokens: 34479, compactRatio: 0.8 };
        },
        async ContextUsageForTab() {
          return this.ContextUsage();
        },
        async Balance() {
      // Mirror the active mock provider: deepseek-flash carries a balance_url.
      const p = settings.providers.find((x) => x.name === settings.defaultModel);
      if (!p?.balanceUrl) return { available: false, display: "" };
          return { available: true, display: "¥128.50" };
        },
        async BalanceForTab() {
          return this.Balance();
        },
        async Jobs() {
          return []; // browser dev mock has no background jobs
        },
        async JobsForTab() {
          return this.Jobs();
        },
        async ToolResultForTab() {
          return null;
        },
        async ArtifactsForTab() {
          return [];
        },
        async Meta() {
          const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
          const toolApprovalMode = normalizeToolApprovalMode(active?.toolApprovalMode, active ? normalizeMode(active.mode) : "normal", settings.autoApproveTools);
          const autoApproveTools = toolApprovalMode === "yolo";
          const collaborationMode = normalizeCollaborationMode(active?.collaborationMode, active?.goal, active ? normalizeMode(active.mode) : "normal");
          const workspacePath = active?.workspacePath || active?.workspaceRoot || active?.cwd || cwd;
          return {
            label: active?.label ?? "DeepSeek-R1",
            ready: active?.ready ?? true,
            eventChannel: EVENT_CHANNEL,
            cwd: active?.cwd || cwd,
            workspaceRoot: active?.workspaceRoot || workspacePath,
            workspaceName: active?.workspaceName,
            workspacePath,
            sandboxPath: settings.sandbox.workspaceRoot,
            gitBranch: active?.gitBranch || (active?.scope === "project" ? "main" : ""),
            imageInputEnabled: true,
            autoApproveTools,
            bypass: autoApproveTools,
            collaborationMode,
            toolApprovalMode,
            tokenMode: normalizeTokenMode(active?.tokenMode),
            goal: active?.goal ?? "",
            goalStatus: active?.goalStatus ?? (active?.goal ? "running" : "stopped"),
            autoResearch: active?.goal ? { taskId: "mock-autoresearch", status: "running", iteration: 4, pivotRequired: false, staleCount: 0 } : undefined,
          };
        },
        async MetaForTab(tabID) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active) ?? mockTabs[0];
          const toolApprovalMode = normalizeToolApprovalMode(tab?.toolApprovalMode, tab ? normalizeMode(tab.mode) : "normal", settings.autoApproveTools);
          const autoApproveTools = toolApprovalMode === "yolo";
          const collaborationMode = normalizeCollaborationMode(tab?.collaborationMode, tab?.goal, tab ? normalizeMode(tab.mode) : "normal");
          const workspacePath = tab?.workspacePath || tab?.workspaceRoot || tab?.cwd || cwd;
          return {
            label: tab?.label ?? "DeepSeek-R1",
            ready: tab?.ready ?? true,
            eventChannel: EVENT_CHANNEL,
            cwd: tab?.cwd || cwd,
            workspaceRoot: tab?.workspaceRoot || workspacePath,
            workspaceName: tab?.workspaceName,
            workspacePath,
            sandboxPath: settings.sandbox.workspaceRoot,
            gitBranch: tab?.gitBranch || (tab?.scope === "project" ? "main" : ""),
            autoApproveTools,
            bypass: autoApproveTools,
            collaborationMode,
            toolApprovalMode,
            tokenMode: normalizeTokenMode(tab?.tokenMode),
            goal: tab?.goal ?? "",
            goalStatus: tab?.goalStatus ?? (tab?.goal ? "running" : "stopped"),
            autoResearch: tab?.goal ? { taskId: "mock-autoresearch", status: "running", iteration: 4, pivotRequired: false, staleCount: 0 } : undefined,
          };
        },
        async AutoResearchCurrent() {
          return {
            taskId: "mock-autoresearch",
            goal: "Mock long-running research",
            status: "running",
            iteration: 4,
            currentDirection: "Inspect status chip",
            staleCount: 0,
            pivotCount: 0,
            pivotRequired: false,
            lastHeartbeatAt: "2026-06-29T00:00:00Z",
            findingCount: 1,
            openCriteria: [],
            blocker: "",
            taskPath: "/tmp/mock/.WorkGround2/autoresearch/mock-autoresearch",
            nextRequiredAction: "continue with the next evidence-producing step",
          };
        },
        async AutoResearchStatus(_tabID) {
          return {
            taskId: "mock-autoresearch",
            goal: "Mock long-running research",
            status: "running",
            iteration: 4,
            currentDirection: "Inspect status chip",
            staleCount: 0,
            pivotCount: 0,
            pivotRequired: false,
            lastHeartbeatAt: "2026-06-29T00:00:00Z",
            findingCount: 1,
            openCriteria: [],
            blocker: "",
            taskPath: "/tmp/mock/.WorkGround2/autoresearch/mock-autoresearch",
            nextRequiredAction: "continue with the next evidence-producing step",
          };
        },
        async AutoResearchList(_tabID) {
          return [{
            taskId: "mock-autoresearch",
            goal: "Mock long-running research",
            status: "running",
            iteration: 4,
            currentDirection: "Inspect status chip",
            staleCount: 0,
            pivotCount: 0,
            pivotRequired: false,
            lastHeartbeatAt: "2026-06-29T00:00:00Z",
            findingCount: 1,
            openCriteria: [],
            blocker: "",
            taskPath: "/tmp/mock/.WorkGround2/autoresearch/mock-autoresearch",
            nextRequiredAction: "continue with the next evidence-producing step",
          }];
        },
        async AutoResearchFindings(_tabID, limit) {
          return [{
            id: "f1",
            kind: "test",
            summary: "Mock accepted finding",
            source: "command",
            command: "go test ./...",
            accepted: true,
            createdAt: "2026-06-29T00:00:00Z",
          }].slice(0, Math.max(0, limit || 1));
        },
        async AutoResearchOpenTask(_tabID) {
          console.info("mock AutoResearchOpenTask");
        },
        async AutoResearchRecordEvidence(_tabID, _criterionID, _input) {
          console.info("mock AutoResearchRecordEvidence");
        },
    async Commands() {
      return [
        { name: "new", description: "start new session; save transcript", kind: "builtin" as const },
        { name: "clear", description: "discard current context", kind: "builtin" as const },
        { name: "compact", description: "Summarize older history to free up context", kind: "builtin" as const },
        { name: "model", description: "Switch model", kind: "builtin" as const },
        { name: "effort", description: "Set reasoning effort", kind: "builtin" as const },
        { name: "skill", description: "List skills", kind: "builtin" as const },
        { name: "explore", description: "Investigate the codebase in an isolated subagent", kind: "skill" as const },
        { name: "review", description: "Review the staged diff", hint: "[focus]", kind: "custom" as const },
      ];
    },
    async Capabilities() {
      return {
        servers: capServers.map((s) => ({ ...s })),
        skills: capSkills.map((s) => ({ ...s })),
        skillRoots: capSkillRoots.map((s) => ({ ...s })),
        plugins: capPlugins.map((p) => ({ ...p })),
      };
    },
    async MCPServers() {
      return capServers.map((s) => ({ ...s }));
    },
    async SkillsSettings() {
      return {
        skills: capSkills.map((s) => ({ ...s })),
        skillRoots: capSkillRoots.map((s) => ({ ...s })),
      };
    },
    async Plugins() {
      return capPlugins.map((p) => ({ ...p }));
    },
    async AddOnPanelSchema(name: string, panelID: string) {
      const plugin = capPlugins.find((item) => item.name === name);
      const panel = plugin?.addon?.panels?.find((item) => item.id === panelID);
      if (!plugin?.addon || !panel) throw new Error(`addon panel ${name}/${panelID} not found`);
      const namespace = plugin.addon.storageNamespace || plugin.addon.kind || plugin.name;
      if (panelID === "sources") return JSON.stringify(mockSkillSharePanelSchema(panel.title || "Sources", namespace, `${namespace}/profiles.json`));
      if (panelID === "providers") return JSON.stringify(mockDrawAddonPanelSchema(panel.title || "Providers", namespace, `${namespace}/config.json`));
      return JSON.stringify({
        version: 1,
        title: panel.title || panelID,
        storage: { namespace: plugin.addon.storageNamespace || plugin.name },
        sections: [{ id: panelID, title: panel.title || panelID, kind: "metadata" }],
      });
    },
    async AddOnPanelQuery(_pluginName: string, _panelID: string, adapter: string) {
      if (adapter.endsWith("/profiles.json")) {
        const profiles = (adapter.startsWith("flow") ? flowSkillShareProfiles : skillShareProfiles) as unknown as Record<string, unknown>[];
        return { records: profiles, form: {} };
      }
      if (adapter.endsWith("/config.json")) {
        return { records: drawAddonProviders as unknown as Record<string, unknown>[], form: {} };
      }
      return { records: [], form: {} };
    },
    async AddOnPanelAction(_pluginName: string, _panelID: string, adapter: string, action: AddOnPanelActionInput) {
      const { actionId, form, recordId, extra } = action;
      if (adapter.endsWith("/profiles.json")) {
        const profiles = adapter.startsWith("flow") ? flowSkillShareProfiles : skillShareProfiles;
        const useFlow = adapter.startsWith("flow");
        const key = (v: SkillShareProfileView) => v.id;
        if (actionId === "save" && form) {
          const input: SkillShareProfileInput = {
            id: String(form.id || ""), enabled: Boolean(form.enabled), displayName: String(form.displayName || ""),
            gitUrl: String(form.gitUrl || ""), branch: String(form.branch || ""), path: String(form.path || ""),
            username: String(form.username || ""), secretRef: String(form.secretRef || ""),
            pluginName: String(form.pluginName || ""), update: form.update as SkillShareUpdateOptions,
          };
          const view = normalizeMockSkillShareProfile(input, "");
          const exist = profiles.findIndex((p) => p.id === view.id);
          if (exist >= 0) profiles[exist] = view; else profiles.push(view);
          const sorted = [...profiles].sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: "base" }));
          if (useFlow) flowSkillShareProfiles = sorted; else skillShareProfiles = sorted;
          return {};
        }
        if (actionId === "edit" && recordId) {
          const p = profiles.find((v) => key(v) === recordId);
          if (p) Object.assign(p, form || {});
          return {};
        }
        if (actionId === "sync" && recordId) {
          const p = profiles.find((v) => key(v) === recordId);
          if (!p) return { error: `profile ${recordId} not found` };
          const now = new Date().toISOString();
          p.state = { status: "ready", currentRevision: extra?.force ? "mock-force" : "mock-rev", lastCheckedAt: now, lastUpdatedAt: now, lastError: "" };
          p.manifestKind = p.manifestKind || "codex"; p.version = p.version || "dev"; p.skills = p.skills ?? 1;
          return {};
        }
        if ((actionId === "delete" || actionId === "delete-secret") && recordId) {
          const idx = profiles.findIndex((v) => key(v) === recordId);
          if (idx >= 0) profiles.splice(idx, 1);
          if (useFlow) flowSkillShareProfiles = [...profiles]; else skillShareProfiles = [...profiles];
          return {};
        }
        return {};
      }
      if (adapter.endsWith("/config.json")) {
        if (actionId === "save" && form) {
          const input: DrawAddonProviderInput = {
            id: String(form.id || ""), enabled: Boolean(form.enabled), displayName: String(form.displayName || ""),
            mode: String(form.mode || "api"), baseUrl: String(form.baseUrl || ""), model: String(form.model || ""),
            apiKeyRef: String(form.apiKeyRef || ""), cliCommand: String(form.cliCommand || ""),
            cliArgs: (form.cliArgs as string[]) || [], outputDir: String(form.outputDir || ""),
          };
          const view = normalizeMockDrawAddonProvider(input, "");
          const exist = drawAddonProviders.findIndex((p) => p.id === view.id);
          if (exist >= 0) drawAddonProviders[exist] = view; else drawAddonProviders.push(view);
          drawAddonProviders = [...drawAddonProviders].sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: "base" }));
          return {};
        }
        if (actionId === "edit" && recordId) {
          const p = drawAddonProviders.find((v) => v.id === recordId);
          if (p) Object.assign(p, form || {});
          return {};
        }
        if (actionId === "delete" && recordId) {
          drawAddonProviders = drawAddonProviders.filter((v) => v.id !== recordId);
          return {};
        }
        if (actionId === "generate" && form) {
          const taskId = `${form.providerId || recordId || "draw"}-${Date.now()}`;
          return { notice: `Generated: ${taskId}` };
        }
        return {};
      }
      return {};
    },
    async GetPendingAddOnDialog() {
      // Non-Wails mock: return null (no pending dialogs in dev mode)
      return null;
    },
    async DismissAddOnDialog(_pluginName: string, _panelID: string, _submitted: boolean, _form: Record<string, unknown>, _actionID: string) {
      // Non-Wails mock: no-op
    },
    async WaitAddOnDialogChange() {
      // Non-Wails mock: no pending dialogs; resolve immediately
    },
    async AddOnDialogQuery(_pluginName: string, _panelID: string) {
      // Non-Wails mock: return empty
      return { records: [], form: {} };
    },
    async AddOnDialogAction(_pluginName: string, _panelID: string, _action: AddOnPanelActionInput) {
      // Non-Wails mock: no-op
      return {};
    },
    async TriggerAddOnDialog(_mcpServer: string, _pluginName: string, _panelID: string, _message: string) {
      // Non-Wails mock: simulate dismissed without submission
      return { submitted: false };
    },
    async SkillShareProfiles() {
      return skillShareProfiles.map((profile) => ({ ...profile, update: { ...profile.update }, state: { ...profile.state } }));
    },
    async SaveSkillShareProfile(input: SkillShareProfileInput, secretValue = "") {
      const view = normalizeMockSkillShareProfile(input, secretValue);
      const existing = skillShareProfiles.findIndex((profile) => profile.id === view.id);
      if (existing >= 0) skillShareProfiles[existing] = view;
      else skillShareProfiles.push(view);
      skillShareProfiles = [...skillShareProfiles].sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: "base" }));
      return { ...view, update: { ...view.update }, state: { ...view.state } };
    },
    async SyncSkillShareProfile(id: string, options: SkillShareSyncOptions) {
      const profile = skillShareProfiles.find((item) => item.id === id.trim());
      if (!profile) throw new Error(`skill share profile ${id} not found`);
      const now = new Date().toISOString();
      const task: SkillShareTaskView = {
        taskId: `${profile.id}-${Date.now()}`,
        profileId: profile.id,
        trigger: options.trigger || "manual",
        phase: profile.enabled ? "ready" : "disabled",
        status: "succeeded",
        startedAt: now,
        finishedAt: now,
        currentRevision: profile.state.currentRevision || "mock-revision",
        targetRevision: options.force ? "mock-revision-force" : "mock-revision",
        retryable: false,
      };
      profile.state = {
        status: profile.enabled ? "ready" : "disabled",
        currentRevision: task.targetRevision,
        lastCheckedAt: now,
        lastUpdatedAt: profile.enabled ? now : profile.state.lastUpdatedAt,
        lastError: "",
      };
      if (profile.enabled) {
        profile.manifestKind = profile.manifestKind || "codex";
        profile.version = profile.version || "dev";
        profile.skills = profile.skills ?? 1;
        profile.hooks = profile.hooks ?? 0;
        profile.mcpServers = profile.mcpServers ?? 0;
        const pluginName = profile.pluginName || profile.id;
        profile.pluginName = pluginName;
        const existing = capPlugins.findIndex((plugin) => plugin.name === pluginName);
        const plugin: PluginView = {
          name: pluginName,
          version: profile.version,
          description: profile.displayName || "Skill Share",
          source: `skill-share:${profile.id}:${profile.gitUrl}`,
          root: `~/.WorkGround2/addons/skill-share/profiles/${profile.id}/active/${profile.path || "."}`,
          manifestKind: profile.manifestKind,
          enabled: true,
          skills: profile.skills,
          hooks: profile.hooks,
          mcpServers: profile.mcpServers,
        };
        if (existing >= 0) capPlugins[existing] = plugin;
        else capPlugins.push(plugin);
      }
      return task;
    },
    async DeleteSkillShareProfile(id: string, _options: SkillShareDeleteOptions) {
      const profile = skillShareProfiles.find((item) => item.id === id.trim());
      skillShareProfiles = skillShareProfiles.filter((item) => item.id !== id.trim());
      const pluginNames = new Set([profile?.pluginName, id.trim()].filter(Boolean));
      capPlugins = capPlugins.filter((plugin) => !pluginNames.has(plugin.name));
      return profile || {
        id: id.trim(),
        enabled: false,
        gitUrl: "",
        authStatus: "none",
        state: { status: "disabled" },
      };
    },
    async RecoverSkillShareProfiles() {
      return [];
    },
    async FlowSkillShareProfiles() {
      return flowSkillShareProfiles.map((profile) => ({ ...profile, update: { ...profile.update }, state: { ...profile.state } }));
    },
    async SaveFlowSkillShareProfile(input: SkillShareProfileInput, secretValue = "") {
      const view = normalizeMockFlowSkillShareProfile(input, secretValue);
      const existing = flowSkillShareProfiles.findIndex((profile) => profile.id === view.id);
      if (existing >= 0) flowSkillShareProfiles[existing] = view;
      else flowSkillShareProfiles.push(view);
      flowSkillShareProfiles = [...flowSkillShareProfiles].sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: "base" }));
      return { ...view, update: { ...view.update }, state: { ...view.state } };
    },
    async SyncFlowSkillShareProfile(id: string, options: SkillShareSyncOptions) {
      const profile = flowSkillShareProfiles.find((item) => item.id === id.trim());
      if (!profile) throw new Error(`flow skill share profile ${id} not found`);
      const now = new Date().toISOString();
      const revision = options.force ? `mock-flow-revision-${Date.now()}` : profile.state.currentRevision || "mock-flow-revision";
      const task: SkillShareTaskView = {
        taskId: `${profile.id}-${Date.now()}`,
        profileId: profile.id,
        trigger: options.trigger || "manual",
        phase: profile.enabled ? "ready" : "disabled",
        status: "succeeded",
        startedAt: now,
        finishedAt: now,
        currentRevision: revision,
        targetRevision: revision,
        retryable: false,
      };
      profile.state = {
        status: profile.enabled ? "ready" : "disabled",
        currentRevision: revision,
        lastCheckedAt: now,
        lastUpdatedAt: profile.enabled ? now : profile.state.lastUpdatedAt,
        lastError: "",
      };
      if (profile.enabled) {
        profile.manifestKind = profile.manifestKind || "WorkGround2";
        profile.version = profile.version || "remote";
        profile.skills = profile.skills ?? 1;
        profile.hooks = profile.hooks ?? 0;
        profile.mcpServers = profile.mcpServers ?? 0;
      }
      return task;
    },
    async DeleteFlowSkillShareProfile(id: string, _options: SkillShareDeleteOptions) {
      const profile = flowSkillShareProfiles.find((item) => item.id === id.trim());
      flowSkillShareProfiles = flowSkillShareProfiles.filter((item) => item.id !== id.trim());
      return profile || {
        id: id.trim(),
        enabled: false,
        gitUrl: "",
        authStatus: "none",
        state: { status: "disabled" },
      };
    },
    async RecoverFlowSkillShareProfiles() {
      return [];
    },
    async DrawAddonProviders() {
      return drawAddonProviders.map(cloneDrawAddonProvider);
    },
    async SaveDrawAddonProvider(input: DrawAddonProviderInput, secretValue = "") {
      const view = normalizeMockDrawAddonProvider(input, secretValue);
      const existing = drawAddonProviders.findIndex((provider) => provider.id === view.id);
      if (existing >= 0) drawAddonProviders[existing] = view;
      else drawAddonProviders.push(view);
      drawAddonProviders = [...drawAddonProviders].sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: "base" }));
      return cloneDrawAddonProvider(view);
    },
    async DeleteDrawAddonProvider(id: string) {
      const provider = drawAddonProviders.find((item) => item.id === id.trim());
      drawAddonProviders = drawAddonProviders.filter((item) => item.id !== id.trim());
      return provider ? cloneDrawAddonProvider(provider) : {
        id: id.trim(),
        enabled: false,
        mode: "api",
        authStatus: "none",
        cliArgs: [],
        state: { status: "disabled" },
      };
    },
    async GenerateImageWithDrawAddon(input: DrawAddonGenerateInput) {
      const provider = drawAddonProviders.find((item) => item.id === input.providerId.trim());
      if (!provider) throw new Error(`draw addon provider ${input.providerId} not found`);
      const now = new Date().toISOString();
      const task: DrawAddonTaskView = {
        taskId: `${provider.id}-${Date.now()}`,
        providerId: provider.id,
        status: provider.enabled ? "succeeded" : "failed",
        phase: provider.mode === "cli" ? "done" : "api_dry_run",
        startedAt: now,
        finishedAt: now,
        prompt: input.prompt,
        outputPath: provider.mode === "cli" ? `${provider.outputDir || "~/.WorkGround2/addons/draw-tool/outputs"}/${provider.id}-mock.png` : undefined,
        error: provider.enabled ? undefined : "provider is disabled",
        retryable: !provider.enabled,
      };
      provider.state = {
        status: task.status === "succeeded" ? "ready" : "failed",
        lastTaskId: task.taskId,
        lastStartedAt: task.startedAt,
        lastFinishedAt: task.finishedAt,
        lastOutputPath: task.outputPath,
        lastError: task.error || "",
      };
      return { ...task };
    },
    async PlanPluginInstall(source: string, options: PluginInstallOptions) {
      const name = options.name || source.split("/").filter(Boolean).pop()?.replace(/\.git$/, "") || "plugin";
      return JSON.stringify({
        ok: true,
        status: "planned",
        kind: "plugin",
        actions: [{ kind: "plugin", action: "install_plugin_package", name, source, status: "planned" }],
      });
    },
    async InstallPlugin(source: string, options: PluginInstallOptions) {
      const name = options.name || source.split("/").filter(Boolean).pop()?.replace(/\.git$/, "") || "plugin";
      const existing = capPlugins.findIndex((p) => p.name === name);
      const view: PluginView = {
        name,
        version: "dev",
        description: "Mock plugin",
        source,
        root: `~/.WorkGround2/plugins/${name}`,
        manifestKind: "WorkGround2",
        enabled: true,
        skills: 1,
        hooks: 0,
        mcpServers: 0,
      };
      if (existing >= 0) capPlugins[existing] = view;
      else capPlugins.push(view);
      return JSON.stringify({ ok: true, status: "done", kind: "plugin", actions: [{ kind: "plugin", name }] });
    },
    async RemovePlugin(name: string) {
      capPlugins = capPlugins.filter((p) => p.name !== name);
    },
    async SetPluginEnabled(name: string, enabled: boolean) {
      capPlugins = capPlugins.map((p) => p.name === name ? { ...p, enabled } : p);
    },
    async UpdatePlugin(name: string) {
      capPlugins = capPlugins.map((p) => p.name === name ? { ...p, version: p.remoteVersion || p.version || "dev", updateAvailable: false, remoteVersion: undefined } : p);
      return JSON.stringify({ ok: true, status: "done", kind: "plugin", name });
    },
    async PluginDoctor(name: string) {
      return capPlugins.find((p) => p.name === name) || {
        name,
        root: "",
        enabled: false,
        skills: 0,
        hooks: 0,
        mcpServers: 0,
        error: "plugin is not installed",
      };
    },
		async StartDSHWorkbench(name: string) {
			return { pluginName: name, url: "http://127.0.0.1:3080", status: "ready" as const, startedAt: new Date().toISOString() };
		},
		async DSHWorkbench(name: string) {
			return { pluginName: name, status: "stopped" as const };
		},
		async StopDSHWorkbench(name: string) {
			return { pluginName: name, status: "stopped" as const };
		},
    async AddMCPServer(input: MCPServerInput) {
      const tools = input.transport === "stdio" ? 3 : 5;
      capServers.push({
        name: input.name,
        transport: input.transport,
        status: "connected",
        configured: true,
        autoStart: true,
        tier: "background",
        command: input.command,
        args: input.args,
        url: input.url,
        envKeys: input.env ? Object.keys(input.env).sort() : undefined,
        headerKeys: input.headers ? Object.keys(input.headers).sort() : undefined,
        tools,
        prompts: 0,
        resources: 0,
        toolList: Array.from({ length: tools }, (_, i) => ({
          name: `${input.name}_tool_${i + 1}`,
          description: `Mock tool ${i + 1} exposed by ${input.name}.`,
        })),
      });
      return tools;
    },
    async UpdateMCPServer(name: string, input: MCPServerInput) {
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        const connected = s.status === "connected" || s.status === "failed" || s.autoStart !== false;
        const nextStatus = s.status === "disabled" ? "disabled" : connected ? "connected" : "deferred";
        const nextTools = nextStatus === "connected" ? s.tools || (input.transport === "stdio" ? 3 : 5) : 0;
        return {
          ...s,
          transport: input.transport,
          status: nextStatus,
          command: input.transport === "stdio" ? input.command : "",
          args: input.transport === "stdio" ? input.args : [],
          url: input.transport === "stdio" ? "" : input.url,
          envKeys: input.env ? Object.keys(input.env).sort() : s.envKeys,
          headerKeys: input.headers ? Object.keys(input.headers).sort() : s.headerKeys,
          trustedReadOnlyTools: input.trustedReadOnlyTools ?? s.trustedReadOnlyTools,
          tools: nextTools,
          error: undefined,
          authStatus: nextStatus !== "connected" && input.transport !== "stdio" ? "possible" : undefined,
          authUrl: nextStatus !== "connected" && input.transport !== "stdio" ? input.url : undefined,
        };
      });
    },
    async RemoveMCPServer(name: string) {
      capServers = capServers.filter((s) => s.name !== name);
    },
    async ReconnectMCPServer(name: string) {
      capServers = capServers.map((s) =>
        s.name === name
          ? { ...s, status: "initializing", error: undefined, authStatus: undefined, authUrl: undefined }
          : s,
      );
      await new Promise((r) => setTimeout(r, 400));
      capServers = capServers.map((s) =>
        s.name === name ? { ...s, status: "connected", tools: s.tools || 4 } : s,
      );
    },
    async ClearMCPServerAuthentication(name: string) {
      capServers = capServers.map((s) =>
        s.name === name
          ? {
              ...s,
              status: s.autoStart === false ? "disabled" : "initializing",
              tools: 0,
              error: undefined,
              authStatus: s.transport !== "stdio" ? "possible" : undefined,
              authUrl: s.transport !== "stdio" ? s.url : undefined,
              authConfigured: undefined,
            }
          : s,
      );
    },
    async TrustMCPServerTool(name: string, toolName: string) {
      const normalizedTool = toolName.trim();
      if (!normalizedTool) return;
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        const trusted = Array.from(new Set([...(s.trustedReadOnlyTools ?? []), normalizedTool]));
        return { ...s, trustedReadOnlyTools: trusted };
      });
    },
    async TrustMCPServerTools(name: string, toolNames: string[]) {
      const normalizedTools = toolNames.map((tool) => tool.trim()).filter(Boolean);
      if (normalizedTools.length === 0) return;
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        const trusted = Array.from(new Set([...(s.trustedReadOnlyTools ?? []), ...normalizedTools]));
        return { ...s, trustedReadOnlyTools: trusted };
      });
    },
    async UntrustMCPServerTool(name: string, toolName: string) {
      const normalizedTool = toolName.trim();
      if (!normalizedTool) return;
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        const trusted = (s.trustedReadOnlyTools ?? []).filter((tool) => tool !== normalizedTool);
        return { ...s, trustedReadOnlyTools: trusted };
      });
    },
    async PickSkillFolder() {
      return "~/my-skills";
    },
    async PickPluginArchive() {
      return "~/plugins/superpowers.zip";
    },
    async PickPluginFolder() {
      return "~/plugins/superpowers";
    },
    async AddSkillPath(path: string) {
      const dir = path.trim() || "~/my-skills";
      if (!capSkillRoots.some((r) => r.scope === "custom" && r.dir === dir)) {
        capSkillRoots.push({
          dir,
          scope: "custom",
          priority: capSkillRoots.length + 1,
          status: "ok",
          configured: true,
          removable: true,
          skills: 1,
          skillItems: [{ name: "local-dev", description: "Local custom development workflow", scope: "custom", runAs: "inline" }],
        });
      }
      if (!capSkills.some((s) => s.name === "local-dev")) {
        capSkills.push({ name: "local-dev", description: "Local custom development workflow", scope: "custom", runAs: "inline", enabled: true });
      }
    },
    async RemoveSkillPath(path: string) {
      capSkillRoots = capSkillRoots.filter((r) => r.dir !== path);
      if (!capSkillRoots.some((r) => r.scope === "custom")) {
        const idx = capSkills.findIndex((s) => s.name === "local-dev");
        if (idx >= 0) capSkills.splice(idx, 1);
      }
    },
    async RefreshSkills() {},
    async ReloadCommands() {},
    async SetSkillEnabled(name: string, enabled: boolean) {
      const skill = capSkills.find((s) => s.name === name);
      if (skill) skill.enabled = enabled;
    },
    async SetMCPServerEnabled(name: string, enabled: boolean) {
      capServers = capServers.map((s) =>
        s.name === name
          ? {
              ...s,
              status: enabled ? "connected" : "disabled",
              autoStart: s.builtIn ? enabled : s.autoStart,
              tools: enabled ? s.tools || 4 : 0,
              error: undefined,
              authStatus: !enabled && s.transport !== "stdio" ? "possible" : undefined,
              authUrl: !enabled && s.transport !== "stdio" ? s.url : undefined,
            }
          : s,
      );
    },
    async SetMCPServerTier(name: string, tier: string) {
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        const tools = s.tools || (s.transport === "stdio" ? 3 : 5);
        return { ...s, tier, autoStart: true, status: "connected", tools, error: undefined, authStatus: undefined, authUrl: undefined };
      });
    },
    async SlashArgs(input: string) {
      // Mirror a slice of the real arg hints so the menu is exercisable in browser dev.
      const from = input.lastIndexOf(" ") + 1;
      const cur = input.slice(from);
      const cmd = input.slice(0, input.indexOf(" ") < 0 ? input.length : input.indexOf(" "));
      const subs: Record<string, { label: string; insert: string; hint: string; descend?: boolean }[]> = {
        "/skill": [
          { label: "list", insert: "list", hint: "list skills" },
          { label: "show", insert: "show ", hint: "show a skill's body", descend: true },
          { label: "enable", insert: "enable ", hint: "enable a disabled skill", descend: true },
          { label: "disable", insert: "disable ", hint: "disable an enabled skill", descend: true },
          { label: "new", insert: "new ", hint: "scaffold a new skill" },
          { label: "paths", insert: "paths", hint: "show discovery paths" },
        ],
        "/hooks": [
          { label: "list", insert: "list", hint: "list active hooks" },
          { label: "trust", insert: "trust", hint: "trust this project's hooks" },
        ],
        "/model": [
          { label: "deepseek/deepseek-v4-flash", insert: "deepseek/deepseek-v4-flash", hint: "current" },
          { label: "deepseek/deepseek-v4-pro", insert: "deepseek/deepseek-v4-pro", hint: "" },
        ],
        "/effort": [
          { label: "auto", insert: "auto", hint: "use the model default" },
          { label: "high", insert: "high", hint: "deeper reasoning" },
          { label: "max", insert: "max", hint: "maximum reasoning" },
        ],
      };
      const items = (subs[cmd] ?? [])
        .filter((it) => it.label.toLowerCase().startsWith(cur.toLowerCase()))
        .map((it) => ({ label: it.label, insert: it.insert, hint: it.hint, descend: it.descend ?? false }));
      return { items, from };
    },
    async CompleteVocabularyForTab(_tabID: string, prefix: string, limit: number) {
      const terms: VocabularyMatch[] = [
        { id: "mock-video-v5", text: "多模态生视频V5", suffix: "", kind: "noun", source: "workspace" },
        { id: "mock-role-pro", text: "角色设定Pro", suffix: "", kind: "noun", source: "skill" },
      ];
      const needle = prefix.toLowerCase();
      return terms
        .filter((term) => term.text.toLowerCase().startsWith(needle) && term.text.length > prefix.length)
        .slice(0, limit)
        .map((term) => ({ ...term, suffix: term.text.slice(prefix.length) }));
    },
    async RecordVocabularyUseForTab(_tabID: string, _id: string, _useID: string) {},
    async ActivateSkillVocabularyForTab(_tabID: string, name: string) {
      return { skill: name, termCount: 2, added: 2, warnings: [] };
    },
    async CompleteVocabulary(prefix: string, limit: number) {
      return this.CompleteVocabularyForTab("mock-active", prefix, limit);
    },
    async RecordVocabularyUse(id: string, useID: string) {
      return this.RecordVocabularyUseForTab("mock-active", id, useID);
    },
    async ListDir(rel: string) {
      // A tiny fake tree so the @ menu is navigable in browser dev.
      if (rel === "" || rel === "./") {
        return [
          { name: "internal", isDir: true },
          { name: "desktop", isDir: true },
          { name: "README.md", isDir: false },
          { name: "go.mod", isDir: false },
        ];
      }
      if (rel === "internal/") {
        return [
          { name: "control", isDir: true },
          { name: "boot", isDir: true },
          { name: "event.go", isDir: false },
        ];
      }
      return [{ name: "file.go", isDir: false }];
    },
    async ListDirForTab(_tabID: string, rel: string) {
      return this.ListDir(rel);
    },
    async ListDirForWorkspace(_root: string, rel: string) {
      return this.ListDir(rel);
    },
    async SearchFileRefs(query: string) {
      const q = query.toLowerCase();
      return ["desktop/frontend/src/lib/bridge.ts", "frontend/wailsjs/runtime/runtime.js", "internal/control/refs.go"]
        .filter((path) => path.split("/").pop()?.toLowerCase().includes(q))
        .map((name) => ({ name, isDir: false }));
    },
    async SearchFileRefsForTab(_tabID: string, query: string) {
      return this.SearchFileRefs(query);
    },
    async SearchFileRefsForWorkspace(_root: string, query: string) {
      return this.SearchFileRefs(query);
    },
    async ReadFile(rel: string) {
      const samples: Record<string, string> = {
        "README.md": "# WorkGround2\n\nBrowser-dev workspace preview.\n\n- Chat in the center\n- Browse files on the right\n- Keep sessions on the left\n",
        "go.mod": "module WorkGround2\n\ngo 1.23\n",
        "desktop/file.go": "package desktop\n\nfunc main() {\n\tprintln(\"workspace preview\")\n}\n",
        "internal/event.go": "package internal\n\n// mock file used by the browser dev seam\n",
      };
      return {
        path: rel,
        body: samples[rel] ?? `// ${rel}\n\nMock file body from browser dev.`,
        size: samples[rel]?.length ?? 42,
        truncated: false,
        binary: false,
      };
    },
    async ReadFileForTab(_tabID: string, rel: string) {
      return this.ReadFile(rel);
    },
    async WorkspaceChanges(_tabID: string) {
      return {
        gitAvailable: true,
        gitBranch: "main",
        files: [
          {
            path: "desktop/frontend/src/components/WorkspacePanel.tsx",
            sources: ["session", "git"],
            gitStatus: "M",
            turns: [0, 2],
            latestPrompt: "Mock session edited the workspace panel.",
            latestTime: Date.now() - 60_000,
          },
          { path: "README.md", sources: ["git"], gitStatus: "??" },
          { path: "internal/control/controller.go", sources: ["session"], turns: [1], latestTime: Date.now() - 120_000 },
        ],
      };
    },
    async WorkspaceWelcome(_tabID: string) {
      return {
        workspaceName: "WorkGround2",
        scope: "project",
        contentKinds: ["code", "docs"],
        confidence: 0.95,
        fileCount: 184,
        changedCount: 3,
        sessionCount: 7,
        recentTitle: "Improve workspace session sync",
        recentActivity: Date.now() - 90 * 60_000,
        scannedAt: Date.now(),
      };
    },
    async PinnedMemoriesForTab(_tabID: string) {
      return { items: [] };
    },
    async PinMemoryForTab(_tabID: string, _role: string, _content: string, _turn: number): Promise<PinMemoryResult> {
      return { id: "pm-mock", isNew: true, pinned: true };
    },
    async SetPinnedMemoryPinnedForTab(_tabID: string, _id: string, _pinned: boolean): Promise<boolean> {
      return true;
    },
    async GitBranches() {
      return ["main", "dev", "feature/branch-switcher"];
    },
    async GitCheckout(_branch: string) {
      console.info("mock GitCheckout", _branch);
    },
    async WorkspaceGitHistory(_tabID: string, path: string) {
      return [
        { hash: "abcdef123456", author: "Mock Author", date: new Date().toISOString(), message: "Mock commit message for " + path },
      ];
    },
    async WorkspaceGitCommitDetail(_tabID: string, _hash: string, path: string) {
      if (path) {
        return { diff: "--- a/mock\n+++ b/mock\n@@ -1,1 +1,1 @@\n-mock\n+mock diff" };
      }
      return { files: ["mock_file_1.ts", "mock_file_2.ts"] };
    },
    async OpenWorkspacePath(rel: string) {
      console.info("mock OpenWorkspacePath", rel);
    },
    async OpenWorkspacePathForTab(_tabID: string, rel: string) {
      await this.OpenWorkspacePath(rel);
    },
    async OpenWorkArtifactForTab(_tabID: string, input: WorkArtifactFileIntent) {
      console.info("mock OpenWorkArtifactForTab", input);
    },
    async OpenWorkArtifactURLForTab(_tabID: string, input: WorkArtifactFileIntent) {
      console.info("mock OpenWorkArtifactURLForTab", input);
    },
    async RevealWorkspacePath(rel: string) {
      console.info("mock RevealWorkspacePath", rel);
    },
    async RevealWorkspacePathForTab(_tabID: string, rel: string) {
      await this.RevealWorkspacePath(rel);
    },
    async RevealWorkArtifactForTab(_tabID: string, input: WorkArtifactFileIntent) {
      console.info("mock RevealWorkArtifactForTab", input);
    },
    async RevealPath(path: string) {
      console.info("mock RevealPath", path);
    },
    async SavePastedImage(_dataUrl: string) {
      return ".WorkGround2/attachments/mock.png";
    },
    async SaveClipboardImage() {
      return ".WorkGround2/attachments/mock-clipboard.png";
    },
    async SavePastedFile(name: string, _dataUrl: string) {
      return `.WorkGround2/attachments/mock-${name}`;
    },
    async PickExportFile(defaultFilename: string, _mimeType: string) {
      return defaultFilename;
    },
    async SaveExportFile(path: string, payload: string, base64Encoded: boolean) {
      const a = document.createElement("a");
      let url = "";
      if (base64Encoded) {
        url = `data:application/octet-stream;base64,${payload}`;
      } else {
        url = URL.createObjectURL(new Blob([payload], { type: "text/plain;charset=utf-8" }));
      }
      a.href = url;
      a.download = path;
      document.body.appendChild(a);
      a.click();
      a.remove();
      if (!base64Encoded) URL.revokeObjectURL(url);
    },
    async AttachDropped(path: string) {
      const name = path.split(/[/\\]/).filter(Boolean).pop() ?? path;
      const hasExt = /\.\w{1,6}$/i.test(name);
      if (!hasExt) {
        const tokenName = name.replace(/[^\w.-]+/g, "-") || "folder";
        return { kind: "workspace" as const, path: `__WorkGround2_external_folder/mock/${tokenName}`, isDir: true, displayPath: path };
      }
      return { kind: "attachment" as const, path: `.WorkGround2/attachments/mock-${name}` };
    },
    async AttachmentDataURL(_path: string) {
      return "data:image/png;base64,iVBORw0KGgo=";
    },
    async RequestHelpImageDataURL(_path: string) {
      return "data:image/png;base64,iVBORw0KGgo=";
    },
    async RequestHelpOpenImage(path: string) {
      console.info("mock RequestHelpOpenImage", path);
    },
    async RequestHelpRevealImage(path: string) {
      console.info("mock RequestHelpRevealImage", path);
    },
    async ArtifactImageDataURL(_tabID: string, _artifactID: string) {
      return "data:image/png;base64,iVBORw0KGgo=";
    },
    async ArtifactOpenImage(_tabID: string, _artifactID: string) {
      console.info("mock ArtifactOpenImage", _tabID, _artifactID);
    },
    async ArtifactRevealImage(_tabID: string, _artifactID: string) {
      console.info("mock ArtifactRevealImage", _tabID, _artifactID);
    },
        async Models() {
          const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
          const current = mockTabModelRef(active);
          return mockModelCatalog.map((model) => ({ ...model, current: model.ref === current }));
        },
        async ModelsForTab(tabID) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active) ?? mockTabs[0];
          const current = mockTabModelRef(tab);
          return mockModelCatalog.map((model) => ({ ...model, current: model.ref === current }));
        },
        async SetModel(name) {
          setMockTabModel(undefined, name);
        },
        async SetModelForTab(tabID, name) {
          setMockTabModel(tabID, name);
        },
        async Effort() {
          return { supported: true, current: mockEffort, default: "high", levels: ["auto", "high", "max"] };
        },
        async EffortForTab() {
          return this.Effort();
        },
        async SetEffort(level: string) {
          mockEffort = level || "auto";
        },
        async SetEffortForTab(_tabID, level) {
          await this.SetEffort(level);
        },
        async SetTokenMode(mode: string) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetTokenModeForTab(active.id, mode);
        },
        async SetTokenModeForTab(tabID, mode) {
          const tokenMode = normalizeTokenMode(mode);
          mockTabs = mockTabs.map((tab) => (tab.id === tabID ? { ...tab, tokenMode } : tab));
        },
    async Memory() {
      return {
        available: true,
        storeDir: "~/.config/WorkGround2/projects/-mock/memory",
        storeGlobalDir: "~/.config/WorkGround2/memory/global",
        docs: [
          {
            path: "WorkGround2.md",
            scope: "project",
            body: "# WorkGround2 project memory\n\nMock doc shown in the browser dev seam.\n\n## Notes\n\n- prefers concise replies",
          },
          {
            path: "~/.config/WorkGround2/WorkGround2.md",
            scope: "user",
            body: t("mock.memoryBody"),
          },
        ],
        facts: [
          {
            name: "prefers-tabs",
            description: "User prefers tabs",
            type: "user",
            body: "Indent with tabs.",
          },
        ],
        archives: [
          {
            name: "old-plan",
            description: "Superseded planning note",
            type: "project",
            body: "This plan was archived after the implementation changed.",
            path: "~/.config/WorkGround2/projects/-mock/memory/.archive/20260612-021500.000-old-plan.md",
            archivedAt: "2026-06-12T02:15:00Z",
          },
        ],
        scopes: [
          { scope: "user", path: "~/.config/WorkGround2/WorkGround2.md" },
          { scope: "project", path: "WorkGround2.md" },
          { scope: "local", path: "WorkGround2.local.md" },
        ],
      };
    },
    async MemorySuggestions() {
      return {
        memories: [
          {
            id: "memory-prefers-concise-replies",
            name: "prefers-concise-replies",
            title: "Prefers concise replies",
            description: "User prefers concise replies unless detail is requested.",
            type: "user",
            body: "User prefers concise replies unless detail is requested.\n\n**Why:** Suggested from recent local history.\n**How to apply:** Keep answers brief by default.",
            reason: "future-facing preference",
            evidence: ["mock-session: always keep replies concise"],
          },
        ],
        skills: [
          {
            id: "skill-WorkGround2-pr-followup",
            name: "WorkGround2-pr-followup",
            description: "Review or update a WorkGround2 GitHub PR, address feedback, verify, and publish safely.",
            scope: "project",
            body: "# WorkGround2 PR Followup\n\nUse this skill for repeated WorkGround2 PR work.\n\n## Workflow\n\n1. Confirm branch and PR state.\n2. Inspect the diff.\n3. Fix actionable feedback.\n4. Verify and update the PR.\n",
            reason: "recent history repeatedly touched PR workflows",
            evidence: ["mock-pr-session: 提交到pr，并更新内容", "mock-review-session: 解决该pr下机器人提出来的问题"],
          },
        ],
        generatedAt: new Date().toISOString(),
        available: true,
        source: "mock",
      };
    },
    async AcceptMemorySuggestion(suggestion: MemorySuggestion) {
      emit({ kind: "notice", level: "info", text: `saved suggested memory → ${suggestion.name}` });
      return `${suggestion.name}.md`;
    },
    async AcceptSkillSuggestion(suggestion: SkillSuggestion) {
      emit({ kind: "notice", level: "info", text: `created suggested skill → ${suggestion.name}` });
      return `.WorkGround2/skills/${suggestion.name}/SKILL.md`;
    },
    async MemorySuggestionsForTab(_tabID: string) {
      return this.MemorySuggestions();
    },
    async AcceptMemorySuggestionForTab(_tabID: string, suggestion: MemorySuggestion) {
      return this.AcceptMemorySuggestion(suggestion);
    },
    async AcceptSkillSuggestionForTab(_tabID: string, suggestion: SkillSuggestion) {
      return this.AcceptSkillSuggestion(suggestion);
    },
    async MemoryForTab(_tabID: string) {
      return this.Memory();
    },
    async Remember(_scope: string, _note: string) {
      emit({ kind: "notice", level: "info", text: `remembered → ${_scope}` });
      return `${_scope} WorkGround2.md (mock): ${_note}`;
    },
    async RememberForTab(_tabID: string, scope: string, note: string) {
      return this.Remember(scope, note);
    },
    async Forget(_name: string) {
      emit({ kind: "notice", level: "info", text: `forgot → ${_name}` });
    },
    async ForgetForTab(_tabID: string, name: string) {
      return this.Forget(name);
    },
    async SaveDoc(_path: string, _body: string) {
      emit({ kind: "notice", level: "info", text: `saved → ${_path}` });
      return _path;
    },
    async SaveDocForTab(_tabID: string, path: string, body: string) {
      return this.SaveDoc(path, body);
    },
    async DesktopStartupSettings() {
      const { bot, desktopLanguage, desktopLayoutStyle, desktopTheme, desktopThemeStyle, displayMode, composerSubmitKey, statusBarStyle, statusBarItems, checkUpdates, widgetEnabled, widgetAlwaysOnTop, widgetSkin, widgetStyle, hoverStatusDelayMs, ownerDecisionEnabled } = settings;
      return JSON.parse(JSON.stringify({
        bot,
        desktopLanguage,
        desktopLayoutStyle,
        desktopTheme,
        desktopThemeStyle,
        displayMode,
        composerSubmitKey,
        statusBarStyle,
        statusBarItems,
        checkUpdates,
        widgetEnabled,
        widgetAlwaysOnTop,
        widgetSkin,
			widgetStyle,
			hoverStatusDelayMs,
        ownerDecisionEnabled,
      })) as DesktopStartupSettingsView;
    },
    async Settings() {
      return JSON.parse(JSON.stringify(settings)) as SettingsView;
    },
    async SessionBackgroundSettings() {
      return JSON.parse(JSON.stringify(sessionBackgroundSettings)) as SessionBackgroundSettingsView;
    },
    async RefreshSessionBackgroundSettings() {
      return JSON.parse(JSON.stringify(sessionBackgroundSettings)) as SessionBackgroundSettingsView;
    },
    async SetSessionBackgroundSettings(next: SessionBackgroundSettingsView) {
      sessionBackgroundSettings = JSON.parse(JSON.stringify(next)) as SessionBackgroundSettingsView;
      mockSessionBackgroundListeners.forEach((listener) => listener());
    },
    async PickSessionBackgroundFiles() {
      return [];
    },
    async PickSessionBackgroundFolder() {
      return "";
    },
    async UnreadState() {
      return emptyUnreadState();
    },
    async MarkUnreadRead() {
      return emptyUnreadState();
    },
    async ResolveLegacySessionUnread(_conversationKey: string) {
      throw new Error("ResolveLegacySessionUnread is not available in browser dev mode");
    },
    async ResolveUnreadSession(_conversationKey: string) {
      throw new Error("ResolveUnreadSession is not available in browser dev mode");
    },
    async SessionBackground(_tabID: string) {
      return { path: "", url: "" };
    },
    async RotateSessionBackground(_tabID: string) {
      return { path: "", url: "" };
    },
    async TryDeferredConfigRebuild() {
      // Browser dev mock has no controller rebuild lifecycle.
    },
    async HooksSettings(scope: string) {
      const key = scope === "project" ? "project" : "global";
      return JSON.parse(JSON.stringify(hookSettings[key])) as HooksSettingsView;
    },
    async SaveHooksSettings(scope: string, hooks: HookConfigView[]) {
      const key = scope === "project" ? "project" : "global";
      hookSettings[key].hooks = JSON.parse(JSON.stringify(hooks)) as HookConfigView[];
    },
    async SaveHooksSettingsForRoot(scope: string, _projectRoot: string, hooks: HookConfigView[]) {
      const key = scope === "project" ? "project" : "global";
      hookSettings[key].hooks = JSON.parse(JSON.stringify(hooks)) as HookConfigView[];
    },
    async TrustProjectHooks() {
      hookSettings.project.trusted = true;
    },
    async TrustProjectHooksForRoot(projectRoot: string) {
      if (projectRoot && projectRoot === hookSettings.project.projectRoot) {
        hookSettings.project.trusted = true;
      }
    },
    async SetDefaultModel(ref: string) {
      settings.defaultModel = ref;
    },
    async SetPlannerModel(ref: string) {
      settings.plannerModel = ref;
    },
    async SetSubagentModel(ref: string) {
      settings.subagentModel = ref;
    },
    async SetSubagentEffort(level: string) {
      settings.subagentEffort = level;
    },
    async SetMaxSubagentDepth(depth: number) {
      settings.agent = { ...settings.agent, maxSubagentDepth: depth <= 1 ? 1 : 2 };
    },
    async SetAutoPlan(mode: string) {
      settings.autoPlan = mode;
    },
    async SetDefaultToolApprovalMode(mode: string) {
      settings.defaultToolApprovalMode = normalizeToolApprovalMode(mode);
    },
    async SaveProvider(p: ProviderView) {
      p.added = true;
      const i = settings.providers.findIndex((x) => x.name === p.name);
      if (i >= 0) settings.providers[i] = p;
      else settings.providers.push(p);
    },
    async AddOfficialProviderAccess(kind: string, key: string, _name: string) {
      const templates: Record<string, ProviderView> = {
        deepseek: { name: "deepseek", builtIn: true, added: true, kind: "openai", baseUrl: "https://api.deepseek.com", modelsUrl: "", models: ["deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"], visionModels: ["deepseek-v4-flash-vision-exp"], visionModelsConfigured: true, default: "deepseek-v4-flash", apiKeyEnv: "DEEPSEEK_API_KEY", keySet: !!key.trim(), balanceUrl: "https://api.deepseek.com/user/balance", contextWindow: 1_000_000, reasoningProtocol: "", supportedEfforts: [], defaultEffort: "" },
        openai: { name: "openai", builtIn: true, added: true, kind: "openai", baseUrl: "https://api.openai.com/v1", modelsUrl: "", models: ["gpt-4o", "gpt-4o-mini", "gpt-4.1", "o4-mini", "o3-mini", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"], visionModels: ["gpt-4o", "gpt-4o-mini", "gpt-4.1", "o4-mini", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"], visionModelsConfigured: true, default: "gpt-4o", apiKeyEnv: "OPENAI_API_KEY", keySet: !!key.trim(), balanceUrl: "", contextWindow: 1_050_000, reasoningProtocol: "", supportedEfforts: [], defaultEffort: "" },
      };
      const next = templates[kind];
      if (!next) throw new Error(`unknown official provider template ${kind}`);
      const i = settings.providers.findIndex((x) => x.name === next.name);
      if (i >= 0) settings.providers[i] = { ...settings.providers[i], ...next, keySet: next.keySet || settings.providers[i].keySet };
      else settings.providers.push(next);
      return "";
    },
    async FetchProviderModels(p: ProviderView) {
      if (!p.baseUrl.trim()) throw new Error(t("settings.fetchModelsMissingBaseUrl"));
      if (providerRequiresKey(p) && !p.apiKeyEnv.trim()) throw new Error(t("settings.fetchModelsMissingKeyEnv"));
      await delay(350);
      if (p.baseUrl.includes("deepseek")) return ["deepseek-v4-flash", "deepseek-v4-pro"];
      if (p.baseUrl.includes("mimo") || p.baseUrl.includes("xiaomimimo")) return ["mimo-v2.5", "mimo-v2.5-pro"];
      return ["gpt-5", "gpt-5-mini", "qwen3-coder"];
    },
    async DeleteProvider(name: string) {
      settings.providers = settings.providers.filter((p) => p.name !== name);
    },
    async RemoveProviderAccess(name: string) {
      const p = settings.providers.find((x) => x.name === name);
      if (p?.builtIn) p.added = false;
      else settings.providers = settings.providers.filter((x) => x.name !== name);
    },
    async SetProviderKey(apiKeyEnv: string, _value: string) {
      settings.providers.forEach((p) => {
        if (p.apiKeyEnv === apiKeyEnv) p.keySet = true;
      });
      return "";
    },
    async ClearProviderKey(apiKeyEnv: string) {
      settings.providers.forEach((p) => {
        if (p.apiKeyEnv === apiKeyEnv) p.keySet = false;
      });
    },
    async SetPermissionMode(mode: string) {
      settings.permissions.mode = mode;
    },
    async AddPermissionRule(list: string, rule: string) {
      const k = list as "allow" | "ask" | "deny";
      if (settings.permissions[k] && !settings.permissions[k].includes(rule)) settings.permissions[k].push(rule);
    },
    async RemovePermissionRule(list: string, rule: string) {
      const k = list as "allow" | "ask" | "deny";
      settings.permissions[k] = settings.permissions[k].filter((r) => r !== rule);
    },
    async SetBrowserPermissions(b: BrowserPermissionsView) {
      settings.permissions.browser = { allowPasswordInput: b.allowPasswordInput, allowFileUpload: b.allowFileUpload };
    },
    async SetBrowserLaunch(b: BrowserLaunchView) {
      settings.browserLaunch = { incognito: b.incognito };
    },
        async SetSandbox(bash: string, network: boolean, workspaceRoot: string, allowWrite: string[], shell: string) {
          settings.sandbox = { bash, network, workspaceRoot, allowWrite, shell };
        },
        async SetNetwork(n: NetworkView) {
          settings.network = n;
        },
        async SetCollaboration(c: CollaborationSettingsView) {
          settings.collaboration = JSON.parse(JSON.stringify(c)) as CollaborationSettingsView;
        },
        async SetBotSettings(b: BotSettingsView) {
          settings.bot = JSON.parse(JSON.stringify(b)) as BotSettingsView;
        },
        async SetBotConnectionToolApprovalMode(connID, mode) {
          const conn = settings.bot.connections.find((c) => c.id === connID);
          if (conn) conn.toolApprovalMode = mode as any;
        },
        async SetBotSecret(envName: string, _value: string) {
          const name = envName.trim();
          if (settings.bot.qq.appSecretEnv === name) settings.bot.qq.secretSet = true;
          if (settings.bot.feishu.appSecretEnv === name) settings.bot.feishu.secretSet = true;
          if (settings.bot.weixin.tokenEnv === name) settings.bot.weixin.tokenSet = true;
          settings.bot.connections = settings.bot.connections.map((connection) => ({
            ...connection,
            credential: connection.credential.appSecretEnv === name || connection.credential.tokenEnv === name
              ? { ...connection.credential, secretSet: true }
              : connection.credential,
          }));
        },
        async ClearBotSecret(envName: string) {
          const name = envName.trim();
          if (settings.bot.qq.appSecretEnv === name) settings.bot.qq.secretSet = false;
          if (settings.bot.feishu.appSecretEnv === name) settings.bot.feishu.secretSet = false;
          if (settings.bot.weixin.tokenEnv === name) settings.bot.weixin.tokenSet = false;
          settings.bot.connections = settings.bot.connections.map((connection) => ({
            ...connection,
            credential: connection.credential.appSecretEnv === name || connection.credential.tokenEnv === name
              ? { ...connection.credential, secretSet: false }
              : connection.credential,
          }));
        },
        async BotRuntimeStatus() {
          const qqRunning = settings.bot.qq.enabled && settings.bot.qq.appId.trim() && settings.bot.qq.secretSet;
          const runningConnections = (qqRunning ? 1 : 0) + settings.bot.connections.filter((connection) => connection.enabled && connection.status === "connected").length;
          return {
            running: settings.bot.enabled && runningConnections > 0,
            status: settings.bot.enabled && runningConnections > 0 ? "running" : "stopped",
            message: settings.bot.enabled && runningConnections > 0 ? `${runningConnections} bot connection(s) running` : "bot runtime is not started",
            connections: runningConnections,
            startedAt: settings.bot.enabled && runningConnections > 0 ? new Date(t0).toISOString() : "",
          };
        },
        async StartBotConnectionInstall(provider: string, domain: string) {
          const normalizedProvider = provider === "weixin" ? "weixin" : "feishu";
          const normalizedDomain = normalizedProvider === "weixin" ? "weixin" : domain === "lark" ? "lark" : "feishu";
          return {
            ok: true,
            provider: normalizedProvider,
            domain: normalizedDomain,
            installId: `mock-${normalizedProvider}-${normalizedDomain}`,
            url: "https://example.com/WorkGround2-bot-qr",
            deviceCode: "MOCKDEVICE",
            userCode: normalizedProvider === "weixin" ? "" : "MOCK-CODE",
            interval: 3,
            expireIn: 300,
            message: "",
          };
        },
        async PollBotConnectionInstall(installID: string) {
          const isWeixin = installID.includes("weixin");
          const domain = installID.includes("lark") ? "lark" : isWeixin ? "weixin" : "feishu";
          const provider = isWeixin ? "weixin" : "feishu";
          const connection = {
            id: `${provider}-${domain}`,
            provider,
            domain,
            label: domain === "lark" ? "Lark" : domain === "weixin" ? "微信" : "飞书",
            enabled: true,
            status: "connected",
            model: "",
            toolApprovalMode: "",
            workspaceRoot: "",
            access: emptyBotAccess(),
            credential: {
              appId: provider === "feishu" ? "cli_mock" : "",
              appSecretEnv: provider === "feishu" ? (domain === "lark" ? "LARK_BOT_APP_SECRET" : "FEISHU_BOT_APP_SECRET") : "",
              accountId: provider === "weixin" ? "mock-account" : "",
              tokenEnv: provider === "weixin" ? "WEIXIN_BOT_TOKEN" : "",
              secretSet: true,
            },
            sessionMappings: [],
            endpoints: [],
            lastError: "",
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          };
          settings.bot.connections = [...settings.bot.connections.filter((c) => c.id !== connection.id), connection];
          return { done: true, connection, status: "connected", message: "connected", error: "" };
        },
        async DiagnoseBotConnection(id: string) {
          const connection = settings.bot.connections.find((c) => c.id === id);
          const occurredAt = new Date().toISOString();
          return connection
            ? { id, label: connection.label, status: connection.enabled ? "ok" : "disabled", message: connection.enabled ? "连接配置已保存。" : "连接已保存但未启用。", messageId: "", phase: "config", code: connection.enabled ? "config_ok" : "connection_disabled", reportKind: "", reportDetail: "", occurredAt }
            : { id, label: "", status: "missing", message: "未找到连接。", messageId: "", phase: "config", code: "connection_missing", reportKind: "bot", reportDetail: JSON.stringify({ schemaVersion: 2, kind: "bot", source: "bot.runtime", label: "bot.mock.config", message: "mock missing bot connection", errorType: "BotConnectionDiagnostic", errorMessage: "bot connection record was not found", topFrame: "bot.config", occurredAt }), occurredAt };
        },
        async TestBotConnection(id: string, target?: string) {
          const diag = await this.DiagnoseBotConnection(id);
          if (target?.trim()) return { ...diag, message: `Mock test sent to ${target.trim()}`, messageId: "mock-message-id" };
          return diag;
        },
        async SetCloseBehavior(mode: string) {
          settings.closeBehavior = mode === "quit" ? "quit" : "background";
        },
        async SetDisplayMode(mode: string) {
          settings.displayMode = mode;
        },
        async SetDesktopComposerSubmitKey(mode: string) {
          settings.composerSubmitKey = mode === "ctrl_enter" ? "ctrl_enter" : "enter";
        },
        async SetStatusBarStyle(style: string) {
          settings.statusBarStyle = style === "text" ? "text" : "icon";
        },
        async SetStatusBarItems(items: string[]) {
          settings.statusBarItems = normalizeStatusBarItems(items);
        },
        async SetDesktopLanguage(lang: string) {
          settings.desktopLanguage = lang === "en" || lang === "zh" ? lang : "";
        },
        async SetDesktopAppearance(theme: string, style: string) {
          settings.desktopTheme = theme === "auto" || theme === "light" ? theme : "dark";
          settings.desktopThemeStyle = style;
        },
        async SetDesktopLayoutStyle(style: string) {
          settings.desktopLayoutStyle = style === "workbench" || style === "creation" ? style : "classic";
        },
        async SetDesktopZoomFactor(_factor: number) {
          // no-op in mock; in production this writes desktop-zoom.json via Go
        },
        async GetDesktopZoomFactor() {
          return desktopZoomFactor;
        },
        async RestartApplication() {
          // no-op in mock
        },
        async SetDesktopCheckUpdates(enabled: boolean) {
          settings.checkUpdates = enabled;
        },
        async SetDesktopTelemetry(enabled: boolean) {
          settings.telemetry = enabled;
        },
        async SetDesktopMetrics(enabled: boolean) {
          settings.metrics = enabled;
        },
        async SetDesktopWidgetEnabled(enabled: boolean) {
          settings.widgetEnabled = enabled;
        },
        async SetDesktopWidgetAlwaysOnTop(on: boolean) {
          settings.widgetAlwaysOnTop = on;
        },
        async SetDesktopWidgetShowDelegation(show: boolean) {
          settings.widgetShowDelegation = show;
        },
        async SetDesktopWidgetShowExternalTools(show: boolean) {
          settings.widgetShowExternalTools = show;
        },
        async SetDesktopWidgetShowAssistant(show: boolean) {
          settings.widgetShowAssistant = show;
        },
        async SetDesktopWidgetSkin(skin: string) {
          settings.widgetSkin = skin;
        },
		async SetDesktopWidgetStyle(style: string) {
			if (style !== "icons") throw new Error(`unsupported widget style ${JSON.stringify(style)}: icons only`);
			desktopWidgetStyle = "icons";
			settings.widgetStyle = "icons";
		},
		async SetDesktopHoverStatusDelayMs(delay: number) { settings.hoverStatusDelayMs = Math.max(0, Math.min(10_000, delay)); },
        async SetMemoryCompilerEnabled(enabled: boolean) {
          settings.memoryCompilerEnabled = enabled;
        },
        async SetExpandThinking(_on: boolean) {},
        async MigrateDesktopPreferences(language: string, theme: string, style: string) {
          if (!settings.desktopLanguage) settings.desktopLanguage = language === "en" || language === "zh" || language === "zh-TW" ? language : "";
          if (!settings.desktopTheme && !settings.desktopThemeStyle) {
            settings.desktopTheme = theme === "auto" || theme === "light" ? theme : "dark";
            settings.desktopThemeStyle = style;
          }
        },
    async SetAgentParams(temperature: number, maxSteps: number, plannerMaxSteps: number, systemPrompt: string) {
      settings.agent = { ...settings.agent, temperature, maxSteps, plannerMaxSteps, systemPrompt };
    },
    async SetColdResumePrune(enabled: boolean) {
      settings.agent = { ...settings.agent, coldResumePrune: enabled };
    },
    async SetReasoningLanguage(lang: string) {
      const normalized = lang === "zh" || lang === "en" ? lang : "auto";
      settings.agent = { ...settings.agent, reasoningLanguage: normalized };
    },
    // ── Heartbeat mock ──
    async HeartbeatListTasks() { return []; },
    async HeartbeatReloadTasks() { return []; },
    async HeartbeatSaveTasks(_tasks: unknown) {},
    async HeartbeatTriggerNow(_id: string) {},
    async HeartbeatGenerateID() { return "mock-" + Date.now().toString(36); },
    async HeartbeatListConversions() { return []; },
    async HeartbeatConvertToAssistant(_id: string) { return { taskId: _id, state: "convertible" }; },
    // ── Assistant mode mock ──
    async AssistantList() {
      return cloneAssistant({
        items: mockAssistantSnapshots.map((item) => item.assistant),
        diagnostics: widgetScenario === "assistant-diagnostic"
          ? [{ at: new Date().toISOString(), operation: "list", message: "一个助手快照损坏，已跳过；健康助手仍可使用。" }]
          : [],
      });
    },
    async AssistantGet(assistantId: string) {
      return cloneAssistant(findAssistantSnapshot(assistantId));
    },
    async AssistantCreate(input: AssistantCreateInput) {
      const existing = mockAssistantSnapshots.find((item) => item.assistant.id === input.assistant.id);
      if (existing) return cloneAssistant(existing);
      const now = new Date().toISOString();
      const assistant = { ...input.assistant, revision: 1, memory_revision: 0, created_at: now, updated_at: now };
      const routines = input.routines.map((routine, index) => ({
        ...routine,
        id: routine.id || `routine-${Date.now()}-${index}`,
        assistant_id: assistant.id,
        revision: 1,
        created_at: now,
        updated_at: now,
      }));
      const initialPrompt = input.initialPrompt?.trim();
      const runs: AssistantRun[] = initialPrompt ? [{
        id: `run-initial-${Date.now()}`,
        assistant_id: assistant.id,
        prompt: initialPrompt,
        scope: assistant.scope,
        workspace_root: assistant.workspace_root,
        request_id: `${input.requestId}:initial`,
        trigger: "manual",
        state: "queued",
        attempt: 0,
        max_attempts: 3,
        revision: 1,
        created_at: now,
        updated_at: now,
      }] : [];
      const snapshot: AssistantSnapshot = {
        revision: 1,
        assistant,
        routines,
        memory: { revision: 0, items: [] },
        runs,
        attention: [],
        plan: { revision: 1, responsibilities: [] },
        artifacts: [],
        opportunities: [],
        updated_at: now,
      };
      mockAssistantSnapshots.push(snapshot);
      return cloneAssistant(snapshot);
    },
    async AssistantUpdate(input: AssistantUpdateInput) {
      const snapshot = findAssistantSnapshot(input.assistant.id);
      snapshot.assistant = {
        ...input.assistant,
        revision: snapshot.assistant.revision + 1,
        updated_at: new Date().toISOString(),
      };
      touchAssistantSnapshot(snapshot);
      return cloneAssistant(snapshot.assistant);
    },
    async AssistantPutRoutine(input: AssistantRoutineInput) {
      const snapshot = findAssistantSnapshot(input.routine.assistant_id);
      const index = snapshot.routines.findIndex((item) => item.id === input.routine.id);
      const now = new Date().toISOString();
      const routine = {
        ...input.routine,
        revision: index >= 0 ? snapshot.routines[index].revision + 1 : 1,
        created_at: index >= 0 ? snapshot.routines[index].created_at : now,
        updated_at: now,
      };
      if (index >= 0) snapshot.routines[index] = routine;
      else snapshot.routines.push(routine);
      touchAssistantSnapshot(snapshot);
      return cloneAssistant(routine);
    },
    async AssistantApplyMemory(input: AssistantMemoryInput) {
      const snapshot = findAssistantSnapshot(input.assistantId);
      const remove = new Set(input.patch.delete ?? []);
      snapshot.memory.items = snapshot.memory.items.filter((item) => !remove.has(item.id));
      for (const item of input.patch.upsert ?? []) {
        const index = snapshot.memory.items.findIndex((current) => current.id === item.id);
        if (index >= 0) snapshot.memory.items[index] = { ...item, revision: snapshot.memory.items[index].revision + 1, updated_at: new Date().toISOString() };
        else snapshot.memory.items.push({ ...item, revision: 1 });
      }
      snapshot.memory.revision += 1;
      snapshot.assistant.memory_revision = snapshot.memory.revision;
      touchAssistantSnapshot(snapshot);
      return cloneAssistant(snapshot.memory);
    },
    async AssistantRunNow(input: AssistantRunNowInput) {
      const snapshot = findAssistantSnapshot(input.assistantId);
      const now = new Date().toISOString();
      const routine = snapshot.routines.find((item) => item.id === input.routineId) ?? snapshot.routines[0];
      const run: AssistantRun = {
        id: `run-${Date.now()}`,
        assistant_id: snapshot.assistant.id,
        routine_id: routine?.id,
        request_id: input.requestId,
        trigger: "manual",
        state: "queued",
        attempt: 0,
        max_attempts: input.maxAttempts ?? 3,
        scheduled_for: now,
        revision: 1,
        created_at: now,
        updated_at: now,
      };
      snapshot.runs.unshift(run);
      touchAssistantSnapshot(snapshot);
      window.setTimeout(() => {
        run.state = "succeeded";
        run.attempt = 1;
        run.started_at = now;
        run.finished_at = new Date().toISOString();
        run.updated_at = run.finished_at;
        run.summary = "已完成手动检查：当前分支测试通过，没有发现新的阻塞风险。";
        run.revision += 1;
        touchAssistantSnapshot(snapshot);
      }, 900);
      return cloneAssistant(run);
    },
    async AssistantSubmitInput(input: AssistantSubmitInputInput) {
      const snapshot = findAssistantSnapshot(input.assistantId);
      const prompt = input.input.trim();
      if (!prompt) throw new Error("assistant: direct input must not be empty");
      if (new TextEncoder().encode(prompt).length > 64 * 1024) throw new Error("assistant: direct input exceeds 65536 bytes");
      const existing = snapshot.runs.find((item) => item.request_id === input.requestId);
      if (existing) {
        if (existing.prompt !== prompt) throw new Error("assistant: request id replay has different direct input");
        return cloneAssistant(existing);
      }
      const now = new Date().toISOString();
      const run: AssistantRun = {
        id: `run-${Date.now()}`,
        assistant_id: snapshot.assistant.id,
        prompt,
        request_id: input.requestId,
        trigger: "manual",
        state: "queued",
        attempt: 0,
        max_attempts: input.maxAttempts ?? 3,
        scheduled_for: now,
        revision: 1,
        created_at: now,
        updated_at: now,
      };
      snapshot.runs.unshift(run);
      touchAssistantSnapshot(snapshot);
      window.setTimeout(() => {
        run.state = "succeeded";
        run.attempt = 1;
        run.started_at = now;
        run.finished_at = new Date().toISOString();
        run.updated_at = run.finished_at;
        run.summary = "已记录这条输入并在后台继续。";
        run.revision += 1;
        touchAssistantSnapshot(snapshot);
      }, 900);
      return cloneAssistant(run);
    },
    async AssistantResolveAttention(input: AssistantResolveAttentionInput) {
      const snapshot = findAssistantSnapshot(input.assistantId);
      const item = snapshot.attention.find((current) => current.id === input.attentionId);
      if (!item) throw new Error("待处理事项已不存在");
      item.state = input.state;
      item.resolution = input.resolution;
      item.revision += 1;
      item.updated_at = new Date().toISOString();
      touchAssistantSnapshot(snapshot);
      return cloneAssistant(item);
    },
    async AssistantResume(input: AssistantResumeInput) {
      const snapshot = mockAssistantSnapshots.find((item) => item.runs.some((run) => run.id === input.runId));
      const run = snapshot?.runs.find((item) => item.id === input.runId);
      if (!snapshot || !run) throw new Error("运行记录已不存在");
      run.state = "queued";
      run.error = undefined;
      run.updated_at = new Date().toISOString();
      run.revision += 1;
      touchAssistantSnapshot(snapshot);
      return cloneAssistant(run);
    },
    async AssistantCancel(input: AssistantCancelInput) {
      const snapshot = mockAssistantSnapshots.find((item) => item.runs.some((run) => run.id === input.runId));
      const run = snapshot?.runs.find((item) => item.id === input.runId);
      if (!snapshot || !run) throw new Error("运行记录已不存在");
      run.state = "cancelled";
      run.summary = input.reason;
      run.finished_at = new Date().toISOString();
      run.updated_at = run.finished_at;
      run.revision += 1;
      touchAssistantSnapshot(snapshot);
      return cloneAssistant(run);
    },
    async SetTrayLocale(_locale: "en" | "zh" | "zh-TW") {},
    async SetAutoApproveTools(on: boolean) {
      await this.SetToolApprovalMode(on ? "yolo" : "ask");
    },
    async SetBypass(on: boolean) {
      await this.SetAutoApproveTools(on);
    },
    async Version() {
      return "v1.0.0 (browser dev)";
    },
    async AICollaborationPrompt() {
      return "The deterministic WorkGround2 Worker Skill Bundle is embedded in WorkGround2 Desktop. Launch the Desktop build to export the exact versioned SKILL.md, references/cli.md, scripts/dispatch.ps1, manifest, and SHA-256 values; browser development mode does not fabricate installable bundle bytes.";
    },
    async InjectAICollaborationPrompt() {
      await delay(200);
      return { ok: true, path: "~/.codex/AGENTS.md", skillPath: "~/.codex/skills/workground2-worker", backups: [] };
    },
    async GetGlobalAgentsMD() {
      await delay(100);
      return "";
    },
    async SetGlobalAgentsMD(_content: string) {
      await delay(100);
    },
    async CheckUpdate() {
      // Keep the default browser preview focused on the primary product surface.
      // DownloadUpdate/InstallUpdate remain mocked for explicit updater-flow tests.
      return {
        available: false,
        current: "v1.0.0",
        latest: "v1.0.0",
        notes: "",
        channel: "stable",
        canSelfUpdate: false,
        manualOnly: true,
        manualReason: "browser preview",
        downloaded: false,
        downloadUrl: "",
        assetSize: 0,
      };
    },
    async DownloadUpdate() {
      const total = 12_345_678;
      for (let r = 0; r <= total; r += 1_800_000) {
        emitUpdater({ phase: "downloading", received: Math.min(r, total), total });
        await delay(120);
      }
      emitUpdater({ phase: "verifying", received: total, total });
      await delay(500);
      emitUpdater({ phase: "downloaded", received: total, total });
      return { version: "v1.1.0", channel: "stable", path: "/tmp/WorkGround2-update", size: total, sha256: "mock" };
    },
    async InstallUpdate() {
      const total = 12_345_678;
      emitUpdater({ phase: "installing", received: total, total });
      await delay(500);
      emitUpdater({ phase: "done", received: total, total });
      // The real shell relaunches here; the mock just stops.
    },
    async ApplyUpdate() {
      await this.DownloadUpdate();
      await this.InstallUpdate();
    },
    async OpenDownloadPage() {
      if (typeof window !== "undefined") {
        window.open("https://github.com/KiddPhenix/WorkGround2/releases/latest", "_blank", "noopener");
      }
    },
    // Dev seam: drives the setup overlay in the browser until ConnectKey sets a
    // key or SkipOnboarding records explicit provider access.
    async NeedsOnboarding() {
      const provider = settings.providers.find((p) => p.apiKeyEnv === "DEEPSEEK_API_KEY");
      return !(provider?.keySet || provider?.added || settings.providers.some((p) => providerRequiresKey(p) === false));
    },
    async ConnectKey(apiKey: string) {
      if (!apiKey.trim()) throw new Error("key is required");
      settings.providers.forEach((p) => {
        if (p.apiKeyEnv === "DEEPSEEK_API_KEY") {
          p.keySet = true;
          p.added = true;
        }
      });
      await delay(300);
      return "";
    },
    async ScanLocalCLIProviders() {
      await delay(250);
      return mockLocalCLIOptions.map((option) => ({ ...option, args: [...option.args] }));
    },
    async ConnectLocalCLIProvider(id: string) {
      const option = mockLocalCLIOptions.find((item) => item.id === id);
      if (!option || !option.installed) throw new Error(`${id} is not installed`);
      const provider: ProviderView = {
        name: `local-${option.id}`,
        builtIn: false,
        added: true,
        kind: "cli",
        baseUrl: "",
        chatUrl: "",
        command: option.command,
        args: [...option.args],
        protocol: option.protocol,
        timeoutSeconds: option.timeoutSeconds,
        models: [option.model],
        visionModels: [],
        visionModelsConfigured: false,
        modelsUrl: "",
        default: option.model,
        apiKeyEnv: "",
        headers: {},
        keySet: false,
        requiresKey: false,
        configured: true,
        balanceUrl: "",
        contextWindow: 128000,
        reasoningProtocol: "",
        supportedEfforts: [],
        defaultEffort: "",
        modelOverrides: [],
      };
      const i = settings.providers.findIndex((p) => p.name === provider.name);
      if (i >= 0) settings.providers[i] = provider;
      else settings.providers.push(provider);
      settings.defaultModel = `${provider.name}/${option.model}`;
      await delay(200);
    },
    async SkipOnboarding() {
      await this.AddOfficialProviderAccess("deepseek", "", "deepseek");
      await delay(100);
    },
    async ReportCrash() {
      await delay(300);
    },
    // Tab management mocks.
    async ListTabs() {
      return mockTabs.map((tab) => ({ ...tab }));
    },
    async ListRuntimeTabs() {
      return mockTabs.map((tab) => ({ ...tab }));
    },
    async RetryTabStartup(tabID: string) {
      mockTabs = mockTabs.map((tab) => tab.id === tabID ? { ...tab, ready: false, startupErr: undefined } : tab);
    },
    async CurrentSessionPath() {
      return mockTabs.find((tab) => tab.active)?.sessionPath ?? sessions.find((s) => s.current)?.path ?? "";
    },
    async OpenProjectTab(workspaceRoot: string, _topicID: string) {
      const existing = mockTabs.find((tab) => tab.scope === "project" && tab.workspaceRoot === workspaceRoot && tab.topicId === _topicID);
      if (existing) {
        const active = { ...existing, active: true, running: mockTopicRunsInScenario(_topicID) };
        mockTabs = mockTabs.map((tab) => (tab.id === existing.id ? active : { ...tab, active: false }));
        return { ...active };
      }
      const defaultToolApprovalMode = normalizeToolApprovalMode(settings.defaultToolApprovalMode);
      const tab: TabMeta = {
        id: "tab_" + Date.now(),
        scope: "project",
        workspaceRoot,
        workspaceName: workspaceRoot.split("/").filter(Boolean).pop() ?? workspaceRoot,
        workspacePath: workspaceRoot,
        gitBranch: "main",
        topicId: _topicID,
        topicTitle: topicLabel(_topicID, t("mock.newSession")),
        sessionPath: `/mock/sessions/${_topicID}.jsonl`,
        projectColor: mockProjectTree.find((node) => node.root === workspaceRoot)?.projectColor,
        label: mockModelLabel(settings.defaultModel),
        ready: true,
        running: mockTopicRunsInScenario(_topicID),
        mode: modeWithAutoApproveTools("normal", defaultToolApprovalMode === "yolo"),
        collaborationMode: "normal",
        toolApprovalMode: defaultToolApprovalMode,
        tokenMode: "full",
        active: true,
        cwd: workspaceRoot,
      };
      mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), tab];
      return { ...tab };
    },
    async OpenGlobalTab(_topicID: string) {
      const existing = mockTabs.find((tab) => tab.scope === "global" && tab.topicId === _topicID);
      if (existing) {
        setMockActiveTab(existing.id);
        return { ...existing, active: true };
      }
      const defaultToolApprovalMode = normalizeToolApprovalMode(settings.defaultToolApprovalMode);
      const tab: TabMeta = {
        id: "tab_" + Date.now(),
        scope: "global",
        workspaceRoot: "",
        workspaceName: "Global",
        workspacePath: cwd,
        topicId: _topicID,
        topicTitle: topicLabel(_topicID, "Global"),
        sessionPath: `/mock/sessions/${_topicID}.jsonl`,
        label: mockModelLabel(settings.defaultModel),
        ready: true,
        running: false,
        mode: modeWithAutoApproveTools("normal", defaultToolApprovalMode === "yolo"),
        collaborationMode: "normal",
        toolApprovalMode: defaultToolApprovalMode,
        tokenMode: "full",
        active: true,
        cwd: "",
      };
      mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), tab];
      return { ...tab };
    },
    async OpenTopicSession(scope: string, workspaceRoot: string, topicID: string, sessionPath: string) {
      const tab = scope === "project"
        ? await this.OpenProjectTab(workspaceRoot, topicID)
        : await this.OpenGlobalTab(topicID);
      const active = { ...tab, sessionPath };
      mockTabs = mockTabs.map((item) => (item.id === tab.id ? active : item));
      return { ...active };
    },
    async OpenLinkedSession(scope: string, workspaceRoot: string, topicID: string, sessionPath: string) {
      const topic = await this.CreateTopic(scope, workspaceRoot, "");
      const tab = scope === "project"
        ? await this.OpenProjectTab(workspaceRoot, topic.id)
        : await this.OpenGlobalTab(topic.id);
      const active = { ...tab, topicId: topicID, sessionPath, sessionKind: "normal" as const, workId: undefined };
      mockTabs = mockTabs.map((item) => (item.id === tab.id ? active : item));
      return { ...active };
    },
    async EnsureBlankTab(scope: string, workspaceRoot: string) {
      const targetScope = scope === "project" && workspaceRoot ? "project" : "global";
      const targetRoot = targetScope === "project" ? workspaceRoot : "";
      const existing = mockTabs.find((tab) =>
        tab.scope === targetScope &&
        (targetScope === "global" || tab.workspaceRoot === targetRoot) &&
        !tab.running &&
        mockTopicIsBlank(tab.topicId)
      );
      if (existing) {
        setMockActiveTab(existing.id);
        return { ...existing, active: true };
      }
      const topic = await this.CreateTopic(targetScope, targetRoot, "");
      return targetScope === "global" ? this.OpenGlobalTab(topic.id) : this.OpenProjectTab(targetRoot, topic.id);
    },
    async CreateBlankSession(input: CreateBlankSessionInput) {
      const targetScope = input.scope === "project" && input.workspaceRoot ? "project" : "global";
      const targetRoot = targetScope === "project" ? input.workspaceRoot : "";
      const target = `${targetScope}:${targetRoot}`;
      const prior = mockBlankCreates.get(input.requestId);
      if (prior && prior.target !== target) throw new Error("requestId was already used for another blank-session target");
      if (prior) {
        const existing = mockTabs.find((tab) => tab.id === prior.tabId);
        if (existing) { setMockActiveTab(existing.id); return { ...existing, active: true }; }
      }
      const topic = await this.CreateTopic(targetScope, targetRoot, "");
      const tab = targetScope === "global" ? await this.OpenGlobalTab(topic.id) : await this.OpenProjectTab(targetRoot, topic.id);
      mockBlankCreates.set(input.requestId, { target, tabId: tab.id });
      return tab;
    },
    async ActivateTopic(scope: string, workspaceRoot: string, topicID: string, sessionPath: string) {
      const tab = sessionPath
        ? await this.OpenTopicSession(scope, workspaceRoot, topicID, sessionPath)
        : scope === "project"
          ? await this.OpenProjectTab(workspaceRoot, topicID)
          : await this.OpenGlobalTab(topicID);
      mockTabs = mockTabs.filter((item) => item.id === tab.id).map((item) => ({ ...item, active: true }));
      return { ...mockTabs[0] };
    },
    async ActivateLinkedSession(scope: string, workspaceRoot: string, topicID: string, sessionPath: string) {
      const tab = await this.OpenLinkedSession(scope, workspaceRoot, topicID, sessionPath);
      mockTabs = mockTabs.filter((item) => item.id === tab.id).map((item) => ({ ...item, active: true }));
      return { ...mockTabs[0] };
    },
    async EnsureBlankSurface(scope: string, workspaceRoot: string) {
      const tab = await this.EnsureBlankTab(scope, workspaceRoot);
      mockTabs = mockTabs.filter((item) => item.id === tab.id).map((item) => ({ ...item, active: true }));
      return { ...mockTabs[0] };
    },
    async SetActiveTab(_tabID: string) {
      setMockActiveTab(_tabID);
      const tab = mockTabs.find((item) => item.id === _tabID);
      if (tab) queueMockTopicRuntime(tab);
    },
    async ReorderTabs(_tabIDs: string[]) {
      const byId = new Map(mockTabs.map((tab) => [tab.id, tab]));
      const ordered = _tabIDs.map((id) => byId.get(id)).filter((tab): tab is TabMeta => Boolean(tab));
      if (ordered.length === mockTabs.length) mockTabs = ordered;
    },
    async CloseTab(_tabID: string) {
      if (mockTabs.length <= 1) return;
      const wasActive = mockTabs.some((tab) => tab.id === _tabID && tab.active);
      mockTabs = mockTabs.filter((tab) => tab.id !== _tabID);
      if (wasActive && mockTabs.length > 0 && !mockTabs.some((tab) => tab.active)) {
        mockTabs[mockTabs.length - 1] = { ...mockTabs[mockTabs.length - 1], active: true };
      }
    },
    async ListProjectTree() {
      return cloneProjectTree();
    },
    async RenameProject(workspaceRoot: string, title: string) {
      const node = workspaceRoot
        ? mockProjectTree.find((item) => item.root === workspaceRoot)
        : mockProjectTree.find((item) => item.kind === "global_folder");
      // Mirror the real backend: an empty title clears the display override and
      // restores the folder-name semantics instead of keeping the old label.
      if (node) node.label = title.trim() || (node.kind === "global_folder" ? "Global" : baseName(node.root || node.label));
    },
    async SetProjectColor(workspaceRoot: string, color: string) {
      const node = workspaceRoot
        ? mockProjectTree.find((item) => item.root === workspaceRoot)
        : mockProjectTree.find((item) => item.kind === "global_folder");
      if (!node) return;
      node.projectColor = color || undefined;
      for (const child of projectChildren(node)) child.projectColor = node.projectColor;
      mockTabs = mockTabs.map((tab) =>
        (workspaceRoot ? tab.workspaceRoot === workspaceRoot : tab.scope === "global")
          ? { ...tab, projectColor: node.projectColor }
        : tab,
      );
    },
    async SetProjectIcon(workspaceRoot: string, icon: string) {
      const node = workspaceRoot
        ? mockProjectTree.find((item) => item.root === workspaceRoot)
        : mockProjectTree.find((item) => item.kind === "global_folder");
      if (!node) return;
      node.projectIcon = icon || undefined;
      for (const child of projectChildren(node)) child.projectIcon = node.projectIcon;
      mockTabs = mockTabs.map((tab) =>
        (workspaceRoot ? tab.workspaceRoot === workspaceRoot : tab.scope === "global")
          ? { ...tab, projectIcon: node.projectIcon }
          : tab,
      );
    },
    async SetProjectPinned(workspaceRoot: string, pinned: boolean) {
      setMockProjectPinned(workspaceRoot, pinned);
    },
    async ReorderProjects(workspaceRoots: string[]) {
      const projects = mockProjectTree.filter((node) => node.kind === "project");
      const globals = mockProjectTree.filter((node) => node.kind === "global_folder");
      if (!workspaceRoots.includes(GLOBAL_PROJECT_ORDER_KEY)) {
        if (workspaceRoots.length !== projects.length) return;
        const byRoot = new Map(projects.map((node) => [node.root, node]));
        const ordered = workspaceRoots.map((root) => byRoot.get(root)).filter((node): node is ProjectNode => Boolean(node));
        if (ordered.length !== projects.length) return;
        mockProjectTree.splice(0, mockProjectTree.length, ...globals, ...ordered);
        return;
      }
      const byKey = new Map<string, ProjectNode>();
      for (const node of projects) {
        if (node.root) byKey.set(node.root, node);
      }
      for (const node of globals) byKey.set(GLOBAL_PROJECT_ORDER_KEY, node);
      const seen = new Set<string>();
      const ordered: ProjectNode[] = [];
      for (const key of workspaceRoots) {
        if (seen.has(key)) return;
        const node = byKey.get(key);
        if (!node) return;
        seen.add(key);
        ordered.push(node);
      }
      if (ordered.length !== projects.length + globals.length) return;
      mockProjectTree.splice(0, mockProjectTree.length, ...ordered);
    },
    async CreateTopic(_scope: string, _workspaceRoot: string, title: string) {
      const now = Date.now();
      const id = "topic_" + now;
      const topicTitle = title.trim() || t("mock.newSession");
      const parent = _scope === "global"
        ? ensureMockGlobalFolder()
        : mockProjectTree.find((node) => node.root === _workspaceRoot);
      if (parent) {
        const global = parent.kind === "global_folder";
        parent.children = [{
          key: parent.kind === "global_folder" ? "global_topic_" + id : "topic_" + id,
          kind: global ? "global_topic" : "topic",
          label: topicTitle,
          root: parent.root,
          topicId: id,
          projectColor: parent.projectColor,
          createdAt: now,
        }, ...projectChildren(parent)];
      }
      return { id, title: topicTitle, createdAt: now };
    },
    async RenameTopic(topicID: string, title: string) {
      const topic = findMockTopic(topicID);
      const nextTitle = title.trim();
      if (!topic || !nextTitle) return;
      const activePrefix = topic.label?.startsWith("● ") ? "● " : "";
      topic.label = `${activePrefix}${nextTitle}`;
      mockTabs = mockTabs.map((tab) =>
        tab.topicId === topicID ? { ...tab, topicTitle: nextTitle } : tab,
      );
    },
    async DeleteTopic(topicID: string) {
      deleteMockTopic(topicID);
    },
    async TrashTopic(topicID: string) {
      deleteMockTopic(topicID);
    },
    async SetTopicPinned(topicID: string, pinned: boolean) {
      setMockTopicPinned(topicID, pinned);
    },
    async SetSessionPinned(_path: string, _pinned: boolean) {
      // Browser mock has no persisted session sidecar.
    },
    async SaveWindowState(_state) {
      // no-op in browser dev — no real window geometry to persist
    },
    async ContextPanel(_tabID: string) {
      const now = Date.now();
      const currency = "¥";
      const cost = (usd: number) => currency === "¥" ? Number((usd * 7.15).toFixed(4)) : usd;
      return {
        usedTokens: 42124,
        windowTokens: 128000,
        promptTokens: 22134,
        completionTokens: 12345,
        totalTokens: 34479,
        reasoningTokens: 7521,
        cacheHitTokens: 87000,
        cacheMissTokens: 13000,
        sessionCacheHitTokens: 87000,
        sessionCacheMissTokens: 13000,
        sessionCompletionTokens: 12345,
        requestCount: 10,
        elapsedMs: 33 * 60 * 1000,
        sessionCost: cost(0.018),
        sessionCurrency: currency,
        sessionCostUsd: cost(0.018),
        sources: {
          executor: {
            promptTokens: 24100,
            completionTokens: 8300,
            totalTokens: 32400,
            reasoningTokens: 5200,
            cacheHitTokens: 76000,
            cacheMissTokens: 9000,
            requestCount: 4,
            sessionCost: cost(0.0124),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0124),
          },
          planner: {
            promptTokens: 1800,
            completionTokens: 600,
            totalTokens: 2400,
            reasoningTokens: 420,
            cacheHitTokens: 3400,
            cacheMissTokens: 700,
            requestCount: 1,
            sessionCost: cost(0.0011),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0011),
          },
          subagent: {
            promptTokens: 4200,
            completionTokens: 2100,
            totalTokens: 6300,
            reasoningTokens: 1500,
            cacheHitTokens: 6100,
            cacheMissTokens: 2100,
            requestCount: 2,
            sessionCost: cost(0.0032),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0032),
          },
          compaction: {
            promptTokens: 2600,
            completionTokens: 700,
            totalTokens: 3300,
            reasoningTokens: 260,
            cacheHitTokens: 1100,
            cacheMissTokens: 900,
            requestCount: 1,
            sessionCost: cost(0.0009),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0009),
          },
          classifier: {
            promptTokens: 900,
            completionTokens: 120,
            totalTokens: 1020,
            reasoningTokens: 70,
            cacheHitTokens: 300,
            cacheMissTokens: 250,
            requestCount: 1,
            sessionCost: cost(0.0003),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0003),
          },
          title: {
            promptTokens: 420,
            completionTokens: 80,
            totalTokens: 500,
            reasoningTokens: 20,
            cacheHitTokens: 100,
            cacheMissTokens: 50,
            requestCount: 1,
            sessionCost: cost(0.0001),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0001),
          },
        },
        mock: true,
        readFiles: [
          { path: "README.md", turn: 2, time: now - 34 * 60 * 1000 },
          { path: "go.mod", turn: 3, time: now - 30 * 60 * 1000 },
          { path: "desktop/file.go", turn: 5, time: now - 13 * 60 * 1000, offset: 0, limit: 180 },
          { path: "internal/event.go", turn: 6, time: now - 4 * 60 * 1000, offset: 120, limit: 80, truncated: true },
        ],
        changedFiles: [
          { path: t("mock.changedFile1Path"), sources: ["session"], gitStatus: "modified", turns: [5, 6], latestPrompt: t("mock.changedFile1Prompt"), latestTime: now - 2 * 60 * 1000 },
          { path: t("mock.changedFile2Path"), sources: ["session"], gitStatus: "added", turns: [6], latestPrompt: t("mock.changedFile2Prompt"), latestTime: now - 60 * 1000 },
        ],
      };
    },

    // ── Work surface ─────────────────────────────────────────────────────
    // Work features require the Go backend and are not available in the
    // browser dev mock. Every method returns a rejected Promise so the UI
    // can render error / unavailable states without faking successful side
    // effects. Callers that need a safe no-op for layout dev can wrap with
    // .catch(() => undefined).

    WorkEnabled: () => Promise.resolve(false),
    WorkCapable: () => Promise.resolve(false),
    WorkCollaborationV2Enabled: () => Promise.resolve(false),
    CreateWorkSession: () => Promise.reject(workUnavailableError()),
    CreateReusableWorkSession: () => Promise.reject(workUnavailableError()),
    CreateWork: () => Promise.reject(workUnavailableError()),
    GetWork: () => Promise.reject(workUnavailableError()),
    ListWorks: () => Promise.reject(workUnavailableError()),
    ListWorkBlueprints: () => Promise.reject(workUnavailableError()),
    CopyWork: () => Promise.reject(workUnavailableError()),
    PrepareReusableFlow: () => Promise.reject(workUnavailableError()),
    SaveReusableFlow: () => Promise.reject(workUnavailableError()),
    RunReusableFlow: () => Promise.reject(workUnavailableError()),
    UpdateDraft: () => Promise.reject(workUnavailableError()),
    UpsertWorkBlock: () => Promise.reject(workUnavailableError()),
    RecoverWorkView: () => Promise.reject(workUnavailableError()),
    RunWork: () => Promise.reject(workUnavailableError()),
    ResumeRun: () => Promise.reject(workUnavailableError()),
    PauseRun: () => Promise.reject(workUnavailableError()),
    CancelRun: () => Promise.reject(workUnavailableError()),
    RestartRun: () => Promise.reject(workUnavailableError()),
    RetryWorkTask: () => Promise.reject(workUnavailableError()),
    ArchiveWork: () => Promise.reject(workUnavailableError()),
    RestoreWork: () => Promise.reject(workUnavailableError()),
    DeleteWork: () => Promise.reject(workUnavailableError()),
    PrepareWorkRerun: () => Promise.reject(workUnavailableError()),
    ExecuteWorkRerun: () => Promise.reject(workUnavailableError()),
    WatchWork: () => Promise.reject(workUnavailableError()),
    UnwatchWork: () => Promise.reject(workUnavailableError()),
    PinCornerstone: () => Promise.reject(workUnavailableError()),
    RefreshCornerstone: () => Promise.reject(workUnavailableError()),
    RemoveCornerstone: () => Promise.reject(workUnavailableError()),
    UndoCornerstone: () => Promise.reject(workUnavailableError()),
    AcceptCornerstone: () => Promise.reject(workUnavailableError()),
    FreezeCornerstone: () => Promise.reject(workUnavailableError()),
    RepairCornerstone: () => Promise.reject(workUnavailableError()),
    SessionPurgeImpact: () => Promise.reject(workUnavailableError()),
    ForcePurgeTrashedSession: () => Promise.reject(workUnavailableError()),
    RetryCleanupPending: () => Promise.reject(workUnavailableError()),
    ListSessionCleanupPending: () => Promise.reject(workUnavailableError()),
    BeginWorkPlanning: () => Promise.reject(workUnavailableError()),
    ApplyDefinition: () => Promise.reject(workUnavailableError()),
    CreateCandidateRevision: () => Promise.reject(workUnavailableError()),
    RetryWorkNode: () => Promise.reject(workUnavailableError()),
    RetryArtifactSlot: () => Promise.reject(workUnavailableError()),
    PreviewArtifact: () => Promise.reject(workUnavailableError()),
    RequestArtifactConversion: () => Promise.reject(workUnavailableError()),
    SelectWorkInputFile: () => Promise.reject(workUnavailableError()),
    SelectWorkInformationFile: () => Promise.reject(workUnavailableError()),
    AddCustomWorkInput: () => Promise.reject(workUnavailableError()),
    InferWorkInputs: () => Promise.reject(workUnavailableError()),
    SubmitWorkInput: () => Promise.reject(workUnavailableError()),
    SetInputCornerstone: () => Promise.reject(workUnavailableError()),
    PreviewWorkPatch: () => Promise.reject(workUnavailableError()),
    ApplyWorkPatch: () => Promise.reject(workUnavailableError()),
    SetNodeSkill: () => Promise.reject(workUnavailableError()),
    ClearNodeSkill: () => Promise.reject(workUnavailableError()),
    ListWorkSkills: () => Promise.reject(workUnavailableError()),
    CreateWorkSkill: () => Promise.reject(workUnavailableError()),
  };
}
