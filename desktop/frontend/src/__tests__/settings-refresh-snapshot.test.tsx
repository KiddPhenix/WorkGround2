// Run: tsx src/__tests__/settings-refresh-snapshot.test.tsx

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import {
  SettingsPanel,
  providerBaseURLFromChatURL,
  providerChatURLPreview,
  providerEditorEffectiveKind,
} from "../components/SettingsPanel";
import { LocaleProvider } from "../lib/i18n";
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

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) {
    ok(true, label);
  } else {
    ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    await act(async () => {
      await flushPromises();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

function baseSettings(displayMode: "standard" | "compact" = "standard"): SettingsView {
  return {
    defaultModel: "",
    plannerModel: "",
    subagentModel: "",
    subagentEffort: "",
    autoPlan: "off",
    providers: [],
    officialProviders: [],
    permissions: { mode: "ask", allow: [], ask: [], deny: [] },
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
    displayMode,
    composerSubmitKey: "enter",
    statusBarStyle: "text",
    statusBarItems: ["model", "workspace", "git_branch", "cache", "balance"],
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

console.log("\nsettings refresh snapshot");

eq(providerEditorEffectiveKind(true, "anthropic", ["anthropic", "openai"]), "openai", "new custom providers ignore sorted providerKinds and default to OpenAI");
eq(providerEditorEffectiveKind(false, "anthropic", ["anthropic", "openai"]), "anthropic", "existing providers preserve their stored kind");
eq(providerChatURLPreview("https://proxy.example.com/v1", "", false), "https://proxy.example.com/v1/chat/completions", "base URL mode previews chat completions URL");
eq(providerChatURLPreview("", "https://proxy.example.com/custom/chat", true), "https://proxy.example.com/custom/chat", "full URL mode previews configured URL");
eq(providerBaseURLFromChatURL("https://proxy.example.com/v1/chat/completions"), "https://proxy.example.com/v1", "chat URL derives base URL for model discovery");

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

const updatedSettings = baseSettings("standard");
updatedSettings.defaultToolApprovalMode = "auto";
const settingsSnapshots = [baseSettings("compact"), updatedSettings];
let settingsCalls = 0;
let setApprovalModeCalls = 0;
let onChangedSettings: SettingsView | undefined;

window.go = {
  main: {
    App: {
      Settings: async () => settingsSnapshots[Math.min(settingsCalls++, settingsSnapshots.length - 1)],
      SetDefaultToolApprovalMode: async () => {
        setApprovalModeCalls += 1;
      },
    } as Partial<AppBindings> as AppBindings,
  },
};

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("missing root");
const root = createRoot(rootEl);

await act(async () => {
  root.render(
    <LocaleProvider>
      <SettingsPanel
        initialTab="general"
        onClose={() => {}}
        onChanged={(settings?: SettingsView) => {
          onChangedSettings = settings;
        }}
      />
    </LocaleProvider>,
  );
  await flushPromises();
});

ok(document.body.textContent?.includes("Desktop style") === false, "general settings hide desktop style selection");
ok(document.body.textContent?.includes("Transcript display mode") === false, "general settings hide transcript display mode selection");
ok(document.body.textContent?.includes("Bottom status bar style") === false, "general settings hide status bar style selection");
ok(document.body.textContent?.includes("Status bar items") === false, "general settings hide status bar item selection");

const askButton = Array.from(document.querySelectorAll("button")).find((button) => button.textContent?.trim() === "Ask first") as HTMLButtonElement | undefined;
if (!askButton) throw new Error("ask-first approval button did not render");

await act(async () => {
  askButton.focus();
  await flushPromises();
});
ok(document.querySelector('[role="tooltip"]')?.textContent?.includes("Protected tool calls") === true, "approval option exposes its explanation on focus or hover");

const autoApproveButton = Array.from(document.querySelectorAll("button")).find((button) => button.textContent?.trim() === "Auto approve") as HTMLButtonElement | undefined;
if (!autoApproveButton) throw new Error("auto-approve button did not render");

await act(async () => {
  autoApproveButton.click();
  await flushPromises();
});

eq(setApprovalModeCalls, 1, "default approval mode mutation is invoked once");
eq(settingsCalls, 2, "settings panel reads Settings only for initial load and post-save reload");
ok(onChangedSettings?.defaultToolApprovalMode === "auto", "onChanged receives the post-save SettingsView snapshot");

await act(async () => {
  root.unmount();
});

const retryRootEl = document.createElement("div");
document.body.appendChild(retryRootEl);
const retryRoot = createRoot(retryRootEl);
let failingSettingsCalls = 0;
window.go = {
  main: {
    App: {
      Settings: async () => {
        failingSettingsCalls += 1;
        if (failingSettingsCalls === 1) throw new Error("/Users/example/.WorkGround2/settings.toml: permission denied");
        return baseSettings("standard");
      },
    } as Partial<AppBindings> as AppBindings,
  },
};

await act(async () => {
  retryRoot.render(
    <LocaleProvider>
      <SettingsPanel
        initialTab="general"
        onClose={() => {}}
        onChanged={() => {}}
      />
    </LocaleProvider>,
  );
  await flushPromises();
});
await waitFor("settings load failure", () => Boolean(document.querySelector(".banner--error")));

ok(document.body.textContent?.includes("Settings could not be loaded.") === true, "failed initial settings load shows a visible error");
ok(document.body.textContent?.includes("Loading…") === false, "failed initial settings load stops showing the loading state");

const retryButton = Array.from(document.querySelectorAll("button")).find((button) => button.textContent?.trim() === "Retry") as HTMLButtonElement | undefined;
if (!retryButton) throw new Error("settings retry button did not render");

await act(async () => {
  retryButton.click();
  await flushPromises();
});
await waitFor("settings retry success", () => Boolean(Array.from(document.querySelectorAll("button")).find((button) => button.textContent?.trim() === "Ask first")));

eq(failingSettingsCalls, 2, "settings retry calls Settings again");
ok(document.body.textContent?.includes("Settings could not be loaded.") === false, "settings retry clears the load error");

await act(async () => {
  retryRoot.unmount();
});
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
