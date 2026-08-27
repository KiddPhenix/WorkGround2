import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { assistantCopy } from "../custom/features/assistant/assistant.copy";
import { isCollapsedAssistantSection, splitAssistantMarkdown } from "../custom/features/assistant/AssistantMarkdown";
import { attentionInboxAction, attentionNeedsRebind, attentionRejectResolution, attentionResolution, nextRoutineDate, responsibilityLabel, responsibilityStatusLabel, runContentTitle, runHasActionableAttention, runHistoryAction, scheduleLabel, timelineEntries, applyAssistantDispatchStream, liveReplyFromDispatch, reconcileAssistantLiveReply, seedAssistantLiveReply, type AssistantLiveReply } from "../custom/features/assistant/assistant.model";
import { assistantIntentKey, assistantMutationKey, assistantOutcomeKey, completeAssistantRequest, pendingAssistantMutation, pendingAssistantRequest, runAssistantApproval, runAssistantCASMutation, runAssistantOutcome, runAssistantResume } from "../custom/features/assistant/assistant.requests";
import type { AssistantAttentionItem, AssistantDispatchStreamEvent, AssistantRun, AssistantSnapshot } from "../custom/features/assistant/assistant.types";
import { normalizeAssistantList } from "../custom/features/assistant/assistant.bridge";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

