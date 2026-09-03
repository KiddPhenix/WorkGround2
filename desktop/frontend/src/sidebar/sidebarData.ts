import { app } from "../lib/bridge";
import { useSidebarStore } from "./sidebarStore";
import { mergeSidebarSessions } from "./sidebarStore";
import type { SidebarPage, SidebarQueryMode, SidebarSearchFilter, SidebarSession } from "./types";
import type { SidebarSearchItem } from "./types";

const PAGE_SIZE = 20;
let requestID = 0;
let activeRequests = 0;
const waiting: Array<() => void> = [];

function nextRequestID(prefix: string): string {
  requestID += 1;
  return `sidebar-${prefix}-${Date.now()}-${requestID}`;
}

async function withRequestSlot<T>(work: () => Promise<T>): Promise<T> {
  if (activeRequests >= 4) await new Promise<void>((resolve) => waiting.push(resolve));
  activeRequests += 1;
  try {
    return await work();
  } finally {
    activeRequests -= 1;
    waiting.shift()?.();
  }
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error || "加载失败");
}

export function isSidebarCursorError(error: unknown): boolean {
  return /(?:invalid|expired).*(?:sidebar )?cursor|(?:sidebar )?cursor.*(?:invalid|expired)/i.test(errorText(error));
}

// loadSidebarIssues refreshes the isolated-sidecar warning for one view mode.
// A failed fetch keeps any previously loaded issues for the SAME mode and only
// flips the status to "error"; a different mode clears stale issues so warnings
// never leak across Projects / ROOM / Assistant.
export async function loadSidebarIssues(mode: SidebarQueryMode): Promise<void> {
  const seq = useSidebarStore.getState().beginIssues(mode);
  try {
    const issues = await app.ListSidebarIssues(mode);
    useSidebarStore.getState().receiveIssues(seq, mode, issues ?? []);
  } catch {
    useSidebarStore.getState().failIssues(seq);
  }
}

// refreshSidebarIssues performs a targeted re-scan of only the plans that own an
// issue in the given mode, then re-reads that mode's issues. It is used by the
// warning retry so a collapsed project can clear its issue without a full sync.
// It resolves to true only on success, so callers can skip the follow-up list
// refresh and keep issuesStatus=error when the refresh fails.
export async function refreshSidebarIssues(mode: SidebarQueryMode): Promise<boolean> {
  const seq = useSidebarStore.getState().beginIssues(mode);
  try {
    const issues = await app.RefreshSidebarIssues(mode);
    useSidebarStore.getState().receiveIssues(seq, mode, issues ?? []);
    return true;
  } catch {
    useSidebarStore.getState().failIssues(seq);
    return false;
  }
}

export async function loadSidebarGroups(mode: SidebarQueryMode): Promise<void> {
  const store = useSidebarStore.getState();
  const seq = store.beginGroups(mode);
  try {
    const items = await app.ListSidebarGroups(mode);
    useSidebarStore.getState().receiveGroups(mode, seq, items ?? []);
  } catch (error) {
    useSidebarStore.getState().failGroups(mode, seq, errorText(error));
  }
  if (useSidebarStore.getState().activeMode === mode) {
    await loadSidebarIssues(mode);
  }
}

export async function loadSidebarPage(mode: SidebarQueryMode, groupID: string, reset: boolean): Promise<void> {
  const key = `${mode}:${groupID}`;
  const request = useSidebarStore.getState().beginPage(key, reset);
  if (!request) return;
  try {
    const page = await withRequestSlot(() => app.ListSidebarSessions({
      mode,
      groupId: groupID || undefined,
      cursor: request.cursor,
      limit: PAGE_SIZE,
      requestId: nextRequestID(key),
    }));
    useSidebarStore.getState().receivePage(key, request.seq, page, reset);
  } catch (error) {
    if (useSidebarStore.getState().pages[key]?.requestSeq !== request.seq) return;
    if (!reset && request.cursor && isSidebarCursorError(error)) {
      const loadedCount = useSidebarStore.getState().pages[key]?.items.length ?? PAGE_SIZE;
      await refreshSidebarPage(mode, groupID, loadedCount);
      return;
    }
    useSidebarStore.getState().failPage(key, request.seq, errorText(error));
  }
  if (useSidebarStore.getState().activeMode === mode) {
    await loadSidebarIssues(mode);
  }
}

