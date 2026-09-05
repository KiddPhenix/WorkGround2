import { ArrowLeft, ExternalLink, FolderOpen, History, Palette, Pencil, Pin, PinOff, Plus, Repeat2, Trash2, XCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { app, onUnreadState } from "../lib/bridge";
import type { ProjectTopicStatus, UnreadConversation } from "../lib/types";
import { ContextMenu, type ContextMenuItem, type ContextMenuPoint } from "../components/ContextMenu";
import { WorkspaceMatteIcon } from "../components/widget/WorkspaceMatteIcon";
import { WORKSPACE_MATTE_ICON_OPTIONS, workspaceMatteIconKey } from "../lib/projectIcons";
import { useToast } from "../lib/toast";
import { PrimaryRail } from "./PrimaryRail";
import { ProjectPanel } from "./ProjectPanel";
import { SearchPanel } from "./SearchPanel";
import { useRecentSessions, type RecentSessionRailItem } from "./recentSessions";
import { useSidebarStore } from "./sidebarStore";
import { useSidebarNow } from "./sidebarTime";
import type { SidebarGroup, SidebarMode, SidebarOpenTarget, SidebarSession } from "./types";

interface SidebarShellProps {
  panelOpen: boolean;
  refreshSignal?: number;
  activeTopicId?: string;
  activeSessionPath?: string;
  activeWorkspaceRoot?: string;
  onTogglePanel: () => void;
  onNewSession: () => void;
  onOpenSettings: () => void;
  onAddProject: () => void;
  onOpenRoom: () => void;
  onCreateAssistant: () => void;
  ownerDecisionEnabled: boolean;
  onOpenOwnerDecision: () => void;
  onCreateProjectSession: (scope: "global" | "project", workspaceRoot: string) => Promise<void> | void;
  onOpenProjectHistory: (scope: "global" | "project", workspaceRoot: string) => void;
  onOpenSession: (target: SidebarOpenTarget, session: SidebarSession) => void;
  onOpenCrewSession: (sessionPath: string) => void;
  onChanged?: () => void;
}

type OpenMenu =
  | { kind: "group"; group: SidebarGroup; point: ContextMenuPoint; view: "actions" | "appearance" }
  | { kind: "session"; session: SidebarSession; point: ContextMenuPoint; confirmTrash?: boolean }
  | { kind: "recent"; item: RecentSessionRailItem; point: ContextMenuPoint };

function menuPoint(element: HTMLElement): ContextMenuPoint {
  const rect = element.getBoundingClientRect();
  return { left: rect.right + 4, top: rect.top };
}

function normalizedPath(path?: string): string { return (path || "").replace(/\\/g, "/").toLowerCase(); }

export function SidebarShell(props: SidebarShellProps) {
  const { panelOpen, refreshSignal, activeTopicId, activeSessionPath, activeWorkspaceRoot, onTogglePanel, onNewSession, onOpenSettings, onAddProject, onOpenRoom, onCreateAssistant, ownerDecisionEnabled, onOpenOwnerDecision, onCreateProjectSession, onOpenProjectHistory, onOpenSession, onOpenCrewSession, onChanged } = props;
  const activeMode = useSidebarStore((state) => state.activeMode);
  const setMode = useSidebarStore((state) => state.setMode);
  const [menu, setMenu] = useState<OpenMenu | null>(null);
  const [unread, setUnread] = useState<UnreadConversation[]>([]);
  const rootRef = useRef<HTMLElement>(null);
  const menuTriggerRef = useRef<HTMLElement | null>(null);
  const recentRequestSeq = useRef(0);
  const recentRequests = useRef(new Map<string, string>());
  const now = useSidebarNow();
  const { showToast } = useToast();

  useEffect(() => {
    useSidebarStore.getState().setCacheProtection({ workspaceRoot: activeWorkspaceRoot, topicId: activeTopicId, sessionPath: activeSessionPath });
  }, [activeSessionPath, activeTopicId, activeWorkspaceRoot]);

  useEffect(() => {
    let alive = true;
    const apply = (items: UnreadConversation[]) => { if (alive) setUnread(items); };
    const unsubscribe = onUnreadState((state) => apply(state.summary.conversations || []));
    void app.UnreadState().then((state) => apply(state.summary.conversations || [])).catch(() => undefined);
    return () => { alive = false; unsubscribe(); };
  }, []);

  const unreadBySession = useMemo(() => {
    const result = new Map<string, number>();
    for (const item of unread) {
      const id = (item.sessionId || "").trim();
      if (!id || item.unreadCount <= 0) continue;
      result.set(id, item.unreadCount);
      if (id.toLowerCase().startsWith("path:")) result.set(normalizedPath(id.slice(5)), item.unreadCount);
    }
    return result;
  }, [unread]);

  const recentSessions = useRecentSessions({ refreshSignal, activeSessionPath, activeTopicId, unreadBySession });

  const selectMode = useCallback((mode: SidebarMode) => {
    setMode(mode);
    if (!panelOpen) onTogglePanel();
  }, [onTogglePanel, panelOpen, setMode]);

  useEffect(() => {
    if (!panelOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || window.innerWidth >= 920) return;
      const state = useSidebarStore.getState();
      if (state.activeMode === "search" && state.searchQuery) state.setSearchQuery("");
      else onTogglePanel();
    };
    const onPointerDown = (event: PointerEvent) => {
      if (window.innerWidth >= 920 || !(event.target instanceof Node) || rootRef.current?.contains(event.target)) return;
      onTogglePanel();
    };
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("pointerdown", onPointerDown, true);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("pointerdown", onPointerDown, true);
    };
  }, [onTogglePanel, panelOpen]);

  const markSessionRead = useCallback((session: SidebarSession) => {
    const matches = unread.filter((item) => {
      const id = (item.sessionId || "").trim();
      return id === session.id || (id.toLowerCase().startsWith("path:") && normalizedPath(id.slice(5)) === normalizedPath(session.sessionPath));
    });
    for (const item of matches) {
      void app.MarkUnreadRead({ conversationKey: item.key, upToSequence: item.latestSequence }).catch(() => undefined);
    }
  }, [unread]);

  const openSession = useCallback((target: SidebarOpenTarget, session: SidebarSession, group: SidebarGroup) => {
    if (group.kind === "crew") {
      if (!session.sessionPath) return;
      onOpenCrewSession(session.sessionPath);
    } else {
      onOpenSession(target, session);
    }
    markSessionRead(session);
  }, [markSessionRead, onOpenCrewSession, onOpenSession]);

  // openRecentSession routes a rail icon through the exact same session-opening
  // path (crew vs normal) and unread read-marking used by the panel rows, so a
  // rail click is indistinguishable from opening the session in the list. It
  // never calls ApplyDesktopIconAction("open").
  const openRecentSession = useCallback((item: RecentSessionRailItem) => {
    const { session, groupKind } = item;
    const target: SidebarOpenTarget = {
      scope: session.scope,
      workspaceRoot: session.workspaceRoot || "",
      topicId: session.topicId || "",
      sessionPath: session.sessionPath,
      runtimeHint: session.running || session.status ? { running: Boolean(session.running), status: session.status as ProjectTopicStatus | undefined } : undefined,
    };
    if (groupKind === "crew") {
      if (!session.sessionPath) return;
      onOpenCrewSession(session.sessionPath);
    } else {
      onOpenSession(target, session);
    }
    markSessionRead(session);
  }, [markSessionRead, onOpenCrewSession, onOpenSession]);

  const closeMenu = useCallback(() => {
    setMenu(null);
    const trigger = menuTriggerRef.current;
    window.requestAnimationFrame(() => trigger?.focus());
  }, []);

  const changeAndClose = useCallback(async (work: () => Promise<void>) => {
    closeMenu();
    try {
      await work();
      onChanged?.();
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error || "操作失败"), "error");
    }
  }, [closeMenu, onChanged, showToast]);

  const menuItems = useMemo<ContextMenuItem[]>(() => {
    if (!menu) return [];
    if (menu.kind === "group") {
      const { group } = menu;
      const isProject = group.kind === "project" && Boolean(group.root);
      const canCustomize = isProject || group.kind === "global";
      const root = isProject ? group.root || "" : "";
      const scope = isProject ? "project" : "global";
      const currentIcon = workspaceMatteIconKey(group.icon);
      const currentIconLabel = WORKSPACE_MATTE_ICON_OPTIONS.find((option) => option.key === currentIcon)?.label ?? "文件夹";
      if (menu.view === "appearance") {
        return [
          { key: "back", icon: <ArrowLeft size={15} />, label: "返回项目菜单", onSelect: () => setMenu({ ...menu, view: "actions" }) },
          { type: "separator", key: "appearance-separator" },
          { key: "icon-heading", icon: <WorkspaceMatteIcon icon={currentIcon} className="session-sidebar__group-matte" />, label: `当前图标 · ${currentIconLabel}`, variant: "section", disabled: true, onSelect: () => undefined },
          { type: "grid", key: "icon-grid", ariaLabel: "选择项目图标", items: WORKSPACE_MATTE_ICON_OPTIONS.map((option) => ({
            key: option.key,
            icon: <WorkspaceMatteIcon icon={option.key} />,
            label: option.label,
            checked: option.key === currentIcon,
            onSelect: () => void changeAndClose(() => app.SetProjectIcon(root, option.key)),
          })) },
        ];
      }
      return [
        { key: "new-session", icon: <Plus size={15} />, label: "新建会话", onSelect: () => void changeAndClose(() => Promise.resolve(onCreateProjectSession(scope, root))) },
        { key: "history", icon: <History size={15} />, label: "查看历史", onSelect: () => { closeMenu(); onOpenProjectHistory(scope, root); } },
        { key: "rename", icon: <Pencil size={15} />, label: isProject ? "重命名项目" : "重命名分组", disabled: !canCustomize, onSelect: () => {
          const next = window.prompt("项目名称", group.label);
          if (next === null || next.trim() === group.label) { closeMenu(); return; }
          void changeAndClose(() => app.RenameProject(root, next.trim()));
        } },
        { key: "pin", icon: group.pinned ? <PinOff size={15} /> : <Pin size={15} />, label: group.pinned ? "取消置顶" : "置顶项目", disabled: !isProject, onSelect: () => void changeAndClose(() => app.SetProjectPinned(root, !group.pinned)) },
        { key: "appearance", icon: <Palette size={15} />, label: `修改图标 · ${currentIconLabel}`, disabled: !canCustomize, onSelect: () => setMenu({ ...menu, view: "appearance" }) },
        { type: "separator", key: "path-separator" },
        { key: "reveal", icon: <FolderOpen size={15} />, label: "在文件管理器中显示", disabled: !isProject, onSelect: () => void changeAndClose(() => app.RevealPath(root)) },
        { key: "remove", icon: <XCircle size={15} />, label: "移除项目", danger: true, disabled: !isProject, onSelect: () => {
          if (!window.confirm(`从侧边栏移除“${group.label}”？项目文件不会被删除。`)) return;
          void changeAndClose(() => app.RemoveWorkspace(root));
        } },
      ];
    }
    if (menu.kind === "recent") {
      const { item } = menu;
      const runRecentAction = (action: string, values: string[] = [], kind: "session" | "routine" = "session") => changeAndClose(async () => {
        const intent = JSON.stringify([kind, item.item.id, item.item.revision, action, values]);
        let requestId = recentRequests.current.get(intent);
        if (!requestId) {
          recentRequestSeq.current += 1;
          requestId = `sidebar-recent-${Date.now()}-${recentRequestSeq.current}`;
          recentRequests.current.set(intent, requestId);
        }
        if (kind === "routine") {
          const result = await app.CreateDailyRoutine({ tabId: item.item.sourceId, sessionRef: item.item.sessionRef, requestId });
          if (result.status === "accepted" || result.status === "already_applied") recentRequests.current.delete(intent);
          else {
            if (result.status === "invalid") recentRequests.current.delete(intent);
            throw new Error(result.error || "固化流程失败");
          }
          return;
        }
        const result = await app.ApplyDesktopIconAction({ itemId: item.item.id, revision: item.item.revision, requestId, action, values });
        if (result.status === "accepted" || result.status === "already_applied") recentRequests.current.delete(intent);
        else {
          if (result.status === "stale" || result.status === "invalid") recentRequests.current.delete(intent);
          throw new Error(result.error || "操作失败");
        }
      });
      return [
        { key: "open", icon: <ExternalLink size={15} />, label: "打开", onSelect: () => { closeMenu(); openRecentSession(item); } },
        { key: "rename", icon: <Pencil size={15} />, label: "重命名", disabled: !item.item.sessionRef?.sessionPath, onSelect: () => {
          const next = window.prompt("会话名称", item.item.title);
          if (next === null || !next.trim() || next.trim() === item.item.title) { closeMenu(); return; }
          void runRecentAction("rename", [next.trim()]);
        } },
        { key: "routine", icon: <Repeat2 size={15} />, label: "固化流程", disabled: !item.item.sessionRef?.sessionPath, onSelect: () => void runRecentAction("routine", [], "routine") },
        { key: "appearance", icon: <Palette size={15} />, label: "换个样子", onSelect: () => void runRecentAction("randomize_icon") },
        { key: "remove", icon: <XCircle size={15} />, label: "移除", danger: true, disabled: !item.item.retained, onSelect: () => void runRecentAction("remove") },
      ];
    }
    const { session } = menu;
    return [
      { key: "rename", icon: <Pencil size={15} />, label: "重命名", disabled: !session.topicId && !session.sessionPath, onSelect: () => {
        const next = window.prompt("会话名称", session.title);
        if (next === null || !next.trim() || next.trim() === session.title) { closeMenu(); return; }
        void changeAndClose(() => session.topicId ? app.RenameTopic(session.topicId, next.trim()) : app.RenameSession(session.sessionPath || "", next.trim()));
      } },
      { key: "pin", icon: session.pinned ? <PinOff size={15} /> : <Pin size={15} />, label: session.pinned ? "取消置顶" : "置顶会话", disabled: !session.topicId && !session.sessionPath, onSelect: () => void changeAndClose(() => session.topicId ? app.SetTopicPinned(session.topicId, !session.pinned) : app.SetSessionPinned(session.sessionPath || "", !session.pinned)) },
      { type: "separator", key: "sep" },
      { key: "trash", icon: <Trash2 size={15} />, label: menu.confirmTrash ? "确认删除" : "移到废纸篓", danger: true, disabled: !session.topicId && !session.sessionPath, onSelect: () => {
        if (!menu.confirmTrash) { setMenu({ ...menu, confirmTrash: true }); return; }
        void changeAndClose(() => session.topicId ? app.TrashTopic(session.topicId) : app.DeleteSession(session.sessionPath || ""));
      } },
    ];
  }, [changeAndClose, closeMenu, menu, onCreateProjectSession, onOpenProjectHistory, openRecentSession]);

  const openGroupMenu = useCallback((group: SidebarGroup, element: HTMLElement) => {
    menuTriggerRef.current = element;
    setMenu({ kind: "group", group, point: menuPoint(element), view: "actions" });
  }, []);
  const openSessionMenu = useCallback((session: SidebarSession, element: HTMLElement) => {
    menuTriggerRef.current = element;
    setMenu({ kind: "session", session, point: menuPoint(element) });
  }, []);
  const openRecentSessionMenu = useCallback((item: RecentSessionRailItem, element: HTMLElement) => {
    menuTriggerRef.current = element;
    setMenu({ kind: "recent", item, point: menuPoint(element) });
  }, []);

  return (
    <aside ref={rootRef} className={`session-sidebar${panelOpen ? " session-sidebar--open" : " session-sidebar--collapsed"}`} aria-label="Workspace sidebar">
      <PrimaryRail panelOpen={panelOpen} activeMode={activeMode} onTogglePanel={onTogglePanel} onMode={selectMode} onNewSession={onNewSession} onOpenSettings={onOpenSettings} recentSessions={recentSessions} onOpenRecentSession={openRecentSession} onOpenRecentSessionMenu={openRecentSessionMenu} />
      {panelOpen && (
        <div id="session-sidebar-panel" className="session-sidebar__panel">
          {activeMode === "search" ? (
            <SearchPanel now={now} refreshSignal={refreshSignal} activeTopicId={activeTopicId} activeSessionPath={activeSessionPath} unreadBySession={unreadBySession} onOpenSession={openSession} onOpenSessionMenu={openSessionMenu} />
          ) : (
            <ProjectPanel mode={activeMode} now={now} refreshSignal={refreshSignal} activeTopicId={activeTopicId} activeSessionPath={activeSessionPath} unreadBySession={unreadBySession} onOpenSession={openSession} onOpenGroupMenu={openGroupMenu} onOpenSessionMenu={openSessionMenu} onAddProject={onAddProject} onModeAction={activeMode === "rooms" ? onOpenRoom : activeMode === "assistants" ? onCreateAssistant : undefined} ownerDecisionEnabled={ownerDecisionEnabled} onOpenOwnerDecision={onOpenOwnerDecision} />
          )}
        </div>
      )}
      <ContextMenu open={Boolean(menu)} point={menu?.point ?? null} items={menuItems} onClose={closeMenu} ariaLabel="侧边栏菜单" />
    </aside>
  );
}
