import { useState } from "react";
import { Bot, ChevronRight, Circle, DoorOpen, LogOut, MessageSquare, RefreshCw, Users, X } from "lucide-react";
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
  onClose(): void;
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

export function CollaborationWorkspace({ sessionID, onClose }: CollaborationWorkspaceProps) {
  const { t } = useI18n();
  const c = collabCopy(t);
  const controller = useCollabController(sessionID);
  const { state, self, selectedItems } = controller;
  const [prefill, setPrefill] = useState("");
  const [batchInstruction, setBatchInstruction] = useState("");
  const connected = state.status === "connected";

  if (!state.room || state.status === "disconnected") {
    return <section className="collab-surface" aria-label={c("title")}>
      <ConnectionPanel sessionID={sessionID} status={state.status} error={state.lastError} initial={state.room} onHost={controller.host} onJoin={controller.join} onClose={onClose} />
    </section>;
  }

  const runForItem = (item: CollaborationTimelineItem) => {
    const instruction = c("agentInstructionReference", { text: item.text });
    void controller.startAgent(instruction, [item.id]);
  };
  const runSelected = () => {
    const instruction = batchInstruction.trim() || c("agentInstructionBatch");
    void controller.startAgent(instruction, selectedItems.map((item) => item.id)).then(() => {
      setBatchInstruction("");
      controller.clearSelection();
    });
  };

  return <section className="collab-surface" aria-label={c("title")}>
    <div className="collab-workspace">
      <aside className="collab-room-rail">
        <div className="collab-brand"><span>W2</span><strong>WorkGround2</strong></div>
        <div className="collab-rail-heading">{c("rooms")}</div>
        <button className="collab-room-item collab-room-item--active" type="button">
          <MessageSquare size={16} /><span><strong>{state.room.title || state.room.room}</strong><small>{state.room.description || `${state.room.host}:${state.room.port}`}</small></span>
        </button>
        <div className="collab-room-meta"><span>{state.room.host}:{state.room.port}</span><span>#{state.room.room}</span></div>
        <div className="collab-rail-spacer" />
        <button type="button" className="collab-rail-action" onClick={() => void controller.leave()}><LogOut size={15} />{c("leave")}</button>
        <button type="button" className="collab-rail-action" onClick={onClose}><DoorOpen size={15} />{c("close")}</button>
      </aside>

      <main className="collab-main">
        <header className="collab-topicbar">
          <div><div className="collab-topic-title"><h1>{state.room.title || state.room.room}</h1><span><Users size={14} />{state.members.length}</span></div><p>{state.room.description || c("subtitle")}</p></div>
          <div className={`collab-connection collab-connection--${state.status}`}><Circle size={9} fill="currentColor" />{statusLabel(state.status, c)}</div>
          <button type="button" className="collab-icon-button" aria-label={c("close")} title={c("close")} onClick={onClose}><X size={18} /></button>
        </header>

        {(state.status === "syncing" || state.status === "reconnecting" || state.status === "failed" || Boolean(state.unsyncedCount)) && <div className={`collab-status-banner collab-status-banner--${state.status}`} role="status">
          <RefreshCw size={14} />
          <span>{state.lastError || statusLabel(state.status, c)}{state.unsyncedCount ? ` · ${c("unsynced", { n: state.unsyncedCount })}` : ""}</span>
          {state.retryable && <button type="button" onClick={() => void controller.refresh(true)}>{c("retry")}</button>}
        </div>}

        <div className="collab-scroll" aria-label={c("timeline")}>
          <CollaborationTimeline
            items={state.timeline}
            selfMemberId={state.selfMemberId}
            selectedIds={state.selectedIds}
            pendingIntents={state.pendingIntents}
            connected={connected}
            onToggle={controller.toggleSelection}
            onReply={(item) => setPrefill(`> ${item.actorName}: ${item.text}\n\n`)}
            onAgree={(item) => void controller.agree(item)}
            onAgreeRun={(item) => void controller.agree(item).then(() => controller.startAgent(c("agentInstructionAgree", { text: item.text }), [item.id]))}
            onAgent={runForItem}
            onAccept={(item) => void controller.acceptRequest(item)}
            onReject={(item) => void controller.rejectRequest(item)}
            onStartPending={(intent) => void controller.startPending(intent)}
            onStopPending={controller.stopPending}
            onEditPending={controller.editPending}
          />
        </div>

        <footer className="collab-footer">
          {selectedItems.length > 0 && <div className="collab-selection-bar">
            <span>{c("selected", { n: selectedItems.length })}</span>
            <input value={batchInstruction} onChange={(event) => setBatchInstruction(event.target.value)} placeholder={c("instruction")} aria-label={c("instruction")} />
            <button type="button" className="collab-primary-button" disabled={!connected || !sessionID} onClick={runSelected}><Bot size={15} />{c("agentRespond")}</button>
            <button type="button" className="collab-icon-button" aria-label={c("clear")} onClick={controller.clearSelection}><X size={16} /></button>
          </div>}
          <CollaborationComposer
            members={state.members}
            selfMemberId={state.selfMemberId}
            disabled={!connected}
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
