import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { TriangleAlert } from 'lucide-react';

import type {
  InputSpec,
  WorkInput,
  SubmitWorkInputRequest,
  SetInputCornerstoneRequest,
  SubmitInputResult,
  CornerstonePinResult,
} from '../../../types_v2';
import type { ArtifactRef } from '../../../types';
import type { FormFieldSpec } from './schema';
import {
  parseValueSchema,
  validateDraft,
  numberDraftWarning,
  validateFormField,
  toWireValue,
  kindLabel,
  SchemaParseError,
  type ParsedValueSchema,
  type DraftValue,
} from './schema';

export type { DraftValue } from './schema';

// ── Stable request IDs ─────────────────────────────────────────────────────

type InputOperation = 'submit' | 'pin' | 'unpin';

function hashIntent(value: string): string {
  let hash = 0;
  for (let index = 0; index < value.length; index++) {
    hash = ((hash << 5) - hash) + value.charCodeAt(index);
    hash |= 0;
  }
  return (hash >>> 0).toString(36);
}

function stableRequestId(
  operation: InputOperation,
  intentKey: string,
  committedRequestId: string | undefined,
): string {
  const prefix = `input-${operation}-${hashIntent(intentKey)}-a`;
  if (!committedRequestId?.startsWith(prefix)) return `${prefix}0`;
  const sequence = Number.parseInt(committedRequestId.slice(prefix.length), 36);
  return `${prefix}${(Number.isSafeInteger(sequence) ? sequence + 1 : 1).toString(36)}`;
}

// ── Public props ────────────────────────────────────────────────────────────

