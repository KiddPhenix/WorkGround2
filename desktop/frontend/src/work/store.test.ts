import { WorkControllerAdapter, type WorkControllerPort } from './controller';
import {
  applySnapshot,
  applyWorkViewEvent,
  resolveSelection,
  selectCardState,
  selectWork,
  useWorkStore,
  useWorkUIStore,
  type WorkDeltaPayload,
  type WorkUIPreference,
} from './store';
import type { Attempt, BlockInstance, RetryTaskInput, Work, WorkView, WorkViewEvent, WorkflowRun } from './types';

type Test = { name: string; run: () => void | Promise<void> };
const tests: Test[] = [];

function test(name: string, run: Test['run']): void {
  tests.push({ name, run });
}

function ok(value: unknown, message: string): asserts value {
  if (!value) throw new Error(message);
}

function equal<T>(actual: T, expected: T, message: string): void {
  if (actual !== expected) throw new Error(`${message}: got ${String(actual)}, want ${String(expected)}`);
}

function reset(): void {
  useWorkStore.getState().clearAll();
  useWorkUIStore.getState().clearAll();
}

function makeBlock(id: string, revision: number, patch: Partial<BlockInstance> = {}): BlockInstance {
  return {
    id,
    kind: 'list',
    schemaVersion: 1,
    revision,
    status: 'ready',
    data: { items: [] },
    source: { provider: 'controller', mode: 'snapshot', verified: true },
    fallback: { summary: id },
    createdAt: '2026-07-20T10:00:00Z',
    updatedAt: '2026-07-20T10:00:00Z',
    ...patch,
  };
}

function makeRun(id: string, state: WorkflowRun['state'], workID = 'work-1'): WorkflowRun {
  return {
    id,
    workId: workID,
    definitionDigest: 'digest',
    state,
    stages: [],
    startedAt: '2026-07-20T10:00:00Z',
    ...(state === 'running' ? {} : { finishedAt: '2026-07-20T10:01:00Z' }),
  };
}

function makeView(id: string, revision: number, patch: Partial<Work> = {}): WorkView {
  const blueprintRef = { id: 'blueprint:test', schemaVersion: 1, version: 1 };
  return {
    schemaVersion: 1,
    revision,
    work: {
      schemaVersion: 1,
      id,
      name: id,
      state: 'ready',
      archiveState: 'active',
      blueprintRef,
      definitionSnapshot: {
        schemaVersion: 1,
        revision: 1,
        blueprintRef,
        promptTemplate: '',
        workflow: { stages: [] },
        blockSpecs: [],
        digest: 'digest',
      },
      blocks: [],
      placements: [],
      prompt: '',
      cornerstones: [],
      runs: [],
      createdWith: { workSchemaVersion: 1, eventSchemaVersion: 1, rendererSetVersion: 1 },
      createdAt: '2026-07-20T10:00:00Z',
      updatedAt: '2026-07-20T10:00:00Z',
      ...patch,
    },
  };
}

function event(
  type: WorkViewEvent['type'],
  eventID: string,
  revision: number,
  baseRevision: number,
  payload: unknown,
  workID = 'work-1',
): WorkViewEvent {
  return {
    schemaVersion: 1,
    type,
    workID,
    eventID,
    revision,
    baseRevision,
    requestID: `request:${eventID}`,
    object: { kind: 'work', id: workID },
    payload,
    createdAt: `2026-07-20T10:00:${String(revision).padStart(2, '0')}Z`,
  };
}

function snapshot(view: WorkView, eventID = `snapshot:${view.revision}`): WorkViewEvent {
  return event('snapshot', eventID, view.revision, 0, view, view.work.id);
}

function delta(eventID: string, revision: number, baseRevision: number, payload: WorkDeltaPayload): WorkViewEvent {
  return event('delta', eventID, revision, baseRevision, payload);
}

class TestPort implements WorkControllerPort {
  readonly listeners = new Map<string, Set<(event: WorkViewEvent) => void>>();
  readonly writes: Array<{ workID: string; preference: WorkUIPreference }> = [];
  fetchCount = 0;
  retryCount = 0;
  fetch: (workID: string) => Promise<WorkView> = async () => { throw new Error('snapshot not configured'); };
  retry: (input: RetryTaskInput) => Promise<Attempt> = async () => { throw new Error('retry not configured'); };
  preference: WorkUIPreference | null = null;

  subscribe(workID: string, listener: (event: WorkViewEvent) => void): () => void {
    const listeners = this.listeners.get(workID) ?? new Set();
    listeners.add(listener);
    this.listeners.set(workID, listeners);
    return () => {
      listeners.delete(listener);
      if (listeners.size === 0) this.listeners.delete(workID);
    };
  }

  fetchSnapshot(workID: string): Promise<WorkView> {
    this.fetchCount++;
    return this.fetch(workID);
  }

  async readUIPreference(): Promise<WorkUIPreference | null> {
    return this.preference;
  }

  async writeUIPreference(workID: string, preference: WorkUIPreference): Promise<void> {
    this.writes.push({ workID, preference });
  }

  retryTask(input: RetryTaskInput): Promise<Attempt> {
    this.retryCount++;
    return this.retry(input);
  }

