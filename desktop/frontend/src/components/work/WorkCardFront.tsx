import React, { useCallback, useMemo, useState } from 'react';
import {
  CheckCircle2,
  ChevronDown,
  CircleDashed,
  ListChecks,
  PauseCircle,
  Sparkles,
} from 'lucide-react';

import type {
  BlockPlacement,
  BlockUpdateRequest,
  Conclusion,
  ResumeRunInput,
  Work,
  WorkflowRun,
  WorkView,
} from '../../work/types';
import type {
  ArtifactSlot,
  TaskV2View,
  WorkDefinitionRevision,
  WorkInput,
  SubmitWorkInputRequest,
  SetInputCornerstoneRequest,
  SubmitInputResult,
  CornerstonePinResult,
  PreviewWorkPatchResult,
  ApplyWorkPatchResult,
  SelectWorkInputFileRequest,
  SelectWorkInputFileResult,
  SelectWorkInformationFileRequest,
  AddCustomWorkInputRequest,
  InferWorkInputsRequest,
  InferWorkInputsResult,
  SetNodeSkillRequest,
  SetNodeSkillResult,
  ClearNodeSkillRequest,
  ClearNodeSkillResult,
  SkillInfo,
  CreateSkillRequest,
  CreateSkillResult,
} from '../../work/types_v2';
import { BlockHost } from './blocks/BlockHost';
import type { BlockActionHandler, BlockHostContext } from './blocks/types';
import { WorkControlBar } from './WorkControlBar';
import { WorkChatInput } from './WorkChatInput';
import { ResultShelf, ExecutionList } from '../../work/components/v2';
import { WorkInformationPanel } from '../../work/components/presentation';
import { isAutomaticV2DiscussionBlock } from '../../work/components/v2/discussionBlock';
import {
  deriveWorkPresentation,
  type WorkPresentation,
} from '../../work/presentation';
import type { ResultWorkflowChangeRequest, WorkflowChangeState } from '../../work/components/v2/ResultShelf';
import type {
  FileDownloadIntent,
  FileLocateIntent,
  FileOpenIntent,
  SlotRetryIntent,
  FilePreviewIntent,
  FileConversionIntent,
} from '../../work/components/v2/ResultCard';
import type { TaskRetryIntent as V2TaskRetryIntent } from '../../work/components/v2/ExecutionRow';
import type {
  WorkInputRefreshContext,
} from '../../work/components/v2/ExpandedBlock';
import type {
  DiscussionPreviewIntent,
  DiscussionApplyIntent,
  DiscussionDraftIntent,
} from '../../work/components/v2/discussion/DiscussionDrawer';
import type { ComposerSubmitKey } from '../../lib/composerKeyboard';
import type { Item, LiveStream } from '../../lib/useController';

