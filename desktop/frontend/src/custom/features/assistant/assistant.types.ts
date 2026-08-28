export type AssistantLifecycle = "active" | "paused" | "archived";
export type AssistantMode = "finite" | "continuous";
export type AssistantScope = "global" | "workspace";
export type AssistantAccess = "deny" | "allow" | "approve";
export type AssistantRunState =
  | "queued"
  | "running"
  | "succeeded"
  | "waiting_approval"
  | "retry_wait"
  | "waiting_attention"
  | "failed"
  | "cancelled";
export type AssistantMemoryKind = "charter" | "facts" | "strategy" | "open_loops" | "metrics";
export type AssistantScheduleKind = "manual" | "interval" | "daily" | "weekly" | "biweekly" | "monthly" | "yearly";
export type AssistantAutoAnswer = "auto" | "ask";
export type AssistantResponsibilityDisposition = "planned" | "waiting" | "review" | "done" | "dropped";

export interface AssistantPolicy {
  local_write: AssistantAccess;
  network: AssistantAccess;
  publish: AssistantAccess;
  delete: AssistantAccess;
  payment: AssistantAccess;
  secrets: AssistantAccess;
  private_data: AssistantAccess;
  constraint_edit: AssistantAccess;
  max_concurrent_sessions?: number;
  auto_answer?: AssistantAutoAnswer;
  isolation?: AssistantAccess;
  external_voice_enabled?: boolean;
}

