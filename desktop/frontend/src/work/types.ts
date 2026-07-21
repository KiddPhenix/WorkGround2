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
export type RunState = 'running' | 'completed' | 'failed' | 'cancelled';
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
  definitionDigest: string;
  state: RunState;
  stages: Stage[];
  startedAt: string;
  finishedAt?: string;
  conclusion?: Conclusion;
}

export interface Stage {
  name: string;
  state: RunState;
  tasks: Task[];
  startedAt: string;
  finishedAt?: string;
}

export interface Task {
  name: string;
  state: RunState;
  attempts: Attempt[];
}

export interface Attempt {
  index: number;
  state: RunState;
  sessionRef: SessionRef;
  startedAt: string;
  finishedAt?: string;
  error?: string;
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
  | 'artifact';

export interface ObjectContext {
  kind: ObjectKind;
  id: string;
  parentID?: string;
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
  payload: unknown;
  createdAt: string;
}

export interface CreateWorkInput {
  blueprintRef: BlueprintRef;
  name: string;
  inputs?: Record<string, unknown>;
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

export interface BlockActionRequest {
  workId: string;
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

export interface ActionReceipt {
  actionId: string;
  status: string;
  message?: string;
  requestId: string;
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
