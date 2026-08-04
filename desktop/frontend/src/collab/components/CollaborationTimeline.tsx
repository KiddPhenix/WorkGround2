import { Ban, Bot, Check, CircleAlert, Download, File, MoreHorizontal, Pause, Play, RefreshCw, Reply, ThumbsUp, UserRound } from "lucide-react";
import { useI18n } from "../../lib/i18n";
import { collabCopy, contributionLabel, type CollabCopy } from "../copy";
import type { CollaborationFileTransfer, CollaborationTimelineItem, PendingIntent } from "../types";
import { IntentCountdown } from "./IntentCountdown";

interface CollaborationTimelineProps {
  items: CollaborationTimelineItem[];
  selfMemberId?: string;
  selectedIds: string[];
  pendingIntents: Record<string, PendingIntent>;
  connected: boolean;
  agentBusy: boolean;
  transfers: CollaborationFileTransfer[];
  onToggle(id: string): void;
  onReply(item: CollaborationTimelineItem): void;
  onAgree(item: CollaborationTimelineItem): void;
  onAgreeRun(item: CollaborationTimelineItem): void;
  onAgent(item: CollaborationTimelineItem): void;
  onAccept(item: CollaborationTimelineItem): void;
  onReject(item: CollaborationTimelineItem): void;
  onStartPending(intent: PendingIntent): void;
  onStopPending(id: string): void;
  onEditPending(id: string, instruction: string): void;
  onReceiveFile(id: string): void;
  onPauseFile(id: string): void;
  onResumeFile(id: string): void;
  onRevokeFile(id: string): void;
}

const kindCopy = {
  chat: "kindChat",
  contribution: "kindContribution",
  agent_command: "kindCommand",
  agent_request: "kindRequest",
  agent_result: "kindResult",
  file: "kindFile",
  reaction: "kindReaction",
  system: "kindSystem",
} as const;

function fileSize(value = 0): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}

function fileStatus(status: CollaborationFileTransfer["status"] | undefined, c: CollabCopy): string {
  if (status === "preparing") return c("filePreparing");
  if (status === "pending") return c("pending");
  if (status === "available") return c("fileAvailable");
  if (status === "unavailable" || status === "waiting_sender") return c("fileWaitingOwner");
  if (status === "source_changed") return c("fileSourceChanged");
  if (status === "revoked") return c("fileRevoked");
  if (status === "negotiating") return c("fileNegotiating");
  if (status === "downloading") return c("fileReceiving");
  if (status === "paused") return c("filePaused");
  if (status === "verifying") return c("fileVerifying");
  if (status === "completed") return c("fileCompleted");
  if (status === "failed") return c("fileFailed");
  return c("fileAvailable");
}

function FileCard({ item, own, transfer, c, onReceive, onPause, onResume, onRevoke }: { item: CollaborationTimelineItem; own: boolean; transfer?: CollaborationFileTransfer; c: CollabCopy; onReceive(): void; onPause(): void; onResume(): void; onRevoke(): void }) {
  const revoked = item.fileRevoked || transfer?.status === "revoked";
  const receiving = transfer?.direction === "receive";
  const progress = transfer?.total ? Math.min(100, Math.round(transfer.transferred / transfer.total * 100)) : 0;
  const resumable = receiving && ["paused", "waiting_sender", "failed"].includes(transfer?.status || "") && transfer?.retryable !== false;
  return <div className={`collab-file-card${revoked ? " collab-file-card--revoked" : ""}`}>
    <div className="collab-file-card__icon"><File size={21} /></div>
    <div className="collab-file-card__body"><strong>{item.fileName || item.text}</strong><span>{fileSize(item.fileSize)} · {fileStatus(revoked ? "revoked" : transfer?.status, c)}</span>{transfer?.error && <small>{transfer.error}</small>}</div>
    <div className="collab-file-card__actions">
      {!own && !revoked && !receiving && <button type="button" onClick={onReceive}><Download size={14} />{c("fileReceive")}</button>}
      {receiving && transfer.status === "downloading" && <button type="button" onClick={onPause}><Pause size={14} />{c("filePause")}</button>}
      {resumable && <button type="button" onClick={onResume}><Play size={14} />{c("fileResume")}</button>}
      {own && !revoked && <button type="button" onClick={onRevoke}><Ban size={14} />{c("fileRevoke")}</button>}
    </div>
    {receiving && transfer.status !== "completed" && <div className="collab-file-progress"><span style={{ width: `${progress}%` }} /></div>}
  </div>;
}

function timeLabel(value: string, locale: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "" : new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(date);
}

function presenceLabel(item: CollaborationTimelineItem, c: CollabCopy): string | undefined {
  if (item.kind !== "system") return undefined;
  const name = item.actorName;
  if (item.systemKind === "member.joined") return c("memberJoined", { name });
  if (item.systemKind === "member.rejoined") return c("memberRejoined", { name });
  if (item.systemKind === "member.left") return c("memberLeft", { name });
  return undefined;
}

function runStatusLabel(status: CollaborationTimelineItem["agentRunStatus"], c: CollabCopy): string {
  if (status === "queued") return c("queued");
  if (status === "waiting_approval") return c("waiting");
  if (status === "completed") return c("completed");
  if (status === "failed") return c("runFailed");
  if (status === "cancelled") return c("cancelled");
  if (status === "interrupted") return c("interrupted");
  return c("running");
}