console.log("\nassistant logic");
const copy = assistantCopy("zh");
const listResult = normalizeAssistantList({
  items: [{ id: "healthy" }],
  diagnostics: [{ at: "2026-08-17T00:00:00Z", operation: "list", message: "corrupt snapshot skipped" }],
});
ok(listResult.items.length === 1, "typed list keeps healthy assistants when another snapshot is corrupt");
ok(listResult.diagnostics.length === 1, "typed list preserves non-blocking diagnostics");
ok(normalizeAssistantList([{ id: "legacy" }]).items.length === 1, "list normalizer tolerates the previous array response during upgrade");
ok(runHistoryAction("failed") === "rerun" && runHistoryAction("cancelled") === "rerun", "terminal failures start a new run instead of invalid Resume");
ok(runHistoryAction("retry_wait") === "cancel", "retry_wait stays on automatic retry and can only be cancelled");
ok(runHistoryAction("waiting_attention") === "attention", "waiting_attention routes through the durable inbox");
ok(attentionNeedsRebind("rebind_workspace"), "workspace rebind attention never approves a frozen old run");
ok(attentionNeedsRebind("cancel_recreate"), "cancel and recreate attention never approves a frozen old run");
ok(!attentionNeedsRebind("publish_release"), "ordinary attention can still follow the approval flow");
const attentionBase: AssistantAttentionItem = { id: "attention-state", assistant_id: "assistant-1", run_id: "run-waiting", request_id: "attention-request", action: "publish_release", summary: "确认发布", resume_token: "approval-2", state: "open", revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" };
const waitingRun: AssistantRun = { id: "run-waiting", assistant_id: "assistant-1", routine_id: "routine-1", request_id: "run-request", trigger: "manual", state: "waiting_attention", resume_token: "approval-2", attempt: 1, max_attempts: 3, revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" };
ok(attentionInboxAction({ ...attentionBase, state: "approved" }, waitingRun) === "continue", "approved attention remains actionable while its run still waits");
ok(attentionInboxAction({ ...attentionBase, resume_token: "approval-1", state: "approved" }, waitingRun) === "none", "an approved item from an earlier continuation stays in history instead of resurfacing");
ok(attentionInboxAction({ ...attentionBase, resume_token: "approval-1" }, waitingRun) === "none", "an open item from an earlier continuation cannot replace the current decision");
ok(attentionInboxAction({ ...attentionBase, state: "approved" }, { ...waitingRun, state: "running" }) === "none", "approved attention hides after its run continues");
ok(attentionInboxAction({ ...attentionBase, action: "answer_required" }, waitingRun) === "answer", "answer-required attention exposes an answer flow");
ok(attentionInboxAction({ ...attentionBase, action: "verify_run_outcome" }, waitingRun) === "verify", "unknown run outcome exposes the three-way verification flow");
ok(attentionInboxAction({ ...attentionBase, action: "verify_run_outcome", state: "approved", resolution: "retry_acknowledged" }, waitingRun) === "continue", "acknowledged retry remains resumable after refresh");
ok(attentionInboxAction({ ...attentionBase, action: "verify_run_outcome", state: "approved", resolution: "mark_succeeded" }, waitingRun) === "none", "marked-success outcome never resumes a waiting run");
ok(attentionInboxAction({ ...attentionBase, action: "verify_run_outcome", state: "approved", resolution: "mark_failed" }, waitingRun) === "none", "marked-failed outcome never resumes a waiting run");
ok(runHasActionableAttention(waitingRun, [{ ...attentionBase, action: "verify_run_outcome" }]) === true, "a waiting run with an open verification item stays actionable in the inbox");
ok(runHasActionableAttention(waitingRun, [{ ...attentionBase, action: "verify_run_outcome", state: "approved", resolution: "mark_succeeded" }]) === false, "a resolved outcome item does not leave the run in an actionable dead end");
ok(runHasActionableAttention(waitingRun, []) === false, "a waiting run with no attention item has no inbox path and must fall back to retry");
ok(runHasActionableAttention(waitingRun, [{ ...attentionBase, resume_token: "approval-1" }]) === false, "a mismatched historical item does not make the run actionable");
ok(runHasActionableAttention({ ...waitingRun, state: "running" }, [{ ...attentionBase, state: "approved" }]) === false, "a running run is never routed through the inbox fallback");
ok(attentionResolution("answer_required", "  用户答案  ", copy.resolveNote) === "用户答案", "answer-required resolution uses the editable user answer");
ok(attentionResolution("publish_release", "ignored", copy.resolveNote) === copy.resolveNote, "ordinary approval keeps its explicit desktop resolution");
ok(attentionRejectResolution("answer_required", copy.resolveNote, copy.rejectNote) === copy.rejectNote, "answer-required rejection records an explicit refusal without an answer");
const day = new Date(2026, 7, 17, 12, 0, 0);
const routine = {
  id: "routine-1", assistant_id: "assistant-1", title: "发布检查", prompt: "检查发布条件",
  schedule: { kind: "daily" as const, timezone: "Asia/Singapore", at: "18:00" }, enabled: true,
  catch_up: "coalesce_latest" as const, revision: 1, created_at: day.toISOString(), updated_at: day.toISOString(),
};
ok(scheduleLabel(routine, copy) === "每天 18:00", "daily schedule has a concise editable label");
ok(nextRoutineDate(routine, day)?.getHours() === 18, "daily routine resolves today's next occurrence");

const snapshot: AssistantSnapshot = {
  revision: 1,
  assistant: { id: "assistant-1", name: "代码项目助理", mission: "检查项目", scope: "workspace", workspace_root: "D:\\Work", lifecycle: "active", policy: { local_write: "allow", network: "deny", publish: "approve", delete: "approve", payment: "approve", secrets: "approve", private_data: "approve" }, memory_revision: 1, revision: 1, created_at: day.toISOString(), updated_at: day.toISOString() },
  routines: [routine],
  memory: { revision: 1, items: [{ id: "memory-1", kind: "strategy", body: "构建前先停旧进程", source_run: "run-1", locked: false, revision: 1, created_at: new Date(2026, 7, 17, 10, 5).toISOString(), updated_at: new Date(2026, 7, 17, 10, 5).toISOString() }] },
  runs: [{ id: "run-1", assistant_id: "assistant-1", routine_id: "routine-1", request_id: "req", trigger: "scheduled", state: "succeeded", attempt: 1, max_attempts: 3, summary: "测试通过。发布说明待补。", revision: 1, created_at: new Date(2026, 7, 17, 9, 30).toISOString(), updated_at: new Date(2026, 7, 17, 9, 31).toISOString(), started_at: new Date(2026, 7, 17, 9, 30).toISOString() }],
  attention: [],
  plan: { revision: 1, responsibilities: [{ id: "resp-1", assistant_id: "assistant-1", alias: "scan", objective: "扫描修改", status: "ready", revision: 1, created_at: day.toISOString(), updated_at: day.toISOString() }] },
  artifacts: [],
  opportunities: [],
  channels: [],
  channel_actions: [],
  channel_metrics: [],
  updated_at: day.toISOString(),
};
const entries = timelineEntries(snapshot, day, "zh", copy);
ok(entries.some((entry) => entry.kind === "run"), "timeline projects factual run state");
ok(entries.some((entry) => entry.kind === "memory"), "timeline projects explicit memory");
ok(entries.some((entry) => entry.kind === "next"), "timeline projects the next routine");
ok(entries.every((entry, index) => index === 0 || entries[index - 1].at.getTime() >= entry.at.getTime()), "timeline orders every entry newest first");
ok(entries.map((entry) => entry.kind).join(",") === "next,memory,run", "timeline keeps future, recent, and older entries in descending order");
const runEntry = entries.find((entry) => entry.kind === "run")!;
ok(runEntry.title === "发布检查", "routine run timeline title identifies the actual routine");
ok(runEntry.detail === "测试通过。发布说明待补。" && runEntry.title !== runEntry.detail, "run summary is preserved as detail, not promoted to the title");
ok(runEntry.prompt === undefined, "routine run entry never exposes its routine prompt as direct input");
ok(runContentTitle({ ...snapshot.runs[0], routine_id: undefined, prompt: "  第一行\n第二行指导  " }, undefined, "zh") === "第一行 第二行指导", "direct-input title uses normalized user content");
ok(runContentTitle({ ...snapshot.runs[0], routine_id: undefined, prompt: "" }, undefined, "zh") === "继续推进助手使命", "continue-mission run has an explicit intent title");
ok(runContentTitle({ ...snapshot.runs[0], routine_id: "missing", prompt: "冻结的例行任务内容" }, undefined, "en") === "冻结的例行任务内容", "missing routine falls back to its frozen task");
const longTitle = runContentTitle({ ...snapshot.runs[0], routine_id: undefined, prompt: "长".repeat(50) }, undefined, "zh");
ok(Array.from(longTitle).length === 41 && longTitle.endsWith("…"), "long run title is Unicode-safe and bounded");
const tiedEntries = timelineEntries({
  ...snapshot,
  routines: [],
  memory: { revision: 1, items: [] },
  runs: [{ ...snapshot.runs[0], id: "run-b" }, { ...snapshot.runs[0], id: "run-a" }],
}, day, "zh", copy);
ok(tiedEntries.map((entry) => entry.id).join(",") === "run-a,run-b", "timeline uses stable ids when timestamps and kinds match");

const directRun: AssistantRun = { id: "run-direct", assistant_id: "assistant-1", request_id: "req-direct", trigger: "manual", state: "succeeded", attempt: 1, max_attempts: 3, prompt: "第一行\n第二行批评", summary: "已记录", revision: 1, created_at: day.toISOString(), updated_at: day.toISOString(), started_at: day.toISOString() };
const directEntry = timelineEntries({ ...snapshot, routines: [], memory: { revision: 1, items: [] }, runs: [directRun] }, day, "zh", copy).find((entry) => entry.id === "run-direct")!;
ok(directEntry.title === "第一行 第二行批评", "direct-input run title exposes its actual content");
ok(directEntry.prompt === "第一行\n第二行批评", "direct-input run exposes its full original prompt");
ok(directEntry.detail === "已记录" && directEntry.prompt !== directEntry.detail, "direct-input run keeps its result summary independent from the prompt");

ok(responsibilityStatusLabel("blocked", "zh") === "被阻塞" && responsibilityStatusLabel("blocked", "en") === "Blocked", "responsibility status localizes");
ok(responsibilityStatusLabel("done", "zh") === "已完成", "done responsibility status is localized");
ok(responsibilityLabel({ id: "resp-1", alias: "scan" } as AssistantSnapshot["plan"]["responsibilities"][number]) === "scan", "responsibility label prefers the stable alias");

const key = assistantIntentKey("run", "assistant-1", "routine-1");
const first = pendingAssistantRequest(key);
const replay = pendingAssistantRequest(key);
ok(first === replay, "response-loss retry reuses the pending request id");
completeAssistantRequest(key);
const next = pendingAssistantRequest(key);
ok(next !== first, "successful completion rotates the request id for the next intent");
completeAssistantRequest(key);

const orderedKey = assistantMutationKey("update", "assistant-1", "assistant-1", { mission: "长期维护", name: "代码助理" });
const reorderedKey = assistantMutationKey("update", "assistant-1", "assistant-1", { name: "代码助理", mission: "长期维护" });
ok(orderedKey === reorderedKey, "mutation key fingerprints payloads independent of object key order");

const mutationIDs: string[] = [];
const mutationRevisions: number[] = [];
let loseMutationResponse = true;
const mutate = (visibleRevision: number) => runAssistantCASMutation(orderedKey, visibleRevision, async ({ requestId, expectedRevision }) => {
  mutationIDs.push(requestId);
  mutationRevisions.push(expectedRevision);
  if (loseMutationResponse) { loseMutationResponse = false; throw new Error("response lost"); }
  return true;
});
try { await mutate(3); } catch { /* response applied, then refreshed to revision 4 */ }
await mutate(4);
ok(mutationIDs[0] === mutationIDs[1], "Create/Update/Routine/Memory mutation helper retains its request after response loss");
ok(mutationRevisions[0] === 3 && mutationRevisions[1] === 3, "CAS replay freezes expectedRevision even after refreshed UI revision changes");
await mutate(4);
ok(mutationIDs[2] !== mutationIDs[1], "mutation helper clears its request only after confirmed success");

const persistedKey = assistantIntentKey("persisted-update", "assistant-1", "payload");
const persistedRequest = "desktop-assistant:persisted:restored";
const persistedValues = new Map<string, string>([[`workground2:assistant-request:${persistedKey}`, persistedRequest]]);
Object.defineProperty(globalThis, "localStorage", { configurable: true, value: {
  getItem: (name: string) => persistedValues.get(name) ?? null,
  setItem: (name: string, value: string) => { persistedValues.set(name, value); },
  removeItem: (name: string) => { persistedValues.delete(name); },
} });
ok(pendingAssistantRequest(persistedKey) === persistedRequest, "pending mutation receipt restores from durable storage after refresh");
completeAssistantRequest(persistedKey);
Reflect.deleteProperty(globalThis, "localStorage");

const casPersistedKey = assistantMutationKey("memory-upsert", "assistant-refresh", "memory-1", { body: "keep" });
const casPersistedValues = new Map<string, string>([[`workground2:assistant-mutation:${casPersistedKey}`, JSON.stringify({ requestId: "desktop-assistant:cas:restored", expectedRevision: 7 })]]);
Object.defineProperty(globalThis, "localStorage", { configurable: true, value: {
  getItem: (name: string) => casPersistedValues.get(name) ?? null,
  setItem: (name: string, value: string) => { casPersistedValues.set(name, value); },
  removeItem: (name: string) => { casPersistedValues.delete(name); },
} });
const restoredCAS = pendingAssistantMutation(casPersistedKey, 12);
ok(restoredCAS.requestId === "desktop-assistant:cas:restored" && restoredCAS.expectedRevision === 7, "refresh restores both CAS requestId and frozen expectedRevision");
completeAssistantRequest(casPersistedKey);
const legacyCASKey = assistantMutationKey("routine", "assistant-refresh", "routine-legacy", { title: "legacy" });
casPersistedValues.set(`workground2:assistant-request:${legacyCASKey}`, "desktop-assistant:legacy:string");
const migratedCAS = pendingAssistantMutation(legacyCASKey, 9);
ok(migratedCAS.requestId === "desktop-assistant:legacy:string" && migratedCAS.expectedRevision === 9, "legacy string ledger migrates with the first observed CAS revision");
completeAssistantRequest(legacyCASKey);
Reflect.deleteProperty(globalThis, "localStorage");

const resolveIDs: string[] = [];
const resumeIDs: string[] = [];
let loseResumeResponse = true;
const approval = () => runAssistantApproval({
  assistantID: "assistant-1",
  attentionID: "attention-1",
  runID: "run-waiting",
  resolve: async (requestID) => { resolveIDs.push(requestID); },
  resume: async (requestID) => {
    resumeIDs.push(requestID);
    if (loseResumeResponse) { loseResumeResponse = false; throw new Error("response lost"); }
  },
});
try { await approval(); } catch { /* retry below */ }
await approval();
ok(resolveIDs[0] === resolveIDs[1], "approval replay reuses the ResolveAttention request id");
ok(resumeIDs[0] === resumeIDs[1], "approval replay reuses the Resume request id after response loss");
await approval();
ok(resolveIDs[2] !== resolveIDs[1] && resumeIDs[2] !== resumeIDs[1], "completed approval rotates both pending receipts");

const refreshedResumeIDs: string[] = [];
let loseApprovedResume = true;
try {
  await runAssistantApproval({
    assistantID: "assistant-1",
    attentionID: "attention-approved-refresh",
    runID: "run-approved-refresh",
    resolve: async () => undefined,
    resume: async (requestID) => {
      refreshedResumeIDs.push(requestID);
      if (loseApprovedResume) { loseApprovedResume = false; throw new Error("response lost"); }
    },
  });
} catch { /* snapshot now observes approved while the run still waits */ }
await runAssistantResume({
  assistantID: "assistant-1",
  attentionID: "attention-approved-refresh",
  runID: "run-approved-refresh",
  resume: async (requestID) => { refreshedResumeIDs.push(requestID); },
});
ok(refreshedResumeIDs[0] === refreshedResumeIDs[1], "approved attention refresh replays the same pending Resume receipt");
await runAssistantResume({
  assistantID: "assistant-1",
  attentionID: "attention-approved-refresh",
  runID: "run-approved-refresh",
  resume: async (requestID) => { refreshedResumeIDs.push(requestID); },
});
ok(refreshedResumeIDs[2] !== refreshedResumeIDs[1], "confirmed continuation clears the recovered Resume receipt");

const outcomeResolveIDs: string[] = [];
let loseOutcomeResponse = true;
const markSucceeded = () => runAssistantOutcome({
  assistantID: "assistant-1",
  attentionID: "attention-outcome-success",
  runID: "run-outcome-success",
  resolution: "mark_succeeded",
  resolve: async (requestID) => {
    outcomeResolveIDs.push(requestID);
    if (loseOutcomeResponse) { loseOutcomeResponse = false; throw new Error("response lost"); }
  },
  resume: async () => { throw new Error("mark_succeeded must not Resume"); },
});
try { await markSucceeded(); } catch { /* replay below */ }
await markSucceeded();
ok(outcomeResolveIDs[0] === outcomeResolveIDs[1], "outcome verification replays the same Resolve receipt after response loss");
await markSucceeded();
ok(outcomeResolveIDs[2] !== outcomeResolveIDs[1], "confirmed outcome resolution clears its stable receipt");

const outcomeResumeIDs: string[] = [];
let loseOutcomeResume = true;
try {
  await runAssistantOutcome({
    assistantID: "assistant-1",
    attentionID: "attention-outcome-retry",
    runID: "run-outcome-retry",
    resolution: "retry_acknowledged",
    resolve: async () => undefined,
    resume: async (requestID) => {
      outcomeResumeIDs.push(requestID);
      if (loseOutcomeResume) { loseOutcomeResume = false; throw new Error("response lost"); }
    },
  });
} catch { /* refreshed item is approved but the run still waits */ }
await runAssistantResume({
  assistantID: "assistant-1",
  attentionID: "attention-outcome-retry",
  runID: "run-outcome-retry",
  resume: async (requestID) => { outcomeResumeIDs.push(requestID); },
  completeKeys: [assistantOutcomeKey("assistant-1", "attention-outcome-retry", "retry_acknowledged")],
});
ok(outcomeResumeIDs[0] === outcomeResumeIDs[1], "verified retry refresh replays the same Resume receipt");

// ── Run summary Markdown structure ─────────────────────────────────────────
// The assistant timeline renders run summaries through the same
// react-markdown + remark-gfm pipeline as <Markdown>. Asserting the rendered
// HTML here (server-side, no jsdom async lazy-load) covers headings, lists,
// and bold without depending on the browser Markdown chunk timing.
const summaryHtml = renderToStaticMarkup(
  createElement(ReactMarkdown, {
    remarkPlugins: [remarkGfm],
    children: "## 结论\n\n- 第一点\n- 第二点\n\n**重点**内容",
  }),
);
ok(summaryHtml.includes("<h2>"), "markdown headings render as h2");
ok(summaryHtml.includes("<ul>") && summaryHtml.includes("<li>"), "markdown lists render as ul/li");
ok(summaryHtml.includes("<strong>"), "markdown bold renders as strong");

const foldedSections = splitAssistantMarkdown([
  "## 结论",
  "",
  "发布条件已满足。",
  "",
  "![构建报告](https://example.com/build.png)",
  "",
  "## 取证证据",
  "",
  "- CI 通过",
  "- 构建产物存在",
  "",
  "## 下一步",
  "",
  "补齐发布说明。",
  "",
  "**说明**",
  "",
  "这里只记录验证口径。",
].join("\n"));
ok(foldedSections.length === 4, "assistant markdown separates visible prose from two folded process sections");
ok(foldedSections[0]?.kind === "content" && foldedSections[0].markdown.includes("![构建报告]"), "visible conclusion keeps Markdown images in the ordinary renderer path");
ok(foldedSections[1]?.kind === "collapsed" && foldedSections[1].title === "取证证据" && foldedSections[1].markdown.includes("CI 通过"), "evidence heading is collapsed with its body");
ok(foldedSections[2]?.kind === "content" && foldedSections[2].markdown.includes("## 下一步"), "next-step conclusion remains visible");
ok(foldedSections[3]?.kind === "collapsed" && foldedSections[3].title === "说明", "standalone bold explanation label is collapsed");
ok(isCollapsedAssistantSection("二、详细说明") && isCollapsedAssistantSection("Evidence"), "numbered Chinese and English process headings use the same fold policy");
const fencedSections = splitAssistantMarkdown("## 结论\n\n```md\n## 取证证据\n代码示例\n```\n\n结论继续");
ok(fencedSections.length === 1 && fencedSections[0]?.kind === "content", "fold parser ignores matching headings inside fenced code");

// ── Inline live reply: correlation, ordering, reconciliation ───────────────
const liveDispatch = {
  id: "dispatch-1", assistant_id: "assistant-1", request_id: "req-1", input: "扫描项目",
  state: "classified" as const, kind: "task" as const, reply: "收到，我来处理。", revision: 2,
  created_at: day.toISOString(), updated_at: day.toISOString(), classified_at: day.toISOString(),
};
const liveSnapshot: AssistantSnapshot = {
  ...snapshot,
  dispatches: [liveDispatch],
  jobs: [{ id: "job-1", assistant_id: "assistant-1", dispatch_id: "dispatch-1", name: "execute", kind: "task", prompt: "扫描项目", state: "queued", attempt: 0, max_attempts: 3, policy: snapshot.assistant.policy, scope: "workspace", revision: 1, created_at: day.toISOString(), updated_at: day.toISOString() }],
};
ok(seedAssistantLiveReply(liveSnapshot)?.phase === "committed" && seedAssistantLiveReply(liveSnapshot)?.reply === "收到，我来处理。", "seed recovers the latest durable dispatch as the authoritative committed reply");
ok(seedAssistantLiveReply({ ...liveSnapshot, dispatches: [{ ...liveDispatch, state: "pending_classification", reply: undefined, classified_at: undefined }] })?.phase === "accepted", "a pending dispatch seeds as 正在理解");

const accepted: AssistantDispatchStreamEvent = { assistantId: "assistant-1", dispatchId: "dispatch-2", requestId: "req-2", sequence: 1, phase: "accepted" };
const streaming: AssistantDispatchStreamEvent = { assistantId: "assistant-1", dispatchId: "dispatch-2", requestId: "req-2", sequence: 2, phase: "streaming", reply: "收到，" };
const streamingMore: AssistantDispatchStreamEvent = { assistantId: "assistant-1", dispatchId: "dispatch-2", requestId: "req-2", sequence: 3, phase: "streaming", reply: "收到，我来处理。" };
const committed: AssistantDispatchStreamEvent = { assistantId: "assistant-1", dispatchId: "dispatch-2", requestId: "req-2", sequence: 4, phase: "committed", reply: "收到，我来处理。", jobCount: 1 };
let live: AssistantLiveReply | null = null;
live = applyAssistantDispatchStream(live, accepted);
ok(live?.phase === "accepted", "accepted stream event opens the inline reply");
live = applyAssistantDispatchStream(live, streaming);
live = applyAssistantDispatchStream(live, streamingMore);
ok(live?.phase === "streaming" && live.reply === "收到，我来处理。", "streaming events accumulate the cumulative reply");
live = applyAssistantDispatchStream(live, { ...streamingMore, sequence: 2 });
ok(live?.sequence === 3, "out-of-order/duplicate sequence is ignored");
live = applyAssistantDispatchStream(live, committed);
ok(live?.phase === "committed" && live.reply === "收到，我来处理。" && live.jobCount === 1, "committed closes with the validated reply and job count");

const otherAccepted: AssistantDispatchStreamEvent = { assistantId: "assistant-1", dispatchId: "dispatch-3", requestId: "req-3", sequence: 5, phase: "accepted" };
live = applyAssistantDispatchStream(live, otherAccepted);
ok(live?.dispatchId === "dispatch-3" && live.phase === "accepted", "a new accepted submission replaces the previous live reply");
live = applyAssistantDispatchStream(live, { ...streaming, dispatchId: "dispatch-2", sequence: 6, phase: "streaming" });
ok(live?.dispatchId === "dispatch-3", "a late stream from an older dispatch does not cross-wire the latest submission");
ok(applyAssistantDispatchStream(live, { assistantId: "assistant-other", dispatchId: "dispatch-x", requestId: "rx", sequence: 7, phase: "streaming" })?.dispatchId === "dispatch-3", "a different assistant's event is ignored");

const provisional: AssistantLiveReply = { assistantId: "assistant-1", dispatchId: "dispatch-1", requestId: "req-1", sequence: 9, phase: "streaming", reply: "临时文本", jobCount: 0 };
ok(reconcileAssistantLiveReply(provisional, liveSnapshot)?.reply === "收到，我来处理。", "a terminal snapshot replaces provisional text with the persisted reply");
ok(reconcileAssistantLiveReply(provisional, { ...liveSnapshot, dispatches: [{ ...liveDispatch, state: "pending_classification", reply: undefined, classified_at: undefined }] })?.reply === "临时文本", "a still-pending snapshot does not flicker the streaming text away");
ok(liveReplyFromDispatch({ ...liveDispatch, state: "classification_failed", error: { code: "classification_unavailable", message: "model down", retryable: true, outcome_known: true, at: day.toISOString() }, reply: undefined }, []).phase === "failed", "failed classification is never presented as committed");

console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed) process.exit(1);
