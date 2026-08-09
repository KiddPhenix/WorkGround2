// ProjectTree is the sidebar replacement for the flat recent-sessions list.
// It shows a tree of projects (each with expandable topics) plus a Global
// section. Clicking a topic opens its tab; "+" next to a project creates a
// new topic.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, DragEvent as ReactDragEvent, KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent } from "react";
import { Archive, Pencil, Plus, MoreHorizontal, MoreVertical, Folder, FolderPlus, Search, BriefcaseBusiness, Copy, FolderOpen, XCircle, History, Check, ListCollapse, ListRestart, MessageSquare, Clock, Pin, Users, ChevronDown, ChevronRight, SquarePlus, SlidersHorizontal } from "lucide-react";
import { asArray } from "../lib/array";
import { useToast } from "../lib/toast";
import { app, onUnreadState } from "../lib/bridge";
import type { ProjectNode, ProjectTopicRuntimeHint, ProjectTopicStatus, UnreadConversation, UnreadState } from "../lib/types";
import { topicActivityTime } from "../lib/session";
import { getLocale, useT, type DictKey, type Translator } from "../lib/i18n";
import { PROJECT_COLOR_OPTIONS, projectColorValue } from "../lib/projectColors";
import { topicShortcutLabel, type TopicShortcutEntry } from "../lib/topicShortcuts";
import type { ShortcutPlatform } from "../lib/keyboardShortcuts";
import { ContextMenu, contextMenuPointFromEvent, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";
import { Tooltip } from "./Tooltip";

interface ProjectTreeProps {
  activeScope?: string;
  activeWorkspaceRoot?: string;
  activeTopicId?: string;
  activeSessionPath?: string;
  imTopicSources?: Record<string, ProjectTreeImTopicSource>;
  variant?: "classic" | "workbench" | "creation";
  onOpenTopic: (scope: string, workspaceRoot: string, topicId: string, sessionPath?: string, runtimeHint?: ProjectTopicRuntimeHint) => Promise<void> | void;
  onOpenCrewSession?: (sessionPath: string) => Promise<void> | void;
  onOpenProjectHistory: (scope: "global" | "project", workspaceRoot: string) => Promise<void> | void;
  onAddProject: () => Promise<void>;
  onCreateTopic?: (scope: string, workspaceRoot: string) => Promise<void> | void;
  onRenameTopic?: (topicId: string, title: string) => Promise<void> | void;
  onTopicsChanged?: () => Promise<void> | void;
  onCreateWork?: (scope: string, workspaceRoot: string) => Promise<void> | void;
  refreshSignal?: number;
  timeFilter: "all" | "10" | "20" | "1h" | "3h" | "5h" | "1d";
  onTimeFilterChange: (filter: "all" | "10" | "20" | "1h" | "3h" | "5h" | "1d") => void;
  searchExpanded?: boolean;
  searchFocusSignal?: number;
  showShortcutBadges?: boolean;
  shortcutPlatform?: ShortcutPlatform;
  onVisibleTopicsChange?: (topics: TopicShortcutEntry[]) => void;
}

type ProjectTreeImTopicSource = {
  platform?: string;
  label: string;
  title?: string;
  remoteId?: string;
};

function projectNodeKey(node: ProjectNode, depth: number): string {
  return node.key || `${node.kind}-${node.root ?? ""}-${node.topicId ?? ""}-${depth}`;
}

export function projectTreeMenuKey(section: "recent" | "projects", nodeKey: string): string {
  return `${section}\u001f${nodeKey}`;
}

function isRuntimeSessionNode(node: ProjectNode): boolean {
  return node.kind === "session" || node.kind === "global_session" || node.kind === "work_session" || node.kind === "global_work_session";
}

function isCrewSessionNode(node: ProjectNode): boolean {
  return node.kind === "crew_session";
}

function isTopicNode(node: ProjectNode): boolean {
  return node.kind === "topic" || node.kind === "global_topic";
}

export type ProjectTreeTrashTarget =
  | { kind: "topic"; topicId: string }
  | { kind: "session"; path: string };

export function projectTreeTrashTarget(node: ProjectNode): ProjectTreeTrashTarget | null {
  if (node.sessionKind === "work" || isTopicNode(node)) {
    const topicId = (node.topicId ?? "").trim();
    return topicId ? { kind: "topic", topicId } : null;
  }
  if (isRuntimeSessionNode(node) || isCrewSessionNode(node)) {
    const path = (node.sessionPath ?? "").trim();
    return path ? { kind: "session", path } : null;
  }
  return null;
}

export function projectTreeNodeScope(node: ProjectNode): "global" | "project" {
  return node.kind.startsWith("global_") ? "global" : "project";
}

function comparableSessionPath(path: string): string {
  const trimmed = path.trim().replace(/\\/g, "/");
  return /^[a-z]:/i.test(trimmed) ? trimmed.toLowerCase() : trimmed;
}

export function projectTreeSessionPathMatches(activeSessionPath?: string, nodeSessionPath?: string): boolean {
  const active = comparableSessionPath(activeSessionPath ?? "");
  const node = comparableSessionPath(nodeSessionPath ?? "");
  return Boolean(active && node && active === node);
}

function unreadSessionPath(sessionId?: string): string {
  const value = (sessionId ?? "").trim();
  return value.toLowerCase().startsWith("path:") ? value.slice(5).trim() : "";
}

export function projectTreeUnreadConversations(node: ProjectNode, conversations: UnreadConversation[]): UnreadConversation[] {
  const nodeSessionId = (node.sessionId ?? "").trim();
  return conversations.filter((conversation) => {
    if (conversation.unreadCount <= 0) return false;
    const conversationSessionId = (conversation.sessionId ?? "").trim();
    if (!conversationSessionId) return false;
    if (nodeSessionId && conversationSessionId === nodeSessionId) return true;
    const path = unreadSessionPath(conversationSessionId);
    return Boolean(path && projectTreeSessionPathMatches(path, node.sessionPath));
  });
}

export function projectTreeUnreadCount(node: ProjectNode, conversations: UnreadConversation[]): number {
  return projectTreeUnreadConversations(node, conversations).reduce((total, conversation) => total + conversation.unreadCount, 0);
}

export type ProjectTreeTopicOpenRequest = {
  scope: "global" | "project";
  workspaceRoot: string;
  topicId: string;
  sessionPath?: string;
  runtimeHint?: ProjectTopicRuntimeHint;
};

export function projectTreeTopicOpenRequest(node: ProjectNode): ProjectTreeTopicOpenRequest | null {
  if (!isTopicNode(node) && !isRuntimeSessionNode(node)) return null;
  const scope = projectTreeNodeScope(node);
  const status = topicStatus(node);
  const runtimeHint = node.running || status ? { running: Boolean(node.running), status: status || undefined, turnStartedAt: node.turnStartedAt } : undefined;
  return {
    scope,
    workspaceRoot: scope === "global" ? "" : node.root ?? "",
    topicId: node.topicId ?? "",
    sessionPath: node.sessionPath,
    runtimeHint,
  };
}

type ProjectTreeTopicClickTarget = {
  rowKey: string;
  canRename: boolean;
};

type ProjectTreePendingTopicOpen = ProjectTreeTopicClickTarget & {
  timer: ReturnType<typeof setTimeout>;
};

export function projectTreeShouldSuppressOpenForRename(
  pending: ProjectTreeTopicClickTarget | null,
  next: ProjectTreeTopicClickTarget,
): boolean {
  return Boolean(pending && pending.rowKey === next.rowKey && pending.canRename && next.canRename);
}

export type ProjectTreeFolderDisclosure = {
  canExpand: boolean;
  isOpen: boolean;
  ariaExpanded?: boolean;
  iconStackClassName: string;
};

export function projectTreeFolderDisclosure(hasChildren: boolean, isExpanded: boolean): ProjectTreeFolderDisclosure {
  const canExpand = hasChildren;
  const isOpen = canExpand && isExpanded;
  return {
    canExpand,
    isOpen,
    ariaExpanded: canExpand ? isExpanded : undefined,
    iconStackClassName: `project-tree__icon-stack${canExpand ? " project-tree__icon-stack--expandable" : ""}`,
  };
}

function topicIsActive(node: ProjectNode, activeScope?: string, activeWorkspaceRoot?: string, activeTopicId?: string, activeSessionPath?: string): boolean {
  if (!isTopicNode(node) && !isRuntimeSessionNode(node) && !isCrewSessionNode(node)) return false;
  if (node.sessionPath) return projectTreeSessionPathMatches(activeSessionPath, node.sessionPath);
  if (activeSessionPath && asArray(node.children).some(isRuntimeSessionNode)) return false;
  const scope = projectTreeNodeScope(node);
  return (
    activeTopicId === node.topicId &&
    activeScope === scope &&
    (scope === "global" || activeWorkspaceRoot === node.root)
  );
}

export function projectTreeActiveKey(
  nodes: ProjectNode[],
  activeScope?: string,
  activeWorkspaceRoot?: string,
  activeTopicId?: string,
  activeSessionPath?: string,
): string | null {
  let activeKey: string | null = null;
  const walk = (nodeList: ProjectNode[], depth: number) => {
    for (const node of nodeList) {
      if (!node) continue;
      if (topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)) {
        activeKey = projectNodeKey(node, depth);
      }
      walk(asArray(node.children), depth + 1);
    }
  };
  walk(nodes, 0);
  return activeKey;
}

function topicMetaLine(node: ProjectNode, t: Translator, compact = false): string {
  const parts: string[] = [];
  const turns = node.turns ?? 0;
  if (turns > 0) parts.push(t(turns === 1 ? "history.turnOne" : "history.turnOther", { n: turns }));
  const activityAt = node.lastActivityAt || node.createdAt || 0;
  if (activityAt) parts.push(topicActivityLabel(activityAt, t, compact));
  if (parts.length === 0) parts.push(t("projectTree.justNow"));
  return parts.join(" · ");
}

const topicStatusLabels: Record<ProjectTopicStatus, DictKey> = {
  thinking: "projectTree.status.thinking",
  streaming: "projectTree.status.streaming",
  waiting_confirmation: "projectTree.status.waitingConfirmation",
  background_job: "projectTree.status.backgroundJob",
  paused: "projectTree.status.paused",
  error: "projectTree.status.error",
};

function normalizeTopicStatus(status?: string): ProjectTopicStatus | "" {
  if (!status) return "";
  if (status === "thinking" || status === "streaming" || status === "waiting_confirmation" || status === "background_job" || status === "paused" || status === "error") {
    return status;
  }
  return "";
}

function topicStatus(node: ProjectNode): ProjectTopicStatus | "" {
  return normalizeTopicStatus(node.status) || (node.running ? "streaming" : "");
}

function topicStatusLabel(node: ProjectNode, t: Translator): string {
  const status = topicStatus(node);
  return status ? t(topicStatusLabels[status]) : "";
}

type TopicVisualState = "none" | "running" | "done" | "failed";

export function projectTreeTopicVisualState(node: ProjectNode, unread: boolean, status = topicStatus(node)): TopicVisualState {
  if (node.running) return "running";
  if (!unread) return "none";
  if (status === "error") return "failed";
  if ((node.turns ?? 0) > 0 || topicActivityAt(node) > 0) return "done";
  return "none";
}

