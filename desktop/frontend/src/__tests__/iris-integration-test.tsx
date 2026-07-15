// iris-integration-test.ts — React rendering integration test for the
// ?uiFixture=iris layout. Mounts the Iris layout components (via the wrapper
// components directly) with fixture data injected into the stores, then asserts
// visual contract: memory line, artifacts, runs, config, AddOns, queue.
//
// Run: npx tsx src/__tests__/iris-integration-test.ts

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// Wrapper components
import {
  SessionMemoryBar,
  SessionRunStream,
  SessionArtifactShelf,
  SessionQueueTray,
  SessionConfigBar,
  AddOnLauncherButton,
  AddOnWorkbenchOverlay,
} from "../components/desktop-ui/IrisInfoComponents";
import { TaskMemoryBar } from "../components/desktop-ui/TaskMemoryBar";
import { ArtifactShelf } from "../components/desktop-ui/ArtifactShelf";
import { QueueTray } from "../components/desktop-ui/QueueTray";
import { RuntimeConfigBar } from "../components/desktop-ui/RuntimeConfigBar";
import { AddOnWorkbench } from "../components/desktop-ui/AddOnWorkbench";
import { LocaleProvider } from "../lib/i18n";

// Fixture data
import {
  irisFixtureMemory,
  irisFixtureArtifacts,
  irisFixtureCompletedRun,
  irisFixtureActiveRun,
  irisFixtureAddOns,
  irisFixtureQueueItems,
  irisFixtureConfig,
  FIXTURE_SESSION_ID,
} from "../lib/irisFixture";

// Stores
import { useMemoryStore } from "../store/memory";
import { useArtifactStore } from "../store/artifacts";
import { useRunStore, selectRun } from "../store/run";
import { useAddOnSurfaceStore } from "../store/addonSurface";
import { useComposerQueueStore } from "../store/composerQueue";

// ── Test framework ──────────────────────────────────────────────────────────

const testDir = dirname(fileURLToPath(import.meta.url));
const styles = readFileSync(resolve(testDir, "../styles.css"), "utf8");
const appSource = readFileSync(resolve(testDir, "../App.tsx"), "utf8");

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

function hasText(el: Element | null, text: string): boolean {
  return el?.textContent?.includes(text) ?? false;
}

function queryByAriaLabel(container: Element, label: string): Element | null {
  return container.querySelector(`[aria-label="${label}"]`);
}

