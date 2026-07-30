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
import type { ResultWorkflowChangeRequest, WorkflowChangeState } from './ResultShelf';

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
  onWorkflowChangeState?: (state: WorkflowChangeState | null) => void;
  /** Called when user clicks info on a task row with a session ref. */
  onTaskInfo?: (runId: string, taskId: string) => void;
  /** Run+Task identities that have session refs — only these rows show the info action. */
  taskInfoTaskKeys?: Set<string>;
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

function wfChangeCommittedKey(workId: string, token: string, kind: 'preview' | 'apply'): string {
  return `wfchange\u0000${workId}\u0000${token}\u0000${kind}`;
}

function deriveTaskId(runId: string, nodeId: string): string {
  const byteLength = (value: string) => new TextEncoder().encode(value).length;
  return `${byteLength(runId)}:${runId}/${byteLength(nodeId)}:${nodeId}`;
}

export const ExecutionList: React.FC<ExecutionListProps> = ({
  workId, expandedTaskId, runId, sessionId, workRevision, blocks,
  onExpandTask, onCollapseTask, onRetryTask,
  onSubmitWorkInput, onSetCornerstone, onUnsetCornerstone, onRefreshAuthoritative, onSelectFile,
  onPreviewPatch, onApplyPatch, onDiscussionDraftChange, externalWorkflowDiscussion,
  onWorkflowChangeState,
  onTaskInfo, taskInfoTaskKeys,
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
  const projectedTasks = useMemo(
    () => effRunId ? allTasks.filter((task) => task.runId === effRunId) : [],
    [allTasks, effRunId],
  );
  const tasks = useMemo(() => {
    if (!effRunId || !definition?.nodes?.length) return projectedTasks;
    const runtimeByNode = new Map(projectedTasks.map((task) => [task.nodeId, task]));
    const plannedNodeIds = new Set(definition.nodes.map((node) => node.id));
    return [
      ...definition.nodes.map((node) => runtimeByNode.get(node.id) ?? {
        id: deriveTaskId(effRunId, node.id),
        runId: effRunId,
        nodeId: node.id,
        title: node.title,
        state: 'pending' as const,
        retryable: false,
        updatedAt: definition.createdAt,
      }),
      ...projectedTasks.filter((task) => !plannedNodeIds.has(task.nodeId)),
    ];
  }, [definition, effRunId, projectedTasks]);
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
  // ── Direct workflow change: preview → apply → refresh (no drawer) ──
  const wfChangeEpochRef = useRef(0);
  const externalRequestRef = useRef('');
  const wfChangeTokenRef = useRef('');
  useEffect(() => {
    if (!externalWorkflowDiscussion) return;
    const token = externalWorkflowDiscussion.token;
    const attempt = externalWorkflowDiscussion.attempt ?? 0;
    const dispatchKey = `${token}\u0000${attempt}`;
    if (externalRequestRef.current === dispatchKey) return;
    externalRequestRef.current = dispatchKey;

    const fail = (error: string) => {
      onWorkflowChangeState?.({ token, status: 'failed', error });
    };
    if (!onPreviewPatch || !onApplyPatch || !onRefreshAuthoritative) {
      fail('当前环境无法更新流程');
      return;
    }
    const task = tasks.find((candidate) => candidate.nodeId === externalWorkflowDiscussion.nodeId);
    const node = nodeMap.get(externalWorkflowDiscussion.nodeId);
    if (!task || !node) {
      fail('暂时无法定位相关任务，请刷新工作后重试');
      return;
    }
    const boundInputs = allInputs.filter((input) => input.runId === task.runId && input.taskId === task.id);
    const inputByID = new Map(boundInputs.map((input) => [input.id, input]));
    const blockID = [
      ...(task.waitingInputIds ?? []).map((id) => inputByID.get(id)?.blockId),
      ...boundInputs.map((input) => input.blockId),
      ...(node.blockIds ?? []),
      v2DiscussionBlockId(node.id),
    ].find((id): id is string => Boolean(id && !removedBlockIDs.has(id)));
    if (!blockID) {
      fail('暂时无法定位流程内容，请刷新工作后重试');
      return;
    }
    const blockRevision = blockMap.get(blockID)?.revision ?? 1;
    const baseWorkRevision = workRevision ?? 1;

    const epoch = ++wfChangeEpochRef.current;
    wfChangeTokenRef.current = token;
    onWorkflowChangeState?.({ token, status: 'updating' });

    (async () => {
      try {
        let currentDefinitionRevision = defRev;
        let currentBlockRevision = blockRevision;
        let currentWorkRevision = baseWorkRevision;
        for (let round = 0; round < 2; round++) {
          const previewKey = wfChangeCommittedKey(workId, token, 'preview');
          const committedPreviewId = cardState?.committedRequestIds?.[previewKey];
          const previewRid = round === 0
            ? committedPreviewId ?? `wf-preview-${token}`
            : `wf-preview-${token}-r${round}`;
          let previewResult: PreviewWorkPatchResult | undefined;
          let failure = '';
          for (let retry = 0; retry < 2; retry++) {
            try {
              previewResult = await onPreviewPatch({
                workId,
                runId: effRunId,
                taskId: task.id,
                blockId: blockID,
                sessionId: sessionId ?? '',
                instruction: externalWorkflowDiscussion.instruction,
                definitionRevision: currentDefinitionRevision,
                blockRevision: currentBlockRevision,
                scope: 'workflow',
                requestId: previewRid,
              });
              failure = previewResult.error ?? previewResult.transportError?.message ?? '';
              if (previewResult.preview ?? previewResult.receipt?.resultPatch) break;
            } catch (error) {
              failure = error instanceof Error ? error.message : String(error);
            }
          }
          if (epoch !== wfChangeEpochRef.current || wfChangeTokenRef.current !== token) return;
          const preview = previewResult?.preview ?? previewResult?.receipt?.resultPatch;
          if (!preview) throw new Error(failure || 'AI 暂时无法理解这次成果调整');
          if (
            preview.workId !== workId
            || preview.runId !== effRunId
            || preview.taskId !== task.id
            || preview.blockId !== blockID
            || preview.sessionId !== (sessionId ?? '')
            || preview.baseDefinitionRev !== currentDefinitionRevision
            || preview.baseBlockRev !== currentBlockRevision
            || preview.scope !== 'workflow'
          ) {
            throw new Error('工作状态已变化，AI 未采用过期的调整结果');
          }
          if (previewResult?.committed) setCommittedRequestId(workId, previewKey, previewRid);

          const applyKey = wfChangeCommittedKey(workId, token, 'apply');
          const committedApplyId = cardState?.committedRequestIds?.[applyKey];
          const applyRid = round === 0
            ? committedApplyId ?? `wf-apply-${token}`
            : `wf-apply-${token}-r${round}`;
          let result: ApplyWorkPatchResult | undefined;
          failure = '';
          for (let retry = 0; retry < 2; retry++) {
            try {
              result = await onApplyPatch({
                workId,
                patchId: preview.id,
                previewDigest: preview.digest,
                scope: 'workflow',
                expectedRevision: currentWorkRevision,
                requestId: applyRid,
              });
              failure = result.error ?? result.transportError?.message ?? '';
              if (result.committed) break;
            } catch (error) {
              failure = error instanceof Error ? error.message : String(error);
            }
          }
          if (epoch !== wfChangeEpochRef.current || wfChangeTokenRef.current !== token) return;
          if (result?.committed) {
            setCommittedRequestId(workId, applyKey, applyRid);
            let refreshFailure = '';
            for (let retry = 0; retry < 3; retry++) {
              try {
                await onRefreshAuthoritative({
                  workId,
                  inputId: '',
                  requestId: `wf-refresh-${token}`,
                  revision: result.newRevision,
                  operation: 'patch',
                });
                refreshFailure = '';
                break;
              } catch (error) {
                refreshFailure = error instanceof Error ? error.message : String(error);
              }
            }
            if (refreshFailure) throw new Error(`修改已提交，但最新状态暂时未同步：${refreshFailure}`);
            if (epoch !== wfChangeEpochRef.current || wfChangeTokenRef.current !== token) return;
            onWorkflowChangeState?.({ token, status: 'applied' });
            return;
          }

          const conflict = `${result?.transportError?.code ?? ''} ${failure}`.toLowerCase();
          if (
            round === 0
            && (conflict.includes('revision') || conflict.includes('digest') || conflict.includes('expired'))
          ) {
            try {
              await onRefreshAuthoritative({
                workId,
                inputId: '',
                requestId: `wf-rebase-${token}`,
                revision: currentWorkRevision,
                operation: 'patch',
              });
            } catch {
              // A fresh preview below remains the source of truth.
            }
            const state = useWorkStore.getState();
            const latestDefinition = selectV2ActiveDefinition(
              state.v2ActiveDefinitions,
              state.v2Definitions,
              workId,
            );
            currentDefinitionRevision = latestDefinition?.revision ?? currentDefinitionRevision;
            currentBlockRevision = state.works[workId]?.work.blocks.find(
              (block) => block.id === blockID,
            )?.revision ?? currentBlockRevision;
            currentWorkRevision = state.revisions[workId] ?? currentWorkRevision;
            continue;
          }
          throw new Error(failure || '这次成果调整暂时无法完成');
        }
      } catch (e) {
        if (epoch !== wfChangeEpochRef.current || wfChangeTokenRef.current !== token) return;
        onWorkflowChangeState?.({
          token,
          status: 'failed',
          error: e instanceof Error ? e.message : String(e),
        });
      }
    })();
  }, [
    allInputs, blockMap, cardState?.committedRequestIds, defRev, effRunId,
    externalWorkflowDiscussion, nodeMap, onApplyPatch, onPreviewPatch,
    onRefreshAuthoritative, onWorkflowChangeState, removedBlockIDs,
    sessionId, setCommittedRequestId, tasks, workId, workRevision,
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
    if (!onPreviewPatch || !onApplyPatch || !onRefreshAuthoritative) return;
    const epoch = ++discussionEpochRef.current;
    const expectedTarget = discTarget;
    setIsPreviewing(true); setPreviewError(null); setPatchPreview(null);
    setRevConflict(false); setDigConflict(false); setApplyResult(null); setApplyError(null);
    try {
      let currentIntent = intent;
      for (let round = 0; round < 2; round++) {
        let previewResult: PreviewWorkPatchResult | undefined;
        let previewFailure = '';
        for (let attempt = 0; attempt < 2; attempt++) {
          try {
            previewResult = await onPreviewPatch(currentIntent);
            previewFailure = previewResult.error ?? previewResult.transportError?.message ?? '';
            if (previewResult.preview ?? previewResult.receipt?.resultPatch) break;
          } catch (error) {
            previewFailure = error instanceof Error ? error.message : String(error);
          }
          if (epoch !== discussionEpochRef.current || activeDiscussionTargetRef.current !== expectedTarget) return;
        }
        if (epoch !== discussionEpochRef.current || activeDiscussionTargetRef.current !== expectedTarget) return;
        const preview = previewResult?.preview ?? previewResult?.receipt?.resultPatch;
        if (!preview) throw new Error(previewFailure || 'AI 暂时无法理解这次调整，请补充要求后再提交。');
        if (
          preview.workId !== currentIntent.workId
          || preview.runId !== currentIntent.runId
          || preview.taskId !== currentIntent.taskId
          || preview.blockId !== currentIntent.blockId
          || preview.sessionId !== currentIntent.sessionId
          || preview.baseDefinitionRev !== currentIntent.definitionRevision
          || preview.baseBlockRev !== currentIntent.blockRevision
          || preview.scope !== currentIntent.scope
        ) {
          throw new Error('工作状态刚刚发生变化，AI 未采用过期的调整结果。');
        }
        setIsPreviewing(false);
        setIsApplying(true);
        if (previewResult?.committed) {
          setCommittedRequestId(workId, `${discKey}\u0000preview`, currentIntent.requestId);
        }

        const applyIntent: DiscussionApplyIntent = {
          workId,
          patchId: preview.id,
          previewDigest: preview.digest,
          scope: preview.scope,
          expectedRevision: useWorkStore.getState().revisions[workId] ?? workRevision ?? 1,
          requestId: `${currentIntent.requestId}/apply`,
        };
        let result: ApplyWorkPatchResult | undefined;
        let applyFailure = '';
        for (let attempt = 0; attempt < 2; attempt++) {
          try {
            result = await onApplyPatch(applyIntent);
            applyFailure = result.error ?? result.transportError?.message ?? '';
            if (result.committed) break;
          } catch (error) {
            applyFailure = error instanceof Error ? error.message : String(error);
          }
          if (epoch !== discussionEpochRef.current || activeDiscussionTargetRef.current !== expectedTarget) return;
        }
        if (epoch !== discussionEpochRef.current || activeDiscussionTargetRef.current !== expectedTarget) return;
        if (result?.committed) {
          setApplyResult(result);
          setCommittedRequestId(workId, `${discKey}\u0000apply`, applyIntent.requestId);
          let refreshFailure = '';
          for (let attempt = 0; attempt < 3; attempt++) {
            try {
              await onRefreshAuthoritative({
                workId,
                inputId: '',
                requestId: `${applyIntent.requestId}/refresh`,
                revision: result.newRevision,
                operation: 'patch',
              });
              refreshFailure = '';
              break;
            } catch (error) {
              refreshFailure = error instanceof Error ? error.message : String(error);
            }
          }
          if (epoch !== discussionEpochRef.current || activeDiscussionTargetRef.current !== expectedTarget) return;
          if (!refreshFailure) {
            handleDClose();
            return;
          }
          setApplyError(`修改已经提交，但最新状态暂时未同步：${refreshFailure}`);
          return;
        }
        const conflict = `${result?.transportError?.code ?? ''} ${applyFailure}`.toLowerCase();
        if (
          round === 0
          && (conflict.includes('revision') || conflict.includes('digest') || conflict.includes('expired'))
        ) {
          try {
            await onRefreshAuthoritative({
              workId,
              inputId: '',
              requestId: `${currentIntent.requestId}/rebase`,
              revision: useWorkStore.getState().revisions[workId] ?? applyIntent.expectedRevision,
              operation: 'patch',
            });
          } catch {
            // The next preview still performs authoritative validation.
          }
          const state = useWorkStore.getState();
          const latestDefinition = selectV2ActiveDefinition(
            state.v2ActiveDefinitions,
            state.v2Definitions,
            workId,
          );
          const latestBlock = state.works[workId]?.work.blocks.find(
            (block) => block.id === currentIntent.blockId,
          );
          currentIntent = {
            ...currentIntent,
            definitionRevision: latestDefinition?.revision ?? currentIntent.definitionRevision,
            blockRevision: latestBlock?.revision ?? currentIntent.blockRevision,
            requestId: `${intent.requestId}/rebase`,
          };
          setIsApplying(false);
          setIsPreviewing(true);
          continue;
        }
        throw new Error(applyFailure || '这次调整暂时无法完成，请补充要求后再提交。');
      }
    } catch (e) {
      if (epoch !== discussionEpochRef.current || activeDiscussionTargetRef.current !== expectedTarget) return;
      setApplyError(e instanceof Error ? e.message : String(e));
    } finally {
      if (epoch === discussionEpochRef.current) {
        setIsPreviewing(false);
        setIsApplying(false);
      }
    }
  }, [
    discKey, discTarget, handleDClose, onApplyPatch, onPreviewPatch,
    onRefreshAuthoritative, setCommittedRequestId, workId, workRevision,
  ]);
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
                  onInfo={onTaskInfo && taskInfoTaskKeys?.has(`${task.runId}\u0000${task.id}`)
                    ? () => onTaskInfo(task.runId, task.id)
                    : undefined}
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
          onApply={() => {}} onDismissResult={handleDismiss}
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
