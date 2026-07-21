import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { JSDOM } from 'jsdom';
import React, { act, useEffect, type ReactElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { RunProgressIndicator } from '../components/work/RunProgressIndicator';
import { useWorkStore, useWorkUIStore } from '../work/store';
import type {
  Attempt,
  RetryIntent,
  RetryStatus,
  RunSelection,
  SessionRef,
  Stage,
  Task,
  WorkflowRun,
} from '../work/types';

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
  return dom;
}

const dom = setupDOM();

async function settle(delay = 30): Promise<void> {
  await act(async () => { await new Promise<void>((res) => setTimeout(res, delay)); });
}

async function interact(action: () => void): Promise<void> {
  await act(async () => {
    action();
    await new Promise<void>((res) => setTimeout(res, 30));
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
  await act(async () => { root.render(element); });
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

function makeSessionRef(overrides: Partial<SessionRef> = {}): SessionRef {
  return {
    sessionPath: '/sessions/test',
    branchId: 'main',
    modelRef: 'test-model',
    turnCount: 3,
    preview: 'test preview',
    startedAt: '2026-07-20T10:00:00Z',
    ...overrides,
  };
}

function makeAttempt(index: number, state: WorkflowRun['state'], overrides: Partial<Attempt> = {}): Attempt {
  return {
    id: `attempt-${index}`,
    index,
    state,
    sessionRef: makeSessionRef(),
    startedAt: '2026-07-20T10:00:00Z',
    ...(state !== 'running' ? { finishedAt: '2026-07-20T10:01:00Z' } : {}),
    ...overrides,
  };
}

function makeTask(name: string, state: WorkflowRun['state'], attempts: Attempt[] = []): Task {
  return { id: `task-${name}`, name, state, attempts };
}

function makeStage(name: string, state: WorkflowRun['state'], tasks: Task[] = []): Stage {
  return {
    id: `stage-${name}`,
    name,
    state,
    tasks,
    startedAt: '2026-07-20T10:00:00Z',
    ...(state !== 'running' ? { finishedAt: '2026-07-20T10:01:00Z' } : {}),
  };
}

function makeRun(id: string, state: WorkflowRun['state'], stages: Stage[] = []): WorkflowRun {
  return {
    id,
    workId: 'work-1',
    definitionDigest: 'digest',
    state,
    stages,
    startedAt: '2026-07-20T10:00:00Z',
    ...(state !== 'running' ? { finishedAt: '2026-07-20T10:01:00Z' } : {}),
  };
}

function makeWork(overrides: Record<string, unknown> = {}) {
  return {
    schemaVersion: 1,
    id: 'work-1',
    name: 'Test Work',
    state: 'running' as const,
    archiveState: 'active' as const,
    blueprintRef: { id: 'bp:test', schemaVersion: 1, version: 1 },
    definitionSnapshot: {
      schemaVersion: 1, revision: 1, blueprintRef: { id: 'bp:test', schemaVersion: 1, version: 1 },
      promptTemplate: '', workflow: { stages: [] }, blockSpecs: [], digest: 'digest',
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
  };
}

// ── Tests ─────────────────────────────────────────────────────────────────

async function testEmptyStateShowsPlaceholder(): Promise<void> {
  reset();
  const work = makeWork();
  const selections: RunSelection[] = [];
  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={(s) => selections.push(s)}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );
  ok(Boolean(mounted.host.querySelector('[data-testid="run-progress-empty"]')), 'empty runs shows placeholder');
  ok(mounted.host.textContent?.includes('暂无运行记录') ?? false, 'empty message is in Chinese');
  await mounted.cleanup();
}

async function testRendersRunStagesTasksAttempts(): Promise<void> {
  reset();
  const att0 = makeAttempt(0, 'completed');
  const att1 = makeAttempt(1, 'failed', { error: 'timeout' });
  const task = makeTask('lint', 'completed', [att0, att1]);
  const stage = makeStage('review', 'completed', [task]);
  const run = makeRun('run-1', 'completed', [stage]);
  const work = makeWork({ runs: [run] });

  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={() => {}}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  // Run level.
  ok(Boolean(mounted.host.querySelector('[data-testid="run-progress"]')), 'run progress renders');
  ok(mounted.host.textContent?.includes('review') ?? false, 'stage name is visible');
  ok(mounted.host.textContent?.includes('lint') ?? false, 'task name is visible');
  ok(mounted.host.textContent?.includes('timeout') ?? false, 'failure error is visible');

  // State badges.
  const badges = [...mounted.host.querySelectorAll('.wg2-run-state-badge')];
  const badgeTexts = badges.map((b) => b.textContent?.trim());
  ok(badgeTexts.includes('已完成'), 'completed state badge rendered');
  ok(badgeTexts.includes('失败'), 'failed state badge rendered');

  // Check attempt indices.
  ok(mounted.host.textContent?.includes('#1') ?? false, 'attempt #1 index visible');
  ok(mounted.host.textContent?.includes('#2') ?? false, 'attempt #2 index visible');

  await mounted.cleanup();
}

async function testSelectionHighlightsTarget(): Promise<void> {
  reset();
  const att = makeAttempt(0, 'completed');
  const task = makeTask('lint', 'completed', [att]);
  const stage = makeStage('review', 'completed', [task]);
  const run = makeRun('run-1', 'completed', [stage]);
  const work = makeWork({ runs: [run] });

  const selection: RunSelection = { runId: 'run-1', stageId: 'stage-review', taskId: 'task-lint', attemptId: 'attempt-0', attemptIndex: 0 };
  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      selection={selection}
      onSelect={() => {}}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  const selectedAttempt = mounted.host.querySelector('.wg2-run-attempt.wg2-run-selected');
  ok(Boolean(selectedAttempt), 'selected attempt has selection class');
  eq(selectedAttempt?.getAttribute('aria-selected'), 'true', 'selected attempt has aria-selected=true');
  await mounted.cleanup();
}

async function testClickSelectsRunLevel(): Promise<void> {
  reset();
  const run = makeRun('run-1', 'running', []);
  const work = makeWork({ runs: [run] });
  const selections: RunSelection[] = [];

  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={(s) => selections.push(s)}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  const runHeader = mounted.host.querySelector<HTMLElement>('.wg2-run-header')!;
  await interact(() => runHeader.click());
  eq(selections.length, 1, 'click fires selection once');
  eq(selections[0].runId, 'run-1', 'selection carries runId');
  eq(selections[0].stageId, undefined, 'run-level selection has no stageId');
  await mounted.cleanup();
}

async function testClickSelectsTaskLevel(): Promise<void> {
  reset();
  const task = makeTask('lint', 'running', [makeAttempt(0, 'running')]);
  const stage = makeStage('review', 'running', [task]);
  const run = makeRun('run-1', 'running', [stage]);
  const work = makeWork({ runs: [run] });
  const selections: RunSelection[] = [];

  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={(s) => selections.push(s)}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  const taskEl = mounted.host.querySelector<HTMLElement>('[data-run-task="task-lint"]')!;
  await interact(() => taskEl.click());
  eq(selections[0].taskId, 'task-lint', 'task selection carries taskId');
  eq(selections[0].attemptIndex, 0, 'task selection drills to latest attempt');
  await mounted.cleanup();
}

async function testAttemptClickDoesNotBubbleToParents(): Promise<void> {
  reset();
  const task = makeTask('lint', 'failed', [makeAttempt(0, 'failed')]);
  const stage = makeStage('review', 'failed', [task]);
  const work = makeWork({ runs: [makeRun('run-1', 'failed', [stage])] });
  const selections: RunSelection[] = [];
  const mounted = await mount(
    <RunProgressIndicator work={work as any} onSelect={(selection) => selections.push(selection)} retryByTarget={{}} readonly={false} archived={false} />,
  );
  await interact(() => mounted.host.querySelector<HTMLElement>('[data-run-attempt="0"]')!.click());
  eq(selections.length, 1, 'attempt click emits exactly one selection');
  eq(selections[0].attemptId, 'attempt-0', 'attempt click is not overwritten by Task or Stage bubbling');
  await mounted.cleanup();
}

async function testRetryFiresWithCorrectIntent(): Promise<void> {
  reset();
  const att = makeAttempt(0, 'failed', { error: 'test failure' });
  const task = makeTask('lint', 'failed', [att]);
  const stage = makeStage('review', 'failed', [task]);
  const run = makeRun('run-1', 'failed', [stage]);
  const work = makeWork({ runs: [run] });

  const retries: RetryIntent[] = [];
  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={() => {}}
      onRetry={(intent) => retries.push(intent)}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  const retryBtn = mounted.host.querySelector<HTMLButtonElement>('.wg2-run-retry-button');
  ok(Boolean(retryBtn), 'retry button is visible for failed attempt');
  await interact(() => retryBtn!.click());
  eq(retries.length, 1, 'retry callback fires once');
  eq(retries[0].workId, 'work-1', 'retry intent carries workId');
  eq(retries[0].runId, 'run-1', 'retry intent carries runId');
  eq(retries[0].stageId, 'stage-review', 'retry intent carries stageId');
  eq(retries[0].taskId, 'task-lint', 'retry intent carries taskId');
  eq(retries[0].attemptId, 'attempt-0', 'retry intent carries source attemptId');
  ok(retries[0].requestId.startsWith('retry:'), 'retry intent has stable requestId prefix');
  await mounted.cleanup();
}

function testRetryRequestIDIsDeterministic(): void {
  const source = readFileSync(resolve(process.cwd(), 'src/components/work/RunProgressIndicator.tsx'), 'utf8');
  for (const forbidden of ['randomUUID', 'getRandomValues', 'Math.random', 'Date.now', 'performance.now']) {
    ok(!source.includes(forbidden), `retry requestId does not use ${forbidden}`);
  }
}

async function testRetryDisabledWhenPending(): Promise<void> {
  reset();
  const att = makeAttempt(0, 'failed', { error: 'test failure' });
  const task = makeTask('lint', 'failed', [att]);
  const stage = makeStage('review', 'failed', [task]);
  const run = makeRun('run-1', 'failed', [stage]);
  const work = makeWork({ runs: [run] });

  const retryByTarget: Record<string, RetryStatus> = {
    'work-1\u0000run-1\u0000stage-review\u0000task-lint': {
      state: 'pending',
      intent: { workId: 'work-1', runId: 'run-1', stageId: 'stage-review', taskId: 'task-lint', attemptId: 'attempt-0', attemptIndex: 0, requestId: 'retry:existing' },
    },
  };
  const retries: RetryIntent[] = [];
  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={() => {}}
      onRetry={(intent) => retries.push(intent)}
      retryByTarget={retryByTarget}
      readonly={false}
      archived={false}
    />,
  );

  const retryBtn = mounted.host.querySelector<HTMLButtonElement>('.wg2-run-retry-button');
  ok(retryBtn?.disabled, 'retry button is disabled when pending');
  eq(retryBtn?.textContent?.trim(), '重试中…', 'retry button shows pending text');
  await interact(() => retryBtn!.click());
  eq(retries.length, 0, 'disabled retry does not fire');
  await mounted.cleanup();
}

async function testRetryHiddenInArchivedMode(): Promise<void> {
  reset();
  const att = makeAttempt(0, 'failed', { error: 'test failure' });
  const task = makeTask('lint', 'failed', [att]);
  const stage = makeStage('review', 'failed', [task]);
  const run = makeRun('run-1', 'failed', [stage]);
  const work = makeWork({ runs: [run] });

  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={() => {}}
      retryByTarget={{}}
      readonly={false}
      archived={true}
    />,
  );

  ok(!mounted.host.querySelector('.wg2-run-retry-button'), 'retry button hidden in archived mode');
  await mounted.cleanup();
}

async function testRetryHiddenForNonTerminalTargets(): Promise<void> {
  reset();
  const att = makeAttempt(0, 'running');
  const task = makeTask('lint', 'running', [att]);
  const stage = makeStage('review', 'running', [task]);
  const run = makeRun('run-1', 'running', [stage]);
  const work = makeWork({ runs: [run] });

  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={() => {}}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  ok(!mounted.host.querySelector('.wg2-run-retry-button'), 'retry button hidden for running attempt');
  await mounted.cleanup();
}

async function testRetryOnlyTargetsLatestFailedAttempt(): Promise<void> {
  reset();
  const task = makeTask('lint', 'failed', [makeAttempt(0, 'failed'), makeAttempt(1, 'failed')]);
  const stage = makeStage('review', 'failed', [task]);
  const work = makeWork({ runs: [makeRun('run-1', 'failed', [stage])] });
  const mounted = await mount(
    <RunProgressIndicator work={work as any} onSelect={() => {}} onRetry={() => {}} retryByTarget={{}} readonly={false} archived={false} />,
  );
  eq(mounted.host.querySelectorAll('.wg2-run-retry-button').length, 1, 'only the latest failed Attempt exposes Task retry');
  eq(mounted.host.querySelector('.wg2-run-retry-button')?.getAttribute('aria-label'), '重试 lint 尝试 #2', 'retry targets the latest Attempt');
  await mounted.cleanup();
}

async function testKeyboardNavigation(): Promise<void> {
  reset();
  const task = makeTask('lint', 'running', [makeAttempt(0, 'running')]);
  const stage = makeStage('review', 'running', [task]);
  const run = makeRun('run-1', 'running', [stage]);
  const work = makeWork({ runs: [run] });

  const selections: RunSelection[] = [];
  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={(s) => selections.push(s)}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  // Enter key on run header.
  const runHeader = mounted.host.querySelector<HTMLElement>('.wg2-run-header')!;
  await interact(() => {
    runHeader.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  });
  eq(selections.length, 1, 'Enter key selects run');

  // Space key on task.
  const taskEl = mounted.host.querySelector<HTMLElement>('[data-run-task="task-lint"]')!;
  await interact(() => {
    taskEl.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: ' ', bubbles: true }));
  });
  eq(selections[1].taskId, 'task-lint', 'Space key selects task');

  await mounted.cleanup();
}