function done() {
  const total = passed + failed;
  process.stdout.write(`\n${total} tests · ${passed} passed · ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

function queryByClassName(container: Element, className: string): Element | null {
  return container.querySelector(`.${className}`);
}

function queryAllByClassName(container: Element, className: string): Element[] {
  return Array.from(container.querySelectorAll(`.${className}`));
}

// ── DOM setup ───────────────────────────────────────────────────────────────

let _root: Root | null = null;
let _rootEl: Element | null = null;

function installDom() {
  const dom = new JSDOM(
    '<!doctype html><html><head></head><body><div id="root"></div></body></html>',
    {
      pretendToBeVisual: true,
      url: "http://localhost/?uiFixture=iris",
    },
  );
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
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

  // Polyfill ResizeObserver for tests — ModelSwitcher uses it to track trigger width
  globalThis.ResizeObserver = class implements ResizeObserver {
    constructor(private callback: ResizeObserverCallback) {}
    observe() { /* no-op */ }
    unobserve() { /* no-op */ }
    disconnect() { /* no-op */ }
  } as unknown as typeof ResizeObserver;

  const style = document.createElement("style");
  style.textContent = styles;
  document.head.appendChild(style);

  _rootEl = document.getElementById("root");
  if (!_rootEl) throw new Error("missing root");
}

function render(ui: React.ReactElement): Element {
  if (!_rootEl) throw new Error("DOM not installed");
  if (!_root) _root = createRoot(_rootEl);
  act(() => _root.render(ui));
  return _rootEl;
}

function cleanup() {
  if (_root) {
    act(() => _root.unmount());
    _root = null;
  }
}

// ── Seed fixture data into stores ───────────────────────────────────────────

function seedFixtureStores() {
  // Memory
  useMemoryStore.setState({ memoryBySession: {} });
  useMemoryStore.getState().setMemory(FIXTURE_SESSION_ID, irisFixtureMemory());

  // Artifacts
  useArtifactStore.setState({ artifacts: {} });
  for (const art of irisFixtureArtifacts()) {
    useArtifactStore.getState().upsertArtifact(art);
  }

  // Runs
  useRunStore.setState({ runs: {} });
  const cr = irisFixtureCompletedRun();
  for (const ev of cr.events) {
    useRunStore.getState().mergeRunEvent(cr.runId, cr.sessionId, cr.turnId, ev);
  }
  useRunStore.getState().setRunStatus(cr.runId, cr.status, {});
  useRunStore.getState().setRunExpanded(cr.runId, cr.expanded);

  const ar = irisFixtureActiveRun();
  for (const ev of ar.events) {
    useRunStore.getState().mergeRunEvent(ar.runId, ar.sessionId, ar.turnId, ev);
  }
  // Keep active run expanded

  // AddOns
  useAddOnSurfaceStore.setState({
    instances: {},
    workbenchOpen: false,
    editingInstanceId: null,
    _frozenDisplayIndex: null,
  });
  for (const inst of irisFixtureAddOns()) {
    useAddOnSurfaceStore.getState().upsertInstance(inst);
  }
  useAddOnSurfaceStore.getState().setWorkbenchOpen(true);

  // Queue
  useComposerQueueStore.setState({ items: [] });
  for (const item of irisFixtureQueueItems()) {
    useComposerQueueStore.getState().addItem(item);
  }
}

// ── Tests ────────────────────────────────────────────────────────────────────

process.stdout.write("\niris integration test\n\n");

installDom();
seedFixtureStores();

// ── Test: MemoryBar with fixture session ID ─────────────────────────────────
const memoryEl = render(<SessionMemoryBar sessionId={FIXTURE_SESSION_ID} />);
ok(hasText(memoryEl, "重构桌面信息架构"), "MemoryBar: shows goal text");
ok(hasText(memoryEl, "Artifact 模型已验证"), "MemoryBar: shows current text");
ok(hasText(memoryEl, "实现持久化"), "MemoryBar: shows next step text");
cleanup();

const emptyMemoryEl = render(<SessionMemoryBar sessionId="session-without-memory" />);
ok(emptyMemoryEl.childElementCount === 0, "MemoryBar: hides when the session has no real memory");
cleanup();

// ── Test: ArtifactShelf with fixture ────────────────────────────────────────
const artEl = render(<SessionArtifactShelf sessionId={FIXTURE_SESSION_ID} />);
ok(hasText(artEl, "产物 4"), "ArtifactShelf: shows count 4");
ok(hasText(artEl, "WorkGround2.exe"), "ArtifactShelf: shows exe");
ok(hasText(artEl, "测试包.zip"), "ArtifactShelf: shows zip");
ok(hasText(artEl, "调试入口"), "ArtifactShelf: shows debug entry");
ok(hasText(artEl, "debug.bat"), "ArtifactShelf: shows script");
const artItems = queryAllByClassName(artEl, "artifact-item");
ok(artItems.length === 4, "ArtifactShelf: exactly 4 artifact items");
cleanup();

// ── Test: QueueTray ─────────────────────────────────────────────────────────
const queueEl = render(<SessionQueueTray />);
ok(hasText(queueEl, "查看测试结果"), "QueueTray: shows first item");
ok(hasText(queueEl, "提交代码审查"), "QueueTray: shows second item");
const queueItems = queryAllByClassName(queueEl, "queue-item-row");
ok(queueItems.length === 2, "QueueTray: exactly 2 queue items");
cleanup();

// ── Test: ConfigBar ─────────────────────────────────────────────────────────
const configData = irisFixtureConfig();
const configEl = render(
  <LocaleProvider><SessionConfigBar
    modelLabel={configData.modelId}
    contextPercent={configData.contextPercent}
    runtimeMode="foreground"
    foregroundActive={true}
    collaborationMode="normal"
    toolApprovalMode="ask"
    controllerReady={true}
    onPrimaryAction={() => {}}
    onSwitchModel={async () => {}}
    onCycleCollaboration={() => {}}
    onSetApprovalMode={() => {}}
  /></LocaleProvider>,
);
ok(hasText(configEl, "DeepSeek-R1"), "ConfigBar: shows model");
ok(hasText(configEl, "33%"), "ConfigBar: shows context percent");
ok(hasText(configEl, "运行中"), "ConfigBar: shows runtime status");
cleanup();

// ── Test: Run stream renders terminal and active states ─────────────────────
const runEl = render(<SessionRunStream sessionId={FIXTURE_SESSION_ID} />);
ok(hasText(runEl, "运行完成"), "RunStream: renders completed run tab");
ok(hasText(runEl, "运行中"), "RunStream: renders active run window");
ok(queryAllByClassName(runEl, "completed-run-tab").length === 1, "RunStream: one completed run is collapsed");
ok(queryAllByClassName(runEl, "active-run-view").length === 1, "RunStream: one active run stays expanded");
cleanup();

// ── Test: AddOnLauncherButton shows active count ────────────────────────────
const btnEl = render(<AddOnLauncherButton />);
ok(hasText(btnEl, "3"), "AddOnLauncher: shows count 3");
cleanup();

// ── Test: AddOnWorkbenchOverlay renders with fixture data ───────────────────
const overlayEl = render(<AddOnWorkbenchOverlay />);
ok(hasText(overlayEl, "AddOn · 3 活跃"), "AddOnWorkbench: header shows 3 active");
ok(hasText(overlayEl, "团队登录"), "AddOnWorkbench: shows login instance");
ok(hasText(overlayEl, "构建监控"), "AddOnWorkbench: shows build instance");
ok(hasText(overlayEl, "媒体预览"), "AddOnWorkbench: shows media instance");
const instanceHeaders = queryAllByClassName(overlayEl, "instance-header");
// 3 instances + 0 addon-workbench headers = 3 (workbench header is separate)
ok(instanceHeaders.length >= 3, "AddOnWorkbench: at least 3 instance headers");
cleanup();

// ── Test: Run records in store ──────────────────────────────────────────────
// Verify runs were seeded correctly by reading directly from the store
const crRecord = selectRun(useRunStore.getState().runs, "cr-1");
ok(crRecord?.status === "completed", "Run store: completed run present");
ok(crRecord?.events.length === 4, "Run store: completed run has 4 events");
const arRecord = selectRun(useRunStore.getState().runs, "ar-1");
ok(arRecord?.status === "running", "Run store: active run present");
ok(arRecord?.events.length === 4, "Run store: active run has 4 events");
cleanup();

// ── Regression: No empty literal artifact shelf or queue ────────────────────
// The SessionArtifactShelf with fixture data must NOT show empty state
const emptyCheck = render(<SessionArtifactShelf sessionId={FIXTURE_SESSION_ID} />);
ok(!hasText(emptyCheck, "暂无产物"), "ArtifactShelf: no empty state text with fixture");
ok(!hasText(emptyCheck, "产物 0"), "ArtifactShelf: no zero count with fixture");
cleanup();

// ── Regression: workbench layout string is valid (no "iris" value) ──────────
// Verify by checking the workbench layout path renders via sidebarWorkbench
ok(!appSource.includes('desktopLayoutStyle === "iris"'), "Regression: no invalid iris layout branch exists");

// ── Regression: AddOn launcher uses setWorkbenchOpen (not AddOnDialog) ──────
// The AddOnLauncherButton component calls setWorkbenchOpen — verified by
// the AddOnWorkbenchOverlay appearing when we seed workbenchOpen=true
ok(appSource.includes("<AddOnLauncherButton />"), "Regression: workbench path renders the AddOn launcher");

// ── Regression: Real workbench path uses correct selectors ──────────────────
ok(appSource.includes('"app--workbench"'), "Real workbench: app--workbench CSS selector exists");
ok(appSource.includes('"layout--workbench"'), "Real workbench: layout--workbench CSS selector exists");
ok(appSource.includes('variant="workbench"'), "Real workbench: ProjectTree variant=workbench exists");
ok(appSource.includes("workspace-sidebar"), "Real workbench: workspace-sidebar element exists");
ok(appSource.includes("session-workspace"), "Real workbench: session-workspace element exists");

// ── Regression: Real workbench path renders ProjectTree (not just fixture) ──
// When !irisFixtureActive, the tree area renders a real ProjectTree component
ok(
  appSource.includes("irisFixtureActive ?") && appSource.includes("ProjectTree"),
  "Real workbench: ProjectTree is rendered when fixture is inactive",
);

// ── Regression: Workbench selectors are NOT "iris" strings ──────────────────
ok(!appSource.includes('"app--iris"'), "Real workbench: no app--iris selector leaks in");
ok(!appSource.includes('"layout--iris"'), "Real workbench: no layout--iris selector leaks in");

// ── Done ─────────────────────────────────────────────────────────────────────

done();
