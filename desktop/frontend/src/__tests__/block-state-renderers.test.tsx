// Run: tsx src/__tests__/block-state-renderers.test.tsx
//
// Tests for all core V1 block renderers: item/list, checklist, file_list,
// git_status, key_value/status, progress/timeline.
// Covers: valid/invalid schema, empty states, readonly/archive, fallback,
// keyboard/ARIA, checklist revision conflicts, draft preservation, retry,
// and file/git zero host side effects.

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { BlockHost } from '../components/work/blocks/BlockHost';
import { FallbackBlock } from '../components/work/blocks/FallbackBlock';
import { blockRegistry, createRegistry } from '../components/work/blocks/registry';
import type { BlockRendererProps, RendererModule } from '../components/work/blocks/types';
import type { BlockInstance } from '../work/types';
import {
  validateChecklist,
  validateFileList,
  validateGitStatus,
  validateItemList,
  validateKeyValue,
  validateProgress,
} from '../components/work/blocks/schemas';

const reportError = console.error.bind(console);

let passed = 0;
let failed = 0;
let root: Root | null = null;
let dom: JSDOM;

// ── helpers ──────────────────────────────────────────────────────────

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  condition ? passed++ : failed++;
}

function equal<T>(actual: T, expected: T, label: string): void {
  ok(Object.is(actual, expected), `${label}${Object.is(actual, expected) ? '' : ` (expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)})`}`);
}

function contains(value: string, part: string, label: string): void {
  ok(value.includes(part), `${label}${value.includes(part) ? '' : ` (missing ${JSON.stringify(part)})`}`);
}

function notContains(value: string, part: string, label: string): void {
  ok(!value.includes(part), `${label}${value.includes(part) ? ` (found forbidden ${JSON.stringify(part)})` : ''}`);
}

function setupDom(): void {
  dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
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
}

function container(): HTMLElement {
  return document.getElementById('root')!;
}

async function flush(): Promise<void> {
  await act(async () => { await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
}

async function render(element: React.ReactElement): Promise<void> {
  if (!root) root = createRoot(container(), { onCaughtError: () => undefined });
  await act(async () => { root!.render(element); });
  await flush();
}

async function waitFor(predicate: () => boolean, label: string, ticks = 40): Promise<void> {
  for (let index = 0; index < ticks; index++) {
    if (predicate()) return;
    await flush();
  }
  ok(false, `${label} timed out`);
}

async function click(element: Element): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }));
  });
  await flush();
}

async function keyDown(element: Element, key: string): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
  });
  await flush();
}

async function change(element: HTMLInputElement): Promise<void> {
  await act(async () => {
    element.dispatchEvent(new dom.window.Event('change', { bubbles: true }));
  });
  await flush();
}

const context = { workId: 'work-test', workSchemaVersion: 1 };

function block(patch: Partial<BlockInstance> = {}): BlockInstance {
  return {
    id: 'block-1',
    kind: 'item',
    schemaVersion: 1,
    revision: 1,
    status: 'ready',
    data: { items: [] },
    source: { provider: 'controller', ref: 'snapshot-1', mode: 'snapshot', verified: true },
    fallback: { summary: 'Block fallback summary' },
    createdAt: '2026-07-21T10:00:00Z',
    updatedAt: '2026-07-21T11:00:00Z',
    ...patch,
  };
}

// ── schema validation tests ──────────────────────────────────────────

