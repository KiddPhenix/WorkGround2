import { useCallback, useEffect, useRef, useState } from "react";
import { app, onSessionBackgroundChanged } from "../lib/bridge";
import type { SessionBackgroundImageView, SessionBackgroundSettingsView } from "../lib/types";

type BackgroundLayers = {
  current: SessionBackgroundImageView | null;
  previous: SessionBackgroundImageView | null;
};

function preloadBackground(url: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    let settled = false;
    const finish = (error?: unknown) => {
      if (settled) return;
      settled = true;
      image.onload = null;
      image.onerror = null;
      if (error) reject(error);
      else resolve();
    };
    image.onload = () => finish();
    image.onerror = () => finish(new Error("Session background image failed to load"));
    image.src = url;
    if (typeof image.decode === "function") {
      void image.decode().then(() => finish(), () => {
        if (image.complete && image.naturalWidth > 0) finish();
      });
    }
  });
}

export function SessionBackground({ tabId }: { tabId?: string }) {
  const [settings, setSettings] = useState<SessionBackgroundSettingsView | null>(null);
  const [layers, setLayers] = useState<BackgroundLayers>({ current: null, previous: null });
  const generationRef = useRef(0);
  const clearPreviousRef = useRef<number | null>(null);

  const show = useCallback(async (next: SessionBackgroundImageView, generation: number) => {
    if (!next.url) {
      if (generation === generationRef.current) setLayers({ current: null, previous: null });
      return;
    }
    await preloadBackground(next.url);
    if (generation !== generationRef.current) return;
    setLayers((current) => ({ current: next, previous: current.current }));
    if (clearPreviousRef.current !== null) window.clearTimeout(clearPreviousRef.current);
    clearPreviousRef.current = window.setTimeout(() => {
      setLayers((current) => ({ ...current, previous: null }));
      clearPreviousRef.current = null;
    }, 550);
  }, []);

  const load = useCallback(async () => {
    const generation = ++generationRef.current;
    try {
      const nextSettings = await app.SessionBackgroundSettings();
      if (generation !== generationRef.current) return;
      setSettings(nextSettings);
      if (!nextSettings.enabled || nextSettings.imageCount === 0 || !tabId) {
        setLayers({ current: null, previous: null });
        return;
      }
      for (let attempt = 0; attempt < nextSettings.imageCount; attempt++) {
        try {
          const image = attempt === 0 ? await app.SessionBackground(tabId) : await app.RotateSessionBackground(tabId);
          await show(image, generation);
          return;
        } catch {
          if (generation !== generationRef.current) return;
        }
      }
      setLayers({ current: null, previous: null });
    } catch {
      if (generation === generationRef.current) setLayers({ current: null, previous: null });
    }
  }, [show, tabId]);

  const rotate = useCallback(async () => {
    if (!tabId || !settings?.enabled || settings.rotateSeconds <= 0) return;
    const generation = ++generationRef.current;
    for (let attempt = 0; attempt < Math.max(1, settings.imageCount); attempt++) {
      try {
        await show(await app.RotateSessionBackground(tabId), generation);
        return;
      } catch {
        if (generation !== generationRef.current) return;
      }
    }
  }, [settings?.enabled, settings?.imageCount, settings?.rotateSeconds, show, tabId]);

  useEffect(() => {
    void load();
    return onSessionBackgroundChanged(() => { void load(); });
  }, [load]);

  useEffect(() => {
    if (!settings?.enabled || settings.rotateSeconds <= 0 || !tabId) return;
    const interval = settings.rotateSeconds * 1000;
    let timer: number | null = null;
    let dueAt = Date.now() + interval;
    const clear = () => {
      if (timer !== null) window.clearTimeout(timer);
      timer = null;
    };
    const schedule = () => {
      clear();
      if (document.visibilityState === "hidden") return;
      const delay = Math.max(0, dueAt - Date.now());
      timer = window.setTimeout(() => {
        dueAt = Date.now() + interval;
        void rotate();
        schedule();
      }, delay);
    };
    const onVisibility = () => {
      if (document.visibilityState === "hidden") {
        clear();
        return;
      }
      if (Date.now() >= dueAt) {
        dueAt = Date.now() + interval;
        void rotate();
      }
      schedule();
    };
    document.addEventListener("visibilitychange", onVisibility);
    schedule();
    return () => {
      clear();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [rotate, settings?.enabled, settings?.rotateSeconds, tabId]);

  useEffect(() => () => {
    generationRef.current++;
    if (clearPreviousRef.current !== null) window.clearTimeout(clearPreviousRef.current);
  }, []);

  if (!settings?.enabled || !layers.current) return null;
  return (
    <div className="session-background" aria-hidden="true">
      {layers.previous && (
        <div className="session-background__image session-background__image--previous" style={{ backgroundImage: `url(${JSON.stringify(layers.previous.url)})` }} />
      )}
      <div key={layers.current.url} className="session-background__image session-background__image--current" style={{ backgroundImage: `url(${JSON.stringify(layers.current.url)})` }} />
      {settings.maskEnabled && <div className="session-background__mask" />}
    </div>
  );
}
