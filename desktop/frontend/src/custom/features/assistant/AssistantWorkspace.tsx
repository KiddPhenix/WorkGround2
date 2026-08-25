import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  Bot,
  Brain,
  CalendarClock,
  Check,
  ChevronLeft,
  ChevronRight,
  Clock3,
  ExternalLink,
  History,
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
import {
  assistantApplyMemory,
  assistantCancel,
  assistantCreate,
  assistantGet,
  assistantList,
  assistantPutRoutine,
  assistantResolveAttention,
  assistantResume,
  assistantRunNow,
  assistantUpdate,
} from "./assistant.bridge";
import { assistantCopy } from "./assistant.copy";
import { attentionInboxAction, attentionRejectResolution, attentionResolution, formatAssistantDate, formatTimelineTime, responsibilityLabel, responsibilityStatusLabel, runHistoryAction, runStateLabel, scheduleLabel, timelineEntries } from "./assistant.model";
import { assistantIntentKey, assistantMutationKey, assistantOutcomeKey, completeAssistantRequest, pendingAssistantRequest, runAssistantApproval, runAssistantCASMutation, runAssistantMutation, runAssistantOutcome, runAssistantRejection, runAssistantResume } from "./assistant.requests";
import { assistantTemplate, assistantTemplateContent, templateRoutine, templateRoutines, type AssistantTemplateID } from "./assistant.templates";
import {
  assistantEntityID,
  type AssistantAccess,
  type AssistantMemoryKind,
  type AssistantDiagnostic,
  type AssistantPolicy,
  type AssistantRecord,
  type AssistantRoutine,
  type AssistantScheduleKind,
  type AssistantSnapshot,
} from "./assistant.types";
import "./assistant.css";

type ManageTab = "overview" | "routines" | "memory" | "history" | "attention" | "plan";

