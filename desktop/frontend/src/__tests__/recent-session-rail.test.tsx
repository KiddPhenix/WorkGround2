// 最近会话 rail 聚焦测试（纯函数 + jsdom 组件 + hook 取数）。
// 运行：node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/recent-session-rail.test.tsx
// 断言：消费 Go 权威投影（app.RecentSessions）；直接复用 AgentIcon 构建规则；
// 名称渲染在图标下方；当前会话选中态；未读点；点击一次走回调；合法空集
// 覆盖旧结果，短时失败保留上次权威结果；refreshSignal 可重试、卸载后无写入。
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { buildAgentIconViewModel } from "../lib/agentIcon/viewModel";
import type { DesktopIconItem, DesktopIconStatus, RecentSessionItem } from "../lib/bridge";
import { RecentSessionRail, recentSessionCapacity } from "../sidebar/RecentSessionRail";
import {
  recentSessionKey,
  useRecentSessions,
  type RecentSessionRailItem,
} from "../sidebar/recentSessions";
import type { SidebarGroup, SidebarSession } from "../sidebar/types";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed += 1; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed += 1; }
}

const dom = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.HTMLElement = dom.window.HTMLElement;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
if (typeof dom.window.HTMLElement.prototype.animate !== "function") {
  Object.defineProperty(dom.window.HTMLElement.prototype, "animate", {
    configurable: true,
    value() { return { cancel() {} } as Animation; },
  });
}

function session(overrides: Partial<SidebarSession> = {}): SidebarSession {
  return {
    id: "topic-1",
    groupId: "project:p",
    scope: "project",
    workspaceRoot: "D:/work",
    title: "会话一",
    topicId: "topic-1",
    sessionPath: "D:/work/sessions/1.jsonl",
    sessionKind: "normal",
    revision: 1,
    ...overrides,
  };
}

function group(overrides: Partial<SidebarGroup> = {}): SidebarGroup {
  return { id: "project:p", kind: "project", label: "项目", sessionCount: 1, ...overrides };
}

function iconItem(sessionOverrides: Partial<SidebarSession> = {}, status: DesktopIconStatus = "done", groupKind = "project"): RecentSessionItem {
  const s = session(sessionOverrides);
  const g = group({ kind: groupKind });
  const item: DesktopIconItem = {
    id: `task:${s.id}`,
    kind: "task",
    sourceId: s.id,
    title: s.title,
    status,
    unreadCount: 0,
    notifications: [],
    position: { row: "bottom", zone: "running", order: 0 },
    revision: String(s.revision),
    sessionId: s.sessionId,
    workspaceIcon: g.icon,
    sessionRef: { scope: s.scope, workspaceRoot: s.workspaceRoot, topicId: s.topicId, sessionPath: s.sessionPath },
  };
  return { item, session: s, groupKind };
}

console.log("\nrecent session rail — pure mapping");

{
  ok(recentSessionKey(session({ topicId: "t", sessionPath: "p", sessionId: "sid", id: "id" })) === "p", "key prefers sessionPath");
  ok(recentSessionKey(session({ topicId: "t", sessionPath: undefined, sessionId: "sid" })) === "t", "key falls back to topicId");
  ok(recentSessionKey(session({ topicId: undefined, sessionPath: undefined, sessionId: "sid" })) === "sid", "key falls back to sessionId");
  ok(recentSessionKey(session({ topicId: undefined, sessionPath: undefined, sessionId: undefined, id: "id" })) === "id", "key falls back to id");
  ok(recentSessionCapacity(0) === 0, "zero height shows no hidden focus targets");
  ok(recentSessionCapacity(50) === 1 && recentSessionCapacity(106) === 2, "capacity follows the new 50px unit height");
}

console.log("\nrecent session rail — component");

function makeItem(overrides: Partial<SidebarSession> = {}, status: DesktopIconStatus = "done", active = false, unread = 0): RecentSessionRailItem {
  const entry = iconItem(overrides, status);
  return { key: recentSessionKey(entry.session), item: entry.item, session: entry.session, groupKind: entry.groupKind, viewModel: buildAgentIconViewModel(entry.item), active, unread };
}

