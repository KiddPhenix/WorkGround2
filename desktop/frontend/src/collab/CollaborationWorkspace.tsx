import { useEffect, useLayoutEffect, useState } from "react";
import { Bot, Check, ChevronRight, Circle, CircleAlert, Copy, ImagePlus, LogOut, Pencil, RadioTower, RefreshCw, Settings2, Share2, Shield, ShieldAlert, ShieldCheck, Trash2, Users, X } from "lucide-react";
import { useI18n } from "../lib/i18n";
import { ModelSwitcher } from "../components/ModelSwitcher";
import { collabCopy } from "./copy";
import { useCollabController } from "./useCollabController";
import { CollaborationComposer } from "./components/CollaborationComposer";
import { ConnectionPanel } from "./components/ConnectionPanel";
import { CollaborationTimeline } from "./components/CollaborationTimeline";
import { AgentActivityPopover, type AgentActivityAnchor } from "./components/AgentActivityPopover";
import { CollaborationAvatar } from "./components/CollaborationAvatar";
import { compressCollaborationAvatar } from "./avatar";
import { loadCollaborationIdentity, saveCollaborationIdentity } from "./identity";
import { tryBuildCollaborationInvite } from "./invite";
import { agentCollaborationClock } from "./state";
import type { CollaborationInvite, CollaborationTimelineItem, CollaborationToolApprovalMode, CollaborationWorkspaceOption } from "./types";
import type { ComposerSubmitKey } from "../lib/composerKeyboard";
import { useScrollManager } from "../lib/useScrollManager";
import "./collab.css";
import "./collab-handoff.css";

interface CollaborationWorkspaceProps {
  sessionID?: string;
  tabID?: string;
  mode?: "session" | "dialog";
  onClose?(): void;
  onConnected?(): Promise<void> | void;
  onConnectRequest?(): void;
  submitKey?: ComposerSubmitKey;
  modelLabel?: string;
  onSwitchModel?(name: string): Promise<void>;
  workspaces?: CollaborationWorkspaceOption[];
  workspaceRoot?: string;
  onWorkspaceChange?(root: string): void;
  sessionResolving?: boolean;
  sessionError?: string;
  onRetrySession?(): void;
}

function statusLabel(status: string, c: ReturnType<typeof collabCopy>) {
  if (status === "syncing" || status === "connecting") return c("syncing");
  if (status === "reconnecting") return c("reconnecting");
  if (status === "failed") return c("failed");
  return c("synced");
}

function agentStatusLabel(status: "idle" | "running" | "waiting" | "completed" | "error" | "offline", c: ReturnType<typeof collabCopy>) {
  return c(status);
}

function handleAction(promise: Promise<unknown>) {
  void promise.catch(() => {});
}

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const input = document.createElement("textarea");
  input.value = value;
  input.style.position = "fixed";
  input.style.opacity = "0";
  document.body.appendChild(input);
  input.select();
  const copied = document.execCommand("copy");
  input.remove();
  if (!copied) throw new Error("copy failed");
}

