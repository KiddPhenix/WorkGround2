import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Pause, Play, RotateCcw, Square } from 'lucide-react';

import type { ResumeRunInput, WorkState, WorkflowRun } from '../../work/types';

export interface WorkControlBarProps {
  workId: string;
  workState: WorkState;
  runs: WorkflowRun[];
  readonly: boolean;
  archived: boolean;
  /** Called for Start (RunWork). */
  onStart?: (input: { workId: string; requestId: string }) => WorkflowRun | Promise<WorkflowRun>;
  /** Called for Start while a paused run exists (ResumeRun). */
  onResume?: (input: ResumeRunInput) => WorkflowRun | Promise<WorkflowRun>;
  /** Called for Pause (PauseRun). */
  onPause?: (input: { workId: string; runId: string; requestId: string }) => Promise<void>;
  /** Called for Stop (CancelRun). */
  onStop?: (input: { workId: string; runId: string; requestId: string }) => Promise<void>;
  /** Called for Restart (RestartRun). */
  onRestart?: (input: { workId: string; runId: string; requestId: string }) => WorkflowRun | Promise<WorkflowRun>;
}

type BusyAction = 'start' | 'pause' | 'stop' | 'restart' | null;
export type ActionName = Exclude<BusyAction, null>;

export interface ActionIntent {
  action: ActionName;
  runId: string;
  requestId: string;
  ackedRunId?: string;
}

export function nextActionIntent(
  current: ActionIntent | null,
  action: ActionName,
  runId: string,
  makeRequestId: (prefix: string) => string = requestID,
): ActionIntent {
  if (current?.action === action && current.runId === runId) return current;
  return { action, runId, requestId: makeRequestId(action) };
}

function requestID(prefix: string): string {
  const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `work-ctrl-${prefix}-${suffix}`;
}

/** Find the latest non-terminal run, if any. */
export function activeWorkRun(runs: WorkflowRun[]): WorkflowRun | undefined {
  return [...runs].reverse().find((run) =>
    run.state === 'running' || run.state === 'waiting' || run.state === 'pending');
}

export interface WorkControlAvailability {
  canStart: boolean;
  canPause: boolean;
  canStop: boolean;
  canRestart: boolean;
  resumeOnStart: boolean;
  activeRun?: WorkflowRun;
  restartRun?: WorkflowRun;
}

export function deriveWorkControlAvailability(input: {
  workState: WorkState;
  runs: WorkflowRun[];
  readonly: boolean;
  archived: boolean;
  hasStart: boolean;
  hasResume: boolean;
  hasPause: boolean;
  hasStop: boolean;
  hasRestart: boolean;
}): WorkControlAvailability {
  const disabled = input.readonly || input.archived;
  const activeRun = activeWorkRun(input.runs);
  const restartRun = activeRun ?? input.runs[input.runs.length - 1];
  const paused = input.workState === 'paused' && activeRun?.state === 'waiting';
  const startable = input.workState === 'draft' || input.workState === 'ready';

  return {
    canStart: !disabled && (paused ? input.hasResume : input.hasStart && startable && !activeRun),
    canPause: !disabled && input.hasPause && input.workState === 'running' && activeRun?.state === 'running',
    canStop: !disabled && input.hasStop && Boolean(activeRun)
      && (activeRun?.state === 'running' || activeRun?.state === 'waiting' || activeRun?.state === 'pending'),
    canRestart: !disabled && input.hasRestart && Boolean(restartRun)
      && input.workState !== 'draft' && input.workState !== 'ready',
    resumeOnStart: paused,
    activeRun,
    restartRun,
  };
}

