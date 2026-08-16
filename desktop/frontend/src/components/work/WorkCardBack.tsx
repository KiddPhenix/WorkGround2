import React, { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { LoaderCircle } from 'lucide-react';

import type { RunSelection, SessionRef, SessionSurfaceContext, WorkView } from '../../work/types';
import { attemptKey, resolveSelection, stageKey, taskKey } from '../../work/store';
import type {
  ApplyDefinitionInput,
  ApplyDefinitionResult,
  CreateCandidateRevisionInput,
  CreateCandidateRevisionResult,
  DefinitionPlanProgress,
  DefinitionStructuralAnswer,
  DefinitionStructuralClarification,
  RunImpact,
  WorkDefinitionRevision,
} from '../../work/types_v2';
import { DefinitionDiff } from '../../work/components/v2';
import { StructureClarificationCard } from './StructureClarificationCard';

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
  startIntent?: {
    id: string;
    prompt: string;
  };
  onStartIntentConsumed?: (id: string) => void;
  onStartIntentNeedsAttention?: (id: string) => void;
  readonly: boolean;
  archived: boolean;
  slots?: WorkCardBackSlots;
  selection?: RunSelection;
  sessionTarget?: SessionSurfaceContext;
  resolveSessionSurface?: (sessionRef: SessionRef, context: SessionSurfaceContext) => ReactNode;
  onSavePrompt?: (prompt: string, name?: string) => Promise<number>;
  onApplyDefinition?: (input: ApplyDefinitionInput) => Promise<ApplyDefinitionResult>;
  v2Definition?: WorkDefinitionRevision;
  /** Active definition for diff comparison (when v2Definition is a draft). */
  v2ActiveDefinition?: WorkDefinitionRevision;
  /** RunImpact from the last apply attempt, if available. */
  applyImpact?: RunImpact;
  onCreateCandidate?: (input: CreateCandidateRevisionInput) => Promise<CreateCandidateRevisionResult>;
  planningProgress?: DefinitionPlanProgress[];
}

type GenerateState = 'idle' | 'saving' | 'generating' | 'applying';
type GenerateBusyState = Exclude<GenerateState, 'idle'>;

const generateBusyCopy: Record<GenerateBusyState, readonly string[]> = {
  saving: [
    '正在给脑洞上户口…',
    '先把灵感钉住…',
    '需求已捕获，别跑…',
    '正在保存这次顿悟…',
  ],
  generating: [
    '正在把大象塞进流程图…',
    '任务们正在认领工位…',
    '依赖关系正在谈判…',
    '给脑洞装上轮子…',
    '章鱼项目经理已上线…',
  ],
  applying: [
    '工作流点火，坐稳…',
    '正在把计划推向现实…',
    '任务小队正在出发…',
    '咖啡已注入，开工…',
    '宇宙正在批准开工…',
  ],
};

const generateBusyStatus: Record<GenerateBusyState, string> = {
  saving: '正在保存名称和任务说明',
  generating: '正在生成工作结构',
  applying: '正在启动工作',
};

function pickGenerateBusyCopy(state: GenerateBusyState, previous = ''): string {
  const pool = generateBusyCopy[state];
  const candidates = pool.filter((copy) => copy !== previous);
  return candidates[Math.floor(Math.random() * candidates.length)] ?? pool[0];
}

function renderSlot(slot: WorkCardBackSlot | undefined, props: WorkCardBackSlotProps): ReactNode {
  return typeof slot === 'function' ? slot(props) : slot;
}

interface PendingStructureClarification {
  schemaVersion: 2;
  workRevision: number;
  prompt: string;
  clarification: DefinitionStructuralClarification;
  answers: DefinitionStructuralAnswer[];
}

function clarificationStorageKey(workID: string): string {
  return `wg2-definition-clarification:${workID}`;
}

