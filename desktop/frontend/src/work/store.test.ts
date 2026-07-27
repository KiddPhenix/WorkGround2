import { WorkControllerAdapter, type WorkControllerPort, type WorkPortSubscription } from './controller';
import {
  parseArtifactSlot,
  parseWorkDefinitionRevision,
  parseWorkInput,
  parseWorkPatchPreview,
  parseWorkViewEvent,
  parseWorkViewV2,
} from './parse';
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
import type { Attempt, BlockInstance, RetryTaskInput, ViewRecoveryIntent, Work, WorkView, WorkViewEvent, WorkflowRun } from './types';
import type {
  ArtifactSlot,
  ApplyDefinitionInput,
  ApplyDefinitionResult,
  CreateCandidateRevisionInput,
  CreateCandidateRevisionResult,
  TaskV2View,
  WorkInput,
  WorkPatchPreview,
  WorkViewV2,
} from './types_v2';
import fixtFutureEvent from './__fixtures__/work-view-event-future.json';
import fixtGoEvent from './__fixtures__/work-view-event-v2.json';
import fixtDefRevision from './__fixtures__/work-v2-definition-revision.json';
import fixtArtifactSlot from './__fixtures__/work-v2-artifact-slot.json';
import fixtWorkInput from './__fixtures__/work-v2-work-input.json';
import fixtPatchPreview from './__fixtures__/work-v2-patch-preview.json';
import fixtPatchResult from './__fixtures__/work-v2-patch-apply-result.json';
import fixtPatchReceipt from './__fixtures__/work-v2-patch-receipt.json';
import goEventGolden from '../../../../internal/work/testdata/contract-v2/work-view-event-v2.json';
import goFullV2Golden from '../../../../internal/work/testdata/contract-v2/work-view-v2-full.json';

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

