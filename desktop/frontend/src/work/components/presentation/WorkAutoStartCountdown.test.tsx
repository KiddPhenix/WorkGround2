import { useState } from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { JSDOM } from 'jsdom';

import { WorkAutoStartCountdown } from './WorkAutoStartCountdown';

let passed = 0;
let failed = 0;
function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  condition ? passed++ : failed++;
}

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
});

const wait = (milliseconds: number) => new Promise((resolve) => setTimeout(resolve, milliseconds));

function Harness({ scope, onStart }: { scope: string; onStart: () => Promise<void> }) {
  const [paused, setPaused] = useState(false);
  return (
    <WorkAutoStartCountdown
      scope={scope}
      paused={paused}
      onPausedChange={setPaused}
      onStart={onStart}
      durationSeconds={1}
    />
  );
}

async function main(): Promise<void> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  let starts = 0;
  const start = async () => { starts++; };

  await act(async () => root.render(<Harness scope="auto" onStart={start} />));
  ok(host.textContent?.includes('信息已齐全') === true, 'shows the approved countdown copy');
  await act(async () => { await wait(1150); });
  ok(starts === 1, 'starts automatically when the countdown reaches zero');

  await act(async () => root.render(<Harness scope="paused" onStart={start} />));
  const pause = [...host.querySelectorAll<HTMLButtonElement>('button')]
    .find((button) => button.textContent?.includes('暂停并修改'));
  await act(async () => pause?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  await act(async () => { await wait(1150); });
  ok(starts === 1, 'pause prevents automatic start');
  ok(host.textContent?.includes('倒计时已暂停') === true, 'pause state remains visible and reversible');

  const startNow = [...host.querySelectorAll<HTMLButtonElement>('button')]
    .find((button) => button.textContent?.includes('立即开始'));
  await act(async () => startNow?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(starts === 2, 'immediate start bypasses the paused countdown');

  await act(async () => root.unmount());
  host.remove();
  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed) process.exit(1);
}

void main();
