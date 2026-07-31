import { readFileSync } from 'node:fs';

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import { WorkSessionTransition } from '../components/work/WorkSessionTransition';
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
  SVGElement: dom.window.SVGElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
});
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });

let retries = 0;
const host = document.getElementById('root')!;
const root = createRoot(host);

function render(phase: 'initializing' | 'planning' | 'revealing' | 'error', error?: string): void {
  root.render(
    <LocaleProvider>
      <WorkSessionTransition
        prompt="生成一份发布计划"
        phase={phase}
        error={error}
        onRetry={() => { retries++; }}
      />
    </LocaleProvider>,
  );
}

async function main(): Promise<void> {
  process.stdout.write('\nSession to Work transition\n');
  await act(async () => { render('planning'); });
  const surface = host.querySelector<HTMLElement>('[data-testid="work-session-transition"]');
  ok(surface?.dataset.phase === 'planning', 'planning is represented inside the Session transition surface');
  ok(host.textContent?.includes('生成一份发布计划') ?? false, 'the first message remains visible while structure planning runs');
  ok(Boolean(host.querySelector('.work-session-transition__progress strong')?.textContent?.trim()), 'planning progress is explicit');
  ok(!host.querySelector('button'), 'planning does not expose a duplicate submit action');

  await act(async () => { render('error', '模型暂时不可用'); });
  ok(host.textContent?.includes('模型暂时不可用') ?? false, 'initialization failure stays visible');
  await act(async () => { host.querySelector<HTMLButtonElement>('button')?.click(); });
  ok(retries === 1, 'failure exposes one retry intent');

  await act(async () => { render('revealing'); });
  ok(surface?.getAttribute('aria-hidden') === 'true', 'transition copy leaves the accessibility tree while Work is revealed');

  const styles = readFileSync(new URL('../styles.css', import.meta.url), 'utf8');
  ok(styles.includes('@keyframes work-session-panel-reveal'), 'Work uses a dedicated Session-to-panel reveal animation');
  ok(styles.includes('@keyframes work-session-fold-away'), 'the old Session has a paired exit animation');

  await act(async () => { root.unmount(); });
  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

void main().catch((error) => {
  console.error(error);
  process.exit(1);
});