function captureError(run: () => unknown): Error {
  try {
    run();
  } catch (error) {
    if (error instanceof Error) return error;
    throw new Error(`expected Error, got ${String(error)}`);
  }
  throw new Error('expected operation to fail');
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
    assessment: { state: 'ready', blocking: false, degraded: false, issues: [] },
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

function overflowResync(view: WorkView, generation: number): WorkViewEvent {
  const eventID = `wv-resync-${view.work.id}-rev-${view.revision}-overflow-${generation}`;
  return {
    ...snapshot(view, eventID),
    requestID: 'overflow-recovery',
    resync: { reason: 'overflow', authoritative: true, generation },
  };
}

function retryResync(view: WorkView, intent: ViewRecoveryIntent): WorkViewEvent {
  const eventID = `wv-resync-${view.work.id}-rev-${view.revision}-${intent.reason}-${intent.generation}`;
  return {
    ...snapshot(view, eventID),
    requestID: intent.reason + '-recovery',
    resync: { reason: intent.reason, authoritative: true, generation: intent.generation },
  };
}

function delta(eventID: string, revision: number, baseRevision: number, payload: WorkDeltaPayload): WorkViewEvent {
  return event('delta', eventID, revision, baseRevision, payload);
}

class TestPort implements WorkControllerPort {
  readonly listeners = new Map<string, Set<(event: WorkViewEvent) => void>>();
  readonly writes: Array<{ workID: string; preference: WorkUIPreference }> = [];
  fetchCount = 0;
  retryCount = 0;
  fetch: (workID: string) => Promise<WorkView | WorkViewV2> = async () => { throw new Error('snapshot not configured'); };
  recover: (workID: string, intent: ViewRecoveryIntent) => Promise<unknown> = async (workID, intent) =>
    retryResync(await this.fetchSnapshot(workID) as WorkView, intent);
  retry: (input: RetryTaskInput) => Promise<Attempt> = async () => { throw new Error('retry not configured'); };
  preference: WorkUIPreference | null = null;

  // V2 write tracking
  candidateInputs: CreateCandidateRevisionInput[] = [];
  applyInputs: ApplyDefinitionInput[] = [];
  candidateNext: CreateCandidateRevisionResult | null = null;
  applyNext: ApplyDefinitionResult | null = null;
  fetchDeferreds: Array<() => Promise<WorkView | WorkViewV2>> = [];

  subscribe(workID: string, listener: (event: WorkViewEvent) => void): WorkPortSubscription {
    const listeners = this.listeners.get(workID) ?? new Set();
    listeners.add(listener);
    this.listeners.set(workID, listeners);
    return {
      ready: Promise.resolve(),
      unsubscribe: () => {
        listeners.delete(listener);
        if (listeners.size === 0) this.listeners.delete(workID);
      },
    };
  }

  fetchSnapshot(workID: string): Promise<WorkView | WorkViewV2> {
    this.fetchCount++;
    if (this.fetchDeferreds.length > 0) {
      return this.fetchDeferreds.shift()!();
    }
    return this.fetch(workID);
  }

  async fetchRecoverySnapshot(workID: string, intent: ViewRecoveryIntent): Promise<WorkViewEvent> {
    return this.recover(workID, intent) as Promise<WorkViewEvent>;
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

  async createCandidateRevision(input: CreateCandidateRevisionInput): Promise<CreateCandidateRevisionResult> {
    this.candidateInputs.push(input);
    if (this.candidateNext) {
      const val = this.candidateNext;
      this.candidateNext = null;
      return val;
    }
    return {
      revision: input.expectedRevision + 1,
      duplicate: false,
      committed: true,
      recoverable: false,
    };
  }

  async applyDefinition(input: ApplyDefinitionInput): Promise<ApplyDefinitionResult> {
    this.applyInputs.push(input);
    if (this.applyNext) {
      const val = this.applyNext;
      this.applyNext = null;
      return val;
    }
    return {
      revision: input.expectedRevision + 1,
      duplicate: false,
      committed: true,
      recoverable: false,
    };
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

test('authoritative overflow resync has bounded revision and generation semantics', () => {
  const cases: Array<{
    name: string;
    incoming: WorkView;
    event: (view: WorkView) => WorkViewEvent;
    expected: ReturnType<typeof applyWorkViewEvent>['kind'];
    expectedName: string;
    expectedBlocked?: boolean;
  }> = [
    {
      name: 'same revision ordinary conflict',
      incoming: makeView('work-1', 5, { name: 'ordinary-conflict' }),
      event: (view) => snapshot(view, 'ordinary-conflict-5'),
      expected: 'conflict', expectedName: 'current',
    },
    {
      name: 'same revision authoritative accepted',
      incoming: { ...makeView('work-1', 5, { name: 'authoritative' }), assessment: { state: 'blocked', blocking: true, degraded: false, issues: [] } },
      event: (view) => overflowResync(view, 1),
      expected: 'applied', expectedName: 'authoritative', expectedBlocked: true,
    },
    {
      name: 'lower authoritative rejected',
      incoming: makeView('work-1', 4, { name: 'lower' }),
      event: (view) => overflowResync(view, 1),
      expected: 'stale', expectedName: 'current',
    },
    {
      name: 'higher authoritative follows normal merge',
      incoming: makeView('work-1', 6, { name: 'higher', state: 'running' }),
      event: (view) => overflowResync(view, 1),
      expected: 'applied', expectedName: 'higher',
    },
  ];

  for (const testCase of cases) {
    reset();
    const current = makeView('work-1', 5, { name: 'current', ...(testCase.name.includes('higher') ? { state: 'completed' as const } : {}) });
    applyWorkViewEvent(snapshot(current, `initial:${testCase.name}`));
    useWorkUIStore.getState().setDraft('work-1', 'front', 'local draft');
    const result = applyWorkViewEvent(testCase.event(testCase.incoming));
    equal(result.kind, testCase.expected, testCase.name);
    equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, testCase.expectedName, `${testCase.name} business projection`);
    if (testCase.name.includes('higher')) {
      equal(selectWork(useWorkStore.getState().works, 'work-1')?.state, 'completed', 'higher resync uses normal terminal merge');
    }
    if (testCase.expectedBlocked !== undefined) {
      equal(useWorkStore.getState().works['work-1']?.assessment.blocking, testCase.expectedBlocked, `${testCase.name} derived projection`);
    }
    equal(selectCardState(useWorkUIStore.getState().cardByWork, 'work-1')?.faces.front.draft, 'local draft', `${testCase.name} keeps UI draft`);
  }

  reset();
  applyWorkViewEvent(snapshot(makeView('work-1', 5), 'initial-duplicate'));
  const blocked = { ...makeView('work-1', 5), assessment: { state: 'blocked' as const, blocking: true, degraded: false, issues: [] } };
  const authoritative = overflowResync(blocked, 2);
  equal(applyWorkViewEvent(authoritative).kind, 'applied', 'first authoritative resync applies');
  equal(applyWorkViewEvent(authoritative).kind, 'duplicate', 'duplicate authoritative EventID is idempotent');
  equal(applyWorkViewEvent(overflowResync(makeView('work-1', 5), 1)).kind, 'ignored', 'older overflow generation is ignored');
  equal(useWorkStore.getState().works['work-1']?.assessment.blocking, true, 'older overflow cannot replace newer assessment');

  reset();
  const identical = makeView('work-1', 5);
  applyWorkViewEvent(snapshot(identical, 'initial-generation-watermark'));
  equal(applyWorkViewEvent(overflowResync(identical, 4)).kind, 'duplicate', 'identical authoritative resync stays data-idempotent');
  equal(applyWorkViewEvent(overflowResync(blocked, 3)).kind, 'ignored', 'identical resync still advances overflow generation watermark');
  equal(useWorkStore.getState().works['work-1']?.assessment.blocking, false, 'older changed overflow cannot bypass identical generation watermark');
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
  adapter.applyEvent(delta('gap-a', 3, 2, { name: 'gap-a' }));
  adapter.applyEvent(delta('gap-b', 4, 3, { name: 'gap-b' }));
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

test('progressive gap recovery is bounded and remains retryable', async () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  applyWorkViewEvent(delta('gap-5', 5, 4, { name: 'gap-5' }));
  const port = new TestPort();
  port.fetch = async () => makeView('work-1', port.fetchCount + 1, { name: `partial-${port.fetchCount}` });
  const adapter = new WorkControllerAdapter(port);
  let failed = false;
  try { await adapter.recoverSnapshot('work-1'); } catch { failed = true; }
  ok(failed, 'recovery rejects after its bounded fetch budget');
  equal(port.fetchCount, 3, 'one recovery is bounded to three fetches');
  equal(useWorkStore.getState().gaps['work-1']?.eventRevision, 5, 'bounded failure keeps the high-water gap');
  ok(adapter.getStatus('work-1').snapshotError?.includes('exceeded 3 fetches'), 'bounded failure remains observable');

  port.fetch = async () => makeView('work-1', 5, { name: 'recovered' });
  equal((await adapter.recoverSnapshot('work-1')).kind, 'applied', 'a later retry can repair the gap');
  equal(port.fetchCount, 4, 'retry starts a fresh bounded recovery');
  equal(useWorkStore.getState().gaps['work-1'], undefined, 'successful retry clears the gap');
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

test('ordinary same-revision refetch keeps conflict semantics and uses a fresh event identity', async () => {
  reset();
  let authoritative = makeView('work-1', 77, { name: 'ready assessment' });
  const port = new TestPort();
  port.fetch = async () => structuredClone(authoritative);
  const adapter = new WorkControllerAdapter(port);
  equal((await adapter.recoverSnapshot('work-1')).kind, 'applied', 'initial ordinary snapshot applies');

  authoritative = makeView('work-1', 77, { name: 'same revision changed' });
  let failed = false;
  try { await adapter.recoverSnapshot('work-1'); } catch { failed = true; }
  ok(failed, 'different ordinary snapshot at the same revision conflicts instead of looking duplicate');
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'ready assessment', 'ordinary conflict does not overwrite projection');
  ok(adapter.getStatus('work-1').snapshotError?.includes('different snapshot'), 'ordinary same-revision conflict remains observable');
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

test('adapter unsubscribe stops event delivery', async () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  const port = new TestPort();
  port.fetch = async () => makeView('work-1', 1);
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe('work-1');
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
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

// ── V2 store tests ────────────────────────────────────────────────────────

function makeV2Slot(id: string, revision: number, state: ArtifactSlot['state'] = 'reserved'): ArtifactSlot {
  return {
    id, workId: 'work-1', definitionRev: 1, title: `Slot ${id}`, kind: 'docx',
    expectedCount: 1, required: true, state, artifactRefs: [], revision,
  };
}

function makeV2Task(id: string, runId: string, state: TaskV2View['state'] = 'pending'): TaskV2View {
  return {
    id, runId, nodeId: `node-${id}`, title: `Task ${id}`, state,
    retryable: state !== 'completed', updatedAt: '2026-07-23T10:00:00Z',
  };
}

function makeV2Input(id: string, revision: number, state: WorkInput['state'] = 'requested'): WorkInput {
  return {
    id, workId: 'work-1', runId: 'run-1', taskId: 'task-1', blockId: 'block-1',
    specId: `spec-${id}`, value: null, state, revision,
    updatedAt: '2026-07-23T10:00:00Z',
  };
}

function makeV2Patch(id: string): WorkPatchPreview {
  return {
    id, workId: 'work-1', runId: 'run-1', taskId: 'task-1', blockId: 'block-1',
    sessionId: `session-${id}`, baseDefinitionRev: 1, baseBlockRev: 1,
    scope: 'block', operations: [], affectedNodeIds: [], affectedBlockIds: [],
    affectedArtifactSlotIds: [], staleArtifactSlotIds: [], invalidatedTaskIds: [],
    requiresRerun: false, digest: `digest-${id}`, expiresAt: '2026-07-24T10:00:00Z',
  };
}

function throughParser(value: WorkViewEvent): WorkViewEvent {
  const parsed = parseWorkViewEvent(JSON.stringify(value));
  ok(parsed.event !== null && parsed.futureError === null, `event ${value.eventID} parses`);
  return parsed.event;
}

function v2Snapshot(view: WorkView, v2Fields?: { artifactSlots?: ArtifactSlot[]; tasks?: TaskV2View[]; inputs?: WorkInput[] }): WorkViewEvent {
  const payload: Record<string, unknown> = { ...view, schemaVersion: 2 };
  if (v2Fields?.artifactSlots) payload.artifactSlots = v2Fields.artifactSlots;
  if (v2Fields?.tasks) payload.tasks = v2Fields.tasks;
  if (v2Fields?.inputs) payload.inputs = v2Fields.inputs;
  return throughParser(event('snapshot', `v2-snapshot:${view.revision}`, view.revision, 0, payload, view.work.id));
}

function v2Delta(
  eventID: string, revision: number, baseRevision: number,
  v1Payload: WorkDeltaPayload, v2Fields?: Partial<import('./store').WorkV2DeltaFields>,
): WorkViewEvent {
  const payload: Record<string, unknown> = { ...v1Payload };
  if (v2Fields?.artifactSlots) payload.artifactSlots = v2Fields.artifactSlots;
  if (v2Fields?.tasks) payload.tasks = v2Fields.tasks;
  if (v2Fields?.inputs) payload.inputs = v2Fields.inputs;
  if (v2Fields?.patchPreviews) payload.patchPreviews = v2Fields.patchPreviews;
  if (v2Fields?.removedArtifactSlotIds) payload.removedArtifactSlotIds = v2Fields.removedArtifactSlotIds;
  if (v2Fields?.removedTaskIds) payload.removedTaskIds = v2Fields.removedTaskIds;
  if (v2Fields?.removedInputIds) payload.removedInputIds = v2Fields.removedInputIds;
  if (v2Fields?.removedPatchIds) payload.removedPatchIds = v2Fields.removedPatchIds;
  return throughParser(event('delta', eventID, revision, baseRevision, payload));
}

function fullGoldenSnapshotEvent(
  eventID: string,
  resync?: WorkViewEvent['resync'],
): WorkViewEvent {
  const view = structuredClone(goFullV2Golden) as unknown as WorkViewV2;
  return {
    schemaVersion: 2,
    type: 'snapshot',
    workID: view.work.id,
    eventID,
    revision: view.revision,
    baseRevision: 0,
    requestID: eventID,
    object: { kind: 'work', id: view.work.id, workID: view.work.id },
    payload: view,
    createdAt: view.work.updatedAt,
    ...(resync ? { resync } : {}),
  };
}

function fullGoldenWithoutArtifactRefs(): Record<string, unknown> {
  const view = structuredClone(goFullV2Golden) as unknown as Record<string, unknown>;
  if (!Array.isArray(view.artifactSlots) || view.artifactSlots.length === 0 ||
      typeof view.artifactSlots[0] !== 'object' || view.artifactSlots[0] === null) {
    throw new Error('real Go full golden must contain an artifact slot');
  }
  delete (view.artifactSlots[0] as Record<string, unknown>).artifactRefs;
  return view;
}

function fullGoldenMissingArtifactRefsEvent(
  type: 'snapshot' | 'delta',
  eventID: string,
  revision: number,
  baseRevision: number,
  resync?: WorkViewEvent['resync'],
): WorkViewEvent {
  const malformed = fullGoldenWithoutArtifactRefs();
  const valid = goFullV2Golden as unknown as WorkViewV2;
  const payload = type === 'snapshot'
    ? malformed
    : { artifactSlots: malformed.artifactSlots };
  return {
    schemaVersion: 2,
    type,
    workID: valid.work.id,
    eventID,
    revision,
    baseRevision,
    requestID: eventID,
    object: { kind: 'work', id: valid.work.id, workID: valid.work.id },
    payload,
    createdAt: valid.work.updatedAt,
    ...(resync ? { resync } : {}),
  };
}

function assertFullGoldenProjected(view: WorkViewV2, chain: string): void {
  const state = useWorkStore.getState();
  const workID = view.work.id;
  equal(state.revisions[workID], view.revision, `${chain}: revision enters the single Store`);
  equal(state.v2Definitions[workID]?.digest, view.definition?.digest, `${chain}: definition enters the single Store`);
  equal((state.artifactSlots[workID] ?? [])[0]?.id, view.artifactSlots?.[0]?.id, `${chain}: artifact slot enters the single Store`);
  equal((state.artifactSlots[workID] ?? [])[0]?.artifactRefs.length, 0, `${chain}: Go null slice is normalized for UI consumption`);
  equal((state.v2Tasks[workID] ?? [])[0]?.state, 'completed', `${chain}: task enters the single Store`);
  equal((state.v2Inputs[workID] ?? [])[0]?.state, 'requested', `${chain}: input enters the single Store`);
  equal((state.patchPreviews[workID] ?? [])[0]?.id, view.patchPreviews?.[0]?.id, `${chain}: patch preview enters the single Store`);
}

test('real Go ArtifactSlot distinguishes required artifactRefs, null, and wrong types', () => {
  const valid = structuredClone(goFullV2Golden) as unknown as Record<string, unknown>;
  const slots = valid.artifactSlots as Record<string, unknown>[];
  const parsedNull = parseArtifactSlot(slots[0]);
  equal(parsedNull.artifactRefs.length, 0, 'explicit Go null slice normalizes to an empty UI array');

  const missing = fullGoldenWithoutArtifactRefs();
  const missingError = captureError(() => parseWorkViewV2(missing));
  ok(missingError instanceof TypeError, 'missing artifactRefs is a parser TypeError');
  ok(missingError.message.includes('missing required field "artifactRefs"'), 'missing field is distinguishable from null');

  const wrong = structuredClone(goFullV2Golden) as unknown as Record<string, unknown>;
  (wrong.artifactSlots as Record<string, unknown>[])[0].artifactRefs = {};
  const wrongTypeError = captureError(() => parseWorkViewV2(wrong));
  ok(wrongTypeError instanceof TypeError, 'wrong artifactRefs type is a parser TypeError');
  ok(wrongTypeError.message.includes('expected array'), 'wrong artifactRefs type remains explicit');
});

test('V2 snapshot applies artifactSlots, tasks, inputs alongside V1 Work', () => {
  reset();
  const slot = makeV2Slot('slot-1', 1, 'generating');
  const task = makeV2Task('task-1', 'run-1', 'running');
  const input = makeV2Input('input-1', 1, 'requested');
  const result = applyWorkViewEvent(v2Snapshot(makeView('work-1', 5), {
    artifactSlots: [slot], tasks: [task], inputs: [input],
  }));
  equal(result.kind, 'applied', 'V2 snapshot applies');
  const slots = useWorkStore.getState().artifactSlots['work-1'] ?? [];
  const tasks = useWorkStore.getState().v2Tasks['work-1'] ?? [];
  const inputs = useWorkStore.getState().v2Inputs['work-1'] ?? [];
  equal(slots.length, 1, 'artifact slot is projected');
  equal(slots[0].state, 'generating', 'slot state matches');
  equal(tasks.length, 1, 'V2 task is projected');
  equal(tasks[0].state, 'running', 'task state matches');
  equal(inputs.length, 1, 'V2 input is projected');
  equal(inputs[0].state, 'requested', 'input state matches');
});

test('V2 delta merges artifactSlots by revision and rejects same-revision conflict', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  const slot1 = makeV2Slot('slot-1', 1, 'reserved');
  equal(applyWorkViewEvent(v2Delta('v2d-2', 2, 1, {}, { artifactSlots: [slot1] })).kind, 'applied', 'first slot delta applies');
  equal(applyWorkViewEvent(v2Delta('v2d-3', 3, 2, {}, { artifactSlots: [makeV2Slot('slot-1', 2, 'generating')] })).kind, 'applied', 'higher revision slot applies');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].state, 'generating', 'slot updated to generating');

  // Stale (lower revision) is ignored.
  equal(applyWorkViewEvent(v2Delta('v2d-4', 4, 3, {}, { artifactSlots: [makeV2Slot('slot-1', 1, 'reserved')] })).kind, 'applied', 'stale slot revision is ignored');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].revision, 2, 'slot keeps higher revision');

  // Same revision, different content → conflict.
  const conflictSlot = { ...makeV2Slot('slot-1', 2), title: 'CHANGED' };
  const conflictResult = applyWorkViewEvent(v2Delta('v2d-5', 5, 4, {}, { artifactSlots: [conflictSlot] }));
  equal(conflictResult.kind, 'conflict', 'same-revision different content is an explicit conflict');
  equal(useWorkStore.getState().revisions['work-1'], 4, 'conflict cannot advance authoritative revision');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].title, 'Slot slot-1', 'conflict cannot overwrite slot');
});

test('V2 task transitions match the authoritative Go state machine', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  equal(
    applyWorkViewEvent(v2Delta('v2d-2', 2, 1, {}, {
      tasks: [
        makeV2Task('task-completed', 'run-1', 'completed'),
        makeV2Task('task-canceled', 'run-1', 'canceled'),
      ],
    })).kind,
    'applied',
    'initial task states apply',
  );
  equal(
    applyWorkViewEvent(v2Delta('v2d-3', 3, 2, {}, {
      tasks: [
        makeV2Task('task-completed', 'run-1', 'invalidated'),
        makeV2Task('task-canceled', 'run-1', 'ready'),
      ],
    })).kind,
    'applied',
    'completed→invalidated and canceled→ready are legal Go transitions',
  );
  const tasks = useWorkStore.getState().v2Tasks['work-1'] ?? [];
  equal(tasks.find((task) => task.id === 'task-completed')?.state, 'invalidated', 'completed task becomes invalidated');
  equal(tasks.find((task) => task.id === 'task-canceled')?.state, 'ready', 'canceled task becomes ready for retry');
});

