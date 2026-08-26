// Run: npx tsx src/__tests__/main-ui-i18n.test.tsx
//
// English-locale coverage for the deterministic main-session UI: the automatic
// blank-session title, Artifact Shelf, RuntimeConfigBar pills, work-intent
// control, and primary Send action. Chinese / Traditional Chinese parity is
// enforced at compile time by the Record<DictKey, string> dictionary types;
// title-source semantics are covered in session-title-contract.test.ts.

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { ArtifactShelf } from "../components/desktop-ui/ArtifactShelf";
import { RuntimeConfigBar, type ConnectionStatus, type RuntimeConfig } from "../components/desktop-ui/RuntimeConfigBar";
import { LocaleProvider } from "../lib/i18n";
import { en, type DictKey } from "../locales/en";
import type { ArtifactRecord } from "../store/artifacts";

// Deterministic English translator backed by the canonical dictionary (locale
// auto-detection would otherwise depend on the JSDOM navigator).
const enT = (key: DictKey, vars?: Record<string, string | number>): string => {
  const value = en[key] ?? key;
  if (!vars) return value;
  return value.replace(/\{(\w+)\}/g, (_, k) => (vars[k] !== undefined ? String(vars[k]) : `{${k}}`));
};

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1;
  else failed += 1;
}

function hasText(el: Element | null, text: string): boolean {
  return el?.textContent?.includes(text) ?? false;
}

// ── DOM setup — single root reused across all renders ──────────────────────

let _root: Root | null = null;
let _rootEl: Element | null = null;

function installDom() {
  const dom = new JSDOM(
    '<!doctype html><html><head></head><body><div id="root"></div></body></html>',
    { pretendToBeVisual: true, url: "http://localhost/" },
  );
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(dom.window.navigator, "language", { configurable: true, value: "en-US" });
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.Element = dom.window.Element;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.Event = dom.window.Event;
  globalThis.KeyboardEvent = dom.window.KeyboardEvent;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.localStorage = dom.window.localStorage;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  globalThis.ResizeObserver = class implements ResizeObserver {
    constructor(private callback: ResizeObserverCallback) {}
    observe() { /* no-op */ }
    unobserve() { /* no-op */ }
    disconnect() { /* no-op */ }
  } as unknown as typeof ResizeObserver;

  const style = document.createElement("style");
  style.textContent = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "../styles.css"), "utf8");
  document.head.appendChild(style);

  _rootEl = document.getElementById("root");
  if (!_rootEl) throw new Error("missing root");
}

function render(ui: React.ReactElement): Element {
  if (!_rootEl) throw new Error("DOM not installed");
  _root ??= createRoot(_rootEl);
  act(() => _root!.render(ui));
  return _rootEl;
}

function cleanup() {
  if (_root) {
    act(() => _root.unmount());
    _root = null;
  }
}

// ── Test data ──────────────────────────────────────────────────────────────

function artifact(overrides: Partial<ArtifactRecord> = {}): ArtifactRecord {
  return {
    artifactId: overrides.artifactId ?? "art-1",
    name: overrides.name ?? "app.exe",
    type: overrides.type ?? "binary",
    status: overrides.status ?? "available",
    sessionId: "session-1",
    relativePath: overrides.relativePath,
    lastVerifiedAt: overrides.lastVerifiedAt,
  };
}

const BASE_CONFIG: RuntimeConfig = {
  modelId: "DeepSeek-R1",
  contextPercent: 33,
  runtimeMode: "idle",
  collaborationMode: "normal",
  approvalMode: "ask",
};

function renderBar(config: RuntimeConfig, connectionStatus: ConnectionStatus, extra: Partial<React.ComponentProps<typeof RuntimeConfigBar>> = {}) {
  return render(
    <LocaleProvider>
      <RuntimeConfigBar
        config={config}
        connectionStatus={connectionStatus}
        hasQueue={false}
        onSwitchModel={async () => {}}
        onCycleCollaboration={() => {}}
        onSetApprovalMode={() => {}}
        {...extra}
      />
    </LocaleProvider>,
  );
}

// ── Tests ───────────────────────────────────────────────────────────────────

console.log("\nmain-session UI English localization");

installDom();

// Title: the dictionary renders the auto blank title as "New session".
ok(enT("topbar.newSession") === "New session", "dict: topbar.newSession is New session");

// Artifact Shelf — empty English state
{
  const container = render(
    <LocaleProvider><ArtifactShelf artifacts={[]} /></LocaleProvider>,
  );
  ok(hasText(container, "Artifacts 0"), "ArtifactShelf: empty shelf shows Artifacts 0");
  ok(hasText(container, "No artifacts yet"), "ArtifactShelf: empty shelf shows No artifacts yet");
  cleanup();
}

