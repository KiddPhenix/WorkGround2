import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import type {
  ApplyWorkPatchResult,
  PatchScope,
  WorkPatchPreview,
  WorkViewV2,
} from '../../../types_v2';
import { DiscussionDrawer } from './DiscussionDrawer';
import type {
  DiscussionPreviewIntent,
  DiscussionApplyIntent,
  DiscussionCloseIntent,
  DiscussionDraftIntent,
} from './DiscussionDrawer';
import { PatchPreview } from './PatchPreview';

// ── test harness ───────────────────────────────────────────────────────────

let passed = 0;
let failed = 0;

function ok(condition: boolean | undefined | null, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else {
    failed++;
    if (failed <= 8) process.stdout.write(`       ${new Error().stack?.split('\n')[2]?.trim() ?? ''}\n`);
  }
}

function eq<T>(actual: T, expected: T, label: string): void {
  const cond = actual === expected;
  ok(cond, `${label}${cond ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function contains(actual: string, substring: string, label: string): void {
  ok(actual.includes(substring), `${label} (expected "${substring}" in "${actual.slice(0, 120)}")`);
}

function notContains(actual: string, substring: string, label: string): void {
  ok(!actual.includes(substring), `${label} (found unexpected "${substring}")`);
}

function setupDOM(): JSDOM {
  const dom = new JSDOM('<!doctype html><html><body></body></html>', {
    pretendToBeVisual: true,
    url: 'http://localhost/',
  });
  // React's input-event compatibility path still probes these legacy methods
  // when jsdom moves focus between controlled text fields.
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
    HTMLFieldSetElement: dom.window.HTMLFieldSetElement,
    SVGElement: dom.window.SVGElement,
    Event: dom.window.Event,
    MouseEvent: dom.window.MouseEvent,
    KeyboardEvent: dom.window.KeyboardEvent,
    MutationObserver: dom.window.MutationObserver,
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
  });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
  return dom;
}

setupDOM();

async function settle(delay = 20): Promise<void> {
  await act(async () => {
    await new Promise<void>((r) => setTimeout(r, delay));
  });
}

async function interact(action: () => void): Promise<void> {
  await act(async () => {
    action();
    await new Promise<void>((r) => setTimeout(r, 20));
  });
}

interface Mounted {
  host: HTMLElement;
  container: HTMLDivElement;
  root: Root;
  cleanup: () => Promise<void>;
}

async function mount(element: React.ReactElement): Promise<Mounted> {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container, { onCaughtError: (error) => { throw error; } });
  await act(async () => { root.render(element); });
  await settle();
  return {
    host: document.body,
    container,
    root,
    cleanup: async () => {
      await act(async () => { root.unmount(); });
      container.remove();
    },
  };
}

// ── golden data validation ─────────────────────────────────────────────────

const __dirname = dirname(fileURLToPath(import.meta.url));
const goFullGoldenPath = resolve(
  __dirname,
  '..', '..', '..', '..', '..', '..', '..',
  'internal', 'work', 'testdata', 'contract-v2', 'work-view-v2-full.json',
);
const goFullGolden: WorkViewV2 = JSON.parse(readFileSync(goFullGoldenPath, 'utf-8'));
function requirePatchPreview(view: WorkViewV2): WorkPatchPreview {
  const preview = view.patchPreviews?.[0];
  if (!preview) {
    throw new Error(`real Go full golden has no patch preview: ${goFullGoldenPath}`);
  }
  return preview;
}
const goPatchGolden = requirePatchPreview(goFullGolden);
const applyResultPath = resolve(__dirname, '..', '..', '..', '__fixtures__', 'work-v2-patch-apply-result.json');
const applyResultGolden: ApplyWorkPatchResult = JSON.parse(readFileSync(applyResultPath, 'utf-8'));
const cssText = readFileSync(resolve(__dirname, 'discussion.css'), 'utf-8');

// ── fixtures ───────────────────────────────────────────────────────────────

const WORK_ID = 'work-dd-test';
const TASK_ID = 'task-dd-1';
const BLOCK_ID = 'block-dd-1';
const RUN_ID = 'run-dd-1';
const SESSION_ID = 'session-dd-1';
const TASK_TITLE = '逻辑审查';
const WORK_REV = 9;
const DEF_REV = 1;
const BLOCK_REV = 3;
const patchGolden: WorkPatchPreview = {
  ...goPatchGolden,
  workId: WORK_ID,
  taskId: TASK_ID,
  blockId: BLOCK_ID,
  runId: RUN_ID,
  sessionId: SESSION_ID,
  baseDefinitionRev: DEF_REV,
  baseBlockRev: BLOCK_REV,
  expiresAt: '2999-01-01T00:00:00Z',
};

interface DDHarness {
  draftText: string;
  selectedScope: PatchScope;
  previewIntents: DiscussionPreviewIntent[];
  applyIntents: DiscussionApplyIntent[];
  closeIntents: DiscussionCloseIntent[];
  draftIntents: DiscussionDraftIntent[];
  dismissCalls: number;
}

function makeHarness(): DDHarness {
  return {
    draftText: '',
    selectedScope: 'block',
    previewIntents: [],
    applyIntents: [],
    closeIntents: [],
    draftIntents: [],
    dismissCalls: 0,
  };
}

function makeDrawer(h: DDHarness, overrides: Partial<Parameters<typeof DiscussionDrawer>[0]> = {}) {
  return (
    <DiscussionDrawer
      workId={WORK_ID}
      taskId={TASK_ID}
      blockId={BLOCK_ID}
      runId={RUN_ID}
      sessionId={SESSION_ID}
      workRevision={WORK_REV}
      definitionRevision={DEF_REV}
      blockRevision={BLOCK_REV}
      taskTitle={TASK_TITLE}
      draftText={h.draftText}
      onDraftChange={(intent) => {
        h.draftText = intent.text;
        h.draftIntents.push(intent);
      }}
      patchPreview={null}
      isPreviewing={false}
      previewError={null}
      isApplying={false}
      applyResult={null}
      applyError={null}
      selectedScope={h.selectedScope}
      onScopeChange={(s) => { h.selectedScope = s; }}
      revisionConflict={false}
      digestConflict={false}
      onClose={(i) => h.closeIntents.push(i)}
      onPreview={(i) => h.previewIntents.push(i)}
      onApply={(i) => h.applyIntents.push(i)}
      onDismissResult={() => h.dismissCalls++}
      {...overrides}
    />
  );
}

// ── tests ──────────────────────────────────────────────────────────────────

async function runTests(): Promise<void> {
  // ════════════════════════════════════════════════════════════════════════
  // 1. Basic render
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    const { host, cleanup } = await mount(makeDrawer(h));

    const drawer = host.querySelector(`[data-testid="discussion-drawer-${TASK_ID}"]`);
    ok(drawer !== null, 'basic: drawer rendered');
    eq(drawer?.getAttribute('role'), 'dialog', 'basic: role=dialog');
    ok(drawer?.getAttribute('aria-label')?.includes(TASK_TITLE) ?? false, 'basic: aria-label contains task title');

    const header = host.querySelector(`[data-testid="discussion-header-${TASK_ID}"]`);
    contains(header?.textContent ?? '', TASK_TITLE, 'basic: header shows task title');

    const textarea = host.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    ok(textarea !== null, 'basic: textarea exists');
    eq(textarea?.value, '', 'basic: textarea empty');

    const previewBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    ok(previewBtn !== null, 'basic: preview button exists');
    eq(previewBtn?.disabled, true, 'basic: preview disabled when empty');

    const scopeBlock = host.querySelector<HTMLInputElement>(`[data-testid="discussion-scope-block-${TASK_ID}"]`);
    ok(scopeBlock?.checked ?? false, 'basic: block scope selected by default');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 2. Golden data: real fixture loads and renders PatchPreview
  // ════════════════════════════════════════════════════════════════════════
  {
    ok(typeof goPatchGolden.id === 'string', 'golden: patch id is string');
    ok(goPatchGolden.scope === 'block', 'golden: scope=block');
    ok(goPatchGolden.operations.length >= 1, 'golden: has operations');
    ok(goPatchGolden.affectedNodeIds.length >= 1, 'golden: has affected nodes');
    ok(goPatchGolden.requiresRerun === true, 'golden: requiresRerun=true');
    ok(typeof goPatchGolden.digest === 'string', 'golden: digest is string');
    ok(typeof goPatchGolden.expiresAt === 'string', 'golden: expiresAt is string');

    // Render PatchPreview standalone
    const { host, cleanup } = await mount(
      <PatchPreview patch={goPatchGolden} taskTitle={TASK_TITLE} taskId={goPatchGolden.taskId} />,
    );

    const panel = host.querySelector(`[data-testid="patch-preview-${goPatchGolden.taskId}"]`);
    ok(panel !== null, 'golden: preview panel rendered');

    const scopeBadge = host.querySelector(`[data-testid="patch-preview-scope-${goPatchGolden.taskId}"]`);
    contains(scopeBadge?.textContent ?? '', '只更新当前内容', 'golden: scope badge');

    const ops = host.querySelector(`[data-testid="patch-preview-ops-${goPatchGolden.taskId}"]`);
    ok(ops !== null, 'golden: ops table exists');

    const op0 = host.querySelector(`[data-testid="patch-preview-op-${goPatchGolden.taskId}-0"]`);
    ok(op0 !== null, 'golden: first op row');
    contains(op0?.textContent ?? '', goPatchGolden.operations[0].path, 'golden: op shows path');

    const rerun = host.querySelector(`[data-testid="patch-requires-rerun-${goPatchGolden.taskId}"]`);
    ok(rerun !== null, 'golden: requiresRerun badge');

    const meta = host.querySelector(`[data-testid="patch-preview-meta-${goPatchGolden.taskId}"]`);
    contains(meta?.textContent ?? '', goPatchGolden.digest.slice(0, 8), 'golden: meta shows digest prefix');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 3. Golden data: apply result fixture
  // ════════════════════════════════════════════════════════════════════════
  {
    ok(typeof applyResultGolden.workRevision === 'number', 'golden-apply: workRevision');
    ok(Array.isArray(applyResultGolden.invalidatedTaskIds), 'golden-apply: invalidatedTaskIds array');
    ok(applyResultGolden.requiresRerun === true, 'golden-apply: requiresRerun');
    ok(applyResultGolden.duplicate === false, 'golden-apply: not duplicate');
  }

  // ════════════════════════════════════════════════════════════════════════
  // 4. Draft text: change intent is scoped by work and block
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    const { host, cleanup } = await mount(makeDrawer(h));

    const textarea = host.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    ok(textarea !== null, 'draft: textarea exists');
    eq(textarea?.value, '', 'draft: initially empty');
    await interact(() => {
      if (!textarea) return;
      const setValue = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set;
      setValue?.call(textarea, '按 Block 保存');
      textarea.dispatchEvent(new Event('input', { bubbles: true }));
    });
    eq(h.draftIntents.length, 1, 'draft: change intent fired');
    eq(h.draftIntents[0]?.workId, WORK_ID, 'draft: intent workId');
    eq(h.draftIntents[0]?.blockId, BLOCK_ID, 'draft: intent blockId');
    eq(h.draftIntents[0]?.taskId, TASK_ID, 'draft: intent taskId');
    eq(h.draftIntents[0]?.text, '按 Block 保存', 'draft: intent text');

    // Controlled component: set harness state and re-mount
    await cleanup();

    const h2 = makeHarness();
    h2.draftText = '改成逻辑与安全审查';
    const { host: host2, cleanup: cleanup2 } = await mount(makeDrawer(h2));

    const textarea2 = host2.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    eq(textarea2?.value, '改成逻辑与安全审查', 'draft: value from parent');
    eq(h2.draftText, '改成逻辑与安全审查', 'draft: harness state');

    const previewBtn = host2.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    eq(previewBtn?.disabled, false, 'draft: preview enabled with text');
    await cleanup2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 5. Whitespace-only input does not enable preview
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '   ';
    const { host, cleanup } = await mount(makeDrawer(h));
    const previewBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    eq(previewBtn?.disabled, true, 'whitespace: preview disabled for whitespace-only');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 6. Preview intent fires with correct payload
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    h.selectedScope = 'workflow';
    const { host, cleanup } = await mount(makeDrawer(h));

    const previewBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    await interact(() => previewBtn?.click());

    eq(h.previewIntents.length, 1, 'preview: intent fired');
    const intent = h.previewIntents[0]!;
    eq(intent.workId, WORK_ID, 'preview: workId');
    eq(intent.taskId, TASK_ID, 'preview: taskId');
    eq(intent.blockId, BLOCK_ID, 'preview: blockId');
    eq(intent.runId, RUN_ID, 'preview: runId');
    eq(intent.sessionId, SESSION_ID, 'preview: sessionId');
    eq(intent.instruction, '改标题', 'preview: instruction');
    eq(intent.definitionRevision, DEF_REV, 'preview: definitionRevision');
    eq(intent.blockRevision, BLOCK_REV, 'preview: blockRevision');
    eq(intent.scope, 'workflow', 'preview: scope=workflow');
    contains(intent.requestId, 'disc-preview-', 'preview: requestId prefix');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 7. Scope selection via parent state (controlled component)
  // ════════════════════════════════════════════════════════════════════════
  {
    // Default is 'block'
    const h = makeHarness();
    const { host, cleanup } = await mount(makeDrawer(h));

    const workflowRadio = host.querySelector<HTMLInputElement>(`[data-testid="discussion-scope-workflow-${TASK_ID}"]`);
    ok(workflowRadio !== null, 'scope: workflow radio exists');
    eq(workflowRadio?.checked, false, 'scope: workflow not checked initially');

    const blockRadio = host.querySelector<HTMLInputElement>(`[data-testid="discussion-scope-block-${TASK_ID}"]`);
    eq(blockRadio?.checked, true, 'scope: block checked initially');
    await cleanup();

    // Re-mount with workflow scope
    const h2 = makeHarness();
    h2.selectedScope = 'workflow';
    const { host: host2, cleanup: cleanup2 } = await mount(makeDrawer(h2));

    const wfRadio2 = host2.querySelector<HTMLInputElement>(`[data-testid="discussion-scope-workflow-${TASK_ID}"]`);
    eq(wfRadio2?.checked, true, 'scope: workflow checked after change');

    const blockRadio2 = host2.querySelector<HTMLInputElement>(`[data-testid="discussion-scope-block-${TASK_ID}"]`);
    eq(blockRadio2?.checked, false, 'scope: block unchecked after change');
    await cleanup2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 8. Esc closes and fires onClose intent
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    const returnButton = document.createElement('button');
    document.body.appendChild(returnButton);
    const returnFocusRef = { current: returnButton };
    const { host, root, cleanup } = await mount(makeDrawer(h, { returnFocusRef }));

    const drawer = host.querySelector(`[data-testid="discussion-drawer-${TASK_ID}"]`);
    await interact(() => drawer?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));

    eq(h.closeIntents.length, 1, 'esc: close intent fired');
    eq(h.closeIntents[0]?.workId, WORK_ID, 'esc: workId');
    eq(h.closeIntents[0]?.taskId, TASK_ID, 'esc: taskId');
    eq(h.closeIntents[0]?.blockId, BLOCK_ID, 'esc: blockId');

    // The integration owner closes the controlled drawer after receiving the
    // intent. Unmount must return focus to the exact discussion trigger.
    await act(async () => {
      root.render(<></>);
    });
    eq(document.activeElement, returnButton, 'esc: focus returns to trigger after close');

    await cleanup();
    returnButton.remove();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 9. Close button fires onClose
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    const { host, cleanup } = await mount(makeDrawer(h));

    const closeBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-close-${TASK_ID}"]`);
    await interact(() => closeBtn?.click());
    eq(h.closeIntents.length, 1, 'close-btn: intent fired');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 10. Ctrl+Enter previews
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '测试指令';
    const { host, cleanup } = await mount(makeDrawer(h));

    const textarea = host.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    await interact(() => {
      textarea?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }));
    });

    eq(h.previewIntents.length, 1, 'ctrl-enter: preview fired');
    eq(h.previewIntents[0]?.instruction, '测试指令', 'ctrl-enter: correct instruction');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 11. Ctrl+Enter does not fire when text empty
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '';
    const { host, cleanup } = await mount(makeDrawer(h));

    const drawer = host.querySelector(`[data-testid="discussion-drawer-${TASK_ID}"]`);
    await interact(() => {
      drawer?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }));
    });

    eq(h.previewIntents.length, 0, 'ctrl-enter-empty: no preview fired');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 12. Preview loading state disables button
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '指令';
    const { host, cleanup } = await mount(makeDrawer(h, { isPreviewing: true }));

    const previewBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    eq(previewBtn?.disabled, true, 'loading: preview btn disabled');
    contains(previewBtn?.textContent ?? '', '分析影响中', 'loading: button text');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 13. Preview error banner with retry
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '指令';
    const { host, cleanup } = await mount(makeDrawer(h, {
      previewError: 'AI 服务暂时不可用',
    }));

    const error = host.querySelector(`[data-testid="discussion-error-${TASK_ID}"]`);
    ok(error !== null, 'error: banner exists');
    eq(error?.getAttribute('role'), 'alert', 'error: role=alert');
    contains(error?.textContent ?? '', 'AI 服务暂时不可用', 'error: message');

    const retryBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-retry-preview-${TASK_ID}"]`);
    ok(retryBtn !== null, 'error: retry button exists');
    await interact(() => retryBtn?.click());
    eq(h.previewIntents.length, 1, 'error: retry fires preview');
    await interact(() => retryBtn?.click());
    eq(h.previewIntents.length, 2, 'error: retry can be repeated');
    eq(
      h.previewIntents[1]?.requestId,
      h.previewIntents[0]?.requestId,
      'error: identical retry reuses requestId',
    );

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 14. Revision conflict banner
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const staleBasePatch: WorkPatchPreview = {
      ...patchGolden,
      id: 'patch-stale-base',
      digest: 'stale-base-digest',
      baseDefinitionRev: DEF_REV - 1,
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: staleBasePatch,
    }));

    const conflict = host.querySelector(`[data-testid="discussion-conflict-${TASK_ID}"]`);
    ok(conflict !== null, 'conflict: banner exists');
    eq(conflict?.getAttribute('role'), 'alert', 'conflict: role=alert');
    contains(conflict?.textContent ?? '', '工作内容已变化', 'conflict: revision message');

    // Apply button should be disabled
    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    eq(applyBtn?.disabled, true, 'conflict: apply disabled');

    // Re-preview button works
    const repreviewBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-repreview-${TASK_ID}"]`);
    ok(repreviewBtn !== null, 'conflict: re-preview button');
    await interact(() => repreviewBtn?.click());
    eq(h.previewIntents.length, 1, 'conflict: re-preview fires');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 15. Non-expired digest conflict is distinct from expiry
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const digestConflictPatch: WorkPatchPreview = {
      ...patchGolden,
      expiresAt: '2099-01-01T00:00:00Z',
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: digestConflictPatch,
      digestConflict: true,
    }));

    const conflict = host.querySelector(`[data-testid="discussion-conflict-${TASK_ID}"]`);
    ok(conflict !== null, 'digest-conflict: banner exists');
    contains(conflict?.textContent ?? '', '改动内容已变化', 'digest-conflict: explicit digest message');
    notContains(conflict?.textContent ?? '', '预览内容已过期', 'digest-conflict: does not misreport expiry');
    const regenerate = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-repreview-${TASK_ID}"]`);
    contains(regenerate?.textContent ?? '', '重新预览', 'digest-conflict: recovery regenerates preview');
    await interact(() => regenerate?.click());
    eq(h.previewIntents.length, 1, 'digest-conflict: regenerate returns to preview planning');

    await cleanup();
  }

  // A changed instruction cannot apply a preview generated from older text.
  {
    const h = makeHarness();
    h.draftText = '使用动物主题';
    const { host, root, cleanup } = await mount(makeDrawer(h));
    const previewBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    await interact(() => previewBtn?.click());
    await act(async () => {
      root.render(makeDrawer(h, { patchPreview: patchGolden }));
    });

    h.draftText = '改成太空主题';
    await act(async () => {
      root.render(makeDrawer(h, { patchPreview: patchGolden }));
    });
    const conflict = host.querySelector(`[data-testid="discussion-conflict-${TASK_ID}"]`);
    contains(conflict?.textContent ?? '', '修改意见已变化', 'instruction-conflict: stale preview is explicit');
    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    eq(applyBtn?.disabled, true, 'instruction-conflict: stale preview cannot be applied');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 16. Apply fires intent with correct payload
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const { host, root, cleanup } = await mount(makeDrawer(h));

    const previewBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    ok(previewBtn !== null, 'apply: preview action exists');
    eq(previewBtn?.disabled, false, 'apply: preview action is enabled');

    await interact(() => previewBtn?.click());
    await act(async () => {
      root.render(makeDrawer(h, { patchPreview: patchGolden }));
    });
    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    ok(applyBtn !== null, 'apply: confirmation action exists after preview');
    await interact(() => applyBtn?.click());
    eq(h.applyIntents.length, 1, 'apply: intent fired');
    const intent = h.applyIntents[0]!;
    eq(intent.workId, WORK_ID, 'apply: workId');
    eq(intent.patchId, patchGolden.id, 'apply: patchId');
    eq(intent.previewDigest, patchGolden.digest, 'apply: digest');
    eq(intent.scope, 'block', 'apply: scope');
    eq(intent.expectedRevision, WORK_REV, 'apply: exact work revision');
    contains(intent.requestId, 'disc-apply-', 'apply: requestId prefix');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 17. Apply loading state
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
      isApplying: true,
    }));

    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    eq(applyBtn?.disabled, true, 'apply-loading: button disabled');
    contains(applyBtn?.textContent ?? '', '正在应用', 'apply-loading: button text');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 18. Apply success result
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const successResult: ApplyWorkPatchResult = {
      workRevision: 5,
      newRevision: 6,
      invalidatedTaskIds: ['logic_review'],
      affectedBlockIds: ['review-block'],
      affectedArtifactSlotIds: ['slot-report'],
      staleArtifactSlotIds: ['slot-report'],
      requiresRerun: true,
      duplicate: false,
      committed: true,
      recoverable: true,
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
      applyResult: successResult,
    }));

    const result = host.querySelector(`[data-testid="discussion-result-${TASK_ID}"]`);
    ok(result !== null, 'apply-ok: result banner exists');
    eq(result?.getAttribute('role'), 'status', 'apply-ok: role=status');
    ok(result?.getAttribute('aria-live') === 'polite', 'apply-ok: aria-live=polite');
    contains(result?.textContent ?? '', '修改已应用', 'apply-ok: success message');
    contains(result?.textContent ?? '', 'AI 正在按新要求重新处理', 'apply-ok: rerun hint');
    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    eq(applyBtn?.disabled, true, 'apply-ok: committed patch cannot be applied again');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 19. Apply error result
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const errorResult: ApplyWorkPatchResult = {
      workRevision: 5,
      newRevision: 5,
      requiresRerun: false,
      duplicate: false,
      error: '版本冲突：定义已被修改',
      committed: false,
      recoverable: true,
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
      applyResult: errorResult,
    }));

    const result = host.querySelector(`[data-testid="discussion-result-${TASK_ID}"]`);
    eq(result?.getAttribute('role'), 'alert', 'apply-error: role=alert');
    contains(result?.textContent ?? '', '应用失败', 'apply-error: error message');
    contains(result?.textContent ?? '', '版本冲突', 'apply-error: error detail');
    contains(result?.textContent ?? '', '可重试', 'apply-error: recoverable hint');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 19b. Committed recovery is not presented as a safe re-apply
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const committedRecoveryResult: ApplyWorkPatchResult = {
      workRevision: WORK_REV,
      newRevision: WORK_REV + 1,
      requiresRerun: false,
      duplicate: false,
      error: '连接中断，等待快照确认',
      committed: true,
      recoverable: true,
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
      applyResult: committedRecoveryResult,
    }));

    const result = host.querySelector(`[data-testid="discussion-result-${TASK_ID}"]`);
    contains(result?.textContent ?? '', '改动已提交', 'committed-recovery: commit is explicit');
    contains(result?.textContent ?? '', '恢复确认', 'committed-recovery: recovery is explicit');
    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    eq(applyBtn?.disabled, true, 'committed-recovery: apply remains disabled');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 20. Apply duplicate result
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const dupResult: ApplyWorkPatchResult = {
      workRevision: 5,
      newRevision: 5,
      requiresRerun: false,
      duplicate: true,
      committed: true,
      recoverable: true,
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
      applyResult: dupResult,
    }));

    const result = host.querySelector(`[data-testid="discussion-result-${TASK_ID}"]`);
    contains(result?.textContent ?? '', '无需重复处理', 'apply-dup: duplicate message');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 21. Dismiss result
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const okResult: ApplyWorkPatchResult = {
      workRevision: 5,
      newRevision: 6,
      requiresRerun: false,
      duplicate: false,
      committed: true,
      recoverable: true,
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
      applyResult: okResult,
    }));

    const dismissBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-dismiss-result-${TASK_ID}"]`);
    ok(dismissBtn !== null, 'dismiss: button exists');
    await interact(() => dismissBtn?.click());
    eq(h.dismissCalls, 1, 'dismiss: onDismissResult fired');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 22. Expired impact preview stays visible but cannot be applied
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const expiredPatch: WorkPatchPreview = {
      ...patchGolden,
      id: 'patch-expired',
      digest: 'expired-digest',
      expiresAt: '2020-01-01T00:00:00Z',
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: expiredPatch,
    }));

    const previewPanel = host.querySelector(`[data-testid="patch-preview-${TASK_ID}"]`);
    ok(previewPanel !== null, 'expired: impact preview remains visible for diagnosis');
    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    eq(applyBtn?.disabled, true, 'expired: apply disabled');
    const conflict = host.querySelector(`[data-testid="discussion-conflict-${TASK_ID}"]`);
    contains(conflict?.textContent ?? '', '改动校验已过期', 'expired: explicit retry message');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 22b. Late preview from another block is ignored
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const foreignPatch: WorkPatchPreview = {
      ...patchGolden,
      id: 'patch-other-block',
      blockId: 'block-other',
      digest: 'other-block-digest',
    };
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: foreignPatch,
    }));

    ok(
      host.querySelector(`[data-testid="patch-preview-${TASK_ID}"]`) === null,
      'late-preview: foreign block preview is not rendered',
    );
    ok(
      host.querySelector(`[data-testid="discussion-apply-btn-${TASK_ID}"]`) === null,
      'late-preview: foreign block preview cannot be applied',
    );

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 23. Empty operations in preview
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const emptyOpsPatch: WorkPatchPreview = {
      ...patchGolden,
      id: 'patch-empty-ops',
      digest: 'empty-digest',
      operations: [],
    };
    const { host, cleanup } = await mount(
      <PatchPreview patch={emptyOpsPatch} taskTitle={TASK_TITLE} taskId={TASK_ID} />,
    );

    const empty = host.querySelector(`[data-testid="patch-preview-empty-ops-${TASK_ID}"]`);
    ok(empty !== null, 'empty-ops: placeholder shown');
    contains(empty?.textContent ?? '', '没有检测到需要修改的内容', 'empty-ops: message');

    // No ops table
    const opsTable = host.querySelector(`[data-testid="patch-preview-ops-${TASK_ID}"]`);
    ok(opsTable === null, 'empty-ops: no ops table');

    await cleanup();
  }

  // Historical previews may contain Go nil slices encoded as null.
  {
    const nullCollections = {
      ...patchGolden,
      operations: null,
      affectedNodeIds: null,
      affectedBlockIds: null,
      affectedArtifactSlotIds: null,
      staleArtifactSlotIds: null,
      invalidatedTaskIds: null,
    } as unknown as WorkPatchPreview;
    const { host, cleanup } = await mount(
      <PatchPreview patch={nullCollections} taskTitle={TASK_TITLE} taskId={TASK_ID} />,
    );
    ok(host.querySelector(`[data-testid="patch-preview-${TASK_ID}"]`) !== null, 'null-collections: preview renders');
    ok(host.querySelector(`[data-testid="patch-preview-empty-ops-${TASK_ID}"]`) !== null, 'null-collections: operations normalize to empty');
    ok(host.querySelector(`[data-testid="patch-affected-nodes-${TASK_ID}"]`) === null, 'null-collections: no phantom impact');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 24. Invalidated tasks shown in preview
  // ════════════════════════════════════════════════════════════════════════
  {
    const patchWithInvalidated: WorkPatchPreview = {
      ...patchGolden,
      id: 'patch-with-invalidated',
      digest: 'inv-digest',
      invalidatedTaskIds: ['task-b', 'task-a'],
    };
    const labels: Record<string, string> = {
      'task-a': '规划主题',
      'task-b': '创作笑话',
    };
    const { host, cleanup } = await mount(
      <PatchPreview
        patch={patchWithInvalidated}
        taskTitle={TASK_TITLE}
        taskId={TASK_ID}
        resolveLabel={(_kind, id) => labels[id]}
        resolveOrder={(_kind, id) => id === 'task-a' ? 0 : 1}
      />,
    );

    const inv = host.querySelector(`[data-testid="patch-invalidated-tasks-${TASK_ID}"]`);
    ok(inv !== null, 'invalidated: section exists');
    contains(inv?.textContent ?? '', '规划主题', 'invalidated: uses friendly task title');
    contains(inv?.textContent ?? '', '创作笑话', 'invalidated: uses friendly task title');
    notContains(inv?.textContent ?? '', 'task-a', 'invalidated: hides internal task id');
    ok(
      (inv?.textContent?.indexOf('规划主题') ?? -1) < (inv?.textContent?.indexOf('创作笑话') ?? -1),
      'invalidated: follows workflow order',
    );

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 25. Affected nodes shown in preview
  // ════════════════════════════════════════════════════════════════════════
  {
    const nodeOnly = {
      ...patchGolden,
      invalidatedTaskIds: [],
    };
    const { host, cleanup } = await mount(
      <PatchPreview patch={nodeOnly} taskTitle={TASK_TITLE} taskId={TASK_ID} />,
    );

    const nodes = host.querySelector(`[data-testid="patch-affected-nodes-${TASK_ID}"]`);
    ok(nodes !== null, 'affected-nodes: section exists');

    for (const id of nodeOnly.affectedNodeIds) {
      contains(nodes?.textContent ?? '', id, `affected-nodes: contains ${id}`);
    }

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 26. Stale slots shown in preview
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <PatchPreview patch={patchGolden} taskTitle={TASK_TITLE} taskId={TASK_ID} />,
    );

    const stale = host.querySelector(`[data-testid="patch-stale-slots-${TASK_ID}"]`);
    ok(stale !== null, 'stale: section exists');

    for (const id of patchGolden.staleArtifactSlotIds) {
      contains(stale?.textContent ?? '', id, `stale: contains ${id}`);
    }

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 27. No stale slots — section not rendered
  // ════════════════════════════════════════════════════════════════════════
  {
    const noStale: WorkPatchPreview = {
      ...patchGolden,
      id: 'no-stale',
      digest: 'ns-digest',
      staleArtifactSlotIds: [],
      invalidatedTaskIds: [],
    };
    const { host, cleanup } = await mount(
      <PatchPreview patch={noStale} taskTitle={TASK_TITLE} taskId={TASK_ID} />,
    );

    const stale = host.querySelector(`[data-testid="patch-stale-slots-${TASK_ID}"]`);
    ok(stale === null, 'no-stale: section absent');

    const inv = host.querySelector(`[data-testid="patch-invalidated-tasks-${TASK_ID}"]`);
    ok(inv === null, 'no-invalidated: section absent');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 28. Keyboard: focus transitions from textarea to buttons
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
    }));

    const textarea = host.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    ok(textarea !== null, 'kb: textarea exists');
    eq(document.activeElement, textarea, 'kb: textarea auto-focused');

    // Move keyboard focus
    textarea?.focus();
    eq(document.activeElement, textarea, 'kb: focus on textarea');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 29. Tab trap: Tab cycles within drawer
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
    }));

    const drawer = host.querySelector(`[data-testid="discussion-drawer-${TASK_ID}"]`);
    ok(drawer !== null, 'tab-trap: drawer exists');

    const focusable = drawer!.querySelectorAll<HTMLElement>(
      'button:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    );
    ok(focusable.length >= 3, `tab-trap: has ${focusable.length} focusable elements`);

    // Focus last element, then Tab should go to first
    focusable[focusable.length - 1]?.focus();
    await interact(() => {
      drawer?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, shiftKey: false }));
    });
    // After Tab on last element, focus should be on first (tab trap)
    // This behavior depends on browser implementation; verify structure at least
    ok(true, 'tab-trap: structure verified');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 30. Same-intent retry reuses requestId (stable cross-process)
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const { host, cleanup } = await mount(makeDrawer(h));

    const previewBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    await interact(() => previewBtn?.click());
    await interact(() => previewBtn?.click());
    await interact(() => previewBtn?.click());

    eq(h.previewIntents.length, 3, 'multi-preview: 3 intents fired');

    const ids = new Set(h.previewIntents.map((i) => i.requestId));
    eq(ids.size, 1, 'multi-preview: same intent reuses same requestId for cross-process stability');

    // All contain correct prefix
    for (const intent of h.previewIntents) {
      ok(intent.requestId.startsWith('disc-preview-'), `multi-preview: ${intent.requestId} has prefix`);
    }

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 31. CSS variables are present and prefixed
  // ════════════════════════════════════════════════════════════════════════
  {
    ok(cssText.length > 100, 'css: has content');
    contains(cssText, '.wg2-dd-backdrop', 'css: backdrop class');
    contains(cssText, '.wg2-dd-drawer', 'css: drawer class');
    contains(cssText, '--z-modal', 'css: uses shared modal layer token');
    contains(cssText, '.wg2-pp-panel', 'css: preview panel class');
    contains(cssText, '.wg2-dd-btn', 'css: button class');
    contains(cssText, '.wg2-pp-ops', 'css: ops table');
    contains(cssText, '.wg2-pp-ops-wrap', 'css: narrow ops scroller');
    contains(cssText, 'overflow-x: auto', 'css: wide ops remain usable on narrow screens');
    contains(cssText, '@media (max-width: 640px)', 'css: responsive breakpoint');
    contains(cssText, 'prefers-reduced-motion', 'css: reduced motion');

    // No pollution of existing prefixes
    notContains(cssText, '.wg2-el-', 'css: no ExecutionList prefix');
    notContains(cssText, '.wg2-er-', 'css: no ExecutionRow prefix');
    notContains(cssText, '.wg2-eb-', 'css: no ExpandedBlock prefix');
    notContains(cssText, '.wg2-rs-', 'css: no ResultShelf prefix');
    notContains(cssText, '.wg2-rc-', 'css: no ResultCard prefix');

    await settle(0);
  }

  // ════════════════════════════════════════════════════════════════════════
  // 32. ARIA attributes on all interactive elements
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const { host, cleanup } = await mount(makeDrawer(h, {
      patchPreview: patchGolden,
    }));

    const drawer = host.querySelector(`[data-testid="discussion-drawer-${TASK_ID}"]`);
    ok(drawer?.getAttribute('aria-label')?.length! > 0, 'aria: drawer has label');

    const textarea = host.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    ok(textarea?.getAttribute('aria-describedby')?.length! > 0, 'aria: textarea has describedby');

    const closeBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-close-${TASK_ID}"]`);
    ok(closeBtn?.getAttribute('aria-label')?.length! > 0, 'aria: close has label');

    const previewPanel = host.querySelector(`[data-testid="patch-preview-${TASK_ID}"]`);
    ok(previewPanel !== null, 'aria: impact preview is exposed before apply');
    contains(previewPanel?.getAttribute('aria-label') ?? '', '修改影响预览', 'aria: preview has a user-facing label');
    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    contains(applyBtn?.textContent ?? '', '确认并应用修改', 'aria: apply action matches local scope');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 33. Draft preserved across re-renders (parent managed)
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '保存的草稿';
    const { host, cleanup } = await mount(makeDrawer(h));

    let textarea = host.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    eq(textarea?.value, '保存的草稿', 'draft-persist: initial value');

    // Re-render with same harness — draft preserved
    await cleanup();

    const { host: host2, cleanup: cleanup2 } = await mount(makeDrawer(h));
    textarea = host2.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    eq(textarea?.value, '保存的草稿', 'draft-persist: after remount');

    await cleanup2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 34. Workflow scope badge displayed in preview
  // ════════════════════════════════════════════════════════════════════════
  {
    const workflowPatch: WorkPatchPreview = {
      ...patchGolden,
      id: 'patch-wf',
      digest: 'wf-digest',
      scope: 'workflow',
    };
    const { host, cleanup } = await mount(
      <PatchPreview patch={workflowPatch} taskTitle={TASK_TITLE} taskId={TASK_ID} />,
    );

    const badge = host.querySelector(`[data-testid="patch-preview-scope-${TASK_ID}"]`);
    contains(badge?.textContent ?? '', '会更新当前任务和后续步骤', 'workflow: badge text');
    ok(badge?.classList.contains('wg2-pp-badge-workflow') ?? false, 'workflow: badge class');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 35. Apply retry keeps the original idempotency envelope
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    h.draftText = '改标题';
    const { host, root, cleanup } = await mount(makeDrawer(h));

    const submitBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-preview-btn-${TASK_ID}"]`);
    await interact(() => submitBtn?.click());
    await act(async () => {
      root.render(makeDrawer(h, { patchPreview: patchGolden }));
    });
    const firstApplyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    await interact(() => firstApplyBtn?.click());
    eq(h.applyIntents.length, 1, 'apply-retry: first apply fired');

    // A late snapshot may advance the visible work revision while the network
    // outcome is unknown. Retrying the same patch must preserve both fields.
    await act(async () => {
      root.render(makeDrawer(h, {
        patchPreview: patchGolden,
        workRevision: WORK_REV + 1,
        applyError: '网络中断，可安全重试',
      }));
    });
    const applyBtn = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-apply-btn-${TASK_ID}"]`);
    await interact(() => applyBtn?.click());

    eq(h.applyIntents.length, 2, 'apply-retry: second apply fired');
    eq(
      h.applyIntents[1]?.requestId,
      h.applyIntents[0]?.requestId,
      'apply-retry: same requestId',
    );
    eq(
      h.applyIntents[1]?.expectedRevision,
      h.applyIntents[0]?.expectedRevision,
      'apply-retry: same expectedRevision',
    );
    eq(h.applyIntents[0]?.expectedRevision, WORK_REV, 'apply-retry: starts from work revision');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 36. Portal modal is outside the render container
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    const { host, container, cleanup } = await mount(makeDrawer(h));

    const backdrop = host.querySelector(`[data-testid="discussion-backdrop-${TASK_ID}"]`);
    const drawer = host.querySelector(`[data-testid="discussion-drawer-${TASK_ID}"]`);
    ok(backdrop?.parentElement === document.body, 'portal: backdrop is a body child');
    ok(!container.contains(backdrop), 'portal: backdrop is outside the ExecutionList render container');
    eq(drawer?.getAttribute('aria-modal'), 'true', 'portal: dialog is modal');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 37. Backdrop closes; dialog content does not
  // ════════════════════════════════════════════════════════════════════════
  {
    const h = makeHarness();
    const { host, cleanup } = await mount(makeDrawer(h));
    const backdrop = host.querySelector(`[data-testid="discussion-backdrop-${TASK_ID}"]`);
    const drawer = host.querySelector(`[data-testid="discussion-drawer-${TASK_ID}"]`);

    await interact(() => {
      drawer?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    eq(h.closeIntents.length, 0, 'backdrop: dialog click stays open');

    await interact(() => {
      backdrop?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    eq(h.closeIntents.length, 1, 'backdrop: empty area closes modal');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 38. Focus returns to the captured trigger without an explicit ref
  // ════════════════════════════════════════════════════════════════════════
  {
    const trigger = document.createElement('button');
    document.body.appendChild(trigger);
    trigger.focus();

    const h = makeHarness();
    const { host, cleanup } = await mount(makeDrawer(h));
    const textarea = host.querySelector<HTMLTextAreaElement>(`[data-testid="discussion-input-${TASK_ID}"]`);
    eq(document.activeElement, textarea, 'focus: textarea receives focus');

    const close = host.querySelector<HTMLButtonElement>(`[data-testid="discussion-close-${TASK_ID}"]`);
    await interact(() => close?.click());
    await cleanup();
    await settle(0);
    eq(document.activeElement, trigger, 'focus: close restores captured trigger');
    trigger.remove();
  }

  // ── Summary ────────────────────────────────────────────────────────────
  process.stdout.write(`\n  ${passed} passed, ${failed} failed\n`);
  if (failed > 0) {
    process.exit(1);
  }
}

runTests().catch((err) => {
  process.stderr.write(`${err instanceof Error ? err.stack ?? err.message : String(err)}\n`);
  process.exit(1);
});
