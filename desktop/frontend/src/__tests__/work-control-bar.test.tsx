import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

import {
  WorkControlBar,
  deriveWorkControlAvailability,
  nextActionIntent,
} from '../components/work/WorkControlBar';
import type { WorkflowRun } from '../work/types';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else failed++;
}

function eq<T>(actual: T, expected: T, label: string): void {
  ok(
    actual === expected,
    `${label}${actual === expected ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`,
  );
}

function run(id: string, state: WorkflowRun['state']): WorkflowRun {
  return {
    id,
    workId: 'work-1',
    definitionDigest: 'definition-1',
    state,
    stages: [],
    startedAt: '2030-01-01T00:00:00Z',
  };
}

const capabilities = {
  hasStart: true,
  hasResume: true,
  hasPause: true,
  hasStop: true,
  hasRestart: true,
};

function main(): void {
  const html = renderToStaticMarkup(
    <WorkControlBar
      workId="work-1"
      workState="draft"
      runs={[]}
      readonly={false}
      archived={false}
      onStart={async () => run('run-1', 'running')}
    />,
  );
  for (const testID of [
    'work-control-bar',
    'work-ctrl-start',
    'work-ctrl-pause',
    'work-ctrl-stop',
    'work-ctrl-restart',
  ]) {
    ok(html.includes(`data-testid="${testID}"`), `renders ${testID}`);
  }

  const draft = deriveWorkControlAvailability({
    workState: 'draft',
    runs: [],
    readonly: false,
    archived: false,
    ...capabilities,
  });
  ok(draft.canStart, 'draft enables start');
  ok(!draft.canPause && !draft.canStop && !draft.canRestart, 'draft disables inactive controls');

  const running = deriveWorkControlAvailability({
    workState: 'running',
    runs: [run('run-1', 'running')],
    readonly: false,
    archived: false,
    ...capabilities,
  });
  ok(!running.canStart, 'running disables start');
  ok(running.canPause && running.canStop && running.canRestart, 'running enables pause, stop, and restart');

  const paused = deriveWorkControlAvailability({
    workState: 'paused',
    runs: [run('run-1', 'waiting')],
    readonly: false,
    archived: false,
    ...capabilities,
  });
  ok(paused.canStart && paused.resumeOnStart, 'paused start resumes the same run');
  ok(!paused.canPause && paused.canStop && paused.canRestart, 'paused run can stop or restart');

  const completed = deriveWorkControlAvailability({
    workState: 'completed',
    runs: [run('run-1', 'completed')],
    readonly: false,
    archived: false,
    ...capabilities,
  });
  ok(!completed.canStart && completed.canRestart, 'completed work uses restart instead of legacy start');
  ok(!completed.canPause && !completed.canStop, 'completed work cannot pause or stop');

  const missingCapabilities = deriveWorkControlAvailability({
    workState: 'running',
    runs: [run('run-1', 'running')],
    readonly: false,
    archived: false,
    hasStart: false,
    hasResume: false,
    hasPause: false,
    hasStop: false,
    hasRestart: false,
  });
  ok(
    !missingCapabilities.canStart
      && !missingCapabilities.canPause
      && !missingCapabilities.canStop
      && !missingCapabilities.canRestart,
    'missing capabilities disable every action',
  );

  const readonly = deriveWorkControlAvailability({
    workState: 'running',
    runs: [run('run-1', 'running')],
    readonly: true,
    archived: false,
    ...capabilities,
  });
  ok(!readonly.canPause && !readonly.canStop && !readonly.canRestart, 'readonly disables mutations');

  let sequence = 0;
  const makeID = (prefix: string) => `${prefix}-${++sequence}`;
  const first = nextActionIntent(null, 'stop', 'run-1', makeID);
  const retry = nextActionIntent(first, 'stop', 'run-1', makeID);
  const nextRun = nextActionIntent(first, 'stop', 'run-2', makeID);
  eq(retry.requestId, first.requestId, 'same failed action safely reuses requestId');
  ok(nextRun.requestId !== first.requestId, 'new run receives a new requestId');

  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

main();
