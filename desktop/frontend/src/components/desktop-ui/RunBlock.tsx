import {
  AlertCircle,
  CheckCircle2,
  CircleHelp,
  CircleStop,
  Clock,
  Loader2,
  RotateCcw,
  Square,
  ChevronDown,
  ChevronUp,
} from "lucide-react";
import type { RunRecord, RunStatus, RunStepStatus } from "../../store/run";

import { useRef, useEffect, useCallback } from "react";

// ── Props ──────────────────────────────────────────────────────────────────

export interface RunBlockProps {
  /** The run record to display. */
  run: RunRecord;
  /** Callback to stop a running / queued run. */
  onStop?: (runId: string) => void;
  /** Callback to retry a failed / cancelled run. */
  onRetry?: (runId: string) => void;
  /** Callback to toggle collapsed / expanded. */
  onToggle?: (runId: string) => void;
  /** Callback when the user selects a specific step tab (0-based index). */
  onStepSelect?: (runId: string, stepIndex: number) => void;
  /** Optional elapsed-seconds override (e.g. from a timer). */
  elapsedSeconds?: number;
}

// ── Helpers ────────────────────────────────────────────────────────────────

const STATUS_LABEL: Record<RunStatus, string> = {
  queued: "排队中",
  running: "运行中",
  waiting_user: "等待用户",
  reconnecting: "正在重连",
  completed: "运行完成",
  failed: "运行失败",
  cancelled: "已取消",
};

function statusIcon(status: RunStatus, size = 16): React.ReactNode {
  const cls = status === "running" || status === "reconnecting" ? "animate-spin" : undefined;
  switch (status) {
    case "queued":
      return <Clock size={size} className={cls} />;
    case "running":
      return <Loader2 size={size} className={cls} />;
    case "waiting_user":
      return <CircleHelp size={size} />;
    case "reconnecting":
      return <Loader2 size={size} className={cls} />;
    case "completed":
      return <CheckCircle2 size={size} />;
    case "failed":
      return <AlertCircle size={size} />;
    case "cancelled":
      return <Square size={size} />;
  }
}

// ── CompletedRunTab (collapsed mode) ────────────────────────────────────────

