import type { CornerstoneControllerPort } from './controller';
import type {
  AcceptCornerstoneInput,
  Cornerstone,
  CornerstoneMutationResult,
  FreezeCornerstoneInput,
  PinCornerstoneInput,
  RefreshCornerstoneInput,
  RemoveCornerstoneInput,
  RepairCornerstoneInput,
  ResumeRunInput,
  SourceRef,
  UndoCornerstoneInput,
  ValidateCornerstoneInput,
  Work,
  WorkflowRun,
  WorkView,
} from './types';

export interface FakeWorkControllerOptions {
  networkErrorOn?: Set<string>;
  revisionConflictOn?: Set<string>;
  latencyMs?: number;
}

let idCounter = 0;

function nextId(): string {
  idCounter++;
  return `cs-${Date.now().toString(36)}-${idCounter.toString(36)}`;
}

/** Test-only in-memory port used until the Wails Work binding exposes T2. */
export class FakeWorkController implements CornerstoneControllerPort {
  private readonly views = new Map<string, WorkView>();
  private readonly completed = new Map<string, CornerstoneMutationResult>();
  private readonly pending = new Map<string, Promise<CornerstoneMutationResult>>();

  constructor(private readonly options: FakeWorkControllerOptions = {}) {}

  seedWork(work: Work, revision = 1): void {
    this.views.set(work.id, {
      schemaVersion: work.schemaVersion,
      work: structuredClone(work),
      revision,
      assessment: { state: 'ready', blocking: false, degraded: false, issues: [] },
    });
  }

  seedView(view: WorkView): void {
    this.views.set(view.work.id, structuredClone(view));
  }

  async getWork(workId: string): Promise<WorkView> {
    return structuredClone(this.requireView(workId));
  }

  getCompletedRequests(): ReadonlyMap<string, CornerstoneMutationResult> {
    return this.completed;
  }

  pinCornerstone = (input: PinCornerstoneInput): Promise<CornerstoneMutationResult> => this.mutate(
    input,
    'pin',
    (work) => {
      const now = new Date().toISOString();
      const provenance: SourceRef = { kind: 'work', workId: work.id };
      const cornerstone: Cornerstone = {
        id: nextId(),
        workId: work.id,
        type: input.type,
        title: input.title,
        content: input.content,
        ref: input.ref,
        mode: input.mode,
        digest: `fake-${input.requestId}`,
        required: input.required,
        status: 'active',
        tags: input.tags,
        provenance,
        pinnedAt: now,
        updatedAt: now,
      };
      work.cornerstones = [...work.cornerstones, cornerstone];
      return cornerstone;
    },
  );

  refreshCornerstone = (input: RefreshCornerstoneInput): Promise<CornerstoneMutationResult> => this.mutateItem(
    input,
    'refresh',
    (cornerstone) => ({
      ...cornerstone,
      status: cornerstone.mode === 'live_ref' && cornerstone.status === 'stale' ? 'active' : cornerstone.status,
      lastVerifiedAt: new Date().toISOString(),
    }),
  );

  validateCornerstone = (input: ValidateCornerstoneInput): Promise<CornerstoneMutationResult> => this.mutateItem(
    input,
    'validate',
    (cornerstone) => ({ ...cornerstone, lastVerifiedAt: new Date().toISOString() }),
  );

  freezeCornerstone = (input: FreezeCornerstoneInput): Promise<CornerstoneMutationResult> => this.mutateItem(
    input,
    'freeze',
    (cornerstone) => ({ ...cornerstone, mode: 'snapshot', status: 'active', resolveErrorKind: undefined }),
  );

  acceptCornerstone = (input: AcceptCornerstoneInput): Promise<CornerstoneMutationResult> => this.mutateItem(
    input,
    'accept',
    (cornerstone) => ({ ...cornerstone, status: 'active', candidateDigest: undefined, resolveErrorKind: undefined }),
  );

  repairCornerstone = (input: RepairCornerstoneInput): Promise<CornerstoneMutationResult> => this.mutateItem(
    input,
    'repair',
    (cornerstone) => ({
      ...cornerstone,
      ref: input.ref ?? cornerstone.ref,
      content: input.content ?? cornerstone.content,
      status: 'active',
      error: undefined,
      resolveErrorKind: undefined,
    }),
  );

  removeCornerstone = (input: RemoveCornerstoneInput): Promise<CornerstoneMutationResult> => this.mutateItem(
    input,
    'remove',
    (cornerstone) => ({ ...cornerstone, tombstone: true }),
  );

