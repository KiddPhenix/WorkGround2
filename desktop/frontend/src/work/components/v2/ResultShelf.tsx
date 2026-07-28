import React, { useMemo, useState } from 'react';
import { Plus, Sparkles, Trash2, X } from 'lucide-react';

import type { ArtifactSlot, ArtifactPreview, ArtifactSlotDef, WorkDefinitionRevision } from '../../types_v2';
import { ResultCard } from './ResultCard';
import type { FileConversionIntent, FileDownloadIntent, FileLocateIntent, FileOpenIntent, SlotRetryIntent, FilePreviewIntent } from './ResultCard';

// ── ResultShelf ────────────────────────────────────────────────────────────

export interface ResultShelfProps {
  /** Artifact slots from the store projection. */
  slots: ArtifactSlot[];
  /** Only slots owned by this authoritative active Definition are visible. */
  activeDefinitionRevision: number;
  /** Active workflow definition used to explain producer/consumer changes. */
  definition?: WorkDefinitionRevision;
  readonly?: boolean;
  onRequestWorkflowChange?: (request: ResultWorkflowChangeRequest) => void;
  /** Called when the user wants to open a file. */
  onOpen?: (intent: FileOpenIntent) => void | Promise<void>;
  /** Called when the user wants to download a file. */
  onDownload?: (intent: FileDownloadIntent) => void | Promise<void>;
  /** Called when the user wants to locate a file on disk. */
  onLocate?: (intent: FileLocateIntent) => void | Promise<void>;
  /** Called when the user wants to retry a failed slot. */
  onRetry?: (intent: SlotRetryIntent) => void | Promise<void>;
  onPreview?: (intent: FilePreviewIntent) => Promise<ArtifactPreview>;
  onConvert?: (intent: FileConversionIntent) => Promise<ArtifactPreview>;
}

export interface ResultWorkflowChangeRequest {
  token: string;
  nodeId: string;
  title: string;
  instruction: string;
}

interface AddDraft {
  title: string;
  kind: string;
  expectedCount: number;
  required: boolean;
  producerNodeId: string;
}

function slotID(title: string, existing: Set<string>): string {
  const ascii = title.trim().toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');
  let hash = 2166136261;
  for (const ch of title.trim()) {
    hash ^= ch.charCodeAt(0);
    hash = Math.imul(hash, 16777619);
  }
  const base = ascii || `result_${(hash >>> 0).toString(36)}`;
  let candidate = base;
  for (let suffix = 2; existing.has(candidate); suffix++) candidate = `${base}_${suffix}`;
  return candidate;
}

