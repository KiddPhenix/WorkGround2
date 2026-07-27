import React, { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';

import type { RunSelection, SessionRef, SessionSurfaceContext, WorkView } from '../../work/types';
import { attemptKey, resolveSelection, stageKey, taskKey } from '../../work/store';
import type {
  ApplyDefinitionInput,
  ApplyDefinitionResult,
  CreateCandidateRevisionInput,
  CreateCandidateRevisionResult,
  RunImpact,
  WorkDefinitionRevision,
} from '../../work/types_v2';
import { DefinitionDiff } from '../../work/components/v2';

export interface WorkCardBackSlotProps {
  workID: string;
  prompt: string;
  readonly: boolean;
  archived: boolean;
  draft: string;
  onDraftChange: (draft: string) => void;
  /** Called when the planning session produces a definition ready to apply. */
  onApplyDefinition?: (input: ApplyDefinitionInput) => Promise<ApplyDefinitionResult>;
  onCreateCandidate?: (input: CreateCandidateRevisionInput) => Promise<CreateCandidateRevisionResult>;
}

export type WorkCardBackSlot = ReactNode | ((props: WorkCardBackSlotProps) => ReactNode);

/**
 * Existing session surfaces are passed through this adapter. Callers should
 * provide the current Transcript, Run/Approval/Ask, ArtifactShelf, Queue and
 * Composer nodes instead of creating a second conversation implementation.
 */
export interface WorkCardBackSlots {
  /** Full production Session surface for attempts without a SessionRef. */
  surface?: WorkCardBackSlot;
  transcript?: WorkCardBackSlot;
  runApproval?: WorkCardBackSlot;
  artifactShelf?: WorkCardBackSlot;
  queue?: WorkCardBackSlot;
  composer?: WorkCardBackSlot;
}

export interface WorkCardBackProps {
  view: WorkView;
  draft: string;
  onDraftChange: (draft: string) => void;
  readonly: boolean;
  archived: boolean;
  slots?: WorkCardBackSlots;
  selection?: RunSelection;
  resolveSessionSurface?: (sessionRef: SessionRef, context: SessionSurfaceContext) => ReactNode;
  onSavePrompt?: (prompt: string) => Promise<number>;
  onApplyDefinition?: (input: ApplyDefinitionInput) => Promise<ApplyDefinitionResult>;
  v2Definition?: WorkDefinitionRevision;
  /** Active definition for diff comparison (when v2Definition is a draft). */
  v2ActiveDefinition?: WorkDefinitionRevision;
  /** RunImpact from the last apply attempt, if available. */
  applyImpact?: RunImpact;
  onCreateCandidate?: (input: CreateCandidateRevisionInput) => Promise<CreateCandidateRevisionResult>;
}

function renderSlot(slot: WorkCardBackSlot | undefined, props: WorkCardBackSlotProps): ReactNode {
  return typeof slot === 'function' ? slot(props) : slot;
}

export const WorkCardBack: React.FC<WorkCardBackProps> = ({
  view,
  draft,
  onDraftChange,
  readonly,
  archived,
  slots,
  selection,
  resolveSessionSurface,
  onSavePrompt,
  onApplyDefinition,
  v2Definition,
  v2ActiveDefinition,
  applyImpact,
  onCreateCandidate,
}) => {
  const { work } = view;
  const [prompt, setPrompt] = useState(draft || work.prompt);
  const [saveState, setSaveState] = useState<'idle' | 'saving' | 'saved'>('idle');
  const [saveError, setSaveError] = useState<string | null>(null);
  const [generateState, setGenerateState] = useState<'idle' | 'saving' | 'generating'>('idle');
  const [generateError, setGenerateError] = useState<string | null>(null);
  const saveCompletedRef = useRef(false);
  const savedRevisionRef = useRef<number>(0);
  const [applyState, setApplyState] = useState<'idle' | 'applying'>('idle');
  const [applyError, setApplyError] = useState<string | null>(null);
  const applyIntentRef = useRef<{ signature: string; requestId: string } | null>(null);
  const candidateIntentRef = useRef<{ signature: string; requestId: string } | null>(null);
  const [localCandidate, setLocalCandidate] = useState<WorkDefinitionRevision | undefined>();
  const [candidateImpact, setCandidateImpact] = useState<RunImpact | undefined>();
  const [candidateState, setCandidateState] = useState<'idle' | 'creating'>('idle');
  const [candidateError, setCandidateError] = useState<string | null>(null);
  const [dismissedCandidateRevision, setDismissedCandidateRevision] = useState<number | null>(null);

  useEffect(() => {
    setPrompt(draft || work.prompt);
    setSaveState('idle');
    setSaveError(null);
  }, [draft, view.revision, work.prompt]);

  useEffect(() => {
    setLocalCandidate(undefined);
    setCandidateImpact(undefined);
    setCandidateError(null);
    setDismissedCandidateRevision(null);
    candidateIntentRef.current = null;
  }, [work.id, v2ActiveDefinition?.revision, v2Definition?.revision]);

  // The base for candidate generation: active definition takes precedence;
  // when only a draft exists (initial planning), use the draft as base so
  // the user can generate the first proper structure from the conversation.
  const candidateBase = v2ActiveDefinition ?? (v2Definition?.status === 'draft' ? v2Definition : undefined);

  const saveAndGenerate = async () => {
    if (!onSavePrompt || !onCreateCandidate || !candidateBase || readonly || archived || generateState !== 'idle' || !prompt.trim()) return;
    const intent = prompt.trim();

    // Phase 1: save the prompt (skip if already completed on a prior attempt).
    if (!saveCompletedRef.current) {
      setGenerateState('saving');
      setGenerateError(null);
      try {
        const newRevision = await onSavePrompt(intent);
        saveCompletedRef.current = true;
        savedRevisionRef.current = newRevision;
      } catch (error) {
        setGenerateState('idle');
        setGenerateError(error instanceof Error ? error.message : String(error));
        return;
      }
    }

    // Phase 2: generate candidate from the saved authoritative revision.
    const signature = JSON.stringify([
      work.id,
      candidateBase.revision,
      savedRevisionRef.current,
      intent,
    ]);
    if (candidateIntentRef.current?.signature !== signature) {
      const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      candidateIntentRef.current = { signature, requestId: `work-candidate-${suffix}` };
    }
    setGenerateState('generating');
    setGenerateError(null);
    try {
      const result = await onCreateCandidate({
        workId: work.id,
        intent,
        baseDefinitionRevision: candidateBase.revision,
        expectedRevision: savedRevisionRef.current,
        requestId: candidateIntentRef.current.requestId,
      });
      if (!result.candidate) throw new Error('候选结构已提交，但响应缺少候选 Definition。');
      setLocalCandidate(result.candidate);
      setCandidateImpact(result.impact);
      setDismissedCandidateRevision(null);
      candidateIntentRef.current = null;
      saveCompletedRef.current = false;
    } catch (error) {
      const code = (error as { code?: string }).code;
      if (code === 'revision_conflict' || code === 'request_conflict') {
        candidateIntentRef.current = null;
        saveCompletedRef.current = false;
        setGenerateError('工作版本已变化，请重新生成候选结构。');
      } else {
        // Transport / planner failure: save already succeeded; retry must skip save.
        setGenerateError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      setGenerateState('idle');
    }
  };

  // Fallback candidate-only path: used when onSavePrompt is unavailable so the
  // combined save+generate flow cannot run.  This preserves V1 / non-candidate
  // backend contracts.
  const createCandidate = async () => {
    if (
      !onCreateCandidate
      || !candidateBase
      || readonly
      || archived
      || candidateState === 'creating'
      || !prompt.trim()
    ) return;
    const intent = prompt.trim();
    const signature = JSON.stringify([
      work.id,
      candidateBase.revision,
      view.revision,
      intent,
    ]);
    if (candidateIntentRef.current?.signature !== signature) {
      const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      candidateIntentRef.current = { signature, requestId: `work-candidate-${suffix}` };
    }
    setCandidateState('creating');
    setCandidateError(null);
    try {
      const result = await onCreateCandidate({
        workId: work.id,
        intent,
        baseDefinitionRevision: candidateBase.revision,
        expectedRevision: view.revision,
        requestId: candidateIntentRef.current.requestId,
      });
      if (!result.candidate) throw new Error('候选结构已提交，但响应缺少候选 Definition。');
      setLocalCandidate(result.candidate);
      setCandidateImpact(result.impact);
      setDismissedCandidateRevision(null);
      candidateIntentRef.current = null;
    } catch (error) {
      const code = (error as { code?: string }).code;
      if (code === 'revision_conflict' || code === 'request_conflict') {
        candidateIntentRef.current = null;
        setCandidateError('工作版本已变化，请重新生成候选结构。');
      } else {
        setCandidateError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      setCandidateState('idle');
    }
  };

  // A draft is a "projected candidate" only when it has BOTH a goal AND at
  // least one node — i.e. the planner has produced a complete structure. A
  // partial draft (goal-only or nodes-only) is still in planning and must go
  // through candidate generation.
  const isSubstantiveDraft = v2Definition?.status === 'draft'
    && v2Definition.goal.trim().length > 0
    && v2Definition.nodes.length > 0;
  const projectedCandidate = isSubstantiveDraft ? v2Definition : undefined;
  const candidateDefinition = localCandidate ?? projectedCandidate;
  const visibleCandidate = candidateDefinition?.revision === dismissedCandidateRevision
    ? undefined
    : candidateDefinition;
  const displayDefinition = visibleCandidate ?? v2ActiveDefinition ?? v2Definition;
  // Hide the empty/partial placeholder that has 0 useful nodes — it must not
  // dominate the first screen before the user has generated a real structure.
  const suppressEmptyPlaceholder = !visibleCandidate && !v2ActiveDefinition
    && v2Definition?.status === 'draft' && v2Definition.nodes.length === 0;
  const hasCombinedFlow = !!(onSavePrompt && onCreateCandidate && candidateBase && !readonly && !archived);
  const suppressDefaultSessionSurface = v2Definition !== undefined;

  const savePrompt = async () => {
    if (!onSavePrompt || readonly || archived || saveState === 'saving' || !prompt.trim()) return;
    setSaveState('saving');
    setSaveError(null);
    try {
      await onSavePrompt(prompt.trim());
      setSaveState('saved');
    } catch (error) {
      setSaveState('idle');
      setSaveError(error instanceof Error ? error.message : String(error));
    }
  };
  const applyDefinition = async () => {
    const candidate = localCandidate ?? (v2Definition?.status === 'draft' ? v2Definition : undefined);
    if (!onApplyDefinition || !candidate || candidate.status !== 'draft' || applyState === 'applying') return;
    const signature = `${work.id}:${candidate.revision}:${view.revision}`;
    if (applyIntentRef.current?.signature !== signature) {
      const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      applyIntentRef.current = { signature, requestId: `work-definition-${suffix}` };
    }
    setApplyState('applying');
    setApplyError(null);
    try {
      const result = await onApplyDefinition({
        workId: work.id,
        revision: candidate.revision,
        expectedRevision: view.revision,
        requestId: applyIntentRef.current.requestId,
      });
      if (!result.committed) {
        if (result.transportError?.code === 'revision_conflict' || result.transportError?.code === 'request_conflict') {
          applyIntentRef.current = null;
        }
        throw new Error(result.transportError?.message || '工作结构未应用，请重试。');
      }
      applyIntentRef.current = null;
    } catch (error) {
      const code = (error as { code?: string }).code;
      setApplyError(
        code === 'revision_conflict' || code === 'request_conflict'
          ? '工作版本已变化，请取消并重新生成候选结构。'
          : error instanceof Error ? error.message : String(error),
      );
    } finally {
      setApplyState('idle');
    }
  };
  const slotProps = useMemo<WorkCardBackSlotProps>(() => ({
    workID: work.id,
    prompt: work.prompt,
    readonly,
    archived,
    draft,
    onDraftChange,
    onApplyDefinition,
    onCreateCandidate,
  }), [archived, draft, onApplyDefinition, onCreateCandidate, onDraftChange, readonly, work.id, work.prompt]);

  // Resolve the selected attempt's session surface.
  const selectedSession = useMemo(() => {
    if (!selection || !resolveSessionSurface) return null;
    const resolved = resolveSelection(work, selection);
    if (!resolved?.stage || !resolved.task || !resolved.attempt?.sessionRef) return null;
    const sessionRef = resolved.attempt.sessionRef;
    const context: SessionSurfaceContext = {
      workId: work.id,
      runId: resolved.run.id,
      stageId: stageKey(resolved.stage),
      taskId: taskKey(resolved.task),
      attemptId: resolved.attempt.id,
      attemptIndex: resolved.attempt.index,
      sessionRef,
      readonly,
      archived,
    };
    return {
      key: `${sessionRef.sessionPath}\u0000${sessionRef.branchId}`,
      targetID: `attempt:${context.runId}:${context.stageId}:${context.taskId}:${attemptKey(resolved.attempt)}`,
      node: resolveSessionSurface(sessionRef, context),
    };
  }, [archived, readonly, resolveSessionSurface, selection, work]);

  return (
    <div
      className="wg2-work-card-back"
      data-testid="work-card-back"
      data-work-id={work.id}
      data-readonly={readonly ? 'true' : 'false'}
      data-archived={archived ? 'true' : 'false'}
    >
      <div className="wg2-work-back-header">
        <h2 className="wg2-work-name">{work.name}</h2>
      </div>

      {!readonly && !archived && (
        <div className="wg2-work-planning-editor">
          <section className="wg2-work-draft-editor" data-testid="work-draft-editor">
            <div className="wg2-work-draft-heading">
              <h3>任务说明</h3>
              <p>用自然语言说明目标、背景和期望结果。</p>
            </div>
            <label className="wg2-work-prompt-field">
              <span className="sr-only">任务说明</span>
              <textarea
                data-testid="work-prompt-editor"
                value={prompt}
                rows={6}
                placeholder="描述你希望 Work 完成的事情…"
                disabled={generateState !== 'idle'}
                onChange={(event) => { setPrompt(event.target.value); onDraftChange(event.target.value); setSaveState('idle'); saveCompletedRef.current = false; }}
              />
            </label>
            <div className="wg2-work-draft-actions">
              {hasCombinedFlow ? (
                <>
                  <button
                    type="button"
                    data-testid="work-generate-structure"
                    disabled={generateState !== 'idle' || !prompt.trim()}
                    onClick={() => void saveAndGenerate()}
                  >
                    {generateState === 'saving' ? '正在保存…'
                      : generateState === 'generating' ? '正在生成工作结构…'
                        : v2ActiveDefinition ? '更新工作结构' : '生成工作结构'}
                  </button>
                  {generateError && <span role="alert" data-testid="work-generate-structure-error">{generateError}</span>}
                </>
              ) : (
                <>
                  <button type="button" data-testid="work-save-draft" onClick={() => void savePrompt()} disabled={!prompt.trim() || saveState === 'saving'}>
                    {saveState === 'saving' ? '保存中…' : '保存任务说明'}
                  </button>
                  {saveState === 'saved' && <span role="status">已保存</span>}
                  {saveError && <span role="alert">{saveError}</span>}
                </>
              )}
            </div>
          </section>
        </div>
      )}

      {displayDefinition && !suppressEmptyPlaceholder && (
        <section
          className="wg2-work-planning-definition"
          data-testid="work-planning-definition"
          aria-label="工作结构"
        >
          <div className="wg2-work-planning-definition__heading">
            <div>
              <h3>{displayDefinition.status === 'active' ? '当前工作结构' : '待应用的工作结构'}</h3>
              <p>
                 修订 {displayDefinition.revision}
                 {displayDefinition.parentRevision > 0 ? `，基于修订 ${displayDefinition.parentRevision}` : ''}
              </p>
            </div>
            <span data-status={displayDefinition.status}>
              {displayDefinition.status === 'active' ? '已应用' : displayDefinition.status === 'superseded' ? '已替代' : '等待确认'}
            </span>
          </div>
          {displayDefinition.goal ? <p className="wg2-work-planning-definition__goal">{displayDefinition.goal}</p> : (
            <p role="status">继续在对话中补齐目标和工作结构。</p>
          )}
          <ul aria-label="结构摘要">
            <li>{displayDefinition.nodes.length} 个任务节点</li>
            <li>{displayDefinition.artifactSlots.length} 个成果槽位</li>
            <li>{displayDefinition.inputSpecs.length} 个输入项</li>
          </ul>
          {visibleCandidate?.status === 'draft' && !readonly && !archived && onApplyDefinition && v2ActiveDefinition && (
            <DefinitionDiff
              active={v2ActiveDefinition}
              candidate={visibleCandidate}
              impact={candidateImpact ?? applyImpact}
              onApply={() => void applyDefinition()}
              onCancel={() => {
                applyIntentRef.current = null;
                setApplyError(null);
                setDismissedCandidateRevision(visibleCandidate.revision);
                setLocalCandidate(undefined);
                setCandidateImpact(undefined);
              }}
              isApplying={applyState === 'applying'}
              error={applyError}
            />
          )}
          {/* Fallback candidate button: only when save is unavailable (non-combined path). */}
          {!hasCombinedFlow && candidateBase && !visibleCandidate && !readonly && !archived && onCreateCandidate && (
            <div className="wg2-work-planning-definition__actions">
              <button
                type="button"
                data-testid="work-create-candidate"
                disabled={candidateState === 'creating' || !prompt.trim()}
                onClick={() => void createCandidate()}
              >
                {candidateState === 'creating' ? '正在生成候选…' : '根据任务说明生成候选结构'}
              </button>
              {candidateError && <span role="alert" data-testid="work-create-candidate-error">{candidateError}</span>}
            </div>
          )}
          {candidateBase && !visibleCandidate && !readonly && !archived && !onCreateCandidate && (
            <div role="status" data-testid="work-create-candidate-unavailable">
              当前环境未连接工作结构规划能力，请稍后重试。
            </div>
          )}
          {displayDefinition.status === 'draft' && !readonly && !archived && onApplyDefinition && !v2ActiveDefinition && (
            <div className="wg2-work-planning-definition__actions">
              <button
                type="button"
                data-testid="work-apply-definition"
                disabled={applyState === 'applying' || !displayDefinition.goal.trim() || displayDefinition.nodes.length === 0}
                onClick={() => void applyDefinition()}
              >
                {applyState === 'applying' ? '正在应用…' : '确认并开始执行'}
              </button>
              {applyError && <span role="alert" data-testid="work-apply-definition-error">{applyError}</span>}
            </div>
          )}
        </section>
      )}

      {selectedSession !== null ? (
        <div
          key={selectedSession.key}
          className="wg2-work-back-selected-session"
          data-testid="work-back-selected-session"
          data-work-target-id={selectedSession.targetID}
        >
          {selectedSession.node ?? (
            <div className="wg2-work-session-unavailable" role="alert">目标 Session 暂不可用。</div>
          )}
        </div>
      ) : slots?.surface ? (
        suppressDefaultSessionSurface ? null : (
          <div className="wg2-work-back-slots" data-testid="work-session-surfaces">
            {renderSlot(slots.surface, slotProps)}
          </div>
        )
      ) : slots?.transcript ? (
        <div className="wg2-work-back-slots" data-testid="work-session-surfaces">
          <div className="wg2-work-back-slot wg2-work-back-transcript" data-testid="work-back-slot-transcript">
            {renderSlot(slots.transcript, slotProps)}
          </div>
          {slots.runApproval && (
            <div className="wg2-work-back-slot wg2-work-back-run-approval" data-testid="work-back-slot-run-approval">
              {renderSlot(slots.runApproval, slotProps)}
            </div>
          )}
          {slots.artifactShelf && (
            <div className="wg2-work-back-slot wg2-work-back-artifact-shelf" data-testid="work-back-slot-artifact-shelf">
              {renderSlot(slots.artifactShelf, slotProps)}
            </div>
          )}
          {slots.queue && (
            <div className="wg2-work-back-slot wg2-work-back-queue" data-testid="work-back-slot-queue">
              {renderSlot(slots.queue, slotProps)}
            </div>
          )}
          {!readonly && !archived && slots.composer && (
            <div className="wg2-work-back-slot wg2-work-back-composer" data-testid="work-back-slot-composer">
              {renderSlot(slots.composer, slotProps)}
            </div>
          )}
        </div>
      ) : suppressDefaultSessionSurface ? null : (
        <div className="wg2-work-session-unavailable" role="status" data-testid="work-session-unavailable">
          关联会话界面尚未载入，可继续查看 Work 概览。
        </div>
      )}

      {(readonly || archived) && (
        <div className="wg2-work-back-readonly-notice" data-testid="work-back-readonly-notice">
          {archived ? '此 Work 已归档，不可编辑或运行。' : '此 Work 为只读模式。'}
        </div>
      )}
    </div>
  );
};
