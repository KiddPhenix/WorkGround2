// Run: tsx src/__tests__/block-action-renderers.test.tsx

import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { BlockHost } from '../components/work/blocks/BlockHost';
import { blockRegistry } from '../components/work/blocks/registry';
import type { ActionReceipt, BlockActionRequest, BlockInstance } from '../work/types';

let passed = 0;
let failed = 0;
let root: Root | undefined;
const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
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
  HTMLInputElement: dom.window.HTMLInputElement,
  HTMLTextAreaElement: dom.window.HTMLTextAreaElement,
  HTMLSelectElement: dom.window.HTMLSelectElement,
  SVGElement: dom.window.SVGElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
  KeyboardEvent: dom.window.KeyboardEvent,
  MutationObserver: dom.window.MutationObserver,
  requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
  cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
});
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
Object.defineProperty(globalThis, 'crypto', {
  configurable: true,
  value: {
    randomUUID: () => { throw new Error('entropy API disabled'); },
    getRandomValues: () => { throw new Error('entropy API disabled'); },
  },
});
Object.defineProperty(dom.window, 'sessionStorage', {
  configurable: true,
  get: () => { throw new Error('browser storage disabled'); },
});

function ok(condition: unknown, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed += 1;
  else failed += 1;
}

function equal<T>(actual: T, expected: T, label: string): void {
  ok(Object.is(actual, expected), `${label}${Object.is(actual, expected) ? '' : ` (expected ${String(expected)}, got ${String(actual)})`}`);
}

function host(): HTMLElement {
  return document.getElementById('root')!;
}

async function flush(): Promise<void> {
  await act(async () => { await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
}

async function render(element: React.ReactElement): Promise<void> {
  root ??= createRoot(host(), { onCaughtError: () => undefined });
  await act(async () => { root!.render(element); });
  for (let attempt = 0; attempt < 4; attempt += 1) await flush();
}

async function remount(element: React.ReactElement): Promise<void> {
  if (root) await act(async () => { root!.unmount(); });
  root = undefined;
  host().replaceChildren();
  await render(element);
}

async function click(element: Element): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }));
  });
  await flush();
}

async function fillInput(value: string): Promise<void> {
  const input = host().querySelector('input') as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, 'value')!.set!;
  const previous = input.value;
  await act(async () => {
    setter.call(input, value);
    (input as HTMLInputElement & { _valueTracker?: { setValue: (next: string) => void } })._valueTracker?.setValue(previous);
    input.dispatchEvent(new dom.window.InputEvent('input', { bubbles: true, inputType: 'insertText', data: value }));
    input.dispatchEvent(new dom.window.Event('change', { bubbles: true }));
  });
  await flush();
}

function action(id: string, label = id) {
  return { id, label, intent: `intent:${id}`, risk: 'read' as const, confirmRequired: false };
}

function block(patch: Partial<BlockInstance>): BlockInstance {
  return {
    id: 'block-1',
    kind: 'action_entry',
    schemaVersion: 1,
    revision: 7,
    status: 'ready',
    data: { description: 'Run the action' },
    source: { provider: 'controller', mode: 'snapshot', verified: true },
    fallback: { summary: 'safe fallback' },
    createdAt: '2026-07-21T00:00:00Z',
    updatedAt: '2026-07-21T00:00:00Z',
    ...patch,
  };
}

const context = { workId: 'work-1', workSchemaVersion: 1, runId: 'run-1', taskId: 'task-1' };

function receipt(patch: Partial<ActionReceipt>): ActionReceipt {
  return {
    workId: 'work-1',
    blockId: 'block-1',
    actionId: 'run',
    status: 'pending',
    requestId: 'request-projected',
    retryable: false,
    outcomeKnown: false,
    revision: 8,
    ...patch,
  };
}

const fixtures: Record<string, unknown> = {
  action_entry: { description: 'Run the action', lastResult: 'Previous result' },
  decision: { question: 'Choose one', options: [{ id: 'a', label: 'Alpha' }, { id: 'b', label: 'Beta' }] },
  approval: { title: 'Approve changes', items: [{ id: 'file', label: 'Update file', risk: 'write' }] },
  input: { prompt: 'Provide a name', fields: [{ id: 'name', label: 'Name', required: true }] },
  notice: { level: 'error', content: 'Operation failed', retryable: true },
};

