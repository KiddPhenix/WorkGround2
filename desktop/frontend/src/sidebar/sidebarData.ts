import { app } from "../lib/bridge";
import { useSidebarStore } from "./sidebarStore";
import { mergeSearchItems, mergeSidebarSessions } from "./sidebarStore";
import type { SidebarPage, SidebarQueryMode, SidebarSearchFilter, SidebarSearchItem, SidebarSession } from "./types";

const PAGE_SIZE = 20;
// All sidebar calls share four active slots and a bounded waiting queue.
const ACTIVE_LIMIT = 4;
const WAIT_LIMIT = 32;

let requestID = 0;
let activeRequests = 0;
// Per-key merging limits duplicates; WAIT_LIMIT also bounds distinct keys.
const budgetQueue: Array<{
  live: () => boolean;
  dropped: () => void;
  run: () => Promise<void>;
}> = [];

function nextRequestID(prefix: string): string {
  requestID += 1;
  return `sidebar-${prefix}-${Date.now()}-${requestID}`;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error || "加载失败");
}

export function isSidebarCursorError(error: unknown): boolean {
  return /(?:invalid|expired).*(?:sidebar )?cursor|(?:sidebar )?cursor.*(?:invalid|expired)/i.test(errorText(error));
}

type SidebarCall<T> =
  | { status: "ok"; value: T }
  | { status: "error"; error: unknown }
  | { status: "dropped" };

// pumpBudget starts queued calls while slots are free. A call that turned stale
// while waiting is settled as "dropped" here — before the backend is invoked —
// and the slot passes to the next queue entry.
function pumpBudget(): void {
  while (activeRequests < ACTIVE_LIMIT && budgetQueue.length > 0) {
    const task = budgetQueue.shift()!;
    if (!task.live()) {
      task.dropped();
      continue;
    }
    activeRequests += 1;
    void task.run().finally(() => {
      activeRequests -= 1;
      pumpBudget();
    });
  }
}

function pruneBudget(): void {
  for (let i = budgetQueue.length - 1; i >= 0; i -= 1) {
    if (!budgetQueue[i].live()) budgetQueue.splice(i, 1)[0].dropped();
  }
}

// queueSidebarCall runs one real backend call inside the shared budget. It
// resolves with "dropped" when the call was still waiting for a slot and became
// stale: the backend was never invoked, so stale intents neither occupy a slot
// nor reach the backend.
function queueSidebarCall<T>(live: () => boolean, call: () => Promise<T>): Promise<SidebarCall<T>> {
  return new Promise<SidebarCall<T>>((resolve) => {
    pruneBudget();
    if (!live()) { resolve({ status: "dropped" }); return; }
    if (budgetQueue.length >= WAIT_LIMIT) {
      resolve({ status: "error", error: new Error("会话列表刷新繁忙，请稍后重试") });
      return;
    }
    budgetQueue.push({
      live,
      dropped: () => resolve({ status: "dropped" }),
      run: async () => {
        try {
          resolve({ status: "ok", value: await call() });
        } catch (error) {
          resolve({ status: "error", error });
        }
      },
    });
    pumpBudget();
  });
}

// Each key owns one active chain and one latest catch-up intent.
// Superseded callers settle immediately; active chains stop between pages.

interface SidebarGate {
  readonly key: string;
  busy: boolean;
  pending: SidebarChain | null;
  current?: SidebarChain;
}

interface SidebarChain {
  readonly key: string;
  // A targeted re-scan (RefreshSidebarIssues) covers a plain issues re-read, so
  // it must not be downgraded by a newer plain load merged into the same slot.
  readonly deep?: boolean;
  readonly append?: boolean;
  live?: () => boolean;
  // Runs the whole chain; resolves true when its result was applied. It must
  // never reject: failures are stored through fail* and resolve false.
  run: () => Promise<boolean>;
  settle?: (applied: boolean) => void;
}

const gates = new Map<string, SidebarGate>();

