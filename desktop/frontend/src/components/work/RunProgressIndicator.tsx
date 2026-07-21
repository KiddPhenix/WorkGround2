import React, { useCallback } from 'react';

import type {
  Attempt,
  RetryIntent,
  RetryStatus,
  RunSelection,
  SessionRef,
  Stage,
  Task,
  Work,
  WorkflowRun,
} from '../../work/types';
import {
  isAttemptTerminal,
  isStageTerminal,
  isTaskTerminal,
  attemptKey,
  selectRetry,
  selectHasPendingRetry,
  stageKey,
  taskKey,
} from '../../work/store';

export interface RunProgressIndicatorProps {
  work: Work;
  selection?: RunSelection;
  onSelect: (selection: RunSelection) => void;
  onRetry?: (intent: RetryIntent) => void | Promise<void>;
  retryByTarget: Record<string, RetryStatus>;
  readonly: boolean;
  archived: boolean;
}

type RunStateLabel = WorkflowRun['state'];

function stateLabel(state: string): string {
  switch (state) {
    case 'pending': return '等待中';
    case 'running': return '运行中';
    case 'waiting': return '等待输入';
    case 'completed': return '已完成';
    case 'failed': return '失败';
    case 'cancelled': return '已取消';
    case 'needs_confirmation': return '需人工确认';
    default: return state;
  }
}

