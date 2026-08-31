import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import {
  Activity,
  AlertCircle,
  Bot,
  Brain,
  CalendarClock,
  Check,
  ChevronLeft,
  ChevronRight,
  Clock3,
  ExternalLink,
  FolderOpen,
  History,
	Lightbulb,
  Megaphone,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Send,
  Settings2,
  Trash2,
  X,
} from "lucide-react";
import { useI18n } from "../../../lib/i18n";
import { useToast } from "../../../lib/toast";
import { isComposerSubmitKey, normalizeComposerSubmitKey, type ComposerSubmitKey } from "../../../lib/composerKeyboard";
import { AssistantMarkdown } from "./AssistantMarkdown";
import { AssistantWorkControlBar } from "./AssistantWorkControlBar";
import {
  assistantApplyMemory,
  assistantCancel,
  assistantCreate,
  assistantDelete,
  assistantGet,
  assistantIdeate,
  assistantList,
  assistantListWorkspaces,
  assistantManagedSessions,
  assistantPickWorkspace,
  assistantPublishViewport,
  assistantPutChannel,
  assistantPutRoutine,
  assistantResolveAttention,
	assistantResolveIdea,
	assistantResolveProposal,
  assistantResume,
  assistantRetryDispatch,
  assistantRunNow,
  assistantSessionCancel,
  assistantSubmit,
  assistantSupervisorDiagnostic,
  assistantUpdate,
  assistantViewport,
  onAssistantDispatchStream,
} from "./assistant.bridge";
import { assistantCopy } from "./assistant.copy";
import { applyAssistantDispatchStream, attentionInboxAction, attentionRejectResolution, attentionResolution, dispatchStateLabel, formatAssistantDate, formatTimelineTime, ideaStateLabel, jobStateLabel, liveReplyFromDispatch, reconcileAssistantLiveReply, responsibilityLabel, responsibilityStatusLabel, runHasActionableAttention, runHistoryAction, runStateLabel, scheduleLabel, seedAssistantLiveReply, timelineEntries, type AssistantLiveReply } from "./assistant.model";
import { assistantIntentKey, assistantMutationKey, assistantOutcomeKey, completeAssistantRequest, pendingAssistantRequest, runAssistantApproval, runAssistantCASMutation, runAssistantMutation, runAssistantOutcome, runAssistantRejection, runAssistantResume } from "./assistant.requests";
import { assistantTemplate, assistantTemplateContent, templateRoutine, templateRoutines, type AssistantTemplateID } from "./assistant.templates";
import {
  assistantEntityID,
  type AssistantAccess,
  type AssistantChannel,
	type AssistantChangeProposal,
  type AssistantManagedSession,
  type AssistantMemoryKind,
  type AssistantDiagnostic,
  type AssistantPolicy,
  type AssistantRecord,
  type AssistantRun,
  type AssistantRunnerJob,
  type AssistantRoutine,
  type AssistantScheduleKind,
  type AssistantSessionControlOutcome,
  type AssistantSessionControlResult,
  type AssistantSnapshot,
  type AssistantSupervisorDiagnostic,
} from "./assistant.types";
import "./assistant.css";
import type { ProjectNode } from "../../../lib/types";

type ManageTab = "overview" | "sessions" | "diagnostics" | "supervisor" | "routines" | "memory" | "channels" | "proposals" | "history" | "attention" | "plan";

export interface AssistantSessionTarget {
  scope: "global" | "project";
  workspaceRoot: string;
  sessionPath: string;
  assistantID: string;
  assistantName: string;
}

interface AssistantWorkspaceProps {
  onOpenSession?: (target: AssistantSessionTarget) => void;
  focusAssistantID?: string;
  composerSubmitKey?: ComposerSubmitKey;
}

function assistantRunSessionTarget(run: AssistantRun, owner: AssistantRecord): AssistantSessionTarget | null {
  const sessionPath = run.session_path?.trim();
  if (!sessionPath) return null;
  const scope = (run.scope || owner.scope) === "workspace" ? "project" : "global";
  return {
    scope,
    workspaceRoot: scope === "project" ? run.workspace_root || owner.workspace_root || "" : "",
    sessionPath,
    assistantID: owner.id,
    assistantName: owner.name,
  };
}

function assistantJobSessionTarget(job: AssistantRunnerJob, owner: AssistantRecord): AssistantSessionTarget | null {
  const sessionPath = job.session_path?.trim();
  if (!sessionPath) return null;
  const scope = (job.scope || owner.scope) === "workspace" ? "project" : "global";
  return {
    scope,
    workspaceRoot: scope === "project" ? job.workspace_root || owner.workspace_root || "" : "",
    sessionPath,
    assistantID: owner.id,
    assistantName: owner.name,
  };
}

export function AssistantSidebarEntry({ active, collapsed = false, onClick }: { active: boolean; collapsed?: boolean; onClick: () => void }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  return (
    <button
      type="button"
      className={`assistant-sidebar-entry${active ? " assistant-sidebar-entry--active" : ""}`}
      aria-current={active ? "page" : undefined}
      aria-label={copy.entry}
      title={collapsed ? copy.entry : undefined}
      onClick={onClick}
    >
      <Bot size={17} aria-hidden="true" />
      {!collapsed && <span>{copy.entry}</span>}
    </button>
  );
}

function lifecycleLabel(assistant: AssistantRecord, copy: ReturnType<typeof assistantCopy>) {
  if (assistant.lifecycle === "paused") return copy.paused;
  if (assistant.lifecycle === "archived") return copy.archived;
  return copy.awake;
}

export function assistantDiagnosticWarning(diagnostics: AssistantDiagnostic[], copy: ReturnType<typeof assistantCopy>) {
  if (diagnostics.some((item) => item.category === "data" || item.operation === "list")) return copy.partialWarning;
  if (diagnostics.some((item) => item.operation === "progress_apply" || item.operation === "progress_parse")) return copy.progressWarning;
  return copy.runtimeWarning;
}

// Missing or unparseable timestamps map to epoch 0 so they safely sink to the
// bottom of the newest-first ordering instead of breaking the sort with NaN.
export function diagnosticTimeMillis(at: string): number {
  if (!at) return 0;
  const parsed = new Date(at).getTime();
  return Number.isNaN(parsed) ? 0 : parsed;
}

// Newest first, never mutating the source array; ties (including invalid
// timestamps) keep their original relative order via the stable sort.
export function sortedDiagnostics(diagnostics: AssistantDiagnostic[]): AssistantDiagnostic[] {
  return [...diagnostics].sort((a, b) => diagnosticTimeMillis(b.at) - diagnosticTimeMillis(a.at));
}

// Local, human-readable timestamp; returns "" for missing/invalid input so the
// caller can render a fallback label instead of throwing.
export function formatDiagnosticTime(at: string, locale: string): string {
  if (!at) return "";
  const parsed = new Date(at);
  if (Number.isNaN(parsed.getTime())) return "";
  const lang = locale === "en" ? "en-GB" : locale === "zh-TW" ? "zh-TW" : "zh-CN";
  return new Intl.DateTimeFormat(lang, {
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", hour12: false,
  }).format(parsed);
}

export function diagnosticCategoryLabel(category: AssistantDiagnostic["category"], copy: ReturnType<typeof assistantCopy>): string {
  if (category === "data") return copy.categoryData;
  if (category === "runtime") return copy.categoryRuntime;
  return copy.unknownValue;
}

