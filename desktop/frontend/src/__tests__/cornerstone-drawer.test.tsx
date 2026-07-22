import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { JSDOM } from 'jsdom';
import React, { act, type ReactElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { WorkCard } from '../components/work/WorkCard';
import { WorkRunEntry } from '../components/work/WorkRunEntry';
import { WorkControllerAdapter, type CornerstoneControllerPort, type WorkControllerPort, type WorkPortSubscription } from '../work/controller';
import { deriveCornerstoneAttention, useCornerstoneUIStore } from '../work/cornerstoneStore';
import { FakeWorkController, type FakeWorkControllerOptions } from '../work/fakeController';
import { useWorkStore, useWorkUIStore, type WorkUIPreference } from '../work/store';
import { createWailsCornerstoneAdapter, createWailsWorkControllerPort } from '../work/wailsAdapter';
import type {
  AcceptCornerstoneInput,
  Attempt,
  Cornerstone,
  CornerstoneMutationResult,
  FreezeCornerstoneInput,
  PinCornerstoneInput,
  RefreshCornerstoneInput,
  RemoveCornerstoneInput,
  RepairCornerstoneInput,
  RetryTaskInput,
  UndoCornerstoneInput,
  ValidateCornerstoneInput,
  ViewRecoveryIntent,
  WorkView,
  WorkViewEvent,
  WorkflowRun,
} from '../work/types';

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else failed++;
}

