import React from 'react';
import { JSDOM } from 'jsdom';
import { act } from 'react';
import { createRoot } from 'react-dom/client';

import { WorkRunEntry } from '../components/work/WorkRunEntry';
import { useWorkStore } from '../work/store';
import type { WorkView } from '../work/types';
import type { WorkDefinitionRevision } from '../work/types_v2';

const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost' });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  HTMLElement: dom.window.HTMLElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
  IS_REACT_ACT_ENVIRONMENT: true,
});
Object.defineProperty(globalThis, 'navigator', { value: dom.window.navigator, configurable: true });

function ok(value: unknown, message: string): asserts value {
  if (!value) throw new Error(message);
}

const view = {
  schemaVersion: 2,
  revision: 8,
  assessment: { state: 'ready', blocking: false, degraded: false },
  work: {
    id: 'work-v2-recovery',
    schemaVersion: 2,
    state: 'running',
    prompt: 'write a novel',
    runs: [{
      id: 'run-1',
      workId: 'work-v2-recovery',
      state: 'pending',
      stages: [],
      definitionDigest: 'digest',
      startedAt: '2026-07-26T00:00:00Z',
    }],
  },
  tasks: [{
    id: 'task-1',
    runId: 'run-1',
    nodeId: 'write',
    title: '创作小说',
    state: 'completed',
    retryable: false,
    updatedAt: '2026-07-26T00:00:00Z',
  }],
  artifactSlots: [{
    id: 'novel',
    workId: 'work-v2-recovery',
    definitionRev: 2,
    title: '小说正文',
    kind: 'text',
    expectedCount: 1,
    required: true,
    state: 'reserved',
    artifactRefs: [],
    revision: 1,
  }],
} as WorkView;

const definition = {
  workId: view.work.id,
  revision: 2,
  status: 'active',
  goal: 'write a novel',
  nodes: [{ id: 'write', title: '创作小说', producesSlotIds: ['novel'] }],
  artifactSlots: [{ id: 'novel', title: '小说正文', kind: 'text', expectedCount: 1, required: true }],
  inputSpecs: [],
  createdBy: 'test',
  createdAt: '2026-07-26T00:00:00Z',
  digest: 'digest',
} as WorkDefinitionRevision;

useWorkStore.setState({ works: { [view.work.id]: view } });
const host = document.createElement('div');
document.body.appendChild(host);
const root = createRoot(host);
const calls: Array<{ slotId: string; definitionRevision: number }> = [];

await act(async () => {
  root.render(<WorkRunEntry
    workId={view.work.id}
    v2Definition={definition}
    onV2ArtifactRetry={(intent) => {
      calls.push({ slotId: intent.slotId, definitionRevision: intent.definitionRevision });
    }}
  />);
});

const button = [...host.querySelectorAll('button')]
  .find((candidate) => candidate.textContent?.includes('生成缺失成果'));
ok(button, '卡死 V2 Work 应显示“生成缺失成果”入口');
ok(!host.textContent?.includes('运行中'), '卡死 V2 Work 不应继续显示运行中');

await act(async () => {
  button.click();
  await Promise.resolve();
});
ok(calls.length === 1, '恢复入口应只派发一次');
ok(calls[0].slotId === 'novel' && calls[0].definitionRevision === 2, '恢复入口应携带成果完整身份');

await act(async () => root.unmount());
host.remove();
console.log('work-v2-recovery-entry: PASS');
