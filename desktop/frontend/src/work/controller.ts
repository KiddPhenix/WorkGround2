import { useEffect, useMemo, useState } from 'react';

import {
  applySnapshot,
  applyWorkViewEvent,
  useWorkStore,
  useWorkUIStore,
  type ApplyResult,
  type WorkFace,
  type WorkUIPreference,
} from './store';
import { parseWorkViewEvent, parseWorkViewSnapshot } from './parse';
import type {
  AcceptCornerstoneInput,
  Attempt,
  BlockUpsertInput,
  CornerstoneMutationResult,
  FreezeCornerstoneInput,
  PinCornerstoneInput,
  RefreshCornerstoneInput,
  RemoveCornerstoneInput,
  RepairCornerstoneInput,
  ResumeRunInput,
  RetryTaskInput,
  UndoCornerstoneInput,
  UpdateDraftInput,
  ValidateCornerstoneInput,
  ViewRecoveryIntent,
  WorkView,
  WorkViewEvent,
  WorkflowRun,
} from './types';
import type {
  ApplyDefinitionInput,
  ApplyDefinitionResult,
  ApplyWorkPatchRequest,
  ApplyWorkPatchResult,
  BeginWorkPlanningInput,
  BeginWorkPlanningResult,
  CornerstonePinResult,
  CreateCandidateRevisionInput,
  CreateCandidateRevisionResult,
  DefinitionPlanProgress,
  ArtifactPreview,
  PreviewArtifactRequest,
  PreviewArtifactResult,
  RequestArtifactConversionInput,
  RequestArtifactConversionResult,
  PreviewWorkPatchRequest,
  PreviewWorkPatchResult,
  RetryWorkNodeRequest,
  RetryWorkNodeResult,
  RetryArtifactSlotRequest,
  RetryArtifactSlotResult,
  SelectWorkInputFileRequest,
  SelectWorkInputFileResult,
  SetInputCornerstoneRequest,
  SubmitInputResult,
  SubmitWorkInputRequest,
} from './types_v2';
export interface CornerstoneControllerPort {
  pinCornerstone?: (input: PinCornerstoneInput) => Promise<CornerstoneMutationResult>;
  refreshCornerstone?: (input: RefreshCornerstoneInput) => Promise<CornerstoneMutationResult>;
  validateCornerstone?: (input: ValidateCornerstoneInput) => Promise<CornerstoneMutationResult>;
  freezeCornerstone?: (input: FreezeCornerstoneInput) => Promise<CornerstoneMutationResult>;
  acceptCornerstone?: (input: AcceptCornerstoneInput) => Promise<CornerstoneMutationResult>;
  repairCornerstone?: (input: RepairCornerstoneInput) => Promise<CornerstoneMutationResult>;
  removeCornerstone?: (input: RemoveCornerstoneInput) => Promise<CornerstoneMutationResult>;
  undoCornerstone?: (input: UndoCornerstoneInput) => Promise<CornerstoneMutationResult>;
}

export interface WorkPortSubscription {
  /** Resolves only after the backend has installed the server-side watch. */
  ready: Promise<void>;
  /** Idempotently removes both the local listener and the backend watch. */
  unsubscribe: () => void;
}

