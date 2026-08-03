import { useEffect, useRef, useState } from "react";
import { useT } from "../../lib/i18n";
import { collabCopy } from "../copy";
import type { PendingIntent } from "../types";

interface IntentCountdownProps {
  intent: PendingIntent;
  connected: boolean;
  onStart(intent: PendingIntent): void;
  onStop(): void;
  onEdit(instruction: string): void;
}

export function IntentCountdown({ intent, connected, onStart, onStop, onEdit }: IntentCountdownProps) {
  const c = collabCopy(useT());
  const [remaining, setRemaining] = useState(() => Math.max(0, Math.ceil((intent.deadline - Date.now()) / 1000)));
  const startedRef = useRef(false);

  useEffect(() => {
    startedRef.current = false;
  }, [intent.messageId, intent.revision, intent.deadline]);

  useEffect(() => {
    if (intent.status !== "pending" || !connected) return;
    const tick = () => {
      const next = Math.max(0, Math.ceil((intent.deadline - Date.now()) / 1000));
      setRemaining(next);
      if (next === 0 && !startedRef.current) {
        startedRef.current = true;
        onStart(intent);
      }
    };
    tick();
    const timer = window.setInterval(tick, 250);
    return () => window.clearInterval(timer);
  }, [connected, intent, onStart]);

  if (intent.status === "dismissed") return null;
  return (
    <div className={`collab-intent collab-intent--${intent.status}`} role="status">
      <span className="collab-intent-ring" aria-hidden="true">{intent.status === "starting" ? "…" : remaining}</span>
      <span>{intent.status === "failed" ? intent.error : c("detected", { n: remaining })}</span>
      <div className="collab-intent-actions">
        <button type="button" disabled={!connected || intent.status === "starting"} onClick={() => {
          if (startedRef.current) return;
          startedRef.current = true;
          onStart(intent);
        }}>{c("startNow")}</button>
        <button type="button" disabled={intent.status === "starting"} onClick={() => {
          const next = window.prompt(c("edit"), intent.instruction);
          if (next?.trim()) onEdit(next.trim());
        }}>{c("edit")}</button>
        <button type="button" disabled={intent.status === "starting"} onClick={onStop}>{c("stop")}</button>
      </div>
    </div>
  );
}
