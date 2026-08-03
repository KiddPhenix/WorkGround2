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

import { useI18n } from '../../lib/i18n';
import { WorkControllerAdapter, type WorkControllerPort, type WorkControllerStatus } from '../../work/controller';
import { createWailsWorkControllerPort } from '../../work/wailsAdapter';
import { useWorkStore, useWorkUIStore, selectV2Definition, selectV2ActiveDefinition, selectV2Tasks, selectV2Inputs, selectArtifactSlots, resolveSelection, type FaceScrollState, type WorkFace } from '../../work/store';
import type {
  BlockUpdateRequest,
  DeepLinkTarget,
  RetryHandler,
  RetryIntent,
  RunSelection,
  SessionRef,
  SessionSurfaceContext,
  Work,
  CreateReusableWorkSessionResult,
  WorkView,
} from '../../work/types';
import type {
  ApplyDefinitionInput,
  ApplyDefinitionResult,
  CreateCandidateRevisionInput,
  CreateCandidateRevisionResult,
  DefinitionPlanProgress,
  RetryArtifactSlotRequest,
  RetryWorkNodeRequest,
  RunImpact,
  SelectWorkInputFileRequest,
  SelectWorkInputFileResult,
  SelectWorkInformationFileRequest,
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
import type { ComposerSubmitKey } from '../../lib/composerKeyboard';
import type { Item, LiveStream } from '../../lib/useController';
import { CornerstoneDrawer } from './CornerstoneDrawer';
import { WorkCardBack, type WorkCardBackSlots } from './WorkCardBack';
import { WorkCardFront } from './WorkCardFront';
import { WorkFlipControl } from './WorkFlipControl';
import { RunProgressPopover } from './RunProgressPopover';
import { WorkRunEntry } from './WorkRunEntry';
import { WorkWorkspace } from './WorkWorkspace';
import { WorkRestartDialog } from './WorkRestartDialog';
import { digestIntent } from './blocks/intentDigest';

export interface WorkDeepLink {
  face: WorkFace;
  targetID?: string;
  /** Structured deep-link target; takes precedence over flat targetID for run/attempt resolution. */
  target?: DeepLinkTarget;
}

export interface WorkCardProps {
  workID: string;
  /** When false the card mounts immediately (showing any cached store data) but
   * defers subscription and snapshot until ready becomes true. Default true. */
  ready?: boolean;
  startIntent?: {
    id: string;
    prompt: string;
  };
  onStartIntentConsumed?: (id: string) => void;
  onStartIntentNeedsAttention?: (id: string) => void;
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
  onOpenSession?: (sessionRef: SessionRef, context: SessionSurfaceContext) => void | Promise<void>;
  onArtifactOpen?: (intent: FileOpenIntent) => void | Promise<void>;
  onArtifactDownload?: (intent: FileDownloadIntent) => void | Promise<void>;
  onArtifactLocate?: (intent: FileLocateIntent) => void | Promise<void>;
  onArtifactRetry?: (intent: SlotRetryIntent) => void | Promise<void>;
  onArtifactPreview?: (intent: FilePreviewIntent) => Promise<import('../../work/types_v2').ArtifactPreview>;
  onArtifactConvert?: (intent: FileConversionIntent) => Promise<import('../../work/types_v2').ArtifactPreview>;
  // ── Chat input props ───────────────────────────────────────────
  chatItems?: Item[];
  chatLive?: LiveStream;
  chatRunning?: boolean;
  chatDisabled?: boolean;
  chatComposerSubmitKey?: ComposerSubmitKey;
  onChatSend?: (text: string) => void | Promise<void>;
  onCreateReusableWorkSession?: (input: {
    flowId: string;
    values: Record<string, unknown>;
    requestId: string;
  }) => Promise<CreateReusableWorkSessionResult>;
}

export function workStartDraftRequestID(
  startIntent: NonNullable<WorkCardProps['startIntent']>,
  signature: string,
  locale: string,
): string {
  const initialSignature = JSON.stringify([startIntent.prompt.trim(), null, locale]);
  const suffix = signature === initialSignature
    ? startIntent.id
    : `${startIntent.id}-${digestIntent(signature)}`;
  return `work-draft-${suffix}`;
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

function taskSessionKey(runId: string, taskId: string): string {
  return `${runId}\u0000${taskId}`;
}

function findTaskSessionSelection(work: Work, runId: string, taskId: string): RunSelection | null {
  const run = work.runs.find((candidate) => candidate.id === runId);
  if (!run) return null;
  for (const stage of run.stages ?? []) {
    for (const task of stage.tasks ?? []) {
      const tid = task.id || task.name;
      if (tid !== taskId) continue;
      const attempt = (task.attempts ?? []).reduce<typeof task.attempts[number] | undefined>(
        (latest, candidate) => candidate.sessionRef?.sessionPath && (!latest || candidate.index > latest.index)
          ? candidate
          : latest,
        undefined,
      );
      if (attempt) {
        return {
          runId: run.id,
          stageId: stage.id || stage.name || '',
          taskId: tid,
          attemptId: attempt.id,
          attemptIndex: attempt.index,
        };
      }
    }
  }
  return null;
}

function taskInfoTaskKeys(work: Work): Set<string> {
  const keys = new Set<string>();
  for (const run of work.runs) {
    for (const stage of run.stages ?? []) {
      for (const task of stage.tasks ?? []) {
        const tid = task.id || task.name;
        if (!tid) continue;
        if ((task.attempts ?? []).some((attempt) => Boolean(attempt.sessionRef?.sessionPath))) {
          keys.add(taskSessionKey(run.id, tid));
        }
      }
    }
  }
  return keys;
}

export const WorkCard: React.FC<WorkCardProps> = ({
  workID,
  ready = true,
  startIntent,
  onStartIntentConsumed,
  onStartIntentNeedsAttention,
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
  onOpenSession,
  onArtifactOpen,
  onArtifactDownload,
  onArtifactLocate,
  onArtifactRetry,
  onArtifactConvert,
  onArtifactPreview,
  chatItems = [],
  chatLive,
  chatRunning = false,
  chatDisabled = false,
  chatComposerSubmitKey = 'enter',
  onChatSend,
  onCreateReusableWorkSession,
}) => {
  const { locale } = useI18n();
  const view = useWorkStore((state) => state.works[workID]);
  const artifactSlots = useWorkStore((state) => selectArtifactSlots(state.artifactSlots, workID));
  const v2Definition = useWorkStore((state) => selectV2Definition(state.v2Definitions, workID));
  const v2ActiveDefinition = useWorkStore((state) => selectV2ActiveDefinition(state.v2ActiveDefinitions, state.v2Definitions, workID));
  const v2Tasks = useWorkStore((state) => selectV2Tasks(state.v2Tasks, workID));
  const v2Inputs = useWorkStore((state) => selectV2Inputs(state.v2Inputs, workID));
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
  const taskInfoKeys = useMemo(
    () => {
      const keys = view ? taskInfoTaskKeys(view.work) : new Set<string>();
      for (const task of v2Tasks) {
        if (task.sessionRef?.sessionPath) keys.add(taskSessionKey(task.runId, task.id));
      }
      return keys;
    },
    [v2Tasks, view],
  );
  const [taskSessionIdentity, setTaskSessionIdentity] = useState<{ runId: string; taskId: string } | null>(null);
  const [statuses, setStatuses] = useState<Record<string, WorkControllerStatus>>({});
  const [definitionImpact, setDefinitionImpact] = useState<RunImpact | undefined>();
  const [planningProgress, setPlanningProgress] = useState<DefinitionPlanProgress[]>([]);
  const [preferenceReady, setPreferenceReady] = useState(false);
  const [hasStoredPreference, setHasStoredPreference] = useState<boolean | null>(null);
  const [deepLinkState, setDeepLinkState] = useState<DeepLinkState>({ kind: 'idle' });
  const [restartTargetRunID, setRestartTargetRunID] = useState<string | null>(null);
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
  const readonly = !ready || !view || view.work.archiveState !== 'active';
  const archived = view?.work.archiveState === 'archived';
  const taskSessionTarget = useMemo<SessionSurfaceContext | undefined>(() => {
    if (!taskSessionIdentity) return undefined;
    const task = v2Tasks.find((candidate) =>
      candidate.runId === taskSessionIdentity.runId && candidate.id === taskSessionIdentity.taskId);
    if (!task?.sessionRef?.sessionPath) return undefined;
    return {
      workId: workID,
      runId: task.runId,
      stageId: 'v2-dag',
      taskId: task.id,
      attemptIndex: 0,
      sessionRef: task.sessionRef,
      readonly,
      archived,
    };
  }, [archived, readonly, taskSessionIdentity, v2Tasks, workID]);
  const faceIDs = useMemo<Record<WorkFace, string>>(() => ({
    front: `work-${workID}-front`,
    back: `work-${workID}-back`,
  }), [workID]);

  useEffect(() => {
    setStatuses(adapter.getStatuses());
    return adapter.subscribeStatus(() => setStatuses(adapter.getStatuses()));
  }, [adapter]);
  useEffect(() => adapter.subscribePlanning(workID, (progress) => {
    setPlanningProgress((current) => {
      const sameRequest = current.length === 0 || current[current.length - 1]?.requestId === progress.requestId;
      const base = sameRequest ? current : [];
      if (base.some((item) => item.sequence === progress.sequence)) return base;
      return [...base, progress].slice(-96);
    });
  }), [adapter, workID]);
  useEffect(() => {
    setDefinitionImpact(undefined);
    setPlanningProgress([]);
    setTaskSessionIdentity(null);
  }, [workID, v2Definition?.revision]);
  useEffect(() => () => adapter.dispose(), [adapter]);

  useEffect(() => {
    let current = true;
    setPreferenceReady(false);
    setHasStoredPreference(null);
    setDeepLinkState({ kind: 'idle' });
    restoredScroll.current = {};
    ensureCard(workID);
    if (!ready) {
      return () => { current = false; };
    }
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
  }, [adapter, ensureCard, ready, workID]);

  useEffect(() => {
    if (!preferenceReady || hasStoredPreference === null || !view) return;
    if (view.work.schemaVersion < 2 && !v2Definition) return;
    if (defaultFaceAppliedRef.current === workID) return;
    defaultFaceAppliedRef.current = workID;
    if (autoFlipRef.current?.startsWith(`${workID}:`)) return;
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
  const savePrompt = useCallback(async (prompt: string, name?: string): Promise<number> => {
    const current = useWorkStore.getState().works[workID];
    if (!current) throw new Error('Work 投影尚未载入。');
    const signature = JSON.stringify([prompt, name ?? null, locale]);
    if (draftIntentRef.current?.signature !== signature) {
      draftIntentRef.current = {
        signature,
        requestId: startIntent
          ? workStartDraftRequestID(startIntent, signature, locale)
          : `work-draft-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`,
      };
    }
    const result = await adapter.updateDraft({
      workId: workID,
      prompt,
      ...(name !== undefined ? { name } : {}),
      locale,
      expectedRevision: current.revision,
      requestId: draftIntentRef.current.requestId,
    });
    draftIntentRef.current = null;
    return result.revision;
  }, [adapter, locale, startIntent?.id, startIntent?.prompt, workID]);
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
  const handleSelectWorkInformationFile = useCallback(async (
    request: SelectWorkInformationFileRequest,
  ): Promise<SelectWorkInputFileResult> => adapter.selectWorkInformationFile(request), [adapter]);
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
    setTaskSessionIdentity(null);
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
  const handleTaskInfo = useCallback((runId: string, taskId: string) => {
    if (!view) return;
    const v2Task = v2Tasks.find((candidate) =>
      candidate.runId === runId && candidate.id === taskId && Boolean(candidate.sessionRef?.sessionPath));
    if (v2Task) {
      if (onOpenSession && v2Task.sessionRef) {
        const context: SessionSurfaceContext = {
          workId: workID,
          runId,
          stageId: 'v2-dag',
          taskId,
          attemptIndex: 0,
          sessionRef: v2Task.sessionRef,
          readonly,
          archived,
        };
        void Promise.resolve(onOpenSession(v2Task.sessionRef, context)).catch(() => undefined);
        return;
      }
      setTaskSessionIdentity({ runId, taskId });
      void adapter.setActiveFace(workID, 'back').catch(() => undefined);
      return;
    }
    const sel = findTaskSessionSelection(view.work, runId, taskId);
    if (!sel) return;
    const resolved = resolveSelection(view.work, sel);
    if (onOpenSession && resolved?.attempt?.sessionRef && resolved.stage && resolved.task) {
      const sessionRef = resolved.attempt.sessionRef;
      const context: SessionSurfaceContext = {
        workId: workID,
        runId: resolved.run.id,
        stageId: resolved.stage.id || resolved.stage.name,
        taskId: resolved.task.id || resolved.task.name,
        attemptId: resolved.attempt.id,
        attemptIndex: resolved.attempt.index,
        sessionRef,
        readonly,
        archived,
      };
      void Promise.resolve(onOpenSession(sessionRef, context)).catch(() => undefined);
      return;
    }
    setTaskSessionIdentity(null);
    setSelection(workID, sel);
    void adapter.setActiveFace(workID, 'back').catch(() => undefined);
  }, [adapter, archived, onOpenSession, readonly, setSelection, v2Tasks, view, workID]);

  const handleControlStart = useCallback((input: { workId: string; requestId: string }) => {
    return adapter.runWork(input);
  }, [adapter]);

  const handleControlPause = useCallback(async (input: { workId: string; runId: string; requestId: string }) => {
    if (!resolvedPort.pauseRun) throw new Error('暂停能力尚未连接。');
    await resolvedPort.pauseRun(input);
    await adapter.recoverSnapshot(input.workId);
  }, [adapter, resolvedPort.pauseRun]);

  const handleControlStop = useCallback(async (input: { workId: string; runId: string; requestId: string }) => {
    if (!resolvedPort.cancelRun) throw new Error('停止能力尚未连接。');
    await resolvedPort.cancelRun(input);
    await adapter.recoverSnapshot(input.workId);
  }, [adapter, resolvedPort.cancelRun]);

  const handleControlRestart = useCallback(async (input: { workId: string; runId: string; requestId: string }) => {
    if (!resolvedPort.restartRun) throw new Error('重启能力尚未连接。');
    const result = await resolvedPort.restartRun(input);
    await adapter.recoverSnapshot(input.workId);
    return result;
  }, [adapter, resolvedPort.restartRun]);

  const handleRestartRequest = useCallback((run: import('../../work/types').WorkflowRun) => {
    setRestartTargetRunID(run.id);
  }, []);

  const retrySync = useCallback(() => {
    adapter.retrySubscription(workID);
  }, [adapter, workID]);
  const retryPreference = useCallback(() => {
    void adapter.setActiveFace(workID, activeFace).catch(() => undefined);
  }, [activeFace, adapter, workID]);

  if (!view) {
    if (!ready) {
      return (
        <div className="wg2-work-card wg2-work-card-unknown wg2-work-card-pending" data-testid="work-card-pending" data-work-id={workID}>
          <div
            className="wg2-work-pending-notice"
            role="status"
            aria-live="polite"
            aria-busy="true"
          >
            <div className="wg2-work-pending-visual" aria-hidden="true">
              <span className="wg2-work-pending-orbit wg2-work-pending-orbit--outer" />
              <span className="wg2-work-pending-orbit wg2-work-pending-orbit--inner" />
              <span className="wg2-work-pending-core" />
            </div>
            <div className="wg2-work-pending-content">
              <div className="wg2-work-pending-kicker">
                <span className="wg2-work-pending-pulse" aria-hidden="true" />
                正在连接工作空间
              </div>
              <h3>正在载入工作</h3>
              <p className="wg2-work-pending-copy">正在恢复工作进度，连接完成后会自动进入。</p>
              <div className="wg2-work-pending-progress" aria-hidden="true">
                <span />
              </div>
              <p className="wg2-work-pending-id">
                <span>工作标识</span>
                <code title={workID}>{workID}</code>
              </p>
            </div>
          </div>
        </div>
      );
    }
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
      {!ready && <span className="wg2-work-status-fetching" data-testid="work-background-sync">后台同步中…</span>}
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
    <>
      <WorkWorkspace
      name={view.work.name}
      state={view.work.state}
      archiveState={view.work.archiveState}
      titleStatus={(
        <RunProgressPopover
          work={view.work}
          selection={selection}
          onSelect={handleRunSelect}
          onRetry={handleRetry}
          retryByTarget={retryByTarget}
          readonly={readonly}
          archived={archived}
          trigger={({ open, panelId, pin }) => (
            <WorkRunEntry
              workId={view.work.id}
              onRun={adapter.runWork}
              onResumeRun={adapter.resumeRun}
              onRecoverProjection={async () => { await adapter.recoverSnapshot(workID); }}
              disabled={readonly || archived}
              v2Definition={v2ActiveDefinition}
              onPlanStructure={() => handleFlip('back')}
              onV2TaskRetry={handleV2TaskRetry}
              onV2ArtifactRetry={onArtifactRetry ?? (
                resolvedPort.retryArtifactSlot ? handleArtifactRetry : undefined
              )}
              onProgressOpen={pin}
              progressOpen={open}
              progressPanelId={panelId}
            />
          )}
        />
      )}
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
              artifactSlots={artifactSlots}
              v2Definition={v2ActiveDefinition}
              v2Tasks={v2Tasks}
              v2Inputs={v2Inputs}
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
                await adapter.reconcileSnapshot(ctx.workId);
              }}
              onSelectWorkInputFile={
                resolvedPort.selectWorkInputFile ? handleSelectWorkInputFile : undefined
              }
              onSelectWorkInformationFile={
                resolvedPort.selectWorkInformationFile ? handleSelectWorkInformationFile : undefined
              }
              onAddCustomWorkInput={
                resolvedPort.addCustomWorkInput ? (req) => adapter.addCustomWorkInput(req) : undefined
              }
              onInferWorkInputs={
                resolvedPort.inferWorkInputs ? (req) => adapter.inferWorkInputs(req) : undefined
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
              onTaskInfo={taskInfoKeys.size > 0 ? handleTaskInfo : undefined}
              taskInfoTaskKeys={taskInfoKeys}
              onControlStart={resolvedPort.runWork ? handleControlStart : undefined}
              onControlResume={resolvedPort.resumeRun ? adapter.resumeRun : undefined}
              onControlPause={resolvedPort.pauseRun ? handleControlPause : undefined}
              onControlStop={resolvedPort.cancelRun ? handleControlStop : undefined}
              onControlRestart={resolvedPort.restartRun ? handleControlRestart : undefined}
              onControlRestartRequest={resolvedPort.restartRun ? handleRestartRequest : undefined}
              displayItems={chatItems}
              live={chatLive}
              running={chatRunning}
              chatDisabled={chatDisabled || !onChatSend}
              composerSubmitKey={chatComposerSubmitKey}
              onChatSend={onChatSend ?? (() => undefined)}
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
              startIntent={startIntent}
              onStartIntentConsumed={onStartIntentConsumed}
              onStartIntentNeedsAttention={onStartIntentNeedsAttention}
              readonly={readonly}
              archived={archived}
              slots={backSlots}
              selection={selection}
              sessionTarget={taskSessionTarget}
              resolveSessionSurface={resolveSessionSurface}
              onSavePrompt={savePrompt}
              onApplyDefinition={handleApplyDefinition}
              onCreateCandidate={
                resolvedPort.createCandidateRevision ? handleCreateCandidate : undefined
              }
              v2Definition={v2Definition}
              v2ActiveDefinition={v2ActiveDefinition}
              applyImpact={definitionImpact}
              planningProgress={planningProgress}
            />
          </div>
        </div>
      </div>
      </WorkWorkspace>
      {restartTargetRunID && (
        <WorkRestartDialog
          open
          workId={view.work.id}
          workName={view.work.name}
          runId={restartTargetRunID}
          onClose={() => setRestartTargetRunID(null)}
          onRestartCurrent={handleControlRestart}
          onPrepareFlow={async (input) => {
            if (!resolvedPort.prepareReusableFlow) throw new Error('常用流程能力尚未连接。');
            return resolvedPort.prepareReusableFlow(input);
          }}
          onSaveFlow={async (input) => {
            if (!resolvedPort.saveReusableFlow) throw new Error('常用流程保存能力尚未连接。');
            return resolvedPort.saveReusableFlow(input);
          }}
          onCreateSession={async (input) => {
            if (!onCreateReusableWorkSession) throw new Error('新工作 Session 能力尚未连接。');
            return onCreateReusableWorkSession(input);
          }}
        />
      )}
    </>
  );
};
