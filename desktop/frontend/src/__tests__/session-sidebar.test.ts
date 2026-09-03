import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { nextContextMenuFocus } from "../components/ContextMenu";
import { onSidebarChanged } from "../lib/bridge";
import { formatSidebarAbsoluteTime, formatSidebarRelativeTime } from "../sidebar/sidebarTime";
import { isSidebarCursorError, loadSidebarPage, loadSidebarSearch } from "../sidebar/sidebarData";
import { isSidebarMenuShortcut } from "../sidebar/sidebarKeyboard";
import { emptySidebarPage, mergeSearchItems, mergeSidebarSessions, pruneSidebarPages, useSidebarStore } from "../sidebar/sidebarStore";
import type { SidebarPage, SidebarSearchItem, SidebarSession } from "../sidebar/types";

const now = new Date(2026, 8, 3, 12, 0, 0).getTime();
assert.equal(formatSidebarRelativeTime(undefined, now), "—");
assert.equal(formatSidebarRelativeTime(now - 20_000, now), "刚刚");
assert.equal(formatSidebarRelativeTime(now - 5 * 60_000, now), "5分钟");
assert.equal(formatSidebarRelativeTime(now - 4 * 60 * 60_000 - 59 * 60_000, now), "4小时");
assert.equal(formatSidebarRelativeTime(now - 3 * 24 * 60 * 60_000, now), "3天");
assert.equal(formatSidebarRelativeTime(now + 60_000, now), "刚刚");
assert.match(formatSidebarAbsoluteTime(now), /^2026-09-03 12:00:00$/);

const session = (id: string, revision: number, title = id): SidebarSession => ({
  id,
  groupId: "project:test",
  scope: "project",
  workspaceRoot: "D:/test",
  title,
  revision,
});

assert.deepEqual(
  mergeSidebarSessions([session("a", 2, "new")], [session("a", 1, "old"), session("b", 1)]).map((item) => [item.id, item.title]),
  [["a", "new"], ["b", "b"]],
  "duplicate pages preserve the newest revision and append each session once",
);

const searchA: SidebarSearchItem = { kind: "session", id: "a", session: session("a", 1) };
assert.equal(mergeSearchItems([searchA], [searchA]).length, 1, "search page retries are idempotent");

useSidebarStore.setState({ pages: {}, pageTouchedAt: {}, searchPage: emptySidebarPage<SidebarSearchItem>() });
const first = useSidebarStore.getState().beginPage("projects:p", true)!;
const second = useSidebarStore.getState().beginPage("projects:p", true)!;
const oldPage: SidebarPage<SidebarSession> = { items: [session("old", 1)], snapshot: "old" };
const newPage: SidebarPage<SidebarSession> = { items: [session("new", 1)], snapshot: "new" };
useSidebarStore.getState().receivePage("projects:p", first.seq, oldPage, true);
useSidebarStore.getState().receivePage("projects:p", second.seq, newPage, true);
assert.deepEqual(useSidebarStore.getState().pages["projects:p"].items.map((item) => item.id), ["new"], "late page response cannot replace the newest request");

const loaded = Array.from({ length: 100 }, (_, index) => session(`loaded-${index}`, 1));
useSidebarStore.setState({
  pages: { "projects:loaded": { items: loaded, nextCursor: "old-cursor", total: 140, snapshot: "old", status: "ready", requestSeq: 8 } },
  pageTouchedAt: { "projects:loaded": 1 },
});
const atomicRefresh = useSidebarStore.getState().beginPage("projects:loaded", true, true)!;
assert.equal(useSidebarStore.getState().pages["projects:loaded"].items.length, 100, "atomic refresh retains all loaded rows while refetching");
useSidebarStore.getState().failPage("projects:loaded", atomicRefresh.seq, "refresh failed");
assert.equal(useSidebarStore.getState().pages["projects:loaded"].items.length, 100, "failed atomic refresh preserves old rows");
assert.equal(useSidebarStore.getState().pages["projects:loaded"].status, "error", "failed atomic refresh exposes its error state");

