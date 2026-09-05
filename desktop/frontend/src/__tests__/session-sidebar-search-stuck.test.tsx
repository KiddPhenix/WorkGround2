import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SearchPanel } from "../sidebar/SearchPanel";
import { loadSidebarSearch, refreshSidebarSearch } from "../sidebar/sidebarData";
import { useSidebarStore } from "../sidebar/sidebarStore";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.HTMLElement = dom.window.HTMLElement;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
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

process.stdout.write("\nsession sidebar search settle\n\n");

{
  // Regression: a stale search invocation (query/filter no longer matching the
  // live store) must be rejected before it can flip searchPage back to
  // "loading". This is the "正在搜索…" hang: a slow/hung backend SearchSidebar
  // for an already-replaced query/filter would otherwise strand the current
  // query in loading with no request to settle it.
  let issued = 0;
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      SearchSidebar: () => { issued += 1; return new Promise<never>(() => {}); }, // hangs forever
      ListSidebarIssues: async () => [],
    } } },
  });
  useSidebarStore.setState({ activeMode: "search", searchQuery: "NGA", searchFilter: "sessions", searchPage: { items: [], snapshot: "", status: "ready", requestSeq: 10 } });
  void refreshSidebarSearch("stale", "projects", 0);
  void loadSidebarSearch("stale", "projects", true);
  ok(issued === 0, "stale search invocations never issue a backend call");
  ok(useSidebarStore.getState().searchPage.status === "ready", "stale search invocations cannot re-enter loading");
  ok(useSidebarStore.getState().searchPage.requestSeq === 10, "stale search invocations leave requestSeq untouched");
  Reflect.deleteProperty(window, "go");
}

{
  // The full typing + filter-switch path must settle to a stable empty result,
  // never stay on 正在搜索….
  const searched: Array<{ query: string; filter: string }> = [];
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: {
      SearchSidebar: async (request: { query: string; filter: string }) => {
        searched.push({ query: request.query, filter: request.filter });
        return { items: [], nextCursor: undefined, total: 0, snapshot: "s" };
      },
      ListSidebarIssues: async () => [],
    } } },
  });
  useSidebarStore.setState({ activeMode: "search", searchQuery: "", searchFilter: "all", searchPage: { items: [], snapshot: "", status: "idle", requestSeq: 0 }, issues: [], issuesStatus: "idle", issuesRequestSeq: 0, issuesScope: "", issuesDataScope: "" });
  const { host, root } = await render(
    <SearchPanel now={Date.now()} unreadBySession={new Map()} onOpenSession={() => {}} onOpenSessionMenu={() => {}} />,
  );
  // Type "NGA" under 全部, then switch to 会话 before the debounce resolves.
  await act(async () => {
    useSidebarStore.getState().setSearchQuery("NGA");
    await new Promise((resolve) => globalThis.setTimeout(resolve, 300));
  });
  await act(async () => {
    useSidebarStore.getState().setSearchFilter("sessions");
    await new Promise((resolve) => globalThis.setTimeout(resolve, 300));
  });
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const state = useSidebarStore.getState();
  ok(state.searchPage.status === "ready", "typing + filter switch settles to ready");
  ok(state.searchPage.items.length === 0, "an empty sessions search keeps an empty result set");
  ok(!/正在搜索…/.test(host.textContent || ""), "the search panel never remains on 正在搜索…");
  ok(searched.some((call) => call.query === "NGA" && call.filter === "sessions"), "the final sessions search issued for the live query/filter");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

if (failed > 0) {
  process.stderr.write(`\n${passed} passed, ${failed} failed\n`);
  process.exit(1);
}
process.stdout.write(`\n${passed} passed, 0 failed\n`);
