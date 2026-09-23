// On-demand body loading for the run detail panel.
//
// The compact run projection only keeps a summary (`content`) plus whatever the
// backend shipped inline. History elides full args/output for memory, and the
// mini scene never needed them. When the user opens the detail pane we fetch the
// real body through the existing ToolResultForTab binding.
//
// Two rules this module exists to guarantee:
//  - a response can only ever write its own (runId, callId) entry, so switching
//    records can never let a stale response pollute the newly selected one;
//  - "the backend does not have it" is a distinct state from "it is empty".

import { useEffect, useRef, useState } from "react";
import type { RunEvent } from "../store/run";

export type RunDetailBodyPhase = "idle" | "loading" | "loaded" | "unavailable" | "error";

export type RunDetailBodyState = {
  phase: RunDetailBodyPhase;
  /** Full arguments from the backend, when it still has them. */
  args?: string;
  /** Full output from the backend, when it still has them. */
  output?: string;
  /** Why the body is unavailable / failed to load. */
  message?: string;
};

export type RunDetailBody = { args?: string; output?: string };

/**
 * Fetches one call's full body. Injected so this module stays free of the Wails
 * bridge: the store imports the cache to invalidate it, and must not drag the
 * runtime bindings into the model layer.
 */
export type RunDetailBodyFetcher = (tabId: string, callId: string) => Promise<RunDetailBody | null>;

const IDLE: RunDetailBodyState = { phase: "idle" };
/** Bounded so a long session cannot grow an unbounded per-call cache. */
const MAX_ENTRIES = 96;

const entries = new Map<string, RunDetailBodyState>();
const inflight = new Map<string, Promise<void>>();
const listeners = new Set<() => void>();
/**
 * Load generation per key, so only the newest request may commit its result.
 * The counter is global and never reset: a request started before a cache reset
 * must not sneak back in when its key is reused.
 */
const generations = new Map<string, number>();
let generationCounter = 0;

function notify(): void {
  for (const listener of listeners) listener();
}

function setEntry(key: string, state: RunDetailBodyState): void {
  const previous = entries.get(key);
  if (previous?.phase === state.phase && previous.args === state.args
    && previous.output === state.output && previous.message === state.message) return;
  if (!previous && entries.size >= MAX_ENTRIES) {
    const oldest = entries.keys().next();
    if (!oldest.done) {
      entries.delete(oldest.value);
      generations.delete(oldest.value);
      inflight.delete(oldest.value);
    }
  }
  entries.set(key, state);
  notify();
}

/**
 * Cache key. The run identity is part of the key so two sessions that happen to
 * reuse a provider call id (or a `call_1`) never share a body.
 */
export function detailBodyKey(runId: string, callId: string): string {
  return `${runId}\u0000${callId}`;
}

export function readDetailBody(key: string): RunDetailBodyState {
  return entries.get(key) ?? IDLE;
}

export function subscribeDetailBody(listener: () => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

/** Test / session-teardown hook: drop every cached body and pending request. */
export function resetDetailBodies(): void {
  entries.clear();
  inflight.clear();
  generations.clear();
}

/**
 * Drop the bodies cached for one run. Runs are removed from the store when a
 * session is re-hydrated or rewound, and a stale body must not outlive its run.
 */
export function forgetDetailBodies(runId: string): void {
  const prefix = `${runId}\u0000`;
  for (const key of [...entries.keys()]) {
    if (key.startsWith(prefix)) entries.delete(key);
  }
  for (const key of [...generations.keys()]) {
    if (key.startsWith(prefix)) {
      generations.delete(key);
      inflight.delete(key);
    }
  }
}

/**
 * Whether a record's inline body is incomplete enough to be worth a round trip.
 * `archived` means the body was elided; `truncated` means the backend head+tailed
 * it, so the full text may still be recoverable from the session.
 */
export function needsDetailBody(event?: RunEvent): boolean {
  if (!event?.callId) return false;
  return Boolean(event.archived || event.truncated);
}

/**
 * Loads (or reloads) one record's full body. Repeated calls while a request is
 * in flight are coalesced; `force` starts a fresh request for the retry path and
 * keeps whatever body is already visible.
 */
export async function loadDetailBody(
  key: string,
  fetchBody: () => Promise<RunDetailBody | null>,
  options?: { force?: boolean },
): Promise<void> {
  if (!key) return;
  if (inflight.has(key) && !options?.force) return inflight.get(key);
  const generation = ++generationCounter;
  generations.set(key, generation);
  const previous = entries.get(key);
  // Keep the last good body on screen while reloading, so a retry never blanks
  // output the user is already reading.
  setEntry(key, { ...(previous ?? {}), phase: "loading", message: undefined });
  const request = (async () => {
    try {
      const body = await fetchBody();
      if (generations.get(key) !== generation) return;
      if (!body) {
        setEntry(key, { ...(previous ?? {}), phase: "unavailable", message: "会话已不再保留这条调用记录" });
        return;
      }
      // A known-empty body is kept as an empty string: "the tool produced
      // nothing" is a different fact from "the session no longer has it".
      setEntry(key, { phase: "loaded", args: body.args, output: body.output });
    } catch (error) {
      if (generations.get(key) !== generation) return;
      setEntry(key, {
        ...(previous ?? {}),
        phase: "error",
        message: error instanceof Error ? error.message : String(error),
      });
    }
  })();
  inflight.set(key, request);
  try {
    await request;
  } finally {
    if (inflight.get(key) === request) {
      inflight.delete(key);
      generations.delete(key);
    }
  }
}

/**
 * Full args/output for the record the panel is showing. The hook is keyed by
 * (tabId, callId), so selecting another record simply reads another entry —
 * an in-flight response for the previous record cannot land on the new one.
 */
export function useRunDetailBody(
  tabId: string | undefined,
  event: RunEvent | undefined,
  fetchBody: RunDetailBodyFetcher,
  runId: string,
) {
  const callId = event?.callId;
  const key = tabId && callId ? detailBodyKey(runId, `${event?.eventId}\u0000${callId}`) : "";
  // Snapshot carries its key so a render triggered by a previous record's
  // response can never show that record's body under the new selection.
  const [snapshot, setSnapshot] = useState<{ key: string; state: RunDetailBodyState }>(
    () => ({ key, state: readDetailBody(key) }),
  );
  const state = snapshot.key === key ? snapshot.state : readDetailBody(key);
  const keyRef = useRef(key);
  keyRef.current = key;

  useEffect(() => {
    setSnapshot({ key, state: readDetailBody(key) });
    return subscribeDetailBody(() => {
      setSnapshot({ key, state: readDetailBody(key) });
    });
  }, [key]);

  const load = (force = false) => {
    const target = keyRef.current;
    if (!target || !tabId || !callId) return;
    // The current API queries the parent session only; a child may reuse its
    // call ID. Never display a parent's body under a child record.
    void loadDetailBody(target, () => event?.parentId ? Promise.resolve(null) : fetchBody(tabId, callId), { force });
  };

  return { state, load };
}
