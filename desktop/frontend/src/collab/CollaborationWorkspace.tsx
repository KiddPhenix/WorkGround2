import { useState } from "react";
import { Bot, ChevronRight, Circle, CircleAlert, LogOut, RefreshCw, Users, X } from "lucide-react";
import { useI18n } from "../lib/i18n";
import { collabCopy } from "./copy";
import { useCollabController } from "./useCollabController";
import type { CollaborationTimelineItem } from "./types";
import { CollaborationComposer } from "./components/CollaborationComposer";
import { ConnectionPanel } from "./components/ConnectionPanel";
import { CollaborationTimeline } from "./components/CollaborationTimeline";
import "./collab.css";

interface CollaborationWorkspaceProps {
  sessionID: string;
  mode?: "session" | "dialog";
  onClose?(): void;
  onConnected?(): Promise<void> | void;
  onConnectRequest?(): void;
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

export function CollaborationWorkspace({ sessionID, mode = "session", onClose, onConnected, onConnectRequest }: CollaborationWorkspaceProps) {
  const { t } = useI18n();
  const c = collabCopy(t);
  const controller = useCollabController(sessionID);
  const { state, self, agentBusy, selectedItems } = controller;
  const [prefill, setPrefill] = useState("");
  const [batchInstruction, setBatchInstruction] = useState("");
  const ownsRoom = Boolean(sessionID) && state.selfSessionId === sessionID;
  const usable = ownsRoom && Boolean(state.room);

  if (mode === "dialog") {
    return <div className="collab-modal" role="dialog" aria-modal="true" aria-label={c("title")}>
      <div className="collab-modal__backdrop" onClick={onClose} />
      <section className="collab-surface collab-surface--dialog">
        <ConnectionPanel
          sessionID={sessionID}
          status={state.status}
          error={state.lastError}
          initial={ownsRoom ? state.room : undefined}
          onHost={controller.host}
          onJoin={controller.join}
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

  return <section className="collab-surface" aria-label={c("title")}>
    <div className="collab-workspace">
      <main className="collab-main">
        <header className="collab-topicbar">
          <div><div className="collab-topic-title"><h1>{state.room.title || state.room.room}</h1><span><Users size={14} />{state.members.length}</span></div><p>{state.room.description || c("subtitle")}</p></div>
          <div className={`collab-connection collab-connection--${state.status}`}><Circle size={9} fill="currentColor" />{statusLabel(state.status, c)}</div>
          <button type="button" className="collab-icon-button" aria-label={c("leave")} title={c("leave")} onClick={() => void controller.leave()}><LogOut size={17} /></button>
        </header>

        {(state.actionError || state.status === "syncing" || state.status === "reconnecting" || state.status === "failed" || Boolean(state.unsyncedCount)) && <div className={`collab-status-banner collab-status-banner--${state.actionError ? "action" : state.status}`} role="status">
          {state.actionError ? <CircleAlert size={14} /> : <RefreshCw size={14} />}
          <span>{state.actionError || state.lastError || statusLabel(state.status, c)}{!state.actionError && (state.status === "reconnecting" || state.status === "failed") ? ` · ${c("cachedBackground")}` : ""}{!state.actionError && state.unsyncedCount ? ` · ${c("unsynced", { n: state.unsyncedCount })}` : ""}</span>
          {!state.actionError && state.retryable && <button type="button" onClick={() => void controller.refresh(true)}>{c("retry")}</button>}
        </div>}

        <div className="collab-scroll" aria-label={c("timeline")}>
          <CollaborationTimeline
            items={state.timeline}
            selfMemberId={state.selfMemberId}
            selectedIds={state.selectedIds}
            pendingIntents={state.pendingIntents}
            connected={usable && !agentBusy}
            agentBusy={agentBusy}
            onToggle={controller.toggleSelection}
            onReply={(item) => setPrefill(`> ${item.actorName}: ${item.text}\n\n`)}
            onAgree={(item) => handleAction(controller.agree(item))}
            onAgreeRun={(item) => handleAction(controller.agree(item).then(() => controller.startAgent(c("agentInstructionAgree", { text: item.text }), [item.id])))}
            onAgent={runForItem}
            onAccept={(item) => handleAction(controller.acceptRequest(item))}
            onReject={(item) => handleAction(controller.rejectRequest(item))}
            onStartPending={(intent) => void controller.startPending(intent)}
            onStopPending={controller.stopPending}
            onEditPending={controller.editPending}
          />
        </div>

        <footer className="collab-footer">
          {selectedItems.length > 0 && <div className="collab-selection-bar">
            <span>{c("selected", { n: selectedItems.length })}</span>
            <input value={batchInstruction} onChange={(event) => setBatchInstruction(event.target.value)} placeholder={c("instruction")} aria-label={c("instruction")} />
            <button type="button" className="collab-primary-button" disabled={!usable || !sessionID || agentBusy} title={agentBusy ? c("agentBusy") : undefined} onClick={runSelected}><Bot size={15} />{c("agentRespond")}</button>
            <button type="button" className="collab-icon-button" aria-label={c("clear")} onClick={controller.clearSelection}><X size={16} /></button>
          </div>}
          <CollaborationComposer
            members={state.members}
            selfMemberId={state.selfMemberId}
            disabled={!usable}
            agentBusy={agentBusy}
            prefill={prefill}
            onPrefillConsumed={() => setPrefill("")}
            onChat={controller.postChat}
            onContribution={controller.postContribution}
            onAgent={(text) => controller.startAgent(text, [])}
            onRequest={(memberId, text) => controller.requestAgent(memberId, text)}
          />
        </footer>
      </main>

      <aside className="collab-members">
        <h2>{c("members")}</h2>
        <div className="collab-member-list">
          {state.members.map((member) => <article key={member.id} className={`collab-member${member.id === state.selfMemberId || member.isSelf ? " collab-member--self" : ""}`}>
            <div className="collab-avatar">{member.name.slice(0, 1)}</div>
            <div><strong>{member.name}{member.id === state.selfMemberId || member.isSelf ? ` (${c("you")})` : ""}</strong><span>{member.agent.name}</span></div>
            <span className={`collab-member-status collab-member-status--${member.online ? member.agent.status : "offline"}`} title={`${member.online ? c("online") : c("offline")} · ${agentStatusLabel(member.agent.status, c)}`}><Circle size={8} fill="currentColor" />{member.online ? agentStatusLabel(member.agent.status, c) : c("offline")}</span>
            <ChevronRight size={15} aria-hidden="true" />
          </article>)}
        </div>
        {self && <div className="collab-owner-note"><Bot size={15} /><span><strong>{self.agent.name}</strong>{c("subtitle")}</span></div>}
      </aside>
    </div>
  </section>;
}
