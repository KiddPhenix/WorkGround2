// Shared action-intent runtime for code-owned WorkBlock renderers.
// Renderers only publish typed intent. Controller receipts remain the business
// source of truth; local state only closes the projection gap after a click.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  ActionReceipt,
  ActionReceiptStatus,
  BlockActionRequest,
  BlockInstance,
} from '../../../work/types';
import { digestIntent } from './intentDigest';
import type { BlockRendererProps } from './types';

const TERMINAL = new Set<ActionReceiptStatus>(['succeeded', 'failed', 'rejected', 'unknown']);

function requestID(intent: unknown): string {
  return `work-action-v1-sha256-${digestIntent(intent)}`;
}

function receiptOrder(receipt: ActionReceipt, index: number): number {
  const timestamp = Date.parse(receipt.updatedAt ?? receipt.createdAt ?? '');
  if (Number.isFinite(timestamp)) return timestamp;
  return (receipt.revision ?? 0) * 1_000 + index;
}

function latestReceipt(
  receipts: readonly ActionReceipt[] | undefined,
  block: BlockInstance,
  workID: string,
  actionID: string,
  activeRequestID?: string,
): ActionReceipt | undefined {
  let latest: ActionReceipt | undefined;
  let latestOrder = Number.NEGATIVE_INFINITY;
  for (let index = 0; index < (receipts?.length ?? 0); index += 1) {
    const receipt = receipts![index];
    if (receipt.workId !== workID || receipt.blockId !== block.id || receipt.actionId !== actionID) continue;
    if (activeRequestID && receipt.requestId !== activeRequestID) continue;
    const order = receiptOrder(receipt, index);
    if (order >= latestOrder) {
      latest = receipt;
      latestOrder = order;
    }
  }
  return latest;
}

function preferReceipt(local: ActionReceipt | undefined, projected: ActionReceipt | undefined): ActionReceipt | undefined {
  if (!local) return projected;
  if (!projected || projected.requestId !== local.requestId) return local;
  // A delayed pending/running projection must not overwrite a terminal receipt.
  if (TERMINAL.has(local.status) && !TERMINAL.has(projected.status)) return local;
  return projected;
}

function safeMessage(receipt: ActionReceipt): string {
  const generic: Record<ActionReceiptStatus, string> = {
    pending: 'Waiting for the controller',
    running: 'Action is running',
    succeeded: 'Action completed',
    failed: 'Action failed safely',
    rejected: 'Action was rejected',
    unknown: 'Outcome is unknown; verify external state before continuing',
  };
  const message = receipt.message?.trim();
  if (!message) return generic[receipt.status];
  if (message.length > 320 || /(?:bearer\s+|token\s*=|password\s*=|secret\s*=|[a-z]:\\|\/(?:users|home|etc)\/)/i.test(message)) {
    return generic[receipt.status];
  }
  return message;
}

function isReceiptPromise(
  value: ActionReceipt | void | Promise<ActionReceipt | void>,
): value is Promise<ActionReceipt | void> {
  return Boolean(value && typeof (value as PromiseLike<unknown>).then === 'function');
}

export interface ActionIntentState {
  canRetry: boolean;
  conflict?: string;
  disabled: boolean;
  disabledReason?: string;
  pending: boolean;
  receipt?: ActionReceipt;
  dispatch: (input?: Record<string, unknown>) => void;
  retry: () => void;
}

type ActionProps = Pick<
  BlockRendererProps,
  'block' | 'readonly' | 'archived' | 'context' | 'onAction'
>;

function disabledReason(props: ActionProps): string | undefined {
  if (props.archived) return 'Archived — actions disabled';
  if (props.readonly) return 'Read-only — actions disabled';
  if (props.block.tombstone) return 'Block removed — actions disabled';
  if (props.block.status !== 'ready') return 'Block not ready — actions disabled';
  if (!props.context.runId || !props.context.taskId) return 'Workflow context unavailable — actions disabled';
  if (typeof props.onAction !== 'function') return 'No handler configured — actions unavailable';
  return undefined;
}

