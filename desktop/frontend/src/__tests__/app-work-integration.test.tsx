import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import type { AppBindings } from "../lib/bridge";
import type {
  BalanceInfo,
  CheckpointMeta,
  ContextInfo,
  EffortInfo,
  HistoryMessage,
  JobView,
  Meta,
  TabMeta,
} from "../lib/types";
import type { Cornerstone, WorkView, WorkflowRun } from "../work/types";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  value ? passed += 1 : failed += 1;
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

async function setControlValue(element: HTMLInputElement | HTMLTextAreaElement, value: string): Promise<void> {
  await act(async () => {
    const previous = element.value;
    const prototype = element instanceof dom.window.HTMLTextAreaElement
      ? dom.window.HTMLTextAreaElement.prototype
      : dom.window.HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, "value")?.set?.call(element, value);
    (element as typeof element & { _valueTracker?: { setValue: (next: string) => void } })._valueTracker?.setValue(previous);
    const propsKey = Object.keys(element).find((key) => key.startsWith("__reactProps$"));
    const props = propsKey
      ? (element as unknown as Record<string, { onChange?: (event: { target: typeof element }) => void }>)[propsKey]
      : undefined;
    if (props?.onChange) props.onChange({ target: element });
    else {
      element.dispatchEvent(new dom.window.InputEvent("input", { bubbles: true, inputType: "insertText", data: value }));
      element.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
    }
    await Promise.resolve();
  });
}

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
  pretendToBeVisual: true,
  url: "http://localhost/",
});
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
for (const key of ["Node", "Element", "HTMLElement", "HTMLDivElement", "HTMLButtonElement", "HTMLInputElement", "HTMLTextAreaElement", "SVGElement", "Event", "CustomEvent", "KeyboardEvent", "MouseEvent", "MutationObserver"] as const) {
  Object.defineProperty(globalThis, key, { configurable: true, value: dom.window[key] });
}
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
globalThis.getComputedStyle = dom.window.getComputedStyle.bind(dom.window);
dom.window.HTMLElement.prototype.scrollIntoView = () => {};
(dom.window.HTMLElement.prototype as typeof dom.window.HTMLElement.prototype & { attachEvent: () => void; detachEvent: () => void }).attachEvent = () => {};
(dom.window.HTMLElement.prototype as typeof dom.window.HTMLElement.prototype & { attachEvent: () => void; detachEvent: () => void }).detachEvent = () => {};
globalThis.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} } as unknown as MediaQueryList);
(globalThis as typeof globalThis & { ResizeObserver: unknown }).ResizeObserver = class {
  observe() {} unobserve() {} disconnect() {}
};
(globalThis as typeof globalThis & { IntersectionObserver: unknown }).IntersectionObserver = class {
  observe() {} unobserve() {} disconnect() {}
  root = null; rootMargin = ""; thresholds: number[] = [];
  takeRecords() { return []; }
};

const tab: TabMeta = {
  id: "tab-a",
  scope: "project",
  workspaceRoot: "D:/repo/a",
  workspaceName: "a",
  workspacePath: "D:/repo/a",
  gitBranch: "main",
  topicId: "topic-a",
  topicTitle: "真实 App",
  sessionPath: "D:/repo/a/session.jsonl",
  label: "model-a",
  ready: true,
  running: false,
  mode: "normal",
  toolApprovalMode: "ask",
  tokenMode: "full",
  active: true,
  cwd: "D:/repo/a",
};
const tabB: TabMeta = {
  ...tab,
  id: "tab-b",
  topicId: "topic-b",
  topicTitle: "已有标签",
  sessionPath: "D:/repo/a/existing.jsonl",
  active: false,
};
const topicTab: TabMeta = {
  ...tab,
  id: "tab-topic",
  topicId: "topic-linked",
  topicTitle: "主题元数据",
  sessionPath: "D:/repo/a/topic.jsonl",
  active: false,
};
const blankTab: TabMeta = {
  ...tab,
  id: "tab-blank",
  topicId: undefined,
  topicTitle: undefined,
  sessionPath: "",
  active: false,
};
let backendActiveID = tab.id;
let topicActivated = false;
let blankEnsured = false;
let blankFailures = 1;
const setActiveCalls: string[] = [];
const activateTopicCalls: string[] = [];
const resumePaths: string[] = [];
const navigationOps: string[] = [];

function currentTabs(): TabMeta[] {
  const tabs = [tab, tabB, ...(topicActivated ? [topicTab] : []), ...(blankEnsured ? [blankTab] : [])];
  return tabs.map((item) => ({ ...item, active: item.id === backendActiveID }));
}

