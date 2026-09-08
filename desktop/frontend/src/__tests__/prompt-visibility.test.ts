// Regression tests for accepted user-prompt visibility across history hydration.
//
// Root cause this suite guards: the frontend creates the user bubble
// optimistically on submit (id `u<n>`), and the agent loop persists the user
// message only after TurnStarted fires. Any history replacement (switch-tab,
// session-activated re-hydration, same-session reopen, a slow in-flight
// open-topic load resolving after the user already sent) rebuilt `items` from
// the backend history and dropped that live bubble — leaving a session that
// shows only "thinking/running" with the accepted prompt invisible. A stale
// page fetched before the send must not erase the newer accepted message either.
//
// The merge must: keep accepted-but-unpersisted user bubbles, converge to
// exactly one record once the backend persists the prompt, keep two legitimate
// identical submissions distinct even across repeated/stale pages, never let a
// queued bubble stand in for persisted history, and never leak a bubble across
// sessions (including a session-path change before the history arrives).

import { initialState, reducer, type Item } from "../lib/useController";
import type { HistoryMessage, Meta } from "../lib/types";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) ok(true, label);
  else ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

function meta(sessionPath: string): Meta {
  return { label: "DeepSeek-R1", ready: true, eventChannel: "events", cwd: "/repo", sessionPath };
}

function countUser(items: Item[], text: string): number {
  return items.filter((item) => item.kind === "user" && item.text === text).length;
}

function countSteer(items: Item[], rawText: string): number {
  return items.filter((item) => item.kind === "notice" && item.level === "info" && item.text === `↪ ${rawText}`).length;
}

const SESSION = "/repo/session-a.jsonl";
const OTHER = "/repo/session-b.jsonl";

function userMessage(text: string): HistoryMessage {
  return { role: "user", content: text } satisfies HistoryMessage;
}

// Accept a prompt exactly like beginSendToTab does: optimistic user bubble +
// pending marker, then the backend TurnStarted acknowledges it.
function acceptedPrompt(text: string, sessionPath = SESSION): ReturnType<typeof reducer> {
  let s = reducer(initialState, { type: "meta", meta: meta(sessionPath) });
  s = reducer(s, { type: "user", text, seq: 0 });
  s = reducer(s, { type: "event", e: { kind: "turn_started" } as never });
  return s;
}

// ── Empty/stale history must not erase the accepted prompt ─────────────────

function testEmptyHistoryKeepsPrompt() {
  const s = acceptedPrompt("请开始做这件事");
  eq(countUser(reducer(s, { type: "history", messages: [], sessionPath: SESSION }).items, "请开始做这件事"), 1, "empty history keeps the accepted prompt visible");
  eq(countUser(reducer(s, {
    type: "history_page",
    page: { messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, sessionPath: SESSION },
    mode: "replace",
    sessionPath: SESSION,
  }).items, "请开始做这件事"), 1, "empty history_page replace keeps the accepted prompt visible");
}

function testStaleHistoryKeepsNewerPrompt() {
  const s = acceptedPrompt("较新的消息");
  const hydrated = reducer(s, { type: "history", messages: [userMessage("较旧的消息")], sessionPath: SESSION });
  eq(countUser(hydrated.items, "较旧的消息"), 1, "hydrated history keeps the older persisted message");
  eq(countUser(hydrated.items, "较新的消息"), 1, "stale history does not erase the newer accepted message");
  eq(hydrated.items.filter((item) => item.kind === "user").length, 2, "both the persisted and the accepted message are present");
}

// ── Once persisted, the transcript converges to exactly one record ─────────

function testPersistedPromptShowsOnce() {
  let s = acceptedPrompt("保持方向");
  s = reducer(s, { type: "history", messages: [], sessionPath: SESSION });
  eq(countUser(s.items, "保持方向"), 1, "unpersisted prompt kept after first refresh");
  s = reducer(s, { type: "history", messages: [userMessage("保持方向")], sessionPath: SESSION });
  eq(countUser(s.items, "保持方向"), 1, "persisted prompt shows exactly once after convergence");
  s = reducer(s, { type: "history", messages: [userMessage("保持方向")], sessionPath: SESSION });
  eq(countUser(s.items, "保持方向"), 1, "repeated hydration does not duplicate the persisted prompt");
}

