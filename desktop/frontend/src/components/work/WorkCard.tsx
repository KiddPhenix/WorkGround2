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
import { useWorkStore, useWorkUIStore, type FaceScrollState, type WorkFace } from '../../work/store';
import type { BlockUpdateRequest } from '../../work/types';
import type { BlockActionHandler } from './blocks/types';
import { WorkCardBack, type WorkCardBackSlots } from './WorkCardBack';
import { WorkCardFront } from './WorkCardFront';
import { WorkFlipControl } from './WorkFlipControl';
import { WorkWorkspace } from './WorkWorkspace';

export interface WorkDeepLink {
  face: WorkFace;
  targetID?: string;
}

export interface WorkCardProps {
  workID: string;
  port: WorkControllerPort;
  deepLink?: WorkDeepLink;
  backSlots?: WorkCardBackSlots;
  cornerstoneEntry?: ReactNode;
  addonPanel?: ReactNode;
  workspaceActions?: ReactNode;
  onBlockAction?: BlockActionHandler;
  onBlockUpdate?: (request: BlockUpdateRequest) => void | Promise<void>;
}

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

export const WorkCard: React.FC<WorkCardProps> = ({
  workID,
  port,
  deepLink,
  backSlots,
  cornerstoneEntry,
  addonPanel,
  workspaceActions,
  onBlockAction,
  onBlockUpdate,
}) => {
  const view = useWorkStore((state) => state.works[workID]);
  const cardState = useWorkUIStore((state) => state.cardByWork[workID]);
  const ensureCard = useWorkUIStore((state) => state.ensureCard);
  const setScroll = useWorkUIStore((state) => state.setScroll);
  const setExpanded = useWorkUIStore((state) => state.setExpanded);
  const setDraft = useWorkUIStore((state) => state.setDraft);

  const adapter = useMemo(() => new WorkControllerAdapter(port), [port]);
  const [statuses, setStatuses] = useState<Record<string, WorkControllerStatus>>({});
  const [preferenceReady, setPreferenceReady] = useState(false);
  const [deepLinkState, setDeepLinkState] = useState<DeepLinkState>({ kind: 'idle' });
  const faceRefs = useRef<Partial<Record<WorkFace, HTMLDivElement>>>({});
  const restoredScroll = useRef<Partial<Record<WorkFace, string>>>({});

  const activeFace = cardState?.activeFace ?? 'front';
  const frontState = cardState?.faces.front;
  const backState = cardState?.faces.back;
  const status = statuses[workID];
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
    void adapter.recoverSnapshot(workID).catch(() => undefined);
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
  const deepTargetID = deepLink?.targetID;
  useEffect(() => {
    if (!preferenceReady || !deepFace) return;
    setDeepLinkState({ kind: deepTargetID ? 'resolving' : 'resolved' });
    void adapter.setActiveFace(workID, deepFace).catch(() => {
      setDeepLinkState({ kind: 'missing', reason: `无法切换到${FACE_NAMES[deepFace]}面，请重试。` });
    });
  }, [adapter, deepFace, deepTargetID, preferenceReady, workID]);

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
  const retrySnapshot = useCallback(() => {
    void adapter.recoverSnapshot(workID).catch(() => undefined);
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
          <p>Work ID: {workID}</p>
          <button type="button" onClick={retrySnapshot}>重试载入</button>
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
      cornerstoneEntry={cornerstoneEntry}
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
        {(status?.snapshotError || status?.eventError) && (
          <div className="wg2-work-error-banner" role="alert" data-testid="work-error-banner">
            {status.snapshotError && <p>快照错误：{status.snapshotError}</p>}
            {status.eventError && <p>事件错误：{status.eventError}</p>}
            <button type="button" onClick={retrySnapshot}>重试同步</button>
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
              onUpdate={onBlockUpdate}
              readonly={readonly}
              archived={archived}
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
            />
          </div>
        </div>
      </div>
    </WorkWorkspace>
  );
};