export interface WorkInputHostProps {
  inputSpec: InputSpec;
  workInput?: WorkInput;
  draftValue: DraftValue;
  onDraftChange: (value: DraftValue) => void;
  /** Submit with full frozen DTO. Host generates stable requestId. */
  onSubmit: (req: SubmitWorkInputRequest) => Promise<SubmitInputResult>;
  /** Pin with full frozen DTO. */
  onPin: (req: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  /** Unpin with full frozen DTO (pin=false). */
  onUnpin: (req: SetInputCornerstoneRequest) => Promise<CornerstonePinResult>;
  /** Request a fresh authoritative snapshot after a committed mutation. */
  onRefreshAuthoritative: (context: WorkInputRefreshContext) => Promise<void>;
  workId: string;
  taskId: string;
  runId: string;
  blockId: string;
  /** Current definition revision for DTO construction. */
  definitionRevision: number;
  /** Current input revision for DTO construction. */
  inputRevision: number;
  /** Current work revision for expectedRevision in DTOs and intent keys. */
  workRevision: number;
  disabled?: boolean;
  committedRequestIds?: Partial<Record<InputOperation, string>>;
  onRequestCommitted?: (operation: InputOperation, requestId: string) => void;
  /** File selection intent — host returns an approved typed ArtifactRef. */
  onSelectFile?: () => Promise<ArtifactRef | null>;
  /** Hide the field-level submit button when the owning Block provides one. */
  hideSubmit?: boolean;
  /** Monotonic signal from the owning Block to submit this field. */
  submitTrigger?: number;
  /** Route Ctrl/Cmd+Enter to the owning Block's shared submit action. */
  onRequestGroupSubmit?: () => void;
  /** Optional free-form context submitted with the typed value. */
  extra?: string;
  /** Used when a parent card owns the visible label and actions. */
  hideHeader?: boolean;
  submitLabel?: string;
  onSubmitCommitted?: (result: SubmitInputResult) => void;
}

export interface WorkInputRefreshContext {
  workId: string;
  inputId: string;
  requestId: string;
  revision: number;
  operation: 'submit' | 'pin' | 'unpin' | 'patch';
}

// ── Internal states ────────────────────────────────────────────────────────

type SubmitPhase = 'idle' | 'submitting' | 'committed' | 'rejected';
type PinPhase = 'idle' | 'pending' | 'pinned' | 'unpinned' | 'error';

interface UIState {
  submitPhase: SubmitPhase;
  submitError: string | null;
  submitResult: SubmitInputResult | null;
  submitRequestId: string | null;
  /** Revision at submit start — for stale detection. */
  submitStartRev: number | null;
  pinPhase: PinPhase;
  pinError: string | null;
  pinResult: CornerstonePinResult | null;
  pinRequestId: string | null;
  pinRecovery: boolean;
  /** For committedRecovery display. */
  committedRecovery: boolean;
  /** File selection state. */
  fileSelecting: boolean;
  fileError: string | null;
  refreshError: string | null;
}

const INITIAL_UI: UIState = {
  submitPhase: 'idle',
  submitError: null,
  submitResult: null,
  submitRequestId: null,
  submitStartRev: null,
  pinPhase: 'idle',
  pinError: null,
  pinResult: null,
  pinRequestId: null,
  pinRecovery: false,
  committedRecovery: false,
  fileSelecting: false,
  fileError: null,
  refreshError: null,
};

interface OperationToken {
  epoch: number;
  identity: string;
  revision: number;
  requestId: string;
}

interface AuthorityToken {
  identity: string;
  revision: number;
}

function isCurrentOperation(
  operation: OperationToken,
  current: OperationToken | null,
  authority: AuthorityToken,
  currentEpoch: number,
): boolean {
  return current === operation
    && operation.epoch === currentEpoch
    && operation.requestId === current.requestId
    && operation.identity === authority.identity
    && operation.revision === authority.revision;
}

// ── WorkInputHost ──────────────────────────────────────────────────────────

export const WorkInputHost: React.FC<WorkInputHostProps> = ({
  inputSpec,
  workInput,
  draftValue,
  onDraftChange,
  onSubmit,
  onPin,
  onUnpin,
  onRefreshAuthoritative,
  workId,
  taskId,
  runId,
  blockId,
  definitionRevision,
  inputRevision,
  workRevision,
  disabled,
  committedRequestIds,
  onRequestCommitted,
  onSelectFile,
  hideSubmit,
  submitTrigger,
  onRequestGroupSubmit,
  extra,
  hideHeader,
  submitLabel,
  onSubmitCommitted,
}) => {
  const [ui, setUI] = useState<UIState>(INITIAL_UI);
  const submittingRef = useRef(false);
  const pinningRef = useRef(false);
  const operationEpochRef = useRef(0);
  const submitOpRef = useRef<OperationToken | null>(null);
  const pinOpRef = useRef<OperationToken | null>(null);

  const authority = {
    identity: [
      workId,
      runId,
      taskId,
      blockId,
      inputSpec.id,
      workInput?.id ?? '',
      definitionRevision,
      workRevision,
    ].join('\u0000'),
    revision: workInput?.revision ?? inputRevision,
  };
  const authorityRef = useRef(authority);
  authorityRef.current = authority;

  // ── Schema parse with error boundary ────────────────────────
  const schemaError = useMemo<string | null>(() => {
    try {
      parseValueSchema(inputSpec.id, inputSpec.kind, inputSpec.valueSchema);
      return null;
    } catch (e) {
      if (e instanceof SchemaParseError) {
        const target = inputSpec.kind === 'choice' || inputSpec.kind === 'multi_choice'
          ? '选项'
          : '输入';
        return `“${inputSpec.label}”的${target}配置有误，请重新规划工作结构后重试。`;
      }
      return `“${inputSpec.label}”暂时无法填写，请刷新后重试。`;
    }
  }, [inputSpec.id, inputSpec.kind, inputSpec.label, inputSpec.valueSchema]);

  const schema: ParsedValueSchema = useMemo(() => {
    if (schemaError) return { kind: inputSpec.kind };
    try {
      return parseValueSchema(inputSpec.id, inputSpec.kind, inputSpec.valueSchema);
    } catch {
      return { kind: inputSpec.kind };
    }
  }, [schemaError, inputSpec.id, inputSpec.kind, inputSpec.valueSchema]);

  // ── Stale detection: snapshot revision advances → old results invalid ──
  const prevAuthorityRef = useRef(authority);
  useEffect(() => {
    const previous = prevAuthorityRef.current;
    if (
      previous.identity === authority.identity
      && previous.revision === authority.revision
    ) {
      return;
    }

    prevAuthorityRef.current = authority;
    operationEpochRef.current++;
    submitOpRef.current = null;
    pinOpRef.current = null;
    submittingRef.current = false;
    pinningRef.current = false;
    setUI((prev) => ({
      ...prev,
      submitPhase: 'idle',
      submitError: null,
      submitResult: null,
      submitStartRev: null,
      committedRecovery: false,
      pinPhase: 'idle',
      pinError: null,
      pinResult: null,
      pinRecovery: false,
      refreshError: null,
    }));
  }, [authority.identity, authority.revision]);

  // ── Validation ──────────────────────────────────────────────
  const validationError = useMemo(() => {
    if (ui.submitPhase === 'submitting') return null;
    if (schemaError) return schemaError;
    return validateDraft(inputSpec, draftValue, schema);
  }, [inputSpec, draftValue, schema, ui.submitPhase, schemaError]);
  const draftIsEmpty = draftValue == null
    || draftValue === ''
    || (Array.isArray(draftValue) && draftValue.length === 0)
    || (!Array.isArray(draftValue)
      && typeof draftValue === 'object'
      && draftValue !== null
      && Object.keys(draftValue).length === 0);
  const visibleValidationError = draftIsEmpty ? null : validationError;
  const advisoryWarning = useMemo(
    () => inputSpec.kind === 'number'
      ? numberDraftWarning(draftValue, schema.number)
      : null,
    [draftValue, inputSpec.kind, schema.number],
  );

  // ── Submit handler (full DTO) ───────────────────────────────
  const handleSubmit = useCallback(async () => {
    if (disabled || submittingRef.current || schemaError) return;
    if (!workInput?.id) {
      setUI((prev) => ({
        ...prev,
        submitPhase: 'rejected',
        submitError: '缺少权威 inputId，无法提交',
      }));
      return;
    }

    const err = validateDraft(inputSpec, draftValue, schema);
    if (err) {
      setUI((prev) => ({ ...prev, submitError: err, submitPhase: 'idle' }));
      return;
    }

    const startRev = workInput?.revision ?? inputRevision;
    const wireValue = toWireValue(inputSpec.kind, draftValue, schema);
    const intentKey = JSON.stringify([authority.identity, startRev, wireValue, extra?.trim() ?? '']);
    // Reuse existing requestId on retry after failure
    const reqId = ui.submitPhase === 'rejected' && ui.submitRequestId
      ? ui.submitRequestId
      : stableRequestId('submit', intentKey, committedRequestIds?.submit);
    const operation: OperationToken = {
      epoch: operationEpochRef.current,
      identity: authority.identity,
      revision: startRev,
      requestId: reqId,
    };

    submittingRef.current = true;
    submitOpRef.current = operation;
    setUI((prev) => ({
      ...prev,
      submitPhase: 'submitting',
      submitError: null,
      submitRequestId: reqId,
      submitStartRev: startRev,
    }));

    const dto: SubmitWorkInputRequest = {
      workId,
      runId,
      taskId,
      blockId,
      inputId: workInput.id,
      value: wireValue,
      extra: extra?.trim() || undefined,
      definitionRevision,
      inputRevision,
      expectedRevision: workRevision,
      requestId: reqId,
    };

    try {
      const result = await onSubmit(dto);
      if (!isCurrentOperation(
        operation,
        submitOpRef.current,
        authorityRef.current,
        operationEpochRef.current,
      )) {
        return;
      }
      submittingRef.current = false;

      if (result.committed) {
        onRequestCommitted?.('submit', reqId);
        onSubmitCommitted?.(result);
        setUI((prev) => ({
          ...prev,
          submitPhase: 'committed',
          submitError: null,
          submitResult: result,
          committedRecovery: !!(result.error || result.transportError || !result.receipt),
        }));
        try {
          await onRefreshAuthoritative({
            workId,
            inputId: workInput.id,
            requestId: reqId,
            revision: result.revision,
            operation: 'submit',
          });
          if (!isCurrentOperation(
            operation,
            submitOpRef.current,
            authorityRef.current,
            operationEpochRef.current,
          )) {
            return;
          }
          submitOpRef.current = null;
          setUI((prev) => ({ ...prev, refreshError: null }));
        } catch (refreshError) {
          if (!isCurrentOperation(
            operation,
            submitOpRef.current,
            authorityRef.current,
            operationEpochRef.current,
          )) {
            return;
          }
          submitOpRef.current = null;
          setUI((prev) => ({
            ...prev,
            committedRecovery: true,
            refreshError: refreshError instanceof Error ? refreshError.message : String(refreshError),
          }));
        }
      } else {
        submitOpRef.current = null;
        setUI((prev) => ({
          ...prev,
          submitPhase: 'rejected',
          submitError: result.error
            ?? result.transportError?.message
            ?? (result.recoverable ? '提交未确认，可重试' : '提交失败'),
          submitResult: result,
        }));
      }
    } catch (e: unknown) {
      if (!isCurrentOperation(
        operation,
        submitOpRef.current,
        authorityRef.current,
        operationEpochRef.current,
      )) {
        return;
      }
      submittingRef.current = false;
      submitOpRef.current = null;
      const msg = e instanceof Error ? e.message : '提交失败';
      setUI((prev) => ({ ...prev, submitPhase: 'rejected', submitError: msg }));
    }
  }, [
    disabled, schemaError, inputSpec, draftValue, schema, ui.submitPhase,
    ui.submitRequestId, workInput, inputRevision, workRevision, onSubmit, workId, runId,
    taskId, blockId, definitionRevision, authority.identity, onRefreshAuthoritative,
    committedRequestIds?.submit, onRequestCommitted, extra, onSubmitCommitted,
  ]);

  const lastSubmitTriggerRef = useRef(submitTrigger);
  useEffect(() => {
    if (submitTrigger === undefined || submitTrigger === lastSubmitTriggerRef.current) return;
    lastSubmitTriggerRef.current = submitTrigger;
    void handleSubmit();
  }, [submitTrigger, handleSubmit]);

  // ── Pin handler (full DTO) ──────────────────────────────────
  const doPin = useCallback(async (pin: boolean) => {
    if (disabled || pinningRef.current) return;
    if (!workInput?.id) {
      setUI((prev) => ({
        ...prev,
        pinPhase: 'error',
        pinError: '缺少权威 inputId，无法固定',
      }));
      return;
    }

    const startRev = workInput?.revision ?? inputRevision;
    const operationKind: InputOperation = pin ? 'pin' : 'unpin';
    const intentKey = JSON.stringify([authority.identity, startRev, pin]);
    const reqId = ui.pinPhase === 'error' && ui.pinRequestId
      ? ui.pinRequestId
      : stableRequestId(operationKind, intentKey, committedRequestIds?.[operationKind]);
    const operation: OperationToken = {
      epoch: operationEpochRef.current,
      identity: authority.identity,
      revision: startRev,
      requestId: reqId,
    };

    pinningRef.current = true;
    pinOpRef.current = operation;
    setUI((prev) => ({
      ...prev,
      pinPhase: 'pending',
      pinError: null,
      pinResult: null,
      pinRequestId: reqId,
      pinRecovery: false,
    }));

    const dto: SetInputCornerstoneRequest = {
      workId,
      inputId: workInput.id,
      pin,
      definitionRevision,
      inputRevision,
      expectedRevision: workRevision,
      requestId: reqId,
    };

    try {
      const handler = pin ? onPin : onUnpin;
      const result = await handler(dto);
      if (!isCurrentOperation(
        operation,
        pinOpRef.current,
        authorityRef.current,
        operationEpochRef.current,
      )) {
        return;
      }
      pinningRef.current = false;

      if (result.committed) {
        onRequestCommitted?.(operationKind, reqId);
        setUI((prev) => ({
          ...prev,
          pinPhase: result.pinned ? 'pinned' : 'unpinned',
          pinError: null,
          pinResult: result,
          pinRecovery: !!(result.error || result.transportError || !result.receipt),
        }));
        try {
          await onRefreshAuthoritative({
            workId,
            inputId: workInput.id,
            requestId: reqId,
            revision: result.revision,
            operation: pin ? 'pin' : 'unpin',
          });
          if (!isCurrentOperation(
            operation,
            pinOpRef.current,
            authorityRef.current,
            operationEpochRef.current,
          )) {
            return;
          }
          pinOpRef.current = null;
          setUI((prev) => ({ ...prev, refreshError: null }));
        } catch (refreshError) {
          if (!isCurrentOperation(
            operation,
            pinOpRef.current,
            authorityRef.current,
            operationEpochRef.current,
          )) {
            return;
          }
          pinOpRef.current = null;
          setUI((prev) => ({
            ...prev,
            pinRecovery: true,
            refreshError: refreshError instanceof Error ? refreshError.message : String(refreshError),
          }));
        }
      } else {
        pinOpRef.current = null;
        setUI((prev) => ({
          ...prev,
          pinPhase: 'error',
          pinError: result.error
            ?? result.transportError?.message
            ?? (result.recoverable ? '固定状态未确认，可重试' : '固定状态更新失败'),
          pinResult: result,
        }));
      }
    } catch (e: unknown) {
      if (!isCurrentOperation(
        operation,
        pinOpRef.current,
        authorityRef.current,
        operationEpochRef.current,
      )) {
        return;
      }
      pinningRef.current = false;
      pinOpRef.current = null;
      const msg = e instanceof Error ? e.message : `${pin ? 'Pin' : 'Unpin'} 操作失败`;
      setUI((prev) => ({ ...prev, pinPhase: 'error', pinError: msg }));
    }
  }, [
    disabled, workInput, inputRevision, workRevision, ui.pinPhase, ui.pinRequestId,
    onPin, onUnpin, workId, definitionRevision, authority.identity,
    onRefreshAuthoritative, committedRequestIds, onRequestCommitted,
  ]);

  const handlePin = useCallback(() => doPin(true), [doPin]);
  const handleUnpin = useCallback(() => doPin(false), [doPin]);

  // ── File selection ──────────────────────────────────────────
  const handleSelectFile = useCallback(async () => {
    if (!onSelectFile || ui.fileSelecting) return;
    setUI((prev) => ({ ...prev, fileSelecting: true, fileError: null }));
    try {
      const ref = await onSelectFile();
      setUI((prev) => ({ ...prev, fileSelecting: false }));
      if (ref) {
        const current: Array<string | ArtifactRef> = Array.isArray(draftValue)
          ? (draftValue as Array<string | ArtifactRef>)
          : typeof draftValue === 'string' && draftValue ? [draftValue] : [];
        const hasRef = current.some((item) => typeof item === 'string' ? item === ref.id : item.id === ref.id);
        if (!hasRef) {
          onDraftChange([...current, ref]);
        }
      }
    } catch (e: unknown) {
      setUI((prev) => ({
        ...prev,
        fileSelecting: false,
        fileError: e instanceof Error ? e.message : '文件选择失败',
      }));
    }
  }, [onSelectFile, ui.fileSelecting, draftValue, onDraftChange]);

  // ── Keyboard: Ctrl/Cmd+Enter to submit ──────────────────────
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.defaultPrevented) return;
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        e.preventDefault();
        if (onRequestGroupSubmit) {
          onRequestGroupSubmit();
        } else {
          void handleSubmit();
        }
      }
    },
    [handleSubmit, onRequestGroupSubmit],
  );

  // ── Derived display state ───────────────────────────────────
  const isPinned = ui.pinPhase === 'pinned'
    ? true
    : ui.pinPhase === 'unpinned'
      ? false
      : !!workInput?.cornerstoneId;
  const pinPending = ui.pinPhase === 'pending';
  const pinError = ui.pinPhase === 'error' ? ui.pinError : null;
  const isSubmitting = ui.submitPhase === 'submitting';
  const isCommitted = ui.submitPhase === 'committed';
  const submitError = ui.submitPhase === 'rejected' ? ui.submitError : null;
  const showCommittedRecovery = ui.committedRecovery && isCommitted;
  const showPinRecovery = ui.pinRecovery
    && (ui.pinPhase === 'pinned' || ui.pinPhase === 'unpinned');

  const controlID = `wg2-wh-input-${workId}-${taskId}-${inputSpec.id}`;
  const errorID = `wg2-wh-error-${workId}-${taskId}-${inputSpec.id}`;
  const descID = inputSpec.description
    ? `wg2-wh-desc-${workId}-${taskId}-${inputSpec.id}`
    : undefined;

  const describedBy = [descID, visibleValidationError || submitError || schemaError ? errorID : null]
    .filter(Boolean)
    .join(' ') || undefined;

  const testIdSuffix = `${taskId}-${inputSpec.id}`;

  // ── Schema error boundary render ────────────────────────────
  if (schemaError) {
    return (
      <div
        className="wg2-wh-host wg2-wh-schema-err"
        data-testid={`work-input-host-${taskId}-${inputSpec.id}`}
        role="alert"
      >
        <span className="wg2-wh-error-icon" aria-hidden="true">⚠</span>
        <span className="wg2-wh-error-msg">{schemaError}</span>
      </div>
    );
  }

  const controlProps: TypedControlProps = {
    id: controlID,
    inputSpec,
    schema,
    draftValue,
    onDraftChange,
    disabled: disabled || isSubmitting,
    describedBy,
    onKeyDown: handleKeyDown,
    onSubmit: handleSubmit,
    testIdSuffix,
    workId,
    onSelectFile,
    onSelectFileTrigger: handleSelectFile,
    fileSelecting: ui.fileSelecting,
    fileError: ui.fileError,
  };

  return (
    <div
      className="wg2-wh-host"
      data-testid={`work-input-host-${taskId}-${inputSpec.id}`}
      data-input-kind={inputSpec.kind}
      data-input-state={workInput?.state ?? 'requested'}
      data-pinned={isPinned ? 'true' : 'false'}
      role="group"
      aria-labelledby={hideHeader ? undefined : `wg2-wh-label-${workId}-${taskId}-${inputSpec.id}`}
      aria-label={hideHeader ? inputSpec.label : undefined}
      onKeyDown={handleKeyDown}
    >
      {/* ── Label row ─────────────────────────────────────────── */}
      {!hideHeader && <div className="wg2-wh-label-row">
        <label
          id={`wg2-wh-label-${workId}-${taskId}-${inputSpec.id}`}
          htmlFor={controlID}
          className="wg2-wh-label"
        >
          {inputSpec.label}
        </label>
        {inputSpec.required && (
          <span className="wg2-wh-required" aria-label="必填">*</span>
        )}
        <span className="wg2-wh-kind-badge">{kindLabel(inputSpec.kind)}</span>

        {inputSpec.pinEligible && (
          <button
            type="button"
            className={`wg2-wh-pin-btn ${isPinned ? 'wg2-wh-pin-active' : ''} ${pinPending ? 'wg2-wh-pin-pending' : ''}`}
            onClick={isPinned ? handleUnpin : handlePin}
            disabled={disabled || pinPending}
            aria-label={isPinned ? `取消固定 ${inputSpec.label}` : `固定 ${inputSpec.label} (Pin)`}
            aria-pressed={isPinned}
            data-testid={`work-input-pin-${taskId}-${inputSpec.id}`}
            tabIndex={0}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                isPinned ? handleUnpin() : handlePin();
              }
            }}
          >
            {isPinned ? '📌' : '📍'}
          </button>
        )}
      </div>}

      {/* Description */}
      {inputSpec.description && (
        <p id={descID} className="wg2-wh-desc" data-testid={`work-input-desc-${taskId}-${inputSpec.id}`}>
          {inputSpec.description}
        </p>
      )}

      {/* ── Typed control ────────────────────────────────────── */}
      <TypedControl {...controlProps} />

      {/* ── Validation / submit error ────────────────────────── */}
      {(visibleValidationError || submitError) && (
        <div
          id={errorID}
          className="wg2-wh-error"
          role="alert"
          data-testid={`work-input-error-${taskId}-${inputSpec.id}`}
        >
          <span className="wg2-wh-error-icon" aria-hidden="true">⚠</span>
          <span className="wg2-wh-error-msg">{submitError ?? visibleValidationError}</span>
        </div>
      )}
      {advisoryWarning && !visibleValidationError && !submitError && (
        <div
          className="wg2-wh-warning"
          role="status"
          data-testid={`work-input-warning-${taskId}-${inputSpec.id}`}
        >
          <TriangleAlert className="wg2-wh-error-icon" size={14} aria-hidden="true" />
          <span>{advisoryWarning}</span>
        </div>
      )}

      {/* ── Pin error (independent, never clears input) ──────── */}
      {pinError && (
        <div
          className="wg2-wh-pin-error"
          role="alert"
          data-testid={`work-input-pin-error-${taskId}-${inputSpec.id}`}
        >
          <span className="wg2-wh-error-icon" aria-hidden="true">📍</span>
          <span className="wg2-wh-error-msg">{pinError}</span>
          <button
            type="button"
            className="wg2-wh-retry-btn"
            onClick={isPinned ? handleUnpin : handlePin}
            data-testid={`work-input-pin-retry-${taskId}-${inputSpec.id}`}
          >
            重试
          </button>
        </div>
      )}

      {/* ── Committed recovery notice ────────────────────────── */}
      {showCommittedRecovery && (
        <div
          className="wg2-wh-recovery"
          role="status"
          data-testid={`work-input-recovery-${taskId}-${inputSpec.id}`}
        >
          {ui.submitResult?.receipt
            ? `⚡ 已通过 receipt 恢复提交状态（${ui.submitResult.receipt.requestId} · r${ui.submitResult.receipt.resultRevision}）`
            : '⚠ 输入已提交，但响应缺少 InputIntentReceipt；正在刷新权威状态'}
          {ui.refreshError ? `；刷新失败：${ui.refreshError}` : ''}
        </div>
      )}

      {showPinRecovery && (
        <div
          className="wg2-wh-recovery"
          role="status"
          data-testid={`work-input-pin-recovery-${taskId}-${inputSpec.id}`}
        >
          {ui.pinResult?.receipt
            ? `⚡ 固定状态已提交${ui.pinResult.duplicate ? '（重复请求）' : ''}（${ui.pinResult.receipt.requestId} · r${ui.pinResult.receipt.resultRevision}）`
            : '⚠ 固定状态已提交，但响应缺少 InputIntentReceipt；正在刷新权威状态'}
          {ui.refreshError ? `；刷新失败：${ui.refreshError}` : ''}
        </div>
      )}

      {/* ── Actions ──────────────────────────────────────────── */}
      {!hideSubmit && (
        <div className="wg2-wh-actions">
          <button
            type="button"
            className={`wg2-wh-submit-btn ${isSubmitting ? 'wg2-wh-submit-pending' : ''}`}
            disabled={disabled || isSubmitting || !!validationError}
            onClick={handleSubmit}
            aria-busy={isSubmitting}
            data-testid={`work-input-submit-${taskId}-${inputSpec.id}`}
          >
            {isSubmitting ? '提交中...' : isCommitted ? '已提交 ✓' : (submitLabel ?? '提交')}
          </button>
          {(isCommitted || ui.submitResult) && ui.submitResult?.revision !== undefined && (
            <span
              className="wg2-wh-revision"
              data-testid={`work-input-rev-${taskId}-${inputSpec.id}`}
            >
              r{ui.submitResult.revision}
              {ui.submitResult?.duplicate ? ' (重复)' : ''}
            </span>
          )}
        </div>
      )}
    </div>
  );
};