function metaFor(item: TabMeta): Meta {
  return {
  label: item.label,
  ready: true,
  eventChannel: "agent:event",
  cwd: item.cwd,
  workspaceRoot: item.workspaceRoot,
  workspaceName: item.workspaceName,
  workspacePath: item.workspacePath,
  gitBranch: item.gitBranch,
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
const balance: BalanceInfo = { available: false, display: "" };
const jobs: JobView[] = [];
const checkpoints: CheckpointMeta[] = [];
let listCalls = 0;
let getCalls = 0;
const initialWorkCapability = deferred<boolean>();
let initialWorkCapabilitySettled = false;
const workEnabledCalls: string[] = [];
const workCapableCalls: string[] = [];
const workDataAPICalls: string[] = [];
let retryWatchCalls = 0;
let retryRecoverCalls = 0;
let retryWatchFailures = 1;
let created = false;
let emptyBeforeCreate = false;
let createFailures = 1;
let createdRun: WorkflowRun | undefined;
let pinnedCornerstone: Cornerstone | undefined;
const createRequestIDs: string[] = [];
const runCalls: Array<{ tabID: string; workID: string; requestID: string }> = [];
const pinCalls: Array<{ tabID: string; workID: string; title: string; requestID: string }> = [];
const cornerstoneMutationCalls: string[] = [];

function runWithSession(sessionPath: string): WorkflowRun {
  return {
    id: "run-1",
    workId: "work-same",
    definitionDigest: "digest",
    state: "completed",
    startedAt: "2026-07-23T10:00:00Z",
    finishedAt: "2026-07-23T10:01:00Z",
    stages: [{
      id: "stage-1",
      name: "实现",
      state: "completed",
      startedAt: "2026-07-23T10:00:00Z",
      finishedAt: "2026-07-23T10:01:00Z",
      tasks: [{
        id: "task-1",
        name: "修复",
        state: "completed",
        attempts: [{
          id: "attempt-1",
          index: 0,
          state: "completed",
          sessionRef: {
            sessionPath,
            branchId: "main",
            modelRef: "model-a",
            turnCount: 2,
            preview: "同一会话",
            startedAt: "2026-07-23T10:00:00Z",
          },
          startedAt: "2026-07-23T10:00:00Z",
          finishedAt: "2026-07-23T10:01:00Z",
        }, {
          id: "attempt-existing",
          index: 1,
          state: "completed",
          sessionRef: { sessionPath: tabB.sessionPath || "", branchId: "existing", modelRef: "model-b", turnCount: 1, preview: "已有标签", startedAt: "2026-07-23T10:02:00Z" },
          startedAt: "2026-07-23T10:02:00Z",
          finishedAt: "2026-07-23T10:03:00Z",
        }, {
          id: "attempt-topic",
          index: 2,
          state: "completed",
          sessionRef: { sessionPath: topicTab.sessionPath || "", branchId: "topic", modelRef: "model-c", turnCount: 1, preview: "主题元数据", startedAt: "2026-07-23T10:04:00Z" },
          startedAt: "2026-07-23T10:04:00Z",
          finishedAt: "2026-07-23T10:05:00Z",
        }, {
          id: "attempt-path",
          index: 3,
          state: "completed",
          sessionRef: { sessionPath: "D:/repo/a/path-only.jsonl", branchId: "path", modelRef: "model-d", turnCount: 1, preview: "只有路径", startedAt: "2026-07-23T10:06:00Z" },
          startedAt: "2026-07-23T10:06:00Z",
          finishedAt: "2026-07-23T10:07:00Z",
        }],
      }],
    }],
  };
}

function workView(
  sessionPath: string,
  workID = "work-same",
  runs: WorkflowRun[] = workID === "work-same" ? [runWithSession(sessionPath)] : createdRun ? [createdRun] : [],
  cornerstones: Cornerstone[] = pinnedCornerstone ? [pinnedCornerstone] : [],
): WorkView {
  const blueprintRef = { id: "blueprint:blank", schemaVersion: 1, version: 1 };
  return {
    schemaVersion: 1,
    revision: 1,
    assessment: { state: "ready", blocking: false, degraded: false },
    work: {
      schemaVersion: 1,
      id: workID,
      name: workID === "work-same" ? "同会话 Work" : "创建的 Work",
      state: workID === "work-same" ? "completed" : createdRun ? "running" : "ready",
      archiveState: "active",
      blueprintRef,
      definitionSnapshot: {
        schemaVersion: 1,
        revision: 1,
        blueprintRef,
        promptTemplate: "",
        workflow: { stages: [] },
        blockSpecs: [],
        digest: "digest",
      },
      blocks: [],
      placements: [],
      prompt: "验证真实 SessionSurface",
      cornerstones,
      runs,
      createdWith: { workSchemaVersion: 1, eventSchemaVersion: 1, rendererSetVersion: 1 },
      createdAt: "2026-07-23T10:00:00Z",
      updatedAt: "2026-07-23T10:01:00Z",
    },
  };
}

function retryWorkView(): WorkView {
  const view = workView("", "work-retry", [], []);
  view.revision = 2;
  view.work.name = "订阅恢复 Work";
  view.work.state = "ready";
  return view;
}

function rawMessage(value: unknown): number[] {
  return Array.from(new TextEncoder().encode(JSON.stringify(value)));
}

const methods: Partial<AppBindings> & { WorkEnabled(tabID: string): Promise<boolean> } = {
  ListTabs: async () => currentTabs(),
  ListRuntimeTabs: async () => [tab, tabB, topicTab],
  MetaForTab: async (tabID) => metaFor([tab, tabB, topicTab, blankTab].find((item) => item.id === tabID) || tab),
  ContextUsageForTab: async () => context,
  EffortForTab: async () => effort,
  BalanceForTab: async () => balance,
  JobsForTab: async () => jobs,
  CheckpointsForTab: async () => checkpoints,
  HistoryForTab: async () => [] as HistoryMessage[],
  HistoryPageForTab: async (tabID) => ({ messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, sessionPath: [tab, tabB, topicTab, blankTab].find((item) => item.id === tabID)?.sessionPath }),
  HistoryCheckpointTurnsForTab: async () => [],
  ReplayPendingPrompts: async () => {},
  ReplayPendingPromptsForSession: async () => {},
  DesktopStartupSettings: async () => ({
    bot: {
      enabled: false,
      model: "",
      toolApprovalMode: "ask",
      maxSteps: 25,
      debounceMs: 1500,
      allowlist: {
        enabled: true,
        allowAll: false,
        qqUsers: [],
        feishuUsers: [],
        weixinUsers: [],
        qqGroups: [],
        feishuGroups: [],
        weixinGroups: [],
      },
      qq: { enabled: false, appId: "", appSecretEnv: "QQ_BOT_APP_SECRET", secretSet: false, sandbox: false },
      feishu: { enabled: false, domain: "feishu", appId: "", appSecretEnv: "FEISHU_BOT_APP_SECRET", secretSet: false, verificationToken: "", mode: "webhook", webhookPort: 8080, requireMention: true },
      weixin: { enabled: false, accountId: "default", tokenEnv: "WEIXIN_BOT_TOKEN", tokenSet: false, apiBase: "https://ilinkai.weixin.qq.com" },
      connections: [],
    },
    desktopLanguage: "zh",
    desktopLayoutStyle: "workbench",
    desktopTheme: "light",
    desktopThemeStyle: "default",
    displayMode: "standard",
    composerSubmitKey: "enter",
    statusBarStyle: "text",
    statusBarItems: [],
    checkUpdates: false,
    widgetEnabled: false,
    widgetSkin: "classic",
  }),
  BotRuntimeStatus: async () => ({ running: false, status: "stopped", message: "", connections: 0, startedAt: "" }),
  GetPendingAddOnDialog: async () => null,
  NeedsOnboarding: async () => false,
  IsWidgetMode: async () => false,
  Platform: async () => "windows",
  IsMainWindowMaximised: async () => false,
  ListProjectTree: async () => [],
  WorkEnabled: async (tabID) => {
    workEnabledCalls.push(tabID);
    return true;
  },
  WorkCapable: async (tabID) => {
    workCapableCalls.push(tabID);
    if (tabID === tab.id && !initialWorkCapabilitySettled) return initialWorkCapability.promise;
    return true;
  },
  SetActiveTab: async (tabID) => {
    navigationOps.push(`existing:${tabID}`);
    setActiveCalls.push(tabID);
    backendActiveID = tabID;
  },
  ActivateTopic: async (_scope, _workspaceRoot, topicID) => {
    navigationOps.push(`topic:${topicID}`);
    activateTopicCalls.push(topicID);
    topicActivated = true;
    backendActiveID = topicTab.id;
    return { ...topicTab, active: true };
  },
  EnsureBlankSurface: async () => {
    if (blankFailures > 0) {
      blankFailures -= 1;
      navigationOps.push("blank:failed");
      throw new Error("linked blank unavailable");
    }
    navigationOps.push("blank");
    blankEnsured = true;
    backendActiveID = blankTab.id;
    return { ...blankTab, active: true };
  },
  ResumeSessionPageForTab: async (_tabID, path) => {
    navigationOps.push(`resume:${path}`);
    resumePaths.push(path);
    return { messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, sessionPath: path };
  },
  ListWorks: async () => {
    workDataAPICalls.push("ListWorks");
    listCalls += 1;
    if (emptyBeforeCreate && !created) {
      return { items: [], total: 0, nextCursor: "" };
    }
    const items = [{
      id: "work-same",
      name: "同会话 Work",
      state: "completed" as const,
      archiveState: "active" as const,
      blueprintRef: { id: "blueprint:blank", schemaVersion: 1, version: 1 },
      createdAt: "2026-07-23T10:00:00Z",
      updatedAt: "2026-07-23T10:01:00Z",
    }, {
      id: "work-retry",
      name: "订阅恢复 Work",
      state: "ready" as const,
      archiveState: "active" as const,
      blueprintRef: { id: "blueprint:blank", schemaVersion: 1, version: 1 },
      createdAt: "2026-07-23T10:00:00Z",
      updatedAt: "2026-07-23T10:01:00Z",
    }];
    if (created) items.push({
      id: "work-created",
      name: "创建的 Work",
      state: "ready",
      archiveState: "active",
      blueprintRef: { id: "blueprint:blank", schemaVersion: 1, version: 1 },
      createdAt: "2026-07-23T11:00:00Z",
      updatedAt: "2026-07-23T11:00:00Z",
    });
    return {
      items,
      total: items.length,
      nextCursor: "",
    };
  },
  CreateWork: async (_tabID, input) => {
    workDataAPICalls.push("CreateWork");
    createRequestIDs.push(input.requestId);
    if (createFailures > 0) {
      createFailures -= 1;
      throw new Error("create transport unavailable");
    }
    created = true;
    return workView(tab.sessionPath || "", "work-created").work;
  },
  GetWork: async (_tabID, workID) => {
    workDataAPICalls.push("GetWork");
    getCalls += 1;
    if (workID === "work-retry") return retryWorkView();
    return workView(tab.sessionPath || "", workID);
  },
  RecoverWorkView: async (_tabID, workID, intent) => {
    workDataAPICalls.push("RecoverWorkView");
    if (workID === "work-retry") retryRecoverCalls += 1;
    const view = workID === "work-retry"
      ? retryWorkView()
      : workView(tab.sessionPath || "", workID);
    return {
      schemaVersion: 1,
      type: "snapshot",
      workID,
      eventID: `wv-resync-${workID}-rev-${view.revision}-${intent.reason}-${intent.generation}`,
      revision: view.revision,
      baseRevision: 0,
      requestID: `${intent.reason}-recovery`,
      object: { kind: "work", id: workID },
      resync: { reason: intent.reason, authoritative: true, generation: intent.generation },
      payload: rawMessage(view),
      createdAt: "2026-07-23T10:02:00Z",
    };
  },
  WatchWork: async (_tabID, workID) => {
    workDataAPICalls.push("WatchWork");
    if (workID !== "work-retry") return;
    retryWatchCalls += 1;
    if (retryWatchFailures > 0) {
      retryWatchFailures -= 1;
      throw new Error("authoritative retry snapshot was not applied: snapshot payload must be a matching Work or WorkView at the event revision");
    }
  },
  UnwatchWork: async () => {
    workDataAPICalls.push("UnwatchWork");
  },
  RunWork: async (tabID, workID, requestID) => {
    workDataAPICalls.push("RunWork");
    runCalls.push({ tabID, workID, requestID });
    createdRun = {
      id: "run-created",
      workId: workID,
      requestId: requestID,
      definitionDigest: "digest",
      state: "running",
      stages: [],
      startedAt: "2026-07-23T11:01:00Z",
    };
    return createdRun;
  },
  PinCornerstone: async (tabID, workID, input) => {
    workDataAPICalls.push("PinCornerstone");
    cornerstoneMutationCalls.push("pin");
    pinCalls.push({ tabID, workID, title: input.title, requestID: input.requestId });
    pinnedCornerstone = {
      id: "cs-created",
      workId: workID,
      type: input.type as Cornerstone["type"],
      title: input.title,
      content: input.content,
      ref: input.ref,
      mode: input.mode as Cornerstone["mode"],
      digest: "digest-cs-created",
      required: input.required,
      status: "active",
      tags: input.tags,
      provenance: { kind: "work", workId: workID },
      pinnedAt: "2026-07-23T11:02:00Z",
      updatedAt: "2026-07-23T11:02:00Z",
    };
    const pinnedView = workView(tab.sessionPath || "", workID);
    pinnedView.revision = 2;
    pinnedView.assessment = {
      state: "blocked",
      blocking: true,
      degraded: false,
      issues: [{
        code: "required_cornerstone_failed",
        cornerstoneId: pinnedCornerstone.id,
        title: pinnedCornerstone.title,
        status: "invalid",
        required: true,
        blocking: true,
        reason: "failed: 运行失败。",
      }],
    };
    return {
      cornerstone: pinnedCornerstone,
      workView: pinnedView,
      duplicate: false,
      revision: 2,
      resolution: null,
      assessment: pinnedView.assessment,
    };
  },
};
const app = new Proxy(methods as AppBindings, {
  get(target, property, receiver) {
    const value = Reflect.get(target, property, receiver);
    return value ?? (async () => undefined);
  },
});

const workEventListeners = new Set<string>();
window.runtime = {
  EventsOn: (name) => {
    if (name.startsWith("work:view:")) workEventListeners.add(name);
    return () => { workEventListeners.delete(name); };
  },
  BrowserOpenURL: () => {},
};
window.go = { main: { App: app } };

const [{ default: App }, { LocaleProvider }, { ToastProvider }] = await Promise.all([
  import("../App"),
  import("../lib/i18n"),
  import("../lib/toast"),
]);
const rootElement = document.getElementById("root");
if (!rootElement) throw new Error("missing root");
const root = createRoot(rootElement);

process.stdout.write("\nApp Work integration smoke\n");
await act(async () => {
  root.render(
    <LocaleProvider>
      <ToastProvider>
        <App />
      </ToastProvider>
    </LocaleProvider>,
  );
});
await waitFor("pending initial Work capability", () => workCapableCalls.includes(tab.id));
ok(workEnabledCalls.includes(tab.id), "真实 <App/> 先查询 tab 配置是否显示 Work 入口");
const pendingWorkEntry = document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]');
ok(pendingWorkEntry != null, "WorkCapable pending 时 Work 按钮已在 DOM");
ok(
  pendingWorkEntry?.disabled === false
    && pendingWorkEntry.getAttribute("aria-disabled") !== "true"
    && pendingWorkEntry.getAttribute("aria-busy") === "true",
  "WorkCapable pending 时按钮 busy 且保持可点击语义",
);
const pendingSessionSurface = document.querySelector<HTMLElement>('[data-testid="session-surface"]');
ok(pendingSessionSurface != null, "WorkCapable pending 时仍显示当前 Session 页面");
await act(async () => {
  pendingWorkEntry?.click();
  await Promise.resolve();
});
const pendingWorkSurface = document.querySelector<HTMLElement>('[data-testid="work-availability"]');
ok(
  pendingWorkSurface?.dataset.workStatus === "initializing"
    && document.querySelector('[data-testid="session-surface"]') == null
    && document.querySelector('[data-testid="work-page"]') == null,
  "实际点击 pending Work 入口后立即进入初始化页并卸载 Session 中心区",
);
ok(document.activeElement?.id === "work-availability-title", "初始化页将焦点移到状态标题");
ok(document.querySelector('[data-testid="work-availability-back"]') != null, "初始化页提供返回会话入口");
ok(listCalls === 0, "实际点击 pending Work 入口不触发 ListWorks");
ok(
  workDataAPICalls.length === 0 && workEventListeners.size === 0,
  "实际点击 pending Work 入口时 Watch/Get/Create/Recover/Run/Pin、listener 等 Work 数据 API 均为零调用",
);
await act(async () => {
  document.querySelector<HTMLButtonElement>('[data-testid="work-availability-back"]')?.click();
});
ok(
  document.querySelector('[data-testid="work-availability"]') == null
    && document.querySelector('[data-testid="session-surface"]') != null,
  "初始化页返回后第一帧卸载 Work surface 并恢复 Session",
);
await act(async () => {
  pendingWorkEntry?.click();
  await Promise.resolve();
});
ok(document.querySelector('[data-work-status="initializing"]') != null, "pending Work surface 可重复安全打开");
initialWorkCapabilitySettled = true;
initialWorkCapability.resolve(true);
await waitFor("automatic Work page", () => document.querySelector('[data-testid="work-page"]') != null);
const workEntry = document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]');
ok(workEntry != null, "真实 <App/> 渲染 Work 入口");
ok(workEntry === pendingWorkEntry && workEntry?.disabled === false && workEntry.getAttribute("aria-busy") !== "true", "WorkCapable 成功后同一按钮原位变为可用");
ok(document.querySelector('[data-testid="work-page"]') != null, "capability 成功后已打开 surface 自动切换真实 WorkPage");
ok(listCalls === 1, "自动进入 WorkPage 恰好一次 ListWorks");