async function render(node: React.ReactNode): Promise<{ host: HTMLElement; root: Root }> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => root.render(node));
  return { host, root };
}

{
  const first = makeItem({ id: "a", topicId: "a", title: "A", sessionPath: "D:/work/sessions/a.jsonl" });
  const second = makeItem({ id: "b", topicId: "b", title: "B", running: true, sessionPath: "D:/work/sessions/b.jsonl" }, "running");
  const active = makeItem({ id: "c", topicId: "c", title: "C", sessionPath: "D:/work/sessions/c.jsonl" }, "done", true, 3);
  const opened: string[] = [];
  const { host, root } = await render(
    <RecentSessionRail items={[first, second, active]} onOpen={(i) => opened.push(i.session.id)} />,
  );
  const buttons = host.querySelectorAll<HTMLButtonElement>(".session-sidebar__recent-session");
  ok(buttons.length === 3, "renders one button per recent session in order");
  ok(buttons[0].getAttribute("aria-label") === "A" && buttons[1].getAttribute("aria-label") === "B", "buttons carry the session title as aria-label/title");
  ok(buttons[0].querySelector(".agent-icon") !== null, "each recent session reuses AgentIcon");
  ok(buttons[0].querySelector(".session-sidebar__recent-label")?.textContent === "A", "session name renders under the icon");
  ok(buttons[1].querySelector(".agent-icon") !== null && buttons[1].querySelector(".agent-icon")?.getAttribute("data-eye-status") === "running", "running session expresses running eyes");
  ok(buttons[2].classList.contains("session-sidebar__recent-session--active") && buttons[2].getAttribute("aria-current") === "page", "current session has a clear selected state");
  ok(buttons[2].querySelector(".session-sidebar__recent-unread") !== null, "unread session shows the unread dot");
  await act(async () => buttons[1].click());
  ok(opened.join(",") === "b", "click routes exactly once to the matching item");
  await act(async () => buttons[1].click());
  ok(opened.join(",") === "b,b", "repeated click is safe and re-routes");
  await act(async () => root.unmount());
  host.remove();
}

{
  const unnamed = makeItem({ id: "blank", topicId: "blank", title: " " });
  unnamed.item.title = " ";
  const { host, root } = await render(<RecentSessionRail items={[unnamed]} onOpen={() => {}} />);
  ok(host.querySelector(".session-sidebar__recent-label")?.textContent === "新的会话", "every icon keeps a visible fallback name");
  await act(async () => root.unmount());
  host.remove();
}

{
  const { host, root } = await render(<RecentSessionRail items={[]} onOpen={() => {}} />);
  ok(host.querySelector(".session-sidebar__recent") === null, "empty recent list renders nothing");
  await act(async () => root.unmount());
  host.remove();
}

