import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { useCornerstoneUIStore } from '../../work/cornerstoneStore';
import { useWorkStore } from '../../work/store';
import type { ResumeRunInput, RunBlockCode, WorkflowRun } from '../../work/types';
import type { WorkDefinitionRevision } from '../../work/types_v2';

const RUN_CODE_LABEL: Record<RunBlockCode, string> = {
  blob_missing: '快照内容缺失',
  budget_exhausted: 'Token 预算耗尽',
  resolver_unavailable: '解析器不可用',
  cornerstone_stale: '基石已变化',
  cornerstone_missing: '基石来源缺失',
  cornerstone_denied: '基石权限不足',
  cornerstone_invalid: '基石内容无效',
  waiting_user: '等待用户操作',
  failed: '运行失败',
  archived: '已归档',
};

export interface WorkRunEntryProps {
  workId: string;
  onRun?: (input: { workId: string; requestId: string }) => WorkflowRun | Promise<WorkflowRun>;
  onResumeRun?: (input: ResumeRunInput) => WorkflowRun | Promise<WorkflowRun>;
  onRecoverProjection?: () => void | Promise<void>;
  onOpenDrawer?: () => void;
  disabled?: boolean;
  /** Active V2 definition — when absent on a V2 work, Run is gated. */
  v2Definition?: WorkDefinitionRevision;
  /** Called when user clicks the planning CTA on a V2 draft work. */
  onPlanStructure?: () => void;
  /** Retries one authoritative recoverable V2 task. */
  onV2TaskRetry?: (intent: { workId: string; taskId: string; runId: string }) => void | Promise<void>;
  /** Regenerates one missing V2 artifact from its completed producer. */
  onV2ArtifactRetry?: (intent: {
    workId: string;
    definitionRevision: number;
    slotId: string;
    revision: number;
  }) => void | Promise<void>;
}

interface RunIntent {
  requestId: string;
  ackedRunId?: string;
}

interface ResumeIntent extends RunIntent {
  runId: string;
}

function requestID(prefix: 'run' | 'resume'): string {
  const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `work-${prefix}-${suffix}`;
}