// chainSuperseded is the mid-chain stale check: the view (mode/query/group) no
// longer matches this chain, or a newer same-key intent merged in while we ran.
function chainSuperseded(key: string, live: () => boolean): boolean {
  const gate = gates.get(key);
  return !live() || gate?.current?.live?.() === false || Boolean(gate?.pending);
}

function submitChain(key: string, chain: SidebarChain): Promise<boolean> {
  return new Promise<boolean>((resolve) => {
    const gate = gates.get(key) ?? { key, busy: false, pending: null };
    // Repeated load-more clicks must not invalidate a page already being read.
    if (gate.busy && chain.append) { resolve(false); return; }
    const mode = useSidebarStore.getState().activeMode;
    const check = chain.live ?? (() => true);
    let expired = false;
    chain.live = () => !expired && useSidebarStore.getState().activeMode === mode && check();
    if (!chain.live()) { resolve(false); return; }
    const unsubscribe = useSidebarStore.subscribe(() => {
      if (!chain.live!()) expired = true;
      pruneBudget();
    });
    chain.settle = (applied) => { unsubscribe(); resolve(applied); };
    gates.set(key, gate);
    if (gate.busy) {
      mergePending(gate, chain);
      pruneBudget();
      return;
    }
    gate.busy = true;
    void drainGate(gate, chain);
  });
}

function mergePending(gate: SidebarGate, chain: SidebarChain): void {
  const prior = gate.pending;
  if (prior && !chain.deep && prior.deep) {
    // The queued targeted re-scan already yields the freshest state; a plain
    // re-read merged on top would only downgrade it. Drop the plain re-read.
    chain.settle?.(false);
    return;
  }
  gate.pending = chain;
  if (prior && prior !== chain) prior.settle?.(false); // superseded by the newer intent
}

function takePending(gate: SidebarGate): SidebarChain | null {
  const next = gate.pending;
  gate.pending = null;
  return next;
}

async function drainGate(gate: SidebarGate, first: SidebarChain): Promise<void> {
  let chain: SidebarChain | null = first;
  while (chain) {
    let applied = false;
    try {
      gate.current = chain;
      if (chain.live?.()) applied = await chain.run();
    } catch (error) {
      console.error(`[sidebar] refresh chain failed for ${chain.key}`, error);
    }
    chain.settle?.(applied);
    chain = takePending(gate);
  }
  gate.busy = false;
  gates.delete(gate.key);
}

// loadSidebarIssues refreshes the isolated-sidecar warning for one view mode.
// A failed fetch keeps any previously loaded issues for the SAME mode and only
// flips the status to "error"; a different mode clears stale issues so warnings
// never leak across Projects / ROOM / Assistant.
export async function loadSidebarIssues(mode: SidebarQueryMode): Promise<boolean> {
  return submitIssues(mode, "load");
}

// refreshSidebarIssues performs a targeted re-scan of only the plans that own an
// issue in the given mode, then re-reads that mode's issues. It is used by the
// warning retry so a collapsed project can clear its issue without a full sync.
// It resolves to true only on success, so callers can skip the follow-up list
// refresh and keep issuesStatus=error when the refresh fails.
export async function refreshSidebarIssues(mode: SidebarQueryMode): Promise<boolean> {
  return submitIssues(mode, "refresh");
}

function submitIssues(mode: SidebarQueryMode, kind: "load" | "refresh"): Promise<boolean> {
  const key = `issues:${mode}`;
  const chain: SidebarChain = {
    key,
    deep: kind === "refresh",
    run: async () => {
      const seq = useSidebarStore.getState().beginIssues(mode);
      const backend = kind === "refresh" ? () => app.RefreshSidebarIssues(mode) : () => app.ListSidebarIssues(mode);
      try {
        const call = await queueSidebarCall(() => !chainSuperseded(key, () => true), backend);
        const store = useSidebarStore.getState();
        if (call.status === "dropped" || !chain.live?.()) return false;
        if (call.status === "error") {
          store.failIssues(seq);
          return false;
        }
        store.receiveIssues(seq, mode, call.value ?? []);
        return true;
      } finally {
        useSidebarStore.getState().cancelIssues(seq);
      }
    },
  };
  return submitChain(key, chain);
}

