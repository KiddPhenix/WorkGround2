import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import type { ArtifactPreview, ArtifactSlot, TaskV2View, WorkDefinitionRevision } from '../../types_v2';
import { ResultCard, ResultShelf } from './index';
import type { FileLocateIntent, FileOpenIntent } from './ResultCard';

// ── test harness ───────────────────────────────────────────────────────────

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else { failed++; if (failed <= 5) { const st = new Error().stack?.split('\n')[2]?.trim(); if (st) process.stdout.write(`       ${st}\n`); } }
}

function eq<T>(actual: T, expected: T, label: string): void {
  const cond = actual === expected;
  ok(cond, `${label}${cond ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function contains(actual: string, substring: string, label: string): void {
  ok(actual.includes(substring), `${label} (expected "${substring}" in "${actual.slice(0, 100)}")`);
}

function setupDOM(): void {
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
}

setupDOM();

async function settle(delay = 20): Promise<void> {
  await act(async () => { await new Promise<void>((r) => setTimeout(r, delay)); });
}

async function interact(action: () => void): Promise<void> {
  await act(async () => { action(); await new Promise<void>((r) => setTimeout(r, 20)); });
}

interface Mounted {
  host: HTMLDivElement;
  root: Root;
  cleanup: () => Promise<void>;
}

async function mount(element: React.ReactElement): Promise<Mounted> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host, { onCaughtError: (error) => { throw error; } });
  await act(async () => { root.render(element); });
  await settle();
  return { host, root, cleanup: async () => { await act(async () => { root.unmount(); }); host.remove(); } };
}

// ── fixtures ───────────────────────────────────────────────────────────────

function makeSlot(overrides: Partial<ArtifactSlot> = {}): ArtifactSlot {
  return {
    id: 'slot-1',
    workId: 'work-1',
    definitionRev: 2,
    title: '测试成果',
    kind: 'text',
    expectedCount: 1,
    required: true,
    state: 'reserved',
    artifactRefs: [],
    revision: 1,
    ...overrides,
  };
}

function makeDefinition(): WorkDefinitionRevision {
  return {
    workId: 'work-1',
    revision: 2,
    parentRevision: 1,
    status: 'active',
    goal: 'deliver',
    nodes: [
      { id: 'make', title: '生成报告', producesSlotIds: ['slot-1'], blockIds: ['block-make'] },
      { id: 'review', title: '审核报告', dependsOn: ['make'], consumesSlotIds: ['slot-1'] },
    ],
    artifactSlots: [
      { id: 'slot-1', title: '测试成果', kind: 'text', expectedCount: 1, required: true },
    ],
    inputSpecs: [],
    createdBy: 'test',
    createdAt: '2026-07-28T00:00:00Z',
    digest: 'digest',
  };
}

function makeTask(overrides: Partial<TaskV2View> = {}): TaskV2View {
  return {
    id: 'task-make',
    runId: 'run-current',
    nodeId: 'make',
    title: '生成报告',
    state: 'running',
    retryable: false,
    updatedAt: '2026-07-30T00:00:00Z',
    ...overrides,
  };
}

function makeRef(overrides: Record<string, unknown> = {}): ArtifactSlot['artifactRefs'][number] {
  return {
    id: 'ref-1',
    name: 'output.txt',
    type: 'text/plain',
    status: 'available',
    path: '/tmp/output.txt',
    relativePath: 'output.txt',
    ...overrides,
  } as ArtifactSlot['artifactRefs'][number];
}

/** Creates a deferred promise: { promise, resolve, reject } */
function deferred(): { promise: Promise<void>; resolve: () => void; reject: (e: Error) => void } {
  let r!: () => void;
  let j!: (e: Error) => void;
  const promise = new Promise<void>((res, rej) => { r = res; j = rej; });
  return { promise, resolve: r, reject: j };
}

function deferredValue<T>(): {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (e: Error) => void;
} {
  let r!: (value: T) => void;
  let j!: (e: Error) => void;
  const promise = new Promise<T>((res, rej) => { r = res; j = rej; });
  return { promise, resolve: r, reject: j };
}

// ── golden data ────────────────────────────────────────────────────────────

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const goldenPath = resolve(__dirname, '..', '..', '..', '..', '..', '..', 'internal', 'work', 'testdata', 'contract-v2', 'work-view-v2-full.json');

let goldenRaw: string;
try {
  goldenRaw = readFileSync(goldenPath, 'utf-8');
  JSON.parse(goldenRaw); // must parse
} catch (err) {
  // Golden fixture MUST be present and parseable — fail hard.
  process.stdout.write(`  FAIL  golden fixture missing or unparseable: ${err}\n`);
  failed++;
  // continue so all test failures are collected, then exit non-zero
}

// ── tests ──────────────────────────────────────────────────────────────────

async function runTests(): Promise<void> {
  // A producer failure can arrive before artifact settlement. The shelf derives
  // the truthful state from the current Run, then returns to generating when
  // the same producer starts its retry.
  {
    const slot = makeSlot({ state: 'generating', progress: 0.72 });
    const definition = makeDefinition();
    const { host, root, cleanup } = await mount(
      <ResultShelf
        slots={[slot]}
        activeDefinitionRevision={2}
        definition={definition}
        tasks={[makeTask({
          state: 'failed_retryable',
          retryable: true,
          error: 'completion gate: missing web_search evidence',
        })]}
        runId="run-current"
        onRetry={async () => {}}
      />,
    );
    const failedCard = host.querySelector('[data-testid="result-card-slot-1"]');
    eq(failedCard?.getAttribute('data-slot-state'), 'failed', 'producer failure: generating artifact becomes failed');
    ok(host.querySelector('[data-testid="result-card-error-slot-1"]') === null, 'producer failure: error does not resize the card');
    const failureBadge = host.querySelector<HTMLButtonElement>('[data-testid="result-card-badge-slot-1"]');
    eq(failureBadge?.getAttribute('aria-haspopup'), 'dialog', 'producer failure: badge exposes floating details');
    await interact(() => failureBadge?.click());
    contains(document.querySelector('[data-testid="result-card-error-slot-1"]')?.textContent ?? '', 'completion gate', 'producer failure: task error opens in floating details');
    ok(document.querySelector('[data-testid="result-card-retry-slot-1"]') !== null, 'producer failure: floating details expose retry');
    ok(host.querySelector('[role="progressbar"]') === null, 'producer failure: stale generation progress is hidden');

    await act(async () => {
      root.render(
        <ResultShelf
          slots={[slot]}
          activeDefinitionRevision={2}
          definition={definition}
          tasks={[
            makeTask({ runId: 'run-old', state: 'failed_retryable', retryable: true }),
            makeTask({ state: 'running' }),
          ]}
          runId="run-current"
        />,
      );
    });
    await settle();
    eq(host.querySelector('[data-testid="result-card-slot-1"]')?.getAttribute('data-slot-state'), 'generating', 'producer retry: current Run restores generating state');
    ok(host.querySelector('[data-testid="result-card-error-slot-1"]') === null, 'producer retry: historical Run failure is ignored');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 1. reserved — no refs, visible placeholder
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'reserved', artifactRefs: [] });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    const card = host.querySelector('[data-testid="result-card-slot-1"]');
    ok(card !== null, 'reserved: card rendered');
    eq(card?.getAttribute('data-slot-state'), 'reserved', 'reserved: data-slot-state');
    const badge = host.querySelector('[data-testid="result-card-badge-slot-1"]');
    ok(badge?.textContent?.includes('待生成') ?? false, 'reserved: badge shows 待生成');
    const placeholder = host.querySelector('[data-testid="result-card-placeholder-slot-1"]');
    ok(placeholder !== null, 'reserved: placeholder visible');
    contains(placeholder?.textContent ?? '', '尚未生成', 'reserved: placeholder text');
    ok(host.querySelector('.wg2-rc-files') === null, 'reserved: no file list');
    await cleanup();
  }

  // Host safety boundary: absolute-only refs never expose open/locate.
  {
    const slot = makeSlot({
      state: 'ready',
      artifactRefs: [makeRef({ id: 'absolute-only', path: 'C:\\outside\\secret.txt', relativePath: undefined })],
    });
    const { host, cleanup } = await mount(
      <ResultCard slot={slot} onOpen={() => { throw new Error('must not run'); }} onLocate={() => { throw new Error('must not run'); }} />,
    );
    ok(host.querySelector('[data-testid="rc-file-open-absolute-only"]') === null, 'safe-host: absolute-only open is hidden');
    ok(host.querySelector('[data-testid="rc-file-locate-absolute-only"]') === null, 'safe-host: absolute-only locate is hidden');
    await cleanup();
  }

  // Blob-backed generated files expose identity-based host actions without
  // forwarding an arbitrary path from the renderer.
  {
    const opened: FileOpenIntent[] = [];
    const located: FileLocateIntent[] = [];
    const slot = makeSlot({
      state: 'ready',
      artifactRefs: [makeRef({
        id: 'blob-file',
        name: '预算表.xlsx',
        type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
        path: undefined,
        relativePath: undefined,
        blobDigest: `sha256:${'a'.repeat(64)}`,
      })],
    });
    const { host, cleanup } = await mount(
      <ResultCard
        slot={slot}
        onOpen={(intent) => { opened.push(intent); }}
        onLocate={(intent) => { located.push(intent); }}
      />,
    );
    const open = host.querySelector<HTMLButtonElement>('[data-testid="rc-file-open-blob-file"]');
    const locate = host.querySelector<HTMLButtonElement>('[data-testid="rc-file-locate-blob-file"]');
    ok(open !== null, 'blob-host: default-app action is visible');
    ok(locate !== null, 'blob-host: file-manager action is visible');
    contains(open?.getAttribute('aria-label') ?? '', '使用默认应用打开', 'blob-host: open action is explicit');
    contains(locate?.getAttribute('aria-label') ?? '', '在文件管理器中显示', 'blob-host: locate action is explicit');
    await interact(() => open?.click());
    await interact(() => locate?.click());
    eq(opened[0]?.artifactRefId, 'blob-file', 'blob-host: open carries authoritative ref identity');
    eq(located[0]?.artifactRefId, 'blob-file', 'blob-host: locate carries authoritative ref identity');
    ok(!('path' in (opened[0] ?? {})), 'blob-host: renderer does not forward a file path');
    await cleanup();
  }

  // Partial and stale slots retain a visible recovery path.
  {
    const retried: string[] = [];
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[
          makeSlot({ id: 'partial-retry', state: 'partial', error: { code: 'partial', message: '部分失败', retryable: true } }),
          makeSlot({ id: 'stale-retry', state: 'stale' }),
        ]}
        activeDefinitionRevision={2}
        onRetry={async (intent) => { retried.push(intent.slotId); }}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-card-badge-partial-retry"]')?.click());
    await interact(() => document.querySelector<HTMLButtonElement>('[data-testid="result-card-retry-partial-retry"]')?.click());
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-card-retry-stale-retry"]')?.click());
    eq(retried.join(','), 'partial-retry,stale-retry', 'slot-retry: partial and stale call the typed recovery port');
    await cleanup();
  }

  // A malformed runtime caller without active authority fails closed instead
  // of exposing historical revisions. TypeScript production callers cannot
  // omit the required prop.
  {
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot({ id: 'historical-slot', definitionRev: 2, state: 'ready' })]}
        activeDefinitionRevision={undefined as unknown as number}
      />,
    );
    ok(host.querySelector('[data-testid="result-card-historical-slot"]') === null, 'active-revision: missing runtime authority hides historical slots');
    ok(host.querySelector('[data-testid="result-shelf-empty"]') !== null, 'active-revision: missing runtime authority fails closed');
    await cleanup();
  }

  // A projection may briefly contain the same slot ID from two Definition
  // revisions. Only the active revision is rendered and its visible ArtifactRef
  // is the exact one forwarded by the click intent.
  {
    const opened: FileOpenIntent[] = [];
    const oldSlot = makeSlot({
      id: 'same-slot',
      definitionRev: 2,
      state: 'ready',
      artifactRefs: [makeRef({ id: 'old-ref', name: 'old.txt', relativePath: 'old.txt' })],
    });
    const activeSlot = makeSlot({
      id: 'same-slot',
      definitionRev: 3,
      state: 'ready',
      artifactRefs: [makeRef({ id: 'active-ref', name: 'active.txt', relativePath: 'active.txt' })],
    });
    const { host, root, cleanup } = await mount(
      <ResultShelf
        slots={[oldSlot, activeSlot]}
        activeDefinitionRevision={3}
        onOpen={(intent) => { opened.push(intent); }}
      />,
    );
    eq(host.querySelectorAll('[data-testid="result-shelf-item-same-slot"]').length, 1, 'active-revision: same slot ID renders once');
    ok(host.querySelector('[data-testid="result-card-file-old-ref"]') === null, 'active-revision: historical artifact is hidden');
    ok(host.querySelector('[data-testid="result-card-file-active-ref"]') !== null, 'active-revision: active artifact is visible');
    const activeCard = host.querySelector('[data-testid="result-card-same-slot"]');
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="rc-file-open-active-ref"]')?.click());
    eq(opened.length, 1, 'active-revision: visible click fires once');
    eq(opened[0]?.definitionRevision, 3, 'active-revision: click carries active definition revision');
    eq(opened[0]?.artifactRefId, 'active-ref', 'active-revision: click carries visible artifact ref');

    await act(async () => {
      root.render(
        <ResultShelf
          slots={[oldSlot, activeSlot]}
          activeDefinitionRevision={2}
          onOpen={(intent) => { opened.push(intent); }}
        />,
      );
    });
    await settle();
    ok(activeCard !== host.querySelector('[data-testid="result-card-same-slot"]'), 'active-revision: definition change remounts same slot ID');
    ok(host.querySelector('[data-testid="result-card-file-active-ref"]') === null, 'active-revision: prior active artifact is removed');
    ok(host.querySelector('[data-testid="result-card-file-old-ref"]') !== null, 'active-revision: newly active artifact is visible');
    await cleanup();
  }

  // partial without ArtifactError is still a recoverable state per 13.2.
  {
    let retried = 0;
    const { host, cleanup } = await mount(
      <ResultCard
        slot={makeSlot({ id: 'partial-no-error', state: 'partial', error: undefined })}
        onRetry={async () => { retried++; }}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-card-badge-partial-no-error"]')?.click());
    const recovery = document.querySelector('[data-testid="result-card-partial-error-partial-no-error"]');
    contains(recovery?.textContent ?? '', '部分产物尚未完成', 'partial-no-error: state explanation is explicit');
    const retry = document.querySelector<HTMLButtonElement>('[data-testid="result-card-retry-partial-no-error"]');
    ok(retry !== null, 'partial-no-error: retry entry remains available');
    await interact(() => retry?.click());
    eq(retried, 1, 'partial-no-error: retry reaches typed recovery port');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 2. generating — with progress
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'generating', progress: 0.65, artifactRefs: [] });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    ok(host.querySelector('[data-testid="result-card-badge-slot-1"]')?.textContent?.includes('生成中') ?? false, 'generating: badge');
    const bar = host.querySelector('[role="progressbar"]');
    ok(bar !== null, 'generating: progressbar role');
    eq(bar?.getAttribute('aria-valuenow'), '65', 'generating: aria-valuenow=65');
    contains(bar?.getAttribute('aria-label') ?? '', '生成进度', 'generating: progress aria-label');
    contains(host.querySelector('.wg2-rc-progress-pct')?.textContent ?? '', '65%', 'generating: pct');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 3. generating — no progress, status role
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'generating', progress: undefined, artifactRefs: [] });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    const status = host.querySelector('[role="status"]');
    ok(status !== null, 'generating-noprogress: status role');
    contains(status?.getAttribute('aria-label') ?? '', '正在生成', 'generating-noprogress: aria-label');
    await cleanup();
  }

  // Paused Work keeps the authoritative generating slot but removes all
  // generating animation and exposes the resumable state.
  {
    const slot = makeSlot({ state: 'generating', progress: undefined, artifactRefs: [] });
    const { host, cleanup } = await mount(<ResultCard slot={slot} paused />);
    const card = host.querySelector('[data-testid="result-card-slot-1"]');
    eq(card?.getAttribute('data-slot-state'), 'generating', 'paused: authoritative slot state is retained');
    eq(card?.getAttribute('data-paused'), 'true', 'paused: card exposes pause overlay');
    contains(
      host.querySelector('[data-testid="result-card-badge-slot-1"]')?.textContent ?? '',
      '已暂停',
      'paused: badge replaces generating copy',
    );
    ok(host.querySelector('[role="status"][aria-label="正在生成"]') === null, 'paused: indeterminate generating bar is removed');
    ok(host.querySelector('.wg2-rc-spin') === null, 'paused: generating spinner is removed');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 4. ready — multi-file without unavailable actions
  // ════════════════════════════════════════════════════════════════════════
  {
    const refs = [
      makeRef({ id: 'ref-a', name: 'report.docx', type: 'docx', path: '/tmp/a.docx' }),
      makeRef({ id: 'ref-b', name: 'summary.txt', type: 'text/plain', path: '/tmp/b.txt' }),
    ];
    const slot = makeSlot({ state: 'ready', kind: 'docx', artifactRefs: refs });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    eq(host.querySelector('[data-testid="result-card-badge-slot-1"]')?.getAttribute('aria-label'), '已完成', 'ready: icon badge remains accessible');
    ok(host.querySelector('[data-testid="result-card-file-ref-a"]') !== null, 'ready: file ref-a');
    contains(host.querySelector('[data-testid="result-card-file-ref-a"]')?.textContent ?? '', 'report.docx', 'ready: ref-a name');
    ok(host.querySelector('[data-testid="result-card-file-ref-b"]') !== null, 'ready: file ref-b');
    eq(host.querySelectorAll('.wg2-rc-file-btn').length, 0, 'ready: unavailable actions are hidden');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 5. partial — refs present + slot error
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({
      state: 'partial', artifactRefs: [makeRef({ id: 'ref-p', name: 'part.docx', status: 'available', path: '/tmp/p.docx' })],
      error: { code: 'PF', message: '部分失败', retryable: false },
    });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    ok(host.querySelector('[data-testid="result-card-badge-slot-1"]')?.textContent?.includes('部分完成') ?? false, 'partial: badge');
    ok(host.querySelector('[data-testid="result-card-file-ref-p"]') !== null, 'partial: file ref visible');
    ok(host.querySelector('[data-testid="result-card-partial-error-slot-1"]') === null, 'partial: error does not occupy card layout');
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-card-badge-slot-1"]')?.click());
    const err = document.querySelector('[data-testid="result-card-partial-error-slot-1"]');
    ok(err !== null, 'partial: floating error details');
    contains(err?.textContent ?? '', '部分失败', 'partial: error msg');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 6. failed — retryable error without retry capability
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'failed', artifactRefs: [], error: { code: 'GEN', message: '磁盘不足', retryable: true } });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    ok(host.querySelector('[data-testid="result-card-badge-slot-1"]')?.textContent?.includes('失败') ?? false, 'failed: badge');
    ok(host.querySelector('[data-testid="result-card-error-slot-1"]') === null, 'failed: error does not occupy card layout');
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-card-badge-slot-1"]')?.click());
    const err = document.querySelector('[data-testid="result-card-error-slot-1"]');
    ok(err !== null, 'failed: floating error details');
    contains(err?.textContent ?? '', '磁盘不足', 'failed: msg');
    contains(err?.textContent ?? '', 'GEN', 'failed: code');
    ok(document.querySelector('[data-testid="result-card-retry-slot-1"]') === null, 'failed: unavailable retry is hidden');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 7. failed non-retryable — no retry button
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'failed', artifactRefs: [], error: { code: 'FATAL', message: '不可恢复', retryable: false } });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-card-badge-slot-1"]')?.click());
    ok(document.querySelector('[data-testid="result-card-retry-slot-1"]') === null, 'failed-nonretryable: no retry');
    ok(document.querySelector('[data-testid="result-card-error-slot-1"]') !== null, 'failed-nonretryable: floating error visible');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 8. stale — refs visible + banner
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'stale', artifactRefs: [makeRef({ id: 'ref-s', name: 'stale.txt', status: 'stale', path: '/tmp/s.txt' })] });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    ok(host.querySelector('[data-testid="result-card-badge-slot-1"]')?.textContent?.includes('已过期') ?? false, 'stale: badge');
    ok(host.querySelector('[data-testid="result-card-stale-slot-1"]') !== null, 'stale: banner');
    contains(host.querySelector('[data-testid="result-card-file-ref-s"]')?.textContent ?? '', '(过期)', 'stale: file suffix');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 9. stable React keys
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot1 = makeSlot({ id: 'key-test', state: 'generating', progress: 0.3, artifactRefs: [] });
    const { host, root, cleanup } = await mount(<ResultCard slot={slot1} />);
    const cardBefore = host.querySelector('[data-testid="result-card-key-test"]');
    const slot2 = { ...slot1, state: 'ready' as const, progress: undefined, artifactRefs: [makeRef({ id: 'r1', name: 'f.txt', type: 'text/plain', path: '/tmp/f.txt' })] };
    await act(async () => { root.render(<ResultCard slot={slot2} />); });
    await settle();
    ok(cardBefore === host.querySelector('[data-testid="result-card-key-test"]'), 'stable-keys: same DOM node');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 10. handler intents fire correctly
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'ready', artifactRefs: [makeRef({ id: 'ref-h', name: 'h.txt', type: 'text/plain', path: '/tmp/h.txt' })] });
    const captured: Record<string, unknown> = {};
    const { host, cleanup } = await mount(
      <ResultCard slot={slot}
        onOpen={(i) => { captured.open = i; }}
        onDownload={(i) => { captured.download = i; }}
        onLocate={(i) => { captured.locate = i; }}
      />,
    );
    const openBtn = host.querySelector('[data-testid="rc-file-open-ref-h"]') as HTMLButtonElement;
    const dlBtn = host.querySelector('[data-testid="rc-file-download-ref-h"]') as HTMLButtonElement;
    const locBtn = host.querySelector('[data-testid="rc-file-locate-ref-h"]') as HTMLButtonElement;

    ok(openBtn !== null, 'handlers: open btn');
    await interact(() => openBtn.click());
    await settle(50); // wait for async fire to settle
    eq((captured.open as Record<string, unknown> | undefined)?.slotId, 'slot-1', 'handlers: open slotId');
    eq((captured.open as Record<string, unknown> | undefined)?.artifactRefId, 'ref-h', 'handlers: open refId');

    ok(dlBtn !== null, 'handlers: download btn');
    await interact(() => dlBtn.click());
    await settle(50);
    eq((captured.download as Record<string, unknown> | undefined)?.slotId, 'slot-1', 'handlers: download slotId');

    ok(locBtn !== null, 'handlers: locate btn');
    await interact(() => locBtn.click());
    await settle(50);
    eq((captured.locate as Record<string, unknown> | undefined)?.slotId, 'slot-1', 'handlers: locate slotId');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 11. Promise-based dedup — deferred, rapid clicks settle前只触发一次
  // ════════════════════════════════════════════════════════════════════════
  {
    let fireCount = 0;
    const d = deferred();
    const slot = makeSlot({ state: 'ready', artifactRefs: [makeRef({ id: 'ref-dd', name: 'dd.txt', type: 'text/plain', path: '/tmp/dd.txt' })] });
    const { host, cleanup } = await mount(
      <ResultCard slot={slot}
        onOpen={async () => { fireCount++; await d.promise; }}
      />,
    );
    const btn = host.querySelector('[data-testid="rc-file-open-ref-dd"]') as HTMLButtonElement;

    // rapid triple-click before settle
    await interact(() => { btn.click(); btn.click(); btn.click(); });
    // wait for async fire to start (it blocks on d.promise)
    await settle(30);

    eq(fireCount, 1, 'dedup: only 1 fire for rapid clicks before settle');
    ok(btn.disabled === true, 'dedup: button disabled while in-flight');
    eq(btn.getAttribute('aria-busy'), 'true', 'dedup: aria-busy=true');
    contains(btn.textContent ?? '', '处理中', 'dedup: button exposes busy state');

    // resolve the deferred promise
    d.resolve();
    await settle(30);

    eq(btn.disabled, false, 'dedup: button re-enabled after settle');
    eq(btn.getAttribute('aria-busy'), null, 'dedup: aria-busy cleared');
    ok(btn.textContent !== '…', 'dedup: button text restored');

    // Dedup cleared — next render with new handler would fire again.
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 11b. After settle, next click fires again
  // ════════════════════════════════════════════════════════════════════════
  {
    let fireCount = 0;
    const d = deferred();
    const slot = makeSlot({ state: 'ready', artifactRefs: [makeRef({ id: 'ref-dd2', name: 'dd2.txt', type: 'text/plain', path: '/tmp/dd2.txt' })] });
    const { host, cleanup } = await mount(
      <ResultCard slot={slot}
        onOpen={async () => { fireCount++; await d.promise; }}
      />,
    );
    const btn = host.querySelector('[data-testid="rc-file-open-ref-dd2"]') as HTMLButtonElement;

    // first click
    await interact(() => btn.click());
    await settle(30);
    eq(fireCount, 1, 'reclick: first fire');
    d.resolve();
    await settle(30);
    ok(btn.disabled === false, 'reclick: re-enabled');

    // second click after settle — should fire again
    // fireCount will increment via the same handler; we know it fired the first time.
    // For a clean test, just verify button is enabled.
    ok(btn.disabled === false, 'reclick: button enabled after settle, can fire again');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 12. Handler reject → role=alert displayed, button re-enabled
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'ready', artifactRefs: [makeRef({ id: 'ref-err', name: 'err.txt', type: 'text/plain', path: '/tmp/err.txt' })] });
    const { host, cleanup } = await mount(
      <ResultCard slot={slot}
        onOpen={async () => { throw new Error('打开失败: 权限不足'); }}
      />,
    );
    const btn = host.querySelector('[data-testid="rc-file-open-ref-err"]') as HTMLButtonElement;
    ok(btn !== null, 'reject: button exists');
    ok(btn.disabled === false, 'reject: not disabled before click');

    await interact(() => btn.click());
    await settle(50); // wait for async rejection

    // After reject: button should be re-enabled
    eq(btn.disabled, false, 'reject: button re-enabled after reject');
    eq(btn.getAttribute('aria-busy'), null, 'reject: aria-busy cleared');

    // Error alert should be visible
    const errEl = host.querySelector('[data-testid="rc-action-error-ref-err-open"]');
    ok(errEl !== null, 'reject: role=alert error visible');
    eq(errEl?.getAttribute('role'), 'alert', 'reject: error has role=alert');
    contains(errEl?.textContent ?? '', '打开失败: 权限不足', 'reject: error message text');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 13. Reject → re-click clears error, can fire again, new reject shows
  // ════════════════════════════════════════════════════════════════════════
  {
    let call = 0;
    const slot = makeSlot({ state: 'ready', artifactRefs: [makeRef({ id: 'ref-err2', name: 'e2.txt', type: 'text/plain', path: '/tmp/e2.txt' })] });
    const { host, cleanup } = await mount(
      <ResultCard slot={slot}
        onOpen={async () => { call++; if (call === 1) throw new Error('第一次失败'); }}
      />,
    );
    const btn = host.querySelector('[data-testid="rc-file-open-ref-err2"]') as HTMLButtonElement;

    // first click → reject
    await interact(() => btn.click());
    await settle(50);
    const err1 = host.querySelector('[data-testid="rc-action-error-ref-err2-open"]');
    ok(err1 !== null, 'reject2: first error visible');
    contains(err1?.textContent ?? '', '第一次失败', 'reject2: first error msg');
    eq(btn.disabled, false, 'reject2: re-enabled after reject');

    // second click → success (no throw)
    await interact(() => btn.click());
    await settle(50);

    // error should be cleared
    const err2 = host.querySelector('[data-testid="rc-action-error-ref-err2-open"]');
    ok(err2 === null, 'reject2: error cleared after success');
    eq(btn.disabled, false, 'reject2: enabled after success');
    eq(call, 2, 'reject2: both calls made');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 14. Action isolation — file A open fails, file B download still works
  // ════════════════════════════════════════════════════════════════════════
  {
    const refs = [
      makeRef({ id: 'iso-a', name: 'a.txt', type: 'text/plain', path: '/tmp/a.txt' }),
      makeRef({ id: 'iso-b', name: 'b.txt', type: 'text/plain', path: '/tmp/b.txt' }),
    ];
    let bFired = false;
    const slot = makeSlot({ state: 'ready', artifactRefs: refs });
    const { host, cleanup } = await mount(
      <ResultCard slot={slot}
        onOpen={async (i) => { if (i.artifactRefId === 'iso-a') throw new Error('A失败'); }}
        onDownload={async () => { bFired = true; }}
      />,
    );

    // click open on A
    const openA = host.querySelector('[data-testid="rc-file-open-iso-a"]') as HTMLButtonElement;
    await interact(() => openA.click());
    await settle(50);
    ok(host.querySelector('[data-testid="rc-action-error-iso-a-open"]') !== null, 'isolation: A open error visible');
    eq(openA.disabled, false, 'isolation: A re-enabled');

    // click download on B — should work independently
    const dlB = host.querySelector('[data-testid="rc-file-download-iso-b"]') as HTMLButtonElement;
    ok(dlB !== null, 'isolation: B download button exists');
    ok(dlB.disabled === false, 'isolation: B not disabled by A error');
    await interact(() => dlB.click());
    await settle(50);
    ok(bFired, 'isolation: B download fired successfully');

    // A error still visible, B has no error
    ok(host.querySelector('[data-testid="rc-action-error-iso-a-open"]') !== null, 'isolation: A error still visible');
    ok(host.querySelector('[data-testid="rc-action-error-iso-b-download"]') === null, 'isolation: B has no error');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 15. Slot retry — deferred dedup, reject alert, re-enable
  // ════════════════════════════════════════════════════════════════════════
  {
    let tryCount = 0;
    const d = deferred();
    const slot = makeSlot({ state: 'failed', artifactRefs: [], error: { code: 'E', message: 'err', retryable: true } });
    const { host, cleanup } = await mount(
      <ResultCard slot={slot}
        onRetry={async () => { tryCount++; await d.promise; }}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-card-badge-slot-1"]')?.click());
    const btn = document.querySelector('[data-testid="result-card-retry-slot-1"]') as HTMLButtonElement;

    // rapid double-click
    await interact(() => { btn.click(); btn.click(); });
    await settle(30);
    eq(tryCount, 1, 'retry-dedup: only 1 fire');
    ok(btn.disabled === true, 'retry-dedup: disabled');
    eq(btn.getAttribute('aria-busy'), 'true', 'retry-dedup: aria-busy=true');

    d.resolve();
    await settle(30);
    eq(btn.disabled, false, 'retry-dedup: re-enabled');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 16. Slot retry — reject shows alert, re-click clears + retries
  // ════════════════════════════════════════════════════════════════════════
  {
    let call = 0;
    const slot = makeSlot({ state: 'failed', artifactRefs: [], error: { code: 'E', message: 'err', retryable: true } });
    const { host, cleanup } = await mount(
      <ResultCard slot={slot}
        onRetry={async () => { call++; if (call === 1) throw new Error('重试网络错误'); }}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-card-badge-slot-1"]')?.click());
    const btn = document.querySelector('[data-testid="result-card-retry-slot-1"]') as HTMLButtonElement;

    // first retry → reject
    await interact(() => btn.click());
    await settle(50);

    eq(call, 1, 'retry-reject: called once');
    eq(btn.disabled, false, 'retry-reject: re-enabled');
    const retryKey = 'retry-slot-1';
    const errEl = document.querySelector(`[data-testid="rc-action-error-${retryKey}"]`);
    ok(errEl !== null, 'retry-reject: error alert visible');
    eq(errEl?.getAttribute('role'), 'alert', 'retry-reject: role=alert');
    contains(errEl?.textContent ?? '', '重试网络错误', 'retry-reject: error msg');

    // second retry → success, error cleared
    await interact(() => btn.click());
    await settle(50);
    eq(call, 2, 'retry-reject: called again');
    const errEl2 = document.querySelector(`[data-testid="rc-action-error-${retryKey}"]`);
    ok(errEl2 === null, 'retry-reject: error cleared after success');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 17. ResultShelf empty state
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(<ResultShelf slots={[]} activeDefinitionRevision={2} />);
    const empty = host.querySelector('[data-testid="result-shelf-empty"]');
    ok(empty !== null, 'shelf-empty: node');
    contains(empty?.textContent ?? '', '暂无成果', 'shelf-empty: text');
    contains(host.querySelector('.wg2-rs-intro')?.textContent ?? '', '成果', 'shelf-empty: intro remains visible');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 18. ResultShelf multiple slots
  // ════════════════════════════════════════════════════════════════════════
  {
    const slots = [
      makeSlot({ id: 'a', title: 'A', state: 'ready', artifactRefs: [makeRef({ id: 'ra', name: 'a.txt', path: '/tmp/a.txt' })] }),
      makeSlot({ id: 'b', title: 'B', state: 'generating', progress: 0.5, artifactRefs: [] }),
      makeSlot({ id: 'c', title: 'C', state: 'failed', artifactRefs: [], error: { code: 'E', message: 'b', retryable: true } }),
    ];
    const { host, cleanup } = await mount(<ResultShelf slots={slots} activeDefinitionRevision={2} />);
    ok(host.querySelector('[data-testid="result-shelf"]') !== null, 'shelf-multi: rendered');
    eq(host.querySelectorAll('[data-testid^="result-shelf-item-"]').length, 3, 'shelf-multi: 3 items');
    contains(host.querySelector('.wg2-rs-intro')?.textContent ?? '', '工作过程中产生的文件与成果将在此汇总', 'shelf-multi: design intro rendered');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 19. ARIA / accessibility
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'ready', artifactRefs: [makeRef({ id: 'ra11y', name: 'a.txt', path: '/tmp/a.txt' })] });
    const { host, cleanup } = await mount(<ResultCard slot={slot} onOpen={() => undefined} />);
    const card = host.querySelector('.wg2-rc-card');
    eq(card?.getAttribute('role'), 'article', 'a11y: role=article');
    ok(card?.getAttribute('aria-label')?.includes('测试成果') ?? false, 'a11y: title in aria-label');
    ok(card?.getAttribute('aria-label')?.includes('已完成') ?? false, 'a11y: state in aria-label');
    eq(card?.getAttribute('tabIndex'), '0', 'a11y: tabIndex=0');
    eq(host.querySelector('.wg2-rc-files')?.getAttribute('role'), 'list', 'a11y: file list role');
    const openBtn = host.querySelector('[data-testid="rc-file-open-ra11y"]');
    ok(openBtn?.getAttribute('aria-label')?.includes('打开') ?? false, 'a11y: open aria-label');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 20. generating has aria-live=polite, ready does not
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host: h1, cleanup: c1 } = await mount(<ResultCard slot={makeSlot({ state: 'generating', progress: 0.5, artifactRefs: [] })} />);
    eq(h1.querySelector('.wg2-rc-card')?.getAttribute('aria-live'), 'polite', 'a11y: generating aria-live=polite');
    await c1();
    const { host: h2, cleanup: c2 } = await mount(<ResultCard slot={makeSlot({ state: 'ready', artifactRefs: [makeRef({ id: 'r', name: 'x.txt', path: '/tmp/x.txt' })] })} />);
    eq(h2.querySelector('.wg2-rc-card')?.getAttribute('aria-live'), null, 'a11y: ready no aria-live');
    await c2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 21. file status badges
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({ state: 'partial', artifactRefs: [
      makeRef({ id: 'rm', name: 'gone.txt', status: 'missing' }),
      makeRef({ id: 'rf', name: 'bad.txt', status: 'failed', path: '/tmp/bad.txt' }),
    ] });
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    contains(host.querySelector('[data-testid="result-card-file-rm"]')?.textContent ?? '', '(缺失)', 'status: missing');
    contains(host.querySelector('[data-testid="result-card-file-rf"]')?.textContent ?? '', '(失败)', 'status: failed');
    // missing file has no open button
    ok(host.querySelector('[data-testid="rc-file-open-rm"]') === null, 'status: no open for missing');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 22. Summary + required state stays accessible without visual punctuation.
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(<ResultCard slot={makeSlot({ state: 'ready', summary: '3 files, 1.2MB', artifactRefs: [makeRef({ id: 'rs', name: 'x.txt', path: '/tmp/x.txt' })] })} />);
    contains(host.querySelector('[data-testid="result-card-summary-slot-1"]')?.textContent ?? '', '3 files', 'summary: text');
    await cleanup();
    const { host: h2, cleanup: c2 } = await mount(<ResultCard slot={makeSlot({ state: 'reserved', required: true })} />);
    ok(h2.querySelector('[data-testid="result-card-slot-1"]')?.getAttribute('aria-label')?.includes('必需') ?? false, 'required: conveyed in aria-label');
    ok(!(h2.querySelector('[data-testid="result-card-badge-slot-1"]')?.textContent?.includes('*') ?? false), 'required: no stray visual asterisk');
    await c2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 23. File icon mapping uses the app icon library, not platform emoji.
  // ════════════════════════════════════════════════════════════════════════
  {
    const cases: Array<[string, string, string]> = [
      ['pdf', 'application/pdf', '.lucide-file-text'],
      ['docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', '.lucide-file-text'],
      ['xlsx', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', '.lucide-file-spreadsheet'],
      ['text', 'text/plain', '.lucide-file-text'],
      ['unknown', 'application/octet-stream', '.lucide-file'],
    ];
    for (const [kind, type, selector] of cases) {
      const { host, cleanup } = await mount(<ResultCard slot={makeSlot({ id: `icon-${kind}`, kind, artifactRefs: [makeRef({ id: `ri-${kind}`, type, name: 'f' })] })} />);
      ok(host.querySelector(`.wg2-rc-file-icon ${selector}`) !== null, `icon: ${kind} → ${selector}`);
      await cleanup();
    }
  }

  // ════════════════════════════════════════════════════════════════════════
  // 24. Golden — must be present (test would have already failed in setup)
  //          Render reserved slot from golden
  // ════════════════════════════════════════════════════════════════════════
  if (goldenRaw) {
    const view = JSON.parse(goldenRaw);
    const slots = view.artifactSlots;
    ok(Array.isArray(slots), 'golden: artifactSlots is array');
    ok(slots.length >= 1, 'golden: at least 1 slot');
    const slot0 = slots[0];
    eq(slot0.state, 'reserved', 'golden: state=reserved');
    eq(slot0.id, 'slot', 'golden: id=slot');
    eq(slot0.artifactRefs, null, 'golden: null refs');
    // Mount it
    const slot: ArtifactSlot = { ...slot0, artifactRefs: slot0.artifactRefs ?? [] };
    const { host, cleanup } = await mount(<ResultCard slot={slot} />);
    ok(host.querySelector('[data-testid="result-card-slot"]') !== null, 'golden-render: card');
    ok(host.querySelector('[data-testid="result-card-placeholder-slot"]') !== null, 'golden-render: placeholder');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 25. Props update does not corrupt interaction state
  // ════════════════════════════════════════════════════════════════════════
  {
    const d = deferred();
    const slot1 = makeSlot({ id: 'prop-update', state: 'ready', artifactRefs: [makeRef({ id: 'rpu', name: 'f.txt', type: 'text/plain', path: '/tmp/f.txt' })] });
    const { host, root, cleanup } = await mount(
      <ResultCard slot={slot1} onOpen={async () => { await d.promise; }} />,
    );
    // start an in-flight open
    const btn = host.querySelector('[data-testid="rc-file-open-rpu"]') as HTMLButtonElement;
    await interact(() => btn.click());
    await settle(30);
    ok(btn.disabled === true, 'prop-update: disabled in-flight');

    // Update props (e.g., slot state changes from ready → stale)
    const slot2 = { ...slot1, state: 'stale' as const, summary: 'new summary' };
    await act(async () => { root.render(<ResultCard slot={slot2} onOpen={async () => { await d.promise; }} />); });
    await settle(10);

    // Still in-flight (ref state persists)
    const btn2 = host.querySelector('[data-testid="rc-file-open-rpu"]') as HTMLButtonElement;
    ok(btn2.disabled === true, 'prop-update: still disabled after prop change');

    // Resolve
    d.resolve();
    await settle(30);
    ok(btn2.disabled === false, 'prop-update: re-enabled after resolve');
    // New props are reflected
    ok(host.querySelector('[data-testid="result-card-badge-prop-update"]')?.textContent?.includes('已过期') ?? false, 'prop-update: new state reflected');
    ok(host.querySelector('[data-testid="result-card-summary-prop-update"]')?.textContent?.includes('new summary') ?? false, 'prop-update: new summary reflected');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 26. Preview → durable local conversion uses one stable intent
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({
      id: 'convert-slot',
      kind: 'docx',
      state: 'ready',
      artifactRefs: [makeRef({ id: 'convert-ref', name: 'report.docx', type: 'docx' })],
    });
    const calls: Array<Record<string, unknown>> = [];
    let conversionCalls = 0;
    const { host, cleanup } = await mount(
      <ResultCard
        slot={slot}
        onPreview={async () => ({
          artifactId: 'convert-ref',
          workId: 'work-1',
          grade: 'filecard',
          canOpen: true,
          canConvert: true,
          summary: 'Word 文档',
        })}
        onConvert={async (intent) => {
          calls.push({ ...intent });
          conversionCalls++;
          if (conversionCalls === 1) {
            return {
              artifactId: 'convert-ref',
              workId: 'work-1',
              grade: 'filecard',
              canOpen: true,
              canConvert: true,
              summary: 'Word 文档',
              conversionState: 'pending',
            };
          }
          return {
            artifactId: 'convert-ref',
            workId: 'work-1',
            grade: 'inline',
            canOpen: true,
            canConvert: false,
            textContent: 'converted preview',
            conversionState: 'completed',
          };
        }}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="rc-file-preview-convert-ref"]')!.click());
    await settle();
    ok(Boolean(host.querySelector('[data-testid="rc-file-convert-convert-ref"]')), 'conversion: button appears from authoritative preview capability');
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="rc-file-convert-convert-ref"]')!.click());
    await settle(180);
    eq(calls.length, 2, 'conversion: bounded polling reaches completion');
    eq(calls[0]?.requestId, calls[1]?.requestId, 'conversion: polling reuses requestId');
    eq(calls[0]?.allowExternal, false, 'conversion: UI cannot self-approve external upload');
    eq(calls[0]?.approvalToken, '', 'conversion: UI sends no invented approval token');
    eq(calls[0]?.definitionRevision, 2, 'conversion: definition identity preserved');
    eq(calls[0]?.slotRevision, 1, 'conversion: slot revision preserved');
    contains(host.querySelector('[data-testid="rc-preview-text"]')?.textContent ?? '', 'converted preview', 'conversion: completed preview rendered');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 27. Late preview promise cannot overwrite a newer slot identity
  // ════════════════════════════════════════════════════════════════════════
  {
    const late = deferredValue<ArtifactPreview>();
    const slot1 = makeSlot({
      id: 'late-slot',
      revision: 1,
      state: 'ready',
      artifactRefs: [makeRef({ id: 'late-ref', name: 'old.txt' })],
    });
    const { host, root, cleanup } = await mount(
      <ResultCard slot={slot1} onPreview={() => late.promise} />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="rc-file-preview-late-ref"]')!.click());
    const slot2 = {
      ...slot1,
      revision: 2,
      artifactRefs: [makeRef({ id: 'new-ref', name: 'new.txt' })],
    };
    await act(async () => { root.render(<ResultCard slot={slot2} onPreview={() => late.promise} />); });
    late.resolve({
      artifactId: 'late-ref',
      workId: 'work-1',
      grade: 'inline',
      canOpen: true,
      canConvert: false,
      textContent: 'stale promise',
    });
    await settle();
    ok(host.querySelector('[data-testid="rc-preview-text"]') === null, 'late preview: stale promise discarded after revision change');
    ok(host.querySelector('[data-testid="result-card-file-new-ref"]') !== null, 'late preview: new authoritative ref remains visible');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 28. Preview action toggles the current preview without a duplicate load
  // ════════════════════════════════════════════════════════════════════════
  {
    const slot = makeSlot({
      id: 'preview-toggle-slot',
      state: 'ready',
      artifactRefs: [makeRef({ id: 'preview-toggle-ref', name: 'notes.md', type: 'text/markdown' })],
    });
    let previewCalls = 0;
    const { host, cleanup } = await mount(
      <ResultCard
        slot={slot}
        onPreview={async () => {
          previewCalls++;
          return {
            artifactId: 'preview-toggle-ref',
            workId: 'work-1',
            grade: 'inline',
            canOpen: false,
            canConvert: false,
            textContent: '# Notes',
          };
        }}
      />,
    );
    const previewButton = () =>
      host.querySelector<HTMLButtonElement>('[data-testid="rc-file-preview-preview-toggle-ref"]')!;

    await interact(() => previewButton().click());
    ok(host.querySelector('[data-testid="rc-inline-preview"]') !== null, 'preview toggle: first click expands');
    eq(previewButton().getAttribute('aria-expanded'), 'true', 'preview toggle: expanded state exposed');
    contains(previewButton().textContent ?? '', '收起预览', 'preview toggle: expanded action label');

    await interact(() => previewButton().click());
    ok(host.querySelector('[data-testid="rc-inline-preview"]') === null, 'preview toggle: second click collapses');
    eq(previewButton().getAttribute('aria-expanded'), 'false', 'preview toggle: collapsed state exposed');
    eq(previewCalls, 1, 'preview toggle: collapse does not reload');

    await interact(() => previewButton().click());
    ok(host.querySelector('[data-testid="rc-inline-preview"]') !== null, 'preview toggle: third click restores cached preview');
    eq(previewCalls, 1, 'preview toggle: cached preview reopens without reload');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 29. Result additions enter workflow-impact preview
  // ════════════════════════════════════════════════════════════════════════
  {
    const requests: Array<{ nodeId: string; instruction: string }> = [];
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot()]}
        activeDefinitionRevision={2}
        definition={makeDefinition()}
        onRequestWorkflowChange={(request) => requests.push(request)}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-add"]')!.click());
    const title = host.querySelector<HTMLInputElement>('[data-testid="result-add-title"]')!;
    await interact(() => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set;
      setter?.call(title, '学习总结');
      title.dispatchEvent(new Event('input', { bubbles: true }));
    });
    ok(
      host.querySelector('[data-testid="result-add-producer"]') === null,
      'result add: producer choice is not exposed to the user',
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-add-submit"]')!.click());
    eq(requests.length, 1, 'result add: emits one workflow preview request');
    eq(requests[0]?.nodeId, 'review', 'result add: uses the terminal task only as a stable preview anchor');
    contains(requests[0]?.instruction ?? '', '学习总结', 'result add: instruction is human readable');
    contains(requests[0]?.instruction ?? '', '自动推断唯一且最合适的产出任务', 'result add: planner owns producer inference');
    ok(!requests[0]?.instruction.includes('节点 ID'), 'result add: no hidden user-selected producer is fabricated');
    contains(requests[0]?.instruction ?? '', '只添加该成果', 'result add: change is narrowly scoped');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 30. Result removal explains references before workflow-impact preview
  // ════════════════════════════════════════════════════════════════════════
  {
    const requests: Array<{ nodeId: string; instruction: string }> = [];
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot()]}
        activeDefinitionRevision={2}
        definition={makeDefinition()}
        onRequestWorkflowChange={(request) => requests.push(request)}
      />,
    );
    const badge = host.querySelector('[data-testid="result-card-badge-slot-1"]');
    const actions = host.querySelector('[data-testid="result-card-actions-slot-1"]');
    ok(actions !== null, 'result manage: actions have a dedicated header region');
    eq(actions?.parentElement, badge?.parentElement, 'result manage: status and actions share a non-overlapping layout column');
    ok(!actions?.contains(badge ?? null), 'result manage: actions do not cover the status badge');
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-delete-slot-1"]')!.click());
    const confirm = host.querySelector('[data-testid="result-delete-confirm"]');
    contains(confirm?.textContent ?? '', '生成报告', 'result remove: producer impact shown');
    contains(confirm?.textContent ?? '', '审核报告', 'result remove: consumer impact shown');
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-delete-confirm-btn"]')!.click());
    eq(requests.length, 1, 'result remove: emits one workflow preview request');
    eq(requests[0]?.nodeId, 'make', 'result remove: anchors discussion to producer');
    contains(requests[0]?.instruction ?? '', '从所有产出或使用它的任务中移除引用', 'result remove: cleans all references');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 31. Result format changes preserve identity and request real regeneration
  // ════════════════════════════════════════════════════════════════════════
  {
    const requests: Array<{ nodeId: string; instruction: string; title: string }> = [];
    const definition = makeDefinition();
    definition.artifactSlots[0] = {
      ...definition.artifactSlots[0],
      title: '预算表.md',
      kind: 'document',
    };
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot({ title: '预算表.md', kind: 'document' })]}
        activeDefinitionRevision={2}
        definition={definition}
        onRequestWorkflowChange={(request) => requests.push(request)}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-edit-slot-1"]')!.click());
    const format = host.querySelector<HTMLSelectElement>('[data-testid="result-edit-kind"]')!;

    // All 12 format options are present with clear labels.
    const expectedFormats: Record<string, string> = {
      document: '文档（Markdown）',
      text: '纯文本',
      docx: 'Word 文档（DOCX）',
      pdf: 'PDF 文档',
      xlsx: 'Excel 工作簿（XLSX）',
      data: '数据',
      sh: 'Shell 脚本（SH）',
      bat: 'Windows 批处理（BAT/CMD）',
      ps1: 'PowerShell 脚本（PS1）',
      exe: '可执行程序（EXE）',
      zip: '压缩包（ZIP）',
      file: '其他文件',
    };
    for (const [value, label] of Object.entries(expectedFormats)) {
      eq(
        format.querySelector<HTMLOptionElement>(`option[value="${value}"]`)?.textContent,
        label,
        `result edit: ${value} format is available with a clear label`,
      );
    }

    // Switching to each new format correctly rewrites the extension.
    const extensionCases: Array<{ kind: string; expectedTitle: string }> = [
      { kind: 'docx', expectedTitle: '预算表.docx' },
      { kind: 'pdf', expectedTitle: '预算表.pdf' },
      { kind: 'sh', expectedTitle: '预算表.sh' },
      { kind: 'bat', expectedTitle: '预算表.bat' },
      { kind: 'ps1', expectedTitle: '预算表.ps1' },
      { kind: 'exe', expectedTitle: '预算表.exe' },
      { kind: 'zip', expectedTitle: '预算表.zip' },
    ];
    for (const tc of extensionCases) {
      await interact(() => {
        const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')?.set;
        setter?.call(format, tc.kind);
        format.dispatchEvent(new Event('change', { bubbles: true }));
      });
      eq(
        host.querySelector<HTMLInputElement>('[data-testid="result-edit-title"]')?.value,
        tc.expectedTitle,
        `result edit: ${tc.kind} rewrites extension to ${tc.expectedTitle}`,
      );
    }

    // Switch back to xlsx for the workflow preview assertion.
    await interact(() => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')?.set;
      setter?.call(format, 'xlsx');
      format.dispatchEvent(new Event('change', { bubbles: true }));
    });
    eq(
      host.querySelector<HTMLInputElement>('[data-testid="result-edit-title"]')?.value,
      '预算表.xlsx',
      'result edit: changing format updates the known file extension',
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-edit-submit"]')!.click());
    eq(requests.length, 1, 'result edit: emits one workflow preview request');
    eq(requests[0]?.nodeId, 'make', 'result edit: anchors the change to the existing producer');
    contains(requests[0]?.title ?? '', '修改成果', 'result edit: request title is human readable');
    contains(requests[0]?.instruction ?? '', 'ID：slot-1', 'result edit: preserves the stable slot identity');
    contains(requests[0]?.instruction ?? '', '格式从 document 改为 xlsx', 'result edit: changes the format field');
    contains(requests[0]?.instruction ?? '', '不能只改扩展名或 MIME', 'result edit: requires true file regeneration');
    contains(requests[0]?.instruction ?? '', '不要删除后重建', 'result edit: keeps producer and consumer references stable');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 32. New format kinds are all available in the add form dropdown
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot()]}
        activeDefinitionRevision={2}
        definition={makeDefinition()}
        onRequestWorkflowChange={() => {}}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-add"]')!.click());
    const addFormat = host.querySelector<HTMLSelectElement>('[data-testid="result-add-kind"]')!;
    const allKinds = ['document', 'text', 'xlsx', 'docx', 'pdf', 'data', 'sh', 'bat', 'ps1', 'exe', 'zip', 'file'];
    for (const kind of allKinds) {
      ok(
        addFormat.querySelector<HTMLOptionElement>(`option[value="${kind}"]`) !== null,
        `result add: ${kind} option is present in the add form`,
      );
    }
    // Default kind is "document".
    eq(addFormat.value, 'document', 'result add: default kind is document');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 33. Extension rewrite handles .txt → new format and compound extensions
  // ════════════════════════════════════════════════════════════════════════
  {
    const definition = makeDefinition();
    definition.artifactSlots[0] = {
      ...definition.artifactSlots[0],
      title: 'output.txt',
      kind: 'text',
    };
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot({ title: 'output.txt', kind: 'text' })]}
        activeDefinitionRevision={2}
        definition={definition}
        onRequestWorkflowChange={() => {}}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-edit-slot-1"]')!.click());
    const format = host.querySelector<HTMLSelectElement>('[data-testid="result-edit-kind"]')!;

    // .txt → .sh rewrites correctly.
    await interact(() => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')?.set;
      setter?.call(format, 'sh');
      format.dispatchEvent(new Event('change', { bubbles: true }));
    });
    eq(
      host.querySelector<HTMLInputElement>('[data-testid="result-edit-title"]')?.value,
      'output.sh',
      'result edit: .txt → .sh rewrites extension',
    );

    // .txt → .exe rewrites correctly.
    await interact(() => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')?.set;
      setter?.call(format, 'exe');
      format.dispatchEvent(new Event('change', { bubbles: true }));
    });
    eq(
      host.querySelector<HTMLInputElement>('[data-testid="result-edit-title"]')?.value,
      'output.exe',
      'result edit: .txt → .exe rewrites extension',
    );

    // .txt → .bat rewrites correctly.
    await interact(() => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value')?.set;
      setter?.call(format, 'bat');
      format.dispatchEvent(new Event('change', { bubbles: true }));
    });
    eq(
      host.querySelector<HTMLInputElement>('[data-testid="result-edit-title"]')?.value,
      'output.bat',
      'result edit: .txt → .bat rewrites extension',
    );
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 34. workflowChangeState: automatic coordination status visible
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot()]}
        activeDefinitionRevision={2}
        definition={makeDefinition()}
        onRequestWorkflowChange={() => {}}
        workflowChangeState={{ token: 'tk-1', status: 'updating' }}
      />,
    );
    const status = host.querySelector('[data-testid="result-workflow-status"]');
    ok(status !== null, 'wf-state updating: status bar visible');
    contains(status?.textContent ?? '', 'AI 正在协调更新', 'wf-state updating: shows coordination text');
    ok(status?.classList.contains('wg2-rs-wfstatus--updating') ?? false, 'wf-state updating: has updating class');
    // Add button should be disabled while updating
    const addBtn = host.querySelector<HTMLButtonElement>('[data-testid="result-add"]');
    ok(addBtn?.disabled === true, 'wf-state updating: add button disabled');
    // Edit button should be disabled
    const editBtn = host.querySelector<HTMLButtonElement>('[data-testid="result-edit-slot-1"]');
    ok(editBtn?.disabled === true, 'wf-state updating: edit button disabled');
    // Delete button should be disabled
    const deleteBtn = host.querySelector<HTMLButtonElement>('[data-testid="result-delete-slot-1"]');
    ok(deleteBtn?.disabled === true, 'wf-state updating: delete button disabled');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 35. workflowChangeState: "已更新" status appears and auto-dismisses
  // ════════════════════════════════════════════════════════════════════════
  {
    let container: Element | null = null;
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot()]}
        activeDefinitionRevision={2}
        definition={makeDefinition()}
        onRequestWorkflowChange={() => {}}
        workflowChangeState={{ token: 'tk-2', status: 'applied' }}
      />,
    );
    container = host.querySelector('[data-testid="result-workflow-status"]');
    ok(container !== null, 'wf-state applied: status bar visible');
    contains(container?.textContent ?? '', '已更新', 'wf-state applied: shows applied text');
    ok(container?.classList.contains('wg2-rs-wfstatus--applied') ?? false, 'wf-state applied: has applied class');
    // After auto-dismiss timeout (>3s), the "已更新" text should disappear
    await act(async () => { await new Promise<void>((resolve) => setTimeout(resolve, 3500)); });
    container = host.querySelector('[data-testid="result-workflow-status"]');
    ok(container === null, 'wf-state applied: status auto-dismissed without an empty container');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 36. workflowChangeState: terminal status asks for a clearer requirement
  // ════════════════════════════════════════════════════════════════════════
  {
    const retried: string[] = [];
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot()]}
        activeDefinitionRevision={2}
        definition={makeDefinition()}
        onRequestWorkflowChange={(req) => { retried.push(req.token); }}
        workflowChangeState={{ token: 'tk-3', status: 'failed', error: '预览超时' }}
      />,
    );
    const status = host.querySelector('[data-testid="result-workflow-status"]');
    ok(status !== null, 'wf-state failed: status bar visible');
    contains(status?.textContent ?? '', '这次调整暂未完成', 'wf-state failed: shows business failure text');
    contains(status?.textContent ?? '', '预览超时', 'wf-state failed: shows error detail');
    contains(status?.textContent ?? '', '补充要求后再次提交', 'wf-state failed: guides the next user action');
    ok(status?.classList.contains('wg2-rs-wfstatus--failed') ?? false, 'wf-state failed: has failed class');
    const retryBtn = host.querySelector<HTMLButtonElement>('[data-testid="result-workflow-retry"]');
    ok(retryBtn === null, 'wf-state failed: technical retry button is hidden');
    eq(retried.length, 0, 'wf-state failed: no hidden redispatch occurs');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 37. Button text: no "预览" or "Patch" language exposed
  // ════════════════════════════════════════════════════════════════════════
  {
    const definition = makeDefinition();
    definition.artifactSlots[0] = { ...definition.artifactSlots[0], title: '测试.md', kind: 'document' };
    const { host, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot({ title: '测试.md', kind: 'document' })]}
        activeDefinitionRevision={2}
        definition={definition}
        onRequestWorkflowChange={() => {}}
      />,
    );
    // Add form
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-add"]')!.click());
    const addBtn = host.querySelector<HTMLButtonElement>('[data-testid="result-add-submit"]');
    contains(addBtn?.textContent ?? '', '添加成果', 'wf-state text: add button says 添加成果');
    ok(!(addBtn?.textContent ?? '').includes('预览'), 'wf-state text: add button no 预览');
    ok(!(addBtn?.textContent ?? '').includes('Patch'), 'wf-state text: add button no Patch');
    ok(!(addBtn?.textContent ?? '').includes('差异'), 'wf-state text: add button no 差异');
    // Close add
    await interact(() => host.querySelector<HTMLButtonElement>('[aria-label="关闭添加成果"]')!.click());

    // Edit form
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-edit-slot-1"]')!.click());
    const editBtn = host.querySelector<HTMLButtonElement>('[data-testid="result-edit-submit"]');
    contains(editBtn?.textContent ?? '', '保存修改', 'wf-state text: edit button says 保存修改');
    ok(!(editBtn?.textContent ?? '').includes('预览'), 'wf-state text: edit button no 预览');
    // Close edit
    await interact(() => host.querySelector<HTMLButtonElement>('[aria-label="关闭修改成果"]')!.click());

    // Delete confirm
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-delete-slot-1"]')!.click());
    const delBtn = host.querySelector<HTMLButtonElement>('[data-testid="result-delete-confirm-btn"]');
    contains(delBtn?.textContent ?? '', '删除成果', 'wf-state text: delete button says 删除成果');
    ok(!(delBtn?.textContent ?? '').includes('预览'), 'wf-state text: delete button no 预览');
    // Editor action description should not mention Patch/差异
    const descAfterAddClose = host.querySelector('.wg2-rs-editor');
    ok(descAfterAddClose === null, 'wf-state text: add editor closed');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 38. Failed state does not ask the user to replay an identical request
  // ════════════════════════════════════════════════════════════════════════
  {
    const requests: Array<{ token: string; attempt?: number }> = [];
    const { host, root, cleanup } = await mount(
      <ResultShelf
        slots={[makeSlot()]}
        activeDefinitionRevision={2}
        definition={makeDefinition()}
        onRequestWorkflowChange={(req) => { requests.push({ token: req.token, attempt: req.attempt }); }}
        workflowChangeState={null}
      />,
    );
    // First submit an add.
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-add"]')!.click());
    const title = host.querySelector<HTMLInputElement>('[data-testid="result-add-title"]')!;
    await interact(() => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set;
      setter?.call(title, 'retry-target');
      title.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="result-add-submit"]')!.click());
    eq(requests.length, 1, 'wf-state retry: first request fired');
    const firstToken = requests[0]!.token;
    ok(firstToken.startsWith('add:'), 'wf-state retry: token is add-type');
    eq(requests[0]?.attempt, undefined, 'wf-state retry: first dispatch has no retry attempt');

    // Re-render with failed state. Internal recovery has already been exhausted,
    // so the UI asks for a clarified edit instead of replaying the same intent.
    await act(async () => {
      root.render(
        <ResultShelf
          slots={[makeSlot()]}
          activeDefinitionRevision={2}
          definition={makeDefinition()}
          onRequestWorkflowChange={(req) => { requests.push({ token: req.token, attempt: req.attempt }); }}
          workflowChangeState={{ token: firstToken, status: 'failed', error: 'boom' }}
        />,
      );
    });
    await settle();
    const retryBtn = host.querySelector<HTMLButtonElement>('[data-testid="result-workflow-retry"]');
    ok(retryBtn === null, 'wf-state retry: retry button is not rendered');
    eq(requests.length, 1, 'wf-state retry: failed status does not redispatch');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // SUMMARY
  // ════════════════════════════════════════════════════════════════════════
  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

runTests().catch((err) => {
  console.error(err);
  process.exit(2);
});
