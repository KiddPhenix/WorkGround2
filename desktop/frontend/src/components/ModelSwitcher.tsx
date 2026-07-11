import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AlertCircle, Brain, Check, ChevronsUpDown, Search } from "lucide-react";
import { asArray } from "../lib/array";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { ModelInfo } from "../lib/types";
import { AnchoredPopover } from "./AnchoredPopover";
import { Tooltip } from "./Tooltip";

// ModelSwitcher opens an upward popover listing configured providers. Selecting
// one switches the active model while the current conversation continues.
// Selection is reflected immediately while onPick queues it for the next turn.
// If queueing fails, the previous selection is restored and the menu reopens
// with an inline error.
export function ModelSwitcher({ label, tabId, onPick }: { label: string; tabId?: string; onPick: (name: string) => Promise<void> }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [query, setQuery] = useState("");
  const [triggerWidth, setTriggerWidth] = useState<number | undefined>(undefined);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [optimisticLabel, setOptimisticLabel] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Measure trigger width off the render path to avoid forced layout
  useEffect(() => {
    const el = triggerRef.current;
    if (!el) return;
    const measure = () => setTriggerWidth(el.getBoundingClientRect().width);
    measure();
    const observer = new ResizeObserver(() => measure());
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const loadModels = useCallback(() => {
    return (tabId ? app.ModelsForTab(tabId) : app.Models()).then((next) => setModels(asArray(next))).catch(() => {});
  }, [tabId]);

  useEffect(() => {
    void loadModels();
  }, [loadModels]);

  useEffect(() => {
    if (open) {
      setQuery("");
      void loadModels();
      window.requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [loadModels, open]);

  const keyword = query.trim().toLowerCase();
  const filtered = useMemo(
    () => keyword
      ? models.filter((m) => m.model.toLowerCase().includes(keyword) || m.provider.toLowerCase().includes(keyword))
      : models,
    [models, keyword],
  );

  // Group by provider, with the current model's group first
  const groups = useMemo(() => {
    const map = new Map<string, ModelInfo[]>();
    let currentProvider = "";
    for (const m of filtered) {
      if (m.current) currentProvider = m.provider;
      const list = map.get(m.provider);
      if (list) list.push(m);
      else map.set(m.provider, [m]);
    }
    return [...map.entries()]
      .sort(([a], [b]) => {
        if (a === currentProvider) return -1;
        if (b === currentProvider) return 1;
        return providerLabel(a, t).localeCompare(providerLabel(b, t));
      })
      .map(([provider, items]) => ({
        provider,
        label: providerLabel(provider, t),
        items,
      }));
  }, [filtered, t]);

  const currentProvider = useMemo(() => {
    const cur = models.find((m) => m.current) ?? models.find((m) => m.model === label || m.ref === label);
    return cur ? providerLabel(cur.provider, t) : null;
  }, [label, models, t]);
  const visibleLabel = optimisticLabel ?? label;
  const triggerLabel = currentProvider ? `${visibleLabel} · ${currentProvider}` : visibleLabel;

  useEffect(() => {
    if (optimisticLabel === label) setOptimisticLabel(null);
  }, [label, optimisticLabel]);

  const pick = async (name: string) => {
    if (busy) return;
    const previous = models;
    const picked = models.find((m) => m.ref === name);
    setBusy(true);
    setError(null);
    setModels((prev) => prev.map((m) => ({ ...m, current: m.ref === name })));
    if (picked) setOptimisticLabel(picked.model);
    setOpen(false);
    try {
      await onPick(name);
    } catch (err: unknown) {
      setModels(previous);
      setOptimisticLabel(null);
      setError(err instanceof Error ? err.message : String(err ?? ""));
      setOpen(true);
    } finally {
      setBusy(false);
    }
  };

  const clearError = () => {
    if (error) setError(null);
  };

  return (
    <div className="modelsw">
      <Tooltip label={triggerLabel} fill>
        <button
          ref={triggerRef}
          type="button"
          className="modelsw__trigger"
          aria-label={triggerLabel}
          aria-expanded={open}
          aria-busy={busy || undefined}
          onClick={() => setOpen((v) => {
            if (!v) setError(null);
            return !v;
          })}
        >
          <Brain size={13} className="modelsw__kind" />
          <span className="modelsw__label">{visibleLabel}</span>
          <ChevronsUpDown size={11} />
        </button>
      </Tooltip>
      <AnchoredPopover
        open={open}
        anchorRef={triggerRef}
        onClose={() => { if (!busy) setOpen(false); }}
        className="modelsw__menu modelsw__menu--portal"
        style={{ minWidth: Math.max(triggerWidth || 200, 200), maxWidth: "min(90vw, 480px)" }}
      >
        <div role="listbox">
          <div className="modelsw__search" role="presentation">
            <Search size={13} />
            <input
              ref={inputRef}
              type="text"
              className="modelsw__search-input"
              placeholder={t("modelSwitcher.searchPlaceholder")}
              value={query}
              onChange={(e) => { setQuery(e.target.value); clearError(); }}
              onKeyDown={(e) => {
                if (e.key === "Escape") setOpen(false);
                if (e.key === "Enter" && filtered.length === 1 && !busy) pick(filtered[0].ref);
              }}
              disabled={busy}
            />
          </div>
          {models.length === 0 && <div className="modelsw__empty">{t("status.noModels")}</div>}
          {models.length > 0 && filtered.length === 0 && query && <div className="modelsw__empty">{t("modelSwitcher.noMatches")}</div>}
          {error && (
            <div className="modelsw__error" role="alert">
              <AlertCircle size={13} />
              <span>{error}</span>
            </div>
          )}
          {groups.map((g) => (
            <div key={g.provider} role="group" aria-label={g.label} className="modelsw__group">
              <div className="modelsw__group-label" role="presentation"><Brain size={11} />{g.label}</div>
              {g.items.map((m) => (
                <button
                  key={m.ref}
                  type="button"
                  role="option"
                  aria-selected={m.current}
                  className={`modelsw__item ${m.current ? "modelsw__item--current" : ""}`}
                  onClick={() => pick(m.ref)}
                  disabled={busy}
                >
                  <span className="modelsw__copy">
                    <span className="modelsw__model">{m.model}</span>
                  </span>
                  {m.current ? (
                    <Check size={13} className="modelsw__check" />
                  ) : null}
                </button>
              ))}
            </div>
          ))}
        </div>
      </AnchoredPopover>
    </div>
  );
}

function providerLabel(provider: string, t: ReturnType<typeof useT>): string {
  switch (provider) {
    case "deepseek":
    case "deepseek-flash":
    case "deepseek-pro":
      return t("settings.providerLabel.deepseek");
    default:
      return provider;
  }
}
