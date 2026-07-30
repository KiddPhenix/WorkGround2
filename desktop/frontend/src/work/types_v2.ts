// V2 contract mirror for internal/work. JSON field names are part of the
// cross-frontend contract; keep them aligned with Go tags and golden fixtures.
//
// V1 types remain in types.ts and are immutable. V2 types supplement V1.

import type { ArtifactRef, AttemptReceipt } from './types';

export const WORK_SCHEMA_VERSION_V2 = 2;
export const WORK_VIEW_SCHEMA_VERSION_V2 = 2;

// ── V2 definition model ────────────────────────────────────────────────────

export type DefinitionStatus = 'draft' | 'active' | 'superseded';

export interface WorkDefinitionRevision {
  workId: string;
  revision: number;
  parentRevision: number;
  status: DefinitionStatus;
  goal: string;
  nodes: NodeDef[];
  artifactSlots: ArtifactSlotDef[];
  inputSpecs: InputSpec[];
  createdBy: string;
  createdAt: string;
  digest: string;
}

export interface NodeDef {
  id: string;
  title: string;
  description?: string;
  dependsOn?: string[];
  inputSpecIds?: string[];
  toolHints?: string[];
  blockIds?: string[];
  producesSlotIds?: string[];
  consumesSlotIds?: string[];
  globalGate?: string;
}

export interface ArtifactSlotDef {
  id: string;
  title: string;
  kind: string;
  expectedCount: number;
  required: boolean;
}

export type InputKind =
  | 'text'
  | 'number'
  | 'date'
  | 'choice'
  | 'multi_choice'
  | 'file'
  | 'roster'
  | 'form'
  | 'approval';

export interface InputSpec {
  id: string;
  label: string;
  description?: string;
  kind: InputKind;
  required: boolean;
  valueSchema?: unknown;
  defaultValue?: unknown;
  pinEligible: boolean;
}

// ── V2 artifact slots ──────────────────────────────────────────────────────

export type ArtifactSlotState =
  | 'reserved'
  | 'generating'
  | 'ready'
  | 'partial'
  | 'failed'
  | 'stale';

export interface ArtifactSlot {
  id: string;
  workId: string;
  definitionRev: number;
  upstreamDigest?: string;
  title: string;
  kind: string;
  expectedCount: number;
  required: boolean;
  state: ArtifactSlotState;
  artifactRefs: import('./types.js').ArtifactRef[];
  progress?: number;
  summary?: string;
  error?: ArtifactError;
  revision: number;
}

export interface ArtifactError {
  code: string;
  message: string;
  retryable: boolean;
}

// ── V2 typed inputs ────────────────────────────────────────────────────────

export type InputState =
  | 'requested'
  | 'draft'
  | 'submitted'
  | 'rejected'
  | 'accepted';

export interface WorkInput {
  id: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  specId: string;
  value: unknown;
  state: InputState;
  cornerstoneId?: string;
  error?: string;
  source?: string;
  updatedBy?: string;
  revision: number;
  updatedAt: string;
}

// ── V2 discussion patches ──────────────────────────────────────────────────

export type PatchScope = 'block' | 'workflow';

export interface PatchOp {
  op: string;
  path: string;
  oldValue?: unknown;
  newValue?: unknown;
}

export type PatchActionKind = 'reuse' | 'reformat' | 'rerun' | 'ask_user';

export interface PatchAction {
  action: PatchActionKind;
  nodeId?: string;
  artifactSlotId?: string;
  question?: string;
  reason?: string;
}

export interface WorkPatchPreview {
  id: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  sessionId: string;
  baseDefinitionRev: number;
  baseBlockRev: number;
  scope: PatchScope;
  operations: PatchOp[];
  actions?: PatchAction[];
  affectedNodeIds: string[];
  affectedBlockIds: string[];
  affectedArtifactSlotIds: string[];
  staleArtifactSlotIds: string[];
  invalidatedTaskIds: string[];
  requiresRerun: boolean;
  digest: string;
  expiresAt: string;
}