{
  // 右键菜单：contextmenu 精确路由到当前 item，左键打开保持不受影响。
  const target = makeItem({ id: "menu-a", topicId: "menu-a", title: "A", sessionPath: "D:/work/sessions/a.jsonl" });
  const opened: string[] = [];
  const menuOpened: Array<{ id: string; element: HTMLElement }> = [];
  const { host, root } = await render(
    <RecentSessionRail items={[target]} onOpen={(i) => opened.push(i.session.id)} onOpenMenu={(i, el) => menuOpened.push({ id: i.session.id, element: el })} />,
  );
  const button = host.querySelector<HTMLButtonElement>(".session-sidebar__recent-session");
  ok(button !== null, "recent session renders a trigger button for the context menu");
  button?.dispatchEvent(new dom.window.MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
  ok(menuOpened.length === 1 && menuOpened[0].id === "menu-a" && menuOpened[0].element === button, "contextmenu routes exactly once with the matching item and trigger element");
  await act(async () => button?.click());
  ok(opened.join(",") === "menu-a", "left click still opens the session after context-menu wiring");
  await act(async () => root.unmount());
  host.remove();
}

console.log("\nrecent session rail — hook");

function Harness({ refreshSignal, unreadBySession, activeSessionPath, activeTopicId, onOpen }: {
  refreshSignal?: number; unreadBySession: Map<string, number>; activeSessionPath?: string; activeTopicId?: string;
  onOpen: (item: RecentSessionRailItem) => void;
}) {
  const items = useRecentSessions({ refreshSignal, activeSessionPath, activeTopicId, unreadBySession });
  return <RecentSessionRail items={items} onOpen={onOpen} />;
}

{
  // 权威绑定失败时不得调用旧 SearchSidebar/ListRuntimeTabs 拼出另一套内容。
  let recentCalls = 0;
  let fallbackCalls = 0;
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      RecentSessions: async () => {
        recentCalls += 1;
        throw new Error("RecentSessions binding unavailable");
      },
      SearchSidebar: async () => { fallbackCalls += 1; return { items: [], snapshot: "fallback" }; },
      ListRuntimeTabs: async () => { fallbackCalls += 1; return []; },
    } } },
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => root.render(<Harness unreadBySession={new Map()} onOpen={() => {}} />));
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(host.querySelector(".session-sidebar__recent-session") === null, "failed first authoritative read keeps the rail empty");
  ok(recentCalls === 1 && fallbackCalls === 0, "hook never consults a second session data source");
  await act(async () => root.unmount());
  host.remove();
  Reflect.deleteProperty(window, "go");
}

{
  // 已显示权威结果后，下一次读取失败，仍保留上次成功图标。
  let fail = false;
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      RecentSessions: async () => {
        if (fail) throw new Error("temporary failure");
        return { items: [iconItem({ id: "kept", topicId: "kept", title: "保留我", sessionPath: "D:/work/sessions/kept.jsonl" })] };
      },
    } } },
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => root.render(<Harness refreshSignal={0} unreadBySession={new Map()} onOpen={() => {}} />));
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  fail = true;
  await act(async () => root.render(<Harness refreshSignal={1} unreadBySession={new Map()} onOpen={() => {}} />));
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(host.querySelector(".session-sidebar__recent-label")?.textContent === "保留我", "temporary total failure preserves the last successful rail");
  await act(async () => root.unmount());
  host.remove();
  Reflect.deleteProperty(window, "go");
}

{
  // Widget 返回合法空集合时必须清空旧 rail，不能回退到 Session List。
  let empty = false;
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      RecentSessions: async () => ({ items: empty ? [] : [iconItem({ id: "old", topicId: "old", title: "旧任务", sessionPath: "D:/work/sessions/old.jsonl" })] }),
    } } },
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => root.render(<Harness refreshSignal={0} unreadBySession={new Map()} onOpen={() => {}} />));
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  empty = true;
  await act(async () => root.render(<Harness refreshSignal={1} unreadBySession={new Map()} onOpen={() => {}} />));
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(host.querySelector(".session-sidebar__recent-session") === null, "authoritative empty widget task list clears stale rail items");
  await act(async () => root.unmount());
  host.remove();
  Reflect.deleteProperty(window, "go");
}

{
  // 卸载后迟到的异步结果不得写入（防过期覆盖：旧请求 seq 已失效）。
  let resolveRecent!: (value: unknown) => void;
  const pending: Promise<unknown> = new Promise((resolve) => { resolveRecent = resolve; });
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: { RecentSessions: async () => pending } } },
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => root.render(<Harness unreadBySession={new Map()} onOpen={() => {}} />));
  await act(async () => root.unmount());
  host.remove();
  await act(async () => { resolveRecent({ items: [iconItem({ id: "a", topicId: "a", title: "A" })] }); await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(host.querySelector(".session-sidebar__recent-session") === null, "late response after unmount never writes state");
  Reflect.deleteProperty(window, "go");
}

console.log(`\nrecent session rail: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