export interface WorkControllerPort extends CornerstoneControllerPort {
  subscribe: (workID: string, onEvent: (event: WorkViewEvent) => void) => WorkPortSubscription;
  fetchSnapshot: (workID: string) => Promise<unknown>;
  fetchRecoverySnapshot: (workID: string, intent: ViewRecoveryIntent) => Promise<unknown>;
  readUIPreference: (workID: string) => Promise<WorkUIPreference | null>;
  writeUIPreference: (workID: string, preference: WorkUIPreference) => Promise<void>;
  retryTask?: (input: RetryTaskInput) => Promise<Attempt>;
  runWork?: (input: { workId: string; requestId: string }) => Promise<WorkflowRun>;
  resumeRun?: (input: ResumeRunInput) => Promise<WorkflowRun>;
  updateDraft?: (input: UpdateDraftInput) => Promise<WorkView>;
  upsertBlock?: (input: BlockUpsertInput) => Promise<WorkView>;
  beginWorkPlanning?: (input: BeginWorkPlanningInput) => Promise<BeginWorkPlanningResult>;
  applyDefinition?: (input: ApplyDefinitionInput) => Promise<ApplyDefinitionResult>;
  createCandidateRevision?: (input: CreateCandidateRevisionInput) => Promise<CreateCandidateRevisionResult>;
  retryWorkNode?: (input: RetryWorkNodeRequest) => Promise<RetryWorkNodeResult>;
  retryArtifactSlot?: (input: RetryArtifactSlotRequest) => Promise<RetryArtifactSlotResult>;
  previewArtifact?: (input: PreviewArtifactRequest) => Promise<PreviewArtifactResult>;
  requestArtifactConversion?: (input: RequestArtifactConversionInput) => Promise<RequestArtifactConversionResult>;
  selectWorkInputFile?: (input: SelectWorkInputFileRequest) => Promise<SelectWorkInputFileResult>;
  submitWorkInput?: (input: SubmitWorkInputRequest) => Promise<SubmitInputResult>;
  setInputCornerstone?: (input: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  previewWorkPatch?: (input: PreviewWorkPatchRequest) => Promise<PreviewWorkPatchResult>;
  applyWorkPatch?: (input: ApplyWorkPatchRequest) => Promise<ApplyWorkPatchResult>;
}

export type WorkStreamHealth =
  | { kind: 'idle' }
  | { kind: 'connecting' }
  | { kind: 'online' }
  | { kind: 'offline'; message: string };

export interface WorkControllerStatus {
  fetching: boolean;
  snapshotError: string | null;
  preferenceError: string | null;
  stream: WorkStreamHealth;
  /** Projection/event reducer error; transport health lives in stream. */
  eventError: string | null;
  /** Raw future-schema data is retained for diagnostics/export but never applied. */
  unsupportedView: {
    source: 'fetch' | 'recover' | 'watch';
    schemaVersion: number;
    raw: string;
  } | null;
}

const idleStatus = (): WorkControllerStatus => ({
  fetching: false,
  snapshotError: null,
  preferenceError: null,
  stream: { kind: 'idle' },
  eventError: null,
  unsupportedView: null,
});

const MAX_SNAPSHOT_RECOVERY_FETCHES = 3;
const MAX_RECOVERY_EVENT_IDS = 256;

interface WorkSubscriptionState {
  token: symbol;
  generation: number;
  recoveryReason: ViewRecoveryIntent['reason'] | null;
  port: WorkPortSubscription | null;
  watchReady: boolean;
  settling: boolean;
  buffering: boolean;
  events: WorkViewEvent[];
  recoveryEventIDs: Set<string>;
  settlement?: WorkSubscriptionSettlement;
}

interface WorkSubscriptionSettlement {
  promise: Promise<ApplyResult>;
  resolve: (result: ApplyResult) => void;
  reject: (error: unknown) => void;
  settled: boolean;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function revisionConflict(error: unknown): { actualRevision?: number } | null {
  const value = typeof error === 'object' && error !== null
    ? error as { code?: unknown; actualRevision?: unknown; currentRevision?: unknown }
    : null;
  const match = errorText(error).match(
    /work event conflict\b.*expected revision \d+,\s*current revision (\d+)/i,
  );
  if (value?.code !== 'revision_conflict' && !match) return null;
  const actual = value?.actualRevision ?? value?.currentRevision ?? (match ? Number(match[1]) : undefined);
  return Number.isSafeInteger(actual) ? { actualRevision: actual as number } : {};
}

function shouldReplayUnconfirmedPatch(result: ApplyWorkPatchResult): boolean {
  if (result.committed || result.error) return false;
  const code = result.transportError?.code;
  return !code || code === 'transport_error' || code === 'contract_malformed' || code === 'work_error';
}

function isOverflowRecoveryFailure(event: WorkViewEvent): boolean {
  if (event.type !== 'attention' || typeof event.payload !== 'object' || event.payload === null || Array.isArray(event.payload)) return false;
  const payload = event.payload as Record<string, unknown>;
  return payload.overflow === true && payload.recovery === 'failed' && payload.retryable === true;
}

function isV2ProjectionEvent(event: WorkViewEvent): boolean {
  if (event.schemaVersion >= 2) return true;
  if (typeof event.payload !== 'object' || event.payload === null || Array.isArray(event.payload)) return false;
  const payload = event.payload as Record<string, unknown>;
  if (event.type === 'snapshot' && payload.schemaVersion === 2) return true;
  return [
    'definition',
    'artifactSlots',
    'tasks',
    'patchPreviews',
    'removedArtifactSlotIds',
    'removedTaskIds',
    'removedInputIds',
    'removedPatchIds',
  ].some((field) => field in payload) || Array.isArray(payload.inputs);
}

function needsProjectionRecovery(event: WorkViewEvent, result: ApplyResult): boolean {
  return result.kind === 'gap' || (result.kind === 'conflict' && (isV2ProjectionEvent(event) || event.type === 'snapshot'));
}

function claimProjectionRecovery(state: WorkSubscriptionState, event: WorkViewEvent, result: ApplyResult): boolean {
  if (!needsProjectionRecovery(event, result) || state.recoveryEventIDs.has(event.eventID)) return false;
  if (state.recoveryEventIDs.size >= MAX_RECOVERY_EVENT_IDS) {
    const oldest = state.recoveryEventIDs.values().next().value;
    if (oldest !== undefined) state.recoveryEventIDs.delete(oldest);
  }
  state.recoveryEventIDs.add(event.eventID);
  return true;
}

function createSubscriptionSettlement(): WorkSubscriptionSettlement {
  let resolvePromise!: (result: ApplyResult) => void;
  let rejectPromise!: (error: unknown) => void;
  const settlement: WorkSubscriptionSettlement = {
    promise: new Promise<ApplyResult>((resolve, reject) => {
      resolvePromise = resolve;
      rejectPromise = reject;
    }),
    resolve: (result) => {
      if (settlement.settled) return;
      settlement.settled = true;
      resolvePromise(result);
    },
    reject: (error) => {
      if (settlement.settled) return;
      settlement.settled = true;
      rejectPromise(error);
    },
    settled: false,
  };
  return settlement;
}

export class WorkControllerAdapter {
  private readonly subscriptions = new Map<string, WorkSubscriptionState>();
  private readonly pendingSnapshots = new Map<string, Promise<ApplyResult>>();
  private readonly pendingReconciliations = new Map<string, Promise<ApplyResult>>();
  private readonly pendingRetries = new Map<string, Promise<Attempt>>();
  private readonly statusByWork: Record<string, WorkControllerStatus> = {};
  private readonly statusListeners = new Set<() => void>();
  private readonly planningListeners = new Map<string, Set<(progress: DefinitionPlanProgress) => void>>();
  private readonly subscriptionGenerations = new Map<string, number>();
  private snapshotEventGeneration = 0;
  private disposed = false;

  constructor(private readonly port: WorkControllerPort) {}

  private updateStatus(workID: string, patch: Partial<WorkControllerStatus>): void {
    this.statusByWork[workID] = { ...(this.statusByWork[workID] ?? idleStatus()), ...patch };
    for (const listener of this.statusListeners) listener();
  }

  subscribeStatus = (listener: () => void): (() => void) => {
    this.statusListeners.add(listener);
    return () => this.statusListeners.delete(listener);
  };

