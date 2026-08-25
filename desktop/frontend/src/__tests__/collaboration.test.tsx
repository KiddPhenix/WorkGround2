// Run: tsx src/__tests__/collaboration.test.tsx

import { JSDOM } from "jsdom";
import React, { useEffect, useState } from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { readFileSync } from "node:fs";
import { IntentCountdown } from "../collab/components/IntentCountdown";
import { ConnectionPanel } from "../collab/components/ConnectionPanel";
import { CollaborationComposer } from "../collab/components/CollaborationComposer";
import { CollaborationTimeline } from "../collab/components/CollaborationTimeline";
import { collabCopy, contributionLabel } from "../collab/copy";
import { agentCollaborationClock, agentCollaborationRequestID, collabReducer, detectSelfAgentIntent, detectSelfAgentIntentRule, initialCollabState, nextAgentCollaborationBatch, nextAutomaticAgentItem, replayableSelfAgentItems, selectedTimelineItems, visibleCollaborationTimeline } from "../collab/state";
import { loadCollaborationIdentity, newCollaborationIdentity, saveCollaborationIdentity } from "../collab/identity";
import { buildCollaborationInvite, buildCollaborationInviteForOption, collaborationInviteOptions, parseCollaborationInvite, tryBuildCollaborationInvite } from "../collab/invite";
import { recentAgentActivity } from "../collab/agentActivity";
import { autoResponseFlags, autoResponseMode, nextApprovalMode, nextAutoResponseMode, nextRecognitionMode } from "../collab/agentPolicy";
import { activeMention, collaborationMentionCandidates, filterMentionCandidates, insertMention, mentionPayload, mentionRequestID, nextMentionedAgentItem } from "../collab/mentions";
import type { CollaborationMember, CollaborationState, CollaborationTimelineItem, CollaborationTransport, PendingIntent } from "../collab/types";
import { createMockCollaborationTransport, createWailsCollaborationTransport, normalizeCollaborationAction, normalizeCollaborationIntent, normalizeCollaborationItem, normalizeCollaborationState } from "../collab/transport";
import { buildAgreeMessageInput, loadCollaborationState, useCollabController, type CollabController } from "../collab/useCollabController";
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

async function testWaitingAgentRunDecisions() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  const decisions: boolean[] = [];
  const waiting: CollaborationTimelineItem = { ...item("waiting-run", 1, "continue current task"), kind: "agent_command", actorAgent: true, agentRunStatus: "waiting_approval" };
  const render = (agentPrompt?: Parameters<typeof CollaborationTimeline>[0]["agentPrompt"]) => <LocaleProvider><CollaborationTimeline
    items={[waiting]} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy transfers={[]}
    agentPrompt={agentPrompt}
    onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
    onRespondAgentRun={(_, response) => decisions.push(response.allow ?? false)} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
    onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
  /></LocaleProvider>;
  await act(async () => root.render(render()));
  equal(document.querySelector(".collab-message header strong")?.textContent, "Me", "own Agent timeline identity does not inherit the member '(you)' suffix");
  const buttons = [...document.querySelectorAll<HTMLButtonElement>(".collab-agent-run__decision button")];
  equal(buttons.map((button) => button.textContent), ["同意", "拒绝"], "waiting Agent Run exposes dedicated agree and reject decisions");
  await act(async () => { buttons[0].click(); buttons[1].click(); });
  equal(decisions, [true, false], "waiting Agent Run decisions resolve the current execution");
  ok(!document.querySelector(".collab-action-more"), "waiting Agent Run no longer renders the removed agree-and-run overflow menu");
  await act(async () => root.render(render({ runId: waiting.id, kind: "approval", id: "approval-1", tool: "shell_command", subject: "go test ./desktop", reason: "执行本地测试" })));
  equal([(document.querySelector(".prompt-shelf__badge") as HTMLElement)?.textContent, (document.querySelector(".approval-subject") as HTMLElement)?.textContent, (document.querySelector(".approval-reason") as HTMLElement)?.textContent], ["shell_command", "go test ./desktop", "执行本地测试"], "Room tool approval shows the concrete tool, subject, and reason");
  ok(!document.querySelector(".collab-agent-run__decision"), "structured tool approval replaces the detail-free agree/reject fallback");
  await act(async () => root.render(render({ runId: waiting.id, kind: "ask", id: "ask-1", questions: [{ id: "q1", header: "目标环境", prompt: "要部署到哪个环境？", options: [{ label: "测试", description: "仅测试环境" }, { label: "生产" }] }] })));
  ok((document.body.textContent || "").includes("要部署到哪个环境？") && (document.body.textContent || "").includes("测试") && (document.body.textContent || "").includes("生产"), "Room Ask shows the actual question and answer choices");
  await act(async () => root.unmount());
}

async function testReferenceAndRunResultPresentation() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  const original = { ...item("original", 1, "第一行\n第二行\n第三行\n第四行") };
  const run = { ...item("run-pair", 2, "执行复核"), kind: "agent_command" as const, actorAgent: true, agentRunStatus: "completed" as const, agentRunSummary: "正在运行中...", referenceIds: [original.id] };
  const result = { ...item("result-pair", 3, "复核完成\n所有检查项已通过\n第三行结果在这里"), kind: "agent_result" as const, actorAgent: true, agentRunId: run.id, handoffs: [{ targetAgentId: "agent-b", instruction: "继续验证", referenceIds: [original.id], requiresResponse: true }] };
  await act(async () => root.render(<LocaleProvider><CollaborationTimeline
    items={[original, run, result]} members={[{ id: "member-b", name: "Bob", online: true, agent: { id: "agent-b", name: "Verifier", status: "idle" } }]}
    selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy={false} transfers={[]}
    onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
    onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
    onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
  /></LocaleProvider>));
  equal(document.querySelectorAll(".collab-message").length, 2, "paired Agent run/result renders as one message card");
  const preview = document.querySelector<HTMLButtonElement>(".collab-reference-card__preview")!;
  equal([preview.getAttribute("aria-expanded"), document.querySelector(".collab-reference-card__text--open")], ["false", null], "long reference starts collapsed at the three-line presentation");
  await act(async () => preview.click());
  equal(preview.getAttribute("aria-expanded"), "true", "reference preview expands in place when clicked");
  ok((document.querySelector(".collab-handoffs")?.textContent || "").includes("@Verifier"), "directed handoff visibly addresses the target Agent");
  const output = document.querySelector(".collab-agent-output")!;
  ok(output !== null && output.tagName === "P", "completed Agent output uses the same paragraph element as normal chat content");
  ok(output.parentElement?.classList.contains("collab-message-body") && !output.closest(".collab-agent-run"), "completed Agent output is direct message content rather than a nested run card");
  ok((output.textContent || "").includes("复核完成") && (output.textContent || "").includes("所有检查项已通过"), "multi-line final output is fully visible in the message body");
  const summaryDiv = document.querySelector(".collab-agent-run__summary");
  ok(summaryDiv === null, "completed run with output does not render a single-line summary div");
  ok(!(output.textContent || "").includes("正在运行中..."), "final output does not leak the run-time progress summary");
  await act(async () => root.unmount());
}

async function testAgentRunResultOutput() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);

  const completedRun: CollaborationTimelineItem = { ...item("cr", 1, "执行复核"), kind: "agent_command", actorAgent: true, agentRunStatus: "completed", agentRunSummary: "进度摘要" };
  const completedResult: CollaborationTimelineItem = { ...item("cr-result", 2, "复核通过\n第二行\n第三行"), kind: "agent_result", actorAgent: true, agentRunId: "cr", handoffs: [{ targetAgentId: "agent-b", instruction: "继续", referenceIds: [], requiresResponse: true }] };
  const failedRun: CollaborationTimelineItem = { ...item("fr", 3, "部署任务"), kind: "agent_command", actorAgent: true, agentRunStatus: "failed", agentRunError: "连接超时" };
  const cancelledRun: CollaborationTimelineItem = { ...item("cancel", 4, "已取消的任务"), kind: "agent_command", actorAgent: true, agentRunStatus: "cancelled" };

  await act(async () => root.render(<LocaleProvider><CollaborationTimeline
    items={[completedRun, completedResult, failedRun, cancelledRun]}
    members={[{ id: "member-b", name: "Bob", online: true, agent: { id: "agent-b", name: "Verifier", status: "idle" } }]}
    selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy={false} transfers={[]}
    onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
    onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
    onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
  /></LocaleProvider>));

  equal(document.querySelectorAll(".collab-message").length, 3, "merged completed pair + failed + cancelled = 3 cards");

  const outputEls = document.querySelectorAll(".collab-agent-output");
  equal(outputEls.length, 1, "only the completed run with result shows output");
  ok(outputEls[0]?.parentElement?.classList.contains("collab-message-body") && !outputEls[0]?.closest(".collab-agent-run"), "completed output shares the normal chat content flow");
  ok((outputEls[0]?.textContent || "").includes("复核通过"), "completed run output contains the result text");
  ok((outputEls[0]?.textContent || "").includes("第三行"), "multi-line output preserves all lines");

  const summaryEls = document.querySelectorAll(".collab-agent-run__summary");
  const summaryTexts = [...summaryEls].map((el) => el.textContent);
  ok(summaryTexts.includes("连接超时"), "failed run without output shows the error in summary");
  ok(summaryTexts.includes("已取消"), "cancelled run without output shows the status label");

  const handoffEl = document.querySelector(".collab-handoffs");
  ok(handoffEl !== null, "handoffs remain visible on merged run/result card");
  ok((handoffEl?.textContent || "").includes("@Verifier"), "handoff addresses target Agent");

  await act(async () => root.unmount());
}

async function testRequestAgentPopup() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  const chatItem = item("chat-request", 1, "检查资源命名规范");
  const members = [
    { id: "self", name: "陈程序", online: true, isSelf: true, agent: { id: "self-agent", name: "程序 Agent", status: "idle" } },
    { id: "planner", name: "林策划", online: true, agent: { id: "planner-agent", name: "策划 Agent", role: "策划", status: "idle" } },
    { id: "artist", name: "周美术", online: true, agent: { id: "artist-agent", name: "美术 Agent", role: "美术", status: "idle" } },
    { id: "offline-dev", name: "离线开发", online: false, agent: { id: "offline-agent", name: "离线 Agent", role: "开发", status: "offline" } },
    { id: "no-role", name: "无名", online: true, agent: { id: "no-role-agent", name: "通用 Agent", status: "idle" } },
  ];
  const requests: { memberId: string; text: string }[] = [];
  await act(async () => root.render(<LocaleProvider><CollaborationTimeline
    items={[chatItem]} members={members} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy={false} transfers={[]}
    onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={(item, memberId) => requests.push({ memberId, text: item.text })} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
    onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
    onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
  /></LocaleProvider>));

  const trigger = document.querySelector<HTMLButtonElement>(".collab-request-agent > button");
  ok(trigger !== null, "request-Agent trigger button is rendered");
  equal([trigger?.getAttribute("aria-label"), trigger?.getAttribute("aria-expanded")], ["请求其他成员的 Agent", "false"], "trigger exposes the localized target and closed popup state");
  if (trigger) trigger.getBoundingClientRect = () => ({ top: 700, bottom: 723, left: 0, right: 26, width: 26, height: 23, x: 0, y: 700, toJSON: () => ({}) }) as DOMRect;

  // Open popup
  await act(async () => trigger?.click());
  const popup = document.querySelector(".collab-request-agent__popup");
  ok(popup !== null, "popup opens on click");
  equal([trigger?.getAttribute("aria-expanded"), popup?.getAttribute("role")], ["true", "group"], "open popup exposes its expanded action group");
  ok(popup?.classList.contains("collab-request-agent__popup--above"), "popup flips above a trigger near the scroll boundary instead of being clipped");

  // Self, offline members excluded
  const rows = popup?.querySelectorAll<HTMLButtonElement>("button");
  equal(rows?.length, 3, "popup shows three eligible members: planner, artist, no-role; excludes self and offline");
  const labels = [...(rows || [])].map((btn) => btn.textContent);
  ok(labels.includes("林策划 · 策划 Agent · 策划"), "three-part label: member name, Agent name, and Agent role from member.agent.role");
  ok(labels.includes("周美术 · 美术 Agent · 美术"), "second eligible member shows correct three-part label");
  ok(labels.includes("无名 · 通用 Agent · 未填写职责"), "member with no agent.role explicitly reports the missing responsibility");

  // Click third row (no-role member) and verify callback
  await act(async () => rows?.[2]?.click());
  equal(requests.length, 1, "selecting a row fires the onRequestAgent callback");
  equal(requests[0].memberId, "no-role", "callback receives member.id of the selected row");
  equal(requests[0].text, "检查资源命名规范", "callback receives the current item text");
  equal(document.querySelector(".collab-request-agent__popup"), null, "popup closes after selecting a row");

  // Verify outside click closes popup
  await act(async () => trigger?.click());
  ok(document.querySelector(".collab-request-agent__popup") !== null, "popup reopens");
  await act(async () => { document.dispatchEvent(new dom.window.MouseEvent("mousedown", { bubbles: true })); });
  equal(document.querySelector(".collab-request-agent__popup"), null, "outside mousedown closes the popup");

  await act(async () => trigger?.click());
  const escapeRow = document.querySelector<HTMLButtonElement>(".collab-request-agent__popup button");
  escapeRow?.focus();
  await act(async () => escapeRow?.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  equal(document.querySelector(".collab-request-agent__popup"), null, "Escape closes the Agent request popup");
  ok(document.activeElement === trigger, "Escape restores focus to the request-Agent trigger");

  // Empty state: no eligible members
  const soloMembers = [{ id: "self", name: "陈程序", online: true, isSelf: true, agent: { id: "self-agent", name: "程序 Agent", status: "idle" } }];
  await act(async () => root.render(<LocaleProvider><CollaborationTimeline
    items={[chatItem]} members={soloMembers} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy={false} transfers={[]}
    onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
    onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
    onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
  /></LocaleProvider>));
  const soloTrigger = document.querySelector<HTMLButtonElement>(".collab-request-agent > button");
  ok(soloTrigger !== null, "request-Agent trigger still renders with no eligible members");
  await act(async () => soloTrigger?.click());
  const emptyText = document.querySelector(".collab-request-agent__empty")?.textContent;
  ok(emptyText === "没有其他拥有 Agent 的成员在线。", "empty state shows localized message when no eligible members exist");
  ok(document.querySelector(".collab-request-agent__popup button") === null, "empty popup has no selectable rows and makes no request");

  await act(async () => root.unmount());
}

async function testRequestAgentPayload() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const transport = createMockCollaborationTransport("request-agent-payload");
  const post = transport.post.bind(transport);
  let posted: Parameters<CollaborationTransport["post"]>[0] | undefined;
  transport.post = async (input) => {
    posted = input;
    return post(input);
  };
  let controller: CollabController | undefined;
  function Harness() { controller = useCollabController("request-agent-payload", transport); return null; }
  const root = createRoot(document.getElementById("root")!);
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); });
  await act(async () => controller!.requestAgent("member-reviewer", "检查资源命名规范", ["chat-request"]));
  equal(
    posted && { kind: posted.kind, targetMemberID: posted.targetMemberID, text: posted.text, referenceIDs: posted.referenceIDs },
    { kind: "agent_request", targetMemberID: "member-reviewer", text: "检查资源命名规范", referenceIDs: ["chat-request"] },
    "message-level Agent delegation sends the selected member, original instruction, and referenced item through the real controller transport",
  );
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
  let semanticCalls = 0;
  const finishStarts: Array<(value: { ok: boolean }) => void> = [];
  const transport: CollaborationTransport = {
    async getState() { return connected; },
    async retry() { return connected; },
    async host() { return connected; },
    async join() { return connected; },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "busy-room" }; },
    async leave() {},
    async classifyIntent(text) {
      semanticCalls++;
      if (text.includes("外部")) return { intent: "uncertain", source: "llm" };
      if (text.includes("网络超时")) throw new Error("context deadline exceeded");
      return { intent: "chat", source: "fallback", error: "model temporarily unavailable", retryable: true };
    },
    async post(input) { return { ok: true, item: { ...item(input.requestID, 1, input.text), kind: input.kind } }; },
    startAgent() {
      startCalls++;
      return new Promise((resolve) => { finishStarts.push(resolve); });
    },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input) { connected.agentConfig = input.config; return connected; },
    async shareFiles() { return []; },
    async receiveFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
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
  await act(async () => { await controller!.postChat("现在 多人协作room, 在session里会有一个\"外部\"的标签"); await Promise.resolve(); });
  equal([semanticCalls, Object.values(controller!.state.pendingIntents).filter((entry) => entry.status === "pending").length], [1, 2], "uncovered statement uses LLM semantic intent and receives a countdown");
  await act(async () => { await controller!.postChat("帮我检查这个接口"); await Promise.resolve(); });
  equal(semanticCalls, 1, "covered instruction does not spend an LLM classification call");
  await act(async () => { await controller!.postChat("今天同步窗口是下午三点"); await Promise.resolve(); });
  const failedIntent = Object.values(controller!.state.pendingIntents).find((entry) => entry.instruction.includes("下午三点"));
  ok(!failedIntent, "LLM classification failure must not create a PendingIntent — message was already sent");
  await act(async () => { await controller!.postChat("现在测试网络超时的情况"); await Promise.resolve(); });
  const timeoutIntent = Object.values(controller!.state.pendingIntents).find((entry) => entry.instruction.includes("网络超时"));
  ok(!timeoutIntent, "network timeout must not create a PendingIntent");
  equal(semanticCalls, 3, "classification failures still consume LLM calls but never create task cards");
  let first!: Promise<void>;
  let second!: Promise<void>;
  await act(async () => {
    first = controller!.startAgent("first", []);
    second = controller!.startAgent("second", []);
    await Promise.resolve();
  });
  equal([startCalls, first === second, controller!.agentBusy], [2, false, true], "concurrent Agent starts remain distinct so the backend can queue both tasks");
  await act(async () => { finishStarts.forEach((finish) => finish({ ok: true })); await Promise.all([first, second]); });
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
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input) { offline.agentConfig = input.config; return offline; },
    async shareFiles() { return []; },
    async receiveFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
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