export async function loadSidebarGroups(mode: SidebarQueryMode): Promise<boolean> {
  const key = `groups:${mode}`;
  const chain: SidebarChain = {
    key,
    run: async () => {
      const store = useSidebarStore.getState();
      // A groups load queued behind other work is only useful while the user is
      // still on that mode; switching away expires it before any backend call.
      if (store.activeMode !== mode) return false;
      const seq = store.beginGroups(mode);
      try {
        const call = await queueSidebarCall(
          () => !chainSuperseded(key, () => useSidebarStore.getState().activeMode === mode),
          () => app.ListSidebarGroups(mode),
        );
        const liveStore = useSidebarStore.getState();
        if (call.status === "dropped") return false;
        if (call.status === "error") {
          liveStore.failGroups(mode, seq, errorText(call.error));
          return false;
        }
        if (chainSuperseded(key, () => true)) return false;
        liveStore.receiveGroups(mode, seq, call.value ?? []);
        return true;
      } finally {
        useSidebarStore.getState().cancelGroups(mode, seq);
        // Tail: re-read this mode's issues once the group list settled, so a
        // collapsed project can clear its warning without a second group sync.
        if (useSidebarStore.getState().activeMode === mode) {
          await submitIssues(mode, "load");
        }
      }
    },
  };
  return submitChain(key, chain);
}

function pageLive(mode: SidebarQueryMode, groupID: string): () => boolean {
  const initial = useSidebarStore.getState();
  const tracked = initial.groupsByMode[mode]?.items.some((group) => group.id === groupID);
  const expanded = initial.expandedGroups.has(groupID);
  return () => {
    const store = useSidebarStore.getState();
    return store.activeMode === mode
      && (!tracked || Boolean(store.groupsByMode[mode]?.items.some((group) => group.id === groupID)))
      && (!(tracked || expanded) || store.expandedGroups.has(groupID));
  };
}

// runRefreshPageChain atomically re-fetches a group's loaded depth (never less
// than the current rows) and installs the result in one receivePage. It is the
// shared engine for refreshSidebarPage and for cursor-error escalation inside a
// load chain. The chain stops paging as soon as a newer same-key intent merged
// or the view changed, leaving the final catch-up chain to finish the scan.
async function runRefreshPageChain(key: string, mode: SidebarQueryMode, groupID: string, loadedCount: number): Promise<boolean> {
  const live = pageLive(mode, groupID);
  const store = useSidebarStore.getState();
  if (!live()) return false;
  const request = store.beginPage(key, true, true);
  if (!request) return false;
  const currentDepth = store.pages[key]?.items.length ?? 0;
  const targetCount = Math.max(PAGE_SIZE, loadedCount, currentDepth);
  let cursor: string | undefined;
  let combined: SidebarSession[] = [];
  let latest: SidebarPage<SidebarSession> | undefined;
  try {
    for (;;) {
      if (chainSuperseded(key, live)) return false;
      const remaining = Math.max(PAGE_SIZE, targetCount - combined.length);
      const call = await queueSidebarCall(() => !chainSuperseded(key, live), () => app.ListSidebarSessions({
        mode,
        groupId: groupID || undefined,
        cursor,
        limit: Math.min(50, remaining),
        requestId: nextRequestID(`${key}-refresh`),
      }));
      if (call.status === "dropped") return false;
      if (call.status === "error") {
        if (!chainSuperseded(key, live)) useSidebarStore.getState().failPage(key, request.seq, errorText(call.error));
        return false;
      }
      if (chainSuperseded(key, live)) return false;
      latest = call.value;
      combined = mergeSidebarSessions(combined, latest.items ?? []);
      cursor = latest.nextCursor || undefined;
      if (!cursor || combined.length >= targetCount) break;
    }
    useSidebarStore.getState().receivePage(key, request.seq, { ...latest!, items: combined, nextCursor: cursor }, true);
    return true;
  } catch (error) {
    if (!chainSuperseded(key, live)) useSidebarStore.getState().failPage(key, request.seq, errorText(error));
    return false;
  } finally {
    useSidebarStore.getState().cancelPage(key, request.seq);
    // Tail: after any completed refresh (success, failure, or cursor escalation)
    // re-read this mode's issues once — unless a newer same-key chain is pending
    // (it will run its own tail) or the user left the mode.
    if (useSidebarStore.getState().activeMode === mode && !gates.get(key)?.pending) {
      await submitIssues(mode, "load");
    }
  }
}

