import {
  AlertCircle,
  ArrowDownToLine,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Copy,
  Filter,
  Loader2,
  Maximize2,
  Minimize2,
  RefreshCw,
  Square,
  WrapText,
  X,
} from "lucide-react";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { createPortal } from "react-dom";
import { useVirtualizer } from "@tanstack/react-virtual";
import { DiffView } from "../DiffView";
import { loadLayoutSize, saveLayoutSize } from "../../lib/layoutPreferences";
import { createRafResizeUpdater } from "../../lib/resizeDrag";
import {
  EMPTY_DETAIL_FILTER,
  countMatches,
  countNewRecords,
  detailArgsText,
  detailBodyText,
  filterRunDetailEvents,
  formatRunDetailDuration,
  highlightSegments,
  nextFailureIndex,
  prettyJson,
  resolveRunDetailSelection,
  type RunDetailFilter,
} from "../../lib/runDetail";
import { app } from "../../lib/bridge";
import type { RunDetailBody, RunDetailBodyFetcher } from "../../lib/runDetailData";
import { needsDetailBody, useRunDetailBody } from "../../lib/runDetailData";
import {
  detailBodyScrollKey,
  detailListScrollKey,
  recallDetailScroll,
  rememberDetailScroll,
} from "../../lib/runDetailScroll";
import type { RunEvent, RunRecord, RunStatus, RunStepStatus } from "../../store/run";

/**
 * The panel's own bridge call: the existing ToolResultForTab binding, which
 * returns the full args/output the backend still retains for one call, and null
 * when the session no longer has it (a sub-agent call, or cleared history).
 */
async function fetchDetailBody(tabId: string, callId: string): Promise<RunDetailBody | null> {
  const data = await app.ToolResultForTab(tabId, callId);
  return data ? { args: data.args, output: data.output } : null;
}

const detailFetcher: RunDetailBodyFetcher = fetchDetailBody;

const PANEL_DEFAULT_WIDTH = 760;
const PANEL_MIN_WIDTH = 420;
const PANEL_MAX_WIDTH = 1400;
const PANEL_MAX_RATIO = 0.92;
const ROW_HEIGHT = 44;
const OVERSCAN = 6;

/** Initial guess so the list renders deterministically before it is measured. */
const LIST_INITIAL_RECT = { width: 320, height: 480 };

export interface RunDetailPanelProps {
  run: RunRecord;
  /** Tab that owns the run; needed to fetch full bodies on demand. */
  tabId?: string;
  onSelectEvent: (runId: string, eventId?: string) => void;
  onToggleMaximized: (runId: string, maximized: boolean) => void;
  onClose: () => void;
  onStop?: (runId: string) => void;
  onRetry?: (runId: string) => void;
}

function clampPanelWidth(width: number, viewportWidth: number): number {
  const max = Math.max(PANEL_MIN_WIDTH, Math.min(PANEL_MAX_WIDTH, Math.floor(viewportWidth * PANEL_MAX_RATIO)));
  return Math.min(max, Math.max(PANEL_MIN_WIDTH, Math.round(width)));
}

function viewportWidth(): number {
  return typeof window === "undefined" ? 1440 : window.innerWidth;
}

const RUN_STATUS_LABEL: Record<RunStatus, string> = {
  queued: "排队中",
  running: "运行中",
  waiting_user: "等待用户",
  reconnecting: "正在重连",
  completed: "运行完成",
  failed: "运行失败",
  cancelled: "已取消",
};

const STEP_STATUS_LABEL: Record<RunStepStatus, string> = {
  running: "运行中",
  completed: "已完成",
  failed: "失败",
  stopped: "已停止",
};

function stepStatusIcon(status?: RunStepStatus) {
  switch (status) {
    case "completed": return <CheckCircle2 size={13} aria-hidden="true" />;
    case "failed": return <AlertCircle size={13} aria-hidden="true" />;
    case "stopped": return <Square size={13} aria-hidden="true" />;
    default: return <Loader2 size={13} className="animate-spin" aria-hidden="true" />;
  }
}

function rowDomId(eventId: string): string {
  return eventId.replace(/[^a-zA-Z0-9_-]/g, "-");
}

/**
 * Right-side detail panel for one run: the record list on the left, the full
 * record on the right. Non-modal by design — a running turn keeps streaming
 * while the user reads, and an arriving record never steals the one being read.
 */