  getStatus = (workID: string): WorkControllerStatus => ({
    ...(this.statusByWork[workID] ?? idleStatus()),
  });

  getStatuses = (): Record<string, WorkControllerStatus> => Object.fromEntries(
    Object.entries(this.statusByWork).map(([workID, status]) => [workID, { ...status }]),
  );

  subscribe = (workID: string): void => {
    // When the store already holds a projection (typical unmount/remount),
    // request a backend-issued typed hydrate resync. Hydrate only accepts
    // I/O-derived assessment/runBlock changes at the same revision; content
    // changes are still rejected as conflicts.
    // First-mount (empty store) uses a plain GetWork snapshot.
    const hasProjection = useWorkStore.getState().works[workID] !== undefined;
    this.startSubscription(workID, hasProjection ? 'hydrate' : null);
  };

  private startSubscription(
    workID: string,
    reason: ViewRecoveryIntent['reason'] | null,
    recoveryEventIDs = new Set<string>(),
    settlement?: WorkSubscriptionSettlement,
  ): void {
    if (this.disposed) {
      settlement?.reject(new Error('Work controller is disposed'));
      return;
    }
    if (this.subscriptions.has(workID)) {
      settlement?.reject(new Error(`Work ${workID} already has an active subscription`));
      return;
    }
    if (this.getStatus(workID).stream.kind !== 'offline') {
      this.updateStatus(workID, { stream: { kind: 'connecting' } });
    }
    const generation = (this.subscriptionGenerations.get(workID) ?? 0) + 1;
    this.subscriptionGenerations.set(workID, generation);
    const token = Symbol(`${workID}:${generation}`);
    const state: WorkSubscriptionState = {
      token,
      generation,
      recoveryReason: reason,
      port: null,
      watchReady: false,
      settling: true,
      buffering: true,
      events: [] as WorkViewEvent[],
      recoveryEventIDs,
      settlement,
    };
    const onEvent = (event: WorkViewEvent): void => {
      if (this.subscriptions.get(workID)?.token !== token) return;
      const parsed = this.parseEvent(workID, event);
      if (!parsed) return;
      if (parsed.workID !== workID) {
        this.updateStatus(workID, { eventError: `received event for ${parsed.workID} on ${workID} subscription` });
        return;
      }
      if (state.recoveryEventIDs.has(parsed.eventID)) return;
      this.notifyPlanning(parsed);
      if (state.buffering) {
        state.events.push(parsed);
        return;
      }
      const result = this.applyParsedEvent(parsed, true);
      if (claimProjectionRecovery(state, parsed, result)) {
        this.restartSubscription(workID, state);
      }
    };
    let subscription: WorkPortSubscription;
    try {
      subscription = this.port.subscribe(workID, onEvent);
      state.port = subscription;
    } catch (error) {
      this.updateStatus(workID, { stream: { kind: 'offline', message: errorText(error) } });
      settlement?.reject(error);
      return;
    }
    this.subscriptions.set(workID, state);
    void Promise.resolve(subscription.ready)
      .then(async () => {
        if (!this.isCurrentSubscription(workID, state)) {
          subscription.unsubscribe();
          return;
        }
        state.watchReady = true;

        // The Watch must exist before either snapshot is requested. Initial
        // hydration keeps ordinary revision conflict rules; explicit retry
        // requests a backend-issued typed authoritative resync.
        const snapshotResult = state.recoveryReason
          ? await this.recoverSubscriptionSnapshot(workID, state)
          : await this.recoverSnapshot(workID);
        // hydrate conflict at the same revision means content genuinely
        // diverged beyond assessment-only merge. Escalate to a full
        // authoritative retry overwrite instead of going offline.
        if (snapshotResult.kind === 'conflict' && state.recoveryReason === 'hydrate') {
          if (this.isCurrentSubscription(workID, state)) {
            this.restartSubscription(workID, state);
          } else {
            subscription.unsubscribe();
          }
          return;
        }
        let observedUnsupported = snapshotResult.kind === 'unsupported';
        if (!this.isCurrentSubscription(workID, state)) {
          subscription.unsubscribe();
          return;
        }
        const buffered = this.flushBuffered(workID, state);
        if (buffered.retryableFailure) throw new Error('authoritative overflow recovery failed; retry synchronization');
        if (buffered.needsRecovery) {
          this.restartSubscription(workID, state);
          return;
        }
        if (!this.isCurrentSubscription(workID, state)) {
          subscription.unsubscribe();
          return;
        }
        state.settling = false;
        if (observedUnsupported) {
          this.updateStatus(workID, {
            stream: { kind: 'online' },
            fetching: false,
          });
          settlement?.resolve(snapshotResult);
          return;
        }
        this.updateStatus(workID, {
          stream: { kind: 'online' },
          snapshotError: null,
          eventError: null,
          unsupportedView: null,
          fetching: false,
        });
        settlement?.resolve(snapshotResult);
      })
      .catch((error: unknown) => {
        if (!this.isCurrentSubscription(workID, state)) return;
        state.settling = false;
        const message = errorText(error);
        this.updateStatus(workID, {
          stream: { kind: 'offline', message },
          snapshotError: message,
          fetching: false,
        });
        // A failed Watch was never installed. Snapshot/apply failures keep the
        // installed Watch until explicit retry replaces its whole generation.
        if (!state.watchReady) {
          subscription.unsubscribe();
          this.subscriptions.delete(workID);
        }
        settlement?.reject(error);
      });
  }

  private isCurrentSubscription(workID: string, state: WorkSubscriptionState): boolean {
    return !this.disposed && this.subscriptions.get(workID)?.token === state.token;
  }

  unsubscribe = (workID: string): void => {
    const state = this.subscriptions.get(workID);
    state?.port?.unsubscribe();
    state?.settlement?.reject(new Error(`Work ${workID} subscription was closed before synchronization completed`));
    this.subscriptions.delete(workID);
  };

