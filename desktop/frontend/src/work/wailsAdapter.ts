/**
 * WailsCornerstoneAdapter — real Wails bridge for CornerstoneControllerPort.
 *
 * Translates between the frontend CornerstoneControllerPort (rich TS DTOs with
 * workId embedded) and the Go Wails bindings (workId passed separately, Go DTOs
 * use lowerCamelCase JSON tags matching Go json struct tags). Handles revision conflicts by preserving draft
 * state and surfacing the latest snapshot. Same business retry reuses requestID;
 * new intent generates a fresh requestID.
 */

import type { CornerstoneControllerPort, WorkControllerPort } from './controller';
import type { WorkUIPreference } from './store';
import type {
  AcceptCornerstoneInput,
  Attempt,
  BlockUpsertInput,
  Cornerstone,
  CornerstoneMutationResult,
  CopyWorkInput,
  CreateWorkInput,
  FreezeCornerstoneInput,
  PinCornerstoneInput,
  RefreshCornerstoneInput,
  RemoveCornerstoneInput,
  RepairCornerstoneInput,
  ResumeRunInput,
  RetryTaskInput,
  RevisionConflict,
  UndoCornerstoneInput,
  UpdateDraftInput,
  ValidateCornerstoneInput,
  ViewRecoveryIntent,
  Work,
  WorkFilter,
  WorkBlueprint,
  WorkPage,
  WorkRecord,
  WorkView,
  WorkViewEvent,
  WorkflowRun,
} from './types';

// Single local contract for every Work-owned method exposed by Wails. The
// application bridge extends this interface while the production Work adapter
// consumes it directly, so the protocol cannot drift into two hand-written
// copies. Generated model classes are deliberately kept outside application
// code; these DTOs mirror their JSON shapes.
export interface WailsWorkBindings {
  WorkEnabled(tabID: string): Promise<boolean>;
  WorkCapable(tabID: string): Promise<boolean>;
  WorkCollaborationV2Enabled(tabID: string): Promise<boolean>;
  CreateWork(tabID: string, input: CreateWorkInput): Promise<Work>;
  GetWork(tabID: string, workID: string): Promise<WorkView>;
  ListWorks(tabID: string, filter: WorkFilter): Promise<WorkPage>;
  ListWorkBlueprints(tabID: string): Promise<WorkBlueprint[]>;
  CopyWork(tabID: string, input: CopyWorkInput): Promise<Work>;
  UpdateDraft(tabID: string, input: UpdateDraftInput): Promise<WorkView>;
  UpsertWorkBlock(tabID: string, input: BlockUpsertInput): Promise<WorkView>;
  RecoverWorkView(tabID: string, workID: string, input: ViewRecoveryIntent): Promise<WorkViewEvent>;
  RunWork(tabID: string, workID: string, requestID: string): Promise<WorkflowRun>;
  ResumeRun(tabID: string, input: ResumeRunInput): Promise<WorkflowRun>;
  RetryWorkTask(tabID: string, input: RetryTaskInput): Promise<Attempt>;
  ArchiveWork(tabID: string, workID: string, requestID: string): Promise<WorkRecord>;
  RestoreWork(tabID: string, workID: string, requestID: string): Promise<WorkView>;
  DeleteWork(tabID: string, workID: string, requestID: string): Promise<void>;
  PrepareWorkRerun(tabID: string, input: import('./types').PrepareRerunInput): Promise<import('./types').RerunPlan>;
  ExecuteWorkRerun(tabID: string, planToken: string, requestID: string): Promise<Work>;
  WatchWork(tabID: string, workID: string, subscriptionID: string): Promise<void>;
  UnwatchWork(subscriptionID: string): Promise<void>;
  PinCornerstone(tabID: string, workID: string, input: GoCornerstoneInput): Promise<GoCornerstoneResult>;
  RefreshCornerstone(tabID: string, workID: string, input: GoRefreshInput): Promise<GoCornerstoneResult>;
  RemoveCornerstone(tabID: string, workID: string, input: GoRefreshInput): Promise<GoCornerstoneResult>;
  UndoCornerstone(tabID: string, workID: string, input: GoRefreshInput): Promise<GoCornerstoneResult>;
  AcceptCornerstone(tabID: string, workID: string, input: GoRefreshInput): Promise<GoCornerstoneResult>;
  FreezeCornerstone(tabID: string, workID: string, input: GoFreezeInput): Promise<GoCornerstoneResult>;
  RepairCornerstone(tabID: string, workID: string, input: GoRepairInput): Promise<GoRepairResult>;
  SessionPurgeImpact(path: string): Promise<GoForcePurgeImpact>;
  ForcePurgeTrashedSession(path: string): Promise<GoForcePurgeImpact>;
  RetryCleanupPending(path: string, requestID: string): Promise<void>;
  ListSessionCleanupPending(): Promise<GoCleanupPendingRecord[]>;
  BeginWorkPlanning(
    tabID: string,
    input: import('./types_v2').BeginWorkPlanningInput,
  ): Promise<GoBeginWorkPlanningResult>;
  ApplyDefinition(
    tabID: string,
    input: import('./types_v2').ApplyDefinitionInput,
  ): Promise<GoApplyDefinitionResult>;
  CreateCandidateRevision(
    tabID: string,
    input: import('./types_v2').CreateCandidateRevisionInput,
  ): Promise<GoCreateCandidateRevisionResult>;
  RetryWorkNode(
    tabID: string,
    input: import('./types_v2').RetryWorkNodeRequest,
  ): Promise<GoRetryWorkNodeResult>;
  RetryArtifactSlot(
    tabID: string,
    input: import('./types_v2').RetryArtifactSlotRequest,
  ): Promise<GoRetryArtifactSlotResult>;
  PreviewArtifact(
    tabID: string,
    input: import('./types_v2').PreviewArtifactRequest,
  ): Promise<GoPreviewArtifactResult>;
  RequestArtifactConversion(
    tabID: string,
    input: import('./types_v2').RequestArtifactConversionInput,
  ): Promise<GoRequestArtifactConversionResult>;
  SelectWorkInputFile(
    tabID: string,
    input: import('./types_v2').SelectWorkInputFileRequest,
  ): Promise<GoSelectWorkInputFileResult>;
  SubmitWorkInput(
    tabID: string,
    input: import('./types_v2').SubmitWorkInputRequest,
  ): Promise<GoSubmitInputResult>;
  SetInputCornerstone(
    tabID: string,
    input: import('./types_v2').SetInputCornerstoneRequest,
  ): Promise<GoCornerstonePinResult>;
  PreviewWorkPatch(
    tabID: string,
    input: import('./types_v2').PreviewWorkPatchRequest,
  ): Promise<GoPreviewWorkPatchResult>;
  ApplyWorkPatch(
    tabID: string,
    input: import('./types_v2').ApplyWorkPatchRequest,
  ): Promise<GoApplyWorkPatchResult>;
}

