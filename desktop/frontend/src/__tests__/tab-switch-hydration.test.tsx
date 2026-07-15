// Run: tsx src/__tests__/tab-switch-hydration.test.tsx

import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import type { AppBindings } from "../lib/bridge";
import { useController } from "../lib/useController";
import { useMemoryStore } from "../store/memory";
import type { BalanceInfo, CheckpointMeta, ContextInfo, EffortInfo, HistoryMessage, JobView, Meta, TabMeta, TaskMemory, WireEvent } from "../lib/types";

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
  for (let attempt = 0; attempt < 30; attempt += 1) {
    await act(async () => {
      await flushPromises();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

function tabMeta(id: string, overrides: Partial<TabMeta> = {}): TabMeta {
  const workspaceRoot = `/repo/${id}`;
  return {
    id,
    scope: "project",
    workspaceRoot,
    workspaceName: id,
    workspacePath: workspaceRoot,
    gitBranch: "main",
    topicId: `topic-${id}`,
    topicTitle: id,
    sessionPath: `${workspaceRoot}/sessions/${id}.jsonl`,
    label: `model-${id}`,
    ready: true,
    running: false,
    mode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    active: false,
    cwd: workspaceRoot,
    ...overrides,
  };
}

function metaFor(tab: TabMeta): Meta {
  return {
    label: tab.label,
    ready: tab.ready,
    startupErr: tab.startupErr,
    eventChannel: "agent:event",
    cwd: tab.cwd || tab.workspaceRoot,
    workspaceRoot: tab.workspaceRoot,
    workspaceName: tab.workspaceName,
    workspacePath: tab.workspacePath,
    gitBranch: tab.gitBranch,
    autoApproveTools: false,
    bypass: false,
    collaborationMode: tab.collaborationMode ?? "normal",
    toolApprovalMode: tab.toolApprovalMode ?? "ask",
    tokenMode: tab.tokenMode ?? "full",
    goal: "",
    goalStatus: "stopped",
    taskMemory: tab.taskMemory,
  };
}

function userMessage(content: string): HistoryMessage {
  return { role: "user", content };
}

console.log("\ntab switch hydration");

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
const tabA = tabMeta("tab-a", { active: true });
const tabBMemory: TaskMemory = {
  sessionKey: "tab-b",
  goal: "恢复运行状态条",
  current: "等待用户查看",
  nextStep: "继续任务",
  revision: 3,
  updatedAt: Date.now(),
};
const tabB = tabMeta("tab-b", { taskMemory: tabBMemory });
const tabC = tabMeta("tab-c");
const tabD = tabMeta("tab-d");
const tabE = tabMeta("tab-e");
const tabF = tabMeta("tab-f");
const tabG = tabMeta("tab-g", { ready: false });
let backendActiveId = "tab-a";
const historyB = deferred<HistoryMessage[]>();
const historyD = deferred<HistoryMessage[]>();
const historyE = deferred<HistoryMessage[]>();
const contextDGate = deferred<ContextInfo>();
const setActiveBGate = deferred<void>();
const setActiveCGate = deferred<void>();
const submitTabCGate = deferred<void>();
const historyCalls: string[] = [];
let tabEHistoryText = "history E";
let contextDCalls = 0;
let holdNextContextForD = false;
let setActiveCalls = 0;
let newSessionCalls = 0;
let failSetActiveFor = "";
let openProjectTabTargetId = "tab-d";
let forceIdleOpenProjectTabFor = "";
let holdNextHistoryForE = false;
let holdSetActiveC = false;
const runningTabs = new Set<string>();
const tabsById = new Map([tabA, tabB, tabC, tabD, tabE, tabF, tabG].map((tab) => [tab.id, tab]));
const eventHandlers: Array<(e: WireEvent) => void> = [];
const readyHandlers: Array<(tabId?: string) => void> = [];
const sessionActivatedHandlers: Array<(payload?: unknown) => void> = [];
let replayPendingCalls = 0;
const submittedPrompts: Array<{ tabId: string; text: string }> = [];
const replayPendingEvents = new Map<string, WireEvent>();

function currentTabs(): TabMeta[] {
  return [tabA, tabB, tabC, tabD, tabE, tabF, tabG].map((tab) => {
    const running = runningTabs.has(tab.id);
    return { ...tab, active: tab.id === backendActiveId, running, cancellable: running };
  });
}

window.runtime = {
  EventsOn: (name: string, cb: (...data: unknown[]) => void) => {
    if (name === "agent:event") eventHandlers.push(cb as (e: WireEvent) => void);
    if (name === "agent:ready") readyHandlers.push(cb as (tabId?: string) => void);
    if (name === "session:activated") sessionActivatedHandlers.push(cb);
    return () => {};
  },
  BrowserOpenURL: () => {},
};
window.go = {
  main: {
    App: {
      ListTabs: async () => currentTabs(),
      MetaForTab: async (tabID: string) => metaFor(tabsById.get(tabID) ?? tabA),
      ContextUsageForTab: async (tabID: string) => {
        if (tabID === "tab-d" && holdNextContextForD) {
          contextDCalls += 1;
          holdNextContextForD = false;
          return contextDGate.promise;
        }
        if (tabID === "tab-d") contextDCalls += 1;
        return context;
      },
      EffortForTab: async () => effort,
      BalanceForTab: async () => balance,
      JobsForTab: async () => jobs,
      CheckpointsForTab: async () => checkpoints,
      HistoryForTab: async (tabID: string) => {
        historyCalls.push(tabID);
        if (tabID === "tab-b") return historyB.promise;
        if (tabID === "tab-d") return historyD.promise;
        if (tabID === "tab-f") return [];
        if (tabID === "tab-e") {
          if (holdNextHistoryForE) {
            holdNextHistoryForE = false;
            return historyE.promise;
          }
          return [userMessage(tabEHistoryText)];
        }
        return [userMessage("cached A")];
      },
      HistoryPageForTab: async (tabID: string) => {
        const messages = await window.go.main.App.HistoryForTab(tabID);
        return { messages, startTurn: 0, endTurn: messages.filter((message) => message.role === "user").length, totalTurns: messages.filter((message) => message.role === "user").length, hasOlder: false };
      },
      HistoryCheckpointTurnsForTab: async () => [],
      OpenProjectTab: async () => {
        backendActiveId = openProjectTabTargetId;
        const tab = tabsById.get(openProjectTabTargetId) ?? tabD;
        const running = runningTabs.has(openProjectTabTargetId) && forceIdleOpenProjectTabFor !== openProjectTabTargetId;
        return { ...tab, active: true, running, cancellable: running };
      },
      NewSession: async () => {
        newSessionCalls += 1;
      },
      ReplayPendingPrompts: async () => {
        replayPendingCalls += 1;
        for (const event of replayPendingEvents.values()) {
          for (const handler of eventHandlers) handler(event);
        }
      },
      SetActiveTab: async (tabID: string) => {
        setActiveCalls += 1;
        if (tabID === "tab-b") await setActiveBGate.promise;
        if (tabID === "tab-c" && holdSetActiveC) await setActiveCGate.promise;
        if (tabID === failSetActiveFor) throw new Error("persist failed");
        backendActiveId = tabID;
      },
      SubmitToTab: async (tabID: string, text: string) => {
        submittedPrompts.push({ tabId: tabID, text });
        runningTabs.add(tabID);
        if (tabID === "tab-c") await submitTabCGate.promise;
      },
      SubmitDisplayToTab: async (tabID: string) => {
        runningTabs.add(tabID);
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
await waitFor("initial active tab", () => controller?.activeTabId === "tab-a" && controller.state.items.length === 1);

let switchToB: Promise<TabMeta[] | undefined> | undefined;
await act(async () => {
  switchToB = controller?.switchTab("tab-b", tabB);
  await flushPromises();
});

eq(setActiveCalls, 1, "SetActiveTab is called for the selected tab");
eq(controller?.activeTabId, "tab-b", "switchTab updates the active tab before backend activation resolves");
eq(controller?.state.meta?.label, "model-tab-b", "switchTab applies optimistic tab metadata immediately");
eq(controller?.state.items.length, 0, "uncached target tab does not keep the previous transcript visible");
eq(controller?.state.hydrating, true, "target tab shows lightweight hydration state while backend activation is pending");
eq(controller?.state.backendActivationPending, true, "target tab gates unscoped actions while backend activation is pending");
eq(useMemoryStore.getState().memoryBySession["tab-b"]?.goal, "恢复运行状态条", "switchTab restores task memory from optimistic tab metadata immediately");
ok(!historyCalls.includes("tab-b"), "HistoryForTab is not requested before SetActiveTab completes");

let newSessionWhileSwitching: Promise<void> | undefined;
await act(async () => {
  newSessionWhileSwitching = controller?.newSession();
  await flushPromises();
});
eq(newSessionCalls, 0, "newSession waits for backend activation before using the unscoped binding");

await act(async () => {
  setActiveBGate.resolve();
  await switchToB;
  await newSessionWhileSwitching;
  await flushPromises();
});
eq(newSessionCalls, 1, "newSession runs after the selected tab is active in the backend");
await waitFor("tab-b history request", () => historyCalls.includes("tab-b"));

const historyCallsBeforeReturnToA = historyCalls.length;
await act(async () => {
  await controller?.switchTab("tab-a", tabA);
  await flushPromises();
});
await waitFor("tab-a restored", () => controller?.activeTabId === "tab-a" && controller.state.items.some((item) => item.kind === "user" && item.text === "cached A"));
eq(historyCalls.length, historyCallsBeforeReturnToA, "cached idle tab skips history hydration when reselected");

await act(async () => {
  historyB.resolve([userMessage("late B")]);
  await historyB.promise;
  await flushPromises();
});

eq(controller?.activeTabId, "tab-a", "late history for another tab does not change the active tab");
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "cached A") ?? false, "late history for another tab does not overwrite the active transcript");
ok(!(controller?.state.items.some((item) => item.kind === "user" && item.text === "late B") ?? false), "late history stays scoped to its tab state");

const historyCallsBeforeFallbackSync = historyCalls.length;
backendActiveId = "tab-b";
await act(async () => {
  await controller?.syncActiveTab(false);
  await flushPromises();
});
eq(controller?.activeTabId, "tab-b", "backend fallback sync activates the backend-selected cached tab");
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "late B") ?? false, "backend fallback sync keeps the cached transcript");
eq(historyCalls.length, historyCallsBeforeFallbackSync, "backend fallback sync preserves cached history instead of reloading it");
await act(async () => {
  await controller?.switchTab("tab-a", tabA);
  await flushPromises();
});
await waitFor("tab-a restored after fallback sync", () => controller?.activeTabId === "tab-a" && controller.state.items.some((item) => item.kind === "user" && item.text === "cached A"));

failSetActiveFor = "tab-b";
const historyCallsBeforeFailedSwitch = historyCalls.length;
await act(async () => {
  await controller?.switchTab("tab-b", tabB);
  await flushPromises();
});
eq(controller?.activeTabId, "tab-a", "failed backend tab switch reverts to the previous active tab");
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "cached A") ?? false, "failed backend tab switch keeps the previous transcript visible");
eq(historyCalls.length, historyCallsBeforeFailedSwitch, "failed backend tab switch does not hydrate the rejected target");
failSetActiveFor = "";

await act(async () => {
  for (const handler of eventHandlers) handler({ kind: "phase", text: "Planner is thinking", tabId: "tab-a" });
  for (const handler of eventHandlers) handler({ kind: "message", text: "Planner kept", reasoning: "Planner notes", tabId: "tab-a" });
  await flushPromises();
});
await waitFor("cached planner transcript", () =>
  controller?.state.items.some((item) => item.kind === "assistant" && item.text === "Planner kept" && item.reasoning === "Planner notes") ?? false
);
const historyCallsBeforeReady = historyCalls.length;
await act(async () => {
  for (const handler of readyHandlers) handler();
  await flushPromises();
});
await waitFor("ready hydration settled", () => controller?.state.hydrating === false);
eq(historyCalls.length, historyCallsBeforeReady, "agent ready with cached transcript skips executor-only history hydration");
ok(controller?.state.items.some((item) => item.kind === "phase" && item.text === "Planner is thinking") ?? false, "agent ready keeps cached planner phase");
ok(controller?.state.items.some((item) => item.kind === "assistant" && item.text === "Planner kept" && item.reasoning === "Planner notes") ?? false, "agent ready keeps cached planner answer");

let tabCSendResolved = false;
await act(async () => {
  const sendPromise = controller?.sendToTab("tab-c", "streaming C");
  sendPromise?.then(() => {
    tabCSendResolved = true;
  });
  await flushPromises();
});
await act(async () => {
  for (const handler of eventHandlers) handler({ kind: "turn_started", tabId: "tab-c" });
  await flushPromises();
});
eq(tabCSendResolved, true, "sendToTab resolves after optimistic dispatch before backend submit completes");
holdSetActiveC = true;
let switchToC: Promise<TabMeta[] | undefined> | undefined;
await act(async () => {
  switchToC = controller?.switchTab("tab-c", tabC);
  await flushPromises();
});
eq(controller?.activeTabId, "tab-c", "switching to a cached running tab still updates the active tab");
eq(controller?.state.running, true, "stale idle tab metadata does not clear cached running state before backend activation");
eq((controller?.state.turnStartAt ?? 0) > 0, true, "cached running tab keeps its run-status timer before backend activation");
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "streaming C") ?? false, "cached running tab keeps its optimistic transcript");
ok(!historyCalls.includes("tab-c"), "cached running tab skips history hydration");
runningTabs.delete("tab-c");
await act(async () => {
  holdSetActiveC = false;
  setActiveCGate.resolve();
  await switchToC;
  await flushPromises();
});
eq(controller?.state.running, true, "backend idle snapshot during tab switch does not finish an active turn before turn_done");
eq((controller?.state.turnStartAt ?? 0) > 0, true, "active turn keeps its run-status timer across pre-turn_done backend idle snapshot");
await act(async () => {
  for (const handler of eventHandlers) handler({ kind: "turn_done", tabId: "tab-c" });
  submitTabCGate.resolve();
  await submitTabCGate.promise;
  await flushPromises();
});
eq(controller?.state.running, false, "turn_done still finishes the active turn after protected switch reconciliation");

