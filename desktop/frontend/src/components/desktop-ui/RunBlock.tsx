import {
  AlertCircle,
  CheckCircle2,
  CircleHelp,
  CircleStop,
  Clock,
  Loader2,
  RotateCcw,
  Square,
} from "lucide-react";
import { useCallback, useEffect, useRef } from "react";
import type { RunRecord, RunStatus, RunStepStatus } from "../../store/run";

export interface RunBlockProps {
  run: RunRecord;
  onStop?: (runId: string) => void;
  onRetry?: (runId: string) => void;
  onStepSelect?: (runId: string, stepIndex: number) => void;
  elapsedSeconds?: number;
  hidden?: boolean;
}

const STATUS_LABEL: Record<RunStatus, string> = {
  queued: "排队中",
  running: "运行中",
  waiting_user: "等待用户",
  reconnecting: "正在重连",
  completed: "运行完成",
  failed: "运行失败",
  cancelled: "已取消",
};

function isTerminal(status: RunStatus): boolean {
  return status === "completed" || status === "failed" || status === "cancelled";
}

function elapsed(run: RunRecord, override?: number): number | undefined {
  if (override !== undefined) return override;
  if (!run.startedAt) return undefined;
  return Math.max(0, Math.round(((run.completedAt ?? Date.now()) - run.startedAt) / 1000));
}

function statusIcon(status: RunStatus, size = 16): React.ReactNode {
  switch (status) {
    case "queued":
      return <Clock size={size} />;
    case "running":
    case "reconnecting":
      return <Loader2 size={size} className="animate-spin" />;
    case "waiting_user":
      return <CircleHelp size={size} />;
    case "completed":
      return <CheckCircle2 size={size} />;
    case "failed":
      return <AlertCircle size={size} />;
    case "cancelled":
      return <Square size={size} />;
  }
}

function runMeta(run: RunRecord, elapsedSeconds?: number): string {
  const seconds = elapsed(run, elapsedSeconds);
  const parts = [`${run.events.length} 条记录`];
  if (seconds !== undefined) parts.push(`${seconds} 秒`);
  return parts.join(" · ");
}

/** Process face: only real events are shown; there are no future placeholders. */
export function ActiveRunView({
  run,
  onStop,
  onRetry,
  onStepSelect,
  elapsedSeconds,
  hidden = false,
}: RunBlockProps) {
  const selectedIndex = run.selectedStepIndex ?? Math.max(0, run.events.length - 1);
  const tabsRef = useRef<HTMLDivElement>(null);
  const drag = useRef({ active: false, moved: false, startX: 0, scrollLeft: 0 });

  const handlePointerDown = useCallback((event: React.PointerEvent) => {
    if (event.button !== 0 || !tabsRef.current) return;
    drag.current = { active: true, moved: false, startX: event.clientX, scrollLeft: tabsRef.current.scrollLeft };
  }, []);

  const handlePointerMove = useCallback((event: React.PointerEvent) => {
    if (!drag.current.active || !tabsRef.current) return;
    const distance = event.clientX - drag.current.startX;
    if (Math.abs(distance) > 6) drag.current.moved = true;
    if (!drag.current.moved) return;
    tabsRef.current.setPointerCapture(event.pointerId);
    tabsRef.current.classList.add("active-run-view__tabs--dragging");
    tabsRef.current.scrollLeft = drag.current.scrollLeft - distance;
  }, []);

  const handlePointerUp = useCallback((event: React.PointerEvent) => {
    const element = tabsRef.current;
    element?.classList.remove("active-run-view__tabs--dragging");
    if (element?.hasPointerCapture(event.pointerId)) element.releasePointerCapture(event.pointerId);
    setTimeout(() => { drag.current = { active: false, moved: false, startX: 0, scrollLeft: 0 }; }, 0);
  }, []);

  const handleWheel = useCallback((event: React.WheelEvent) => {
    const element = tabsRef.current;
    if (!element || element.scrollWidth <= element.clientWidth) return;
    event.preventDefault();
    element.scrollLeft += Math.abs(event.deltaX) >= Math.abs(event.deltaY) ? event.deltaX : event.deltaY;
  }, []);

  useEffect(() => {
    const element = tabsRef.current;
    const target = element?.querySelectorAll<HTMLElement>(".run-step-tab")[selectedIndex];
    if (typeof target?.scrollIntoView === "function") {
      target.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "nearest" });
    }
  }, [run.events.length, selectedIndex]);

  return (
    <section
      className={`run-work-face run-process-face active-run-view active-run-view--${run.status}`}
      aria-label={`思考与执行过程 — ${STATUS_LABEL[run.status]}`}
      aria-busy={!isTerminal(run.status)}
      aria-hidden={hidden}
    >
      <header className="run-work-face__header active-run-view__header">
        <span className="run-work-face__status active-run-view__header-status">
          {statusIcon(run.status, 16)}
          <strong className="active-run-view__status-text">{STATUS_LABEL[run.status]}</strong>
          <span aria-hidden="true">·</span>
          <span className="run-work-face__meta">{runMeta(run, elapsedSeconds)}</span>
        </span>
        <span className="run-work-face__actions active-run-view__actions">
          {!isTerminal(run.status) && onStop && (
            <IconButton
              icon={<CircleStop size={14} />}
              label="停止运行"
              tabIndex={hidden ? -1 : 0}
              onClick={() => onStop(run.runId)}
            />
          )}
          {(run.status === "failed" || run.status === "cancelled") && onRetry && (
            <IconButton
              icon={<RotateCcw size={14} />}
              label="重试"
              tabIndex={hidden ? -1 : 0}
              onClick={() => onRetry(run.runId)}
            />
          )}
        </span>
      </header>

      <div
        ref={tabsRef}
        className="active-run-view__tabs"
        role="tablist"
        aria-label="已发生的执行记录"
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerUp}
        onWheel={handleWheel}
        onClickCapture={(event) => { if (drag.current.moved) event.stopPropagation(); }}
      >
        {run.events.map((event, index) => (
          <RunStepTab
            key={event.eventId}
            index={index}
            label={event.stepLabel ?? "执行记录"}
            isLast={index === run.events.length - 1}
            selected={selectedIndex === index}
            runStatus={run.status}
            eventStatus={event.status}
            tabIndex={hidden ? -1 : 0}
            onClick={() => onStepSelect?.(run.runId, index)}
          />
        ))}
      </div>

      <RunDetailViewport events={run.events} selectedStepIndex={selectedIndex} />
    </section>
  );
}

