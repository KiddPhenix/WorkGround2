import type { ProjectNode, SessionMeta, TabMeta } from "./types";
import type { Translator } from "./i18n";

// Backend sentinel mirror (desktop/tabs.go defaultTopicTitle): the stored title
// of an auto-created blank topic. It is never migrated or renamed; the frontend
// only uses it to recognize legacy blank titles that lack source metadata.
export const DEFAULT_TOPIC_TITLE_SENTINEL = "新的会话";
export const TITLE_SOURCE_AUTO = "auto";
export const TITLE_SOURCE_MANUAL = "manual";

/**
 * isAutoBlankTitle reports whether a topic/session title is the system-generated
 * blank placeholder. The backend keeps `auto` as the title source even after it
 * promotes the first user message into a derived title, so an auto-sourced title
 * is only blank while it still equals the sentinel. Manual/user titles never
 * qualify — a manually named session keeps its verbatim text even when it
 * happens to equal the sentinel — and legacy topics without source metadata
 * fall back to the backend's own sentinel equality.
 */
export function isAutoBlankTitle(title: string | undefined, titleSource?: string): boolean {
  const trimmed = title?.trim();
  if (!trimmed) return false;
  const source = titleSource?.trim().toLowerCase();
  if (source === TITLE_SOURCE_MANUAL) return false;
  return trimmed === DEFAULT_TOPIC_TITLE_SENTINEL;
}

/**
 * localizedSessionTitle returns the display title for a topic/session: an
 * auto-blank title renders as the locale's "New session", while manual and
 * user titles stay verbatim.
 */
export function localizedSessionTitle(
  title: string | undefined,
  titleSource: string | undefined,
  fallback: string,
  t: Translator,
): string {
  const trimmed = title?.trim();
  if (!trimmed) return fallback;
  if (isAutoBlankTitle(trimmed, titleSource)) return t("topbar.newSession");
  return trimmed;
}

export function tabSessionDisplayTitle(
  tab?: Pick<TabMeta, "scope" | "workspaceName" | "workspaceRoot" | "topicTitle" | "sessionDisplayTitle">,
): string {
  if (!tab) return "Global";
  const workspace = tab.workspaceName || tab.workspaceRoot || (tab.scope === "global" ? "Global" : "Project");
  return tab.sessionDisplayTitle?.trim() || tab.topicTitle?.trim() || (tab.scope === "global" ? workspace : "Untitled");
}

/**
 * tabDisplayTitle is the locale-aware page header / tab title: it keeps the
 * verbatim session/topic title (session rename first) and only swaps an
 * auto-blank title for the locale's "New session".
 */
export function tabDisplayTitle(tab: TabMeta | undefined, t: Translator): string {
  if (!tab) return "Global";
  const effective = tabSessionDisplayTitle(tab);
  const workspace = tab.workspaceName || tab.workspaceRoot || (tab.scope === "global" ? "Global" : "Project");
  return localizedSessionTitle(effective, tab.titleSource, tab.scope === "global" ? workspace : "Untitled", t);
}

export function sessionActivityTime(session: SessionMeta): number {
  return session.lastActivityAt ?? session.modTime;
}

export function historySessionDisplayTitle(session: Pick<SessionMeta, "preview" | "title" | "topicTitle">, fallback: string): string {
  return session.title?.trim() || session.topicTitle?.trim() || session.preview?.trim() || fallback;
}

export function paletteSessionDisplayTitle(session: Pick<SessionMeta, "preview" | "title" | "topicTitle">, fallback: string): string {
  return session.topicTitle?.trim() || session.title?.trim() || session.preview?.trim() || fallback;
}

export function paletteSessionHint(
  session: Pick<SessionMeta, "preview" | "title" | "topicTitle" | "workspaceRoot">,
): string | undefined {
  const primary = paletteSessionDisplayTitle(session, "");
  const title = session.title?.trim();
  const preview = session.preview?.trim();
  const workspace = session.workspaceRoot?.trim();
  const secondary = title && title !== primary ? title : preview && preview !== primary ? preview : "";
  const hint = [secondary, workspace].filter(Boolean).join(" · ");
  return hint || undefined;
}

export function paletteSessionKeywords(session: Pick<SessionMeta, "preview" | "title">): string[] {
  return [session.title?.trim(), session.preview?.trim()].filter((value): value is string => Boolean(value));
}

// topicActivityTime returns the last-activity timestamp for a sidebar topic
// node. Falls back to the topic's creation time so blank topics (no session
// files yet) are still visible under time-based filters.
export function topicActivityTime(node: ProjectNode): number {
  return node.lastActivityAt || node.createdAt || 0;
}