holdNextContextForD = true;
runningTabs.add("tab-d");
await act(async () => {
  await controller?.openProjectTab(tabD.workspaceRoot, tabD.topicId || "");
  await flushPromises();
});
eq(controller?.activeTabId, "tab-d", "openProjectTab activates the opened tab");
eq(controller?.state.running, true, "openProjectTab applies running runtime metadata immediately");
eq((controller?.state.turnStartAt ?? 0) > 0, true, "openProjectTab restores run-status timer for running sessions");
eq(controller?.state.items.length, 0, "open topic keeps the new tab transcript empty while hydrating");
ok(controller?.state.hydratePlaceholderItems?.some((item) => item.kind === "user" && item.text === "streaming C") ?? false, "open topic stores previous transcript only as a hydration placeholder");

await act(async () => {
  historyD.resolve([userMessage("history D")]);
  await historyD.promise;
  await flushPromises();
});
eq(controller?.state.hydrating, false, "topic history clears visible hydration before ancillary phase 2 settles");
await waitFor("open topic phase 2 started", () => contextDCalls === 1);
const contextCallsBeforeReadyD = contextDCalls;
const historyCallsBeforeReadyD = historyCalls.length;
await act(async () => {
  for (const handler of readyHandlers) {
    handler("tab-b");
    handler("tab-d");
    handler();
  }
  await flushPromises();
});
eq(contextDCalls, contextCallsBeforeReadyD, "agent ready reuses in-flight open-topic hydration for the active tab");
eq(historyCalls.length, historyCallsBeforeReadyD, "background ready events do not hydrate the active tab");
await act(async () => {
  contextDGate.resolve(context);
  await contextDGate.promise;
  await flushPromises();
});
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "history D") ?? false, "topic history replaces the hydration placeholder");
eq(controller?.state.hydratePlaceholderItems?.length ?? 0, 0, "topic history clears the hydration placeholder");