export function CompletedRunTab({
  run,
  onStop,
  onRetry,
  onToggle,
  elapsedSeconds,
}: RunBlockProps) {
  const isTerminal =
    run.status === "completed" || run.status === "failed" || run.status === "cancelled";
  const secs =
    elapsedSeconds ??
    (run.completedAt && run.startedAt
      ? Math.round((run.completedAt - run.startedAt) / 1000)
      : undefined);

  return (
    <button
      type="button"
      className={`completed-run-tab completed-run-tab--${run.status}`}
      aria-label={`${STATUS_LABEL[run.status]} · ${run.events.length} 步` + (secs !== undefined ? ` · ${secs} 秒` : "")}
      aria-expanded={run.expanded}
      onClick={() => onToggle?.(run.runId)}
    >
      {statusIcon(run.status, 16)}

      <span className="completed-run-tab__label">
        <span className="completed-run-tab__status-text">{STATUS_LABEL[run.status]}</span>
        {isTerminal && (
          <span className="completed-run-tab__meta">
            <span className="completed-run-tab__divider"> · </span>
            {run.events.length} 步
            {secs !== undefined ? <><span className="completed-run-tab__divider"> · </span>{secs} 秒</> : ""}
          </span>
        )}
      </span>

      <span className="completed-run-tab__actions">
        {(run.status === "queued" || run.status === "running" || run.status === "waiting_user" || run.status === "reconnecting") &&
          onStop && (
              <IconButton
              icon={<CircleStop size={14} />}
              label="停止运行"
              onClick={(e) => { e.stopPropagation(); onStop(run.runId); }}
            />
          )}

        {(run.status === "failed" || run.status === "cancelled") && onRetry && (
          <IconButton
            icon={<RotateCcw size={14} />}
            label="重试"
            onClick={(e) => { e.stopPropagation(); onRetry(run.runId); }}
          />
        )}

        {run.expanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
      </span>
    </button>
  );
}

// ── ActiveRunView (expanded mode) ───────────────────────────────────────────

export function ActiveRunView({
  run,
  onStop,
  onRetry,
  onToggle,
  onStepSelect,
  elapsedSeconds,
}: RunBlockProps) {
  const selectedStepIndex = run.selectedStepIndex ?? Math.max(0, run.events.length - 1);
  const secs =
    elapsedSeconds ??
    (run.completedAt && run.startedAt
      ? Math.round((run.completedAt - run.startedAt) / 1000)
      : undefined);

  // ── Drag-to-scroll state ──────────────────────────────────────────────────
  const tabsRef = useRef<HTMLDivElement>(null);
  const dragState = useRef({ active: false, dragging: false, startX: 0, scrollLeft: 0 });
  const wasDragging = useRef(false);

  const handlePointerDown = useCallback((e: React.PointerEvent) => {
    if (e.button !== 0) return;
    const el = tabsRef.current;
    if (!el) return;
    wasDragging.current = false;
    dragState.current = {
      active: true,
      dragging: false,
      startX: e.clientX,
      scrollLeft: el.scrollLeft,
    };
  }, []);

  const handlePointerMove = useCallback((e: React.PointerEvent) => {
    if (!dragState.current.active) return;
    const dx = e.clientX - dragState.current.startX;
    if (!dragState.current.dragging && Math.abs(dx) > 6) {
      dragState.current.dragging = true;
      wasDragging.current = true;
      const el = tabsRef.current;
      el?.classList.add("active-run-view__tabs--dragging");
      el?.setPointerCapture(e.pointerId);
    }
    if (dragState.current.dragging) {
      const el = tabsRef.current;
      if (el) {
        el.scrollLeft = dragState.current.scrollLeft - dx;
      }
    }
  }, []);

  const handlePointerUp = useCallback((e: React.PointerEvent) => {
    const el = tabsRef.current;
    if (el) {
      el.classList.remove("active-run-view__tabs--dragging");
      try { el.releasePointerCapture(e.pointerId); } catch { /* ignore */ }
    }
    dragState.current = { active: false, dragging: false, startX: 0, scrollLeft: 0 };
    if (wasDragging.current) {
      setTimeout(() => { wasDragging.current = false; }, 0);
    }
  }, []);

  const handleTabClickCapture = useCallback((e: React.MouseEvent) => {
    if (wasDragging.current) {
      e.stopPropagation();
    }
  }, []);

  // ── Wheel horizontal scroll ───────────────────────────────────────────────
  const handleWheel = useCallback((e: React.WheelEvent) => {
    const el = tabsRef.current;
    if (!el || el.scrollWidth <= el.clientWidth) return;
    const delta = Math.abs(e.deltaX) >= Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
    if (!delta) return;
    e.preventDefault();
    el.scrollLeft += delta;
  }, []);

  // ── Scroll selected tab into view ─────────────────────────────────────────
  useEffect(() => {
    const el = tabsRef.current;
    if (!el) return;
    const tabButtons = el.querySelectorAll<HTMLElement>(".run-step-tab");
    const target = tabButtons[selectedStepIndex];
    if (target) {
      target.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "nearest" });
    } else {
      el.scrollLeft = el.scrollWidth;
    }
  }, [selectedStepIndex, run.events.length]);

  return (
    <div
      className={`active-run-view active-run-view--${run.status}`}
      role="region"
      aria-label={`运行详情 — ${STATUS_LABEL[run.status]}`}
      aria-busy={run.status === "running" || run.status === "reconnecting" || run.status === "queued"}
    >
      <div className="active-run-view__header">
        <span className="active-run-view__header-status">
          {statusIcon(run.status, 16)}
          <span className="active-run-view__status-text">{STATUS_LABEL[run.status]}</span>
        </span>
        {isTerminalLike(run.status) && (
          <span className="active-run-view__meta">
            · {run.events.length} 步
            {secs !== undefined ? ` · ${secs} 秒` : ""}
          </span>
        )}

        <span className="active-run-view__actions">
          {(run.status === "queued" || run.status === "running" || run.status === "waiting_user" || run.status === "reconnecting") &&
            onStop && (
              <IconButton
                icon={<CircleStop size={14} />}
                label="停止运行"
                onClick={() => onStop(run.runId)}
              />
            )}
          {(run.status === "failed" || run.status === "cancelled") && onRetry && (
            <IconButton
              icon={<RotateCcw size={14} />}
              label="重试"
              onClick={() => onRetry(run.runId)}
            />
          )}
          <IconButton
            icon={<ChevronUp size={14} />}
            label="收起"
            onClick={() => onToggle?.(run.runId)}
          />
        </span>
      </div>

      {run.events.length > 0 && (
        <div
          ref={tabsRef}
          className="active-run-view__tabs"
          role="tablist"
          aria-label="运行步骤"
          onPointerDown={handlePointerDown}
          onPointerMove={handlePointerMove}
          onPointerUp={handlePointerUp}
          onPointerCancel={handlePointerUp}
          onWheel={handleWheel}
          onClickCapture={handleTabClickCapture}
        >
          {run.events.map((event, idx) => (
            <RunStepTab
              key={event.eventId}
              index={idx}
              label={event.stepLabel ?? `步骤 ${idx + 1}`}
              isLast={idx === run.events.length - 1}
              selected={selectedStepIndex === idx}
              status={run.status}
              eventStatus={event.status}
              onClick={() => onStepSelect?.(run.runId, idx)}
            />
          ))}
        </div>
      )}

      <RunDetailViewport events={run.events} selectedStepIndex={selectedStepIndex} />
    </div>
  );
}

