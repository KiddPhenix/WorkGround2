import type { CollaborationTimelineItem } from "./types";

export interface CollaborationAgentActivity {
  id: string;
  kind: "input" | "output";
  text: string;
  createdAt: string;
  sequence: number;
}

function activityText(value?: string): string {
  return (value || "").trim().replace(/\s+/g, " ");
}

// The Room timeline is the single source of truth for shared Agent work. One
// running command can contribute both its input instruction and latest progress
// summary; a completed result contributes the final output.
export function recentAgentActivity(
  timeline: CollaborationTimelineItem[],
  memberId: string,
  limit = 6,
): CollaborationAgentActivity[] {
  if (!memberId || limit <= 0) return [];

  const activity = timeline
    .filter((item) => item.actorId === memberId && (item.kind === "agent_command" || item.kind === "agent_result"))
    .sort((left, right) => left.sequence - right.sequence)
    .flatMap((item) => {
      if (item.kind === "agent_result") {
        const text = activityText(item.text);
        return text ? [{ id: `${item.id}:output`, kind: "output" as const, text, createdAt: item.createdAt, sequence: item.sequence }] : [];
      }

      const entries: CollaborationAgentActivity[] = [];
      const input = activityText(item.text);
      const output = activityText(item.agentRunError || item.agentRunSummary);
      if (input) entries.push({ id: `${item.id}:input`, kind: "input", text: input, createdAt: item.createdAt, sequence: item.sequence });
      if (output && output !== input) entries.push({ id: `${item.id}:output`, kind: "output", text: output, createdAt: item.createdAt, sequence: item.sequence });
      return entries;
    });

  return activity.slice(-limit).reverse();
}
