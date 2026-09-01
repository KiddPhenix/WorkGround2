import { useEffect, useRef } from "react";
import type { PendingIntent } from "./types";

// Deadlines belong to the Room controller, independent of mounted message rows.
export function useIntentTimers(intents: Record<string, PendingIntent>, connected: boolean, scope: string, onStart: (intent: PendingIntent) => void) {
  const fired = useRef(new Map<string, string>());
  const owner = useRef(scope);
  useEffect(() => {
    if (owner.current !== scope) {
      owner.current = scope;
      fired.current.clear();
    }
    const version = (intent: PendingIntent) => `${intent.revision}:${intent.deadline}`;
    for (const [id, value] of fired.current) {
      const intent = intents[id];
      if (!intent || intent.status !== "pending" || version(intent) !== value) fired.current.delete(id);
    }
    if (!connected) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = () => {
      if (cancelled) return;
      const now = Date.now();
      let next = Infinity;
      for (const intent of Object.values(intents)) {
        if (intent.status !== "pending" || fired.current.get(intent.messageId) === version(intent)) continue;
        if (intent.deadline <= now) {
          fired.current.set(intent.messageId, version(intent));
          onStart(intent);
        } else {
          next = Math.min(next, intent.deadline);
        }
      }
      if (next !== Infinity) timer = setTimeout(tick, Math.min(2_147_483_647, Math.max(0, next - Date.now())));
    };
    // Defer even expired intents so a disconnect/scope change can cancel them.
    timer = setTimeout(tick, 0);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [intents, connected, scope, onStart]);
}
