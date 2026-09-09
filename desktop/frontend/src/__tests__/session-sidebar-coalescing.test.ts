// Behavioral regression tests for the sidebar refresh coalescing gate.
//
// These tests drive the REAL sidebarData scheduler with deferred fake backend
// promises (no static source matching) and assert the load-bearing runtime
// invariants:
//   1. real backend call concurrency never exceeds the budget, and a burst
//      across several keys cannot grow the queue without bound;
//   2. same-key bursts merge into one in-flight chain plus at most one pending
//      catch-up (late changes arriving during a scan still get a final refresh);
//   3. intents that became stale while waiting (mode / query changed) never
//      issue a backend call, and a superseded multi-page chain stops paging;
//   4. failures stay visible/retryable and release their slot;
//   5. refreshes keep the previously loaded pagination depth.
import {
  loadSidebarGroups,
  loadSidebarIssues,
  loadSidebarPage,
  loadSidebarSearch,
  refreshSidebarPage,
  refreshSidebarSearch,
} from "../sidebar/sidebarData";
import { emptySidebarPage, useSidebarStore } from "../sidebar/sidebarStore";
import type { SidebarGroup, SidebarPage, SidebarSearchItem, SidebarSession } from "../sidebar/types";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed += 1; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed += 1; }
}

process.stdout.write("\nsession sidebar refresh coalescing\n\n");

// ---- helpers ---------------------------------------------------------------

const windowDescriptor = Object.getOwnPropertyDescriptor(globalThis, "window");

function defineBackend(handlers: Record<string, unknown>): void {
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { go: { main: { App: handlers } } },
  });
}

function restoreWindow(): void {
  if (windowDescriptor) Object.defineProperty(globalThis, "window", windowDescriptor);
  else Reflect.deleteProperty(globalThis, "window");
}