// --- Go DTO shapes (lowerCamelCase JSON tags matching Go json struct tags) ---
interface GoCornerstoneInput {
  type: string;
  title: string;
  content: string;
  ref: { kind: string; sessionId?: string; turn?: number; path?: string; artifactId?: string; url?: string; blobDigest?: string };
  mode: string;
  required: boolean;
  tags?: string[];
  expectedRevision: number;
  requestId: string;
}

interface GoRefreshInput {
  cornerstoneId: string;
  expectedRevision: number;
  requestId: string;
}

interface GoFreezeInput {
  cornerstoneId: string;
  useLastKnown?: boolean;
  expectedRevision: number;
  requestId: string;
}

interface GoRepairInput {
  cornerstoneId: string;
  ref?: { kind: string; sessionId?: string; turn?: number; path?: string; artifactId?: string; url?: string; blobDigest?: string };
  content?: string;
  expectedRevision: number;
  requestId: string;
}

interface GoCornerstoneResult {
  cornerstone: Cornerstone | null;
  workView: WorkView | null;
  duplicate: boolean;
  revision: number;
  resolution?: {
    candidateContent?: string;
    candidateDigest?: string;
    diff?: string;
    errorKind?: string;
    retryable: boolean;
  } | null;
  assessment: {
    state: string;
    blocking: boolean;
    degraded: boolean;
    issues?: { cornerstoneId: string; title: string; problem: string; blocking: boolean }[];
  };
}

interface GoRepairResult {
  cornerstone: Cornerstone | null;
  workView: WorkView | null;
  repaired: boolean;
  duplicate: boolean;
  revision: number;
  failedRefs?: string[];
  resolution?: {
    candidateContent?: string;
    candidateDigest?: string;
    diff?: string;
    errorKind?: string;
    retryable: boolean;
  } | null;
  assessment: {
    state: string;
    blocking: boolean;
    degraded: boolean;
    issues?: { cornerstoneId: string; title: string; problem: string; blocking: boolean }[];
  };
}

interface GoSessionOwner {
  ownerType: 'work' | 'branch';
  ownerId: string;
  scopeId?: string;
  workId?: string;
  state: 'active' | 'trashed';
  trashedAt?: number;
  restoredAt?: number;
}

interface GoForcePurgeImpact {
  sessionPath: string;
  affectedOwners: GoSessionOwner[];
  affectedWorkIDs: string[];
  affectedBranchIDs?: string[];
}

interface GoCleanupPendingRecord {
  sessionPath: string;
  reason: string;
  requestId: string;
  stage?: string;
  error?: string;
  attempts?: number;
  impact?: GoForcePurgeImpact;
  createdAt: number;
  updatedAt?: number;
}