  subscribePlanning = (
    workID: string,
    listener: (progress: DefinitionPlanProgress) => void,
  ): (() => void) => {
    let listeners = this.planningListeners.get(workID);
    if (!listeners) {
      listeners = new Set();
      this.planningListeners.set(workID, listeners);
    }
    listeners.add(listener);
    return () => {
      listeners?.delete(listener);
      if (listeners?.size === 0) this.planningListeners.delete(workID);
    };
  };

  private notifyPlanning(event: WorkViewEvent): void {
    if (event.type !== 'attention' || typeof event.payload !== 'object' || event.payload === null || Array.isArray(event.payload)) return;
    const planning = (event.payload as Record<string, unknown>).planning;
    if (typeof planning !== 'object' || planning === null || Array.isArray(planning)) return;
    const value = planning as Record<string, unknown>;
    if (
      typeof value.requestId !== 'string'
      || !Number.isSafeInteger(value.sequence)
      || Number(value.sequence) <= 0
      || typeof value.kind !== 'string'
      || typeof value.text !== 'string'
      || typeof value.state !== 'string'
    ) return;
    const progress = value as unknown as DefinitionPlanProgress;
    for (const listener of this.planningListeners.get(event.workID) ?? []) {
      listener(progress);
    }
  }

  retrySubscription = (workID: string): void => {
    void this.reconcileSnapshot(workID).catch(() => undefined);
  };

  private restartSubscription(workID: string, state: WorkSubscriptionState): void {
    if (!this.isCurrentSubscription(workID, state)) return;
    state.port?.unsubscribe();
    this.subscriptions.delete(workID);
    this.startSubscription(workID, 'retry', state.recoveryEventIDs, state.settlement);
  }

  private flushBuffered(workID: string, state: WorkSubscriptionState): { needsRecovery: boolean; retryableFailure: boolean } {
    if (!this.isCurrentSubscription(workID, state)) return { needsRecovery: false, retryableFailure: false };
    let needsRecovery = false;
    let retryableFailure = false;
    while (state.events.length > 0) {
      const pending = state.events.splice(0);
      for (const event of pending) {
        if (state.recoveryEventIDs.has(event.eventID)) continue;
        const result = this.applyParsedEvent(event, true);
        if (claimProjectionRecovery(state, event, result)) needsRecovery = true;
        if (isOverflowRecoveryFailure(event)) retryableFailure = true;
      }
    }
    state.buffering = false;
    return { needsRecovery, retryableFailure };
  }

  private async recoverSubscriptionSnapshot(workID: string, state: WorkSubscriptionState): Promise<ApplyResult> {
    this.updateStatus(workID, { fetching: true });
    const reason = state.recoveryReason!;
    const rawEvent = await this.port.fetchRecoverySnapshot(workID, { reason, generation: state.generation });
    const parsed = parseWorkViewEvent(JSON.stringify(rawEvent));
    if (parsed.futureError) {
      return this.observeUnsupported(workID, parsed.futureError.got, parsed.raw, 'recover');
    }
    const event = parsed.event;
    if (!event) throw new Error(`backend returned an unreadable authoritative ${reason} snapshot`);
    if (!this.isCurrentSubscription(workID, state)) throw new Error('stale recovery generation');
    if (event.workID !== workID || event.type !== 'snapshot' || event.resync?.reason !== reason ||
        !event.resync?.authoritative || (event.resync?.generation ?? 0) < state.generation) {
      throw new Error(`backend returned an invalid authoritative ${reason} snapshot`);
    }
    const result = applyWorkViewEvent(event);
    // Another adapter may have already applied a newer backend-global
    // generation while this valid response was in flight. That makes the
    // response harmlessly ignored, not a failed subscription handshake.
    if (result.kind !== 'applied' && result.kind !== 'duplicate' && result.kind !== 'ignored') {
      // hydrate at the same revision may legitimately conflict when
      // content diverged (store protects against silent overwrite of
      // non-assessment fields). Return the conflict so the caller can
      // escalate to a full authoritative retry overwrite.
      if (reason === 'hydrate' && result.kind === 'conflict') return result;
      const detail = result.kind === 'conflict' ? result.conflict.reason : result.kind;
      throw new Error(`authoritative ${reason} snapshot was not applied: ${detail}`);
    }
    return result;
  }

  private parseEvent(statusWorkID: string, event: WorkViewEvent): WorkViewEvent | null {
    try {
      const parsed = parseWorkViewEvent(JSON.stringify(event));
      if (parsed.futureError) {
        this.observeUnsupported(statusWorkID, parsed.futureError.got, parsed.raw, 'watch');
        return null;
      }
      return parsed.event;
    } catch (error) {
      this.updateStatus(statusWorkID, { eventError: errorText(error) });
      return null;
    }
  }

  private applyParsedEvent(event: WorkViewEvent, suppressRecoverableConflict = false): ApplyResult {
    const result = applyWorkViewEvent(event);
    if (isOverflowRecoveryFailure(event)) {
      this.updateStatus(event.workID, {
        stream: { kind: 'offline', message: 'authoritative overflow recovery failed; retry synchronization' },
        snapshotError: 'authoritative overflow recovery failed; retry synchronization',
      });
    } else if (result.kind === 'conflict' && !(suppressRecoverableConflict && needsProjectionRecovery(event, result))) {
      this.updateStatus(event.workID, { eventError: result.conflict.reason });
    } else if (result.kind === 'applied') {
      this.updateStatus(event.workID, {
        eventError: null,
        ...(event.resync?.reason === 'overflow' && event.resync.authoritative
          ? { stream: { kind: 'online' as const }, snapshotError: null }
          : {}),
      });
    }
    return result;
  }

