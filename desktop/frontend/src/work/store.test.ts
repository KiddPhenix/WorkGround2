import { WorkControllerAdapter, type WorkControllerPort } from './controller';
import {
  applySnapshot,
  applyWorkViewEvent,
  selectCardState,
  selectWork,
  useWorkStore,
  useWorkUIStore,
  type WorkDeltaPayload,
  type WorkUIPreference,
} from './store';
import type { BlockInstance, Work, WorkView, WorkViewEvent, WorkflowRun } from './types';

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
  fetch: (workID: string) => Promise<WorkView> = async () => { throw new Error('snapshot not configured'); };
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

let passed = 0;
for (const entry of tests) {
  await entry.run();
  passed++;
}
console.log(`Work store contract: ${passed}/${tests.length} tests passed`);
