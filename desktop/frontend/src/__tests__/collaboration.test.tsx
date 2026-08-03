// Run: tsx src/__tests__/collaboration.test.tsx

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { readFileSync } from "node:fs";
import { IntentCountdown } from "../collab/components/IntentCountdown";
import { collabCopy, contributionLabel } from "../collab/copy";
import { collabReducer, detectSelfAgentIntent, initialCollabState, replayableSelfAgentItems, selectedTimelineItems } from "../collab/state";
import { loadCollaborationIdentity, newCollaborationIdentity, saveCollaborationIdentity } from "../collab/identity";
import { buildCollaborationInvite, parseCollaborationInvite } from "../collab/invite";
import type { CollaborationState, CollaborationTimelineItem, CollaborationTransport, PendingIntent } from "../collab/types";
import { createMockCollaborationTransport, normalizeCollaborationAction, normalizeCollaborationItem, normalizeCollaborationState } from "../collab/transport";
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

  const failed: PendingIntent = { messageId: "failed", revision: 1, instruction: "retry", deadline: Date.now(), status: "failed", error: "workspace is still starting" };
  await act(async () => root.render(countdown(failed, true)));
  await act(async () => { (document.querySelector(".collab-intent-actions button") as HTMLButtonElement).click(); });
  equal(starts, 2, "failed countdown allows Start now to retry the Agent request");
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
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "busy-room" }; },
    async leave() {},
    async post(input) { return { ok: true, item: { ...item(input.requestID, 1, input.text), kind: input.kind } }; },
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
  await act(async () => { await controller!.postChat("这个接口是不是有问题？"); });
  ok(Object.keys(controller!.state.pendingIntents).length === 1, "uncertain self message receives a stoppable Agent countdown");
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

async function testOfflineSelfAgentIntervention() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const offline: CollaborationState = {
    status: "failed",
    mode: "host",
    room: { room: "solo-room", host: "127.0.0.1", port: 39170, latestSequence: 1 },
    selfMemberId: "self",
    selfSessionId: "solo-session",
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle", sessionId: "solo-session" } }],
    timeline: [{ ...item("outbox:old-chat", 2, "把现有修改提交一下"), localPending: true, syncStatus: "pending" }],
  };
  let agentStarts = 0;
  const transport: CollaborationTransport = {
    async getState() { return offline; },
    async retry() { return offline; },
    async host() { return offline; },
    async join() { return offline; },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "solo-room" }; },
    async leave() {},
    async post(input) { return { ok: true, queued: true, item: { ...item(`outbox:${input.requestID}`, 2, input.text), localPending: true, syncStatus: "pending" } }; },
    async startAgent(input) {
      agentStarts++;
      if (agentStarts === 1) return { ok: false, error: "workspace is still starting", retryable: true };
      return { ok: true, queued: true, item: { ...item(`outbox:${input.requestID}`, 3, input.instruction), kind: "agent_command", actorAgent: true, localPending: true, syncStatus: "pending", agentRunStatus: "running" } };
    },
    async respond() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  let controller: CollabController | undefined;
  function Harness() {
    controller = useCollabController("solo-session", transport);
    return null;
  }
  const root = createRoot(document.getElementById("root")!);
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); });
  await act(async () => { await Promise.resolve(); });
  const intent = Object.values(controller!.state.pendingIntents)[0];
  ok(Boolean(intent), "offline solo Host still detects a local Agent instruction");
  await act(async () => { await controller!.startPending(intent); });
  equal(controller!.state.pendingIntents[intent.messageId]?.status, "failed", "workspace startup failure remains visible and retryable");
  await act(async () => { await controller!.startPending(controller!.state.pendingIntents[intent.messageId]); });
  equal([agentStarts, controller!.state.status, controller!.state.pendingIntents[intent.messageId]?.status], [2, "failed", "dismissed"], "offline solo Host can retry and run its own Agent while Room sync remains retryable");
  await act(async () => root.unmount());
}