async function testSchemas(): Promise<void> {
  console.log('\n-- schema validation: item/list');
  ok(validateItemList(1, { items: [{ id: 'a', title: 'Test' }] }).valid, 'item: valid minimal item');
  ok(!validateItemList(1, null).valid, 'item: null rejected');
  ok(!validateItemList(1, undefined).valid, 'item: undefined rejected');
  ok(!validateItemList(1, []).valid, 'item: array rejected (not plain object)');
  ok(!validateItemList(1, {}).valid, 'item: missing items rejected');
  ok(!validateItemList(1, { items: null }).valid, 'item: null items rejected');
  ok(!validateItemList(1, { items: [{ id: '', title: 'x' }] }).valid, 'item: empty id rejected');
  ok(!validateItemList(1, { items: [{ id: 'a' }] }).valid, 'item: missing title rejected');
  ok(!validateItemList(1, { items: [{ id: 'a', title: 'x', state: 'bogus' }] }).valid, 'item: invalid state rejected');
  ok(!validateItemList(1, { items: [{ id: 'a', title: 'x' }, { id: 'a', title: 'y' }] }).valid, 'item: duplicate id rejected');
  ok(validateItemList(1, { items: [{ id: 'a', title: 'x', state: 'success', detail: 'desc' }] }).valid, 'item: full valid');
  ok(validateItemList(1, { items: [] }).valid, 'item: empty items array is valid');

  console.log('\n-- schema validation: checklist');
  ok(validateChecklist(1, { items: [{ id: 'a', text: 'Do it', checked: false }] }).valid, 'checklist: valid minimal');
  ok(!validateChecklist(1, { items: [{ id: 'a', text: 'x' }] }).valid, 'checklist: missing checked rejected');
  ok(!validateChecklist(1, { items: [{ id: 'a', text: 'x', checked: 'yes' }] }).valid, 'checklist: non-boolean checked rejected');
  ok(!validateChecklist(1, { items: [{ id: 'a', text: 'x', checked: false }, { id: 'a', text: 'y', checked: true }] }).valid, 'checklist: duplicate id rejected');
  ok(validateChecklist(1, { items: [{ id: 'a', text: 'x', checked: true, detail: 'desc' }] }).valid, 'checklist: full valid');

  console.log('\n-- schema validation: file_list');
  ok(validateFileList(1, { files: [{ path: 'src/main.ts', status: 'modified' }] }).valid, 'file_list: valid minimal');
  ok(!validateFileList(1, { files: [{ path: 'x', status: 'bogus' }] }).valid, 'file_list: invalid status rejected');
  ok(!validateFileList(1, { files: [{ status: 'added' }] }).valid, 'file_list: missing path rejected');
  ok(!validateFileList(1, { files: [{ path: 'a', status: 'added' }, { path: 'a', status: 'modified' }] }).valid, 'file_list: duplicate path rejected');
  ok(validateFileList(1, { files: [{ path: 'a', status: 'added', digest: 'abc', desc: 'desc' }] }).valid, 'file_list: full valid');

  console.log('\n-- schema validation: git_status');
  ok(validateGitStatus(1, { branch: 'main', changes: [] }).valid, 'git_status: valid minimal');
  ok(!validateGitStatus(1, { changes: [] }).valid, 'git_status: missing branch rejected');
  ok(!validateGitStatus(1, { branch: '', changes: [] }).valid, 'git_status: empty branch rejected');
  ok(!validateGitStatus(1, { branch: 'main', changes: [{ file: 'x', type: 'bogus' }] }).valid, 'git_status: invalid type rejected');
  ok(validateGitStatus(1, { branch: 'main', changes: [{ file: 'a', staged: true, type: 'modified' }] }).valid, 'git_status: full valid');

  console.log('\n-- schema validation: key_value');
  ok(validateKeyValue(1, { items: [{ key: 'env', label: 'Env', value: 'prod' }] }).valid, 'key_value: valid minimal');
  ok(!validateKeyValue(1, { items: [{ key: 'a', label: 'b' }] }).valid, 'key_value: missing value rejected');
  ok(!validateKeyValue(1, { items: [{ key: 'a', label: 'b', value: 'c', state: 'bogus' }] }).valid, 'key_value: invalid state rejected');
  ok(!validateKeyValue(1, { items: [{ key: 'a', label: 'A', value: '1' }, { key: 'a', label: 'B', value: '2' }] }).valid, 'key_value: duplicate key rejected');

  console.log('\n-- schema validation: progress/timeline');
  ok(validateProgress(1, { items: [{ id: 's1', label: 'Setup', state: 'completed' }] }).valid, 'progress: valid minimal');
  ok(!validateProgress(1, { items: [{ id: 'a', label: 'b', state: 'bogus' }] }).valid, 'progress: invalid state rejected');
  ok(!validateProgress(1, { items: [{ id: 'a', state: 'pending' }] }).valid, 'progress: missing label rejected');
  ok(!validateProgress(1, { items: [{ id: 'a', label: 'A', state: 'pending' }, { id: 'a', label: 'B', state: 'completed' }] }).valid, 'progress: duplicate id rejected');
}

// ── renderer tests (with JSDOM) ──────────────────────────────────────

