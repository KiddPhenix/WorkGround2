import React, { useMemo, useState } from 'react';
import { Pencil, Plus, Sparkles, Trash2, X } from 'lucide-react';

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

interface ArtifactDraft {
  title: string;
  kind: string;
  expectedCount: number;
  required: boolean;
}

interface EditState {
  slot: ArtifactSlotDef;
  draft: ArtifactDraft;
}

const ARTIFACT_KINDS = [
  { value: 'document', label: '文档（Markdown）' },
  { value: 'text', label: '纯文本' },
  { value: 'xlsx', label: 'Excel 工作簿（XLSX）' },
  { value: 'data', label: '数据' },
  { value: 'file', label: '其他文件' },
] as const;

function titleForKind(title: string, kind: string): string {
  const extension: Record<string, string> = {
    document: '.md',
    markdown: '.md',
    md: '.md',
    text: '.txt',
    txt: '.txt',
    xlsx: '.xlsx',
    spreadsheet: '.xlsx',
    excel: '.xlsx',
    data: '.json',
  };
  const next = extension[kind.toLowerCase()];
  if (!next) return title;
  const trimmed = title.trim();
  if (!trimmed) return trimmed;
  if (trimmed.toLowerCase().endsWith(next)) return trimmed;
  if (/\.(?:md|markdown|txt|xlsx|xls|csv|json)$/i.test(trimmed)) {
    return trimmed.replace(/\.(?:md|markdown|txt|xlsx|xls|csv|json)$/i, next);
  }
  return `${trimmed}${next}`;
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
  const [editing, setEditing] = useState<EditState | null>(null);
  const [deleting, setDeleting] = useState<ArtifactSlotDef | null>(null);
  const [draft, setDraft] = useState<ArtifactDraft>({
    title: '',
    kind: 'document',
    expectedCount: 1,
    required: true,
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
    const anchor = workflowAnchorNode(definition);
    if (!title || !anchor || !onRequestWorkflowChange) return;
    const id = slotID(title, existingIDs);
    onRequestWorkflowChange({
      token: `add:${id}:${Date.now()}`,
      nodeId: anchor.id,
      title: `新增成果：${title}`,
      instruction: `新增成果“${title}”（ID：${id}，类型：${draft.kind}，数量：${draft.expectedCount}，必需：${draft.required ? '是' : '否'}）。请根据当前流程中各任务的职责、依赖顺序及已有成果关系，自动推断唯一且最合适的产出任务；不要新增任务，也不要要求用户选择产出任务。只添加该成果并更新推断任务的产出引用，不改动其他流程结构。`,
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

  const beginEdit = (slot: ArtifactSlot) => {
    const current = definitions.find((candidate) => candidate.id === slot.id) ?? {
      id: slot.id,
      title: slot.title,
      kind: slot.kind,
      expectedCount: slot.expectedCount,
      required: slot.required,
    };
    setAdding(false);
    setDeleting(null);
    setEditing({
      slot: current,
      draft: {
        title: current.title,
        kind: current.kind,
        expectedCount: current.expectedCount,
        required: current.required,
      },
    });
  };

  const submitEdit = () => {
    if (!editing || !definition || !onRequestWorkflowChange) return;
    const title = editing.draft.title.trim();
    if (!title || !artifactChanged(editing.slot, editing.draft)) return;
    const producers = definition.nodes.filter((node) => node.producesSlotIds?.includes(editing.slot.id));
    const consumers = definition.nodes.filter((node) => node.consumesSlotIds?.includes(editing.slot.id));
    const target = producers[0] ?? consumers[0] ?? definition.nodes[0];
    if (!target) return;
    const changes = artifactChanges(editing.slot, { ...editing.draft, title });
    const formatChanged = editing.slot.kind !== editing.draft.kind;
    onRequestWorkflowChange({
      token: `edit:${editing.slot.id}:${Date.now()}`,
      nodeId: target.id,
      title: `修改成果：${editing.slot.title}`,
      instruction: `修改成果“${editing.slot.title}”（ID：${editing.slot.id}）：${changes.join('；')}。保留成果 ID、唯一产出任务、所有使用关系和流程依赖；对该成果的相应字段使用 replace，不要删除后重建。${formatChanged ? `格式必须真实转换为 ${editing.draft.kind}，重新生成可被对应软件打开的文件，不能只改扩展名或 MIME。` : ''}只修改该成果定义以及产出任务必要的格式要求，不改动其他任务职责。`,
    });
    setEditing(null);
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
        onAdd={() => {
          setEditing(null);
          setDeleting(null);
          setAdding((current) => !current);
        }}
      />
      <div className="wg2-rs-content">
        {adding && (
          <ResultEditor
            mode="add"
            title="添加成果"
            draft={draft}
            onChange={setDraft}
            onClose={() => setAdding(false)}
            onSubmit={submitAdd}
            submitDisabled={!draft.title.trim()}
          />
        )}
        {editing && (
          <ResultEditor
            mode="edit"
            title={`修改成果：${editing.slot.title}`}
            draft={editing.draft}
            onChange={(next) => setEditing({ ...editing, draft: next })}
            onClose={() => setEditing(null)}
            onSubmit={submitEdit}
            submitDisabled={!editing.draft.title.trim() || !artifactChanged(editing.slot, editing.draft)}
          />
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
                <ResultCard
                  slot={slot}
                  onOpen={onOpen}
                  onDownload={onDownload}
                  onLocate={onLocate}
                  onRetry={onRetry}
                  onPreview={onPreview}
                  onConvert={onConvert}
                  managementActions={canEdit ? (
                    <>
                      <button
                        type="button"
                        className="wg2-rc-manage-btn"
                        aria-label={`修改成果 ${slot.title}`}
                        title="修改成果"
                        onClick={() => beginEdit(slot)}
                        data-testid={`result-edit-${slot.id}`}
                      >
                        <Pencil size={14} />
                      </button>
                      <button
                        type="button"
                        className="wg2-rc-manage-btn wg2-rc-manage-btn--danger"
                        aria-label={`删除成果 ${slot.title}`}
                        title="删除成果"
                        onClick={() => {
                          setAdding(false);
                          setEditing(null);
                          setDeleting(definitions.find((candidate) => candidate.id === slot.id) ?? {
                            id: slot.id,
                            title: slot.title,
                            kind: slot.kind,
                            expectedCount: slot.expectedCount,
                            required: slot.required,
                          });
                        }}
                        data-testid={`result-delete-${slot.id}`}
                      >
                        <Trash2 size={14} />
                      </button>
                    </>
                  ) : undefined}
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

const ResultEditor: React.FC<{
  mode: 'add' | 'edit';
  title: string;
  draft: ArtifactDraft;
  onChange: (draft: ArtifactDraft) => void;
  onClose: () => void;
  onSubmit: () => void;
  submitDisabled: boolean;
}> = ({ mode, title, draft, onChange, onClose, onSubmit, submitDisabled }) => (
  <div className="wg2-rs-editor" data-testid={`result-${mode}-form`}>
    <div className="wg2-rs-editor-head">
      <strong>{title}</strong>
      <button type="button" onClick={onClose} aria-label={`关闭${mode === 'add' ? '添加' : '修改'}成果`}><X size={15} /></button>
    </div>
    <div className="wg2-rs-editor-grid">
      <label>
        <span>成果名称</span>
        <input
          value={draft.title}
          onInput={(event) => onChange({ ...draft, title: event.currentTarget.value })}
          placeholder="例如：学习总结"
          data-testid={`result-${mode}-title`}
        />
      </label>
      <label>
        <span>成果格式</span>
        <select
          value={draft.kind}
          onChange={(event) => {
            const kind = event.currentTarget.value;
            onChange({ ...draft, kind, title: titleForKind(draft.title, kind) });
          }}
          data-testid={`result-${mode}-kind`}
        >
          {!ARTIFACT_KINDS.some((option) => option.value === draft.kind) && (
            <option value={draft.kind}>{draft.kind}</option>
          )}
          {ARTIFACT_KINDS.map((option) => (
            <option key={option.value} value={option.value}>{option.label}</option>
          ))}
        </select>
      </label>
      <label>
        <span>数量</span>
        <input
          type="number"
          min={1}
          max={99}
          value={draft.expectedCount}
          onChange={(event) => onChange({ ...draft, expectedCount: Math.max(1, Number(event.currentTarget.value) || 1) })}
          data-testid={`result-${mode}-count`}
        />
      </label>
    </div>
    <label className="wg2-rs-required">
      <input
        type="checkbox"
        checked={draft.required}
        onChange={(event) => onChange({ ...draft, required: event.currentTarget.checked })}
      />
      <span>必须生成后，工作才算完成</span>
    </label>
    <div className="wg2-rs-editor-actions">
      <span>{mode === 'add'
        ? '产出任务将根据流程自动推断；下一步会先展示影响，不会立即修改。'
        : '会生成新版本，并重跑产出任务及受影响的后续任务；历史文件仍可追溯。'}</span>
      <button
        type="button"
        disabled={submitDisabled}
        onClick={onSubmit}
        data-testid={`result-${mode}-preview`}
      >
        预览流程变化
      </button>
    </div>
  </div>
);

function artifactChanged(slot: ArtifactSlotDef, draft: ArtifactDraft): boolean {
  return slot.title !== draft.title.trim()
    || slot.kind !== draft.kind
    || slot.expectedCount !== draft.expectedCount
    || slot.required !== draft.required;
}

function artifactChanges(slot: ArtifactSlotDef, draft: ArtifactDraft): string[] {
  const changes: string[] = [];
  if (slot.title !== draft.title) changes.push(`名称从“${slot.title}”改为“${draft.title}”`);
  if (slot.kind !== draft.kind) changes.push(`格式从 ${slot.kind} 改为 ${draft.kind}`);
  if (slot.expectedCount !== draft.expectedCount) changes.push(`数量从 ${slot.expectedCount} 改为 ${draft.expectedCount}`);
  if (slot.required !== draft.required) changes.push(`完成条件改为${draft.required ? '必须生成' : '可选生成'}`);
  return changes;
}

function deleteImpactText(slotId: string, definition?: WorkDefinitionRevision): string {
  const producers = definition?.nodes?.filter((node) => node.producesSlotIds?.includes(slotId)) ?? [];
  const consumers = definition?.nodes?.filter((node) => node.consumesSlotIds?.includes(slotId)) ?? [];
  const tasks = [...producers, ...consumers]
    .filter((node, index, all) => all.findIndex((candidate) => candidate.id === node.id) === index);
  if (tasks.length === 0) return '这个成果及其历史文件会从当前流程中移除。';
  return `会移除 ${tasks.map((node) => `“${node.title}”`).join('、')} 对它的产出或使用关系，并重跑受影响的后续任务。`;
}

function workflowAnchorNode(
  definition?: WorkDefinitionRevision,
): WorkDefinitionRevision['nodes'][number] | undefined {
  const nodes = definition?.nodes ?? [];
  if (nodes.length === 0) return undefined;
  const upstreamIDs = new Set(nodes.flatMap((node) => node.dependsOn ?? []));
  return [...nodes].reverse().find((node) => !upstreamIDs.has(node.id)) ?? nodes[nodes.length - 1];
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
