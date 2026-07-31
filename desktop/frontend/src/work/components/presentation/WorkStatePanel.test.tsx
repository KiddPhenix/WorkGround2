import { JSDOM } from 'jsdom';
import { act } from 'react';
import { createRoot } from 'react-dom/client';

import type {
  PresentationTask,
  WorkPresentation,
  WorkPresentationPhase,
} from '../../presentation';
import { WorkStatePanel } from './WorkStatePanel';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed += 1;
  else failed += 1;
}

function eq<T>(actual: T, expected: T, label: string): void {
  ok(
    actual === expected,
    `${label}${actual === expected ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`,
  );
}

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
});

function makeTask(overrides: Partial<PresentationTask> = {}): PresentationTask {
  return {
    id: 'task-primary',
    runId: 'run-1',
    nodeId: 'node-primary',
    order: 0,
    title: '整理当前输入',
    state: 'running',
    retryable: false,
    updatedAt: '2026-07-30T08:00:00Z',
    source: 'runtime',
    ...overrides,
  };
}

function makePresentation(
  phase: WorkPresentationPhase,
  overrides: Partial<WorkPresentation> = {},
): WorkPresentation {
  return {
    workId: 'work-1',
    definitionRevision: 1,
    runId: 'run-1',
    phase,
    layoutMode: phase === 'completed' ? 'results' : 'balanced',
    tasks: [],
    artifactSlots: [],
    artifacts: {
      total: 0,
      required: 0,
      ready: 0,
      requiredReady: 0,
      reserved: 0,
      generating: 0,
      partial: 0,
      failed: 0,
      stale: 0,
      artifactCount: 0,
      allRequiredReady: true,
    },
    ...overrides,
  };
}

async function renderPanel(presentation: WorkPresentation) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  const focused: string[] = [];

  await act(async () => {
    root.render(
      <WorkStatePanel
        presentation={presentation}
        onFocusTask={(taskId) => focused.push(taskId)}
      />,
    );
  });

  return {
    host,
    focused,
    cleanup: async () => {
      await act(async () => root.unmount());
      host.remove();
    },
  };
}