function timeLabel(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

function sessionSummary(ref: SessionRef): string {
  const parts: string[] = [];
  if (ref.modelRef) parts.push(ref.modelRef);
  if (ref.turnCount > 0) parts.push(`${ref.turnCount} 轮`);
  if (ref.preview) parts.push(ref.preview.slice(0, 60));
  return parts.join(' · ') || 'Session';
}

function isSelected(sel: RunSelection | undefined, runId: string, stageId?: string, taskId?: string, attemptId?: string, attemptIndex?: number): boolean {
  if (!sel || sel.runId !== runId) return false;
  if (stageId !== undefined && sel.stageId !== stageId) return false;
  if (taskId !== undefined && sel.taskId !== taskId) return false;
  if (attemptId !== undefined && sel.attemptId !== attemptId) return false;
  if (attemptIndex !== undefined && sel.attemptIndex !== attemptIndex) return false;
  return true;
}

function retryRequestID(workId: string, runId: string, stageId: string, taskId: string, attemptId: string): string {
  return `retry:${workId}:${runId}:${stageId}:${taskId}:${attemptId}`;
}

const RunStateBadge: React.FC<{ state: RunStateLabel }> = ({ state }) => (
  <span className="wg2-run-state-badge" data-run-state={state}>
    {stateLabel(state)}
  </span>
);

const AttemptDetail: React.FC<{
  attempt: Attempt;
  runId: string;
  stageId: string;
  taskId: string;
  taskName: string;
  selection?: RunSelection;
  onSelect: (sel: RunSelection) => void;
  onRetry?: (intent: RetryIntent) => void | Promise<void>;
  retryByTarget: Record<string, RetryStatus>;
  readonly: boolean;
  archived: boolean;
  workId: string;
  latest: boolean;
}> = ({ attempt, runId, stageId, taskId, taskName, selection, onSelect, onRetry, retryByTarget, readonly, archived, workId, latest }) => {
  const currentAttemptId = attemptKey(attempt);
  const selected = isSelected(selection, runId, stageId, taskId, currentAttemptId, attempt.index);
  const terminal = isAttemptTerminal(attempt);
  const needsConfirmation = attempt.state === 'needs_confirmation';
  const canRetry = latest && (attempt.state === 'failed' || needsConfirmation) && onRetry && !readonly && !archived;
  const target = { workId, runId, stageId, taskId };
  const retry = selectRetry(retryByTarget, target);
  const hasPending = selectHasPendingRetry(retryByTarget, target);

  const handleClick = useCallback((event?: React.SyntheticEvent) => {
    event?.stopPropagation();
    onSelect({ runId, stageId, taskId, attemptId: currentAttemptId, attemptIndex: attempt.index });
  }, [onSelect, runId, stageId, taskId, currentAttemptId, attempt.index]);

  const handleRetry = useCallback((e: React.MouseEvent) => {
    e.stopPropagation();
    if (!canRetry || hasPending) return;
    const intent: RetryIntent = retry?.intent ?? {
      ...target,
      attemptId: attempt.id,
      attemptIndex: attempt.index,
      requestId: retryRequestID(workId, runId, stageId, taskId, currentAttemptId),
    };
    void onRetry!(intent);
  }, [attempt.id, attempt.index, canRetry, currentAttemptId, hasPending, onRetry, retry, runId, stageId, target, taskId, workId]);

  return (
    <li
      className={`wg2-run-attempt${selected ? ' wg2-run-selected' : ''}${terminal ? ' wg2-run-terminal' : ''}`}
      data-run-attempt={attempt.index}
      data-work-target-id={`attempt:${runId}:${stageId}:${taskId}:${currentAttemptId}`}
      role="treeitem"
      aria-selected={selected}
      tabIndex={0}
      onClick={handleClick}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleClick(e); } }}
    >
      <span className="wg2-run-attempt-index">#{attempt.index + 1}</span>
      <RunStateBadge state={attempt.state} />
      <span className="wg2-run-time">
        {timeLabel(attempt.startedAt)}
        {attempt.finishedAt && ` → ${timeLabel(attempt.finishedAt)}`}
      </span>
      {attempt.error && (
        <span className="wg2-run-error" title={attempt.error}>
          {attempt.error.slice(0, 120)}{attempt.error.length > 120 ? '…' : ''}
        </span>
      )}
      {needsConfirmation && (
        <span className="wg2-run-confirmation" role="alert">
          外部结果尚未确认，请核实实际结果后再决定是否重试。
        </span>
      )}
      {attempt.receipt && (
        <span className="wg2-run-receipt" data-attempt-receipt={attempt.receipt.outcome}>
          执行凭据：{attempt.receipt.outcome}
        </span>
      )}
      {retry?.state === 'failed' && retry.error && (
        <span className="wg2-run-retry-error" role="alert">重试失败：{retry.error}</span>
      )}
      {attempt.sessionRef && (
        <span className="wg2-run-session">
          {sessionSummary(attempt.sessionRef)}
        </span>
      )}
      {canRetry && (
        <button
          type="button"
          className="wg2-run-retry-button"
          disabled={hasPending}
          onClick={handleRetry}
          aria-label={`${needsConfirmation ? '确认结果并重试' : '重试'} ${taskName} 尝试 #${attempt.index + 1}`}
        >
          {hasPending ? '重试中…' : needsConfirmation ? '确认并重试' : '重试'}
        </button>
      )}
    </li>
  );
};

