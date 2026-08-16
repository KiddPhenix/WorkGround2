import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { botDecisionTargets, decisionChannelInputForBot } from "../lib/botDecisionChannel";
import type { BotConnectionView } from "../lib/types";

const connection: BotConnectionView = {
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
  sessionMappings: [
    { remoteId: "owner", sessionId: "", sessionSource: "auto", chatType: "", userId: "", threadId: "", scope: "global", workspaceRoot: "", updatedAt: "" },
    { remoteId: "team", sessionId: "", sessionSource: "auto", chatType: "group", userId: "one", threadId: "", scope: "global", workspaceRoot: "", updatedAt: "" },
    { remoteId: "team", sessionId: "", sessionSource: "auto", chatType: "group", userId: "two", threadId: "", scope: "global", workspaceRoot: "", updatedAt: "" },
  ],
  lastError: "",
  createdAt: "",
  updatedAt: "",
};

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

const here = dirname(fileURLToPath(import.meta.url));
const settingsSource = readFileSync(resolve(here, "../components/SettingsPanel.tsx"), "utf8");
assert.match(settingsSource, /settings\.botDecisionChannelAction/);
assert.match(settingsSource, /app\.SaveDecisionChannel\(decisionChannelInputForBot\(/);

console.log("bot decision channel tests passed");