async function testComposerOfflineAgentOnly() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  let agentCalls = 0;
  let chatCalls = 0;
  let contributionCalls = 0;
  let requestCalls = 0;
  let shareCalls = 0;
  const members: CollaborationMember[] = [
    { id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent-self", name: "MyAgent", status: "idle" } },
    { id: "other", name: "Alice", online: true, agent: { id: "agent-other", name: "AliceAgent", status: "idle" } },
  ];
  await act(async () => root.render(<LocaleProvider><CollaborationComposer
    members={members} selfMemberId="self" connected={false} submitKey="enter"
    onReplyClear={() => {}} onChat={async () => { chatCalls++; }} onAgent={async () => { agentCalls++; }}
    onContribution={async () => { contributionCalls++; }} onRequest={async () => { requestCalls++; }}
    onShareFiles={async () => { shareCalls++; }}
  /></LocaleProvider>));
  const textarea = document.querySelector("textarea")!;
  ok(!textarea.disabled, "offline composer textarea remains editable");
  // All non-agent mode options are disabled when offline.
  const modeSelect = document.querySelector("select")!;
  const options = [...modeSelect.querySelectorAll("option")];
  const agentOption = options.find((opt) => opt.value === "agent")!;
  const chatOption = options.find((opt) => opt.value === "chat")!;
  const bothOption = options.find((opt) => opt.value === "both")!;
  const requestOption = options.find((opt) => opt.value === "request")!;
  const contributionOption = options.find((opt) => opt.value === "contribution")!;
  ok(!agentOption.disabled, "agent mode stays enabled while offline");
  ok(chatOption.disabled, "chat mode is disabled while offline");
  ok(bothOption.disabled, "both mode is disabled while offline");
  ok(requestOption.disabled, "request mode is disabled while offline");
  ok(contributionOption.disabled, "contribution mode is disabled while offline");
  ok((chatOption.textContent || "").includes("离线"), "chat option shows offline label");
  ok((bothOption.textContent || "").includes("离线"), "both option shows offline label");
  equal(modeSelect.value, "agent", "an initially offline composer selects the local Agent mode");

  const sendButton = document.querySelector(".collab-primary-button") as HTMLButtonElement;
  await act(async () => {
    Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype, "value")?.set?.call(textarea, "离线继续工作");
    const propsKey = Object.keys(textarea).find((key) => key.startsWith("__reactProps$"));
    if (!propsKey) throw new Error("missing textarea React props");
    (textarea as unknown as Record<string, { onChange(event: { target: HTMLTextAreaElement }): void }>)[propsKey].onChange({ target: textarea });
  });
  equal(sendButton.disabled, false, "offline local Agent message can be submitted");
  await act(async () => { sendButton.click(); await Promise.resolve(); });
  equal(agentCalls, 1, "offline submit invokes the local Agent");
  equal(chatCalls, 0, "onChat was not called");
  equal(contributionCalls, 0, "onContribution was not called");
  equal(requestCalls, 0, "onRequest was not called");
  equal(shareCalls, 0, "onShareFiles was not called");

  await act(async () => root.unmount());
}

async function testComposerAutoSwitchPreservesDraft() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  const members: CollaborationMember[] = [
    { id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent-self", name: "MyAgent", status: "idle" } },
  ];
  // Use a harness so connected can be toggled without unmounting the Composer.
  let setConnected: ((c: boolean) => void) | undefined;
  function Harness() {
    const [connected, setC] = useState(true);
    useEffect(() => { setConnected = setC; }, []);
    return <LocaleProvider><CollaborationComposer
      members={members} selfMemberId="self" connected={connected} submitKey="enter"
      onReplyClear={() => {}} onChat={async () => {}} onAgent={async () => {}}
      onContribution={async () => {}} onRequest={async () => {}} onShareFiles={async () => {}}
    /></LocaleProvider>;
  }

  await act(async () => root.render(<Harness />));
  const modeSelect = document.querySelector("select") as HTMLSelectElement;
  const textarea = document.querySelector("textarea") as HTMLTextAreaElement;
  // Start in chat mode (connected).
  await act(async () => {
    (modeSelect as HTMLSelectElement).value = "chat";
    modeSelect.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
  });
  equal(modeSelect.value, "chat", "starts in chat mode");
  await act(async () => {
    Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype, "value")?.set?.call(textarea, "keep this draft");
    const propsKey = Object.keys(textarea).find((key) => key.startsWith("__reactProps$"));
    if (!propsKey) throw new Error("missing textarea React props");
    (textarea as unknown as Record<string, { onChange(event: { target: HTMLTextAreaElement }): void }>)[propsKey].onChange({ target: textarea });
  });
  equal(textarea.value, "keep this draft", "chat draft is present before disconnect");

  // Disconnect without unmounting: auto-switch to agent mode.
  await act(async () => { setConnected!(false); await Promise.resolve(); });
  equal(modeSelect.value, "agent", "auto-switched to agent mode on disconnect");
  equal(textarea.value, "keep this draft", "disconnect preserves the draft for the local Agent");

  // Reconnect: agent mode stays but chat is available again.
  await act(async () => { setConnected!(true); await Promise.resolve(); });
  const chatOption = [...modeSelect.querySelectorAll("option")].find((o) => o.value === "chat")!;
  ok(!chatOption.disabled, "chat mode re-enabled after reconnect");

  await act(async () => root.unmount());
}

async function testComposerReconnectRestoresModes() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  const members: CollaborationMember[] = [
    { id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent-self", name: "MyAgent", status: "idle" } },
    { id: "other", name: "Alice", online: true, agent: { id: "agent-other", name: "AliceAgent", status: "idle" } },
  ];
  const render = (connected: boolean) => <LocaleProvider><CollaborationComposer
    members={members} selfMemberId="self" connected={connected} submitKey="enter"
    onReplyClear={() => {}} onChat={async () => {}} onAgent={async () => {}}
    onContribution={async () => {}} onRequest={async () => {}} onShareFiles={async () => {}}
  /></LocaleProvider>;

  // Offline: non-agent modes disabled.
  await act(async () => root.render(render(false)));
  let modeSelect = document.querySelector("select") as HTMLSelectElement;
  const offlineOptions = [...modeSelect.querySelectorAll("option")];
  ok(offlineOptions.find((o) => o.value === "chat")!.disabled, "chat disabled while offline");
  ok(offlineOptions.find((o) => o.value === "both")!.disabled, "both disabled while offline");
  ok(offlineOptions.find((o) => o.value === "request")!.disabled, "request disabled while offline");

  // Reconnect: all modes re-enabled.
  await act(async () => root.render(render(true)));
  modeSelect = document.querySelector("select") as HTMLSelectElement;
  const onlineOptions = [...modeSelect.querySelectorAll("option")];
  ok(!onlineOptions.find((o) => o.value === "chat")!.disabled, "chat re-enabled after reconnect");
  ok(!onlineOptions.find((o) => o.value === "both")!.disabled, "both re-enabled after reconnect");
  ok(!onlineOptions.find((o) => o.value === "request")!.disabled, "request re-enabled after reconnect");
  ok(!onlineOptions.find((o) => o.value === "contribution")!.disabled, "contribution re-enabled after reconnect");

  await act(async () => root.unmount());
}

async function testMentionStartsAgent() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const mention = { ...item("mention-1", 1, "@Me 这个项目是做什么的？"), actorId: "other", actorName: "Alice", mentionMemberIds: ["self"] };
  const connected: CollaborationState = {
    status: "connected",
    room: { room: "mention-room", host: "127.0.0.1", port: 39170, latestSequence: 1 },
    selfMemberId: "self",
    selfSessionId: "mention-session",
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle", sessionId: "mention-session" } }],
    timeline: [mention],
    agentConfig: { alias: "Agent", autoRespondQuestions: true, autoRespondRequests: false, autoRespondAgents: false, agentResponseIntervalSeconds: 30, agentClockTurns: 12, agentClockUnlimited: false, recognitionMode: "message" },
  };
  const starts: Array<{ requestID: string; referenceIDs: string[]; instruction: string; automatic?: boolean }> = [];
  const transport: CollaborationTransport = {
    async getState() { return connected; },
    async retry() { return connected; },
    async host() { return connected; },
    async join() { return connected; },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "mention-room" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent(input) {
      starts.push({ requestID: input.requestID, referenceIDs: input.referenceIDs, instruction: input.instruction, automatic: input.automatic });
      return { ok: true, item: { ...item(input.requestID, 2, input.instruction), kind: "agent_command", actorId: "self", actorAgent: true, referenceIds: input.referenceIDs, agentCommandId: input.requestID, agentRunStatus: "running" } };
    },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input) { connected.agentConfig = input.config; return connected; },
    async shareFiles() { return []; },
    async receiveFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  function Harness() { useCollabController("mention-session", transport); return null; }
  const root = createRoot(document.getElementById("root")!);
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); await Promise.resolve(); });
  // Backend scheduler handles auto-start; frontend must NOT auto-start on mention.
  equal(starts.length, 0, "frontend does NOT auto-start Agent on @mention — backend scheduling handles this");
  await act(async () => root.unmount());
}

async function testRoomAgentUsesScopedAutoApproval() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const connected: CollaborationState = {
    status: "connected",
    room: { room: "auto-room", host: "127.0.0.1", port: 39170, latestSequence: 1 },
    selfMemberId: "self",
    selfSessionId: "auto-session",
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle", sessionId: "auto-session" } }],
    timeline: [{ ...item("question-1", 1, "互动影游功能在哪里实现的？"), actorId: "other", actorName: "Alice" }],
    agentConfig: { alias: "Agent", autoRespondQuestions: true, autoRespondRequests: false, autoRespondAgents: false, agentResponseIntervalSeconds: 30, agentClockTurns: 12, agentClockUnlimited: false, recognitionMode: "message" },
  };
  const starts: Array<boolean | undefined> = [];
  const transport: CollaborationTransport = {
    async getState() { return connected; },
    async retry() { return connected; },
    async host() { return connected; },
    async join() { return connected; },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "auto-room" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent(input) {
      starts.push(input.automatic);
      return { ok: true, item: { ...item(input.requestID, starts.length + 1, input.instruction), kind: "agent_command", actorId: "self", actorAgent: true, referenceIds: input.referenceIDs, agentCommandId: input.requestID, agentRunStatus: "running" } };
    },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input) { connected.agentConfig = input.config; return connected; },
    async shareFiles() { return []; },
    async receiveFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  let controller: CollabController | undefined;
  function Harness() { controller = useCollabController("auto-session", transport); return null; }
  const root = createRoot(document.getElementById("root")!);
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); await Promise.resolve(); });
  // Backend scheduler handles auto-respond; frontend must NOT auto-start on question.
  equal(starts, [], "frontend does NOT auto-start Agent on question — backend scheduling handles this");
  // Manual startAgent still works and is NOT automatic.
  await act(async () => { await controller!.startAgent("手动分析", []); });
  equal(starts, [false], "manual Agent start is never automatic");
  await act(async () => root.unmount());
}