const workItem = document.querySelector<HTMLButtonElement>('[data-testid="work-item-work-same"] button');
await act(async () => { workItem?.click(); });
await waitFor("Work card", () => document.querySelector('[data-testid="work-card"]') != null);
ok(getCalls > 0, "真实 App 边界调用 GetWork");
const attempt = document.querySelector<HTMLElement>('[data-work-target-id="attempt:run-1:stage-1:task-1:attempt-1"]');
await act(async () => { attempt?.click(); });
const flip = document.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]');
await waitFor("enabled flip", () => flip?.disabled === false);
await act(async () => { flip?.click(); });
await waitFor("real Session surface", () => document.querySelector('[data-testid="work-back-real-session"]') != null);
const surface = document.querySelector<HTMLElement>('[data-testid="session-surface"][data-session-surface-variant="work"]');
ok(surface != null, "同路径 attempt 解析为共享 SessionSurface");
ok(surface?.querySelector(".transcript") != null, "Work 背面复用真实 Transcript");
ok(surface?.querySelector('[data-testid="session-run-slot"]') != null, "Work 背面装配真实 Run 树槽");
ok(surface?.querySelector('[data-testid="session-decision-slot"]') != null, "Work 背面装配 Approval/Ask 决策槽");
ok(surface?.querySelector('[data-testid="session-artifact-slot"]') != null, "Work 背面装配真实 ArtifactShelf 槽");
ok(surface?.querySelector('[data-testid="session-queue-slot"]') != null, "Work 背面装配真实 Queue 槽");
ok(surface?.querySelector(".composer") != null, "Work 背面复用真实 Composer");
ok(surface?.querySelector('[data-testid="session-config-slot"]') != null, "Work 背面装配真实 SessionConfigBar 槽");