async function testAccessibilityRoles(): Promise<void> {
  reset();
  const task = makeTask('lint', 'running', [makeAttempt(0, 'running')]);
  const stage = makeStage('review', 'running', [task]);
  const run = makeRun('run-1', 'running', [stage]);
  const work = makeWork({ runs: [run] });

  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={() => {}}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  // Tree role.
  ok(Boolean(mounted.host.querySelector('[role="tree"]')), 'outer list has tree role');
  ok(Boolean(mounted.host.querySelector('[role="treeitem"]')), 'run items are tree items');
  ok(mounted.host.querySelectorAll('[role="treeitem"]').length >= 4, 'run/stage/task/attempt items are tree items');
  await mounted.cleanup();
}

async function testDeepLinkTargetResolution(): Promise<void> {
  reset();
  const att = makeAttempt(0, 'completed', { sessionRef: makeSessionRef({ modelRef: 'deepseek-v3' }) });
  const task = makeTask('lint', 'completed', [att]);
  const stage = makeStage('review', 'completed', [task]);
  const run = makeRun('run-1', 'completed', [stage]);
  const work = makeWork({ runs: [run] });

  // Structured deep-link target resolves to correct attempt.
  const selection: RunSelection = {
    runId: 'run-1',
    stageId: 'stage-review',
    taskId: 'task-lint',
    attemptId: 'attempt-0',
    attemptIndex: 0,
  };

  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      selection={selection}
      onSelect={() => {}}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  const selected = mounted.host.querySelector('.wg2-run-attempt.wg2-run-selected');
  ok(Boolean(selected), 'deep-link target is selected');
  eq(selected?.getAttribute('data-run-attempt'), '0', 'correct attempt index is selected');
  await mounted.cleanup();
}

