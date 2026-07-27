import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { JSDOM } from 'jsdom';
import React, { act, useEffect, type ReactElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { WorkCard } from '../components/work/WorkCard';
import type { WorkCardBackSlots } from '../components/work/WorkCardBack';
import { WorkFlipControl } from '../components/work/WorkFlipControl';
import { LocaleProvider } from '../lib/i18n';
import { WorkControllerAdapter, type WorkControllerPort, type WorkPortSubscription, type WorkUIPreference } from '../work/controller';
import { DefinitionDiff } from '../work/components/v2';
import { useWorkStore, useWorkUIStore } from '../work/store';
import { createWailsWorkControllerPort } from '../work/wailsAdapter';
import type {
  Attempt,
  BlockInstance,
  RetryTaskInput,
  SessionSurfaceContext,
  ViewRecoveryIntent,
  WorkView,
  WorkViewEvent,
  WorkflowRun,
} from '../work/types';
import type {
  ApplyDefinitionInput,
  ApplyDefinitionResult,
  ApplyWorkPatchRequest,
  ArtifactPreview,
  ArtifactSlot,
  CreateCandidateRevisionInput,
  CreateCandidateRevisionResult,
  PreviewWorkPatchRequest,
  PreviewArtifactRequest,
  PreviewArtifactResult,
  RequestArtifactConversionInput,
  RequestArtifactConversionResult,
  RetryArtifactSlotRequest,
  RetryArtifactSlotResult,
  RetryWorkNodeRequest,
  RetryWorkNodeResult,
  RunImpact,
  SetInputCornerstoneRequest,
  SubmitWorkInputRequest,
  TaskV2View,
  WorkInput,
  WorkPatchPreview,
  WorkDefinitionRevision,
} from '../work/types_v2';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else failed++;
}