export interface WorkCardFrontProps {
  view: WorkView;
  expanded: Record<string, boolean>;
  onToggleExpand: (targetID: string) => void;
  onExecutionExpand: (taskID: string, expanded: boolean) => void;
  onAction?: BlockActionHandler;
  onUpdate?: (request: BlockUpdateRequest) => void | Promise<void>;
  readonly: boolean;
  archived: boolean;
  /** V2 artifact slots from the store projection. */
  artifactSlots?: ArtifactSlot[];
  /** V2 definition when V2 planning has produced one. */
  v2Definition?: WorkDefinitionRevision;
  /** Current V2 task projections used to reconcile artifact presentation. */
  v2Tasks?: TaskV2View[];
  /** Current typed inputs used by the generic definition overview. */
  v2Inputs?: WorkInput[];
  onV2TaskRetry?: (intent: V2TaskRetryIntent) => void | Promise<void>;
  onArtifactOpen?: (intent: FileOpenIntent) => void | Promise<void>;
  /** Called when the user wants to open a URL artifact in the Desktop browser. */
  onArtifactOpenURL?: (intent: FileOpenIntent) => void | Promise<void>;
  onArtifactDownload?: (intent: FileDownloadIntent) => void | Promise<void>;
  onArtifactLocate?: (intent: FileLocateIntent) => void | Promise<void>;
  onArtifactRetry?: (intent: SlotRetryIntent) => void | Promise<void>;
  /** Called when user wants to preview an artifact file in-app. */
  onArtifactPreview?: (intent: FilePreviewIntent) => Promise<import('../../work/types_v2').ArtifactPreview>;
  onArtifactConvert?: (intent: FileConversionIntent) => Promise<import('../../work/types_v2').ArtifactPreview>;
  /** V2 run ID for input identity. */
  runId?: string;
  /** Session ID for discussion context. */
  sessionId?: string;
  // ── V2 typed input callbacks ──────────────────────────────────
  onSubmitWorkInput?: (req: SubmitWorkInputRequest) => Promise<SubmitInputResult>;
  onSetCornerstone?: (req: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  onUnsetCornerstone?: (req: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  onRefreshAuthoritative?: (context: WorkInputRefreshContext) => Promise<void>;
  onSelectWorkInputFile?: (request: SelectWorkInputFileRequest) => Promise<SelectWorkInputFileResult>;
  onSelectWorkInformationFile?: (request: SelectWorkInformationFileRequest) => Promise<SelectWorkInputFileResult>;
  onAddCustomWorkInput?: (request: AddCustomWorkInputRequest) => Promise<SubmitInputResult>;
  onInferWorkInputs?: (request: InferWorkInputsRequest) => Promise<InferWorkInputsResult>;
  // ── Optional Skill binding callbacks ─────────────────────────
  onSetNodeSkill?: (request: SetNodeSkillRequest) => Promise<SetNodeSkillResult>;
  onClearNodeSkill?: (request: ClearNodeSkillRequest) => Promise<ClearNodeSkillResult>;
  onListWorkSkills?: () => Promise<SkillInfo[]>;
  onCreateWorkSkill?: (request: CreateSkillRequest) => Promise<CreateSkillResult>;
  // ── Discussion callbacks ──────────────────────────────────────
  onPreviewPatch?: (intent: DiscussionPreviewIntent) => Promise<PreviewWorkPatchResult>;
  onApplyPatch?: (intent: DiscussionApplyIntent) => Promise<ApplyWorkPatchResult>;
  onDiscussionDraftChange?: (intent: DiscussionDraftIntent) => void;
  /** Called when user clicks info on a V2 task row with a session ref. */
  onTaskInfo?: (runId: string, taskId: string) => void;
  /** Run+Task identities that have session refs — only these rows show the info action. */
  taskInfoTaskKeys?: Set<string>;
  // ── Control bar callbacks ─────────────────────────────────────────
  onControlStart?: (input: { workId: string; requestId: string }) => import('../../work/types').WorkflowRun | Promise<import('../../work/types').WorkflowRun>;
  onControlResume?: (input: ResumeRunInput) => import('../../work/types').WorkflowRun | Promise<import('../../work/types').WorkflowRun>;
  onControlPause?: (input: { workId: string; runId: string; requestId: string }) => Promise<void>;
  onControlStop?: (input: { workId: string; runId: string; requestId: string }) => Promise<void>;
  onControlRestart?: (input: { workId: string; runId: string; requestId: string }) => import('../../work/types').WorkflowRun | Promise<import('../../work/types').WorkflowRun>;
  onControlRestartRequest?: (run: WorkflowRun) => void;
  // ── Chat input props ───────────────────────────────────────────
  displayItems: Item[];
  live?: LiveStream;
  running: boolean;
  chatDisabled: boolean;
  composerSubmitKey: ComposerSubmitKey;
  onChatSend: (text: string) => void | Promise<void>;
}

function latestRun(runs: WorkflowRun[]): WorkflowRun | undefined {
  return runs.length > 0 ? runs[runs.length - 1] : undefined;
}

const BLOCK_SLOTS: readonly BlockPlacement['slot'][] = ['attention', 'primary', 'secondary', 'result'];
const BLOCK_SLOT_LABELS: Record<BlockPlacement['slot'], string> = {
  attention: '需要关注',
  primary: '主要内容',
  secondary: '辅助内容',
  result: '结果',
};

function blockSpan(placement?: BlockPlacement): number {
  const value = placement?.span ?? 0;
  return value > 0 ? Math.min(value, 12) : 12;
}

function blockSlot(placement?: BlockPlacement): BlockPlacement['slot'] {
  const slot = placement?.slot;
  return slot && BLOCK_SLOTS.includes(slot) ? slot : 'primary';
}

const ConclusionList: React.FC<{ conclusions: Conclusion[] }> = ({ conclusions }) => {
  if (conclusions.length === 0) return null;
  return (
    <div className="wg2-work-conclusions" data-testid="work-conclusions">
      <h4 className="wg2-work-section-title">结论</h4>
      <ul className="wg2-work-conclusion-list">
        {conclusions.map((conclusion) => (
          <li
            key={conclusion.id}
            className="wg2-work-conclusion-item"
            data-work-target-id={conclusion.id}
            tabIndex={-1}
          >
            <span className="wg2-work-conclusion-kind">{conclusion.kind}</span>
            {conclusion.summary && <span className="wg2-work-conclusion-summary">{conclusion.summary}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
};

const ArtifactSummary: React.FC<{ work: Work }> = ({ work }) => {
  const artifactRefs = work.conclusions?.flatMap((conclusion) => conclusion.artifacts ?? []) ?? [];
  if (artifactRefs.length === 0) return null;
  return (
    <div className="wg2-work-artifacts" data-testid="work-artifacts">
      <h4 className="wg2-work-section-title">产出</h4>
      <ul className="wg2-work-artifact-list">
        {artifactRefs.map((ref, index) => (
          <li key={ref.id ?? `artifact-${index}`} className="wg2-work-artifact-item">
            {ref.name ?? ref.id ?? `产出 ${index + 1}`}
          </li>
        ))}
      </ul>
    </div>
  );
};

interface WorkStructureSummaryProps {
  presentation: WorkPresentation;
  expanded: boolean;
  onToggle: () => void;
}

const WorkStructureSummary: React.FC<WorkStructureSummaryProps> = ({
  presentation,
  expanded,
  onToggle,
}) => {
  const completed = presentation.tasks.filter((task) => task.state === 'completed').length;
  const running = presentation.tasks.filter(
    (task) => task.state === 'running' || task.state === 'ready',
  ).length;
  const waiting = presentation.tasks.filter(
    (task) => task.state === 'waiting_input' || task.state === 'waiting_approval',
  ).length;
  const failed = presentation.tasks.filter(
    (task) => task.state === 'failed_retryable' || task.state === 'failed_terminal',
  ).length;
  const Icon = presentation.phase === 'completed' ? CheckCircle2
    : presentation.phase === 'paused' ? PauseCircle
    : presentation.phase === 'planning' ? CircleDashed
      : ListChecks;
  const detail = presentation.phase === 'completed'
    ? `${completed}/${presentation.tasks.length} 已完成`
    : presentation.phase === 'paused'
      ? running > 0 ? `${running} 项已暂停` : '已暂停'
    : [
        running > 0 ? `${running} 运行中` : '',
        waiting > 0 ? `${waiting} 等待处理` : '',
        failed > 0 ? `${failed} 需要处理` : '',
        completed > 0 ? `${completed} 已完成` : '',
      ].filter(Boolean).join(' · ') || `${presentation.tasks.length} 项待执行`;

  return (
    <button
      type="button"
      className="wg2-work-execution-summary"
      data-phase={presentation.phase}
      data-testid="work-execution-summary"
      aria-expanded={expanded}
      onClick={onToggle}
    >
      <span className="wg2-work-execution-summary__icon">
        <Icon aria-hidden="true" size={18} strokeWidth={1.8} />
      </span>
      <strong>工作结构</strong>
      <span className="wg2-work-execution-summary__detail">{detail}</span>
      <span className="wg2-work-execution-summary__action">
        {expanded ? '收起详情' : '展开详情'}
        <ChevronDown aria-hidden="true" size={17} strokeWidth={1.8} />
      </span>
    </button>
  );
};

export const WorkCardFront: React.FC<WorkCardFrontProps> = ({
  view,
  expanded,
  onToggleExpand,
  onExecutionExpand,
  onAction,
  onUpdate,
  readonly,
  archived,
  artifactSlots,
  v2Definition,
  v2Tasks,
  v2Inputs,
  onV2TaskRetry,
  onArtifactOpen,
  onArtifactOpenURL,
  onArtifactDownload,
  onArtifactLocate,
  onArtifactRetry,
  onArtifactPreview,
  onArtifactConvert,
  runId,
  sessionId,
  onSubmitWorkInput,
  onSetCornerstone,
  onUnsetCornerstone,
  onRefreshAuthoritative,
  onSelectWorkInputFile,
  onSelectWorkInformationFile,
  onAddCustomWorkInput,
  onInferWorkInputs,
  onSetNodeSkill,
  onClearNodeSkill,
  onListWorkSkills,
  onCreateWorkSkill,
  onPreviewPatch,
  onApplyPatch,
  onDiscussionDraftChange,
  onTaskInfo,
  taskInfoTaskKeys,
  onControlStart,
  onControlResume,
  onControlPause,
  onControlStop,
  onControlRestart,
  onControlRestartRequest,
  displayItems,
  live,
  running,
  chatDisabled,
  composerSubmitKey,
  onChatSend,
}) => {
  const { work } = view;
  const [resultWorkflowChange, setResultWorkflowChange] = useState<ResultWorkflowChangeRequest>();
  const [workflowChangeState, setWorkflowChangeState] = useState<WorkflowChangeState | null>(null);
  const [executionExpanded, setExecutionExpanded] = useState(true);
  const canChangeWorkflow = Boolean(onPreviewPatch && onApplyPatch && onRefreshAuthoritative);
  const requestWorkflowChange = useCallback((request: ResultWorkflowChangeRequest) => {
    setWorkflowChangeState({ token: request.token, status: 'updating' });
    setResultWorkflowChange({ ...request });
  }, []);
  const isV2 = v2Definition !== undefined && v2Definition.status === 'active';
  const presentation = useMemo(() => {
    if (!v2Definition || v2Definition.status !== 'active') return undefined;
    return deriveWorkPresentation(
      v2Definition,
      v2Tasks ?? [],
      artifactSlots ?? [],
      { activeRunId: runId, workState: work.state },
    );
  }, [artifactSlots, runId, v2Definition, v2Tasks, work.state]);
  const isAutomaticNodeSummary = useCallback((block: Work['blocks'][number]) => {
    return isAutomaticV2DiscussionBlock(block);
  }, []);
  const presentationBlocks = useMemo(
    () => work.blocks.filter((block) => !block.tombstone && !isAutomaticNodeSummary(block)),
    [isAutomaticNodeSummary, work.blocks],
  );
  const hasPresentationCanvas = presentationBlocks.length > 0;
  const expandedTaskId = Object.entries(expanded)
    .find(([targetID, open]) => open && targetID.startsWith('v2-task:'))
    ?.[0].slice('v2-task:'.length);
  const executionDetailsOpen = executionExpanded || expandedTaskId !== undefined;
  const hostContext = useMemo<BlockHostContext>(() => ({
    workId: work.id,
    workSchemaVersion: work.schemaVersion,
    workRevision: view.revision,
    runId: latestRun(work.runs)?.id,
    actionReceipts: work.actionReceipts,
  }), [view.revision, work.actionReceipts, work.id, work.runs, work.schemaVersion]);

  const placementByBlock = useMemo(
    () => new Map(work.placements.map((placement) => [placement.blockId, placement])),
    [work.placements],
  );
  const blocksBySlot = useMemo(() => {
    const grouped = new Map<BlockPlacement['slot'], Array<{
      block: Work['blocks'][number];
      index: number;
      placement?: BlockPlacement;
    }>>();
    for (const slot of BLOCK_SLOTS) grouped.set(slot, []);
    work.blocks
      .filter((block) => !block.tombstone)
      .forEach((block, index) => {
        const placement = placementByBlock.get(block.id);
        const slot = blockSlot(placement);
        grouped.get(slot)!.push({ block, index, placement });
      });
    for (const blocks of grouped.values()) {
      blocks.sort((left, right) => (left.placement?.order ?? Number.MAX_SAFE_INTEGER) -
        (right.placement?.order ?? Number.MAX_SAFE_INTEGER) || left.index - right.index);
    }
    return grouped;
  }, [placementByBlock, work.blocks]);
  const visibleBlockCount = useMemo(
    () => [...blocksBySlot.values()].reduce((count, blocks) => count + blocks.length, 0),
    [blocksBySlot],
  );

  const renderBlock = ({ block, placement }: { block: Work['blocks'][number]; placement?: BlockPlacement }) => {
    const canCollapse = placement?.collapsed !== undefined;
    const isExpanded = expanded[block.id] ?? !placement?.collapsed;
    return (
      <div
        key={block.id}
        className={`wg2-work-block-host-wrapper${isExpanded ? ' wg2-work-block-expanded' : ' wg2-work-block-collapsed'}`}
        role="listitem"
        data-block-id={block.id}
        data-block-slot={blockSlot(placement)}
        data-block-span={blockSpan(placement)}
        data-work-target-id={block.id}
        data-testid={`work-block-${block.id}`}
        tabIndex={-1}
        style={{ '--wg2-block-span': blockSpan(placement) } as React.CSSProperties}
      >
        {isV2 && block.title ? (
          <h3 className="wg2-work-block-title">{block.title}</h3>
        ) : null}
        <BlockHost
          block={block}
          placement={placement}
          readonly={readonly}
          archived={archived}
          context={hostContext}
          onAction={onAction}
          onUpdate={onUpdate}
        />
        {canCollapse && (
          <button
            type="button"
            className="wg2-work-block-expand-toggle"
            onClick={() => onToggleExpand(block.id)}
            aria-expanded={isExpanded}
            aria-label={isExpanded ? '收起' : '展开'}
          >
            {isExpanded ? '收起' : '展开'}
          </button>
        )}
      </div>
    );
  };

  const blockCanvas = (
    <div className="wg2-work-block-canvas" data-testid="work-block-canvas">
      {BLOCK_SLOTS.map((slot) => {
        const blocks = blocksBySlot.get(slot) ?? [];
        if (blocks.length === 0) return null;
        return (
          <section
            key={slot}
            className={`wg2-work-block-slot wg2-work-block-slot--${slot}`}
            data-block-slot-region={slot}
            aria-label={BLOCK_SLOT_LABELS[slot]}
          >
            <div className="wg2-work-block-host-list" role="list">
              {blocks.map(renderBlock)}
            </div>
          </section>
        );
      })}
      {visibleBlockCount === 0 && (
        <div className="wg2-work-front-empty" data-testid="work-front-empty">
          <p>暂无工作流内容。在背面编辑提示词后运行即可生成。</p>
        </div>
      )}
    </div>
  );

  const presentationBlockCanvas = (
    <div
      className="wg2-work-block-canvas wg2-work-block-canvas--presentation"
      data-testid="work-block-canvas"
      role="list"
      aria-label="工作内容"
    >
      {(presentation?.layoutMode === 'balanced'
        ? presentationBlocks
          .map((block, index) => ({
            block,
            index,
            placement: placementByBlock.get(block.id),
          }))
        : BLOCK_SLOTS.flatMap((slot) =>
          (blocksBySlot.get(slot) ?? []).filter(({ block }) => !isAutomaticNodeSummary(block)))
      ).map(renderBlock)}
    </div>
  );

  const executionList = (
    <ExecutionList
      workId={work.id}
      expandedTaskId={expandedTaskId}
      runId={runId}
      sessionId={sessionId}
      workRevision={view.revision}
      blocks={work.blocks}
      onExpandTask={(intent) => onExecutionExpand(intent.taskId, true)}
      onCollapseTask={(intent) => onExecutionExpand(intent.taskId, false)}
      onRetryTask={onV2TaskRetry}
      onSubmitWorkInput={onSubmitWorkInput}
      onSetCornerstone={onSetCornerstone}
      onUnsetCornerstone={onUnsetCornerstone}
      onRefreshAuthoritative={onRefreshAuthoritative}
      onSelectFile={onSelectWorkInputFile}
      onPreviewPatch={onPreviewPatch}
      onApplyPatch={onApplyPatch}
      onDiscussionDraftChange={onDiscussionDraftChange}
      externalWorkflowDiscussion={resultWorkflowChange}
      onWorkflowChangeState={setWorkflowChangeState}
      onTaskInfo={onTaskInfo}
      taskInfoTaskKeys={taskInfoTaskKeys}
      showTaskInputs={false}
      paused={work.state === 'paused'}
      nodeSkillBindings={work.v2NodeSkillBindings}
      onSetNodeSkill={onSetNodeSkill}
      onClearNodeSkill={onClearNodeSkill}
      onListWorkSkills={onListWorkSkills}
      onCreateWorkSkill={onCreateWorkSkill}
    />
  );

  if (isV2 && v2Definition && presentation) {
    return (
      <div
        className="wg2-work-card-front wg2-work-card-front--presentation"
        data-testid="work-card-front"
        data-work-id={work.id}
        data-readonly={readonly ? 'true' : 'false'}
        data-archived={archived ? 'true' : 'false'}
        data-work-state={work.state}
        data-presentation-phase={presentation.phase}
        data-presentation-layout={presentation.layoutMode}
      >
        <div className="wg2-work-presentation">
          <ResultShelf
            slots={artifactSlots ?? []}
            activeDefinitionRevision={v2Definition.revision}
            definition={v2Definition}
            tasks={v2Tasks}
            runId={presentation.runId ?? runId}
            readonly={readonly || archived}
            paused={work.state === 'paused'}
            onRequestWorkflowChange={canChangeWorkflow ? requestWorkflowChange : undefined}
            workflowChangeState={workflowChangeState}
            onOpen={onArtifactOpen}
            onOpenURL={onArtifactOpenURL}
            onDownload={onArtifactDownload}
            onLocate={onArtifactLocate}
            onRetry={onArtifactRetry}
            onPreview={onArtifactPreview}
            onConvert={onArtifactConvert}
          />

          <WorkInformationPanel
            workId={work.id}
            runId={presentation.runId ?? runId}
            workRevision={view.revision}
            definition={v2Definition}
            tasks={v2Tasks ?? []}
            inputs={v2Inputs ?? []}
            readonly={readonly || archived}
            onSubmit={onSubmitWorkInput}
            onPin={onSetCornerstone}
            onUnpin={onUnsetCornerstone}
            onRefresh={onRefreshAuthoritative}
            onSelectFile={onSelectWorkInputFile}
            onSelectCustomFile={onSelectWorkInformationFile}
            onAddCustom={onAddCustomWorkInput}
            onInfer={onInferWorkInputs}
          />

          {hasPresentationCanvas ? (
            <div className="wg2-work-presentation__canvas">
              {presentationBlockCanvas}
            </div>
          ) : null}

          <section
            className="wg2-work-execution"
            data-expanded={executionDetailsOpen ? 'true' : 'false'}
            data-testid="work-execution"
            aria-label="工作结构"
          >
            <WorkStructureSummary
              presentation={presentation}
              expanded={executionDetailsOpen}
              onToggle={() => {
                if (executionDetailsOpen) {
                  setExecutionExpanded(false);
                  if (expandedTaskId) onExecutionExpand(expandedTaskId, false);
                  return;
                }
                setExecutionExpanded(true);
              }}
            />
            <div
              className="wg2-work-execution__details"
              aria-hidden={!executionDetailsOpen}
              inert={!executionDetailsOpen || undefined}
            >
              {executionList}
            </div>
          </section>

          <footer className="wg2-work-goal" data-testid="work-goal">
            <Sparkles aria-hidden="true" size={18} strokeWidth={1.7} />
            <span>{v2Definition.goal}</span>
          </footer>
          <div className="wg2-work-bottom" data-testid="work-bottom">
            <WorkChatInput
              disabled={readonly || archived || chatDisabled}
              composerSubmitKey={composerSubmitKey}
              onSend={onChatSend}
              displayItems={displayItems}
              live={live}
              running={running}
            />
            <WorkControlBar
              workId={work.id}
              workState={work.state}
              runs={work.runs}
              readonly={readonly}
              archived={archived}
              onStart={onControlStart}
              onResume={onControlResume}
              onPause={onControlPause}
              onStop={onControlStop}
              onRestart={onControlRestart}
              onRestartRequest={onControlRestartRequest}
            />
          </div>
        </div>
      </div>
    );
  }

  return (
    <div
      className="wg2-work-card-front"
      data-testid="work-card-front"
      data-work-id={work.id}
      data-readonly={readonly ? 'true' : 'false'}
      data-archived={archived ? 'true' : 'false'}
    >
      <ConclusionList conclusions={work.conclusions ?? []} />
      <ArtifactSummary work={work} />
      {blockCanvas}
      <div className="wg2-work-bottom" data-testid="work-bottom">
        <WorkChatInput
          disabled={readonly || archived || chatDisabled}
          composerSubmitKey={composerSubmitKey}
          onSend={onChatSend}
          displayItems={displayItems}
          live={live}
          running={running}
        />
        <WorkControlBar
          workId={work.id}
          workState={work.state}
          runs={work.runs}
          readonly={readonly}
          archived={archived}
          onStart={onControlStart}
          onResume={onControlResume}
          onPause={onControlPause}
          onStop={onControlStop}
          onRestart={onControlRestart}
          onRestartRequest={onControlRestartRequest}
        />
      </div>
    </div>
  );
};
