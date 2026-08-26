import type { AssistantCopy } from "./assistant.copy";
import type {
  AssistantAttentionItem,
  AssistantContextPack,
  AssistantDispatch,
  AssistantDispatchKind,
  AssistantIdeaProposal,
  AssistantIdeaState,
  AssistantJobState,
  AssistantMemoryItem,
  AssistantResponsibility,
  AssistantRoutine,
  AssistantRun,
  AssistantRunnerJob,
  AssistantSnapshot,
} from "./assistant.types";

export interface AssistantTimelineEntry {
  id: string;
  at: Date;
  kind: "run" | "memory" | "next" | "dispatch" | "idea";
  title: string;
  detail?: string;
  // prompt carries the original direct input text for a direct-input run; it is
  // rendered as plain text (not Markdown) with preserved line breaks.
  prompt?: string;
  run?: AssistantRun;
  memory?: AssistantMemoryItem;
  routine?: AssistantRoutine;
  dispatch?: AssistantDispatch;
  jobs?: AssistantRunnerJob[];
  pack?: AssistantContextPack;
  idea?: AssistantIdeaProposal;
}

function validDate(value?: string): Date | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

export function formatAssistantDate(date: Date, locale: string): string {
  if (locale === "en") return new Intl.DateTimeFormat("en", { month: "long", day: "numeric" }).format(date);
  const parts = new Intl.DateTimeFormat(locale === "zh-TW" ? "zh-TW" : "zh-CN", { month: "long", day: "numeric" }).format(date);
  return parts.replace(/\s+/g, "");
}

