import { create } from "zustand";
import type {
  SidebarGroup,
  SidebarIssue,
  SidebarMode,
  SidebarPage,
  SidebarPageState,
  SidebarSearchFilter,
  SidebarSearchItem,
  SidebarSession,
} from "./types";

const MODE_KEY = "WorkGround2.sidebar.mode";
const EXPANDED_KEY = "WorkGround2.sidebar.expandedGroups";
export const SIDEBAR_SESSION_CACHE_LIMIT = 2_000;

function readMode(): SidebarMode {
  try {
    const value = localStorage.getItem(MODE_KEY);
    if (value === "search" || value === "projects" || value === "rooms" || value === "assistants") return value;
  } catch { /* storage unavailable */ }
  return "projects";
}

function readExpanded(): string[] {
  try {
    const value = JSON.parse(localStorage.getItem(EXPANDED_KEY) || "[]");
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
  } catch {
    return [];
  }
}

function save(key: string, value: unknown): void {
  try { localStorage.setItem(key, typeof value === "string" ? value : JSON.stringify(value)); } catch { /* storage unavailable */ }
}

export function emptySidebarPage<T>(): SidebarPageState<T> {
  return { items: [], snapshot: "", status: "idle", requestSeq: 0 };
}

// sidebarSessionKey is the single stable identity for a physical session across
// sidebar projections. The same session file can be projected with different
// runtime/index IDs, so identity must not key on `id` alone: prefer the
// normalized sessionPath (Windows case and / vs \ are equivalent), then topicId,
// then sessionId, then id. Two genuinely different sessions may share a title,
// so title is deliberately never part of the key.
export function sidebarSessionKey(session: SidebarSession): string {
  const path = comparablePath(session.sessionPath).trim();
  if (path) return `path:${path}`;
  const topicId = (session.topicId || "").trim();
  if (topicId) return `topic:${topicId}`;
  const sessionId = (session.sessionId || "").trim();
  if (sessionId) return `session:${sessionId}`;
  return `id:${session.id}`;
}

function preferSidebarSession(current: SidebarSession, incoming: SidebarSession): SidebarSession {
  const currentRevision = current.revision ?? 0;
  const incomingRevision = incoming.revision ?? 0;
  if (incomingRevision !== currentRevision) return incomingRevision > currentRevision ? incoming : current;
  const currentActivity = current.lastActivityAt ?? 0;
  const incomingActivity = incoming.lastActivityAt ?? 0;
  if (incomingActivity !== currentActivity) return incomingActivity > currentActivity ? incoming : current;
  const richness = (item: SidebarSession): number =>
    Number(Boolean(item.running)) + Number(Boolean(item.open)) + Number(Boolean(item.topicId)) +
    Number(Boolean(item.sessionId)) + Number(Boolean(item.sessionPath)) + Number(Boolean(item.status));
  const currentRichness = richness(current);
  const incomingRichness = richness(incoming);
  if (incomingRichness !== currentRichness) return incomingRichness > currentRichness ? incoming : current;
  return incoming;
}

export function mergeSidebarSessions(current: SidebarSession[], incoming: SidebarSession[]): SidebarSession[] {
  const byKey = new Map<string, SidebarSession>();
  for (const item of current) byKey.set(sidebarSessionKey(item), item);
  for (const item of incoming) {
    const key = sidebarSessionKey(item);
    const prior = byKey.get(key);
    byKey.set(key, prior ? preferSidebarSession(prior, item) : item);
  }
  return [...byKey.values()];
}

export function sidebarSearchItemKey(item: SidebarSearchItem): string {
  if (item.kind === "session" && item.session) return `session:${sidebarSessionKey(item.session)}`;
  return `${item.kind}:${item.id}`;
}

function preferSearchItem(current: SidebarSearchItem, incoming: SidebarSearchItem): SidebarSearchItem {
  const currentRevision = current.session?.revision ?? 0;
  const incomingRevision = incoming.session?.revision ?? 0;
  if (incomingRevision !== currentRevision) return incomingRevision > currentRevision ? incoming : current;
  const currentActivity = current.lastActivityAt ?? current.session?.lastActivityAt ?? 0;
  const incomingActivity = incoming.lastActivityAt ?? incoming.session?.lastActivityAt ?? 0;
  if (incomingActivity !== currentActivity) return incomingActivity > currentActivity ? incoming : current;
  return incoming;
}

