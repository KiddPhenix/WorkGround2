import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { useWorkStore, useWorkUIStore } from '../../store';
import type { BlockInstance } from '../../types';
import { selectV2Tasks, selectV2ActiveDefinition, selectV2Inputs } from '../../store';
import type {
  NodeDef, InputSpec, WorkInput, WorkPatchPreview,
  SubmitWorkInputRequest, SetInputCornerstoneRequest,
  SubmitInputResult, CornerstonePinResult,
  PreviewWorkPatchResult, ApplyWorkPatchResult, PatchScope,
  SelectWorkInputFileRequest, SelectWorkInputFileResult,
} from '../../types_v2';
import { ExecutionRow } from './ExecutionRow';
import type { TaskExpandIntent, TaskRetryIntent } from './ExecutionRow';
import { ExpandedBlock } from './ExpandedBlock';
import type { TaskCollapseIntent, WorkInputRefreshContext } from './ExpandedBlock';
import { DiscussionDrawer } from './discussion/DiscussionDrawer';
import type {
  DiscussionPreviewIntent, DiscussionApplyIntent,
  DiscussionDraftIntent,
} from './discussion/DiscussionDrawer';
import type { DraftValue } from './input/WorkInputHost';
import { v2DiscussionBlockId } from './discussionBlock';
import type { ResultWorkflowChangeRequest } from './ResultShelf';