  emit(workID: string, value: WorkViewEvent): void {
    for (const listener of this.listeners.get(workID) ?? []) listener(value);
  }
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (reason: unknown) => void } {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

test('snapshot is event-idempotent and rejects stale/conflicting revisions', () => {
  reset();
  const initial = makeView('work-1', 5, { name: 'current' });
  equal(applyWorkViewEvent(snapshot(initial, 'snapshot-5')).kind, 'applied', 'initial snapshot applies');
  equal(applyWorkViewEvent(snapshot(initial, 'snapshot-5')).kind, 'duplicate', 'same eventID is duplicate');
  equal(applyWorkViewEvent(snapshot(makeView('work-1', 4, { name: 'old' }), 'snapshot-4')).kind, 'stale', 'older snapshot is stale');
  equal(applyWorkViewEvent(snapshot(makeView('work-1', 5, { name: 'conflict' }), 'snapshot-5b')).kind, 'conflict', 'same revision with different data conflicts');
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'current', 'conflict does not overwrite projection');
});

test('delta handles duplicate, late and retains the highest revision gap', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  const update = delta('delta-2', 2, 1, { name: 'updated' });
  equal(applyWorkViewEvent(update).kind, 'applied', 'contiguous delta applies');
  equal(applyWorkViewEvent(update).kind, 'duplicate', 'repeated eventID is duplicate');
  equal(applyWorkViewEvent(delta('late', 1, 0, { name: 'late' })).kind, 'stale', 'late revision is stale');
  equal(applyWorkViewEvent(delta('gap-4', 4, 3, { name: 'gap-4' })).kind, 'gap', 'first base mismatch reports gap');
  equal(applyWorkViewEvent(delta('gap-5', 5, 4, { name: 'gap-5' })).kind, 'gap', 'consecutive mismatch reports gap');
  equal(useWorkStore.getState().gaps['work-1']?.reason, 'base_revision_mismatch', 'gap remains observable');
  equal(useWorkStore.getState().gaps['work-1']?.eventRevision, 5, 'highest observed gap revision is retained');
  applyWorkViewEvent(snapshot(makeView('work-1', 4, { name: 'low-water' }), 'low-water-4'));
  equal(useWorkStore.getState().gaps['work-1']?.eventRevision, 5, 'lower snapshot cannot clear the high-water gap');
  equal(useWorkStore.getState().gaps['work-1']?.currentRevision, 4, 'gap records snapshot progress');
});

test('removed deletes the projection but keeps a revision tombstone', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  equal(applyWorkViewEvent(event('removed', 'remove-2', 2, 1, null)).kind, 'applied', 'removed applies');
  equal(selectWork(useWorkStore.getState().works, 'work-1'), undefined, 'removed projection is no longer visible');
  equal(useWorkStore.getState().revisions['work-1'], 2, 'removed revision is retained');
  equal(applyWorkViewEvent(delta('late-after-remove', 1, 0, { name: 'zombie' })).kind, 'stale', 'old delta cannot resurrect');
  equal(applyWorkViewEvent(delta('missing-after-remove', 3, 2, { name: 'missing' })).kind, 'gap', 'new delta needs a snapshot after removal');
  equal(applyWorkViewEvent(snapshot(makeView('work-1', 4, { name: 'restored' }), 'restore-4')).kind, 'applied', 'newer snapshot can restore');
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'restored', 'restored snapshot is visible');
});

test('block tombstone survives later work events and equal-revision conflict is explicit', () => {
  reset();
  applySnapshot(makeView('work-1', 1, { blocks: [makeBlock('block-1', 1)] }));
  equal(applyWorkViewEvent(delta('block-remove', 2, 1, { blocks: [{ id: 'block-1', revision: 2, tombstone: true }] })).kind, 'applied', 'block tombstone applies');
  equal(applyWorkViewEvent(delta('old-block', 3, 2, { blocks: [{ id: 'block-1', revision: 1, tombstone: false }] })).kind, 'applied', 'event can advance while old block is ignored');
  ok(selectWork(useWorkStore.getState().works, 'work-1')?.blocks[0].tombstone, 'old block cannot revive tombstone');
  equal(applyWorkViewEvent(delta('block-conflict', 4, 3, { blocks: [{ id: 'block-1', revision: 2, tombstone: false }] })).kind, 'conflict', 'same block revision with different content conflicts');
  equal(useWorkStore.getState().revisions['work-1'], 3, 'conflicting event does not advance revision');
});

test('removedBlockIds creates a retained block tombstone', () => {
  reset();
  applySnapshot(makeView('work-1', 1, { blocks: [makeBlock('block-1', 3)] }));
  applyWorkViewEvent(delta('remove-block-id', 2, 1, { removedBlockIds: ['block-1'] }));
  const block = selectWork(useWorkStore.getState().works, 'work-1')?.blocks[0];
  ok(block?.tombstone, 'removedBlockIds marks a tombstone');
  equal(block.revision, 4, 'removedBlockIds advances block revision');
});

