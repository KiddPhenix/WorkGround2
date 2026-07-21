// Run: tsx src/__tests__/block-renderers.test.tsx
// Tests all six builtin WorkBlock renderers: table, chart, graph, code, markdown, artifact.
// Covers registration, validation, lazy load, render, security, and edge cases.

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { BlockHost } from '../components/work/blocks/BlockHost';
import { blockRegistry } from '../components/work/blocks/registry';
import { registerBuiltinBlocks } from '../components/work/blocks/register';
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

function notContains(value: string, part: string, label: string): void {
  ok(!value.includes(part), `${label}${value.includes(part) ? ` (found ${JSON.stringify(part)})` : ''}`);
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

async function click(element: HTMLElement): Promise<void> {
  await act(async () => { element.click(); });
  await flush();
}

async function waitFor(predicate: () => boolean, label: string, ticks = 40): Promise<void> {
  for (let index = 0; index < ticks; index++) {
    if (predicate()) return;
    await flush();
  }
  ok(false, `${label} timed out`);
}

const context = { workId: 'work-test', workSchemaVersion: 1 };

function blk(patch: Partial<BlockInstance> = {}): BlockInstance {
  return {
    id: 'b-1',
    kind: 'table',
    schemaVersion: 1,
    revision: 1,
    status: 'ready',
    data: {},
    source: { provider: 'controller', ref: 's-1', mode: 'snapshot', verified: true },
    fallback: { summary: 'Test block' },
    createdAt: '2026-07-20T10:00:00Z',
    updatedAt: '2026-07-20T11:00:00Z',
    ...patch,
  };
}

// ── Valid data fixtures ─────────────────────────────────────────────────────

const validTable = {
  columns: [
    { key: 'name', title: 'Name' },
    { key: 'age', title: 'Age' },
    { key: 'score', title: 'Score', type: 'number' },
  ],
  rows: [
    { name: 'Alice', age: 30, score: 95 },
    { name: 'Bob', age: 25, score: 88 },
    { name: 'Charlie', age: 35, score: 72 },
  ],
};

const validChartBar = {
  type: 'bar' as const,
  series: [{ label: 'Sales', values: [10, 20, 15, 25] }],
  axes: { x: { labels: ['Q1', 'Q2', 'Q3', 'Q4'] } },
};

const validChartLine = {
  type: 'line' as const,
  series: [
    { label: 'A', values: [1, 3, 2, 4] },
    { label: 'B', values: [2, 4, 3, 5] },
  ],
  axes: { x: { labels: ['Jan', 'Feb', 'Mar', 'Apr'] } },
};

const validChartPie = {
  type: 'pie' as const,
  series: [{ label: 'Distribution', values: [30, 20, 50] }],
  axes: { x: { labels: ['Chrome', 'Firefox', 'Safari'] } },
};

const validGraph = { format: 'mermaid' as const, source: 'graph TD\n  A-->B' };

const validCode = { language: 'typescript', content: 'const x = 1;', filename: 'app.ts' };

const validMarkdown = { content: '# Hello\n\nThis is **markdown**.' };

const validArtifact = {
  artifactRef: {
    id: 'art-1',
    name: 'screenshot.png',
    type: 'image',
    status: 'available' as const,
    path: '/tmp/screenshot.png',
    blobDigest: 'sha256:abc123def456',
    sourceRunId: 'run-1',
    lastVerifiedAt: '2026-07-20T10:00:00Z',
  },
  previewType: 'image',
};

async function run(): Promise<void> {
  setupDom();

  // ── Registration ─────────────────────────────────────────────────────────

  console.log('\n-- registration: all six kinds registered');
  for (const kind of ['table', 'chart', 'graph', 'code', 'markdown', 'artifact']) {
    ok(blockRegistry.has(kind, 1), `${kind} registered by production BlockHost`);
  }
  registerBuiltinBlocks();
  ok(blockRegistry.has('table', 1), 'repeated registration is idempotent');

  // ── Table: validation ────────────────────────────────────────────────────

  console.log('\n-- table validation');
  const tv = blockRegistry.validate('table', 1, validTable)!;
  ok(tv.valid, 'valid table data passes');
  ok(!blockRegistry.validate('table', 1, {})!.valid, 'empty object fails');
  ok(!blockRegistry.validate('table', 1, null as unknown as object)!.valid, 'null fails');
  ok(!blockRegistry.validate('table', 1, { columns: [], rows: [] })!.valid, 'empty columns fails');
  ok(!blockRegistry.validate('table', 1, { columns: [{ key: 'a', title: 'A' }], rows: 'bad' })!.valid, 'rows not array fails');
  ok(!blockRegistry.validate('table', 1, { columns: 'bad', rows: [] })!.valid, 'columns not array fails');
  ok(!blockRegistry.validate('table', 1, { columns: [{ key: '', title: '' }], rows: [{}] })!.valid, 'empty key fails');
  ok(!blockRegistry.validate('table', 1, { columns: [{ key: 'drop;table', title: 'X' }], rows: [{}] })!.valid, 'SQL injection key fails');
  ok(!blockRegistry.validate('table', 1, { columns: [{ key: 'a', title: 'A' }, { key: 'a', title: 'B' }], rows: [{}] })!.valid, 'duplicate key fails');
  ok(!blockRegistry.validate('table', 1, { columns: [{ key: 'a', title: 'A' }], rows: [[]] })!.valid, 'array row fails');
  ok(!blockRegistry.validate('table', 1, { columns: [{ key: 'a', title: 'A' }], rows: [{ a: { html: '<b>x</b>' } }] })!.valid, 'object cell fails');
  ok(!blockRegistry.validate('table', 1, { columns: [{ key: 'a', title: 'A' }], rows: [{ a: 1, hidden: 2 }] })!.valid, 'unknown row key fails');
  ok(!blockRegistry.validate('table', 1, { columns: [{ key: 'a', title: 'A', render: 'html' }], rows: [{ a: 1 }] })!.valid, 'unknown column config fails');
  ok(blockRegistry.validate('table', 2, validTable) === null, 'schema v2 returns null validator');

  // ── Table: render ────────────────────────────────────────────────────────

  console.log('\n-- table render');
  await render(React.createElement(BlockHost, { block: blk({ kind: 'table', data: validTable }), context }));
  await waitFor(() => (container().textContent ?? '').includes('Alice'), 'table shows Alice');
  contains(container().textContent ?? '', 'Bob', 'table shows Bob');
  contains(container().textContent ?? '', 'Name', 'table shows column header');
  const sortBtn = container().querySelector('.wg2-table-block__sort') as HTMLButtonElement;
  ok(sortBtn !== null, 'sort button exists');
  const ths = container().querySelectorAll('th');
  ok(ths.length === 3, 'three columns');
  ok(ths[0].getAttribute('aria-sort') === 'none', 'initial aria-sort is none');
  await render(React.createElement(BlockHost, {
    block: blk({
      kind: 'table',
      revision: 1_001,
      data: { columns: [{ key: 'value', title: 'Value' }], rows: [{ value: 10 }, { value: 2 }, { value: 1 }] },
    }),
    context,
  }));
  await waitFor(() => container().querySelector('button') !== null, 'numeric table renders');
  await click(container().querySelector('button')!);
  const sortedValues = Array.from(container().querySelectorAll('tbody td')).map((cell) => cell.textContent).join(',');
  ok(sortedValues === '1,2,10', 'numeric sort is numeric and ascending');
  ok(container().querySelector('th')?.getAttribute('aria-sort') === 'ascending', 'sorted header exposes aria-sort');
  await render(React.createElement(BlockHost, { block: blk({ kind: 'table', data: validTable, revision: 1_002 }), context, archived: true }));
  await waitFor(() => (container().textContent ?? '').includes('Alice'), 'archived table renders');
  ok(container().querySelector('.wg2-table-block__sort') === null, 'archived table is frozen without sort controls');
  // No HTML injection
  await render(React.createElement(BlockHost, {
    block: blk({ kind: 'table', data: { columns: [{ key: 'col', title: '<img src=x onerror=alert(1)>' }], rows: [{ col: '<script>alert(1)</script>' }] }, revision: 2 }),
    context,
  }));
  await waitFor(() => (container().textContent ?? '').includes('<img'), 'table renders raw text for injection');
  ok(container().querySelector('script') === null, 'no script element injected');
  ok(container().querySelector('img') === null, 'no img element injected');

  // ── Chart: validation ────────────────────────────────────────────────────

  console.log('\n-- chart validation');
  ok(blockRegistry.validate('chart', 1, validChartBar)!.valid, 'bar chart passes');
  ok(blockRegistry.validate('chart', 1, validChartLine)!.valid, 'line chart passes');
  ok(blockRegistry.validate('chart', 1, validChartPie)!.valid, 'pie chart passes');
  ok(!blockRegistry.validate('chart', 1, { type: 'scatter', series: validChartBar.series })!.valid, 'unknown chart type fails');
  ok(!blockRegistry.validate('chart', 1, { type: 'bar', series: [] })!.valid, 'empty series fails');
  ok(!blockRegistry.validate('chart', 1, { type: 'bar', series: [{ label: 'X', values: [NaN] }] })!.valid, 'NaN value fails');
  ok(!blockRegistry.validate('chart', 1, { type: 'bar', series: [{ label: 'X', values: [Infinity] }] })!.valid, 'Infinity fails');
  ok(blockRegistry.validate('chart', 1, { type: 'bar', series: [{ label: 'C', values: [0, 0, 0] }] })!.valid, 'all zeros ok');
  ok(blockRegistry.validate('chart', 1, { type: 'line', series: [{ label: 'S', values: [5] }] })!.valid, 'single point ok');
  ok(blockRegistry.validate('chart', 1, { type: 'bar', series: [{ label: 'N', values: [-5, -3, -7] }] })!.valid, 'negative values ok');
  ok(!blockRegistry.validate('chart', 1, { type: 'bar', series: [{ label: 'B', values: Array.from({ length: 501 }, (_, i) => i) }] })!.valid, '>500 points fails');
  ok(!blockRegistry.validate('chart', 1, { ...validChartBar, renderer: 'echarts' })!.valid, 'unknown chart library config fails');
  ok(!blockRegistry.validate('chart', 1, { type: 'line', series: [{ label: 'A', values: [1, 2] }, { label: 'B', values: [1] }] })!.valid, 'mismatched series lengths fail');
  ok(!blockRegistry.validate('chart', 1, { ...validChartBar, axes: { x: { labels: ['only-one'] } } })!.valid, 'mismatched axis labels fail');
  ok(!blockRegistry.validate('chart', 1, { type: 'pie', series: [{ label: 'P', values: [-1, 2] }] })!.valid, 'negative pie slice fails');
  ok(!blockRegistry.validate('chart', 1, { type: 'pie', series: validChartLine.series })!.valid, 'multi-series pie fails');
  ok(!blockRegistry.validate('chart', 1, { type: 'pie', series: [{ label: 'P', values: Array(25).fill(1) }] })!.valid, 'oversized pie legend fails');

  // ── Chart: render SVG ────────────────────────────────────────────────────

  console.log('\n-- chart render');
  // bar
  await render(React.createElement(BlockHost, { block: blk({ kind: 'chart', data: validChartBar, revision: 10 }), context }));
  await waitFor(() => container().querySelector('svg') !== null, 'bar chart svg renders');
  ok(container().querySelector('svg')!.querySelector('title') !== null, 'svg has title');
  ok(container().querySelector('svg')!.querySelector('desc') !== null, 'svg has desc');
  ok(container().querySelector('.wg2-chart-block__data') !== null, 'chart has collapsible data table');
  const barMarks = Array.from(container().querySelectorAll<SVGRectElement>('[data-chart-mark="bar"]'));
  const barHeights = barMarks.map((mark) => Number(mark.getAttribute('height')));
  ok(barHeights.every(Number.isFinite), 'bar geometry is finite');
  ok(barHeights[1] > barHeights[0], 'larger positive value produces taller bar');
  const mixedChart = {
    type: 'bar' as const,
    series: [{ label: 'A', values: [-5, 10] }, { label: 'B', values: [3, -2] }],
    axes: { x: { labels: ['Left', 'Right'] } },
  };
  await render(React.createElement(BlockHost, { block: blk({ kind: 'chart', data: mixedChart, revision: 10_001 }), context }));
  await waitFor(() => container().querySelectorAll('[data-chart-mark="bar"]').length === 4, 'mixed chart renders all bars');
  ok(Array.from(container().querySelectorAll<SVGRectElement>('[data-chart-mark="bar"]')).every((mark) => {
    const values = ['x', 'y', 'width', 'height'].map((name) => Number(mark.getAttribute(name)));
    return values.every(Number.isFinite) && values[0] >= 0 && values[1] >= 0 && values[2] >= 0 && values[3] >= 0 &&
      values[0] + values[2] <= 600 && values[1] + values[3] <= 300;
  }), 'mixed-sign bar geometry remains inside SVG bounds');
  await render(React.createElement(BlockHost, {
    block: blk({ kind: 'chart', data: { type: 'bar', series: [{ label: 'Debt', values: [-5] }] }, revision: 10_002 }),
    context,
  }));
  await waitFor(() => container().querySelector('[data-chart-mark="bar"]') !== null, 'constant negative bar renders');
  const negativeBar = container().querySelector<SVGRectElement>('[data-chart-mark="bar"]')!;
  ok(['x', 'y', 'width', 'height'].map((name) => Number(negativeBar.getAttribute(name))).every(Number.isFinite), 'constant negative bar geometry is finite');
  ok(Number(negativeBar.getAttribute('y')) + Number(negativeBar.getAttribute('height')) <= 300, 'constant negative bar remains inside SVG bounds');
  // line
  await render(React.createElement(BlockHost, { block: blk({ kind: 'chart', data: validChartLine, revision: 11 }), context }));
  await waitFor(() => container().querySelector('polyline') !== null, 'line chart renders polyline');
  // pie
  await render(React.createElement(BlockHost, { block: blk({ kind: 'chart', data: validChartPie, revision: 12 }), context }));
  await waitFor(() => container().querySelector('path') !== null, 'pie chart renders path');
  await render(React.createElement(BlockHost, {
    block: blk({ kind: 'chart', data: { type: 'pie', series: [{ label: 'Empty', values: [0, 0] }] }, revision: 12_001 }),
    context,
  }));
  await waitFor(() => (container().textContent ?? '').includes('No positive values'), 'zero pie renders explicit empty state');
  // SVG safety
  const svgHtml = container().querySelector('.wg2-chart-block__svg')?.outerHTML ?? '';
  notContains(svgHtml.toLowerCase(), '<script', 'svg has no script element');
  notContains(svgHtml.toLowerCase(), 'onerror', 'svg has no event handler');
  notContains(svgHtml.toLowerCase(), 'javascript:', 'svg has no javascript URI');
  // archived
  await render(React.createElement(BlockHost, { block: blk({ kind: 'chart', data: validChartBar, revision: 13 }), context, archived: true }));
  await waitFor(() => container().querySelector('svg') !== null, 'archived chart renders');
  ok(container().querySelector('.wg2-block-host')!.hasAttribute('inert'), 'archived host is inert');

  // ── Graph: validation ────────────────────────────────────────────────────

  console.log('\n-- graph validation');
  ok(blockRegistry.validate('graph', 1, validGraph)!.valid, 'valid mermaid graph passes');
  ok(!blockRegistry.validate('graph', 1, { format: 'dot', source: 'A->B' })!.valid, 'non-mermaid format fails');
  ok(!blockRegistry.validate('graph', 1, { format: 'mermaid', source: '' })!.valid, 'empty source fails');
  ok(!blockRegistry.validate('graph', 1, {})!.valid, 'missing fields fails');
  ok(!blockRegistry.validate('graph', 1, { ...validGraph, html: true })!.valid, 'graph rejects unknown renderer config');
  ok(blockRegistry.validate('graph', 1, { format: 'mermaid', source: '<script>alert(1)</script>' }).valid, 'mermaid source passes validation (security by MermaidDiagram antiscript)');
  // Oversized source
  ok(!blockRegistry.validate('graph', 1, { format: 'mermaid', source: 'x'.repeat(100_001) })!.valid, 'oversized source fails');

  // ── Graph: render (MermaidDiagram lazy-imports mermaid/svg-pan-zoom —
  //   may not fully render in jsdom; we verify the block host correctly
  //   resolves and the module loads without crash) ──────────────────────────

  console.log('\n-- graph render');
  await render(React.createElement(BlockHost, { block: blk({ kind: 'graph', data: validGraph, revision: 20 }), context }));
  // Either the graph block renders or MermaidDiagram fails safely in jsdom —
  // either way, the host must not crash and must produce some output.
  await flush();
  await flush();
  const gtext = container().textContent ?? '';
  const graphOk = gtext.includes('graph') || gtext.includes('Rendering') || gtext.includes('failed');
  ok(graphOk, 'graph block produces output without host crash');

  // ── Code: validation ─────────────────────────────────────────────────────

  console.log('\n-- code validation');
  ok(blockRegistry.validate('code', 1, validCode)!.valid, 'valid code passes');
  ok(blockRegistry.validate('code', 1, { language: 'go', content: 'package main' })!.valid, 'code without filename ok');
  ok(!blockRegistry.validate('code', 1, { language: '', content: 'x' })!.valid, 'empty language fails');
  ok(!blockRegistry.validate('code', 1, { language: 'ts', content: 'x', filename: 'a\nb' })!.valid, 'newline in filename fails');
  ok(!blockRegistry.validate('code', 1, { language: 'ts', content: 'x', filename: 'a\0b' })!.valid, 'null byte in filename fails');
  ok(!blockRegistry.validate('code', 1, { language: 'ts', content: 'x'.repeat(200_001) })!.valid, 'oversized content fails');
  ok(!blockRegistry.validate('code', 1, { ...validCode, html: true })!.valid, 'code rejects unknown HTML config');

  // ── Code: render (CodeViewer lazy-imports highlight.js; may not fully
  //   render in jsdom but must not crash the host) ──────────────────────────

  console.log('\n-- code render');
  await render(React.createElement(BlockHost, { block: blk({ kind: 'code', data: validCode, revision: 30 }), context }));
  await flush();
  await flush();
  const ctext = container().textContent ?? '';
  ok(ctext.length > 0, 'code block produces output without host crash');

  // Code with injected filename — verify no HTML from filename
  await render(React.createElement(BlockHost, {
    block: blk({ kind: 'code', data: { language: 'html', content: '<b>bold</b>', filename: '<img src=x onerror=alert(1)>' }, revision: 31 }),
    context,
  }));
  await flush();
  await flush();
  ok(container().querySelector('img') === null, 'filename does not inject HTML element');

  // ── Markdown: validation ─────────────────────────────────────────────────

  console.log('\n-- markdown validation');
  ok(blockRegistry.validate('markdown', 1, validMarkdown)!.valid, 'valid markdown passes');
  ok(blockRegistry.validate('markdown', 1, { content: '' })!.valid, 'empty content ok');
  ok(!blockRegistry.validate('markdown', 1, {})!.valid, 'missing content fails');
  ok(!blockRegistry.validate('markdown', 1, { content: 'x'.repeat(200_001) }).valid, 'oversized content fails');
  ok(!blockRegistry.validate('markdown', 1, { ...validMarkdown, allowDangerousHtml: true }).valid, 'markdown rejects raw HTML config');

  // ── Markdown: render (MarkdownRenderer lazy-imports react-markdown/katex;
  //   may not fully render in jsdom but must not crash) + security ─────────

  console.log('\n-- markdown render');
  await render(React.createElement(BlockHost, { block: blk({ kind: 'markdown', data: validMarkdown, revision: 40 }), context }));
  await flush();
  await flush();
  const mtext = container().textContent ?? '';
  ok(mtext.length > 0, 'markdown block produces output without host crash');

  // Unsafe markdown — verify no script/style/event in DOM even after render attempt
  const unsafeMd = { content: '<script>alert("xss")</script><img src=x onerror=alert(1)><style>body{color:red}</style>' };
  await render(React.createElement(BlockHost, { block: blk({ kind: 'markdown', data: unsafeMd, revision: 41 }), context }));
  await flush();
  await flush();
  ok(container().querySelector('script') === null, 'script element not in DOM');
  ok(container().querySelector('style') === null, 'style element not in DOM');
  ok(container().querySelector('img[onerror]') === null, 'event handler img not in DOM');

  // ── Artifact: validation ─────────────────────────────────────────────────

  console.log('\n-- artifact validation');
  ok(blockRegistry.validate('artifact', 1, validArtifact)!.valid, 'valid artifact passes');
  ok(!blockRegistry.validate('artifact', 1, {})!.valid, 'missing artifactRef fails');
  ok(!blockRegistry.validate('artifact', 1, { artifactRef: {} })!.valid, 'empty artifactRef fails');
  ok(!blockRegistry.validate('artifact', 1, { artifactRef: { id: '', name: '', type: '', status: 'available' } }).valid, 'empty id fails');
  ok(!blockRegistry.validate('artifact', 1, { artifactRef: { id: 'x', name: 'y', type: 'z', status: 'unknown_status' } }).valid, 'invalid status fails');
  ok(!blockRegistry.validate('artifact', 1, { artifactRef: { id: 'x', name: 'y', type: 'z', status: 'available', url: 'javascript:alert(1)' } }).valid, 'unknown artifact URL fails host validation');
  ok(!blockRegistry.validate('artifact', 1, { artifactRef: { id: 'x', name: 'y', type: 'z', status: 'available' }, previewType: 'html' }).valid, 'unknown artifact preview fails');
  ok(!blockRegistry.validate('artifact', 1, { artifactRef: { id: 'x', name: 'y', type: 'z', status: 'failed', error: 'x'.repeat(4097) } }).valid, 'oversized artifact error fails');
  ok(blockRegistry.validate('artifact', 1, {
    artifactRef: { id: 'x', name: '<img src=x onerror=alert(1)>', type: '<script>', status: 'failed', error: '<iframe src=evil>' },
  }).valid, 'malicious strings pass validation but must render as text');

  // ── Artifact: render — four states ───────────────────────────────────────

  console.log('\n-- artifact render: four states');
  for (const [status, expectedLabel] of [
    ['available', 'Available'],
    ['stale', 'Stale'],
    ['missing', 'Missing'],
    ['failed', 'Failed'],
  ] as const) {
    await render(React.createElement(BlockHost, {
      block: blk({ kind: 'artifact', data: { artifactRef: { id: 'a', name: `test-${status}`, type: 'file', status } }, revision: 50 }),
      context,
    }));
    await waitFor(() => (container().textContent ?? '').includes(expectedLabel), `artifact ${status} shows ${expectedLabel}`);
    ok(container().querySelector(`.wg2-artifact-block--${status}`) !== null, `artifact has ${status} class`);
  }

  // Error message
  await render(React.createElement(BlockHost, {
    block: blk({
      kind: 'artifact',
      data: { artifactRef: { id: 'a', name: 'broken.svg', type: 'image', status: 'failed', error: 'Generation timed out' } },
      revision: 60,
    }),
    context,
  }));
  await waitFor(() => (container().textContent ?? '').includes('Generation timed out'), 'error message visible');
  ok(container().querySelector('.wg2-artifact-block__error') !== null, 'error section rendered');

  // Malicious error as text only
  await render(React.createElement(BlockHost, {
    block: blk({
      kind: 'artifact',
      data: {
        artifactRef: { id: 'x', name: '<b>bold</b>', type: '<script>alert(1)</script>', status: 'failed', error: '<img src=x onerror=alert(1)>' },
      },
      revision: 61,
    }),
    context,
  }));
  await waitFor(() => (container().textContent ?? '').includes('<img'), 'malicious error rendered as text');
  ok(container().querySelector('img') === null, 'no img element injected from error');
  ok(container().querySelector('script') === null, 'no script element from artifact');

  // ── Invalid data → fallback for all six kinds ────────────────────────────

  console.log('\n-- invalid data → fallback');
  for (const kind of ['table', 'chart', 'graph', 'code', 'markdown', 'artifact']) {
    await render(React.createElement(BlockHost, {
      block: blk({ kind, data: { __bad: true }, revision: 100 }),
      context,
    }));
    await waitFor(
      () => (container().textContent ?? '').includes('Invalid data'),
      `${kind} invalid data enters fallback`,
    );
  }

  // ── Future schema → fallback ─────────────────────────────────────────────

  console.log('\n-- future/unsupported schema → fallback');
  await render(React.createElement(BlockHost, {
    block: blk({ kind: 'table', schemaVersion: 99, data: validTable, revision: 200 }),
    context,
  }));
  await waitFor(() => (container().textContent ?? '').includes('Future schema'), 'future schema shows fallback');

  await render(React.createElement(BlockHost, {
    block: blk({ kind: 'unknown_kind', schemaVersion: 1, data: {}, revision: 201 }),
    context,
  }));
  await waitFor(() => (container().textContent ?? '').includes('Unknown renderer kind'), 'unknown kind shows fallback');

  process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed) process.exit(1);
}

run().catch((error) => {
  console.error = reportError;
  reportError('block renderers test runner failed', error);
  process.exit(1);
});
