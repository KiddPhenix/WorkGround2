import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from "react";
import { BookOpen, Bot, Check, CircleAlert, CornerUpRight, HelpCircle, MessageCircle, Plus, Search, Users, X } from "lucide-react";
import { app, type DesktopIconActionInput, type DesktopIconItem, type DesktopIconNotice, type DesktopIconPosition, type DesktopIconSearchItem, type DesktopIconSnapshot, type WidgetWorkspaceOption } from "../../lib/bridge";
import { iconHitRect, placeIconPopup, quickStartWorkspaceIndex, scaleIconRect } from "./desktopIconLayout";
import { resolveWidgetZoomFrame } from "./widgetZoom";
import "./desktop-icon-mode.css";

const CLICK_DELAY = 240;
const DRAG_THRESHOLD = 7;
const QUICK_WORKSPACE_KEY = "wg2.icon-widget-workspace";

function nativeHitPadding(node: HTMLElement): number {
	if (node.matches(".desktop-icon-popup")) return 40;
	if (node.matches(".desktop-icon-menu, .desktop-icon-toast")) return 30;
	if (node.matches(".desktop-icon")) return 20;
	return 8;
}

function widgetWorkspaceKey(option: WidgetWorkspaceOption): string {
	return option.scope === "auto" ? "auto" : option.scope === "global" ? "global" : `project:${option.root}`;
}