// ── RunStepTab ──────────────────────────────────────────────────────────────

function RunStepTab({
  index,
  label,
  isLast,
  selected,
  status,
  eventStatus,
  onClick,
}: {
  index: number;
  label: string;
  isLast: boolean;
  selected: boolean;
  status: RunStatus;
  eventStatus?: RunStepStatus;
  onClick: () => void;
}) {
  const stepStatus: RunStatus = eventStatus ?? (isLast ? status : "completed");

  return (
    <button
      type="button"
      role="tab"
      className={`run-step-tab run-step-tab--${stepStatus}`}
      aria-selected={selected}
      aria-label={`步骤 ${index + 1}: ${label}`}
      onClick={onClick}
    >
      <span className="run-step-tab__number">{index + 1}</span>
      <span className="run-step-tab__label">{label}</span>
      {stepStatus === "completed" && <CheckCircle2 size={12} />}
      {stepStatus === "failed" && <AlertCircle size={12} />}
      {(stepStatus === "running" || stepStatus === "queued" || stepStatus === "reconnecting") && (
        <Loader2 size={12} className="animate-spin" />
      )}
    </button>
  );
}

// ── RunDetailViewport ───────────────────────────────────────────────────────

export function RunDetailViewport({
  events,
  selectedStepIndex,
}: {
  events: RunRecord["events"];
  selectedStepIndex?: number;
}) {
  const visibleEvents = selectedStepIndex !== undefined
    ? events.filter((_, idx) => idx === selectedStepIndex)
    : events;

  return (
    <div
      className="run-detail-viewport"
      role="log"
      aria-label="运行日志"
      aria-live="polite"
    >
      {visibleEvents.length === 0 && <span className="run-detail-viewport__empty">暂无事件</span>}
      {visibleEvents.map((event) => (
        <div key={event.eventId} className="run-detail-viewport__event">
          {event.content}
        </div>
      ))}
    </div>
  );
}

// ── RunBlock (orchestrator) ─────────────────────────────────────────────────

/**
 * RunBlock renders a run in either collapsed (CompletedRunTab) or expanded
 * (ActiveRunView) mode.
 *
 * This is a pure presentational primitive — it does NOT subscribe to stores.
 */
export function RunBlock(props: RunBlockProps) {
  return props.run.expanded ? <ActiveRunView {...props} /> : <CompletedRunTab {...props} />;
}

// ── Shared helpers ─────────────────────────────────────────────────────────-

function isTerminalLike(status: RunStatus): boolean {
  return status === "completed" || status === "failed" || status === "cancelled";
}

function IconButton({
  icon,
  label,
  onClick,
}: {
  icon: React.ReactNode;
  label: string;
  onClick: (e: React.MouseEvent) => void;
}) {
  return (
    <button
      type="button"
      className="icon-button"
      aria-label={label}
      onClick={onClick}
    >
      {icon}
    </button>
  );
}