interface AssistantWorkspaceProps {
  onOpenSession?: (scope: string, workspaceRoot: string, sessionPath: string) => void;
  focusAssistantID?: string;
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

export function AssistantWorkspace({ onOpenSession, focusAssistantID }: AssistantWorkspaceProps) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const { showToast } = useToast();
  const data = useAssistantData(focusAssistantID);
  const [manageTab, setManageTab] = useState<ManageTab | null>(null);
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState("");
  const [handoff, setHandoff] = useState("");
  const [handoffNotice, setHandoffNotice] = useState(false);
  const today = useMemo(() => new Date(), [data.snapshot?.revision]);
  const timeline = useMemo(() => data.snapshot ? timelineEntries(data.snapshot, today, copy) : [], [copy, data.snapshot, today]);
  const openAttention = data.snapshot?.attention.filter((item) => attentionInboxAction(item, data.snapshot?.runs.find((run) => run.id === item.run_id)) !== "none").length ?? 0;

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
    const runRequestKey = `${intentKey}:run`;
    try {
      const existing = data.snapshot.routines.find((routine) => routine.id === `adhoc-${data.snapshot!.assistant.id}`);
      const now = new Date().toISOString();
      const routine: AssistantRoutine = existing ? { ...existing, title: "临时交办", prompt, enabled: true, updated_at: now } : {
        id: `adhoc-${data.snapshot.assistant.id}`,
        assistant_id: data.snapshot.assistant.id,
        title: "临时交办",
        prompt,
        schedule: { kind: "manual", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" },
        enabled: true,
        catch_up: "coalesce_latest",
        revision: 0,
        created_at: now,
        updated_at: now,
      };
      const routineKey = assistantMutationKey("routine", data.snapshot.assistant.id, routine.id, { title: routine.title, prompt: routine.prompt, schedule: routine.schedule, enabled: routine.enabled, catch_up: routine.catch_up });
      const saved = await runAssistantCASMutation(routineKey, existing?.revision ?? 0, ({ requestId, expectedRevision }) => assistantPutRoutine({ requestId, expectedRevision, routine }));
      await assistantRunNow({ assistantId: data.snapshot.assistant.id, routineId: saved.id, requestId: pendingAssistantRequest(runRequestKey), maxAttempts: 3 });
      completeAssistantRequest(runRequestKey);
      setHandoff("");
      setHandoffNotice(true);
      await data.refresh();
    } catch (cause) {
      showToast(cause instanceof Error ? cause.message : copy.error, "error");
    } finally {
      setBusy("");
    }
  }, [busy, copy.error, data, handoff, showToast]);

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
          {openAttention > 0 && <button className="assistant-attention-chip" type="button" onClick={() => setManageTab("attention")}><AlertCircle size={14} />{openAttention}</button>}
          <button className="assistant-icon-button" type="button" aria-label={copy.newAssistant} title={copy.newAssistant} onClick={() => setCreating(true)}><Plus size={17} /></button>
          <button className="assistant-icon-button" type="button" aria-label={copy.manage} title={copy.manage} onClick={() => setManageTab("overview")}><Settings2 size={17} /></button>
        </div>
      </header>

      <button className="assistant-continue" type="button" disabled={Boolean(busy) || assistant.lifecycle !== "active"} onClick={() => void run()}>
        {busy === "run" ? <RefreshCw className="assistant-spin" size={16} /> : <Play size={16} fill="currentColor" />}
        {copy.continueWork}
      </button>

      {data.diagnostics.length > 0 && (
        <div className="assistant-diagnostic" role="status">
          <AlertCircle size={14} aria-hidden="true" />
          <span>{copy.partialWarning}</span>
          <button type="button" onClick={() => setManageTab("overview")}>{copy.viewDetails}</button>
        </div>
      )}

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
                {entry.kind === "run" && entry.run && <span className={`assistant-run-state assistant-run-state--${entry.run.state}`}>{runStateLabel(entry.run.state, locale)}</span>}
                {entry.detail && entry.detail !== entry.title && <p>{entry.detail}</p>}
                {entry.run?.error && <p className="assistant-event__error">{entry.run.error.message}</p>}
                {entry.kind === "next" && entry.routine && (
                  <button type="button" className="assistant-text-action" onClick={() => setManageTab("routines")}>{copy.changeTime}<ChevronRight size={13} /></button>
                )}
              </div>
            </article>
          ))}
        </div>

        <form className="assistant-handoff" onSubmit={(event) => { event.preventDefault(); void submitHandoff(); }}>
          <ChevronRight size={18} aria-hidden="true" />
          <label className="sr-only" htmlFor="assistant-handoff-input">{copy.taskPlaceholder}</label>
          <input id="assistant-handoff-input" value={handoff} onChange={(event) => setHandoff(event.target.value)} placeholder={copy.taskPlaceholder} disabled={Boolean(busy)} />
          <button type="submit" disabled={!handoff.trim() || Boolean(busy)} aria-label={copy.send}><Send size={16} /></button>
        </form>
        {handoffNotice && <div className="assistant-notice" role="status"><Check size={14} />{copy.queued}<button type="button" aria-label={copy.close} onClick={() => setHandoffNotice(false)}><X size={13} /></button></div>}
      </div>

      {manageTab && (
        <AssistantManager
          snapshot={snapshot}
          tab={manageTab}
          onTab={setManageTab}
          onClose={() => setManageTab(null)}
          onRefresh={data.refresh}
          onRun={run}
          onOpenSession={onOpenSession}
          diagnostics={data.diagnostics}
        />
      )}
      {creating && <CreateAssistantDialog onClose={() => setCreating(false)} onCreated={(id) => { setCreating(false); void data.loadList(id); }} />}
    </section>
  );
}

