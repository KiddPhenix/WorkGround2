import React from 'react';

import type { DefinitionStructuralClarification } from '../../work/types_v2';
import { StructureClarificationCard } from './StructureClarificationCard';

const previewClarification: DefinitionStructuralClarification = {
  id: 'report_topology',
  impact: 'task_dependencies',
  question: 'A、B 两组报告应独立并行处理，还是 B 组必须使用 A 组的结果？',
  description: '任务说明没有给出两组报告之间的数据依赖，两种结构都会实质改变执行拓扑。',
  flow: ['处理 A 组报告', '处理 B 组报告', '生成汇总'],
  options: [
    { id: 'parallel', label: '两组独立并行', description: 'A、B 两组互不依赖，完成后再汇总' },
    { id: 'a_then_b', label: 'A 完成后处理 B', description: 'B 组节点依赖 A 组结果' },
    { id: 'custom', label: '自定义依赖关系', description: '说明两个处理节点之间的关系', custom: true },
  ],
  customPlaceholder: '例如：先处理 A，B 只使用 A 的结论',
};

export const StructureClarificationPreview: React.FC = () => (
  <main className="wg2-structure-preview">
    <header className="wg2-structure-preview__topbar">
      <strong>整理预算报告…</strong>
      <button type="button">继续规划工作结构</button>
      <span>请先在背面完成工作结构规划，确认后可开始执行。</span>
    </header>
    <section className="wg2-structure-preview__workspace">
      <label>工作名称<input value="整理两组预算报告并汇总" readOnly /></label>
      <div className="wg2-work-draft-heading">
        <h3>任务说明</h3>
        <p>用自然语言说明目标、背景和期望结果。</p>
      </div>
      <div className="wg2-work-prompt-field" data-busy="true">
        <textarea value="处理 A、B 两组预算报告并汇总；它们之间的关系由我决定" readOnly />
        <div className="wg2-definition-planning">
          <pre className="wg2-definition-planning__raw" aria-hidden="true">
            {'{"goal":"处理两组报告并汇总","nodes":[{"id":"process_a","title":"处理 A 组报告"},{"id":"process_b","title":"处理 B 组报告"},{"id":"summary","title":"生成汇总"}],"structuralQuestions":[{"id":"report_topology","impact":"task_dependencies"}]}'}
          </pre>
          <ol className="wg2-definition-planning__steps">
            <li data-kind="node"><span className="wg2-definition-planning__dot" />已建立 · 处理 A 组报告</li>
            <li data-kind="node"><span className="wg2-definition-planning__dot" />已建立 · 处理 B 组报告</li>
            <li data-kind="node"><span className="wg2-definition-planning__dot" />已建立 · 生成汇总</li>
            <li data-kind="clarification"><span className="wg2-definition-planning__dot" />发现无法推导的节点依赖关系</li>
          </ol>
        </div>
      </div>
    </section>
    <StructureClarificationCard
      clarification={previewClarification}
      busy={false}
      onClose={() => undefined}
      onSubmit={() => undefined}
    />
  </main>
);
