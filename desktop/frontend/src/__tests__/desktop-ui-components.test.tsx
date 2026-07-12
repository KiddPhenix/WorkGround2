// Run: npx tsx src/__tests__/desktop-ui-components.test.tsx
//
// Tests for the seven desktop UI presentational primitives:
//   TaskMemoryBar, RunBlock, ArtifactShelf, QueueTray,
//   RuntimeConfigBar, AddOnWorkbench, and their sub-components.
//
// Each test renders the component via JSDOM and validates DOM output.

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { TaskMemoryBar } from "../components/desktop-ui/TaskMemoryBar";
import { RunBlock, CompletedRunTab, ActiveRunView } from "../components/desktop-ui/RunBlock";
import { ArtifactShelf, ArtifactItem } from "../components/desktop-ui/ArtifactShelf";
import { QueueTray } from "../components/desktop-ui/QueueTray";
import { RuntimeConfigBar, derivePrimaryActionLabel } from "../components/desktop-ui/RuntimeConfigBar";
import { LocaleProvider } from "../lib/i18n";
import { AddOnWorkbench, WorkbenchHeader, InstanceHeader, AddOnInstanceView } from "../components/desktop-ui/AddOnWorkbench";
import { recentSessionSummary, SessionMemoryBar } from "../components/desktop-ui/IrisInfoComponents";

import type { MemoryLine } from "../store/memory";
import type { RunRecord, RunEvent } from "../store/run";
import type { ArtifactRecord } from "../store/artifacts";
import type { QueueItem } from "../store/composerQueue";
import type { RuntimeConfig, ConnectionStatus } from "../components/desktop-ui/RuntimeConfigBar";
import type { AddOnInstance, AddOnDensity, AddOnInstanceStatus } from "../store/addonSurface";

// ── Test framework ──────────────────────────────────────────────────────────

const testDir = dirname(fileURLToPath(import.meta.url));
const styles = readFileSync(resolve(testDir, "../styles.css"), "utf8");

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
  if (actual === expected) ok(true, label);
  else ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

function hasText(el: Element | null, text: string): boolean {
  return el?.textContent?.includes(text) ?? false;
}

function queryByAriaLabel(container: Element, label: string): Element | null {
  return container.querySelector(`[aria-label="${label}"]`);
}

// ── DOM setup — single root reused across all renders ──────────────────────

let _root: Root | null = null;
let _rootEl: Element | null = null;