export const WorkControlBar: React.FC<WorkControlBarProps> = ({
  workId,
  workState,
  runs,
  readonly,
  archived,
  onStart,
  onResume,
  onPause,
  onStop,
  onRestart,
}) => {
  const [busyAction, setBusyAction] = useState<BusyAction>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const actionIntentRef = useRef<ActionIntent | null>(null);

  const availability = useMemo(() => deriveWorkControlAvailability({
    workState,
    runs,
    readonly,
    archived,
    hasStart: Boolean(onStart),
    hasResume: Boolean(onResume),
    hasPause: Boolean(onPause),
    hasStop: Boolean(onStop),
    hasRestart: Boolean(onRestart),
  }), [archived, onPause, onRestart, onResume, onStart, onStop, readonly, runs, workState]);
  const { activeRun: currentRun, restartRun, canStart, canPause, canStop, canRestart, resumeOnStart } = availability;

  // Authoritative projection changes settle the prior action. A failed action
  // leaves its intent in place, so a repeated click safely reuses requestId.
  useEffect(() => {
    actionIntentRef.current = null;
    setActionError(null);
    setBusyAction(null);
  }, [currentRun?.id, currentRun?.state, restartRun?.id, workId, workState]);

  // ── action handlers ──────────────────────────────────────────────────

  const actionIntent = useCallback((action: ActionName, runId = ''): ActionIntent => {
    const next = nextActionIntent(actionIntentRef.current, action, runId);
    actionIntentRef.current = next;
    return next;
  }, []);

  const doStart = useCallback(async () => {
    if (!canStart || busyAction) return;
    setBusyAction('start');
    setActionError(null);
    const runId = resumeOnStart ? currentRun?.id ?? '' : '';
    const intent = actionIntent('start', runId);
    try {
      const ack = resumeOnStart
        ? await onResume!({ workId, runId, requestId: intent.requestId })
        : await onStart!({ workId, requestId: intent.requestId });
      if (actionIntentRef.current?.requestId === intent.requestId) {
        actionIntentRef.current = { ...intent, ackedRunId: ack.id };
      }
    } catch (error) {
      if (actionIntentRef.current?.requestId === intent.requestId) {
        setActionError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      setBusyAction(null);
    }
  }, [actionIntent, busyAction, canStart, currentRun?.id, onResume, onStart, resumeOnStart, workId]);

  const doPause = useCallback(async () => {
    if (!onPause || !currentRun || !canPause || busyAction) return;
    setBusyAction('pause');
    setActionError(null);
    const intent = actionIntent('pause', currentRun.id);
    try {
      await onPause({ workId, runId: currentRun.id, requestId: intent.requestId });
    } catch (error) {
      setActionError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusyAction(null);
    }
  }, [actionIntent, busyAction, canPause, currentRun, onPause, workId]);

  const doStop = useCallback(async () => {
    if (!onStop || !currentRun || !canStop || busyAction) return;
    setBusyAction('stop');
    setActionError(null);
    const intent = actionIntent('stop', currentRun.id);
    try {
      await onStop({ workId, runId: currentRun.id, requestId: intent.requestId });
    } catch (error) {
      setActionError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusyAction(null);
    }
  }, [actionIntent, busyAction, canStop, currentRun, onStop, workId]);

  const doRestart = useCallback(async () => {
    if (!onRestart || !restartRun || !canRestart || busyAction) return;
    setBusyAction('restart');
    setActionError(null);
    const intent = actionIntent('restart', restartRun.id);
    try {
      const ack = await onRestart({ workId, runId: restartRun.id, requestId: intent.requestId });
      if (actionIntentRef.current?.requestId === intent.requestId) {
        actionIntentRef.current = { ...intent, ackedRunId: ack.id };
      }
    } catch (error) {
      if (actionIntentRef.current?.requestId === intent.requestId) {
        setActionError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      setBusyAction(null);
    }
  }, [actionIntent, busyAction, canRestart, onRestart, restartRun, workId]);

  const busy = busyAction !== null;

  return (
    <div
      className="wg2-work-control-bar"
      data-testid="work-control-bar"
      data-work-state={workState}
      data-readonly={readonly || archived ? 'true' : 'false'}
      role="toolbar"
      aria-label="工作控制"
      aria-busy={busy}
    >
      <div className="wg2-work-control-bar__actions">
        <button
          type="button"
          className="wg2-work-control-bar__btn wg2-work-control-bar__btn--start"
          data-testid="work-ctrl-start"
          title={resumeOnStart ? '开始 — 继续暂停的工作运行' : '开始 — 启动新的工作运行'}
          aria-label="开始"
          disabled={!canStart || busy}
          aria-busy={busyAction === 'start'}
          onClick={doStart}
        >
          <Play aria-hidden="true" size={16} strokeWidth={2} />
          {busyAction === 'start' && <span className="wg2-work-control-bar__btn-spinner" aria-hidden="true" />}
        </button>

        <button
          type="button"
          className="wg2-work-control-bar__btn wg2-work-control-bar__btn--pause"
          data-testid="work-ctrl-pause"
          title="暂停 — 暂停当前运行"
          aria-label="暂停"
          disabled={!canPause || busy}
          aria-busy={busyAction === 'pause'}
          onClick={doPause}
        >
          <Pause aria-hidden="true" size={16} strokeWidth={2} />
          {busyAction === 'pause' && <span className="wg2-work-control-bar__btn-spinner" aria-hidden="true" />}
        </button>

        <button
          type="button"
          className="wg2-work-control-bar__btn wg2-work-control-bar__btn--stop"
          data-testid="work-ctrl-stop"
          title="停止 — 取消当前运行"
          aria-label="停止"
          disabled={!canStop || busy}
          aria-busy={busyAction === 'stop'}
          onClick={doStop}
        >
          <Square aria-hidden="true" size={16} strokeWidth={2} />
          {busyAction === 'stop' && <span className="wg2-work-control-bar__btn-spinner" aria-hidden="true" />}
        </button>

        <button
          type="button"
          className="wg2-work-control-bar__btn wg2-work-control-bar__btn--restart"
          data-testid="work-ctrl-restart"
          title="重启 — 终止当前运行并重新开始"
          aria-label="重启"
          disabled={!canRestart || busy}
          aria-busy={busyAction === 'restart'}
          onClick={doRestart}
        >
          <RotateCcw aria-hidden="true" size={16} strokeWidth={2} />
          {busyAction === 'restart' && <span className="wg2-work-control-bar__btn-spinner" aria-hidden="true" />}
        </button>
      </div>

      {actionError && (
        <div className="wg2-work-control-bar__action-error" role="alert" data-testid="work-ctrl-action-error">
          {actionError}
        </div>
      )}
    </div>
  );
};

export default WorkControlBar;