test('V2 task rejects transitions absent from the authoritative Go state machine', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  applyWorkViewEvent(v2Delta('v2d-2', 2, 1, {}, {
    tasks: [makeV2Task('task-1', 'run-1', 'completed')],
  }));
  const invalid = applyWorkViewEvent(v2Delta('v2d-3', 3, 2, {}, {
    tasks: [makeV2Task('task-1', 'run-1', 'running')],
  }));
  equal(invalid.kind, 'conflict', 'completed→running is an explicit conflict');
  equal(useWorkStore.getState().revisions['work-1'], 2, 'illegal transition cannot advance projection revision');
  equal((useWorkStore.getState().v2Tasks['work-1'] ?? [])[0].state, 'completed', 'illegal transition cannot overwrite task state');
  ok(
    useWorkStore.getState().conflicts['work-1']?.reason.includes('completed → running'),
    'illegal transition remains observable',
  );
});

test('V2 authoritative snapshot repairs a disconnected multi-step task transition and preserves UI state', () => {
  reset();
  useWorkUIStore.getState().setDraft('work-1', 'front', 'keep local input');
  applyWorkViewEvent(v2Snapshot(makeView('work-1', 1), {
    tasks: [makeV2Task('task-1', 'run-1', 'pending')],
  }));
  equal(
    applyWorkViewEvent(v2Snapshot(makeView('work-1', 5), {
      tasks: [makeV2Task('task-1', 'run-1', 'completed')],
    })).kind,
    'applied',
    'authoritative snapshot may bridge pending→ready→running→completed after disconnect',
  );
  equal((useWorkStore.getState().v2Tasks['work-1'] ?? [])[0].state, 'completed', 'authority replaces stale task state');
  equal(
    selectCardState(useWorkUIStore.getState().cardByWork, 'work-1')?.faces.front.draft,
    'keep local input',
    'authoritative task recovery preserves UI-local draft',
  );
});

test('V2 removed event clears both V1 and V2 state', () => {
  reset();
  const slot = makeV2Slot('slot-1', 1);
  applyWorkViewEvent(v2Snapshot(makeView('work-1', 1), { artifactSlots: [slot] }));
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 1, 'slot exists before removal');
  equal(applyWorkViewEvent(event('removed', 'remove-2', 2, 1, null)).kind, 'applied', 'removed applies');
  equal(selectWork(useWorkStore.getState().works, 'work-1'), undefined, 'V1 projection removed');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 0, 'V2 slots removed');
});

