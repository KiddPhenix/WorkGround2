import { useVirtualizer } from "@tanstack/react-virtual";
import { AlertCircle, ChevronDown, ChevronRight, Circle, MoreHorizontal } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent } from "react";
import type { SidebarSession } from "./types";
import { ProjectGlyph } from "./ProjectGlyph";
import { formatSidebarAbsoluteTime, formatSidebarRelativeTime } from "./sidebarTime";
import { isSidebarMenuShortcut } from "./sidebarKeyboard";

export type SidebarListRow =
  | { key: string; kind: "group"; label: string; color?: string; icon?: string; count?: number; expanded?: boolean; onToggle?: () => void; onMenu?: (element: HTMLElement) => void }
  | { key: string; kind: "session"; session: SidebarSession; unread?: number; active?: boolean; onOpen: () => void; onMenu?: (element: HTMLElement) => void }
  | { key: string; kind: "project"; label: string; color?: string; onOpen: () => void }
  | { key: string; kind: "load"; label: string; disabled?: boolean; onLoad: () => void }
  | { key: string; kind: "error"; message: string; onRetry: () => void }
  | { key: string; kind: "empty"; label: string };

function rowInteractive(row: SidebarListRow | undefined): boolean {
  if (!row) return false;
  if (row.kind === "group") return Boolean(row.onToggle);
  return row.kind !== "empty" && (row.kind !== "load" || !row.disabled);
}

function runRow(row: SidebarListRow): void {
  if (row.kind === "group") row.onToggle?.();
  else if (row.kind === "session" || row.kind === "project") row.onOpen();
  else if (row.kind === "load") row.onLoad();
  else if (row.kind === "error") row.onRetry();
}

function openRowMenu(event: ReactKeyboardEvent<HTMLElement> | ReactMouseEvent<HTMLElement>, row: Extract<SidebarListRow, { kind: "group" | "session" }>): void {
  if (("key" in event && !isSidebarMenuShortcut(event.key, event.shiftKey)) || !row.onMenu) return;
  event.preventDefault();
  event.stopPropagation();
  row.onMenu(event.currentTarget);
}

export function SessionList({ rows, now, className = "", id }: { rows: SidebarListRow[]; now: number; className?: string; id?: string }) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [focusIndex, setFocusIndex] = useState(0);
  const firstInteractive = rows.findIndex(rowInteractive);
  const activeIndex = rowInteractive(rows[focusIndex]) ? focusIndex : firstInteractive;
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (index) => rows[index]?.kind === "group" ? 44 : 40,
    overscan: 8,
  });

  useEffect(() => { virtualizer.measure(); }, [rows.length, virtualizer]);

  useEffect(() => {
    if (activeIndex >= 0 && focusIndex !== activeIndex) setFocusIndex(activeIndex);
  }, [activeIndex, focusIndex]);

  const moveFocus = (next: number) => {
    if (next < 0) return;
    setFocusIndex(next);
    virtualizer.scrollToIndex(next, { align: "auto" });
    window.requestAnimationFrame(() => scrollRef.current?.querySelector<HTMLElement>(`[data-sidebar-row-index="${next}"] [data-sidebar-primary]`)?.focus());
  };

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const current = activeIndex < 0 ? 0 : activeIndex;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      let next = current + step;
      while (next >= 0 && next < rows.length && !rowInteractive(rows[next])) next += step;
      moveFocus(next);
      return;
    }
    const row = rows[current];
    if ((event.key === "ArrowRight" || event.key === "ArrowLeft") && row?.kind === "group" && row.onToggle) {
      const shouldToggle = event.key === "ArrowRight" ? row.expanded === false : row.expanded !== false;
      if (shouldToggle) {
        event.preventDefault();
        row.onToggle();
      }
      return;
    }
    if (event.key === "Enter" && rowInteractive(row)) {
      event.preventDefault();
      runRow(row);
    }
  };

  return (
    <div id={id} ref={scrollRef} className={`session-sidebar__scroll ${className}`} role="list" onKeyDown={onKeyDown}>
      <div className="session-sidebar__virtual" style={{ height: virtualizer.getTotalSize() }}>
        {virtualizer.getVirtualItems().map((virtualRow) => {
          const row = rows[virtualRow.index];
          return (
            <div
              key={row.key}
              className="session-sidebar__virtual-row"
              style={{ height: virtualRow.size, transform: `translateY(${virtualRow.start}px)` }}
              data-index={virtualRow.index}
              data-sidebar-row-index={virtualRow.index}
              ref={virtualizer.measureElement}
            >
              <SidebarRow row={row} now={now} tabIndex={virtualRow.index === activeIndex ? 0 : -1} onFocus={() => setFocusIndex(virtualRow.index)} />
            </div>
          );
        })}
      </div>
    </div>
  );
}