// Artifact Shelf — populated English state (count, all trigger, status labels)
{
  const container = render(
    <LocaleProvider><ArtifactShelf
      artifacts={[
        artifact({ artifactId: "a1", name: "app.exe", lastVerifiedAt: 2 }),
        artifact({ artifactId: "a2", name: "gen.exe", status: "generating", lastVerifiedAt: 1 }),
      ]}
    /></LocaleProvider>,
  );
  ok(hasText(container, "Artifacts 2"), "ArtifactShelf: populated shelf shows Artifacts 2");
  ok(hasText(container, "View all"), "ArtifactShelf: all trigger reads View all");
  ok(hasText(container, "Generating"), "ArtifactShelf: generating status reads Generating");
  cleanup();
}

// RuntimeConfigBar — idle English state
{
  const container = renderBar(BASE_CONFIG, "idle");
  ok(hasText(container, "DeepSeek-R1"), "RuntimeConfigBar: shows the dynamic model name unchanged");
  ok(hasText(container, "33%"), "RuntimeConfigBar: shows the dynamic context percent unchanged");
  ok(hasText(container, "Idle"), "RuntimeConfigBar: runtime pill reads Idle");
  ok(hasText(container, "Chat"), "RuntimeConfigBar: normal collaboration reads Chat");
  ok(hasText(container, "Approval: Ask"), "RuntimeConfigBar: approval pill reads Approval: Ask");
  ok(hasText(container, "Send"), "RuntimeConfigBar: primary action reads Send when idle");
  cleanup();
}

// RuntimeConfigBar — work-intent control visible when available
{
  const container = renderBar(BASE_CONFIG, "idle", { workSendAvailable: true, onWorkSendChange: () => {} });
  ok(hasText(container, "Start as work"), "RuntimeConfigBar: work-intent control reads Start as work");
  cleanup();
}

// RuntimeConfigBar — work surface collaboration pill reads Work
{
  const container = renderBar(BASE_CONFIG, "idle", { surfaceKind: "work" });
  ok(hasText(container, "Work"), "RuntimeConfigBar: work surface normal collaboration reads Work");
  ok(!hasText(container, "Chat"), "RuntimeConfigBar: work surface does not show Chat");
  cleanup();
}

// RuntimeConfigBar — plan and goal collaboration pills
{
  const plan = renderBar({ ...BASE_CONFIG, collaborationMode: "plan" }, "idle", { surfaceKind: "work" });
  ok(hasText(plan, "Plan"), "RuntimeConfigBar: plan collaboration reads Plan");
  cleanup();
  const goal = renderBar({ ...BASE_CONFIG, collaborationMode: "goal" }, "idle");
  ok(hasText(goal, "Goal"), "RuntimeConfigBar: goal collaboration reads Goal");
  cleanup();
}

// RuntimeConfigBar — approval values
{
  const auto = renderBar({ ...BASE_CONFIG, approvalMode: "auto" }, "idle");
  ok(hasText(auto, "Approval: Auto"), "RuntimeConfigBar: auto approval reads Approval: Auto");
  cleanup();
  const yolo = renderBar({ ...BASE_CONFIG, approvalMode: "yolo" }, "idle");
  ok(hasText(yolo, "Approval: Allow all"), "RuntimeConfigBar: yolo approval reads Approval: Allow all");
  cleanup();
}

// RuntimeConfigBar — runtime states
{
  const running = renderBar({ ...BASE_CONFIG, runtimeMode: "foreground" }, "foreground");
  ok(hasText(running, "Running"), "RuntimeConfigBar: foreground runtime reads Running");
  ok(hasText(running, "Add to queue"), "RuntimeConfigBar: primary action reads Add to queue while foreground");
  cleanup();
  const waiting = renderBar({ ...BASE_CONFIG, runtimeMode: "waiting_user" }, "waiting_user");
  ok(hasText(waiting, "Waiting for you"), "RuntimeConfigBar: waiting_user runtime reads Waiting for you");
  cleanup();
  const background = renderBar({ ...BASE_CONFIG, runtimeMode: "background_only" }, "background_only");
  ok(hasText(background, "Background"), "RuntimeConfigBar: background runtime reads Background");
  cleanup();
  const cancelling = renderBar({ ...BASE_CONFIG, runtimeMode: "cancelling" }, "cancelling");
  ok(hasText(cancelling, "Cancelling"), "RuntimeConfigBar: cancelling runtime reads Cancelling");
  cleanup();
}

// RuntimeConfigBar — work-intent selected state
{
  const container = renderBar(BASE_CONFIG, "idle", { workSendAvailable: true, workSendSelected: true, onWorkSendChange: () => {} });
  ok(hasText(container, "Sending as work"), "RuntimeConfigBar: selected work intent reads Sending as work");
  cleanup();
}

// Dictionary parity: zh / zh-TW contain every en key (compile-time type plus a
// runtime spot check that the deterministic English strings exist).
ok(enT("artifact.shelfCount", { n: 0 }) === "Artifacts 0", "dict: Artifacts 0 renders with count placeholder");
ok(enT("artifact.empty") === "No artifacts yet", "dict: empty artifact copy is No artifacts yet");
ok(enT("runtimeBar.workSend.off") === "Start as work", "dict: work intent off label is Start as work");
ok(enT("runtimeBar.action.send") === "Send", "dict: primary action label is Send");

console.log(`\nResults: ${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