export function useActionIntent(props: ActionProps, actionID: string, inputRequired = false): ActionIntentState {
  const reason = disabledReason(props);
  const activeRef = useRef(true);
  const inFlightRef = useRef(false);
  const inputRef = useRef<Record<string, unknown> | undefined>(undefined);
  const hasInputRef = useRef(false);
  const retryMatchesRef = useRef(!inputRequired);
  const [local, setLocal] = useState<ActionReceipt>();
  const [conflict, setConflict] = useState('');
  const projected = useMemo(() => latestReceipt(
    props.context.actionReceipts,
    props.block,
    props.context.workId,
    actionID,
    local?.requestId,
  ), [actionID, local?.requestId, props.block, props.context.actionReceipts, props.context.workId]);
  const receipt = preferReceipt(local, projected ?? (!local
    ? latestReceipt(props.context.actionReceipts, props.block, props.context.workId, actionID)
    : undefined));
  const pending = receipt?.status === 'pending' || receipt?.status === 'running';
  const terminal = receipt ? TERMINAL.has(receipt.status) : false;
  const retryableFailure = Boolean(receipt && receipt.status === 'failed' && receipt.retryable && receipt.outcomeKnown !== false);

  useEffect(() => {
    activeRef.current = true;
    return () => { activeRef.current = false; };
  }, []);
  useEffect(() => {
    if (terminal) inFlightRef.current = false;
  }, [terminal]);

  const publish = useCallback((input?: Record<string, unknown>, retryReceipt?: ActionReceipt) => {
    if (reason || pending || inFlightRef.current || (terminal && !retryReceipt && !retryableFailure) || !props.onAction) return;
    const normalizedInput = input && Object.keys(input).length > 0 ? input : undefined;
    inputRef.current = normalizedInput;
    hasInputRef.current = true;
    const candidateID = requestID({
      workId: props.context.workId,
      runId: props.context.runId,
      taskId: props.context.taskId,
      blockId: props.block.id,
      revision: props.block.revision,
      actionId: actionID,
      input: normalizedInput,
    });
    const failedReceipt = retryReceipt ?? (retryableFailure ? receipt : undefined);
    if (failedReceipt && inputRequired && candidateID !== failedReceipt.requestId) {
      retryMatchesRef.current = false;
      setConflict('Retry input does not match the original request');
      return;
    }
    retryMatchesRef.current = true;
    setConflict('');
    const id = failedReceipt?.requestId ?? candidateID;
    inFlightRef.current = true;
    const optimistic: ActionReceipt = {
      workId: props.context.workId,
      blockId: props.block.id,
      blockKind: props.block.kind,
      actionId: actionID,
      status: 'pending',
      requestId: id,
      retryable: false,
      outcomeKnown: false,
    };
    setLocal(optimistic);
    const request: BlockActionRequest = {
      workId: props.context.workId,
      runId: props.context.runId,
      taskId: props.context.taskId,
      blockId: props.block.id,
      actionId: actionID,
      input: normalizedInput,
      requestId: id,
      expectedRevision: props.block.revision,
    };
    try {
      const result = props.onAction(request);
      if (isReceiptPromise(result)) {
        void Promise.resolve(result).then((next) => {
          if (activeRef.current && next) setLocal((current) => preferReceipt(current, next));
        });
      } else if (result) {
        setLocal((current) => preferReceipt(current, result));
      }
    } catch {
      // BlockHost owns callback isolation and will replace only this renderer.
    }
  }, [actionID, inputRequired, pending, props.block, props.context, props.onAction, reason, receipt, retryableFailure, terminal]);

  const retry = useCallback(() => {
    if (!receipt || receipt.status !== 'failed' || !receipt.retryable || receipt.outcomeKnown === false ||
        (inputRequired && (!hasInputRef.current || !retryMatchesRef.current))) return;
    inFlightRef.current = false;
    publish(inputRef.current, receipt);
  }, [inputRequired, publish, receipt]);

  const canRetry = Boolean(!conflict && receipt && receipt.status === 'failed' && receipt.retryable &&
    receipt.outcomeKnown !== false && (!inputRequired || (hasInputRef.current && retryMatchesRef.current)));

  return {
    canRetry,
    conflict: conflict || undefined,
    disabled: Boolean(reason) || pending || (terminal && !retryableFailure),
    disabledReason: reason,
    pending,
    receipt,
    dispatch: (input) => publish(input),
    retry,
  };
}

export function ActionFeedback({ state }: { state: ActionIntentState }): React.ReactElement | null {
  const receipt = state.receipt;
  if (!receipt) return state.disabledReason
    ? <div className="wg2-action-feedback wg2-action-feedback--disabled">{state.disabledReason}</div>
    : null;
  const alert = receipt.status === 'failed' || receipt.status === 'rejected' || receipt.status === 'unknown';
  const canRetry = state.canRetry && !state.disabledReason;
  return (
    <div
      className={`wg2-action-feedback wg2-action-feedback--${receipt.status}`}
      role={alert ? 'alert' : 'status'}
      aria-live="polite"
      data-action-status={receipt.status}
    >
      <span>{state.conflict ?? safeMessage(receipt)}</span>
      {canRetry && (
        <button type="button" className="wg2-action-feedback__retry" onClick={state.retry} disabled={state.pending}>
          Retry safely
        </button>
      )}
    </div>
  );
}