export const ResultShelf: React.FC<ResultShelfProps> = ({
  slots,
  activeDefinitionRevision,
  definition,
  readonly = false,
  onRequestWorkflowChange,
  onOpen,
  onDownload,
  onLocate,
  onRetry,
  onPreview,
  onConvert,
}) => {
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<ArtifactSlotDef | null>(null);
  const [draft, setDraft] = useState<AddDraft>({
    title: '',
    kind: 'document',
    expectedCount: 1,
    required: true,
    producerNodeId: definition?.nodes?.[0]?.id ?? '',
  });
  const visibleSlots = slots.filter(
    (slot) => slot.definitionRev === activeDefinitionRevision,
  );
  const definitions = definition?.artifactSlots ?? [];
  const existingIDs = useMemo(
    () => new Set(definitions.map((slot) => slot.id)),
    [definitions],
  );
  const canEdit = !readonly && Boolean(onRequestWorkflowChange && definition?.nodes?.length);

  const submitAdd = () => {
    const title = draft.title.trim();
    const producer = definition?.nodes?.find((node) => node.id === draft.producerNodeId);
    if (!title || !producer || !onRequestWorkflowChange) return;
    const id = slotID(title, existingIDs);
    onRequestWorkflowChange({
      token: `add:${id}:${Date.now()}`,
      nodeId: producer.id,
      title: `新增成果：${title}`,
      instruction: `新增成果“${title}”（ID：${id}，类型：${draft.kind}，数量：${draft.expectedCount}，必需：${draft.required ? '是' : '否'}），由任务“${producer.title}”（节点 ID：${producer.id}）产出。只添加该成果并更新这个任务的产出引用，不改动其他流程结构。`,
    });
    setAdding(false);
    setDraft((current) => ({ ...current, title: '' }));
  };

  const submitDelete = () => {
    if (!deleting || !onRequestWorkflowChange || !definition) return;
    const producers = definition.nodes.filter((node) => node.producesSlotIds?.includes(deleting.id));
    const consumers = definition.nodes.filter((node) => node.consumesSlotIds?.includes(deleting.id));
    const target = producers[0] ?? consumers[0] ?? definition.nodes[0];
    if (!target) return;
    const references = [...producers, ...consumers]
      .filter((node, index, all) => all.findIndex((candidate) => candidate.id === node.id) === index)
      .map((node) => `“${node.title}”（${node.id}）`)
      .join('、');
    onRequestWorkflowChange({
      token: `remove:${deleting.id}:${Date.now()}`,
      nodeId: target.id,
      title: `删除成果：${deleting.title}`,
      instruction: `删除成果“${deleting.title}”（ID：${deleting.id}），并从所有产出或使用它的任务中移除引用${references ? `，涉及：${references}` : ''}。保留这些任务，只移除该成果及其引用，不改动其他流程结构。`,
    });
    setDeleting(null);
  };

  return (
    <section
      className="wg2-rs-frame"
      role="region"
      aria-label="成果架"
      aria-live="polite"
    >
      <ShelfIntro
        canEdit={canEdit}
        adding={adding}
        onAdd={() => setAdding((current) => !current)}
      />
      <div className="wg2-rs-content">
        {adding && (
          <div className="wg2-rs-editor" data-testid="result-add-form">
            <div className="wg2-rs-editor-head">
              <strong>添加成果</strong>
              <button type="button" onClick={() => setAdding(false)} aria-label="关闭添加成果"><X size={15} /></button>
            </div>
            <div className="wg2-rs-editor-grid">
              <label>
                <span>成果名称</span>
                <input
                  value={draft.title}
                  onInput={(event) => setDraft({ ...draft, title: event.currentTarget.value })}
                  placeholder="例如：学习总结"
                  data-testid="result-add-title"
                />
              </label>
              <label>
                <span>成果类型</span>
                <select value={draft.kind} onChange={(event) => setDraft({ ...draft, kind: event.currentTarget.value })}>
                  <option value="document">文档</option>
                  <option value="text">文本</option>
                  <option value="data">数据</option>
                  <option value="file">文件</option>
                </select>
              </label>
              <label>
                <span>由哪个任务产出</span>
                <select
                  value={draft.producerNodeId}
                  onChange={(event) => setDraft({ ...draft, producerNodeId: event.currentTarget.value })}
                  data-testid="result-add-producer"
                >
                  <option value="" disabled>选择任务</option>
                  {definition?.nodes?.map((node) => <option key={node.id} value={node.id}>{node.title}</option>)}
                </select>
              </label>
              <label>
                <span>数量</span>
                <input
                  type="number"
                  min={1}
                  max={99}
                  value={draft.expectedCount}
                  onChange={(event) => setDraft({ ...draft, expectedCount: Math.max(1, Number(event.currentTarget.value) || 1) })}
                />
              </label>
            </div>
            <label className="wg2-rs-required">
              <input
                type="checkbox"
                checked={draft.required}
                onChange={(event) => setDraft({ ...draft, required: event.currentTarget.checked })}
              />
              <span>必须生成后，工作才算完成</span>
            </label>
            <div className="wg2-rs-editor-actions">
              <span>下一步会先展示受影响的任务，不会立即修改。</span>
              <button
                type="button"
                disabled={!draft.title.trim() || !draft.producerNodeId}
                onClick={submitAdd}
                data-testid="result-add-preview"
              >
                预览流程变化
              </button>
            </div>
          </div>
        )}
        {visibleSlots.length === 0 ? (
          <div className="wg2-rs-empty" data-testid="result-shelf-empty">
            <span>暂无成果</span>
            <small>{canEdit ? '添加成果后，它会进入对应任务的产出流程。' : '工作产出将在这里集中显示。'}</small>
          </div>
        ) : (
          <ul className="wg2-rs-shelf" data-testid="result-shelf">
            {visibleSlots.map((slot) => (
              <li
                key={`${slot.definitionRev}:${slot.id}`}
                data-definition-revision={slot.definitionRev}
                data-testid={`result-shelf-item-${slot.id}`}
              >
                {canEdit && (
                  <button
                    type="button"
                    className="wg2-rs-delete"
                    aria-label={`删除成果 ${slot.title}`}
                    onClick={() => setDeleting(definitions.find((candidate) => candidate.id === slot.id) ?? {
                      id: slot.id,
                      title: slot.title,
                      kind: slot.kind,
                      expectedCount: slot.expectedCount,
                      required: slot.required,
                    })}
                    data-testid={`result-delete-${slot.id}`}
                  >
                    <Trash2 size={14} />
                  </button>
                )}
                <ResultCard
                  slot={slot}
                  onOpen={onOpen}
                  onDownload={onDownload}
                  onLocate={onLocate}
                  onRetry={onRetry}
                  onPreview={onPreview}
                  onConvert={onConvert}
                />
              </li>
            ))}
          </ul>
        )}
      </div>
      {deleting && (
        <div className="wg2-rs-confirm" role="alertdialog" aria-label="确认删除成果" data-testid="result-delete-confirm">
          <strong>删除“{deleting.title}”？</strong>
          <p>{deleteImpactText(deleting.id, definition)}</p>
          <div>
            <button type="button" onClick={() => setDeleting(null)}>取消</button>
            <button type="button" className="danger" onClick={submitDelete} data-testid="result-delete-preview">
              预览删除影响
            </button>
          </div>
        </div>
      )}
    </section>
  );
};

function deleteImpactText(slotId: string, definition?: WorkDefinitionRevision): string {
  const producers = definition?.nodes?.filter((node) => node.producesSlotIds?.includes(slotId)) ?? [];
  const consumers = definition?.nodes?.filter((node) => node.consumesSlotIds?.includes(slotId)) ?? [];
  const tasks = [...producers, ...consumers]
    .filter((node, index, all) => all.findIndex((candidate) => candidate.id === node.id) === index);
  if (tasks.length === 0) return '这个成果及其历史文件会从当前流程中移除。';
  return `会移除 ${tasks.map((node) => `“${node.title}”`).join('、')} 对它的产出或使用关系，并重跑受影响的后续任务。`;
}

const ShelfIntro: React.FC<{ canEdit: boolean; adding: boolean; onAdd: () => void }> = ({ canEdit, adding, onAdd }) => (
  <header className="wg2-rs-intro">
    <div className="wg2-rs-title">
      <Sparkles size={16} aria-hidden="true" />
      <span>成果</span>
    </div>
    <p>工作过程中产生的文件与成果将在此汇总</p>
    {canEdit && (
      <button type="button" className="wg2-rs-add" onClick={onAdd} aria-expanded={adding} data-testid="result-add">
        <Plus size={14} />
        <span>添加成果</span>
      </button>
    )}
  </header>
);