test('terminal Run does not regress and invalid Work terminal transition is ignored', () => {
  reset();
  applySnapshot(makeView('work-1', 1, { state: 'completed', runs: [makeRun('run-1', 'completed')] }));
  applyWorkViewEvent(delta('terminal-delta', 2, 1, { state: 'running', runs: [makeRun('run-1', 'running')] }));
  let work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.state, 'completed', 'delta cannot regress Work terminal state');
  equal(work?.runs[0].state, 'completed', 'delta cannot regress Run terminal state');
  applyWorkViewEvent(snapshot(makeView('work-1', 3, { state: 'failed', runs: [makeRun('run-1', 'failed')] }), 'terminal-snapshot'));
  work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.state, 'completed', 'snapshot cannot replace a Work terminal state');
  equal(work?.runs[0].state, 'completed', 'snapshot cannot replace a Run terminal state');
});

test('completed Work starts a new Run and ignores a late old completion', () => {
  reset();
  applySnapshot(makeView('work-1', 1, { state: 'completed', runs: [makeRun('run-1', 'completed')] }));
  equal(
    applyWorkViewEvent(delta('rerun-started', 2, 1, { state: 'running', runs: [makeRun('run-2', 'running')] })).kind,
    'applied',
    'legal rerun delta applies',
  );
  let work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.state, 'running', 'new Run moves completed Work back to running');
  equal(work?.runs.length, 2, 'delta upserts the new Run without deleting history');
  equal(work?.runs.find((run) => run.id === 'run-2')?.state, 'running', 'new Run remains active');

  equal(
    applyWorkViewEvent(delta('late-old-completion', 3, 2, { state: 'completed', runs: [makeRun('run-1', 'completed')] })).kind,
    'applied',
    'late old completion is handled idempotently',
  );
  work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.state, 'running', 'old completion cannot complete Work with a newer active Run');
  equal(work?.runs.find((run) => run.id === 'run-2')?.state, 'running', 'old completion cannot remove the active Run');

  equal(
    applyWorkViewEvent(snapshot(makeView('work-1', 4, { state: 'completed', runs: [makeRun('run-1', 'completed')] }), 'late-old-snapshot')).kind,
    'applied',
    'late old completion snapshot remains recoverable',
  );
  work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.state, 'running', 'old completion snapshot cannot regress active rerun state');
  equal(work?.runs.find((run) => run.id === 'run-2')?.state, 'running', 'old snapshot cannot delete the active Run');
});

test('UI state is isolated by work and face and survives business snapshots', () => {
  reset();
  const ui = useWorkUIStore.getState();
  ui.setDraft('work-1', 'front', 'front draft');
  ui.setDraft('work-1', 'back', 'back draft');
  ui.setScroll('work-1', 'front', { scrollTop: 120 });
  ui.setExpanded('work-1', 'front', 'block-1', true);
  ui.setDraft('work-2', 'front', 'other draft');
  applySnapshot(makeView('work-1', 1));
  const first = selectCardState(useWorkUIStore.getState().cardByWork, 'work-1');
  const second = selectCardState(useWorkUIStore.getState().cardByWork, 'work-2');
  equal(first?.faces.front.draft, 'front draft', 'front draft survives snapshot');
  equal(first?.faces.back.draft, 'back draft', 'back draft is face-local');
  equal(first?.faces.front.scroll.scrollTop, 120, 'scroll survives snapshot');
  equal(first?.faces.front.expanded['block-1'], true, 'expanded state survives snapshot');
  equal(second?.faces.front.draft, 'other draft', 'work IDs are isolated');
});

test('adapter deduplicates recovery and fetches through the highest gap revision', async () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  useWorkUIStore.getState().setDraft('work-1', 'back', 'keep me');
  const lowWater = deferred<WorkView>();
  const highWater = deferred<WorkView>();
  const port = new TestPort();
  port.fetch = () => port.fetchCount === 1 ? lowWater.promise : highWater.promise;
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe('work-1');
  port.emit('work-1', delta('gap-a', 3, 2, { name: 'gap-a' }));
  port.emit('work-1', delta('gap-b', 4, 3, { name: 'gap-b' }));
  const joined = adapter.recoverSnapshot('work-1');
  await Promise.resolve();
  equal(port.fetchCount, 1, 'concurrent gap recovery uses one fetch');
  lowWater.resolve(makeView('work-1', 3, { name: 'low-water' }));
  await Promise.resolve();
  await Promise.resolve();
  equal(port.fetchCount, 2, 'low-water snapshot triggers another fetch');
  equal(useWorkStore.getState().gaps['work-1']?.eventRevision, 4, 'low-water snapshot keeps highest gap observable');
  highWater.resolve(makeView('work-1', 4, { name: 'backfilled' }));
  await joined;
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'backfilled', 'backfill snapshot applies');
  equal(selectCardState(useWorkUIStore.getState().cardByWork, 'work-1')?.faces.back.draft, 'keep me', 'backfill keeps UI draft');
  adapter.dispose();
});