function RunStepTab({
  index,
  label,
  isLast,
  selected,
  runStatus,
  eventStatus,
  tabIndex,
  onClick,
}: {
  index: number;
  label: string;
  isLast: boolean;
  selected: boolean;
  runStatus: RunStatus;
  eventStatus?: RunStepStatus;
  tabIndex: number;
  onClick: () => void;
}) {
  const status: RunStatus = eventStatus ?? (isLast ? runStatus : "completed");
  return (
    <button
      type="button"
      role="tab"
      className={`run-step-tab run-step-tab--${status}`}
      aria-selected={selected}
      aria-label={`记录 ${index + 1}: ${label}`}
      tabIndex={tabIndex}
      onClick={onClick}
    >
      <span className="run-step-tab__number">{index + 1}</span>
      <span className="run-step-tab__label">{label}</span>
      {status === "completed" && <CheckCircle2 size={12} />}
      {status === "failed" && <AlertCircle size={12} />}
      {(status === "running" || status === "queued" || status === "reconnecting") && (
        <Loader2 size={12} className="animate-spin" />
      )}
    </button>
  );
}

export function RunDetailViewport({ events, selectedStepIndex }: {
  events: RunRecord["events"];
  selectedStepIndex?: number;
}) {
  const event = selectedStepIndex === undefined ? events[events.length - 1] : events[selectedStepIndex];
  return (
    <div className="run-detail-viewport" role="log" aria-label="执行详情" aria-live="polite">
      {event ? <div className="run-detail-viewport__event">{event.content}</div> : (
        <span className="run-detail-viewport__empty">等待第一条执行记录…</span>
      )}
    </div>
  );
}

/** Fixed-size process window. Terminal visibility is controlled by its action-row toggle. */
export function RunBlock(props: RunBlockProps) {
  return (
    <div
      className={`run-work-window run-work-window--${props.run.status}`}
      data-face="process"
    >
      <ActiveRunView {...props} />
    </div>
  );
}

function IconButton({ icon, label, text, tabIndex, onClick }: {
  icon: React.ReactNode;
  label: string;
  text?: string;
  tabIndex?: number;
  onClick: (event: React.MouseEvent) => void;
}) {
  return (
    <button type="button" className={`icon-button${text ? " icon-button--text" : ""}`} aria-label={label} tabIndex={tabIndex} onClick={onClick}>
      {icon}
      {text && <span>{text}</span>}
    </button>
  );
}