// ── V2 Task states ─────────────────────────────────────────────────────────

export type TaskStateV2 =
  | 'pending'
  | 'ready'
  | 'running'
  | 'waiting_input'
  | 'waiting_approval'
  | 'completed'
  | 'failed_retryable'
  | 'failed_terminal'
  | 'canceled'
  | 'invalidated';

// ── V2 Controller contract (frozen DTOs, no implementation) ────────────────

export interface SubmitWorkInputRequest {
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  inputId: string;
  value: unknown;
  definitionRevision: number;
  inputRevision: number;
  expectedRevision: number;
  requestId: string;
}

export interface InputSubmissionResult {
  input: WorkInput;
  revision: number;
  duplicate: boolean;
  error?: string;
}

export interface SetInputCornerstoneRequest {
  workId: string;
  inputId: string;
  pin: boolean;
  definitionRevision: number;
  inputRevision: number;
  expectedRevision: number;
  requestId: string;
}

export interface CornerstonePinResult {
  cornerstoneId?: string;
  pinned: boolean;
  revision: number;
  duplicate: boolean;
  error?: string;
  committed: boolean;
  recoverable: boolean;
  transportError?: WorkTransportError;
  receipt?: InputIntentReceipt;
}

export interface SubmitInputResult {
  input?: WorkInput;
  revision: number;
  duplicate: boolean;
  affectedTaskIds?: string[];
  error?: string;
  committed: boolean;
  recoverable: boolean;
  transportError?: WorkTransportError;
  receipt?: InputIntentReceipt;
}

export interface PreviewWorkPatchRequest {
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  sessionId: string;
  instruction: string;
  definitionRevision: number;
  blockRevision: number;
  scope: PatchScope;
  requestId: string;
}

export interface ApplyWorkPatchRequest {
  workId: string;
  patchId: string;
  previewDigest: string;
  scope: PatchScope;
  expectedRevision: number;
  requestId: string;
}

export interface ApplyWorkPatchResult {
  workRevision: number;
  newRevision: number;
  invalidatedTaskIds?: string[];
  affectedBlockIds?: string[];
  affectedArtifactSlotIds?: string[];
  staleArtifactSlotIds?: string[];
  requiresRerun: boolean;
  duplicate: boolean;
  error?: string;
  committed: boolean;
  recoverable: boolean;
  transportError?: WorkTransportError;
  receipt?: PatchIntentReceipt;
}

export interface BeginWorkPlanningInput {
  sessionId: string;
  requestId: string;
  locale: 'en' | 'zh' | 'zh-TW';
}

export interface BeginWorkPlanningResult {
  result?: import('./types.js').WorkView;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  transportError?: WorkTransportError;
}

export interface ApplyDefinitionInput {
  workId: string;
  revision: number;
  expectedRevision: number;
  requestId: string;
}

export interface CreateCandidateRevisionInput {
  workId: string;
  intent: string;
  baseDefinitionRevision: number;
  expectedRevision: number;
  requestId: string;
  inferName?: boolean;
  structuralAnswers?: DefinitionStructuralAnswer[];
}

export type DefinitionStructureImpact =
  | 'task_nodes'
  | 'task_dependencies'
  | 'input_slots'
  | 'artifact_slots';

export interface DefinitionStructuralAnswer {
  questionId: string;
  optionId?: string;
  value: string;
}

export interface DefinitionStructuralOption {
  id: string;
  label: string;
  description?: string;
  recommended?: boolean;
  custom?: boolean;
}

export interface DefinitionStructuralClarification {
  id: string;
  impact: DefinitionStructureImpact;
  question: string;
  description?: string;
  flow: string[];
  options: DefinitionStructuralOption[];
  customPlaceholder?: string;
}

export interface DefinitionPlanProgress {
  requestId: string;
  sequence: number;
  kind: 'analyzing' | 'raw' | 'node' | 'dependency' | 'clarification' | 'complete';
  text: string;
  state: 'streaming' | 'waiting' | 'complete';
}