export async function refreshSidebarPage(mode: SidebarQueryMode, groupID: string, loadedCount: number): Promise<boolean> {
  const key = `${mode}:${groupID}`;
  return submitChain(key, { key, deep: true, live: pageLive(mode, groupID), run: () => runRefreshPageChain(key, mode, groupID, loadedCount) });
}

export async function loadSidebarPage(mode: SidebarQueryMode, groupID: string, reset: boolean): Promise<boolean> {
  const key = `${mode}:${groupID}`;
  const depth = useSidebarStore.getState().pages[key]?.items.length ?? 0;
  if (reset && depth) return refreshSidebarPage(mode, groupID, depth);
  const chain: SidebarChain = {
    key,
    append: !reset,
    live: pageLive(mode, groupID),
    run: async () => {
      const live = pageLive(mode, groupID);
      const store = useSidebarStore.getState();
      if (!live()) return false;
      const request = store.beginPage(key, reset);
      if (!request) return false;
      let applied = false;
      try {
        const call = await queueSidebarCall(() => !chainSuperseded(key, live), () => app.ListSidebarSessions({
          mode,
          groupId: groupID || undefined,
          cursor: request.cursor,
          limit: PAGE_SIZE,
          requestId: nextRequestID(key),
        }));
        if (call.status === "dropped") return false;
        if (call.status === "error") {
          const current = useSidebarStore.getState();
          if (current.pages[key]?.requestSeq !== request.seq) return false;
          if (!chainSuperseded(key, live)) {
            if (!reset && request.cursor && isSidebarCursorError(call.error)) {
              // An expired cursor means the backend rebuilt its index: rebuild
              // this group atomically at the depth already on screen instead of
              // appending onto a stale chain.
              const loadedCount = current.pages[key]?.items.length ?? PAGE_SIZE;
              return runRefreshPageChain(key, mode, groupID, loadedCount);
            }
            current.failPage(key, request.seq, errorText(call.error));
          }
          return false;
        }
        if (chainSuperseded(key, live)) return false;
        useSidebarStore.getState().receivePage(key, request.seq, call.value, reset);
        applied = true;
        return true;
      } catch (error) {
        if (!chainSuperseded(key, live)) useSidebarStore.getState().failPage(key, request.seq, errorText(error));
        return false;
      } finally {
        useSidebarStore.getState().cancelPage(key, request.seq);
        // Tail parity with the previous implementation: after the load settles
        // (including cursor-error escalation, which re-enters the refresh
        // engine), re-read this mode's issues unless superseded or left.
        if (applied && useSidebarStore.getState().activeMode === mode && !gates.get(key)?.pending) {
          await submitIssues(mode, "load");
        }
      }
    },
  };
  return submitChain(key, chain);
}

function searchLive(query: string, filter: SidebarSearchFilter): () => boolean {
  return () => {
    const store = useSidebarStore.getState();
    return store.activeMode === "search" && store.searchQuery.trim() === query && store.searchFilter === filter;
  };
}

