import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import { LinkedSessionCard } from '../components/work/LinkedSessionCard';
import type { SessionRef, SessionSurfaceContext } from '../work/types';

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
  pretendToBeVisual: true,
  url: 'http://localhost/',
});
Object.assign(globalThis, {
  IS_REACT_ACT_ENVIRONMENT: true,
  window: dom.window,
  document: dom.window.document,
  HTMLElement: dom.window.HTMLElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
});

const host = document.getElementById('root');
if (!host) throw new Error('missing test root');

const sessionRef: SessionRef = {
  sessionPath: 'D:/repo/sessions/work-task.jsonl',
  branchId: 'work-task',
  modelRef: 'test-model',
  turnCount: 2,
  preview: 'linked session',
  startedAt: '2026-07-31T10:00:00Z',
};
const context: SessionSurfaceContext = {
  workId: 'work-1',
  runId: 'run-1',
  stageId: 'stage-1',
  taskId: 'task-1',
  attemptId: 'attempt-1',
  attemptIndex: 1,
};

let calls = 0;
let fail = true;
const onNavigate = async (target: SessionRef): Promise<void> => {
  calls++;
  if (target.sessionPath !== sessionRef.sessionPath) throw new Error('unexpected target');
  if (fail) throw new Error('injected open failure');
};

const root = createRoot(host);

// Mount: no automatic navigation.
await act(async () => {
  root.render(<LinkedSessionCard sessionRef={sessionRef} context={context} onNavigate={onNavigate} />);
});
if (calls !== 0) throw new Error(`mount navigation calls = ${calls}, want 0`);
if (host.querySelector('[data-testid="linked-session-error"]')) {
  throw new Error('mount should not show an error when no navigation is attempted');
}

// Rerender with same path: still no automatic navigation.
await act(async () => {
  root.render(<LinkedSessionCard sessionRef={{ ...sessionRef }} context={context} onNavigate={onNavigate} />);
});
if (calls !== 0) throw new Error(`rerender navigation calls = ${calls}, want 0`);

// Explicit "打开关联会话" button: navigate fails, error is visible.
await act(async () => {
  host.querySelector<HTMLButtonElement>('[data-testid="linked-session-navigate"]')?.click();
});
if (calls !== 1) throw new Error(`explicit navigate calls = ${calls}, want 1`);
if (!host.querySelector('[data-testid="linked-session-error"]')?.textContent?.includes('injected open failure')) {
  throw new Error('explicit navigation failure is not visible');
}

// Retry button: navigate now succeeds, error is cleared.
fail = false;
await act(async () => {
  host.querySelector<HTMLButtonElement>('[data-testid="linked-session-retry"]')?.click();
});
if (calls !== 2) throw new Error(`retry calls = ${calls}, want 2`);
if (host.querySelector('[data-testid="linked-session-error"]')) throw new Error('successful retry kept stale error');

await act(async () => root.unmount());
dom.window.close();
process.stdout.write('\nLinkedSessionCard: 8 passed, 0 failed\n');
