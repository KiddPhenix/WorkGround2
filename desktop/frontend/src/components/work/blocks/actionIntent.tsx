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
import type { BlockRendererProps } from './types';

const TERMINAL = new Set<ActionReceiptStatus>(['succeeded', 'failed', 'rejected', 'unknown']);

function stableJSON(value: unknown): string {
  if (value === null) return 'null';
  switch (typeof value) {
    case 'boolean': return `boolean:${value}`;
    case 'number': return `number:${Number.isNaN(value) ? 'NaN' : Object.is(value, -0) ? '-0' : String(value)}`;
    case 'string': return `string:${JSON.stringify(value)}`;
    case 'undefined': return 'undefined';
    case 'bigint': return `bigint:${value}`;
    case 'symbol': return `symbol:${JSON.stringify(value.description ?? '')}`;
    case 'function': return `function:${JSON.stringify(value.name)}`;
    default: break;
  }
  if (Array.isArray(value)) return `array:[${value.map(stableJSON).join(',')}]`;
  return `object:{${Object.keys(value as Record<string, unknown>).sort().map((key) =>
    `${JSON.stringify(key)}:${stableJSON((value as Record<string, unknown>)[key])}`).join(',')}}`;
}

const SHA256_K = [
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
] as const;

function utf8(value: string): number[] {
  const bytes: number[] = [];
  for (let index = 0; index < value.length; index += 1) {
    let point = value.codePointAt(index) ?? 0xfffd;
    if (point > 0xffff) index += 1;
    else if (point >= 0xd800 && point <= 0xdfff) point = 0xfffd;
    if (point <= 0x7f) bytes.push(point);
    else if (point <= 0x7ff) bytes.push(0xc0 | (point >>> 6), 0x80 | (point & 0x3f));
    else if (point <= 0xffff) bytes.push(0xe0 | (point >>> 12), 0x80 | ((point >>> 6) & 0x3f), 0x80 | (point & 0x3f));
    else {
      bytes.push(
        0xf0 | (point >>> 18),
        0x80 | ((point >>> 12) & 0x3f),
        0x80 | ((point >>> 6) & 0x3f),
        0x80 | (point & 0x3f),
      );
    }
  }
  return bytes;
}

function rotateRight(value: number, bits: number): number {
  return (value >>> bits) | (value << (32 - bits));
}

function sha256(value: string): string {
  const source = utf8(value);
  const bitLength = source.length * 8;
  const byteLength = Math.ceil((source.length + 9) / 64) * 64;
  const bytes = new Uint8Array(byteLength);
  bytes.set(source);
  bytes[source.length] = 0x80;
  const high = Math.floor(bitLength / 0x1_0000_0000);
  const low = bitLength >>> 0;
  for (let index = 0; index < 4; index += 1) {
    bytes[byteLength - 8 + index] = high >>> (24 - index * 8);
    bytes[byteLength - 4 + index] = low >>> (24 - index * 8);
  }

  const state = [
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ];
  const words = new Uint32Array(64);
  for (let offset = 0; offset < bytes.length; offset += 64) {
    for (let index = 0; index < 16; index += 1) {
      const cursor = offset + index * 4;
      words[index] = ((bytes[cursor] << 24) | (bytes[cursor + 1] << 16) |
        (bytes[cursor + 2] << 8) | bytes[cursor + 3]) >>> 0;
    }
    for (let index = 16; index < 64; index += 1) {
      const x = words[index - 15];
      const y = words[index - 2];
      const s0 = rotateRight(x, 7) ^ rotateRight(x, 18) ^ (x >>> 3);
      const s1 = rotateRight(y, 17) ^ rotateRight(y, 19) ^ (y >>> 10);
      words[index] = (words[index - 16] + s0 + words[index - 7] + s1) >>> 0;
    }
    let [a, b, c, d, e, f, g, h] = state;
    for (let index = 0; index < 64; index += 1) {
      const s1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25);
      const choose = (e & f) ^ (~e & g);
      const first = (h + s1 + choose + SHA256_K[index] + words[index]) >>> 0;
      const s0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22);
      const majority = (a & b) ^ (a & c) ^ (b & c);
      const second = (s0 + majority) >>> 0;
      h = g;
      g = f;
      f = e;
      e = (d + first) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (first + second) >>> 0;
    }
    state[0] = (state[0] + a) >>> 0;
    state[1] = (state[1] + b) >>> 0;
    state[2] = (state[2] + c) >>> 0;
    state[3] = (state[3] + d) >>> 0;
    state[4] = (state[4] + e) >>> 0;
    state[5] = (state[5] + f) >>> 0;
    state[6] = (state[6] + g) >>> 0;
    state[7] = (state[7] + h) >>> 0;
  }
  return state.map((part) => part.toString(16).padStart(8, '0')).join('');
}

function requestID(intent: unknown): string {
  return `work-action-v1-sha256-${sha256(stableJSON(intent))}`;
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