export interface AssistantRecord {
  id: string;
  name: string;
  description?: string;
  mission: string;
  mode?: AssistantMode;
  scope: AssistantScope;
  workspace_root?: string;
  lifecycle: AssistantLifecycle;
  policy: AssistantPolicy;
  memory_revision: number;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AssistantSchedule {
  kind: AssistantScheduleKind;
  interval_seconds?: number;
  timezone?: string;
  at?: string;
  weekday?: number;
  day?: number;
  month?: number;
  start_at?: string;
  window?: { start?: string; end?: string };
}

export interface AssistantRoutine {
  id: string;
  assistant_id: string;
  title: string;
  prompt: string;
  schedule: AssistantSchedule;
  enabled: boolean;
  catch_up: "coalesce_latest" | "skip";
  last_scheduled_for?: string;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AssistantRunError {
  code: string;
  message: string;
  provider?: string;
  retryable: boolean;
  outcome_known: boolean;
  at: string;
}

export interface AssistantRun {
  id: string;
  assistant_id: string;
  routine_id?: string;
  // prompt carries the frozen original user input for a direct-input run
  // ("对助手说") and the frozen routine prompt for a routine run.
  prompt?: string;
  scope?: AssistantScope;
  workspace_root?: string;
  request_id: string;
  trigger: "manual" | "scheduled" | "retry";
  state: AssistantRunState;
  resume_token?: string;
  attempt: number;
  max_attempts: number;
  session_path?: string;
  scheduled_for?: string;
  retry_at?: string;
  started_at?: string;
  finished_at?: string;
  summary?: string;
  error?: AssistantRunError;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AssistantMemoryItem {
  id: string;
  kind: AssistantMemoryKind;
  body: string;
  source_run?: string;
  evidence?: string;
  locked: boolean;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AssistantMemory {
  revision: number;
  items: AssistantMemoryItem[];
}

export interface AssistantMemoryPatch {
  upsert?: AssistantMemoryItem[];
  delete?: string[];
}

export interface AssistantAttentionItem {
  id: string;
  assistant_id: string;
  run_id?: string;
  request_id: string;
  action: string;
  summary: string;
  resume_token?: string;
  state: "open" | "approved" | "rejected" | "cancelled";
  resolution?: string;
  revision: number;
  created_at: string;
  updated_at: string;
}

export type AssistantResponsibilityStatus = "blocked" | "ready" | "active" | "done" | "failed";

export interface AssistantResponsibility {
  id: string;
  assistant_id: string;
  alias?: string;
  objective: string;
  done_criteria?: string;
  next_action?: string;
  status: AssistantResponsibilityStatus;
  disposition?: AssistantResponsibilityDisposition;
  depends_on?: string[];
  block_reason?: string;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AssistantArtifact {
  id: string;
  assistant_id: string;
  resp_id?: string;
  run_id?: string;
  title: string;
  kind?: string;
  content?: string;
  evidence?: string;
  revision: number;
  created_at: string;
}

export interface AssistantOpportunity {
  id: string;
  assistant_id: string;
  resp_id?: string;
  run_id?: string;
  reason?: string;
  revision: number;
  created_at: string;
}

export type AssistantProposalTarget = "routine" | "channel";
export type AssistantProposalState = "pending" | "applied" | "rejected" | "superseded";

export interface AssistantRoutineProposalPatch {
  prompt?: string;
  schedule?: AssistantSchedule;
  enabled?: boolean;
}

export interface AssistantChannelProposalPatch {
  collect_interval_seconds?: number;
  enabled?: boolean;
}

export interface AssistantChangeProposal {
  id: string;
  assistant_id: string;
  run_id: string;
  target_kind: AssistantProposalTarget;
  target_id: string;
  base_revision: number;
  routine?: { before: AssistantRoutineProposalPatch; after: AssistantRoutineProposalPatch };
  channel?: { before: AssistantChannelProposalPatch; after: AssistantChannelProposalPatch };
  summary: string;
  reason: string;
  evidence: string[];
  state: AssistantProposalState;
  resolution?: string;
  revision: number;
  created_at: string;
  updated_at: string;
  resolved_at?: string;
}

export interface AssistantChannel {
  id: string;
  assistant_id: string;
  name: string;
  kind: "discourse";
  base_url: string;
  username: string;
  credential_key: string;
  category_id?: number;
  collect_interval_seconds: number;
  enabled: boolean;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AssistantChannelAction {
  id: string;
  assistant_id: string;
  channel_id: string;
  request_id: string;
  kind: "create_topic" | "reply_topic";
  title?: string;
  body: string;
  external_topic_id?: number;
  external_post_id?: number;
  url?: string;
  state: "executing" | "succeeded" | "failed" | "unknown";
  error?: string;
  collect_error?: string;
  next_collect_at?: string;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AssistantChannelMetric {
  id: string;
  assistant_id: string;
  channel_id: string;
  action_id: string;
  topic_id: number;
  views: number;
  likes: number;
  replies: number;
  views_delta: number;
  likes_delta: number;
  reply_delta: number;
  collected_at: string;
}

export interface AssistantPlan {
  revision: number;
  responsibilities: AssistantResponsibility[];
}

export type AssistantDispatchKind = "task" | "question" | "feedback" | "improvement" | "correction" | "control";
export type AssistantDispatchState = "pending_classification" | "classified" | "classification_failed" | "reflected" | "reflection_failed";
export type AssistantDispatchStreamPhase = "accepted" | "streaming" | "committed" | "failed";

export interface AssistantDispatchStreamEvent {
  assistantId: string;
  dispatchId: string;
  requestId: string;
  sequence: number;
  phase: AssistantDispatchStreamPhase;
  reply?: string;
  jobCount?: number;
  revision?: number;
  error?: string;
}

export interface AssistantDispatch {
  id: string;
  assistant_id: string;
  request_id: string;
  input: string;
  kind?: AssistantDispatchKind;
  reply?: string;
  state: AssistantDispatchState;
  error?: AssistantRunError;
  retry_at?: string;
  classification_attempt?: number;
  reflection_attempt?: number;
  revision: number;
  created_at: string;
  updated_at: string;
  classified_at?: string;
}

export type AssistantJobState = "queued" | "running" | "succeeded" | "retry_wait" | "waiting_attention" | "failed" | "cancelled";

export interface AssistantRunnerJob {
  id: string;
  assistant_id: string;
  dispatch_id: string;
  name: string;
  kind: AssistantDispatchKind;
  target?: string;
  prompt: string;
  scope?: AssistantScope;
  workspace_root?: string;
  session_path?: string;
  policy: AssistantPolicy;
  context_pack_revision?: number;
  state: AssistantJobState;
  attempt: number;
  max_attempts: number;
  lease_owner?: string;
  lease_fence?: number;
  lease_until?: string;
  retry_at?: string;
  started_at?: string;
  finished_at?: string;
  summary?: string;
  error?: AssistantRunError;
  revision: number;
  created_at: string;
  updated_at: string;
}

export interface AssistantContextPack {
  id: string;
  assistant_id: string;
  dispatch_id: string;
  revision: number;
  conclusion: string;
  evidence?: string[];
  failures?: string[];
  strategies?: string[];
  open_loops?: string[];
  runner_context?: string;
  bound_job_ids?: string[];
  created_at: string;
}

export type AssistantIdeaTrigger = "manual" | "cadence";
export type AssistantIdeaState = "pending" | "accepted" | "rejected" | "superseded";
export type AssistantIdeaDecision = "accept" | "reject";

export interface AssistantIdeaProposal {
  id: string;
  assistant_id: string;
  request_id: string;
  trigger: AssistantIdeaTrigger;
  summary: string;
  rationale?: string;
  strategy_memory?: string;
  responsibility?: string;
  objective?: string;
  done_criteria?: string;
  next_action?: string;
  state: AssistantIdeaState;
  resolution?: string;
  revision: number;
  created_at: string;
  updated_at: string;
  resolved_at?: string;
}

export interface AssistantSnapshot {
  revision: number;
  assistant: AssistantRecord;
  routines: AssistantRoutine[];
  memory: AssistantMemory;
  runs: AssistantRun[];
  attention: AssistantAttentionItem[];
  plan: AssistantPlan;
  artifacts: AssistantArtifact[];
  opportunities: AssistantOpportunity[];
  proposals?: AssistantChangeProposal[];
  channels: AssistantChannel[];
  channel_actions: AssistantChannelAction[];
  channel_metrics: AssistantChannelMetric[];
  dispatches?: AssistantDispatch[];
  jobs?: AssistantRunnerJob[];
  context_packs?: AssistantContextPack[];
  ideas?: AssistantIdeaProposal[];
  updated_at: string;
}

export interface AssistantDiagnostic {
  at: string;
  category?: "data" | "runtime";
  operation: string;
  message: string;
}

export interface AssistantListResult {
  items: AssistantRecord[];
  diagnostics: AssistantDiagnostic[];
}

export interface AssistantCreateInput {
  requestId: string;
  assistant: AssistantRecord;
  routines: AssistantRoutine[];
  initialPrompt?: string;
}

export interface AssistantUpdateInput {
  requestId: string;
  expectedRevision: number;
  assistant: AssistantRecord;
}

export interface AssistantDeleteInput {
  assistantId: string;
  requestId: string;
  expectedRevision: number;
}

export interface AssistantRoutineInput {
  requestId: string;
  expectedRevision: number;
  routine: AssistantRoutine;
}

export interface AssistantMemoryInput {
  assistantId: string;
  requestId: string;
  expectedRevision: number;
  patch: AssistantMemoryPatch;
}

export interface AssistantChannelInput {
  requestId: string;
  expectedRevision: number;
  channel: AssistantChannel;
  apiKey?: string;
}

export interface AssistantRunNowInput {
  assistantId: string;
  routineId?: string;
  requestId: string;
  maxAttempts?: number;
}

export interface AssistantSubmitInputInput {
  assistantId: string;
  requestId: string;
  input: string;
  maxAttempts?: number;
}

export interface AssistantResolveAttentionInput {
  assistantId: string;
  attentionId: string;
  requestId: string;
  expectedRevision: number;
  state: "approved" | "rejected" | "cancelled";
  resolution: string;
}

export interface AssistantResolveProposalInput {
  assistantId: string;
  proposalId: string;
  requestId: string;
  expectedRevision: number;
  decision: "accept" | "reject";
  resolution?: string;
}

export interface AssistantResumeInput {
  runId: string;
  requestId: string;
}

export interface AssistantCancelInput {
  runId: string;
  requestId: string;
  reason: string;
}

export interface AssistantSubmitInput {
  assistantId: string;
  requestId: string;
  input: string;
}

export interface AssistantRetryDispatchInput {
  assistantId: string;
  dispatchId: string;
  requestId: string;
}

export interface AssistantIdeateInput {
  assistantId: string;
  requestId: string;
}

export interface AssistantResolveIdeaInput {
  assistantId: string;
  ideaId: string;
  requestId: string;
  expectedRevision: number;
  decision: AssistantIdeaDecision;
  resolution?: string;
}

export interface AssistantRetryJobInput {
  jobId: string;
  requestId: string;
}

export interface AssistantCancelJobInput {
  jobId: string;
  requestId: string;
  reason: string;
}

export const DEFAULT_ASSISTANT_POLICY: AssistantPolicy = {
  local_write: "deny",
  network: "deny",
  publish: "approve",
  delete: "approve",
  payment: "approve",
  secrets: "approve",
  private_data: "approve",
  constraint_edit: "approve",
};

export function assistantRequestID(action: string, id = ""): string {
  const random = typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 9)}`;
  return `desktop-assistant:${action}:${id || "new"}:${random}`;
}

export function assistantEntityID(prefix: string): string {
  const random = typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 9)}`;
  return `${prefix}-${random}`;
}

export type AssistantWorkControlState = "running" | "quiescing" | "paused" | "recovering";

export interface AssistantActiveWork {
  kind: string;
  id: string;
  state: string;
}

export interface AssistantWorkControl {
  state: AssistantWorkControlState;
  epoch: number;
  revision: number;
  active: AssistantActiveWork[];
  next_hint: string;
  error?: string;
}

export interface AssistantWorkControlInput {
  requestId: string;
}

export interface AssistantViewportSnapshot {
  window_id: string;
  workspace_id?: string;
  visible_session_ids?: string[];
  selected_session_id?: string;
  observed_at: string;
  ui_revision?: number;
}

export interface AssistantPublishViewportInput {
  windowId: string;
  workspaceId?: string;
  visibleSessionIds?: string[];
  selectedSessionId?: string;
  uiRevision?: number;
}

// ── 受管 Session 视图与控制 ───────────────────────────────────

export type AssistantSessionControlOutcome =
  | "accepted"
  | "already_applied"
  | "stale"
  | "retryable_error"
  | "invalid"
  | "blocked_by_policy";

export interface AssistantManagedSession {
  id: string;
  path: string;
  title: string;
  preview: string;
  status: string;
  turns: number;
  owner_id: string;
  purpose: string;
  responsibility_id?: string;
  workspace_root?: string;
  updated_at: string;
}

export interface AssistantSessionControlResult {
  outcome: AssistantSessionControlOutcome;
  session_id?: string;
  session_status?: string;
  revision?: number;
  next_hint?: string;
  message?: string;
  at: string;
}

export interface AssistantAskOption {
  label: string;
  description?: string;
}

export interface AssistantAskQuestion {
  id: string;
  header?: string;
  prompt: string;
  options?: AssistantAskOption[];
  multi?: boolean;
}

export interface AssistantSessionInteraction {
  kind: "ask" | "approval";
  id: string;
  questions?: AssistantAskQuestion[];
  due_at?: string;
}

export interface AssistantSessionStatusView {
  id: string;
  path: string;
  title: string;
  status: string;
  turns: number;
  purpose: string;
  running: boolean;
  updated_at: string;
  interactions?: AssistantSessionInteraction[];
}

export interface AssistantSteerRequest {
  sessionId: string;
  text: string;
  requestId: string;
}

export interface AssistantAnswerRequest {
  sessionId: string;
  interactionId: string;
  answers: Array<{ questionId: string; selected: string[] }>;
  requestId: string;
}

export interface AssistantSessionRequest {
  sessionId: string;
  requestId: string;
}

// ── 监督循环诊断 ──────────────────────────────────────────────

export interface AssistantSupervisorRef {
  id: string;
  path: string;
}

export interface AssistantCycleObservation {
  plan_revision: number;
  assistant_revision: number;
  memory_revision: number;
  work_epoch: number;
}

export interface AssistantCycleView {
  id: string;
  fence: number;
  state: string;
  observed: AssistantCycleObservation;
  next_step?: string;
  revision: number;
  updated_at: string;
}

export interface AssistantEventView {
  id: string;
  kind: string;
  session_id?: string;
  revision?: number;
  request_id?: string;
  payload?: string;
  at: string;
}

export interface AssistantDecisionView {
  id: string;
  session_id: string;
  interaction_id: string;
  source?: string;
  confidence?: number;
  result?: string;
  winner?: string;
  rollback?: string;
  due_at?: string;
  created_at: string;
}

export interface AssistantReceiptView {
  request_id: string;
  operation: string;
  created_at: string;
}

export interface AssistantSupervisorSessionView {
  id: string;
  title: string;
  status: string;
  purpose?: string;
}

export interface AssistantSupervisorDiagnostic {
  assistant_id: string;
  supervisor?: AssistantSupervisorRef;
  cycle?: AssistantCycleView;
  pending_events?: AssistantEventView[];
  recent_decisions?: AssistantDecisionView[];
  recent_receipts?: AssistantReceiptView[];
  next_step?: string;
  running_sessions?: AssistantSupervisorSessionView[];
  failed_sessions?: AssistantSupervisorSessionView[];
  retry_due: number;
  diagnostics?: AssistantDiagnostic[];
  at: string;
}