function AssistantManager({ snapshot, tab, onTab, onClose, onRefresh, onRun, onOpenSession, diagnostics }: {
  snapshot: AssistantSnapshot;
  tab: ManageTab;
  onTab: (tab: ManageTab) => void;
  onClose: () => void;
  onRefresh: () => Promise<void>;
  onRun: (routineID?: string) => Promise<void>;
  onOpenSession?: AssistantWorkspaceProps["onOpenSession"];
  diagnostics: AssistantDiagnostic[];
}) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const { showToast } = useToast();
  const [busy, setBusy] = useState("");
  const tabs: Array<{ id: ManageTab; label: string; icon: typeof Bot }> = [
    { id: "overview", label: copy.overview, icon: Bot },
    { id: "plan", label: copy.plan, icon: Check },
    { id: "routines", label: copy.routines, icon: CalendarClock },
    { id: "memory", label: copy.memory, icon: Brain },
    { id: "history", label: copy.history, icon: History },
    { id: "attention", label: copy.attention, icon: AlertCircle },
  ];
  const act = async (key: string, action: () => Promise<unknown>) => {
    setBusy(key);
    try { await action(); await onRefresh(); return true; }
    catch (cause) { showToast(cause instanceof Error ? cause.message : copy.error, "error"); return false; }
    finally { setBusy(""); }
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
        <nav aria-label={copy.manage}>{tabs.map(({ id, label, icon: Icon }) => <button key={id} type="button" aria-current={tab === id ? "page" : undefined} className={tab === id ? "is-active" : ""} onClick={() => onTab(id)}><Icon size={15} />{label}{id === "attention" && snapshot.attention.some((item) => attentionInboxAction(item, snapshot.runs.find((run) => run.id === item.run_id)) !== "none") && <span className="assistant-nav-dot" />}</button>)}</nav>
        <div className="assistant-manager__content">
          {tab === "overview" && <OverviewEditor snapshot={snapshot} diagnostics={diagnostics} busy={busy} act={act} />}
          {tab === "plan" && <PlanView snapshot={snapshot} />}
          {tab === "routines" && <RoutineEditor snapshot={snapshot} busy={busy} act={act} onRun={onRun} />}
          {tab === "memory" && <MemoryEditor snapshot={snapshot} busy={busy} act={act} />}
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

type Act = (key: string, action: () => Promise<unknown>) => Promise<boolean>;

const ALWAYS_ASK_POLICY: ReadonlySet<keyof AssistantPolicy> = new Set(["publish", "delete", "payment", "secrets", "private_data"]);

export function OverviewEditor({ snapshot, diagnostics, busy, act }: { snapshot: AssistantSnapshot; diagnostics: AssistantDiagnostic[]; busy: string; act: Act }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  const [name, setName] = useState(snapshot.assistant.name);
  const [mission, setMission] = useState(snapshot.assistant.mission);
  const [scope, setScope] = useState(snapshot.assistant.scope);
  const [workspace, setWorkspace] = useState(snapshot.assistant.workspace_root ?? "");
  const [policy, setPolicy] = useState<AssistantPolicy>(snapshot.assistant.policy);
  useEffect(() => {
    setName(snapshot.assistant.name);
    setMission(snapshot.assistant.mission);
    setScope(snapshot.assistant.scope);
    setWorkspace(snapshot.assistant.workspace_root ?? "");
    setPolicy(snapshot.assistant.policy);
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
      {diagnostics.length > 0 && <div className="assistant-diagnostic-list" role="status"><strong>{copy.diagnosticTitle}</strong>{diagnostics.map((item, index) => <p key={`${item.at}-${index}`}><span>{item.operation}</span>{item.message}</p>)}</div>}
      <div className="assistant-form__actions">
        <button className="assistant-button" type="button" disabled={Boolean(busy)} onClick={() => void changeLifecycle()}>
          {lifecycle === "paused" ? <Pause size={14} /> : <Play size={14} />}{lifecycle === "paused" ? copy.pause : copy.resume}
        </button>
        <button className="assistant-button assistant-button--accent" type="submit" disabled={Boolean(busy) || !name.trim() || !mission.trim() || (scope === "workspace" && !workspace.trim())}>{copy.save}</button>
      </div>
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

function RunHistory({ snapshot, busy, act, onRun, onAttention, onOpenSession }: { snapshot: AssistantSnapshot; busy: string; act: Act; onRun: (id?: string) => Promise<void>; onAttention: () => void; onOpenSession?: AssistantWorkspaceProps["onOpenSession"] }) {
  const { locale } = useI18n();
  const copy = assistantCopy(locale);
  if (snapshot.runs.length === 0) return <p className="assistant-empty-copy">{copy.noHistory}</p>;
  const cancel = (runID: string) => {
    const key = assistantIntentKey("cancel", snapshot.assistant.id, runID);
    return act(`cancel-${runID}`, () => runAssistantMutation(key, (requestId) => assistantCancel({ runId: runID, requestId, reason: "用户从助手工作区停止" })));
  };
  return <div className="assistant-history">{[...snapshot.runs].sort((a, b) => b.created_at.localeCompare(a.created_at)).map((run) => {
    const action = runHistoryAction(run.state);
    return <article key={run.id} className="assistant-history-item"><div><strong>{runStateLabel(run.state, locale)}</strong><time>{formatTimelineTime(new Date(run.started_at || run.created_at), locale)}</time></div><p>{run.summary || run.error?.message || snapshot.routines.find((item) => item.id === run.routine_id)?.title}</p>{run.state === "retry_wait" && <span className="assistant-history-item__hint">{copy.waitingRetry}</span>}<div>{run.session_path && <button className="assistant-text-action" type="button" onClick={() => onOpenSession?.(run.scope || snapshot.assistant.scope, run.workspace_root || snapshot.assistant.workspace_root || "", run.session_path!)}><ExternalLink size={13} />{copy.openSession}</button>}{action === "rerun" && <button className="assistant-text-action" type="button" disabled={Boolean(busy)} onClick={() => void onRun(run.routine_id)}><RefreshCw size={13} />{copy.rerun}</button>}{action === "cancel" && <button className="assistant-text-action" type="button" disabled={Boolean(busy)} onClick={() => void cancel(run.id)}><X size={13} />{copy.stopRun}</button>}{action === "attention" && <button className="assistant-text-action" type="button" onClick={onAttention}><AlertCircle size={13} />{copy.handleAttention}</button>}</div></article>;
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
  };

  const generalRoutineSchedule = (): AssistantRoutine["schedule"] => {
    if (routineKind === "manual") return { kind: "manual", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" };
    if (routineKind === "interval") return { kind: "interval", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", interval_seconds: Math.max(1, routineHours) * 3600 };
    return { kind: "daily", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", at: routineAt };
  };

  const routinesFor = (): AssistantRoutine[] => {
    if (!template) return [];
    if (template.id === "code") return templateRoutines(identity.assistantID, template, identity.createdAt);
    if (template.id === "general") {
      return [templateRoutine(identity.assistantID, identity.routineID, routineTitle.trim(), routinePrompt.trim(), generalRoutineSchedule(), identity.createdAt)];
    }
    return [];
  };

  const submit = async () => {
    if (!template || !template.available) return;
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
      policy: { ...template.policy },
      memory_revision: 0,
      revision: 0,
      created_at: createdAt,
      updated_at: createdAt,
    };
    const key = assistantMutationKey("create", id, id, { assistant, routines, template: template.id });
    setBusy(true);
    try { await runAssistantMutation(key, (requestId) => assistantCreate({ requestId, assistant, routines })); onCreated(id); }
    catch (cause) { showToast(cause instanceof Error ? cause.message : copy.error, "error"); setBusy(false); }
  };

  const accessLabel = (access: AssistantRecord["policy"][keyof AssistantRecord["policy"]]): string => {
    if (access === "allow") return copy.accessAllow;
    if (access === "deny") return copy.accessDeny;
    return copy.accessApprove;
  };
  const permissionRows = template ? [
    { label: copy.policyLocalWrite, access: accessLabel(template.policy.local_write) },
    { label: copy.policyNetwork, access: accessLabel(template.policy.network) },
    { label: copy.policyPublish, access: accessLabel(template.policy.publish) },
    { label: copy.policyHighRisk, access: accessLabel(template.policy.delete) },
  ] : [];
  const needsConfirmation = template?.id === "code";
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
            <label>{copy.workspace}<input value={workspace} onChange={(event) => setWorkspace(event.target.value)} placeholder="D:\\Work\\Project" /></label>

            {template && template.id === "code" && (
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
