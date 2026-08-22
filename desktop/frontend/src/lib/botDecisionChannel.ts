import type { BotConnectionView, DecisionChannelView } from "./types";

export type BotDecisionTarget = { remoteId: string; chatType: string };

export type BotDecisionTargetKey = string;

// botDecisionTargets derives the stable notification/decision candidates for a
// connection. Stable endpoints registered on authorized inbound messages come
// first; legacy session_mappings are kept as migration candidates so existing
// configs keep working after an upgrade. Targets dedupe by remoteId + chatType.
export function botDecisionTargets(connection: BotConnectionView): BotDecisionTarget[] {
  const seen = new Set<string>();
  const targets: BotDecisionTarget[] = [];
  for (const endpoint of connection.endpoints ?? []) {
    appendTarget(targets, seen, { remoteId: endpoint.remoteId, chatType: endpoint.chatType });
  }
  for (const mapping of connection.sessionMappings) {
    appendTarget(targets, seen, { remoteId: mapping.remoteId, chatType: mapping.chatType });
  }
  return targets;
}

function appendTarget(targets: BotDecisionTarget[], seen: Set<string>, next: { remoteId: string; chatType: string }) {
  const remoteId = next.remoteId.trim();
  if (!remoteId) return;
  const chatType = next.chatType.trim() || "dm";
  const key = `${remoteId}\n${chatType}`;
  if (seen.has(key)) return;
  seen.add(key);
  targets.push({ remoteId, chatType });
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

// savedDecisionChannelForConnection returns the persisted broker channel that
// routes decisions/notifications for this connection, so the settings page can
// show "已设置" and offer a test even when sessionMappings is empty.
export function savedDecisionChannelForConnection(connection: BotConnectionView, channels: DecisionChannelView[]): DecisionChannelView | null {
  for (const channel of channels) {
    if (!channel || channel.kind === "desktop" || !channel.connection_id) continue;
    if (channel.connection_id.trim() !== connection.id.trim()) continue;
    if (channel.domain && connection.domain && channel.domain.trim() !== connection.domain.trim()) continue;
    if (!channel.chat_id?.trim()) continue;
    return channel;
  }
  return null;
}

// decisionTargetKey builds the same key the settings panel uses to compare the
// selected candidate against a saved channel.
export function decisionTargetKey(connection: BotConnectionView, channel: DecisionChannelView): BotDecisionTargetKey {
  return `${connection.id}:${channel.chat_id?.trim()}:${channel.chat_type?.trim() || "dm"}`;
}
