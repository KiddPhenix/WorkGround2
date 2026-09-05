// RecentSessionRail：PrimaryRail 的竖排最近会话区。纯展示组件——只消费
// SidebarShell 投影好的 RecentSessionRailItem，在每个图标下方渲染单行短名称，
// 并复用 AgentIcon；点击回调上抛。取数、过滤、身份映射、选中态与未读都在
// 数据层（Go 权威投影 + recentSessions 映射）完成。
import { AgentIcon } from "../components/agent-icon/AgentIcon";
import { useLayoutEffect, useRef, useState } from "react";
import type { RecentSessionRailItem } from "./recentSessions";

// 图标 29px（由 34px 精确缩小约 15%），下方单行名称。单元高度与 styles.css 的
// .session-sidebar__recent-session 尺寸保持一致，容量计算才准确。
const ITEM_HEIGHT = 50;
const ITEM_GAP = 6;

export function recentSessionCapacity(height: number): number {
  return Math.max(0, Math.floor((height + ITEM_GAP) / (ITEM_HEIGHT + ITEM_GAP)));
}

export function RecentSessionRail({ items, onOpen, onOpenMenu }: { items: RecentSessionRailItem[]; onOpen: (item: RecentSessionRailItem) => void; onOpenMenu?: (item: RecentSessionRailItem, element: HTMLElement) => void }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const [capacity, setCapacity] = useState(items.length);

  useLayoutEffect(() => {
    const host = hostRef.current;
    if (!host || typeof ResizeObserver === "undefined") {
      setCapacity(items.length);
      return;
    }
    const update = () => setCapacity(recentSessionCapacity(host.clientHeight));
    const observer = new ResizeObserver(update);
    observer.observe(host);
    update();
    return () => observer.disconnect();
  }, [items.length]);

  if (items.length === 0) return null;
  // 只渲染容量内的条目：超出部分不进入 DOM，因此既不可聚焦也不参与读屏。
  const visible = items.slice(0, capacity);
  return (
    <div ref={hostRef} className="session-sidebar__recent" role="group" aria-label="最近会话">
      {visible.map((item) => {
        const label = item.session.title.trim() || item.item.title.trim() || "新的会话";
        return (
          <button
            key={item.key}
            type="button"
            className={`session-sidebar__recent-session${item.active ? " session-sidebar__recent-session--active" : ""}`}
            aria-label={label}
            aria-current={item.active ? "page" : undefined}
            title={label}
            onClick={() => onOpen(item)}
            onContextMenu={(event) => {
              if (!onOpenMenu) return;
              event.preventDefault();
              onOpenMenu(item, event.currentTarget);
            }}
          >
            <AgentIcon viewModel={item.viewModel} />
            <span className="session-sidebar__recent-label" aria-hidden="true">{label}</span>
            {item.unread > 0 && <span className="session-sidebar__recent-unread" aria-hidden="true" />}
          </button>
        );
      })}
    </div>
  );
}