export async function refreshSidebarPage(mode: SidebarQueryMode, groupID: string, loadedCount: number): Promise<void> {
  const key = `${mode}:${groupID}`;
  const request = useSidebarStore.getState().beginPage(key, true, true);
  if (!request) return;
  const targetCount = Math.max(PAGE_SIZE, loadedCount);
  let cursor: string | undefined;
  let combined: SidebarSession[] = [];
  let latest: SidebarPage<SidebarSession> | undefined;
  try {
    do {
      const remaining = Math.max(PAGE_SIZE, targetCount - combined.length);
      latest = await withRequestSlot(() => app.ListSidebarSessions({
        mode,
        groupId: groupID || undefined,
        cursor,
        limit: Math.min(50, remaining),
        requestId: nextRequestID(`${key}-refresh`),
      }));
      combined = mergeSidebarSessions(combined, latest.items ?? []);
      cursor = latest.nextCursor || undefined;
    } while (cursor && combined.length < targetCount);
    useSidebarStore.getState().receivePage(key, request.seq, { ...latest!, items: combined, nextCursor: cursor }, true);
  } catch (error) {
    useSidebarStore.getState().failPage(key, request.seq, errorText(error));
  }
  if (useSidebarStore.getState().activeMode === mode) {
    await loadSidebarIssues(mode);
  }
}

export async function loadSidebarSearch(query: string, filter: SidebarSearchFilter, reset: boolean): Promise<void> {
  const request = useSidebarStore.getState().beginSearch(reset);
  if (!request) return;
  try {
    const page = await withRequestSlot(() => app.SearchSidebar({
      query,
      filter,
      cursor: request.cursor,
      limit: PAGE_SIZE,
      requestId: nextRequestID("search"),
    }));
    useSidebarStore.getState().receiveSearch(request.seq, page, reset);
  } catch (error) {
    const current = useSidebarStore.getState();
    if (current.searchPage.requestSeq !== request.seq || current.searchQuery.trim() !== query || current.searchFilter !== filter) return;
    if (!reset && request.cursor && isSidebarCursorError(error)) {
      await refreshSidebarSearch(query, filter, current.searchPage.items.length);
      return;
    }
    useSidebarStore.getState().failSearch(request.seq, errorText(error));
  }
  if (useSidebarStore.getState().activeMode === "search") {
    await loadSidebarIssues("projects");
  }
}

export async function refreshSidebarSearch(query: string, filter: SidebarSearchFilter, loadedCount: number): Promise<void> {
  const request = useSidebarStore.getState().beginSearch(true, true);
  if (!request) return;
  const targetCount = Math.max(PAGE_SIZE, loadedCount);
  let cursor: string | undefined;
  let combined: SidebarSearchItem[] = [];
  let latest: SidebarPage<SidebarSearchItem> | undefined;
  try {
    do {
      const remaining = Math.max(PAGE_SIZE, targetCount - combined.length);
      latest = await withRequestSlot(() => app.SearchSidebar({
        query,
        filter,
        cursor,
        limit: Math.min(50, remaining),
        requestId: nextRequestID("search-refresh"),
      }));
      const seen = new Set(combined.map((item) => `${item.kind}:${item.id}`));
      combined = [...combined, ...(latest.items ?? []).filter((item) => !seen.has(`${item.kind}:${item.id}`))];
      cursor = latest.nextCursor || undefined;
    } while (cursor && combined.length < targetCount);
    useSidebarStore.getState().receiveSearch(request.seq, { ...latest!, items: combined, nextCursor: cursor }, true);
  } catch (error) {
    useSidebarStore.getState().failSearch(request.seq, errorText(error));
  }
  if (useSidebarStore.getState().activeMode === "search") {
    await loadSidebarIssues("projects");
  }
}