function AgentRunCard({ item, c }: { item: CollaborationTimelineItem; c: CollabCopy }) {
  const status = item.agentRunStatus || "running";
  const active = status === "queued" || status === "running" || status === "waiting_approval";
  return <div className={`collab-agent-run collab-agent-run--${status}${active ? " collab-agent-run--active" : ""}`}>
    <div className="collab-agent-run__head"><span className="collab-agent-run__pulse" aria-hidden="true" /><strong>{runStatusLabel(status, c)}</strong></div>
    <p>{item.text}</p>
    {active
      ? <div className="collab-agent-run__marquee" aria-label={runStatusLabel(status, c)}><div>
          <span>{c("agentStageContext")}</span><span>{c("agentStageTools")}</span><span>{c("agentStageShare")}</span><span>{c("agentStageContext")}</span>
        </div></div>
      : <div className="collab-agent-run__summary">{item.agentRunError || item.agentRunSummary || runStatusLabel(status, c)}</div>}
  </div>;
}

export function CollaborationTimeline(props: CollaborationTimelineProps) {
  const { locale, t } = useI18n();
  const c = collabCopy(t);
  if (props.items.length === 0) return <div className="collab-empty">{c("empty")}</div>;

  return <div className="collab-timeline-list">
    {props.items.map((item) => {
      const presence = presenceLabel(item, c);
      if (presence) return <div key={item.id} className="collab-presence-notice" role="status"><span aria-hidden="true" />{presence}<time dateTime={item.createdAt}>{timeLabel(item.createdAt, locale)}</time></div>;

      const own = item.actorId === props.selfMemberId;
      const selected = props.selectedIds.includes(item.id);
      const pending = props.pendingIntents[item.id];
      const transfer = props.transfers.find((value) => value.fileId === item.id && value.direction === (own ? "share" : "receive"));
      const incomingRequest = item.kind === "agent_request" && item.targetMemberId === props.selfMemberId && item.requestStatus !== "accepted" && item.requestStatus !== "rejected";
      return (
        <article key={item.id} className={`collab-message collab-message--${item.kind}${selected ? " collab-message--selected" : ""}${item.localPending ? " collab-message--pending" : ""}`}>
          <div className={`collab-avatar${item.actorAgent ? " collab-avatar--agent" : ""}`}>{item.actorAgent ? <Bot size={17} /> : item.actorName.slice(0, 1)}</div>
          <div className="collab-message-body">
            <header>
              <strong>{item.actorName}{own ? ` (${c("you")})` : ""}</strong>
              <span className={`collab-kind collab-kind--${item.kind}`}>{item.kind === "contribution" ? contributionLabel(c, item.contributionKind) : c(kindCopy[item.kind])}</span>
              <time dateTime={item.createdAt}>{timeLabel(item.createdAt, locale)}</time>
              {item.syncStatus === "pending" && <span className="collab-sync collab-sync--pending"><RefreshCw size={11} />{c("pending")}</span>}
              {item.syncStatus === "failed" && <span className="collab-sync collab-sync--failed"><CircleAlert size={11} />{c("failedItem")}</span>}
            </header>
            {item.kind === "agent_command" ? <AgentRunCard item={item} c={c} /> : item.kind === "file" ? <FileCard item={item} own={own} transfer={transfer} c={c} onReceive={() => props.onReceiveFile(item.id)} onPause={() => props.onPauseFile(item.id)} onResume={() => props.onResumeFile(item.id)} onRevoke={() => props.onRevokeFile(item.id)} /> : <p>{item.text}</p>}
            {item.referenceIds.length > 0 && <div className="collab-references"><Reply size={12} />{c("references", { n: item.referenceIds.length })}</div>}
            {item.kind === "agent_request" && !incomingRequest && <div className="collab-request-state">{c("waitingOwner")}</div>}
            {!item.localPending && <div className="collab-message-actions">
              <label className="collab-message-select">
                <input type="checkbox" checked={selected} onChange={() => props.onToggle(item.id)} aria-label={`${c("agentRespond")}: ${item.actorName}`} />
                <span><Check size={14} /></span>
              </label>
              <button type="button" aria-label={c("reply")} title={c("reply")} onClick={() => props.onReply(item)}><Reply size={14} /><span>{c("reply")}</span></button>
              <button type="button" aria-label={c("agree")} title={c("agree")} onClick={() => props.onAgree(item)}><ThumbsUp size={14} /><span>{c("agree")}</span></button>
              <button type="button" aria-label={c("agentRespond")} title={props.agentBusy ? c("agentBusy") : c("agentRespond")} disabled={props.agentBusy} onClick={() => props.onAgent(item)}><Bot size={14} /><span>{c("agentRespond")}</span></button>
              <details className="collab-action-more">
                <summary aria-label={c("moreActions")} title={c("moreActions")}><MoreHorizontal size={15} /></summary>
                <div><button type="button" disabled={props.agentBusy} title={props.agentBusy ? c("agentBusy") : undefined} onClick={() => props.onAgreeRun(item)}><Bot size={13} />{c("agreeRun")}</button></div>
              </details>
            </div>}
            {incomingRequest && <div className="collab-request-actions">
              <button type="button" className="collab-action-accent" disabled={props.agentBusy} title={props.agentBusy ? c("agentBusy") : undefined} onClick={() => props.onAccept(item)}><UserRound size={13} />{c("acceptRun")}</button>
              <button type="button" disabled={props.agentBusy} title={props.agentBusy ? c("agentBusy") : undefined} onClick={() => {
                const next = window.prompt(c("modifyAccept"), item.text);
                if (next?.trim()) props.onAccept({ ...item, text: next.trim() });
              }}>{c("modifyAccept")}</button>
              <button type="button" onClick={() => props.onReject(item)}>{c("reject")}</button>
            </div>}
            {pending && <IntentCountdown intent={pending} connected={props.connected} onStart={props.onStartPending} onStop={() => props.onStopPending(item.id)} onEdit={(instruction) => props.onEditPending(item.id, instruction)} />}
          </div>
        </article>
      );
    })}
  </div>;
}
