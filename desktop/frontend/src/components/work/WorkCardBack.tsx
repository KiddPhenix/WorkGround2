import React, { useMemo, type ReactNode } from 'react';

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
  transcript: WorkCardBackSlot;
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
}) => {
  const { work } = view;
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
    };
    return {
      key: `${sessionRef.sessionPath}\u0000${sessionRef.branchId}`,
      targetID: `attempt:${context.runId}:${context.stageId}:${context.taskId}:${attemptKey(resolved.attempt)}`,
      node: resolveSessionSurface(sessionRef, context),
    };
  }, [selection, resolveSessionSurface, work]);

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
        {work.prompt && (
          <div className="wg2-work-back-prompt-preview" data-testid="work-back-prompt">
            {work.prompt.slice(0, 200)}{work.prompt.length > 200 ? '…' : ''}
          </div>
        )}
      </div>

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
      ) : slots ? (
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