function eq<T>(actual: T, expected: T, label: string): void {
  ok(actual === expected, `${label}${actual === expected ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function contentDigest(content: string): string {
  const normalized = content.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
  return `sha256:${createHash('sha256').update(normalized, 'utf8').digest('hex')}`;
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
  HTMLInputElement: dom.window.HTMLInputElement,
  HTMLTextAreaElement: dom.window.HTMLTextAreaElement,
  HTMLSelectElement: dom.window.HTMLSelectElement,
  SVGElement: dom.window.SVGElement,
  Event: dom.window.Event,
  InputEvent: dom.window.InputEvent,
  MouseEvent: dom.window.MouseEvent,
  KeyboardEvent: dom.window.KeyboardEvent,
  MutationObserver: dom.window.MutationObserver,
  requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
  cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
});
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });

interface Mounted {
  host: HTMLDivElement;
  root: Root;
  cleanup: () => Promise<void>;
}

async function settle(delay = 20): Promise<void> {
  await act(async () => { await new Promise<void>((resolveWait) => setTimeout(resolveWait, delay)); });
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (reason: unknown) => void } {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((accept, fail) => { resolve = accept; reject = fail; });
  return { promise, resolve, reject };
}

async function mount(element: ReactElement): Promise<Mounted> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host, { onCaughtError: (error) => { throw error; } });
  await act(async () => { root.render(element); });
  await settle();
  return {
    host,
    root,
    cleanup: async () => {
      await act(async () => { root.unmount(); });
      host.remove();
    },
  };
}

async function click(element: Element | null, delay = 20): Promise<void> {
  if (!(element instanceof dom.window.HTMLElement)) throw new Error('click target missing');
  await act(async () => {
    element.click();
    await new Promise<void>((resolveWait) => setTimeout(resolveWait, delay));
  });
}

async function change(element: Element | null, value: string): Promise<void> {
  if (!(element instanceof dom.window.HTMLInputElement || element instanceof dom.window.HTMLTextAreaElement || element instanceof dom.window.HTMLSelectElement)) {
    throw new Error('change target missing');
  }
  await act(async () => {
    const previous = element.value;
    const prototype = element instanceof dom.window.HTMLInputElement
      ? dom.window.HTMLInputElement.prototype
      : element instanceof dom.window.HTMLTextAreaElement
        ? dom.window.HTMLTextAreaElement.prototype
        : dom.window.HTMLSelectElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value')?.set?.call(element, value);
    (element as typeof element & { _valueTracker?: { setValue: (next: string) => void } })._valueTracker?.setValue(previous);
    const propsKey = Object.keys(element).find((key) => key.startsWith('__reactProps$'));
    const props = propsKey
      ? (element as unknown as Record<string, { onChange?: (event: { target: typeof element; currentTarget: typeof element }) => void }>)[propsKey]
      : undefined;
    if (props?.onChange) {
      props.onChange({ target: element, currentTarget: element });
    } else {
      if (!(element instanceof dom.window.HTMLSelectElement)) {
        element.dispatchEvent(new dom.window.InputEvent('input', { bubbles: true, inputType: 'insertText', data: value }));
      }
      element.dispatchEvent(new dom.window.Event('change', { bubbles: true }));
    }
    await Promise.resolve();
  });
}

function button(scope: ParentNode, label: string): HTMLButtonElement {
  const result = [...scope.querySelectorAll<HTMLButtonElement>('button')]
    .find((candidate) => candidate.textContent?.replace(/\s+/g, '') === label.replace(/\s+/g, ''));
  if (!result) throw new Error(`button not found: ${label}`);
  return result;
}

function reset(): void {
  useWorkStore.getState().clearAll();
  useWorkUIStore.getState().clearAll();
  useCornerstoneUIStore.getState().clearAll();
}

function makeCornerstone(id: string, overrides: Partial<Cornerstone> = {}): Cornerstone {
  return {
    id,
    workId: 'work-cornerstone',
    type: 'instruction',
    title: id,
    content: 'accepted content',
    ref: { kind: 'inline' },
    mode: 'snapshot',
    digest: `digest-${id}`,
    required: false,
    status: 'active',
    tags: [],
    provenance: { kind: 'work', workId: 'work-cornerstone' },
    pinnedAt: '2026-07-20T10:00:00Z',
    updatedAt: '2026-07-20T10:00:00Z',
    ...overrides,
  };
}

function makeView(cornerstones: Cornerstone[], revision = 1): WorkView {
  const blueprintRef = { id: 'blueprint:test', schemaVersion: 1, version: 1 };
  return {
    schemaVersion: 1,
    revision,
    work: {
      schemaVersion: 1,
      id: 'work-cornerstone',
      name: 'Cornerstone Work',
      state: 'ready',
      archiveState: 'active',
      blueprintRef,
      definitionSnapshot: {
        schemaVersion: 1,
        revision: 1,
        blueprintRef,
        promptTemplate: '',
        workflow: { stages: [] },
        blockSpecs: [],
        digest: 'definition-digest',
      },
      blocks: [],
      placements: [],
      prompt: '',
      cornerstones,
      runs: [],
      createdWith: { workSchemaVersion: 1, eventSchemaVersion: 1, rendererSetVersion: 1 },
      createdAt: '2026-07-20T10:00:00Z',
      updatedAt: '2026-07-20T10:00:00Z',
    },
  };
}

type MutationInput =
  | PinCornerstoneInput
  | RefreshCornerstoneInput
  | ValidateCornerstoneInput
  | FreezeCornerstoneInput
  | AcceptCornerstoneInput
  | RepairCornerstoneInput
  | RemoveCornerstoneInput
  | UndoCornerstoneInput;

class TestPort implements WorkControllerPort, CornerstoneControllerPort {
  private readonly fake: FakeWorkController;
  private readonly listeners = new Map<string, (event: WorkViewEvent) => void>();
  readonly calls: Array<{ action: string; input: MutationInput }> = [];
  readonly runCalls: Array<{ workId: string; requestId: string }> = [];
  readonly preferences = new Map<string, WorkUIPreference>();

  constructor(view: WorkView, options: FakeWorkControllerOptions = {}) {
    this.fake = new FakeWorkController(options);
    this.fake.seedWork(view.work, view.revision);
  }

  subscribe(workId: string, onEvent: (event: WorkViewEvent) => void): WorkPortSubscription {
    this.listeners.set(workId, onEvent);
    return { ready: Promise.resolve(), unsubscribe: () => { this.listeners.delete(workId); } };
  }

  fetchSnapshot(workId: string): Promise<WorkView> {
    return this.fake.getWork(workId);
  }

  async fetchRecoverySnapshot(workId: string, intent: ViewRecoveryIntent): Promise<WorkViewEvent> {
    const view = await this.fetchSnapshot(workId);
    return retryResync(view, intent.generation);
  }

  async readUIPreference(workId: string): Promise<WorkUIPreference | null> {
    return this.preferences.get(workId) ?? null;
  }

  async writeUIPreference(workId: string, preference: WorkUIPreference): Promise<void> {
    this.preferences.set(workId, preference);
  }

  async retryTask(_input: RetryTaskInput): Promise<Attempt> {
    throw new Error('unused');
  }

  async runWork(input: { workId: string; requestId: string }): Promise<WorkflowRun> {
    this.runCalls.push(structuredClone(input));
    return {
      id: `run-${input.requestId}`,
      workId: input.workId,
      requestId: input.requestId,
      definitionDigest: 'definition-digest',
      state: 'running',
      stages: [],
      startedAt: '2026-07-22T00:00:00Z',
    };
  }

  pinCornerstone = (input: PinCornerstoneInput) => this.record('pin', input, this.fake.pinCornerstone);
  refreshCornerstone = (input: RefreshCornerstoneInput) => this.record('refresh', input, this.fake.refreshCornerstone);
  validateCornerstone = (input: ValidateCornerstoneInput) => this.record('validate', input, this.fake.validateCornerstone);
  freezeCornerstone = (input: FreezeCornerstoneInput) => this.record('freeze', input, this.fake.freezeCornerstone);
  acceptCornerstone = (input: AcceptCornerstoneInput) => this.record('accept', input, this.fake.acceptCornerstone);
  repairCornerstone = (input: RepairCornerstoneInput) => this.record('repair', input, this.fake.repairCornerstone);
  removeCornerstone = (input: RemoveCornerstoneInput) => this.record('remove', input, this.fake.removeCornerstone);
  undoCornerstone = (input: UndoCornerstoneInput) => this.record('undo', input, this.fake.undoCornerstone);

  private record<T extends MutationInput>(
    action: string,
    input: T,
    invoke: (value: T) => Promise<CornerstoneMutationResult>,
  ): Promise<CornerstoneMutationResult> {
    this.calls.push({ action, input: structuredClone(input) });
    return invoke(input);
  }
}

async function testFixedOuterIdentity(): Promise<void> {
  reset();
  const view = makeView([makeCornerstone('cs-fixed')]);
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort(view, { latencyMs: 70 });
  const mounted = await mount(<WorkCard workID={view.work.id} port={port} />);

  const drawer = mounted.host.querySelector<HTMLElement>('[data-testid="cornerstone-drawer"]')!;
  ok(!!drawer.closest('[data-testid="work-outer-header"]'), 'Drawer 位于固定外层 header');
  ok(!drawer.closest('[data-testid="work-card"]'), 'Drawer 不在翻面子树');
  await click(drawer.querySelector('button'));
  const title = drawer.querySelector<HTMLInputElement>('[aria-label="基石标题"]')!;
  const content = drawer.querySelector<HTMLTextAreaElement>('[aria-label="基石内容"]')!;
  await change(title, '保留的标题草稿');
  await change(content, '保留的内容草稿');

  const item = drawer.querySelector<HTMLElement>('[data-testid="cornerstone-item-cs-fixed"]')!;
  await act(async () => {
    button(item, '校验').click();
    button(item, '校验').click();
    mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click();
    await Promise.resolve();
  });

  eq(mounted.host.querySelector('[data-testid="cornerstone-drawer"]'), drawer, '翻面保留同一 Drawer DOM 实例');
  ok(!!drawer.querySelector('[data-testid="cornerstone-drawer-body"]'), '翻面不关闭 Drawer');
  eq(title.value, '保留的标题草稿', '翻面保留标题草稿');
  eq(content.value, '保留的内容草稿', '翻面保留内容草稿');
  ok(!!item.querySelector('.cornerstone-item__pending'), '翻面期间保留 pending 状态');
  eq(port.calls.filter((call) => call.action === 'validate').length, 1, '重复点击只派发一次请求');
  await settle(90);
  eq(mounted.host.querySelector('[data-testid="work-card"]')?.getAttribute('data-active-face'), 'back', '翻面完成');

  await mounted.cleanup();
}

async function testTypedMutations(): Promise<void> {
  reset();
  const view = makeView([
    makeCornerstone('cs-live', { mode: 'live_ref', ref: { kind: 'url', url: 'https://example.invalid/source' } }),
    makeCornerstone('cs-accept', { mode: 'live_ref', status: 'stale', candidateDigest: 'candidate' }),
    makeCornerstone('cs-repair', { mode: 'live_ref', status: 'missing', ref: { kind: 'workspace_file', path: 'missing.txt' } }),
    makeCornerstone('cs-removed', { tombstone: true }),
  ]);
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort(view);
  const mounted = await mount(<WorkCard workID={view.work.id} port={port} />);
  await click(mounted.host.querySelector('[data-testid="cornerstone-drawer"] > button'));

  const item = (id: string) => mounted.host.querySelector<HTMLElement>(`[data-testid="cornerstone-item-${id}"]`)!;
  await click(button(item('cs-live'), '校验'));
  await click(button(item('cs-live'), '刷新'));
  await click(button(item('cs-live'), '冻结'));
  await click(button(item('cs-accept'), '接受新版本'));
  await change(item('cs-repair').querySelector('[aria-label="修复 cs-repair 的引用值"]'), 'repaired.txt');
  await click(button(item('cs-repair'), '修复引用'));
  await click(button(item('cs-accept'), '移除'));
  await click(button(item('cs-accept'), '撤销移除'));
  await click(button(item('cs-removed'), '撤销移除'));

  await change(mounted.host.querySelector('[aria-label="基石标题"]'), 'Pinned Title');
  await change(mounted.host.querySelector('[aria-label="基石内容"]'), 'Pinned Content');
  await click(button(mounted.host.querySelector('[data-testid="cornerstone-pin-form"]')!, 'Pin'));

  const expected = ['validate', 'refresh', 'freeze', 'accept', 'repair', 'remove', 'undo', 'undo', 'pin'];
  eq(port.calls.map((call) => call.action).join(','), expected.join(','), '全部 typed mutation 均通过 Controller port');
  for (const call of port.calls) {
    eq(call.input.workId, view.work.id, `${call.action} 携带 workId`);
    ok(call.input.requestId.startsWith('cornerstone-'), `${call.action} 携带独立 requestId`);
    ok(Number.isSafeInteger(call.input.expectedRevision), `${call.action} 携带 expectedRevision`);
  }
  const revisions = port.calls.map((call) => call.input.expectedRevision);
  ok(revisions.every((revision, index) => index === 0 || revision > revisions[index - 1]), '成功操作使用最新递增 revision');
  eq(useWorkStore.getState().works[view.work.id].revision, view.revision + expected.length, 'mutation WorkView 回到单一投影');

  await mounted.cleanup();
}

async function testConflictAndNetworkRetry(): Promise<void> {
  reset();
  const conflictOptions: FakeWorkControllerOptions = { revisionConflictOn: new Set(['repair']) };
  const view = makeView([
    makeCornerstone('cs-conflict', { status: 'missing', mode: 'live_ref', ref: { kind: 'workspace_file', path: 'old.txt' } }),
  ]);
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort(view, conflictOptions);
  const mounted = await mount(<WorkCard workID={view.work.id} port={port} />);
  await click(mounted.host.querySelector('[data-testid="cornerstone-drawer"] > button'));
  const item = mounted.host.querySelector<HTMLElement>('[data-testid="cornerstone-item-cs-conflict"]')!;
  const draft = item.querySelector<HTMLInputElement>('[aria-label="修复 cs-conflict 的引用值"]')!;
  await change(draft, 'draft-kept.txt');
  await click(button(item, '修复引用'));

  ok(item.textContent?.includes('版本冲突') ?? false, 'revision 冲突显式显示');
  eq(draft.value, 'draft-kept.txt', '冲突后保留修复草稿');
  ok(!!item.querySelector('.cornerstone-item__conflict'), '冲突展示最新 Cornerstone 状态');
  await click(button(item.querySelector('.cornerstone-item__error')!, '重试'));
  const repairCalls = port.calls.filter((call) => call.action === 'repair');
  eq(repairCalls.length, 2, '冲突后可重试');
  ok(repairCalls[0].input.requestId !== repairCalls[1].input.requestId, '冲突更新 revision 后生成新 requestId');
  eq(repairCalls[1].input.expectedRevision, view.revision, '冲突重试使用服务端最新 revision');
  await mounted.cleanup();

  reset();
  const network = new Set(['validate']);
  const networkView = makeView([makeCornerstone('cs-network')]);
  useWorkStore.getState().applySnapshot(networkView);
  const networkPort = new TestPort(networkView, { networkErrorOn: network });
  const networkMounted = await mount(<WorkCard workID={networkView.work.id} port={networkPort} />);
  await click(networkMounted.host.querySelector('[data-testid="cornerstone-drawer"] > button'));
  const networkItem = networkMounted.host.querySelector<HTMLElement>('[data-testid="cornerstone-item-cs-network"]')!;
  await click(button(networkItem, '校验'));
  ok(networkItem.textContent?.includes('网络请求失败') ?? false, '网络失败显式显示');
  network.delete('validate');
  await click(button(networkItem.querySelector('.cornerstone-item__error')!, '重试'));
  const validateCalls = networkPort.calls.filter((call) => call.action === 'validate');
  eq(validateCalls.length, 2, '网络失败可重试');
  eq(validateCalls[0].input.requestId, validateCalls[1].input.requestId, '网络重试复用 requestId，避免重复副作用');
  eq(validateCalls[0].input.expectedRevision, validateCalls[1].input.expectedRevision, '网络重试复用 expectedRevision');
  await networkMounted.cleanup();
}

async function testUnifiedAttentionAndRunGate(): Promise<void> {
  reset();
  const view = makeView([
    makeCornerstone('cs-required', { required: true, status: 'invalid' }),
    makeCornerstone('cs-optional', { required: false, status: 'missing' }),
    makeCornerstone('cs-removed-required', { required: true, status: 'missing', tombstone: true }),
  ]);
  useWorkStore.getState().applySnapshot(view);
  const attention = deriveCornerstoneAttention(view.work);
  eq(attention.items.length, 1, 'Attention 只包含未移除的 required 失效基石');

  const port = new TestPort(view);
  const card = await mount(<WorkCard workID={view.work.id} port={port} />);
  ok(!!card.host.querySelector('[data-testid="work-attention"]'), 'WorkCard 显示同一 required Attention');
  ok(!!card.host.querySelector('.cornerstone-drawer__attention-badge'), 'Drawer 入口显示同一 Attention');
  ok(!card.host.querySelector('[data-testid="work-cornerstones"]'), '正面不保留重复 CornerstoneSummary 入口');

  await click(button(card.host, '运行'));
  eq(port.runCalls.length, 0, 'required 失效阻止生产运行请求');
  ok(useCornerstoneUIStore.getState().byWork[view.work.id]?.open === true, '运行阻塞打开同一 Drawer');

  const active = structuredClone(view);
  active.revision = 2;
  active.work.cornerstones[0].status = 'active';
  await act(async () => {
    useWorkStore.getState().applySnapshot(active, 'attention-resolved');
    await Promise.resolve();
  });
  await click(button(card.host, '运行'));
  eq(port.runCalls.length, 1, 'Attention 解除后生产运行入口正常派发');

  const testDir = dirname(fileURLToPath(import.meta.url));
  const drawerSource = readFileSync(resolve(testDir, '../components/work/CornerstoneDrawer.tsx'), 'utf8');
  ok(!drawerSource.includes('console.'), 'Drawer 不记录内容、路径或 Secret 日志');

  await card.cleanup();
}

async function testRunRetryReusesRequestID(): Promise<void> {
  reset();
  const view = makeView([]);
  useWorkStore.getState().applySnapshot(view);
  const requestIds: string[] = [];
  let attempts = 0;
  const run = await mount(<WorkRunEntry workId={view.work.id} onRun={({ requestId }) => {
    requestIds.push(requestId);
    attempts++;
    if (attempts === 1) throw new Error('network');
  }} />);
  await click(button(run.host, '运行'));
  await click(button(run.host, '运行'));
  eq(requestIds.length, 2, '失败后运行请求可重试');
  eq(requestIds[1], requestIds[0], '失败重试复用原 requestID');
  await click(button(run.host, '运行'));
  ok(requestIds[2] !== requestIds[1], '成功后的新运行意图使用新 requestID');
  await run.cleanup();
}

async function testProductionWorkCardAssembly(): Promise<void> {
  reset();
  const view = makeView([]);
  const watched: string[] = [];
  const unwatched: string[] = [];
  const runInputs: Array<{ workId: string; requestId: string }> = [];
  const eventListeners = new Map<string, (payload: unknown) => void>();
  (dom.window as unknown as { runtime: unknown }).runtime = {
    EventsOn: (name: string, callback: (payload: unknown) => void) => {
      eventListeners.set(name, callback);
      return () => { eventListeners.delete(name); };
    },
  };
  (dom.window as unknown as { go: unknown }).go = {
    main: {
      App: {
        GetWork: async (_tabID: string, workId: string) => {
          if (workId !== view.work.id) throw new Error('wrong Work');
          return structuredClone(view);
        },
        WatchWork: async (tabID: string, workId: string, id: string) => {
          watched.push(`${tabID}:${workId}:${id}`);
        },
        UnwatchWork: async (id: string) => { unwatched.push(id); },
        RunWork: async (_tabID: string, workId: string, requestId: string) => {
          runInputs.push({ workId, requestId });
          return {
            id: `run-${requestId}`, workId, requestId, definitionDigest: 'definition-digest',
            state: 'running', stages: [], startedAt: '2026-07-22T00:00:00Z',
          } satisfies WorkflowRun;
        },
        RetryWorkTask: async () => { throw new Error('unused'); },
      },
    },
  };

  const card = await mount(<WorkCard workID={view.work.id} tabID="tab-production" />);
  await settle();
  ok(watched.some((item) => item.startsWith(`tab-production:${view.work.id}:work-view-`)), 'WorkCard 生产树启动 Wails WorkView 订阅');
  ok(!!card.host.querySelector('[data-testid="cornerstone-drawer"]'), '生产 WorkCard 树挂载固定 Drawer');
  ok(!!card.host.querySelector('[data-testid="work-run-entry"]'), '生产 WorkCard 树挂载运行入口');
  await click(button(card.host, '运行'));
  eq(runInputs.length, 1, '生产 WorkCard 通过 Wails RunWork');
  eq(runInputs[0].workId, view.work.id, '生产 RunWork 携带显式 workId');
  ok(runInputs[0].requestId.startsWith('work-run-'), '生产 RunWork 携带稳定 requestID');

  const testDir = dirname(fileURLToPath(import.meta.url));
  const cardSource = readFileSync(resolve(testDir, '../components/work/WorkCard.tsx'), 'utf8');
  const adapterSource = readFileSync(resolve(testDir, '../work/wailsAdapter.ts'), 'utf8');
  const drawerSource = readFileSync(resolve(testDir, '../components/work/CornerstoneDrawer.tsx'), 'utf8');
  ok(cardSource.includes('createWailsWorkControllerPort(tabID)'), 'WorkCard 实际消费生产 Wails port');
  ok(!adapterSource.includes('fakeController') && !adapterSource.includes('useWorkStore'), '生产 adapter 无 fake 或业务 Store 直写');
  ok(!drawerSource.includes('useWorkStore') && !drawerSource.includes('applySnapshot'), 'Drawer 只派发 intent，不直写业务 Store');

  await card.cleanup();
  await settle();
  eq(unwatched.length, 1, 'WorkCard 卸载释放 Wails WorkView 订阅');
  delete (dom.window as unknown as { go?: unknown }).go;
  delete (dom.window as unknown as { runtime?: unknown }).runtime;
}

async function testWailsConflictLoadsLatestProjection(): Promise<void> {
  const latest = makeView([makeCornerstone('cs-wails', { status: 'active' })]);
  latest.revision = 9;
  let getWorkCalls = 0;
  (dom.window as unknown as { go: unknown }).go = {
    main: {
      App: {
        GetWork: async () => {
          getWorkCalls++;
          return structuredClone(latest);
        },
        RepairCornerstone: async () => {
          throw new Error('work event conflict: expected revision 3, current revision 7');
        },
      },
    },
  };
  const adapter = createWailsCornerstoneAdapter('tab-wails');
  ok(!!adapter?.repairCornerstone, 'Wails production adapter 已装配 Repair');
  const result = await adapter!.repairCornerstone!({
    workId: latest.work.id,
    cornerstoneId: 'cs-wails',
    expectedRevision: 3,
    requestId: 'repair-wails-conflict',
  });
  ok(!result.ok && result.error.kind === 'revision_conflict', 'Wails 冲突转换为 typed revision_conflict');
  if (!result.ok && result.error.kind === 'revision_conflict') {
    eq(result.error.actualRevision, 9, 'Wails 冲突 actualRevision 来自最新 WorkView');
    eq(result.error.latestView?.revision, 9, 'Wails 冲突携带 latestView');
    eq(result.error.latestSnapshot?.id, 'cs-wails', 'Wails 冲突携带 latest Cornerstone');
  }
  eq(getWorkCalls, 1, 'Wails 冲突只补读一次最新投影');
  delete (dom.window as unknown as { go?: unknown }).go;
}

async function testWailsSnapshotRepairContentRoundTrip(): Promise<void> {
  reset();
  const replacement = 'production-adapter-content';
  const missing = makeCornerstone('cs-wails-blob', {
    mode: 'snapshot',
    ref: { kind: 'inline', blobDigest: contentDigest(replacement) },
    digest: contentDigest(replacement),
    status: 'invalid',
    resolveErrorKind: 'invalid',
  });
  let persisted = makeView([missing], 4);
  let received: Record<string, unknown> | null = null;
  (dom.window as unknown as { go: unknown }).go = {
    main: { App: {
      RepairCornerstone: async (_tabID: string, workID: string, input: Record<string, unknown>) => {
        received = structuredClone(input);
        if (workID !== persisted.work.id || input.content !== replacement || input.ref !== undefined) {
          throw new Error('snapshot repair rejected');
        }
        const active = { ...missing, content: replacement, status: 'active' as const, error: undefined, resolveErrorKind: undefined };
        persisted = { ...persisted, revision: 5, work: { ...persisted.work, cornerstones: [active] } };
        return {
          cornerstone: active,
          workView: structuredClone(persisted),
          repaired: true,
          duplicate: false,
          revision: persisted.revision,
          assessment: { state: 'ready', blocking: false, degraded: false },
        };
      },
      GetWork: async () => structuredClone(persisted),
    } },
  };

  const port = createWailsWorkControllerPort('tab-content-roundtrip')!;
  const result = await port.repairCornerstone!({
    workId: persisted.work.id,
    cornerstoneId: missing.id,
    content: replacement,
    expectedRevision: persisted.revision,
    requestId: 'repair-content-roundtrip',
  });
  ok(result.ok, 'production Wails adapter 接受 snapshot replacement content');
  eq(received?.content, replacement, 'production Wails input 贯通 content');
  ok(received?.ref === undefined, 'production Wails snapshot repair 不携带 ref');

  // Recreate the production port to model a Desktop refresh/restart. Its
  // authoritative GetWork projection must still expose the repaired state.
  const restarted = createWailsWorkControllerPort('tab-content-roundtrip-restart')!;
  const refreshed = await restarted.fetchSnapshot(persisted.work.id);
  eq(refreshed.revision, 5, '重建 production adapter 后读取持久 revision');
  eq(refreshed.work.cornerstones[0].status, 'active', '重建/刷新后 snapshot 仍为 active');
  delete (dom.window as unknown as { go?: unknown }).go;
}

function workDelta(workId: string, eventId: string, revision: number, baseRevision: number, name: string): WorkViewEvent {
  return {
    schemaVersion: 1,
    type: 'delta',
    workID: workId,
    eventID: eventId,
    revision,
    baseRevision,
    requestID: `request-${eventId}`,
    object: { kind: 'work', id: workId },
    payload: { name },
    createdAt: '2026-07-22T00:00:00Z',
  };
}

function retryResync(view: WorkView, generation: number): WorkViewEvent {
  return {
    schemaVersion: 1,
    type: 'snapshot',
    workID: view.work.id,
    eventID: `wv-resync-${view.work.id}-rev-${view.revision}-retry-${generation}`,
    revision: view.revision,
    baseRevision: 0,
    requestID: 'retry-recovery',
    object: { kind: 'work', id: view.work.id },
    resync: { reason: 'retry', authoritative: true, generation },
    payload: structuredClone(view),
    createdAt: '2026-07-22T00:00:00Z',
  };
}

async function testWailsWatchHandshakeAndRecovery(): Promise<void> {
  reset();
  const view = makeView([]);
  useWorkStore.getState().applySnapshot(view);
  const eventListeners = new Map<string, (payload: unknown) => void>();
  let removedListeners = 0;
  (dom.window as unknown as { runtime: unknown }).runtime = {
    EventsOn: (name: string, callback: (payload: unknown) => void) => {
      eventListeners.set(name, callback);
      return () => {
        if (eventListeners.delete(name)) removedListeners++;
      };
    },
  };

  let watchCalls = 0;
  let getWorkCalls = 0;
  let recoveryCalls = 0;
  const retryWatchReady = deferred<void>();
  const unwatched: string[] = [];
  (dom.window as unknown as { go: unknown }).go = {
    main: { App: {
      GetWork: async () => {
        getWorkCalls++;
        return structuredClone(view);
      },
      RecoverWorkView: async (_tab: string, _work: string, intent: ViewRecoveryIntent) => {
        recoveryCalls++;
        return retryResync(view, intent.generation);
      },
      WatchWork: async () => {
        watchCalls++;
        if (watchCalls === 1) throw new Error('watch transport unavailable');
        await retryWatchReady.promise;
      },
      UnwatchWork: async (id: string) => { unwatched.push(id); },
      RunWork: async () => { throw new Error('unused'); },
      RetryWorkTask: async () => { throw new Error('unused'); },
    } },
  };

  // A valid projection can already be present while installing its live watch
  // fails. The failure must remain visible and retry must create one generation.
  const card = await mount(<WorkCard workID={view.work.id} tabID="tab-watch-retry" />);
  await settle();
  ok(card.host.querySelector('[data-testid="work-error-banner"]')?.textContent?.includes('watch transport unavailable'), 'Watch reject 在已有 snapshot 上显式显示');
  await act(async () => {
    button(card.host, '重试同步').click();
    button(card.host, '重试同步').click();
    await Promise.resolve();
  });
  eq(watchCalls, 2, '连续 retry 只创建一个新 Watch generation');
  eq(eventListeners.size, 1, '连续 retry 不重复 runtime listener');
  eq(getWorkCalls, 0, 'Watch 握手完成前不读取 snapshot');
  retryWatchReady.resolve();
  await settle();
  eq(getWorkCalls, 0, '显式 retry 不退化为普通 GetWork snapshot');
  eq(recoveryCalls, 1, 'Watch 握手后只请求一次 typed 权威 recovery');
  ok(!card.host.querySelector('[data-testid="work-error-banner"]')?.textContent?.includes('watch transport unavailable'), '成功重订阅清除 Watch 错误');
  await card.cleanup();
  ok(unwatched.length >= 2, '失败 generation 与卸载 generation 均执行 Unwatch');
  ok(removedListeners >= 2, '失败和卸载都清理 runtime listener');

  // Events arriving after the Watch handshake but before GetWork resolves are
  // buffered, then replayed over the authoritative snapshot by revision.
  reset();
  eventListeners.clear();
  const snapshotWait = deferred<WorkView>();
  let windowSubscription = '';
  (dom.window as unknown as { go: unknown }).go = {
    main: { App: {
      GetWork: async () => snapshotWait.promise,
      WatchWork: async (_tab: string, _work: string, id: string) => { windowSubscription = id; },
      UnwatchWork: async () => undefined,
      RunWork: async () => { throw new Error('unused'); },
      RetryWorkTask: async () => { throw new Error('unused'); },
    } },
  };
  const windowPort = createWailsWorkControllerPort('tab-window')!;
  const windowAdapter = new WorkControllerAdapter(windowPort);
  windowAdapter.subscribe(view.work.id);
  await Promise.resolve();
  await Promise.resolve();
  eventListeners.get(`work:view:${windowSubscription}`)?.(workDelta(view.work.id, 'window-event-2', 2, 1, 'event-wins'));
  snapshotWait.resolve(makeView([], 1));
  await settle();
  eq(useWorkStore.getState().revisions[view.work.id], 2, '握手/快照窗口事件推进到最高 revision');
  eq(useWorkStore.getState().works[view.work.id]?.work.name, 'event-wins', '窗口事件不被旧 snapshot 覆盖');
  windowAdapter.dispose();

  // Real Wails runtime -> adapter -> Zustand chain: overflow GetWork fails,
  // then an external blob deletion changes assessment at the same persisted
  // revision. Explicit retry must install a new Watch and apply a fresh typed
  // authoritative event without losing local UI state.
  reset();
  eventListeners.clear();
  const readyView = { ...makeView([], 77), assessment: { state: 'ready' as const, blocking: false, degraded: false, issues: [] } };
  const blobMissingView: WorkView = {
    ...structuredClone(readyView),
    assessment: {
      state: 'blocked', blocking: true, degraded: false,
      issues: [{ cornerstoneId: 'cs-required', title: 'required blob', problem: 'blob_missing', blocking: true }],
    },
    runBlock: {
      blocked: true,
      items: [{ code: 'blob_missing', cornerstoneId: 'cs-required', status: 'active' }],
    },
  };
  let overflowSubscription = '';
  const overflowSubscriptions: string[] = [];
  const recoveryEvents: WorkViewEvent[] = [];
  let recoveryMode: 'blocked' | 'delayed' | 'failed' = 'blocked';
  let delayedRecovery = deferred<WorkViewEvent>();
  (dom.window as unknown as { go: unknown }).go = {
    main: { App: {
      GetWork: async () => structuredClone(readyView),
      RecoverWorkView: async (_tab: string, _work: string, intent: ViewRecoveryIntent) => {
        if (recoveryMode === 'failed') throw new Error('retry GetWork still unavailable');
        if (recoveryMode === 'delayed') return delayedRecovery.promise;
        const event = retryResync(blobMissingView, intent.generation);
        recoveryEvents.push(event);
        return event;
      },
      WatchWork: async (_tab: string, _work: string, id: string) => {
        overflowSubscription = id;
        overflowSubscriptions.push(id);
      },
      UnwatchWork: async () => undefined,
      RunWork: async () => { throw new Error('unused'); },
      RetryWorkTask: async () => { throw new Error('unused'); },
    } },
  };
  const overflowPort = createWailsWorkControllerPort('tab-overflow-store')!;
  const overflowAdapter = new WorkControllerAdapter(overflowPort);
  overflowAdapter.subscribe(readyView.work.id);
  await settle();
  eq(useWorkStore.getState().works[readyView.work.id]?.assessment.state, 'ready', '普通初始 snapshot 落入 ready 投影');
  eq(overflowAdapter.getStatus(readyView.work.id).stream.kind, 'online', '初始 Watch/GetWork 握手完成后 online');
  useWorkUIStore.getState().setDraft(readyView.work.id, 'front', 'keep local draft');
  eventListeners.get(`work:view:${overflowSubscription}`)?.({
    schemaVersion: 1,
    type: 'attention',
    workID: readyView.work.id,
    eventID: 'wv-recover-failed-work-cornerstone-1',
    revision: 0,
    baseRevision: 0,
    requestID: 'overflow-recovery-failed',
    object: { kind: 'work', id: readyView.work.id },
    payload: { overflow: true, recovery: 'failed', retryable: true },
    createdAt: '2026-07-22T00:00:01Z',
  } satisfies WorkViewEvent);
  await settle();
  eq(overflowAdapter.getStatus(readyView.work.id).stream.kind, 'offline', 'overflow GetWork 失败 attention 保持 offline');
  eq(useWorkStore.getState().works[readyView.work.id]?.assessment.state, 'ready', '失败 attention 不发伪 recovery');

  overflowAdapter.retrySubscription(readyView.work.id);
  overflowAdapter.retrySubscription(readyView.work.id);
  await settle();
  eq(overflowSubscriptions.length, 2, '双 retry 只创建一个新 Watch generation');
  ok(overflowSubscriptions[1] !== overflowSubscriptions[0], '显式 retry 使用新的 Wails subscriptionID');
  eq(recoveryEvents.length, 1, '新 Watch 握手后只执行一次 typed recovery GetWork');
  ok(recoveryEvents[0].eventID !== `fetch:${readyView.work.id}:77`, 'retry EventID 不复用固定 work+revision fetch ID');
  eq(recoveryEvents[0].resync?.reason, 'retry', 'retry recovery 携带 typed reason');
  eq(useWorkStore.getState().revisions[readyView.work.id], 77, 'retry resync 保持 persisted revision');
  eq(useWorkStore.getState().works[readyView.work.id]?.assessment.blocking, true, '同 revision blocked assessment 经 retry 落入 store');
  eq(useWorkStore.getState().works[readyView.work.id]?.runBlock?.items?.[0]?.code, 'blob_missing', 'typed runBlock 经 retry 落入 store');
  eq(overflowAdapter.getStatus(readyView.work.id).stream.kind, 'online', 'typed snapshot 成功应用后才恢复 online');
  eq(overflowAdapter.getStatus(readyView.work.id).snapshotError, null, '完整 handshake 成功后清除 snapshotError');
  eq(useWorkUIStore.getState().cardByWork[readyView.work.id]?.faces.front.draft, 'keep local draft', 'retry resync 保留 UI draft');

  // Cancel a delayed generation, complete a newer retry, then release the old
  // response. The old generation must not replace or pollute the new state.
  recoveryMode = 'delayed';
  delayedRecovery = deferred<WorkViewEvent>();
  overflowAdapter.retrySubscription(readyView.work.id);
  await Promise.resolve();
  await Promise.resolve();
  const lateGeneration = overflowSubscriptions.length;
  overflowAdapter.unsubscribe(readyView.work.id);
  recoveryMode = 'blocked';
  overflowAdapter.retrySubscription(readyView.work.id);
  await settle();
  eq(overflowAdapter.getStatus(readyView.work.id).stream.kind, 'online', '较新 retry generation 完成后 online');
  delayedRecovery.resolve(retryResync(readyView, lateGeneration));
  await settle();
  eq(useWorkStore.getState().works[readyView.work.id]?.assessment.blocking, true, '迟到旧 generation 不覆盖较新 blocked 投影');
  eq(overflowAdapter.getStatus(readyView.work.id).snapshotError, null, '迟到旧 generation 不污染较新健康状态');

  recoveryMode = 'failed';
  overflowAdapter.retrySubscription(readyView.work.id);
  await settle();
  eq(overflowAdapter.getStatus(readyView.work.id).stream.kind, 'offline', '再次 retry GetWork 失败保持 offline');
  ok(overflowAdapter.getStatus(readyView.work.id).snapshotError?.includes('still unavailable'), '再次失败保留可重试错误');
  eq(useWorkStore.getState().works[readyView.work.id]?.assessment.blocking, true, '再次失败不制造伪成功投影');
  eq(useWorkUIStore.getState().cardByWork[readyView.work.id]?.faces.front.draft, 'keep local draft', '再次失败仍保留 UI draft');
  overflowAdapter.dispose();

  // A snapshot failure after a successful Watch is recovered through a whole
  // new Watch + typed snapshot handshake. Events on the new Watch are buffered
  // until that authoritative snapshot has applied.
  reset();
  eventListeners.clear();
  let retryFetches = 0;
  let retryWatches = 0;
  let retrySubscription = '';
  let retryIntent: ViewRecoveryIntent | undefined;
  const retryRecovery = deferred<WorkViewEvent>();
  (dom.window as unknown as { go: unknown }).go = {
    main: { App: {
      GetWork: async () => {
        retryFetches++;
        throw new Error('snapshot temporarily unavailable');
      },
      RecoverWorkView: async (_tab: string, _work: string, intent: ViewRecoveryIntent) => {
        retryIntent = intent;
        return retryRecovery.promise;
      },
      WatchWork: async (_tab: string, _work: string, id: string) => {
        retryWatches++;
        retrySubscription = id;
      },
      UnwatchWork: async () => undefined,
      RunWork: async () => { throw new Error('unused'); },
      RetryWorkTask: async () => { throw new Error('unused'); },
    } },
  };
  const retryPort = createWailsWorkControllerPort('tab-snapshot-retry')!;
  const retryAdapter = new WorkControllerAdapter(retryPort);
  retryAdapter.subscribe(view.work.id);
  await settle();
  ok(retryAdapter.getStatus(view.work.id).snapshotError?.includes('temporarily unavailable'), 'Watch 成功后的 snapshot 失败可观察');
  const failedSubscription = retrySubscription;
  retryAdapter.retrySubscription(view.work.id);
  await settle();
  eq(retryWatches, 2, 'snapshot retry 安装新的 Watch');
  ok(retrySubscription !== failedSubscription, 'snapshot retry 使用新的 subscriptionID');
  ok(!!retryIntent, 'snapshot retry 在新 Watch ready 后携带 typed recovery intent');
  eq(retryAdapter.getStatus(view.work.id).stream.kind, 'offline', '新 Watch ready 但 typed snapshot 未完成时仍 offline');
  ok(retryAdapter.getStatus(view.work.id).snapshotError?.includes('temporarily unavailable'), '进行中的 retry 不提前清除旧错误');
  eventListeners.get(`work:view:${retrySubscription}`)?.(workDelta(view.work.id, 'buffered-after-fetch-failure', 2, 1, 'retry-event'));
  retryRecovery.resolve(retryResync(makeView([], 1), retryIntent!.generation));
  await settle();
  eq(useWorkStore.getState().revisions[view.work.id], 2, 'snapshot retry 后回放保留的窗口事件');
  eq(retryAdapter.getStatus(view.work.id).snapshotError, null, 'snapshot retry 成功后清除错误');
  retryAdapter.dispose();

  // Unmount before WatchWork resolves clears the listener immediately; a late
  // resolve performs a second Unwatch and late old events cannot cross work.
  reset();
  eventListeners.clear();
  const watchWait = deferred<void>();
  const lateUnwatched: string[] = [];
  let oldEvent: ((payload: unknown) => void) | undefined;
  (dom.window as unknown as { runtime: unknown }).runtime = {
    EventsOn: (name: string, callback: (payload: unknown) => void) => {
      oldEvent = callback;
      eventListeners.set(name, callback);
      return () => { eventListeners.delete(name); };
    },
  };
  (dom.window as unknown as { go: unknown }).go = {
    main: { App: {
      GetWork: async () => structuredClone(view),
      WatchWork: async () => watchWait.promise,
      UnwatchWork: async (id: string) => { lateUnwatched.push(id); },
      RunWork: async () => { throw new Error('unused'); },
      RetryWorkTask: async () => { throw new Error('unused'); },
    } },
  };
  const latePort = createWailsWorkControllerPort('tab-late')!;
  const lateAdapter = new WorkControllerAdapter(latePort);
  lateAdapter.subscribe(view.work.id);
  lateAdapter.unsubscribe(view.work.id);
  eq(eventListeners.size, 0, '卸载早于 Watch resolve 时立即清 listener');
  watchWait.resolve();
  await settle();
  ok(lateUnwatched.length >= 2, '迟到 Watch resolve 后再次 Unwatch');
  oldEvent?.(workDelta(view.work.id, 'late-old-event', 9, 8, 'must-not-apply'));
  eq(useWorkStore.getState().works[view.work.id], undefined, '旧 generation 的迟到事件不写入 projection');
  lateAdapter.dispose();

  delete (dom.window as unknown as { go?: unknown }).go;
  delete (dom.window as unknown as { runtime?: unknown }).runtime;
}

async function testWatchHealthIsolation(): Promise<void> {
  reset();
  const view = makeView([]);
  const listeners = new Map<string, (payload: unknown) => void>();
  (dom.window as unknown as { runtime: unknown }).runtime = {
    EventsOn: (name: string, callback: (payload: unknown) => void) => {
      listeners.set(name, callback);
      return () => { listeners.delete(name); };
    },
  };
  (dom.window as unknown as { go: unknown }).go = {
    main: { App: {
      GetWork: async () => structuredClone(view),
      WatchWork: async () => { throw new Error('isolated watch offline'); },
      UnwatchWork: async () => undefined,
      RunWork: async () => { throw new Error('unused'); },
      RetryWorkTask: async () => { throw new Error('unused'); },
    } },
  };

  const port = createWailsWorkControllerPort('tab-health-isolation')!;
  const adapter = new WorkControllerAdapter(port);
  adapter.subscribe(view.work.id);
  await settle();
  let stream = adapter.getStatus(view.work.id).stream;
  ok(stream.kind === 'offline' && stream.message === 'isolated watch offline', 'Watch reject 写入独立 typed offline health');
  await adapter.recoverSnapshot(view.work.id);
  stream = adapter.getStatus(view.work.id).stream;
  ok(stream.kind === 'offline' && stream.message === 'isolated watch offline', '成功 snapshot 不清除 Watch offline');
  const changed = makeView([makeCornerstone('cs-health')], 2);
  await adapter.applyMutationResult(view.work.id, {
    ok: true,
    cornerstone: changed.work.cornerstones[0],
    workView: changed,
    revision: changed.revision,
  });
  stream = adapter.getStatus(view.work.id).stream;
  ok(stream.kind === 'offline' && stream.message === 'isolated watch offline', '成功 mutation 不清除 Watch offline');
  adapter.dispose();

  // With no cached projection, the unknown branch must expose the concrete
  // watch failure and a re-subscribe action rather than only snapshot errors.
  reset();
  const unknown = await mount(<WorkCard workID={view.work.id} tabID="tab-health-isolation" />);
  await settle();
  ok(unknown.host.querySelector('[data-testid="work-card-unknown"]')?.textContent?.includes('事件订阅错误：isolated watch offline'), '无缓存 Work 显示具体 Watch 错误');
  eq(button(unknown.host, '重试订阅').textContent, '重试订阅', '无缓存 Work 提供安全重订阅入口');
  await unknown.cleanup();

  // A ready result from an old generation cannot clear the current offline
  // state. Repeated retry opens one listener and only current ready heals it.
  reset();
  listeners.clear();
  const oldReady = deferred<void>();
  const currentReady = deferred<void>();
  let watchCalls = 0;
  (dom.window as unknown as { go: unknown }).go = {
    main: { App: {
      GetWork: async () => structuredClone(view),
      RecoverWorkView: async (_tab: string, _work: string, intent: ViewRecoveryIntent) => retryResync(view, intent.generation),
      WatchWork: async () => {
        watchCalls++;
        if (watchCalls === 1) return oldReady.promise;
        if (watchCalls === 2) throw new Error('current generation offline');
        return currentReady.promise;
      },
      UnwatchWork: async () => undefined,
      RunWork: async () => { throw new Error('unused'); },
      RetryWorkTask: async () => { throw new Error('unused'); },
    } },
  };
  const generationPort = createWailsWorkControllerPort('tab-health-generation')!;
  const generationAdapter = new WorkControllerAdapter(generationPort);
  generationAdapter.subscribe(view.work.id);
  generationAdapter.unsubscribe(view.work.id);
  generationAdapter.subscribe(view.work.id);
  await settle();
  stream = generationAdapter.getStatus(view.work.id).stream;
  ok(stream.kind === 'offline' && stream.message === 'current generation offline', '当前 generation reject 保持 offline');
  oldReady.resolve();
  await settle();
  stream = generationAdapter.getStatus(view.work.id).stream;
  ok(stream.kind === 'offline' && stream.message === 'current generation offline', '旧 generation ready 不清当前错误');
  generationAdapter.retrySubscription(view.work.id);
  generationAdapter.retrySubscription(view.work.id);
  await Promise.resolve();
  eq(watchCalls, 3, '重复 retry 只创建一个当前 generation');
  eq(listeners.size, 1, '重复 retry 只保留一个 listener');
  eq(generationAdapter.getStatus(view.work.id).stream.kind, 'offline', '当前 ready 前仍显式 offline');
  currentReady.resolve();
  await settle();
  eq(generationAdapter.getStatus(view.work.id).stream.kind, 'online', '只有当前 Watch ready 清除 offline');
  generationAdapter.dispose();
  eq(listeners.size, 0, '成功 generation 卸载后无 listener 泄漏');

  delete (dom.window as unknown as { go?: unknown }).go;
  delete (dom.window as unknown as { runtime?: unknown }).runtime;
}

async function testSnapshotBlobRepairContentPath(): Promise<void> {
  reset();
  const replacement = 'correct-replacement-content\r\nwith-normalized-lines';
  const view = makeView([
    makeCornerstone('cs-blob-missing', {
      mode: 'snapshot',
      ref: { kind: 'inline', blobDigest: contentDigest(replacement) },
      digest: contentDigest(replacement),
      status: 'invalid',
      error: 'blob missing',
      resolveErrorKind: 'invalid',
    }),
  ]);
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort(view);
  const mounted = await mount(<WorkCard workID={view.work.id} port={port} />);
  await click(mounted.host.querySelector('[data-testid="cornerstone-drawer"] > button'));

  const item = mounted.host.querySelector<HTMLElement>('[data-testid="cornerstone-item-cs-blob-missing"]')!;
  // Snapshot blob repair shows content textarea, not ref editing
  ok(!!item.querySelector('textarea[aria-label="修复 cs-blob-missing 的快照内容"]'), 'snapshot blob 修复显示 content textarea');
  ok(!item.querySelector('select[aria-label*="引用类型"]'), 'snapshot blob 修复不显示 ref 编辑');
  ok(!!item.querySelector('.cornerstone-item__repair-meta'), 'snapshot blob 修复显示 digest/provenance 元数据');
  ok(!!item.querySelector('.cornerstone-item__repair-risk'), 'snapshot blob 修复显示风险提示');

  // Repair button disabled with empty content
  const repairBtn = button(item, '修复快照');
  ok(repairBtn.disabled, '空内容时修复快照按钮禁用');

  // Wrong content, secret-like input, and oversized payload all fail closed
  // before crossing the production port. Errors never echo the raw input.
  const textarea = item.querySelector<HTMLTextAreaElement>('textarea[aria-label="修复 cs-blob-missing 的快照内容"]')!;
  await change(textarea, 'wrong-content');
  ok(!button(item, '修复快照').disabled, '内容填写后修复按钮可用');
  await click(button(item, '修复快照'));
  eq(port.calls.filter((call) => call.action === 'repair').length, 0, '错误 digest 不调用 repair port');
  ok(item.textContent?.includes('digest 与已接受快照不匹配') ?? false, '错误 digest 显式且不回显正文');

  const secret = 'api_key=sk-1234567890abcdef';
  await change(textarea, secret);
  await click(button(item, '修复快照'));
  eq(port.calls.filter((call) => call.action === 'repair').length, 0, 'Secret-like 内容不调用 repair port');
  ok(item.textContent?.includes('疑似包含敏感凭据') ?? false, 'Secret-like 内容显式拒绝');
  ok(!item.querySelector('.cornerstone-item__error')?.textContent?.includes(secret), '错误提示不回显 Secret');

  await change(textarea, 'token: budget unit');
  await click(button(item, '修复快照'));
  ok(item.textContent?.includes('digest 与已接受快照不匹配') ?? false, '普通 token budget 文本不被误判为 Secret');

  await change(textarea, 'x'.repeat(8 * 1024 * 1024 + 1));
  await click(button(item, '修复快照'));
  eq(port.calls.filter((call) => call.action === 'repair').length, 0, '过大内容不调用 repair port');
  ok(item.textContent?.includes('超过 8 MiB') ?? false, '过大内容显式拒绝');

  await change(textarea, replacement);

  // Click repair — verify content is sent, ref is NOT sent
  await click(button(item, '修复快照'));
  const repairCalls = port.calls.filter((call) => call.action === 'repair');
  eq(repairCalls.length, 1, 'repair 调用一次');
  const call = repairCalls[0].input as RepairCornerstoneInput;
  ok(call.content !== undefined && call.content !== null, 'snapshot repair 传 content');
  eq(call.content, 'correct-replacement-content\nwith-normalized-lines', 'content 规范化后真实进入 repair input');
  ok(call.ref === undefined || call.ref === null, 'snapshot repair 不传 ref');

  // Verify the cornerstone became active
  const updatedView = useWorkStore.getState().works[view.work.id]!;
  const updated = updatedView.work.cornerstones.find((cs) => cs.id === 'cs-blob-missing')!;
  eq(updated.status, 'active', '修复后 cornerstone 变为 active');
  eq(updated.content, 'correct-replacement-content\nwith-normalized-lines', 'content 已更新');
  eq(useCornerstoneUIStore.getState().byWork[view.work.id]?.byId['cs-blob-missing']?.draftContent, null, '成功后才清理 repair 草稿');

  await mounted.cleanup();
}

async function testSnapshotRepairDraftSurvivesFlip(): Promise<void> {
  reset();
  const view = makeView([
    makeCornerstone('cs-blob-flip', {
      mode: 'snapshot',
      ref: { kind: 'inline', blobDigest: 'blob-sha256-fliptest' },
      digest: 'digest-flip',
      status: 'invalid',
      resolveErrorKind: 'invalid',
    }),
  ]);
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort(view);
  const mounted = await mount(<WorkCard workID={view.work.id} port={port} />);
  await click(mounted.host.querySelector('[data-testid="cornerstone-drawer"] > button'));

  const textarea = mounted.host.querySelector<HTMLTextAreaElement>('textarea[aria-label="修复 cs-blob-flip 的快照内容"]')!;
  await change(textarea, 'draft-across-flip');

  // Flip to back
  await act(async () => {
    mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click();
    await Promise.resolve();
  });
  await settle();

  // Draft persists after flip
  const afterFlip = mounted.host.querySelector<HTMLTextAreaElement>('textarea[aria-label="修复 cs-blob-flip 的快照内容"]')!;
  eq(afterFlip.value, 'draft-across-flip', '翻面后草稿保持不变');

  // Flip back to front
  await act(async () => {
    mounted.host.querySelector<HTMLButtonElement>('[data-testid="work-flip-button"]')!.click();
    await Promise.resolve();
  });
  await settle();

  const afterFlipBack = mounted.host.querySelector<HTMLTextAreaElement>('textarea[aria-label="修复 cs-blob-flip 的快照内容"]')!;
  eq(afterFlipBack.value, 'draft-across-flip', '再次翻回正面后草稿保持');

  await mounted.cleanup();
}

async function testSnapshotRepairConflictPreservesDraft(): Promise<void> {
  reset();
  const replacement = 'my-content';
  const view = makeView([
    makeCornerstone('cs-blob-conflict', {
      mode: 'snapshot',
      ref: { kind: 'inline', blobDigest: 'blob-conflict' },
      digest: contentDigest(replacement),
      status: 'invalid',
      resolveErrorKind: 'invalid',
    }),
  ]);
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort(view, { revisionConflictOn: new Set(['repair']) });
  const mounted = await mount(<WorkCard workID={view.work.id} port={port} />);
  await click(mounted.host.querySelector('[data-testid="cornerstone-drawer"] > button'));

  const item = mounted.host.querySelector<HTMLElement>('[data-testid="cornerstone-item-cs-blob-conflict"]')!;
  await change(item.querySelector<HTMLTextAreaElement>('textarea')!, replacement);
  await click(button(item, '修复快照'));

  ok(item.textContent?.includes('版本冲突') ?? false, 'snapshot repair 冲突显式显示');
  // Draft should survive conflict
  eq((item.querySelector<HTMLTextAreaElement>('textarea')!).value, 'my-content', '冲突后草稿保留');
  ok(!!item.querySelector('.cornerstone-item__conflict'), '冲突展示最新状态');

  // Retry generates new requestID
  await click(button(item.querySelector('.cornerstone-item__error')!, '重试'));
  const repairCalls = port.calls.filter((call) => call.action === 'repair');
  eq(repairCalls.length, 2, 'conflict 后可重试');
  ok(repairCalls[0].input.requestId !== repairCalls[1].input.requestId, '冲突重试使用新 requestId');
  const secondCall = repairCalls[1].input as RepairCornerstoneInput;
  ok(secondCall.content !== undefined, '重试仍传 content 而非 ref');

  await mounted.cleanup();
}

async function testSnapshotRepairNetworkRetrySameRequestID(): Promise<void> {
  reset();
  const replacement = 'network-content';
  const view = makeView([
    makeCornerstone('cs-blob-network', {
      mode: 'snapshot',
      ref: { kind: 'inline', blobDigest: 'blob-network' },
      digest: contentDigest(replacement),
      status: 'invalid',
      resolveErrorKind: 'invalid',
    }),
  ]);
  useWorkStore.getState().applySnapshot(view);
  const network = new Set(['repair']);
  const port = new TestPort(view, { networkErrorOn: network });
  const mounted = await mount(<WorkCard workID={view.work.id} port={port} />);
  await click(mounted.host.querySelector('[data-testid="cornerstone-drawer"] > button'));

  const item = mounted.host.querySelector<HTMLElement>('[data-testid="cornerstone-item-cs-blob-network"]')!;
  await change(item.querySelector<HTMLTextAreaElement>('textarea')!, replacement);
  await click(button(item, '修复快照'));

  ok(item.textContent?.includes('网络请求失败') ?? false, 'snapshot repair 网络失败显式显示');
  ok(!!item.querySelector('.cornerstone-item__error'), '网络错误可重试');

  // Retry with same requestID
  network.delete('repair');
  await click(button(item.querySelector('.cornerstone-item__error')!, '重试'));
  const repairCalls = port.calls.filter((call) => call.action === 'repair');
  eq(repairCalls.length, 2, '网络失败后可重试');
  eq(repairCalls[0].input.requestId, repairCalls[1].input.requestId, '网络重试复用 requestId');
  const retryCall = repairCalls[1].input as RepairCornerstoneInput;
  eq(retryCall.content, 'network-content', '重试保持 content');
  ok(retryCall.ref === undefined || retryCall.ref === null, '网络重试不换成 ref');

  await mounted.cleanup();
}

async function testLiveRefRepairStillWorksWithRef(): Promise<void> {
  reset();
  const view = makeView([
    makeCornerstone('cs-liveref-missing', {
      mode: 'live_ref',
      ref: { kind: 'workspace_file', path: 'missing.txt' },
      status: 'missing',
      resolveErrorKind: 'missing',
    }),
  ]);
  useWorkStore.getState().applySnapshot(view);
  const port = new TestPort(view);
  const mounted = await mount(<WorkCard workID={view.work.id} port={port} />);
  await click(mounted.host.querySelector('[data-testid="cornerstone-drawer"] > button'));

  const item = mounted.host.querySelector<HTMLElement>('[data-testid="cornerstone-item-cs-liveref-missing"]')!;
  // live_ref still shows ref editing, NOT content textarea
  ok(!!item.querySelector('select[aria-label*="修复"]'), 'live_ref 修复显示 ref 类型选择');
  ok(!!item.querySelector('input[aria-label*="修复"]'), 'live_ref 修复显示 ref 值输入');
  ok(!item.querySelector('textarea[aria-label*="快照内容"]'), 'live_ref 修复不显示 content textarea');
  ok(!!button(item, '修复引用'), 'live_ref 显示修复引用按钮');

  await change(item.querySelector('input[aria-label*="修复"]')!, 'new-file.txt');
  await click(button(item, '修复引用'));

  const repairCall = port.calls.find((call) => call.action === 'repair')!.input as RepairCornerstoneInput;
  ok(repairCall.ref !== undefined && repairCall.ref !== null, 'live_ref repair 传 ref');
  eq(repairCall.ref?.path, 'new-file.txt', 'live_ref repair ref 正确');
  ok(repairCall.content === undefined || repairCall.content === null, 'live_ref repair 不传 content');

  await mounted.cleanup();
}

console.log('\ncornerstone drawer — T5');
await testFixedOuterIdentity();
await testTypedMutations();
await testConflictAndNetworkRetry();
await testUnifiedAttentionAndRunGate();
await testRunRetryReusesRequestID();
await testProductionWorkCardAssembly();
await testWailsConflictLoadsLatestProjection();
await testWailsSnapshotRepairContentRoundTrip();
await testWailsWatchHandshakeAndRecovery();
await testWatchHealthIsolation();
await testSnapshotBlobRepairContentPath();
await testSnapshotRepairDraftSurvivesFlip();
await testSnapshotRepairConflictPreservesDraft();
await testSnapshotRepairNetworkRetrySameRequestID();
await testLiveRefRepairStillWorksWithRef();

console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
