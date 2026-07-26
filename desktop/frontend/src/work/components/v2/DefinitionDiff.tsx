import React, { useMemo } from 'react';

import type { WorkDefinitionRevision, RunImpact } from '../../types_v2';

// ── DefinitionDiff ────────────────────────────────────────────────────────

export interface DefinitionDiffProps {
  /** The currently active definition. */
  active: WorkDefinitionRevision;
  /** The candidate (draft) definition. */
  candidate: WorkDefinitionRevision;
  /** Optional RunImpact from a preview (if available from backend). */
  impact?: RunImpact;
  /** Called when user wants to apply the candidate. */
  onApply: () => void;
  /** Called when user cancels (rejects candidate). */
  onCancel: () => void;
  /** True while applying in progress. */
  isApplying?: boolean;
  /** Optional error. */
  error?: string | null;
  disabled?: boolean;
}

interface DiffSummary {
  nodesAdded: string[];
  nodesRemoved: string[];
  nodesChanged: string[];
  slotsAdded: string[];
  slotsRemoved: string[];
  slotsChanged: string[];
  specsAdded: string[];
  specsRemoved: string[];
  specsChanged: string[];
  goalChanged: boolean;
}

function canonicalJSON(value: unknown): string {
  const normalize = (item: unknown): unknown => {
    if (Array.isArray(item)) return item.map(normalize);
    if (item && typeof item === 'object') {
      return Object.fromEntries(
        Object.entries(item as Record<string, unknown>)
          .filter(([, child]) => child !== undefined)
          .sort(([left], [right]) => left.localeCompare(right))
          .map(([key, child]) => [key, normalize(child)]),
      );
    }
    return item;
  };
  return JSON.stringify(normalize(value));
}

function computeDiff(active: WorkDefinitionRevision, candidate: WorkDefinitionRevision): DiffSummary {
  const activeNodeIds = new Set(active.nodes.map((n) => n.id));
  const candidateNodeIds = new Set(candidate.nodes.map((n) => n.id));
  const activeNodes = new Map(active.nodes.map((node) => [node.id, node]));
  const activeSlotIds = new Set(active.artifactSlots.map((s) => s.id));
  const candidateSlotIds = new Set(candidate.artifactSlots.map((s) => s.id));
  const activeSlots = new Map(active.artifactSlots.map((slot) => [slot.id, slot]));
  const activeSpecIds = new Set(active.inputSpecs.map((s) => s.id));
  const candidateSpecIds = new Set(candidate.inputSpecs.map((s) => s.id));
  const activeSpecs = new Map(active.inputSpecs.map((spec) => [spec.id, spec]));

  return {
    nodesAdded: candidate.nodes.filter((n) => !activeNodeIds.has(n.id)).map((n) => n.id),
    nodesRemoved: active.nodes.filter((n) => !candidateNodeIds.has(n.id)).map((n) => n.id),
    nodesChanged: candidate.nodes
      .filter((node) => activeNodeIds.has(node.id) && canonicalJSON(activeNodes.get(node.id)) !== canonicalJSON(node))
      .map((node) => node.id),
    slotsAdded: candidate.artifactSlots.filter((s) => !activeSlotIds.has(s.id)).map((s) => s.id),
    slotsRemoved: active.artifactSlots.filter((s) => !candidateSlotIds.has(s.id)).map((s) => s.id),
    slotsChanged: candidate.artifactSlots
      .filter((slot) => activeSlotIds.has(slot.id) && canonicalJSON(activeSlots.get(slot.id)) !== canonicalJSON(slot))
      .map((slot) => slot.id),
    specsAdded: candidate.inputSpecs.filter((s) => !activeSpecIds.has(s.id)).map((s) => s.id),
    specsRemoved: active.inputSpecs.filter((s) => !candidateSpecIds.has(s.id)).map((s) => s.id),
    specsChanged: candidate.inputSpecs
      .filter((spec) => activeSpecIds.has(spec.id) && canonicalJSON(activeSpecs.get(spec.id)) !== canonicalJSON(spec))
      .map((spec) => spec.id),
    goalChanged: active.goal !== candidate.goal,
  };
}

