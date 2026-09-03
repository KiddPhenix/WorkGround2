import { Bot, ChevronLeft, ChevronRight, Folder, Search, Settings, SquarePen, Users } from "lucide-react";
import type { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";
import logoSymbol from "../assets/logo-symbol.svg";
import type { SidebarMode } from "./types";

interface PrimaryRailProps {
  panelOpen: boolean;
  activeMode: SidebarMode;
  onTogglePanel: () => void;
  onMode: (mode: SidebarMode) => void;
  onNewSession: () => void;
  onOpenSettings: () => void;
}

function runFramelessPointerAction(event: ReactPointerEvent<HTMLElement>, action: () => void) {
  event.stopPropagation();
  if (event.button === 0) action();
}

function stopFramelessPointerDown(event: ReactPointerEvent<HTMLElement>) { event.stopPropagation(); }
function stopFramelessMouseDown(event: ReactMouseEvent<HTMLElement>) { event.stopPropagation(); }
function runKeyboardClick(event: ReactMouseEvent<HTMLElement>, action: () => void) {
  if (event.detail === 0) action();
}

export function PrimaryRail({ panelOpen, activeMode, onTogglePanel, onMode, onNewSession, onOpenSettings }: PrimaryRailProps) {
  const modes: Array<{ mode: SidebarMode; label: string; icon: typeof Search }> = [
    { mode: "search", label: "搜索", icon: Search },
    { mode: "projects", label: "项目", icon: Folder },
    { mode: "rooms", label: "ROOM", icon: Users },
    { mode: "assistants", label: "助手", icon: Bot },
  ];
  return (
    <nav className="session-sidebar__rail" aria-label="主导航">
      <button
        type="button"
        className="session-sidebar__brand-toggle"
        aria-label={panelOpen ? "收起侧边栏" : "展开侧边栏"}
        aria-expanded={panelOpen}
        aria-controls="session-sidebar-panel"
        title={panelOpen ? "收起侧边栏" : "展开侧边栏"}
        onClick={onTogglePanel}
      >
        <img src={logoSymbol} alt="W2" draggable={false} />
        {panelOpen ? <ChevronLeft size={13} aria-hidden="true" /> : <ChevronRight size={13} aria-hidden="true" />}
      </button>

      <div className="session-sidebar__rail-actions">
        <RailButton {...modes[0]} selected={activeMode === "search"} onClick={() => onMode("search")} />
        <button type="button" className="session-sidebar__rail-button" aria-label="新建会话" title="新建会话" onClick={onNewSession}>
          <SquarePen size={21} aria-hidden="true" />
        </button>
        {modes.slice(1).map((item) => (
          <RailButton key={item.mode} {...item} selected={activeMode === item.mode} onClick={() => onMode(item.mode)} />
        ))}
      </div>

      <button
        type="button"
        className="session-sidebar__rail-button session-sidebar__rail-settings"
        aria-label="设置"
        title="设置"
        onPointerDown={stopFramelessPointerDown}
        onPointerUp={(event) => runFramelessPointerAction(event, onOpenSettings)}
        onMouseDown={stopFramelessMouseDown}
        onClick={(event) => runKeyboardClick(event, onOpenSettings)}
      >
        <Settings size={21} aria-hidden="true" />
      </button>
    </nav>
  );
}

function RailButton({ label, icon: Icon, selected, onClick }: { mode: SidebarMode; label: string; icon: typeof Search; selected: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      className={`session-sidebar__rail-button${selected ? " session-sidebar__rail-button--active" : ""}`}
      aria-label={label}
      aria-current={selected ? "page" : undefined}
      title={label}
      onClick={onClick}
    >
      <Icon size={21} aria-hidden="true" />
    </button>
  );
}