  applyEvent = (event: WorkViewEvent): ApplyResult => {
    const parsed = this.parseEvent(event.workID, event);
    if (!parsed) return { kind: 'ignored', workID: event.workID, eventID: event.eventID };
    return this.applyParsedEvent(parsed);
  };

  /**
   * Reconcile after a committed write through one complete
   * Watch -> authoritative retry snapshot handshake. Unlike an ordinary
   * fetch, the backend-issued retry snapshot may replace different content at
   * the current revision, so transient event/RPC ordering never leaks to UI.
   */
  reconcileSnapshot = (workID: string): Promise<ApplyResult> => {
    const pending = this.pendingReconciliations.get(workID);
    if (pending) return pending;
    if (this.disposed) return Promise.reject(new Error('Work controller is disposed'));

    const settlement = createSubscriptionSettlement();
    const request = settlement.promise.finally(() => {
      if (this.pendingReconciliations.get(workID) === request) {
        this.pendingReconciliations.delete(workID);
      }
    });
    this.pendingReconciliations.set(workID, request);

    const state = this.subscriptions.get(workID);
    const recoveryEventIDs = state?.recoveryEventIDs ?? new Set<string>();
    state?.port?.unsubscribe();
    this.subscriptions.delete(workID);
    this.updateStatus(workID, {
      stream: { kind: 'connecting' },
      snapshotError: null,
      eventError: null,
      unsupportedView: null,
    });
    this.startSubscription(workID, 'retry', recoveryEventIDs, settlement);
    return request;
  };

  recoverSnapshot = (workID: string): Promise<ApplyResult> => {
    const pending = this.pendingSnapshots.get(workID);
    if (pending) return pending;
    this.updateStatus(workID, { fetching: true });
    const request = Promise.resolve()
      .then(async () => {
        let previousRevision = useWorkStore.getState().revisions[workID] ?? -1;
        for (let attempt = 0; attempt < MAX_SNAPSHOT_RECOVERY_FETCHES; attempt++) {
          const fetched = await this.port.fetchSnapshot(workID);
          if (this.disposed) {
            return { kind: 'ignored' as const, workID, eventID: `fetch:${workID}:disposed` };
          }
          const parsed = parseWorkViewSnapshot(fetched);
          if (parsed.kind === 'unsupported') {
            return this.observeUnsupported(workID, parsed.schemaVersion, parsed.raw, 'fetch');
          }
          const view = parsed.view;
          if (view.work.id !== workID) throw new Error(`snapshot workID ${view.work.id} does not match ${workID}`);
          const eventGeneration = ++this.snapshotEventGeneration;
          const result = applySnapshot(view, `fetch:${workID}:${view.revision}:${eventGeneration}`);
          if (result.kind === 'conflict') {
            if (result.conflict.reason === 'different snapshot at the current revision') {
              return this.reconcileSnapshot(workID);
            }
            throw new Error(result.conflict.reason);
          }
          const state = useWorkStore.getState();
          const issue = state.gaps[workID];
          if (!issue) {
            this.updateStatus(workID, { snapshotError: null, eventError: null, unsupportedView: null });
            return result;
          }
          const currentRevision = state.revisions[workID] ?? -1;
          if (currentRevision <= previousRevision) {
            throw new Error(
              `snapshot revision ${view.revision} did not repair the projection gap through revision ${issue.eventRevision}`,
            );
          }
          previousRevision = currentRevision;
        }
        const issue = useWorkStore.getState().gaps[workID];
        throw new Error(
          `snapshot recovery exceeded ${MAX_SNAPSHOT_RECOVERY_FETCHES} fetches without repairing the projection gap through revision ${issue?.eventRevision ?? 'unknown'}`,
        );
      })
      .catch((error: unknown) => {
        this.updateStatus(workID, { snapshotError: errorText(error) });
        throw error;
      })
      .finally(() => {
        this.pendingSnapshots.delete(workID);
        this.updateStatus(workID, { fetching: false });
      });
    this.pendingSnapshots.set(workID, request);
    return request;
  };

  /** Fetch authoritative snapshots until the store revision reaches at least
   *  minimumRevision, or exhaust retries. Used after a committed write whose
   *  result does not include a trustworthy WorkView payload — the store must
   *  catch up to the backend's authoritative revision before callers can read
   *  expectedRevision for the next write. */
  private async recoverSnapshotToRevision(workID: string, minimumRevision: number): Promise<void> {
    for (let attempt = 0; attempt < MAX_SNAPSHOT_RECOVERY_FETCHES + 1; attempt++) {
      try {
        await this.recoverSnapshot(workID);
      } catch {
        // recoverSnapshot already sets snapshotError; continue retrying.
      }
      const currentRevision = useWorkStore.getState().revisions[workID] ?? -1;
      if (currentRevision >= minimumRevision) {
        this.updateStatus(workID, { snapshotError: null });
        return;
      }
    }
    const currentRevision = useWorkStore.getState().revisions[workID] ?? -1;
    const message = `权威状态刷新 ${MAX_SNAPSHOT_RECOVERY_FETCHES + 1} 次后仍停留在 revision ${currentRevision}，未达到 revision ${minimumRevision}，请重试。`;
    this.updateStatus(workID, { snapshotError: message });
    throw Object.assign(new Error(message), {
      committed: true, recoverable: true, code: 'snapshot_stale',
    });
  }

  restoreUIPreference = async (workID: string): Promise<WorkUIPreference | null> => {
    try {
      const preference = await this.port.readUIPreference(workID);
      if (preference) useWorkUIStore.getState().setActiveFace(workID, preference.activeFace);
      this.updateStatus(workID, { preferenceError: null });
      return preference;
    } catch (error) {
      this.updateStatus(workID, { preferenceError: errorText(error) });
      throw error;
    }
  };

