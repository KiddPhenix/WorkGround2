import type { BotConnectionSessionMappingView, BotConnectionView } from "./types";

export type BotDecisionTarget = { remoteId: string; chatType: string };

export function botDecisionTargets(connection: BotConnectionView): BotDecisionTarget[] {
  const seen = new Set<string>();
  const targets: BotDecisionTarget[] = [];
  for (const mapping of connection.sessionMappings) {
    const remoteId = mapping.remoteId.trim();
    if (!remoteId) continue;
    const chatType = decisionChatType(mapping);
    const key = `${remoteId}\n${chatType}`;
    if (seen.has(key)) continue;
    seen.add(key);
    targets.push({ remoteId, chatType });
  }
  return targets;
}

function decisionChatType(mapping: BotConnectionSessionMappingView): string {
  return mapping.chatType.trim() || "dm";
}

export function decisionChannelInputForBot(connection: BotConnectionView, target: BotDecisionTarget) {
  return {
    id: "",
    name: connection.label.trim() || connection.provider,
    kind: connection.provider,
    enabled: true,
    connectionId: connection.id,
    domain: connection.domain,
    chatId: target.remoteId,
    chatType: target.chatType,
  };
}