async function reopenWork(): Promise<void> {
  await waitFor("Work entry after navigation", () => document.querySelector('[data-testid="work-sidebar-btn"]') != null);
  await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]')?.click(); });
  await waitFor("Work list after navigation", () => document.querySelector('[data-testid="work-page"]') != null);
  await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-item-work-same"] button')?.click(); });
  await waitFor("Work card after navigation", () => document.querySelector('[data-testid="work-card"]') != null);
}

async function openLinkedAttempt(attemptID: string): Promise<HTMLButtonElement> {
  const target = `[data-work-target-id="attempt:run-1:stage-1:task-1:${attemptID}"]`;
  await act(async () => { document.querySelector<HTMLElement>(target)?.click(); });
  const nextFlip = document.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]');
  await waitFor(`enabled flip for ${attemptID}`, () => nextFlip?.disabled === false);
  await act(async () => { nextFlip?.click(); });
  await waitFor(`linked card for ${attemptID}`, () => document.querySelector('[data-testid="linked-session-card"]') != null);
  const navigate = document.querySelector<HTMLButtonElement>('[data-testid="linked-session-navigate"]');
  if (!navigate) throw new Error(`missing navigate button for ${attemptID}`);
  return navigate;
}

await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-view-back"]')?.click(); });
await reopenWork();
let navigate = await openLinkedAttempt("attempt-existing");
await act(async () => { navigate.click(); });
await waitFor("existing tab navigation", () => setActiveCalls.includes(tabB.id));
ok(navigationOps[0] === `existing:${tabB.id}`, "linked 第一级复用已有 tab");

