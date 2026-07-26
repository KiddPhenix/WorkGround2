import React, { useCallback, useState } from 'react';

import type { TaskV2View, TaskStateV2, NodeDef } from '../../types_v2';

// ── Handler intents ────────────────────────────────────────────────────────

export interface TaskExpandIntent {
  workId: string;
  taskId: string;
}

export interface TaskRetryIntent {
  workId: string;
  taskId: string;
  runId: string;
}

// ── State display data (not only color) ─────────────────────────────────────

const STATE_LABELS: Record<TaskStateV2, string> = {
  pending: '等待中',
  ready: '就绪',
  running: '运行中',
  waiting_input: '等待输入',
  waiting_approval: '等待批准',
  completed: '已完成',
  failed_retryable: '失败（可重试）',
  failed_terminal: '失败（不可恢复）',
  canceled: '已取消',
  invalidated: '待重新生成',
};

const STATE_ICONS: Record<TaskStateV2, string> = {
  pending: '○',
  ready: '◌',
  running: '◉',
  waiting_input: '✎',
  waiting_approval: '⚠',
  completed: '✓',
  failed_retryable: '↻',
  failed_terminal: '✗',
  canceled: '⊘',
  invalidated: '⏻',
};

const STATE_BADGE: Record<TaskStateV2, string> = {
  pending: 'neutral',
  ready: 'info',
  running: 'info',
  waiting_input: 'warn',
  waiting_approval: 'warn',
  completed: 'success',
  failed_retryable: 'error',
  failed_terminal: 'error',
  canceled: 'neutral',
  invalidated: 'error',
};

// ── ExecutionRow ───────────────────────────────────────────────────────────

export interface ExecutionRowProps {
  /** Task view from the store projection. */
  task: TaskV2View;
  /** Work ID for intent construction. */
  workId: string;
  /** Optional node definition for extra context. */
  nodeDef?: NodeDef;
  /** Whether this row is currently expanded. */
  isExpanded: boolean;
  /** Called when user toggles expand/collapse. */
  onToggleExpand: (intent: TaskExpandIntent) => void;
  /** Called when user wants to retry a failed task. */
  onRetry?: (intent: TaskRetryIntent) => void | Promise<void>;
}

export const ExecutionRow: React.FC<ExecutionRowProps> = ({
  task,
  workId,
  isExpanded,
  onToggleExpand,
  onRetry,
}) => {
  const [retrying, setRetrying] = useState(false);
  const [retryError, setRetryError] = useState<string | null>(null);
  const handleClick = useCallback(() => {
    onToggleExpand({ workId, taskId: task.id });
  }, [onToggleExpand, workId, task.id]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        onToggleExpand({ workId, taskId: task.id });
      }
    },
    [onToggleExpand, workId, task.id],
  );

  const handleRetry = useCallback(
    async (e: React.MouseEvent) => {
      e.stopPropagation();
      if (!onRetry || (task.state !== 'failed_retryable' && task.state !== 'invalidated') || retrying) return;
      setRetrying(true);
      setRetryError(null);
      try {
        await onRetry({ workId, taskId: task.id, runId: task.runId });
      } catch (error) {
        setRetryError(error instanceof Error ? error.message : String(error));
      } finally {
        setRetrying(false);
      }
    },
    [onRetry, retrying, workId, task.id, task.runId, task.state],
  );

  const badgeClass = STATE_BADGE[task.state];
  const progressValue = task.state === 'running' ? progressPercent(task.progress) : null;

  return (
    <div
      className="wg2-er-row"
      data-task-state={task.state}
      data-task-id={task.id}
      data-testid={`execution-row-${task.id}`}
      aria-label={`${task.title} — ${STATE_LABELS[task.state]}${isExpanded ? '（已展开）' : ''}`}
    >
      {/* ── Header (always visible, clickable) ───────────────────── */}
      <div
        className="wg2-er-header"
        onClick={handleClick}
        onKeyDown={handleKeyDown}
        role="button"
        tabIndex={0}
        aria-expanded={isExpanded}
        aria-controls={`expanded-block-${task.id}`}
        data-testid={`execution-row-header-${task.id}`}
      >
        {/* State icon */}
        <span
          className="wg2-er-icon"
          aria-hidden="true"
          data-testid={`execution-row-icon-${task.id}`}
        >
          {STATE_ICONS[task.state]}
        </span>

        {/* Title */}
        <span className="wg2-er-title" title={task.title}>
          {task.title}
        </span>

        {/* Progress bar for running */}
        {progressValue !== null && (
          <div
            className="wg2-er-progress"
            role="progressbar"
            aria-valuenow={progressValue}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-label={`${task.title} 进度`}
          >
            <div
              className="wg2-er-progress-fill"
              style={{ width: `${progressValue}%` }}
            />
          </div>
        )}

        {/* State badge */}
        <span
          className="wg2-er-badge"
          data-badge={badgeClass}
          data-testid={`execution-row-badge-${task.id}`}
        >
          {STATE_LABELS[task.state]}
        </span>

        {/* Failed retry button (inline) */}
        {(task.state === 'failed_retryable' || task.state === 'invalidated') && onRetry && (
          <button
            type="button"
            className="wg2-eb-btn wg2-eb-btn-danger"
            onClick={(event) => void handleRetry(event)}
            disabled={retrying}
            aria-busy={retrying ? 'true' : undefined}
            aria-label={`重试 ${task.title}`}
            data-testid={`execution-row-retry-${task.id}`}
          >
            {retrying ? '重试中…' : '重试'}
          </button>
        )}

        {/* Expand chevron */}
        <span
          className="wg2-er-chevron"
          aria-hidden="true"
          data-expanded={isExpanded}
        >
          ▶
        </span>
      </div>
      {retryError && (
        <div role="alert" className="wg2-eb-error" data-testid={`execution-row-retry-error-${task.id}`}>
          {retryError}
        </div>
      )}
    </div>
  );
};

function progressPercent(progress: string | undefined): number | null {
  if (!progress) return null;
  const trimmed = progress.trim();
  const numeric = Number.parseFloat(trimmed);
  if (!Number.isFinite(numeric)) return null;
  const percent = trimmed.endsWith('%') ? numeric : numeric <= 1 ? numeric * 100 : numeric;
  return Math.max(0, Math.min(100, Math.round(percent)));
}
