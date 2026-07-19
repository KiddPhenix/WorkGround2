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
import { useI18n, useT, type Translator } from "../../lib/i18n";
import { pickWidgetSuffix, widgetSuffixes } from "./widgetCopy";
import { startWidgetConversationWithRetry } from "./startWidgetConversation";
import { resolveWidgetSkin, widgetSkinTiles, type WidgetSkinId } from "./widgetSkins";
import { resolveWidgetZoomFrame } from "./widgetZoom";
import { WidgetInfoCarousel } from "./WidgetInfoCarousel";
import "./widget-mode.css";
import "./widget-skins.css";

const EMPTY_SNAPSHOT: WidgetSnapshot = {
  mode: true,
  remainingCount: 0,
  runningCount: 0,
  waitingCount: 0,
  completedCount: 0,
  failedCount: 0,
  backgroundCount: 0,
  isIdle: true,
  info: {
    totalTokens: 0,
    tokenPartial: false,
    system: { available: false, network: "unknown", cpu: 0, memory: 0 },
    models: [],
  },
  version: "loading",
};

function NineSliceShell({ skin }: { skin: string }) {
  const tiles = widgetSkinTiles(skin);
  return (
    <div className="widget-shell__nine-slice" aria-hidden="true">
      {tiles.map((source, index) => <img key={`${index}:${source}`} src={source} alt="" data-tile={index} />)}
    </div>
  );
}