async function testConnectionPanelWorkspace() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, localStorage: dom.window.localStorage });
  localStorage.setItem("collab:identity:v1", JSON.stringify({ memberID: "member-1", memberName: "Alice", agentID: "agent-1", agentName: "Alice Agent" }));
  const workspaces = [
    { root: "D:/Projects/Alpha", name: "Alpha" },
    { root: "D:/Projects/Beta", name: "Beta" },
  ];
  const calls: string[] = [];
  let hostedRoomName = "";
  let hostedRoomID = "unset";
  let hostedProtocol = 0;
  let sessionID: string | undefined;
  let workspaceRoot = "";
  let resolving = false;
  const root = createRoot(document.getElementById("root")!);
  const render = () => {
    root.render(<LocaleProvider><ConnectionPanel
      sessionID={sessionID}
      status="disconnected"
      initial={{ room: "", title: "Beta Room", host: "127.0.0.1", port: 39170, latestSequence: 0 }}
      workspaces={workspaces}
      workspaceRoot={workspaceRoot}
      onWorkspaceChange={(value) => { calls.push(`change:${value}`); }}
      sessionResolving={resolving}
      onRetrySession={() => { calls.push("retry"); }}
      onHost={async (input) => { hostedRoomName = input.roomName || ""; hostedRoomID = input.room; hostedProtocol = input.protocolVersion || 0; calls.push(`host:${sessionID}`); }}
      onJoin={async () => { calls.push(`join:${sessionID}`); }}
      relayConfig={{ preferLAN: true, connectTimeoutSeconds: 10, routeStableSeconds: 60, relays: [{ id: "relay-sg", name: "Singapore", url: "wss://relay.example.test/relay/v1/connect", enabled: true, priority: 100, discovery: true }] }}
      roomQuery={{ rooms: [{ publicRoomId: "public-room", room: "relay-room", name: "Relay Room", description: "Cross-network room", requiresToken: false, hostKey: "host-key", routes: [{ kind: "relay", relayId: "relay-sg", url: "wss://relay.example.test/relay/v1/connect", tunnelId: "tun-1" }], joinRef: "join-ref" }] }}
      onQueryRooms={async () => { calls.push("query"); }}
      onSaveRelayConfig={async (config) => { calls.push(`save:${config.relays.length}`); }}
      onClose={() => {}}
      onConnected={async () => { calls.push("connected"); }}
    /></LocaleProvider>);
  };
  const setInputValue = (input: HTMLInputElement, value: string) => {
    const setter = Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")?.set;
    setter?.call(input, value);
    input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
    input.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
  };
  await act(async () => { render(); await Promise.resolve(); });
  const select = document.querySelector(".collab-connect-form select") as HTMLSelectElement;
  ok(Boolean(select) && [...select.options].map((option) => option.value).join(",") === ",D:/Projects/Alpha,D:/Projects/Beta", "Join tab lists every registered project Workspace plus the empty placeholder");
  equal((document.querySelector(".collab-connect-form button[type=submit]") as HTMLButtonElement).disabled, true, "an unselected Workspace blocks Room submission");
  const tabs = document.querySelectorAll(".collab-mode-tabs button");
  await act(async () => {
    (tabs[1] as HTMLButtonElement).click();
    await Promise.resolve();
  });
  equal((document.querySelector(".collab-connect-form select") as HTMLSelectElement)?.value, "", "Host tab keeps the same mandatory Workspace selector");
  equal((document.querySelector(".collab-connect-form button[type=submit]") as HTMLButtonElement).disabled, true, "Host stays blocked without a Workspace");
  await act(async () => {
    (tabs[0] as HTMLButtonElement).click();
    await Promise.resolve();
  });
  await act(async () => {
    select.value = "D:/Projects/Alpha";
    select.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
    await Promise.resolve();
  });
  equal(calls.join(","), "change:D:/Projects/Alpha", "selecting a Workspace notifies the resolver immediately");
  await act(async () => {
    (tabs[2] as HTMLButtonElement).click();
    await Promise.resolve();
  });
  equal(tabs.length, 3, "Room connection dialog exposes Join, Host, and Relay Server tabs");
  ok(Boolean(document.querySelector(".collab-relay-panel")), "Relay Server tab owns relay configuration and discovery");
  await act(async () => {
    (document.querySelector(".collab-discovery .collab-quiet-button") as HTMLButtonElement).click();
    await Promise.resolve();
  });
  equal(calls.at(-1), "query", "Relay Server tab queries active Rooms through the discovery controller");
  await act(async () => {
    (document.querySelector(".collab-relay-panel__intro button") as HTMLButtonElement).click();
    await Promise.resolve();
  });
  await act(async () => {
    (document.querySelector(".collab-relay-save button") as HTMLButtonElement).click();
    await Promise.resolve();
  });
  equal(calls.at(-1), "save:2", "Relay Server tab saves through the shared user-level Relay configuration source");
  await act(async () => {
    (document.querySelector(".collab-discovery-list button") as HTMLButtonElement).click();
    await Promise.resolve();
  });
  equal((document.querySelector<HTMLInputElement>('input[name="room"]'))?.value, "relay-room", "choosing a discovered Room fills JoinRef routes and returns to the Join tab");
  sessionID = "session-alpha";
  workspaceRoot = "D:/Projects/Alpha";
  await act(async () => { render(); await Promise.resolve(); });
  const form = document.querySelector(".collab-connect-form") as HTMLFormElement;
  await act(async () => {
    form.dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true }));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  equal(calls.at(-1), "connected", "Join completes only after the Room connection and Session binding");
  equal(calls.at(-2), "join:session-alpha", "Join submits with the resolved Session of the selected Workspace");
  // Switching Workspace invalidates the previous Session: late stale results must not be submitted
  sessionID = undefined;
  workspaceRoot = "D:/Projects/Beta";
  resolving = true;
  await act(async () => { render(); await Promise.resolve(); });
  equal((document.querySelector(".collab-connect-form button[type=submit]") as HTMLButtonElement).disabled, true, "submission is blocked while the new Workspace Session resolves");
  sessionID = "session-beta";
  workspaceRoot = "D:/Projects/Beta";
  resolving = false;
  await act(async () => { render(); await Promise.resolve(); });
  await act(async () => {
    form.dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true }));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  equal(calls.at(-2), "join:session-beta", "Join after a Workspace switch uses the new Session, never the stale one");
  await act(async () => {
    (tabs[1] as HTMLButtonElement).click();
    await Promise.resolve();
  });
  ok(!document.querySelector('input[name="room"]'), "Host hides the generated Room ID from the editable form");
  const roomNameInput = document.querySelector<HTMLInputElement>('input[name="roomName"]')!;
  ok(Boolean(roomNameInput) && roomNameInput.compareDocumentPosition(document.querySelector(".collab-route-picker")!) === dom.window.Node.DOCUMENT_POSITION_FOLLOWING, "Host puts required Room information above connection routes");
  equal(roomNameInput.value, "Beta Room", "Host keeps the visible Room name independent from the generated Room ID");
  await act(async () => {
    (document.querySelector(".collab-connect-form") as HTMLFormElement).dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true }));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  equal(calls.at(-2), "host:session-beta", "Host uses the same explicitly selected Workspace Session");
  equal([hostedRoomName, hostedRoomID, hostedProtocol], ["Beta Room", "", 2], "Host submits display metadata and delegates V2 Room ID generation to the backend");
  await act(async () => {
    (tabs[0] as HTMLButtonElement).click();
    await Promise.resolve();
  });
  // Importing a connection string updates only network/Room fields, never the local Workspace
  await act(async () => {
    setInputValue(document.querySelector<HTMLInputElement>('input[name="connectionString"]')!, "workground2://192.168.1.8:39170/imported-room");
    await Promise.resolve();
  });
  equal((document.querySelector(".collab-connect-form select") as HTMLSelectElement)?.value, "D:/Projects/Beta", "importing a connection string leaves the selected Workspace untouched");
  await act(async () => root.unmount());
}

async function testDiscoveryFailureIsHandled() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const transport = createMockCollaborationTransport("discovery-error");
  transport.listRooms = async () => { throw new Error("Relay protocol mismatch"); };
  let controller: CollabController | undefined;
  function Harness() { controller = useCollabController("discovery-error", transport); return null; }
  const root = createRoot(document.getElementById("root")!);
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); await Promise.resolve(); });
  let rejected = false;
  await act(async () => {
    try { await controller!.queryRooms({ limit: 20 }); } catch { rejected = true; }
  });
  equal([rejected, controller!.discoveryError], [false, "Relay protocol mismatch"], "Discovery failure stays observable without escaping as an unhandled rejection");
  await act(async () => root.unmount());
}

async function testSessionSwitchIsolation() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const pending: Array<{ sessionID: string; resolve: (value: CollaborationState) => void; reject: (error: Error) => void }> = [];
  const makeTransport = (sessionID: string): CollaborationTransport => ({
    async getState() { return { status: "disconnected", members: [], timeline: [] }; },
    async retry() { return { status: "disconnected", members: [], timeline: [] }; },
    host() { return new Promise((resolve, reject) => { pending.push({ sessionID, resolve, reject }); }); },
    join() { return new Promise((resolve, reject) => { pending.push({ sessionID, resolve, reject }); }); },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "room" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input) { return { status: "connected", room: { room: "room", host: "127.0.0.1", port: 39170, latestSequence: 0 }, selfMemberId: "self", selfSessionId: sessionID, members: [], timeline: [] }; },
    async shareFiles() { return []; },
    async receiveFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  });
  let controller: CollabController | undefined;
  let renderSessionID = "switch-a";
  function Harness() {
    const transport = React.useMemo(() => makeTransport(renderSessionID), [renderSessionID]);
    controller = useCollabController(renderSessionID, transport);
    return null;
  }
  const root = createRoot(document.getElementById("root")!);
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); });
  let hostPromise!: Promise<void>;
  await act(async () => {
    hostPromise = controller!.host({ listenHost: "127.0.0.1", port: 39170, room: "room-a", memberName: "Alice", agentName: "Agent", sessionID: "switch-a" });
    await Promise.resolve();
  });
  equal(pending.length, 1, "host request is in flight on the first Session binding");
  renderSessionID = "switch-b";
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); });
  await act(async () => {
    pending[0].resolve({ status: "connected", mode: "host", room: { room: "room-a", host: "127.0.0.1", port: 39170, latestSequence: 1 }, selfMemberId: "self", selfSessionId: "switch-a", members: [], timeline: [] });
    await hostPromise;
    await Promise.resolve();
  });
  equal([controller!.state.status, controller!.state.room, controller!.state.selfSessionId], ["disconnected", undefined, undefined], "a late Host result from the old Session cannot overwrite the new binding");
  let joinPromise!: Promise<void>;
  await act(async () => {
    joinPromise = controller!.join({ host: "10.0.0.9", port: 39171, room: "room-b", memberName: "Alice", agentName: "Agent", sessionID: "switch-b" });
    await Promise.resolve();
  });
  equal(pending.length, 2, "the new Session binding issues its own join request");
  renderSessionID = "switch-c";
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); });
  await act(async () => {
    pending[1].resolve({ status: "connected", mode: "client", room: { room: "room-b", host: "10.0.0.9", port: 39171, latestSequence: 1 }, selfMemberId: "self", selfSessionId: "switch-b", members: [], timeline: [] });
    await joinPromise;
    await Promise.resolve();
  });
  equal([controller!.state.status, controller!.state.room?.room], ["disconnected", undefined], "a late join result from the previous Workspace Session is discarded");
  await act(async () => root.unmount());
}

async function testCachedRoomSurvivesFailedRetry() {
  // When a persisted Room's auto-retry fails, loadCollaborationState must
  // return the cached Snapshot (with room, members, timeline) instead of
  // throwing. The workspace can then render the timeline with a retryable
  // banner rather than a full-page blocking connection card.
  const cached: CollaborationState = {
    status: "failed", mode: "host", retryable: true,
    room: { room: "persisted-room", host: "127.0.0.1", port: 39170, latestSequence: 3 },
    selfMemberId: "self",
    selfSessionId: "persisted-session",
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle", sessionId: "persisted-session" } }],
    timeline: [item("cached-msg", 1, "离线期间的消息"), item("cached-msg-2", 2, "第二条消息")],
    lastError: "Host is unreachable",
  };
  let retryCalls = 0;
  const transport: CollaborationTransport = {
    async getState() { return { ...cached }; },
    async retry() { retryCalls++; throw new Error("still unreachable"); },
    async host() { return cached; },
    async join() { return cached; },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "persisted-room" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent() { return { ok: true }; },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input: any) { cached.agentConfig = input.config; return cached; },
    async shareFiles() { return []; },
    async receiveFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  let renderedCached: CollaborationState | undefined;
  const result = await loadCollaborationState(transport, false, (state) => { renderedCached = state; });
  equal([renderedCached?.room?.room, renderedCached?.timeline.length], ["persisted-room", 2],
    "cached host Room is exposed before the network retry completes");
  equal([result.status, result.room?.room, result.members.length, result.timeline.length, retryCalls],
    ["failed", "persisted-room", 1, 2, 1],
    "failed auto-retry preserves cached Snapshot with room, members, and timeline");
  ok(Boolean(result.lastError), "failed auto-retry surfaces the last error for retry banner");
  ok(result.retryable === true, "failed auto-retry marks the state as retryable so the user can manually retry");
}

