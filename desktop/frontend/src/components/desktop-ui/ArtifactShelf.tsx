import {
  AlertCircle,
  Archive,
  Binary,
  Bug,
  CheckCircle2,
  ExternalLink,
  FileCode,
  FileText,
  Film,
  Image,
  Link,
  ListFilter,
  Loader2,
  Music,
  Package,
  RefreshCw,
  Search,
  TerminalSquare,
  X,
} from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { AnchoredPopover } from "../AnchoredPopover";
import type { ArtifactRecord, ArtifactStatus } from "../../store/artifacts";

// ── Props ──────────────────────────────────────────────────────────────────

export interface ArtifactItemProps {
  artifact: ArtifactRecord;
  /** Open / locate the artifact. */
  onOpen?: (artifactId: string) => void;
  /** Revalidate the artifact's availability. */
  onRevalidate?: (artifactId: string) => void;
  /** Regenerate the artifact. */
  onRegenerate?: (artifactId: string) => void;
}

export interface ArtifactShelfProps {
  artifacts: ArtifactRecord[];
  onOpen?: (artifactId: string) => void;
  onRevalidate?: (artifactId: string) => void;
  onRegenerate?: (artifactId: string) => void;
}

// ── Constants ──────────────────────────────────────────────────────────────

const MAX_SHELF_ITEMS = 6;
const POPOVER_SEARCH_THRESHOLD = 12;
const HISTORICAL_STATUSES: Set<ArtifactStatus> = new Set(["stale", "missing", "failed"]);

// ── Sort & group helpers ────────────────────────────────────────────────────

function sortByRecency(artifacts: ArtifactRecord[]): ArtifactRecord[] {
  return [...artifacts].sort((a, b) => {
    const aTime = a.lastVerifiedAt ?? 0;
    const bTime = b.lastVerifiedAt ?? 0;
    if (aTime !== bTime) return bTime - aTime;
    // Stable tie-break
    return a.artifactId.localeCompare(b.artifactId);
  });
}

interface ArtifactGroups {
  active: ArtifactRecord[];
  historical: ArtifactRecord[];
}

function groupArtifacts(artifacts: ArtifactRecord[]): ArtifactGroups {
  const active: ArtifactRecord[] = [];
  const historical: ArtifactRecord[] = [];
  for (const a of sortByRecency(artifacts)) {
    if (HISTORICAL_STATUSES.has(a.status)) {
      historical.push(a);
    } else {
      active.push(a);
    }
  }
  return { active, historical };
}

// ── Status helpers ─────────────────────────────────────────────────────────

const STATUS_LABEL: Record<ArtifactStatus, string> = {
  generating: "生成中",
  available: "可用",
  stale: "可能已过期",
  missing: "文件不存在",
  failed: "生成失败",
};

function statusIcon(status: ArtifactStatus, size = 14): React.ReactNode {
  switch (status) {
    case "generating":
      return <Loader2 size={size} className="animate-spin" />;
    case "available":
      return <CheckCircle2 size={size} />;
    case "stale":
      return <RefreshCw size={size} />;
    case "missing":
      return <AlertCircle size={size} />;
    case "failed":
      return <AlertCircle size={size} />;
  }
}

// ── Type icon map ──────────────────────────────────────────────────────────

function typeIcon(type: string, size = 20): React.ReactNode {
  switch (type) {
    case "code":
    case "go":
    case "ts":
    case "js":
    case "py":
      return <FileCode size={size} />;
    case "image":
    case "png":
    case "jpg":
    case "svg":
      return <Image size={size} />;
    case "binary":
    case "exe":
    case "appimage":
      return <Binary size={size} />;
    case "script":
    case "bat":
    case "cmd":
    case "ps1":
      return <TerminalSquare size={size} />;
    case "debug":
    case "bug":
      return <Bug size={size} />;
    case "url":
    case "preview":
      return <ExternalLink size={size} />;
    case "package":
      return <Package size={size} />;
    case "archive":
    case "zip":
    case "tar":
    case "gz":
      return <Archive size={size} />;
    case "link":
      return <Link size={size} />;
    case "log":
    case "txt":
      return <FileText size={size} />;
    case "video":
    case "film":
      return <Film size={size} />;
    case "audio":
      return <Music size={size} />;
    case "document":
    case "pdf":
      return <FileText size={size} />;
    default:
      return <FileText size={size} />;
  }
}

