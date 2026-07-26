import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type UIEvent,
} from 'react';

import { WorkControllerAdapter, type WorkControllerPort, type WorkControllerStatus } from '../../work/controller';
import { createWailsWorkControllerPort } from '../../work/wailsAdapter';
import { useWorkStore, useWorkUIStore, selectV2Definition, selectV2ActiveDefinition, selectArtifactSlots, type FaceScrollState, type WorkFace } from '../../work/store';
import type {
  BlockUpdateRequest,
  DeepLinkTarget,
  RetryHandler,
  RetryIntent,
  RunSelection,
  SessionRef,
  SessionSurfaceContext,
  WorkView,
} from '../../work/types';
import type {
  ApplyDefinitionInput,
  ApplyDefinitionResult,
  CreateCandidateRevisionInput,
  CreateCandidateRevisionResult,
  RetryArtifactSlotRequest,
  RetryWorkNodeRequest,
  RunImpact,
  SelectWorkInputFileRequest,
  SelectWorkInputFileResult,
} from '../../work/types_v2';
import type {
  FileDownloadIntent,
  FileLocateIntent,
  FileOpenIntent,
  SlotRetryIntent,
  FilePreviewIntent,
  FileConversionIntent,
} from '../../work/components/v2/ResultCard';
import type { TaskRetryIntent as V2TaskRetryIntent } from '../../work/components/v2/ExecutionRow';
import type { BlockActionHandler } from './blocks/types';
import { CornerstoneDrawer } from './CornerstoneDrawer';
import { WorkCardBack, type WorkCardBackSlots } from './WorkCardBack';
import { WorkCardFront } from './WorkCardFront';
import { WorkFlipControl } from './WorkFlipControl';
import { WorkWorkspace } from './WorkWorkspace';

export interface WorkDeepLink {
  face: WorkFace;
  targetID?: string;
  /** Structured deep-link target; takes precedence over flat targetID for run/attempt resolution. */
  target?: DeepLinkTarget;
}

export interface WorkCardProps {
  workID: string;
  /** Tests/embedders may inject a complete port. Desktop production passes the
   * owning tabID and the WorkCard assembles the real Wails port itself. */
  port?: WorkControllerPort;
  tabID?: string;
  /** Explicit session ID for discussion context. When provided, it is preferred
   * over the implicit derivation from historical attempts (which yields an
   * empty string for Works that have not yet produced any Attempt). */
  sessionId?: string;
  deepLink?: WorkDeepLink;
  backSlots?: WorkCardBackSlots;
  cornerstoneEntry?: ReactNode;
  addonPanel?: ReactNode;
  workspaceActions?: ReactNode;
  onBlockAction?: BlockActionHandler;
  onBlockUpdate?: (request: BlockUpdateRequest) => void | Promise<void>;
  onRetry?: RetryHandler;
  resolveSessionSurface?: (sessionRef: SessionRef, context: SessionSurfaceContext) => ReactNode;
  onArtifactOpen?: (intent: FileOpenIntent) => void | Promise<void>;
  onArtifactDownload?: (intent: FileDownloadIntent) => void | Promise<void>;
  onArtifactLocate?: (intent: FileLocateIntent) => void | Promise<void>;
  onArtifactRetry?: (intent: SlotRetryIntent) => void | Promise<void>;
  onArtifactPreview?: (intent: FilePreviewIntent) => Promise<import('../../work/types_v2').ArtifactPreview>;
  onArtifactConvert?: (intent: FileConversionIntent) => Promise<import('../../work/types_v2').ArtifactPreview>;
}

const unavailablePort: WorkControllerPort = {
  subscribe: () => ({ ready: Promise.reject(new Error('Work Controller 尚未连接。')), unsubscribe: () => undefined }),
  fetchSnapshot: async () => { throw new Error('Work Controller 尚未连接。'); },
  fetchRecoverySnapshot: async () => { throw new Error('Work Controller 尚未连接。'); },
  readUIPreference: async () => null,
  writeUIPreference: async () => { throw new Error('Work UI 偏好存储尚未连接。'); },
};

