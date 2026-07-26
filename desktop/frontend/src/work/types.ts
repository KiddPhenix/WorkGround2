// Go contract mirror for internal/work. JSON field names are part of the V1
// cross-frontend contract; keep them aligned with Go tags and golden fixtures.

export const WORK_SCHEMA_VERSION = 1;
export const WORK_VIEW_SCHEMA_VERSION = 1;

export type WorkState =
  | 'draft'
  | 'ready'
  | 'running'
  | 'waiting_user'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'cancelled';
export type WorkArchiveState = 'active' | 'archived' | 'deleted';
export type RunState =
  | 'pending'
  | 'running'
  | 'waiting'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'needs_confirmation';
export type BlueprintSource = 'system' | 'user' | `addon:${string}`;

export interface BlueprintRef {
  id: string;
  schemaVersion: number;
  version: number;
}

export interface ToolContractRef {
  name: string;
  contractVersion: number;
  provider?: string;
  sideEffectClass: 'read' | 'workspace_write' | 'external_write' | 'destructive';
  required: boolean;
}

export interface RuntimeFingerprint {
  workSchemaVersion: number;
  eventSchemaVersion: number;
  rendererSetVersion: number;
  toolContracts?: ToolContractRef[];
  provider?: string;
  model?: string;
}

export interface WorkBlueprint {
  schemaVersion: number;
  id: string;
  version: number;
  name: string;
  description: string;
  source: BlueprintSource;
  inputSchema?: unknown;
  promptTemplate: string;
  workflow: WorkflowDef;
  blockSpecs: BlockSpec[];
  cornerstoneRequirements?: CornerstoneReq[];
  conclusionKinds?: ConclusionKind[];
  artifactKinds?: string[];
  toolContracts?: ToolContractRef[];
  createdAt: string;
}

export interface WorkflowDef {
  stages: StageSpec[];
}

export interface StageSpec {
  id: string;
  title: string;
  tasks: TaskSpec[];
  gate?: 'input' | 'approval';
}

export interface TaskSpec {
  id: string;
  title: string;
}

export interface WorkDefinitionSnapshot {
  schemaVersion: number;
  revision: number;
  blueprintRef: BlueprintRef;
  inputSchema?: unknown;
  promptTemplate: string;
  workflow: WorkflowDef;
  blockSpecs: BlockSpec[];
  cornerstoneRequirements?: CornerstoneReq[];
  conclusionKinds?: ConclusionKind[];
  artifactKinds?: string[];
  toolContracts?: ToolContractRef[];
  digest: string;
}

export interface Work {
  schemaVersion: number;
  id: string;
  name: string;
  state: WorkState;
  archiveState: WorkArchiveState;
  blueprintRef: BlueprintRef;
  definitionSnapshot: WorkDefinitionSnapshot;
  inputs?: Record<string, unknown>;
  blocks: BlockInstance[];
  placements: BlockPlacement[];
  prompt: string;
  cornerstones: Cornerstone[];
  runs: WorkflowRun[];
  actionReceipts?: ActionReceipt[];
  conclusions?: Conclusion[];
  rerunOf?: string;
  copiedFrom?: string;
  referencedWorks?: string[];
  rerunUpgraded?: boolean;
  migrationPath?: number[];
  createdWith: RuntimeFingerprint;
  createdAt: string;
  updatedAt: string;
  archivedAt?: string;
}

export interface WorkRecord {
  archiveSchemaVersion: number;
  workId: string;
  snapshot: Work;
  rendererSetVersion: number;
  fallbackBlocks: BlockFallback[];
  archivedAt: string;
}

export interface WorkflowRun {
  id: string;
  workId: string;
  requestId?: string;
  definitionDigest: string;
  state: RunState;
  stages: Stage[];
  startedAt: string;
  finishedAt?: string;
  conclusion?: Conclusion;
  cancel?: RunCancelReceipt;
  pause?: RunPauseReceipt;
}

export type CancelDelivery = 'pending' | 'delivered' | 'failed';

export interface RunCancelReceipt {
  requestId: string;
  status: CancelDelivery;
  error?: string;
  attempts: number;
  updatedAt: string;
}

export interface RunPauseReceipt {
  requestId: string;
  pausedAt: string;
  notice: string;
}

export interface Stage {
  id?: string;
  name: string;
  gate?: string;
  state: RunState;
  tasks: Task[];
  startedAt: string;
  finishedAt?: string;
  resolution?: GateResolution;
}