const TaskDetail: React.FC<{
  task: Task;
  runId: string;
  stageId: string;
  selection?: RunSelection;
  onSelect: (sel: RunSelection) => void;
  onRetry?: (intent: RetryIntent) => void | Promise<void>;
  retryByTarget: Record<string, RetryStatus>;
  readonly: boolean;
  archived: boolean;
  workId: string;
}> = ({ task, runId, stageId, selection, onSelect, onRetry, retryByTarget, readonly, archived, workId }) => {
  const currentTaskId = taskKey(task);
  const selected = isSelected(selection, runId, stageId, currentTaskId);
  const terminal = isTaskTerminal(task);
  const selectedStage = selection?.stageId === stageId && selection?.runId === runId;
  const latestAttempt = task.attempts.reduce<Attempt | undefined>((latest, attempt) =>
    !latest || attempt.index > latest.index ? attempt : latest, undefined);

  const handleClick = useCallback((event?: React.SyntheticEvent) => {
    event?.stopPropagation();
    // Selecting a task drills into its latest attempt if available.
    onSelect({
      runId,
      stageId,
      taskId: currentTaskId,
      attemptId: latestAttempt ? attemptKey(latestAttempt) : undefined,
      attemptIndex: latestAttempt?.index,
    });
  }, [onSelect, runId, stageId, currentTaskId, latestAttempt]);

  return (
    <li
      className={`wg2-run-task${selected ? ' wg2-run-selected' : ''}${terminal && task.state === 'failed' ? ' wg2-run-failed' : ''}`}
      data-run-task={currentTaskId}
      data-work-target-id={`task:${runId}:${stageId}:${currentTaskId}`}
      role="treeitem"
      aria-selected={selected}
      aria-expanded={selectedStage ? task.attempts.length > 0 : undefined}
      tabIndex={0}
      onClick={handleClick}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleClick(e); } }}
    >
      <span className="wg2-run-task-name">{task.name}</span>
      <RunStateBadge state={task.state} />
      {task.attempts.length > 0 && (
        <ul className="wg2-run-attempts" role="group" aria-label={`${task.name} 尝试记录`}>
          {task.attempts.map((attempt) => (
            <AttemptDetail
              key={attemptKey(attempt)}
              attempt={attempt}
              runId={runId}
              stageId={stageId}
              taskId={currentTaskId}
              taskName={task.name}
              selection={selection}
              onSelect={onSelect}
              onRetry={onRetry}
              retryByTarget={retryByTarget}
              readonly={readonly}
              archived={archived}
              workId={workId}
              latest={attempt === latestAttempt}
            />
          ))}
        </ul>
      )}
    </li>
  );
};

const StageDetail: React.FC<{
  stage: Stage;
  runId: string;
  selection?: RunSelection;
  onSelect: (sel: RunSelection) => void;
  onRetry?: (intent: RetryIntent) => void | Promise<void>;
  retryByTarget: Record<string, RetryStatus>;
  readonly: boolean;
  archived: boolean;
  workId: string;
}> = ({ stage, runId, selection, onSelect, onRetry, retryByTarget, readonly, archived, workId }) => {
  const currentStageId = stageKey(stage);
  const selected = isSelected(selection, runId, currentStageId);
  const terminal = isStageTerminal(stage);
  const selectedRun = selection?.runId === runId;

  const handleClick = useCallback((event?: React.SyntheticEvent) => {
    event?.stopPropagation();
    onSelect({ runId, stageId: currentStageId });
  }, [onSelect, runId, currentStageId]);

  return (
    <li
      className={`wg2-run-stage${selected ? ' wg2-run-selected' : ''}${terminal ? ' wg2-run-terminal' : ''}`}
      data-run-stage={currentStageId}
      data-work-target-id={`stage:${runId}:${currentStageId}`}
      role="treeitem"
      aria-selected={selected}
      aria-expanded={selectedRun ? stage.tasks.length > 0 : undefined}
      tabIndex={0}
      onClick={handleClick}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleClick(e); } }}
    >
      <span className="wg2-run-stage-name">{stage.name}</span>
      <RunStateBadge state={stage.state} />
      <span className="wg2-run-time">
        {timeLabel(stage.startedAt)}
        {stage.finishedAt && ` → ${timeLabel(stage.finishedAt)}`}
      </span>
      {stage.gate && (
        <span className="wg2-run-gate" data-stage-gate={stage.gate}>
          {stage.resolution ? `门控已解决：${stage.resolution.outcome}` : `等待${stage.gate === 'approval' ? '审批' : '输入'}`}
        </span>
      )}
      {stage.tasks.length > 0 && (
        <ul className="wg2-run-tasks" role="group" aria-label={`${stage.name} 任务列表`}>
          {stage.tasks.map((task) => (
            <TaskDetail
              key={taskKey(task)}
              task={task}
              runId={runId}
              stageId={currentStageId}
              selection={selection}
              onSelect={onSelect}
              onRetry={onRetry}
              retryByTarget={retryByTarget}
              readonly={readonly}
              archived={archived}
              workId={workId}
            />
          ))}
        </ul>
      )}
    </li>
  );
};