function eq<T>(actual: T, expected: T, label: string): void {
  ok(actual === expected, `${label}${actual === expected ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function setupDOM(): JSDOM {
  const dom = new JSDOM('<!doctype html><html><body></body></html>', {
    pretendToBeVisual: true,
    url: 'http://localhost/',
  });
  Object.assign(globalThis, {
    IS_REACT_ACT_ENVIRONMENT: true,
    window: dom.window,
    document: dom.window.document,
    Node: dom.window.Node,
    Element: dom.window.Element,
    HTMLElement: dom.window.HTMLElement,
    SVGElement: dom.window.SVGElement,
    Event: dom.window.Event,
    MouseEvent: dom.window.MouseEvent,
    KeyboardEvent: dom.window.KeyboardEvent,
    MutationObserver: dom.window.MutationObserver,
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
  });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
  Object.assign(dom.window.HTMLElement.prototype, {
    attachEvent: () => undefined,
    detachEvent: () => undefined,
  });
  return dom;
}

const dom = setupDOM();

async function settle(delay = 30): Promise<void> {
  await act(async () => { await new Promise<void>((resolveWait) => setTimeout(resolveWait, delay)); });
}

async function interact(action: () => void): Promise<void> {
  await act(async () => {
    action();
    await new Promise<void>((resolveWait) => setTimeout(resolveWait, 30));
  });
}

interface Mounted {
  host: HTMLDivElement;
  root: Root;
  cleanup: () => Promise<void>;
}

async function mount(element: ReactElement): Promise<Mounted> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host, { onCaughtError: (error) => { throw error; } });
  await act(async () => { root.render(<LocaleProvider>{element}</LocaleProvider>); });
  await settle();
  return {
    host,
    root,
    cleanup: async () => {
      await act(async () => { root.unmount(); });
      host.remove();
    },
  };
}

function reset(): void {
  useWorkStore.getState().clearAll();
  useWorkUIStore.getState().clearAll();
}

class TestPort implements WorkControllerPort {
  private listeners = new Map<string, (event: WorkViewEvent) => void>();
  readonly preferences = new Map<string, WorkUIPreference>();
  readonly operations: string[] = [];
  preferenceFailures = 0;
  snapshotFailures = 0;
  retryFailures = 0;
  readonly retryInputs: RetryTaskInput[] = [];

  // V2 fields
  readonly applyInputs: ApplyDefinitionInput[] = [];
  readonly candidateInputs: CreateCandidateRevisionInput[] = [];
  readonly candidateErrors: Array<Error & { code?: string }> = [];
  applyFailures = 0;
  applyNext: Partial<ApplyDefinitionResult> = {};
  /** Non-null when createCandidateRevision committed: fetchSnapshot returns
   *  a view with this revision so the controller can refresh the store. */
  candidateSnapshotRevision: number | null = null;
  /** When true, fetchSnapshot ignores candidateSnapshotRevision and returns
   *  the stale store view — simulates refresh failure. */
  candidateSnapshotStale = false;
  readonly v2RetryInputs: RetryWorkNodeRequest[] = [];
  v2RetryFailures = 0;
  readonly v2RetryNext: RetryWorkNodeResult[] = [];
  readonly v2RetryBeforeResponse: Array<() => void> = [];
  readonly artifactRetryInputs: RetryArtifactSlotRequest[] = [];
  readonly artifactRetryResults: Array<Promise<RetryArtifactSlotResult> | RetryArtifactSlotResult> = [];
  readonly artifactPreviewInputs: PreviewArtifactRequest[] = [];
  readonly artifactConversionInputs: RequestArtifactConversionInput[] = [];
  readonly artifactConversionResults: RequestArtifactConversionResult[] = [];

  subscribe(workID: string, onEvent: (event: WorkViewEvent) => void): WorkPortSubscription {
    this.listeners.set(workID, onEvent);
    return { ready: Promise.resolve(), unsubscribe: () => { this.listeners.delete(workID); } };
  }

  async fetchSnapshot(workID: string): Promise<WorkView> {
    this.operations.push(`snapshot:${workID}`);
    if (this.snapshotFailures-- > 0) throw new Error('snapshot unavailable');
    const view = useWorkStore.getState().works[workID];
    if (!view) throw new Error(`no projection for ${workID}`);
    if (this.candidateSnapshotRevision !== null && !this.candidateSnapshotStale) {
      return { ...view, revision: this.candidateSnapshotRevision };
    }
    return view;
  }

  async fetchRecoverySnapshot(workID: string, intent: ViewRecoveryIntent): Promise<WorkViewEvent> {
    const view = await this.fetchSnapshot(workID);
    return {
      schemaVersion: 1,
      type: 'snapshot',
      workID,
      eventID: `wv-resync-${workID}-rev-${view.revision}-retry-${intent.generation}`,
      revision: view.revision,
      baseRevision: 0,
      requestID: 'retry-recovery',
      object: { kind: 'work', id: workID },
      resync: { reason: 'retry', authoritative: true, generation: intent.generation },
      payload: view,
      createdAt: view.work.updatedAt,
    };
  }

  async readUIPreference(workID: string): Promise<WorkUIPreference | null> {
    this.operations.push(`read:${workID}`);
    return this.preferences.get(workID) ?? null;
  }

  async writeUIPreference(workID: string, preference: WorkUIPreference): Promise<void> {
    this.operations.push(`write:${preference.activeFace}`);
    if (this.preferenceFailures-- > 0) throw new Error('preference unavailable');
    this.preferences.set(workID, preference);
  }

  async retryTask(input: RetryTaskInput): Promise<Attempt> {
    this.retryInputs.push(input);
    if (this.retryFailures-- > 0) throw new Error('retry unavailable');
    return {
      id: 'attempt-1',
      index: 1,
      state: 'pending',
      sessionRef: { sessionPath: '/sessions/retry', branchId: 'main', modelRef: 'test', turnCount: 0, preview: '', startedAt: '2026-07-20T10:02:00Z' },
      startedAt: '2026-07-20T10:02:00Z',
    };
  }

  async applyDefinition(input: ApplyDefinitionInput): Promise<ApplyDefinitionResult> {
    this.applyInputs.push(input);
    if (this.applyFailures-- > 0) throw new Error('apply unavailable');
    const defaults: ApplyDefinitionResult = {
      revision: 2,
      duplicate: false,
      committed: true,
      recoverable: false,
      intent: !this.applyNext.duplicate ? {
        workId: input.workId,
        runId: 'run-v2-1',
        definitionRev: input.revision,
        reason: 'definition applied',
      } : undefined,
    };
    return { ...defaults, ...this.applyNext };
  }

  async createCandidateRevision(
    input: CreateCandidateRevisionInput,
  ): Promise<CreateCandidateRevisionResult> {
    this.candidateInputs.push(input);
    const plannedError = this.candidateErrors.shift();
    if (plannedError) throw plannedError;
    const store = useWorkStore.getState();
    const base = store.v2ActiveDefinitions[input.workId] ?? store.v2Definitions[input.workId];
    if (!base) throw new Error('definition unavailable');
    const plannedNode = {
      id: 'node-planned',
      title: 'Planned by backend',
      description: input.intent,
    };
    const newRevision = input.expectedRevision + 1;
    // Let controller refresh via fetchSnapshot (authoritative path).
    this.candidateSnapshotRevision = newRevision;
    return {
      candidate: {
        ...base,
        revision: base.revision + 1,
        parentRevision: base.revision,
        status: 'draft',
        goal: input.intent,
        nodes: [...base.nodes, plannedNode],
        digest: 'candidate-digest',
      },
      impact: {
        keptNodeIds: base.nodes.map((node) => node.id),
        invalidatedNodeIds: [],
        newNodeIds: [plannedNode.id],
        removedNodeIds: [],
        requiresRerun: true,
      },
      revision: newRevision,
      duplicate: false,
      committed: true,
      recoverable: false,
    };
  }

  async retryWorkNode(input: RetryWorkNodeRequest): Promise<RetryWorkNodeResult> {
    this.v2RetryInputs.push(input);
    if (this.v2RetryFailures-- > 0) throw new Error('node retry unavailable');
    this.v2RetryBeforeResponse.shift()?.();
    const next = this.v2RetryNext.shift();
    if (next) return next;
    return {
      revision: input.expectedRevision + 1,
      duplicate: false,
      committed: true,
      recoverable: false,
    };
  }

  async retryArtifactSlot(input: RetryArtifactSlotRequest): Promise<RetryArtifactSlotResult> {
    this.artifactRetryInputs.push(input);
    const result = this.artifactRetryResults.shift();
    if (result) return result;
    return {
      revision: input.expectedRevision + 1,
      duplicate: false,
      committed: true,
      recoverable: false,
    };
  }

  async previewArtifact(input: PreviewArtifactRequest): Promise<PreviewArtifactResult> {
    this.artifactPreviewInputs.push(input);
    return {
      preview: {
        artifactId: input.artifactId,
        workId: input.workId,
        grade: 'filecard',
        canOpen: true,
        canConvert: true,
        summary: 'Office file',
      },
      committed: true,
      recoverable: false,
    };
  }

  async requestArtifactConversion(
    input: RequestArtifactConversionInput,
  ): Promise<RequestArtifactConversionResult> {
    this.artifactConversionInputs.push(input);
    const planned = this.artifactConversionResults.shift();
    if (planned) return planned;
    const preview: ArtifactPreview = {
      artifactId: input.artifactId,
      workId: input.workId,
      grade: 'inline',
      canOpen: true,
      canConvert: false,
      textContent: 'converted through port',
      conversionState: 'completed',
    };
    return {
      preview,
      committed: true,
      recoverable: false,
      duplicate: false,
    };
  }
}

function makeBlock(id: string): BlockInstance {
  return {
    id,
    kind: 'notice',
    schemaVersion: 1,
    revision: 1,
    status: 'ready',
    title: id,
    data: { text: id },
    source: { provider: 'controller', mode: 'snapshot', verified: true },
    fallback: { summary: id },
    createdAt: '2026-07-20T10:00:00Z',
    updatedAt: '2026-07-20T10:00:00Z',
  };
}

function makeView(workID: string, overrides: Partial<WorkView['work']> = {}): WorkView {
  const blueprintRef = { id: 'blueprint:test', schemaVersion: 1, version: 1 };
  return {
    schemaVersion: 1,
    revision: 1,
    work: {
      schemaVersion: 1,
      id: workID,
      name: workID,
      state: 'ready',
      archiveState: 'active',
      blueprintRef,
      definitionSnapshot: {
        schemaVersion: 1,
        revision: 1,
        blueprintRef,
        promptTemplate: '',
        workflow: { stages: [] },
        blockSpecs: [],
        digest: 'digest',
      },
      blocks: [],
      placements: [],
      prompt: '',
      cornerstones: [],
      runs: [],
      createdWith: { workSchemaVersion: 1, eventSchemaVersion: 1, rendererSetVersion: 1 },
      createdAt: '2026-07-20T10:00:00Z',
      updatedAt: '2026-07-20T10:00:00Z',
      ...overrides,
    },
  };
}

function makeFailedRun(workID: string): WorkflowRun {
  return {
    id: 'run-1',
    workId: workID,
    definitionDigest: 'digest',
    state: 'failed',
    startedAt: '2026-07-20T10:00:00Z',
    finishedAt: '2026-07-20T10:01:00Z',
    stages: [{
      id: 'stage-review',
      name: 'review',
      state: 'failed',
      startedAt: '2026-07-20T10:00:00Z',
      finishedAt: '2026-07-20T10:01:00Z',
      tasks: [{
        id: 'task-lint',
        name: 'lint',
        state: 'failed',
        attempts: [{
          id: 'attempt-0',
          index: 0,
          state: 'failed',
          error: 'timeout',
          sessionRef: { sessionPath: '/sessions/failed', branchId: 'branch-a', modelRef: 'test', turnCount: 2, preview: 'failed attempt', startedAt: '2026-07-20T10:00:00Z' },
          startedAt: '2026-07-20T10:00:00Z',
          finishedAt: '2026-07-20T10:01:00Z',
        }],
      }],
    }],
  };
}

async function testFacesAndFixedWorkspace(): Promise<void> {
  reset();
  const view = makeView('work-faces', { state: 'running', blocks: [makeBlock('block-a')] });
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort();
  const mounts: Record<string, number> = {};
  const unmounts: Record<string, number> = {};
  const Probe = ({ name }: { name: string }) => {
    useEffect(() => {
      mounts[name] = (mounts[name] ?? 0) + 1;
      return () => { unmounts[name] = (unmounts[name] ?? 0) + 1; };
    }, [name]);
    return <div data-probe={name}>{name}</div>;
  };
  const backSlots: WorkCardBackSlots = {
    transcript: <Probe name="transcript" />,
    runApproval: <Probe name="run" />,
    artifactShelf: <Probe name="artifact" />,
    queue: <Probe name="queue" />,
    composer: <Probe name="composer" />,
  };
  const mounted = await mount(
    <WorkCard
      workID="work-faces"
      port={port}
      backSlots={backSlots}
      cornerstoneEntry={<Probe name="cornerstone" />}
      addonPanel={<Probe name="addon" />}
    />,
  );

  const front = mounted.host.querySelector<HTMLElement>('[data-testid="work-face-front"]')!;
  const back = mounted.host.querySelector<HTMLElement>('[data-testid="work-face-back"]')!;
  const outer = mounted.host.querySelector('[data-testid="work-outer-header"]');
  const transcript = mounted.host.querySelector('[data-probe="transcript"]');
  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  ok(Boolean(outer && front && back && transcript), 'workspace and both real faces render');
  eq(flip.textContent?.trim(), '会话', 'front flip entry says 会话');
  eq(flip.getAttribute('aria-controls'), back.id, 'flip control references target face');
  eq(front.getAttribute('aria-hidden'), 'false', 'front is exposed');
  ok(back.hasAttribute('inert'), 'inactive back is inert');

  await interact(() => flip.click());
  eq(useWorkUIStore.getState().cardByWork['work-faces'].activeFace, 'back', 'flip updates UI preference only');
  eq(useWorkStore.getState().works['work-faces'].work.state, 'running', 'running Work is not interrupted');
  eq(mounted.host.querySelector('[data-testid="work-outer-header"]'), outer, 'outer workspace keeps DOM identity');
  eq(mounted.host.querySelector('[data-probe="transcript"]'), transcript, 'session surface keeps DOM identity');
  eq(mounts.transcript, 1, 'session surface mounts once');
  eq(unmounts.transcript ?? 0, 0, 'session surface does not unmount on flip');
  eq(mounts.addon, 1, 'AddOn mounts once outside flip');
  eq(unmounts.addon ?? 0, 0, 'AddOn does not unmount on flip');
  ok(front.hasAttribute('inert'), 'inactive front becomes inert');
  ok(!back.hasAttribute('inert'), 'active back is interactive');
  ok(back.querySelector('[data-testid="work-prompt-editor"]') != null, 'back face exposes the natural-language task input');
  eq(back.querySelectorAll('textarea').length, 1, 'back face exposes only one text input');
  ok(!back.textContent?.includes('JSON') && !back.textContent?.includes('Inputs'), 'back face hides internal JSON inputs');
  eq(flip.textContent?.trim(), '工作流', 'back flip entry says 工作流');
  eq(port.preferences.get('work-faces')?.activeFace, 'back', 'last face is persisted by Work ID');
  await mounted.cleanup();
}

async function testScrollExpandedAndDraft(): Promise<void> {
  reset();
  const view = makeView('work-local', {
    blocks: [makeBlock('block-local')],
    placements: [{ blockId: 'block-local', slot: 'primary', order: 0, collapsed: true }],
  });
  useWorkStore.getState().applySnapshot(view);
  const ui = useWorkUIStore.getState();
  ui.ensureCard('work-local');
  ui.setScroll('work-local', 'front', { scrollTop: 37, scrollLeft: 4 });
  ui.setScroll('work-local', 'back', { scrollTop: 91, scrollLeft: 7 });
  ui.setExpanded('work-local', 'front', 'block-local', true);
  ui.setDraft('work-local', 'back', 'saved draft');
  const slots: WorkCardBackSlots = {
    transcript: <div>Transcript</div>,
    composer: ({ draft, onDraftChange }) => (
      <button type="button" data-testid="draft-editor" onClick={() => onDraftChange('changed draft')}>{draft}</button>
    ),
  };
  const mounted = await mount(<WorkCard workID="work-local" port={new TestPort()} backSlots={slots} />);
  const front = mounted.host.querySelector<HTMLElement>('[data-testid="work-face-front"]')!;
  const back = mounted.host.querySelector<HTMLElement>('[data-testid="work-face-back"]')!;
  eq(front.scrollTop, 37, 'front scroll restores from per-Work UI state');
  eq(front.scrollLeft, 4, 'front horizontal scroll restores');
  eq(back.scrollTop, 91, 'back scroll restores while mounted inactive');
  eq(back.scrollLeft, 7, 'back horizontal scroll restores');
  eq(mounted.host.querySelector('[data-testid="draft-editor"]')?.textContent, 'saved draft', 'draft restores into existing Composer adapter');
  eq(mounted.host.querySelector('[data-testid="work-block-block-local"]')?.classList.contains('wg2-work-block-expanded'), true, 'expanded state restores');

  await interact(() => {
    front.scrollTop = 144;
    front.scrollLeft = 9;
    front.dispatchEvent(new dom.window.Event('scroll', { bubbles: true }));
  });
  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  await interact(() => flip.click());
  await interact(() => {
    back.scrollTop = 288;
    back.scrollLeft = 11;
    back.dispatchEvent(new dom.window.Event('scroll', { bubbles: true }));
    mounted.host.querySelector<HTMLButtonElement>('[data-testid="draft-editor"]')!.click();
  });
  await interact(() => flip.click());
  const local = useWorkUIStore.getState().cardByWork['work-local'];
  eq(local.faces.front.scroll.scrollTop, 144, 'front scroll writes to its own face state');
  eq(local.faces.back.scroll.scrollTop, 288, 'back scroll writes to its own face state');
  eq(local.faces.back.draft, 'changed draft', 'Composer draft survives face switching');
  eq(front.scrollTop, 144, 'front DOM scroll is unchanged after round trip');
  eq(local.faces.front.expanded['block-local'], true, 'expanded state survives face switching');
  await mounted.cleanup();
}

async function testDeepLinkOrderAndMissingReason(): Promise<void> {
  reset();
  const view = makeView('work-deep');
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort();
  port.preferences.set('work-deep', { activeFace: 'front' });
  const operations = port.operations;
  const slots: WorkCardBackSlots = {
    transcript: <button type="button" data-work-target-id="message-7" onFocus={() => operations.push('focus:message-7')}>message</button>,
  };
  const mounted = await mount(
    <WorkCard workID="work-deep" port={port} backSlots={slots} deepLink={{ face: 'back', targetID: 'message-7' }} />,
  );
  await settle();
  eq(useWorkUIStore.getState().cardByWork['work-deep'].activeFace, 'back', 'deep link switches to requested face');
  eq((document.activeElement as HTMLElement | null)?.dataset.workTargetId, 'message-7', 'deep link focuses requested target');
  const readIndex = operations.indexOf('read:work-deep');
  const writeIndex = operations.indexOf('write:back');
  const focusIndex = operations.indexOf('focus:message-7');
  ok(readIndex >= 0 && readIndex < writeIndex && writeIndex < focusIndex, 'preference restore, face switch, and focus happen in order');
  await mounted.cleanup();

  reset();
  useWorkStore.getState().applySnapshot(makeView('work-late'));
  const late = await mount(
    <WorkCard
      workID="work-late"
      port={new TestPort()}
      backSlots={{ transcript: <div data-testid="late-transcript" /> }}
      deepLink={{ face: 'back', targetID: 'late-message' }}
    />,
  );
  const lateTarget = document.createElement('button');
  lateTarget.dataset.workTargetId = 'late-message';
  late.host.querySelector('[data-testid="late-transcript"]')!.appendChild(lateTarget);
  await settle();
  eq(document.activeElement, lateTarget, 'deep link recovers when session target hydrates late');
  ok(!late.host.querySelector('[data-testid="work-deeplink-missing"]'), 'late target does not produce a stale-target error');
  await late.cleanup();

  reset();
  useWorkStore.getState().applySnapshot(makeView('work-missing', { blocks: [makeBlock('known')] }));
  const missing = await mount(
    <WorkCard workID="work-missing" port={new TestPort()} deepLink={{ face: 'front', targetID: 'removed' }} />,
  );
  await settle();
  const alert = missing.host.querySelector('[data-testid="work-deeplink-missing"]');
  ok(Boolean(alert), 'invalid deep link exposes a visible reason');
  ok(alert?.textContent?.includes('不存在') ?? false, 'invalid deep link explains that target is gone');
  ok(Boolean(missing.host.querySelector('[data-testid="work-card-front"]')), 'invalid deep link keeps a readable Work overview');
  await missing.cleanup();
}

async function testStructuredAttemptDeepLinkUsesExplicitSessionOwner(): Promise<void> {
  reset();
  const workID = 'work-attempt-link';
  useWorkStore.getState().applySnapshot(makeView(workID, { state: 'failed', runs: [makeFailedRun(workID)] }));
  let resolved: SessionSurfaceContext | undefined;
  const mounted = await mount(
    <WorkCard
      workID={workID}
      port={new TestPort()}
      deepLink={{
        face: 'back',
        target: { runId: 'run-1', stageId: 'stage-review', taskId: 'task-lint', attemptId: 'attempt-0', attemptIndex: 0 },
      }}
      resolveSessionSurface={(_sessionRef, context) => {
        resolved = context;
        return <div data-testid="real-session-surface">session</div>;
      }}
    />,
  );
  await settle();
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'structured attempt link opens the back face');
  ok(Boolean(mounted.host.querySelector('[data-testid="real-session-surface"]')), 'selected SessionRef resolves a real React surface');
  eq(resolved?.workId, workID, 'session resolver receives explicit Work owner');
  eq(resolved?.runId, 'run-1', 'session resolver receives explicit Run owner');
  eq(resolved?.stageId, 'stage-review', 'session resolver receives explicit Stage owner');
  eq(resolved?.taskId, 'task-lint', 'session resolver receives explicit Task owner');
  eq(resolved?.attemptId, 'attempt-0', 'session resolver receives explicit Attempt owner');
  eq((document.activeElement as HTMLElement | null)?.dataset.workTargetId, 'attempt:run-1:stage-review:task-lint:attempt-0', 'structured link focuses the selected Session surface');
  await mounted.cleanup();
}

async function testRetryFailureIsVisibleAndReusesRequestID(): Promise<void> {
  reset();
  const workID = 'work-retry';
  useWorkStore.getState().applySnapshot(makeView(workID, { state: 'failed', runs: [makeFailedRun(workID)] }));
  const port = new TestPort();
  port.retryFailures = 1;
  const mounted = await mount(<WorkCard workID={workID} port={port} />);
  const retry = mounted.host.querySelector<HTMLButtonElement>('.wg2-run-retry-button')!;
  await interact(() => retry.click());
  ok(mounted.host.textContent?.includes('重试失败：retry unavailable') ?? false, 'retry failure is explicit and observable');
  eq(port.retryInputs.length, 1, 'failed retry dispatches once');
  const requestID = port.retryInputs[0].requestId;
  await interact(() => retry.click());
  eq(port.retryInputs.length, 2, 'explicit retry dispatches again after failure');
  eq(port.retryInputs[1].requestId, requestID, 'safe retry reuses the same requestId');
  eq(port.retryInputs[1].stageId, 'stage-review', 'retry uses stable Stage ID');
  eq(port.retryInputs[1].taskId, 'task-lint', 'retry uses stable Task ID');
  ok(retry.disabled, 'successful dispatch remains pending until a newer Attempt enters the projection');
  await mounted.cleanup();
}

async function testPreferenceFailureRetry(): Promise<void> {
  reset();
  useWorkStore.getState().applySnapshot(makeView('work-pref'));
  const port = new TestPort();
  port.preferenceFailures = 1;
  const mounted = await mount(<WorkCard workID="work-pref" port={port} />);
  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  await interact(() => flip.click());
  eq(useWorkUIStore.getState().cardByWork['work-pref'].activeFace, 'back', 'failed persistence does not roll back local face');
  const error = mounted.host.querySelector<HTMLElement>('[data-testid="work-pref-error"]');
  ok(Boolean(error && error.textContent?.includes('preference unavailable')), 'preference failure is explicit');
  await interact(() => error.querySelector<HTMLButtonElement>('button')!.click());
  ok(!mounted.host.querySelector('[data-testid="work-pref-error"]'), 'preference retry clears the visible failure');
  eq(port.preferences.get('work-pref')?.activeFace, 'back', 'preference retry persists current face idempotently');
  await mounted.cleanup();
}

async function testArchiveAndUnavailableSession(): Promise<void> {
  reset();
  useWorkStore.getState().applySnapshot(makeView('work-archive', { archiveState: 'archived', state: 'completed' }));
  const mounted = await mount(
    <WorkCard
      workID="work-archive"
      port={new TestPort()}
      backSlots={{ transcript: <div data-testid="archive-transcript">history</div>, composer: <div data-testid="archive-composer" /> }}
    />,
  );
  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  ok(!flip.disabled, 'archived Work remains flippable');
  await interact(() => flip.click());
  ok(Boolean(mounted.host.querySelector('[data-testid="archive-transcript"]')), 'archived Work can browse existing transcript');
  ok(!mounted.host.querySelector('[data-testid="archive-composer"]'), 'archived Work does not expose Composer');
  ok(Boolean(mounted.host.querySelector('[data-testid="work-back-readonly-notice"]')), 'archive readonly state is visible');
  await mounted.cleanup();

  reset();
  useWorkStore.getState().applySnapshot(makeView('work-no-session'));
  const unavailable = await mount(<WorkCard workID="work-no-session" port={new TestPort()} />);
  ok(Boolean(unavailable.host.querySelector('[data-testid="work-session-unavailable"]')), 'missing session integration fails visibly');
  ok(!unavailable.host.querySelector('[data-testid^="work-back-placeholder-"]'), 'no parallel placeholder chat surfaces are created');
  await unavailable.cleanup();
}

async function testPlacementAndFlipAccessibility(): Promise<void> {
  reset();
  useWorkStore.getState().applySnapshot(makeView('work-order', {
    blocks: [makeBlock('later'), makeBlock('first')],
    placements: [
      { blockId: 'later', slot: 'primary', order: 2 },
      { blockId: 'first', slot: 'attention', order: 1 },
    ],
  }));
  const mounted = await mount(<WorkCard workID="work-order" port={new TestPort()} />);
  const blocks = [...mounted.host.querySelectorAll<HTMLElement>('[data-block-id]')].map((node) => node.dataset.blockId);
  eq(blocks.join(','), 'first,later', 'front honors placement order and passes placement to BlockHost');
  const button = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  eq(button.tagName, 'BUTTON', 'flip entry uses native keyboard semantics');
  eq(button.type, 'button', 'flip entry cannot submit an enclosing form');
  button.focus();
  eq(document.activeElement, button, 'flip entry is keyboard focusable');
  await mounted.cleanup();

  let target: string | undefined;
  const control = await mount(<WorkFlipControl activeFace="front" onFlip={(face) => { target = face; }} />);
  await interact(() => control.host.querySelector<HTMLButtonElement>('button')!.click());
  eq(target, 'back', 'native activation emits the target face once');
  await control.cleanup();
}

function testMotionCSSContract(): void {
  const css = readFileSync(resolve(process.cwd(), 'src/styles.css'), 'utf8');
  ok(/\.wg2-work-face\s*\{[\s\S]*?transition:\s*transform 240ms ease/.test(css), 'standard face transition is 240ms');
  ok(/prefers-reduced-motion:\s*reduce[\s\S]*?transition:\s*opacity 150ms ease/.test(css), 'reduced motion uses a fade');
  ok(/max-width:\s*480px[\s\S]*?\.wg2-work-face\s*\{[\s\S]*?transition:\s*none/.test(css), 'narrow layout uses an ordinary switch');
}

async function testUnknownWorkRetry(): Promise<void> {
  reset();
  const port = new TestPort();
  port.snapshotFailures = 1;
  const mounted = await mount(<WorkCard workID="unknown" port={port} />);
  ok(Boolean(mounted.host.querySelector('[data-testid="work-card-unknown"]')), 'unknown Work has a readable fallback');
  ok(Boolean(mounted.host.querySelector('button')), 'unknown Work exposes retry');
  await mounted.cleanup();
}

// ── V2 integration tests ────────────────────────────────────────────────────

function makeV2Definition(workId: string): WorkDefinitionRevision {
  return {
    workId,
    revision: 1,
    parentRevision: 0,
    status: 'active',
    goal: 'test goal',
    nodes: [{
      id: 'node-1',
      title: 'Analyze',
      description: 'Analyze data',
    }],
    artifactSlots: [{
      id: 'slot-1',
      title: 'Report',
      kind: 'docx',
      expectedCount: 1,
      required: true,
    }],
    inputSpecs: [{
      id: 'spec-1',
      label: 'Name',
      kind: 'text' as const,
      required: true,
      pinEligible: false,
    }],
    createdBy: 'test',
    createdAt: '2026-07-24T00:00:00Z',
    digest: 'sha256:abc',
  };
}

async function testV2AutoFlipOnApplyDefinition(): Promise<void> {
  reset();
  const workID = 'work-v2-autoswitch';
  const view = makeView(workID, { state: 'ready' });
  useWorkStore.getState().applySnapshot(view);
  // Seed V2 definition in store
  const def = makeV2Definition(workID);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...def, status: 'draft' } },
    artifactSlots: { ...s.artifactSlots, [workID]: [] },
    v2Tasks: { ...s.v2Tasks, [workID]: [] },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'draft V2 defaults to back face');

  // Click Apply — should auto-flip to front
  ok(Boolean(mounted.host.querySelector('[data-testid="work-planning-definition"]')), 'production planning definition is mounted');
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')!.click());
  await settle(50);
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'front', 'auto-flip to front after ApplyDefinition success');
  eq(port.applyInputs.length, 1, 'ApplyDefinition called exactly once');
  ok(port.applyInputs[0].requestId.startsWith('work-definition-'), 'production entry creates typed stable requestId');

  // Now simulate definition becoming active in store
  await act(async () => {
    useWorkStore.setState((s) => ({
      v2Definitions: { ...s.v2Definitions, [workID]: { ...def, status: 'active' } },
    }));
    await Promise.resolve();
  });
  ok(Boolean(mounted.host.querySelector('[data-testid="result-shelf"]') || mounted.host.querySelector('[data-testid="result-shelf-empty"]')), 'ResultShelf renders after V2 activation');
  ok(Boolean(mounted.host.querySelector('[data-testid="execution-list-empty"]')), 'ExecutionList renders after V2 activation');

  await mounted.cleanup();
}

async function testV2PlanningFaceDefaultsAndSnapshotDedup(): Promise<void> {
  reset();
  const workID = 'work-v2-planning-face';
  const view = makeView(workID, {
    schemaVersion: 2,
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort();
  let mounted = await mount(<WorkCard workID={workID} port={port} />);
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'schema2 Work without active definition defaults to back');

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'front', 'manual planning face flip remains available');
  await act(async () => {
    useWorkStore.getState().applySnapshot(view, 'duplicate-planning-snapshot');
    await Promise.resolve();
  });
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'front', 'duplicate snapshot does not repeatedly force back');
  await mounted.cleanup();

  useWorkUIStore.getState().clearAll();
  mounted = await mount(<WorkCard workID={workID} port={new TestPort()} />);
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'refresh without active definition returns to back');
  await mounted.cleanup();
}

async function testV2DuplicateApplyNoReflip(): Promise<void> {
  reset();
  const workID = 'work-v2-dup';
  const view = makeView(workID, { state: 'ready' });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...makeV2Definition(workID), status: 'draft' } },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'draft V2 starts on back');

  // First Apply — auto-flip to front
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')!.click());
  await settle(50);
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'front', 'first apply flips to front');

  // Flip back to back
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'manual flip back to back');

  // Second Apply with same requestId — duplicate result
  port.applyNext = { duplicate: true, committed: true, revision: 2 };
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')!.click());
  await settle(50);
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'duplicate apply does NOT re-flip to front');
  eq(port.applyInputs.length, 2, 'Apply called twice but second was duplicate');

  await mounted.cleanup();
}

async function testV2FirstObservedDuplicateStillFlipsOnce(): Promise<void> {
  reset();
  const workID = 'work-v2-duplicate-recovery';
  const view = makeView(workID, { state: 'ready' });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...makeV2Definition(workID), status: 'draft' } },
  }));
  const port = new TestPort();
  port.applyNext = { duplicate: true, committed: true, revision: 2, intent: undefined };
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'duplicate recovery starts on planning face');
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')!.click());
  await settle(50);
  eq(
    useWorkUIStore.getState().cardByWork[workID].activeFace,
    'front',
    'first observed committed duplicate recovers the single automatic flip',
  );

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  port.applyNext = { duplicate: true, committed: true, revision: 2, intent: undefined };
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')!.click());
  await settle(50);
  eq(
    useWorkUIStore.getState().cardByWork[workID].activeFace,
    'back',
    'later duplicate for the same definition revision cannot repeat the automatic flip',
  );

  await mounted.cleanup();
}

async function testV2CommittedRecoveryBeforeFlip(): Promise<void> {
  reset();
  const workID = 'work-v2-committed-recovery';
  const view = makeView(workID, { state: 'ready' });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...makeV2Definition(workID), status: 'draft' } },
  }));
  const port = new TestPort();
  port.applyNext = {
    intent: undefined,
    committed: true,
    recoverable: true,
    transportError: {
      code: 'committed_recovery',
      message: 'projection delivery interrupted after commit',
      operation: 'definition-view',
      workId: workID,
      requestId: 'server-request',
      revision: view.revision + 1,
      committed: true,
      recoverable: true,
    },
  };
  const mounted = await mount(<WorkCard workID={workID} port={port} />);
  const snapshotsBefore = port.operations.filter((operation) => operation === `snapshot:${workID}`).length;
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')!.click());
  await settle(50);

  const recoveryIndex = port.operations.lastIndexOf(`snapshot:${workID}`);
  const flipIndex = port.operations.lastIndexOf('write:front');
  eq(
    port.operations.filter((operation) => operation === `snapshot:${workID}`).length,
    snapshotsBefore + 1,
    'committed recovery re-reads authoritative projection once',
  );
  ok(flipIndex > recoveryIndex, 'committed recovery completes before automatic flip persistence');
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'front', 'committed recovery still flips exactly once');

  await mounted.cleanup();
}

async function testV2ManualFlipIndependent(): Promise<void> {
  reset();
  const workID = 'work-v2-manual';
  const view = makeView(workID, { state: 'running' });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...makeV2Definition(workID), status: 'active' } },
    artifactSlots: { ...s.artifactSlots, [workID]: [] },
    v2Tasks: { ...s.v2Tasks, [workID]: [] },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'front', 'default face is front');
  ok(Boolean(mounted.host.querySelector('[data-testid="result-shelf-empty"]')), 'V2 empty shelf visible on front');

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'manual flip to back');
  eq(useWorkStore.getState().works[workID].work.state, 'running', 'running state preserved across flip');

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'front', 'manual flip back to front');

  await mounted.cleanup();
}

async function testV2ScrollDraftExpandPerWork(): Promise<void> {
  reset();
  const viewA = makeView('work-v2-a', { blocks: [makeBlock('block-a')], placements: [{ blockId: 'block-a', slot: 'primary', order: 0, collapsed: true }] });
  useWorkStore.getState().applySnapshot(viewA);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, 'work-v2-a': { ...makeV2Definition('work-v2-a'), status: 'active' } },
  }));
  const ui = useWorkUIStore.getState();
  ui.ensureCard('work-v2-a');
  ui.setScroll('work-v2-a', 'front', { scrollTop: 42, scrollLeft: 2 });
  ui.setDraft('work-v2-a', 'back', 'draft-a');

  // Mount with a second work to test isolation
  const viewB = makeView('work-v2-b', { state: 'failed' });
  useWorkStore.getState().applySnapshot(viewB);
  ui.ensureCard('work-v2-b');
  ui.setScroll('work-v2-b', 'back', { scrollTop: 7, scrollLeft: 0 });
  ui.setDraft('work-v2-b', 'back', 'draft-b');

  const mountedA = await mount(<WorkCard workID="work-v2-a" port={new TestPort()} />);
  const frontA = mountedA.host.querySelector<HTMLElement>('[data-testid="work-face-front"]')!;
  eq(frontA.scrollTop, 42, 'work A front scroll restored');
  await mountedA.cleanup();

  const mountedB = await mount(
    <WorkCard workID="work-v2-b" port={new TestPort()}
      backSlots={{ transcript: ({ draft }) => <span data-testid="draft-display">{draft}</span> }}
    />,
  );
  const backB = mountedB.host.querySelector<HTMLElement>('[data-testid="work-face-back"]')!;
  eq(backB.scrollTop, 7, 'work B back scroll restored');
  eq(mountedB.host.querySelector('[data-testid="draft-display"]')?.textContent, 'draft-b', 'work B draft independent of work A');
  await mountedB.cleanup();
}

async function testV2ApplyFailurePreservesDraft(): Promise<void> {
  reset();
  const workID = 'work-v2-fail';
  const view = makeView(workID, { state: 'ready' });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...makeV2Definition(workID), status: 'draft' } },
  }));
  const port = new TestPort();
  port.applyFailures = 1;
  const mounted = await mount(
    <WorkCard
      workID={workID}
      port={port}
      backSlots={{
        transcript: ({ draft }) => <span data-testid="draft-value">{draft}</span>,
      }}
    />,
  );

  // Set draft on back
  await act(async () => {
    useWorkUIStore.getState().setDraft(workID, 'back', 'planning notes');
    await Promise.resolve();
  });
  await settle();

  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'on back face');
  eq(mounted.host.querySelector('[data-testid="draft-value"]')?.textContent, 'planning notes', 'draft visible on back');

  // Click Apply — should fail, stay on back, keep draft
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')!.click());
  await settle(50);
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'failed apply stays on back face');
  eq(useWorkUIStore.getState().cardByWork[workID].faces.back.draft, 'planning notes', 'draft preserved after failed apply');
  eq(mounted.host.querySelector('[data-testid="draft-value"]')?.textContent, 'planning notes', 'draft still visible');
  ok(Boolean(mounted.host.querySelector('[data-testid="work-apply-definition-error"]')), 'failed apply is explicit and retryable');

  await mounted.cleanup();
}

async function testV2ComponentsA11y(): Promise<void> {
  reset();
  const workID = 'work-v2-a11y';
  const view = makeView(workID, { state: 'running' });
  useWorkStore.getState().applySnapshot(view);
  const slot: ArtifactSlot = {
    id: 'slot-1', workId: workID, definitionRev: 1, title: 'Report',
    kind: 'docx', expectedCount: 1, required: true, state: 'reserved',
    artifactRefs: [], revision: 1,
  };
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...makeV2Definition(workID), status: 'active' } },
    artifactSlots: { ...s.artifactSlots, [workID]: [slot] },
    v2Tasks: { ...s.v2Tasks, [workID]: [] },
  }));
  const mounted = await mount(<WorkCard workID={workID} port={new TestPort()} />);

  const shelf = mounted.host.querySelector('[data-testid="result-shelf"]');
  ok(Boolean(shelf), 'ResultShelf renders');
  eq(shelf?.getAttribute('role'), 'region', 'ResultShelf role=region');
  eq(shelf?.getAttribute('aria-label'), '成果架', 'ResultShelf aria-label');
  eq(shelf?.getAttribute('aria-live'), 'polite', 'ResultShelf aria-live=polite');

  const execList = mounted.host.querySelector('[data-testid="execution-list-empty"]');
  ok(Boolean(execList), 'ExecutionList renders empty state');
  eq(execList?.getAttribute('role'), 'status', 'ExecutionList empty role=status');
  eq(execList?.getAttribute('aria-live'), 'polite', 'ExecutionList empty aria-live=polite');

  const flip = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!;
  eq(flip.getAttribute('aria-label'), '查看会话', 'flip button has descriptive aria-label');
  await mounted.cleanup();
}

async function testV2FlipDoesNotInterruptRunning(): Promise<void> {
  reset();
  const workID = 'work-v2-running';
  const view = makeView(workID, { state: 'running' });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...makeV2Definition(workID), status: 'active' } },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);
  eq(useWorkStore.getState().works[workID].work.state, 'running', 'work is running');

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  eq(useWorkStore.getState().works[workID].work.state, 'running', 'running state not changed by flip');

  await mounted.cleanup();
}

async function testV2CSSImportsProduceClasses(): Promise<void> {
  reset();
  const workID = 'work-v2-css';
  const view = makeView(workID, { state: 'ready' });
  useWorkStore.getState().applySnapshot(view);
  const slot: ArtifactSlot = {
    id: 'slot-1', workId: workID, definitionRev: 1, title: 'Report',
    kind: 'docx', expectedCount: 1, required: true, state: 'reserved',
    artifactRefs: [], revision: 1,
  };
  const task: TaskV2View = {
    id: 'task-1', runId: 'run-1', nodeId: 'node-1', title: 'Analyze',
    state: 'pending', retryable: false, updatedAt: '2026-07-24T00:00:00Z',
  };
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...makeV2Definition(workID), status: 'active' } },
    artifactSlots: { ...s.artifactSlots, [workID]: [slot] },
    v2Tasks: { ...s.v2Tasks, [workID]: [task] },
  }));
  const mounted = await mount(<WorkCard workID={workID} port={new TestPort()} />);

  // ResultShelf CSS classes
  ok(Boolean(mounted.host.querySelector('.wg2-rs-shelf')), 'wg2-rs-shelf CSS class present');
  ok(Boolean(mounted.host.querySelector('[data-testid="result-shelf-item-slot-1"]')), 'ResultCard renders for slot');

  // ExecutionList CSS classes
  ok(Boolean(mounted.host.querySelector('.wg2-el-list')), 'wg2-el-list CSS class present');
  ok(Boolean(mounted.host.querySelector('[data-testid="execution-list-item-task-1"]')), 'ExecutionRow renders for task');

  await interact(() => mounted.host.querySelector<HTMLElement>('[data-testid="execution-row-header-task-1"]')!.click());
  ok(Boolean(mounted.host.querySelector('[data-testid="expanded-block-task-1"]')), 'production ExecutionList opens real ExpandedBlock');
  eq(
    useWorkUIStore.getState().cardByWork[workID].faces.front.expanded['v2-task:task-1'],
    true,
    'expanded task is stored in per-work UI state',
  );

  await act(async () => {
    useWorkStore.setState((s) => ({
      v2Tasks: {
        ...s.v2Tasks,
        [workID]: [{ ...task, state: 'running', progress: '33%' }],
      },
    }));
    await Promise.resolve();
  });
  ok(Boolean(mounted.host.querySelector('[data-testid="expanded-block-task-1"]')), 'snapshot replacement preserves ExpandedBlock');

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  ok(Boolean(mounted.host.querySelector('[data-testid="expanded-block-task-1"]')), 'flip round-trip preserves ExpandedBlock');

  const css = readFileSync(resolve(process.cwd(), 'src/styles.css'), 'utf8');
  eq((css.match(/V2 ResultShelf \+ ExecutionList CSS/g) ?? []).length, 1, 'V2 component CSS has one authoritative source');
  ok(css.includes('.wg2-rs-shelf {') && css.includes('.wg2-el-list {'), 'global production CSS contains both V2 surfaces');
  ok(css.includes('@media (max-width: 640px)'), 'V2 CSS includes narrow-screen rules');

  await mounted.cleanup();
}

async function testV2ProductionActionCapabilities(): Promise<void> {
  reset();
  const workID = 'work-v2-actions';
  const view = makeView(workID, { state: 'running' });
  useWorkStore.getState().applySnapshot(view);
  const definition = makeV2Definition(workID);
  definition.nodes[0] = { ...definition.nodes[0], inputSpecIds: ['spec-1'] };
  const slot: ArtifactSlot = {
    id: 'slot-actions',
    workId: workID,
    definitionRev: 1,
    title: 'Actions',
    kind: 'text',
    expectedCount: 1,
    required: true,
    state: 'ready',
    artifactRefs: [{
      id: 'artifact-actions',
      name: 'actions.txt',
      type: 'text/plain',
      path: 'D:\\safe\\actions.txt',
      relativePath: 'outputs/actions.txt',
      status: 'available',
    }],
    revision: 1,
  };
  const task: TaskV2View = {
    id: 'task-actions',
    runId: 'run-actions',
    nodeId: 'node-1',
    title: 'Retry and input',
    state: 'failed_retryable',
    retryable: true,
    waitingInputIds: ['spec-1'],
    updatedAt: '2026-07-24T00:00:00Z',
  };
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: { ...definition, status: 'active' } },
    artifactSlots: { ...s.artifactSlots, [workID]: [slot] },
    v2Tasks: { ...s.v2Tasks, [workID]: [task] },
  }));
  const port = new TestPort();
  port.v2RetryFailures = 1;
  const artifactIntents: string[] = [];
  const mounted = await mount(
    <WorkCard
      workID={workID}
      port={port}
      onArtifactOpen={(intent) => { artifactIntents.push(`open:${intent.path}`); }}
      onArtifactLocate={(intent) => { artifactIntents.push(`locate:${intent.path}`); }}
    />,
  );

  ok(Boolean(mounted.host.querySelector('[data-testid="rc-file-open-artifact-actions"]')), 'artifact open is visible with safe host handler');
  ok(Boolean(mounted.host.querySelector('[data-testid="rc-file-locate-artifact-actions"]')), 'artifact locate is visible with safe host handler');
  ok(!mounted.host.querySelector('[data-testid="rc-file-download-artifact-actions"]'), 'artifact download is hidden without capability');
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="rc-file-open-artifact-actions"]')!.click());
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="rc-file-locate-artifact-actions"]')!.click());
  eq(artifactIntents.join('|'), 'open:outputs/actions.txt|locate:outputs/actions.txt', 'artifact actions emit workspace-scoped typed host intents');

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="execution-row-retry-task-actions"]')!.click());
  await settle(50);
  ok(Boolean(mounted.host.querySelector('[data-testid="execution-row-retry-error-task-actions"]')), 'task retry rejection is explicit');
  const firstRetryID = port.v2RetryInputs[0].requestId;
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="execution-row-retry-task-actions"]')!.click());
  await settle(50);
  eq(port.v2RetryInputs.length, 2, 'task retry reaches typed adapter twice');
  eq(port.v2RetryInputs[1].requestId, firstRetryID, 'task retry reuses requestId after transport rejection');
  ok(!mounted.host.querySelector('[data-testid="execution-row-retry-error-task-actions"]'), 'successful retry clears visible error');

  const revisionBeforeConflict = useWorkStore.getState().works[workID].revision;
  port.v2RetryBeforeResponse.push(() => {
    useWorkStore.setState((state) => ({
      works: {
        ...state.works,
        [workID]: { ...state.works[workID], revision: revisionBeforeConflict + 5 },
      },
    }));
  });
  port.v2RetryNext.push({
    revision: revisionBeforeConflict + 5,
    duplicate: false,
    committed: false,
    recoverable: true,
    error: {
      code: 'revision_conflict',
      message: 'projection advanced',
      revision: revisionBeforeConflict + 5,
      committed: false,
      recoverable: true,
    },
  });
  const snapshotsBeforeConflict = port.operations.filter((operation) => operation === `snapshot:${workID}`).length;
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="execution-row-retry-task-actions"]')!.click());
  await settle(50);
  const conflictRequest = port.v2RetryInputs[2];
  eq(
    port.operations.filter((operation) => operation === `snapshot:${workID}`).length,
    snapshotsBeforeConflict + 1,
    'revision conflict refreshes the authoritative projection',
  );
  ok(Boolean(mounted.host.querySelector('[data-testid="execution-row-retry-error-task-actions"]')), 'revision conflict remains explicit');
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="execution-row-retry-task-actions"]')!.click());
  await settle(50);
  ok(port.v2RetryInputs[3].requestId !== conflictRequest.requestId, 'post-conflict retry derives a fresh requestId');
  eq(port.v2RetryInputs[3].expectedRevision, revisionBeforeConflict + 5, 'post-conflict retry uses refreshed Work revision');

  await interact(() => mounted.host.querySelector<HTMLElement>('[data-testid="execution-row-header-task-actions"]')!.click());
  // V2 typed input unavailable message: shown when port lacks typed callbacks
  ok(Boolean(mounted.host.querySelector('[data-testid="expanded-block-input-unavailable-task-actions"]')), 'typed input unavailable message is shown without port callbacks');
  // Old inline input controls are replaced by WorkInputHost; no disabled <input> exists
  const oldInput = mounted.host.querySelector<HTMLInputElement>('[data-testid="expanded-block-control-task-actions-spec-1"]');
  eq(oldInput, null, 'old inline input control is removed — replaced by WorkInputHost');
  // No submit button since typed callbacks are absent
  ok(!mounted.host.querySelector('[data-testid="expanded-block-submit-task-actions-spec-1"]'), 'input submit action is hidden without handler');

  await mounted.cleanup();

  const appSource = readFileSync(resolve(process.cwd(), 'src/App.tsx'), 'utf8');
  ok(
    appSource.includes('onArtifactOpen={(intent) => app.OpenWorkspacePathForTab(ownerTabID, intent.path)}'),
    'production artifact open reaches the authorized system-open host boundary',
  );
  ok(
    appSource.includes('onArtifactLocate={(intent) => app.RevealWorkspacePathForTab(ownerTabID, intent.path)}'),
    'production artifact locate reaches the authorized file-manager host boundary',
  );
  ok(
    !appSource.includes('onArtifactOpen={(intent) => app.RevealPath(intent.path)}'),
    'production artifact open cannot silently degrade into locate',
  );
}

async function testV2ArtifactPreviewConversionPort(): Promise<void> {
  reset();
  const workID = 'work-v2-preview-convert';
  const view = makeView(workID, { schemaVersion: 2, state: 'running' });
  useWorkStore.getState().applySnapshot(view);
  const definition = { ...makeV2Definition(workID), status: 'active' as const };
  const slot: ArtifactSlot = {
    id: 'slot-convert',
    workId: workID,
    definitionRev: definition.revision,
    title: 'Report',
    kind: 'docx',
    expectedCount: 1,
    required: true,
    state: 'ready',
    artifactRefs: [{
      id: 'artifact-convert',
      name: 'report.docx',
      type: 'docx',
      status: 'available',
      relativePath: 'outputs/report.docx',
    }],
    revision: 3,
  };
  useWorkStore.setState((state) => ({
    v2Definitions: { ...state.v2Definitions, [workID]: definition },
    v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [workID]: definition },
    artifactSlots: { ...state.artifactSlots, [workID]: [slot] },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="rc-file-preview-artifact-convert"]')!.click());
  eq(port.artifactPreviewInputs.length, 1, 'preview port: WorkCard calls typed preview port');
  eq(port.artifactPreviewInputs[0]?.definitionRevision, definition.revision, 'preview port: definition revision preserved');
  eq(port.artifactPreviewInputs[0]?.slotRevision, 3, 'preview port: slot revision preserved');
  ok(
    (mounted.host.querySelector('[data-testid="rc-filecard-preview"]')?.textContent ?? '').includes('Office file'),
    'preview port: production adapter returns ArtifactPreview content directly',
  );
  ok(Boolean(mounted.host.querySelector('[data-testid="rc-file-convert-artifact-convert"]')), 'preview port: conversion action mounted from preview capability');

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="rc-file-convert-artifact-convert"]')!.click());
  await settle(50);
  eq(port.artifactConversionInputs.length, 1, 'conversion port: WorkCard calls typed conversion port');
  const input = port.artifactConversionInputs[0]!;
  eq(input.workId, workID, 'conversion port: work identity preserved');
  eq(input.slotId, slot.id, 'conversion port: slot identity preserved');
  eq(input.slotRevision, slot.revision, 'conversion port: slot revision preserved');
  eq(input.artifactId, 'artifact-convert', 'conversion port: artifact identity preserved');
  eq(input.allowExternal, false, 'conversion port: local UI cannot self-authorize external conversion');
  eq(input.approvalToken, '', 'conversion port: no invented approval token');
  ok(input.requestId.includes(`${workID}:${definition.revision}:${slot.id}:${slot.revision}:artifact-convert`), 'conversion port: stable requestID binds full identity');
  ok(
    (mounted.host.querySelector('[data-testid="rc-preview-text"]')?.textContent ?? '').includes('converted through port'),
    'conversion port: authoritative converted preview rendered',
  );
  await mounted.cleanup();
}

async function testV2ArtifactRetryRevisionIdentity(): Promise<void> {
  reset();
  const workID = 'work-v2-artifact-revision';
  const view = makeView(workID, { schemaVersion: 2, state: 'running' });
  useWorkStore.getState().applySnapshot(view);
  const rev2 = {
    ...makeV2Definition(workID),
    revision: 2,
    status: 'active' as const,
  };
  const rev3 = {
    ...rev2,
    revision: 3,
    parentRevision: 2,
    digest: 'sha256:rev3',
  };
  const slotAt = (definitionRev: number): ArtifactSlot => ({
    id: 'same-retry-slot',
    workId: workID,
    definitionRev,
    title: `Retry rev ${definitionRev}`,
    kind: 'text',
    expectedCount: 1,
    required: true,
    state: 'failed',
    artifactRefs: [],
    error: {
      code: 'retryable',
      message: `rev ${definitionRev} failed`,
      retryable: true,
    },
    revision: definitionRev,
  });
  useWorkStore.setState((state) => ({
    v2Definitions: { ...state.v2Definitions, [workID]: rev2 },
    v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [workID]: rev2 },
    artifactSlots: {
      ...state.artifactSlots,
      [workID]: [slotAt(1), slotAt(2)],
    },
  }));

  let resolveOld!: (result: RetryArtifactSlotResult) => void;
  const oldPending = new Promise<RetryArtifactSlotResult>((resolveResult) => {
    resolveOld = resolveResult;
  });
  const port = new TestPort();
  port.artifactRetryResults.push(oldPending);
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  eq(
    mounted.host.querySelectorAll('[data-testid="result-shelf-item-same-retry-slot"]').length,
    1,
    'artifact revision: same slotID renders only active revision',
  );
  ok(
    (mounted.host.querySelector('[data-testid="result-card-same-retry-slot"]')?.textContent ?? '').includes('Retry rev 2'),
    'artifact revision: historical rev1 content is not visible',
  );
  await interact(() => mounted.host.querySelector<HTMLButtonElement>(
    '[data-testid="result-card-retry-same-retry-slot"]',
  )!.click());
  eq(port.artifactRetryInputs.length, 1, 'artifact revision: visible rev2 retry dispatched once');
  eq(port.artifactRetryInputs[0]?.definitionRevision, 2, 'artifact revision: DTO freezes clicked rev2');

  await act(async () => {
    useWorkStore.setState((state) => ({
      v2Definitions: { ...state.v2Definitions, [workID]: rev3 },
      v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [workID]: rev3 },
      artifactSlots: {
        ...state.artifactSlots,
        [workID]: [slotAt(2), slotAt(3)],
      },
    }));
    await Promise.resolve();
  });
  ok(
    (mounted.host.querySelector('[data-testid="result-card-retry-same-retry-slot"]')?.textContent ?? '').includes('重试'),
    'artifact revision: active rev3 replaces rev2 card',
  );

  resolveOld({
    revision: view.revision + 1,
    duplicate: false,
    committed: true,
    recoverable: false,
  });
  await settle(50);
  eq(port.artifactRetryInputs.length, 1, 'artifact revision: old continuation cannot dispatch a second current intent');
  eq(port.artifactRetryInputs[0]?.definitionRevision, 2, 'artifact revision: old continuation remains rev2');

  await interact(() => mounted.host.querySelector<HTMLButtonElement>(
    '[data-testid="result-card-retry-same-retry-slot"]',
  )!.click());
  eq(port.artifactRetryInputs.length, 2, 'artifact revision: explicit rev3 click dispatches separately');
  eq(port.artifactRetryInputs[1]?.definitionRevision, 3, 'artifact revision: new DTO carries active rev3');
  ok(
    port.artifactRetryInputs[1]?.requestId !== port.artifactRetryInputs[0]?.requestId,
    'artifact revision: cross-revision retries cannot share requestId',
  );
  await mounted.cleanup();
}

async function testV2DefaultWailsProductionMount(): Promise<void> {
  reset();
  window.localStorage.clear();
  const workID = 'work-v2-wails-production';
  const runID = 'run-production';
  const sessionID = 'session-production';
  const taskID = 'task-production';
  const definition = makeV2Definition(workID);
  definition.status = 'active';
  definition.nodes[0] = {
    ...definition.nodes[0],
    id: 'node-1',
    blockIds: ['b1'],
    inputSpecIds: ['spec-1'],
  };
  definition.inputSpecs = [{
    id: 'spec-1',
    label: 'Production input',
    kind: 'file',
    required: true,
    pinEligible: true,
  }];
  const workInput: WorkInput = {
    id: 'input-production',
    workId: workID,
    runId: runID,
    taskId: taskID,
    blockId: 'b1',
    specId: 'spec-1',
    value: null,
    state: 'requested',
    revision: 1,
    updatedAt: '2026-07-24T00:00:00Z',
  };
  const task: TaskV2View = {
    id: taskID,
    runId: runID,
    nodeId: 'node-1',
    title: 'Production task',
    state: 'waiting_input',
    waitingInputIds: ['spec-1'],
    retryable: false,
    updatedAt: '2026-07-24T00:00:00Z',
  };
  const retrySlot: ArtifactSlot = {
    id: 'slot-production',
    workId: workID,
    definitionRev: definition.revision,
    title: 'Production artifact',
    kind: 'text',
    expectedCount: 1,
    required: true,
    state: 'stale',
    artifactRefs: [],
    revision: 1,
  };
  const productionView: WorkView = {
    ...makeView(workID, {
      schemaVersion: 2,
      state: 'waiting_user',
      prompt: 'production goal',
      blocks: [makeBlock('b1')],
      runs: [{
        id: runID,
        workId: workID,
        definitionDigest: definition.digest,
        state: 'waiting_user',
        stages: [],
        startedAt: '2026-07-24T00:00:00Z',
      }],
      sessionRefs: [{
        sessionPath: sessionID,
        branchId: 'main',
        modelRef: 'test',
        turnCount: 1,
        preview: '',
        startedAt: '2026-07-24T00:00:00Z',
      }],
    }),
    schemaVersion: 2,
    definition,
    tasks: [task],
    inputs: [workInput],
    artifactSlots: [retrySlot],
    patchPreviews: [],
  };
  const calls: string[] = [];
  let wailsArtifactRetryInput: RetryArtifactSlotRequest | undefined;
  let snapshots = 0;
  const preview: WorkPatchPreview = {
    id: 'patch-production',
    workId: workID,
    runId: runID,
    taskId: taskID,
    blockId: 'b1',
    sessionId: sessionID,
    baseDefinitionRev: definition.revision,
    baseBlockRev: workInput.revision,
    scope: 'block',
    operations: [{ op: 'replace', path: 'blocks/b1/title', newValue: 'Updated' }],
    affectedNodeIds: ['node-1'],
    affectedBlockIds: ['b1'],
    affectedArtifactSlotIds: [],
    staleArtifactSlotIds: [],
    invalidatedTaskIds: [],
    requiresRerun: false,
    digest: 'preview-digest-production',
    expiresAt: '2099-07-24T00:00:00Z',
  };
  const app = {
    WatchWork: async () => { calls.push('WatchWork'); },
    UnwatchWork: async () => { calls.push('UnwatchWork'); },
    GetWork: async () => {
      snapshots++;
      return structuredClone(productionView);
    },
    RecoverWorkView: async () => { throw new Error('unexpected recovery'); },
    SubmitWorkInput: async (_tabID: string, input: SubmitWorkInputRequest) => {
      calls.push('SubmitWorkInput');
      return {
        input: { ...workInput, value: input.value, state: 'submitted', revision: 2 },
        revision: 2,
        duplicate: false,
        committed: true,
        recoverable: false,
      };
    },
    SetInputCornerstone: async (_tabID: string, input: SetInputCornerstoneRequest) => {
      calls.push('SetInputCornerstone');
      return {
        cornerstoneID: input.pin ? 'cornerstone-production' : undefined,
        pinned: input.pin,
        revision: 3,
        duplicate: false,
        committed: true,
        recoverable: false,
      };
    },
    PreviewWorkPatch: async (_tabID: string, _input: PreviewWorkPatchRequest) => {
      calls.push('PreviewWorkPatch');
      return {
        preview: preview,
        revision: 4,
        duplicate: false,
        committed: true,
        recoverable: false,
      };
    },
    ApplyWorkPatch: async (_tabID: string, _input: ApplyWorkPatchRequest) => {
      calls.push('ApplyWorkPatch');
      return {
        workRevision: 5,
        newRevision: 2,
        invalidatedTaskIDs: [],
        affectedBlockIDs: ['b1'],
        requiresRerun: false,
        duplicate: false,
        committed: true,
        recoverable: false,
      };
    },
    RetryArtifactSlot: async (_tabID: string, input: RetryArtifactSlotRequest) => {
      calls.push('RetryArtifactSlot');
      wailsArtifactRetryInput = input;
      return {
        slot: { ...retrySlot, state: 'generating', revision: 2 },
        revision: 6,
        duplicate: false,
        committed: true,
        recoverable: false,
      };
    },
    SelectWorkInputFile: async () => {
      calls.push('SelectWorkInputFile');
      return {
        artifactRef: {
          id: 'selected-production',
          name: 'selected.txt',
          type: 'text/plain',
          status: 'available',
          relativePath: 'inputs/selected.txt',
        },
        canceled: false,
      };
    },
    CreateCandidateRevision: async (_tabID: string, input: CreateCandidateRevisionInput) => {
      calls.push('CreateCandidateRevision');
      return {
        candidate: {
          ...definition,
          revision: definition.revision + 1,
          parentRevision: input.baseDefinitionRevision,
          status: 'draft',
          goal: input.intent,
          nodes: [...definition.nodes, {
            id: 'node-production-planned',
            title: 'Backend planned node',
            description: input.intent,
          }],
          digest: 'candidate-production',
        },
        impact: {
          keptNodeIds: definition.nodes.map((node) => node.id),
          invalidatedNodeIds: [],
          newNodeIds: ['node-production-planned'],
          removedNodeIds: [],
          requiresRerun: true,
        },
        revision: 7,
        duplicate: false,
        committed: true,
        recoverable: false,
      };
    },
  };
  (window as unknown as { go: unknown }).go = { main: { App: app } };
  (window as unknown as { runtime: unknown }).runtime = {
    EventsOn: () => () => undefined,
  };

  const inputKey = [workID, runID, taskID, 'b1', workInput.id, 'spec-1', definition.revision, workInput.revision].join('\u0000');
  const discussionKey = [
    workID,
    runID,
    sessionID,
    taskID,
    'b1',
    definition.revision,
    workInput.revision,
  ].join('\u0000');
  useWorkUIStore.getState().ensureCard(workID);
  useWorkUIStore.getState().setInputDirtyFlag(workID, inputKey);
  useWorkUIStore.getState().setInputDraft(workID, inputKey, 'production value');
  useWorkUIStore.getState().setDiscussionDraft(workID, discussionKey, 'update production block');
  useWorkStore.setState((state) => ({
    works: { ...state.works, [workID]: productionView },
    revisions: { ...state.revisions, [workID]: productionView.revision },
    v2Definitions: { ...state.v2Definitions, [workID]: definition },
    v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [workID]: definition },
    v2Tasks: { ...state.v2Tasks, [workID]: [task] },
    v2Inputs: { ...state.v2Inputs, [workID]: [workInput] },
    artifactSlots: { ...state.artifactSlots, [workID]: [retrySlot] },
  }));

  const mounted = await mount(<WorkCard workID={workID} tabID="tab-production" />);
  await settle(80);
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="result-card-retry-slot-production"]')!.click());
  await interact(() => mounted.host.querySelector<HTMLElement>(`[data-testid="execution-row-header-${taskID}"]`)!.click());

  ok(Boolean(mounted.host.querySelector(`[data-testid="work-input-host-${taskID}-spec-1"]`)), 'default Wails mount enables typed input');
  await interact(() => mounted.host.querySelector<HTMLButtonElement>(`[data-testid="work-input-control-${taskID}-spec-1-select"]`)!.click());
  await interact(() => mounted.host.querySelector<HTMLButtonElement>(`[data-testid="work-input-submit-${taskID}-spec-1"]`)!.click());
  await interact(() => mounted.host.querySelector<HTMLButtonElement>(`[data-testid="work-input-pin-${taskID}-spec-1"]`)!.click());
  await interact(() => mounted.host.querySelector<HTMLButtonElement>(`[data-testid="expanded-block-discuss-${taskID}"]`)!.click());
  eq(mounted.host.querySelectorAll('[data-testid^="discussion-drawer-"]').length, 1, 'production mounts one discussion drawer');
  await interact(() => mounted.host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${taskID}"]`)!.click());
  await settle(50);
  await interact(() => mounted.host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${taskID}"]`)!.click());
  await settle(50);
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-adjust-structure"]')!.click());
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!.click());
  await settle(50);

  for (const operation of [
    'RetryArtifactSlot',
    'SelectWorkInputFile',
    'CreateCandidateRevision',
    'SubmitWorkInput',
    'SetInputCornerstone',
    'PreviewWorkPatch',
    'ApplyWorkPatch',
  ]) {
    ok(calls.includes(operation), `default Wails production reaches ${operation}`);
  }
  eq(
    wailsArtifactRetryInput?.definitionRevision,
    definition.revision,
    'default Wails preserves artifact retry definitionRevision',
  );
  ok(snapshots >= 2, `committed mutation refreshes authoritative snapshot (fetches=${snapshots})`);

  await mounted.cleanup();
  delete (window as unknown as { go?: unknown }).go;
  delete (window as unknown as { runtime?: unknown }).runtime;
}

