import { useEffect, useState } from "react";
import { Pause, Play, RefreshCw } from "lucide-react";
import { assistantPauseAll, assistantPauseForRestart, assistantResumeAll, assistantWorkControl } from "./assistant.bridge";
import type { AssistantCopy } from "./assistant.copy";
import { assistantRequestID } from "./assistant.types";
import type { AssistantWorkControlState } from "./assistant.types";

function stateLabel(state: AssistantWorkControlState, copy: AssistantCopy): string {
  switch (state) {
    case "running": return copy.workRunning;
    case "quiescing": return copy.workQuiescing;
    case "paused": return copy.workPaused;
    case "recovering": return copy.workRecovering;
  }
}

export function AssistantWorkControlBar({ copy }: { copy: AssistantCopy }) {
  const [state, setState] = useState<AssistantWorkControlState | null>(null);
  const [hint, setHint] = useState("");
  const [active, setActive] = useState(0);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const refresh = async (alive: () => boolean) => {
    try {
      const wc = await assistantWorkControl();
      if (!alive()) return;
      setState(wc.state);
      setHint(wc.next_hint ?? "");
      setActive((wc.active ?? []).length);
      setError(wc.error ?? "");
    } catch {
      /* keep last known state; the poll retries */
    }
  };

  useEffect(() => {
    let alive = true;
    void refresh(() => alive);
    const timer = window.setInterval(() => { void refresh(() => alive); }, 5000);
    return () => { alive = false; window.clearInterval(timer); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const act = async (op: "pause" | "resume" | "restart") => {
    setBusy(true);
    setError("");
    try {
      const req = { requestId: assistantRequestID(`work-${op}`) };
      const wc = op === "pause" ? await assistantPauseAll(req)
        : op === "resume" ? await assistantResumeAll(req)
        : await assistantPauseForRestart(req);
      setState(wc.state);
      setHint(wc.next_hint ?? "");
      setActive((wc.active ?? []).length);
      if (wc.error) setError(wc.error);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  if (state === null) return null;

  const statusText = hint || stateLabel(state, copy);

  return (
    <div className="assistant-work-control" role="group" aria-label={copy.workControl}>
      <span className={`assistant-work-control__state assistant-work-control__state--${state}`}>
        {statusText}
        {active > 0 ? ` (${active})` : ""}
      </span>
      <button type="button" title={copy.pauseAll} aria-label={copy.pauseAll} disabled={busy || state === "paused"} onClick={() => void act("pause")}><Pause size={15} /></button>
      <button type="button" title={copy.resumeAll} aria-label={copy.resumeAll} disabled={busy || state === "running"} onClick={() => void act("resume")}><Play size={15} /></button>
      <button type="button" title={copy.pauseForRestart} aria-label={copy.pauseForRestart} disabled={busy} onClick={() => void act("restart")}><RefreshCw size={15} /></button>
      {error ? <span className="assistant-work-control__error" title={error}>{error}</span> : null}
    </div>
  );
}