function useAssistantData(focusAssistantID?: string) {
  const [assistants, setAssistants] = useState<AssistantRecord[]>([]);
  const [diagnostics, setDiagnostics] = useState<AssistantDiagnostic[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [snapshot, setSnapshot] = useState<AssistantSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const generation = useRef(0);

  const loadList = useCallback(async (preferredID?: string) => {
    const gen = ++generation.current;
    setLoading(true);
    setError("");
    try {
      const result = await assistantList();
      if (generation.current !== gen) return;
      const list = result.items;
      setAssistants(list);
      setDiagnostics(result.diagnostics);
      const id = preferredID && list.some((item) => item.id === preferredID)
        ? preferredID
        : selectedID && list.some((item) => item.id === selectedID)
          ? selectedID
          : list[0]?.id ?? "";
      setSelectedID(id);
      if (!id) {
        setSnapshot(null);
        return;
      }
      const next = await assistantGet(id);
      if (generation.current !== gen) return;
      setSnapshot(next);
    } catch (cause) {
      if (generation.current !== gen) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (generation.current === gen) setLoading(false);
    }
  }, [selectedID]);

  const refresh = useCallback(async () => {
    if (!selectedID) return loadList();
    const gen = ++generation.current;
    try {
      const next = await assistantGet(selectedID);
      if (generation.current !== gen) return;
      setSnapshot((current) => !current || next.revision > current.revision ? next : current);
      setAssistants((items) => items.map((item) => item.id === next.assistant.id ? next.assistant : item));
      setError("");
    } catch (cause) {
      if (generation.current === gen) setError(cause instanceof Error ? cause.message : String(cause));
    }
  }, [loadList, selectedID]);

  const select = useCallback(async (id: string) => {
    if (!id || id === selectedID) return;
    setSelectedID(id);
    setLoading(true);
    const gen = ++generation.current;
    try {
      const next = await assistantGet(id);
      if (generation.current === gen) {
        setSnapshot(next);
        setError("");
      }
    } catch (cause) {
      if (generation.current === gen) setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (generation.current === gen) setLoading(false);
    }
  }, [selectedID]);

  useEffect(() => { void loadList(); }, []);
  useEffect(() => {
    if (!focusAssistantID) return;
    void loadList(focusAssistantID);
  }, [focusAssistantID]);
  useEffect(() => {
    if (!selectedID) return;
    const timer = window.setInterval(() => { void refresh(); }, 4000);
    return () => window.clearInterval(timer);
  }, [refresh, selectedID]);

  return { assistants, diagnostics, selectedID, snapshot, loading, error, loadList, refresh, select };
}

export function AssistantWorkspace({ onOpenSession, focusAssistantID, composerSubmitKey }: AssistantWorkspaceProps) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const submitKey = normalizeComposerSubmitKey(composerSubmitKey);
  const { showToast } = useToast();
  const data = useAssistantData(focusAssistantID);
  const [manageTab, setManageTab] = useState<ManageTab | null>(null);
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState("");
  const [handoff, setHandoff] = useState("");
  const [handoffNotice, setHandoffNotice] = useState(false);
  const [liveReply, setLiveReply] = useState<AssistantLiveReply | null>(null);
  const liveReplyViewportRef = useRef<HTMLDivElement>(null);
  const today = useMemo(() => new Date(), [data.snapshot?.revision]);
  const timeline = useMemo(() => data.snapshot ? timelineEntries(data.snapshot, today, locale, copy) : [], [copy, data.snapshot, locale, today]);
  const openAttention = data.snapshot?.attention.filter((item) => attentionInboxAction(item, data.snapshot?.runs.find((run) => run.id === item.run_id)) !== "none").length ?? 0;
	const openProposals = data.snapshot?.proposals?.filter((item) => item.state === "pending").length ?? 0;

  const run = useCallback(async (routineID?: string) => {
    if (!data.snapshot || busy) return;
    setBusy("run");
    const intentKey = assistantIntentKey("run", data.snapshot.assistant.id, routineID || "default");
    try {
      await assistantRunNow({ assistantId: data.snapshot.assistant.id, routineId: routineID, requestId: pendingAssistantRequest(intentKey), maxAttempts: 3 });
      completeAssistantRequest(intentKey);
      setHandoffNotice(true);
      await data.refresh();
    } catch (cause) {
      showToast(cause instanceof Error ? cause.message : copy.error, "error");
    } finally {
      setBusy("");
    }
  }, [busy, copy.error, data, showToast]);

  const submitHandoff = useCallback(async () => {
    const prompt = handoff.trim();
    if (!data.snapshot || !prompt || busy) return;
    setBusy("handoff");
    const intentKey = assistantIntentKey("handoff", data.snapshot.assistant.id, prompt);
    try {
      const dispatch = await assistantSubmit({ assistantId: data.snapshot.assistant.id, requestId: pendingAssistantRequest(intentKey), input: prompt });
      completeAssistantRequest(intentKey);
      setHandoff("");
      setHandoffNotice(true);
      setLiveReply(liveReplyFromDispatch(dispatch, data.snapshot?.jobs ?? []));
      await data.refresh();
    } catch (cause) {
      showToast(cause instanceof Error ? cause.message : copy.error, "error");
    } finally {
      setBusy("");
    }
  }, [busy, copy.error, data, handoff, showToast]);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const composingRef = useRef(false);

  const ideate = useCallback(async () => {
    if (!data.snapshot || busy) return;
    setBusy("ideate");
    const intentKey = assistantIntentKey("ideate", data.snapshot.assistant.id);
    try {
      await assistantIdeate({ assistantId: data.snapshot.assistant.id, requestId: pendingAssistantRequest(intentKey) });
      completeAssistantRequest(intentKey);
      await data.refresh();
    } catch (cause) {
      showToast(cause instanceof Error ? cause.message : copy.error, "error");
    } finally {
      setBusy("");
    }
  }, [busy, copy.error, data, showToast]);

  const resolveIdea = useCallback(async (ideaID: string, revision: number, decision: "accept" | "reject") => {
    if (!data.snapshot || busy) return;
    const assistantID = data.snapshot.assistant.id;
    setBusy(`idea-${ideaID}`);
    const key = assistantMutationKey(`idea-${decision}`, assistantID, ideaID, decision);
    try {
      await runAssistantCASMutation(key, revision, ({ requestId, expectedRevision }) => assistantResolveIdea({
        assistantId: assistantID, ideaId: ideaID, requestId, expectedRevision, decision,
        resolution: decision === "accept" ? "accepted from Desktop" : "rejected from Desktop",
      }));
      await data.refresh();
    } catch (cause) {
      showToast(cause instanceof Error ? cause.message : copy.error, "error");
    } finally {
      setBusy("");
    }
  }, [busy, copy.error, data, showToast]);

  const retryClassification = useCallback(async (dispatchID: string) => {
    if (!data.snapshot || busy) return;
    setBusy(`dispatch-${dispatchID}`);
    const intentKey = assistantIntentKey("dispatch-retry", data.snapshot.assistant.id, dispatchID);
    try {
      await assistantRetryDispatch({ assistantId: data.snapshot.assistant.id, dispatchId: dispatchID, requestId: pendingAssistantRequest(intentKey) });
      completeAssistantRequest(intentKey);
      await data.refresh();
    } catch (cause) {
      showToast(cause instanceof Error ? cause.message : copy.error, "error");
    } finally {
      setBusy("");
    }
  }, [busy, copy.error, data, showToast]);

  const resizeHandoff = useCallback(() => {
    const element = textareaRef.current;
    if (!element) return;
    element.style.height = "auto";
    const max = 120;
    const height = Math.min(Math.max(element.scrollHeight, 0), max);
    element.style.height = height > 0 ? `${height}px` : "";
    element.style.overflowY = element.scrollHeight > max ? "auto" : "hidden";
  }, []);

  useEffect(() => { resizeHandoff(); }, [handoff, resizeHandoff]);

  const selectedAssistantID = data.selectedID;
  const selectedIDRef = useRef(selectedAssistantID);
  useEffect(() => { selectedIDRef.current = selectedAssistantID; }, [selectedAssistantID]);

  useEffect(() => onAssistantDispatchStream((event) => {
    if (event.assistantId !== selectedIDRef.current) return;
    setLiveReply((prev) => applyAssistantDispatchStream(prev, event));
  }), []);

  useEffect(() => {
    const snapshot = data.snapshot;
    if (!snapshot) return;
    setLiveReply((prev) => {
      if (!prev) return seedAssistantLiveReply(snapshot);
      return reconcileAssistantLiveReply(prev, snapshot);
    });
  }, [data.snapshot]);

  useEffect(() => { setLiveReply(null); }, [selectedAssistantID]);

  useEffect(() => {
    if (liveReply?.phase !== "streaming") return;
    const viewport = liveReplyViewportRef.current;
    if (viewport) viewport.scrollTop = viewport.scrollHeight;
  }, [liveReply?.phase, liveReply?.reply]);

  // ── 受管 Session 区：只读投影 + 用户控制（控制见 section 组件） ──
  const [managedSessions, setManagedSessions] = useState<AssistantManagedSession[]>([]);
  const [managedReload, setManagedReload] = useState(0);
  useEffect(() => {
    setManagedSessions([]);
    const id = data.selectedID;
    if (!id) return;
    let alive = true;
    const load = async () => {
      try {
        const sessions = await assistantManagedSessions(id);
        if (alive) setManagedSessions(sessions);
      } catch { /* keep last known list; the poll retries */ }
    };
    void load();
    const timer = window.setInterval(() => { void load(); }, 5000);
    return () => { alive = false; window.clearInterval(timer); };
  }, [data.selectedID, managedReload]);

  // viewport：把真实 UI 选中/可见的 Session 发布给后端，revision 单调递增。
  const viewportRef = useRef({ revision: 0 });
  useEffect(() => {
    let alive = true;
    void assistantViewport().then(([snap]) => {
      if (alive) viewportRef.current.revision = Math.max(viewportRef.current.revision, snap.ui_revision ?? 0);
    }).catch(() => {});
    return () => { alive = false; };
  }, [data.selectedID]);
  const visibleSessionIDs = useMemo(
    () => manageTab === "sessions" ? managedSessions.map((item) => item.id) : [],
    [manageTab, managedSessions],
  );
  const visibleSessionKey = visibleSessionIDs.join(",");
  useEffect(() => {
    const current = data.snapshot?.assistant;
    if (!current) return;
    viewportRef.current.revision += 1;
    assistantPublishViewport({
      windowId: "assistant-main",
      workspaceId: current.workspace_root ?? "",
      visibleSessionIds: visibleSessionIDs,
      selectedSessionId: "",
      uiRevision: viewportRef.current.revision,
    });
  }, [data.snapshot?.assistant.id, data.snapshot?.assistant.workspace_root, visibleSessionKey]);

  const handleHandoffKeyDown = useCallback((event: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (isComposerSubmitKey(event, submitKey, composingRef.current)) {
      event.preventDefault();
      void submitHandoff();
    }
  }, [submitKey, submitHandoff]);

  if (data.loading && !data.snapshot) {
    return <section className="assistant-workspace assistant-workspace--center" aria-live="polite"><RefreshCw className="assistant-spin" size={22} /><p>{copy.loading}</p></section>;
  }

  if (data.error && !data.snapshot) {
    return (
      <section className="assistant-workspace assistant-workspace--center" role="alert">
        <AlertCircle size={24} />
        <h1>{copy.loadFailed}</h1>
        <p>{data.error}</p>
        <button className="assistant-button" type="button" onClick={() => void data.loadList()}>{copy.retry}</button>
      </section>
    );
  }

  if (!data.snapshot) {
    return (
      <section className="assistant-workspace assistant-workspace--empty">
        <Bot size={30} aria-hidden="true" />
        <h1>{copy.emptyTitle}</h1>
        <p>{copy.emptyBody}</p>
        <button className="assistant-button assistant-button--accent" type="button" onClick={() => setCreating(true)}><Plus size={16} />{copy.newAssistant}</button>
        {creating && <CreateAssistantDialog onClose={() => setCreating(false)} onCreated={(id) => { setCreating(false); void data.loadList(id); }} />}
      </section>
    );
  }

  const snapshot = data.snapshot;
  const assistant = snapshot.assistant;
  return (
    <section className="assistant-workspace" aria-label={copy.entry}>
      <header className="assistant-workspace__topbar">
        <div className="assistant-picker-wrap">
          <select className="assistant-picker" value={data.selectedID} onChange={(event) => void data.select(event.target.value)} aria-label={copy.entry}>
            {data.assistants.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
          <span className={`assistant-presence assistant-presence--${assistant.lifecycle}`} aria-hidden="true" />
          <span className="assistant-state">{lifecycleLabel(assistant, copy)}</span>
        </div>
        <div className="assistant-workspace__actions">
		  {openProposals > 0 && <button className="assistant-proposal-chip" type="button" title={copy.proposals} aria-label={`${copy.proposals} ${openProposals}`} onClick={() => setManageTab("proposals")}><Lightbulb size={14} />{openProposals}</button>}
          {openAttention > 0 && <button className="assistant-attention-chip" type="button" onClick={() => setManageTab("attention")}><AlertCircle size={14} />{openAttention}</button>}
          <button className="assistant-icon-button" type="button" aria-label={copy.ideate} title={copy.ideate} disabled={Boolean(busy)} onClick={() => void ideate()}><Lightbulb size={17} /></button>
          <button className="assistant-icon-button" type="button" aria-label={copy.newAssistant} title={copy.newAssistant} onClick={() => setCreating(true)}><Plus size={17} /></button>
          <button className="assistant-icon-button" type="button" aria-label={copy.manage} title={copy.manage} onClick={() => setManageTab("overview")}><Settings2 size={17} /></button>
        </div>
      </header>

      <AssistantWorkControlBar copy={copy} />

      {data.diagnostics.length > 0 && (
        <div className="assistant-diagnostic" role="status">
          <AlertCircle size={14} aria-hidden="true" />
          <span>{assistantDiagnosticWarning(data.diagnostics, copy)}</span>
          <button type="button" onClick={() => setManageTab("diagnostics")}>{copy.viewDetails}</button>
        </div>
      )}

      <div className="assistant-handoff-zone">
        <form className="assistant-handoff" onSubmit={(event) => { event.preventDefault(); void submitHandoff(); }}>
          <label className="sr-only" htmlFor="assistant-handoff-input">{copy.taskPlaceholder}</label>
          <textarea
            id="assistant-handoff-input"
            ref={textareaRef}
            rows={1}
            value={handoff}
            onChange={(event) => setHandoff(event.target.value)}
            onKeyDown={handleHandoffKeyDown}
            onCompositionStart={() => { composingRef.current = true; }}
            onCompositionEnd={() => { composingRef.current = false; }}
            placeholder={copy.taskPlaceholder}
            disabled={Boolean(busy)}
          />
          <button type="submit" disabled={!handoff.trim() || Boolean(busy)} aria-label={copy.send} title={copy.send}><Send size={16} /></button>
        </form>
        <div className="assistant-handoff__meta">
          <span className="assistant-handoff__hint">{submitKey === "ctrl_enter" ? "Ctrl+Enter" : "Enter"} {copy.sendShortcut}</span>
          <span className="assistant-handoff__record">{copy.inputRecorded}</span>
          {handoffNotice && (
            <span className="assistant-handoff__status" role="status">
              <Check size={13} aria-hidden="true" />
              <span>{copy.queued}</span>
              <button type="button" aria-label={copy.close} onClick={() => setHandoffNotice(false)}><X size={12} /></button>
            </span>
          )}
        </div>
        {liveReply && liveReply.assistantId === assistant.id && (
          <div className="assistant-live-reply" data-phase={liveReply.phase}>
            <div className="assistant-live-reply__head">
              <Bot size={14} className="assistant-live-reply__mark" aria-hidden="true" />
              <span className={liveReply.phase === "failed" ? "assistant-live-reply__error" : "assistant-live-reply__label"}>
                {liveReply.phase === "accepted"
                  ? copy.assistantUnderstanding
                  : liveReply.phase === "streaming"
                    ? copy.assistantReplying
                    : liveReply.phase === "committed"
                      ? (liveReply.jobCount > 0 ? `${copy.understood} · ${copy.arrangedWork.replace("{n}", String(liveReply.jobCount))}` : copy.replied)
                      : copy.classificationFailed}
              </span>
              {(liveReply.phase === "accepted" || liveReply.phase === "streaming") ? <span className="assistant-live-reply__pulse" aria-hidden="true" /> : null}
              {liveReply.phase === "failed" ? (
                <button type="button" className="assistant-live-reply__retry" disabled={Boolean(busy)} onClick={() => void retryClassification(liveReply.dispatchId)}><RefreshCw size={12} />{copy.retry}</button>
              ) : null}
            </div>
            <div ref={liveReplyViewportRef} className="assistant-live-reply__viewport" role="status" aria-live="polite" aria-atomic="false">
              {liveReply.phase === "failed" ? (
                <span className="assistant-live-reply__error">{liveReply.error || copy.classificationFailed}</span>
              ) : liveReply.reply ? (
                <span className="assistant-live-reply__text">{liveReply.reply}{liveReply.phase === "streaming" ? <span className="assistant-live-reply__caret" aria-hidden="true" /> : null}</span>
              ) : (
                <span className="assistant-live-reply__empty">{copy.liveReplyWaiting}{liveReply.phase === "accepted" ? <span className="assistant-live-reply__caret" aria-hidden="true" /> : null}</span>
              )}
            </div>
          </div>
        )}
      </div>

      <div className="assistant-workspace__scroll">
        <div className="assistant-day">
          <h1><span>{copy.today}</span>，{formatAssistantDate(today, locale)}</h1>
        </div>
        <div className="assistant-timeline" aria-live="polite">
          {timeline.length === 0 ? <p className="assistant-timeline__empty">{copy.timelineEmpty}</p> : timeline.map((entry) => (
            <article key={entry.id} className={`assistant-event assistant-event--${entry.kind}`}>
              <time dateTime={entry.at.toISOString()}>{formatTimelineTime(entry.at, locale)}</time>
              <span className="assistant-event__node" aria-hidden="true" />
              <div className="assistant-event__body">
                <h2>{entry.title}</h2>
                {entry.kind === "run" && entry.run && (() => {
                  const run = entry.run;
                  const label = runStateLabel(run.state, locale);
                  const target = assistantRunSessionTarget(run, assistant);
                  return target && onOpenSession ? (
                    <button
                      type="button"
                      className={`assistant-run-state assistant-run-state--${run.state} assistant-run-state--link`}
                      title={copy.openSession}
                      aria-label={`${label}，${copy.openSession}`}
                      onClick={() => onOpenSession(target)}
                    >
                      {label}
                      <ExternalLink size={11} aria-hidden="true" />
                    </button>
                  ) : (
                    <span className={`assistant-run-state assistant-run-state--${run.state}`}>{label}</span>
                  );
                })()}
                {entry.kind === "run" ? (
                  <>
                    {entry.prompt ? <div className="assistant-event__prompt"><span>{copy.youSaid}</span><p>{entry.prompt}</p></div> : null}
                    {entry.run?.summary ? <div className="assistant-event__summary"><AssistantMarkdown text={entry.run.summary} /></div> : null}
                  </>
                ) : (
                  entry.detail && entry.detail !== entry.title ? <p>{entry.detail}</p> : null
                )}
                {entry.run?.error && entry.run.error.message.trim() !== entry.run.summary?.trim() && <p className="assistant-event__error">{entry.run.error.message}</p>}
                {entry.kind === "dispatch" && entry.dispatch ? (() => {
                  const dispatch = entry.dispatch;
                  const stateClass = dispatch.state === "classification_failed" ? "failed" : dispatch.state;
                  return (
                    <>
                      <span className={`assistant-run-state assistant-run-state--${stateClass}`}>{dispatchStateLabel(dispatch.state, locale)}</span>
                      {dispatch.state === "classification_failed" ? <p className="assistant-event__error">{copy.classificationFailed}<button type="button" className="assistant-text-action" disabled={Boolean(busy)} onClick={() => void retryClassification(dispatch.id)}><RefreshCw size={13} />{copy.retry}</button></p> : null}
                      {dispatch.state === "reflection_failed" ? <p className="assistant-event__error">{copy.reflectionFailed}</p> : null}
                      {dispatch.reply ? <p>{dispatch.reply}</p> : null}
                      {entry.prompt ? <div className="assistant-event__prompt"><span>{copy.youSaid}</span><p>{entry.prompt}</p></div> : null}
                      {entry.jobs && entry.jobs.length > 0 ? (
                        <ul className="assistant-jobs">
                          {entry.jobs.map((job) => (
                            // 主执行视角不再操作 RunnerJob：历史只读展示（无 retry/stop）。
                            <AssistantJobRow key={job.id} job={job} busy={busy} owner={assistant} onOpenSession={onOpenSession} />
                          ))}
                        </ul>
                      ) : null}
                      {entry.pack ? <div className="assistant-event__summary"><strong>{copy.reflection}：</strong><AssistantMarkdown text={entry.pack.conclusion} /></div> : null}
                    </>
                  );
                })() : null}
                {entry.kind === "idea" && entry.idea ? (() => {
                  const idea = entry.idea;
                  return (
                    <div className="assistant-idea">
                      <span className="assistant-run-state assistant-run-state--waiting_approval">{ideaStateLabel(idea.state, locale)}</span>
                      {idea.rationale ? <p>{idea.rationale}</p> : null}
                      <div className="assistant-idea__actions">
                        <button type="button" className="assistant-text-action" disabled={Boolean(busy)} onClick={() => void resolveIdea(idea.id, idea.revision, "accept")}><Check size={13} />{copy.acceptIdea}</button>
                        <button type="button" className="assistant-text-action" disabled={Boolean(busy)} onClick={() => void resolveIdea(idea.id, idea.revision, "reject")}><X size={13} />{copy.rejectIdea}</button>
                      </div>
                    </div>
                  );
                })() : null}
                {entry.kind === "next" && entry.routine && (
                  <button type="button" className="assistant-text-action" onClick={() => setManageTab("routines")}>{copy.changeTime}<ChevronRight size={13} /></button>
                )}
              </div>
            </article>
          ))}
        </div>
      </div>

      {manageTab && (
        <AssistantManager
          snapshot={snapshot}
          tab={manageTab}
          onTab={setManageTab}
          onClose={() => setManageTab(null)}
          onRefresh={data.refresh}
          onDeleted={() => data.loadList()}
          onRun={run}
          onOpenSession={onOpenSession}
          diagnostics={data.diagnostics}
          managedSessions={managedSessions}
          onManagedChanged={() => setManagedReload((value) => value + 1)}
        />
      )}
      {creating && <CreateAssistantDialog onClose={() => setCreating(false)} onCreated={(id) => { setCreating(false); void data.loadList(id); }} />}
    </section>
  );
}

// ── 受管 Session 紧凑列表 ────────────────────────────────────
// 只展示用户判断和控制运行所需的信息：运行目的、状态、更新时间、打开与停止。
// 停止沿用稳定 request_id；失败重试复用同一 ID，成功后释放。

function sessionStatusLabel(status: string, copy: ReturnType<typeof assistantCopy>): string {
  switch (status) {
    case "running": return copy.sessionStatusRunning;
    case "waiting": return copy.sessionStatusWaiting;
    case "retrying": return copy.sessionStatusRetrying;
    case "failed": return copy.sessionStatusFailed;
    case "completed": return copy.sessionStatusCompleted;
    default: return copy.sessionStatusIdle;
  }
}

function sessionOutcomeLabel(outcome: AssistantSessionControlOutcome, copy: ReturnType<typeof assistantCopy>): string {
  switch (outcome) {
    case "accepted": return copy.outcomeAccepted;
    case "already_applied": return copy.outcomeAlreadyApplied;
    case "stale": return copy.outcomeStale;
    case "retryable_error": return copy.outcomeRetryable;
    case "invalid": return copy.outcomeInvalid;
    case "blocked_by_policy": return copy.outcomeBlockedPolicy;
  }
}

function AssistantManagedSessionsSection({ assistant, sessions, onOpenSession, onChanged, copy }: {
  assistant: AssistantRecord;
  sessions: AssistantManagedSession[];
  onOpenSession?: (target: AssistantSessionTarget) => void;
  onChanged: () => void;
  copy: ReturnType<typeof assistantCopy>;
}) {
  const { locale } = useI18n();
  const [busyID, setBusyID] = useState("");
  const [outcomes, setOutcomes] = useState<Record<string, AssistantSessionControlResult>>({});

  // 稳定 request_id：key 固定为 (action, assistant, session)；失败不释放，
  // 重试复用同一 request_id；accepted/already_applied 后释放。
  const runControl = async (op: string, sessionID: string, request: (requestId: string) => Promise<AssistantSessionControlResult>) => {
    if (busyID) return;
    const key = assistantIntentKey(`session-${op}`, assistant.id, sessionID);
    const requestId = pendingAssistantRequest(key);
    setBusyID(`${sessionID}:${op}`);
    try {
      const result = await request(requestId);
      if (result.outcome === "accepted" || result.outcome === "already_applied") completeAssistantRequest(key);
      setOutcomes((prev) => ({ ...prev, [`${sessionID}:${op}`]: result }));
      onChanged();
    } catch (cause) {
      // 失败保留 request_id：下一次重试必须复用同一个 request_id。
      setOutcomes((prev) => ({ ...prev, [`${sessionID}:${op}`]: {
        outcome: "retryable_error", session_id: sessionID,
        message: cause instanceof Error ? cause.message : String(cause),
        at: new Date().toISOString(),
      } }));
    } finally {
      setBusyID("");
    }
  };

  if (sessions.length === 0) {
    return <div className="assistant-managed" aria-label={copy.managedSessions}><h2 className="assistant-managed__title">{copy.managedSessions}</h2><p className="assistant-managed__empty">{copy.managedSessionsEmpty}</p></div>;
  }
  return (
    <div className="assistant-managed" aria-label={copy.managedSessions}>
      <h2 className="assistant-managed__title">{copy.managedSessions}</h2>
      <div className="assistant-managed__list" role="table">
        <div className="assistant-managed__head" role="row">
          <span role="columnheader">{copy.sessionPurpose}</span>
          <span role="columnheader">{copy.sessionStatus}</span>
          <span role="columnheader">{copy.sessionUpdated}</span>
          <span role="columnheader" aria-label={copy.sessionActions} />
        </div>
        {sessions.map((session) => {
          const workspace = session.workspace_root || assistant.workspace_root || "";
          const canStop = session.status === "running" || session.status === "waiting" || session.status === "retrying";
          const cancelOutcome = outcomes[`${session.id}:cancel`];
          const target: AssistantSessionTarget = {
            scope: workspace ? "project" : "global",
            workspaceRoot: workspace,
            sessionPath: session.path,
            assistantID: assistant.id,
            assistantName: assistant.name,
          };
          return (
            <article key={session.id} className="assistant-managed__row" data-session-id={session.id} role="row">
              <strong className="assistant-managed__purpose" role="cell" title={session.title || session.id}>{session.title || session.id}</strong>
              <span className={`assistant-run-state assistant-run-state--${session.status}`} role="cell">{sessionStatusLabel(session.status, copy)}</span>
              <time role="cell" dateTime={session.updated_at || undefined} title={session.updated_at || undefined}>
                {session.updated_at ? formatTimelineTime(new Date(session.updated_at), locale) : "—"}
              </time>
              <div className="assistant-managed__actions" role="cell">
                {onOpenSession ? (
                  <button type="button" className="assistant-text-action" aria-label={`${session.title || session.id}，${copy.sessionOpen}`} onClick={() => onOpenSession(target)}><ExternalLink size={12} />{copy.sessionOpen}</button>
                ) : null}
                <button
                  className="assistant-text-action"
                  type="button"
                  aria-label={`${session.title || session.id}，${copy.sessionCancel}`}
                  disabled={Boolean(busyID) || !canStop}
                  onClick={() => void runControl("cancel", session.id, (requestId) => assistantSessionCancel({ sessionId: session.id, requestId }))}
                ><X size={12} />{copy.sessionCancel}</button>
                {cancelOutcome ? (
                  <span className={`assistant-session-outcome assistant-session-outcome--${cancelOutcome.outcome}`} role="status" title={cancelOutcome.message || cancelOutcome.next_hint}>
                    {sessionOutcomeLabel(cancelOutcome.outcome, copy)}
                  </span>
                ) : null}
              </div>
            </article>
          );
        })}
      </div>
    </div>
  );
}

// ── 监督诊断面板（设计 16.5）─────────────────────────────────
// 复用后端 AssistantSupervisorDiagnostic DTO：supervisor Session、cycle 观察
// 版本、pending events、recent decisions/receipts、next step、retry/failure。

function AssistantSupervisorDiagnosticPanel({ assistantID, copy }: { assistantID: string; copy: ReturnType<typeof assistantCopy> }) {
  const [diag, setDiag] = useState<AssistantSupervisorDiagnostic | null>(null);
  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const value = await assistantSupervisorDiagnostic(assistantID);
        if (alive) setDiag(value);
      } catch { /* keep last known diagnostic; the poll retries */ }
    };
    void load();
    const timer = window.setInterval(() => { void load(); }, 5000);
    return () => { alive = false; window.clearInterval(timer); };
  }, [assistantID]);
  if (!diag) return null;
  const hasContent = Boolean(diag.supervisor || diag.cycle || diag.next_step ||
    diag.pending_events?.length || diag.recent_decisions?.length || diag.recent_receipts?.length ||
    diag.running_sessions?.length || diag.failed_sessions?.length || diag.retry_due > 0 || diag.diagnostics?.length);
  if (!hasContent) return null;
  return (
    <details className="assistant-supervisor">
      <summary className="assistant-supervisor__head"><Activity size={14} aria-hidden="true" />{copy.supervisorDiagnostic}</summary>
      <div className="assistant-supervisor__body">
        {diag.supervisor ? (
          <div className="assistant-supervisor__row"><strong>{copy.supervisorSession}</strong><code>{diag.supervisor.id}</code></div>
        ) : null}
        {diag.cycle ? (
          <div className="assistant-supervisor__row">
            <strong>{copy.supervisorCycle}</strong>
            <span>{diag.cycle.state} · fence {diag.cycle.fence} · rev {diag.cycle.revision}</span>
            <span>{copy.cycleObserved}：{copy.cyclePlanRevision} {diag.cycle.observed.plan_revision} · {copy.cycleAssistantRevision} {diag.cycle.observed.assistant_revision} · {copy.cycleMemoryRevision} {diag.cycle.observed.memory_revision} · {copy.cycleWorkEpoch} {diag.cycle.observed.work_epoch}</span>
          </div>
        ) : null}
        {diag.next_step ? <div className="assistant-supervisor__row"><strong>{copy.cycleNextStep}</strong><span>{diag.next_step}</span></div> : null}
        {diag.pending_events?.length ? (
          <div className="assistant-supervisor__row"><strong>{copy.pendingEvents}</strong>
            <ul className="assistant-supervisor__list">{diag.pending_events.map((event) => (
              <li key={event.id}><code>{event.kind}</code><span>{event.session_id || event.request_id || ""}</span></li>
            ))}</ul>
          </div>
        ) : null}
        {diag.recent_decisions?.length ? (
          <div className="assistant-supervisor__row"><strong>{copy.recentDecisions}</strong>
            <ul className="assistant-supervisor__list">{diag.recent_decisions.map((decision) => (
              <li key={decision.id}><code>{decision.source || "auto"}</code><span>{decision.result || decision.winner || decision.id}</span></li>
            ))}</ul>
          </div>
        ) : null}
        {diag.recent_receipts?.length ? (
          <div className="assistant-supervisor__row"><strong>{copy.recentReceipts}</strong>
            <ul className="assistant-supervisor__list">{diag.recent_receipts.map((receipt) => (
              <li key={`${receipt.request_id}-${receipt.operation}`}><code>{receipt.operation}</code><span>{receipt.request_id}</span></li>
            ))}</ul>
          </div>
        ) : null}
        {diag.running_sessions?.length ? (
          <div className="assistant-supervisor__row"><strong>{copy.runningSessions}</strong>
            <ul className="assistant-supervisor__list">{diag.running_sessions.map((item) => (
              <li key={item.id}><code>{item.id}</code><span>{item.title || item.status}</span></li>
            ))}</ul>
          </div>
        ) : null}
        {diag.failed_sessions?.length ? (
          <div className="assistant-supervisor__row"><strong>{copy.failedSessions}</strong>
            <ul className="assistant-supervisor__list">{diag.failed_sessions.map((item) => (
              <li key={item.id}><code>{item.id}</code><span>{item.title || item.status}</span></li>
            ))}</ul>
          </div>
        ) : null}
        {diag.retry_due > 0 ? <div className="assistant-supervisor__row"><strong>{copy.retryDue}</strong><span>{diag.retry_due}</span></div> : null}
        {diag.diagnostics?.length ? (
          <div className="assistant-supervisor__row"><strong>{copy.diagnostics}</strong>
            <ul className="assistant-supervisor__list">{diag.diagnostics.map((item, index) => (
              <li key={`${item.operation}-${index}`}><code>{item.operation}</code><span>{item.message}</span></li>
            ))}</ul>
          </div>
        ) : null}
      </div>
    </details>
  );
}

