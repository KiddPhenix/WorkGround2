import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';

import type {
  InputKind,
  AddCustomWorkInputRequest,
  SelectWorkInputFileResult,
  SubmitWorkInputRequest,
  WorkDefinitionRevision,
  WorkInput,
} from '../../types_v2';
import { useWorkUIStore } from '../../store';
import { WorkInformationPanel } from './WorkInformationPanel';
import '../../../styles.css';
import '../v2/input/WorkInputHost.css';
import './WorkInformationPanel.css';
import './WorkInformationPreview.css';

const kinds: Array<{ id: string; label: string; kind: InputKind; schema?: unknown; value?: unknown }> = [
  { id: 'short', label: '这次活动的名称是什么？', kind: 'text' },
  {
    id: 'date',
    label: '计划在什么日期或时间开展？',
    kind: 'date',
    schema: { mode: 'datetime' },
  },
  {
    id: 'list',
    label: '你偏爱哪种类型的团建活动？',
    kind: 'choice',
    schema: { options: [{ value: 'outdoor', label: '户外活动' }, { value: 'indoor', label: '室内活动' }] },
  },
  {
    id: 'multi',
    label: '希望活动包含哪些环节？',
    kind: 'multi_choice',
    schema: { options: [{ value: 'game', label: '协作游戏' }, { value: 'meal', label: '聚餐' }, { value: 'share', label: '分享' }] },
  },
  {
    id: 'number',
    label: '人均预算或总预算大约是多少元？',
    kind: 'number',
    schema: { min: 100, max: 3000, unit: 'amount', currency: 'CNY' },
    value: 99999,
  },
  { id: 'file', label: '上传参考资料或需求文件', kind: 'file', schema: { acceptTypes: ['pdf', 'docx'] } },
];

const definition: WorkDefinitionRevision = {
  workId: 'preview-work',
  revision: 1,
  parentRevision: 0,
  status: 'active',
  goal: '设计一个定制化的团建方案',
  nodes: [{ id: 'node-plan', title: '设计团建方案', inputSpecIds: kinds.map((item) => item.id) }],
  artifactSlots: [],
  inputSpecs: kinds.map((item) => ({
    id: item.id,
    label: item.label,
    description: item.kind === 'file' ? '支持拖拽到此处，或点击选择文件' : undefined,
    kind: item.kind,
    required: true,
    valueSchema: item.schema,
    defaultValue: item.value,
    pinEligible: false,
  })),
  createdBy: 'preview',
  createdAt: '2026-07-30T00:00:00Z',
  digest: 'preview',
};

const initialInputs: WorkInput[] = kinds.map((item, index) => ({
  id: `preview-input-${item.id}`,
  workId: 'preview-work',
  runId: 'preview-run',
  taskId: 'preview-task',
  blockId: 'preview-block',
  specId: item.id,
  value: null,
  state: 'requested',
  revision: 0,
  updatedAt: '2026-07-30T00:00:00Z',
  source: String(index),
}));

function Preview(): React.ReactElement {
  const [inputs, setInputs] = useState(initialInputs);
  useEffect(() => {
    useWorkUIStore.getState().setInformationPanel('preview-work', {
      activeInputId: undefined,
      closed: true,
    });
  }, []);
  const choose = (inputId: string) => {
    useWorkUIStore.getState().setInformationPanel('preview-work', {
      activeInputId: inputId,
      closed: false,
    });
  };
  const submit = async (request: SubmitWorkInputRequest) => {
    const current = inputs.find((input) => input.id === request.inputId)!;
    const updated = { ...current, value: request.value, extra: request.extra, state: 'submitted' as const, revision: current.revision + 1 };
    setInputs((all) => all.map((input) => input.id === updated.id ? updated : input));
    return { input: updated, revision: updated.revision + 1, duplicate: false, committed: true, recoverable: false };
  };
  const selectFile = async (): Promise<SelectWorkInputFileResult> => ({
    artifactRef: {
      id: 'preview-file',
      name: '团建活动需求.pdf',
      type: 'pdf',
      status: 'available',
      relativePath: '团建活动需求.pdf',
    },
    canceled: false,
  });
  const addCustom = async (request: AddCustomWorkInputRequest) => {
    const input: WorkInput = {
      id: request.inputId,
      workId: request.workId,
      runId: request.runId,
      taskId: 'work-information',
      blockId: 'work-information',
      specId: `custom:${request.inputId}`,
      customSpec: {
        id: `custom:${request.inputId}`,
        label: request.name,
        description: request.description,
        kind: request.kind,
        required: true,
        pinEligible: false,
      },
      value: request.value,
      state: 'submitted',
      revision: 1,
      updatedAt: new Date().toISOString(),
    };
    setInputs((current) => [...current, input]);
    return { input, revision: request.expectedRevision + 2, duplicate: false, committed: true, recoverable: false };
  };
  const infer = async () => ({
    items: [
      { inputId: 'preview-input-short', value: '夏日协作营', reason: '依据团建方案目标生成可编辑名称' },
      { inputId: 'preview-input-list', value: 'outdoor', reason: '默认采用适合团队协作的户外活动' },
      { inputId: 'preview-input-multi', value: ['game', 'meal'], reason: '采用协作游戏与聚餐的常见组合' },
    ],
    skipped: [
      { inputId: 'preview-input-date', reason: '具体日期需要用户确认' },
      { inputId: 'preview-input-number', reason: '预算需要用户决定' },
      { inputId: 'preview-input-file', reason: '需要用户提供真实文件' },
    ],
  });

  return (
    <main className="wg2-info-preview">
      <div className="wg2-info-preview__title">
        <span>工作流 / 团建方案</span>
        <strong>独立填写信息面板</strong>
      </div>
      <nav aria-label="预览输入类型">
        {kinds.map((item) => (
          <button key={item.id} type="button" onClick={() => choose(`preview-input-${item.id}`)}>
            {item.label.replace(/[？?].*$/, '')}
          </button>
        ))}
      </nav>
      <div className="wg2-info-preview__surface">
        <WorkInformationPanel
          workId="preview-work"
          runId="preview-run"
          workRevision={1}
          definition={definition}
          tasks={[{
            id: 'preview-task',
            runId: 'preview-run',
            nodeId: 'node-plan',
            title: '设计团建方案',
            state: 'waiting_input',
            waitingInputIds: inputs.filter((input) => input.state === 'requested').map((input) => input.id),
            retryable: false,
            updatedAt: '2026-07-30T00:00:00Z',
          }]}
          inputs={inputs}
          onSubmit={submit}
          onPin={async () => ({ pinned: false, revision: 1, duplicate: false, committed: true, recoverable: false })}
          onUnpin={async () => ({ pinned: false, revision: 1, duplicate: false, committed: true, recoverable: false })}
          onRefresh={async () => {}}
          onSelectFile={selectFile}
          onSelectCustomFile={selectFile}
          onAddCustom={addCustom}
          onInfer={infer}
        />
      </div>
    </main>
  );
}

createRoot(document.getElementById('root')!).render(<Preview />);
