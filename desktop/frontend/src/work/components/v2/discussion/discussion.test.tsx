import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import type { PatchScope } from '../../../types_v2';
import { DiscussionDrawer } from './DiscussionDrawer';
import type {
  DiscussionApplyIntent,
  DiscussionCloseIntent,
  DiscussionDraftIntent,
  DiscussionPreviewIntent,
} from './DiscussionDrawer';

let passed = 0;
let failed = 0;

function ok(condition: boolean | undefined | null, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else failed++;
}

function eq<T>(actual: T, expected: T, label: string): void {
  ok(actual === expected, `${label}${actual === expected ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

const dom = new JSDOM('<!doctype html><html><body></body></html>', {
  pretendToBeVisual: true,
  url: 'http://localhost/',
});
Object.defineProperties(dom.window.HTMLElement.prototype, {
  attachEvent: { configurable: true, value: () => undefined },
  detachEvent: { configurable: true, value: () => undefined },
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
  HTMLButtonElement: dom.window.HTMLButtonElement,
  SVGElement: dom.window.SVGElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
  KeyboardEvent: dom.window.KeyboardEvent,
  MutationObserver: dom.window.MutationObserver,
  requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
  cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
});
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });

interface Mounted {
  host: HTMLElement;
  root: Root;
  container: HTMLDivElement;
  cleanup: () => Promise<void>;
}

async function mount(element: React.ReactElement): Promise<Mounted> {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => { root.render(element); });
  return {
    host: document.body,
    root,
    container,
    cleanup: async () => {
      await act(async () => root.unmount());
      container.remove();
    },
  };
}

async function interact(action: () => void): Promise<void> {
  await act(async () => {
    action();
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
  });
}

const WORK_ID = 'work-dd';
const TASK_ID = 'task-dd';
const BLOCK_ID = 'block-dd';

interface Harness {
  draft: string;
  scope: PatchScope;
  previews: DiscussionPreviewIntent[];
  applies: DiscussionApplyIntent[];
  closes: DiscussionCloseIntent[];
  drafts: DiscussionDraftIntent[];
}

function harness(): Harness {
  return { draft: '', scope: 'block', previews: [], applies: [], closes: [], drafts: [] };
}

function drawer(
  state: Harness,
  overrides: Partial<Parameters<typeof DiscussionDrawer>[0]> = {},
) {
  return (
    <DiscussionDrawer
      workId={WORK_ID}
      taskId={TASK_ID}
      blockId={BLOCK_ID}
      runId="run-dd"
      sessionId="session-dd"
      workRevision={9}
      definitionRevision={3}
      blockRevision={2}
      taskTitle="逻辑审查"
      draftText={state.draft}
      onDraftChange={(intent) => {
        state.draft = intent.text;
        state.drafts.push(intent);
      }}
      patchPreview={null}
      isPreviewing={false}
      previewError={null}
      isApplying={false}
      applyResult={null}
      applyError={null}
      selectedScope={state.scope}
      onScopeChange={(scope) => { state.scope = scope; }}
      revisionConflict={false}
      digestConflict={false}
      onClose={(intent) => state.closes.push(intent)}
      onPreview={(intent) => state.previews.push(intent)}
      onApply={(intent) => state.applies.push(intent)}
      onDismissResult={() => undefined}
      {...overrides}
    />
  );
}

async function run(): Promise<void> {
  {
    const state = harness();
    const mounted = await mount(drawer(state));
    const dialog = mounted.host.querySelector(`[data-testid="discussion-drawer-${TASK_ID}"]`);
    const submit = mounted.host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    ok(dialog?.getAttribute('role') === 'dialog', '基础：讨论抽屉是对话框');
    ok(dialog?.textContent?.includes('你希望怎么调整？'), '基础：只要求用户描述目标');
    eq(submit?.textContent?.trim(), '提交修改', '基础：唯一主动作是提交修改');
    eq(submit?.disabled, true, '基础：空内容不能提交');
    ok(!mounted.host.querySelector(`[data-testid="discussion-apply-btn-${TASK_ID}"]`), '基础：不显示应用按钮');
    ok(!mounted.host.querySelector(`[data-testid="patch-preview-${TASK_ID}"]`), '基础：不显示技术预览');
    ok(!mounted.host.querySelector(`[data-testid="discussion-retry-preview-${TASK_ID}"]`), '基础：不显示重试按钮');
    await mounted.cleanup();
  }

  {
    const state = harness();
    state.draft = '把结论写得更简洁';
    const mounted = await mount(drawer(state));
    await interact(() => mounted.host.querySelector<HTMLButtonElement>(
      `[data-testid="discussion-preview-btn-${TASK_ID}"]`,
    )?.click());
    eq(state.previews.length, 1, '提交：只发出一次协调意图');
    eq(state.applies.length, 0, '提交：用户侧不发出应用动作');
    eq(state.previews[0]?.instruction, state.draft, '提交：保留用户原始要求');
    eq(state.previews[0]?.scope, 'block', '提交：保留调整范围');
    ok(state.previews[0]?.requestId.startsWith('disc-coordinate-'), '提交：协调请求具有稳定身份');
    await mounted.cleanup();
  }

  {
    const state = harness();
    state.draft = '同步修改后续步骤';
    const mounted = await mount(drawer(state));
    const workflow = mounted.host.querySelector<HTMLInputElement>(
      `[data-testid="discussion-scope-workflow-${TASK_ID}"]`,
    );
    await interact(() => workflow?.click());
    eq(state.scope, 'workflow', '范围：用户可以选择同步后续工作');
    await mounted.cleanup();
  }

  {
    const state = harness();
    state.draft = '调整内容';
    const mounted = await mount(drawer(state, { isPreviewing: true }));
    const submit = mounted.host.querySelector<HTMLButtonElement>(
      `[data-testid="discussion-preview-btn-${TASK_ID}"]`,
    );
    eq(submit?.disabled, true, '处理中：禁止重复提交');
    ok(submit?.textContent?.includes('AI 正在协调更新'), '处理中：呈现业务状态');
    ok(mounted.host.textContent.includes('安排需要更新的内容与后续步骤'), '处理中：说明协调器正在工作');
    await mounted.cleanup();
  }

  {
    const state = harness();
    state.draft = '调整内容';
    const mounted = await mount(drawer(state, { applyError: '缺少目标格式，请补充要求。' }));
    const error = mounted.host.querySelector(`[data-testid="discussion-error-${TASK_ID}"]`);
    ok(error?.textContent?.includes('缺少目标格式'), '失败：明确展示需要用户补充的信息');
    ok(!error?.querySelector('button'), '失败：不把技术重试交给用户');
    await mounted.cleanup();
  }

  {
    const state = harness();
    state.draft = '键盘提交';
    const mounted = await mount(drawer(state));
    const dialog = mounted.host.querySelector<HTMLElement>(`[data-testid="discussion-drawer-${TASK_ID}"]`);
    await interact(() => dialog?.dispatchEvent(new KeyboardEvent('keydown', {
      key: 'Enter',
      ctrlKey: true,
      bubbles: true,
    })));
    eq(state.previews.length, 1, '键盘：Ctrl+Enter 提交协调');
    await interact(() => dialog?.dispatchEvent(new KeyboardEvent('keydown', {
      key: 'Escape',
      bubbles: true,
    })));
    eq(state.closes.length, 1, '键盘：Escape 关闭');
    await mounted.cleanup();
  }

  process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

run().catch((error) => {
  console.error(error);
  process.exit(2);
});
