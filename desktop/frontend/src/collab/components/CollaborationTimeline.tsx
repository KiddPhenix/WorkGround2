import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import { Ban, Bot, Check, ChevronDown, ChevronUp, CircleAlert, Download, ExternalLink, File, FolderOpen, MoreHorizontal, Pause, Play, RefreshCw, Reply, ThumbsUp, UserRound } from "lucide-react";
import { useI18n } from "../../lib/i18n";
import { ApprovalModal } from "../../components/ApprovalModal";
import { AskCard } from "../../components/AskCard";
import { collabCopy, contributionLabel, type CollabCopy } from "../copy";
import { visibleCollaborationTimeline } from "../state";
import type { CollaborationAgentPrompt, CollaborationAgentRunResponse, CollaborationFileTransfer, CollaborationMember, CollaborationTimelineItem, PendingIntent } from "../types";
import { IntentCountdown } from "./IntentCountdown";
import { CollaborationAvatar } from "./CollaborationAvatar";

interface CollaborationTimelineProps {
  items: CollaborationTimelineItem[];
  members?: CollaborationMember[];
  selfMemberId?: string;
  selectedIds: string[];
  pendingIntents: Record<string, PendingIntent>;
  connected: boolean;
  agentBusy: boolean;
  transfers: CollaborationFileTransfer[];
  agentPrompt?: CollaborationAgentPrompt;
  onToggle(id: string): void;
  onReply(item: CollaborationTimelineItem): void;
  onAgree(item: CollaborationTimelineItem): void;
  onRequestAgent(item: CollaborationTimelineItem, memberId: string): void;
  onAgent(item: CollaborationTimelineItem): void;
  onAccept(item: CollaborationTimelineItem): void;
  onReject(item: CollaborationTimelineItem): void;
  onRespondAgentRun(item: CollaborationTimelineItem, response: CollaborationAgentRunResponse): void;
  onStartPending(intent: PendingIntent): void;
  onStopPending(id: string): void;
  onEditPending(id: string, instruction: string): void;
  onReceiveFile(id: string): void;
  onPauseFile(id: string): void;
  onResumeFile(id: string): void;
  onRevokeFile(id: string): void;
  onOpenFile(id: string): void;
  onRevealFile(id: string): void;
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

function FileCard({ item, own, transfer, c, onReceive, onPause, onResume, onRevoke, onOpen, onReveal }: { item: CollaborationTimelineItem; own: boolean; transfer?: CollaborationFileTransfer; c: CollabCopy; onReceive(): void; onPause(): void; onResume(): void; onRevoke(): void; onOpen(): void; onReveal(): void }) {
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
      {receiving && transfer.status === "completed" && <><button type="button" className="collab-file-card__open" onClick={onOpen}><ExternalLink size={14} />{c("fileOpen")}</button><details className="collab-file-card__more"><summary aria-label={c("moreActions")} title={c("moreActions")}><MoreHorizontal size={15} /></summary><div><button type="button" onClick={onReveal}><FolderOpen size={14} />{c("fileReveal")}</button></div></details></>}
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

function AgentRunCard({ item, c, canRespond, prompt, onRespond }: { item: CollaborationTimelineItem; c: CollabCopy; canRespond: boolean; prompt?: CollaborationAgentPrompt; onRespond(response: CollaborationAgentRunResponse): void }) {
  const status = item.agentRunStatus || "running";
  const active = status === "queued" || status === "running" || status === "waiting_approval";
  const hasOutput = !active && Boolean(item.agentRunOutput);
  const detailed = canRespond && prompt?.runId === item.id;
  return <div className={`collab-agent-run-stack${detailed ? " collab-agent-run-stack--prompt" : ""}`}>
    <div className={`collab-agent-run collab-agent-run--${status}${active ? " collab-agent-run--active" : ""}${hasOutput ? " collab-agent-run--has-output" : ""}`}>
      <div className="collab-agent-run__head"><span className="collab-agent-run__pulse" aria-hidden="true" /><strong>{runStatusLabel(status, c)}</strong>{canRespond && !detailed && <span className="collab-agent-run__decision"><button type="button" className="collab-agent-run__allow" onClick={() => onRespond({ allow: true })}><Check size={12} />{c("agree")}</button><button type="button" onClick={() => onRespond({ allow: false })}><Ban size={12} />{c("reject")}</button></span>}</div>
      {active
        ? <><p>{item.text}</p><div className="collab-agent-run__marquee" aria-label={runStatusLabel(status, c)}><div>
            <span>{c("agentStageContext")}</span><span>{c("agentStageTools")}</span><span>{c("agentStageShare")}</span><span>{c("agentStageContext")}</span>
          </div></div></>
        : hasOutput
          ? <p className="collab-agent-run__instruction">{item.text}</p>
          : <><p>{item.text}</p><div className="collab-agent-run__summary">{item.agentRunError || item.agentRunSummary || runStatusLabel(status, c)}</div></>}
    </div>
    {detailed && prompt.kind === "approval" && <div className="collab-agent-prompt"><ApprovalModal
      key={prompt.id}
      approval={{ id: prompt.id, tool: prompt.tool || "tool", subject: prompt.subject || "", reason: prompt.reason }}
      onAnswer={(allow, session, persist) => onRespond({ allow, session, persist })}
      onStop={() => onRespond({ allow: false })}
    /></div>}
    {detailed && prompt.kind === "ask" && <div className="collab-agent-prompt"><AskCard
      key={prompt.id}
      ask={{ id: prompt.id, questions: prompt.questions || [] }}
      onAnswer={(_, answers) => onRespond({ answering: true, answers })}
      onDismiss={() => onRespond({ answering: true, answers: [] })}
      onStop={() => onRespond({ allow: false })}
    /></div>}
  </div>;
}

function timelineDOMID(id: string): string {
  return `collab-item-${encodeURIComponent(id)}`;
}

function ReferenceCards({ item, items, c, expanded, onToggle, onJump }: { item: CollaborationTimelineItem; items: Map<string, CollaborationTimelineItem>; c: CollabCopy; expanded: Set<string>; onToggle(id: string): void; onJump(id: string): void }) {
  if (item.referenceIds.length === 0) return null;
  return <div className="collab-reference-list">
    {item.referenceIds.map((id) => {
      const reference = items.get(id);
      const open = expanded.has(id);
      return <div key={id} className="collab-reference-card">
        <button type="button" className="collab-reference-card__preview" aria-expanded={open} onClick={() => onToggle(id)}>
          <span><Reply size={12} /><strong>{reference?.actorName || c("referenceMissing")}</strong></span>
          <p className={open ? "collab-reference-card__text--open" : ""}>{reference?.text || c("referenceMissing")}</p>
          <small>{open ? <ChevronUp size={12} /> : <ChevronDown size={12} />}{open ? c("referenceCollapse") : c("referenceExpand")}</small>
        </button>
        {reference && <button type="button" className="collab-reference-card__jump" aria-label={c("referenceJump")} title={c("referenceJump")} onClick={() => onJump(id)}><ExternalLink size={13} /></button>}
      </div>;
    })}
  </div>;
}

export function CollaborationTimeline(props: CollaborationTimelineProps) {
  const { locale, t } = useI18n();
  const c = collabCopy(t);
  const [expandedReferences, setExpandedReferences] = useState<Set<string>>(new Set());
  const [requestAgentOpen, setRequestAgentOpen] = useState<string | null>(null);
  const [requestAgentPlacement, setRequestAgentPlacement] = useState<{ side: "above" | "below"; maxHeight: number }>({ side: "above", maxHeight: 260 });
  const requestAgentRef = useRef<HTMLDivElement>(null);
  const requestAgentTriggerRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!requestAgentOpen) return;
    const close = (event: MouseEvent) => {
      if (!requestAgentRef.current || !requestAgentRef.current.contains(event.target as Node)) {
        setRequestAgentOpen(null);
      }
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [requestAgentOpen]);
  const closeRequestAgent = (restoreFocus = false) => {
    if (restoreFocus) requestAgentTriggerRef.current?.focus();
    setRequestAgentOpen(null);
  };
  const toggleRequestAgent = (event: ReactMouseEvent<HTMLButtonElement>, itemId: string) => {
    if (requestAgentOpen === itemId) {
      closeRequestAgent();
      return;
    }
    requestAgentTriggerRef.current = event.currentTarget;
    const trigger = event.currentTarget.getBoundingClientRect();
    const scroll = event.currentTarget.closest(".collab-scroll")?.getBoundingClientRect();
    const above = Math.max(0, trigger.top - (scroll?.top ?? 0));
    const below = Math.max(0, (scroll?.bottom ?? window.innerHeight) - trigger.bottom);
    const side = below >= above ? "below" : "above";
    setRequestAgentPlacement({ side, maxHeight: Math.max(48, Math.min(260, Math.floor(Math.max(above, below) - 8))) });
    setRequestAgentOpen(itemId);
  };
  if (props.items.length === 0) return <div className="collab-empty">{c("empty")}</div>;
  const rawItems = new Map(props.items.map((item) => [item.id, item]));
  const visibleItems = visibleCollaborationTimeline(props.items);
  const runByResult = new Map(props.items.filter((item) => item.kind === "agent_result" && item.agentRunId).map((item) => [item.id, item.agentRunId as string]));
  const jumpTo = (id: string) => {
    const target = document.getElementById(timelineDOMID(runByResult.get(id) || id));
    if (!target) return;
    target.scrollIntoView({ behavior: "smooth", block: "center" });
    target.classList.add("collab-message--referenced");
    window.setTimeout(() => target.classList.remove("collab-message--referenced"), 1800);
  };

  const requestAgentEligible = (props.members || []).filter(
    (member) => member.online && member.id !== props.selfMemberId && !member.isSelf && Boolean(member.agent.id.trim()),
  );

  return <div className="collab-timeline-list">
    {visibleItems.map((item) => {
      const presence = presenceLabel(item, c);
      if (presence) return <div key={item.id} className="collab-presence-notice" role="status"><span aria-hidden="true" />{presence}<time dateTime={item.createdAt}>{timeLabel(item.createdAt, locale)}</time></div>;

      const own = item.actorId === props.selfMemberId;
      const selected = props.selectedIds.includes(item.id);
      const pending = props.pendingIntents[item.id];
      const transfer = props.transfers.find((value) => value.fileId === item.id && value.direction === (own ? "share" : "receive"));
      const incomingRequest = item.kind === "agent_request" && item.targetMemberId === props.selfMemberId && item.requestStatus !== "accepted" && item.requestStatus !== "rejected";
      const waitingAgentRun = own && item.kind === "agent_command" && item.agentRunStatus === "waiting_approval";
      const actor = (props.members || []).find((member) => member.id === item.actorId || member.agent.id === item.actorId);
      return (
        <article id={timelineDOMID(item.id)} key={item.id} className={`collab-message collab-message--${item.kind}${selected ? " collab-message--selected" : ""}${item.localPending ? " collab-message--pending" : ""}`}>
          <CollaborationAvatar name={item.actorName} src={item.actorAgent ? actor?.agent.avatar : actor?.avatar} agent={item.actorAgent} />
          <div className="collab-message-body">
            <header>
              <strong>{item.actorName}{own && !item.actorAgent ? ` (${c("you")})` : ""}</strong>
              <span className={`collab-kind collab-kind--${item.kind}`}>{item.kind === "contribution" ? contributionLabel(c, item.contributionKind) : c(kindCopy[item.kind])}</span>
              <time dateTime={item.createdAt}>{timeLabel(item.createdAt, locale)}</time>
              {item.syncStatus === "pending" && <span className="collab-sync collab-sync--pending"><RefreshCw size={11} />{c("pending")}</span>}
              {item.syncStatus === "failed" && <span className="collab-sync collab-sync--failed"><CircleAlert size={11} />{c("failedItem")}</span>}
            </header>
            {item.kind === "agent_command" ? <><AgentRunCard item={item} c={c} canRespond={waitingAgentRun} prompt={props.agentPrompt} onRespond={(response) => props.onRespondAgentRun(item, response)} />{item.agentRunOutput && <p className="collab-agent-output">{item.agentRunOutput}</p>}</> : item.kind === "file" ? <FileCard item={item} own={own} transfer={transfer} c={c} onReceive={() => props.onReceiveFile(item.id)} onPause={() => props.onPauseFile(item.id)} onResume={() => props.onResumeFile(item.id)} onRevoke={() => props.onRevokeFile(item.id)} onOpen={() => props.onOpenFile(item.id)} onReveal={() => props.onRevealFile(item.id)} /> : <p>{item.text}</p>}
            <ReferenceCards item={item} items={rawItems} c={c} expanded={expandedReferences} onToggle={(id) => setExpandedReferences((current) => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })} onJump={jumpTo} />
            {(item.handoffs || []).length > 0 && <div className="collab-handoffs">{item.handoffs?.map((handoff, index) => {
              const target = props.members?.find((member) => member.agent.id === handoff.targetAgentId);
              return <div key={`${handoff.targetAgentId}:${index}`}><Bot size={12} /><span><strong>{c("handoffTo", { name: target?.agent.name || handoff.targetAgentId })}</strong>{handoff.instruction}</span></div>;
            })}</div>}
            {item.kind === "agent_request" && !incomingRequest && <div className="collab-request-state">{c("waitingOwner")}</div>}
            {!item.localPending && !waitingAgentRun && <div className="collab-message-actions">
              <label className="collab-message-select">
                <input type="checkbox" checked={selected} onChange={() => props.onToggle(item.id)} aria-label={`${c("agentRespond")}: ${item.actorName}`} />
                <span><Check size={14} /></span>
              </label>
              <button type="button" aria-label={c("reply")} title={c("reply")} onClick={() => props.onReply(item)}><Reply size={14} /><span>{c("reply")}</span></button>
              <button type="button" aria-label={c("agree")} title={c("agree")} onClick={() => props.onAgree(item)}><ThumbsUp size={14} /><span>{c("agree")}</span></button>
              <button type="button" aria-label={c("agentRespond")} title={props.agentBusy ? c("agentQueueHint") : c("agentRespond")} onClick={() => props.onAgent(item)}><Bot size={14} /><span>{c("agentRespond")}</span></button>
              <div className="collab-request-agent" ref={requestAgentOpen === item.id ? requestAgentRef : undefined} onKeyDown={(event) => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeRequestAgent(true); } }}>
                <button type="button" aria-label={c("requestOther")} title={c("requestOther")} aria-expanded={requestAgentOpen === item.id} aria-controls={`${timelineDOMID(item.id)}-request-agents`} onClick={(event) => toggleRequestAgent(event, item.id)}>
                  <span className="collab-double-bot" aria-hidden="true"><Bot size={10} /><Bot size={10} /></span>
                </button>
                {requestAgentOpen === item.id && <div id={`${timelineDOMID(item.id)}-request-agents`} className={`collab-request-agent__popup collab-request-agent__popup--${requestAgentPlacement.side}`} role="group" aria-label={c("requestOther")} style={{ maxHeight: requestAgentPlacement.maxHeight }}>
                  {requestAgentEligible.length === 0
                    ? <p className="collab-request-agent__empty">{c("requestAgentEmpty")}</p>
                    : requestAgentEligible.map((member) => (
                      <button key={member.id} type="button" title={`${member.name} · ${member.agent.name} · ${member.agent.role || c("agentResponsibilityFallback")}`} onClick={() => { closeRequestAgent(true); props.onRequestAgent(item, member.id); }}>
                        <span>{member.name} · {member.agent.name} · {member.agent.role || c("agentResponsibilityFallback")}</span>
                      </button>
                    ))}
                </div>}
              </div>
            </div>}
            {incomingRequest && <div className="collab-request-actions">
              <button type="button" className="collab-action-accent" title={props.agentBusy ? c("agentQueueHint") : undefined} onClick={() => props.onAccept(item)}><UserRound size={13} />{c("acceptRun")}</button>
              <button type="button" title={props.agentBusy ? c("agentQueueHint") : undefined} onClick={() => {
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