export interface Task {
  id?: string;
  name: string;
  state: RunState;
  attempts: Attempt[];
  startedAt?: string;
  finishedAt?: string;
}

export interface Attempt {
  id?: string;
  requestId?: string;
  index: number;
  state: RunState;
  sessionRef: SessionRef;
  startedAt: string;
  finishedAt?: string;
  error?: string;
  receipt?: AttemptReceipt;
  sideEffectClass?: string;
}

export interface AttemptReceipt {
  requestId: string;
  outcome: string;
  evidence?: string;
  sideEffectClass?: string;
  confirmedAt: string;
}

export type ConclusionKind = 'fact' | 'finding' | 'decision' | 'outcome' | 'lesson';
export type ConclusionStatus = 'proposed' | 'confirmed' | 'superseded';

export interface Conclusion {
  id: string;
  kind: ConclusionKind;
  status: ConclusionStatus;
  title: string;
  summary: string;
  evidence?: SourceRef[];
  artifacts?: ArtifactRef[];
  nextSteps?: string[];
  supersedes?: string;
  generatedAt: string;
}

export interface ArtifactRef {
  id: string;
  name: string;
  type: string;
  status: 'available' | 'stale' | 'missing' | 'failed';
  path?: string;
  relativePath?: string;
  blobDigest?: string;
  sourceRunId?: string;
  lastVerifiedAt?: string;
  error?: string;
}

export interface SessionRef {
  sessionPath: string;
  branchId: string;
  modelRef: string;
  turnCount: number;
  preview: string;
  startedAt: string;
}

export interface SourceRef {
  kind: 'work' | 'session_turn' | 'block' | 'artifact' | 'file' | 'url';
  workId?: string;
  objectId?: string;
  path?: string;
  url?: string;
  digest?: string;
}

export type CornerstoneType =
  | 'instruction'
  | 'file_ref'
  | 'file_snapshot'
  | 'decision'
  | 'conclusion'
  | 'source'
  | 'policy'
  | 'parameter';
export type CornerstoneMode = 'live_ref' | 'snapshot';
export type CornerstoneStatus = 'active' | 'stale' | 'missing' | 'denied' | 'invalid';

export interface CornerstoneRef {
  kind: 'inline' | 'session_turn' | 'workspace_file' | 'artifact' | 'url';
  sessionId?: string;
  turn?: number;
  path?: string;
  artifactId?: string;
  url?: string;
  blobDigest?: string;
}

export interface Cornerstone {
  id: string;
  workId: string;
  type: CornerstoneType;
  title: string;
  content?: string;
  ref: CornerstoneRef;
  mode: CornerstoneMode;
  digest: string;
  required: boolean;
  status: CornerstoneStatus;
  tags?: string[];
  provenance: SourceRef;
  lastVerifiedAt?: string;
  pinnedAt: string;
  updatedAt: string;
  error?: string;
  resolveErrorKind?: 'missing' | 'denied' | 'invalid' | 'network';
  candidateDigest?: string;
  tombstone?: boolean;
}

export interface CornerstoneReq {
  type: CornerstoneType;
  required: boolean;
  label?: string;
}

export type BlockStatus = 'loading' | 'ready' | 'empty' | 'stale' | 'blocked' | 'failed';

export interface BlockSource {
  provider: string;
  ref?: string;
  mode: 'snapshot' | 'query' | 'stream';
  verified: boolean;
}

export interface BlockFreshness {
  checkedAt?: string;
  expiresAt?: string;
  retryAt?: string;
  staleReason?: string;
}

export interface BlockFallback {
  summary: string;
  data?: unknown;
}

export interface BlockPlacement {
  blockId: string;
  slot: 'primary' | 'secondary' | 'attention' | 'result';
  order: number;
  span?: number;
  collapsed?: boolean;
}

export interface BlockActionSpec {
  id: string;
  label: string;
  intent: string;
  payload?: unknown;
  risk: 'read' | 'write' | 'destructive' | 'external';
  confirmRequired: boolean;
}

