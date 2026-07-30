import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AlertCircle, Check, Pencil, Plus, RefreshCw, Sparkles, Trash2, X } from 'lucide-react';

import type { ArtifactSlot, ArtifactPreview, ArtifactSlotDef, TaskV2View, WorkDefinitionRevision } from '../../types_v2';
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
  /** Current Run tasks used to reconcile a lagging artifact projection. */
  tasks?: TaskV2View[];
  /** Current authoritative Run identity. */
  runId?: string;
  readonly?: boolean;
  onRequestWorkflowChange?: (request: ResultWorkflowChangeRequest) => void;
  workflowChangeState?: WorkflowChangeState | null;
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
  /** UI dispatch attempt; retries increment this while keeping token/idempotency keys stable. */
  attempt?: number;
  nodeId: string;
  title: string;
  instruction: string;
}

export interface WorkflowChangeState {
  token: string;
  status: 'updating' | 'applied' | 'failed';
  error?: string;
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
  { value: 'docx', label: 'Word 文档（DOCX）' },
  { value: 'pdf', label: 'PDF 文档' },
  { value: 'xlsx', label: 'Excel 工作簿（XLSX）' },
  { value: 'data', label: '数据' },
  { value: 'sh', label: 'Shell 脚本（SH）' },
  { value: 'bat', label: 'Windows 批处理（BAT/CMD）' },
  { value: 'ps1', label: 'PowerShell 脚本（PS1）' },
  { value: 'exe', label: '可执行程序（EXE）' },
  { value: 'zip', label: '压缩包（ZIP）' },
  { value: 'file', label: '其他文件' },
] as const;