async function testV2ExplicitSessionIdReachesPreviewPatch(): Promise<void> {
  reset();
  const workID = 'work-v2-explicit-sid';
  const runID = 'run-explicit';
  const taskID = 'task-explicit';
  const explicitSessionID = 'explicit-session-abc';

  // Build a work view WITHOUT sessionRefs so latestWorkSessionID returns ''.
  // Use schemaVersion 1 to avoid parseWorkViewV2 validation; V2 data is set
  // through store slices below.
  const view: WorkView = makeView(workID, {
    state: 'waiting_user',
    prompt: 'explicit session goal',
    blocks: [makeBlock('b1')],
    runs: [{
      id: runID,
      workId: workID,
      definitionDigest: 'digest',
      state: 'waiting',
      stages: [],
      startedAt: '2026-07-25T00:00:00Z',
    }],
  });
  useWorkStore.getState().applySnapshot(view);

  const definition: WorkDefinitionRevision = {
    workId: workID,
    revision: 1,
    parentRevision: 0,
    status: 'active',
    goal: 'explicit session goal',
    nodes: [{
      id: 'node-1',
      title: 'Task 1',
      description: '',
      blockIds: ['b1'],
    }],
    artifactSlots: [],
    inputSpecs: [],
    createdBy: 'ai',
    createdAt: '2026-07-25T00:00:00Z',
    digest: 'digest-explicit',
  };
  const workInput: WorkInput = {
    id: 'wi-explicit',
    workId: workID,
    runId: runID,
    taskId: taskID,
    blockId: 'b1',
    specId: 'spec-explicit',
    value: null,
    state: 'requested',
    revision: 1,
    updatedAt: '2026-07-25T00:00:00Z',
  };
  const task: TaskV2View = {
    id: taskID,
    runId: runID,
    nodeId: 'node-1',
    title: 'Task explicit',
    state: 'waiting_input',
    waitingInputIds: ['spec-explicit'],
    retryable: false,
    updatedAt: '2026-07-25T00:00:00Z',
  };
  useWorkStore.setState((state) => ({
    v2Definitions: { ...state.v2Definitions, [workID]: definition },
    v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [workID]: definition },
    v2Tasks: { ...state.v2Tasks, [workID]: [task] },
    v2Inputs: { ...state.v2Inputs, [workID]: [workInput] },
  }));

  // Capture the PreviewWorkPatch call to inspect sessionId.
  let capturedInput: PreviewWorkPatchRequest | null = null;
  const preview: WorkPatchPreview = {
    id: 'patch-explicit',
    workId: workID,
    runId: runID,
    taskId: taskID,
    blockId: 'b1',
    sessionId: explicitSessionID,
    baseDefinitionRev: definition.revision,
    baseBlockRev: workInput.revision,
    scope: 'block',
    operations: [],
    affectedNodeIds: ['node-1'],
    affectedBlockIds: ['b1'],
    affectedArtifactSlotIds: [],
    staleArtifactSlotIds: [],
    invalidatedTaskIds: [],
    requiresRerun: false,
    digest: 'digest-explicit-preview',
    expiresAt: '2099-07-25T00:00:00Z',
  };

  const savedGo = (window as unknown as { go?: unknown }).go;
  const savedRuntime = (window as unknown as { runtime?: unknown }).runtime;
  (window as unknown as { go: unknown }).go = { main: { App: {
    GetWork: async () => structuredClone(view),
    WatchWork: async () => {},
    UnwatchWork: async () => {},
    PreviewWorkPatch: async (_tabID: string, input: PreviewWorkPatchRequest) => {
      capturedInput = input;
      return {
        preview,
        revision: 10,
        duplicate: false,
        committed: true,
        recoverable: false,
      };
    },
    ApplyWorkPatch: async () => ({
      workRevision: 20,
      newRevision: 2,
      invalidatedTaskIDs: [],
      requiresRerun: false,
      duplicate: false,
      committed: true,
      recoverable: false,
    }),
  } } };
  (window as unknown as { runtime: unknown }).runtime = {
    EventsOn: () => () => {},
  };

  // Mount with explicit sessionId.
  const mounted = await mount(<WorkCard workID={workID} tabID="tab-explicit" sessionId={explicitSessionID} />);
  await settle(80);

  // Open discussion for the task's block.
  const execRow = mounted.host.querySelector<HTMLElement>(`[data-testid="execution-row-header-${taskID}"]`);
  ok(Boolean(execRow), 'explicit session: execution row header exists');
  await interact(() => mounted.host.querySelector<HTMLElement>(`[data-testid="execution-row-header-${taskID}"]`)!.click());
  await settle(50);
  const discussBtn = mounted.host.querySelector<HTMLButtonElement>(`[data-testid="expanded-block-discuss-${taskID}"]`);
  ok(Boolean(discussBtn), 'explicit session: discuss button is present');
  await interact(() => discussBtn!.click());
  await settle(50);

  // Type instruction and click preview.
  const textarea = mounted.host.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${taskID}"]`);
  ok(Boolean(textarea), 'explicit session: discussion textarea is present');
  await interact(() => {
    textarea!.value = 'make it better';
    textarea!.dispatchEvent(new Event('input', { bubbles: true }));
  });
  const previewBtn = mounted.host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${taskID}"]`);
  ok(Boolean(previewBtn), 'explicit session: preview button is present');
  ok(!previewBtn!.disabled, 'explicit session: preview button is enabled');
  await interact(() => previewBtn!.click());
  await settle(50);

  // Verify the captured PreviewWorkPatch input.
  ok(capturedInput !== null, 'explicit session: PreviewWorkPatch was called');
  eq(capturedInput?.sessionId, explicitSessionID, 'explicit session: sessionId is the explicit value, not empty');
  eq(capturedInput?.workId, workID, 'explicit session: workId is preserved');
  eq(capturedInput?.runId, runID, 'explicit session: runId is preserved');
  eq(capturedInput?.taskId, taskID, 'explicit session: taskId is preserved');
  eq(capturedInput?.blockId, 'b1', 'explicit session: blockId is preserved');

  await mounted.cleanup();
  (window as unknown as { go?: unknown }).go = savedGo;
  (window as unknown as { runtime?: unknown }).runtime = savedRuntime;
  reset();
}