export interface BlockInstance {
  id: string;
  kind: string;
  schemaVersion: number;
  revision: number;
  title?: string;
  status: BlockStatus;
  data: unknown;
  actions?: BlockActionSpec[];
  source: BlockSource;
  freshness?: BlockFreshness;
  fallback: BlockFallback;
  tombstone?: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface BlockSpec {
  id: string;
  kind: string;
  schemaVersion: number;
  label: string;
  description?: string;
  defaultData?: unknown;
  placement: BlockPlacement;
  editable: boolean;
}

export type WorkEventType =
  | 'work.created'
  | 'definition.frozen'
  | 'draft.updated'
  | 'run.started'
  | 'stage.changed'
  | 'task.changed'
  | 'attempt.changed'
  | 'block.upserted'
  | 'block.removed'
  | 'cornerstone.upserted'
  | 'cornerstone.removed'
  | 'conclusion.upserted'
  | 'artifact.linked'
  | 'work.archived'
  | 'work.deleted';

export interface WorkEvent {
  schemaVersion: number;
  id: string;
  requestId: string;
  workId: string;
  type: WorkEventType;
  revision: number;
  baseRevision: number;
  payload: unknown;
  contentDigest: string;
  writerId: string;
  createdAt: string;
}

export type ViewEventType = 'snapshot' | 'delta' | 'attention' | 'removed';
export type ObjectKind =
  | 'work'
  | 'block'
  | 'run'
  | 'stage'
  | 'task'
  | 'attempt'
  | 'cornerstone'
  | 'conclusion'
  | 'artifact'
  | 'definition'
  | 'artifact_slot'
  | 'input'
  | 'patch';

export interface ObjectContext {
  kind: ObjectKind;
  id: string;
  parentID?: string;
  // V2 typed context fields
  workID?: string;
  runID?: string;
  taskID?: string;
  blockID?: string;
  inputID?: string;
  specID?: string;
  definitionID?: string;
  artifactSlotID?: string;
  patchID?: string;
  expectedRevision?: number | null;
  definitionRevision?: number | null;
}

export interface ViewResync {
  reason: 'overflow' | 'retry' | 'hydrate';
  authoritative: true;
  generation: number;
}

export interface ViewRecoveryIntent {
  reason: 'retry' | 'hydrate';
  generation: number;
}

export interface WorkViewEvent {
  schemaVersion: number;
  type: ViewEventType;
  workID: string;
  eventID: string;
  revision: number;
  baseRevision: number;
  requestID: string;
  object: ObjectContext;
  resync?: ViewResync;
  payload: unknown;
  createdAt: string;
}

export interface CreateWorkInput {
  blueprintRef: BlueprintRef;
  name?: string;
  prompt?: string;
  inputs?: Record<string, unknown>;
  requestId: string;
}

export interface CopyWorkInput {
  sourceWorkId: string;
  name?: string;
  requestId: string;
}

export interface UpdateDraftInput {
  workId: string;
  name?: string;
  prompt?: string;
  inputs?: Record<string, unknown>;
  expectedRevision: number;
  requestId: string;
}

export interface RetryTaskInput {
  workId: string;
  runId: string;
  stageId: string;
  taskId: string;
  requestId: string;
}

export interface ResumeRunInput {
  workId: string;
  runId: string;
  requestId: string;
  gateResolutions?: Record<string, GateResolution>;
}

export interface GateResolution {
  stageId: string;
  outcome: 'approved' | 'input_provided';
  input?: Record<string, unknown>;
  note?: string;
}

export interface BlockActionRequest {
  workId: string;
  /** Explicit workflow owner; interactive renderers never infer it from the active tab. */
  runId?: string;
  /** Explicit task owner; interactive renderers never infer it from the active tab. */
  taskId?: string;
  blockId: string;
  actionId: string;
  input?: Record<string, unknown>;
  requestId: string;
  expectedRevision: number;
}

export interface BlockUpdateRequest {
  workId: string;
  blockId: string;
  data: Record<string, unknown>;
  requestId: string;
  expectedRevision: number;
}

export interface BlockUpsertInput {
  workId: string;
  blockId: string;
  kind: string;
  schemaVersion: number;
  revision: number;
  title?: string;
  status: BlockStatus;
  data: unknown;
  actions?: BlockActionSpec[];
  source: BlockSource;
  freshness?: BlockFreshness;
  fallback: BlockFallback;
  expectedRevision: number;
  requestId: string;
}

export interface WorkFilter {
  state?: WorkState;
  archiveState?: WorkArchiveState;
  blueprint?: string;
  search?: string;
  cursor?: string;
  limit: number;
}

export interface WorkSummary {
  id: string;
  name: string;
  state: WorkState;
  archiveState: WorkArchiveState;
  blueprintRef: BlueprintRef;
  createdAt: string;
  updatedAt: string;
}

export interface WorkPage {
  items: WorkSummary[];
  nextCursor?: string;
  total: number;
}

export interface WorkView {
  schemaVersion: number;
  work: Work;
  revision: number;
  assessment: CornerstoneAssessment;
  runBlock?: RunBlockReason;
  definition?: import('./types_v2.js').WorkDefinitionRevision;
  artifactSlots?: import('./types_v2.js').ArtifactSlot[];
  tasks?: import('./types_v2.js').TaskV2View[];
  inputs?: import('./types_v2.js').WorkInput[];
  patchPreviews?: import('./types_v2.js').WorkPatchPreview[];
}

export type CornerstoneUseState = 'ready' | 'degraded' | 'blocked';

export interface CornerstoneAssessment {
  state: CornerstoneUseState;
  blocking: boolean;
  degraded: boolean;
  issues?: CornerstoneIssue[];
}

export type RunBlockCode =
  | 'blob_missing'
  | 'budget_exhausted'
  | 'resolver_unavailable'
  | 'cornerstone_stale'
  | 'cornerstone_missing'
  | 'cornerstone_denied'
  | 'cornerstone_invalid'
  | 'waiting_user'
  | 'failed'
  | 'archived';

export interface RunBlockItem {
  code: RunBlockCode;
  cornerstoneId?: string;
  status?: CornerstoneStatus;
  detail?: string;
}

export interface RunBlockReason {
  blocked: boolean;
  items?: RunBlockItem[];
}

export interface CornerstoneInput {
  type: CornerstoneType;
  title: string;
  content: string;
  ref: CornerstoneRef;
  mode: CornerstoneMode;
  required: boolean;
  tags?: string[];
  requestId: string;
}

export type RerunMode = 'original_definition' | 'latest_definition';
export interface PrepareRerunInput {
  recordId: string;
  mode: RerunMode;
}

export interface ChangeSummary {
  field: string;
  previous?: string;
  current?: string;
  breaking: boolean;
}

export interface PermissionIssue {
  tool: string;
  description: string;
  blocking: boolean;
}

export interface CornerstoneIssue {
  cornerstoneId: string;
  title: string;
  problem: string;
  blocking: boolean;
}

export interface BlockCompatIssue {
  blockId: string;
  kind: string;
  problem: string;
  blocking: boolean;
}

export interface RerunPlan {
  planToken: string;
  sourceDefinition: BlueprintRef;
  targetDefinition: BlueprintRef;
  definitionDiff?: ChangeSummary[];
  missingTools?: ToolContractRef[];
  missingFiles?: SourceRef[];
  missingSecrets?: string[];
  permissionIssues?: PermissionIssue[];
  cornerstoneIssues?: CornerstoneIssue[];
  blockIssues?: BlockCompatIssue[];
  blocking: boolean;
  warnings?: string[];
  expiresAt: string;
}

export type ActionReceiptStatus =
  | 'pending'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'rejected'
  | 'unknown';

export interface ActionReceipt {
  workId: string;
  blockId: string;
  blockKind?: string;
  actionId: string;
  handlerIdentityVersion?: number;
  handlerId?: string;
  handlerVersion?: string;
  status: ActionReceiptStatus;
  message?: string;
  requestId: string;
  inputDigest?: string;
  fingerprint?: string;
  intent?: string;
  summary?: string;
  risk?: BlockActionSpec['risk'];
  confirmRequired?: boolean;
  result?: unknown;
  retryable: boolean;
  outcomeKnown: boolean;
  revision?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface TaskExecuteInput {
  workId: string;
  runId: string;
  stageId: string;
  taskId: string;
  attemptIndex: number;
  requestId: string;
  definitionDigest: string;
  prompt: string;
}

// ── Run progress & selection ──────────────────────────────────────────────

export interface RunSelection {
  runId: string;
  stageId?: string;
  taskId?: string;
  attemptId?: string;
  attemptIndex?: number;
}

export interface RetryIntent extends RetryTaskInput {
  /** The failed attempt that initiated this retry; the backend retries the owning Task. */
  attemptId?: string;
  attemptIndex: number;
}

export type RetryHandler = (intent: RetryIntent) => void | Promise<void>;

export interface RetryStatus {
  intent: RetryIntent;
  state: 'pending' | 'failed';
  error?: string;
}

export interface SessionSurfaceContext {
  workId: string;
  runId: string;
  stageId: string;
  taskId: string;
  attemptId?: string;
  attemptIndex: number;
  sessionRef: SessionRef;
  readonly?: boolean;
  archived?: boolean;
}

// ── Deep link extensions ──────────────────────────────────────────────────

export interface DeepLinkTarget {
  runId?: string;
  stageId?: string;
  taskId?: string;
  attemptId?: string;
  attemptIndex?: number;
  /** Legacy flat targetID; resolved to a structured target when possible. */
  targetID?: string;
}

export interface ToolCapability {
  available: boolean;
  compatible: boolean;
  reason?: string;
}

export interface PermissionRequest {
  workId: string;
  requestId: string;
  object: ObjectContext;
  toolName: string;
  risk: string;
  input?: Record<string, unknown>;
}

export interface PermissionDecision {
  allowed: boolean;
  approvalRequired: boolean;
  reason?: string;
}

// CornerstoneDrawer keeps drafts and in-flight state outside the Work
// projection. Controller results still re-enter through WorkView snapshots or
// events; components never patch Cornerstones directly.

export type CornerstoneUIAction =
  | 'pin'
  | 'refresh'
  | 'validate'
  | 'freeze'
  | 'accept'
  | 'repair'
  | 'remove'
  | 'undo';

export interface CornerstoneRetry {
  action: CornerstoneUIAction;
  requestId: string;
  expectedRevision: number;
}

export interface CornerstoneItemUI {
  draftTitle: string | null;
  draftContent: string | null;
  pendingAction: CornerstoneUIAction | null;
  pendingRequestId: string | null;
  error: string | null;
  conflictSnapshot: Cornerstone | null;
  retry: CornerstoneRetry | null;
}

export interface CornerstoneDrawerUI {
  byId: Record<string, CornerstoneItemUI>;
  open: boolean;
  filterType: CornerstoneType | 'all';
  filterRequired: boolean | null;
}

export interface CornerstoneUIState {
  byWork: Record<string, CornerstoneDrawerUI>;
}

export interface CornerstoneMutationContext {
  workId: string;
  requestId: string;
  expectedRevision: number;
}

export interface PinCornerstoneInput extends CornerstoneMutationContext {
  type: CornerstoneType;
  title: string;
  content: string;
  ref: CornerstoneRef;
  mode: CornerstoneMode;
  required: boolean;
  tags: string[];
}

export interface RefreshCornerstoneInput extends CornerstoneMutationContext {
  cornerstoneId: string;
}

export interface ValidateCornerstoneInput extends RefreshCornerstoneInput {}

export interface FreezeCornerstoneInput extends CornerstoneMutationContext {
  cornerstoneId: string;
  useLastKnown?: boolean;
}

export interface AcceptCornerstoneInput extends CornerstoneMutationContext {
  cornerstoneId: string;
}

export interface RepairCornerstoneInput extends CornerstoneMutationContext {
  cornerstoneId: string;
  ref?: CornerstoneRef;
  content?: string;
}

export interface RemoveCornerstoneInput extends CornerstoneMutationContext {
  cornerstoneId: string;
}

export interface UndoCornerstoneInput extends CornerstoneMutationContext {
  cornerstoneId: string;
}

// Compatibility names for callers created against the first Drawer draft.
export type AcceptCornerstoneVersionInput = AcceptCornerstoneInput;
export type RepairCornerstoneRefInput = RepairCornerstoneInput;
export type UndoRemoveCornerstoneInput = UndoCornerstoneInput;

export interface CornerstoneAttention {
  workId: string;
  items: CornerstoneAttentionItem[];
}

export interface CornerstoneAttentionItem {
  cornerstoneId: string;
  title: string;
  status: CornerstoneStatus;
  reason: string;
}

export interface RevisionConflict {
  kind: 'revision_conflict';
  workId: string;
  cornerstoneId: string;
  expectedRevision: number;
  actualRevision: number;
  latestSnapshot?: Cornerstone;
  latestView?: WorkView;
  message: string;
}

export interface NetworkError {
  kind: 'network_error';
  requestId: string;
  message: string;
  retryable: boolean;
}

export type CornerstoneMutationError = RevisionConflict | NetworkError;

export type CornerstoneMutationResult =
  | {
      ok: true;
      cornerstone: Cornerstone;
      workView?: WorkView;
      revision?: number;
      duplicate?: boolean;
    }
  | { ok: false; error: CornerstoneMutationError };