function topicActivityAt(node: ProjectNode): number {
  return node.lastActivityAt || node.createdAt || 0;
}

export function projectTreeReadActivityKey(node: ProjectNode): string | null {
  const request = projectTreeTopicOpenRequest(node);
  if (!request?.topicId) return null;
  return [
    request.scope,
    request.workspaceRoot,
    request.topicId,
    request.sessionPath ?? "",
  ].join("\u001f");
}

type ProjectTreeReadActivity = Record<string, number>;

export function projectTreeTopicHasUnreadActivity(
  node: ProjectNode,
  readActivity: ProjectTreeReadActivity,
  activeScope?: string,
  activeWorkspaceRoot?: string,
  activeTopicId?: string,
  activeSessionPath?: string,
): boolean {
  if (!isTopicNode(node) && !isRuntimeSessionNode(node)) return false;
  if (topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)) return false;
  const status = topicStatus(node);
  if (status === "thinking" || status === "streaming" || status === "waiting_confirmation" || status === "background_job") return false;
  const key = projectTreeReadActivityKey(node);
  const activityAt = topicActivityAt(node);
  return Boolean(key && activityAt > 0 && (readActivity[key] ?? 0) < activityAt);
}

export function projectTreeShouldRenderTopicActions(
  compactTopics: boolean,
  unread: boolean,
): boolean {
  return compactTopics && !unread;
}

function topicActivityLabel(ms: number, t: Translator, compact = false): string {
  if (ms <= 0) return "";
  const delta = Date.now() - ms;
  const locale = getLocale();
  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;
  const month = 30 * day;
  const year = 365 * day;
  if (delta < minute) return t("projectTree.justNow");
  if (!compact) {
    const rtfLocale = locale === "zh" ? "zh-CN" : locale === "zh-TW" ? "zh-TW" : "en";
    const rtf = new Intl.RelativeTimeFormat(rtfLocale, { numeric: "auto" });
    if (delta < hour) return rtf.format(-Math.max(1, Math.round(delta / minute)), "minute");
    if (delta < day) return rtf.format(-Math.round(delta / hour), "hour");
    if (delta < 7 * day) return rtf.format(-Math.round(delta / day), "day");
    return new Date(ms).toLocaleDateString();
  }
  if (delta < hour) {
    const value = Math.max(1, Math.round(delta / minute));
    return locale === "zh" || locale === "zh-TW" ? `${value} 分钟` : `${value}m`;
  }
  if (delta < day) {
    const value = Math.round(delta / hour);
    return locale === "zh" || locale === "zh-TW" ? `${value} 小时` : `${value}h`;
  }
  if (delta < 7 * day) {
    const value = Math.round(delta / day);
    return locale === "zh" || locale === "zh-TW" ? `${value} 天` : `${value}d`;
  }
  if (delta < month) {
    const value = Math.round(delta / day);
    return locale === "zh" || locale === "zh-TW" ? `${value} 天` : `${value}d`;
  }
  if (delta < year) {
    const value = Math.max(1, Math.round(delta / month));
    return locale === "zh" || locale === "zh-TW" ? `${value} 个月` : `${value}mo`;
  }
  const value = Math.max(1, Math.round(delta / year));
  return locale === "zh" || locale === "zh-TW" ? `${value} 年` : `${value}y`;
}

function topicActivityDateLabel(ms: number): string {
  if (ms <= 0) return "";
  const locale = getLocale();
  const dateLocale = locale === "zh" ? "zh-CN" : locale === "zh-TW" ? "zh-TW" : "en";
  return new Date(ms).toLocaleDateString(dateLocale);
}

type ProjectDropPosition = "before" | "after";
type WorkbenchOrganizeMode = "project" | "recent" | "time";
type WorkbenchSortMode = "created" | "updated";
export type WorkbenchRecentLimit = 1 | 3 | 5 | 10;

export type WorkbenchRecentSettings = {
  showExternal: boolean;
  limit: WorkbenchRecentLimit;
};

type CollapseSnapshot = {
  expanded: Set<string>;
  manuallyCollapsed: Set<string>;
};

type WorkbenchTreeSections = {
  recent: ProjectNode[];
  projects: ProjectNode[];
};

const GLOBAL_PROJECT_ORDER_KEY = "__global__";
const WORKBENCH_ORGANIZE_KEY = "projectTree:workbenchOrganize";
const WORKBENCH_SORT_KEY = "projectTree:workbenchSort";
const WORKBENCH_RECENT_KEY = "projectTree:workbenchRecent";
const WORKBENCH_RECENT_LIMITS: WorkbenchRecentLimit[] = [1, 3, 5, 10];
const DEFAULT_WORKBENCH_RECENT: WorkbenchRecentSettings = { showExternal: true, limit: 1 };
const READ_ACTIVITY_KEY = "projectTree:readActivity";
const READ_ACTIVITY_INIT_KEY = "projectTree:readActivityInitialized";
const EMPTY_UNREAD_STATE: UnreadState = {
  available: false,
  summary: { revision: 0, totalUnread: 0, highPriorityCount: 0, conversations: [] },
};

function loadReadActivity(): ProjectTreeReadActivity {
  try {
    const raw = localStorage.getItem(READ_ACTIVITY_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    const out: ProjectTreeReadActivity = {};
    for (const [key, value] of Object.entries(parsed)) {
      if (typeof value === "number" && Number.isFinite(value)) out[key] = value;
    }
    return out;
  } catch {
    return {};
  }
}

function saveReadActivity(readActivity: ProjectTreeReadActivity) {
  try {
    localStorage.setItem(READ_ACTIVITY_KEY, JSON.stringify(readActivity));
  } catch {
    /* localStorage unavailable */
  }
}

function loadWorkbenchOrganizeMode(): WorkbenchOrganizeMode {
  try {
    const value = localStorage.getItem(WORKBENCH_ORGANIZE_KEY);
    if (value === "recent" || value === "time") return value;
  } catch {
    /* localStorage unavailable */
  }
  return "project";
}

function loadWorkbenchSortMode(): WorkbenchSortMode {
  try {
    const value = localStorage.getItem(WORKBENCH_SORT_KEY);
    if (value === "created") return "created";
  } catch {
    /* localStorage unavailable */
  }
  return "updated";
}

export function parseWorkbenchRecentSettings(value: unknown): WorkbenchRecentSettings {
  if (!value || typeof value !== "object") return DEFAULT_WORKBENCH_RECENT;
  const candidate = value as Partial<WorkbenchRecentSettings>;
  return {
    showExternal: typeof candidate.showExternal === "boolean" ? candidate.showExternal : DEFAULT_WORKBENCH_RECENT.showExternal,
    limit: WORKBENCH_RECENT_LIMITS.includes(candidate.limit as WorkbenchRecentLimit)
      ? candidate.limit as WorkbenchRecentLimit
      : DEFAULT_WORKBENCH_RECENT.limit,
  };
}

function loadWorkbenchRecentSettings(): WorkbenchRecentSettings {
  try {
    const value = localStorage.getItem(WORKBENCH_RECENT_KEY);
    return value ? parseWorkbenchRecentSettings(JSON.parse(value)) : DEFAULT_WORKBENCH_RECENT;
  } catch {
    return DEFAULT_WORKBENCH_RECENT;
  }
}

function saveWorkbenchRecentSettings(settings: WorkbenchRecentSettings) {
  try {
    localStorage.setItem(WORKBENCH_RECENT_KEY, JSON.stringify(settings));
  } catch {
    /* localStorage unavailable */
  }
}

const CREW_PROJECT_ORDER_KEY = "__crew__";

function projectOrderKey(node: ProjectNode): string {
  if (node.kind === "global_folder") return GLOBAL_PROJECT_ORDER_KEY;
  if (node.kind === "crew_folder") return CREW_PROJECT_ORDER_KEY;
  if (node.kind === "project" && node.root) return node.root;
  return "";
}

function projectRoots(nodes: ProjectNode[]): string[] {
  return nodes
    .map(projectOrderKey)
    .filter((key) => key !== "" && key !== CREW_PROJECT_ORDER_KEY);
}

function collapsibleFolderKeys(nodes: ProjectNode[], depth = 0): string[] {
  const keys: string[] = [];
  for (const node of nodes) {
    if (!node) continue;
    const children = asArray(node.children);
    if ((node.kind === "project" || node.kind === "global_folder" || node.kind === "crew_folder") && children.length > 0) {
      keys.push(projectNodeKey(node, depth));
    }
    keys.push(...collapsibleFolderKeys(children, depth + 1));
  }
  return keys;
}

export function activeSessionAncestorKeys(
  nodes: ProjectNode[],
  activeScope?: string,
  activeWorkspaceRoot?: string,
  activeTopicId?: string,
  activeSessionPath?: string,
): string[] {
  const walk = (nodeList: ProjectNode[], ancestors: string[]): string[] | null => {
    for (const node of nodeList) {
      if (!node) continue;
      if (topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)) return ancestors;
      const children = asArray(node.children);
      if (children.length > 0) {
        const next = walk(children, [...ancestors, projectNodeKey(node, ancestors.length)]);
        if (next) return next;
      }
    }
    return null;
  };
  return walk(nodes, []) ?? [];
}

export function defaultExpandedProjectTreeKeys(
  nodes: ProjectNode[],
  activeScope?: string,
  activeWorkspaceRoot?: string,
  activeTopicId?: string,
  activeSessionPath?: string,
): string[] {
  return activeSessionAncestorKeys(nodes, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath);
}

export function reorderedProjectRoots(nodes: ProjectNode[], draggedRoot: string, targetRoot: string, position: ProjectDropPosition): string[] {
  const roots = projectRoots(nodes);
  if (draggedRoot === targetRoot || !roots.includes(draggedRoot) || !roots.includes(targetRoot)) return roots;
  const next = roots.filter((root) => root !== draggedRoot);
  const targetIndex = next.indexOf(targetRoot);
  if (targetIndex < 0) return roots;
  next.splice(position === "before" ? targetIndex : targetIndex + 1, 0, draggedRoot);
  return next;
}

function applyProjectOrder(nodes: ProjectNode[], roots: string[]): ProjectNode[] {
  const projectEntries = nodes
    .map((node): [string, ProjectNode] => [projectOrderKey(node), node])
    .filter(([key]) => key !== "");
  const byRoot = new Map<string, ProjectNode>(projectEntries);
  const orderedProjects = roots.map((root) => byRoot.get(root)).filter((node): node is ProjectNode => Boolean(node));
  const orderedKeys = new Set(roots);
  const nonProjects = nodes.filter((node) => !orderedKeys.has(projectOrderKey(node)));
  return [...orderedProjects, ...nonProjects];
}

function topicSortValue(node: ProjectNode, sortMode: WorkbenchSortMode): number {
  if (sortMode === "created") return node.createdAt || node.lastActivityAt || 0;
  return topicActivityTime(node);
}

function projectSortValue(node: ProjectNode, sortMode: WorkbenchSortMode): number {
  return asArray(node.children).reduce((max, child) => {
    if (!isTopicNode(child)) return max;
    return Math.max(max, topicSortValue(child, sortMode));
  }, 0);
}