function installDom() {
  const dom = new JSDOM(
    '<!doctype html><html><head></head><body><div id="root"></div></body></html>',
    {
      pretendToBeVisual: true,
      url: "http://localhost/",
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

  // Polyfill ResizeObserver — ModelSwitcher uses it to track trigger width
  globalThis.ResizeObserver = class implements ResizeObserver {
    constructor(private callback: ResizeObserverCallback) {}
    observe() { /* no-op */ }
    unobserve() { /* no-op */ }
    disconnect() { /* no-op */ }
  } as unknown as typeof ResizeObserver;

  // Polyfill scrollIntoView — not available in the JSDOM version used here
  if (typeof Element.prototype.scrollIntoView !== "function") {
    Element.prototype.scrollIntoView = () => {};
  }

  const style = document.createElement("style");
  style.textContent = styles;
  document.head.appendChild(style);

  _rootEl = document.getElementById("root");
  if (!_rootEl) throw new Error("missing root");
}

function render(ui: React.ReactElement): Element {
  if (!_rootEl) throw new Error("DOM not installed");
  // Create a single root on first render; unmount + re-render for each test.
  if (!_root) _root = createRoot(_rootEl);
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

const BASE_MEMORY: MemoryLine = {
  goal: "实现登录功能",
  current: "编写 API 接口",
  nextStep: "添加单元测试",
};

const BASE_EVENTS: RunEvent[] = [
  { eventId: "e1", content: "读取配置文件", stepLabel: "配置" },
  { eventId: "e2", content: "连接数据库", stepLabel: "数据库" },
  { eventId: "e3", content: "执行查询", stepLabel: "查询" },
];

const COMPLETED_RUN: RunRecord = {
  runId: "run-1",
  sessionId: "sess-1",
  turnId: "turn-1",
  status: "completed",
  events: BASE_EVENTS,
  expanded: false,
  startedAt: 1000,
  completedAt: 5000,
};

const RUNNING_RUN: RunRecord = {
  runId: "run-2",
  sessionId: "sess-1",
  turnId: "turn-1",
  status: "running",
  events: BASE_EVENTS,
  expanded: true,
  startedAt: 1000,
};

const FAILED_RUN: RunRecord = {
  runId: "run-3",
  sessionId: "sess-1",
  turnId: "turn-1",
  status: "failed",
  events: BASE_EVENTS,
  expanded: false,
  startedAt: 1000,
  completedAt: 3000,
  errorMessage: "连接超时",
};

const BASE_ARTIFACT: ArtifactRecord = {
  artifactId: "art-1",
  name: "app.exe",
  type: "binary",
  status: "available",
  sessionId: "sess-1",
};

const STALE_ARTIFACT: ArtifactRecord = {
  artifactId: "art-2",
  name: "config.yaml",
  type: "code",
  status: "stale",
  sessionId: "sess-1",
};

const QUEUE_ITEMS: QueueItem[] = [
  { queueItemId: "q1", requestId: "req-1", content: "优化数据库查询", createdAt: 1000 },
  { queueItemId: "q2", requestId: "req-2", content: "添加错误处理", createdAt: 2000 },
  { queueItemId: "q3", requestId: "req-3", content: "编写文档", createdAt: 3000 },
];

const BASE_CONFIG: RuntimeConfig = {
  modelId: "DeepSeek-R1",
  contextPercent: 33,
  runtimeStatus: "运行中",
  collaborationMode: "normal" as const,
  approvalMode: "ask" as const,
};

const BASE_ADDON_INSTANCE: AddOnInstance = {
  instanceId: "addon-1",
  pluginId: "plugin-a",
  panelId: "panel-1",
  scope: "session",
  status: "active",
  density: "focus",
  pinned: false,
  activationOrder: 1,
  title: "代码分析器",
};

// ── Tests ───────────────────────────────────────────────────────────────────

console.log("\ndesktop-ui presentational components");

installDom();

// ── TaskMemoryBar ───────────────────────────────────────────────────────────

{
  const container = render(<TaskMemoryBar memoryLine={null} />);
  ok(container.childElementCount === 0, "TaskMemoryBar: renders nothing when memoryLine is null");
  cleanup();
}

{
  const container = render(<TaskMemoryBar memoryLine={{ goal: "兼容旧任务记忆" }} />);
  ok(hasText(container, "兼容旧任务记忆"), "TaskMemoryBar: accepts legacy partial memory without crashing");
  ok(container.querySelectorAll(".task-memory-bar__segment").length === 1, "TaskMemoryBar: renders only populated legacy fields");
  cleanup();
}

{
  const container = render(<TaskMemoryBar memoryLine={BASE_MEMORY} />);
  ok(hasText(container, "目标"), "TaskMemoryBar: shows 目标 label");
  ok(hasText(container, "实现登录功能"), "TaskMemoryBar: shows goal value");
  ok(hasText(container, "当前"), "TaskMemoryBar: shows 当前 label");
  ok(hasText(container, "编写 API 接口"), "TaskMemoryBar: shows current value");
  ok(hasText(container, "下一步"), "TaskMemoryBar: shows 下一步 label");
  ok(hasText(container, "添加单元测试"), "TaskMemoryBar: shows nextStep value");
  ok(container.querySelector(".task-memory-bar__segment") !== null, "TaskMemoryBar: uses segment class");
  cleanup();
}

{
  const container = render(<TaskMemoryBar memoryLine={{ goal: "真实任务", current: "", nextStep: "", goalSource: "user_prompt", revision: 1 }} />);
  ok(hasText(container, "任务"), "TaskMemoryBar: labels a user prompt source as 任务");
  ok(hasText(container, "真实任务"), "TaskMemoryBar: renders a populated partial field");
  ok(!hasText(container, "当前"), "TaskMemoryBar: omits an empty current segment");
  ok(!hasText(container, "下一步"), "TaskMemoryBar: omits an empty next-step segment");
  cleanup();
}

{
  const container = render(
    <TaskMemoryBar memoryLine={BASE_MEMORY} onEditGoal={() => {}} />,
  );
  ok(hasText(container, "展开"), "TaskMemoryBar: shows 展开 button when onEditGoal provided");
  ok(container.querySelector(".task-memory-bar__edit-btn") !== null, "TaskMemoryBar: uses __edit-btn class");
  cleanup();
}

{
  // no-current class when current is empty
  const container = render(<TaskMemoryBar memoryLine={{ goal: "实现登录功能", current: "", nextStep: "", goalSource: "derived", revision: 1 }} />);
  ok(container.querySelector(".task-memory-bar--no-current") !== null, "TaskMemoryBar: has --no-current class when current is empty");
  ok(container.querySelectorAll(".task-memory-bar__segment").length === 1, "TaskMemoryBar: only one segment rendered when current is empty and nextStep is empty");
  cleanup();
}

{
  // no-current class NOT present when current is present (all three segments)
  const container = render(<TaskMemoryBar memoryLine={BASE_MEMORY} />);
  ok(container.querySelector(".task-memory-bar--no-current") === null, "TaskMemoryBar: no --no-current class when current is present");
  cleanup();
}

{
  // tooltip trigger wraps each complete segment for full-text disclosure
  const container = render(<TaskMemoryBar memoryLine={BASE_MEMORY} />);
  const triggers = container.querySelectorAll(".task-memory-bar__segment-slot.tooltip-trigger");
  ok(triggers.length === 3, "TaskMemoryBar: each of 3 complete segments is a tooltip trigger");
  cleanup();
}

{
  // tooltip and aria-label expose full segment text regardless of clipping
  const container = render(<TaskMemoryBar memoryLine={BASE_MEMORY} />);
  // Each complete segment is wrapped by the portal tooltip trigger.
  ok(container.querySelectorAll(".task-memory-bar__segment-slot.tooltip-trigger").length === 3, "TaskMemoryBar: each complete segment has tooltip-trigger wrapper");
  // aria-label on bar exposes full text for all segments as fallback disclosure
  const bar = container.querySelector(".task-memory-bar");
  const ariaLabel = bar?.getAttribute("aria-label") ?? "";
  ok(ariaLabel.includes("目标"), "TaskMemoryBar: aria-label contains goal label");
  ok(ariaLabel.includes("实现登录功能"), "TaskMemoryBar: aria-label contains goal value");
  ok(ariaLabel.includes("当前"), "TaskMemoryBar: aria-label contains current label");
  ok(ariaLabel.includes("编写 API 接口"), "TaskMemoryBar: aria-label contains current value");
  ok(ariaLabel.includes("下一步"), "TaskMemoryBar: aria-label contains nextStep label");
  ok(ariaLabel.includes("添加单元测试"), "TaskMemoryBar: aria-label contains nextStep value");
  cleanup();
}

{
  const container = render(<TaskMemoryBar memoryLine={{ goal: "完整目标文本", current: "", nextStep: "后续步骤", revision: 1 }} />);
  ok(container.querySelector(".task-memory-bar__segment-slot--goal") !== null, "TaskMemoryBar: goal exposes its flex role");
  ok(container.querySelector(".task-memory-bar__segment-slot--next") !== null, "TaskMemoryBar: next step exposes its flex role");
  cleanup();
}

{
  const container = render(<TaskMemoryBar memoryLine={null} recentlyDid="创建并验证启动脚本" />);
  ok(hasText(container, "最近"), "TaskMemoryBar: terminal fallback has a recent-summary label");
  ok(hasText(container, "创建并验证启动脚本"), "TaskMemoryBar: terminal fallback exposes factual transcript text");
  cleanup();
}

{
  const container = render(<TaskMemoryBar memoryLine={{ goal: "增加启动脚本", current: "", nextStep: "", goalSource: "user_prompt" }} recentlyDid="已创建三个跨平台脚本" />);
  ok(hasText(container, "任务"), "TaskMemoryBar: restored task memory remains visible after completion");
  ok(hasText(container, "最近"), "TaskMemoryBar: completed session adds a recent recap beside restored memory");
  ok(container.querySelectorAll(".task-memory-bar__segment").length === 2, "TaskMemoryBar: completed memory and recap share one compact bar");
  cleanup();
}

{
  const summary = recentSessionSummary([
    { kind: "user", id: "u1", text: "增加启动脚本" },
    { kind: "assistant", id: "a1", text: "## 已完成\n创建了 `start.bat`、`start.ps1` 和 `start.sh`。", reasoning: "", streaming: false },
  ]);
  ok(summary === "已完成 创建了 start.bat、start.ps1 和 start.sh。", "SessionMemoryBar: recap is cleaned from real assistant history");
}

{
  const fullText = "需要完整展示的会话总结".repeat(12);
  const summary = recentSessionSummary([
    { kind: "user", id: "u-long", text: fullText },
  ]);
  ok(summary === `处理：${fullText}`, "SessionMemoryBar: long recap keeps the complete cleaned transcript for its tooltip");
  ok(!summary?.endsWith("…"), "SessionMemoryBar: long recap is not truncated before rendering");

  const container = render(<TaskMemoryBar memoryLine={null} recentlyDid={summary} />);
  const ariaLabel = container.querySelector(".task-memory-bar")?.getAttribute("aria-label") ?? "";
  ok(ariaLabel === `最近：${summary}`, "TaskMemoryBar: long recap exposes the same full text to disclosure UI");
  cleanup();
}

{
  const container = render(<SessionMemoryBar sessionId="legacy-running" running items={[{ kind: "user", id: "u1", text: "恢复运行状态条" }]} />);
  ok(hasText(container, "恢复运行状态条"), "SessionMemoryBar: legacy running session falls back to its real user task");
  ok(hasText(container, "运行中"), "SessionMemoryBar: legacy running fallback reflects the real runtime state");
  cleanup();
}

// ── RunBlock / CompletedRunTab ──────────────────────────────────────────────

{
  const container = render(<RunBlock run={COMPLETED_RUN} />);
  ok(hasText(container, "运行完成"), "CompletedRunTab: shows completed label");
  ok(hasText(container, "3 步"), "CompletedRunTab: shows step count");
  ok(hasText(container, "4 秒"), "CompletedRunTab: shows elapsed seconds");
  const btn = container.querySelector("button.completed-run-tab");
  ok(btn !== null, "CompletedRunTab: is a native <button>");
  ok(btn?.getAttribute("aria-expanded") === "false", "CompletedRunTab: aria-expanded is false");
  ok(btn?.classList.contains("completed-run-tab--completed"), "CompletedRunTab: has --completed modifier class");
  cleanup();
}

{
  const container = render(<RunBlock run={RUNNING_RUN} />);
  ok(hasText(container, "运行中"), "ActiveRunView: shows running label");
  ok(hasText(container, "配置"), "ActiveRunView: shows first step tab");
  ok(hasText(container, "执行查询"), "ActiveRunView: shows last step tab (current step)");
  const log = container.querySelector('[role="log"]');
  ok(log !== null, "ActiveRunView: has log region");
  const region = container.querySelector('[aria-busy="true"]');
  ok(region !== null, "ActiveRunView: aria-busy is true for running run");
  ok(container.querySelector(".active-run-view--running") !== null, "ActiveRunView: has --running modifier class");
  cleanup();
}

{
  const container = render(<RunBlock run={RUNNING_RUN} onStop={() => {}} />);
  const stop = container.querySelector('button[aria-label="停止运行"]');
  ok(stop !== null, "ActiveRunView: stop action has an explicit accessible label");
  ok(stop?.querySelector(".lucide-circle-stop") !== null, "ActiveRunView: stop action uses the recognizable CircleStop icon");
  cleanup();
}

{
  const container = render(<RunBlock run={FAILED_RUN} />);
  ok(hasText(container, "运行失败"), "CompletedRunTab: shows failed label");
  ok(container.querySelector(".completed-run-tab--failed") !== null, "CompletedRunTab: has --failed modifier class");
  cleanup();
}

// ── RunBlock native button test ──────────────────────────────────────────────

{
  const container = render(<RunBlock run={COMPLETED_RUN} />);
  const btn = container.querySelector("button.completed-run-tab");
  ok(btn !== null, "CompletedRunTab: native <button> element exists");
  ok(btn?.tagName === "BUTTON", "CompletedRunTab: tag is BUTTON");
  cleanup();
}

{
  const container = render(<RunBlock run={RUNNING_RUN} />);
  const btn = container.querySelector("button.completed-run-tab");
  ok(btn === null, "ActiveRunView: not rendered as button (uses ActiveRunView div)");
  cleanup();
}

// ── RunStepTab modifier class test ──────────────────────────────────────────

{
  const container = render(<RunBlock run={COMPLETED_RUN} />);
  // expanded=false, so CompletedRunTab is shown — no step tabs visible
  ok(container.querySelector(".run-step-tab") === null, "CompletedRunTab: no step tabs rendered");
  cleanup();
}

{
  const container = render(<RunBlock run={RUNNING_RUN} />);
  const tabs = container.querySelectorAll(".run-step-tab");
  ok(tabs.length === 3, "ActiveRunView: 3 step tabs rendered");
  // First two are completed, last is running
  ok(tabs[0].classList.contains("run-step-tab--completed"), "RunStepTab: first tab is --completed");
  ok(tabs[2].classList.contains("run-step-tab--running"), "RunStepTab: last tab is --running");
  cleanup();
}

{
  const completedLastStep: RunRecord = {
    ...RUNNING_RUN,
    events: BASE_EVENTS.map((event, index) => index === BASE_EVENTS.length - 1
      ? { ...event, stepLabel: "bash 完成", status: "completed" as const }
      : event),
  };
  const container = render(<RunBlock run={completedLastStep} />);
  const last = container.querySelectorAll(".run-step-tab")[BASE_EVENTS.length - 1];
  ok(last.classList.contains("run-step-tab--completed"), "RunStepTab: completed last tool stays completed while run continues");
  ok(last.querySelector(".animate-spin") === null, "RunStepTab: completed last tool does not show a running spinner");
  cleanup();
}

// ── RunStepTab aria-selected test ──────────────────────────────────────────

{
  let selectedIndex = -1;
  const container = render(<ActiveRunView run={RUNNING_RUN} onStepSelect={(_, index) => { selectedIndex = index; }} />);
  const tabs = container.querySelectorAll<HTMLButtonElement>(".run-step-tab");
  act(() => tabs[1]?.click());
  eq(selectedIndex, 1, "RunStepTab: clicking number/label invokes step selection");
  cleanup();
}

{
  const selectedRun: RunRecord = {
    ...RUNNING_RUN,
    selectedStepIndex: 1,  // 0-based: second tab selected
  };
  const container = render(<ActiveRunView run={selectedRun} />);
  const tabs = container.querySelectorAll(".run-step-tab");
  ok(tabs.length === 3, "aria-selected: 3 step tabs rendered");
  ok(tabs[0].getAttribute("aria-selected") === "false", "aria-selected: first tab is false");
  ok(tabs[1].getAttribute("aria-selected") === "true", "aria-selected: second tab is true (selectedStepIndex=1)");
  ok(tabs[2].getAttribute("aria-selected") === "false", "aria-selected: last tab is false when not selected");
  cleanup();
}

{
  // Default (no selectedStepIndex) → auto-follow: last tab selected
  const container = render(<ActiveRunView run={RUNNING_RUN} />);
  const tabs = container.querySelectorAll(".run-step-tab");
  const selected = container.querySelectorAll('.run-step-tab[aria-selected="true"]');
  ok(selected.length === 1 && selected[0] === tabs[tabs.length - 1], "aria-selected: auto-follow selects the latest tab");
  cleanup();
}

// ── RunDetailViewport with selectedStepIndex filter ─────────────────────────

{
  const selectedRun: RunRecord = {
    ...RUNNING_RUN,
    selectedStepIndex: 1,
  };
  const container = render(<ActiveRunView run={selectedRun} />);
  // Should show only the second event ("连接数据库"), not the others
  ok(hasText(container, "连接数据库"), "RunDetailViewport filter: shows selected event content");
  ok(!hasText(container, "读取配置文件"), "RunDetailViewport filter: hides first event");
  ok(!hasText(container, "执行查询"), "RunDetailViewport filter: hides last event when filtered");
  cleanup();
}

{
  // No selectedStepIndex → auto-follow shows the latest event
  const container = render(<ActiveRunView run={RUNNING_RUN} />);
  ok(!hasText(container, "读取配置文件"), "RunDetailViewport auto-follow: hides first event");
  ok(hasText(container, "执行查询"), "RunDetailViewport auto-follow: shows latest event");
  ok(!hasText(container, "连接数据库"), "RunDetailViewport auto-follow: hides middle event");
  cleanup();
}

// ── RunDetailViewport ──────────────────────────────────────────────────────

{
  const container = render(<ActiveRunView run={RUNNING_RUN} />);
  const log = container.querySelector('[role="log"]');
  ok(log !== null, "RunDetailViewport: role=log present");
  ok(!hasText(container, "读取配置文件"), "RunDetailViewport: auto-follow hides older event content");
  ok(hasText(container, "执行查询"), "RunDetailViewport: shows last event");
  cleanup();
}

// ── ArtifactShelf ──────────────────────────────────────────────────────────

{
  const container = render(<ArtifactShelf artifacts={[]} />);
  ok(hasText(container, "产物 0"), "ArtifactShelf: shows count 0 when empty");
  ok(hasText(container, "暂无产物"), "ArtifactShelf: shows empty hint");
  cleanup();
}

{
  const container = render(<ArtifactShelf artifacts={[BASE_ARTIFACT]} />);
  ok(hasText(container, "产物 1"), "ArtifactShelf: shows count 1");
  ok(hasText(container, "app.exe"), "ArtifactShelf: shows artifact name");
  ok(container.querySelector(".artifact-shelf__all-btn") !== null, "ArtifactShelf: shows '全部' button when artifacts present");
  cleanup();
}

{
  const container = render(<ArtifactShelf artifacts={[]} />);
  ok(container.querySelector(".artifact-shelf__all-btn") === null, "ArtifactShelf: no '全部' button when empty");
  cleanup();
}

{
  const container = render(
    <ArtifactShelf
      artifacts={[BASE_ARTIFACT, {
        ...BASE_ARTIFACT,
        artifactId: "art-gen",
        name: "build.log",
        type: "log",
        status: "generating",
      }]}
      onOpen={() => {}}
    />,
  );
  ok(hasText(container, "产物 2"), "ArtifactShelf: shows count 2");
  ok(hasText(container, "build.log"), "ArtifactShelf: shows generating artifact name");
  ok(hasText(container, "生成中"), "ArtifactShelf: shows generating status label");
  cleanup();
}

// ── ArtifactItem types and status ───────────────────────────────────────────

{
  const container = render(
    <ArtifactItem
      artifact={BASE_ARTIFACT}
      onOpen={() => {}}
    />,
  );
  const item = container.querySelector('.artifact-item[aria-label*="app.exe"]');
  const openButton = container.querySelector('.artifact-item__primary');
  ok(item !== null, "ArtifactItem: has aria-label with name");
  ok(openButton?.tagName === "BUTTON", "ArtifactItem: available primary action is a native <button>");
  ok(item?.classList.contains("artifact-item--available"), "ArtifactItem: has --available modifier class");
  cleanup();
}

{
  const missingArtifact: ArtifactRecord = {
    ...BASE_ARTIFACT,
    artifactId: "art-missing",
    name: "lost.txt",
    status: "missing",
  };
  const container = render(<ArtifactItem artifact={missingArtifact} />);
  ok(hasText(container, "文件不存在"), "ArtifactItem: shows missing status");
  cleanup();
}

// ── ArtifactItem: missing regen should appear when onRegenerate supplied ────

{
  const missingArtifact: ArtifactRecord = {
    ...BASE_ARTIFACT,
    artifactId: "art-missing",
    name: "lost.txt",
    status: "missing",
  };
  let regenCalled = false;
  const container = render(
    <ArtifactItem
      artifact={missingArtifact}
      onRegenerate={() => { regenCalled = true; }}
    />,
  );
  const regenBtn = queryByAriaLabel(container, "重新生成");
  ok(regenBtn !== null, "ArtifactItem missing: shows regenerate button when onRegenerate supplied");
  act(() => regenBtn?.click());
  ok(regenCalled, "ArtifactItem missing: onRegenerate callback fires on click");
  cleanup();
}

// ── ArtifactItem: failed regen should appear when onRegenerate supplied ─────

{
  const failedArtifact: ArtifactRecord = {
    ...BASE_ARTIFACT,
    artifactId: "art-fail",
    name: "crash.log",
    status: "failed",
  };
  let regenCalled = false;
  const container = render(
    <ArtifactItem
      artifact={failedArtifact}
      onRegenerate={() => { regenCalled = true; }}
    />,
  );
  const regenBtn = queryByAriaLabel(container, "重新生成");
  ok(regenBtn !== null, "ArtifactItem failed: shows regenerate button when onRegenerate supplied");
  act(() => regenBtn?.click());
  ok(regenCalled, "ArtifactItem failed: onRegenerate callback fires on click");
  cleanup();
}

// ── ArtifactItem: stale revalidate + regen ──────────────────────────────────

{
  let revalidateCalled = false;
  let regenCalled = false;
  const container = render(
    <ArtifactItem
      artifact={STALE_ARTIFACT}
      onRevalidate={() => { revalidateCalled = true; }}
      onRegenerate={() => { regenCalled = true; }}
    />,
  );
  const revalidateBtn = queryByAriaLabel(container, "重新校验");
  ok(revalidateBtn !== null, "ArtifactItem stale: shows revalidate button when onRevalidate supplied");
  const regenBtn = queryByAriaLabel(container, "重新生成");
  ok(regenBtn !== null, "ArtifactItem stale: shows regenerate button when onRegenerate supplied");
  cleanup();
}

// ── ArtifactItem modifier classes ───────────────────────────────────────────

{
  const staleArtifact: ArtifactRecord = {
    ...BASE_ARTIFACT,
    artifactId: "art-stale-mod",
    name: "stale.md",
    status: "stale",
  };
  const container = render(
    <ArtifactItem artifact={staleArtifact} onRevalidate={() => {}} />,
  );
  const item = container.querySelector(".artifact-item--stale");
  ok(item !== null, "ArtifactItem stale: has --stale modifier class");
  ok(container.querySelector('.action-button')?.tagName === "BUTTON", "ArtifactItem stale: action is a native <button>");
  cleanup();
}

{
  const availableArtifact: ArtifactRecord = {
    ...BASE_ARTIFACT,
    artifactId: "art-avail-mod",
    name: "ready.txt",
    status: "available",
  };
  const container = render(
    <ArtifactItem artifact={availableArtifact} onOpen={() => {}} />,
  );
  const item = container.querySelector(".artifact-item--available");
  ok(item !== null, "ArtifactItem available: has --available modifier class");
  ok(item?.classList.contains("artifact-item--actionable"), "ArtifactItem available: has --actionable class");
  cleanup();
}

// ── QueueTray ───────────────────────────────────────────────────────────────

{
  const container = render(<QueueTray items={[]} />);
  ok(container.textContent === "", "QueueTray: returns null when empty");
  cleanup();
}

{
  const container = render(<QueueTray items={QUEUE_ITEMS} />);
  ok(hasText(container, "优化数据库查询"), "QueueTray: shows first item");
  ok(hasText(container, "添加错误处理"), "QueueTray: shows second item");
  ok(hasText(container, "+1 更多"), "QueueTray: shows overflow count");
  ok(!hasText(container, "编写文档"), "QueueTray: does not show hidden item text");
  cleanup();
}

{
  let removedId = "";
  let editedId = "";
  const container = render(
    <QueueTray
      items={QUEUE_ITEMS.slice(0, 1)}
      onEdit={(id) => { editedId = id; }}
      onRemove={(id) => { removedId = id; }}
    />,
  );
  const editBtn = queryByAriaLabel(container, "编辑");
  ok(editBtn !== null, "QueueTray: has 编辑 button");
  const removeBtn = queryByAriaLabel(container, "移除");
  ok(removeBtn !== null, "QueueTray: has 移除 button");
  cleanup();
}

// ── RuntimeConfigBar ────────────────────────────────────────────────────────

{
  const container = render(
    <LocaleProvider><RuntimeConfigBar config={BASE_CONFIG} connectionStatus="idle" hasQueue={false} onSwitchModel={async () => {}} onCycleCollaboration={() => {}} onSetApprovalMode={() => {}} /></LocaleProvider>,
  );
  ok(hasText(container, "DeepSeek-R1"), "RuntimeConfigBar: shows model");
  ok(hasText(container, "33%"), "RuntimeConfigBar: shows context percent");
  ok(hasText(container, "运行中"), "RuntimeConfigBar: shows runtime status");
  ok(hasText(container, "对话"), "RuntimeConfigBar: shows collaboration mode (对话 for normal)");
  ok(hasText(container, "询问"), "RuntimeConfigBar: shows approval mode (询问 for ask)");
  ok(hasText(container, "发送"), "RuntimeConfigBar: shows 发送 for idle");
  ok(container.querySelector(".runtime-config-bar__primary-action--idle") !== null, "RuntimeConfigBar: has --idle modifier on primary action");
  cleanup();
}

{
  const container = render(
    <LocaleProvider><RuntimeConfigBar config={BASE_CONFIG} connectionStatus="offline" hasQueue={false} onSwitchModel={async () => {}} onCycleCollaboration={() => {}} onSetApprovalMode={() => {}} /></LocaleProvider>,
  );
  ok(hasText(container, "保存到本地队列"), "RuntimeConfigBar: shows offline label");
  cleanup();
}

{
  let collaborationClicks = 0;
  let approvalMode = "";
  const container = render(
    <LocaleProvider><RuntimeConfigBar
      config={BASE_CONFIG}
      connectionStatus="idle"
      hasQueue={false}
      onSwitchModel={async () => {}}
      onCycleCollaboration={() => { collaborationClicks += 1; }}
      onSetApprovalMode={(mode) => { approvalMode = mode; }}
    /></LocaleProvider>,
  );
  const modelButton = container.querySelector<HTMLButtonElement>(".runtime-config-bar__model .modelsw__trigger");
  ok(modelButton !== null, "RuntimeConfigBar: model is a real picker trigger");
  act(() => queryByAriaLabel(container, "协作模式")?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  eq(collaborationClicks, 1, "RuntimeConfigBar: collaboration control invokes real callback");
  act(() => queryByAriaLabel(container, "工具批准模式")?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  eq(approvalMode, "auto", "RuntimeConfigBar: approval click advances ask to auto");
  cleanup();
}

{
  const container = render(
    <LocaleProvider><RuntimeConfigBar config={BASE_CONFIG} connectionStatus="running" hasQueue={false} onSwitchModel={async () => {}} onCycleCollaboration={() => {}} onSetApprovalMode={() => {}} /></LocaleProvider>,
  );
  ok(hasText(container, "加入队列"), "RuntimeConfigBar: shows 加入队列 for running");
  cleanup();
}

// ── derivePrimaryActionLabel ────────────────────────────────────────────────

eq(derivePrimaryActionLabel("idle", false), "发送", "derivePrimaryActionLabel: idle → 发送");
eq(derivePrimaryActionLabel("running", true), "加入队列", "derivePrimaryActionLabel: running → 加入队列");
eq(derivePrimaryActionLabel("waiting_user", false), "加入队列", "derivePrimaryActionLabel: waiting_user → 加入队列");
eq(derivePrimaryActionLabel("offline", false), "保存到本地队列", "derivePrimaryActionLabel: offline → 保存到本地队列");

// ── AddOnWorkbench ─────────────────────────────────────────────────────────

{
  const container = render(<AddOnWorkbench instances={[]} pinned={false} />);
  ok(hasText(container, "AddOn · 0 活跃"), "AddOnWorkbench: shows workbench header with 0 active");
  ok(hasText(container, "暂无活跃的 AddOn"), "AddOnWorkbench: shows empty state");
  cleanup();
}

{
  const instances: AddOnInstance[] = [
    BASE_ADDON_INSTANCE,
    { ...BASE_ADDON_INSTANCE, instanceId: "addon-2", title: "文件管理器", status: "completed" as AddOnInstanceStatus, density: "peek" as AddOnDensity, activationOrder: 2 },
  ];
  const container = render(<AddOnWorkbench instances={instances} pinned={false} onPin={() => {}} />);
  ok(hasText(container, "AddOn · 2 活跃"), "AddOnWorkbench: shows workbench header with 2 active");
  ok(hasText(container, "代码分析器"), "AddOnWorkbench: shows first instance title");
  ok(hasText(container, "文件管理器"), "AddOnWorkbench: shows second instance title");
  const header = queryByAriaLabel(container, "固定");
  ok(header !== null, "AddOnWorkbench: has 固定 button when onPin provided");
  cleanup();
}

// ── WorkbenchHeader ─────────────────────────────────────────────────────────

{
  let pinCalled = false;
  const container = render(
    <WorkbenchHeader activeCount={3} pinned={false} onPin={() => { pinCalled = true; }} />,
  );
  ok(hasText(container, "AddOn · 3 活跃"), "WorkbenchHeader: shows count");
  const pinBtn = queryByAriaLabel(container, "固定");
  ok(pinBtn !== null, "WorkbenchHeader: has 固定 button");
  act(() => pinBtn?.click());
  ok(pinCalled, "WorkbenchHeader: onPin callback fires");
  cleanup();
}

// ── InstanceHeader ──────────────────────────────────────────────────────────

{
  const instance: AddOnInstance = {
    ...BASE_ADDON_INSTANCE,
    status: "error",
    message: "连接断开",
  };
  const container = render(<InstanceHeader instance={instance} />);
  ok(hasText(container, "代码分析器"), "InstanceHeader: shows title");
  ok(hasText(container, "错误"), "InstanceHeader: shows error status text");
  ok(container.querySelector(".instance-header__status-text--error") !== null, "InstanceHeader: has --error modifier on status text");
  cleanup();
}

{
  const instance: AddOnInstance = {
    ...BASE_ADDON_INSTANCE,
    status: "needs_input",
  };
  const container = render(<InstanceHeader instance={instance} />);
  ok(hasText(container, "需要输入"), "InstanceHeader: shows needs_input status text");
  cleanup();
}

// ── AddOnInstanceView modifier classes (density + status) ───────────────────

{
  const tabInstance: AddOnInstance = { ...BASE_ADDON_INSTANCE, density: "tab" };
  const container = render(<AddOnInstanceView instance={tabInstance} />);
  const el = container.querySelector(".addon-instance");
  ok(el !== null, "AddOnInstanceView: renders tab density");
  ok(el?.classList.contains("addon-instance--tab"), "AddOnInstanceView: has --tab density modifier class");
  ok(el?.classList.contains("addon-instance--active"), "AddOnInstanceView: has --active status modifier class");
  ok(el?.getAttribute("tabindex") === "0", "AddOnInstanceView: tab density is tabbable");
  cleanup();
}

{
  const peekInstance: AddOnInstance = { ...BASE_ADDON_INSTANCE, density: "peek" };
  const container = render(<AddOnInstanceView instance={peekInstance} />);
  const el = container.querySelector(".addon-instance--peek");
  ok(el !== null, "AddOnInstanceView: peek has --peek density modifier class");
  cleanup();
}

{
  const focusInstance: AddOnInstance = { ...BASE_ADDON_INSTANCE, density: "focus" };
  const container = render(<AddOnInstanceView instance={focusInstance} />);
  const el = container.querySelector(".addon-instance--focus");
  ok(el !== null, "AddOnInstanceView: focus has --focus density modifier class");
  cleanup();
}

{
  const errorInstance: AddOnInstance = { ...BASE_ADDON_INSTANCE, status: "error", density: "focus" };
  const container = render(<AddOnInstanceView instance={errorInstance} />);
  const el = container.querySelector(".addon-instance--error");
  ok(el !== null, "AddOnInstanceView: error status has --error modifier class");
  cleanup();
}

{
  const instance: AddOnInstance = {
    ...BASE_ADDON_INSTANCE,
    density: "focus",
    message: "正在扫描代码库",
    status: "loading",
  };
  const container = render(
    <AddOnInstanceView
      instance={instance}
      renderBody={(inst) => <div data-testid="body">Body: {inst.title}</div>}
    />,
  );
  ok(hasText(container, "Body: 代码分析器"), "AddOnInstanceView: renders body slot");
  ok(hasText(container, "正在扫描代码库"), "AddOnInstanceView: shows message when present");
  ok(container.querySelector(".instance-body__message--loading") !== null, "AddOnInstanceView: has --loading modifier on message");
  cleanup();
}

// ── Summary ─────────────────────────────────────────────────────────────────

console.log(`\nResults: ${passed} passed, ${failed} failed\n`);

if (failed > 0) {
  process.exit(1);
}
