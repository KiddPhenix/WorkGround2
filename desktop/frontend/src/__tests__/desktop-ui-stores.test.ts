// Run: npx tsx src/__tests__/desktop-ui-stores.test.ts
//
// Tests for the five desktop UI state-model stores:
//   store/run.ts, store/artifacts.ts, store/composerQueue.ts,
//   store/addonSurface.ts, store/memory.ts

import {
  useRunStore,
  isTerminalStatus,
  selectRun,
  selectRunEvents,
  selectRunsByStatus,
  selectRunCountByStatus,
  type RunEvent,
} from "../store/run";
import {
  useArtifactStore,
  selectArtifact,
  selectArtifactsBySession,
  selectArtifactIdsByStatus,
  selectArtifactCountByStatus,
  type ArtifactRecord,
} from "../store/artifacts";
import {
  useComposerQueueStore,
  selectQueueItem,
  selectQueueHasItems,
  selectQueueHead,
  type QueueItem,
} from "../store/composerQueue";
import {
  useAddOnSurfaceStore,
  compareAddOnInstances,
  selectSortedAddOnInstances,
  selectActiveAddOnCount,
  selectNeedsInputAddOnCount,
  selectErrorAddOnCount,
  selectPinnedAddOnCount,
  type AddOnInstance,
} from "../store/addonSurface";
import {
  useMemoryStore,
  formatMemoryLine,
  selectMemory,
  selectMemoryExists,
  type MemoryLine,
} from "../store/memory";

// ── Test framework ──────────────────────────────────────────────────────────

let passed = 0;
let failed = 0;

function eq<T>(a: T, b: T, label: string) {
  if (a === b) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(
      `  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`,
    );
    failed += 1;
  }
}

