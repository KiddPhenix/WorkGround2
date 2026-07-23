// Accessible checklist renderer with revision-based structured updates. Local
// drafts remain the source of truth until the host confirms an attempt.

import React, { useCallback, useEffect, useRef, useState } from 'react';
import type { BlockUpdateRequest } from '../../../work/types';
import { digestIntent } from './intentDigest';
import type { ChecklistData, ChecklistItem } from './schemas';
import type { BlockRendererProps } from './types';

interface DraftState {
  blockID: string;
  values: Map<string, boolean>;
}

interface UpdateAttempt {
  blockID: string;
  request: BlockUpdateRequest;
  submitted: Map<string, boolean>;
}

interface UpdateError {
  attempt: UpdateAttempt;
  message: string;
}

const EMPTY_DRAFT = new Map<string, boolean>();
const MAX_CACHED_DRAFTS = 128;
const draftCache = new Map<string, Map<string, boolean>>();

function cacheKey(workID: string, blockID: string): string {
  return `${workID.length}:${workID}${blockID}`;
}

function readDraft(key: string): Map<string, boolean> {
  return new Map(draftCache.get(key) ?? EMPTY_DRAFT);
}

function writeDraft(key: string, values: Map<string, boolean>): void {
  draftCache.delete(key);
  if (values.size > 0) draftCache.set(key, new Map(values));
  while (draftCache.size > MAX_CACHED_DRAFTS) {
    const oldest = draftCache.keys().next().value as string | undefined;
    if (oldest === undefined) break;
    draftCache.delete(oldest);
  }
}

function safeCrop(text: string, max: number): string {
  const chars = Array.from(text);
  return chars.length > max ? `${chars.slice(0, max).join('')}\u2026` : text;
}

function safeError(error: unknown): string {
  return error instanceof Error
    ? 'Server rejected the update. Your changes are preserved and can be retried.'
    : 'Update failed. Your changes are preserved and can be retried.';
}

function sameDraft(left: Map<string, boolean>, right: Map<string, boolean>): boolean {
  if (left.size !== right.size) return false;
  for (const [id, checked] of left) {
    if (right.get(id) !== checked) return false;
  }
  return true;
}

