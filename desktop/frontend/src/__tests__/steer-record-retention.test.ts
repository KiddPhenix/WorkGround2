// Regression tests for mid-turn steer record visibility.
//
// Root cause this suite guards (#3660 contract): a successful steer's only
// live record is the "↪ <text>" notice the reducer appends from the backend
// Steer event. The agent loop persists the steer message only on its next step,
// so while a long-running tool/command still holds the turn the accepted
// guidance has no history row yet. Any history replacement (window refocus,
// session-activated re-hydration, same-session reopen) must keep that live
// record instead of silently erasing the only trace of an accepted steer; once
// the backend persists it, the transcript must converge to exactly one record.
// Legitimate repeated submissions of identical text must stay distinct.

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

function countSteer(items: Item[], rawText: string): number {
  return items.filter((item) => item.kind === "notice" && item.level === "info" && item.text === `↪ ${rawText}`).length;
}

function hasNotice(items: Item[], text: string): boolean {
  return items.some((item) => item.kind === "notice" && item.text === text);
}

const SESSION = "/scope/proj/session-a";
const OTHER = "/scope/proj/session-b";

// ── Running steer is visible live ───────────────────────────────────────────

function testLiveSteerVisible() {
  let s = reducer(initialState, { type: "event", e: { kind: "steer", text: "继续做 X" } });
  eq(countSteer(s.items, "继续做 X"), 1, "running steer appends one ↪ record to the live transcript");
  eq(s.items.filter((item) => item.kind === "user").length, 0, "a steer is not a backend turn — no user bubble");

  // Two steer events with identical text are two records, not one (no crude
  // same-text dedupe of legitimate repeated submissions).
  s = reducer(s, { type: "event", e: { kind: "steer", text: "继续做 X" } });
  eq(countSteer(s.items, "继续做 X"), 2, "a second identical steer event appends a second record");
}

// ── History refresh before the steer is persisted keeps the record ──────────

function testHistoryRefreshKeepsUnpersistedSteer() {
  let s = reducer(initialState, { type: "event", e: { kind: "steer", text: "等待命令完成后继续" } });
  s = reducer(s, {
    type: "history",
    messages: [{ role: "user", content: "旧消息" } satisfies HistoryMessage],
    sessionPath: SESSION,
  });
  eq(countSteer(s.items, "等待命令完成后继续"), 1, "history refresh without the persisted steer keeps the live record");
  eq(s.items.filter((item) => item.kind === "user").length, 1, "hydrated history is present next to the kept record");

  // history_page replace (the loader's actual path) behaves the same.
  s = reducer(initialState, { type: "event", e: { kind: "steer", text: "等待命令完成后继续" } });
  s = reducer(s, {
    type: "history_page",
    page: {
      messages: [{ role: "user", content: "旧消息" } satisfies HistoryMessage],
      startTurn: 0,
      endTurn: 1,
      totalTurns: 1,
      hasOlder: false,
    },
    mode: "replace",
    sessionPath: SESSION,
  });
  eq(countSteer(s.items, "等待命令完成后继续"), 1, "history_page replace keeps an unpersisted steer record");
}

// ── Once persisted, the transcript converges to one record ──────────────────

function testPersistedSteerShowsOnce() {
  let s = reducer(initialState, { type: "event", e: { kind: "steer", text: "保持方向" } });
  // First refresh while still unpersisted.
  s = reducer(s, { type: "history", messages: [{ role: "user", content: "旧消息" } satisfies HistoryMessage], sessionPath: SESSION });
  eq(countSteer(s.items, "保持方向"), 1, "unpersisted steer kept after the first refresh");
  // The agent loop persisted it; the next refresh must not duplicate it.
  s = reducer(s, {
    type: "history",
    messages: [
      { role: "user", content: "旧消息" } satisfies HistoryMessage,
      { role: "notice", content: "↪ 保持方向" } satisfies HistoryMessage,
    ],
    sessionPath: SESSION,
  });
  eq(countSteer(s.items, "保持方向"), 1, "persisted steer shows exactly once after convergence");

  // Two identical submissions, both persisted, stay two records.
  s = initialState;
  s = reducer(s, { type: "event", e: { kind: "steer", text: "再来一次" } });
  s = reducer(s, { type: "event", e: { kind: "steer", text: "再来一次" } });
  s = reducer(s, {
    type: "history",
    messages: [
      { role: "user", content: "旧消息" } satisfies HistoryMessage,
      { role: "notice", content: "↪ 再来一次" } satisfies HistoryMessage,
      { role: "notice", content: "↪ 再来一次" } satisfies HistoryMessage,
    ],
    sessionPath: SESSION,
  });
  eq(countSteer(s.items, "再来一次"), 2, "two persisted identical steers stay two records (no collapse)");
}

// ── Only steer records survive; transient notices do not resurrect ──────────

function testTransientNoticesDoNotResurrect() {
  let s = reducer(initialState, { type: "event", e: { kind: "steer", text: "可见记录" } });
  s = reducer(s, { type: "local_notice", level: "warn", text: "临时错误" });
  s = reducer(s, { type: "history", messages: [{ role: "user", content: "旧消息" } satisfies HistoryMessage], sessionPath: SESSION });
  eq(countSteer(s.items, "可见记录"), 1, "steer record survives the refresh");
  eq(hasNotice(s.items, "临时错误"), false, "transient warn notice does not resurrect after the refresh");
}

// ── Session ownership across reset ──────────────────────────────────────────

function testResetSessionOwnership() {
  // Same-session reload keeps accepted-but-unpersisted records.
  let s = reducer(initialState, { type: "meta", meta: meta(SESSION) });
  s = reducer(s, { type: "event", e: { kind: "steer", text: "属于 A" } });
  s = reducer(s, { type: "reset", queueSessionPath: SESSION });
  eq(countSteer(s.items, "属于 A"), 1, "same-session reset keeps the unpersisted steer record");

  // Switching to another session drops the record — no cross-session mixing.
  s = reducer(initialState, { type: "meta", meta: meta(SESSION) });
  s = reducer(s, { type: "event", e: { kind: "steer", text: "A 的记录" } });
  s = reducer(s, { type: "reset", queueSessionPath: OTHER });
  eq(countSteer(s.items, "A 的记录"), 0, "switching sessions drops the other session's steer record");
  eq(s.meta?.sessionPath, SESSION, "reset keeps the old meta until the new session hydrates");
}

testLiveSteerVisible();
testHistoryRefreshKeepsUnpersistedSteer();
testPersistedSteerShowsOnce();
testTransientNoticesDoNotResurrect();
testResetSessionOwnership();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
