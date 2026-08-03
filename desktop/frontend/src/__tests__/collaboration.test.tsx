// Run: tsx src/__tests__/collaboration.test.tsx

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { readFileSync } from "node:fs";
import { IntentCountdown } from "../collab/components/IntentCountdown";
import { collabCopy, contributionLabel } from "../collab/copy";
import { collabReducer, detectSelfAgentIntent, initialCollabState, selectedTimelineItems } from "../collab/state";
import type { CollaborationState, CollaborationTimelineItem, CollaborationTransport, PendingIntent } from "../collab/types";
import { createMockCollaborationTransport, normalizeCollaborationAction, normalizeCollaborationItem } from "../collab/transport";
import { buildAgreeMessageInput, useCollabController, type CollabController } from "../collab/useCollabController";
import { LocaleProvider, t } from "../lib/i18n";

let passed = 0;
let failed = 0;
function ok(condition: boolean, label: string) {
  process.stdout.write(`  ${condition ? "PASS" : "FAIL"}  ${label}\n`);
  condition ? passed++ : failed++;
}
function equal(actual: unknown, expected: unknown, label: string) {
  ok(JSON.stringify(actual) === JSON.stringify(expected), `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

const item = (id: string, sequence: number, text = id): CollaborationTimelineItem => ({
  id, sequence, revision: 1, kind: "chat", actorId: "self", actorName: "Me", text, createdAt: `2026-08-03T00:00:0${sequence}Z`, referenceIds: [],
});

async function testCountdown() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  let starts = 0;
  const first: PendingIntent = { messageId: "one", revision: 1, instruction: "run", deadline: Date.now() + 20, status: "pending" };
  const countdown = (intent: PendingIntent, connected: boolean) => <LocaleProvider><IntentCountdown intent={intent} connected={connected} onStart={() => starts++} onStop={() => {}} onEdit={() => {}} /></LocaleProvider>;
  await act(async () => root.render(countdown(first, true)));
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 650)); });
  equal(starts, 1, "countdown has an atomic one-shot start latch");

  const offline: PendingIntent = { messageId: "offline", revision: 1, instruction: "must not run", deadline: Date.now() + 20, status: "pending" };
  await act(async () => root.render(countdown(offline, false)));
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 350)); });
  const dismissed = { ...offline, status: "dismissed" as const };
  await act(async () => root.render(countdown(dismissed, true)));
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 350)); });
  equal(starts, 1, "disconnect dismissal prevents delayed execution after reconnect");
  await act(async () => root.unmount());
}

async function testSessionTransportIsolation() {
  const first = createMockCollaborationTransport("multi-session-a");
  const second = createMockCollaborationTransport("multi-session-b");
  await first.host({ listenHost: "127.0.0.1", port: 39170, room: "room-a", memberName: "Alice", agentName: "A Agent", sessionID: "multi-session-a" });
  await second.join({ host: "10.0.0.8", port: 39171, room: "room-b", memberName: "Bob", agentName: "B Agent", sessionID: "multi-session-b" });
  let firstEvents = 0;
  let secondEvents = 0;
  const offFirst = first.subscribeEvent(() => firstEvents++);
  const offSecond = second.subscribeEvent(() => secondEvents++);
  await first.post({ requestID: "only-a", kind: "chat", text: "A update" });
  const [firstState, secondState] = await Promise.all([first.getState(), second.getState()]);
  equal([firstState.selfSessionId, firstState.room?.room, firstState.timeline.at(-1)?.text], ["multi-session-a", "room-a", "A update"], "first collaboration Session owns its Room state");
  equal([secondState.selfSessionId, secondState.room?.room, secondState.timeline.some((entry) => entry.id === "only-a")], ["multi-session-b", "room-b", false], "second collaboration Session does not receive another Session's timeline");
  equal([firstEvents, secondEvents], [1, 0], "event subscribers are isolated by collaboration Session");
  offFirst();
  offSecond();
}

async function testAgentBusyGuard() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const connected: CollaborationState = {
    status: "connected",
    room: { room: "busy-room", host: "127.0.0.1", port: 39170, latestSequence: 0 },
    selfMemberId: "self",
    selfSessionId: "busy-session",
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle", sessionId: "busy-session" } }],
    timeline: [],
  };
  let startCalls = 0;
  let finishStart: ((value: { ok: boolean }) => void) | undefined;
  const transport: CollaborationTransport = {
    async getState() { return connected; },
    async retry() { return connected; },
    async host() { return connected; },
    async join() { return connected; },
    async leave() {},
    async post() { return { ok: true }; },
    startAgent() {
      startCalls++;
      return new Promise((resolve) => { finishStart = resolve; });
    },
    async respond() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  let controller: CollabController | undefined;
  function Harness() {
    controller = useCollabController("busy-session", transport);
    return null;
  }
  const root = createRoot(document.getElementById("root")!);
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); });
  let first!: Promise<void>;
  let second!: Promise<void>;
  await act(async () => {
    first = controller!.startAgent("first", []);
    second = controller!.startAgent("second", []);
    await Promise.resolve();
  });
  equal([startCalls, first === second, controller!.agentBusy], [1, true, true], "concurrent Agent starts share one in-flight request");
  await act(async () => { finishStart?.({ ok: true }); await Promise.all([first, second]); });
  equal(controller!.agentBusy, false, "Agent start latch releases after the request settles");

  transport.startAgent = async () => ({ ok: false, code: "agent_busy", error: "backend wording may change" });
  await act(async () => { try { await controller!.startAgent("busy", []); } catch { /* expected business rejection */ } });
  equal([controller!.state.status, Boolean(controller!.state.actionError), controller!.state.retryable], ["connected", true, undefined], "Agent busy is a non-fatal action error and does not corrupt Room connection state");
  await act(async () => root.unmount());
}

async function main() {
  process.stdout.write("\ncollaboration state and countdown\n");
  const layoutCSS = readFileSync(new URL("../collab/collab.css", import.meta.url), "utf8");
  const appSource = readFileSync(new URL("../App.tsx", import.meta.url), "utf8");
  const workspaceSource = readFileSync(new URL("../collab/CollaborationWorkspace.tsx", import.meta.url), "utf8");
  const composerSource = readFileSync(new URL("../collab/components/CollaborationComposer.tsx", import.meta.url), "utf8");
  const connectionSource = readFileSync(new URL("../collab/components/ConnectionPanel.tsx", import.meta.url), "utf8");
  for (const [selector, row] of [["collab-topicbar", 1], ["collab-status-banner", 2], ["collab-scroll", 3], ["collab-footer", 4]] as const) {
    ok(new RegExp(`\\.${selector}\\s*\\{[^}]*grid-row:\\s*${row}(?:;|\\s)`).test(layoutCSS), `${selector} stays in grid row ${row} when the optional status banner is absent`);
  }
  ok(/\.collab-surface\s*\{[^}]*position:\s*relative/.test(layoutCSS), "collaboration session is embedded in the normal session surface");
  ok(/\.collab-modal\s*\{[^}]*position:\s*fixed/.test(layoutCSS), "Host and Join form uses a popup layer");
  ok(appSource.includes('activeTab?.sessionKind === "collaboration"') && appSource.includes("ensureBlankTab(target.scope, target.workspaceRoot)"), "Room starts from a workspace-owned blank Session");
  ok(appSource.includes('mode="dialog"') && workspaceSource.includes('mode?: "session" | "dialog"'), "connection popup and connected Session have separate presentation modes");
  ok(!workspaceSource.includes("collab-room-rail"), "embedded collaboration view reuses the existing Session List instead of duplicating a Room rail");
  ok(workspaceSource.includes('const usable = ownsRoom && Boolean(state.room)') && workspaceSource.includes('c("cachedBackground")'), "cached Room context remains usable and is explicitly disclosed while offline");
  ok(workspaceSource.includes("handleAction(controller.startAgent") && composerSource.includes("catch {"), "Agent action promises are consumed at both timeline and composer UI boundaries");
  ok(connectionSource.includes("await onHost(") && connectionSource.includes("await onJoin(") && connectionSource.includes("await onConnected?.()"), "popup closes only after Room connection and Session binding both complete");
  equal(detectSelfAgentIntent("帮我检查这个接口的错误日志"), "self_agent", "detects an explicit self-Agent instruction");
  equal(detectSelfAgentIntent("这个接口是不是有问题？"), "uncertain", "keeps ambiguous questions as suggestions");
  equal(detectSelfAgentIntent("小王，你检查一下这个接口"), "chat", "does not hijack instructions directed at another person");
  equal(detectSelfAgentIntent("接口已经修好了"), "chat", "does not execute completion statements");
  equal(contributionLabel(collabCopy(t), "verified"), "Verified", "known contribution kind uses its localized badge");
  equal(contributionLabel(collabCopy(t), "future_kind"), "Contribution", "unknown contribution kind falls back safely");

  const pending: PendingIntent = { messageId: "m1", revision: 2, instruction: "fix", deadline: Date.now() + 5_000, status: "pending" };
  let state = collabReducer(initialCollabState, { type: "PENDING_INTENT", intent: pending });
  state = collabReducer(state, { type: "PENDING_STATUS", id: "m1", status: "dismissed" });
  state = collabReducer(state, { type: "PENDING_INTENT", intent: { ...pending, deadline: Date.now() + 10_000 } });
  equal(state.pendingIntents.m1.status, "dismissed", "same message revision stays dismissed and is not re-prompted");
  state = collabReducer(state, { type: "PENDING_INTENT", intent: { ...pending, revision: 3, deadline: Date.now() + 10_000 } });
  equal(state.pendingIntents.m1.status, "pending", "a newer message revision may be detected again");
  state = collabReducer(state, { type: "SYNCING", reconnecting: true });
  equal(state.pendingIntents.m1.status, "dismissed", "disconnect/reconnect cancels pending auto-starts");
  const connectedState = collabReducer(initialCollabState, { type: "STATE", state: { status: "connected", members: [], timeline: [item("kept", 1)] } });
  const actionFailed = collabReducer(connectedState, { type: "ACTION_FAILED", error: "Agent busy" });
  equal([actionFailed.status, actionFailed.timeline.length, actionFailed.actionError], ["connected", 1, "Agent busy"], "business action failure preserves Room connection and cached timeline");

  let timelineState = collabReducer(initialCollabState, { type: "EVENT", item: item("same", 1, "old") });
  timelineState = collabReducer(timelineState, { type: "EVENT", item: { ...item("same", 1, "new"), revision: 2 } });
  equal([timelineState.timeline.length, timelineState.timeline[0].text, timelineState.timeline[0].revision], [1, "new", 2], "action result and runtime event merge by stable item id");

  timelineState = collabReducer(initialCollabState, { type: "STATE", state: { status: "connected", members: [], timeline: [item("three", 3), item("one", 1), item("two", 2)] } });
  timelineState = collabReducer(timelineState, { type: "TOGGLE_SELECT", id: "three" });
  timelineState = collabReducer(timelineState, { type: "TOGGLE_SELECT", id: "one" });
  equal(selectedTimelineItems(timelineState).map((entry) => [entry.id, entry.sequence, entry.actorName, entry.revision]), [["one", 1, "Me", 1], ["three", 3, "Me", 1]], "multi-select keeps authoritative order, author and revision");

  const roomEvent = normalizeCollaborationItem({
    eventId: "event-8", sequence: 8, type: "chat.posted", actorId: "member-a", createdAt: "2026-08-03T08:00:00Z",
    payload: { id: "timeline-8", sequence: 8, type: "chat", chat: { id: "timeline-8", authorId: "member-a", text: "raw RoomEvent payload", revision: 1, createdAt: "2026-08-03T08:00:00Z" } },
  }, new Map([["member-a", "Alice"]]));
  equal([roomEvent.id, roomEvent.kind, roomEvent.text, roomEvent.actorName, roomEvent.sequence], ["timeline-8", "chat", "raw RoomEvent payload", "Alice", 8], "raw RoomEvent unwraps its typed TimelineItem payload");
  const agree = buildAgreeMessageInput(item("target-item", 9), "agree-request");
  equal([agree.targetItemID, agree.reactionKind, agree.targetMemberID], ["target-item", "agree", undefined], "reaction targets a timeline item without polluting member targeting");
  const wrappedEvent = normalizeCollaborationItem({ event: { eventId: "event-9", sequence: 9 }, item: { id: "timeline-9", sequence: 9, type: "agent_result", agentResult: { id: "timeline-9", ownerId: "member-a", summary: "verified", revision: 1, createdAt: "2026-08-03T09:00:00Z" } } }, new Map([["member-a", "Alice"]]));
  equal([wrappedEvent.id, wrappedEvent.kind, wrappedEvent.text], ["timeline-9", "agent_result", "verified"], "desktop event wrapper normalizes its item projection");
  const queued = normalizeCollaborationAction({ requestId: "queued-1", queued: true, retryable: true, error: "host temporarily unavailable" });
  equal([queued.ok, queued.queued, queued.error], [true, true, "host temporarily unavailable"], "queued Outbox action is accepted while keeping its observable warning");
  const busy = normalizeCollaborationAction({ requestId: "busy-1", code: "agent_busy", retryable: true, error: "wording-independent" });
  equal([busy.ok, busy.code, busy.retryable], [false, "agent_busy", true], "structured Agent busy code survives transport normalization");

  await testSessionTransportIsolation();
  await testAgentBusyGuard();
  await testCountdown();
  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed) process.exit(1);
}

void main();
