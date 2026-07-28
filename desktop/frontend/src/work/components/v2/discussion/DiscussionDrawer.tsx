import React, { useCallback, useLayoutEffect, useRef } from 'react';
import { createPortal } from 'react-dom';

import type {
  ApplyWorkPatchRequest,
  ApplyWorkPatchResult,
  PatchScope,
  PreviewWorkPatchRequest,
  WorkPatchPreview,
} from '../../../types_v2';
import { PatchPreview } from './PatchPreview';
import type { PatchPreviewLabelResolver, PatchPreviewOrderResolver } from './PatchPreview';

// ── Typed intents ──────────────────────────────────────────────────────────

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

/** Stable hash of intent key for cross-process requestID reuse. */
function hashIntentKey(key: string): string {
  let hash = 0;
  for (let i = 0; i < key.length; i++) {
    const ch = key.charCodeAt(i);
    hash = ((hash << 5) - hash) + ch;
    hash |= 0; // Convert to 32bit integer
  }
  return (hash >>> 0).toString(36);
}

function requestIdAfterCommit(
  kind: 'preview' | 'apply',
  intentKey: string,
  committedRequestId: string | undefined,
): string {
  const prefix = `disc-${kind}-${hashIntentKey(intentKey)}-a`;
  if (!committedRequestId?.startsWith(prefix)) return `${prefix}0`;
  const sequence = Number.parseInt(committedRequestId.slice(prefix.length), 36);
  return `${prefix}${(Number.isSafeInteger(sequence) ? sequence + 1 : 1).toString(36)}`;
}

function isPreviewExpired(preview: WorkPatchPreview): boolean {
  const expiresAt = Date.parse(preview.expiresAt);
  return Number.isFinite(expiresAt) && expiresAt <= Date.now();
}

// ── DiscussionDrawer ───────────────────────────────────────────────────────

export interface DiscussionDrawerProps {
  /** Identity of the block this discussion is attached to. */
  workId: string;
  taskId: string;
  blockId: string;
  runId: string;
  sessionId: string;

  /** Revisions for conflict detection. */
  workRevision: number;
  definitionRevision: number;
  blockRevision: number;

  /** Task title for context display. */
  taskTitle: string;
  resolvePatchLabel?: PatchPreviewLabelResolver;
  resolvePatchOrder?: PatchPreviewOrderResolver;

  // ── Draft state (parent-managed, survives close/reopen/block switch) ──
  draftText: string;
  onDraftChange: (intent: DiscussionDraftIntent) => void;

  // ── Preview state ──────────────────────────────────────────────────
  patchPreview: WorkPatchPreview | null;
  isPreviewing: boolean;
  previewError: string | null;

  // ── Apply state ────────────────────────────────────────────────────
  isApplying: boolean;
  applyResult: ApplyWorkPatchResult | null;
  applyError: string | null;

  // ── Scope selection ────────────────────────────────────────────────
  selectedScope: PatchScope;
  onScopeChange: (scope: PatchScope) => void;
  scopeLocked?: boolean;

  // ── Conflict detection ─────────────────────────────────────────────
  /** True when base revision has changed since last preview — user must re-preview. */
  revisionConflict: boolean;
  /** True when preview digest doesn't match the applied digest. */
  digestConflict: boolean;

  // ── Intent callbacks ───────────────────────────────────────────────
  onClose: (intent: DiscussionCloseIntent) => void;
  onPreview: (intent: DiscussionPreviewIntent) => void;
  onApply: (intent: DiscussionApplyIntent) => void;
  onDismissResult: () => void;

  /** Previously committed IDs for this full identity. */
  committedPreviewRequestId?: string;
  committedApplyRequestId?: string;
  previewAvailable?: boolean;
  applyAvailable?: boolean;

  /** Optional: element ref to return focus to on close. */
  returnFocusRef?: React.RefObject<HTMLElement | null>;
}

