import { Check, CircleDashed, GitBranch, SlidersHorizontal } from 'lucide-react';

import type {
  InputSpec,
  NodeDef,
  WorkDefinitionRevision,
  WorkInput,
} from '../../types_v2';
import type { PresentationTask } from '../../presentation';

export interface WorkDefinitionOverviewProps {
  definition: WorkDefinitionRevision;
  inputs: readonly WorkInput[];
  runId?: string;
  tasks: readonly PresentationTask[];
}

function optionLabels(spec: InputSpec): Map<string, string> {
  const schema = spec.valueSchema;
  if (!schema || typeof schema !== 'object' || Array.isArray(schema)) return new Map();
  const options = (schema as { options?: unknown }).options;
  if (!Array.isArray(options)) return new Map();
  return new Map(
    options.flatMap((option) => {
      if (!option || typeof option !== 'object' || Array.isArray(option)) return [];
      const value = (option as { value?: unknown }).value;
      const label = (option as { label?: unknown }).label;
      return typeof value === 'string' && typeof label === 'string'
        ? [[value, label] as const]
        : [];
    }),
  );
}

function formatValue(spec: InputSpec, value: unknown): string {
  const labels = optionLabels(spec);
  const display = (item: unknown) => {
    if (typeof item === 'string') return labels.get(item) ?? item;
    if (typeof item === 'number' || typeof item === 'boolean') return String(item);
    if (item && typeof item === 'object') {
      const named = (item as { name?: unknown }).name;
      if (typeof named === 'string') return named;
      try {
        return JSON.stringify(item);
      } catch {
        return '已填写';
      }
    }
    return '';
  };

  if (Array.isArray(value)) return value.map(display).filter(Boolean).join('、') || '已填写';
  const result = display(value);
  if (!result) return '已填写';
  if (spec.kind === 'date') {
    const date = new Date(result);
    if (!Number.isNaN(date.getTime())) {
      return new Intl.DateTimeFormat('zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
      }).format(date);
    }
  }
  return result;
}

function currentInputs(inputs: readonly WorkInput[], runId?: string): Map<string, WorkInput> {
  const result = new Map<string, WorkInput>();
  if (!runId) return result;
  for (const input of inputs) {
    if (input.runId !== runId) continue;
    const previous = result.get(input.specId);
    if (!previous || input.revision > previous.revision
      || (input.revision === previous.revision && input.updatedAt > previous.updatedAt)) {
      result.set(input.specId, input);
    }
  }
  return result;
}

function nodeState(node: NodeDef, tasks: readonly PresentationTask[]): PresentationTask['state'] {
  return tasks.find((task) => task.nodeId === node.id)?.state ?? 'pending';
}

function nodeStateLabel(state: PresentationTask['state']): string {
  switch (state) {
    case 'completed': return '已完成';
    case 'running': return '执行中';
    case 'ready': return '准备执行';
    case 'waiting_input': return '等待信息';
    case 'waiting_approval': return '等待确认';
    case 'failed_retryable': return '可重试';
    case 'failed_terminal': return '已停止';
    case 'invalidated': return '待更新';
    case 'canceled': return '已取消';
    default: return '待执行';
  }
}

export function WorkDefinitionOverview({
  definition,
  inputs,
  runId,
  tasks,
}: WorkDefinitionOverviewProps) {
  const bySpec = currentInputs(inputs, runId);
  const filled = definition.inputSpecs.filter((spec) => {
    const input = bySpec.get(spec.id);
    return input?.state === 'submitted' || input?.state === 'accepted';
  }).length;

  return (
    <div
      className="work-definition-overview"
      data-testid="work-definition-overview"
      data-has-inputs={definition.inputSpecs.length > 0 ? 'true' : 'false'}
    >
      {definition.inputSpecs.length > 0 ? (
        <section className="work-definition-overview__panel work-definition-overview__panel--inputs">
          <header className="work-definition-overview__header">
            <span className="work-definition-overview__title">
              <SlidersHorizontal aria-hidden="true" size={17} strokeWidth={1.8} />
              工作信息
            </span>
            <span className="work-definition-overview__count">
              {filled}/{definition.inputSpecs.length} 已填写
            </span>
          </header>
          <div className="work-definition-overview__fields">
            {definition.inputSpecs.map((spec) => {
              const input = bySpec.get(spec.id);
              const complete = input?.state === 'submitted' || input?.state === 'accepted';
              return (
                <div
                  className="work-definition-overview__field"
                  data-state={complete ? 'complete' : input?.state ?? 'missing'}
                  key={spec.id}
                >
                  <span>{spec.label}</span>
                  <strong>
                    {complete
                      ? formatValue(spec, input.value)
                      : input?.state === 'draft' ? '填写中' : '待填写'}
                  </strong>
                  {complete
                    ? <Check aria-hidden="true" size={15} strokeWidth={2} />
                    : <CircleDashed aria-hidden="true" size={15} strokeWidth={1.8} />}
                </div>
              );
            })}
          </div>
        </section>
      ) : null}

      <section className="work-definition-overview__panel work-definition-overview__panel--flow">
        <header className="work-definition-overview__header">
          <span className="work-definition-overview__title">
            <GitBranch aria-hidden="true" size={17} strokeWidth={1.8} />
            工作结构
          </span>
          <span className="work-definition-overview__count">{definition.nodes.length} 个步骤</span>
        </header>
        <ol className="work-definition-overview__nodes">
          {definition.nodes.map((node, index) => {
            const state = nodeState(node, tasks);
            return (
              <li key={node.id} data-state={state}>
                <span className="work-definition-overview__node-index">{index + 1}</span>
                <span className="work-definition-overview__node-copy">
                  <strong>{node.title}</strong>
                  {node.description ? <span>{node.description}</span> : null}
                </span>
                <span className="work-definition-overview__node-state">
                  {nodeStateLabel(state)}
                </span>
              </li>
            );
          })}
        </ol>
      </section>
    </div>
  );
}
