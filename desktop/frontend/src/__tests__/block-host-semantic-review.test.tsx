// Reviewer fix3 regressions for semantic identity and safe key boundaries.
// Run: tsx src/__tests__/block-host-semantic-review.test.tsx

import { JSDOM } from 'jsdom';
import React, { act, useEffect, useId, useState } from 'react';
import { flushSync } from 'react-dom';
import { createRoot, type Root } from 'react-dom/client';
import { BlockHost } from '../components/work/blocks/BlockHost';
import { FallbackBlock } from '../components/work/blocks/FallbackBlock';
import { blockRegistry } from '../components/work/blocks/registry';
import { buildSafeJson, isSecretKey } from '../components/work/blocks/safeBlockJson';
import type { BlockRendererProps } from '../components/work/blocks/types';
import type { BlockInstance } from '../work/types';

const originalError = console.error.bind(console);
let passed = 0;
let failed = 0;
let dom: JSDOM;
let root: Root;
let mounts = 0;
let unmounts = 0;
let validations = 0;
let loads = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  condition ? passed++ : failed++;
}

function setup(): void {
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
  root = createRoot(document.getElementById('root')!);
}

function block(patch: Partial<BlockInstance> = {}): BlockInstance {
  return {
    id: 'semantic_probe',
    kind: 'review4_mount_probe',
    schemaVersion: 1,
    revision: 1,
    title: 'initial title',
    status: 'ready',
    data: {},
    source: { provider: 'provider_0', ref: 'digest_0', mode: 'snapshot', verified: true },
    fallback: { summary: 'safe summary' },
    createdAt: '2026-07-21T00:00:00Z',
    updatedAt: '2026-07-21T00:00:00Z',
    ...patch,
  };
}

function text(): string {
  return document.getElementById('root')?.textContent ?? '';
}

async function flush(): Promise<void> {
  await act(async () => { await new Promise<void>((resolve) => setTimeout(resolve, 0)); });
}

async function render(element: React.ReactElement): Promise<void> {
  await act(async () => { root.render(element); });
  await flush();
}