async function run(): Promise<void> {
  process.stdout.write('\n# registry and schemas\n');
  const original = ['item', 'list', 'checklist', 'file_list', 'git_status', 'key_value', 'status', 'progress', 'timeline', 'table', 'chart', 'graph', 'code', 'markdown', 'artifact'];
  const added = ['action_entry', 'decision', 'approval', 'input', 'notice'];
  for (const kind of [...original, ...added]) ok(blockRegistry.has(kind, 1), `${kind} v1 registered`);
  for (const kind of added) {
    ok(blockRegistry.validate(kind, 1, fixtures[kind])?.valid, `${kind} valid fixture accepted`);
    ok(!blockRegistry.validate(kind, 1, {})?.valid, `${kind} empty object rejected`);
  }
  const actionIntentSource = readFileSync(new URL('../components/work/blocks/actionIntent.tsx', import.meta.url), 'utf8');
  for (const forbidden of ['randomUUID', 'getRandomValues', 'Math.random', 'Date.now', 'sessionStorage', 'localStorage']) {
    ok(!actionIntentSource.includes(forbidden), `action intent runtime does not use ${forbidden}`);
  }

  process.stdout.write('\n# action context, duplicate delivery, and replay\n');
  const entry = block({ actions: [action('run', 'Run')] });
  const requests: BlockActionRequest[] = [];
  await render(<BlockHost block={entry} context={context} onAction={(request) => { requests.push(request); }} />);
  const runButton = host().querySelector('.wg2-action-block__btn')!;
  await act(async () => {
    runButton.dispatchEvent(new dom.window.MouseEvent('click', { bubbles: true }));
    runButton.dispatchEvent(new dom.window.MouseEvent('click', { bubbles: true }));
  });
  await flush();
  equal(requests.length, 1, 'same-render double click publishes once');
  equal(requests[0].workId, 'work-1', 'request carries workId');
  equal(requests[0].runId, 'run-1', 'request carries runId');
  equal(requests[0].taskId, 'task-1', 'request carries taskId');
  equal(requests[0].blockId, 'block-1', 'request carries blockId');
  equal(requests[0].expectedRevision, 7, 'request carries exact revision');
  ok(Boolean(requests[0].requestId), 'request carries requestId');

  await remount(<BlockHost block={entry} context={context} onAction={(request) => { requests.push(request); }} />);
  await click(host().querySelector('.wg2-action-block__btn')!);
  equal(requests.length, 2, 'page recovery may safely replay transport intent');
  equal(requests[1].requestId, requests[0].requestId, 'page recovery reuses idempotency requestId');
  equal(new Set(requests.map((request) => request.requestId)).size, 1, 'replay represents one business action');
  ok(/^work-action-v1-sha256-[0-9a-f]{64}$/.test(requests[0].requestId), 'requestId is a deterministic SHA-256 digest');
  equal(requests[0].requestId,
    'work-action-v1-sha256-b6661a66b930b95014322cc56b450c64bc46289dd6ac058f9e093921152c9971',
    'requestId digest matches the canonical intent vector');

  const distinctRequests: BlockActionRequest[] = [];
  const intentCases: Array<[BlockInstance, typeof context]> = [
    [entry, context],
    [entry, { ...context, workId: 'work-2' }],
    [entry, { ...context, runId: 'run-2' }],
    [entry, { ...context, taskId: 'task-2' }],
    [block({ id: 'block-2', actions: [action('run', 'Run')] }), context],
    [block({ revision: 8, actions: [action('run', 'Run')] }), context],
    [block({ actions: [action('inspect', 'Inspect')] }), context],
  ];
  for (const [intentBlock, intentContext] of intentCases) {
    await remount(<BlockHost
      block={intentBlock}
      context={intentContext}
      onAction={(request) => { distinctRequests.push(request); }}
    />);
    await click(host().querySelector('.wg2-action-block__btn')!);
  }
  equal(new Set(distinctRequests.map((request) => request.requestId)).size, intentCases.length,
    'work/run/task/block/action/revision changes cannot merge request IDs');

  process.stdout.write('\n# explicit receipt states and safe retry\n');
  const projectedID = 'receipt-failed';
  await remount(<BlockHost
    block={entry}
    context={{ ...context, actionReceipts: [receipt({ requestId: projectedID, status: 'pending' })] }}
    onAction={() => undefined}
  />);
  equal(host().querySelector('[data-action-status]')?.getAttribute('data-action-status'), 'pending', 'pending projection is visible');
  ok((host().querySelector('.wg2-action-block__btn') as HTMLButtonElement).disabled, 'pending projection disables duplicate action');

  const retryRequests: BlockActionRequest[] = [];
  await render(<BlockHost
    block={entry}
    context={{ ...context, actionReceipts: [receipt({
      requestId: projectedID,
      status: 'failed',
      message: 'temporary controller failure',
      retryable: true,
      outcomeKnown: true,
    })] }}
    onAction={(request) => { retryRequests.push(request); }}
  />);
  ok(host().textContent?.includes('temporary controller failure'), 'failed projection exposes safe error');
  await click(host().querySelector('.wg2-action-feedback__retry')!);
  equal(retryRequests.length, 1, 'retry emits one intent');
  equal(retryRequests[0].requestId, projectedID, 'retry reuses failed requestId');

  await render(<BlockHost
    block={entry}
    context={{ ...context, actionReceipts: [receipt({
      requestId: projectedID,
      status: 'unknown',
      message: 'token=must-not-render C:\\private',
      retryable: true,
      outcomeKnown: false,
    })] }}
    onAction={() => undefined}
  />);
  ok(host().textContent?.includes('Outcome is unknown'), 'unknown outcome is explicit');
  ok(!host().textContent?.includes('must-not-render'), 'unsafe receipt message is redacted');
  ok(!host().querySelector('.wg2-action-feedback__retry'), 'unknown outcome cannot be retried');

  process.stdout.write('\n# terminal guard against late projection\n');
  const completed: ActionReceipt = receipt({
    requestId: requests[0].requestId,
    status: 'succeeded',
    message: 'Completed once',
    retryable: false,
    outcomeKnown: true,
  });
  await remount(<BlockHost block={entry} context={context} onAction={() => completed} />);
  await click(host().querySelector('.wg2-action-block__btn')!);
  ok(host().textContent?.includes('Completed once'), 'synchronous terminal receipt is visible');
  await render(<BlockHost
    block={entry}
    context={{ ...context, actionReceipts: [receipt({ requestId: completed.requestId, status: 'pending' })] }}
    onAction={() => completed}
  />);
  equal(host().querySelector('[data-action-status]')?.getAttribute('data-action-status'), 'succeeded', 'late pending receipt cannot regress terminal state');

  process.stdout.write('\n# structured interaction renderers\n');
  let decisionRequest: BlockActionRequest | undefined;
  const decision = block({ kind: 'decision', data: fixtures.decision, actions: [action('decide', 'Choose')] });
  await remount(<BlockHost block={decision} context={context} onAction={(request) => { decisionRequest = request; }} />);
  ok(Boolean(host().querySelector('.prompt-shelf')), 'decision reuses PromptShelf');
  equal(host().querySelector('.wg2-decision-block__opt-btn')?.getAttribute('role'), 'radio', 'decision exposes radio semantics');
  await click(host().querySelector('.wg2-decision-block__opt-btn')!);
  equal(decisionRequest?.actionId, 'decide', 'decision uses declared action ID');
  equal((decisionRequest?.input?.selected as string[])?.[0], 'a', 'decision sends typed selection');

  let approvalRequest: BlockActionRequest | undefined;
  const approval = block({ kind: 'approval', data: fixtures.approval, actions: [action('approve', 'Approve')] });
  await remount(<BlockHost block={approval} context={context} onAction={(request) => { approvalRequest = request; }} />);
  await click(host().querySelector('.wg2-approval-block__btn')!);
  const approveAction = [...host().querySelectorAll('.prompt-action')].find((element) => element.textContent?.includes('Approve'))!;
  await click(approveAction);
  equal(approvalRequest?.actionId, 'approve', 'approval uses declared action ID');
  equal((approvalRequest?.input?.verdicts as Array<{ verdict: string }>)[0].verdict, 'approved', 'approval sends item verdicts');

  let inputRequest: BlockActionRequest | undefined;
  const inputBlock = block({ kind: 'input', data: fixtures.input, actions: [action('submit_input', 'Submit')] });
  await remount(<BlockHost block={inputBlock} context={context} onAction={(request) => { inputRequest = request; }} />);
  const submit = host().querySelector('.prompt-action')!;
  await click(submit);
  ok(host().textContent?.includes('Name is required'), 'required input failure is explicit');
  await fillInput('Ada');
  await click(submit);
  equal((inputRequest?.input?.values as Record<string, string>)?.name, 'Ada', 'input sends typed values');
  const adaRequestID = inputRequest!.requestId;
  ok(!adaRequestID.includes('Ada'), 'requestId never exposes raw input');

  let graceRequest: BlockActionRequest | undefined;
  await remount(<BlockHost block={inputBlock} context={context} onAction={(request) => { graceRequest = request; }} />);
  await fillInput('Grace');
  await click(host().querySelector('.prompt-action')!);
  ok(graceRequest?.requestId !== adaRequestID, 'different effective input gets a different requestId');

  const conflictRequests: BlockActionRequest[] = [];
  const failedInput = receipt({
    actionId: 'submit_input',
    requestId: adaRequestID,
    status: 'failed',
    message: 'temporary controller failure',
    retryable: true,
    outcomeKnown: true,
  });
  await remount(<BlockHost
    block={inputBlock}
    context={{ ...context, actionReceipts: [failedInput] }}
    onAction={(request) => { conflictRequests.push(request); }}
  />);
  await fillInput('Grace');
  await click(host().querySelector('.prompt-action')!);
  equal(conflictRequests.length, 0, 'failed input cannot replay with conflicting effective payload');
  ok(host().textContent?.includes('Retry input does not match'), 'input replay conflict is explicit');

  await remount(<BlockHost
    block={inputBlock}
    context={{ ...context, actionReceipts: [failedInput] }}
    onAction={(request) => { conflictRequests.push(request); }}
  />);
  await fillInput('Ada');
  await click(host().querySelector('.prompt-action')!);
  equal(conflictRequests.length, 1, 'same failed input can replay after page recovery');
  equal(conflictRequests[0].requestId, adaRequestID, 'failed input replay preserves its original requestId');

  process.stdout.write('\n# notice and artifact intents\n');
  let noticeRequest: BlockActionRequest | undefined;
  const notice = block({ kind: 'notice', data: fixtures.notice, actions: [action('retry', 'Retry')] });
  await remount(<BlockHost block={notice} context={context} onAction={(request) => { noticeRequest = request; }} />);
  await click(host().querySelector('.wg2-notice-block__retry')!);
  equal(noticeRequest?.actionId, 'retry', 'notice retry stays on action-intent path');

  let artifactRequest: BlockActionRequest | undefined;
  const artifact = block({
    kind: 'artifact',
    data: { artifactRef: { id: 'artifact-1', name: 'report.pdf', type: 'pdf', status: 'stale', path: 'C:\\hidden\\report.pdf' } },
    actions: [action('revalidate', 'Revalidate')],
  });
  await remount(<BlockHost block={artifact} context={context} onAction={(request) => { artifactRequest = request; }} />);
  ok(Boolean(host().querySelector('.artifact-item')), 'artifact reuses ArtifactItem presentation');
  equal(host().querySelectorAll('a').length, 0, 'artifact path is never exposed as a direct link');
  ok(!host().textContent?.includes('C:\\hidden'), 'artifact path is not rendered');
  await click(host().querySelector('.wg2-artifact-block__action')!);
  equal(artifactRequest?.actionId, 'revalidate', 'artifact action stays on action-intent path');

  process.stdout.write('\n# readonly, archive, and missing context fail closed\n');
  for (const [label, extra] of [
    ['readonly', { readonly: true }],
    ['archived', { archived: true }],
  ] as const) {
    await remount(<BlockHost block={entry} context={context} onAction={() => undefined} {...extra} />);
    ok([...host().querySelectorAll('button')].every((button) => button.disabled), `${label} disables every action control`);
  }
  await remount(<BlockHost block={entry} context={{ workId: 'work-1', workSchemaVersion: 1 }} onAction={() => undefined} />);
  ok((host().querySelector('.wg2-action-block__btn') as HTMLButtonElement).disabled, 'missing run/task context disables action');
  ok(host().textContent?.includes('Workflow context unavailable'), 'missing context reason is explicit');

  process.stdout.write('\n# invalid schemas fall back independently\n');
  for (const kind of added) {
    await remount(<BlockHost block={block({ kind, data: {} })} context={context} onAction={() => undefined} />);
    ok(host().textContent?.includes('Invalid data for renderer'), `${kind} invalid data uses BlockHost fallback`);
  }
}

void run().catch((error) => {
  process.stdout.write(`\n  ERROR ${error instanceof Error ? error.stack : String(error)}\n`);
  failed += 1;
}).finally(async () => {
  if (root) await act(async () => { root!.unmount(); });
  process.stdout.write(`\nResults: ${passed} passed, ${failed} failed\n`);
  process.exitCode = failed === 0 ? 0 : 1;
});