function sortWorkbenchChildren(children: ProjectNode[], sortMode: WorkbenchSortMode): ProjectNode[] {
  return [...children].sort((a, b) => {
    if (!isTopicNode(a) || !isTopicNode(b)) return 0;
    if (Boolean(a.pinned) !== Boolean(b.pinned)) return a.pinned ? -1 : 1;
    return topicSortValue(b, sortMode) - topicSortValue(a, sortMode);
  });
}

function arrangeWorkbenchTree(nodes: ProjectNode[], organizeMode: WorkbenchOrganizeMode, sortMode: WorkbenchSortMode): ProjectNode[] {
  const arranged = nodes.map((node) => {
    if (node.kind !== "project" && node.kind !== "global_folder" && node.kind !== "crew_folder") return node;
    return { ...node, children: sortWorkbenchChildren(asArray(node.children), sortMode) };
  });
  if (organizeMode === "project") return arranged;
  const mode = organizeMode === "recent" ? "updated" : sortMode;
  return [...arranged].sort((a, b) => {
    if (Boolean(a.pinned) !== Boolean(b.pinned)) return a.pinned ? -1 : 1;
    return projectSortValue(b, mode) - projectSortValue(a, mode);
  });
}

export function projectTreeIsExternalCall(node: ProjectNode): boolean {
  const source = (node.sessionSource ?? "").trim().toLowerCase();
  const channel = (node.channel ?? "").trim();
  const titleSource = (node.titleSource ?? "").trim().toLowerCase();
  if (channel) return true;
  if (source.startsWith("work:") || source === "collaboration") return false;
  if (source) return true;
  return Boolean(titleSource && titleSource !== "manual" && titleSource !== "auto");
}

export function splitWorkbenchRecentTree(
  nodes: ProjectNode[],
  sortMode: WorkbenchSortMode,
  settings: WorkbenchRecentSettings,
): WorkbenchTreeSections {
  const recentTopics: ProjectNode[] = [];
  const projects: ProjectNode[] = [];

  for (const node of nodes) {
    if (!node) continue;
    const isFolder = node.kind === "project" || node.kind === "global_folder" || node.kind === "crew_folder";
    if (!isFolder) {
      if (isTopicNode(node)) recentTopics.push(node);
      projects.push(node);
      continue;
    }
    recentTopics.push(...asArray(node.children).filter((child) => isTopicNode(child) || isRuntimeSessionNode(child) || isCrewSessionNode(child)));
    projects.push(node);
  }

  recentTopics.sort((a, b) => {
    if (Boolean(a.running) !== Boolean(b.running)) return a.running ? -1 : 1;
    return topicSortValue(b, sortMode) - topicSortValue(a, sortMode);
  });

  return {
    recent: recentTopics
      .filter((node) => settings.showExternal || !projectTreeIsExternalCall(node))
      .slice(0, settings.limit),
    projects,
  };
}

// Global rows use the same project tree recipe; the fallback supplies their non-workspace accent.
function projectAccentStyle(color?: string, fallbackValue?: string): CSSProperties | undefined {
  const value = projectColorValue(color) || fallbackValue;
  if (!value) return undefined;
  return { "--project-accent": value } as CSSProperties;
}

const projectAccentPalette = ["#57b987", "#a970df", "#e9a03b", "#369fe8", "#d56f92", "#64a7c8"];

function projectAccentFallback(node: ProjectNode): string {
  if (node.kind === "global_folder") return "#369fe8";
  if (node.kind === "crew_folder") return "#e9a03b";
  const source = node.root || node.label || node.key || "project";
  let hash = 0;
  for (let index = 0; index < source.length; index += 1) hash = ((hash << 5) - hash + source.charCodeAt(index)) | 0;
  return projectAccentPalette[Math.abs(hash) % projectAccentPalette.length];
}

function recentProjectLabel(node: ProjectNode): string {
  if (projectTreeNodeScope(node) === "global") return "Global";
  const root = (node.root || "").replace(/[\\/]+$/, "");
  return root.split(/[\\/]/).filter(Boolean).pop() || "";
}

function colorMenuLabel(label: string, color?: string, active = false) {
  const value = projectColorValue(color);
  return (
    <span className="project-tree__color-option">
      <span
        className="project-tree__color-swatch"
        style={value ? ({ "--project-accent": value } as CSSProperties) : undefined}
        aria-hidden="true"
      />
      <span>{label}</span>
      {active && <Check className="project-tree__color-check" size={12} />}
    </span>
  );
}

function revealLabelKey(platform: string): "projectTree.revealInFinder" | "projectTree.revealInExplorer" | "projectTree.revealInFileManager" {
  if (platform === "darwin") return "projectTree.revealInFinder";
  if (platform === "windows") return "projectTree.revealInExplorer";
  return "projectTree.revealInFileManager";
}

function projectColorLabel(t: Translator, color?: string): string {
  switch (color) {
    case "red": return t("projectTree.colorRed");
    case "orange": return t("projectTree.colorOrange");
    case "amber": return t("projectTree.colorAmber");
    case "green": return t("projectTree.colorGreen");
    case "teal": return t("projectTree.colorTeal");
    case "blue": return t("projectTree.colorBlue");
    case "purple": return t("projectTree.colorPurple");
    case "pink": return t("projectTree.colorPink");
    default: return t("projectTree.colorDefault");
  }
}

type ProjectTreeSourceBadge = {
  label: string;
  title: string;
  className: string;
};

function projectTreeSourceBadge(node: ProjectNode, t: Translator): ProjectTreeSourceBadge | null {
  const source = (node.sessionSource ?? "").trim().toLowerCase();
  const channel = (node.channel ?? "").trim().toLowerCase();
  const titleSource = (node.titleSource ?? "").trim().toLowerCase();
  if (!source && !channel && !titleSource) return null;

  const title = t("projectTree.externalNamed");
  if (channel) {
    return { label: "IM", title, className: "project-tree__topic-origin--im" };
  }
  if (source === "auto") {
    return { label: "BOT", title, className: "project-tree__topic-origin--bot" };
  }
  if (source.includes("cli")) return { label: "CLI", title, className: "project-tree__topic-origin--cli" };
  if (source.includes("bot")) return { label: "BOT", title, className: "project-tree__topic-origin--bot" };
  if (!source && titleSource && titleSource !== "manual" && titleSource !== "auto") {
    return { label: titleSource.length <= 3 ? titleSource.toUpperCase() : t("projectTree.sourceExternalShort"), title, className: "project-tree__topic-origin--external" };
  }
  if (!source) return null;
  const label = source.length <= 3 ? source.toUpperCase() : t("projectTree.sourceExternalShort");
  return { label, title, className: "project-tree__topic-origin--external" };
}