test('V2 removedArtifactSlotIds and removedTaskIds remove items', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  applyWorkViewEvent(v2Delta('v2d-2', 2, 1, {}, {
    artifactSlots: [makeV2Slot('slot-1', 1), makeV2Slot('slot-2', 1)],
    tasks: [makeV2Task('task-1', 'run-1'), makeV2Task('task-2', 'run-2')],
  }));
  applyWorkViewEvent(v2Delta('v2d-3', 3, 2, {}, {
    removedArtifactSlotIds: ['slot-1'],
    removedTaskIds: ['task-1'],
  }));
  const slots = useWorkStore.getState().artifactSlots['work-1'] ?? [];
  const tasks = useWorkStore.getState().v2Tasks['work-1'] ?? [];
  equal(slots.length, 1, 'slot-1 removed, slot-2 remains');
  equal(slots[0].id, 'slot-2', 'remaining slot is slot-2');
  equal(tasks.length, 1, 'task-1 removed, task-2 remains');
  equal(tasks[0].id, 'task-2', 'remaining task is task-2');
});

test('V2 inputs merge by revision', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  applyWorkViewEvent(v2Delta('v2d-2', 2, 1, {}, { inputs: [makeV2Input('input-1', 1, 'requested')] }));
  // Higher revision overrides.
  applyWorkViewEvent(v2Delta('v2d-3', 3, 2, {}, { inputs: [makeV2Input('input-1', 2, 'submitted')] }));
  let inputs = useWorkStore.getState().v2Inputs['work-1'] ?? [];
  equal(inputs[0].state, 'submitted', 'input state upgraded to submitted');
  equal(inputs[0].revision, 2, 'input revision advanced');
  // Stale revision ignored.
  applyWorkViewEvent(v2Delta('v2d-4', 4, 3, {}, { inputs: [makeV2Input('input-1', 1, 'draft')] }));
  inputs = useWorkStore.getState().v2Inputs['work-1'] ?? [];
  equal(inputs[0].revision, 2, 'stale input revision ignored');
});

test('V2 patch previews merge and reject same-ID conflicts', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  applyWorkViewEvent(v2Delta('v2d-2', 2, 1, {}, { patchPreviews: [makeV2Patch('patch-1')] }));
  let patches = useWorkStore.getState().patchPreviews['work-1'] ?? [];
  equal(patches.length, 1, 'patch preview added');
  // Same ID, same content → ok (no-op).
  applyWorkViewEvent(v2Delta('v2d-3', 3, 2, {}, { patchPreviews: [makeV2Patch('patch-1')] }));
  patches = useWorkStore.getState().patchPreviews['work-1'] ?? [];
  equal(patches.length, 1, 'same patch idempotent');
  // Removed.
  applyWorkViewEvent(v2Delta('v2d-4', 4, 3, {}, { removedPatchIds: ['patch-1'] }));
  patches = useWorkStore.getState().patchPreviews['work-1'] ?? [];
  equal(patches.length, 0, 'patch removed');
});

test('V2 delta baseRevision mismatch reports gap and keeps highest', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  equal(applyWorkViewEvent(v2Delta('v2d-3', 3, 2, {}, { artifactSlots: [makeV2Slot('slot-1', 1)] })).kind, 'gap', 'baseRevision mismatch → gap');
  equal(useWorkStore.getState().gaps['work-1']?.eventRevision, 3, 'highest gap revision recorded');
  // V2 fields are not applied on gap.
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 0, 'V2 fields not applied on gap');
  // Backfill snapshot repairs.
  equal(applyWorkViewEvent(v2Snapshot(makeView('work-1', 3), { artifactSlots: [makeV2Slot('slot-1', 3, 'ready')] })).kind, 'applied', 'backfill snapshot repairs');
  equal(useWorkStore.getState().gaps['work-1'], undefined, 'gap cleared by backfill');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].state, 'ready', 'V2 slot applied via backfill snapshot');
});

test('V2 duplicate eventID is idempotent', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  const evt = v2Delta('v2d-2', 2, 1, {}, { artifactSlots: [makeV2Slot('slot-1', 1)] });
  equal(applyWorkViewEvent(evt).kind, 'applied', 'first delta applies');
  equal(applyWorkViewEvent(evt).kind, 'duplicate', 'same eventID is duplicate');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 1, 'idempotent does not duplicate data');
});

test('V2 mutation snapshot converges with same-revision same-content authoritative snapshot', () => {
  // After a V2 mutation emits a full WorkView snapshot,
  // a subsequent GetWork snapshot at the same revision with identical
  // content must be a duplicate — not "different snapshot at the current
  // revision".
  reset();
  const view = makeView('work-1', 2, { blocks: [makeBlock('b1', 1)], updatedAt: '2026-07-20T10:00:02Z' });
  const v2m = v2Snapshot(view, { artifactSlots: [makeV2Slot('slot-1', 1)] });
  equal(applyWorkViewEvent(v2m).kind, 'applied', 'V2 mutation snapshot applies');
  equal(useWorkStore.getState().revisions['work-1'], 2, 'revision advanced');
  const baseView = useWorkStore.getState().works['work-1'] as WorkViewV2;
  equal(baseView.artifactSlots, undefined, 'base WorkView does not duplicate V2 artifact state');
  equal(baseView.tasks, undefined, 'base WorkView does not duplicate V2 task state');
  equal(baseView.inputs, undefined, 'base WorkView does not duplicate V2 input state');

  // Same-revision authoritative snapshot with identical Work content must be duplicate.
  const resync = v2Snapshot(view, { artifactSlots: [makeV2Slot('slot-1', 1)] });
  // Force a different eventID so seenEventIDs does not short-circuit.
  const duplicate = applyWorkViewEvent({ ...resync, eventID: 'getwork-snapshot:2' });
  equal(duplicate.kind, 'duplicate', 'same-revision same-content snapshot is duplicate');
  equal(useWorkStore.getState().revisions['work-1'], 2, 'revision unchanged');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].state, 'reserved', 'V2 projection unchanged');
});

test('V2 mutation snapshot same-revision truly different content still conflicts', () => {
  reset();
  const view = makeView('work-1', 2, { blocks: [makeBlock('b1', 1)], updatedAt: '2026-07-20T10:00:02Z' });
  const v2m = v2Snapshot(view, { artifactSlots: [makeV2Slot('slot-1', 1)] });
  equal(applyWorkViewEvent(v2m).kind, 'applied', 'V2 mutation snapshot applies');

  // Same revision but different block content → must be conflict.
  const different = makeView('work-1', 2, { blocks: [makeBlock('b1', 1, { title: 'changed' })], updatedAt: '2026-07-20T10:00:02Z' });
  const conflictEvent = v2Snapshot(different, { artifactSlots: [makeV2Slot('slot-1', 1)] });
  const conflict = applyWorkViewEvent({ ...conflictEvent, eventID: 'other-snapshot:2' });
  equal(conflict.kind, 'conflict', 'same-revision different content conflicts');
});

test('V2 different eventID cannot reuse the same authoritative work revision', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  equal(applyWorkViewEvent(v2Delta('v2-first-2', 2, 1, {}, {
    artifactSlots: [makeV2Slot('slot-1', 1, 'reserved')],
  })).kind, 'applied', 'first revision 2 delta applies');
  const conflict = applyWorkViewEvent(v2Delta('v2-other-2', 2, 1, {}, {
    artifactSlots: [makeV2Slot('slot-1', 2, 'generating')],
  }));
  equal(conflict.kind, 'conflict', 'different content at the same work revision conflicts');
  equal(useWorkStore.getState().revisions['work-1'], 2, 'same-revision conflict cannot advance revision');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].state, 'reserved', 'same-revision conflict cannot overwrite projection');
});

test('V2 future schema event is parsed as read-only with ViewFutureSchemaError', () => {
  const raw = JSON.stringify(fixtFutureEvent);
  const result = parseWorkViewEvent(raw);
  ok(result.futureError !== null, 'future schema produces ViewFutureSchemaError');
  equal(result.event, null, 'future schema event is null');
  equal(result.futureError!.got, 999, 'future error reports the schema version');
});

test('V2 real fixtures: definition revision parses correctly', () => {
  const def = parseWorkDefinitionRevision(fixtDefRevision);
  equal(def.workId, 'work-v2-001', 'definition parser consumes the golden workId');
  ok(def.nodes.length > 0, 'definition parser consumes nodes');
});