  setActiveFace = async (workID: string, activeFace: WorkFace): Promise<void> => {
    useWorkUIStore.getState().setActiveFace(workID, activeFace);
    try {
      await this.port.writeUIPreference(workID, { activeFace });
      this.updateStatus(workID, { preferenceError: null });
    } catch (error) {
      this.updateStatus(workID, { preferenceError: errorText(error) });
      throw error;
    }
  };

  retryTask = (input: RetryTaskInput): Promise<Attempt> => {
    const pending = this.pendingRetries.get(input.requestId);
    if (pending) return pending;
    if (!this.port.retryTask) return Promise.reject(new Error('Work Task 重试能力尚未连接。'));
    const request = Promise.resolve()
      .then(() => this.port.retryTask!(input))
      .finally(() => this.pendingRetries.delete(input.requestId));
    this.pendingRetries.set(input.requestId, request);
    return request;
  };

  beginWorkPlanning = async (input: BeginWorkPlanningInput): Promise<BeginWorkPlanningResult> => {
    if (!this.port.beginWorkPlanning) throw new Error('Work 对话规划能力尚未连接。');
    return this.port.beginWorkPlanning(input);
  };

  applyDefinition = async (input: ApplyDefinitionInput): Promise<ApplyDefinitionResult> => {
    if (!this.port.applyDefinition) throw new Error('Work 定义应用能力尚未连接。');
    const result = await this.port.applyDefinition(input);
    if (result.view) {
      const eventID = `apply-def:${input.requestId}:${result.revision}`;
      if (!this.applyMutationView(input.workId, result.view, eventID)) {
        await this.recoverSnapshot(input.workId);
      }
    } else if (
      (result.committed && result.recoverable) ||
      (!result.duplicate && !result.committed && !result.transportError)
    ) {
      // A committed-recovery response has durable state but no trustworthy
      // response body. Re-read the authoritative projection before callers
      // switch faces. A body-less claimed revision is recovered as well.
      await this.recoverSnapshot(input.workId);
    }
    return result;
  };

  createCandidateRevision = async (
    input: CreateCandidateRevisionInput,
  ): Promise<CreateCandidateRevisionResult> => {
    if (!this.port.createCandidateRevision) throw new Error('Work 候选定义生成能力尚未连接。');
    let result = await this.port.createCandidateRevision(input);
    if (!result.committed && result.transportError?.code === 'revision_conflict') {
      await this.recoverSnapshot(input.workId);
      const expectedRevision = useWorkStore.getState().revisions[input.workId];
      if (!Number.isSafeInteger(expectedRevision)) {
        throw new Error(`Work ${input.workId} 权威 revision 未载入，候选定义未重试。`);
      }
      result = await this.port.createCandidateRevision({
        ...input,
        expectedRevision: expectedRevision!,
      });
    }
    if (result.committed && !result.candidate) {
      // A body-less commit cannot safely drive the next write, so recover the
      // authoritative projection before returning. When Candidate is present,
      // the mutation response already carries the complete committed fragment
      // and its aggregate revision. Event delivery may legitimately lag behind
      // that write; callers can continue with result.revision while the normal
      // subscription converges the read-side projection.
      try {
        await this.recoverSnapshotToRevision(input.workId, result.revision);
      } catch (err) {
        if (!result.recoverable) {
          result.recoverable = true;
          result.transportError = {
            code: (err as { code?: string }).code ?? 'snapshot_stale',
            message: (err as Error).message,
            operation: 'CreateCandidateRevision',
            workId: input.workId,
            requestId: input.requestId,
            revision: result.revision,
            committed: true,
            recoverable: true,
          };
        }
        throw Object.assign(
          new Error(result.transportError?.message ?? (err as Error).message),
          { code: result.transportError?.code ?? 'snapshot_stale', committed: true, recoverable: true },
        );
      }
    }
    if (!result.committed && !result.clarification) {
      if (result.transportError?.code === 'revision_conflict') {
        await this.recoverSnapshot(input.workId);
      }
      throw Object.assign(
        new Error(result.transportError?.message || 'Work 候选定义未生成。'),
        { code: result.transportError?.code },
      );
    }
    return result;
  };

  retryWorkNode = async (input: RetryWorkNodeRequest): Promise<RetryWorkNodeResult> => {
    if (!this.port.retryWorkNode) throw new Error('Work V2 节点重试能力尚未连接。');
    const result = await this.port.retryWorkNode(input);
    if (!result.committed) {
      if (result.error?.code === 'revision_conflict') {
        await this.recoverSnapshot(input.workId);
      }
      throw Object.assign(
        new Error(result.error?.message || 'Work 节点重试未提交。'),
        { code: result.error?.code },
      );
    }
    return result;
  };

  retryArtifactSlot = async (
    input: RetryArtifactSlotRequest,
  ): Promise<RetryArtifactSlotResult> => {
    if (!this.port.retryArtifactSlot) throw new Error('Work 成果重试能力尚未连接。');
    const result = await this.port.retryArtifactSlot(input);
    if (result.committed && result.recoverable) {
      await this.recoverSnapshot(input.workId);
    }
    if (!result.committed) {
      if (result.transportError?.code === 'revision_conflict') {
        await this.recoverSnapshot(input.workId);
      }
      throw Object.assign(
        new Error(result.transportError?.message || 'Work 成果重试未提交。'),
        { code: result.transportError?.code },
      );
    }
    return result;
  };

  previewArtifact = async (
    input: PreviewArtifactRequest,
  ): Promise<ArtifactPreview> => {
    if (!this.port.previewArtifact) throw new Error('Work 预览能力尚未连接。');
    const result = await this.port.previewArtifact(input);
    if (result.transportError && !result.committed) {
      throw Object.assign(
        new Error(result.transportError.message || '预览生成失败。'),
        { code: result.transportError.code },
      );
    }
    if (!result.preview) throw new Error('预览结果为空。');
    return result.preview;
  };