async function testV2CandidateDiffAndLocalCancel(): Promise<void> {
  reset();
  const workID = 'work-v2-candidate-cancel';
  const view = makeView(workID, { schemaVersion: 2, state: 'running', prompt: '原始目标' });
  useWorkStore.getState().applySnapshot(view);
  const active = { ...makeV2Definition(workID), status: 'active' as const };
  useWorkStore.setState((state) => ({
    v2Definitions: { ...state.v2Definitions, [workID]: active },
    v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [workID]: active },
  }));
  useWorkUIStore.getState().ensureCard(workID);
  useWorkUIStore.getState().setDraft(workID, 'back', '调整后的交付目标');
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-adjust-structure"]')!.click());
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!.click());
  await settle(30);

  eq(port.candidateInputs.length, 1, 'candidate: active Definition reaches typed CreateCandidateRevision port');
  eq(port.candidateInputs[0]?.intent, '调整后的交付目标', 'candidate: UI sends only natural-language intent');
  eq(port.candidateInputs[0]?.baseDefinitionRevision, active.revision, 'candidate: authoritative base revision is frozen');
  ok(Boolean(mounted.host.querySelector('[data-testid="definition-diff"]')), 'candidate: real definition diff is shown before apply');
  ok(
    (mounted.host.querySelector('[data-testid="definition-diff-nodes-added"]')?.textContent ?? '').includes('node-planned'),
    'candidate: backend-planned structural node is shown',
  );
  ok(Boolean(mounted.host.querySelector('[data-testid="definition-diff-impact"]')), 'candidate: backend run impact is shown before apply');

  const writesBeforeCancel = port.operations.length;
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="definition-diff-cancel"]')!.click());
  ok(!mounted.host.querySelector('[data-testid="definition-diff"]'), 'candidate cancel: candidate view closes locally');
  ok(Boolean(mounted.host.querySelector('[data-testid="work-create-candidate"]')), 'candidate cancel: active Definition remains available for regeneration');
  eq(port.applyInputs.length, 0, 'candidate cancel: ApplyDefinition is never called');
  eq(port.operations.length, writesBeforeCancel, 'candidate cancel: no Wails/controller write is emitted');

  await mounted.cleanup();
}

