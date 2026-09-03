import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PanelPrimaryAction, ProjectPanel } from "../sidebar/ProjectPanel";
import { PrimaryRail } from "../sidebar/PrimaryRail";
import { SearchPanel } from "../sidebar/SearchPanel";
import { useSidebarStore } from "../sidebar/sidebarStore";
import type { SidebarGroup, SidebarIssue, SidebarMode, SidebarQueryMode } from "../sidebar/types";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.HTMLElement = dom.window.HTMLElement;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
// React 19's input-event polyfill expects attachEvent/detachEvent, which jsdom
// does not provide; SearchPanel focuses its input on mount.
if (!("attachEvent" in globalThis.HTMLElement.prototype)) {
  (globalThis.HTMLElement.prototype as unknown as { attachEvent: () => void }).attachEvent = () => {};
  (globalThis.HTMLElement.prototype as unknown as { detachEvent: () => void }).detachEvent = () => {};
}

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed += 1; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed += 1; }
}

async function render(node: React.ReactNode): Promise<{ host: HTMLElement; root: Root }> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => root.render(node));
  return { host, root };
}

async function dispose(host: HTMLElement, root: Root) {
  await act(async () => root.unmount());
  host.remove();
}

process.stdout.write("\nsession sidebar actions\n\n");

{
  const selected: SidebarMode[] = [];
  let unrelatedActions = 0;
  const { host, root } = await render(
    <PrimaryRail
      panelOpen
      activeMode="projects"
      onTogglePanel={() => { unrelatedActions += 1; }}
      onMode={(mode) => selected.push(mode)}
      onNewSession={() => { unrelatedActions += 1; }}
      onOpenSettings={() => { unrelatedActions += 1; }}
    />,
  );
  for (const label of ["项目", "ROOM", "助手"]) {
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)?.click());
  }
  ok(selected.join(",") === "projects,rooms,assistants", "rail project/ROOM/assistant buttons only select their matching panel");
  ok(unrelatedActions === 0, "rail mode selection does not invoke creation or navigation actions");
  await dispose(host, root);
}

for (const [mode, label] of [
  ["projects", "创建项目"],
  ["rooms", "创建 / 加入 ROOM"],
  ["assistants", "创建助手"],
] as Array<[SidebarQueryMode, string]>) {
  let addProject = 0;
  let openMode = 0;
  const { host, root } = await render(
    <PanelPrimaryAction mode={mode} onAddProject={() => { addProject += 1; }} onModeAction={() => { openMode += 1; }} />,
  );
  const button = host.querySelector<HTMLButtonElement>(".session-sidebar__primary-action");
  ok(button?.textContent?.trim() === label, `${mode} panel renders the approved full-row CTA`);
  await act(async () => button?.click());
  const routed = mode === "projects" ? addProject === 1 && openMode === 0 : addProject === 0 && openMode === 1;
  const routeLabel = mode === "assistants"
    ? "assistants CTA emits the existing App-owned creation callback exactly once"
    : `${mode} CTA invokes the existing business callback exactly once`;
  ok(routed, routeLabel);
  await dispose(host, root);
}

