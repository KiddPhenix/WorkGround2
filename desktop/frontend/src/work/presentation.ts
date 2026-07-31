import type {
  ArtifactRef,
  WorkState,
} from './types';
import type {
  ArtifactSlot,
  ArtifactSlotState,
  TaskStateV2,
  TaskV2View,
  WorkDefinitionRevision,
} from './types_v2';

export type WorkPresentationPhase =
  | 'planning'
  | 'running'
  | 'paused'
  | 'waiting'
  | 'failed'
  | 'completed';

export type WorkLayoutMode =
  | 'structure'
  | 'balanced'
  | 'attention'
  | 'results';

export interface WorkPresentationOptions {
  /**
   * The authoritative run when the caller has it. Without one, the projector
   * deterministically chooses the most recently updated relevant run.
   */
  activeRunId?: string;
  /** Authoritative Work lifecycle state. Runtime Task/Artifact snapshots may
   * remain running while a paused Work is resumable. */
  workState?: WorkState;
}

export interface PresentationTask {
  id: string;
  runId?: string;
  nodeId: string;
  order: number;
  title: string;
  state: TaskStateV2;
  progress?: string;
  sessionRef?: TaskV2View['sessionRef'];
  waitingInputIds?: string[];
  error?: string;
  retryable: boolean;
  updatedAt: string;
  source: 'runtime' | 'definition';
}

export interface PresentationArtifactSlot {
  id: string;
  order: number;
  title: string;
  kind: string;
  expectedCount: number;
  required: boolean;
  state: ArtifactSlotState;
  artifactRefs: ArtifactRef[];
  progress?: number;
  summary?: string;
  error?: ArtifactSlot['error'];
  revision: number;
  source: 'runtime' | 'definition';
}

export interface ArtifactPresentationSummary {
  total: number;
  required: number;
  ready: number;
  requiredReady: number;
  reserved: number;
  generating: number;
  partial: number;
  failed: number;
  stale: number;
  artifactCount: number;
  allRequiredReady: boolean;
}

export interface WorkPresentation {
  workId: string;
  definitionRevision: number;
  runId?: string;
  phase: WorkPresentationPhase;
  layoutMode: WorkLayoutMode;
  tasks: PresentationTask[];
  attentionTask?: PresentationTask;
  primaryTask?: PresentationTask;
  artifactSlots: PresentationArtifactSlot[];
  artifacts: ArtifactPresentationSummary;
}

const FAILURE_STATES: ReadonlySet<TaskStateV2> = new Set([
  'failed_retryable',
  'failed_terminal',
  'canceled',
]);

const WAITING_STATES: ReadonlySet<TaskStateV2> = new Set([
  'waiting_input',
  'waiting_approval',
]);

function timestamp(value: string): number {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY;
}

function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function chooseRunId(tasks: readonly TaskV2View[], activeRunId?: string): string | undefined {
  if (activeRunId) return activeRunId;

  const latestByRun = new Map<string, number>();
  for (const task of tasks) {
    const next = timestamp(task.updatedAt);
    const current = latestByRun.get(task.runId);
    if (current === undefined || next > current) latestByRun.set(task.runId, next);
  }

  return [...latestByRun]
    .sort(([leftId, leftTime], [rightId, rightTime]) =>
      rightTime - leftTime || compareText(leftId, rightId))
    [0]?.[0];
}

function utf8Length(value: string): number {
  return new TextEncoder().encode(value).length;
}

function syntheticTaskId(
  definition: WorkDefinitionRevision,
  runId: string | undefined,
  nodeId: string,
): string {
  if (runId) return `${utf8Length(runId)}:${runId}/${utf8Length(nodeId)}:${nodeId}`;
  return `definition:${definition.workId}:${definition.revision}:${nodeId}`;
}

function compareTaskFreshness(left: TaskV2View, right: TaskV2View): number {
  return timestamp(left.updatedAt) - timestamp(right.updatedAt)
    || compareText(left.id, right.id);
}

function projectTasks(
  definition: WorkDefinitionRevision,
  tasks: readonly TaskV2View[],
  activeRunId?: string,
): { runId?: string; tasks: PresentationTask[] } {
  const nodeIds = new Set(definition.nodes.map((node) => node.id));
  const relevant = tasks.filter((task) => nodeIds.has(task.nodeId));
  const runId = chooseRunId(relevant, activeRunId);
  const runtimeByNode = new Map<string, TaskV2View>();

  if (runId) {
    for (const task of relevant) {
      if (task.runId !== runId) continue;
      const existing = runtimeByNode.get(task.nodeId);
      if (!existing || compareTaskFreshness(task, existing) > 0) {
        runtimeByNode.set(task.nodeId, task);
      }
    }
  }

  return {
    runId,
    tasks: definition.nodes.map((node, order) => {
      const runtime = runtimeByNode.get(node.id);
      if (!runtime) {
        return {
          id: syntheticTaskId(definition, runId, node.id),
          runId,
          nodeId: node.id,
          order,
          title: node.title,
          state: 'pending',
          retryable: false,
          updatedAt: definition.createdAt,
          source: 'definition',
        };
      }
      return {
        id: runtime.id,
        runId: runtime.runId,
        nodeId: node.id,
        order,
        title: node.title,
        state: runtime.state,
        progress: runtime.progress,
        sessionRef: runtime.sessionRef,
        waitingInputIds: runtime.waitingInputIds,
        error: runtime.error,
        retryable: runtime.retryable,
        updatedAt: runtime.updatedAt,
        source: 'runtime',
      };
    }),
  };
}

