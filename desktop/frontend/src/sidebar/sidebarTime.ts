import { useEffect, useState } from "react";

const MINUTE_MS = 60_000;

export function formatSidebarRelativeTime(timestamp: number | undefined, now = Date.now()): string {
  if (!timestamp) return "—";
  const then = new Date(timestamp);
  if (Number.isNaN(then.getTime())) return "—";
  const elapsed = Math.max(0, now - then.getTime());
  if (elapsed < MINUTE_MS) return "刚刚";
  if (elapsed < 60 * MINUTE_MS) return `${Math.floor(elapsed / MINUTE_MS)}分钟`;
  if (elapsed < 24 * 60 * MINUTE_MS) return `${Math.floor(elapsed / (60 * MINUTE_MS))}小时`;
  if (elapsed < 30 * 24 * 60 * MINUTE_MS) return `${Math.floor(elapsed / (24 * 60 * MINUTE_MS))}天`;
  if (then.getFullYear() === new Date(now).getFullYear()) return `${then.getMonth() + 1}月${then.getDate()}日`;
  return `${then.getFullYear()}年${then.getMonth() + 1}月${then.getDate()}日`;
}

export function formatSidebarAbsoluteTime(timestamp: number | undefined): string {
  if (!timestamp) return "";
  const value = new Date(timestamp);
  if (Number.isNaN(value.getTime())) return "";
  const part = (n: number) => String(n).padStart(2, "0");
  return `${value.getFullYear()}-${part(value.getMonth() + 1)}-${part(value.getDate())} ${part(value.getHours())}:${part(value.getMinutes())}:${part(value.getSeconds())}`;
}

/** One timer per mounted sidebar; rows receive the shared timestamp. */
export function useSidebarNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    let interval = 0;
    let align = 0;
    const refresh = () => setNow(Date.now());
    const startMinuteInterval = () => {
      refresh();
      interval = window.setInterval(refresh, MINUTE_MS);
    };
    align = window.setTimeout(startMinuteInterval, MINUTE_MS - (Date.now() % MINUTE_MS));
    window.addEventListener("focus", refresh);
    return () => {
      window.clearTimeout(align);
      window.clearInterval(interval);
      window.removeEventListener("focus", refresh);
    };
  }, []);
  return now;
}
