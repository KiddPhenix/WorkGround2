// Hook-level regression: a delayed history response crossing a send must not
// erase the accepted prompt. Mirrors the ordering that produces the reported
// "only thinking/running, initial prompt absent" symptom: an in-flight
// HistoryPageForTab (started by an open/sync before the user sent) resolves
// AFTER the optimistic user bubble, and an empty/stale page must keep that
// newer accepted message instead of rebuilding the transcript over it.
//
// Run: node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/prompt-visibility-hook.test.tsx

import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { useController } from "../lib/useController";
import type { AppBindings } from "../lib/bridge";
import type { BalanceInfo, CheckpointMeta, ContextInfo, EffortInfo, HistoryMessage, JobView, Meta, TabMeta } from "../lib/types";

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

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
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

function tabMeta(overrides: Partial<TabMeta> = {}): TabMeta {
  return {
    id: "tab-a",
    sessionId: "session-a",
    scope: "project",
    workspaceRoot: "/repo",
    workspaceName: "repo",
    workspacePath: "/repo",
    gitBranch: "main",
    topicId: "topic-a",
    topicTitle: "General",
    label: "model",
    ready: true,
    running: false,
    mode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    active: true,
    cwd: "/repo",
    sessionPath: "/repo/session-a.jsonl",
    ...overrides,
  };
}

function meta(): Meta {
  return {
    label: "model",
    ready: true,
    eventChannel: "agent:event",
    cwd: "/repo",
    workspaceRoot: "/repo",
    workspaceName: "repo",
    workspacePath: "/repo",
    gitBranch: "main",
    autoApproveTools: false,
    bypass: false,
    collaborationMode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    goal: "",
    goalStatus: "stopped",
    sessionPath: "/repo/session-a.jsonl",
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
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);

const context: ContextInfo = { used: 12, window: 100, sessionTokens: 12 };
const effort: EffortInfo = { supported: true, current: "auto", default: "auto", levels: ["auto"] };
const balance: BalanceInfo = { available: false, display: "" };
const jobs: JobView[] = [];
const checkpoints: CheckpointMeta[] = [];

const gatedHistory = deferred<HistoryMessage[]>();
let gateNextHistory = false;

window.runtime = {
  EventsOn: () => () => {},
  BrowserOpenURL: () => {},
};
window.go = {
  main: {
    App: {
      ListTabs: async () => [tabMeta()],
      MetaForTab: async () => meta(),
      ContextUsageForTab: async () => context,
      EffortForTab: async () => effort,
      BalanceForTab: async () => balance,
      JobsForTab: async () => jobs,
      CheckpointsForTab: async () => checkpoints,
      ArtifactsForTab: async () => [],
      HistoryForTab: async () => [],
      HistoryPageForTab: async () => {
        if (gateNextHistory) {
          const messages = await gatedHistory.promise;
          return { messages, startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, sessionPath: "/repo/session-a.jsonl" };
        }
        return { messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, sessionPath: "/repo/session-a.jsonl" };
      },
      HistoryCheckpointTurnsForTab: async () => [],
      ReplayPendingPrompts: async () => {},
      ReplayPendingPromptsForSession: async () => {},
      SubmitToTab: async () => {},
    } as Partial<AppBindings> as AppBindings,
  },
};

type Controller = ReturnType<typeof useController>;
let controller: Controller | undefined;

function Probe() {
  controller = useController();
  return null;
}

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("missing root");
const root = createRoot(rootEl);

await act(async () => {
  root.render(<Probe />);
  await flushPromises();
});
await waitFor("active tab", () => controller?.activeTabId === "tab-a");
ok(typeof controller?.send === "function", "controller exposes send");

// Start a history reload whose HistoryPageForTab stays pending, then send while
// it is in flight, then resolve the (empty) history — the accepted prompt must
// survive the replacement.
gateNextHistory = true;
const syncPromise = controller!.syncActiveTab(true);

await act(async () => {
  await flushPromises();
});
ok(controller!.state.hydrating, "history reload is pending before the send");

await act(async () => {
  controller!.send("hello");
  await flushPromises();
});
ok(controller!.state.items.some((item) => item.kind === "user" && item.text === "hello"), "optimistic prompt is visible while history is pending");

await act(async () => {
  gatedHistory.resolve([]);
  await gatedHistory.promise;
  await syncPromise;
  await flushPromises();
});
ok(controller!.state.items.some((item) => item.kind === "user" && item.text === "hello"), "empty history resolving after the send keeps the accepted prompt visible");

await act(async () => {
  root.unmount();
});
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