function compareSlotAuthority(left: ArtifactSlot, right: ArtifactSlot): number {
  return left.revision - right.revision
    || compareText(left.state, right.state)
    || compareText(left.upstreamDigest ?? '', right.upstreamDigest ?? '');
}

function projectArtifactSlots(
  definition: WorkDefinitionRevision,
  slots: readonly ArtifactSlot[],
): PresentationArtifactSlot[] {
  const definitions = new Map(definition.artifactSlots.map((slot) => [slot.id, slot]));
  const currentById = new Map<string, ArtifactSlot>();

  for (const slot of slots) {
    if (slot.workId !== definition.workId
      || slot.definitionRev !== definition.revision
      || !definitions.has(slot.id)) {
      continue;
    }
    const existing = currentById.get(slot.id);
    if (!existing || compareSlotAuthority(slot, existing) > 0) {
      currentById.set(slot.id, slot);
    }
  }

  return definition.artifactSlots.map((slot, order) => {
    const runtime = currentById.get(slot.id);
    if (!runtime) {
      return {
        ...slot,
        order,
        state: 'reserved',
        artifactRefs: [],
        revision: 0,
        source: 'definition',
      };
    }
    return {
      id: slot.id,
      order,
      title: slot.title,
      kind: slot.kind,
      expectedCount: slot.expectedCount,
      required: slot.required,
      state: runtime.state,
      artifactRefs: runtime.artifactRefs,
      progress: runtime.progress,
      summary: runtime.summary,
      error: runtime.error,
      revision: runtime.revision,
      source: 'runtime',
    };
  });
}

function summarizeArtifacts(slots: readonly PresentationArtifactSlot[]): ArtifactPresentationSummary {
  const count = (state: ArtifactSlotState) =>
    slots.reduce((total, slot) => total + (slot.state === state ? 1 : 0), 0);
  const required = slots.filter((slot) => slot.required);
  const requiredReady = required.filter((slot) => slot.state === 'ready').length;

  return {
    total: slots.length,
    required: required.length,
    ready: count('ready'),
    requiredReady,
    reserved: count('reserved'),
    generating: count('generating'),
    partial: count('partial'),
    failed: count('failed'),
    stale: count('stale'),
    artifactCount: slots.reduce((total, slot) => total + slot.artifactRefs.length, 0),
    allRequiredReady: requiredReady === required.length,
  };
}

function firstTask(
  tasks: readonly PresentationTask[],
  states: ReadonlySet<TaskStateV2>,
): PresentationTask | undefined {
  return tasks.find((task) => states.has(task.state));
}

function deriveAttentionTask(
  tasks: readonly PresentationTask[],
): PresentationTask | undefined {
  return firstTask(tasks, FAILURE_STATES) ?? firstTask(tasks, WAITING_STATES);
}

function derivePrimaryTask(
  tasks: readonly PresentationTask[],
  attentionTask: PresentationTask | undefined,
): PresentationTask | undefined {
  return tasks.find((task) => task.state === 'running')
    ?? tasks.find((task) => task.state === 'ready')
    ?? attentionTask
    ?? tasks.find((task) => task.state === 'invalidated')
    ?? tasks.find((task) => task.state === 'pending');
}

function derivePhase(
  tasks: readonly PresentationTask[],
  slots: readonly PresentationArtifactSlot[],
  artifacts: ArtifactPresentationSummary,
  workState?: WorkState,
): WorkPresentationPhase {
  const tasksCompleted = tasks.length > 0
    && tasks.every((task) => task.state === 'completed');
  if (tasksCompleted && artifacts.allRequiredReady) return 'completed';

  if (tasks.some((task) => FAILURE_STATES.has(task.state))
    || slots.some((slot) => slot.required && slot.state === 'failed')) {
    return 'failed';
  }
  if (workState === 'paused') return 'paused';
  if (tasks.some((task) => WAITING_STATES.has(task.state))) return 'waiting';

  const hasRuntime = tasks.some((task) => task.source === 'runtime');
  const artifactStarted = slots.some((slot) =>
    slot.source === 'runtime' && slot.state !== 'reserved');
  return hasRuntime || artifactStarted ? 'running' : 'planning';
}

function layoutModeForPhase(phase: WorkPresentationPhase): WorkLayoutMode {
  switch (phase) {
    case 'planning':
      return 'structure';
    case 'running':
    case 'paused':
      return 'balanced';
    case 'waiting':
    case 'failed':
      return 'attention';
    case 'completed':
      return 'results';
  }
}

/**
 * Derives a deterministic, domain-independent UI projection from the current
 * active Definition and runtime snapshots. Historical Definition revisions,
 * runs, undeclared nodes and undeclared artifact slots never enter the model.
 */
export function deriveWorkPresentation(
  definition: WorkDefinitionRevision,
  tasks: readonly TaskV2View[],
  artifactSlots: readonly ArtifactSlot[],
  options: WorkPresentationOptions = {},
): WorkPresentation {
  const projectedTasks = projectTasks(definition, tasks, options.activeRunId);
  const projectedSlots = projectArtifactSlots(definition, artifactSlots);
  const artifacts = summarizeArtifacts(projectedSlots);
  const phase = derivePhase(projectedTasks.tasks, projectedSlots, artifacts, options.workState);
  const attentionTask = deriveAttentionTask(projectedTasks.tasks);
  const primaryTask = phase === 'completed'
    ? undefined
    : derivePrimaryTask(projectedTasks.tasks, attentionTask);

  return {
    workId: definition.workId,
    definitionRevision: definition.revision,
    runId: projectedTasks.runId,
    phase,
    layoutMode: layoutModeForPhase(phase),
    tasks: projectedTasks.tasks,
    attentionTask,
    primaryTask,
    artifactSlots: projectedSlots,
    artifacts,
  };
}