test('V2 real fixtures: artifactSlot, input, patch fields match DTO contract', () => {
  const slot = parseArtifactSlot(fixtArtifactSlot);
  equal(slot.state, 'generating', 'slot parser consumes state');
  equal(slot.progress, 0.45, 'slot parser consumes progress');
  equal(slot.revision, 3, 'slot parser consumes revision');

  const input = parseWorkInput(fixtWorkInput);
  equal(input.state, 'submitted', 'input parser consumes state');
  ok(Array.isArray(input.value) && input.value.includes('类型安全'), 'input parser consumes choices');

  const patch = parseWorkPatchPreview(fixtPatchPreview);
  equal(patch.scope, 'block', 'patch parser consumes scope');
  equal(patch.requiresRerun, true, 'patch parser consumes requiresRerun');

  const result = fixtPatchResult as unknown as Record<string, unknown>;
  ok(result.duplicate === false, 'patch result is not duplicate');
  ok(typeof result.newRevision === 'number', 'patch result has newRevision');

  const receipt = fixtPatchReceipt as unknown as Record<string, unknown>;
  ok(receipt.operation === 'ApplyWorkPatch', 'receipt operation is ApplyWorkPatch');
  ok(typeof receipt.resultRevision === 'number', 'receipt has resultRevision');
});

test('V2 frontend fixture is byte-equivalent to Go golden and enters parser/store chain', () => {
  const goFixture = goEventGolden as unknown;
  equal(JSON.stringify(fixtGoEvent), JSON.stringify(goFixture), 'frontend event fixture matches the real Go golden');
  const parsed = parseWorkViewEvent(JSON.stringify(goFixture));
  ok(parsed.event !== null && parsed.futureError === null, 'real Go event golden passes the event parser');
  reset();
  equal(applyWorkViewEvent(parsed.event).kind, 'conflict', 'partial definition snapshot is rejected by the projection store');
  equal(useWorkStore.getState().revisions['work-v2-001'], undefined, 'partial snapshot cannot advance revision');
});

test('real FileWorkStore WorkViewV2 golden enters parser → fetch adapter → Store', async () => {
  reset();
  const parsed = parseWorkViewV2(structuredClone(goFullV2Golden));
  const port = new TestPort();
  port.fetch = async () => structuredClone(goFullV2Golden) as unknown as WorkViewV2;
  const adapter = new WorkControllerAdapter(port);
  equal((await adapter.recoverSnapshot(parsed.work.id)).kind, 'applied', 'real Go Get DTO applies through the fetch adapter');
  assertFullGoldenProjected(parsed, 'fetch');
  adapter.dispose();
});

test('real FileWorkStore WorkViewV2 golden enters parser → watch adapter → Store', () => {
  reset();
  const parsed = parseWorkViewV2(structuredClone(goFullV2Golden));
  const adapter = new WorkControllerAdapter(new TestPort());
  equal(adapter.applyEvent(fullGoldenSnapshotEvent('go-full-watch')).kind, 'applied', 'real Go payload applies through the watch adapter');
  assertFullGoldenProjected(parsed, 'watch');
  adapter.dispose();
});

test('real FileWorkStore WorkViewV2 golden enters parser → recover adapter → Store', async () => {
  reset();
  const parsed = parseWorkViewV2(structuredClone(goFullV2Golden));
  applySnapshot(makeView(parsed.work.id, 1, { name: 'stale projection' }));
  const port = new TestPort();
  port.recover = async (_workID, intent) => fullGoldenSnapshotEvent(
    `wv-resync-${parsed.work.id}-rev-${parsed.revision}-${intent.reason}-${intent.generation}`,
    { reason: intent.reason, authoritative: true, generation: intent.generation },
  );
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe(parsed.work.id);
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  assertFullGoldenProjected(parsed, 'recover');
  equal(adapter.getStatus(parsed.work.id).stream.kind, 'online', 'real Go recovery completes the subscription handshake');
  adapter.dispose();
});

test('real FileWorkStore golden missing artifactRefs fails fetch without mutating Store', async () => {
  reset();
  const valid = parseWorkViewV2(structuredClone(goFullV2Golden));
  equal(applyWorkViewEvent(fullGoldenSnapshotEvent('go-full-fetch-seed')).kind, 'applied', 'valid golden seeds authority');
  const port = new TestPort();
  port.fetch = async () => fullGoldenWithoutArtifactRefs() as unknown as WorkViewV2;
  const adapter = new WorkControllerAdapter(port);
  let error: unknown;
  try {
    await adapter.recoverSnapshot(valid.work.id);
  } catch (caught) {
    error = caught;
  }
  ok(error instanceof TypeError, 'fetch exposes the parser TypeError');
  ok(error.message.includes('missing required field "artifactRefs"'), 'fetch reports the missing field');
  equal(useWorkStore.getState().revisions[valid.work.id], valid.revision, 'failed fetch cannot advance revision');
  equal((useWorkStore.getState().artifactSlots[valid.work.id] ?? [])[0].artifactRefs.length, 0, 'failed fetch cannot replace refs');
  ok(adapter.getStatus(valid.work.id).snapshotError?.includes('missing required field "artifactRefs"'), 'fetch failure remains observable');
  adapter.dispose();
});

test('real FileWorkStore golden missing artifactRefs conflicts in delta and repeated events are inert', () => {
  reset();
  const valid = parseWorkViewV2(structuredClone(goFullV2Golden));
  equal(applyWorkViewEvent(fullGoldenSnapshotEvent('go-full-delta-seed')).kind, 'applied', 'valid golden seeds authority');
  const adapter = new WorkControllerAdapter(new TestPort());
  const malformed = fullGoldenMissingArtifactRefsEvent(
    'delta',
    'go-full-missing-refs-delta',
    valid.revision + 1,
    valid.revision,
  );
  const first = adapter.applyEvent(malformed);
  equal(first.kind, 'conflict', 'missing artifactRefs delta is an explicit conflict');
  const second = adapter.applyEvent(malformed);
  equal(second.kind, 'conflict', 'repeated malformed delta remains a conflict');
  equal(useWorkStore.getState().revisions[valid.work.id], valid.revision, 'malformed delta cannot advance revision');
  equal((useWorkStore.getState().artifactSlots[valid.work.id] ?? [])[0].artifactRefs.length, 0, 'malformed delta cannot replace refs');
  ok(adapter.getStatus(valid.work.id).eventError?.includes('missing required field "artifactRefs"'), 'delta parser conflict remains observable');
  adapter.dispose();
});

test('V2 watch conflict from missing artifactRefs triggers one deduplicated authoritative fetch', async () => {
  reset();
  const valid = parseWorkViewV2(structuredClone(goFullV2Golden));
  const port = new TestPort();
  port.fetch = async () => structuredClone(goFullV2Golden) as unknown as WorkViewV2;
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe(valid.work.id);
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  equal(port.fetchCount, 1, 'subscription performs its initial authoritative fetch');

  const malformed = fullGoldenMissingArtifactRefsEvent(
    'delta',
    'go-full-missing-refs-watch',
    valid.revision + 1,
    valid.revision,
  );
  port.emit(valid.work.id, malformed);
  port.emit(valid.work.id, malformed);
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));

  equal(port.fetchCount, 2, 'duplicate malformed watch events share one recovery fetch');
  equal(useWorkStore.getState().revisions[valid.work.id], valid.revision, 'watch conflict cannot advance revision');
  equal((useWorkStore.getState().artifactSlots[valid.work.id] ?? [])[0].artifactRefs.length, 0, 'authoritative null refs remain stable');
  adapter.dispose();
});

test('V2 watch conflict recovery failure is observable and explicit retry can recover', async () => {
  reset();
  const valid = parseWorkViewV2(structuredClone(goFullV2Golden));
  const port = new TestPort();
  port.fetch = async () => structuredClone(goFullV2Golden) as unknown as WorkViewV2;
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe(valid.work.id);
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));

  port.fetch = async () => { throw new Error('temporary authoritative fetch failure'); };
  const malformed = fullGoldenMissingArtifactRefsEvent(
    'delta',
    'go-full-missing-refs-watch-retry',
    valid.revision + 1,
    valid.revision,
  );
  port.emit(valid.work.id, malformed);
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  ok(adapter.getStatus(valid.work.id).snapshotError?.includes('temporary authoritative fetch failure'), 'failed automatic recovery is observable');
  equal(port.fetchCount, 2, 'failed automatic recovery performs one fetch');

  port.emit(valid.work.id, malformed);
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  equal(port.fetchCount, 2, 'repeated malformed event cannot start an unbounded retry loop');

  port.recover = async (_workID, intent) => fullGoldenSnapshotEvent(
    `wv-resync-${valid.work.id}-rev-${valid.revision}-${intent.reason}-${intent.generation}`,
    { reason: intent.reason, authoritative: true, generation: intent.generation },
  );
  adapter.retrySubscription(valid.work.id);
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  const retryStatus = adapter.getStatus(valid.work.id);
  ok(retryStatus.stream.kind === 'online', `explicit subscription retry restores the watch: ${JSON.stringify(retryStatus)}`);
  equal(retryStatus.snapshotError, null, 'successful explicit retry clears the recovery error');
  equal(useWorkStore.getState().revisions[valid.work.id], valid.revision, 'explicit retry preserves authoritative revision');
  adapter.dispose();
});

