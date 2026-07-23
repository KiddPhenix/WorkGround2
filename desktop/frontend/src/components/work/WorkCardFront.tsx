import React, { useMemo } from 'react';

import type {
  BlockPlacement,
  BlockUpdateRequest,
  Conclusion,
  RetryIntent,
  RetryStatus,
  ResumeRunInput,
  RunSelection,
  Work,
  WorkflowRun,
  WorkView,
} from '../../work/types';
import { BlockHost } from './blocks/BlockHost';
import type { BlockActionHandler, BlockHostContext } from './blocks/types';
import { RunProgressIndicator } from './RunProgressIndicator';
import { WorkRunEntry } from './WorkRunEntry';

export interface WorkCardFrontProps {
  view: WorkView;
  expanded: Record<string, boolean>;
  onToggleExpand: (targetID: string) => void;
  onAction?: BlockActionHandler;
  onUpdate?: (request: BlockUpdateRequest) => void | Promise<void>;
  readonly: boolean;
  archived: boolean;
  runSelection?: RunSelection;
  onRunSelect: (selection: RunSelection) => void;
  onRetry?: (intent: RetryIntent) => void;
  retryByTarget: Record<string, RetryStatus>;
  onRun?: (input: { workId: string; requestId: string }) => WorkflowRun | Promise<WorkflowRun>;
  onResumeRun?: (input: ResumeRunInput) => WorkflowRun | Promise<WorkflowRun>;
  onRecoverProjection?: () => void | Promise<void>;
}

function latestRun(runs: WorkflowRun[]): WorkflowRun | undefined {
  return runs.length > 0 ? runs[runs.length - 1] : undefined;
}

function runStateLabel(state: string): string {
  switch (state) {
    case 'running': return '运行中';
    case 'completed': return '已完成';
    case 'failed': return '失败';
    case 'cancelled': return '已取消';
    default: return state;
  }
}

const AttentionBadge: React.FC<{ view: WorkView }> = ({ view }) => {
  const blocked = Boolean(view.runBlock?.blocked || view.assessment?.blocking);
  const degraded = view.assessment?.degraded ?? false;
  if (!blocked && !degraded) return null;
  const count = Math.max(view.runBlock?.items?.length ?? 0, view.assessment?.issues?.length ?? 0, 1);
  return (
    <div className="wg2-work-attention" role="alert" aria-live="polite" data-testid="work-attention">
      <span className="wg2-work-attention-count">{blocked ? count : '⚠'}</span>
      <span className="wg2-work-attention-label">{blocked ? '个阻断阻止运行' : '降级可用'}</span>
    </div>
  );
};

const WorkflowSummary: React.FC<{ work: Work }> = ({ work }) => {
  const run = latestRun(work.runs);
  if (!run) return null;
  return (
    <div className="wg2-work-workflow-summary" data-testid="work-workflow-summary">
      <span className="wg2-work-workflow-state" data-run-state={run.state}>{runStateLabel(run.state)}</span>
      <span className="wg2-work-workflow-stages">{run.stages.length} 个阶段</span>
    </div>
  );
};

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

export const WorkCardFront: React.FC<WorkCardFrontProps> = ({
  view,
  expanded,
  onToggleExpand,
  onAction,
  onUpdate,
  readonly,
  archived,
  runSelection,
  onRunSelect,
  onRetry,
  retryByTarget,
  onRun,
  onResumeRun,
  onRecoverProjection,
}) => {
  const { work } = view;
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
  const orderedBlocks = useMemo(() => work.blocks
    .filter((block) => !block.tombstone)
    .map((block, index) => ({ block, index, placement: placementByBlock.get(block.id) }))
    .sort((left, right) => (left.placement?.order ?? Number.MAX_SAFE_INTEGER) -
      (right.placement?.order ?? Number.MAX_SAFE_INTEGER) || left.index - right.index),
  [placementByBlock, work.blocks]);

  const renderBlock = ({ block, placement }: { block: Work['blocks'][number]; placement?: BlockPlacement }) => {
    const canCollapse = placement?.collapsed !== undefined;
    const isExpanded = expanded[block.id] ?? !placement?.collapsed;
    return (
      <div
        key={block.id}
        className={`wg2-work-block-host-wrapper${isExpanded ? ' wg2-work-block-expanded' : ' wg2-work-block-collapsed'}`}
        role="listitem"
        data-block-id={block.id}
        data-work-target-id={block.id}
        data-testid={`work-block-${block.id}`}
        tabIndex={-1}
      >
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

  return (
    <div
      className="wg2-work-card-front"
      data-testid="work-card-front"
      data-work-id={work.id}
      data-readonly={readonly ? 'true' : 'false'}
      data-archived={archived ? 'true' : 'false'}
    >
      <div className="wg2-work-front-header">
        <h2 className="wg2-work-name">{work.name}</h2>
        <WorkflowSummary work={work} />
        <AttentionBadge view={view} />
        <WorkRunEntry
          workId={work.id}
          onRun={onRun}
          onResumeRun={onResumeRun}
          onRecoverProjection={onRecoverProjection}
          disabled={readonly || archived}
        />
      </div>
      <ConclusionList conclusions={work.conclusions ?? []} />
      <ArtifactSummary work={work} />
      <RunProgressIndicator
        work={work}
        selection={runSelection}
        onSelect={onRunSelect}
        onRetry={onRetry}
        retryByTarget={retryByTarget}
        readonly={readonly}
        archived={archived}
      />
      <div className="wg2-work-block-host-list" role="list">
        {orderedBlocks.map(renderBlock)}
        {orderedBlocks.length === 0 && (
          <div className="wg2-work-front-empty" data-testid="work-front-empty">
            <p>暂无工作流内容。在背面编辑提示词后运行即可生成。</p>
          </div>
        )}
      </div>
    </div>
  );
};