export const ChecklistBlock: React.FC<BlockRendererProps> = ({
  block,
  readonly,
  archived,
  context,
  onUpdate,
}) => {
  const data = block.data as ChecklistData;
  const items = data?.items ?? [];
  const disabled = readonly || archived || typeof onUpdate !== 'function';
  const key = cacheKey(context.workId, block.id);
  const [draftState, setDraftState] = useState<DraftState>(() => ({
    blockID: block.id,
    values: readDraft(key),
  }));
  const [pending, setPending] = useState<UpdateAttempt | null>(null);
  const [error, setError] = useState<UpdateError | null>(null);
  const pendingRef = useRef<UpdateAttempt | null>(null);
  const currentBlockID = useRef(block.id);
  const mountedRef = useRef(true);
  currentBlockID.current = block.id;

  // State is keyed explicitly so a reused renderer instance can never paint a
  // previous block's draft while React reconciles a new projection.
  const draft = draftState.blockID === block.id ? draftState.values : EMPTY_DRAFT;
  const draftRef = useRef(draft);
  draftRef.current = draft;
  const activeError = error?.attempt.blockID === block.id ? error : null;
  const updating = pending?.blockID === block.id;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      pendingRef.current = null;
    };
  }, []);

  const effectiveChecked = useCallback(
    (item: ChecklistItem): boolean => draft.get(item.id) ?? item.checked,
    [draft],
  );

  const toggleItem = useCallback((item: ChecklistItem) => {
    if (disabled) return;
    const values = new Map(draftRef.current);
    values.set(item.id, !(values.get(item.id) ?? item.checked));
    writeDraft(key, values);
    setDraftState({ blockID: block.id, values });
    setError(null);
  }, [block.id, disabled, key]);

  const submitUpdate = useCallback(async (retry?: UpdateAttempt) => {
    if (disabled || !onUpdate || draft.size === 0) return;
    if (pendingRef.current?.blockID === block.id) return;

    const submitted = new Map(draft);
    const reuse = retry &&
      retry.blockID === block.id &&
      retry.request.expectedRevision === block.revision &&
      sameDraft(retry.submitted, submitted);
    const attempt = reuse ? retry : (() => {
      const newItems = items.map((item) => ({
        id: item.id,
        text: item.text,
        checked: submitted.get(item.id) ?? item.checked,
        ...(item.detail !== undefined ? { detail: item.detail } : {}),
      }));
      const updateIntent = {
        workId: context.workId,
        runId: context.runId,
        taskId: context.taskId,
        blockId: block.id,
        revision: block.revision,
        data: { items: newItems },
      };
      return {
        blockID: block.id,
        submitted,
        request: {
          workId: context.workId,
          blockId: block.id,
          data: { items: newItems },
          requestId: `checklist-update-v1-sha256-${digestIntent(updateIntent)}`,
          expectedRevision: block.revision,
        },
      } satisfies UpdateAttempt;
    })();

    pendingRef.current = attempt;
    setPending(attempt);
    setError(null);
    try {
      await onUpdate(attempt.request);
      if (mountedRef.current && currentBlockID.current === attempt.blockID) {
        const values = new Map(draftRef.current);
        for (const [id, checked] of attempt.submitted) {
          // A user may toggle again while the request is pending. Only clear
          // the exact draft values acknowledged by this attempt.
          if (values.get(id) === checked) values.delete(id);
        }
        writeDraft(key, values);
        setDraftState({ blockID: attempt.blockID, values });
        setError(null);
      }
    } catch (cause) {
      if (mountedRef.current && currentBlockID.current === attempt.blockID) {
        setError({ attempt, message: safeError(cause) });
      }
    } finally {
      if (pendingRef.current === attempt) pendingRef.current = null;
      if (mountedRef.current && currentBlockID.current === attempt.blockID) {
        setPending((current) => current === attempt ? null : current);
      }
    }
  }, [block.id, block.revision, context.runId, context.taskId, context.workId, disabled, draft, items, key, onUpdate]);

  const handleKeyDown = useCallback((event: React.KeyboardEvent, item: ChecklistItem) => {
    if (disabled) return;
    if (event.key === ' ' || event.key === 'Enter') {
      event.preventDefault();
      toggleItem(item);
    }
  }, [disabled, toggleItem]);

  if (items.length === 0) {
    return (
      <section className="wg2-block wg2-checklist-block" aria-label="Checklist">
        <p className="wg2-block-empty" role="status">No checklist items</p>
      </section>
    );
  }

  return (
    <section className="wg2-block wg2-checklist-block" aria-label="Checklist" aria-busy={updating}>
      <ul className="wg2-checklist-list">
        {items.map((item) => {
          const checked = effectiveChecked(item);
          const dirty = draft.has(item.id);
          const itemID = `checklist-${block.id}-${item.id}`;
          return (
            <li
              key={item.id}
              className={`wg2-checklist-item ${checked ? 'wg2-checklist-checked' : ''} ${dirty ? 'wg2-checklist-dirty' : ''}`}
            >
              <label htmlFor={itemID} className="wg2-checklist-label">
                <input
                  id={itemID}
                  type="checkbox"
                  className="wg2-checklist-checkbox"
                  checked={checked}
                  disabled={disabled}
                  onChange={() => toggleItem(item)}
                  onKeyDown={(event) => handleKeyDown(event, item)}
                  aria-label={item.text}
                />
                <span className="wg2-checklist-text">{safeCrop(item.text, 512)}</span>
              </label>
              {item.detail !== undefined && (
                <details className="wg2-checklist-details">
                  <summary className="wg2-checklist-detail-toggle">Detail</summary>
                  <p className="wg2-checklist-detail-text">{safeCrop(item.detail, 1024)}</p>
                </details>
              )}
            </li>
          );
        })}
      </ul>

      {draft.size > 0 && !disabled && (
        <div className="wg2-checklist-actions">
          <button
            type="button"
            className="wg2-checklist-save"
            disabled={updating}
            onClick={() => void submitUpdate()}
          >
            {updating ? 'Saving\u2026' : `Save changes (${draft.size})`}
          </button>
        </div>
      )}

      {activeError && (
        <div className="wg2-checklist-error" role="alert" aria-live="assertive">
          <p className="wg2-checklist-error-text">Update failed: {activeError.message}</p>
          <button
            type="button"
            className="wg2-checklist-retry"
            disabled={updating}
            onClick={() => void submitUpdate(activeError.attempt)}
          >
            Retry
          </button>
        </div>
      )}
    </section>
  );
};

ChecklistBlock.displayName = 'ChecklistBlock';