// ── Typed control router ───────────────────────────────────────────────────

interface TypedControlProps {
  id: string;
  inputSpec: InputSpec;
  schema: ParsedValueSchema;
  draftValue: DraftValue;
  onDraftChange: (value: DraftValue) => void;
  disabled: boolean;
  describedBy: string | undefined;
  onKeyDown: (e: React.KeyboardEvent) => void;
  onSubmit: () => void;
  testIdSuffix: string;
  workId: string;
  onSelectFile?: () => Promise<ArtifactRef | null>;
  onSelectFileTrigger: () => void;
  fileSelecting: boolean;
  fileError: string | null;
}

const LEGACY_MULTILINE_HINT =
  /(?:每行|逐行|换行|多行|列表|one\s+.+\s+per\s+line|line[- ]separated|multiline|newlines?|\blist\b)/i;

function shouldUseMultilineText(inputSpec: InputSpec, schema: ParsedValueSchema): boolean {
  if (schema.text?.multiline !== undefined) return schema.text.multiline;
  // Compatibility for definitions created before valueSchema.multiline existed.
  return LEGACY_MULTILINE_HINT.test(`${inputSpec.label}\n${inputSpec.description ?? ''}`);
}

const TypedControl: React.FC<TypedControlProps> = ({
  id,
  inputSpec,
  schema,
  draftValue,
  onDraftChange,
  disabled,
  describedBy,
  onKeyDown,
  testIdSuffix,
  onSelectFile,
  onSelectFileTrigger,
  fileSelecting,
  fileError,
}) => {
  const ariaInvalid = !!(describedBy?.includes('wg2-wh-error'));

  switch (inputSpec.kind) {
    // ── Text ──────────────────────────────────────────────────
    case 'text': {
      const value = typeof draftValue === 'string' ? draftValue : String(draftValue ?? '');
      if (shouldUseMultilineText(inputSpec, schema)) {
        return (
          <textarea
            id={id}
            className="wg2-wh-text wg2-wh-textarea"
            value={value}
            rows={4}
            placeholder={schema.text?.pattern ? `匹配: ${schema.text.pattern}` : undefined}
            aria-describedby={describedBy}
            disabled={disabled}
            aria-required={inputSpec.required}
            aria-invalid={ariaInvalid || undefined}
            onChange={(e) => onDraftChange(e.currentTarget.value)}
            onKeyDown={onKeyDown}
            data-testid={`work-input-control-${testIdSuffix}`}
          />
        );
      }
      return (
        <input
          id={id}
          className="wg2-wh-text"
          type="text"
          value={value}
          placeholder={schema.text?.pattern ? `匹配: ${schema.text.pattern}` : undefined}
          aria-describedby={describedBy}
          disabled={disabled}
          aria-required={inputSpec.required}
          aria-invalid={ariaInvalid || undefined}
          onChange={(e) => onDraftChange(e.currentTarget.value)}
          onKeyDown={onKeyDown}
          data-testid={`work-input-control-${testIdSuffix}`}
        />
      );
    }

    // ── Number ────────────────────────────────────────────────
    case 'number': {
      const numSchema = schema.number;
      const unitLabel = numSchema?.unit === 'amount' && numSchema?.currency
        ? `${numSchema.currency} `
        : numSchema?.unit === 'percent' ? '%' : '';
      return (
        <div className="wg2-wh-number-wrap">
          {unitLabel && <span className="wg2-wh-number-unit">{unitLabel}</span>}
          <input
            id={id}
            className="wg2-wh-number"
            type="number"
            step="any"
            value={typeof draftValue === 'number' ? draftValue : (draftValue ?? '') as string | number}
            aria-describedby={describedBy}
            disabled={disabled}
            aria-required={inputSpec.required}
            aria-invalid={ariaInvalid || undefined}
            onChange={(e) => {
              const raw = e.currentTarget.value;
              if (raw === '') { onDraftChange(''); return; }
              const n = parseFloat(raw);
              onDraftChange(Number.isNaN(n) ? '' : n);
            }}
            onKeyDown={onKeyDown}
            data-testid={`work-input-control-${testIdSuffix}`}
          />
        </div>
      );
    }

    // ── Date ──────────────────────────────────────────────────
    case 'date': {
      const dateSchema = schema.date;
      const mode = dateSchema?.mode ?? 'date';

      if (mode === 'range') {
        const rangeVal = (draftValue && typeof draftValue === 'object' && !Array.isArray(draftValue))
          ? draftValue as { start: string; end: string }
          : { start: '', end: '' };
        return (
          <div className="wg2-wh-date-range" data-testid={`work-input-control-${testIdSuffix}`}>
            <input
              id={`${id}-start`}
              className="wg2-wh-date"
              type="date"
              value={rangeVal.start}
              aria-label="开始日期"
              aria-describedby={describedBy}
              disabled={disabled}
              onChange={(e) => onDraftChange({ ...rangeVal, start: e.currentTarget.value })}
              onKeyDown={onKeyDown}
              data-testid={`work-input-control-${testIdSuffix}-start`}
            />
            <span className="wg2-wh-date-sep">—</span>
            <input
              id={`${id}-end`}
              className="wg2-wh-date"
              type="date"
              value={rangeVal.end}
              aria-label="结束日期"
              aria-describedby={describedBy}
              disabled={disabled}
              onChange={(e) => onDraftChange({ ...rangeVal, end: e.currentTarget.value })}
              onKeyDown={onKeyDown}
              data-testid={`work-input-control-${testIdSuffix}-end`}
            />
          </div>
        );
      }

      const inputType = mode === 'time' ? 'time' : mode === 'datetime' ? 'datetime-local' : 'date';
      return (
        <input
          id={id}
          className="wg2-wh-date"
          type={inputType}
          value={typeof draftValue === 'string' ? draftValue : ''}
          aria-describedby={describedBy}
          disabled={disabled}
          aria-required={inputSpec.required}
          aria-invalid={ariaInvalid || undefined}
          onChange={(e) => onDraftChange(e.currentTarget.value)}
          onKeyDown={onKeyDown}
          data-testid={`work-input-control-${testIdSuffix}`}
        />
      );
    }

    // ── Choice ────────────────────────────────────────────────
    case 'choice': {
      const options = schema.choice?.options ?? [];
      const allowOther = schema.choice?.allowOther ?? false;
      const strVal = typeof draftValue === 'string' ? draftValue : '';
      const inOptions = options.some((o) => o.value === strVal);
      if (options.length === 0) {
        return (
          <input
            id={id}
            className="wg2-wh-text"
            type="text"
            value={strVal}
            placeholder="请输入内容"
            aria-describedby={describedBy}
            disabled={disabled}
            aria-required={inputSpec.required}
            aria-invalid={ariaInvalid || undefined}
            onChange={(e) => onDraftChange(e.currentTarget.value)}
            onKeyDown={onKeyDown}
            data-testid={`work-input-control-${testIdSuffix}-manual`}
          />
        );
      }
      return (
        <div className="wg2-wh-choice-wrap" data-testid={`work-input-control-${testIdSuffix}`}>
          <select
            id={id}
            className="wg2-wh-select"
            value={inOptions ? strVal : ''}
            aria-describedby={describedBy}
            disabled={disabled}
            aria-required={inputSpec.required}
            aria-invalid={ariaInvalid || undefined}
            onChange={(e) => onDraftChange(e.currentTarget.value)}
            onKeyDown={onKeyDown}
            data-testid={`work-input-control-${testIdSuffix}-select`}
          >
            <option value="" disabled>— 请选择 —</option>
            {options.map((opt) => (
              <option key={opt.value} value={opt.value}>{opt.label}</option>
            ))}
          </select>
          {allowOther && (
            <input
              id={`${id}-other`}
              className="wg2-wh-text"
              type="text"
              value={inOptions ? '' : strVal}
              placeholder="其他..."
              aria-label="自定义输入"
              disabled={disabled}
              onChange={(e) => onDraftChange(e.currentTarget.value)}
              onKeyDown={onKeyDown}
              data-testid={`work-input-control-${testIdSuffix}-other`}
            />
          )}
        </div>
      );
    }

    // ── MultiChoice ───────────────────────────────────────────
    case 'multi_choice': {
      const options = schema.multiChoice?.options ?? [];
      const selected: string[] = Array.isArray(draftValue) ? (draftValue as unknown as string[]) : [];
      const allowOther = schema.multiChoice?.allowOther ?? false;
      const otherVal = selected.filter((s) => !options.some((o) => o.value === s));
      if (options.length === 0) {
        const manualValue = typeof draftValue === 'string' ? draftValue : selected.join('\n');
        return (
          <textarea
            id={id}
            className="wg2-wh-textarea"
            rows={3}
            value={manualValue}
            placeholder="每行填写一项"
            aria-describedby={describedBy}
            disabled={disabled}
            aria-required={inputSpec.required}
            aria-invalid={ariaInvalid || undefined}
            onChange={(e) => onDraftChange(e.currentTarget.value)}
            onKeyDown={onKeyDown}
            data-testid={`work-input-control-${testIdSuffix}-manual`}
          />
        );
      }
      return (
        <fieldset
          className="wg2-wh-multichoice-fs"
          data-testid={`work-input-control-${testIdSuffix}`}
          aria-describedby={describedBy}
          disabled={disabled}
        >
          {options.map((opt) => (
            <label key={opt.value} className="wg2-wh-check-label">
              <input
                type="checkbox"
                className="wg2-wh-check"
                checked={selected.includes(opt.value)}
                disabled={disabled}
                aria-label={opt.label}
                onChange={(e) => {
                  if (e.currentTarget.checked) onDraftChange([...selected, opt.value]);
                  else onDraftChange(selected.filter((s) => s !== opt.value));
                }}
                onKeyDown={onKeyDown}
                data-testid={`work-input-control-${testIdSuffix}-opt-${opt.value}`}
              />
              <span>{opt.label}</span>
            </label>
          ))}
          {allowOther && (
            <input
              className="wg2-wh-text wg2-wh-multichoice-other"
              type="text"
              placeholder="其他..."
              value={otherVal.join(', ')}
              disabled={disabled}
              onChange={(e) => {
                const base = selected.filter((s) => options.some((o) => o.value === s));
                const val = e.currentTarget.value;
                onDraftChange(val ? [...base, val] : base);
              }}
              onKeyDown={onKeyDown}
              data-testid={`work-input-control-${testIdSuffix}-other`}
            />
          )}
        </fieldset>
      );
    }

    // ── Roster ────────────────────────────────────────────────
    case 'roster': {
      const entries: Array<Record<string, string>> = Array.isArray(draftValue) ? (draftValue as unknown as Array<Record<string, string>>) : [];
      const fields = schema.roster?.fields ?? (entries.length > 0 ? Object.keys(entries[0]) : []);
      return (
        <div className="wg2-wh-roster" data-testid={`work-input-control-${testIdSuffix}`}>
          {entries.map((entry, i) => (
            <div key={i} className="wg2-wh-roster-entry" data-testid={`work-input-control-${testIdSuffix}-entry-${i}`}>
              {fields.map((field) => (
                <input
                  key={field}
                  className="wg2-wh-text"
                  type="text"
                  placeholder={field}
                  value={entry[field] ?? ''}
                  aria-label={`${field} #${i + 1}`}
                  disabled={disabled}
                  onChange={(ev) => {
                    const next = entries.map((entry, j) => j === i ? { ...entry, [field]: ev.currentTarget.value } : entry);
                    onDraftChange(next);
                  }}
                  onKeyDown={onKeyDown}
                  data-testid={`work-input-control-${testIdSuffix}-entry-${i}-${field}`}
                />
              ))}
              <button
                type="button"
                className="wg2-wh-roster-remove"
                disabled={disabled}
                aria-label={`删除条目 #${i + 1}`}
                onClick={() => onDraftChange(entries.filter((_, j) => j !== i))}
                data-testid={`work-input-control-${testIdSuffix}-remove-${i}`}
              >
                ✕
              </button>
            </div>
          ))}
          <button
            type="button"
            className="wg2-wh-roster-add"
            disabled={disabled}
            onClick={() => {
              const entry: Record<string, string> = {};
              for (const f of fields) entry[f] = '';
              onDraftChange([...entries, entry]);
            }}
            data-testid={`work-input-control-${testIdSuffix}-add`}
          >
            + 添加
          </button>
        </div>
      );
    }

    // ── Form ──────────────────────────────────────────────────
    case 'form': {
      const formFields = schema.form?.fields ?? [];
      const obj = (draftValue && typeof draftValue === 'object' && !Array.isArray(draftValue))
        ? draftValue as Record<string, unknown>
        : {};
      return (
        <fieldset
          className="wg2-wh-form-fs"
          data-testid={`work-input-control-${testIdSuffix}`}
          aria-describedby={describedBy}
          disabled={disabled}
        >
          {formFields.map((field) => {
            const fv = obj[field.id] ?? '';
            // Parse field-level schema for typed control routing
            let fSchema: ParsedValueSchema;
            try {
              fSchema = parseValueSchema(field.id, field.kind, field.valueSchema);
            } catch {
              fSchema = { kind: field.kind };
            }
            const fErr = validateFormField(field, fv);
            return (
              <div key={field.id} className="wg2-wh-form-field">
                <label className="wg2-wh-form-label" htmlFor={`${id}-${field.id}`}>
                  {field.label}
                  {field.required && <span className="wg2-wh-required">*</span>}
                </label>
                <FormFieldControl
                  id={`${id}-${field.id}`}
                  field={field}
                  schema={fSchema}
                  value={fv}
                  disabled={disabled}
                  error={fErr}
                  testIdSuffix={`${testIdSuffix}-${field.id}`}
                  onKeyDown={onKeyDown}
                  onChange={(val) => onDraftChange({ ...obj, [field.id]: val })}
                />
                {fErr && (
                  <span className="wg2-wh-form-err" role="alert" data-testid={`work-input-control-${testIdSuffix}-${field.id}-err`}>
                    {fErr}
                  </span>
                )}
              </div>
            );
          })}
        </fieldset>
      );
    }

    // ── File ──────────────────────────────────────────────────
    case 'file': {
      const fileRefs: Array<string | ArtifactRef> = Array.isArray(draftValue)
        ? (draftValue as Array<string | ArtifactRef>)
        : typeof draftValue === 'string' && draftValue
          ? [draftValue as string]
          : [];
      const hasSelector = !!onSelectFile;
      return (
        <div className="wg2-wh-file" data-testid={`work-input-control-${testIdSuffix}`}>
          {fileRefs.length === 0 && !hasSelector && (
            <span className="wg2-wh-file-empty">未选择文件</span>
          )}
          {fileRefs.map((ref, i) => {
            const refId = typeof ref === 'string' ? ref : ref.id;
            const refName = typeof ref === 'string' ? ref : ref.name;
            return (
            <div key={refId} className="wg2-wh-file-ref" data-testid={`work-input-control-${testIdSuffix}-ref-${i}`}>
              <span className="wg2-wh-file-name">{refName}</span>
              <button
                type="button"
                className="wg2-wh-file-remove"
                disabled={disabled}
                aria-label={`移除 ${refName}`}
                onClick={() => onDraftChange(fileRefs.filter((_, j) => j !== i))}
                data-testid={`work-input-control-${testIdSuffix}-remove-${i}`}
              >
                ✕
              </button>
            </div>
          );})}
          {hasSelector && (
            <div className="wg2-wh-file-actions">
              <button
                type="button"
                className="wg2-wh-file-select-btn"
                disabled={disabled || fileSelecting}
                onClick={(e) => { e.preventDefault(); onSelectFileTrigger(); }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelectFileTrigger(); }
                }}
                aria-busy={fileSelecting}
                data-testid={`work-input-control-${testIdSuffix}-select`}
              >
                {fileSelecting ? '选择中...' : '选择文件'}
              </button>
              {fileError && (
                <span className="wg2-wh-file-err" role="alert" data-testid={`work-input-control-${testIdSuffix}-file-err`}>
                  {fileError}
                </span>
              )}
            </div>
          )}
          {!hasSelector && <span className="wg2-wh-file-hint">通过宿主选择文件</span>}
        </div>
      );
    }

    // ── Approval ──────────────────────────────────────────────
    case 'approval': {
      const risk = schema.approval?.riskLevel;
      const approved = draftValue === 'approved';
      const rejected = draftValue === 'rejected';
      return (
        <div
          className={`wg2-wh-approval ${risk ? `wg2-wh-approval-${risk}` : ''}`}
          data-testid={`work-input-control-${testIdSuffix}`}
        >
          {schema.approval?.description && (
            <p className="wg2-wh-approval-desc">{schema.approval.description}</p>
          )}
          {risk && (
            <span className={`wg2-wh-approval-risk wg2-wh-risk-${risk}`}>
              风险等级: {riskLabel(risk)}
            </span>
          )}
          <div className="wg2-wh-approval-btns">
            <button
              type="button"
              className={`wg2-wh-approval-btn wg2-wh-approval-accept ${approved ? 'wg2-wh-approval-chosen' : ''}`}
              disabled={disabled}
              aria-pressed={approved}
              aria-label="批准"
              onClick={() => onDraftChange('approved')}
              data-testid={`work-input-control-${testIdSuffix}-approved`}
            >
              ✓ 批准
            </button>
            <button
              type="button"
              className={`wg2-wh-approval-btn wg2-wh-approval-reject ${rejected ? 'wg2-wh-approval-chosen' : ''}`}
              disabled={disabled}
              aria-pressed={rejected}
              aria-label="拒绝"
              onClick={() => onDraftChange('rejected')}
              data-testid={`work-input-control-${testIdSuffix}-rejected`}
            >
              ✗ 拒绝
            </button>
          </div>
          {!approved && !rejected && (
            <span className="wg2-wh-approval-pending" aria-live="polite">
              请明确选择批准或拒绝
            </span>
          )}
        </div>
      );
    }

    default: {
      return (
        <div className="wg2-wh-unknown" role="alert" data-testid={`work-input-control-${testIdSuffix}`}>
          <span className="wg2-wh-error-icon" aria-hidden="true">⚠</span>
          未知输入类型: {inputSpec.kind}
        </div>
      );
    }
  }
};

