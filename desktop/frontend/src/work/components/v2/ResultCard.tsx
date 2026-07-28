import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  AlertTriangle,
  Archive,
  CheckCircle2,
  Clock3,
  Download,
  ExternalLink,
  Eye,
  EyeOff,
  File,
  FileImage,
  FileSpreadsheet,
  FileText,
  FolderOpen,
  LoaderCircle,
  Presentation,
  RefreshCw,
} from 'lucide-react';

import type {
  ArtifactSlot,
  ArtifactPreview,
  RequestArtifactConversionInput,
} from '../../types_v2';
import type { ArtifactRef } from '../../types';

// ── Handler intents (no direct Wails / file-system calls) ──────────────────

export interface FilePreviewIntent {
  workId: string;
  definitionRevision: number;
  slotId: string;
  slotRevision: number;
  artifactId: string;
  requestId: string;
}

export type FileConversionIntent = RequestArtifactConversionInput;

export interface FileOpenIntent {
  workId: string;
  definitionRevision: number;
  slotRevision: number;
  slotId: string;
  artifactRefId: string;
  path: string;
}

export interface FileDownloadIntent {
  workId: string;
  definitionRevision: number;
  slotRevision: number;
  slotId: string;
  artifactRefId: string;
  path: string;
  name: string;
}

export interface FileLocateIntent {
  workId: string;
  definitionRevision: number;
  slotRevision: number;
  slotId: string;
  artifactRefId: string;
  path: string;
}

export interface SlotRetryIntent {
  workId: string;
  definitionRevision: number;
  slotId: string;
  revision: number;
}

// ── ResultCard ─────────────────────────────────────────────────────────────

export interface ResultCardProps {
  slot: ArtifactSlot;
  /** Called when the user wants to open a file with the system handler.
   *  May return void or Promise<void>. */
  onOpen?: (intent: FileOpenIntent) => void | Promise<void>;
  /** Called when the user wants to download/save a file.
   *  May return void or Promise<void>. */
  onDownload?: (intent: FileDownloadIntent) => void | Promise<void>;
  /** Called when the user wants to locate a file on disk.
   *  May return void or Promise<void>. */
  onLocate?: (intent: FileLocateIntent) => void | Promise<void>;
  /** Called when the user wants to retry a failed slot.
   *  May return void or Promise<void>. */
  onRetry?: (intent: SlotRetryIntent) => void | Promise<void>;
  /** Called when the user wants to preview a file in-app.
   *  Returns the ArtifactPreview for inline rendering. */
  onPreview?: (intent: FilePreviewIntent) => Promise<ArtifactPreview>;
  /** Requests a durable, idempotent local conversion. Repeated calls with the
   * same intent poll the authoritative receipt; external conversion remains
   * unavailable until a separate approval flow supplies a real token. */
  onConvert?: (intent: FileConversionIntent) => Promise<ArtifactPreview>;
  /** Current preview state for this card (managed by parent). */
  preview?: ArtifactPreview | null;
}

const STATE_LABELS: Record<ArtifactSlot['state'], string> = {
  reserved: '待生成',
  generating: '生成中',
  ready: '已完成',
  partial: '部分完成',
  failed: '失败',
  stale: '已过期',
};

function fileIcon(kind: string, type?: string): React.ReactNode {
  const key = `${type ?? ''} ${kind}`.toLowerCase();
  if (key.includes('image')) return <FileImage size={18} />;
  if (key.includes('sheet') || key.includes('xls') || key.includes('excel')) {
    return <FileSpreadsheet size={18} />;
  }
  if (key.includes('present') || key.includes('ppt')) return <Presentation size={18} />;
  if (key.includes('zip') || key.includes('tar') || key.includes('gzip')) return <Archive size={18} />;
  if (
    key.includes('pdf') ||
    key.includes('word') ||
    key.includes('doc') ||
    key.includes('text') ||
    key.includes('markdown')
  ) {
    return <FileText size={18} />;
  }
  return <File size={18} />;
}