export function CollaborationWorkspace({ sessionID, tabID, mode = "session", onClose, onConnected, onConnectRequest, submitKey = "enter", modelLabel = "—", onSwitchModel = async () => {}, workspaces = [], workspaceRoot = "", onWorkspaceChange = () => {}, sessionResolving = false, sessionError, onRetrySession = () => {} }: CollaborationWorkspaceProps) {
  const { t } = useI18n();
  const c = collabCopy(t);
  const controller = useCollabController(sessionID || "");
  const { state, self, agentBusy, selectedItems } = controller;
  const [replyTo, setReplyTo] = useState<CollaborationTimelineItem>();
  const [batchInstruction, setBatchInstruction] = useState("");
  const [invite, setInvite] = useState<CollaborationInvite>();
  const [inviteHost, setInviteHost] = useState("");
  const [inviteError, setInviteError] = useState("");
  const [inviteCopied, setInviteCopied] = useState(false);
  const [configSaving, setConfigSaving] = useState(false);
  const [approvalSaving, setApprovalSaving] = useState(false);
  const [profileEditor, setProfileEditor] = useState<"member" | "agent">();
  const [profileName, setProfileName] = useState("");
  const [profileAvatar, setProfileAvatar] = useState("");
  const [profileError, setProfileError] = useState("");
  const [profileSaving, setProfileSaving] = useState(false);
  const [contextOpen, setContextOpen] = useState(false);
  const [agentCollaborationOpen, setAgentCollaborationOpen] = useState(false);
  const [activityAnchor, setActivityAnchor] = useState<AgentActivityAnchor>();
  const { scrollRef: timelineRef, stick: timelineStick, onScroll: onTimelineScroll, snapToBottom: snapTimelineToBottom } = useScrollManager();
  const ownsRoom = Boolean(sessionID) && state.selfSessionId === sessionID;
  const usable = ownsRoom && Boolean(state.room);
  const onlineMembers = state.members.filter((member) => member.online);
  const agentClock = agentCollaborationClock(state);

  useLayoutEffect(() => {
    snapTimelineToBottom();
  }, [snapTimelineToBottom, sessionID, state.room?.room]);

  useLayoutEffect(() => {
    if (timelineStick.current) snapTimelineToBottom();
  }, [snapTimelineToBottom, state.timeline, state.transfers, timelineStick]);

  useEffect(() => {
    if (!profileEditor || !self) return;
    setProfileName(profileEditor === "member" ? self.name : self.agent.name);
    setProfileAvatar(profileEditor === "member" ? self.avatar || "" : self.agent.avatar || "");
    setProfileError("");
  }, [profileEditor, self]);

  useEffect(() => {
    if (!activityAnchor) return;
    const member = state.members.find((item) => item.id === activityAnchor.memberId);
    if (!member?.online || member.agent.status !== "running") {
      setActivityAnchor(undefined);
      return;
    }
    const close = () => setActivityAnchor(undefined);
    window.addEventListener("resize", close);
    window.addEventListener("scroll", close, true);
    return () => {
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", close, true);
    };
  }, [activityAnchor, state.members]);

  if (mode === "dialog") {
    return <div className="collab-modal" role="dialog" aria-modal="true" aria-label={c("title")}>
      <div className="collab-modal__backdrop" onClick={onClose} />
      <section className="collab-surface collab-surface--dialog">
        <ConnectionPanel
          sessionID={sessionID}
          status={state.status}
          error={state.lastError}
          initial={ownsRoom ? state.room : undefined}
          workspaces={workspaces}
          workspaceRoot={workspaceRoot}
          onWorkspaceChange={onWorkspaceChange}
          sessionResolving={sessionResolving}
          sessionError={sessionError}
          onRetrySession={onRetrySession}
          onHost={controller.host}
          onJoin={controller.join}
          relayConfig={controller.relayConfig}
          roomQuery={controller.roomQuery}
          discoveryLoading={controller.discoveryLoading}
          discoveryError={controller.discoveryError}
          relaySaving={controller.relaySaving}
          relayConfigError={controller.relayConfigError}
          onQueryRooms={controller.queryRooms}
          onSaveRelayConfig={controller.saveRelayConfig}
          onClose={() => onClose?.()}
          onConnected={onConnected}
        />
      </section>
    </div>;
  }

  if (!ownsRoom || !state.room) {
    return <section className="collab-surface collab-surface--empty" aria-label={c("title")}>
      <div className="collab-session-empty">
        <Users size={30} aria-hidden="true" />
        <h2>{c("title")}</h2>
        <p>{state.lastError || c("failed")}</p>
        <button type="button" className="collab-primary-button" onClick={onConnectRequest}>{c("connect")}</button>
      </div>
    </section>;
  }

  const runForItem = (item: CollaborationTimelineItem) => {
    const instruction = c("agentInstructionReference", { text: item.text });
    handleAction(controller.startAgent(instruction, [item.id]));
  };
  const runSelected = () => {
    const instruction = batchInstruction.trim() || c("agentInstructionBatch");
    handleAction(controller.startAgent(instruction, selectedItems.map((item) => item.id)).then(() => {
      setBatchInstruction("");
      controller.clearSelection();
    }));
  };
  const toggleInvite = async () => {
    if (invite) {
      setInvite(undefined);
      return;
    }
    setInviteError("");
    try {
      const next = await controller.invite();
      setInvite(next);
      setInviteHost(next.hosts[0] || "127.0.0.1");
    } catch (error) {
      setInviteError(error instanceof Error ? error.message : String(error));
    }
  };
  const inviteString = invite?.invite || (invite?.version === 2 && invite.hostKey && invite.routes?.length
    ? tryBuildCollaborationInvite({ version: 2, room: invite.room, hostKey: invite.hostKey, routes: invite.routes, roomToken: invite.token })
    : invite && inviteHost ? tryBuildCollaborationInvite({ host: inviteHost, port: invite.port, room: invite.room, token: invite.token }) : "");
  const inviteBuildError = invite && !inviteString ? c("connectionStringInvalid") : "";
  const copyInvite = async () => {
    if (!inviteString) return;
    try {
      await copyText(inviteString);
      setInviteCopied(true);
      setInviteError("");
      window.setTimeout(() => setInviteCopied(false), 1500);
    } catch (error) {
      setInviteError(error instanceof Error ? error.message : String(error));
    }
  };
  const saveAgentConfig = async (next = state.agentConfig) => {
    setConfigSaving(true);
    try {
      await controller.updateAgentConfig(next);
    } finally {
      setConfigSaving(false);
    }
  };
  const windAgentClock = () => saveAgentConfig({ ...state.agentConfig, agentClockWoundAt: new Date().toISOString() });
  const saveProfile = async () => {
    if (!self || !profileEditor || !profileName.trim()) return;
    const profile = {
      memberName: profileEditor === "member" ? profileName.trim() : self.name,
      memberAvatar: profileEditor === "member" ? profileAvatar : self.avatar,
      agentName: profileEditor === "agent" ? profileName.trim() : self.agent.name,
      agentAvatar: profileEditor === "agent" ? profileAvatar : self.agent.avatar,
    };
    setProfileSaving(true);
    setProfileError("");
    try {
      await controller.updateProfile(profile);
      const savedIdentity = loadCollaborationIdentity();
      saveCollaborationIdentity({
        memberID: self.id, memberName: profile.memberName, memberAvatar: profile.memberAvatar, memberRole: self.role || savedIdentity?.memberRole,
        agentID: self.agent.id, agentName: profile.agentName, agentAvatar: profile.agentAvatar, agentRole: savedIdentity?.agentRole,
      });
      setProfileEditor(undefined);
    } catch (error) {
      setProfileError(error instanceof Error ? error.message : String(error));
    } finally {
      setProfileSaving(false);
    }
  };
  const chooseAvatar = async (file?: File) => {
    if (!file) return;
    setProfileError("");
    try {
      setProfileAvatar(await compressCollaborationAvatar(file));
    } catch (error) {
      setProfileError(error instanceof Error ? error.message : String(error));
    }
  };
  const chooseApprovalMode = async (next: CollaborationToolApprovalMode) => {
    if (next === (state.toolApprovalMode || "ask")) return;
    setApprovalSaving(true);
    try {
      await controller.updateToolApprovalMode(next);
    } finally {
      setApprovalSaving(false);
    }
  };
  const toggleContextRef = async (ref: string, selected: boolean) => {
    const current = state.agentConfig.contextRefs || [];
    const contextRefs = selected ? [...new Set([...current, ref])] : current.filter((item) => item !== ref);
    await saveAgentConfig({ ...state.agentConfig, contextRefs });
  };
  const showAgentActivity = (memberId: string, target: HTMLElement) => {
    const rect = target.getBoundingClientRect();
    setActivityAnchor({ memberId, left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom });
  };
  const activityMember = activityAnchor ? onlineMembers.find((member) => member.id === activityAnchor.memberId) : undefined;

  return <section className="collab-surface" aria-label={c("title")}>
    <div className="collab-workspace">
      <main className="collab-main">
        <header className="collab-topicbar">
          <div><div className="collab-topic-title"><h1>{state.room.title || state.room.room}</h1><span><Users size={14} />{onlineMembers.length}</span></div><p>{state.room.description || c("subtitle")}</p></div>
          <div className={`collab-connection collab-connection--${state.status}`}><Circle size={9} fill="currentColor" />{statusLabel(state.status, c)}</div>
          {((state.routes?.length || 0) > 0 || (state.advertisement?.relays.length || 0) > 0) && <details className="collab-reachability">
            <summary aria-label={c("reachabilityStatus")} title={c("reachabilityStatus")}><RadioTower size={16} /></summary>
            <section aria-label={c("reachabilityStatus")}>
              {(state.routes || []).map((route) => <div key={route.id} className={`collab-route-state collab-route-state--${route.status}`} title={route.lastError}><Circle size={8} fill="currentColor" /><span>{route.kind === "lan" ? c("lanRoute") : route.relayId || route.id}</span><small>{c(route.status === "connected" ? "routeConnected" : route.status === "connecting" ? "routeConnecting" : route.status === "degraded" ? "routeDegraded" : route.status === "failed" ? "routeFailed" : "routeDisabled")}{route.latencyMs !== undefined ? ` · ${route.latencyMs}ms` : ""}{route.active ? ` · ${c("activeRoute")}` : ""}</small>{route.retryable && <button type="button" onClick={() => void controller.refresh(true)}>{c("retry")}</button>}</div>)}
              {(state.advertisement?.relays || []).map((advertisement) => <div key={`ad:${advertisement.relayId}`} className={`collab-route-state collab-route-state--${advertisement.status}`} title={advertisement.lastError}><Circle size={8} fill="currentColor" /><span>{advertisement.relayId}</span><small>{c("advertisementStatus")} · {c(advertisement.status === "published" ? "advertisementPublished" : advertisement.status === "pending" ? "advertisementPending" : advertisement.status === "failed" ? "advertisementFailed" : advertisement.status === "revoking" ? "advertisementRevoking" : advertisement.status === "revoked" ? "advertisementRevoked" : "routeDisabled")}</small></div>)}
            </section>
          </details>}
          {state.mode === "host" && <div className="collab-invite-wrap">
            <button type="button" className="collab-icon-button" aria-label={c("exportConnection")} title={c("exportConnection")} onClick={() => void toggleInvite()}><Share2 size={17} /></button>
            {(invite || inviteError) && <div className="collab-invite-popover" role="dialog" aria-label={c("exportConnection")}>
              <strong>{c("exportConnection")}</strong>
              {invite && <>
                {invite.hosts.length > 0 && <label><span>{c("selectLocalIP")}</span><select value={inviteHost} onChange={(event) => { setInviteHost(event.target.value); setInviteCopied(false); }}>{invite.hosts.map((host) => <option key={host} value={host}>{host}</option>)}</select></label>}
                <div className="collab-invite-value"><input readOnly value={inviteString} aria-label={c("connectionString")} /><button type="button" disabled={!inviteString} onClick={() => void copyInvite()} aria-label={c("copyConnection")} title={c("copyConnection")}>{inviteCopied ? <Check size={15} /> : <Copy size={15} />}</button></div>
                <small>{c("connectionTokenNotice")}</small>
              </>}
              {(inviteError || inviteBuildError) && <span className="collab-invite-error">{inviteError || inviteBuildError}</span>}
            </div>}
          </div>}
          <button type="button" className="collab-icon-button" aria-label={c("leave")} title={c("leave")} onClick={() => void controller.leave()}><LogOut size={17} /></button>
        </header>

        {(state.actionError || state.status === "syncing" || state.status === "reconnecting" || state.status === "failed" || Boolean(state.unsyncedCount)) && <div className={`collab-status-banner collab-status-banner--${state.actionError ? "action" : state.status}`} role="status">
          {state.actionError ? <CircleAlert size={14} /> : <RefreshCw size={14} />}
          <span>{state.actionError || state.lastError || statusLabel(state.status, c)}{!state.actionError && (state.status === "reconnecting" || state.status === "failed") ? ` · ${c("cachedBackground")}` : ""}{!state.actionError && state.unsyncedCount ? ` · ${c("unsynced", { n: state.unsyncedCount })}` : ""}</span>
          {!state.actionError && state.retryable && <button type="button" onClick={() => void controller.refresh(true)}>{c("retry")}</button>}
        </div>}

        <div ref={timelineRef} className="collab-scroll" aria-label={c("timeline")} onScroll={onTimelineScroll}>
          <CollaborationTimeline
            items={state.timeline}
            members={state.members}
            selfMemberId={state.selfMemberId}
            selectedIds={state.selectedIds}
            pendingIntents={state.pendingIntents}
            connected={usable}
            agentBusy={agentBusy}
            transfers={state.transfers || []}
            agentPrompt={state.agentPrompt}
            onToggle={controller.toggleSelection}
            onReply={setReplyTo}
            onAgree={(item) => handleAction(controller.agree(item))}
            onAgreeRun={(item) => handleAction(controller.agree(item).then(() => controller.startAgent(c("agentInstructionAgree", { text: item.text }), [item.id])))}
            onAgent={runForItem}
            onAccept={(item) => handleAction(controller.acceptRequest(item))}
            onReject={(item) => handleAction(controller.rejectRequest(item))}
            onRespondAgentRun={(item, response) => handleAction(controller.respondAgentRun(item, response))}
            onStartPending={(intent) => void controller.startPending(intent)}
            onStopPending={controller.stopPending}
            onEditPending={controller.editPending}
            onReceiveFile={(id) => handleAction(controller.receiveFile(id))}
            onPauseFile={(id) => handleAction(controller.pauseFile(id))}
            onResumeFile={(id) => handleAction(controller.resumeFile(id))}
            onRevokeFile={(id) => handleAction(controller.revokeFile(id))}
            onOpenFile={(id) => handleAction(controller.openFile(id))}
            onRevealFile={(id) => handleAction(controller.revealFile(id))}
          />
        </div>

        <footer className="collab-footer">
          {selectedItems.length > 0 && <div className="collab-selection-bar">
            <span>{c("selected", { n: selectedItems.length })}</span>
            <input value={batchInstruction} onChange={(event) => setBatchInstruction(event.target.value)} placeholder={c("instruction")} aria-label={c("instruction")} />
            <button type="button" className="collab-primary-button" disabled={!usable || !sessionID} title={agentBusy ? c("agentQueueHint") : undefined} onClick={runSelected}><Bot size={15} />{c("agentRespond")}</button>
            <button type="button" className="collab-icon-button" aria-label={c("clear")} onClick={controller.clearSelection}><X size={16} /></button>
          </div>}
          <CollaborationComposer
            members={state.members}
            selfMemberId={state.selfMemberId}
            connected={state.status === "connected"}
            disabled={!usable}
            agentBusy={agentBusy}
            submitKey={submitKey}
            replyTo={replyTo}
            onReplyClear={() => setReplyTo(undefined)}
            onChat={controller.postChat}
            onContribution={controller.postContribution}
            onAgent={(text, referenceIDs) => controller.startAgent(text, referenceIDs)}
            onRequest={(memberId, text, referenceIDs) => controller.requestAgent(memberId, text, referenceIDs)}
            onShareFiles={controller.shareFiles}
          />
        </footer>
      </main>

      <aside className="collab-members">
        {self && <section className="collab-agent-config" aria-labelledby="collab-agent-config-title">
          <header>
            <span><Settings2 size={15} aria-hidden="true" /></span>
            <div><h2 id="collab-agent-config-title">{c("agentSettings")}</h2><p>{c("agentSettingsHint")}</p></div>
          </header>
          {state.currentRun && state.currentRun.phase !== "idle" && (
            <div className="collab-current-run" data-phase={state.currentRun.phase}>
              <div className="collab-current-run__status">
                <Circle size={10} fill="currentColor" />
                <span>{c(`agentRunPhase_${state.currentRun.phase}`)}</span>
                {state.currentRun.phase === "stopping" && <RefreshCw size={12} className="collab-current-run__spin" />}
              </div>
              <p className="collab-current-run__instruction" title={state.currentRun.instruction}>
                {state.currentRun.instruction}
              </p>
              {state.currentRun.progress && (
                <p className="collab-current-run__progress">{state.currentRun.progress}</p>
              )}
              <div className="collab-current-run__meta">
                {state.currentRun.startedAt ? (
                  <small>{c("agentRunStarted", { time: new Date(state.currentRun.startedAt).toLocaleTimeString() })}</small>
                ) : null}
                {state.currentRun.queueCount > 0 ? (
                  <small>{c("agentRunQueued", { n: state.currentRun.queueCount })}</small>
                ) : null}
              </div>
              {(state.currentRun.phase === "running" || state.currentRun.phase === "waiting_approval") && (
                <button
                  type="button"
                  className="collab-current-run__stop"
                  onClick={() => handleAction(controller.stopCurrentRun(state.currentRun!.runId))}
                >
                  <CircleAlert size={14} />
                  {c("agentRunStop")}
                </button>
              )}
              {state.currentRun.phase === "stopping" && (
                <button type="button" className="collab-current-run__stop" disabled>
                  <RefreshCw size={14} className="collab-current-run__spin" />
                  {c("agentRunStopping")}
                </button>
              )}
              {state.actionError && (
                <p className="collab-current-run__error" role="alert">{state.actionError}</p>
              )}
            </div>
          )}
          {(!state.currentRun || state.currentRun.phase === "idle") && (
            <div className="collab-current-run collab-current-run--idle" data-phase="idle">
              <div className="collab-current-run__status">
                <Circle size={10} fill="currentColor" />
                <span>{c("agentRunPhase_idle")}</span>
              </div>
            </div>
          )}
          <div className="collab-agent-identities">
            <button type="button" onClick={() => setProfileEditor("member")}>
              <CollaborationAvatar name={self.name} src={self.avatar} />
              <span><small>{c("memberIdentity")}</small><strong>{self.name}</strong></span><Pencil size={12} />
            </button>
            <button type="button" onClick={() => setProfileEditor("agent")}>
              <CollaborationAvatar name={self.agent.name} src={self.agent.avatar} agent />
              <span><small>{c("agentIdentity")}</small><strong>{self.agent.name}</strong></span><Pencil size={12} />
            </button>
          </div>
          <div className="collab-agent-model">
            <span>{c("agentModel")}</span>
            <ModelSwitcher label={modelLabel} tabId={tabID} onPick={onSwitchModel} />
          </div>
          <div className="collab-agent-approval">
            <span>{c("agentApproval")}</span>
            <div className="composer-modebar composer-modebar--approval" data-mode={state.toolApprovalMode || "ask"}>
              <span className="composer-modebar__thumb" aria-hidden="true" />
              <button type="button" className={`composer-modebar__item composer-modebar__item--ask${(state.toolApprovalMode || "ask") === "ask" ? " composer-modebar__item--active" : ""}`} onClick={() => handleAction(chooseApprovalMode("ask"))} disabled={approvalSaving} aria-pressed={(state.toolApprovalMode || "ask") === "ask"} title={t("composer.accessAskTitle")}><Shield size={14} /><span>{t("composer.modeAsk")}</span></button>
              <button type="button" className={`composer-modebar__item composer-modebar__item--auto${state.toolApprovalMode === "auto" ? " composer-modebar__item--active" : ""}`} onClick={() => handleAction(chooseApprovalMode("auto"))} disabled={approvalSaving} aria-pressed={state.toolApprovalMode === "auto"} title={t("composer.accessAutoTitle")}><ShieldCheck size={14} /><span>{t("composer.modeNormal")}</span></button>
              <button type="button" className={`composer-modebar__item composer-modebar__item--yolo${state.toolApprovalMode === "yolo" ? " composer-modebar__item--active" : ""}`} onClick={() => handleAction(chooseApprovalMode("yolo"))} disabled={approvalSaving} aria-pressed={state.toolApprovalMode === "yolo"} title={t("composer.accessYoloTitle")}><ShieldAlert size={14} /><span>{t("composer.modeYolo")}</span></button>
            </div>
          </div>
          <button type="button" className="collab-agent-context-trigger" onClick={() => setContextOpen(true)}><span>{c("agentContext")}</span><small>{c("agentContextCount", { n: (state.agentConfig.contextRefs || []).length })}</small><ChevronRight size={13} /></button>
          <div className="collab-agent-policy">
            <span className="collab-agent-section-title">{c("agentAutoResponse")}</span>
            <div className="collab-agent-options">
              <label title={c("autoQuestionsHint")}><span>{c("autoQuestionsShort")}</span><input type="checkbox" checked={state.agentConfig.autoRespondQuestions} disabled={configSaving} onChange={(event) => handleAction(saveAgentConfig({ ...state.agentConfig, autoRespondQuestions: event.target.checked }))} /></label>
              <label title={c("autoRequestsHint")}><span>{c("autoRequestsShort")}</span><input type="checkbox" checked={state.agentConfig.autoRespondRequests} disabled={configSaving} onChange={(event) => handleAction(saveAgentConfig({ ...state.agentConfig, autoRespondRequests: event.target.checked }))} /></label>
            </div>
            <label className="collab-agent-scan"><span>{c("recognitionMode")}</span><select value={state.agentConfig.recognitionMode} disabled={configSaving} onChange={(event) => handleAction(saveAgentConfig({ ...state.agentConfig, recognitionMode: event.target.value as typeof state.agentConfig.recognitionMode }))}><option value="message">{c("recognitionMessage")}</option><option value="interval">{c("recognitionInterval")}</option><option value="off">{c("recognitionOff")}</option></select></label>
            <div className={`collab-agent-peer-policy${agentCollaborationOpen ? " collab-agent-peer-policy--open" : ""}`}>
              <div className="collab-agent-peer-head">
                <button type="button" className="collab-agent-peer-summary" aria-expanded={agentCollaborationOpen} title={agentCollaborationOpen ? c("agentCollaborationCollapse") : c("agentCollaborationExpand")} onClick={() => setAgentCollaborationOpen((open) => !open)}><ChevronRight size={13} /><span><strong>{c("agentCollaboration")}</strong><small>{state.agentConfig.autoRespondAgents ? (agentClock.unlimited ? c("agentClockUnlimitedStatus") : c("agentClockRemaining", { remaining: agentClock.remaining, limit: agentClock.limit })) : c("agentCollaborationOff")}</small></span></button>
                <label className="collab-agent-peer-switch" title={c("agentCollaborationHint")}><input aria-label={c("agentCollaboration")} type="checkbox" checked={state.agentConfig.autoRespondAgents} disabled={configSaving} onChange={(event) => handleAction(saveAgentConfig({ ...state.agentConfig, autoRespondAgents: event.target.checked, agentClockWoundAt: new Date().toISOString() }))} /></label>
              </div>
              {agentCollaborationOpen && state.agentConfig.autoRespondAgents && <div className="collab-agent-peer-body">
                <p className="collab-agent-peer-hint">{c("agentCollaborationHint")}</p>
                <div className="collab-agent-clock-row"><small className="collab-agent-clock-status">{agentClock.unlimited ? c("agentClockUnlimitedStatus") : c("agentClockRemaining", { remaining: agentClock.remaining, limit: agentClock.limit })}</small><button type="button" disabled={configSaving} onClick={() => handleAction(windAgentClock())} title={c("agentClockWindHint")}><RefreshCw size={11} />{c("agentClockWind")}</button></div>
                <label className="collab-agent-peer-toggle" title={c("agentClockUnlimitedHint")}><span><strong>{c("agentClockUnlimited")}</strong><small>{c("agentClockUnlimitedHint")}</small></span><input type="checkbox" checked={state.agentConfig.agentClockUnlimited} disabled={configSaving} onChange={(event) => handleAction(saveAgentConfig({ ...state.agentConfig, agentClockUnlimited: event.target.checked, agentClockWoundAt: new Date().toISOString() }))} /></label>
                <label className="collab-agent-scan"><span>{c("agentResponseFrequency")}</span><select value={state.agentConfig.agentResponseIntervalSeconds} disabled={configSaving} onChange={(event) => handleAction(saveAgentConfig({ ...state.agentConfig, agentResponseIntervalSeconds: Number(event.target.value) }))}><option value={5}>{c("agentFrequency5")}</option><option value={15}>{c("agentFrequency15")}</option><option value={30}>{c("agentFrequency30")}</option><option value={60}>{c("agentFrequency60")}</option><option value={300}>{c("agentFrequency300")}</option></select></label>
                {!state.agentConfig.agentClockUnlimited && <label className="collab-agent-scan" title={c("agentClockHint")}><span>{c("agentClockTurns")}</span><input key={state.agentConfig.agentClockTurns} type="number" min={1} max={100} defaultValue={state.agentConfig.agentClockTurns} disabled={configSaving} onKeyDown={(event) => { if (event.key === "Enter") event.currentTarget.blur(); }} onBlur={(event) => { const turns = Math.min(100, Math.max(1, Number(event.currentTarget.value) || 12)); event.currentTarget.value = String(turns); if (turns !== state.agentConfig.agentClockTurns) handleAction(saveAgentConfig({ ...state.agentConfig, agentClockTurns: turns })); }} /></label>}
              </div>}
            </div>
          </div>
          <div className="collab-agent-queue">
            <div className="collab-agent-queue__header"><span>{c("agentQueue")}</span><small>{(state.queuedTasks || []).length}/20</small></div>
            {(state.queuedTasks || []).length === 0
              ? <p>{c("agentQueueEmpty")}</p>
              : <div className="collab-agent-queue__list">{(state.queuedTasks || []).map((task, index) => <div key={task.id} className="collab-agent-queue__item">
                <span>{index + 1}</span><strong title={task.instruction}>{task.instruction}</strong>
                <button type="button" aria-label={c("agentQueueCancel", { task: task.instruction })} title={c("agentQueueCancelShort")} onClick={() => handleAction(controller.cancelQueuedTask(task.id))}><X size={13} /></button>
              </div>)}</div>}
          </div>
        </section>}
        <section className="collab-member-section">
          <h2>{c("members")} <span>{onlineMembers.length}</span></h2>
          <div className="collab-member-list">
          {onlineMembers.map((member) => <article key={member.id} className={`collab-member${member.id === state.selfMemberId || member.isSelf ? " collab-member--self" : ""}`}>
            <CollaborationAvatar name={member.name} src={member.avatar} />
            <div><strong>{member.name}{member.id === state.selfMemberId || member.isSelf ? ` (${c("you")})` : ""}</strong><span>{member.agent.name}</span></div>
            <span
              className={`collab-member-status collab-member-status--${member.online ? member.agent.status : "offline"}${member.online && member.agent.status === "running" ? " collab-member-status--previewable" : ""}`}
              title={`${member.online ? c("online") : c("offline")} · ${agentStatusLabel(member.agent.status, c)}${member.online && member.agent.status === "running" ? ` · ${c("agentActivityHover")}` : ""}`}
              tabIndex={member.online && member.agent.status === "running" ? 0 : undefined}
              aria-describedby={activityAnchor?.memberId === member.id ? "collab-agent-activity-popover" : undefined}
              onMouseEnter={member.online && member.agent.status === "running" ? (event) => showAgentActivity(member.id, event.currentTarget) : undefined}
              onMouseLeave={member.online && member.agent.status === "running" ? () => setActivityAnchor(undefined) : undefined}
              onFocus={member.online && member.agent.status === "running" ? (event) => showAgentActivity(member.id, event.currentTarget) : undefined}
              onBlur={member.online && member.agent.status === "running" ? () => setActivityAnchor(undefined) : undefined}
            ><Circle size={8} fill="currentColor" />{member.online ? agentStatusLabel(member.agent.status, c) : c("offline")}</span>
            <ChevronRight size={15} aria-hidden="true" />
          </article>)}
          </div>
        </section>
      </aside>
    </div>
    {profileEditor && self && <div className="collab-config-modal" role="dialog" aria-modal="true" aria-label={profileEditor === "member" ? c("memberIdentity") : c("agentIdentity")}>
      <button type="button" className="collab-config-modal__backdrop" aria-label={c("close")} onClick={() => setProfileEditor(undefined)} />
      <form className="collab-config-dialog collab-profile-dialog" onSubmit={(event) => { event.preventDefault(); handleAction(saveProfile()); }}>
        <header><div><h3>{profileEditor === "member" ? c("memberIdentity") : c("agentIdentity")}</h3><p>{profileEditor === "member" ? c("memberIdentityHint") : c("agentIdentityHint")}</p></div><button type="button" onClick={() => setProfileEditor(undefined)} aria-label={c("close")}><X size={16} /></button></header>
        <div className="collab-profile-editor">
          <CollaborationAvatar name={profileName} src={profileAvatar} agent={profileEditor === "agent"} className="collab-avatar--editor" />
          <div><label className="collab-profile-upload"><ImagePlus size={14} />{c("chooseAvatar")}<input type="file" accept="image/png,image/jpeg,image/webp" onChange={(event) => { void chooseAvatar(event.target.files?.[0]); event.currentTarget.value = ""; }} /></label>{profileAvatar && <button type="button" className="collab-profile-remove" onClick={() => setProfileAvatar("")}><Trash2 size={13} />{c("removeAvatar")}</button>}</div>
        </div>
        <label className="collab-profile-name"><span>{profileEditor === "member" ? c("memberName") : c("agentName")}</span><input autoFocus value={profileName} maxLength={256} disabled={profileSaving} onChange={(event) => setProfileName(event.target.value)} /></label>
        {profileError && <p className="collab-config-error" role="alert">{profileError}</p>}
        <footer><button type="button" onClick={() => setProfileEditor(undefined)}>{c("cancel")}</button><button type="submit" className="collab-primary-button" disabled={profileSaving || !profileName.trim()}>{profileSaving ? c("saving") : c("save")}</button></footer>
      </form>
    </div>}
    {contextOpen && <div className="collab-config-modal" role="dialog" aria-modal="true" aria-label={c("agentContext")}>
      <button type="button" className="collab-config-modal__backdrop" aria-label={c("close")} onClick={() => setContextOpen(false)} />
      <section className="collab-config-dialog collab-context-dialog">
        <header><div><h3>{c("agentContext")}</h3><p>{c("agentContextHint")}</p></div><button type="button" onClick={() => setContextOpen(false)} aria-label={c("close")}><X size={16} /></button></header>
        <div className="collab-agent-context__group"><strong>AGENTS.md</strong>{(state.agentSources?.agents || []).length === 0 ? <small>{c("agentContextNoAgents")}</small> : (state.agentSources?.agents || []).map((source) => <label key={source.id} title={source.path}><input type="checkbox" checked={(state.agentConfig.contextRefs || []).includes(source.id)} disabled={configSaving} onChange={(event) => handleAction(toggleContextRef(source.id, event.target.checked))} /><span><b>{source.name}{!source.available ? ` · ${c("agentContextUnavailable")}` : ""}</b><small>{source.path}</small></span></label>)}</div>
        <div className="collab-agent-context__group"><strong>SKILL.md</strong>{(state.agentSources?.skills || []).length === 0 ? <small>{c("agentContextNoSkills")}</small> : (state.agentSources?.skills || []).map((source) => <label key={source.id} title={source.path}><input type="checkbox" checked={(state.agentConfig.contextRefs || []).includes(source.id)} disabled={configSaving} onChange={(event) => handleAction(toggleContextRef(source.id, event.target.checked))} /><span><b>{source.name}{!source.available ? ` · ${c("agentContextUnavailable")}` : ""}</b><small>{source.description || source.path}{source.runAs ? ` · ${source.runAs}` : ""}</small></span></label>)}</div>
        <footer><span>{c("agentContextCount", { n: (state.agentConfig.contextRefs || []).length })}</span><button type="button" className="collab-primary-button" onClick={() => setContextOpen(false)}>{c("done")}</button></footer>
      </section>
    </div>}
    {activityAnchor && activityMember && <AgentActivityPopover member={activityMember} timeline={state.timeline} anchor={activityAnchor} c={c} />}
  </section>;
}
