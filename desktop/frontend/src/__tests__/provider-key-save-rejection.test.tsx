// Run: tsx src/__tests__/provider-key-save-rejection.test.tsx
//
// Regression: saving a custom provider with a key whose SetProviderKey is
// rejected by the backend (a real persistence failure, e.g. a malformed
// credential value) must not surface an unhandled Promise rejection. The error
// converges to the SettingsPanel error banner, the provider is not saved, the
// editor stays open, and the key draft survives for retry.

import { JSDOM } from "jsdom";
import type { Root } from "react-dom/client";
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

// Track any unhandled rejection so the regression is observed directly.
const unhandledRejections: unknown[] = [];
process.on("unhandledRejection", (reason) => {
  unhandledRejections.push(reason);
});

const React = await import("react");
const { act } = React;
const { createRoot } = await import("react-dom/client");
const { SettingsPanel } = await import("../components/SettingsPanel");
const { LocaleProvider } = await import("../lib/i18n");

function emptySettings(): SettingsView {
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

function button(label: string): HTMLButtonElement {
  const match = Array.from(document.querySelectorAll("button")).find((item) => item.textContent?.trim() === label);
  if (!match) throw new Error(`missing button ${label}`);
  return match as HTMLButtonElement;
}

function inputByPlaceholder(placeholder: string): HTMLInputElement {
  const match = Array.from(document.querySelectorAll("input")).find((item) => item.getAttribute("placeholder") === placeholder);
  if (!match) throw new Error(`missing input with placeholder ${JSON.stringify(placeholder)}`);
  return match as HTMLInputElement;
}

function inputByPlaceholderContaining(substr: string): HTMLInputElement {
  const match = Array.from(document.querySelectorAll("input")).find((item) => item.getAttribute("placeholder")?.includes(substr));
  if (!match) throw new Error(`missing input with placeholder containing ${JSON.stringify(substr)}`);
  return match as HTMLInputElement;
}

function setInputValue(input: HTMLInputElement, value: string) {
  const valueSetter = Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")?.set;
  valueSetter?.call(input, value);
  input.dispatchEvent(new dom.window.InputEvent("input", { bubbles: true, inputType: "insertText", data: value }));
  input.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
}

console.log("\nprovider key save rejection");

let saveProviderCalls = 0;
let root: Root = null as unknown as Root;
{
  window.go = {
    main: {
      App: {
        Settings: async () => emptySettings(),
        FetchProviderModels: async () => [],
        SetProviderKey: async () => {
          throw new Error("credential value for provider contains a newline");
        },
        SaveProvider: async () => {
          saveProviderCalls += 1;
        },
      } as Partial<AppBindings> as AppBindings,
    },
  };

  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  root = createRoot(rootEl);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <SettingsPanel initialTab="models" desktopPlatform="windows" onClose={() => {}} onChanged={() => {}} />
      </LocaleProvider>,
    );
    await flushPromises();
  });
  await waitFor("add model service", () => document.body.textContent?.includes("+ Add model service") === true);

  await act(async () => {
    button("+ Add model service").click();
    await flushPromises();
  });
  await waitFor("custom provider tab", () => document.body.textContent?.includes("Custom provider") === true);

  await act(async () => {
    button("Custom provider").click();
    await flushPromises();
  });

  await act(async () => {
    setInputValue(inputByPlaceholder(""), "custom-proxy");
    setInputValue(inputByPlaceholderContaining("base_url"), "https://proxy.example.com/v1");
    setInputValue(inputByPlaceholder("Enter API key (saved globally)"), "sk-test-draft");
    setInputValue(inputByPlaceholder("models (comma-separated)"), "gpt-test");
    await flushPromises();
  });

  const keyInput = inputByPlaceholder("Enter API key (saved globally)");
  await act(async () => {
    button("Save").click();
    await flushPromises();
  });

  await waitFor("provider key error", () => document.body.textContent?.includes("contains a newline") === true);

  ok(saveProviderCalls === 0, "key rejection stops the provider from being saved");
  ok(document.body.textContent?.includes("contains a newline") === true, "key rejection surfaces in the settings error banner");
  ok(button("Save") !== undefined, "editor stays open after key rejection");
  ok(keyInput.value === "sk-test-draft", "key draft is preserved for retry");
  ok(unhandledRejections.length === 0, "SetProviderKey rejection produces no unhandled rejection");
}

await act(async () => root.unmount());
document.body.innerHTML = '<div id="root"></div>';
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
