// Run: node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/linked-session-refresh.test.tsx
//
// Regression coverage for linked (read-only) Assistant/Job Session previews:
// a Runner/Job writes its session_path file over time, and the linked preview
// must re-read an initially-empty file instead of being stuck on the empty
// transcript forever.

import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";

import { useController } from "../lib/useController";
import type { AppBindings } from "../lib/bridge";
import type { ContextInfo, EffortInfo, HistoryMessage, HistoryPage, Meta, TabMeta } from "../lib/types";

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
  ok(actual === expected, `${label}${actual === expected ? "" : `: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`}`);
}

function flushPromises(delayMs = 0): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, delayMs));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 200; attempt += 1) {
    await act(async () => {
      await flushPromises(2);
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
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);

// The preview reload poll schedules real window.setTimeout delays. Collapse
// them to 0ms so the bounded poll runs deterministically and the test stays
// fast. React's scheduler uses the global (Node) timers, which stay real.
const nodeSetTimeout = setTimeout;
const nodeClearTimeout = clearTimeout;
window.setTimeout = ((fn: (...args: unknown[]) => void) => nodeSetTimeout(fn, 0)) as unknown as typeof window.setTimeout;
window.clearTimeout = ((id: unknown) => nodeClearTimeout(id as Parameters<typeof clearTimeout>[0])) as unknown as typeof window.clearTimeout;

const sessionPath = "D:/repo/sessions/job-evaluate-resume.jsonl";
const userMessage: HistoryMessage = { role: "user", content: "评估简历" };
const assistantMessage: HistoryMessage = { role: "assistant", content: "已生成评估" };

function page(messages: HistoryMessage[]): HistoryPage {
  const turns = messages.filter((message) => message.role === "user").length;
  return { messages, startTurn: 0, endTurn: turns, totalTurns: turns, hasOlder: false, sessionPath };
}

function linkedTabMeta(overrides: Partial<TabMeta> = {}): TabMeta {
  return {
    id: "linked-tab",
    sessionId: "linked-session",
    scope: "project",
    workspaceRoot: "D:/repo",
    workspaceName: "repo",
    workspacePath: "D:/repo",
    topicId: "topic-job",
    topicTitle: "Job evaluate_resume",
    label: "model",
    ready: true,
    running: false,
    cancellable: false,
    mode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    active: true,
    cwd: "D:/repo",
    readOnly: true,
    sessionPath,
    sessionKind: "assistant",
    ...overrides,
  };
}

function meta(): Meta {
  return {
    label: "model",
    ready: true,
    eventChannel: "agent:event",
    cwd: "D:/repo",
    workspaceRoot: "D:/repo",
    workspaceName: "repo",
    workspacePath: "D:/repo",
    autoApproveTools: false,
    bypass: false,
    collaborationMode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    goal: "",
    goalStatus: "stopped",
  };
}

const context: ContextInfo = { used: 0, window: 100, sessionTokens: 0 };
const effort: EffortInfo = { supported: true, current: "auto", default: "auto", levels: ["auto"] };

// Mutable bridge state shared by every mounted controller in this test.
let fileMessages: HistoryMessage[] = [];
let historyCalls = 0;
let checkpointsCalls = 0;
let gateAt = 0;
let gate: { promise: Promise<void>; resolve: () => void } | null = null;

function installBridge() {
  fileMessages = [];
  historyCalls = 0;
  checkpointsCalls = 0;
  gateAt = 0;
  gate = null;

  window.runtime = {
    EventsOn: () => () => {},
    BrowserOpenURL: () => {},
  };
  window.go = {
    main: {
      App: {
        ListTabs: async () => [] as TabMeta[],
        MetaForTab: async () => meta(),
        ContextUsageForTab: async () => context,
        EffortForTab: async () => effort,
        BalanceForTab: async () => ({ available: false, display: "" }),
        JobsForTab: async () => [],
        CheckpointsForTab: async () => {
          checkpointsCalls += 1;
          return [];
        },
        ArtifactsForTab: async () => [],
        HistoryForTab: async () => [],
        HistoryPageForTab: async () => {
          const call = ++historyCalls;
          const snapshot = fileMessages.slice();
          if (gate && call === gateAt) await gate.promise;
          return page(snapshot);
        },
        HistoryCheckpointTurnsForTab: async () => [],
        ReplayPendingPrompts: async () => {},
        ReplayPendingPromptsForSession: async () => {},
        OpenLinkedSession: async () => linkedTabMeta(),
        ActivateLinkedSession: async () => linkedTabMeta(),
      } as Partial<AppBindings> as AppBindings,
    },
  };
}

type Controller = ReturnType<typeof useController>;

function makeDeferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

// The controller is stored in a module-level binding so re-renders (triggered
// by setActiveTabId) keep the test's reference fresh, matching the existing
// useController test harnesses.
let controller: Controller | undefined;
function Probe() {
  controller = useController();
  return null;
}

async function mountController(): Promise<ReturnType<typeof createRoot>> {
  controller = undefined;
  const host = document.getElementById("root");
  if (!host) throw new Error("missing root");
  host.innerHTML = "";
  const root = createRoot(host);
  await act(async () => {
    root.render(<Probe />);
    await flushPromises();
  });
  return root;
}

function currentController(): Controller {
  if (!controller) throw new Error("controller not mounted");
  return controller;
}

async function openLinked() {
  await act(async () => {
    await currentController().openLinkedSession("project", "D:/repo", "topic-job", sessionPath);
    await flushPromises();
    await flushPromises();
  });
}

console.log("\nlinked session refresh");

// --- Section A: first open is empty, file then appears → poll surfaces it ---
installBridge();
fileMessages = [];
{
  const root = await mountController();

  // Block the poll's first re-read (call #2) so the file can grow after the
  // initial empty read but before the poll decides the preview is still empty.
  gateAt = 2;
  gate = makeDeferred();
  await openLinked();
  await waitFor("poll re-read is in flight", () => historyCalls >= 2);
  eq(currentController().state.items.length, 0, "A: initially empty preview shows no transcript");
  ok(currentController().state.hydrating === true, "A: preview keeps a bounded loading state while re-reading");

  // The Job writes its first messages after the initial read; release the gate.
  fileMessages = [userMessage, assistantMessage];
  gate.resolve();
  gate = null;
  await waitFor("poll surfaces messages", () => currentController().state.items.length > 0);
  eq(currentController().state.items[0]?.kind, "user", "A: poll re-reads and surfaces the persisted user message");
  eq(currentController().state.items.length, 2, "A: poll surfaces the full first turn (user + assistant)");

  await act(async () => root.unmount());
}

// --- Section B: reopening the same path refreshes from disk ---
installBridge();
fileMessages = [];
{
  const root = await mountController();
  await openLinked();
  await waitFor("first open settles empty", () => currentController().state.hydrating === false);
  // Phase 2 ancillary work runs after hydrate_done; wait until the in-flight
  // load fully clears so the reopen does not coalesce with the first open.
  await waitFor("first open in-flight clears", () => checkpointsCalls >= 1);
  await act(async () => {
    for (let i = 0; i < 5; i += 1) await flushPromises();
  });
  eq(currentController().state.items.length, 0, "B: first open ends empty");
  const callsAfterFirstOpen = historyCalls;

  // The file now has content; reopening the same path must not reuse the empty cache.
  fileMessages = [userMessage];
  await openLinked();
  await waitFor("reopen surfaces messages", () => currentController().state.items.length > 0);
  ok(historyCalls > callsAfterFirstOpen, "B: reopening calls HistoryPageForTab again instead of reusing empty history");
  eq(currentController().state.items[0]?.text, "评估简历", "B: reopening hydrates the fresh disk content");

  await act(async () => root.unmount());
}

// --- Section C: the reload is bounded when the file stays empty ---
installBridge();
fileMessages = [];
{
  const root = await mountController();
  await openLinked();
  await waitFor("bounded poll settles", () => currentController().state.hydrating === false);
  const callsAfterSettle = historyCalls;

  // A bounded poll must stop once its delays are exhausted; it must never spin
  // forever polling an empty file.
  await act(async () => {
    for (let i = 0; i < 40; i += 1) await flushPromises(2);
  });
  eq(historyCalls, callsAfterSettle, "C: empty preview stops polling after its bounded retries");
  eq(currentController().state.items.length, 0, "C: empty preview stays empty instead of hanging");

  await act(async () => root.unmount());
}

// --- Section D: a superseding reset discards a stale blocked re-read ---
installBridge();
fileMessages = [];
{
  const root = await mountController();

  // Block the poll's first re-read (call #2) so we can supersede it.
  gateAt = 2;
  gate = makeDeferred();
  await openLinked();
  eq(historyCalls, 2, "D: poll re-read is in flight and blocked");

  // The file gains content, and a newer load (reset via ActivateLinkedSession)
  // supersedes the blocked stale read.
  fileMessages = [userMessage];
  await act(async () => {
    await currentController().activateLinkedSession("project", "D:/repo", "topic-job", sessionPath);
    await flushPromises();
    await flushPromises();
  });
  await waitFor("newer load surfaces content", () => currentController().state.items.length > 0);

  // Release the stale blocked read now; its stillCurrent() guard must discard it.
  gate.resolve();
  gate = null;
  await act(async () => {
    for (let i = 0; i < 20; i += 1) await flushPromises(2);
  });
  eq(currentController().state.items[0]?.text, "评估简历", "D: stale blocked re-read cannot overwrite newer content");

  await act(async () => root.unmount());
}

dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