function artifactTone(kind: string, type?: string): string {
  const key = `${type ?? ''} ${kind}`.toLowerCase();
  if (key.includes('pdf')) return 'pdf';
  if (key.includes('sheet') || key.includes('xls') || key.includes('excel')) return 'sheet';
  if (key.includes('present') || key.includes('ppt') || key.includes('image')) return 'image';
  if (key.includes('zip') || key.includes('tar') || key.includes('gzip')) return 'archive';
  if (key.includes('word') || key.includes('doc') || key.includes('text') || key.includes('markdown')) return 'document';
  return 'generic';
}

function stateIcon(state: ArtifactSlot['state']): React.ReactNode {
  switch (state) {
    case 'ready':
      return <CheckCircle2 size={13} />;
    case 'generating':
      return <LoaderCircle size={13} className="wg2-rc-spin" />;
    case 'failed':
    case 'partial':
      return <AlertTriangle size={13} />;
    case 'stale':
      return <RefreshCw size={13} />;
    case 'reserved':
      return <Clock3 size={13} />;
  }
}

// ── file action key helpers ────────────────────────────────────────────────

type FileActionKind = 'open' | 'download' | 'locate';

function fileActionKey(refId: string, kind: FileActionKind): string {
  return `${refId}-${kind}`;
}

function retryActionKey(slotId: string): string {
  return `retry-${slotId}`;
}

// ── FileActions sub-component ──────────────────────────────────────────────

interface FileActionsProps {
  refInfo: ArtifactRef;
  available: Record<FileActionKind, boolean>;
  inFlight: Record<string, boolean>;
  actionErrors: Record<string, string>;
  onFire: (refId: string, kind: FileActionKind) => void;
}

const FileActions: React.FC<FileActionsProps> = ({
  refInfo,
  available,
  inFlight,
  actionErrors,
  onFire,
}) => {
  // Production open/locate ports are workspace-scoped. An absolute path is
  // never forwarded through this component; refs without an authorised
  // relative path intentionally have no host actions.
  const hasPath = Boolean(refInfo.relativePath);
  const canOpen = available.open && hasPath && refInfo.status !== 'missing';
  const canDownload = available.download && hasPath;
  const canLocate = available.locate && hasPath;

  function actionLabel(kind: FileActionKind): string {
    switch (kind) {
      case 'open': return '打开';
      case 'download': return '下载';
      case 'locate': return '定位';
    }
  }

  function actionIcon(kind: FileActionKind): React.ReactNode {
    switch (kind) {
      case 'open': return <ExternalLink size={12} />;
      case 'download': return <Download size={12} />;
      case 'locate': return <FolderOpen size={12} />;
    }
  }

  function renderButton(kind: FileActionKind, enabled: boolean) {
    const key = fileActionKey(refInfo.id, kind);
    const busy = inFlight[key] === true;
    const error = actionErrors[key];

    return (
      <React.Fragment key={kind}>
        {enabled && (
          <button
            type="button"
            className={`wg2-rc-file-btn${busy ? ' wg2-rc-file-btn--busy' : ''}`}
            onClick={() => onFire(refInfo.id, kind)}
            disabled={busy}
            aria-busy={busy ? 'true' : undefined}
            aria-label={`${actionLabel(kind)} ${refInfo.name ?? refInfo.id}`}
            data-testid={`rc-file-${kind}-${refInfo.id}`}
          >
            {busy ? <LoaderCircle size={12} className="wg2-rc-spin" /> : actionIcon(kind)}
            <span>{busy ? '处理中' : actionLabel(kind)}</span>
          </button>
        )}
        {error && (
          <div
            className="wg2-rc-action-error"
            role="alert"
            data-testid={`rc-action-error-${key}`}
          >
            {error}
          </div>
        )}
      </React.Fragment>
    );
  }

  return (
    <span className="wg2-rc-file-actions">
      {renderButton('open', canOpen)}
      {renderButton('download', canDownload)}
      {renderButton('locate', canLocate)}
    </span>
  );
};