// Go returns BeginWorkPlanningResult with lowerCamelCase JSON tags matching Go json struct tags.
interface GoBeginWorkPlanningResult {
  result?: import('./types').WorkView;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  transportError?: import('./types_v2').WorkTransportError;
}

// Go returns ApplyDefinitionResult with lowerCamelCase JSON tags matching Go json struct tags.
interface GoApplyDefinitionResult {
  view?: import('./types').WorkView;
  intent?: import('./types_v2').AutoSwitchFaceIntent;
  impact?: import('./types_v2').RunImpact;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  transportError?: import('./types_v2').WorkTransportError;
}

interface GoCreateCandidateRevisionResult {
  candidate?: import('./types_v2').WorkDefinitionRevision;
  clarification?: import('./types_v2').DefinitionStructuralClarification;
  impact?: import('./types_v2').RunImpact;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  transportError?: import('./types_v2').WorkTransportError;
}

interface GoRetryWorkNodeResult {
  result?: import('./types').Task;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  error?: import('./types_v2').WorkTransportError;
}

interface GoRetryArtifactSlotResult {
  slot?: import('./types_v2').ArtifactSlot;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  transportError?: import('./types_v2').WorkTransportError;
}

interface GoPreviewArtifactResult {
  preview?: import('./types_v2').ArtifactPreview;
  committed: boolean;
  recoverable: boolean;
  transportError?: import('./types_v2').WorkTransportError;
}

interface GoRequestArtifactConversionResult {
  preview?: import('./types_v2').ArtifactPreview;
  committed: boolean;
  recoverable: boolean;
  duplicate: boolean;
  transportError?: import('./types_v2').WorkTransportError;
}

interface GoSelectWorkInputFileResult {
  artifactRef?: import('./types').ArtifactRef;
  canceled: boolean;
  error?: import('./types_v2').WorkTransportError;
}

interface GoSubmitInputResult {
  input?: import('./types_v2').WorkInput;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  error?: string;
  transportError?: import('./types_v2').WorkTransportError;
  receipt?: import('./types_v2').InputIntentReceipt;
}

interface GoCornerstonePinResult {
  cornerstoneId?: string;
  pinned: boolean;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  error?: string;
  transportError?: import('./types_v2').WorkTransportError;
  receipt?: import('./types_v2').InputIntentReceipt;
}

interface GoPreviewWorkPatchResult {
  preview?: import('./types_v2').WorkPatchPreview;
  revision: number;
  duplicate: boolean;
  committed: boolean;
  recoverable: boolean;
  error?: string;
  transportError?: import('./types_v2').WorkTransportError;
  receipt?: import('./types_v2').PatchIntentReceipt;
}

interface GoApplyWorkPatchResult {
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
  transportError?: import('./types_v2').WorkTransportError;
  receipt?: import('./types_v2').PatchIntentReceipt;
}

// --- Helpers ---

function normalizeWorkPatchPreview(
  preview: import('./types_v2').WorkPatchPreview | undefined,
): import('./types_v2').WorkPatchPreview | undefined {
  if (!preview) return undefined;
  return {
    ...preview,
    operations: Array.isArray(preview.operations) ? preview.operations : [],
    affectedNodeIds: Array.isArray(preview.affectedNodeIds) ? preview.affectedNodeIds : [],
    affectedBlockIds: Array.isArray(preview.affectedBlockIds) ? preview.affectedBlockIds : [],
    affectedArtifactSlotIds: Array.isArray(preview.affectedArtifactSlotIds) ? preview.affectedArtifactSlotIds : [],
    staleArtifactSlotIds: Array.isArray(preview.staleArtifactSlotIds) ? preview.staleArtifactSlotIds : [],
    invalidatedTaskIds: Array.isArray(preview.invalidatedTaskIds) ? preview.invalidatedTaskIds : [],
  };
}

function getWailsApp(): WailsWorkBindings | undefined {
  if (typeof window === 'undefined') return undefined;
  const go = (window as unknown as Record<string, unknown>).go as Record<string, unknown> | undefined;
  const main = go?.main as Record<string, unknown> | undefined;
  return main?.App as WailsWorkBindings | undefined;
}

function subscriptionID(): string {
  const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `work-view-${suffix}`;
}

function preferenceKey(tabID: string, workID: string): string {
  return `work-card-ui:${tabID}:${workID}`;
}

function decodeWailsRawMessage(payload: unknown): unknown {
  if (!Array.isArray(payload) || !payload.every((value) => Number.isInteger(value) && value >= 0 && value <= 255)) {
    return payload;
  }
  try {
    const json = new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(payload));
    return JSON.parse(json) as unknown;
  } catch {
    // Preserve malformed bytes so the shared Work reducer rejects the event
    // with its normal observable conflict instead of accepting partial data.
    return payload;
  }
}