// ── ArtifactItem (preserved — used on shelf) ────────────────────────────────

export function ArtifactItem({ artifact, onOpen, onRevalidate, onRegenerate }: ArtifactItemProps) {
  const canOpen = artifact.status === "available";
  const showStatus = artifact.status !== "available";
  const statusActions: React.ReactNode[] = [];
  if (artifact.status === "stale") {
    if (onRevalidate) {
      statusActions.push(
        <ActionButton
          key="revalidate"
          icon={<RefreshCw size={12} />}
          label="重新校验"
          onClick={() => onRevalidate(artifact.artifactId)}
        />
      );
    }
    if (onRegenerate) {
      statusActions.push(
        <ActionButton
          key="regenerate"
          icon={<RefreshCw size={12} />}
          label="重新生成"
          onClick={() => onRegenerate(artifact.artifactId)}
        />
      );
    }
  }
  if (artifact.status === "missing" && onRegenerate) {
    statusActions.push(
      <ActionButton
        key="regenerate"
        icon={<RefreshCw size={12} />}
        label="重新生成"
        onClick={() => onRegenerate(artifact.artifactId)}
      />
    );
  }
  if (artifact.status === "failed" && onRegenerate) {
    statusActions.push(
      <ActionButton
        key="regenerate"
        icon={<RefreshCw size={12} />}
        label="重新生成"
        onClick={() => onRegenerate(artifact.artifactId)}
      />
    );
  }

  const actionable = Boolean((canOpen && onOpen) || statusActions.length > 0);
  const content = (
    <>
      {typeIcon(artifact.type, 20)}
      <span className="artifact-item__name" title={artifact.name}>{artifact.name}</span>
      {showStatus && <span className="artifact-item__status-badge">
        {statusIcon(artifact.status, 14)}
        <span className="artifact-item__status-label">{STATUS_LABEL[artifact.status]}</span>
      </span>}
    </>
  );

  return (
    <div
      className={`artifact-item artifact-item--${artifact.status}${actionable ? " artifact-item--actionable" : ""}`}
      role="listitem"
      aria-label={`${artifact.name} — ${STATUS_LABEL[artifact.status]}`}
    >
      {canOpen && onOpen ? (
        <button
          type="button"
          className="artifact-item__primary"
          aria-label={`打开 ${artifact.name}`}
          onClick={() => onOpen(artifact.artifactId)}
        >
          {content}
        </button>
      ) : <span className="artifact-item__primary">{content}</span>}
      {statusActions.length > 0 && (
        <span className="artifact-item__actions" aria-label={`${artifact.name} 操作`}>
          {statusActions}
        </span>
      )}
    </div>
  );
}

// ── ArtifactShelf ───────────────────────────────────────────────────────────

/**
 * ArtifactShelf always renders a 64px region, even when empty (empty state).
 *
 * Main shelf shows ≤6 most-recent non-historical artifacts sorted by
 * lastVerifiedAt descending.  Clicking “全部” opens an AnchoredPopover
 * anchored above the shelf with the full grouped list, search, and type
 * filter (search/filter visible only when >12 total).
 *
 * This is a pure presentational primitive — it does NOT subscribe to stores.
 */
