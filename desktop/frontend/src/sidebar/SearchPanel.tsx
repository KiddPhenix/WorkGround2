import { Search, X } from "lucide-react";
import { useEffect, useMemo, useRef } from "react";
import { loadSidebarSearch, refreshSidebarSearch } from "./sidebarData";
import { SessionList, type SidebarListRow } from "./SessionList";
import { useSidebarStore } from "./sidebarStore";
import type { SidebarGroup, SidebarOpenTarget, SidebarSearchFilter, SidebarSession } from "./types";
import type { ProjectTopicStatus } from "../lib/types";

interface SearchPanelProps {
  now: number;
  activeTopicId?: string;
  activeSessionPath?: string;
  unreadBySession: Map<string, number>;
  onOpenSession: (target: SidebarOpenTarget, session: SidebarSession, group: SidebarGroup) => void;
  onOpenSessionMenu: (session: SidebarSession, element: HTMLElement) => void;
  refreshSignal?: number;
}

function pathKey(path?: string): string { return (path || "").replace(/\\/g, "/").toLowerCase(); }

export function SearchPanel({ now, activeTopicId, activeSessionPath, unreadBySession, onOpenSession, onOpenSessionMenu, refreshSignal }: SearchPanelProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const priorRefreshRef = useRef(refreshSignal);
  const query = useSidebarStore((state) => state.searchQuery);
  const filter = useSidebarStore((state) => state.searchFilter);
  const page = useSidebarStore((state) => state.searchPage);
  const setQuery = useSidebarStore((state) => state.setSearchQuery);
  const setFilter = useSidebarStore((state) => state.setSearchFilter);

  useEffect(() => { inputRef.current?.focus(); }, []);
  useEffect(() => {
    const timer = window.setTimeout(() => { void loadSidebarSearch(query.trim(), filter, true); }, 250);
    return () => window.clearTimeout(timer);
  }, [filter, query]);
  useEffect(() => {
    if (refreshSignal === undefined || priorRefreshRef.current === refreshSignal) return;
    priorRefreshRef.current = refreshSignal;
    const state = useSidebarStore.getState();
    void refreshSidebarSearch(state.searchQuery.trim(), state.searchFilter, state.searchPage.items.length);
  }, [refreshSignal]);

  const rows = useMemo<SidebarListRow[]>(() => {
    const grouped = new Map<string, { group: SidebarGroup; sessions: SidebarSession[]; project: boolean }>();
    for (const item of page.items) {
      if (!item.group) continue;
      const entry = grouped.get(item.group.id) ?? { group: item.group, sessions: [], project: false };
      if (item.kind === "project") entry.project = true;
      if (item.session) entry.sessions.push(item.session);
      grouped.set(item.group.id, entry);
    }
    const result: SidebarListRow[] = [];
    for (const { group, sessions, project } of grouped.values()) {
      result.push({
        key: `search-group:${group.id}`,
        kind: "group",
        label: group.label,
        color: group.color,
        count: group.sessionCount,
        expanded: true,
      });
      if (project && sessions.length === 0) {
        result.push({ key: `search-project:${group.id}`, kind: "project", label: "在项目中查看", color: group.color, onOpen: () => {
          useSidebarStore.getState().setMode("projects");
          useSidebarStore.getState().expandGroup(group.id);
        } });
      }
      for (const session of sessions) {
        const atPath = pathKey(session.sessionPath);
        result.push({
          key: `search-session:${session.id}`,
          kind: "session",
          session,
          unread: unreadBySession.get(session.id) ?? unreadBySession.get(atPath) ?? 0,
          active: Boolean((atPath && atPath === pathKey(activeSessionPath)) || (!session.sessionPath && session.topicId && session.topicId === activeTopicId)),
          onOpen: () => onOpenSession({
            scope: session.scope,
            workspaceRoot: session.workspaceRoot || "",
            topicId: session.topicId || "",
            sessionPath: session.sessionPath,
            runtimeHint: session.running || session.status ? { running: Boolean(session.running), status: session.status as ProjectTopicStatus | undefined } : undefined,
          }, session, group),
          onMenu: (element) => onOpenSessionMenu(session, element),
        });
      }
    }
    if (page.status === "loading" && page.items.length === 0) result.push({ key: "search-loading", kind: "empty", label: "正在搜索…" });
    else if (page.status === "error") result.push({ key: "search-error", kind: "error", message: page.error || "搜索失败", onRetry: () => void (page.items.length > 0 ? refreshSidebarSearch(query.trim(), filter, page.items.length) : loadSidebarSearch(query.trim(), filter, true)) });
    else if (page.status === "ready" && page.items.length === 0) result.push({ key: "search-empty", kind: "empty", label: query ? "没有找到匹配的项目或会话" : "暂无最近会话" });
    if (page.nextCursor) result.push({ key: "search-more", kind: "load", label: page.status === "loading" ? "加载中…" : "加载更多结果", disabled: page.status === "loading", onLoad: () => void loadSidebarSearch(query.trim(), filter, false) });
    return result;
  }, [activeSessionPath, activeTopicId, filter, onOpenSession, onOpenSessionMenu, page, query, unreadBySession]);

  const filters: Array<{ id: SidebarSearchFilter; label: string }> = [{ id: "all", label: "全部" }, { id: "projects", label: "项目" }, { id: "sessions", label: "会话" }];
  return (
    <section className="session-sidebar__panel-view" aria-label="搜索">
      <h2>搜索</h2>
      <label className="session-sidebar__search-box">
        <Search size={19} aria-hidden="true" />
        <input ref={inputRef} value={query} onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => {
          if (event.key !== "ArrowDown") return;
          event.preventDefault();
          document.querySelector<HTMLElement>('#session-sidebar-search-results [data-sidebar-primary][tabindex="0"]')?.focus();
        }} placeholder="搜索项目或会话" aria-label="搜索项目或会话" />
        {query && <button type="button" aria-label="清除搜索" onClick={() => setQuery("")}><X size={15} aria-hidden="true" /></button>}
      </label>
      <div className="session-sidebar__filters" role="group" aria-label="搜索范围">
        {filters.map((item) => <button key={item.id} type="button" className={filter === item.id ? "is-active" : ""} aria-pressed={filter === item.id} onClick={() => setFilter(item.id)}>{item.label}</button>)}
      </div>
      <SessionList id="session-sidebar-search-results" rows={rows} now={now} className="session-sidebar__search-results" />
      {page.items.length > 0 && <div className="session-sidebar__count">已显示 {page.items.length}{typeof page.total === "number" ? ` / ${page.total}` : ""}</div>}
    </section>
  );
}
