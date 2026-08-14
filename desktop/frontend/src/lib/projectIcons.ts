export const PROJECT_ICON_OPTIONS = ["", "star", "bookmark", "code", "terminal", "bolt"] as const;

export type ProjectIconKey = (typeof PROJECT_ICON_OPTIONS)[number];

export function projectIconKey(value: string | undefined): ProjectIconKey {
  const normalized = (value ?? "").trim().toLowerCase();
  return PROJECT_ICON_OPTIONS.includes(normalized as ProjectIconKey)
    ? normalized as ProjectIconKey
    : "";
}
