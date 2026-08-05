import type { CollaborationMember, CollaborationTimelineItem } from "./types";

export type CollaborationMentionKind = "member" | "agent";

export interface CollaborationMention {
  key: string;
  kind: CollaborationMentionKind;
  memberId: string;
  targetId: string;
  label: string;
  ownerName: string;
}

export interface ActiveMention {
  start: number;
  end: number;
  query: string;
}

export function collaborationMentionCandidates(
  members: CollaborationMember[],
  selfMemberId: string | undefined,
  connected: boolean,
): CollaborationMention[] {
  const self = members.find((member) => member.id === selfMemberId || member.isSelf);
  const result: CollaborationMention[] = [];
  if (self?.agent.id) {
    result.push({ key: `agent:${self.agent.id}`, kind: "agent", memberId: self.id, targetId: self.agent.id, label: self.agent.name, ownerName: self.name });
  }
  if (!connected) return result;
  for (const member of members) {
    if (!member.online || member.id === self?.id || member.isSelf) continue;
    result.push({ key: `member:${member.id}`, kind: "member", memberId: member.id, targetId: member.id, label: member.name, ownerName: member.name });
    if (member.agent.id) {
      result.push({ key: `agent:${member.agent.id}`, kind: "agent", memberId: member.id, targetId: member.agent.id, label: member.agent.name, ownerName: member.name });
    }
  }
  return result;
}

export function activeMention(value: string, cursor: number): ActiveMention | undefined {
  const safeCursor = Math.max(0, Math.min(cursor, value.length));
  const match = /(?:^|[\s([{'"，。！？,:;])@([^\s@]*)$/u.exec(value.slice(0, safeCursor));
  if (!match) return undefined;
  const query = match[1] || "";
  return { start: safeCursor - query.length - 1, end: safeCursor, query };
}

export function filterMentionCandidates(candidates: CollaborationMention[], query: string): CollaborationMention[] {
  const needle = query.trim().normalize("NFKC").toLocaleLowerCase();
  if (!needle) return candidates;
  return candidates.filter((candidate) => `${candidate.label}\n${candidate.ownerName}`.normalize("NFKC").toLocaleLowerCase().includes(needle));
}

export function insertMention(value: string, active: ActiveMention, mention: CollaborationMention): { value: string; cursor: number } {
  const inserted = `@${mention.label} `;
  return {
    value: value.slice(0, active.start) + inserted + value.slice(active.end),
    cursor: active.start + inserted.length,
  };
}

function escaped(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function hasMentionToken(text: string, label: string): boolean {
  return new RegExp(`(?:^|\\s)@${escaped(label)}(?=$|\\s|[,.!?，。！？:：;；])`, "u").test(text);
}

export function mentionPayload(
  text: string,
  selected: CollaborationMention[],
): { mentionMemberIDs: string[]; mentionAgentIDs: string[] } {
  const resolved = new Map<string, CollaborationMention>();
  for (const mention of selected) {
    if (hasMentionToken(text, mention.label)) resolved.set(mention.key, mention);
  }
  return {
    mentionMemberIDs: [...resolved.values()].filter((mention) => mention.kind === "member").map((mention) => mention.targetId),
    mentionAgentIDs: [...resolved.values()].filter((mention) => mention.kind === "agent").map((mention) => mention.targetId),
  };
}

function hash(value: string, seed: number): string {
  let current = seed >>> 0;
  for (let index = 0; index < value.length; index++) {
    current ^= value.charCodeAt(index);
    current = Math.imul(current, 16777619) >>> 0;
  }
  return current.toString(16).padStart(8, "0");
}

export function mentionRequestID(itemId: string, agentId: string): string {
  const value = `${itemId}\0${agentId}`;
  return `mention-${hash(value, 2166136261)}${hash(value, 2246822519)}`;
}

export function nextMentionedAgentItem(
  timeline: CollaborationTimelineItem[],
  selfMemberId: string | undefined,
  agentId: string | undefined,
): CollaborationTimelineItem | undefined {
  if (!selfMemberId || !agentId) return undefined;
  const ownRuns = timeline.filter((item) => item.kind === "agent_command" && item.actorId === selfMemberId);
  const handledReferences = new Set(ownRuns.flatMap((item) => item.referenceIds));
  const handledCommands = new Set(ownRuns.map((item) => item.agentCommandId).filter(Boolean));
  return timeline.find((item) => item.kind === "chat"
    && (item.mentionMemberIds?.includes(selfMemberId) || item.mentionAgentIds?.includes(agentId))
    && !handledReferences.has(item.id)
    && !handledCommands.has(mentionRequestID(item.id, agentId)));
}
