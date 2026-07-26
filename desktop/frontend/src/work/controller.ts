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
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
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
  return result.kind === 'gap' || (result.kind === 'conflict' && isV2ProjectionEvent(event));
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

export class WorkControllerAdapter {
  private readonly subscriptions = new Map<string, WorkSubscriptionState>();
  private readonly pendingSnapshots = new Map<string, Promise<ApplyResult>>();
  private readonly pendingRetries = new Map<string, Promise<Attempt>>();
  private readonly statusByWork: Record<string, WorkControllerStatus> = {};
  private readonly statusListeners = new Set<() => void>();
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

  private startSubscription(workID: string, reason: ViewRecoveryIntent['reason'] | null): void {
    if (this.disposed || this.subscriptions.has(workID)) return;
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
      recoveryEventIDs: new Set(),
    };
    const onEvent = (event: WorkViewEvent): void => {
      if (this.subscriptions.get(workID)?.token !== token) return;
      const parsed = this.parseEvent(workID, event);
      if (!parsed) return;
      if (parsed.workID !== workID) {
        this.updateStatus(workID, { eventError: `received event for ${parsed.workID} on ${workID} subscription` });
        return;
      }
      if (state.buffering) {
        state.events.push(parsed);
        return;
      }
      const result = this.applyParsedEvent(parsed);
      if (claimProjectionRecovery(state, parsed, result)) {
        void this.recoverSnapshot(workID).catch(() => {
          // recoverSnapshot already set snapshotError; ensure stream reflects the failure.
          this.updateStatus(workID, {
            stream: { kind: 'offline', message: this.getStatus(workID).snapshotError ?? 'watch-event recovery failed; retry subscription' },
          });
        });
      }
    };
    let subscription: WorkPortSubscription;
    try {
      subscription = this.port.subscribe(workID, onEvent);
      state.port = subscription;
    } catch (error) {
      this.updateStatus(workID, { stream: { kind: 'offline', message: errorText(error) } });
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
        let observedUnsupported = snapshotResult.kind === 'unsupported';
        if (!this.isCurrentSubscription(workID, state)) {
          subscription.unsubscribe();
          return;
        }
        const buffered = this.flushBuffered(workID, state);
        if (buffered.retryableFailure) throw new Error('authoritative overflow recovery failed; retry synchronization');
        if (buffered.needsRecovery) {
          const recovery = await this.recoverSnapshot(workID);
          observedUnsupported ||= recovery.kind === 'unsupported';
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
          return;
        }
        this.updateStatus(workID, {
          stream: { kind: 'online' },
          snapshotError: null,
          eventError: null,
          unsupportedView: null,
          fetching: false,
        });
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
      });
  }

  private isCurrentSubscription(workID: string, state: WorkSubscriptionState): boolean {
    return !this.disposed && this.subscriptions.get(workID)?.token === state.token;
  }

  unsubscribe = (workID: string): void => {
    this.subscriptions.get(workID)?.port?.unsubscribe();
    this.subscriptions.delete(workID);
  };

  retrySubscription = (workID: string): void => {
    if (this.disposed) return;
    const state = this.subscriptions.get(workID);
    // One retry owns the complete Watch -> authoritative snapshot handshake.
    // Repeated clicks while either half is still settling must not create a
    // competing generation or let a late response overwrite newer state.
    if (state && (!state.watchReady || state.settling)) return;
    state?.port?.unsubscribe();
    this.subscriptions.delete(workID);
    this.startSubscription(workID, 'retry');
  };

  private flushBuffered(workID: string, state: WorkSubscriptionState): { needsRecovery: boolean; retryableFailure: boolean } {
    if (!this.isCurrentSubscription(workID, state)) return { needsRecovery: false, retryableFailure: false };
    let needsRecovery = false;
    let retryableFailure = false;
    while (state.events.length > 0) {
      const pending = state.events.splice(0);
      for (const event of pending) {
        const result = this.applyEvent(event);
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

  private applyParsedEvent(event: WorkViewEvent): ApplyResult {
    const result = applyWorkViewEvent(event);
    if (isOverflowRecoveryFailure(event)) {
      this.updateStatus(event.workID, {
        stream: { kind: 'offline', message: 'authoritative overflow recovery failed; retry synchronization' },
        snapshotError: 'authoritative overflow recovery failed; retry synchronization',
      });
    } else if (result.kind === 'conflict') {
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
          if (result.kind === 'conflict') throw new Error(result.conflict.reason);
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
    const message = `候选已提交 (revision ${minimumRevision})，但权威状态刷新 ${MAX_SNAPSHOT_RECOVERY_FETCHES + 1} 次后仍停留在 revision ${currentRevision}，请重试。`;
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
    const result = await this.port.createCandidateRevision(input);
    if (result.committed) {
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
    if (!result.committed) {
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
    const result = await this.port.submitWorkInput(input);
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
    const result = await this.port.applyWorkPatch(input);
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
    const view = await this.port.updateDraft(input);
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
    for (const subscription of this.subscriptions.values()) subscription.port?.unsubscribe();
    this.subscriptions.clear();
    this.statusListeners.clear();
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
    restoreUIPreference: adapter.restoreUIPreference,
    setActiveFace: adapter.setActiveFace,
    retryTask: adapter.retryTask,
    clearErrors: adapter.clearErrors,
    resumeRun: adapter.resumeRun,
  };
}