export function mergeSearchItems(current: SidebarSearchItem[], incoming: SidebarSearchItem[]): SidebarSearchItem[] {
  const byKey = new Map<string, SidebarSearchItem>();
  for (const item of current) byKey.set(sidebarSearchItemKey(item), item);
  for (const item of incoming) {
    const key = sidebarSearchItemKey(item);
    const prior = byKey.get(key);
    byKey.set(key, prior ? preferSearchItem(prior, item) : item);
  }
  return [...byKey.values()];
}

function pageGroupID(key: string): string {
  const separator = key.indexOf(":");
  return separator < 0 ? key : key.slice(separator + 1);
}

export interface SidebarCacheProtection {
  workspaceRoot?: string;
  topicId?: string;
  sessionPath?: string;
  groupIds?: string[];
}

function comparablePath(value?: string): string { return (value || "").replace(/\\/g, "/").toLowerCase(); }

function pageIsProtected(key: string, page: SidebarPageState<SidebarSession>, protection: SidebarCacheProtection): boolean {
  const workspaceRoot = comparablePath(protection.workspaceRoot);
  const sessionPath = comparablePath(protection.sessionPath);
  return Boolean(protection.groupIds?.includes(pageGroupID(key))) || page.items.some((session) =>
    Boolean(workspaceRoot && comparablePath(session.workspaceRoot) === workspaceRoot) ||
    Boolean(protection.topicId && session.topicId === protection.topicId) ||
    Boolean(sessionPath && comparablePath(session.sessionPath) === sessionPath));
}

export function pruneSidebarPages(
  pages: Record<string, SidebarPageState<SidebarSession>>,
  touchedAt: Record<string, number>,
  expandedGroups: Set<string>,
  limit = SIDEBAR_SESSION_CACHE_LIMIT,
  protection: SidebarCacheProtection = {},
): { pages: Record<string, SidebarPageState<SidebarSession>>; touchedAt: Record<string, number> } {
  const nextPages = { ...pages };
  const nextTouchedAt = { ...touchedAt };
  let sessionCount = Object.values(nextPages).reduce((total, page) => total + page.items.length, 0);
  const reclaimable = Object.keys(nextPages)
    .filter((key) => !expandedGroups.has(pageGroupID(key)) && nextPages[key].status !== "loading" && !pageIsProtected(key, nextPages[key], protection))
    .sort((left, right) => (nextTouchedAt[left] ?? 0) - (nextTouchedAt[right] ?? 0));

  for (const key of reclaimable) {
    if (sessionCount <= limit) break;
    sessionCount -= nextPages[key].items.length;
    delete nextPages[key];
    delete nextTouchedAt[key];
  }
  return { pages: nextPages, touchedAt: nextTouchedAt };
}

type GroupState = { items: SidebarGroup[]; status: "idle" | "loading" | "ready" | "error"; error?: string; requestSeq: number };

function resolveProtectedGroups(protection: SidebarCacheProtection, groupsByMode: Record<string, GroupState>): SidebarCacheProtection {
  const workspaceRoot = comparablePath(protection.workspaceRoot);
  if (!workspaceRoot) return { ...protection, groupIds: [] };
  const groupIds = new Set<string>();
  for (const state of Object.values(groupsByMode)) {
    for (const group of state.items) {
      if (comparablePath(group.root) === workspaceRoot) groupIds.add(group.id);
    }
  }
  return { ...protection, groupIds: [...groupIds] };
}