async function main() {
  process.stdout.write("\ncollaboration state and countdown\n");
  const layoutCSS = readFileSync(new URL("../collab/collab.css", import.meta.url), "utf8");
  const appSource = readFileSync(new URL("../App.tsx", import.meta.url), "utf8");
  const workspaceSource = readFileSync(new URL("../collab/CollaborationWorkspace.tsx", import.meta.url), "utf8");
  const composerSource = readFileSync(new URL("../collab/components/CollaborationComposer.tsx", import.meta.url), "utf8");
  const connectionSource = readFileSync(new URL("../collab/components/ConnectionPanel.tsx", import.meta.url), "utf8");
  const timelineSource = readFileSync(new URL("../collab/components/CollaborationTimeline.tsx", import.meta.url), "utf8");
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
  ok(connectionSource.includes("parseCollaborationInvite") && connectionSource.includes("loadCollaborationIdentity"), "connection popup imports invite strings and guides cached local identity");
  ok(timelineSource.includes("collab-presence-notice") && timelineSource.includes("collab-agent-run__marquee"), "presence events stay lightweight while Agent work uses a fixed animated status card");
  ok(/\.collab-message-actions\s*\{[^}]*opacity:\s*0/.test(layoutCSS) && timelineSource.includes("MoreHorizontal"), "per-message actions collapse to a hover icon toolbar and overflow menu");
  ok(/\.collab-topicbar\s*\{[^}]*--wails-draggable:\s*drag/.test(layoutCSS) && layoutCSS.includes("--wails-draggable: no-drag"), "collaboration title bar is draggable while controls remain interactive");
  ok(composerSource.includes("isComposerSubmitKey") && composerSource.includes("ModelSwitcher") && appSource.includes("submitKey={composerSubmitKey}"), "collaboration composer reuses configured send shortcut and active Session model selection");

  const invite = buildCollaborationInvite({ host: "192.168.1.8", port: 39170, room: "接口 联调", token: "shared secret" });
  equal(parseCollaborationInvite(invite), { host: "192.168.1.8", port: 39170, room: "接口 联调", token: "shared secret" }, "connection string round-trips Room and token");
  const ipv6Invite = buildCollaborationInvite({ host: "::1", port: 39170, room: "room-v6" });
  equal(parseCollaborationInvite(ipv6Invite), { host: "::1", port: 39170, room: "room-v6", token: undefined }, "connection string preserves bracketed IPv6 hosts");
  let invalidInvite = false;
  try { parseCollaborationInvite("https://example.com/room"); } catch { invalidInvite = true; }
  ok(invalidInvite, "non-WorkGround2 URLs are rejected as Room invites");

  const identityDOM = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
  Object.assign(globalThis, { localStorage: identityDOM.window.localStorage });
  equal(loadCollaborationIdentity(), undefined, "first collaboration connection has no silent placeholder identity");
  localStorage.setItem("collab:memberName", "成员");
  localStorage.setItem("collab:agentName", "Personal Agent");
  equal(loadCollaborationIdentity(), undefined, "legacy placeholder names still trigger first-time identity guidance");
  localStorage.clear();
  const draftIdentity = { ...newCollaborationIdentity(), memberName: "Alice", agentName: "Alice Agent", memberRole: "Backend" };
  saveCollaborationIdentity(draftIdentity);
  const cachedIdentity = loadCollaborationIdentity();
  equal([cachedIdentity?.memberID, cachedIdentity?.memberName, cachedIdentity?.memberRole, cachedIdentity?.agentID, cachedIdentity?.agentName], [draftIdentity.memberID, "Alice", "Backend", draftIdentity.agentID, "Alice Agent"], "completed local identity is cached with stable member and Agent ids");
  equal(detectSelfAgentIntent("帮我检查这个接口的错误日志"), "self_agent", "detects an explicit self-Agent instruction");
  equal(detectSelfAgentIntent("把现有修改提交一下"), "self_agent", "detects a Chinese commit instruction for the local Agent");
  equal(detectSelfAgentIntent("把现有的修改 commit 一下"), "self_agent", "detects a mixed-language commit instruction for the local Agent");
  equal(detectSelfAgentIntent("build and test the current changes"), "self_agent", "detects an English build instruction for the local Agent");
  equal(detectSelfAgentIntent("这个接口是不是有问题？"), "uncertain", "keeps ambiguous questions as suggestions");
  equal(detectSelfAgentIntent("小王，你检查一下这个接口"), "chat", "does not hijack instructions directed at another person");
  equal(detectSelfAgentIntent("接口已经修好了"), "chat", "does not execute completion statements");
  const replayChat = { ...item("outbox:queued-chat", 12, "把现有修改提交一下"), localPending: true, syncStatus: "pending" as const };
  const replayRun = { ...item("outbox:queued-run", 13, "把现有修改提交一下"), kind: "agent_command" as const, actorAgent: true, localPending: true, syncStatus: "pending" as const, referenceIds: [replayChat.id], agentRunStatus: "running" as const };
  equal(replayableSelfAgentItems({ ...initialCollabState, selfMemberId: "self", timeline: [replayChat] }).map((entry) => entry.id), [replayChat.id], "restored Outbox chat replays a missing local Agent intent");
  equal(replayableSelfAgentItems({ ...initialCollabState, selfMemberId: "self", timeline: [replayChat, replayRun] }).length, 0, "restored Agent run reference prevents duplicate intervention");
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
  const runningEvent = normalizeCollaborationItem({ id: "run-1", sequence: 10, type: "agent_run", agentRun: { id: "run-1", ownerId: "member-a", instruction: "fix it", status: "running", summary: "reading files" } }, new Map([["member-a", "Alice"]]));
  equal([runningEvent.kind, runningEvent.agentRunStatus, runningEvent.agentRunSummary], ["agent_command", "running", "reading files"], "Agent run status survives timeline normalization for the animated card");
  const rejoinedEvent = normalizeCollaborationItem({ id: "system-1", sequence: 11, type: "system", system: { kind: "member.rejoined", memberId: "member-a" } }, new Map([["member-a", "Alice"]]));
  equal([rejoinedEvent.systemKind, rejoinedEvent.actorName], ["member.rejoined", "Alice"], "member.rejoined remains typed for lightweight notice rendering");
  const queued = normalizeCollaborationAction({ requestId: "queued-1", queued: true, retryable: true, error: "host temporarily unavailable" });
  equal([queued.ok, queued.queued, queued.error], [true, true, "host temporarily unavailable"], "queued Outbox action is accepted while keeping its observable warning");
  const queuedState = normalizeCollaborationState({
    status: "reconnecting", room: "room-a", memberId: "self", sessionId: "session-a",
    snapshot: { room: { id: "room-a", latestSequence: 3 }, latestSequence: 3, members: [{ id: "self", name: "Me", status: "online", agent: { id: "agent", name: "Agent", status: "idle" } }], timeline: [] },
    outbox: [{ requestId: "queued-chat", status: "pending", item: { id: "outbox:queued-chat", sequence: 4, type: "chat", chat: { id: "outbox:queued-chat", authorId: "self", text: "not swallowed", revision: 1, createdAt: "2026-08-03T10:00:00Z" } } }],
  });
  equal([queuedState.timeline[0].text, queuedState.timeline[0].syncStatus, queuedState.timeline[0].localPending, queuedState.timeline[0].actorName], ["not swallowed", "pending", true, "Me"], "persisted Outbox is visible as a local pending timeline message");
  let pendingState = collabReducer(initialCollabState, { type: "STATE", state: queuedState });
  pendingState = collabReducer(pendingState, { type: "STATE", state: { ...queuedState, status: "connected", timeline: [item("synced-chat", 4, "not swallowed")] } });
  equal(pendingState.timeline.map((entry) => entry.id), ["synced-chat"], "authoritative sync replaces the local pending projection without a ghost duplicate");
  const busy = normalizeCollaborationAction({ requestId: "busy-1", code: "agent_busy", retryable: true, error: "wording-independent" });
  equal([busy.ok, busy.code, busy.retryable], [false, "agent_busy", true], "structured Agent busy code survives transport normalization");

  await testSessionTransportIsolation();
  await testAgentBusyGuard();
  await testOfflineSelfAgentIntervention();
  await testCountdown();
  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed) process.exit(1);
}

void main();
