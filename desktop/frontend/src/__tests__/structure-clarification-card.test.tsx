// Run: tsx src/__tests__/structure-clarification-card.test.tsx

import { JSDOM } from 'jsdom';
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';

import { StructureClarificationCard } from '../components/work/StructureClarificationCard';
import type { DefinitionStructuralAnswer, DefinitionStructuralClarification } from '../work/types_v2';

let failed = 0;
function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? 'PASS' : 'FAIL'}  ${label}\n`);
  if (!value) failed++;
}

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
  pretendToBeVisual: true,
  url: 'http://localhost/',
});
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.Element = dom.window.Element;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
Object.defineProperty(dom.window.HTMLElement.prototype, 'attachEvent', { configurable: true, value: () => {} });
Object.defineProperty(dom.window.HTMLElement.prototype, 'detachEvent', { configurable: true, value: () => {} });

const clarification: DefinitionStructuralClarification = {
  id: 'report_topology',
  impact: 'task_dependencies',
  question: 'A、B 两组报告应独立并行处理，还是 B 组必须使用 A 组的结果？',
  description: '任务说明没有给出两组报告之间的数据依赖，两种结构都会改变执行拓扑。',
  flow: ['处理 A 组报告', '处理 B 组报告', '生成汇总'],
  options: [
    { id: 'parallel', label: '两组独立并行' },
    { id: 'a_then_b', label: 'A 完成后处理 B' },
    { id: 'custom', label: '自定义依赖关系', custom: true },
  ],
  customPlaceholder: '例如：先处理 A，B 只使用 A 的结论',
};

const answers: DefinitionStructuralAnswer[] = [];
let closed = 0;
const root = createRoot(document.getElementById('root')!);
await act(async () => {
  root.render(
    <StructureClarificationCard
      clarification={clarification}
      busy={false}
      onClose={() => { closed++; }}
      onSubmit={(answer) => { answers.push(answer); }}
    />,
  );
});

const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
ok(dialog?.getAttribute('aria-modal') === 'true', 'answer card is a modal dialog');
ok(dialog?.textContent?.includes('只补充无法推导的工作结构') === true, 'scope is limited to non-inferable work structure');
ok(dialog?.textContent?.includes('会改变任务节点与依赖') === true, 'structural impact is explicit');
ok(document.querySelector('[role="radio"][data-selected="true"]') === null, 'non-inferable choice has no preselected default');
const initialConfirm = [...document.querySelectorAll<HTMLButtonElement>('button')]
  .find((button) => button.textContent?.includes('采用此结构并继续'));
ok(initialConfirm?.disabled === true, 'user must explicitly select a structural alternative');

const custom = [...document.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
  .find((button) => button.textContent?.includes('自定义依赖关系'));
await act(async () => custom?.click());
const textarea = document.querySelector<HTMLTextAreaElement>('[data-testid="structure-clarification-custom"]');
ok(!!textarea, 'custom structure input is available');
await act(async () => {
  if (!textarea) return;
  const previous = textarea.value;
  const setter = Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype, 'value')?.set;
  setter?.call(textarea, '节点 2 后确认');
  (textarea as HTMLTextAreaElement & { _valueTracker?: { setValue: (next: string) => void } })._valueTracker?.setValue(previous);
  const propsKey = Object.keys(textarea).find((key) => key.startsWith('__reactProps$'));
  const props = propsKey
    ? (textarea as unknown as Record<string, { onChange?: (event: { target: HTMLTextAreaElement }) => void }>)[propsKey]
    : undefined;
  props?.onChange?.({ target: textarea });
});
const confirm = [...document.querySelectorAll<HTMLButtonElement>('button')]
  .find((button) => button.textContent?.includes('采用此结构并继续'));
await act(async () => confirm?.click());
ok(answers[0]?.questionId === 'report_topology' && answers[0]?.value === '节点 2 后确认', 'custom answer preserves structure identity');

await act(async () => document.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
ok(closed === 1, 'Escape closes without discarding the pending question');

await act(async () => root.unmount());
dom.window.close();
if (failed > 0) process.exitCode = 1;
