// Run: npx tsx src/__tests__/composer-queue-drain.test.ts
//
// Focused tests for the ordinary Desktop session queue (store/composerQueue):
//   - enqueue during run (session-scoped, display + submit text)
//   - FIFO head selection
//   - session isolation (no leak / drain across sessions)
//   - drain gate (no drain while running / decision-pending / not ready)
//   - retained item on send failure + retry clears the error

import {
  useComposerQueueStore,
  selectItemsBySession,
  selectSessionQueueHead,
  canDrainQueue,
  makeQueueItem,
  type QueueItem,
} from "../store/composerQueue";

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

function reset() {
  useComposerQueueStore.setState({ items: [] });
}

function add(id: string, sessionId: string, overrides?: Partial<QueueItem>): QueueItem {
  const item: QueueItem = {
    queueItemId: id,
    requestId: `req-${id}`,
    sessionId,
    content: `message ${id}`,
    createdAt: Date.now(),
    ...overrides,
  };
  useComposerQueueStore.getState().addItem(item);
  return item;
}

// ── Enqueue during run ─────────────────────────────────────────────────────

console.log("\ncomposer queue enqueue + scoping");

reset();
add("q1", "session-a", { content: "display one", submitText: "hidden ctx\ndisplay one" });
add("q2", "session-a", { content: "display two", submitText: "display two" });
add("q3", "session-b", { content: "other session" });

const all = useComposerQueueStore.getState().items;
eq(all.length, 3, "enqueue appends items to the shared queue");

{
  const a = selectItemsBySession(all, "session-a");
  eq(a.length, 2, "session-a keeps its two items");
  eq(a[0].queueItemId, "q1", "session-a FIFO order starts with the first enqueued item");
  eq(a[0].submitText, "hidden ctx\ndisplay one", "enqueue preserves backend submit text (session context)");
  eq(a[1].queueItemId, "q2", "session-a FIFO order keeps insertion order");
}

{
  const b = selectItemsBySession(all, "session-b");
  eq(b.length, 1, "session-b keeps its own item");
  eq(b[0].queueItemId, "q3", "session-b item is isolated from session-a");
}

eq(selectItemsBySession(all, "session-c").length, 0, "unknown session has no items");

// ── FIFO head ──────────────────────────────────────────────────────────────

console.log("\ncomposer queue FIFO head");

reset();
add("h1", "s");
add("h2", "s");
add("h3", "s");

let head = selectSessionQueueHead(useComposerQueueStore.getState().items, "s");
eq(head?.queueItemId, "h1", "head is the first enqueued item");

useComposerQueueStore.getState().removeItem("h1");
head = selectSessionQueueHead(useComposerQueueStore.getState().items, "s");
eq(head?.queueItemId, "h2", "after removing the head, the next item becomes head");

// ── Session isolation on remove / clear ────────────────────────────────────

console.log("\ncomposer queue session isolation");

reset();
add("i1", "a");
add("i2", "b");

useComposerQueueStore.getState().clearSessionQueue("a");
eq(selectItemsBySession(useComposerQueueStore.getState().items, "a").length, 0, "clearSessionQueue clears only the target session");
eq(selectItemsBySession(useComposerQueueStore.getState().items, "b").length, 1, "clearSessionQueue leaves other sessions untouched");

// ── Reorder within a session ───────────────────────────────────────────────

console.log("\ncomposer queue session-scoped reorder");

reset();
add("r1", "a");
add("r2", "a");
add("r3", "b");

useComposerQueueStore.getState().reorderSessionItems("a", 1, 0);
{
  const a = selectItemsBySession(useComposerQueueStore.getState().items, "a");
  eq(a.map((item) => item.queueItemId).join(","), "r2,r1", "reorderSessionItems moves within the session");
}
eq(
  selectItemsBySession(useComposerQueueStore.getState().items, "b")[0].queueItemId,
  "r3",
  "reorderSessionItems leaves other sessions untouched",
);

// Out-of-bounds reorder is a no-op
useComposerQueueStore.getState().reorderSessionItems("a", -1, 0);
eq(selectItemsBySession(useComposerQueueStore.getState().items, "a").length, 2, "out-of-bounds reorder is a no-op");

// ── Drain gate ─────────────────────────────────────────────────────────────

console.log("\ncomposer queue drain gate");

eq(canDrainQueue({ ready: true, running: false, decisionPending: false }), true, "ready + idle + no gate may drain");
eq(canDrainQueue({ ready: true, running: true, decisionPending: false }), false, "running blocks drain");
eq(canDrainQueue({ ready: true, running: false, decisionPending: true }), false, "decision gate blocks drain");
eq(canDrainQueue({ ready: false, running: false, decisionPending: false }), false, "not-ready blocks drain");
eq(canDrainQueue({ ready: true, running: true, decisionPending: true }), false, "running + gate blocks drain");

// ── Retained item on send failure ──────────────────────────────────────────

console.log("\ncomposer queue send failure retention");

reset();
add("f1", "s");

useComposerQueueStore.getState().updateItem("f1", { error: "provider unavailable" });
let failedHead = selectSessionQueueHead(useComposerQueueStore.getState().items, "s");
eq(failedHead?.error, "provider unavailable", "failed send retains the item with a retryable error");

// The failed head stays present (not silently dropped) and can be retried.
useComposerQueueStore.getState().updateItem("f1", { error: undefined });
failedHead = selectSessionQueueHead(useComposerQueueStore.getState().items, "s");
eq(failedHead?.error, undefined, "retry clears the error so the item can drain again");
eq(failedHead?.queueItemId, "f1", "the retried item remains in place");

// ── makeQueueItem helper ───────────────────────────────────────────────────

console.log("\ncomposer queue makeQueueItem");

{
  const made = makeQueueItem({ sessionId: "s", content: "hello", submitText: "ctx\nhello" });
  ok(made.queueItemId.length > 0, "makeQueueItem assigns a stable queueItemId");
  ok(made.requestId.length > 0, "makeQueueItem assigns a stable requestId");
  eq(made.sessionId, "s", "makeQueueItem scopes to the session");
  eq(made.content, "hello", "makeQueueItem keeps display content");
  eq(made.submitText, "ctx\nhello", "makeQueueItem keeps backend submit text");
}

// ── Summary ────────────────────────────────────────────────────────────────

const total = passed + failed;
process.stdout.write(`\n${total} tests · ${passed} passed · ${failed} failed\n`);
if (failed > 0) process.exit(1);
