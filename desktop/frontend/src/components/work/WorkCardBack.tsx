import React, { useEffect, useMemo, useState, type ReactNode } from 'react';

import type { RunSelection, SessionRef, SessionSurfaceContext, WorkView } from '../../work/types';
import { attemptKey, resolveSelection, stageKey, taskKey } from '../../work/store';

export interface WorkCardBackSlotProps {
  workID: string;
  prompt: string;
  readonly: boolean;
  archived: boolean;
  draft: string;
  onDraftChange: (draft: string) => void;
}

export type WorkCardBackSlot = ReactNode | ((props: WorkCardBackSlotProps) => ReactNode);

/**
 * Existing session surfaces are passed through this adapter. Callers should
 * provide the current Transcript, Run/Approval/Ask, ArtifactShelf, Queue and
 * Composer nodes instead of creating a second conversation implementation.
 */
export interface WorkCardBackSlots {
  /** Full production Session surface for attempts without a SessionRef. */
  surface?: WorkCardBackSlot;
  transcript?: WorkCardBackSlot;
  runApproval?: WorkCardBackSlot;
  artifactShelf?: WorkCardBackSlot;
  queue?: WorkCardBackSlot;
  composer?: WorkCardBackSlot;
}

export interface WorkCardBackProps {
  view: WorkView;
  draft: string;
  onDraftChange: (draft: string) => void;
  readonly: boolean;
  archived: boolean;
  slots?: WorkCardBackSlots;
  selection?: RunSelection;
  resolveSessionSurface?: (sessionRef: SessionRef, context: SessionSurfaceContext) => ReactNode;
  onSaveDraft?: (input: { name: string; prompt: string; inputs: Record<string, unknown> }) => Promise<void>;
}

function renderSlot(slot: WorkCardBackSlot | undefined, props: WorkCardBackSlotProps): ReactNode {
  return typeof slot === 'function' ? slot(props) : slot;
}