export function formatTimelineTime(date: Date, locale: string): string {
  return new Intl.DateTimeFormat(locale === "en" ? "en-GB" : locale, { hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
}

export function runStateLabel(state: AssistantRun["state"], locale: string): string {
  const labels: Record<AssistantRun["state"], [string, string]> = {
    queued: ["Queued", "已排队"],
    running: ["Running", "正在工作"],
    succeeded: ["Completed", "已完成"],
    waiting_approval: ["Approval needed", "等待批准"],
    retry_wait: ["Retry scheduled", "等待重试"],
    waiting_attention: ["Attention needed", "需要处理"],
    failed: ["Failed", "失败"],
    cancelled: ["Cancelled", "已取消"],
  };
  return labels[state][locale === "en" ? 0 : 1];
}

export function dispatchKindLabel(kind: AssistantDispatchKind | undefined, locale: string): string {
  const labels: Record<AssistantDispatchKind, [string, string]> = {
    task: ["Task", "任务"],
    question: ["Question", "问题"],
    feedback: ["Feedback", "反馈"],
    improvement: ["Improvement", "改进"],
    correction: ["Correction", "更正"],
    control: ["Control", "控制"],
  };
  if (!kind) return locale === "en" ? "Unclassified" : "未分类";
  return labels[kind][locale === "en" ? 0 : 1];
}

export function jobStateLabel(state: AssistantJobState, locale: string): string {
  const labels: Record<AssistantJobState, [string, string]> = {
    queued: ["Queued", "已排队"],
    running: ["Running", "进行中"],
    succeeded: ["Succeeded", "已完成"],
    retry_wait: ["Retry scheduled", "等待重试"],
    waiting_attention: ["Attention needed", "需要处理"],
    failed: ["Failed", "失败"],
    cancelled: ["Cancelled", "已取消"],
  };
  return labels[state][locale === "en" ? 0 : 1];
}

export function ideaStateLabel(state: AssistantIdeaState, locale: string): string {
  const labels: Record<AssistantIdeaState, [string, string]> = {
    pending: ["Pending", "待确认"],
    accepted: ["Accepted", "已接受"],
    rejected: ["Rejected", "已拒绝"],
    superseded: ["Superseded", "已淘汰"],
  };
  return labels[state][locale === "en" ? 0 : 1];
}

export function dispatchStateLabel(state: AssistantDispatch["state"], locale: string): string {
  const labels: Record<AssistantDispatch["state"], [string, string]> = {
    pending_classification: ["Classifying", "分类中"],
    classified: ["Classified", "已分类"],
    classification_failed: ["Classification failed", "分类失败"],
    reflected: ["Reflected", "已反思"],
    reflection_failed: ["Reflection failed", "反思失败"],
  };
  return labels[state][locale === "en" ? 0 : 1];
}

const runTitleMaxChars = 40;

function compactRunTitle(value?: string): string {
  const normalized = value?.replace(/\s+/g, " ").trim() ?? "";
  const chars = Array.from(normalized);
  return chars.length > runTitleMaxChars ? `${chars.slice(0, runTitleMaxChars).join("")}…` : normalized;
}

// The timeline heading identifies what the run is doing. State belongs to the
// adjacent badge; the full prompt/result remain independently traceable below.
export function runContentTitle(run: AssistantRun, routine: AssistantRoutine | undefined, locale: string): string {
  if (!run.routine_id) {
    const directInput = compactRunTitle(run.prompt);
    if (directInput) return directInput;
  }
  const routineTitle = compactRunTitle(routine?.title);
  if (routineTitle) return routineTitle;
  const frozenTask = compactRunTitle(run.prompt);
  if (frozenTask) return frozenTask;
  return locale === "en" ? "Continue assistant mission" : "继续推进助手使命";
}

export type AssistantRunAction = "rerun" | "cancel" | "attention" | "none";

export function responsibilityStatusLabel(status: AssistantResponsibility["status"], locale: string): string {
  const labels: Record<AssistantResponsibility["status"], [string, string]> = {
    blocked: ["Blocked", "被阻塞"],
    ready: ["Ready", "就绪"],
    active: ["Active", "进行中"],
    done: ["Done", "已完成"],
    failed: ["Failed", "失败"],
  };
  return labels[status][locale === "en" ? 0 : 1];
}

export function responsibilityLabel(responsibility: AssistantResponsibility): string {
  return responsibility.alias?.trim() || responsibility.id;
}

export function runHistoryAction(state: AssistantRun["state"]): AssistantRunAction {
  if (state === "failed" || state === "cancelled") return "rerun";
  if (state === "queued" || state === "running" || state === "retry_wait") return "cancel";
  if (state === "waiting_approval" || state === "waiting_attention") return "attention";
  return "none";
}

export function attentionNeedsRebind(action: string): boolean {
  return ["rebind_workspace", "cancel_recreate"].includes(action.trim().toLowerCase());
}

export function attentionNeedsAnswer(action: string): boolean {
  return action.trim().toLowerCase() === "answer_required";
}

export function attentionVerifiesOutcome(action: string): boolean {
  return action.trim().toLowerCase() === "verify_run_outcome";
}

export function attentionResolution(action: string, answer: string, fallback: string): string {
  return attentionNeedsAnswer(action) ? answer.trim() : fallback;
}

export function attentionRejectResolution(action: string, fallback: string, answerRejected: string): string {
  return attentionNeedsAnswer(action) ? answerRejected : fallback;
}

export type AssistantInboxAction = "approval" | "answer" | "continue" | "rebind" | "verify" | "none";

export function attentionInboxAction(item: AssistantAttentionItem, run?: AssistantRun): AssistantInboxAction {
  // A run can request several approvals in sequence. Resolved items stay in
  // the snapshot for audit, so only the item bound to the run's current
  // continuation may remain actionable.
  if (run && (item.resume_token || run.resume_token) && item.resume_token !== run.resume_token) return "none";
  if (item.state === "open") {
    if (attentionNeedsRebind(item.action)) return "rebind";
    if (attentionVerifiesOutcome(item.action)) return "verify";
    return attentionNeedsAnswer(item.action) ? "answer" : "approval";
  }
  const waiting = run?.state === "waiting_approval" || run?.state === "waiting_attention";
  if (!item.resume_token || !run?.resume_token) return "none";
  if (item.state === "approved" && attentionVerifiesOutcome(item.action)) {
    return item.resolution === "retry_acknowledged" && waiting ? "continue" : "none";
  }
  if (item.state === "approved" && waiting && !attentionNeedsRebind(item.action)) return "continue";
  return "none";
}

export function scheduleLabel(routine: AssistantRoutine, copy: AssistantCopy): string {
  const { schedule } = routine;
  switch (schedule.kind) {
    case "manual": return copy.manual;
    case "interval": {
      const hours = Math.max(1, Math.round((schedule.interval_seconds ?? 3600) / 3600));
      return `${copy.interval} · ${hours} ${copy.hour}`;
    }
    case "daily": return `${copy.daily} ${schedule.at || "09:00"}`;
    case "weekly": return `${copy.weekly} ${schedule.at || "09:00"}`;
    case "biweekly": return `${copy.weekly} · 2 ${schedule.at || "09:00"}`;
    case "monthly": return `${schedule.day || 1} · ${schedule.at || "09:00"}`;
    case "yearly": return `${schedule.month || 1}/${schedule.day || 1} · ${schedule.at || "09:00"}`;
  }
}

export function nextRoutineDate(routine: AssistantRoutine, now: Date): Date | null {
  const schedule = routine.schedule;
  if (!routine.enabled || schedule.kind === "manual") return null;
  if (schedule.kind === "interval") {
    const anchor = validDate(routine.last_scheduled_for) ?? now;
    let next = new Date(anchor.getTime() + Math.max(60, schedule.interval_seconds ?? 3600) * 1000);
    while (next <= now) next = new Date(next.getTime() + Math.max(60, schedule.interval_seconds ?? 3600) * 1000);
    return next;
  }
  const [hour, minute] = (schedule.at || "09:00").split(":").map(Number);
  const next = new Date(now);
  next.setHours(hour || 0, minute || 0, 0, 0);
  if (next <= now) next.setDate(next.getDate() + (schedule.kind === "weekly" ? 7 : schedule.kind === "biweekly" ? 14 : 1));
  return next;
}

export function timelineEntries(snapshot: AssistantSnapshot, day: Date, locale: string, copy: AssistantCopy): AssistantTimelineEntry[] {
  const sameDay = (date: Date) => date.getFullYear() === day.getFullYear() && date.getMonth() === day.getMonth() && date.getDate() === day.getDate();
  const entries: AssistantTimelineEntry[] = [];
  const routines = new Map(snapshot.routines.map((routine) => [routine.id, routine]));
  for (const run of snapshot.runs) {
    const at = validDate(run.started_at) ?? validDate(run.scheduled_for) ?? validDate(run.created_at);
    if (!at || !sameDay(at)) continue;
    const prompt = !run.routine_id && run.prompt?.trim() ? run.prompt : undefined;
    const routine = run.routine_id ? routines.get(run.routine_id) : undefined;
    entries.push({
      id: run.id,
      at,
      kind: "run",
      title: runContentTitle(run, routine, locale),
      detail: run.summary,
      prompt,
      run,
      routine,
    });
  }
  const dispatchJobs = new Map<string, AssistantRunnerJob[]>();
  for (const job of snapshot.jobs ?? []) {
    const list = dispatchJobs.get(job.dispatch_id) ?? [];
    list.push(job);
    dispatchJobs.set(job.dispatch_id, list);
  }
  const dispatchPacks = new Map<string, AssistantContextPack>();
  for (const pack of snapshot.context_packs ?? []) dispatchPacks.set(pack.dispatch_id, pack);
  for (const dispatch of snapshot.dispatches ?? []) {
    const at = validDate(dispatch.classified_at) ?? validDate(dispatch.created_at);
    if (!at || !sameDay(at)) continue;
    const jobs = dispatchJobs.get(dispatch.id) ?? [];
    const pack = dispatchPacks.get(dispatch.id);
    entries.push({
      id: `dispatch-${dispatch.id}`,
      at,
      kind: "dispatch",
      title: dispatch.kind ? dispatchKindLabel(dispatch.kind, locale) : dispatchStateLabel(dispatch.state, locale),
      detail: dispatch.reply,
      prompt: dispatch.input,
      dispatch,
      jobs,
      pack,
    });
  }
  for (const idea of snapshot.ideas ?? []) {
    if (idea.state !== "pending") continue;
    const at = validDate(idea.created_at);
    if (!at || !sameDay(at)) continue;
    entries.push({ id: `idea-${idea.id}`, at, kind: "idea", title: idea.summary, detail: idea.rationale, idea });
  }
  for (const memory of snapshot.memory.items) {
    const at = validDate(memory.updated_at) ?? validDate(memory.created_at);
    if (!at || !sameDay(at)) continue;
    if (entries.some((entry) => entry.run?.id === memory.source_run)) {
      entries.push({ id: `memory-${memory.id}`, at, kind: "memory", title: `${copy.learned}：${memory.body}`, detail: memory.evidence, memory });
    }
  }
  const next = snapshot.routines
    .map((routine) => ({ routine, at: nextRoutineDate(routine, day) }))
    .filter((item): item is { routine: AssistantRoutine; at: Date } => item.at !== null)
    .sort((a, b) => a.at.getTime() - b.at.getTime())[0];
  if (next) entries.push({ id: `next-${next.routine.id}`, at: next.at, kind: "next", title: `${copy.next}，${next.routine.title}`, detail: scheduleLabel(next.routine, copy), routine: next.routine });
  const kindOrder: Record<AssistantTimelineEntry["kind"], number> = { run: 0, dispatch: 1, idea: 2, memory: 3, next: 4 };
  return entries.sort((a, b) =>
    b.at.getTime() - a.at.getTime()
    || kindOrder[a.kind] - kindOrder[b.kind]
    || a.id.localeCompare(b.id),
  );
}
