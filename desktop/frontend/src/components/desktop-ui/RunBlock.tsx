import {
  AlertCircle,
  CheckCircle2,
  CircleHelp,
  CircleStop,
  Code2,
  Clock,
  FileText,
  FlaskConical,
  Globe2,
  Loader2,
  RotateCcw,
  Search,
  Square,
  Terminal,
  Wrench,
} from "lucide-react";
import { useCallback, useEffect, useRef } from "react";
import type { RunEventKind, RunRecord, RunStatus, RunStepStatus } from "../../store/run";

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

      <RunDetailViewport events={run.events} selectedStepIndex={selectedIndex} runStatus={run.status} />

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
            kind={event.kind}
            tabIndex={hidden ? -1 : 0}
            onClick={() => onStepSelect?.(run.runId, index)}
          />
        ))}
      </div>

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
  kind,
  tabIndex,
  onClick,
}: {
  index: number;
  label: string;
  isLast: boolean;
  selected: boolean;
  runStatus: RunStatus;
  eventStatus?: RunStepStatus;
  kind: RunEventKind;
  tabIndex: number;
  onClick: () => void;
}) {
  const status: RunStatus = eventStatus ?? (isLast ? runStatus : "completed");
  return (
    <button
      type="button"
      role="tab"
      className={`run-step-tab run-step-tab--${status}`}
      data-kind={kind}
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

const ACTIVITY_LABEL: Record<RunEventKind, string> = {
  search: "正在搜索",
  read: "正在读取",
  edit: "正在编辑",
  command: "正在运行命令",
  test: "正在运行测试",
  browser: "正在操作浏览器",
  generic: "正在执行工具",
};

function activityIcon(kind: RunEventKind, size = 15): React.ReactNode {
  switch (kind) {
    case "search": return <Search size={size} />;
    case "read": return <FileText size={size} />;
    case "edit": return <Code2 size={size} />;
    case "command": return <Terminal size={size} />;
    case "test": return <FlaskConical size={size} />;
    case "browser": return <Globe2 size={size} />;
    case "generic": return <Wrench size={size} />;
  }
}

function compactText(value: string, max = 108): string {
  const text = value.replace(/\s+/g, " ").trim();
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

function sceneSubject(args?: string, fallback?: string): string {
  if (!args?.trim()) return compactText(fallback || "等待执行信息…", 92);
  try {
    const parsed = JSON.parse(args) as Record<string, unknown>;
    for (const key of ["command", "path", "file", "url", "query", "pattern", "selector"]) {
      if (typeof parsed[key] === "string" && parsed[key]) return compactText(parsed[key] as string, 92);
    }
  } catch {
    // Plain tool arguments are already displayable source data.
  }
  return compactText(args, 92);
}

function sceneLines(content: string): string[] {
  const lines = content
    .split(/\r?\n/)
    .map((line) => compactText(line, 112))
    .filter(Boolean)
    .slice(-3);
  return lines.length ? lines : ["等待输出…"];
}

function SceneBody({ kind, subject, lines }: { kind: RunEventKind; subject: string; lines: string[] }) {
  if (kind === "browser") {
    return (
      <div className="run-activity-browser">
        <div className="run-activity-browser__bar">
          <span className="run-activity-browser__lights" aria-hidden="true"><i /><i /><i /></span>
          <span className="run-activity-browser__address">{subject}</span>
        </div>
        <div className="run-activity-browser__page">{lines.map((line, index) => <span key={index}>{line}</span>)}</div>
      </div>
    );
  }

  if (kind === "command" || kind === "test") {
    return (
      <div className="run-activity-terminal">
        <div className="run-activity-terminal__command"><span aria-hidden="true">›</span>{subject}</div>
        {lines.map((line, index) => <span className="run-activity-terminal__line" key={index}>{line}</span>)}
      </div>
    );
  }

  if (kind === "read" || kind === "edit") {
    return (
      <div className="run-activity-editor">
        <div className="run-activity-editor__tab">{subject}</div>
        <div className="run-activity-editor__code">
          {lines.map((line, index) => <span key={index}><i>{index + 1}</i><code>{line}</code></span>)}
        </div>
      </div>
    );
  }

  return (
    <div className={`run-activity-lines run-activity-lines--${kind}`}>
      <strong>{subject}</strong>
      {lines.map((line, index) => <span key={index}>{line}</span>)}
    </div>
  );
}

export function RunDetailViewport({ events, selectedStepIndex, runStatus }: {
  events: RunRecord["events"];
  selectedStepIndex?: number;
  runStatus?: RunStatus;
}) {
  const event = selectedStepIndex === undefined ? events[events.length - 1] : events[selectedStepIndex];
  const kind = event?.kind ?? "generic";
  const sceneStatus = runStatus && runStatus !== "running"
    ? runStatus
    : (event?.status ?? runStatus ?? "running");
  const subject = sceneSubject(event?.args, event?.toolName || event?.stepLabel || event?.content);
  const lines = sceneLines(event?.content ?? "");
  return (
    <div
      className={`run-detail-viewport run-activity-scene run-activity-scene--${kind} run-activity-scene--${sceneStatus}`}
      data-kind={kind}
      data-status={sceneStatus}
      role="log"
      aria-label="执行详情"
      aria-live="polite"
    >
      {event ? <>
        <div className="run-activity-scene__heading">
          <span className="run-activity-scene__icon" aria-hidden="true">{activityIcon(kind)}</span>
          <strong>{sceneStatus === "completed" ? "执行完成" : sceneStatus === "failed" ? "执行失败" : ACTIVITY_LABEL[kind]}</strong>
          <span>{event.toolName || event.stepLabel}</span>
        </div>
        <SceneBody kind={kind} subject={subject} lines={lines} />
        <span className="run-activity-scene__scan" aria-hidden="true" />
        {(sceneStatus === "completed" || sceneStatus === "failed") && (
          <span className="run-activity-scene__reveal" aria-hidden="true">
            {sceneStatus === "completed" ? <CheckCircle2 size={17} /> : <AlertCircle size={17} />}
          </span>
        )}
      </> : (
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
