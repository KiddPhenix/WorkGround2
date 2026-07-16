import { FormEvent, type CSSProperties, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowRight, Check, ChevronRight, ChevronUp, Maximize2, MessageSquarePlus, PanelTopOpen, RotateCcw, Send, X } from "lucide-react";
import {
  app,
  type WidgetActionInput,
  type WidgetActionResult,
  type WidgetConversationResult,
  type WidgetMessage,
  type WidgetOption,
  type WidgetSnapshot,
  type WidgetWorkspaceOption,
} from "../../lib/bridge";
import { isComposerSubmitKey, normalizeComposerSubmitKey, type ComposerSubmitKey } from "../../lib/composerKeyboard";
import calibrationRail from "../../assets/widget-mode/calibration-rail.png";
import w2Mark from "../../assets/widget-mode/w2-mark.png";
import shellTopLeft from "../../assets/widget-mode/pager-shell.9/top-left.png";
import shellTop from "../../assets/widget-mode/pager-shell.9/top.png";
import shellTopRight from "../../assets/widget-mode/pager-shell.9/top-right.png";
import shellLeft from "../../assets/widget-mode/pager-shell.9/left.png";
import shellCenter from "../../assets/widget-mode/pager-shell.9/center.png";
import shellRight from "../../assets/widget-mode/pager-shell.9/right.png";
import shellBottomLeft from "../../assets/widget-mode/pager-shell.9/bottom-left.png";
import shellBottom from "../../assets/widget-mode/pager-shell.9/bottom.png";
import shellBottomRight from "../../assets/widget-mode/pager-shell.9/bottom-right.png";
import "./widget-mode.css";

const EMPTY_SNAPSHOT: WidgetSnapshot = {
  mode: true,
  remainingCount: 0,
  runningCount: 0,
  waitingCount: 0,
  completedCount: 0,
  failedCount: 0,
  backgroundCount: 0,
  isIdle: true,
  version: "loading",
};

// IDLE_SUFFIXES are the rotating second-half phrases shown when tasks are
// running but nothing requires user attention. Every entry must be ≤10 Chinese
// characters to fit the 590 px widget without wrapping.
export const IDLE_SUFFIXES = [
  "一切正常",
  "无需回复",
  "一切顺利",
  "无需操作",
  "自动运行中",
  "静待完成",
  "平稳进行中",
  "无需干预",
  "一切安好",
  "安心等待",
  "正常运行",
  "风平浪静",
  "无需挂念",
  "自动巡航",
  "一切就绪",
  "安稳运行",
  "无需操心",
  "进展顺利",
  "一切良好",
  "状态平稳",
  "自动推进",
  "无需照看",
  "运行平稳",
  "一帆风顺",
  "安好勿念",
  "自动作业",
  "无需关注",
  "顺风顺水",
  "一切如常",
  "平静运行",
  "自动处理",
  "无需过问",
  "稳步推进",
  "安然无恙",
  "自动执行",
  "无需在意",
  "平安无事",
  "运行如常",
  "无需协助",
  "一切安稳",
];

// pickIdleSuffix returns a random idle suffix that differs from prev.
// Callers guarantee that IDLE_SUFFIXES has at least 2 entries.
export function pickIdleSuffix(prev: string): string {
  const candidates = IDLE_SUFFIXES.filter((s) => s !== prev);
  if (candidates.length === 0) return IDLE_SUFFIXES[0];
  return candidates[Math.floor(Math.random() * candidates.length)];
}

// validateIdleSuffixes returns suffixes longer than 10 characters — a pure
// function so the constraint is verifiable at build time.
export function validateIdleSuffixes(): string[] {
  return IDLE_SUFFIXES.filter((s) => s.length > 10);
}

const shellTiles = [
  shellTopLeft,
  shellTop,
  shellTopRight,
  shellLeft,
  shellCenter,
  shellRight,
  shellBottomLeft,
  shellBottom,
  shellBottomRight,
];