export const WorkCardBack: React.FC<WorkCardBackProps> = ({
  view,
  draft,
  onDraftChange,
  readonly,
  archived,
  slots,
  selection,
  resolveSessionSurface,
  onSaveDraft,
}) => {
  const { work } = view;
  const [name, setName] = useState(work.name);
  const [prompt, setPrompt] = useState(work.prompt);
  const [inputsText, setInputsText] = useState(() => JSON.stringify(work.inputs ?? {}, null, 2));
  const [saveState, setSaveState] = useState<'idle' | 'saving' | 'saved'>('idle');
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    setName(work.name);
    setPrompt(work.prompt);
    setInputsText(JSON.stringify(work.inputs ?? {}, null, 2));
    setSaveState('idle');
    setSaveError(null);
  }, [view.revision, work.inputs, work.name, work.prompt]);

  const saveDraft = async () => {
    if (!onSaveDraft || readonly || archived || saveState === 'saving') return;
    let inputs: Record<string, unknown>;
    try {
      const parsed = JSON.parse(inputsText || '{}') as unknown;
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('Inputs 必须是 JSON 对象');
      inputs = parsed as Record<string, unknown>;
    } catch (error) {
      setSaveError(error instanceof Error ? error.message : String(error));
      return;
    }
    setSaveState('saving');
    setSaveError(null);
    try {
      await onSaveDraft({ name: name.trim(), prompt, inputs });
      setSaveState('saved');
    } catch (error) {
      setSaveState('idle');
      setSaveError(error instanceof Error ? error.message : String(error));
    }
  };
  const slotProps = useMemo<WorkCardBackSlotProps>(() => ({
    workID: work.id,
    prompt: work.prompt,
    readonly,
    archived,
    draft,
    onDraftChange,
  }), [archived, draft, onDraftChange, readonly, work.id, work.prompt]);

  // Resolve the selected attempt's session surface.
  const selectedSession = useMemo(() => {
    if (!selection || !resolveSessionSurface) return null;
    const resolved = resolveSelection(work, selection);
    if (!resolved?.stage || !resolved.task || !resolved.attempt?.sessionRef) return null;
    const sessionRef = resolved.attempt.sessionRef;
    const context: SessionSurfaceContext = {
      workId: work.id,
      runId: resolved.run.id,
      stageId: stageKey(resolved.stage),
      taskId: taskKey(resolved.task),
      attemptId: resolved.attempt.id,
      attemptIndex: resolved.attempt.index,
      sessionRef,
      readonly,
      archived,
    };
    return {
      key: `${sessionRef.sessionPath}\u0000${sessionRef.branchId}`,
      targetID: `attempt:${context.runId}:${context.stageId}:${context.taskId}:${attemptKey(resolved.attempt)}`,
      node: resolveSessionSurface(sessionRef, context),
    };
  }, [archived, readonly, resolveSessionSurface, selection, work]);

  return (
    <div
      className="wg2-work-card-back"
      data-testid="work-card-back"
      data-work-id={work.id}
      data-readonly={readonly ? 'true' : 'false'}
      data-archived={archived ? 'true' : 'false'}
    >
      <div className="wg2-work-back-header">
        <h2 className="wg2-work-name">{work.name}</h2>
      </div>

      {!readonly && !archived && (
        <section className="wg2-work-draft-editor" data-testid="work-draft-editor">
          <label>
            名称
            <input value={name} maxLength={120} onChange={(event) => { setName(event.target.value); setSaveState('idle'); }} />
          </label>
          <label>
            Prompt
            <textarea
              data-testid="work-prompt-editor"
              value={prompt}
              rows={6}
              placeholder="请输入要执行的任务；空 Prompt 无法运行。"
              onChange={(event) => { setPrompt(event.target.value); onDraftChange(event.target.value); setSaveState('idle'); }}
            />
          </label>
          <label>
            Inputs（JSON）
            <textarea value={inputsText} rows={4} onChange={(event) => { setInputsText(event.target.value); setSaveState('idle'); }} />
          </label>
          <div>
            <button type="button" data-testid="work-save-draft" onClick={() => void saveDraft()} disabled={!name.trim() || saveState === 'saving'}>
              {saveState === 'saving' ? '保存中…' : '保存草稿'}
            </button>
            {saveState === 'saved' && <span role="status">已保存</span>}
            {saveError && <span role="alert">{saveError}</span>}
          </div>
        </section>
      )}

      {selectedSession !== null ? (
        <div
          key={selectedSession.key}
          className="wg2-work-back-selected-session"
          data-testid="work-back-selected-session"
          data-work-target-id={selectedSession.targetID}
        >
          {selectedSession.node ?? (
            <div className="wg2-work-session-unavailable" role="alert">目标 Session 暂不可用。</div>
          )}
        </div>
      ) : slots?.surface ? (
        <div className="wg2-work-back-slots" data-testid="work-session-surfaces">
          {renderSlot(slots.surface, slotProps)}
        </div>
      ) : slots?.transcript ? (
        <div className="wg2-work-back-slots" data-testid="work-session-surfaces">
          <div className="wg2-work-back-slot wg2-work-back-transcript" data-testid="work-back-slot-transcript">
            {renderSlot(slots.transcript, slotProps)}
          </div>
          {slots.runApproval && (
            <div className="wg2-work-back-slot wg2-work-back-run-approval" data-testid="work-back-slot-run-approval">
              {renderSlot(slots.runApproval, slotProps)}
            </div>
          )}
          {slots.artifactShelf && (
            <div className="wg2-work-back-slot wg2-work-back-artifact-shelf" data-testid="work-back-slot-artifact-shelf">
              {renderSlot(slots.artifactShelf, slotProps)}
            </div>
          )}
          {slots.queue && (
            <div className="wg2-work-back-slot wg2-work-back-queue" data-testid="work-back-slot-queue">
              {renderSlot(slots.queue, slotProps)}
            </div>
          )}
          {!readonly && !archived && slots.composer && (
            <div className="wg2-work-back-slot wg2-work-back-composer" data-testid="work-back-slot-composer">
              {renderSlot(slots.composer, slotProps)}
            </div>
          )}
        </div>
      ) : (
        <div className="wg2-work-session-unavailable" role="status" data-testid="work-session-unavailable">
          关联会话界面尚未载入，可继续查看 Work 概览。
        </div>
      )}

      {(readonly || archived) && (
        <div className="wg2-work-back-readonly-notice" data-testid="work-back-readonly-notice">
          {archived ? '此 Work 已归档，不可编辑或运行。' : '此 Work 为只读模式。'}
        </div>
      )}
    </div>
  );
};