function readPendingClarification(workID: string): PendingStructureClarification | null {
  try {
    const raw = globalThis.localStorage?.getItem(clarificationStorageKey(workID));
    if (!raw) return null;
    const value = JSON.parse(raw) as PendingStructureClarification;
    if (
      value.schemaVersion !== 2
      || !Number.isSafeInteger(value.workRevision)
      || typeof value.prompt !== 'string'
      || typeof value.clarification?.id !== 'string'
      || !Array.isArray(value.clarification.options)
      || !Array.isArray(value.answers)
    ) return null;
    return value;
  } catch {
    return null;
  }
}

function writePendingClarification(workID: string, value: PendingStructureClarification | null): void {
  try {
    const key = clarificationStorageKey(workID);
    if (value) globalThis.localStorage?.setItem(key, JSON.stringify(value));
    else globalThis.localStorage?.removeItem(key);
  } catch {
    // Local persistence is recovery assistance; the live typed result remains authoritative.
  }
}

export const WorkCardBack: React.FC<WorkCardBackProps> = ({
  view,
  draft,
  onDraftChange,
  startIntent,
  onStartIntentConsumed,
  onStartIntentNeedsAttention,
  readonly,
  archived,
  slots,
  selection,
  sessionTarget,
  resolveSessionSurface,
  onSavePrompt,
  onApplyDefinition,
  v2Definition,
  v2ActiveDefinition,
  applyImpact,
  onCreateCandidate,
  planningProgress = [],
}) => {
  const { work } = view;
  const [prompt, setPrompt] = useState(startIntent?.prompt || draft || work.prompt);
  const [name, setName] = useState(work.name);
  const [nameDirty, setNameDirty] = useState(false);
  const [saveState, setSaveState] = useState<'idle' | 'saving' | 'saved'>('idle');
  const [saveError, setSaveError] = useState<string | null>(null);
  const [generateState, setGenerateState] = useState<GenerateState>('idle');
  const [generateStatusCopy, setGenerateStatusCopy] = useState('');
  const [generateError, setGenerateError] = useState<string | null>(null);
  const [clarification, setClarification] = useState<DefinitionStructuralClarification | null>(null);
  const [clarificationOpen, setClarificationOpen] = useState(false);
  const [clarificationBusy, setClarificationBusy] = useState(false);
  const [clarificationError, setClarificationError] = useState<string | null>(null);
  const [structuralAnswers, setStructuralAnswers] = useState<DefinitionStructuralAnswer[]>([]);
  const saveCompletedRef = useRef(false);
  const savedRevisionRef = useRef<number>(0);
  const [applyState, setApplyState] = useState<'idle' | 'applying'>('idle');
  const [applyError, setApplyError] = useState<string | null>(null);
  const applyIntentRef = useRef<{ signature: string; requestId: string } | null>(null);
  const autoApplyAttemptRef = useRef<string | null>(null);
  const generationOwnedRef = useRef(false);
  const autoStartIntentRef = useRef<string | null>(null);
  const pendingCandidateRef = useRef<{
    candidate: WorkDefinitionRevision;
    expectedRevision: number;
  } | null>(null);
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
    if (!startIntent || autoStartIntentRef.current === startIntent.id) return;
    setPrompt(startIntent.prompt);
    onDraftChange(startIntent.prompt);
    setSaveState('idle');
    setSaveError(null);
    saveCompletedRef.current = false;
  }, [onDraftChange, startIntent]);

  useEffect(() => {
    setName(work.name);
    setNameDirty(false);
  }, [work.id, work.name]);

  useEffect(() => {
    if (generateState === 'idle') {
      setGenerateStatusCopy('');
      return;
    }
    const rotate = () => setGenerateStatusCopy((previous) => pickGenerateBusyCopy(generateState, previous));
    rotate();
    const timer = globalThis.setInterval(rotate, 1800);
    return () => globalThis.clearInterval(timer);
  }, [generateState]);

  useEffect(() => {
    setLocalCandidate(undefined);
    setCandidateImpact(undefined);
    setCandidateError(null);
    setDismissedCandidateRevision(null);
    candidateIntentRef.current = null;
    setClarification(null);
    setClarificationOpen(false);
    setClarificationBusy(false);
    setClarificationError(null);
    setStructuralAnswers([]);
    const restored = readPendingClarification(work.id);
    if (restored && restored.workRevision === view.revision && restored.prompt === prompt.trim()) {
      setClarification(restored.clarification);
      setClarificationOpen(true);
      setStructuralAnswers(restored.answers);
    } else if (restored) {
      writePendingClarification(work.id, null);
    }
  }, [work.id, v2ActiveDefinition?.revision, v2Definition?.revision]);

  useEffect(() => {
    pendingCandidateRef.current = null;
    autoApplyAttemptRef.current = null;
  }, [work.id, v2ActiveDefinition?.revision]);

  // The base for candidate generation: active definition takes precedence;
  // when only a draft exists (initial planning), use the draft as base so
  // the user can generate the first proper structure from the conversation.
  const candidateBase = v2ActiveDefinition ?? (v2Definition?.status === 'draft' ? v2Definition : undefined);

  const applyGeneratedCandidate = async (
    candidate: WorkDefinitionRevision,
    expectedRevision: number,
  ): Promise<boolean> => {
    if (!onApplyDefinition) throw new Error('工作结构应用能力尚未连接。');
    const signature = `${work.id}:${candidate.revision}:${expectedRevision}`;
    if (applyIntentRef.current?.signature !== signature) {
      const suffix = startIntent?.id
        ?? globalThis.crypto?.randomUUID?.()
        ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      applyIntentRef.current = { signature, requestId: `work-definition-${suffix}` };
    }
    setGenerateState('applying');
    setGenerateError(null);
    try {
      const result = await onApplyDefinition({
        workId: work.id,
        revision: candidate.revision,
        expectedRevision,
        requestId: applyIntentRef.current.requestId,
      });
      if (!result.committed) {
        throw Object.assign(
          new Error(result.transportError?.message || '工作结构暂未应用。'),
          { code: result.transportError?.code },
        );
      }
      applyIntentRef.current = null;
      pendingCandidateRef.current = null;
      candidateIntentRef.current = null;
      saveCompletedRef.current = false;
      return true;
    } catch (error) {
      const code = (error as { code?: string }).code;
      if (code === 'revision_conflict' || code === 'request_conflict') {
        applyIntentRef.current = null;
        pendingCandidateRef.current = null;
        candidateIntentRef.current = null;
        saveCompletedRef.current = false;
        setGenerateError('工作状态仍在变化，AI 暂时未能完成这次调整。');
      } else {
        setGenerateError(`这次调整暂未完成：${error instanceof Error ? error.message : String(error)}`);
      }
      if (startIntent) onStartIntentNeedsAttention?.(startIntent.id);
      return false;
    } finally {
      setGenerateState('idle');
    }
  };

  const saveAndGenerate = async (answerOverride?: DefinitionStructuralAnswer[]) => {
    if (!onSavePrompt || !onCreateCandidate || !onApplyDefinition || !candidateBase || readonly || archived || generateState !== 'idle' || !prompt.trim()) return;
    const pending = pendingCandidateRef.current;
    if (pending) {
      const applied = await applyGeneratedCandidate(pending.candidate, pending.expectedRevision);
      if (applied && startIntent) onStartIntentConsumed?.(startIntent.id);
      return;
    }
    const intent = prompt.trim();
    const answers = answerOverride ?? structuralAnswers;
    const explicitName = nameDirty && name.trim() ? name.trim() : undefined;
    const inferName = !v2ActiveDefinition && explicitName === undefined;

    // Phase 1: save prompt and an explicit user name in one idempotent write.
    if (!saveCompletedRef.current) {
      setGenerateState('saving');
      setGenerateError(null);
      try {
        const newRevision = await onSavePrompt(intent, explicitName);
        saveCompletedRef.current = true;
        savedRevisionRef.current = newRevision;
      } catch (error) {
        setGenerateState('idle');
        setGenerateError(error instanceof Error ? error.message : String(error));
        if (startIntent) onStartIntentNeedsAttention?.(startIntent.id);
        return;
      }
    }

    // Phase 2: ask the planner for a complete candidate. Initial Work creation
    // also lets the planner's goal become the inferred title.
    const signature = JSON.stringify([
      work.id,
      candidateBase.revision,
      savedRevisionRef.current,
      intent,
      inferName,
      answers,
    ]);
    if (candidateIntentRef.current?.signature !== signature) {
      const suffix = startIntent?.id
        ?? globalThis.crypto?.randomUUID?.()
        ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      candidateIntentRef.current = { signature, requestId: `work-candidate-${suffix}` };
    }
    setGenerateState('generating');
    setGenerateError(null);
    generationOwnedRef.current = true;
    try {
      const result = await onCreateCandidate({
        workId: work.id,
        intent,
        baseDefinitionRevision: candidateBase.revision,
        expectedRevision: savedRevisionRef.current,
        requestId: candidateIntentRef.current.requestId,
        inferName,
        structuralAnswers: answers,
      });
      if (result.clarification) {
        setClarification(result.clarification);
        setClarificationOpen(true);
        setClarificationError(null);
        writePendingClarification(work.id, {
          schemaVersion: 2,
          workRevision: result.revision,
          prompt: intent,
          clarification: result.clarification,
          answers,
        });
        generationOwnedRef.current = false;
        setGenerateState('idle');
        if (startIntent) onStartIntentNeedsAttention?.(startIntent.id);
        return;
      }
      if (!result.candidate) throw new Error('候选结构已提交，但响应缺少候选 Definition。');
      setClarification(null);
      setClarificationOpen(false);
      setClarificationError(null);
      writePendingClarification(work.id, null);
      pendingCandidateRef.current = {
        candidate: result.candidate,
        expectedRevision: result.revision,
      };
      setCandidateImpact(result.impact);
      setDismissedCandidateRevision(null);
      const applied = await applyGeneratedCandidate(result.candidate, result.revision);
      if (applied && startIntent) onStartIntentConsumed?.(startIntent.id);
      generationOwnedRef.current = false;
    } catch (error) {
      generationOwnedRef.current = false;
      const code = (error as { code?: string }).code;
      if (code === 'revision_conflict' || code === 'request_conflict') {
        candidateIntentRef.current = null;
        saveCompletedRef.current = false;
        setGenerateError('工作状态仍在变化，AI 暂时未能完成这次调整。');
      } else {
        // Save already succeeded. A later clarified submission can continue
        // from the durable phase without replaying the save.
        const message = error instanceof Error ? error.message : String(error);
        setGenerateError(message);
        if (clarification) setClarificationError(message);
      }
      setGenerateState('idle');
      if (startIntent) onStartIntentNeedsAttention?.(startIntent.id);
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
      if (result.clarification) {
        setClarification(result.clarification);
        setClarificationOpen(true);
        writePendingClarification(work.id, {
          schemaVersion: 2,
          workRevision: result.revision,
          prompt: intent,
          clarification: result.clarification,
          answers: [],
        });
        return;
      }
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
  const hasCombinedFlow = !!(
    onSavePrompt
    && onCreateCandidate
    && onApplyDefinition
    && candidateBase
    && !readonly
    && !archived
  );
  useEffect(() => {
    if (
      !startIntent
      || autoStartIntentRef.current === startIntent.id
      || !hasCombinedFlow
      || !candidateBase
      || generateState !== 'idle'
    ) return;
    const intent = startIntent.prompt.trim();
    if (!intent || prompt.trim() !== intent) return;
    autoStartIntentRef.current = startIntent.id;
    void saveAndGenerate();
  }, [
    candidateBase,
    generateState,
    hasCombinedFlow,
    onStartIntentConsumed,
    prompt,
    startIntent,
  ]);
  const projectedCandidate = isSubstantiveDraft ? v2Definition : undefined;
  const candidateDefinition = localCandidate ?? projectedCandidate;
  const visibleCandidate = hasCombinedFlow || candidateDefinition?.revision === dismissedCandidateRevision
    ? undefined
    : candidateDefinition;
  const displayDefinition = hasCombinedFlow
    ? v2ActiveDefinition
    : visibleCandidate ?? v2ActiveDefinition ?? v2Definition;
  // Hide the empty/partial placeholder that has 0 useful nodes — it must not
  // dominate the first screen before the user has generated a real structure.
  const suppressEmptyPlaceholder = !visibleCandidate && !v2ActiveDefinition
    && v2Definition?.status === 'draft' && v2Definition.nodes.length === 0;
  const suppressDefaultSessionSurface = v2Definition !== undefined;

  useEffect(() => {
    if (generationOwnedRef.current || !hasCombinedFlow || !projectedCandidate || v2ActiveDefinition || generateState !== 'idle') return;
    const signature = `${work.id}:${projectedCandidate.revision}:${view.revision}`;
    if (autoApplyAttemptRef.current === signature) return;
    autoApplyAttemptRef.current = signature;
    pendingCandidateRef.current = {
      candidate: projectedCandidate,
      expectedRevision: view.revision,
    };
    void applyGeneratedCandidate(projectedCandidate, view.revision);
  }, [
    generateState,
    hasCombinedFlow,
    projectedCandidate,
    v2ActiveDefinition,
    view.revision,
    work.id,
  ]);

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
  const submitClarification = async (answer: DefinitionStructuralAnswer) => {
    if (clarificationBusy) return;
    const nextAnswers = [
      ...structuralAnswers.filter((item) => item.questionId !== answer.questionId),
      answer,
    ];
    setStructuralAnswers(nextAnswers);
    setClarificationBusy(true);
    setClarificationError(null);
    await saveAndGenerate(nextAnswers);
    setClarificationBusy(false);
  };
  const semanticProgress = planningProgress.filter((item) => item.kind !== 'raw').slice(-6);
  const rawProgress = planningProgress
    .filter((item) => item.kind === 'raw')
    .map((item) => item.text)
    .join('')
    .slice(-2400);
  const showPlanningFeed = generateState !== 'idle' || !!clarification;
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
    if (!resolveSessionSurface) return null;
    if (sessionTarget) {
      return {
        key: `${sessionTarget.sessionRef.sessionPath}\u0000${sessionTarget.sessionRef.branchId}`,
        targetID: `attempt:${sessionTarget.runId}:${sessionTarget.stageId}:${sessionTarget.taskId}:${sessionTarget.attemptId ?? sessionTarget.attemptIndex}`,
        node: resolveSessionSurface(sessionTarget.sessionRef, sessionTarget),
      };
    }
    if (!selection) return null;
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
  }, [archived, readonly, resolveSessionSurface, selection, sessionTarget, work]);

  return (
    <div
      className="wg2-work-card-back"
      data-testid="work-card-back"
      data-work-id={work.id}
      data-readonly={readonly ? 'true' : 'false'}
      data-archived={archived ? 'true' : 'false'}
    >
      <div className="wg2-work-back-header">
        {hasCombinedFlow ? (
          <div className="wg2-work-name-editor">
            <label htmlFor={`wg2-work-name-${work.id}`}>工作名称</label>
            <div className="wg2-work-name-editor__row">
              <input
                id={`wg2-work-name-${work.id}`}
                data-testid="work-name-editor"
                value={name}
                placeholder="留空时由模型自动推定"
                disabled={generateState !== 'idle'}
                onChange={(event) => {
                  setName(event.target.value);
                  setNameDirty(event.target.value.trim() !== work.name);
                }}
                onKeyDown={(event) => {
                  if (event.key === 'Escape') {
                    setName(work.name);
                    setNameDirty(false);
                  }
                }}
              />
            </div>
          </div>
        ) : (
          <h2 className="wg2-work-name">{work.name}</h2>
        )}
      </div>

      {!readonly && !archived && !v2ActiveDefinition && (
        <div className="wg2-work-planning-editor">
          <section className="wg2-work-draft-editor" data-testid="work-draft-editor">
            <div className="wg2-work-draft-heading">
              <h3>任务说明</h3>
              <p>用自然语言说明目标、背景和期望结果。</p>
            </div>
            <label
              className="wg2-work-prompt-field"
              data-busy={generateState !== 'idle' ? 'true' : 'false'}
              aria-busy={generateState !== 'idle'}
            >
              <span className="sr-only">任务说明</span>
              <textarea
                data-testid="work-prompt-editor"
                value={prompt}
                rows={6}
                placeholder="描述你希望 Work 完成的事情…"
                disabled={generateState !== 'idle'}
                onChange={(event) => {
                  setPrompt(event.target.value);
                  onDraftChange(event.target.value);
                  setSaveState('idle');
                  saveCompletedRef.current = false;
                  candidateIntentRef.current = null;
                  setClarification(null);
                  setClarificationOpen(false);
                  setStructuralAnswers([]);
                  writePendingClarification(work.id, null);
                }}
              />
              {showPlanningFeed && (
                <div className="wg2-definition-planning" data-testid="definition-planning-feed">
                  <pre className="wg2-definition-planning__raw" aria-hidden="true">{rawProgress}</pre>
                  <ol className="wg2-definition-planning__steps" aria-live="polite">
                    {semanticProgress.map((item) => (
                      <li key={`${item.requestId}-${item.sequence}`} data-kind={item.kind}>
                        <span className="wg2-definition-planning__dot" aria-hidden="true" />
                        <span>{item.text}</span>
                      </li>
                    ))}
                  </ol>
                </div>
              )}
            </label>
            <div
              className="wg2-work-draft-actions"
              data-busy={generateState !== 'idle' ? 'true' : 'false'}
            >
              {hasCombinedFlow ? (
                <>
                  <button
                    type="button"
                    className="wg2-work-generate-btn"
                    data-testid="work-generate-structure"
                    aria-busy={generateState !== 'idle'}
                    disabled={generateState !== 'idle' || !prompt.trim()}
                    onClick={() => void saveAndGenerate()}
                  >
                    {generateState !== 'idle' && (
                      <LoaderCircle
                        className="wg2-work-generate-btn__spinner"
                        size={15}
                        strokeWidth={2}
                        aria-hidden="true"
                      />
                    )}
                    <span
                      data-testid="work-generate-status-copy"
                      data-state={generateState}
                      aria-hidden={generateState !== 'idle' ? 'true' : undefined}
                    >
                      {generateState !== 'idle'
                        ? generateStatusCopy || generateBusyCopy[generateState][0]
                        : pendingCandidateRef.current ? '继续协调'
                          : v2ActiveDefinition ? '怎么改进' : '生成工作结构'}
                    </span>
                    {generateState !== 'idle' && (
                      <span
                        className="sr-only"
                        role="status"
                        aria-live="polite"
                        data-testid="work-generate-status-a11y"
                      >
                        {generateBusyStatus[generateState]}
                      </span>
                    )}
                  </button>
                  {generateError && <span role="alert" data-testid="work-generate-structure-error">{generateError}</span>}
                  {clarification && !clarificationOpen && (
                    <button
                      type="button"
                      className="wg2-structure-clarification__reopen"
                      data-testid="structure-clarification-reopen"
                      onClick={() => setClarificationOpen(true)}
                    >
                      还差 1 个结构问题
                    </button>
                  )}
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
      {clarification && clarificationOpen && (
        <StructureClarificationCard
          clarification={clarification}
          busy={clarificationBusy}
          error={clarificationError}
          onClose={() => {
            if (!clarificationBusy) setClarificationOpen(false);
          }}
          onSubmit={submitClarification}
        />
      )}

      {displayDefinition && (!suppressEmptyPlaceholder || (candidateBase && !onCreateCandidate)) && (
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
          {!hasCombinedFlow && displayDefinition.status === 'draft' && !readonly && !archived && onApplyDefinition && !v2ActiveDefinition && (
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
