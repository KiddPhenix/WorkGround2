import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent } from "react";
import { BookOpen, Bot, Check, ChevronDown, ChevronUp, CircleAlert, HelpCircle, Loader2, MessageCircle, Plus, Search, Users, X } from "lucide-react";
import { app, type DesktopIconActionInput, type DesktopIconItem, type DesktopIconNotice, type DesktopIconPosition, type DesktopIconSearchItem, type DesktopIconSnapshot, type WidgetWorkspaceOption } from "../../lib/bridge";
import { asArray } from "../../lib/array";
import { filterAtMatches } from "../../lib/atMatches";
import { isComposerSubmitKey } from "../../lib/composerKeyboard";
import { activeFileReferenceToken } from "../FileReferenceMenu";
import type { CommandInfo, DirEntry, ModelInfo, SlashArgItem, ToolApprovalMode, VocabularyMatch } from "../../lib/types";
import { acceptVocabulary, type VocabularyToken } from "../../lib/vocabularyCompletion";
import { iconHitRect, parseCollapseState, placeIconPopup, quickStartWorkspaceIndex, scaleIconRect, serializeCollapseState } from "./desktopIconLayout";
import logoSymbol from "../../assets/logo-symbol.svg";
import { QUICK_APPROVAL_KEY, QUICK_MODEL_KEY, nextQuickStartApproval, quickStartApprovalLabel, quickStartModelLabel, quickStartModelOptions, quickStartPreferences, resolveQuickStartApproval, resolveQuickStartModel, sameQuickStartIntent, type QuickStartIntent, type QuickStartPreferences } from "./quickStartPreferences";
import { quickStartAcceptCompletion, quickStartAtItems, quickStartCompletionKey, quickStartCompletionMove, quickStartPickMenu, quickStartSkillMatches, quickStartSkillQuery, quickStartSlashMatches, quickStartSlashQuery, quickStartVocabularyToken, type QuickStartCompletion } from "./quickStartCompletion";
import { resolveWidgetZoomFrame } from "./widgetZoom";
import "./desktop-icon-mode.css";

const CLICK_DELAY = 240;
const DRAG_THRESHOLD = 7;
const QUICK_WORKSPACE_KEY = "wg2.icon-widget-workspace";
const CLUSTER_KEY = "wg2.icon-widget-cluster";
const HIT_REGION_SELECTOR = ".desktop-icon, .desktop-icon-popup, .desktop-icon-menu, .desktop-icon-toast, .desktop-icon-anchor, .desktop-icon-collapse";
const IME_CONFIRM_GRACE_MS = 100;

// IME composition must never leak into completion or submit handling: the
// native isComposing flag, the WebView2 keyCode 229 convention, and a short
// grace after compositionend cover the confirm-Enter that lands right after.
function isWidgetImeKeyEvent(event: ReactKeyboardEvent<HTMLTextAreaElement>, composing: boolean, lastCompositionEndAt: number): boolean {
	const native = event.nativeEvent as globalThis.KeyboardEvent & { isComposing?: boolean; keyCode?: number };
	return composing || native.isComposing === true || native.keyCode === 229 || Date.now() - lastCompositionEndAt < IME_CONFIRM_GRACE_MS;
}

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

function readCollapsedState(): boolean {
  try { return parseCollapseState(localStorage.getItem(CLUSTER_KEY)); }
  catch { return false; }
}

function writeCollapsedState(collapsed: boolean): void {
  try { localStorage.setItem(CLUSTER_KEY, serializeCollapseState(collapsed)); } catch { /* storage unavailable */ }
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
  if (item.runtimeStatus) return `${item.runtimeStatus.summary || item.runtimeStatus.phase} · ${Math.max(0, Math.round(item.runtimeStatus.elapsedMs / 1000))} 秒`;
  if (item.unreadCount > 0) return `${item.unreadCount} 条待处理信息`;
  if (item.kind === "workspace") return `${item.title} · 快速发起`;
  if (item.status === "done") return item.subtitle || "已完成，可在搜索中找到记录";
  return item.title;
}

function runtimeTail(text: string, limit = 13): string {
	const chars = Array.from(text.trim());
	return chars.length > limit ? `…${chars.slice(-limit).join("")}` : chars.join("");
}

function RuntimeIndicator({ item }: { item: DesktopIconItem }) {
	if (item.status !== "thinking" && item.status !== "running") return null;
	const summary = item.runtimeStatus?.summary || (item.status === "thinking" ? "正在等待思考内容" : "正在执行");
	return <span className={`desktop-icon__runtime desktop-icon__runtime--${item.status}`} aria-label={`${item.status === "thinking" ? "Thinking" : "Running"}：${summary}`}>
		<span className="desktop-icon__runtime-state">
			{item.status === "thinking" ? <i aria-hidden="true" /> : <Loader2 aria-hidden="true" />}
			{item.status === "thinking" ? "Thinking" : "Running"}
		</span>
		<span key={summary} className="desktop-icon__runtime-copy" title={summary}>{runtimeTail(summary)}</span>
		{item.status === "running" && <span className="desktop-icon__runtime-track" aria-hidden="true"><i /><i /><i /></span>}
	</span>;
}