// ── ResultCard ─────────────────────────────────────────────────────────────

export const ResultCard: React.FC<ResultCardProps> = ({
  slot,
  onOpen,
  onDownload,
  onLocate,
  onRetry,
  onPreview,
  onConvert,
  preview,
}) => {
  // Ref-backed state: ref is the authority, state triggers re-renders.
  const inFlightRef = useRef<Record<string, boolean>>({});
  const [inFlightRender, setInFlightRender] = useState<Record<string, boolean>>({});

  const actionErrorsRef = useRef<Record<string, string>>({});
  const [actionErrorsRender, setActionErrorsRender] = useState<Record<string, string>>({});

  // Local preview state — per-artifact, epoch-gated.
  const [localPreview, setLocalPreview] = useState<ArtifactPreview | null>(null);
  const [previewBusy, setPreviewBusy] = useState<Record<string, boolean>>({});
  const [previewError, setPreviewError] = useState<Record<string, string | null>>({});
  const [collapsedPreviewId, setCollapsedPreviewId] = useState<string | null>(null);
  // Epoch counter: increments on identity change → stale promises ignored.
  const previewEpochRef = useRef(0);

  const previewCandidate = preview ?? localPreview;
  const activePreview =
    previewCandidate?.artifactId === collapsedPreviewId ? null : previewCandidate;

  // Clear preview when slot identity changes.
  useEffect(() => {
    setLocalPreview(null);
    setPreviewBusy({});
    setPreviewError({});
    setCollapsedPreviewId(null);
    previewEpochRef.current++;
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slot.workId, slot.definitionRev, slot.id, slot.revision]);

  const firePreview = useCallback(async (refId: string) => {
    if (!onPreview) return;
    if (previewCandidate?.artifactId === refId) {
      previewEpochRef.current++;
      setCollapsedPreviewId(current => current === refId ? null : refId);
      setPreviewBusy(prev => ({ ...prev, [refId]: false }));
      setPreviewError(prev => ({ ...prev, [refId]: null }));
      return;
    }

    const epoch = ++previewEpochRef.current;
    setCollapsedPreviewId(null);
    setPreviewBusy(prev => ({ ...prev, [refId]: true }));
    setPreviewError(prev => ({ ...prev, [refId]: null }));
    try {
      const ref = slot.artifactRefs?.find(r => r.id === refId);
      if (!ref) return;
      const p = await onPreview({
        workId: slot.workId,
        definitionRevision: slot.definitionRev,
        slotId: slot.id,
        slotRevision: slot.revision,
        artifactId: refId,
        requestId: `preview:${slot.workId}:${slot.definitionRev}:${slot.id}:${slot.revision}:${refId}`,
      });
      // Only apply if epoch still matches.
      if (previewEpochRef.current !== epoch) return;
      setLocalPreview(p);
    } catch (e: unknown) {
      if (previewEpochRef.current !== epoch) return;
      setPreviewError(prev => ({ ...prev, [refId]: e instanceof Error ? e.message : String(e) }));
    } finally {
      if (previewEpochRef.current !== epoch) return;
      setPreviewBusy(prev => ({ ...prev, [refId]: false }));
    }
  }, [onPreview, previewCandidate, slot]);

  const fireConversion = useCallback(async (refId: string) => {
    if (!onConvert) return;
    const epoch = ++previewEpochRef.current;
    const request: FileConversionIntent = {
      workId: slot.workId,
      definitionRevision: slot.definitionRev,
      slotId: slot.id,
      slotRevision: slot.revision,
      artifactId: refId,
      requestId: `convert:${slot.workId}:${slot.definitionRev}:${slot.id}:${slot.revision}:${refId}`,
      allowExternal: false,
      approvalToken: '',
    };
    setPreviewBusy(prev => ({ ...prev, [refId]: true }));
    setPreviewError(prev => ({ ...prev, [refId]: null }));
    try {
      let next = await onConvert(request);
      // Bounded polling of the same durable request. A queued result remains
      // visible after the bound; no new intent or unbounded retry is created.
      for (let attempt = 0;
        attempt < 20 && (next.conversionState === 'pending' || next.conversionState === 'running');
        attempt++) {
        await new Promise(resolve => globalThis.setTimeout(resolve, 100));
        if (previewEpochRef.current !== epoch) return;
        next = await onConvert(request);
      }
      if (previewEpochRef.current !== epoch) return;
      setLocalPreview(next);
    } catch (e: unknown) {
      if (previewEpochRef.current !== epoch) return;
      setPreviewError(prev => ({ ...prev, [refId]: e instanceof Error ? e.message : String(e) }));
    } finally {
      if (previewEpochRef.current !== epoch) return;
      setPreviewBusy(prev => ({ ...prev, [refId]: false }));
    }
  }, [onConvert, slot]);

  const markInFlight = useCallback((key: string): boolean => {
    if (inFlightRef.current[key]) return false;
    inFlightRef.current = { ...inFlightRef.current, [key]: true };
    setInFlightRender(inFlightRef.current);
    return true;
  }, []);

  const clearInFlight = useCallback((key: string) => {
    const { [key]: _, ...rest } = inFlightRef.current;
    inFlightRef.current = rest;
    setInFlightRender(rest);
  }, []);

  const setActionError = useCallback((key: string, msg: string) => {
    actionErrorsRef.current = { ...actionErrorsRef.current, [key]: msg };
    setActionErrorsRender(actionErrorsRef.current);
  }, []);

  const clearActionError = useCallback((key: string) => {
    const { [key]: _, ...rest } = actionErrorsRef.current;
    actionErrorsRef.current = rest;
    setActionErrorsRender(rest);
  }, []);

  // ── file action fire ──────────────────────────────────────────────────

  const fireFileAction = useCallback(
    async (refId: string, kind: FileActionKind) => {
      const actionKey = fileActionKey(refId, kind);
      if (!markInFlight(actionKey)) return;

      // Clear any previous error for this action before attempting.
      clearActionError(actionKey);

      const ref = slot.artifactRefs?.find((r) => r.id === refId);
      const path = ref?.relativePath;
      if (!path) {
        clearInFlight(actionKey);
        return;
      }

      let handler: ((intent: unknown) => void | Promise<void>) | undefined;
      let intent: unknown;
      switch (kind) {
        case 'open':
          handler = onOpen as ((intent: unknown) => void | Promise<void>) | undefined;
          intent = {
            workId: slot.workId,
            definitionRevision: slot.definitionRev,
            slotRevision: slot.revision,
            slotId: slot.id,
            artifactRefId: refId,
            path,
          };
          break;
        case 'download':
          handler = onDownload as ((intent: unknown) => void | Promise<void>) | undefined;
          intent = {
            workId: slot.workId,
            definitionRevision: slot.definitionRev,
            slotRevision: slot.revision,
            slotId: slot.id,
            artifactRefId: refId,
            path,
            name: ref?.name ?? refId,
          };
          break;
        case 'locate':
          handler = onLocate as ((intent: unknown) => void | Promise<void>) | undefined;
          intent = {
            workId: slot.workId,
            definitionRevision: slot.definitionRev,
            slotRevision: slot.revision,
            slotId: slot.id,
            artifactRefId: refId,
            path,
          };
          break;
      }

      try {
        if (!handler) throw new Error(`${kind} capability is unavailable`);
        await Promise.resolve(handler(intent));
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        setActionError(actionKey, msg);
      } finally {
        clearInFlight(actionKey);
      }
    },
    [slot, onOpen, onDownload, onLocate, markInFlight, clearInFlight, clearActionError, setActionError],
  );

  // ── slot retry fire ───────────────────────────────────────────────────

  const fireRetry = useCallback(async () => {
    const actionKey = retryActionKey(slot.id);
    if (!markInFlight(actionKey)) return;

    clearActionError(actionKey);

    try {
      if (!onRetry) throw new Error('retry capability is unavailable');
      await Promise.resolve(onRetry({
        workId: slot.workId,
        definitionRevision: slot.definitionRev,
        slotId: slot.id,
        revision: slot.revision,
      }));
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setActionError(actionKey, msg);
    } finally {
      clearInFlight(actionKey);
    }
  }, [slot, onRetry, markInFlight, clearInFlight, clearActionError, setActionError]);

  // ── render ────────────────────────────────────────────────────────────

  const hasRefs = slot.artifactRefs && slot.artifactRefs.length > 0;
  const showProgress = slot.state === 'generating' && slot.progress !== undefined;
  const showStaleBanner = slot.state === 'stale';
  const retryKey = retryActionKey(slot.id);
  const retryBusy = inFlightRender[retryKey] === true;
  const retryError = actionErrorsRender[retryKey];
  const singleRef = slot.artifactRefs?.length === 1 ? slot.artifactRefs[0] : undefined;
  const displayTitle = singleRef?.name ?? slot.title;

  const ariaLabel = `${slot.title} — ${STATE_LABELS[slot.state]}${slot.required ? '（必需）' : ''}`;

  return (
    <article
      className="wg2-rc-card"
      data-slot-state={slot.state}
      data-slot-id={slot.id}
      data-testid={`result-card-${slot.id}`}
      role="article"
      aria-label={ariaLabel}
      aria-live={slot.state === 'generating' ? 'polite' : undefined}
      tabIndex={0}
      key={slot.id}
    >
      {/* Artifact-led header mirrors the Work V2 design shelf. */}
      <div className="wg2-rc-header">
        <span
          className="wg2-rc-hero-icon"
          data-artifact-tone={artifactTone(slot.kind, singleRef?.type)}
          aria-hidden="true"
        >
          {fileIcon(slot.kind, singleRef?.type)}
        </span>
        <span className="wg2-rc-heading">
          <span className="wg2-rc-title" title={displayTitle}>
            {displayTitle}
          </span>
          <span className="wg2-rc-meta">
            {STATE_LABELS[slot.state]}
            {slot.summary && singleRef && (
              <span data-testid={`result-card-summary-${slot.id}`}> · {slot.summary}</span>
            )}
          </span>
        </span>
        <span
          className="wg2-rc-badge"
          data-badge={slot.state}
          data-testid={`result-card-badge-${slot.id}`}
          aria-label={STATE_LABELS[slot.state]}
        >
          {stateIcon(slot.state)}
          {slot.state !== 'ready' && <span>{STATE_LABELS[slot.state]}</span>}
        </span>
      </div>

      {/* Progress bar for generating */}
      {showProgress && (
        <div className="wg2-rc-progress" role="progressbar" aria-valuenow={Math.round(slot.progress! * 100)} aria-valuemin={0} aria-valuemax={100} aria-label="生成进度">
          <div className="wg2-rc-progress-bar">
            <div
              className="wg2-rc-progress-fill"
              style={{ width: `${Math.round(slot.progress! * 100)}%` }}
            />
          </div>
          <span className="wg2-rc-progress-pct">{Math.round(slot.progress! * 100)}%</span>
        </div>
      )}

      {/* Generating spinner when no progress number */}
      {slot.state === 'generating' && !showProgress && (
        <div className="wg2-rc-progress" role="status" aria-label="正在生成">
          <div className="wg2-rc-progress-bar">
            <div className="wg2-rc-progress-fill" style={{ width: '100%', animation: 'wg2-rc-indeterminate 1.5s ease-in-out infinite' }} />
          </div>
        </div>
      )}

      {/* Summary */}
      {slot.summary && !singleRef && (
        <p className="wg2-rc-summary" data-testid={`result-card-summary-${slot.id}`}>
          {slot.summary}
        </p>
      )}
      {/* Slot-level recovery. A partial slot remains actionable even when the
          projection has no ArtifactError payload. */}
      {(slot.state === 'failed' || slot.state === 'partial') && (slot.error || slot.state === 'partial') && (
        <div
          className="wg2-rc-error"
          role="alert"
          data-testid={`${slot.state === 'partial' ? 'result-card-partial-error' : 'result-card-error'}-${slot.id}`}
        >
          <AlertTriangle className="wg2-rc-error-icon" size={14} aria-hidden="true" />
          <span className="wg2-rc-error-msg">
            {slot.error?.message ?? '部分产物尚未完成，可重新生成缺失部分。'}
            {slot.error?.code && <span> ({slot.error.code})</span>}
          </span>
          {onRetry && (slot.error?.retryable === true || (slot.state === 'partial' && !slot.error)) && (
            <button
              type="button"
              className={`wg2-rc-retry-btn${retryBusy ? ' wg2-rc-retry-btn--busy' : ''}`}
              onClick={fireRetry}
              disabled={retryBusy}
              aria-busy={retryBusy ? 'true' : undefined}
              aria-label={`重试 ${slot.title}`}
              data-testid={`result-card-retry-${slot.id}`}
            >
              {retryBusy ? '重试中…' : '重试'}
            </button>
          )}
          {!onRetry && (slot.error?.retryable === true || (slot.state === 'partial' && !slot.error)) && (
            <span data-testid={`result-card-recovery-unavailable-${slot.id}`}>
              当前无法自动重试，请刷新工作状态后重试。
            </span>
          )}
          {slot.error?.retryable === false && (
            <span data-testid={`result-card-recovery-manual-${slot.id}`}>
              此失败无法安全自动重试，请检查错误后人工处理。
            </span>
          )}
        </div>
      )}

      {/* Retry action error (from handler reject) */}
      {retryError && (
        <div
          className="wg2-rc-action-error"
          role="alert"
          data-testid={`rc-action-error-${retryKey}`}
        >
          {retryError}
        </div>
      )}

      {/* Stale banner */}
      {showStaleBanner && (
        <div className="wg2-rc-stale-banner" role="status" data-testid={`result-card-stale-${slot.id}`}>
          <RefreshCw size={13} aria-hidden="true" />
          <span>上游输入已变化，结果可能过期</span>
          {onRetry && (
            <button
              type="button"
              className={`wg2-rc-retry-btn${retryBusy ? ' wg2-rc-retry-btn--busy' : ''}`}
              onClick={fireRetry}
              disabled={retryBusy}
              aria-busy={retryBusy ? 'true' : undefined}
              aria-label={`重新生成 ${slot.title}`}
              data-testid={`result-card-retry-${slot.id}`}
            >
              {retryBusy ? '重试中…' : '重新生成'}
            </button>
          )}
        </div>
      )}

      {/* File list */}
      {hasRefs && (
        <ul
          className="wg2-rc-files"
          data-layout={singleRef ? 'single' : 'multiple'}
          role="list"
          aria-label={`${slot.title} 的文件列表`}
        >
          {slot.artifactRefs.map((ref) => {
            const isPreviewOpen = activePreview?.artifactId === ref.id;
            return (
              <li
                key={ref.id}
                className="wg2-rc-file"
                data-testid={`result-card-file-${ref.id}`}
              >
              <span className="wg2-rc-file-icon" aria-hidden="true">
                {fileIcon(slot.kind, ref.type)}
              </span>
              <span
                className="wg2-rc-file-name"
                data-status={ref.status}
                title={ref.name ?? ref.id}
              >
                {ref.name ?? ref.id}
                {ref.status === 'stale' && ' (过期)'}
                {ref.status === 'missing' && ' (缺失)'}
                {ref.status === 'failed' && ' (失败)'}
              </span>
              <FileActions
                refInfo={ref}
                available={{
                  open: Boolean(onOpen),
                  download: Boolean(onDownload),
                  locate: Boolean(onLocate),
                }}
                inFlight={inFlightRender}
                actionErrors={actionErrorsRender}
                onFire={fireFileAction}
              />
              {/* Preview button for inline/filecard artifacts */}
              {onPreview && ref.status === 'available' && (
                <button
                  type="button"
                  className={`wg2-rc-file-btn${previewBusy[ref.id] ? ' wg2-rc-file-btn--busy' : ''}`}
                  onClick={() => firePreview(ref.id)}
                  disabled={previewBusy[ref.id] === true}
                  aria-busy={previewBusy[ref.id] ? 'true' : undefined}
                  aria-expanded={isPreviewOpen}
                  aria-label={`${isPreviewOpen ? '收起预览' : '预览'} ${ref.name ?? ref.id}`}
                  data-testid={`rc-file-preview-${ref.id}`}
                >
                  {previewBusy[ref.id]
                    ? <LoaderCircle size={12} className="wg2-rc-spin" />
                    : isPreviewOpen ? <EyeOff size={12} /> : <Eye size={12} />}
                  <span>{previewBusy[ref.id] ? '载入中' : isPreviewOpen ? '收起预览' : '预览'}</span>
                </button>
              )}
              {onConvert &&
                activePreview?.artifactId === ref.id &&
                activePreview.canConvert &&
                ref.status === 'available' && (
                  <button
                    type="button"
                    className={`wg2-rc-file-btn${previewBusy[ref.id] ? ' wg2-rc-file-btn--busy' : ''}`}
                    onClick={() => fireConversion(ref.id)}
                    disabled={previewBusy[ref.id] === true}
                    aria-busy={previewBusy[ref.id] ? 'true' : undefined}
                    aria-label={`本地转换预览 ${ref.name ?? ref.id}`}
                    data-testid={`rc-file-convert-${ref.id}`}
                  >
                    {previewBusy[ref.id]
                      ? <LoaderCircle size={12} className="wg2-rc-spin" />
                      : <RefreshCw size={12} />}
                    <span>{previewBusy[ref.id] ? '转换中' : '本地转换预览'}</span>
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {/* Preview error */}
      {Object.entries(previewError).filter(([, msg]) => msg).map(([refId, msg]) => (
        <div key={refId} className="wg2-rc-action-error" role="alert" data-testid={`rc-preview-error-${refId}`}>
          {msg}
        </div>
      ))}

      {/* Inline preview rendering */}
      {activePreview && activePreview.grade === 'inline' && (
        <div className="wg2-rc-inline-preview" data-testid="rc-inline-preview">
          {/* Image preview */}
          {activePreview.dataURL && (
            <img
              src={activePreview.dataURL}
              alt="预览"
              className="wg2-rc-preview-image"
              data-testid="rc-preview-image"
            />
          )}
          {/* Text/Markdown preview */}
          {activePreview.textContent && (
            <pre className="wg2-rc-preview-text" data-testid="rc-preview-text">
              {activePreview.textContent}
            </pre>
          )}
          {/* PDF preview */}
          {activePreview.pdfRaw && (
            <iframe
              src={activePreview.pdfRaw}
              className="wg2-rc-preview-pdf"
              title="PDF 预览"
              data-testid="rc-preview-pdf"
            />
          )}
        </div>
      )}

      {/* Filecard preview summary */}
      {activePreview && activePreview.grade === 'filecard' && activePreview.summary && (
        <div className="wg2-rc-filecard-preview" data-testid="rc-filecard-preview">
          <p className="wg2-rc-summary">{activePreview.summary}</p>
          {(activePreview.conversionState === 'pending' || activePreview.conversionState === 'running') && (
            <p role="status" data-testid="rc-conversion-pending">
              转换已排队，可稍后使用同一请求继续查看。
            </p>
          )}
        </div>
      )}

      {/* Reserved with no refs: show placeholder */}
      {!hasRefs && slot.state === 'reserved' && (
        <p className="wg2-rc-summary" data-testid={`result-card-placeholder-${slot.id}`}>
          文件尚未生成，生成后将在此显示。
        </p>
      )}

    </article>
  );
};
