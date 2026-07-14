import type { WorkspaceWelcomeView } from "./types";

export type WelcomeLoadState = "loading" | "refreshing" | "ready" | "stale" | "error";
export type WelcomeDelightKind = "art" | "emoticon" | "joke" | "microglitch";

export interface WelcomeDelight {
  id: string;
  kind: WelcomeDelightKind;
  text: string;
}

export interface WelcomeAction {
  id: string;
  label: string;
  prompt: string;
}

export interface WelcomeModel {
  workspaceName: string;
  summary: string;
  primary: WelcomeAction;
  secondary: WelcomeAction[];
  status: string;
  statusTone: "normal" | "muted" | "warning";
  delight?: WelcomeDelight;
  experienced: boolean;
  familiar: boolean;
}

export interface WelcomePolicyInput {
  view?: WorkspaceWelcomeView | null;
  fallbackName: string;
  scope: string;
  loadState: WelcomeLoadState;
  globalVisits: number;
  workspaceVisits: number;
  delightEnabled: boolean;
  reducedMotion: boolean;
  seenDelights?: Record<string, number>;
  now?: number;
}

export type WelcomeTranslate = (key: string, vars?: Record<string, string | number>) => string;

const DAY = 24 * 60 * 60_000;
const REPEAT_WINDOW = 30 * DAY;

function short(value: string, limit = 52): string {
  const text = value.trim().replace(/\s+/g, " ");
  return text.length <= limit ? text : `${text.slice(0, limit - 1)}…`;
}

function primaryKind(view?: WorkspaceWelcomeView | null): string {
  const kinds = view?.contentKinds?.filter((kind) => kind !== "empty" && kind !== "unknown") ?? [];
  if (kinds.length > 1) return "mixed";
  return kinds[0] ?? view?.contentKinds?.[0] ?? "unknown";
}

function contentAction(kind: string, t: WelcomeTranslate): WelcomeAction {
  switch (kind) {
    case "code":
      return { id: "inspect-code", label: t("welcome.adaptive.action.code"), prompt: t("welcome.adaptive.prompt.code") };
    case "docs":
      return { id: "organize-docs", label: t("welcome.adaptive.action.docs"), prompt: t("welcome.adaptive.prompt.docs") };
    case "data":
      return { id: "inspect-data", label: t("welcome.adaptive.action.data"), prompt: t("welcome.adaptive.prompt.data") };
    case "media":
      return { id: "organize-media", label: t("welcome.adaptive.action.media"), prompt: t("welcome.adaptive.prompt.media") };
    case "research":
      return { id: "synthesize-research", label: t("welcome.adaptive.action.research"), prompt: t("welcome.adaptive.prompt.research") };
    case "mixed":
      return { id: "understand-mixed", label: t("welcome.adaptive.action.mixed"), prompt: t("welcome.adaptive.prompt.mixed") };
    case "empty":
      return { id: "set-goal", label: t("welcome.adaptive.action.empty"), prompt: t("welcome.adaptive.prompt.empty") };
    default:
      return { id: "understand", label: t("welcome.adaptive.action.understand"), prompt: t("welcome.adaptive.prompt.understand") };
  }
}

export function selectWelcomeDelight(input: WelcomePolicyInput, kind: string, t: WelcomeTranslate): WelcomeDelight | undefined {
  const view = input.view;
  if (!input.delightEnabled || input.workspaceVisits <= 3 || view?.degraded || input.loadState === "error" || input.loadState === "stale") {
    return undefined;
  }
  const now = input.now ?? Date.now();
  const seen = input.seenDelights ?? {};
  const available = (id: string) => now - (seen[id] ?? 0) >= REPEAT_WINDOW;
  const visit = input.workspaceVisits;

  if (!input.reducedMotion && visit % 20 === 0 && available("microglitch")) {
    return { id: "microglitch", kind: "microglitch", text: input.fallbackName };
  }
  if (visit % 10 === 0) {
    const id = `joke-${kind}`;
    if (available(id)) return { id, kind: "joke", text: t(`welcome.adaptive.joke.${kind}`) };
  }
  if (visit % 8 === 0 && available("emoticon")) {
    return { id: "emoticon", kind: "emoticon", text: "·ᴗ·" };
  }
  if (visit % 6 === 0 && available("art")) {
    return { id: "art", kind: "art", text: "  ·\n╶─┼─╴\n  ·" };
  }
  return undefined;
}

export function buildWelcomeModel(input: WelcomePolicyInput, t: WelcomeTranslate): WelcomeModel {
  const view = input.view;
  const workspaceName = short(view?.workspaceName || input.fallbackName || t("welcome.adaptive.workspace"), 36);
  const experienced = input.globalVisits >= 10;
  const familiar = Math.max(input.workspaceVisits, view?.sessionCount ?? 0) >= 5;
  const kind = primaryKind(view);
  const confidence = view?.confidence ?? 0;

  let summary: string;
  if (input.loadState === "error" || view?.degraded) {
    summary = t("welcome.adaptive.summary.degraded", { name: workspaceName });
  } else if (view?.recentTitle && familiar) {
    summary = t("welcome.adaptive.summary.returning", { name: workspaceName, title: short(view.recentTitle) });
  } else if (confidence < 0.55 && kind !== "empty") {
    summary = t("welcome.adaptive.summary.observed", { name: workspaceName });
  } else {
    summary = t(`welcome.adaptive.summary.${kind}`, { name: workspaceName });
  }

  let primary = contentAction(kind, t);
  if (view?.recentTitle && familiar) {
    primary = {
      id: "continue",
      label: t("welcome.adaptive.action.continue"),
      prompt: t("welcome.adaptive.prompt.continue", { title: short(view.recentTitle) }),
    };
  } else if ((view?.changedCount ?? 0) > 0 && kind !== "empty") {
    primary = {
      id: "review-changes",
      label: t("welcome.adaptive.action.changes"),
      prompt: t("welcome.adaptive.prompt.changes"),
    };
  }

  const secondary: WelcomeAction[] = [];
  if (primary.id !== "understand" && primary.id !== "set-goal") {
    secondary.push({ id: "understand", label: t("welcome.adaptive.action.understand"), prompt: t("welcome.adaptive.prompt.understand") });
  }
  secondary.push({ id: "new-direction", label: t("welcome.adaptive.action.new"), prompt: "" });

  let status = t("welcome.adaptive.status.fresh");
  let statusTone: WelcomeModel["statusTone"] = "muted";
  if (input.loadState === "loading") status = t("welcome.adaptive.status.loading");
  if (input.loadState === "refreshing") status = t("welcome.adaptive.status.refreshing");
  if (input.loadState === "stale") {
    status = t("welcome.adaptive.status.stale");
    statusTone = "warning";
  }
  if (input.loadState === "error" || view?.degraded) {
    status = t("welcome.adaptive.status.degraded");
    statusTone = "warning";
  } else if (view?.partial) {
    status = t("welcome.adaptive.status.partial");
    statusTone = "warning";
  } else if (experienced && familiar) {
    status = t("welcome.adaptive.status.familiar");
    statusTone = "normal";
  }

  return {
    workspaceName,
    summary,
    primary,
    secondary: secondary.slice(0, 2),
    status,
    statusTone,
    delight: selectWelcomeDelight(input, kind, t),
    experienced,
    familiar,
  };
}
