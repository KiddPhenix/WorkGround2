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
const planningDeferreds: Array<Deferred<any>> = [];
const v2Deferreds: Array<Deferred<boolean>> = [];
const listTabs: string[] = [];
const createInputs: Array<{ prompt: string; requestId: string }> = [];
const planningInputs: Array<{ sessionId: string; requestId: string }> = [];
let collaborationV2Enabled = false;
let v2FlagPending = false;
let v2FlagRejects = false;
let v2BindingMissing = false;

function resetBridge(): void {
  window.localStorage.clear();
  listDeferreds.length = 0;
  createDeferreds.length = 0;
  planningDeferreds.length = 0;
  v2Deferreds.length = 0;
  listTabs.length = 0;
  createInputs.length = 0;
  planningInputs.length = 0;
  collaborationV2Enabled = false;
  v2FlagPending = false;
  v2FlagRejects = false;
  v2BindingMissing = false;
  const appMock: Record<string, unknown> = {
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
    WorkCollaborationV2Enabled: () => {
      if (v2FlagRejects) return Promise.reject(new Error('flag unavailable'));
      if (v2FlagPending) {
        const next = deferred<boolean>();
        v2Deferreds.push(next);
        return next.promise;
      }
      return Promise.resolve(collaborationV2Enabled);
    },
    BeginWorkPlanning: (_tabID: string, input: { sessionId: string; requestId: string }) => {
      planningInputs.push(input);
      const next = deferred<any>();
      planningDeferreds.push(next);
      return next.promise;
    },
  };
  if (!v2BindingMissing) {
    appMock.WorkCollaborationV2Enabled = () => {
      if (v2FlagRejects) return Promise.reject(new Error('flag unavailable'));
      if (v2FlagPending) {
        const next = deferred<boolean>();
        v2Deferreds.push(next);
        return next.promise;
      }
      return Promise.resolve(collaborationV2Enabled);
    };
  }
  window.go = { main: { App: appMock as any } };
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
  check(host.querySelector('[data-testid="work-empty-new-btn"]') === null, 'stale empty-state CTA leaked');
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  check(cta !== null, 'header create CTA missing');
  equal(cta?.textContent?.trim(), 'New Work', 'create CTA label');
  equal(cta?.disabled, false, 'create CTA disabled');
  cta?.focus();
  equal(document.activeElement, cta, 'create CTA focus');
  click(host, 'work-new-btn');
  check(host.querySelector('[data-testid="work-create-form"]') !== null, 'CTA did not open create form');
  check(host.querySelector('[data-testid="work-create-prompt"]') !== null, 'prompt input missing');
  check(host.querySelector('[data-testid="work-create-name"]') === null, 'name input leaked into simple create form');
  check(host.querySelector('[data-testid="work-create-inputs"]') === null, 'JSON inputs leaked into simple create form');
  check(host.querySelector('[data-testid="work-create-blueprint"]') === null, 'Blueprint selector leaked into simple create form');
});