export function RunDetailPanel({
  run,
  tabId,
  onSelectEvent,
  onToggleMaximized,
  onClose,
  onStop,
  onRetry,
}: RunDetailPanelProps) {
  const [filter, setFilter] = useState<RunDetailFilter>(EMPTY_DETAIL_FILTER);
  const [width, setWidth] = useState(() => loadLayoutSize("runDetailWidth", PANEL_DEFAULT_WIDTH, clampPanelWidthAtViewport));
  const [screenWidth, setScreenWidth] = useState(viewportWidth);
  const panelRef = useRef<HTMLElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const listRestored = useRef(false);

  const maximized = Boolean(run.detailMaximized);
  const effectiveWidth = maximized ? screenWidth : clampPanelWidth(width, screenWidth);

  useEffect(() => {
    const onResize = () => setScreenWidth(window.innerWidth);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  // Focus moves into the panel on open so Escape and the list keys work without
  // a click; the opener restores focus to its trigger when this closes.
  useEffect(() => {
    panelRef.current?.focus();
  }, []);

  const selection = resolveRunDetailSelection(run);
  const ordinals = useMemo(() => new Map(run.events.map((event, index) => [event.eventId, index + 1])), [run.events]);
  const visibleEvents = useMemo(() => filterRunDetailEvents(run.events, filter), [run.events, filter]);
  const selectedVisibleIndex = selection.event
    ? visibleEvents.findIndex((event) => event.eventId === selection.event?.eventId)
    : -1;

  const virtualizer = useVirtualizer({
    count: visibleEvents.length,
    getScrollElement: () => listRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: OVERSCAN,
    initialRect: LIST_INITIAL_RECT,
  });

  useLayoutEffect(() => {
    const target = listRef.current;
    if (!target) return;
    target.scrollTop = recallDetailScroll(detailListScrollKey(run.runId));
  }, [run.runId]);

  useEffect(() => {
    if (selectedVisibleIndex < 0) return;
    if (!listRestored.current) {
      listRestored.current = true;
      if (!selection.following && recallDetailScroll(detailListScrollKey(run.runId)) > 0) return;
    }
    virtualizer.scrollToIndex(selectedVisibleIndex, { align: "auto" });
  }, [selectedVisibleIndex, virtualizer, selection.following ? visibleEvents.length : 0]);

  const startResize = useCallback((event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.button !== 0 || maximized) return;
    const panel = panelRef.current;
    if (!panel) return;
    event.preventDefault();
    let nextWidth = effectiveWidth;
    const live = createRafResizeUpdater({ target: panel, separator: event.currentTarget, cssVar: "--run-detail-width" });
    const onMove = (move: PointerEvent) => {
      nextWidth = clampPanelWidth(window.innerWidth - move.clientX, window.innerWidth);
      live.schedule(nextWidth);
    };
    const onDone = () => {
      live.flush();
      setWidth(nextWidth);
      saveLayoutSize("runDetailWidth", nextWidth, (value) => clampPanelWidth(value, window.innerWidth));
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onDone);
      window.removeEventListener("pointercancel", onDone);
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
    };
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onDone);
    window.addEventListener("pointercancel", onDone);
  }, [effectiveWidth, maximized]);

  const resizeByKeyboard = useCallback((event: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (maximized) return;
    const delta = event.key === "ArrowLeft" ? 24 : event.key === "ArrowRight" ? -24 : 0;
    if (delta === 0) return;
    event.preventDefault();
    const next = clampPanelWidth(effectiveWidth + delta, window.innerWidth);
    setWidth(next);
    saveLayoutSize("runDetailWidth", next, (value) => clampPanelWidth(value, window.innerWidth));
  }, [effectiveWidth, maximized]);

  const jumpToFailure = (direction: 1 | -1) => {
    const next = nextFailureIndex(visibleEvents, selectedVisibleIndex, direction);
    if (next === undefined) return;
    onSelectEvent(run.runId, visibleEvents[next].eventId);
  };

  const pinned = !selection.following;
  const unseen = pinned ? countNewRecords(run.events, selection.index) : 0;

  const onListKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (visibleEvents.length === 0) return;
    const current = selectedVisibleIndex >= 0 ? selectedVisibleIndex : visibleEvents.length - 1;
    let next: number | undefined;
    if (event.key === "ArrowDown") next = Math.min(visibleEvents.length - 1, current + 1);
    else if (event.key === "ArrowUp") next = Math.max(0, current - 1);
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = visibleEvents.length - 1;
    if (next === undefined) return;
    event.preventDefault();
    onSelectEvent(run.runId, visibleEvents[next].eventId);
  };

  return createPortal(
    <section
      ref={panelRef}
      className={`run-detail-panel${maximized ? " run-detail-panel--maximized" : ""}${effectiveWidth <= 600 ? " run-detail-panel--narrow" : ""}`}
      role="dialog"
      aria-modal={false}
      aria-label={`运行记录详情 — ${RUN_STATUS_LABEL[run.status]}`}
      tabIndex={-1}
      data-status={run.status}
      style={{ "--run-detail-width": `${effectiveWidth}px` } as CSSProperties}
      onKeyDown={(event) => {
        if (event.key !== "Escape") return;
        event.stopPropagation();
        onClose();
      }}
    >
      <button
        className="run-detail-panel__resizer"
        type="button"
        role="separator"
        aria-orientation="vertical"
        aria-label="调整记录详情面板宽度"
        aria-valuemin={PANEL_MIN_WIDTH}
        aria-valuemax={PANEL_MAX_WIDTH}
        aria-valuenow={effectiveWidth}
        onPointerDown={startResize}
        onKeyDown={resizeByKeyboard}
        onDoubleClick={() => {
          setWidth(PANEL_DEFAULT_WIDTH);
          saveLayoutSize("runDetailWidth", PANEL_DEFAULT_WIDTH);
        }}
      />

      <header className="run-detail-panel__header">
        <span className="run-detail-panel__title">
          <strong>运行记录详情</strong>
          <span className="run-detail-panel__meta">
            {RUN_STATUS_LABEL[run.status]} · {run.events.length} 条记录
            {run.completedAt ? ` · ${formatRunDetailDuration(Math.max(0, run.completedAt - run.startedAt))}` : ""}
          </span>
        </span>
        <span className="run-detail-panel__actions">
          <button
            type="button"
            className="run-detail-icon"
            aria-label={maximized ? "还原面板" : "最大化面板"}
            aria-pressed={maximized}
            onClick={() => onToggleMaximized(run.runId, !maximized)}
          >
            {maximized ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
          </button>
          <button type="button" className="run-detail-icon" aria-label="关闭记录详情" onClick={onClose}>
            <X size={15} />
          </button>
        </span>
      </header>

      <div className="run-detail-panel__toolbar" role="toolbar" aria-label="记录筛选">
        <input
          type="search"
          className="run-detail-search"
          aria-label="搜索命令、文件或工具"
          placeholder="搜索命令、文件或工具"
          value={filter.query}
          onChange={(event) => setFilter({ ...filter, query: event.target.value })}
        />
        <button
          type="button"
          className="run-detail-chip"
          aria-label="仅失败"
          aria-pressed={filter.failuresOnly}
          onClick={() => setFilter({ ...filter, failuresOnly: !filter.failuresOnly })}
        >
          <Filter size={12} aria-hidden="true" />
          仅失败
        </button>
        <button
          type="button"
          className="run-detail-chip"
          aria-label="上一个失败记录"
          disabled={visibleEvents.length === 0}
          onClick={() => jumpToFailure(-1)}
        >
          <ChevronUp size={12} aria-hidden="true" />
          上一失败
        </button>
        <button
          type="button"
          className="run-detail-chip"
          aria-label="下一个失败记录"
          disabled={visibleEvents.length === 0}
          onClick={() => jumpToFailure(1)}
        >
          <ChevronDown size={12} aria-hidden="true" />
          下一失败
        </button>
        <button
          type="button"
          className="run-detail-chip"
          aria-label="跟随最新"
          aria-pressed={!pinned}
          onClick={() => onSelectEvent(run.runId, pinned ? undefined : selection.event?.eventId)}
        >
          <ArrowDownToLine size={12} aria-hidden="true" />
          跟随最新
        </button>
        <span className="run-detail-panel__count" aria-label="已显示记录数">
          {visibleEvents.length}/{run.events.length}
        </span>
      </div>

      {pinned && unseen > 0 && (
        <button
          type="button"
          className="run-detail-newrecords"
          onClick={() => onSelectEvent(run.runId, undefined)}
        >
          {unseen} 条新记录 · 跳到最新
        </button>
      )}

      <div className="run-detail-panel__body">
        <div
          ref={listRef}
          className="run-detail-list"
          role="listbox"
          aria-label="运行记录列表"
          aria-activedescendant={selection.event ? `run-detail-row-${rowDomId(selection.event.eventId)}` : undefined}
          tabIndex={0}
          onKeyDown={onListKeyDown}
          onScroll={(event) => rememberDetailScroll(detailListScrollKey(run.runId), event.currentTarget.scrollTop)}
        >
          {visibleEvents.length === 0 && (
            <p className="run-detail-list__empty">没有符合条件的记录。</p>
          )}
          <div className="run-detail-list__sizer" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((row) => {
              const record = visibleEvents[row.index];
              const ordinal = ordinals.get(record.eventId)!;
              const selected = selection.event?.eventId === record.eventId;
              return (
                <div
                  key={record.eventId}
                  id={`run-detail-row-${rowDomId(record.eventId)}`}
                  role="option"
                  aria-selected={selected}
                  aria-label={`记录 ${ordinal}: ${record.stepLabel ?? "执行记录"}`}
                  data-index={row.index}
                  data-status={record.status ?? "completed"}
                  className={`run-detail-row run-detail-row--${record.status ?? "completed"}${selected ? " run-detail-row--selected" : ""}`}
                  style={{ transform: `translateY(${row.start}px)`, height: ROW_HEIGHT }}
                  onClick={() => onSelectEvent(run.runId, record.eventId)}
                >
                  <span className="run-detail-row__number">{ordinal}</span>
                  <span className="run-detail-row__label">
                    <strong>{record.toolName ?? record.stepLabel ?? "执行记录"}</strong>
                    <span>{record.stepLabel ?? record.content}</span>
                  </span>
                  <span className="run-detail-row__status">{stepStatusIcon(record.status)}</span>
                </div>
              );
            })}
          </div>
        </div>

        <div className="run-detail-record">
          <RunDetailRecord
            runId={run.runId}
            tabId={tabId}
            event={selection.event}
            index={selection.index}
            total={run.events.length}
            pinMissing={selection.pinMissing}
          />
          <div className="run-detail-panel__runactions">
            {run.status !== "completed" && run.status !== "failed" && run.status !== "cancelled" && onStop && (
              <button type="button" className="run-detail-chip" onClick={() => onStop(run.runId)}>停止运行</button>
            )}
            {(run.status === "failed" || run.status === "cancelled") && onRetry && (
              <button type="button" className="run-detail-chip" onClick={() => onRetry(run.runId)}>重试</button>
            )}
          </div>
        </div>
      </div>
    </section>,
    document.body,
  );
}