test('failed progressive backfill keeps the gap and retries safely', async () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  applyWorkViewEvent(delta('gap-3', 3, 2, { name: 'gap-3' }));
  applyWorkViewEvent(delta('gap-4', 4, 3, { name: 'gap-4' }));
  const port = new TestPort();
  port.fetch = async () => {
    if (port.fetchCount === 1) return makeView('work-1', 3, { name: 'partial' });
    throw new Error('offline after partial snapshot');
  };
  const adapter = new WorkControllerAdapter(port);
  let failed = false;
  try { await adapter.recoverSnapshot('work-1'); } catch { failed = true; }
  ok(failed, 'failed follow-up fetch rejects');
  equal(port.fetchCount, 2, 'progressive recovery attempted the follow-up fetch');
  equal(useWorkStore.getState().gaps['work-1']?.eventRevision, 4, 'failed follow-up keeps the high-water gap');
  equal(useWorkStore.getState().gaps['work-1']?.currentRevision, 3, 'failed follow-up preserves applied progress');
  equal(adapter.getStatus('work-1').snapshotError, 'offline after partial snapshot', 'follow-up failure is observable');

  port.fetch = async () => makeView('work-1', 4, { name: 'recovered' });
  await adapter.recoverSnapshot('work-1');
  equal(port.fetchCount, 3, 'retry performs a new fetch');
  equal(useWorkStore.getState().gaps['work-1'], undefined, 'retry clears the gap at high-water');
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'recovered', 'retry applies the complete snapshot');
  adapter.dispose();
});

test('snapshot backfill failure is visible and safely retryable', async () => {
  reset();
  const port = new TestPort();
  port.fetch = async () => { throw new Error('offline'); };
  const adapter = new WorkControllerAdapter(port);
  let failed = false;
  try { await adapter.recoverSnapshot('work-1'); } catch { failed = true; }
  ok(failed, 'failed fetch rejects');
  equal(adapter.getStatus('work-1').snapshotError, 'offline', 'fetch error remains observable');
  port.fetch = async () => makeView('work-1', 2);
  equal((await adapter.recoverSnapshot('work-1')).kind, 'applied', 'retry applies snapshot');
  equal(port.fetchCount, 2, 'retry performs a new fetch');
  equal(adapter.getStatus('work-1').snapshotError, null, 'successful retry clears error');
  adapter.dispose();
});

test('stale snapshot cannot silently satisfy a gap and a newer retry recovers', async () => {
  reset();
  applySnapshot(makeView('work-1', 2));
  applyWorkViewEvent(delta('gap-before-stale-fetch', 5, 4, { name: 'gap' }));
  const port = new TestPort();
  port.fetch = async () => makeView('work-1', 1);
  const adapter = new WorkControllerAdapter(port);
  let failed = false;
  try { await adapter.recoverSnapshot('work-1'); } catch { failed = true; }
  ok(failed, 'stale snapshot does not report successful recovery');
  ok(adapter.getStatus('work-1').snapshotError?.includes('did not repair'), 'unrepaired gap stays observable');
  port.fetch = async () => makeView('work-1', 6, { name: 'recovered' });
  await adapter.recoverSnapshot('work-1');
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'recovered', 'newer retry repairs the gap');
  equal(useWorkStore.getState().gaps['work-1'], undefined, 'successful retry clears the gap');
  adapter.dispose();
});

test('adapter unsubscribe stops event delivery', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  const port = new TestPort();
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe('work-1');
  port.emit('work-1', delta('before-unsubscribe', 2, 1, { name: 'before' }));
  adapter.unsubscribe('work-1');
  port.emit('work-1', delta('after-unsubscribe', 3, 2, { name: 'after' }));
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'before', 'unsubscribed event is ignored');
  equal(port.listeners.has('work-1'), false, 'port listener is removed');
  adapter.dispose();
});

test('adapter persists only activeFace and preference restore keeps face drafts', async () => {
  reset();
  useWorkUIStore.getState().setDraft('work-1', 'back', 'local draft');
  const port = new TestPort();
  port.preference = { activeFace: 'back' };
  const adapter = new WorkControllerAdapter(port);
  await adapter.restoreUIPreference('work-1');
  let card = selectCardState(useWorkUIStore.getState().cardByWork, 'work-1');
  equal(card?.activeFace, 'back', 'activeFace restores from preference');
  equal(card?.faces.back.draft, 'local draft', 'preference restore does not replace draft');
  await adapter.setActiveFace('work-1', 'front');
  card = selectCardState(useWorkUIStore.getState().cardByWork, 'work-1');
  equal(card?.activeFace, 'front', 'adapter updates UI store');
  equal(port.writes.length, 1, 'adapter performs one preference write');
  equal(Object.keys(port.writes[0].preference).join(','), 'activeFace', 'only activeFace is persisted');
  adapter.dispose();
});

test('adapter deduplicates RetryTask by stable requestId', async () => {
  const pending = deferred<Attempt>();
  const port = new TestPort();
  port.retry = () => pending.promise;
  const adapter = new WorkControllerAdapter(port);
  const input: RetryTaskInput = { workId: 'work-1', runId: 'run-1', stageId: 'stage-1', taskId: 'task-1', requestId: 'retry-1' };
  const first = adapter.retryTask(input);
  const joined = adapter.retryTask(input);
  await Promise.resolve();
  equal(port.retryCount, 1, 'same requestId joins one backend RetryTask call');
  const attempt = makeAttempt(1, 'pending');
  pending.resolve(attempt);
  equal(await first, attempt, 'first caller receives the created Attempt');
  equal(await joined, attempt, 'joined caller receives the same Attempt');
  adapter.dispose();
});

test('adapter rejects cross-work subscription events explicitly', () => {
  reset();
  const port = new TestPort();
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe('work-1');
  port.emit('work-1', snapshot(makeView('work-2', 1), 'wrong-work'));
  ok(adapter.getStatus('work-1').eventError?.includes('work-2'), 'cross-work event is observable');
  equal(selectWork(useWorkStore.getState().works, 'work-2'), undefined, 'cross-work event does not mutate another projection');
  adapter.dispose();
});