test('real FileWorkStore golden missing artifactRefs fails authoritative recover without mutation', async () => {
  reset();
  const valid = parseWorkViewV2(structuredClone(goFullV2Golden));
  equal(applyWorkViewEvent(fullGoldenSnapshotEvent('go-full-recover-seed')).kind, 'applied', 'valid golden seeds authority');
  const port = new TestPort();
  port.recover = async (_workID, intent) => fullGoldenMissingArtifactRefsEvent(
    'snapshot',
    `go-full-missing-refs-${intent.reason}-${intent.generation}`,
    valid.revision,
    0,
    { reason: intent.reason, authoritative: true, generation: intent.generation },
  );
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe(valid.work.id);
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));

  equal(useWorkStore.getState().revisions[valid.work.id], valid.revision, 'failed recover cannot advance revision');
  equal((useWorkStore.getState().artifactSlots[valid.work.id] ?? [])[0].artifactRefs.length, 0, 'failed recover cannot replace refs');
  ok(adapter.getStatus(valid.work.id).snapshotError?.includes('missing required field "artifactRefs"'), 'recover conflict remains observable and retryable');
  equal(adapter.getStatus(valid.work.id).stream.kind, 'offline', 'failed recover exposes retryable subscription state');
  adapter.dispose();
});

test('V2 cross-work V2 fields rejected', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  // Emit a V2 delta for work-1 but with artifactSlots pointing to a different workId.
  const foreignSlot = makeV2Slot('slot-1', 1);
  foreignSlot.workId = 'work-2';
  const result = applyWorkViewEvent(v2Delta('v2d-2', 2, 1, {}, { artifactSlots: [foreignSlot] }));
  equal(result.kind, 'conflict', 'cross-work V2 slot is rejected');
  equal(useWorkStore.getState().revisions['work-1'], 1, 'cross-work V2 payload cannot advance revision');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 0, 'cross-work V2 payload cannot enter projection');
});

test('V2 item tombstones block late resurrection until authoritative snapshot', () => {
  reset();
  applyWorkViewEvent(v2Snapshot(makeView('work-1', 1), {
    artifactSlots: [makeV2Slot('slot-1', 1, 'ready')],
  }));
  equal(applyWorkViewEvent(v2Delta('v2-remove-2', 2, 1, {}, {
    removedArtifactSlotIds: ['slot-1'],
  })).kind, 'applied', 'slot removal applies');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 0, 'removed slot leaves projection');

  equal(applyWorkViewEvent(v2Delta('v2-late-3', 3, 2, {}, {
    artifactSlots: [makeV2Slot('slot-1', 99, 'generating')],
  })).kind, 'applied', 'late payload is consumed without reviving tombstone');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 0, 'late delta cannot revive removed slot');

  equal(applyWorkViewEvent(v2Snapshot(makeView('work-1', 4), {
    artifactSlots: [makeV2Slot('slot-1', 100, 'ready')],
  })).kind, 'applied', 'authoritative snapshot may restore the slot');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].state, 'ready', 'restored slot comes from authority');
});

test('V2 authoritative snapshot replaces absent server items and preserves all UI-local text', () => {
  reset();
  useWorkUIStore.getState().setDraft('work-1', 'front', 'input draft');
  useWorkUIStore.getState().setDraft('work-1', 'back', 'discussion text');
  useWorkUIStore.getState().setExpanded('work-1', 'front', 'block-1', true);
  applyWorkViewEvent(v2Snapshot(makeView('work-1', 1), {
    artifactSlots: [makeV2Slot('slot-1', 1), makeV2Slot('slot-2', 1)],
    tasks: [makeV2Task('task-1', 'run-1', 'running')],
    inputs: [makeV2Input('input-1', 1, 'draft')],
  }));
  equal(applyWorkViewEvent(v2Snapshot(makeView('work-1', 2), {
    artifactSlots: [makeV2Slot('slot-2', 2, 'ready')],
  })).kind, 'applied', 'higher authoritative snapshot applies');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 1, 'absent slot is removed by authoritative replacement');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].id, 'slot-2', 'remaining slot identity is stable');
  equal((useWorkStore.getState().v2Tasks['work-1'] ?? []).length, 0, 'omitted empty tasks clear old server tasks');
  equal((useWorkStore.getState().v2Inputs['work-1'] ?? []).length, 0, 'omitted empty inputs clear old server inputs');
  const card = selectCardState(useWorkUIStore.getState().cardByWork, 'work-1');
  equal(card?.faces.front.draft, 'input draft', 'input draft survives snapshot replacement');
  equal(card?.faces.back.draft, 'discussion text', 'discussion text survives snapshot replacement');
  equal(card?.faces.front.expanded['block-1'], true, 'expanded state survives snapshot replacement');
});

test('V2 malformed partial snapshot is observable and cannot mutate projection', () => {
  reset();
  applySnapshot(makeView('work-1', 1));
  const partial = throughParser(event('snapshot', 'v2-partial-2', 2, 0, {
    schemaVersion: 2,
    revision: 2,
    artifactSlots: [],
  }));
  const result = applyWorkViewEvent(partial);
  equal(result.kind, 'conflict', 'missing V2 work body conflicts');
  equal(useWorkStore.getState().revisions['work-1'], 1, 'partial snapshot cannot advance revision');
  ok(useWorkStore.getState().conflicts['work-1']?.reason.includes('expected object'), 'partial failure reason remains observable');
});

test('V2 controller fetch parses snapshot before the single Store projection', async () => {
  reset();
  const port = new TestPort();
  const v2View = {
    ...makeView('work-1', 2),
    schemaVersion: 2,
    artifactSlots: [makeV2Slot('slot-1', 1, 'generating')],
    tasks: [makeV2Task('task-1', 'run-1', 'running')],
    inputs: [makeV2Input('input-1', 1, 'requested')],
  } as WorkViewV2;
  port.fetch = async () => structuredClone(v2View);
  const adapter = new WorkControllerAdapter(port);
  equal((await adapter.recoverSnapshot('work-1')).kind, 'applied', 'parsed V2 fetch enters Store');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].id, 'slot-1', 'Store is the V2 snapshot projection');
  equal(adapter.getStatus('work-1').snapshotError, null, 'successful V2 fetch clears recovery error');
  adapter.dispose();
});

test('V2 subscribed gap automatically fetches and applies an authoritative snapshot', async () => {
  reset();
  const port = new TestPort();
  const recovered = {
    ...makeView('work-1', 3, { name: 'v2 recovered' }),
    schemaVersion: 2,
    artifactSlots: [makeV2Slot('slot-recovered', 3, 'ready')],
    tasks: [],
    inputs: [],
  } as WorkViewV2;
  port.fetch = async () => port.fetchCount === 1 ? makeView('work-1', 1) : structuredClone(recovered);
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe('work-1');
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  equal(port.fetchCount, 1, 'subscription installs watch before initial snapshot');

  port.emit('work-1', v2Delta('v2-gap-3', 3, 2, {}, {
    artifactSlots: [makeV2Slot('slot-gap', 1, 'generating')],
  }));
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  equal(port.fetchCount, 2, 'gap automatically triggers one authoritative fetch');
  equal(useWorkStore.getState().gaps['work-1'], undefined, 'authoritative V2 fetch clears the gap');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].id, 'slot-recovered', 'only fetched authority enters Store');
  equal(adapter.getStatus('work-1').snapshotError, null, 'successful automatic recovery is observable');
  adapter.dispose();
});

test('V2 controller keeps future watch schema read-only and observable', () => {
  reset();
  const adapter = new WorkControllerAdapter(new TestPort());
  const result = adapter.applyEvent(structuredClone(fixtFutureEvent) as WorkViewEvent);
  equal(result.kind, 'ignored', 'future schema does not enter Store');
  ok(adapter.getStatus('work-future-001').eventError?.includes('read-only'), 'future schema read-only state is observable');
  equal(useWorkStore.getState().revisions['work-future-001'], undefined, 'future schema cannot overwrite projection');
  adapter.dispose();
});