function AssistantManager({ snapshot, tab, onTab, onClose, onRefresh, onDeleted, onRun, onOpenSession, diagnostics, managedSessions, onManagedChanged }: {
  snapshot: AssistantSnapshot;
  tab: ManageTab;
  onTab: (tab: ManageTab) => void;
  onClose: () => void;
  onRefresh: () => Promise<void>;
  onDeleted: () => Promise<void>;
  onRun: (routineID?: string) => Promise<void>;
  onOpenSession?: AssistantWorkspaceProps["onOpenSession"];
  diagnostics: AssistantDiagnostic[];
  managedSessions: AssistantManagedSession[];
  onManagedChanged: () => void;
}) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const { showToast } = useToast();
  const [busy, setBusy] = useState("");
  const tabs: Array<{ id: ManageTab; label: string; icon: typeof Bot }> = [
    { id: "overview", label: copy.overview, icon: Bot },
    { id: "sessions", label: copy.managedSessions, icon: FolderOpen },
    { id: "diagnostics", label: copy.diagnostics, icon: Activity },
    { id: "supervisor", label: copy.supervisorDiagnostic, icon: Activity },
    { id: "plan", label: copy.plan, icon: Check },
    { id: "routines", label: copy.routines, icon: CalendarClock },
    { id: "memory", label: copy.memory, icon: Brain },
    { id: "channels", label: copy.channels, icon: Megaphone },
	{ id: "proposals", label: copy.proposals, icon: Lightbulb },
    { id: "history", label: copy.history, icon: History },
    { id: "attention", label: copy.attention, icon: AlertCircle },
  ];
  const act = async (key: string, action: () => Promise<unknown>) => {
    setBusy(key);
    try { await action(); await onRefresh(); return true; }
    catch (cause) { showToast(cause instanceof Error ? cause.message : copy.error, "error"); return false; }
    finally { setBusy(""); }
  };
  const deleteAssistant = async () => {
    if (busy) return;
    setBusy("delete");
    const key = assistantMutationKey("delete", snapshot.assistant.id, snapshot.assistant.id, { revision: snapshot.revision });
    try {
      await runAssistantCASMutation(key, snapshot.revision, ({ requestId, expectedRevision }) => assistantDelete({
        assistantId: snapshot.assistant.id,
        requestId,
        expectedRevision,
      }));
      showToast(copy.deletedAssistant, "info");
      onClose();
      await onDeleted();
    } catch (cause) {
      showToast(cause instanceof Error ? cause.message : copy.error, "error");
    } finally {
      setBusy("");
    }
  };
  useEffect(() => {
    const close = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);
  return (
    <div className="assistant-manager-backdrop" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <aside className="assistant-manager" role="dialog" aria-modal="true" aria-label={copy.manage}>
        <header><h2>{copy.manage}</h2><button className="assistant-icon-button" type="button" aria-label={copy.close} onClick={onClose}><X size={18} /></button></header>
        <nav aria-label={copy.manage}>{tabs.map(({ id, label, icon: Icon }) => <button key={id} type="button" aria-current={tab === id ? "page" : undefined} className={tab === id ? "is-active" : ""} onClick={() => onTab(id)}><Icon size={15} />{label}{id === "diagnostics" && diagnostics.length > 0 && <span className="assistant-nav-count">{diagnostics.length}</span>}{id === "attention" && snapshot.attention.some((item) => attentionInboxAction(item, snapshot.runs.find((run) => run.id === item.run_id)) !== "none") && <span className="assistant-nav-dot" />}{id === "proposals" && snapshot.proposals?.some((item) => item.state === "pending") && <span className="assistant-nav-dot" />}</button>)}</nav>
        <div className="assistant-manager__content">
          {tab === "overview" && <OverviewEditor snapshot={snapshot} busy={busy} act={act} onDelete={deleteAssistant} />}
          {tab === "sessions" && <AssistantManagedSessionsSection assistant={snapshot.assistant} sessions={managedSessions} onOpenSession={onOpenSession} onChanged={onManagedChanged} copy={copy} />}
          {tab === "diagnostics" && <DiagnosticsEditor diagnostics={diagnostics} />}
          {tab === "supervisor" && <AssistantSupervisorDiagnosticPanel assistantID={snapshot.assistant.id} copy={copy} />}
          {tab === "plan" && <PlanView snapshot={snapshot} />}
          {tab === "routines" && <RoutineEditor snapshot={snapshot} busy={busy} act={act} onRun={onRun} />}
          {tab === "memory" && <MemoryEditor snapshot={snapshot} busy={busy} act={act} />}
          {tab === "channels" && <ChannelEditor snapshot={snapshot} busy={busy} act={act} />}
		  {tab === "proposals" && <ProposalInbox snapshot={snapshot} busy={busy} act={act} />}
          {tab === "history" && <RunHistory snapshot={snapshot} busy={busy} act={act} onRun={onRun} onAttention={() => onTab("attention")} onOpenSession={onOpenSession} />}
          {tab === "attention" && <AttentionInbox snapshot={snapshot} busy={busy} act={act} onOverview={() => onTab("overview")} />}
        </div>
      </aside>
    </div>
  );
}

