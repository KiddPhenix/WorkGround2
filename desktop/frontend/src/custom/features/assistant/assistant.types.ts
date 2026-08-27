export type AssistantLifecycle = "active" | "paused" | "archived";
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

export interface AssistantPolicy {
  local_write: AssistantAccess;
  network: AssistantAccess;
  publish: AssistantAccess;
  delete: AssistantAccess;
  payment: AssistantAccess;
  secrets: AssistantAccess;
  private_data: AssistantAccess;
}

export interface AssistantRecord {
  id: string;
  name: string;
  description?: string;
  mission: string;
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
