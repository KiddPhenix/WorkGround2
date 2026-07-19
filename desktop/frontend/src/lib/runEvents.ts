import { useRunStore, type RunEvent, type RunStatus } from "../store/run";
import type { WireEvent, WireTool } from "./types";

type HistoryRunItem = {
  kind: string;
  id?: string;
  name?: string;
  args?: string;
  status?: string;
  reasoning?: string;
  output?: string;
  error?: string;
  summary?: string;
};

type ToolStep = { content: string; label: string };
type ActiveRun = { runId: string; turnId: string; seq: number; toolSteps: Map<string, ToolStep> };

const activeRuns = new Map<string, ActiveRun>();
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

function nextRun(sessionId: string, turnId?: string): ActiveRun {
  const id = `${sessionId}:turn:${Date.now()}:${++runSeq}`;
  useRunStore.getState().collapseSessionRuns(sessionId);
  const run = { runId: id, turnId: turnId ?? id, seq: 0, toolSteps: new Map<string, ToolStep>() };
  activeRuns.set(sessionId, run);
  return run;
}

function currentRun(sessionId: string): ActiveRun {
  const current = activeRuns.get(sessionId);
  if (current) return current;
  return nextRun(sessionId);
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
  return tool?.id || tool?.name || "tool";
}

function toolStepId(run: ActiveRun, tool?: WireTool): string {
  return `${run.runId}:tool:${toolStepKey(tool)}`;
}

function isCompleteStepSuccess(tool?: Pick<WireTool, "name" | "err">): boolean {
  if (tool?.name !== "complete_step") return false;
  return !tool.err || /newly completed|already completed/i.test(tool.err);
}

function setStatus(run: ActiveRun, status: RunStatus, errorMessage?: string) {
  const store = useRunStore.getState();
  store.setRunStatus(run.runId, status, errorMessage ? { errorMessage } : undefined);
  if (status === "completed" || status === "failed" || status === "cancelled") {
    store.setRunExpanded(run.runId, false);
  }
}

/** Projects the controller wire stream into the compact Workbench run model. */
export function applyRunWireEvent(sessionId: string, event: WireEvent, turnId?: string): void {
  if (!sessionId) return;

  if (event.kind === "turn_started") {
    const run = nextRun(sessionId, turnId);
    append(sessionId, run, { content: "开始执行", stepLabel: "开始" }, `${run.runId}:start`);
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

  const run = currentRun(sessionId);
  switch (event.kind) {
    case "tool_dispatch": {
      if (!event.tool || event.tool.partial) return;
      const label = toolLabel(event.tool);
      run.toolSteps.set(toolStepKey(event.tool), { content: label, label });
      append(sessionId, run, { content: label, stepLabel: label, status: "running" }, toolStepId(run, event.tool));
      setStatus(run, "running");
      return;
    }
    case "tool_progress": {
      const content = stripRunAnsi(event.tool?.output ?? "").trim();
      if (!content) return;
      const key = toolStepKey(event.tool);
      const previous = run.toolSteps.get(key);
      const label = previous?.label || event.tool?.name || "执行工具";
      const combined = previous?.content && previous.content !== label
        ? `${previous.content}\n${content}`
        : content;
      run.toolSteps.set(key, { content: combined, label });
      append(sessionId, run, { content: combined, stepLabel: label, status: "running" }, toolStepId(run, event.tool));
      setStatus(run, "running");
      return;
    }
    case "tool_result": {
      if (!event.tool) return;
      const name = event.tool.name;
      const completedStep = isCompleteStepSuccess(event.tool);
      const failed = Boolean(event.tool.err) && !completedStep;
      const key = toolStepKey(event.tool);
      const progress = run.toolSteps.get(key);
      const output = stripRunAnsi(event.tool.output ?? "");
      const error = stripRunAnsi(event.tool.err ?? "");
      const content = completedStep
        ? output || "步骤确认完成"
        : error || output || progress?.content || toolLabel(event.tool);
      // Track successful complete_step calls — they are explicit terminal signals
      if (completedStep) {
        completedStepRuns.add(run.runId);
      }
      // Don't show complete_step completion sentinel as an error
      const stepLabel = completedStep
        ? "步骤确认完成"
        : `${name} ${failed ? "失败" : "完成"}`;
      append(
        sessionId,
        run,
        { content, stepLabel, status: failed ? "failed" : "completed" },
        toolStepId(run, event.tool),
      );
      run.toolSteps.delete(key);
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
      const finalErr = hadCompleteStep ? undefined : event.err;
      append(
        sessionId,
        run,
        { content: hadCompleteStep ? "步骤确认完成" : (event.err || "运行完成"), stepLabel: finalErr ? "失败" : "完成", status: finalErr ? "failed" : "completed" },
        `${run.runId}:done`,
      );
      setStatus(run, finalErr ? "failed" : "completed", finalErr);
      activeRuns.delete(sessionId);
      return;
    }
    default:
      return;
  }
}

export function resetRunProjection(): void {
  activeRuns.clear();
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
      events.push({ eventId: `${sessionId}:history:${turn}:reasoning`, content: item.reasoning.trim(), stepLabel: "思考完成", status: "completed" });
      continue;
    }
    if (item.kind === "tool") {
      const name = item.name || "工具";
      const subject = item.summary || item.args || name;
      const stepCompleted = name === "complete_step" && (!item.error || /newly completed|already completed/i.test(item.error));
      events.push({
        eventId: `${sessionId}:history:${turn}:tool:${item.id || events.length}`,
        content: stepCompleted ? stripRunAnsi(item.output || "步骤确认完成") : stripRunAnsi(item.error || item.output || subject),
        stepLabel: stepCompleted ? "步骤确认完成" : `${name} ${item.status === "error" ? "失败" : "完成"}`,
        status: item.status === "error" && !stepCompleted ? "failed" : "completed",
      });
      completedStep ||= stepCompleted;
      failed ||= item.status === "error" && !stepCompleted;
    }
  }
  flush();
}
