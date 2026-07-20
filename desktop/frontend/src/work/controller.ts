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
import type { WorkView, WorkViewEvent } from './types';

export interface WorkControllerPort {
  subscribe: (workID: string, onEvent: (event: WorkViewEvent) => void) => () => void;
  fetchSnapshot: (workID: string) => Promise<WorkView>;
  readUIPreference: (workID: string) => Promise<WorkUIPreference | null>;
  writeUIPreference: (workID: string, preference: WorkUIPreference) => Promise<void>;
}

export interface WorkControllerStatus {
  fetching: boolean;
  snapshotError: string | null;
  preferenceError: string | null;
  eventError: string | null;
}

const idleStatus = (): WorkControllerStatus => ({
  fetching: false,
  snapshotError: null,
  preferenceError: null,
  eventError: null,
});

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export class WorkControllerAdapter {
  private readonly subscriptions = new Map<string, () => void>();
  private readonly pendingSnapshots = new Map<string, Promise<ApplyResult>>();
  private readonly statusByWork: Record<string, WorkControllerStatus> = {};
  private readonly statusListeners = new Set<() => void>();
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
    if (this.disposed || this.subscriptions.has(workID)) return;
    const unsubscribe = this.port.subscribe(workID, (event) => {
      if (event.workID !== workID) {
        this.updateStatus(workID, { eventError: `received event for ${event.workID} on ${workID} subscription` });
        return;
      }
      const result = this.applyEvent(event);
      if (result.kind === 'gap') void this.recoverSnapshot(workID).catch(() => undefined);
    });
    this.subscriptions.set(workID, unsubscribe);
  };

  unsubscribe = (workID: string): void => {
    this.subscriptions.get(workID)?.();
    this.subscriptions.delete(workID);
  };

  applyEvent = (event: WorkViewEvent): ApplyResult => {
    const result = applyWorkViewEvent(event);
    if (result.kind === 'conflict') this.updateStatus(event.workID, { eventError: result.conflict.reason });
    else if (result.kind === 'applied') this.updateStatus(event.workID, { eventError: null });
    return result;
  };

  recoverSnapshot = (workID: string): Promise<ApplyResult> => {
    const pending = this.pendingSnapshots.get(workID);
    if (pending) return pending;
    this.updateStatus(workID, { fetching: true, snapshotError: null });
    const request = Promise.resolve()
      .then(async () => {
        let previousRevision = useWorkStore.getState().revisions[workID] ?? -1;
        for (;;) {
          const view = await this.port.fetchSnapshot(workID);
          if (view.work.id !== workID) throw new Error(`snapshot workID ${view.work.id} does not match ${workID}`);
          const result = applySnapshot(view, `fetch:${workID}:${view.revision}`);
          if (result.kind === 'conflict') throw new Error(result.conflict.reason);
          const state = useWorkStore.getState();
          const issue = state.gaps[workID];
          if (!issue) {
            this.updateStatus(workID, { snapshotError: null });
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

  clearErrors = (workID: string): void => {
    this.updateStatus(workID, { snapshotError: null, preferenceError: null, eventError: null });
  };

  dispose = (): void => {
    if (this.disposed) return;
    this.disposed = true;
    for (const unsubscribe of this.subscriptions.values()) unsubscribe();
    this.subscriptions.clear();
    this.statusListeners.clear();
  };
}

export interface WorkController {
  statuses: Record<string, WorkControllerStatus>;
  subscribe: (workID: string) => void;
  unsubscribe: (workID: string) => void;
  recoverSnapshot: (workID: string) => Promise<ApplyResult>;
  restoreUIPreference: (workID: string) => Promise<void>;
  setActiveFace: (workID: string, activeFace: WorkFace) => Promise<void>;
  clearErrors: (workID: string) => void;
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
    clearErrors: adapter.clearErrors,
  };
}
