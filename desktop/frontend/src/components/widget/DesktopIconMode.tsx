import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent } from "react";
import { AtSign, Bot, Bookmark, Check, ChevronDown, ChevronUp, CircleAlert, Code2, ExternalLink, Folder, HelpCircle, Loader2, Pencil, Pin, PinOff, Search, Settings as SettingsIcon, SquareTerminal, Star, Trash2, Users, X, Zap, ZoomIn, ZoomOut } from "lucide-react";
import { app, type DailyRoutine, type DesktopIconActionInput, type DesktopIconActionResult, type DesktopIconDelegation, type DesktopIconItem, type DesktopIconNotice, type DesktopIconPosition, type DesktopIconSearchItem, type DesktopIconSnapshot, type ExternalRunSnapshot, type WidgetWorkspaceOption } from "../../lib/bridge";
import type { QuestionAnswer } from "../../lib/types";
import { asArray } from "../../lib/array";
import { AgentIcon } from "../agent-icon/AgentIcon";
import { buildAgentIconViewModel, isAgentIconItem } from "../../lib/agentIcon/viewModel";
import type { AgentIconViewModel } from "../../lib/agentIcon/types";
import { filterAtMatches } from "../../lib/atMatches";
import { isComposerSubmitKey } from "../../lib/composerKeyboard";
import { activeFileReferenceToken } from "../FileReferenceMenu";
import type { CommandInfo, DirEntry, ModelInfo, SlashArgItem, ToolApprovalMode, VocabularyMatch } from "../../lib/types";
import { acceptVocabulary, type VocabularyToken } from "../../lib/vocabularyCompletion";
import { clusterGridMaxWidth, iconHitRect, ICON_ZOOM_MAX, ICON_ZOOM_MIN, ICON_ZOOM_STEP, normalizeIconZoom, parseCollapseState, parseIconZoom, placeIconPopup, quickStartWorkspaceIndex, scaleIconRect, serializeCollapseState, serializeIconZoom, stepIconZoom, widgetViewportSize } from "./desktopIconLayout";
import logoSymbol from "../../assets/logo-symbol.svg";
import { QUICK_APPROVAL_KEY, QUICK_MODEL_KEY, nextQuickStartApproval, quickStartApprovalLabel, quickStartModelLabel, quickStartModelOptions, quickStartPreferences, resolveQuickStartApproval, resolveQuickStartModel, type QuickStartPreferences } from "./quickStartPreferences";
import { quickStartAcceptCompletion, quickStartAtItems, quickStartCompletionKey, quickStartCompletionMove, quickStartPickMenu, quickStartSkillMatches, quickStartSkillQuery, quickStartSlashMatches, quickStartSlashQuery, quickStartVocabularyToken, type QuickStartCompletion } from "./quickStartCompletion";
import { DRAG_THRESHOLD, IconTimers, windowTimerHost } from "./desktopIconTimers";
import { desktopIconDragOrder, previewDesktopIconMove } from "./desktopIconDrag";
import { IdleHoverTracer } from "./idleHoverTrace";
import { QUICK_DRAFT_KEY, cleanupConsumedDraft, clearConsumedDraftMarker, createQuickStartOpenTaskGate, decideConsumedDraft, isQuickStartJobItem, mergeQuickStartItems, quickStartJobItem, quickStartJobPromptLabel, quickStartJobRequestId, quickStartJobRequestIDFromItem, quickStartJobStateLabel, quickStartJobWorkspaceLabel, recordConsumedDraftMarker, useWidgetQuickStartJobs, type QuickStartConsumedDraftDecision, type QuickStartJob, type QuickStartJobIntent, type WidgetQuickStartJobsApi } from "./widgetQuickStartJobs";
import { resolveWidgetZoomFrame } from "./widgetZoom";
import { deleteConfirmNext, pinnedWorkspaceRows, projectWorkspaceRows, renameTitle, WORKSPACE_PIN_LIMIT, workspacePinsFull, type WorkspaceRow } from "./workspaceManager";
import { applyRoomIcons, applyRoomPins, normalizeRoomIcons, normalizeRoomPins, pinnedRoomRows, ROOM_PIN_LIMIT, roomPinsFull, roomRows, type RoomRow } from "./roomsManager";
import { readRoomIconCount, visibleDesktopIcons, writeRoomIconCount } from "./roomIconCount";
import { clearExternalRunLaunch, prepareExternalRunLaunch, readExternalRunLaunch } from "./externalRunLaunchLedger";
import { consumeRoomPopup, newRoomPopupState, readRoomNotificationMode, reconcileRoomPopups, roomAttentionLabel, writeRoomNotificationMode, type RoomNotificationMode } from "./roomNotifications";
import { consumeBlockPopup, newBlockPopupState, reconcileBlockPopups } from "./blockPopups";
import { AskFlow } from "./desktopIconAsk";
import { isWorkspaceMatteIcon, projectIconKey, WORKSPACE_MATTE_ICON_OPTIONS, type ProjectIconKey, type WorkspaceMatteIconKey } from "../../lib/projectIcons";
import { canRenameTaskIcon } from "./desktopIconRename";
import { WorkspaceMatteIcon } from "./WorkspaceMatteIcon";
import { useT } from "../../lib/i18n";
import { DESKTOP_ICON_OVERLAY_BOUNDS, desktopIconLayoutBounds, useDesktopIconSurface } from "../../lib/desktopIconSurface";
import "./desktop-icon-mode.css";

const QUICK_WORKSPACE_KEY = "wg2.icon-widget-workspace";
const CLUSTER_KEY = "wg2.icon-widget-cluster";
const QUICK_ZOOM_KEY = "wg2.icon-widget-zoom";
const DAILY_ROUTINE_RUN_REQUESTS_KEY = "wg2.daily-routine-run-requests-v1";
const DAILY_ROUTINE_EXTRACT_REQUESTS_KEY = "wg2.daily-routine-extract-requests-v1";
const HIT_REGION_SELECTOR = ".desktop-icon, .desktop-icon-popup, .desktop-icon-menu, .desktop-icon-anchor-menu, .desktop-icon-quick, .desktop-icon-toast, .desktop-icon-anchor, .desktop-icon-collapse";
// Surfaces that own their own pointer handling: clicking them must never be
// treated as an outside click. The document-level outside-click handler closes
// transient anchor UI (quick toolbar / right-click menu / icon popups) only
// when the pointer lands on the desktop background or a container/grid/control
// gap.
const TRANSIENT_PROTECTED_SELECTOR = ".desktop-icon-quick, .desktop-icon-anchor, .desktop-icon-anchor-menu, .desktop-icon-menu, .desktop-icon, .desktop-icon-collapse, .desktop-icon-popup, .desktop-icon-toast";
const TOPMOST_READ_ERROR = "读取置顶状态失败，请重新打开快捷操作条重试";
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
	if (node.matches(".desktop-icon-menu, .desktop-icon-toast, .desktop-icon-anchor-menu, .desktop-icon-quick")) return 30;
	if (node.matches(".desktop-icon")) return 20;
	return 8;
}

function widgetWorkspaceKey(option: WidgetWorkspaceOption): string {
	return option.scope === "auto" ? "auto" : option.scope === "global" ? "global" : `project:${option.root}`;
}