test('V2 future fetch returns observed/unsupported and preserves the Store projection', async () => {
  reset();
  applySnapshot(makeView('work-1', 7, { name: 'current projection' }));
  const before = structuredClone(useWorkStore.getState().works['work-1']);
  const port = new TestPort();
  const future = {
    schemaVersion: 3,
    revision: 99,
    work: { id: 'work-1', name: 'must not apply', updatedAt: '2030-01-01T00:00:00Z' },
    futureOnly: { mode: 'unknown' },
  };
  port.fetch = async () => future as unknown as WorkViewV2;
  const adapter = new WorkControllerAdapter(port);
  const result = await adapter.recoverSnapshot('work-1');
  equal(result.kind, 'unsupported', 'future fetch is observed as unsupported');
  if (result.kind !== 'unsupported') throw new Error('future fetch result must be unsupported');
  equal(result.observed, true, 'future fetch is explicitly observed');
  equal(result.schemaVersion, 3, 'future fetch reports its schema version');
  equal(result.raw, JSON.stringify(future), 'future fetch retains raw data');
  equal(useWorkStore.getState().revisions['work-1'], 7, 'future fetch cannot advance Store revision');
  equal(JSON.stringify(useWorkStore.getState().works['work-1']), JSON.stringify(before), 'future fetch cannot overwrite Store data');
  equal(adapter.getStatus('work-1').unsupportedView?.source, 'fetch', 'future fetch is observable through adapter status');
  adapter.dispose();
});

test('V2 future recovery returns observed/unsupported and preserves the Store projection', async () => {
  reset();
  applySnapshot(makeView('work-1', 7, { name: 'current projection' }));
  const before = structuredClone(useWorkStore.getState().works['work-1']);
  const port = new TestPort();
  const futureRecovery = {
    schemaVersion: 3,
    type: 'snapshot',
    workID: 'work-1',
    eventID: 'future-recovery',
    revision: 99,
    baseRevision: 0,
    requestID: 'future-recovery',
    object: { kind: 'work', id: 'work-1' },
    payload: { schemaVersion: 3, revision: 99, work: { id: 'work-1', name: 'must not apply' } },
    createdAt: '2030-01-01T00:00:00Z',
  };
  port.recover = async () => futureRecovery;
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe('work-1');
  await new Promise<void>((resolveWait) => setTimeout(resolveWait, 0));
  equal(useWorkStore.getState().revisions['work-1'], 7, 'future recovery cannot advance Store revision');
  equal(JSON.stringify(useWorkStore.getState().works['work-1']), JSON.stringify(before), 'future recovery cannot overwrite Store data');
  equal(adapter.getStatus('work-1').unsupportedView?.source, 'recover', 'future recovery is observable through adapter status');
  equal(adapter.getStatus('work-1').unsupportedView?.raw, JSON.stringify(futureRecovery), 'future recovery retains raw data');
  equal(adapter.getStatus('work-1').stream.kind, 'online', 'installed watch remains usable in read-only mode');
  adapter.dispose();
});

test('V2 restore: removeProjection clears V2 fields, re-snapshot restores', () => {
  reset();
  applyWorkViewEvent(v2Snapshot(makeView('work-1', 2), {
    artifactSlots: [makeV2Slot('slot-1', 1, 'ready')],
    tasks: [makeV2Task('task-1', 'run-1', 'running')],
  }));
  useWorkStore.getState().removeProjection('work-1');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? []).length, 0, 'V2 slots cleared on removeProjection');
  equal((useWorkStore.getState().v2Tasks['work-1'] ?? []).length, 0, 'V2 tasks cleared on removeProjection');
  equal(useWorkStore.getState().removed['work-1'], true, 'removed flag set');
  // Restore via higher-revision snapshot.
  equal(applyWorkViewEvent(v2Snapshot(makeView('work-1', 4), {
    artifactSlots: [makeV2Slot('slot-2', 2, 'generating')],
  })).kind, 'applied', 'newer snapshot restores');
  equal((useWorkStore.getState().artifactSlots['work-1'] ?? [])[0].id, 'slot-2', 'restored V2 slot visible');
  equal(useWorkStore.getState().removed['work-1'], false, 'removed flag cleared on restore');
});

test('V2 UI ephemeral state survives V2 snapshot + delta chain', () => {
  reset();
  useWorkUIStore.getState().setDraft('work-1', 'front', 'my draft');
  useWorkUIStore.getState().setExpanded('work-1', 'front', 'block-1', true);
  applyWorkViewEvent(v2Snapshot(makeView('work-1', 2), {
    artifactSlots: [makeV2Slot('slot-1', 1)],
  }));
  applyWorkViewEvent(v2Delta('v2d-3', 3, 2, {}, { tasks: [makeV2Task('task-1', 'run-1', 'running')] }));
  const card = selectCardState(useWorkUIStore.getState().cardByWork, 'work-1');
  equal(card?.faces.front.draft, 'my draft', 'draft survives V2 snapshot + delta chain');
  equal(card?.faces.front.expanded['block-1'], true, 'expanded state survives V2 chain');
});

// ── V2 candidate → apply authoritative revision ──────────────────────────

test('candidate committed: fetch stale rev8 first, then rev9; apply uses expectedRevision=9', async () => {
  reset();
  const workID = 'work-cand-auth';
  // Seed store at revision 8.
  const rev8 = makeView(workID, 8);
  applySnapshot(rev8);
  equal(useWorkStore.getState().revisions[workID], 8, 'store at rev8');

  const port = new TestPort();
  // Simulate backend: candidate bumps to rev9, fetch first returns stale rev8 then fresh rev9.
  port.candidateNext = {
    candidate: undefined,
    revision: 9,
    duplicate: false,
    committed: true,
    recoverable: false,
  };
  let fetchCall = 0;
  port.fetchDeferreds = [
    async () => { fetchCall++; return makeView(workID, 8); },  // stale
    async () => { fetchCall++; return makeView(workID, 9); },  // authoritative
  ];

  const adapter = new WorkControllerAdapter(port);
  const result = await adapter.createCandidateRevision({
    workId: workID,
    intent: 'test',
    baseDefinitionRevision: 1,
    expectedRevision: 8,
    requestId: 'cand-auth-1',
  });
  equal(result.committed, true, 'candidate committed');
  equal(result.recoverable, false, 'not recoverable — snapshot caught up');
  ok(fetchCall >= 2, 'fetchSnapshot called at least twice (stale + authoritative)');
  equal(useWorkStore.getState().revisions[workID], 9, 'store revision reaches 9');

  // Simulate subsequent applyDefinition: port tracks expectedRevision.
  await adapter.applyDefinition({
    workId: workID,
    revision: 2,
    expectedRevision: useWorkStore.getState().revisions[workID] ?? -1,
    requestId: 'apply-auth-1',
  });
  equal(port.applyInputs.length, 1, 'applyDefinition called');
  equal(port.applyInputs[0].expectedRevision, 9, 'apply expectedRevision = 9 (authoritative)');
  adapter.dispose();
});

test('candidate duplicate/replay: refreshes to revision 9', async () => {
  reset();
  const workID = 'work-cand-replay';
  const rev8 = makeView(workID, 8);
  applySnapshot(rev8);

  const port = new TestPort();
  port.candidateNext = {
    candidate: undefined,
    revision: 9,
    duplicate: true,       // replay
    committed: true,
    recoverable: false,
  };
  port.fetchDeferreds = [
    async () => makeView(workID, 8),
    async () => makeView(workID, 9),
  ];

  const adapter = new WorkControllerAdapter(port);
  const result = await adapter.createCandidateRevision({
    workId: workID,
    intent: 'test',
    baseDefinitionRevision: 1,
    expectedRevision: 8,
    requestId: 'cand-replay-1',
  });
  equal(result.duplicate, true, 'duplicate/replay');
  equal(result.committed, true, 'replay committed');
  equal(useWorkStore.getState().revisions[workID], 9, 'store reaches rev9 after replay');
  adapter.dispose();
});

