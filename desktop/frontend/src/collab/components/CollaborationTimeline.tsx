import { Bot, Check, CircleAlert, MoreHorizontal, RefreshCw, Reply, ThumbsUp, UserRound } from "lucide-react";
import { useI18n } from "../../lib/i18n";
import { collabCopy, contributionLabel, type CollabCopy } from "../copy";
import type { CollaborationTimelineItem, PendingIntent } from "../types";
import { IntentCountdown } from "./IntentCountdown";

interface CollaborationTimelineProps {
  items: CollaborationTimelineItem[];
  selfMemberId?: string;
  selectedIds: string[];
  pendingIntents: Record<string, PendingIntent>;
  connected: boolean;
  agentBusy: boolean;
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
}

const kindCopy = {
  chat: "kindChat",
  contribution: "kindContribution",
  agent_command: "kindCommand",
  agent_request: "kindRequest",
  agent_result: "kindResult",
  reaction: "kindReaction",
  system: "kindSystem",
} as const;

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
      const incomingRequest = item.kind === "agent_request" && item.targetMemberId === props.selfMemberId && item.requestStatus !== "accepted" && item.requestStatus !== "rejected";
      return (
        <article key={item.id} className={`collab-message collab-message--${item.kind}${selected ? " collab-message--selected" : ""}`}>
          <label className="collab-message-select">
            <input type="checkbox" checked={selected} onChange={() => props.onToggle(item.id)} aria-label={`${c("agentRespond")}: ${item.actorName}`} />
            <span><Check size={11} /></span>
          </label>
          <div className={`collab-avatar${item.actorAgent ? " collab-avatar--agent" : ""}`}>{item.actorAgent ? <Bot size={17} /> : item.actorName.slice(0, 1)}</div>
          <div className="collab-message-body">
            <header>
              <strong>{item.actorName}{own ? ` (${c("you")})` : ""}</strong>
              <span className={`collab-kind collab-kind--${item.kind}`}>{item.kind === "contribution" ? contributionLabel(c, item.contributionKind) : c(kindCopy[item.kind])}</span>
              <time dateTime={item.createdAt}>{timeLabel(item.createdAt, locale)}</time>
              {item.syncStatus === "pending" && <span className="collab-sync collab-sync--pending"><RefreshCw size={11} />{c("pending")}</span>}
              {item.syncStatus === "failed" && <span className="collab-sync collab-sync--failed"><CircleAlert size={11} />{c("failedItem")}</span>}
            </header>
            {item.kind === "agent_command" ? <AgentRunCard item={item} c={c} /> : <p>{item.text}</p>}
            {item.referenceIds.length > 0 && <div className="collab-references"><Reply size={12} />{c("references", { n: item.referenceIds.length })}</div>}
            {item.kind === "agent_request" && !incomingRequest && <div className="collab-request-state">{c("waitingOwner")}</div>}
            <div className="collab-message-actions">
              <button type="button" aria-label={c("reply")} title={c("reply")} onClick={() => props.onReply(item)}><Reply size={14} /><span>{c("reply")}</span></button>
              <button type="button" aria-label={c("agree")} title={c("agree")} onClick={() => props.onAgree(item)}><ThumbsUp size={14} /><span>{c("agree")}</span></button>
              <button type="button" aria-label={c("agentRespond")} title={props.agentBusy ? c("agentBusy") : c("agentRespond")} disabled={props.agentBusy} onClick={() => props.onAgent(item)}><Bot size={14} /><span>{c("agentRespond")}</span></button>
              <details className="collab-action-more">
                <summary aria-label={c("moreActions")} title={c("moreActions")}><MoreHorizontal size={15} /></summary>
                <div><button type="button" disabled={props.agentBusy} title={props.agentBusy ? c("agentBusy") : undefined} onClick={() => props.onAgreeRun(item)}><Bot size={13} />{c("agreeRun")}</button></div>
              </details>
            </div>
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