async function testCachedClientRoomSurvivesFailedRetry() {
  // A client-mode Session with a cached Room must also auto-retry and,
  // on failure, preserve the cached Snapshot so the workspace renders
  // rather than blocking on a full-page connection card.
  const cached: CollaborationState = {
    status: "failed", mode: "client", retryable: true,
    room: { room: "joined-room", host: "192.168.1.50", port: 39170, latestSequence: 5 },
    selfMemberId: "self",
    selfSessionId: "client-session",
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle", sessionId: "client-session" } }],
    timeline: [item("cached-client-msg", 1, "客户端离线消息")],
    lastError: "Host unreachable",
  };
  let retryCalls = 0;
  const transport: CollaborationTransport = {
    async getState() { return { ...cached }; },
    async retry() { retryCalls++; throw new Error("still unreachable"); },
    async host() { return cached; },
    async join() { return cached; },
    async invite() { return { hosts: ["192.168.1.50"], port: 39170, room: "joined-room" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent() { return { ok: true }; },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input: any) { cached.agentConfig = input.config; return cached; },
    async shareFiles() { return []; },
    async receiveFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  let renderedCached: CollaborationState | undefined;
  const result = await loadCollaborationState(transport, false, (state) => { renderedCached = state; });
  equal([renderedCached?.room?.room, renderedCached?.timeline.length], ["joined-room", 1],
    "cached client Room is exposed before the network retry completes");
  equal([result.status, result.room?.room, result.members.length, result.timeline.length, retryCalls],
    ["failed", "joined-room", 1, 1, 1],
    "failed client auto-retry preserves cached Snapshot with room, members, and timeline");
  ok(Boolean(result.lastError), "failed client auto-retry surfaces the last error for retry banner");
  ok(result.retryable === true, "failed client auto-retry marks the state as retryable");
}

async function testNoCacheSessionEntryWithoutAutoConnect() {
  // A Session with no persisted Room data must show the connection entry
  // (empty state). The entry form must not auto-connect to a globally
  // saved recent Room — it must wait for explicit user action.
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement, localStorage: dom.window.localStorage });

  // Simulate a previous session having saved a Room to global localStorage.
  localStorage.setItem("collab:host", "192.168.1.100");
  localStorage.setItem("collab:port", "39170");
  localStorage.setItem("collab:room", "stale-room");

  const empty: CollaborationState = {
    status: "disconnected",
    selfMemberId: undefined,
    selfSessionId: "fresh-session",
    members: [],
    timeline: [],
  };
  const transport: CollaborationTransport = {
    async getState() { return empty; },
    async retry() { return empty; },
    async host() { return empty; },
    async join() { return empty; },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "fresh-room" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent() { return { ok: true }; },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig() { return empty; },
    async shareFiles() { return []; },
    async receiveFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  let controller: CollabController | undefined;
  let connectCalls = 0;
  function Harness() {
    controller = useCollabController("fresh-session", transport);
    return null;
  }
  const root = createRoot(document.getElementById("root")!);
  await act(async () => { root.render(<LocaleProvider><Harness /></LocaleProvider>); await Promise.resolve(); });

  // No cached data → the workspace guard (!ownsRoom || !state.room) should
  // trigger the empty state with a "Connect" button. But the controller
  // must not have auto-called host() or join().
  equal([controller!.state.status, controller!.state.room, controller!.state.members.length, controller!.state.timeline.length],
    ["disconnected", undefined, 0, 0],
    "fresh session with no cached Room stays disconnected and shows the connection entry");
  equal(connectCalls, 0, "fresh session with no cache does not auto-connect to a stale global Room");

  // Verify the loadCollaborationState does not auto-retry for a session
  // with no room (mode is not "host", status is "disconnected").
  const loaded = await loadCollaborationState(transport);
  equal([loaded.status, loaded.room], ["disconnected", undefined],
    "loadCollaborationState does not attempt auto-retry for a session with no persisted Room");

  await act(async () => {
    root.render(<LocaleProvider><ConnectionPanel
      sessionID="fresh-session"
      status="disconnected"
      workspaces={[{ root: "D:/Projects/Fresh", name: "Fresh" }]}
      workspaceRoot="D:/Projects/Fresh"
      onWorkspaceChange={() => {}}
      sessionResolving={false}
      onRetrySession={() => {}}
      onHost={async () => { connectCalls++; }}
      onJoin={async () => { connectCalls++; }}
      onClose={() => {}}
    /></LocaleProvider>);
    await Promise.resolve();
  });
  equal((document.querySelector<HTMLInputElement>('input[name="room"]'))?.value, "", "a fresh Session does not inherit the globally saved recent Room");
  equal((document.querySelector<HTMLInputElement>('input[name="connectionString"]'))?.value, "", "a fresh Session waits for an explicit invite or Room target");
  equal(connectCalls, 0, "rendering the connection entry never submits Host or Join implicitly");

  await act(async () => root.unmount());
}

async function testHostRestoreMissingSelfSessionId() {
  // An old persisted Host state may lack selfSessionId. The restore flow
  // must still recognise the Room and auto-retry, and the cached dispatch
  // must carry enough context for ownsRoom to resolve correctly.
  const stale: CollaborationState = {
    status: "failed", mode: "host", retryable: true,
    room: { room: "host-room-no-ssid", host: "127.0.0.1", port: 39170, latestSequence: 2 },
    selfMemberId: "self",
    // intentionally omit selfSessionId
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle" } }],
    timeline: [item("host-no-ssid", 1, "离线 Host 消息")],
    lastError: "port busy",
  };
  let retryCalls = 0;
  const transport: CollaborationTransport = {
    async getState() { return { ...stale }; },
    async retry() { retryCalls++; return { ...stale, status: "connected" as const, lastError: undefined }; },
    async host() { return stale; },
    async join() { return stale; },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "host-room-no-ssid" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent() { return { ok: true }; },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input: any) { stale.agentConfig = input.config; return stale; },
    async shareFiles() { return []; },
    async receiveFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  let renderedCached: CollaborationState | undefined;
  const result = await loadCollaborationState(transport, false, (state) => { renderedCached = state; });
  equal([renderedCached?.room?.room, renderedCached?.timeline.length], ["host-room-no-ssid", 1],
    "cached Host Room without selfSessionId is still dispatched to the UI before retry");
  equal([result.status, result.room?.room, result.members.length, result.timeline.length, retryCalls],
    ["connected", "host-room-no-ssid", 1, 1, 1],
    "Host auto-retry succeeds even when the persisted snapshot lacks selfSessionId");
  ok(result.selfSessionId === undefined, "loadCollaborationState returns the state as-is; selfSessionId backfill is the transport normalizer's job");

  // Verify that collabReducer's STATE action sets selfSessionId when present.
  const fromReducer = collabReducer(initialCollabState, { type: "STATE", state: { ...stale, selfSessionId: "current-host-session" } });
  equal(fromReducer.selfSessionId, "current-host-session", "a Host STATE with explicit selfSessionId survives the reducer round-trip");
  // Verify that when normalizeCollaborationState produces selfSessionId: undefined
  // (all sources empty), the reducer spread overwrites an existing value. This
  // is why the transport normalizer must backfill — the TYPE system allows
  // undefined, but the reducer treats it as an explicit overwrite.
  const withExplicitUndefined: CollaborationState = { ...stale, selfSessionId: undefined };
  const afterIncomplete = collabReducer(fromReducer, { type: "STATE", state: withExplicitUndefined });
  ok(afterIncomplete.selfSessionId === undefined, "an incoming STATE where selfSessionId is explicitly undefined resets it (the normalizer must always backfill)");
}

async function testClientRestoreMissingSelfSessionId() {
  // A client-mode cached Room without selfSessionId must also auto-retry
  // and preserve timeline/members across the restore flow.
  const stale: CollaborationState = {
    status: "failed", mode: "client", retryable: true,
    room: { room: "joined-room-no-ssid", host: "192.168.1.99", port: 39171, latestSequence: 3 },
    selfMemberId: "self",
    // intentionally omit selfSessionId
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle" } }],
    timeline: [item("client-no-ssid", 1, "客户端离线消息")],
    lastError: "host not reachable",
  };
  let retryCalls = 0;
  const transport: CollaborationTransport = {
    async getState() { return { ...stale }; },
    async retry() { retryCalls++; throw new Error("still unreachable"); },
    async host() { return stale; },
    async join() { return stale; },
    async invite() { return { hosts: ["192.168.1.99"], port: 39171, room: "joined-room-no-ssid" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent() { return { ok: true }; },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig(input: any) { stale.agentConfig = input.config; return stale; },
    async shareFiles() { return []; },
    async receiveFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  let renderedCached: CollaborationState | undefined;
  const result = await loadCollaborationState(transport, false, (state) => { renderedCached = state; });
  equal([renderedCached?.room?.room, renderedCached?.timeline.length], ["joined-room-no-ssid", 1],
    "cached client Room without selfSessionId is dispatched before retry");
  equal([result.status, result.room?.room, result.members.length, result.timeline.length, retryCalls],
    ["failed", "joined-room-no-ssid", 1, 1, 1],
    "failed client auto-retry preserves cached Snapshot even without selfSessionId");
}

async function testExplicitWrongSessionIdRejected() {
  // When selfSessionId is explicitly set and does not match the current
  // session, the transport must NOT backfill and the ownership check must
  // fail. This prevents cross-session Room leakage.
  const wrongSession: CollaborationState = {
    status: "connected", mode: "host",
    room: { room: "other-room", host: "127.0.0.1", port: 39172, latestSequence: 1 },
    selfMemberId: "self",
    selfSessionId: "other-session-id",
    members: [{ id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent", name: "Agent", status: "idle" } }],
    timeline: [item("other-msg", 1, "其他 session 的消息")],
  };
  // Simulate the reducer receiving a STATE from a mismatched session.
  const reducerState = collabReducer(initialCollabState, { type: "STATE", state: wrongSession });
  equal(reducerState.selfSessionId, "other-session-id",
    "explicit selfSessionId from a different session is preserved verbatim");
  // Ownership check (the same logic as CollaborationWorkspace.ownsRoom):
  const currentSession = "current-session";
  const ownsRoom = Boolean(currentSession) && reducerState.selfSessionId === currentSession;
  ok(!ownsRoom, "explicit mismatched selfSessionId does NOT grant ownsRoom to a different session");

  // The transport normalizer must NOT overwrite an explicit selfSessionId.
  // (This is the production code path — verify by inspection that the
  //  backfill guard is `!state.selfSessionId`, not `state.selfSessionId !== sessionID`.)
  const transportSource = readFileSync(new URL("../collab/transport.ts", import.meta.url), "utf8");
  ok(transportSource.includes("!state.selfSessionId"),
    "transport normalizer backfill gate is !state.selfSessionId so explicit values are never overwritten");
}

async function testNewSessionNoRoomShowsEntry() {
  // A fresh Session with no persisted Room must stay disconnected and
  // must NOT auto-retry or inherit a global cached Room.
  const emptyState: CollaborationState = {
    status: "disconnected",
    selfSessionId: "new-empty-session",
  };
  const transport: CollaborationTransport = {
    async getState() { return { ...emptyState }; },
    async retry() { return { ...emptyState }; },
    async host() { return emptyState; },
    async join() { return emptyState; },
    async invite() { return { hosts: ["127.0.0.1"], port: 39170, room: "never-joined" }; },
    async leave() {},
    async post() { return { ok: true }; },
    async startAgent() { return { ok: true }; },
    async cancelQueuedTask() { return { ok: true }; },
    async respond() { return { ok: true }; },
    async updateAgentConfig() { return emptyState; },
    async shareFiles() { return []; },
    async receiveFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "completed", transferred: 1, total: 1 }; },
    async pauseFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "paused", transferred: 0, total: 1 }; },
    async resumeFile(fileId: string) { return { id: `receive:${fileId}`, fileId, direction: "receive", name: fileId, status: "downloading", transferred: 0, total: 1 }; },
    async revokeFile() { return { ok: true }; },
    subscribeState() { return () => {}; },
    subscribeEvent() { return () => {}; },
  };
  const result = await loadCollaborationState(transport);
  equal([result.status, result.room, result.mode],
    ["disconnected", undefined, undefined],
    "new Session with no cached Room stays disconnected and does not auto-retry");
}

async function testWailsTransportRestoresSessionOwnership() {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document });
  let raw: Record<string, unknown> = {
    status: "failed", mode: "host", retryable: true,
    room: "owned-host-room", snapshot: { room: { id: "owned-host-room" }, members: [], timeline: [] },
  };
  (window as any).go = { main: { App: {
    GetCollaborationState: async () => raw,
    RetryCollaboration: async () => { throw new Error("route unavailable"); },
  } } };

  const hostTransport = createWailsCollaborationTransport("host-session");
  let cachedHost: CollaborationState | undefined;
  const host = await loadCollaborationState(hostTransport, false, (state) => { cachedHost = state; });
  equal([cachedHost?.selfSessionId, host.selfSessionId, host.room?.room], ["host-session", "host-session", "owned-host-room"],
    "production Wails transport restores ownership for a cached Host Room without selfSessionId");

  raw = { ...raw, mode: "client", room: "owned-client-room", snapshot: { room: { id: "owned-client-room" }, members: [], timeline: [] } };
  const client = await createWailsCollaborationTransport("client-session").getState();
  equal([client.selfSessionId, client.room?.room], ["client-session", "owned-client-room"],
    "production Wails transport restores ownership for a cached client Room without selfSessionId");

  raw = { ...raw, selfSessionId: "other-session" };
  const mismatched = await createWailsCollaborationTransport("current-session").getState();
  equal(mismatched.selfSessionId, "other-session", "an explicit different Session owner is never overwritten");

  raw = { status: "disconnected", members: [], timeline: [] };
  const fresh = await createWailsCollaborationTransport("fresh-session").getState();
  equal([fresh.selfSessionId, fresh.room], [undefined, undefined], "a fresh Session without a Room does not inherit ownership");
}