await test('list page keeps the WorkPage style contract on every interactive region', async () => {
  const { host } = await mount(<WorkPage tabID="tab-style" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page([summary('work-style', 'Styled Work')]));
  await settle();

  check(host.querySelector('[data-testid="work-back-btn"]')?.classList.contains('work-page__back-btn') === true, 'back button style class missing');
  check(host.querySelector('[data-testid="work-new-btn"]')?.classList.contains('work-page__new-btn') === true, 'new button style class missing');
  equal(host.querySelectorAll('.work-page__filter-btn').length, 3, 'filter button style classes');
  check(host.querySelector('.work-page__filter-btn[aria-pressed="true"]') !== null, 'active filter style state missing');
  check(host.querySelector('[data-testid="work-item-work-style"]')?.classList.contains('work-page__item') === true, 'Work row style class missing');
  equal(host.querySelectorAll('[data-testid="work-item-work-style"] .work-page__action-btn').length, 3, 'action button style classes');
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

await test('V2 enabled: single CTA routes to BeginWorkPlanning, retry reuses requestID, zero V1 CreateWork', async () => {
  collaborationV2Enabled = true;
  const opened: string[] = [];
  const { host } = await mount(<WorkPage tabID="tab-v2" sessionID="session-v2" onBack={() => undefined} onOpenWork={(id) => { opened.push(id); }} />);
  listDeferreds[0].resolve(page());
  await settle();
  // Single CTA only; no separate work-begin-planning button.
  check(host.querySelector('[data-testid="work-begin-planning"]') === null, 'stale V2 planning button leaked');
  check(host.querySelector('[data-testid="work-new-btn"]') !== null, 'unified CTA missing');
  click(host, 'work-new-btn');
  equal(planningInputs.length, 1, 'BeginWorkPlanning first call');
  equal(createInputs.length, 0, 'V1 CreateWork must not be called when V2 enabled');
  equal(planningInputs[0].sessionId, 'session-v2', 'BeginWorkPlanning persists real Session identity');
  planningDeferreds[0].reject(new Error('offline'));
  await settle();
  check(host.querySelector('[data-testid="work-planning-error"]')?.textContent?.includes('offline') === true, 'planning failure not explicit');
  click(host, 'work-planning-retry');
  equal(planningInputs[1].requestId, planningInputs[0].requestId, 'planning retry requestID');
  // Retry must not call CreateWork.
  equal(createInputs.length, 0, 'V1 CreateWork must not be called on V2 retry');
  planningDeferreds[1].resolve({
    result: {
      schemaVersion: 2,
      revision: 1,
      work: { id: 'work-v2-created' },
    },
    revision: 1,
    duplicate: false,
    committed: true,
    recoverable: false,
  });
  await settle();
  equal(listDeferreds.length, 2, 'planning success refresh count');
  listDeferreds[1].resolve(page());
  await settle();
  equal(opened[0], 'work-v2-created', 'planning success target');
  equal(
    window.localStorage.getItem('work-card-ui:tab-v2:work-v2-created'),
    JSON.stringify({ activeFace: 'back' }),
    'planning face is persisted before opening the Work',
  );
  equal(createInputs.length, 0, 'V2 planning does not route through V1 CreateWork');
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

await test('v2Flag pending: CTA disabled, zero creation side effects', async () => {
  v2FlagPending = true;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  // Flag is deferred — CTA must remain disabled.
  listDeferreds[0].resolve(page());
  await settle();
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  check(cta !== null, 'unified CTA missing');
  equal(cta!.disabled, true, 'pending flag CTA must be disabled');
  equal(cta!.textContent!.trim(), '…', 'pending flag CTA label');
  // Click must not trigger any creation.
  click(host, 'work-new-btn');
  equal(createInputs.length, 0, 'CreateWork called while flag pending');
  equal(planningInputs.length, 0, 'BeginWorkPlanning called while flag pending');
});

await test('v2Flag rejected: explicit error, retryable, zero creation', async () => {
  v2FlagRejects = true;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  check(host.querySelector('[data-testid="work-v2-flag-error"]') !== null, 'flag error missing');
  check(host.querySelector('[data-testid="work-v2-flag-error"]')?.textContent?.includes('flag unavailable') === true, 'flag error message');
  check(host.querySelector('[data-testid="work-v2-flag-retry"]') !== null, 'flag retry button missing');
  // CTA must be disabled during error.
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  equal(cta!.disabled, true, 'error flag CTA must be disabled');
  // Click must not trigger any creation.
  click(host, 'work-new-btn');
  equal(createInputs.length, 0, 'CreateWork called while flag rejected');
  equal(planningInputs.length, 0, 'BeginWorkPlanning called while flag rejected');
});

await test('v2Flag retry after rejection: resolves to true, CTA switches to V2', async () => {
  v2FlagRejects = true;
  collaborationV2Enabled = true;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  check(host.querySelector('[data-testid="work-v2-flag-error"]') !== null, 'initial flag error missing');
  // Retry: flip from reject to resolve.
  v2FlagRejects = false;
  click(host, 'work-v2-flag-retry');
  await settle();
  check(host.querySelector('[data-testid="work-v2-flag-error"]') === null, 'flag error not cleared after retry');
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  equal(cta!.disabled, false, 'CTA still disabled after retry success');
  // Clicking CTA should now route to V2 planning.
  click(host, 'work-new-btn');
  equal(planningInputs.length, 1, 'BeginWorkPlanning not called after flag retry resolved to true');
  equal(createInputs.length, 0, 'CreateWork called after flag retry resolved to true');
});

await test('v2Flag late resolve to true: CTA transitions from pending to V2', async () => {
  v2FlagPending = true;
  collaborationV2Enabled = true;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  // Pending: CTA disabled.
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  equal(cta!.disabled, true, 'pending CTA not disabled');
  // Resolve the deferred flag.
  v2Deferreds[0].resolve(true);
  await settle();
  equal(cta!.disabled, false, 'CTA still disabled after flag resolved');
  // Clicking CTA should route to V2.
  click(host, 'work-new-btn');
  equal(planningInputs.length, 1, 'BeginWorkPlanning not called after late flag resolve to true');
  equal(createInputs.length, 0, 'CreateWork called after late flag resolve to true');
});

await test('v2Flag late resolve to false: CTA transitions from pending to V1 dialog', async () => {
  v2FlagPending = true;
  collaborationV2Enabled = false;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  equal(cta!.disabled, true, 'pending CTA not disabled');
  // Resolve to false.
  v2Deferreds[0].resolve(false);
  await settle();
  equal(cta!.disabled, false, 'CTA still disabled after flag resolved');
  // Clicking CTA should open V1 dialog.
  click(host, 'work-new-btn');
  check(host.querySelector('[data-testid="work-create-form"]') !== null, 'V1 create form did not open after flag resolved to false');
  equal(planningInputs.length, 0, 'BeginWorkPlanning called when flag resolved to false');
  equal(createInputs.length, 0, 'CreateWork called before form submit');
});

await test('tab switch during pending flag discards old flag result', async () => {
  v2FlagPending = true;
  const { host, rerender } = await mount(<WorkPage tabID="tab-a" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  // Switch tab while flag is pending.
  await rerender(<WorkPage tabID="tab-b" onBack={() => undefined} onOpenWork={() => undefined} />);
  // Resolve the old tab's flag — must be ignored.
  v2Deferreds[0].resolve(true);
  await settle();
  // tab-b should have its own pending flag (v2FlagPending is still true).
  // But since rerender triggers a new mount cycle, a new deferred is created.
  // Actually, the original v2Deferreds[0] was for tab-a. tab-b creates v2Deferreds[1].
  // Let's resolve tab-b's flag to false.
  equal(v2Deferreds.length, 2, 'expected 2 flag requests');
  v2Deferreds[1].resolve(false);
  await settle();
  // tab-b CTA should be enabled and open V1 dialog.
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  equal(cta!.disabled, false, 'tab-b CTA still disabled');
  click(host, 'work-new-btn');
  check(host.querySelector('[data-testid="work-create-form"]') !== null, 'tab-b V1 dialog not opened');
});

await test('double click on V2 CTA: single planning call', async () => {
  collaborationV2Enabled = true;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  click(host, 'work-new-btn');
  equal(planningInputs.length, 1, 'duplicate BeginWorkPlanning calls on double click');
  equal(createInputs.length, 0, 'CreateWork must not be called when V2 enabled');
});

await test('V2 enabled: V1 history items readable, zero CreateWork calls', async () => {
  collaborationV2Enabled = true;
  const opened: string[] = [];
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={(id) => { opened.push(id); }} />);
  listDeferreds[0].resolve(page([summary('work-v1', 'Legacy Work')]));
  await settle();
  // V1 history items must render.
  check(host.querySelector('[data-testid="work-item-work-v1"]') !== null, 'V1 history work not rendered');
  // Opening a V1 work must not trigger CreateWork.
  act(() => {
    (host.querySelector('[data-testid="work-item-work-v1"] button') as HTMLButtonElement).click();
  });
  equal(opened[0], 'work-v1', 'V1 work did not open');
  equal(createInputs.length, 0, 'CreateWork called when opening V1 history');
});

await test('V2 disabled: CTA opens V1 CreateWorkDialog, zero BeginWorkPlanning', async () => {
  collaborationV2Enabled = false;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  check(host.querySelector('[data-testid="work-create-form"]') !== null, 'V1 create form did not open');
  check(host.querySelector('[data-testid="work-begin-planning"]') === null, 'stale V2 button leaked');
  equal(planningInputs.length, 0, 'BeginWorkPlanning called when V2 disabled');
});

await test('no duplicate CTAs: exactly one work-new-btn, zero work-begin-planning, zero work-empty-new-btn', async () => {
  collaborationV2Enabled = true;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  const ctas = host.querySelectorAll('[data-testid="work-new-btn"]');
  equal(ctas.length, 1, 'more than one work-new-btn');
  check(host.querySelector('[data-testid="work-begin-planning"]') === null, 'separate V2 planning button leaked');
  check(host.querySelector('[data-testid="work-empty-new-btn"]') === null, 'empty-state duplicate CTA leaked');
  // Also verify with items in the list — still only one CTA.
});

await test('non-empty tab with missing WorkCollaborationV2Enabled binding: error, CTA disabled, zero creation', async () => {
  v2BindingMissing = true;
  // resetBridge already built the mock with WorkCollaborationV2Enabled present;
  // remove it now to simulate a missing binding.
  delete (window.go.main.App as Record<string, unknown>).WorkCollaborationV2Enabled;
  const { host } = await mount(<WorkPage tabID="tab-1" onBack={() => undefined} onOpenWork={() => undefined} />);
  listDeferreds[0].resolve(page());
  await settle();
  // Must show error, not silently fall back to V1.
  check(host.querySelector('[data-testid="work-v2-flag-error"]') !== null, 'missing-binding error missing');
  check(host.querySelector('[data-testid="work-v2-flag-error"]')?.textContent?.includes('尚未连接') === true, 'missing-binding error message');
  // CTA must be disabled.
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  equal(cta!.disabled, true, 'missing-binding CTA must be disabled');
  // Click must not trigger any creation.
  click(host, 'work-new-btn');
  equal(createInputs.length, 0, 'CreateWork called when binding missing');
  equal(planningInputs.length, 0, 'BeginWorkPlanning called when binding missing');
  // V1 form must not appear.
  check(host.querySelector('[data-testid="work-create-form"]') === null, 'V1 form appeared when binding missing');
});

await test('empty tab with missing binding: CTA disabled, zero creation', async () => {
  v2BindingMissing = true;
  delete (window.go.main.App as Record<string, unknown>).WorkCollaborationV2Enabled;
  await mount(<WorkPage tabID="" onBack={() => undefined} onOpenWork={() => undefined} />);
  // Empty tab → no list call. CTA still rendered but must be disabled.
  equal(listTabs.length, 0, 'ListWorks called for empty tab');
  equal(createInputs.length, 0, 'CreateWork called for empty tab');
  equal(planningInputs.length, 0, 'BeginWorkPlanning called for empty tab');
});

await test('pending planning → tab switch → late result discarded, new tab CTA usable with correct routing', async () => {
  collaborationV2Enabled = true;
  const opened: string[] = [];
  const { host, rerender } = await mount(
    <WorkPage tabID="tab-a" sessionID="sess-a" onBack={() => undefined} onOpenWork={(id) => { opened.push(`a:${id}`); }} />,
  );
  listDeferreds[0].resolve(page());
  await settle();
  // Start planning on tab-a.
  click(host, 'work-new-btn');
  equal(planningInputs.length, 1, 'tab-a BeginWorkPlanning call');
  // Switch to tab-b while planning is pending.
  await rerender(
    <WorkPage tabID="tab-b" sessionID="sess-b" onBack={() => undefined} onOpenWork={(id) => { opened.push(`b:${id}`); }} />,
  );
  equal(listDeferreds.length, 2, 'tab-b ListWorks not triggered');
  listDeferreds[1].resolve(page());
  await settle();
  // tab-b CTA must be enabled (planning was reset on identity change).
  const cta = host.querySelector('[data-testid="work-new-btn"]') as HTMLButtonElement | null;
  equal(cta!.disabled, false, 'tab-b CTA still disabled after tab switch');
  // Now resolve the old tab-a planning — must not navigate or affect tab-b.
  planningDeferreds[0].resolve({
    result: { schemaVersion: 2, revision: 1, work: { id: 'stale-work' } },
    revision: 1,
    duplicate: false,
    committed: true,
    recoverable: false,
  });
  await settle();
  equal(opened.length, 0, 'late tab-a planning opened a Work on tab-b');
  // tab-b CTA must route to V2 (BeginWorkPlanning), not V1.
  click(host, 'work-new-btn');
  equal(planningInputs.length, 2, 'tab-b BeginWorkPlanning not triggered');
  equal(createInputs.length, 0, 'tab-b routed to V1 CreateWork after flag resolved true');
});

// ── V2 BeginPlanning lowercase-camel contract tests ─────────────────────
// These verify that the adapter correctly rejects PascalCase-only and
// malformed responses, and handles committed-recovery / duplicate retry
// without calling V1 CreateWork.

await test('V2 BeginPlanning: PascalCase-only → not committed, error displayed, retryable', async () => {
  collaborationV2Enabled = true;
  const opened: string[] = [];
  const { host } = await mount(
    <WorkPage tabID="tab-pc" sessionID="sess-pc" onBack={() => undefined} onOpenWork={(id) => { opened.push(id); }} />,
  );
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  equal(planningInputs.length, 1, 'planning call');
  equal(createInputs.length, 0, 'no V1 CreateWork');
  // Resolve with PascalCase response → adapter returns contract_malformed.
  planningDeferreds[0].resolve({
    Result: { schemaVersion: 2, revision: 1, work: { id: 'bad' } },
    Revision: 1,
    Duplicate: false,
    Committed: true,
    Recoverable: false,
  });
  await settle();
  // Must not open any Work.
  equal(opened.length, 0, 'PascalCase-only response opened a Work');
  // Error must be visible and retryable (retry button present).
  const errEl = host.querySelector('[data-testid="work-planning-error"]');
  check(errEl !== null, 'no planning error displayed for PascalCase response');
  check(errEl!.textContent!.length > 0, 'planning error text is empty');
  const retryBtn = host.querySelector('[data-testid="work-planning-retry"]');
  check(retryBtn !== null, 'retry button missing for PascalCase error');
  equal(createInputs.length, 0, 'V1 CreateWork called on PascalCase failure');
});

await test('V2 BeginPlanning: committed without result.work.id → not opened, error displayed', async () => {
  collaborationV2Enabled = true;
  const opened: string[] = [];
  const { host } = await mount(
    <WorkPage tabID="tab-cnp" sessionID="sess-cnp" onBack={() => undefined} onOpenWork={(id) => { opened.push(id); }} />,
  );
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  planningDeferreds[0].resolve({
    revision: 1,
    duplicate: false,
    committed: true,
    recoverable: false,
    // No result field at all
  });
  await settle();
  equal(opened.length, 0, 'committed-no-payload opened a Work');
  const errEl = host.querySelector('[data-testid="work-planning-error"]');
  check(errEl !== null, 'no error for committed-no-payload');
  const retryBtn = host.querySelector('[data-testid="work-planning-retry"]');
  check(retryBtn !== null, 'retry button missing for committed-no-payload');
});

await test('V2 BeginPlanning: committed-recovery opens real workID after refresh, no CreateWork', async () => {
  collaborationV2Enabled = true;
  const opened: string[] = [];
  const { host } = await mount(
    <WorkPage tabID="tab-cr" sessionID="sess-cr" onBack={() => undefined} onOpenWork={(id) => { opened.push(id); }} />,
  );
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  equal(planningInputs.length, 1, 'first planning');
  equal(createInputs.length, 0, 'no V1 CreateWork');
  // First response: committed-recovery (no result payload).
  const crWorkId = 'w-cr-123';
  planningDeferreds[0].resolve({
    revision: 5,
    duplicate: false,
    committed: true,
    recoverable: true,
    transportError: {
      code: 'committed_recovery',
      message: 'committed but response lost',
      operation: 'BeginWorkPlanning',
      workId: crWorkId,
      requestId: planningInputs[0].requestId,
      committed: true,
      recoverable: true,
    },
  });
  await settle();
  // Committed-recovery triggers a ListWorks refresh.
  equal(listDeferreds.length, 2, 'list refresh not triggered for committed-recovery');
  // Resolve the refresh with the recovered work in the list.
  listDeferreds[1].resolve(page([summary(crWorkId, 'Recovered')]));
  await settle();
  // After refresh resolves, onOpenWork must be called exactly once with transportError.workId.
  equal(opened.length, 1, 'committed-recovery did not open exactly one Work');
  equal(opened[0], crWorkId, 'committed-recovery opened wrong workID');
  equal(createInputs.length, 0, 'V1 CreateWork called during committed-recovery');
});

await test('V2 BeginPlanning: network fail → retry same requestID → duplicate committed replay opens real Work', async () => {
  collaborationV2Enabled = true;
  const opened: string[] = [];
  const { host } = await mount(
    <WorkPage tabID="tab-replay" sessionID="sess-replay" onBack={() => undefined} onOpenWork={(id) => { opened.push(id); }} />,
  );
  listDeferreds[0].resolve(page());
  await settle();
  click(host, 'work-new-btn');
  const firstReqId = planningInputs[0].requestId;
  // First attempt: network error.
  planningDeferreds[0].reject(new Error('offline'));
  await settle();
  equal(createInputs.length, 0, 'V1 CreateWork on first V2 failure');
  // Retry with same requestID.
  click(host, 'work-planning-retry');
  equal(planningInputs.length, 2, 'second planning call');
  equal(planningInputs[1].requestId, firstReqId, 'retry reuses requestID');
  equal(createInputs.length, 0, 'V1 CreateWork on retry click');
  // Backend recognizes idempotent retry: duplicate=true, committed=true, result present.
  const replayWorkId = 'w-replay';
  planningDeferreds[1].resolve({
    result: { schemaVersion: 2, revision: 1, work: { id: replayWorkId } },
    revision: 1,
    duplicate: true,
    committed: true,
    recoverable: false,
  });
  await settle();
  // Planning success triggers ListWorks refresh.
  equal(listDeferreds.length, 2, 'list refresh not triggered for replay');
  listDeferreds[1].resolve(page([summary(replayWorkId, 'Replayed')]));
  await settle();
  // Must open the replayed work exactly once.
  equal(opened.length, 1, 'replay did not open exactly one Work');
  equal(opened[0], replayWorkId, 'replay opened wrong workID');
  equal(createInputs.length, 0, 'V1 CreateWork called during replay');
  equal(planningInputs.length, 2, 'third BeginPlanning call after committed replay');
});

process.stdout.write(`\nWorkPage: ${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