interface SidebarState {
  activeMode: SidebarMode;
  expandedGroups: Set<string>;
  groupsByMode: Record<string, GroupState>;
  pages: Record<string, SidebarPageState<SidebarSession>>;
  pageTouchedAt: Record<string, number>;
  cacheProtection: SidebarCacheProtection;
  searchQuery: string;
  searchFilter: SidebarSearchFilter;
  searchPage: SidebarPageState<SidebarSearchItem>;
  issues: SidebarIssue[];
  issuesStatus: "idle" | "loading" | "ready" | "error";
  issuesRequestSeq: number;
  issuesScope: string;
  issuesDataScope: string;
  beginIssues: (scope: string) => number;
  receiveIssues: (seq: number, scope: string, issues: SidebarIssue[]) => void;
  failIssues: (seq: number) => void;
  setMode: (mode: SidebarMode) => void;
  setSearchQuery: (query: string) => void;
  setSearchFilter: (filter: SidebarSearchFilter) => void;
  toggleGroup: (groupID: string) => void;
  expandGroup: (groupID: string) => void;
  setCacheProtection: (protection: SidebarCacheProtection) => void;
  beginGroups: (mode: string) => number;
  receiveGroups: (mode: string, seq: number, items: SidebarGroup[]) => void;
  failGroups: (mode: string, seq: number, error: string) => void;
  beginPage: (key: string, reset: boolean, retainItems?: boolean) => { seq: number; cursor?: string } | null;
  receivePage: (key: string, seq: number, page: SidebarPage<SidebarSession>, reset: boolean) => void;
  failPage: (key: string, seq: number, error: string) => void;
  beginSearch: (reset: boolean, retainItems?: boolean) => { seq: number; cursor?: string } | null;
  receiveSearch: (seq: number, page: SidebarPage<SidebarSearchItem>, reset: boolean) => void;
  failSearch: (seq: number, error: string) => void;
}

const emptyGroups = (): GroupState => ({ items: [], status: "idle", requestSeq: 0 });

