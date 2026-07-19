// Run: tsx src/__tests__/new-session-load-race.test.tsx

import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { initialState, reducer, useController, type Item } from "../lib/useController";
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
  for (let attempt = 0; attempt < 20; attempt += 1) {
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
  };
}

console.log("\nnew session load race");

const resetSourceItems: Item[] = [{ kind: "user", id: "old-user", text: "old prompt" }];
const resetPlaceholderItems: Item[] = [{ kind: "user", id: "placeholder-user", text: "placeholder prompt" }];
const resetState = reducer(
  {
    ...initialState,
    items: resetSourceItems,
    hydrating: true,
    hydrateReason: "open-topic",
    hydratePlaceholderItems: resetPlaceholderItems,
  },
  { type: "reset" },
);
eq(resetState.items.length, 0, "reset clears real transcript items");
eq(resetState.hydratePlaceholderItems?.length, 1, "reset preserves hydration placeholder separately");

const emptyHistoryState = reducer(resetState, { type: "history", messages: [] });
eq(emptyHistoryState.items.length, 0, "empty history keeps the real transcript empty");
eq(emptyHistoryState.hydrateHistoryLoaded, true, "empty history marks transcript hydration loaded");
eq(emptyHistoryState.hydratePlaceholderItems?.length ?? 0, 0, "empty history clears hydration placeholder items");

const hydrateDoneState = reducer(emptyHistoryState, { type: "hydrate_done" });
eq(Boolean(hydrateDoneState.hydrateHistoryLoaded), false, "hydrate_done clears the history-loaded marker");

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

const staleHistory = deferred<HistoryMessage[]>();
let newSessionCalls = 0;
let newSessionTarget = "";
const context: ContextInfo = { used: 12, window: 100, sessionTokens: 12 };
const effort: EffortInfo = { supported: true, current: "auto", default: "auto", levels: ["auto"] };
const balance: BalanceInfo = { available: false, display: "" };
const jobs: JobView[] = [];
const checkpoints: CheckpointMeta[] = [];

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
      HistoryForTab: async () => staleHistory.promise,
      HistoryPageForTab: async () => {
        const messages = await staleHistory.promise;
        return { messages, startTurn: 0, endTurn: messages.filter((message) => message.role === "user").length, totalTurns: messages.filter((message) => message.role === "user").length, hasOlder: false };
      },
      HistoryCheckpointTurnsForTab: async () => [],
      ReplayPendingPrompts: async () => {},
      NewSession: async () => {
        newSessionCalls += 1;
      },
      NewSessionForSession: async (sessionId: string) => {
        newSessionCalls += 1;
        newSessionTarget = sessionId;
      },
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
eq(controller?.activeSessionId, "session-a", "UI caches the active SessionID separately from TabID");

await act(async () => {
  await controller?.newSession();
  await flushPromises();
});
eq(newSessionCalls, 1, "NewSession is called once");
eq(newSessionTarget, "session-a", "new session uses the cached SessionID");
eq(controller?.state.items.length, 0, "new session clears the visible transcript");

await act(async () => {
  staleHistory.resolve([{ role: "user", content: "old prompt" }]);
  await staleHistory.promise;
  await flushPromises();
});

eq(controller?.state.items.length, 0, "stale history load cannot repopulate a new blank session");

await act(async () => {
  root.unmount();
});

const guardedStartupTabs = deferred<TabMeta[]>();
let ensureBlankSurfaceCalls = 0;
window.go.main.App = {
  ListTabs: async () => guardedStartupTabs.promise,
  MetaForTab: async () => meta(),
  ContextUsageForTab: async () => context,
  EffortForTab: async () => effort,
  BalanceForTab: async () => balance,
  JobsForTab: async () => jobs,
  CheckpointsForTab: async () => checkpoints,
  HistoryForTab: async () => [],
  HistoryPageForTab: async () => ({ messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false }),
  HistoryCheckpointTurnsForTab: async () => [],
  ReplayPendingPrompts: async () => {},
  EnsureBlankSurface: async () => {
    ensureBlankSurfaceCalls += 1;
    return tabMeta({ id: "tab-new", topicId: "topic-new", topicTitle: "New session" });
  },
} as Partial<AppBindings> as AppBindings;

