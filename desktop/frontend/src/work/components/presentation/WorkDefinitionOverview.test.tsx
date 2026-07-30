import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { JSDOM } from 'jsdom';

import type { PresentationTask } from '../../presentation';
import type { WorkDefinitionRevision, WorkInput } from '../../types_v2';
import { WorkDefinitionOverview } from './WorkDefinitionOverview';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed += 1;
  else failed += 1;
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
});

const definition: WorkDefinitionRevision = {
  workId: 'work-1',
  revision: 3,
  parentRevision: 2,
  status: 'active',
  goal: '制定活动方案',
  nodes: [{
    id: 'plan',
    title: '设计方案',
    description: '根据约束整理候选方案',
    inputSpecIds: ['size', 'type'],
    producesSlotIds: ['plan'],
  }],
  artifactSlots: [{
    id: 'plan',
    title: '方案文档',
    kind: 'document',
    expectedCount: 1,
    required: true,
  }],
  inputSpecs: [
    {
      id: 'size',
      label: '参与人数',
      kind: 'number',
      required: true,
      pinEligible: false,
    },
    {
      id: 'type',
      label: '活动类型',
      kind: 'multi_choice',
      required: true,
      valueSchema: {
        options: [
          { value: 'indoor', label: '室内' },
          { value: 'dining', label: '聚餐' },
        ],
      },
      pinEligible: false,
    },
  ],
  createdBy: 'test',
  createdAt: '2026-07-30T08:00:00Z',
  digest: 'sha256:test',
};

const inputs: WorkInput[] = [
  {
    id: 'input-old',
    workId: 'work-1',
    runId: 'run-old',
    taskId: 'task-old',
    blockId: 'block-plan',
    specId: 'size',
    value: 99,
    state: 'submitted',
    revision: 1,
    updatedAt: '2026-07-30T07:00:00Z',
  },
  {
    id: 'input-current',
    workId: 'work-1',
    runId: 'run-current',
    taskId: 'task-current',
    blockId: 'block-plan',
    specId: 'type',
    value: ['indoor', 'dining'],
    state: 'submitted',
    revision: 1,
    updatedAt: '2026-07-30T08:00:00Z',
  },
];

const tasks: PresentationTask[] = [{
  id: 'task-current',
  runId: 'run-current',
  nodeId: 'plan',
  order: 0,
  title: '设计方案',
  state: 'waiting_input',
  retryable: false,
  updatedAt: '2026-07-30T08:00:00Z',
  source: 'runtime',
}];

async function runTests(): Promise<void> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <WorkDefinitionOverview
        definition={definition}
        inputs={inputs}
        runId="run-current"
        tasks={tasks}
      />,
    );
  });

  ok(host.querySelector('[data-testid="work-definition-overview"]') !== null, 'renders generic overview');
  ok(host.textContent?.includes('1/2 已填写') === true, 'counts only current-run values');
  ok(host.textContent?.includes('室内、聚餐') === true, 'maps choice values to labels');
  ok(host.textContent?.includes('99') === false, 'does not leak an older run value');
  ok(host.textContent?.includes('等待信息') === true, 'shows runtime state without domain rules');
  ok(host.textContent?.includes('根据约束整理候选方案') === true, 'shows definition structure');

  await act(async () => root.unmount());
  host.remove();
  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

runTests().catch((error: unknown) => {
  console.error(error);
  process.exit(1);
});