export const useSidebarStore = create<SidebarState>((set, get) => ({
  activeMode: readMode(),
  expandedGroups: new Set(readExpanded()),
  groupsByMode: {},
  pages: {},
  pageTouchedAt: {},
  cacheProtection: {},
  searchQuery: "",
  searchFilter: "all",
  searchPage: emptySidebarPage(),
  issues: [],
  issuesStatus: "idle",
  issuesRequestSeq: 0,
  issuesScope: "",
  issuesDataScope: "",
  beginIssues: (scope) => {
    const seq = (get().issuesRequestSeq ?? 0) + 1;
    set((state) => ({
      issuesStatus: "loading",
      issuesRequestSeq: seq,
      issuesScope: scope,
      // Switching to a different view must not show the previous view's issues.
      issues: state.issuesDataScope === scope ? state.issues : [],
    }));
    return seq;
  },
  receiveIssues: (seq, scope, issues) => set((state) => (state.issuesRequestSeq !== seq || state.issuesScope !== scope) ? state : ({
    issues,
    issuesStatus: "ready",
    issuesDataScope: scope,
  })),
  failIssues: (seq) => set((state) => state.issuesRequestSeq !== seq ? state : ({ issuesStatus: "error" })),
  setMode: (activeMode) => {
    save(MODE_KEY, activeMode);
    set({ activeMode });
  },
  setSearchQuery: (searchQuery) => set((state) => state.searchQuery === searchQuery ? state : ({
    searchQuery,
    searchPage: { ...emptySidebarPage<SidebarSearchItem>(), requestSeq: state.searchPage.requestSeq + 1 },
  })),
  setSearchFilter: (searchFilter) => set((state) => state.searchFilter === searchFilter ? state : ({
    searchFilter,
    searchPage: { ...emptySidebarPage<SidebarSearchItem>(), requestSeq: state.searchPage.requestSeq + 1 },
  })),
  toggleGroup: (groupID) => set((state) => {
    const expandedGroups = new Set(state.expandedGroups);
    const expanding = !expandedGroups.has(groupID);
    if (expanding) expandedGroups.add(groupID); else expandedGroups.delete(groupID);
    save(EXPANDED_KEY, [...expandedGroups]);
    const pageTouchedAt = { ...state.pageTouchedAt };
    if (expanding) {
      for (const key of Object.keys(state.pages)) {
        if (pageGroupID(key) === groupID) pageTouchedAt[key] = Date.now();
      }
      return { expandedGroups, pageTouchedAt };
    }
    return { expandedGroups, ...pruneSidebarPages(state.pages, pageTouchedAt, expandedGroups, SIDEBAR_SESSION_CACHE_LIMIT, state.cacheProtection) };
  }),
  expandGroup: (groupID) => set((state) => {
    if (state.expandedGroups.has(groupID)) {
      const pageTouchedAt = { ...state.pageTouchedAt };
      for (const key of Object.keys(state.pages)) {
        if (pageGroupID(key) === groupID) pageTouchedAt[key] = Date.now();
      }
      return { pageTouchedAt };
    }
    const expandedGroups = new Set(state.expandedGroups).add(groupID);
    save(EXPANDED_KEY, [...expandedGroups]);
    const pageTouchedAt = { ...state.pageTouchedAt };
    for (const key of Object.keys(state.pages)) {
      if (pageGroupID(key) === groupID) pageTouchedAt[key] = Date.now();
    }
    return { expandedGroups, pageTouchedAt };
  }),
  setCacheProtection: (cacheProtection) => set((state) => ({ cacheProtection: resolveProtectedGroups(cacheProtection, state.groupsByMode) })),
  beginGroups: (mode) => {
    const prior = get().groupsByMode[mode] ?? emptyGroups();
    const seq = prior.requestSeq + 1;
    set((state) => ({ groupsByMode: { ...state.groupsByMode, [mode]: { ...prior, status: "loading", error: undefined, requestSeq: seq } } }));
    return seq;
  },
  receiveGroups: (mode, seq, items) => set((state) => {
    const prior = state.groupsByMode[mode] ?? emptyGroups();
    if (prior.requestSeq !== seq) return state;
    const groupsByMode = { ...state.groupsByMode, [mode]: { items, status: "ready" as const, requestSeq: seq } };
    return { groupsByMode, cacheProtection: resolveProtectedGroups(state.cacheProtection, groupsByMode) };
  }),
  failGroups: (mode, seq, error) => set((state) => {
    const prior = state.groupsByMode[mode] ?? emptyGroups();
    if (prior.requestSeq !== seq) return state;
    return { groupsByMode: { ...state.groupsByMode, [mode]: { ...prior, status: "error", error } } };
  }),
  beginPage: (key, reset, retainItems = false) => {
    const prior = get().pages[key] ?? emptySidebarPage<SidebarSession>();
    if (prior.status === "loading" && !reset) return null;
    const seq = prior.requestSeq + 1;
    const cursor = reset ? undefined : prior.nextCursor;
    if (!reset && !cursor) return null;
    set((state) => ({
      pages: { ...state.pages, [key]: { ...(reset && !retainItems ? emptySidebarPage<SidebarSession>() : prior), status: "loading", error: undefined, requestSeq: seq } },
      pageTouchedAt: { ...state.pageTouchedAt, [key]: Date.now() },
    }));
    return { seq, cursor };
  },
  receivePage: (key, seq, page, reset) => set((state) => {
    const prior = state.pages[key] ?? emptySidebarPage<SidebarSession>();
    if (prior.requestSeq !== seq) return state;
    const receivedPage: SidebarPageState<SidebarSession> = {
      items: mergeSidebarSessions(reset ? [] : prior.items, page.items),
      nextCursor: page.nextCursor || undefined,
      total: page.total,
      snapshot: page.snapshot,
      status: "ready",
      requestSeq: seq,
    };
    const pages = { ...state.pages, [key]: receivedPage };
    const pageTouchedAt = { ...state.pageTouchedAt, [key]: Date.now() };
    return pruneSidebarPages(pages, pageTouchedAt, state.expandedGroups, SIDEBAR_SESSION_CACHE_LIMIT, state.cacheProtection);
  }),
  failPage: (key, seq, error) => set((state) => {
    const prior = state.pages[key] ?? emptySidebarPage<SidebarSession>();
    if (prior.requestSeq !== seq) return state;
    return { pages: { ...state.pages, [key]: { ...prior, status: "error", error } } };
  }),
  beginSearch: (reset, retainItems = false) => {
    const prior = get().searchPage;
    if (prior.status === "loading" && !reset) return null;
    const seq = prior.requestSeq + 1;
    const cursor = reset ? undefined : prior.nextCursor;
    if (!reset && !cursor) return null;
    set({ searchPage: { ...(reset && !retainItems ? emptySidebarPage<SidebarSearchItem>() : prior), status: "loading", error: undefined, requestSeq: seq } });
    return { seq, cursor };
  },
  receiveSearch: (seq, page, reset) => set((state) => {
    if (state.searchPage.requestSeq !== seq) return state;
    return { searchPage: {
      items: mergeSearchItems(reset ? [] : state.searchPage.items, page.items),
      nextCursor: page.nextCursor || undefined,
      total: page.total,
      snapshot: page.snapshot,
      status: "ready",
      requestSeq: seq,
    } };
  }),
  failSearch: (seq, error) => set((state) => state.searchPage.requestSeq === seq
    ? { searchPage: { ...state.searchPage, status: "error", error } }
    : state),
}));