async function testV2DefinitionDiffAcceptsLegacyNullImpactLists(): Promise<void> {
  const active = { ...makeV2Definition('work-v2-null-impact'), status: 'active' as const };
  const candidate = {
    ...active,
    revision: active.revision + 1,
    parentRevision: active.revision,
    status: 'draft' as const,
    goal: '更新后的目标',
  };
  const legacyImpact = {
    keptNodeIds: null,
    invalidatedNodeIds: null,
    newNodeIds: null,
    removedNodeIds: null,
    requiresRerun: false,
  } as unknown as RunImpact;

  const mounted = await mount(
    <DefinitionDiff
      active={active}
      candidate={candidate}
      impact={legacyImpact}
      onApply={() => undefined}
      onCancel={() => undefined}
    />,
  );

  ok(Boolean(mounted.host.querySelector('[data-testid="definition-diff"]')), 'candidate: legacy null impact lists do not crash DefinitionDiff');
  ok(Boolean(mounted.host.querySelector('[data-testid="definition-diff-impact"]')), 'candidate: legacy impact remains observable');
  await mounted.cleanup();
}

async function testV2CandidatePlannerRecovery(): Promise<void> {
  // Planner capability absence is explicit; no client-side clone fallback.
  reset();
  const unavailableWorkID = 'work-v2-planner-unavailable';
  const unavailableView = makeView(unavailableWorkID, { schemaVersion: 2, state: 'running', prompt: '调整结构' });
  const unavailableActive = { ...makeV2Definition(unavailableWorkID), status: 'active' as const };
  useWorkStore.getState().applySnapshot(unavailableView);
  useWorkStore.setState((state) => ({
    v2Definitions: { ...state.v2Definitions, [unavailableWorkID]: unavailableActive },
    v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [unavailableWorkID]: unavailableActive },
  }));
  const unavailablePort = new TestPort();
  Object.defineProperty(unavailablePort, 'createCandidateRevision', { value: undefined });
  const unavailableMount = await mount(<WorkCard workID={unavailableWorkID} port={unavailablePort} />);
  await interact(() => unavailableMount.host.querySelector<HTMLButtonElement>('[data-testid="work-adjust-structure"]')!.click());
  ok(
    Boolean(unavailableMount.host.querySelector('[data-testid="work-create-candidate-unavailable"]')),
    'candidate unavailable: explicit planner capability status',
  );
  ok(
    !unavailableMount.host.querySelector('[data-testid="definition-diff"]'),
    'candidate unavailable: UI does not clone active Definition',
  );
  await unavailableMount.cleanup();

  // A transient planner failure is retryable with the same idempotency key.
  reset();
  const retryWorkID = 'work-v2-planner-retry';
  const retryView = makeView(retryWorkID, { schemaVersion: 2, state: 'running', prompt: '增加校验节点' });
  const retryActive = { ...makeV2Definition(retryWorkID), status: 'active' as const };
  useWorkStore.getState().applySnapshot(retryView);
  useWorkStore.setState((state) => ({
    v2Definitions: { ...state.v2Definitions, [retryWorkID]: retryActive },
    v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [retryWorkID]: retryActive },
  }));
  useWorkUIStore.getState().ensureCard(retryWorkID);
  useWorkUIStore.getState().setDraft(retryWorkID, 'back', '增加校验节点');
  const retryPort = new TestPort();
  retryPort.candidateErrors.push(new Error('planner unavailable'));
  const retryMount = await mount(<WorkCard workID={retryWorkID} port={retryPort} />);
  await interact(() => retryMount.host.querySelector<HTMLButtonElement>('[data-testid="work-adjust-structure"]')!.click());
  const retryButton = retryMount.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!;
  await interact(() => retryButton.click());
  ok(
    (retryMount.host.querySelector('[data-testid="work-create-candidate-error"]')?.textContent ?? '').includes('planner unavailable'),
    'candidate failure: planner error is explicit',
  );
  const failedRequestID = retryPort.candidateInputs[0]?.requestId;
  await interact(() => retryButton.click());
  eq(retryPort.candidateInputs[1]?.requestId, failedRequestID, 'candidate failure: retry reuses requestId');
  ok(Boolean(retryMount.host.querySelector('[data-testid="definition-diff-nodes-added"]')), 'candidate failure: retry shows backend structural change');
  await retryMount.cleanup();

  // A revision conflict invalidates the old intent key; regeneration gets a
  // fresh request ID and never reuses a locally synthesized candidate.
  reset();
  const conflictWorkID = 'work-v2-planner-conflict';
  const conflictView = makeView(conflictWorkID, { schemaVersion: 2, state: 'running', prompt: '重排工作结构' });
  const conflictActive = { ...makeV2Definition(conflictWorkID), status: 'active' as const };
  useWorkStore.getState().applySnapshot(conflictView);
  useWorkStore.setState((state) => ({
    v2Definitions: { ...state.v2Definitions, [conflictWorkID]: conflictActive },
    v2ActiveDefinitions: { ...state.v2ActiveDefinitions, [conflictWorkID]: conflictActive },
  }));
  useWorkUIStore.getState().ensureCard(conflictWorkID);
  useWorkUIStore.getState().setDraft(conflictWorkID, 'back', '重排工作结构');
  const conflictPort = new TestPort();
  const conflictError = Object.assign(new Error('revision conflict'), { code: 'revision_conflict' });
  conflictPort.candidateErrors.push(conflictError);
  const conflictMount = await mount(<WorkCard workID={conflictWorkID} port={conflictPort} />);
  await interact(() => conflictMount.host.querySelector<HTMLButtonElement>('[data-testid="work-adjust-structure"]')!.click());
  const conflictButton = conflictMount.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!;
  await interact(() => conflictButton.click());
  ok(
    (conflictMount.host.querySelector('[data-testid="work-create-candidate-error"]')?.textContent ?? '').includes('重新生成候选结构'),
    'candidate conflict: explicit regeneration guidance',
  );
  const conflictedRequestID = conflictPort.candidateInputs[0]?.requestId;
  await interact(() => conflictButton.click());
  ok(
    conflictPort.candidateInputs[1]?.requestId !== conflictedRequestID,
    'candidate conflict: regeneration uses fresh requestId',
  );
  ok(Boolean(conflictMount.host.querySelector('[data-testid="definition-diff"]')), 'candidate conflict: regeneration returns real candidate');
  await conflictMount.cleanup();
}

