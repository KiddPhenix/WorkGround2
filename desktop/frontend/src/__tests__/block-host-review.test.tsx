// Reviewer regression overlay for the BlockHost trust and lifecycle boundary.
// Run: tsx src/__tests__/block-host-review.test.tsx

import { JSDOM } from 'jsdom';
import React, { act, useEffect } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { BlockHost } from '../components/work/blocks/BlockHost';
import { blockRegistry, createRegistry, isRendererKind } from '../components/work/blocks/registry';
import { buildSafeJson } from '../components/work/blocks/safeBlockJson';
import type { BlockRendererProps, RendererModule } from '../components/work/blocks/types';
import type { BlockActionRequest, BlockInstance } from '../work/types';

const originalError = console.error.bind(console);
const context = { workId: 'review_work', workSchemaVersion: 1 };
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
    id: 'review_block',
    kind: 'review_value',
    schemaVersion: 1,
    revision: 1,
    status: 'ready',
    data: { label: 'one' },
    source: { provider: 'controller', ref: 'digest_1', mode: 'snapshot', verified: true },
    fallback: { summary: 'safe summary' },
    createdAt: '2026-07-21T00:00:00Z',
    updatedAt: '2026-07-21T00:00:00Z',
    ...patch,
  };
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

function text(): string {
  return document.getElementById('root')?.textContent ?? '';
}

async function click(element: Element): Promise<boolean> {
  try {
    await act(async () => {
      element.dispatchEvent(new dom.window.MouseEvent('click', { bubbles: true, cancelable: true }));
    });
    await flush();
    return true;
  } catch {
    return false;
  }
}

function request(target: BlockInstance): BlockActionRequest {
  return {
    workId: context.workId,
    blockId: target.id,
    actionId: 'run',
    requestId: `request_${target.revision}`,
    expectedRevision: target.revision,
  };
}