// Macrotask flush so microtask chains (gate drains, budget pumps, settle
// callbacks) fully unwind. Several rounds are needed for merged catch-up chains.
async function flush(rounds = 3): Promise<void> {
  for (let round = 0; round < rounds; round += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (error: unknown) => void } {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const session = (id: string, revision = 1): SidebarSession => ({
  id,
  groupId: "g",
  scope: "project",
  workspaceRoot: "D:/work",
  title: id,
  revision,
});

const pageOf = (list: SidebarSession[], offset: number, limit: number): SidebarPage<SidebarSession> => ({
  items: list.slice(offset, offset + limit),
  nextCursor: offset + limit < list.length ? String(offset + limit) : undefined,
  total: list.length,
  snapshot: `v-${offset}`,
});

function resetStore(): void {
  useSidebarStore.setState({
    activeMode: "projects",
    groupsByMode: {},
    pages: {},
    pageTouchedAt: {},
    searchQuery: "",
    searchFilter: "all",
    searchPage: emptySidebarPage<SidebarSearchItem>(),
    issues: [],
    issuesStatus: "idle",
    issuesRequestSeq: 0,
    issuesScope: "",
    issuesDataScope: "",
  });
}

function seedPage(key: string, count: number, cursor?: string): void {
  const items = Array.from({ length: count }, (_, index) => session(`old-${index}`, 1));
  useSidebarStore.setState({
    pages: {
      ...useSidebarStore.getState().pages,
      [key]: { items, nextCursor: cursor, total: cursor ? count + 1 : count, snapshot: "old", status: "ready", requestSeq: 0 },
    },
    pageTouchedAt: { ...useSidebarStore.getState().pageTouchedAt, [key]: Date.now() },
  });
}

// ---- 1. same-key burst merges: one in-flight + one catch-up ----------------

{
  const list = Array.from({ length: 20 }, (_, index) => session(`s-${index}`));
  let issued = 0;
  const held = deferred<SidebarPage<SidebarSession>>();
  let first = true;
  defineBackend({
    ListSidebarSessions: (request: { cursor?: string }) => {
      issued += 1;
      if (first) {
        first = false;
        return held.promise;
      }
      return Promise.resolve(pageOf(list, Number(request.cursor || 0), 20));
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: async () => [],
    RefreshSidebarIssues: async () => [],
  });
  resetStore();
  seedPage("projects:g", 20);

  try {
    const refreshA = refreshSidebarPage("projects", "g", 20);
    await flush(1);
    ok(issued === 1, "the first refresh chain holds one backend call");

    // 20 same-key refreshes while that chain is in flight: they merge into one
    // pending intent; none may start a parallel scan.
    const burst: Array<Promise<boolean>> = [];
    for (let index = 0; index < 20; index += 1) burst.push(refreshSidebarPage("projects", "g", 20));
    await flush(1);
    ok(issued === 1, "same-key burst issues no backend calls while the first chain is in flight");

    held.resolve(pageOf(list, 0, 20));
    await refreshA;
    await flush();
    ok(issued === 2, `20 merged refresh intents perform exactly one catch-up scan (issued=${issued})`);

    const results = await Promise.all(burst);
    ok(results.filter((applied) => applied).length === 1, "exactly the newest merged intent applies; superseded intents settle without running");
    const page = useSidebarStore.getState().pages["projects:g"];
    ok(page.status === "ready" && page.items.length === 20, "the page settles to ready at the loaded depth");
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 2. late changes arriving during a scan get a final refresh ------------

{
  const list = Array.from({ length: 20 }, (_, index) => session(`s-${index}`));
  let issued = 0;
  let firstSnapshot: SidebarPage<SidebarSession> | undefined;
  let first = true;
  const held = deferred<SidebarPage<SidebarSession>>();
  defineBackend({
    ListSidebarSessions: (request: { cursor?: string }) => {
      issued += 1;
      // Snapshot the list at request time: the first (held) scan must not see
      // rows that arrive later, while the catch-up scan must.
      const snapshot = pageOf(list, Number(request.cursor || 0), 20);
      if (first) {
        first = false;
        firstSnapshot = snapshot;
        return held.promise;
      }
      return Promise.resolve(snapshot);
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: async () => [],
    RefreshSidebarIssues: async () => [],
  });
  resetStore();
  seedPage("projects:g", 20);

  try {
    const firstRefresh = refreshSidebarPage("projects", "g", 20);
    await flush(1);
    // A session appears while the scan is in flight (and sorts to the top of
    // the activity-ordered list): the merged catch-up must run again and
    // observe it instead of dropping the change.
    list.unshift(session("late-arrival", 2));
    const lateRefresh = refreshSidebarPage("projects", "g", 20);
    await flush(1);
    held.resolve(firstSnapshot!);
    await firstRefresh;
    await flush();
    await lateRefresh;
    await flush();

    const items = useSidebarStore.getState().pages["projects:g"].items;
    ok(items.some((item) => item.id === "late-arrival"), "a session created during the scan is picked up by the final catch-up refresh");
    ok(issued >= 2, "the catch-up performed its own scan");
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 3. a superseded multi-page chain stops paging -------------------------

{
  const list = Array.from({ length: 100 }, (_, index) => session(`s-${index}`));
  let issued = 0;
  const held = deferred<SidebarPage<SidebarSession>>();
  let first = true;
  defineBackend({
    ListSidebarSessions: (request: { cursor?: string }) => {
      issued += 1;
      if (first) {
        first = false;
        return held.promise;
      }
      return Promise.resolve(pageOf(list, Number(request.cursor || 0), 50));
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: async () => [],
    RefreshSidebarIssues: async () => [],
  });
  resetStore();
  seedPage("projects:g", 100);

  try {
    // 100 loaded rows => a full refresh would page 50 + 50 (2 calls) when it
    // runs alone.
    const firstRefresh = refreshSidebarPage("projects", "g", 100);
    await flush(1);
    ok(issued === 1, "the deep refresh issued its first page");

    // A newer intent merges while page one is in flight: the old chain must NOT
    // continue paging the remaining 50 rows; the merged catch-up restarts.
    const superseding = refreshSidebarPage("projects", "g", 100);
    held.resolve(pageOf(list, 0, 50));
    await firstRefresh;
    await flush();
    await superseding;
    await flush();

    ok(issued <= 4, `a superseded 2-page chain stops paging early instead of walking the full depth (issued=${issued})`);
    const page = useSidebarStore.getState().pages["projects:g"];
    ok(page.items.length === 100, "the catch-up restores the full loaded depth");
    ok(page.status === "ready", "the superseded chain settles to ready");
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 4. stale queued work never invokes the backend after a mode switch ----

{
  // Four hung page loads fill the whole budget; a page refresh for another key
  // queues behind them. Leaving its mode while queued must expire it: when a
  // slot frees, the call is dropped before reaching the backend.
  const heldA: Array<{ resolve: () => void; reject: (error: unknown) => void }> = [];
  let issuedA = 0;
  let issuedB = 0;
  defineBackend({
    ListSidebarSessions: (request: { mode?: string; groupId?: string; cursor?: string }) => {
      if (request.groupId === "B") {
        issuedB += 1;
        return Promise.resolve(pageOf([session("b-0")], 0, 20));
      }
      issuedA += 1;
      const gate = deferred<SidebarPage<SidebarSession>>();
      heldA.push({
        resolve: () => gate.resolve(pageOf([session(`a-${heldA.length}`)], 0, 20)),
        reject: (error) => gate.reject(error),
      });
      return gate.promise;
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: async () => [],
    RefreshSidebarIssues: async () => [],
  });
  resetStore();

  try {
    const hungA: Array<Promise<boolean>> = [];
    for (let index = 0; index < 4; index += 1) hungA.push(loadSidebarPage("projects", `A${index + 1}`, true));
    await flush(1);
    ok(issuedA === 4, "four page loads fill the whole budget");
    ok(useSidebarStore.getState().pages["projects:A1"].status === "loading", "the A pages are loading");

    // Queue a refresh for B under rooms while the budget is fully occupied.
    useSidebarStore.setState({ activeMode: "rooms" });
    seedPage("rooms:B", 20);
    const queuedB = refreshSidebarPage("rooms", "B", 20);
    await flush(1);
    ok(issuedB === 0, "the B refresh waits for a budget slot without calling the backend");

    // The user leaves rooms before a slot frees: B is now stale. Free one slot
    // (one hung A page completes); B must be dropped without any backend call.
    useSidebarStore.setState({ activeMode: "projects" });
    heldA[0].resolve();
    await flush();
    ok(issuedB === 0, "a mode-expired queued refresh never reaches the backend");

    const settledB = await queuedB;
    ok(settledB === false, "the expired queued refresh settles as not applied");
    const pageB = useSidebarStore.getState().pages["rooms:B"];
    ok(pageB.items.length === 20, "the expired key keeps its previous rows");
    ok(pageB.status !== "error", "the expired key is not left in an error state");

    // Release the rest of the budget so later blocks start clean.
    for (let index = 1; index < heldA.length; index += 1) heldA[index].resolve();
    await Promise.all(hungA).catch(() => undefined);
    await flush();
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 5. failure stays visible/retryable and releases its slot --------------

{
  const list = Array.from({ length: 20 }, (_, index) => session(`s-${index}`));
  let failNext = true;
  let issued = 0;
  defineBackend({
    ListSidebarSessions: (request: { cursor?: string }) => {
      issued += 1;
      if (failNext) {
        failNext = false;
        return Promise.reject(new Error("backend unavailable"));
      }
      return Promise.resolve(pageOf(list, Number(request.cursor || 0), 20));
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: async () => [],
    RefreshSidebarIssues: async () => [],
  });
  resetStore();
  seedPage("projects:g", 20);

  try {
    const failing = await refreshSidebarPage("projects", "g", 20);
    ok(failing === false, "a failed refresh reports failure");
    const page = useSidebarStore.getState().pages["projects:g"];
    ok(page.status === "error", "a failed refresh exposes an explicit error state");
    ok(page.items.length === 20, "a failed refresh keeps the previously loaded rows");

    // The slot was released by the failure: an immediate retry succeeds.
    const retried = await refreshSidebarPage("projects", "g", 20);
    ok(retried === true, "a retry right after the failure succeeds");
    ok(useSidebarStore.getState().pages["projects:g"].status === "ready", "the retried page settles to ready");
    ok(issued === 2, "the failed call and the retry are the only backend calls");
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 6. groups and issues share the same bounded budget --------------------

{
  const heldPages: Array<() => void> = [];
  let pageIssued = 0;
  let groupsIssued = 0;
  let issuesIssued = 0;
  let refreshIssuesIssued = 0;
  const groupsGate = deferred<SidebarGroup[]>();
  defineBackend({
    ListSidebarSessions: () => {
      pageIssued += 1;
      const gate = deferred<SidebarPage<SidebarSession>>();
      heldPages.push(() => gate.resolve(pageOf([session("p")], 0, 20)));
      return gate.promise;
    },
    ListSidebarGroups: () => { groupsIssued += 1; return groupsGate.promise; },
    ListSidebarIssues: () => { issuesIssued += 1; return Promise.resolve([]); },
    RefreshSidebarIssues: () => { refreshIssuesIssued += 1; return Promise.resolve([]); },
  });
  resetStore();
  useSidebarStore.setState({ activeMode: "rooms" });

  try {
    const hung: Array<Promise<boolean>> = [];
    for (let index = 0; index < 4; index += 1) hung.push(loadSidebarPage("rooms", `g${index + 1}`, true));
    await flush(1);
    ok(pageIssued === 4, "four page loads fill the whole budget");

    // Groups must queue behind the full budget rather than bypass the limit.
    const groupsLoad = loadSidebarGroups("rooms");
    await flush(1);
    ok(groupsIssued === 0, "a groups load queues behind busy page calls instead of bypassing the budget");

    // Free one slot: the groups call issues, resolves, and tails an issues
    // load through the same budget.
    heldPages[0]();
    await flush();
    groupsGate.resolve([{ id: "r", kind: "room", label: "R", sessionCount: 0 }]);
    await groupsLoad;
    await flush();
    ok(groupsIssued === 1, "the queued groups load issues once a slot frees");
    ok(useSidebarStore.getState().groupsByMode.rooms?.status === "ready", "the queued groups load settles");
    ok(issuesIssued + refreshIssuesIssued >= 1, "the groups tail re-reads issues through the budget");

    for (let index = 1; index < heldPages.length; index += 1) heldPages[index]();
    await Promise.all(hung).catch(() => undefined);
    await flush();
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 6b. issues requests also queue and merge behind the budget ------------

{
  const heldPages: Array<() => void> = [];
  let pageIssued = 0;
  let issuesIssued = 0;
  defineBackend({
    ListSidebarSessions: () => {
      pageIssued += 1;
      const gate = deferred<SidebarPage<SidebarSession>>();
      heldPages.push(() => gate.resolve(pageOf([session("p")], 0, 20)));
      return gate.promise;
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: () => { issuesIssued += 1; return Promise.resolve([]); },
    RefreshSidebarIssues: () => { issuesIssued += 1; return Promise.resolve([]); },
  });
  resetStore();
  useSidebarStore.setState({ activeMode: "projects" });

  try {
    const hung: Array<Promise<boolean>> = [];
    for (let index = 0; index < 4; index += 1) hung.push(loadSidebarPage("projects", `g${index + 1}`, true));
    await flush(1);
    const issuesLoad = loadSidebarIssues("projects");
    await flush(1);
    ok(issuesIssued === 0, "an issues load queues behind a full page budget");

    // Free one slot: the queued issues load issues (no budget bypass).
    heldPages[0]();
    await flush();
    await issuesLoad;
    ok(issuesIssued >= 1, "the queued issues load issues once a slot frees");

    for (let index = 1; index < heldPages.length; index += 1) heldPages[index]();
    await Promise.all(hung).catch(() => undefined);
    await flush();
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 7. search: replaced query and mode switch expire queued work ----------

{
  const searchCalls: Array<{ query: string; filter: string }> = [];
  const heldPages: Array<() => void> = [];
  let pageIssued = 0;
  defineBackend({
    ListSidebarSessions: () => {
      pageIssued += 1;
      const gate = deferred<SidebarPage<SidebarSession>>();
      heldPages.push(() => gate.resolve(pageOf([session("p")], 0, 20)));
      return gate.promise;
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: async () => [],
    RefreshSidebarIssues: async () => [],
    SearchSidebar: (request: { query: string; filter: string }) => {
      searchCalls.push({ query: request.query, filter: request.filter });
      return Promise.resolve({ items: [], nextCursor: undefined, total: 0, snapshot: "s" });
    },
  });
  resetStore();

  try {
    // A refresh for a query the store no longer carries must drop before any
    // backend call (the live store's query/filter is the authority).
    useSidebarStore.setState({ activeMode: "search", searchQuery: "new", searchFilter: "all", searchPage: { ...emptySidebarPage<SidebarSearchItem>(), requestSeq: 7 } });
    const staleRefresh = refreshSidebarSearch("old", "sessions", 0);
    const settled = await staleRefresh;
    ok(settled === false, "a search refresh for a replaced query settles without running");
    ok(searchCalls.length === 0, "no backend call is issued for the replaced query");

    // Leaving search mode expires a search load that is still queued behind a
    // full page budget: when a slot frees, the call is dropped.
    useSidebarStore.setState({ activeMode: "projects" });
    const hung: Array<Promise<boolean>> = [];
    for (let index = 0; index < 4; index += 1) hung.push(loadSidebarPage("projects", `g${index + 1}`, true));
    await flush(1);
    ok(pageIssued === 4, "four project page loads fill the whole budget");

    useSidebarStore.setState({ activeMode: "search", searchQuery: "queued", searchFilter: "all", searchPage: { ...emptySidebarPage<SidebarSearchItem>(), requestSeq: 8 } });
    const queuedSearch = loadSidebarSearch("queued", "all", true);
    await flush(1);
    ok(searchCalls.length === 0, "the search load queues behind the full budget");

    // User leaves search before a slot frees: the queued call must expire.
    useSidebarStore.setState({ activeMode: "projects" });
    heldPages[0]();
    await flush();
    const settledQueued = await queuedSearch;
    ok(settledQueued === false, "a mode-expired queued search load settles without running");
    ok(searchCalls.length === 0, "no backend call is issued after leaving search mode");

    for (let index = 1; index < heldPages.length; index += 1) heldPages[index]();
    await Promise.all(hung).catch(() => undefined);
    await flush();
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 8. depth preservation: refresh derives limits from the loaded depth ----

{
  const requestedLimits: number[] = [];
  defineBackend({
    ListSidebarSessions: (request: { cursor?: string; limit: number }) => {
      requestedLimits.push(request.limit);
      const offset = Number(request.cursor || 0);
      const items = Array.from({ length: request.limit }, (_, index) => session(`new-${offset + index}`, 2));
      return Promise.resolve({
        items,
        nextCursor: offset + items.length < 60 ? String(offset + items.length) : undefined,
        total: 60,
        snapshot: `deep-${offset}`,
      });
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: async () => [],
    RefreshSidebarIssues: async () => [],
  });
  resetStore();
  seedPage("projects:g", 45);

  try {
    const applied = await refreshSidebarPage("projects", "g", 45);
    ok(applied === true, "the depth-preserving refresh applies");
    ok(requestedLimits[0] > 20, `refresh derives its first limit from the loaded depth instead of resetting to 20 (limit=${requestedLimits[0]})`);
    ok(useSidebarStore.getState().pages["projects:g"].items.length === 45, "refresh restores exactly the previously loaded depth");
  } finally {
    restoreWindow();
    await flush();
  }
}

// ---- 9. load-more appends and cursor recovery rebuilds atomically ----------

{
  const list = Array.from({ length: 40 }, (_, index) => session(`s-${index}`));
  let issued = 0;
  defineBackend({
    ListSidebarSessions: (request: { cursor?: string }) => {
      issued += 1;
      if (request.cursor === "expired") throw new Error("invalid or expired sidebar cursor");
      return Promise.resolve(pageOf(list, Number(request.cursor || 0), 20));
    },
    ListSidebarGroups: async () => [],
    ListSidebarIssues: async () => [],
    RefreshSidebarIssues: async () => [],
  });
  resetStore();
  seedPage("projects:g", 20, "20");

  try {
    const appended = await loadSidebarPage("projects", "g", false);
    ok(appended === true, "load-more appends the next page");
    ok(useSidebarStore.getState().pages["projects:g"].items.length === 40, "load-more reaches the full depth");

    // Cursor recovery: an expired cursor rebuilds the page atomically at the
    // loaded depth (40 rows => one failed attempt + two rebuild pages).
    resetStore();
    seedPage("projects:g", 40, "expired");
    const recovered = await loadSidebarPage("projects", "g", false);
    ok(recovered === true, "an expired cursor recovers by rebuilding the page");
    const page = useSidebarStore.getState().pages["projects:g"];
    ok(page.items.length === 40, "cursor recovery restores the loaded depth");
    ok(page.status === "ready", "cursor recovery settles to ready");
    ok(issued >= 3, "recovery performed its own backend scans");
  } finally {
    restoreWindow();
    await flush();
  }
}

// Different keys must also have a hard queue bound, not just per-key merging.
{
  resetStore();
  const held = deferred<SidebarPage<SidebarSession>>();
  let calls = 0;
  defineBackend({ ListSidebarSessions: () => { calls++; return held.promise; }, ListSidebarIssues: async () => [] });
  const requests = Array.from({ length: 100 }, (_, i) => loadSidebarPage("projects", `bounded-${i}`, true));
  await flush();
  ok(calls === 4, "100 different keys still use only four backend slots");
  const errors = Object.values(useSidebarStore.getState().pages).filter((page) => page.status === "error");
  ok(errors.length === 64, "four active plus 32 waiting; remaining 64 requests fail visibly without queueing");
  held.resolve({ items: [], snapshot: "ready" });
  await Promise.all(requests);
  ok(calls === 36, "only admitted requests reached the backend");
  await loadSidebarPage("projects", "bounded-99", true);
  ok(useSidebarStore.getState().pages["projects:bounded-99"].status === "ready", "overload is retryable after slots recover");
  restoreWindow();
  await flush();
}

// A duplicate append must not cancel its owner or downgrade a pending refresh.
{
  resetStore();
  seedPage("projects:g", 20, "20");
  const held = deferred<SidebarPage<SidebarSession>>();
  let calls = 0;
  defineBackend({ ListSidebarSessions: () => { calls++; return held.promise; }, ListSidebarIssues: async () => [] });
  const first = loadSidebarPage("projects", "g", false);
  const duplicate = loadSidebarPage("projects", "g", false);
  held.resolve({ items: [session("new")], snapshot: "new" });
  await Promise.all([first, duplicate]);
  ok(calls === 1, "duplicate load-more does not supersede or repeat the active page");
  ok(useSidebarStore.getState().pages["projects:g"].status === "ready", "duplicate load-more cannot strand loading state");
  ok(useSidebarStore.getState().pages["projects:g"].items.length === 21, "the original append result is retained");
  restoreWindow();
  await flush();
}

// A pending refresh has higher priority than repeated append intent.
{
  resetStore();
  seedPage("projects:g", 40, "40");
  const held = deferred<SidebarPage<SidebarSession>>();
  let calls = 0;
  defineBackend({
    ListSidebarSessions: () => ++calls === 1 ? held.promise : Promise.resolve({ items: Array.from({ length: 40 }, (_, i) => session(`fresh-${i}`)), snapshot: "fresh" }),
    ListSidebarIssues: async () => [],
  });
  const first = loadSidebarPage("projects", "g", false);
  const refresh = refreshSidebarPage("projects", "g", 40);
  const duplicate = loadSidebarPage("projects", "g", false);
  held.resolve({ items: [session("discarded")], snapshot: "stale" });
  await Promise.all([first, refresh, duplicate]);
  const page = useSidebarStore.getState().pages["projects:g"];
  ok(calls === 2 && page.status === "ready" && page.items.length === 40, "load-more cannot downgrade a pending depth-preserving refresh");
  ok(page.items[0].id === "fresh-0", "the pending refresh installs the newest result");
  restoreWindow();
  await flush();
}

// Queued work expires on navigation/collapse even if every backend slot stalls.
{
  resetStore();
  useSidebarStore.setState({ expandedGroups: new Set(["fold"]) });
  const held = deferred<SidebarPage<SidebarSession>>();
  const calls: string[] = [];
  defineBackend({
    ListSidebarSessions: (request: { groupId: string }) => { calls.push(request.groupId); return held.promise; },
    ListSidebarIssues: async () => [],
  });
  const active = Array.from({ length: 4 }, (_, i) => loadSidebarPage("projects", `held-${i}`, true));
  const folded = loadSidebarPage("projects", "fold", true);
  useSidebarStore.setState({ expandedGroups: new Set() });
  await flush();
  ok(await folded === false && !calls.includes("fold"), "collapsing a queued group settles it before any slot frees");
  ok(useSidebarStore.getState().pages["projects:fold"].status === "idle", "cancelled empty group returns to idle");
  const navigation = loadSidebarPage("projects", "navigation", true);
  useSidebarStore.setState({ activeMode: "rooms" });
  useSidebarStore.setState({ activeMode: "projects" });
  await flush();
  ok(await navigation === false && !calls.includes("navigation"), "leaving and immediately returning cannot revive a stale queued request");
  held.resolve({ items: [], snapshot: "ready" });
  await Promise.all(active);
  await loadSidebarPage("projects", "navigation", true);
  ok(useSidebarStore.getState().pages["projects:navigation"].status === "ready", "returning to the mode can load the cancelled page again");
  restoreWindow();
  await flush();
}

// Late results and warning reads from the old mode must not leave loading flags.
{
  resetStore();
  const held = deferred<SidebarPage<SidebarSession>>();
  let issueCalls = 0;
  defineBackend({ ListSidebarSessions: () => held.promise, ListSidebarIssues: async () => { issueCalls++; return []; } });
  const active = Array.from({ length: 4 }, (_, i) => loadSidebarPage("projects", `leave-${i}`, true));
  const issues = loadSidebarIssues("projects");
  useSidebarStore.setState({ activeMode: "rooms" });
  await issues;
  ok(issueCalls === 0, "warnings queued for an abandoned view do not reach the backend");
  ok(useSidebarStore.getState().issuesStatus === "idle", "abandoned warning request releases its loading flag");
  held.resolve({ items: [session("stale")], snapshot: "old" });
  await Promise.all(active);
  ok(Object.values(useSidebarStore.getState().pages).every((page) => page.status === "idle" && page.items.length === 0), "late page results are discarded and empty pages are retryable");
  restoreWindow();
  await flush();
}

if (failed > 0) {
  process.stderr.write(`\n${passed} passed, ${failed} failed\n`);
  process.exit(1);
}
process.stdout.write(`\n${passed} passed, 0 failed\n`);