function requestID(prefix: string): string {
  return `${prefix}:${typeof crypto !== "undefined" && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`}`;
}

function statusGlyph(item: DesktopIconItem) {
  if (item.status === "done") return <Check aria-hidden="true" />;
  if (item.status === "failed") return <CircleAlert aria-hidden="true" />;
  if (item.status === "needs_input") return <HelpCircle aria-hidden="true" />;
  if (item.status === "needs_confirm") return <CircleAlert aria-hidden="true" />;
  return null;
}

function itemGlyph(item: DesktopIconItem) {
  if (item.kind === "room") return <MessageCircle />;
  if (item.kind === "person") return <Users />;
  if (item.kind === "task") return <Bot />;
  if (item.kind === "workspace") return <span className="desktop-icon__letter">{item.title.slice(0, 1).toUpperCase()}</span>;
  if (item.sourceId === "new") return <Plus />;
  if (item.sourceId === "delegate") return <Users />;
  if (item.sourceId === "knowledge") return <BookOpen />;
  return <Search />;
}

function previewText(item: DesktopIconItem): string {
  if (item.runtimeStatus) return `${item.runtimeStatus.phase} · ${Math.max(0, Math.round(item.runtimeStatus.elapsedMs / 1000))} 秒`;
  if (item.unreadCount > 0) return `${item.unreadCount} 条待处理信息`;
  if (item.kind === "workspace") return `${item.title} · 快速发起`;
  if (item.status === "done") return item.subtitle || "已完成，可在搜索中找到记录";
  return item.title;
}

function NoticeBody({ item, notice, busy, run }: { item: DesktopIconItem; notice: DesktopIconNotice; busy: boolean; run: (action: string, values?: string[]) => void }) {
  const [answer, setAnswer] = useState("");
	const [selected, setSelected] = useState("");
	const [reply, setReply] = useState("");
  const needsAnswer = notice.kind === "needs_input";
  const completion = notice.kind === "completed" || notice.kind === "failed";
  return <>
    <div className="desktop-icon-popup__eyebrow">{notice.title}</div>
    <strong>{item.title}</strong>
    <p>{notice.body}</p>
    {needsAnswer && <div className="desktop-icon-popup__answers">
		{notice.options.map((option) => <button key={option.value} type="button" aria-pressed={selected === option.value} disabled={busy} onClick={() => { setSelected(option.value); setAnswer(""); }}><span>{option.label}</span>{option.description && <small>{option.description}</small>}</button>)}
      <label><span className="sr-only">自定义回答</span><input value={answer} disabled={busy} placeholder="自定义回答" onChange={(event) => setAnswer(event.target.value)} /></label>
    </div>}
	{notice.kind === "message" && (item.kind === "room" || item.kind === "person") && <label className="desktop-icon-popup__reply"><span className="sr-only">快速回复</span><input value={reply} disabled={busy} placeholder="快速回复" onChange={(event) => setReply(event.target.value)} /></label>}
    <div className="desktop-icon-popup__actions">
		{needsAnswer && <button disabled={busy || !(answer.trim() || selected)} onClick={() => run("answer", [answer.trim() || selected])}>提交回答</button>}
      {notice.kind === "needs_confirm" && <><button disabled={busy} onClick={() => run("approve")}>允许</button><button disabled={busy} onClick={() => run("deny")}>拒绝</button></>}
      {notice.kind === "failed" && notice.retryable && <button disabled={busy} onClick={() => run("retry")}>重试</button>}
      {completion && <><button disabled={busy} onClick={() => run("ok")}>OK</button><button disabled={busy} className="subtle" onClick={() => run("dismiss")}>Dismiss</button></>}
      {(needsAnswer || notice.kind === "needs_confirm") && <button disabled={busy} className="subtle" onClick={() => run("later")}>稍后处理</button>}
		{notice.kind === "message" && (item.kind === "room" || item.kind === "person") && <button disabled={busy || !reply.trim()} onClick={() => run("reply", [reply.trim()])}>回复</button>}
		{notice.kind === "message" && <button disabled={busy} onClick={() => run("open")}>打开会话</button>}
    </div>
    {completion && <small className="desktop-icon-popup__history">记录仍可在搜索中找到</small>}
  </>;
}

function RuntimeBody({ item, busy, run }: { item: DesktopIconItem; busy: boolean; run: (action: string) => void }) {
  const runtime = item.runtimeStatus;
  return <>
    <div className="desktop-icon-popup__eyebrow">{item.status === "thinking" ? "Thinking" : "运行中"}</div>
    <strong>{item.title}</strong>
    <p>{runtime?.summary || "正在执行当前任务"}</p>
    <div className="desktop-icon-popup__facts"><span>{runtime?.phase || "Running"}</span><span>{Math.max(0, Math.round((runtime?.elapsedMs || 0) / 1000))} 秒</span><span>{item.subtitle}</span></div>
    <ol className="desktop-icon-popup__stages"><li className="done">已读取当前 Workspace</li><li className="active">{runtime?.summary || "正在处理"}</li><li>即将组织结果</li></ol>
    <div className="desktop-icon-popup__actions"><button disabled={busy} onClick={() => run("open")}>打开任务</button><button disabled={busy} className="danger" onClick={() => run("stop")}>停止</button></div>
  </>;
}

function QuickStart({ workspaces, initialWorkspace = "", onClose }: { workspaces: WidgetWorkspaceOption[]; initialWorkspace?: string; onClose: () => void }) {
  const choices = workspaces.length ? workspaces : [{ scope: "auto", name: "自动" } as WidgetWorkspaceOption];
	const [pending, setPendingState] = useState<{ id: string; prompt: string; workspace: string } | null>(() => {
		try { return JSON.parse(localStorage.getItem("wg2.icon-widget-pending") || "null"); } catch { return null; }
	});
	const keys = useMemo(() => choices.map(widgetWorkspaceKey), [workspaces]);
	const keysToken = keys.join("\n");
	const initializedKeys = useRef(keysToken);
	const [index, setIndex] = useState(() => quickStartWorkspaceIndex(keys, pending?.workspace, initialWorkspace, localStorage.getItem(QUICK_WORKSPACE_KEY) || ""));
	const [draft, setDraft] = useState(() => localStorage.getItem("wg2.icon-widget-draft") || "");
	const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const choice = choices[index % choices.length];
  const workspace = widgetWorkspaceKey(choice);
	const setPending = (next: { id: string; prompt: string; workspace: string } | null) => { setPendingState(next); if (next) localStorage.setItem("wg2.icon-widget-pending", JSON.stringify(next)); else localStorage.removeItem("wg2.icon-widget-pending"); };
	useEffect(() => {
		if (!pending) return;
		const pendingIndex = keys.indexOf(pending.workspace);
		if (pendingIndex >= 0) setIndex(pendingIndex);
	}, [keys, pending]);
	useEffect(() => {
		if (initializedKeys.current === keysToken) return;
		initializedKeys.current = keysToken;
		setIndex(quickStartWorkspaceIndex(keys, pending?.workspace, initialWorkspace, localStorage.getItem(QUICK_WORKSPACE_KEY) || ""));
	}, [initialWorkspace, keys, keysToken, pending]);
	useEffect(() => { if (workspaces.length) localStorage.setItem(QUICK_WORKSPACE_KEY, workspace); }, [workspace, workspaces.length]);
	const switchBy = (delta: number) => { setIndex((current) => (current + delta + choices.length) % choices.length); setPending(null); };
	useEffect(() => {
		let frame = 0;
		let previous = { lt: false, rt: false };
		const poll = () => {
			const pad = typeof navigator.getGamepads === "function" ? Array.from(navigator.getGamepads()).find(Boolean) : null;
			const lt = Boolean(pad?.buttons[6]?.pressed), rt = Boolean(pad?.buttons[7]?.pressed);
			if ((lt && !previous.lt) || (rt && !previous.rt)) {
				const delta = lt ? -1 : 1;
				setIndex((current) => (current + delta + choices.length) % choices.length);
				setPendingState(null);
				localStorage.removeItem("wg2.icon-widget-pending");
			}
			previous = { lt, rt };
			frame = requestAnimationFrame(poll);
		};
		frame = requestAnimationFrame(poll);
		return () => cancelAnimationFrame(frame);
	}, [choices.length]);
  const saveDraft = (text: string) => {
		setDraft(text);
		setPending(null);
		localStorage.setItem("wg2.icon-widget-draft", text);
  };
  const send = async () => {
    const prompt = draft.trim();
    if (!prompt) return;
    const attempt = pending && pending.prompt === prompt && pending.workspace === workspace ? pending : { id: requestID("icon-new"), prompt, workspace };
		setPending(attempt); setBusy(true); setError("");
    try {
      const result = await app.StartWidgetConversation({ prompt, workspace, requestId: attempt.id });
		if (result.status === "accepted" || result.status === "already_applied") { saveDraft(""); setPending(null); onClose(); }
      else setError(result.error || "发起失败，可安全重试");
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
		finally { setBusy(false); }
  };
  return <div className="desktop-icon-popup__quick" onKeyDown={(event) => {
    if (event.key === "Escape") onClose();
    if (event.key === "PageUp") switchBy(-1);
    if (event.key === "PageDown") switchBy(1);
  }}>
    <div className="desktop-icon-popup__workspace"><button aria-label="上一个 Workspace（LT）" onClick={() => switchBy(-1)}>LT</button><strong>{choice.name} · {index + 1} / {choices.length}</strong><button aria-label="下一个 Workspace（RT）" onClick={() => switchBy(1)}>RT</button></div>
    <textarea autoFocus value={draft} placeholder="告诉 WorkGround2 你要完成什么…" onChange={(event) => saveDraft(event.target.value)} />
    {error && <p role="alert" className="desktop-icon-popup__error">{error}</p>}
		<div className="desktop-icon-popup__actions"><button disabled={busy || !draft.trim()} onClick={() => void send()}>{busy ? "发送中…" : pending ? "重试" : "发送"}</button><button className="subtle" onClick={onClose}>取消</button></div>
  </div>;
}

function SearchPanel({ onClose, onPick }: { onClose: () => void; onPick: (item: DesktopIconSearchItem) => Promise<boolean> }) {
  const [query, setQuery] = useState("");
	const [results, setResults] = useState<DesktopIconSearchItem[]>([]);
	const [loading, setLoading] = useState(false);
	const [opening, setOpening] = useState(false);
	const [error, setError] = useState("");
	useEffect(() => {
		let cancelled = false;
		const timer = window.setTimeout(() => {
			setLoading(true); setError("");
			void app.DesktopIconSearch(query).then((result) => {
				if (cancelled) return;
				setResults(result.items || []); setError(result.error || "");
			}).catch((cause) => { if (!cancelled) setError(cause instanceof Error ? cause.message : String(cause)); })
				.finally(() => { if (!cancelled) setLoading(false); });
		}, 120);
		return () => { cancelled = true; window.clearTimeout(timer); };
	}, [query]);
  return <div className="desktop-icon-popup__search"><div className="desktop-icon-popup__searchbox"><Search /><input autoFocus value={query} disabled={opening} placeholder="搜索历史任务、Room、Workspace" onChange={(event) => setQuery(event.target.value)} /><button aria-label="关闭搜索" disabled={opening} onClick={onClose}><X /></button></div>{error && <p role="alert" className="desktop-icon-popup__error">{error}</p>}<div className="desktop-icon-popup__results" role="listbox" aria-busy={loading || opening}>{results.map((item) => <button key={item.id} role="option" disabled={opening} onClick={() => { setOpening(true); void onPick(item).finally(() => setOpening(false)); }}><span>{item.title}</span><small>{item.subtitle || item.kind}</small></button>)}{!loading && !results.length && <p className="desktop-icon-popup__empty">没有匹配结果</p>}</div></div>;
}

export function DesktopIconMode({ onExit }: { onExit: () => void }) {
  const [snapshot, setSnapshot] = useState<DesktopIconSnapshot>({ items: [], revision: "", hoverStatusDelayMs: 1200, style: "icons", unreadRevision: 0 });
  const [desktopZoom, setDesktopZoom] = useState(1);
  const [activeID, setActiveID] = useState("");
  const [previewID, setPreviewID] = useState("");
  const [menuID, setMenuID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [workspaces, setWorkspaces] = useState<WidgetWorkspaceOption[]>([]);
	const [quickWorkspace, setQuickWorkspace] = useState("");
	const clickTimer = useRef<number | undefined>(undefined);
	const hoverTimer = useRef<number | undefined>(undefined);
	const previewCloseTimer = useRef<number | undefined>(undefined);
  const drag = useRef<{ item: DesktopIconItem; x: number; y: number; moved: boolean } | null>(null);
	const actionRequests = useRef(new Map<string, string>());
  const itemRefs = useRef(new Map<string, HTMLButtonElement>());
	const regionKey = useRef("");
	const regionQueue = useRef<Promise<void>>(Promise.resolve());

  const refresh = useCallback(async () => {
    try { const next = await app.GetDesktopIconSnapshot(); setSnapshot(next); setError(next.error || ""); }
    catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
  }, []);
  useEffect(() => { void refresh(); void app.ListWidgetWorkspaces().then(setWorkspaces).catch(() => {}); const timer = window.setInterval(() => void refresh(), 1000); return () => window.clearInterval(timer); }, [refresh]);
  useEffect(() => { if (snapshot.style === "pager") onExit(); }, [onExit, snapshot.style]);
	useEffect(() => {
		let alive = true;
		void app.GetDesktopZoomFactor()
			.then((zoom) => { if (alive) setDesktopZoom(resolveWidgetZoomFrame(zoom).zoom); })
			.catch(() => { if (alive) setDesktopZoom(1); });
		return () => { alive = false; };
	}, []);
	useLayoutEffect(() => {
		let frame = 0;
		let alive = true;
		const report = (rects: ReturnType<typeof iconHitRect>[]) => {
			const key = rects.map((rect) => `${rect.x},${rect.y},${rect.width},${rect.height}`).join(";");
			if (!key || key === regionKey.current) return;
			regionKey.current = key;
			const next = regionQueue.current.catch(() => {}).then(() => app.SetDesktopIconHitRegions(rects));
			regionQueue.current = next.catch((cause) => {
				if (regionKey.current === key) regionKey.current = "";
				if (alive) setError(cause instanceof Error ? cause.message : String(cause));
			});
		};
		const sync = () => {
			cancelAnimationFrame(frame);
			frame = requestAnimationFrame(() => {
				const nodes = document.querySelectorAll<HTMLElement>(".desktop-icon, .desktop-icon-popup, .desktop-icon-menu, .desktop-icon-toast, .desktop-icon-exit");
				const rects = Array.from(nodes)
					.filter((node) => node.getClientRects().length > 0)
					.map((node) => iconHitRect(node.getBoundingClientRect(), window.devicePixelRatio, nativeHitPadding(node)));
				if (rects.length) report(rects);
			});
		};
		const observer = new ResizeObserver(sync);
		document.querySelectorAll<HTMLElement>(".desktop-icon, .desktop-icon-popup, .desktop-icon-menu, .desktop-icon-toast, .desktop-icon-exit").forEach((node) => observer.observe(node));
		sync(); window.addEventListener("resize", sync);
		void document.fonts?.ready.then(() => { if (alive) sync(); });
		return () => { alive = false; cancelAnimationFrame(frame); observer.disconnect(); window.removeEventListener("resize", sync); };
	}, [activeID, menuID, previewID, snapshot.revision]);

  const run = useCallback(async (item: DesktopIconItem, action: string, values: string[] = [], notice = item.notifications[0], position?: DesktopIconPosition) => {
    setBusy(true); setError("");
		const intent = JSON.stringify([item.id, notice?.id || "", item.revision, action, values, position || null]);
		const stableID = actionRequests.current.get(intent) || requestID(`icon-${action}`);
		actionRequests.current.set(intent, stableID);
		const input: DesktopIconActionInput = { itemId: item.id, noticeId: notice?.id, revision: item.revision, requestId: stableID, action, values, position, conversation: notice?.conversation, readSequence: notice?.readSequence };
    try {
      const result = await app.ApplyDesktopIconAction(input);
      setSnapshot(result.snapshot);
		if (result.status === "accepted" || result.status === "already_applied") { actionRequests.current.delete(intent); if (["ok", "dismiss", "later", "open", "reply"].includes(action)) setActiveID(""); }
		else { if (result.status === "stale" || result.status === "invalid") actionRequests.current.delete(intent); setError(result.error || "操作失败，可安全重试"); }
		return result.status === "accepted" || result.status === "already_applied";
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); return false; }
    finally { setBusy(false); }
  }, []);

  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (event.key === "Escape") { setActiveID(""); setPreviewID(""); setMenuID(""); }
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "n") { event.preventDefault(); setQuickWorkspace(""); setActiveID("fixed:new"); }
    };
    window.addEventListener("keydown", key); return () => window.removeEventListener("keydown", key);
  }, []);
	useEffect(() => {
		const close = () => {
			window.clearTimeout(clickTimer.current); window.clearTimeout(hoverTimer.current); window.clearTimeout(previewCloseTimer.current);
			drag.current = null; setActiveID(""); setPreviewID(""); setMenuID("");
		};
		window.addEventListener("blur", close);
		return () => { close(); window.removeEventListener("blur", close); };
	}, []);

  const openItem = (item: DesktopIconItem) => {
    if (item.kind === "fixed" && item.sourceId === "new") { setQuickWorkspace(""); setActiveID(item.id); }
		else if (item.kind === "fixed" && item.sourceId === "search") {
			setActiveID(item.id);
		}
    else setActiveID((current) => current === item.id ? "" : item.id);
    setPreviewID(""); setMenuID("");
  };
  const enter = (item: DesktopIconItem) => {
    window.clearTimeout(hoverTimer.current);
		window.clearTimeout(previewCloseTimer.current);
    if (!snapshot.hoverStatusDelayMs || activeID || menuID || drag.current) return;
    hoverTimer.current = window.setTimeout(() => setPreviewID(item.id), snapshot.hoverStatusDelayMs);
  };
	const closePreviewSoon = () => { window.clearTimeout(previewCloseTimer.current); previewCloseTimer.current = window.setTimeout(() => setPreviewID(""), 180); };
  const pointerDown = (event: ReactPointerEvent, item: DesktopIconItem) => {
    if (event.button !== 0) return;
    drag.current = { item, x: event.clientX, y: event.clientY, moved: false };
    event.currentTarget.setPointerCapture(event.pointerId);
  };
  const pointerMove = (event: ReactPointerEvent) => {
    if (!drag.current) return;
    if (Math.hypot(event.clientX - drag.current.x, event.clientY - drag.current.y) > DRAG_THRESHOLD) { drag.current.moved = true; setPreviewID(""); window.clearTimeout(clickTimer.current); }
  };
  const pointerUp = (event: ReactPointerEvent, item: DesktopIconItem) => {
    const current = drag.current; drag.current = null;
    if (current?.moved) {
      const delta = Math.round((event.clientX - current.x) / 72);
      void run(item, "move", [], undefined, { ...item.position, order: Math.max(0, item.position.order + delta) });
      return;
    }
    window.clearTimeout(clickTimer.current);
    clickTimer.current = window.setTimeout(() => openItem(item), CLICK_DELAY);
  };
	const doubleClick = (item: DesktopIconItem) => { window.clearTimeout(clickTimer.current); if (item.kind === "fixed") openItem(item); else void run(item, "open"); };

  const rows = { top: snapshot.items.filter((item) => item.position.row === "top"), bottom: snapshot.items.filter((item) => item.position.row === "bottom") };
  const active = snapshot.items.find((item) => item.id === activeID);
  const preview = snapshot.items.find((item) => item.id === previewID);
  const popupItem = active || preview;
	const popupStyle = useMemo(() => {
    if (!popupItem) return {};
    const node = itemRefs.current.get(popupItem.id); if (!node) return {};
		const rect = scaleIconRect(node.getBoundingClientRect(), desktopZoom);
		const viewportWidth = window.innerWidth * desktopZoom;
		const viewportHeight = window.innerHeight * desktopZoom;
		const width = active ? 330 : Math.min(300, viewportWidth - 20);
		const placed = placeIconPopup(rect, viewportWidth, viewportHeight, width);
		return { left: `${placed.left}px`, bottom: `${placed.bottom}px`, "--arrow-left": `${placed.arrowLeft}px` } as CSSProperties;
	}, [active, desktopZoom, popupItem, snapshot.revision]);

  const renderItem = (item: DesktopIconItem) => <div key={item.id} className={`desktop-icon-wrap desktop-icon-wrap--${item.position.zone}`}>
		<button ref={(node) => { if (node) itemRefs.current.set(item.id, node); else itemRefs.current.delete(item.id); }} type="button" className={`desktop-icon desktop-icon--${item.status}`} aria-label={`${item.title}，${previewText(item)}`} aria-expanded={activeID === item.id} onPointerDown={(event) => pointerDown(event, item)} onPointerMove={pointerMove} onPointerUp={(event) => pointerUp(event, item)} onDoubleClick={() => doubleClick(item)} onContextMenu={(event) => { event.preventDefault(); setMenuID(item.id); setActiveID(""); }} onMouseEnter={() => enter(item)} onMouseLeave={() => { window.clearTimeout(hoverTimer.current); if (previewID === item.id) closePreviewSoon(); }} onFocus={() => { if (!activeID) setPreviewID(item.id); }} onBlur={() => { if (!activeID) closePreviewSoon(); }}>
      <span className="desktop-icon__art">{itemGlyph(item)}<span className="desktop-icon__shortcut" aria-hidden="true"><CornerUpRight /></span></span>
      <span className="desktop-icon__label">{item.title}</span>
      {item.unreadCount > 0 && <span className="desktop-icon__unread" aria-label={`${item.unreadCount} 条未读`}>{item.unreadCount > 99 ? "99+" : item.unreadCount}</span>}
      {item.activityCount ? <span className="desktop-icon__activity" aria-label={`${item.activityCount} 个活动任务`}>{item.activityCount}</span> : null}
      {statusGlyph(item) && <span className="desktop-icon__status">{statusGlyph(item)}</span>}
      {(item.status === "running" || item.status === "thinking") && <span className="desktop-icon__ring" aria-hidden="true" />}
    </button>
		{menuID === item.id && <div className="desktop-icon-menu" role="menu"><button role="menuitem" onClick={() => item.kind === "fixed" ? openItem(item) : void run(item, "open")}>打开</button>{item.unreadCount > 0 && <button role="menuitem" onClick={() => void run(item, "mark_read")}>标记已读</button>}{item.retained && <button role="menuitem" onClick={() => void run(item, "remove")}>移除</button>}</div>}
  </div>;

	const zoomFrame = resolveWidgetZoomFrame(desktopZoom);
	const zoomStyle: CSSProperties = {
		width: `${zoomFrame.widthVw}vw`,
		height: `${zoomFrame.heightVh}vh`,
		transform: `scale(${zoomFrame.scale})`,
		transformOrigin: "left top",
	};

  return <main className="desktop-icon-mode" style={zoomStyle} aria-label="WorkGround2 桌面图标小组件" onPointerDown={(event) => { if (event.target === event.currentTarget) { setActiveID(""); setMenuID(""); } }}>
		<span className="sr-only" aria-live="polite">{snapshot.items.reduce((count, item) => count + item.unreadCount, 0)} 条桌面待处理信息</span>
		<div className="desktop-icon-grid"><div className="desktop-icon-row desktop-icon-row--top">{rows.top.map(renderItem)}</div><div className="desktop-icon-row desktop-icon-row--bottom">{rows.bottom.map(renderItem)}</div></div>
		{popupItem && <section className={`desktop-icon-popup${active ? " desktop-icon-popup--interactive" : " desktop-icon-popup--preview"}`} style={popupStyle} aria-live={popupItem.status === "failed" || popupItem.status === "needs_input" ? "assertive" : "polite"} onMouseEnter={() => window.clearTimeout(previewCloseTimer.current)} onMouseLeave={closePreviewSoon}>
      <span className="desktop-icon-popup__arrow" aria-hidden="true" />
      {!active && <p>{previewText(popupItem)}</p>}
      {active && active.sourceId === "new" && <QuickStart workspaces={workspaces} initialWorkspace={quickWorkspace} onClose={() => setActiveID("")} />}
      {active && active.sourceId === "search" && <SearchPanel onClose={() => setActiveID("")} onPick={async (result) => { const opened = await run(active, "open_search", [result.id]); if (opened) onExit(); return opened; }} />}
      {active && active.notifications[0] && <NoticeBody item={active} notice={active.notifications[0]} busy={busy} run={(action, values) => void run(active, action, values)} />}
      {active && !active.notifications[0] && active.runtimeStatus && <RuntimeBody item={active} busy={busy} run={(action) => void run(active, action)} />}
      {active && !active.notifications[0] && !active.runtimeStatus && active.sourceId !== "new" && active.sourceId !== "search" && <><strong>{active.title}</strong><p>{previewText(active)}</p><div className="desktop-icon-popup__actions"><button onClick={() => void run(active, "open")}>打开</button>{active.kind === "workspace" && <button onClick={() => { setQuickWorkspace(`project:${active.sourceId}`); setActiveID("fixed:new"); }}>在此发起</button>}</div></>}
    </section>}
    {error && <div className="desktop-icon-toast" role="alert">{error}<button aria-label="关闭错误" onClick={() => setError("")}><X /></button></div>}
    <button className="desktop-icon-exit" onClick={() => void app.ExitWidgetMode("").then(onExit)}>返回主窗口</button>
  </main>;
}
