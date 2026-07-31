import React, { useCallback, useState } from 'react';
import {
  Ban,
  Check,
  ChevronRight,
  Circle,
  Clock3,
  Info,
  LoaderCircle,
  MessageCircle,
  Pause,
  PencilLine,
  RefreshCw,
  RotateCcw,
  ShieldAlert,
  X,
} from 'lucide-react';

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
  /** Opens discussion for this task without changing its expanded state. */
  onDiscuss?: () => void;
  /** Opens the linked execution session on the back face. */
  onInfo?: () => void;
  /** Presentation-only pause overlay; the Task remains running for resume. */
  paused?: boolean;
}

export const ExecutionRow: React.FC<ExecutionRowProps> = ({
  task,
  workId,
  isExpanded,
  onToggleExpand,
  onRetry,
  onDiscuss,
  onInfo,
  paused = false,
}) => {
  const [retrying, setRetrying] = useState(false);
  const [retryError, setRetryError] = useState<string | null>(null);
  const handleClick = useCallback(() => {
    onToggleExpand({ workId, taskId: task.id });
  }, [onToggleExpand, workId, task.id]);

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
  const handleDiscuss = useCallback((event: React.MouseEvent) => {
    event.stopPropagation();
    onDiscuss?.();
  }, [onDiscuss]);
  const handleInfo = useCallback((event: React.MouseEvent) => {
    event.stopPropagation();
    onInfo?.();
  }, [onInfo]);
  const handleChevron = useCallback((event: React.MouseEvent) => {
    event.stopPropagation();
    handleClick();
  }, [handleClick]);

  const pausedRunning = paused && task.state === 'running';
  const stateLabel = pausedRunning ? '已暂停' : STATE_LABELS[task.state];
  const badgeClass = pausedRunning ? 'warn' : STATE_BADGE[task.state];
  const progressValue = task.state === 'running' ? progressPercent(task.progress) : null;
  const liveOutput = task.state === 'running' && progressValue === null
    ? task.progress?.trim() ?? ''
    : '';

  return (
    <div
      className="wg2-er-row"
      data-task-state={task.state}
      data-paused={pausedRunning ? 'true' : undefined}
      data-task-id={task.id}
      data-testid={`execution-row-${task.id}`}
      aria-label={`${task.title} — ${stateLabel}${isExpanded ? '（已展开）' : ''}`}
    >
      {/* ── Header (always visible, clickable) ───────────────────── */}
      <div
        className="wg2-er-header"
        onClick={handleClick}
        data-testid={`execution-row-header-${task.id}`}
      >
        {/* State icon */}
        <span
          className="wg2-er-icon"
          data-tone={badgeClass}
          aria-hidden="true"
          data-testid={`execution-row-icon-${task.id}`}
        >
          {pausedRunning ? <Pause size={19} /> : stateIcon(task.state)}
        </span>

        <span className="wg2-er-copy">
          <span className="wg2-er-title" title={task.title}>
            {task.title}
          </span>
          <span className="wg2-er-meta">
            <span
              className="wg2-er-badge"
              data-badge={badgeClass}
              data-testid={`execution-row-badge-${task.id}`}
            >
              {stateLabel}
            </span>
            {task.progress && progressValue === null && task.state !== 'running' && (
              <span className="wg2-er-progress-copy"> · {task.progress}</span>
            )}
          </span>
        </span>

        {task.state === 'running' && (
          <div
            className="wg2-er-live"
            data-has-output={liveOutput ? 'true' : 'false'}
            data-paused={pausedRunning ? 'true' : undefined}
            data-testid={`execution-row-live-${task.id}`}
          >
            <div
              className="wg2-er-live-viewport"
              aria-label={pausedRunning
                ? `已暂停：${liveOutput || '等待继续'}`
                : liveOutput ? `模型输出：${liveOutput}` : '等待模型输出'}
              aria-live="polite"
            >
              <span className="wg2-er-live-track">
                <span className="wg2-er-live-copy">{pausedRunning ? liveOutput || '已暂停' : liveOutput || '等待模型输出…'}</span>
                {!pausedRunning && (
                  <span className="wg2-er-live-copy" aria-hidden="true">{liveOutput || '等待模型输出…'}</span>
                )}
              </span>
            </div>
            {onInfo && (
              <button
                type="button"
                className="wg2-er-info wg2-er-info-live"
                onClick={handleInfo}
                aria-label={`打开 ${task.title} 的隐藏会话`}
                data-testid={`execution-row-info-${task.id}`}
              >
                <Info size={15} aria-hidden="true" />
              </button>
            )}
          </div>
        )}

        {/* Progress bar for running */}
        {progressValue !== null && (
          <div className="wg2-er-progress-group">
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
            <span className="wg2-er-progress-value">{progressValue}%</span>
          </div>
        )}

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

        {onInfo && task.state !== 'running' && (
          <button
            type="button"
            className="wg2-er-info"
            onClick={handleInfo}
            aria-label={`查看 ${task.title} 的会话`}
            data-testid={`execution-row-info-${task.id}`}
          >
            <Info size={15} aria-hidden="true" />
          </button>
        )}

        {onDiscuss && (
          <button
            type="button"
            className="wg2-er-discuss"
            onClick={handleDiscuss}
            aria-label={`${task.state === 'completed' ? '提出修改意见' : '讨论'} ${task.title}`}
            data-testid={`execution-row-discuss-${task.id}`}
          >
            <MessageCircle size={15} aria-hidden="true" />
            <span>{task.state === 'completed' ? '修改意见' : '讨论'}</span>
          </button>
        )}

        {/* Expand chevron */}
        <button
          type="button"
          className="wg2-er-chevron"
          onClick={handleChevron}
          aria-label={`${isExpanded ? '收起' : '展开'} ${task.title}`}
          aria-expanded={isExpanded}
          aria-controls={`expanded-block-${task.id}`}
          data-expanded={isExpanded}
          data-testid={`execution-row-toggle-${task.id}`}
        >
          <ChevronRight size={16} aria-hidden="true" />
        </button>
      </div>
      {retryError && (
        <div role="alert" className="wg2-eb-error" data-testid={`execution-row-retry-error-${task.id}`}>
          {retryError}
        </div>
      )}
    </div>
  );
};

function stateIcon(state: TaskStateV2): React.ReactNode {
  switch (state) {
    case 'pending':
      return <Clock3 size={19} />;
    case 'ready':
      return <Circle size={19} />;
    case 'running':
      return <LoaderCircle size={19} />;
    case 'waiting_input':
      return <PencilLine size={19} />;
    case 'waiting_approval':
      return <ShieldAlert size={19} />;
    case 'completed':
      return <Check size={19} />;
    case 'failed_retryable':
      return <RotateCcw size={19} />;
    case 'failed_terminal':
      return <X size={19} />;
    case 'canceled':
      return <Ban size={19} />;
    case 'invalidated':
      return <RefreshCw size={19} />;
  }
}

function progressPercent(progress: string | undefined): number | null {
  if (!progress) return null;
  const trimmed = progress.trim();
  if (!/^\d+(?:\.\d+)?%?$/.test(trimmed)) return null;
  const numeric = Number.parseFloat(trimmed);
  if (!Number.isFinite(numeric)) return null;
  const percent = trimmed.endsWith('%') ? numeric : numeric <= 1 ? numeric * 100 : numeric;
  return Math.max(0, Math.min(100, Math.round(percent)));
}