const historyCallsBeforeReopenD = historyCalls.length;
await act(async () => {
  await controller?.openProjectTab(tabD.workspaceRoot, tabD.topicId || "");
  await flushPromises();
});
eq(controller?.activeTabId, "tab-d", "reopening an already hydrated topic keeps it active");
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "history D") ?? false, "reopened cached topic keeps its transcript");
eq(historyCalls.length, historyCallsBeforeReopenD, "reopening an already hydrated topic skips history hydration");

const contextCallsBeforeInactiveD = contextDCalls;
await act(async () => {
  await controller?.openProjectTab(tabD.workspaceRoot, tabD.topicId || "");
  await controller?.switchTab("tab-a", tabA);
  await flushPromises();
});
await act(async () => {
  await flushPromises();
  await flushPromises();
});
eq(contextDCalls, contextCallsBeforeInactiveD, "inactive topic skips ancillary hydration after quick tab switch");

tabsById.set("tab-d", { ...tabD, sessionPath: `${tabD.workspaceRoot}/sessions/next-tab-d.jsonl` });
const historyCallsBeforeReboundD = historyCalls.length;
await act(async () => {
  await controller?.openProjectTab(tabD.workspaceRoot, tabD.topicId || "");
  await flushPromises();
});
eq(historyCalls.length, historyCallsBeforeReboundD + 1, "rebound topic reloads history when session path changes");

