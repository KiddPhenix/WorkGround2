import { ChevronUp, ChevronDown, CornerDownRight, GripVertical, Pencil, RotateCw, X } from "lucide-react";
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
  /** Retry a failed item (clears its error so it can drain again). */
  onRetry?: (queueItemId: string) => void;
  /** Steer a queued item into the running turn. Async; resolves on success. */
  onSteer?: (queueItemId: string) => Promise<void> | void;
  /** queueItemId of the item currently being steered — its 引导 button is disabled. */
  steeringId?: string | null;
}

// ── Component ───────────────────────────────────────────────────────────────

/**
 * QueueTray shows at most two summary lines plus a "+N" overflow indicator.
 * It is fully hidden (returns null) when the queue is empty.
 *
 * This is a pure presentational primitive — it does NOT subscribe to stores.
 */
export function QueueTray({ items, onEdit, onRemove, onMoveUp, onMoveDown, onRetry, onSteer, steeringId }: QueueTrayProps) {
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
          onRetry={onRetry}
          onSteer={onSteer}
          steeringId={steeringId}
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
  onRetry,
  onSteer,
  steeringId,
}: {
  item: QueueItem;
  index: number;
  total: number;
  onEdit?: (queueItemId: string) => void;
  onRemove?: (queueItemId: string) => void;
  onMoveUp?: (queueItemId: string) => void;
  onMoveDown?: (queueItemId: string) => void;
  onRetry?: (queueItemId: string) => void;
  onSteer?: (queueItemId: string) => Promise<void> | void;
  steeringId?: string | null;
}) {
  return (
    <div
      className={`queue-item-row${item.error ? " queue-item-row--error" : ""}`}
      role="listitem"
      aria-label={`队列项 ${index + 1}: ${truncate(item.content, 60)}`}
    >
      <span className="queue-item-row__handle" aria-hidden="true">
        <GripVertical size={14} />
      </span>

      <span className="queue-item-row__body">
        <span className="queue-item-row__content">{truncate(item.content, 80)}</span>
        {item.error && (
          <span className="queue-item-row__error" title={item.error}>{item.error}</span>
        )}
      </span>

      <span className="queue-item-row__actions">
        {onSteer && (
          <button
            type="button"
            className="queue-item-row__steer"
            aria-label="引导"
            disabled={steeringId === item.queueItemId}
            onClick={() => void onSteer(item.queueItemId)}
          >
            <CornerDownRight size={14} />
            <span>引导</span>
          </button>
        )}

        {onRetry && item.error && (
          <button
            type="button"
            className="icon-button"
            aria-label="重试"
            onClick={() => onRetry(item.queueItemId)}
          >
            <RotateCw size={14} />
          </button>
        )}

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