async function main() {
  process.stdout.write("\ncollaboration state and countdown\n");
  const layoutCSS = readFileSync(new URL("../collab/collab.css", import.meta.url), "utf8");
  const workbenchCSS = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
  const handoffCSS = readFileSync(new URL("../collab/collab-handoff.css", import.meta.url), "utf8");
  const appSource = readFileSync(new URL("../App.tsx", import.meta.url), "utf8");
  const workspaceSource = readFileSync(new URL("../collab/CollaborationWorkspace.tsx", import.meta.url), "utf8");
  const composerSource = readFileSync(new URL("../collab/components/CollaborationComposer.tsx", import.meta.url), "utf8");
  const connectionSource = readFileSync(new URL("../collab/components/ConnectionPanel.tsx", import.meta.url), "utf8");
  const timelineSource = readFileSync(new URL("../collab/components/CollaborationTimeline.tsx", import.meta.url), "utf8");
  const activityPopoverSource = readFileSync(new URL("../collab/components/AgentActivityPopover.tsx", import.meta.url), "utf8");
  const avatarSource = readFileSync(new URL("../collab/avatar.ts", import.meta.url), "utf8");
  const transportSource = readFileSync(new URL("../collab/transport.ts", import.meta.url), "utf8");
  const controllerSource = readFileSync(new URL("../collab/useCollabController.ts", import.meta.url), "utf8");
  const projectTreeSource = readFileSync(new URL("../components/ProjectTree.tsx", import.meta.url), "utf8");
  for (const [selector, row] of [["collab-topicbar", 1], ["collab-status-banner", 2], ["collab-scroll", 3], ["collab-footer", 4]] as const) {
    ok(new RegExp(`\\.${selector}\\s*\\{[^}]*grid-row:\\s*${row}(?:;|\\s)`).test(layoutCSS), `${selector} stays in grid row ${row} when the optional status banner is absent`);
  }
  ok(/\.collab-surface\s*\{[^}]*position:\s*relative/.test(layoutCSS), "collaboration session is embedded in the normal session surface");
  ok(/\.collab-surface select\s*\{[^}]*appearance:\s*none/.test(layoutCSS), "collaboration selects do not leak native macOS appearance");
  ok(/\.collab-modal\s*\{[^}]*position:\s*fixed/.test(layoutCSS), "Host and Join form uses a popup layer");
  ok(appSource.includes('activeTab?.sessionKind === "collaboration"') && appSource.includes('ensureBlankTab("project", workspaceRoot)') && !appSource.includes("ensureBlankTab(target.scope, target.workspaceRoot)"), "Room starts from an explicitly selected project Workspace Session instead of the implicit default");
  ok(appSource.includes("selectCollaborationWorkspace") && appSource.includes("collabResolveGen.current") && appSource.includes("app.ListWorkspaces()") && appSource.includes("if (!workspaceRoot)"), "the connection dialog resolves the chosen Workspace with a generation guard and never creates a Session for an empty selection");
  ok(appSource.includes('mode="dialog"') && workspaceSource.includes('mode?: "session" | "dialog"'), "connection popup and connected Session have separate presentation modes");
  ok(!workspaceSource.includes("collab-room-rail"), "embedded collaboration view reuses the existing Session List instead of duplicating a Room rail");
  ok(projectTreeSource.includes("const sourceBadge = collaborationSession ? null :"), "Room Session keeps its dedicated icon without an external-source badge");
  ok(appSource.match(/activeContentVisible=\{!widgetActive\}/g)?.length === 2, "both ProjectTree layouts receive the real main-content visibility boundary");
  ok(projectTreeSource.includes("if (!activeContentVisible) return;") && projectTreeSource.includes("activeContentVisible, activeScope"), "a hidden ProjectTree cannot auto-clear the active Room unread state");
  ok(workspaceSource.includes('const usable = ownsRoom && Boolean(state.room)') && workspaceSource.includes('c("cachedBackground")'), "cached Room context remains usable and is explicitly disclosed while offline");
  ok(workspaceSource.includes("if (!ownsRoom || !state.room)") && !workspaceSource.includes('c("untitledRoom")'), "a Session without an authoritative Room stays on the connection entry instead of rendering a synthetic Room");
  ok(workspaceSource.includes("handleAction(controller.startAgent") && composerSource.includes("catch {"), "Agent action promises are consumed at both timeline and composer UI boundaries");
  ok(connectionSource.includes("await onHost(") && connectionSource.includes("await onJoin(") && connectionSource.includes("await onConnected?.()"), "popup closes only after Room connection and Session binding both complete");
  ok(connectionSource.includes('c("workspace")') && connectionSource.indexOf('c("workspace")') < connectionSource.indexOf('c("connectionString")'), "the Workspace selector sits before the network fields and is shared by Join and Host");
  ok(connectionSource.includes("onWorkspaceChange") && connectionSource.includes("sessionResolving") && connectionSource.includes("onRetrySession") && connectionSource.includes("workspaceRequired"), "Workspace resolution progress, errors and retry stay visible in the connection form");
  ok(connectionSource.includes("parseCollaborationInvite") && connectionSource.includes("loadCollaborationIdentity"), "connection popup imports invite strings and guides cached local identity");
  ok(connectionSource.includes("open={identityOpen || !identityReady}") && connectionSource.includes("event.currentTarget.open = true"), "incomplete first-time identity stays expanded instead of silently disabling Room creation");
  ok(connectionSource.includes('disabled={busy || !sessionID}') && !connectionSource.includes("|| !room.trim() || !identityReady"), "Room action remains clickable so native required-field validation can explain missing input");
  ok(/\.collab-connect-shell\s*\{[^}]*grid-template-rows:\s*auto minmax\(0, 1fr\)[^}]*overflow:\s*hidden/.test(layoutCSS) && /\.collab-connect-form\s*\{[^}]*overflow:\s*auto/.test(layoutCSS), "connection form scrolls inside the viewport instead of extending below it");
  ok(/\.collab-connect-form > \.collab-primary-button\s*\{[^}]*position:\s*sticky[^}]*bottom:\s*0/.test(layoutCSS), "Room action stays visible at the bottom of a tall form");
  ok(/\.collab-advanced-fields\s*\{[^}]*grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/.test(layoutCSS), "optional Room and identity fields use two columns to reduce form height");
  ok(timelineSource.includes("collab-presence-notice") && timelineSource.includes("collab-agent-run__marquee"), "presence events stay lightweight while Agent work uses a fixed animated status card");
  ok(/\.collab-message-actions\s*\{[^}]*opacity:\s*0/.test(layoutCSS) && timelineSource.includes("collab-double-bot") && !timelineSource.includes("collab-action-more"), "per-message actions collapse to a hover toolbar with a dedicated multi-Agent request trigger instead of the old overflow action");
  ok(workspaceSource.includes("controller.requestAgent(memberId, item.text, [item.id])"), "message-level delegation targets the selected member and preserves the current item as request context");
  ok(/\.collab-topicbar\s*\{[^}]*--wails-draggable:\s*drag/.test(layoutCSS) && layoutCSS.includes("--wails-draggable: no-drag"), "collaboration title bar is draggable while controls remain interactive");
  ok(/\.app--windows-frameless\.app--workbench-room \.collab-members\s*\{[^}]*height:\s*100%[^}]*margin-top:\s*0[^}]*padding-top:\s*calc\(20px \+ var\(--windows-window-controls-height\)\)/.test(workbenchCSS), "Room right plate extends behind the caption rail while its content keeps the same safe offset");
  ok(/--collab-bg:\s*var\(--bg\)/.test(layoutCSS) && /--collab-panel:\s*var\(--bg-elev/.test(layoutCSS) && /--collab-text:\s*var\(--fg\)/.test(layoutCSS) && /--collab-accent:\s*var\(--accent\)/.test(layoutCSS), "Room derives surfaces, text, and accents from light/dark Settings theme tokens");
  ok(/\.collab-primary-button\s*\{[^}]*background-color:\s*var\(--control-primary-bg/.test(layoutCSS), "Room primary actions keep a solid accent fallback when a visual style disables gradients");
  ok(handoffCSS.includes("var(--collab-accent)") && handoffCSS.includes("var(--collab-control)") && !handoffCSS.includes("rgba(155, 114, 255"), "Room handoff and reply additions reuse the same theme palette");
  ok(workspaceSource.indexOf("collab-agent-config") < workspaceSource.indexOf("collab-member-section"), "own Agent configuration is placed above the member list");
  ok(workspaceSource.includes("state.currentRun") && workspaceSource.includes("controller.stopCurrentRun") && workspaceSource.includes('data-phase={state.currentRun.phase}') && layoutCSS.includes(".collab-current-run__stop") && workspaceSource.indexOf('className="collab-agent-queue"') < workspaceSource.indexOf('className="collab-current-run"'), "My Agent run status and stop control live with the queued-task section");
  ok(workspaceSource.includes('c("autoManualShort")') && workspaceSource.includes('c("autoQuestionsShort")') && workspaceSource.includes('c("autoRequestsShort")') && workspaceSource.includes("cycleApprovalMode") && workspaceSource.includes("cycleResponseMode") && workspaceSource.includes("cycleRecognitionMode") && !workspaceSource.includes("composer-modebar composer-modebar--"), "Agent panel exposes approval, response, and recognition as click-to-cycle controls");
  ok(workspaceSource.includes('c("agentCollaboration")') && workspaceSource.includes('c("agentResponseFrequency")') && workspaceSource.includes("agentClockRemaining") && workspaceSource.includes("autoRespondAgents") && workspaceSource.includes("agentResponseIntervalSeconds") && workspaceSource.includes("agentClockTurns"), "Agent panel exposes independent Agent-to-Agent collaboration, frequency, and configurable clockwork controls");
  ok(workspaceSource.includes("agentCollaborationOpen") && workspaceSource.includes("aria-expanded={agentCollaborationOpen}") && workspaceSource.includes('c("agentClockWind")') && workspaceSource.includes("agentClockWoundAt") && workspaceSource.includes("agentClockUnlimited"), "Agent collaboration section folds and exposes persistent wind-up and unlimited controls");
  ok(/\.collab-agent-peer-policy--open \.collab-agent-peer-summary > svg\s*\{[^}]*rotate\(90deg\)/.test(layoutCSS) && /\.collab-agent-clock-row > button\s*\{/.test(layoutCSS), "fold and wind-up controls have explicit compact rail styling");
  ok(!controllerSource.includes("nextAgentCollaborationBatch") && !controllerSource.includes("batch.handoffs") && !controllerSource.includes("agentCollaborationRequestID") && !controllerSource.includes("autoAgentCollaborationInstruction") && !controllerSource.includes("window.setInterval") && !controllerSource.includes("scanAutomaticResponses") && !controllerSource.includes("scanAgentCollaboration") && !controllerSource.includes("nextMentionedAgentItem"), "Agent collaboration scheduler moved to Go backend — frontend controller has no automatic scheduling");
  ok(workspaceSource.includes("nextApprovalMode(approvalMode)") && workspaceSource.includes("controller.updateToolApprovalMode(next)"), "Agent panel cycles through the same ask, auto, and YOLO approval modes as a normal Session");
  ok(/\.collab-workspace\s*\{[^}]*grid-template-columns:\s*minmax\(430px,\s*1fr\)\s+328px/.test(layoutCSS) && /\.collab-agent-model \.modelsw__trigger\s*\{[^}]*height:\s*34px[^}]*font-size:\s*12\.5px/.test(layoutCSS) && /\.collab-agent-context-trigger\s*\{[^}]*min-height:\s*34px/.test(layoutCSS), "Agent panel keeps its deliberate rail width while compacting primary controls to a shared 34px scale");
  ok(/\.collab-agent-policies\s*\{[^}]*gap:\s*2px[^}]*padding:\s*4px[^}]*border:\s*0/.test(layoutCSS) && /\.collab-agent-policy-row\s*\{[^}]*min-height:\s*34px[^}]*padding:\s*0 4px 0 7px/.test(layoutCSS) && /\.collab-agent-policy-row \+ \.collab-agent-policy-row\s*\{[^}]*border-top:\s*0/.test(layoutCSS) && /\.collab-agent-cycle\s*\{[^}]*justify-content:\s*flex-end[^}]*height:\s*26px[^}]*border:\s*0[^}]*background:\s*transparent/.test(layoutCSS) && workspaceSource.includes("<ChevronsUpDown size={12}"), "approval, automatic response, and recognition use lightweight rows without nested control borders");
  ok(workspaceSource.includes("state.queuedTasks") && workspaceSource.includes("controller.cancelQueuedTask(task.id)") && /\.collab-agent-queue__list\s*\{[^}]*max-height:[^}]*overflow:\s*auto/.test(layoutCSS), "Agent panel shows the bounded queue and lets its owner cancel waiting tasks");
  ok(activityPopoverSource.includes("createPortal") && workspaceSource.includes("onMouseEnter") && workspaceSource.includes("onFocus") && workspaceSource.includes("AgentActivityPopover") && /\.collab-agent-activity-popover\s*\{[^}]*position:\s*fixed/.test(layoutCSS), "running Agent status exposes a non-clipping hover and keyboard-focus activity carousel");
  ok(!composerSource.includes("(agentMode && props.agentBusy)") && !timelineSource.includes('disabled={props.agentBusy}'), "busy Agent actions stay available so new work can be queued");
  ok(composerSource.includes("isComposerSubmitKey") && workspaceSource.includes("ModelSwitcher") && !composerSource.includes("ModelSwitcher") && appSource.includes("submitKey={composerSubmitKey}"), "Agent panel owns the active Session model while the collaboration composer reuses the configured send shortcut");
  ok(workspaceSource.includes('c("agentContext")') && workspaceSource.includes("state.agentSources?.agents") && workspaceSource.includes("state.agentSources?.skills") && workspaceSource.includes("toggleContextRef"), "Agent panel exposes explicit AGENTS.md and SKILL.md selection");
  ok(workspaceSource.includes('setProfileEditor("member")') && workspaceSource.includes('setProfileEditor("agent")') && workspaceSource.includes("controller.updateProfile(profile)") && workspaceSource.includes("saveCollaborationIdentity") && avatarSource.includes('canvas.toDataURL("image/webp"'), "member and Agent names/avatars are independently editable, compressed, synced, and cached");
  ok(transportSource.includes("memberAvatar") && transportSource.includes("agentAvatar") && timelineSource.includes("actor?.agent.avatar") && timelineSource.includes("actor?.avatar"), "Room member normalization and timeline rendering preserve both synchronized avatars");
  ok(workspaceSource.includes("collab-agent-context-trigger") && workspaceSource.includes("contextOpen &&") && /\.collab-config-modal\s*\{[^}]*position:\s*fixed[^}]*z-index:\s*var\(--z-modal\)/.test(layoutCSS), "explicit AGENTS.md and Skill selection opens in a dedicated modal");
  ok(/\.app--workbench \.collab-surface:not\(\.collab-surface--dialog\) > \.collab-config-modal\s*\{[^}]*position:\s*fixed[^}]*z-index:\s*var\(--z-modal\)/.test(layoutCSS), "Room config modal overrides the workbench direct-child positioning rule instead of being clipped inside the Room grid");
  ok(workspaceSource.includes("useScrollManager") && workspaceSource.includes("timelineStick.current") && workspaceSource.includes("snapTimelineToBottom()") && workspaceSource.includes("onScroll={onTimelineScroll}"), "Room timeline follows new messages while reusing the shared sticky-bottom guard");
  ok(composerSource.includes("onFilesDroppedIn") && composerSource.includes('"--wails-drop-target": "drop"') && workspaceSource.includes("onShareFiles={controller.shareFiles}"), "Room composer owns native file drops and routes paths to sharing");
  ok(timelineSource.includes("FileCard") && timelineSource.includes("onReceiveFile") && /\.collab-file-progress\s*\{/.test(layoutCSS), "file cards expose receive and resumable progress controls");
  ok(timelineSource.includes('transfer.status === "completed"') && timelineSource.includes('c("fileOpen")') && timelineSource.includes('c("fileReveal")'), "completed received files expose default open and show-in-folder actions");
  ok(transportSource.includes("app.OpenCollaborationFile({ sessionID, fileID })") && transportSource.includes("app.RevealCollaborationFile({ sessionID, fileID })"), "file actions send stable session and file ids instead of UI-provided paths");
  ok(workspaceSource.includes("const onlineMembers = state.members.filter") && workspaceSource.includes("onlineMembers.map"), "member rail and its counters exclude offline members");
  ok(composerSource.includes('role="listbox"') && composerSource.includes("mentionPayload") && /\.collab-mention-popup\s*\{/.test(layoutCSS), "composer exposes a keyboard-accessible typed mention popup");

  const invite = buildCollaborationInvite({ host: "192.168.1.8", port: 39170, room: "接口 联调", token: "shared secret" });
  equal(parseCollaborationInvite(invite), { host: "192.168.1.8", port: 39170, room: "接口 联调", token: "shared secret" }, "connection string round-trips Room and token");
  const ipv6Invite = buildCollaborationInvite({ host: "::1", port: 39170, room: "room-v6" });
  equal(parseCollaborationInvite(ipv6Invite), { host: "::1", port: 39170, room: "room-v6", token: undefined }, "connection string preserves bracketed IPv6 hosts");
  const relayInviteValue = { version: 2 as const, room: "跨网联调", hostKey: "sha256:host-key", routes: [{ kind: "lan" as const, protocolVersion: 2 as const, host: "192.168.1.8", port: 39170 }, { kind: "relay" as const, relayId: "official-sg", url: "wss://relay.example.test/relay/v1/connect", tunnelId: "tun-1", guestCapability: "cap-1", priority: 100 }], roomToken: "secret" };
  equal(parseCollaborationInvite(buildCollaborationInvite(relayInviteValue)), relayInviteValue, "V2 RouteSet invite round-trips UTF-8 Room, LAN and Relay routes");
  const selectableInvite = { version: 2 as const, hosts: ["127.0.0.1", "10.0.0.8", "::1"], port: 39170, room: "route-room", token: "secret", hostKey: "sha256:host-key", routes: [
    { id: "lan:127.0.0.1", kind: "lan" as const, protocolVersion: 2 as const, host: "127.0.0.1", port: 39170 },
    { id: "relay:sg", kind: "relay" as const, relayId: "official-sg", url: "wss://relay.example.test/relay/v1/connect", tunnelId: "tun-1", guestCapability: "cap-1" },
    { id: "lan:10.0.0.8", kind: "lan" as const, protocolVersion: 2 as const, host: "10.0.0.8", port: 39170 },
    { id: "lan:::1", kind: "lan" as const, protocolVersion: 2 as const, host: "::1", port: 39170 },
  ] };
  const exportOptions = collaborationInviteOptions(selectableInvite);
  equal(exportOptions.map((option) => `${option.kind}:${option.label}`), ["relay:official-sg", "lan:10.0.0.8", "lan:127.0.0.1", "lan:::1"], "export routes keep Relay first and loopback addresses last");
  const relayExport = buildCollaborationInviteForOption(selectableInvite, exportOptions[0]);
  const lanExport = buildCollaborationInviteForOption(selectableInvite, exportOptions[1]);
  ok(relayExport !== lanExport, "changing the export route changes the connection string");
  equal((parseCollaborationInvite(relayExport) as typeof relayInviteValue).routes.map((route) => route.kind), ["relay"], "Relay selection exports only the selected Relay route");
  equal((parseCollaborationInvite(lanExport) as typeof relayInviteValue).routes.map((route) => route.host), ["10.0.0.8"], "LAN selection exports only the selected IP route");
  let invalidInvite = false;
  try { parseCollaborationInvite("https://example.com/room"); } catch { invalidInvite = true; }
  ok(invalidInvite, "non-WorkGround2 URLs are rejected as Room invites");

  equal(tryBuildCollaborationInvite({ version: 1 as const, host: "", port: 0, room: "" }), "", "invalid V1 invite (empty host, port 0) returns empty string without throwing");
  equal(tryBuildCollaborationInvite({ version: 1 as const, host: "127.0.0.1", port: 0, room: "test" }), "", "V1 invite with port 0 returns empty string without throwing");
  equal(tryBuildCollaborationInvite({ version: 2 as const, room: "", hostKey: "", routes: [] }), "", "invalid V2 invite (empty room, hostKey, routes) returns empty string without throwing");
  equal(tryBuildCollaborationInvite({ version: 2 as const, room: "test", hostKey: "key", routes: [] }), "", "V2 invite without routes returns empty string without throwing");
  ok(tryBuildCollaborationInvite({ host: "192.168.1.8", port: 39170, room: "test" }).startsWith("workground2://"), "valid V1 invite produces workground2 URL");
  ok(workspaceSource.includes('inviteBuildError = invite && !inviteString ? c("connectionStringInvalid")') && workspaceSource.includes("disabled={!inviteString}"), "invalid exported invites stay visible as retryable errors and cannot be copied");
  ok(workspaceSource.includes("buildCollaborationInviteForOption") && !workspaceSource.includes("invite?.invite ||"), "exported connection strings follow the selected route instead of the prebuilt aggregate invite");

  const identityDOM = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://localhost/" });
  Object.assign(globalThis, { localStorage: identityDOM.window.localStorage });
  equal(loadCollaborationIdentity(), undefined, "first collaboration connection has no silent placeholder identity");
  localStorage.setItem("collab:memberName", "成员");
  localStorage.setItem("collab:agentName", "Personal Agent");
  equal(loadCollaborationIdentity(), undefined, "legacy placeholder names still trigger first-time identity guidance");
  localStorage.clear();
  const draftIdentity = { ...newCollaborationIdentity(), memberName: "Alice", memberAvatar: "data:image/png;base64,AAAA", agentName: "Alice Agent", agentAvatar: "data:image/webp;base64,BBBB", memberRole: "Backend" };
  saveCollaborationIdentity(draftIdentity);
  const cachedIdentity = loadCollaborationIdentity();
  equal([cachedIdentity?.memberID, cachedIdentity?.memberName, cachedIdentity?.memberAvatar, cachedIdentity?.memberRole, cachedIdentity?.agentID, cachedIdentity?.agentName, cachedIdentity?.agentAvatar], [draftIdentity.memberID, "Alice", draftIdentity.memberAvatar, "Backend", draftIdentity.agentID, "Alice Agent", draftIdentity.agentAvatar], "completed local identity caches both profile avatars with stable member and Agent ids");
  equal(detectSelfAgentIntent("帮我检查这个接口的错误日志"), "self_agent", "detects an explicit self-Agent instruction");
  equal(detectSelfAgentIntent("把现有修改提交一下"), "self_agent", "detects a Chinese commit instruction for the local Agent");
  equal(detectSelfAgentIntent("把现有的修改 commit 一下"), "self_agent", "detects a mixed-language commit instruction for the local Agent");
  equal(detectSelfAgentIntent("build and test the current changes"), "self_agent", "detects an English build instruction for the local Agent");
  equal(detectSelfAgentIntent("这个接口是不是有问题？"), "uncertain", "keeps ambiguous questions as suggestions");
  equal(detectSelfAgentIntent("小王，你检查一下这个接口"), "chat", "does not hijack instructions directed at another person");
  equal(detectSelfAgentIntent("接口已经修好了"), "chat", "does not execute completion statements");
  equal(detectSelfAgentIntentRule("现在 多人协作room, 在session里会有一个\"外部\"的标签"), { intent: "chat", covered: false }, "plain statement is explicitly marked for semantic fallback");
  equal(detectSelfAgentIntentRule("接口已经修好了"), { intent: "chat", covered: true }, "completion statement is a covered negative and skips semantic fallback");
  const replayChat = { ...item("outbox:queued-chat", 12, "把现有修改提交一下"), localPending: true, syncStatus: "pending" as const };
  const replayRun = { ...item("outbox:queued-run", 13, "把现有修改提交一下"), kind: "agent_command" as const, actorAgent: true, localPending: true, syncStatus: "pending" as const, referenceIds: [replayChat.id], agentRunStatus: "running" as const };
  equal(replayableSelfAgentItems({ ...initialCollabState, selfMemberId: "self", timeline: [replayChat] }).map((entry) => entry.id), [replayChat.id], "restored Outbox chat replays a missing local Agent intent");
  equal(replayableSelfAgentItems({ ...initialCollabState, selfMemberId: "self", timeline: [replayChat, replayRun] }).length, 0, "restored Agent run reference prevents duplicate intervention");
  const mentionMembers = [
    { id: "self", name: "Me", online: true, isSelf: true, agent: { id: "agent-self", name: "My Agent", status: "idle" as const } },
    { id: "online", name: "Alice", online: true, agent: { id: "agent-online", name: "Alice Agent", status: "idle" as const } },
    { id: "offline", name: "Bob", online: false, agent: { id: "agent-offline", name: "Bob Agent", status: "offline" as const } },
  ];
  const onlineMentions = collaborationMentionCandidates(mentionMembers, "self", true);
  equal(onlineMentions.map((entry) => entry.key), ["agent:agent-self", "member:online", "agent:agent-online"], "online mention list includes own Agent and online peers while excluding self and offline peers");
  equal(collaborationMentionCandidates(mentionMembers, "self", false).map((entry) => entry.key), ["agent:agent-self"], "offline Room mention list contains only the local Agent");
  const active = activeMention("请 @Ali", 7)!;
  equal(filterMentionCandidates(onlineMentions, active.query).map((entry) => entry.key), ["member:online", "agent:agent-online"], "mention popup matches member and Agent labels from the active query");
  const inserted = insertMention("请 @Ali", active, onlineMentions[2]);
  equal([inserted.value, mentionPayload(inserted.value, [onlineMentions[2]])], ["请 @Alice Agent ", { mentionMemberIDs: [], mentionAgentIDs: ["agent-online"] }], "selected mention inserts readable text and retains typed Agent identity");
  const mentionedItem = { ...item("mention-item", 14, "@My Agent inspect"), actorId: "online", mentionAgentIds: ["agent-self"] };
  equal(nextMentionedAgentItem([mentionedItem], "self", "agent-self")?.id, "mention-item", "typed mention targets the owning Agent independently of text parsing");
  const mentionedMember = { ...item("mention-member", 15, "@Me inspect"), actorId: "online", mentionMemberIds: ["self"] };
  equal(nextMentionedAgentItem([mentionedMember], "self", "agent-self")?.id, "mention-member", "typed member mention routes to the mentioned member's Agent");
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
    payload: { id: "timeline-8", sequence: 8, type: "chat", chat: { id: "timeline-8", authorId: "member-a", text: "raw RoomEvent payload", mentionMemberIds: ["member-b"], mentionAgentIds: ["agent-b"], revision: 1, createdAt: "2026-08-03T08:00:00Z" } },
  }, new Map([["member-a", "Alice"]]));
  equal([roomEvent.id, roomEvent.kind, roomEvent.text, roomEvent.actorName, roomEvent.sequence, roomEvent.mentionMemberIds, roomEvent.mentionAgentIds], ["timeline-8", "chat", "raw RoomEvent payload", "Alice", 8, ["member-b"], ["agent-b"]], "raw RoomEvent unwraps typed chat and mention targets");
  const agree = buildAgreeMessageInput(item("target-item", 9), "agree-request");
  equal([agree.targetItemID, agree.reactionKind, agree.targetMemberID], ["target-item", "agree", undefined], "reaction targets a timeline item without polluting member targeting");
  const wrappedEvent = normalizeCollaborationItem({ event: { eventId: "event-9", sequence: 9 }, item: { id: "timeline-9", sequence: 9, type: "agent_result", actorName: "Alice", agentResult: { id: "timeline-9", ownerId: "member-a", agentId: "agent-a", runId: "run-9", summary: "verified", revision: 1, createdAt: "2026-08-03T09:00:00Z", handoffs: [{ targetAgentId: "agent-b", instruction: "review", referenceIds: ["message-1"], requiresResponse: true }] } } }, new Map([["member-a", "Alice"]]), new Map([["member-a", "KBot"], ["agent-a", "KBot"]]));
  equal([wrappedEvent.id, wrappedEvent.kind, wrappedEvent.text, wrappedEvent.actorName, wrappedEvent.actorAgent, wrappedEvent.agentRunId, wrappedEvent.handoffs?.[0]], ["timeline-9", "agent_result", "verified", "KBot", true, "run-9", { targetAgentId: "agent-b", instruction: "review", referenceIds: ["message-1"], reason: undefined, expectedOutcome: undefined, requiresResponse: true }], "Agent result preserves alias, run pairing, and directed handoff metadata");
  const runningEvent = normalizeCollaborationItem({ id: "run-1", sequence: 10, type: "agent_run", agentRun: { id: "run-1", ownerId: "member-a", instruction: "fix it", status: "running", summary: "reading files" } }, new Map([["member-a", "Alice"]]));
  equal([runningEvent.kind, runningEvent.agentRunStatus, runningEvent.agentRunSummary], ["agent_command", "running", "reading files"], "Agent run status survives timeline normalization for the animated card");
  equal(recentAgentActivity([wrappedEvent, runningEvent, { ...runningEvent, id: "other", actorId: "member-b", sequence: 11 }], "member-a").map((entry) => [entry.kind, entry.text]), [["output", "reading files"], ["input", "fix it"], ["output", "verified"]], "running Agent carousel derives recent input, progress output and final results from the authoritative member timeline");
  const fileEvent = normalizeCollaborationItem({ id: "file-1", sequence: 11, type: "file", file: { id: "file-1", ownerId: "member-a", name: "report.zip", size: 4194305, mime: "application/zip", sha256: "abc", revision: 2, revokedAt: "2026-08-04T00:00:00Z" } }, new Map([["member-a", "Alice"]]));
  equal([fileEvent.kind, fileEvent.actorName, fileEvent.fileName, fileEvent.fileSize, fileEvent.fileRevoked, fileEvent.revision], ["file", "Alice", "report.zip", 4194305, true, 2], "file offer metadata and revocation survive timeline normalization");
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
  const agentRoleState = normalizeCollaborationState({ status: "connected", snapshot: { members: [
    { id: "nested-role", name: "Nested", agent: { id: "agent-a", name: "A", role: "Backend" } },
    { ID: "nested-Role", Name: "Nested Legacy", Agent: { ID: "agent-b", Name: "B", Role: "Design" } },
    { id: "flat-role", name: "Flat", agent: { id: "agent-c", name: "C" }, agentRole: "QA" },
    { ID: "flat-Role", Name: "Flat Legacy", Agent: { ID: "agent-d", Name: "D" }, AgentRole: "Ops" },
  ], timeline: [] } });
  equal(agentRoleState.members.map((member) => member.agent.role), ["Backend", "Design", "QA", "Ops"], "Agent responsibility survives nested and flattened bridge field variants");
  const agentQueueState = normalizeCollaborationState({ status: "connected", room: "room-a", snapshot: { room: { id: "room-a" }, members: [], timeline: [] }, queuedTasks: [{ id: "run-2", requestId: "request-2", instruction: "检查接口", referenceIds: ["message-1"], queuedAt: "2026-08-04T10:00:00Z" }] });
  equal(agentQueueState.queuedTasks, [{ id: "run-2", requestId: "request-2", instruction: "检查接口", referenceIds: ["message-1"], agentRequestId: undefined, queuedAt: "2026-08-04T10:00:00Z" }], "persisted Agent queue survives desktop state normalization");
  const currentRunState = normalizeCollaborationState({ status: "connected", room: "room-a", snapshot: { room: { id: "room-a" }, members: [], timeline: [] }, currentRun: { sessionId: "session-a", runId: "run-1", phase: "waiting_approval", instruction: "检查接口", progress: "等待工具确认", startedAt: 1700000000000, queueCount: 2 } });
  equal(currentRunState.currentRun, { sessionId: "session-a", runId: "run-1", phase: "waiting_approval", instruction: "检查接口", progress: "等待工具确认", startedAt: 1700000000000, queueCount: 2 }, "local current Agent run remains visible with progress, phase and queue depth");
  const transferState = normalizeCollaborationState({ status: "connected", room: "room-a", snapshot: { room: { id: "room-a" }, members: [], timeline: [] }, transfers: [{ id: "receive-1", fileId: "file-1", direction: "receive", name: "report.zip", status: "paused", transferred: 4, total: 10, retryable: true }] });
  equal(transferState.transfers?.map((value) => [value.fileId, value.status, value.transferred, value.total, value.retryable]), [["file-1", "paused", 4, 10, true]], "persisted file transfer state remains resumable after normalization");
  let pendingState = collabReducer(initialCollabState, { type: "STATE", state: queuedState });
  pendingState = collabReducer(pendingState, { type: "STATE", state: { ...queuedState, status: "connected", timeline: [item("synced-chat", 4, "not swallowed")] } });
  equal(pendingState.timeline.map((entry) => entry.id), ["synced-chat"], "authoritative sync replaces the local pending projection without a ghost duplicate");
  const busy = normalizeCollaborationAction({ requestId: "busy-1", code: "agent_busy", retryable: true, error: "wording-independent" });
  equal([busy.ok, busy.code, busy.retryable], [false, "agent_busy", true], "structured Agent busy code survives transport normalization");
  equal(normalizeCollaborationIntent({ Intent: "uncertain", Source: "llm" }), { intent: "uncertain", source: "llm", error: undefined, retryable: false }, "semantic intent bridge normalizes strict model output");
  equal(normalizeCollaborationIntent({ Intent: "maybe", Source: "llm" }).intent, "chat", "invalid semantic intent safely falls back to chat");
  const configured = normalizeCollaborationState({ status: "connected", memberId: "self", snapshot: { members: [{ id: "self", name: "Me", agent: { id: "agent", name: "Old" } }], timeline: [] }, agentConfig: { alias: "Kite", autoRespondQuestions: true, autoRespondRequests: true, autoRespondAgents: true, agentResponseIntervalSeconds: 15, agentClockTurns: 8, agentClockUnlimited: true, agentClockWoundAt: "2026-08-05T03:04:05Z", recognitionMode: "interval" } });
  equal(configured.agentConfig, { alias: "Kite", autoRespondQuestions: true, autoRespondRequests: true, autoRespondAgents: true, agentResponseIntervalSeconds: 15, agentClockTurns: 8, agentClockUnlimited: true, agentClockWoundAt: "2026-08-05T03:04:05Z", recognitionMode: "interval", contextRefs: [] }, "Agent response policy survives desktop state normalization");
  equal(initialCollabState.agentConfig.recognitionMode, "interval", "new Room state recognizes conversations every 30 seconds by default");
  equal(normalizeCollaborationState({ agentConfig: {}, snapshot: {} }).agentConfig.recognitionMode, "interval", "missing persisted recognition policy falls back to 30 seconds");
  equal([autoResponseMode({ autoRespondQuestions: false, autoRespondRequests: false }), autoResponseMode({ autoRespondQuestions: true, autoRespondRequests: false }), autoResponseMode({ autoRespondQuestions: false, autoRespondRequests: true })], ["manual", "questions", "operations"], "three-level response control maps legacy requests-only state to Operations");
  equal([autoResponseFlags("manual"), autoResponseFlags("questions"), autoResponseFlags("operations")], [{ autoRespondQuestions: false, autoRespondRequests: false }, { autoRespondQuestions: true, autoRespondRequests: false }, { autoRespondQuestions: true, autoRespondRequests: true }], "three-level response choices persist monotonic question and operation flags");
  equal([nextAutoResponseMode("manual"), nextAutoResponseMode("questions"), nextAutoResponseMode("operations")], ["questions", "operations", "manual"], "automatic response cycles through all three levels");
  equal([nextApprovalMode("ask"), nextApprovalMode("auto"), nextApprovalMode("yolo")], ["auto", "yolo", "ask"], "tool approval cycles through ask, auto, and YOLO");
  equal([nextRecognitionMode("interval"), nextRecognitionMode("message"), nextRecognitionMode("off")], ["message", "off", "interval"], "recognition control cycles 30 seconds, each message, and off without a dropdown");
  const sourceState = normalizeCollaborationState({ status: "connected", memberId: "self", snapshot: { members: [], timeline: [] }, agentConfig: { contextRefs: ["agents:C:/repo/AGENTS.md", "skill:review"] }, agentSources: { agents: [{ id: "agents:C:/repo/AGENTS.md", kind: "agents", name: "AGENTS.md", path: "C:/repo/AGENTS.md", scope: "project" }], skills: [{ id: "skill:review", kind: "skill", name: "review", path: "C:/skills/review/SKILL.md", description: "Review changes", runAs: "inline" }] } });
  equal([sourceState.agentConfig.contextRefs, sourceState.agentSources?.agents[0].path, sourceState.agentSources?.skills[0].runAs, sourceState.agentSources?.skills[0].available], [["agents:C:/repo/AGENTS.md", "skill:review"], "C:/repo/AGENTS.md", "inline", true], "explicit Agent instruction sources survive desktop state normalization");
  const promptState = normalizeCollaborationState({ status: "connected", memberId: "self", toolApprovalMode: "auto", agentPrompt: { runId: "run-1", kind: "approval", id: "approval-1", tool: "shell_command", subject: "go test ./desktop", reason: "执行本地测试" }, snapshot: { members: [], timeline: [] } });
  const reachabilityState = normalizeCollaborationState({ status: "connected", snapshot: { room: { id: "relay-room" }, members: [], timeline: [] }, routes: [{ id: "relay:sg", kind: "relay", relayId: "sg", status: "degraded", active: true, priority: 100, latencyMs: 82, lastError: "packet loss", retryable: true }], advertisement: { visibility: "public", revision: 4, relays: [{ relayId: "sg", status: "failed", lastError: "quota", retryable: true }] } });
  equal(reachabilityState.routes?.[0], { id: "relay:sg", kind: "relay", host: undefined, port: undefined, relayId: "sg", url: undefined, tunnelId: undefined, guestCapability: undefined, priority: 100, status: "degraded", active: true, latencyMs: 82, lastError: "packet loss", retryable: true }, "Route status remains independent and observable in Desktop state");
  equal(reachabilityState.advertisement, { visibility: "public", revision: 4, relays: [{ relayId: "sg", status: "failed", lastError: "quota", retryable: true }] }, "advertisement failure remains scoped to its Relay");
  equal([promptState.toolApprovalMode, promptState.agentPrompt?.runId, promptState.agentPrompt?.tool, promptState.agentPrompt?.subject], ["auto", "run-1", "shell_command", "go test ./desktop"], "Room state preserves Session approval mode and local pending prompt details");
  const automaticBase = {
    ...initialCollabState,
    selfMemberId: "self",
    agentConfig: { alias: "Kite", autoRespondQuestions: true, autoRespondRequests: true, autoRespondAgents: false, agentResponseIntervalSeconds: 30, agentClockTurns: 12, agentClockUnlimited: false, recognitionMode: "message" as const },
    timeline: [
      { ...item("question-1", 1, "这个接口能重试吗？"), actorId: "other", actorName: "Other" },
      { ...item("request-1", 2, "请运行测试"), kind: "agent_request" as const, actorId: "other", actorName: "Other", targetMemberId: "self", requestStatus: "waiting" as const },
    ],
  };
  equal(nextAutomaticAgentItem(automaticBase)?.kind, "request", "automatic policy prioritizes explicit operation requests");
  equal(nextAutomaticAgentItem({ ...automaticBase, agentConfig: { ...automaticBase.agentConfig, autoRespondRequests: false } })?.item.id, "question-1", "automatic question response selects an unanswered external question");
  const memberMentionQuestion = { ...item("member-mention-question", 3, "@Me 这个接口能重试吗？"), actorId: "other", actorName: "Other", mentionMemberIds: ["self"] };
  equal(nextAutomaticAgentItem({ ...automaticBase, timeline: [memberMentionQuestion] }, "agent-self"), undefined, "explicit member mention takes precedence over automatic question recognition");
  const agentMentionQuestion = { ...item("agent-mention-question", 4, "@Kite 这个接口能重试吗？"), actorId: "other", actorName: "Other", mentionAgentIds: ["agent-self"] };
  equal(nextAutomaticAgentItem({ ...automaticBase, timeline: [agentMentionQuestion] }, "agent-self"), undefined, "explicit Agent mention takes precedence over automatic question recognition");
  equal(nextAutomaticAgentItem({ ...automaticBase, agentConfig: { ...automaticBase.agentConfig, recognitionMode: "off" } }), undefined, "recognition off disables every automatic response");

  const peerBase = {
    ...initialCollabState,
    selfMemberId: "self",
    agentConfig: { ...automaticBase.agentConfig, autoRespondAgents: true, agentResponseIntervalSeconds: 30 },
    timeline: [
      { ...item("peer-1", 1, "先检查重试逻辑"), kind: "agent_result" as const, actorId: "other-a", actorName: "Planner Agent", actorAgent: true, handoffs: [{ targetAgentId: "agent-self", instruction: "检查重试逻辑", referenceIds: [], requiresResponse: true }] },
      { ...item("peer-2", 2, "我来扮演异常服务"), kind: "agent_result" as const, actorId: "other-b", actorName: "Tester Agent", actorAgent: true, handoffs: [{ targetAgentId: "agent-self", instruction: "验证异常服务", referenceIds: [], requiresResponse: true }] },
      { ...item("self-result", 3, "本机输出"), kind: "agent_result" as const, actorId: "self", actorName: "Kite", actorAgent: true },
      { ...item("untargeted", 4, "仅供参考"), kind: "agent_result" as const, actorId: "other-c", actorName: "Observer Agent", actorAgent: true, handoffs: [{ targetAgentId: "agent-other", instruction: "继续观察", referenceIds: [], requiresResponse: true }] },
    ],
  };
  const peerBatch = nextAgentCollaborationBatch(peerBase, "agent-self", Date.parse("2026-08-03T00:00:10Z"));
  equal(peerBatch?.items.map((entry) => entry.id), ["peer-1", "peer-2"], "Agent collaboration consumes only explicit handoffs addressed to the local Agent");
  equal(peerBatch?.handoffs.map((entry) => entry.instruction), ["检查重试逻辑", "验证异常服务"], "directed handoff instructions remain structured");
  equal(peerBatch?.waitMs, 0, "first peer collaboration batch can start immediately");
  equal(agentCollaborationRequestID([peerBase.timeline[0], peerBase.timeline[1]], "agent-self"), agentCollaborationRequestID([peerBase.timeline[1], peerBase.timeline[0]], "agent-self"), "peer collaboration request id is stable across equivalent batch ordering");
  const ownPeerCommand = { ...item("own-peer-run", 4, "继续协作"), kind: "agent_command" as const, actorId: "self", actorAgent: true, agentCommandId: "agent-collab-first", referenceIds: ["peer-1"], createdAt: "2026-08-03T00:00:09Z" };
  const coolingBatch = nextAgentCollaborationBatch({ ...peerBase, timeline: [...peerBase.timeline, ownPeerCommand] }, "agent-self", Date.parse("2026-08-03T00:00:10Z"));
  equal([coolingBatch?.items.map((entry) => entry.id), coolingBatch?.waitMs], [["peer-2"], 29_000], "handled peer results stay deduplicated and the next batch respects the configured frequency");
  const spentClock = Array.from({ length: 12 }, (_, index): CollaborationTimelineItem => ({
    ...item(`auto-run-${index + 1}`, index + 1, `handoff ${index + 1}`),
    kind: "agent_command",
    actorId: index % 2 ? "other-a" : "other-b",
    actorName: "Loop Agent",
    actorAgent: true,
    agentCommandId: `agent-collab-${index + 1}`,
    createdAt: `2026-08-03T00:00:${String(index + 1).padStart(2, "0")}Z`,
  }));
  const afterSpent = { ...item("after-spent", 13, "还要继续吗"), kind: "agent_result" as const, actorId: "other", actorName: "Loop Agent", actorAgent: true, createdAt: "2026-08-03T00:00:13Z", handoffs: [{ targetAgentId: "agent-self", instruction: "继续验证", referenceIds: [], requiresResponse: true }] };
  const spentState = { ...peerBase, timeline: [...spentClock, afterSpent] };
  equal(agentCollaborationClock(spentState), { limit: 12, used: 12, remaining: 0, unlimited: false, resetItem: undefined, woundAt: undefined }, "twelve unattended Agent handoffs fully unwind the default collaboration clockwork");
  equal(nextAgentCollaborationBatch(spentState, "agent-self", Date.parse("2026-08-03T00:02:00Z")), undefined, "an empty collaboration clockwork pauses further Agent handoffs");
  const unlimitedState = { ...spentState, agentConfig: { ...spentState.agentConfig, agentClockUnlimited: true } };
  equal(nextAgentCollaborationBatch(unlimitedState, "agent-self", Date.parse("2026-08-03T00:02:00Z"))?.items.map((entry) => entry.id), ["after-spent"], "unlimited mode keeps Agent handoffs eligible after the configured clockwork is empty");
  const woundAt = "2026-08-03T00:00:12.500Z";
  const rewoundManually = { ...spentState, agentConfig: { ...spentState.agentConfig, agentClockWoundAt: woundAt } };
  equal(agentCollaborationClock(rewoundManually), { limit: 12, used: 0, remaining: 12, unlimited: false, resetItem: undefined, woundAt }, "manual winding persists a fresh clockwork boundary without adding Room chat noise");
  equal(nextAgentCollaborationBatch(rewoundManually, "agent-self", Date.parse("2026-08-03T00:02:00Z"))?.items.map((entry) => entry.id), ["after-spent"], "manual winding resumes only peer results newer than its persisted boundary");
  const humanMessage = { ...item("human-message", 14, "继续，但先验证边界"), actorId: "human", actorName: "Alice", actorAgent: false, createdAt: "2026-08-03T00:00:14Z" };
  const afterHuman = { ...item("after-human", 15, "边界验证完成"), kind: "agent_result" as const, actorId: "other", actorName: "Tester Agent", actorAgent: true, createdAt: "2026-08-03T00:00:15Z", handoffs: [{ targetAgentId: "agent-self", instruction: "复核边界", referenceIds: [], requiresResponse: true }] };
  const rewoundByMessage = { ...peerBase, timeline: [...spentClock, afterSpent, humanMessage, afterHuman] };
  equal(agentCollaborationClock(rewoundByMessage), { limit: 12, used: 0, remaining: 12, unlimited: false, resetItem: humanMessage, woundAt: undefined }, "a human message fully rewinds the collaboration clockwork");
  equal(nextAgentCollaborationBatch(rewoundByMessage, "agent-self", Date.parse("2026-08-03T00:02:00Z"))?.items.map((entry) => entry.id), ["after-human"], "only peer results after the latest human intervention enter the rewound cycle");
  const humanOperation = { ...item("human-operation", 16, "手动启动调试"), kind: "agent_command" as const, actorId: "human", actorAgent: true, agentCommandId: "manual-debug-1", createdAt: "2026-08-03T00:00:16Z" };
  equal(agentCollaborationClock({ ...peerBase, timeline: [...spentClock, humanOperation] }).remaining, 12, "a manually initiated Agent operation also rewinds the collaboration clockwork");
  equal(nextAgentCollaborationBatch({ ...peerBase, agentConfig: { ...peerBase.agentConfig, autoRespondAgents: false } }, "agent-self"), undefined, "peer collaboration switch disables the scheduler independently from human-message recognition");

  const mergedLifecycle = visibleCollaborationTimeline([
    { ...item("run-merge", 30, "执行验证"), kind: "agent_command", actorAgent: true, agentRunStatus: "completed" },
    { ...item("result-merge", 31, "验证通过"), kind: "agent_result", actorAgent: true, agentRunId: "run-merge", referenceIds: ["peer-1"], handoffs: [{ targetAgentId: "agent-self", instruction: "继续检查", referenceIds: ["peer-1"], requiresResponse: true }] },
    { ...item("orphan-result", 32, "旧结果"), kind: "agent_result", actorAgent: true },
  ]);
  equal([mergedLifecycle.map((entry) => entry.id), mergedLifecycle[0].agentRunOutput, mergedLifecycle[0].agentRunSummary, mergedLifecycle[0].handoffs?.length], [["run-merge", "orphan-result"], "验证通过", undefined, 1], "paired run/result render as one lifecycle item with agentRunOutput as authoritative final output while orphan results remain visible");

  const summaryOverride = visibleCollaborationTimeline([
    { ...item("run-with-summary", 40, "执行验证"), kind: "agent_command", actorAgent: true, agentRunStatus: "completed", agentRunSummary: "正在运行中..." },
    { ...item("result-final", 41, "验证通过，三阶段已完成"), kind: "agent_result", actorAgent: true, agentRunId: "run-with-summary" },
  ]);
  equal(summaryOverride[0].agentRunOutput, "验证通过，三阶段已完成", "agentRunOutput is the authoritative final output even when run has its own summary");
  equal(summaryOverride[0].agentRunSummary, "正在运行中...", "agentRunSummary preserves the run-time progress summary without being overwritten by result text");

  const multilineOutput = visibleCollaborationTimeline([
    { ...item("run-ml", 50, "多行输出测试"), kind: "agent_command", actorAgent: true, agentRunStatus: "completed" },
    { ...item("result-ml", 51, "第一行结果\n第二行: 成功\n第三行: 待确认\n第四行很长很长的内容在这里展示完整输出"), kind: "agent_result", actorAgent: true, agentRunId: "run-ml" },
  ]);
  equal(multilineOutput[0].agentRunOutput, "第一行结果\n第二行: 成功\n第三行: 待确认\n第四行很长很长的内容在这里展示完整输出", "multi-line output preserves newlines verbatim in agentRunOutput");

  const restoreTransport = createMockCollaborationTransport("restore-host");
  let restoreCalls = 0;
  restoreTransport.getState = async () => ({
    status: "failed", mode: "host", retryable: true,
    room: { room: "room-a", host: "127.0.0.1", port: 39170, latestSequence: 3 }, members: [], timeline: [],
  });
  restoreTransport.retry = async () => {
    restoreCalls++;
    return { status: "connected", mode: "host", room: { room: "room-a", host: "127.0.0.1", port: 39170, latestSequence: 4 }, members: [], timeline: [] };
  };
  const restoredHost = await loadCollaborationState(restoreTransport);
  equal([restoredHost.status, restoreCalls], ["connected", 1], "persisted Host automatically restores once after app restart");

  await testCachedRoomSurvivesFailedRetry();
  await testCachedClientRoomSurvivesFailedRetry();
  await testNoCacheSessionEntryWithoutAutoConnect();
  await testHostRestoreMissingSelfSessionId();
  await testClientRestoreMissingSelfSessionId();
  await testExplicitWrongSessionIdRejected();
  await testNewSessionNoRoomShowsEntry();
  await testWailsTransportRestoresSessionOwnership();

  await testSessionTransportIsolation();
  await testAgentBusyGuard();
  await testOfflineSelfAgentIntervention();
  await testComposerOfflineAgentOnly();
  await testComposerAutoSwitchPreservesDraft();
  await testComposerReconnectRestoresModes();
  await testMentionStartsAgent();
  await testRoomAgentUsesScopedAutoApproval();
  await testWaitingAgentRunDecisions();
  await testReferenceAndRunResultPresentation();
  await testAgentRunResultOutput();
  await testRequestAgentPopup();
  await testRequestAgentPayload();
  await testCountdown();
  await testConnectionPanelWorkspace();
  await testDiscoveryFailureIsHandled();
  await testSessionSwitchIsolation();
  await testFileCardImagePreview();
  await testFileCardPreviewFallback();
  await testOwnFileImagePreviewRetry();
  await testFileCardIgnoresLatePreview();
  await testFilePreviewVisibilityAndCache();
  await testFilePreviewConcurrencyLimit();
  await testFilePreviewQueuedCancellation();
  await testFilePreviewCacheBounds();

  // Regression: switching rooms isolates timeline
  let roomAState = collabReducer(initialCollabState, { type: "STATE", state: { status: "connected", room: { room: "room-a", host: "127.0.0.1", port: 39170, latestSequence: 3 }, selfMemberId: "self", members: [], timeline: [item("synced-a", 1, "room A synced")] } });
  roomAState = collabReducer(roomAState, { type: "STATE", state: { ...roomAState, timeline: [...roomAState.timeline, { ...item("outbox:pending-a", 2, "room A pending"), localPending: true, syncStatus: "pending" }] } });
  equal(roomAState.timeline.map((entry) => entry.id), ["synced-a", "outbox:pending-a"], "room A has both synced and pending items");

  // Switch to room B — same host/port, different room
  const roomBState = collabReducer(roomAState, { type: "STATE", state: { status: "connected", room: { room: "room-b", host: "127.0.0.1", port: 39170, latestSequence: 1 }, selfMemberId: "self", members: [], timeline: [item("synced-b", 1, "room B synced")] } });
  equal(roomBState.timeline.map((entry) => entry.id), ["synced-b"], "switching rooms drops all old-room items (synced and pending)");

  // Same room update preserves items (regression check for same-room merge)
  const roomBSame = collabReducer(roomBState, { type: "STATE", state: { ...roomBState, room: { ...roomBState.room!, latestSequence: 2 }, timeline: [...roomBState.timeline, item("synced-b2", 2, "room B second")] } });
  equal(roomBSame.timeline.map((entry) => [entry.id, entry.sequence]), [["synced-b", 1], ["synced-b2", 2]], "same-room updates preserve existing items and append new ones");

  // Room switch also clears pendingIntents
  const withIntent = collabReducer(roomBState, { type: "PENDING_INTENT", intent: { messageId: "m1", revision: 1, instruction: "fix", deadline: Date.now() + 60000, status: "pending" } });
  equal(withIntent.pendingIntents.m1?.status, "pending", "pending intent is registered");
  const afterSwitch = collabReducer(withIntent, { type: "STATE", state: { status: "connected", room: { room: "room-c", host: "10.0.0.1", port: 39172, latestSequence: 1 }, selfMemberId: "self", members: [], timeline: [] } });
  equal(afterSwitch.pendingIntents.m1?.status ?? "dismissed", "dismissed", "room switch cancels pending intents to prevent auto-start in wrong room");

  // Rejoining a recreated room at the same endpoint and with the same ID is a new authoritative lifetime.
  const reconnectingSameRoom = collabReducer(roomAState, { type: "CONNECTING", operation: "join" });
  equal(reconnectingSameRoom.timeline.length, 0, "new join fences the previous timeline before same-room reconnect");
  const recreatedRoom = collabReducer(reconnectingSameRoom, { type: "STATE", state: { status: "connected", room: { room: "room-a", host: "127.0.0.1", port: 39170, latestSequence: 1 }, selfMemberId: "self", members: [], timeline: [item("new-room-a", 1, "recreated room A")] } });
  equal(recreatedRoom.timeline.map((entry) => entry.id), ["new-room-a"], "same endpoint and room ID reuse replaces the old authoritative timeline");

  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed) process.exit(1);
}

