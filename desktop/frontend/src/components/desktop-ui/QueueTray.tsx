import { ChevronUp, ChevronDown, GripVertical, Pencil, X } from "lucide-react";
import type { QueueItem } from "../../store/composerQueue";

// ── Props ──────────────────────────────────────────────────────────────────

export interface QueueTrayProps {
  items: QueueItem[];
  /** Edit a queued item. */
  onEdit?: (queueItemId: string) => void;
  /** Remove a queued item. */
  onRemove?: (queueItemId: string) => void;
  /** Move an item up (fromIndex → fromIndex - 1). */
  onMoveUp?: (queueItemId: string) => void;
  /** Move an item down (fromIndex → fromIndex + 1). */
  onMoveDown?: (queueItemId: string) => void;
}

// ── Component ───────────────────────────────────────────────────────────────

/**
 * QueueTray shows at most two summary lines plus a "+N" overflow indicator.
 * It is fully hidden (returns null) when the queue is empty.
 *
 * This is a pure presentational primitive — it does NOT subscribe to stores.
 */
export function QueueTray({ items, onEdit, onRemove, onMoveUp, onMoveDown }: QueueTrayProps) {
  if (items.length === 0) return null;

  const visible = items.slice(0, 2);
  const overflow = items.length - 2;

  return (
    <div
      className="queue-tray"
      role="region"
      aria-label={`队列 — ${items.length} 项`}
      aria-live="polite"
    >
      {visible.map((item, idx) => (
        <QueueItemRow
          key={item.queueItemId}
          item={item}
          index={idx}
          total={items.length}
          onEdit={onEdit}
          onRemove={onRemove}
          onMoveUp={onMoveUp}
          onMoveDown={onMoveDown}
        />
      ))}
      {overflow > 0 && (
        <div className="queue-tray__overflow">
          <span>+{overflow} 更多</span>
        </div>
      )}
    </div>
  );
}

// ── QueueItemRow ────────────────────────────────────────────────────────────

function QueueItemRow({
  item,
  index,
  total,
  onEdit,
  onRemove,
  onMoveUp,
  onMoveDown,
}: {
  item: QueueItem;
  index: number;
  total: number;
  onEdit?: (queueItemId: string) => void;
  onRemove?: (queueItemId: string) => void;
  onMoveUp?: (queueItemId: string) => void;
  onMoveDown?: (queueItemId: string) => void;
}) {
  return (
    <div
      className="queue-item-row"
      role="listitem"
      aria-label={`队列项 ${index + 1}: ${truncate(item.content, 60)}`}
    >
      <span className="queue-item-row__handle" aria-hidden="true">
        <GripVertical size={14} />
      </span>

      <span className="queue-item-row__content">{truncate(item.content, 80)}</span>

      <span className="queue-item-row__actions">
        {onEdit && (
          <button
            type="button"
            className="icon-button"
            aria-label="编辑"
            onClick={() => onEdit(item.queueItemId)}
          >
            <Pencil size={14} />
          </button>
        )}

        {onMoveUp && index > 0 && (
          <button
            type="button"
            className="icon-button"
            aria-label="上移"
            onClick={() => onMoveUp(item.queueItemId)}
          >
            <ChevronUp size={14} />
          </button>
        )}

        {onMoveDown && index < total - 1 && (
          <button
            type="button"
            className="icon-button"
            aria-label="下移"
            onClick={() => onMoveDown(item.queueItemId)}
          >
            <ChevronDown size={14} />
          </button>
        )}

        {onRemove && (
          <button
            type="button"
            className="icon-button"
            aria-label="移除"
            onClick={() => onRemove(item.queueItemId)}
          >
            <X size={14} />
          </button>
        )}
      </span>
    </div>
  );
}

// ── Shared ──────────────────────────────────────────────────────────────────

function truncate(text: string, max: number): string {
  if (text.length <= max) return text;
  return text.slice(0, max - 1) + "…";
}
