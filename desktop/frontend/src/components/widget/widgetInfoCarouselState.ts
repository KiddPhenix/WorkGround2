import type { WidgetSnapshot } from "../../lib/bridge";

export type WidgetInfoPage = "tokens" | "clock" | "pet" | "idle" | "system" | "models";
export type WidgetPetState = "idle" | "working" | "waiting" | "success" | "error" | "offline";

export const INFO_PAGES: WidgetInfoPage[] = ["tokens", "clock", "pet", "idle", "system", "models"];

export function availableWidgetInfoPages(snapshot: WidgetSnapshot): WidgetInfoPage[] {
  return INFO_PAGES.filter((page) => {
    if (page === "system") return snapshot.info.system.available;
    if (page === "models") return snapshot.info.models.length > 0;
    return true;
  });
}

export function resolveWidgetInfoPage(page: WidgetInfoPage, pages: WidgetInfoPage[]): WidgetInfoPage {
  return pages.includes(page) ? page : pages[0] ?? "tokens";
}

export function nextWidgetInfoPage(page: WidgetInfoPage, pages: WidgetInfoPage[]): WidgetInfoPage {
  const current = pages.indexOf(page);
  return pages[(current < 0 ? 0 : current + 1) % pages.length] ?? "tokens";
}

export function shouldShowWidgetContext(seen: string, current: string): boolean {
  return current.length > 0 && seen !== current;
}

export function formatCompactTokens(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0";
  const units = ["", "K", "M", "B"];
  let amount = value;
  let unit = 0;
  while (amount >= 1000 && unit < units.length - 1) {
    amount /= 1000;
    unit += 1;
  }
  const digits = amount >= 100 ? 0 : 2;
  return `${amount.toFixed(digits).replace(/\.0+$|(?<=\.[0-9])0+$/, "")}${units[unit]}`;
}

export function formatWidgetDuration(milliseconds: number): string {
  const total = Math.max(0, Math.floor(milliseconds / 1000));
  const hours = Math.floor(total / 3600).toString().padStart(2, "0");
  const minutes = Math.floor((total % 3600) / 60).toString().padStart(2, "0");
  const seconds = (total % 60).toString().padStart(2, "0");
  return `${hours}:${minutes}:${seconds}`;
}

export function widgetPetState(snapshot: WidgetSnapshot): WidgetPetState {
  if (snapshot.current?.kind === "error" || snapshot.failedCount > 0) return "error";
  if (snapshot.current?.kind === "choice" || snapshot.current?.kind === "reply" || snapshot.waitingCount > 0) return "waiting";
  if (snapshot.current?.kind === "result" || snapshot.completedCount > 0) return "success";
  if (snapshot.runningCount > 0) return "working";
  if (snapshot.info.system.available && snapshot.info.system.network === "offline") return "offline";
  return "idle";
}