useSidebarStore.setState({ searchQuery: "old", searchFilter: "all", searchPage: { items: [searchA], nextCursor: "stale", total: 2, snapshot: "old", status: "loading", requestSeq: 12 } });
useSidebarStore.getState().setSearchQuery("new");
assert.equal(useSidebarStore.getState().searchPage.requestSeq, 13, "query changes immediately invalidate an in-flight search request");
assert.equal(useSidebarStore.getState().searchPage.items.length, 0, "query changes immediately clear mismatched search rows");
useSidebarStore.getState().receiveSearch(12, { items: [searchA], snapshot: "late" }, true);
assert.equal(useSidebarStore.getState().searchPage.items.length, 0, "an invalidated late search response cannot reappear");

const cachedPage = (id: string): ReturnType<typeof emptySidebarPage<SidebarSession>> => ({
  ...emptySidebarPage<SidebarSession>(),
  items: [session(id, 1)],
  status: "ready",
});
const pruned = pruneSidebarPages(
  { "projects:a": cachedPage("a"), "projects:b": cachedPage("b"), "projects:c": cachedPage("c") },
  { "projects:a": 1, "projects:b": 2, "projects:c": 3 },
  new Set(["b"]),
  2,
);
assert.deepEqual(Object.keys(pruned.pages).sort(), ["projects:b", "projects:c"], "LRU reclaims the oldest folded group and retains expanded pages");
assert.equal(isSidebarCursorError(new Error("invalid or expired sidebar cursor")), true, "expired cursors trigger one first-page recovery");
assert.equal(isSidebarCursorError(new Error("network unavailable")), false, "ordinary failures remain visible and retryable");
assert.equal(isSidebarMenuShortcut("F10", true), true, "Shift+F10 opens the focused row menu");
assert.equal(isSidebarMenuShortcut("F10", false), false, "plain F10 does not unexpectedly open a row menu");
assert.equal(isSidebarMenuShortcut("ContextMenu", false), true, "the keyboard ContextMenu key opens the focused row menu");
const enabledMenuItems = [0, 2, 4];
assert.equal(nextContextMenuFocus(enabledMenuItems, 0, "ArrowDown"), 2, "menu ArrowDown skips disabled items");
assert.equal(nextContextMenuFocus(enabledMenuItems, 4, "ArrowDown"), 0, "menu ArrowDown wraps at the end");
assert.equal(nextContextMenuFocus(enabledMenuItems, 0, "ArrowUp"), 4, "menu ArrowUp wraps at the start");
assert.equal(nextContextMenuFocus(enabledMenuItems, 2, "Home"), 0, "menu Home focuses the first enabled item");
assert.equal(nextContextMenuFocus(enabledMenuItems, 2, "End"), 4, "menu End focuses the last enabled item");
assert.equal(nextContextMenuFocus([], -1, "ArrowDown"), undefined, "an all-disabled menu keeps focus stable");