// runRefreshSearchChain is the search counterpart of runRefreshPageChain: it
// atomically re-fetches the loaded result depth and stops paging once the query,
// filter, or view changed or a newer same-key intent merged.
async function runRefreshSearchChain(query: string, filter: SidebarSearchFilter, loadedCount: number): Promise<boolean> {
  const live = searchLive(query, filter);
  const store = useSidebarStore.getState();
  if (!live()) return false;
  const request = store.beginSearch(true, true);
  if (!request) return false;
  const currentDepth = store.searchPage.items.length;
  const targetCount = Math.max(PAGE_SIZE, loadedCount, currentDepth);
  let cursor: string | undefined;
  let combined: SidebarSearchItem[] = [];
  let latest: SidebarPage<SidebarSearchItem> | undefined;
  try {
    for (;;) {
      if (chainSuperseded("search", live)) return false;
      const remaining = Math.max(PAGE_SIZE, targetCount - combined.length);
      const call = await queueSidebarCall(() => !chainSuperseded("search", live), () => app.SearchSidebar({
        query,
        filter,
        cursor,
        limit: Math.min(50, remaining),
        requestId: nextRequestID("search-refresh"),
      }));
      if (call.status === "dropped") return false;
      if (call.status === "error") {
        if (!chainSuperseded("search", live)) useSidebarStore.getState().failSearch(request.seq, errorText(call.error));
        return false;
      }
      if (chainSuperseded("search", live)) return false;
      latest = call.value;
      combined = mergeSearchItems(combined, latest.items ?? []);
      cursor = latest.nextCursor || undefined;
      if (!cursor || combined.length >= targetCount) break;
    }
    useSidebarStore.getState().receiveSearch(request.seq, { ...latest!, items: combined, nextCursor: cursor }, true);
    return true;
  } catch (error) {
    if (!chainSuperseded("search", live)) useSidebarStore.getState().failSearch(request.seq, errorText(error));
    return false;
  } finally {
    useSidebarStore.getState().cancelSearch(request.seq);
    if (useSidebarStore.getState().activeMode === "search" && !gates.get("search")?.pending) {
      await submitIssues("projects", "load");
    }
  }
}

export async function refreshSidebarSearch(query: string, filter: SidebarSearchFilter, loadedCount: number): Promise<boolean> {
  return submitChain("search", { key: "search", deep: true, live: searchLive(query, filter), run: () => runRefreshSearchChain(query, filter, loadedCount) });
}

export async function loadSidebarSearch(query: string, filter: SidebarSearchFilter, reset: boolean): Promise<boolean> {
  const depth = useSidebarStore.getState().searchPage.items.length;
  if (reset && depth) return refreshSidebarSearch(query, filter, depth);
  const chain: SidebarChain = {
    key: "search",
    append: !reset,
    live: searchLive(query, filter),
    run: async () => {
      const live = searchLive(query, filter);
      const store = useSidebarStore.getState();
      // A stale invocation (e.g. a debounce/refresh that fired after the user
      // already replaced the query or filter) must not flip the current search
      // back to "loading". The live store is the single source of truth; reject
      // the stale request before it can issue a backend call or mutate searchPage.
      if (!live()) return false;
      const request = store.beginSearch(reset);
      if (!request) return false;
      let applied = false;
      try {
        const call = await queueSidebarCall(() => !chainSuperseded("search", live), () => app.SearchSidebar({
          query,
          filter,
          cursor: request.cursor,
          limit: PAGE_SIZE,
          requestId: nextRequestID("search"),
        }));
        if (call.status === "dropped") return false;
        if (call.status === "error") {
          const current = useSidebarStore.getState();
          if (current.searchPage.requestSeq !== request.seq) return false;
          if (!chainSuperseded("search", live)) {
            if (!reset && request.cursor && isSidebarCursorError(call.error)) {
              const loadedCount = current.searchPage.items.length;
              return runRefreshSearchChain(query, filter, loadedCount);
            }
            current.failSearch(request.seq, errorText(call.error));
          }
          return false;
        }
        if (chainSuperseded("search", live)) return false;
        useSidebarStore.getState().receiveSearch(request.seq, call.value, reset);
        applied = true;
        return true;
      } catch (error) {
        if (!chainSuperseded("search", live)) useSidebarStore.getState().failSearch(request.seq, errorText(error));
        return false;
      } finally {
        useSidebarStore.getState().cancelSearch(request.seq);
        if (applied && useSidebarStore.getState().activeMode === "search" && !gates.get("search")?.pending) {
          await submitIssues("projects", "load");
        }
      }
    },
  };
  return submitChain("search", chain);
}
