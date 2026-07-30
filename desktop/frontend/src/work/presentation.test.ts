import assert from 'node:assert/strict';

import { deriveWorkPresentation } from './presentation.js';
import type {
  ArtifactSlot,
  ArtifactSlotDef,
  NodeDef,
  TaskStateV2,
  TaskV2View,
  WorkDefinitionRevision,
} from './types_v2.js';

const CREATED_AT = '2026-07-30T00:00:00.000Z';

function node(id: string, title = id): NodeDef {
  return { id, title };
}

function artifactDef(
  id: string,
  required = true,
  expectedCount = 1,
): ArtifactSlotDef {
  return { id, title: id, kind: 'document', expectedCount, required };
}

function definition(
  nodes: NodeDef[],
  artifactSlots: ArtifactSlotDef[] = [],
  revision = 4,
): WorkDefinitionRevision {
  return {
    workId: 'work-1',
    revision,
    parentRevision: revision - 1,
    status: 'active',
    goal: '通用目标',
    nodes,
    artifactSlots,
    inputSpecs: [],
    createdBy: 'test',
    createdAt: CREATED_AT,
    digest: `definition-${revision}`,
  };
}

function task(
  nodeId: string,
  state: TaskStateV2,
  overrides: Partial<TaskV2View> = {},
): TaskV2View {
  const runId = overrides.runId ?? 'run-current';
  return {
    id: overrides.id ?? `${runId}/${nodeId}`,
    runId,
    nodeId,
    title: overrides.title ?? `runtime-${nodeId}`,
    state,
    retryable: overrides.retryable ?? state === 'failed_retryable',
    updatedAt: overrides.updatedAt ?? '2026-07-30T01:00:00.000Z',
    ...overrides,
  };
}

function slot(
  id: string,
  state: ArtifactSlot['state'],
  overrides: Partial<ArtifactSlot> = {},
): ArtifactSlot {
  return {
    id,
    workId: 'work-1',
    definitionRev: 4,
    title: id,
    kind: 'document',
    expectedCount: 1,
    required: true,
    state,
    artifactRefs: [],
    revision: 1,
    ...overrides,
  };
}

// Definition order is authoritative. Missing runtime rows are synthesized and
// unrelated nodes cannot leak into the presentation.
{
  const model = deriveWorkPresentation(
    definition([node('collect', '收集'), node('review', '复核'), node('deliver', '交付')]),
    [
      task('other', 'running'),
      task('review', 'running', { title: '旧标题' }),
      task('collect', 'completed'),
    ],
    [],
  );

  assert.deepEqual(model.tasks.map((item) => item.nodeId), ['collect', 'review', 'deliver']);
  assert.deepEqual(model.tasks.map((item) => item.state), ['completed', 'running', 'pending']);
  assert.equal(model.tasks[1]?.title, '复核');
  assert.equal(model.tasks[2]?.source, 'definition');
  assert.equal(model.primaryTask?.nodeId, 'review');
  assert.equal(model.phase, 'running');
  assert.equal(model.layoutMode, 'balanced');
}

// No runtime means the active structure is still visible as pending/planning.
{
  const model = deriveWorkPresentation(
    definition([node('first'), node('second')]),
    [],
    [],
  );

  assert.equal(model.runId, undefined);
  assert.equal(model.phase, 'planning');
  assert.equal(model.layoutMode, 'structure');
  assert.deepEqual(model.tasks.map((item) => item.state), ['pending', 'pending']);
  assert.equal(model.primaryTask?.nodeId, 'first');
}

// Explicit current-run authority excludes a newer historical update. Runtime
// gaps in that run are represented by deterministic pending rows.
{
  const def = definition([node('a'), node('b')]);
  const model = deriveWorkPresentation(
    def,
    [
      task('a', 'failed_terminal', {
        runId: 'run-history',
        updatedAt: '2026-07-30T09:00:00.000Z',
      }),
      task('a', 'completed', {
        runId: 'run-active',
        updatedAt: '2026-07-30T02:00:00.000Z',
      }),
    ],
    [],
    { activeRunId: 'run-active' },
  );

  assert.equal(model.runId, 'run-active');
  assert.deepEqual(model.tasks.map((item) => item.state), ['completed', 'pending']);
  assert.equal(model.tasks[1]?.id, '10:run-active/1:b');
  assert.equal(model.phase, 'running');
}

