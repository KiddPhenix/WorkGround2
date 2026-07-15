// Run: tsx src/__tests__/send-failed.test.ts

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { initialState, reducer, replayPendingPromptsForActiveTab } from "../lib/useController";
import type { WireEvent } from "../lib/types";

let passed = 0;
let failed = 0;

function eq(a: unknown, b: unknown, label: string) {
  if (a === b) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`);
    failed += 1;
  }
}

console.log("\nsend failure feedback");

const sent = reducer({ ...initialState }, { type: "user", text: "hello", seq: 0 });
eq(sent.items.length, 1, "submit appends the user bubble immediately");
eq(sent.items[0].kind === "user" && sent.items[0].text, "hello", "bubble carries the submitted text");
eq(sent.running, true, "submit marks the turn running");
eq(sent.pendingUser, "hello", "submit tracks the optimistic bubble");
const duplicatePending = reducer(sent, { type: "user", text: "hello", seq: sent.seq });
eq(duplicatePending.items.filter((it) => it.kind === "user").length, 1, "duplicate pending submit does not add a second user bubble");

const hiddenSubmit = reducer({ ...initialState }, { type: "user", text: "display prompt", submitText: "hidden context\ndisplay prompt", seq: 0 });
eq(
  hiddenSubmit.items[0].kind === "user" && hiddenSubmit.items[0].submitText,
  "hidden context\ndisplay prompt",
  "optimistic user bubble preserves submit-only context",
);
const duplicateHiddenSubmit = reducer(hiddenSubmit, { type: "user", text: "display prompt", submitText: "hidden context\ndisplay prompt", seq: hiddenSubmit.seq });
eq(duplicateHiddenSubmit.items.filter((it) => it.kind === "user").length, 1, "duplicate pending submit with submit-only context is ignored");

const startupQueued = reducer({ ...initialState }, {
  type: "startup_user_queued",
  id: "u0",
  text: "run after startup",
  submitText: "hidden context\nrun after startup",
  seq: 0,
  queueSessionPath: "/repo/session.jsonl",
});
const startupQueuedBubble = startupQueued.items[0];
eq(startupQueuedBubble.kind === "user" && startupQueuedBubble.queued, true, "startup submit is visible as queued");
eq(startupQueued.running, false, "startup queue does not pretend a turn is running");
eq(startupQueued.pendingUser, undefined, "startup queue does not create a backend pending marker");
const startupQueuedAfterHydrate = reducer(startupQueued, { type: "history", messages: [], sessionPath: "/repo/session.jsonl" });
eq(startupQueuedAfterHydrate.items[0].kind === "user" && startupQueuedAfterHydrate.items[0].queued, true, "startup queue survives empty history hydration");
const startupQueuedAfterReset = reducer(startupQueued, { type: "reset" });
eq(startupQueuedAfterReset.items[0].kind === "user" && startupQueuedAfterReset.items[0].queued, true, "startup queue survives session reset during activation");
eq(startupQueuedAfterReset.seq, startupQueued.seq, "startup queue keeps sequence identity across reset");

// Same tab, different sessionPath: queued items must be filtered out
const altSessionQueued = reducer({ ...initialState }, {
  type: "startup_user_queued",
  id: "u0",
  text: "msg for session A",
  seq: 0,
  queueSessionPath: "/repo/sessions/a.jsonl",
});
const altSessionReset = reducer(altSessionQueued, { type: "reset" });
eq(altSessionReset.items.length, 1, "reset preserves queued items before history resolves session identity");
const altSessionHistory = reducer(altSessionReset, { type: "history", messages: [], sessionPath: "/repo/sessions/b.jsonl" });
eq(altSessionHistory.items.length, 0, "queued item from session A is dropped when history resolves to session B");

// Same sessionPath: queued items survive
const sameSessionQueued = reducer({ ...initialState }, {
  type: "startup_user_queued",
  id: "u0",
  text: "msg for same session",
  seq: 0,
  queueSessionPath: "/repo/sessions/same.jsonl",
});
const sameSessionReset = reducer(sameSessionQueued, { type: "reset" });
const sameSessionHistory = reducer(sameSessionReset, { type: "history", messages: [], sessionPath: "/repo/sessions/same.jsonl" });
eq(sameSessionHistory.items.length, 1, "queued item survives when history resolves to the same sessionPath");
eq(sameSessionHistory.items[0].kind === "user" && sameSessionHistory.items[0].text, "msg for same session", "queued item text survives same-session history hydration");

// Empty sessionPath in queued item, non-empty in history: drop (conservative)
const emptyPathQueued = reducer({ ...initialState }, {
  type: "startup_user_queued",
  id: "u0",
  text: "msg with empty sessionPath",
  seq: 0,
  queueSessionPath: "",
});
const emptyPathReset = reducer(emptyPathQueued, { type: "reset" });
const emptyPathHistory = reducer(emptyPathReset, { type: "history", messages: [], sessionPath: "/repo/sessions/b.jsonl" });
eq(emptyPathHistory.items.length, 0, "queued item with empty sessionPath is dropped when history has a known sessionPath");

// Non-empty sessionPath in queued item, empty in history: drop (conservative)
const knownPathQueued = reducer({ ...initialState }, {
  type: "startup_user_queued",
  id: "u0",
  text: "msg with known sessionPath",
  seq: 0,
  queueSessionPath: "/repo/sessions/a.jsonl",
});
const knownPathReset = reducer(knownPathQueued, { type: "reset" });
const knownPathHistory = reducer(knownPathReset, { type: "history", messages: [], sessionPath: "" });
eq(knownPathHistory.items.length, 0, "queued item with known sessionPath is dropped when history has empty sessionPath");

// Both empty: preserve (no session identity to violate)
const bothEmptyQueued = reducer({ ...initialState }, {
  type: "startup_user_queued",
  id: "u0",
  text: "msg with both empty",
  seq: 0,
  queueSessionPath: "",
});
const bothEmptyReset = reducer(bothEmptyQueued, { type: "reset" });
const bothEmptyHistory = reducer(bothEmptyReset, { type: "history", messages: [], sessionPath: "" });
eq(bothEmptyHistory.items.length, 1, "queued item with empty sessionPath survives when history also has empty sessionPath");

const startupSending = reducer(startupQueued, { type: "startup_user_sending", id: "u0" });
eq(startupSending.items[0].kind === "user" && startupSending.items[0].queued, false, "ready drain promotes the queued bubble to sending");
eq(startupSending.running, true, "ready drain starts the foreground state");
eq(startupSending.pendingUser, "run after startup", "ready drain tracks backend confirmation");

const startupStillQueued = reducer(startupSending, { type: "startup_user_waiting", id: "u0", error: "provider unavailable" });
eq(startupStillQueued.items[0].kind === "user" && startupStillQueued.items[0].queued, true, "failed startup drain keeps the message queued");
eq(startupStillQueued.items[0].kind === "user" && startupStillQueued.items[0].queueError, "provider unavailable", "queued message retains the startup error");
eq(startupStillQueued.running, false, "failed startup drain returns to idle");
eq(startupStillQueued.pendingUser, undefined, "failed startup drain clears the pending marker");

const confirmed = reducer(sent, { type: "event", e: { kind: "text", text: "hi" } as WireEvent });
eq(confirmed.items.filter((it) => it.kind === "user").length, 1, "first backend event confirms without duplicating");
eq(confirmed.pendingUser, undefined, "confirmation clears the pending marker");

const memoryStatsEvent = {
  kind: "memory_compiler_stats",
  memoryCompiler: {
    injected: true,
    usefulIR: true,
    compiledTokens: 640,
    irOverheadTokens: 120,
    memoryReferences: 2,
    constraints: 1,
    riskNotes: 0,
    executionSteps: 3,
    totalNodes: 18,
    highSignalNodes: 4,
    toolResultNodes: 6,
    decisionNodes: 2,
    strategyCount: 5,
    learningCount: 3,
  },
} as WireEvent;
const statsOnly = reducer(sent, { type: "event", e: memoryStatsEvent });
eq(statsOnly, sent, "memory compiler stats do not confirm or mutate the visible turn");
const startedThenStats = reducer(reducer(sent, { type: "event", e: { kind: "turn_started" } as WireEvent }), { type: "event", e: memoryStatsEvent });
eq(startedThenStats.items.length, 2, "memory compiler stats do not add transcript items after turn start");
const compilerCitationMessage = {
  kind: "message",
  memoryCitations: [{ kind: "compiler_reference", source: "Memory v5", note: "evidence: bash succeeded" }],
} as WireEvent;
const citationOnlyFinal = reducer(reducer(sent, { type: "event", e: { kind: "turn_started" } as WireEvent }), { type: "event", e: compilerCitationMessage });
eq(citationOnlyFinal.items.length, 1, "memory compiler citations alone do not leave an empty assistant bubble");
eq(citationOnlyFinal.items.some((it) => it.kind === "assistant"), false, "memory compiler citations alone stay hidden from the transcript");
const textThenCitationFinal = reducer(reducer(startedThenStats, { type: "event", e: { kind: "text", text: "done" } as WireEvent }), { type: "event", e: compilerCitationMessage });
const citedAssistant = textThenCitationFinal.items.find((it) => it.kind === "assistant");
eq(citedAssistant?.kind === "assistant" && citedAssistant.text, "done", "memory compiler citations preserve existing assistant text");
eq(citedAssistant?.kind === "assistant" && citedAssistant.memoryCitations?.length, 1, "memory compiler citations attach to real assistant content");

const failedState = reducer(sent, { type: "send_failed", error: "Send failed: bridge unavailable" });
const failedBubble = failedState.items.find((it) => it.kind === "user");
eq(failedBubble?.kind === "user" && failedBubble.failed, true, "send_failed marks the bubble failed");
const notice = failedState.items[failedState.items.length - 1];
eq(notice.kind, "notice", "send_failed appends a notice");
eq(notice.kind === "notice" && notice.level, "warn", "the notice is a warning");
eq(failedState.running, false, "send_failed stops the running indicator");
eq(failedState.pendingUser, undefined, "send_failed clears the pending marker");

const shellSent = reducer({ ...initialState }, { type: "user", text: "!ls", seq: 0 });
const shellFailed = reducer(shellSent, { type: "send_failed", error: "Command failed: workspace is still starting" });
const shellNotice = shellFailed.items[shellFailed.items.length - 1];
eq(shellNotice.kind, "notice", "rejected shell command appends a visible notice");
eq(shellNotice.kind === "notice" && shellNotice.text.includes("workspace is still starting"), true, "shell rejection notice includes the backend error");

const lateFailure = reducer(confirmed, { type: "send_failed", error: "Send failed: late" });
eq(lateFailure, confirmed, "send_failed after backend confirmation is a no-op");

const beforeMcpReady = { ...initialState };
const mcpReady = reducer(beforeMcpReady, { type: "event", e: { kind: "mcp_surface_ready" } as WireEvent });
eq(mcpReady, beforeMcpReady, "mcp_surface_ready is accepted as a deliberate no-op");
const pendingMcpReady = reducer(sent, { type: "event", e: { kind: "mcp_surface_ready" } as WireEvent });
eq(pendingMcpReady, sent, "mcp_surface_ready does not confirm a pending submit");
const failedAfterMcpReady = reducer(pendingMcpReady, { type: "send_failed", error: "Send failed: bridge unavailable" });
const failedAfterMcpReadyBubble = failedAfterMcpReady.items.find((it) => it.kind === "user");
eq(
  failedAfterMcpReadyBubble?.kind === "user" && failedAfterMcpReadyBubble.failed,
  true,
  "send_failed still marks a pending submit after mcp readiness",
);

const here = dirname(fileURLToPath(import.meta.url));
const appSource = readFileSync(resolve(here, "../App.tsx"), "utf8");
const typesSource = readFileSync(resolve(here, "../lib/types.ts"), "utf8");
const controllerSource = readFileSync(resolve(here, "../lib/useController.ts"), "utf8");
eq(typesSource.includes('"mcp_surface_ready"'), true, "TypeScript EventKind declares mcp_surface_ready");
eq(controllerSource.includes('e.kind === "memory_compiler_stats" || e.kind === "mcp_surface_ready"'), true, "reducer handles mcp_surface_ready before optimistic confirmation");
eq(
  /state\.approval!\.tool === "exit_plan_mode" && allow\) await applyCollaborationMode\("normal"\);/.test(appSource),
  true,
  "plan approval clears the remembered plan restore intent before execution",
);
eq(
  !/exit_plan_mode[\s\S]{0,240}rememberUserIntent:\s*false/.test(appSource),
  true,
  "plan approval must not preserve stale plan restore intent",
);
eq(
  !appSource.includes("rememberUserIntent"),
  true,
  "collaboration mode changes always reconcile the remembered plan restore intent",
);

const unsent = reducer(sent, { type: "unsend" });
eq(unsent.pendingUser, undefined, "unsend clears the pending marker");
eq(unsent.discardTurn, true, "unsend discards the in-flight turn");

const planApprovalFirst = reducer(
  { ...initialState },
  { type: "event", e: { kind: "approval_request", approval: { id: "plan-1", tool: "exit_plan_mode", subject: "Approve plan" } } as WireEvent },
);
const planTurnDoneAfter = reducer(planApprovalFirst, { type: "event", e: { kind: "turn_done" } as WireEvent });
eq(
  planTurnDoneAfter.approval?.id,
  "plan-1",
  "turn_done preserves out-of-order plan approval",
);
eq(planTurnDoneAfter.running, true, "preserved plan approval keeps the tab running");
eq(planTurnDoneAfter.pendingPrompt, true, "preserved plan approval keeps the prompt gate active");

let replayCalls = 0;
replayPendingPromptsForActiveTab(undefined, () => {
  replayCalls += 1;
  return Promise.resolve();
});
eq(replayCalls, 0, "no active tab does not replay pending prompts");

replayPendingPromptsForActiveTab("tab-a", () => {
  replayCalls += 1;
  return Promise.resolve();
});
eq(replayCalls, 1, "active tab switch replays pending prompts");

replayPendingPromptsForActiveTab("tab-b", () => {
  replayCalls += 1;
  return Promise.reject(new Error("bridge unavailable"));
});
await new Promise((resolve) => setTimeout(resolve, 0));
eq(replayCalls, 2, "replay bridge failures are swallowed by the tab-switch effect");

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
