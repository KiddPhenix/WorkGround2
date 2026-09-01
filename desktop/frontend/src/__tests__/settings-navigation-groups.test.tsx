// Run: node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/settings-navigation-groups.test.tsx
// Focused contract for the grouped settings navigation: nine top-level entries
// in order, single-group expansion, cross-group auto-collapse, direct-entry
// collapse, per-group leaf memory, initialTab deep-linking, ARIA, and the
// three-locale group/nav labels.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { SettingsPanel } from "../components/SettingsPanel";
import { LocaleProvider } from "../lib/i18n";
import { en } from "../locales/en.ts";
import { zh } from "../locales/zh.ts";
import { zhTW } from "../locales/zh-TW.ts";
import type { AppBindings } from "../lib/bridge";
import type { SettingsView } from "../lib/types";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function eq<T>(actual: T, expected: T, label: string) {
  if (JSON.stringify(actual) === JSON.stringify(expected)) {
    ok(true, label);
  } else {
    ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    await act(async () => {
      await flushPromises();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

function baseSettings(): SettingsView {
  return {
    defaultModel: "",
    plannerModel: "",
    subagentModel: "",
    subagentEffort: "",
    autoPlan: "off",
    providers: [],
    officialProviders: [],
    permissions: { mode: "ask", allow: [], ask: [], deny: [], browser: { allowPasswordInput: true, allowFileUpload: true } },
    sandbox: { bash: "enforce", network: false, workspaceRoot: "", allowWrite: [], shell: "auto" },
    network: { proxyMode: "auto", proxyUrl: "", noProxy: "", proxy: { type: "socks5", server: "", port: 0, username: "", password: "" } },
    agent: { temperature: 0, maxSteps: 0, plannerMaxSteps: 0, maxSubagentDepth: 2, systemPrompt: "", coldResumePrune: true, reasoningLanguage: "auto" },
    bot: {
      enabled: false,
      model: "",
      toolApprovalMode: "",
      maxSteps: 0,
      debounceMs: 0,
      allowlist: { enabled: false, allowAll: false, qqUsers: [], feishuUsers: [], weixinUsers: [], qqGroups: [], feishuGroups: [], weixinGroups: [] },
      qq: { enabled: false, appId: "", appSecretEnv: "", secretSet: false, sandbox: false },
      feishu: { enabled: false, domain: "feishu", appId: "", appSecretEnv: "", secretSet: false, verificationToken: "", mode: "webhook", webhookPort: 0, requireMention: false },
      weixin: { enabled: false, accountId: "", tokenEnv: "", tokenSet: false, apiBase: "" },
      connections: [],
    },
    desktopLanguage: "en",
    desktopLayoutStyle: "workbench",
    desktopTheme: "auto",
    desktopThemeStyle: "graphite",
    closeBehavior: "background",
    displayMode: "standard",
    composerSubmitKey: "enter",
    statusBarStyle: "text",
    statusBarItems: ["model", "workspace", "git_branch"],
    defaultToolApprovalMode: "ask",
    checkUpdates: true,
    telemetry: true,
    metrics: true,
    memoryCompilerEnabled: true,
    configPath: "/tmp/WorkGround2/config.toml",
    providerKinds: [],
    autoApproveTools: false,
    bypass: false,
  };
}

console.log("\nsettings navigation groups");

// --- source contract: the grouped nav maps every leaf into its composite group ---
const settingsSource = readFileSync(resolve(import.meta.dirname, "../components/SettingsPanel.tsx"), "utf8");
assert.match(settingsSource, /id: "aiConfig", tabs: \["models", "styles", "memory", "global"\]/, "aiConfig group holds models, styles, memory, global");
assert.match(settingsSource, /id: "aiTools", tabs: \["ai", "skills", "plugins", "mcp"\]/, "aiTools group holds ai, skills, plugins, mcp");
assert.match(settingsSource, /id: "advanced", tabs: \["permissions", "sandbox", "network", "hooks"\]/, "advanced group holds permissions, sandbox, network, hooks");
ok(true, "SETTINGS_NAV composite groups carry the specified leaf pages");

// --- three-locale label contract ---
const GROUP_KEYS = ["settings.group.aiConfig", "settings.group.aiTools", "settings.group.advanced"] as const;
for (const key of GROUP_KEYS) {
  for (const [locale, dict] of [["en", en], ["zh", zh], ["zh-TW", zhTW]] as const) {
    const value = (dict as Record<string, string>)[key];
    ok(typeof value === "string" && value.length > 0, `${locale} defines ${key}`);
    ok(value !== key, `${locale} ${key} is translated (not a key fall-through)`);
  }
}
eq(en["settings.tab.bots"], "IM Integrations", "English renames Bots to IM Integrations");
eq(zh["settings.tab.bots"], "IM 集成", "Chinese renames 机器人 to IM 集成");
eq(zhTW["settings.tab.bots"], "IM 整合", "Traditional Chinese renames 機器人 to IM 整合");
eq(en["settings.tab.global"], "Global Instructions", "English names the global leaf by its instruction purpose");
eq(zh["settings.tab.global"], "全局指令", "Chinese names the global leaf 全局指令");
eq(zhTW["settings.tab.global"], "全域指令", "Traditional Chinese names the global leaf 全域指令");

// --- rendering contract ---
const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
  pretendToBeVisual: true,
  url: "http://localhost/",
});
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;
globalThis.CustomEvent = dom.window.CustomEvent;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
window.scrollTo = () => {};
Object.defineProperty(dom.window.HTMLCanvasElement.prototype, "getContext", {
  configurable: true,
  value: () => null,
});

window.go = {
  main: {
    App: {
      Settings: async () => baseSettings(),
    } as Partial<AppBindings> as AppBindings,
  },
};

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("missing root");

function navButtons(): HTMLButtonElement[] {
  return Array.from(document.querySelectorAll(".settings-center__nav button")) as HTMLButtonElement[];
}

function navButton(label: string): HTMLButtonElement {
  const match = navButtons().find((el) => el.textContent?.trim() === label);
  if (!match) throw new Error(`missing nav button ${label}`);
  return match;
}

function groupButton(label: string): HTMLButtonElement {
  const match = Array.from(document.querySelectorAll(".settings-center__navitem--group")).find((el) => el.textContent?.trim() === label);
  if (!match) throw new Error(`missing group header ${label}`);
  return match as HTMLButtonElement;
}

function topLevelLabels(): string[] {
  const nav = document.querySelector(".settings-center__nav");
  if (!nav) throw new Error("missing settings nav");
  const labels: string[] = [];
  for (const child of Array.from(nav.children)) {
    if (child.tagName === "BUTTON") {
      labels.push((child.textContent ?? "").trim());
    } else {
      const header = child.querySelector("button.settings-center__navitem--group");
      if (header) labels.push((header.textContent ?? "").trim());
    }
  }
  return labels;
}

function activeLeafLabel(): string {
  const el = document.querySelector(".settings-center__navitem--active");
  return (el?.textContent ?? "").trim();
}

function groupExpanded(label: string): boolean {
  return groupButton(label).getAttribute("aria-expanded") === "true";
}

function childLabels(group: string): string[] {
  const header = groupButton(group);
  const container = header.closest(".settings-center__group");
  return Array.from(container?.querySelectorAll(".settings-center__navitem--child") ?? []).map((el) => (el.textContent ?? "").trim());
}

let root = createRoot(rootEl);
await act(async () => {
  root.render(
    <LocaleProvider>
      <SettingsPanel initialTab="general" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} />
    </LocaleProvider>,
  );
  await flushPromises();
});

const expectedTopLevel = ["General", "AI Configuration", "AI & Tools", "IM Integrations", "Widget", "Appearance", "Shortcuts", "Advanced", "About"];
eq(topLevelLabels(), expectedTopLevel, "left nav shows nine top-level entries in the specified order");
ok(groupExpanded("AI Configuration") === false, "AI Configuration group starts collapsed");
ok(groupExpanded("AI & Tools") === false, "AI & Tools group starts collapsed");
ok(groupExpanded("Advanced") === false, "Advanced group starts collapsed");
eq(childLabels("Advanced"), [], "no group renders leaf children before expansion");

await act(async () => {
  navButton("Advanced").click();
  await flushPromises();
});
ok(groupExpanded("Advanced") === true, "clicking a group expands it (aria-expanded true)");
eq(childLabels("Advanced"), ["Permissions", "Sandbox", "Network", "Hooks"], "expanded group lists its leaf children");
eq(activeLeafLabel(), "Permissions", "expanding a group opens its first leaf");

await act(async () => {
  navButton("AI & Tools").click();
  await flushPromises();
});
ok(groupExpanded("AI & Tools") === true, "selecting another group expands it");
ok(groupExpanded("Advanced") === false, "selecting another group auto-collapses the previous group");
eq(activeLeafLabel(), "AI Collaboration", "the newly expanded group opens its first leaf");

await act(async () => {
  navButton("Appearance").click();
  await flushPromises();
});
ok(groupExpanded("AI & Tools") === false, "selecting a direct entry collapses the open group");
ok(groupExpanded("Advanced") === false, "no group stays expanded after a direct entry");
eq(activeLeafLabel(), "Appearance", "direct entry becomes the active leaf");

// per-group memory: the last visited leaf is reopened on re-entry
await act(async () => {
  navButton("Advanced").click();
  await flushPromises();
});
eq(activeLeafLabel(), "Permissions", "re-entering the group opens its remembered leaf (first visit)");
await act(async () => {
  navButton("Network").click();
  await flushPromises();
});
eq(activeLeafLabel(), "Network", "clicking a leaf inside the group updates the active leaf");
await act(async () => {
  navButton("Appearance").click();
  await flushPromises();
});
await act(async () => {
  navButton("Advanced").click();
  await flushPromises();
});
eq(activeLeafLabel(), "Network", "re-entering the group reopens the last visited leaf, not the first");

await act(async () => {
  root.unmount();
});

// initialTab deep-links to a leaf inside a composite group and auto-expands it
rootEl.innerHTML = "";
root = createRoot(rootEl);
await act(async () => {
  root.render(
    <LocaleProvider>
      <SettingsPanel initialTab="sandbox" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} />
    </LocaleProvider>,
  );
  await flushPromises();
});
await waitFor("sandbox content", () => document.body.textContent?.includes("Allow network") === true);
ok(groupExpanded("Advanced") === true, "initialTab deep-link auto-expands the group containing the leaf");
eq(activeLeafLabel(), "Sandbox", "initialTab deep-link activates the requested leaf");

await act(async () => {
  root.unmount();
});
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