test('store rejects cross-work payload ownership and object contexts', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  equal(
    applyWorkViewEvent(delta('foreign-run', 2, 1, { runs: [makeRun('run-2', 'running', 'work-2')] })).kind,
    'conflict',
    'cross-work Run delta conflicts',
  );
  equal(useWorkStore.getState().revisions['work-1'], 1, 'cross-work Run cannot advance work-1');
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.runs.length, 0, 'cross-work Run cannot enter work-1');

  equal(
    applyWorkViewEvent(snapshot(makeView('work-1', 2, { runs: [makeRun('run-3', 'running', 'work-2')] }), 'foreign-snapshot')).kind,
    'conflict',
    'cross-work Run snapshot conflicts',
  );
  equal(useWorkStore.getState().revisions['work-1'], 1, 'cross-work snapshot cannot advance work-1');

  const wrongContext = delta('foreign-context', 2, 1, { runs: [makeRun('run-4', 'running')] });
  wrongContext.object = { kind: 'run', id: 'run-4', parentID: 'work-2' };
  equal(applyWorkViewEvent(wrongContext).kind, 'conflict', 'cross-work object context conflicts');
  equal(useWorkStore.getState().revisions['work-1'], 1, 'cross-work context cannot mutate work-1');
});

// ── Nested terminal guards ───────────────────────────────────────────────

function makeAttempt(index: number, state: WorkflowRun['state'], patch: Partial<import('./types').Attempt> = {}): import('./types').Attempt {
  return {
    id: `attempt-${index}`,
    index,
    state,
    sessionRef: { sessionPath: `/sessions/${index}`, branchId: 'main', modelRef: 'test-model', turnCount: index + 1, preview: `attempt ${index}`, startedAt: '2026-07-20T10:00:00Z' },
    startedAt: '2026-07-20T10:00:00Z',
    ...(state === 'running' ? {} : { finishedAt: '2026-07-20T10:01:00Z' }),
    ...patch,
  };
}

function makeTask(name: string, state: WorkflowRun['state'], attempts: import('./types').Attempt[] = [], patch: Partial<import('./types').Task> = {}): import('./types').Task {
  return { id: `task-${name}`, name, state, attempts, ...patch };
}

function makeStage(name: string, state: WorkflowRun['state'], tasks: import('./types').Task[] = [], patch: Partial<import('./types').Stage> = {}): import('./types').Stage {
  return { id: `stage-${name}`, name, state, tasks, startedAt: '2026-07-20T10:00:00Z', ...(state === 'running' ? {} : { finishedAt: '2026-07-20T10:01:00Z' }), ...patch };
}

function makeRunWithStages(id: string, state: WorkflowRun['state'], stages: import('./types').Stage[], workID = 'work-1'): WorkflowRun {
  return {
    id,
    workId: workID,
    definitionDigest: 'digest',
    state,
    stages,
    startedAt: '2026-07-20T10:00:00Z',
    ...(state === 'running' ? {} : { finishedAt: '2026-07-20T10:01:00Z' }),
  };
}

test('nested Stage terminal guard blocks regression from completed to running', () => {
  reset();
  const stage = makeStage('review', 'completed', [makeTask('lint', 'completed', [makeAttempt(0, 'completed')])]);
  applySnapshot(makeView('work-1', 1, {
    state: 'running',
    runs: [makeRunWithStages('run-1', 'running', [stage])],
  }));
  // Try to regress stage back to running.
  const regressedStage = makeStage('review', 'running', [makeTask('lint', 'running', [makeAttempt(0, 'running')])]);
  applyWorkViewEvent(delta('stage-regress', 2, 1, {
    runs: [makeRunWithStages('run-1', 'running', [regressedStage])],
  }));
  const work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.runs[0].stages[0].state, 'completed', 'stage terminal state is preserved');
  equal(work?.runs[0].stages[0].tasks[0].state, 'completed', 'task under completed stage is preserved');
  equal(work?.runs[0].stages[0].tasks[0].attempts[0].state, 'completed', 'attempt under completed task is preserved');
});

test('nested Task terminal guard blocks regression, but new task appears', () => {
  reset();
  const lintTask = makeTask('lint', 'completed', [makeAttempt(0, 'completed')]);
  const stage = makeStage('review', 'running', [lintTask]);
  applySnapshot(makeView('work-1', 1, {
    state: 'running',
    runs: [makeRunWithStages('run-1', 'running', [stage])],
  }));
  // Incoming has lint regressed but adds a new 'test' task.
  const regressedLint = makeTask('lint', 'running', [makeAttempt(0, 'running')]);
  const newTest = makeTask('test', 'running', [makeAttempt(0, 'running')]);
  applyWorkViewEvent(delta('task-regress', 2, 1, {
    runs: [makeRunWithStages('run-1', 'running', [makeStage('review', 'running', [regressedLint, newTest])])],
  }));
  const work = selectWork(useWorkStore.getState().works, 'work-1');
  const tasks = work?.runs[0].stages[0].tasks ?? [];
  const lint = tasks.find((t) => t.name === 'lint');
  const test = tasks.find((t) => t.name === 'test');
  equal(lint?.state, 'completed', 'completed task state is preserved');
  equal(lint?.attempts[0].state, 'completed', 'completed attempt state is preserved');
  equal(test?.state, 'running', 'new task appears alongside preserved terminal task');
});