function titleForKind(title: string, kind: string): string {
  const extension: Record<string, string> = {
    document: '.md',
    markdown: '.md',
    md: '.md',
    text: '.txt',
    txt: '.txt',
    docx: '.docx',
    word: '.docx',
    pdf: '.pdf',
    xlsx: '.xlsx',
    spreadsheet: '.xlsx',
    excel: '.xlsx',
    data: '.json',
    sh: '.sh',
    shell: '.sh',
    bat: '.bat',
    cmd: '.cmd',
    batch: '.bat',
    ps1: '.ps1',
    powershell: '.ps1',
    executable: '.exe',
    exe: '.exe',
    archive: '.zip',
    zip: '.zip',
  };
  const next = extension[kind.toLowerCase()];
  if (!next) return title;
  const trimmed = title.trim();
  if (!trimmed) return trimmed;
  if (trimmed.toLowerCase().endsWith(next)) return trimmed;
  if (/\.(?:md|markdown|txt|docx?|pdf|xlsx|xls|csv|json|sh|bat|cmd|ps1|exe|zip|7z|tar|gz)$/i.test(trimmed)) {
    return trimmed.replace(/\.(?:md|markdown|txt|docx?|pdf|xlsx|xls|csv|json|sh|bat|cmd|ps1|exe|zip|7z|tar|gz)$/i, next);
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

function effectiveSlot(
  slot: ArtifactSlot,
  definition?: WorkDefinitionRevision,
  tasks: TaskV2View[] = [],
  runId?: string,
): ArtifactSlot {
  if ((slot.state !== 'reserved' && slot.state !== 'generating') || !definition || !runId) return slot;
  const producerIDs = new Set(
    definition.nodes
      .filter((node) => node.producesSlotIds?.includes(slot.id))
      .map((node) => node.id),
  );
  const producer = tasks.find((task) =>
    task.runId === runId &&
    producerIDs.has(task.nodeId) &&
    (task.state === 'failed_retryable' || task.state === 'failed_terminal' || task.state === 'canceled'));
  if (!producer) return slot;

  const retryable = producer.state === 'failed_retryable';
  const message = producer.error?.trim() || (
    retryable
      ? '产出任务失败，等待重试。'
      : producer.state === 'canceled'
        ? '产出任务已取消。'
        : '产出任务失败，无法自动重试。'
  );
  return {
    ...slot,
    state: 'failed',
    progress: undefined,
    error: {
      code: producer.state === 'canceled' ? 'producer_canceled' : 'producer_failed',
      message,
      retryable,
    },
  };
}

export const ResultShelf: React.FC<ResultShelfProps> = ({
  slots,
  activeDefinitionRevision,
  definition,
  tasks,
  runId,
  readonly = false,
  onRequestWorkflowChange,
  workflowChangeState,
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
  const [appliedVisible, setAppliedVisible] = useState(false);
  const appliedTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [draft, setDraft] = useState<ArtifactDraft>({
    title: '',
    kind: 'document',
    expectedCount: 1,
    required: true,
  });
  const visibleSlots = useMemo(
    () => slots
      .filter((slot) => slot.definitionRev === activeDefinitionRevision)
      .map((slot) => effectiveSlot(slot, definition, tasks, runId)),
    [activeDefinitionRevision, definition, runId, slots, tasks],
  );
  const definitions = definition?.artifactSlots ?? [];
  const existingIDs = useMemo(
    () => new Set(definitions.map((slot) => slot.id)),
    [definitions],
  );
  const canEdit = !readonly && Boolean(onRequestWorkflowChange && definition?.nodes?.length);
  const isUpdating = workflowChangeState?.status === 'updating';

  useEffect(() => {
    if (workflowChangeState?.status === 'applied') {
      setAppliedVisible(true);
      clearTimeout(appliedTimerRef.current);
      appliedTimerRef.current = setTimeout(() => setAppliedVisible(false), 3000);
      return () => clearTimeout(appliedTimerRef.current);
    }
    setAppliedVisible(false);
  }, [workflowChangeState?.status, workflowChangeState?.token]);

  const fireRequest = useCallback((request: ResultWorkflowChangeRequest) => {
    onRequestWorkflowChange?.(request);
  }, [onRequestWorkflowChange]);

  const submitAdd = () => {
    const title = draft.title.trim();
    const anchor = workflowAnchorNode(definition);
    if (!title || !anchor || !onRequestWorkflowChange || isUpdating) return;
    const id = slotID(title, existingIDs);
    fireRequest({
      token: `add:${id}:${Date.now()}`,
      nodeId: anchor.id,
      title: `新增成果：${title}`,
      instruction: `新增成果“${title}”（ID：${id}，类型：${draft.kind}，数量：${draft.expectedCount}，必需：${draft.required ? '是' : '否'}）。请根据当前流程中各任务的职责、依赖顺序及已有成果关系，自动推断唯一且最合适的产出任务；不要新增任务，也不要要求用户选择产出任务。只添加该成果并更新推断任务的产出引用，不改动其他流程结构。`,
    });
    setAdding(false);
    setDraft((current) => ({ ...current, title: '' }));
  };

  const submitDelete = () => {
    if (!deleting || !onRequestWorkflowChange || !definition || isUpdating) return;
    const producers = definition.nodes.filter((node) => node.producesSlotIds?.includes(deleting.id));
    const consumers = definition.nodes.filter((node) => node.consumesSlotIds?.includes(deleting.id));
    const target = producers[0] ?? consumers[0] ?? definition.nodes[0];
    if (!target) return;
    const references = [...producers, ...consumers]
      .filter((node, index, all) => all.findIndex((candidate) => candidate.id === node.id) === index)
      .map((node) => `“${node.title}”（${node.id}）`)
      .join('、');
    fireRequest({
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
    if (!editing || !definition || !onRequestWorkflowChange || isUpdating) return;
    const title = editing.draft.title.trim();
    if (!title || !artifactChanged(editing.slot, editing.draft)) return;
    const producers = definition.nodes.filter((node) => node.producesSlotIds?.includes(editing.slot.id));
    const consumers = definition.nodes.filter((node) => node.consumesSlotIds?.includes(editing.slot.id));
    const target = producers[0] ?? consumers[0] ?? definition.nodes[0];
    if (!target) return;
    const changes = artifactChanges(editing.slot, { ...editing.draft, title });
    const formatChanged = editing.slot.kind !== editing.draft.kind;
    fireRequest({
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
        disabled={isUpdating}
        onAdd={() => {
          setEditing(null);
          setDeleting(null);
          setAdding((current) => !current);
        }}
      />
      <div className="wg2-rs-main">
        {workflowChangeState && (workflowChangeState.status !== 'applied' || appliedVisible) && (
          <div
            className={`wg2-rs-wfstatus wg2-rs-wfstatus--${workflowChangeState.status}`}
            role={workflowChangeState.status === 'failed' ? 'alert' : 'status'}
            aria-live="polite"
            data-testid="result-workflow-status"
          >
            {workflowChangeState.status === 'updating' && (
              <><RefreshCw size={14} className="wg2-rs-wfstatus-spin" /> AI 正在协调更新…</>
            )}
            {workflowChangeState.status === 'applied' && appliedVisible && (
              <><Check size={14} /> 已更新</>
            )}
            {workflowChangeState.status === 'failed' && (
              <>
                <AlertCircle size={14} />
                <span className="wg2-rs-wfstatus-err">
                  这次调整暂未完成{workflowChangeState.error ? `：${workflowChangeState.error}` : ''}。你可以补充要求后再次提交。
                </span>
              </>
            )}
          </div>
        )}
        <div className="wg2-rs-content">
        {adding && (
          <ResultEditor
            mode="add"
            title="添加成果"
            draft={draft}
            onChange={setDraft}
            onClose={() => setAdding(false)}
            onSubmit={submitAdd}
            submitDisabled={!draft.title.trim() || isUpdating}
            isUpdating={isUpdating}
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
            submitDisabled={!editing.draft.title.trim() || !artifactChanged(editing.slot, editing.draft) || isUpdating}
            isUpdating={isUpdating}
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
                        disabled={isUpdating}
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
                        disabled={isUpdating}
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
      </div>
      {deleting && (
        <div className="wg2-rs-confirm" role="alertdialog" aria-label="确认删除成果" data-testid="result-delete-confirm">
          <strong>删除“{deleting.title}”？</strong>
          <p>{deleteImpactText(deleting.id, definition)}</p>
          <div>
            <button type="button" onClick={() => setDeleting(null)}>取消</button>
            <button type="button" className="danger" onClick={submitDelete} disabled={isUpdating} data-testid="result-delete-confirm-btn">
              删除成果
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
  isUpdating?: boolean;
}> = ({ mode, title, draft, onChange, onClose, onSubmit, submitDisabled, isUpdating }) => (
  <div className="wg2-rs-editor" data-testid={`result-${mode}-form`}>
    <div className="wg2-rs-editor-head">
      <strong>{title}</strong>
      <button type="button" onClick={onClose} disabled={isUpdating} aria-label={`关闭${mode === 'add' ? '添加' : '修改'}成果`}><X size={15} /></button>
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
        ? '产出任务将由流程自动推断。'
        : '会生成新版本并重跑相关任务，历史文件仍可追溯。'}</span>
      <button
        type="button"
        disabled={submitDisabled}
        onClick={onSubmit}
        data-testid={`result-${mode}-submit`}
      >
        {mode === 'add' ? '添加成果' : '保存修改'}
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

const ShelfIntro: React.FC<{ canEdit: boolean; adding: boolean; disabled?: boolean; onAdd: () => void }> = ({ canEdit, adding, disabled, onAdd }) => (
  <header className="wg2-rs-intro">
    <div className="wg2-rs-title">
      <Sparkles size={16} aria-hidden="true" />
      <span>成果</span>
    </div>
    <p>工作过程中产生的文件与成果将在此汇总</p>
    {canEdit && (
      <button type="button" className="wg2-rs-add" onClick={onAdd} disabled={disabled} aria-expanded={adding} data-testid="result-add">
        <Plus size={14} />
        <span>添加成果</span>
      </button>
    )}
  </header>
);