openProjectTabTargetId = "tab-e";
forceIdleOpenProjectTabFor = "tab-e";
runningTabs.delete("tab-e");
await act(async () => {
  await controller?.openProjectTab(tabE.workspaceRoot, tabE.topicId || "");
});
eq(controller?.activeTabId, "tab-e", "openProjectTab activates a runtime with stale idle metadata");
eq(controller?.state.running, false, "openProjectTab can start idle before ListTabs observes the runtime");
runningTabs.add("tab-e");
await waitFor("open runtime reconciliation", () => controller?.state.running === true && (controller?.state.turnStartAt ?? 0) > 0);
eq(controller?.state.running, true, "openProjectTab reconciles stale idle metadata without waiting for agent ready");
eq((controller?.state.turnStartAt ?? 0) > 0, true, "openProjectTab runtime reconciliation restores the run-status timer");
await act(async () => {
  for (const handler of eventHandlers) handler({ kind: "turn_done", tabId: "tab-e" });
  await flushPromises();
});

openProjectTabTargetId = "tab-f";
forceIdleOpenProjectTabFor = "tab-f";
runningTabs.delete("tab-f");
const hintedTurnStartAt = Date.now() - 45_000;
const replayCallsBeforePromptHint = replayPendingCalls;
replayPendingEvents.set("tab-f", {
  kind: "ask_request",
  tabId: "tab-f",
  ask: {
    id: "ask-tab-f",
    questions: [
      {
        id: "replace-target",
        header: "替换目标",
        prompt: "你希望替换哪些内容？",
        options: [
          { label: "最小范围", description: "更新逻辑 + README" },
          { label: "全面替换", description: "所有 GitHub URL + reasonix 引用" },
        ],
      },
    ],
  },
});
await act(async () => {
  await controller?.openProjectTab(tabF.workspaceRoot, tabF.topicId || "", { running: true, status: "waiting_confirmation", turnStartedAt: hintedTurnStartAt });
});
eq(controller?.activeTabId, "tab-f", "openProjectTab activates a stale runtime with a project-tree hint");
eq(controller?.state.running, true, "project-tree runtime hint restores run-status before ListTabs observes the runtime");
eq(controller?.state.turnStartAt, hintedTurnStartAt, "project-tree runtime hint preserves the backend run-status timer");
eq(controller?.state.pendingPrompt, true, "project-tree waiting hint marks the prompt gate immediately");
eq(controller?.state.ask?.id, "ask-tab-f", "project-tree waiting hint replays the ask card immediately");
eq(replayPendingCalls > replayCallsBeforePromptHint, true, "project-tree waiting hint triggers pending prompt replay immediately");
await act(async () => {
  for (const handler of eventHandlers) handler({ kind: "turn_done", tabId: "tab-f" });
  await flushPromises();
});
replayPendingEvents.delete("tab-f");