export interface CreateCandidateRevisionResult {
  candidate?: WorkDefinitionRevision;
  clarification?: DefinitionStructuralClarification;
  impact?: RunImpact;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  transportError?: WorkTransportError;
}

export interface AutoSwitchFaceIntent {
  workId: string;
  runId: string;
  definitionRev: number;
  reason: string;
}

export interface RunImpact {
  keptNodeIds: string[];
  invalidatedNodeIds: string[];
  newNodeIds: string[];
  removedNodeIds: string[];
  requiresRerun: boolean;
}

export interface ApplyDefinitionResult {
  view?: import('./types.js').WorkView;
  intent?: AutoSwitchFaceIntent;
  impact?: RunImpact;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  transportError?: WorkTransportError;
}

export interface PreviewWorkPatchResult {
  preview?: WorkPatchPreview;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  error?: string;
  transportError?: WorkTransportError;
  receipt?: PatchIntentReceipt;
}

export interface RetryWorkNodeRequest {
  workId: string;
  runId: string;
  taskId: string;
  expectedRevision: number;
  requestId: string;
}

export interface WorkTransportError {
  code: string;
  message: string;
  operation?: string;
  workId?: string;
  requestId?: string;
  revision?: number;
  committed: boolean;
  recoverable: boolean;
}

export interface RetryWorkNodeResult {
  result?: import('./types.js').Task;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  error?: WorkTransportError;
}

export interface RetryArtifactSlotRequest {
  workId: string;
  slotId: string;
  definitionRevision: number;
  expectedRevision: number;
  requestId: string;
}

export interface RetryArtifactSlotResult {
  slot?: ArtifactSlot;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  transportError?: WorkTransportError;
}

// ── Artifact Preview ───────────────────────────────────────────────────────

export type PreviewGrade = 'inline' | 'filecard' | 'fallback';

export interface ArtifactPreview {
  artifactId: string;
  workId: string;
  contentDigest?: string;
  grade: PreviewGrade;
  mimeType?: string;
  textContent?: string;
  dataURL?: string;
  pdfRaw?: string;
  summary?: string;
  thumbnailDataURL?: string;
  pageCount?: number;
  sheetNames?: string[];
  fileSize?: number;
  canOpen: boolean;
  canConvert: boolean;
  conversionState?: string;
  cachedAt?: string;
  converterVersion?: string;
  error?: string;
}

export interface PreviewArtifactRequest {
  workId: string;
  definitionRevision: number;
  slotId: string;
  slotRevision: number;
  artifactId: string;
  requestId: string;
}

export interface PreviewArtifactResult {
  preview?: ArtifactPreview;
  committed: boolean;
  recoverable: boolean;
  transportError?: WorkTransportError;
}

export interface RequestArtifactConversionInput {
  workId: string;
  definitionRevision: number;
  slotId: string;
  slotRevision: number;
  artifactId: string;
  requestId: string;
  allowExternal: boolean;
  approvalToken: string;
}

export interface RequestArtifactConversionResult {
  preview?: ArtifactPreview;
  committed: boolean;
  recoverable: boolean;
  duplicate: boolean;
  transportError?: WorkTransportError;
}

export interface SelectWorkInputFileRequest {
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  inputId: string;
  specId: string;
}

export interface SelectWorkInputFileResult {
  artifactRef?: ArtifactRef;
  canceled: boolean;
  error?: WorkTransportError;
}

// ── V2 WorkView extension ──────────────────────────────────────────────────

export interface WorkViewV2 {
  schemaVersion: number;
  work: import('./types.js').Work;
  revision: number;
  assessment: import('./types.js').CornerstoneAssessment;
  runBlock?: import('./types.js').RunBlockReason;
  definition?: WorkDefinitionRevision;
  artifactSlots?: ArtifactSlot[];
  tasks?: TaskV2View[];
  inputs?: WorkInput[];
  patchPreviews?: WorkPatchPreview[];
}

