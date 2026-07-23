import { JSDOM } from 'jsdom';
import React, { act, type ReactElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { WorkPage } from '../components/work/WorkPage';
import { LocaleProvider } from '../lib/i18n';

let passed = 0;
let failed = 0;

function check(condition: boolean, label: string): void {
  if (!condition) throw new Error(label);
}

function equal<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) {
    throw new Error(`${label}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
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
    HTMLInputElement: dom.window.HTMLInputElement,
    HTMLTextAreaElement: dom.window.HTMLTextAreaElement,
    Event: dom.window.Event,
    InputEvent: dom.window.InputEvent,
    MouseEvent: dom.window.MouseEvent,
    KeyboardEvent: dom.window.KeyboardEvent,
    MutationObserver: dom.window.MutationObserver,
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
  });
  Object.defineProperty(dom.window.HTMLElement.prototype, 'attachEvent', { configurable: true, value: () => {} });
  Object.defineProperty(dom.window.HTMLElement.prototype, 'detachEvent', { configurable: true, value: () => {} });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
}

setupDOM();

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: unknown) => void;
}

function deferred<T>(): Deferred<T> {
  let resolvePromise!: (value: T) => void;
  let rejectPromise!: (error: unknown) => void;
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve;
    rejectPromise = reject;
  });
  return { promise, resolve: resolvePromise, reject: rejectPromise };
}

const listDeferreds: Array<Deferred<any>> = [];
const createDeferreds: Array<Deferred<any>> = [];
const listTabs: string[] = [];
const createInputs: Array<{ prompt: string; requestId: string }> = [];

function resetBridge(): void {
  listDeferreds.length = 0;
  createDeferreds.length = 0;
  listTabs.length = 0;
  createInputs.length = 0;
  window.go = { main: { App: {
    ListWorks: (tabID: string) => {
      listTabs.push(tabID);
      const next = deferred<any>();
      listDeferreds.push(next);
      return next.promise;
    },
    CreateWork: (_tabID: string, input: { prompt: string; requestId: string }) => {
      createInputs.push({ prompt: input.prompt, requestId: input.requestId });
      const next = deferred<any>();
      createDeferreds.push(next);
      return next.promise;
    },
  } as any } };
}

interface Mounted {
  host: HTMLDivElement;
  root: Root;
  rerender: (next: ReactElement) => Promise<void>;
}

const mounts: Mounted[] = [];

async function mount(element: ReactElement): Promise<Mounted> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host, { onCaughtError: (error) => { throw error; } });
  await act(async () => { root.render(<LocaleProvider>{element}</LocaleProvider>); });
  const mounted: Mounted = {
    host,
    root,
    rerender: async (next) => {
      await act(async () => { root.render(<LocaleProvider>{next}</LocaleProvider>); });
    },
  };
  mounts.push(mounted);
  return mounted;
}

async function cleanup(): Promise<void> {
  while (mounts.length > 0) {
    const mounted = mounts.pop()!;
    await act(async () => { mounted.root.unmount(); });
    mounted.host.remove();
  }
}

async function settle(): Promise<void> {
  await act(async () => { await Promise.resolve(); });
}

function page(items: unknown[] = []) {
  return { items, total: items.length };
}

function summary(id: string, name: string) {
  return {
    id,
    name,
    state: 'draft',
    archiveState: 'active',
    blueprintRef: { id: 'blueprint:blank', schemaVersion: 1, version: 1 },
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
  };
}

function click(host: HTMLElement, testID: string): void {
  act(() => {
    const button = host.querySelector(`[data-testid="${testID}"]`) as HTMLButtonElement | null;
    if (!button) throw new Error(`missing button ${testID}`);
    button.click();
  });
}

async function setPrompt(host: HTMLElement, value: string): Promise<void> {
  const input = host.querySelector('[data-testid="work-create-prompt"]') as HTMLTextAreaElement | null;
  if (!input) throw new Error('missing Work prompt input');
  await act(async () => {
    const previous = input.value;
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!;
    setter.call(input, value);
    (input as HTMLTextAreaElement & { _valueTracker?: { setValue: (next: string) => void } })._valueTracker?.setValue(previous);
    const propsKey = Object.keys(input).find((key) => key.startsWith('__reactProps$'));
    const props = propsKey
      ? (input as unknown as Record<string, { onChange?: (event: { target: HTMLTextAreaElement }) => void }>)[propsKey]
      : undefined;
    if (props?.onChange) props.onChange({ target: input });
    else {
      input.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: value }));
      input.dispatchEvent(new Event('change', { bubbles: true }));
    }
    await Promise.resolve();
  });
}

async function test(name: string, body: () => Promise<void> | void): Promise<void> {
  resetBridge();
  try {
    await body();
    process.stdout.write(`  PASS  ${name}\n`);
    passed++;
  } catch (error) {
    process.stdout.write(`  FAIL  ${name}: ${error instanceof Error ? error.message : String(error)}\n`);
    failed++;
  } finally {
    await cleanup();
  }
}

await test('empty state exposes a focusable create CTA that opens the real form', async () => {
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  check(host.querySelector('[data-testid="work-loading"]') !== null, 'loading state missing');
  listDeferreds[0].resolve(page());
  await settle();
  check(host.querySelector('[data-testid="work-empty"]') !== null, 'empty state missing');
  const emptyCTA = host.querySelector('[data-testid="work-empty-new-btn"]') as HTMLButtonElement | null;
  check(emptyCTA !== null, 'empty create CTA missing');
  equal(emptyCTA?.textContent?.trim(), 'New Work', 'empty create CTA label');
  equal(emptyCTA?.disabled, false, 'empty create CTA disabled');
  emptyCTA?.focus();
  equal(document.activeElement, emptyCTA, 'empty create CTA focus');
  click(host, 'work-empty-new-btn');
  check(host.querySelector('[data-testid="work-create-form"]') !== null, 'empty CTA did not open create form');
  check(host.querySelector('[data-testid="work-create-prompt"]') !== null, 'prompt input missing');
  check(host.querySelector('[data-testid="work-create-name"]') === null, 'name input leaked into simple create form');
  check(host.querySelector('[data-testid="work-create-inputs"]') === null, 'JSON inputs leaked into simple create form');
  check(host.querySelector('[data-testid="work-create-blueprint"]') === null, 'Blueprint selector leaked into simple create form');
});

await test('load failure is explicit and retryable', async () => {
  let opened = '';
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={(id) => { opened = id; }} />);
  listDeferreds[0].reject(new Error('offline'));
  await settle();
  check(host.querySelector('[data-testid="work-error"]')?.textContent?.includes('offline') === true, 'load error missing');
  click(host, 'work-retry-btn');
  listDeferreds[1].resolve(page([summary('work-1', 'First')]));
  await settle();
  act(() => {
    (host.querySelector('[data-testid="work-item-work-1"] button') as HTMLButtonElement).click();
  });
  equal(opened, 'work-1', 'active Work did not open');
});

await test('flag-off/empty tab performs zero ListWorks calls', async () => {
  await mount(<WorkPage tabID="" onBack={() => undefined} onOpenWork={() => undefined} />);
  equal(listTabs.length, 0, 'ListWorks call count');
});

await test('unchanged failed create intent reuses requestID', async () => {
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  await setPrompt(host, 'My Work');
  click(host, 'work-create-submit');
  createDeferreds[0].reject(new Error('network'));
  await settle();
  const firstID = createInputs[0].requestId;
  click(host, 'work-create-submit');
  equal(createInputs.length, 2, 'CreateWork call count');
  equal(createInputs[1].requestId, firstID, 'retry requestID');
});

await test('A to B to A edit creates a new requestID', async () => {
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  await setPrompt(host, 'A');
  click(host, 'work-create-submit');
  createDeferreds[0].reject(new Error('network'));
  await settle();
  const firstID = createInputs[0].requestId;
  await setPrompt(host, 'B');
  await setPrompt(host, 'A');
  click(host, 'work-create-submit');
  check(createInputs[1].requestId !== firstID, 'edited intent reused its old requestID');
});

await test('cancel creates a new intent and pending submit cannot duplicate', async () => {
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  await setPrompt(host, 'Same');
  click(host, 'work-create-submit');
  const firstID = createInputs[0].requestId;
  click(host, 'work-create-submit');
  equal(createInputs.length, 1, 'duplicate pending CreateWork calls');
  createDeferreds[0].reject(new Error('network'));
  await settle();
  click(host, 'work-create-cancel');
  click(host, 'work-new-btn');
  await setPrompt(host, 'Same');
  click(host, 'work-create-submit');
  check(createInputs[1].requestId !== firstID, 'cancelled intent reused requestID');
});

await test('same-instance tab switch ignores the old ListWorks result', async () => {
  const { host, rerender } = await mount(<WorkPage tabID="tab-a" onBack={() => undefined} onOpenWork={() => undefined} />);
  await rerender(<WorkPage tabID="tab-b" onBack={() => undefined} onOpenWork={() => undefined} />);
  equal(listDeferreds.length, 2, 'per-tab ListWorks call count');
  listDeferreds[0].resolve(page([summary('old', 'Old')]));
  await settle();
  check(host.querySelector('[data-testid="work-loading"]') !== null, 'old ListWorks replaced new loading state');
  check(host.querySelector('[data-testid="work-item-old"]') === null, 'old Work leaked into new tab');
  listDeferreds[1].resolve(page([summary('new', 'New')]));
  await settle();
  check(host.querySelector('[data-testid="work-item-new"]') !== null, 'new tab result missing');
});

await test('same-instance tab switch ignores a late CreateWork result', async () => {
  const opened: string[] = [];
  const { host, rerender } = await mount(<WorkPage tabID="tab-a" onBack={() => undefined} onOpenWork={(id) => { opened.push(`a:${id}`); }} />);
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  await setPrompt(host, 'Late');
  click(host, 'work-create-submit');
  await rerender(<WorkPage tabID="tab-b" onBack={() => undefined} onOpenWork={(id) => { opened.push(`b:${id}`); }} />);
  createDeferreds[0].resolve({ id: 'old-work', name: 'Late' });
  await settle();
  equal(opened.length, 0, 'late CreateWork opened a Work');
  equal(listDeferreds.length, 2, 'late CreateWork started an old-tab refresh');
});

await test('same-instance tab switch ignores a late post-create refresh', async () => {
  const opened: string[] = [];
  const { host, rerender } = await mount(<WorkPage tabID="tab-a" onBack={() => undefined} onOpenWork={(id) => { opened.push(`a:${id}`); }} />);
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  await setPrompt(host, 'Refresh');
  click(host, 'work-create-submit');
  createDeferreds[0].resolve({ id: 'old-work', name: 'Refresh' });
  await settle();
  equal(listDeferreds.length, 2, 'post-create refresh was not started');
  await rerender(<WorkPage tabID="tab-b" onBack={() => undefined} onOpenWork={(id) => { opened.push(`b:${id}`); }} />);
  equal(listDeferreds.length, 3, 'new-tab ListWorks was not started');
  listDeferreds[1].resolve(page([summary('old-work', 'Refresh')]));
  await settle();
  equal(opened.length, 0, 'late refresh opened a Work');
  check(host.querySelector('[data-testid="work-item-old-work"]') === null, 'late refresh polluted the new tab');
  listDeferreds[2].resolve(page());
  await settle();
  check(host.querySelector('[data-testid="work-empty"]') !== null, 'new tab did not settle');
});

await test('successful create refreshes the list and opens exactly once', async () => {
  const opened: string[] = [];
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={(id) => { opened.push(id); }} />);
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  await setPrompt(host, 'Created');
  click(host, 'work-create-submit');
  createDeferreds[0].resolve({ id: 'work-created', name: 'Created' });
  await settle();
  equal(listDeferreds.length, 2, 'post-create ListWorks call count');
  listDeferreds[1].resolve(page([summary('work-created', 'Created')]));
  await settle();
  equal(opened.length, 1, 'successful create open count');
  equal(opened[0], 'work-created', 'successful create target');
  check(host.querySelector('[data-testid="work-create-dialog"]') === null, 'successful create kept the dialog open');
});

process.stdout.write(`\nWorkPage: ${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