type DeepLinkState =
  | { kind: 'idle' | 'resolving' | 'resolved' }
  | { kind: 'missing'; reason: string };

const FACE_NAMES: Record<WorkFace, string> = { front: '工作流', back: '会话' };

function findTarget(root: HTMLElement, targetID: string): HTMLElement | null {
  const candidates = root.querySelectorAll<HTMLElement>('[data-work-target-id], [data-block-id]');
  for (const candidate of candidates) {
    if (candidate.dataset.workTargetId === targetID || candidate.dataset.blockId === targetID) return candidate;
  }
  return null;
}

function focusTarget(target: HTMLElement): void {
  if (!target.hasAttribute('tabindex')) target.tabIndex = -1;
  target.focus({ preventScroll: true });
  target.scrollIntoView?.({ block: 'center', inline: 'nearest' });
}

function structuredTargetID(target: DeepLinkTarget | undefined): string | undefined {
  if (!target) return undefined;
  if (target.targetID) return target.targetID;
  if (!target.runId) return undefined;
  if (!target.stageId) return `run:${target.runId}`;
  if (!target.taskId) return `stage:${target.runId}:${target.stageId}`;
  if (!target.attemptId && target.attemptIndex === undefined) return `task:${target.runId}:${target.stageId}:${target.taskId}`;
  return `attempt:${target.runId}:${target.stageId}:${target.taskId}:${target.attemptId ?? target.attemptIndex}`;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function latestWorkSessionID(view: WorkView): string {
  const linked = (view.work as unknown as { sessionRefs?: SessionRef[] }).sessionRefs?.[0]?.sessionPath;
  if (linked) return linked;
  for (let runIndex = view.work.runs.length - 1; runIndex >= 0; runIndex--) {
    const stages = view.work.runs[runIndex]?.stages ?? [];
    for (let stageIndex = stages.length - 1; stageIndex >= 0; stageIndex--) {
      const tasks = stages[stageIndex]?.tasks ?? [];
      for (let taskIndex = tasks.length - 1; taskIndex >= 0; taskIndex--) {
        const attempts = tasks[taskIndex]?.attempts ?? [];
        for (let attemptIndex = attempts.length - 1; attemptIndex >= 0; attemptIndex--) {
          const sessionPath = attempts[attemptIndex]?.sessionRef?.sessionPath;
          if (sessionPath) return sessionPath;
        }
      }
    }
  }
  return '';
}

export const WorkCard: React.FC<WorkCardProps> = ({
  workID,
  port,
  tabID,
  sessionId,
  deepLink,
  backSlots,
  cornerstoneEntry,
  addonPanel,
  workspaceActions,
  onBlockAction,
  onBlockUpdate,
  onRetry,
  resolveSessionSurface,
  onArtifactOpen,
  onArtifactDownload,
  onArtifactLocate,
  onArtifactRetry,
  onArtifactConvert,
  onArtifactPreview,
}) => {
  const view = useWorkStore((state) => state.works[workID]);
  const artifactSlots = useWorkStore((state) => selectArtifactSlots(state.artifactSlots, workID));
  const v2Definition = useWorkStore((state) => selectV2Definition(state.v2Definitions, workID));
  const v2ActiveDefinition = useWorkStore((state) => selectV2ActiveDefinition(state.v2ActiveDefinitions, state.v2Definitions, workID));
  const cardState = useWorkUIStore((state) => state.cardByWork[workID]);
  const selection = useWorkUIStore((state) => state.selectionByWork[workID]);
  const retryByTarget = useWorkUIStore((state) => state.retryByTarget);
  const ensureCard = useWorkUIStore((state) => state.ensureCard);
  const setScroll = useWorkUIStore((state) => state.setScroll);
  const setExpanded = useWorkUIStore((state) => state.setExpanded);
  const setDraft = useWorkUIStore((state) => state.setDraft);
  const setSelection = useWorkUIStore((state) => state.setSelection);
  const beginRetry = useWorkUIStore((state) => state.beginRetry);
  const failRetry = useWorkUIStore((state) => state.failRetry);
  const clearRetry = useWorkUIStore((state) => state.clearRetry);

  const resolvedPort = useMemo(
    () => port ?? (tabID ? createWailsWorkControllerPort(tabID) : undefined) ?? unavailablePort,
    [port, tabID],
  );
  const adapter = useMemo(() => new WorkControllerAdapter(resolvedPort), [resolvedPort]);
  const [statuses, setStatuses] = useState<Record<string, WorkControllerStatus>>({});
  const [definitionImpact, setDefinitionImpact] = useState<RunImpact | undefined>();
  const [preferenceReady, setPreferenceReady] = useState(false);
  const [hasStoredPreference, setHasStoredPreference] = useState<boolean | null>(null);
  const [deepLinkState, setDeepLinkState] = useState<DeepLinkState>({ kind: 'idle' });
  const faceRefs = useRef<Partial<Record<WorkFace, HTMLDivElement>>>({});
  const restoredScroll = useRef<Partial<Record<WorkFace, string>>>({});
  const draftIntentRef = useRef<{ signature: string; requestId: string } | null>(null);
  const v2RetryIntentsRef = useRef(new Map<string, RetryWorkNodeRequest>());
  const artifactRetryIntentsRef = useRef(new Map<string, RetryArtifactSlotRequest>());
  // Auto-flip dedup: only flip once per definition revision.
  const autoFlipRef = useRef<string | null>(null);
  const defaultFaceAppliedRef = useRef<string | null>(null);

  const activeFace = cardState?.activeFace ?? 'front';
  const frontState = cardState?.faces.front;
  const backState = cardState?.faces.back;
  const status = statuses[workID];
  const streamError = status?.stream.kind === 'offline' ? status.stream.message : null;
  const readonly = !view || view.work.archiveState !== 'active';
  const archived = view?.work.archiveState === 'archived';
  const faceIDs = useMemo<Record<WorkFace, string>>(() => ({
    front: `work-${workID}-front`,
    back: `work-${workID}-back`,
  }), [workID]);

  useEffect(() => {
    setStatuses(adapter.getStatuses());
    return adapter.subscribeStatus(() => setStatuses(adapter.getStatuses()));
  }, [adapter]);
  useEffect(() => {
    setDefinitionImpact(undefined);
  }, [workID, v2Definition?.revision]);
  useEffect(() => () => adapter.dispose(), [adapter]);

  useEffect(() => {
    let current = true;
    setPreferenceReady(false);
    setHasStoredPreference(null);
    setDeepLinkState({ kind: 'idle' });
    restoredScroll.current = {};
    ensureCard(workID);
    adapter.subscribe(workID);
    void adapter.restoreUIPreference(workID)
      .then((preference) => {
        if (current) setHasStoredPreference(preference !== null);
      })
      .catch(() => undefined)
      .finally(() => {
        if (current) setPreferenceReady(true);
      });
    return () => {
      current = false;
      adapter.unsubscribe(workID);
    };
  }, [adapter, ensureCard, workID]);

  useEffect(() => {
    if (!preferenceReady || hasStoredPreference === null || !view) return;
    if (view.work.schemaVersion < 2 && !v2Definition) return;
    if (defaultFaceAppliedRef.current === workID) return;
    defaultFaceAppliedRef.current = workID;
    if (v2Definition?.status !== 'active') {
      useWorkUIStore.getState().setActiveFace(workID, 'back');
    } else if (!hasStoredPreference) {
      useWorkUIStore.getState().setActiveFace(workID, 'front');
    }
  }, [hasStoredPreference, preferenceReady, v2Definition?.status, view, workID]);

  const deepFace = deepLink?.face;
  const deepTarget = deepLink?.target;
  const deepTargetID = deepLink?.targetID ?? structuredTargetID(deepTarget);
  useEffect(() => {
    if (!preferenceReady || !deepFace) return;
    setDeepLinkState({ kind: deepTargetID || deepTarget ? 'resolving' : 'resolved' });
    void adapter.setActiveFace(workID, deepFace).catch(() => {
      setDeepLinkState({ kind: 'missing', reason: `无法切换到${FACE_NAMES[deepFace]}面，请重试。` });
    });
    // Resolve structured deep-link target into selection.
    if (deepTarget && deepTarget.runId) {
      const sel: RunSelection = {
        runId: deepTarget.runId,
        stageId: deepTarget.stageId,
        taskId: deepTarget.taskId,
        attemptId: deepTarget.attemptId,
        attemptIndex: deepTarget.attemptIndex,
      };
      setSelection(workID, sel);
    }
  }, [adapter, deepFace, deepTargetID, deepTarget, preferenceReady, setSelection, workID]);

  useLayoutEffect(() => {
    if (!preferenceReady || deepLinkState.kind !== 'resolving' || !deepFace || !deepTargetID || activeFace !== deepFace) return;
    const root = faceRefs.current[deepFace];
    if (!root) return;
    let resolved = false;
    const resolveTarget = () => {
      const target = findTarget(root, deepTargetID);
      if (!target) return false;
      resolved = true;
      focusTarget(target);
      setDeepLinkState({ kind: 'resolved' });
      return true;
    };
    if (resolveTarget()) return;
    const missing = () => {
      if (resolved) return;
      setDeepLinkState({
        kind: 'missing',
        reason: `${FACE_NAMES[deepFace]}面目标“${deepTargetID}”不存在或已被移除。`,
      });
    };
    // Front targets are part of WorkView and are complete at this commit. Back
    // session history may hydrate late, so observe it briefly before declaring
    // the target stale.
    if (deepFace === 'front') {
      missing();
      return;
    }
    const observer = new MutationObserver(() => {
      if (resolveTarget()) observer.disconnect();
    });
    observer.observe(root, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-work-target-id'] });
    const timer = window.setTimeout(() => {
      observer.disconnect();
      missing();
    }, 1000);
    return () => {
      resolved = true;
      observer.disconnect();
      window.clearTimeout(timer);
    };
  }, [activeFace, deepFace, deepLinkState.kind, deepTargetID, preferenceReady, view?.revision]);

  useLayoutEffect(() => {
    const restore = (face: WorkFace, scroll: FaceScrollState | undefined) => {
      const node = faceRefs.current[face];
      const key = `${workID}:${face}`;
      if (!node || !scroll || restoredScroll.current[face] === key) return;
      node.scrollTop = scroll.scrollTop;
      node.scrollLeft = scroll.scrollLeft;
      restoredScroll.current[face] = key;
    };
    restore('front', frontState?.scroll);
    restore('back', backState?.scroll);
  }, [backState?.scroll, frontState?.scroll, workID]);

  const bindFront = useCallback((node: HTMLDivElement | null) => {
    if (node) faceRefs.current.front = node;
    else delete faceRefs.current.front;
  }, []);
  const bindBack = useCallback((node: HTMLDivElement | null) => {
    if (node) faceRefs.current.back = node;
    else delete faceRefs.current.back;
  }, []);
  const handleScroll = useCallback((face: WorkFace, event: UIEvent<HTMLDivElement>) => {
    setScroll(workID, face, {
      scrollTop: event.currentTarget.scrollTop,
      scrollLeft: event.currentTarget.scrollLeft,
    });
  }, [setScroll, workID]);
  const handleFlip = useCallback((next: WorkFace) => {
    void adapter.setActiveFace(workID, next).catch(() => undefined);
  }, [adapter, workID]);
  const handleToggleExpand = useCallback((targetID: string) => {
    const current = useWorkUIStore.getState().cardByWork[workID]?.faces.front.expanded[targetID] ?? false;
    setExpanded(workID, 'front', targetID, !current);
  }, [setExpanded, workID]);
  const handleExecutionExpand = useCallback((taskID: string, next: boolean) => {
    const key = `v2-task:${taskID}`;
    const expanded = useWorkUIStore.getState().cardByWork[workID]?.faces.front.expanded ?? {};
    for (const [targetID, open] of Object.entries(expanded)) {
      if (open && targetID.startsWith('v2-task:') && targetID !== key) {
        setExpanded(workID, 'front', targetID, false);
      }
    }
    setExpanded(workID, 'front', key, next);
  }, [setExpanded, workID]);
  const handleDraftChange = useCallback((draft: string) => {
    setDraft(workID, 'back', draft);
  }, [setDraft, workID]);
  const savePrompt = useCallback(async (prompt: string) => {
    const current = useWorkStore.getState().works[workID];
    if (!current) throw new Error('Work 投影尚未载入。');
    const signature = prompt;
    if (draftIntentRef.current?.signature !== signature) {
      draftIntentRef.current = {
        signature,
        requestId: `work-draft-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`,
      };
    }
    await adapter.updateDraft({
      workId: workID,
      prompt,
      expectedRevision: current.revision,
      requestId: draftIntentRef.current.requestId,
    });
    draftIntentRef.current = null;
  }, [adapter, workID]);
  const handleApplyDefinition = useCallback(async (input: ApplyDefinitionInput): Promise<ApplyDefinitionResult> => {
    const result = await adapter.applyDefinition(input);
    if (result.impact) setDefinitionImpact(result.impact);
    const definitionRev = result.intent?.definitionRev ?? (result.committed ? input.revision : undefined);
    const autoFlipKey = definitionRev ? `${workID}:${definitionRev}` : null;
    if (autoFlipKey && autoFlipRef.current !== autoFlipKey) {
      autoFlipRef.current = autoFlipKey;
      // A duplicate can be the first response observed after the original
      // commit acknowledgement was lost. The revision key still guarantees
      // that this component performs the UI side effect at most once.
      // The definition commit is authoritative. Face preference persistence is
      // a separate UI concern whose failure is exposed by adapter status and
      // must not turn a committed apply into a retryable domain failure.
      void adapter.setActiveFace(workID, 'front').catch(() => undefined);
    }
    return result;
  }, [adapter, workID]);
  const handleCreateCandidate = useCallback(async (
    input: CreateCandidateRevisionInput,
  ): Promise<CreateCandidateRevisionResult> => {
    return adapter.createCandidateRevision(input);
  }, [adapter]);
  const handleV2TaskRetry = useCallback(async (intent: V2TaskRetryIntent): Promise<void> => {
    const current = useWorkStore.getState().works[workID];
    if (!current) throw new Error('Work 投影尚未载入。');
    const key = `${intent.runId}\u0000${intent.taskId}`;
    let request = v2RetryIntentsRef.current.get(key);
    if (!request || request.expectedRevision !== current.revision) {
      const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      request = {
        workId: workID,
        runId: intent.runId,
        taskId: intent.taskId,
        expectedRevision: current.revision,
        requestId: `work-node-retry-${suffix}`,
      };
      v2RetryIntentsRef.current.set(key, request);
    }
    try {
      await adapter.retryWorkNode(request);
      v2RetryIntentsRef.current.delete(key);
    } catch (error) {
      // A request conflict is definitive for this request ID. Transport
      // failures keep the exact request for idempotent replay; revision
      // conflicts refresh the snapshot in the adapter, so the next click sees
      // a different expectedRevision and derives a fresh request automatically.
      if ((error as { code?: string }).code === 'request_conflict') {
        v2RetryIntentsRef.current.delete(key);
      }
      throw error;
    }
  }, [adapter, workID]);
  const handleArtifactRetry = useCallback(async (intent: SlotRetryIntent): Promise<void> => {
    const current = useWorkStore.getState().works[workID];
    if (!current) throw new Error('Work 投影尚未载入。');
    if (intent.definitionRevision !== v2ActiveDefinition?.revision) {
      throw new Error('成果所属 Definition 已过期，请刷新后重试。');
    }
    const identity = `${intent.definitionRevision}\u0000${intent.slotId}`;
    let request = artifactRetryIntentsRef.current.get(identity);
    if (!request || request.expectedRevision !== current.revision) {
      const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      request = {
        workId: workID,
        slotId: intent.slotId,
        definitionRevision: intent.definitionRevision,
        expectedRevision: current.revision,
        requestId: `work-artifact-retry-${suffix}`,
      };
      artifactRetryIntentsRef.current.set(identity, request);
    }
    try {
      await adapter.retryArtifactSlot(request);
      artifactRetryIntentsRef.current.delete(identity);
    } catch (error) {
      if ((error as { code?: string }).code === 'request_conflict') {
        artifactRetryIntentsRef.current.delete(identity);
      }
      throw error;
    }
  }, [adapter, v2ActiveDefinition?.revision, workID]);
  const handleSelectWorkInputFile = useCallback(async (
    request: SelectWorkInputFileRequest,
  ): Promise<SelectWorkInputFileResult> => adapter.selectWorkInputFile(request), [adapter]);
  const updateBlock = useCallback(async (request: BlockUpdateRequest) => {
    if (onBlockUpdate) {
      await onBlockUpdate(request);
      return;
    }
    const current = useWorkStore.getState().works[workID];
    const block = current?.work.blocks.find((item) => item.id === request.blockId);
    if (!current || !block) throw new Error('Block 投影尚未载入。');
    await adapter.upsertBlock({
      workId: workID,
      blockId: block.id,
      kind: block.kind,
      schemaVersion: block.schemaVersion,
      revision: block.revision + 1,
      title: block.title,
      status: block.status,
      data: request.data,
      actions: block.actions,
      source: block.source,
      freshness: block.freshness,
      fallback: block.fallback,
      expectedRevision: current.revision,
      requestId: request.requestId,
    });
  }, [adapter, onBlockUpdate, workID]);
  const handleRunSelect = useCallback((sel: RunSelection) => {
    setSelection(workID, sel);
  }, [setSelection, workID]);
  const handleRetry = useCallback(async (intent: RetryIntent) => {
    const existing = useWorkUIStore.getState().retryByTarget;
    const pending = Object.values(existing).some((retry) =>
      retry.state === 'pending' &&
      retry.intent.workId === intent.workId && retry.intent.runId === intent.runId &&
      retry.intent.stageId === intent.stageId && retry.intent.taskId === intent.taskId,
    );
    if (pending) return;
    beginRetry(intent);
    try {
      if (onRetry) await onRetry(intent);
      else await adapter.retryTask(intent);
      await adapter.recoverSnapshot(workID);
    } catch (error) {
      failRetry(intent, errorText(error));
    }
  }, [adapter, beginRetry, failRetry, onRetry, workID]);

  useEffect(() => {
    if (!view) return;
    for (const retry of Object.values(retryByTarget)) {
      if (retry.state !== 'pending' || retry.intent.workId !== workID) continue;
      const run = view.work.runs.find((item) => item.id === retry.intent.runId);
      const stage = run?.stages.find((item) => (item.id || item.name) === retry.intent.stageId);
      const task = stage?.tasks.find((item) => (item.id || item.name) === retry.intent.taskId);
      if (task?.attempts.some((attempt) => attempt.index > retry.intent.attemptIndex)) clearRetry(retry.intent);
    }
  }, [clearRetry, retryByTarget, view, workID]);
  const retrySync = useCallback(() => {
    adapter.retrySubscription(workID);
  }, [adapter, workID]);
  const retryPreference = useCallback(() => {
    void adapter.setActiveFace(workID, activeFace).catch(() => undefined);
  }, [activeFace, adapter, workID]);

  if (!view) {
    return (
      <div className="wg2-work-card wg2-work-card-unknown" data-testid="work-card-unknown" data-work-id={workID}>
        <div className="wg2-work-unknown-notice" role="alert">
          <h3>Work 不可用</h3>
          {status?.snapshotError && <p className="wg2-work-error">{status.snapshotError}</p>}
          {streamError && <p className="wg2-work-error">事件订阅错误：{streamError}</p>}
          <p>Work ID: {workID}</p>
          <button type="button" onClick={retrySync}>{streamError ? '重试订阅' : '重试载入'}</button>
        </div>
      </div>
    );
  }

  const workspaceStatus = (
    <>
      {status?.fetching && <span className="wg2-work-status-fetching">同步中…</span>}
      {status?.preferenceError && (
        <span className="wg2-work-status-error" role="alert" data-testid="work-pref-error">
          偏好保存失败：{status.preferenceError}
          <button type="button" onClick={retryPreference}>重试</button>
        </span>
      )}
    </>
  );

  return (
    <WorkWorkspace
      name={view.work.name}
      state={view.work.state}
      archiveState={view.work.archiveState}
      status={workspaceStatus}
      actions={(
        <>
          {workspaceActions}
          <WorkFlipControl
            activeFace={activeFace}
            onFlip={handleFlip}
            faceIDs={faceIDs}
            disabled={!preferenceReady}
          />
        </>
      )}
      cornerstoneEntry={cornerstoneEntry ?? (
        <CornerstoneDrawer
          workId={workID}
          view={view}
          port={resolvedPort}
          readonly={readonly}
          onApplyMutationResult={(result) => adapter.applyMutationResult(workID, result)}
        />
      )}
      cornerstoneCount={view.work.cornerstones.length}
      addonPanel={addonPanel}
    >
      <div
        className="wg2-work-card-inner"
        data-testid="work-card"
        data-work-id={workID}
        data-active-face={activeFace}
        data-readonly={readonly ? 'true' : 'false'}
        data-archived={archived ? 'true' : 'false'}
      >
        {deepLinkState.kind === 'missing' && (
          <div className="wg2-work-deeplink-missing" role="alert" data-testid="work-deeplink-missing">
            <p>{deepLinkState.reason}</p>
          </div>
        )}
        {(status?.snapshotError || status?.eventError || streamError) && (
          <div className="wg2-work-error-banner" role="alert" data-testid="work-error-banner">
            {status.snapshotError && <p>快照错误：{status.snapshotError}</p>}
            {status.eventError && <p>事件错误：{status.eventError}</p>}
            {streamError && <p>事件订阅错误：{streamError}</p>}
            <button type="button" onClick={retrySync}>重试同步</button>
          </div>
        )}
        {cardState?.persistenceError && (
          <div className="wg2-work-error-banner" role="alert" data-testid="work-local-draft-error">
            <p>{cardState.persistenceError}</p>
          </div>
        )}
        <div
          className={`wg2-work-card-body${archived ? ' wg2-work-archived' : ''}${readonly && !archived ? ' wg2-work-readonly' : ''}`}
          data-testid="work-card-body"
        >
          <div
            ref={bindFront}
            id={faceIDs.front}
            className={`wg2-work-face wg2-work-face-front${activeFace === 'front' ? ' wg2-work-face-active' : ''}`}
            data-face="front"
            data-testid="work-face-front"
            aria-hidden={activeFace !== 'front'}
            inert={activeFace !== 'front' || undefined}
            onScroll={(event) => handleScroll('front', event)}
          >
            <WorkCardFront
              view={view}
              expanded={frontState?.expanded ?? {}}
              onToggleExpand={handleToggleExpand}
              onExecutionExpand={handleExecutionExpand}
              onAction={onBlockAction}
              onUpdate={updateBlock}
              readonly={readonly}
              archived={archived}
              runSelection={selection}
              onRunSelect={handleRunSelect}
              onRetry={handleRetry}
              retryByTarget={retryByTarget}
              onRun={adapter.runWork}
              onResumeRun={adapter.resumeRun}
              onRecoverProjection={async () => { await adapter.recoverSnapshot(workID); }}
              artifactSlots={artifactSlots}
              v2Definition={v2ActiveDefinition}
              onV2TaskRetry={handleV2TaskRetry}
              onArtifactOpen={onArtifactOpen}
              onArtifactDownload={onArtifactDownload}
              onArtifactLocate={onArtifactLocate}
              onArtifactRetry={onArtifactRetry ?? (
                resolvedPort.retryArtifactSlot ? handleArtifactRetry : undefined
              )}
              onArtifactPreview={onArtifactPreview ?? (
                resolvedPort.previewArtifact ? (intent) => adapter.previewArtifact(intent) : undefined
              )}
              onArtifactConvert={onArtifactConvert ?? (
                resolvedPort.requestArtifactConversion
                  ? (intent) => adapter.requestArtifactConversion(intent)
                  : undefined
              )}
              onAdjustStructure={() => handleFlip('back')}
              runId={view.work.runs[view.work.runs.length - 1]?.id}
              sessionId={sessionId ?? latestWorkSessionID(view)}
              onSubmitWorkInput={
                resolvedPort.submitWorkInput
                  ? (req) => adapter.submitWorkInput(req)
                  : undefined
              }
              onSetCornerstone={
                resolvedPort.setInputCornerstone
                  ? (req) => adapter.setInputCornerstone(req)
                  : undefined
              }
              onUnsetCornerstone={
                resolvedPort.setInputCornerstone
                  ? (req) => adapter.setInputCornerstone({ ...req, pin: false })
                  : undefined
              }
              onRefreshAuthoritative={async (ctx) => {
                await adapter.recoverSnapshot(ctx.workId);
              }}
              onSelectWorkInputFile={
                resolvedPort.selectWorkInputFile ? handleSelectWorkInputFile : undefined
              }
              onPreviewPatch={
                resolvedPort.previewWorkPatch
                  ? (intent) => adapter.previewWorkPatch(intent)
                  : undefined
              }
              onApplyPatch={
                resolvedPort.applyWorkPatch
                  ? (intent) => adapter.applyWorkPatch(intent)
                  : undefined
              }
            />
          </div>
          <div
            ref={bindBack}
            id={faceIDs.back}
            className={`wg2-work-face wg2-work-face-back${activeFace === 'back' ? ' wg2-work-face-active' : ''}`}
            data-face="back"
            data-testid="work-face-back"
            aria-hidden={activeFace !== 'back'}
            inert={activeFace !== 'back' || undefined}
            onScroll={(event) => handleScroll('back', event)}
          >
            <WorkCardBack
              view={view}
              draft={backState?.draft ?? ''}
              onDraftChange={handleDraftChange}
              readonly={readonly}
              archived={archived}
              slots={backSlots}
              selection={selection}
              resolveSessionSurface={resolveSessionSurface}
              onSavePrompt={savePrompt}
              onApplyDefinition={handleApplyDefinition}
              onCreateCandidate={
                resolvedPort.createCandidateRevision ? handleCreateCandidate : undefined
              }
              v2Definition={v2Definition}
              v2ActiveDefinition={v2ActiveDefinition}
              applyImpact={definitionImpact}
            />
          </div>
        </div>
      </div>
    </WorkWorkspace>
  );
};
