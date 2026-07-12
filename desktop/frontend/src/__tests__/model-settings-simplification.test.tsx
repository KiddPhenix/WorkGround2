// Run: tsx src/__tests__/model-settings-simplification.test.tsx

import { JSDOM } from "jsdom";
import type { Root } from "react-dom/client";
import type { AppBindings } from "../lib/bridge";
import type { ProviderView, SettingsView } from "../lib/types";

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

function deepSeekProvider(): ProviderView {
  return {
    name: "deepseek",
    builtIn: true,
    added: false,
    kind: "openai",
    baseUrl: "https://api.deepseek.com",
    modelsUrl: "",
    models: ["deepseek-v4-flash", "deepseek-v4-pro"],
    visionModels: [],
    visionModelsConfigured: false,
    default: "deepseek-v4-pro",
    apiKeyEnv: "DEEPSEEK_API_KEY",
    keySet: true,
    balanceUrl: "https://api.deepseek.com/user/balance",
    contextWindow: 1_000_000,
    reasoningProtocol: "",
    supportedEfforts: [],
    defaultEffort: "",
  };
}

function settingsWithProvider(provider?: ProviderView): SettingsView {
  return {
    defaultModel: provider ? `${provider.name}/${provider.default}` : "",
    plannerModel: "",
    subagentModel: "",
    subagentEffort: "",
    autoPlan: "off",
    providers: provider ? [provider] : [],
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
    desktopTheme: "dark",
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
    providerKinds: ["openai", "anthropic"],
    autoApproveTools: false,
    bypass: false,
  };
}

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
globalThis.InputEvent = dom.window.InputEvent;
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
window.scrollTo = () => {};

const React = await import("react");
const { act } = React;
const { createRoot } = await import("react-dom/client");
const { SettingsPanel } = await import("../components/SettingsPanel");
const { LocaleProvider } = await import("../lib/i18n");

function button(label: string): HTMLButtonElement {
  const match = Array.from(document.querySelectorAll("button")).find((item) => item.textContent?.trim() === label);
  if (!match) throw new Error(`missing button ${label}`);
  return match as HTMLButtonElement;
}

function buttonContaining(label: string): HTMLButtonElement {
  const match = Array.from(document.querySelectorAll("button")).find((item) => item.textContent?.includes(label));
  if (!match) throw new Error(`missing button containing ${label}`);
  return match as HTMLButtonElement;
}

async function renderSettings(settings: SettingsView, fetchModels: AppBindings["FetchProviderModels"]): Promise<Root> {
  window.go = {
    main: {
      App: {
        Settings: async () => settings,
        FetchProviderModels: fetchModels,
        SetDefaultModel: async (ref: string) => { settings.defaultModel = ref; },
        AddOfficialProviderAccess: async () => "",
        SaveProvider: async () => {},
        SetPlannerModel: async () => {},
        SetSubagentModel: async () => {},
        SetSubagentEffort: async () => {},
        SetMaxSubagentDepth: async () => {},
        SetAgentParams: async () => {},
        SetColdResumePrune: async () => {},
        SetReasoningLanguage: async () => {},
      } as Partial<AppBindings> as AppBindings,
    },
  };
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <SettingsPanel initialTab="models" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} />
      </LocaleProvider>,
    );
    await flushPromises();
  });
  await waitFor("model settings", () => document.body.textContent?.includes("Add model service") === true);
  return root;
}

console.log("\nmodel settings simplification");

const connected = settingsWithProvider(deepSeekProvider());
let fetchCalls = 0;
let root = await renderSettings(connected, async () => {
  fetchCalls += 1;
  return ["deepseek-v4-flash", "deepseek-v4-pro"];
});

ok(document.body.textContent?.includes("Model connected") === true, "configured built-in default provider is summarized as connected");
ok(document.body.textContent?.includes("Model is not configured") === false, "configured built-in default model remains selectable");
ok(document.body.textContent?.includes("Usage") === false, "legacy usage subtab is removed");
ok(document.body.textContent?.includes("Access") === false, "legacy access subtab is removed");
ok(button("+ Add model service").getAttribute("aria-expanded") === "false", "add model service is the primary collapsed entry");
ok(document.body.textContent?.includes("Model used for planning") === false, "advanced controls are hidden by default");

await act(async () => {
  buttonContaining("Advanced: complex tasks and multi-model collaboration").click();
  await flushPromises();
});
ok(document.body.textContent?.includes("Model used for planning") === true, "advanced controls expand on demand");

await act(async () => {
  button("+ Add model service").click();
  await flushPromises();
});
ok(document.body.textContent?.includes("Add provider") === true, "add flow opens in the current settings page");
ok(document.body.textContent?.includes("Name (e.g. deepseek-work)") === false, "official add flow hides the internal provider name");

await act(async () => root.unmount());
document.body.innerHTML = '<div id="root"></div>';

const unconfigured = settingsWithProvider();
root = await renderSettings(unconfigured, async () => []);
await act(async () => {
  button("+ Add model service").click();
  await flushPromises();
});
const keyInput = document.querySelector('input[type="password"]') as HTMLInputElement | null;
if (!keyInput) throw new Error("official key input did not render");
await act(async () => {
  const valueSetter = Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")?.set;
  valueSetter?.call(keyInput, "sk-test-draft");
  keyInput.dispatchEvent(new dom.window.InputEvent("input", { bubbles: true, inputType: "insertText", data: "sk-test-draft" }));
  keyInput.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
  await flushPromises();
  button("Add provider").click();
  await flushPromises();
});
await waitFor("failed add validation", () => document.body.textContent?.includes("could not be loaded") === true);
ok(document.body.textContent?.includes("Add provider") === true, "failed validation keeps the add flow open");
ok(keyInput.value === "sk-test-draft", "failed validation keeps the key draft for retry");

await act(async () => root.unmount());
document.body.innerHTML = '<div id="root"></div>';

const failing = settingsWithProvider({ ...deepSeekProvider(), added: true });
let manualFetchCalls = 0;
root = await renderSettings(failing, async () => {
  manualFetchCalls += 1;
  throw new Error("network unavailable");
});
await waitFor("background failure", () => document.body.textContent?.includes("using the last available list") === true);
manualFetchCalls = 0;

await act(async () => {
  button("Check connection").click();
  await flushPromises();
});
await waitFor("manual connection failure", () => document.body.textContent?.includes("Connection check failed") === true);
ok(document.body.textContent?.includes("deepseek-v4-pro") === true, "connection failure keeps the existing default model");
ok(button("Retry").disabled === false, "connection failure exposes a retry action");

await act(async () => {
  button("Retry").click();
  await flushPromises();
});
await waitFor("retry request", () => manualFetchCalls === 2);
ok(manualFetchCalls === 2, "retry performs a fresh connection check");

await act(async () => root.unmount());
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