await reopenWork();
navigate = await openLinkedAttempt("attempt-topic");
await act(async () => { navigate.click(); });
await waitFor("topic navigation", () => activateTopicCalls.includes("topic-linked"));
ok(navigationOps.includes("topic:topic-linked"), "linked 第二级复用 topic 元数据");

await reopenWork();
navigate = await openLinkedAttempt("attempt-path");
await act(async () => { navigate.click(); });
await waitFor("linked navigation failure", () => document.querySelector('[data-testid="linked-session-error"]') != null);
ok(document.querySelector('[data-testid="work-card"]') != null, "linked 导航失败保留原 WorkCard 上下文");
ok(document.querySelector('[data-testid="linked-session-error"]')?.textContent?.includes("linked blank unavailable") === true, "linked 导航失败显式显示错误");
const retryLinked = document.querySelector<HTMLButtonElement>('[data-testid="linked-session-retry"]');
await act(async () => { retryLinked?.click(); });
await waitFor("path-only resume", () => resumePaths.includes("D:/repo/a/path-only.jsonl"));
const blankIndex = navigationOps.indexOf("blank");
const resumeIndex = navigationOps.indexOf("resume:D:/repo/a/path-only.jsonl");
ok(blankIndex >= 0 && resumeIndex > blankIndex, "linked 第三级执行 blank + resume 链");
ok(document.querySelector('[data-testid="linked-session-error"]') == null, "linked 同一卡片重试成功后清除错误");

emptyBeforeCreate = true;
await waitFor("Work entry for create", () => document.querySelector('[data-testid="work-sidebar-btn"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]')?.click(); });
await waitFor("Work page for create", () => document.querySelector('[data-testid="work-page"]') != null);
await waitFor("empty Work CTA", () => document.querySelector('[data-testid="work-empty-new-btn"]') != null);
ok(document.querySelector('[data-testid="work-empty-new-btn"]') != null, "真实 <App/> 空 ListWorks 显示新建工作 CTA");
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-empty-new-btn"]')?.click(); });
const createPrompt = document.querySelector<HTMLTextAreaElement>('[data-testid="work-create-prompt"]');
if (!createPrompt) throw new Error("missing create prompt input");
await setControlValue(createPrompt, "创建的 Work");
const submitCreate = () => document.querySelector<HTMLButtonElement>('[data-testid="work-create-submit"]');
await act(async () => { submitCreate()?.click(); });
await waitFor("visible create failure", () => document.querySelector('[data-testid="work-create-error"]') != null);
ok(createRequestIDs.length === 1, "CreateWork 首次失败显式可观察");
await act(async () => { submitCreate()?.click(); });
await waitFor("created Work card", () => document.querySelector('[data-testid="work-card"][data-work-id="work-created"]') != null);
ok(createRequestIDs.length === 2 && createRequestIDs[0] === createRequestIDs[1], "CreateWork 同名重试复用稳定 requestID");
ok(document.querySelector('[data-testid="work-card"][data-work-id="work-created"]') != null, "Create 成功后经 List 自动 Open 真实 WorkCard");

const runButton = [...document.querySelectorAll<HTMLButtonElement>('[data-testid="work-run-entry"] button')]
  .find((button) => button.textContent?.trim() === "运行");
if (!runButton) throw new Error("missing RunWork button");
await act(async () => { runButton.click(); });
await waitFor("RunWork call", () => runCalls.length === 1);
ok(runCalls[0].workID === "work-created" && runCalls[0].requestID.startsWith("work-run-"), "真实 WorkCard 调用生产 RunWork");

const drawerToggle = document.querySelector<HTMLButtonElement>('[data-testid="cornerstone-drawer"] > button');
await act(async () => { drawerToggle?.click(); });
await waitFor("Cornerstone Drawer body", () => document.querySelector('[data-testid="cornerstone-drawer-body"]') != null);
const titleInput = document.querySelector<HTMLInputElement>('[aria-label="基石标题"]');
const contentInput = document.querySelector<HTMLTextAreaElement>('[aria-label="基石内容"]');
if (!titleInput || !contentInput) throw new Error("missing Cornerstone inputs");
await setControlValue(titleInput, "生产基石");
await setControlValue(contentInput, "稳定内容");
const pinButton = [...document.querySelectorAll<HTMLButtonElement>('[data-testid="cornerstone-pin-form"] button')]
  .find((button) => button.textContent?.trim() === "Pin");
