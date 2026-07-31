import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ChevronDown,
  ClipboardPenLine,
  FileText,
  FileUp,
  Sparkles,
  TriangleAlert,
  X,
} from 'lucide-react';

import { onFilesDroppedIn } from '../../../lib/bridge';
import { useWorkUIStore } from '../../store';
import type {
  CornerstonePinResult,
  AddCustomWorkInputRequest,
  InputSpec,
  SelectWorkInputFileRequest,
  SelectWorkInputFileResult,
  SelectWorkInformationFileRequest,
  SetInputCornerstoneRequest,
  SubmitInputResult,
  SubmitWorkInputRequest,
  TaskV2View,
  WorkDefinitionRevision,
  WorkInput,
} from '../../types_v2';
import type { ArtifactRef } from '../../types';
import {
  WorkInputHost,
  type DraftValue,
  type WorkInputRefreshContext,
} from '../v2/input/WorkInputHost';
import { kindLabel } from '../v2/input/schema';
import { WorkDefinitionOverview } from './WorkDefinitionOverview';

type AfterSubmit = 'next' | 'close';

export interface WorkInformationPanelProps {
  workId: string;
  runId?: string;
  workRevision: number;
  definition: WorkDefinitionRevision;
  tasks: TaskV2View[];
  inputs: WorkInput[];
  readonly?: boolean;
  onSubmit?: (request: SubmitWorkInputRequest) => Promise<SubmitInputResult>;
  onPin?: (request: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  onUnpin?: (request: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  onRefresh?: (context: WorkInputRefreshContext) => Promise<void>;
  onSelectFile?: (request: SelectWorkInputFileRequest) => Promise<SelectWorkInputFileResult>;
  onSelectCustomFile?: (request: SelectWorkInformationFileRequest) => Promise<SelectWorkInputFileResult>;
  onAddCustom?: (request: AddCustomWorkInputRequest) => Promise<SubmitInputResult>;
}

function inputKey(
  workId: string,
  runId: string,
  input: WorkInput,
  definitionRevision: number,
): string {
  return [
    workId,
    runId,
    input.taskId,
    input.blockId,
    input.id,
    input.specId,
    definitionRevision,
    input.revision,
  ].join('\u0000');
}

function initialDraft(spec: InputSpec, input: WorkInput): DraftValue {
  if (input.value != null) {
    try {
      return JSON.parse(
        typeof input.value === 'string' ? input.value : JSON.stringify(input.value),
      ) as DraftValue;
    } catch {
      return String(input.value ?? '');
    }
  }
  if (spec.defaultValue != null) return spec.defaultValue as DraftValue;
  if (spec.kind === 'multi_choice' || spec.kind === 'file' || spec.kind === 'roster') return [];
  return spec.kind === 'approval' ? false : '';
}

function isDone(input: WorkInput): boolean {
  return input.state === 'submitted' || input.state === 'accepted';
}

export const WorkInformationPanel: React.FC<WorkInformationPanelProps> = ({
  workId,
  runId,
  workRevision,
  definition,
  tasks,
  inputs,
  readonly,
  onSubmit,
  onPin,
  onUnpin,
  onRefresh,
  onSelectFile,
  onSelectCustomFile,
  onAddCustom,
}) => {
  const effectiveRunId = runId ?? '';
  const ensureCard = useWorkUIStore((state) => state.ensureCard);
  const card = useWorkUIStore((state) => state.cardByWork[workId]);
  const setInputDraft = useWorkUIStore((state) => state.setInputDraft);
  const setInputDirtyFlag = useWorkUIStore((state) => state.setInputDirtyFlag);
  const setCommittedRequestId = useWorkUIStore((state) => state.setCommittedRequestId);
  const setPanel = useWorkUIStore((state) => state.setInformationPanel);
  const [dragOver, setDragOver] = useState(false);
  const [dropError, setDropError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [addName, setAddName] = useState('');
  const [addDescription, setAddDescription] = useState('');
  const [addKind, setAddKind] = useState<'text' | 'file'>('text');
  const [addText, setAddText] = useState('');
  const [addFiles, setAddFiles] = useState<ArtifactRef[]>([]);
  const [addBusy, setAddBusy] = useState(false);
  const [addError, setAddError] = useState<string | null>(null);
  const dropTargetRef = useRef<HTMLDivElement>(null);
  const editAuthorityRef = useRef<{ inputId: string; revision: number } | null>(null);

  useEffect(() => ensureCard(workId), [ensureCard, workId]);

  const specs = useMemo(() => new Map([
    ...definition.inputSpecs.map((spec) => [spec.id, spec] as const),
    ...inputs.flatMap((input) => input.customSpec
      ? [[input.customSpec.id, input.customSpec] as const]
      : []),
  ]), [definition.inputSpecs, inputs]);
  const taskOrder = useMemo(
    () => new Map(definition.nodes.map((node, index) => [node.id, index])),
    [definition.nodes],
  );
  const taskById = useMemo(() => new Map(tasks.map((task) => [task.id, task])), [tasks]);
  const currentInputs = useMemo(
    () => inputs
      .filter((input) => (input.customSpec || input.runId === effectiveRunId) && specs.has(input.specId))
      .sort((left, right) => {
        const leftTask = taskById.get(left.taskId);
        const rightTask = taskById.get(right.taskId);
        const taskDelta = (taskOrder.get(leftTask?.nodeId ?? '') ?? Number.MAX_SAFE_INTEGER)
          - (taskOrder.get(rightTask?.nodeId ?? '') ?? Number.MAX_SAFE_INTEGER);
        if (taskDelta !== 0) return taskDelta;
        const leftSpec = definition.inputSpecs.findIndex((candidate) => candidate.id === left.specId);
        const rightSpec = definition.inputSpecs.findIndex((candidate) => candidate.id === right.specId);
        if (leftSpec < 0 || rightSpec < 0) {
          if (leftSpec < 0 && rightSpec >= 0) return 1;
          if (rightSpec < 0 && leftSpec >= 0) return -1;
          return left.updatedAt.localeCompare(right.updatedAt) || left.id.localeCompare(right.id);
        }
        return leftSpec - rightSpec || left.id.localeCompare(right.id);
      }),
    [definition.inputSpecs, effectiveRunId, inputs, specs, taskById, taskOrder],
  );
  const pending = useMemo(() => currentInputs.filter((input) => !isDone(input)), [currentInputs]);
  const done = currentInputs.length - pending.length;
  const panelState = card?.informationPanel ?? {
    closed: false,
    afterSubmit: 'next' as const,
    advancedOpen: {},
  };
  const active = panelState.activeInputId
    ? currentInputs.find((input) => input.id === panelState.activeInputId)
    : undefined;
  const editingDone = !!active && isDone(active);
  const stackInputs = editingDone && active ? [active] : pending;
  const activeIndex = active ? stackInputs.findIndex((input) => input.id === active.id) : -1;
  const spec = active ? specs.get(active.specId) : undefined;
  const key = active ? inputKey(workId, effectiveRunId, active, definition.revision) : '';
  const extraKey = `${key}\u0000extra`;
  const draft = active && spec
    ? (card?.inputDirtyFlags[key] ? card.inputDrafts[key] as DraftValue : initialDraft(spec, active))
    : '';
  const extra = typeof card?.inputDrafts[extraKey] === 'string'
    ? card.inputDrafts[extraKey] as string
    : active?.extra ?? '';
  const advancedOpen = active ? !!panelState.advancedOpen[active.id] : false;
  const canEdit = !readonly && !!onSubmit && !!onRefresh;
  const overlayOpen = (!panelState.closed && !!active && !!spec) || adding;
  const selectableInputSpecIds = useMemo(
    () => new Set(currentInputs.map((input) => input.specId)),
    [currentInputs],
  );

  // SubmitWorkInput waits for synchronous V2 rescheduling before its promise
  // resolves. The input revision itself is the earlier durable commit signal:
  // close a completed-input editor as soon as that authoritative revision
  // arrives, while rejected writes remain open because they are no longer done.
  useEffect(() => {
    if (panelState.closed || !active || !editingDone) {
      editAuthorityRef.current = null;
      return;
    }
    const authority = editAuthorityRef.current;
    if (!authority || authority.inputId !== active.id) {
      editAuthorityRef.current = { inputId: active.id, revision: active.revision };
      return;
    }
    if (active.revision <= authority.revision) return;
    editAuthorityRef.current = null;
    setPanel(workId, { closed: true, activeInputId: undefined });
  }, [
    active?.id,
    active?.revision,
    editingDone,
    panelState.closed,
    setPanel,
    workId,
  ]);

  const changeDraft = useCallback((value: DraftValue) => {
    if (!active) return;
    setInputDirtyFlag(workId, key);
    setInputDraft(workId, key, value);
  }, [active, key, setInputDirtyFlag, setInputDraft, workId]);

  const changeExtra = useCallback((value: string) => {
    if (!active) return;
    setInputDirtyFlag(workId, extraKey);
    setInputDraft(workId, extraKey, value);
  }, [active, extraKey, setInputDirtyFlag, setInputDraft, workId]);

  const selectFile = useCallback(async (path?: string) => {
    if (!active || !onSelectFile) return null;
    const result = await onSelectFile({
      workId,
      runId: active.runId,
      taskId: active.taskId,
      blockId: active.blockId,
      inputId: active.id,
      specId: active.specId,
      path,
    });
    if (result.error) throw new Error(result.error.message);
    return result.artifactRef ?? null;
  }, [active, onSelectFile, workId]);

  const selectCustomFile = useCallback(async (path?: string) => {
    if (!onSelectCustomFile) return;
    const result = await onSelectCustomFile({ workId, path });
    if (result.error) throw new Error(result.error.message);
    if (result.artifactRef) {
      setAddFiles((current) => current.some((item) => item.id === result.artifactRef?.id)
        ? current
        : [...current, result.artifactRef!]);
    }
  }, [onSelectCustomFile, workId]);

  useEffect(() => {
    if (spec?.kind !== 'file' || !canEdit) return;
    return onFilesDroppedIn(
      () => dropTargetRef.current,
      (paths) => {
        void (async () => {
          setDragOver(false);
          setDropError(null);
          const existing = Array.isArray(draft)
            ? [...draft] as Array<string | import('../../types').ArtifactRef>
            : [];
          try {
            for (const path of paths) {
              const ref = await selectFile(path);
              if (ref && !existing.some((item) =>
                typeof item === 'string' ? item === ref.id : item && typeof item === 'object' && 'id' in item && item.id === ref.id)) {
                existing.push(ref);
              }
            }
            changeDraft(existing);
          } catch (error) {
            setDropError(error instanceof Error ? error.message : '文件拖入失败，请重试');
          }
        })();
      },
    );
  }, [canEdit, changeDraft, draft, selectFile, spec?.kind]);

  useEffect(() => {
    if (!adding || addKind !== 'file' || !onSelectCustomFile) return;
    return onFilesDroppedIn(
      () => dropTargetRef.current,
      (paths) => {
        void (async () => {
          setAddError(null);
          try {
            for (const path of paths) await selectCustomFile(path);
          } catch (error) {
            setAddError(error instanceof Error ? error.message : '文件拖入失败，请重试');
          }
        })();
      },
    );
  }, [addKind, adding, onSelectCustomFile, selectCustomFile]);

  useEffect(() => {
    if (!overlayOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      setAdding(false);
      setPanel(workId, { closed: true, activeInputId: undefined });
    };
    window.addEventListener('keydown', closeOnEscape);
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [overlayOpen, setPanel, workId]);

  if (definition.inputSpecs.length === 0 && currentInputs.length === 0 && !onAddCustom) return null;

  const openInput = (specId: string) => {
    const input = currentInputs.find((candidate) => candidate.specId === specId);
    if (!input) return;
    setAdding(false);
    setPanel(workId, { activeInputId: input.id, closed: false });
  };

  const openAdd = () => {
    setPanel(workId, { activeInputId: undefined, closed: true });
    setAddName('');
    setAddDescription('');
    setAddKind('text');
    setAddText('');
    setAddFiles([]);
    setAddError(null);
    setAdding(true);
  };

  const submitCustom = async () => {
    const name = addName.trim();
    if (!name) {
      setAddError('请填写信息名称');
      return;
    }
    if (addKind === 'text' && !addText.trim()) {
      setAddError('请填写内容');
      return;
    }
    if (addKind === 'file' && addFiles.length === 0) {
      setAddError('请添加至少一个文件');
      return;
    }
    if (!onAddCustom || !effectiveRunId) {
      setAddError('新增工作信息暂不可用，请稍后重试');
      return;
    }
    setAddBusy(true);
    setAddError(null);
    try {
      const suffix = globalThis.crypto?.randomUUID?.()
        ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      await onAddCustom({
        workId,
        runId: effectiveRunId,
        inputId: `work-info-${suffix}`,
        name,
        description: addDescription.trim() || undefined,
        kind: addKind,
        value: addKind === 'file' ? addFiles : addText.trim(),
        definitionRevision: definition.revision,
        expectedRevision: workRevision,
        requestId: `add-work-info-${suffix}`,
      });
      setAdding(false);
    } catch (error) {
      setAddError(error instanceof Error ? error.message : '新增工作信息失败，请重试');
    } finally {
      setAddBusy(false);
    }
  };

  const summary = (
    <WorkDefinitionOverview
      definition={definition}
      inputs={currentInputs}
      runId={effectiveRunId}
      tasks={[]}
      showStructure={false}
      onSelectInput={openInput}
      selectableInputSpecIds={selectableInputSpecIds}
      onAddInput={!readonly && onAddCustom ? openAdd : undefined}
    />
  );

  if (!overlayOpen) {
    return (
      <section className="wg2-info-entry" data-testid="work-information-panel">
        {summary}
      </section>
    );
  }

  if (adding) {
    return (
      <section className="wg2-info-entry" data-testid="work-information-panel">
        {summary}
        <div
          className="wg2-info-overlay"
          role="presentation"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) setAdding(false);
          }}
        >
          <section
            className="wg2-info-panel"
            role="dialog"
            aria-modal="true"
            aria-labelledby="wg2-add-info-title"
            onMouseDown={(event) => event.stopPropagation()}
          >
            <header className="wg2-info-panel__bar">
              <div>
                <ClipboardPenLine size={17} aria-hidden="true" />
                <strong id="wg2-add-info-title">添加信息</strong>
                <span>补充工作需要知道的内容</span>
              </div>
              <button type="button" className="wg2-info-panel__close" onClick={() => setAdding(false)} aria-label="关闭添加信息">
                <X size={16} aria-hidden="true" />
              </button>
            </header>
            <div className="wg2-info-stack" data-stack-count="1">
              <article className="wg2-info-card wg2-info-card--add">
                <div className="wg2-info-card__heading">
                  <div>
                    <h3>新增工作信息</h3>
                    <p>名称会显示在工作信息列表中，解释可以留空。</p>
                  </div>
                </div>
                <div className="wg2-info-add__fields">
                  <label>
                    <span>名称</span>
                    <input value={addName} onChange={(event) => setAddName(event.currentTarget.value)} placeholder="例如：参考资料、补充要求" />
                  </label>
                  <label>
                    <span>解释 <em>可选</em></span>
                    <input value={addDescription} onChange={(event) => setAddDescription(event.currentTarget.value)} placeholder="说明这项信息有什么用途" />
                  </label>
                  <div className="wg2-info-add__kind" role="radiogroup" aria-label="内容类型">
                    <button type="button" role="radio" aria-checked={addKind === 'text'} className={addKind === 'text' ? 'is-selected' : undefined} onClick={() => setAddKind('text')}>
                      <FileText size={15} aria-hidden="true" />文本
                    </button>
                    <button type="button" role="radio" aria-checked={addKind === 'file'} className={addKind === 'file' ? 'is-selected' : undefined} onClick={() => setAddKind('file')}>
                      <FileUp size={15} aria-hidden="true" />文件
                    </button>
                  </div>
                  {addKind === 'text' ? (
                    <label>
                      <span>内容</span>
                      <textarea rows={6} value={addText} onChange={(event) => setAddText(event.currentTarget.value)} placeholder="填写希望工作过程持续参考的信息" />
                    </label>
                  ) : (
                    <div
                      ref={dropTargetRef}
                      className="wg2-info-add__file"
                      data-wg2-work-input-drop="true"
                      style={{ '--wails-drop-target': 'drop' } as React.CSSProperties}
                    >
                      <button type="button" onClick={() => void selectCustomFile()} disabled={!onSelectCustomFile}>
                        <FileUp size={18} aria-hidden="true" />
                        点击选择文件
                      </button>
                      {addFiles.length > 0 ? (
                        <ul>{addFiles.map((file) => (
                          <li key={file.id}>
                            <span>{file.name}</span>
                            <button
                              type="button"
                              aria-label={`移除文件：${file.name}`}
                              onClick={() => setAddFiles((current) => current.filter((item) => item.id !== file.id))}
                            >
                              <X size={13} aria-hidden="true" />
                            </button>
                          </li>
                        ))}</ul>
                      ) : <span>也可以把文件拖到这里</span>}
                    </div>
                  )}
                  {addError ? <div className="wg2-wh-error" role="alert"><TriangleAlert size={14} aria-hidden="true" />{addError}</div> : null}
                </div>
                <footer className="wg2-info-add__footer">
                  <button type="button" onClick={() => setAdding(false)}>取消</button>
                  <button type="button" className="is-primary" disabled={addBusy} onClick={() => void submitCustom()}>
                    {addBusy ? '添加中…' : '添加信息'}
                  </button>
                </footer>
              </article>
            </div>
          </section>
        </div>
      </section>
    );
  }

  if (!active || !spec) return <section className="wg2-info-entry">{summary}</section>;

  const taskTitle = taskById.get(active.taskId)?.title;
  const nextInput = pending.length > 0
    ? pending[(Math.max(activeIndex, 0) + 1) % pending.length]
    : undefined;
  const setAfterSubmit = (afterSubmit: AfterSubmit) => setPanel(workId, { afterSubmit });
  const toggleAdvanced = () => setPanel(workId, {
    advancedOpen: { ...panelState.advancedOpen, [active.id]: !advancedOpen },
  });

  return (
    <section className="wg2-info-entry" data-testid="work-information-panel">
      {summary}
      <div
        className="wg2-info-overlay"
        role="presentation"
        onMouseDown={(event) => {
          if (event.target !== event.currentTarget) return;
          setPanel(workId, { closed: true, activeInputId: undefined });
        }}
      >
        <section
          className="wg2-info-panel"
          role="dialog"
          aria-modal="true"
          aria-labelledby={`wg2-info-dialog-${active.id}`}
          onMouseDown={(event) => event.stopPropagation()}
        >
          <header className="wg2-info-panel__bar">
            <div>
              <ClipboardPenLine size={17} aria-hidden="true" />
              <strong id={`wg2-info-dialog-${active.id}`}>填写信息</strong>
              <span>{done}/{currentInputs.length} 已填写</span>
            </div>
            <button
              type="button"
              className="wg2-info-panel__close"
              onClick={() => setPanel(workId, { closed: true, activeInputId: undefined })}
              aria-label="关闭填写信息"
            >
              <X size={16} aria-hidden="true" />
            </button>
          </header>

          <div className="wg2-info-stack" data-stack-count={Math.min(stackInputs.length, 3)}>
        {stackInputs.length > 2 && (
          <div className="wg2-info-stack__back wg2-info-stack__back--2" aria-hidden="true">
            <span>{specs.get(stackInputs[(activeIndex + 2) % stackInputs.length].specId)?.label}</span>
          </div>
        )}
        {stackInputs.length > 1 && (
          <div className="wg2-info-stack__back wg2-info-stack__back--1" aria-hidden="true">
            <span>{specs.get(stackInputs[(activeIndex + 1) % stackInputs.length].specId)?.label}</span>
          </div>
        )}
        <article className="wg2-info-card">
          <div className="wg2-info-card__meta">
            <span>{editingDone ? '修改已填写信息' : `${taskTitle ? `${taskTitle} · ` : ''}${activeIndex + 1}/${stackInputs.length}`}</span>
            <span className="wg2-info-card__kind">{kindLabel(spec.kind)}</span>
          </div>
          <div className="wg2-info-card__heading">
            <div>
              <h3>{spec.label}</h3>
              {spec.description && <p>{spec.description}</p>}
            </div>
            <button
              type="button"
              className={advancedOpen ? 'is-open' : undefined}
              onClick={toggleAdvanced}
              aria-expanded={advancedOpen}
            >
              <Sparkles size={14} aria-hidden="true" />
              高级
              <ChevronDown size={14} aria-hidden="true" />
            </button>
          </div>

          <div
            ref={dropTargetRef}
            className={`wg2-info-card__control${dragOver ? ' is-drag-over' : ''}`}
            data-wg2-work-input-drop={spec.kind === 'file' ? 'true' : undefined}
            style={spec.kind === 'file' ? { '--wails-drop-target': 'drop' } as React.CSSProperties : undefined}
            onDragEnter={(event) => {
              if (spec.kind !== 'file') return;
              event.preventDefault();
              setDragOver(true);
            }}
            onDragOver={(event) => {
              if (spec.kind !== 'file') return;
              event.preventDefault();
            }}
            onDragLeave={() => setDragOver(false)}
            onDropCapture={(event) => {
              if (spec.kind !== 'file') return;
              event.preventDefault();
              event.stopPropagation();
            }}
            onDrop={(event) => {
              if (spec.kind !== 'file') return;
              event.preventDefault();
              setDragOver(false);
            }}
          >
            {spec.kind === 'file' && dragOver && (
              <div className="wg2-info-card__drop-cover">
                <FileUp size={22} aria-hidden="true" />
                松开即可添加文件
              </div>
            )}
            <WorkInputHost
              inputSpec={spec}
              workInput={active}
              draftValue={draft}
              onDraftChange={changeDraft}
              onSubmit={onSubmit ?? (async () => { throw new Error('当前无法提交'); })}
              onPin={onPin ?? (async () => { throw new Error('当前无法固定'); })}
              onUnpin={onUnpin ?? (async () => { throw new Error('当前无法取消固定'); })}
              onRefreshAuthoritative={onRefresh ?? (async () => {})}
              workId={workId}
              taskId={active.taskId}
              runId={active.runId}
              blockId={active.blockId}
              definitionRevision={definition.revision}
              inputRevision={active.revision}
              workRevision={workRevision}
              disabled={!canEdit}
              committedRequestIds={{
                submit: card?.committedRequestIds[`${key}\u0000submit`],
              }}
              onRequestCommitted={(operation, requestId) =>
                setCommittedRequestId(workId, `${key}\u0000${operation}`, requestId)}
              onSelectFile={() => selectFile()}
              hideHeader
              extra={extra}
              submitLabel={editingDone ? '保存修改' : panelState.afterSubmit === 'next' ? '保存并继续' : '保存'}
              onSubmitCommitted={() => {
                if (editingDone || panelState.afterSubmit === 'close' || pending.length === 1 || !nextInput) {
                  setPanel(workId, { closed: true, activeInputId: undefined });
                  return;
                }
                setPanel(workId, { activeInputId: nextInput.id });
              }}
            />
            {dropError && (
              <div className="wg2-wh-error" role="alert">
                <TriangleAlert className="wg2-wh-error-icon" size={14} aria-hidden="true" />
                <span>{dropError}</span>
              </div>
            )}
          </div>
          {!canEdit && !readonly && (
            <p className="wg2-info-card__unavailable" role="status" data-testid="work-information-unavailable">
              填写服务暂不可用，内容已保留，请稍后重试。
            </p>
          )}

          {advancedOpen && (
            <div className="wg2-info-card__advanced">
              <label htmlFor={`wg2-info-extra-${active.id}`}>补充说明</label>
              <textarea
                id={`wg2-info-extra-${active.id}`}
                value={extra}
                rows={3}
                disabled={!canEdit}
                placeholder="可填写预期之外但有帮助的信息"
                onChange={(event) => changeExtra(event.currentTarget.value)}
              />
            </div>
          )}

          <footer className="wg2-info-card__footer">
            {editingDone ? (
              <span>保存后返回工作信息</span>
            ) : <div className="wg2-info-card__mode" role="radiogroup" aria-label="填写完成后的操作">
              <button
                type="button"
                role="radio"
                aria-checked={panelState.afterSubmit === 'next'}
                className={panelState.afterSubmit === 'next' ? 'is-selected' : undefined}
                onClick={() => setAfterSubmit('next')}
              >
                填完后继续下一项
              </button>
              <button
                type="button"
                role="radio"
                aria-checked={panelState.afterSubmit === 'close'}
                className={panelState.afterSubmit === 'close' ? 'is-selected' : undefined}
                onClick={() => setAfterSubmit('close')}
              >
                填完后先关闭
              </button>
            </div>}
            {!editingDone ? <span>{pending.length > 1 && nextInput ? `下一项：${specs.get(nextInput.specId)?.label ?? '待填写信息'}` : '这是最后一项'}</span> : null}
          </footer>
            </article>
          </div>
        </section>
      </div>
    </section>
  );
};
