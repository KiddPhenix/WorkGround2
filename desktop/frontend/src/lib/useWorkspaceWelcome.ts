import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { app } from "./bridge";
import type { WorkspaceWelcomeView } from "./types";
import type { WelcomeLoadState } from "./welcome";

interface WelcomeMemory {
  visits: number;
  lastSessionKey: string;
  delightDisabled: boolean;
  seenDelights: Record<string, number>;
}

interface WelcomeCacheEntry {
  view: WorkspaceWelcomeView;
  at: number;
}

export interface WorkspaceWelcomeTarget {
  tabId: string;
  scope: string;
  workspaceRoot: string;
  workspaceName: string;
  sessionKey: string;
}

const CACHE_TTL = 10 * 60_000;
const REQUEST_TIMEOUT = 3_000;
const memoryCache = new Map<string, WelcomeCacheEntry>();

function hashKey(value: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < value.length; i += 1) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(36);
}

function workspaceStorageKey(target: WorkspaceWelcomeTarget): string {
  const identity = target.scope === "global" ? "global" : target.workspaceRoot || target.workspaceName;
  return `welcome:v1:workspace:${hashKey(identity)}`;
}

function readMemory(key: string): WelcomeMemory {
  const fallback: WelcomeMemory = { visits: 0, lastSessionKey: "", delightDisabled: false, seenDelights: {} };
  if (typeof window === "undefined") return fallback;
  try {
    const parsed = JSON.parse(window.localStorage.getItem(key) || "null") as Partial<WelcomeMemory> | null;
    if (!parsed) return fallback;
    return {
      visits: Math.max(0, Number(parsed.visits) || 0),
      lastSessionKey: typeof parsed.lastSessionKey === "string" ? parsed.lastSessionKey : "",
      delightDisabled: parsed.delightDisabled === true,
      seenDelights: parsed.seenDelights && typeof parsed.seenDelights === "object" ? parsed.seenDelights : {},
    };
  } catch {
    return fallback;
  }
}

function writeMemory(key: string, value: WelcomeMemory): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Welcome personalization is optional; storage failures keep the page usable.
  }
}

function readGlobalVisits(): number {
  if (typeof window === "undefined") return 0;
  try {
    return Math.max(0, Number(window.localStorage.getItem("welcome:v1:globalVisits")) || 0);
  } catch {
    return 0;
  }
}

function writeGlobalVisits(value: number): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem("welcome:v1:globalVisits", String(value));
  } catch {
    // Optional local familiarity signal.
  }
}

async function loadWelcome(tabId: string): Promise<WorkspaceWelcomeView> {
  let timeout = 0;
  try {
    return await Promise.race([
      app.WorkspaceWelcome(tabId),
      new Promise<WorkspaceWelcomeView>((_, reject) => {
        timeout = window.setTimeout(() => reject(new Error("workspace welcome timed out")), REQUEST_TIMEOUT);
      }),
    ]);
  } finally {
    if (timeout) window.clearTimeout(timeout);
  }
}

export function useWorkspaceWelcome(target: WorkspaceWelcomeTarget) {
  const cacheKey = target.scope === "global" ? "global" : target.workspaceRoot || target.workspaceName || target.tabId;
  const storageKey = useMemo(() => workspaceStorageKey(target), [target.scope, target.workspaceName, target.workspaceRoot]);
  const cached = memoryCache.get(cacheKey);
  const [view, setView] = useState<WorkspaceWelcomeView | null>(() => cached?.view ?? null);
  const [loadState, setLoadState] = useState<WelcomeLoadState>(() => {
    if (!cached) return "loading";
    return Date.now() - cached.at <= CACHE_TTL ? "refreshing" : "stale";
  });
  const [retryNonce, setRetryNonce] = useState(0);
  const [memory, setMemory] = useState<WelcomeMemory>(() => readMemory(storageKey));
  const [globalVisits, setGlobalVisits] = useState(readGlobalVisits);
  const generationRef = useRef(0);
  const markedDelightsRef = useRef(new Set<string>());

  useEffect(() => {
    markedDelightsRef.current.clear();
    const next = readMemory(storageKey);
    const sessionIdentity = `${target.tabId}:${target.sessionKey}`;
    if (sessionIdentity && next.lastSessionKey !== sessionIdentity) {
      next.visits += 1;
      next.lastSessionKey = sessionIdentity;
      writeMemory(storageKey, next);
      const total = readGlobalVisits() + 1;
      writeGlobalVisits(total);
      setGlobalVisits(total);
    } else {
      setGlobalVisits(readGlobalVisits());
    }
    setMemory({ ...next, seenDelights: { ...next.seenDelights } });
  }, [storageKey, target.sessionKey, target.tabId]);

  useEffect(() => {
    const generation = ++generationRef.current;
    const entry = memoryCache.get(cacheKey);
    if (entry) {
      setView(entry.view);
      setLoadState(Date.now() - entry.at <= CACHE_TTL ? "refreshing" : "stale");
    } else {
      setView(null);
      setLoadState("loading");
    }
    void loadWelcome(target.tabId)
      .then((next) => {
        if (generationRef.current !== generation) return;
        memoryCache.set(cacheKey, { view: next, at: Date.now() });
        setView(next);
        setLoadState(next.degraded ? "error" : "ready");
      })
      .catch(() => {
        if (generationRef.current !== generation) return;
        const fallback = memoryCache.get(cacheKey);
        if (fallback) {
          setView(fallback.view);
          setLoadState("stale");
        } else {
          setView(null);
          setLoadState("error");
        }
      });
    return () => {
      if (generationRef.current === generation) generationRef.current += 1;
    };
  }, [cacheKey, retryNonce, target.tabId]);

  const retry = useCallback(() => setRetryNonce((value) => value + 1), []);
  const disableDelight = useCallback(() => {
    setMemory((current) => {
      const next = { ...current, delightDisabled: true };
      writeMemory(storageKey, next);
      return next;
    });
  }, [storageKey]);
  const markDelightSeen = useCallback((id: string) => {
    if (!id || markedDelightsRef.current.has(id)) return;
    markedDelightsRef.current.add(id);
    setMemory((current) => {
      if (current.seenDelights[id]) return current;
      const next = { ...current, seenDelights: { ...current.seenDelights, [id]: Date.now() } };
      writeMemory(storageKey, next);
      // Keep it visible for this visit; persisted memory suppresses it later.
      return current;
    });
  }, [storageKey]);

  const reducedMotion = typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-reduced-motion: reduce)").matches
    : false;
  return {
    view,
    loadState,
    workspaceVisits: memory.visits,
    globalVisits,
    delightEnabled: !memory.delightDisabled,
    seenDelights: memory.seenDelights,
    reducedMotion,
    retry,
    disableDelight,
    markDelightSeen,
  };
}