function requestID(prefix: string): string {
  return `${prefix}:${typeof crypto !== "undefined" && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`}`;
}

function readDailyRoutineRequests(key: string): { requests: Map<string, string>; recovered: boolean } {
	try {
		const raw = localStorage.getItem(key);
		if (!raw) return { requests: new Map(), recovered: false };
		const parsed = JSON.parse(raw) as unknown;
		if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("invalid request ledger");
		const entries = Object.entries(parsed).filter((entry): entry is [string, string] => Boolean(entry[0]) && typeof entry[1] === "string" && Boolean(entry[1]));
		if (entries.length !== Object.keys(parsed).length) throw new Error("invalid request ledger entry");
		return { requests: new Map(entries), recovered: false };
	} catch {
		try { localStorage.removeItem(key); } catch { /* the caller surfaces storage recovery failure */ }
		return { requests: new Map(), recovered: true };
	}
}

function writeDailyRoutineRequests(key: string, requests: Map<string, string>): boolean {
	try {
		if (requests.size) localStorage.setItem(key, JSON.stringify(Object.fromEntries(requests)));
		else localStorage.removeItem(key);
		return true;
	} catch {
		return false;
	}
}

function DailyRoutinePanel({ workspaceRoot, onStartHere, onClose }: { workspaceRoot: string; onStartHere: () => void; onClose: () => void }) {
	const t = useT();
	const [routines, setRoutines] = useState<DailyRoutine[]>([]);
	const [loading, setLoading] = useState(true);
	const [runRequestLoad] = useState(() => readDailyRoutineRequests(DAILY_ROUTINE_RUN_REQUESTS_KEY));
	const [error, setError] = useState(() => runRequestLoad.recovered ? t("dailyRoutine.requestStoreRecovered") : "");
	const [busy, setBusy] = useState<Record<string, string>>({});
	const [renaming, setRenaming] = useState("");
	const [renameDraft, setRenameDraft] = useState("");
	const generation = useRef(0);
	const runRequests = useRef(runRequestLoad.requests);

	const load = useCallback(async () => {
		const token = ++generation.current;
		const root = workspaceRoot;
		setLoading(true);
		try {
			const next = await app.ListDailyRoutines(root);
			if (generation.current === token && workspaceRoot === root) {
				setRoutines(next);
				if (!runRequestLoad.recovered) setError("");
			}
		} catch (cause) {
			if (generation.current === token && workspaceRoot === root) setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			if (generation.current === token && workspaceRoot === root) setLoading(false);
		}
	}, [runRequestLoad.recovered, workspaceRoot]);

	useEffect(() => {
		void load();
		return () => { generation.current += 1; };
	}, [load]);

	const setRoutineBusy = (id: string, action: string) => setBusy((current) => ({ ...current, [id]: action }));
	const clearRoutineBusy = (id: string) => setBusy((current) => { const next = { ...current }; delete next[id]; return next; });
	const validResponse = (token: number, root: string) => generation.current === token && workspaceRoot === root;

	const run = async (routine: DailyRoutine) => {
		if (busy[routine.id]) return;
		const root = workspaceRoot;
		const token = generation.current;
		const requestKey = `${root}\u0000${routine.id}`;
		const stableRequest = runRequests.current.get(requestKey) || requestID(`daily-run:${routine.id}`);
		if (!runRequests.current.has(requestKey)) {
			runRequests.current.set(requestKey, stableRequest);
			if (!writeDailyRoutineRequests(DAILY_ROUTINE_RUN_REQUESTS_KEY, runRequests.current)) {
				runRequests.current.delete(requestKey);
				setError(t("dailyRoutine.requestStoreFailed"));
				return;
			}
		}
		setRoutineBusy(routine.id, "run"); setError("");
		try {
			const result = await app.RunDailyRoutine({ workspaceRoot: root, routineId: routine.id, requestId: stableRequest });
			if (!validResponse(token, root)) return;
			if (result.status !== "accepted" && result.status !== "already_applied" && result.status !== "pending") throw new Error(result.error || t("dailyRoutine.runFailed"));
			if (!result.tabId) throw new Error(t("dailyRoutine.missingSession"));
			// Settle the request ledger BEFORE the window switch can unmount this
			// renderer. accepted and already_applied are terminal, and pending
			// still means the Controller acknowledged the turn and the Session
			// exists — the run is visible, so the next explicit click must start
			// a fresh requestId and a new Session. A lost response never reaches
			// this point, so its ledger entry survives and a retry resumes the
			// same receipt instead of submitting a second turn.
			const nextRequests = new Map(runRequests.current);
			nextRequests.delete(requestKey);
			if (!writeDailyRoutineRequests(DAILY_ROUTINE_RUN_REQUESTS_KEY, nextRequests)) {
				setError(t("dailyRoutine.requestStoreCleanupFailed"));
				return;
			}
			runRequests.current = nextRequests;
			try {
				await app.ExitWidgetMode(result.tabId);
			} catch (exitCause) {
				// The Session already exists even when the window switch fails.
				// Restore the ledger so the next click resumes the same receipt
				// (reopen, not re-run) instead of submitting a duplicate Session.
				runRequests.current.set(requestKey, stableRequest);
				if (!writeDailyRoutineRequests(DAILY_ROUTINE_RUN_REQUESTS_KEY, runRequests.current)) {
					throw new Error(`${exitCause instanceof Error ? exitCause.message : String(exitCause)}；${t("dailyRoutine.requestStoreFailed")}`);
				}
				throw exitCause;
			}
		} catch (cause) {
			if (validResponse(token, root)) setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			if (validResponse(token, root)) clearRoutineBusy(routine.id);
		}
	};

	const rename = async (routine: DailyRoutine) => {
		const name = renameDraft.trim();
		if (!name || busy[routine.id]) return;
		const root = workspaceRoot;
		const token = generation.current;
		setRoutineBusy(routine.id, "rename"); setError("");
		try {
			const result = await app.RenameDailyRoutine({ workspaceRoot: root, routineId: routine.id, name });
			if (!validResponse(token, root)) return;
			if (result.status !== "accepted" && result.status !== "already_applied") throw new Error(result.error || t("dailyRoutine.renameFailed"));
			setRenaming(""); setRenameDraft("");
			clearRoutineBusy(routine.id);
			await load();
		} catch (cause) {
			if (validResponse(token, root)) setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			if (validResponse(token, root)) clearRoutineBusy(routine.id);
		}
	};

	const remove = async (routine: DailyRoutine) => {
		if (busy[routine.id]) return;
		const root = workspaceRoot;
		const token = generation.current;
		setRoutineBusy(routine.id, "delete"); setError("");
		try {
			const result = await app.DeleteDailyRoutine({ workspaceRoot: root, routineId: routine.id });
			if (!validResponse(token, root)) return;
			if (result.status !== "accepted" && result.status !== "already_applied") throw new Error(result.error || t("dailyRoutine.deleteFailed"));
			setRoutines((current) => current.filter((item) => item.id !== routine.id));
		} catch (cause) {
			if (validResponse(token, root)) setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			if (validResponse(token, root)) clearRoutineBusy(routine.id);
		}
	};

	return <div className="desktop-icon-popup__routines">
		<div className="desktop-icon-popup__workspace-head"><strong>{t("dailyRoutine.title")}</strong></div>
		{loading && <p className="desktop-icon-popup__workspace-note">{t("dailyRoutine.loading")}</p>}
		{!loading && routines.length === 0 && <p className="desktop-icon-popup__workspace-note">{t("dailyRoutine.empty")}</p>}
		{error && <div className="desktop-icon-popup__routine-error" role="alert"><span>{error}</span><button type="button" onClick={() => void load()}>{t("common.retry")}</button></div>}
		<div className="desktop-icon-popup__routine-list">{routines.map((routine) => <div key={routine.id} className="desktop-icon-popup__routine-row">
			{renaming === routine.id ? <div className="desktop-icon-popup__routine-rename"><input autoFocus value={renameDraft} disabled={Boolean(busy[routine.id])} aria-label={t("dailyRoutine.renameAria", { name: routine.name })} onChange={(event) => setRenameDraft(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.nativeEvent.isComposing) void rename(routine); if (event.key === "Escape") { setRenaming(""); setRenameDraft(""); } }} /><button type="button" disabled={!renameDraft.trim() || Boolean(busy[routine.id])} onClick={() => void rename(routine)}>{t("common.save")}</button><button type="button" className="subtle" disabled={Boolean(busy[routine.id])} onClick={() => { setRenaming(""); setRenameDraft(""); }}>{t("common.cancel")}</button></div> : <>
				<div className="desktop-icon-popup__routine-copy"><strong>{routine.name}</strong><small>{routine.goal}</small></div>
				<div className="desktop-icon-popup__routine-actions"><button type="button" disabled={Boolean(busy[routine.id])} onClick={() => void run(routine)}>{busy[routine.id] === "run" ? t("dailyRoutine.starting") : t("dailyRoutine.run")}</button><button type="button" className="subtle" disabled={Boolean(busy[routine.id])} aria-label={t("dailyRoutine.renameAria", { name: routine.name })} onClick={() => { setRenaming(routine.id); setRenameDraft(routine.name); setError(""); }}><Pencil aria-hidden="true" /></button><button type="button" className="subtle danger" disabled={Boolean(busy[routine.id])} aria-label={t("dailyRoutine.deleteAria", { name: routine.name })} onClick={() => void remove(routine)}><Trash2 aria-hidden="true" /></button></div>
			</>}
		</div>)}</div>
		<div className="desktop-icon-popup__workspace-foot"><button type="button" className="subtle" onClick={onClose}>{t("common.close")}</button><button type="button" onClick={onStartHere}>{t("dailyRoutine.startHere")}</button></div>
	</div>;
}

function readLocalStorage(key: string): string {
  try { return localStorage.getItem(key) || ""; } catch { return ""; }
}

function writeLocalStorage(key: string, value: string): void {
  try { localStorage.setItem(key, value); } catch { /* best-effort; callers degrade to in-memory state */ }
}

function readCollapsedState(): boolean {
  try { return parseCollapseState(localStorage.getItem(CLUSTER_KEY)); }
  catch { return false; }
}

function writeCollapsedState(collapsed: boolean): void {
  try { localStorage.setItem(CLUSTER_KEY, serializeCollapseState(collapsed)); } catch { /* storage unavailable */ }
}

function readClusterZoom(): number {
  try { return parseIconZoom(localStorage.getItem(QUICK_ZOOM_KEY)); }
  catch { return 1; }
}

function writeClusterZoom(zoom: number): void {
  try { localStorage.setItem(QUICK_ZOOM_KEY, serializeIconZoom(zoom)); } catch { /* storage unavailable */ }
}

function statusGlyph(item: DesktopIconItem) {
  if (item.status === "done") return <Check aria-hidden="true" />;
  if (item.status === "failed") return <CircleAlert aria-hidden="true" />;
  if (item.status === "needs_input") return <HelpCircle aria-hidden="true" />;
  if (item.status === "needs_confirm") return <CircleAlert aria-hidden="true" />;
  return null;
}

function itemGlyph(item: DesktopIconItem, agentViewModel?: AgentIconViewModel) {
  if (item.kind === "room") return <><RoomGlyph icon={projectIconKey(item.icon)} matteClassName="desktop-icon__matte" />{item.notifications.some((notice) => notice.attention) && <AtSign className="desktop-icon__room-mention" aria-hidden="true" />}</>;
  if (item.kind === "person") return agentViewModel ? <AgentIcon viewModel={agentViewModel} /> : <Users />;
  // 真实 task/session 使用可组合 Agent Icon（身份/任务/徽标/状态眼睛）；
  // QuickStart 乐观条目尚未形成真实 session，保留旧 Bot 图标。
  if (item.kind === "task") return agentViewModel ? <AgentIcon viewModel={agentViewModel} /> : <Bot />;
  if (item.kind === "external" || item.sourceId === "dsh") return <SquareTerminal />;
  if (item.kind === "workspace") return <WorkspaceMatteIcon icon={isWorkspaceMatteIcon(item.icon) ? item.icon : "folder"} className="desktop-icon__matte" />;
  if (item.sourceId === "assistant") return <WorkspaceMatteIcon icon="robot" className="desktop-icon__matte" />;
  const fixedIcon: Record<string, WorkspaceMatteIconKey> = {
    new: "new",
    workspace: "folder",
    rooms: "discussion",
    delegate: "delegate",
    knowledge: "document",
    search: "research",
  };
  return <WorkspaceMatteIcon icon={fixedIcon[item.sourceId] ?? "research"} className="desktop-icon__matte" />;
}

function ExternalRunBody({ item, busy, run }: { item: DesktopIconItem; busy: boolean; run: (action: string) => void }) {
	const terminal = item.status === "done" || item.status === "failed";
	return <>
		<div className="desktop-icon-popup__eyebrow">DSH · {terminal ? "已结算" : "外部任务"}</div>
		<strong>{item.title}</strong>
		<p>{item.runtimeStatus?.summary || item.subtitle || (item.status === "failed" ? "任务失败或已中断" : "任务状态已更新")}</p>
		{item.runtimeStatus && <div className="desktop-icon-popup__facts"><span>{item.runtimeStatus.phase}</span><span>{Math.max(0, Math.round(item.runtimeStatus.elapsedMs / 1000))} 秒</span><span>{item.subtitle}</span></div>}
		<div className="desktop-icon-popup__actions">
			{item.actions?.includes("cancel") && <button disabled={busy} className="danger" onClick={() => run("cancel")}>取消 DSH 任务</button>}
			{!item.actions?.length && <small>DSH rc.8 未提供 Open、Retry、Resume、Approve 或 Send。</small>}
		</div>
	</>;
}

function previewText(item: DesktopIconItem): string {
  if (isQuickStartJobItem(item)) return quickStartJobStateLabel(item);
  if (item.runtimeStatus) return `${item.runtimeStatus.summary || item.runtimeStatus.phase} · ${Math.max(0, Math.round(item.runtimeStatus.elapsedMs / 1000))} 秒`;
  const noticeBody = item.notifications[0]?.body.trim();
  if (noticeBody) return noticeBody;
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

function NoticeBody({ item, notice, busy, run, onClose }: { item: DesktopIconItem; notice: DesktopIconNotice; busy: boolean; run: (action: string, values?: string[], answers?: QuestionAnswer[]) => Promise<DesktopIconActionResult["status"]>; onClose: () => void }) {
  const [answer, setAnswer] = useState("");
	const [selected, setSelected] = useState("");
	const [reply, setReply] = useState("");
	const [followup, setFollowup] = useState("");
	const [failedFollowup, setFailedFollowup] = useState("");
	const composingRef = useRef(false);
	const lastCompositionEndAt = useRef(0);
	const followupSent = useRef(false);
  const needsAnswer = notice.kind === "needs_input";
  const completion = notice.kind === "completed" || notice.kind === "failed";
	const attention = notice.kind === "message" ? notice.attention : undefined;
	const blocking = needsAnswer || notice.kind === "needs_confirm";
	const questions = notice.questions;
	const askFlow = needsAnswer && (questions?.length ?? 0) > 0;
	// The continuation input stays resident on completion notices. Sending
	// guards busy/empty and a same-tick double submit (button + Ctrl+Enter).
	// A retryable error freezes the exact submitted text: retry must reuse both
	// that text and run()'s stable requestId until the backend accepts it.
	const sendFollowup = async () => {
		const text = failedFollowup || followup.trim();
		if (busy || !text || followupSent.current) return;
		followupSent.current = true;
		const freezeRetry = () => {
			setFollowup(text);
			setFailedFollowup(text);
			followupSent.current = false;
		};
		try {
			const status = await run("continue", [text]);
			if (status === "retryable_error") {
				freezeRetry();
			} else if (status !== "accepted" && status !== "already_applied") {
				followupSent.current = false;
			}
		} catch {
			// The prop is async and may reject before the shared run path can
			// classify the failure. Treat it as retryable: keep the exact intent,
			// release the guard, and never leak an unhandled rejection.
			freezeRetry();
		}
	};
  return <div className="desktop-icon-popup__scroll" tabIndex={0} role="region" aria-label="任务通知详情">
    <div className={`desktop-icon-popup__eyebrow${attention ? " desktop-icon-popup__eyebrow--mention" : ""}`}>{attention && <AtSign aria-hidden="true" />}{notice.title || roomAttentionLabel(attention)}{blocking && <span className="desktop-icon-popup__block">{needsAnswer ? "待回答" : "待确认"}</span>}</div>
    <strong>{item.title}</strong>
    {!askFlow && <p>{notice.body}</p>}
    {askFlow && questions && <AskFlow questions={questions} busy={busy} onAnswer={(answers) => void run("answer", [], answers)} />}
    {needsAnswer && !askFlow && <div className="desktop-icon-popup__answers">
		{asArray(notice.options).map((option) => <button key={option.value} type="button" aria-pressed={selected === option.value} disabled={busy} onClick={() => { setSelected(option.value); setAnswer(""); }}><span>{option.label}</span>{option.description && <small>{option.description}</small>}</button>)}
      <label><span className="sr-only">自定义回答</span><input value={answer} disabled={busy} placeholder="自定义回答" onChange={(event) => setAnswer(event.target.value)} /></label>
    </div>}
	{notice.kind === "message" && (item.kind === "room" || item.kind === "person") && <label className="desktop-icon-popup__reply"><span className="sr-only">快速回复</span><input value={reply} disabled={busy} placeholder="快速回复" onChange={(event) => setReply(event.target.value)} /></label>}
    <div className={`desktop-icon-popup__actions${completion ? " desktop-icon-popup__actions--completion" : ""}`}>
		{needsAnswer && !askFlow && <button disabled={busy || !(answer.trim() || selected)} onClick={() => run("answer", [answer.trim() || selected])}>提交回答</button>}
      {notice.kind === "needs_confirm" && <><button disabled={busy} onClick={() => run("approve")}>允许</button><button disabled={busy} onClick={() => run("deny")}>拒绝</button></>}
      {notice.kind === "failed" && notice.retryable && <button disabled={busy} onClick={() => run("retry")}>重试</button>}
      {completion && <><button type="button" className="desktop-icon-popup__ok" onClick={onClose}>OK</button><button type="button" className="desktop-icon-popup__detail" disabled={busy} onClick={() => run("open")}>Detail</button><button type="button" className="desktop-icon-popup__dismiss" disabled={busy} onClick={() => run("dismiss")}>Dismiss</button></>}
      {(needsAnswer || notice.kind === "needs_confirm") && <button disabled={busy} className="subtle" onClick={() => run("later")}>稍后处理</button>}
		{notice.kind === "message" && (item.kind === "room" || item.kind === "person") && <button disabled={busy || !reply.trim()} onClick={() => run("reply", [reply.trim()])}>回复</button>}
		{notice.kind === "message" && <button disabled={busy} onClick={() => run("open")}>打开会话</button>}
		{notice.kind === "message" && item.kind === "person" && <button disabled={busy} className="danger" onClick={() => run("remove")}>移除</button>}
    </div>
	{completion && <div className="desktop-icon-popup__continue" aria-busy={busy}>
		<label><span className="sr-only">继续当前任务</span><textarea value={followup} disabled={busy} readOnly={Boolean(failedFollowup)} placeholder="告诉 WorkGround2 接下来要完成什么…" aria-label="继续当前任务" aria-describedby={failedFollowup ? "desktop-icon-followup-error" : "desktop-icon-followup-hint"} onChange={(event) => setFollowup(event.target.value)} onKeyDown={(event) => {
			if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); return; }
			if (event.key === "Enter" && (event.ctrlKey || event.metaKey) && !isWidgetImeKeyEvent(event, composingRef.current, lastCompositionEndAt.current)) { event.preventDefault(); void sendFollowup(); }
		}} onCompositionStart={() => { composingRef.current = true; }} onCompositionEnd={() => { composingRef.current = false; lastCompositionEndAt.current = Date.now(); }} /></label>
		{failedFollowup && <small id="desktop-icon-followup-error" role="status" className="desktop-icon-popup__continue-error">发送失败，可重试原内容</small>}
		<div className="desktop-icon-popup__continue-actions"><button type="button" disabled={busy || !followup.trim()} onClick={() => void sendFollowup()}>{busy ? "发送中…" : failedFollowup ? "重试发送" : "发送"}</button><small id={failedFollowup ? undefined : "desktop-icon-followup-hint"}>{failedFollowup ? "原内容已锁定" : "Ctrl+Enter 发送，Enter 换行"}</small></div>
	</div>}
    {completion && notice.summaryStatus === "failed" && <small className="desktop-icon-popup__summary-failed">摘要生成失败，稍后自动重试</small>}
    {completion && <small className="desktop-icon-popup__history">记录仍可在搜索中找到</small>}
  </div>;
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

// QuickStartJobBody is the popup for an optimistic task icon. A failed job is
// the only terminal state with retry/edit/dismiss actions: retry replays the
// frozen requestId, edit opens QuickStart prefilled (a new requestId on
// submit), and dismiss removes the entry. An accepted job stays until its
// real task:<tabId> icon appears or the user dismisses it, and opens the real
// task (ExitWidgetMode(tabId)) when available. A running job can NEVER be
// dismissed or deleted: it is the only durable recovery intent until the
// backend receipt exists, so the popup only reports 后台发送中，请等待 and offers
// an open-main action.
function QuickStartJobBody({ job, onRetry, onEdit, onDismiss, onOpenMain, onOpenTask }: { job: QuickStartJob | undefined; onRetry: (requestId: string) => void; onEdit: (job: QuickStartJob) => void; onDismiss: (requestId: string) => void; onOpenMain: () => void; onOpenTask: (() => void) | undefined }) {
	if (!job) return null; // reconciled into the real task icon while open
	const failed = job.phase === "failed";
	// Only failed and accepted (with a real backend tabId) entries may be
	// dismissed; a running job's ledger entry must never be removed.
	const dismissible = failed || (job.phase === "accepted" && Boolean(job.tabId));
	return <>
		<div className="desktop-icon-popup__eyebrow">{failed ? "发送失败" : job.phase === "accepted" ? "正在运行" : "后台发送中"}</div>
		<strong>{quickStartJobPromptLabel(job.intent)}</strong>
		{failed ? <>
			<p role="alert" className="desktop-icon-popup__error">{job.error || "发起失败，可安全重试"}</p>
			<div className="desktop-icon-popup__job-facts"><span>{quickStartJobWorkspaceLabel(job.intent.workspace)}</span><span>{job.intent.model ? quickStartModelLabel(job.intent.model) : "默认模型"}</span><span>{quickStartApprovalLabel((job.intent.approvalMode || "ask") as ToolApprovalMode)}</span></div>
			<div className="desktop-icon-popup__actions desktop-icon-popup__actions--quick">
				<button onClick={() => onRetry(job.requestId)}>重试</button>
				<button className="subtle" onClick={() => onEdit(job)}>编辑</button>
				<button className="subtle" onClick={() => onDismiss(job.requestId)}>丢弃</button>
			</div>
		</> : <>
			<p>{job.phase === "accepted" ? "任务已提交，正在同步为任务图标。" : "后台发送中，请等待；完成后会显示为任务图标。"}</p>
			<div className="desktop-icon-popup__actions desktop-icon-popup__actions--quick">
				{job.phase === "accepted" && onOpenTask && <button onClick={onOpenTask}>打开任务</button>}
				{/* A running job is the only durable recovery intent: it cannot be
				    deleted, only followed up in the main window. */}
				{job.phase === "running" && <button onClick={onOpenMain}>打开主窗口</button>}
				{dismissible && <button className="subtle" onClick={() => onDismiss(job.requestId)}>丢弃</button>}
			</div>
		</>}
	</>;
}

function QuickStart({ workspaces, initialWorkspace = "", editJob = null, initialDraft = "", submitJob, openWindowCreate, onClose }: { workspaces: WidgetWorkspaceOption[]; initialWorkspace?: string; editJob?: QuickStartJob | null; initialDraft?: string; submitJob: WidgetQuickStartJobsApi["submit"]; openWindowCreate: (workspace: string, requestId: string) => Promise<void>; onClose: () => void }) {
	const t = useT();
  const choices = workspaces.length ? workspaces : [{ scope: "auto", name: "自动" } as WidgetWorkspaceOption];
	const keys = useMemo(() => choices.map(widgetWorkspaceKey), [workspaces]);
	const keysToken = keys.join("\n");
	const initializedKeys = useRef(keysToken);
	const [index, setIndex] = useState(() => quickStartWorkspaceIndex(keys, editJob?.intent.workspace ?? "", initialWorkspace, readLocalStorage(QUICK_WORKSPACE_KEY)));
	const [draft, setDraft] = useState(() => editJob ? editJob.intent.prompt : initialDraft);
  const [error, setError] = useState("");
	const [preferences, setPreferences] = useState<QuickStartPreferences | null>(null);
	const [preferencesError, setPreferencesError] = useState("");
	const [models, setModels] = useState<ModelInfo[]>([]);
	const [model, setModel] = useState(() => editJob?.intent.model || readLocalStorage(QUICK_MODEL_KEY));
	const [approval, setApproval] = useState(() => editJob?.intent.approvalMode || readLocalStorage(QUICK_APPROVAL_KEY));
	const [modelMenuOpen, setModelMenuOpen] = useState(false);
	const [modelQuery, setModelQuery] = useState("");
	const [completion, setCompletion] = useState<QuickStartCompletion | null>(null);
	const [completionDismissed, setCompletionDismissed] = useState(false);
	const [caret, setCaret] = useState({ start: 0, end: 0 });
	const [commands, setCommands] = useState<CommandInfo[]>([]);
	const composingRef = useRef(false);
	const lastCompositionEndAt = useRef(0);
	const preferencesLoad = useRef(0);
	const sentRef = useRef(false);
	const taRef = useRef<HTMLTextAreaElement>(null);
	// open-window create keeps its own in-flight guard and a stable requestId
	// keyed to the current intent, so a retry after a lost response or a failed
	// window switch replays the same backend receipt instead of duplicating.
	const openWindowRef = useRef(false);
	const openWindowIntentRef = useRef<{ requestId: string; key: string } | null>(null);
	const [openWindowBusy, setOpenWindowBusy] = useState(false);
  const choice = choices[index % choices.length];
  const workspace = widgetWorkspaceKey(choice);
	useEffect(() => {
		if (initializedKeys.current === keysToken) return;
		initializedKeys.current = keysToken;
		setIndex(quickStartWorkspaceIndex(keys, editJob?.intent.workspace ?? "", initialWorkspace, readLocalStorage(QUICK_WORKSPACE_KEY)));
	}, [editJob, initialWorkspace, keys, keysToken]);
	useEffect(() => { if (workspaces.length) writeLocalStorage(QUICK_WORKSPACE_KEY, workspace); }, [workspace, workspaces.length]);
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
	const switchBy = (delta: number) => { setIndex((current) => (current + delta + choices.length) % choices.length); };
	useEffect(() => {
		let frame = 0;
		let previous = { lt: false, rt: false };
		const poll = () => {
			const pad = typeof navigator.getGamepads === "function" ? Array.from(navigator.getGamepads()).find(Boolean) : null;
			const lt = Boolean(pad?.buttons[6]?.pressed), rt = Boolean(pad?.buttons[7]?.pressed);
			if ((lt && !previous.lt) || (rt && !previous.rt)) {
				const delta = lt ? -1 : 1;
				setIndex((current) => (current + delta + choices.length) % choices.length);
			}
			previous = { lt, rt };
			frame = requestAnimationFrame(poll);
		};
		frame = requestAnimationFrame(poll);
		return () => cancelAnimationFrame(frame);
	}, [choices.length]);
  const saveDraft = (text: string) => {
		setDraft(text);
		writeLocalStorage(QUICK_DRAFT_KEY, text);
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
	const pickModel = (ref: string) => { setModel(ref); writeLocalStorage(QUICK_MODEL_KEY, ref); setModelMenuOpen(false); setModelQuery(""); };
	const pickApproval = (mode: ToolApprovalMode) => { setApproval(mode); writeLocalStorage(QUICK_APPROVAL_KEY, mode); setModelMenuOpen(false); };

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

  // send validates and enqueues synchronously: the modal closes and the
	// optimistic icon appears immediately, while the job runner delivers in the
	// background. A validation or ledger persistence failure keeps the modal
	// open and exposes the error (draft preserved). sentRef makes a same-tick
	// double submit (button + Enter) a single request. The stored draft is
	// cleared/guarded by the parent's submit wrapper (best-effort): a cleanup
	// failure never blocks the modal close or duplicates the dispatch.
  const send = () => {
    const prompt = draft.trim();
		if (!prompt || !preferences || sentRef.current || openWindowRef.current || openWindowBusy) return;
		sentRef.current = true;
    const submitted = submitJob(
			{ prompt, workspace, model: selectedModel, approvalMode: selectedApproval },
			editJob ? { replacesRequestId: editJob.requestId } : undefined,
		);
		if (!submitted.ok) { setError(submitted.error); sentRef.current = false; return; }
		setDraft("");
		onClose();
  };
  // openWindow creates a NORMAL blank Session in the selected workspace through
	// the shared backend workspace-open path (the same semantics as double-
	// clicking a Workspace icon) and then exits the widget focusing the returned
	// tab. It never enqueues an optimistic quick-start job and never carries the
	// QuickStart draft/model/approval (those belong to "send"), so the new
	// Session lands in Session List directly. The requestId is stable for the
	// exact same workspace and regenerates when the workspace changes, so a
	// retry after a lost response or failed window switch replays the same
	// backend receipt instead of duplicating the Session.
  const openWindow = async () => {
		if (sentRef.current || openWindowRef.current || openWindowBusy) return;
		const key = workspace;
		if (!openWindowIntentRef.current || openWindowIntentRef.current.key !== key) {
			openWindowIntentRef.current = { requestId: quickStartJobRequestId("icon-window-new"), key };
		}
		openWindowRef.current = true;
		setOpenWindowBusy(true);
		setError("");
		try {
			await openWindowCreate(workspace, openWindowIntentRef.current.requestId);
			onClose();
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			openWindowRef.current = false;
			setOpenWindowBusy(false);
		}
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
    <div className="desktop-icon-popup__workspace"><button aria-label="上一个 Workspace（LT 或 Ctrl+←）" title="上一个（LT / Ctrl+←）" onClick={() => switchBy(-1)}>Ctrl + ←</button><strong>{choice.name} · {index + 1} / {choices.length}</strong><button aria-label="下一个 Workspace（RT 或 Ctrl+→）" title="下一个（RT / Ctrl+→）" onClick={() => switchBy(1)}>Ctrl + →</button></div>
		<div className="desktop-icon-popup__quick-meta">
			<div className="desktop-icon-popup__quick-chip-wrap">
				<button type="button" className="desktop-icon-popup__quick-chip" aria-label="选择模型" aria-haspopup="listbox" aria-expanded={modelMenuOpen} disabled={!preferences} onClick={() => setModelMenuOpen((open) => !open)}>
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
				<button type="button" className="desktop-icon-popup__quick-chip" aria-label={`审批：${preferences ? quickStartApprovalLabel(selectedApproval) : "读取中"}，点击切换`} disabled={!preferences} onClick={() => pickApproval(nextQuickStartApproval(selectedApproval))}>
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
				if (draft.trim()) send();
			}
			}} onCompositionStart={() => { composingRef.current = true; setCompletionDismissed(true); setVocabMatch(null); }} onCompositionEnd={(event) => { composingRef.current = false; lastCompositionEndAt.current = Date.now(); setCaret({ start: event.currentTarget.selectionStart ?? draft.length, end: event.currentTarget.selectionEnd ?? draft.length }); }} />
			{vocabMatch && <span id="desktop-icon-vocab-hint" className="sr-only">按 Tab 补全为 {vocabMatch.text}</span>}
		</div>
		{preferencesError && <div role="alert" className="desktop-icon-popup__settings-error"><span>读取新会话设置失败：{preferencesError}</span><button type="button" className="subtle" onClick={loadPreferences}>重试</button></div>}
    {error && <p role="alert" className="desktop-icon-popup__error">{error}</p>}
		<div className="desktop-icon-popup__actions desktop-icon-popup__actions--quick"><button disabled={!draft.trim() || !preferences || openWindowBusy} onClick={send}>{!preferences ? "读取设置…" : "发送"}</button><button type="button" className="subtle desktop-icon-popup__open-window" disabled={openWindowBusy} aria-label={openWindowBusy ? t("widget.openWindowCreating") : t("widget.openWindowCreate")} title={openWindowBusy ? t("widget.openWindowCreating") : t("widget.openWindowCreate")} onClick={() => void openWindow()}>{openWindowBusy ? <Loader2 aria-hidden="true" /> : <ExternalLink aria-hidden="true" />}</button><button className="subtle" onClick={onClose}>取消</button>{preferences && <small className="desktop-icon-popup__submit-hint">{preferences.submitKey === "ctrl_enter" ? "Ctrl+Enter 发送" : "Enter 发送"}</small>}</div>
  </div>;
}

function DSHQuickStart({ workspaces, onChanged, onClose }: { workspaces: WidgetWorkspaceOption[]; onChanged: () => Promise<void>; onClose: () => void }) {
	const pending = useMemo(() => {
		try { return readExternalRunLaunch(localStorage); } catch { return null; }
	}, []);
	const [profile, setProfile] = useState<ExternalRunSnapshot | null>(null);
	const [workspace, setWorkspace] = useState(pending?.workspace || "");
	const choices = useMemo(() => {
		const values: Array<{ name: string; root: string }> = [];
		const add = (name: string, root: string) => {
			root = root.trim();
			if (root && !values.some((item) => item.root === root)) values.push({ name, root });
		};
		add("当前 Workspace", profile?.workspace || "");
		workspaces.filter((item) => item.scope === "project" && item.root).forEach((item) => add(item.name, item.root || ""));
		// A lost-response packet remains replayable even if its project was
		// removed from the current tree before Desktop restarted.
		if (pending?.workspace) add("待恢复 Workspace", pending.workspace);
		return values;
	}, [pending?.workspace, profile?.workspace, workspaces]);
	useEffect(() => {
		if (!workspace && profile?.workspace) setWorkspace(profile.workspace);
	}, [profile?.workspace, workspace]);
	const index = Math.max(0, choices.findIndex((choice) => choice.root === workspace));
	const [prompt, setPrompt] = useState(pending?.prompt || "");
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState("");
	const sent = useRef(false);
	const load = useCallback(() => {
		setError("");
		return app.GetExternalRunSnapshot().then(setProfile).catch((cause) => setError(cause instanceof Error ? cause.message : String(cause)));
	}, []);
	useEffect(() => { void load(); }, [load]);
	const choice = choices[index];
	const switchBy = (delta: number) => {
		if (!choices.length) return;
		setWorkspace(choices[(index + delta + choices.length) % choices.length].root);
	};
	const submit = async () => {
		if (busy || sent.current || !profile?.dsh.ready || !choice?.root) return;
		sent.current = true;
		setBusy(true);
		setError("");
		try {
			const packet = prepareExternalRunLaunch(localStorage, choice.root, prompt, () => requestID("dsh"));
			const result = await app.LaunchDSHRun({ requestId: packet.requestId, workspace: packet.workspace, prompt: packet.prompt });
			if (result.receipt.status !== "accepted" && result.receipt.status !== "already_applied") {
				throw new Error(result.receipt.message || `DSH 启动失败：${result.receipt.status}`);
			}
			try { clearExternalRunLaunch(localStorage, packet.requestId); } catch { /* replay remains safe */ }
			await onChanged();
			onClose();
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setBusy(false);
			sent.current = false;
		}
	};
	const profileError = profile?.dsh.error || profile?.dsh.missing?.map((item) => item.detail).join("；") || profile?.error || "";
	return <div className="desktop-icon-popup__quick desktop-icon-popup__dsh">
		<div className="desktop-icon-popup__workspace"><button disabled={busy || choices.length < 2} onClick={() => switchBy(-1)}>Ctrl + ←</button><strong>{choice ? `${choice.name} · ${index + 1} / ${choices.length}` : "没有可用 Workspace"}</strong><button disabled={busy || choices.length < 2} onClick={() => switchBy(1)}>Ctrl + →</button></div>
		<div className="desktop-icon-popup__quick-meta">
			<div className="desktop-icon-popup__quick-chip-wrap"><button type="button" className="desktop-icon-popup__quick-chip" disabled><SquareTerminal /><span className="desktop-icon-popup__quick-chip-copy"><small>运行时</small><strong>{profile?.dsh.ready ? `DSH ${profile.dsh.version || "rc.8"}` : "DSH 未就绪"}</strong></span></button></div>
			<div className="desktop-icon-popup__quick-chip-wrap"><button type="button" className="desktop-icon-popup__quick-chip" disabled><Folder /><span className="desktop-icon-popup__quick-chip-copy"><small>Workspace</small><strong>{choice?.name || "未选择"}</strong></span></button></div>
		</div>
		<div className="desktop-icon-popup__quick-composer"><textarea autoFocus value={prompt} disabled={busy} placeholder="告诉 DSH 在所选 Workspace 中完成什么…" onChange={(event) => setPrompt(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) { event.preventDefault(); void submit(); } }} /></div>
		<p className="desktop-icon-popup__dsh-warning">DSH 将按本地 cordis.yml 权限配置执行；当前 rc.8 仅支持取消，无法从 WorkGround2 恢复或批准。</p>
		{profileError && <div role="alert" className="desktop-icon-popup__settings-error"><span>{profileError}</span><button type="button" className="subtle" onClick={() => void load()}>重试探针</button></div>}
		{error && <p role="alert" className="desktop-icon-popup__error">{error}</p>}
		<div className="desktop-icon-popup__actions desktop-icon-popup__actions--quick"><button disabled={busy || !choice?.root || !prompt.trim() || !profile?.dsh.ready} onClick={() => void submit()}>{busy ? "启动中…" : "启动 DSH"}</button><button className="subtle" disabled={busy} onClick={onClose}>取消</button><small>Ctrl+Enter 启动</small></div>
	</div>;
}

function SearchPanel({ onClose, onPick }: { onClose: () => void; onPick: (item: DesktopIconSearchItem) => Promise<unknown> }) {
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
	return <div className="desktop-icon-popup__search">
		<div className="desktop-icon-popup__searchbox"><Search /><input autoFocus value={query} disabled={opening} placeholder="搜索历史任务、Room、Workspace" onChange={(event) => setQuery(event.target.value)} /><button aria-label="关闭搜索" disabled={opening} onClick={onClose}><X /></button></div>
		<div className="desktop-icon-popup__search-content" role="region" aria-label="搜索结果" aria-busy={loading || opening}>
			{error
				? <p role="alert" className="desktop-icon-popup__error">{error}</p>
				: loading
					? <p role="status" className="desktop-icon-popup__empty">搜索中…</p>
				: results.length
					? <div className="desktop-icon-popup__results" role="listbox" aria-label="匹配结果">{results.map((item) => <button key={item.id} role="option" disabled={opening} onClick={() => { setOpening(true); void onPick(item).finally(() => setOpening(false)); }}><span>{item.title}</span><small>{item.subtitle || item.kind}</small></button>)}</div>
					: <p role="status" className="desktop-icon-popup__empty">没有匹配结果</p>}
		</div>
	</div>;
}

function DelegationPanel({ items, error, busy, onClose, onPick }: { items: DesktopIconDelegation[]; error?: string; busy: boolean; onClose: () => void; onPick: (item: DesktopIconDelegation) => Promise<unknown> }) {
	return <div className="desktop-icon-popup__delegations">
		<div className="desktop-icon-popup__delegation-head"><strong>正在运行的委托</strong><button type="button" aria-label="关闭委托列表" disabled={busy} onClick={onClose}><X /></button></div>
		{error && <p role="alert" className="desktop-icon-popup__delegation-error">委托扫描失败：{error}。列表保留已读取结果，将自动重试。</p>}
		{items.length > 0
			? <div className="desktop-icon-popup__delegation-list" role="list" aria-busy={busy}>{items.map((item) => <button type="button" role="listitem" key={item.id} disabled={busy} onClick={() => void onPick(item)}><span>{item.content}</span><small><b>{item.status === "running" ? "运行中" : item.status}</b> · {item.sessionTitle}{item.workspaceName ? ` · ${item.workspaceName}` : ""}</small></button>)}</div>
			: <p role="status" className="desktop-icon-popup__empty">当前没有运行中的委托</p>}
	</div>;
}

// WorkspaceGlyph maps a normalized projectIconKey to the same Lucide glyphs
// ProjectTree uses; "" and any unknown key fall back to the plain folder.
function WorkspaceGlyph({ icon, size = 16 }: { icon: ProjectIconKey; size?: number }) {
  if (isWorkspaceMatteIcon(icon)) return <WorkspaceMatteIcon icon={icon} className="desktop-icon-popup__workspace-matte" />;
  switch (icon) {
    case "star": return <Star size={size} aria-hidden="true" />;
    case "bookmark": return <Bookmark size={size} aria-hidden="true" />;
    case "code": return <Code2 size={size} aria-hidden="true" />;
    case "terminal": return <SquareTerminal size={size} aria-hidden="true" />;
    case "bolt": return <Zap size={size} aria-hidden="true" />;
    default: return <Folder size={size} aria-hidden="true" />;
  }
}

// A Room with no explicit preference uses the shared social matte asset;
// configured values share the workspace icon catalog and legacy compatibility.
function RoomGlyph({ icon, size = 16, matteClassName = "desktop-icon-popup__workspace-matte" }: { icon: ProjectIconKey; size?: number; matteClassName?: string }) {
  if (isWorkspaceMatteIcon(icon)) return <WorkspaceMatteIcon icon={icon} className={matteClassName} />;
  switch (icon) {
    case "star": return <Star size={size} aria-hidden="true" />;
    case "bookmark": return <Bookmark size={size} aria-hidden="true" />;
    case "code": return <Code2 size={size} aria-hidden="true" />;
    case "terminal": return <SquareTerminal size={size} aria-hidden="true" />;
    case "bolt": return <Zap size={size} aria-hidden="true" />;
    default: return <WorkspaceMatteIcon icon="social" className={matteClassName} />;
  }
}

function WorkspaceManager({ onClose, onChanged, initialIconRoot = "" }: { onClose: () => void; onChanged: () => Promise<void>; initialIconRoot?: string }) {
  const [rows, setRows] = useState<WorkspaceRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameDraft, setRenameDraft] = useState("");
  const [renamingBusy, setRenamingBusy] = useState(false);
  const [armed, setArmed] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [pinning, setPinning] = useState<string | null>(null);
	const [workspaceSlots, setWorkspaceSlots] = useState(WORKSPACE_PIN_LIMIT);
	const [slotsBusy, setSlotsBusy] = useState(false);
  const [iconEditing, setIconEditing] = useState<string | null>(() => initialIconRoot || null);
  const [iconBusy, setIconBusy] = useState(false);
  const pinnedRows = pinnedWorkspaceRows(rows);
  const pinsFull = workspacePinsFull(rows);
  const reload = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
		const [tree, slots] = await Promise.all([app.ListProjectTree(), app.GetDesktopWorkspaceSlots()]);
      setRows(projectWorkspaceRows(tree));
		setWorkspaceSlots(slots);
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setLoading(false); }
  }, []);
  useEffect(() => { void reload(); }, [reload]);
  useEffect(() => { if (initialIconRoot) setIconEditing(initialIconRoot); }, [initialIconRoot]);
  const add = async () => {
    if (adding) return;
    setAdding(true);
    setError("");
    try {
      const root = await app.PickWorkspace();
      // A cancelled picker returns "" with no side effects; a real pick reloads
      // the authoritative list and keeps the management dialog open.
      if (root) await reload();
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setAdding(false); }
  };
  const togglePin = async (row: WorkspaceRow) => {
    if (pinning) return;
    setPinning(row.root);
    setError("");
    try {
      // Pin toggles to an explicit target state for this exact workspace: the
      // backend write is idempotent on retry, and the snapshot refresh is
      // always triggered so the bottom desktop icons reconcile immediately.
      const targetPinned = !row.pinned;
      await app.SetProjectPinned(row.root, targetPinned);
      await reload();
      await onChanged();
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setPinning(null); }
  };
	const chooseWorkspaceSlots = async (slots: number) => {
		if (slotsBusy || slots === workspaceSlots) return;
		setSlotsBusy(true);
		setError("");
		try {
			await app.SetDesktopWorkspaceSlots(slots);
			await onChanged();
			setWorkspaceSlots(slots);
		} catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
		finally { setSlotsBusy(false); }
	};
  const startRename = (row: WorkspaceRow) => { setRenaming(row.root); setRenameDraft(row.label); setError(""); };
  const cancelRename = () => { setRenaming(null); setRenameDraft(""); setRenamingBusy(false); };
  const commitRename = async (row: WorkspaceRow) => {
    if (renamingBusy) return;
    setRenamingBusy(true);
    setError("");
    try {
      await app.RenameProject(row.root, renameTitle(renameDraft));
      cancelRename();
      await reload();
    } catch (cause) {
      // Keep the inline input open so the same edit can be retried safely.
      setError(cause instanceof Error ? cause.message : String(cause));
      setRenamingBusy(false);
    }
  };
  const chooseIcon = async (row: WorkspaceRow, icon: WorkspaceMatteIconKey) => {
    if (iconBusy) return;
    if (row.icon === icon) { setIconEditing(null); return; }
    setIconBusy(true);
    setError("");
    try {
      await app.SetProjectIcon(row.root, icon);
      await reload();
      await onChanged();
      setIconEditing(null);
    } catch (cause) {
      // Keep the palette open so a failed IPC or disk write can be retried
      // without losing the user's target workspace and icon choice.
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setIconBusy(false);
    }
  };
  const requestDelete = (row: WorkspaceRow) => {
    const next = deleteConfirmNext(armed, row.root);
    setArmed(next.armed);
    if (next.confirmed) void confirmDelete(row);
  };
  const confirmDelete = async (row: WorkspaceRow) => {
    if (deleting) return;
    setDeleting(row.root);
    setError("");
    try {
      await app.RemoveWorkspace(row.root);
      setArmed(null);
      await reload();
    } catch (cause) {
      // The row stays (no optimistic removal) and stays armed, so the same
      // 确认删除 is a safe retry entry.
      setError(cause instanceof Error ? cause.message : String(cause));
    }
    finally { setDeleting(null); }
  };
  const renameRow = (row: WorkspaceRow) => {
    if (renaming !== row.root) return null;
    return <input autoFocus value={renameDraft} aria-label={`重命名 ${row.label}`} disabled={renamingBusy} onChange={(event) => setRenameDraft(event.target.value)} onKeyDown={(event) => {
      if (event.key === "Enter" && !event.nativeEvent.isComposing) { event.preventDefault(); void commitRename(row); return; }
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); cancelRename(); }
    }} />;
  };
  return <div className="desktop-icon-popup__workspaces">
    <div className="desktop-icon-popup__workspace-head"><strong>工作区</strong><button type="button" disabled={adding} onClick={() => void add()}>{adding ? "添加中…" : "新增"}</button></div>
    {error && <p role="alert" className="desktop-icon-popup__error">{error}</p>}
    {loading && <p className="desktop-icon-popup__workspace-note">加载中…</p>}
    {!loading && rows.length === 0 && <p className="desktop-icon-popup__workspace-note">暂无工作区，点击“新增”添加</p>}
    {!loading && rows.length > 0 && <div className="desktop-icon-popup__workspace-list">{rows.map((row) => <div key={row.root} className={`desktop-icon-popup__workspace-row${armed === row.root ? " is-armed" : ""}`}>
      <div className="desktop-icon-popup__workspace-main">
        <span className="desktop-icon-popup__workspace-glyph"><WorkspaceGlyph icon={row.icon} /></span>
        {renaming === row.root ? renameRow(row) : <strong className="desktop-icon-popup__workspace-name" title={row.root}>{row.label}</strong>}
        <button type="button" className="desktop-icon-popup__workspace-pin" aria-pressed={row.pinned} aria-label={row.pinned ? `取消固定 ${row.label}` : `固定 ${row.label}`} title={row.pinned ? "取消固定" : pinsFull ? "4 个固定位置已满" : "固定到桌面"} disabled={pinning === row.root || renaming === row.root || (!row.pinned && pinsFull)} onClick={() => void togglePin(row)}>{pinnedIcon(row)}</button>
      </div>
      {renaming === row.root
        ? <div className="desktop-icon-popup__workspace-actions"><button disabled={renamingBusy} onClick={() => void commitRename(row)}>{renamingBusy ? "保存中…" : "确认"}</button><button type="button" className="subtle" disabled={renamingBusy} onClick={cancelRename}>取消</button><small className="desktop-icon-popup__workspace-hint">留空恢复目录名</small></div>
        : armed === row.root
          ? <div className="desktop-icon-popup__workspace-actions desktop-icon-popup__workspace-actions--confirm"><span className="desktop-icon-popup__workspace-warn">删除“{row.label}”？</span><button type="button" className="danger" disabled={deleting === row.root} onClick={() => void confirmDelete(row)}>{deleting === row.root ? "删除中…" : "确认删除"}</button><button type="button" className="subtle" disabled={deleting === row.root} onClick={() => setArmed(null)}>取消</button></div>
          : <><div className="desktop-icon-popup__workspace-actions"><button type="button" className="subtle" disabled={renamingBusy || iconBusy} onClick={() => startRename(row)}><Pencil aria-hidden="true" />重命名</button><button type="button" className={`subtle${iconEditing === row.root ? " is-active" : ""}`} disabled={iconBusy} aria-expanded={iconEditing === row.root} onClick={() => setIconEditing((current) => current === row.root ? null : row.root)}>修改图标</button><button type="button" className="subtle desktop-icon-popup__workspace-delete" disabled={iconBusy} onClick={() => requestDelete(row)}><Trash2 aria-hidden="true" />删除</button></div>
            {iconEditing === row.root && <div className="desktop-icon-popup__workspace-icons" role="group" aria-label={`为 ${row.label} 选择图标`} aria-busy={iconBusy}>{WORKSPACE_MATTE_ICON_OPTIONS.map((option) => <button key={option.key} type="button" className={row.icon === option.key ? "is-selected" : ""} aria-pressed={row.icon === option.key} aria-label={option.label} title={option.label} disabled={iconBusy} onClick={() => void chooseIcon(row, option.key)}><WorkspaceMatteIcon icon={option.key} /></button>)}</div>}</>}
    </div>)}</div>}
    <div className="desktop-icon-popup__workspace-pins">
		<div className="desktop-icon-popup__workspace-count"><span>桌面显示数量<small>固定优先，空位由当前与最近活跃工作区补齐</small></span><div role="group" aria-label="桌面工作区显示数量">{Array.from({ length: WORKSPACE_PIN_LIMIT + 1 }, (_, slots) => <button key={slots} type="button" aria-pressed={workspaceSlots === slots} disabled={slotsBusy} onClick={() => void chooseWorkspaceSlots(slots)}>{slots}</button>)}</div></div>
		<div className="desktop-icon-popup__workspace-pins-head"><span><Pin aria-hidden="true" />优先固定</span><small>{pinnedRows.length}/{WORKSPACE_PIN_LIMIT}</small></div>
      <div className="desktop-icon-popup__workspace-slots">{Array.from({ length: WORKSPACE_PIN_LIMIT }, (_, index) => {
        const row = pinnedRows[index];
        return <div key={row?.root ?? `empty-${index}`} className={`desktop-icon-popup__workspace-slot${row ? " is-filled" : ""}`} title={row?.root}>
          {row ? <><WorkspaceGlyph icon={row.icon} size={13} /><span>{row.label}</span></> : <span>空位</span>}
        </div>;
      })}</div>
      {pinsFull && <small className="desktop-icon-popup__workspace-pin-limit">固定位置已满，取消一个后可固定其他工作区</small>}
    </div>
    <div className="desktop-icon-popup__workspace-foot"><button type="button" className="subtle" onClick={onClose}>关闭</button><small>Escape 关闭</small></div>
  </div>;
}

// RoomsManager mirrors the WorkspaceManager interaction contract: it loads its
// authoritative rows from app.ListProjectTree(), reloads after every
// successful mutation, keeps failures visible and retryable, and never
// optimistically removes or renames a row. Opening a Room activates the
// backend tab first and then asks the root App to exit widget mode focused on
// that tab.
function RoomsManager({ roomIconCount, onRoomIconCountChange, notificationMode, onNotificationModeChange, onClose, onChanged, onNewRoom, onOpenRoom }: { roomIconCount: number; onRoomIconCountChange: (count: number) => void; notificationMode: RoomNotificationMode; onNotificationModeChange: (mode: RoomNotificationMode) => void; onClose: () => void; onChanged: () => Promise<void>; onNewRoom: () => void; onOpenRoom: (tabID: string) => Promise<void> }) {
  const [rows, setRows] = useState<RoomRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [opening, setOpening] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameDraft, setRenameDraft] = useState("");
  const [renamingBusy, setRenamingBusy] = useState(false);
  const [armed, setArmed] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [pinning, setPinning] = useState<string | null>(null);
	const [countBusy, setCountBusy] = useState(false);
	const [iconEditing, setIconEditing] = useState<string | null>(null);
	const [iconBusy, setIconBusy] = useState(false);
	const pinnedRows = pinnedRoomRows(rows);
	const pinsFull = roomPinsFull(rows);
  const reload = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      // Preference bindings were introduced after the Room tree binding. Old
      // binaries may omit them, and nil Go slices/maps arrive through Wails as
      // null. Keep the authoritative Room list usable while surfacing degraded
      // pin/icon preferences as a retryable warning.
      const [treeResult, pinsResult, iconsResult] = await Promise.allSettled([
        Promise.resolve().then(() => app.ListProjectTree()),
        Promise.resolve().then(() => {
          const load = app.GetDesktopRoomPins;
          if (typeof load !== "function") throw new Error("当前 Desktop 未提供 Room 固定设置接口");
          return load();
        }),
        Promise.resolve().then(() => {
          const load = app.GetDesktopRoomIcons;
          if (typeof load !== "function") throw new Error("当前 Desktop 未提供 Room 图标设置接口");
          return load();
        }),
      ]);
      if (treeResult.status === "rejected") throw treeResult.reason;
      const warnings: string[] = [];
      let pins: string[] = [];
      let icons: Record<string, string> = {};
      if (pinsResult.status === "fulfilled") {
        try { pins = normalizeRoomPins(pinsResult.value); }
        catch (cause) { warnings.push(cause instanceof Error ? cause.message : String(cause)); }
      } else {
        warnings.push(pinsResult.reason instanceof Error ? pinsResult.reason.message : String(pinsResult.reason));
      }
      if (iconsResult.status === "fulfilled") {
        try { icons = normalizeRoomIcons(iconsResult.value); }
        catch (cause) { warnings.push(cause instanceof Error ? cause.message : String(cause)); }
      } else {
        warnings.push(iconsResult.reason instanceof Error ? iconsResult.reason.message : String(iconsResult.reason));
      }
      setRows(applyRoomIcons(applyRoomPins(roomRows(treeResult.value), pins), icons));
      if (warnings.length > 0) setError(`Room 设置加载失败（已使用默认值）：${warnings.join("；")}`);
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setLoading(false); }
  }, []);
  useEffect(() => { void reload(); }, [reload]);
  const chooseRoomCount = (count: number) => {
    if (countBusy || count === roomIconCount) return;
    setCountBusy(true);
    setError("");
    try {
      // Persist before reflecting the new count so a failed write keeps the
      // confirmed count visible and the same number button is a safe retry.
      writeRoomIconCount(localStorage, count);
      onRoomIconCountChange(count);
    } catch (cause) {
      setError(`保存 Room 显示数量设置失败：${cause instanceof Error ? cause.message : String(cause)}`);
    } finally {
      setCountBusy(false);
    }
  };
	const chooseNotificationMode = (mode: RoomNotificationMode) => {
		if (mode === notificationMode) return;
		try {
			writeRoomNotificationMode(localStorage, mode);
			onNotificationModeChange(mode);
			setError("");
		} catch (cause) {
			setError(`保存 Room 消息提醒设置失败：${cause instanceof Error ? cause.message : String(cause)}`);
		}
	};
  const open = async (row: RoomRow) => {
    if (opening) return;
    setOpening(row.topicId);
    setError("");
    try {
      const meta = await app.OpenTopicSession(row.scope, row.workspaceRoot, row.topicId, row.sessionPath);
      // OpenTopicSession owns activation; the root App exits the widget and
      // focuses the returned tab. A failed exit stays visible in this popup
      // (the main window is still hidden) and is retryable.
      await onOpenRoom(meta.id);
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setOpening(null); }
  };
  const togglePin = async (row: RoomRow) => {
    if (pinning) return;
    setPinning(row.topicId);
    setError("");
    try {
      const targetPinned = !row.pinned;
      await app.SetDesktopRoomPinned(row.topicId, targetPinned);
      await reload();
		await onChanged();
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setPinning(null); }
  };
  const startRename = (row: RoomRow) => { setRenaming(row.topicId); setRenameDraft(row.label); setError(""); };
  const cancelRename = () => { setRenaming(null); setRenameDraft(""); setRenamingBusy(false); };
  const commitRename = async (row: RoomRow) => {
    if (renamingBusy) return;
    setRenamingBusy(true);
    setError("");
    try {
      await app.RenameTopic(row.topicId, renameTitle(renameDraft));
      cancelRename();
      await reload();
    } catch (cause) {
      // Keep the inline input open so the same edit can be retried safely.
      setError(cause instanceof Error ? cause.message : String(cause));
      setRenamingBusy(false);
    }
  };
	const chooseIcon = async (row: RoomRow, icon: ProjectIconKey) => {
		if (iconBusy) return;
		if (row.icon === icon) { setIconEditing(null); return; }
		setIconBusy(true);
		setError("");
		try {
			await app.SetDesktopRoomIcon(row.topicId, icon);
			await reload();
			await onChanged();
			setIconEditing(null);
		} catch (cause) {
			// Keep the palette open so persistence or IPC failures remain visible
			// and the same explicit target can be retried safely.
			setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setIconBusy(false);
		}
	};
  const requestTrash = (row: RoomRow) => {
    const next = deleteConfirmNext(armed, row.topicId);
    setArmed(next.armed);
    if (next.confirmed) void confirmTrash(row);
  };
  const confirmTrash = async (row: RoomRow) => {
    if (deleting) return;
    setDeleting(row.topicId);
    setError("");
    try {
      await app.TrashTopic(row.topicId);
      setArmed(null);
      await reload();
    } catch (cause) {
      // The row stays (no optimistic removal) and stays armed, so the same
      // 确认移入 is a safe retry entry.
      setError(cause instanceof Error ? cause.message : String(cause));
    }
    finally { setDeleting(null); }
  };
  const renameRow = (row: RoomRow) => {
    if (renaming !== row.topicId) return null;
    return <input autoFocus value={renameDraft} aria-label={`重命名 ${row.label}`} disabled={renamingBusy} onChange={(event) => setRenameDraft(event.target.value)} onKeyDown={(event) => {
      if (event.key === "Enter" && !event.nativeEvent.isComposing) { event.preventDefault(); void commitRename(row); return; }
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); cancelRename(); }
    }} />;
  };
  return <div className="desktop-icon-popup__workspaces desktop-icon-popup__rooms">
    <div className="desktop-icon-popup__workspace-head"><strong>Rooms</strong><button type="button" onClick={onNewRoom}>新增</button></div>
    <div className="desktop-icon-popup__room-settings">
      <div className="desktop-icon-popup__room-notification">
        <span>消息提醒<small>数字仅更新角标；弹出会展示后续新消息</small></span>
        <div role="radiogroup" aria-label="Room 消息提醒方式">
          <button type="button" role="radio" aria-checked={notificationMode === "count"} onClick={() => chooseNotificationMode("count")}>数字</button>
          <button type="button" role="radio" aria-checked={notificationMode === "popup"} onClick={() => chooseNotificationMode("popup")}>弹出</button>
        </div>
      </div>
    </div>
    {error && <p role="alert" className="desktop-icon-popup__error">{error}</p>}
    {loading && <p className="desktop-icon-popup__workspace-note">加载中…</p>}
    {!loading && rows.length === 0 && <p className="desktop-icon-popup__workspace-note">暂无 Room，点击“新增”创建</p>}
    {!loading && rows.length > 0 && <div className="desktop-icon-popup__workspace-list">{rows.map((row) => <div key={row.topicId} className={`desktop-icon-popup__workspace-row${armed === row.topicId ? " is-armed" : ""}`}>
      <div className="desktop-icon-popup__workspace-main">
        <span className="desktop-icon-popup__workspace-glyph"><RoomGlyph icon={row.icon} /></span>
        {renaming === row.topicId ? renameRow(row) : <strong className="desktop-icon-popup__workspace-name" title={row.sessionPath}>{row.label}</strong>}
        <button type="button" className="desktop-icon-popup__workspace-pin" aria-pressed={row.pinned} aria-label={row.pinned ? `取消固定 ${row.label}` : `固定 ${row.label}`} title={row.pinned ? "取消固定" : pinsFull ? "7 个固定位置已满" : "固定到桌面"} disabled={pinning === row.topicId || renaming === row.topicId || (!row.pinned && pinsFull)} onClick={() => void togglePin(row)}>{pinnedIcon(row)}</button>
      </div>
      {renaming === row.topicId
        ? <div className="desktop-icon-popup__workspace-actions"><button disabled={renamingBusy} onClick={() => void commitRename(row)}>{renamingBusy ? "保存中…" : "确认"}</button><button type="button" className="subtle" disabled={renamingBusy} onClick={cancelRename}>取消</button><small className="desktop-icon-popup__workspace-hint">留空恢复自动标题</small></div>
        : armed === row.topicId
          ? <div className="desktop-icon-popup__workspace-actions desktop-icon-popup__workspace-actions--confirm"><span className="desktop-icon-popup__workspace-warn">移入回收站“{row.label}”？</span><button type="button" className="danger" disabled={deleting === row.topicId} onClick={() => void confirmTrash(row)}>{deleting === row.topicId ? "移入中…" : "确认移入"}</button><button type="button" className="subtle" disabled={deleting === row.topicId} onClick={() => setArmed(null)}>取消</button></div>
          : <><div className="desktop-icon-popup__workspace-actions"><button type="button" className="desktop-icon-popup__room-open" disabled={opening === row.topicId || iconBusy} onClick={() => void open(row)}>{opening === row.topicId ? "打开中…" : "打开"}</button><button type="button" className="subtle" disabled={renamingBusy || iconBusy} onClick={() => startRename(row)}><Pencil aria-hidden="true" />重命名</button><button type="button" className={`subtle${iconEditing === row.topicId ? " is-active" : ""}`} disabled={iconBusy} aria-expanded={iconEditing === row.topicId} onClick={() => setIconEditing((current) => current === row.topicId ? null : row.topicId)}>修改图标</button><button type="button" className="subtle desktop-icon-popup__workspace-delete" disabled={iconBusy} onClick={() => requestTrash(row)}><Trash2 aria-hidden="true" />移入回收站</button></div>
            {iconEditing === row.topicId && <div className="desktop-icon-popup__workspace-icons" role="group" aria-label={`为 ${row.label} 选择图标`} aria-busy={iconBusy}><button type="button" className={row.icon === "" ? "is-selected" : ""} aria-pressed={row.icon === ""} aria-label="默认 Room 图标" title="默认 Room 图标" disabled={iconBusy} onClick={() => void chooseIcon(row, "")}><WorkspaceMatteIcon icon="social" /></button>{WORKSPACE_MATTE_ICON_OPTIONS.map((option) => <button key={option.key} type="button" className={row.icon === option.key ? "is-selected" : ""} aria-pressed={row.icon === option.key} aria-label={option.label} title={option.label} disabled={iconBusy} onClick={() => void chooseIcon(row, option.key)}><WorkspaceMatteIcon icon={option.key} /></button>)}</div>}</>}
    </div>)}</div>}
    <div className="desktop-icon-popup__workspace-pins desktop-icon-popup__room-pins">
      <div className="desktop-icon-popup__workspace-count desktop-icon-popup__room-count">
        <span>桌面显示数量<small>固定优先，空位由其余 Room 按顺序补齐</small></span>
        <div role="group" aria-label="桌面 Room 显示数量">{Array.from({ length: ROOM_PIN_LIMIT + 1 }, (_, count) => <button key={count} type="button" aria-pressed={roomIconCount === count} disabled={countBusy} onClick={() => chooseRoomCount(count)}>{count}</button>)}</div>
      </div>
      <div className="desktop-icon-popup__workspace-pins-head"><span><Pin aria-hidden="true" />优先固定</span><small>{pinnedRows.length}/{ROOM_PIN_LIMIT}</small></div>
      <div className="desktop-icon-popup__workspace-slots desktop-icon-popup__room-slots">{Array.from({ length: ROOM_PIN_LIMIT }, (_, index) => {
        const row = pinnedRows[index];
        return <div key={row?.topicId ?? `empty-${index}`} className={`desktop-icon-popup__workspace-slot${row ? " is-filled" : ""}`} title={row?.sessionPath}>
          {row ? <><RoomGlyph icon={row.icon} size={13} /><span>{row.label}</span></> : <span>空位</span>}
        </div>;
      })}</div>
      {pinsFull && <small className="desktop-icon-popup__workspace-pin-limit">固定位置已满，取消一个后可固定其他 Room</small>}
    </div>
    <div className="desktop-icon-popup__workspace-foot"><button type="button" className="subtle" onClick={onClose}>关闭</button><small>Escape 关闭</small></div>
  </div>;
}

function pinnedIcon(row: { pinned: boolean }) {
  return row.pinned ? <PinOff aria-hidden="true" /> : <Pin aria-hidden="true" />;
}

export function DesktopIconMode({ onNewRoom, onOpenRoom, onOpenSettings, onOpenMain, onOpenAssistant }: { onNewRoom: () => void; onOpenRoom: (tabID: string) => Promise<void>; onOpenSettings: () => Promise<void>; onOpenMain: () => Promise<void>; onOpenAssistant: () => Promise<void> }) {
	const t = useT();
  const [snapshot, setSnapshot] = useState<DesktopIconSnapshot>({ items: [], delegations: [], revision: "", hoverStatusDelayMs: 1200, style: "icons", unreadRevision: 0 });
  const [desktopZoom, setDesktopZoom] = useState(1);
	const [viewport, setViewport] = useState(() => widgetViewportSize(window.innerWidth, window.innerHeight, 1));
  const [activeID, setActiveID] = useState("");
	const [activeNoticeID, setActiveNoticeID] = useState("");
  const [previewID, setPreviewID] = useState("");
  const [menuID, setMenuID] = useState("");
  const [renamingID, setRenamingID] = useState("");
  const [renameDraft, setRenameDraft] = useState("");
  const [workspaceIconRoot, setWorkspaceIconRoot] = useState("");
  const [popupAnchorID, setPopupAnchorID] = useState("");
  const [draggingID, setDraggingID] = useState("");
  const [dragPreview, setDragPreview] = useState<{ itemId: string; order: number } | null>(null);
  const [anchorMenuOpen, setAnchorMenuOpen] = useState(false);
  const [quickOpen, setQuickOpen] = useState(false);
  const [clusterZoom, setClusterZoom] = useState(readClusterZoom);
  const [roomIconCount, setRoomIconCount] = useState(() => {
    try { return readRoomIconCount(localStorage); }
    catch { return ROOM_PIN_LIMIT; }
  });
	const [roomNotificationMode, setRoomNotificationMode] = useState<RoomNotificationMode>(() => {
		try { return readRoomNotificationMode(localStorage); }
		catch { return "count"; }
	});
	const [roomPopupState, setRoomPopupState] = useState(newRoomPopupState);
	const [blockPopupState, setBlockPopupState] = useState(newBlockPopupState);
	const [snapshotLoaded, setSnapshotLoaded] = useState(false);
  const [topmost, setTopmost] = useState(false);
  const [topmostLoaded, setTopmostLoaded] = useState(false);
  const [topmostBusy, setTopmostBusy] = useState(false);
  const [topmostReadFailed, setTopmostReadFailed] = useState(false);
  const [topmostAttempt, setTopmostAttempt] = useState(0);
  const [exiting, setExiting] = useState(false);
  const [busy, setBusy] = useState(false);
	const [extractRequestLoad] = useState(() => readDailyRoutineRequests(DAILY_ROUTINE_EXTRACT_REQUESTS_KEY));
  const [error, setError] = useState(() => extractRequestLoad.recovered ? t("dailyRoutine.requestStoreRecovered") : "");
  const [quickError, setQuickError] = useState("");
	const [routineNotice, setRoutineNotice] = useState("");
  const [workspaces, setWorkspaces] = useState<WidgetWorkspaceOption[]>([]);
	const [quickWorkspace, setQuickWorkspace] = useState("");
	const [quickStartEditJob, setQuickStartEditJob] = useState<QuickStartJob | null>(null);
	// QuickStart's async delivery is owned here, not by the modal: submit
	// closes the modal synchronously and the optimistic icon renders
	// immediately while the background runner delivers the job.
	const quickJobs = useWidgetQuickStartJobs(app.StartWidgetConversation);
	// submitQuickStart wraps the runner's submit with the durable consumed-draft
	// marker flow: once ledger persistence + dispatch succeeded, a consumed
	// marker keyed to the submitted draft/requestId is recorded BEFORE the
	// best-effort draft removal — if the removal fails, the marker makes the
	// NEXT mount ignore that exact stale draft and retries the cleanup; if the
	// removal succeeds the marker is cleared so an intentionally identical
	// future draft is never permanently suppressed. Marker read/write/remove
	// errors stay visible (quickError) and never re-dispatch.
	const submitQuickStart = (intent: QuickStartJobIntent, opts?: { replacesRequestId?: string }) => {
		const result = quickJobs.submit(intent, opts);
		if (result.ok) {
			const trimmed = intent.prompt.trim();
			let marked = false;
			try {
				recordConsumedDraftMarker(localStorage, trimmed, result.requestId);
				marked = true;
			} catch (cause) {
				setQuickError(`发送成功，但记录草稿标记失败：${cause instanceof Error ? cause.message : String(cause)}`);
			}
			try {
				localStorage.removeItem(QUICK_DRAFT_KEY);
				if (marked) {
					try { clearConsumedDraftMarker(localStorage); } catch (cause) {
						setQuickError(`发送成功并清理草稿，但清除草稿标记失败：${cause instanceof Error ? cause.message : String(cause)}`);
					}
				}
			} catch (cause) {
				setQuickError(marked
					? "发送成功，但清理本地草稿失败；该内容已标记为已发送，重新打开不会重复提交。"
					: `发送成功，但清理本地草稿失败：${cause instanceof Error ? cause.message : String(cause)}`);
			}
		}
		return result;
	};
	// openWindowCreate reuses the SAME backend workspace-open path as double-
	// clicking a Workspace icon: it resolves the selected workspace, creates a
	// normal blank Session, and exits the widget focusing that Session. It never
	// touches the optimistic quick-start ledger and never carries the draft,
	// model, or approval posture, so the new Session lands in Session List
	// directly. A failed create/exit rejects so the modal keeps the error
	// visible and the same button stays a safe idempotent retry.
	const openWindowCreate = async (workspace: string, requestId: string) => {
		await app.OpenWidgetWorkspace(workspace, requestId);
	};
	const optimisticItems = useMemo(
		() => Object.values(quickJobs.jobs).sort((a, b) => b.createdAt - a.createdAt).map(quickStartJobItem),
		[quickJobs.jobs],
	);
	// activeQuickStartDrafts maps normalized prompts to their real requestId.
	// The pure consumed-draft decision uses it both to suppress an already
	// submitted draft and to schedule committed marker+draft cleanup when the
	// submit path could persist neither write.
	const activeQuickStartDrafts = useMemo(
		() => new Map(Object.values(quickJobs.jobs).map((job) => [job.intent.prompt.trim(), job.requestId])),
		[quickJobs.jobs],
	);
	const mergedItems = useMemo(() => mergeQuickStartItems(snapshot.items, optimisticItems), [optimisticItems, snapshot.items]);
	const visibleItems = useMemo(() => visibleDesktopIcons(mergedItems, roomIconCount), [mergedItems, roomIconCount]);
	const displayItems = useMemo(
		() => dragPreview ? previewDesktopIconMove(visibleItems, dragPreview.itemId, dragPreview.order) : visibleItems,
		[dragPreview, visibleItems],
	);
	// Agent Icon ViewModel 按真实 task 条目 memoize：身份/任务/状态推导只算
	// 一次（每 session 一个），重复渲染、live→retained、重启后同 seed 输出
	// 完全一致。动画帧不在这里 —— 由 AgentIcon 内部共享时钟推导。
	const agentIconViewModels = useMemo(() => {
		const map = new Map<string, AgentIconViewModel>();
		for (const item of displayItems) {
			if (isAgentIconItem(item)) map.set(item.id, buildAgentIconViewModel(item));
		}
		return map;
	}, [displayItems]);
	const timers = useRef<IconTimers | null>(null);
	if (timers.current === null) timers.current = new IconTimers(windowTimerHost);
	// idleTrace runs the low-overhead "first hover after idle" performance
	// diagnostics: one hover_start write immediately on a qualifying hover,
	// then one bounded hover_recovery summary. Write failures are swallowed
	// inside the tracer, so diagnostics can never break widget interaction.
	const idleTrace = useRef<IdleHoverTracer | null>(null);
	if (idleTrace.current === null) idleTrace.current = new IdleHoverTracer({
		write: (record) => { void app.WriteDesktopIconDiagnostics(record).catch(() => {}); },
	});
  const drag = useRef<{ item: DesktopIconItem; x: number; y: number; moved: boolean; targetOrder: number } | null>(null);
	const actionRequests = useRef(new Map<string, string>());
	const routineExtractRequests = useRef(extractRequestLoad.requests);
  const itemRefs = useRef(new Map<string, HTMLButtonElement>());
	const popupRef = useRef<HTMLElement>(null);
	const [popupWidth, setPopupWidth] = useState(0);
	const regionKey = useRef("");
	const regionErrorKey = useRef("");
	const regionQueue = useRef<Promise<void>>(Promise.resolve());
	const [collapsed, setCollapsed] = useState(readCollapsedState);
	const exitRequest = useRef(false);
	const [surfaceGeneration, setSurfaceGeneration] = useState(0);
	// surface owns the native icon-canvas bounds: every caller funnels through
	// it, so growth is immediate and the surface never shrinks until mode exit.
	const surface = useDesktopIconSurface(
		(cause) => setError(cause instanceof Error ? cause.message : String(cause)),
		(result) => setSurfaceGeneration((current) => current === result.generation ? current : result.generation),
	);
	const [overlayReadyKey, setOverlayReadyKey] = useState("");

	// cancelTransientTimers cancels every scheduled click/hover/preview and the
	// in-flight drag: a delayed open or preview can never resurrect transient UI
	// that was just closed, and never fires while a drag is in progress.
	const cancelTransientTimers = useCallback(() => {
		timers.current?.cancel();
		drag.current = null;
	}, []);
	// closeTransient is the one entry point that closes every transient surface
	// (icon popup, preview, icon menu, quick toolbar, anchor menu). Escape,
	// document outside clicks, blur, collapse/expand, anchor interactions and
	// every icon action route through it, so the timer cancellation always
	// precedes the state clearing.
	const closeTransient = useCallback(() => {
		cancelTransientTimers();
		setActiveID(""); setActiveNoticeID(""); setPreviewID(""); setMenuID(""); setRenamingID(""); setRenameDraft(""); setWorkspaceIconRoot(""); setPopupAnchorID(""); setDraggingID(""); setDragPreview(null); setAnchorMenuOpen(false); setQuickOpen(false);
	}, [cancelTransientTimers]);

	// Share one in-flight snapshot request across every caller. Polling waits for
	// completion before starting its one-second delay, so a slow backend can
	// reduce the refresh rate but can never build an unbounded request queue.
	const refreshPending = useRef<Promise<void> | null>(null);
  const refresh = useCallback(() => {
		if (refreshPending.current) return refreshPending.current;
		const pending = (async () => {
			try {
				const next = await app.GetDesktopIconSnapshot();
				const nextItems = visibleDesktopIcons(mergeQuickStartItems(next.items, optimisticItems), roomIconCount);
				const prepared = await surface.prepare(desktopIconLayoutBounds(nextItems, collapsed, clusterZoom * desktopZoom));
				if (!prepared) return;
				setSnapshot((current) => current.revision === next.revision && (current.error || "") === (next.error || "") ? current : next);
				setSnapshotLoaded(true);
				setError(next.error || "");
			// Accepted jobs hand off to their real task:task:<tabId> icon the
			// moment this refreshed snapshot contains it (same render, no
			// empty-frame gap, no duplicate). Polls never touch other phases,
			// and no timer ever evicts an accepted job whose real icon is
			// filtered/capped out of the snapshot.
				quickJobs.reconcile(next.items);
			} catch (cause) {
				setError(cause instanceof Error ? cause.message : String(cause));
			}
		})();
		refreshPending.current = pending;
		void pending.finally(() => { if (refreshPending.current === pending) refreshPending.current = null; });
		return pending;
	}, [collapsed, clusterZoom, desktopZoom, optimisticItems, quickJobs.reconcile, roomIconCount, surface]);
  useEffect(() => {
		let stopped = false;
		let timer = 0;
		const poll = async () => {
			await refresh();
			if (!stopped) timer = window.setTimeout(() => void poll(), 1000);
		};
		void poll();
		void app.ListWidgetWorkspaces().then(setWorkspaces).catch(() => {});
		return () => { stopped = true; window.clearTimeout(timer); };
	}, [refresh]);
	useEffect(() => {
		if (!snapshotLoaded) return;
		setRoomPopupState((current) => reconcileRoomPopups(current, snapshot.items, roomNotificationMode));
	}, [roomNotificationMode, snapshot.items, snapshotLoaded]);
	// Blocked sessions (needs_input / needs_confirm) auto-open the popup once
	// per notice revision when nothing else occupies the interface. Declared
	// before the Room popup effect so a pending block wins over a queued Room
	// message; "稍后处理" (or any close) consumes the queue entry and the
	// watermark prevents the same notice/revision from re-popping. The target
	// is peeked before consuming so a capped-out (undisplayed) icon keeps its
	// queued reminder instead of silently losing it.
	useEffect(() => {
		if (!snapshotLoaded) return;
		setBlockPopupState((current) => reconcileBlockPopups(current, snapshot.items));
	}, [snapshot.items, snapshotLoaded]);
	useEffect(() => {
		if (blockPopupState.queue.length === 0) return;
		if (activeID || previewID || menuID || renamingID || anchorMenuOpen || quickOpen || draggingID || busy || exiting) return;
		const candidate = blockPopupState.queue[0];
		if (!candidate || !displayItems.some((item) => item.id === candidate.itemId)) return;
		const consumed = consumeBlockPopup(blockPopupState);
		if (!consumed.candidate) return;
		setBlockPopupState(consumed.state);
		cancelTransientTimers();
		setPopupAnchorID("");
		setActiveNoticeID(consumed.candidate.noticeId);
		setActiveID(consumed.candidate.itemId);
	}, [activeID, anchorMenuOpen, blockPopupState, busy, cancelTransientTimers, displayItems, draggingID, exiting, menuID, previewID, quickOpen, renamingID, snapshotLoaded]);
	useEffect(() => {
		// A pending blocked session outranks queued Room popups: with both
		// queues non-empty the block effect above opens first (this guard sees
		// the old non-empty queue in the same commit and defers), and once the
		// block is closed its queue is empty, so Room popups resume.
		if (roomNotificationMode !== "popup" || roomPopupState.queue.length === 0 || blockPopupState.queue.length > 0) return;
		if (activeID || previewID || menuID || renamingID || anchorMenuOpen || quickOpen || draggingID || busy || exiting) return;
		const consumed = consumeRoomPopup(roomPopupState);
		if (!consumed.candidate) return;
		setRoomPopupState(consumed.state);
		if (!displayItems.some((item) => item.id === consumed.candidate?.itemId)) return;
		cancelTransientTimers();
		setPopupAnchorID("");
		setActiveNoticeID(consumed.candidate.noticeId);
		setActiveID(consumed.candidate.itemId);
	}, [activeID, anchorMenuOpen, blockPopupState, busy, cancelTransientTimers, displayItems, draggingID, exiting, menuID, previewID, quickOpen, renamingID, roomNotificationMode, roomPopupState]);
	useEffect(() => {
		let alive = true;
		void app.GetDesktopZoomFactor()
			.then((zoom) => { if (alive) setDesktopZoom(resolveWidgetZoomFrame(zoom).zoom); })
			.catch(() => { if (alive) setDesktopZoom(1); });
		return () => { alive = false; };
	}, []);
	// Keep one logical viewport state for popup placement. Resize updates width
	// and height together; a zoom change immediately recomputes both in the same
	// coordinate system. The equality guard avoids unrelated rerenders.
	useEffect(() => {
		const onResize = () => {
			const next = widgetViewportSize(window.innerWidth, window.innerHeight, desktopZoom);
			setViewport((current) => current.width === next.width && current.height === next.height ? current : next);
		};
		onResize();
		window.addEventListener("resize", onResize);
		return () => window.removeEventListener("resize", onResize);
	}, [desktopZoom]);
	const clusterMaxWidth = useMemo(
		() => clusterGridMaxWidth(viewport.width / desktopZoom, desktopZoom, clusterZoom),
		[viewport.width, desktopZoom, clusterZoom],
	);
	// The always-on-top switch mirrors the persisted config (single source of
	// truth). The initial value comes through the existing lightweight startup
	// contract, and the UI only ever reflects the last confirmed config state.
	// A failed initial read stays visible, keeps the switch disabled, and never
	// assumes false: reopening the quick toolbar bumps topmostAttempt, which
	// retries the read so the switch can recover without a restart.
	useEffect(() => {
		let alive = true;
		setTopmostBusy(true);
		void app.DesktopStartupSettings()
			.then((settings) => { if (alive) { setTopmost(Boolean(settings.widgetAlwaysOnTop)); setTopmostReadFailed(false); setQuickError((current) => current === TOPMOST_READ_ERROR ? "" : current); } })
			.catch(() => { if (alive) { setTopmostReadFailed(true); setQuickError(TOPMOST_READ_ERROR); } })
			.finally(() => { if (alive) { setTopmostLoaded(true); setTopmostBusy(false); } });
		return () => { alive = false; };
	}, [topmostAttempt]);
	useLayoutEffect(() => {
		let frame = 0;
		let alive = true;
		const report = (rects: ReturnType<typeof iconHitRect>[]) => {
			const key = `${surfaceGeneration}|${rects.map((rect) => `${rect.x},${rect.y},${rect.width},${rect.height}`).join(";")}`;
			if (!key || key === regionKey.current) return;
			regionKey.current = key;
			const next = regionQueue.current.catch(() => {}).then(() => app.SetDesktopIconHitRegions({ rects, generation: surfaceGeneration }));
			regionQueue.current = next.then(() => { regionErrorKey.current = ""; }, (cause) => {
				if (regionKey.current === key) regionKey.current = "";
				const message = cause instanceof Error ? cause.message : String(cause);
				if (alive && regionErrorKey.current !== message) {
					regionErrorKey.current = message;
					setError(message);
				}
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
	}, [activeID, menuID, previewID, snapshot.revision, collapsed, anchorMenuOpen, quickOpen, clusterZoom, optimisticItems, roomIconCount, surfaceGeneration, overlayReadyKey]);

	// Surface size comes from the next layout intent, never from animated DOM.
	// A transient key is only render-ready after native expansion succeeds.
	const layoutBounds = useMemo(() => desktopIconLayoutBounds(displayItems, collapsed, clusterZoom * desktopZoom), [collapsed, clusterZoom, desktopZoom, displayItems]);
	const overlayKey = activeID ? `active:${activeID}` : menuID ? `menu:${menuID}` : previewID ? `preview:${previewID}` : anchorMenuOpen ? "anchor-menu" : quickOpen ? "quick" : "";
	const overlayReady = Boolean(overlayKey && overlayReadyKey === overlayKey);
	useEffect(() => {
		surface.settle(layoutBounds);
	}, [layoutBounds, surface]);
	useEffect(() => {
		let alive = true;
		if (!overlayKey) {
			setOverlayReadyKey("");
			surface.settle(layoutBounds);
			return () => { alive = false; };
		}
		void surface.prepare(DESKTOP_ICON_OVERLAY_BOUNDS).then((ready) => {
			if (alive && ready) setOverlayReadyKey(overlayKey);
		});
		return () => { alive = false; };
	}, [layoutBounds, overlayKey, surface]);

  const run = useCallback(async (item: DesktopIconItem, action: string, values: string[] = [], notice = item.notifications[0], position?: DesktopIconPosition, answers?: QuestionAnswer[]) => {
    setBusy(true); setError(""); cancelTransientTimers(); setAnchorMenuOpen(false); setQuickOpen(false);
		const intent = JSON.stringify([item.id, notice?.id || "", action === "open_delegation" ? "" : item.revision, action, values, position || null, answers || null]);
		const stableID = actionRequests.current.get(intent) || requestID(`icon-${action}`);
		actionRequests.current.set(intent, stableID);
		const input: DesktopIconActionInput = { itemId: item.id, noticeId: notice?.id, revision: item.revision, requestId: stableID, action, values, answers, position, conversation: notice?.conversation, readSequence: notice?.readSequence };
    try {
      const result = await app.ApplyDesktopIconAction(input);
      setSnapshot(result.snapshot);
		if (result.status === "accepted" || result.status === "already_applied") { actionRequests.current.delete(intent); if (["dismiss", "later", "open", "reply", "continue", "remove"].includes(action) || action === "open_delegation") { setActiveID(""); setActiveNoticeID(""); } }
		else { if (result.status === "stale" || result.status === "invalid") actionRequests.current.delete(intent); setError(result.error || "操作失败，可安全重试"); }
		return result.status;
    } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); return "retryable_error"; }
    finally { setBusy(false); }
  }, []);

  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      // Escape inside the resident continuation input stays local (the
      // textarea stops propagation as well): it must never close the popup.
      if (event.key === "Escape" && !(event.target instanceof HTMLElement && event.target.closest(".desktop-icon-popup__continue"))) { closeTransient(); }
		if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "n") { event.preventDefault(); setQuickWorkspace(""); setQuickStartEditJob(null); setPopupAnchorID(""); setActiveID("fixed:new"); }
    };
    window.addEventListener("keydown", key); return () => window.removeEventListener("keydown", key);
  }, [closeTransient]);
	useEffect(() => {
		const close = () => closeTransient();
		window.addEventListener("blur", close);
		return () => { close(); window.removeEventListener("blur", close); };
	}, [closeTransient]);
	// Pointer-inactivity tracking for the idle-hover trace: window listeners
	// only stamp a monotonic clock (no timers, no per-event work), and the idle
	// check happens lazily at the next meaningful hover.
	useEffect(() => {
		const trace = idleTrace.current;
		if (!trace) return;
		const onActivity = () => trace.pointerActivity();
		const events = ["pointerover", "pointermove", "pointerdown", "pointerup", "wheel", "keydown"] as const;
		events.forEach((name) => window.addEventListener(name, onActivity, true));
		return () => events.forEach((name) => window.removeEventListener(name, onActivity, true));
	}, []);
	// Closing the widget aborts any in-flight recovery with an explicit summary.
	useEffect(() => () => { idleTrace.current?.dispose(); }, []);
	// Outside-click detection covers the whole widget window, including the
	// container/grid/control gaps that the old main-only handler missed.
	// Protected surfaces (quick toolbar, anchor, menus, icons, popups, toast)
	// own their own pointer handling and are excluded here.
	useEffect(() => {
		const onPointerDown = (event: PointerEvent) => {
			const target = event.target;
			if (!(target instanceof Element)) return;
			if (target.closest(TRANSIENT_PROTECTED_SELECTOR)) return;
			closeTransient();
		};
		document.addEventListener("pointerdown", onPointerDown);
		return () => document.removeEventListener("pointerdown", onPointerDown);
	}, [closeTransient]);

  const openItem = (item: DesktopIconItem) => {
    cancelTransientTimers();
    setPopupAnchorID("");
		setActiveNoticeID("");
    if (item.kind === "fixed" && item.sourceId === "new") { setQuickWorkspace(""); setQuickStartEditJob(null); setActiveID(item.id); }
		else if (item.kind === "fixed" && item.sourceId === "search") {
			setActiveID(item.id);
		}
		else if (item.kind === "fixed" && item.sourceId === "workspace") {
			// Single click opens the management dialog; it never runs the
			// generic fixed action (which would exit widget mode).
			setWorkspaceIconRoot(""); setActiveID(item.id);
		}
		else if (item.kind === "fixed" && item.sourceId === "rooms") {
			// Single click opens the Rooms management dialog; it never runs
			// the generic fixed action (which would exit widget mode).
			setActiveID(item.id);
		}
		else if (item.kind === "fixed" && item.sourceId === "dsh") {
			setActiveID(item.id);
		}
		else if (item.kind === "fixed" && item.sourceId === "assistant") {
			// Single click exits the widget and opens the Assistant home through
			// the root App; it never opens a generic popup and never runs the
			// generic fixed action.
			void openAssistant();
		}
    else setActiveID((current) => current === item.id ? "" : item.id);
    setPreviewID(""); setMenuID(""); setAnchorMenuOpen(false); setQuickOpen(false);
  };
  const enter = (item: DesktopIconItem) => {
		idleTrace.current?.hoverEnter("icon", { iconCount: visibleItems.length, revision: snapshot.revision });
    timers.current?.clearHover();
		timers.current?.clearPreviewClose();
    if (!snapshot.hoverStatusDelayMs || activeID || menuID || drag.current || anchorMenuOpen || quickOpen) return;
    timers.current?.scheduleHover(() => setPreviewID(item.id), snapshot.hoverStatusDelayMs);
  };
	const closePreviewSoon = () => { timers.current?.schedulePreviewClose(() => setPreviewID("")); };
  const pointerDown = (event: ReactPointerEvent, item: DesktopIconItem) => {
    if (event.button !== 0) return;
    // Drag start must cancel any pending click/hover/preview so a delayed open
    // or preview cannot resurrect while the pointer is down or mid-drag.
    timers.current?.cancel();
    // Screen coordinates stay stable when the first focused preview expands
    // the bottom-right-anchored native window underneath this pointer stream.
    drag.current = { item, x: event.screenX, y: event.screenY, moved: false, targetOrder: item.position.order };
    setAnchorMenuOpen(false);
    setQuickOpen(false);
    event.currentTarget.setPointerCapture(event.pointerId);
  };
  const pointerMove = (event: ReactPointerEvent) => {
    const current = drag.current;
    if (!current) return;
    if (Math.hypot(event.screenX - current.x, event.screenY - current.y) > DRAG_THRESHOLD) {
		timers.current?.cancel(); current.moved = true; setPreviewID(""); setDraggingID(current.item.id);
		const count = displayItems.filter((candidate) => candidate.position.row === current.item.position.row && candidate.position.zone === current.item.position.zone).length;
		const order = desktopIconDragOrder(current.item.position.order, current.x, event.screenX, count);
		if (order !== current.targetOrder) { current.targetOrder = order; setDragPreview({ itemId: current.item.id, order }); }
	}
  };
  const pointerUp = (event: ReactPointerEvent, item: DesktopIconItem) => {
	void event;
    const current = drag.current; drag.current = null;
    if (!current) return;
    if (current.moved) {
      // Optimistic jobs have no backend identity yet: dragging them must not
      // dispatch a move against the backend.
      if (isQuickStartJobItem(current.item)) { setDraggingID(""); setDragPreview(null); return; }
		const target = { ...current.item.position, order: current.targetOrder };
		void run(current.item, "move", [], undefined, target).finally(() => { setDraggingID(""); setDragPreview(null); });
      return;
    }
    timers.current?.scheduleClick(() => openItem(item));
  };
	const pointerCancel = () => { drag.current = null; setDraggingID(""); setDragPreview(null); };
	// openQuickStartTask opens the real backend task behind an accepted job:
	// ExitWidgetMode(tabId) is exactly what the existing run(item, "open")
	// action performs for a real task icon, and it stays safe even when the
	// real task:<tabId> icon is filtered/capped out of the snapshot. The gate
	// passes the exact tabId at most once per invocation (double-click guard)
	// and releases on failure, so a failed exit stays visible/retryable.
	const quickTaskGate = useRef(createQuickStartOpenTaskGate());
	const openQuickStartTask = async (job: QuickStartJob) => {
		if (exitRequest.current || exiting) return;
		exitRequest.current = true;
		setExiting(true);
		try {
			await quickTaskGate.current.open(job, (tabId) => app.ExitWidgetMode(tabId));
		} catch (cause) {
			setQuickError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			exitRequest.current = false;
			setExiting(false);
		}
	};
	const openQuickStartJob = (item: DesktopIconItem) => {
		const requestId = quickStartJobRequestIDFromItem(item.id);
		const job = requestId ? quickJobs.jobs[requestId] : undefined;
		if (job?.phase === "accepted" && job.tabId) { void openQuickStartTask(job); return; }
		openItem(item);
	};
	const doubleClick = (item: DesktopIconItem) => { timers.current?.cancel(); setAnchorMenuOpen(false); setQuickOpen(false); if (item.kind === "fixed" || item.kind === "external") openItem(item); else if (isQuickStartJobItem(item)) openQuickStartJob(item); else void run(item, "open"); };

  const rows = { top: displayItems.filter((item) => item.position.row === "top"), bottom: displayItems.filter((item) => item.position.row === "bottom") };
  const active = overlayReady && activeID ? displayItems.find((item) => item.id === activeID) : undefined;
  const preview = overlayReady && previewID ? displayItems.find((item) => item.id === previewID) : undefined;
  const popupItem = active || preview;
	const activeNotice = active?.notifications.find((notice) => notice.id === activeNoticeID) ?? active?.notifications[0];
	const popupAttention = active?.kind === "room" && activeNotice?.kind === "message" ? activeNotice.attention : undefined;
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
		const node = itemRefs.current.get(popupAnchorID) || itemRefs.current.get(popupItem.id); if (!node) return {};
		const rect = scaleIconRect(node.getBoundingClientRect(), desktopZoom);
		const fallbackWidth = active ? 330 : Math.min(300, viewport.width - 20);
		const width = popupWidth || fallbackWidth;
		const placed = placeIconPopup(rect, viewport.width, viewport.height, width);
		return { left: `${placed.left}px`, bottom: `${placed.bottom}px`, "--arrow-left": `${placed.arrowLeft}px`, "--popup-max-height": `${placed.maxHeight}px` } as CSSProperties;
	}, [active, desktopZoom, popupAnchorID, popupItem, popupWidth, snapshot.revision, viewport.height, viewport.width]);
	const finishMenuAction = async (item: DesktopIconItem, action: string, values: string[] = []) => {
		const status = await run(item, action, values);
		if (status === "accepted" || status === "already_applied") {
			setMenuID(""); setRenamingID(""); setRenameDraft("");
		}
	};
	const openWorkspaceIconEditor = (item: DesktopIconItem) => {
		setWorkspaceIconRoot(item.sourceId); setMenuID(""); setActiveID("fixed:workspace");
	};
	const startSessionRename = (item: DesktopIconItem) => {
		setRenamingID(item.id); setRenameDraft(item.title); setError("");
	};
	const commitSessionRename = (item: DesktopIconItem) => {
		const title = renameDraft.trim();
		if (!title || busy) return;
		void finishMenuAction(item, "rename", [title]);
	};
	const createRoutine = async (item: DesktopIconItem) => {
		if (busy) return;
		const requestKey = item.sessionRef?.sessionPath || item.sourceId;
		const stableRequest = routineExtractRequests.current.get(requestKey) || requestID(`daily-extract:${item.id}`);
		if (!routineExtractRequests.current.has(requestKey)) {
			routineExtractRequests.current.set(requestKey, stableRequest);
			if (!writeDailyRoutineRequests(DAILY_ROUTINE_EXTRACT_REQUESTS_KEY, routineExtractRequests.current)) {
				routineExtractRequests.current.delete(requestKey);
				setError(t("dailyRoutine.requestStoreFailed"));
				return;
			}
		}
		setBusy(true); setError(""); setRoutineNotice("");
		try {
			const result = await app.CreateDailyRoutine({ tabId: item.sourceId, sessionRef: item.sessionRef, requestId: stableRequest });
			if (result.status !== "accepted" && result.status !== "already_applied") throw new Error(result.error || t("dailyRoutine.extractFailed"));
			routineExtractRequests.current.delete(requestKey);
			if (!writeDailyRoutineRequests(DAILY_ROUTINE_EXTRACT_REQUESTS_KEY, routineExtractRequests.current)) setError(t("dailyRoutine.requestStoreCleanupFailed"));
			setRoutineNotice(t("dailyRoutine.saved", { name: result.routine?.name || t("dailyRoutine.unnamed") }));
			setMenuID("");
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setBusy(false);
		}
	};

  const renderItem = (item: DesktopIconItem) => {
		const agentVM = agentIconViewModels.get(item.id);
		const agentIcon = Boolean(agentVM);
		const blockLabel = item.status === "needs_input" ? "待回答" : item.status === "needs_confirm" ? "待确认" : "";
		return <div key={item.id} className={`desktop-icon-wrap desktop-icon-wrap--${item.position.zone}${draggingID === item.id ? " is-dragging" : ""}`}>
		<RuntimeIndicator item={item} />
		<button ref={(node) => { if (node) itemRefs.current.set(item.id, node); else itemRefs.current.delete(item.id); }} type="button" className={`desktop-icon desktop-icon--${item.status}${item.unreadCount > 0 ? " desktop-icon--pending" : ""}`} aria-label={`${item.title}，${previewText(item)}`} title={isQuickStartJobItem(item) ? `${item.title}，${quickStartJobStateLabel(item)}` : undefined} aria-expanded={activeID === item.id} onPointerDown={(event) => pointerDown(event, item)} onPointerMove={pointerMove} onPointerUp={(event) => pointerUp(event, item)} onPointerCancel={pointerCancel} onDoubleClick={() => doubleClick(item)} onContextMenu={(event) => { event.preventDefault(); cancelTransientTimers(); setAnchorMenuOpen(false); setQuickOpen(false); setPreviewID(""); setRenamingID(""); setRenameDraft(""); if (isQuickStartJobItem(item)) { setActiveID(item.id); setMenuID(""); } else { setMenuID(item.id); setActiveID(""); } }} onMouseEnter={() => enter(item)} onMouseLeave={() => { timers.current?.clearHover(); if (previewID === item.id) closePreviewSoon(); }} onFocus={() => { timers.current?.clearPreviewClose(); if (!activeID && !anchorMenuOpen && !quickOpen) setPreviewID(item.id); }} onBlur={() => { if (!activeID) closePreviewSoon(); }}>
      <span className={`desktop-icon__art${agentIcon ? " desktop-icon__art--agent" : ""}`}>{itemGlyph(item, agentVM)}{!agentIcon && (item.status === "running" || item.status === "thinking") && <span className={`desktop-icon__motion desktop-icon__motion--${item.status}`} aria-hidden="true">{item.status === "running" && <><i className="desktop-icon__motion-corner" /><i className="desktop-icon__motion-corner" /><i className="desktop-icon__motion-corner" /><i className="desktop-icon__motion-corner" /></>}</span>}{isQuickStartJobItem(item) && item.status === "idle" && <span className="desktop-icon__queued" aria-hidden="true" />}</span>
      <span className="desktop-icon__label">{item.title}</span>
		{blockLabel && <span className={`desktop-icon__block-state desktop-icon__block-state--${item.status}`}>{blockLabel}</span>}
      {item.unreadCount > 0 && <span className="desktop-icon__unread" aria-label={`${item.unreadCount} 条未读`}>{item.unreadCount > 99 ? "99+" : item.unreadCount}</span>}
      {item.activityCount ? <span className="desktop-icon__activity" aria-label={`${item.activityCount} 个活动任务`}>{item.activityCount}</span> : null}
      {!agentIcon && statusGlyph(item) && <span className="desktop-icon__status">{statusGlyph(item)}</span>}
    </button>
		{overlayReady && menuID === item.id && <div className="desktop-icon-menu" role="menu">
			{renamingID === item.id ? <div className="desktop-icon-menu__rename">
				<input autoFocus value={renameDraft} disabled={busy} aria-label={`改名 ${item.title}`} onChange={(event) => setRenameDraft(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.nativeEvent.isComposing) { event.preventDefault(); commitSessionRename(item); } else if (event.key === "Escape") { event.preventDefault(); setRenamingID(""); setRenameDraft(""); } }} />
				<div><button type="button" disabled={busy || !renameDraft.trim()} onClick={() => commitSessionRename(item)}>{busy ? "保存中…" : "保存"}</button><button type="button" disabled={busy} onClick={() => { setRenamingID(""); setRenameDraft(""); }}>取消</button></div>
			</div> : <>
				{item.kind === "external" ? <><button role="menuitem" onClick={() => openItem(item)}>查看状态</button>{item.actions?.includes("cancel") && <button role="menuitem" disabled={busy} onClick={() => void run(item, "cancel")}>取消任务</button>}{item.actions?.includes("remove") && <button role="menuitem" disabled={busy} onClick={() => void run(item, "remove")}>移除</button>}</> : <>
					<button role="menuitem" onClick={() => item.kind === "fixed" ? openItem(item) : void run(item, "open")}>打开</button>
					{item.kind === "workspace" && <button role="menuitem" onClick={() => openWorkspaceIconEditor(item)}>修改图标</button>}
					{item.kind === "task" && <><button role="menuitem" disabled={!canRenameTaskIcon(item)} title={canRenameTaskIcon(item) ? undefined : "该任务没有可改名的 Session"} onClick={() => startSessionRename(item)}>改名</button><button role="menuitem" disabled={busy} onClick={() => void createRoutine(item)}>{busy && routineExtractRequests.current.has(item.sessionRef?.sessionPath || item.sourceId) ? t("dailyRoutine.extracting") : t("dailyRoutine.make")}</button><button role="menuitem" disabled={busy} onClick={() => void finishMenuAction(item, "randomize_icon")}>换个样子</button></>}
				</>}
				{item.unreadCount > 0 && <button role="menuitem" onClick={() => void run(item, "mark_read")}>标记已读</button>}
				{(item.retained || item.kind === "person") && <button role="menuitem" onClick={() => void run(item, "remove")}>移除</button>}
			</>}
		</div>}
  </div>;
	};

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
		closeTransient();
		const next = !collapsed;
		setCollapsed(next);
		writeCollapsedState(next);
	};

	const applyClusterZoom = (next: number) => {
		if (exiting) return;
		const zoom = normalizeIconZoom(next);
		setClusterZoom(zoom);
		writeClusterZoom(zoom);
	};

	// Left-clicking the anchor toggles the inline quick toolbar. Wails only
	// starts the native window drag on mousemove, so a stationary primary click
	// still reaches the button; clearing every click/hover/preview timer here
	// stops a delayed icon click or hover from reopening transient UI over the
	// toolbar, and the toolbar is mutually exclusive with every other menu.
	const toggleQuick = () => {
		closeTransient();
		const next = !quickOpen;
		setQuickOpen(next);
		// Reopening the toolbar retries the always-on-top read that failed on
		// mount, so the switch can recover without a restart.
		if (next && topmostReadFailed) setTopmostAttempt((attempt) => attempt + 1);
	};

	// Always-on-top is config-owned: the switch never flips optimistically, it
	// only reflects the last confirmed value and disables while a toggle is in
	// flight or the initial read failed, so a double click cannot double-submit
	// and an unconfirmed value is never acted on.
	const toggleTopmost = async () => {
		if (exiting || topmostBusy || !topmostLoaded || topmostReadFailed) return;
		const next = !topmost;
		setTopmostBusy(true);
		setQuickError("");
		try {
			await app.SetDesktopWidgetAlwaysOnTop(next);
			setTopmost(next);
		} catch (cause) {
			setQuickError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setTopmostBusy(false);
		}
	};

	// Open main / open settings both exit widget mode through the root App.
	// A failed exit stays in the widget (the main window is still hidden) and
	// the error stays visible, so the same click is a safe retry. The shared
	// guard keeps the async round-trips from double-submitting, and both
	// entries are disabled (aria-busy) while the exit is in flight.
	const openMainWindow = async () => {
		if (exitRequest.current) return;
		exitRequest.current = true;
		setExiting(true);
		try {
			await onOpenMain();
		} catch (cause) {
			setQuickError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			exitRequest.current = false;
			setExiting(false);
		}
	};
	const openSettingsWindow = async () => {
		if (exitRequest.current) return;
		exitRequest.current = true;
		setExiting(true);
		try {
			await onOpenSettings();
		} catch (cause) {
			// A failed exit stays in the widget; the toolbar stays open (like
			// open main) so the same 设置 click is a safe retry.
			setQuickError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			exitRequest.current = false;
			setExiting(false);
		}
	};
	// Opening Assistant from its fixed entry exits widget mode through the root
	// App first, then opens the Assistant home. A failed exit stays in the
	// widget with the error visible, so the same 助手 click is a safe retry; the
	// shared guard keeps a fast double-click from double-exiting/double-opening.
	const openAssistant = async () => {
		if (exitRequest.current) return;
		exitRequest.current = true;
		setExiting(true);
		try {
			await onOpenAssistant();
		} catch (cause) {
			setQuickError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			exitRequest.current = false;
			setExiting(false);
		}
	};

	// Arrow-key roving for the quick toolbar: Left/Right move focus, Home/End
	// jump to the first/last control, and disabled buttons (e.g. zoom out at
	// the minimum) are skipped.
	const quickRove = (event: ReactKeyboardEvent<HTMLDivElement>) => {
		const buttons = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>(".desktop-icon-quick__btn"));
		if (buttons.length < 2) return;
		const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
		let next = -1;
		if (event.key === "ArrowRight" || event.key === "ArrowLeft") {
			const step = event.key === "ArrowRight" ? 1 : -1;
			for (let offset = 1; offset <= buttons.length; offset++) {
				const candidate = (index + step * offset + buttons.length) % buttons.length;
				if (!buttons[candidate].disabled) { next = candidate; break; }
			}
		} else if (event.key === "Home") {
			next = buttons.findIndex((button) => !button.disabled);
		} else if (event.key === "End") {
			for (let i = buttons.length - 1; i >= 0; i--) {
				if (!buttons[i].disabled) { next = i; break; }
			}
		} else {
			return;
		}
		if (next < 0) return;
		event.preventDefault();
		buttons[next].focus();
	};

	// editQuickStartJob reopens QuickStart prefilled with the failed job's
	// frozen intent. The intent travels in state/props, never through
	// localStorage, so a storage failure can never lose the edit target.
	// Submitting the edited draft creates a NEW requestId (the failed entry is
	// replaced on submit); the frozen intent itself is never mutated by later
	// modal changes.
	const editQuickStartJob = (job: QuickStartJob) => {
		setQuickStartEditJob(job);
		setQuickWorkspace(job.intent.workspace || "");
		closeTransient();
		setActiveID("fixed:new");
	};

	const quickDraftDecision: QuickStartConsumedDraftDecision = (() => {
		if (!(active && active.sourceId === "new")) return { draft: "", cleanupPending: false };
		try {
			return decideConsumedDraft(localStorage, localStorage.getItem(QUICK_DRAFT_KEY) || "", activeQuickStartDrafts);
		} catch {
			return { draft: "", cleanupPending: false };
		}
	})();

	// Committed-effect consumed-draft cleanup: opening QuickStart runs the
	// idempotent cleanup AFTER commit (never during render), so an aborted
	// render or a StrictMode double render cannot remove the draft or the
	// marker; the second StrictMode effect run is a no-op once the cleanup
	// succeeded. When the draft matches an active ledger job but the submit
	// path wrote neither marker nor removal, cleanupMarker recreates the marker
	// from that job's requestId before deleting the draft. A failed cleanup is
	// retried by remount/open or later snapshot renders while remaining visible.
	useEffect(() => {
		if (!(active && active.sourceId === "new") || !quickDraftDecision.cleanupPending) return;
		if (!cleanupConsumedDraft(localStorage, quickDraftDecision.cleanupMarker)) {
			setQuickError("清理已发送的本地草稿失败，重新打开会重试。");
		}
	}, [active, quickDraftDecision.cleanupMarker?.prompt, quickDraftDecision.cleanupMarker?.requestId, quickDraftDecision.cleanupPending]);
	// activeQuickJob is the ledger job behind the open optimistic icon; it can
	// become undefined while the popup is open when the job was just
	// reconciled into its real task icon.
	const activeQuickJob = active && isQuickStartJobItem(active) ? (() => {
		const requestId = quickStartJobRequestIDFromItem(active.id);
		return requestId ? quickJobs.jobs[requestId] : undefined;
	})() : undefined;

  return <main className="desktop-icon-mode" style={zoomStyle} aria-label="WorkGround2 桌面图标小组件">
		<span className="sr-only" aria-live="polite">{visibleItems.reduce((count, item) => count + item.unreadCount, 0)} 条桌面待处理信息</span>
		<div className="desktop-icon-cluster" style={{ transform: `scale(${clusterZoom})`, transformOrigin: "bottom right", "--cluster-zoom": String(clusterZoom), "--cluster-max-width": `${clusterMaxWidth}px` } as CSSProperties}>
			<div className="desktop-icon-grid" id="desktop-icon-grid">
				{!collapsed && rows.top.length > 0 && <div className="desktop-icon-row desktop-icon-row--top">{rows.top.map(renderItem)}</div>}
				{!collapsed && <div className="desktop-icon-row desktop-icon-row--bottom">{rows.bottom.map(renderItem)}</div>}
			</div>
			<div className="desktop-icon-controls">
				<button type="button" className="desktop-icon-collapse" title={collapsed ? "展开图标组" : "收起图标组"} aria-label={collapsed ? "展开图标组" : "收起图标组"} aria-expanded={!collapsed} aria-controls="desktop-icon-grid" onClick={toggleCollapsed}>{collapsed ? <ChevronUp aria-hidden="true" /> : <ChevronDown aria-hidden="true" />}</button>
				{overlayReady && quickOpen && <div id="desktop-icon-quick" className="desktop-icon-quick" role="toolbar" aria-label="小组件快捷操作" aria-busy={exiting} onKeyDown={quickRove} onClick={(event) => event.stopPropagation()}>
					<button type="button" className="desktop-icon-quick__btn" aria-label="缩小图标" title="缩小图标" disabled={exiting || clusterZoom <= ICON_ZOOM_MIN} onClick={() => applyClusterZoom(stepIconZoom(clusterZoom, -ICON_ZOOM_STEP))}><ZoomOut aria-hidden="true" /></button>
					<button type="button" className="desktop-icon-quick__btn" aria-label="放大图标" title="放大图标" disabled={exiting || clusterZoom >= ICON_ZOOM_MAX} onClick={() => applyClusterZoom(stepIconZoom(clusterZoom, ICON_ZOOM_STEP))}><ZoomIn aria-hidden="true" /></button>
					<button type="button" className={`desktop-icon-quick__btn${topmost ? " desktop-icon-quick__btn--on" : ""}`} role="switch" aria-checked={topmost} aria-label={topmost ? "取消保持置顶" : "保持置顶"} title={topmost ? "取消保持置顶" : "保持置顶"} disabled={exiting || topmostBusy || !topmostLoaded || topmostReadFailed} onClick={() => void toggleTopmost()}>{topmostBusy ? <Loader2 className="desktop-icon-quick__spin" aria-hidden="true" /> : topmost ? <Pin aria-hidden="true" /> : <PinOff aria-hidden="true" />}</button>
					<button type="button" className="desktop-icon-quick__btn" aria-label="打开主窗口" title="打开主窗口" disabled={exiting} onClick={() => void openMainWindow()}><ExternalLink aria-hidden="true" /></button>
					<button type="button" className="desktop-icon-quick__btn" aria-label="设置" title="设置" disabled={exiting} onClick={() => void openSettingsWindow()}><SettingsIcon aria-hidden="true" /></button>
				</div>}
				<button type="button" className={`desktop-icon-anchor${anchorMenuOpen ? " desktop-icon-anchor--menu-open" : ""}${quickOpen ? " desktop-icon-anchor--quick-open" : ""}`} title="拖动窗口移动小组件，左键打开快捷操作" aria-label="移动小组件窗口" aria-expanded={quickOpen} aria-controls="desktop-icon-quick" aria-haspopup="menu" onClick={toggleQuick} onPointerEnter={() => idleTrace.current?.hoverEnter("anchor", { iconCount: visibleItems.length, revision: snapshot.revision })} onContextMenu={(event) => {
					event.preventDefault();
					closeTransient();
					setAnchorMenuOpen(true);
				}}><img src={logoSymbol} alt="" draggable={false} /></button>
				{overlayReady && anchorMenuOpen && <div id="desktop-icon-anchor-menu" className="desktop-icon-anchor-menu" role="menu" onClick={(event) => event.stopPropagation()}><button type="button" role="menuitem" disabled={exiting} onClick={() => void openSettingsWindow()}>设置</button></div>}
			</div>
		</div>
		{popupItem && <section ref={popupRef} className={`desktop-icon-popup${active ? " desktop-icon-popup--interactive" : " desktop-icon-popup--preview"}${popupAttention ? " desktop-icon-popup--mention" : ""}`} style={popupStyle} role={popupAttention ? "alertdialog" : active ? "dialog" : "status"} aria-label={active ? popupAttention ? `${popupItem.title}，${roomAttentionLabel(popupAttention)}` : `${popupItem.title} 操作` : undefined} aria-live={popupAttention || popupItem.status === "failed" || popupItem.status === "needs_input" ? "assertive" : "polite"} onMouseEnter={() => timers.current?.clearPreviewClose()} onMouseLeave={(event) => { if (!event.currentTarget.contains(document.activeElement)) closePreviewSoon(); }}>
      <span className="desktop-icon-popup__arrow" aria-hidden="true" />
      {!active && <p tabIndex={0} aria-label={`${popupItem.title}，${previewText(popupItem)}`} onFocus={() => timers.current?.clearPreviewClose()} onBlur={closePreviewSoon}>{previewText(popupItem)}</p>}
      {active && active.sourceId === "new" && <QuickStart workspaces={workspaces} initialWorkspace={quickWorkspace} editJob={quickStartEditJob} initialDraft={quickDraftDecision.draft} submitJob={submitQuickStart} openWindowCreate={openWindowCreate} onClose={() => { setQuickStartEditJob(null); setPopupAnchorID(""); setActiveID(""); }} />}
      {active && active.sourceId === "search" && <SearchPanel onClose={() => setActiveID("")} onPick={(result) => run(active, "open_search", [result.id])} />}
      {active && active.sourceId === "delegate" && <DelegationPanel items={snapshot.delegations || []} error={snapshot.delegationError} busy={busy} onClose={() => setActiveID("")} onPick={(item) => run(active, "open_delegation", [item.id])} />}
      {active && active.sourceId === "workspace" && <WorkspaceManager initialIconRoot={workspaceIconRoot} onClose={() => { setWorkspaceIconRoot(""); setActiveID(""); }} onChanged={refresh} />}
      {active && active.sourceId === "rooms" && <RoomsManager roomIconCount={roomIconCount} onRoomIconCountChange={setRoomIconCount} notificationMode={roomNotificationMode} onNotificationModeChange={setRoomNotificationMode} onClose={() => setActiveID("")} onChanged={refresh} onNewRoom={onNewRoom} onOpenRoom={onOpenRoom} />}
      {active && active.sourceId === "dsh" && <DSHQuickStart workspaces={workspaces} onChanged={refresh} onClose={() => setActiveID("")} />}
      {active && active.kind === "workspace" && <DailyRoutinePanel key={active.sourceId} workspaceRoot={active.sourceId} onStartHere={() => { setQuickWorkspace(`project:${active.sourceId}`); setQuickStartEditJob(null); setPopupAnchorID(active.id); setActiveID("fixed:new"); }} onClose={() => setActiveID("")} />}
      {active && isQuickStartJobItem(active) && <QuickStartJobBody job={activeQuickJob} onRetry={(requestId) => { quickJobs.retry(requestId); }} onEdit={editQuickStartJob} onDismiss={(requestId) => { if (quickJobs.dismiss(requestId)) { setActiveID(""); setPreviewID(""); } }} onOpenMain={openMainWindow} onOpenTask={activeQuickJob?.phase === "accepted" && activeQuickJob.tabId ? () => void openQuickStartTask(activeQuickJob) : undefined} />}
      {active && activeNotice && <NoticeBody key={`${activeNotice.id}:${activeNotice.revision}`} item={active} notice={activeNotice} busy={busy} run={(action, values, answers) => run(active, action, values, activeNotice, undefined, answers)} onClose={() => { setActiveID(""); setActiveNoticeID(""); setPreviewID(""); }} />}
      {active && active.kind === "external" && !activeNotice && <ExternalRunBody item={active} busy={busy} run={(action) => void run(active, action)} />}
      {active && active.kind !== "external" && !activeNotice && active.runtimeStatus && <RuntimeBody item={active} busy={busy} run={(action) => void run(active, action)} />}
      {active && active.kind !== "external" && active.kind !== "workspace" && !isQuickStartJobItem(active) && !activeNotice && !active.runtimeStatus && active.sourceId !== "new" && active.sourceId !== "search" && active.sourceId !== "workspace" && active.sourceId !== "rooms" && active.sourceId !== "delegate" && active.sourceId !== "dsh" && <><strong>{active.title}</strong><p>{previewText(active)}</p><div className="desktop-icon-popup__actions"><button onClick={() => void run(active, "open")}>打开</button></div></>}

    </section>}
    {(error || quickError || quickJobs.storageError || routineNotice) && <div className="desktop-icon-toast" role={error || quickError || quickJobs.storageError ? "alert" : "status"}>{error}{error && (quickError || quickJobs.storageError || routineNotice) ? <span aria-hidden="true">；</span> : null}{quickError}{quickError && (quickJobs.storageError || routineNotice) ? <span aria-hidden="true">；</span> : null}{quickJobs.storageError}{quickJobs.storageError && routineNotice ? <span aria-hidden="true">；</span> : null}{routineNotice}<button aria-label="关闭" onClick={() => { setError(""); setQuickError(""); setRoutineNotice(""); quickJobs.clearStorageError(); }}><X /></button></div>}
  </main>;
}
