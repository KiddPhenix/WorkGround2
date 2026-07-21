// Reviewer3 regressions for nested event ownership and collision-free identity.
// Run: tsx src/__tests__/block-host-broker-review.test.tsx

import { JSDOM } from 'jsdom';
import React, { act, useEffect, useRef } from 'react';
import { flushSync } from 'react-dom';
import { createRoot, type Root } from 'react-dom/client';
import { BlockHost } from '../components/work/blocks/BlockHost';
import { blockRegistry } from '../components/work/blocks/registry';
import { createBlockRenderIdentity, matchesBlockRenderIdentity } from '../components/work/blocks/safeBlockJson';
import type { BlockRendererProps } from '../components/work/blocks/types';
import type { BlockInstance } from '../work/types';

const originalError = console.error.bind(console);
const context = { workId: 'review3_work', workSchemaVersion: 1 };
let passed = 0;
let failed = 0;
let dom: JSDOM;
let root: Root;

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
    ErrorEvent: dom.window.ErrorEvent,
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
    id: 'review3_block',
    kind: 'review3_plain',
    schemaVersion: 1,
    revision: 1,
    status: 'ready',
    data: {},
    source: { provider: 'controller', ref: 'review3_digest', mode: 'snapshot', verified: true },
    fallback: { summary: 'review3 safe summary' },
    createdAt: '2026-07-21T00:00:00Z',
    updatedAt: '2026-07-21T00:00:00Z',
    ...patch,
  };
}

function hostFailed(id: string): boolean {
  return document.querySelector(`#${id} .wg2-block-error`) !== null;
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

async function waitFor(predicate: () => boolean, label: string, ticks = 40): Promise<void> {
  for (let index = 0; index < ticks; index++) {
    if (predicate()) return;
    await flush();
  }
  ok(false, `${label} timed out`);
}

async function dispatch(target: Element, event: Event): Promise<boolean> {
  try {
    await act(async () => { target.dispatchEvent(event); });
    await flush();
    return true;
  } catch {
    return false;
  }
}

interface NestedData {
  dispatchB: boolean;
  throwSelf: boolean;
}

const NestedA: React.FC<BlockRendererProps> = ({ block: current }) => {
  const data = current.data as NestedData;
  return (
    <button
      type="button"
      data-review3="nested-a"
      onClick={() => {
        if (data.dispatchB) {
          document.querySelector('[data-review3="nested-b"]')?.dispatchEvent(
            new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }),
          );
        }
        if (data.throwSelf) throw new Error('Bearer nested-a-private');
      }}
    >
      nested A
    </button>
  );
};

const NestedB: React.FC<BlockRendererProps> = ({ block: current }) => {
  const data = current.data as NestedData;
  return (
    <button
      type="button"
      data-review3="nested-b"
      onClick={() => {
        if (data.throwSelf) throw new Error('token=nested-b-private');
      }}
    >
      nested B
    </button>
  );
};

const Plain: React.FC<BlockRendererProps> = ({ block: current }) => (
  <div data-review3="plain">plain:{String(current.data)}</div>
);

interface PhaseData {
  mode: 'normal' | 'stop_immediate' | 'stop_throw';
}

const PhaseRenderer: React.FC<BlockRendererProps> = ({ block: current }) => {
  const mode = (current.data as PhaseData).mode;
  return (
    <button
      type="button"
      data-review3="phase"
      onClick={(event) => {
        if (mode === 'stop_immediate') event.nativeEvent.stopImmediatePropagation();
        if (mode === 'stop_throw') {
          event.stopPropagation();
          throw new Error('Bearer stop-propagation-private');
        }
      }}
    >
      phase event
    </button>
  );
};

interface NativeCrashData {
  eventType: string;
}

const NativeCrash: React.FC<BlockRendererProps> = ({ block: current }) => {
  const ref = useRef<HTMLDivElement>(null);
  const eventType = (current.data as NativeCrashData).eventType;
  useEffect(() => {
    const target = ref.current;
    if (!target) return;
    const crash = () => { throw new Error('password=native-event-private'); };
    target.addEventListener(eventType, crash);
    return () => target.removeEventListener(eventType, crash);
  }, [eventType]);
  return <div ref={ref} tabIndex={0} data-review3="native-crash">native {eventType}</div>;
};

function nestedPair(dataA: NestedData, dataB: NestedData, revision: number): React.ReactElement {
  return (
    <div>
      <div id="nested-host-a">
        <BlockHost
          block={block({ id: 'nested_a', kind: 'review3_nested_a', revision, data: dataA })}
          context={context}
        />
      </div>
      <div id="nested-host-b">
        <BlockHost
          block={block({ id: 'nested_b', kind: 'review3_nested_b', revision, data: dataB })}
          context={context}
        />
      </div>
    </div>
  );
}

