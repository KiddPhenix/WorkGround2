import assert from 'node:assert/strict';

import { applySnapshot, useWorkStore } from '../work/store.js';
import { WorkControllerAdapter } from '../work/controller.js';
import type { CornerstoneMutationResult, WorkView, WorkViewEvent } from '../work/types.js';
import type { WorkControllerPort, WorkControllerStatus } from '../work/controller.js';

// ── Helpers ─────────────────────────────────────────────────────────────────

function makeView(workID: string, revision: number, overrides?: Partial<WorkView['work']>): WorkView {
  return {
    schemaVersion: 1,
    work: {
      schemaVersion: 1, id: workID, name: 'Test', state: 'draft', archiveState: 'active',
      blueprintRef: { id: 'bp', schemaVersion: 1, version: 1 },
      definitionSnapshot: {
        schemaVersion: 1, revision: 1, promptTemplate: '',
        blueprintRef: { id: 'bp', schemaVersion: 1, version: 1 },
        workflow: { stages: [] }, blockSpecs: [], digest: 'sha256:abc',
      },
      blocks: [], placements: [], prompt: '', cornerstones: [], runs: [],
      createdWith: { workSchemaVersion: 1, eventSchemaVersion: 1, rendererSetVersion: 1 },
      createdAt: '2026-07-24T10:00:00Z', updatedAt: '2026-07-24T10:00:00Z',
      ...overrides,
    },
    revision,
  };
}

function okMutation(view: WorkView): CornerstoneMutationResult {
  return { ok: true, workView: view };
}

function getStatus(adapter: WorkControllerAdapter, workID: string): WorkControllerStatus {
  return adapter.getStatus(workID);
}

function resetStore(): void {
  useWorkStore.getState().clearAll();
}

function sleep(ms: number): Promise<void> { return new Promise(r => setTimeout(r, ms)); }

function gapEvent(workID: string, revision: number, baseRevision: number): WorkViewEvent {
  return {
    schemaVersion: 1, type: 'delta', workID,
    eventID: `gap-${workID}-${revision}`, revision, baseRevision,
    requestID: `gap-req-${workID}`, object: { kind: 'work', id: workID },
    payload: { state: 'running' },
    createdAt: '2026-07-24T10:00:00Z',
  };
}

type OnEvent = (event: WorkViewEvent) => void;

interface MockPort extends WorkControllerPort {
  _capturedOnEvent: OnEvent | null;
  _setFetchSnapshot: (fn: (() => Promise<unknown>) | null) => void;
  _setFetchRecoverySnapshot: (fn: (() => Promise<unknown>) | null) => void;
}

function makeMockPort(): MockPort {
  let fetchFn: (() => Promise<unknown>) | null = null;
  let recoveryFn: (() => Promise<unknown>) | null = null;
  let captured: OnEvent | null = null;

  const port = {
    _capturedOnEvent: null as OnEvent | null,
    subscribe: (_wid: string, onEvent: OnEvent) => {
      captured = onEvent;
      port._capturedOnEvent = onEvent;
      return { ready: Promise.resolve(), unsubscribe: () => { captured = null; } };
    },
    fetchSnapshot: async (_wid: string) => {
      if (fetchFn) return fetchFn();
      throw new Error('fetchSnapshot not configured');
    },
    fetchRecoverySnapshot: async (_wid: string, _intent: unknown) => {
      if (recoveryFn) return recoveryFn();
      throw new Error('fetchRecoverySnapshot not configured');
    },
    _setFetchSnapshot: (fn: (() => Promise<unknown>) | null) => { fetchFn = fn; },
    _setFetchRecoverySnapshot: (fn: (() => Promise<unknown>) | null) => { recoveryFn = fn; },
    readUIPreference: async () => null,
    writeUIPreference: async () => {},
  };
  return port;
}