openProjectTabTargetId = "tab-e";
forceIdleOpenProjectTabFor = "";
runningTabs.delete("tab-e");
await act(async () => {
  await controller?.openProjectTab(tabE.workspaceRoot, tabE.topicId || "");
  await flushPromises();
});
eq(controller?.activeTabId, "tab-e", "openProjectTab activates a runtime that is still rebuilding");
eq(controller?.state.running, false, "openProjectTab can start idle before the backend reattaches the runtime");
holdNextHistoryForE = true;
runningTabs.add("tab-e");
await act(async () => {
  for (const handler of readyHandlers) {
    handler("tab-e");
  }
  await flushPromises();
});
eq(controller?.state.running, true, "agent ready reconciles running metadata before blocked history returns");
historyE.resolve([userMessage("history E after ready")]);
await waitFor("ready runtime reconciliation", () => controller?.state.running === true && (controller?.state.turnStartAt ?? 0) > 0);
eq(controller?.state.running, true, "agent ready reconciles running metadata after runtime reattach");
eq((controller?.state.turnStartAt ?? 0) > 0, true, "agent ready restores the run-status timer after runtime reattach");

const cliSessionPath = `${tabE.workspaceRoot}/sessions/cli-worker-tab-e.jsonl`;
tabE.sessionPath = cliSessionPath;
tabEHistoryText = "CLI worker session E";
holdNextHistoryForE = false;
ok(sessionActivatedHandlers.length > 0, "useController subscribes to session activation events");
await act(async () => {
  for (const handler of sessionActivatedHandlers) handler({ reason: "remote-new", tabId: "tab-e", sessionPath: cliSessionPath });
  await flushPromises();
});
await waitFor("CLI session activation reload", () =>
  controller?.activeTabId === "tab-e" &&
  controller.state.meta?.sessionPath === cliSessionPath &&
  controller.state.items.some((item) => item.kind === "user" && item.text === "CLI worker session E"),
);
ok(!(controller?.state.items.some((item) => item.kind === "user" && item.text === "history E after ready") ?? false), "CLI session activation replaces the visible transcript");