// Without explicit run authority, selection is deterministic and based on the
// latest relevant runtime, independent of input ordering.
{
  const def = definition([node('a')]);
  const older = task('a', 'failed_terminal', {
    runId: 'run-old',
    updatedAt: '2026-07-30T01:00:00.000Z',
  });
  const current = task('a', 'running', {
    runId: 'run-new',
    updatedAt: '2026-07-30T02:00:00.000Z',
  });
  const left = deriveWorkPresentation(def, [older, current], []);
  const right = deriveWorkPresentation(def, [current, older], []);

  assert.equal(left.runId, 'run-new');
  assert.deepEqual(left, right);
}

// Waiting states focus the attention layout while an independent running task
// remains primary.
for (const waiting of ['waiting_input', 'waiting_approval'] as const) {
  const model = deriveWorkPresentation(
    definition([node('wait'), node('parallel')]),
    [task('wait', waiting), task('parallel', 'running')],
    [],
  );

  assert.equal(model.phase, 'waiting');
  assert.equal(model.layoutMode, 'attention');
  assert.equal(model.attentionTask?.nodeId, 'wait');
  assert.equal(model.primaryTask?.nodeId, 'parallel');
}

// Both failure classes are explicit failures and preserve retryability from
// the runtime projection.
for (const failed of ['failed_retryable', 'failed_terminal'] as const) {
  const model = deriveWorkPresentation(
    definition([node('failed')]),
    [task('failed', failed)],
    [],
  );

  assert.equal(model.phase, 'failed');
  assert.equal(model.layoutMode, 'attention');
  assert.equal(model.attentionTask?.state, failed);
  assert.equal(model.attentionTask?.retryable, failed === 'failed_retryable');
}

// Completion requires all Definition tasks and every required slot to be
// ready on the active Definition revision. Historical ready data is ignored.
{
  const def = definition(
    [node('produce')],
    [artifactDef('required'), artifactDef('optional', false)],
  );
  const tasks = [task('produce', 'completed')];
  const historicalReady = slot('required', 'ready', { definitionRev: 3 });
  const optionalFailure = slot('optional', 'failed', { required: false });
  const incomplete = deriveWorkPresentation(def, tasks, [historicalReady, optionalFailure]);

  assert.equal(incomplete.phase, 'running');
  assert.equal(incomplete.artifacts.required, 1);
  assert.equal(incomplete.artifacts.requiredReady, 0);
  assert.equal(incomplete.artifacts.allRequiredReady, false);
  assert.equal(incomplete.artifactSlots[0]?.source, 'definition');

  const complete = deriveWorkPresentation(
    def,
    tasks,
    [
      historicalReady,
      optionalFailure,
      slot('required', 'ready', {
        artifactRefs: [{
          id: 'artifact-1',
          name: 'result.md',
          type: 'text/markdown',
          status: 'available',
        }],
      }),
    ],
  );

  assert.equal(complete.phase, 'completed');
  assert.equal(complete.layoutMode, 'results');
  assert.equal(complete.primaryTask, undefined);
  assert.equal(complete.artifacts.ready, 1);
  assert.equal(complete.artifacts.failed, 1);
  assert.equal(complete.artifacts.artifactCount, 1);
  assert.equal(complete.artifacts.allRequiredReady, true);
}

// A failed required artifact is blocking even when its producer task finished.
{
  const model = deriveWorkPresentation(
    definition([node('produce')], [artifactDef('result')]),
    [task('produce', 'completed')],
    [slot('result', 'failed')],
  );

  assert.equal(model.phase, 'failed');
  assert.equal(model.layoutMode, 'attention');
}

process.stdout.write('presentation tests passed\n');