async function runTests(): Promise<void> {
  const phaseCases: Array<[WorkPresentationPhase, string]> = [
    ['planning', '正在整理工作'],
    ['running', '工作正在推进'],
    ['paused', '工作已暂停'],
    ['waiting', '需要你的回应'],
    ['failed', '工作遇到问题'],
    ['completed', '工作已经完成'],
  ];

  for (const [phase, title] of phaseCases) {
    const view = await renderPanel(makePresentation(phase));
    const panel = view.host.querySelector('[data-testid="work-state-panel"]');
    eq(panel?.getAttribute('data-phase'), phase, `${phase}: exposes phase`);
    ok(view.host.textContent?.includes(title) === true, `${phase}: renders status title`);

    if (phase === 'completed') {
      ok(
        view.host.querySelector('[data-testid="work-state-panel-completion"]') !== null,
        'completed: renders summary',
      );
    } else {
      ok(
        view.host.querySelector('[data-testid="work-state-panel-empty"]') !== null,
        `${phase}: renders useful taskless state`,
      );
    }

    await view.cleanup();
  }

  {
    const primary = makeTask({ progress: '已完成资料收集' });
    const view = await renderPanel(
      makePresentation('paused', {
        tasks: [primary],
        primaryTask: primary,
      }),
    );
    eq(
      view.host.querySelector('[data-testid="work-state-panel-primary-task"]')
        ?.querySelector('.work-state-panel__task-label')?.textContent,
      '暂停于',
      'paused: current task is labeled as the pause point',
    );
    ok(!view.host.querySelector('.work-state-panel--running'), 'paused: does not use the spinning running phase');
    await view.cleanup();
  }

  {
    const attention = makeTask({
      id: 'task-attention',
      title: '确认缺少的信息',
      state: 'waiting_input',
      progress: '已完成 2/3 项',
    });
    const primary = makeTask({
      id: 'task-primary',
      title: '继续生成结果',
      error: '上次处理未完成',
    });
    const view = await renderPanel(
      makePresentation('waiting', {
        tasks: [attention, primary],
        attentionTask: attention,
        primaryTask: primary,
      }),
    );

    ok(
      view.host.querySelector('[data-testid="work-state-panel-attention-task"]') !== null,
      'tasks: renders attention task',
    );
    ok(
      view.host.querySelector('[data-testid="work-state-panel-primary-task"]') !== null,
      'tasks: renders distinct primary task',
    );
    eq(
      view.host.querySelector('[data-testid="work-state-panel-attention-progress"]')?.textContent,
      '已完成 2/3 项',
      'tasks: renders progress',
    );
    eq(
      view.host.querySelector('[data-testid="work-state-panel-primary-error"]')?.textContent,
      '上次处理未完成',
      'tasks: renders error',
    );

    const button = view.host.querySelector<HTMLButtonElement>(
      '[data-testid="work-state-panel-attention-focus"]',
    );
    await act(async () => button?.click());
    eq(view.focused[0], 'task-attention', 'tasks: focus action returns stable task id');

    await view.cleanup();
  }

  {
    const task = makeTask({ id: 'task-shared', state: 'waiting_approval' });
    const view = await renderPanel(
      makePresentation('waiting', {
        tasks: [task],
        attentionTask: task,
        primaryTask: task,
      }),
    );

    eq(
      view.host.querySelectorAll('[data-task-id="task-shared"]').length,
      1,
      'tasks: shared attention and primary task renders once',
    );
    await view.cleanup();
  }

  {
    const task = makeTask({
      id: 'task-input',
      title: '设计方案',
      state: 'waiting_input',
      waitingInputIds: ['team_size', 'budget'],
    });
    const host = document.createElement('div');
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <WorkStatePanel
          presentation={makePresentation('waiting', {
            tasks: [task],
            attentionTask: task,
            primaryTask: task,
          })}
          pendingInputSpecs={[
            {
              id: 'team_size',
              label: '参与人数是多少？',
              kind: 'number',
              required: true,
              pinEligible: false,
            },
            {
              id: 'budget',
              label: '总预算是多少？',
              kind: 'number',
              required: true,
              pinEligible: false,
            },
          ]}
          onFocusTask={() => undefined}
        />,
      );
    });
    ok(
      host.querySelector('[data-testid="work-state-panel-input-context"]') !== null,
      'waiting input: fills the third status column with pending information',
    );
    ok(host.textContent?.includes('2 项信息待填写') === true, 'waiting input: exposes pending count');
    await act(async () => root.unmount());
    host.remove();
  }

  {
    const task = makeTask({
      id: 'definition:work-1:1:node-planned',
      state: 'pending',
      source: 'definition',
    });
    const view = await renderPanel(
      makePresentation('planning', {
        tasks: [task],
        primaryTask: task,
      }),
    );

    ok(
      view.host.querySelector('[data-testid="work-state-panel-primary-focus"]') === null,
      'planning: synthetic task does not expose a dead focus action',
    );
    await view.cleanup();
  }

  {
    const tasks = [
      makeTask({ id: 'task-1', state: 'completed' }),
      makeTask({ id: 'task-2', state: 'completed' }),
    ];
    const view = await renderPanel(
      makePresentation('completed', {
        tasks,
        artifacts: {
          ...makePresentation('completed').artifacts,
          total: 3,
          required: 2,
          ready: 3,
          requiredReady: 2,
          artifactCount: 3,
        },
      }),
    );

    const summary = view.host.querySelector('[data-testid="work-state-panel-completion"]');
    ok(summary?.textContent?.includes('2 项任务') === true, 'completed: counts completed tasks');
    ok(summary?.textContent?.includes('3/3 项成果就绪') === true, 'completed: counts ready artifacts');
    await view.cleanup();
  }

  {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <>
          <WorkStatePanel presentation={makePresentation('running')} onFocusTask={() => undefined} />
          <WorkStatePanel presentation={makePresentation('waiting')} onFocusTask={() => undefined} />
        </>,
      );
    });

    const panels = [...host.querySelectorAll('[data-testid="work-state-panel"]')];
    const titleIds = panels.map((panel) => panel.getAttribute('aria-labelledby'));
    ok(
      Boolean(titleIds[0]) && titleIds[0] !== titleIds[1],
      'multiple panels: title associations use unique ids',
    );

    await act(async () => root.unmount());
    host.remove();
  }

  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

runTests().catch((error: unknown) => {
  console.error(error);
  process.exit(1);
});
