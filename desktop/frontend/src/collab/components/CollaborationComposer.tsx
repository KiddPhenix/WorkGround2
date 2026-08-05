import { useEffect, useRef, useState, type CSSProperties, type DragEvent, type KeyboardEvent } from "react";
import { Bot, FileUp, Reply, Send, UserRound, Users, X } from "lucide-react";
import { isComposerSubmitKey, normalizeComposerSubmitKey, type ComposerSubmitKey } from "../../lib/composerKeyboard";
import { useT } from "../../lib/i18n";
import { onFilesDroppedIn } from "../../lib/bridge";
import { collabCopy, contributionKinds, contributionLabel } from "../copy";
import { activeMention, collaborationMentionCandidates, filterMentionCandidates, insertMention, mentionPayload, type CollaborationMention } from "../mentions";
import type { CollaborationMember, CollaborationTimelineItem } from "../types";

type ComposerMode = "chat" | "contribution" | "agent" | "both" | "request";

interface CollaborationComposerProps {
  members: CollaborationMember[];
  selfMemberId?: string;
  connected: boolean;
  disabled?: boolean;
  agentBusy?: boolean;
  submitKey: ComposerSubmitKey;
  replyTo?: CollaborationTimelineItem;
  onReplyClear(): void;
  onChat(text: string, mentions: { mentionMemberIDs: string[]; mentionAgentIDs: string[] }, referenceIDs: string[]): Promise<void>;
  onAgent(text: string, referenceIDs: string[]): Promise<void>;
  onContribution(text: string, kind: string): Promise<void>;
  onRequest(memberId: string, text: string, referenceIDs: string[]): Promise<void>;
  onShareFiles(paths: string[]): Promise<void>;
}

