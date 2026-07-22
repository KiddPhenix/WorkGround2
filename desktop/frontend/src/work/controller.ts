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
import type {
  AcceptCornerstoneInput,
  Attempt,
  CornerstoneMutationResult,
  FreezeCornerstoneInput,
  PinCornerstoneInput,
  RefreshCornerstoneInput,
  RemoveCornerstoneInput,
  RepairCornerstoneInput,
  ResumeRunInput,
  RetryTaskInput,
  UndoCornerstoneInput,
  ValidateCornerstoneInput,
  ViewRecoveryIntent,
  WorkView,
  WorkViewEvent,
  WorkflowRun,
} from './types';

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
  fetchSnapshot: (workID: string) => Promise<WorkView>;
  fetchRecoverySnapshot: (workID: string, intent: ViewRecoveryIntent) => Promise<WorkViewEvent>;
  readUIPreference: (workID: string) => Promise<WorkUIPreference | null>;
  writeUIPreference: (workID: string, preference: WorkUIPreference) => Promise<void>;
  retryTask?: (input: RetryTaskInput) => Promise<Attempt>;
  runWork?: (input: { workId: string; requestId: string }) => Promise<WorkflowRun>;
  resumeRun?: (input: ResumeRunInput) => Promise<WorkflowRun>;
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
}

const idleStatus = (): WorkControllerStatus => ({
  fetching: false,
  snapshotError: null,
  preferenceError: null,
  stream: { kind: 'idle' },
  eventError: null,
});

interface WorkSubscriptionState {
  token: symbol;
  generation: number;
  recovery: boolean;
  port: WorkPortSubscription | null;
  watchReady: boolean;
  settling: boolean;
  buffering: boolean;
  events: WorkViewEvent[];
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function isOverflowRecoveryFailure(event: WorkViewEvent): boolean {
  if (event.type !== 'attention' || typeof event.payload !== 'object' || event.payload === null || Array.isArray(event.payload)) return false;
  const payload = event.payload as Record<string, unknown>;
  return payload.overflow === true && payload.recovery === 'failed' && payload.retryable === true;
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
    this.startSubscription(workID, false);
  };