function decodeWailsWorkViewEvent(event: WorkViewEvent): WorkViewEvent {
  return { ...event, payload: decodeWailsRawMessage(event.payload) };
}

function okResult(go: GoCornerstoneResult): CornerstoneMutationResult {
  return {
    ok: true,
    cornerstone: go.cornerstone!,
    workView: go.workView ?? undefined,
    revision: go.revision,
    duplicate: go.duplicate,
  };
}

function okRepairResult(go: GoRepairResult): CornerstoneMutationResult {
  return {
    ok: true,
    cornerstone: go.cornerstone!,
    workView: go.workView ?? undefined,
    revision: go.revision,
    duplicate: go.duplicate,
  };
}

// Go error format: "work event conflict: expected revision 3, current revision 5"
const revisionConflictPattern = /work event conflict:.*expected revision (\d+).*current revision (\d+)/;

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function parseRevisionConflict(message: string): { expected: number; actual: number } | null {
  const match = message.match(revisionConflictPattern);
  if (!match) return null;
  return { expected: Number(match[1]), actual: Number(match[2]) };
}

function isCornerstoneDisabled(err: unknown): boolean {
  return errorMessage(err).includes('Cornerstone feature is disabled');
}

function networkError(requestId: string, retryable = true): CornerstoneMutationResult {
  return {
    ok: false,
    error: {
      kind: 'network_error',
      requestId,
      message: retryable ? 'Cornerstone request failed; retry is safe' : 'Cornerstone operation is unavailable',
      retryable,
    },
  };
}

function revisionConflictError(
  workId: string,
  cornerstoneId: string,
  expectedRevision: number,
  actualRevision: number,
  message: string,
): { ok: false; error: RevisionConflict } {
  const conflict: RevisionConflict = {
    kind: 'revision_conflict',
    workId,
    cornerstoneId,
    expectedRevision,
    actualRevision,
    message,
  };
  return { ok: false, error: conflict };
}

async function errorResult(
  app: WailsWorkBindings,
  tabID: string,
  err: unknown,
  workId: string,
  input: { requestId: string; expectedRevision: number },
  cornerstoneId: string,
): Promise<CornerstoneMutationResult> {
  const message = errorMessage(err);
  if (isCornerstoneDisabled(err)) {
    return networkError(input.requestId, false);
  }
  const parsed = parseRevisionConflict(message);
  if (parsed) {
    try {
      const latestView = await app.GetWork(tabID, workId);
      const latestSnapshot = latestView.work.cornerstones.find((item) => item.id === cornerstoneId);
      const conflict = revisionConflictError(
        workId,
        cornerstoneId,
        input.expectedRevision,
        latestView.revision,
        'Cornerstone revision conflict',
      );
      conflict.error.latestView = latestView;
      conflict.error.latestSnapshot = latestSnapshot;
      return conflict;
    } catch {
      return revisionConflictError(
        workId,
        cornerstoneId,
        input.expectedRevision,
        parsed.actual,
        'Cornerstone revision conflict; latest projection could not be loaded',
      );
    }
  }
  return networkError(input.requestId);
}

// --- Adapter factory ---

/**
 * Create the complete Wails-backed WorkControllerPort for a specific tab.
 * Returns undefined when Wails is unavailable (browser dev mode).
 */
