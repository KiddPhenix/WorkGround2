import { readFileSync } from 'node:fs';

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import { WorkStartSurface } from '../components/work/WorkStartSurface';
import { LocaleProvider } from '../lib/i18n';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else failed++;
}

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
  HTMLTextAreaElement: dom.window.HTMLTextAreaElement,
  SVGElement: dom.window.SVGElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
  requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
  cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
});
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
Object.assign(dom.window.HTMLElement.prototype, {
  attachEvent: () => undefined,
  detachEvent: () => undefined,
});

let prompt = '';
let starts = 0;
const host = document.getElementById('root')!;
const root = createRoot(host);

function render(phase: 'initializing' | 'ready' | 'starting' | 'error', error?: string): void {
  root.render(
    <LocaleProvider>
      <WorkStartSurface
        prompt={prompt}
        phase={phase}
        error={error}
        onPromptChange={(value) => {
          prompt = value;
          render(phase, error);
        }}
        onStart={() => { starts++; }}
      />
    </LocaleProvider>,
  );
}

async function main(): Promise<void> {
  process.stdout.write('\nWork start surface\n');
  await act(async () => { render('initializing'); });
  const editor = host.querySelector<HTMLTextAreaElement>('[data-testid="work-start-prompt"]')!;
  const button = host.querySelector<HTMLButtonElement>('[data-testid="work-start-submit"]')!;
  const dragRegion = host.querySelector<HTMLElement>('[data-testid="work-start-drag-region"]');
  const styles = readFileSync(new URL('../styles.css', import.meta.url), 'utf8');
  ok(Boolean(dragRegion) && dragRegion?.getAttribute('aria-hidden') === 'true', 'creation surface exposes a non-interactive titlebar drag region');
  ok(
    /\.work-start-surface__drag-region\s*\{[^}]*--wails-draggable:\s*drag;/s.test(styles)
      && /\.work-start-surface\s*>\s*\.wg2-work-draft-editor\s*\{[^}]*--wails-draggable:\s*no-drag;/s.test(styles),
    'drag rail and interactive editor keep separate Wails drag behavior',
  );
  ok(Boolean(editor) && !editor.disabled, 'background initialization does not block typing');
  ok(button.disabled, 'Start is disabled until the prompt is non-empty');

  prompt = '生成一份发布计划';
  await act(async () => { render('initializing'); });
  ok(host.querySelector<HTMLTextAreaElement>('[data-testid="work-start-prompt"]')!.value === prompt, 'typed prompt is kept while initialization runs');
  ok(!host.querySelector<HTMLButtonElement>('[data-testid="work-start-submit"]')!.disabled, 'Start is available before initialization completes');

  await act(async () => { host.querySelector<HTMLButtonElement>('[data-testid="work-start-submit"]')!.click(); });
  ok(starts === 1, 'one click emits one start intent');

  await act(async () => { render('starting'); });
  ok(host.querySelector<HTMLTextAreaElement>('[data-testid="work-start-prompt"]')!.disabled, 'starting freezes the submitted prompt');
  ok(host.querySelector<HTMLButtonElement>('[data-testid="work-start-submit"]')!.disabled, 'duplicate Start is blocked while joining the background task');

  await act(async () => { render('error', '初始化失败'); });
  ok(host.querySelector<HTMLTextAreaElement>('[data-testid="work-start-prompt"]')!.value === prompt, 'failure keeps the prompt for retry');
  ok(host.textContent?.includes('初始化失败') ?? false, 'background failure is explicit');

  await act(async () => { root.unmount(); });
  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

void main().catch((error) => {
  console.error(error);
  process.exit(1);
});