  private startSubscription(workID: string, recovery: boolean): void {
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
      recovery,
      port: null,
      watchReady: false,
      settling: true,
      buffering: true,
      events: [] as WorkViewEvent[],
    };
    const onEvent = (event: WorkViewEvent): void => {
      if (this.subscriptions.get(workID)?.token !== token) return;
      if (event.workID !== workID) {
        this.updateStatus(workID, { eventError: `received event for ${event.workID} on ${workID} subscription` });
        return;
      }
      if (state.buffering) {
        state.events.push(event);
        return;
      }
      const result = this.applyEvent(event);
      if (result.kind === 'gap') void this.recoverSnapshot(workID).catch(() => undefined);
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
        if (state.recovery) await this.recoverSubscriptionSnapshot(workID, state);
        else await this.recoverSnapshot(workID);
        if (!this.isCurrentSubscription(workID, state)) {
          subscription.unsubscribe();
          return;
        }
        const buffered = this.flushBuffered(workID, state);
        if (buffered.retryableFailure) throw new Error('authoritative overflow recovery failed; retry synchronization');
        if (buffered.needsRecovery) await this.recoverSnapshot(workID);
        if (!this.isCurrentSubscription(workID, state)) {
          subscription.unsubscribe();
          return;
        }
        state.settling = false;
        this.updateStatus(workID, {
          stream: { kind: 'online' },
          snapshotError: null,
          eventError: null,
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
    this.startSubscription(workID, true);
  };

  private flushBuffered(workID: string, state: WorkSubscriptionState): { needsRecovery: boolean; retryableFailure: boolean } {
    if (!this.isCurrentSubscription(workID, state)) return { needsRecovery: false, retryableFailure: false };
    let needsRecovery = false;
    let retryableFailure = false;
    while (state.events.length > 0) {
      const pending = state.events.splice(0);
      for (const event of pending) {
        const result = this.applyEvent(event);
        if (result.kind === 'gap') needsRecovery = true;
        if (isOverflowRecoveryFailure(event)) retryableFailure = true;
      }
    }
    state.buffering = false;
    return { needsRecovery, retryableFailure };
  }

  private async recoverSubscriptionSnapshot(workID: string, state: WorkSubscriptionState): Promise<ApplyResult> {
    this.updateStatus(workID, { fetching: true });
    const event = await this.port.fetchRecoverySnapshot(workID, { reason: 'retry', generation: state.generation });
    if (!this.isCurrentSubscription(workID, state)) throw new Error('stale recovery generation');
    if (event.workID !== workID || event.type !== 'snapshot' || event.resync?.reason !== 'retry' ||
        !event.resync.authoritative || event.resync.generation < state.generation) {
      throw new Error('backend returned an invalid authoritative retry snapshot');
    }
    const result = applyWorkViewEvent(event);
    if (result.kind !== 'applied' && result.kind !== 'duplicate') {
      const detail = result.kind === 'conflict' ? result.conflict.reason : result.kind;
      throw new Error(`authoritative retry snapshot was not applied: ${detail}`);
    }
    return result;
  }

  applyEvent = (event: WorkViewEvent): ApplyResult => {
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
  };

  recoverSnapshot = (workID: string): Promise<ApplyResult> => {
    const pending = this.pendingSnapshots.get(workID);
    if (pending) return pending;
    this.updateStatus(workID, { fetching: true });
    const request = Promise.resolve()
      .then(async () => {
        let previousRevision = useWorkStore.getState().revisions[workID] ?? -1;
        for (;;) {
          const view = await this.port.fetchSnapshot(workID);
          if (view.work.id !== workID) throw new Error(`snapshot workID ${view.work.id} does not match ${workID}`);
          const eventGeneration = ++this.snapshotEventGeneration;
          const result = applySnapshot(view, `fetch:${workID}:${view.revision}:${eventGeneration}`);
          if (result.kind === 'conflict') throw new Error(result.conflict.reason);
          const state = useWorkStore.getState();
          const issue = state.gaps[workID];
          if (!issue) {
            this.updateStatus(workID, { snapshotError: null, eventError: null });
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

  restoreUIPreference = async (workID: string): Promise<void> => {
    try {
      const preference = await this.port.readUIPreference(workID);
      if (preference) useWorkUIStore.getState().setActiveFace(workID, preference.activeFace);
      this.updateStatus(workID, { preferenceError: null });
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

  runWork = (input: { workId: string; requestId: string }): Promise<WorkflowRun> => {
    if (!this.port.runWork) return Promise.reject(new Error('Work 运行能力尚未连接。'));
    return this.port.runWork(input);
  };

  resumeRun = (input: ResumeRunInput): Promise<WorkflowRun> => {
    if (!this.port.resumeRun) return Promise.reject(new Error('Work 继续运行能力尚未连接。'));
    return this.port.resumeRun(input);
  };

  clearErrors = (workID: string): void => {
    this.updateStatus(workID, { snapshotError: null, preferenceError: null, eventError: null });
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
      this.updateStatus(workID, { eventError: 'cornerstone mutation returned a mismatched Work projection' });
      return false;
    }
    const applied = applySnapshot(view, eventID);
    if (applied.kind === 'conflict' || applied.kind === 'gap') {
      this.updateStatus(workID, { eventError: 'cornerstone mutation returned an invalid Work projection' });
      return false;
    }
    this.updateStatus(workID, { eventError: null });
    return true;
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
          await this.recoverSnapshot(workID).catch(() => undefined);
        }
      } else {
        await this.recoverSnapshot(workID).catch(() => undefined);
      }
      return;
    }
    if (result.error.kind === 'revision_conflict' && result.error.latestView) {
      if (!this.applyMutationView(
        workID,
        result.error.latestView,
        `cornerstone:conflict:${workID}:${result.error.latestView.revision}`,
      )) {
        await this.recoverSnapshot(workID).catch(() => undefined);
      }
      return;
    }
    await this.recoverSnapshot(workID).catch(() => undefined);
  };
}

export interface WorkController {
  statuses: Record<string, WorkControllerStatus>;
  subscribe: (workID: string) => void;
  unsubscribe: (workID: string) => void;
  recoverSnapshot: (workID: string) => Promise<ApplyResult>;
  restoreUIPreference: (workID: string) => Promise<void>;
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