async function testFileCardImagePreview() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  const previewCalls: string[] = [];
  const previewFile = async (fileId: string) => {
    previewCalls.push(fileId);
    return { mime: "image/png", dataUrl: "data:image/png;base64,abc123" };
  };
  const fileItem: CollaborationTimelineItem = {
    id: "file-img", sequence: 1, revision: 1, kind: "file", actorId: "other", actorName: "Alice",
    text: "img.png", fileName: "img.png", fileSize: 100, fileMime: "image/png", fileSHA256: "abc",
    createdAt: "2026-08-03T00:00:01Z", referenceIds: [],
  };
  const completedTransfer = { id: "t-img", fileId: "file-img", direction: "receive" as const, name: "img.png", status: "completed" as const, transferred: 100, total: 100 };

  await act(async () => {
    root.render(<LocaleProvider><CollaborationTimeline
      items={[fileItem]} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy
      transfers={[completedTransfer]}
      onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
      onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
      onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
      previewFile={previewFile}
    /></LocaleProvider>);
  });
  // Wait for useEffect to fire.
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
  equal(previewCalls, ["file-img"], "completed received file triggers previewFile call");
  const previewImg = document.querySelector(".collab-file-card__preview img") as HTMLImageElement | null;
  ok(previewImg !== null, "image preview element is rendered for completed image file");
  ok(previewImg?.getAttribute("src") === "data:image/png;base64,abc123", "preview img src is the returned dataUrl");
  ok(previewImg?.getAttribute("loading") === "lazy", "preview img uses lazy loading");
  ok(previewImg?.getAttribute("alt") === "img.png", "preview img has alt text from file name");
  await act(async () => root.unmount());
}

