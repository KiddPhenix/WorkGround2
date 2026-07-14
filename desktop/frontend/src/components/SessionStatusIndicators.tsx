// SessionStatusIndicators — global session status badges in the session header.
//
// Renders two controls before the AddOn launcher:
//   1. "运行中 N" — only when N > 0; hover shows a compact floating list of
//      running sessions. Click any row to switch to that tab.
//   2. "待关注 N" — Sparkles icon + count, full-button click area. Click jumps
//      to the next needs-attention session (earliest completed first). Disabled
//      (greyed out) when N === 0.
import { useCallback, useMemo, useRef, useState } from "react";
import { Sparkles } from "lucide-react";
import type { TabMeta } from "../lib/types";
import type { Translator } from "../lib/i18n";

interface Props {
  tabs: TabMeta[];
  activeTabId: string | undefined;
  onSwitchTab: (tab: TabMeta) => void;
  t: Translator;
}

// ── helpers ──────────────────────────────────────────────────────────────────

function isRunningWork(tab: TabMeta): boolean {
  if (typeof tab.runningWork === "boolean") return tab.runningWork;
  if (typeof tab.running === "boolean") return tab.running;
  if (tab.pendingPrompt || tab.runtimeMode === "waiting_user") return false;
  return tab.runtimeMode === "foreground"
    || tab.runtimeMode === "background_only"
    || tab.runtimeMode === "cancelling";
}

function runningTabs(tabs: TabMeta[]): TabMeta[] {
  return tabs.filter(isRunningWork);
}

function needsAttentionTabs(tabs: TabMeta[]): TabMeta[] {
  return tabs
    .filter((tab) => tab.needsAttention && tab.sessionSource?.trim().toLowerCase() !== "cli")
    .sort((a, b) => {
      const aAt = a.needsAttentionAt && a.needsAttentionAt > 0 ? a.needsAttentionAt : Number.MAX_SAFE_INTEGER;
      const bAt = b.needsAttentionAt && b.needsAttentionAt > 0 ? b.needsAttentionAt : Number.MAX_SAFE_INTEGER;
      return aAt - bAt || a.id.localeCompare(b.id);
    });
}

function relativeTimeLabel(ts: number | undefined, t: Translator): string {
  if (!ts || ts <= 0) return "";
  const diff = Date.now() - ts;
  const seconds = Math.floor(diff / 1000);
  if (seconds < 60) return t("sessionStatus.justNow");
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return t("sessionStatus.minutesAgo", { n: String(minutes) });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t("sessionStatus.hoursAgo", { n: String(hours) });
  const days = Math.floor(hours / 24);
  return t("sessionStatus.daysAgo", { n: String(days) });
}

function tabLabel(tab: TabMeta): string {
  return tab.sessionDisplayTitle || tab.topicTitle || tab.label || tab.workspaceName || tab.id;
}

function tabSubLabel(tab: TabMeta): string {
  const parts: string[] = [];
  if (tab.workspaceName) parts.push(tab.workspaceName);
  if (tab.sessionSource === "cli") parts.push("CLI");
  return parts.join(" · ");
}

// ── RunningIndicator ─────────────────────────────────────────────────────────

function RunningIndicator({ tabs, activeTabId, onSwitchTab, t }: Props) {
  const running = useMemo(() => runningTabs(tabs), [tabs]);
  const [listOpen, setListOpen] = useState(false);
  const containerRef = useRef<HTMLSpanElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const showList = useCallback(() => setListOpen(true), []);
  const hideList = useCallback(() => setListOpen(false), []);

  if (running.length === 0) return null;

  const handleRowClick = (tab: TabMeta) => {
    setListOpen(false);
    if (tab.id !== activeTabId) onSwitchTab(tab);
  };

  return (
    <span
      ref={containerRef}
      className="session-status-indicator session-status-indicator--running"
      onMouseEnter={showList}
      onMouseLeave={hideList}
      onFocus={showList}
      onBlur={(e) => {
        if (!containerRef.current?.contains(e.relatedTarget as Node)) hideList();
      }}
      tabIndex={0}
      role="button"
      aria-haspopup="listbox"
      aria-expanded={listOpen}
      aria-label={t("sessionStatus.runningCount", { n: String(running.length) })}
    >
      <span className="session-status-indicator__dot" aria-hidden="true" />
      <span className="session-status-indicator__label">{t("sessionStatus.running")}</span>
      <span className="session-status-indicator__count">{running.length}</span>
      {listOpen && (
        <div ref={listRef} className="session-status-popup" role="listbox">
          {running.map((tab) => (
            <div
              key={tab.id}
              className={`session-status-popup__row${tab.id === activeTabId ? " session-status-popup__row--active" : ""}`}
              role="option"
              aria-selected={tab.id === activeTabId}
              onClick={() => handleRowClick(tab)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") handleRowClick(tab);
              }}
              tabIndex={0}
            >
              <span className="session-status-popup__row-title">{tabLabel(tab)}</span>
              <span className="session-status-popup__row-meta">
                {tabSubLabel(tab)}
                {tab.turnStartedAt ? ` · ${relativeTimeLabel(tab.turnStartedAt, t)}` : ""}
              </span>
            </div>
          ))}
        </div>
      )}
    </span>
  );
}

// ── NeedsAttentionIndicator ──────────────────────────────────────────────────

function NeedsAttentionIndicator({ tabs, onSwitchTab, t }: Props) {
  const items = useMemo(() => needsAttentionTabs(tabs), [tabs]);

  const handleClick = useCallback(() => {
    if (items.length === 0) return;
    // Jump to the earliest-completed needs-attention session.
    onSwitchTab(items[0]);
  }, [items, onSwitchTab]);

  return (
    <button
      type="button"
      className="session-status-indicator session-status-indicator--attention"
      disabled={items.length === 0}
      onClick={handleClick}
      aria-label={
        items.length > 0
          ? t("sessionStatus.needsAttentionCount", { n: String(items.length) })
          : t("sessionStatus.needsAttention")
      }
    >
      <Sparkles size={14} aria-hidden="true" className="session-status-indicator__icon" />
      <span className="session-status-indicator__label">{t("sessionStatus.needsAttention")}</span>
      <span className="session-status-indicator__count">{items.length}</span>
    </button>
  );
}

// ── SessionStatusIndicators ──────────────────────────────────────────────────

export function SessionStatusIndicators({ tabs, activeTabId, onSwitchTab, t }: Props) {
  return (
    <>
      <RunningIndicator tabs={tabs} activeTabId={activeTabId} onSwitchTab={onSwitchTab} t={t} />
      <NeedsAttentionIndicator tabs={tabs} activeTabId={activeTabId} onSwitchTab={onSwitchTab} t={t} />
    </>
  );
}