const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
const recoveredSessions = Array.from({ length: 40 }, (_, index) => session(`recovered-${index}`, 2));
const recoveredSearch = recoveredSessions.map((item): SidebarSearchItem => ({ kind: "session", id: item.id, session: item }));
const recoveryCounts: number[] = [];
Object.defineProperty(globalThis, "window", {
  configurable: true,
  value: {
    go: { main: { App: {
      async ListSidebarSessions(query: { cursor?: string }) {
        if (query.cursor === "expired") throw new Error("expired sidebar cursor");
        recoveryCounts.push(useSidebarStore.getState().pages["projects:recover"]?.items.length ?? 0);
        const offset = Number(query.cursor || 0);
        return { items: recoveredSessions.slice(offset, offset + 20), nextCursor: offset < 20 ? "20" : undefined, total: 40, snapshot: "recovered" };
      },
      async SearchSidebar(request: { query: string; cursor?: string }) {
        if (request.cursor === "expired") throw new Error("invalid sidebar cursor");
        const offset = Number(request.cursor || 0);
        if (request.query === "fail" && offset === 20) throw new Error("recovery network failure");
        return { items: recoveredSearch.slice(offset, offset + 20), nextCursor: offset < 20 ? "20" : undefined, total: 40, snapshot: "recovered" };
      },
    } } },
  },
});
try {
  const oldSessions = Array.from({ length: 40 }, (_, index) => session(`old-${index}`, 1));
  useSidebarStore.setState({
    pages: { "projects:recover": { items: oldSessions, nextCursor: "expired", total: 60, snapshot: "old", status: "ready", requestSeq: 0 } },
    pageTouchedAt: { "projects:recover": 1 },
  });
  await loadSidebarPage("projects", "recover", false);
  assert.deepEqual(recoveryCounts, [40, 40], "cursor recovery retains the old rows until every replacement page arrives");
  assert.equal(useSidebarStore.getState().pages["projects:recover"].items.length, 40, "cursor recovery restores the previously loaded project depth");
  assert.equal(useSidebarStore.getState().pages["projects:recover"].items[39].id, "recovered-39", "cursor recovery atomically installs the complete replacement");

  useSidebarStore.setState({ searchQuery: "recover", searchFilter: "sessions", searchPage: { items: recoveredSearch.map((item, index) => ({ ...item, id: `old-search-${index}` })), nextCursor: "expired", total: 60, snapshot: "old", status: "ready", requestSeq: 0 } });
  await loadSidebarSearch("recover", "sessions", false);
  assert.equal(useSidebarStore.getState().searchPage.items.length, 40, "search cursor recovery restores the previously loaded result depth");
  assert.equal(useSidebarStore.getState().searchPage.items[39].id, "recovered-39", "search recovery atomically installs all rebuilt pages");

  const failedRows = recoveredSearch.map((item, index) => ({ ...item, id: `preserved-${index}` }));
  useSidebarStore.setState({ searchQuery: "fail", searchFilter: "sessions", searchPage: { items: failedRows, nextCursor: "expired", total: 60, snapshot: "old", status: "ready", requestSeq: 0 } });
  await loadSidebarSearch("fail", "sessions", false);
  assert.deepEqual(useSidebarStore.getState().searchPage.items.map((item) => item.id), failedRows.map((item) => item.id), "failed search cursor recovery preserves the old results");
  assert.equal(useSidebarStore.getState().searchPage.status, "error", "failed search cursor recovery exposes a retryable error");
} finally {
  if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
  else Reflect.deleteProperty(globalThis, "window");
}

const sidebarListeners = new Map<string, () => void>();
const sidebarUnsubscribed: string[] = [];
let sidebarChanges = 0;
Object.defineProperty(globalThis, "window", {
  configurable: true,
  value: {
    go: { main: { App: {} } },
    runtime: { EventsOn(name: string, callback: () => void) {
      sidebarListeners.set(name, callback);
      return () => { sidebarListeners.delete(name); sidebarUnsubscribed.push(name); };
    } },
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  },
});
try {
  const unsubscribeSidebar = onSidebarChanged(() => { sidebarChanges += 1; });
  sidebarListeners.get("project-tree:changed")?.();
  sidebarListeners.get("session:changed")?.();
  await new Promise((resolve) => globalThis.setTimeout(resolve, 120));
  assert.equal(sidebarChanges, 1, "project and session watcher events are debounced into one sidebar refresh");
  sidebarListeners.get("session:changed")?.();
  unsubscribeSidebar();
  await new Promise((resolve) => globalThis.setTimeout(resolve, 120));
  assert.equal(sidebarChanges, 1, "sidebar event cleanup cancels a pending debounced refresh");
  assert.deepEqual(sidebarUnsubscribed.sort(), ["project-tree:changed", "session:changed"], "sidebar event cleanup unsubscribes both runtime events");
} finally {
  if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
  else Reflect.deleteProperty(globalThis, "window");
}

const currentProjectPage = cachedPage("current-project");
currentProjectPage.items[0] = { ...currentProjectPage.items[0], workspaceRoot: "D:/current" };
const cacheWithProtection = pruneSidebarPages(
  { "projects:current": currentProjectPage, "projects:old": cachedPage("old"), "projects:new": cachedPage("new") },
  { "projects:current": 1, "projects:old": 2, "projects:new": 3 },
  new Set(),
  2,
  { workspaceRoot: "d:\\current" },
);
assert.deepEqual(Object.keys(cacheWithProtection.pages).sort(), ["projects:current", "projects:new"], "LRU retains the current project even when it is folded");

