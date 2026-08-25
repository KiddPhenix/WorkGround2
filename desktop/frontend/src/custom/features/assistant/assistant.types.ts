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

export interface AssistantPlan {
  revision: number;
  responsibilities: AssistantResponsibility[];
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
  updated_at: string;
}

export interface AssistantDiagnostic {
  at: string;
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

export interface AssistantResumeInput {
  runId: string;
  requestId: string;
}

export interface AssistantCancelInput {
  runId: string;
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