export const DiscussionDrawer: React.FC<DiscussionDrawerProps> = ({
  workId,
  taskId,
  blockId,
  runId,
  sessionId,
  workRevision,
  definitionRevision,
  blockRevision,
  taskTitle,
  resolvePatchLabel,
  resolvePatchOrder,
  draftText,
  onDraftChange,
  patchPreview,
  isPreviewing,
  previewError,
  isApplying,
  applyResult,
  applyError,
  selectedScope,
  onScopeChange,
  scopeLocked = false,
  revisionConflict,
  digestConflict,
  onClose,
  onPreview,
  onApply,
  onDismissResult,
  returnFocusRef,
  committedPreviewRequestId,
  committedApplyRequestId,
  previewAvailable = true,
  applyAvailable = true,
}) => {
  const drawerRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const capturedActiveRef = useRef<Element | null>(null);
  const closeRequestedRef = useRef(false);
  const previewAttemptRef = useRef<{ key: string; requestId: string } | null>(null);
  const applyAttemptRef = useRef<{
    key: string;
    requestId: string;
    expectedRevision: number;
  } | null>(null);
  const previewIntentKey = JSON.stringify([
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

  const activePreview =
    patchPreview?.workId === workId
    && patchPreview.taskId === taskId
    && patchPreview.blockId === blockId
    && patchPreview.runId === runId
    && patchPreview.sessionId === sessionId
      ? patchPreview
      : null;

  const previewExpired = activePreview !== null && isPreviewExpired(activePreview);
  const baseRevisionConflict =
    activePreview !== null
    && (
      activePreview.baseDefinitionRev !== definitionRevision
      || (activePreview.scope === 'block' && activePreview.baseBlockRev !== blockRevision)
    );
  const scopeConflict = activePreview !== null && activePreview.scope !== selectedScope;
  const instructionConflict =
    activePreview !== null
    && previewAttemptRef.current !== null
    && previewAttemptRef.current.key !== previewIntentKey;
  const hasRevisionConflict = revisionConflict || baseRevisionConflict;
  const hasConflict =
    hasRevisionConflict
    || digestConflict
    || previewExpired
    || scopeConflict
    || instructionConflict;

  // Focus textarea on mount; capture activeElement for restoration on close.
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
  }, [workId, taskId, blockId, onClose]);

  const firePreview = useCallback((reuseAttempt = false) => {
    const instruction = draftText.trim();
    if (instruction.length === 0 || isPreviewing) return;

    const key = previewIntentKey;
    if (
      !reuseAttempt
      || previewAttemptRef.current?.key !== key
      || previewAttemptRef.current?.requestId === committedPreviewRequestId
    ) {
      previewAttemptRef.current = {
        key,
        requestId: requestIdAfterCommit('preview', key, committedPreviewRequestId),
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
      requestId: previewAttemptRef.current.requestId,
    });
  }, [
    draftText,
    isPreviewing,
    workId,
    taskId,
    blockId,
    runId,
    sessionId,
    definitionRevision,
    blockRevision,
    selectedScope,
    previewIntentKey,
    committedPreviewRequestId,
    onPreview,
  ]);

  const fireApply = useCallback(() => {
    if (!activePreview || hasConflict || isApplying || applyResult?.committed) return;
    const key = JSON.stringify([
      workId,
      activePreview.id,
      activePreview.digest,
      activePreview.scope,
    ]);
    if (
      applyAttemptRef.current?.key !== key
      || applyAttemptRef.current?.requestId === committedApplyRequestId
    ) {
      applyAttemptRef.current = {
        key,
        requestId: requestIdAfterCommit('apply', key, committedApplyRequestId),
        expectedRevision: workRevision,
      };
    }
    onApply({
      workId,
      patchId: activePreview.id,
      previewDigest: activePreview.digest,
      scope: activePreview.scope,
      expectedRevision: applyAttemptRef.current.expectedRevision,
      requestId: applyAttemptRef.current.requestId,
    });
  }, [
    activePreview,
    hasConflict,
    isApplying,
    applyResult,
    workId,
    taskId,
    workRevision,
    committedApplyRequestId,
    onApply,
  ]);

  // Trap focus within drawer (Tab / Shift+Tab cycling)
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        requestClose();
        return;
      }

      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter' && draftText.trim().length > 0) {
        e.preventDefault();
        firePreview(false);
      }

      if (e.key === 'Tab' && drawerRef.current) {
        const focusable = drawerRef.current.querySelectorAll<HTMLElement>(
          'button:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
        );
        if (focusable.length === 0) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    },
    [draftText, requestClose, firePreview],
  );

  const hasPreview = activePreview !== null;
  const canPreview = previewAvailable && draftText.trim().length > 0 && !isPreviewing && !isApplying;
  const canApply = applyAvailable && hasPreview && !isApplying && !hasConflict && !applyResult?.committed;
  const showApplyResult = applyResult !== null;
  const committedRecovery = Boolean(
    applyResult?.committed && (applyResult.error || applyResult.transportError),
  );

  const handleBackdropClick = useCallback((e: React.MouseEvent) => {
    if (e.target === e.currentTarget) {
      requestClose();
    }
  }, [requestClose]);

  const dialog = (
    <div
      ref={drawerRef}
      id={`discussion-drawer-${taskId}`}
      className="wg2-dd-drawer"
      data-testid={`discussion-drawer-${taskId}`}
      role="dialog"
      aria-label={`${taskTitle} 修改意见`}
      aria-modal={true}
      onKeyDown={handleKeyDown}
    >
      {/* ── Header ────────────────────────────────────────────────── */}
      <div className="wg2-dd-header">
        <span className="wg2-dd-header-title" data-testid={`discussion-header-${taskId}`}>
          修改意见：{taskTitle}
        </span>
        <button
          type="button"
          className="wg2-dd-btn wg2-dd-btn-close"
          onClick={requestClose}
          aria-label="关闭讨论"
          data-testid={`discussion-close-${taskId}`}
        >
          ✕
        </button>
      </div>

      {/* ── Instruction textarea ───────────────────────────────────── */}
      <div className="wg2-dd-body">
        <label htmlFor={`discussion-input-${taskId}`} className="wg2-dd-label">
          输入改进、指导或意见
        </label>
        <textarea
          ref={textareaRef}
          id={`discussion-input-${taskId}`}
          className="wg2-dd-textarea"
          value={draftText}
          onInput={(e) => onDraftChange({
            workId,
            taskId,
            blockId,
            text: e.currentTarget.value,
          })}
          placeholder="例如：把这个节点标题改成「逻辑与安全审查」"
          rows={3}
          aria-describedby={`discussion-hint-${taskId}`}
          data-testid={`discussion-input-${taskId}`}
        />
        <span id={`discussion-hint-${taskId}`} className="wg2-dd-hint">
          Ctrl+Enter 预览影响
        </span>
        {(!previewAvailable || !applyAvailable) && (
          <div className="wg2-dd-error" role="status" data-testid={`discussion-capability-${taskId}`}>
            当前环境无法应用讨论改动。
          </div>
        )}

        {/* ── Scope selector ─────────────────────────────────────── */}
        <fieldset className="wg2-dd-scope" data-testid={`discussion-scope-${taskId}`}>
          <legend className="wg2-dd-label">作用范围</legend>
          {scopeLocked ? (
            <p className="wg2-dd-scope-note">这是流程结构变更，会更新相关任务并重跑受影响的后续任务。</p>
          ) : <><label className="wg2-dd-radio">
            <input
              type="radio"
              name={`discussion-scope-${taskId}`}
              value="block"
              checked={selectedScope === 'block'}
              onChange={() => onScopeChange('block')}
              data-testid={`discussion-scope-block-${taskId}`}
            />
            <span>仅更新当前内容，不重跑后续任务</span>
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
            <span>影响当前项及后续任务（推荐）</span>
          </label>
          </>}
        </fieldset>

        {/* ── Action buttons ─────────────────────────────────────── */}
        <div className="wg2-dd-actions">
          <button
            type="button"
            className="wg2-dd-btn wg2-dd-btn-preview"
            disabled={!canPreview}
            onClick={() => firePreview(false)}
            data-testid={`discussion-preview-btn-${taskId}`}
          >
            {isPreviewing ? '分析影响中…' : hasPreview ? '重新预览' : '预览影响'}
          </button>
          {hasPreview && (
            <button
              type="button"
              className="wg2-dd-btn wg2-dd-btn-apply"
              disabled={!canApply}
              onClick={fireApply}
              data-testid={`discussion-apply-btn-${taskId}`}
            >
              {isApplying ? '正在应用…' : activePreview?.scope === 'workflow' ? '确认修改并更新后续步骤' : '确认并应用修改'}
            </button>
          )}
        </div>

        {/* ── Error banner ───────────────────────────────────────── */}
        {(previewError || applyError) && (
          <div
            className="wg2-dd-error"
            role="alert"
            data-testid={`discussion-error-${taskId}`}
          >
            <span className="wg2-dd-error-icon" aria-hidden="true">⚠</span>
            <span className="wg2-dd-error-msg">
              {applyError ?? previewError}
            </span>
            {previewError && (
              <button
                type="button"
                className="wg2-dd-btn wg2-dd-btn-retry"
                onClick={() => firePreview(true)}
                data-testid={`discussion-retry-preview-${taskId}`}
              >
                重试
              </button>
            )}
          </div>
        )}

        {/* ── Conflict banner ────────────────────────────────────── */}
        {hasConflict && (
          <div
            className="wg2-dd-conflict"
            role="alert"
            data-testid={`discussion-conflict-${taskId}`}
          >
            <span className="wg2-dd-error-icon" aria-hidden="true">↻</span>
            <span className="wg2-dd-error-msg">
              {hasRevisionConflict
                ? '工作内容已变化，请重新预览影响。'
                : scopeConflict
                  ? '作用范围已变化，请重新预览影响。'
                  : instructionConflict
                    ? '修改意见已变化，请重新预览影响。'
                  : digestConflict
                    ? '改动内容已变化，请重新预览影响。'
                    : '改动校验已过期，请重新预览影响。'}
            </span>
            <button
              type="button"
              className="wg2-dd-btn wg2-dd-btn-retry"
              onClick={() => firePreview(false)}
              data-testid={`discussion-repreview-${taskId}`}
            >
              重新预览
            </button>
          </div>
        )}
      </div>

      {activePreview && (
        <PatchPreview
          patch={activePreview}
          taskTitle={taskTitle}
          taskId={taskId}
          resolveLabel={resolvePatchLabel}
          resolveOrder={resolvePatchOrder}
        />
      )}

      {/* ── Apply result banner ─────────────────────────────────────── */}
      {showApplyResult && (
        <div
          className={
            committedRecovery
              ? 'wg2-dd-result wg2-dd-result-info'
              : applyResult!.error
                ? 'wg2-dd-result wg2-dd-result-error'
                : applyResult!.duplicate
                  ? 'wg2-dd-result wg2-dd-result-info'
                  : 'wg2-dd-result wg2-dd-result-ok'
          }
          role={applyResult!.error ? 'alert' : 'status'}
          aria-live="polite"
          data-testid={`discussion-result-${taskId}`}
        >
          {committedRecovery ? (
            <>
              <span className="wg2-dd-result-icon" aria-hidden="true">↻</span>
              <span className="wg2-dd-result-msg">
                改动已提交，正在恢复确认：
                {applyResult!.transportError?.message ?? applyResult!.error}
              </span>
            </>
          ) : applyResult!.error ? (
            <>
              <span className="wg2-dd-result-icon" aria-hidden="true">✗</span>
              <span className="wg2-dd-result-msg">
                应用失败：{applyResult!.error}
                {applyResult!.recoverable && '（可重试）'}
              </span>
            </>
          ) : applyResult!.duplicate ? (
            <>
              <span className="wg2-dd-result-icon" aria-hidden="true">ⓘ</span>
              <span className="wg2-dd-result-msg">修改已经应用，无需重复处理。</span>
            </>
          ) : (
            <>
              <span className="wg2-dd-result-icon" aria-hidden="true">✓</span>
              <span className="wg2-dd-result-msg">
                修改已应用。
                {applyResult!.requiresRerun
                  ? `AI 正在按新要求重新处理 ${applyResult!.invalidatedTaskIds?.length ?? 0} 个步骤。`
                  : ''}
              </span>
            </>
          )}
          <button
            type="button"
            className="wg2-dd-btn wg2-dd-btn-close-result"
            onClick={onDismissResult}
            aria-label="关闭结果"
            data-testid={`discussion-dismiss-result-${taskId}`}
          >
            关闭
          </button>
        </div>
      )}
    </div>
  );

  return createPortal(
    <div
      className="wg2-dd-backdrop"
      onClick={handleBackdropClick}
      data-testid={`discussion-backdrop-${taskId}`}
    >
      {dialog}
    </div>,
    document.body,
  );
};