export interface TaskV2View {
  id: string;
  runId: string;
  nodeId: string;
  title: string;
  state: TaskStateV2;
  progress?: string;
  sessionRef?: import('./types.js').SessionRef;
  waitingInputIds?: string[];
  error?: string;
  retryable: boolean;
  updatedAt: string;
}

// ── V2 event types ─────────────────────────────────────────────────────────

export type V2WorkEventType =
  | 'definition.planning_started'
  | 'definition.revision_created'
  | 'definition.revision_applied'
  | 'artifact_slot.declared'
  | 'artifact_slot.updated'
  | 'input.requested'
  | 'input.draft_saved'
  | 'input.submitted'
  | 'input.rejected'
  | 'input.cornerstone_changed'
  | 'discussion.patch_previewed'
  | 'discussion.patch_applied'
  | 'task.invalidated'
  | 'task.ready'
  | 'task.waiting_input'
  | 'task.waiting_approval'
  | 'task.runtime_created'
  | 'task.runtime_updated'
  | 'task.stale_result';

export type V2ObjectKind = 'definition' | 'artifact_slot' | 'input' | 'patch' | 'task';

export const V2_OBJECT_KINDS: ReadonlySet<string> = new Set([
  'definition',
  'artifact_slot',
  'input',
  'patch',
  'task',
]);

export const V2_EVENT_TYPES: ReadonlySet<string> = new Set([
  'definition.planning_started',
  'definition.revision_created',
  'definition.revision_applied',
  'artifact_slot.declared',
  'artifact_slot.updated',
  'input.requested',
  'input.draft_saved',
  'input.submitted',
  'input.rejected',
  'input.cornerstone_changed',
  'discussion.patch_previewed',
  'discussion.patch_applied',
  'task.invalidated',
  'task.ready',
  'task.waiting_input',
  'task.waiting_approval',
  'task.runtime_created',
  'task.runtime_updated',
  'task.stale_result',
]);

// ── V2 event payloads ──────────────────────────────────────────────────────

export interface DefPlanningStartedPayload {
  workId: string;
  sessionId: string;
}

export interface DefRevisionCreatedPayload {
  workId: string;
  revision: number;
  parentRevision: number;
  digest: string;
}

export interface DefRevisionAppliedPayload {
  workId: string;
  revision: number;
  previousRevision: number;
  expectedRevision: number;
  invalidatedTasks?: string[];
}

export interface ArtifactSlotDeclaredPayload {
  slotId: string;
  workId: string;
  definitionRev: number;
  title: string;
  kind: string;
  expectedCount: number;
  required: boolean;
}

export interface ArtifactSlotUpdatedPayload {
  slotId: string;
  workId: string;
  state: ArtifactSlotState;
  refs?: import('./types.js').ArtifactRef[];
  upstreamDigest?: string;
  progress?: number;
  summary?: string;
  error?: ArtifactError;
  revision: number;
}

export interface InputRequestedPayload {
  inputId: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  specId: string;
  receipt?: InputIntentReceipt;
}

export interface InputDraftSavedPayload {
  inputId: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  specId: string;
  value: unknown;
  source?: string;
  updatedBy?: string;
  revision: number;
  expectedRevision: number;
  receipt?: InputIntentReceipt;
}

export interface InputSubmittedPayload {
  inputId: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  specId: string;
  value: unknown;
  source?: string;
  updatedBy?: string;
  revision: number;
  expectedRevision: number;
  affectedTaskIds?: string[];
  receipt?: InputIntentReceipt;
}

export interface InputRejectedPayload {
  inputId: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  specId: string;
  value: unknown;
  reason?: string;
  source?: string;
  updatedBy?: string;
  revision: number;
  expectedRevision: number;
  receipt?: InputIntentReceipt;
}

export interface InputCornerstoneChangedPayload {
  inputId: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  specId: string;
  cornerstoneId: string;
  pinned: boolean;
  revision: number;
  expectedRevision: number;
  receipt?: InputIntentReceipt;
}