async function testSessionSummaryRendersModelAndTurns(): Promise<void> {
  reset();
  const att = makeAttempt(0, 'completed', {
    sessionRef: makeSessionRef({ modelRef: 'deepseek-v3', turnCount: 12, preview: '这里是一些预览文本内容' }),
  });
  const task = makeTask('lint', 'completed', [att]);
  const stage = makeStage('review', 'completed', [task]);
  const run = makeRun('run-1', 'completed', [stage]);
  const work = makeWork({ runs: [run] });

  const mounted = await mount(
    <RunProgressIndicator
      work={work as any}
      onSelect={() => {}}
      retryByTarget={{}}
      readonly={false}
      archived={false}
    />,
  );

  const sessionEl = mounted.host.querySelector('.wg2-run-session');
  ok(Boolean(sessionEl), 'session summary element renders');
  ok(sessionEl?.textContent?.includes('deepseek-v3') ?? false, 'model ref is visible');
  ok(sessionEl?.textContent?.includes('12 轮') ?? false, 'turn count is visible');
  await mounted.cleanup();
}

async function main(): Promise<void> {
  console.log('\nRunProgressIndicator Tests\n');
  await testEmptyStateShowsPlaceholder();
  await testRendersRunStagesTasksAttempts();
  await testSelectionHighlightsTarget();
  await testClickSelectsRunLevel();
  await testClickSelectsTaskLevel();
  await testAttemptClickDoesNotBubbleToParents();
  await testRetryFiresWithCorrectIntent();
  testRetryRequestIDIsDeterministic();
  await testRetryDisabledWhenPending();
  await testRetryHiddenInArchivedMode();
  await testRetryHiddenForNonTerminalTargets();
  await testRetryOnlyTargetsLatestFailedAttempt();
  await testKeyboardNavigation();
  await testAccessibilityRoles();
  await testDeepLinkTargetResolution();
  await testSessionSummaryRendersModelAndTurns();
  console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total\n`);
  if (failed > 0) process.exit(1);
}

void main().catch((error) => {
  console.error(error);
  process.exit(1);
});