// ── Old identical text (hydrated) + new identical text (live) ──────────────

function testOldAndNewIdenticalStayDistinct() {
  let s = reducer(initialState, { type: "meta", meta: meta(SESSION) });
  s = reducer(s, { type: "history", messages: [userMessage("X")], sessionPath: SESSION });
  // items now carry a hydrated (h-id) "X"; the user submits an identical "X".
  s = reducer(s, { type: "user", text: "X", seq: s.seq });
  eq(countUser(s.items, "X"), 2, "old hydrated X plus a new live X are two records before hydration");

  // A stale page carrying only the old X must not consume the new live X.
  s = reducer(s, { type: "history", messages: [userMessage("X")], sessionPath: SESSION });
  eq(countUser(s.items, "X"), 2, "stale history with the old X keeps the new live X as a second record");
  s = reducer(s, { type: "reset", queueSessionPath: SESSION });
  for (let n = 0; n < 2; n++) {
    s = reducer(s, { type: "history", messages: [userMessage("X")], sessionPath: SESSION });
    eq(countUser(s.items, "X"), 2, "same-session reload retains the old X as matching context");
  }
}

// ── Two live identical submissions stay two records across repeated pages ──

function testIdenticalSubmissionsStayDistinctAcrossRepeatedPages() {
  let s = acceptedPrompt("再来一次");
  s = reducer(s, { type: "user", text: "再来一次", seq: s.seq });
  s = reducer(s, { type: "event", e: { kind: "turn_started" } as never });
  eq(countUser(s.items, "再来一次"), 2, "two identical submissions are two records before hydration");

  // Partial persistence: only the first is persisted yet.
  s = reducer(s, { type: "history", messages: [userMessage("再来一次")], sessionPath: SESSION });
  eq(countUser(s.items, "再来一次"), 2, "partial persistence preserves the second identical submission");

  // Repeating the SAME stale page must stay stable at two records.
  s = reducer(s, { type: "history", messages: [userMessage("再来一次")], sessionPath: SESSION });
  eq(countUser(s.items, "再来一次"), 2, "repeating the stale page does not erase the second identical submission");

  // Both persisted: converge to exactly two.
  s = reducer(s, { type: "history", messages: [userMessage("再来一次"), userMessage("再来一次")], sessionPath: SESSION });
  eq(countUser(s.items, "再来一次"), 2, "two persisted identical submissions stay two records");
}

// ── A queued bubble must not count as persistence of an accepted bubble ────

function testQueuedDoesNotCoverAccepted() {
  let s = reducer(initialState, { type: "meta", meta: meta(SESSION) });
  s = reducer(s, { type: "startup_user_queued", id: "uq0", text: "X", seq: 0, queueSessionPath: SESSION });
  s = reducer(s, { type: "user", text: "X", seq: s.seq });
  eq(countUser(s.items, "X"), 2, "queued X and accepted X are distinct bubbles before hydration");

  s = reducer(s, { type: "history", messages: [], sessionPath: SESSION });
  eq(countUser(s.items, "X"), 2, "a queued bubble does not erase the accepted bubble on empty history");
}

// ── No cross-session leakage (history, reset, meta change before hydrate) ──

function testNoCrossSessionLeak() {
  const s = acceptedPrompt("属于 A");

  eq(countUser(reducer(s, { type: "history", messages: [], sessionPath: OTHER }).items, "属于 A"), 0, "history replacement to another session drops the live prompt");
  eq(countUser(reducer(s, { type: "reset", queueSessionPath: OTHER }).items, "属于 A"), 0, "reset to another session drops the live prompt");

  // Session path changes via meta BEFORE the history arrives.
  const metaChanged = reducer(s, { type: "meta", meta: meta(OTHER) });
  eq(countUser(metaChanged.items, "属于 A"), 0, "meta session-path change drops the previous session's live prompt");
  eq(countUser(reducer(metaChanged, { type: "history", messages: [], sessionPath: OTHER }).items, "属于 A"), 0, "post-meta-change history cannot resurrect the other session's prompt");

  // Same session meta refresh keeps the live prompt.
  const sameMeta = reducer(s, { type: "meta", meta: meta(SESSION) });
  eq(countUser(sameMeta.items, "属于 A"), 1, "same-session meta refresh keeps the live prompt");
}