export function CollaborationComposer(props: CollaborationComposerProps) {
  const c = collabCopy(useT());
  const [draft, setDraft] = useState("");
  const [mode, setMode] = useState<ComposerMode>("chat");
  const [target, setTarget] = useState("");
  const [contributionKind, setContributionKind] = useState("proposal");
  const [sending, setSending] = useState(false);
  const [sharing, setSharing] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [cursor, setCursor] = useState(0);
  const [mentionIndex, setMentionIndex] = useState(0);
  const [mentionDismissed, setMentionDismissed] = useState(false);
  const [selectedMentions, setSelectedMentions] = useState<CollaborationMention[]>([]);
  const rootRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const composingRef = useRef(false);
  const value = draft;
  const others = props.members.filter((member) => member.online && member.id !== props.selfMemberId);
  const agentMode = mode === "agent" || mode === "both";
  const mentionEnabled = mode === "chat" || mode === "both";
  const mentionCandidates = collaborationMentionCandidates(props.members, props.selfMemberId, props.connected);
  const mention = mentionEnabled ? activeMention(value, cursor) : undefined;
  const mentionMatches = mention ? filterMentionCandidates(mentionCandidates, mention.query) : [];
  const mentionOpen = !mentionDismissed && Boolean(mention) && mentionMatches.length > 0;

  const update = (next: string, nextCursor = next.length) => {
    setDraft(next);
    setCursor(nextCursor);
    setMentionDismissed(false);
  };
  const chooseMention = (candidate: CollaborationMention) => {
    if (!mention) return;
    const next = insertMention(value, mention, candidate);
    update(next.value, next.cursor);
    setSelectedMentions((current) => [...current.filter((item) => item.key !== candidate.key), candidate]);
    setMentionIndex(0);
    window.requestAnimationFrame(() => {
      textareaRef.current?.focus();
      textareaRef.current?.setSelectionRange(next.cursor, next.cursor);
    });
  };
  const submit = async () => {
    const text = value.trim();
    if (!text || sending || props.disabled) return;
    const mentions = mentionPayload(text, selectedMentions);
    const referenceIDs = props.replyTo ? [props.replyTo.id] : [];
    setSending(true);
    try {
      if (mode === "agent") await props.onAgent(text, referenceIDs);
      else if (mode === "contribution") await props.onContribution(text, contributionKind);
      else if (mode === "both") { await props.onChat(text, mentions, referenceIDs); await props.onAgent(text, referenceIDs); }
      else if (mode === "request") await props.onRequest(target, text, referenceIDs);
      else await props.onChat(text, mentions, referenceIDs);
      setDraft(""); setCursor(0); setSelectedMentions([]); props.onReplyClear();
    } catch {
      // The controller projects the actionable error into the collaboration surface.
    } finally { setSending(false); }
  };
  const keyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (mentionOpen && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      setMentionIndex((current) => (current + step + mentionMatches.length) % mentionMatches.length);
      return;
    }
    if (mentionOpen && (event.key === "Enter" || event.key === "Tab")) {
      event.preventDefault();
      chooseMention(mentionMatches[mentionIndex] || mentionMatches[0]);
      return;
    }
    if (mentionOpen && event.key === "Escape") {
      event.preventDefault();
      setMentionDismissed(true);
      return;
    }
    if (isComposerSubmitKey(event, normalizeComposerSubmitKey(props.submitKey), composingRef.current)) { event.preventDefault(); void submit(); }
  };

  useEffect(() => { setMentionIndex(0); }, [mention?.query, mentionMatches.length]);

  useEffect(() => onFilesDroppedIn(() => rootRef.current, (paths) => {
    if (props.disabled || sharing || paths.length === 0) return;
    setDragging(false);
    setSharing(true);
    void props.onShareFiles(paths).catch(() => {}).finally(() => setSharing(false));
  }), [props.disabled, props.onShareFiles, sharing]);

  const dragOver = (event: DragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
    setDragging(true);
  };

  return <div ref={rootRef} className={`collab-composer${dragging ? " collab-composer--dragging" : ""}`} style={{ "--wails-drop-target": "drop" } as CSSProperties} onDragEnter={dragOver} onDragOver={dragOver} onDragLeave={() => setDragging(false)} onDrop={(event) => { event.preventDefault(); setDragging(false); }}>
    {(dragging || sharing) && <div className="collab-file-drop" role="status"><FileUp size={18} />{sharing ? c("filePreparing") : c("fileDrop")}</div>}
    {props.replyTo && <div className="collab-composer-reply"><Reply size={13} /><span><strong>{c("replyingTo", { name: props.replyTo.actorName })}</strong><small>{props.replyTo.text}</small></span><button type="button" aria-label={c("cancelReply")} title={c("cancelReply")} onClick={props.onReplyClear}><X size={14} /></button></div>}
    <div className="collab-composer-mode">
      <select value={mode} onChange={(event) => setMode(event.target.value as ComposerMode)} aria-label={c("messageType")}>
        <option value="chat">{c("teamChat")}</option>
        <option value="contribution">{c("contribution")}</option>
        <option value="agent">{c("myAgent")}</option>
        <option value="both">{c("chatAndAgent")}</option>
        <option value="request">{c("requestOther")}</option>
      </select>
      {mode === "request" && <select required value={target} onChange={(event) => setTarget(event.target.value)} aria-label={c("requestOther")}>
        <option value="">—</option>{others.map((member) => <option key={member.id} value={member.id}>{member.name} · {member.agent.name}</option>)}
      </select>}
      {mode === "contribution" && <select value={contributionKind} onChange={(event) => setContributionKind(event.target.value)} aria-label={c("contribution")}>
        {contributionKinds.map((kind) => <option key={kind} value={kind}>{contributionLabel(c, kind)}</option>)}
      </select>}
      <span className="collab-composer-shortcut">{normalizeComposerSubmitKey(props.submitKey) === "ctrl_enter" ? "Ctrl+Enter" : "Enter"}</span>
      <span className="collab-composer-owner"><Bot size={13} />{c("subtitle")}</span>
    </div>
    <div className="collab-composer-row">
      <div className="collab-composer-input">
        {mentionOpen && <div id="collab-mention-list" className="collab-mention-popup" role="listbox" aria-label={c("mentionList")}>
          {mentionMatches.map((candidate, index) => <button key={candidate.key} id={`collab-mention-${index}`} type="button" role="option" aria-selected={index === mentionIndex} className={index === mentionIndex ? "collab-mention-option--active" : ""} onMouseDown={(event) => event.preventDefault()} onClick={() => chooseMention(candidate)}>
            <span>{candidate.kind === "agent" ? <Bot size={15} /> : <UserRound size={15} />}</span>
            <span><strong>@{candidate.label}</strong><small>{candidate.kind === "agent" ? c("mentionAgent", { name: candidate.ownerName }) : c("mentionMember")}</small></span>
          </button>)}
        </div>}
        <textarea ref={textareaRef} rows={2} value={value} placeholder={c("messagePlaceholder")} aria-autocomplete="list" aria-controls={mentionOpen ? "collab-mention-list" : undefined} aria-expanded={mentionOpen} aria-activedescendant={mentionOpen ? `collab-mention-${mentionIndex}` : undefined} onChange={(event) => update(event.target.value, event.target.selectionStart)} onSelect={(event) => setCursor(event.currentTarget.selectionStart)} onKeyDown={keyDown} onCompositionStart={() => { composingRef.current = true; }} onCompositionEnd={() => { composingRef.current = false; }} disabled={props.disabled} />
      </div>
      <button type="button" className="collab-primary-button" title={agentMode && props.agentBusy ? c("agentQueueHint") : undefined} onClick={() => void submit()} disabled={props.disabled || sending || !value.trim() || (mode === "request" && !target)}>
        {mode === "agent" || mode === "both" ? <Bot size={16} /> : mode === "request" ? <Users size={16} /> : <Send size={16} />}{c("send")}
      </button>
    </div>
  </div>;
}
