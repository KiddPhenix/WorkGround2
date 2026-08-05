import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Bot } from "lucide-react";
import { recentAgentActivity } from "../agentActivity";
import type { CollabCopy } from "../copy";
import type { CollaborationMember, CollaborationTimelineItem } from "../types";

export interface AgentActivityAnchor {
  memberId: string;
  left: number;
  right: number;
  top: number;
  bottom: number;
}

function activityTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function AgentActivityPopover({ member, timeline, anchor, c }: {
  member: CollaborationMember;
  timeline: CollaborationTimelineItem[];
  anchor: AgentActivityAnchor;
  c: CollabCopy;
}) {
  const activity = recentAgentActivity(timeline, member.id);
  const [activeIndex, setActiveIndex] = useState(0);
  const current = activity[activeIndex] || activity[0];

  useEffect(() => setActiveIndex(0), [member.id, activity.length, activity[0]?.id]);
  useEffect(() => {
    if (activity.length <= 1 || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const timer = window.setInterval(() => setActiveIndex((index) => (index + 1) % activity.length), 2800);
    return () => window.clearInterval(timer);
  }, [activity.length, member.id]);

  if (typeof document === "undefined") return null;
  const width = Math.min(326, window.innerWidth - 24);
  const left = Math.max(12, Math.min(anchor.right - width, window.innerWidth - width - 12));
  const top = anchor.bottom + 190 <= window.innerHeight ? anchor.bottom + 8 : Math.max(12, anchor.top - 182);

  return createPortal(<aside
    id="collab-agent-activity-popover"
    className="collab-agent-activity-popover"
    style={{ left, top, width }}
    role="tooltip"
    aria-label={c("agentActivityTitle", { name: member.agent.name })}
  >
    <header><span><Bot size={14} aria-hidden="true" /></span><div><strong>{member.agent.name}</strong><small>{c("agentActivityRecent")}</small></div><em>{activity.length > 0 ? `${activeIndex + 1} / ${activity.length}` : "—"}</em></header>
    <div className="collab-agent-activity-popover__viewport">
      {current
        ? <article key={current.id} className="collab-agent-activity-popover__item" data-activity-kind={current.kind}>
          <div><span>{c(current.kind === "input" ? "agentActivityInput" : "agentActivityOutput")}</span><time>{activityTime(current.createdAt)}</time></div>
          <p>{current.text}</p>
        </article>
        : <p className="collab-agent-activity-popover__empty">{c("agentActivityEmpty")}</p>}
    </div>
  </aside>, document.body);
}