const emptyCurrentProjectPage = { ...emptySidebarPage<SidebarSession>(), status: "ready" as const };
const cacheWithEmptyProjectProtection = pruneSidebarPages(
  { "projects:current": emptyCurrentProjectPage, "projects:old": cachedPage("old"), "projects:new": cachedPage("new") },
  { "projects:current": 1, "projects:old": 2, "projects:new": 3 },
  new Set(),
  1,
  { groupIds: ["current"] },
);
assert.deepEqual(Object.keys(cacheWithEmptyProjectProtection.pages).sort(), ["projects:current", "projects:new"], "LRU retains the current project group before it has session rows");

const currentSessionPage = cachedPage("current-session");
currentSessionPage.items[0] = { ...currentSessionPage.items[0], workspaceRoot: undefined, sessionPath: "D:/sessions/current.jsonl" };
const cacheWithSessionProtection = pruneSidebarPages(
  { "projects:current-session": currentSessionPage, "projects:old": cachedPage("old"), "projects:new": cachedPage("new") },
  { "projects:current-session": 1, "projects:old": 2, "projects:new": 3 },
  new Set(),
  2,
  { sessionPath: "d:\\sessions\\current.jsonl" },
);
assert.deepEqual(Object.keys(cacheWithSessionProtection.pages).sort(), ["projects:current-session", "projects:new"], "LRU retains the current session even when its group is folded");

