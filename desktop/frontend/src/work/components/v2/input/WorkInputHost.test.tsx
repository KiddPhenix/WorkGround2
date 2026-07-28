import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

import { JSDOM } from 'jsdom';
import React, { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import type {
  InputSpec,
  InputKind,
  WorkInput,
  SubmitWorkInputRequest,
  SetInputCornerstoneRequest,
  SubmitInputResult,
  CornerstonePinResult,
} from '../../../types_v2';
import { WorkInputHost as ProductionWorkInputHost } from './WorkInputHost';
import type { DraftValue, WorkInputHostProps } from './WorkInputHost';
import { parseValueSchema, toWireValue, validateDraft } from './schema';

// ── test harness ───────────────────────────────────────────────────────────

let passed = 0;
let failed = 0;

function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (condition) passed++;
  else { failed++; if (failed <= 5) { const st = new Error().stack?.split('\n')[2]?.trim(); if (st) process.stdout.write(`       ${st}\n`); } }
}

function eq<T>(actual: T, expected: T, label: string): void {
  const cond = actual === expected;
  ok(cond, `${label}${cond ? '' : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function contains(actual: string, substring: string, label: string): void {
  ok(actual.includes(substring), `${label} (expected "${substring}" in "${actual.slice(0, 100)}")`);
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
    HTMLInputElement: dom.window.HTMLInputElement,
    HTMLTextAreaElement: dom.window.HTMLTextAreaElement,
    HTMLSelectElement: dom.window.HTMLSelectElement,
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

function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: Error) => void } {
  let r!: (v: T) => void;
  let j!: (e: Error) => void;
  const promise = new Promise<T>((res, rej) => { r = res; j = rej; });
  return { promise, resolve: r, reject: j };
}

// ── golden data ────────────────────────────────────────────────────────────

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const goldenPath = resolve(__dirname, '..', '..', '..', '..', '..', '..', '..', 'internal', 'work', 'testdata', 'contract-v2', 'work-view-v2-full.json');
const goldenView: unknown = JSON.parse(readFileSync(goldenPath, 'utf-8'));

// ── fixtures ───────────────────────────────────────────────────────────────

const WORK_ID = 'work-fixture';
const TASK_ID = 'task-fixture';
const RUN_ID = 'run-fixture';
const BLOCK_ID = 'b1';
const DEF_REV = 2;
const INPUT_REV = 1;
const WORK_REV = 27;

function makeSpec(overrides: Partial<InputSpec> = {}): InputSpec {
  return { id: 'spec-text', label: '测试输入', kind: 'text' as InputKind, required: false, pinEligible: false, ...overrides };
}

function makeWorkInput(overrides: Partial<WorkInput> = {}): WorkInput {
  return {
    id: 'input-1', workId: WORK_ID, runId: RUN_ID, taskId: TASK_ID, blockId: BLOCK_ID,
    specId: 'spec-text', value: null, state: 'requested', revision: INPUT_REV,
    updatedAt: new Date().toISOString(), ...overrides,
  };
}

function makeInputReceipt(
  requestId: string,
  revision: number,
  overrides: Partial<NonNullable<SubmitInputResult['receipt']>> = {},
): NonNullable<SubmitInputResult['receipt']> {
  return {
    requestId,
    operation: 'input.submit',
    intentDigest: `intent-${requestId}`,
    inputId: overrides.inputId ?? 'input-1',
    resultRevision: revision,
    resultDigest: `result-${revision}`,
    createdAt: '2026-07-24T00:00:00Z',
    ...overrides,
  };
}

function WorkInputHost(
  props: Omit<WorkInputHostProps, 'onRefreshAuthoritative' | 'workRevision'> &
    Partial<Pick<WorkInputHostProps, 'onRefreshAuthoritative' | 'workRevision'>>,
): React.ReactElement {
  return (
    <ProductionWorkInputHost
      workInput={props.workInput ?? makeWorkInput({ specId: props.inputSpec.id })}
      onRefreshAuthoritative={async () => {}}
      workRevision={props.workRevision ?? 1}
      {...props}
    />
  );
}

// ── DTO-consuming noop helpers ─────────────────────────────────────────────

const noopSubmit = async (req: SubmitWorkInputRequest): Promise<SubmitInputResult> => ({
  revision: INPUT_REV + 1, duplicate: false, committed: true, recoverable: true,
  receipt: makeInputReceipt(req.requestId, INPUT_REV + 1, { inputId: req.inputId }),
});

const noopPin = async (req: SetInputCornerstoneRequest): Promise<CornerstonePinResult> => ({
  cornerstoneId: 'cs-1', pinned: true, revision: INPUT_REV + 1, duplicate: false, committed: true, recoverable: true,
  receipt: makeInputReceipt(req.requestId, INPUT_REV + 1, {
    inputId: req.inputId,
    operation: 'input.cornerstone',
    pinned: true,
    cornerstoneId: 'cs-1',
  }),
});

const noopUnpin = async (req: SetInputCornerstoneRequest): Promise<CornerstonePinResult> => ({
  pinned: false, revision: INPUT_REV + 1, duplicate: false, committed: true, recoverable: true,
  receipt: makeInputReceipt(req.requestId, INPUT_REV + 1, {
    inputId: req.inputId,
    operation: 'input.cornerstone',
    pinned: false,
  }),
});

// ── tests ──────────────────────────────────────────────────────────────────

async function runTests(): Promise<void> {

  // ════════════════════════════════════════════════════════════════════════
  // 0. Golden data validation
  // ════════════════════════════════════════════════════════════════════════
  {
    ok(goldenView !== null, 'golden: file loaded');
    ok(typeof goldenView === 'object', 'golden: is object');
    const gv = goldenView as Record<string, unknown>;
    eq(gv.schemaVersion, 2, 'golden: schemaVersion=2');
    ok(Array.isArray(gv.inputs), 'golden: inputs is array');
    const def = gv.definition as Record<string, unknown>;
    const inputSpecs = def?.inputSpecs as Array<Record<string, unknown>>;
    ok(Array.isArray(inputSpecs), 'golden: definition.inputSpecs is array');
    ok(inputSpecs.length >= 1, 'golden: at least 1 inputSpec');
    if (inputSpecs.length > 0) {
      eq(inputSpecs[0].id, 'topic', 'golden: inputSpec[0].id=topic');
      eq(inputSpecs[0].kind, 'text', 'golden: inputSpec[0].kind=text');
      eq(inputSpecs[0].label, 'Topic', 'golden: inputSpec[0].label=Topic');
    }
  }

  // ════════════════════════════════════════════════════════════════════════
  // 1. Text: render + required validation + keyboard submit + DTO fields
  // ════════════════════════════════════════════════════════════════════════
  {
    let draft: DraftValue = 'hello';
    const submitReqs: SubmitWorkInputRequest[] = [];
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 't1', label: '姓名', kind: 'text', required: true })}
        draftValue={draft}
        onDraftChange={(v) => { draft = v; }}
        onSubmit={async (req) => { submitReqs.push(req); return { revision: 3, duplicate: false, committed: true, recoverable: true }; }}
        onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );

    const hostEl = host.querySelector('[data-testid="work-input-host-task-fixture-t1"]');
    ok(hostEl !== null, 'text: host rendered');
    eq(hostEl?.getAttribute('data-input-kind'), 'text', 'text: data-input-kind');
    contains(hostEl?.textContent ?? '', '姓名', 'text: label');
    ok(hostEl?.textContent?.includes('*') ?? false, 'text: required asterisk');

    const ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-t1"]');
    ok(ctrl !== null, 'text: control exists');
    eq(ctrl?.tagName, 'INPUT', 'text: is INPUT');
    eq(ctrl?.type, 'text', 'text: type=text');
    eq(ctrl?.value, 'hello', 'text: draft displayed');

    // Keyboard Ctrl+Enter — dispatch on host wrapper (bubbles to host div's onKeyDown)
    const hostWrapper = host.querySelector<HTMLDivElement>('[data-testid="work-input-host-task-fixture-t1"]');
    await interact(() => {
      hostWrapper?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }));
    });
    await settle(50);

    // DTO must have full fields
    eq(submitReqs.length >= 1, true, 'text: submit fired via Ctrl+Enter');
    if (submitReqs.length > 0) {
      const dto = submitReqs[0];
      eq(dto.workId, WORK_ID, 'text: DTO.workId');
      eq(dto.taskId, TASK_ID, 'text: DTO.taskId');
      eq(dto.runId, RUN_ID, 'text: DTO.runId');
      eq(dto.blockId, BLOCK_ID, 'text: DTO.blockId');
      eq(dto.inputId, 'input-1', 'text: DTO.inputId');
      eq(dto.definitionRevision, DEF_REV, 'text: DTO.definitionRevision');
      eq(dto.inputRevision, INPUT_REV, 'text: DTO.inputRevision');
      ok(typeof dto.requestId === 'string' && dto.requestId.length > 0, 'text: DTO.requestId non-empty');
      eq(dto.value, 'hello', 'text: DTO.value');
    }

    // Revision displayed
    const rev = host.querySelector('[data-testid="work-input-rev-task-fixture-t1"]');
    ok(rev !== null, 'text: revision visible');
    ok(rev?.textContent?.includes('r3') ?? false, 'text: r3');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 1b. Text multiline contract + legacy "每行一个" compatibility
  // ════════════════════════════════════════════════════════════════════════
  {
    const draft: DraftValue = 'hello\nworld';
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({
          id: 'text-multiline',
          label: '详细内容',
          valueSchema: JSON.stringify({ multiline: true }),
        })}
        draftValue={draft}
        onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const textarea = host.querySelector<HTMLTextAreaElement>(
      '[data-testid="work-input-control-task-fixture-text-multiline"]',
    );
    eq(textarea?.tagName, 'TEXTAREA', 'text-multiline: explicit schema renders textarea');
    eq(textarea?.value, 'hello\nworld', 'text-multiline: newline preserved');
    eq(textarea?.rows, 4, 'text-multiline: shows multiple editable rows');
    await cleanup();

    const legacy = await mount(
      <WorkInputHost
        inputSpec={makeSpec({
          id: 'legacy-lines',
          label: '单词列表',
          description: '每行一个单词',
        })}
        draftValue="hello"
        onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    eq(
      legacy.host.querySelector('[data-testid="work-input-control-task-fixture-legacy-lines"]')?.tagName,
      'TEXTAREA',
      'text-multiline: legacy 每行 hint renders textarea',
    );
    await legacy.cleanup();

    const explicitSingleLine = await mount(
      <WorkInputHost
        inputSpec={makeSpec({
          id: 'explicit-single-line',
          label: '单词列表',
          description: '每行一个单词',
          valueSchema: JSON.stringify({ multiline: false }),
        })}
        draftValue="hello"
        onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    eq(
      explicitSingleLine.host.querySelector(
        '[data-testid="work-input-control-task-fixture-explicit-single-line"]',
      )?.tagName,
      'INPUT',
      'text-multiline: explicit false overrides legacy hint',
    );
    await explicitSingleLine.cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 2. Required: empty draft shows error, submit disabled
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'req1', label: '必填项', kind: 'text', required: true })}
        draftValue="" onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const err = host.querySelector('[data-testid="work-input-error-task-fixture-req1"]');
    ok(err !== null, 'required: error shown');
    contains(err?.textContent ?? '', '是必填项', 'required: message');

    const btn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-req1"]');
    ok(btn?.disabled ?? false, 'required: submit disabled');

    const ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-req1"]');
    eq(ctrl?.getAttribute('aria-invalid'), 'true', 'required: aria-invalid=true');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 3. Text constraints: minLength / maxLength / pattern
  // ════════════════════════════════════════════════════════════════════════
  {
    const valueSchema = JSON.stringify({ minLength: 3, maxLength: 10, pattern: '^[a-z]+$' });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'tc1', label: '约束文本', kind: 'text', required: true, valueSchema })}
        draftValue="AB" onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const err = host.querySelector('[data-testid="work-input-error-task-fixture-tc1"]');
    ok(err !== null, 'text-c: error for too-short');
    contains(err?.textContent ?? '', '至少需要 3', 'text-c: minLength');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 4. Number: basic, min/max, integer, unit validation
  // ════════════════════════════════════════════════════════════════════════
  {
    const valueSchema = JSON.stringify({ min: 0, max: 100, integer: true });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'n1', label: '数量', kind: 'number', required: true, valueSchema })}
        draftValue={150} onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const hostEl = host.querySelector('[data-testid="work-input-host-task-fixture-n1"]');
    eq(hostEl?.getAttribute('data-input-kind'), 'number', 'number: data-input-kind');

    const ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-n1"]');
    ok(ctrl !== null, 'number: control exists');
    eq(ctrl?.step, '1', 'number: step=1');

    const err = host.querySelector('[data-testid="work-input-error-task-fixture-n1"]');
    ok(err !== null, 'number: error > max');
    contains(err?.textContent ?? '', '不能大于 100', 'number: max msg');

    await cleanup();
  }

  // Ratio / percent unit validation
  {
    const { host: h1, cleanup: c1 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'ratio1', label: '比例', kind: 'number', required: true, valueSchema: JSON.stringify({ unit: 'ratio' }) })}
        draftValue={1.5} onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID} definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const errR = h1.querySelector('[data-testid="work-input-error-task-fixture-ratio1"]');
    ok(errR !== null, 'number-ratio: error for >1');

    const { host: h2, cleanup: c2 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'pct1', label: '百分比', kind: 'number', required: true, valueSchema: JSON.stringify({ unit: 'percent' }) })}
        draftValue={150} onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID} definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const errP = h2.querySelector('[data-testid="work-input-error-task-fixture-pct1"]');
    ok(errP !== null, 'number-pct: error for >100');
    await c1(); await c2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 5. Date: date / time / datetime / range modes
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'd1', label: '日期', kind: 'date', required: true })}
        draftValue="2026-07-24" onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-d1"]');
    ok(ctrl !== null, 'date: control exists');
    eq(ctrl?.type, 'date', 'date: type=date');
    await cleanup();
  }

  // Date range
  {
    const valueSchema = JSON.stringify({ mode: 'range' });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'dr1', label: '范围', kind: 'date', required: true, valueSchema })}
        draftValue={{ start: '2026-07-01', end: '2026-07-31' }} onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const start = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-dr1-start"]');
    const end = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-dr1-end"]');
    ok(start !== null && end !== null, 'date-range: both inputs exist');
    eq(start?.value, '2026-07-01', 'date-range: start');
    eq(end?.value, '2026-07-31', 'date-range: end');
    await cleanup();
  }

  // Time mode
  {
    const valueSchema = JSON.stringify({ mode: 'time' });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'tm1', label: '时间', kind: 'date', required: true, valueSchema })}
        draftValue="15:04" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID} definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-tm1"]');
    eq(ctrl?.type, 'time', 'date-time: type=time');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 6. Choice: select options, invalid value, allowOther
  // ════════════════════════════════════════════════════════════════════════
  {
    const valueSchema = JSON.stringify({ options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'c1', label: '选择', kind: 'choice', required: true, valueSchema })}
        draftValue="a" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const sel = host.querySelector<HTMLSelectElement>('[data-testid="work-input-control-task-fixture-c1-select"]');
    ok(sel !== null, 'choice: select exists');
    eq(sel?.value, 'a', 'choice: value=a');

    // Invalid value
    const { host: h2, cleanup: c2 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'c2', label: '选择2', kind: 'choice', required: true, valueSchema })}
        draftValue="invalid" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID} definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    ok(h2.querySelector('[data-testid="work-input-error-task-fixture-c2"]') !== null, 'choice-invalid: error');
    await cleanup(); await c2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 7. MultiChoice: checkboxes, minSelect/maxSelect
  // ════════════════════════════════════════════════════════════════════════
  {
    const valueSchema = JSON.stringify({
      options: [{ value: 'x', label: 'X' }, { value: 'y', label: 'Y' }, { value: 'z', label: 'Z' }],
      minSelect: 1, maxSelect: 2,
    });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'mc1', label: '多选', kind: 'multi_choice', required: true, valueSchema })}
        draftValue={['x', 'y']} onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const optX = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-mc1-opt-x"]');
    ok(optX?.checked ?? false, 'multi: x checked');
    const optZ = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-mc1-opt-z"]');
    ok(!(optZ?.checked ?? true), 'multi: z unchecked');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 8. Roster: entries, add/remove
  // ════════════════════════════════════════════════════════════════════════
  {
    const valueSchema = JSON.stringify({ fields: ['name', 'role'], minEntries: 1, maxEntries: 3 });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'r1', label: '名单', kind: 'roster', required: true, valueSchema })}
        draftValue={[{ name: '张三', role: '负责人' }]} onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const n = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-r1-entry-0-name"]');
    ok(n !== null, 'roster: entry name exists');
    eq(n?.value, '张三', 'roster: name=张三');
    const addBtn = host.querySelector('[data-testid="work-input-control-task-fixture-r1-add"]');
    ok(addBtn !== null, 'roster: add btn exists');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 9. Form: typed nested fields (choice + date + approval)
  // ════════════════════════════════════════════════════════════════════════
  {
    const valueSchema = JSON.stringify({
      fields: [
        { id: 'name', label: '名称', kind: 'text', required: true },
        { id: 'type', label: '类型', kind: 'choice', required: true, valueSchema: JSON.stringify({ options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] }) },
        { id: 'deadline', label: '截止', kind: 'date', required: false },
        { id: 'confirm', label: '确认', kind: 'approval', required: true, valueSchema: JSON.stringify({ riskLevel: 'high' }) },
      ],
    });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'f1', label: '表单', kind: 'form', required: true, valueSchema })}
        draftValue={{ name: '测试', type: 'a', deadline: '2026-07-24', confirm: '' }}
        onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    // Text field
    const nameCtrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-f1-name"]');
    ok(nameCtrl !== null, 'form: text field');
    eq(nameCtrl?.value, '测试', 'form: name value');

    // Choice field should be a <select>
    const typeCtrl = host.querySelector<HTMLSelectElement>('[data-testid="work-input-control-task-fixture-f1-type"]');
    ok(typeCtrl !== null, 'form: choice field is select');
    eq(typeCtrl?.tagName, 'SELECT', 'form: choice is SELECT');

    // Date field
    const dateCtrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-f1-deadline"]');
    ok(dateCtrl !== null, 'form: date field');
    eq(dateCtrl?.type, 'date', 'form: date type=date');

    // Approval field
    const appBtn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-control-task-fixture-f1-confirm-approved"]');
    ok(appBtn !== null, 'form: approval accept btn');

    // Missing required confirm field → error
    const fErr = host.querySelector('[data-testid="work-input-error-task-fixture-f1"]');
    ok(fErr !== null, 'form: error for missing required approval');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 10. File: ref display, select intent
  // ════════════════════════════════════════════════════════════════════════
  {
    let selectCalled = 0;
    const selectDfd = deferred<string>();
    const draftChanges: DraftValue[] = [];
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'file1', label: '文件', kind: 'file', required: true })}
        draftValue={[]} onDraftChange={(v) => { draftChanges.push(v); }}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
        onSelectFile={async () => {
          selectCalled++;
          const id = await selectDfd.promise;
          return { id, name: id, type: 'file', status: 'available' };
        }}
      />,
    );
    const selBtn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-control-task-fixture-file1-select"]');
    ok(selBtn !== null, 'file: select button exists');

    // Click select
    await interact(() => selBtn?.click());
    await settle(50);
    eq(selectCalled, 1, 'file: select called once');

    // Second click while pending → dedup
    await interact(() => selBtn?.click());
    await settle(50);
    eq(selectCalled, 1, 'file: select dedup');

    // Resolve
    selectDfd.resolve('ref-artifact-1');
    await settle(50);

    // Verify onDraftChange was called with the ref appended
    ok(draftChanges.length >= 1, 'file: draftChange called');
    const lastDraft = draftChanges[draftChanges.length - 1];
    ok(
      Array.isArray(lastDraft)
      && lastDraft.some((item) => typeof item === 'object' && item !== null && item.id === 'ref-artifact-1'),
      'file: typed ref in draft changes',
    );

    // Select failure
    const { host: h2, cleanup: c2 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'file2', label: '文件2', kind: 'file' })}
        draftValue={[]} onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID} definitionRevision={DEF_REV} inputRevision={INPUT_REV}
        onSelectFile={async () => { throw new Error('选择被拒绝'); }}
      />,
    );
    const selBtn2 = h2.querySelector<HTMLButtonElement>('[data-testid="work-input-control-task-fixture-file2-select"]');
    await interact(() => selBtn2?.click());
    await settle(50);
    const fileErr = h2.querySelector('[data-testid="work-input-control-task-fixture-file2-file-err"]');
    ok(fileErr !== null, 'file: error after reject');
    contains(fileErr?.textContent ?? '', '选择被拒绝', 'file: error msg');

    await cleanup(); await c2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 11. Approval: must not default, risk levels
  // ════════════════════════════════════════════════════════════════════════
  {
    const valueSchema = JSON.stringify({ riskLevel: 'critical', description: '不可逆操作' });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'app1', label: '批准', kind: 'approval', required: true, valueSchema })}
        draftValue="" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    // Not defaulted
    const pending = host.querySelector('.wg2-wh-approval-pending');
    ok(pending !== null, 'approval: pending prompt shown');
    contains(pending?.textContent ?? '', '请明确选择', 'approval: explicit choice required');

    // Buttons not pre-pressed
    const accept = host.querySelector<HTMLButtonElement>('[data-testid="work-input-control-task-fixture-app1-approved"]');
    eq(accept?.getAttribute('aria-pressed'), 'false', 'approval: accept not pressed');

    // Submit disabled
    const submit = host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-app1"]');
    ok(submit?.disabled ?? false, 'approval: submit disabled without choice');

    // Risk displayed
    contains(host.textContent ?? '', '严重', 'approval: critical risk');

    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 12. Pin/unpin: DTO fields, keyboard, independent error
  // ════════════════════════════════════════════════════════════════════════
  {
    const pinReqs: SetInputCornerstoneRequest[] = [];
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'pin1', label: '可固定', kind: 'text', required: false, pinEligible: true })}
        draftValue="val" onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={async (req) => {
          pinReqs.push(req);
          return {
            cornerstoneId: 'cs-1',
            pinned: true,
            revision: 2,
            duplicate: false,
            committed: true,
            recoverable: true,
            receipt: makeInputReceipt(req.requestId, 2, {
              inputId: req.inputId,
              operation: 'input.cornerstone',
              pinned: true,
              cornerstoneId: 'cs-1',
            }),
          };
        }}
        onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const pinBtn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-pin-task-fixture-pin1"]');
    ok(pinBtn !== null, 'pin: button exists');
    eq(pinBtn?.getAttribute('aria-pressed'), 'false', 'pin: not pressed');

    // Keyboard activate pin: Space key
    await interact(() => {
      pinBtn?.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }));
    });
    await settle(50);
    eq(pinReqs.length >= 1, true, 'pin: keyboard Space fires pin');
    if (pinReqs.length > 0) {
      const dto = pinReqs[0];
      eq(dto.pin, true, 'pin: DTO.pin=true');
      eq(dto.workId, WORK_ID, 'pin: DTO.workId');
      eq(dto.definitionRevision, DEF_REV, 'pin: DTO.definitionRevision');
      eq(dto.inputRevision, INPUT_REV, 'pin: DTO.inputRevision');
      ok(typeof dto.requestId === 'string' && dto.requestId.length > 0, 'pin: DTO.requestId');
    }
    eq(
      host.querySelector('[data-testid="work-input-host-task-fixture-pin1"]')?.getAttribute('data-pinned'),
      'true',
      'pin: committed result updates local feedback',
    );

    // Pin failure doesn't rollback input
    const { host: h2, cleanup: c2 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'pin2', label: '失败测试', kind: 'text', required: false, pinEligible: true })}
        draftValue="保留的值" onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={async () => ({ pinned: false, revision: 1, duplicate: false, committed: false, recoverable: true, error: '网络错误' })}
        onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const pinBtn2 = h2.querySelector<HTMLButtonElement>('[data-testid="work-input-pin-task-fixture-pin2"]');
    await interact(() => pinBtn2?.click());
    await settle(50);
    // Error visible
    const pinErr = h2.querySelector('[data-testid="work-input-pin-error-task-fixture-pin2"]');
    ok(pinErr !== null, 'pin-fail: error visible');
    contains(pinErr?.textContent ?? '', '网络错误', 'pin-fail: message');
    // Retry button
    ok(h2.querySelector('[data-testid="work-input-pin-retry-task-fixture-pin2"]') !== null, 'pin-fail: retry btn');
    // Input NOT cleared
    const ctrl = h2.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-pin2"]');
    eq(ctrl?.value, '保留的值', 'pin-fail: value NOT rolled back');

    // committed pin wins over transport error and duplicate, then refreshes authority
    let pinRefresh = '';
    const { host: h3, cleanup: c3 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'pin3', label: '已提交固定', kind: 'text', pinEligible: true })}
        draftValue="保留" onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={async (req) => ({
          cornerstoneId: 'cs-3',
          pinned: true,
          revision: 4,
          duplicate: true,
          error: 'ACK 丢失',
          committed: true,
          recoverable: true,
          transportError: {
            code: 'ACK_LOST',
            message: 'ACK 丢失',
            committed: true,
            recoverable: true,
          },
          receipt: makeInputReceipt(req.requestId, 4, {
            inputId: req.inputId,
            operation: 'input.cornerstone',
            pinned: true,
            cornerstoneId: 'cs-3',
          }),
        })}
        onUnpin={noopUnpin}
        onRefreshAuthoritative={async (context) => { pinRefresh = `${context.operation}:${context.revision}`; }}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const pinBtn3 = h3.querySelector<HTMLButtonElement>('[data-testid="work-input-pin-task-fixture-pin3"]');
    await interact(() => pinBtn3?.click()); await settle(50);
    ok(h3.querySelector('[data-testid="work-input-pin-error-task-fixture-pin3"]') === null, 'pin-recovery: committed result is not error');
    const pinRecovery = h3.querySelector('[data-testid="work-input-pin-recovery-task-fixture-pin3"]');
    ok(pinRecovery !== null, 'pin-recovery: warning visible');
    contains(pinRecovery?.textContent ?? '', '重复请求', 'pin-recovery: duplicate visible');
    eq(pinRefresh, 'pin:4', 'pin-recovery: authoritative refresh requested');

    await cleanup(); await c2(); await c3();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 13. Dedup: only one submit at a time, stable requestId on retry
  // ════════════════════════════════════════════════════════════════════════
  {
    let submitCount = 0;
    const dfd = deferred<SubmitInputResult>();
    const reqIds: string[] = [];
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'ds1', label: '去重', kind: 'text', required: true })}
        draftValue="hello" onDraftChange={() => {}}
        onSubmit={async (req) => { reqIds.push(req.requestId); submitCount++; return dfd.promise; }}
        onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const btn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-ds1"]');
    await interact(() => btn?.click()); await settle(50);
    eq(submitCount, 1, 'dedup: first submit');
    await interact(() => btn?.click()); await settle(50);
    eq(submitCount, 1, 'dedup: second ignored');

    dfd.resolve({ revision: 3, duplicate: false, committed: true, recoverable: true });
    await settle(50);

    // New submit should use same requestId (retry)
    await interact(() => btn?.click()); await settle(50);
    eq(submitCount, 2, 'dedup: retry fires');
    eq(reqIds.length >= 2, true, 'dedup: reqIds tracked');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 14. Submit retry after failure, committed recovery
  // ════════════════════════════════════════════════════════════════════════
  {
    let attempt = 0;
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'rt1', label: '重试', kind: 'text', required: true })}
        draftValue="x" onDraftChange={() => {}}
        onSubmit={async () => {
          attempt++;
          if (attempt === 1) return { revision: 1, duplicate: false, error: '失败', committed: false, recoverable: true };
          return { revision: 2, duplicate: false, committed: true, recoverable: true };
        }}
        onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const btn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-rt1"]');
    await interact(() => btn?.click()); await settle(50);
    eq(attempt, 1, 'retry: first attempt');
    ok(host.querySelector('[data-testid="work-input-error-task-fixture-rt1"]') !== null, 'retry: error');
    await interact(() => btn?.click()); await settle(50);
    eq(attempt, 2, 'retry: second attempt');
    ok(host.querySelector('[data-testid="work-input-rev-task-fixture-rt1"]') !== null, 'retry: revision after success');

    // committed=true wins over error/transport uncertainty and refreshes authority
    const refreshes: Array<{ operation: string; requestId: string; revision: number }> = [];
    const { host: h2, cleanup: c2 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'cr1', label: 'Recovery', kind: 'text', required: true })}
        draftValue="y" onDraftChange={() => {}}
        onSubmit={async (req) => ({
          revision: 5,
          duplicate: true,
          error: 'ACK 丢失',
          committed: true,
          recoverable: true,
          transportError: {
            code: 'ACK_LOST',
            message: 'ACK 丢失',
            committed: true,
            recoverable: true,
          },
          receipt: makeInputReceipt(req.requestId, 5, { inputId: req.inputId }),
        })}
        onPin={noopPin} onUnpin={noopUnpin}
        onRefreshAuthoritative={async (context) => { refreshes.push(context); }}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const btn2 = h2.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-cr1"]');
    await interact(() => btn2?.click()); await settle(50);
    const recoveryEl = h2.querySelector('[data-testid="work-input-recovery-task-fixture-cr1"]');
    ok(recoveryEl !== null, 'recovery: committed warning is explicit');
    ok(h2.querySelector('[data-testid="work-input-error-task-fixture-cr1"]') === null, 'recovery: committed result is not rejected');
    const revCR = h2.querySelector('[data-testid="work-input-rev-task-fixture-cr1"]');
    ok(revCR !== null, 'recovery: revision visible');
    contains(revCR?.textContent ?? '', '重复', 'recovery: duplicate visible');
    eq(refreshes.length, 1, 'recovery: authoritative refresh requested');
    eq(refreshes[0]?.operation, 'submit', 'recovery: submit refresh context');
    eq(refreshes[0]?.revision, 5, 'recovery: refresh revision');
    await cleanup(); await c2();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 15. Snapshot change invalidates a late submit promise
  // ════════════════════════════════════════════════════════════════════════
  {
    const dfd = deferred<SubmitInputResult>();
    let refreshCount = 0;
    const submit = async (): Promise<SubmitInputResult> => dfd.promise;
    const oldInput = makeWorkInput({ specId: 'late1', revision: 1, state: 'requested' });
    const newInput = makeWorkInput({ specId: 'late1', revision: 2, state: 'submitted', value: 'fresh' });
    const common = {
      inputSpec: makeSpec({ id: 'late1', label: '迟到结果', kind: 'text' as InputKind }),
      draftValue: 'fresh' as DraftValue,
      onDraftChange: () => {},
      onSubmit: submit,
      onPin: noopPin,
      onUnpin: noopUnpin,
      onRefreshAuthoritative: async () => { refreshCount++; },
      workId: WORK_ID,
      taskId: TASK_ID,
      runId: RUN_ID,
      blockId: BLOCK_ID,
      definitionRevision: DEF_REV,
      inputRevision: INPUT_REV,
    };
    const { host, root, cleanup } = await mount(
      <WorkInputHost {...common} workInput={oldInput} />,
    );
    const submitButton = host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-late1"]');
    await interact(() => submitButton?.click());

    await act(async () => {
      root.render(<WorkInputHost {...common} workInput={newInput} />);
      await new Promise<void>((resolveWait) => setTimeout(resolveWait, 20));
    });
    dfd.resolve({
      revision: 3,
      duplicate: false,
      error: '旧请求的迟到 ACK',
      committed: true,
      recoverable: true,
    });
    await settle(50);

    eq(
      host.querySelector('[data-testid="work-input-host-task-fixture-late1"]')?.getAttribute('data-input-state'),
      'submitted',
      'late: authoritative snapshot remains visible',
    );
    ok(host.querySelector('[data-testid="work-input-rev-task-fixture-late1"]') === null, 'late: old revision does not land');
    ok(host.querySelector('[data-testid="work-input-recovery-task-fixture-late1"]') === null, 'late: old recovery does not land');
    ok(host.querySelector('[data-testid="work-input-error-task-fixture-late1"]') === null, 'late: old error does not land');
    eq(refreshCount, 0, 'late: stale result does not request another refresh');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 16. Draft rebuild from props + isolation
  // ════════════════════════════════════════════════════════════════════════
  {
    let draft: DraftValue = '初始';
    const { host, root, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'dr1', label: '草稿', kind: 'text' })}
        draftValue={draft} onDraftChange={(v) => { draft = v; }}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    let ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-dr1"]');
    eq(ctrl?.value, '初始', 'draft: initial');

    // Snapshot reload
    await act(async () => {
      root.render(
        <WorkInputHost
          inputSpec={makeSpec({ id: 'dr1', label: '草稿', kind: 'text' })}
          draftValue="快照" onDraftChange={(v) => { draft = v; }}
          onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
          workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
          definitionRevision={DEF_REV} inputRevision={INPUT_REV}
        />,
      );
      await new Promise<void>((r) => setTimeout(r, 20));
    });
    ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-dr1"]');
    eq(ctrl?.value, '快照', 'draft: rebuilt from props');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 17. ARIA: label, describedby, required, role
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'a11y', label: 'A11y', kind: 'text', required: true, description: '描述' })}
        draftValue="" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const hostEl = host.querySelector('[data-testid="work-input-host-task-fixture-a11y"]');
    eq(hostEl?.getAttribute('role'), 'group', 'a11y: role=group');
    ok(hostEl?.getAttribute('aria-labelledby')?.includes('wg2-wh-label-') ?? false, 'a11y: aria-labelledby');
    ok(host.querySelector('[data-testid="work-input-desc-task-fixture-a11y"]') !== null, 'a11y: description');
    const ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-a11y"]');
    ok(ctrl?.getAttribute('aria-describedby')?.includes('wg2-wh-desc-') ?? false, 'a11y: aria-describedby');
    eq(ctrl?.getAttribute('aria-required'), 'true', 'a11y: aria-required');
    eq(ctrl?.getAttribute('aria-invalid'), 'true', 'a11y: aria-invalid on empty required');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 18. Disabled state
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'dis1', label: '禁用', kind: 'text' })}
        draftValue="" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV} disabled={true}
      />,
    );
    const ctrl = host.querySelector<HTMLInputElement>('[data-testid="work-input-control-task-fixture-dis1"]');
    ok(ctrl?.disabled ?? false, 'disabled: input');
    const btn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-dis1"]');
    ok(btn?.disabled ?? false, 'disabled: submit');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 19. Unknown kind → explicit alert
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'unk1', label: '未知', kind: 'bogus' as InputKind })}
        draftValue="" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const unk = host.querySelector('[data-testid="work-input-control-task-fixture-unk1"]');
    ok(unk !== null, 'unknown: error rendered');
    contains(unk?.textContent ?? '', '未知输入类型', 'unknown: msg');
    eq(unk?.getAttribute('role'), 'alert', 'unknown: role=alert');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 20. Schema errors: invalid JSON, kind mismatch, illegal risk/unit/pattern
  // ════════════════════════════════════════════════════════════════════════
  {
    // Invalid JSON
    const { host: h1, cleanup: c1 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'se1', label: '坏JSON', kind: 'text', valueSchema: 'not-json' })}
        draftValue="" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    ok(h1.querySelector('.wg2-wh-schema-err') !== null, 'schema-err: JSON parse failure rendered');
    contains(h1.textContent ?? '', '“坏JSON”的输入配置有误', 'schema-err: JSON error is human readable');

    // Kind mismatch
    const { host: h2, cleanup: c2 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'se2', label: 'KindMismatch', kind: 'text', valueSchema: JSON.stringify({ kind: 'number' }) })}
        draftValue="" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    ok(h2.querySelector('.wg2-wh-schema-err') !== null, 'schema-err: kind mismatch rendered');
    contains(h2.textContent ?? '', '“KindMismatch”的输入配置有误', 'schema-err: kind mismatch is human readable');

    // Illegal riskLevel
    const { host: h3, cleanup: c3 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'se3', label: '坏Risk', kind: 'approval', valueSchema: JSON.stringify({ riskLevel: 'extreme' }) })}
        draftValue="" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    ok(h3.querySelector('.wg2-wh-schema-err') !== null, 'schema-err: illegal risk rendered');
    contains(h3.textContent ?? '', '“坏Risk”的输入配置有误', 'schema-err: invalid risk is human readable');
    ok(!h3.textContent?.includes('extreme'), 'schema-err: invalid internal value stays hidden');

    // Illegal unit
    const { host: h4, cleanup: c4 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'se4', label: '坏Unit', kind: 'number', valueSchema: JSON.stringify({ unit: 'kilograms' }) })}
        draftValue={0} onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    ok(h4.querySelector('.wg2-wh-schema-err') !== null, 'schema-err: illegal unit rendered');

    // Invalid regex pattern
    const { host: h5, cleanup: c5 } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'se5', label: '坏Pattern', kind: 'text', valueSchema: JSON.stringify({ pattern: '[invalid' }) })}
        draftValue="" onDraftChange={() => {}} onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    ok(h5.querySelector('.wg2-wh-schema-err') !== null, 'schema-err: bad pattern rendered');
    contains(h5.textContent ?? '', '“坏Pattern”的输入配置有误', 'schema-err: pattern error is human readable');

    await c1(); await c2(); await c3(); await c4(); await c5();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 20. Pin state from committed WorkInput
  // ════════════════════════════════════════════════════════════════════════
  {
    const wi = makeWorkInput({ specId: 'ps1', cornerstoneId: 'cs-1', value: '"val"' });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'ps1', label: '已固定', kind: 'text', pinEligible: true })}
        workInput={wi} draftValue="val" onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const hostEl = host.querySelector('[data-testid="work-input-host-task-fixture-ps1"]');
    eq(hostEl?.getAttribute('data-pinned'), 'true', 'pin-state: data-pinned=true');
    const btn = host.querySelector('[data-testid="work-input-pin-task-fixture-ps1"]');
    eq(btn?.getAttribute('aria-pressed'), 'true', 'pin-state: pressed');
    contains(btn?.textContent ?? '', '📌', 'pin-state: icon');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 21. Transport error with revision conflict
  // ════════════════════════════════════════════════════════════════════════
  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'te1', label: '冲突', kind: 'text', required: true })}
        draftValue="x" onDraftChange={() => {}}
        onSubmit={async () => ({
          revision: 1, duplicate: false, error: 'revision conflict', committed: false, recoverable: true,
          transportError: { code: 'CONFLICT', message: 'Revision conflict', committed: false, recoverable: true },
        })}
        onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const btn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-te1"]');
    await interact(() => btn?.click()); await settle(50);
    ok(host.querySelector('[data-testid="work-input-error-task-fixture-te1"]') !== null, 'transport: error');
    contains(host.textContent ?? '', 'revision conflict', 'transport: msg');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 22. Unpin tombstone: pinned=false, committed=true
  // ════════════════════════════════════════════════════════════════════════
  {
    const wi = makeWorkInput({ specId: 'ut1', cornerstoneId: 'cs-old' });
    const unpinReqs: SetInputCornerstoneRequest[] = [];
    let unpinRefresh = '';
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'ut1', label: 'UnpinTomb', kind: 'text', pinEligible: true })}
        workInput={wi} draftValue="x" onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={noopPin}
        onUnpin={async (req) => { unpinReqs.push(req); return { pinned: false, revision: 3, duplicate: false, committed: true, recoverable: true }; }}
        onRefreshAuthoritative={async (context) => { unpinRefresh = `${context.operation}:${context.revision}`; }}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    const btn = host.querySelector<HTMLButtonElement>('[data-testid="work-input-pin-task-fixture-ut1"]');
    await interact(() => btn?.click()); await settle(50);
    eq(unpinReqs.length >= 1, true, 'unpin: request sent');
    if (unpinReqs.length > 0) {
      eq(unpinReqs[0].pin, false, 'unpin: DTO.pin=false');
      ok(unpinReqs[0].requestId.length > 0, 'unpin: DTO.requestId');
    }
    eq(
      host.querySelector('[data-testid="work-input-host-task-fixture-ut1"]')?.getAttribute('data-pinned'),
      'false',
      'unpin: committed tombstone updates local feedback',
    );
    eq(unpinRefresh, 'unpin:3', 'unpin: authoritative refresh requested');
    await cleanup();
  }

  // ════════════════════════════════════════════════════════════════════════
  // 23. Committed input state reflected
  // ════════════════════════════════════════════════════════════════════════
  {
    const wi = makeWorkInput({ specId: 'cv1', value: '"saved"', state: 'submitted', revision: 5 });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'cv1', label: '已提交', kind: 'text' })}
        workInput={wi} draftValue="saved" onDraftChange={() => {}}
        onSubmit={noopSubmit} onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={INPUT_REV}
      />,
    );
    eq(host.querySelector('[data-testid="work-input-host-task-fixture-cv1"]')?.getAttribute('data-input-state'), 'submitted', 'committed: state');
    await cleanup();
  }

  // Committed/ACK-lost recovery awaits the authoritative refresh and keeps
  // both the typed receipt and a refresh failure visible.
  {
    const wi = makeWorkInput({ specId: 'refresh-fail', revision: 7 });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'refresh-fail', label: '恢复输入', kind: 'text' })}
        workInput={wi}
        draftValue="value"
        onDraftChange={() => {}}
        onSubmit={async (req) => ({
          revision: 8,
          duplicate: true,
          committed: true,
          recoverable: true,
          transportError: {
            code: 'committed_recovery',
            message: 'ACK 丢失',
            operation: 'SubmitWorkInput',
            committed: true,
            recoverable: true,
          },
          receipt: makeInputReceipt(req.requestId, 8, { inputId: req.inputId }),
        })}
        onPin={noopPin}
        onUnpin={noopUnpin}
        onRefreshAuthoritative={async () => { throw new Error('刷新失败'); }}
        workId={WORK_ID}
        taskId={TASK_ID}
        runId={RUN_ID}
        blockId={BLOCK_ID}
        definitionRevision={DEF_REV}
        inputRevision={wi.revision}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-refresh-fail"]')?.click());
    const recovery = host.querySelector('[data-testid="work-input-recovery-task-fixture-refresh-fail"]')?.textContent ?? '';
    contains(recovery, 'input-submit-', 'refresh failure: typed request receipt remains visible');
    contains(recovery, 'r8', 'refresh failure: committed revision remains visible');
    contains(recovery, '刷新失败', 'refresh failure: awaited snapshot failure is explicit');
    await cleanup();
  }

  // Native selection stays typed in the draft while the frozen backend file
  // schema receives stable ArtifactRef IDs.
  {
    const wi = makeWorkInput({ specId: 'file-wire' });
    let wireValue: unknown;
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'file-wire', label: '文件线协议', kind: 'file' })}
        workInput={wi}
        draftValue={[{
          id: 'artifact-wire-id',
          name: 'wire.txt',
          type: 'text/plain',
          status: 'available',
          relativePath: 'inputs/wire.txt',
        }]}
        onDraftChange={() => {}}
        onSubmit={async (request) => {
          wireValue = request.value;
          return { revision: 2, duplicate: false, committed: true, recoverable: false };
        }}
        onPin={noopPin}
        onUnpin={noopUnpin}
        workId={WORK_ID}
        taskId={TASK_ID}
        runId={RUN_ID}
        blockId={BLOCK_ID}
        definitionRevision={DEF_REV}
        inputRevision={wi.revision}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-file-wire"]')?.click());
    eq(JSON.stringify(wireValue), JSON.stringify(['artifact-wire-id']), 'file wire: typed ArtifactRef submits stable IDs');
    await cleanup();
  }

  // A refresh continuation belongs to the exact operation epoch and full
  // authority identity. Switching input identity makes both resolve and
  // reject from the old refresh inert.
  for (const outcome of ['resolve', 'reject'] as const) {
    const refresh = deferred<void>();
    const oldInput = makeWorkInput({ id: `old-${outcome}`, specId: `late-refresh-${outcome}`, revision: 1 });
    const newInput = makeWorkInput({
      id: `new-${outcome}`,
      specId: `late-refresh-${outcome}`,
      revision: 1,
      state: 'requested',
    });
    const spec = makeSpec({ id: `late-refresh-${outcome}`, label: `迟到刷新 ${outcome}`, kind: 'text' });
    const common = {
      inputSpec: spec,
      draftValue: 'value' as DraftValue,
      onDraftChange: () => {},
      onSubmit: async (request: SubmitWorkInputRequest): Promise<SubmitInputResult> => ({
        revision: 2,
        duplicate: false,
        committed: true,
        recoverable: false,
        receipt: makeInputReceipt(request.requestId, 2, { inputId: request.inputId }),
      }),
      onPin: noopPin,
      onUnpin: noopUnpin,
      onRefreshAuthoritative: async () => refresh.promise,
      workId: WORK_ID,
      taskId: TASK_ID,
      runId: RUN_ID,
      blockId: BLOCK_ID,
      definitionRevision: DEF_REV,
      inputRevision: 1,
    };
    const { host, root, cleanup } = await mount(<WorkInputHost {...common} workInput={oldInput} />);
    await interact(() => host.querySelector<HTMLButtonElement>(
      `[data-testid="work-input-submit-${TASK_ID}-${spec.id}"]`,
    )?.click());
    await act(async () => {
      root.render(<WorkInputHost {...common} workInput={newInput} />);
      await new Promise<void>((resolveWait) => setTimeout(resolveWait, 20));
    });
    if (outcome === 'resolve') refresh.resolve();
    else refresh.reject(new Error('旧刷新失败'));
    await settle(50);
    ok(
      host.querySelector(`[data-testid="work-input-rev-${TASK_ID}-${spec.id}"]`) === null,
      `late-refresh-${outcome}: old committed revision does not pollute new identity`,
    );
    ok(
      host.querySelector(`[data-testid="work-input-recovery-${TASK_ID}-${spec.id}"]`) === null,
      `late-refresh-${outcome}: old refresh continuation does not expose recovery`,
    );
    ok(
      !(host.textContent ?? '').includes('旧刷新失败'),
      `late-refresh-${outcome}: old refresh error stays invisible`,
    );
    await cleanup();
  }

  // committed=true without the frozen InputIntentReceipt is a recoverable
  // protocol failure. The host still refreshes authority and never fabricates
  // receipt identity from the client request/revision.
  {
    let refreshed = 0;
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'missing-receipt-submit', label: '缺 receipt', kind: 'text' })}
        draftValue="value"
        onDraftChange={() => {}}
        onSubmit={async () => ({
          revision: 9,
          duplicate: false,
          committed: true,
          recoverable: true,
        })}
        onPin={noopPin}
        onUnpin={noopUnpin}
        onRefreshAuthoritative={async () => { refreshed++; }}
        workId={WORK_ID}
        taskId={TASK_ID}
        runId={RUN_ID}
        blockId={BLOCK_ID}
        definitionRevision={DEF_REV}
        inputRevision={INPUT_REV}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>(
      '[data-testid="work-input-submit-task-fixture-missing-receipt-submit"]',
    )?.click());
    const warning = host.querySelector(
      '[data-testid="work-input-recovery-task-fixture-missing-receipt-submit"]',
    )?.textContent ?? '';
    contains(warning, '缺少 InputIntentReceipt', 'receipt-contract: submit missing receipt is explicit');
    ok(!warning.includes('input-submit-'), 'receipt-contract: client requestId is not displayed as receipt');
    eq(refreshed, 1, 'receipt-contract: submit still refreshes authority');
    await cleanup();
  }

  {
    const input = makeWorkInput({
      id: 'missing-receipt-pin-input',
      specId: 'missing-receipt-pin',
      cornerstoneId: 'existing-cornerstone',
    });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'missing-receipt-pin', label: '缺 pin receipt', kind: 'text', pinEligible: true })}
        workInput={input}
        draftValue="value"
        onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={noopPin}
        onUnpin={async () => ({
          pinned: false,
          revision: 3,
          duplicate: false,
          committed: true,
          recoverable: true,
        })}
        onRefreshAuthoritative={async () => {}}
        workId={WORK_ID}
        taskId={TASK_ID}
        runId={RUN_ID}
        blockId={BLOCK_ID}
        definitionRevision={DEF_REV}
        inputRevision={input.revision}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>(
      '[data-testid="work-input-pin-task-fixture-missing-receipt-pin"]',
    )?.click());
    const warning = host.querySelector(
      '[data-testid="work-input-pin-recovery-task-fixture-missing-receipt-pin"]',
    )?.textContent ?? '';
    contains(warning, '缺少 InputIntentReceipt', 'receipt-contract: unpin missing receipt is explicit');
    ok(!warning.includes('input-unpin-'), 'receipt-contract: unpin client requestId is not displayed as receipt');
    await cleanup();
  }

  // ── workRevision regression: expectedRevision vs inputRevision ──
  // Submit DTO must send workRevision as expectedRevision, not input revision.
  {
    const submitReqs: SubmitWorkInputRequest[] = [];
    const wi = makeWorkInput({ specId: 'wr-sub', revision: 0 });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'wr-sub', label: 'WorkRevSubmit', kind: 'text', required: false })}
        workInput={wi}
        draftValue="test" onDraftChange={() => {}}
        onSubmit={async (req) => { submitReqs.push(req); return { revision: 28, duplicate: false, committed: true, recoverable: false }; }}
        onPin={noopPin} onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={0} workRevision={WORK_REV}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="work-input-submit-task-fixture-wr-sub"]')?.click());
    eq(submitReqs.length >= 1, true, 'workRev: submit fired');
    if (submitReqs.length > 0) {
      eq(submitReqs[0].expectedRevision, WORK_REV, 'workRev: submit expectedRevision=27 (work revision)');
      eq(submitReqs[0].inputRevision, 0, 'workRev: submit inputRevision=0 (input revision)');
      eq(submitReqs[0].definitionRevision, DEF_REV, 'workRev: submit definitionRevision=2');
    }
    await cleanup();
  }

  // Pin DTO must send workRevision as expectedRevision, not input revision.
  {
    const pinReqs: SetInputCornerstoneRequest[] = [];
    const wi = makeWorkInput({ specId: 'wr-pin', revision: 1 });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'wr-pin', label: 'WorkRevPin', kind: 'text', required: false, pinEligible: true })}
        workInput={wi}
        draftValue="test" onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={async (req) => { pinReqs.push(req); return { cornerstoneId: 'cs-wr', pinned: true, revision: 28, duplicate: false, committed: true, recoverable: false }; }}
        onUnpin={noopUnpin}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={1} workRevision={WORK_REV}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="work-input-pin-task-fixture-wr-pin"]')?.click());
    eq(pinReqs.length >= 1, true, 'workRev: pin fired');
    if (pinReqs.length > 0) {
      eq(pinReqs[0].expectedRevision, WORK_REV, 'workRev: pin expectedRevision=27 (work revision)');
      eq(pinReqs[0].inputRevision, 1, 'workRev: pin inputRevision=1 (input revision)');
      eq(pinReqs[0].pin, true, 'workRev: pin DTO.pin=true');
    }
    await cleanup();
  }

  // Unpin DTO must also use workRevision for expectedRevision.
  {
    const unpinReqs: SetInputCornerstoneRequest[] = [];
    const wi = makeWorkInput({ specId: 'wr-unpin', revision: 0, cornerstoneId: 'cs-old' });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({ id: 'wr-unpin', label: 'WorkRevUnpin', kind: 'text', required: false, pinEligible: true })}
        workInput={wi}
        draftValue="test" onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={noopPin}
        onUnpin={async (req) => { unpinReqs.push(req); return { pinned: false, revision: 28, duplicate: false, committed: true, recoverable: false }; }}
        workId={WORK_ID} taskId={TASK_ID} runId={RUN_ID} blockId={BLOCK_ID}
        definitionRevision={DEF_REV} inputRevision={0} workRevision={WORK_REV}
      />,
    );
    await interact(() => host.querySelector<HTMLButtonElement>('[data-testid="work-input-pin-task-fixture-wr-unpin"]')?.click());
    eq(unpinReqs.length >= 1, true, 'workRev: unpin fired');
    if (unpinReqs.length > 0) {
      eq(unpinReqs[0].expectedRevision, WORK_REV, 'workRev: unpin expectedRevision=27 (work revision)');
      eq(unpinReqs[0].inputRevision, 0, 'workRev: unpin inputRevision=0 (input revision)');
      eq(unpinReqs[0].pin, false, 'workRev: unpin DTO.pin=false');
    }
    await cleanup();
  }

  // Legacy planner output may encode choice options as an object map. The
  // current UI must recover it instead of exposing a schema implementation
  // error or blocking the whole input block.
  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({
          id: 'method',
          label: '学习方式',
          kind: 'choice',
          required: true,
          valueSchema: JSON.stringify({
            options: { visual: '视觉学习', audio: '听觉学习' },
          }),
        })}
        draftValue=""
        onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={noopPin}
        onUnpin={noopUnpin}
        workId={WORK_ID}
        taskId={TASK_ID}
        runId={RUN_ID}
        blockId={BLOCK_ID}
        definitionRevision={DEF_REV}
        inputRevision={INPUT_REV}
      />,
    );
    const select = host.querySelector<HTMLSelectElement>(
      '[data-testid="work-input-control-task-fixture-method-select"]',
    );
    ok(select !== null, 'choice recovery: object-map options render a select');
    eq(select?.options.length, 3, 'choice recovery: placeholder plus two recovered options');
    contains(select?.textContent ?? '', '视觉学习', 'choice recovery: recovered label is visible');
    ok(!host.textContent?.includes('[method/choice]'), 'choice recovery: internal schema identity stays hidden');
    await cleanup();
  }

  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({
          id: 'broken-method',
          label: '学习方式',
          kind: 'choice',
          required: true,
          valueSchema: JSON.stringify({ options: 42 }),
        })}
        draftValue=""
        onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={noopPin}
        onUnpin={noopUnpin}
        workId={WORK_ID}
        taskId={TASK_ID}
        runId={RUN_ID}
        blockId={BLOCK_ID}
        definitionRevision={DEF_REV}
        inputRevision={INPUT_REV}
      />,
    );
    contains(
      host.querySelector('[role="alert"]')?.textContent ?? '',
      '“学习方式”的选项配置有误，请重新规划工作结构后重试。',
      'choice recovery: irrecoverable schema error is human readable',
    );
    ok(!host.textContent?.includes('options 必须是数组'), 'choice recovery: parser detail stays hidden');
    await cleanup();
  }

  {
    const spec = makeSpec({
      id: 'manual-activities',
      label: '活动安排',
      kind: 'multi_choice',
      required: true,
      valueSchema: undefined,
    });
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={spec}
        draftValue=""
        onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={noopPin}
        onUnpin={noopUnpin}
        workId={WORK_ID}
        taskId={TASK_ID}
        runId={RUN_ID}
        blockId={BLOCK_ID}
        definitionRevision={DEF_REV}
        inputRevision={INPUT_REV}
      />,
    );
    const manual = host.querySelector<HTMLTextAreaElement>(
      '[data-testid="work-input-control-task-fixture-manual-activities-manual"]',
    );
    ok(manual !== null, 'multi fallback: missing options renders an editable textarea');
    eq(manual?.placeholder, '每行填写一项', 'multi fallback: input format is explicit');
    const parsed = parseValueSchema(spec.id, spec.kind, spec.valueSchema);
    eq(
      validateDraft(spec, '晨读\n听力训练', parsed),
      null,
      'multi fallback: manual text satisfies required validation',
    );
    const wire = toWireValue(
      'multi_choice',
      '晨读\n听力训练\n晨读',
      parsed,
    );
    eq(JSON.stringify(wire), JSON.stringify(['晨读', '听力训练']), 'multi fallback: manual lines submit as a deduplicated string array');
    await cleanup();
  }

  {
    const { host, cleanup } = await mount(
      <WorkInputHost
        inputSpec={makeSpec({
          id: 'manual-method',
          label: '学习方式',
          kind: 'choice',
          required: true,
          valueSchema: undefined,
        })}
        draftValue=""
        onDraftChange={() => {}}
        onSubmit={noopSubmit}
        onPin={noopPin}
        onUnpin={noopUnpin}
        workId={WORK_ID}
        taskId={TASK_ID}
        runId={RUN_ID}
        blockId={BLOCK_ID}
        definitionRevision={DEF_REV}
        inputRevision={INPUT_REV}
      />,
    );
    ok(
      host.querySelector('[data-testid="work-input-control-task-fixture-manual-method-manual"]') !== null,
      'choice fallback: missing options renders a text input',
    );
    await cleanup();
  }

  // ── Report ────────────────────────────────────────────────────
  process.stdout.write(`\n${passed} passed, ${failed} failed, ${passed + failed} total\n`);
  if (failed > 0) { process.exitCode = 1; }
}

runTests();
