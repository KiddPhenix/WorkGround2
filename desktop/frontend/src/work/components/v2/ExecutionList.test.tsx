import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { useWorkStore, useWorkUIStore } from '../../store';
import type { BlockInstance } from '../../types';
import type {
  CornerstonePinResult,
  ApplyWorkPatchResult,
  NodeDef,
  SetInputCornerstoneRequest,
  SubmitInputResult,
  SubmitWorkInputRequest,
  TaskStateV2,
  TaskV2View,
  PreviewWorkPatchResult,
  WorkPatchPreview,
  WorkDefinitionRevision,
  WorkInput,
} from '../../types_v2';
import { ExecutionList } from './index';
import { v2DiscussionBlockId } from './discussionBlock';

// ── test harness ───────────────────────────────────────────────────────────

let passed = 0;
let failed = 0;

function ok(condition: boolean | undefined | null, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else { failed++; if (failed <= 5) process.stdout.write(`       ${new Error().stack?.split('\n')[2]?.trim() ?? ''}\n`); }
}

function eq<T>(actual: T, expected: T, label: string): void {
  const cond = actual === expected;
  ok(cond, `${label}${cond ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function contains(actual: string, substring: string, label: string): void {
  ok(actual.includes(substring), `${label} (expected "${substring}" in "${actual.slice(0, 80)}")`);
}

function setupDOM(): JSDOM {
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
    SVGElement: dom.window.SVGElement,
    Event: dom.window.Event,
    MouseEvent: dom.window.MouseEvent,
    KeyboardEvent: dom.window.KeyboardEvent,
    MutationObserver: dom.window.MutationObserver,
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
  });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
  Object.defineProperties(dom.window.HTMLElement.prototype, {
    attachEvent: { configurable: true, value: () => undefined },
    detachEvent: { configurable: true, value: () => undefined },
  });
  return dom;
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

function resetStore(): void {
  useWorkStore.getState().clearAll();
  useWorkUIStore.getState().clearAll();
  window.localStorage.clear();
}

// ── fixtures ───────────────────────────────────────────────────────────────

const WORK_ID = 'work-el-test';

function makeNodeDef(overrides: Partial<NodeDef> = {}): NodeDef {
  return {
    id: 'node-1',
    title: '节点 1',
    ...overrides,
  };
}

function makeDefinition(nodes: NodeDef[]): WorkDefinitionRevision {
  return {
    workId: WORK_ID,
    revision: 1,
    parentRevision: 0,
    status: 'active',
    goal: '测试目标',
    nodes,
    artifactSlots: [],
    inputSpecs: [],
    createdBy: 'test',
    createdAt: new Date().toISOString(),
    digest: 'abc',
  };
}

function makeTask(overrides: Partial<TaskV2View> = {}): TaskV2View {
  return {
    id: 'task-1',
    runId: 'run-1',
    nodeId: 'node-1',
    title: '任务 1',
    state: 'pending',
    retryable: false,
    updatedAt: new Date().toISOString(),
    ...overrides,
  };
}

function makeBlock(id: string, revision = 1, title = ''): BlockInstance {
  const now = '2026-07-24T16:00:00Z';
  return {
    id,
    kind: 'markdown',
    schemaVersion: 1,
    revision,
    title,
    status: 'ready',
    data: {},
    source: { provider: 'controller', mode: 'snapshot', verified: true },
    fallback: { summary: title },
    createdAt: now,
    updatedAt: now,
  };
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function seedStore(tasks: TaskV2View[], definition?: WorkDefinitionRevision, inputs?: WorkInput[]): void {
  useWorkStore.setState((s) => ({
    ...s,
    v2Tasks: { ...s.v2Tasks, [WORK_ID]: tasks },
    v2Definitions: definition ? { ...s.v2Definitions, [WORK_ID]: definition } : s.v2Definitions,
    v2Inputs: inputs ? { ...s.v2Inputs, [WORK_ID]: inputs } : s.v2Inputs,
  }));
}

// ── golden data validation ─────────────────────────────────────────────────

const __dirname = dirname(fileURLToPath(import.meta.url));
const goldenPath = resolve(__dirname, '..', '..', '..', '..', '..', '..', 'internal', 'work', 'testdata', 'contract-v2', 'work-view-v2-full.json');
const goldenView: unknown = JSON.parse(readFileSync(goldenPath, 'utf-8'));
const cssText = readFileSync(resolve(__dirname, '..', '..', '..', 'styles.css'), 'utf-8');

// ── tests ──────────────────────────────────────────────────────────────────

async function runTests(): Promise<void> {
  // ════════════════════════════════════════════════════════════════════════
  // 1. Empty state
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    seedStore([]);
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);
    const empty = host.querySelector('[data-testid="execution-list-empty"]');
    ok(empty !== null, 'empty: node exists');
    contains(empty?.textContent ?? '', '暂无执行任务', 'empty: text');
    eq(empty?.getAttribute('role'), 'status', 'empty: role=status');
    await cleanup();
  }

  // Active-run authority: historical tasks/inputs with the same spec never
  // leak into the current run and task identity is never rewritten.
  {
    resetStore();
    const spec = { id: 'shared-spec', label: '同名输入', kind: 'text' as const, required: true, pinEligible: false };
    const definition = {
      ...makeDefinition([makeNodeDef({ id: 'shared-node', inputSpecIds: [spec.id], blockIds: ['shared-block'] })]),
      inputSpecs: [spec],
    };
    const oldTask = makeTask({
      id: 'old-task',
      runId: 'old-run',
      nodeId: 'shared-node',
      state: 'waiting_input',
      waitingInputIds: ['old-input'],
    });
    const activeTask = makeTask({
      id: 'active-task',
      runId: 'active-run',
      nodeId: 'shared-node',
      state: 'waiting_input',
      waitingInputIds: ['active-input'],
    });
    const makeInput = (id: string, runId: string, taskId: string, value: string): WorkInput => ({
      id, workId: WORK_ID, runId, taskId, blockId: 'shared-block', specId: spec.id,
      value, state: 'requested', revision: 1, updatedAt: '2026-07-24T16:00:00Z',
    });
    seedStore(
      [oldTask, activeTask],
      definition,
      [
        makeInput('old-input', 'old-run', 'old-task', '历史值'),
        makeInput('active-input', 'active-run', 'active-task', '当前值'),
      ],
    );
    const committedSubmit = async (): Promise<SubmitInputResult> => ({
      revision: 2, duplicate: false, committed: true, recoverable: false,
    });
    const committedPin = async (request: SetInputCornerstoneRequest): Promise<CornerstonePinResult> => ({
      pinned: request.pin, revision: 2, duplicate: false, committed: true, recoverable: false,
    });
    const { host, cleanup } = await mount(
      <ExecutionList
        workId={WORK_ID}
        runId="active-run"
        expandedTaskId="active-task"
        onSubmitWorkInput={committedSubmit}
        onSetCornerstone={committedPin}
        onUnsetCornerstone={committedPin}
        onRefreshAuthoritative={async () => {}}
      />,
    );
    ok(host.querySelector('[data-testid="execution-row-old-task"]') === null, 'active-run: historical task is filtered');
    ok(host.querySelector('[data-testid="execution-row-active-task"]') !== null, 'active-run: authoritative task is rendered');
    eq(
      host.querySelector<HTMLInputElement>('[data-testid="work-input-control-active-task-shared-spec"]')?.value,
      '当前值',
      'active-run: same spec resolves only the full-identity current input',
    );
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 2. Single task renders
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't1', title: '分析数据', state: 'running' })];
    const nodes = [makeNodeDef({ id: 'node-1', title: '分析数据' })];
    seedStore(tasks, makeDefinition(nodes));
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);
    const list = host.querySelector('[data-testid="execution-list"]');
    ok(list !== null, 'single: list rendered');
    eq(list?.getAttribute('role'), 'list', 'single: role=list');
    const row = host.querySelector('[data-testid="execution-row-t1"]');
    ok(row !== null, 'single: row exists');
    eq(row?.getAttribute('data-task-state'), 'running', 'single: data-task-state=running');
    const toggle = host.querySelector('[data-testid="execution-row-toggle-t1"]');
    eq(toggle?.getAttribute('aria-expanded'), 'false', 'single: aria-expanded=false');
    const icon = host.querySelector('[data-testid="execution-row-icon-t1"]');
    ok(icon?.querySelector('.lucide-loader-circle') !== null, 'single: running icon uses the V2 icon treatment');
    const badge = host.querySelector('[data-testid="execution-row-badge-t1"]');
    contains(badge?.textContent ?? '', '运行中', 'single: badge shows 运行中');
    contains(host.querySelector('.wg2-el-heading')?.textContent ?? '', 'AI 正在执行', 'single: V2 execution heading');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 3. All 10 task states have distinct icons and labels
  // ════════════════════════════════════════════════════════════════════════
  {
    const stateCases: Array<[TaskStateV2, string, string]> = [
      ['pending', '.lucide-clock-3', '等待中'],
      ['ready', '.lucide-circle', '就绪'],
      ['running', '.lucide-loader-circle', '运行中'],
      ['waiting_input', '.lucide-pencil-line', '等待输入'],
      ['waiting_approval', '.lucide-shield-alert', '等待批准'],
      ['completed', '.lucide-check', '已完成'],
      ['failed_retryable', '.lucide-rotate-ccw', '失败（可重试）'],
      ['failed_terminal', '.lucide-x', '失败（不可恢复）'],
      ['canceled', '.lucide-ban', '已取消'],
      ['invalidated', '.lucide-refresh-cw', '待重新生成'],
    ];
    for (const [state, iconSelector, expectedLabel] of stateCases) {
      resetStore();
      const tasks = [makeTask({ id: `st-${state}`, nodeId: 'n1', state, title: `${expectedLabel}任务` })];
      seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1', title: `${expectedLabel}任务` })]));
      const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);
      const row = host.querySelector(`[data-testid="execution-row-st-${state}"]`);
      ok(row !== null, `state-${state}: row rendered`);
      eq(row?.getAttribute('data-task-state'), state, `state-${state}: data-task-state`);
      const icon = host.querySelector(`[data-testid="execution-row-icon-st-${state}"]`);
      ok(icon?.querySelector(iconSelector) !== null, `state-${state}: icon=${iconSelector}`);
      const badge = host.querySelector(`[data-testid="execution-row-badge-st-${state}"]`);
      contains(badge?.textContent ?? '', expectedLabel, `state-${state}: badge=${expectedLabel}`);
      await cleanup();
    }
    ok(
      /\.wg2-el-item\[data-task-state="running"\]\s*\{[\s\S]*?--wg2-el-flow-a:[^;]*#38bdf8[\s\S]*?--wg2-el-flow-b:\s*#2dd4bf[\s\S]*?--wg2-el-flow-c:\s*#a78bfa/.test(cssText),
      'state-flow: running uses the blue, cyan, and violet palette',
    );
    ok(
      /\.wg2-el-item\[data-task-state="waiting_input"\],[\s\S]*?--wg2-el-flow-a:\s*var\(--warn\)/.test(cssText),
      'state-flow: user input and approval use the warning palette',
    );
    ok(
      /\.wg2-el-item\[data-task-state="failed_retryable"\],[\s\S]*?--wg2-el-flow-a:\s*var\(--err\)/.test(cssText),
      'state-flow: failures use the error palette',
    );
    ok(
      /animation:\s*wg2-el-border-orbit var\(--wg2-el-flow-speed\) linear infinite/.test(cssText),
      'state-flow: attention border orbits continuously',
    );
    ok(
      /prefers-reduced-motion:\s*reduce[\s\S]*?\.wg2-el-item\[data-task-state="running"\]::before[\s\S]*?animation:\s*none/.test(cssText),
      'state-flow: reduced motion freezes the orbit',
    );
  }

  // ════════════════════════════════════════════════════════════════════════
  // 4. Expand / collapse
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-exp', nodeId: 'n1', title: '展开测试', state: 'ready' })];
    const nodes = [makeNodeDef({ id: 'n1', title: '展开测试', description: '节点描述文本' })];
    seedStore(tasks, makeDefinition(nodes));

    const expandCalls: Array<{ workId: string; taskId: string }> = [];
    const collapseCalls: Array<{ workId: string; taskId: string }> = [];

    const { host, cleanup } = await mount(
      <ExecutionList
        workId={WORK_ID}
        expandedTaskId="t-exp"
        onExpandTask={(i) => expandCalls.push(i)}
        onCollapseTask={(i) => collapseCalls.push(i)}
      />,
    );

    // Expanded block visible
    const panel = host.querySelector('[data-testid="expanded-block-t-exp"]');
    ok(panel !== null, 'expand: panel visible');
    const desc = host.querySelector('[data-testid="expanded-block-desc-t-exp"]');
    contains(desc?.textContent ?? '', '节点描述文本', 'expand: description');

    ok(
      host.querySelector('[data-testid="expanded-block-collapse-t-exp"]') === null,
      'expand: lower collapse button is omitted',
    );
    const collapseBtn = host.querySelector<HTMLButtonElement>('[data-testid="execution-row-toggle-t-exp"]');
    await interact(() => collapseBtn?.click());
    eq(collapseCalls.length, 1, 'expand: collapse intent fired');
    eq(collapseCalls[0]?.taskId, 't-exp', 'expand: collapse correct taskId');
    await cleanup();
  }

  // V2 discussion entry is available on a collapsed row and does not expand it.
  {
    resetStore();
    const tasks = [makeTask({
      id: 't-discuss-row',
      nodeId: 'n-discuss',
      title: '已完成任务',
      state: 'completed',
    })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n-discuss' })]));
    const expandCalls: Array<{ workId: string; taskId: string }> = [];
    const { host, cleanup } = await mount(
      <ExecutionList
        workId={WORK_ID}
        sessionId="session-discuss"
        onExpandTask={(intent) => expandCalls.push(intent)}
      />,
    );
    const discuss = host.querySelector<HTMLButtonElement>('[data-testid="execution-row-discuss-t-discuss-row"]');
    ok(discuss !== null, 'row discussion: V2 discussion action visible while collapsed');
    contains(discuss?.textContent ?? '', '修改意见', 'row discussion: completed task uses revision wording');
    await interact(() => {
      discuss?.focus();
      discuss?.click();
    });
    ok(document.querySelector('[data-testid="discussion-drawer-t-discuss-row"]') !== null, 'row discussion: drawer opens');
    eq(
      document.querySelector<HTMLInputElement>('[data-testid="discussion-scope-workflow-t-discuss-row"]')?.checked,
      true,
      'row discussion: completed task defaults to downstream workflow revision',
    );
    contains(
      document.querySelector('[data-testid="discussion-scope-t-discuss-row"]')?.textContent ?? '',
      '影响当前项及后续任务',
      'row discussion: downstream effect is explicit',
    );
    eq(expandCalls.length, 0, 'row discussion: opening discussion does not expand task');
    await interact(() =>
      document.querySelector<HTMLButtonElement>('[data-testid="discussion-close-t-discuss-row"]')?.click());
    await cleanup();
  }

  // ResultShelf structure changes open the existing discussion flow with a
  // human-readable draft and a locked workflow scope.
  {
    resetStore();
    const tasks = [makeTask({
      id: 't-result-change',
      nodeId: 'n-result-change',
      title: '生成成果',
      state: 'completed',
    })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n-result-change' })]));
    const { cleanup } = await mount(
      <ExecutionList
        workId={WORK_ID}
        sessionId="session-result-change"
        externalWorkflowDiscussion={{
          token: 'add:summary:1',
          nodeId: 'n-result-change',
          title: '新增成果：总结',
          instruction: '新增成果“总结”，由任务“生成成果”产出。',
        }}
      />,
    );
    const drawer = document.querySelector('[data-testid="discussion-drawer-t-result-change"]');
    ok(drawer !== null, 'result workflow: external request opens discussion drawer');
    eq(
      document.querySelector<HTMLTextAreaElement>('[data-testid="discussion-input-t-result-change"]')?.value,
      '新增成果“总结”，由任务“生成成果”产出。',
      'result workflow: human-readable instruction is preserved',
    );
    contains(
      document.querySelector('[data-testid="discussion-scope-t-result-change"]')?.textContent ?? '',
      '流程结构变更',
      'result workflow: workflow-only scope is explicit',
    );
    ok(
      document.querySelector('[data-testid="discussion-scope-block-t-result-change"]') === null,
      'result workflow: block-only scope cannot be selected',
    );
    await interact(() =>
      document.querySelector<HTMLButtonElement>('[data-testid="discussion-close-t-result-change"]')?.click());
    await cleanup();
  }

  // A successful patch may replace the Definition while its authoritative
  // refresh is still pending. That self-triggered identity change must close
  // the original drawer after refresh instead of leaving stale UI behind.
  {
    resetStore();
    const sessionId = 'session-auto-close';
    const blockId = 'block-auto-close';
    const task = makeTask({
      id: 'task-auto-close',
      runId: 'run-auto-close',
      nodeId: 'node-auto-close',
      title: '自动关闭测试',
      state: 'completed',
    });
    const definition = makeDefinition([
      makeNodeDef({ id: task.nodeId, title: task.title, blockIds: [blockId] }),
    ]);
    seedStore([task], definition);
    const draftKey = [
      WORK_ID,
      task.runId,
      sessionId,
      task.id,
      blockId,
      definition.revision,
      1,
    ].join('\u0000');
    useWorkUIStore.getState().setDiscussionDraft(WORK_ID, draftKey, '更新当前任务及后续步骤');
    let refreshCalls = 0;
    const { host, cleanup } = await mount(
      <ExecutionList
        workId={WORK_ID}
        sessionId={sessionId}
        workRevision={definition.revision}
        blocks={[makeBlock(blockId)]}
        onPreviewPatch={async () => ({
          preview: {
            id: 'patch-auto-close',
            workId: WORK_ID,
            runId: task.runId,
            taskId: task.id,
            blockId,
            sessionId,
            baseDefinitionRev: definition.revision,
            baseBlockRev: 1,
            scope: 'workflow',
            operations: [{
              op: 'replace',
              path: `nodes/${task.nodeId}/description`,
              newValue: '新的任务说明',
            }],
            affectedNodeIds: [task.nodeId],
            affectedBlockIds: [blockId],
            affectedArtifactSlotIds: [],
            staleArtifactSlotIds: [],
            invalidatedTaskIds: [task.id],
            requiresRerun: true,
            digest: 'digest-auto-close',
            expiresAt: '2099-07-24T00:00:00Z',
          },
          revision: definition.revision,
          duplicate: false,
          committed: true,
          recoverable: false,
        })}
        onApplyPatch={async (intent) => {
          // The event stream can project the new Definition before the
          // ApplyWorkPatch RPC returns its receipt.
          seedStore(
            [
              { ...task, state: 'invalidated' },
              makeTask({
                id: 'task-after-patch',
                runId: 'run-after-patch',
                nodeId: task.nodeId,
                title: task.title,
                state: 'waiting_input',
              }),
            ],
            { ...definition, revision: 2, parentRevision: definition.revision },
          );
          await new Promise<void>((resolveApply) => setTimeout(resolveApply, 0));
          return {
            workRevision: 2,
            newRevision: 2,
            affectedArtifactSlotIds: [],
            staleArtifactSlotIds: [],
            invalidatedTaskIds: [task.id],
            requiresRerun: true,
            duplicate: false,
            committed: true,
            recoverable: false,
            receipt: {
              requestId: intent.requestId,
              operation: 'patch.apply',
              intentDigest: 'intent-auto-close',
              patchId: intent.patchId,
              resultRevision: 2,
              resultDigest: 'result-auto-close',
              affectedArtifactSlotIds: [],
              staleArtifactSlotIds: [],
              invalidatedTaskIds: [task.id],
              requiresRerun: true,
              createdAt: '2026-07-24T00:00:00Z',
            },
          };
        }}
        onRefreshAuthoritative={async () => {
          refreshCalls++;
        }}
      />,
    );
    await interact(() =>
      host.querySelector<HTMLButtonElement>('[data-testid="execution-row-discuss-task-auto-close"]')?.click());
    await interact(() =>
      document.querySelector<HTMLButtonElement>('[data-testid="discussion-preview-btn-task-auto-close"]')?.click());
    ok(
      document.querySelector('[data-testid="discussion-drawer-task-auto-close"]') !== null,
      'discussion auto-close: drawer stays open for confirmation',
    );
    await interact(() =>
      document.querySelector<HTMLButtonElement>('[data-testid="discussion-apply-btn-task-auto-close"]')?.click());
    await settle();
    eq(refreshCalls, 1, 'discussion auto-close: authoritative refresh runs exactly once');
    ok(
      document.querySelector('[data-testid="discussion-drawer-task-auto-close"]') === null,
      'discussion auto-close: pre-response structure update still closes the drawer',
    );
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 5. Click row toggles expand
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-click', nodeId: 'n1', title: '点击展开' })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1' })]));

    const expandCalls: Array<{ workId: string; taskId: string }> = [];
    const { host, cleanup } = await mount(
      <ExecutionList workId={WORK_ID} onExpandTask={(i) => expandCalls.push(i)} />,
    );

    const header = host.querySelector<HTMLElement>('[data-testid="execution-row-header-t-click"]');
    ok(header !== null, 'click: header exists');
    await interact(() => header?.click());
    eq(expandCalls.length, 1, 'click: expand intent fired');
    eq(expandCalls[0]?.taskId, 't-click', 'click: correct taskId');
    eq(expandCalls[0]?.workId, WORK_ID, 'click: correct workId');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 6. Native toggle button is keyboard-operable
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-key', nodeId: 'n1', title: '键盘测试' })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1' })]));

    const expandCalls: Array<{ workId: string; taskId: string }> = [];
    const { host, cleanup } = await mount(
      <ExecutionList workId={WORK_ID} onExpandTask={(i) => expandCalls.push(i)} />,
    );

    const toggle = host.querySelector<HTMLButtonElement>('[data-testid="execution-row-toggle-t-key"]');
    ok(toggle !== null, 'keyboard: native toggle exists');
    eq(toggle?.tagName, 'BUTTON', 'keyboard: native button semantics');
    toggle?.focus();
    eq(document.activeElement, toggle, 'keyboard: toggle can receive focus');
    await interact(() => toggle?.click());
    eq(expandCalls.length, 1, 'keyboard: native activation fires expand');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 7. Task order stable by definition node order
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const nodes = [
      makeNodeDef({ id: 'n3', title: '第三步' }),
      makeNodeDef({ id: 'n1', title: '第一步' }),
      makeNodeDef({ id: 'n2', title: '第二步' }),
    ];
    // Tasks arrive in reverse order, but should sort by definition
    const tasks = [
      makeTask({ id: 't2', nodeId: 'n2', title: '第二步' }),
      makeTask({ id: 't3', nodeId: 'n3', title: '第三步' }),
      makeTask({ id: 't1', nodeId: 'n1', title: '第一步' }),
    ];
    seedStore(tasks, makeDefinition(nodes));
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);

    const items = host.querySelectorAll('[data-testid^="execution-list-item-"]');
    eq(items.length, 3, 'order: 3 items');
    // Definition order: n3, n1, n2 → expected task order: t3, t1, t2
    const ids = [...items].map((el) => el.getAttribute('data-testid'));
    ok(ids[0]?.includes('t3'), `order: first is t3 (got ${ids[0]})`);
    ok(ids[1]?.includes('t1'), `order: second is t1 (got ${ids[1]})`);
    ok(ids[2]?.includes('t2'), `order: third is t2 (got ${ids[2]})`);

    // Progress events do not reorder — update a task state, check order stays
    await act(async () => {
      seedStore([
        makeTask({ id: 't2', nodeId: 'n2', title: '第二步', state: 'completed' }),
        makeTask({ id: 't3', nodeId: 'n3', title: '第三步' }),
        makeTask({ id: 't1', nodeId: 'n1', title: '第一步', state: 'running' }),
      ], makeDefinition(nodes));
      await new Promise<void>((r) => setTimeout(r, 20));
    });
    const items2 = host.querySelectorAll('[data-testid^="execution-list-item-"]');
    const ids2 = [...items2].map((el) => el.getAttribute('data-testid'));
    ok(ids2[0]?.includes('t3'), `reorder-resist: first still t3 (got ${ids2[0]})`);
    ok(ids2[1]?.includes('t1'), `reorder-resist: second still t1 (got ${ids2[1]})`);
    ok(ids2[2]?.includes('t2'), `reorder-resist: third still t2 (got ${ids2[2]})`);
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 8. Identity stability: task.id as React key
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const spec = { id: 'spec-id', label: '稳定草稿', kind: 'text' as const, required: true, pinEligible: false };
    const node = makeNodeDef({ id: 'n1', blockIds: ['block-id'], inputSpecIds: [spec.id] });
    const definition = { ...makeDefinition([node]), inputSpecs: [spec] };
    const tasks = [makeTask({
      id: 't-id',
      nodeId: 'n1',
      title: 'Identity Test',
      state: 'waiting_input',
      waitingInputIds: [spec.id],
    })];
    const workInput: WorkInput = {
      id: 'input-id',
      workId: WORK_ID,
      runId: 'run-1',
      taskId: 't-id',
      blockId: 'block-id',
      specId: spec.id,
      value: null,
      state: 'requested',
      revision: 1,
      updatedAt: '2026-07-24T14:00:00Z',
    };
    seedStore(tasks, definition, [workInput]);

    const submitCalls: SubmitWorkInputRequest[] = [];
    let refreshCalls = 0;
    const onSubmitWorkInput = async (request: SubmitWorkInputRequest): Promise<SubmitInputResult> => {
      submitCalls.push(request);
      if (submitCalls.length === 1) {
        return {
          revision: workInput.revision,
          duplicate: false,
          committed: false,
          recoverable: true,
          error: '临时传输失败，可重试',
        };
      }
      return {
        input: {
          ...workInput,
          value: request.value,
          state: 'submitted',
          revision: workInput.revision + 1,
          updatedAt: '2026-07-24T15:01:00Z',
        },
        revision: workInput.revision + 1,
        duplicate: true,
        committed: true,
        recoverable: false,
      };
    };
    const onCornerstone = async (request: SetInputCornerstoneRequest): Promise<CornerstonePinResult> => ({
      pinned: request.pin,
      revision: workInput.revision,
      duplicate: false,
      committed: true,
      recoverable: false,
    });
    const listProps = {
      workId: WORK_ID,
      runId: 'run-1',
      workRevision: definition.revision,
      onSubmitWorkInput,
      onSetCornerstone: onCornerstone,
      onUnsetCornerstone: onCornerstone,
      onRefreshAuthoritative: async () => { refreshCalls++; },
    };
    const { host, root, cleanup } = await mount(
      <ExecutionList
        {...listProps}
        expandedTaskId="t-id"
      />,
    );

    // Expanded block is mounted
    ok(host.querySelector('[data-testid="expanded-block-t-id"]') !== null, 'identity: block mounted');
    const originalRow = host.querySelector('[data-testid="execution-row-t-id"]');
    const originalControl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-t-id-spec-id"]');
    ok(originalControl !== null, 'identity: typed input control mounted from authoritative WorkInput');
    await interact(() => {
      const key = [
        WORK_ID,
        workInput.runId,
        workInput.taskId,
        workInput.blockId,
        workInput.id,
        workInput.specId,
        definition.revision,
        workInput.revision,
      ].join('\u0000');
      useWorkUIStore.getState().setInputDirtyFlag(WORK_ID, key);
      useWorkUIStore.getState().setInputDraft(WORK_ID, key, '保留的草稿');
    });
    eq(originalControl?.value, '保留的草稿', 'identity: persisted typed draft reaches WorkInputHost');

    // Update task state — same task.id, row should NOT remount (key stable)
    await act(async () => {
      seedStore([
        makeTask({
          id: 't-id',
          nodeId: 'n1',
          title: 'Identity Test',
          state: 'waiting_input',
          waitingInputIds: [spec.id],
          updatedAt: '2026-07-24T15:00:00Z',
        }),
      ], definition, [workInput]);
      await new Promise<void>((r) => setTimeout(r, 20));
    });

    // Row and input DOM identities survive projection updates.
    const row = host.querySelector('[data-testid="execution-row-t-id"]');
    ok(row !== null, 'identity: row still present after state change');
    ok(row === originalRow, 'identity: row DOM node was not remounted');
    ok(host.querySelector('[data-testid="expanded-block-t-id"]') !== null, 'identity: expanded block still mounted');
    const updatedControl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-t-id-spec-id"]');
    ok(updatedControl === originalControl, 'identity: input control was not remounted');
    eq(updatedControl?.value, '保留的草稿', 'identity: typed draft survives late projection update');

    // Collapse may unmount the panel, but the work/task/spec-scoped draft remains.
    await act(async () => {
      root.render(<ExecutionList {...listProps} expandedTaskId={null} />);
    });
    ok(host.querySelector('[data-testid="expanded-block-t-id"]') === null, 'identity: collapse hides inline panel');
    await act(async () => {
      root.render(<ExecutionList {...listProps} expandedTaskId="t-id" />);
    });
    const restoredControl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-t-id-spec-id"]');
    eq(restoredControl?.value, '保留的草稿', 'identity: typed draft survives collapse/re-expand');

    // A temporary projection removal must converge safely without discarding
    // the local draft; the same stable task identity restores it on reappear.
    await act(async () => {
      seedStore([], definition, []);
    });
    ok(host.querySelector('[data-testid="execution-row-t-id"]') === null, 'identity: removed task leaves no stale row');
    await act(async () => {
      seedStore(tasks, definition, [workInput]);
    });
    const recoveredControl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-t-id-spec-id"]');
    eq(recoveredControl?.value, '保留的草稿', 'identity: typed draft survives temporary projection removal');

    const submit = host.querySelector<HTMLButtonElement>('[data-testid="expanded-block-submit-t-id"]');
    await interact(() => submit?.click());
    eq(submitCalls.length, 1, 'identity: first typed submit reaches recoverable failure');
    eq(submitCalls[0]?.value, '保留的草稿', 'identity: typed submit carries retained draft');
    contains(
      host.querySelector('[data-testid="work-input-error-t-id-spec-id"]')?.textContent ?? '',
      '临时传输失败',
      'identity: recoverable failure stays visible',
    );
    await interact(() => submit?.click());
    eq(submitCalls.length, 2, 'identity: typed submit can retry safely');
    eq(submitCalls[1]?.requestId, submitCalls[0]?.requestId, 'identity: retry reuses the same requestId');
    eq(submitCalls[1]?.value, '保留的草稿', 'identity: retry retains the same draft value');
    eq(refreshCalls, 1, 'identity: committed retry refreshes authoritative state once');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 9. Parallel updates — one waiting doesn't block others
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const nodes = [
      makeNodeDef({ id: 'n-wait', title: '等待节点' }),
      makeNodeDef({ id: 'n-run', title: '运行节点' }),
    ];
    const tasks = [
      makeTask({ id: 't-wait', nodeId: 'n-wait', title: '等待节点', state: 'waiting_input' }),
      makeTask({ id: 't-run', nodeId: 'n-run', title: '运行节点', state: 'running' }),
    ];
    seedStore(tasks, makeDefinition(nodes));
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);

    // Both rows present
    ok(host.querySelector('[data-testid="execution-row-t-wait"]') !== null, 'parallel: waiting row exists');
    ok(host.querySelector('[data-testid="execution-row-t-run"]') !== null, 'parallel: running row exists');

    // Running row is not blocked
    const runBadge = host.querySelector('[data-testid="execution-row-badge-t-run"]');
    contains(runBadge?.textContent ?? '', '运行中', 'parallel: running not blocked by waiting');

    // Both are in the list — independent
    eq(host.querySelectorAll('[data-testid^="execution-list-item-"]').length, 2, 'parallel: 2 items');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 10. ARIA: list/listitem roles, aria-expanded, aria-label
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-a11y', nodeId: 'n1', title: 'A11y 任务', state: 'running' })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1', title: 'A11y 任务' })]));
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);

    const list = host.querySelector('[data-testid="execution-list"]');
    eq(list?.getAttribute('role'), 'list', 'a11y: list role');
    ok(list?.getAttribute('aria-label')?.includes('执行任务列表') ?? false, 'a11y: list aria-label');

    const item = host.querySelector('[data-testid="execution-list-item-t-a11y"]');
    eq(item?.getAttribute('role'), 'listitem', 'a11y: item role=listitem');
    const row = host.querySelector('[data-testid="execution-row-t-a11y"]');
    ok(row?.getAttribute('aria-label')?.includes('A11y 任务') ?? false, 'a11y: aria-label includes title');
    ok(row?.getAttribute('aria-label')?.includes('运行中') ?? false, 'a11y: aria-label includes state');

    const toggle = host.querySelector('[data-testid="execution-row-toggle-t-a11y"]');
    eq(toggle?.tagName, 'BUTTON', 'a11y: native toggle button');
    eq(toggle?.getAttribute('aria-expanded'), 'false', 'a11y: toggle aria-expanded=false');
    eq(toggle?.getAttribute('aria-controls'), 'expanded-block-t-a11y', 'a11y: toggle controls inline panel');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 11. 50+ rows — render all, check bounded
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const count = 55;
    const nodes: NodeDef[] = [];
    const taskList: TaskV2View[] = [];
    for (let i = 0; i < count; i++) {
      const nid = `n${i}`;
      nodes.push(makeNodeDef({ id: nid, title: `节点 ${i}` }));
      taskList.push(makeTask({ id: `t${i}`, nodeId: nid, title: `节点 ${i}`, state: i % 5 === 0 ? 'running' : 'pending' }));
    }
    seedStore(taskList, makeDefinition(nodes));
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);

    const items = host.querySelectorAll('[data-testid^="execution-list-item-"]');
    eq(items.length, count, `50+: ${count} items rendered`);
    // All rows have data-task-state
    const rowsWithState = host.querySelectorAll('.wg2-er-row[data-task-state]');
    eq(rowsWithState.length, count, `50+: all ${count} rows have data-task-state`);
    ok(cssText.includes('max-height: min(70vh, 44rem)'), '50+: list has a bounded viewport');
    ok(cssText.includes('overflow-y: auto'), '50+: list owns vertical scrolling');
    ok(cssText.includes('content-visibility: auto'), '50+: offscreen rows skip rendering work');
    ok(cssText.includes('contain-intrinsic-size: auto 64px'), '50+: offscreen rows reserve stable height');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 12. Missing definition — preserve projection order across mutable titles
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [
      makeTask({ id: 't-z', nodeId: 'nz', title: 'Z 任务' }),
      makeTask({ id: 't-a', nodeId: 'na', title: 'A 任务' }),
    ];
    seedStore(tasks); // no definition
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);

    const items = host.querySelectorAll('[data-testid^="execution-list-item-"]');
    eq(items.length, 2, 'no-def: 2 items');
    // Projection order is authoritative while Definition is unavailable.
    const ids = [...items].map((el) => el.getAttribute('data-testid'));
    ok(ids[0]?.includes('t-z'), `no-def: projection first stays t-z (got ${ids[0]})`);
    ok(ids[1]?.includes('t-a'), `no-def: projection second stays t-a (got ${ids[1]})`);
    await act(async () => {
      seedStore([
        makeTask({ id: 't-z', nodeId: 'nz', title: 'A renamed' }),
        makeTask({ id: 't-a', nodeId: 'na', title: 'Z renamed' }),
      ]);
    });
    const renamed = [...host.querySelectorAll('[data-testid^="execution-list-item-"]')]
      .map((el) => el.getAttribute('data-testid'));
    ok(renamed[0]?.includes('t-z'), 'no-def: title change does not reorder first task');
    ok(renamed[1]?.includes('t-a'), 'no-def: title change does not reorder second task');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 13. Missing task nodeDef — safe convergence (no crash)
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-orphan', nodeId: 'nonexistent', title: '孤立任务' })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1' })]));
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);

    const row = host.querySelector('[data-testid="execution-row-t-orphan"]');
    ok(row !== null, 'orphan: row still renders');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 14. Terminal → invalidated legal transition
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-inv', nodeId: 'n1', title: '失效测试', state: 'completed' })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1' })]));
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);

    // Start as completed
    let badge = host.querySelector('[data-testid="execution-row-badge-t-inv"]');
    contains(badge?.textContent ?? '', '已完成', 'invalidated: starts completed');

    // Transition to invalidated
    await act(async () => {
      seedStore([makeTask({ id: 't-inv', nodeId: 'n1', title: '失效测试', state: 'invalidated' })]);
      await new Promise<void>((r) => setTimeout(r, 20));
    });

    badge = host.querySelector('[data-testid="execution-row-badge-t-inv"]');
    contains(badge?.textContent ?? '', '待重新生成', 'invalidated: transitions to invalidated');

    const row = host.querySelector('[data-testid="execution-row-t-inv"]');
    eq(row?.getAttribute('data-task-state'), 'invalidated', 'invalidated: data-task-state updated');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 15. Waiting inputs resolve correctly in ExpandedBlock
  // ════════════════════════════════════════════════════════════════════════
  {
    const tasks = [makeTask({
      id: 't-inputs',
      nodeId: 'n-inputs',
      title: '输入测试',
      state: 'waiting_input',
      waitingInputIds: ['spec-1', 'spec-3'],
    })];
    const nodes = [makeNodeDef({
      id: 'n-inputs',
      title: '输入测试',
      description: '需要提供以下信息',
      inputSpecIds: ['spec-1', 'spec-2', 'spec-3'],
    })];
    const definition = {
      ...makeDefinition(nodes),
      inputSpecs: [
        { id: 'spec-1', label: '姓名', kind: 'text' as const, required: true, pinEligible: false },
        { id: 'spec-2', label: '备注', kind: 'text' as const, required: false, pinEligible: false },
        { id: 'spec-3', label: '日期', kind: 'date' as const, required: true, pinEligible: true },
      ],
    };
    resetStore();
    seedStore(tasks, definition);

    const { host, cleanup } = await mount(
      <ExecutionList workId={WORK_ID} expandedTaskId="t-inputs" />,
    );

    // Only waiting inputs shown (spec-1 and spec-3, not spec-2)
    const input1 = host.querySelector('[data-testid="expanded-block-input-t-inputs-spec-1"]');
    ok(input1 !== null, 'inputs: spec-1 shown');
    contains(input1?.textContent ?? '', '姓名', 'inputs: spec-1 label');
    contains(input1?.textContent ?? '', '*', 'inputs: spec-1 required asterisk');

    const input3 = host.querySelector('[data-testid="expanded-block-input-t-inputs-spec-3"]');
    ok(input3 !== null, 'inputs: spec-3 shown');
    contains(input3?.textContent ?? '', '日期', 'inputs: spec-3 label');

    // spec-2 is not waiting, should not appear
    const input2 = host.querySelector('[data-testid="expanded-block-input-t-inputs-spec-2"]');
    ok(input2 === null, 'inputs: spec-2 not shown (not waiting)');

    await cleanup();
  }

  // Materialized inputs are editable in one compact group. Tab advances only
  // through that Block's fields, then leaves the group after the final field.
  {
    resetStore();
    const specs = [
      { id: 'party-theme', label: '派对主题', kind: 'text' as const, required: true, pinEligible: false },
      { id: 'party-budget', label: '预算金额', kind: 'number' as const, required: true, pinEligible: false },
      { id: 'party-guests', label: '宾客人数', kind: 'number' as const, required: true, pinEligible: false },
    ];
    const inputs: WorkInput[] = specs.map((spec, index) => ({
      id: `party-input-${index + 1}`,
      workId: WORK_ID,
      runId: 'run-party',
      taskId: 'task-party',
      blockId: 'party-inputs',
      specId: spec.id,
      value: index === 0 ? '武侠主题' : index === 1 ? 4500 : 15,
      state: 'requested',
      revision: 1,
      updatedAt: '2026-07-24T16:00:00Z',
    }));
    seedStore(
      [makeTask({
        id: 'task-party',
        runId: 'run-party',
        nodeId: 'node-party',
        title: '策划派对',
        state: 'waiting_input',
        waitingInputIds: inputs.map((input) => input.id),
      })],
      {
        ...makeDefinition([makeNodeDef({
          id: 'node-party',
          title: '策划派对',
          blockIds: ['party-inputs'],
          inputSpecIds: specs.map((spec) => spec.id),
        })]),
        inputSpecs: specs,
      },
      inputs,
    );
    const groupSubmits: SubmitWorkInputRequest[] = [];
    let groupRefreshCalls = 0;
    const committedSubmit = async (request: SubmitWorkInputRequest): Promise<SubmitInputResult> => {
      groupSubmits.push(request);
      return {
        revision: request.expectedRevision + 1,
        duplicate: false,
        committed: true,
        recoverable: false,
      };
    };
    const committedPin = async (request: SetInputCornerstoneRequest): Promise<CornerstonePinResult> => ({
      pinned: request.pin, revision: 2, duplicate: false, committed: true, recoverable: false,
    });
    const { host, cleanup } = await mount(
      <ExecutionList
        workId={WORK_ID}
        runId="run-party"
        expandedTaskId="task-party"
        onSubmitWorkInput={committedSubmit}
        onSetCornerstone={committedPin}
        onUnsetCornerstone={committedPin}
        onRefreshAuthoritative={async () => { groupRefreshCalls++; }}
      />,
    );
    const group = host.querySelector<HTMLElement>('[data-input-focus-group="task-party"]');
    const theme = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-party-party-theme"]');
    const budget = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-party-party-budget"]');
    const guests = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-party-party-guests"]');
    ok(group?.classList.contains('wg2-eb-inputs--editable'), 'input group: one compact editable Block');
    ok(Boolean(theme && !theme.disabled), 'input group: text field is editable');
    eq(
      host.querySelectorAll('[data-testid^="work-input-submit-"]').length,
      0,
      'input group: field-level submit buttons are omitted',
    );
    eq(
      host.querySelectorAll('[data-testid="expanded-block-submit-task-party"]').length,
      1,
      'input group: Block has exactly one submit button',
    );
    if (theme) {
      eq(theme.value, '武侠主题', 'input group: persisted draft is rendered');
      ok(
        !host.querySelector<HTMLButtonElement>('[data-testid="expanded-block-submit-task-party"]')?.disabled,
        'input group: shared submit enables after every field is valid',
      );
      theme.focus();
      await interact(() => theme.dispatchEvent(new KeyboardEvent(
        'keydown',
        { key: 'Tab', bubbles: true, cancelable: true },
      )));
      eq(document.activeElement, budget, 'input group: Tab advances to next field');
    }
    if (budget) {
      await interact(() => budget.dispatchEvent(new KeyboardEvent(
        'keydown',
        { key: 'Tab', bubbles: true, cancelable: true },
      )));
      eq(document.activeElement, guests, 'input group: second Tab stays in the same Block');
    }
    if (guests) {
      await interact(() => guests.dispatchEvent(new KeyboardEvent(
        'keydown',
        { key: 'Tab', bubbles: true, cancelable: true },
      )));
      ok(!group?.contains(document.activeElement), 'input group: final Tab leaves the Block');
    }
    await interact(() =>
      host.querySelector<HTMLButtonElement>('[data-testid="expanded-block-submit-task-party"]')?.click());
    await settle();
    eq(groupSubmits.length, 3, 'input group: one click submits every field');
    eq(groupSubmits[1]?.expectedRevision, groupSubmits[0]?.expectedRevision + 1, 'input group: second submit uses advanced revision');
    eq(groupSubmits[2]?.expectedRevision, groupSubmits[1]?.expectedRevision + 1, 'input group: third submit uses advanced revision');
    eq(groupRefreshCalls, 1, 'input group: authoritative state refreshes once after the full Block');
    const focusSink = document.createElement('button');
    document.body.appendChild(focusSink);
    focusSink.focus();
    await settle();
    await cleanup();
    focusSink.remove();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 16. Failed task shows error and retry button
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({
      id: 't-fail',
      nodeId: 'n1',
      title: '失败任务',
      state: 'failed_retryable',
      error: '磁盘空间不足',
      retryable: true,
    })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1', title: '失败任务' })]));

    const retryCalls: Array<{ workId: string; taskId: string; runId: string }> = [];
    const { host, cleanup } = await mount(
      <ExecutionList workId={WORK_ID} expandedTaskId="t-fail" onRetryTask={(i) => { retryCalls.push(i); }} />,
    );

    // Row level retry button
    const rowRetry = host.querySelector<HTMLButtonElement>('[data-testid="execution-row-retry-t-fail"]');
    ok(rowRetry !== null, 'failed: row retry button visible');
    contains(rowRetry?.textContent ?? '', '重试', 'failed: row retry text');

    // Expanded block retry button
    const expRetry = host.querySelector<HTMLButtonElement>('[data-testid="expanded-block-retry-t-fail"]');
    ok(expRetry !== null, 'failed: expanded retry button visible');

    // Error block in expanded area
    const err = host.querySelector('[data-testid="expanded-block-error-t-fail"]');
    ok(err !== null, 'failed: error block visible');
    eq(err?.getAttribute('role'), 'alert', 'failed: error role=alert');
    contains(err?.textContent ?? '', '磁盘空间不足', 'failed: error message');

    // Click retry
    await interact(() => expRetry?.click());
    eq(retryCalls.length, 1, 'failed: retry intent fired');
    eq(retryCalls[0]?.taskId, 't-fail', 'failed: correct taskId');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 17. Non-retryable failed task has no retry button
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({
      id: 't-term',
      nodeId: 'n1',
      title: '致命失败',
      state: 'failed_terminal',
      error: '不可恢复错误',
      retryable: false,
    })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1' })]));
    const { host, cleanup } = await mount(
      <ExecutionList workId={WORK_ID} expandedTaskId="t-term" />,
    );

    const rowRetry = host.querySelector('[data-testid="execution-row-retry-t-term"]');
    ok(rowRetry === null, 'nonretryable: no row retry button');
    const expRetry = host.querySelector('[data-testid="expanded-block-retry-t-term"]');
    ok(expRetry === null, 'nonretryable: no expanded retry button');
    // Error still shown
    const err = host.querySelector('[data-testid="expanded-block-error-t-term"]');
    ok(err !== null, 'nonretryable: error still visible');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 18. Running task has progress bar with ARIA
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-prog', nodeId: 'n1', title: '进度任务', state: 'running', progress: '0.73' })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1' })]));
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);

    const bar = host.querySelector('[role="progressbar"]');
    ok(bar !== null, 'progress: progressbar role exists');
    eq(bar?.getAttribute('aria-valuenow'), '73', 'progress: aria-valuenow=73');
    eq(bar?.getAttribute('aria-valuemin'), '0', 'progress: aria-valuemin=0');
    eq(bar?.getAttribute('aria-valuemax'), '100', 'progress: aria-valuemax=100');

    // No progress bar for non-running
    await cleanup();
    resetStore();
    const tasks2 = [makeTask({ id: 't-noprog', nodeId: 'n1', title: '无进度', state: 'pending', progress: '0.5' })];
    seedStore(tasks2, makeDefinition([makeNodeDef({ id: 'n1' })]));
    const { host: host2, cleanup: cleanup2 } = await mount(<ExecutionList workId={WORK_ID} />);
    const bar2 = host2.querySelector('[role="progressbar"]');
    ok(bar2 === null, 'progress: no progressbar for pending task');
    await cleanup2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 19. ExpandedBlock has region role and aria-label
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-region', nodeId: 'n1', title: 'Region 测试' })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1', title: 'Region 测试' })]));
    const { host, cleanup } = await mount(
      <ExecutionList workId={WORK_ID} expandedTaskId="t-region" />,
    );

    const panel = host.querySelector('[data-testid="expanded-block-t-region"]');
    eq(panel?.getAttribute('role'), 'region', 'region: role=region');
    ok(panel?.getAttribute('aria-label')?.includes('Region 测试') ?? false, 'region: aria-label includes title');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 20. Golden data — consume real Go full golden
  // ════════════════════════════════════════════════════════════════════════
  {
    const view = goldenView as Record<string, unknown>;
    const tasks = view.tasks as TaskV2View[] | undefined;
    const definition = view.definition as WorkDefinitionRevision | undefined;

    ok(Array.isArray(tasks) && tasks.length > 0, 'golden: real Go fixture contains tasks');
    resetStore();
    seedStore(tasks ?? [], definition);
    const { host, cleanup } = await mount(<ExecutionList workId={WORK_ID} />);
    const items = host.querySelectorAll('[data-testid^="execution-list-item-"]');
    eq(items.length, tasks?.length ?? 0, `golden: ${tasks?.length ?? 0} items rendered`);
    const firstId = tasks?.[0]?.id;
    const row = firstId ? host.querySelector(`[data-testid="execution-row-${firstId}"]`) : null;
    ok(row !== null, `golden: first row (${firstId ?? 'missing'}) rendered`);
    ok(row?.getAttribute('data-task-state') !== null, 'golden: first row has data-task-state');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 21. CSS classes present (basic style check)
  // ════════════════════════════════════════════════════════════════════════
  {
    resetStore();
    const tasks = [makeTask({ id: 't-css', nodeId: 'n1', title: 'CSS 测试' })];
    seedStore(tasks, makeDefinition([makeNodeDef({ id: 'n1' })]));
    const { host, cleanup } = await mount(
      <ExecutionList workId={WORK_ID} expandedTaskId="t-css" />,
    );

    ok(host.querySelector('.wg2-el-list') !== null, 'css: list class present');
    ok(host.querySelector('.wg2-er-row') !== null, 'css: row class present');
    ok(host.querySelector('.wg2-er-header') !== null, 'css: header class present');
    ok(host.querySelector('.wg2-er-icon') !== null, 'css: icon class present');
    ok(host.querySelector('.wg2-er-badge') !== null, 'css: badge class present');
    ok(host.querySelector('.wg2-eb-panel') !== null, 'css: panel class present');
    await cleanup();
  }

  // Discussion operation epoch: a late apply from block A cannot mutate the
  // drawer for block B. A committed transport recovery remains explicit and
  // awaits an authoritative refresh whose own failure is visible.
  {
    resetStore();
    const task = makeTask({ id: 'task-without-block', nodeId: 'node-without-block' });
    const definition = makeDefinition([
      makeNodeDef({ id: 'node-without-block' }),
    ]);
    seedStore([task], definition);
    const blockId = v2DiscussionBlockId('node-without-block');
    const sessionId = 'session-no-block';
    const draftKey = [
      WORK_ID,
      task.runId,
      sessionId,
      task.id,
      blockId,
      definition.revision,
      1,
    ].join('\u0000');
    useWorkUIStore.getState().setDiscussionDraft(WORK_ID, draftKey, '首次讨论');
    let previewBlock: { id: string; revision: number } | undefined;
    const { host, cleanup } = await mount(
      <ExecutionList
        workId={WORK_ID}
        runId={task.runId}
        sessionId={sessionId}
        expandedTaskId={task.id}
        blocks={[]}
        onPreviewPatch={async (intent) => {
          previewBlock = { id: intent.blockId, revision: intent.blockRevision };
          return {
            revision: 1,
            duplicate: false,
            committed: false,
            recoverable: true,
            error: 'fixture stop',
          };
        }}
        onApplyPatch={async () => ({
          workRevision: 1,
          newRevision: 1,
          requiresRerun: false,
          duplicate: false,
          committed: false,
          recoverable: true,
        })}
        onRefreshAuthoritative={async () => {}}
      />,
    );
    ok(
      Boolean(host.querySelector(`[data-testid="execution-row-discuss-${task.id}"]`)),
      'discussion identity: task row keeps the single discussion entry',
    );
    ok(
      !host.querySelector(`[data-testid="expanded-block-discuss-${task.id}"]`),
      'discussion identity: expanded Block does not duplicate discussion entry',
    );
    await interact(() =>
      host.querySelector<HTMLButtonElement>(`[data-testid="execution-row-discuss-${task.id}"]`)?.click());
    await interact(() =>
      document.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${task.id}"]`)?.click());
    eq(previewBlock?.id, blockId, 'discussion identity: legacy node sends its stable derived Block ID');
    eq(previewBlock?.revision, 1, 'discussion identity: first materialization starts at Block revision 1');
    eq(v2DiscussionBlockId('中文'), 'v2-node-e4b8ade69687', 'discussion identity: Go/TS UTF-8 ID contract');
    await cleanup();
  }

  {
    resetStore();
    const spec = { id: 'discussion-spec', label: '讨论输入', kind: 'text' as const, required: true, pinEligible: false };
    const task = makeTask({
      id: 'task-real-block',
      nodeId: 'node-real-block',
      state: 'waiting_input',
      waitingInputIds: ['input-real-block'],
    });
    const definition = {
      ...makeDefinition([
        makeNodeDef({
          id: task.nodeId,
          blockIds: ['declared-block'],
          inputSpecIds: [spec.id],
        }),
      ]),
      inputSpecs: [spec],
    };
    const input: WorkInput = {
      id: 'input-real-block',
      workId: WORK_ID,
      runId: task.runId,
      taskId: task.id,
      blockId: 'input-block',
      specId: spec.id,
      value: null,
      state: 'requested',
      revision: 99,
      updatedAt: '2026-07-24T16:00:00Z',
    };
    const blocks = [
      makeBlock('declared-block', 3, '声明 Block'),
      makeBlock('input-block', 7, '输入 Block'),
    ];
    seedStore([task], definition, [input]);
    const sessionId = 'session-real-block';
    const draftKey = [
      WORK_ID,
      task.runId,
      sessionId,
      task.id,
      input.blockId,
      definition.revision,
      blocks[1].revision,
    ].join('\u0000');
    useWorkUIStore.getState().setDiscussionDraft(WORK_ID, draftKey, '更新这个 Block');
    let previewIntent: { blockId: string; blockRevision: number } | undefined;
    const { host, cleanup } = await mount(
      <ExecutionList
        workId={WORK_ID}
        runId={task.runId}
        sessionId={sessionId}
        expandedTaskId={task.id}
        blocks={blocks}
        onPreviewPatch={async (intent) => {
          previewIntent = intent;
          return {
            revision: 1,
            duplicate: false,
            committed: false,
            recoverable: true,
            error: 'fixture stop',
          };
        }}
        onApplyPatch={async () => ({
          workRevision: 1,
          newRevision: 1,
          requiresRerun: false,
          duplicate: false,
          committed: false,
          recoverable: true,
        })}
        onRefreshAuthoritative={async () => {}}
      />,
    );
    await interact(() =>
      host.querySelector<HTMLButtonElement>(`[data-testid="execution-row-discuss-${task.id}"]`)?.click());
    await interact(() =>
      document.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${task.id}"]`)?.click());
    eq(previewIntent?.blockId, 'input-block', 'discussion identity: bound input Block wins over declared Block');
    eq(previewIntent?.blockRevision, 7, 'discussion identity: preview uses authoritative Block revision');
    ok(previewIntent?.blockId !== task.id, 'discussion identity: taskId is never used as blockId');
    await cleanup();
  }

  {
    resetStore();
    const definition = makeDefinition([
      makeNodeDef({ id: 'node-a', blockIds: ['block-a'] }),
      makeNodeDef({ id: 'node-b', blockIds: ['block-b'] }),
    ]);
    const tasks = [
      makeTask({ id: 'task-a', runId: 'active-run', nodeId: 'node-a', title: 'A' }),
      makeTask({ id: 'task-b', runId: 'active-run', nodeId: 'node-b', title: 'B' }),
    ];
    seedStore(tasks, definition);
    const sessionId = 'session-disc';
    for (const [taskId, blockId] of [['task-a', 'block-a'], ['task-b', 'block-b']]) {
      const key = [WORK_ID, 'active-run', sessionId, taskId, blockId, definition.revision, 1].join('\u0000');
      useWorkUIStore.getState().setDiscussionDraft(WORK_ID, key, `draft-${taskId}`);
    }
    const makePreview = (taskId: string, blockId: string): WorkPatchPreview => ({
      id: `patch-${taskId}`,
      workId: WORK_ID,
      runId: 'active-run',
      taskId,
      blockId,
      sessionId,
      baseDefinitionRev: definition.revision,
      baseBlockRev: 1,
      scope: 'block',
      operations: [{ op: 'replace', path: `blocks/${blockId}/title`, newValue: 'changed' }],
      affectedNodeIds: [],
      affectedBlockIds: [blockId],
      affectedArtifactSlotIds: ['slot-affected'],
      staleArtifactSlotIds: ['slot-stale'],
      invalidatedTaskIds: [],
      requiresRerun: false,
      digest: `digest-${taskId}`,
      expiresAt: '2099-07-24T00:00:00Z',
    });
    const lateA = deferred<ApplyWorkPatchResult>();
    const onPreviewPatch = async (intent: { taskId: string; blockId: string }): Promise<PreviewWorkPatchResult> => ({
      preview: makePreview(intent.taskId, intent.blockId),
      revision: 1,
      duplicate: false,
      committed: true,
      recoverable: false,
    });
    let refreshCalls = 0;
    let returnMissingReceipt = false;
    const onApplyPatch = async (intent: { patchId: string; requestId: string }): Promise<ApplyWorkPatchResult> => {
      if (intent.patchId === 'patch-task-a') return lateA.promise;
      if (returnMissingReceipt) {
        return {
          workRevision: 4,
          newRevision: 3,
          affectedArtifactSlotIds: ['slot-affected'],
          staleArtifactSlotIds: ['slot-stale'],
          requiresRerun: false,
          duplicate: false,
          committed: true,
          recoverable: false,
        };
      }
      return {
        workRevision: 3,
        newRevision: 2,
        affectedArtifactSlotIds: ['slot-affected'],
        staleArtifactSlotIds: ['slot-stale'],
        requiresRerun: false,
        duplicate: false,
        committed: true,
        recoverable: true,
        transportError: {
          code: 'committed_recovery',
          message: 'ACK 丢失',
          operation: 'ApplyWorkPatch',
          committed: true,
          recoverable: true,
        },
        receipt: {
          requestId: intent.requestId,
          operation: 'patch.apply',
          intentDigest: 'intent-task-b',
          patchId: intent.patchId,
          resultRevision: 2,
          resultDigest: 'result-task-b',
          affectedArtifactSlotIds: ['slot-affected'],
          staleArtifactSlotIds: ['slot-stale'],
          requiresRerun: false,
          createdAt: '2026-07-24T00:00:00Z',
        },
      };
    };
    const props = {
      workId: WORK_ID,
      runId: 'active-run',
      sessionId,
      workRevision: 2,
      blocks: [makeBlock('block-a'), makeBlock('block-b')],
      onPreviewPatch,
      onApplyPatch,
      onRefreshAuthoritative: async () => {
        refreshCalls++;
        throw new Error('刷新不可用');
      },
    };
    const { host, root, cleanup } = await mount(<ExecutionList {...props} expandedTaskId="task-a" />);
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="execution-row-discuss-task-a"]')?.click());
    await interact(() => document.querySelector<HTMLButtonElement>('[data-testid="discussion-preview-btn-task-a"]')?.click());
    await interact(() => document.querySelector<HTMLButtonElement>('[data-testid="discussion-apply-btn-task-a"]')?.click());
    await act(async () => { root.render(<ExecutionList {...props} expandedTaskId="task-b" />); });
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="execution-row-discuss-task-b"]')?.click());
    lateA.resolve({
      workRevision: 3,
      newRevision: 2,
      requiresRerun: false,
      duplicate: false,
      committed: true,
      recoverable: false,
    });
    await settle();
    ok(document.querySelector('[data-testid="discussion-result-task-b"]') === null, 'discussion epoch: late apply cannot pollute another full identity');
    eq(refreshCalls, 0, 'discussion epoch: late apply cannot trigger authoritative refresh');

    await interact(() => document.querySelector<HTMLButtonElement>('[data-testid="discussion-preview-btn-task-b"]')?.click());
    await interact(() => document.querySelector<HTMLButtonElement>('[data-testid="discussion-apply-btn-task-b"]')?.click());
    await settle();
    contains(
      document.querySelector('[data-testid="discussion-result-task-b"]')?.textContent ?? '',
      '改动已提交',
      'discussion recovery: transport-only committed result is explicit',
    );
    contains(
      document.querySelector('[data-testid="discussion-error-task-b"]')?.textContent ?? '',
      '刷新最新状态失败',
      'discussion recovery: awaited refresh failure is explicit',
    );
    ok(!document.querySelector('[data-testid="discussion-receipt-task-b"]'), 'discussion recovery: technical receipt stays hidden');
    eq(refreshCalls, 1, 'discussion recovery: committed result refreshes exactly once');

    await interact(() => document.querySelector<HTMLButtonElement>('[data-testid="discussion-close-task-b"]')?.click());
    returnMissingReceipt = true;
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="execution-row-discuss-task-b"]')?.click());
    await interact(() => document.querySelector<HTMLButtonElement>('[data-testid="discussion-preview-btn-task-b"]')?.click());
    await interact(() => document.querySelector<HTMLButtonElement>('[data-testid="discussion-apply-btn-task-b"]')?.click());
    await settle();
    contains(
      document.querySelector('[data-testid="discussion-error-task-b"]')?.textContent ?? '',
      '确认响应不完整',
      'discussion receipt contract: incomplete confirmation is explicit',
    );
    contains(
      document.querySelector('[data-testid="discussion-error-task-b"]')?.textContent ?? '',
      '刷新最新状态失败',
      'discussion receipt contract: missing receipt still awaits refresh',
    );
    ok(
      !document.querySelector('[data-testid="discussion-receipt-task-b"]'),
      'discussion receipt contract: client request/revision are not fabricated as receipt',
    );
    eq(refreshCalls, 2, 'discussion receipt contract: missing receipt refreshes exactly once');
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
