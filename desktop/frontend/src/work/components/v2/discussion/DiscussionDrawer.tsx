import React, { useCallback, useLayoutEffect, useRef } from 'react';
import { createPortal } from 'react-dom';

import type {
  ApplyWorkPatchRequest,
  ApplyWorkPatchResult,
  PatchScope,
  PreviewWorkPatchRequest,
  WorkPatchPreview,
} from '../../../types_v2';
import type { PatchPreviewLabelResolver, PatchPreviewOrderResolver } from './PatchPreview';

export type DiscussionPreviewIntent = PreviewWorkPatchRequest;
export type DiscussionApplyIntent = ApplyWorkPatchRequest;

export interface DiscussionCloseIntent {
  workId: string;
  taskId: string;
  blockId: string;
}

export interface DiscussionDraftIntent {
  workId: string;
  taskId: string;
  blockId: string;
  text: string;
}

function hashIntentKey(key: string): string {
  let hash = 0;
  for (let i = 0; i < key.length; i++) {
    hash = ((hash << 5) - hash) + key.charCodeAt(i);
    hash |= 0;
  }
  return (hash >>> 0).toString(36);
}

function requestIdAfterCommit(
  intentKey: string,
  committedRequestId: string | undefined,
): string {
  const prefix = `disc-coordinate-${hashIntentKey(intentKey)}-a`;
  if (!committedRequestId?.startsWith(prefix)) return `${prefix}0`;
  const sequence = Number.parseInt(committedRequestId.slice(prefix.length), 36);
  return `${prefix}${(Number.isSafeInteger(sequence) ? sequence + 1 : 1).toString(36)}`;
}

export interface DiscussionDrawerProps {
  workId: string;
  taskId: string;
  blockId: string;
  runId: string;
  sessionId: string;
  workRevision: number;
  definitionRevision: number;
  blockRevision: number;
  taskTitle: string;
  resolvePatchLabel?: PatchPreviewLabelResolver;
  resolvePatchOrder?: PatchPreviewOrderResolver;
  draftText: string;
  onDraftChange: (intent: DiscussionDraftIntent) => void;
  patchPreview: WorkPatchPreview | null;
  isPreviewing: boolean;
  previewError: string | null;
  isApplying: boolean;
  applyResult: ApplyWorkPatchResult | null;
  applyError: string | null;
  selectedScope: PatchScope;
  onScopeChange: (scope: PatchScope) => void;
  scopeLocked?: boolean;
  revisionConflict: boolean;
  digestConflict: boolean;
  onClose: (intent: DiscussionCloseIntent) => void;
  /** Submits the user's intent to the automatic preview → apply → refresh coordinator. */
  onPreview: (intent: DiscussionPreviewIntent) => void;
  /** Retained as an integration-compatible prop; applying is now automatic. */
  onApply: (intent: DiscussionApplyIntent) => void;
  onDismissResult: () => void;
  committedPreviewRequestId?: string;
  committedApplyRequestId?: string;
  previewAvailable?: boolean;
  applyAvailable?: boolean;
  returnFocusRef?: React.RefObject<HTMLElement | null>;
}