await act(async () => {
  await controller?.switchTab("tab-g", tabG);
  await flushPromises();
});
eq(controller?.state.meta?.ready, false, "new session exposes its startup state before the controller is ready");
await act(async () => {
  await controller?.sendToTab("tab-g", "queued during startup");
  await flushPromises();
});
eq(submittedPrompts.some((entry) => entry.tabId === "tab-g"), false, "startup message is not submitted before ready");
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "queued during startup" && item.queued) ?? false, "startup message is visible in the transcript queue");
eq(controller?.state.running, false, "startup queue does not show a false running state");

tabG.ready = true;
await act(async () => {
  for (const handler of readyHandlers) handler("tab-g");
  await flushPromises();
});
await waitFor("startup queue drained", () => submittedPrompts.some((entry) => entry.tabId === "tab-g" && entry.text === "queued during startup"));
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "queued during startup" && !item.queued) ?? false, "ready session promotes the queued message to sending");

runningTabs.delete("tab-g");
await act(async () => {
  for (const handler of eventHandlers) handler({ kind: "turn_done", tabId: "tab-g" });
  await flushPromises();
});
tabG.ready = false;
await act(async () => {
  await controller?.switchTab("tab-g", tabG);
  await controller?.sendToTab("tab-g", "queue owned by old session");
  await flushPromises();
});
ok(controller?.state.items.some((item) => item.kind === "user" && item.text === "queue owned by old session" && item.queued) ?? false, "old session owns its startup queue before session activation");
const submittedBeforeSessionChange = submittedPrompts.length;
const nextTabGSessionPath = `${tabG.workspaceRoot}/sessions/next-tab-g.jsonl`;
tabG.sessionPath = nextTabGSessionPath;
tabG.ready = true;
await act(async () => {
  for (const handler of sessionActivatedHandlers) handler({ reason: "remote-new", tabId: "tab-g", sessionPath: nextTabGSessionPath });
  await flushPromises();
});
await waitFor("tab-g session queue isolation", () => controller?.state.meta?.sessionPath === nextTabGSessionPath && controller.state.hydrating === false);
ok(!(controller?.state.items.some((item) => item.kind === "user" && item.text === "queue owned by old session") ?? false), "new session does not display the previous session queue");
eq(submittedPrompts.length, submittedBeforeSessionChange, "new session does not drain the previous session queue");