const RunDetail: React.FC<{
  run: WorkflowRun;
  selection?: RunSelection;
  onSelect: (sel: RunSelection) => void;
  onRetry?: (intent: RetryIntent) => void | Promise<void>;
  retryByTarget: Record<string, RetryStatus>;
  readonly: boolean;
  archived: boolean;
  workId: string;
}> = ({ run, selection, onSelect, onRetry, retryByTarget, readonly, archived, workId }) => {
  const selected = isSelected(selection, run.id);

  const handleClick = useCallback(() => {
    onSelect({ runId: run.id });
  }, [onSelect, run.id]);

  return (
    <li
      className={`wg2-run-item${selected ? ' wg2-run-selected' : ''}`}
      data-work-target-id={`run:${run.id}`}
      role="treeitem"
      aria-selected={selected}
      aria-expanded={run.stages.length > 0}
    >
      <div
        className="wg2-run-header"
        role="button"
        tabIndex={0}
        onClick={handleClick}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleClick(); } }}
      >
        <span className="wg2-run-id">{run.id}</span>
        <RunStateBadge state={run.state} />
        <span className="wg2-run-time">
          {timeLabel(run.startedAt)}
          {run.finishedAt && ` → ${timeLabel(run.finishedAt)}`}
        </span>
        {run.conclusion && (
          <span className="wg2-run-conclusion" title={run.conclusion.summary}>
            {run.conclusion.title}
          </span>
        )}
        {run.cancel && (
          <span className="wg2-run-cancel" data-cancel-status={run.cancel.status} role={run.cancel.status === 'failed' ? 'alert' : undefined}>
            取消指令：{run.cancel.status === 'delivered' ? '已送达' : run.cancel.status === 'failed' ? `送达失败${run.cancel.error ? `（${run.cancel.error}）` : ''}` : '待送达'}
          </span>
        )}
      </div>
      {run.stages.length > 0 && (
        <ul className="wg2-run-stages" role="group" aria-label={`Run ${run.id} 阶段`}>
          {run.stages.map((stage) => (
            <StageDetail
              key={stageKey(stage)}
              stage={stage}
              runId={run.id}
              selection={selection}
              onSelect={onSelect}
              onRetry={onRetry}
              retryByTarget={retryByTarget}
              readonly={readonly}
              archived={archived}
              workId={workId}
            />
          ))}
        </ul>
      )}
    </li>
  );
};

export const RunProgressIndicator: React.FC<RunProgressIndicatorProps> = ({
  work,
  selection,
  onSelect,
  onRetry,
  retryByTarget,
  readonly,
  archived,
}) => {
  if (work.runs.length === 0) {
    return (
      <div className="wg2-run-progress-empty" data-testid="run-progress-empty">
        <p>暂无运行记录。启动 Work 后将在此显示运行进度。</p>
      </div>
    );
  }

  return (
    <div
      className={`wg2-run-progress${readonly ? ' wg2-run-progress-readonly' : ''}${archived ? ' wg2-run-progress-archived' : ''}`}
      data-testid="run-progress"
      data-work-id={work.id}
      data-readonly={readonly ? 'true' : 'false'}
      data-archived={archived ? 'true' : 'false'}
    >
      <h4 className="wg2-run-progress-title">运行进度</h4>
      <ul className="wg2-run-list" role="tree" aria-label="运行列表">
        {work.runs.map((run) => (
          <RunDetail
            key={run.id}
            run={run}
            selection={selection}
            onSelect={onSelect}
            onRetry={onRetry}
            retryByTarget={retryByTarget}
            readonly={readonly}
            archived={archived}
            workId={work.id}
          />
        ))}
      </ul>
    </div>
  );
};
