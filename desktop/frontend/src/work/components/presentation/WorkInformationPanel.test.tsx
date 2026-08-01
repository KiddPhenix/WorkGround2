import { JSDOM } from 'jsdom';
import { act } from 'react';
import { createRoot } from 'react-dom/client';

import { useWorkUIStore } from '../../store';
import type {
  CornerstonePinResult,
  InferWorkInputsRequest,
  InferWorkInputsResult,
  SubmitInputResult,
  WorkDefinitionRevision,
  WorkInput,
} from '../../types_v2';
import { WorkInformationPanel } from './WorkInformationPanel';

let passed = 0;
let failed = 0;
function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? 'PASS' : 'FAIL'}  ${label}\n`);
  condition ? passed++ : failed++;
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
  SVGElement: dom.window.SVGElement,
  Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent,
  KeyboardEvent: dom.window.KeyboardEvent,
});

const definition: WorkDefinitionRevision = {
  workId: 'work-1',
  revision: 2,
  parentRevision: 1,
  status: 'active',
  goal: '测试独立信息面板',
  nodes: [{ id: 'node-1', title: '准备方案', inputSpecIds: ['name', 'budget'] }],
  artifactSlots: [],
  inputSpecs: [
    { id: 'name', label: '活动名称', kind: 'text', required: true, pinEligible: false },
    {
      id: 'budget',
      label: '预算',
      kind: 'number',
      required: true,
      pinEligible: false,
      valueSchema: { min: 0, max: 100 },
    },
  ],
  createdBy: 'test',
  createdAt: '2026-07-30T00:00:00Z',
  digest: 'test',
};

const inputs: WorkInput[] = definition.inputSpecs.map((spec, index) => ({
  id: `input-${index + 1}`,
  workId: 'work-1',
  runId: 'run-1',
  taskId: 'task-1',
  blockId: 'block-1',
  specId: spec.id,
  value: null,
  state: 'requested',
  revision: 0,
  updatedAt: '2026-07-30T00:00:00Z',
}));

const submitResult: SubmitInputResult = {
  revision: 4,
  duplicate: false,
  committed: true,
  recoverable: true,
};
const pinResult: CornerstonePinResult = {
  pinned: false,
  revision: 4,
  duplicate: false,
  committed: true,
  recoverable: true,
};

async function main(): Promise<void> {
  window.localStorage.clear();
  useWorkUIStore.setState((state) => ({ ...state, cardByWork: {} }));
  const host = document.createElement('div');
  document.body.appendChild(host);
  const root = createRoot(host);
  const inferRequests: InferWorkInputsRequest[] = [];
  let budgetAttempts = 0;
  let resolveNameSuggestion: ((result: InferWorkInputsResult) => void) | undefined;
  const nameSuggestion = new Promise<InferWorkInputsResult>((resolve) => {
    resolveNameSuggestion = resolve;
  });
  const inferInput = async (request: InferWorkInputsRequest) => {
    inferRequests.push(request);
    if (request.inputIds?.[0] === 'input-2') {
      budgetAttempts++;
      if (budgetAttempts === 1) throw new Error('模型暂时不可用');
      return {
        items: [],
        skipped: [{ inputId: 'input-2', reason: '预算需要用户决定' }],
      };
    }
    return nameSuggestion;
  };

  await act(async () => {
    root.render(
      <WorkInformationPanel
        workId="work-1"
        runId="run-1"
        workRevision={3}
        definition={definition}
        tasks={[{
          id: 'task-1',
          runId: 'run-1',
          nodeId: 'node-1',
          title: '准备方案',
          state: 'waiting_input',
          retryable: false,
          updatedAt: '2026-07-30T00:00:00Z',
        }]}
        inputs={inputs}
        onSubmit={async () => submitResult}
        onPin={async () => pinResult}
        onUnpin={async () => pinResult}
        onRefresh={async () => {}}
        onInfer={inferInput}
      />,
    );
  });

  ok(host.textContent?.includes('工作信息') === true, 'keeps persistent work information list');
  ok(host.textContent?.includes('0/2 已填写') === true, 'shows progress');
  ok(host.querySelector('[role="dialog"]') === null, 'form layer starts closed');
  ok(host.querySelectorAll('.work-definition-overview__field').length === 2, 'renders each information entry');

  ok(host.textContent?.includes('自己推断') === false, 'removes the panel-wide inference action');
  ok(host.querySelectorAll('.work-definition-overview__field-suggest').length === 2, 'shows one suggestion icon per pending field');
  ok([...host.querySelectorAll('.work-definition-overview__field-suggest')]
    .every((button) => button.textContent?.trim() === ''), 'keeps suggestion actions icon-only');
  const nameSuggest = host.querySelector<HTMLButtonElement>('[aria-label="为“活动名称”生成建议"]');
  await act(async () => nameSuggest?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(inferRequests.length === 1 && inferRequests[0]?.inputIds?.join(',') === 'input-1', 'requests only the selected field');
  ok(nameSuggest?.disabled === true
    && host.querySelector<HTMLButtonElement>('[aria-label="为“预算”生成建议"]')?.disabled === false,
  'busy state disables only the selected field');
  await act(async () => resolveNameSuggestion?.({
    items: [{ inputId: 'input-1', value: '自动建议的活动名称', reason: '依据工作目标' }],
    skipped: [],
  }));
  ok(host.textContent?.includes('建议依据：依据工作目标') === true, 'shows the selected field suggestion reason');
  ok(host.querySelector<HTMLInputElement>('input')?.value === '自动建议的活动名称', 'stages only the selected field as a reviewable draft');
  const inferredClose = host.querySelector<HTMLButtonElement>('[aria-label="关闭填写信息"]');
  await act(async () => inferredClose?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.querySelector('[role="dialog"]') === null, 'inferred draft remains reviewable without auto-submit');

  let budgetSuggest = host.querySelector<HTMLButtonElement>('[aria-label="为“预算”生成建议"]');
  await act(async () => budgetSuggest?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.textContent?.includes('模型暂时不可用，可以重试') === true, 'isolates a field failure and keeps retry visible');
  budgetSuggest = host.querySelector<HTMLButtonElement>('[aria-label="为“预算”生成建议"]');
  await act(async () => budgetSuggest?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(inferRequests.length === 3 && inferRequests.every((request) => request.inputIds?.length === 1), 'retries only the failed field');
  ok(host.textContent?.includes('暂时无法建议：预算需要用户决定') === true, 'shows a field-specific skip reason');

  const firstEntry = host.querySelector<HTMLButtonElement>('[aria-label="填写：活动名称"]');
  await act(async () => firstEntry?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.querySelector('[role="dialog"]') !== null, 'clicking an entry opens the upper form layer');
  ok(host.querySelectorAll('.wg2-info-stack__back').length === 1, 'renders stacked-card depth');
  ok(host.textContent?.includes('活动名称') === true, 'shows first pending item');
  ok(host.textContent?.includes('填完后继续下一项') === true, 'defaults to continue next');

  const advanced = [...host.querySelectorAll('button')].find((button) =>
    button.textContent?.includes('高级'));
  await act(async () => advanced?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.querySelector('textarea[placeholder*="预期之外"]') !== null, 'advanced context opens');

  const close = host.querySelector<HTMLButtonElement>('[aria-label="关闭填写信息"]');
  await act(async () => close?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.textContent?.includes('工作信息') === true, 'close keeps work information list visible');
  ok(host.querySelector('[role="dialog"]') === null, 'close removes only the upper form layer');

  const completedInputs = [
    { ...inputs[0], value: '夏日团建', state: 'submitted' as const, revision: 1 },
    inputs[1],
  ];
  let finishEditSubmit: ((result: SubmitInputResult) => void) | undefined;
  const editSubmit = new Promise<SubmitInputResult>((resolve) => {
    finishEditSubmit = resolve;
  });
  await act(async () => {
    root.render(
      <WorkInformationPanel
        workId="work-1"
        runId="run-1"
        workRevision={4}
        definition={definition}
        tasks={[]}
        inputs={completedInputs}
        onSubmit={() => editSubmit}
        onRefresh={async () => {}}
        onAddCustom={async () => submitResult}
        onInfer={inferInput}
      />,
    );
  });
  const completedSuggest = host.querySelector<HTMLButtonElement>('[aria-label="为“活动名称”生成建议"]');
  ok(completedSuggest?.textContent?.trim() === '', 'completed information keeps the same additive suggestion icon');
  await act(async () => completedSuggest?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.querySelector<HTMLInputElement>('input')?.value === '自动建议的活动名称', 'replacement suggestion opens as a draft without overwriting the saved value');
  const completedSuggestClose = host.querySelector<HTMLButtonElement>('[aria-label="关闭填写信息"]');
  await act(async () => completedSuggestClose?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  const completedEntry = host.querySelector<HTMLButtonElement>('[aria-label="修改：活动名称"]');
  await act(async () => completedEntry?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.textContent?.includes('修改已填写信息') === true, 'completed information opens in edit mode');
  ok(host.textContent?.includes('保存修改') === true, 'completed information uses edit action');
  const saveEdit = [...host.querySelectorAll<HTMLButtonElement>('button')].find((button) =>
    button.textContent?.includes('保存修改'));
  await act(async () => saveEdit?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.querySelector('[role="dialog"]') !== null, 'edit remains visible before an authoritative commit');
  await act(async () => {
    root.render(
      <WorkInformationPanel
        workId="work-1"
        runId="run-1"
        workRevision={5}
        definition={definition}
        tasks={[]}
        inputs={[
          { ...completedInputs[0], value: '夏日团建 v2', revision: 2 },
          completedInputs[1],
        ]}
        onSubmit={() => editSubmit}
        onRefresh={async () => {}}
        onAddCustom={async () => submitResult}
      />,
    );
  });
  ok(host.querySelector('[role="dialog"]') === null, 'authoritative edit commit closes the panel before rerun completion');
  await act(async () => finishEditSubmit?.(submitResult));

  const addButton = [...host.querySelectorAll('button')].find((button) =>
    button.textContent?.includes('添加信息'));
  await act(async () => addButton?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
  ok(host.textContent?.includes('新增工作信息') === true, 'add information opens upper panel');
  ok(host.querySelector('input[placeholder*="参考资料"]') !== null, 'custom information asks for a name');
  ok(host.querySelector('input[placeholder*="用途"]') !== null, 'custom information explanation is optional');
  ok(host.querySelector('textarea[placeholder*="持续参考"]') !== null, 'custom information supports text content');
  ok(host.querySelectorAll('[role="radio"][aria-label=""]').length === 0
    && host.querySelectorAll('.wg2-info-add__kind [role="radio"]').length === 2,
  'custom information offers text and file types');

  await act(async () => root.unmount());
  host.remove();
  process.stdout.write(`\n${passed} passed, ${failed} failed, ${passed + failed} total\n`);
  if (failed) process.exit(1);
}

void main();
