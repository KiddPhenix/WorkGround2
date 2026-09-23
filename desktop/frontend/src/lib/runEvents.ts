import { isTerminalStatus, useRunStore, type RunEvent, type RunRecord, type RunEventKind, type RunStatus, type RunStepStatus } from "../store/run";
import { classifyTool } from "./activity";
import type { WireEvent, WireTool } from "./types";

type HistoryRunItem = {
  kind: string;
  id?: string;
  /** Backend call identity (preferred over the synthetic `id` for lookups). */
  callId?: string;
  parentId?: string;
  name?: string;
  args?: string;
  status?: string;
  reasoning?: string;
  output?: string;
  error?: string;
  summary?: string;
  subject?: string;
  truncated?: boolean;
  /** Args/output were elided for memory; the body needs an on-demand load. */
  dataArchived?: boolean;
  durationMs?: number;
  fileDiff?: { diff?: string; added?: number; removed?: number };
};

type ToolStep = {
  content: string;
  /** Raw accumulated stream body; `content` is only its trimmed preview. */
  streamed?: string;
  label: string;
  kind: RunEventKind;
  toolName?: string;
  args?: string;
  startedAt: number;
};
type ActiveRun = { runId: string; turnId: string; seq: number };

const activeRuns = new Map<string, ActiveRun>();
/** session → (call key → the runId that opened that call). */
const callOwners = new Map<string, Map<string, string>>();
/** session → in-flight streaming step per call key (label, args, partial body). */
const callSteps = new Map<string, Map<string, ToolStep>>();
let runSeq = 0;

/** Tracks runIds that had a successful complete_step call this turn. */
const completedStepRuns = new Set<string>();

/** Remove terminal color/title control sequences before rendering logs in HTML. */
export function stripRunAnsi(value: string): string {
  return value
    .replace(/\u001B\][^\u0007]*(?:\u0007|\u001B\\)/g, "")
    .replace(/\u001B\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/\u001B[@-_]/g, "");
}

function sessionMap<T>(map: Map<string, Map<string, T>>, sessionId: string): Map<string, T> {
  let inner = map.get(sessionId);
  if (!inner) {
    inner = new Map<string, T>();
    map.set(sessionId, inner);
  }
  return inner;
}

function nextRun(sessionId: string, turnId?: string): ActiveRun {
  const id = `${sessionId}:turn:${Date.now()}:${++runSeq}`;
  useRunStore.getState().collapseSessionRuns(sessionId);
  const run = { runId: id, turnId: turnId ?? id, seq: 0 };
  activeRuns.set(sessionId, run);
  return run;
}

function runRef(sessionId: string, runId: string): ActiveRun {
  const active = activeRuns.get(sessionId);
  if (active?.runId === runId) return active;
  const record = useRunStore.getState().runs[runId];
  return { runId, turnId: record?.turnId ?? runId, seq: 0 };
}

function latestSessionRun(sessionId: string): RunRecord | undefined {
  return Object.values(useRunStore.getState().runs).reduce<RunRecord | undefined>(
    (acc, record) => {
      if (record.sessionId !== sessionId) return acc;
      return !acc || record.startedAt >= acc.startedAt ? record : acc;
    },
    undefined,
  );
}

/**
 * The run a delivery belongs to.
 *
 * A tool call always belongs to the run that opened it, so a late result from an
 * older turn completes its own record instead of landing on a newer turn that
 * happens to be running. With no recorded owner the delivery goes to the live
 * run; with no live run it goes to the newest finished run, whose existing
 * records can still be completed while the store drops anything genuinely new
 * (that turn is over — no phantom run is invented for it).
 */