async function testRenderers(): Promise<void> {
  console.log('\n-- production registration');
  for (const kind of ['item', 'list', 'checklist', 'file_list', 'git_status', 'key_value', 'status', 'progress', 'timeline']) {
    ok(blockRegistry.has(kind, 1), `${kind} is registered by the production BlockHost entry`);
  }

  console.log('\n-- empty data rendering');
  for (const { kind, data, expected } of [
    { kind: 'item', data: { items: [] }, expected: 'No items' },
    { kind: 'list', data: { items: [] }, expected: 'No items' },
    { kind: 'checklist', data: { items: [] }, expected: 'No checklist items' },
    { kind: 'file_list', data: { files: [] }, expected: 'No files' },
    { kind: 'git_status', data: { branch: 'main', changes: [] }, expected: 'Working tree clean' },
    { kind: 'key_value', data: { items: [] }, expected: 'No entries' },
    { kind: 'status', data: { items: [] }, expected: 'No entries' },
    { kind: 'progress', data: { items: [] }, expected: 'No stages' },
    { kind: 'timeline', data: { items: [] }, expected: 'No stages' },
  ]) {
    await render(<BlockHost
      block={block({ kind, data })}
      context={context}
    />);
    await waitFor(
      () => (container().textContent ?? '').includes(expected),
      `${kind} empty state shows "${expected}"`,
    );
  }

  console.log('\n-- populated data rendering');
  await render(<BlockHost
    block={block({
      kind: 'item',
      data: { items: [{ id: 'i1', title: 'First', state: 'success', detail: 'Done' }] },
    })}
    context={context}
  />);
  await waitFor(() => (container().textContent ?? '').includes('First'), 'item renders title');
  contains(container().textContent ?? '', 'Done', 'item renders detail');
  ok(container().querySelector('.wg2-item-state-success') !== null, 'item has state class');

  await render(<BlockHost
    block={block({
      kind: 'list',
      data: { items: [{ id: 'a', title: 'A' }, { id: 'b', title: 'B' }] },
    })}
    context={context}
  />);
  await waitFor(() => container().querySelectorAll('.wg2-list-item').length >= 2, 'list renders multiple items');

  await render(<BlockHost
    block={block({
      kind: 'checklist',
      data: { items: [{ id: 'c1', text: 'Task 1', checked: false, detail: 'Details' }] },
    })}
    context={context}
  />);
  await waitFor(() => container().querySelector('input[type="checkbox"]') !== null, 'checklist has checkbox');
  const cb = container().querySelector('input[type="checkbox"]') as HTMLInputElement;
  ok(!cb.checked, 'checkbox starts unchecked');
  contains(container().textContent ?? '', 'Details', 'checklist detail renders');

  await render(<BlockHost
    block={block({
      kind: 'file_list',
      data: { files: [{ path: 'src/index.ts', status: 'modified' }] },
    })}
    context={context}
  />);
  await waitFor(() => (container().textContent ?? '').includes('src/index.ts'), 'file_list renders path');
  contains(container().textContent ?? '', 'modified', 'file_list shows status');

  await render(<BlockHost
    block={block({
      kind: 'git_status',
      data: { branch: 'feature/x', changes: [{ file: 'a.ts', staged: true, type: 'added' }] },
    })}
    context={context}
  />);
  await waitFor(() => (container().textContent ?? '').includes('feature/x'), 'git_status renders branch');
  contains(container().textContent ?? '', 'staged', 'git_status shows staged');

  await render(<BlockHost
    block={block({
      kind: 'key_value',
      data: { items: [{ key: 'NODE_ENV', label: 'Environment', value: 'production', state: 'success' }] },
    })}
    context={context}
  />);
  await waitFor(() => (container().textContent ?? '').includes('production'), 'key_value renders value');
  contains(container().textContent ?? '', 'Environment', 'key_value renders label');

  await render(<BlockHost
    block={block({
      kind: 'progress',
      data: { items: [
        { id: 's1', label: 'Init', state: 'completed' },
        { id: 's2', label: 'Build', state: 'in_progress' },
        { id: 's3', label: 'Deploy', state: 'pending' },
      ]},
    })}
    context={context}
  />);
  await waitFor(() => container().querySelector('progress') !== null, 'progress shows progress element');
  contains(container().textContent ?? '', 'Build', 'progress renders label');
  contains(container().textContent ?? '', 'Deploy', 'progress renders pending item');

  console.log('\n-- git_status archived indicator');
  await render(<BlockHost
    block={block({
      kind: 'git_status',
      data: { branch: 'main', changes: [{ file: 'x.ts', staged: false, type: 'modified' }] },
    })}
    context={context}
    archived
  />);
  await waitFor(() => (container().textContent ?? '').includes('Archived'), 'archived git_status shows [Archived]');
  contains(container().textContent ?? '', 'frozen', 'archived git_status shows frozen footer');

  console.log('\n-- readonly mode and failed status cover every kind');
  const validCases: Array<{ kind: string; data: unknown; expected: string }> = [
    { kind: 'item', data: { items: [{ id: 'i', title: 'readonly item' }] }, expected: 'readonly item' },
    { kind: 'list', data: { items: [{ id: 'i', title: 'readonly list' }] }, expected: 'readonly list' },
    { kind: 'checklist', data: { items: [{ id: 'i', text: 'readonly checklist', checked: false }] }, expected: 'readonly checklist' },
    { kind: 'file_list', data: { files: [{ path: 'readonly.txt', status: 'unchanged' }] }, expected: 'readonly.txt' },
    { kind: 'git_status', data: { branch: 'readonly/branch', changes: [] }, expected: 'readonly/branch' },
    { kind: 'key_value', data: { items: [{ key: 'ro', label: 'readonly key', value: 'value' }] }, expected: 'readonly key' },
    { kind: 'status', data: { items: [{ key: 'ro', label: 'readonly status', value: 'value' }] }, expected: 'readonly status' },
    { kind: 'progress', data: { items: [{ id: 'ro', label: 'readonly progress', state: 'pending' }] }, expected: 'readonly progress' },
    { kind: 'timeline', data: { items: [{ id: 'ro', label: 'readonly timeline', state: 'pending' }] }, expected: 'readonly timeline' },
  ];
  for (const testCase of validCases) {
    await render(<BlockHost
      block={block({ kind: testCase.kind, data: testCase.data })}
      context={context}
      readonly
      onUpdate={async () => { throw new Error('readonly callback must not run'); }}
    />);
    await waitFor(() => (container().textContent ?? '').includes(testCase.expected), `${testCase.kind} readonly renders`);
    const host = container().querySelector('.wg2-block-host');
    ok(Boolean(host?.hasAttribute('inert') || host?.getAttribute('aria-disabled') === 'true'), `${testCase.kind} readonly host is inert`);
    const checkbox = container().querySelector('input[type="checkbox"]') as HTMLInputElement | null;
    if (checkbox) ok(checkbox.disabled, `${testCase.kind} readonly control is disabled`);

    await render(<BlockHost
      block={block({ kind: testCase.kind, status: 'failed', data: testCase.data })}
      context={context}
    />);
    await waitFor(() => (container().textContent ?? '').includes('Block generation failed'), `${testCase.kind} failed status falls back`);
    ok(container().querySelector('.wg2-fallback') !== null, `${testCase.kind} failed state uses safe fallback`);
  }

  console.log('\n-- invalid data falls back for every kind');
  for (const { kind, data } of [
    { kind: 'item', data: { items: [{ id: 'x' }] } },
    { kind: 'list', data: { items: [{ id: 'x', title: 1 }] } },
    { kind: 'checklist', data: { items: [{ id: 'x', text: 'x', checked: 'not_a_bool' }] } },
    { kind: 'file_list', data: { files: [{ path: 'x', status: 'invalid' }] } },
    { kind: 'git_status', data: { branch: 'main', changes: [{ file: 'x', staged: 'yes', type: 'added' }] } },
    { kind: 'key_value', data: { items: [{ key: 'x', label: 'X' }] } },
    { kind: 'status', data: { items: [{ key: 'x', label: 'X', value: 1 }] } },
    { kind: 'progress', data: { items: [{ id: 'x', label: 'X', state: 'invalid' }] } },
    { kind: 'timeline', data: { items: [{ id: 'x', state: 'pending' }] } },
  ]) {
    await render(<BlockHost block={block({ kind, data })} context={context} />);
    await waitFor(() => (container().textContent ?? '').includes('Invalid data'), `${kind} invalid data falls back`);
    ok(container().querySelector('.wg2-fallback') !== null, `${kind} invalid data uses safe fallback`);
  }

  console.log('\n-- HTML injection prevention');
  await render(<BlockHost
    block={block({
      kind: 'item',
      data: { items: [{ id: 'x', title: '<script>alert(1)</script>' }] },
    })}
    context={context}
  />);
  await waitFor(() => (container().textContent ?? '').includes('<script>'), 'script tag is text, not executed');
  ok(container().querySelector('script') === null, 'no script element injected');

  await render(<BlockHost
    block={block({
      kind: 'file_list',
      data: { files: [{ path: '<img src=x onerror=alert(1)>', status: 'added' }] },
    })}
    context={context}
  />);
  await waitFor(() => (container().textContent ?? '').includes('<img src=x'), 'file path is safe text');
  ok(container().querySelector('img') === null, 'no img element injected from path');

  console.log('\n-- file_list and git_status: zero host side effects');
  // Render with file paths that look like real paths, but should NEVER
  // trigger any file I/O, bridge calls, or network requests.
  await render(<BlockHost
    block={block({
      kind: 'file_list',
      data: { files: [
        { path: 'C:\\Windows\\System32\\config\\SAM', status: 'added' },
        { path: '/etc/passwd', status: 'modified' },
        { path: '../../../.env', status: 'untracked' },
      ]},
    })}
    context={context}
  />);
  await flush();
  // Just verify it renders without error and paths are displayed as text.
  contains(container().textContent ?? '', 'C:\\Windows', 'file_list renders Windows path safely');
  contains(container().textContent ?? '', '/etc/passwd', 'file_list renders Unix path safely');
  // No <a> elements — paths are not clickable.
  ok(container().querySelector('a') === null, 'file paths are not hyperlinks');

  await render(<BlockHost
    block={block({
      kind: 'git_status',
      data: { branch: 'main', changes: [{ file: '.git/config', staged: false, type: 'modified' }] },
    })}
    context={context}
  />);
  await flush();
  contains(container().textContent ?? '', '.git/config', 'git_status renders git path safely');

  console.log('\n-- ARIA and semantic accessibility');
  await render(<BlockHost
    block={block({
      kind: 'checklist',
      data: { items: [{ id: 'c1', text: 'Accessible task', checked: false }] },
    })}
    context={context}
  />);
  await waitFor(() => container().querySelector('input[type="checkbox"]') !== null, 'checklist rendered');
  const ariaCb = container().querySelector('input[type="checkbox"]')!;
  ok(ariaCb.getAttribute('aria-label') !== null, 'checkbox has aria-label');
  ok(container().querySelector('section[aria-label="Checklist"]') !== null, 'checklist has section aria-label');

  await render(<BlockHost
    block={block({
      kind: 'progress',
      data: { items: [{ id: 'p1', label: 'Stage 1', state: 'completed' }] },
    })}
    context={context}
  />);
  await waitFor(() => container().querySelector('ol') !== null, 'progress renders ordered list');
  ok(container().querySelector('progress') !== null, 'progress uses native progress element');
  ok((container().querySelector('progress') as HTMLProgressElement)?.getAttribute('aria-label') !== null,
    'progress has aria-label');

  await render(<BlockHost
    block={block({
      kind: 'key_value',
      data: { items: [{ key: 'k', label: 'Key', value: 'Val' }] },
    })}
    context={context}
  />);
  await waitFor(() => container().querySelector('dl') !== null, 'key_value uses description list');
}

