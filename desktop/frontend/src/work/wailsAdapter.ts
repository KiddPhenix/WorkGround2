/**
 * WailsCornerstoneAdapter — real Wails bridge for CornerstoneControllerPort.
 *
 * Translates between the frontend CornerstoneControllerPort (rich TS DTOs with
 * workId embedded) and the Go Wails bindings (workId passed separately, Go DTOs
 * use PascalCase JSON tags). Handles revision conflicts by preserving draft
 * state and surfacing the latest snapshot. Same business retry reuses requestID;
 * new intent generates a fresh requestID.
 */

import type { CornerstoneControllerPort, WorkControllerPort } from './controller';
import type { WorkUIPreference } from './store';
import type {
  AcceptCornerstoneInput,
  Attempt,
  Cornerstone,
  CornerstoneMutationResult,
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
  CreateWork(tabID: string, input: CreateWorkInput): Promise<Work>;
  GetWork(tabID: string, workID: string): Promise<WorkView>;
  ListWorks(tabID: string, filter: WorkFilter): Promise<WorkPage>;
  UpdateDraft(tabID: string, input: UpdateDraftInput): Promise<WorkView>;
  RecoverWorkView(tabID: string, workID: string, input: ViewRecoveryIntent): Promise<WorkViewEvent>;
  RunWork(tabID: string, workID: string, requestID: string): Promise<WorkflowRun>;
  ResumeRun(tabID: string, input: ResumeRunInput): Promise<WorkflowRun>;
  RetryWorkTask(tabID: string, input: RetryTaskInput): Promise<Attempt>;
  ArchiveWork(tabID: string, workID: string, requestID: string): Promise<WorkRecord>;
  RestoreWork(tabID: string, workID: string, requestID: string): Promise<WorkView>;
  DeleteWork(tabID: string, workID: string, requestID: string): Promise<void>;
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
}

// --- Go DTO shapes (PascalCase JSON tags as returned by Wails) ---
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

// --- Helpers ---

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
      return parsed.activeFace === 'front' || parsed.activeFace === 'back'
        ? { activeFace: parsed.activeFace }
        : null;
    },

    writeUIPreference: async (workID, preference) => {
      window.localStorage.setItem(preferenceKey(tabID, workID), JSON.stringify({ activeFace: preference.activeFace }));
    },

    retryTask: (input) => app.RetryWorkTask(tabID, input),
    runWork: (input) => app.RunWork(tabID, input.workId, input.requestId),

    resumeRun: (input) => app.ResumeRun(tabID, input),

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
  };
}

// Kept as a narrow compatibility factory for callers that only render the
// Drawer. The production WorkCard consumes createWailsWorkControllerPort.
export function createWailsCornerstoneAdapter(tabID: string): CornerstoneControllerPort | undefined {
  return createWailsWorkControllerPort(tabID);
}
