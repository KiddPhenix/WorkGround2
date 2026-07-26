import React from 'react';

import type { WorkPatchPreview as WorkPatchPreviewType } from '../../../types_v2';

// ── Helpers ────────────────────────────────────────────────────────────────

function shortDigest(d: string): string {
  if (d.length <= 16) return d;
  return `${d.slice(0, 8)}…${d.slice(-8)}`;
}

function formatExpiry(iso: string): string {
  try {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toLocaleString();
  } catch {
    return iso;
  }
}

function isExpired(iso: string): boolean {
  try {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return false;
    return d.getTime() < Date.now();
  } catch {
    return false;
  }
}

function opLabel(op: string): string {
  const labels: Record<string, string> = {
    replace: '替换',
    add: '新增',
    remove: '删除',
    move: '移动',
    copy: '复制',
    test: '校验',
  };
  return labels[op] ?? op;
}

// ── PatchPreview ───────────────────────────────────────────────────────────

export interface PatchPreviewProps {
  patch: WorkPatchPreviewType;
  taskTitle: string;
  taskId: string;
}

export const PatchPreview: React.FC<PatchPreviewProps> = ({ patch, taskTitle, taskId }) => {
  const expired = isExpired(patch.expiresAt);

  return (
    <div
      className="wg2-pp-panel"
      data-testid={`patch-preview-${taskId}`}
      role="region"
      aria-label={`${taskTitle} 补丁预览`}
    >
      {/* ── Header ────────────────────────────────────────────────── */}
      <div className="wg2-pp-header" data-testid={`patch-preview-header-${taskId}`}>
        <span className="wg2-pp-title">补丁预览</span>
        <span
          className={`wg2-pp-badge ${patch.scope === 'workflow' ? 'wg2-pp-badge-workflow' : ''}`}
          data-testid={`patch-preview-scope-${taskId}`}
        >
          {patch.scope === 'block' ? '仅此 Block' : '整个工作流'}
        </span>
        {expired && (
          <span
            className="wg2-pp-badge wg2-pp-badge-expired"
            data-testid={`patch-preview-expired-${taskId}`}
          >
            已过期
          </span>
        )}
      </div>

      {/* ── Operations diff table ─────────────────────────────────── */}
      {patch.operations.length > 0 && (
        <div className="wg2-pp-ops-wrap" data-testid={`patch-preview-ops-${taskId}`}>
          <table className="wg2-pp-ops" aria-label="补丁操作列表">
            <thead>
              <tr>
                <th className="wg2-pp-th">操作</th>
                <th className="wg2-pp-th">路径</th>
                <th className="wg2-pp-th">旧值</th>
                <th className="wg2-pp-th">新值</th>
              </tr>
            </thead>
            <tbody>
              {patch.operations.map((op, i) => (
                <tr
                  key={`${op.op}-${op.path}-${i}`}
                  className="wg2-pp-row"
                  data-testid={`patch-preview-op-${taskId}-${i}`}
                >
                  <td className="wg2-pp-td wg2-pp-td-op">{opLabel(op.op)}</td>
                  <td className="wg2-pp-td wg2-pp-td-path">{op.path}</td>
                  <td className="wg2-pp-td wg2-pp-td-old">
                    {op.oldValue != null ? truncateValue(op.oldValue) : '—'}
                  </td>
                  <td className="wg2-pp-td wg2-pp-td-new">
                    {op.newValue != null ? truncateValue(op.newValue) : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* ── Empty operations ──────────────────────────────────────── */}
      {patch.operations.length === 0 && (
        <p className="wg2-pp-empty" data-testid={`patch-preview-empty-ops-${taskId}`}>
          无具体操作变更。
        </p>
      )}

      {/* ── Impact summary ────────────────────────────────────────── */}
      <div className="wg2-pp-impact" data-testid={`patch-preview-impact-${taskId}`}>
        {patch.affectedNodeIds.length > 0 && (
          <ImpactTag
            label="影响节点"
            kind="affected"
            ids={patch.affectedNodeIds}
            testId={`patch-affected-nodes-${taskId}`}
          />
        )}
        {patch.affectedBlockIds.length > 0 && (
          <ImpactTag
            label="影响 Block"
            kind="affected"
            ids={patch.affectedBlockIds}
            testId={`patch-affected-blocks-${taskId}`}
          />
        )}
        {patch.affectedArtifactSlotIds.length > 0 && (
          <ImpactTag
            label="影响成果"
            kind="affected"
            ids={patch.affectedArtifactSlotIds}
            testId={`patch-affected-slots-${taskId}`}
          />
        )}
        {patch.staleArtifactSlotIds.length > 0 && (
          <ImpactTag
            label="成果失效"
            kind="stale"
            ids={patch.staleArtifactSlotIds}
            testId={`patch-stale-slots-${taskId}`}
          />
        )}
        {patch.invalidatedTaskIds.length > 0 && (
          <ImpactTag
            label="任务失效"
            kind="invalidated"
            ids={patch.invalidatedTaskIds}
            testId={`patch-invalidated-tasks-${taskId}`}
          />
        )}
        {patch.requiresRerun && (
          <span
            className="wg2-pp-impact-rerun"
            data-testid={`patch-requires-rerun-${taskId}`}
          >
            需要重新执行
          </span>
        )}
      </div>

      {/* ── Meta footer ───────────────────────────────────────────── */}
      <div className="wg2-pp-meta" data-testid={`patch-preview-meta-${taskId}`}>
        <span className="wg2-pp-meta-item">
          Digest: <code>{shortDigest(patch.digest)}</code>
        </span>
        <span className="wg2-pp-meta-item">
          过期时间: {formatExpiry(patch.expiresAt)}
          {expired && <span className="wg2-pp-meta-warn">（已过期）</span>}
        </span>
        <span className="wg2-pp-meta-item">
          Base rev: {patch.baseDefinitionRev}
        </span>
      </div>
    </div>
  );
};

// ── ImpactTag sub-component ────────────────────────────────────────────────

interface ImpactTagProps {
  label: string;
  kind: 'affected' | 'stale' | 'invalidated';
  ids: string[];
  testId: string;
}

const ImpactTag: React.FC<ImpactTagProps> = ({ label, kind, ids, testId }) => (
  <div className={`wg2-pp-impact-group wg2-pp-impact-${kind}`} data-testid={testId}>
    <span className="wg2-pp-impact-label">{label}</span>
    <ul className="wg2-pp-impact-list" aria-label={`${label}列表`}>
      {ids.map((id) => (
        <li key={id} className="wg2-pp-impact-id">
          {id}
        </li>
      ))}
    </ul>
  </div>
);

// ── Helpers ────────────────────────────────────────────────────────────────

function truncateValue(v: unknown): string {
  const s = typeof v === 'string' ? v : (JSON.stringify(v) ?? String(v));
  if (s.length <= 60) return s;
  return `${s.slice(0, 57)}…`;
}
