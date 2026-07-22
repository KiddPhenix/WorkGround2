import { create } from 'zustand';

import type {
  Attempt,
  BlockInstance,
  Cornerstone,
  Conclusion,
  RetryIntent,
  RetryStatus,
  RunCancelReceipt,
  RunSelection,
  Stage,
  Task,
  Work,
  WorkState,
  WorkflowRun,
  WorkView,
  WorkViewEvent,
} from './types';

const MAX_SEEN_EVENTS = 256;
const WORK_STATES = new Set<WorkState>([
  'draft', 'ready', 'running', 'waiting_user', 'paused', 'completed', 'failed', 'cancelled',
]);
const WORK_TERMINAL = new Set<WorkState>(['completed', 'failed', 'cancelled']);
const RUN_STATES = new Set<WorkflowRun['state']>(['pending', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'needs_confirmation']);
const RUN_TERMINAL = new Set<WorkflowRun['state']>(['completed', 'failed', 'cancelled']);
const WORK_TRANSITIONS: Record<WorkState, ReadonlySet<WorkState>> = {
  draft: new Set(['ready', 'running']),
  ready: new Set(['draft', 'running']),
  running: new Set(['waiting_user', 'paused', 'completed', 'failed', 'cancelled']),
  waiting_user: new Set(['running']),
  paused: new Set(['running']),
  completed: new Set(['running']),
  failed: new Set(['running', 'draft']),
  cancelled: new Set(['draft']),
};

export type GapReason = 'missing_projection' | 'base_revision_mismatch';

export interface WorkGap {
  workID: string;
  eventID: string;
  currentRevision: number;
  eventRevision: number;
  baseRevision: number;
  reason: GapReason;
}

export interface WorkConflict {
  workID: string;
  eventID: string;
  revision: number;
  reason: string;
}

export type ApplyResult =
  | { kind: 'applied'; workID: string; revision: number }
  | { kind: 'duplicate'; workID: string; eventID: string }
  | { kind: 'stale'; workID: string; eventID: string; currentRevision: number; eventRevision: number }
  | { kind: 'gap'; gap: WorkGap }
  | { kind: 'conflict'; conflict: WorkConflict }
  | { kind: 'ignored'; workID: string; eventID: string };

export interface BlockDeltaItem extends Partial<Omit<BlockInstance, 'id' | 'revision'>> {
  id: string;
  revision: number;
}

export interface WorkDeltaPayload {
  state?: WorkState;
  archiveState?: Work['archiveState'];
  name?: string;
  prompt?: string;
  inputs?: Record<string, unknown>;
  blocks?: BlockDeltaItem[];
  removedBlockIds?: string[];
  runs?: WorkflowRun[];
  cornerstones?: Cornerstone[];
  conclusions?: Conclusion[];
}

interface WorkStoreData {
  works: Record<string, WorkView>;
  revisions: Record<string, number>;
  removed: Record<string, boolean>;
  seenEventIDs: Record<string, readonly string[]>;
  resyncGenerations: Record<string, number>;
  gaps: Record<string, WorkGap | undefined>;
  conflicts: Record<string, WorkConflict | undefined>;
}