export function createWailsWorkControllerPort(tabID: string): WorkControllerPort | undefined {
  const app = getWailsApp();
  if (!app) return undefined;

  return {
    subscribe: (workID, onEvent) => {
      if (!window.runtime?.EventsOn) throw new Error('Wails Work event runtime is unavailable');
      const id = subscriptionID();
      const eventName = `work:view:${id}`;
      const off = window.runtime.EventsOn(eventName, (payload) => onEvent(decodeWailsWorkViewEvent(payload as WorkViewEvent)));
      let active = true;
      let offCalled = false;
      const removeListener = (): void => {
        if (offCalled) return;
        offCalled = true;
        off();
      };
      const ready = app.WatchWork(tabID, workID, id)
        .then(async () => {
          // Unmount/work switching may win the race with the Wails promise.
          // Unwatch again after a late resolve because the earlier call may
          // have reached Go before WatchWork registered the server watch.
          if (!active) await app.UnwatchWork(id);
        })
        .catch((error: unknown) => {
          removeListener();
          throw error;
        });
      return {
        ready,
        unsubscribe: () => {
          if (!active) return;
          active = false;
          removeListener();
          void app.UnwatchWork(id).catch(() => undefined);
        },
      };
    },

    fetchSnapshot: (workID) => app.GetWork(tabID, workID),
    fetchRecoverySnapshot: async (workID, intent) => decodeWailsWorkViewEvent(await app.RecoverWorkView(tabID, workID, intent)),

    readUIPreference: async (workID) => {
      const raw = window.localStorage.getItem(preferenceKey(tabID, workID));
      if (!raw) return null;
      const parsed = JSON.parse(raw) as Partial<WorkUIPreference>;
      if (parsed.activeFace !== 'front' && parsed.activeFace !== 'back') return null;
      return {
        activeFace: parsed.activeFace,
        discussionDrafts: parsed.discussionDrafts,
        inputDrafts: parsed.inputDrafts,
      };
    },

    writeUIPreference: async (workID, preference) => {
      window.localStorage.setItem(preferenceKey(tabID, workID), JSON.stringify(preference));
    },

    retryTask: (input) => app.RetryWorkTask(tabID, input),
    runWork: (input) => app.RunWork(tabID, input.workId, input.requestId),

    resumeRun: (input) => app.ResumeRun(tabID, input),
    updateDraft: (input) => app.UpdateDraft(tabID, input),
    upsertBlock: (input) => app.UpsertWorkBlock(tabID, input),

    pinCornerstone: async (input: PinCornerstoneInput) => {
      const goInput: GoCornerstoneInput = {
        type: input.type,
        title: input.title,
        content: input.content,
        ref: input.ref,
        mode: input.mode,
        required: input.required,
        tags: input.tags,
        expectedRevision: input.expectedRevision,
        requestId: input.requestId,
      };
      try {
        const result = await app.PinCornerstone(tabID, input.workId, goInput);
        return okResult(result);
      } catch (err) {
        return errorResult(app, tabID, err, input.workId, input, '__new__');
      }
    },

    refreshCornerstone: async (input: RefreshCornerstoneInput) => {
      const goInput: GoRefreshInput = {
        cornerstoneId: input.cornerstoneId,
        expectedRevision: input.expectedRevision,
        requestId: input.requestId,
      };
      try {
        const result = await app.RefreshCornerstone(tabID, input.workId, goInput);
        return okResult(result);
      } catch (err) {
        return errorResult(app, tabID, err, input.workId, input, input.cornerstoneId);
      }
    },

    validateCornerstone: async (input: ValidateCornerstoneInput) => {
      const goInput: GoRefreshInput = {
        cornerstoneId: input.cornerstoneId,
        expectedRevision: input.expectedRevision,
        requestId: input.requestId,
      };
      try {
        const result = await app.RefreshCornerstone(tabID, input.workId, goInput);
        return okResult(result);
      } catch (err) {
        return errorResult(app, tabID, err, input.workId, input, input.cornerstoneId);
      }
    },

    freezeCornerstone: async (input: FreezeCornerstoneInput) => {
      const goInput: GoFreezeInput = {
        cornerstoneId: input.cornerstoneId,
        useLastKnown: input.useLastKnown,
        expectedRevision: input.expectedRevision,
        requestId: input.requestId,
      };
      try {
        const result = await app.FreezeCornerstone(tabID, input.workId, goInput);
        return okResult(result);
      } catch (err) {
        return errorResult(app, tabID, err, input.workId, input, input.cornerstoneId);
      }
    },

    acceptCornerstone: async (input: AcceptCornerstoneInput) => {
      const goInput: GoRefreshInput = {
        cornerstoneId: input.cornerstoneId,
        expectedRevision: input.expectedRevision,
        requestId: input.requestId,
      };
      try {
        const result = await app.AcceptCornerstone(tabID, input.workId, goInput);
        return okResult(result);
      } catch (err) {
        return errorResult(app, tabID, err, input.workId, input, input.cornerstoneId);
      }
    },

    repairCornerstone: async (input: RepairCornerstoneInput) => {
      const goInput: GoRepairInput = {
        cornerstoneId: input.cornerstoneId,
        ref: input.ref,
        content: input.content,
        expectedRevision: input.expectedRevision,
        requestId: input.requestId,
      };
      try {
        const result = await app.RepairCornerstone(tabID, input.workId, goInput);
        return okRepairResult(result);
      } catch (err) {
        return errorResult(app, tabID, err, input.workId, input, input.cornerstoneId);
      }
    },

    removeCornerstone: async (input: RemoveCornerstoneInput) => {
      const goInput: GoRefreshInput = {
        cornerstoneId: input.cornerstoneId,
        expectedRevision: input.expectedRevision,
        requestId: input.requestId,
      };
      try {
        const result = await app.RemoveCornerstone(tabID, input.workId, goInput);
        return okResult(result);
      } catch (err) {
        return errorResult(app, tabID, err, input.workId, input, input.cornerstoneId);
      }
    },

    undoCornerstone: async (input: UndoCornerstoneInput) => {
      const goInput: GoRefreshInput = {
        cornerstoneId: input.cornerstoneId,
        expectedRevision: input.expectedRevision,
        requestId: input.requestId,
      };
      try {
        const result = await app.UndoCornerstone(tabID, input.workId, goInput);
        return okResult(result);
      } catch (err) {
        return errorResult(app, tabID, err, input.workId, input, input.cornerstoneId);
      }
    },

    beginWorkPlanning: async (input) => {
      try {
        const go = await app.BeginWorkPlanning(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.revision !== 'number' || !Number.isFinite(go.revision)) {
          return {
            revision: 0,
            duplicate: false,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'BeginWorkPlanning: required scalar missing/wrong-type (committed/recoverable/duplicate=boolean, revision=number)',
              operation: 'BeginWorkPlanning',
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.result?.work?.id && !(go.transportError?.code === 'committed_recovery')) {
          return {
            revision: go.revision,
            duplicate: go.duplicate,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'BeginWorkPlanning committed but result.work.id missing',
              operation: 'BeginWorkPlanning',
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          result: go.result,
          revision: go.revision,
          duplicate: go.duplicate,
          committed: go.committed,
          recoverable: go.recoverable,
          transportError: go.transportError,
        };
      } catch (error) {
        return {
          revision: 0,
          duplicate: false,
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'BeginWorkPlanning',
            requestId: input.requestId,
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    applyDefinition: async (input) => {
      try {
        const go = await app.ApplyDefinition(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.revision !== 'number' || !Number.isFinite(go.revision)) {
          return {
            revision: 0,
            duplicate: false,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'ApplyDefinition: required scalar missing/wrong-type (committed/recoverable/duplicate=boolean, revision=number)',
              operation: 'ApplyDefinition',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.view?.work?.id && !(go.transportError?.code === 'committed_recovery')) {
          return {
            revision: go.revision,
            duplicate: go.duplicate,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'ApplyDefinition committed but view.work.id missing',
              operation: 'ApplyDefinition',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          view: go.view,
          intent: go.intent,
          impact: go.impact,
          revision: go.revision,
          duplicate: go.duplicate,
          committed: go.committed,
          recoverable: go.recoverable,
          transportError: go.transportError,
        };
      } catch (error) {
        return {
          revision: 0,
          duplicate: false,
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'ApplyDefinition',
            workId: input.workId,
            requestId: input.requestId,
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    createCandidateRevision: async (input) => {
      try {
        const go = await app.CreateCandidateRevision(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.revision !== 'number' || !Number.isFinite(go.revision)) {
          return {
            revision: 0,
            duplicate: false,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'CreateCandidateRevision: required scalar missing/wrong-type (committed/recoverable/duplicate=boolean, revision=number)',
              operation: 'CreateCandidateRevision',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.candidate?.workId && !(go.transportError?.code === 'committed_recovery')) {
          return {
            revision: go.revision,
            duplicate: go.duplicate,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'CreateCandidateRevision committed but candidate.workId missing',
              operation: 'CreateCandidateRevision',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          candidate: go.candidate,
          clarification: go.clarification,
          impact: go.impact,
          revision: go.revision,
          duplicate: go.duplicate,
          committed: go.committed,
          recoverable: go.recoverable,
          transportError: go.transportError,
        };
      } catch (error) {
        return {
          revision: 0,
          duplicate: false,
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'CreateCandidateRevision',
            workId: input.workId,
            requestId: input.requestId,
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    retryWorkNode: async (input) => {
      try {
        const go = await app.RetryWorkNode(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.revision !== 'number' || !Number.isFinite(go.revision)) {
          return {
            revision: 0,
            duplicate: false,
            committed: false,
            recoverable: true,
            error: {
              code: 'contract_malformed',
              message: 'RetryWorkNode: required scalar missing/wrong-type (committed/recoverable/duplicate=boolean, revision=number)',
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.result?.id && !(go.error?.code === 'committed_recovery')) {
          return {
            revision: go.revision,
            duplicate: go.duplicate,
            committed: false,
            recoverable: true,
            error: {
              code: 'contract_malformed',
              message: 'RetryWorkNode committed but result.id missing',
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          result: go.result,
          revision: go.revision,
          duplicate: go.duplicate,
          committed: go.committed,
          recoverable: go.recoverable,
          error: go.error,
        };
      } catch (error) {
        return {
          revision: 0,
          duplicate: false,
          committed: false,
          recoverable: true,
          error: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    retryArtifactSlot: async (input) => {
      try {
        const go = await app.RetryArtifactSlot(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.revision !== 'number' || !Number.isFinite(go.revision)) {
          return {
            revision: 0,
            duplicate: false,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'RetryArtifactSlot: required scalar missing/wrong-type (committed/recoverable/duplicate=boolean, revision=number)',
              operation: 'RetryArtifactSlot',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.slot?.id && !(go.transportError?.code === 'committed_recovery')) {
          return {
            revision: go.revision,
            duplicate: go.duplicate,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'RetryArtifactSlot committed but slot.id missing',
              operation: 'RetryArtifactSlot',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          slot: go.slot,
          revision: go.revision,
          duplicate: go.duplicate,
          committed: go.committed,
          recoverable: go.recoverable,
          transportError: go.transportError,
        };
      } catch (error) {
        return {
          revision: 0,
          duplicate: false,
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'RetryArtifactSlot',
            workId: input.workId,
            requestId: input.requestId,
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    previewArtifact: async (input) => {
      try {
        const go = await app.PreviewArtifact(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean') {
          return {
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'PreviewArtifact: required scalar missing/wrong-type (committed/recoverable=boolean)',
              operation: 'PreviewArtifact',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.preview?.artifactId && !(go.transportError?.code === 'committed_recovery')) {
          return {
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'PreviewArtifact committed but preview.artifactId missing',
              operation: 'PreviewArtifact',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          preview: go.preview,
          committed: go.committed,
          recoverable: go.recoverable,
          transportError: go.transportError,
        };
      } catch (error) {
        return {
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'PreviewArtifact',
            workId: input.workId,
            requestId: input.requestId,
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    requestArtifactConversion: async (input) => {
      try {
        const go = await app.RequestArtifactConversion(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean') {
          return {
            committed: false,
            recoverable: true,
            duplicate: false,
            transportError: {
              code: 'contract_malformed',
              message: 'RequestArtifactConversion: required scalar missing/wrong-type (committed/recoverable/duplicate=boolean)',
              operation: 'RequestArtifactConversion',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.preview?.artifactId && !(go.transportError?.code === 'committed_recovery')) {
          return {
            committed: false,
            recoverable: true,
            duplicate: false,
            transportError: {
              code: 'contract_malformed',
              message: 'RequestArtifactConversion committed but preview.artifactId missing',
              operation: 'RequestArtifactConversion',
              workId: input.workId,
              requestId: input.requestId,
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          preview: go.preview,
          committed: go.committed,
          recoverable: go.recoverable,
          duplicate: go.duplicate,
          transportError: go.transportError,
        };
      } catch (error) {
        return {
          committed: false,
          recoverable: true,
          duplicate: false,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'RequestArtifactConversion',
            workId: input.workId,
            requestId: input.requestId,
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    selectWorkInputFile: async (input) => {
      try {
        const go = await app.SelectWorkInputFile(tabID, input);
        if (typeof go.canceled !== 'boolean') {
          return {
            canceled: false,
            error: {
              code: 'contract_malformed',
              message: 'SelectWorkInputFile: required scalar canceled missing/wrong-type (boolean)',
              committed: false,
              recoverable: true,
            },
          };
        }
        // Conditional: canceled=false with no explicit error → artifactRef must be valid.
        if (!go.canceled && !go.error) {
          const ref = go.artifactRef;
          const validStatuses = new Set(['available', 'stale', 'missing', 'failed']);
          const missing: string[] = [];
          if (!ref) { missing.push('artifactRef'); }
          else {
            if (typeof ref.id !== 'string' || !ref.id) missing.push('id');
            if (typeof ref.name !== 'string' || !ref.name) missing.push('name');
            if (typeof ref.type !== 'string' || !ref.type) missing.push('type');
            if (typeof ref.status !== 'string' || !ref.status) missing.push('status');
            else if (!validStatuses.has(ref.status)) missing.push(`status(${ref.status})`);
          }
          if (missing.length > 0) {
            return {
              canceled: false,
              error: {
                code: 'contract_malformed',
                message: `SelectWorkInputFile: canceled=false without error but artifactRef invalid: ${missing.join(', ')}`,
                committed: false,
                recoverable: true,
              },
            };
          }
        }
        return {
          artifactRef: go.artifactRef,
          canceled: go.canceled,
          error: go.error,
        };
      } catch (error) {
        return {
          canceled: false,
          error: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'SelectWorkInputFile',
            workId: input.workId,
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    submitWorkInput: async (input) => {
      try {
        const go = await app.SubmitWorkInput(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.revision !== 'number' || !Number.isFinite(go.revision)) {
          return {
            revision: 0,
            duplicate: false,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'SubmitWorkInput: required scalar missing/wrong-type (committed/recoverable/duplicate=boolean, revision=number)',
              operation: 'SubmitWorkInput',
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.input?.id && !(go.transportError?.code === 'committed_recovery')) {
          return {
            revision: go.revision,
            duplicate: go.duplicate,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'SubmitWorkInput committed but input.id missing',
              operation: 'SubmitWorkInput',
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          input: go.input,
          revision: go.revision,
          duplicate: go.duplicate,
          committed: go.committed,
          recoverable: go.recoverable,
          error: go.error,
          transportError: go.transportError,
          receipt: go.receipt,
        };
      } catch (error) {
        return {
          revision: 0,
          duplicate: false,
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'SubmitWorkInput',
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    setInputCornerstone: async (input) => {
      try {
        const go = await app.SetInputCornerstone(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.pinned !== 'boolean' || typeof go.revision !== 'number' || !Number.isFinite(go.revision)) {
          return {
            pinned: false,
            revision: 0,
            duplicate: false,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'SetInputCornerstone: required scalar missing/wrong-type (committed/recoverable/duplicate/pinned=boolean, revision=number)',
              operation: 'SetInputCornerstone',
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && go.pinned && !go.cornerstoneId) {
          return {
            pinned: go.pinned,
            revision: go.revision,
            duplicate: go.duplicate,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'SetInputCornerstone committed+pinned but cornerstoneId missing',
              operation: 'SetInputCornerstone',
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          cornerstoneId: go.cornerstoneId,
          pinned: go.pinned,
          revision: go.revision,
          duplicate: go.duplicate,
          committed: go.committed,
          recoverable: go.recoverable,
          error: go.error,
          transportError: go.transportError,
          receipt: go.receipt,
        };
      } catch (error) {
        return {
          pinned: false,
          revision: 0,
          duplicate: false,
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'SetInputCornerstone',
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    previewWorkPatch: async (input) => {
      try {
        const go = await app.PreviewWorkPatch(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.revision !== 'number' || !Number.isFinite(go.revision)) {
          return {
            revision: 0,
            duplicate: false,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'PreviewWorkPatch: required scalar missing/wrong-type (committed/recoverable/duplicate=boolean, revision=number)',
              operation: 'PreviewWorkPatch',
              committed: false,
              recoverable: true,
            },
          };
        }
        if (go.committed && !go.preview?.id && !(go.transportError?.code === 'committed_recovery')) {
          return {
            revision: go.revision,
            duplicate: go.duplicate,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'PreviewWorkPatch committed but preview.id missing',
              operation: 'PreviewWorkPatch',
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          preview: normalizeWorkPatchPreview(go.preview),
          revision: go.revision,
          duplicate: go.duplicate,
          committed: go.committed,
          recoverable: go.recoverable,
          error: go.error,
          transportError: go.transportError,
          receipt: go.receipt,
        };
      } catch (error) {
        return {
          revision: 0,
          duplicate: false,
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'PreviewWorkPatch',
            committed: false,
            recoverable: true,
          },
        };
      }
    },

    applyWorkPatch: async (input) => {
      try {
        const go = await app.ApplyWorkPatch(tabID, input);
        if (typeof go.committed !== 'boolean' || typeof go.recoverable !== 'boolean' || typeof go.duplicate !== 'boolean' || typeof go.workRevision !== 'number' || !Number.isFinite(go.workRevision) || typeof go.newRevision !== 'number' || !Number.isFinite(go.newRevision) || typeof go.requiresRerun !== 'boolean') {
          return {
            workRevision: 0,
            newRevision: 0,
            requiresRerun: false,
            duplicate: false,
            committed: false,
            recoverable: true,
            transportError: {
              code: 'contract_malformed',
              message: 'ApplyWorkPatch: required scalar missing/wrong-type (committed/recoverable/duplicate/requiresRerun=boolean, workRevision/newRevision=number)',
              operation: 'ApplyWorkPatch',
              committed: false,
              recoverable: true,
            },
          };
        }
        return {
          workRevision: go.workRevision,
          newRevision: go.newRevision,
          invalidatedTaskIds: go.invalidatedTaskIds,
          affectedBlockIds: go.affectedBlockIds,
          affectedArtifactSlotIds: go.affectedArtifactSlotIds,
          staleArtifactSlotIds: go.staleArtifactSlotIds,
          requiresRerun: go.requiresRerun,
          duplicate: go.duplicate,
          error: go.error,
          committed: go.committed,
          recoverable: go.recoverable,
          transportError: go.transportError,
          receipt: go.receipt,
        };
      } catch (error) {
        return {
          workRevision: 0,
          newRevision: 0,
          requiresRerun: false,
          duplicate: false,
          committed: false,
          recoverable: true,
          transportError: {
            code: 'transport_error',
            message: error instanceof Error ? error.message : String(error),
            operation: 'ApplyWorkPatch',
            committed: false,
            recoverable: true,
          },
        };
      }
    },
  };
}

// Kept as a narrow compatibility factory for callers that only render the
// Drawer. The production WorkCard consumes createWailsWorkControllerPort.
export function createWailsCornerstoneAdapter(tabID: string): CornerstoneControllerPort | undefined {
  return createWailsWorkControllerPort(tabID);
}