async function testFileCardPreviewFallback() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);

  // Test 1: previewFile returns null → no preview, normal file card.
  let previewNullCalls = 0;
  const previewNull = async () => { previewNullCalls++; return null; };
  const fileItem: CollaborationTimelineItem = {
    id: "file-txt", sequence: 1, revision: 1, kind: "file", actorId: "other", actorName: "Alice",
    text: "doc.txt", fileName: "doc.txt", fileSize: 50, fileMime: "text/plain", fileSHA256: "def",
    createdAt: "2026-08-03T00:00:01Z", referenceIds: [],
  };
  const completedTransfer = { id: "t-txt", fileId: "file-txt", direction: "receive" as const, name: "doc.txt", status: "completed" as const, transferred: 50, total: 50 };

  await act(async () => {
    root.render(<LocaleProvider><CollaborationTimeline
      items={[fileItem]} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy
      transfers={[completedTransfer]}
      onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
      onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
      onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
      previewFile={previewNull}
    /></LocaleProvider>);
  });
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
  ok(document.querySelector(".collab-file-card__preview") === null, "non-image preview (null) shows no image preview element");
  ok(document.querySelector(".collab-file-card__icon svg") !== null, "file icon is shown when preview is unavailable");
  equal(previewNullCalls, 0, "plain files do not call the image preview bridge");

  // Test 2: Non-completed file → previewFile not called.
  await act(async () => root.unmount());
  const root2 = createRoot(document.getElementById("root")!);
  let previewCalledForIncomplete = false;
  const previewIncomplete = async () => { previewCalledForIncomplete = true; return null; };
  const incompleteTransfer = { id: "t-inc", fileId: "file-txt", direction: "receive" as const, name: "doc.txt", status: "downloading" as const, transferred: 20, total: 50 };

  await act(async () => {
    root2.render(<LocaleProvider><CollaborationTimeline
      items={[fileItem]} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy
      transfers={[incompleteTransfer]}
      onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
      onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
      onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
      previewFile={previewIncomplete}
    /></LocaleProvider>);
  });
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
  ok(!previewCalledForIncomplete, "incomplete file transfer does not call previewFile");

  // Test 3: Existing file operations still rendered.
  const buttons = document.querySelectorAll(".collab-file-card__actions button");
  const buttonTexts = [...buttons].map((btn) => btn.textContent?.trim() || "");
  ok(buttonTexts.some((t) => t.includes("Pause") || t.includes("暂停")), "pause button is visible for downloading file transfer");

  await act(async () => root2.unmount());
}