function NoticeBody({ item, notice, busy, run, onClose }: { item: DesktopIconItem; notice: DesktopIconNotice; busy: boolean; run: (action: string, values?: string[]) => Promise<boolean>; onClose: () => void }) {
  const [answer, setAnswer] = useState("");
	const [selected, setSelected] = useState("");
	const [reply, setReply] = useState("");
	const [dialogOpen, setDialogOpen] = useState(false);
	const [followup, setFollowup] = useState("");
  const needsAnswer = notice.kind === "needs_input";
  const completion = notice.kind === "completed" || notice.kind === "failed";
	const closeDialog = () => { setDialogOpen(false); setFollowup(""); };
	const sendFollowup = () => {
		const text = followup.trim();
		if (!busy && text) void run("continue", [text]);
	};
  return <>
    <div className="desktop-icon-popup__eyebrow">{notice.title}</div>
    <strong>{item.title}</strong>
    <p>{notice.body}</p>
    {needsAnswer && <div className="desktop-icon-popup__answers">
		{notice.options.map((option) => <button key={option.value} type="button" aria-pressed={selected === option.value} disabled={busy} onClick={() => { setSelected(option.value); setAnswer(""); }}><span>{option.label}</span>{option.description && <small>{option.description}</small>}</button>)}
      <label><span className="sr-only">自定义回答</span><input value={answer} disabled={busy} placeholder="自定义回答" onChange={(event) => setAnswer(event.target.value)} /></label>
    </div>}
	{notice.kind === "message" && (item.kind === "room" || item.kind === "person") && <label className="desktop-icon-popup__reply"><span className="sr-only">快速回复</span><input value={reply} disabled={busy} placeholder="快速回复" onChange={(event) => setReply(event.target.value)} /></label>}
    <div className={`desktop-icon-popup__actions${completion ? " desktop-icon-popup__actions--completion" : ""}`}>
		{needsAnswer && <button disabled={busy || !(answer.trim() || selected)} onClick={() => run("answer", [answer.trim() || selected])}>提交回答</button>}
      {notice.kind === "needs_confirm" && <><button disabled={busy} onClick={() => run("approve")}>允许</button><button disabled={busy} onClick={() => run("deny")}>拒绝</button></>}
      {notice.kind === "failed" && notice.retryable && <button disabled={busy} onClick={() => run("retry")}>重试</button>}
      {completion && <><button type="button" className="desktop-icon-popup__ok" onClick={onClose}>OK</button><button type="button" className="desktop-icon-popup__detail" disabled={busy} onClick={() => run("open")}>Detail</button><button type="button" className="desktop-icon-popup__dismiss" disabled={busy} onClick={() => run("dismiss")}>Dismiss</button></>}
      {(needsAnswer || notice.kind === "needs_confirm") && <button disabled={busy} className="subtle" onClick={() => run("later")}>稍后处理</button>}
		{notice.kind === "message" && (item.kind === "room" || item.kind === "person") && <button disabled={busy || !reply.trim()} onClick={() => run("reply", [reply.trim()])}>回复</button>}
		{notice.kind === "message" && <button disabled={busy} onClick={() => run("open")}>打开会话</button>}
    </div>
	{completion && <div className={`desktop-icon-popup__dialog${dialogOpen ? " is-open" : ""}`}>
		{!dialogOpen && <button type="button" className="desktop-icon-popup__dialog-trigger" disabled={busy} onClick={() => setDialogOpen(true)}>对话框</button>}
		{dialogOpen && <>
			<label><span className="sr-only">继续当前任务</span><textarea autoFocus value={followup} disabled={busy} placeholder="告诉 WorkGround2 接下来要完成什么…" onChange={(event) => setFollowup(event.target.value)} onKeyDown={(event) => {
				if (event.key === "Escape") { event.preventDefault(); closeDialog(); return; }
				if (event.key === "Enter" && (event.ctrlKey || event.metaKey) && !event.nativeEvent.isComposing) { event.preventDefault(); sendFollowup(); }
			}} /></label>
			<div className="desktop-icon-popup__dialog-actions"><button type="button" disabled={busy || !followup.trim()} onClick={sendFollowup}>{busy ? "发送中…" : "发送"}</button><button type="button" className="subtle" disabled={busy} onClick={closeDialog}>取消</button><small>Ctrl+Enter 发送</small></div>
		</>}
	</div>}
    {completion && notice.summaryStatus === "failed" && <small className="desktop-icon-popup__summary-failed">摘要生成失败，稍后自动重试</small>}
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
    <div className="desktop-icon-popup__actions"><button disabled={busy} onClick={() => run("open")}>打开任务</button><button disabled={busy} className="danger" onClick={() => run("stop")}>停止</button></div>
  </>;
}