export function ProjectTree({
  activeScope,
  activeWorkspaceRoot,
  activeTopicId,
  activeSessionPath,
  imTopicSources = {},
  variant = "classic",
  onOpenTopic,
  onOpenCrewSession,
  onOpenProjectHistory,
  onAddProject,
  onCreateTopic,
  onCreateWork,
  onRenameTopic,
  onTopicsChanged,
  refreshSignal,
  timeFilter,
  onTimeFilterChange,
  searchExpanded = true,
  searchFocusSignal = 0,
  showShortcutBadges = false,
  shortcutPlatform,
  onVisibleTopicsChange,
}: ProjectTreeProps) {
  const t = useT();
  const { showToast } = useToast();
  const compactTopics = variant === "workbench";
  const creationTopics = variant === "creation";
  const [tree, setTree] = useState<ProjectNode[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [manuallyCollapsed, setManuallyCollapsed] = useState<Set<string>>(new Set());
  const [creatingProject, setCreatingProject] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [editingTopic, setEditingTopic] = useState<string | null>(null);
  const [topicDraft, setTopicDraft] = useState("");
  const [menuTopic, setMenuTopic] = useState<string | null>(null);
  const [menuProject, setMenuProject] = useState<{ key: string; root: string; path: string; scope: "global" | "project"; label: string; mode?: "actions" | "color" } | null>(null);
  const [menuPoint, setMenuPoint] = useState<ContextMenuPoint | null>(null);
  const [editingProject, setEditingProject] = useState<{ key: string; root: string } | null>(null);
  const [projectDraft, setProjectDraft] = useState("");
  const [addingProject, setAddingProject] = useState(false);
  const [confirmAction, setConfirmAction] = useState<{ targetKey: string; action: "trash" } | null>(null);
  const [confirmRemoveProject, setConfirmRemoveProject] = useState<string | null>(null);
  const [dragProjectRoot, setDragProjectRoot] = useState<string | null>(null);
  const [dropProject, setDropProject] = useState<{ root: string; position: ProjectDropPosition } | null>(null);
  const [collapseSnapshot, setCollapseSnapshot] = useState<CollapseSnapshot | null>(null);
  const [platform, setPlatform] = useState("");
  const [workbenchOrganizeMode, setWorkbenchOrganizeMode] = useState<WorkbenchOrganizeMode>(loadWorkbenchOrganizeMode);
  const [workbenchSortMode] = useState<WorkbenchSortMode>(loadWorkbenchSortMode);
  const [workbenchRecentSettings, setWorkbenchRecentSettings] = useState<WorkbenchRecentSettings>(loadWorkbenchRecentSettings);
  const [recentSettingsOpen, setRecentSettingsOpen] = useState(false);
  const [readActivity, setReadActivity] = useState<ProjectTreeReadActivity>(loadReadActivity);
  const [unreadState, setUnreadState] = useState<UnreadState>(EMPTY_UNREAD_STATE);
  const recentSettingsRef = useRef<HTMLDivElement>(null);
  const recentSettingsTriggerRef = useRef<HTMLButtonElement>(null);
  const filterRef = useRef<HTMLDivElement>(null);
  const filterTriggerRef = useRef<HTMLButtonElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const topicIndexRef = useRef(0);
  const visibleTopicsCollectorRef = useRef<TopicShortcutEntry[]>([]);
  const [filterMenuOpen, setFilterMenuOpen] = useState(false);
  const creatingRef = useRef(false);
  const clickTimerRef = useRef<ProjectTreePendingTopicOpen | null>(null);
  useEffect(() => {
    return () => {
      if (clickTimerRef.current !== null) clearTimeout(clickTimerRef.current.timer);
    };
  }, []);
  const applyUnreadState = useCallback((next: UnreadState) => {
    setUnreadState((current) => next.summary.revision >= current.summary.revision ? next : current);
  }, []);
  useEffect(() => {
    let alive = true;
    const unsubscribe = onUnreadState((next) => {
      if (alive) applyUnreadState(next);
    });
    void app.UnreadState()
      .then((next) => {
        if (alive) applyUnreadState(next);
      })
      .catch((error) => {
        if (!alive) return;
        setUnreadState({
          ...EMPTY_UNREAD_STATE,
          error: error instanceof Error ? error.message : String(error),
        });
      });
    return () => {
      alive = false;
      unsubscribe();
    };
  }, [applyUnreadState]);
  const manuallyCollapsedRef = useRef(manuallyCollapsed);

  const closeMenu = useCallback(() => {
    setMenuTopic(null);
    setMenuProject(null);
    setMenuPoint(null);
    setConfirmAction(null);
    setConfirmRemoveProject(null);
  }, []);

  const updateWorkbenchRecentSettings = useCallback((patch: Partial<WorkbenchRecentSettings>) => {
    setWorkbenchRecentSettings((current) => {
      const next = parseWorkbenchRecentSettings({ ...current, ...patch });
      saveWorkbenchRecentSettings(next);
      return next;
    });
  }, []);

  const updateManuallyCollapsed = useCallback((updater: (prev: Set<string>) => Set<string>) => {
    setManuallyCollapsed((prev) => {
      const next = updater(prev);
      manuallyCollapsedRef.current = next;
      return next;
    });
  }, []);

  const refresh = useCallback(async () => {
    try {
      const nodes = await app.ListProjectTree();
      const list = asArray(nodes);
      setTree(list);
      setExpanded((prev) => {
        const next = new Set(prev);
        const collapsed = manuallyCollapsedRef.current;
        for (const key of defaultExpandedProjectTreeKeys(list, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)) {
          if (!collapsed.has(key)) next.add(key);
        }
        return next;
      });
    } catch {
      /* bridge unavailable */
    }
  }, [activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath]);

  useEffect(() => {
    manuallyCollapsedRef.current = manuallyCollapsed;
  }, [manuallyCollapsed]);

  const searchVisible = searchExpanded || query.trim().length > 0;

  useEffect(() => {
    if (!searchVisible || searchFocusSignal <= 0) return;
    searchInputRef.current?.focus();
  }, [searchFocusSignal, searchVisible]);

  useEffect(() => {
    void refresh();
  }, [refresh, refreshSignal]);

  const markNodeRead = useCallback((node: ProjectNode) => {
    const key = projectTreeReadActivityKey(node);
    const activityAt = topicActivityAt(node);
    if (!key || activityAt <= 0) return;
    setReadActivity((prev) => {
      if ((prev[key] ?? 0) >= activityAt) return prev;
      const next = { ...prev, [key]: activityAt };
      saveReadActivity(next);
      return next;
    });
  }, []);

  const markRemoteUnread = useCallback((node: ProjectNode) => {
    for (const conversation of projectTreeUnreadConversations(node, asArray(unreadState.summary.conversations))) {
      void app.MarkUnreadRead({
        conversationKey: conversation.key,
        upToSequence: conversation.latestSequence,
      }).then(applyUnreadState).catch((error) => {
        const message = error instanceof Error ? error.message : String(error);
        setUnreadState((current) => ({ ...current, error: message }));
        console.error("mark unread conversation read failed", error);
      });
    }
  }, [applyUnreadState, unreadState.summary.conversations]);

  useEffect(() => {
    if (tree.length === 0) return;
    try {
      if (localStorage.getItem(READ_ACTIVITY_INIT_KEY)) return;
    } catch {
      return;
    }
    const baseline: ProjectTreeReadActivity = {};
    const collectBaseline = (nodes: ProjectNode[]) => {
      for (const node of nodes) {
        if ((isTopicNode(node) || isRuntimeSessionNode(node)) && topicStatus(node) === "") {
          const key = projectTreeReadActivityKey(node);
          const activityAt = topicActivityAt(node);
          if (key && activityAt > 0) baseline[key] = Math.max(baseline[key] ?? 0, activityAt);
        }
        collectBaseline(asArray(node.children));
      }
    };
    collectBaseline(tree);
    try {
      localStorage.setItem(READ_ACTIVITY_INIT_KEY, "1");
    } catch {
      /* localStorage unavailable */
    }
    if (Object.keys(baseline).length === 0) return;
    setReadActivity((prev) => {
      const next = { ...prev };
      let changed = false;
      for (const [key, value] of Object.entries(baseline)) {
        if ((next[key] ?? 0) >= value) continue;
        next[key] = value;
        changed = true;
      }
      if (!changed) return prev;
      saveReadActivity(next);
      return next;
    });
  }, [tree]);

  useEffect(() => {
    const markActive = (nodes: ProjectNode[]) => {
      for (const node of nodes) {
        if (topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)) {
          markNodeRead(node);
          markRemoteUnread(node);
        }
        markActive(asArray(node.children));
      }
    };
    markActive(tree);
  }, [activeScope, activeSessionPath, activeTopicId, activeWorkspaceRoot, markNodeRead, markRemoteUnread, tree]);

  useEffect(() => {
    try {
      localStorage.setItem(WORKBENCH_ORGANIZE_KEY, workbenchOrganizeMode);
    } catch {
      /* ignore */
    }
  }, [workbenchOrganizeMode]);

  useEffect(() => {
    try {
      localStorage.setItem(WORKBENCH_SORT_KEY, workbenchSortMode);
    } catch {
      /* ignore */
    }
  }, [workbenchSortMode]);

  useEffect(() => {
    let cancelled = false;
    void app.Platform().then((value) => {
      if (!cancelled) setPlatform(value);
    }).catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  // Close the time-filter menu on outside click or Escape; move focus into the
  // menu on open and back to the trigger on Escape so it is keyboard-operable.
  useEffect(() => {
    if (!filterMenuOpen) return;
    const onMouseDown = (e: MouseEvent) => {
      if (filterRef.current && !filterRef.current.contains(e.target as Node)) setFilterMenuOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setFilterMenuOpen(false);
        filterTriggerRef.current?.focus();
      }
    };
    document.addEventListener("mousedown", onMouseDown);
    document.addEventListener("keydown", onKeyDown);
    const menu = filterRef.current?.querySelector<HTMLElement>(".project-tree__time-filter-menu");
    (menu?.querySelector<HTMLButtonElement>(".project-tree__time-filter-opt--on") ??
      menu?.querySelector<HTMLButtonElement>('[role="menuitem"]'))?.focus();
    return () => {
      document.removeEventListener("mousedown", onMouseDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [filterMenuOpen]);

  useEffect(() => {
    if (!recentSettingsOpen) return;
    const onMouseDown = (event: MouseEvent) => {
      if (recentSettingsRef.current && !recentSettingsRef.current.contains(event.target as Node)) setRecentSettingsOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setRecentSettingsOpen(false);
      recentSettingsTriggerRef.current?.focus();
    };
    document.addEventListener("mousedown", onMouseDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onMouseDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [recentSettingsOpen]);

  const moveMenuFocus = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    const items = Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'));
    if (items.length === 0) return;
    const current = items.indexOf(document.activeElement as HTMLButtonElement);
    const next = e.key === "Home" ? 0
      : e.key === "End" ? items.length - 1
      : e.key === "ArrowDown" ? (current + 1 + items.length) % items.length
      : (current - 1 + items.length) % items.length;
    items[next]?.focus();
  };

  const toggleExpand = (key: string) => {
    const willCollapse = expanded.has(key);
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
    updateManuallyCollapsed((prev) => {
      const next = new Set(prev);
      if (willCollapse) next.add(key);
      else next.delete(key);
      return next;
    });
  };

  const folderKeys = useMemo(() => collapsibleFolderKeys(tree), [tree]);
  const searchActive = query.trim().length > 0;
  const hasExpandedFolders = !searchActive && folderKeys.some((key) => expanded.has(key));
  const canRestoreCollapsedView = collapseSnapshot !== null;
  const canToggleCollapsedView = !searchActive && folderKeys.length > 0 && (hasExpandedFolders || canRestoreCollapsedView);
  const collapseToggleLabel = t(canRestoreCollapsedView ? "projectTree.restoreCollapsedTooltip" : "projectTree.collapseAllTooltip");
  const toggleCollapsedView = useCallback(() => {
    if (searchActive || folderKeys.length === 0) return;
    if (collapseSnapshot) {
      const currentFolderKeys = new Set(folderKeys);
      setExpanded(() => {
        const next = new Set<string>();
        for (const key of collapseSnapshot.expanded) {
          if (currentFolderKeys.has(key)) next.add(key);
        }
        return next;
      });
      updateManuallyCollapsed(() => {
        const next = new Set<string>();
        for (const key of collapseSnapshot.manuallyCollapsed) {
          if (currentFolderKeys.has(key)) next.add(key);
        }
        return next;
      });
      setCollapseSnapshot(null);
      return;
    }
    if (!hasExpandedFolders) return;
    setCollapseSnapshot({
      expanded: new Set(expanded),
      manuallyCollapsed: new Set(manuallyCollapsed),
    });
    setExpanded((prev) => {
      let changed = false;
      const next = new Set(prev);
      for (const key of folderKeys) {
        if (next.delete(key)) changed = true;
      }
      return changed ? next : prev;
    });
    updateManuallyCollapsed((prev) => {
      let changed = false;
      const next = new Set(prev);
      for (const key of folderKeys) {
        if (!next.has(key)) {
          next.add(key);
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [collapseSnapshot, expanded, folderKeys, hasExpandedFolders, manuallyCollapsed, searchActive, updateManuallyCollapsed]);

  const handleAddProject = async () => {
    if (addingProject) return;
    setAddingProject(true);
    try {
      await onAddProject();
      await refresh();
    } finally {
      setAddingProject(false);
    }
  };

  const handleCreateTopic = async (scope: string, workspaceRoot: string, key: string) => {
    if (creatingRef.current) return;
    creatingRef.current = true;
    setCreatingProject(key);
    setMenuProject(null);
    setMenuPoint(null);
    setExpanded((prev) => {
      const next = new Set(prev);
      next.add(key);
      return next;
    });
    updateManuallyCollapsed((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
    try {
      if (onCreateTopic) {
        await onCreateTopic(scope, workspaceRoot);
        await refresh();
        await onTopicsChanged?.();
        return;
      }
      const topic = await app.CreateTopic(scope, workspaceRoot, "");
      await refresh();
      await onTopicsChanged?.();
      await onOpenTopic(scope, workspaceRoot, topic.id);
    } catch {
      /* ignore */
    } finally {
      creatingRef.current = false;
      setCreatingProject(null);
    }
  };

  const startRenameTopic = (node: ProjectNode, label: string) => {
    setMenuTopic(null);
    setMenuProject(null);
    setMenuPoint(null);
    setConfirmAction(null);
    setEditingTopic(node.topicId ?? null);
    setTopicDraft(label);
  };

  const startRenameProject = (key: string, root: string, label: string) => {
    setMenuProject(null);
    setMenuTopic(null);
    setMenuPoint(null);
    setConfirmRemoveProject(null);
    setEditingProject({ key, root });
    setProjectDraft(label);
  };

  const commitRenameTopic = async (topicId: string) => {
    const title = topicDraft.trim();
    setEditingTopic(null);
    if (!title) return;
    try {
      if (onRenameTopic) await onRenameTopic(topicId, title);
      else await app.RenameTopic(topicId, title);
      await refresh();
      if (!onRenameTopic) await onTopicsChanged?.();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const commitRenameProject = async (root: string) => {
    const title = projectDraft.trim();
    setEditingProject(null);
    if (!title) return;
    try {
      await app.RenameProject(root, title);
      await refresh();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const trashTopic = async (topicId: string) => {
    try {
      await app.TrashTopic(topicId);
      setMenuTopic(null);
      setMenuPoint(null);
      setConfirmAction(null);
      await refresh();
      await onTopicsChanged?.();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const trashSession = async (path: string) => {
    try {
      await app.DeleteSession(path);
      setMenuTopic(null);
      setMenuPoint(null);
      setConfirmAction(null);
      await refresh();
      await onTopicsChanged?.();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const setTopicPinned = async (topicId: string, pinned: boolean) => {
    try {
      await app.SetTopicPinned(topicId, pinned);
      setMenuTopic(null);
      setMenuPoint(null);
      await refresh();
      await onTopicsChanged?.();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const setSessionPinned = async (path: string, pinned: boolean) => {
    if (!path) return;
    try {
      await app.SetSessionPinned(path, pinned);
      await refresh();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const setProjectPinned = async (workspaceRoot: string, pinned: boolean) => {
    if (!workspaceRoot) return;
    try {
      await app.SetProjectPinned(workspaceRoot, pinned);
      setMenuProject(null);
      setMenuPoint(null);
      await refresh();
      await onTopicsChanged?.();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const copyProjectPath = async (path: string) => {
    if (!path) return;
    try {
      await navigator.clipboard?.writeText(path);
    } catch {
      /* ignore */
    }
  };

  const removeProject = async (path: string) => {
    if (!path) return;
    try {
      await app.RemoveWorkspace(path);
      setMenuProject(null);
      setMenuPoint(null);
      setConfirmRemoveProject(null);
      await refresh();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const setProjectColor = async (path: string, color: string) => {
    try {
      await app.SetProjectColor(path, color);
      setMenuProject(null);
      setMenuPoint(null);
      await refresh();
      await onTopicsChanged?.();
    } catch {
      /* ignore */
    }
  };

  const visibleTree = useMemo(() => {
    const q = query.trim().toLowerCase();
    // Time filter: compute cutoff timestamp.
    const diff = timeFilter === "1h" ? 60 * 60 * 1000
      : timeFilter === "3h" ? 3 * 60 * 60 * 1000
      : timeFilter === "5h" ? 5 * 60 * 60 * 1000
      : timeFilter === "1d" ? 24 * 60 * 60 * 1000
      : 0;
    const nthLatestActivity = (n: number): number | null => {
      const times = new Set<number>();
      const collect = (nodes: ProjectNode[]) => {
        for (const node of nodes) {
          if (node.kind === "topic" || node.kind === "global_topic") times.add(topicActivityTime(node));
          collect(asArray(node.children));
        }
      };
      collect(tree);
      const sorted = [...times].sort((a, b) => b - a);
      return sorted.length === 0 ? null : sorted[Math.min(n, sorted.length) - 1];
    };
    const cutoff: number | null = timeFilter === "all" ? null
      : timeFilter === "10" ? nthLatestActivity(10)
      : timeFilter === "20" ? nthLatestActivity(20)
      : Date.now() - diff;
    const topicMatchesTime = (node: ProjectNode) => {
      if (cutoff === null) return true;
      return topicActivityTime(node) >= cutoff;
    };
    const matchesQuery = (node: ProjectNode) =>
      [node.label, node.root, node.topicId].some((value) => (value ?? "").toLowerCase().includes(q));
    const filterNode = (node: ProjectNode): ProjectNode | null => {
      // For folder nodes: always show when time filter is active (so the tree structure remains navigable).
      const isFolder = node.kind === "project" || node.kind === "global_folder" || node.kind === "crew_folder";
      const children = asArray(node.children)
        .map(filterNode)
        .filter((child): child is ProjectNode => child !== null);
      if (isFolder) {
        if (cutoff !== null && children.length === 0 && !matchesQuery(node) && q === "") return null;
        if (children.length > 0 || matchesQuery(node)) return { ...node, children };
        if (q) return null;
        // With only time filter, show folder if it has any child that matches the time.
        const hasTimeMatch = asArray(node.children).some((c) => topicMatchesTime(c));
        return hasTimeMatch ? { ...node, children: asArray(node.children).filter(topicMatchesTime) } : null;
      }
      if (!q && cutoff === null) return node;
      if (cutoff !== null && !topicMatchesTime(node)) return null;
      if (q && !matchesQuery(node)) return null;
      return node;
    };
    const filtered = tree
      .map(filterNode)
      .filter((node): node is ProjectNode => node !== null);
    return compactTopics ? arrangeWorkbenchTree(filtered, workbenchOrganizeMode, workbenchSortMode) : filtered;
  }, [compactTopics, query, tree, timeFilter, workbenchOrganizeMode, workbenchSortMode]);

  const workbenchTreeSections = useMemo<WorkbenchTreeSections>(() => {
    if (!compactTopics) return { recent: [], projects: visibleTree };
    return splitWorkbenchRecentTree(visibleTree, workbenchSortMode, workbenchRecentSettings);
  }, [compactTopics, visibleTree, workbenchRecentSettings, workbenchSortMode]);

  const projectDragEnabled = query.trim() === "";

  const commitProjectReorder = useCallback(async (draggedRoot: string, targetRoot: string, position: ProjectDropPosition) => {
    const nextRoots = reorderedProjectRoots(tree, draggedRoot, targetRoot, position);
    const currentRoots = projectRoots(tree);
    if (nextRoots.join("\n") === currentRoots.join("\n")) return;
    if (compactTopics && workbenchOrganizeMode !== "project") setWorkbenchOrganizeMode("project");
    setTree((current) => applyProjectOrder(current, nextRoots));
    try {
      await app.ReorderProjects(nextRoots);
      await refresh();
      await onTopicsChanged?.();
    } catch {
      await refresh();
    }
  }, [compactTopics, onTopicsChanged, refresh, tree, workbenchOrganizeMode]);

  const clearProjectDrag = useCallback(() => {
    setDragProjectRoot(null);
    setDropProject(null);
  }, []);

  useEffect(() => {
    if (!dragProjectRoot) return;
    window.addEventListener("dragend", clearProjectDrag);
    window.addEventListener("drop", clearProjectDrag);
    window.addEventListener("blur", clearProjectDrag);
    return () => {
      window.removeEventListener("dragend", clearProjectDrag);
      window.removeEventListener("drop", clearProjectDrag);
      window.removeEventListener("blur", clearProjectDrag);
    };
  }, [clearProjectDrag, dragProjectRoot]);

  const activeAncestorKeys = useMemo(
    () => activeSessionAncestorKeys(tree, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath),
    [activeScope, activeSessionPath, activeTopicId, activeWorkspaceRoot, tree],
  );

  const activeRowKey = useMemo(
    () => projectTreeActiveKey(tree, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath),
    [activeScope, activeSessionPath, activeTopicId, activeWorkspaceRoot, tree],
  );

  useEffect(() => {
    if (activeAncestorKeys.length === 0) return;
    setExpanded((prev) => {
      let changed = false;
      const next = new Set(prev);
      for (const key of activeAncestorKeys) {
        if (manuallyCollapsed.has(key) || next.has(key)) continue;
        next.add(key);
        changed = true;
      }
      return changed ? next : prev;
    });
  }, [activeAncestorKeys, manuallyCollapsed]);

  const renderNode = (node: ProjectNode | null | undefined, depth: number, section: "recent" | "projects" = "projects", isVisible = true) => {
    if (!node) return null;
    const key = projectNodeKey(node, depth);
    const children = asArray(node.children);
    const isExpanded = query.trim() ? true : expanded.has(key);
    const hasChildren = children.length > 0;
    const folderDisclosure = projectTreeFolderDisclosure(hasChildren, isExpanded);

    if (isTopicNode(node) || isRuntimeSessionNode(node) || isCrewSessionNode(node)) {
      const isSessionNode = isRuntimeSessionNode(node) || isCrewSessionNode(node);
      const openRequest = isCrewSessionNode(node) ? null : projectTreeTopicOpenRequest(node);
      const scope = isCrewSessionNode(node) ? "global" : (openRequest?.scope ?? "project");
      const scopeClass = scope === "global" ? " project-tree__topic--global" : " project-tree__topic--project";
      const accentStyle = isCrewSessionNode(node) ? undefined : projectAccentStyle(node.projectColor, scope === "global" ? "var(--project-tree-global-accent)" : undefined);
      const active = section !== "recent" && key === activeRowKey;
      const label = (node.label || node.topicId || "Untitled").replace(/^●\s*/, "");
      const activityAt = node.lastActivityAt || node.createdAt || 0;
      const sideTimeVisible = compactTopics || creationTopics;
      const timeLabel = sideTimeVisible && activityAt ? topicActivityLabel(activityAt, t, true) : "";
      const exactTimeLabel = sideTimeVisible && activityAt ? topicActivityDateLabel(activityAt) : "";
      const meta = topicMetaLine(node, t, compactTopics);
      const status = topicStatus(node);
      const statusLabel = topicStatusLabel(node, t);
      const showStatusInSide = status === "thinking" || status === "streaming" || status === "waiting_confirmation" || status === "background_job";
      const unread = isCrewSessionNode(node) ? false : projectTreeTopicHasUnreadActivity(node, readActivity, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath);
      const remoteUnreadCount = section === "recent"
        ? projectTreeUnreadCount(node, asArray(unreadState.summary.conversations))
        : 0;
      const visualState = projectTreeTopicVisualState(node, unread, status);
      const workSession = node.sessionKind === "work";
      const collaborationSession = node.sessionKind === "collaboration";
      const topicId = node.topicId ?? "";
      const imSource = scope === "global" && topicId ? imTopicSources[topicId] : undefined;
      const imSourceLabel = imSource?.label || "";
      const imSourceTitle = imSourceLabel ? t("msg.fromIm", { source: imSourceLabel }) : "";
      const imSourcePlatform = (imSource?.platform || "im").replace(/[^a-z0-9_-]/gi, "").toLowerCase() || "im";
      const sourceBadge = collaborationSession ? null : projectTreeSourceBadge(node, t);
      const title = [label, imSourceTitle, sourceBadge?.title, statusLabel, meta, exactTimeLabel].filter(Boolean).join(" · ");
      const menuKey = projectTreeMenuKey(section, key);
      const trashTarget = projectTreeTrashTarget(node);
      const trashTargetKey = trashTarget?.kind === "topic" ? `topic:${trashTarget.topicId}` : trashTarget ? `session:${trashTarget.path}` : "";
      const topicMenuOpen = Boolean(trashTarget) && menuTopic === menuKey;
      const pinned = Boolean(node.pinned);
      const pinLabel = t(pinned ? "projectTree.unpinTopic" : "projectTree.pinTopic");
      const openTopicMenu = (event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>) => {
        if (!trashTarget) return;
        event.preventDefault();
        event.stopPropagation();
        setMenuProject(null);
        setConfirmRemoveProject(null);
        setMenuPoint(contextMenuPointFromEvent(event));
        setMenuTopic(menuKey);
        setConfirmAction(null);
      };
      const trashMenuItem: ContextMenuItem = {
        key: "trash",
        icon: <Archive size={13} />,
        label: confirmAction?.targetKey === trashTargetKey && confirmAction.action === "trash" ? t("history.confirmMoveToTrash") : t("history.moveToTrash"),
        danger: true,
        onSelect: () => {
          if (!trashTarget) return;
          if (confirmAction?.targetKey === trashTargetKey && confirmAction.action === "trash") {
            if (trashTarget.kind === "topic") void trashTopic(trashTarget.topicId);
            else void trashSession(trashTarget.path);
          } else {
            setConfirmAction({ targetKey: trashTargetKey, action: "trash" });
          }
        },
      };
      const topicMenuItems: ContextMenuItem[] = isSessionNode ? [trashMenuItem] : [
        ...(compactTopics
          ? [
              {
                key: pinned ? "unpin" : "pin",
                icon: <Pin size={13} />,
                label: pinLabel,
                onSelect: () => void setTopicPinned(topicId, !pinned),
              },
            ]
          : []),
        {
          key: "rename",
          icon: <Pencil size={13} />,
          label: t("projectTree.renameTopic"),
          onSelect: () => startRenameTopic(node, label),
        },
        trashMenuItem,
      ];
      if (!isSessionNode && editingTopic === topicId) {
        return (
          <div
            key={key}
            className={`project-tree__topic project-tree__topic--editing${active ? " project-tree__topic--active" : ""}${imSource ? " project-tree__topic--im-source" : ""}${meta ? " project-tree__topic--has-meta" : ""}`}
            style={{ paddingLeft: 14 + depth * 16 }}
          >
            <input
              autoFocus
              className="project-tree__topic-input"
              value={topicDraft}
              onChange={(event) => setTopicDraft(event.target.value)}
              onFocus={(event) => event.target.select()}
              onKeyDown={(event) => {
                if (event.key === "Enter") void commitRenameTopic(topicId);
                if (event.key === "Escape") setEditingTopic(null);
              }}
              onBlur={() => void commitRenameTopic(topicId)}
            />
          </div>
        );
      }
      const shortcutIndex = showShortcutBadges && isVisible && topicIndexRef.current < 9 ? topicIndexRef.current + 1 : 0;
      if (shortcutIndex > 0) topicIndexRef.current++;
      // Collect visible topics in render order for shortcut navigation
      if (openRequest && isVisible) {
        visibleTopicsCollectorRef.current.push({
          scope: openRequest.scope,
          workspaceRoot: openRequest.workspaceRoot,
          topicId: openRequest.topicId,
          sessionPath: openRequest.sessionPath,
        });
      }
      const row = (
        <div
          className={`project-tree__topic${scopeClass}${isSessionNode ? " project-tree__topic--session" : ""}${workSession ? " project-tree__topic--work-session" : ""}${collaborationSession ? " project-tree__topic--collaboration-session" : ""}${active ? " project-tree__topic--active" : ""}${node.running ? " project-tree__topic--running" : ""}${status ? ` project-tree__topic--status-${status}` : ""}${visualState !== "none" ? ` project-tree__topic--visual-${visualState}` : ""}${sourceBadge ? " project-tree__topic--external-source" : ""}${unread ? " project-tree__topic--unread" : ""}${!isSessionNode && pinned ? " project-tree__topic--pinned" : ""}${topicMenuOpen ? " project-tree__topic--menu-open" : ""}${sideTimeVisible && (timeLabel || showStatusInSide) ? " project-tree__topic--with-side" : meta ? " project-tree__topic--has-meta" : ""}${imSource ? " project-tree__topic--im-source" : ""}${shortcutIndex > 0 ? " project-tree__topic--show-shortcut" : ""}`}
          style={accentStyle}
          onContextMenu={trashTarget ? openTopicMenu : undefined}
        >
          <button
            type="button"
            className="project-tree__topic-main"
            title={title}
            style={{ paddingLeft: 14 + depth * 16 }}
            aria-current={active ? "page" : undefined}
            onClick={() => {
              if (isCrewSessionNode(node)) {
                markNodeRead(node);
                void onOpenCrewSession?.(node.sessionPath ?? "");
                return;
              }
              if (!openRequest) return;
              const nextClick = { rowKey: key, canRename: !isSessionNode };
              const pending = clickTimerRef.current;
              if (pending !== null) {
                clearTimeout(pending.timer);
                clickTimerRef.current = null;
                if (projectTreeShouldSuppressOpenForRename(pending, nextClick)) return;
              }
              const timer = setTimeout(() => {
                if (clickTimerRef.current?.timer === timer) clickTimerRef.current = null;
                markNodeRead(node);
                onOpenTopic(openRequest.scope, openRequest.workspaceRoot, openRequest.topicId, openRequest.sessionPath, openRequest.runtimeHint);
              }, 200);
              clickTimerRef.current = { ...nextClick, timer };
            }}
            onKeyDown={(event) => {
              if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
                openTopicMenu(event);
              }
            }}
            onDoubleClick={(event) => {
              if (isSessionNode) return;
              event.stopPropagation();
              if (clickTimerRef.current !== null && clickTimerRef.current.rowKey === key) {
                clearTimeout(clickTimerRef.current.timer);
                clickTimerRef.current = null;
              }
              startRenameTopic(node, label);
            }}
          >
            {compactTopics && section === "recent" && remoteUnreadCount > 0 ? (
              <span
                className="project-tree__topic-unread-count"
                title={t("projectTree.unreadCount", { n: remoteUnreadCount })}
                aria-label={t("projectTree.unreadCount", { n: remoteUnreadCount })}
              >
                {remoteUnreadCount > 99 ? "99+" : remoteUnreadCount}
              </span>
            ) : compactTopics && (visualState !== "none" || section === "recent") && (
              <span
                className={`project-tree__topic-visual project-tree__topic-visual--${visualState === "none" ? "recent" : visualState}`}
                title={visualState === "none" ? undefined : visualState === "failed" ? statusLabel : visualState === "running" ? statusLabel || t("projectTree.running") : t("projectTree.status.done")}
                aria-hidden="true"
              />
            )}
            {workSession && (
              <span className="project-tree__work-icon" title={t("projectTree.workSession")} aria-label={t("projectTree.workSession")}>
                <BriefcaseBusiness size={12} aria-hidden="true" />
              </span>
            )}
            {collaborationSession && (
              <span className="project-tree__work-icon" title={t("collab.title")} aria-label={t("collab.title")}>
                <Users size={12} aria-hidden="true" />
              </span>
            )}
            {!workSession && !collaborationSession && !sourceBadge && (
              <span className="project-tree__session-icon" aria-hidden="true">
                <MessageSquare size={13} />
              </span>
            )}
            <span className="project-tree__topic-copy">
              <span className="project-tree__topic-heading">
                {compactTopics && sourceBadge && (
                  <span className={`project-tree__topic-origin ${sourceBadge.className}`} title={sourceBadge.title}>
                    {sourceBadge.label}
                  </span>
                )}
                <span className="project-tree__topic-label">{label}</span>
                {imSource && (
                  <span
                    className={`project-tree__topic-im project-tree__topic-im--${imSourcePlatform}`}
                    title={imSourceTitle}
                    aria-label={imSourceTitle}
                  >
                    <MessageSquare size={11} />
                    <span>{imSourceLabel}</span>
                  </span>
                )}
                {!compactTopics && statusLabel && <span className={`project-tree__topic-status project-tree__topic-status--${status}`}>{statusLabel}</span>}
              </span>
              {!compactTopics && !creationTopics && meta && (
                <span className="project-tree__topic-meta">
                  <span className="project-tree__topic-meta-text">{meta}</span>
                </span>
              )}
            </span>
            {compactTopics && section === "recent" && recentProjectLabel(node) && (
              <span className="project-tree__topic-project" aria-hidden="true">
                {recentProjectLabel(node)}
              </span>
            )}
            {sideTimeVisible && !compactTopics && (
              <span className={`project-tree__topic-side${!timeLabel && !showStatusInSide ? " project-tree__topic-side--empty" : ""}`} aria-hidden="true">
                {showStatusInSide && <span className={`project-tree__topic-state project-tree__topic-state--${status}`} title={statusLabel} />}
                {timeLabel && <span className="project-tree__topic-time">{timeLabel}</span>}
              </span>
            )}
            {compactTopics && showStatusInSide && statusLabel && (
              <span className="sr-only">
                {statusLabel}
              </span>
            )}
            {compactTopics && !showStatusInSide && meta && (
              <span className="sr-only">
                {meta}
              </span>
            )}
          </button>
          {!compactTopics && unread && <span className="project-tree__topic-unread-dot" aria-hidden="true" />}
          {(section === "recent" || !compactTopics) && projectTreeShouldRenderTopicActions(compactTopics, unread) && (
            <span className="project-tree__topic-actions" aria-label={t("projectTree.topicActions")}>
              {section === "recent" && !workSession && <Tooltip label={pinLabel} side="top" className="project-tree__topic-action-slot">
                <button
                  className={`project-tree__topic-action${pinned ? " project-tree__topic-action--pinned" : ""}`}
                  type="button"
                  aria-label={pinLabel}
                  aria-pressed={pinned}
                  onClick={(event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    if (isCrewSessionNode(node)) void setSessionPinned(node.sessionPath ?? "", !pinned);
                    else void setTopicPinned(topicId, !pinned);
                  }}
                >
                  <Pin size={15} aria-hidden="true" />
                </button>
              </Tooltip>}
              {trashTarget && <Tooltip label={t("projectTree.topicActions")} side="top" className="project-tree__topic-action-slot">
                <button
                  className="project-tree__topic-action project-tree__topic-action--more"
                  type="button"
                  aria-label={t("projectTree.topicActions")}
                  aria-haspopup="menu"
                  aria-expanded={topicMenuOpen}
                  onClick={openTopicMenu}
                >
                  <MoreHorizontal size={16} aria-hidden="true" />
                </button>
              </Tooltip>}
            </span>
          )}
          {trashTarget && (
            <ContextMenu
              open={topicMenuOpen}
              point={menuPoint}
              items={topicMenuItems}
              minWidth={178}
              ariaLabel={t("projectTree.topicActions")}
              onClose={closeMenu}
            />
          )}
          {shortcutIndex > 0 && (
            <span className="project-tree__topic-shortcut" aria-hidden="true">
              {topicShortcutLabel(shortcutIndex, shortcutPlatform)}
            </span>
          )}
        </div>
      );
      return (
        <div key={key}>
          {row}
          {hasChildren && (
            <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
              <div className="project-tree__children-inner">
                {children.map((child) => renderNode(child, depth + 1, section, isVisible && isExpanded))}
              </div>
            </div>
          )}
        </div>
      );
    }

    // Crew folder: virtual bot-session container.
    if (node.kind === "crew_folder") {
      const scopeClass = " project-tree__folder--global";
      const crewLabel = node.label || "Crew";
      const projectActive = activeAncestorKeys.includes(key);
      const folderClass = `project-tree__folder project-tree__folder--crew${scopeClass}${isExpanded ? " project-tree__folder--expanded" : ""}${projectActive ? " project-tree__folder--active" : ""}`;
      return (
        <div key={key}>
          <div
            className={folderClass}
            style={{ "--project-accent": projectAccentFallback(node) } as CSSProperties}
          >
            <button
              type="button"
              className="project-tree__folder-main"
              style={{ paddingLeft: 8 + depth * 16 }}
              title={crewLabel}
              onClick={() => { if (folderDisclosure.canExpand) toggleExpand(key); }}
              aria-expanded={folderDisclosure.ariaExpanded}
            >
              <span className="project-tree__folder-chevron" aria-hidden="true">
                {folderDisclosure.isOpen ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
              </span>
              <span className="project-tree__folder-color" aria-hidden="true" />
              {folderDisclosure.isOpen ? <FolderOpen size={16} className="project-tree__folder-icon" /> : <Folder size={16} className="project-tree__folder-icon" />}
              <span className="project-tree__folder-heading">
                <span className="project-tree__folder-label">{crewLabel}</span>
              </span>
              <span className="project-tree__folder-count" aria-label={`${children.length}`}>{children.length}</span>
            </button>
          </div>
          {hasChildren && (
            <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
              <div className="project-tree__children-inner">
                {children.map((child) => renderNode(child, depth + 1, section, isVisible && isExpanded))}
              </div>
            </div>
          )}
        </div>
      );
    }

    const scope = node.kind === "global_folder" ? "global" : "project";
    const scopeClass = scope === "global" ? " project-tree__folder--global" : " project-tree__folder--project";
    const pinnedClass = node.pinned ? " project-tree__folder--pinned" : "";
    const accentStyle = projectAccentStyle(node.projectColor, projectAccentFallback(node));
    const projectRoot = scope === "global" ? "" : node.root ?? "";
    const projectDragKey = scope === "global" ? GLOBAL_PROJECT_ORDER_KEY : projectRoot;
    const projectPath = node.root ?? "";
    const colorTargetRoot = scope === "global" ? "" : projectPath;
    const projectLabel = node.label || (scope === "global" ? "Global" : "Untitled");
    const projectPinned = Boolean(node.pinned);
    const projectActive = activeAncestorKeys.includes(key) || (activeScope === scope && (scope === "global" || activeWorkspaceRoot === node.root));
    const projectMenuOpen = menuProject?.key === key;
    const activeTopicInProject = Boolean(activeTopicId) && activeScope === scope && (scope === "global" || activeWorkspaceRoot === projectRoot);
    const draggableProject = section !== "recent" && projectDragEnabled && depth === 0 && Boolean(projectDragKey) && editingProject?.key !== key;
    const projectDropPosition = dropProject?.root === projectDragKey ? dropProject.position : null;
    const handleProjectDragStart = (event: ReactDragEvent<HTMLElement>) => {
      if (!draggableProject) return;
      const target = event.target;
      if (target instanceof Element && target.closest(".project-tree__action-slot,.project-tree__folder-action-slot")) {
        event.preventDefault();
        return;
      }
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", projectDragKey);
      setDragProjectRoot(projectDragKey);
      setDropProject(null);
    };
    const handleProjectDragOver = (event: ReactDragEvent<HTMLDivElement>) => {
      if (!draggableProject || !dragProjectRoot || dragProjectRoot === projectDragKey) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      const rect = event.currentTarget.getBoundingClientRect();
      const position: ProjectDropPosition = event.clientY < rect.top + rect.height / 2 ? "before" : "after";
      setDropProject((current) => {
        if (current?.root === projectDragKey && current.position === position) return current;
        return { root: projectDragKey, position };
      });
    };
    const handleProjectDrop = (event: ReactDragEvent<HTMLDivElement>) => {
      if (!draggableProject) return;
      const draggedRoot = dragProjectRoot || event.dataTransfer.getData("text/plain");
      const position = dropProject?.root === projectDragKey ? dropProject.position : "after";
      event.preventDefault();
      clearProjectDrag();
      if (draggedRoot && draggedRoot !== projectDragKey) void commitProjectReorder(draggedRoot, projectDragKey, position);
    };
    const openProjectMenu = (event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>, mode: "actions" | "color" = "actions") => {
      event.preventDefault();
      event.stopPropagation();
      setMenuTopic(null);
      setConfirmAction(null);
      setMenuPoint(contextMenuPointFromEvent(event));
      setMenuProject({ key, root: projectRoot, path: projectPath, scope, label: projectLabel, mode });
      setConfirmRemoveProject(null);
    };
    const projectColorMenuItems: ContextMenuItem[] = [
      {
        key: "visual-label-heading",
        label: t("projectTree.visualLabel"),
        variant: "section" as const,
        disabled: true,
        onSelect: () => {},
      },
      ...PROJECT_COLOR_OPTIONS.map((option): ContextMenuItem => ({
        key: `color-${option.key || "default"}`,
        label: colorMenuLabel(projectColorLabel(t, option.key), option.key, (node.projectColor || "") === option.key),
        onSelect: () => {
          void setProjectColor(colorTargetRoot, option.key);
        },
      })),
    ];
    const projectMenuItems: ContextMenuItem[] = [
      {
        key: "new-session",
        icon: <Plus size={13} />,
        label: t("projectTree.newTopic"),
        onSelect: () => {
          void handleCreateTopic(scope, projectRoot, key);
        },
      },
      ...(scope === "project"
        ? [
            {
              key: "project-history",
              icon: <History size={13} />,
              label: t("projectTree.projectHistory"),
              onSelect: () => {
                closeMenu();
                void onOpenProjectHistory(scope, projectRoot);
              },
            },
          ]
        : []),
      {
        key: "rename",
        icon: <Pencil size={13} />,
        label: t("projectTree.renameProject"),
        onSelect: () => startRenameProject(key, projectRoot, projectLabel),
      },
      { type: "separator" as const, key: "color-separator" },
      ...projectColorMenuItems,
      { type: "separator" as const, key: "path-separator" },
      {
        key: "reveal",
        icon: <FolderOpen size={13} />,
        label: t(revealLabelKey(platform)),
        disabled: !projectPath,
        onSelect: () => {
          void app.RevealPath(projectPath).catch(() => {});
          closeMenu();
        },
      },
      {
        key: "copy-path",
        icon: <Copy size={13} />,
        label: t("projectTree.copyPath"),
        disabled: !projectPath,
        onSelect: () => {
          void copyProjectPath(projectPath);
          closeMenu();
        },
      },
      ...(scope === "project"
        ? [
            { type: "separator" as const, key: "remove-separator" },
            {
              key: "remove",
              icon: <XCircle size={13} />,
              label: confirmRemoveProject === key ? t("projectTree.confirmRemoveProject") : t("projectTree.removeProject"),
              danger: true,
              onSelect: () => {
                if (confirmRemoveProject === key) void removeProject(projectPath);
                else setConfirmRemoveProject(key);
              },
            },
          ]
        : []),
    ];
    const workbenchProjectMenuItems: ContextMenuItem[] = [
      {
        key: "new-session",
        icon: <Plus size={13} />,
        label: t("projectTree.newTopic"),
        onSelect: () => {
          void handleCreateTopic(scope, projectRoot, key);
        },
      },
      ...(onCreateWork
        ? [
            {
              key: "new-work",
              icon: <BriefcaseBusiness size={13} />,
              label: t("projectTree.newWorkTooltip"),
              onSelect: () => {
                void onCreateWork(scope, projectRoot);
              },
            },
          ]
        : []),
      { type: "separator" as const, key: "create-separator" },
      ...(scope === "project"
        ? [
            {
              key: projectPinned ? "unpin-project" : "pin-project",
              icon: <Pin size={13} />,
              label: t(projectPinned ? "projectTree.unpinProject" : "projectTree.pinProject"),
              onSelect: () => {
                void setProjectPinned(projectRoot, !projectPinned);
              },
            },
          ]
        : []),
      {
        key: "reveal",
        icon: <FolderOpen size={13} />,
        label: t(revealLabelKey(platform)),
        disabled: !projectPath,
        onSelect: () => {
          void app.RevealPath(projectPath).catch(() => {});
          closeMenu();
        },
      },
      ...(scope === "project"
        ? [
            {
              key: "project-history",
              icon: <History size={13} />,
              label: t("projectTree.projectHistory"),
              onSelect: () => {
                closeMenu();
                void onOpenProjectHistory(scope, projectRoot);
              },
            },
          ]
        : []),
      {
        key: "rename",
        icon: <Pencil size={13} />,
        label: t("projectTree.renameProjectWorkbench"),
        onSelect: () => startRenameProject(key, projectRoot, projectLabel),
      },
      { type: "separator" as const, key: "visual-separator" },
      ...projectColorMenuItems,
      {
        key: "archive-active-topic",
        icon: <Archive size={13} />,
        label: activeTopicId && confirmAction?.targetKey === `topic:${activeTopicId}` && confirmAction.action === "trash"
          ? t("history.confirmMoveToTrash")
          : t("projectTree.archiveConversation"),
        disabled: !activeTopicInProject || !activeTopicId,
        danger: true,
        onSelect: () => {
          if (!activeTopicId) return;
          if (confirmAction?.targetKey === `topic:${activeTopicId}` && confirmAction.action === "trash") void trashTopic(activeTopicId);
          else setConfirmAction({ targetKey: `topic:${activeTopicId}`, action: "trash" });
        },
      },
      ...(scope === "project"
        ? [
            { type: "separator" as const, key: "remove-separator" },
            {
              key: "remove",
              icon: <XCircle size={13} />,
              label: confirmRemoveProject === key ? t("projectTree.confirmRemoveProjectShort") : t("projectTree.removeProjectShort"),
              danger: true,
              onSelect: () => {
                if (confirmRemoveProject === key) void removeProject(projectPath);
                else setConfirmRemoveProject(key);
              },
            },
          ]
        : []),
    ];

    if (editingProject?.key === key) {
      return (
        <div key={key} className="project-tree__project-wrapper">
          <div
            className={`project-tree__folder project-tree__folder--editing${projectActive ? " project-tree__folder--active" : ""}`}
            style={{ paddingLeft: 8 + depth * 16 }}
          >
            <input
              autoFocus
              className="project-tree__folder-input"
              value={projectDraft}
              onChange={(event) => setProjectDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void commitRenameProject(projectRoot);
                if (event.key === "Escape") setEditingProject(null);
              }}
              onBlur={() => void commitRenameProject(projectRoot)}
            />
          </div>
          {hasChildren && (
            <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
              <div className="project-tree__children-inner">
                {children.map((child) => renderNode(child, depth + 1, section, isVisible && isExpanded))}
              </div>
            </div>
          )}
        </div>
      );
    }

    return (
      <div key={key} className="project-tree__project-wrapper">
        <div
          className={`project-tree__folder${scopeClass}${pinnedClass} project-tree__folder--has-color${draggableProject ? " project-tree__folder--draggable" : ""}${projectActive ? " project-tree__folder--active" : ""}${projectMenuOpen ? " project-tree__folder--menu-open" : ""}${dragProjectRoot === projectDragKey ? " project-tree__folder--dragging" : ""}${projectDropPosition ? ` project-tree__folder--drop-${projectDropPosition}` : ""}`}
          style={accentStyle}
          draggable={draggableProject}
          aria-grabbed={draggableProject ? dragProjectRoot === projectRoot : undefined}
          onDragStart={handleProjectDragStart}
          onDragOver={handleProjectDragOver}
          onDragLeave={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropProject(null);
          }}
          onDrop={handleProjectDrop}
          onDragEnd={clearProjectDrag}
          onContextMenu={openProjectMenu}
        >
          <button
            type="button"
            className="project-tree__folder-main"
            title={projectLabel}
            style={{ paddingLeft: 8 + depth * 16 }}
            onClick={() => {
              if (folderDisclosure.canExpand) toggleExpand(key);
            }}
            onKeyDown={(event) => {
              if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
                openProjectMenu(event);
              }
            }}
            aria-expanded={folderDisclosure.ariaExpanded}
          >
            <span className="project-tree__folder-chevron" aria-hidden="true">
              {folderDisclosure.isOpen ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
            </span>
            <span className="project-tree__folder-color" aria-hidden="true" />
            {folderDisclosure.isOpen ? <FolderOpen size={16} className="project-tree__folder-icon" /> : <Folder size={16} className="project-tree__folder-icon" />}
            <span className={`project-tree__folder-label${!hasChildren ? " project-tree__folder-label--empty" : ""}`}>{projectLabel}</span>
            <span className="project-tree__folder-count" aria-label={`${children.length}`}>{children.length}</span>
          </button>
          {creationTopics && (
            <Tooltip label={t("projectTree.newTopicTooltip")} className="project-tree__action-slot">
              <button
                type="button"
                className="project-tree__new-topic"
                aria-label={t("projectTree.newTopicTooltip")}
                disabled={creatingProject !== null}
                onClick={(e) => {
                  e.stopPropagation();
                  void handleCreateTopic(scope, projectRoot, key);
                }}
              >
                <Plus size={12} aria-hidden="true" />
              </button>
            </Tooltip>
          )}
          {creationTopics && onCreateWork && (
            <Tooltip label={t("projectTree.newWorkTooltip")} className="project-tree__action-slot">
              <button
                type="button"
                className="project-tree__new-topic project-tree__new-work"
                aria-label={t("projectTree.newWorkTooltip")}
                disabled={creatingProject !== null}
                onClick={(e) => {
                  e.stopPropagation();
                  void onCreateWork(scope, projectRoot);
                }}
              >
                <BriefcaseBusiness size={12} aria-hidden="true" />
              </button>
            </Tooltip>
          )}
          {compactTopics && (
            <div className="project-tree__workspace-actions" aria-label={t("projectTree.projectActions")}>
              <Tooltip label={t("projectTree.projectActions")} className="project-tree__workspace-action-slot">
                <button
                  type="button"
                  className="project-tree__workspace-action"
                  aria-label={t("projectTree.projectActions")}
                  aria-haspopup="menu"
                  aria-expanded={projectMenuOpen}
                  onClick={openProjectMenu}
                >
                  <MoreVertical size={17} aria-hidden="true" />
                </button>
              </Tooltip>
            </div>
          )}
          <ContextMenu
            open={projectMenuOpen}
            point={menuPoint}
            items={compactTopics ? (menuProject?.mode === "color" ? projectColorMenuItems : workbenchProjectMenuItems) : projectMenuItems}
            minWidth={compactTopics && menuProject?.mode === "color" ? 168 : compactTopics ? 206 : 212}
            ariaLabel={t("projectTree.projectActions")}
            onClose={closeMenu}
          />
        </div>
        {hasChildren && (
          <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
            <div className="project-tree__children-inner">
              {children.map((child) => renderNode(child, depth + 1, section, isVisible && isExpanded))}
            </div>
          </div>
        )}
      </div>
    );
  };

  const timeFilterBadge = timeFilter !== "all" ? (timeFilter === "1d" ? "24h" : timeFilter) : "";
  const renderTimeFilterControl = () => {
    const active = timeFilter !== "all";
    const controlLabel = t("projectTree.timeFilter");
    const buttonClassName = `project-tree__header-action-btn${active ? " project-tree__header-action-btn--active" : ""}`;
    return (
      <Tooltip
        label={controlLabel}
        className="project-tree__action-slot project-tree__header-action-slot project-tree__header-action-slot--filter"
      >
        <div ref={filterRef} className="project-tree__time-filter">
          <button
            ref={filterTriggerRef}
            type="button"
            className={buttonClassName}
            aria-label={controlLabel}
            aria-haspopup="menu"
            aria-expanded={filterMenuOpen}
            onClick={() => {
              setMenuPoint(null);
              setFilterMenuOpen(!filterMenuOpen);
            }}
          >
            <Clock size={14} aria-hidden="true" />
            {timeFilterBadge && (
              <span className="project-tree__time-filter-label">
                {timeFilterBadge}
              </span>
            )}
          </button>
          {filterMenuOpen && (
            <div className="project-tree__time-filter-menu" role="menu" aria-label={t("projectTree.timeFilter")} onKeyDown={moveMenuFocus}>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "all" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("all"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilterAll")}
              </button>
              <div className="project-tree__time-filter-sep" role="separator" />
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "10" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("10"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter10")}
              </button>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "20" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("20"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter20")}
              </button>
              <div className="project-tree__time-filter-sep" role="separator" />
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "1h" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("1h"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter1h")}
              </button>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "3h" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("3h"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter3h")}
              </button>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "5h" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("5h"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter5h")}
              </button>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "1d" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("1d"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter1d")}
              </button>
            </div>
          )}
        </div>
      </Tooltip>
    );
  };

  const renderProjectHeader = (mode: "classic" | "workbench") => (
    <div className="project-tree__header">
      <span className="project-tree__header-title">
        {mode === "classic" && <BriefcaseBusiness className="project-tree__header-icon" size={13} />}
        {t("projectTree.workspaceTitle")}
      </span>
      <span className="project-tree__header-actions">
        {mode === "workbench" ? (
          <Tooltip label={t("projectTree.addProjectTooltip")} className="project-tree__action-slot project-tree__header-action-slot">
            <button
              type="button"
              className="project-tree__add-project project-tree__header-icon-btn"
              aria-label={t("projectTree.addProjectTooltip")}
              disabled={addingProject}
              onClick={() => void handleAddProject()}
            >
              <SquarePlus size={17} aria-hidden="true" />
            </button>
          </Tooltip>
        ) : (
          <>
            {renderTimeFilterControl()}
            <Tooltip label={collapseToggleLabel} className="project-tree__action-slot project-tree__header-action-slot project-tree__action-slot--collapse">
              <button
                type="button"
                className={`project-tree__collapse-all${canRestoreCollapsedView ? " project-tree__collapse-all--restore" : ""}`}
                aria-label={collapseToggleLabel}
                aria-pressed={canRestoreCollapsedView}
                disabled={!canToggleCollapsedView}
                onClick={toggleCollapsedView}
              >
                {canRestoreCollapsedView ? <ListRestart size={14} /> : <ListCollapse size={14} />}
              </button>
            </Tooltip>
            <Tooltip label={t("projectTree.addProjectTooltip")} className="project-tree__action-slot project-tree__header-action-slot project-tree__action-slot--add">
              <button
                type="button"
                className="project-tree__add-project"
                aria-label={t("projectTree.addProjectTooltip")}
                disabled={addingProject}
                onClick={() => void handleAddProject()}
              >
                <FolderPlus size={14} />
              </button>
            </Tooltip>
          </>
        )}
      </span>
    </div>
  );

  const renderEmptyState = () => {
    if (query.trim()) return <div className="project-tree__empty">{t("projectTree.emptyNoMatch")}</div>;
    if (timeFilter !== "all") {
      return (
        <div className="project-tree__empty">{t("projectTree.emptyNoTimeFilterMatch")}
          <button
            type="button"
            className="project-tree__empty-primary"
            onClick={() => onTimeFilterChange("all")}
          >
            {t("projectTree.clearTimeFilter")}
          </button>
        </div>
      );
    }
    return (
      <div className="project-tree__empty-state">
        <div className="project-tree__empty project-tree__empty--subtle">{t("projectTree.emptyNoProjects")}</div>
        <button
          type="button"
          className="project-tree__empty-primary"
          onClick={() => void handleAddProject()}
          disabled={addingProject}
        >
          <FolderPlus size={14} />
          <span>{t("projectTree.addProjectTooltip")}</span>
        </button>
      </div>
    );
  };

  // Report visible topics to parent after render so shortcuts match sidebar order.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => {
    onVisibleTopicsChange?.(visibleTopicsCollectorRef.current);
  });

  // Reset topic index counter and visible topics collector before each render.
  topicIndexRef.current = 0;
  visibleTopicsCollectorRef.current = [];

  return (
    <div className="project-tree">
      {searchVisible && (
        <label className="project-tree__search">
          <Search size={14} />
          <input
            ref={searchInputRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("projectTree.searchPlaceholder")}
          />
        </label>
      )}
      {compactTopics ? (
        <div className="project-tree__list project-tree__list--workbench">
          <div className="project-tree__section project-tree__section--recent">
            <div className="project-tree__section-head">
              <div className="project-tree__section-title">{t("projectTree.recentTitle")}</div>
              <div ref={recentSettingsRef} className="project-tree__recent-settings">
                <button
                  ref={recentSettingsTriggerRef}
                  type="button"
                  className={`project-tree__recent-settings-trigger${recentSettingsOpen ? " project-tree__recent-settings-trigger--open" : ""}`}
                  title={t("projectTree.recentSettings")}
                  aria-label={t("projectTree.recentSettings")}
                  aria-haspopup="dialog"
                  aria-controls="project-tree-recent-settings"
                  aria-expanded={recentSettingsOpen}
                  onPointerDown={(event) => event.stopPropagation()}
                  onClick={(event) => {
                    event.stopPropagation();
                    setRecentSettingsOpen((open) => !open);
                  }}
                >
                  <SlidersHorizontal size={16} aria-hidden="true" />
                </button>
                {recentSettingsOpen && (
                  <div id="project-tree-recent-settings" className="project-tree__recent-settings-popover" role="dialog" aria-label={t("projectTree.recentSettings")}>
                    <div className="project-tree__recent-settings-row">
                      <span>{t("projectTree.showExternalCalls")}</span>
                      <button
                        type="button"
                        role="switch"
                        aria-label={t("projectTree.showExternalCalls")}
                        aria-checked={workbenchRecentSettings.showExternal}
                        className={`project-tree__recent-switch${workbenchRecentSettings.showExternal ? " project-tree__recent-switch--on" : ""}`}
                        onClick={() => updateWorkbenchRecentSettings({ showExternal: !workbenchRecentSettings.showExternal })}
                      >
                        <span aria-hidden="true" />
                      </button>
                    </div>
                    <div className="project-tree__recent-settings-divider" />
                    <div className="project-tree__recent-limit-label">{t("projectTree.recentCount")}</div>
                    <div className="project-tree__recent-limit-options">
                      {WORKBENCH_RECENT_LIMITS.map((limit) => (
                        <button
                          key={limit}
                          type="button"
                          className={workbenchRecentSettings.limit === limit ? "project-tree__recent-limit--active" : ""}
                          aria-pressed={workbenchRecentSettings.limit === limit}
                          onClick={() => updateWorkbenchRecentSettings({ limit })}
                        >
                          {limit}
                        </button>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            </div>
            {workbenchTreeSections.recent.length > 0 ? (
              workbenchTreeSections.recent.map((node) => renderNode(node, 0, "recent", false))
            ) : (
              <div className="project-tree__recent-empty">{t("projectTree.recentEmpty")}</div>
            )}
          </div>
          {renderProjectHeader("workbench")}
          {workbenchTreeSections.projects.length > 0 ? (
            <div className="project-tree__section project-tree__section--projects">
              {workbenchTreeSections.projects.map((node) => renderNode(node, 0, "projects"))}
            </div>
          ) : renderEmptyState()}
        </div>
      ) : (
        <>
          {renderProjectHeader("classic")}
          <div className="project-tree__list">
            {visibleTree.length === 0 ? renderEmptyState() : visibleTree.map((node) => renderNode(node, 0))}
          </div>
        </>
      )}
    </div>
  );
}
