import React from 'react';
import { FilePenLine, RefreshCw } from 'lucide-react';

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

function artifactSlotDetails(value: unknown): { title?: string; text?: string } {
  if (value == null || typeof value !== 'object' || Array.isArray(value)) return {};
  const slot = value as Record<string, unknown>;
  const title = typeof slot.title === 'string' ? slot.title.trim() : undefined;
  const kindLabels: Record<string, string> = {
    document: '文档',
    text: '文本',
    data: '数据',
    file: '文件',
  };
  const kind = typeof slot.kind === 'string' ? (kindLabels[slot.kind] ?? slot.kind) : '成果';
  const count = typeof slot.expectedCount === 'number' ? `${slot.expectedCount} 个` : '';
  const required = typeof slot.required === 'boolean' ? (slot.required ? '必须生成' : '可选') : '';
  return { title, text: [kind, count, required].filter(Boolean).join(' · ') };
}

// ── PatchPreview ───────────────────────────────────────────────────────────

export interface PatchPreviewProps {
  patch: WorkPatchPreviewType;
  taskTitle: string;
  taskId: string;
  resolveLabel?: PatchPreviewLabelResolver;
  resolveOrder?: PatchPreviewOrderResolver;
}

export type PatchPreviewEntity = 'node' | 'task' | 'slot' | 'block';
export type PatchPreviewLabelResolver = (kind: PatchPreviewEntity, id: string) => string | undefined;
export type PatchPreviewOrderResolver = (kind: PatchPreviewEntity, id: string) => number | undefined;