// ── A: Watch event auto-restarts with authoritative recovery; a later retry clears failure ──
{
  resetStore();
  const workID = 'w-watch-retry';
  const port = makeMockPort();

  let fetchCalls = 0;
  port._setFetchSnapshot(() => {
    fetchCalls++;
    if (fetchCalls === 1) {
      // Initial hydration succeeds.
      return Promise.resolve(makeView(workID, 3, { name: 'Hydrated' }));
    }
    return Promise.resolve(makeView(workID, 3, { name: 'Hydrated' }));
  });

  let recoveryCalls = 0;
  port._setFetchRecoverySnapshot(() => {
    recoveryCalls++;
    if (recoveryCalls === 1) {
      return Promise.reject(new Error('watch-recovery-network-down'));
    }
    const gen = 999;
    return Promise.resolve({
      schemaVersion: 1, type: 'snapshot' as const, workID,
      eventID: `wv-resync-${workID}-rev-5-retry-${gen}`,
      revision: 5, baseRevision: 0,
      requestID: `retry-req:${workID}`,
      object: { kind: 'work' as const, id: workID },
      resync: { reason: 'retry' as const, authoritative: true, generation: gen },
      payload: makeView(workID, 5, { name: 'RetryRecovered' }),
      createdAt: '2026-07-24T10:00:00Z',
    });
  });

  const adapter = new WorkControllerAdapter(port);

  // Subscribe triggers full handshake: initial hydration fetchSnapshot → revision 3.
  adapter.subscribe(workID);
  await sleep(300);

  let status = getStatus(adapter, workID);
  assert.equal(status.stream.kind, 'online', `initial hydration failed: stream=${status.stream.kind}`);
  assert.equal(status.snapshotError, null);
  assert.equal(status.eventError, null);

  // Store must have revision 3.
  const initialRev = useWorkStore.getState().revisions[workID];
  assert.equal(initialRev, 3, `expected revision 3 after hydration, got ${initialRev}`);

  // Store at rev 3; baseRevision 2 creates a gap and starts a fresh Watch plus
  // an authoritative retry snapshot. The first authoritative request fails.
  assert.ok(port._capturedOnEvent, 'subscribe must capture onEvent');
  port._capturedOnEvent!(gapEvent(workID, 4, 2));

  // Wait for the automatic replacement subscription to settle.
  await sleep(300);

  status = getStatus(adapter, workID);
  assert.equal(status.stream.kind, 'offline', 'watch-triggered recovery failure must set stream offline');
  assert.ok(status.snapshotError !== null, 'snapshotError must be set');
  assert.match(status.snapshotError!, /watch-recovery-network-down/);

  // Retry: adapter.retrySubscription triggers new generation with reason='retry'.
  // This goes through recoverSubscriptionSnapshot → fetchRecoverySnapshot → succeeds.
  adapter.retrySubscription(workID);
  await sleep(300);

  status = getStatus(adapter, workID);
  assert.equal(status.snapshotError, null, 'retry must clear snapshotError');
  assert.equal(status.eventError, null, 'retry must clear eventError');
  const retryRev = useWorkStore.getState().revisions[workID];
  assert.equal(retryRev, 5, `retry must converge to revision 5, got ${retryRev}`);
  assert.ok(recoveryCalls >= 1, `fetchRecoverySnapshot must have been called, got ${recoveryCalls}`);
}

// ── B: event-first — Watch event lands, then same-revision mutation converges ──
{
  resetStore();
  const workID = 'w-eventfirst';
  const port = makeMockPort();

  let fetchCalls = 0;
  const authView = makeView(workID, 5, { name: 'Authoritative' });
  port._setFetchSnapshot(() => {
    fetchCalls++;
    if (fetchCalls === 1) {
      // Initial hydration: seed revision 3.
      return Promise.resolve(makeView(workID, 3, { name: 'Initial' }));
    }
    // Recovery returns authoritative revision 5.
    return Promise.resolve(authView);
  });
  port._setFetchRecoverySnapshot(() => {
    const gen = 999;
    return Promise.resolve({
      schemaVersion: 1, type: 'snapshot' as const, workID,
      eventID: `wv-resync-${workID}-rev-5-retry-${gen}`,
      revision: 5, baseRevision: 0,
      requestID: `retry-req:${workID}`,
      object: { kind: 'work' as const, id: workID },
      resync: { reason: 'retry' as const, authoritative: true, generation: gen },
      payload: authView,
      createdAt: '2026-07-24T10:00:00Z',
    });
  });

  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe(workID);
  await sleep(300);

  assert.equal(getStatus(adapter, workID).stream.kind, 'online');
  const revBefore = useWorkStore.getState().revisions[workID];
  assert.equal(revBefore, 3);

  // Watch event arrives first: the gap replaces the Watch and applies an
  // authoritative retry snapshot at revision 5.
  assert.ok(port._capturedOnEvent);
  port._capturedOnEvent!(gapEvent(workID, 4, 2));

  // Let the replacement subscription settle.
  await sleep(300);

  // Store must have converged to revision 5 after recovery.
  const revAfter = useWorkStore.getState().revisions[workID];
  assert.equal(revAfter, 5, `event-first recovery must converge to revision 5, got ${revAfter}`);

  // Now mutation response arrives late: same revision 3, different content.
  // applyMutationResult → applyMutationView at rev 3 → store already at 5 → stale/conflict
  // → recoverSnapshot → fetchSnapshot returns revision 5 again → duplicate.
  await adapter.applyMutationResult(workID, okMutation(makeView(workID, 3, { name: 'LateMutated' })));

  const status = getStatus(adapter, workID);
  assert.equal(status.eventError, null, 'late mutation after Watch recovery must not leave eventError');
  assert.equal(status.snapshotError, null, 'late mutation after Watch recovery must not leave snapshotError');

  // Store must still be at the authoritative revision 5, name 'Authoritative'.
  const finalRev = useWorkStore.getState().revisions[workID];
  assert.equal(finalRev, 5, `final revision must be 5, got ${finalRev}`);
  assert.equal(useWorkStore.getState().works[workID]?.work.name, 'Authoritative', 'store must have authoritative content');
}

