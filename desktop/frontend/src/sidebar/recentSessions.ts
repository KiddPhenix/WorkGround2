// 侧边栏任务图标只消费 Go 侧 RecentSessions。该绑定直接投影 Widget 的
// DesktopIconSnapshot task 项；前端不再从 SearchSidebar/ListRuntimeTabs
// 重新拼一套集合，合法空集合、顺序、标题与外观身份都和 Widget 保持一致。
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { buildAgentIconViewModel } from "../lib/agentIcon/viewModel";
import type { AgentIconViewModel } from "../lib/agentIcon/types";
import { app, type DesktopIconItem, type RecentSessionItem } from "../lib/bridge";
import type { SidebarSession } from "./types";

export const RECENT_SESSION_LIMIT = 50;

export interface RecentSessionRailItem {
  key: string;
  item: DesktopIconItem;
  session: SidebarSession;
  groupKind: string;
  viewModel: AgentIconViewModel;
  active: boolean;
  unread: number;
}

function normalizedPath(path?: string): string { return (path || "").replace(/\\/g, "/").toLowerCase(); }

/** 稳定 React key：sessionPath → topicId → sessionId → id。 */
export function recentSessionKey(session: SidebarSession): string {
  return (session.sessionPath || "").trim() || (session.topicId || "").trim() || (session.sessionId || "").trim() || session.id;
}

function isActiveSession(session: SidebarSession, activeSessionPath?: string, activeTopicId?: string): boolean {
  const path = normalizedPath(session.sessionPath);
  return Boolean((path && path === normalizedPath(activeSessionPath)) || (session.topicId && session.topicId === activeTopicId));
}

let recentRequestID = 0;
function nextRequestID(): string {
  recentRequestID += 1;
  return `widget-task-icons-${Date.now()}-${recentRequestID}`;
}

export interface RecentSessionsOptions {
  refreshSignal?: number;
  activeSessionPath?: string;
  activeTopicId?: string;
  // 保留调用契约；rail 未读直接使用 Widget item.unreadCount，避免第二数据源覆盖。
  unreadBySession: Map<string, number>;
}

export function useRecentSessions(options: RecentSessionsOptions): RecentSessionRailItem[] {
  const { refreshSignal, activeSessionPath, activeTopicId } = options;
  const [items, setItems] = useState<RecentSessionItem[]>([]);
  const seqRef = useRef(0);

  const load = useCallback(async () => {
    const seq = seqRef.current + 1;
    seqRef.current = seq;
    try {
      const page = await app.RecentSessions({ limit: RECENT_SESSION_LIMIT, requestId: nextRequestID() });
      if (seqRef.current !== seq) return;
      // 空数组也是 Widget 的权威结果，必须覆盖旧内容。
      setItems(page.items ?? []);
    } catch (error) {
      // 短时读取失败保留最后一次权威结果，等待事件或恢复轮询安全重试。
      console.error("[recent-session-rail] failed to load widget task projection", error);
    }
  }, []);

  useEffect(() => {
    void load();
    const recovery = window.setInterval(() => void load(), 30_000);
    return () => {
      window.clearInterval(recovery);
      seqRef.current += 1;
    };
  }, [load]);

  const priorSignalRef = useRef(refreshSignal);
  useEffect(() => {
    if (refreshSignal === undefined || priorSignalRef.current === refreshSignal) return;
    priorSignalRef.current = refreshSignal;
    void load();
  }, [load, refreshSignal]);

  return useMemo(() => items.map((entry) => ({
    key: recentSessionKey(entry.session),
    item: entry.item,
    session: entry.session,
    groupKind: entry.groupKind || "",
    viewModel: buildAgentIconViewModel(entry.item),
    active: isActiveSession(entry.session, activeSessionPath, activeTopicId),
    unread: entry.item.unreadCount || 0,
  })), [activeSessionPath, activeTopicId, items]);
}