function QuickStart({ workspaces, initialWorkspace = "", onClose }: { workspaces: WidgetWorkspaceOption[]; initialWorkspace?: string; onClose: () => void }) {
  const choices = workspaces.length ? workspaces : [{ scope: "auto", name: "自动" } as WidgetWorkspaceOption];
	const [pending, setPendingState] = useState<QuickStartIntent | null>(() => {
		try { return JSON.parse(localStorage.getItem("wg2.icon-widget-pending") || "null") as QuickStartIntent | null; } catch { return null; }
	});
	const keys = useMemo(() => choices.map(widgetWorkspaceKey), [workspaces]);
	const keysToken = keys.join("\n");
	const initializedKeys = useRef(keysToken);
	const [index, setIndex] = useState(() => quickStartWorkspaceIndex(keys, pending?.workspace, initialWorkspace, localStorage.getItem(QUICK_WORKSPACE_KEY) || ""));
	const [draft, setDraft] = useState(() => localStorage.getItem("wg2.icon-widget-draft") || "");
	const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
	const [preferences, setPreferences] = useState<QuickStartPreferences | null>(null);
	const [preferencesError, setPreferencesError] = useState("");
	const [models, setModels] = useState<ModelInfo[]>([]);
	const [model, setModel] = useState(() => localStorage.getItem(QUICK_MODEL_KEY) || "");
	const [approval, setApproval] = useState(() => localStorage.getItem(QUICK_APPROVAL_KEY) || "");
	const [modelMenuOpen, setModelMenuOpen] = useState(false);
	const [modelQuery, setModelQuery] = useState("");
	const [completion, setCompletion] = useState<QuickStartCompletion | null>(null);
	const [completionDismissed, setCompletionDismissed] = useState(false);
	const [caret, setCaret] = useState({ start: 0, end: 0 });
	const [commands, setCommands] = useState<CommandInfo[]>([]);
	const composingRef = useRef(false);
	const lastCompositionEndAt = useRef(0);
	const preferencesLoad = useRef(0);
	const taRef = useRef<HTMLTextAreaElement>(null);
  const choice = choices[index % choices.length];
  const workspace = widgetWorkspaceKey(choice);
	const setPending = (next: QuickStartIntent | null) => { setPendingState(next); if (next) localStorage.setItem("wg2.icon-widget-pending", JSON.stringify(next)); else localStorage.removeItem("wg2.icon-widget-pending"); };
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
	const loadPreferences = useCallback(() => {
		const generation = ++preferencesLoad.current;
		setPreferencesError("");
		void Promise.all([app.Settings(), app.Models(), app.Commands()])
			.then(([settings, nextModels, nextCommands]) => {
				if (generation !== preferencesLoad.current) return;
				setPreferences(quickStartPreferences(settings));
				setModels(asArray(nextModels));
				setCommands(asArray(nextCommands));
			})
			.catch((cause) => {
				if (generation !== preferencesLoad.current) return;
				setPreferences(null);
				setModels([]);
				setCommands([]);
				setPreferencesError(cause instanceof Error ? cause.message : String(cause));
			});
	}, []);
	useEffect(() => { loadPreferences(); }, [loadPreferences]);
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
	// Model / approval: remember the QuickStart-specific choice so a retry
	// replays the exact same intent (same requestId) until one of the four
	// inputs changes; the send passes both fields to the backend.
	const selectedModel = useMemo(() => resolveQuickStartModel(model, preferences?.model ?? "", models), [model, models, preferences]);
	const selectedApproval = useMemo(() => resolveQuickStartApproval(approval, preferences?.approvalMode ?? "ask"), [approval, preferences]);
	const modelOptions = useMemo(() => quickStartModelOptions(models), [models]);
	const modelKeyword = modelQuery.trim().toLowerCase();
	const filteredModelOptions = useMemo(
		() => modelKeyword ? modelOptions.filter((option) => option.label.toLowerCase().includes(modelKeyword) || option.provider.toLowerCase().includes(modelKeyword)) : modelOptions,
		[modelKeyword, modelOptions],
	);
	const pickModel = (ref: string) => { setModel(ref); localStorage.setItem(QUICK_MODEL_KEY, ref); setModelMenuOpen(false); setModelQuery(""); setPending(null); };
	const pickApproval = (mode: ToolApprovalMode) => { setApproval(mode); localStorage.setItem(QUICK_APPROVAL_KEY, mode); setModelMenuOpen(false); setPending(null); };

	// --- menu completions: slash commands, slash args, $ skills, @ file refs ---
	const slashQuery = useMemo(() => quickStartSlashQuery(draft), [draft]);
	const slashMatches = useMemo(() => quickStartSlashMatches(slashQuery, commands), [slashQuery, commands]);
	const skillQuery = useMemo(() => quickStartSkillQuery(draft), [draft]);
	const skillMatches = useMemo(() => quickStartSkillMatches(skillQuery, commands), [skillQuery, commands]);
	const [argRes, setArgRes] = useState<{ items: SlashArgItem[]; from: number } | null>(null);
	useEffect(() => {
		if (!draft.startsWith("/") || !/\s/.test(draft)) { setArgRes(null); return; }
		let live = true;
		const timer = window.setTimeout(() => {
			app.SlashArgs(draft)
				.then((result) => {
					if (!live) return;
					const items = asArray(result?.items);
					const from = result?.from ?? 0;
					const useful = items.filter((item) => draft.slice(0, from) + item.insert !== draft);
					setArgRes(useful.length > 0 ? { items: useful, from } : null);
				})
				.catch(() => {});
		}, 120);
		return () => { live = false; window.clearTimeout(timer); };
	}, [draft]);
	const atToken = useMemo(() => activeFileReferenceToken(draft), [draft]);
	const atRoot = choice.scope === "project" ? (choice.root ?? "") : "";
	const [atEntries, setAtEntries] = useState<DirEntry[]>([]);
	useEffect(() => {
		if (!atToken) { setAtEntries([]); return; }
		let live = true;
		const { dir, frag } = atToken;
		const request = dir !== "" || frag === ""
			? app.ListDirForWorkspace(atRoot, dir)
			: app.SearchFileRefsForWorkspace(atRoot, frag);
		request
			.then((entries) => { if (live) setAtEntries(entries ?? []); })
			.catch(() => {});
		return () => { live = false; };
	}, [atRoot, atToken]);
	const atMatches = useMemo(() => {
		if (!atToken) return [];
		return atToken.dir !== "" ? filterAtMatches(atEntries, [], atToken.frag) : atEntries;
	}, [atEntries, atToken]);
	const menuBase = useMemo(
		() => completionDismissed ? null : quickStartPickMenu({
			slashMatches,
			slashArgs: argRes,
			skillMatches,
			at: atToken ? { token: atToken, items: quickStartAtItems(atMatches) } : null,
		}),
		[argRes, atMatches, atToken, completionDismissed, skillMatches, slashMatches],
	);
	const [vocabMatch, setVocabMatch] = useState<VocabularyMatch | null>(null);
	const [vocabToken, setVocabToken] = useState<VocabularyToken | null>(null);
	const [vocabDismissed, setVocabDismissed] = useState(false);
	const [vocabScrollTop, setVocabScrollTop] = useState(0);
	const vocabularyToken = useMemo(
		() => composingRef.current ? null : quickStartVocabularyToken(draft, caret.start, caret.end),
		// eslint-disable-next-line react-hooks/exhaustive-deps
		[draft, caret],
	);
	useEffect(() => {
		if (menuBase || vocabDismissed || !vocabularyToken) { setVocabMatch(null); setVocabToken(null); return; }
		let live = true;
		const timer = window.setTimeout(() => {
			app.CompleteVocabulary(vocabularyToken.prefix, 5)
				.then((items) => {
					if (!live) return;
					const first = asArray(items).find((item) => item.text !== vocabularyToken.prefix) ?? null;
					setVocabMatch(first);
					setVocabToken(first ? vocabularyToken : null);
				})
				.catch(() => { if (live) { setVocabMatch(null); setVocabToken(null); } });
		}, 90);
		return () => { live = false; window.clearTimeout(timer); };
	// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [menuBase, vocabDismissed, vocabularyToken?.from, vocabularyToken?.prefix]);
	useEffect(() => { setCompletion(menuBase); }, [menuBase]);
	useEffect(() => { setCompletionDismissed(false); }, [draft]);
	useEffect(() => { setVocabDismissed(false); }, [draft, caret.start, caret.end]);
	const applyCompletion = (target?: QuickStartCompletion) => {
		const menu = target ?? completion;
		if (!menu) return;
		const accepted = quickStartAcceptCompletion(menu, draft);
		saveDraft(accepted.text);
		setCaret({ start: accepted.cursor, end: accepted.cursor });
		if (accepted.recordUse) void app.RecordVocabularyUse(accepted.recordUse.id, accepted.recordUse.useID).catch(() => {});
		requestAnimationFrame(() => {
			const node = taRef.current;
			if (node) { node.focus(); node.selectionStart = node.selectionEnd = accepted.cursor; }
		});
	};
	const acceptVocab = () => {
		if (!vocabMatch || !vocabToken) return;
		const accepted = acceptVocabulary(draft, vocabToken, vocabMatch);
		saveDraft(accepted.value);
		setCaret({ start: accepted.cursor, end: accepted.cursor });
		setVocabMatch(null);
		setVocabToken(null);
		setVocabDismissed(true);
		const useID = typeof crypto !== "undefined" && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
		void app.RecordVocabularyUse(vocabMatch.id, useID).catch(() => {});
		requestAnimationFrame(() => {
			const node = taRef.current;
			if (node) { node.focus(); node.selectionStart = node.selectionEnd = accepted.cursor; }
		});
	};
	const completionItemClass = (index: number) => `desktop-icon-popup__completion-item${index === completion?.active ? " is-active" : ""}`;
	const completionItem = (index: number) => ({
		onMouseDown: (event: ReactPointerEvent<HTMLButtonElement>) => event.preventDefault(),
		onMouseMove: () => setCompletion((current) => current ? { ...current, active: index } : current),
		onClick: () => { const target = completion ? { ...completion, active: index } : null; if (target) applyCompletion(target); },
	});

  const send = async () => {
    const prompt = draft.trim();
		if (!prompt || !preferences) return;
		const modelRef = selectedModel;
		const approvalMode = selectedApproval;
    const attempt = pending && sameQuickStartIntent(pending, { prompt, workspace, model: modelRef, approvalMode })
			? pending
			: { id: requestID("icon-new"), prompt, workspace, model: modelRef, approvalMode };
		setPending(attempt); setBusy(true); setError("");
    try {
      const result = await app.StartWidgetConversation({ prompt, workspace, requestId: attempt.id, model: modelRef, approvalMode });
		if (result.status === "accepted" || result.status === "already_applied") { saveDraft(""); setPending(null); onClose(); }
      else setError(result.error || "发起失败，可安全重试");
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
		finally { setBusy(false); }
  };
  return <div className="desktop-icon-popup__quick" onKeyDown={(event) => {
    if (event.key === "Escape") { onClose(); return; }
    if ((event.ctrlKey || event.metaKey) && (event.key === "ArrowLeft" || event.key === "ArrowRight")) {
      event.preventDefault();
      switchBy(event.key === "ArrowLeft" ? -1 : 1);
      return;
    }
    if (event.key === "PageUp") { event.preventDefault(); switchBy(-1); return; }
    if (event.key === "PageDown") { event.preventDefault(); switchBy(1); }
  }}>
    <div className="desktop-icon-popup__workspace"><button aria-label="上一个 Workspace（LT 或 Ctrl+←）" title="上一个（LT / Ctrl+←）" onClick={() => switchBy(-1)}>上一个</button><strong>{choice.name} · {index + 1} / {choices.length}</strong><button aria-label="下一个 Workspace（RT 或 Ctrl+→）" title="下一个（RT / Ctrl+→）" onClick={() => switchBy(1)}>下一个</button></div>
		<div className="desktop-icon-popup__quick-meta">
			<div className="desktop-icon-popup__quick-chip-wrap">
				<button type="button" className="desktop-icon-popup__quick-chip" aria-label="选择模型" aria-haspopup="listbox" aria-expanded={modelMenuOpen} disabled={!preferences || busy} onClick={() => setModelMenuOpen((open) => !open)}>
					<span className="desktop-icon-popup__quick-chip-copy"><strong title={preferences?.model}>{preferences ? quickStartModelLabel(selectedModel) : "读取中…"}</strong></span>
					<ChevronDown aria-hidden="true" />
				</button>
				{modelMenuOpen && <div className="desktop-icon-popup__picker" role="listbox" aria-label="选择模型">
					<div className="desktop-icon-popup__picker-search"><Search aria-hidden="true" /><input autoFocus value={modelQuery} placeholder="搜索模型" onChange={(event) => setModelQuery(event.target.value)} onKeyDown={(event) => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); setModelMenuOpen(false); } }} /></div>
					{filteredModelOptions.length === 0 && <div className="desktop-icon-popup__picker-empty">没有可用模型</div>}
					{filteredModelOptions.map((option) => <button key={option.ref} type="button" role="option" aria-selected={option.ref === selectedModel} className={`desktop-icon-popup__picker-item${option.ref === selectedModel ? " is-active" : ""}`} onMouseDown={(event) => event.preventDefault()} onClick={() => pickModel(option.ref)}><span className="desktop-icon-popup__picker-name">{option.label}</span><span className="desktop-icon-popup__picker-meta">{option.provider}</span>{option.ref === selectedModel && <Check aria-hidden="true" />}</button>)}
				</div>}
			</div>
			<div className="desktop-icon-popup__quick-chip-wrap">
				<button type="button" className="desktop-icon-popup__quick-chip" aria-label={`审批：${preferences ? quickStartApprovalLabel(selectedApproval) : "读取中"}，点击切换`} disabled={!preferences || busy} onClick={() => pickApproval(nextQuickStartApproval(selectedApproval))}>
					<span className="desktop-icon-popup__quick-chip-copy"><strong>{preferences ? quickStartApprovalLabel(selectedApproval) : "读取中…"}</strong></span>
				</button>
			</div>
		</div>
		{completion && <div className="desktop-icon-popup__completion" role="listbox" aria-label="补全候选">
			{completion.kind === "slash" && completion.items.map((item, index) => <button key={`${item.kind}:${item.name}`} type="button" role="option" aria-selected={index === completion.active} className={completionItemClass(index)} {...completionItem(index)}><span className="desktop-icon-popup__completion-name">/{item.name}</span><span className="desktop-icon-popup__completion-desc">{item.description}</span>{item.kind !== "builtin" && <span className="desktop-icon-popup__completion-kind">{item.kind === "skill" ? "技能" : item.kind === "custom" ? "项目" : item.kind === "mcp" ? "MCP" : item.kind}</span>}</button>)}
			{completion.kind === "slasharg" && completion.items.map((item, index) => <button key={`${item.label}:${index}`} type="button" role="option" aria-selected={index === completion.active} className={completionItemClass(index)} {...completionItem(index)}><span className="desktop-icon-popup__completion-name">{item.label}</span>{item.hint && <span className="desktop-icon-popup__completion-desc">{item.hint}</span>}</button>)}
			{completion.kind === "skill" && completion.items.map((item, index) => <button key={`skill:${item.name}`} type="button" role="option" aria-selected={index === completion.active} className={completionItemClass(index)} {...completionItem(index)}><span className="desktop-icon-popup__completion-name">${item.name}</span><span className="desktop-icon-popup__completion-desc">{item.description}</span><span className="desktop-icon-popup__completion-kind">技能</span></button>)}
			{completion.kind === "at" && completion.items.map((item, index) => <button key={`${item.isDir ? "d:" : "f:"}${item.path}`} type="button" role="option" aria-selected={index === completion.active} className={completionItemClass(index)} {...completionItem(index)}><span className="desktop-icon-popup__completion-name">{item.label}{item.isDir ? "/" : ""}</span><span className="desktop-icon-popup__completion-desc">{item.isDir ? "目录" : "文件"}</span></button>)}
		</div>}
		<div className="desktop-icon-popup__quick-composer">
			{vocabMatch && vocabToken && <div className="desktop-icon-popup__vocab-ghost" aria-hidden="true" style={{ top: `${-vocabScrollTop}px` }}><span>{draft}</span><b>{vocabMatch.suffix}</b></div>}
			<textarea autoFocus ref={taRef} value={draft} aria-describedby={vocabMatch ? "desktop-icon-vocab-hint" : undefined} placeholder="告诉 WorkGround2 你要完成什么…" onChange={(event) => { saveDraft(event.target.value); setCaret({ start: event.target.selectionStart ?? event.target.value.length, end: event.target.selectionEnd ?? event.target.value.length }); setVocabMatch(null); }} onSelect={() => { const node = taRef.current; if (node) setCaret({ start: node.selectionStart, end: node.selectionEnd }); }} onClick={() => { const node = taRef.current; if (node) setCaret({ start: node.selectionStart, end: node.selectionEnd }); }} onKeyUp={() => { const node = taRef.current; if (node) setCaret({ start: node.selectionStart, end: node.selectionEnd }); }} onScroll={(event) => setVocabScrollTop(event.currentTarget.scrollTop)} onKeyDown={(event) => {
			const composing = isWidgetImeKeyEvent(event, composingRef.current, lastCompositionEndAt.current);
			if (event.key === "Enter" && composing) return;
			if (!completion && event.key === "Tab" && !event.shiftKey && vocabMatch && vocabToken && !composing) { event.preventDefault(); acceptVocab(); return; }
			if (!completion && event.key === "Escape" && vocabMatch && !composing) { event.preventDefault(); setVocabMatch(null); setVocabToken(null); setVocabDismissed(true); return; }
			const action = quickStartCompletionKey(completion, event, composing, preferences?.submitKey ?? "enter");
			if (action.type === "move") { event.preventDefault(); setCompletion(quickStartCompletionMove(completion, action.delta)); return; }
			if (action.type === "accept") { event.preventDefault(); applyCompletion(); return; }
			if (action.type === "close") { event.preventDefault(); event.stopPropagation(); setCompletionDismissed(true); return; }
			if (action.type === "submit") {
				if (!preferences || !isComposerSubmitKey(event, preferences.submitKey, event.nativeEvent.isComposing)) return;
				event.preventDefault();
				if (!busy && draft.trim()) void send();
			}
			}} onCompositionStart={() => { composingRef.current = true; setCompletionDismissed(true); setVocabMatch(null); }} onCompositionEnd={(event) => { composingRef.current = false; lastCompositionEndAt.current = Date.now(); setCaret({ start: event.currentTarget.selectionStart ?? draft.length, end: event.currentTarget.selectionEnd ?? draft.length }); }} />
			{vocabMatch && <span id="desktop-icon-vocab-hint" className="sr-only">按 Tab 补全为 {vocabMatch.text}</span>}
		</div>
		{preferencesError && <div role="alert" className="desktop-icon-popup__settings-error"><span>读取新会话设置失败：{preferencesError}</span><button type="button" className="subtle" onClick={loadPreferences}>重试</button></div>}
    {error && <p role="alert" className="desktop-icon-popup__error">{error}</p>}
		<div className="desktop-icon-popup__actions desktop-icon-popup__actions--quick"><button disabled={busy || !draft.trim() || !preferences} onClick={() => void send()}>{busy ? "发送中…" : !preferences ? "读取设置…" : pending ? "重试" : "发送"}</button><button className="subtle" onClick={onClose}>取消</button>{preferences && <small className="desktop-icon-popup__submit-hint">{preferences.submitKey === "ctrl_enter" ? "Ctrl+Enter 发送" : "Enter 发送"}</small>}</div>
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

export function DesktopIconMode() {
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
	const popupRef = useRef<HTMLElement>(null);
	const [popupWidth, setPopupWidth] = useState(0);
	const regionKey = useRef("");
	const regionQueue = useRef<Promise<void>>(Promise.resolve());
	const [collapsed, setCollapsed] = useState(readCollapsedState);

  const refresh = useCallback(async () => {
    try { const next = await app.GetDesktopIconSnapshot(); setSnapshot(next); setError(next.error || ""); }
    catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
  }, []);
  useEffect(() => { void refresh(); void app.ListWidgetWorkspaces().then(setWorkspaces).catch(() => {}); const timer = window.setInterval(() => void refresh(), 1000); return () => window.clearInterval(timer); }, [refresh]);
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
				const nodes = document.querySelectorAll<HTMLElement>(HIT_REGION_SELECTOR);
				const rects = Array.from(nodes)
					.filter((node) => node.getClientRects().length > 0)
					.map((node) => iconHitRect(node.getBoundingClientRect(), window.devicePixelRatio, nativeHitPadding(node)));
				if (rects.length) report(rects);
			});
		};
		const observer = new ResizeObserver(sync);
		document.querySelectorAll<HTMLElement>(HIT_REGION_SELECTOR).forEach((node) => observer.observe(node));
		sync(); window.addEventListener("resize", sync);
		void document.fonts?.ready.then(() => { if (alive) sync(); });
		return () => { alive = false; cancelAnimationFrame(frame); observer.disconnect(); window.removeEventListener("resize", sync); };
	}, [activeID, menuID, previewID, snapshot.revision, collapsed]);

  const run = useCallback(async (item: DesktopIconItem, action: string, values: string[] = [], notice = item.notifications[0], position?: DesktopIconPosition) => {
    setBusy(true); setError("");
		const intent = JSON.stringify([item.id, notice?.id || "", item.revision, action, values, position || null]);
		const stableID = actionRequests.current.get(intent) || requestID(`icon-${action}`);
		actionRequests.current.set(intent, stableID);
		const input: DesktopIconActionInput = { itemId: item.id, noticeId: notice?.id, revision: item.revision, requestId: stableID, action, values, position, conversation: notice?.conversation, readSequence: notice?.readSequence };
    try {
      const result = await app.ApplyDesktopIconAction(input);
      setSnapshot(result.snapshot);
		if (result.status === "accepted" || result.status === "already_applied") { actionRequests.current.delete(intent); if (["dismiss", "later", "open", "reply", "continue"].includes(action)) setActiveID(""); }
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
			drag.current = null;
			setActiveID(""); setPreviewID(""); setMenuID("");
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
	useLayoutEffect(() => {
		const node = popupRef.current;
		if (!popupItem || !node) {
			setPopupWidth(0);
			return;
		}
		const measure = () => {
			const width = node.getBoundingClientRect().width * desktopZoom;
			setPopupWidth((current) => Math.abs(current - width) < 0.5 ? current : width);
		};
		const observer = new ResizeObserver(measure);
		observer.observe(node);
		measure();
		return () => observer.disconnect();
	}, [desktopZoom, popupItem]);
	const popupStyle = useMemo(() => {
    if (!popupItem) return {};
    const node = itemRefs.current.get(popupItem.id); if (!node) return {};
		const rect = scaleIconRect(node.getBoundingClientRect(), desktopZoom);
		const viewportWidth = window.innerWidth * desktopZoom;
		const viewportHeight = window.innerHeight * desktopZoom;
		const fallbackWidth = active ? 330 : Math.min(300, viewportWidth - 20);
		const width = popupWidth || fallbackWidth;
		const placed = placeIconPopup(rect, viewportWidth, viewportHeight, width);
		return { left: `${placed.left}px`, bottom: `${placed.bottom}px`, "--arrow-left": `${placed.arrowLeft}px` } as CSSProperties;
	}, [active, desktopZoom, popupItem, popupWidth, snapshot.revision]);

  const renderItem = (item: DesktopIconItem) => <div key={item.id} className={`desktop-icon-wrap desktop-icon-wrap--${item.position.zone}`}>
		<RuntimeIndicator item={item} />
		<button ref={(node) => { if (node) itemRefs.current.set(item.id, node); else itemRefs.current.delete(item.id); }} type="button" className={`desktop-icon desktop-icon--${item.status}`} aria-label={`${item.title}，${previewText(item)}`} aria-expanded={activeID === item.id} onPointerDown={(event) => pointerDown(event, item)} onPointerMove={pointerMove} onPointerUp={(event) => pointerUp(event, item)} onDoubleClick={() => doubleClick(item)} onContextMenu={(event) => { event.preventDefault(); setMenuID(item.id); setActiveID(""); }} onMouseEnter={() => enter(item)} onMouseLeave={() => { window.clearTimeout(hoverTimer.current); if (previewID === item.id) closePreviewSoon(); }} onFocus={() => { if (!activeID) setPreviewID(item.id); }} onBlur={() => { if (!activeID) closePreviewSoon(); }}>
      <span className="desktop-icon__art">{itemGlyph(item)}{(item.status === "running" || item.status === "thinking") && <span className={`desktop-icon__motion desktop-icon__motion--${item.status}`} aria-hidden="true">{item.status === "running" && <><i className="desktop-icon__motion-corner" /><i className="desktop-icon__motion-corner" /><i className="desktop-icon__motion-corner" /><i className="desktop-icon__motion-corner" /></>}</span>}</span>
      <span className="desktop-icon__label">{item.title}</span>
      {item.unreadCount > 0 && <span className="desktop-icon__unread" aria-label={`${item.unreadCount} 条未读`}>{item.unreadCount > 99 ? "99+" : item.unreadCount}</span>}
      {item.activityCount ? <span className="desktop-icon__activity" aria-label={`${item.activityCount} 个活动任务`}>{item.activityCount}</span> : null}
      {statusGlyph(item) && <span className="desktop-icon__status">{statusGlyph(item)}</span>}
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

	// The WG2 anchor is a native Wails window drag handle (CSS
	// --wails-draggable: drag). The cluster itself is pinned to the bottom-right
	// corner of the transparent window, so dragging moves the whole window and
	// the native position restore path owns persistence — no cluster coordinates.
	const toggleCollapsed = () => {
		const next = !collapsed;
		setCollapsed(next);
		if (next) { setActiveID(""); setPreviewID(""); setMenuID(""); }
		writeCollapsedState(next);
	};

  return <main className="desktop-icon-mode" style={zoomStyle} aria-label="WorkGround2 桌面图标小组件" onPointerDown={(event) => { if (event.target === event.currentTarget) { setActiveID(""); setMenuID(""); } }}>
		<span className="sr-only" aria-live="polite">{snapshot.items.reduce((count, item) => count + item.unreadCount, 0)} 条桌面待处理信息</span>
		<div className="desktop-icon-cluster">
			<div className="desktop-icon-grid" id="desktop-icon-grid">
				{!collapsed && <div className="desktop-icon-row desktop-icon-row--top">{rows.top.map(renderItem)}</div>}
				{!collapsed && <div className="desktop-icon-row desktop-icon-row--bottom">{rows.bottom.map(renderItem)}</div>}
			</div>
			<div className="desktop-icon-controls">
				<button type="button" className="desktop-icon-collapse" title={collapsed ? "展开图标组" : "收起图标组"} aria-label={collapsed ? "展开图标组" : "收起图标组"} aria-expanded={!collapsed} aria-controls="desktop-icon-grid" onClick={toggleCollapsed}>{collapsed ? <ChevronUp aria-hidden="true" /> : <ChevronDown aria-hidden="true" />}</button>
				<button type="button" className="desktop-icon-anchor" title="拖动窗口移动小组件" aria-label="移动小组件窗口"><img src={logoSymbol} alt="" draggable={false} /></button>
			</div>
		</div>
		{popupItem && <section ref={popupRef} className={`desktop-icon-popup${active ? " desktop-icon-popup--interactive" : " desktop-icon-popup--preview"}`} style={popupStyle} aria-live={popupItem.status === "failed" || popupItem.status === "needs_input" ? "assertive" : "polite"} onMouseEnter={() => window.clearTimeout(previewCloseTimer.current)} onMouseLeave={closePreviewSoon}>
      <span className="desktop-icon-popup__arrow" aria-hidden="true" />
      {!active && <p>{previewText(popupItem)}</p>}
      {active && active.sourceId === "new" && <QuickStart workspaces={workspaces} initialWorkspace={quickWorkspace} onClose={() => setActiveID("")} />}
      {active && active.sourceId === "search" && <SearchPanel onClose={() => setActiveID("")} onPick={(result) => run(active, "open_search", [result.id])} />}
      {active && active.notifications[0] && <NoticeBody item={active} notice={active.notifications[0]} busy={busy} run={(action, values) => run(active, action, values)} onClose={() => { setActiveID(""); setPreviewID(""); }} />}
      {active && !active.notifications[0] && active.runtimeStatus && <RuntimeBody item={active} busy={busy} run={(action) => void run(active, action)} />}
      {active && !active.notifications[0] && !active.runtimeStatus && active.sourceId !== "new" && active.sourceId !== "search" && <><strong>{active.title}</strong><p>{previewText(active)}</p><div className="desktop-icon-popup__actions"><button onClick={() => void run(active, "open")}>打开</button>{active.kind === "workspace" && <button onClick={() => { setQuickWorkspace(`project:${active.sourceId}`); setActiveID("fixed:new"); }}>在此发起</button>}</div></>}
    </section>}
    {error && <div className="desktop-icon-toast" role="alert">{error}<button aria-label="关闭错误" onClick={() => setError("")}><X /></button></div>}
  </main>;
}
