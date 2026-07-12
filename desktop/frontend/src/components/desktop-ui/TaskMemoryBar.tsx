import { Target, ArrowRight, ListTodo, CheckCircle2 } from "lucide-react";
import { Tooltip } from "../Tooltip";
import type { MemoryLine } from "../../store/memory";

// ── Props ──────────────────────────────────────────────────────────────────

export interface TaskMemoryBarProps {
  /** The memory line for the current session, or null when none is set. */
  memoryLine: Partial<MemoryLine> | null;
  /** Optional callback to open the memory editor. */
  onEditGoal?: () => void;
  /**
   * When memoryLine is null and the session is terminal, this compact
   * headline (conclusion-first, news-style) derived from the latest real
   * assistant text keeps the bar visible. Never use static demo text.
   */
  recentlyDid?: string | null;
  /**
   * Full detail version of the recentlyDid recap. When provided, the
   * visible bar shows the headline while the Tooltip and aria-label
   * expose the full detail. Only meaningful for the "recent" segment;
   * goal / current / next are unaffected.
   */
  recentlyDidDetail?: string | null;
}

// ── Component ───────────────────────────────────────────────────────────────

/**
 * TaskMemoryBar renders real goal / current / next-step memory as one compact
 * accessible line. When memoryLine is null and recentlyDid is provided, it
 * shows a simplified "recently did" summary so the bar stays visible for
 * non-empty terminal sessions.
 *
 * This is a pure presentational primitive — it does NOT subscribe to stores.
 */
export function TaskMemoryBar({ memoryLine, onEditGoal, recentlyDid, recentlyDidDetail }: TaskMemoryBarProps) {
  // Terminal fallback: no task memory recorded, but we have a derived summary.
  if (!memoryLine && recentlyDid?.trim()) {
    const detail = recentlyDidDetail?.trim() || recentlyDid;
    return (
      <div className="task-memory-bar task-memory-bar--no-current" role="status" aria-label={`最近：${detail}`}>
        <Segment
          kind="recent"
          icon={<CheckCircle2 size={14} aria-hidden="true" />}
          label="最近"
          value={recentlyDid}
          detail={recentlyDidDetail?.trim() || undefined}
        />
      </div>
    );
  }

  if (!memoryLine) return null;

  const goal = typeof memoryLine.goal === "string" ? memoryLine.goal : "";
  const current = typeof memoryLine.current === "string" ? memoryLine.current : "";
  const nextStep = typeof memoryLine.nextStep === "string" ? memoryLine.nextStep : "";
  const recent = recentlyDid?.trim() ?? "";
  const recentDetail = recentlyDidDetail?.trim();
  if (!goal.trim() && !current.trim() && !nextStep.trim() && !recent) return null;

  return (
    <div
      className={"task-memory-bar" + (!current.trim() ? " task-memory-bar--no-current" : "")}
      role="status"
      aria-label={[goal && `${memoryLine.goalSource === "user_prompt" ? "任务" : "目标"}：${goal}`, current && `当前：${current}`, nextStep && `下一步：${nextStep}`, recent && `最近：${recentDetail ?? recent}`].filter(Boolean).join(" · ")}
    >
      {goal && <Segment kind="goal" icon={<Target size={14} aria-hidden="true" />} label={memoryLine.goalSource === "user_prompt" ? "任务" : "目标"} value={goal} />}
      {current && <Segment kind="current" icon={<ArrowRight size={14} aria-hidden="true" />} label="当前" value={current} />}
      {nextStep && <Segment kind="next" icon={<ListTodo size={14} aria-hidden="true" />} label="下一步" value={nextStep} />}
      {recent && <Segment kind="recent" icon={<CheckCircle2 size={14} aria-hidden="true" />} label="最近" value={recent} detail={recentDetail || undefined} />}

      {onEditGoal && (
        <button
          type="button"
          className="task-memory-bar__edit-btn"
          onClick={onEditGoal}
          aria-label="展开记忆"
        >
          展开
        </button>
      )}
    </div>
  );
}

// ── Internal ────────────────────────────────────────────────────────────────

function Segment({
  kind,
  icon,
  label,
  value,
  detail,
}: {
  kind: "goal" | "current" | "next" | "recent";
  icon: React.ReactNode;
  label: string;
  value: string;
  /** Full text for Tooltip; falls back to value when absent. */
  detail?: string;
}) {
  const tooltipText = detail ?? value;
  return (
    <Tooltip label={`${label}：${tooltipText}`} side="top" className={`task-memory-bar__segment-slot task-memory-bar__segment-slot--${kind}`}>
      <span className="task-memory-bar__segment" tabIndex={0}>
        {icon}
        <span className="task-memory-bar__segment-label">{label}：</span>
        <span className="task-memory-bar__segment-value">{value}</span>
      </span>
    </Tooltip>
  );
}
