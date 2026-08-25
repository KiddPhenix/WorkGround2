import { assistantCopy } from "../custom/features/assistant/assistant.copy";
import { attentionInboxAction, attentionNeedsRebind, attentionRejectResolution, attentionResolution, nextRoutineDate, responsibilityLabel, responsibilityStatusLabel, runHistoryAction, scheduleLabel, timelineEntries } from "../custom/features/assistant/assistant.model";
import { assistantIntentKey, assistantMutationKey, assistantOutcomeKey, completeAssistantRequest, pendingAssistantMutation, pendingAssistantRequest, runAssistantApproval, runAssistantCASMutation, runAssistantOutcome, runAssistantResume } from "../custom/features/assistant/assistant.requests";
import type { AssistantAttentionItem, AssistantRun, AssistantSnapshot } from "../custom/features/assistant/assistant.types";
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
const attentionBase: AssistantAttentionItem = { id: "attention-state", assistant_id: "assistant-1", run_id: "run-waiting", request_id: "attention-request", action: "publish_release", summary: "确认发布", state: "open", revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" };
const waitingRun: AssistantRun = { id: "run-waiting", assistant_id: "assistant-1", routine_id: "routine-1", request_id: "run-request", trigger: "manual", state: "waiting_attention", attempt: 1, max_attempts: 3, revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" };
ok(attentionInboxAction({ ...attentionBase, state: "approved" }, waitingRun) === "continue", "approved attention remains actionable while its run still waits");
ok(attentionInboxAction({ ...attentionBase, state: "approved" }, { ...waitingRun, state: "running" }) === "none", "approved attention hides after its run continues");
ok(attentionInboxAction({ ...attentionBase, action: "answer_required" }, waitingRun) === "answer", "answer-required attention exposes an answer flow");
ok(attentionInboxAction({ ...attentionBase, action: "verify_run_outcome" }, waitingRun) === "verify", "unknown run outcome exposes the three-way verification flow");
ok(attentionInboxAction({ ...attentionBase, action: "verify_run_outcome", state: "approved", resolution: "retry_acknowledged" }, waitingRun) === "continue", "acknowledged retry remains resumable after refresh");
ok(attentionInboxAction({ ...attentionBase, action: "verify_run_outcome", state: "approved", resolution: "mark_succeeded" }, waitingRun) === "none", "marked-success outcome never resumes a waiting run");
ok(attentionInboxAction({ ...attentionBase, action: "verify_run_outcome", state: "approved", resolution: "mark_failed" }, waitingRun) === "none", "marked-failed outcome never resumes a waiting run");
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
  updated_at: day.toISOString(),
};
const entries = timelineEntries(snapshot, day, copy);
ok(entries.some((entry) => entry.kind === "run"), "timeline projects factual run state");
ok(entries.some((entry) => entry.kind === "memory"), "timeline projects explicit memory");
ok(entries.some((entry) => entry.kind === "next"), "timeline projects the next routine");

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

console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed) process.exit(1);