function PlanView({ snapshot }: { snapshot: AssistantSnapshot }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const responsibilities = snapshot.plan?.responsibilities ?? [];
  const artifacts = snapshot.artifacts ?? [];
  const opportunities = snapshot.opportunities ?? [];
  const aliasById = new Map(responsibilities.map((item) => [item.id, responsibilityLabel(item)]));
  if (responsibilities.length === 0 && artifacts.length === 0 && opportunities.length === 0) {
    return <p className="assistant-empty-copy">{copy.planEmpty}</p>;
  }
  return (
    <div className="assistant-plan">
      {responsibilities.length > 0 && (
        <section>
          <h3>{copy.responsibility}</h3>
          <ul className="assistant-plan__list">
            {responsibilities.map((responsibility) => (
              <li key={responsibility.id} className={`assistant-responsibility assistant-responsibility--${responsibility.status}`}>
                <header><strong>{responsibilityLabel(responsibility)}</strong><span>{responsibilityStatusLabel(responsibility.status, locale)}</span></header>
                <p>{responsibility.objective}</p>
                {responsibility.done_criteria?.trim() && <p className="assistant-responsibility__meta">{copy.doneCriteria}：{responsibility.done_criteria}</p>}
                {responsibility.next_action?.trim() && <p className="assistant-responsibility__meta">{copy.nextAction}：{responsibility.next_action}</p>}
                {responsibility.depends_on?.length ? <p className="assistant-responsibility__meta">{copy.dependsOn}：{responsibility.depends_on.map((id) => aliasById.get(id) ?? id).join("、")}</p> : null}
                {responsibility.block_reason?.trim() && <p className="assistant-responsibility__block">{copy.blockReason}：{responsibility.block_reason}</p>}
              </li>
            ))}
          </ul>
        </section>
      )}
      {artifacts.length > 0 && (
        <section>
          <h3>{copy.artifacts}</h3>
          <ul className="assistant-plan__list">
            {artifacts.map((artifact) => (
              <li key={artifact.id} className="assistant-artifact">
                <strong>{artifact.title}</strong>
                {artifact.resp_id && <span className="assistant-artifact__resp">{aliasById.get(artifact.resp_id) ?? artifact.resp_id}</span>}
                {artifact.evidence?.trim() && <p>{artifact.evidence}</p>}
                {artifact.content?.trim() && <p>{artifact.content}</p>}
              </li>
            ))}
          </ul>
        </section>
      )}
      {opportunities.length > 0 && (
        <section>
          <h3>{copy.opportunities}</h3>
          <ul className="assistant-plan__list">
            {opportunities.map((opportunity) => (
              <li key={opportunity.id} className="assistant-opportunity">
                <strong>{aliasById.get(opportunity.resp_id ?? "") ?? opportunity.resp_id ?? copy.responsibility}</strong>
                {opportunity.reason?.trim() && <p>{opportunity.reason}</p>}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

interface ProposalDiff {
	label: string;
	before: string;
	after: string;
}

function proposalStateLabel(state: AssistantChangeProposal["state"], copy: ReturnType<typeof assistantCopy>): string {
	return {
		pending: copy.proposalPending,
		applied: copy.proposalApplied,
		rejected: copy.proposalRejected,
		superseded: copy.proposalSuperseded,
	}[state];
}

function proposalScheduleLabel(schedule: AssistantRoutine["schedule"], copy: ReturnType<typeof assistantCopy>): string {
	return scheduleLabel({ schedule } as AssistantRoutine, copy);
}

function proposalIntervalLabel(seconds: number, copy: ReturnType<typeof assistantCopy>): string {
	if (seconds >= 3600 && seconds % 3600 === 0) return `${seconds / 3600} ${copy.hour}`;
	return `${Math.round(seconds / 60)} ${copy.minute}`;
}

function proposalDiffs(proposal: AssistantChangeProposal, copy: ReturnType<typeof assistantCopy>): ProposalDiff[] {
	const rows: ProposalDiff[] = [];
	if (proposal.routine) {
		const { before, after } = proposal.routine;
		if (after.prompt !== undefined) rows.push({ label: copy.routinePrompt, before: before.prompt ?? "", after: after.prompt });
		if (after.schedule !== undefined) rows.push({
			label: copy.frequency,
			before: before.schedule ? proposalScheduleLabel(before.schedule, copy) : copy.unknownValue,
			after: proposalScheduleLabel(after.schedule, copy),
		});
		if (after.enabled !== undefined) rows.push({ label: copy.enabled, before: before.enabled ? copy.enabledState : copy.disabledState, after: after.enabled ? copy.enabledState : copy.disabledState });
	}
	if (proposal.channel) {
		const { before, after } = proposal.channel;
		if (after.collect_interval_seconds !== undefined) rows.push({
			label: copy.channelCollectInterval,
			before: before.collect_interval_seconds !== undefined ? proposalIntervalLabel(before.collect_interval_seconds, copy) : copy.unknownValue,
			after: proposalIntervalLabel(after.collect_interval_seconds, copy),
		});
		if (after.enabled !== undefined) rows.push({ label: copy.enabled, before: before.enabled ? copy.enabledState : copy.disabledState, after: after.enabled ? copy.enabledState : copy.disabledState });
	}
	return rows;
}

export function ProposalInbox({ snapshot, busy, act }: { snapshot: AssistantSnapshot; busy: string; act: Act }) {
	const { locale } = useI18n();
	const copy = assistantCopy(locale);
	const proposals = [...(snapshot.proposals ?? [])].sort((left, right) => right.updated_at.localeCompare(left.updated_at));
	const pending = proposals.filter((item) => item.state === "pending");
	const history = proposals.filter((item) => item.state !== "pending");
	const resolve = (proposal: AssistantChangeProposal, decision: "accept" | "reject") => {
		const intent = { decision, proposalRevision: proposal.revision };
		const key = assistantMutationKey(`proposal-${decision}`, snapshot.assistant.id, proposal.id, intent);
		return act(`proposal-${proposal.id}`, () => runAssistantCASMutation(key, proposal.revision, ({ requestId, expectedRevision }) => assistantResolveProposal({
			assistantId: snapshot.assistant.id,
			proposalId: proposal.id,
			requestId,
			expectedRevision,
			decision,
			resolution: decision === "accept" ? copy.proposalAcceptedNote : copy.proposalRejectedNote,
		})));
	};
	const targetLabel = (proposal: AssistantChangeProposal) => {
		if (proposal.target_kind === "routine") return snapshot.routines.find((item) => item.id === proposal.target_id)?.title ?? proposal.target_id;
		return snapshot.channels.find((item) => item.id === proposal.target_id)?.name ?? proposal.target_id;
	};
	const renderProposal = (proposal: AssistantChangeProposal) => (
		<article key={proposal.id} className={`assistant-proposal assistant-proposal--${proposal.state}`}>
			<header>
				<div><strong>{proposal.summary}</strong><span>{copy.proposalTarget}：{targetLabel(proposal)}</span></div>
				<span className="assistant-proposal__state">{proposalStateLabel(proposal.state, copy)}</span>
			</header>
			<p className="assistant-proposal__reason"><span>{copy.proposalReason}</span>{proposal.reason}</p>
			<div className="assistant-proposal__diff" aria-label={copy.proposalChanges}>
				{proposalDiffs(proposal, copy).map((row) => <div key={row.label} className="assistant-proposal__diff-row"><strong>{row.label}</strong><span>{row.before}</span><ChevronRight size={14} aria-hidden="true" /><span>{row.after}</span></div>)}
			</div>
			<div className="assistant-proposal__evidence"><strong>{copy.proposalEvidence}</strong><ul>{proposal.evidence.map((item, index) => <li key={`${proposal.id}-evidence-${index}`}>{item}</li>)}</ul></div>
			{proposal.state === "pending" ? <footer>
				<button className="assistant-button assistant-button--accent" type="button" disabled={Boolean(busy)} onClick={() => void resolve(proposal, "accept")}><Check size={14} />{copy.acceptProposal}</button>
				<button className="assistant-button" type="button" disabled={Boolean(busy)} onClick={() => void resolve(proposal, "reject")}><X size={14} />{copy.rejectProposal}</button>
			</footer> : <p className="assistant-proposal__resolution"><span>{copy.proposalResolution}</span>{proposal.resolution}</p>}
		</article>
	);
	if (proposals.length === 0) return <div className="assistant-proposals"><p className="assistant-empty-copy">{copy.noProposals}</p><p className="assistant-proposals__hint">{copy.proposalIntro}</p></div>;
	return <div className="assistant-proposals">
		<p className="assistant-proposals__hint">{copy.proposalIntro}</p>
		<section aria-label={copy.pendingProposals}><h3>{copy.pendingProposals}<span>{pending.length}</span></h3>{pending.length > 0 ? pending.map(renderProposal) : <p className="assistant-empty-copy">{copy.noPendingProposals}</p>}</section>
		{history.length > 0 && <section aria-label={copy.proposalHistory}><h3>{copy.proposalHistory}</h3>{history.map(renderProposal)}</section>}
	</div>;
}

export function DiagnosticsEditor({ diagnostics }: { diagnostics: AssistantDiagnostic[] }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const rows = useMemo(() => sortedDiagnostics(diagnostics), [diagnostics]);
  if (rows.length === 0) {
    return <p className="assistant-empty-copy">{copy.noDiagnostics}</p>;
  }
  return (
    <div className="assistant-diagnostics">
      {rows.map((item, index) => {
        const when = formatDiagnosticTime(item.at, locale);
        return (
          <article key={`${item.at}-${index}`} className="assistant-diagnostic-entry">
            <header>
              <span className="assistant-diagnostic-entry__category">{diagnosticCategoryLabel(item.category, copy)}</span>
              <code className="assistant-diagnostic-entry__operation">{item.operation}</code>
              <time dateTime={item.at}>{when || copy.diagnosticTimeUnknown}</time>
            </header>
            <p>{item.message}</p>
          </article>
        );
      })}
    </div>
  );
}

type Act = (key: string, action: () => Promise<unknown>) => Promise<boolean>;

const ALWAYS_ASK_POLICY: ReadonlySet<keyof AssistantPolicy> = new Set(["delete", "payment", "secrets", "private_data"]);

export function OverviewEditor({ snapshot, busy, act, onDelete }: { snapshot: AssistantSnapshot; busy: string; act: Act; onDelete: () => Promise<void> }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const [name, setName] = useState(snapshot.assistant.name);
  const [mission, setMission] = useState(snapshot.assistant.mission);
  const [scope, setScope] = useState(snapshot.assistant.scope);
  const [workspace, setWorkspace] = useState(snapshot.assistant.workspace_root ?? "");
  const [policy, setPolicy] = useState<AssistantPolicy>(snapshot.assistant.policy);
  const [confirmDelete, setConfirmDelete] = useState(false);
  useEffect(() => {
    setName(snapshot.assistant.name);
    setMission(snapshot.assistant.mission);
    setScope(snapshot.assistant.scope);
    setWorkspace(snapshot.assistant.workspace_root ?? "");
    setPolicy(snapshot.assistant.policy);
    setConfirmDelete(false);
  }, [snapshot.assistant.id, snapshot.assistant.revision]);
  const policyRows: Array<{ key: keyof AssistantPolicy; label: string }> = [
    { key: "local_write", label: copy.policyLocalWrite },
    { key: "network", label: copy.policyNetwork },
    { key: "publish", label: copy.policyPublish },
    { key: "delete", label: copy.policyDelete },
    { key: "payment", label: copy.policyPayment },
    { key: "secrets", label: copy.policySecrets },
    { key: "private_data", label: copy.policyPrivateData },
  ];
  const accessLabel = (field: keyof AssistantPolicy, value: AssistantAccess): string => {
    if (value === "allow") return ALWAYS_ASK_POLICY.has(field) ? copy.accessAllowAsk : copy.accessAllow;
    if (value === "deny") return copy.accessDeny;
    return copy.accessApprove;
  };
  const setAccess = (field: keyof AssistantPolicy, value: AssistantAccess) => setPolicy((current) => ({ ...current, [field]: value }));
  const save = () => {
    const intent = { name: name.trim(), mission: mission.trim(), scope, workspace_root: scope === "workspace" ? workspace.trim() : undefined, policy };
    const key = assistantMutationKey("update", snapshot.assistant.id, snapshot.assistant.id, intent);
    return act("overview", () => runAssistantCASMutation(key, snapshot.assistant.revision, ({ requestId, expectedRevision }) => assistantUpdate({
      requestId,
      expectedRevision,
      assistant: { ...snapshot.assistant, ...intent },
    })));
  };
  const lifecycle = snapshot.assistant.lifecycle === "active" ? "paused" : "active";
  const changeLifecycle = () => {
    const key = assistantMutationKey("lifecycle", snapshot.assistant.id, snapshot.assistant.id, { lifecycle });
    return act("lifecycle", () => runAssistantCASMutation(key, snapshot.assistant.revision, ({ requestId, expectedRevision }) => assistantUpdate({
      requestId,
      expectedRevision,
      assistant: { ...snapshot.assistant, lifecycle },
    })));
  };
  return (
    <form className="assistant-form" onSubmit={(event) => { event.preventDefault(); void save(); }}>
      <label>{copy.name}<input value={name} onChange={(event) => setName(event.target.value)} required /></label>
      <label>{copy.mission}<textarea value={mission} onChange={(event) => setMission(event.target.value)} rows={7} required /></label>
      <label>{copy.scope}<select value={scope} onChange={(event) => {
        const next = event.target.value as AssistantRecord["scope"];
        setScope(next);
        if (next === "global") setWorkspace("");
      }}><option value="global">{copy.scopeGlobal}</option><option value="workspace">{copy.scopeWorkspace}</option></select></label>
      {scope === "workspace" && <label>{copy.workspace}<input value={workspace} onChange={(event) => setWorkspace(event.target.value)} required /></label>}
      <p className="assistant-form__hint"><AlertCircle size={13} />{copy.workspaceFreezeHint}</p>
      <section className="assistant-policy" aria-label={copy.permissionTitle}>
        <h3>{copy.permissionTitle}</h3>
        {policyRows.map((row) => (
          <div className="assistant-policy__row" key={row.key}>
            <span>{row.label}</span>
            <div className="assistant-policy__options" role="group" aria-label={row.label}>
              {(["deny", "approve", "allow"] as const).map((value) => (
                <button
                  key={value}
                  type="button"
                  className={`assistant-policy__option${policy[row.key] === value ? " is-active" : ""}${value === "allow" && ALWAYS_ASK_POLICY.has(row.key) ? " is-always-ask" : ""}`}
                  aria-pressed={policy[row.key] === value}
                  onClick={() => setAccess(row.key, value)}
                >
                  {accessLabel(row.key, value)}
                </button>
              ))}
            </div>
          </div>
        ))}
        <p className="assistant-form__hint"><AlertCircle size={13} />{copy.policyAskNote}</p>
        <p className="assistant-form__hint"><Clock3 size={13} />{copy.policyFreezeHint}</p>
      </section>
      <div className="assistant-form__actions">
        <button className="assistant-button" type="button" disabled={Boolean(busy)} onClick={() => void changeLifecycle()}>
          {lifecycle === "paused" ? <Pause size={14} /> : <Play size={14} />}{lifecycle === "paused" ? copy.pause : copy.resume}
        </button>
        <button className="assistant-button assistant-button--accent" type="submit" disabled={Boolean(busy) || !name.trim() || !mission.trim() || (scope === "workspace" && !workspace.trim())}>{copy.save}</button>
      </div>
      <section className="assistant-danger-zone" aria-label={copy.deleteAssistantTitle}>
        <div><strong>{copy.deleteAssistantTitle}</strong><p>{copy.deleteAssistantBody}</p></div>
        {confirmDelete ? (
          <div className="assistant-danger-zone__confirm" role="alert">
            <button className="assistant-button" type="button" disabled={Boolean(busy)} onClick={() => setConfirmDelete(false)}>{copy.cancel}</button>
            <button className="assistant-button assistant-button--danger" type="button" disabled={Boolean(busy)} onClick={() => void onDelete()}><Trash2 size={14} />{copy.confirmDeleteAssistant}</button>
          </div>
        ) : (
          <button className="assistant-button assistant-button--danger-quiet" type="button" disabled={Boolean(busy)} onClick={() => setConfirmDelete(true)}><Trash2 size={14} />{copy.deleteAssistant}</button>
        )}
      </section>
    </form>
  );
}

function RoutineEditor({ snapshot, busy, act, onRun }: { snapshot: AssistantSnapshot; busy: string; act: Act; onRun: (id?: string) => Promise<void> }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const [selected, setSelected] = useState(snapshot.routines[0]?.id ?? "");
  const source = snapshot.routines.find((item) => item.id === selected) ?? snapshot.routines[0];
  const [draft, setDraft] = useState<AssistantRoutine | null>(source ?? null);
  useEffect(() => setDraft(source ? { ...source, schedule: { ...source.schedule } } : null), [source?.id, source?.revision]);
  if (!draft) {
    return (
      <div className="assistant-form">
        <p className="assistant-empty-copy">{copy.noRoutines}</p>
        <div className="assistant-form__actions">
          <button className="assistant-button assistant-button--accent" type="button" onClick={() => setDraft({
            id: assistantEntityID("routine"),
            assistant_id: snapshot.assistant.id,
            title: "",
            prompt: "",
            schedule: { kind: "manual", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" },
            enabled: true,
            catch_up: "coalesce_latest",
            revision: 0,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          })}><Plus size={14} />{copy.addRoutine}</button>
        </div>
      </div>
    );
  }
  const setSchedule = (kind: AssistantScheduleKind, patch: Partial<AssistantRoutine["schedule"]> = {}) => setDraft((current) => current ? { ...current, schedule: { kind, timezone: current.schedule.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", ...patch } } : current);
  const save = () => {
    const intent = { title: draft.title.trim(), prompt: draft.prompt.trim(), schedule: draft.schedule, enabled: draft.enabled, catch_up: draft.catch_up };
    const key = assistantMutationKey("routine", snapshot.assistant.id, draft.id, intent);
    return act(`routine-${draft.id}`, () => runAssistantCASMutation(key, draft.revision, ({ requestId, expectedRevision }) => assistantPutRoutine({ requestId, expectedRevision, routine: { ...draft, title: intent.title, prompt: intent.prompt } })));
  };
  return (
    <div className="assistant-form">
      {snapshot.routines.length > 1 && <label>{copy.routines}<select value={draft.id} onChange={(event) => setSelected(event.target.value)}>{snapshot.routines.map((routine) => <option key={routine.id} value={routine.id}>{routine.title}</option>)}</select></label>}
      <label>{copy.routineTitle}<input value={draft.title} onChange={(event) => setDraft({ ...draft, title: event.target.value })} /></label>
      <label>{copy.routinePrompt}<textarea rows={5} value={draft.prompt} onChange={(event) => setDraft({ ...draft, prompt: event.target.value })} /></label>
      <label>{copy.frequency}<select value={draft.schedule.kind} onChange={(event) => setSchedule(event.target.value as AssistantScheduleKind, event.target.value === "interval" ? { interval_seconds: 4 * 3600 } : { at: "18:00" })}><option value="manual">{copy.manual}</option><option value="interval">{copy.interval}</option><option value="daily">{copy.daily}</option><option value="weekly">{copy.weekly}</option></select></label>
      {draft.schedule.kind === "interval" && <label>{copy.hour}<input type="number" min={1} max={720} value={Math.max(1, Math.round((draft.schedule.interval_seconds ?? 3600) / 3600))} onChange={(event) => setSchedule("interval", { interval_seconds: Math.max(1, Number(event.target.value)) * 3600 })} /></label>}
      {draft.schedule.kind !== "manual" && draft.schedule.kind !== "interval" && <label>{copy.at}<input type="time" value={draft.schedule.at || "18:00"} onChange={(event) => setSchedule(draft.schedule.kind, { at: event.target.value })} /></label>}
      <label>{copy.timezone}<input value={draft.schedule.timezone || "UTC"} onChange={(event) => setDraft({ ...draft, schedule: { ...draft.schedule, timezone: event.target.value } })} /></label>
      <label className="assistant-check"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} />{copy.enabled}</label>
      <p className="assistant-form__hint"><Clock3 size={13} />{scheduleLabel(draft, copy)}</p>
      <div className="assistant-form__actions">
        <button className="assistant-button" type="button" disabled={Boolean(busy)} onClick={() => void onRun(draft.id)}><Play size={14} />{copy.runNow}</button>
        <button className="assistant-button assistant-button--accent" type="button" disabled={Boolean(busy) || !draft.title.trim() || !draft.prompt.trim()} onClick={() => void save()}>{copy.save}</button>
      </div>
    </div>
  );
}

function MemoryEditor({ snapshot, busy, act }: { snapshot: AssistantSnapshot; busy: string; act: Act }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const [kind, setKind] = useState<AssistantMemoryKind>("facts");
  const [body, setBody] = useState("");
  const addDraft = useRef<{ signature: string; id: string; at: string } | null>(null);
  const add = async () => {
    const signature = `${kind}\u0000${body.trim()}`;
    if (!addDraft.current || addDraft.current.signature !== signature) {
      addDraft.current = { signature, id: assistantEntityID("memory"), at: new Date().toISOString() };
    }
    const { id, at } = addDraft.current;
    const patch = { upsert: [{ id, kind, body: body.trim(), locked: kind === "charter", revision: 0, created_at: at, updated_at: at }] };
    const key = assistantMutationKey("memory-upsert", snapshot.assistant.id, id, patch);
    const saved = await act("memory-add", () => runAssistantCASMutation(key, snapshot.memory.revision, ({ requestId, expectedRevision }) => assistantApplyMemory({ assistantId: snapshot.assistant.id, requestId, expectedRevision, patch })));
    if (saved) {
      addDraft.current = null;
      setBody("");
    }
  };
  const remove = (id: string) => {
    const patch = { delete: [id] };
    const key = assistantMutationKey("memory-delete", snapshot.assistant.id, id, patch);
    return act(`memory-delete-${id}`, () => runAssistantCASMutation(key, snapshot.memory.revision, ({ requestId, expectedRevision }) => assistantApplyMemory({ assistantId: snapshot.assistant.id, requestId, expectedRevision, patch })));
  };
  return (
    <div className="assistant-form">
      <div className="assistant-memory-list">
        {snapshot.memory.items.length === 0 ? <p className="assistant-empty-copy">{copy.noMemory}</p> : snapshot.memory.items.map((item) => (
          <article key={item.id} className="assistant-memory-item"><span>{copy[item.kind === "open_loops" ? "openLoops" : item.kind]}</span><p>{item.body}</p><button type="button" aria-label={copy.cancel} disabled={Boolean(busy) || item.locked} onClick={() => void remove(item.id)}><Trash2 size={14} /></button></article>
        ))}
      </div>
      <label>{copy.memoryKind}<select value={kind} onChange={(event) => setKind(event.target.value as AssistantMemoryKind)}><option value="charter">{copy.charter}</option><option value="facts">{copy.facts}</option><option value="strategy">{copy.strategy}</option><option value="open_loops">{copy.openLoops}</option><option value="metrics">{copy.metrics}</option></select></label>
      <label>{copy.memoryBody}<textarea rows={4} value={body} onChange={(event) => setBody(event.target.value)} /></label>
      <div className="assistant-form__actions"><button className="assistant-button assistant-button--accent" type="button" disabled={Boolean(busy) || !body.trim()} onClick={() => void add()}><Plus size={14} />{copy.addMemory}</button></div>
    </div>
  );
}

function emptyChannel(assistantID: string): AssistantChannel {
  const now = new Date().toISOString();
  return { id: assistantEntityID("channel"), assistant_id: assistantID, name: "Discourse", kind: "discourse", base_url: "", username: "", credential_key: "", category_id: 0, collect_interval_seconds: 3600, enabled: true, revision: 0, created_at: now, updated_at: now };
}

function ChannelEditor({ snapshot, busy, act }: { snapshot: AssistantSnapshot; busy: string; act: Act }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const [selected, setSelected] = useState(snapshot.channels[0]?.id ?? "");
  const [creating, setCreating] = useState(snapshot.channels.length === 0);
  const source = creating ? undefined : (snapshot.channels.find((item) => item.id === selected) ?? snapshot.channels[0]);
  const [draft, setDraft] = useState<AssistantChannel>(() => source ? { ...source } : emptyChannel(snapshot.assistant.id));
  const [apiKey, setAPIKey] = useState("");
  useEffect(() => { setDraft(source ? { ...source } : emptyChannel(snapshot.assistant.id)); setAPIKey(""); }, [source?.id, source?.revision, snapshot.assistant.id, creating]);
  const save = async () => {
    const intent = { ...draft, base_url: draft.base_url.trim().replace(/\/+$/, ""), username: draft.username.trim(), name: draft.name.trim(), category_id: Number(draft.category_id || 0), collect_interval_seconds: Math.max(1, Math.round(draft.collect_interval_seconds / 3600)) * 3600 };
    const key = assistantMutationKey("channel", snapshot.assistant.id, draft.id, { ...intent, apiKeySet: Boolean(apiKey.trim()) });
    const saved = await act(`channel-${draft.id}`, () => runAssistantCASMutation(key, draft.revision, ({ requestId, expectedRevision }) => assistantPutChannel({ requestId, expectedRevision, channel: intent, apiKey: apiKey.trim() || undefined })));
    if (saved) { setSelected(draft.id); setCreating(false); setAPIKey(""); }
    return saved;
  };
  const actions = snapshot.channel_actions.filter((item) => item.channel_id === draft.id).slice().reverse().slice(0, 10);
  const metrics = snapshot.channel_metrics.filter((item) => item.channel_id === draft.id).slice().reverse().slice(0, 10);
  return (
    <form className="assistant-form" onSubmit={(event) => { event.preventDefault(); void save(); }}>
      {snapshot.channels.length > 0 && <label>{copy.channels}<select value={source?.id ?? ""} onChange={(event) => { setCreating(false); setSelected(event.target.value); }}>{snapshot.channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}
      {snapshot.channels.length > 0 && !creating && <button className="assistant-text-action" type="button" onClick={() => setCreating(true)}><Plus size={13} />{copy.addChannel}</button>}
      {snapshot.channels.length === 0 && <p className="assistant-empty-copy">{copy.noChannels}</p>}
      <label>{copy.channelName}<input value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} required /></label>
      <label>{copy.channelBaseURL}<input type="url" value={draft.base_url} onChange={(event) => setDraft({ ...draft, base_url: event.target.value })} placeholder="https://community.example.com" required /></label>
      <label>{copy.channelUsername}<input value={draft.username} onChange={(event) => setDraft({ ...draft, username: event.target.value })} required /></label>
      <label>{copy.channelAPIKey}<input type="password" autoComplete="new-password" value={apiKey} onChange={(event) => setAPIKey(event.target.value)} required={draft.revision === 0} /><small>{copy.channelAPIKeyHint}</small></label>
      <label>{copy.channelCategory}<input type="number" min={0} value={draft.category_id ?? 0} onChange={(event) => setDraft({ ...draft, category_id: Number(event.target.value) })} /></label>
      <label>{copy.channelCollectHours}<input type="number" min={1} max={168} value={Math.max(1, Math.round(draft.collect_interval_seconds / 3600))} onChange={(event) => setDraft({ ...draft, collect_interval_seconds: Number(event.target.value) * 3600 })} /></label>
      <label className="assistant-check"><input type="checkbox" checked={draft.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} />{copy.enabled}</label>
      <div className="assistant-form__actions"><button className="assistant-button assistant-button--accent" type="submit" disabled={Boolean(busy) || !draft.name.trim() || !draft.base_url.trim() || !draft.username.trim() || (draft.revision === 0 && !apiKey.trim())}>{draft.revision === 0 ? copy.addChannel : copy.save}</button></div>
      {actions.length > 0 && <section className="assistant-channel-ledger"><h3>{copy.channelActions}</h3>{actions.map((item) => <article key={item.id}><strong>{item.title || item.kind}</strong><span>{item.state}</span>{item.url && <a href={item.url} target="_blank" rel="noreferrer">{item.url}</a>}{item.error && <small>{item.error}</small>}</article>)}</section>}
      {metrics.length > 0 && <section className="assistant-channel-ledger"><h3>{copy.channelMetrics}</h3>{metrics.map((item) => <article key={item.id}><strong>Topic {item.topic_id}</strong><span>👁 {item.views} (+{item.views_delta}) · ♥ {item.likes} (+{item.likes_delta}) · ↩ {item.replies} (+{item.reply_delta})</span><time>{new Date(item.collected_at).toLocaleString()}</time></article>)}</section>}
    </form>
  );
}

// AssistantJobRow renders one dispatch job as read-only history. The main
// execution view no longer operates RunnerJobs (no claim/retry/stop): retry and
// stop buttons render only when the caller explicitly supplies handlers, which
// the main timeline never does.
export function AssistantJobRow({ job, busy, onRetry, onCancel, owner, onOpenSession }: { job: AssistantRunnerJob; busy: string; onRetry?: (id: string) => Promise<void>; onCancel?: (id: string) => Promise<void>; owner: AssistantRecord; onOpenSession?: (target: AssistantSessionTarget) => void }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const target = assistantJobSessionTarget(job, owner);
  const name = target && onOpenSession ? (
    <button
      type="button"
      className="assistant-jobs__name assistant-jobs__name--link"
      title={copy.openSession}
      aria-label={`${job.name}，${copy.openSession}`}
      onClick={() => onOpenSession(target)}
    >
      {job.name}
      <ExternalLink size={11} aria-hidden="true" />
    </button>
  ) : (
    <span className="assistant-jobs__name">{job.name}</span>
  );
  return (
    <li key={job.id}>
      <span className={`assistant-run-state assistant-run-state--${job.state}`}>{jobStateLabel(job.state, locale)}</span>
      {name}
      {job.error?.message && job.error.message.trim() !== (job.summary ?? "").trim() ? <p className="assistant-event__error">{job.error.message}</p> : null}
      {onRetry && (job.state === "failed" || job.state === "cancelled" || job.state === "waiting_attention") ? (
        <button type="button" className="assistant-text-action" disabled={Boolean(busy)} onClick={() => void onRetry(job.id)}><RefreshCw size={13} />{copy.retryJob}</button>
      ) : null}
      {onCancel && (job.state === "queued" || job.state === "running" || job.state === "retry_wait") ? (
        <button type="button" className="assistant-text-action" disabled={Boolean(busy)} onClick={() => void onCancel(job.id)}><X size={13} />{copy.stopJob}</button>
      ) : null}
    </li>
  );
}

export function RunHistory({ snapshot, busy, act, onRun, onAttention, onOpenSession }: { snapshot: AssistantSnapshot; busy: string; act: Act; onRun: (id?: string) => Promise<void>; onAttention: () => void; onOpenSession?: AssistantWorkspaceProps["onOpenSession"] }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  if (snapshot.runs.length === 0) return <p className="assistant-empty-copy">{copy.noHistory}</p>;
  const cancel = (runID: string) => {
    const key = assistantIntentKey("cancel", snapshot.assistant.id, runID);
    return act(`cancel-${runID}`, () => runAssistantMutation(key, (requestId) => assistantCancel({ runId: runID, requestId, reason: "用户从助手工作区停止" })));
  };
  return <div className="assistant-history">{[...snapshot.runs].sort((a, b) => b.created_at.localeCompare(a.created_at)).map((run) => {
    // A waiting run must always stay actionable. If its attention was already
    // resolved or is otherwise missing, the inbox would be an empty dead end —
    // fall back to a retry that starts a fresh executable run.
    const action = runHistoryAction(run.state) === "attention" && !runHasActionableAttention(run, snapshot.attention) ? "rerun" : runHistoryAction(run.state);
    const sessionTarget = assistantRunSessionTarget(run, snapshot.assistant);
    const directInput = !run.routine_id ? run.prompt?.trim() : "";
    const result = run.summary || run.error?.message || snapshot.routines.find((item) => item.id === run.routine_id)?.title;
    return <article key={run.id} className="assistant-history-item"><div><strong>{runStateLabel(run.state, locale)}</strong><time>{formatTimelineTime(new Date(run.started_at || run.created_at), locale)}</time></div>{directInput && <div className="assistant-history-item__prompt"><span>{copy.youSaid}</span><p>{directInput}</p></div>}{result && <div className="assistant-history-item__result"><AssistantMarkdown text={result} /></div>}{run.state === "retry_wait" && <span className="assistant-history-item__hint">{copy.waitingRetry}</span>}<div>{sessionTarget && onOpenSession && <button className="assistant-text-action" type="button" onClick={() => onOpenSession(sessionTarget)}><ExternalLink size={13} />{copy.openSession}</button>}{action === "rerun" && <button className="assistant-text-action" type="button" disabled={Boolean(busy)} onClick={() => void onRun(run.routine_id)}><RefreshCw size={13} />{copy.rerun}</button>}{action === "cancel" && <button className="assistant-text-action" type="button" disabled={Boolean(busy)} onClick={() => void cancel(run.id)}><X size={13} />{copy.stopRun}</button>}{action === "attention" && <button className="assistant-text-action" type="button" onClick={onAttention}><AlertCircle size={13} />{copy.handleAttention}</button>}</div></article>;
  })}</div>;
}

export function AttentionInbox({ snapshot, busy, act, onOverview }: { snapshot: AssistantSnapshot; busy: string; act: Act; onOverview: () => void }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const items = snapshot.attention.map((item) => {
    const run = snapshot.runs.find((candidate) => candidate.id === item.run_id);
    return { item, run, mode: attentionInboxAction(item, run) };
  }).filter(({ mode }) => mode !== "none");
  if (items.length === 0) return <p className="assistant-empty-copy">{copy.noAttention}</p>;
  const approve = ({ item }: (typeof items)[number]) => act(`approve-${item.id}`, async () => {
    const resolution = attentionResolution(item.action, answers[item.id] ?? "", copy.resolveNote);
    await runAssistantApproval({
      assistantID: snapshot.assistant.id,
      attentionID: item.id,
      runID: item.run_id,
      resolve: (requestID) => assistantResolveAttention({ assistantId: snapshot.assistant.id, attentionId: item.id, requestId: requestID, expectedRevision: item.revision, state: "approved", resolution }),
      resume: (requestID) => assistantResume({ runId: item.run_id!, requestId: requestID }),
    });
  });
  const reject = ({ item }: (typeof items)[number]) => act(`reject-${item.id}`, async () => {
    const resolution = attentionRejectResolution(item.action, copy.resolveNote, copy.rejectNote);
    await runAssistantRejection({
      assistantID: snapshot.assistant.id,
      attentionID: item.id,
      reject: (requestID) => assistantResolveAttention({ assistantId: snapshot.assistant.id, attentionId: item.id, requestId: requestID, expectedRevision: item.revision, state: "rejected", resolution }),
    });
  });
  const resume = ({ item }: (typeof items)[number]) => act(`resume-${item.id}`, () => runAssistantResume({
    assistantID: snapshot.assistant.id,
    attentionID: item.id,
    runID: item.run_id!,
    resume: (requestID) => assistantResume({ runId: item.run_id!, requestId: requestID }),
    completeKeys: item.action === "verify_run_outcome" && item.resolution === "retry_acknowledged"
      ? [assistantOutcomeKey(snapshot.assistant.id, item.id, item.resolution)]
      : undefined,
  }));
  const resolveOutcome = (entry: (typeof items)[number], resolution: "retry_acknowledged" | "mark_succeeded" | "mark_failed") => {
    const { item } = entry;
    return act(`outcome-${resolution}-${item.id}`, () => runAssistantOutcome({
      assistantID: snapshot.assistant.id,
      attentionID: item.id,
      runID: item.run_id,
      resolution,
      resolve: (requestID) => assistantResolveAttention({ assistantId: snapshot.assistant.id, attentionId: item.id, requestId: requestID, expectedRevision: item.revision, state: "approved", resolution }),
      resume: item.run_id ? (requestID) => assistantResume({ runId: item.run_id!, requestId: requestID }) : undefined,
    }));
  };
  const cancelRebind = ({ item }: (typeof items)[number]) => act(`cancel-rebind-${item.id}`, async () => {
    if (!item.run_id) return;
    const key = assistantIntentKey("attention-cancel", snapshot.assistant.id, item.run_id);
    await runAssistantMutation(key, (requestId) => assistantCancel({ runId: item.run_id!, requestId, reason: "项目路径需要重新绑定" }));
  });
  return <div className="assistant-history">{items.map((entry) => {
    const { item, mode } = entry;
    const answer = answers[item.id] ?? "";
    const answerMissing = mode === "answer" && !answer.trim();
    return <article key={item.id} className="assistant-attention-item"><strong>{item.action}</strong><p>{item.summary}</p>{mode === "rebind" && <p className="assistant-attention-item__warning">{copy.rebindWarning}</p>}{mode === "answer" && <label className="assistant-attention-item__answer">{copy.answer}<textarea rows={3} value={answer} onChange={(event) => setAnswers((current) => ({ ...current, [item.id]: event.target.value }))} placeholder={copy.answerPlaceholder} /></label>}<div>{mode === "rebind" ? <><button className="assistant-button assistant-button--accent" type="button" onClick={onOverview}>{copy.changeProject}</button><button className="assistant-button" type="button" disabled={Boolean(busy) || !item.run_id} onClick={() => void cancelRebind(entry)}><X size={14} />{copy.cancelOldRun}</button></> : mode === "verify" ? <><button className="assistant-button assistant-button--accent" type="button" disabled={Boolean(busy) || !item.run_id} onClick={() => void resolveOutcome(entry, "retry_acknowledged")}><RefreshCw size={14} />{copy.confirmRetry}</button><button className="assistant-button" type="button" disabled={Boolean(busy)} onClick={() => void resolveOutcome(entry, "mark_succeeded")}><Check size={14} />{copy.markSucceeded}</button><button className="assistant-button" type="button" disabled={Boolean(busy)} onClick={() => void resolveOutcome(entry, "mark_failed")}><X size={14} />{copy.markFailed}</button></> : mode === "continue" ? <button className="assistant-button assistant-button--accent" type="button" disabled={Boolean(busy) || !item.run_id} onClick={() => void resume(entry)}><Play size={14} />{copy.continueRun}</button> : <><button className="assistant-button assistant-button--accent" type="button" disabled={Boolean(busy) || answerMissing} onClick={() => void approve(entry)}><Check size={14} />{mode === "answer" ? copy.answer : copy.approve}</button><button className="assistant-button" type="button" disabled={Boolean(busy)} onClick={() => void reject(entry)}><X size={14} />{copy.reject}</button></>}</div></article>;
  })}</div>;
}

export function CreateAssistantDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (id: string) => void }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const { showToast } = useToast();
  const nameRef = useRef<HTMLInputElement>(null);
  const [templateID, setTemplateID] = useState<AssistantTemplateID | null>(null);
  const [name, setName] = useState("");
  const [mission, setMission] = useState("");
  const [workspace, setWorkspace] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [learnFirst, setLearnFirst] = useState(true);
  const [routineTitle, setRoutineTitle] = useState("");
  const [routinePrompt, setRoutinePrompt] = useState("");
  const [routineKind, setRoutineKind] = useState<"manual" | "daily" | "interval">("daily");
  const [routineAt, setRoutineAt] = useState("09:00");
  const [routineHours, setRoutineHours] = useState(4);
  const [identity] = useState(() => ({
    assistantID: assistantEntityID("assistant"),
    routineID: assistantEntityID("routine"),
    createdAt: new Date().toISOString(),
  }));
  const [picking, setPicking] = useState(false);
  const workspaceOp = useRef(false);
  const [chooserOpen, setChooserOpen] = useState(false);
  const [chooserLoading, setChooserLoading] = useState(false);
  const [chooserError, setChooserError] = useState("");
  const [workspaceChoices, setWorkspaceChoices] = useState<Array<{ root: string; label: string }>>([]);
  const chooserGeneration = useRef(0);
  useEffect(() => { nameRef.current?.focus(); }, []);
  useEffect(() => {
    const close = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);

  const template = templateID ? assistantTemplate(templateID, locale) : null;
  const templates = assistantTemplateContent(locale);
  const templateLabels: Record<AssistantTemplateID, string> = {
    code: copy.templateCode,
    general: copy.templateGeneral,
    promo: copy.templatePromo,
  };
  const templateDescriptions: Record<AssistantTemplateID, string> = {
    code: copy.templateCodeDesc,
    general: copy.templateGeneralDesc,
    promo: copy.templatePromoDesc,
  };

  const pickTemplate = (id: AssistantTemplateID) => {
    const chosen = assistantTemplate(id, locale);
    if (!chosen.available) return;
    setTemplateID(id);
    setName(chosen.defaultName);
    setMission(chosen.mission);
    setConfirmed(false);
    setLearnFirst(true);
  };

  // "从文件夹新建": the native folder picker creates or picks any directory;
  // an empty result means the user canceled, so the current value is kept.
  const pickWorkspace = async () => {
    if (workspaceOp.current || chooserOpen) return;
    workspaceOp.current = true;
    setPicking(true);
    try {
      const picked = await assistantPickWorkspace(workspace);
      if (picked && picked.trim()) setWorkspace(picked.trim());
    } catch (cause) {
      showToast(cause instanceof Error ? cause.message : copy.error, "error");
    } finally {
      workspaceOp.current = false;
      setPicking(false);
    }
  };

  // "选择已有工作区": pick a registered project workspace from ListProjectTree.
  // The generation guard drops late responses after the chooser closes or a
  // newer load starts, so they can never overwrite a later selection state.
  const loadWorkspaces = async () => {
    const gen = ++chooserGeneration.current;
    setChooserLoading(true);
    setChooserError("");
    try {
      const tree = await assistantListWorkspaces();
      if (chooserGeneration.current !== gen) return;
      setWorkspaceChoices(
        tree
          .filter((node): node is ProjectNode & { root: string } => node.kind === "project" && Boolean(node.root))
          .map((node) => ({ root: node.root, label: node.label || node.root })),
      );
    } catch (cause) {
      if (chooserGeneration.current !== gen) return;
      setChooserError(cause instanceof Error ? cause.message : copy.error);
    } finally {
      if (chooserGeneration.current === gen) setChooserLoading(false);
    }
  };

  const openChooser = () => {
    if (workspaceOp.current) return;
    setChooserOpen(true);
    setWorkspaceChoices([]);
    void loadWorkspaces();
  };

  const closeChooser = () => {
    chooserGeneration.current += 1;
    setChooserOpen(false);
  };

  const chooseWorkspace = (root: string) => {
    if (workspaceOp.current) return;
    setWorkspace(root);
    closeChooser();
  };

  const generalRoutineSchedule = (): AssistantRoutine["schedule"] => {
    if (routineKind === "manual") return { kind: "manual", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" };
    if (routineKind === "interval") return { kind: "interval", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", interval_seconds: Math.max(1, routineHours) * 3600 };
    return { kind: "daily", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", at: routineAt };
  };

  const routinesFor = (): AssistantRoutine[] => {
    if (!template) return [];
    if (template.id === "code" || template.id === "promo") return templateRoutines(identity.assistantID, template, identity.createdAt);
    if (template.id === "general") {
      return [templateRoutine(identity.assistantID, identity.routineID, routineTitle.trim(), routinePrompt.trim(), generalRoutineSchedule(), identity.createdAt)];
    }
    return [];
  };

  const creationPolicy: AssistantRecord["policy"] | null = template ? (learnFirst
    ? { ...template.policy, local_write: "allow", network: "allow" }
    : { ...template.policy }) : null;

  const submit = async () => {
    if (!template || !template.available || !creationPolicy) return;
    const routines = routinesFor();
    if (routines.length === 0) return;
    const { assistantID: id, createdAt } = identity;
    const assistant: AssistantRecord = {
      id,
      name: name.trim() || template.defaultName,
      mission: mission.trim() || template.mission,
      description: "",
      scope: workspace.trim() ? "workspace" : "global",
      workspace_root: workspace.trim() || undefined,
      lifecycle: "active",
      policy: { ...creationPolicy },
      memory_revision: 0,
      revision: 0,
      created_at: createdAt,
      updated_at: createdAt,
    };
    const initialPrompt = learnFirst ? copy.learnFirstPrompt : undefined;
    const key = assistantMutationKey("create", id, id, { assistant, routines, template: template.id, initialPrompt });
    setBusy(true);
    try { await runAssistantMutation(key, (requestId) => assistantCreate({ requestId, assistant, routines, initialPrompt })); onCreated(id); }
    catch (cause) { showToast(cause instanceof Error ? cause.message : copy.error, "error"); setBusy(false); }
  };

  const accessLabel = (access: AssistantRecord["policy"][keyof AssistantRecord["policy"]]): string => {
    if (access === "allow") return copy.accessAllow;
    if (access === "deny") return copy.accessDeny;
    return copy.accessApprove;
  };
  const permissionRows = creationPolicy ? [
    { label: copy.policyLocalWrite, access: accessLabel(creationPolicy.local_write) },
    { label: copy.policyNetwork, access: accessLabel(creationPolicy.network) },
    { label: copy.policyPublish, access: accessLabel(creationPolicy.publish) },
    { label: copy.policyHighRisk, access: accessLabel(creationPolicy.delete) },
  ] : [];
  // Confirmation follows the effective policy, not the learn-first switch:
  // any auto-allow capability in the final policy still requires an explicit
  // user confirmation, while a fully read-only/approve policy does not.
  const needsConfirmation = Boolean(creationPolicy && Object.values(creationPolicy).some((access) => access === "allow"));
  const generalRoutineValid = template?.id !== "general" || (routineTitle.trim() !== "" && routinePrompt.trim() !== "");

  return (
    <div className="assistant-manager-backdrop assistant-manager-backdrop--create" role="presentation" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <form className="assistant-create" role="dialog" aria-modal="true" aria-label={copy.createTitle} onSubmit={(event) => { event.preventDefault(); void submit(); }}>
        <header><div><h2>{copy.createTitle}</h2><p>{copy.createBody}</p></div><button className="assistant-icon-button" type="button" aria-label={copy.close} onClick={onClose}><X size={18} /></button></header>

        {!templateID ? (
          <div className="assistant-templates">
            <h3>{copy.templates}</h3>
            {templates.map((item) => (
              <button
                key={item.id}
                type="button"
                className={`assistant-template${item.available ? "" : " assistant-template--disabled"}`}
                disabled={!item.available}
                onClick={() => pickTemplate(item.id)}
              >
                <span className="assistant-template__name">
                  {templateLabels[item.id]}
                  {!item.available && <span className="assistant-template__badge">{copy.phase4Preview}</span>}
                </span>
                <span className="assistant-template__desc">{templateDescriptions[item.id]}</span>
              </button>
            ))}
          </div>
        ) : (
          <>
            <div className="assistant-create__template-bar">
              <span>{templateLabels[templateID]}</span>
              <button className="assistant-text-action" type="button" onClick={() => setTemplateID(null)}><ChevronLeft size={13} />{copy.templates}</button>
            </div>
            <label>{copy.name}<input ref={nameRef} value={name} onChange={(event) => setName(event.target.value)} placeholder={copy.createName} required /></label>
            <label>{copy.mission}<textarea rows={5} value={mission} onChange={(event) => setMission(event.target.value)} placeholder={copy.createMission} required /></label>
            <div className="assistant-create__workspace">
              <label>{copy.workspace}<input id="assistant-workspace-input" value={workspace} onChange={(event) => setWorkspace(event.target.value)} placeholder="D:\\Work\\Project" /></label>
              <div className="assistant-create__workspace-actions">
                <button className="assistant-button" type="button" disabled={Boolean(workspaceOp.current) || picking || chooserOpen} onClick={openChooser} aria-label={copy.workspacePick}><FolderOpen size={14} />{copy.workspacePick}</button>
                <button className="assistant-button" type="button" disabled={Boolean(workspaceOp.current) || chooserOpen} onClick={() => void pickWorkspace()} aria-label={copy.workspaceNew}>{picking ? <RefreshCw className="assistant-spin" size={14} /> : <Plus size={14} />}{copy.workspaceNew}</button>
              </div>
              {chooserOpen && (
                <div className="assistant-create__workspace-chooser" role="group" aria-label={copy.workspaceChooseTitle} aria-busy={chooserLoading}>
                  <header>
                    <strong>{copy.workspaceChooseTitle}</strong>
                    <button className="assistant-icon-button" type="button" aria-label={copy.close} onClick={closeChooser}><X size={15} /></button>
                  </header>
                  <p>{copy.workspaceChooseBody}</p>
                  {chooserLoading ? (
                    <p className="assistant-create__workspace-chooser-note"><RefreshCw className="assistant-spin" size={13} />{copy.workspaceChooseLoading}</p>
                  ) : chooserError ? (
                    <p className="assistant-create__workspace-chooser-note assistant-create__workspace-chooser-error" role="alert">
                      <AlertCircle size={13} />{copy.workspaceChooseFailed}: {chooserError}
                      <button className="assistant-text-action" type="button" onClick={() => void loadWorkspaces()}>{copy.retry}</button>
                    </p>
                  ) : workspaceChoices.length === 0 ? (
                    <p className="assistant-create__workspace-chooser-note">{copy.workspaceChooseEmpty}</p>
                  ) : (
                    <ul className="assistant-create__workspace-chooser-list">
                      {workspaceChoices.map((choice) => (
                        <li key={choice.root}>
                          <button className="assistant-create__workspace-choice" type="button" onClick={() => chooseWorkspace(choice.root)}>
                            <span className="assistant-create__workspace-choice-label">{choice.label}</span>
                            <span className="assistant-create__workspace-choice-path">{choice.root}</span>
                          </button>
                        </li>
                      ))}
                    </ul>
                  )}
                  <p className="assistant-create__workspace-chooser-hint">{copy.workspaceNewHint}</p>
                </div>
              )}
            </div>

            {template && (template.id === "code" || template.id === "promo") && (
              <div className="assistant-create__routines">
                <span className="assistant-create__routines-label">{copy.routinePreview}</span>
                {template.routines.map((routine) => (
                  <span key={routine.title} className="assistant-create__routine"><Check size={13} />{routine.title}</span>
                ))}
              </div>
            )}

            {template && template.id === "general" && (
              <div className="assistant-create__routines">
                <span className="assistant-create__routines-label">{copy.routinePreview}</span>
                <label className="assistant-create__routine-label">{copy.routineTitle}<input value={routineTitle} onChange={(event) => setRoutineTitle(event.target.value)} placeholder={copy.routineTitle} /></label>
                <label className="assistant-create__routine-label">{copy.routinePrompt}<textarea rows={3} value={routinePrompt} onChange={(event) => setRoutinePrompt(event.target.value)} placeholder={copy.routinePrompt} /></label>
                <label className="assistant-create__routine-label">{copy.frequency}<select value={routineKind} onChange={(event) => setRoutineKind(event.target.value as "manual" | "daily" | "interval")}><option value="daily">{copy.daily}</option><option value="interval">{copy.interval}</option><option value="manual">{copy.routineFrequencyManual}</option></select></label>
                {routineKind === "daily" && <label className="assistant-create__routine-label">{copy.at}<input type="time" value={routineAt} onChange={(event) => setRoutineAt(event.target.value)} /></label>}
                {routineKind === "interval" && <label className="assistant-create__routine-label">{copy.hour}<input type="number" min={1} max={720} value={routineHours} onChange={(event) => setRoutineHours(Number(event.target.value))} /></label>}
              </div>
            )}

            {template && (
              <label className="assistant-check assistant-create__learn">
                {/* Turning learn-first off lowers the disclosed permissions, so an already
                    confirmed wider policy stays valid; re-enabling the boost raises them
                    again and therefore requires a fresh confirmation. */}
                <input type="checkbox" checked={learnFirst} onChange={(event) => { const boosted = event.target.checked; setLearnFirst(boosted); if (boosted) setConfirmed(false); }} />
                <span><strong>{copy.learnFirst}</strong><small>{copy.learnFirstHint}</small></span>
              </label>
            )}

            {template && (
              <div className="assistant-create__permissions" role="status">
                <span className="assistant-create__routines-label">{copy.permissionTitle}</span>
                {permissionRows.map((row) => (
                  <span key={row.label} className="assistant-create__permission-row"><span>{row.label}</span><strong>{row.access}</strong></span>
                ))}
              </div>
            )}

            {needsConfirmation && (
              <label className="assistant-check assistant-create__confirm">
                <input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />
                {copy.permissionConfirm}
              </label>
            )}

            <footer>
              <button className="assistant-button" type="button" onClick={() => setTemplateID(null)}>{copy.cancel}</button>
              <button className="assistant-button assistant-button--accent" type="submit" disabled={busy || !name.trim() || !mission.trim() || !generalRoutineValid || (needsConfirmation && !confirmed)}>{busy && <RefreshCw className="assistant-spin" size={14} />}{copy.create}</button>
            </footer>
          </>
        )}
      </form>
    </div>
  );
}