openProjectTabTargetId = "tab-f";
await act(async () => {
  await controller?.openProjectTab(tabF.workspaceRoot, tabF.topicId || "");
  await flushPromises();
});
await waitFor("empty history resolution", () => controller?.state.historyLoading === false);
eq(controller?.state.items.length, 0, "empty session resolves without transcript items");
const tabFHistoryCallsBeforeReady = historyCalls.filter((tabID) => tabID === "tab-f").length;
await act(async () => {
  for (const handler of readyHandlers) handler("tab-f");
  await flushPromises();
});
await waitFor("empty ready reconciliation", () => controller?.state.hydrating === false);
eq(controller?.state.historyLoading, false, "same-path agent ready keeps resolved empty history visible");
eq(historyCalls.filter((tabID) => tabID === "tab-f").length, tabFHistoryCallsBeforeReady, "same-path agent ready does not reload resolved empty history");

// ── foreground runtime snapshot during tab switch ──────────────────────
// Goal: when switching from the global running tab list, the full runtime
// snapshot (foregroundActive, runtimeMode, turnStartedAt) must seed
// immediately and survive a stale idle backend snapshot.

const tabH = tabMeta("tab-h", {
  running: true,
  runtimeMode: "foreground" as const,
  foregroundActive: true,
  backgroundJobs: 0,
  cancellable: true,
  turnStartedAt: Date.now() - 10_000,
});
tabsById.set("tab-h", tabH);
runningTabs.add("tab-h");
const setActiveHGate = deferred<void>();
const originalSetActive = window.go.main.App.SetActiveTab;
const setActiveHResolve: typeof setActiveHGate.resolve = setActiveHGate.resolve;
window.go.main.App.SetActiveTab = async (tabID: string) => {
  if (tabID === "tab-h") {
    await setActiveHGate.promise;
  }
  return originalSetActive(tabID);
};

let switchToH: Promise<TabMeta[] | undefined> | undefined;
await act(async () => {
  switchToH = controller?.switchTab("tab-h", tabH);
  await flushPromises();
});
eq(controller?.activeTabId, "tab-h", "foreground runtime snapshot: active tab updated before backend activation");
eq(controller?.state.running, true, "foreground runtime snapshot: running is true before SetActiveTab resolves");
eq(controller?.state.runtimeMode, "foreground", "foreground runtime snapshot: runtimeMode is foreground before SetActiveTab resolves");
eq((controller?.state.turnStartAt ?? 0) > 0, true, "foreground runtime snapshot: turnStartedAt preserved before SetActiveTab resolves");

// Now simulate a stale idle snapshot arriving during backend activation.
// The protection should prevent clearing the live foreground turn.
runningTabs.delete("tab-h");
await act(async () => {
  setActiveHGate.resolve();
  await switchToH;
  await flushPromises();
});
eq(controller?.state.running, true, "foreground runtime snapshot: stale idle backend snapshot does not clear running after SetActiveTab");
eq((controller?.state.turnStartAt ?? 0) > 0, true, "foreground runtime snapshot: turnStartedAt survives stale idle reconciliation");

// Only turn_done from the authoritative event stream should finish the turn.
await act(async () => {
  for (const handler of eventHandlers) handler({ kind: "turn_done", tabId: "tab-h" });
  await flushPromises();
});
eq(controller?.state.running, false, "foreground runtime snapshot: turn_done finishes the turn");

// Restore original SetActiveTab
window.go.main.App.SetActiveTab = originalSetActive;

await act(async () => {
  root.unmount();
});
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
