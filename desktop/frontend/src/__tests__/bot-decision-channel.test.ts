import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { botDecisionTargets, decisionChannelInputForBot, decisionTargetKey, savedDecisionChannelForConnection } from "../lib/botDecisionChannel";
import type { BotConnectionView, DecisionChannelView } from "../lib/types";

function baseConnection(overrides: Partial<BotConnectionView> = {}): BotConnectionView {
  return {
    id: "weixin-weixin",
    provider: "weixin",
    domain: "weixin",
    label: "微信",
    enabled: true,
    status: "connected",
    model: "",
    toolApprovalMode: "ask",
    workspaceRoot: "",
    access: { enabled: true, allowAll: false, pairingEnabled: false, users: [], groups: [], approvers: [], admins: [] },
    credential: { appId: "", appSecretEnv: "", tokenEnv: "", accountId: "", secretSet: true },
    sessionMappings: [],
    endpoints: [],
    lastError: "",
    createdAt: "",
    updatedAt: "",
    ...overrides,
  };
}

const legacyMappings = [
  { remoteId: "owner", sessionId: "", sessionSource: "auto", chatType: "", userId: "", threadId: "", scope: "global", workspaceRoot: "", updatedAt: "" },
  { remoteId: "team", sessionId: "", sessionSource: "auto", chatType: "group", userId: "one", threadId: "", scope: "global", workspaceRoot: "", updatedAt: "" },
  { remoteId: "team", sessionId: "", sessionSource: "auto", chatType: "group", userId: "two", threadId: "", scope: "global", workspaceRoot: "", updatedAt: "" },
];

// Legacy behavior: candidates come from session_mappings and dedupe by
// remoteId + chatType (two group users share one target).
{
  const connection = baseConnection({ sessionMappings: legacyMappings });
  const targets = botDecisionTargets(connection);
  assert.deepEqual(targets, [
    { remoteId: "owner", chatType: "dm" },
    { remoteId: "team", chatType: "group" },
  ]);
  assert.deepEqual(decisionChannelInputForBot(connection, targets[1]), {
    id: "",
    name: "微信",
    kind: "weixin",
    enabled: true,
    connectionId: "weixin-weixin",
    domain: "weixin",
    chatId: "team",
    chatType: "group",
  });
}

// Stable endpoints survive empty sessionMappings: candidates still resolve so
// the channel stays selectable and testable after session GC.
{
  const connection = baseConnection({
    sessionMappings: [],
    endpoints: [
      { remoteId: "wx-owner", chatType: "", threadId: "", updatedAt: "" },
      { remoteId: "wx-team", chatType: "group", threadId: "", updatedAt: "" },
    ],
  });
  const targets = botDecisionTargets(connection);
  assert.deepEqual(targets, [
    { remoteId: "wx-owner", chatType: "dm" },
    { remoteId: "wx-team", chatType: "group" },
  ]);
}

// Endpoints and legacy mappings union without duplicates; endpoints win the
// ordering for a shared remoteId+chatType.
{
  const connection = baseConnection({
    sessionMappings: [{ remoteId: "owner", sessionId: "", sessionSource: "auto", chatType: "", userId: "", threadId: "", scope: "global", workspaceRoot: "", updatedAt: "" }],
    endpoints: [{ remoteId: "owner", chatType: "", threadId: "", updatedAt: "" }],
  });
  const targets = botDecisionTargets(connection);
  assert.deepEqual(targets, [{ remoteId: "owner", chatType: "dm" }]);
}

// A saved decision channel is matched by connection id even when
// sessionMappings is empty, and its target key equals the candidate key.
{
  const connection = baseConnection({ sessionMappings: [], endpoints: [{ remoteId: "wx-owner", chatType: "", threadId: "", updatedAt: "" }] });
  const channels: DecisionChannelView[] = [
    { id: "channel-1", name: "微信", kind: "weixin", enabled: true, connection_id: "weixin-weixin", domain: "weixin", chat_id: "wx-owner", chat_type: "dm" },
    { id: "channel-other", name: "别的", kind: "weixin", enabled: true, connection_id: "other-conn", chat_id: "x", chat_type: "dm" },
    { id: "channel-desktop", name: "桌面", kind: "desktop", enabled: true, connection_id: "weixin-weixin", chat_id: "", chat_type: "" },
  ];
  const saved = savedDecisionChannelForConnection(connection, channels);
  assert.ok(saved, "saved channel must be found for the connection");
  assert.equal(saved!.id, "channel-1");
  assert.equal(decisionTargetKey(connection, saved!), "weixin-weixin:wx-owner:dm");
  const selected = botDecisionTargets(connection)[0];
  assert.equal(`${connection.id}:${selected.remoteId}:${selected.chatType}`, decisionTargetKey(connection, saved!));
}

// A connection without endpoints or mappings yields no candidates.
{
  const connection = baseConnection();
  assert.deepEqual(botDecisionTargets(connection), []);
  assert.equal(savedDecisionChannelForConnection(connection, []), null);
}

const here = dirname(fileURLToPath(import.meta.url));
const settingsSource = readFileSync(resolve(here, "../components/SettingsPanel.tsx"), "utf8");
assert.match(settingsSource, /settings\.botDecisionChannelAction/);
assert.match(settingsSource, /app\.SaveDecisionChannel\(decisionChannelInputForBot\(/);
assert.match(settingsSource, /endpoints:\s*asArray\(raw\?\.endpoints\)/);

console.log("bot decision channel tests passed");