// ── V2 draft run gate: store-level production chain ────────────────────────

async function testV2BlankDraftStoreChainNoRun(): Promise<void> {
  reset();
  const workID = 'work-v2-blank-norun';
  const view = makeView(workID, {
    schemaVersion: 2,
    state: 'draft',
    prompt: 'test prompt',
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  // Seed blank draft: no goal, 0 nodes — baseline planning state.
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: makeBlankV2Definition(workID) },
    artifactSlots: { ...s.artifactSlots, [workID]: [] },
    v2Tasks: { ...s.v2Tasks, [workID]: [] },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  // Default face is back for V2 without active definition.
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'V2 blank draft defaults to back');

  // Front face: must show planning CTA, NOT Run button.
  ok(Boolean(mounted.host.querySelector('[data-testid="work-plan-structure"]')), 'blank draft front shows planning CTA');
  ok(!maybeButtonWorkCard(mounted.host, '运行'), 'blank draft front has no Run button');
  ok(!!mounted.host.querySelector('[data-testid="work-v2-draft-hint"]'), 'blank draft shows planning hint');

  // Flip to front and verify Run still absent.
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  await settle(30);
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'front', 'flipped to front');
  ok(Boolean(mounted.host.querySelector('[data-testid="work-plan-structure"]')), 'after flip: planning CTA still on front');
  ok(!maybeButtonWorkCard(mounted.host, '运行'), 'after flip: Run button still absent');

  // Click planning CTA → should flip to back.
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-plan-structure"]')!.click());
  await settle(30);
  eq(useWorkUIStore.getState().cardByWork[workID].activeFace, 'back', 'planning CTA flips to back');

  await mounted.cleanup();
}

async function testV2ActiveDefinitionLateArrivalRestoresRun(): Promise<void> {
  reset();
  const workID = 'work-v2-late-active';
  const view = makeView(workID, {
    schemaVersion: 2,
    state: 'draft',
    prompt: 'test prompt',
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: makeBlankV2Definition(workID) },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  // Initially: planning CTA on front, no Run.
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click());
  await settle(30);
  ok(Boolean(mounted.host.querySelector('[data-testid="work-plan-structure"]')), 'before active: planning CTA visible');
  ok(!maybeButtonWorkCard(mounted.host, '运行'), 'before active: no Run button');

  // Late arrival: active Definition injected into store.
  const activeDef = makeV2Definition(workID);
  await act(async () => {
    useWorkStore.setState((s) => ({
      v2ActiveDefinitions: { ...s.v2ActiveDefinitions, [workID]: activeDef },
    }));
    await Promise.resolve();
  });
  await settle(30);

  // Same component should now show Run, not planning CTA.
  ok(!mounted.host.querySelector('[data-testid="work-plan-structure"]'), 'after active: planning CTA removed');
  ok(Boolean(maybeButtonWorkCard(mounted.host, '运行')), 'after active: Run button appears');

  await mounted.cleanup();
}