controller = undefined;
const guardRoot = createRoot(rootEl);

await act(async () => {
  guardRoot.render(<Probe />);
  await flushPromises();
});

await act(async () => {
  await controller?.ensureBlankSurface("project", "/repo");
  await flushPromises();
});

eq(ensureBlankSurfaceCalls, 1, "EnsureBlankSurface is called once");
eq(controller?.activeTabId, "tab-new", "blank surface becomes active before startup sync resolves");

await act(async () => {
  guardedStartupTabs.resolve([tabMeta({ id: "tab-old", topicId: "topic-old", topicTitle: "Old session" })]);
  await guardedStartupTabs.promise;
  await flushPromises();
});

eq(controller?.activeTabId, "tab-new", "guarded startup sync cannot restore an older active tab");

await act(async () => {
  guardRoot.unmount();
});

// --- ready race: reconcileTabRuntime syncs Meta after backend becomes ready ---
console.log("\nready race: reconcileTabRuntime syncs Meta after backend becomes ready");

const staleMeta = deferred<Meta>();
let readyCb: ((tabId?: unknown) => void) | undefined;
let tabReady = false;

window.runtime = {
  EventsOn: (event: string, cb: (...args: unknown[]) => void) => {
    if (event === "agent:ready") readyCb = cb as (tabId?: unknown) => void;
    return () => { readyCb = undefined; };
  },
  BrowserOpenURL: () => {},
};

window.go.main.App = {
  ListTabs: async () => [tabMeta({ ready: tabReady })],
  MetaForTab: async () => staleMeta.promise,
  ContextUsageForTab: async () => context,
  EffortForTab: async () => effort,
  BalanceForTab: async () => balance,
  JobsForTab: async () => jobs,
  CheckpointsForTab: async () => checkpoints,
  ArtifactsForTab: async () => [],
  HistoryForTab: async () => [],
  HistoryPageForTab: async () => ({ messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false }),
  HistoryCheckpointTurnsForTab: async () => [],
  ReplayPendingPrompts: async () => {},
  ReplayPendingPromptsForSession: async () => {},
} as Partial<AppBindings> as AppBindings;

controller = undefined;
const raceRoot = createRoot(rootEl);

await act(async () => {
  raceRoot.render(<Probe />);
  await flushPromises();
});

await waitFor("active tab for ready race", () => controller?.activeTabId === "tab-a");
eq(controller?.state.meta?.ready, false, "initial meta.ready is false before MetaForTab resolves");

// Backend becomes ready
tabReady = true;

// Fire the ready event — triggers onReady handler:
//   reconcileTabRuntime → optimistic_meta(ready=true)
//   loadSessionDataForTab → joins in-flight (MetaForTab still pending)
//   reconcileTabRuntime → optimistic_meta(ready=true) again
ok(typeof readyCb === "function", "ready callback registered");
await act(async () => {
  readyCb?.();
  await flushPromises();
});

eq(controller?.state.meta?.ready, true, "meta.ready is true after reconcileTabRuntime even with MetaForTab pending");

// Resolve stale MetaForTab with ready=false — the in-flight hydration dispatches
// meta(ready=false), then the onReady handler's second reconcileTabRuntime fixes it.
await act(async () => {
  staleMeta.resolve({ ...meta(), ready: false });
  await staleMeta.promise;
  await flushPromises();
});

// The stale meta dispatch and Phase 2 ancillary work need multiple macrotask
// flushes to complete, then the onReady handler's final reconcileTabRuntime
// must have dispatched optimistic_meta(ready=true).
await act(async () => {
  for (let i = 0; i < 10; i += 1) await flushPromises();
});
await waitFor("ready after stale meta", () => controller?.state.meta?.ready === true);

eq(controller?.state.meta?.ready, true, "meta.ready stays true after stale MetaForTab resolves");

// Verify Meta fields that TabMeta doesn't carry are preserved.
eq(controller?.state.meta?.eventChannel, "agent:event", "eventChannel preserved from existing Meta");

await act(async () => {
  raceRoot.unmount();
});

dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