export const WorkRunEntry: React.FC<WorkRunEntryProps> = ({
  workId,
  onRun,
  onResumeRun,
  onRecoverProjection,
  onOpenDrawer,
  disabled = false,
  v2Definition,
  onPlanStructure,
  onV2TaskRetry,
  onV2ArtifactRetry,
}) => {
  const view = useWorkStore((state) => state.works[workId]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const runIntentRef = useRef<RunIntent | null>(null);
  const resumeIntentRef = useRef<ResumeIntent | null>(null);
  const [runIntent, setRunIntentState] = useState<RunIntent | null>(null);
  const [resumeIntent, setResumeIntentState] = useState<ResumeIntent | null>(null);
  const assessment = view?.assessment;
  const runBlock = view?.runBlock;
  const blocked = Boolean(runBlock?.blocked || assessment?.blocking);
  const degraded = assessment?.degraded ?? false;
  const v2Tasks = view?.tasks ?? [];
  const recoverableV2Task = v2Tasks.find((task) =>
    task.state === 'failed_retryable' || task.state === 'invalidated');
  const v2Running = v2Tasks.some((task) =>
    task.state === 'pending' || task.state === 'ready' || task.state === 'running');
  const v2Waiting = v2Tasks.some((task) =>
    task.state === 'waiting_input' || task.state === 'waiting_approval');
  const v2Completed = v2Tasks.length > 0 && v2Tasks.every((task) => task.state === 'completed');
  const missingV2Artifact = v2Completed
    ? view?.artifactSlots?.find((slot) => slot.required && slot.state !== 'ready')
    : undefined;
  const running = v2Definition && v2Tasks.length > 0 ? v2Running : view?.work.state === 'running';
  const promptMissing = Boolean(view && !view.work.prompt.trim());
  // V2 draft: schemaVersion >= 2 but no active executable Definition.
  // Run/Resume are gated — user must finish planning first.
  const isV2Draft = Boolean(view && view.work.schemaVersion >= 2 && !v2Definition);
  const waitingRun = useMemo(() => {
    if (!view) return undefined;
    return [...view.work.runs].reverse().find((run) => run.state === 'waiting');
  }, [view]);
  const blockItems = runBlock?.items ?? [];
  const waitingUser = blockItems.some((item) => item.code === 'waiting_user');
  const canResume = runBlock?.blocked === true
    && waitingUser
    && Boolean(waitingRun)
    && assessment?.state === 'ready'
    && assessment.blocking === false
    && blockItems.length > 0
    && blockItems.every((item) => item.code === 'waiting_user');
  const blockCount = Math.max(blockItems.length, assessment?.issues?.length ?? 0, 1);

  const setRunIntent = useCallback((next: RunIntent | null) => {
    runIntentRef.current = next;
    setRunIntentState(next);
  }, []);

  const setResumeIntent = useCallback((next: ResumeIntent | null) => {
    resumeIntentRef.current = next;
    setResumeIntentState(next);
  }, []);

  useEffect(() => {
    setRunIntent(null);
    setResumeIntent(null);
    setError(null);
    setPending(false);
  }, [setResumeIntent, setRunIntent, workId]);

  // ACK only records identity. The intent stays live until an authoritative
  // WorkView confirms it, so a failed snapshot cannot turn a retry into a new Run.
  useEffect(() => {
    if (!view) return;
    const currentRun = runIntentRef.current;
    if (currentRun) {
      const confirmed = view.work.runs.some((run) => currentRun.ackedRunId
        ? run.id === currentRun.ackedRunId
        : run.requestId === currentRun.requestId);
      if (confirmed) {
        setRunIntent(null);
        setError(null);
      }
    }

    const currentResume = resumeIntentRef.current;
    if (currentResume) {
      const tracked = view.work.runs.find((run) => run.id === currentResume.runId);
      const confirmed = Boolean(tracked && tracked.state !== 'waiting');
      const waitingRunChanged = waitingRun?.id !== currentResume.runId;
      if (confirmed || waitingRunChanged) {
        setResumeIntent(null);
        setError(null);
      }
    }
  }, [setResumeIntent, setRunIntent, view, waitingRun?.id]);

  const openDrawer = useCallback(() => {
    useCornerstoneUIStore.getState().setOpen(workId, true);
    onOpenDrawer?.();
  }, [onOpenDrawer, workId]);

  const handleRun = useCallback(async () => {
    // V2 draft gate: Run is never reachable for V2 planning works.
    // The UI renders a planning CTA instead of the Run button.
    if (isV2Draft) return;
    if (promptMissing) {
      setError('请先在背面填写并保存 Prompt。');
      return;
    }
    if (blocked) {
      openDrawer();
      return;
    }
    if (!onRun || disabled || pending || running) return;
    setPending(true);
    setError(null);
    const existing = runIntentRef.current;
    if (existing?.ackedRunId) {
      try {
        if (onRecoverProjection) await onRecoverProjection();
        else setError('运行已接收，正在等待权威投影确认。');
      } catch {
        if (runIntentRef.current?.requestId === existing.requestId) {
          setError('运行已接收，但权威投影同步失败；可安全重试同步。');
        }
      } finally {
        setPending(false);
      }
      return;
    }

    const intent = existing ?? { requestId: requestID('run') };
    if (!existing) setRunIntent(intent);
    let ack: WorkflowRun;
    try {
      ack = await onRun({ workId, requestId: intent.requestId });
    } catch {
      if (runIntentRef.current?.requestId === intent.requestId) setError('运行请求失败，可使用同一请求标识安全重试。');
      setPending(false);
      return;
    }

    if (runIntentRef.current?.requestId !== intent.requestId) {
      setPending(false);
      return;
    }
    if (!ack.id || ack.workId !== workId || (ack.requestId && ack.requestId !== intent.requestId)) {
      setError('运行 ACK 身份不匹配，保留原请求标识等待安全重试。');
      setPending(false);
      return;
    }
    setRunIntent({ ...intent, ackedRunId: ack.id });
    try {
      if (onRecoverProjection) await onRecoverProjection();
    } catch {
      if (runIntentRef.current?.requestId === intent.requestId) {
        setError('运行已接收，但权威投影同步失败；可安全重试同步。');
      }
    } finally {
      setPending(false);
    }
  }, [blocked, disabled, isV2Draft, onRecoverProjection, onRun, openDrawer, pending, promptMissing, running, setRunIntent, workId]);

  const handleResume = useCallback(async () => {
    if (!onResumeRun || !waitingRun || disabled || pending) return;
    setPending(true);
    setError(null);
    const existing = resumeIntentRef.current?.runId === waitingRun.id ? resumeIntentRef.current : null;
    if (existing?.ackedRunId) {
      try {
        if (onRecoverProjection) await onRecoverProjection();
        else setError('继续运行已接收，正在等待权威投影确认。');
      } catch {
        if (resumeIntentRef.current?.requestId === existing.requestId && resumeIntentRef.current.runId === existing.runId) {
          setError('继续运行已接收，但权威投影同步失败；可安全重试同步。');
        }
      } finally {
        setPending(false);
      }
      return;
    }

    const intent = existing ?? { runId: waitingRun.id, requestId: requestID('resume') };
    if (!existing) setResumeIntent(intent);
    let ack: WorkflowRun;
    try {
      ack = await onResumeRun({ workId, runId: intent.runId, requestId: intent.requestId });
    } catch {
      if (resumeIntentRef.current?.requestId === intent.requestId && resumeIntentRef.current.runId === intent.runId) {
        setError('继续运行请求失败，可使用同一请求标识安全重试。');
      }
      setPending(false);
      return;
    }

    if (resumeIntentRef.current?.requestId !== intent.requestId || resumeIntentRef.current.runId !== intent.runId) {
      setPending(false);
      return;
    }
    if (!ack.id || ack.id !== intent.runId || ack.workId !== workId) {
      setError('继续运行 ACK 身份不匹配，保留原请求标识等待安全重试。');
      setPending(false);
      return;
    }
    setResumeIntent({ ...intent, ackedRunId: ack.id });
    try {
      if (onRecoverProjection) await onRecoverProjection();
    } catch {
      if (resumeIntentRef.current?.requestId === intent.requestId && resumeIntentRef.current.runId === intent.runId) {
        setError('继续运行已接收，但权威投影同步失败；可安全重试同步。');
      }
    } finally {
      setPending(false);
    }
  }, [disabled, onRecoverProjection, onResumeRun, pending, setResumeIntent, waitingRun, workId]);

  const handleV2Retry = useCallback(async () => {
    if (!recoverableV2Task || !onV2TaskRetry || disabled || pending) return;
    setPending(true);
    setError(null);
    try {
      await onV2TaskRetry({
        workId,
        taskId: recoverableV2Task.id,
        runId: recoverableV2Task.runId,
      });
      if (onRecoverProjection) await onRecoverProjection();
    } catch {
      setError('阶段重试失败，状态已保留，可安全重试。');
    } finally {
      setPending(false);
    }
  }, [disabled, onRecoverProjection, onV2TaskRetry, pending, recoverableV2Task, workId]);

  const handleV2ArtifactRetry = useCallback(async () => {
    if (!missingV2Artifact || !onV2ArtifactRetry || disabled || pending) return;
    setPending(true);
    setError(null);
    try {
      await onV2ArtifactRetry({
        workId,
        definitionRevision: missingV2Artifact.definitionRev,
        slotId: missingV2Artifact.id,
        revision: missingV2Artifact.revision,
      });
      if (onRecoverProjection) await onRecoverProjection();
    } catch {
      setError('缺失成果重新生成失败，状态已保留，可安全重试。');
    } finally {
      setPending(false);
    }
  }, [disabled, missingV2Artifact, onRecoverProjection, onV2ArtifactRetry, pending, workId]);

  return (
    <div className="work-run-entry" data-testid="work-run-entry">
      {isV2Draft ? (
        <button
          type="button"
          className="work-run-entry__btn work-run-entry__btn--plan"
          onClick={() => onPlanStructure?.()}
          disabled={disabled}
          data-testid="work-plan-structure"
        >
          继续规划工作结构
        </button>
      ) : v2Definition && recoverableV2Task ? (
        <button
          type="button"
          className="work-run-entry__btn work-run-entry__btn--resume"
          onClick={() => void handleV2Retry()}
          disabled={disabled || pending || !onV2TaskRetry}
          data-testid="work-v2-retry"
        >
          {pending ? '正在重试…' : '重试未完成阶段'}
        </button>
      ) : v2Definition && missingV2Artifact ? (
        <button
          type="button"
          className="work-run-entry__btn work-run-entry__btn--resume"
          onClick={() => void handleV2ArtifactRetry()}
          disabled={disabled || pending || !onV2ArtifactRetry}
          data-testid="work-v2-artifact-retry"
        >
          {pending ? '正在生成…' : '生成缺失成果'}
        </button>
      ) : v2Definition && v2Tasks.length > 0 ? (
        <button
          type="button"
          className="work-run-entry__btn"
          disabled
          data-testid="work-v2-status"
        >
          {v2Waiting ? '等待输入…' : v2Completed ? '已完成' : running ? '运行中…' : '准备中…'}
        </button>
      ) : canResume && waitingRun ? (
        <button
          type="button"
          className="work-run-entry__btn work-run-entry__btn--resume"
          onClick={() => void handleResume()}
          disabled={disabled || pending || !onResumeRun}
          aria-describedby={`work-run-blocked-${workId}`}
        >
          {pending ? (resumeIntent?.ackedRunId ? '正在同步…' : '正在继续…') : (resumeIntent?.ackedRunId ? '重试同步' : '继续运行')}
        </button>
      ) : (
        <button
          type="button"
          className="work-run-entry__btn"
          onClick={() => void handleRun()}
          disabled={disabled || running || pending || !view || blocked || promptMissing}
          aria-describedby={blocked ? `work-run-blocked-${workId}` : undefined}
        >
          {running ? '运行中…' : pending ? (runIntent?.ackedRunId ? '正在同步…' : '正在启动…') : (runIntent?.ackedRunId ? '重试同步' : '运行')}
        </button>
      )}

      {isV2Draft && (
        <p className="work-run-entry__hint" data-testid="work-v2-draft-hint">
          请先在背面完成工作结构规划，确认后可开始执行。
        </p>
      )}

      {!isV2Draft && promptMissing && <p className="work-run-entry__error" role="alert">请先在背面填写并保存 Prompt。</p>}

      {blocked && (
        <div id={`work-run-blocked-${workId}`} className="work-run-entry__attention work-run-entry__blocked" role="alert" data-testid="work-run-blocked">
          <span className="work-run-entry__blocked-label">
            {canResume ? '用户操作已完成，可以继续原运行。' : `${blockCount} 个阻断原因阻止运行。`}
          </span>
          {runBlock?.items?.map((item) => (
            <span key={item.code + (item.cornerstoneId ?? '')} className="work-run-entry__blocked-reason" data-testid={`run-block-${item.code}`}>
              {RUN_CODE_LABEL[item.code] ?? item.code}
            </span>
          ))}
          <button type="button" onClick={openDrawer}>查看基石</button>
        </div>
      )}

      {degraded && !blocked && (
        <div className="work-run-entry__attention work-run-entry__degraded" role="status" data-testid="work-run-degraded">
          <span>部分基石降级可用，仍可运行。</span>
          {runBlock?.items?.map((item) => (
            <span key={item.code + (item.cornerstoneId ?? '')} className="work-run-entry__blocked-reason">
              {RUN_CODE_LABEL[item.code] ?? item.code}
            </span>
          ))}
          <button type="button" onClick={openDrawer}>查看基石</button>
        </div>
      )}

      {error && <p className="work-run-entry__error" role="alert">{error}</p>}
    </div>
  );
};

export default WorkRunEntry;