async function testV2BlankDraftBackCandidateGeneration(): Promise<void> {
  reset();
  const workID = 'work-v2-candidate-gen';
  const view = makeView(workID, {
    schemaVersion: 2,
    state: 'draft',
    prompt: 'build a report',
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: makeBlankV2Definition(workID) },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  // Back face: blank draft → candidate generation button must be visible.
  // Blank draft: apply button exists but is disabled (no goal/nodes).
  // Candidate generation is the correct path forward.
  const applyBtn = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]');
  ok(Boolean(applyBtn), 'blank draft: apply button rendered');
  ok(applyBtn!.disabled, 'blank draft: apply button disabled (no goal/nodes)');

  // Save prompt first.
  const promptEditor = mounted.host.querySelector<HTMLTextAreaElement>('[data-testid="work-prompt-editor"]')!;
  ok(Boolean(promptEditor), 'prompt editor exists');
  await interact(() => {
    promptEditor.value = 'build a comprehensive report';
    promptEditor.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-save-draft"]')!.click());
  await settle(40);

  // Click candidate generation.
  const genBtn = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!;
  ok(!genBtn.disabled, 'candidate generation button enabled after saving prompt');
  await interact(() => genBtn.click());
  await settle(40);

  eq(port.candidateInputs.length, 1, 'createCandidateRevision called');
  const call = port.candidateInputs[0];
  eq(call.workId, workID, 'candidate call has correct workId');
  eq(call.baseDefinitionRevision, 1, 'blank draft base revision is 1');
  ok(Boolean(call.intent), 'candidate call has intent');
  ok(call.requestId.startsWith('work-candidate-'), 'candidate call has typed requestId');

  // After candidate generation from initial blank draft: there is no active
  // definition to diff against, so DefinitionDiff is hidden. The apply button
  // (line ~327 in WorkCardBack) appears instead, letting the user commit the
  // first active definition directly.
  ok(Boolean(mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')), 'candidate: apply button appears');
  const candidateApplyBtn = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')!;
  ok(!candidateApplyBtn.disabled, 'candidate: apply button enabled (has goal and nodes)');

  await mounted.cleanup();
}

// ── Production Wails chain: blank draft through real window.go.main.App mock → createWailsWorkControllerPort → parser → Store ──
// This test exercises the full production path: raw lowerCamel JSON → parseWorkViewSnapshot →
// parseWorkViewV2 → parseWorkDefinitionRevision → store.applySnapshot.
// NO setState, applySnapshot, or applyEvent calls inject the definition.
// ── Production Wails chain through real mock window.go.main.App → createWailsWorkControllerPort → parser → Store ──
// No setState, applySnapshot, or applyEvent inject the definition — every definition
// enters the Store exclusively through the production lowerCamel → parseWorkViewSnapshot path.
async function testV2BlankDraftProductionWailsChain(): Promise<void> {
  reset();
  const workID = 'work-v2-wails-blank';
  const tabID = 'test-tab-blank';

  // ── Raw lowerCamel blank draft snapshot (string, not typed object) ──
  const blankDraftSnapshot = `{"schemaVersion":2,"revision":1,"work":{"schemaVersion":2,"id":"${workID}","name":"New Work","state":"draft","archiveState":"active","blueprintRef":{"id":"blueprint:collaboration-v2","schemaVersion":2,"version":1},"definitionSnapshot":{"schemaVersion":1,"revision":1,"blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"promptTemplate":"","workflow":{"stages":[]},"blockSpecs":[],"digest":"sha256:abc"},"blocks":[],"placements":[],"prompt":"test prompt","cornerstones":[],"runs":[],"createdWith":{"workSchemaVersion":2,"eventSchemaVersion":2,"rendererSetVersion":1},"createdAt":"2026-07-24T00:00:00Z","updatedAt":"2026-07-24T00:00:00Z"},"definition":{"workId":"${workID}","revision":1,"parentRevision":0,"status":"draft","goal":"","nodes":[],"artifactSlots":[],"inputSpecs":[],"createdBy":"planning","createdAt":"2026-07-24T00:00:00Z","digest":"sha256:blank-draft"},"artifactSlots":[],"tasks":[],"inputs":[]}`;

  // ── 1. GetWork production chain → blank draft enters Store → candidate button ──
  let getWorkCalls = 0;
  const candidateInputs: CreateCandidateRevisionInput[] = [];
  const savedWindow = (globalThis as Record<string, unknown>).window;
  (globalThis as Record<string, unknown>).window = {
    go: { main: { App: {
      GetWork: async () => { getWorkCalls++; return blankDraftSnapshot; },
      UpdateDraft: async () => ({ revision: 1, duplicate: false }),
      CreateCandidateRevision: async (_t: string, input: CreateCandidateRevisionInput) => {
        candidateInputs.push(input);
        return { candidate: { workId: workID, revision: 2, parentRevision: 1, status: 'draft', goal: input.intent, nodes: [{ id: 'n1', title: 'Planned' }], artifactSlots: [], inputSpecs: [], createdBy: 'ai', createdAt: new Date().toISOString(), digest: 'candidate-digest' }, impact: { keptNodeIds: [], invalidatedNodeIds: [], newNodeIds: ['n1'], removedNodeIds: [], requiresRerun: true }, revision: 7, duplicate: false, committed: true, recoverable: false };
      },
      WatchWork: async () => {},
      UnwatchWork: async () => {},
    } } },
    runtime: { EventsOn: () => () => {}, EventsOff: () => {} },
  };

  const mounted = await mount(<WorkCard workID={workID} tabID={tabID} />);
  await settle(100);

  // Blank draft must enter Store via production parser chain.
  const stored = useWorkStore.getState().v2Definitions[workID];
  ok(Boolean(stored), 'wails-chain: blank draft stored via production parser');
  eq(stored!.status, 'draft', 'wails-chain: definition status is draft');
  eq(stored!.goal, '', 'wails-chain: empty goal preserved');
  eq(stored!.nodes.length, 0, 'wails-chain: empty nodes preserved');
  eq(stored!.revision, 1, 'wails-chain: revision=1');
  eq(getWorkCalls, 1, 'wails-chain: GetWork called exactly once');

  // Candidate button visible → click → baseDefinitionRevision=1.
  const genBtn = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]');
  ok(Boolean(genBtn), 'wails-chain: candidate button rendered');
  const promptEditor = mounted.host.querySelector<HTMLTextAreaElement>('[data-testid="work-prompt-editor"]')!;
  await interact(() => { promptEditor.value = 'build report'; promptEditor.dispatchEvent(new Event('input', { bubbles: true })); });
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-save-draft"]')!.click());
  await settle(40);
  ok(!genBtn!.disabled, 'wails-chain: candidate button enabled');
  await interact(() => genBtn!.click());
  await settle(40);
  eq(candidateInputs.length, 1, 'wails-chain: CreateCandidateRevision called');
  eq(candidateInputs[0].baseDefinitionRevision, 1, 'wails-chain: baseDefinitionRevision=1');

  // Cleanup first mount but KEEP Store state (no reset).
  await mounted.cleanup();
  await settle(50);

  // ── 2. Recover: remount same work, GetWork NOT called again, RecoverWorkView hydrates ──
  // RecoverWorkView returns a raw WorkViewEvent (not bare WorkView string).
  const recoveryEvent = { schemaVersion: 2, type: 'snapshot', workID, eventID: 'wv-recover-blank', revision: 1, baseRevision: 0, requestID: 'recover-test', object: { kind: 'work', id: workID }, payload: JSON.parse(blankDraftSnapshot), createdAt: '2026-07-24T00:00:00Z' };
  let recoverCalls = 0;
  const prevGetWorkCalls = getWorkCalls;
  (globalThis as Record<string, unknown>).window = {
    go: { main: { App: {
      GetWork: async () => { getWorkCalls++; return blankDraftSnapshot; },
      RecoverWorkView: async () => { recoverCalls++; return recoveryEvent; },
      UpdateDraft: async () => ({ revision: 1, duplicate: false }),
      CreateCandidateRevision: async () => ({ revision: 0, duplicate: false, committed: false, recoverable: true }),
      WatchWork: async () => {},
      UnwatchWork: async () => {},
    } } },
    runtime: { EventsOn: () => () => {}, EventsOff: () => {} },
  };
  const remounted = await mount(<WorkCard workID={workID} tabID={tabID} />);
  await settle(100);
  const remountDef = useWorkStore.getState().v2Definitions[workID];
  ok(Boolean(remountDef), 'wails-chain remount: definition still in store');
  eq(remountDef!.goal, '', 'wails-chain remount: blank definition consistent');
  eq(recoverCalls, 1, 'wails-chain remount: RecoverWorkView called exactly once');
  eq(getWorkCalls, prevGetWorkCalls, 'wails-chain remount: GetWork NOT called again');
  await remounted.cleanup();
  await settle(50);

  // Restore window and reset for isolated sub-tests below.
  (globalThis as Record<string, unknown>).window = savedWindow;
  reset();

  // ── 3. Watch via WorkControllerAdapter.subscribe → EventsOn callback → real event delivery ──
  const watchWorkID = 'work-v2-watch-snap';
  const watchBlankSnapshot = `{"schemaVersion":2,"revision":1,"work":{"schemaVersion":2,"id":"${watchWorkID}","name":"Watch","state":"draft","archiveState":"active","blueprintRef":{"id":"bp","schemaVersion":2,"version":1},"definitionSnapshot":{"schemaVersion":1,"revision":1,"blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"promptTemplate":"","workflow":{"stages":[]},"blockSpecs":[],"digest":"sha256:abc"},"blocks":[],"placements":[],"prompt":"","cornerstones":[],"runs":[],"createdWith":{"workSchemaVersion":2,"eventSchemaVersion":2,"rendererSetVersion":1},"createdAt":"2026-07-24T00:00:00Z","updatedAt":"2026-07-24T00:00:00Z"},"definition":{"workId":"${watchWorkID}","revision":1,"parentRevision":0,"status":"draft","goal":"watch-goal","nodes":[],"artifactSlots":[],"inputSpecs":[],"createdBy":"x","createdAt":"2026-07-24T00:00:00Z","digest":"sha256:watch"},"artifactSlots":[],"tasks":[],"inputs":[]}`;
  let eventsOnCallback: ((payload: unknown) => void) | null = null;
  let watchSubID = '';
  (globalThis as Record<string, unknown>).window = {
    go: { main: { App: {
      GetWork: async () => watchBlankSnapshot,
      RecoverWorkView: async () => ({ schemaVersion: 2, type: 'snapshot', workID: watchWorkID, eventID: 'hydrate-1', revision: 1, baseRevision: 0, requestID: 'hydrate', object: { kind: 'work', id: watchWorkID }, payload: JSON.parse(watchBlankSnapshot), createdAt: '2026-07-24T00:00:00Z' }),
      WatchWork: async (_t: string, _w: string, subID: string) => { watchSubID = subID; },
      UnwatchWork: async () => {},
      UpdateDraft: async () => ({ revision: 1, duplicate: false }),
      CreateCandidateRevision: async () => ({ revision: 0, duplicate: false, committed: false, recoverable: true }),
    } } },
    runtime: {
      EventsOn: (_name: string, cb: (payload: unknown) => void) => {
        eventsOnCallback = cb;
        return () => { eventsOnCallback = null; };
      },
      EventsOff: () => {},
    },
  };
  const watchPort = createWailsWorkControllerPort('tab-watch')!;
  const watchAdapter = new WorkControllerAdapter(watchPort);
  // First mount path: subscribe triggers GetWork snapshot (no prior projection in Store).
  // The adapter manages the initial GetWork + watch registration internally.
  watchAdapter.subscribe(watchWorkID);
  await settle(200);
  ok(watchSubID !== '', 'wails-chain watch: subscriptionID captured');
  ok(!!eventsOnCallback, 'wails-chain watch: EventsOn callback captured');

  // Deliver snapshot event through captured callback → adapter parses/applies.
  const watchPayload = JSON.parse(watchBlankSnapshot);
  watchPayload.definition.goal = 'snap-updated-goal';
  watchPayload.revision = 2;
  const watchEventRaw = { schemaVersion: 2, type: 'snapshot', workID: watchWorkID, eventID: 'snap-1', revision: 2, baseRevision: 0, requestID: 'req-snap-1', object: { kind: 'work', id: watchWorkID }, payload: watchPayload, createdAt: '2026-07-24T00:00:00Z' };
  eventsOnCallback!(watchEventRaw);
  await settle(200);
  const watchDef = useWorkStore.getState().v2Definitions[watchWorkID];
  ok(Boolean(watchDef), 'wails-chain watch: definition in Store');
  eq(watchDef!.goal, 'snap-updated-goal', 'wails-chain watch: updated goal via adapter chain');

  // Duplicate same eventID → idempotent (adapter seen-event dedup). Re-read from Store.
  eventsOnCallback!(watchEventRaw);
  await settle(150);
  eq(useWorkStore.getState().v2Definitions[watchWorkID]?.goal, 'snap-updated-goal', 'wails-chain watch: duplicate eventID idempotent');

  // Late stale: lower revision → adapter rejects. Re-read from Store.
  const staleRaw = { ...watchEventRaw, eventID: 'snap-stale', revision: 1, baseRevision: 0 };
  eventsOnCallback!(staleRaw);
  await settle(150);
  eq(useWorkStore.getState().v2Definitions[watchWorkID]?.goal, 'snap-updated-goal', 'wails-chain watch: stale revision did not overwrite');

  // ── V2 delta through EventsOn callback: schemaVersion:2, type:'delta', revision:3, baseRevision:2 ──
  // Payload matches production workViewV2Delta format (view_v2_projection.go);
  // extractV2Fields (store.ts:734) handles definition/artifactSlots/tasks/inputs from delta payloads.
  const v2DefCopy = JSON.parse(JSON.stringify(JSON.parse(watchBlankSnapshot).definition));
  v2DefCopy.goal = 'delta-v2-goal';
  v2DefCopy.revision = 2;
  v2DefCopy.parentRevision = 1;
  const v2DeltaPayload = { definition: v2DefCopy };
  const v2DeltaRaw = { schemaVersion: 2, type: 'delta', workID: watchWorkID, eventID: 'delta-v2-1', revision: 3, baseRevision: 2, requestID: 'req-delta-v2-1', object: { kind: 'work', id: watchWorkID }, payload: v2DeltaPayload, createdAt: '2026-07-24T00:00:00Z' };
  eventsOnCallback!(v2DeltaRaw);
  await settle(150);
  eq(useWorkStore.getState().v2Definitions[watchWorkID]?.goal, 'delta-v2-goal', 'wails-chain V2 delta: definition goal updated via callback');

  // Duplicate V2 delta → idempotent. Re-read from Store.
  eventsOnCallback!(v2DeltaRaw);
  await settle(150);
  eq(useWorkStore.getState().v2Definitions[watchWorkID]?.goal, 'delta-v2-goal', 'wails-chain V2 delta: duplicate idempotent');

  // Stale V2 delta (lower revision) → rejected. Re-read from Store.
  const staleV2DeltaRaw = { ...v2DeltaRaw, eventID: 'delta-v2-stale', revision: 2, baseRevision: 1 };
  eventsOnCallback!(staleV2DeltaRaw);
  await settle(150);
  eq(useWorkStore.getState().v2Definitions[watchWorkID]?.goal, 'delta-v2-goal', 'wails-chain V2 delta: stale revision did not overwrite');

  watchAdapter.dispose();
  (globalThis as Record<string, unknown>).window = savedWindow;
  reset();

  // ── 4. Malformed active definition → adapter snapshotError visible ──
  const badWorkID = 'work-v2-wails-malformed';
  const malformedJSON = `{"schemaVersion":2,"revision":1,"work":{"schemaVersion":2,"id":"${badWorkID}","name":"Bad","state":"draft","archiveState":"active","blueprintRef":{"id":"bp","schemaVersion":2,"version":1},"definitionSnapshot":{"schemaVersion":1,"revision":1,"blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"promptTemplate":"","workflow":{"stages":[]},"blockSpecs":[],"digest":"sha256:abc"},"blocks":[],"placements":[],"prompt":"","cornerstones":[],"runs":[],"createdWith":{"workSchemaVersion":2,"eventSchemaVersion":2,"rendererSetVersion":1},"createdAt":"2026-07-24T00:00:00Z","updatedAt":"2026-07-24T00:00:00Z"},"definition":{"workId":"${badWorkID}","revision":1,"parentRevision":0,"status":"active","goal":"","nodes":[],"artifactSlots":[],"inputSpecs":[],"createdBy":"x","createdAt":"2026-07-24T00:00:00Z","digest":"sha256:bad"},"artifactSlots":[],"tasks":[],"inputs":[]}`;
  (globalThis as Record<string, unknown>).window = {
    go: { main: { App: {
      GetWork: async () => malformedJSON,
      WatchWork: async () => {},
      UnwatchWork: async () => {},
      UpdateDraft: async () => ({ revision: 0 }),
      CreateCandidateRevision: async () => ({ revision: 0, duplicate: false, committed: false, recoverable: true }),
    } } },
    runtime: { EventsOn: () => () => {}, EventsOff: () => {} },
  };
  const badAdapter = new WorkControllerAdapter(createWailsWorkControllerPort('tab-malformed')!);
  await badAdapter.recoverSnapshot(badWorkID).catch(() => {});
  await settle(100);
  const badStatus = badAdapter.getStatus(badWorkID);
  ok(badStatus.snapshotError !== null || badStatus.unsupportedView !== null,
    `wails-chain malformed: visible adapter error (snapshotError="${badStatus.snapshotError}", unsupportedView=${!!badStatus.unsupportedView})`);
  badAdapter.dispose();
  (globalThis as Record<string, unknown>).window = savedWindow;
  reset();

  // ── 5. Future schema → adapter unsupportedView ──
  const futureWorkID = 'work-v2-wails-future';
  const futureJSON = `{"schemaVersion":999,"revision":1,"work":{"schemaVersion":1,"id":"${futureWorkID}","name":"Future","state":"draft","archiveState":"active","blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"definitionSnapshot":{"schemaVersion":1,"revision":1,"blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"promptTemplate":"","workflow":{"stages":[]},"blockSpecs":[],"digest":"sha256:abc"},"blocks":[],"placements":[],"prompt":"","cornerstones":[],"runs":[],"createdWith":{"workSchemaVersion":1,"eventSchemaVersion":1,"rendererSetVersion":1},"createdAt":"2026-07-24T00:00:00Z","updatedAt":"2026-07-24T00:00:00Z"}}`;
  (globalThis as Record<string, unknown>).window = {
    go: { main: { App: {
      GetWork: async () => futureJSON,
      WatchWork: async () => {},
      UnwatchWork: async () => {},
      UpdateDraft: async () => ({ revision: 0 }),
      CreateCandidateRevision: async () => ({ revision: 0, duplicate: false, committed: false, recoverable: true }),
    } } },
    runtime: { EventsOn: () => () => {}, EventsOff: () => {} },
  };
  const futureAdapter = new WorkControllerAdapter(createWailsWorkControllerPort('tab-future')!);
  await futureAdapter.recoverSnapshot(futureWorkID).catch(() => {});
  await settle(100);
  const futureStatus = futureAdapter.getStatus(futureWorkID);
  ok(futureStatus.unsupportedView !== null,
    `wails-chain future: unsupportedView set (schemaVersion=${futureStatus.unsupportedView?.schemaVersion})`);
  futureAdapter.dispose();
  (globalThis as Record<string, unknown>).window = savedWindow;
  reset();

  // ── 6. Wrong workId → adapter snapshotError ──
  const wrongWorkID = 'work-v2-wails-wrong-id';
  const wrongIDJSON = `{"schemaVersion":2,"revision":1,"work":{"schemaVersion":2,"id":"work-v2-OTHER-ID","name":"Wrong","state":"draft","archiveState":"active","blueprintRef":{"id":"bp","schemaVersion":2,"version":1},"definitionSnapshot":{"schemaVersion":1,"revision":1,"blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"promptTemplate":"","workflow":{"stages":[]},"blockSpecs":[],"digest":"sha256:abc"},"blocks":[],"placements":[],"prompt":"","cornerstones":[],"runs":[],"createdWith":{"workSchemaVersion":2,"eventSchemaVersion":2,"rendererSetVersion":1},"createdAt":"2026-07-24T00:00:00Z","updatedAt":"2026-07-24T00:00:00Z"},"definition":{"workId":"work-v2-OTHER-ID","revision":1,"parentRevision":0,"status":"draft","goal":"goal","nodes":[{"id":"n1","title":"T1"}],"artifactSlots":[],"inputSpecs":[],"createdBy":"x","createdAt":"2026-07-24T00:00:00Z","digest":"sha256:wrong"},"artifactSlots":[],"tasks":[],"inputs":[]}`;
  (globalThis as Record<string, unknown>).window = {
    go: { main: { App: {
      GetWork: async () => wrongIDJSON,
      WatchWork: async () => {},
      UnwatchWork: async () => {},
      UpdateDraft: async () => ({ revision: 0 }),
      CreateCandidateRevision: async () => ({ revision: 0, duplicate: false, committed: false, recoverable: true }),
    } } },
    runtime: { EventsOn: () => () => {}, EventsOff: () => {} },
  };
  const wrongAdapter = new WorkControllerAdapter(createWailsWorkControllerPort('tab-wrong')!);
  await wrongAdapter.recoverSnapshot(wrongWorkID).catch(() => {});
  await settle(100);
  const wrongStatus = wrongAdapter.getStatus(wrongWorkID);
  ok(wrongStatus.snapshotError !== null,
    `wails-chain wrong workId: snapshotError visible (snapshotError="${wrongStatus.snapshotError}")`);
  wrongAdapter.dispose();

  // Restore original window.
  (globalThis as Record<string, unknown>).window = savedWindow;
  reset();
}

async function testV2CandidateGenerationFailureRetrySameRequestID(): Promise<void> {
  reset();
  const workID = 'work-v2-candidate-retry';
  const view = makeView(workID, {
    schemaVersion: 2,
    state: 'draft',
    prompt: 'build a report',
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: makeBlankV2Definition(workID) },
  }));
  const port = new TestPort();
  port.candidateErrors.push(Object.assign(new Error('network down'), { code: 'transport' }));
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  // Save prompt.
  const promptEditor = mounted.host.querySelector<HTMLTextAreaElement>('[data-testid="work-prompt-editor"]')!;
  await interact(() => {
    promptEditor.value = 'build report';
    promptEditor.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-save-draft"]')!.click());
  await settle(40);

  // First attempt: fails.
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!.click());
  await settle(40);
  eq(port.candidateInputs.length, 1, 'first candidate call made');
  ok(Boolean(mounted.host.querySelector('[data-testid="work-create-candidate-error"]')), 'error shown after failure');

  // Retry: same button, same requestId.
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!.click());
  await settle(40);
  eq(port.candidateInputs.length, 2, 'retry made second call');
  eq(port.candidateInputs[1].requestId, port.candidateInputs[0].requestId, 'retry reuses same requestId');
  eq(port.candidateInputs[1].baseDefinitionRevision, port.candidateInputs[0].baseDefinitionRevision, 'retry reuses same base revision');
  eq(port.candidateInputs[1].intent, port.candidateInputs[0].intent, 'retry reuses same intent');
  eq(port.candidateInputs[1].expectedRevision, port.candidateInputs[0].expectedRevision, 'retry reuses same expectedRevision');

  await mounted.cleanup();
}

async function testV2CandidateGeneratedThenApply(): Promise<void> {
  reset();
  const workID = 'work-v2-candidate-apply';
  const view = makeView(workID, {
    schemaVersion: 2,
    state: 'draft',
    prompt: 'build a report',
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: makeBlankV2Definition(workID) },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  // Save prompt and generate candidate.
  const editor = mounted.host.querySelector<HTMLTextAreaElement>('[data-testid="work-prompt-editor"]')!;
  await interact(() => { editor.value = 'build report'; editor.dispatchEvent(new Event('input', { bubbles: true })); });
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-save-draft"]')!.click());
  await settle(40);
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!.click());
  await settle(40);

  eq(port.candidateInputs.length, 1, 'candidate generated');
  // Initial draft → candidate: no active baseline for DefinitionDiff, so the
  // direct apply button ("确认并开始执行") appears instead.
  ok(Boolean(mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]')), 'apply button visible after candidate');

  // Apply the candidate.
  const applyBtn = [...mounted.host.querySelectorAll<HTMLButtonElement>('button')]
    .find((b) => b.textContent?.includes('确认并开始执行') || b.textContent?.includes('应用'));
  ok(Boolean(applyBtn), 'apply button found');
  await interact(() => applyBtn!.click());
  await settle(40);

  eq(port.applyInputs.length, 1, 'applyDefinition called');
  eq(port.applyInputs[0].workId, workID, 'apply has correct workId');
  eq(port.applyInputs[0].expectedRevision, port.candidateInputs[0].expectedRevision + 1,
    'apply expectedRevision = candidate result revision (authoritative, not stale view)');
  ok(port.applyInputs[0].requestId.startsWith('work-definition-'), 'apply has typed requestId');

  await mounted.cleanup();
}

async function testV2CandidateRefreshFailureBlocksApply(): Promise<void> {
  reset();
  const workID = 'work-v2-candidate-stale';
  const view = makeView(workID, {
    schemaVersion: 2,
    state: 'draft',
    prompt: 'build a report',
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: makeBlankV2Definition(workID) },
  }));
  const port = new TestPort();
  port.candidateSnapshotStale = true; // fetchSnapshot always returns stale rev
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  // Save prompt and generate candidate.
  const editor = mounted.host.querySelector<HTMLTextAreaElement>('[data-testid="work-prompt-editor"]')!;
  await interact(() => { editor.value = 'build report'; editor.dispatchEvent(new Event('input', { bubbles: true })); });
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-save-draft"]')!.click());
  await settle(40);
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!.click());
  await settle(40);

  eq(port.candidateInputs.length, 1, 'candidate request sent');
  // Refresh failed: candidate diff must NOT be displayed; blank-draft apply
  // button may exist but must be disabled (no candidate was accepted).
  const applyBtn = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]');
  ok(applyBtn !== null, 'blank-draft apply button rendered');
  ok(applyBtn!.disabled, 'apply button disabled — candidate not accepted');
  // DefinitionDiff (candidate apply UI) must not appear.
  ok(mounted.host.querySelector('[data-testid="definition-diff"]') === null, 'candidate diff not shown');
  // Error must be visible.
  ok(Boolean(mounted.host.querySelector('[data-testid="work-create-candidate-error"]')), 'error message visible');
  eq(port.applyInputs.length, 0, 'applyDefinition never called — stale apply blocked');

  await mounted.cleanup();
}