export interface WorkStoreState extends WorkStoreData {
  applyEvent: (event: WorkViewEvent) => ApplyResult;
  applySnapshot: (view: WorkView, eventID?: string) => ApplyResult;
  removeProjection: (workID: string, revision?: number) => void;
  forgetWork: (workID: string) => void;
  clearAll: () => void;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function sameValue(left: unknown, right: unknown): boolean {
  return JSON.stringify(left) === JSON.stringify(right);
}

function cloneView(view: WorkView): WorkView {
  return structuredClone(view);
}

function rememberEvent(
  seen: Record<string, readonly string[]>,
  workID: string,
  eventID: string,
): Record<string, readonly string[]> {
  const current = seen[workID] ?? [];
  if (current.includes(eventID)) return seen;
  return {
    ...seen,
    [workID]: [...current, eventID].slice(-MAX_SEEN_EVENTS),
  };
}

function clearIssue<T>(values: Record<string, T | undefined>, workID: string): Record<string, T | undefined> {
  if (!(workID in values)) return values;
  const { [workID]: _, ...rest } = values;
  return rest;
}

function conflict(event: WorkViewEvent, reason: string): WorkConflict {
  return { workID: event.workID, eventID: event.eventID, revision: event.revision, reason };
}

function onlyIODerivedDiffers(current: WorkView, incoming: WorkView): boolean {
  const { assessment: _a1, runBlock: _r1, ...currentRest } = current;
  const { assessment: _a2, runBlock: _r2, ...incomingRest } = incoming;
  return sameValue(currentRest, incomingRest);
}

function resyncEventID(workID: string, revision: number, reason: string, generation: number): string {
  return `wv-resync-${workID}-rev-${revision}-${reason}-${generation}`;
}

function authoritativeResyncGeneration(event: WorkViewEvent): number | null {
  const resync = event.resync as unknown;
  if (!isRecord(resync) || event.type !== 'snapshot' || (resync.reason !== 'overflow' && resync.reason !== 'retry' && resync.reason !== 'hydrate') || resync.authoritative !== true ||
      !Number.isSafeInteger(resync.generation) || Number(resync.generation) <= 0) return null;
  const generation = Number(resync.generation);
  return event.eventID === resyncEventID(event.workID, event.revision, resync.reason, generation) ? generation : null;
}

function gap(event: WorkViewEvent, currentRevision: number, reason: GapReason): WorkGap {
  return {
    workID: event.workID,
    eventID: event.eventID,
    currentRevision,
    eventRevision: event.revision,
    baseRevision: event.baseRevision,
    reason,
  };
}

function retainHighestGap(current: WorkGap | undefined, incoming: WorkGap): WorkGap {
  if (!current || incoming.eventRevision > current.eventRevision) return incoming;
  return { ...current, currentRevision: Math.max(current.currentRevision, incoming.currentRevision) };
}

function gapsWith(
  gaps: Record<string, WorkGap | undefined>,
  event: WorkViewEvent,
  currentRevision: number,
  reason: GapReason,
): { gaps: Record<string, WorkGap | undefined>; gap: WorkGap } {
  const issue = retainHighestGap(gaps[event.workID], gap(event, currentRevision, reason));
  return { gaps: { ...gaps, [event.workID]: issue }, gap: issue };
}

function gapsAfterRevision(
  gaps: Record<string, WorkGap | undefined>,
  workID: string,
  revision: number,
): Record<string, WorkGap | undefined> {
  const current = gaps[workID];
  if (!current || revision >= current.eventRevision) return clearIssue(gaps, workID);
  return { ...gaps, [workID]: { ...current, currentRevision: revision } };
}

function validID(value: unknown): boolean {
  return value === undefined || (typeof value === 'string' && value.length > 0);
}

function validSessionRef(value: unknown): boolean {
  return isRecord(value) &&
    typeof value.sessionPath === 'string' && typeof value.branchId === 'string' &&
    typeof value.modelRef === 'string' && Number.isSafeInteger(value.turnCount) && Number(value.turnCount) >= 0 &&
    typeof value.preview === 'string' && typeof value.startedAt === 'string';
}

function validAttemptReceipt(value: unknown): boolean {
  // A missing/mismatched receipt ID is itself evidence for
  // needs_confirmation; keep the receipt visible instead of rejecting the
  // whole projection.
  return isRecord(value) && typeof value.requestId === 'string' &&
    typeof value.outcome === 'string' && typeof value.confirmedAt === 'string' &&
    (value.evidence === undefined || typeof value.evidence === 'string') &&
    (value.sideEffectClass === undefined || typeof value.sideEffectClass === 'string');
}

function validGateResolution(value: unknown): boolean {
  return isRecord(value) && typeof value.stageId === 'string' &&
    (value.outcome === 'approved' || value.outcome === 'input_provided') &&
    (value.input === undefined || isRecord(value.input)) &&
    (value.note === undefined || typeof value.note === 'string');
}

function validCancelReceipt(value: unknown): boolean {
  return isRecord(value) && typeof value.requestId === 'string' && value.requestId.length > 0 &&
    (value.status === 'pending' || value.status === 'delivered' || value.status === 'failed') &&
    Number.isSafeInteger(value.attempts) && Number(value.attempts) >= 0 && typeof value.updatedAt === 'string' &&
    (value.error === undefined || typeof value.error === 'string');
}

function validPauseReceipt(value: unknown): boolean {
  return isRecord(value) && typeof value.requestId === 'string' && value.requestId.length > 0 &&
    typeof value.pausedAt === 'string' && typeof value.notice === 'string';
}

function validAttempt(value: unknown): value is Attempt {
  return isRecord(value) &&
    validID(value.id) &&
    validID(value.requestId) &&
    Number.isSafeInteger(value.index) && Number(value.index) >= 0 &&
    RUN_STATES.has(value.state as WorkflowRun['state']) &&
    validSessionRef(value.sessionRef) && typeof value.startedAt === 'string' &&
    (value.receipt === undefined || validAttemptReceipt(value.receipt)) &&
    (value.sideEffectClass === undefined || typeof value.sideEffectClass === 'string');
}

function validTask(value: unknown): value is Task {
  return isRecord(value) &&
    validID(value.id) && typeof value.name === 'string' && value.name.length > 0 &&
    RUN_STATES.has(value.state as WorkflowRun['state']) &&
    Array.isArray(value.attempts) && value.attempts.every(validAttempt);
}

function validStage(value: unknown): value is Stage {
  return isRecord(value) &&
    validID(value.id) && typeof value.name === 'string' && value.name.length > 0 &&
    RUN_STATES.has(value.state as WorkflowRun['state']) &&
    Array.isArray(value.tasks) && value.tasks.every(validTask) &&
    typeof value.startedAt === 'string' &&
    (value.gate === undefined || typeof value.gate === 'string') &&
    (value.resolution === undefined || validGateResolution(value.resolution));
}

function validRun(run: unknown, workID: string): run is WorkflowRun {
  return isRecord(run) &&
    typeof run.id === 'string' && run.id.length > 0 &&
    run.workId === workID &&
    RUN_STATES.has(run.state as WorkflowRun['state']) &&
    Array.isArray(run.stages) && run.stages.every(validStage) &&
    validID(run.requestId) &&
    (run.cancel === undefined || validCancelReceipt(run.cancel)) &&
    (run.pause === undefined || validPauseReceipt(run.pause));
}

function validCornerstoneOwner(cornerstone: unknown, workID: string): cornerstone is Cornerstone {
  return isRecord(cornerstone) && typeof cornerstone.id === 'string' && cornerstone.workId === workID;
}

function objectContextConflict(event: WorkViewEvent): string | null {
  if (event.object.kind === 'work') {
    return event.object.id === event.workID
      ? null
      : `work object ${event.object.id} does not belong to ${event.workID}`;
  }
  return event.object.parentID === event.workID
    ? null
    : `${event.object.kind} object ${event.object.id} does not belong to ${event.workID}`;
}

function snapshotPayload(event: WorkViewEvent): WorkView | null {
  if (!isRecord(event.payload)) return null;
  if (isRecord(event.payload.work) && Number.isSafeInteger(event.payload.revision)) {
    const view = event.payload as unknown as WorkView;
    if (!validSnapshotWork(view.work, event.workID) || view.revision !== event.revision) return null;
    return view;
  }
  const work = event.payload as unknown as Work;
  if (!validSnapshotWork(work, event.workID)) return null;
  return {
    schemaVersion: work.schemaVersion,
    work,
    revision: event.revision,
    assessment: { state: 'ready', blocking: false, degraded: false, issues: [] },
  };
}

function validSnapshotWork(work: Work, workID: string): boolean {
  return isRecord(work) &&
    work.id === workID &&
    Number.isSafeInteger(work.schemaVersion) &&
    WORK_STATES.has(work.state) &&
    Array.isArray(work.blocks) &&
    Array.isArray(work.placements) &&
    Array.isArray(work.cornerstones) &&
    work.cornerstones.every((cornerstone) => validCornerstoneOwner(cornerstone, workID)) &&
    Array.isArray(work.runs) &&
    work.runs.every((run) => validRun(run, workID));
}

function patchBlock(current: BlockInstance | undefined, delta: BlockDeltaItem, createdAt: string): BlockInstance {
  return {
    id: delta.id,
    kind: delta.kind ?? current?.kind ?? 'unknown',
    schemaVersion: delta.schemaVersion ?? current?.schemaVersion ?? 1,
    revision: delta.revision,
    title: delta.title ?? current?.title,
    status: delta.status ?? current?.status ?? 'loading',
    data: 'data' in delta ? delta.data : current?.data,
    actions: delta.actions ?? current?.actions,
    source: delta.source ?? current?.source ?? { provider: 'controller', mode: 'snapshot', verified: false },
    freshness: 'freshness' in delta ? delta.freshness : current?.freshness,
    fallback: delta.fallback ?? current?.fallback ?? { summary: '' },
    tombstone: delta.tombstone ?? current?.tombstone,
    createdAt: delta.createdAt ?? current?.createdAt ?? createdAt,
    updatedAt: delta.updatedAt ?? current?.updatedAt ?? createdAt,
  };
}

function mergeDeltaBlocks(
  current: BlockInstance[],
  deltas: BlockDeltaItem[],
  removedIDs: string[],
  createdAt: string,
): { blocks?: BlockInstance[]; error?: string } {
  const blocks = new Map(current.map((block) => [block.id, block]));
  for (const delta of deltas) {
    if (!delta.id || !Number.isSafeInteger(delta.revision) || delta.revision < 0) {
      return { error: 'delta contains an invalid block id or revision' };
    }
    const existing = blocks.get(delta.id);
    if (existing && delta.revision < existing.revision) continue;
    const next = patchBlock(existing, delta, createdAt);
    if (existing && delta.revision === existing.revision) {
      if (!sameValue(existing, next)) return { error: `block ${delta.id} conflicts at revision ${delta.revision}` };
      continue;
    }
    blocks.set(delta.id, next);
  }
  for (const id of removedIDs) {
    const existing = blocks.get(id);
    if (!existing || existing.tombstone) continue;
    blocks.set(id, { ...existing, revision: existing.revision + 1, tombstone: true, updatedAt: createdAt });
  }
  return { blocks: [...blocks.values()] };
}

function mergeSnapshotBlocks(
  current: BlockInstance[],
  incoming: BlockInstance[],
): { blocks?: BlockInstance[]; error?: string } {
  const previous = new Map(current.map((block) => [block.id, block]));
  const blocks = new Map<string, BlockInstance>();
  for (const next of incoming) {
    const existing = previous.get(next.id);
    if (!existing || next.revision > existing.revision) {
      blocks.set(next.id, next);
      continue;
    }
    if (next.revision < existing.revision) {
      blocks.set(next.id, existing);
      continue;
    }
    if (!sameValue(existing, next)) return { error: `snapshot block ${next.id} conflicts at revision ${next.revision}` };
    blocks.set(next.id, next);
  }
  for (const existing of current) {
    if (existing.tombstone && !blocks.has(existing.id)) blocks.set(existing.id, existing);
  }
  return { blocks: [...blocks.values()] };
}

function mergeRun(current: WorkflowRun | undefined, incoming: WorkflowRun): WorkflowRun {
  const next = structuredClone(incoming);
  if (!current) return next;

  // A failed path may reopen only when the newer projection reserves a new
  // Attempt. This matches RetryTask while still rejecting late "running"
  // payloads for completed/cancelled Runs or the same failed Attempt.
  const retryReopen = current.state === 'failed' && incoming.state === 'running' && runHasNewAttempt(current, incoming);
  if (RUN_TERMINAL.has(current.state) && !retryReopen) {
    next.state = current.state;
    next.finishedAt = current.finishedAt ?? next.finishedAt;
  }
  next.requestId = next.requestId ?? current.requestId;
  next.conclusion = next.conclusion ?? current.conclusion;
  next.cancel = mergeCancelReceipt(current.cancel, next.cancel);
  next.pause = next.pause ?? current.pause;

  next.stages = mergeStages(current.stages, next.stages);

  return next;
}

const CANCEL_STATUS_RANK: Record<RunCancelReceipt['status'], number> = {
  pending: 0,
  failed: 1,
  delivered: 2,
};

function mergeCancelReceipt(current: RunCancelReceipt | undefined, incoming: RunCancelReceipt | undefined): RunCancelReceipt | undefined {
  if (!current) return incoming ? structuredClone(incoming) : undefined;
  if (!incoming || current.requestId !== incoming.requestId) return structuredClone(current);
  if (incoming.attempts < current.attempts) return structuredClone(current);
  if (incoming.attempts === current.attempts && CANCEL_STATUS_RANK[incoming.status] < CANCEL_STATUS_RANK[current.status]) {
    return structuredClone(current);
  }
  return structuredClone(incoming);
}

function sameStage(left: Stage, right: Stage): boolean {
  if (left.id && right.id) return left.id === right.id;
  return left.name === right.name;
}

function sameTask(left: Task, right: Task): boolean {
  if (left.id && right.id) return left.id === right.id;
  return left.name === right.name;
}

function sameAttempt(left: Attempt, right: Attempt): boolean {
  if (left.id && right.id) return left.id === right.id;
  return left.index === right.index;
}

function taskHasNewAttempt(current: Task, incoming: Task): boolean {
  return incoming.attempts.some((attempt) => !current.attempts.some((existing) => sameAttempt(existing, attempt)));
}

function stageHasNewAttempt(current: Stage, incoming: Stage): boolean {
  return incoming.tasks.some((task) => {
    const previous = current.tasks.find((existing) => sameTask(existing, task));
    return previous ? taskHasNewAttempt(previous, task) : false;
  });
}

function runHasNewAttempt(current: WorkflowRun, incoming: WorkflowRun): boolean {
  return incoming.stages.some((stage) => {
    const previous = current.stages.find((existing) => sameStage(existing, stage));
    return previous ? stageHasNewAttempt(previous, stage) : false;
  });
}

function mergeOrdered<T>(current: T[], incoming: T[], same: (left: T, right: T) => boolean, merge: (left: T, right: T) => T): T[] {
  const result = current.map((value) => structuredClone(value));
  for (const value of incoming) {
    const index = result.findIndex((existing) => same(existing, value));
    if (index < 0) result.push(structuredClone(value));
    else result[index] = merge(result[index], value);
  }
  return result;
}

function mergeStages(current: Stage[], incoming: Stage[]): Stage[] {
  return mergeOrdered(current, incoming, sameStage, (previous, value) => {
    const next = structuredClone(value);
    const retryReopen = previous.state === 'failed' && value.state === 'running' && stageHasNewAttempt(previous, value);
    if (RUN_TERMINAL.has(previous.state) && !retryReopen) {
      next.state = previous.state;
      next.finishedAt = previous.finishedAt ?? next.finishedAt;
    }
    next.gate = next.gate ?? previous.gate;
    next.resolution = next.resolution ?? previous.resolution;
    next.tasks = mergeTasks(previous.tasks, next.tasks);
    return next;
  });
}

function mergeTasks(current: Task[], incoming: Task[]): Task[] {
  return mergeOrdered(current, incoming, sameTask, (previous, value) => {
    const next = structuredClone(value);
    const retryReopen = previous.state === 'failed' && value.state === 'running' && taskHasNewAttempt(previous, value);
    if (RUN_TERMINAL.has(previous.state) && !retryReopen) {
      next.state = previous.state;
      next.finishedAt = previous.finishedAt ?? next.finishedAt;
    }
    next.startedAt = next.startedAt ?? previous.startedAt;
    next.attempts = mergeAttempts(previous.attempts, next.attempts);
    return next;
  });
}

function mergeAttempts(current: Attempt[], incoming: Attempt[]): Attempt[] {
  return mergeOrdered(current, incoming, sameAttempt, (previous, value) => {
    const next = structuredClone(value);
    if (RUN_TERMINAL.has(previous.state)) {
      return structuredClone(previous);
    }
    next.requestId = next.requestId ?? previous.requestId;
    next.receipt = next.receipt ?? previous.receipt;
    next.sideEffectClass = next.sideEffectClass ?? previous.sideEffectClass;
    return next;
  }).sort((left, right) => left.index - right.index);
}

function mergeSnapshotRuns(current: WorkflowRun[], incoming: WorkflowRun[]): WorkflowRun[] {
  return mergeDeltaRuns(current, incoming);
}

function mergeDeltaRuns(current: WorkflowRun[], incoming: WorkflowRun[]): WorkflowRun[] {
  const runs = new Map(current.map((run) => [run.id, structuredClone(run)]));
  for (const run of incoming) runs.set(run.id, mergeRun(runs.get(run.id), run));
  return [...runs.values()];
}

function nextWorkState(
  currentState: WorkState,
  requestedState: WorkState | undefined,
  currentRuns: WorkflowRun[],
  nextRuns: WorkflowRun[],
): WorkState {
  if (requestedState === undefined) return currentState;
  if (requestedState === currentState) return currentState;
  if (!WORK_TRANSITIONS[currentState].has(requestedState)) return currentState;
  const currentRunIDs = new Set(currentRuns.map((run) => run.id));
  const hasNewRunningRun = nextRuns.some((run) => run.state === 'running' && !currentRunIDs.has(run.id));
  const hasRetriedRun = nextRuns.some((run) => {
    const previous = currentRuns.find((candidate) => candidate.id === run.id);
    return previous?.state === 'failed' && run.state === 'running' && runHasNewAttempt(previous, run);
  });
  const hasRunningRun = nextRuns.some((run) => run.state === 'running');
  if (WORK_TERMINAL.has(currentState)) {
    if (requestedState !== 'running' || hasNewRunningRun || (currentState === 'failed' && hasRetriedRun)) return requestedState;
    return currentState;
  }
  if (WORK_TERMINAL.has(requestedState) && hasRunningRun) return currentState;
  return requestedState;
}

function applySnapshotEvent(state: WorkStoreData, event: WorkViewEvent): { patch?: Partial<WorkStoreData>; result: ApplyResult } {
  const knownRevision = state.revisions[event.workID];
  if (state.seenEventIDs[event.workID]?.includes(event.eventID)) {
    return { result: { kind: 'duplicate', workID: event.workID, eventID: event.eventID } };
  }
  if (knownRevision !== undefined && event.revision < knownRevision) {
    return { result: { kind: 'stale', workID: event.workID, eventID: event.eventID, currentRevision: knownRevision, eventRevision: event.revision } };
  }
  const resyncGeneration = event.resync === undefined ? null : authoritativeResyncGeneration(event);
  if (event.resync !== undefined && resyncGeneration === null) {
    const issue = conflict(event, 'snapshot contains an invalid authoritative resync marker');
    return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
  }
  const decoded = snapshotPayload(event);
  if (!decoded) {
    const issue = conflict(event, 'snapshot payload must be a matching Work or WorkView at the event revision');
    return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
  }
  const current = state.works[event.workID];
  if (knownRevision !== undefined && event.revision === knownRevision) {
    if (resyncGeneration !== null) {
      const currentGeneration = state.resyncGenerations[event.workID] ?? 0;
      if (resyncGeneration <= currentGeneration) {
        return {
          patch: { seenEventIDs: rememberEvent(state.seenEventIDs, event.workID, event.eventID) },
          result: { kind: 'ignored', workID: event.workID, eventID: event.eventID },
        };
      }
      if (current && sameValue(current, decoded)) {
        return {
          patch: {
            seenEventIDs: rememberEvent(state.seenEventIDs, event.workID, event.eventID),
            resyncGenerations: { ...state.resyncGenerations, [event.workID]: resyncGeneration },
          },
          result: { kind: 'duplicate', workID: event.workID, eventID: event.eventID },
        };
      }
      // hydrate: narrow acceptance — only I/O-derived assessment/runBlock may
      // differ at the same revision. Any content change (blocks, cornerstones,
      // runs, name, prompt, etc.) is still a conflict.
      if (event.resync?.reason === 'hydrate') {
        if (current && onlyIODerivedDiffers(current, decoded)) {
          const view = cloneView(current);
          view.assessment = structuredClone(decoded.assessment);
          view.runBlock = decoded.runBlock ? structuredClone(decoded.runBlock) : undefined;
          return {
            patch: {
              works: { ...state.works, [event.workID]: view },
              seenEventIDs: rememberEvent(state.seenEventIDs, event.workID, event.eventID),
              resyncGenerations: { ...state.resyncGenerations, [event.workID]: resyncGeneration },
              gaps: gapsAfterRevision(state.gaps, event.workID, event.revision),
              conflicts: clearIssue(state.conflicts, event.workID),
            },
            result: { kind: 'applied', workID: event.workID, revision: event.revision },
          };
        }
        const issue = conflict(event, 'hydrate snapshot conflicts with current content at the same revision');
        return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
      }
      // retry / overflow: full authoritative overwrite.
      const view = cloneView(decoded);
      return {
        patch: {
          works: { ...state.works, [event.workID]: view },
          revisions: { ...state.revisions, [event.workID]: event.revision },
          removed: { ...state.removed, [event.workID]: false },
          seenEventIDs: rememberEvent(state.seenEventIDs, event.workID, event.eventID),
          resyncGenerations: { ...state.resyncGenerations, [event.workID]: resyncGeneration },
          gaps: gapsAfterRevision(state.gaps, event.workID, event.revision),
          conflicts: clearIssue(state.conflicts, event.workID),
        },
        result: { kind: 'applied', workID: event.workID, revision: event.revision },
      };
    }
    if (current && sameValue(current, decoded)) {
      return { result: { kind: 'duplicate', workID: event.workID, eventID: event.eventID } };
    }
    const issue = conflict(event, 'different snapshot at the current revision');
    return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
  }

  const view = cloneView(decoded);
  if (current) {
    const blocks = mergeSnapshotBlocks(current.work.blocks, view.work.blocks);
    if (blocks.error) {
      const issue = conflict(event, blocks.error);
      return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
    }
    view.work.blocks = blocks.blocks ?? [];
    view.work.runs = mergeSnapshotRuns(current.work.runs, view.work.runs);
    view.work.state = nextWorkState(current.work.state, view.work.state, current.work.runs, view.work.runs);
  }
  view.revision = event.revision;
  return {
    patch: {
      works: { ...state.works, [event.workID]: view },
      revisions: { ...state.revisions, [event.workID]: event.revision },
      removed: { ...state.removed, [event.workID]: false },
      seenEventIDs: rememberEvent(state.seenEventIDs, event.workID, event.eventID),
      resyncGenerations: resyncGeneration === null
        ? state.resyncGenerations
        : { ...state.resyncGenerations, [event.workID]: Math.max(state.resyncGenerations[event.workID] ?? 0, resyncGeneration) },
      gaps: gapsAfterRevision(state.gaps, event.workID, event.revision),
      conflicts: clearIssue(state.conflicts, event.workID),
    },
    result: { kind: 'applied', workID: event.workID, revision: event.revision },
  };
}

function applyDeltaEvent(state: WorkStoreData, event: WorkViewEvent): { patch?: Partial<WorkStoreData>; result: ApplyResult } {
  const knownRevision = state.revisions[event.workID];
  if (state.seenEventIDs[event.workID]?.includes(event.eventID)) {
    return { result: { kind: 'duplicate', workID: event.workID, eventID: event.eventID } };
  }
  if (knownRevision !== undefined && event.revision <= knownRevision) {
    return { result: { kind: 'stale', workID: event.workID, eventID: event.eventID, currentRevision: knownRevision, eventRevision: event.revision } };
  }
  const currentRevision = knownRevision ?? -1;
  if (event.baseRevision !== currentRevision) {
    const issue = gapsWith(state.gaps, event, currentRevision, 'base_revision_mismatch');
    return { patch: { gaps: issue.gaps }, result: { kind: 'gap', gap: issue.gap } };
  }

  if (event.type === 'removed') {
    const { [event.workID]: _, ...works } = state.works;
    return {
      patch: {
        works,
        revisions: { ...state.revisions, [event.workID]: event.revision },
        removed: { ...state.removed, [event.workID]: true },
        seenEventIDs: rememberEvent(state.seenEventIDs, event.workID, event.eventID),
        gaps: gapsAfterRevision(state.gaps, event.workID, event.revision),
        conflicts: clearIssue(state.conflicts, event.workID),
      },
      result: { kind: 'applied', workID: event.workID, revision: event.revision },
    };
  }

  const current = state.works[event.workID];
  if (!current || state.removed[event.workID]) {
    const issue = gapsWith(state.gaps, event, currentRevision, 'missing_projection');
    return { patch: { gaps: issue.gaps }, result: { kind: 'gap', gap: issue.gap } };
  }
  if (!isRecord(event.payload)) {
    const issue = conflict(event, 'delta payload must be an object');
    return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
  }
  const payload = event.payload as WorkDeltaPayload;
  if ((payload.state !== undefined && !WORK_STATES.has(payload.state)) ||
      (payload.archiveState !== undefined && !['active', 'archived', 'deleted'].includes(payload.archiveState)) ||
      (payload.name !== undefined && typeof payload.name !== 'string') ||
      (payload.prompt !== undefined && typeof payload.prompt !== 'string') ||
      (payload.inputs !== undefined && !isRecord(payload.inputs)) ||
      (payload.blocks !== undefined && !Array.isArray(payload.blocks)) ||
      (payload.removedBlockIds !== undefined && !Array.isArray(payload.removedBlockIds)) ||
      (payload.runs !== undefined && !Array.isArray(payload.runs)) ||
      (payload.cornerstones !== undefined && !Array.isArray(payload.cornerstones)) ||
      (payload.conclusions !== undefined && !Array.isArray(payload.conclusions)) ||
      payload.removedBlockIds?.some((id) => typeof id !== 'string') ||
      payload.runs?.some((run) => !validRun(run, event.workID)) ||
      payload.cornerstones?.some((cornerstone) => !validCornerstoneOwner(cornerstone, event.workID))) {
    const issue = conflict(event, 'delta payload contains invalid scalar or collection fields');
    return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
  }

  const work = structuredClone(current.work);
  const currentRuns = work.runs;
  if (payload.archiveState !== undefined) work.archiveState = payload.archiveState;
  if (payload.name !== undefined) work.name = payload.name;
  if (payload.prompt !== undefined) work.prompt = payload.prompt;
  if (payload.inputs !== undefined) work.inputs = structuredClone(payload.inputs);
  if (payload.blocks !== undefined || payload.removedBlockIds !== undefined) {
    const blocks = mergeDeltaBlocks(work.blocks, payload.blocks ?? [], payload.removedBlockIds ?? [], event.createdAt);
    if (blocks.error) {
      const issue = conflict(event, blocks.error);
      return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
    }
    work.blocks = blocks.blocks ?? work.blocks;
  }
  if (payload.runs !== undefined) work.runs = mergeDeltaRuns(work.runs, payload.runs);
  work.state = nextWorkState(work.state, payload.state, currentRuns, work.runs);
  if (payload.cornerstones !== undefined) work.cornerstones = structuredClone(payload.cornerstones);
  if (payload.conclusions !== undefined) work.conclusions = structuredClone(payload.conclusions);
  work.updatedAt = event.createdAt;
  return {
    patch: {
      works: { ...state.works, [event.workID]: { ...current, work, revision: event.revision } },
      revisions: { ...state.revisions, [event.workID]: event.revision },
      seenEventIDs: rememberEvent(state.seenEventIDs, event.workID, event.eventID),
      gaps: gapsAfterRevision(state.gaps, event.workID, event.revision),
      conflicts: clearIssue(state.conflicts, event.workID),
    },
    result: { kind: 'applied', workID: event.workID, revision: event.revision },
  };
}

function reduceEvent(state: WorkStoreData, event: WorkViewEvent): { patch?: Partial<WorkStoreData>; result: ApplyResult } {
  const contextError = objectContextConflict(event);
  if (contextError) {
    const issue = conflict(event, contextError);
    return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
  }
  if (event.type === 'attention') {
    if (state.seenEventIDs[event.workID]?.includes(event.eventID)) {
      return { result: { kind: 'duplicate', workID: event.workID, eventID: event.eventID } };
    }
    return {
      patch: { seenEventIDs: rememberEvent(state.seenEventIDs, event.workID, event.eventID) },
      result: { kind: 'ignored', workID: event.workID, eventID: event.eventID },
    };
  }
  if (event.type === 'snapshot') return applySnapshotEvent(state, event);
  return applyDeltaEvent(state, event);
}

const emptyWorkState = (): WorkStoreData => ({
  works: {},
  revisions: {},
  removed: {},
  seenEventIDs: {},
  resyncGenerations: {},
  gaps: {},
  conflicts: {},
});

export const useWorkStore = create<WorkStoreState>((set, get) => ({
  ...emptyWorkState(),
  applyEvent: (event) => {
    let result: ApplyResult = { kind: 'ignored', workID: event.workID, eventID: event.eventID };
    set((state) => {
      const reduced = reduceEvent(state, event);
      result = reduced.result;
      return reduced.patch ?? state;
    });
    return result;
  },
  applySnapshot: (view, eventID = `snapshot:${view.work.id}:${view.revision}`) => get().applyEvent({
    schemaVersion: view.schemaVersion,
    type: 'snapshot',
    workID: view.work.id,
    eventID,
    revision: view.revision,
    baseRevision: 0,
    requestID: eventID,
    object: { kind: 'work', id: view.work.id },
    payload: view,
    createdAt: view.work.updatedAt,
  }),
  removeProjection: (workID, revision) => set((state) => {
    const { [workID]: _, ...works } = state.works;
    const nextRevision = revision ?? state.revisions[workID];
    return {
      works,
      revisions: nextRevision === undefined ? state.revisions : { ...state.revisions, [workID]: nextRevision },
      removed: { ...state.removed, [workID]: true },
    };
  }),
  forgetWork: (workID) => set((state) => {
    const omit = <T,>(values: Record<string, T>): Record<string, T> => {
      const { [workID]: _, ...rest } = values;
      return rest;
    };
    return {
      works: omit(state.works),
      revisions: omit(state.revisions),
      removed: omit(state.removed),
      seenEventIDs: omit(state.seenEventIDs),
      resyncGenerations: omit(state.resyncGenerations),
      gaps: omit(state.gaps),
      conflicts: omit(state.conflicts),
    };
  }),
  clearAll: () => set(emptyWorkState()),
}));

export function applyWorkViewEvent(event: WorkViewEvent): ApplyResult {
  return useWorkStore.getState().applyEvent(event);
}

export function applySnapshot(view: WorkView, eventID?: string): ApplyResult {
  return useWorkStore.getState().applySnapshot(view, eventID);
}

export function applyDelta(event: WorkViewEvent): ApplyResult {
  return applyWorkViewEvent(event);
}

export function removeProjection(workID: string, revision?: number): void {
  useWorkStore.getState().removeProjection(workID, revision);
}

export type WorkFace = 'front' | 'back';

export interface FaceScrollState {
  scrollTop: number;
  scrollLeft: number;
}

export interface WorkFaceLocalState {
  scroll: FaceScrollState;
  expanded: Record<string, boolean>;
  draft: string;
}

export interface WorkCardLocalState {
  activeFace: WorkFace;
  faces: Record<WorkFace, WorkFaceLocalState>;
}

export interface WorkUIPreference {
  activeFace: WorkFace;
}

function defaultFaceState(): WorkFaceLocalState {
  return { scroll: { scrollTop: 0, scrollLeft: 0 }, expanded: {}, draft: '' };
}

export function defaultCardState(): WorkCardLocalState {
  return { activeFace: 'front', faces: { front: defaultFaceState(), back: defaultFaceState() } };
}

export interface WorkUIStoreState {
  cardByWork: Record<string, WorkCardLocalState>;
  selectionByWork: Record<string, RunSelection>;
  retryByTarget: Record<string, RetryStatus>;
  ensureCard: (workID: string) => void;
  setActiveFace: (workID: string, face: WorkFace) => void;
  setScroll: (workID: string, face: WorkFace, scroll: Partial<FaceScrollState>) => void;
  setExpanded: (workID: string, face: WorkFace, targetID: string, expanded: boolean) => void;
  setDraft: (workID: string, face: WorkFace, draft: string) => void;
  setSelection: (workID: string, selection: RunSelection) => void;
  beginRetry: (intent: RetryIntent) => void;
  failRetry: (intent: RetryIntent, error: string) => void;
  clearRetry: (intent: RetryIntent) => void;
  removeCard: (workID: string) => void;
  clearAll: () => void;
}

function cardFor(state: WorkUIStoreState, workID: string): WorkCardLocalState {
  return state.cardByWork[workID] ?? defaultCardState();
}

export const useWorkUIStore = create<WorkUIStoreState>((set) => ({
  cardByWork: {},
  selectionByWork: {},
  retryByTarget: {},
  ensureCard: (workID) => set((state) => state.cardByWork[workID] ? state : { cardByWork: { ...state.cardByWork, [workID]: defaultCardState() } }),
  setActiveFace: (workID, activeFace) => set((state) => {
    const card = cardFor(state, workID);
    return { cardByWork: { ...state.cardByWork, [workID]: { ...card, activeFace } } };
  }),
  setScroll: (workID, face, scroll) => set((state) => {
    const card = cardFor(state, workID);
    const faceState = card.faces[face];
    return { cardByWork: { ...state.cardByWork, [workID]: {
      ...card,
      faces: { ...card.faces, [face]: { ...faceState, scroll: { ...faceState.scroll, ...scroll } } },
    } } };
  }),
  setExpanded: (workID, face, targetID, expanded) => set((state) => {
    const card = cardFor(state, workID);
    const faceState = card.faces[face];
    return { cardByWork: { ...state.cardByWork, [workID]: {
      ...card,
      faces: { ...card.faces, [face]: { ...faceState, expanded: { ...faceState.expanded, [targetID]: expanded } } },
    } } };
  }),
  setDraft: (workID, face, draft) => set((state) => {
    const card = cardFor(state, workID);
    return { cardByWork: { ...state.cardByWork, [workID]: {
      ...card,
      faces: { ...card.faces, [face]: { ...card.faces[face], draft } },
    } } };
  }),
  setSelection: (workID, selection) => set((state) => ({
    selectionByWork: { ...state.selectionByWork, [workID]: selection },
  })),
  beginRetry: (intent) => set((state) => {
    const key = retryTargetKey(intent);
    const existing = state.retryByTarget[key];
    if (existing?.state === 'pending') return state;
    return { retryByTarget: { ...state.retryByTarget, [key]: { intent: existing?.intent ?? intent, state: 'pending' } } };
  }),
  failRetry: (intent, error) => set((state) => {
    const key = retryTargetKey(intent);
    const existing = state.retryByTarget[key];
    if (existing && existing.intent.requestId !== intent.requestId) return state;
    return { retryByTarget: { ...state.retryByTarget, [key]: { intent, state: 'failed', error } } };
  }),
  clearRetry: (intent) => set((state) => {
    const key = retryTargetKey(intent);
    const existing = state.retryByTarget[key];
    if (!existing || existing.intent.requestId !== intent.requestId) return state;
    const { [key]: _, ...retryByTarget } = state.retryByTarget;
    return { retryByTarget };
  }),
  removeCard: (workID) => set((state) => {
    const hasRetry = Object.values(state.retryByTarget).some((retry) => retry.intent.workId === workID);
    if (!(workID in state.cardByWork) && !(workID in state.selectionByWork) && !hasRetry) return state;
    const { [workID]: _card, ...cardByWork } = state.cardByWork;
    const { [workID]: _sel, ...selectionByWork } = state.selectionByWork;
    const retryByTarget = Object.fromEntries(Object.entries(state.retryByTarget).filter(([, retry]) => retry.intent.workId !== workID));
    return { cardByWork, selectionByWork, retryByTarget };
  }),
  clearAll: () => set({ cardByWork: {}, selectionByWork: {}, retryByTarget: {} }),
}));

export const selectWorkView = (works: Record<string, WorkView>, workID: string): WorkView | undefined => works[workID];
export const selectWork = (works: Record<string, WorkView>, workID: string): Work | undefined => works[workID]?.work;
export const selectWorkRevision = (revisions: Record<string, number>, workID: string): number => revisions[workID] ?? -1;
export const selectWorkIDs = (works: Record<string, WorkView>): string[] => Object.keys(works);
export const selectWorksByState = (works: Record<string, WorkView>, state: WorkState): WorkView[] => Object.values(works).filter((view) => view.work.state === state);
export const selectCardState = (cards: Record<string, WorkCardLocalState>, workID: string): WorkCardLocalState | undefined => cards[workID];
export const selectExpanded = (cards: Record<string, WorkCardLocalState>, workID: string, face: WorkFace, targetID: string): boolean => cards[workID]?.faces[face].expanded[targetID] ?? false;
export const selectSelection = (selections: Record<string, RunSelection>, workID: string): RunSelection | undefined => selections[workID];
export const retryTargetKey = (intent: Pick<RetryIntent, 'workId' | 'runId' | 'stageId' | 'taskId'>): string =>
  `${intent.workId}\u0000${intent.runId}\u0000${intent.stageId}\u0000${intent.taskId}`;
export const selectRetry = (retries: Record<string, RetryStatus>, intent: Pick<RetryIntent, 'workId' | 'runId' | 'stageId' | 'taskId'>): RetryStatus | undefined =>
  retries[retryTargetKey(intent)];
export const selectHasPendingRetry = (retries: Record<string, RetryStatus>, intent: Pick<RetryIntent, 'workId' | 'runId' | 'stageId' | 'taskId'>): boolean =>
  selectRetry(retries, intent)?.state === 'pending';

// ── Run navigation helpers ────────────────────────────────────────────────

export function findRun(work: Work, runId: string): WorkflowRun | undefined {
  return work.runs.find((r) => r.id === runId);
}

export function stageKey(stage: Stage): string {
  return stage.id || stage.name;
}

export function taskKey(task: Task): string {
  return task.id || task.name;
}

export function attemptKey(attempt: Attempt): string {
  return attempt.id || String(attempt.index);
}

export function findStage(run: WorkflowRun, stageId: string): Stage | undefined {
  return run.stages.find((stage) => stageKey(stage) === stageId);
}

export function findTask(stage: Stage, taskId: string): Task | undefined {
  return stage.tasks.find((task) => taskKey(task) === taskId);
}

export function findAttempt(task: Task, attemptId: string | undefined, attemptIndex: number | undefined): Attempt | undefined {
  if (attemptId) return task.attempts.find((attempt) => attemptKey(attempt) === attemptId);
  return attemptIndex === undefined ? undefined : task.attempts.find((attempt) => attempt.index === attemptIndex);
}

export function isRunTerminal(run: WorkflowRun): boolean {
  return RUN_TERMINAL.has(run.state);
}

export function isStageTerminal(stage: Stage): boolean {
  return RUN_TERMINAL.has(stage.state);
}

export function isTaskTerminal(task: Task): boolean {
  return RUN_TERMINAL.has(task.state);
}

export function isAttemptTerminal(attempt: Attempt): boolean {
  return RUN_TERMINAL.has(attempt.state);
}

export interface ResolvedSelection {
  run: WorkflowRun;
  stage?: Stage;
  task?: Task;
  attempt?: Attempt;
}

export function resolveSelection(work: Work, selection: RunSelection): ResolvedSelection | null {
  const run = findRun(work, selection.runId);
  if (!run) return null;
  if (!selection.stageId) return { run };
  const stage = findStage(run, selection.stageId);
  if (!stage) return { run };
  if (!selection.taskId) return { run, stage };
  const task = findTask(stage, selection.taskId);
  if (!task) return { run, stage };
  if (!selection.attemptId && selection.attemptIndex === undefined) return { run, stage, task };
  const attempt = findAttempt(task, selection.attemptId, selection.attemptIndex);
  return { run, stage, task, attempt: attempt ?? undefined };
}
