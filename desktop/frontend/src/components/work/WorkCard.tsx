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
import { useWorkStore, useWorkUIStore, type FaceScrollState, type WorkFace } from '../../work/store';
import type {
  BlockUpdateRequest,
  DeepLinkTarget,
  RetryHandler,
  RetryIntent,
  RunSelection,
  SessionRef,
  SessionSurfaceContext,
} from '../../work/types';
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
  deepLink?: WorkDeepLink;
  backSlots?: WorkCardBackSlots;
  cornerstoneEntry?: ReactNode;
  addonPanel?: ReactNode;
  workspaceActions?: ReactNode;
  onBlockAction?: BlockActionHandler;
  onBlockUpdate?: (request: BlockUpdateRequest) => void | Promise<void>;
  onRetry?: RetryHandler;
  resolveSessionSurface?: (sessionRef: SessionRef, context: SessionSurfaceContext) => ReactNode;
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

export const WorkCard: React.FC<WorkCardProps> = ({
  workID,
  port,
  tabID,
  deepLink,
  backSlots,
  cornerstoneEntry,
  addonPanel,
  workspaceActions,
  onBlockAction,
  onBlockUpdate,
  onRetry,
  resolveSessionSurface,
}) => {
  const view = useWorkStore((state) => state.works[workID]);
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
  const [preferenceReady, setPreferenceReady] = useState(false);
  const [deepLinkState, setDeepLinkState] = useState<DeepLinkState>({ kind: 'idle' });
  const faceRefs = useRef<Partial<Record<WorkFace, HTMLDivElement>>>({});
  const restoredScroll = useRef<Partial<Record<WorkFace, string>>>({});
  const draftIntentRef = useRef<{ signature: string; requestId: string } | null>(null);

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
  useEffect(() => () => adapter.dispose(), [adapter]);

  useEffect(() => {
    let current = true;
    setPreferenceReady(false);
    setDeepLinkState({ kind: 'idle' });
    restoredScroll.current = {};
    ensureCard(workID);
    adapter.subscribe(workID);
    void adapter.restoreUIPreference(workID)
      .catch(() => undefined)
      .finally(() => {
        if (current) setPreferenceReady(true);
      });
    return () => {
      current = false;
      adapter.unsubscribe(workID);
    };
  }, [adapter, ensureCard, workID]);

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
  const handleDraftChange = useCallback((draft: string) => {
    setDraft(workID, 'back', draft);
  }, [setDraft, workID]);
  const saveDraft = useCallback(async (input: { name: string; prompt: string; inputs: Record<string, unknown> }) => {
    const current = useWorkStore.getState().works[workID];
    if (!current) throw new Error('Work 投影尚未载入。');
    const signature = JSON.stringify(input);
    if (draftIntentRef.current?.signature !== signature) {
      draftIntentRef.current = {
        signature,
        requestId: `work-draft-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`,
      };
    }
    await adapter.updateDraft({
      workId: workID,
      ...input,
      expectedRevision: current.revision,
      requestId: draftIntentRef.current.requestId,
    });
    draftIntentRef.current = null;
  }, [adapter, workID]);
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
              onSaveDraft={saveDraft}
            />
          </div>
        </div>
      </div>
    </WorkWorkspace>
  );
};
