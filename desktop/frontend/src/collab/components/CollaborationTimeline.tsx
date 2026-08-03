import { Bot, Check, CircleAlert, RefreshCw, Reply, ThumbsUp, UserRound } from "lucide-react";
import { useI18n } from "../../lib/i18n";
import { collabCopy, contributionLabel } from "../copy";
import { detectSelfAgentIntent } from "../state";
import type { CollaborationTimelineItem, PendingIntent } from "../types";
import { IntentCountdown } from "./IntentCountdown";

interface CollaborationTimelineProps {
  items: CollaborationTimelineItem[];
  selfMemberId?: string;
  selectedIds: string[];
  pendingIntents: Record<string, PendingIntent>;
  connected: boolean;
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

export function CollaborationTimeline(props: CollaborationTimelineProps) {
  const { locale, t } = useI18n();
  const c = collabCopy(t);
  if (props.items.length === 0) return <div className="collab-empty">{c("empty")}</div>;

  return <div className="collab-timeline-list">
    {props.items.map((item) => {
      const own = item.actorId === props.selfMemberId;
      const selected = props.selectedIds.includes(item.id);
      const pending = props.pendingIntents[item.id];
      const uncertain = own && item.kind === "chat" && !pending && detectSelfAgentIntent(item.text) === "uncertain";
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
            <p>{item.text}</p>
            {item.referenceIds.length > 0 && <div className="collab-references"><Reply size={12} />{c("references", { n: item.referenceIds.length })}</div>}
            {item.kind === "agent_request" && !incomingRequest && <div className="collab-request-state">{c("waitingOwner")}</div>}
            <div className="collab-message-actions">
              <button type="button" onClick={() => props.onReply(item)}><Reply size={13} />{c("reply")}</button>
              <button type="button" onClick={() => props.onAgree(item)}><ThumbsUp size={13} />{c("agree")}</button>
              <button type="button" onClick={() => props.onAgreeRun(item)}><Bot size={13} />{c("agreeRun")}</button>
              <button type="button" onClick={() => props.onAgent(item)}><Bot size={13} />{c("agentRespond")}</button>
              {incomingRequest && <>
                <button type="button" className="collab-action-accent" onClick={() => props.onAccept(item)}><UserRound size={13} />{c("acceptRun")}</button>
                <button type="button" onClick={() => {
                  const next = window.prompt(c("modifyAccept"), item.text);
                  if (next?.trim()) props.onAccept({ ...item, text: next.trim() });
                }}>{c("modifyAccept")}</button>
                <button type="button" onClick={() => props.onReject(item)}>{c("reject")}</button>
              </>}
            </div>
            {uncertain && <button type="button" className="collab-suggestion" onClick={() => props.onAgent(item)}><Bot size={13} />{c("uncertain")} · {c("agentRespond")}</button>}
            {pending && <IntentCountdown intent={pending} connected={props.connected} onStart={props.onStartPending} onStop={() => props.onStopPending(item.id)} onEdit={(instruction) => props.onEditPending(item.id, instruction)} />}
          </div>
        </article>
      );
    })}
  </div>;
}
