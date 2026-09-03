import { ArrowUpRight, FolderPlus, MessageCircleQuestion, Plus } from "lucide-react";
import { useEffect, useMemo } from "react";
import type { ProjectTopicStatus } from "../lib/types";
import { loadSidebarGroups, loadSidebarPage, refreshSidebarPage } from "./sidebarData";
import { SessionList, type SidebarListRow } from "./SessionList";
import { emptySidebarPage, useSidebarStore } from "./sidebarStore";
import type { SidebarGroup, SidebarOpenTarget, SidebarQueryMode, SidebarSession } from "./types";

interface ProjectPanelProps {
  mode: SidebarQueryMode;
  now: number;
  refreshSignal?: number;
  activeTopicId?: string;
  activeSessionPath?: string;
  unreadBySession: Map<string, number>;
  onOpenSession: (target: SidebarOpenTarget, session: SidebarSession, group: SidebarGroup) => void;
  onOpenGroupMenu: (group: SidebarGroup, element: HTMLElement) => void;
  onOpenSessionMenu: (session: SidebarSession, element: HTMLElement) => void;
  onAddProject: () => void;
  onModeAction?: () => void;
  ownerDecisionEnabled?: boolean;
  onOpenOwnerDecision?: () => void;
}

function pathKey(path?: string): string { return (path || "").replace(/\\/g, "/").toLowerCase(); }

const MODE_TITLES: Record<SidebarQueryMode, string> = { projects: "项目", rooms: "ROOM", assistants: "助手" };

export function ProjectPanel(props: ProjectPanelProps) {
  const { mode, now, refreshSignal, activeTopicId, activeSessionPath, unreadBySession, onOpenSession, onOpenGroupMenu, onOpenSessionMenu, onAddProject, onModeAction, ownerDecisionEnabled, onOpenOwnerDecision } = props;
  const groupState = useSidebarStore((state) => state.groupsByMode[mode]);
  const pages = useSidebarStore((state) => state.pages);
  const expanded = useSidebarStore((state) => state.expandedGroups);
  const toggleGroup = useSidebarStore((state) => state.toggleGroup);

  useEffect(() => {
    let alive = true;
    void loadSidebarGroups(mode).then(() => {
      if (!alive) return;
      const state = useSidebarStore.getState();
      for (const group of state.groupsByMode[mode]?.items ?? []) {
        if (!state.expandedGroups.has(group.id)) continue;
        const page = state.pages[`${mode}:${group.id}`];
        if (page?.items.length) void refreshSidebarPage(mode, group.id, page.items.length);
        else if (!page || page.status !== "loading") void loadSidebarPage(mode, group.id, true);
      }
    });
    return () => { alive = false; };
  }, [mode, refreshSignal]);
  useEffect(() => {
    for (const group of groupState?.items ?? []) {
      if (!expanded.has(group.id)) continue;
      const page = pages[`${mode}:${group.id}`];
      if (!page || page.status === "idle") void loadSidebarPage(mode, group.id, true);
    }
  }, [expanded, groupState?.items, mode, pages]);

  const rows = useMemo<SidebarListRow[]>(() => {
    const result: SidebarListRow[] = [];
    for (const group of groupState?.items ?? []) {
      const open = expanded.has(group.id);
      const key = `${mode}:${group.id}`;
      const page = pages[key] ?? emptySidebarPage<SidebarSession>();
      const handleToggle = () => {
        toggleGroup(group.id);
        if (!open && page.status === "idle") void loadSidebarPage(mode, group.id, true);
      };
      result.push({
        key: `group:${mode}:${group.id}`,
        kind: "group",
        label: group.label,
        color: group.color,
        icon: group.icon,
        count: page.status === "ready" && typeof page.total === "number" ? page.total : group.sessionCount,
        expanded: open,
        onToggle: handleToggle,
        onMenu: mode === "projects" && group.kind !== "crew" ? (element) => onOpenGroupMenu(group, element) : undefined,
      });
      if (!open) continue;
      if (page.status === "idle") {
        result.push({ key: `load-first:${key}`, kind: "load", label: "加载会话", onLoad: () => void loadSidebarPage(mode, group.id, true) });
        continue;
      }
      for (const session of page.items) {
        const sessionPath = pathKey(session.sessionPath);
        result.push({
          key: `session:${mode}:${group.id}:${session.id}`,
          kind: "session",
          session,
          unread: unreadBySession.get(session.id) ?? unreadBySession.get(sessionPath) ?? 0,
          active: Boolean((sessionPath && sessionPath === pathKey(activeSessionPath)) || (!session.sessionPath && session.topicId && session.topicId === activeTopicId)),
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
      if (page.status === "loading" && page.items.length === 0) result.push({ key: `loading:${key}`, kind: "empty", label: "正在加载…" });
      if (page.status === "error") result.push({ key: `error:${key}`, kind: "error", message: page.error || "加载失败", onRetry: () => void (page.items.length > 0 ? refreshSidebarPage(mode, group.id, page.items.length) : loadSidebarPage(mode, group.id, true)) });
      if (page.nextCursor) result.push({ key: `more:${key}`, kind: "load", label: page.status === "loading" ? "加载中…" : `加载更多 · 已加载 ${page.items.length}${typeof page.total === "number" ? ` / ${page.total}` : ""}`, disabled: page.status === "loading", onLoad: () => void loadSidebarPage(mode, group.id, false) });
      else if (page.status === "ready" && page.items.length === 0) result.push({ key: `empty:${key}`, kind: "empty", label: mode === "projects" ? "这个项目还没有会话" : "暂无会话" });
    }
    if (groupState?.status === "loading" && !groupState.items.length) result.push({ key: `groups-loading:${mode}`, kind: "empty", label: "正在加载…" });
    if (groupState?.status === "error") result.push({ key: `groups-error:${mode}`, kind: "error", message: groupState.error || "加载失败", onRetry: () => void loadSidebarGroups(mode) });
    if (groupState?.status === "ready" && !groupState.items.length) result.push({ key: `groups-empty:${mode}`, kind: "empty", label: mode === "projects" ? "还没有项目" : "暂无会话" });
    return result;
  }, [activeSessionPath, activeTopicId, expanded, groupState, mode, onOpenGroupMenu, onOpenSession, onOpenSessionMenu, pages, toggleGroup, unreadBySession]);

  return (
    <section className="session-sidebar__panel-view" aria-label={MODE_TITLES[mode]}>
      <header className="session-sidebar__panel-header">
        <h2>{MODE_TITLES[mode]}</h2>
        <div className="session-sidebar__panel-actions">
          {mode === "assistants" && ownerDecisionEnabled && onOpenOwnerDecision && (
            <button type="button" aria-label="打开主人决策" title="主人决策" onClick={onOpenOwnerDecision}><MessageCircleQuestion size={18} aria-hidden="true" /></button>
          )}
          {mode === "projects" ? (
            <button type="button" aria-label="添加项目" title="添加项目" onClick={onAddProject}><FolderPlus size={18} aria-hidden="true" /></button>
          ) : onModeAction ? (
            <button type="button" aria-label={mode === "rooms" ? "新建或加入 ROOM" : "打开助手管理"} title={mode === "rooms" ? "新建或加入 ROOM" : "打开助手管理"} onClick={onModeAction}>
              {mode === "rooms" ? <Plus size={18} aria-hidden="true" /> : <ArrowUpRight size={18} aria-hidden="true" />}
            </button>
          ) : null}
        </div>
      </header>
      <SessionList rows={rows} now={now} />
    </section>
  );
}