// ── Same-session reset keeps the accepted prompt; new-session reset drops ──

function testResetOwnership() {
  const s = acceptedPrompt("刷新后还在");
  eq(countUser(reducer(s, { type: "reset", queueSessionPath: SESSION }).items, "刷新后还在"), 1, "same-session reset keeps the accepted prompt");
  eq(countUser(reducer(s, { type: "reset", dropQueued: true }).items, "刷新后还在"), 0, "brand-new session reset drops the accepted prompt");
}

// ── Reset then stale/repeated pages stays stable ───────────────────────────

function testResetThenStalePageStable() {
  let s = acceptedPrompt("稳定提示");
  s = reducer(s, { type: "reset", queueSessionPath: SESSION });
  eq(countUser(s.items, "稳定提示"), 1, "same-session reset keeps the prompt until hydration");

  // Empty page keeps it; repeating the empty page stays stable.
  s = reducer(s, { type: "history_page", page: { messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, sessionPath: SESSION }, mode: "replace", sessionPath: SESSION });
  eq(countUser(s.items, "稳定提示"), 1, "empty page after reset keeps the prompt");
  s = reducer(s, { type: "history_page", page: { messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, sessionPath: SESSION }, mode: "replace", sessionPath: SESSION });
  eq(countUser(s.items, "稳定提示"), 1, "repeating the empty page after reset stays stable");

  // Persisted page converges to one.
  s = reducer(s, { type: "history_page", page: { messages: [userMessage("稳定提示")], startTurn: 0, endTurn: 1, totalTurns: 1, hasOlder: false, sessionPath: SESSION }, mode: "replace", sessionPath: SESSION });
  eq(countUser(s.items, "稳定提示"), 1, "persisted page after reset converges to one record");
}

// ── Failed sends stay excluded ─────────────────────────────────────────────

function testFailedSendNotResurrected() {
  let s = reducer(initialState, { type: "meta", meta: meta(SESSION) });
  s = reducer(s, { type: "user", text: "会失败的发送", seq: 0 });
  s = reducer(s, { type: "send_failed", error: "Send failed: bridge unavailable" });
  eq(countUser(reducer(s, { type: "history", messages: [], sessionPath: SESSION }).items, "会失败的发送"), 0, "a failed send is not resurrected by history replacement");
}

// ── Original ordering across retained live types ───────────────────────────

function testRetainedLiveTypesKeepOrder() {
  let s = reducer(initialState, { type: "meta", meta: meta(SESSION) });
  s = reducer(s, { type: "user", text: "先发送", seq: 0 });
  s = reducer(s, { type: "event", e: { kind: "steer", text: "继续做" } as never });
  // live items: [user "先发送", steer "继续做"]
  const hydrated = reducer(s, { type: "history", messages: [], sessionPath: SESSION });
  const userIndex = hydrated.items.findIndex((item) => item.kind === "user" && item.text === "先发送");
  const steerIndex = hydrated.items.findIndex((item) => countSteer([item], "继续做") === 1);
  eq(userIndex >= 0, true, "retained user bubble is present after hydration");
  eq(steerIndex > userIndex, true, "retained steer confirmation keeps its original order after the user bubble");
}

testEmptyHistoryKeepsPrompt();
testStaleHistoryKeepsNewerPrompt();
testPersistedPromptShowsOnce();
testOldAndNewIdenticalStayDistinct();
testIdenticalSubmissionsStayDistinctAcrossRepeatedPages();
testQueuedDoesNotCoverAccepted();
testNoCrossSessionLeak();
testResetOwnership();
testResetThenStalePageStable();
testFailedSendNotResurrected();
testRetainedLiveTypesKeepOrder();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