function ok(cond: boolean, label: string) {
  if (cond) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected truthy\n`);
    failed += 1;
  }
}

function done() {
  const total = passed + failed;
  process.stdout.write(`\n${total} tests · ${passed} passed · ${failed} failed\n`);
}

// Reset each store before the tests that use it.
function resetStores() {
  useRunStore.setState({ runs: {} });
  useArtifactStore.setState({ artifacts: {} });
  useComposerQueueStore.setState({ items: [] });
  useAddOnSurfaceStore.setState({
    instances: {},
    workbenchOpen: false,
    editingInstanceId: null,
    _frozenDisplayIndex: null,
  });
  useMemoryStore.setState({ memoryBySession: {}, revisionBySession: {}, sessionKeyBySession: {} });
}

// ────────────────────────────────────────────────────────────────────────────
// run.ts
// ────────────────────────────────────────────────────────────────────────────

console.log("\nrun store");

function testRunMergeEvent() {
  const event1: RunEvent = { eventId: "e1", content: "读取文件", stepLabel: "已读 1 个文件" };
  const event2: RunEvent = { eventId: "e2", content: "分析调用", stepLabel: "分析 delete_range 调用" };

  useRunStore.getState().mergeRunEvent("run-a", "sess-1", "turn-1", event1);
  const s1 = useRunStore.getState();
  eq(s1.runs["run-a"].events.length, 1, "first event creates run with 1 event");
  eq(s1.runs["run-a"].status, "running", "new run starts as running");
  ok(s1.runs["run-a"].expanded, "new run starts expanded");

  // Idempotent: repeat same eventId
  useRunStore.getState().mergeRunEvent("run-a", "sess-1", "turn-1", event1);
  const s2 = useRunStore.getState();
  eq(s2.runs["run-a"].events.length, 1, "repeated same eventId is idempotent");

  useRunStore.getState().mergeRunEvent("run-a", "sess-1", "turn-1", { ...event1, content: "读取完成", status: "completed" });
  const updated = useRunStore.getState().runs["run-a"].events;
  eq(updated.length, 1, "same eventId updates in place without adding a tab");
  eq(updated[0].content, "读取完成", "same eventId accepts newer streamed content");
  eq(updated[0].status, "completed", "same eventId accepts the terminal step status");

  // Second distinct event
  useRunStore.getState().mergeRunEvent("run-a", "sess-1", "turn-1", event2);
  const s3 = useRunStore.getState();
  eq(s3.runs["run-a"].events.length, 2, "second event appends");
  eq(s3.runs["run-a"].events[1].eventId, "e2", "second event is correct");
}

function testRunTerminalGuard() {
  resetStores();
  const { setRunStatus, mergeRunEvent } = useRunStore.getState();

  mergeRunEvent("run-b", "sess-1", "turn-1", {
    eventId: "e1",
    content: "start",
  });
  setRunStatus("run-b", "completed");
  eq(useRunStore.getState().runs["run-b"].status, "completed", "run transitions to completed");

  // Terminal guard: can't change status after terminal
  setRunStatus("run-b", "running");
  eq(useRunStore.getState().runs["run-b"].status, "completed", "completed run stays completed after setRunStatus");

  // Terminal guard: late events are dropped
  mergeRunEvent("run-b", "sess-1", "turn-1", {
    eventId: "e2",
    content: "late event",
  });
  eq(useRunStore.getState().runs["run-b"].events.length, 1, "late events dropped for terminal run");

  // Failed is also terminal
  mergeRunEvent("run-c", "sess-1", "turn-1", { eventId: "e1", content: "start" });
  setRunStatus("run-c", "failed", { errorMessage: "timeout" });
  eq(useRunStore.getState().runs["run-c"].status, "failed", "run transitions to failed");
  eq(useRunStore.getState().runs["run-c"].errorMessage, "timeout", "failed carries errorMessage");
  setRunStatus("run-c", "running");
  eq(useRunStore.getState().runs["run-c"].status, "failed", "failed run stays failed");

  // Cancelled is also terminal
  setRunStatus("run-c", "cancelled");
  eq(useRunStore.getState().runs["run-c"].status, "failed", "terminal->terminal is no-op (stays failed)");
}

function testRunExpandCollapse() {
  resetStores();
  useRunStore.getState().mergeRunEvent("run-d", "sess-1", "turn-1", {
    eventId: "e1",
    content: "start",
  });

  useRunStore.getState().setRunExpanded("run-d", false);
  eq(useRunStore.getState().runs["run-d"].expanded, false, "run can be collapsed");

  useRunStore.getState().setRunExpanded("run-d", true);
  eq(useRunStore.getState().runs["run-d"].expanded, true, "run can be expanded again");
}

function testCollapseSessionRuns() {
  resetStores();
  const store = useRunStore.getState();
  store.mergeRunEvent("run-1", "sess-1", "turn-1", { eventId: "e1", content: "one" });
  store.mergeRunEvent("run-2", "sess-1", "turn-2", { eventId: "e2", content: "two" });
  store.mergeRunEvent("run-3", "sess-2", "turn-3", { eventId: "e3", content: "three" });
  store.collapseSessionRuns("sess-1");
  eq(useRunStore.getState().runs["run-1"].expanded, false, "collapseSessionRuns collapses the first matching run");
  eq(useRunStore.getState().runs["run-2"].expanded, false, "collapseSessionRuns collapses every matching run");
  eq(useRunStore.getState().runs["run-3"].expanded, true, "collapseSessionRuns preserves other sessions");
}

function testRunClear() {
  resetStores();
  useRunStore.getState().mergeRunEvent("run-e", "sess-1", "turn-1", {
    eventId: "e1",
    content: "start",
  });
  useRunStore.getState().clearRun("run-e");
  eq(useRunStore.getState().runs["run-e"], undefined, "cleared run is removed");
  // Safe to clear again
  useRunStore.getState().clearRun("run-e");
  ok(true, "clearRun on missing id is safe");

  // clearAllRuns
  useRunStore.getState().mergeRunEvent("run-f", "sess-1", "turn-1", {
    eventId: "e1",
    content: "start",
  });
  useRunStore.getState().clearAllRuns();
  eq(Object.keys(useRunStore.getState().runs).length, 0, "clearAllRuns removes all runs");
}

function testRunSelectors() {
  resetStores();
  const runs = useRunStore.getState().runs;
  eq(selectRun(runs, "nonexistent"), undefined, "selectRun returns undefined for unknown id");

  useRunStore.getState().mergeRunEvent("run-g", "sess-1", "turn-1", {
    eventId: "e1",
    content: "start",
  });
  const s = useRunStore.getState();
  ok(selectRun(s.runs, "run-g") !== undefined, "selectRun finds existing run");
  eq(selectRunEvents(s.runs, "run-g").length, 1, "selectRunEvents returns events");

  // selectRunsByStatus
  useRunStore.getState().setRunStatus("run-g", "completed");
  const s2 = useRunStore.getState();
  const completedIds = selectRunsByStatus(s2.runs, "completed");
  eq(completedIds.length, 1, "selectRunsByStatus finds completed runs");
  eq(completedIds[0], "run-g", "returns correct runId");

  // selectRunCountByStatus
  eq(selectRunCountByStatus(s2.runs, "completed"), 1, "selectRunCountByStatus counts correctly");
  eq(selectRunCountByStatus(s2.runs, "running"), 0, "selectRunCountByStatus zero for missing");
}

function testIsTerminalStatus() {
  ok(isTerminalStatus("completed"), "completed is terminal");
  ok(isTerminalStatus("failed"), "failed is terminal");
  ok(isTerminalStatus("cancelled"), "cancelled is terminal");
  ok(!isTerminalStatus("running"), "running is not terminal");
  ok(!isTerminalStatus("queued"), "queued is not terminal");
  ok(!isTerminalStatus("waiting_user"), "waiting_user is not terminal");
  ok(!isTerminalStatus("reconnecting"), "reconnecting is not terminal");
}

// ────────────────────────────────────────────────────────────────────────────
// artifacts.ts
// ────────────────────────────────────────────────────────────────────────────

console.log("\nartifact store");

function testArtifactUpsert() {
  resetStores();
  const art1: ArtifactRecord = {
    artifactId: "a1",
    name: "WorkGround2.exe",
    type: "binary",
    status: "available",
    sessionId: "sess-1",
    sourceRunId: "run-a",
  };
  useArtifactStore.getState().upsertArtifact(art1);
  eq(useArtifactStore.getState().artifacts["a1"].name, "WorkGround2.exe", "artifact upserted");

  // Idempotent: same data again is fine
  useArtifactStore.getState().upsertArtifact(art1);
  eq(Object.keys(useArtifactStore.getState().artifacts).length, 1, "repeated upsert keeps one entry");
}

function testArtifactRemove() {
  resetStores();
  useArtifactStore.getState().upsertArtifact({
    artifactId: "a2",
    name: "debug.bat",
    type: "script",
    status: "available",
    sessionId: "sess-1",
  });
  useArtifactStore.getState().removeArtifact("a2");
  eq(useArtifactStore.getState().artifacts["a2"], undefined, "removed artifact is gone");
  // Safe to remove again
  useArtifactStore.getState().removeArtifact("a2");
  ok(true, "removeArtifact on missing id is safe");
}

function testArtifactStatusTransition() {
  resetStores();
  useArtifactStore.getState().upsertArtifact({
    artifactId: "a3",
    name: "test.zip",
    type: "archive",
    status: "available",
    sessionId: "sess-1",
  });
  useArtifactStore.getState().setArtifactStatus("a3", "stale");
  eq(useArtifactStore.getState().artifacts["a3"].status, "stale", "status transitions to stale");

  useArtifactStore.getState().setArtifactStatus("a3", "missing");
  eq(useArtifactStore.getState().artifacts["a3"].status, "missing", "status transitions to missing");

  useArtifactStore.getState().setArtifactStatus("a3", "failed", { errorMessage: "file not found" });
  eq(useArtifactStore.getState().artifacts["a3"].status, "failed", "status transitions to failed");
  eq(useArtifactStore.getState().artifacts["a3"].errorMessage, "file not found", "errorMessage stored");

  // setArtifactStatus on missing id is safe
  useArtifactStore.getState().setArtifactStatus("nonexistent", "available");
  ok(true, "setArtifactStatus on unknown id is safe");
}

function testArtifactSelectors() {
  resetStores();
  useArtifactStore.getState().upsertArtifact({
    artifactId: "a4", name: "f1.exe", type: "binary",
    status: "available", sessionId: "sess-1",
  });
  useArtifactStore.getState().upsertArtifact({
    artifactId: "a5", name: "f2.bat", type: "script",
    status: "stale", sessionId: "sess-1",
  });
  useArtifactStore.getState().upsertArtifact({
    artifactId: "a6", name: "f3.zip", type: "archive",
    status: "available", sessionId: "sess-2",
  });
  const arts = useArtifactStore.getState().artifacts;

  eq(selectArtifact(arts, "a4")?.name, "f1.exe", "selectArtifact finds by id");

  const sess1Arts = selectArtifactsBySession(arts, "sess-1");
  eq(sess1Arts.length, 2, "selectArtifactsBySession filters correctly");

  const staleIds = selectArtifactIdsByStatus(arts, "stale");
  eq(staleIds.length, 1, "selectArtifactIdsByStatus finds stale");
  eq(staleIds[0], "a5", "returns correct id");

  const availCount = selectArtifactCountByStatus(arts, "available");
  eq(availCount, 2, "selectArtifactCountByStatus counts correctly");

  eq(selectArtifactCountByStatus(arts, "missing"), 0, "zero for missing status");
}

function testArtifactSessionReplacement() {
  resetStores();
  useArtifactStore.getState().upsertArtifact({
    artifactId: "old-1", name: "old.bat", type: "script",
    status: "available", sessionId: "sess-1",
  });
  useArtifactStore.getState().upsertArtifact({
    artifactId: "keep-2", name: "keep.zip", type: "archive",
    status: "available", sessionId: "sess-2",
  });
  const projection: ArtifactRecord[] = [
    { artifactId: "new-1", name: "start.sh", type: "script", status: "available", sessionId: "wrong" },
    { artifactId: "new-2", name: "demo.exe", type: "binary", status: "missing", sessionId: "wrong" },
  ];
  useArtifactStore.getState().replaceSessionArtifacts("sess-1", projection);
  const replaced = useArtifactStore.getState().artifacts;
  eq(replaced["old-1"], undefined, "session replacement removes stale records from that session");
  eq(replaced["keep-2"]?.sessionId, "sess-2", "session replacement preserves other sessions");
  eq(replaced["new-1"]?.sessionId, "sess-1", "session replacement owns incoming records");
  eq(selectArtifactsBySession(replaced, "sess-1").length, 2, "session replacement installs the complete projection");

  const beforeRepeat = useArtifactStore.getState();
  useArtifactStore.getState().replaceSessionArtifacts("sess-1", projection);
  eq(useArtifactStore.getState(), beforeRepeat, "repeating an identical projection is a no-op");
}

// ────────────────────────────────────────────────────────────────────────────
// composerQueue.ts
// ────────────────────────────────────────────────────────────────────────────

console.log("\ncomposer queue store");

function makeQueueItem(id: string, overrides?: Partial<QueueItem>): QueueItem {
  return {
    queueItemId: id,
    requestId: `req-${id}`,
    content: `message ${id}`,
    createdAt: Date.now(),
    ...overrides,
  };
}

function testQueueAddItem() {
  resetStores();
  useComposerQueueStore.getState().addItem(makeQueueItem("q1"));
  eq(useComposerQueueStore.getState().items.length, 1, "addItem adds to empty queue");

  // Add another
  useComposerQueueStore.getState().addItem(makeQueueItem("q2"));
  eq(useComposerQueueStore.getState().items.length, 2, "addItem appends second item");
}

function testQueueNoDuplicates() {
  resetStores();
  useComposerQueueStore.getState().addItem(makeQueueItem("q1"));
  useComposerQueueStore.getState().addItem(makeQueueItem("q1"));
  eq(useComposerQueueStore.getState().items.length, 1, "duplicate queueItemId updates in place, no duplicate");
}

function testQueueUpdateItem() {
  resetStores();
  useComposerQueueStore.getState().addItem(makeQueueItem("q1", { content: "original" }));
  useComposerQueueStore.getState().updateItem("q1", { content: "updated" });
  eq(useComposerQueueStore.getState().items[0].content, "updated", "updateItem changes content");

  // updateItem on missing id is safe
  useComposerQueueStore.getState().updateItem("nonexistent", { content: "nope" });
  eq(useComposerQueueStore.getState().items.length, 1, "updateItem on unknown id is no-op");
}

function testQueueRemoveItem() {
  resetStores();
  useComposerQueueStore.getState().addItem(makeQueueItem("q1"));
  useComposerQueueStore.getState().addItem(makeQueueItem("q2"));
  useComposerQueueStore.getState().removeItem("q1");
  eq(useComposerQueueStore.getState().items.length, 1, "removeItem removes by id");
  eq(useComposerQueueStore.getState().items[0].queueItemId, "q2", "remaining item is correct");

  // Safe to remove non-existent
  useComposerQueueStore.getState().removeItem("nonexistent");
  eq(useComposerQueueStore.getState().items.length, 1, "removeItem on unknown id is safe");
}

function testQueueReorder() {
  resetStores();
  useComposerQueueStore.getState().addItem(makeQueueItem("q1"));
  useComposerQueueStore.getState().addItem(makeQueueItem("q2"));
  useComposerQueueStore.getState().addItem(makeQueueItem("q3"));
  // Move q3 (index 2) to index 0
  useComposerQueueStore.getState().reorderItems(2, 0);
  eq(useComposerQueueStore.getState().items[0].queueItemId, "q3", "reorder moves item to front");
  eq(useComposerQueueStore.getState().items[1].queueItemId, "q1", "reorder shifts q1 down");
  eq(useComposerQueueStore.getState().items[2].queueItemId, "q2", "reorder shifts q2 down");

  // Same index is no-op
  useComposerQueueStore.getState().reorderItems(0, 0);
  eq(useComposerQueueStore.getState().items.length, 3, "same-index reorder is no-op");

  // Out of bounds is no-op
  useComposerQueueStore.getState().reorderItems(-1, 0);
  eq(useComposerQueueStore.getState().items.length, 3, "negative index reorder is no-op");
  useComposerQueueStore.getState().reorderItems(0, 99);
  eq(useComposerQueueStore.getState().items.length, 3, "out-of-bounds reorder is no-op");
}

function testQueueClear() {
  resetStores();
  useComposerQueueStore.getState().addItem(makeQueueItem("q1"));
  useComposerQueueStore.getState().clearQueue();
  eq(useComposerQueueStore.getState().items.length, 0, "clearQueue removes all");
}

function testQueueSelectors() {
  resetStores();
  eq(selectQueueItem([], "x"), undefined, "selectQueueItem on empty returns undefined");
  eq(selectQueueHasItems([]), false, "selectQueueHasItems false for empty");
  eq(selectQueueHead([]), undefined, "selectQueueHead on empty is undefined");

  useComposerQueueStore.getState().addItem(makeQueueItem("q1"));
  useComposerQueueStore.getState().addItem(makeQueueItem("q2"));
  const items = useComposerQueueStore.getState().items;
  ok(selectQueueItem(items, "q1") !== undefined, "selectQueueItem finds by id");
  ok(selectQueueHasItems(items), "selectQueueHasItems true for non-empty");
  eq(selectQueueHead(items)?.queueItemId, "q1", "selectQueueHead returns first item");
}

// ────────────────────────────────────────────────────────────────────────────
// addonSurface.ts
// ────────────────────────────────────────────────────────────────────────────

console.log("\naddon surface store");

function makeAddOnInstance(
  id: string,
  overrides?: Partial<AddOnInstance>,
): AddOnInstance {
  return {
    instanceId: id,
    pluginId: "plugin-" + id,
    panelId: "panel-" + id,
    scope: "session",
    status: "active",
    density: "tab",
    pinned: false,
    activationOrder: 0,
    title: "Instance " + id,
    ...overrides,
  };
}

function testAddOnUpsert() {
  resetStores();
  useAddOnSurfaceStore.getState().upsertInstance(makeAddOnInstance("inst-a"));
  ok(useAddOnSurfaceStore.getState().instances["inst-a"] !== undefined, "instance upserted");
  ok(useAddOnSurfaceStore.getState().instances["inst-a"].activationOrder > 0, "activationOrder auto-assigned");
}

function testAddOnRemove() {
  resetStores();
  useAddOnSurfaceStore.getState().upsertInstance(makeAddOnInstance("inst-a"));
  useAddOnSurfaceStore.getState().removeInstance("inst-a");
  eq(useAddOnSurfaceStore.getState().instances["inst-a"], undefined, "removed instance is gone");
  // Safe to remove missing
  useAddOnSurfaceStore.getState().removeInstance("inst-a");
  ok(true, "removeInstance on missing id is safe");
}

function testAddOnSortOrder() {
  resetStores();
  // Insert in reverse activation order
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-c", { status: "active", pinned: false }),
  );
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-b", { status: "active", pinned: false }),
  );
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-a", { status: "active", pinned: false }),
  );

  const insts = useAddOnSurfaceStore.getState().instances;

  // With same status and no pin, order should be by activation (insertion order)
  const sorted = selectSortedAddOnInstances(insts, null, null);
  eq(sorted.length, 3, "selectSortedAddOnInstances returns all instances");
  eq(sorted[0].instanceId, "inst-c", "inst-c sorts first by activation");
  eq(sorted[1].instanceId, "inst-b", "inst-b sorts second by activation");
  eq(sorted[2].instanceId, "inst-a", "inst-a sorts third by activation");

  // Promote inst-a to needs_input — should come first
  useAddOnSurfaceStore.getState().setInstanceStatus("inst-a", "needs_input");
  const sorted2 = selectSortedAddOnInstances(
    useAddOnSurfaceStore.getState().instances,
    null,
    null,
  );
  eq(sorted2[0].instanceId, "inst-a", "needs_input instance sorts first");
  eq(sorted2[1].instanceId, "inst-c", "remaining sorted by activation");

  // Pin inst-b — it should come after needs_input but before unpinned
  useAddOnSurfaceStore.getState().setInstancePinned("inst-b", true);
  const sorted3 = selectSortedAddOnInstances(
    useAddOnSurfaceStore.getState().instances,
    null,
    null,
  );
  eq(sorted3[0].instanceId, "inst-a", "needs_input still first");
  eq(sorted3[1].instanceId, "inst-b", "pinned comes second");
  eq(sorted3[2].instanceId, "inst-c", "unpinned comes last");
}

function testAddOnEditingFreezesPosition() {
  resetStores();
  // Insert three instances with distinct activation order
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-c", { status: "active" }),
  );
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-b", { status: "active" }),
  );
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-a", { status: "active" }),
  );

  // Confirm normal sort before editing
  const insts = useAddOnSurfaceStore.getState().instances;
  const before = selectSortedAddOnInstances(insts, null, null);
  eq(before[0].instanceId, "inst-c", "normal sort: inst-c first by activation");
  eq(before[1].instanceId, "inst-b", "normal sort: inst-b second");
  eq(before[2].instanceId, "inst-a", "normal sort: inst-a third");

  // Begin editing inst-b (currently at index 1)
  useAddOnSurfaceStore.getState().setEditingInstance("inst-b");

  // Change inst-b to needs_input — normally it would jump to position 0
  useAddOnSurfaceStore.getState().setInstanceStatus("inst-b", "needs_input");

  const s = useAddOnSurfaceStore.getState();
  const afterStatusChange = selectSortedAddOnInstances(
    s.instances,
    "inst-b",
    s._frozenDisplayIndex,
  );
  eq(afterStatusChange.length, 3, "still three instances");
  eq(
    afterStatusChange[0].instanceId,
    "inst-c",
    "editing instance frozen: inst-c still at position 0",
  );
  eq(
    afterStatusChange[1].instanceId,
    "inst-b",
    "editing instance frozen: inst-b still at position 1 despite needs_input",
  );
  eq(
    afterStatusChange[2].instanceId,
    "inst-a",
    "editing instance frozen: inst-a still at position 2",
  );

  // Pin inst-b while still editing — position should also be frozen
  useAddOnSurfaceStore.getState().setInstancePinned("inst-b", true);
  const s2 = useAddOnSurfaceStore.getState();
  const afterPin = selectSortedAddOnInstances(
    s2.instances,
    "inst-b",
    s2._frozenDisplayIndex,
  );
  eq(afterPin[1].instanceId, "inst-b", "editing instance still frozen after pin");

  // Now end editing — normal sort resumes
  useAddOnSurfaceStore.getState().setEditingInstance(null);
  const afterEditing = selectSortedAddOnInstances(
    useAddOnSurfaceStore.getState().instances,
    null,
    null,
  );
  eq(
    afterEditing[0].instanceId,
    "inst-b",
    "after editing ends, needs_input + pinned sorts first",
  );
  eq(afterEditing[1].instanceId, "inst-c", "second by activation");
  eq(afterEditing[2].instanceId, "inst-a", "third by activation");
}

function testAddOnWorkbenchOpen() {
  resetStores();
  eq(useAddOnSurfaceStore.getState().workbenchOpen, false, "workbench starts closed");
  useAddOnSurfaceStore.getState().setWorkbenchOpen(true);
  eq(useAddOnSurfaceStore.getState().workbenchOpen, true, "workbench opens");
  useAddOnSurfaceStore.getState().setWorkbenchOpen(false);
  eq(useAddOnSurfaceStore.getState().workbenchOpen, false, "workbench closes");
}

function testAddOnDensity() {
  resetStores();
  useAddOnSurfaceStore.getState().upsertInstance(makeAddOnInstance("inst-a"));
  useAddOnSurfaceStore.getState().setInstanceDensity("inst-a", "focus");
  eq(useAddOnSurfaceStore.getState().instances["inst-a"].density, "focus", "density changes to focus");
  useAddOnSurfaceStore.getState().setInstanceDensity("inst-a", "peek");
  eq(useAddOnSurfaceStore.getState().instances["inst-a"].density, "peek", "density changes to peek");
  useAddOnSurfaceStore.getState().setInstanceDensity("inst-a", "tab");
  eq(useAddOnSurfaceStore.getState().instances["inst-a"].density, "tab", "density changes to tab");
}

function testAddOnScope() {
  resetStores();
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-a", { scope: "session" }),
  );
  useAddOnSurfaceStore.getState().setInstanceScope("inst-a", "window");
  eq(useAddOnSurfaceStore.getState().instances["inst-a"].scope, "window", "scope changes to window");
}

function testAddOnClearAll() {
  resetStores();
  useAddOnSurfaceStore.getState().upsertInstance(makeAddOnInstance("inst-a"));
  useAddOnSurfaceStore.getState().upsertInstance(makeAddOnInstance("inst-b"));
  useAddOnSurfaceStore.getState().setWorkbenchOpen(true);
  useAddOnSurfaceStore.getState().setEditingInstance("inst-a");
  useAddOnSurfaceStore.getState().clearAllInstances();
  eq(Object.keys(useAddOnSurfaceStore.getState().instances).length, 0, "clearAll removes all instances");
  eq(useAddOnSurfaceStore.getState().workbenchOpen, false, "workbench resets to closed");
  eq(useAddOnSurfaceStore.getState().editingInstanceId, null, "editingInstanceId resets to null");
}

function testAddOnSelectors() {
  resetStores();
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-a", { pinned: true }),
  );
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-b", { status: "needs_input" }),
  );
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-c", { status: "dismissed" }),
  );
  useAddOnSurfaceStore.getState().upsertInstance(
    makeAddOnInstance("inst-d", { status: "error", message: "oops" }),
  );
  const insts = useAddOnSurfaceStore.getState().instances;

  eq(selectActiveAddOnCount(insts), 3, "dismissed excluded from active count");
  eq(selectNeedsInputAddOnCount(insts), 1, "needs_input count correct");
  eq(selectErrorAddOnCount(insts), 1, "error count correct");
  eq(selectPinnedAddOnCount(insts), 1, "pinned count correct");
}

function testCompareAddOnInstances() {
  const a: AddOnInstance = makeAddOnInstance("a", { status: "needs_input", pinned: false, activationOrder: 1 });
  const b: AddOnInstance = makeAddOnInstance("b", { status: "active", pinned: true, activationOrder: 2 });
  const c: AddOnInstance = makeAddOnInstance("c", { status: "active", pinned: false, activationOrder: 3 });

  ok(compareAddOnInstances(a, b) < 0, "needs_input before pinned");
  ok(compareAddOnInstances(b, c) < 0, "pinned before unpinned");
  ok(compareAddOnInstances(a, c) < 0, "needs_input before unpinned");
  ok(compareAddOnInstances(c, a) > 0, "unpinned after needs_input");
  ok(compareAddOnInstances(a, a) === 0, "same instance equals zero");
}

// ────────────────────────────────────────────────────────────────────────────
// memory.ts
// ────────────────────────────────────────────────────────────────────────────

console.log("\nmemory store");

function testMemorySet() {
  resetStores();
  eq(Object.keys(useMemoryStore.getState().memoryBySession).length, 0, "memoryBySession starts empty");

  const line: MemoryLine = {
    goal: "重构桌面信息架构",
    current: "Artifact 模型已验证",
    nextStep: "实现持久化",
  };
  useMemoryStore.getState().setMemory("sess-1", line);
  const mem = selectMemory(useMemoryStore.getState().memoryBySession, "sess-1");
  eq(mem?.goal, "重构桌面信息架构", "memory goal set for session");
  eq(mem?.current, "Artifact 模型已验证", "memory current set for session");
  eq(mem?.nextStep, "实现持久化", "memory nextStep set for session");
}

function testMemoryClear() {
  resetStores();
  useMemoryStore.getState().setMemory("sess-1", {
    goal: "g",
    current: "c",
    nextStep: "n",
  });
  useMemoryStore.getState().setMemory("sess-1", null);
  eq(selectMemory(useMemoryStore.getState().memoryBySession, "sess-1"), undefined, "memory clears to undefined for session");
}

function testMemoryIndependentSessions() {
  resetStores();
  const line1: MemoryLine = { goal: "重构桌面信息架构", current: "Artifact 模型已验证", nextStep: "实现持久化" };
  const line2: MemoryLine = { goal: "任务执行进度组件", current: "状态枚举定义", nextStep: "实现状态机" };

  useMemoryStore.getState().setMemory("sess-alpha", line1);
  useMemoryStore.getState().setMemory("sess-beta", line2);

  const memBySession = useMemoryStore.getState().memoryBySession;
  eq(Object.keys(memBySession).length, 2, "two independent session entries");

  eq(
    selectMemory(memBySession, "sess-alpha")?.goal,
    "重构桌面信息架构",
    "session alpha has its own memory",
  );
  eq(
    selectMemory(memBySession, "sess-beta")?.goal,
    "任务执行进度组件",
    "session beta has its own memory — no cross-contamination",
  );

  // Clear one session without affecting the other
  useMemoryStore.getState().clearSessionMemory("sess-alpha");
  const afterClear = useMemoryStore.getState().memoryBySession;
  ok(!selectMemoryExists(afterClear, "sess-alpha"), "session alpha cleared");
  ok(selectMemoryExists(afterClear, "sess-beta"), "session beta still has memory");
  eq(Object.keys(afterClear).length, 1, "only one session entry remains");
}

function testFormatMemoryLine() {
  const line: MemoryLine = {
    goal: "重构桌面信息架构",
    current: "Artifact 模型已验证",
    nextStep: "实现持久化",
  };
  const formatted = formatMemoryLine(line);
  eq(
    formatted,
    "目标：重构桌面信息架构 · 当前：Artifact 模型已验证 · 下一步：实现持久化",
    "formatMemoryLine produces correct string matching spec §16.2",
  );
}

function testSelectMemoryExists() {
  resetStores();
  eq(selectMemoryExists(useMemoryStore.getState().memoryBySession, "sess-1"), false, "selectMemoryExists false for missing session");

  useMemoryStore.getState().setMemory("sess-1", { goal: "g", current: "c", nextStep: "n" });
  eq(selectMemoryExists(useMemoryStore.getState().memoryBySession, "sess-1"), true, "selectMemoryExists true for set session");
}

function testMemoryRevisionOrderingAndTombstone() {
  resetStores();
  const store = useMemoryStore.getState();
  store.applyMemory("sess-1", { sessionKey: "branch-a", goal: "new", current: "", nextStep: "", revision: 2, updatedAt: 2 });
  store.applyMemory("sess-1", { sessionKey: "branch-a", goal: "old", current: "", nextStep: "", revision: 1, updatedAt: 1 });
  eq(selectMemory(useMemoryStore.getState().memoryBySession, "sess-1")?.goal, "new", "stale memory revision is ignored");
  useMemoryStore.getState().applyMemory("sess-1", { sessionKey: "branch-a", goal: "", current: "", nextStep: "", revision: 3, updatedAt: 3 });
  eq(selectMemory(useMemoryStore.getState().memoryBySession, "sess-1"), undefined, "newer empty memory clears the visible row");
  useMemoryStore.getState().applyMemory("sess-1", { sessionKey: "branch-a", goal: "resurrect", current: "", nextStep: "", revision: 2, updatedAt: 2 });
  eq(selectMemory(useMemoryStore.getState().memoryBySession, "sess-1"), undefined, "tombstone revision prevents stale resurrection");
  useMemoryStore.getState().applyMemory("sess-1", { sessionKey: "branch-b", goal: "other branch", current: "", nextStep: "", revision: 1, updatedAt: 4 });
  eq(selectMemory(useMemoryStore.getState().memoryBySession, "sess-1")?.goal, "other branch", "new session namespace accepts a lower revision");

  useMemoryStore.getState().applyMemory("sess-legacy", { sessionKey: "legacy", goal: "legacy goal", revision: 1 } as MemoryLine);
  const legacy = selectMemory(useMemoryStore.getState().memoryBySession, "sess-legacy");
  eq(legacy?.current, "", "missing legacy current field is normalized");
  eq(legacy?.nextStep, "", "missing legacy next-step field is normalized");
}

// ── Run all tests ───────────────────────────────────────────────────────────

testRunMergeEvent();
testRunTerminalGuard();
testRunExpandCollapse();
testCollapseSessionRuns();
testRunClear();
testRunSelectors();
testIsTerminalStatus();

testArtifactUpsert();
testArtifactRemove();
testArtifactStatusTransition();
testArtifactSelectors();
testArtifactSessionReplacement();

testQueueAddItem();
testQueueNoDuplicates();
testQueueUpdateItem();
testQueueRemoveItem();
testQueueReorder();
testQueueClear();
testQueueSelectors();

testAddOnUpsert();
testAddOnRemove();
testAddOnSortOrder();
testAddOnEditingFreezesPosition();
testAddOnWorkbenchOpen();
testAddOnDensity();
testAddOnScope();
testAddOnClearAll();
testAddOnSelectors();
testCompareAddOnInstances();

testMemorySet();
testMemoryClear();
testMemoryIndependentSessions();
testFormatMemoryLine();
testSelectMemoryExists();
testMemoryRevisionOrderingAndTombstone();

done();

// Exit with non-zero code if any test failed
process.exit(failed > 0 ? 1 : 0);