test('nested Attempt terminal guard preserves completed attempt, new retry attempt visible', () => {
  reset();
  const att0 = makeAttempt(0, 'completed', { finishedAt: '2026-07-20T10:01:00Z' });
  const task = makeTask('lint', 'completed', [att0]);
  const stage = makeStage('review', 'completed', [task]);
  applySnapshot(makeView('work-1', 1, {
    state: 'completed',
    runs: [makeRunWithStages('run-1', 'completed', [stage])],
  }));
  // Incoming: attempt 0 regressed to running, attempt 1 (retry) is running.
  const att0Regressed = makeAttempt(0, 'running');
  const att1 = makeAttempt(1, 'running');
  const retryTask = makeTask('lint', 'running', [att0Regressed, att1]);
  applyWorkViewEvent(delta('attempt-retry', 2, 1, {
    runs: [makeRunWithStages('run-1', 'running', [makeStage('review', 'running', [retryTask])])],
  }));
  const work = selectWork(useWorkStore.getState().works, 'work-1');
  const attempts = work?.runs[0].stages[0].tasks[0].attempts ?? [];
  const a0 = attempts.find((a) => a.index === 0);
  const a1 = attempts.find((a) => a.index === 1);
  equal(a0?.state, 'completed', 'completed attempt 0 is preserved in history');
  ok(a1 !== undefined, 'new retry attempt 1 is visible');
});

test('duplicate and late attempts do not corrupt attempt list', () => {
  reset();
  const att0 = makeAttempt(0, 'completed');
  const task = makeTask('lint', 'completed', [att0]);
  const stage = makeStage('review', 'completed', [task]);
  applySnapshot(makeView('work-1', 1, {
    state: 'completed',
    runs: [makeRunWithStages('run-1', 'completed', [stage])],
  }));
  // Late duplicate: same index 0, same state.
  applyWorkViewEvent(delta('late-dup', 2, 1, {
    runs: [makeRunWithStages('run-1', 'completed', [makeStage('review', 'completed', [makeTask('lint', 'completed', [makeAttempt(0, 'completed')])])])],
  }));
  const work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.runs[0].stages[0].tasks[0].attempts.length, 1, 'duplicate attempt does not duplicate');
});

test('stable IDs survive renamed labels and preserve terminal Attempt identity', () => {
  reset();
  const original = makeStage('old stage', 'running', [
    makeTask('old task', 'running', [makeAttempt(0, 'completed', { id: 'attempt-stable' })], { id: 'task-stable' }),
  ], { id: 'stage-stable' });
  applySnapshot(makeView('work-1', 1, { state: 'running', runs: [makeRunWithStages('run-1', 'running', [original])] }));
  const renamed = makeStage('new stage label', 'running', [
    makeTask('new task label', 'running', [
      makeAttempt(99, 'running', { id: 'attempt-stable' }),
      makeAttempt(1, 'running', { id: 'attempt-retry' }),
    ], { id: 'task-stable' }),
  ], { id: 'stage-stable' });
  applyWorkViewEvent(delta('stable-ids', 2, 1, { runs: [makeRunWithStages('run-1', 'running', [renamed])] }));
  const stage = selectWork(useWorkStore.getState().works, 'work-1')?.runs[0].stages[0];
  equal(stage?.id, 'stage-stable', 'Stage is merged by stable ID');
  equal(stage?.tasks.length, 1, 'renamed Task does not duplicate');
  equal(stage?.tasks[0].id, 'task-stable', 'Task is merged by stable ID');
  equal(stage?.tasks[0].attempts[0].index, 0, 'terminal Attempt keeps its committed identity and index');
  equal(stage?.tasks[0].attempts[0].state, 'completed', 'terminal Attempt cannot regress');
  equal(stage?.tasks[0].attempts[1].id, 'attempt-retry', 'new retry Attempt remains visible');
});

test('partial nested run delta preserves untouched stages and tasks', () => {
  reset();
  const first = makeStage('first', 'running', [makeTask('kept', 'running', [makeAttempt(0, 'running')])]);
  const second = makeStage('second', 'pending', [makeTask('later', 'pending')]);
  applySnapshot(makeView('work-1', 1, { state: 'running', runs: [makeRunWithStages('run-1', 'running', [first, second])] }));
  const changedFirst = makeStage('first', 'running', [makeTask('added', 'waiting')]);
  applyWorkViewEvent(delta('partial-nested', 2, 1, { runs: [makeRunWithStages('run-1', 'running', [changedFirst])] }));
  const stages = selectWork(useWorkStore.getState().works, 'work-1')?.runs[0].stages ?? [];
  equal(stages.length, 2, 'partial delta does not drop an untouched Stage');
  equal(stages[0].tasks.length, 2, 'partial delta does not drop an untouched Task');
  equal(stages[1].state, 'pending', 'pending RunState from the backend contract is accepted');
  equal(stages[0].tasks[1].state, 'waiting', 'waiting RunState from the backend contract is accepted');
});