function currentRun(sessionId: string, callKey?: string): ActiveRun {
  if (callKey) {
    const owner = callOwners.get(sessionId)?.get(callKey);
    if (owner && useRunStore.getState().runs[owner]) return runRef(sessionId, owner);
    if (owner) callOwners.get(sessionId)?.delete(callKey);
  }
  const current = activeRuns.get(sessionId);
  if (current && useRunStore.getState().runs[current.runId]) return current;
  const latest = latestSessionRun(sessionId);
  if (latest && isTerminalStatus(latest.status)) return runRef(sessionId, latest.runId);
  return nextRun(sessionId);
}

/** Whether a run for this session + turn identity already exists in the store. */
function hasRunForTurn(sessionId: string, turnId: string): boolean {
  const runs = useRunStore.getState().runs;
  for (const run of Object.values(runs)) {
    if (run.sessionId === sessionId && run.turnId === turnId) return true;
  }
  return false;
}

function append(sessionId: string, run: ActiveRun, event: Omit<RunEvent, "eventId">, stableId?: string) {
  useRunStore.getState().mergeRunEvent(
    run.runId,
    sessionId,
    run.turnId,
    { ...event, eventId: stableId ?? `${run.runId}:event:${++run.seq}` },
  );
}

function toolLabel(tool?: WireTool): string {
  if (!tool) return "执行工具";
  const subject = tool.args?.trim().replace(/\s+/g, " ");
  return subject ? `${tool.name} ${subject.slice(0, 48)}` : tool.name;
}

function toolStepKey(tool?: WireTool): string {
  const id = tool?.id || tool?.name || "tool";
  return tool?.parentId ? `${tool.parentId}:child:${id}` : id;
}

function toolStepId(run: ActiveRun, tool?: WireTool): string {
  return `${run.runId}:tool:${toolStepKey(tool)}`;
}

/** Central visual-semantic mapping for RunBlock activity scenes. */
export function classifyRunEventKind(name: string, args = ""): RunEventKind {
  const normalized = name.trim().toLowerCase();
  if (normalized.startsWith("browser_") || normalized.includes("__browser__")) return "browser";
  switch (classifyTool(normalized, args)) {
    case "searching": return "search";
    case "reading": return "read";
    case "editing": return "edit";
    case "testing": return "test";
    case "command": return "command";
    default: return "generic";
  }
}

function toolMeta(tool?: WireTool, previous?: ToolStep): Pick<ToolStep, "kind" | "toolName" | "args"> {
  const toolName = tool?.name || previous?.toolName;
  const args = tool?.args ?? previous?.args;
  return {
    kind: toolName ? classifyRunEventKind(toolName, args) : (previous?.kind ?? "generic"),
    ...(toolName ? { toolName } : {}),
    ...(args ? { args } : {}),
  };
}

function isCompleteStepSuccess(tool?: Pick<WireTool, "name" | "err">): boolean {
  if (tool?.name !== "complete_step") return false;
  return !tool.err || /newly completed|already completed/i.test(tool.err);
}

/**
 * Stable identity + preview data carried on every delivery of a call. `callId`
 * is the only safe key for the on-demand full-args/output lookup: it comes from
 * the backend, never from the record's position in the list.
 */
function toolIdentityFields(tool?: WireTool): Partial<RunEvent> {
  if (!tool) return {};
  const fields: Partial<RunEvent> = {};
  if (tool.id) fields.callId = tool.id;
  if (tool.parentId) fields.parentId = tool.parentId;
  if (tool.diff) {
    fields.diff = tool.diff;
    if (tool.added) fields.added = tool.added;
    if (tool.removed) fields.removed = tool.removed;
  }
  return fields;
}

/**
 * Body fields for a settled call. Output and error stay separate so a failure
 * shows the error *and* whatever the tool already produced, instead of one
 * replacing the other in the compact `content` string.
 */
function toolResultFields(tool?: WireTool): Partial<RunEvent> {
  if (!tool) return {};
  const output = stripRunAnsi(tool.output ?? "");
  const error = stripRunAnsi(tool.err ?? "");
  return {
    ...toolIdentityFields(tool),
    ...(output ? { output } : {}),
    ...(error ? { error } : {}),
    ...(tool.truncated ? { truncated: true } : {}),
    ...(tool.durationMs ? { durationMs: tool.durationMs } : {}),
  };
}