async function run(): Promise<void> {
  setup();
  blockRegistry.register('review3_nested_a', 1, () => ({ valid: true }), async () => ({ component: NestedA }));
  blockRegistry.register('review3_nested_b', 1, () => ({ valid: true }), async () => ({ component: NestedB }));
  blockRegistry.register('review3_plain', 1, () => ({ valid: true }), async () => ({ component: Plain }));
  blockRegistry.register('review3_phase', 1, () => ({ valid: true }), async () => ({ component: PhaseRenderer }));
  blockRegistry.register('review3_native', 1, () => ({ valid: true }), async () => ({ component: NativeCrash }));

  const safeLogs: unknown[][] = [];
  console.error = (...args: unknown[]) => { safeLogs.push(args); };

  console.log('\n-- nested frame stack ownership');
  await render(nestedPair({ dispatchB: true, throwSelf: true }, { dispatchB: false, throwSelf: false }, 1));
  await waitFor(() => document.querySelector('[data-review3="nested-a"]') !== null, 'nested A renderer');
  const aContained = await dispatch(
    document.querySelector('[data-review3="nested-a"]')!,
    new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }),
  );
  ok(aContained && hostFailed('nested-host-a'), 'A throw after nested B returns is attributed to A');
  ok(!hostFailed('nested-host-b'), 'normal nested B remains healthy when A throws');

  await render(nestedPair({ dispatchB: true, throwSelf: false }, { dispatchB: false, throwSelf: true }, 2));
  await waitFor(() => document.querySelector('[data-review3="nested-a"]') !== null, 'nested B throw setup');
  const bContained = await dispatch(
    document.querySelector('[data-review3="nested-a"]')!,
    new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }),
  );
  ok(bContained && hostFailed('nested-host-b'), 'B throw during nested dispatch is attributed to B');
  ok(!hostFailed('nested-host-a'), 'A remains healthy when only nested B throws');

  await render(nestedPair({ dispatchB: true, throwSelf: true }, { dispatchB: false, throwSelf: true }, 3));
  await waitFor(() => document.querySelector('[data-review3="nested-a"]') !== null, 'two-way nested throw setup');
  await dispatch(
    document.querySelector('[data-review3="nested-a"]')!,
    new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }),
  );
  ok(hostFailed('nested-host-a') && hostFailed('nested-host-b'), 'nested B then A throws degrade their own owners independently');

  console.log('\n-- owned and unowned ErrorEvent behavior');
  const observed: Array<{ defaultPrevented: boolean; marker: string }> = [];
  const observer = (event: ErrorEvent) => {
    const marker = event.error instanceof Error ? event.error.message : String(event.message);
    observed.push({ defaultPrevented: event.defaultPrevented, marker });
  };
  dom.window.addEventListener('error', observer);

  const phaseBlock = (revision: number, mode: PhaseData['mode']) => block({
    id: `phase_${revision}`,
    kind: 'review3_phase',
    revision,
    data: { mode },
  });
  await render(<BlockHost block={phaseBlock(10, 'stop_immediate')} context={context} />);
  await waitFor(() => document.querySelector('[data-review3="phase"]') !== null, 'eventPhase setup');
  const originalEvent = new dom.window.MouseEvent('click', { bubbles: true, cancelable: true });
  let unrelated: ErrorEvent | null = null;
  await act(async () => {
    document.querySelector('[data-review3="phase"]')!.dispatchEvent(originalEvent);
    unrelated = new dom.window.ErrorEvent('error', {
      cancelable: true,
      error: new Error('review3-unowned-after-dispatch'),
      message: 'review3-unowned-after-dispatch',
    });
    dom.window.dispatchEvent(unrelated);
  });
  await flush();
  ok(originalEvent.eventPhase === 0, 'completed original event reports EventPhase.NONE');
  ok(unrelated !== null && !unrelated.defaultPrevented, 'residual NONE frame does not prevent an unrelated ErrorEvent');
  ok(!text().includes('Renderer failed safely'), 'unowned error after dispatch does not degrade the block');
  ok(observed.some((item) => item.marker === 'review3-unowned-after-dispatch'), 'unowned ErrorEvent remains visible to other observers');

  observed.length = 0;
  await render(<BlockHost block={phaseBlock(11, 'stop_throw')} context={context} />);
  await waitFor(() => document.querySelector('[data-review3="phase"]') !== null, 'stopPropagation setup');
  const stopContained = await dispatch(
    document.querySelector('[data-review3="phase"]')!,
    new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }),
  );
  ok(stopContained && text().includes('Renderer failed safely'), 'stopPropagation does not break owned failure attribution');
  ok(observed.some((item) => item.marker.includes('stop-propagation-private') && item.defaultPrevented), 'owned error is prevented but still observed safely');

  await render(<BlockHost block={phaseBlock(13, 'stop_throw')} context={context} />);
  await waitFor(() => document.querySelector('[data-review3="phase"]') !== null, 'throwing logger event setup');
  console.error = () => { throw new Error('logger failed during event attribution'); };
  const loggerContained = await dispatch(
    document.querySelector('[data-review3="phase"]')!,
    new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }),
  );
  console.error = (...args: unknown[]) => { safeLogs.push(args); };
  ok(loggerContained && text().includes('Renderer failed safely'), 'broker and telemetry callbacks contain a throwing logger');

  observed.length = 0;
  await render(<BlockHost block={phaseBlock(14, 'normal')} context={context} />);
  await waitFor(() => document.querySelector('[data-review3="phase"]') !== null, 'continuous dispatch setup');
  await act(async () => {
    const target = document.querySelector('[data-review3="phase"]')!;
    for (let index = 0; index < 32; index++) {
      target.dispatchEvent(new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }));
    }
    const event = new dom.window.ErrorEvent('error', {
      cancelable: true,
      error: new Error('review3-unowned-after-sequence'),
      message: 'review3-unowned-after-sequence',
    });
    dom.window.dispatchEvent(event);
    ok(!event.defaultPrevented, 'continuous dispatch leaves no frame that claims a later error');
  });
  await flush();
  ok(!text().includes('Renderer failed safely'), 'continuous synchronous dispatch does not leave stale ownership');

  dom.window.removeEventListener('error', observer);

  console.log('\n-- focus, blur, wheel, and composition coverage');
  for (const [eventType, bubbles] of [
    ['focus', false],
    ['blur', false],
    ['wheel', true],
    ['compositionstart', true],
    ['compositionupdate', true],
    ['compositionend', true],
  ] as const) {
    await render(
      <BlockHost
        block={block({ id: `native_${eventType}`, kind: 'review3_native', revision: 20 + eventType.length, data: { eventType } })}
        context={context}
      />,
    );
    await waitFor(() => document.querySelector('[data-review3="native-crash"]') !== null, `${eventType} setup`);
    const contained = await dispatch(
      document.querySelector('[data-review3="native-crash"]')!,
      new dom.window.Event(eventType, { bubbles, cancelable: true }),
    );
    ok(contained && text().includes('Renderer failed safely'), `${eventType} failure is owned during actual dispatch`);
  }

  console.log('\n-- collision-free render identity');
  const collisionA = block({ id: 'collision', revision: 40, data: '2vcp_cta6my' });
  const collisionB = block({ id: 'collision', revision: 40, data: '2vnr_b7tdu0' });
  const identityA = createBlockRenderIdentity(collisionA);
  ok(!matchesBlockRenderIdentity(identityA, collisionB), 'known 32-bit FNV collision values have distinct identities');

  await render(<BlockHost block={collisionA} context={context} />);
  await waitFor(() => text().includes('2vcp_cta6my'), 'first collision value');
  let immediate = '';
  await act(async () => {
    flushSync(() => { root.render(<BlockHost block={collisionB} context={context} />); });
    immediate = text();
  });
  ok(!immediate.includes('2vcp_cta6my'), 'identity switch synchronously removes the old renderer module output');
  await waitFor(() => text().includes('2vnr_b7tdu0'), 'second collision value');
  ok(!text().includes('2vcp_cta6my'), 'collision pair cannot retain stale renderer data');

  console.log('\n-- global listener reference balance');
  await render(<div>listener reset</div>);
  await flush();
  const originalAdd = dom.window.addEventListener.bind(dom.window);
  const originalRemove = dom.window.removeEventListener.bind(dom.window);
  let errorAdds = 0;
  let errorRemoves = 0;
  dom.window.addEventListener = ((type: string, ...args: unknown[]) => {
    if (type === 'error') errorAdds++;
    (originalAdd as unknown as (type: string, ...args: unknown[]) => void)(type, ...args);
  }) as typeof dom.window.addEventListener;
  dom.window.removeEventListener = ((type: string, ...args: unknown[]) => {
    if (type === 'error') errorRemoves++;
    (originalRemove as unknown as (type: string, ...args: unknown[]) => void)(type, ...args);
  }) as typeof dom.window.removeEventListener;

  await render(
    <React.StrictMode>
      <div>
        {[1, 2, 3].map((revision) => (
          <BlockHost
            key={revision}
            block={block({ id: `listener_${revision}`, revision, data: `listener_${revision}` })}
            context={context}
          />
        ))}
      </div>
    </React.StrictMode>,
  );
  await waitFor(() => document.querySelectorAll('[data-review3="plain"]').length === 3, 'listener islands');
  ok(errorAdds === 1, 'multiple islands share one global window error listener');
  await render(<div>listeners unmounted</div>);
  await flush();
  ok(errorRemoves === 1 && errorAdds === errorRemoves, 'mount/unmount and StrictMode balance the global listener exactly');
  dom.window.addEventListener = originalAdd as typeof dom.window.addEventListener;
  dom.window.removeEventListener = originalRemove as typeof dom.window.removeEventListener;

  const logText = JSON.stringify(safeLogs);
  ok(!/nested-a-private|nested-b-private|native-event-private|stop-propagation-private/.test(logText), 'broker telemetry never logs raw event errors or secrets');
  console.error = originalError;

  await act(async () => { root.unmount(); });
  await flush();
  process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed) process.exit(1);
}

run().catch((error) => {
  console.error = originalError;
  originalError('block host broker reviewer overlay failed', error);
  process.exit(1);
});
