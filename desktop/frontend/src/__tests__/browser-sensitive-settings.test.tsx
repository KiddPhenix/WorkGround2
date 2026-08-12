// Run: tsx src/__tests__/browser-sensitive-settings.test.tsx
// Focused contract test for browser permissions and launch settings on the
// permissions page: render, defaults, bridge arguments, and refresh.

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { SettingsPanel } from "../components/SettingsPanel";
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
    permissions: {
      mode: "ask",
      allow: [],
      ask: [],
      deny: [],
      browser: { allowPasswordInput: true, allowFileUpload: true },
    },
    browserLaunch: { incognito: false },
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

console.log("\nbrowser sensitive settings");

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

const saved = baseSettings();
let settingsCalls = 0;
const permissionCalls: Array<{ allowPasswordInput: boolean; allowFileUpload: boolean }> = [];
const launchCalls: Array<{ incognito: boolean }> = [];
window.go = {
  main: {
    App: {
      Settings: async () => {
        settingsCalls += 1;
        return JSON.parse(JSON.stringify(saved)) as SettingsView;
      },
      SetBrowserPermissions: async (b: { allowPasswordInput: boolean; allowFileUpload: boolean }) => {
        permissionCalls.push({ allowPasswordInput: b.allowPasswordInput, allowFileUpload: b.allowFileUpload });
        saved.permissions.browser = { allowPasswordInput: b.allowPasswordInput, allowFileUpload: b.allowFileUpload };
      },
      SetBrowserLaunch: async (b: { incognito: boolean }) => {
        launchCalls.push({ incognito: b.incognito });
        saved.browserLaunch = { incognito: b.incognito };
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
      <SettingsPanel initialTab="permissions" onClose={() => {}} onChanged={() => {}} />
    </LocaleProvider>,
  );
  await flushPromises();
});

await waitFor("permissions page renders", () => document.body.textContent?.includes("Browser sensitive operations") === true);

ok(document.body.textContent?.includes("Allow password input") === true, "password switch label renders");
ok(document.body.textContent?.includes("Allow local file upload") === true, "file upload switch label renders");
ok(document.body.textContent?.includes("Open in incognito mode") === true, "incognito switch label renders");
ok(document.body.textContent?.includes("Browser launch") === true, "browser launch section renders");

function segmentFor(label: string): HTMLElement {
  const fields = Array.from(document.querySelectorAll(".settings-field")) as HTMLElement[];
  const field = fields.find((f) => f.textContent?.includes(label));
  if (!field) throw new Error(`settings field for ${label} not found`);
  const seg = field.querySelector(".set-seg") as HTMLElement | null;
  if (!seg) throw new Error(`toggle segment for ${label} not found`);
  return seg;
}

function segmentOn(seg: HTMLElement): boolean {
  return Boolean(seg.querySelector(".set-seg__btn--on")?.textContent === "On");
}

const passwordSeg = segmentFor("Allow password input");
const uploadSeg = segmentFor("Allow local file upload");
const incognitoSeg = segmentFor("Open in incognito mode");
ok(segmentOn(passwordSeg) === true, "password switch defaults to on (allowed)");
ok(segmentOn(uploadSeg) === true, "file upload switch defaults to on (allowed)");
ok(segmentOn(incognitoSeg) === false, "incognito switch defaults to off");

// Flip password off; the file switch must be preserved.
const passwordOff = Array.from(passwordSeg.querySelectorAll(".set-seg__btn")).find((b) => b.textContent?.trim() === "Off") as HTMLButtonElement;
await act(async () => {
  passwordOff.click();
  await flushPromises();
});
await waitFor("password switch reflects saved value", () => segmentOn(segmentFor("Allow password input")) === false);
eq(permissionCalls[0], { allowPasswordInput: false, allowFileUpload: true }, "password toggle calls SetBrowserPermissions with file switch preserved");
ok(segmentOn(segmentFor("Allow local file upload")) === true, "file upload switch untouched after password toggle");
ok(segmentOn(segmentFor("Open in incognito mode")) === false, "incognito switch untouched after password toggle");
ok(settingsCalls >= 2, "settings refreshed after save");

// Flip file upload off too.
const uploadOff = Array.from(uploadSeg.querySelectorAll(".set-seg__btn")).find((b) => b.textContent?.trim() === "Off") as HTMLButtonElement;
await act(async () => {
  uploadOff.click();
  await flushPromises();
});
await waitFor("file switch reflects saved value", () => segmentOn(segmentFor("Allow local file upload")) === false);
eq(permissionCalls[1], { allowPasswordInput: false, allowFileUpload: false }, "file toggle calls SetBrowserPermissions with both switches");

// Flip incognito on; both sensitive switches must be preserved.
const incognitoSegFresh = segmentFor("Open in incognito mode");
const incognitoOn = Array.from(incognitoSegFresh.querySelectorAll(".set-seg__btn")).find((b) => b.textContent?.trim() === "On") as HTMLButtonElement;
await act(async () => {
  incognitoOn.click();
  await flushPromises();
});
await waitFor("incognito switch reflects saved value", () => segmentOn(segmentFor("Open in incognito mode")) === true);
eq(launchCalls[0], { incognito: true }, "incognito toggle calls SetBrowserLaunch");
eq(permissionCalls.length, 2, "incognito toggle does not rewrite browser permissions");
ok(segmentOn(segmentFor("Allow password input")) === false, "password switch untouched after incognito toggle");
ok(segmentOn(segmentFor("Allow local file upload")) === false, "file upload switch untouched after incognito toggle");

await act(async () => {
  root.unmount();
});
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