export const PatchPreview: React.FC<PatchPreviewProps> = ({
  patch,
  taskTitle,
  taskId,
  resolveLabel,
  resolveOrder,
}) => {
  const operations = Array.isArray(patch.operations) ? patch.operations : [];
  const affectedNodeIds = orderedIds(stringArray(patch.affectedNodeIds), 'node', resolveOrder);
  const affectedBlockIds = stringArray(patch.affectedBlockIds);
  const affectedArtifactSlotIds = orderedIds(stringArray(patch.affectedArtifactSlotIds), 'slot', resolveOrder);
  const staleArtifactSlotIds = orderedIds(stringArray(patch.staleArtifactSlotIds), 'slot', resolveOrder);
  const invalidatedTaskIds = orderedIds(stringArray(patch.invalidatedTaskIds), 'task', resolveOrder);
  const expiresAt = typeof patch.expiresAt === 'string' ? patch.expiresAt : '';
  const digest = typeof patch.digest === 'string' ? patch.digest : '';
  const expired = isExpired(expiresAt);
  const artifactIds = staleArtifactSlotIds.length > 0
    ? staleArtifactSlotIds
    : affectedArtifactSlotIds;
  const labelFor = (kind: PatchPreviewEntity, id: string) => {
    const resolved = resolveLabel?.(kind, id)?.trim();
    if (resolved) return resolved;
    if (kind === 'slot') {
      const slotOp = operations.find((op) => op.path === `artifactSlots/${id}`);
      const details = artifactSlotDetails(slotOp?.newValue ?? slotOp?.oldValue);
      if (details.title) return details.title;
    }
    return humanizeId(id);
  };

  return (
    <div
      className="wg2-pp-panel"
      data-testid={`patch-preview-${taskId}`}
      role="region"
      aria-label={`${taskTitle} 修改影响预览`}
    >
      {/* ── Header ────────────────────────────────────────────────── */}
      <div className="wg2-pp-header" data-testid={`patch-preview-header-${taskId}`}>
        <span className="wg2-pp-title">修改确认</span>
        <span
          className={`wg2-pp-badge ${patch.scope === 'workflow' ? 'wg2-pp-badge-workflow' : ''}`}
          data-testid={`patch-preview-scope-${taskId}`}
        >
          {patch.scope === 'block' ? '只更新当前内容' : '会更新当前任务和后续步骤'}
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

      <div className="wg2-pp-intro">
        <FilePenLine size={18} aria-hidden="true" />
        <p>
          这次会更新<strong>「{taskTitle}」</strong>。
          {patch.scope === 'workflow' && '确认后，后续步骤会使用新的要求继续处理。'}
        </p>
      </div>

      {operations.length > 0 && (
        <div className="wg2-pp-changes" aria-label="修改内容">
          {operations.map((op, i) => {
            const change = describePath(op.path, taskTitle, labelFor);
            const slotDetails = op.path.split('/').length === 2 && op.path.startsWith('artifactSlots/')
              ? artifactSlotDetails(op.newValue ?? op.oldValue)
              : {};
            return (
              <section
                key={`${op.op}-${op.path}-${i}`}
                className="wg2-pp-change"
                data-testid={`patch-preview-change-${taskId}-${i}`}
              >
                <h4>{slotDetails.title ? `成果「${slotDetails.title}」` : change.subject}<span>{change.field}</span></h4>
                <div className="wg2-pp-change-grid">
                  <div className="wg2-pp-before">
                    <span>修改前</span>
                    <p>{op.op === 'add'
                      ? '尚未添加'
                      : op.oldValue != null
                        ? (slotDetails.text || displayValue(op.oldValue))
                        : '暂无内容'}</p>
                  </div>
                  <div className="wg2-pp-after">
                    <span>修改后</span>
                    <p>{op.op === 'remove'
                      ? '将从流程中移除'
                      : op.newValue != null
                        ? (slotDetails.text || displayValue(op.newValue))
                        : '删除这项内容'}</p>
                  </div>
                </div>
              </section>
            );
          })}
        </div>
      )}

      {operations.length === 0 && (
        <p className="wg2-pp-empty" data-testid={`patch-preview-empty-ops-${taskId}`}>
          没有检测到需要修改的内容。
        </p>
      )}

      <div className="wg2-pp-impact" data-testid={`patch-preview-impact-${taskId}`}>
        {invalidatedTaskIds.length > 0 && (
          <ImpactTag
            label={`将自动重做 ${invalidatedTaskIds.length} 个步骤`}
            kind="invalidated"
            ids={invalidatedTaskIds}
            resolve={(id) => labelFor('task', id)}
            testId={`patch-invalidated-tasks-${taskId}`}
          />
        )}
        {invalidatedTaskIds.length === 0 && affectedNodeIds.length > 0 && (
          <ImpactTag
            label="会更新这些步骤"
            kind="affected"
            ids={affectedNodeIds}
            resolve={(id) => labelFor('node', id)}
            testId={`patch-affected-nodes-${taskId}`}
          />
        )}
        {artifactIds.length > 0 && (
          <ImpactTag
            label={staleArtifactSlotIds.length > 0 ? '完成后会重新生成这些成果' : '完成后会更新这些成果'}
            kind={staleArtifactSlotIds.length > 0 ? 'stale' : 'affected'}
            ids={artifactIds}
            resolve={(id) => labelFor('slot', id)}
            testId={staleArtifactSlotIds.length > 0
              ? `patch-stale-slots-${taskId}`
              : `patch-affected-slots-${taskId}`}
          />
        )}
        {patch.requiresRerun && (
          <span
            className="wg2-pp-impact-rerun"
            data-testid={`patch-requires-rerun-${taskId}`}
          >
            <RefreshCw size={13} aria-hidden="true" />
            AI 会自动完成，无需手动重跑
          </span>
        )}
      </div>

      <details className="wg2-pp-technical" data-testid={`patch-preview-technical-${taskId}`}>
        <summary>查看技术信息</summary>
        {operations.length > 0 && (
          <div className="wg2-pp-ops-wrap" data-testid={`patch-preview-ops-${taskId}`}>
            <table className="wg2-pp-ops" aria-label="技术改动列表">
              <thead>
                <tr>
                  <th className="wg2-pp-th">操作</th>
                  <th className="wg2-pp-th">内部路径</th>
                  <th className="wg2-pp-th">修改前</th>
                  <th className="wg2-pp-th">修改后</th>
                </tr>
              </thead>
              <tbody>
                {operations.map((op, i) => (
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
        <div className="wg2-pp-meta" data-testid={`patch-preview-meta-${taskId}`}>
          <span className="wg2-pp-meta-item">校验码 <code>{shortDigest(digest)}</code></span>
          <span className="wg2-pp-meta-item">
            有效期至 {formatExpiry(expiresAt)}
            {expired && <span className="wg2-pp-meta-warn">（已过期）</span>}
          </span>
          <span className="wg2-pp-meta-item">基于版本 {patch.baseDefinitionRev}</span>
          <span className="wg2-pp-meta-item">
            内部影响：{[...new Set([
              ...affectedNodeIds,
              ...affectedBlockIds,
              ...affectedArtifactSlotIds,
              ...staleArtifactSlotIds,
              ...invalidatedTaskIds,
            ])].join('、') || '无'}
          </span>
        </div>
      </details>
    </div>
  );
};

// ── ImpactTag sub-component ────────────────────────────────────────────────

interface ImpactTagProps {
  label: string;
  kind: 'affected' | 'stale' | 'invalidated';
  ids: string[];
  resolve: (id: string) => string;
  testId: string;
}

const ImpactTag: React.FC<ImpactTagProps> = ({ label, kind, ids, resolve, testId }) => (
  <div className={`wg2-pp-impact-group wg2-pp-impact-${kind}`} data-testid={testId}>
    <span className="wg2-pp-impact-label">{label}</span>
    <ul className="wg2-pp-impact-list" aria-label={`${label}列表`}>
      {ids.map((id) => (
        <li key={id} className="wg2-pp-impact-id">
          {resolve(id)}
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

function displayValue(value: unknown): string {
  const text = typeof value === 'string' ? value : (JSON.stringify(value, null, 2) ?? String(value));
  if (text.length <= 600) return text;
  return `${text.slice(0, 597)}…`;
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];
}

function humanizeId(id: string): string {
  return id.replace(/[_-]+/g, ' ').replace(/\s+/g, ' ').trim() || id;
}

function orderedIds(
  ids: string[],
  kind: PatchPreviewEntity,
  resolveOrder: PatchPreviewOrderResolver | undefined,
): string[] {
  if (!resolveOrder || ids.length < 2) return ids;
  return [...ids].sort((left, right) => {
    const leftOrder = resolveOrder(kind, left);
    const rightOrder = resolveOrder(kind, right);
    if (leftOrder == null && rightOrder == null) return 0;
    if (leftOrder == null) return 1;
    if (rightOrder == null) return -1;
    return leftOrder - rightOrder;
  });
}

function describePath(
  path: string,
  fallbackTaskTitle: string,
  resolve: (kind: PatchPreviewEntity, id: string) => string,
): { subject: string; field: string } {
  const parts = path.split('/');
  const fieldLabels: Record<string, string> = {
    title: '名称',
    description: '任务说明',
    dependsOn: '执行顺序',
    inputSpecIds: '所需输入',
    toolHints: '使用方式',
    blockIds: '关联内容',
    producesSlotIds: '产出成果',
    consumesSlotIds: '使用成果',
    kind: '类型',
    expectedCount: '数量',
    required: '是否必需',
    label: '显示名称',
    valueSchema: '输入格式',
    defaultValue: '默认内容',
    pinEligible: '是否可固定',
    data: '内容',
  };
  if (parts[0] === 'nodes' && parts[1]) {
    return { subject: resolve('node', parts[1]) || fallbackTaskTitle, field: fieldLabels[parts[2]] ?? '任务设置' };
  }
  if ((parts[0] === 'slots' || parts[0] === 'artifactSlots') && parts[1]) {
    return { subject: resolve('slot', parts[1]), field: fieldLabels[parts[2]] ?? '成果设置' };
  }
  if ((parts[0] === 'specs' || parts[0] === 'inputSpecs') && parts[1]) {
    return { subject: humanizeId(parts[1]), field: fieldLabels[parts[2]] ?? '输入设置' };
  }
  if (parts[0] === 'blocks' && parts[1]) {
    return { subject: resolve('block', parts[1]), field: fieldLabels[parts[2]] ?? '当前内容' };
  }
  if (path === 'root/goal') {
    return { subject: '这项工作', field: '工作目标' };
  }
  return { subject: fallbackTaskTitle, field: '任务设置' };
}