function setStatus(run: ActiveRun, status: RunStatus, errorMessage?: string) {
  const store = useRunStore.getState();
  store.setRunStatus(run.runId, status, errorMessage ? { errorMessage } : undefined);
  if (status === "completed" || status === "failed" || status === "cancelled") {
    store.setRunExpanded(run.runId, false);
  }
}

/**
 * Projects the controller wire stream into the compact Workbench run model.
 *
 * `cancelledHint` reports whether the session had a cancel in flight when this
 * event arrived. TurnDone carries no error for a user cancellation, so without
 * the hint a stopped turn would be projected as a success.
 */
export function applyRunWireEvent(
  sessionId: string,
  event: WireEvent,
  turnId?: string,
  options?: { cancelled?: boolean },
): void {
  if (!sessionId) return;
  const cancelledHint = options?.cancelled;

  if (event.kind === "turn_started") {
    // A turn_started for the same session + turnId can be re-delivered on
    // replay, reconnect, or duplicate delivery. It must be idempotent: reuse
    // the existing run (never spawn a second live run) and never repeat the
    // start event. An existing record also keeps a terminal run from being
    // resurrected by a stale turn_started. Without a turnId there is no
    // reliable identity to dedupe on, so keep the legacy behavior of opening
    // a new run for each turn_started.
    if (turnId && hasRunForTurn(sessionId, turnId)) return;
    const run = nextRun(sessionId, turnId);
    append(sessionId, run, { kind: "generic", content: "开始执行", stepLabel: "开始" }, `${run.runId}:start`);
    return;
  }

  if (
    event.kind !== "tool_dispatch" &&
    event.kind !== "tool_progress" &&
    event.kind !== "tool_result" &&
    event.kind !== "approval_request" &&
    event.kind !== "ask_request" &&
    event.kind !== "retrying" &&
    event.kind !== "turn_done"
  ) return;

  // A tool call belongs to the run that opened it; turn_done belongs to the run
  // that is still live (or, on a re-delivery, to the run that already has it).
  const callKey = event.kind === "turn_done" ? undefined : toolStepKey(event.tool);
  const run = currentRun(sessionId, callKey);
  const steps = sessionMap(callSteps, sessionId);
  switch (event.kind) {
    case "tool_dispatch": {
      if (!event.tool || event.tool.partial) return;
      const label = toolLabel(event.tool);
      const step = { content: label, label, ...toolMeta(event.tool), startedAt: Date.now() };
      steps.set(callKey!, step);
      sessionMap(callOwners, sessionId).set(callKey!, run.runId);
      append(sessionId, run, {
        kind: step.kind,
        toolName: step.toolName,
        args: step.args,
        content: step.content,
        stepLabel: label,
        status: "running",
        phase: "dispatch",
        startedAt: step.startedAt,
        ...toolIdentityFields(event.tool),
      }, toolStepId(run, event.tool));
      setStatus(run, "running");
      return;
    }
    case "tool_progress": {
      // The raw chunk is the body; the trimmed copy is only the compact preview.
      const raw = stripRunAnsi(event.tool?.output ?? "");
      const content = raw.trim();
      if (!raw) return;
      const previous = steps.get(callKey!);
      const label = previous?.label || event.tool?.name || "执行工具";
      const combined = previous?.content && previous.content !== label
        ? `${previous.content}\n${content}`
        : content;
      const streamed = `${previous?.streamed ?? ""}${raw}`;
      const step = {
        content: combined,
        streamed,
        label,
        ...toolMeta(event.tool, previous),
        startedAt: previous?.startedAt ?? Date.now(),
      };
      steps.set(callKey!, step);
      sessionMap(callOwners, sessionId).set(callKey!, run.runId);
      append(sessionId, run, {
        kind: step.kind,
        toolName: step.toolName,
        args: step.args,
        content: step.content,
        // Keep the streamed body too, so a failure that reports no output of its
        // own still shows what the tool had already produced.
        output: streamed,
        stepLabel: label,
        status: "running",
        phase: "progress",
        ...toolIdentityFields(event.tool),
      }, toolStepId(run, event.tool));
      setStatus(run, "running");
      return;
    }
    case "tool_result": {
      if (!event.tool) return;
      sessionMap(callOwners, sessionId).set(callKey!, run.runId);
      const name = event.tool.name;
      const completedStep = isCompleteStepSuccess(event.tool);
      const stopped = Boolean(event.tool.stopped);
      const failed = Boolean(event.tool.err) && !completedStep && !stopped;
      const progress = steps.get(callKey!);
      const reported = stripRunAnsi(event.tool.output ?? "");
      const error = stripRunAnsi(event.tool.err ?? "");
      // A settled call's body is what it reported, or else what it streamed
      // before failing — never the error summary, which has its own field.
      const body = reported || progress?.streamed || "";
      // The compact scene still leads with the error: that is what a glance needs.
      const content = completedStep
        ? reported || "步骤确认完成"
        : stopped
          ? reported || progress?.content || `${name} 已停止`
          : error || reported || progress?.content || toolLabel(event.tool);
      // Track successful complete_step calls — they are explicit terminal signals
      if (completedStep) {
        completedStepRuns.add(run.runId);
      }
      // A stopped call is neither a success nor a failure: it is unconfirmed.
      const stepStatus: RunStepStatus = completedStep ? "completed" : stopped ? "stopped" : failed ? "failed" : "completed";
      const stepLabel = completedStep
        ? "步骤确认完成"
        : stopped
          ? `${name} 已停止`
          : `${name} ${failed ? "失败" : "完成"}`;
      append(
        sessionId,
        run,
        {
          ...toolMeta(event.tool, progress),
          content,
          stepLabel,
          status: stepStatus,
          phase: "result",
          ...(progress?.startedAt ? { startedAt: progress.startedAt } : {}),
          ...(body ? { output: body } : {}),
          ...toolResultFields(event.tool),
        },
        toolStepId(run, event.tool),
      );
      steps.delete(callKey!);
      return;
    }
    case "approval_request":
    case "ask_request":
      setStatus(run, "waiting_user");
      return;
    case "retrying":
      setStatus(run, "reconnecting");
      return;
    case "turn_done": {
      // If a successful complete_step was called this turn, treat as completed
      // regardless of turn_done error — complete_step is the explicit terminal signal.
      const hadCompleteStep = completedStepRuns.has(run.runId);
      completedStepRuns.delete(run.runId);
      const cancelled = Boolean(cancelledHint);
      const finalErr = hadCompleteStep ? undefined : event.err;
      // TurnDone carries no error for a user cancellation, so a cancelled turn
      // must not be projected as a success. The error text (if any) is still
      // kept on the record so nothing is hidden.
      const failed = Boolean(finalErr) && !cancelled;
      const stepStatus: RunStepStatus = cancelled ? "stopped" : failed ? "failed" : "completed";
      const stepLabel = cancelled ? "已取消" : failed ? "失败" : "完成";
      const content = cancelled
        ? "运行已取消"
        : hadCompleteStep
          ? "步骤确认完成"
          : (finalErr || "运行完成");
      append(
        sessionId,
        run,
        {
          kind: "generic",
          content,
          stepLabel,
          status: stepStatus,
          phase: "note",
          ...(finalErr ? { error: stripRunAnsi(finalErr) } : {}),
        },
        `${run.runId}:done`,
      );
      setStatus(run, cancelled ? "cancelled" : failed ? "failed" : "completed", finalErr);
      activeRuns.delete(sessionId);
      return;
    }
    default:
      return;
  }
}