test('RetryTask reopens one failed Run path only when a new Attempt is reserved', () => {
  reset();
  const failedAttempt = makeAttempt(0, 'failed', { requestId: 'run-1/execute/0', error: 'temporary failure' });
  const failedTask = makeTask('lint', 'failed', [failedAttempt]);
  const failedStage = makeStage('review', 'failed', [failedTask]);
  applySnapshot(makeView('work-1', 1, {
    state: 'failed',
    runs: [makeRunWithStages('run-1', 'failed', [failedStage])],
  }));

  const retryAttempt = makeAttempt(1, 'running', { requestId: 'retry-1/execute' });
  const reopenedTask = makeTask('lint', 'running', [makeAttempt(0, 'running'), retryAttempt]);
  const result = applyWorkViewEvent(delta('retry-reserved', 2, 1, {
    state: 'running',
    runs: [makeRunWithStages('run-1', 'running', [makeStage('review', 'running', [reopenedTask])])],
  }));

  equal(result.kind, 'applied', 'RetryTask reservation projection is accepted');
  const work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.state, 'running', 'failed Work reopens for the same retried Run');
  equal(work?.runs[0].state, 'running', 'failed Run reopens when a new Attempt exists');
  equal(work?.runs[0].stages[0].state, 'running', 'failed Stage reopens on the retry path');
  equal(work?.runs[0].stages[0].tasks[0].state, 'running', 'failed Task reopens on the retry path');
  equal(work?.runs[0].stages[0].tasks[0].attempts[0].state, 'failed', 'source Attempt remains terminal history');
  equal(work?.runs[0].stages[0].tasks[0].attempts[1].requestId, 'retry-1/execute', 'reserved retry Attempt is visible');
});

test('late running payload without a new Attempt on the same owner cannot reopen a failed path', () => {
  reset();
  const failed = makeAttempt(0, 'failed');
  applySnapshot(makeView('work-1', 1, {
    state: 'failed',
    runs: [makeRunWithStages('run-1', 'failed', [makeStage('review', 'failed', [makeTask('lint', 'failed', [failed])])])],
  }));
  applyWorkViewEvent(delta('late-running', 2, 1, {
    state: 'running',
    runs: [makeRunWithStages('run-1', 'running', [
      makeStage('review', 'running', [
        makeTask('lint', 'running', [makeAttempt(0, 'running')]),
        makeTask('foreign', 'running', [makeAttempt(1, 'running', { id: 'foreign-attempt' })]),
      ]),
      makeStage('foreign', 'running', [makeTask('foreign', 'running', [makeAttempt(0, 'running')])]),
    ])],
  }));
  const work = selectWork(useWorkStore.getState().works, 'work-1');
  equal(work?.state, 'failed', 'late same-Attempt payload cannot reopen Work');
  equal(work?.runs[0].state, 'failed', 'late same-Attempt payload cannot reopen Run');
  equal(work?.runs[0].stages[0].tasks[0].state, 'failed', 'late same-Attempt payload cannot reopen Task');
});

test('needs_confirmation and execution evidence survive projection validation', () => {
  reset();
  applySnapshot(makeView('work-1', 1, {
    state: 'running',
    runs: [makeRunWithStages('run-1', 'running', [makeStage('approval', 'running', [makeTask('deploy', 'running', [makeAttempt(0, 'running')])])])],
  }));
  const uncertain = makeAttempt(0, 'needs_confirmation', {
    requestId: 'deploy/execute',
    sideEffectClass: 'external_write',
    error: 'external outcome has no matching receipt',
    receipt: {
      requestId: '',
      outcome: 'observed',
      evidence: 'remote response was ambiguous',
      sideEffectClass: 'external_write',
      confirmedAt: '2026-07-20T10:01:00Z',
    },
  });
  const stage = makeStage('approval', 'needs_confirmation', [makeTask('deploy', 'needs_confirmation', [uncertain])], {
    gate: 'approval',
    resolution: { stageId: 'stage-approval', outcome: 'approved', note: 'reviewed' },
  });
  const result = applyWorkViewEvent(delta('needs-confirmation', 2, 1, {
    state: 'waiting_user',
    runs: [makeRunWithStages('run-1', 'needs_confirmation', [stage])],
  }));
  equal(result.kind, 'applied', 'needs_confirmation is a valid non-terminal RunState');
  const projected = selectWork(useWorkStore.getState().works, 'work-1')?.runs[0];
  equal(projected?.state, 'needs_confirmation', 'Run keeps needs_confirmation');
  equal(projected?.stages[0].resolution?.outcome, 'approved', 'GateResolution remains in the Stage projection');
  equal(projected?.stages[0].tasks[0].attempts[0].receipt?.requestId, '', 'mismatched AttemptReceipt remains visible for diagnosis');
  equal(projected?.stages[0].tasks[0].attempts[0].sideEffectClass, 'external_write', 'side-effect evidence remains visible');
});