// ── C: mutation conflict → authoritative converges (direct mutationResult path) ──
{
  resetStore();
  const workID = 'w-mut-converge';
  applySnapshot(makeView(workID, 3, { name: 'Original' }));

  const port = makeMockPort();
  port._setFetchSnapshot(() => Promise.resolve(makeView(workID, 5, { name: 'Recovered' })));
  const adapter = new WorkControllerAdapter(port);

  // Mutation at same revision 3 but different name → conflict → recoverSnapshot.
  await adapter.applyMutationResult(workID, okMutation(makeView(workID, 3, { name: 'Mutated' })));

  const status = getStatus(adapter, workID);
  assert.equal(status.eventError, null, 'conflict→recover must clear eventError');
  assert.equal(status.snapshotError, null);
  assert.equal(useWorkStore.getState().revisions[workID], 5);
}

// ── D: duplicate mutation does not produce persistent eventError ────────────
{
  resetStore();
  const workID = 'w-duplicate';
  const v = makeView(workID, 2, { name: 'Dup' });
  applySnapshot(v);

  const port = makeMockPort();
  port._setFetchSnapshot(() => Promise.resolve(v));
  const adapter = new WorkControllerAdapter(port);

  // applyMutationView: same view at same revision → duplicate (not conflict/gap) → returns true.
  await adapter.applyMutationResult(workID, okMutation(makeView(workID, 2, { name: 'Dup' })));

  const status = getStatus(adapter, workID);
  assert.equal(status.eventError, null, 'duplicate mutation must not set eventError');
}

// ── E: mutation mismatch → recovery clears error ────────────────────────────
{
  resetStore();
  const workID = 'w-mismatch';
  applySnapshot(makeView(workID, 1));

  const port = makeMockPort();
  port._setFetchSnapshot(() => Promise.resolve(makeView(workID, 2)));
  const adapter = new WorkControllerAdapter(port);

  await adapter.applyMutationResult(workID, okMutation(makeView('other-w', 1)));
  assert.equal(getStatus(adapter, workID).eventError, null);
  assert.equal(getStatus(adapter, workID).snapshotError, null);
}

// ── F: recover I/O failure → reject + observable error ──────────────────────
{
  resetStore();
  const workID = 'w-reject';
  applySnapshot(makeView(workID, 1));

  const port = makeMockPort();
  port._setFetchSnapshot(() => Promise.reject(new Error('network down')));
  const adapter = new WorkControllerAdapter(port);

  try {
    await adapter.applyMutationResult(workID, { ok: false, error: { kind: 'transport_error', message: 'boom' } });
    assert.fail('should have thrown');
  } catch (e) {
    assert.ok(e instanceof Error);
    assert.match(String(e), /network down/);
  }

  const status = getStatus(adapter, workID);
  assert.ok(status.snapshotError !== null);
  assert.match(status.snapshotError!, /network down/);
}

// ── G: stale draft write → refresh authoritative revision + retry once ───────
{
  resetStore();
  const workID = 'w-draft-conflict-retry';
  applySnapshot(makeView(workID, 16, { prompt: 'Old prompt' }));

  const port = makeMockPort();
  let fetchCalls = 0;
  port._setFetchSnapshot(() => {
    fetchCalls++;
    return Promise.resolve(makeView(workID, 17, { prompt: 'Old prompt' }));
  });
  const writes: Array<{ expectedRevision: number; requestId: string }> = [];
  port.updateDraft = async (input) => {
    writes.push({ expectedRevision: input.expectedRevision, requestId: input.requestId });
    if (writes.length === 1) {
      throw new Error('work event conflict: expected revision 16, current revision 17');
    }
    return makeView(workID, 18, { prompt: input.prompt });
  };
  const adapter = new WorkControllerAdapter(port);

  const view = await adapter.updateDraft({
    workId: workID,
    prompt: 'Improved prompt',
    expectedRevision: 16,
    requestId: 'draft-request-stable',
  });

  assert.equal(fetchCalls, 1, 'revision conflict must refresh the authoritative snapshot');
  assert.deepEqual(
    writes.map((input) => input.expectedRevision),
    [16, 17],
    'retry must use the recovered revision',
  );
  assert.deepEqual(
    writes.map((input) => input.requestId),
    ['draft-request-stable', 'draft-request-stable'],
    'safe retry must preserve requestId',
  );
  assert.equal(view.revision, 18);
  assert.equal(useWorkStore.getState().revisions[workID], 18);
  assert.equal(useWorkStore.getState().works[workID]?.work.prompt, 'Improved prompt');
}

