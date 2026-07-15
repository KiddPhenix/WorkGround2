// SessionStatusIndicators visual & behaviour contract tests.
//
// Run: npx tsx src/__tests__/session-status-indicators.test.tsx
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SessionStatusIndicators } from "../components/SessionStatusIndicators";
import type { TabMeta } from "../lib/types";

// ── DOM setup (mirrors iris-integration-test.tsx) ────────────────────────────

const testDir = dirname(fileURLToPath(import.meta.url));
const styles = readFileSync(resolve(testDir, "../styles.css"), "utf8");
const appSource = readFileSync(resolve(testDir, "../App.tsx"), "utf8");

const dom = new JSDOM(
  '<!doctype html><html><head></head><body><div id="root"></div></body></html>',
  { pretendToBeVisual: true, url: "http://localhost/" },
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

const style = dom.window.document.createElement("style");
style.textContent = styles;
dom.window.document.head.appendChild(style);

const rootEl = dom.window.document.getElementById("root")!;

// ── test harness ─────────────────────────────────────────────────────────────

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

function eq<T>(got: T, want: T, label: string) {
  const isEq = got === want;
  if (isEq) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label} — got=${JSON.stringify(got)}, want=${JSON.stringify(want)}\n`);
    failed += 1;
  }
}

function hasText(el: Element | null, text: string): boolean {
  return el?.textContent?.includes(text) ?? false;
}

const STUB_T = (key: string, vars?: Record<string, string | number>): string => {
  if (vars) {
    return key.replace(/\{n\}/g, String(vars.n ?? ""));
  }
  return key;
};

let _root: Root | null = null;

function render(el: React.ReactElement): Element {
  if (!_root) _root = createRoot(rootEl);
  act(() => { _root!.render(el); });
  return rootEl;
}

// ── helpers ──────────────────────────────────────────────────────────────────

function makeTab(overrides: Partial<TabMeta> & { id: string }): TabMeta {
  return {
    id: overrides.id,
    scope: "project",
    workspaceRoot: "/tmp/test",
    workspaceName: "test",
    topicId: "topic_" + overrides.id,
    topicTitle: "Topic " + overrides.id,
    label: "Tab " + overrides.id,
    ready: true,
    running: false,
    foregroundActive: false,
    mode: "normal",
    active: false,
    cwd: "/tmp/test",
    ...overrides,
  };
}

let onSwitchCalls: string[] = [];
function resetCalls() { onSwitchCalls = []; }
function onSwitchTest(tab: TabMeta) { onSwitchCalls.push(tab.id); }

// ── tests ────────────────────────────────────────────────────────────────────

console.log("\n── Running count includes CLI sessions ──\n");

{
  const tabs: TabMeta[] = [
    makeTab({ id: "t1", runningWork: true, foregroundActive: true }),
    makeTab({ id: "t2", runningWork: false, foregroundActive: true, runtimeMode: "waiting_user", pendingPrompt: true }),
    makeTab({ id: "t3", runningWork: true, foregroundActive: true, sessionSource: "cli" }),
  ];
  render(
    React.createElement(SessionStatusIndicators, {
      tabs, activeTabId: "t1", onSwitchTab: onSwitchTest, t: STUB_T,
    })
  );
  const container = rootEl;
  const running = container.querySelector(".session-status-indicator--running");
  ok(running !== null, "running indicator renders when N>0");
  ok(running !== null && hasText(running, "2"), "running count is 2 (includes CLI, excludes waiting_user)");

  const runningCounts = container.querySelectorAll(".session-status-indicator--running .session-status-indicator__count");
  eq(runningCounts.length, 1, "exactly one running count badge");
  ok(runningCounts[0]?.textContent === "2", "running count text is 2");

  act(() => {
    running?.dispatchEvent(new dom.window.MouseEvent("mouseover", { bubbles: true }));
  });
  eq(container.querySelectorAll(".session-status-popup__row").length, 2, "hover tip lists only running sessions");
}

console.log("\n── Running indicator hidden at 0 ──\n");

{
  const tabs: TabMeta[] = [
    makeTab({ id: "t1", runningWork: false, foregroundActive: false }),
  ];
  render(
    React.createElement(SessionStatusIndicators, {
      tabs, activeTabId: "t1", onSwitchTab: onSwitchTest, t: STUB_T,
    })
  );
  const container = rootEl;
  const running = container.querySelector(".session-status-indicator--running");
  ok(running === null, "running indicator not rendered when N=0");
}

console.log("\n── Needs-attention button disabled at 0 ──\n");

{
  const tabs: TabMeta[] = [
    makeTab({ id: "t1", needsAttention: false }),
  ];
  render(
    React.createElement(SessionStatusIndicators, {
      tabs, activeTabId: "t1", onSwitchTab: onSwitchTest, t: STUB_T,
    })
  );
  const container = rootEl;
  const btn = container.querySelector(".session-status-indicator--attention") as HTMLButtonElement | null;
  ok(btn !== null, "needs-attention button always renders");
  ok(btn?.disabled === true, "button disabled when count is 0");
  ok(btn !== null && hasText(btn, "0"), "count badge shows 0");

  resetCalls();
  btn?.click();
  eq(onSwitchCalls.length, 0, "clicking disabled button does NOT switch tab");
}

console.log("\n── Needs-attention sorting & jump ──\n");

{
  const tabs: TabMeta[] = [
    makeTab({ id: "t-late", needsAttention: true, needsAttentionAt: 3000 }),
    makeTab({ id: "t-early", needsAttention: true, needsAttentionAt: 1000 }),
    makeTab({ id: "t-mid", needsAttention: true, needsAttentionAt: 2000 }),
  ];
  resetCalls();
  render(
    React.createElement(SessionStatusIndicators, {
      tabs, activeTabId: "t-late", onSwitchTab: onSwitchTest, t: STUB_T,
    })
  );
  const container = rootEl;
  const btn = container.querySelector(".session-status-indicator--attention") as HTMLButtonElement | null;
  ok(btn !== null && hasText(btn, "3"), "count badge shows 3");
  ok(btn?.disabled === false, "button enabled when N>0");

  btn?.click();
  eq(onSwitchCalls[0], "t-early", "jumps to earliest-completed (needsAttentionAt=1000)");
}

console.log("\n── CLI sessions never need attention ──\n");

{
  const tabs: TabMeta[] = [
    makeTab({ id: "t-cli", needsAttention: true, needsAttentionAt: 500, sessionSource: " CLI " }),
    makeTab({ id: "t-desktop", needsAttention: true, needsAttentionAt: 1000 }),
  ];
  resetCalls();
  render(
    React.createElement(SessionStatusIndicators, {
      tabs, activeTabId: "other", onSwitchTab: onSwitchTest, t: STUB_T,
    })
  );
  const btn = rootEl.querySelector(".session-status-indicator--attention") as HTMLButtonElement | null;
  ok(btn !== null && hasText(btn, "1"), "attention count excludes CLI session");
  btn?.click();
  eq(onSwitchCalls[0], "t-desktop", "attention jump skips CLI session");
}

console.log("\n── Active stale attention can be cleared ──\n");

{
  const tabs: TabMeta[] = [
    makeTab({ id: "t-active", needsAttention: true, needsAttentionAt: 1000 }),
  ];
  resetCalls();
  render(
    React.createElement(SessionStatusIndicators, {
      tabs, activeTabId: "t-active", onSwitchTab: onSwitchTest, t: STUB_T,
    })
  );
  const container = rootEl;
  const btn = container.querySelector(".session-status-indicator--attention") as HTMLButtonElement | null;
  btn?.click();
  eq(onSwitchCalls[0], "t-active", "active stale attention still reaches the clearing path");
}

console.log("\n── Waiting_user needs attention but not running ──\n");

{
  const tabs: TabMeta[] = [
    // waiting_user: needs attention in real time but is NOT running
    makeTab({ id: "t-waiting", needsAttention: true, needsAttentionAt: 0,
      runningWork: false, pendingPrompt: true, runtimeMode: "waiting_user", foregroundActive: true }),
    // Normal running tab
    makeTab({ id: "t-busy", runningWork: true, foregroundActive: true }),
    // CLI waiting_user: excluded from both running and attention
    makeTab({ id: "t-cli-waiting", needsAttention: true,
      runningWork: false, pendingPrompt: true, runtimeMode: "waiting_user",
      sessionSource: "cli" }),
  ];
  render(
    React.createElement(SessionStatusIndicators, {
      tabs, activeTabId: "t-busy", onSwitchTab: onSwitchTest, t: STUB_T,
    })
  );
  const container = rootEl;

  // Running: only t-busy (t-waiting is waiting_user, t-cli-waiting is CLI)
  const running = container.querySelector(".session-status-indicator--running");
  ok(running !== null, "running indicator renders");
  ok(running !== null && hasText(running, "1"), "running count=1 excludes waiting_user");

  // Needs-attention: t-waiting counts, t-cli-waiting excluded (CLI)
  const attn = container.querySelector(".session-status-indicator--attention") as HTMLButtonElement | null;
  ok(attn !== null && hasText(attn, "1"), "attention count=1 includes waiting_user, excludes CLI");

  resetCalls();
  attn?.click();
  eq(onSwitchCalls[0], "t-waiting", "attention jump targets waiting_user tab");
}

console.log("\n── Controls render before AddOn in App.tsx ──\n");

{
  const statusPos = appSource.indexOf("<SessionStatusIndicators");
  const addonPos = appSource.indexOf("<AddOnLauncherButton />");
  ok(statusPos > -1 && addonPos > -1 && statusPos < addonPos,
    "SessionStatusIndicators appears before AddOnLauncherButton in App.tsx");
  ok(appSource.includes("tabs={runtimeTabMetas}"), "global status uses visible and detached runtime tabs");
  ok(appSource.includes("handleTabChange(tab.id, tab)"), "running-list navigation forwards the fresh runtime snapshot into tab switching");
}

console.log("\n── CSS classes exist ──\n");

{
  ok(styles.includes(".session-status-indicator"), "base indicator class defined");
  ok(styles.includes("--running"), "running variant class defined");
  ok(styles.includes("--attention"), "attention variant class defined");
  ok(styles.includes("session-status-popup"), "popup class defined");
  ok(styles.includes(".session-status-indicator__dot"), "running control uses the reference status dot");
  ok(styles.includes("right: 0"), "running tip opens inward from the right edge");
  ok(styles.includes(":disabled"), "disabled state defined");
}

console.log("\n── Locale keys exist ──\n");

const enRaw = readFileSync(resolve(testDir, "../locales/en.ts"), "utf8");
const zhRaw = readFileSync(resolve(testDir, "../locales/zh.ts"), "utf8");
const zhTwRaw = readFileSync(resolve(testDir, "../locales/zh-TW.ts"), "utf8");

for (const key of ["sessionStatus.running", "sessionStatus.runningCount", "sessionStatus.needsAttention", "sessionStatus.needsAttentionCount", "sessionStatus.justNow", "sessionStatus.minutesAgo", "sessionStatus.hoursAgo", "sessionStatus.daysAgo"]) {
  ok(enRaw.includes(key), `en locale has key: ${key}`);
  ok(zhRaw.includes(key), `zh locale has key: ${key}`);
  ok(zhTwRaw.includes(key), `zh-TW locale has key: ${key}`);
}

// ── done ─────────────────────────────────────────────────────────────────────

const total = passed + failed;
console.log(`\n${total} tests · ${passed} passed · ${failed} failed\n`);
if (failed > 0) process.exit(1);