export function ArtifactShelf({ artifacts, onOpen, onRevalidate, onRegenerate }: ArtifactShelfProps) {
  const groups = useMemo(() => groupArtifacts(artifacts), [artifacts]);
  const { active, historical } = groups;
  const total = artifacts.length;

  // Shelf display: max 6 most-recent active items
  const shelfItems = active.slice(0, MAX_SHELF_ITEMS);

  const [popoverOpen, setPopoverOpen] = useState(false);
  const allBtnRef = useRef<HTMLButtonElement>(null);

  return (
    <div
      className="artifact-shelf"
      role="region"
      aria-label={`产物架 — ${total} 项`}
    >
      <span className="artifact-shelf__count">产物 {total}</span>

      {total === 0 && (
        <span className="artifact-shelf__empty">暂无产物</span>
      )}

      {total > 0 && (
        <button
          ref={allBtnRef}
          type="button"
          className="artifact-shelf__all-btn"
          aria-label="查看全部产物"
          aria-haspopup="dialog"
          aria-expanded={popoverOpen}
          onClick={() => setPopoverOpen((v) => !v)}
        >
          全部
        </button>
      )}

      {total > 0 && (
        <div className="artifact-shelf__recent" role="list" aria-label="最近产物">
          {shelfItems.map((art) => (
            <ArtifactItem
              key={art.artifactId}
              artifact={art}
              onOpen={onOpen}
              onRevalidate={onRevalidate}
              onRegenerate={onRegenerate}
            />
          ))}
        </div>
      )}

      {total > 0 && (
        <AnchoredPopover
          open={popoverOpen}
          anchorRef={allBtnRef}
          onClose={() => setPopoverOpen(false)}
          className="artifact-popover"
          align="start"
        >
          <ArtifactPopoverContent
            active={active}
            historical={historical}
            onOpen={onOpen ? (artifactId) => {
              onOpen(artifactId);
              setPopoverOpen(false);
            } : undefined}
            onRevalidate={onRevalidate}
            onRegenerate={onRegenerate}
            onClose={() => setPopoverOpen(false)}
          />
        </AnchoredPopover>
      )}
    </div>
  );
}

// ── Popover content ─────────────────────────────────────────────────────────

function ArtifactPopoverContent({
  active,
  historical,
  onOpen,
  onRevalidate,
  onRegenerate,
  onClose,
}: {
  active: ArtifactRecord[];
  historical: ArtifactRecord[];
  onOpen?: (artifactId: string) => void;
  onRevalidate?: (artifactId: string) => void;
  onRegenerate?: (artifactId: string) => void;
  onClose: () => void;
}) {
  const total = active.length + historical.length;
  const [search, setSearch] = useState("");
  const [typeFilter, setTypeFilter] = useState("");

  const availableTypes = useMemo(() => {
    const types = new Set<string>();
    for (const a of active) types.add(a.type);
    for (const a of historical) types.add(a.type);
    return [...types].sort();
  }, [active, historical]);

  const filteredActive = useMemo(
    () => filterGroup(active, search, typeFilter),
    [active, search, typeFilter],
  );
  const filteredHistorical = useMemo(
    () => filterGroup(historical, search, typeFilter),
    [historical, search, typeFilter],
  );
  const filteredTotal = filteredActive.length + filteredHistorical.length;

  const showSearch = total > POPOVER_SEARCH_THRESHOLD;

  return (
    <div className="artifact-popover__wrap" role="dialog" aria-label="全部产物">
      <div className="artifact-popover__head">
        <span className="artifact-popover__title">全部产物 · {total}</span>
        <button
          type="button"
          className="artifact-popover__close"
          aria-label="关闭"
          onClick={onClose}
        >
          <X size={14} />
        </button>
      </div>

      {showSearch && (
        <div className="artifact-popover__filters">
          <div className="artifact-popover__search-wrap">
            <Search size={13} className="artifact-popover__search-icon" />
            <input
              type="text"
              className="artifact-popover__search-input"
              placeholder="搜索名称、路径或类型…"
              value={search}
              onInput={(e) => setSearch(e.currentTarget.value)}
            />
            {search && (
              <button
                type="button"
                className="artifact-popover__search-clear"
                aria-label="清除搜索"
                onClick={() => setSearch("")}
              >
                <X size={12} />
              </button>
            )}
          </div>
          <div className="artifact-popover__type-select-wrap">
            <ListFilter size={13} className="artifact-popover__type-icon" />
            <select
              className="artifact-popover__type-select"
              value={typeFilter}
              onChange={(e) => setTypeFilter(e.target.value)}
            >
              <option value="">全部类型</option>
              {availableTypes.map((t) => (
                <option key={t} value={t}>{t}</option>
              ))}
            </select>
          </div>
        </div>
      )}

      <div className="artifact-popover__list">
        {filteredActive.length > 0 && (
          <>
            <div className="artifact-popover__group-title">可用 / 进行中</div>
            {filteredActive.map((a) => (
              <PopoverItem
                key={a.artifactId}
                artifact={a}
                onOpen={onOpen}
                onRevalidate={onRevalidate}
                onRegenerate={onRegenerate}
              />
            ))}
          </>
        )}

        {filteredHistorical.length > 0 && (
          <>
            <div className="artifact-popover__group-title">历史</div>
            {filteredHistorical.map((a) => (
              <PopoverItem
                key={a.artifactId}
                artifact={a}
                onOpen={onOpen}
                onRevalidate={onRevalidate}
                onRegenerate={onRegenerate}
              />
            ))}
          </>
        )}

        {filteredTotal === 0 && (
          <div className="artifact-popover__empty">
            {search || typeFilter
              ? "没有匹配的产物"
              : "暂无产物"}
          </div>
        )}
      </div>
    </div>
  );
}