// ── Form field control (reuses typed routing) ──────────────────────────────

interface FormFieldControlProps {
  id: string;
  field: FormFieldSpec;
  schema: ParsedValueSchema;
  value: unknown;
  disabled: boolean;
  error: string | null;
  testIdSuffix: string;
  onKeyDown: (e: React.KeyboardEvent) => void;
  onChange: (value: unknown) => void;
}

const FormFieldControl: React.FC<FormFieldControlProps> = ({
  id,
  field,
  schema,
  value,
  disabled,
  error,
  testIdSuffix,
  onKeyDown,
  onChange,
}) => {
  const ariaInvalid = !!error;

  switch (field.kind) {
    case 'text':
      return (
        <input
          id={id}
          className="wg2-wh-text"
          type="text"
          value={typeof value === 'string' ? value : String(value ?? '')}
          disabled={disabled}
          aria-required={field.required}
          aria-invalid={ariaInvalid || undefined}
          placeholder={schema.text?.pattern ? `匹配: ${schema.text.pattern}` : undefined}
          onChange={(e) => onChange(e.currentTarget.value)}
          onKeyDown={onKeyDown}
          data-testid={`work-input-control-${testIdSuffix}`}
        />
      );

    case 'number':
      return (
        <input
          id={id}
          className="wg2-wh-number"
          type="number"
          step={schema.number?.integer ? '1' : 'any'}
          min={schema.number?.min ?? undefined}
          max={schema.number?.max ?? undefined}
          value={typeof value === 'number' ? value : (value ?? '') as string | number}
          disabled={disabled}
          aria-required={field.required}
          aria-invalid={ariaInvalid || undefined}
          onChange={(e) => {
            const raw = e.currentTarget.value;
            if (raw === '') { onChange(''); return; }
            const n = parseFloat(raw);
            onChange(Number.isNaN(n) ? '' : n);
          }}
          onKeyDown={onKeyDown}
          data-testid={`work-input-control-${testIdSuffix}`}
        />
      );

    case 'date': {
      const mode = schema.date?.mode ?? 'date';
      const inputType = mode === 'time' ? 'time' : mode === 'datetime' ? 'datetime-local' : 'date';
      return (
        <input
          id={id}
          className="wg2-wh-date"
          type={inputType}
          value={typeof value === 'string' ? value : ''}
          disabled={disabled}
          aria-required={field.required}
          aria-invalid={ariaInvalid || undefined}
          onChange={(e) => onChange(e.currentTarget.value)}
          onKeyDown={onKeyDown}
          data-testid={`work-input-control-${testIdSuffix}`}
        />
      );
    }

    case 'choice': {
      const options = schema.choice?.options ?? [];
      const strVal = typeof value === 'string' ? value : '';
      const inOptions = options.some((o) => o.value === strVal);
      return (
        <select
          id={id}
          className="wg2-wh-select"
          value={inOptions ? strVal : ''}
          disabled={disabled}
          aria-required={field.required}
          aria-invalid={ariaInvalid || undefined}
          onChange={(e) => onChange(e.currentTarget.value)}
          onKeyDown={onKeyDown}
          data-testid={`work-input-control-${testIdSuffix}`}
        >
          <option value="" disabled>— 请选择 —</option>
          {options.map((opt) => (
            <option key={opt.value} value={opt.value}>{opt.label}</option>
          ))}
        </select>
      );
    }

    case 'approval': {
      const approved = value === 'approved';
      const rejected = value === 'rejected';
      return (
        <div className="wg2-wh-approval-btns" data-testid={`work-input-control-${testIdSuffix}`}>
          <button
            type="button"
            className={`wg2-wh-approval-btn wg2-wh-approval-accept ${approved ? 'wg2-wh-approval-chosen' : ''}`}
            disabled={disabled}
            aria-pressed={approved}
            onClick={() => onChange('approved')}
            data-testid={`work-input-control-${testIdSuffix}-approved`}
          >
            ✓ 批准
          </button>
          <button
            type="button"
            className={`wg2-wh-approval-btn wg2-wh-approval-reject ${rejected ? 'wg2-wh-approval-chosen' : ''}`}
            disabled={disabled}
            aria-pressed={rejected}
            onClick={() => onChange('rejected')}
            data-testid={`work-input-control-${testIdSuffix}-rejected`}
          >
            ✗ 拒绝
          </button>
        </div>
      );
    }

    default:
      // Fallback: text input for any other kind
      return (
        <input
          id={id}
          className="wg2-wh-text"
          type="text"
          value={typeof value === 'string' ? value : String(value ?? '')}
          disabled={disabled}
          onChange={(e) => onChange(e.currentTarget.value)}
          onKeyDown={onKeyDown}
          data-testid={`work-input-control-${testIdSuffix}`}
        />
      );
  }
};

// ── Helpers ─────────────────────────────────────────────────────────────────

function riskLabel(risk: string): string {
  switch (risk) {
    case 'low': return '低';
    case 'medium': return '中';
    case 'high': return '高';
    case 'critical': return '严重';
    default: return risk;
  }
}