function requestID(): string {
  return globalThis.crypto?.randomUUID?.() ?? `widget-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function NineSliceShell() {
  return (
    <div className="widget-shell__nine-slice" aria-hidden="true">
      {shellTiles.map((source, index) => <img key={source} src={source} alt="" data-tile={index} />)}
    </div>
  );
}

function TickerText({ children }: { children: string }) {
  const frameRef = useRef<HTMLHeadingElement>(null);
  const textRef = useRef<HTMLSpanElement>(null);
  const [shift, setShift] = useState(0);

  useLayoutEffect(() => {
    const frame = frameRef.current;
    const text = textRef.current;
    if (!frame || !text) return;
    const measure = () => setShift(Math.max(0, Math.ceil(text.scrollWidth - frame.clientWidth)));
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(frame);
    observer?.observe(text);
    return () => observer?.disconnect();
  }, [children]);

  const style = shift > 0 ? {
    "--widget-ticker-shift": `-${shift}px`,
    "--widget-ticker-duration": `${Math.min(22, Math.max(10, 9 + shift / 28))}s`,
  } as CSSProperties : undefined;

  return (
    <h1 ref={frameRef} className={`widget-ticker${shift > 0 ? " widget-ticker--moving" : ""}`}>
      <span ref={textRef} style={style}>{children}</span>
    </h1>
  );
}

function splitWidgetPages(text: string, maxWidth: number, measure: (value: string) => number): string[] {
  if (maxWidth <= 0 || measure(text) <= maxWidth) return [text];
  const tokens = text.split(/(\s+|[，。！？、；：,.!?;:])/u).filter(Boolean);
  const pages: string[] = [];
  let current = "";

  const pushCurrent = () => {
    const value = current.trim();
    if (value) pages.push(value);
    current = "";
  };

  for (const token of tokens) {
    const candidate = current + token;
    if (measure(candidate) <= maxWidth) {
      current = candidate;
      continue;
    }
    pushCurrent();
    if (measure(token.trim()) <= maxWidth) {
      current = token.trimStart();
      continue;
    }
    for (const char of Array.from(token.trim())) {
      if (current && measure(current + char) > maxWidth) pushCurrent();
      current += char;
    }
  }
  pushCurrent();
  return pages.length > 0 ? pages : [text];
}

function PagedText({ children }: { children: string }) {
  const frameRef = useRef<HTMLDivElement>(null);
  const measureRef = useRef<HTMLSpanElement>(null);
  const [pages, setPages] = useState([children]);
  const [page, setPage] = useState(0);

  useLayoutEffect(() => {
    const frame = frameRef.current;
    const measureNode = measureRef.current;
    if (!frame || !measureNode) return;
    let active = true;
    const measure = (value: string) => {
      measureNode.textContent = value;
      return measureNode.scrollWidth;
    };
    const paginate = () => {
      if (!active) return;
      const next = splitWidgetPages(children, Math.max(1, frame.clientWidth - 126), measure);
      setPages(next);
      setPage(0);
    };
    paginate();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(paginate);
    observer?.observe(frame);
    void document.fonts?.ready.then(paginate);
    return () => {
      active = false;
      observer?.disconnect();
    };
  }, [children]);

  const hasNext = page < pages.length - 1;
  const next = () => {
    if (hasNext) setPage((current) => current + 1);
  };

  return (
    <div
      ref={frameRef}
      className={`widget-pager${hasNext ? " widget-pager--more" : ""}`}
      role={hasNext ? "button" : undefined}
      tabIndex={hasNext ? 0 : undefined}
      aria-label={hasNext ? `${pages[page]} 点击显示下一页` : pages[page]}
      data-page-index={page}
      data-page-count={pages.length}
      onClick={next}
      onKeyDown={(event) => {
        if (!hasNext || (event.key !== "Enter" && event.key !== " ")) return;
        event.preventDefault();
        next();
      }}
    >
      <h1><span key={page} className="widget-pager__text">{pages[page]}</span><span ref={measureRef} className="widget-pager__measure" aria-hidden="true" /></h1>
      {hasNext && <span className="widget-pager__next" aria-hidden="true">下一页 <ChevronRight size={17} /></span>}
    </div>
  );
}

function ContextBlock({ message, projectName, taskName }: {
  message?: WidgetMessage;
  projectName?: string;
  taskName?: string;
}) {
  return (
    <section className="widget-context" aria-label="任务上下文">
      <img className="widget-context__rail" src={calibrationRail} alt="" aria-hidden="true" />
      <div className="widget-context__identity">
        <img className="widget-context__mark" src={w2Mark} alt="WorkGround2" />
        <strong className="widget-context__project">{message?.projectName ?? projectName ?? "WorkGround2"}</strong>
        <span className="widget-context__task">{message?.taskName ?? taskName ?? "任务状态"}</span>
      </div>
    </section>
  );
}

function QueueLabel({ snapshot }: { snapshot: WidgetSnapshot }) {
  if (snapshot.remainingCount <= 0) return null;
	return <span className="widget-message__queue" aria-label={`当前消息之后还有 ${snapshot.remainingCount} 条待查看信息`}>还有 {snapshot.remainingCount} 条</span>;
}

function OptionButton({ option, primary, onClick, disabled }: {
  option: WidgetOption;
  primary: boolean;
  onClick: () => void;
  disabled: boolean;
}) {
  return (
    <button
      className={`widget-action${primary ? " widget-action--primary" : ""}`}
      type="button"
      onClick={onClick}
      disabled={disabled}
    >
      <span>{option.label}</span>
      {option.description && <small>{option.description}</small>}
    </button>
  );
}

function workspaceKey(opt: WidgetWorkspaceOption): string {
  if (opt.scope === "auto") return "auto";
  if (opt.scope === "global") return "global";
  return `project:${opt.root ?? ""}`;
}

const AUTO_WORKSPACE: WidgetWorkspaceOption = { scope: "auto", name: "自动" };

function WorkspaceDropdown({ options, selected, onChange, disabled }: {
  options: WidgetWorkspaceOption[];
  selected: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const clickHandler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const keyHandler = (e: KeyboardEvent) => {
      if (e.key === "Escape") { setOpen(false); e.stopPropagation(); }
    };
    document.addEventListener("mousedown", clickHandler);
    document.addEventListener("keydown", keyHandler);
    return () => {
      document.removeEventListener("mousedown", clickHandler);
      document.removeEventListener("keydown", keyHandler);
    };
  }, [open]);

  const selectedOption = options.find((o) => workspaceKey(o) === selected);
  const label = selectedOption?.name ?? "自动";

  return (
    <div className="widget-workspace" ref={ref}>
      <button
        className="widget-workspace__toggle"
        type="button"
        onClick={() => setOpen(!open)}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`工作区选择：${label}`}
      >
        <span className="widget-workspace__value"><strong>{label}</strong><small>工作区</small></span>
        <ChevronUp size={16} />
      </button>
      {open && (
        <ul className="widget-workspace__menu" role="menu" aria-label="选择工作区">
          {options.map((opt) => {
            const key = workspaceKey(opt);
            const isSelected = key === selected;
            return (
              <li key={key} role="none">
                <button
                  className={`widget-workspace__item${isSelected ? " widget-workspace__item--selected" : ""}`}
                  type="button"
                  onClick={() => { onChange(key); setOpen(false); }}
                  role="menuitemradio"
                  aria-checked={isSelected}
                >
                  {isSelected && <Check size={20} className="widget-workspace__check" />}
                  <span className="widget-workspace__label">{opt.name}</span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function IdleStatus({ snapshot, workspaces, selectedWorkspace, onWorkspaceChange, onNew, disabled }: {
  snapshot: WidgetSnapshot;
  workspaces: WidgetWorkspaceOption[];
  selectedWorkspace: string;
  onWorkspaceChange: (value: string) => void;
  onNew: () => void;
  disabled: boolean;
}) {
  const [suffix, setSuffix] = useState(() => pickIdleSuffix(""));
  const prevRunning = useRef(snapshot.runningCount);
  const prevHasCurrent = useRef(snapshot.current != null);

  // Reset suffix when runningCount or current presence changes.
  useEffect(() => {
    const hasCurrent = snapshot.current != null;
    if (prevRunning.current !== snapshot.runningCount || prevHasCurrent.current !== hasCurrent) {
      prevRunning.current = snapshot.runningCount;
      prevHasCurrent.current = hasCurrent;
      if (snapshot.runningCount > 0 && !hasCurrent) {
        setSuffix(pickIdleSuffix(""));
      }
    }
  }, [snapshot.runningCount, snapshot.current]);

  // Rotate suffix every 4–7 seconds when tasks are running and there is no
  // current message. Independent of the 800 ms snapshot poll.
  useEffect(() => {
    if (snapshot.runningCount <= 0 || snapshot.current != null) return;
    const timer = window.setInterval(() => {
      setSuffix((prev) => pickIdleSuffix(prev));
    }, 4000 + Math.floor(Math.random() * 3000));
    return () => window.clearInterval(timer);
  }, [snapshot.runningCount, snapshot.current]);

  const runningText = snapshot.runningCount > 0
    ? `${snapshot.runningCount} 个任务运行中 · ${suffix}`
    : "暂无运行任务 · 无待处理消息";
  const secondary = snapshot.backgroundCount > 0
    ? `${snapshot.backgroundCount} 个后台作业持续运行`
    : snapshot.runningCount > 0 ? "目前没有需要处理的消息" : "没有需要处理的重要信息";
  return (
    <section className="widget-message widget-message--idle" aria-live="polite">
      <div className="widget-message__head">
		<span className="widget-message__state">{snapshot.runningCount > 0 ? "运行中" : "系统在线"}</span>
      </div>
      <TickerText>{runningText}</TickerText>
      <div className="widget-status-line" aria-hidden="true"><span /></div>
      <div className="widget-idle__foot">
        <p>{secondary}</p>
        <WorkspaceDropdown options={workspaces} selected={selectedWorkspace} onChange={onWorkspaceChange} disabled={disabled} />
        <button className="widget-new" type="button" onClick={onNew} disabled={disabled}>
          <MessageSquarePlus size={18} />
          <span><strong>新对话</strong><small>输入任务</small></span>
        </button>
      </div>
    </section>
  );
}

function NewConversation({ prompt, busy, workspaces, selectedWorkspace, onWorkspaceChange, onPrompt, onSubmit, onCancel, submitKey }: {
  prompt: string;
  busy: boolean;
  workspaces: WidgetWorkspaceOption[];
  selectedWorkspace: string;
  onWorkspaceChange: (value: string) => void;
  onPrompt: (value: string) => void;
  onSubmit: (event: FormEvent) => void;
  onCancel: () => void;
  submitKey: ComposerSubmitKey;
}) {
  const selectedName = workspaces.find((option) => workspaceKey(option) === selectedWorkspace)?.name ?? "自动";
  const placeholder = selectedWorkspace === "auto"
    ? "输入任务，系统会自动选择工作区…"
    : `输入任务，将发送到 ${selectedName}…`;
  return (
    <section className="widget-message widget-message--compose" aria-live="polite">
      <div className="widget-message__head"><span className="widget-message__state">新对话</span></div>
      <TickerText>想让 WorkGround2 做什么？</TickerText>
      <div className="widget-message__scan" aria-hidden="true"><span /></div>
      <form className="widget-reply widget-reply--compose" onSubmit={onSubmit}>
        <input
          value={prompt}
          onChange={(event) => onPrompt(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Escape") { onCancel(); return; }
            if (event.key !== "Enter") return;
            if (isComposerSubmitKey(event, submitKey, false)) {
              if (submitKey !== "enter") {
                event.preventDefault();
                event.currentTarget.form?.requestSubmit();
              }
              return;
            }
            event.preventDefault();
          }}
          placeholder={placeholder}
          aria-label="新对话内容"
          autoFocus
          disabled={busy}
        />
        <WorkspaceDropdown options={workspaces} selected={selectedWorkspace} onChange={onWorkspaceChange} disabled={busy} />
        <button className="widget-reply__send" type="submit" disabled={busy || prompt.trim() === ""} aria-label="开始新对话"><Send size={18} /></button>
        <button className="widget-reply__later" type="button" onClick={onCancel} disabled={busy}><X size={15} /> 取消</button>
      </form>
    </section>
  );
}

function RouteNotice({ result, prompt }: { result: WidgetConversationResult; prompt: string }) {
  return (
    <section className="widget-message widget-message--route" aria-live="polite">
      <div className="widget-message__head">
        <span className="widget-message__state">新对话已创建</span>
        <span className="widget-route__reason">{result.routeReason}</span>
      </div>
      <TickerText>{`已交给 ${result.workspaceName || "Global"}`}</TickerText>
      <div className="widget-message__scan" aria-hidden="true"><span /></div>
      <p>正在处理：{prompt}</p>
    </section>
  );
}

export function WidgetMode({ onExit, submitKey }: { onExit: () => void; submitKey?: string }) {
  const [snapshot, setSnapshot] = useState<WidgetSnapshot>(EMPTY_SNAPSHOT);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
	const [composing, setComposing] = useState(false);
	const [newPrompt, setNewPrompt] = useState("");
	const [routeNotice, setRouteNotice] = useState<{ result: WidgetConversationResult; prompt: string } | null>(null);
	const retryRequest = useRef<{ key: string; id: string } | null>(null);
	const conversationRequest = useRef<{ prompt: string; workspace: string; id: string } | null>(null);
	const [workspaces, setWorkspaces] = useState<WidgetWorkspaceOption[]>([AUTO_WORKSPACE]);
	const [selectedWorkspace, setSelectedWorkspace] = useState("auto");
	const composerSubmitKey = normalizeComposerSubmitKey(submitKey);

  const refresh = useCallback(async () => {
    try {
      const next = await app.GetWidgetSnapshot();
      setSnapshot((current) => current.version === next.version ? current : next);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 800);
    return () => window.clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    app.ListWidgetWorkspaces()
      .then((options) => {
        setWorkspaces(options.length > 0 ? options : [AUTO_WORKSPACE]);
      })
      .catch(() => {
        setWorkspaces([AUTO_WORKSPACE]);
        setError("工作区列表加载失败，仍可使用自动选择");
      });
  }, []);

  useEffect(() => {
    setTyped("");
    setError("");
  }, [snapshot.current?.id]);

  const apply = useCallback(async (
    action: WidgetActionInput["action"],
    values: string[] = [],
  ) => {
    const current = snapshot.current;
    if (!current || busy) return;
    setBusy(true);
    setError("");
	const actionKey = `${current.id}:${action}:${values.join("\u0000")}`;
	const actionRequestID = retryRequest.current?.key === actionKey ? retryRequest.current.id : requestID();
    try {
      const result: WidgetActionResult = await app.ApplyWidgetAction({
        itemId: current.id,
        revision: current.revision,
		requestId: actionRequestID,
        action,
        values,
      });
      setSnapshot(result.snapshot);
      if (result.status === "retryable_error" || result.status === "invalid") {
		retryRequest.current = { key: actionKey, id: actionRequestID };
        setError(result.error ?? "操作失败，可以重试");
        return;
      }
	  retryRequest.current = null;
      if (result.status === "stale") {
        setError(result.error ?? "信息已更新");
        return;
      }
      if (action === "open") onExit();
    } catch (cause) {
	  retryRequest.current = { key: actionKey, id: actionRequestID };
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  }, [busy, onExit, snapshot.current]);

  const choose = useCallback((option: WidgetOption) => {
    if (option.value === "allow") void apply("approve");
    else if (option.value === "deny") void apply("deny");
    else void apply("answer", [option.value]);
  }, [apply]);

  const submitTyped = useCallback((event: FormEvent) => {
    event.preventDefault();
    const value = typed.trim();
    if (value) void apply("answer", [value]);
  }, [apply, typed]);

	const startConversation = useCallback(async (event: FormEvent) => {
		event.preventDefault();
		const prompt = newPrompt.trim();
		if (!prompt || busy) return;
		const ws = selectedWorkspace;
		const requestId = conversationRequest.current?.prompt === prompt
			&& conversationRequest.current?.workspace === ws
			? conversationRequest.current.id
			: requestID();
		conversationRequest.current = { prompt, workspace: ws, id: requestId };
		setBusy(true);
		setError("");
		try {
			const result = await app.StartWidgetConversation({ prompt, requestId, workspace: ws });
			setSnapshot(result.snapshot);
			if (result.status === "retryable_error" || result.status === "invalid") {
				setError(result.error ?? "新对话创建失败，可以重试");
				return;
			}
			conversationRequest.current = null;
			setComposing(false);
			setNewPrompt("");
			setRouteNotice({ result, prompt });
			window.setTimeout(() => setRouteNotice((current) => current?.result.tabId === result.tabId ? null : current), 3200);
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setBusy(false);
		}
	}, [busy, newPrompt, selectedWorkspace]);

  const exit = useCallback(async (tabID = "") => {
    if (busy) return;
    setBusy(true);
    try {
      await app.ExitWidgetMode(tabID);
      onExit();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      setBusy(false);
    }
  }, [busy, onExit]);

  const current = snapshot.current;
  const widgetIdle = snapshot.isIdle && !composing && !routeNotice;
  const body = useMemo(() => {
    if (!current && composing) return (
		<NewConversation prompt={newPrompt} busy={busy} workspaces={workspaces} selectedWorkspace={selectedWorkspace} onWorkspaceChange={setSelectedWorkspace} onPrompt={setNewPrompt} onSubmit={startConversation} onCancel={() => setComposing(false)} submitKey={composerSubmitKey} />
	);
	if (!current && routeNotice) return <RouteNotice result={routeNotice.result} prompt={routeNotice.prompt} />;
    if (!current) return <IdleStatus snapshot={snapshot} workspaces={workspaces} selectedWorkspace={selectedWorkspace} onWorkspaceChange={setSelectedWorkspace} onNew={() => { setRouteNotice(null); setComposing(true); }} disabled={busy} />;
    return (
	  <section key={current.id} className={`widget-message widget-message--${current.kind}`} aria-live="polite">
        <div className="widget-message__head">
          <span className="widget-message__state">{current.stateLabel}</span>
          <QueueLabel snapshot={snapshot} />
        </div>
        <PagedText>{current.message}</PagedText>
        <div className="widget-message__scan" aria-hidden="true"><span /></div>

        {current.requiresWindow ? (
          <div className="widget-actions">
            <button className="widget-action widget-action--primary" type="button" onClick={() => void apply("open")} disabled={busy}>
              <span>在窗口中处理</span><small>打开完整上下文</small>
            </button>
          </div>
        ) : current.kind === "choice" ? (
          <div className="widget-actions">
            {current.options.slice(0, 3).map((option, index) => (
              <OptionButton key={option.value} option={option} primary={index === 0} onClick={() => choose(option)} disabled={busy} />
            ))}
			<button className="widget-action widget-action--quiet" type="button" onClick={() => void apply("later")} disabled={busy}>
			  <span>稍后</span>
			</button>
          </div>
        ) : current.kind === "reply" ? (
          <form className="widget-reply" onSubmit={submitTyped}>
            <input
              value={typed}
              onChange={(event) => setTyped(event.target.value)}
			  onKeyDown={(event) => {
				if (event.key === "Escape") { event.currentTarget.blur(); return; }
				if (event.key !== "Enter") return;
				if (isComposerSubmitKey(event, composerSubmitKey, false)) {
				  if (composerSubmitKey !== "enter") {
					event.preventDefault();
					event.currentTarget.form?.requestSubmit();
				  }
				  return;
				}
				event.preventDefault();
			  }}
              placeholder="输入简短回复…"
              aria-label="回复内容"
              autoFocus
              disabled={busy}
            />
			<button className="widget-reply__send" type="submit" disabled={busy || typed.trim() === ""} aria-label="发送回复"><Send size={18} /></button>
			<button className="widget-reply__later" type="button" onClick={() => void apply("later")} disabled={busy}>稍后</button>
          </form>
        ) : current.kind === "result" ? (
          <div className="widget-actions widget-actions--result">
			<button className="widget-action widget-action--primary" type="button" onClick={() => void apply("next")} disabled={busy}>
              <span>下一条</span><ArrowRight size={16} />
            </button>
			<button className="widget-action" type="button" onClick={() => void apply("open")} disabled={busy}>
			  <span>查看结果</span><Maximize2 size={16} />
			</button>
          </div>
        ) : (
          <div className="widget-actions widget-actions--result">
            <button className="widget-action widget-action--primary" type="button" onClick={() => void apply("retry")} disabled={busy}>
              <span>重试</span><RotateCcw size={16} />
            </button>
            <button className="widget-action" type="button" onClick={() => void apply("open")} disabled={busy}>
              <span>查看详情</span><Maximize2 size={16} />
            </button>
          </div>
        )}
      </section>
    );
  }, [apply, busy, choose, composing, composerSubmitKey, current, newPrompt, routeNotice, selectedWorkspace, snapshot, startConversation, submitTyped, typed, workspaces]);

	const contextProject = routeNotice?.result.workspaceName;
	const contextTask = composing || routeNotice ? "新对话" : undefined;

  return (
    <main className={`widget-mode${widgetIdle ? " widget-mode--idle" : ""}`}>
      <div className="widget-shell">
        <NineSliceShell />
        <div className="widget-shell__drag" aria-hidden="true" />
        <ContextBlock message={current} projectName={contextProject} taskName={contextTask} />
        {body}
        <button className="widget-return" type="button" onClick={() => void exit(current?.kind === "result" ? current.tabId : "")} disabled={busy} aria-label="返回主窗口">
          <span className="widget-return__icon"><PanelTopOpen size={18} strokeWidth={1.8} /></span>
          <span className="widget-return__copy"><strong>主窗口</strong><small>FULL VIEW</small></span>
        </button>
        {error && <div className="widget-error" role="alert">{error}</div>}
      </div>
    </main>
  );
}
