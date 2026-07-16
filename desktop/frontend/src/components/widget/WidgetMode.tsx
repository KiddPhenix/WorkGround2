import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowRight, Maximize2, MessageSquarePlus, PanelTopOpen, RotateCcw, Send, X } from "lucide-react";
import {
  app,
  type WidgetActionInput,
  type WidgetActionResult,
  type WidgetConversationResult,
  type WidgetMessage,
  type WidgetOption,
  type WidgetSnapshot,
} from "../../lib/bridge";
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
  version: "loading",
};

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

function IdleStatus({ snapshot, onNew, disabled }: {
  snapshot: WidgetSnapshot;
  onNew: () => void;
  disabled: boolean;
}) {
	const runningText = snapshot.runningCount > 0
	  ? `${snapshot.runningCount} 个任务运行中 · 无待处理消息`
	  : "暂无运行任务 · 无待处理消息";
  const secondary = snapshot.backgroundCount > 0
    ? `${snapshot.backgroundCount} 个后台作业持续运行`
    : "没有需要处理的重要信息";
  return (
    <section className="widget-message widget-message--idle" aria-live="polite">
      <div className="widget-message__head">
		<span className="widget-message__state">{snapshot.runningCount > 0 ? "运行中" : "系统在线"}</span>
      </div>
      <h1>{runningText}</h1>
      <div className="widget-status-line" aria-hidden="true"><span /></div>
      <div className="widget-idle__foot">
        <p>{secondary}</p>
        <button className="widget-new" type="button" onClick={onNew} disabled={disabled}>
          <MessageSquarePlus size={18} />
          <span><strong>新对话</strong><small>自动选择工作区</small></span>
        </button>
      </div>
    </section>
  );
}

function NewConversation({ prompt, busy, onPrompt, onSubmit, onCancel }: {
  prompt: string;
  busy: boolean;
  onPrompt: (value: string) => void;
  onSubmit: (event: FormEvent) => void;
  onCancel: () => void;
}) {
  return (
    <section className="widget-message widget-message--compose" aria-live="polite">
      <div className="widget-message__head"><span className="widget-message__state">新对话</span></div>
      <h1>想让 WorkGround2 做什么？</h1>
      <div className="widget-message__scan" aria-hidden="true"><span /></div>
      <form className="widget-reply widget-reply--compose" onSubmit={onSubmit}>
        <input
          value={prompt}
          onChange={(event) => onPrompt(event.target.value)}
          onKeyDown={(event) => { if (event.key === "Escape") onCancel(); }}
          placeholder="输入任务，系统会自动选择工作区…"
          aria-label="新对话内容"
          autoFocus
          disabled={busy}
        />
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
      <h1>已交给 {result.workspaceName || "Global"}</h1>
      <div className="widget-message__scan" aria-hidden="true"><span /></div>
      <p>正在处理：{prompt}</p>
    </section>
  );
}

export function WidgetMode({ onExit }: { onExit: () => void }) {
  const [snapshot, setSnapshot] = useState<WidgetSnapshot>(EMPTY_SNAPSHOT);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
	const [composing, setComposing] = useState(false);
	const [newPrompt, setNewPrompt] = useState("");
	const [routeNotice, setRouteNotice] = useState<{ result: WidgetConversationResult; prompt: string } | null>(null);
	const retryRequest = useRef<{ key: string; id: string } | null>(null);
	const conversationRequest = useRef<{ prompt: string; id: string } | null>(null);

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
		const requestId = conversationRequest.current?.prompt === prompt
			? conversationRequest.current.id
			: requestID();
		conversationRequest.current = { prompt, id: requestId };
		setBusy(true);
		setError("");
		try {
			const result = await app.StartWidgetConversation({ prompt, requestId });
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
	}, [busy, newPrompt]);

  const exit = useCallback(async () => {
    if (busy) return;
    setBusy(true);
    try {
      await app.ExitWidgetMode("");
      onExit();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      setBusy(false);
    }
  }, [busy, onExit]);

  const current = snapshot.current;
  const body = useMemo(() => {
    if (!current && composing) return (
		<NewConversation prompt={newPrompt} busy={busy} onPrompt={setNewPrompt} onSubmit={startConversation} onCancel={() => setComposing(false)} />
	);
	if (!current && routeNotice) return <RouteNotice result={routeNotice.result} prompt={routeNotice.prompt} />;
    if (!current) return <IdleStatus snapshot={snapshot} onNew={() => { setRouteNotice(null); setComposing(true); }} disabled={busy} />;
    return (
	  <section key={current.id} className={`widget-message widget-message--${current.kind}`} aria-live="polite">
        <div className="widget-message__head">
          <span className="widget-message__state">{current.stateLabel}</span>
          <QueueLabel snapshot={snapshot} />
        </div>
        <h1>{current.message}</h1>
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
				if (event.key === "Escape") event.currentTarget.blur();
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
  }, [apply, busy, choose, composing, current, newPrompt, routeNotice, snapshot, startConversation, submitTyped, typed]);

	const contextProject = routeNotice?.result.workspaceName;
	const contextTask = composing || routeNotice ? "新对话" : undefined;

  return (
    <main className="widget-mode">
      <div className="widget-shell">
        <NineSliceShell />
        <div className="widget-shell__drag" aria-hidden="true" />
        <ContextBlock message={current} projectName={contextProject} taskName={contextTask} />
        {body}
        <button className="widget-return" type="button" onClick={() => void exit()} disabled={busy} aria-label="返回主窗口">
          <span className="widget-return__icon"><PanelTopOpen size={18} strokeWidth={1.8} /></span>
          <span className="widget-return__copy"><strong>主窗口</strong><small>FULL VIEW</small></span>
        </button>
        {error && <div className="widget-error" role="alert">{error}</div>}
      </div>
    </main>
  );
}