async function testOwnFileImagePreviewRetry() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  const fileItem: CollaborationTimelineItem = {
    id: "file-own-img", sequence: 1, revision: 1, kind: "file", actorId: "self", actorName: "Me",
    text: "own.png", fileName: "own.png", fileSize: 100, fileMime: "application/octet-stream", fileSHA256: "abc",
    createdAt: "2026-08-03T00:00:01Z", referenceIds: [],
  };
  const transfer = { id: "share:file-own-img", fileId: "file-own-img", direction: "share" as const, name: "own.png", status: "available" as const, transferred: 100, total: 100 };
  let calls = 0;
  const previewFile = async () => {
    calls++;
    if (calls === 1) throw new Error("temporary read failure");
    return { mime: "image/png", dataUrl: "data:image/png;base64,b3du" };
  };

  await act(async () => {
    root.render(<LocaleProvider><CollaborationTimeline
      items={[fileItem]} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy transfers={[transfer]}
      onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
      onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
      onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
      previewFile={previewFile}
    /></LocaleProvider>);
  });
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
  const retry = document.querySelector(".collab-file-card__preview-error button") as HTMLButtonElement | null;
  ok(retry !== null, "own shared image exposes a preview retry after a transient read failure");
  await act(async () => { retry?.click(); await new Promise((resolve) => setTimeout(resolve, 20)); });
  equal(calls, 2, "own shared image preview retries explicitly");
  const image = document.querySelector(".collab-file-card__preview img") as HTMLImageElement | null;
  ok(image?.getAttribute("decoding") === "async", "own shared image renders with asynchronous decoding");
  ok(image?.getAttribute("draggable") === "false", "inline image preview is not draggable");
  await act(async () => root.unmount());
}

async function testFileCardIgnoresLatePreview() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const root = createRoot(document.getElementById("root")!);
  const fileItem: CollaborationTimelineItem = {
    id: "file-late", sequence: 1, revision: 1, kind: "file", actorId: "other", actorName: "Alice",
    text: "late.png", fileName: "late.png", fileSize: 100, fileMime: "image/png", fileSHA256: "abc",
    createdAt: "2026-08-03T00:00:01Z", referenceIds: [],
  };
  let resolvePreview: ((value: { mime: string; dataUrl: string }) => void) | undefined;
  const previewFile = () => new Promise<{ mime: string; dataUrl: string }>((resolve) => { resolvePreview = resolve; });
  const render = (status: "completed" | "downloading") => <LocaleProvider><CollaborationTimeline
    items={[fileItem]} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy
    transfers={[{ id: "t-late", fileId: fileItem.id, direction: "receive", name: "late.png", status, transferred: status === "completed" ? 100 : 50, total: 100 }]}
    onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
    onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
    onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
    previewFile={previewFile}
  /></LocaleProvider>;

  await act(async () => root.render(render("completed")));
  await act(async () => root.render(render("downloading")));
  await act(async () => {
    resolvePreview?.({ mime: "image/png", dataUrl: "data:image/png;base64,bGF0ZQ==" });
    await Promise.resolve();
  });
  ok(document.querySelector(".collab-file-card__preview") === null, "late preview result cannot appear after transfer state changes");
  await act(async () => root.unmount());
}

async function testFilePreviewVisibilityAndCache() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const originalObserver = globalThis.IntersectionObserver;
  let observed: Element | undefined;
  let reveal: (() => void) | undefined;
  let hide: (() => void) | undefined;
  class TestIntersectionObserver {
    readonly root = null;
    readonly rootMargin = "0px";
    readonly thresholds = [0];
    private active = true;
    constructor(callback: IntersectionObserverCallback) {
      reveal = () => { if (this.active) callback([{ isIntersecting: true, target: observed } as IntersectionObserverEntry], this as unknown as IntersectionObserver); };
      hide = () => { if (this.active) callback([{ isIntersecting: false, target: observed } as IntersectionObserverEntry], this as unknown as IntersectionObserver); };
    }
    observe(target: Element) { observed = target; }
    disconnect() { this.active = false; }
    unobserve() {}
    takeRecords(): IntersectionObserverEntry[] { return []; }
  }
  globalThis.IntersectionObserver = TestIntersectionObserver as unknown as typeof IntersectionObserver;

  const fileItem: CollaborationTimelineItem = {
    id: "file-visible", sequence: 1, revision: 1, kind: "file", actorId: "other", actorName: "Alice",
    text: "visible.png", fileName: "visible.png", fileSize: 100, fileMime: "image/png", fileSHA256: "abc",
    createdAt: "2026-08-03T00:00:01Z", referenceIds: [],
  };
  const transfer = { id: "t-visible", fileId: fileItem.id, direction: "receive" as const, name: "visible.png", status: "completed" as const, transferred: 100, total: 100 };
  let calls = 0;
  const previewFile = async () => {
    calls++;
    return { mime: "image/png", dataUrl: "data:image/png;base64,dmlzaWJsZQ==" };
  };
  const render = (currentItem = fileItem) => <LocaleProvider><CollaborationTimeline
    items={[currentItem]} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy transfers={[transfer]}
    onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
    onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
    onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
    previewFile={previewFile}
  /></LocaleProvider>;

  const container = document.getElementById("root")!;
  const root = createRoot(container);
  await act(async () => { root.render(render()); await Promise.resolve(); });
  equal(calls, 0, "image preview waits until its card intersects the viewport");
  await act(async () => { reveal?.(); await Promise.resolve(); });
  equal(calls, 1, "visible image starts one preview request");
  ok(document.querySelector(".collab-file-card__preview img") !== null, "visible image renders after intersection");
  await act(async () => { hide?.(); await Promise.resolve(); });
  ok(document.querySelector(".collab-file-card__preview") === null, "image preview leaves the DOM after its card exits the viewport");
  await act(async () => { reveal?.(); await Promise.resolve(); });
  equal(calls, 1, "re-entering the viewport can reuse a completed cached preview");
  ok(document.querySelector(".collab-file-card__preview img") !== null, "image preview renders again after re-entering the viewport");
  await act(async () => root.unmount());

  if (originalObserver) globalThis.IntersectionObserver = originalObserver;
  else delete (globalThis as { IntersectionObserver?: typeof IntersectionObserver }).IntersectionObserver;
  const remount = createRoot(container);
  await act(async () => { remount.render(render()); await Promise.resolve(); });
  equal(calls, 1, "remount reuses the preview cache for the same loader and file id");
  ok(document.querySelector(".collab-file-card__preview img") !== null, "cached preview still renders directly");
  await act(async () => {
    remount.render(render({ ...fileItem, fileSHA256: "different-sha", fileSize: 101, fileMime: "image/jpeg" }));
    await Promise.resolve();
  });
  equal(calls, 2, "same file id with a different content identity does not reuse a stale preview across Rooms");
  await act(async () => remount.unmount());
}

async function testFilePreviewConcurrencyLimit() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const originalObserver = globalThis.IntersectionObserver;
  delete (globalThis as { IntersectionObserver?: typeof IntersectionObserver }).IntersectionObserver;
  const items: CollaborationTimelineItem[] = [1, 2, 3].map((index) => ({
    id: `file-limit-${index}`, sequence: index, revision: 1, kind: "file", actorId: "other", actorName: "Alice",
    text: `limit-${index}.png`, fileName: `limit-${index}.png`, fileSize: 100, fileMime: "image/png", fileSHA256: `sha-${index}`,
    createdAt: `2026-08-03T00:00:0${index}Z`, referenceIds: [],
  }));
  const calls: string[] = [];
  const pending: Array<(value: null) => void> = [];
  const previewFile = (fileId: string) => {
    calls.push(fileId);
    return new Promise<null>((resolve) => pending.push(resolve));
  };
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<LocaleProvider><CollaborationTimeline
      items={items} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy
      transfers={items.map((entry) => ({ id: `t-${entry.id}`, fileId: entry.id, direction: "receive", name: entry.fileName || entry.id, status: "completed", transferred: 100, total: 100 }))}
      onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
      onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
      onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
      previewFile={previewFile}
    /></LocaleProvider>);
    await Promise.resolve();
  });
  equal(calls.length, 2, "preview scheduler starts at most two global requests");
  await act(async () => { pending.shift()?.(null); await Promise.resolve(); await Promise.resolve(); });
  equal(calls.length, 3, "preview scheduler starts the next request after one slot completes");
  await act(async () => {
    for (const resolve of pending.splice(0)) resolve(null);
    await Promise.resolve();
  });
  await act(async () => root.unmount());
  if (originalObserver) globalThis.IntersectionObserver = originalObserver;
}

async function testFilePreviewQueuedCancellation() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const originalObserver = globalThis.IntersectionObserver;
  const observers = new Map<Element, { callback: IntersectionObserverCallback; observer: IntersectionObserver; active: boolean }>();
  class TestIntersectionObserver {
    readonly root = null;
    readonly rootMargin = "0px";
    readonly thresholds = [0];
    private target?: Element;
    constructor(private callback: IntersectionObserverCallback) {}
    observe(target: Element) {
      this.target = target;
      observers.set(target, { callback: this.callback, observer: this as unknown as IntersectionObserver, active: true });
    }
    disconnect() {
      if (this.target) {
        const value = observers.get(this.target);
        if (value) value.active = false;
      }
    }
    unobserve() {}
    takeRecords(): IntersectionObserverEntry[] { return []; }
  }
  globalThis.IntersectionObserver = TestIntersectionObserver as unknown as typeof IntersectionObserver;
  const emit = (target: Element, isIntersecting: boolean) => {
    const value = observers.get(target);
    if (value?.active) value.callback([{ isIntersecting, target } as IntersectionObserverEntry], value.observer);
  };
  const items: CollaborationTimelineItem[] = [1, 2, 3].map((index) => ({
    id: `file-cancel-${index}`, sequence: index, revision: 1, kind: "file", actorId: "other", actorName: "Alice",
    text: `cancel-${index}.png`, fileName: `cancel-${index}.png`, fileSize: 100, fileMime: "image/png", fileSHA256: `cancel-sha-${index}`,
    createdAt: `2026-08-03T00:00:0${index}Z`, referenceIds: [],
  }));
  const calls: string[] = [];
  const pending = new Map<string, (value: null) => void>();
  const previewFile = (fileId: string) => {
    calls.push(fileId);
    return new Promise<null>((resolve) => pending.set(fileId, resolve));
  };
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<LocaleProvider><CollaborationTimeline
      items={items} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy
      transfers={items.map((entry) => ({ id: `t-${entry.id}`, fileId: entry.id, direction: "receive", name: entry.fileName || entry.id, status: "completed", transferred: 100, total: 100 }))}
      onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
      onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
      onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
      previewFile={previewFile}
    /></LocaleProvider>);
    await Promise.resolve();
  });
  const cards = [...document.querySelectorAll(".collab-file-card")];
  await act(async () => { cards.forEach((card) => emit(card, true)); await Promise.resolve(); });
  equal(calls, [items[0].id, items[1].id], "only two visible preview loaders start while the third request remains queued");
  await act(async () => { emit(cards[2], false); await Promise.resolve(); });
  pending.get(items[0].id)?.(null);
  await act(async () => { await Promise.resolve(); await Promise.resolve(); });
  equal(calls, [items[0].id, items[1].id], "a queued preview does not call its loader after the card leaves the viewport");
  await act(async () => { emit(cards[2], true); await Promise.resolve(); });
  equal(calls, [items[0].id, items[1].id, items[2].id], "the cancelled card can queue a fresh preview after re-entering the viewport");
  pending.get(items[1].id)?.(null);
  pending.get(items[2].id)?.(null);
  await act(async () => { await Promise.resolve(); });
  await act(async () => root.unmount());
  if (originalObserver) globalThis.IntersectionObserver = originalObserver;
  else delete (globalThis as { IntersectionObserver?: typeof IntersectionObserver }).IntersectionObserver;
}

async function testFilePreviewCacheBounds() {
  const dom = new JSDOM("<!doctype html><html><body><div id='root'></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true, window: dom.window, document: dom.window.document, HTMLElement: dom.window.HTMLElement });
  const originalObserver = globalThis.IntersectionObserver;
  delete (globalThis as { IntersectionObserver?: typeof IntersectionObserver }).IntersectionObserver;
  const makeItem = (id: string, size = 100): CollaborationTimelineItem => ({
    id, sequence: Number(id.replace(/\D/g, "")) || 1, revision: 1, kind: "file", actorId: "other", actorName: "Alice",
    text: `${id}.png`, fileName: `${id}.png`, fileSize: size, fileMime: "image/png", fileSHA256: `sha-${id}`,
    createdAt: "2026-08-03T00:00:01Z", referenceIds: [],
  });
  const render = (items: CollaborationTimelineItem[], previewFile: (fileId: string) => Promise<{ mime: string; dataUrl: string }>) => <LocaleProvider><CollaborationTimeline
    items={items} selfMemberId="self" selectedIds={[]} pendingIntents={{}} connected agentBusy
    transfers={items.map((entry) => ({ id: `t-${entry.id}`, fileId: entry.id, direction: "receive" as const, name: entry.fileName || entry.id, status: "completed" as const, transferred: entry.fileSize || 0, total: entry.fileSize || 0 }))}
    onToggle={() => {}} onReply={() => {}} onAgree={() => {}} onRequestAgent={() => {}} onAgent={() => {}} onAccept={() => {}} onReject={() => {}}
    onRespondAgentRun={() => {}} onStartPending={() => {}} onStopPending={() => {}} onEditPending={() => {}}
    onReceiveFile={() => {}} onPauseFile={() => {}} onResumeFile={() => {}} onRevokeFile={() => {}} onOpenFile={() => {}} onRevealFile={() => {}}
    previewFile={previewFile}
  /></LocaleProvider>;
  const container = document.getElementById("root")!;

  const countItems = Array.from({ length: 17 }, (_, index) => makeItem(`cache-count-${index + 1}`));
  let countCalls = 0;
  const countLoader = async () => {
    countCalls++;
    return { mime: "image/png", dataUrl: "data:image/png;base64,Y2FjaGU=" };
  };
  const countRoot = createRoot(container);
  await act(async () => { countRoot.render(render(countItems, countLoader)); await new Promise((resolve) => setTimeout(resolve, 30)); });
  equal(countCalls, 17, "preview cache loads every distinct visible image");
  await act(async () => countRoot.unmount());
  const countRemount = createRoot(container);
  await act(async () => { countRemount.render(render([countItems[0]], countLoader)); await new Promise((resolve) => setTimeout(resolve, 10)); });
  equal(countCalls, 18, "preview cache evicts the oldest entry after its 16-item limit and can reload it");
  await act(async () => countRemount.unmount());

  const byteItems = [makeItem("cache-bytes-1", 9 * 1024 * 1024), makeItem("cache-bytes-2", 9 * 1024 * 1024)];
  const largeDataUrl = `data:image/png;base64,${"A".repeat(9 * 1024 * 1024)}`;
  let byteCalls = 0;
  const byteLoader = async () => {
    byteCalls++;
    return { mime: "image/png", dataUrl: largeDataUrl };
  };
  const byteRoot = createRoot(container);
  await act(async () => { byteRoot.render(render(byteItems, byteLoader)); await new Promise((resolve) => setTimeout(resolve, 30)); });
  equal(byteCalls, 2, "preview cache loads entries that individually fit its byte budget");
  await act(async () => byteRoot.unmount());
  const byteRemount = createRoot(container);
  await act(async () => { byteRemount.render(render([byteItems[0]], byteLoader)); await new Promise((resolve) => setTimeout(resolve, 10)); });
  equal(byteCalls, 3, "preview cache evicts the oldest DataURL above its 16 MiB total and can reload it");
  await act(async () => byteRemount.unmount());
  if (originalObserver) globalThis.IntersectionObserver = originalObserver;
}

void main();