async function waitFor(predicate: () => boolean, label: string, ticks = 60): Promise<void> {
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

const MountProbe: React.FC<BlockRendererProps> = ({ block: current, context }) => {
  const [local, setLocal] = useState(0);
  const reactID = useId();
  useEffect(() => {
    mounts++;
    return () => { unmounts++; };
  }, []);
  return (
    <div id={reactID} data-testid="mount-probe">
      <span data-testid="probe-props">
        {current.title}|{current.source.provider}|{context.workId}|local:{local}
      </span>
      <button type="button" data-testid="probe-increment" onClick={() => setLocal((value) => value + 1)}>
        increment
      </button>
      <div data-testid="probe-scroll" style={{ height: 10, overflow: 'auto' }}>
        <div style={{ height: 100 }}>scroll content</div>
      </div>
    </div>
  );
};

async function run(): Promise<void> {
  setup();
  const logs: unknown[][] = [];
  console.error = (...args: unknown[]) => { logs.push(args); };

  blockRegistry.register(
    'review4_mount_probe',
    1,
    () => { validations++; return { valid: true }; },
    async () => { loads++; return { component: MountProbe }; },
  );

  console.log('\n-- semantic identity preserves renderer instances');
  const stableData = { value: 'stable' };
  const first = block({ data: stableData });
  await render(
    <BlockHost block={first} context={{ workId: 'work_0', workSchemaVersion: 1 }} />,
  );
  await waitFor(() => document.querySelector('[data-testid="mount-probe"]') !== null, 'initial probe');
  await click(document.querySelector('[data-testid="probe-increment"]')!);
  const scroll = document.querySelector('[data-testid="probe-scroll"]') as HTMLElement;
  scroll.scrollTop = 37;

  await act(async () => {
    for (let index = 0; index < 100; index++) {
      const wrapper = block({
        data: stableData,
        title: `title_${index}`,
        source: {
          provider: `provider_${index}`,
          ref: 'digest_0',
          mode: 'snapshot',
          verified: index % 2 === 0,
        },
        fallback: { summary: `summary_${index}` },
        updatedAt: `2026-07-21T00:00:${String(index % 60).padStart(2, '0')}Z`,
      });
      flushSync(() => {
        root.render(
          <BlockHost block={wrapper} context={{ workId: `work_${index}`, workSchemaVersion: 1 }} />,
        );
      });
    }
  });
  await flush();

  ok(mounts === 1 && unmounts === 0, '100 same-identity wrappers keep one mounted component instance');
  ok(validations === 1 && loads === 1, 'same semantic identity does not revalidate or restart module loading');
  ok(text().includes('title_99|provider_99|work_99|local:1'), 'non-identity block and context props update through the existing root');
  ok((document.querySelector('[data-testid="probe-scroll"]') as HTMLElement).scrollTop === 37, 'same identity preserves renderer DOM and scroll state');

  const htmlWithUseID = document.getElementById('root')!.outerHTML;
  ok(!htmlWithUseID.includes('data-block-identity'), 'internal identity is not exposed as a DOM attribute');
  ok(!/block-\d+/.test(htmlWithUseID), 'React useId output contains no internal identity sequence');

  const nextData = { value: 'new-ref' };
  await render(<BlockHost block={block({ data: nextData, title: 'data ref' })} context={{ workId: 'work_data', workSchemaVersion: 1 }} />);
  await waitFor(() => mounts === 2, 'dataRef remount');
  ok(mounts === 2 && unmounts === 1, 'dataRef change remounts exactly once');

  await render(<BlockHost block={block({ data: nextData, revision: 2, title: 'revision' })} context={{ workId: 'work_revision', workSchemaVersion: 1 }} />);
  await waitFor(() => mounts === 3, 'revision remount');
  ok(mounts === 3 && unmounts === 2, 'revision change remounts exactly once');

  await render(
    <BlockHost
      block={block({
        data: nextData,
        revision: 2,
        title: 'digest',
        source: { provider: 'provider_digest', ref: 'digest_1', mode: 'snapshot', verified: true },
      })}
      context={{ workId: 'work_digest', workSchemaVersion: 1 }}
    />,
  );
  await waitFor(() => mounts === 4, 'digest remount');
  ok(mounts === 4 && unmounts === 3, 'digest change remounts exactly once');

  console.log('\n-- internal identity stays memory-only');
  let copied = '';
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async (value: string) => { copied = value; } },
  });
  await render(
    <FallbackBlock
      block={block({ id: 'fallback_public', kind: 'unknown_public', data: { count: 42n } })}
      reason="Renderer unavailable"
    />,
  );
  await click(document.querySelector('.wg2-fallback-copy')!);
  await waitFor(() => copied.length > 0, 'fallback copy');
  const exposed = `${htmlWithUseID}\n${JSON.stringify(logs)}\n${copied}`;
  ok(!/block-\d+/.test(exposed), 'rendered HTML, console, and fallback JSON contain no identity sequence');
  ok(!/\b\d+n\b|BigInt/.test(exposed), 'rendered HTML, console, and fallback JSON contain no BigInt token');

  console.log('\n-- secret key compact/token boundaries');
  const secretKeys = [
    'password', 'passWord', 'pass_word', 'pass.word', 'clientSecret', 'client secret',
    'accessToken', 'access.token', 'privateKey', 'private.key', 'apiKey',
    'refreshToken', 'authorization', 'cookie', 'sessionKey', 'ReFrEsH.ToKeN',
    'PrIvAtE_kEy',
  ];
  const publicKeys = ['monkey', 'keyboard', 'keynote', 'tokenCount'];
  ok(secretKeys.every((key) => isSecretKey(key)), 'compact and normalized token forms detect every required secret key');
  ok(publicKeys.every((key) => !isSecretKey(key)), 'ordinary key/token substrings are not over-redacted');

  const input: Record<string, string> = {};
  for (const key of secretKeys) input[key] = `private_${key}`;
  for (const key of publicKeys) input[key] = `visible_${key}`;
  const envelope = JSON.parse(buildSafeJson(input)) as { value: Record<string, unknown> };
  ok(secretKeys.every((key) => envelope.value[key] === '[redacted]'), 'safe JSON redacts required secret-key values');
  ok(publicKeys.every((key) => envelope.value[key] === `visible_${key}`), 'safe JSON preserves explicit non-secret boundary values');

  console.error = originalError;
  await act(async () => { root.unmount(); });
  await flush();
  process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed) process.exit(1);
}

run().catch((error) => {
  console.error = originalError;
  originalError('block host semantic reviewer overlay failed', error);
  process.exit(1);
});