export const DiscussionDrawer: React.FC<DiscussionDrawerProps> = ({
  workId,
  taskId,
  blockId,
  runId,
  sessionId,
  definitionRevision,
  blockRevision,
  taskTitle,
  draftText,
  onDraftChange,
  isPreviewing,
  previewError,
  isApplying,
  applyResult,
  applyError,
  selectedScope,
  onScopeChange,
  scopeLocked = false,
  onClose,
  onPreview,
  onDismissResult,
  committedPreviewRequestId,
  previewAvailable = true,
  applyAvailable = true,
  returnFocusRef,
}) => {
  const drawerRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const capturedActiveRef = useRef<Element | null>(null);
  const closeRequestedRef = useRef(false);
  const submitRef = useRef<{ key: string; requestId: string } | null>(null);
  const busy = isPreviewing || isApplying;
  const available = previewAvailable && applyAvailable;
  const intentKey = JSON.stringify([
    workId,
    taskId,
    blockId,
    runId,
    sessionId,
    draftText.trim(),
    definitionRevision,
    blockRevision,
    selectedScope,
  ]);

  useLayoutEffect(() => {
    capturedActiveRef.current = document.activeElement;
    textareaRef.current?.focus();
    return () => {
      if (!closeRequestedRef.current) {
        textareaRef.current?.blur();
        return;
      }
      const target = returnFocusRef?.current ?? capturedActiveRef.current;
      if (target instanceof HTMLElement && target.isConnected) target.focus();
    };
  }, [returnFocusRef]);

  const requestClose = useCallback(() => {
    closeRequestedRef.current = true;
    onClose({ workId, taskId, blockId });
  }, [blockId, onClose, taskId, workId]);

  const submit = useCallback(() => {
    const instruction = draftText.trim();
    if (!available || !instruction || busy) return;
    if (
      submitRef.current?.key !== intentKey
      || submitRef.current.requestId === committedPreviewRequestId
    ) {
      submitRef.current = {
        key: intentKey,
        requestId: requestIdAfterCommit(intentKey, committedPreviewRequestId),
      };
    }
    onPreview({
      workId,
      taskId,
      blockId,
      runId,
      sessionId,
      instruction,
      definitionRevision,
      blockRevision,
      scope: selectedScope,
      requestId: submitRef.current.requestId,
    });
  }, [
    available, blockId, blockRevision, busy, committedPreviewRequestId,
    definitionRevision, draftText, intentKey, onPreview, runId, selectedScope,
    sessionId, taskId, workId,
  ]);

  const handleKeyDown = useCallback((event: React.KeyboardEvent) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      requestClose();
      return;
    }
    if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
      event.preventDefault();
      submit();
      return;
    }
    if (event.key !== 'Tab' || !drawerRef.current) return;
    const focusable = drawerRef.current.querySelectorAll<HTMLElement>(
      'button:not([disabled]), textarea:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
    );
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }, [requestClose, submit]);

  const error = applyError ?? previewError;
  const committedRecovery = Boolean(
    applyResult?.committed && (applyResult.error || applyResult.transportError),
  );

  return createPortal(
    <div
      className="wg2-dd-backdrop"
      data-testid={`discussion-backdrop-${taskId}`}
      onClick={(event) => {
        if (event.target === event.currentTarget && !busy) requestClose();
      }}
    >
      <div
        ref={drawerRef}
        id={`discussion-drawer-${taskId}`}
        className="wg2-dd-drawer"
        data-testid={`discussion-drawer-${taskId}`}
        role="dialog"
        aria-label={`${taskTitle} 修改意见`}
        aria-modal
        onKeyDown={handleKeyDown}
      >
        <div className="wg2-dd-header">
          <span className="wg2-dd-header-title" data-testid={`discussion-header-${taskId}`}>
            调整：{taskTitle}
          </span>
          <button
            type="button"
            className="wg2-dd-btn wg2-dd-btn-close"
            onClick={requestClose}
            disabled={busy}
            aria-label="关闭讨论"
            data-testid={`discussion-close-${taskId}`}
          >
            ✕
          </button>
        </div>

        <div className="wg2-dd-body">
          <label htmlFor={`discussion-input-${taskId}`} className="wg2-dd-label">
            你希望怎么调整？
          </label>
          <textarea
            ref={textareaRef}
            id={`discussion-input-${taskId}`}
            className="wg2-dd-textarea"
            value={draftText}
            disabled={busy}
            onInput={(event) => onDraftChange({
              workId,
              taskId,
              blockId,
              text: event.currentTarget.value,
            })}
            placeholder="描述目标或要求即可，AI 会判断需要更新哪些内容和后续步骤"
            rows={4}
            aria-describedby={`discussion-hint-${taskId}`}
            data-testid={`discussion-input-${taskId}`}
          />
          <span id={`discussion-hint-${taskId}`} className="wg2-dd-hint">
            提交后会自动分析、更新并继续执行。Ctrl+Enter 快速提交。
          </span>

          <fieldset className="wg2-dd-scope" disabled={busy} data-testid={`discussion-scope-${taskId}`}>
            <legend className="wg2-dd-label">调整范围</legend>
            {scopeLocked ? (
              <p className="wg2-dd-scope-note">AI 会同步更新相关任务，并继续处理受影响的后续步骤。</p>
            ) : (
              <>
                <label className="wg2-dd-radio">
                  <input
                    type="radio"
                    name={`discussion-scope-${taskId}`}
                    value="block"
                    checked={selectedScope === 'block'}
                    onChange={() => onScopeChange('block')}
                    data-testid={`discussion-scope-block-${taskId}`}
                  />
                  <span>只调整当前内容</span>
                </label>
                <label className="wg2-dd-radio">
                  <input
                    type="radio"
                    name={`discussion-scope-${taskId}`}
                    value="workflow"
                    checked={selectedScope === 'workflow'}
                    onChange={() => onScopeChange('workflow')}
                    data-testid={`discussion-scope-workflow-${taskId}`}
                  />
                  <span>同步调整后续工作</span>
                </label>
              </>
            )}
          </fieldset>

          <div className="wg2-dd-actions">
            <button
              type="button"
              className="wg2-dd-btn wg2-dd-btn-apply"
              disabled={!available || !draftText.trim() || busy}
              onClick={submit}
              data-testid={`discussion-preview-btn-${taskId}`}
            >
              {busy ? 'AI 正在协调更新…' : '提交修改'}
            </button>
          </div>

          {!available && (
            <div className="wg2-dd-error" role="status" data-testid={`discussion-capability-${taskId}`}>
              当前环境暂时无法更新这个工作。
            </div>
          )}
          {busy && (
            <div className="wg2-dd-result wg2-dd-result-info" role="status" aria-live="polite">
              <span className="wg2-dd-result-icon" aria-hidden="true">↻</span>
              <span className="wg2-dd-result-msg">正在理解你的要求，并安排需要更新的内容与后续步骤…</span>
            </div>
          )}
          {error && !busy && (
            <div className="wg2-dd-error" role="alert" data-testid={`discussion-error-${taskId}`}>
              <span className="wg2-dd-error-icon" aria-hidden="true">⚠</span>
              <span className="wg2-dd-error-msg">{error}</span>
            </div>
          )}
        </div>

        {applyResult && !busy && (
          <div
            className={committedRecovery
              ? 'wg2-dd-result wg2-dd-result-info'
              : applyResult.error
                ? 'wg2-dd-result wg2-dd-result-error'
                : 'wg2-dd-result wg2-dd-result-ok'}
            role={applyResult.error ? 'alert' : 'status'}
            aria-live="polite"
            data-testid={`discussion-result-${taskId}`}
          >
            <span className="wg2-dd-result-icon" aria-hidden="true">
              {applyResult.error ? '✗' : committedRecovery ? '↻' : '✓'}
            </span>
            <span className="wg2-dd-result-msg">
              {applyResult.error
                ? applyResult.error
                : committedRecovery
                  ? '修改已提交，正在同步最新状态。'
                  : '修改已完成，AI 会继续处理受影响的工作。'}
            </span>
            <button
              type="button"
              className="wg2-dd-btn wg2-dd-btn-close-result"
              onClick={onDismissResult}
              aria-label="关闭结果"
              data-testid={`discussion-dismiss-result-${taskId}`}
            >
              知道了
            </button>
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
};
