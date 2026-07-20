import { create } from 'zustand';

import type {
  BlockInstance,
  Cornerstone,
  Conclusion,
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
const RUN_STATES = new Set<WorkflowRun['state']>(['running', 'completed', 'failed', 'cancelled']);
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

function validRun(run: unknown, workID: string): run is WorkflowRun {
  return isRecord(run) &&
    typeof run.id === 'string' &&
    run.workId === workID &&
    RUN_STATES.has(run.state as WorkflowRun['state']) &&
    Array.isArray(run.stages);
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
  return { schemaVersion: work.schemaVersion, work, revision: event.revision };
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
  if (current && RUN_TERMINAL.has(current.state)) {
    next.state = current.state;
    next.finishedAt = current.finishedAt ?? next.finishedAt;
  }
  return next;
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
  const hasRunningRun = nextRuns.some((run) => run.state === 'running');
  if (WORK_TERMINAL.has(currentState)) {
    if (requestedState !== 'running' || hasNewRunningRun) return requestedState;
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
  const decoded = snapshotPayload(event);
  if (!decoded) {
    const issue = conflict(event, 'snapshot payload must be a matching Work or WorkView at the event revision');
    return { patch: { conflicts: { ...state.conflicts, [event.workID]: issue } }, result: { kind: 'conflict', conflict: issue } };
  }
  const current = state.works[event.workID];
  if (knownRevision !== undefined && event.revision === knownRevision) {
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
  ensureCard: (workID: string) => void;
  setActiveFace: (workID: string, face: WorkFace) => void;
  setScroll: (workID: string, face: WorkFace, scroll: Partial<FaceScrollState>) => void;
  setExpanded: (workID: string, face: WorkFace, targetID: string, expanded: boolean) => void;
  setDraft: (workID: string, face: WorkFace, draft: string) => void;
  removeCard: (workID: string) => void;
  clearAll: () => void;
}

function cardFor(state: WorkUIStoreState, workID: string): WorkCardLocalState {
  return state.cardByWork[workID] ?? defaultCardState();
}

export const useWorkUIStore = create<WorkUIStoreState>((set) => ({
  cardByWork: {},
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
  removeCard: (workID) => set((state) => {
    if (!(workID in state.cardByWork)) return state;
    const { [workID]: _, ...cardByWork } = state.cardByWork;
    return { cardByWork };
  }),
  clearAll: () => set({ cardByWork: {} }),
}));

export const selectWorkView = (works: Record<string, WorkView>, workID: string): WorkView | undefined => works[workID];
export const selectWork = (works: Record<string, WorkView>, workID: string): Work | undefined => works[workID]?.work;
export const selectWorkRevision = (revisions: Record<string, number>, workID: string): number => revisions[workID] ?? -1;
export const selectWorkIDs = (works: Record<string, WorkView>): string[] => Object.keys(works);
export const selectWorksByState = (works: Record<string, WorkView>, state: WorkState): WorkView[] => Object.values(works).filter((view) => view.work.state === state);
export const selectCardState = (cards: Record<string, WorkCardLocalState>, workID: string): WorkCardLocalState | undefined => cards[workID];
export const selectExpanded = (cards: Record<string, WorkCardLocalState>, workID: string, face: WorkFace, targetID: string): boolean => cards[workID]?.faces[face].expanded[targetID] ?? false;