test('terminal cancel receipt never regresses on a late delivery snapshot', () => {
  reset();
  const cancelled = makeRunWithStages('run-1', 'cancelled', [makeStage('review', 'cancelled', [makeTask('lint', 'cancelled', [makeAttempt(0, 'cancelled')])])]);
  cancelled.cancel = { requestId: 'cancel-1', status: 'delivered', attempts: 2, updatedAt: '2026-07-20T10:02:00Z' };
  applySnapshot(makeView('work-1', 1, { state: 'cancelled', runs: [cancelled] }));
  const late = makeRunWithStages('run-1', 'cancelled', [makeStage('review', 'running', [makeTask('lint', 'running', [makeAttempt(0, 'running')])])]);
  late.cancel = { requestId: 'cancel-1', status: 'pending', attempts: 1, updatedAt: '2026-07-20T10:01:00Z' };
  applyWorkViewEvent(delta('late-cancel-delivery', 2, 1, { runs: [late] }));
  const run = selectWork(useWorkStore.getState().works, 'work-1')?.runs[0];
  equal(run?.state, 'cancelled', 'cancelled Run remains terminal');
  equal(run?.cancel?.status, 'delivered', 'late pending receipt cannot regress delivered cancel');
  equal(run?.cancel?.attempts, 2, 'cancel delivery attempt counter is monotonic');
  equal(run?.stages[0].tasks[0].attempts[0].state, 'cancelled', 'late Task result cannot regress cancelled history');
});

test('selection state isolates by workID', () => {
  reset();
  const ui = useWorkUIStore.getState();
  ui.setSelection('work-1', { runId: 'run-a', stageId: 'stage-review' });
  ui.setSelection('work-2', { runId: 'run-b', taskId: 'task-lint' });
  const state = useWorkUIStore.getState();
  equal(state.selectionByWork['work-1']?.runId, 'run-a', 'selection is per-work');
  equal(state.selectionByWork['work-1']?.stageId, 'stage-review', 'stageId is preserved');
  equal(state.selectionByWork['work-2']?.taskId, 'task-lint', 'taskId is per-work isolated');
  state.removeCard('work-1');
  const after = useWorkUIStore.getState();
  equal(after.selectionByWork['work-1'], undefined, 'removeCard clears selection');
  ok(after.selectionByWork['work-2'] !== undefined, 'other work selection survives removeCard');
});

test('retry tracking prevents duplicate dispatch', () => {
  reset();
  const ui = useWorkUIStore.getState();
  const intent = { workId: 'work-1', runId: 'run-1', stageId: 'stage-review', taskId: 'task-lint', attemptId: 'attempt-0', attemptIndex: 0, requestId: 'retry-1' };
  ui.beginRetry(intent);
  let state = useWorkUIStore.getState();
  equal(Object.values(state.retryByTarget)[0]?.state, 'pending', 'retry intent is tracked');
  // Second begin with the same target and requestId is idempotent.
  state.beginRetry(intent);
  state = useWorkUIStore.getState();
  equal(Object.keys(state.retryByTarget).length, 1, 'duplicate requestId does not create duplicate');
  state.failRetry(intent, 'network unavailable');
  state = useWorkUIStore.getState();
  equal(Object.values(state.retryByTarget)[0]?.error, 'network unavailable', 'retry failure stays observable');
  state.beginRetry({ ...intent, requestId: 'retry-2' });
  state = useWorkUIStore.getState();
  equal(Object.values(state.retryByTarget)[0]?.intent.requestId, 'retry-1', 'retrying a failed target reuses its original requestId');
  state.clearRetry(intent);
  state = useWorkUIStore.getState();
  equal(Object.keys(state.retryByTarget).length, 0, 'clearRetry removes tracking');
  // clearRetry on an unknown target is a no-op.
  state.clearRetry({ ...intent, taskId: 'missing' });
  state = useWorkUIStore.getState();
  equal(Object.keys(state.retryByTarget).length, 0, 'clearRetry on unknown target is safe');
});

test('resolveSelection navigates nested structure correctly', () => {
  const att0 = makeAttempt(0, 'completed');
  const att1 = makeAttempt(1, 'failed', { error: 'timeout' });
  const task = makeTask('lint', 'completed', [att0, att1]);
  const stage = makeStage('review', 'completed', [task]);
  const run = makeRunWithStages('run-1', 'completed', [stage]);

  // Resolve to run level.
  equal(resolveSelection({ runs: [run] } as any, { runId: 'run-1' })?.run.id, 'run-1', 'resolves run');
  // Resolve to stage.
  equal(resolveSelection({ runs: [run] } as any, { runId: 'run-1', stageId: 'stage-review' })?.stage?.name, 'review', 'resolves stage');
  // Resolve to task.
  equal(resolveSelection({ runs: [run] } as any, { runId: 'run-1', stageId: 'stage-review', taskId: 'task-lint' })?.task?.name, 'lint', 'resolves task');
  // Resolve to attempt.
  const resolved = resolveSelection({ runs: [run] } as any, { runId: 'run-1', stageId: 'stage-review', taskId: 'task-lint', attemptId: 'attempt-1', attemptIndex: 1 });
  equal(resolved?.attempt?.index, 1, 'resolves attempt by index');
  equal(resolved?.attempt?.error, 'timeout', 'resolved attempt preserves error');
  // Missing run.
  equal(resolveSelection({ runs: [run] } as any, { runId: 'run-missing' }), null, 'missing run returns null');
  // Missing stage returns run only.
  equal(resolveSelection({ runs: [run] } as any, { runId: 'run-1', stageId: 'missing' })?.stage, undefined, 'missing stage returns run with no stage');
});

let passed = 0;
for (const entry of tests) {
  await entry.run();
  passed++;
}
console.log(`Work store contract: ${passed}/${tests.length} tests passed`);
