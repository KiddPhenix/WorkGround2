import type { AssistantCopy } from "./assistant.copy";
import type { AssistantAttentionItem, AssistantMemoryItem, AssistantRoutine, AssistantRun, AssistantSnapshot } from "./assistant.types";

export interface AssistantTimelineEntry {
  id: string;
  at: Date;
  kind: "run" | "memory" | "next";
  title: string;
  detail?: string;
  run?: AssistantRun;
  memory?: AssistantMemoryItem;
  routine?: AssistantRoutine;
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

export type AssistantRunAction = "rerun" | "cancel" | "attention" | "none";

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
  if (item.state === "open") {
    if (attentionNeedsRebind(item.action)) return "rebind";
    if (attentionVerifiesOutcome(item.action)) return "verify";
    return attentionNeedsAnswer(item.action) ? "answer" : "approval";
  }
  const waiting = run?.state === "waiting_approval" || run?.state === "waiting_attention";
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

function firstSentence(value: string): string {
  const trimmed = value.trim();
  const match = trimmed.match(/^(.+?[。！？.!?])(?:\s|$)/);
  return match?.[1] ?? trimmed;
}

export function timelineEntries(snapshot: AssistantSnapshot, day: Date, copy: AssistantCopy): AssistantTimelineEntry[] {
  const sameDay = (date: Date) => date.getFullYear() === day.getFullYear() && date.getMonth() === day.getMonth() && date.getDate() === day.getDate();
  const entries: AssistantTimelineEntry[] = [];
  for (const run of snapshot.runs) {
    const at = validDate(run.started_at) ?? validDate(run.scheduled_for) ?? validDate(run.created_at);
    if (!at || !sameDay(at)) continue;
    entries.push({
      id: run.id,
      at,
      kind: "run",
      title: run.summary ? firstSentence(run.summary) : runStateLabel(run.state, "zh"),
      detail: run.summary,
      run,
    });
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
  return entries.sort((a, b) => a.at.getTime() - b.at.getTime());
}