function clampPanelWidthAtViewport(value: number): number {
  return clampPanelWidth(value, viewportWidth());
}

/** One record rendered in full: identity, args, diff, output and error apart. */
export function RunDetailRecord({
  runId,
  tabId,
  event,
  index,
  total,
  pinMissing = false,
}: {
  runId: string;
  tabId?: string;
  event?: RunEvent;
  index: number;
  total: number;
  pinMissing?: boolean;
}) {
  const { state, load } = useRunDetailBody(tabId, event, detailFetcher, runId);
  const [wrap, setWrap] = useState(true);
  const [find, setFind] = useState("");
  const bodyRef = useRef<HTMLPreElement>(null);
  const loadKey = needsDetailBody(event) ? `${runId}\u0000${event?.eventId}\u0000${event?.callId ?? ""}` : "";
  const loaded = useRef("");

  // Only `archived`/`truncated` records need a round trip; a complete inline
  // body is already the real thing and must not be re-fetched or replaced.
  useEffect(() => {
    if (!loadKey || loaded.current === loadKey) return;
    loaded.current = loadKey;
    load();
  }, [loadKey]);

  const eventId = event?.eventId ?? "";
  useLayoutEffect(() => {
    const target = bodyRef.current;
    if (!target) return;
    target.scrollTop = recallDetailScroll(detailBodyScrollKey(runId, eventId));
  }, [runId, eventId, state.phase]);

  if (!event) {
    return <div className="run-detail-record__empty">本次运行还没有记录。</div>;
  }

  const args = state.args ?? detailArgsText(event);
  const body = state.output ?? detailBodyText(event);
  const status = event.status ?? "completed";
  const showFind = find.trim().length > 0;

  return (
    <>
      <header className="run-detail-record__header">
        <span className="run-detail-record__index">{index + 1} / {total}</span>
        <strong>{event.stepLabel ?? event.toolName ?? "执行记录"}</strong>
        <span className={`run-detail-record__status run-detail-record__status--${status}`} data-status={status}>
          {stepStatusIcon(event.status)}
          {STEP_STATUS_LABEL[status]}
        </span>
        {event.durationMs !== undefined ? <span className="run-detail-record__duration">耗时 {formatRunDetailDuration(event.durationMs)}</span> : null}
        {event.toolName ? <span className="run-detail-record__tool">{event.toolName}</span> : null}
        {event.callId ? <span className="run-detail-record__callid">{event.callId}</span> : null}
      </header>

      {pinMissing && (
        <p className="run-detail-record__note" role="status">
          原来选中的记录已不在本次运行中，已回到最新记录。
        </p>
      )}

      {loadKey && state.phase !== "loaded" && (
        <div className="run-detail-record__load" role="status">
          {state.phase === "loading" && <><Loader2 size={13} className="animate-spin" aria-hidden="true" />正在读取完整参数与输出…</>}
          {state.phase === "unavailable" && (
            <><AlertCircle size={13} aria-hidden="true" />
              这条调用的完整内容已不可用：会话不再保留它，可能来自子代理或已被清理。{state.message ? `（${state.message}）` : ""}
            </>
          )}
          {state.phase === "error" && <><AlertCircle size={13} aria-hidden="true" />读取完整内容失败：{state.message ?? "未知错误"}</>}
          {state.phase !== "loading" && (
            <button type="button" className="run-detail-chip" aria-label="重新加载" onClick={() => load(true)}>
              <RefreshCw size={12} aria-hidden="true" />重新加载
            </button>
          )}
        </div>
      )}

      {args && (
        <section className="run-detail-block" aria-label="调用参数">
          <div className="run-detail-block__head">
            <span>参数</span>
            <CopyAction text={() => args} label="复制参数" />
          </div>
          <pre className={`run-detail-code${wrap ? " run-detail-code--wrap" : ""}`}>{prettyJson(args)}</pre>
        </section>
      )}

      {event.diff && (
        <section className="run-detail-block" aria-label="文件改动">
          <div className="run-detail-block__head">
            <span>文件改动</span>
          </div>
          <DiffView diff={event.diff} maxHeight={320} />
        </section>
      )}

      {body && (
        <section className="run-detail-block" aria-label="输出">
          <div className="run-detail-block__head">
            <span>输出</span>
            <span className="run-detail-block__tools">
              <input
                type="search"
                className="run-detail-find"
                aria-label="在输出中查找"
                placeholder="在输出中查找"
                value={find}
                onChange={(changed) => setFind(changed.target.value)}
              />
              <span className="run-detail-find__count" aria-live="polite">
                {showFind ? `${countMatches(body, find)} 处匹配` : ""}
              </span>
              <button
                type="button"
                className="run-detail-icon"
                aria-label="自动换行"
                aria-pressed={wrap}
                onClick={() => setWrap(!wrap)}
              >
                <WrapText size={13} />
              </button>
              <CopyAction text={() => body} label="复制输出" />
            </span>
          </div>
          <pre
            ref={bodyRef}
            className={`run-detail-code${wrap ? " run-detail-code--wrap" : ""}`}
            onScroll={(changed) => rememberDetailScroll(detailBodyScrollKey(runId, eventId), changed.currentTarget.scrollTop)}
          >
            {highlightSegments(body, find).map((segment, position) => (
              segment.hit
                ? <mark key={position} className="run-detail-hit">{segment.text}</mark>
                : <span key={position}>{segment.text}</span>
            ))}
          </pre>
          {event.truncated && (
            <p className="run-detail-record__note" role="status">
              输出已被截断展示（后端只保留首尾内容），这不是完整日志。
            </p>
          )}
        </section>
      )}

      {!body && state.phase !== "loading" && (event.archived || event.phase === "result") && (
        <p className="run-detail-record__note" role="status">
          {event.archived && state.phase !== "loaded"
            ? "这条记录的输出未被保留，无法展示——它不等于“无输出”。"
            : "该调用没有输出。"}
        </p>
      )}

      {event.error && (
        <section className="run-detail-block run-detail-block--error" aria-label="错误">
          <div className="run-detail-block__head">
            <span>错误</span>
            <CopyAction text={() => event.error ?? ""} label="复制错误" />
          </div>
          <pre className={`run-detail-code${wrap ? " run-detail-code--wrap" : ""}`}>{event.error}</pre>
        </section>
      )}
    </>
  );
}

function CopyAction({ text, label }: { text: () => string; label: string }) {
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState(false);
  const timer = useRef<number | null>(null);
  useEffect(() => () => { if (timer.current != null) window.clearTimeout(timer.current); }, []);
  return (
    <><button
      type="button"
      className="run-detail-icon"
      aria-label={copied ? `${label}（已复制）` : label}
      onClick={async () => {
        setError(false);
        setCopied(false);
        try {
          await copyText(text());
          setCopied(true);
          if (timer.current != null) window.clearTimeout(timer.current);
          timer.current = window.setTimeout(() => setCopied(false), 1200);
        } catch {
          setError(true);
        }
      }}
    >
      {copied ? <CheckCircle2 size={13} /> : <Copy size={13} />}
    </button>{error && <span role="status">复制失败，请重试</span>}</>
  );
}

async function copyText(value: string): Promise<void> {
  if (typeof navigator !== "undefined" && navigator.clipboard) {
    await navigator.clipboard.writeText(value);
    return;
  }
  throw new Error("clipboard unavailable");
}
