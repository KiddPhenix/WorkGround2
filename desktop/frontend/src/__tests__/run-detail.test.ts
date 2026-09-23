// Behaviour regressions for the run record detail feature:
//   - the run model keeps a stable call identity and separate output/error;
//   - late / duplicated / out-of-order deliveries complete an owned record but
//     never reopen a finished run or let a progress preview overwrite a result;
//   - the history projection marks an elided body as archived instead of empty;
//   - the on-demand body loader distinguishes "empty" from "unavailable",
//     retries safely, and can never let one record's response land on another.

import {
  applyRunWireEvent,
  projectRunHistory,
  resetRunProjection,
} from "../lib/runEvents";
import { useRunStore, type RunEvent, type RunRecord } from "../store/run";
import {
  countMatches,
  countNewRecords,
  detailBodyText,
  filterRunDetailEvents,
  formatRunDetailDuration,
  highlightSegments,
  nextFailureIndex,
  prettyJson,
  resolveRunDetailSelection,
} from "../lib/runDetail";
import {
  detailBodyKey,
  loadDetailBody,
  needsDetailBody,
  readDetailBody,
  resetDetailBodies,
} from "../lib/runDetailData";
import {
  detailBodyScrollKey,
  detailListScrollKey,
  forgetDetailScroll,
  recallDetailScroll,
  rememberDetailScroll,
  resetDetailScrollMemory,
} from "../lib/runDetailScroll";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed++; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed++; process.stdout.write(`  FAIL  ${label}\n`); }
}
function eq<T>(actual: T, expected: T, label: string) {
  ok(actual === expected, `${label}${actual === expected ? "" : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function resetRunState() {
  useRunStore.setState({ runs: {} });
  resetRunProjection();
  resetDetailBodies();
  resetDetailScrollMemory();
}

function runsFor(sessionId: string): RunRecord[] {
  return Object.values(useRunStore.getState().runs).filter((run) => run.sessionId === sessionId);
}

function eventsOf(sessionId: string): RunEvent[] {
  const runs = runsFor(sessionId);
  return runs.length === 0 ? [] : runs[0].events;
}

/** The record carrying a given backend call id, across every run. */
function byCallId(callId: string): RunEvent {
  const found = Object.values(useRunStore.getState().runs)
    .flatMap((run) => run.events)
    .find((event) => event.callId === callId);
  if (!found) throw new Error(`missing record for ${callId}`);
  return found;
}

resetRunState();

// ── wire projection keeps a full, honest record ──────────────────────────────

applyRunWireEvent("tab-a", { kind: "turn_started" }, "turn:1");
applyRunWireEvent("tab-a", {
  kind: "tool_dispatch",
  tool: {
    id: "call-1",
    name: "edit_file",
    args: '{"path":"a.ts"}',
    readOnly: false,
    diff: "--- a/a.ts\n+++ b/a.ts\n@@ -1 +1 @@\n-old\n+new",
    added: 1,
    removed: 1,
  },
});
let edit = byCallId("call-1");
eq(edit.phase, "dispatch", "dispatch records its phase");
eq(edit.status, "running", "dispatch starts the record as running");
ok(Boolean(edit.diff?.includes("+new")), "dispatch keeps the previewed diff");
ok(typeof edit.startedAt === "number", "dispatch records a start time");
const startedAt = edit.startedAt;

applyRunWireEvent("tab-a", { kind: "tool_progress", tool: { id: "call-1", name: "edit_file", output: "writing…", readOnly: false } });
edit = byCallId("call-1");
eq(edit.phase, "progress", "progress advances the phase of the same record");
eq(eventsOf("tab-a").filter((event) => event.callId === "call-1").length, 1, "progress does not open a second record");

applyRunWireEvent("tab-a", {
  kind: "tool_result",
  tool: { id: "call-1", name: "edit_file", output: "saved 12 lines", durationMs: 1450, readOnly: false },
});
edit = byCallId("call-1");
eq(edit.phase, "result", "result settles the record");
eq(edit.status, "completed", "a successful result is completed");
eq(edit.output, "saved 12 lines", "result keeps the full output");
eq(edit.durationMs, 1450, "result keeps the reported duration");
eq(edit.startedAt, startedAt, "result preserves the original start time");

// A failure must not hide what the tool already produced.
applyRunWireEvent("tab-a", { kind: "tool_dispatch", tool: { id: "call-2", name: "bash", args: "go test ./...", readOnly: true } });
applyRunWireEvent("tab-a", {
  kind: "tool_result",
  tool: { id: "call-2", name: "bash", output: "FAIL util\n1 failing", err: "exit status 1", truncated: true, readOnly: true },
});
const failedRecord = byCallId("call-2");
eq(failedRecord.status, "failed", "a failed call is marked failed");
eq(failedRecord.error, "exit status 1", "the error text is kept on its own field");
eq(failedRecord.output, "FAIL util\n1 failing", "the partial output survives next to the error");
eq(failedRecord.truncated, true, "a head+tailed output is flagged as truncated");

// A stopped call is unconfirmed: neither success nor failure.
applyRunWireEvent("tab-a", { kind: "tool_dispatch", tool: { id: "call-3", name: "bash", args: "sleep 60", readOnly: true } });
applyRunWireEvent("tab-a", {
  kind: "tool_result",
  tool: { id: "call-3", name: "bash", output: "partial", err: "tool stopped by user: bash", stopped: true, readOnly: true },
});
const stoppedRecord = byCallId("call-3");
eq(stoppedRecord.status, "stopped", "a user-stopped call is not reported as success");
ok(stoppedRecord.stepLabel?.includes("已停止") === true, "a stopped record says so in its label");

// ── late, duplicate and out-of-order deliveries ─────────────────────────────

applyRunWireEvent("tab-a", { kind: "turn_done" });
eq(runsFor("tab-a")[0].status, "completed", "turn_done completes the run");
const beforeLate = runsFor("tab-a")[0].events.length;

// A progress chunk that lost the race with the result must not replace it.
applyRunWireEvent("tab-a", { kind: "tool_progress", tool: { id: "call-1", name: "edit_file", output: "stale preview", readOnly: false } });
eq(byCallId("call-1").content, "saved 12 lines", "a late progress preview cannot overwrite a settled result");
eq(byCallId("call-1").phase, "result", "the settled record keeps its result phase");

// A duplicate dispatch after the result is equally a no-op.
applyRunWireEvent("tab-a", { kind: "tool_dispatch", tool: { id: "call-1", name: "edit_file", args: '{"path":"a.ts"}', readOnly: false } });
eq(byCallId("call-1").status, "completed", "a late dispatch does not reopen a settled record");

// A brand-new record after the run finished is still dropped.
applyRunWireEvent("tab-a", { kind: "tool_dispatch", tool: { id: "call-9", name: "read_file", readOnly: true } });
eq(runsFor("tab-a")[0].events.length, beforeLate, "a terminal run gains no new records");

// A late result for a record the run already owns completes it in place.
applyRunWireEvent("tab-b", { kind: "turn_started" }, "turn:1");
applyRunWireEvent("tab-b", { kind: "tool_dispatch", tool: { id: "late-1", name: "bash", args: "sleep 5", readOnly: true } });
applyRunWireEvent("tab-b", { kind: "turn_done" });
eq(runsFor("tab-b")[0].status, "completed", "the run finished before its tool reported");
applyRunWireEvent("tab-b", {
  kind: "tool_result",
  tool: { id: "late-1", name: "bash", output: "done after turn_done", durationMs: 400, readOnly: true },
});
eq(runsFor("tab-b")[0].status, "completed", "a late result never regresses the run status");
eq(byCallId("late-1").status, "completed", "a late result completes its own record");
eq(byCallId("late-1").output, "done after turn_done", "a late result fills in the missing body");

// Duplicate delivery of the same result is idempotent.
const snapshot = JSON.stringify(runsFor("tab-b")[0].events);
applyRunWireEvent("tab-b", { kind: "tool_result", tool: { id: "late-1", name: "bash", output: "done after turn_done", durationMs: 400, readOnly: true } });
eq(JSON.stringify(runsFor("tab-b")[0].events), snapshot, "re-delivering the same result changes nothing");

// ── cancellation is never reported as success ──────────────────────────────

applyRunWireEvent("tab-c", { kind: "turn_started" }, "turn:1");
applyRunWireEvent("tab-c", { kind: "tool_dispatch", tool: { id: "c-1", name: "bash", args: "long", readOnly: true } });
applyRunWireEvent("tab-c", { kind: "turn_done" }, undefined, { cancelled: true });
eq(runsFor("tab-c")[0]?.status, "cancelled", "a cancelled turn is not reported as a success");
eq(eventsOf("tab-c").find((event) => event.eventId.endsWith(":done"))?.stepLabel, "已取消", "the terminal record says cancelled");

applyRunWireEvent("tab-c2", { kind: "turn_started" }, "turn:1");
applyRunWireEvent("tab-c2", { kind: "turn_done", err: "provider exploded" }, undefined, { cancelled: true });
eq(runsFor("tab-c2")[0]?.status, "cancelled", "cancelled wins over the report shape");
eq(eventsOf("tab-c2").find((event) => event.eventId.endsWith(":done"))?.error, "provider exploded", "the real error text is still kept");

applyRunWireEvent("tab-c3", { kind: "turn_started" }, "turn:1");
applyRunWireEvent("tab-c3", { kind: "turn_done" });
eq(runsFor("tab-c3")[0]?.status, "completed", "an ordinary turn_done is still a success");

// ── history projection: archived is not empty ──────────────────────────────

resetRunState();
projectRunHistory("tab-h", [
  { kind: "user" },
  { kind: "tool", id: "h1", callId: "call-h1", name: "read_file", args: "a.go", status: "done", output: "ok", durationMs: 12, truncated: true },
  { kind: "user" },
  { kind: "tool", id: "h2", callId: "call-h2", name: "shell", args: "go build", status: "error", error: "compile failed", dataArchived: true, summary: "go build" },
  { kind: "user" },
  { kind: "tool", id: "h3", callId: "call-h3", name: "edit_file", args: "b.ts", status: "done", dataArchived: true, fileDiff: { diff: "@@ -1 +1 @@\n-a\n+b", added: 1, removed: 1 } },
  { kind: "user" },
  { kind: "tool", id: "h4", callId: "call-h4", name: "bash", args: "sleep", status: "stopped" },
]);
const hydrated = runsFor("tab-h");
eq(hydrated.length, 4, "history still rebuilds one record set per execution turn");
const h1 = hydrated[0].events[0];
eq(h1.callId, "call-h1", "history keeps the backend call identity for on-demand loading");
eq(h1.output, "ok", "history keeps an available output");
eq(h1.truncated, true, "history keeps the truncation flag");
eq(h1.durationMs, 12, "history keeps the duration");
const h2 = hydrated[1].events[0];
eq(h2.archived, true, "an elided history body is marked archived");
eq(h2.error, "compile failed", "history keeps a retained error preview");
eq(h2.status, "failed", "an archived failure is still a failure");
eq(hydrated[2].events[0].diff, "@@ -1 +1 @@\n-a\n+b", "history keeps a previewed diff");
eq(hydrated[3].events[0].status, "stopped", "history keeps a stopped record unconfirmed");
ok(needsDetailBody(h2), "an archived record is worth a round trip");
ok(needsDetailBody(h1), "a truncated record is worth a round trip");
ok(!needsDetailBody({ eventId: "plain", kind: "generic", content: "inline" }), "a record with its body inline needs no round trip");

// ── selection model: follow, pin, and never steal a reader ─────────────────

function record(events: RunEvent[], extra: Partial<RunRecord> = {}): RunRecord {
  return {
    runId: "run-x",
    sessionId: "tab-x",
    turnId: "turn:1",
    status: "running",
    events,
    expanded: true,
    startedAt: 1,
    ...extra,
  };
}
const three: RunEvent[] = [
  { eventId: "e1", kind: "read", content: "one", status: "completed" },
  { eventId: "e2", kind: "command", content: "two", status: "failed" },
  { eventId: "e3", kind: "test", content: "three", status: "running" },
];

let selection = resolveRunDetailSelection(record(three, { detailFollowLatest: true }));
eq(selection.index, 2, "follow-latest selects the newest record");
eq(selection.following, true, "follow-latest reports itself as following");

selection = resolveRunDetailSelection(record(three, { detailFollowLatest: false, detailSelectedEventId: "e1" }));
eq(selection.index, 0, "a pinned record stays selected");
eq(selection.following, false, "a pinned record is not following");

// The acceptance case: while record #1 is being read, a record lands after it.
const grown = [...three, { eventId: "e4", kind: "generic", content: "four", status: "completed" } as RunEvent];
selection = resolveRunDetailSelection(record(grown, { detailFollowLatest: false, detailSelectedEventId: "e1" }));
eq(selection.index, 0, "an arriving record does not steal the record being read");
eq(countNewRecords(grown, selection.index), 3, "the panel can report how many records arrived");
eq(countNewRecords(grown, 3), 0, "no new-record hint while reading the newest record");

selection = resolveRunDetailSelection(record(three, { detailFollowLatest: false, detailSelectedEventId: "gone" }));
eq(selection.index, 2, "a vanished pin falls back to the newest record");
eq(selection.pinMissing, true, "a vanished pin is reported instead of silently ignored");

// ── store: detail open/select/follow/maximize ──────────────────────────────

resetRunState();
const store = useRunStore.getState();
store.mergeRunEvent("run-1", "tab-s", "turn:1", three[0]);
store.mergeRunEvent("run-1", "tab-s", "turn:1", three[1]);
store.mergeRunEvent("run-1", "tab-s", "turn:1", three[2]);
const run1 = () => useRunStore.getState().runs["run-1"];

eq(run1().detailFollowLatest, true, "a new run starts by following the newest record");
useRunStore.getState().setRunDetailOpen("run-1", true);
eq(run1().detailOpen, true, "the panel can be opened");
useRunStore.getState().selectRunDetailEvent("run-1", "e2");
eq(run1().detailSelectedEventId, "e2", "the selection is stored by stable eventId");
eq(run1().detailFollowLatest, false, "selecting a record stops following");
useRunStore.getState().setRunDetailOpen("run-1", false);
useRunStore.getState().setRunDetailOpen("run-1", true);
eq(run1().detailSelectedEventId, "e2", "closing and reopening keeps the selection");
eq(run1().detailFollowLatest, false, "closing and reopening keeps the pin");
useRunStore.getState().selectRunDetailEvent("run-1", undefined);
eq(run1().detailFollowLatest, true, "clearing the selection restores follow-latest");
eq(run1().detailSelectedEventId, undefined, "clearing the selection drops the pin");
// The compact card's step is the same intent, so releasing one releases both.
const compactPin: RunEvent[] = [...three, { eventId: "e4", kind: "generic", content: "four", status: "completed" }];
useRunStore.getState().mergeRunEvent("run-1", "tab-s", "turn:1", compactPin[3]);
useRunStore.getState().setRunSelectedStep("run-1", 0);
eq(resolveRunDetailSelection(run1()).index, 0, "a step pinned in the compact card is the record the panel reads");
useRunStore.getState().selectRunDetailEvent("run-1", undefined);
eq(run1().selectedStepIndex, undefined, "跳到最新 releases the compact card's pin as well");
eq(resolveRunDetailSelection(run1()).index, 3, "after releasing the pin the newest record wins again");
useRunStore.getState().setRunDetailMaximized("run-1", true);
eq(run1().detailMaximized, true, "the panel can be maximized");
eq(run1().detailOpen, true, "maximizing does not close the panel");

// The panel lives on the same right edge for every run, so only one run owns it.
store.mergeRunEvent("run-2", "tab-s", "turn:2", three[0]);
useRunStore.getState().setRunDetailOpen("run-2", true);
eq(useRunStore.getState().runs["run-2"].detailOpen, true, "another run can take the panel");
eq(run1().detailOpen, false, "taking the panel closes the previous run's panel");
eq(run1().detailMaximized, true, "the previous run keeps its own layout preference");

// Removing a run must not leave fetched bodies behind for a later run.
const orphanKey = detailBodyKey("run-2", "call-orphan");
await loadDetailBody(orphanKey, async () => ({ output: "orphan body" }));
eq(readDetailBody(orphanKey).phase, "loaded", "a body is cached for the run");
useRunStore.getState().clearRun("run-2");
eq(readDetailBody(orphanKey).phase, "idle", "removing a run invalidates its cached bodies");

// ── pure helpers ───────────────────────────────────────────────────────────

const searchable: RunEvent[] = [
  { eventId: "s1", kind: "command", content: "go test ./...", toolName: "bash", args: '{"command":"go test ./..."}' },
  { eventId: "s2", kind: "read", content: "read a.ts", toolName: "read_file", args: '{"path":"src/a.ts"}' },
  { eventId: "s3", kind: "edit", content: "write b.ts", toolName: "edit_file", args: '{"path":"src/b.ts"}', status: "failed", error: "permission denied" },
];
eq(filterRunDetailEvents(searchable, { query: "./...", failuresOnly: false }).length, 1, "search matches a command");
eq(filterRunDetailEvents(searchable, { query: "a.ts", failuresOnly: false }).length, 1, "search matches a file");
eq(filterRunDetailEvents(searchable, { query: "edit_file", failuresOnly: false }).length, 1, "search matches a tool name");
eq(filterRunDetailEvents(searchable, { query: "permission", failuresOnly: false }).length, 1, "search matches the error text");
eq(filterRunDetailEvents(searchable, { query: "", failuresOnly: true }).length, 1, "仅失败 keeps failures only");
eq(filterRunDetailEvents(searchable, { query: "read", failuresOnly: true }).length, 0, "filters combine");
eq(filterRunDetailEvents(searchable, { query: "", failuresOnly: false }).length, 3, "an empty filter keeps every record");

eq(nextFailureIndex(searchable, -1, 1), 2, "next failure walks forward to the failed record");
eq(nextFailureIndex(searchable, 2, -1), undefined, "there is no failure before the first one");
const twoFailures: RunEvent[] = [
  { eventId: "f1", kind: "generic", content: "a", status: "failed" },
  { eventId: "f2", kind: "generic", content: "b" },
  { eventId: "f3", kind: "generic", content: "c", status: "failed" },
];
eq(nextFailureIndex(twoFailures, 0, 1), 2, "下一失败 skips successful records");
eq(nextFailureIndex(twoFailures, 2, -1), 0, "上一失败 walks backwards");
eq(nextFailureIndex(twoFailures, 0, -1), undefined, "上一失败 stops at the start");
eq(nextFailureIndex(twoFailures, -1, -1), 2, "上一失败 from an untouched selection finds the last failure");

eq(countMatches("a b a", "a"), 2, "find counts every match");
eq(highlightSegments("a b a", "b")[1].hit, true, "find highlights the matching span");
ok(highlightSegments("no hit", "zzz").every((segment) => !segment.hit), "find without matches stays plain");
eq(formatRunDetailDuration(940), "940 ms", "sub-second durations read in ms");
eq(formatRunDetailDuration(1500), "1.5 s", "longer durations read in seconds");
eq(formatRunDetailDuration(undefined), "", "a missing duration renders nothing");
eq(detailBodyText({ eventId: "x", kind: "generic", content: "summary", output: "full" }), "full", "the detail body prefers the full output");
eq(detailBodyText({ eventId: "x", kind: "generic", content: "summary only", archived: true }), "", "an archived body is missing, not the summary");
eq(detailBodyText({ eventId: "x", kind: "generic", content: "streaming" }), "streaming", "a running record shows its streaming preview");
eq(prettyJson('{"a":1}'), '{\n  "a": 1\n}', "json arguments are pretty printed");
eq(prettyJson("plain text"), "plain text", "plain arguments are left alone");

// ── scroll memory: closing and reopening keeps the reader's place ──────────

resetDetailScrollMemory();
const listKey = detailListScrollKey("run-1");
rememberDetailScroll(listKey, 320);
eq(recallDetailScroll(listKey), 320, "the list offset survives a remount");
rememberDetailScroll(detailBodyScrollKey("run-1", "e2"), 640);
eq(recallDetailScroll(detailBodyScrollKey("run-1", "e2")), 640, "the body offset is remembered per record");
eq(recallDetailScroll(detailBodyScrollKey("run-1", "e3")), 0, "another record starts at the top");
eq(recallDetailScroll(detailListScrollKey("run-2")), 0, "another run does not inherit offsets");
forgetDetailScroll("run-1");
eq(recallDetailScroll(listKey), 0, "dropping a run clears its offsets");
eq(recallDetailScroll(detailBodyScrollKey("run-1", "e2")), 0, "dropping a run clears every record offset");

// ── on-demand body loader ──────────────────────────────────────────────────

async function testLoader() {
  resetDetailBodies();
  const key = detailBodyKey("run-live", "call-1");
  eq(readDetailBody(key).phase, "idle", "a record starts with no loaded body");

  await loadDetailBody(key, async () => ({ args: '{"path":"a.ts"}', output: "full output" }));
  eq(readDetailBody(key).phase, "loaded", "a successful fetch lands as loaded");
  eq(readDetailBody(key).output, "full output", "the fetched output is kept");

  // "The backend does not have it" is not "empty output".
  const missing = detailBodyKey("run-live", "call-2");
  await loadDetailBody(missing, async () => null);
  eq(readDetailBody(missing).phase, "unavailable", "a missing body is unavailable, not empty");
  eq(readDetailBody(missing).output, undefined, "an unavailable body carries no empty output string");
  ok(Boolean(readDetailBody(missing).message), "an unavailable body explains itself");

  // Failure is visible, and a retry can recover.
  let attempts = 0;
  const flaky = detailBodyKey("run-live", "call-3");
  await loadDetailBody(flaky, async () => {
    attempts++;
    if (attempts === 1) throw new Error("bridge offline");
    return { args: "", output: "recovered" };
  });
  eq(readDetailBody(flaky).phase, "error", "a failed fetch reports an error");
  eq(readDetailBody(flaky).message, "bridge offline", "the failure keeps its reason");
  await loadDetailBody(flaky, async () => ({ output: "recovered" }), { force: true });
  eq(readDetailBody(flaky).phase, "loaded", "retrying a failed fetch can recover");
  eq(readDetailBody(flaky).output, "recovered", "the retry shows the recovered body");

  // A reload keeps the previous body visible instead of blanking the pane.
  const slow = detailBodyKey("run-live", "call-4");
  await loadDetailBody(slow, async () => ({ output: "first" }));
  let release: (() => void) | null = null;
  const pending = loadDetailBody(slow, async () => {
    await new Promise<void>((resolve) => { release = resolve; });
    return { output: "second" };
  }, { force: true });
  eq(readDetailBody(slow).phase, "loading", "a forced reload reports that it is loading");
  eq(readDetailBody(slow).output, "first", "a reload keeps the previous body visible");
  release!();
  await pending;
  eq(readDetailBody(slow).output, "second", "the reload replaces the body once it lands");

  // A stale in-flight request may not overwrite a newer one.
  const racing = detailBodyKey("run-live", "call-5");
  const order: string[] = [];
  let finishOld: (() => void) | null = null;
  const oldRequest = loadDetailBody(racing, async () => {
    await new Promise<void>((resolve) => { finishOld = resolve; });
    order.push("old");
    return { output: "old" };
  });
  const newRequest = loadDetailBody(racing, async () => { order.push("new"); return { output: "new" }; }, { force: true });
  await newRequest;
  finishOld!();
  await oldRequest;
  eq(readDetailBody(racing).output, "new", "a superseded response cannot overwrite the newer body");
  eq(order.join(","), "new,old", "the superseded request still completes without throwing");

  // Requests for the same record are coalesced instead of stacking up.
  let calls = 0;
  const once = detailBodyKey("run-live", "call-6");
  const first = loadDetailBody(once, async () => { calls++; return { output: "one" }; });
  const second = loadDetailBody(once, async () => { calls++; return { output: "one" }; });
  await Promise.all([first, second]);
  eq(calls, 1, "duplicate loads for one record share a single request");

  // Keying: one record's response can never land on another run's entry.
  const a = detailBodyKey("run-a", "call-1");
  const b = detailBodyKey("run-b", "call-1");
  await loadDetailBody(a, async () => ({ output: "run-a body" }));
  await loadDetailBody(b, async () => ({ output: "run-b body" }));
  eq(readDetailBody(a).output, "run-a body", "two runs with the same call id keep separate bodies");
  eq(readDetailBody(b).output, "run-b body", "two runs with the same call id keep separate bodies (b)");
  resetDetailBodies();
  eq(readDetailBody(a).phase, "idle", "resetting the cache drops every body");
}

testLoader().then(() => {
  process.stdout.write(`\n${passed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed > 0) process.exit(1);
});