await act(async () => { pinButton?.click(); });
await waitFor("PinCornerstone call", () => pinCalls.length === 1);
ok(pinCalls[0].workID === "work-created" && pinCalls[0].title === "生产基石", "Cornerstone Drawer 调用生产 PinCornerstone");
await waitFor("pinned Cornerstone projection", () => document.querySelector('[data-testid="cornerstone-item-cs-created"]') != null);
ok(document.querySelector('[data-testid="cornerstone-item-cs-created"]') != null, "Pin ACK 回写权威 WorkView");
await waitFor("Cornerstone authority failure", () => document.querySelector('[data-testid="cornerstone-attention-summary"]') != null);
const mutationCountBeforeClose = cornerstoneMutationCalls.length;
const drawerBack = document.querySelector<HTMLButtonElement>('[data-testid="cornerstone-drawer-back"]');
ok(drawerBack != null, "authority failure 下 Drawer 仍有语义返回按钮");
await act(async () => { drawerBack?.click(); });
await waitFor("Cornerstone Drawer closed by back", () => document.querySelector('[data-testid="cornerstone-drawer-body"]') == null);
ok(document.querySelector('[data-testid="work-card"][data-work-id="work-created"]') != null, "点击返回关闭 Drawer 并恢复原 WorkCard");
ok(document.activeElement === drawerToggle, "点击返回后焦点恢复到 Drawer 打开按钮");
ok(cornerstoneMutationCalls.length === mutationCountBeforeClose, "返回只关闭视图且不发送 Wails mutation");
await act(async () => { drawerToggle?.click(); });
await waitFor("Cornerstone Drawer reopened", () => document.querySelector('[data-testid="cornerstone-drawer-body"]') != null);
await act(async () => {
  document.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
});
await waitFor("Cornerstone Drawer closed by Escape", () => document.querySelector('[data-testid="cornerstone-drawer-body"]') == null);
ok(document.activeElement === drawerToggle, "Esc 关闭 Drawer 后焦点恢复到打开按钮");
ok(cornerstoneMutationCalls.length === mutationCountBeforeClose, "Esc 关闭不发送 Wails mutation");

await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-view-back"]')?.click(); });
await waitFor("retry Work list", () => document.querySelector('[data-testid="work-item-work-retry"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-item-work-retry"] button')?.click(); });
await waitFor("retry Work unavailable", () => document.querySelector('[data-testid="work-card-unknown"]') != null);
ok(document.body.textContent?.includes("Work 不可用") === true, "真实 <App/> 复现 Work 不可用");
ok(document.body.textContent?.includes("事件订阅错误") === true, "真实 <App/> 显示订阅错误");
const retrySubscriptionButton = [...document.querySelectorAll<HTMLButtonElement>('[data-testid="work-card-unknown"] button')]
  .find((button) => button.textContent?.trim() === "重试订阅");
if (!retrySubscriptionButton) throw new Error("missing retry subscription button");
await act(async () => { retrySubscriptionButton.click(); });
await waitFor("retry Work recovered", () => document.querySelector('[data-testid="work-card"][data-work-id="work-retry"]') != null);
ok(retryWatchCalls === 2, "真实 <App/> 重试只安装一份新 Watch");
ok(retryRecoverCalls === 1, "真实 <App/> 只请求一次 authoritative RecoverWorkView");
ok(workEventListeners.size === 1, "真实 <App/> 恢复后只有一份 Work listener");
ok(document.body.textContent?.includes("Work 不可用") === false, "真实 <App/> 恢复后渲染 WorkCard");
ok(document.body.textContent?.includes("事件订阅错误") === false, "真实 <App/> 恢复后清除订阅错误");
ok(document.body.textContent?.includes("authoritative retry snapshot was not applied") === false, "真实 <App/> 恢复后清除快照错误");

await act(async () => { root.unmount(); });
ok(workEventListeners.size === 0, "真实 <App/> 卸载后无 Work listener 泄漏");

// Same-App tab race harness: every ACK is released only after the owner tab
// has changed through the real session:activated runtime path.
const [{ useWorkStore, useWorkUIStore }, { useCornerstoneUIStore }] = await Promise.all([
  import("../work/store"),
  import("../work/cornerstoneStore"),
]);
useWorkStore.getState().clearAll();
useWorkUIStore.getState().clearAll();
useCornerstoneUIStore.getState().clearAll();
localStorage.clear();
document.body.innerHTML = '<div id="race-root"></div>';

const raceTabs = ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"].map((suffix, index): TabMeta => ({
  ...tab,
  id: `race-${suffix}`,
  topicId: `race-topic-${suffix}`,
  topicTitle: `Race ${suffix.toUpperCase()}`,
  sessionPath: `D:/repo/race/${suffix}.jsonl`,
  active: index === 0,
}));
let raceActiveID = raceTabs[0].id;
const capableA = deferred<boolean>();
const enabledA = deferred<boolean>();
const capableC = deferred<boolean>();
let capableCSettled = false;
let enabledHAttempts = 0;
let capableIAttempts = 0;
let enabledJAttempts = 0;
let capableKAttempts = 0;
const listC = deferred<Awaited<ReturnType<AppBindings["ListWorks"]>>>();
const createD = deferred<Awaited<ReturnType<AppBindings["CreateWork"]>>>();
const getE = deferred<WorkView>();
const raceCapableCalls: string[] = [];
const raceEnabledCalls: string[] = [];
const raceListCalls: string[] = [];
const raceCreateCalls: string[] = [];
const raceGetCalls: string[] = [];
const raceSessionHandlers: Array<(payload?: unknown) => void> = [];
const raceSummary = {
  id: "work-race",
  name: "Race Work",
  state: "ready" as const,
  archiveState: "active" as const,
  blueprintRef: { id: "blueprint:blank", schemaVersion: 1, version: 1 },
  createdAt: "2026-07-23T12:00:00Z",
  updatedAt: "2026-07-23T12:00:00Z",
};
const readyPage = { items: [raceSummary], total: 1, nextCursor: "" };