function SidebarRow({ row, now, tabIndex, onFocus }: { row: SidebarListRow; now: number; tabIndex: number; onFocus: () => void }) {
  if (row.kind === "group") {
    return (
      <div className="session-sidebar__group-row" role="listitem">
        <button type="button" className="session-sidebar__group-main" aria-expanded={row.expanded} aria-haspopup={row.onMenu ? "menu" : undefined} onClick={row.onToggle} onKeyDown={(event) => openRowMenu(event, row)} onContextMenu={(event) => openRowMenu(event, row)} data-sidebar-primary tabIndex={row.onToggle ? tabIndex : -1} onFocus={onFocus}>
          <span className="session-sidebar__chevron" aria-hidden="true">{row.expanded === false ? <ChevronRight size={15} /> : <ChevronDown size={15} />}</span>
          <span className="session-sidebar__group-icon" aria-hidden="true"><ProjectGlyph icon={row.icon} open={row.expanded} /></span>
          <span className="session-sidebar__group-label">{row.label}</span>
          {typeof row.count === "number" && <span className="session-sidebar__group-count">{row.count}</span>}
        </button>
        {row.onMenu && (
          <button type="button" className="session-sidebar__row-menu" aria-label={`${row.label}菜单`} aria-haspopup="menu" tabIndex={-1} onFocus={onFocus} onClick={(event) => row.onMenu?.(event.currentTarget)}>
            <MoreHorizontal size={16} aria-hidden="true" />
          </button>
        )}
      </div>
    );
  }
  if (row.kind === "session") {
    const at = row.session.lastActivityAt || row.session.createdAt;
    const state = row.session.status || (row.session.running ? "running" : "");
    const source = row.session.channelLabel || (row.session.sessionSource === "assist" ? "ASSIST" : row.session.channel ? row.session.channel.toUpperCase() : "");
    return (
      <div className={`session-sidebar__session-row${row.active ? " session-sidebar__session-row--active" : ""}`} role="listitem">
        <button type="button" className="session-sidebar__session-main" aria-haspopup={row.onMenu ? "menu" : undefined} onClick={row.onOpen} onKeyDown={(event) => openRowMenu(event, row)} onContextMenu={(event) => openRowMenu(event, row)} title={`${row.session.title}${at ? `\n${formatSidebarAbsoluteTime(at)}` : ""}`} data-sidebar-primary tabIndex={tabIndex} onFocus={onFocus}>
          <span className={`session-sidebar__status${state ? ` session-sidebar__status--${state}` : ""}`} aria-label={state || "会话"}>
            {state === "error" ? <AlertCircle size={12} aria-hidden="true" /> : <Circle size={9} fill="currentColor" aria-hidden="true" />}
          </span>
          {source && <span className="session-sidebar__source" title={source}>{source}</span>}
          <span className="session-sidebar__session-title">{row.session.title || "未命名会话"}</span>
          {Boolean(row.unread) && <span className="session-sidebar__unread" aria-label={`${row.unread}条未读`}>{row.unread! > 99 ? "99+" : row.unread}</span>}
          <time className="session-sidebar__time" dateTime={at ? new Date(at).toISOString() : undefined}>{formatSidebarRelativeTime(at, now)}</time>
        </button>
        {row.onMenu && (
          <button type="button" className="session-sidebar__row-menu" aria-label={`${row.session.title}菜单`} aria-haspopup="menu" tabIndex={-1} onFocus={onFocus} onClick={(event) => row.onMenu?.(event.currentTarget)}>
            <MoreHorizontal size={15} aria-hidden="true" />
          </button>
        )}
      </div>
    );
  }
  if (row.kind === "project") return <button type="button" className="session-sidebar__project-result" onClick={row.onOpen} data-sidebar-primary tabIndex={tabIndex} onFocus={onFocus}><span style={{ background: row.color || "var(--accent)" }} />{row.label}</button>;
  if (row.kind === "load") return <button type="button" className="session-sidebar__load-more" disabled={row.disabled} onClick={row.onLoad} data-sidebar-primary tabIndex={tabIndex} onFocus={onFocus}>{row.label}</button>;
  if (row.kind === "error") return <div className="session-sidebar__inline-error" role="alert"><span>{row.message || "加载失败"}</span><button type="button" onClick={row.onRetry} data-sidebar-primary tabIndex={tabIndex} onFocus={onFocus}>重试</button></div>;
  return <div className="session-sidebar__empty" role="status">{row.label}</div>;
}
