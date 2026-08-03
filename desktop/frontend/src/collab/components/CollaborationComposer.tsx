import { useRef, useState, type KeyboardEvent } from "react";
import { Bot, Send, Users } from "lucide-react";
import { ModelSwitcher } from "../../components/ModelSwitcher";
import { isComposerSubmitKey, normalizeComposerSubmitKey, type ComposerSubmitKey } from "../../lib/composerKeyboard";
import { useT } from "../../lib/i18n";
import { collabCopy, contributionKinds, contributionLabel } from "../copy";
import type { CollaborationMember } from "../types";

type ComposerMode = "chat" | "contribution" | "agent" | "both" | "request";

interface CollaborationComposerProps {
  members: CollaborationMember[];
  selfMemberId?: string;
  disabled?: boolean;
  agentBusy?: boolean;
  tabID?: string;
  modelLabel: string;
  submitKey: ComposerSubmitKey;
  prefill: string;
  onPrefillConsumed(): void;
  onChat(text: string): Promise<void>;
  onAgent(text: string): Promise<void>;
  onContribution(text: string, kind: string): Promise<void>;
  onRequest(memberId: string, text: string): Promise<void>;
  onSwitchModel(name: string): Promise<void>;
}

export function CollaborationComposer(props: CollaborationComposerProps) {
  const c = collabCopy(useT());
  const [draft, setDraft] = useState("");
  const [mode, setMode] = useState<ComposerMode>("chat");
  const [target, setTarget] = useState("");
  const [contributionKind, setContributionKind] = useState("proposal");
  const [sending, setSending] = useState(false);
  const composingRef = useRef(false);
  const value = props.prefill || draft;
  const others = props.members.filter((member) => member.id !== props.selfMemberId);
  const agentMode = mode === "agent" || mode === "both";

  const update = (next: string) => {
    if (props.prefill) props.onPrefillConsumed();
    setDraft(next);
  };
  const submit = async () => {
    const text = value.trim();
    if (!text || sending || props.disabled) return;
    setSending(true);
    try {
      if (mode === "agent") await props.onAgent(text);
      else if (mode === "contribution") await props.onContribution(text, contributionKind);
      else if (mode === "both") { await props.onChat(text); await props.onAgent(text); }
      else if (mode === "request") await props.onRequest(target, text);
      else await props.onChat(text);
      setDraft(""); props.onPrefillConsumed();
    } catch {
      // The controller projects the actionable error into the collaboration surface.
    } finally { setSending(false); }
  };
  const keyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (isComposerSubmitKey(event, normalizeComposerSubmitKey(props.submitKey), composingRef.current)) { event.preventDefault(); void submit(); }
  };

  return <div className="collab-composer">
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
      <ModelSwitcher label={props.modelLabel} tabId={props.tabID} onPick={props.onSwitchModel} />
      <span className="collab-composer-shortcut">{normalizeComposerSubmitKey(props.submitKey) === "ctrl_enter" ? "Ctrl+Enter" : "Enter"}</span>
      <span className="collab-composer-owner"><Bot size={13} />{c("subtitle")}</span>
    </div>
    <div className="collab-composer-row">
      <textarea rows={2} value={value} placeholder={c("messagePlaceholder")} onChange={(event) => update(event.target.value)} onKeyDown={keyDown} onCompositionStart={() => { composingRef.current = true; }} onCompositionEnd={() => { composingRef.current = false; }} disabled={props.disabled} />
      <button type="button" className="collab-primary-button" title={agentMode && props.agentBusy ? c("agentBusy") : undefined} onClick={() => void submit()} disabled={props.disabled || sending || (agentMode && props.agentBusy) || !value.trim() || (mode === "request" && !target)}>
        {mode === "agent" || mode === "both" ? <Bot size={16} /> : mode === "request" ? <Users size={16} /> : <Send size={16} />}{c("send")}
      </button>
    </div>
  </div>;
}
