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

// ── A: Watch event triggers onEvent → claimProjectionRecovery → recoverSnapshot fails → retry clears ──
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
    // Watch-triggered recovery fails.
    return Promise.reject(new Error('watch-recovery-network-down'));
  });

  let recoveryCalls = 0;
  port._setFetchRecoverySnapshot(() => {
    recoveryCalls++;
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

  // Dispatch a Watch gap event via the captured onEvent. Store at rev 3; event
  // baseRevision 2 → gap → claimProjectionRecovery → recoverSnapshot → fetchSnapshot fails.
  assert.ok(port._capturedOnEvent, 'subscribe must capture onEvent');
  port._capturedOnEvent!(gapEvent(workID, 4, 2));

  // Wait for onEvent's async recoverSnapshot + catch at L262 to settle.
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

  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe(workID);
  await sleep(300);

  assert.equal(getStatus(adapter, workID).stream.kind, 'online');
  const revBefore = useWorkStore.getState().revisions[workID];
  assert.equal(revBefore, 3);

  // Watch event arrives first: gap event (baseRevision != current) triggers
  // claimProjectionRecovery → recoverSnapshot → fetchSnapshot returns revision 5.
  assert.ok(port._capturedOnEvent);
  port._capturedOnEvent!(gapEvent(workID, 4, 2));

  // Let the onEvent → recoverSnapshot async chain settle.
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

process.stdout.write('\ncontroller mutation + watch recovery (6 scenarios): all assertions passed\n');
