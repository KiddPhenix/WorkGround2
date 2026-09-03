import type { ProjectTopicRuntimeHint } from "../lib/types";

export type SidebarQueryMode = "projects" | "rooms" | "assistants";
export type SidebarMode = "search" | SidebarQueryMode;
export type SidebarSearchFilter = "all" | "projects" | "sessions";

export interface SidebarGroup {
  id: string;
  kind: string;
  label: string;
  root?: string;
  color?: string;
  icon?: string;
  pinned?: boolean;
  sessionCount: number;
  lastActivityAt?: number;
}

export interface SidebarSession {
  id: string;
  sessionId?: string;
  groupId: string;
  scope: "global" | "project";
  workspaceRoot?: string;
  title: string;
  sessionPath?: string;
  topicId?: string;
  sessionKind?: string;
  sessionSource?: string;
  channel?: string;
  channelLabel?: string;
  titleSource?: string;
  status?: string;
  turns?: number;
  open?: boolean;
  running?: boolean;
  pinned?: boolean;
  createdAt?: number;
  lastActivityAt?: number;
  turnStartedAt?: number;
  revision: number;
}

export interface SidebarSessionQuery {
  mode: SidebarQueryMode;
  groupId?: string;
  cursor?: string;
  limit?: number;
  requestId?: string;
}

export interface SidebarPage<T> {
  items: T[];
  nextCursor?: string;
  total?: number;
  snapshot: string;
}

export interface SidebarSearchRequest {
  query: string;
  filter: SidebarSearchFilter;
  cursor?: string;
  limit?: number;
  requestId?: string;
}

export interface SidebarSearchItem {
  kind: "project" | "session";
  id: string;
  group?: SidebarGroup;
  session?: SidebarSession;
  lastActivityAt?: number;
}

export interface SidebarOpenTarget {
  scope: "global" | "project";
  workspaceRoot: string;
  topicId: string;
  sessionPath?: string;
  runtimeHint?: ProjectTopicRuntimeHint;
}

export interface SidebarPageState<T> {
  items: T[];
  nextCursor?: string;
  total?: number;
  snapshot: string;
  status: "idle" | "loading" | "ready" | "error";
  error?: string;
  requestSeq: number;
}