// ── Counterexamples: partial drafts are NOT projected candidates ───────────

async function testV2GoalOnlyDraftStillShowsCandidateGeneration(): Promise<void> {
  reset();
  const workID = 'work-v2-goal-only';
  const view = makeView(workID, {
    schemaVersion: 2,
    state: 'draft',
    prompt: 'build a report',
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  // goal-only: has goal text but 0 nodes — NOT a projected candidate.
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: makeGoalOnlyV2Definition(workID) },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  // Back face: candidate generation button must be visible.
  ok(Boolean(mounted.host.querySelector('[data-testid="work-create-candidate"]')), 'goal-only draft: candidate generation visible');

  // Apply button exists but is disabled (no nodes).
  const applyBtn = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]');
  ok(Boolean(applyBtn), 'goal-only draft: apply button rendered');
  ok(applyBtn!.disabled, 'goal-only draft: apply button disabled (no nodes)');

  // Save prompt and generate candidate — must succeed (base is the draft).
  const editor = mounted.host.querySelector<HTMLTextAreaElement>('[data-testid="work-prompt-editor"]')!;
  await interact(() => { editor.value = 'build report'; editor.dispatchEvent(new Event('input', { bubbles: true })); });
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-save-draft"]')!.click());
  await settle(40);
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!.click());
  await settle(40);

  eq(port.candidateInputs.length, 1, 'goal-only: candidate generation called');
  eq(port.candidateInputs[0].baseDefinitionRevision, 1, 'goal-only: base revision is 1');

  await mounted.cleanup();
}

async function testV2NodesOnlyDraftStillShowsCandidateGeneration(): Promise<void> {
  reset();
  const workID = 'work-v2-nodes-only';
  const view = makeView(workID, {
    schemaVersion: 2,
    state: 'draft',
    prompt: 'build a report',
    createdWith: { workSchemaVersion: 2, eventSchemaVersion: 2, rendererSetVersion: 1 },
  });
  useWorkStore.getState().applySnapshot(view);
  // nodes-only: has 1 node but no goal — NOT a projected candidate.
  useWorkStore.setState((s) => ({
    v2Definitions: { ...s.v2Definitions, [workID]: makeNodesOnlyV2Definition(workID) },
  }));
  const port = new TestPort();
  const mounted = await mount(<WorkCard workID={workID} port={port} />);

  // Back face: candidate generation button must be visible.
  ok(Boolean(mounted.host.querySelector('[data-testid="work-create-candidate"]')), 'nodes-only draft: candidate generation visible');

  // Apply button exists but is disabled (no goal).
  const applyBtn = mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-apply-definition"]');
  ok(Boolean(applyBtn), 'nodes-only draft: apply button rendered');
  ok(applyBtn!.disabled, 'nodes-only draft: apply button disabled (no goal)');

  // Save prompt and generate candidate.
  const editor = mounted.host.querySelector<HTMLTextAreaElement>('[data-testid="work-prompt-editor"]')!;
  await interact(() => { editor.value = 'build report'; editor.dispatchEvent(new Event('input', { bubbles: true })); });
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-save-draft"]')!.click());
  await settle(40);
  await interact(() => mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-create-candidate"]')!.click());
  await settle(40);

  eq(port.candidateInputs.length, 1, 'nodes-only: candidate generation called');
  eq(port.candidateInputs[0].baseDefinitionRevision, 1, 'nodes-only: base revision is 1');

  await mounted.cleanup();
}

function makeBlankV2Definition(workId: string): WorkDefinitionRevision {
  return {
    workId,
    revision: 1,
    parentRevision: 0,
    status: 'draft',
    goal: '',
    nodes: [],
    artifactSlots: [],
    inputSpecs: [],
    createdBy: 'test',
    createdAt: '2026-07-24T00:00:00Z',
    digest: 'sha256:blank',
  };
}

function makeGoalOnlyV2Definition(workId: string): WorkDefinitionRevision {
  return {
    ...makeBlankV2Definition(workId),
    goal: 'build a report',
    nodes: [],
  };
}

function makeNodesOnlyV2Definition(workId: string): WorkDefinitionRevision {
  return {
    ...makeBlankV2Definition(workId),
    goal: '',
    nodes: [{ id: 'node-1', title: 'Analyze' }],
  };
}

function maybeButtonWorkCard(scope: ParentNode, label: string): HTMLButtonElement | null {
  return [...scope.querySelectorAll<HTMLButtonElement>('button')]
    .find((candidate) => candidate.textContent?.replace(/\s+/g, '') === label.replace(/\s+/g, '')) ?? null;
}

async function main(): Promise<void> {
  console.log('\nWorkCard M1 Tests\n');
  await testFacesAndFixedWorkspace();
  await testScrollExpandedAndDraft();
  await testDeepLinkOrderAndMissingReason();
  await testStructuredAttemptDeepLinkUsesExplicitSessionOwner();
  await testRetryFailureIsVisibleAndReusesRequestID();
  await testPreferenceFailureRetry();
  await testArchiveAndUnavailableSession();
  await testPlacementAndFlipAccessibility();
  testMotionCSSContract();
  await testUnknownWorkRetry();

  console.log('\nWorkCard V2 Tests\n');
  await testV2AutoFlipOnApplyDefinition();
  await testV2PlanningFaceDefaultsAndSnapshotDedup();
  await testV2DuplicateApplyNoReflip();
  await testV2FirstObservedDuplicateStillFlipsOnce();
  await testV2CommittedRecoveryBeforeFlip();
  await testV2ManualFlipIndependent();
  await testV2ScrollDraftExpandPerWork();
  await testV2ApplyFailurePreservesDraft();
  await testV2ComponentsA11y();
  await testV2FlipDoesNotInterruptRunning();
  await testV2CSSImportsProduceClasses();
  await testV2ProductionActionCapabilities();
  await testV2ArtifactPreviewConversionPort();
  await testV2ArtifactRetryRevisionIdentity();
  await testV2DefaultWailsProductionMount();
  await testV2ExplicitSessionIdReachesPreviewPatch();
  await testV2CandidateDiffAndLocalCancel();
  await testV2DefinitionDiffAcceptsLegacyNullImpactLists();
  await testV2CandidatePlannerRecovery();
  await testV2BlankDraftStoreChainNoRun();
  await testV2ActiveDefinitionLateArrivalRestoresRun();
  await testV2BlankDraftBackCandidateGeneration();
  await testV2BlankDraftProductionWailsChain();
  await testV2CandidateGenerationFailureRetrySameRequestID();
  await testV2CandidateGeneratedThenApply();
  await testV2CandidateRefreshFailureBlocksApply();
  await testV2GoalOnlyDraftStillShowsCandidateGeneration();
  await testV2NodesOnlyDraftStillShowsCandidateGeneration();

  console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total\n`);
  if (failed > 0) process.exit(1);
}

void main().catch((error) => {
  console.error(error);
  process.exit(1);
});