  requestArtifactConversion = async (
    input: RequestArtifactConversionInput,
  ): Promise<ArtifactPreview> => {
    if (!this.port.requestArtifactConversion) throw new Error('Work 转换能力尚未连接。');
    const result = await this.port.requestArtifactConversion(input);
    if (result.transportError && !result.committed) {
      throw Object.assign(
        new Error(result.transportError.message || '转换请求失败。'),
        { code: result.transportError.code, recoverable: result.recoverable },
      );
    }
    if (!result.preview) throw new Error('转换结果为空。');
    return result.preview;
  };

  selectWorkInputFile = async (
    input: SelectWorkInputFileRequest,
  ): Promise<SelectWorkInputFileResult> => {
    if (!this.port.selectWorkInputFile) throw new Error('Work 文件选择能力尚未连接。');
    const result = await this.port.selectWorkInputFile(input);
    if (result.error) {
      throw Object.assign(new Error(result.error.message), { code: result.error.code });
    }
    return result;
  };

  submitWorkInput = async (input: SubmitWorkInputRequest): Promise<SubmitInputResult> => {
    if (!this.port.submitWorkInput) throw new Error('Work 输入提交能力尚未连接。');
    let result = await this.port.submitWorkInput(input);
    if (!result.committed && result.transportError?.code === 'revision_conflict') {
      await this.recoverSnapshot(input.workId);
      const expectedRevision = useWorkStore.getState().revisions[input.workId];
      if (!Number.isSafeInteger(expectedRevision)) {
        throw new Error(`Work ${input.workId} 权威 revision 未载入，输入提交未重试。`);
      }
      result = await this.port.submitWorkInput({
        ...input,
        expectedRevision: expectedRevision!,
      });
    }
    if (!result.committed) {
      if (result.transportError?.code === 'revision_conflict') {
        await this.recoverSnapshot(input.workId);
      }
      throw Object.assign(
        new Error(result.transportError?.message || result.error || 'Work 输入提交未确认。'),
        { code: result.transportError?.code },
      );
    }
    if (!result.receipt) {
      return {
        ...result,
        recoverable: true,
        transportError: {
          code: 'contract_receipt_missing',
          message: '输入已提交，但响应缺少 InputIntentReceipt；正在刷新权威状态。',
          operation: 'SubmitWorkInput',
          workId: input.workId,
          requestId: input.requestId,
          committed: true,
          recoverable: true,
        },
      };
    }
    return result;
  };

  setInputCornerstone = async (input: SetInputCornerstoneRequest): Promise<CornerstonePinResult> => {
    if (!this.port.setInputCornerstone) throw new Error('Work Cornerstone 能力尚未连接。');
    const result = await this.port.setInputCornerstone(input);
    if (!result.committed) {
      if (result.transportError?.code === 'revision_conflict') {
        await this.recoverSnapshot(input.workId);
      }
      throw Object.assign(
        new Error(result.transportError?.message || 'Work Cornerstone 操作未确认。'),
        { code: result.transportError?.code },
      );
    }
    if (!result.receipt) {
      return {
        ...result,
        recoverable: true,
        transportError: {
          code: 'contract_receipt_missing',
          message: 'Cornerstone 已提交，但响应缺少 InputIntentReceipt；正在刷新权威状态。',
          operation: 'SetInputCornerstone',
          workId: input.workId,
          requestId: input.requestId,
          committed: true,
          recoverable: true,
        },
      };
    }
    return result;
  };

  previewWorkPatch = async (input: PreviewWorkPatchRequest): Promise<PreviewWorkPatchResult> => {
    if (!this.port.previewWorkPatch) throw new Error('Work 讨论补丁预览能力尚未连接。');
    const result = await this.port.previewWorkPatch(input);
    if (!result.committed) {
      if (result.transportError?.code === 'revision_conflict') {
        await this.recoverSnapshot(input.workId);
      }
      throw Object.assign(
        new Error(result.transportError?.message || 'Work 补丁预览未确认。'),
        { code: result.transportError?.code },
      );
    }
    return result;
  };

  applyWorkPatch = async (input: ApplyWorkPatchRequest): Promise<ApplyWorkPatchResult> => {
    if (!this.port.applyWorkPatch) throw new Error('Work 讨论补丁应用能力尚未连接。');
    let result = await this.port.applyWorkPatch(input);
    if (shouldReplayUnconfirmedPatch(result)) {
      try {
        await this.recoverSnapshot(input.workId);
      } catch {
        // recoverSnapshot keeps the failure observable; the exact request can
        // still be replayed safely because ApplyWorkPatch is request-idempotent.
      }
      result = await this.port.applyWorkPatch(input);
    }
    if (!result.committed) {
      if (result.transportError?.code === 'revision_conflict') {
        await this.recoverSnapshot(input.workId);
      }
      throw Object.assign(
        new Error(result.transportError?.message || 'Work 补丁应用未确认。'),
        { code: result.transportError?.code },
      );
    }
    return result;
  };

  runWork = (input: { workId: string; requestId: string }): Promise<WorkflowRun> => {
    if (!this.port.runWork) return Promise.reject(new Error('Work 运行能力尚未连接。'));
    return this.port.runWork(input);
  };

  resumeRun = (input: ResumeRunInput): Promise<WorkflowRun> => {
    if (!this.port.resumeRun) return Promise.reject(new Error('Work 继续运行能力尚未连接。'));
    return this.port.resumeRun(input);
  };

  requestConversion = (input: RequestArtifactConversionInput): Promise<RequestArtifactConversionResult> => {
    if (!this.port.requestArtifactConversion) return Promise.reject(new Error('Work 转换能力尚未连接。'));
    return this.port.requestArtifactConversion(input);
  };