export const DefinitionDiff: React.FC<DefinitionDiffProps> = ({
  active,
  candidate,
  impact,
  onApply,
  onCancel,
  isApplying,
  error,
  disabled,
}) => {
  const diff = useMemo(() => computeDiff(active, candidate), [active, candidate]);

  const hasChanges =
    diff.nodesAdded.length > 0
    || diff.nodesRemoved.length > 0
    || diff.slotsAdded.length > 0
    || diff.slotsRemoved.length > 0
    || diff.slotsChanged.length > 0
    || diff.specsAdded.length > 0
    || diff.specsRemoved.length > 0
    || diff.specsChanged.length > 0
    || diff.goalChanged;

  return (
    <div
      className="wg2-dd-diff-panel"
      data-testid="definition-diff"
      role="region"
      aria-label="定义变更预览"
    >
      <div className="wg2-dd-diff-header">
        <h3 className="wg2-dd-diff-title">工作结构调整预览</h3>
        <span className="wg2-dd-diff-revision">
          rev {active.revision} → rev {candidate.revision}
        </span>
        {impact?.requiresRerun && (
          <span className="wg2-dd-diff-badge wg2-dd-diff-badge-rerun" data-testid="definition-diff-rerun">
            需要重新执行
          </span>
        )}
      </div>

      {/* ── Empty diff — cancel is only option ───────────────────── */}
      {!hasChanges && !impact && (
        <div className="wg2-dd-diff-empty" data-testid="definition-diff-empty">
          <p>候选定义与当前定义无结构性差异。</p>
        </div>
      )}

      {/* ── Goal change ──────────────────────────────────────────── */}
      {diff.goalChanged && (
        <div className="wg2-dd-diff-goal" data-testid="definition-diff-goal">
          <span className="wg2-dd-diff-goal-label">目标变更</span>
          <p className="wg2-dd-diff-goal-prev">旧: {active.goal || '(空)'}</p>
          <p className="wg2-dd-diff-goal-curr">新: {candidate.goal || '(空)'}</p>
        </div>
      )}

      {/* ── Nodes ────────────────────────────────────────────────── */}
      {diff.nodesAdded.length > 0 && (
        <ChangeGroup label="新增节点" kind="added" ids={diff.nodesAdded} testId="definition-diff-nodes-added" />
      )}
      {diff.nodesRemoved.length > 0 && (
        <ChangeGroup label="移除节点" kind="removed" ids={diff.nodesRemoved} testId="definition-diff-nodes-removed" />
      )}
      {diff.nodesChanged.length > 0 && (
        <ChangeGroup label="修改节点" kind="changed" ids={diff.nodesChanged} testId="definition-diff-nodes-changed" />
      )}

      {/* ── Slots ────────────────────────────────────────────────── */}
      {diff.slotsAdded.length > 0 && (
        <ChangeGroup label="新增成果槽位" kind="added" ids={diff.slotsAdded} testId="definition-diff-slots-added" />
      )}
      {diff.slotsRemoved.length > 0 && (
        <ChangeGroup label="移除成果槽位" kind="removed" ids={diff.slotsRemoved} testId="definition-diff-slots-removed" />
      )}
      {diff.slotsChanged.length > 0 && (
        <ChangeGroup label="修改成果槽位" kind="changed" ids={diff.slotsChanged} testId="definition-diff-slots-changed" />
      )}

      {/* ── Input specs ──────────────────────────────────────────── */}
      {diff.specsAdded.length > 0 && (
        <ChangeGroup label="新增输入项" kind="added" ids={diff.specsAdded} testId="definition-diff-specs-added" />
      )}
      {diff.specsRemoved.length > 0 && (
        <ChangeGroup label="移除输入项" kind="removed" ids={diff.specsRemoved} testId="definition-diff-specs-removed" />
      )}
      {diff.specsChanged.length > 0 && (
        <ChangeGroup label="修改输入项" kind="changed" ids={diff.specsChanged} testId="definition-diff-specs-changed" />
      )}

      {/* ── RunImpact if available ───────────────────────────────── */}
      {impact && (
        <div className="wg2-dd-diff-impact" data-testid="definition-diff-impact">
          {impact.keptNodeIds.length > 0 && (
            <ChangeGroup label="保留任务" kind="kept" ids={impact.keptNodeIds} testId="definition-diff-kept" />
          )}
          {impact.invalidatedNodeIds.length > 0 && (
            <ChangeGroup label="需重新执行" kind="invalidated" ids={impact.invalidatedNodeIds} testId="definition-diff-invalidated" />
          )}
          {impact.newNodeIds.length > 0 && (
            <ChangeGroup label="新增任务" kind="new" ids={impact.newNodeIds} testId="definition-diff-new" />
          )}
          {impact.removedNodeIds.length > 0 && (
            <ChangeGroup label="移除任务" kind="removed" ids={impact.removedNodeIds} testId="definition-diff-removed" />
          )}
        </div>
      )}

      {/* ── Error ───────────────────────────────────────────────── */}
      {error && (
        <div className="wg2-dd-diff-error" role="alert" data-testid="definition-diff-error">
          <span className="wg2-dd-diff-error-icon" aria-hidden="true">⚠</span>
          <span>{error}</span>
        </div>
      )}

      {/* ── Actions ─────────────────────────────────────────────── */}
      <div className="wg2-dd-diff-actions" data-testid="definition-diff-actions">
        <button
          type="button"
          className="wg2-dd-diff-btn wg2-dd-diff-btn-cancel"
          onClick={onCancel}
          disabled={disabled || isApplying}
          data-testid="definition-diff-cancel"
        >
          取消（保持当前结构）
        </button>
        {hasChanges && (
          <button
            type="button"
            className="wg2-dd-diff-btn wg2-dd-diff-btn-apply"
            onClick={onApply}
            disabled={disabled || isApplying}
            aria-busy={isApplying ? 'true' : undefined}
            data-testid="definition-diff-apply"
          >
            {isApplying ? '应用中…' : '应用新结构'}
          </button>
        )}
      </div>
    </div>
  );
};

// ── ChangeGroup ───────────────────────────────────────────────────────────

interface ChangeGroupProps {
  label: string;
  kind: 'added' | 'removed' | 'changed' | 'kept' | 'invalidated' | 'new';
  ids: string[];
  testId: string;
}

const ChangeGroup: React.FC<ChangeGroupProps> = ({ label, kind, ids, testId }) => (
  <div className={`wg2-dd-diff-group wg2-dd-diff-group-${kind}`} data-testid={testId}>
    <span className="wg2-dd-diff-group-label">
      {label} ({ids.length})
    </span>
    <ul className="wg2-dd-diff-group-list" aria-label={`${label}列表`}>
      {ids.map((id) => (
        <li key={id} className="wg2-dd-diff-group-id">
          {id}
        </li>
      ))}
    </ul>
  </div>
);