// ── H: structured revision conflict is also recoverable ─────────────────────
{
  resetStore();
  const workID = 'w-draft-typed-conflict';
  applySnapshot(makeView(workID, 4));

  const port = makeMockPort();
  port._setFetchSnapshot(() => Promise.resolve(makeView(workID, 5)));
  let attempts = 0;
  port.updateDraft = async (input) => {
    attempts++;
    if (attempts === 1) {
      throw Object.assign(new Error('stale Work projection'), {
        code: 'revision_conflict',
        actualRevision: 5,
      });
    }
    return makeView(workID, 6, { prompt: input.prompt });
  };
  const adapter = new WorkControllerAdapter(port);

  await adapter.updateDraft({
    workId: workID,
    prompt: 'Typed recovery',
    expectedRevision: 4,
    requestId: 'typed-conflict-request',
  });

  assert.equal(attempts, 2);
  assert.equal(useWorkStore.getState().revisions[workID], 6);
}

// ── I: transient draft failure gets one idempotent automatic retry ──────────
{
  resetStore();
  const workID = 'w-draft-network-failure';
  applySnapshot(makeView(workID, 2));

  const port = makeMockPort();
  let attempts = 0;
  port.updateDraft = async () => {
    attempts++;
    throw new Error('network unavailable');
  };
  const adapter = new WorkControllerAdapter(port);

  await assert.rejects(
    adapter.updateDraft({
      workId: workID,
      prompt: 'Keep this draft',
      expectedRevision: 2,
      requestId: 'network-failure-request',
    }),
    /network unavailable/,
  );
  assert.equal(attempts, 2, 'transient errors receive one bounded automatic retry');
}

// ── J: input submit refreshes stale authority and retries in one action ──────
{
  resetStore();
  const workID = 'w-input-conflict-retry';
  applySnapshot(makeView(workID, 10));

  const port = makeMockPort();
  port._setFetchSnapshot(() => Promise.resolve(makeView(workID, 11)));
  const writes: Array<{ expectedRevision: number; requestId: string }> = [];
  port.submitWorkInput = async (input) => {
    writes.push({ expectedRevision: input.expectedRevision, requestId: input.requestId });
    if (writes.length === 1) {
      return {
        revision: 11, duplicate: false, committed: false, recoverable: true,
        transportError: {
          code: 'revision_conflict', message: 'stale input authority',
          committed: false, recoverable: true,
        },
      };
    }
    return { revision: 12, duplicate: false, committed: true, recoverable: false };
  };
  const adapter = new WorkControllerAdapter(port);

  const result = await adapter.submitWorkInput({
    workId: workID, runId: 'run-1', taskId: 'task-1', blockId: 'block-1',
    inputId: 'input-1', value: 'new value', definitionRevision: 2,
    inputRevision: 1, expectedRevision: 10, requestId: 'input-request-stable',
  });

  assert.equal(result.committed, true);
  assert.deepEqual(
    writes.map((input) => input.expectedRevision),
    [10, 11],
    'input retry must use recovered Work revision',
  );
  assert.deepEqual(
    writes.map((input) => input.requestId),
    ['input-request-stable', 'input-request-stable'],
    'uncommitted input retry preserves the idempotency key',
  );
}

// ── K: candidate generation retries a pre-planning revision conflict once ───
{
  resetStore();
  const workID = 'w-candidate-conflict-retry';
  applySnapshot(makeView(workID, 20));

  const port = makeMockPort();
  port._setFetchSnapshot(() => Promise.resolve(makeView(workID, 21)));
  const writes: number[] = [];
  port.createCandidateRevision = async (input) => {
    writes.push(input.expectedRevision);
    if (writes.length === 1) {
      return {
        revision: 21, duplicate: false, committed: false, recoverable: true,
        transportError: {
          code: 'revision_conflict', message: 'runtime advanced before planning',
          committed: false, recoverable: true,
        },
      };
    }
    return {
      revision: 21, duplicate: false, committed: false, recoverable: true,
      clarification: {
        id: 'clarify-1', impact: 'task_nodes', question: 'Choose a structure',
        flow: ['current', 'candidate'],
        options: [{ id: 'keep', label: 'Keep current' }, { id: 'split', label: 'Split task' }],
      },
    };
  };
  const adapter = new WorkControllerAdapter(port);

  const result = await adapter.createCandidateRevision({
    workId: workID, intent: 'improve structure', baseDefinitionRevision: 2,
    expectedRevision: 20, requestId: 'candidate-request-stable',
  });

  assert.equal(result.clarification?.id, 'clarify-1');
  assert.deepEqual(writes, [20, 21], 'candidate retry must use recovered Work revision');
}

process.stdout.write('\ncontroller mutation + watch recovery (11 scenarios): all assertions passed\n');