function requestID(): string {
  return globalThis.crypto?.randomUUID?.() ?? `widget-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function workspaceLabel(option: WidgetWorkspaceOption | undefined, t: Translator): string {
  if (!option || option.scope === "auto") return t("common.auto");
  return option.name;
}

function stateLabel(message: WidgetMessage, t: Translator): string {
  const code = message.stateCode
    ?? (message.id.startsWith("approval:") ? "confirm"
      : message.id.startsWith("ask:") ? "reply"
        : message.id.startsWith("error:") ? "action"
          : message.id.startsWith("result:") ? "complete" : undefined);
  switch (code) {
    case "confirm": return t("widget.stateConfirm");
    case "reply": return t("widget.stateReply");
    case "action": return t("widget.stateAction");
    case "complete": return t("widget.stateComplete");
    default: return message.stateLabel;
  }
}

function messageText(message: WidgetMessage, t: Translator): string {
  if (message.messageCode === "complete_fallback" || message.message === "执行已完成，结果可以查看。") {
    return t("widget.completeFallback");
  }
  if (message.messageCode === "multi_question") {
    return t("widget.multiQuestion", { count: message.messageCount ?? 0 });
  }
  const legacyMulti = /^需要回答\s+(\d+)\s+个问题，请在主窗口继续。$/.exec(message.message);
  return legacyMulti ? t("widget.multiQuestion", { count: Number(legacyMulti[1]) }) : message.message;
}

function routeReason(result: WidgetConversationResult, t: Translator): string {
  const legacy: Record<string, WidgetConversationResult["routeReasonCode"]> = {
    "Global 兜底": "global_fallback",
    "最近使用": "recent",
    "名称匹配": "name_match",
    "历史上下文": "history",
    "当前工作区": "current",
    "主工作区": "primary",
    "手动选择": "manual",
  };
  switch (result.routeReasonCode ?? legacy[result.routeReason ?? ""]) {
    case "global_fallback": return t("widget.routeGlobalFallback");
    case "recent": return t("widget.routeRecent");
    case "name_match": return t("widget.routeNameMatch");
    case "history": return t("widget.routeHistory");
    case "current": return t("widget.routeCurrent");
    case "primary": return t("widget.routePrimary");
    case "manual": return t("widget.routeManual");
    default: return result.routeReason ?? "";
  }
}

function optionCopy(option: WidgetOption, message: WidgetMessage, t: Translator): WidgetOption {
  const code = option.code ?? (message.id.startsWith("approval:") && (option.value === "allow" || option.value === "deny") ? option.value : undefined);
  if (code === "allow") return { ...option, label: t("widget.approvalAllow"), description: t("widget.approvalAllowDesc") };
  if (code === "deny") return { ...option, label: t("widget.approvalDeny"), description: t("widget.approvalDenyDesc") };
  return option;
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
  const t = useT();
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
      aria-label={hasNext ? t("widget.pageAria", { text: pages[page] }) : pages[page]}
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
      {hasNext && <span className="widget-pager__next" aria-hidden="true">{t("widget.pageNext")} <ChevronRight size={17} /></span>}
    </div>
  );
}

function QueueLabel({ snapshot }: { snapshot: WidgetSnapshot }) {
  const t = useT();
  if (snapshot.remainingCount <= 0) return null;
	return <span className="widget-message__queue" aria-label={t("widget.queueAria", { count: snapshot.remainingCount })}>{t("widget.queueMore", { count: snapshot.remainingCount })}</span>;
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

const AUTO_WORKSPACE: WidgetWorkspaceOption = { scope: "auto", name: "" };

function WorkspaceDropdown({ options, selected, onChange, disabled }: {
  options: WidgetWorkspaceOption[];
  selected: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const t = useT();
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
  const label = workspaceLabel(selectedOption, t);

  return (
    <div className="widget-workspace" ref={ref}>
      <button
        className="widget-workspace__toggle"
        type="button"
        onClick={() => setOpen(!open)}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("widget.workspaceSelectAria", { name: label })}
      >
        <span className="widget-workspace__value"><strong>{label}</strong><small>{t("widget.workspaceLabel")}</small></span>
        <ChevronUp size={16} />
      </button>
      {open && (
        <ul className="widget-workspace__menu" role="menu" aria-label={t("widget.workspaceMenu")}>
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
                  <span className="widget-workspace__label">{workspaceLabel(opt, t)}</span>
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
  const { t, locale } = useI18n();
  const suffixes = widgetSuffixes(locale);
  const [suffix, setSuffix] = useState(() => pickWidgetSuffix(suffixes));
  const prevRunning = useRef(snapshot.runningCount);
  const prevHasCurrent = useRef(snapshot.current != null);

  // Reset suffix when runningCount or current presence changes.
  useEffect(() => {
    const hasCurrent = snapshot.current != null;
    if (prevRunning.current !== snapshot.runningCount || prevHasCurrent.current !== hasCurrent) {
      prevRunning.current = snapshot.runningCount;
      prevHasCurrent.current = hasCurrent;
      if (snapshot.runningCount > 0 && !hasCurrent) {
        setSuffix(pickWidgetSuffix(suffixes));
      }
    }
  }, [locale, snapshot.runningCount, snapshot.current, suffixes]);

  // Rotate suffix every 4–7 seconds when tasks are running and there is no
  // current message. Independent of the 800 ms snapshot poll.
  useEffect(() => {
    if (snapshot.runningCount <= 0 || snapshot.current != null) return;
    const timer = window.setInterval(() => {
      setSuffix((prev) => pickWidgetSuffix(suffixes, prev));
    }, 4000 + Math.floor(Math.random() * 3000));
    return () => window.clearInterval(timer);
  }, [locale, snapshot.runningCount, snapshot.current, suffixes]);

  const runningText = snapshot.runningCount > 0
    ? t(snapshot.runningCount === 1 ? "widget.runningOne" : "widget.runningMany", { count: snapshot.runningCount, status: suffix })
    : t("widget.noTasks");
  const secondary = snapshot.backgroundCount > 0
    ? t(snapshot.backgroundCount === 1 ? "widget.backgroundOne" : "widget.backgroundMany", { count: snapshot.backgroundCount })
    : snapshot.runningCount > 0 ? t("widget.noMessages") : t("widget.noImportantMessages");
  return (
    <section className="widget-message widget-message--idle" aria-live="polite">
      <div className="widget-message__head">
		<span className="widget-message__state">{t(snapshot.runningCount > 0 ? "widget.stateRunning" : "widget.stateOnline")}</span>
      </div>
      <TickerText>{runningText}</TickerText>
      <div className="widget-status-line" aria-hidden="true"><span /></div>
      <div className="widget-idle__foot">
        <p>{secondary}</p>
        <WorkspaceDropdown options={workspaces} selected={selectedWorkspace} onChange={onWorkspaceChange} disabled={disabled} />
        <button className="widget-new" type="button" onClick={onNew} disabled={disabled}>
          <MessageSquarePlus size={18} />
          <span><strong>{t("widget.newConversation")}</strong><small>{t("widget.enterTask")}</small></span>
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
  const t = useT();
  const selectedName = workspaceLabel(workspaces.find((option) => workspaceKey(option) === selectedWorkspace), t);
  const placeholder = selectedWorkspace === "auto"
    ? t("widget.composePlaceholderAuto")
    : t("widget.composePlaceholderWorkspace", { name: selectedName });
  return (
    <section className="widget-message widget-message--compose" aria-live="polite">
      <div className="widget-message__head"><span className="widget-message__state">{t("widget.newConversation")}</span></div>
      <TickerText>{t("widget.composePrompt")}</TickerText>
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
          aria-label={t("widget.composeAria")}
          autoFocus
          disabled={busy}
        />
        <WorkspaceDropdown options={workspaces} selected={selectedWorkspace} onChange={onWorkspaceChange} disabled={busy} />
        <button className="widget-reply__send" type="submit" disabled={busy || prompt.trim() === ""} aria-label={t("widget.startConversation")}><Send size={18} /></button>
        <button className="widget-reply__later" type="button" onClick={onCancel} disabled={busy}><X size={15} /> {t("common.cancel")}</button>
      </form>
    </section>
  );
}

function RouteNotice({ result, prompt }: { result: WidgetConversationResult; prompt: string }) {
  const t = useT();
  return (
    <section className="widget-message widget-message--route" aria-live="polite">
      <div className="widget-message__head">
        <span className="widget-message__state">{t("widget.conversationCreated")}</span>
        <span className="widget-route__reason">{routeReason(result, t)}</span>
      </div>
      <TickerText>{t("widget.assignedTo", { name: result.workspaceName || "Global" })}</TickerText>
      <div className="widget-message__scan" aria-hidden="true"><span /></div>
      <p>{t("widget.processing", { prompt })}</p>
    </section>
  );
}

export function WidgetMode({ onExit, submitKey }: { onExit: () => void; submitKey?: string }) {
  const t = useT();
  const [skinId, setSkinId] = useState<WidgetSkinId>("classic");
  const [desktopZoom, setDesktopZoom] = useState(1);
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

  useEffect(() => {
    let alive = true;
    void app.DesktopStartupSettings()
      .then((settings) => {
        if (alive) setSkinId(resolveWidgetSkin(settings.widgetSkin));
      })
      .catch(() => undefined);
    const unsubscribe = typeof window !== "undefined" && window.runtime
      ? window.runtime.EventsOn("widget:skin", (payload: unknown) => {
        setSkinId(resolveWidgetSkin(typeof payload === "string" ? payload : "classic"));
      })
      : undefined;
    return () => {
      alive = false;
      unsubscribe?.();
    };
  }, []);

  useEffect(() => {
    let alive = true;
    void app.GetDesktopZoomFactor()
      .then((zoom) => {
        if (alive) setDesktopZoom(resolveWidgetZoomFrame(zoom).zoom);
      })
      .catch(() => {
        if (alive) setDesktopZoom(1);
      });
    return () => {
      alive = false;
    };
  }, []);

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
        setError(t("widget.workspaceLoadFailed"));
      });
  }, [t]);

  // Update native window region on resize so the four transparent corners stay
  // accurate.  Debounced — the Go-side SetWindowRgn is cheap but CreatePolygonRgn
  // on every pixel is unnecessary.
  const regionTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    const refreshRegion = () => {
      if (regionTimer.current !== null) window.clearTimeout(regionTimer.current);
      regionTimer.current = window.setTimeout(() => {
        regionTimer.current = null;
        void app.RefreshWidgetWindowRegion().catch((cause) => {
          setError(cause instanceof Error ? cause.message : String(cause));
        });
      }, 120);
    };
    refreshRegion();
    window.addEventListener("resize", refreshRegion);
    return () => {
      window.removeEventListener("resize", refreshRegion);
      if (regionTimer.current !== null) window.clearTimeout(regionTimer.current);
    };
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
        setError(result.error ?? t("widget.actionFailed"));
        return;
      }
	  retryRequest.current = null;
      if (result.status === "stale") {
        setError(result.error ?? t("widget.infoUpdated"));
        return;
      }
      if (action === "open") onExit();
    } catch (cause) {
	  retryRequest.current = { key: actionKey, id: actionRequestID };
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  }, [busy, onExit, snapshot.current, t]);

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
			const input = { prompt, requestId, workspace: ws };
			const result = await startWidgetConversationWithRetry(app.StartWidgetConversation, input);
			setSnapshot(result.snapshot);
			if (result.status === "retryable_error" || result.status === "invalid") {
				setError(result.error ?? t("widget.conversationFailed"));
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
	}, [busy, newPrompt, selectedWorkspace, t]);

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
          <span className="widget-message__state">{stateLabel(current, t)}</span>
          <QueueLabel snapshot={snapshot} />
        </div>
        <PagedText>{messageText(current, t)}</PagedText>
        <div className="widget-message__scan" aria-hidden="true"><span /></div>

        {current.requiresWindow ? (
          <div className="widget-actions">
            <button className="widget-action widget-action--primary" type="button" onClick={() => void apply("open")} disabled={busy}>
              <span>{t("widget.handleInWindow")}</span><small>{t("widget.openFullContext")}</small>
            </button>
          </div>
        ) : current.kind === "choice" ? (
          <div className="widget-actions">
            {current.options.slice(0, 3).map((option, index) => (
              <OptionButton key={option.value} option={optionCopy(option, current, t)} primary={index === 0} onClick={() => choose(option)} disabled={busy} />
            ))}
			<button className="widget-action widget-action--quiet" type="button" onClick={() => void apply("later")} disabled={busy}>
			  <span>{t("widget.later")}</span>
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
              placeholder={t("widget.replyPlaceholder")}
              aria-label={t("widget.replyAria")}
              autoFocus
              disabled={busy}
            />
			<button className="widget-reply__send" type="submit" disabled={busy || typed.trim() === ""} aria-label={t("widget.sendReply")}><Send size={18} /></button>
			<button className="widget-reply__later" type="button" onClick={() => void apply("later")} disabled={busy}>{t("widget.later")}</button>
          </form>
        ) : current.kind === "result" ? (
          <div className="widget-actions widget-actions--result">
			<button className="widget-action widget-action--primary" type="button" onClick={() => void apply("next")} disabled={busy}>
			  <span>{t("widget.next")}</span><ArrowRight size={16} />
            </button>
			<button className="widget-action" type="button" onClick={() => void apply("open")} disabled={busy}>
			  <span>{t("widget.viewResult")}</span><Maximize2 size={16} />
			</button>
          </div>
        ) : (
          <div className="widget-actions widget-actions--result">
            <button className="widget-action widget-action--primary" type="button" onClick={() => void apply("retry")} disabled={busy}>
              <span>{t("common.retry")}</span><RotateCcw size={16} />
            </button>
            <button className="widget-action" type="button" onClick={() => void apply("open")} disabled={busy}>
              <span>{t("widget.viewDetails")}</span><Maximize2 size={16} />
            </button>
          </div>
        )}
      </section>
    );
  }, [apply, busy, choose, composing, composerSubmitKey, current, newPrompt, routeNotice, selectedWorkspace, snapshot, startConversation, submitTyped, t, typed, workspaces]);

	const contextProject = routeNotice?.result.workspaceName;
	const contextTask = composing || routeNotice ? t("widget.newConversation") : undefined;
  const zoomFrame = resolveWidgetZoomFrame(desktopZoom);
  const zoomStyle: CSSProperties = {
    width: `${zoomFrame.widthVw}vw`,
    height: `${zoomFrame.heightVh}vh`,
    transform: `scale(${zoomFrame.scale})`,
    transformOrigin: "left top",
  };

  return (
    <main
      className={`widget-mode${widgetIdle ? " widget-mode--idle" : ""}`}
      data-widget-skin={skinId}
      data-widget-zoom={zoomFrame.zoom}
      style={zoomStyle}
    >
      <div className="widget-shell">
        <NineSliceShell skin={skinId} />
        <div className="widget-shell__drag" aria-hidden="true" />
		<WidgetInfoCarousel snapshot={snapshot} message={current} projectName={contextProject} taskName={contextTask} skin={skinId} />
        {body}
        <button className="widget-return" type="button" onClick={() => void exit(current?.kind === "result" ? current.tabId : "")} disabled={busy} aria-label={t("widget.returnMain")}>
          <span className="widget-return__icon"><PanelTopOpen size={18} strokeWidth={1.8} /></span>
          <span className="widget-return__copy"><strong>{t("widget.mainWindow")}</strong><small>{t("widget.fullView")}</small></span>
        </button>
        {error && <div className="widget-error" role="alert">{error}</div>}
      </div>
    </main>
  );
}