export function resetRunProjection(): void {
  activeRuns.clear();
  callOwners.clear();
  callSteps.clear();
  completedStepRuns.clear();
  runSeq = 0;
}

/** Rebuilds collapsed run tabs from hydrated transcript history. */
export function projectRunHistory(sessionId: string, items: HistoryRunItem[]): void {
  if (!sessionId) return;
  const store = useRunStore.getState();
  for (const run of Object.values(store.runs)) {
    if (run.sessionId === sessionId) store.clearRun(run.runId);
  }

  let turn = 0;
  let events: RunEvent[] = [];
  let failed = false;
  let completedStep = false;
  const flush = () => {
    if (events.length === 0) return;
    const runId = `${sessionId}:history:${turn}`;
    for (const event of events) store.mergeRunEvent(runId, sessionId, `turn:${turn}`, event);
    const runFailed = failed && !completedStep;
    store.setRunStatus(runId, runFailed ? "failed" : "completed", runFailed ? { errorMessage: "历史工具执行失败" } : undefined);
    store.setRunExpanded(runId, false);
    events = [];
    failed = false;
    completedStep = false;
  };

  for (const item of items) {
    if (item.kind === "user") {
      flush();
      turn++;
      continue;
    }
    if (item.kind === "assistant" && item.reasoning?.trim()) {
      events.push({
        eventId: `${sessionId}:history:${turn}:reasoning`,
        kind: "generic",
        content: item.reasoning.trim(),
        stepLabel: "思考完成",
        status: "completed",
        phase: "note",
      });
      continue;
    }
    if (item.kind === "tool") {
      const name = item.name || "工具";
      const subject = item.summary || item.subject || item.args || name;
      const callId = item.callId || item.id;
      const stepCompleted = name === "complete_step" && (!item.error || /newly completed|already completed/i.test(item.error));
      // History elides args/output for memory. That is "unavailable until
      // loaded", never "empty": the record keeps `archived` so the detail panel
      // can say so honestly and fetch the body on demand.
      const archived = Boolean(item.dataArchived);
      const stopped = item.status === "stopped";
      const errored = item.status === "error" && !stepCompleted && !stopped;
      const output = archived ? "" : stripRunAnsi(item.output ?? "");
      const error = stripRunAnsi(item.error ?? "");
      const stepStatus: RunStepStatus = stepCompleted ? "completed" : stopped ? "stopped" : errored ? "failed" : "completed";
      events.push({
        eventId: `${sessionId}:history:${turn}:tool:${item.parentId ? `${item.parentId}:child:` : ""}${callId || events.length}`,
        kind: classifyRunEventKind(name, item.args),
        toolName: name,
        ...(item.args ? { args: item.args } : {}),
        content: stepCompleted
          ? (output || "步骤确认完成")
          : errored
            ? (error || output || subject)
            : (output || subject),
        stepLabel: stepCompleted ? "步骤确认完成" : stopped ? `${name} 已停止` : `${name} ${errored ? "失败" : "完成"}`,
        status: stepStatus,
        phase: "result",
        ...(callId ? { callId } : {}),
        ...(item.parentId ? { parentId: item.parentId } : {}),
        ...(output ? { output } : {}),
        ...(error ? { error } : {}),
        ...(item.fileDiff?.diff
          ? {
              diff: item.fileDiff.diff,
              ...(item.fileDiff.added ? { added: item.fileDiff.added } : {}),
              ...(item.fileDiff.removed ? { removed: item.fileDiff.removed } : {}),
            }
          : {}),
        ...(item.truncated ? { truncated: true } : {}),
        ...(item.durationMs ? { durationMs: item.durationMs } : {}),
        ...(archived ? { archived: true } : {}),
      });
      completedStep ||= stepCompleted;
      failed ||= errored;
    }
  }
  flush();
}