  undoCornerstone = (input: UndoCornerstoneInput): Promise<CornerstoneMutationResult> => this.mutateItem(
    input,
    'undo',
    (cornerstone) => ({ ...cornerstone, tombstone: false }),
  );

  resumeRun = (input: ResumeRunInput): Promise<WorkflowRun> => {
    const view = this.requireView(input.workId);
    const run = view.work.runs.find((r) => r.id === input.runId);
    if (!run) return Promise.reject(new Error(`run ${input.runId} not found`));
    const resumed: WorkflowRun = {
      ...run,
      state: 'running',
      stages: run.stages.map((s) => {
        if (s.state === 'waiting') return { ...s, state: 'running' as const };
        return s;
      }),
    };
    const updatedRuns = view.work.runs.map((r) => r.id === run.id ? resumed : r);
    view.work = { ...view.work, runs: updatedRuns, state: 'running', updatedAt: new Date().toISOString() };
    view.revision += 1;
    this.views.set(input.workId, view);
    return Promise.resolve(resumed);
  };

  private mutateItem(
    input: RefreshCornerstoneInput,
    action: string,
    update: (cornerstone: Cornerstone) => Cornerstone,
  ): Promise<CornerstoneMutationResult> {
    return this.mutate(input, action, (work) => {
      const index = work.cornerstones.findIndex((cornerstone) => cornerstone.id === input.cornerstoneId);
      if (index < 0) throw new Error('Cornerstone 不存在或已清理。');
      const cornerstone = {
        ...update(structuredClone(work.cornerstones[index])),
        updatedAt: new Date().toISOString(),
      };
      work.cornerstones = [
        ...work.cornerstones.slice(0, index),
        cornerstone,
        ...work.cornerstones.slice(index + 1),
      ];
      return cornerstone;
    });
  }

  private mutate(
    input: { workId: string; requestId: string; expectedRevision: number },
    action: string,
    update: (work: Work) => Cornerstone,
  ): Promise<CornerstoneMutationResult> {
    const done = this.completed.get(input.requestId);
    if (done) return Promise.resolve(structuredClone(done));
    const inFlight = this.pending.get(input.requestId);
    if (inFlight) return inFlight;

    const request = this.execute(input, action, update).then((result) => {
      if (result.ok || result.error.kind === 'revision_conflict') this.completed.set(input.requestId, structuredClone(result));
      return result;
    }).finally(() => this.pending.delete(input.requestId));
    this.pending.set(input.requestId, request);
    return request;
  }

  private async execute(
    input: { workId: string; requestId: string; expectedRevision: number },
    action: string,
    update: (work: Work) => Cornerstone,
  ): Promise<CornerstoneMutationResult> {
    if (this.options.latencyMs) await new Promise((resolve) => setTimeout(resolve, this.options.latencyMs));
    if (this.options.networkErrorOn?.has(action)) {
      return { ok: false, error: { kind: 'network_error', requestId: input.requestId, message: '网络暂不可用，请重试。', retryable: true } };
    }

    const view = this.requireView(input.workId);
    if (this.options.revisionConflictOn?.delete(action) || input.expectedRevision !== view.revision) {
      const latestSnapshot = 'cornerstoneId' in input
        ? view.work.cornerstones.find((cornerstone) => cornerstone.id === input.cornerstoneId)
        : undefined;
      return {
        ok: false,
        error: {
          kind: 'revision_conflict',
          workId: input.workId,
          cornerstoneId: 'cornerstoneId' in input ? String(input.cornerstoneId) : '__new__',
          expectedRevision: input.expectedRevision,
          actualRevision: view.revision,
          latestSnapshot: latestSnapshot && structuredClone(latestSnapshot),
          latestView: structuredClone(view),
          message: 'Work 已被其他操作更新。',
        },
      };
    }

    const nextWork = structuredClone(view.work);
    const cornerstone = update(nextWork);
    nextWork.updatedAt = new Date().toISOString();
    const nextView = { ...view, work: nextWork, revision: view.revision + 1 };
    this.views.set(input.workId, nextView);
    return {
      ok: true,
      cornerstone: structuredClone(cornerstone),
      workView: structuredClone(nextView),
      revision: nextView.revision,
    };
  }

  private requireView(workId: string): WorkView {
    const view = this.views.get(workId);
    if (!view) throw new Error(`Work ${workId} 不存在。`);
    return view;
  }
}
