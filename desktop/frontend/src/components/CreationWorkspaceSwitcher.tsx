import { useEffect, useMemo } from "react";

import { useT } from "../lib/i18n";
import type { WidgetWorkspaceOption } from "../lib/bridge";
import type { ProjectNode } from "../lib/types";

export interface CreationWorkspaceChoice {
  key: string;
  scope: "global" | "project";
  root: string;
  name: string;
}

export function showsBlankSessionWorkspaceHeader(
  newSessionSurface: boolean,
  backendBlank: boolean | undefined,
  hasContent: boolean,
): boolean {
  return !hasContent && (newSessionSurface || backendBlank === true);
}

export function creationWorkspaceChoices(nodes: ProjectNode[]): CreationWorkspaceChoice[] {
  const seen = new Set<string>();
  const choices: CreationWorkspaceChoice[] = [];
  for (const node of nodes) {
    const scope = node.kind === "project" ? "project" : node.kind === "global_folder" ? "global" : null;
    const root = scope === "project" ? node.root?.trim() ?? "" : "";
    if (!scope || (scope === "project" && !root)) continue;
    const key = scope === "global" ? "global" : `project:${comparableWorkspaceRoot(root)}`;
    if (seen.has(key)) continue;
    seen.add(key);
    choices.push({ key, scope, root, name: node.label.trim() || (scope === "global" ? "Global" : root) });
  }
  return choices;
}

export function creationWorkspaceChoicesFromOptions(
  options: WidgetWorkspaceOption[],
): CreationWorkspaceChoice[] {
  const seen = new Set<string>();
  const choices: CreationWorkspaceChoice[] = [];
  for (const option of options) {
    if (option.scope !== "project" && option.scope !== "global") continue;
    const root = option.scope === "project" ? option.root?.trim() ?? "" : "";
    if (option.scope === "project" && !root) continue;
    const key = option.scope === "global" ? "global" : `project:${comparableWorkspaceRoot(root)}`;
    if (seen.has(key)) continue;
    seen.add(key);
    choices.push({
      key,
      scope: option.scope,
      root,
      name: option.name.trim() || (option.scope === "global" ? "Global" : root),
    });
  }
  if (!seen.has("global")) choices.push({ key: "global", scope: "global", root: "", name: "Global" });
  return choices;
}

export function comparableWorkspaceRoot(root: string): string {
  const normalized = root.trim().replace(/\\/g, "/").replace(/\/+$/, "");
  return /^[a-z]:/i.test(normalized) ? normalized.toLowerCase() : normalized;
}

export function creationWorkspaceIndex(
  choices: CreationWorkspaceChoice[],
  scope: string,
  root: string,
): number {
  if (scope === "global") return choices.findIndex((choice) => choice.scope === "global");
  const target = comparableWorkspaceRoot(root);
  return choices.findIndex((choice) => choice.scope === "project" && comparableWorkspaceRoot(choice.root) === target);
}

interface CreationWorkspaceSwitcherProps {
  choices: CreationWorkspaceChoice[];
  activeScope: string;
  activeRoot: string;
  activeName: string;
  busy?: boolean;
  onSelect: (choice: CreationWorkspaceChoice) => void;
}

interface CreationWorkspaceHeaderProps extends CreationWorkspaceSwitcherProps {
  title: string;
  titleHint: string;
}

export function CreationWorkspaceHeader({
  title,
  titleHint,
  ...switcher
}: CreationWorkspaceHeaderProps) {
  return (
    <>
      <h1 className="topicbar__creation-title" title={titleHint}>{title}</h1>
      <CreationWorkspaceSwitcher {...switcher} />
    </>
  );
}

export function CreationWorkspaceSwitcher({
  choices,
  activeScope,
  activeRoot,
  activeName,
  busy = false,
  onSelect,
}: CreationWorkspaceSwitcherProps) {
  const t = useT();
  const visibleChoices = useMemo(() => {
    if (creationWorkspaceIndex(choices, activeScope, activeRoot) >= 0) return choices;
    if (activeScope !== "project" || !activeRoot.trim()) return choices;
    return [{
      key: `project:${comparableWorkspaceRoot(activeRoot)}`,
      scope: "project" as const,
      root: activeRoot,
      name: activeName || activeRoot,
    }, ...choices];
  }, [activeName, activeRoot, activeScope, choices]);
  const index = creationWorkspaceIndex(visibleChoices, activeScope, activeRoot);
  const active = index >= 0 ? visibleChoices[index] : visibleChoices[0];
  const disabled = busy || visibleChoices.length < 2;

  const switchBy = (delta: number) => {
    if (disabled || visibleChoices.length === 0) return;
    const base = index >= 0 ? index : delta > 0 ? -1 : 0;
    onSelect(visibleChoices[(base + delta + visibleChoices.length) % visibleChoices.length]);
  };

  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.defaultPrevented || disabled || (!event.ctrlKey && !event.metaKey)) return;
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      switchBy(event.key === "ArrowLeft" ? -1 : 1);
    };
    window.addEventListener("keydown", onKeyDown, { capture: true });
    return () => window.removeEventListener("keydown", onKeyDown, { capture: true });
  });

  const name = active?.name || activeName;
  const position = active ? `${Math.max(index, 0) + 1} / ${visibleChoices.length}` : "";
  return (
    <div className="creation-workspace-switcher" aria-busy={busy} aria-live="polite">
      <button
        className="creation-workspace-switcher__step creation-workspace-switcher__step--previous"
        type="button"
        disabled={disabled}
        aria-label={t("desktopIcon.quick.previousWorkspaceAria")}
        title={t("desktopIcon.quick.previousWorkspaceTitle")}
        onClick={() => switchBy(-1)}
      >
        <kbd>Ctrl</kbd><span aria-hidden="true">←</span>
      </button>
      <strong className="creation-workspace-switcher__current" title={active?.root || name}>
        <span className="creation-workspace-switcher__name">{name}</span>
        {position && <span className="creation-workspace-switcher__position"> · {position}</span>}
      </strong>
      <button
        className="creation-workspace-switcher__step creation-workspace-switcher__step--next"
        type="button"
        disabled={disabled}
        aria-label={t("desktopIcon.quick.nextWorkspaceAria")}
        title={t("desktopIcon.quick.nextWorkspaceTitle")}
        onClick={() => switchBy(1)}
      >
        <kbd>Ctrl</kbd><span aria-hidden="true">→</span>
      </button>
    </div>
  );
}