const rail = readFileSync(resolve(import.meta.dirname, "../sidebar/PrimaryRail.tsx"), "utf8");
const shell = readFileSync(resolve(import.meta.dirname, "../sidebar/SidebarShell.tsx"), "utf8");
const projectPanel = readFileSync(resolve(import.meta.dirname, "../sidebar/ProjectPanel.tsx"), "utf8");
const sessionList = readFileSync(resolve(import.meta.dirname, "../sidebar/SessionList.tsx"), "utf8");
const sidebarData = readFileSync(resolve(import.meta.dirname, "../sidebar/sidebarData.ts"), "utf8");
const searchPanel = readFileSync(resolve(import.meta.dirname, "../sidebar/SearchPanel.tsx"), "utf8");
const bridge = readFileSync(resolve(import.meta.dirname, "../lib/bridge.ts"), "utf8");
const contextMenu = readFileSync(resolve(import.meta.dirname, "../components/ContextMenu.tsx"), "utf8");
const appSource = readFileSync(resolve(import.meta.dirname, "../App.tsx"), "utf8");
const orderedTokens = ['mode: "search"', 'mode: "projects"', 'mode: "rooms"', 'mode: "assistants"'];
let last = -1;
for (const token of orderedTokens) {
  const index = rail.indexOf(token, last + 1);
  assert.ok(index > last, `primary rail keeps ${token} in the approved order`);
  last = index;
}
assert.match(rail, /<RailButton \{\.\.\.modes\[0\]\}[\s\S]*<SquarePen[\s\S]*modes\.slice\(1\)/, "search, new session, then project/ROOM/assistant render in the approved order");
assert.match(rail, /className="session-sidebar__brand-toggle"[\s\S]*logoSymbol[\s\S]*ChevronLeft/, "W2 and the collapse chevron stay in one button");
assert.match(projectPanel, /group\.kind !== "crew" \? \(element\) => onOpenGroupMenu/, "crew groups do not expose project history, rename, or pin menus");
assert.match(projectPanel, /topicId: session\.topicId \|\| ""/, "missing topic IDs are never replaced with a session ID");
assert.match(shell, /group\.kind === "crew"[\s\S]*onOpenCrewSession\(session\.sessionPath\)/, "crew sessions use the existing crew opener");
assert.match(projectPanel, /加载更多 · 已加载 \$\{page\.items\.length\}/, "project pagination reports loaded count and total");
assert.match(projectPanel, /count: page\.status === "ready" && typeof page\.total === "number" \? page\.total : group\.sessionCount/, "a materialized group prefers the authoritative page total over its approximate summary count");
assert.match(shell, /key: "new-session"[\s\S]*key: "history"[\s\S]*key: "rename"[\s\S]*key: "appearance"[\s\S]*key: "reveal"[\s\S]*key: "remove"/, "project menus retain create, history, rename, appearance, reveal, and remove actions");
assert.match(shell, /canCustomize = isProject \|\| group\.kind === "global"/, "global groups retain rename and appearance capability");
assert.ok(/RenameProject\(root/.test(shell) && /SetProjectColor\(root/.test(shell) && /SetProjectIcon\(root/.test(shell), "global customization routes through the empty-root backend contract");
assert.match(shell, /session\.topicId \? app\.SetTopicPinned\(session\.topicId,[\s\S]*: app\.SetSessionPinned\(session\.sessionPath/, "topic and historical session pin actions use their matching bridge APIs");
assert.match(shell, /window\.confirm\(`将“\$\{session\.title\}”移到废纸篓？`\)[\s\S]*session\.topicId \? app\.TrashTopic\(session\.topicId\) : app\.DeleteSession/, "topic, crew, and orphan deletion is confirmed and routed by identity");
assert.match(sessionList, /ProjectGlyph icon=\{row\.icon\} open=\{row\.expanded\}/, "project rows render the configured icon with a folder fallback");
assert.ok(/tabIndex=\{virtualRow\.index === activeIndex \? 0 : -1\}/.test(sessionList) && /ArrowDown/.test(sessionList) && /ArrowUp/.test(sessionList) && /ArrowRight/.test(sessionList) && /ArrowLeft/.test(sessionList) && /event\.key === "Enter"/.test(sessionList), "virtual rows expose roving tabindex and basic list/tree keyboard navigation");
assert.match(sessionList, /aria-haspopup=\{row\.onMenu \? "menu" : undefined\}[\s\S]*onKeyDown=\{\(event\) => openRowMenu\(event, row\)\}[\s\S]*onContextMenu=\{\(event\) => openRowMenu\(event, row\)\}/, "focused rows expose an ARIA menu and support keyboard/native context-menu events");
assert.match(sidebarData, /refreshSidebarPage[\s\S]*targetCount[\s\S]*do \{[\s\S]*Math\.min\(50, remaining\)[\s\S]*while \(cursor && combined\.length < targetCount\)/, "refresh refetches the previously loaded row count before atomically replacing it");
assert.match(sidebarData, /isSidebarCursorError\(error\)[\s\S]*loadedCount[\s\S]*await refreshSidebarPage\(mode, groupID, loadedCount\)/, "project cursor recovery delegates to the depth-preserving atomic refresh");
assert.match(sidebarData, /isSidebarCursorError\(error\)[\s\S]*await refreshSidebarSearch\(query, filter, current\.searchPage\.items\.length\)/, "search cursor recovery delegates to the depth-preserving atomic refresh");
assert.match(projectPanel, /page\.items\.length > 0 \? refreshSidebarPage\(mode, group\.id, page\.items\.length\) : loadSidebarPage\(mode, group\.id, true\)/, "a failed deep refresh retries atomically at the previous loaded depth");
assert.match(searchPanel, /priorRefreshRef[\s\S]*refreshSignal[\s\S]*refreshSidebarSearch\(state\.searchQuery\.trim\(\), state\.searchFilter, state\.searchPage\.items\.length\)/, "search mode reacts to refresh signals without discarding its loaded depth");
assert.match(bridge, /let groupCursor: string \| undefined;[\s\S]*cursor: groupCursor[\s\S]*!groupMatches[\s\S]*while \(groupCursor\)/, "browser mock search walks every group page and lets project-name matches include its sessions");
assert.match(bridge, /kind: "crew_folder"[\s\S]*kind: "crew_session"[\s\S]*if \(mode === "projects"\) return true/, "browser mock mirrors project-mode all-category and crew behavior");
const rowMenuTabStops = [...sessionList.matchAll(/className="session-sidebar__row-menu"[^>]*tabIndex=\{(-?\d+)\}/g)];
assert.equal(rowMenuTabStops.length, 2, "group and session rows both expose a menu action");
assert.equal(rowMenuTabStops.every((match) => match[1] === "-1"), true, "each virtual row has one roving Tab stop; its menu is reached through the row shortcut or pointer");
assert.match(contextMenu, /onKeyDown=\{\(event\) => \{[\s\S]*nextContextMenuFocus[\s\S]*buttons\[next\]\?\.focus\(\)/, "the role=menu surface wires standard navigation keys to enabled item focus");
assert.match(appSource, /onSidebarChanged\(\(\) => \{[\s\S]*setProjectRevision[\s\S]*refreshTabMetas/, "the unified watcher feeds the existing debounced sidebar refreshSignal chain");

console.log("session-sidebar tests passed");