test('candidate committed-recovery (ACK loss): fetches stale then fresh, returns with original transportError', async () => {
  reset();
  const workID = 'work-cand-recovery';
  const rev8 = makeView(workID, 8);
  applySnapshot(rev8);

  const port = new TestPort();
  const origTransportError = {
    code: 'committed_recovery',
    message: 'ACK lost; candidate persisted',
    operation: 'CreateCandidateRevision',
    workId: workID,
    requestId: 'cand-recovery-1',
    committed: true,
    recoverable: true,
  };
  port.candidateNext = {
    candidate: undefined,
    revision: 9,
    duplicate: false,
    committed: true,
    recoverable: true,
    transportError: origTransportError,
  };
  port.fetchDeferreds = [
    async () => makeView(workID, 8),  // stale
    async () => makeView(workID, 9),  // authoritative
  ];

  const adapter = new WorkControllerAdapter(port);
  const result = await adapter.createCandidateRevision({
    workId: workID,
    intent: 'test',
    baseDefinitionRevision: 1,
    expectedRevision: 8,
    requestId: 'cand-recovery-1',
  });
  equal(result.committed, true, 'committed-recovery: committed');
  equal(result.recoverable, true, 'committed-recovery: recoverable preserved');
  ok(result.transportError != null, 'committed-recovery: transportError preserved');
  equal(result.transportError!.code, 'committed_recovery', 'committed-recovery: original code survived');
  equal(useWorkStore.getState().revisions[workID], 9, 'store reaches rev9 after recovery fetch');
  ok(port.fetchCount >= 2, 'fetchSnapshot called at least twice (stale + authoritative)');
  adapter.dispose();
});

test('late rev8 snapshot does not roll back rev9 store', () => {
  reset();
  const workID = 'work-late-snap';
  // Store at rev9.
  const rev9 = makeView(workID, 9);
  applySnapshot(rev9);
  equal(useWorkStore.getState().revisions[workID], 9, 'store at rev9');

  // Apply a late rev8 snapshot.
  const late = snapshot(makeView(workID, 8), 'late-snap-8');
  const result = applyWorkViewEvent(late);
  equal(result.kind, 'stale', 'late rev8 snapshot is stale');
  equal(useWorkStore.getState().revisions[workID], 9, 'revision stays at 9');
  equal(useWorkStore.getState().works[workID]?.revision, 9, 'view stays at rev9');
});

test('candidate committed: fetch always stale → committed+recoverable error', async () => {
  reset();
  const workID = 'work-cand-stale';
  const rev8 = makeView(workID, 8);
  applySnapshot(rev8);

  const port = new TestPort();
  port.candidateNext = {
    candidate: undefined,
    revision: 9,
    duplicate: false,
    committed: true,
    recoverable: false,
  };
  // All fetches return stale rev8.
  port.fetchDeferreds = [
    async () => makeView(workID, 8),
    async () => makeView(workID, 8),
    async () => makeView(workID, 8),
    async () => makeView(workID, 8),
    async () => makeView(workID, 8),
  ];

  const adapter = new WorkControllerAdapter(port);
  let caught: Error & { code?: string; committed?: boolean; recoverable?: boolean } | null = null;
  try {
    await adapter.createCandidateRevision({
      workId: workID,
      intent: 'test',
      baseDefinitionRevision: 1,
      expectedRevision: 8,
      requestId: 'cand-stale-1',
    });
  } catch (err) {
    caught = err as Error & { code?: string; committed?: boolean; recoverable?: boolean };
  }
  ok(caught != null, 'createCandidateRevision must throw when snapshot never catches up');
  equal(caught!.code, 'snapshot_stale', 'error code snapshot_stale');
  ok(caught!.committed === true, 'committed=true on error');
  ok(caught!.recoverable === true, 'recoverable=true on error');
  equal(useWorkStore.getState().revisions[workID], 8, 'store stays at rev8 — refresh failed');
  adapter.dispose();
});

test('candidate revision_conflict: controller refreshes, retry uses new requestId', async () => {
  reset();
  const workID = 'work-cand-conflict';
  const rev8 = makeView(workID, 8);
  applySnapshot(rev8);

  const port = new TestPort();
  // First call: revision_conflict.
  port.candidateNext = {
    revision: 0,
    duplicate: false,
    committed: false,
    recoverable: true,
    transportError: {
      code: 'revision_conflict',
      message: 'revision conflict',
      operation: 'CreateCandidateRevision',
      workId: workID,
      requestId: 'cand-conflict-1',
      committed: false,
      recoverable: true,
    },
  };
  // After conflict, fetch returns authoritative rev9.
  port.fetchDeferreds = [
    async () => makeView(workID, 9),
  ];

  const adapter = new WorkControllerAdapter(port);
  let conflictThrown = false;
  try {
    await adapter.createCandidateRevision({
      workId: workID,
      intent: 'test',
      baseDefinitionRevision: 1,
      expectedRevision: 8,
      requestId: 'cand-conflict-1',
    });
  } catch (err) {
    conflictThrown = true;
    equal((err as { code?: string }).code, 'revision_conflict', 'error code revision_conflict');
  }
  ok(conflictThrown, 'revision_conflict throws');
  equal(useWorkStore.getState().revisions[workID], 9, 'store refreshed to rev9 after conflict');

  // Retry with new requestId.
  port.candidateNext = {
    candidate: undefined,
    revision: 10,
    duplicate: false,
    committed: true,
    recoverable: false,
  };
  port.fetchDeferreds = [
    async () => makeView(workID, 10),
  ];
  const retry = await adapter.createCandidateRevision({
    workId: workID,
    intent: 'test',
    baseDefinitionRevision: 1,
    expectedRevision: 9,   // uses refreshed revision
    requestId: 'cand-conflict-2', // new requestId
  });
  equal(retry.committed, true, 'retry succeeds');
  equal(retry.revision, 10, 'retry revision 10');
  equal(useWorkStore.getState().revisions[workID], 10, 'store reaches rev10 after retry');
  adapter.dispose();
});

test('submit input exposes authoritative rejection instead of generic unconfirmed error', async () => {
  const port: WorkControllerPort = new TestPort();
  port.submitWorkInput = async () => ({
    revision: 12,
    duplicate: false,
    committed: false,
    recoverable: false,
    error: 'definition revision mismatch: expected 7, current 0',
  });
  const adapter = new WorkControllerAdapter(port);
  let caught: Error | null = null;
  try {
    await adapter.submitWorkInput({
      workId: 'work-submit-error',
      runId: 'run-1',
      taskId: 'task-1',
      blockId: 'block-1',
      inputId: 'input-1',
      value: 20000,
      definitionRevision: 7,
      inputRevision: 1,
      expectedRevision: 12,
      requestId: 'submit-error-1',
    });
  } catch (error) {
    caught = error as Error;
  }
  ok(caught, 'submit rejection must throw');
  equal(
    caught.message,
    'definition revision mismatch: expected 7, current 0',
    'submit rejection keeps backend detail',
  );
  adapter.dispose();
});

test('watch event same-revision snapshot conflict triggers auto-recovery', async () => {
  reset();
  const port = new TestPort();
  port.fetch = async () => makeView('work-1', 42, { name: 'stale' });
  let recoveryCount = 0;
  port.recover = async (workID, intent) => {
    recoveryCount++;
    equal(intent.reason, 'retry', 'automatic conflict recovery uses the authoritative retry handshake');
    return retryResync(makeView(workID, 42, { name: 'recovered' }), intent);
  };
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe('work-1');
  await new Promise<void>((r) => setTimeout(r, 0));
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'stale', 'init snapshot applied');
  port.emit('work-1', snapshot(makeView('work-1', 42, { name: 'recovered' }), 'ss'));
  await new Promise<void>((r) => setTimeout(r, 10));
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'recovered',
    'auto-recovery applied the authoritative snapshot');
  equal(recoveryCount, 1, 'one conflict creates one authoritative recovery');
  equal(port.fetchCount, 1, 'automatic recovery does not overwrite through an ordinary snapshot fetch');
  equal(port.listeners.get('work-1')?.size, 1, 'automatic recovery replaces rather than duplicates the Watch');
  equal(adapter.getStatus('work-1').eventError, null, 'eventError cleared after recovery');
  adapter.dispose();
});

test('permanent fetch failure keeps error observable', async () => {
  reset();
  const port = new TestPort();
  port.fetch = async () => makeView('work-1', 5, { name: 'stale' });
  port.recover = async () => { throw new Error('network unreachable'); };
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe('work-1');
  await new Promise<void>((r) => setTimeout(r, 0));
  port.emit('work-1', snapshot(makeView('work-1', 5, { name: 'different' }), 'fail-rec'));
  await new Promise<void>((r) => setTimeout(r, 10));
  ok(adapter.getStatus('work-1').snapshotError?.includes('network unreachable'),
    'permanent fetch failure remains visible as snapshotError');
  equal(adapter.getStatus('work-1').stream.kind, 'offline', 'failed automatic recovery marks the stream offline');
  equal(selectWork(useWorkStore.getState().works, 'work-1')?.name, 'stale',
    'failed automatic recovery preserves the last usable projection');
  adapter.dispose();
});

let passed = 0;
for (const entry of tests) {
  await entry.run();
  passed++;
}
console.log(`Work store contract: ${passed}/${tests.length} tests passed`);