export interface ExecutionListProps {
  workId: string; expandedTaskId?: string | null;
  runId?: string; sessionId?: string; workRevision?: number;
  blocks?: readonly BlockInstance[];
  onExpandTask?: (intent: TaskExpandIntent) => void;
  onCollapseTask?: (intent: TaskCollapseIntent) => void;
  onRetryTask?: (intent: TaskRetryIntent) => void | Promise<void>;
  onSubmitWorkInput?: (req: SubmitWorkInputRequest) => Promise<SubmitInputResult>;
  onSetCornerstone?: (req: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  onUnsetCornerstone?: (req: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  onRefreshAuthoritative?: (context: WorkInputRefreshContext) => Promise<void>;
  onSelectFile?: (req: SelectWorkInputFileRequest) => Promise<SelectWorkInputFileResult>;
  onPreviewPatch?: (intent: DiscussionPreviewIntent) => Promise<PreviewWorkPatchResult>;
  onApplyPatch?: (intent: DiscussionApplyIntent) => Promise<ApplyWorkPatchResult>;
  onDiscussionDraftChange?: (intent: DiscussionDraftIntent) => void;
  externalWorkflowDiscussion?: ResultWorkflowChangeRequest;
}

function inputIdentity(wid: string, rid: string, tid: string, bid: string, iid: string, sid: string, dr: number, ir: number) {
  return `${wid}\u0000${rid}\u0000${tid}\u0000${bid}\u0000${iid}\u0000${sid}\u0000${dr}\u0000${ir}`;
}
function discIdentity(
  wid: string,
  rid: string,
  sid: string,
  tid: string,
  bid: string,
  definitionRevision: number,
  blockRevision: number,
) {
  return `${wid}\u0000${rid}\u0000${sid}\u0000${tid}\u0000${bid}\u0000${definitionRevision}\u0000${blockRevision}`;
}
function discTargetIdentity(wid: string, sid: string, tid: string, bid: string) {
  return `${wid}\u0000${sid}\u0000${tid}\u0000${bid}`;
}

export const ExecutionList: React.FC<ExecutionListProps> = ({
  workId, expandedTaskId, runId, sessionId, workRevision, blocks,
  onExpandTask, onCollapseTask, onRetryTask,
  onSubmitWorkInput, onSetCornerstone, onUnsetCornerstone, onRefreshAuthoritative, onSelectFile,
  onPreviewPatch, onApplyPatch, onDiscussionDraftChange, externalWorkflowDiscussion,
}) => {
  const allTasks = useWorkStore((s) => selectV2Tasks(s.v2Tasks, workId));
  const definition = useWorkStore((s) =>
    selectV2ActiveDefinition(s.v2ActiveDefinitions, s.v2Definitions, workId));
  const allInputs = useWorkStore((s) => selectV2Inputs(s.v2Inputs, workId));
  const cardState = useWorkUIStore((s) => s.cardByWork[workId]);
  const setDiscussionDraft = useWorkUIStore((s) => s.setDiscussionDraft);
  const setInputDraft = useWorkUIStore((s) => s.setInputDraft);
  const setInputDirtyFlag = useWorkUIStore((s) => s.setInputDirtyFlag);
  const setCommittedRequestId = useWorkUIStore((s) => s.setCommittedRequestId);

  const nodeMap = useMemo(() => {
    const m = new Map<string, NodeDef>();
    definition?.nodes?.forEach((n) => m.set(n.id, n));
    return m;
  }, [definition]);
  const inputSpecs = useMemo<InputSpec[]>(() => definition?.inputSpecs ?? [], [definition]);
  const blockMap = useMemo(() => {
    const values = new Map<string, BlockInstance>();
    for (const block of blocks ?? []) {
      if (!block.tombstone) values.set(block.id, block);
    }
    return values;
  }, [blocks]);
  const removedBlockIDs = useMemo(
    () => new Set((blocks ?? []).filter((block) => block.tombstone).map((block) => block.id)),
    [blocks],
  );
  const defRev = definition?.revision ?? 1;
  // Production passes the active run explicitly. Standalone renderers fall
  // back to the last projected run identity without rewriting any task.
  const effRunId = runId ?? allTasks[allTasks.length - 1]?.runId ?? '';
  const tasks = useMemo(
    () => effRunId ? allTasks.filter((task) => task.runId === effRunId) : [],
    [allTasks, effRunId],
  );
  const resolvePatchLabel = useCallback((
    kind: 'node' | 'task' | 'slot' | 'block',
    id: string,
  ): string | undefined => {
    switch (kind) {
      case 'node':
        return nodeMap.get(id)?.title;
      case 'task': {
        const task = tasks.find((candidate) => candidate.id === id || candidate.nodeId === id);
        return task?.title ?? nodeMap.get(id)?.title;
      }
      case 'slot':
        return definition?.artifactSlots?.find((slot) => slot.id === id)?.title;
      case 'block':
        return blockMap.get(id)?.title;
    }
  }, [blockMap, definition?.artifactSlots, nodeMap, tasks]);
  const resolvePatchOrder = useCallback((
    kind: 'node' | 'task' | 'slot' | 'block',
    id: string,
  ): number | undefined => {
    if (kind === 'node' || kind === 'task') {
      const nodeId = kind === 'task'
        ? tasks.find((task) => task.id === id || task.nodeId === id)?.nodeId ?? id
        : id;
      const index = definition?.nodes?.findIndex((node) => node.id === nodeId) ?? -1;
      return index >= 0 ? index : undefined;
    }
    if (kind === 'slot') {
      const index = definition?.artifactSlots?.findIndex((slot) => slot.id === id) ?? -1;
      return index >= 0 ? index : undefined;
    }
    return undefined;
  }, [definition?.artifactSlots, definition?.nodes, tasks]);

  const hasFullTypedInput = !!(onSubmitWorkInput && onSetCornerstone && onUnsetCornerstone && onRefreshAuthoritative);

  // Input drafts read from persistent UI store, dirty tracked via useRef (volatile per mount).
  const inputDirtyRef = useRef<Record<string, boolean>>({});

  const persistedDrafts = cardState?.inputDrafts ?? {};
  const persistedDirty = cardState?.inputDirtyFlags ?? {};

  const getInputKey = useCallback(
    (tid: string, bid: string, iid: string, sid: string, ir: number) =>
      inputIdentity(workId, effRunId, tid, bid, iid, sid, defRev, ir),
    [workId, effRunId, defRev]);

  const handleInputDraftChange = useCallback(
    (tid: string, bid: string, iid: string, sid: string, ir: number, value: DraftValue) => {
      const key = getInputKey(tid, bid, iid, sid, ir);
      inputDirtyRef.current[key] = true;
      setInputDirtyFlag(workId, key);
      setInputDraft(workId, key, value);
    }, [getInputKey, workId, setInputDraft, setInputDirtyFlag]);

  const resolveInputDraft = useCallback(
    (tid: string, bid: string, spec: InputSpec, wi: WorkInput): DraftValue => {
      const key = getInputKey(tid, bid, wi.id, spec.id, wi.revision);
      if (persistedDirty[key] || inputDirtyRef.current[key]) {
        const v = persistedDrafts[key];
        if (v !== undefined) return v as DraftValue;
      }
      return computeInitialDraft(spec, wi);
    }, [getInputKey, persistedDrafts, persistedDirty]);

  const resolveCommittedInputRequestIds = useCallback(
    (tid: string, bid: string, spec: InputSpec, wi: WorkInput) => {
      const key = getInputKey(tid, bid, wi.id, spec.id, wi.revision);
      return {
        submit: cardState?.committedRequestIds?.[`${key}\u0000submit`],
        pin: cardState?.committedRequestIds?.[`${key}\u0000pin`],
        unpin: cardState?.committedRequestIds?.[`${key}\u0000unpin`],
      };
    },
    [cardState?.committedRequestIds, getInputKey],
  );

  const handleInputRequestCommitted = useCallback(
    (
      tid: string,
      bid: string,
      spec: InputSpec,
      wi: WorkInput,
      operation: 'submit' | 'pin' | 'unpin',
      requestId: string,
    ) => {
      const key = getInputKey(tid, bid, wi.id, spec.id, wi.revision);
      setCommittedRequestId(workId, `${key}\u0000${operation}`, requestId);
    },
    [getInputKey, setCommittedRequestId, workId],
  );

  // Discussion state — single global drawer, persistent via UI store
  const [dOpen, setDOpen] = useState(false);
  const [dBid, setDBid] = useState(''); const [dTid, setDTid] = useState('');
  const [dTitle, setDTitle] = useState(''); const [dScope, setDScope] = useState<PatchScope>('block');
  const [isPreviewing, setIsPreviewing] = useState(false);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [patchPreview, setPatchPreview] = useState<WorkPatchPreview | null>(null);
  const [isApplying, setIsApplying] = useState(false);
  const [applyResult, setApplyResult] = useState<ApplyWorkPatchResult | null>(null);
  const [applyError, setApplyError] = useState<string | null>(null);
  const [revConflict, setRevConflict] = useState(false);
  const [digConflict, setDigConflict] = useState(false);
  const [dBlockRev, setDBlockRev] = useState(1);
  const [dScopeLocked, setDScopeLocked] = useState(false);
  const discussionEpochRef = useRef(0);
  const activeDiscussionRef = useRef('');
  const activeDiscussionTargetRef = useRef('');

  const discKey = useMemo(
    () => discIdentity(workId, effRunId, sessionId ?? '', dTid, dBid, defRev, dBlockRev),
    [workId, effRunId, sessionId, dTid, dBid, defRev, dBlockRev],
  );
  const discTarget = useMemo(
    () => discTargetIdentity(workId, sessionId ?? '', dTid, dBid),
    [workId, sessionId, dTid, dBid],
  );
  const discDraft = cardState?.discussionDrafts?.[discKey] ?? '';
  activeDiscussionRef.current = discKey;
  activeDiscussionTargetRef.current = discTarget;
  const committedPreviewRid = cardState?.committedRequestIds?.[`${discKey}\u0000preview`];
  const committedApplyRid = cardState?.committedRequestIds?.[`${discKey}\u0000apply`];

  const handleDOpen = useCallback((
    tid: string,
    bid: string,
    title: string,
    br: number,
    scope: PatchScope,
    scopeLocked = false,
  ) => {
    discussionEpochRef.current++;
    setDOpen(true); setDTid(tid); setDBid(bid); setDTitle(title); setDBlockRev(br);
    setDScope(scope);
    setDScopeLocked(scopeLocked);
    setIsPreviewing(false); setIsApplying(false);
    setApplyResult(null); setApplyError(null); setRevConflict(false); setDigConflict(false);
    setPreviewError(null); setPatchPreview(null);
  }, []);
  const externalRequestRef = useRef('');
  useEffect(() => {
    if (!externalWorkflowDiscussion || externalRequestRef.current === externalWorkflowDiscussion.token) return;
    const task = tasks.find((candidate) => candidate.nodeId === externalWorkflowDiscussion.nodeId);
    const node = nodeMap.get(externalWorkflowDiscussion.nodeId);
    if (!task || !node) return;
    const boundInputs = allInputs.filter((input) => input.runId === task.runId && input.taskId === task.id);
    const inputByID = new Map(boundInputs.map((input) => [input.id, input]));
    const blockID = [
      ...(task.waitingInputIds ?? []).map((id) => inputByID.get(id)?.blockId),
      ...boundInputs.map((input) => input.blockId),
      ...(node.blockIds ?? []),
      v2DiscussionBlockId(node.id),
    ].find((id): id is string => Boolean(id && !removedBlockIDs.has(id)));
    if (!blockID) return;
    const blockRevision = blockMap.get(blockID)?.revision ?? 1;
    externalRequestRef.current = externalWorkflowDiscussion.token;
    const intent = {
      workId,
      taskId: task.id,
      blockId: blockID,
      text: externalWorkflowDiscussion.instruction,
    };
    setDiscussionDraft(
      workId,
      discIdentity(workId, effRunId, sessionId ?? '', task.id, blockID, defRev, blockRevision),
      intent.text,
    );
    onDiscussionDraftChange?.(intent);
    handleDOpen(task.id, blockID, externalWorkflowDiscussion.title, blockRevision, 'workflow', true);
  }, [
    allInputs, blockMap, defRev, effRunId, externalWorkflowDiscussion, handleDOpen,
    nodeMap, onDiscussionDraftChange, removedBlockIDs, sessionId, setDiscussionDraft, tasks, workId,
  ]);
  const handleDClose = useCallback(() => {
    discussionEpochRef.current++;
    setDOpen(false);
    setPreviewError(null); setIsPreviewing(false); setIsApplying(false);
  }, []);
  const handleDDraft = useCallback((intent: DiscussionDraftIntent) => {
    const k = discIdentity(
      intent.workId,
      effRunId,
      sessionId ?? '',
      intent.taskId,
      intent.blockId,
      defRev,
      dBlockRev,
    );
    setDiscussionDraft(workId, k, intent.text);
    onDiscussionDraftChange?.(intent);
  }, [effRunId, sessionId, defRev, dBlockRev, workId, setDiscussionDraft, onDiscussionDraftChange]);

  const handleDPreview = useCallback(async (intent: DiscussionPreviewIntent) => {
    if (!onPreviewPatch) return;
    const epoch = ++discussionEpochRef.current;
    const expectedIdentity = discIdentity(
      intent.workId,
      intent.runId,
      intent.sessionId,
      intent.taskId,
      intent.blockId,
      intent.definitionRevision,
      intent.blockRevision,
    );
    setIsPreviewing(true); setPreviewError(null); setPatchPreview(null);
    setRevConflict(false); setDigConflict(false); setApplyResult(null); setApplyError(null);
    try {
      const r = await onPreviewPatch(intent);
      if (epoch !== discussionEpochRef.current || activeDiscussionRef.current !== expectedIdentity) return;
      if (r.preview) {
        const preview = r.preview;
        if (
          preview.workId !== intent.workId
          || preview.runId !== intent.runId
          || preview.taskId !== intent.taskId
          || preview.blockId !== intent.blockId
          || preview.sessionId !== intent.sessionId
          || preview.baseDefinitionRev !== intent.definitionRevision
          || preview.baseBlockRev !== intent.blockRevision
        ) {
          setPreviewError('改动校验结果与当前 Block 不一致，已忽略迟到结果。');
          return;
        }
        setPatchPreview(r.preview);
        if (r.committed) {
          setCommittedRequestId(workId, `${discKey}\u0000preview`, intent.requestId);
        }
      }
      else setPreviewError(r.error ?? r.transportError?.message ?? '预览失败');
    } catch (e) {
      if (epoch !== discussionEpochRef.current || activeDiscussionRef.current !== expectedIdentity) return;
      const code = (e as { code?: string }).code;
      if (code === 'revision_conflict') setRevConflict(true);
      setPreviewError(e instanceof Error ? e.message : String(e));
    }
    finally {
      if (epoch === discussionEpochRef.current && activeDiscussionRef.current === expectedIdentity) {
        setIsPreviewing(false);
      }
    }
  }, [onPreviewPatch, setCommittedRequestId, workId, discKey]);

  const handleDApply = useCallback(async (intent: DiscussionApplyIntent) => {
    if (!onApplyPatch || !onRefreshAuthoritative) return;
    const epoch = ++discussionEpochRef.current;
    const expectedTarget = discTarget;
    setIsApplying(true); setApplyError(null); setApplyResult(null);
    try {
      const r = await onApplyPatch(intent);
      if (epoch !== discussionEpochRef.current || activeDiscussionTargetRef.current !== expectedTarget) return;
      const receiptMissing = r.committed && !r.receipt;
      const visibleResult = receiptMissing
        ? {
            ...r,
            recoverable: true,
            transportError: {
              code: 'contract_receipt_missing',
              message: '改动已提交，但确认响应不完整；正在刷新状态。',
              operation: 'ApplyWorkPatch',
              workId,
              requestId: intent.requestId,
              committed: true,
              recoverable: true,
            },
          }
        : r;
      setApplyResult(visibleResult);
      if (r.committed) {
        setCommittedRequestId(workId, `${discKey}\u0000apply`, intent.requestId);
        if (receiptMissing) {
          setApplyError('改动已提交，但确认响应不完整；正在刷新状态。');
        }
        try {
          await onRefreshAuthoritative({
            workId,
            inputId: '',
            requestId: intent.requestId,
            revision: r.newRevision,
            operation: 'patch',
          });
          if (epoch !== discussionEpochRef.current) return;
          if (!receiptMissing && !r.error && !r.transportError) {
            handleDClose();
            return;
          }
        } catch (refreshError) {
          if (epoch !== discussionEpochRef.current) return;
          const refreshMessage = `改动已提交，但刷新最新状态失败：${
            refreshError instanceof Error ? refreshError.message : String(refreshError)
          }`;
          setApplyError((current) => current ? `${current}；${refreshMessage}` : refreshMessage);
        }
      }
      const conflictText = `${r.error ?? ''} ${r.transportError?.message ?? ''}`.toLowerCase();
      if (r.transportError?.code === 'revision_conflict' || conflictText.includes('revision mismatch')) {
        setRevConflict(true);
      }
      if (conflictText.includes('digest mismatch')) setDigConflict(true);
      if (!r.committed && r.error) setApplyError(r.error);
      if (!r.committed && r.transportError) setApplyError(r.transportError.message);
    } catch (e) {
      if (epoch !== discussionEpochRef.current || activeDiscussionTargetRef.current !== expectedTarget) return;
      const code = (e as { code?: string }).code;
      if (code === 'revision_conflict') setRevConflict(true);
      setApplyError(e instanceof Error ? e.message : String(e));
    }
    finally {
      if (epoch === discussionEpochRef.current) {
        setIsApplying(false);
      }
    }
  }, [onApplyPatch, onRefreshAuthoritative, workId, discKey, discTarget, setCommittedRequestId, handleDClose]);
  const handleDismiss = useCallback(() => { setApplyResult(null); setApplyError(null); }, []);

  const ordered = useMemo(() => {
    if (!definition?.nodes?.length) return tasks;
    const om = new Map<string, number>();
    definition.nodes.forEach((n, i) => om.set(n.id, i));
    return tasks.map((t, pi) => ({ t, pi })).sort((a, b) => {
      const ai = om.get(a.t.nodeId), bi = om.get(b.t.nodeId);
      if (ai !== undefined && bi !== undefined && ai !== bi) return ai - bi;
      if (ai !== undefined) return -1; if (bi !== undefined) return 1;
      return a.pi - b.pi;
    }).map(({ t }) => t);
  }, [tasks, definition]);

  if (!ordered.length) {
    return (
      <section className="wg2-el-frame" aria-label="AI 执行任务">
        <ExecutionListHeading />
        <div className="wg2-el-empty" data-testid="execution-list-empty" role="status" aria-live="polite">
          暂无执行任务
        </div>
      </section>
    );
  }

  return (
    <>
      <section className="wg2-el-frame" aria-label="AI 执行任务">
        <ExecutionListHeading />
        <ul className="wg2-el-list" data-testid="execution-list" role="list" aria-label="执行任务列表">
          {ordered.map((task) => {
            const nd = nodeMap.get(task.nodeId);
            const isExpanded = expandedTaskId === task.id;
            const blockIDs = new Set(nd?.blockIds ?? []);
            const boundInputs = allInputs.filter((input) =>
              input.runId === task.runId
              && input.taskId === task.id);
            const taskInputs = boundInputs.filter((input) =>
              blockIDs.size === 0 || blockIDs.has(input.blockId));
            const inputByID = new Map(boundInputs.map((input) => [input.id, input]));
            const discussionBlockIDs = [
              ...(task.waitingInputIds ?? []).map((id) => inputByID.get(id)?.blockId),
              ...boundInputs.map((input) => input.blockId),
              ...(nd?.blockIds ?? []),
              ...(nd ? [v2DiscussionBlockId(nd.id)] : []),
            ];
            const discussionBlockID = discussionBlockIDs.find(
              (id): id is string => Boolean(id && !removedBlockIDs.has(id)),
            );
            const materializedBlock = discussionBlockID ? blockMap.get(discussionBlockID) : undefined;
            const discussionBlock = materializedBlock ?? (discussionBlockID ? {
              id: discussionBlockID,
              title: task.title,
              revision: 1,
            } : undefined);
            return (
              <li
                key={`${task.runId}\u0000${task.id}`}
                className="wg2-el-item"
                data-expanded={isExpanded}
                data-task-state={task.state}
                role="listitem"
                data-testid={`execution-list-item-${task.id}`}
              >
                <ExecutionRow
                  task={task}
                  workId={workId}
                  nodeDef={nd}
                  isExpanded={isExpanded}
                  onToggleExpand={(intent) => {
                    isExpanded ? onCollapseTask?.({ workId: intent.workId, taskId: intent.taskId })
                      : onExpandTask?.(intent);
                  }}
                  onRetry={onRetryTask}
                  onDiscuss={discussionBlock
                    ? () => handleDOpen(
                        task.id,
                        discussionBlock.id,
                        task.title,
                        discussionBlock.revision,
                        task.state === 'completed' ? 'workflow' : 'block',
                      )
                    : undefined}
                />
                {isExpanded && (
                  <ExpandedBlock
                    task={task} workId={workId} runId={task.runId} sessionId={sessionId ?? ''}
                    nodeDef={nd} inputSpecs={inputSpecs} workInputs={taskInputs}
                    definitionRevision={defRev} workRevision={workRevision ?? 1}
                    hasTypedInput={hasFullTypedInput}
                    onRetry={onRetryTask}
                    onSubmitWorkInput={onSubmitWorkInput} onSetCornerstone={onSetCornerstone}
                    onUnsetCornerstone={onUnsetCornerstone} onRefreshAuthoritative={onRefreshAuthoritative}
                    onSelectFile={onSelectFile}
                    onInputDraftChange={(tid, bid, iid, sid, ir, v) => handleInputDraftChange(tid, bid, iid, sid, ir, v)}
                    resolveInputDraft={(tid, bid, spec, wi) => resolveInputDraft(tid, bid, spec, wi)}
                    resolveCommittedRequestIds={resolveCommittedInputRequestIds}
                    onInputRequestCommitted={handleInputRequestCommitted}
                  />
                )}
              </li>
            );
          })}
        </ul>
      </section>
      {dOpen && (
        <DiscussionDrawer
          workId={workId} taskId={dTid} blockId={dBid} runId={effRunId} sessionId={sessionId ?? ''}
          workRevision={workRevision ?? 1} definitionRevision={defRev}
          blockRevision={dBlockRev} taskTitle={dTitle}
          resolvePatchLabel={resolvePatchLabel}
          resolvePatchOrder={resolvePatchOrder}
          draftText={discDraft} onDraftChange={handleDDraft}
          patchPreview={patchPreview} isPreviewing={isPreviewing}
          previewError={previewError} isApplying={isApplying}
          applyResult={applyResult} applyError={applyError}
          selectedScope={dScope} onScopeChange={setDScope}
          scopeLocked={dScopeLocked}
          revisionConflict={revConflict} digestConflict={digConflict}
          onClose={handleDClose} onPreview={handleDPreview}
          onApply={handleDApply} onDismissResult={handleDismiss}
          committedPreviewRequestId={committedPreviewRid}
           committedApplyRequestId={committedApplyRid}
          previewAvailable={Boolean(onPreviewPatch)}
          applyAvailable={Boolean(onApplyPatch && onRefreshAuthoritative)}
        />
      )}
    </>
  );
};

const ExecutionListHeading: React.FC = () => (
  <div className="wg2-el-heading">
    <strong>AI 正在执行</strong>
    <span>无需额外照看，各项任务将并行推进</span>
  </div>
);

function computeInitialDraft(spec: InputSpec, wi: WorkInput): DraftValue {
  if (wi.value != null) {
    try { return JSON.parse(typeof wi.value === 'string' ? wi.value : JSON.stringify(wi.value)) as DraftValue; }
    catch { return String(wi.value ?? ''); }
  }
  if (spec.defaultValue != null) {
    try { return JSON.parse(typeof spec.defaultValue === 'string' ? spec.defaultValue : JSON.stringify(spec.defaultValue)) as DraftValue; }
    catch { return ''; }
  }
  return spec.kind === 'approval' ? false : '';
}