async function run(): Promise<void> {
  setup();

  console.log('\n-- full identity and late-result guards');
  const Value: React.FC<BlockRendererProps> = ({ block: current }) => (
    <div data-value={String((current.data as { label?: string }).label)}>
      value:{String((current.data as { label?: string }).label)}:{current.revision}:{current.source.ref}
    </div>
  );
  blockRegistry.register('review_value', 1, () => ({ valid: true }), async () => ({ component: Value }));
  const first = block();
  await render(<BlockHost block={first} context={context} />);
  await waitFor(() => text().includes('value:one'), 'first identity');

  const statusChanged = block({ status: 'blocked' });
  await render(<BlockHost block={statusChanged} context={context} />);
  ok(text().includes('Block is blocked') && !text().includes('value:one'), 'status identity cannot paint the old renderer');

  const dataChanged = block({ data: { label: 'two' } });
  await render(<BlockHost block={dataChanged} context={context} />);
  await waitFor(() => text().includes('value:two'), 'data identity');
  ok(!text().includes('value:one'), 'data identity cannot retain stale renderer output');

  const versionChanged = block({ revision: 2, source: { ...first.source, ref: 'digest_2' }, data: { label: 'three' } });
  await render(<BlockHost block={versionChanged} context={context} />);
  await waitFor(() => text().includes('value:three:2:digest_2'), 'revision and digest identity');
  ok(!text().includes('value:two'), 'revision/digest identity cannot retain prior output');

  let rejectLate!: (reason?: unknown) => void;
  const late = new Promise<RendererModule>((_resolve, reject) => { rejectLate = reject; });
  blockRegistry.register('review_late', 1, () => ({ valid: true }), () => late);
  await render(<BlockHost block={block({ kind: 'review_late', revision: 3 })} context={context} />);
  await render(<BlockHost block={block({ revision: 4, data: { label: 'winner' } })} context={context} />);
  await waitFor(() => text().includes('value:winner'), 'replacement after pending import');
  await act(async () => { rejectLate(new Error('Bearer stale-secret C:\\private')); });
  await flush();
  ok(text().includes('value:winner') && !text().includes('failed to load'), 'late import error cannot overwrite a newer identity');

  const strictLogs: unknown[][] = [];
  console.error = (...args: unknown[]) => { strictLogs.push(args); };
  await render(
    <React.StrictMode>
      <BlockHost block={block({ revision: 5, data: { label: 'strict' } })} context={context} />
    </React.StrictMode>,
  );
  await waitFor(() => text().includes('value:strict'), 'StrictMode renderer lifecycle');
  console.error = originalError;
  ok(!JSON.stringify(strictLogs).includes('createRoot'), 'StrictMode reuses the nested root without duplicate-root races');

  console.log('\n-- live, identity-scoped action gate');
  const cached: NonNullable<BlockRendererProps['onAction']>[] = [];
  const CaptureAction: React.FC<BlockRendererProps> = ({ onAction }) => {
    useEffect(() => { if (onAction) cached.push(onAction); }, [onAction]);
    return <div>action renderer</div>;
  };
  blockRegistry.register('review_action', 1, () => ({ valid: true }), async () => ({ component: CaptureAction }));
  const actionA = block({ id: 'action_a', kind: 'review_action', revision: 10 });
  let callsA = 0;
  await render(<BlockHost block={actionA} context={context} onAction={() => { callsA++; }} />);
  await waitFor(() => cached.length === 1, 'capture active action');
  const oldAction = cached[0];
  await act(async () => { oldAction(request(actionA)); });
  ok(callsA === 1, 'matching active action is forwarded');

  await render(<BlockHost block={actionA} context={context} archived onAction={() => { callsA++; }} />);
  await act(async () => { oldAction(request(actionA)); });
  ok(callsA === 1, 'cached action is blocked after archive without relying on undefined props');

  const actionB = block({ id: 'action_b', kind: 'review_action', revision: 11 });
  let callsB = 0;
  await render(<BlockHost block={actionB} context={context} onAction={() => { callsB++; }} />);
  await waitFor(() => cached.length >= 2, 'capture replacement action');
  await act(async () => { oldAction(request(actionA)); });
  ok(callsA === 1 && callsB === 0, 'old wrapper cannot route to a new block or callback');
  const newAction = cached[cached.length - 1];
  await act(async () => { newAction(request(actionB)); });
  ok(callsB === 1, 'new identity receives only its matching request');
  await act(async () => { newAction({ ...request(actionB), expectedRevision: 999 }); });
  ok(callsB === 1, 'revision mismatch fails closed');

  const syncFail = block({ id: 'action_sync_fail', kind: 'review_action', revision: 12 });
  const beforeSync = cached.length;
  await render(<BlockHost block={syncFail} context={context} onAction={() => { throw new Error('Bearer action-secret'); }} />);
  await waitFor(() => cached.length > beforeSync, 'capture throwing action');
  let syncActionContained = true;
  try {
    await act(async () => { cached[cached.length - 1](request(syncFail)); });
  } catch {
    syncActionContained = false;
  }
  await waitFor(() => text().includes('Renderer failed safely'), 'sync action fallback');
  ok(syncActionContained, 'controlled synchronous action failure is contained');

  const asyncFail = block({ id: 'action_async_fail', kind: 'review_action', revision: 13 });
  const beforeAsync = cached.length;
  await render(<BlockHost block={asyncFail} context={context} onAction={async () => { throw new Error('token=async-action-secret'); }} />);
  await waitFor(() => cached.length > beforeAsync, 'capture rejecting action');
  await act(async () => { cached[cached.length - 1](request(asyncFail)); });
  await waitFor(() => text().includes('Renderer failed safely'), 'async action fallback');
  ok(text().includes('Renderer failed safely'), 'controlled asynchronous action rejection is contained');

  const staleRetry = document.querySelector('.wg2-block-retry') as HTMLButtonElement;
  await render(<BlockHost block={block({ revision: 14, data: { label: 'retry_winner' } })} context={context} />);
  await waitFor(() => text().includes('value:retry_winner'), 'replacement after action failure');
  await click(staleRetry);
  ok(text().includes('value:retry_winner') && !text().includes('Renderer failed safely'), 'retry control from an old identity cannot affect the replacement');

  console.log('\n-- nested-root render, effect, event, and logger isolation');
  const Sibling: React.FC = () => <div>sibling remains healthy</div>;
  const RenderCrash: React.FC = () => { throw new Error('Bearer render-secret C:\\render-path'); };
  const EffectCrash: React.FC = () => {
    useEffect(() => { throw new Error('token=effect-secret C:\\effect-path'); }, []);
    return <div>effect pending</div>;
  };
  const EventCrash: React.FC = () => (
    <button type="button" onClick={() => { throw new Error('password=event-secret C:\\event-path'); }}>
      explode event
    </button>
  );
  blockRegistry.register('review_sibling', 1, () => ({ valid: true }), async () => ({ component: Sibling }));
  blockRegistry.register('review_render_crash', 1, () => ({ valid: true }), async () => ({ component: RenderCrash }));
  blockRegistry.register('review_effect_crash', 1, () => ({ valid: true }), async () => ({ component: EffectCrash }));
  blockRegistry.register('review_event_crash', 1, () => ({ valid: true }), async () => ({ component: EventCrash }));

  const safeLogs: unknown[][] = [];
  console.error = (...args: unknown[]) => { safeLogs.push(args); };
  await render(
    <div>
      <BlockHost block={block({ id: 'render_crash', kind: 'review_render_crash', revision: 20 })} context={context} />
      <BlockHost block={block({ id: 'render_sibling', kind: 'review_sibling', revision: 20 })} context={context} />
    </div>,
  );
  await waitFor(() => text().includes('Renderer failed safely'), 'render crash isolation');
  ok(text().includes('sibling remains healthy'), 'render crash leaves sibling mounted');

  await render(
    <div>
      <BlockHost block={block({ id: 'effect_crash', kind: 'review_effect_crash', revision: 21 })} context={context} />
      <BlockHost block={block({ id: 'effect_sibling', kind: 'review_sibling', revision: 21 })} context={context} />
    </div>,
  );
  await waitFor(() => text().includes('Renderer failed safely'), 'effect crash isolation');
  ok(text().includes('sibling remains healthy'), 'effect crash leaves sibling mounted');

  await render(
    <div>
      <BlockHost block={block({ id: 'event_crash', kind: 'review_event_crash', revision: 22 })} context={context} />
      <BlockHost block={block({ id: 'event_sibling', kind: 'review_sibling', revision: 22 })} context={context} />
    </div>,
  );
  await waitFor(() => text().includes('explode event'), 'event renderer');
  const eventStayedContained = await click(document.querySelector('.wg2-renderer-island button')!);
  await waitFor(() => text().includes('Renderer failed safely'), 'event crash isolation');
  ok(eventStayedContained, 'onClick throw does not escape the host interaction');
  ok(text().includes('sibling remains healthy'), 'event crash degrades only its own block');
  const logText = JSON.stringify(safeLogs);
  ok(!/render-secret|effect-secret|event-secret|\\render-path|\\effect-path|\\event-path/.test(logText), 'renderer logs contain no raw messages, paths, or secrets');

  console.error = () => { throw new Error('logger itself failed'); };
  let loggerContained = true;
  try {
    const LoggerCrash: React.FC = () => { throw new Error('Bearer logger-secret'); };
    blockRegistry.register('review_logger_crash', 1, () => ({ valid: true }), async () => ({ component: LoggerCrash }));
    await render(<BlockHost block={block({ kind: 'review_logger_crash', revision: 23 })} context={context} />);
    await waitFor(() => text().includes('Renderer failed safely'), 'throwing logger isolation');
  } catch {
    loggerContained = false;
  }
  console.error = originalError;
  ok(loggerContained, 'throwing telemetry logger cannot escape renderer failure handling');

  console.log('\n-- validator secrecy and bounded serializer');
  const validationLogs: unknown[][] = [];
  console.error = (...args: unknown[]) => { validationLogs.push(args); };
  blockRegistry.register(
    'review_invalid_data',
    1,
    () => ({ valid: false, reason: 'Bearer validator-secret C:\\validator-private' }),
    async () => ({ component: Value }),
  );
  await render(<BlockHost block={block({ kind: 'review_invalid_data', revision: 30 })} context={context} />);
  ok(text().includes('Invalid data for renderer'), 'validator exposes a fixed safe message');
  ok(!/validator-secret|validator-private/.test(text() + JSON.stringify(validationLogs)), 'validator reason is absent from UI and logs');

  console.error = () => { throw new Error('logger failed during validation'); };
  let validatorThrowContained = true;
  try {
    blockRegistry.register('review_validator_throw', 1, () => { throw new Error('token=validator-throw'); }, async () => ({ component: Value }));
    await render(<BlockHost block={block({ kind: 'review_validator_throw', revision: 31 })} context={context} />);
  } catch {
    validatorThrowContained = false;
  }
  console.error = originalError;
  ok(validatorThrowContained && text().includes('Invalid data for renderer'), 'validator and logger throws are both contained');

  let getterReads = 0;
  let toJSONReads = 0;
  const shared = { visible: 'yes' };
  const wide: Record<string, unknown> = {};
  for (let index = 0; index < 32_768; index++) wide[`field_${index}`] = index;
  const deep: Record<string, unknown> = {};
  let cursor = deep;
  for (let index = 0; index < 100; index++) {
    const next: Record<string, unknown> = {};
    cursor.next = next;
    cursor = next;
  }
  const serialInput: Record<string, unknown> = {
    'API-TOKEN': 'one',
    'client.secret': 'two',
    'user password': 'three',
    AUTH_COOKIE: 'four',
    sessionKey: 'five',
    authorization: 'Bearer six',
    sharedA: shared,
    sharedB: shared,
    wide,
    deep,
  };
  Object.defineProperty(serialInput, 'getter', {
    enumerable: true,
    get() { getterReads++; return 'Bearer getter-secret'; },
  });
  Object.defineProperty(serialInput, 'toJSON', {
    enumerable: false,
    get() { toJSONReads++; return () => ({ leaked: true }); },
  });
  const serialized = buildSafeJson(serialInput);
  const envelope = JSON.parse(serialized) as {
    meta: { redacted: boolean; truncated: boolean; properties: number; reasons: string[] };
    value: Record<string, unknown>;
  };
  ok(envelope.meta.redacted && envelope.meta.truncated, 'serializer reports redaction and truncation metadata');
  ok(envelope.meta.properties <= 4_096 && new TextEncoder().encode(serialized).byteLength <= 64 * 1024, 'one global property/byte budget bounds wide and deep data');
  ok(getterReads === 0 && toJSONReads === 0, 'serializer never invokes getters or toJSON');
  ok(['API-TOKEN', 'client.secret', 'user password', 'AUTH_COOKIE', 'sessionKey', 'authorization']
    .every((key) => envelope.value[key] === '[redacted]'), 'secret keys normalize case and separators before redaction');
  ok(envelope.value.sharedB === '[shared reference]', 'shared references consume one traversal budget');

  const hostile = new Proxy({}, {
    ownKeys() { throw new Error('Bearer proxy-secret'); },
  });
  const hostileEnvelope = JSON.parse(buildSafeJson(hostile)) as { meta: { truncated: boolean }; value: unknown };
  ok(hostileEnvelope.meta.truncated && hostileEnvelope.value === '[object unavailable]', 'proxy descriptor failures produce valid safe JSON');

  console.log('\n-- centralized kind grammar');
  const registry = createRegistry();
  const invalidKinds = ['../module', 'module/path', 'module.name', 'bad-kind', 'BadKind', 'bad\u0000kind', `a${'b'.repeat(64)}`];
  ok(invalidKinds.every((kind) => !isRendererKind(kind)), 'path, module, control, case, separator, and length violations are rejected');
  ok(invalidKinds.every((kind) => {
    try {
      registry.register(kind, 1, () => ({ valid: true }), async () => ({ component: Value }));
      return false;
    } catch {
      return true;
    }
  }), 'registry registration uses the centralized kind grammar');

  await act(async () => { root.unmount(); });
  await flush();
  process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed) process.exit(1);
}

run().catch((error) => {
  console.error = originalError;
  originalError('block host reviewer overlay failed', error);
  process.exit(1);
});
