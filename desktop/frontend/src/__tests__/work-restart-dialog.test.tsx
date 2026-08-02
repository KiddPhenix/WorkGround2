import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import { WorkRestartDialog } from '../components/work/WorkRestartDialog';
import type { ReusableFlow, ReusableFlowSetup } from '../work/types';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else failed++;
}

const dom = new JSDOM('<!doctype html><html><body></body></html>', { pretendToBeVisual: true, url: 'http://localhost/' });
Object.assign(globalThis, {
  IS_REACT_ACT_ENVIRONMENT: true,
  window: dom.window,
  document: dom.window.document,
  HTMLElement: dom.window.HTMLElement,
  SVGElement: dom.window.SVGElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
  KeyboardEvent: dom.window.KeyboardEvent,
});
Object.assign(dom.window.HTMLElement.prototype, {
  attachEvent: () => undefined,
  detachEvent: () => undefined,
});

async function settle(): Promise<void> {
  await act(async () => { await new Promise<void>((resolve) => setTimeout(resolve, 20)); });
}

async function click(element: Element | null): Promise<void> {
  if (!(element instanceof dom.window.HTMLElement)) throw new Error('click target missing');
  await act(async () => {
    element.click();
    await new Promise<void>((resolve) => setTimeout(resolve, 20));
  });
}

function flow(fields: ReusableFlow['fields']): ReusableFlow {
  return {
    schemaVersion: 1,
    id: 'flow-0123456789abcdef01234567',
    name: '长篇小说创作',
    sourceWorkId: 'work-1',
    fields,
    digest: `sha256:${'0'.repeat(64)}`,
    createdAt: '2026-08-02T00:00:00Z',
  };
}

const fields = [
  { key: 'goal', label: '主题', kind: 'text', required: true, variable: false, value: '海洋小说' },
  { key: 'input:chapters', label: '章节数', kind: 'number', required: true, variable: false, value: 20 },
];

async function scenario(existing = false): Promise<{
  host: HTMLDivElement;
  calls: { restart: number; prepare: number; save: number; create: number; savedKeys: string[]; values?: Record<string, unknown> };
  cleanup: () => Promise<void>;
}> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  const calls = { restart: 0, prepare: 0, save: 0, create: 0, savedKeys: [] as string[], values: undefined as Record<string, unknown> | undefined };
  const setup: ReusableFlowSetup = {
    ...(existing ? { existing: flow(fields.map((field) => ({ ...field, variable: true }))) } : {}),
    suggestedName: '长篇小说创作',
    fields,
    fixedItems: ['工作结构', '工具与执行方式', '成果格式'],
  };
  await act(async () => {
    root.render(
      <WorkRestartDialog
        open
        workId="work-1"
        workName="长篇小说创作"
        runId="run-1"
        onClose={() => undefined}
        onRestartCurrent={async () => { calls.restart++; }}
        onPrepareFlow={async () => { calls.prepare++; return setup; }}
        onSaveFlow={async (input) => {
          calls.save++;
          calls.savedKeys = input.variableKeys;
          return flow(fields.map((field) => ({ ...field, variable: input.variableKeys.includes(field.key) })));
        }}
        onCreateSession={async (input) => {
          calls.create++;
          calls.values = input.values;
          return {
            tabMeta: { id: 'tab-2' },
            run: { flow: flow(fields), work: { id: 'work-2' } as never, duplicate: false },
            duplicate: false,
            recoverable: false,
          };
        }}
      />,
    );
  });
  await settle();
  return {
    host,
    calls,
    cleanup: async () => {
      await act(async () => root.unmount());
      host.remove();
    },
  };
}

async function main(): Promise<void> {
  const restart = await scenario();
  ok(Boolean(restart.host.querySelector('[data-testid="restart-current-choice"]')), 'shows restart-current choice');
  ok(Boolean(restart.host.querySelector('[data-testid="save-and-rerun-choice"]')), 'shows save-and-rerun choice');
  await click(restart.host.querySelector('[data-testid="restart-current-choice"]'));
  ok(restart.calls.restart === 1 && restart.calls.prepare === 0, 'restart choice only restarts the current Work');
  await restart.cleanup();

  const firstSave = await scenario();
  await click(firstSave.host.querySelector('[data-testid="save-and-rerun-choice"]'));
  ok(Boolean(firstSave.host.querySelector('[data-testid="reusable-flow-name"]')), 'first reuse opens save dialog');
  ok(firstSave.host.textContent?.includes('工作结构') === true, 'save dialog shows fixed flow content');
  await click(firstSave.host.querySelector('button[type="submit"]'));
  ok(firstSave.calls.save === 1 && firstSave.calls.savedKeys.length === 2, 'save persists selected variable fields');
  ok(firstSave.host.textContent?.includes('再次运行') === true, 'save advances to rerun inputs');
  await click(firstSave.host.querySelector('button[type="submit"]'));
  ok(firstSave.calls.create === 1, 'rerun creates a separate Work Session');
  ok(firstSave.calls.values?.goal === '海洋小说' && firstSave.calls.values?.['input:chapters'] === 20, 'rerun submits typed changing values');
  await firstSave.cleanup();

  const saved = await scenario(true);
  await click(saved.host.querySelector('[data-testid="save-and-rerun-choice"]'));
  ok(saved.calls.prepare === 1 && saved.calls.save === 0, 'existing flow skips save mutation');
  ok(!saved.host.querySelector('[data-testid="reusable-flow-name"]') && saved.host.textContent?.includes('再次运行') === true, 'existing flow opens rerun dialog directly');
  await saved.cleanup();

  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

void main();