function currentRaceTabs(): TabMeta[] {
  return raceTabs.map((item) => ({ ...item, active: item.id === raceActiveID }));
}

const raceMethods: Partial<AppBindings> & { WorkEnabled(tabID: string): Promise<boolean> } = {
  ...methods,
  ListTabs: async () => currentRaceTabs(),
  ListRuntimeTabs: async () => currentRaceTabs(),
  MetaForTab: async (tabID) => metaFor(raceTabs.find((item) => item.id === tabID) || raceTabs[0]),
  HistoryPageForTab: async (tabID) => ({ messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, sessionPath: raceTabs.find((item) => item.id === tabID)?.sessionPath }),
  WorkEnabled: async (tabID) => {
    raceEnabledCalls.push(tabID);
    if (tabID === "race-a") return enabledA.promise;
    if (tabID === "race-h" && ++enabledHAttempts === 1) throw new Error("config unavailable");
    if (tabID === "race-j") {
      if (++enabledJAttempts === 1) throw new Error("config unavailable");
      return false;
    }
    return tabID !== "race-b" && tabID !== "race-g";
  },
  WorkCapable: async (tabID) => {
    raceCapableCalls.push(tabID);
    if (tabID === "race-a") return capableA.promise;
    if (tabID === "race-b") return true;
    if (tabID === "race-c" && !capableCSettled) return capableC.promise;
    if (tabID === "race-g") return false;
    if (tabID === "race-i") return ++capableIAttempts > 1;
    if (tabID === "race-k" && ++capableKAttempts === 1) throw new Error("capability unavailable");
    return true;
  },
  ListWorks: async (tabID) => {
    raceListCalls.push(tabID);
    if (tabID === "race-c") return listC.promise;
    return readyPage;
  },
  CreateWork: async (tabID) => {
    raceCreateCalls.push(tabID);
    if (tabID === "race-d") return createD.promise;
    return workView("D:/repo/race/late-created.jsonl", "work-late-created").work;
  },
  GetWork: async (tabID, workID) => {
    raceGetCalls.push(tabID);
    if (tabID === "race-e") return getE.promise;
    return workView(raceTabs.find((item) => item.id === tabID)?.sessionPath || "", workID, [], []);
  },
  WatchWork: async () => {},
  UnwatchWork: async () => {},
};
const raceApp = new Proxy(raceMethods as AppBindings, {
  get(target, property, receiver) {
    return Reflect.get(target, property, receiver) ?? (async () => undefined);
  },
});
window.go = { main: { App: raceApp } };
window.runtime = {
  EventsOn: (name, callback) => {
    if (name === "session:activated") raceSessionHandlers.push(callback);
    return () => {};
  },
  BrowserOpenURL: () => {},
};

const raceRootElement = document.getElementById("race-root");
if (!raceRootElement) throw new Error("missing race root");
const raceRoot = createRoot(raceRootElement);
await act(async () => {
  raceRoot.render(
    <LocaleProvider>
      <ToastProvider><App /></ToastProvider>
    </LocaleProvider>,
  );
});
await waitFor("pending race-a config or capability", () => raceEnabledCalls.includes("race-a") || raceCapableCalls.includes("race-a"));

async function activateRaceTab(tabID: string): Promise<void> {
  raceActiveID = tabID;
  await act(async () => {
    for (const handler of raceSessionHandlers) handler({ reason: "test", tabId: tabID });
    await Promise.resolve();
  });
  await waitFor(`${tabID} Work startup query`, () => raceEnabledCalls.includes(tabID) || raceCapableCalls.includes(tabID));
  await act(async () => { await Promise.resolve(); });
}

await activateRaceTab("race-b");
ok(document.querySelector('[data-testid="work-sidebar-btn"]') == null, "显式 work.enabled=false 从稳定 render 起无 Work 入口");
ok(!raceCapableCalls.includes("race-b"), "显式 work.enabled=false 不探测 controller capability");
enabledA.resolve(true);
await act(async () => { await Promise.resolve(); });
ok(!raceCapableCalls.includes("race-a"), "late WorkEnabled(A) 不触发旧 tab capability 查询");
capableA.resolve(true);
await act(async () => { await Promise.resolve(); });
ok(document.querySelector('[data-testid="work-sidebar-btn"]') == null, "late WorkEnabled/WorkCapable(A) 不开启 B 的 Work 入口");
ok(!raceListCalls.includes("race-b"), "flag off 的 B 零 ListWorks 调用");

await activateRaceTab("race-c");
await waitFor("race-c pending capability", () => raceCapableCalls.includes("race-c"));
const pendingRaceCEntry = document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]');
ok(pendingRaceCEntry?.disabled === false && pendingRaceCEntry.getAttribute("aria-busy") === "true", "tab C capability pending 时入口稳定可点击");
await act(async () => { pendingRaceCEntry?.click(); });
ok(document.querySelector('[data-work-status="initializing"]') != null, "tab C pending 点击后进入所属 tab 初始化页");
await activateRaceTab("race-g");
ok(document.querySelector('[data-testid="work-availability"]') == null, "切 tab 第一帧卸载旧 tab pending Work surface");
capableCSettled = true;
capableC.resolve(true);
await act(async () => { await Promise.resolve(); });
ok(document.querySelector('[data-testid="work-sidebar-btn"]') == null, "late WorkCapable(C) 不污染显式关闭的 tab G");
ok(!raceListCalls.includes("race-c") && !raceListCalls.includes("race-g"), "pending/flag-off/late ACK 路径零 ListWorks");

