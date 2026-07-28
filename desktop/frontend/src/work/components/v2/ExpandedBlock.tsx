import React, { useCallback, useState } from 'react';
import { MessageCircle } from 'lucide-react';

import type {
  TaskV2View,
  NodeDef,
  InputSpec,
  WorkInput,
  SubmitWorkInputRequest,
  SetInputCornerstoneRequest,
  SubmitInputResult,
  CornerstonePinResult,
  SelectWorkInputFileRequest,
  SelectWorkInputFileResult,
} from '../../types_v2';
import { WorkInputHost } from './input/WorkInputHost';
import type { DraftValue, WorkInputRefreshContext } from './input/WorkInputHost';
export type { WorkInputRefreshContext } from './input/WorkInputHost';

// ── Handler intents ────────────────────────────────────────────────────────

export interface TaskCollapseIntent { workId: string; taskId: string; }
export interface TaskRetryIntent { workId: string; taskId: string; runId: string; }
export interface DiscussionBlockRef {
  id: string;
  title?: string;
  revision: number;
}

// ── ExpandedBlock ──────────────────────────────────────────────────────────

export interface ExpandedBlockProps {
  task: TaskV2View;
  workId: string;
  runId: string;
  sessionId: string;
  nodeDef?: NodeDef;
  inputSpecs?: InputSpec[];
  workInputs?: WorkInput[];
  definitionRevision: number;
  workRevision: number;
  hasTypedInput: boolean;
  discussionBlock?: DiscussionBlockRef;
  onCollapse: (intent: TaskCollapseIntent) => void;
  onRetry?: (intent: TaskRetryIntent) => void | Promise<void>;
  onSubmitWorkInput?: (req: SubmitWorkInputRequest) => Promise<SubmitInputResult>;
  onSetCornerstone?: (req: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  onUnsetCornerstone?: (req: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  onRefreshAuthoritative?: (context: WorkInputRefreshContext) => Promise<void>;
  onSelectFile?: (req: SelectWorkInputFileRequest) => Promise<SelectWorkInputFileResult>;
  /** Called when user edits a draft — parent persists by full identity. */
  onInputDraftChange?: (taskId: string, blockId: string, inputId: string, specId: string, inputRev: number, value: DraftValue) => void;
  /** Resolve draft value from parent's persistent store. */
  resolveInputDraft?: (taskId: string, blockId: string, spec: InputSpec, wi: WorkInput) => DraftValue;
  resolveCommittedRequestIds?: (
    taskId: string,
    blockId: string,
    spec: InputSpec,
    wi: WorkInput,
  ) => Partial<Record<'submit' | 'pin' | 'unpin', string>>;
  onInputRequestCommitted?: (
    taskId: string,
    blockId: string,
    spec: InputSpec,
    wi: WorkInput,
    operation: 'submit' | 'pin' | 'unpin',
    requestId: string,
  ) => void;
  /** Called when user clicks discussion button — parent opens single global drawer. */
  onOpenDiscussion?: (blockId: string, blockTitle: string, blockRevision: number) => void;
}

export const ExpandedBlock: React.FC<ExpandedBlockProps> = ({
  task, workId, sessionId: _sessionId,
  nodeDef, inputSpecs, workInputs,
  definitionRevision, workRevision,
  hasTypedInput,
  discussionBlock,
  onCollapse, onRetry,
  onSubmitWorkInput, onSetCornerstone, onUnsetCornerstone,
  onRefreshAuthoritative, onSelectFile,
  onInputDraftChange, resolveInputDraft, resolveCommittedRequestIds, onInputRequestCommitted,
  onOpenDiscussion,
}) => {
  const [retrying, setRetrying] = useState(false);
  const [retryError, setRetryError] = useState<string | null>(null);

  const handleCollapse = useCallback(() => {
    onCollapse({ workId, taskId: task.id });
  }, [onCollapse, workId, task.id]);

  const handleRetry = useCallback(async () => {
    if (!onRetry || retrying) return;
    setRetrying(true);
    setRetryError(null);
    try { await onRetry({ workId, taskId: task.id, runId: task.runId }); }
    catch (error) { setRetryError(error instanceof Error ? error.message : String(error)); }
    finally { setRetrying(false); }
  }, [onRetry, retrying, workId, task.id, task.runId]);

  const waitingInputs = resolveWaitingInputs(task.waitingInputIds, nodeDef?.inputSpecIds, inputSpecs, workInputs);
  const handleDiscuss = useCallback(() => {
    if (!discussionBlock) return;
    onOpenDiscussion?.(
      discussionBlock.id,
      discussionBlock.title?.trim() || task.title,
      discussionBlock.revision,
    );
  }, [onOpenDiscussion, discussionBlock, task.title]);

  const handleInputGroupKeyDown = useCallback((event: React.KeyboardEvent<HTMLUListElement>) => {
    if (event.key !== 'Tab' || event.altKey || event.ctrlKey || event.metaKey) return;
    const target = event.target;
    if (!(target instanceof HTMLElement) || !target.matches(EDITABLE_INPUT_SELECTOR)) return;
    const group = event.currentTarget;
    const fields = Array.from(group.querySelectorAll<HTMLElement>(EDITABLE_INPUT_SELECTOR));
    const index = fields.indexOf(target);
    if (index < 0) return;
    const next = fields[index + (event.shiftKey ? -1 : 1)];
    if (next) {
      event.preventDefault();
      next.focus();
      return;
    }
    const tabbable = Array.from(document.querySelectorAll<HTMLElement>(TABBABLE_SELECTOR));
    const currentIndex = tabbable.indexOf(target);
    const step = event.shiftKey ? -1 : 1;
    for (let cursor = currentIndex + step; cursor >= 0 && cursor < tabbable.length; cursor += step) {
      if (group.contains(tabbable[cursor])) continue;
      event.preventDefault();
      tabbable[cursor].focus();
      return;
    }
  }, []);

  return (
    <div
      id={`expanded-block-${task.id}`}
      className="wg2-eb-panel"
      data-testid={`expanded-block-${task.id}`}
      role="region"
      aria-label={`${task.title} 详情`}
    >
      <div className="wg2-eb-header-row">
        {nodeDef?.description && (
          <p className="wg2-eb-desc" data-testid={`expanded-block-desc-${task.id}`}>
            {nodeDef.description}
          </p>
        )}
        {onOpenDiscussion && discussionBlock && (
          <button type="button" className="wg2-eb-btn wg2-eb-btn-discuss"
            onClick={handleDiscuss}
            aria-label={`讨论 ${task.title}`}
            data-testid={`expanded-block-discuss-${task.id}`}>
            <MessageCircle size={15} aria-hidden="true" />
            <span>讨论</span>
          </button>
        )}
      </div>

      {waitingInputs.length > 0 && (
        <div data-testid={`expanded-block-inputs-${task.id}`}>
          {!hasTypedInput && (
            <p role="status" data-testid={`expanded-block-input-unavailable-${task.id}`}>
              类型化输入面板未完整连接（需 submit/pin/unpin/refresh），暂时禁用。
            </p>
          )}
          <ul
            className={`wg2-eb-inputs${hasTypedInput ? ' wg2-eb-inputs--editable' : ''}`}
            role="list"
            aria-label="等待输入"
            data-input-focus-group={task.id}
            onKeyDownCapture={handleInputGroupKeyDown}
          >
            {waitingInputs.map(({ spec, input: workInput }) => {
              const editable = Boolean(
                hasTypedInput && workInput && onSubmitWorkInput && onSetCornerstone &&
                onUnsetCornerstone && onRefreshAuthoritative && resolveInputDraft
              );
              return (
                <li
                  key={`${workInput?.id ?? 'unbound'}\u0000${spec.id}`}
                  className={`wg2-eb-input-item${editable ? ' wg2-eb-input-item--editable' : ''}`}
                  data-testid={`expanded-block-input-${task.id}-${spec.id}`}>
                  {editable && workInput && onSubmitWorkInput && onSetCornerstone && onUnsetCornerstone && onRefreshAuthoritative && resolveInputDraft ? (
                    <WorkInputHost
                      inputSpec={spec}
                      workInput={workInput}
                      draftValue={resolveInputDraft(task.id, workInput.blockId, spec, workInput)}
                      onDraftChange={(value) =>
                        onInputDraftChange?.(task.id, workInput.blockId, workInput.id, spec.id, workInput.revision, value)
                      }
                      onSubmit={onSubmitWorkInput}
                      onPin={onSetCornerstone}
                      onUnpin={onUnsetCornerstone}
                      onRefreshAuthoritative={onRefreshAuthoritative}
                      onSelectFile={onSelectFile ? async () => {
                        const selected = await onSelectFile({
                          workId,
                          runId: workInput.runId,
                          taskId: workInput.taskId,
                          blockId: workInput.blockId,
                          inputId: workInput.id,
                          specId: workInput.specId,
                        });
                        if (selected.error) throw new Error(selected.error.message);
                        return selected.canceled ? null : selected.artifactRef ?? null;
                      } : undefined}
                      workId={workId} taskId={task.id} runId={workInput.runId} blockId={workInput.blockId}
                      definitionRevision={definitionRevision}
                      inputRevision={workInput.revision}
                      workRevision={workRevision}
                      committedRequestIds={resolveCommittedRequestIds?.(task.id, workInput.blockId, spec, workInput)}
                      onRequestCommitted={(operation, requestId) =>
                        onInputRequestCommitted?.(task.id, workInput.blockId, spec, workInput, operation, requestId)
                      }
                    />
                  ) : (
                    <span className="wg2-eb-input-kind">
                      {spec.label}
                      {spec.required && <span className="wg2-eb-input-required" aria-label="必填">*</span>}
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {nodeDef?.dependsOn && nodeDef.dependsOn.length > 0 && (
        <div>
          <ul className="wg2-eb-deps" role="list" aria-label="依赖节点">
            {nodeDef.dependsOn.map((depId) => <li key={depId} className="wg2-eb-dep-tag">{depId}</li>)}
          </ul>
        </div>
      )}

      {task.error && (
        <div className="wg2-eb-error" role="alert" data-testid={`expanded-block-error-${task.id}`}>
          <span className="wg2-eb-error-icon" aria-hidden="true">⚠</span>
          <span className="wg2-eb-error-msg">{task.error}</span>
        </div>
      )}

      <div className="wg2-eb-actions" data-testid={`expanded-block-actions-${task.id}`}>
        <button type="button" className="wg2-eb-btn" onClick={handleCollapse}
          aria-label={`收起 ${task.title} 详情`}
          data-testid={`expanded-block-collapse-${task.id}`}>
          收起
        </button>
        {(task.state === 'failed_retryable' || task.state === 'invalidated') && onRetry && (
          <button type="button" className="wg2-eb-btn wg2-eb-btn-danger"
            onClick={() => void handleRetry()} disabled={retrying}
            aria-busy={retrying ? 'true' : undefined}
            aria-label={`重试 ${task.title}`}
            data-testid={`expanded-block-retry-${task.id}`}>
            {retrying ? '重试中…' : '重试'}
          </button>
        )}
        {retryError && (
          <div role="alert" className="wg2-eb-error" data-testid={`expanded-block-retry-error-${task.id}`}>
            {retryError}
          </div>
        )}
      </div>
    </div>
  );
};

// ── Helpers ────────────────────────────────────────────────────────────────

const EDITABLE_INPUT_SELECTOR =
  'input:not([disabled]):not([type="hidden"]), textarea:not([disabled]), select:not([disabled]), [contenteditable="true"]';
const TABBABLE_SELECTOR = [
  'button:not([disabled])',
  EDITABLE_INPUT_SELECTOR,
  'a[href]',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

function resolveWaitingInputs(
  waitingIds: string[] | undefined,
  nodeInputSpecIds: string[] | undefined,
  allSpecs: InputSpec[] | undefined,
  workInputs: WorkInput[] | undefined,
): Array<{ spec: InputSpec; input?: WorkInput }> {
  if (!waitingIds || waitingIds.length === 0 || !allSpecs) return [];
  const specMap = new Map<string, InputSpec>();
  for (const s of allSpecs) specMap.set(s.id, s);
  const waitingSet = new Set(waitingIds);
  const order = nodeInputSpecIds ?? allSpecs.map((s) => s.id);
  const inputBySpec = new Map<string, WorkInput>();
  for (const input of workInputs ?? []) {
    if (waitingSet.has(input.id) || waitingSet.has(input.specId)) inputBySpec.set(input.specId, input);
  }
  const result: Array<{ spec: InputSpec; input?: WorkInput }> = [];
  for (const specId of order) {
    const spec = specMap.get(specId);
    const input = inputBySpec.get(specId);
    if (spec && (input || waitingSet.has(specId))) result.push({ spec, input });
  }
  return result;
}