  updateDraft = async (input: UpdateDraftInput): Promise<WorkView> => {
    if (!this.port.updateDraft) throw new Error('Work 草稿保存能力尚未连接。');
    let view: WorkView;
    try {
      view = await this.port.updateDraft(input);
    } catch (error) {
      const conflict = revisionConflict(error);
      if (!conflict) throw error;
      if (conflict.actualRevision !== undefined) {
        await this.recoverSnapshotToRevision(input.workId, conflict.actualRevision);
      } else {
        await this.recoverSnapshot(input.workId);
      }
      const expectedRevision = useWorkStore.getState().revisions[input.workId];
      if (!Number.isSafeInteger(expectedRevision)) {
        throw new Error(`Work ${input.workId} 权威 revision 未载入，草稿保存未重试。`);
      }
      view = await this.port.updateDraft({ ...input, expectedRevision: expectedRevision! });
    }
    if (!this.applyMutationView(input.workId, view, `draft:${input.requestId}:${view.revision}`)) {
      await this.recoverSnapshot(input.workId);
    }
    return view;
  };

  upsertBlock = async (input: BlockUpsertInput): Promise<WorkView> => {
    if (!this.port.upsertBlock) throw new Error('Work Block 保存能力尚未连接。');
    const view = await this.port.upsertBlock(input);
    if (!this.applyMutationView(input.workId, view, `block:${input.requestId}:${view.revision}`)) {
      await this.recoverSnapshot(input.workId);
    }
    return view;
  };

  clearErrors = (workID: string): void => {
    this.updateStatus(workID, { snapshotError: null, preferenceError: null, eventError: null, unsupportedView: null });
  };

  dispose = (): void => {
    if (this.disposed) return;
    this.disposed = true;
    for (const subscription of this.subscriptions.values()) {
      subscription.port?.unsubscribe();
      subscription.settlement?.reject(new Error('Work controller was disposed before synchronization completed'));
    }
    this.subscriptions.clear();
    this.statusListeners.clear();
    this.planningListeners.clear();
  };

  private applyMutationView(workID: string, view: WorkView, eventID: string): boolean {
    if (view.work.id !== workID) {
      this.updateStatus(workID, { eventError: 'mutation returned a mismatched Work projection' });
      return false;
    }
    const applied = applySnapshot(view, eventID);
    if (applied.kind === 'conflict' || applied.kind === 'gap') {
      // Don't pre-set eventError — the caller will recover and clear or fail.
      return false;
    }
    this.updateStatus(workID, { eventError: null });
    return true;
  }

  private observeUnsupported(
    workID: string,
    schemaVersion: number,
    raw: string,
    source: 'fetch' | 'recover' | 'watch',
  ): ApplyResult {
    const message = `WorkView schema version ${schemaVersion} exceeds current max 2; read-only access is required`;
    this.updateStatus(workID, {
      unsupportedView: { source, schemaVersion, raw },
      ...(source === 'watch' ? { eventError: message } : { snapshotError: message }),
    });
    return { kind: 'unsupported', observed: true, workID, schemaVersion, raw, source };
  }

  /** Apply a cornerstone mutation result to the Work store. On success the
   *  returned WorkView overwrites the projection. On conflict the returned
   *  latestView (if any) is applied so the UI shows fresh state while the draft
   *  is preserved by the caller. On conflict without a latestView the adapter
   *  recovers the snapshot via the port. */
  applyMutationResult = async (workID: string, result: CornerstoneMutationResult): Promise<void> => {
    if (result.ok) {
      if (result.workView) {
        if (!this.applyMutationView(workID, result.workView, `cornerstone:ok:${workID}:${result.workView.revision}`)) {
          await this.recoverSnapshot(workID);
        }
      } else {
        await this.recoverSnapshot(workID);
      }
      return;
    }
    if (result.error.kind === 'revision_conflict' && result.error.latestView) {
      if (!this.applyMutationView(
        workID,
        result.error.latestView,
        `cornerstone:conflict:${workID}:${result.error.latestView.revision}`,
      )) {
        await this.recoverSnapshot(workID);
      }
      return;
    }
    await this.recoverSnapshot(workID);
  };
}

export interface WorkController {
  statuses: Record<string, WorkControllerStatus>;
  subscribe: (workID: string) => void;
  unsubscribe: (workID: string) => void;
  recoverSnapshot: (workID: string) => Promise<ApplyResult>;
  reconcileSnapshot: (workID: string) => Promise<ApplyResult>;
  restoreUIPreference: (workID: string) => Promise<WorkUIPreference | null>;
  setActiveFace: (workID: string, activeFace: WorkFace) => Promise<void>;
  retryTask: (input: RetryTaskInput) => Promise<Attempt>;
  clearErrors: (workID: string) => void;
  resumeRun: (input: ResumeRunInput) => Promise<WorkflowRun>;
}

export function useWorkController(port: WorkControllerPort): WorkController {
  const adapter = useMemo(() => new WorkControllerAdapter(port), [port]);
  const [statuses, setStatuses] = useState<Record<string, WorkControllerStatus>>({});

  useEffect(() => {
    setStatuses(adapter.getStatuses());
    return adapter.subscribeStatus(() => setStatuses(adapter.getStatuses()));
  }, [adapter]);
  useEffect(() => () => adapter.dispose(), [adapter]);

  return {
    statuses,
    subscribe: adapter.subscribe,
    unsubscribe: adapter.unsubscribe,
    recoverSnapshot: adapter.recoverSnapshot,
    reconcileSnapshot: adapter.reconcileSnapshot,
    restoreUIPreference: adapter.restoreUIPreference,
    setActiveFace: adapter.setActiveFace,
    retryTask: adapter.retryTask,
    clearErrors: adapter.clearErrors,
    resumeRun: adapter.resumeRun,
  };
}
