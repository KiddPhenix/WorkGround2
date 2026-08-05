import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (path: string) => readFileSync(new URL(path, import.meta.url), "utf8");
const panel = read("../components/SettingsPanel.tsx");
const types = read("../lib/types.ts");
const bridge = read("../lib/bridge.ts");

assert.match(types, /interface RelayView[\s\S]+allowInsecure: boolean/, "relay settings expose explicit insecure consent");
assert.match(types, /interface CollaborationSettingsView[\s\S]+relays: RelayView\[\]/, "settings expose the relay list");
assert.match(types, /interface CollaborationSettingsView[\s\S]+preferLAN: boolean/, "settings expose LAN preference");
assert.match(types, /interface CollaborationSettingsView[\s\S]+connectTimeoutSeconds: number[\s\S]+routeStableSeconds: number/, "settings expose routing timing controls");
assert.match(bridge, /SetCollaboration\(c: CollaborationSettingsView\)/, "bridge exposes relay persistence");
assert.match(panel, /app\.SetCollaboration\(draft\)/, "Settings saves relays through the backend");
assert.match(panel, /relayNeedsInsecureConsent\(relay\.url\)[\s\S]+!relay\.allowInsecure/, "Settings detects non-loopback insecure relay URLs without consent");
assert.match(panel, /hostname !== "localhost"[\s\S]+127[\s\S]+hostname !== "::1"/, "Settings exempts loopback ws URLs");
assert.match(panel, /disabled=\{busy \|\| !dirty \|\| unsafe \|\| invalidTiming\}/, "Settings blocks saving unconfirmed insecure relays or invalid timing");
assert.match(panel, /settings\.relay\.allowInsecure/, "Settings renders the explicit insecure-risk checkbox");

for (const locale of ["en", "zh", "zh-TW"]) {
  const source = read(`../locales/${locale}.ts`);
  for (const key of ["settings.relay.title", "settings.relay.allowInsecure", "settings.relay.insecureWarning"]) {
    assert.ok(source.includes(`"${key}"`), `${locale} includes ${key}`);
  }
}

console.log("relay settings contract tests passed");