export interface InputIntentReceipt {
  requestId: string;
  operation: string;
  intentDigest: string;
  inputId: string;
  resultRevision: number;
  resultDigest: string;
  affectedTaskIds?: string[];
  cornerstoneId?: string;
  resultInput?: WorkInput;
  pinned?: boolean;
  error?: string;
  createdAt: string;
}

export interface PatchPreviewedPayload {
  patchId: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  sessionId: string;
  scope: PatchScope;
  baseDefinitionRev?: number;
  baseBlockRev?: number;
  operations?: PatchOp[];
  actions?: PatchAction[];
  affectedNodeIds: string[];
  affectedBlockIds: string[];
  affectedArtifactSlotIds: string[];
  staleArtifactSlotIds: string[];
  invalidatedTasks: string[];
  requiresRerun: boolean;
  digest?: string;
  expiresAt?: string;
  receipt?: PatchIntentReceipt;
}

export interface PatchAppliedPayload {
  patchId: string;
  workId: string;
  runId: string;
  taskId: string;
  blockId: string;
  scope: PatchScope;
  newRevision: number;
  expectedRevision: number;
  invalidatedTaskIds: string[];
  receipt?: PatchIntentReceipt;
}

export interface PatchIntentReceipt {
  requestId: string;
  operation: string;
  intentDigest: string;
  patchId: string;
  resultRevision: number;
  resultDigest: string;
  resultPatch?: WorkPatchPreview;
  scope?: PatchScope;
  newRevision?: number;
  invalidatedTaskIds?: string[];
  affectedBlockIds?: string[];
  affectedArtifactSlotIds?: string[];
  staleArtifactSlotIds?: string[];
  requiresRerun: boolean;
  error?: string;
  createdAt: string;
}

export interface TaskInvalidatedPayload {
  taskId: string;
  workId: string;
  runId: string;
  reason?: string;
}

export interface TaskReadyPayload {
  taskId: string;
  workId: string;
  runId: string;
}

export interface TaskWaitingPayload {
  taskId: string;
  workId: string;
  runId: string;
  inputIds?: string[];
  approvalToken?: string;
}

// ── Task runtime types ─────────────────────────────────────────────────────

export interface V2TaskRuntime {
  taskId: string;
  workId: string;
  runId: string;
  nodeId: string;
  definitionRev: number;
  state: TaskStateV2;
  inputDigest?: string;
  dependencyDigest?: string;
  executionToken?: string;
  sideEffectClass?: string;
  attempts?: V2Attempt[];
  error?: string;
  waitingInputIds?: string[];
  approvalToken?: string;
  revision: number;
  updatedAt: string;
}

export interface V2Attempt {
  id: string;
  requestId?: string;
  index: number;
  state: TaskStateV2;
  startedAt: string;
  finishedAt?: string;
  definitionRev: number;
  inputDigest?: string;
  dependencyDigest?: string;
  executionToken?: string;
  sideEffectClass?: string;
  error?: string;
  receipt?: AttemptReceipt;
  resultRef?: string;
  staleResult?: boolean;
}

export interface TaskRuntimeCreatedPayload {
  taskId: string;
  workId: string;
  runId: string;
  nodeId: string;
  expectedRevision: number;
  definitionRev: number;
  sideEffectClass?: string;
  inputDigest?: string;
  dependencyDigest?: string;
  executionToken?: string;
  runtime: V2TaskRuntime;
}

export interface TaskRuntimeUpdatedPayload {
  taskId: string;
  workId: string;
  runId: string;
  expectedRevision: number;
  state: TaskStateV2;
  runtime: V2TaskRuntime;
  attempt?: V2Attempt;
}

export interface TaskStaleResultPayload {
  taskId: string;
  workId: string;
  runId: string;
  expectedRevision: number;
  attemptId: string;
  staleToken: string;
  currentToken: string;
  resultRef?: string;
  previousReceipt?: AttemptReceipt;
}