await activateRaceTab("race-h");
await waitFor("race-h observable config failure", () => document.querySelector('[data-work-state="unavailable"]') != null);
const failedConfigEntry = document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]');
ok(failedConfigEntry?.disabled === false, "WorkEnabled 加载失败保留可点击入口");
await act(async () => { failedConfigEntry?.click(); });
ok(document.querySelector('[data-work-status="unavailable"]') != null, "WorkEnabled 加载失败点击后进入暂不可用页");
ok(document.activeElement?.id === "work-availability-title", "WorkEnabled 错误页将焦点移到错误标题");
ok(!raceListCalls.includes("race-h"), "WorkEnabled 错误页不调用 ListWorks");
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-availability-retry"]')?.click(); });
await waitFor("race-h config retry Work page", () => document.querySelector('[data-testid="work-page"]') != null);
ok(
  document.querySelector('[data-testid="work-sidebar-btn"]') === failedConfigEntry
    && enabledHAttempts === 2
    && raceCapableCalls.includes("race-h"),
  "WorkEnabled 错误页重试成功后继续 capability 并原地进入 WorkPage",
);
ok(raceListCalls.filter((tabID) => tabID === "race-h").length === 1, "WorkEnabled 重试成功后恰好一次 ListWorks");

await activateRaceTab("race-i");
await waitFor("race-i observable capability failure", () => document.querySelector('[data-work-state="unavailable"]') != null);
const failedCapabilityEntry = document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]');
ok(failedCapabilityEntry?.disabled === false, "WorkCapable false 保留可点击入口");
await act(async () => { failedCapabilityEntry?.click(); });
ok(document.querySelector('[data-work-status="unavailable"]') != null, "WorkCapable false 点击后进入可重试错误页");
ok(!raceListCalls.includes("race-i"), "WorkCapable false 错误页不调用 ListWorks");
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-availability-retry"]')?.click(); });
await waitFor("race-i capability retry Work page", () => document.querySelector('[data-testid="work-page"]') != null);
ok(document.querySelector('[data-testid="work-sidebar-btn"]') === failedCapabilityEntry && capableIAttempts === 2, "WorkCapable false 重试成功后原地进入 WorkPage");
ok(raceListCalls.filter((tabID) => tabID === "race-i").length === 1, "WorkCapable false 重试成功后恰好一次 ListWorks");

await activateRaceTab("race-j");
await waitFor("race-j observable config failure", () => document.querySelector('[data-work-state="unavailable"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]')?.click(); });
ok(document.querySelector('[data-work-status="unavailable"]') != null, "config error 可进入错误页");
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-availability-retry"]')?.click(); });
await waitFor("race-j disabled after config retry", () => document.querySelector('[data-testid="work-sidebar-btn"]') == null);
ok(
  document.querySelector('[data-testid="work-availability"]') == null
    && document.querySelector('[data-testid="session-surface"]') != null,
  "config 重试返回 false 后立即返回 Session、隐藏入口且无幽灵 Work surface",
);
ok(!raceCapableCalls.includes("race-j") && !raceListCalls.includes("race-j"), "config 重试 false 保持零 WorkCapable/Work API");

await activateRaceTab("race-k");
await waitFor("race-k observable capability error", () => document.querySelector('[data-work-state="unavailable"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]')?.click(); });
ok(document.querySelector('[data-work-status="unavailable"]') != null, "WorkCapable reject 点击后进入可重试错误页");
ok(!raceListCalls.includes("race-k"), "WorkCapable reject 错误页不调用 ListWorks");
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-availability-retry"]')?.click(); });
await waitFor("race-k capability retry Work page", () => document.querySelector('[data-testid="work-page"]') != null);
ok(capableKAttempts === 2, "WorkCapable reject 可安全重试");
ok(raceListCalls.filter((tabID) => tabID === "race-k").length === 1, "WorkCapable reject 重试成功后恰好一次 ListWorks");

await activateRaceTab("race-c");
await waitFor("race-c Work entry", () => document.querySelector('[data-testid="work-sidebar-btn"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]')?.click(); });
await waitFor("race-c pending ListWorks", () => raceListCalls.includes("race-c"));
await activateRaceTab("race-d");
listC.resolve(readyPage);
await act(async () => { await Promise.resolve(); });
ok(document.querySelector('[data-testid="work-page"]') == null && document.querySelector('[data-testid="work-item-work-race"]') == null, "late ListWorks(C) 不恢复旧 WorkPage");

await waitFor("race-d Work entry", () => document.querySelector('[data-testid="work-sidebar-btn"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]')?.click(); });
await waitFor("race-d Work page", () => document.querySelector('[data-testid="work-item-work-race"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-new-btn"]')?.click(); });
const raceCreatePrompt = document.querySelector<HTMLTextAreaElement>('[data-testid="work-create-prompt"]');
if (!raceCreatePrompt) throw new Error("missing race create input");
await setControlValue(raceCreatePrompt, "Late Create");
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-create-submit"]')?.click(); });
await waitFor("race-d pending CreateWork", () => raceCreateCalls.includes("race-d"));
const listCallsBeforeLateCreate = raceListCalls.length;
await activateRaceTab("race-e");
createD.resolve(workView("D:/repo/race/late-created.jsonl", "work-late-created").work);
await act(async () => { await Promise.resolve(); });
ok(document.querySelector('[data-work-id="work-late-created"]') == null, "late CreateWork(D) 不错误打开旧 Work");
ok(raceListCalls.length === listCallsBeforeLateCreate, "late CreateWork(D) 不触发旧列表刷新");

await waitFor("race-e Work entry", () => document.querySelector('[data-testid="work-sidebar-btn"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-sidebar-btn"]')?.click(); });
await waitFor("race-e Work item", () => document.querySelector('[data-testid="work-item-work-race"]') != null);
await act(async () => { document.querySelector<HTMLButtonElement>('[data-testid="work-item-work-race"] button')?.click(); });
await waitFor("race-e pending GetWork", () => raceGetCalls.includes("race-e"));
await activateRaceTab("race-f");
ok(document.querySelector('[data-testid="work-card"]') == null, "同一 App tab 首次切换提交即隐藏旧 WorkCard");
getE.resolve(workView("D:/repo/race/e.jsonl", "work-race", [], []));
await act(async () => { await Promise.resolve(); });
ok(useWorkStore.getState().works["work-race"] == null, "late GetWork(E) 不污染全局 Work store");

await act(async () => { raceRoot.unmount(); });
dom.window.close();
process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exitCode = 1;