export function filterGroup(
  items: ArtifactRecord[],
  search: string,
  typeFilter: string,
): ArtifactRecord[] {
  const q = search.trim().toLowerCase();
  return items.filter((a) => {
    if (typeFilter && a.type !== typeFilter) return false;
    if (!q) return true;
    return (
      a.name.toLowerCase().includes(q) ||
      (a.relativePath ?? "").toLowerCase().includes(q) ||
      a.type.toLowerCase().includes(q)
    );
  });
}

function PopoverItem({
  artifact,
  onOpen,
  onRevalidate,
  onRegenerate,
}: {
  artifact: ArtifactRecord;
  onOpen?: (artifactId: string) => void;
  onRevalidate?: (artifactId: string) => void;
  onRegenerate?: (artifactId: string) => void;
}) {
  const canOpen = artifact.status === "available" && Boolean(onOpen);
  const Tag = canOpen ? "button" : "span";
  const props = canOpen
    ? {
        type: "button" as const,
        onClick: () => onOpen?.(artifact.artifactId),
      }
    : {};

  return (
    <Tag
      className={`artifact-popover__item artifact-popover__item--${artifact.status}`}
      {...props}
    >
      <span className="artifact-popover__item-icon">
        {typeIcon(artifact.type, 18)}
      </span>
      <span className="artifact-popover__item-body">
        <span className="artifact-popover__item-name" title={artifact.name}>{artifact.name}</span>
        {artifact.relativePath && (
          <span className="artifact-popover__item-path" title={artifact.relativePath}>{artifact.relativePath}</span>
        )}
      </span>
      <span className="artifact-popover__item-status">
        {statusIcon(artifact.status, 12)}
        <span>{STATUS_LABEL[artifact.status]}</span>
      </span>
      {(artifact.status === "stale" || artifact.status === "missing" || artifact.status === "failed") && (
        <span className="artifact-popover__item-actions" aria-label={`${artifact.name} 操作`}>
          {artifact.status === "stale" && onRevalidate && (
            <ActionButton
              icon={<RefreshCw size={12} />}
              label="重新校验"
              onClick={() => onRevalidate(artifact.artifactId)}
            />
          )}
          {onRegenerate && (
            <ActionButton
              icon={<RefreshCw size={12} />}
              label="重新生成"
              onClick={() => onRegenerate(artifact.artifactId)}
            />
          )}
        </span>
      )}
    </Tag>
  );
}

// ── Internal ────────────────────────────────────────────────────────────────

function ActionButton({
  icon,
  label,
  onClick,
}: {
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="action-button"
      aria-label={label}
      onClick={onClick}
    >
      {icon}
    </button>
  );
}