// ── checklist: onUpdate with conflict, draft, and retry ──────────────

async function testChecklistUpdate(): Promise<void> {
  console.log('\n-- checklist onUpdate: successful update');
  let updateCalls: Array<{
    workId: string; blockId: string; data: Record<string, unknown>;
    requestId: string; expectedRevision: number;
  }> = [];
  let updateResult: 'success' | 'conflict' | 'error' = 'success';

  const onUpdate = async (req: {
    workId: string; blockId: string; data: Record<string, unknown>;
    requestId: string; expectedRevision: number;
  }) => {
    updateCalls.push(req);
    if (updateResult === 'conflict') throw new Error('Revision conflict: expected 1, current 2');
    if (updateResult === 'error') throw new Error('Network failure C:\\sensitive\\path');
    // success
  };

  await render(<BlockHost
    block={block({
      kind: 'checklist',
      data: { items: [{ id: 'c1', text: 'Task', checked: false }] },
    })}
    context={context}
    onUpdate={onUpdate}
  />);
  await waitFor(() => container().querySelector('input[type="checkbox"]') !== null, 'checklist rendered');

  // Toggle the checkbox
  const checkbox = container().querySelector('input[type="checkbox"]') as HTMLInputElement;
  await click(checkbox);
  await flush();

  // Verify draft state: checkbox appears checked, save button appears
  ok(checkbox.checked, 'checkbox is checked after toggle (draft)');
  contains(container().textContent ?? '', 'Save changes', 'save button appears');

  // Click save
  const saveBtn = container().querySelector('.wg2-checklist-save') as HTMLButtonElement;
  ok(saveBtn !== null, 'save button exists');
  await click(saveBtn);
  await flush();

  ok(updateCalls.length === 1, 'onUpdate called once');
  ok(updateCalls[0].expectedRevision === 1, 'onUpdate carries expectedRevision');
  ok(updateCalls[0].blockId === 'block-1', 'onUpdate carries blockId');
  ok(updateCalls[0].workId === 'work-test', 'onUpdate carries workId');

  console.log('\n-- checklist onUpdate: revision conflict preserves draft');
  updateCalls = [];
  updateResult = 'conflict';
  const conflictLog: string[][] = [];
  console.error = (...args: string[]) => { conflictLog.push(args); };

  await render(<BlockHost
    block={block({
      kind: 'checklist',
      revision: 2,
      data: { items: [{ id: 'c1', text: 'Task v2', checked: false }] },
    })}
    context={context}
    onUpdate={onUpdate}
  />);
  await waitFor(() => container().querySelector('input[type="checkbox"]') !== null, 'checklist v2 rendered');

  const cb2 = container().querySelector('input[type="checkbox"]') as HTMLInputElement;
  await click(cb2);
  await flush();
  ok(cb2.checked, 'draft checkbox is checked');

  const saveBtn2 = container().querySelector('.wg2-checklist-save') as HTMLButtonElement;
  await click(saveBtn2);
  await flush();
  const conflictRequestID = updateCalls[0]?.requestId;

  // Error should be shown
  contains(container().textContent ?? '', 'Update failed', 'conflict error shown');
  // Error message must NOT contain raw implementation details
  notContains(container().textContent ?? '', 'expected 1', 'error message hides revision numbers');
  notContains(container().textContent ?? '', 'current 2', 'error message hides current revision');
  // Draft must be preserved (checkbox still checked)
  ok(cb2.checked, 'draft preserved after conflict');

  // Retry button should be present
  const retryBtn = container().querySelector('.wg2-checklist-retry') as HTMLButtonElement;
  ok(retryBtn !== null, 'retry button present');

  console.log('\n-- checklist onUpdate: retry succeeds after conflict');
  updateCalls = [];
  updateResult = 'success';
  await click(retryBtn);
  await flush();
  ok(updateCalls.length === 1, 'retry calls onUpdate');
  equal(updateCalls[0]?.requestId, conflictRequestID, 'same revision retry reuses requestId idempotently');
  equal(updateCalls[0]?.expectedRevision, 2, 'same revision retry keeps expectedRevision');
  // After success, save button should be gone (draft cleared)
  ok(container().querySelector('.wg2-checklist-save') === null, 'save button gone after successful retry');

  console.log('\n-- checklist onUpdate: network error is sanitized');
  updateCalls = [];
  updateResult = 'error';
  console.error = reportError;

  await render(<BlockHost
    block={block({
      id: 'network-checklist',
      kind: 'checklist',
      revision: 3,
      data: { items: [{ id: 'c1', text: 'Task v3', checked: false }] },
    })}
    context={context}
    onUpdate={onUpdate}
  />);
  await waitFor(() => container().querySelector('input[type="checkbox"]') !== null, 'checklist v3 rendered');

  const cb3 = container().querySelector('input[type="checkbox"]') as HTMLInputElement;
  await click(cb3);
  await flush();
  const saveBtn3 = container().querySelector('.wg2-checklist-save') as HTMLButtonElement;
  await click(saveBtn3);
  await flush();

  contains(container().textContent ?? '', 'Update failed', 'network error shown');
  notContains(container().textContent ?? '', 'sensitive', 'network error sanitizes path');
  notContains(container().textContent ?? '', 'C:\\\\', 'network error sanitizes Windows path');

  console.log('\n-- checklist onUpdate: revision change preserves draft and ignores late callback');
  console.error = reportError;
  let lateResolve!: (value: void) => void;
  const latePromise = new Promise<void>((resolve) => { lateResolve = resolve; });
  let lateReached = false;

  const lateOnUpdate = async () => {
    await latePromise;
    lateReached = true;
  };

  await render(<BlockHost
    block={block({
      id: 'late-checklist',
      kind: 'checklist',
      revision: 4,
      data: { items: [{ id: 'c1', text: 'V4 task', checked: false }] },
    })}
    context={context}
    onUpdate={lateOnUpdate}
  />);
  await waitFor(() => container().querySelector('input[type="checkbox"]') !== null, 'checklist v4 rendered');

  const cb4 = container().querySelector('input[type="checkbox"]') as HTMLInputElement;
  await click(cb4);
  await flush();
  const saveBtn4 = container().querySelector('.wg2-checklist-save') as HTMLButtonElement;
  await click(saveBtn4);
  // Save is in-flight (waiting on latePromise)

  // Render a new block with different revision — should reset draft
  await render(<BlockHost
    block={block({
      id: 'late-checklist',
      kind: 'checklist',
      revision: 5,
      data: { items: [{ id: 'c1', text: 'V5 task', checked: false }] },
    })}
    context={context}
    onUpdate={async () => {}}
  />);
  await waitFor(() => (container().textContent ?? '').includes('V5 task'), 'checklist v5 rendered');
  const v5Checkbox = container().querySelector('input[type="checkbox"]') as HTMLInputElement;
  ok(v5Checkbox.checked, 'draft from v4 remains visible over latest v5 projection');
  contains(container().textContent ?? '', 'Save changes', 'latest revision retains a saveable user draft');

  // Now release the late promise
  await act(async () => { lateResolve(); await flush(); });
  await flush();

  // The old completion cannot acknowledge or clear the draft owned by v5.
  ok(lateReached, 'late callback completed');
  ok(v5Checkbox.checked, 'late completion does not overwrite latest user draft');
  contains(container().textContent ?? '', 'Save changes', 'late completion does not clear latest draft');
  ok(!container().textContent?.includes('Update failed'), 'late completion does not leak stale error state');

  console.log('\n-- checklist keyboard accessibility');
  await render(<BlockHost
    block={block({
      id: 'keyboard-checklist',
      kind: 'checklist',
      revision: 6,
      data: { items: [{ id: 'k1', text: 'Keyboard task', checked: false }] },
    })}
    context={context}
    onUpdate={async () => {}}
  />);
  await waitFor(() => container().querySelector('input[type="checkbox"]') !== null, 'keyboard checklist rendered');

  const kCb = container().querySelector('input[type="checkbox"]') as HTMLInputElement;
  ok(!kCb.checked, 'keyboard checkbox starts unchecked');
  // Focus and press Space
  kCb.focus();
  await keyDown(kCb, ' ');
  ok(kCb.checked, 'Space toggles checkbox');
  // Press Enter
  await keyDown(kCb, 'Enter');
  ok(!kCb.checked, 'Enter toggles checkbox back');

  // readonly: keyboard should not toggle
  await render(<BlockHost
    block={block({
      id: 'readonly-keyboard-checklist',
      kind: 'checklist',
      revision: 7,
      data: { items: [{ id: 'k2', text: 'Readonly', checked: false }] },
    })}
    context={context}
    readonly
    onUpdate={async () => {}}
  />);
  await waitFor(() => container().querySelector('input[type="checkbox"]') !== null, 'readonly keyboard checklist');
  const roKb = container().querySelector('input[type="checkbox"]') as HTMLInputElement;
  ok(roKb.disabled, 'readonly checkbox is disabled for keyboard');
  await keyDown(roKb, ' ');
  ok(!roKb.checked, 'readonly checkbox not toggled by keyboard');
}

// ── main ─────────────────────────────────────────────────────────────

async function run(): Promise<void> {
  testSchemas();

  setupDom();
  await testRenderers();
  await testChecklistUpdate();

  process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed) process.exit(1);
}

run().catch((error) => {
  console.error = reportError;
  reportError('block renderers test runner failed', error);
  process.exit(1);
});