{
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: { ListSidebarGroups: async () => { throw new Error("room index unavailable"); }, ListSidebarIssues: async () => [] } } },
  });
  useSidebarStore.setState({ groupsByMode: {} });
  let roomAction = 0;
  const { host, root } = await render(
    <ProjectPanel
      mode="rooms"
      now={Date.now()}
      unreadBySession={new Map()}
      onOpenSession={() => {}}
      onOpenGroupMenu={() => {}}
      onOpenSessionMenu={() => {}}
      onAddProject={() => {}}
      onModeAction={() => { roomAction += 1; }}
    />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const action = host.querySelector<HTMLButtonElement>(".session-sidebar__primary-action");
  await act(async () => action?.click());
  ok(roomAction === 1, "ROOM primary CTA remains operational when its query fails");
  ok(useSidebarStore.getState().groupsByMode.rooms?.status === "error", "ROOM query failure remains explicit below the persistent CTA");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

{
  let currentIssues: SidebarIssue[] = [{ code: "meta_decode", retryable: true, observedAt: Date.now() }];
  const groups: SidebarGroup[] = [{ id: "project:room", kind: "project", label: "Room", root: "D:/rooms", sessionCount: 1 }];
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      ListSidebarGroups: async () => groups,
      ListSidebarIssues: async () => currentIssues,
      RefreshSidebarIssues: async () => currentIssues,
    } } },
  });
  useSidebarStore.setState({ activeMode: "rooms", groupsByMode: {}, issues: [], issuesStatus: "idle", issuesRequestSeq: 0, issuesScope: "", issuesDataScope: "" });
  const { host, root } = await render(
    <ProjectPanel
      mode="rooms"
      now={Date.now()}
      unreadBySession={new Map()}
      onOpenSession={() => {}}
      onOpenGroupMenu={() => {}}
      onOpenSessionMenu={() => {}}
      onAddProject={() => {}}
      onModeAction={() => {}}
    />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const banner = host.querySelector<HTMLElement>(".session-sidebar__issues");
  ok(banner !== null, "isolated sidecar issues render a concise warning without replacing groups");
  ok(host.querySelector<HTMLButtonElement>(".session-sidebar__primary-action") !== null, "the ROOM create/join CTA stays available above the partial warning");

  currentIssues = [];
  const retry = banner?.querySelector<HTMLButtonElement>("button");
  await act(async () => { retry?.click(); await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(host.querySelector<HTMLElement>(".session-sidebar__issues") === null, "retry reloads issues and clears the warning once the sidecar is repaired");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

{
  // A failed issues fetch must render a generic error + retry (not leak a path),
  // and the primary CTA must stay above it.
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      ListSidebarGroups: async () => [],
      ListSidebarIssues: async () => { throw new Error("boom"); },
    } } },
  });
  useSidebarStore.setState({ activeMode: "projects", groupsByMode: {}, issues: [], issuesStatus: "idle", issuesRequestSeq: 0, issuesScope: "", issuesDataScope: "" });
  const { host, root } = await render(
    <ProjectPanel
      mode="projects"
      now={Date.now()}
      unreadBySession={new Map()}
      onOpenSession={() => {}}
      onOpenGroupMenu={() => {}}
      onOpenSessionMenu={() => {}}
      onAddProject={() => {}}
      onModeAction={() => {}}
    />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const errorBanner = host.querySelector<HTMLElement>(".session-sidebar__issues");
  ok(errorBanner !== null && /无法加载会话索引状态/.test(errorBanner.textContent || ""), "a failed issues fetch renders a generic retryable error");
  ok(!/D:[/\\]/.test(errorBanner?.textContent || ""), "the issues error does not leak a path");
  ok(host.querySelector<HTMLButtonElement>(".session-sidebar__primary-action") !== null, "the primary CTA stays above the issues error");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

{
  // A ROOM-scoped warning must never render while the Assistant panel is active,
  // even before the Assistant group load resolves.
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      ListSidebarGroups: async () => new Promise<never>(() => {}), // never resolves
      ListSidebarIssues: async () => [{ code: "meta_decode", retryable: true, observedAt: 1 }],
    } } },
  });
  useSidebarStore.setState({ activeMode: "assistants", groupsByMode: {}, issues: [{ code: "meta_decode", retryable: true, observedAt: 1 }], issuesStatus: "ready", issuesRequestSeq: 0, issuesScope: "rooms", issuesDataScope: "rooms" });
  const { host, root } = await render(
    <ProjectPanel
      mode="assistants"
      now={Date.now()}
      unreadBySession={new Map()}
      onOpenSession={() => {}}
      onOpenGroupMenu={() => {}}
      onOpenSessionMenu={() => {}}
      onAddProject={() => {}}
      onModeAction={() => {}}
    />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(host.querySelector<HTMLElement>(".session-sidebar__issues") === null, "a ROOM-scoped warning is hidden immediately when the Assistant panel mounts");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

{
  // The SearchPanel only shows issues scoped to "projects", never a stale ROOM warning.
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      SearchSidebar: async () => ({ items: [], nextCursor: undefined, total: 0, snapshot: "s" }),
      ListSidebarIssues: async () => [],
    } } },
  });
  useSidebarStore.setState({ activeMode: "search", searchQuery: "", searchFilter: "all", searchPage: { items: [], snapshot: "", status: "ready", requestSeq: 0 }, issues: [{ code: "meta_decode", retryable: true, observedAt: 1 }], issuesStatus: "ready", issuesRequestSeq: 0, issuesScope: "rooms", issuesDataScope: "rooms" });
  const { host, root } = await render(
    <SearchPanel now={Date.now()} unreadBySession={new Map()} onOpenSession={() => {}} onOpenSessionMenu={() => {}} />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(host.querySelector<HTMLElement>(".session-sidebar__issues") === null, "a ROOM-scoped warning is hidden in the Search panel");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

{
  // A collapsed project (no expanded groups) warning retry must call
  // RefreshSidebarIssues("projects") and clear the warning.
  const refreshCalls: string[] = [];
  let issueState: SidebarIssue[] = [{ code: "meta_decode", retryable: true, observedAt: 1 }];
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      ListSidebarGroups: async () => [],
      ListSidebarIssues: async () => issueState,
      RefreshSidebarIssues: async (mode: SidebarQueryMode) => { refreshCalls.push(mode); issueState = []; return []; },
    } } },
  });
  useSidebarStore.setState({ activeMode: "projects", groupsByMode: {}, pages: {}, expandedGroups: new Set(), issues: issueState, issuesStatus: "ready", issuesRequestSeq: 0, issuesScope: "projects", issuesDataScope: "projects" });
  const { host, root } = await render(
    <ProjectPanel
      mode="projects"
      now={Date.now()}
      unreadBySession={new Map()}
      onOpenSession={() => {}}
      onOpenGroupMenu={() => {}}
      onOpenSessionMenu={() => {}}
      onAddProject={() => {}}
      onModeAction={() => {}}
    />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const banner = host.querySelector<HTMLElement>(".session-sidebar__issues");
  ok(banner !== null, "collapsed project renders its warning");
  const retry = banner?.querySelector<HTMLButtonElement>("button");
  await act(async () => { retry?.click(); await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(refreshCalls.includes("projects"), "collapsed project warning retry calls RefreshSidebarIssues('projects')");
  ok(host.querySelector<HTMLElement>(".session-sidebar__issues") === null, "collapsed project warning clears after RefreshSidebarIssues");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

{
  // A failed RefreshSidebarIssues must keep issuesStatus=error and must NOT run
  // the follow-up group/list refresh.
  let groupCalls = 0;
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      ListSidebarGroups: async () => { groupCalls += 1; return []; },
      ListSidebarIssues: async () => [{ code: "meta_decode", retryable: true, observedAt: 1 }],
      RefreshSidebarIssues: async () => { throw new Error("refresh rejected"); },
    } } },
  });
  useSidebarStore.setState({ activeMode: "projects", groupsByMode: {}, issues: [{ code: "meta_decode", retryable: true, observedAt: 1 }], issuesStatus: "ready", issuesRequestSeq: 0, issuesScope: "projects", issuesDataScope: "projects" });
  const { host, root } = await render(
    <ProjectPanel
      mode="projects"
      now={Date.now()}
      unreadBySession={new Map()}
      onOpenSession={() => {}}
      onOpenGroupMenu={() => {}}
      onOpenSessionMenu={() => {}}
      onAddProject={() => {}}
      onModeAction={() => {}}
    />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const banner = host.querySelector<HTMLElement>(".session-sidebar__issues");
  const retry = banner?.querySelector<HTMLButtonElement>("button");
  await act(async () => { retry?.click(); await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const state = useSidebarStore.getState();
  ok(state.issuesStatus === "error", "a rejected RefreshSidebarIssues keeps issuesStatus=error");
  ok(host.querySelector<HTMLElement>(".session-sidebar__issues") !== null && /无法加载会话索引状态/.test(host.querySelector<HTMLElement>(".session-sidebar__issues")!.textContent || ""), "the error banner stays visible after a rejected refresh");
  ok(groupCalls === 1, "a rejected RefreshSidebarIssues does not trigger the follow-up group refresh");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

{
  // A deferred search-warning retry must use the live query after resolution, not
  // the click-time query.
  const searchedQueries: string[] = [];
  let resolveRefresh!: (value: SidebarIssue[]) => void;
  const refreshPromise: Promise<SidebarIssue[]> = new Promise((resolve) => { resolveRefresh = resolve as (value: SidebarIssue[]) => void; });
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      SearchSidebar: async (request: { query: string; limit: number }) => {
        searchedQueries.push(request.query);
        return { items: [{ kind: "session", id: `r-${request.query}`, session: { id: `r-${request.query}`, groupId: "g", scope: "project", title: request.query, revision: 1 } }], nextCursor: undefined, total: 1, snapshot: "s" };
      },
      ListSidebarIssues: async () => [],
      RefreshSidebarIssues: async () => refreshPromise,
    } } },
  });
  useSidebarStore.setState({ activeMode: "search", searchQuery: "A", searchFilter: "all", searchPage: { items: [], snapshot: "", status: "ready", requestSeq: 0 }, issues: [{ code: "meta_decode", retryable: true, observedAt: 1 }], issuesStatus: "ready", issuesRequestSeq: 0, issuesScope: "projects", issuesDataScope: "projects" });
  const { host, root } = await render(
    <SearchPanel now={Date.now()} unreadBySession={new Map()} onOpenSession={() => {}} onOpenSessionMenu={() => {}} />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const retry = host.querySelector<HTMLButtonElement>(".session-sidebar__issues button");
  await act(async () => { retry?.click(); });
  // While RefreshSidebarIssues is pending, the user changes the query to B.
  act(() => { useSidebarStore.setState({ searchQuery: "B" }); });
  await act(async () => { resolveRefresh([]); await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  ok(!searchedQueries.includes("A"), "the deferred search retry never issues the stale query A");
  ok(searchedQueries.includes("B"), "the deferred search retry issues the live query B after resolution");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

if (failed > 0) {
  process.stderr.write(`\n${passed} passed, ${failed} failed\n`);
  process.exit(1);
}
process.stdout.write(`\n${passed} passed, 0 failed\n`);
