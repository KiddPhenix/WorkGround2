// Run: tsx src/__tests__/block-host-fallback.test.tsx

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { BlockHost } from '../components/work/blocks/BlockHost';
import { FallbackBlock } from '../components/work/blocks/FallbackBlock';
import { blockRegistry } from '../components/work/blocks/registry';
import type { BlockRendererProps, RendererModule } from '../components/work/blocks/types';
import type { BlockInstance } from '../work/types';

const reportError = console.error.bind(console);

let passed = 0;
let failed = 0;
let root: Root | null = null;
let dom: JSDOM;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  condition ? passed++ : failed++;
}

function contains(value: string, part: string, label: string): void {
  ok(value.includes(part), `${label}${value.includes(part) ? '' : ` (missing ${JSON.stringify(part)})`}`);
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

const context = { workId: 'work-test', workSchemaVersion: 1 };

function block(patch: Partial<BlockInstance> = {}): BlockInstance {
  return {
    id: 'block-1',
    kind: 'test-unknown',
    schemaVersion: 1,
    revision: 1,
    status: 'ready',
    data: { items: [] },
    source: { provider: 'controller', ref: 'snapshot-17', mode: 'snapshot', verified: true },
    fallback: { summary: 'Archived block summary' },
    createdAt: '2026-07-20T10:00:00Z',
    updatedAt: '2026-07-20T11:00:00Z',
    ...patch,
  };
}

async function run(): Promise<void> {
  setupDom();

  console.log('\n-- fallback metadata, safety, and accessibility');
  await render(<FallbackBlock block={block({ kind: 'chart', revision: 7 })} reason="Renderer unavailable" />);
  const text = container().textContent ?? '';
  for (const expected of ['chart', '7', 'controller', 'snapshot-17', '2026-07-20', 'Archived block summary', 'Copy safe JSON']) {
    contains(text, expected, `fallback exposes ${expected}`);
  }
  ok(container().querySelector('section[aria-label]') !== null, 'fallback has an accessible region label');

  await render(<FallbackBlock block={block()} reason="Archived" interactiveDisabled />);
  const disabledCopy = container().querySelector('.wg2-fallback-copy') as HTMLButtonElement;
  ok(disabledCopy.disabled, 'archive fallback uses native disabled button');
  disabledCopy.focus();
  ok(document.activeElement !== disabledCopy, 'native disabled button cannot receive focus');

  let copied = '';
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async (value: string) => { copied = value; } },
  });
  const circular: Record<string, unknown> = {
    apiKey: 'sk-private',
    authorization: 'Bearer private',
    normal: 'visible',
    bigint: 42n,
  };
  circular.self = circular;
  let getterReads = 0;
  Object.defineProperty(circular, 'danger', {
    enumerable: true,
    get() { getterReads++; throw new Error('must not execute'); },
  });
  await render(<FallbackBlock block={block({ data: circular })} reason="Copy" />);
  await click(container().querySelector('.wg2-fallback-copy')!);
  await waitFor(() => copied.length > 0, 'clipboard write');
  const parsed = JSON.parse(copied) as Record<string, unknown>;
  const copiedData = parsed.data as Record<string, unknown>;
  ok(copiedData.apiKey === '[redacted]' && copiedData.authorization === '[redacted]', 'secret-like fields are redacted');
  ok(copiedData.normal === 'visible' && copiedData.self === '[circular]', 'normal and circular values serialize safely');
  ok(copiedData.danger === '[accessor omitted]' && getterReads === 0, 'accessors are not executed');
  ok(new TextEncoder().encode(copied).byteLength <= 64 * 1024, 'copied JSON obeys UTF-8 byte limit');

  const large: Record<string, string> = {};
  for (let index = 0; index < 250; index++) large[`field-${index}`] = '界'.repeat(512);
  copied = '';
  await render(<FallbackBlock block={block({ data: large })} reason="Large" />);
  await click(container().querySelector('.wg2-fallback-copy')!);
  await waitFor(() => copied.length > 0, 'large clipboard write');
  const largeJson = JSON.parse(copied) as { truncated?: boolean; originalBytes?: number };
  ok(largeJson.truncated === true && (largeJson.originalBytes ?? 0) > 64 * 1024, 'oversized payload becomes valid bounded JSON envelope');
  ok(new TextEncoder().encode(copied).byteLength <= 64 * 1024, 'oversized envelope remains within byte limit');

  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async () => { throw new Error('secret=C:\\private\\file'); } },
  });
  await render(<FallbackBlock block={block()} reason="Copy" />);
  await click(container().querySelector('.wg2-fallback-copy')!);
  await waitFor(() => (container().textContent ?? '').includes('Clipboard unavailable'), 'copy failure');
  ok(!(container().textContent ?? '').includes('private'), 'clipboard failure never exposes raw error details');

  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: () => { throw new Error('synchronous clipboard failure'); } },
  });
  await render(<FallbackBlock block={block({ revision: 2 })} reason="Copy" />);
  await click(container().querySelector('.wg2-fallback-copy')!);
  contains(container().textContent ?? '', 'Clipboard unavailable', 'synchronous clipboard failure is contained');

  await render(<FallbackBlock block={block({ kind: '<img src=x onerror=alert(1)>' })} reason="Unknown" />);
  ok(container().querySelector('img') === null, 'payload text cannot inject HTML');
  contains(container().textContent ?? '', '<img src=x', 'payload kind is preserved as text');

  console.log('\n-- host validation order and status states');
  let validations = 0;
  let loads = 0;
  const Ready: React.FC<BlockRendererProps> = ({ readonly, onAction }) => (
    <div data-testid="ready" data-readonly={String(readonly)}>
      ready renderer
      <button type="button" onClick={() => onAction?.({
        workId: 'work-test', blockId: 'ready', actionId: 'act', requestId: 'req', expectedRevision: 1,
      })}>act</button>
    </div>
  );
  blockRegistry.register(
    'test-ready',
    1,
    (_schema, data) => {
      validations++;
      return { valid: Boolean((data as { ok?: boolean })?.ok), reason: 'token=private expected ok' };
    },
    async () => { loads++; return { component: Ready }; },
  );

  await render(<BlockHost block={block({ kind: 'test-ready', status: 'loading' })} context={context} />);
  ok(container().querySelector('[role="status"]') !== null, 'loading status renders accessible loading state');
  ok(validations === 0 && loads === 0, 'loading status neither validates data nor imports renderer');

  await render(<BlockHost block={block({ kind: 'test-ready', data: { ok: false }, revision: 2 })} context={context} />);
  await waitFor(() => (container().textContent ?? '').includes('Invalid data'), 'invalid data fallback');
  ok(validations === 1 && loads === 0, 'invalid data is rejected before lazy import');
  ok(!(container().textContent ?? '').includes('private'), 'validator reason is redacted');

  await render(<BlockHost block={block({ kind: 'test-ready', data: { ok: true }, revision: 3 })} context={context} />);
  await waitFor(() => (container().textContent ?? '').includes('ready renderer'), 'ready renderer');
  ok(validations === 2 && loads === 1, 'valid data loads renderer exactly once');

  for (const [status, expected] of [
    ['empty', 'no content'], ['stale', 'outdated'], ['blocked', 'blocked'], ['failed', 'failed'],
  ] as const) {
    await render(<BlockHost block={block({ kind: 'test-ready', status, revision: block().revision + expected.length })} context={context} />);
    contains((container().textContent ?? '').toLowerCase(), expected, `${status} has a distinct degraded state`);
  }
  ok(loads === 1, 'degraded statuses do not import renderer');

  await render(<BlockHost block={block({ kind: 'test-ready', schemaVersion: 0, revision: 20 })} context={context} />);
  contains(container().textContent ?? '', 'schema version is invalid', 'invalid schema is explicit');

  console.log('\n-- unknown, unsupported, and future schema');
  blockRegistry.register('test-versioned', { min: 2, max: 3 }, () => ({ valid: true }), async () => ({ component: Ready }));
  await render(<BlockHost block={block({ kind: 'missing-kind', revision: 21 })} context={context} />);
  contains(container().textContent ?? '', 'Unknown kind', 'unknown kind degrades locally');
  await render(<BlockHost block={block({ kind: 'test-versioned', schemaVersion: 1, revision: 22 })} context={context} />);
  contains(container().textContent ?? '', 'Unsupported schema', 'old unsupported schema is distinct');
  await render(<BlockHost block={block({ kind: 'test-versioned', schemaVersion: 99, revision: 23 })} context={context} />);
  contains(container().textContent ?? '', 'Future schema', 'future schema is explicit');
  ok((container().querySelector('.wg2-fallback-copy') as HTMLButtonElement).disabled, 'future schema fallback is forced readonly');

  console.log('\n-- archive enforcement');
  let actions = 0;
  await render(
    <BlockHost
      block={block({ kind: 'test-ready', data: { ok: true }, revision: 24 })}
      context={context}
      archived
      onAction={() => { actions++; }}
    />,
  );
  await waitFor(() => (container().textContent ?? '').includes('ready renderer'), 'archived renderer');
  const host = container().querySelector('.wg2-block-host')!;
  ok(host.hasAttribute('inert') && host.getAttribute('aria-disabled') === 'true', 'archive host is inert and aria-disabled');
  ok(container().querySelector('[data-readonly="true"]') !== null, 'renderer receives forced readonly=true');
  await click(container().querySelector('.wg2-block-host button')!);
  ok(actions === 0, 'host capture boundary blocks renderer interaction');

  await render(
    <BlockHost
      block={block({ kind: 'test-ready', data: { ok: true }, revision: 25 })}
      context={context}
      onAction={() => { actions++; }}
    />,
  );
  await waitFor(() => (container().textContent ?? '').includes('ready renderer'), 'interactive renderer');
  await click(container().querySelector('.wg2-block-host button')!);
  ok(actions === 1, 'active host forwards structured actions');

  console.log('\n-- import retry and stale-load isolation');
  const loggedErrors: unknown[][] = [];
  console.error = (...args: unknown[]) => { loggedErrors.push(args); };
  let flakyLoads = 0;
  blockRegistry.register('test-flaky', 1, () => ({ valid: true }), async () => {
    flakyLoads++;
    if (flakyLoads === 1) throw new Error('token=private C:\\secret-path');
    return { component: Ready };
  });
  await render(<BlockHost block={block({ kind: 'test-flaky', revision: 30 })} context={context} />);
  await waitFor(() => (container().textContent ?? '').includes('failed to load'), 'import failure fallback');
  ok(!(container().textContent ?? '').includes('secret-path'), 'import failure hides raw error');
  await click(container().querySelector('.wg2-block-retry')!);
  await waitFor(() => (container().textContent ?? '').includes('ready renderer'), 'import retry succeeds');
  ok(flakyLoads === 2, 'import retry starts one new attempt');

  let releaseSlow!: (module: RendererModule) => void;
  const slow = new Promise<RendererModule>((resolve) => { releaseSlow = resolve; });
  blockRegistry.register('test-slow', 1, () => ({ valid: true }), () => slow);
  blockRegistry.register('test-fast', 1, () => ({ valid: true }), async () => ({
    component: () => <div>fast renderer wins</div>,
  }));
  await render(<BlockHost block={block({ kind: 'test-slow', revision: 31 })} context={context} />);
  await render(<BlockHost block={block({ kind: 'test-fast', revision: 32 })} context={context} />);
  await waitFor(() => (container().textContent ?? '').includes('fast renderer wins'), 'fast replacement renderer');
  await act(async () => { releaseSlow({ component: () => <div>stale slow renderer</div> }); });
  await flush();
  contains(container().textContent ?? '', 'fast renderer wins', 'late lazy result cannot replace a newer block');
  ok(!(container().textContent ?? '').includes('stale slow'), 'late result stays isolated');

  console.log('\n-- renderer crash isolation and bounded retry');
  const Crash: React.FC = () => { throw new Error('password=private renderer detail'); };
  blockRegistry.register('test-crash', 1, () => ({ valid: true }), async () => ({ component: Crash }));
  blockRegistry.register('test-sibling', 1, () => ({ valid: true }), async () => ({
    component: () => <div>sibling survives</div>,
  }));
  await render(
    <div>
      <BlockHost block={block({ id: 'crash', kind: 'test-crash', revision: 40 })} context={context} />
      <BlockHost block={block({ id: 'sibling', kind: 'test-sibling', revision: 40 })} context={context} />
    </div>,
  );
  await waitFor(() => (container().textContent ?? '').includes('Renderer crashed'), 'renderer crash fallback');
  contains(container().textContent ?? '', 'sibling survives', 'crash only degrades one block');
  ok(!(container().textContent ?? '').includes('private'), 'renderer error message is absent from UI');
  for (let attempt = 0; attempt < 3; attempt++) {
    const retry = container().querySelector('.wg2-block-retry');
    ok(retry !== null, `crash retry ${attempt + 1} is available`);
    if (retry) await click(retry);
    await waitFor(() => (container().textContent ?? '').includes('Renderer crashed'), `crash retry ${attempt + 1}`);
  }
  ok(container().querySelector('.wg2-block-retry') === null, 'renderer retries stop after the configured bound');
  contains(container().textContent ?? '', 'sibling survives', 'sibling remains mounted after retries');
  console.error = reportError;
  const safeLogs = JSON.stringify(loggedErrors);
  ok(!safeLogs.includes('private') && !safeLogs.includes('secret-path'), 'renderer logs omit raw messages, paths, and secrets');
  ok(safeLogs.includes('blockId') && safeLogs.includes('revision'), 'renderer failures retain safe diagnostic context');

  console.log('\n-- unmount during lazy load');
  let releaseUnmount!: (module: RendererModule) => void;
  const pendingUnmount = new Promise<RendererModule>((resolve) => { releaseUnmount = resolve; });
  blockRegistry.register('test-unmount', 1, () => ({ valid: true }), () => pendingUnmount);
  await render(<BlockHost block={block({ kind: 'test-unmount', revision: 50 })} context={context} />);
  await act(async () => { root!.unmount(); root = null; });
  await act(async () => { releaseUnmount({ component: () => <div>must not mount</div> }); });
  await flush();
  ok(container().textContent === '', 'late import after unmount performs no visible update');

  process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed) process.exit(1);
}

run().catch((error) => {
  console.error = reportError;
  reportError('block host test runner failed', error);
  process.exit(1);
});
