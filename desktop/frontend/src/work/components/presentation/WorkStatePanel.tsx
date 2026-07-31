import { useId, type ComponentType } from 'react';
import {
  ArrowUpRight,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  CircleHelp,
  LoaderCircle,
  PauseCircle,
} from 'lucide-react';

import type {
  PresentationTask,
  WorkPresentation,
  WorkPresentationPhase,
} from '../../presentation';
import type { InputSpec } from '../../types_v2';

interface PhaseMeta {
  eyebrow: string;
  title: string;
  emptyText: string;
  Icon: ComponentType<{
    'aria-hidden'?: boolean | 'true' | 'false';
    className?: string;
    size?: number | string;
    strokeWidth?: number | string;
  }>;
}

const PHASE_META: Record<WorkPresentationPhase, PhaseMeta> = {
  planning: {
    eyebrow: '准备中',
    title: '正在整理工作',
    emptyText: '正在确认执行结构与所需信息。',
    Icon: CircleDashed,
  },
  running: {
    eyebrow: '进行中',
    title: '工作正在推进',
    emptyText: '执行已开始，新的进展会显示在这里。',
    Icon: LoaderCircle,
  },
  paused: {
    eyebrow: '已暂停',
    title: '工作已暂停',
    emptyText: '继续后将从当前进度恢复。',
    Icon: PauseCircle,
  },
  waiting: {
    eyebrow: '等待处理',
    title: '需要你的回应',
    emptyText: '工作暂时停在需要确认的环节。',
    Icon: CircleHelp,
  },
  failed: {
    eyebrow: '需要处理',
    title: '工作遇到问题',
    emptyText: '查看问题后可以继续处理或重试。',
    Icon: CircleAlert,
  },
  completed: {
    eyebrow: '已完成',
    title: '工作已经完成',
    emptyText: '所有必要步骤均已处理完成。',
    Icon: CircleCheck,
  },
};

export interface WorkStatePanelProps {
  presentation: WorkPresentation;
  pendingInputSpecs?: readonly InputSpec[];
  onFocusTask: (taskId: string) => void;
}

interface TaskSummaryProps {
  task: PresentationTask;
  kind: 'attention' | 'primary';
  paused: boolean;
  onFocusTask: (taskId: string) => void;
}

function progressPercent(progress?: string): number | undefined {
  const match = progress?.match(/(\d+(?:\.\d+)?)\s*%/);
  if (!match) return undefined;
  const value = Number(match[1]);
  return Number.isFinite(value) ? Math.min(100, Math.max(0, value)) : undefined;
}

function TaskSummary({ task, kind, paused, onFocusTask }: TaskSummaryProps) {
  const label = kind === 'attention' ? '需要关注' : paused ? '暂停于' : '当前处理';
  const actionLabel = kind === 'attention' ? '处理' : '查看';
  const percent = progressPercent(task.progress);

  return (
    <article
      className={`work-state-panel__task work-state-panel__task--${kind}`}
      data-testid={`work-state-panel-${kind}-task`}
      data-task-id={task.id}
    >
      <div className="work-state-panel__task-copy">
        <span className="work-state-panel__task-label">{label}</span>
        <strong className="work-state-panel__task-title">{task.title}</strong>
        {task.progress ? (
          <span
            className="work-state-panel__task-progress"
            data-testid={`work-state-panel-${kind}-progress`}
          >
            {task.progress}
          </span>
        ) : null}
        {percent !== undefined ? (
          <span
            className="work-state-panel__progress-track"
            role="progressbar"
            aria-label={`${task.title}进度`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={percent}
          >
            <span style={{ width: `${percent}%` }} />
          </span>
        ) : null}
        {task.error ? (
          <span
            className="work-state-panel__task-error"
            data-testid={`work-state-panel-${kind}-error`}
            role="alert"
          >
            {task.error}
          </span>
        ) : null}
      </div>

      {task.source === 'runtime' ? (
        <button
          className="work-state-panel__focus"
          data-testid={`work-state-panel-${kind}-focus`}
          type="button"
          onClick={() => onFocusTask(task.id)}
          aria-label={`${actionLabel}任务：${task.title}`}
        >
          <span>{actionLabel}</span>
          <ArrowUpRight aria-hidden="true" size={16} strokeWidth={1.8} />
        </button>
      ) : null}
    </article>
  );
}

function CompletedSummary({ presentation }: { presentation: WorkPresentation }) {
  const completedTasks = presentation.tasks.filter((task) => task.state === 'completed').length;
  const readyArtifacts = presentation.artifacts.ready;

  return (
    <div
      className="work-state-panel__completion"
      data-testid="work-state-panel-completion"
    >
      <strong>全部必要步骤已完成</strong>
      <span>
        {completedTasks} 项任务
        {presentation.artifacts.total > 0
          ? ` · ${readyArtifacts}/${presentation.artifacts.total} 项成果就绪`
          : ''}
      </span>
    </div>
  );
}

export function WorkStatePanel({
  presentation,
  pendingInputSpecs = [],
  onFocusTask,
}: WorkStatePanelProps) {
  const titleId = useId();
  const meta = PHASE_META[presentation.phase];
  const { Icon } = meta;
  const attentionTask = presentation.attentionTask;
  const primaryTask =
    presentation.primaryTask?.id === attentionTask?.id
      ? undefined
      : presentation.primaryTask;
  const hasTask = Boolean(attentionTask || primaryTask);
  const showInputContext = presentation.phase === 'waiting'
    && attentionTask?.state === 'waiting_input'
    && pendingInputSpecs.length > 0
    && !primaryTask;

  return (
    <section
      className={`work-state-panel work-state-panel--${presentation.phase}`}
      data-testid="work-state-panel"
      data-phase={presentation.phase}
      data-layout={presentation.layoutMode}
      aria-labelledby={titleId}
    >
      <header className="work-state-panel__header">
        <span className="work-state-panel__phase-icon">
          <Icon aria-hidden="true" size={20} strokeWidth={1.8} />
        </span>
        <div className="work-state-panel__heading">
          <span className="work-state-panel__eyebrow">{meta.eyebrow}</span>
          <h3 id={titleId}>{meta.title}</h3>
        </div>
      </header>

      {presentation.phase === 'completed' ? (
        <CompletedSummary presentation={presentation} />
      ) : null}

      <div className="work-state-panel__tasks">
        {attentionTask ? (
          <TaskSummary
            task={attentionTask}
            kind="attention"
            paused={presentation.phase === 'paused'}
            onFocusTask={onFocusTask}
          />
        ) : null}
        {primaryTask ? (
          <TaskSummary
            task={primaryTask}
            kind="primary"
            paused={presentation.phase === 'paused'}
            onFocusTask={onFocusTask}
          />
        ) : null}
        {showInputContext ? (
          <article
            className="work-state-panel__task work-state-panel__task--context"
            data-testid="work-state-panel-input-context"
          >
            <div className="work-state-panel__task-copy">
              <span className="work-state-panel__task-label">等待信息</span>
              <strong className="work-state-panel__task-title">
                {pendingInputSpecs.length} 项信息待填写
              </strong>
              <span className="work-state-panel__task-progress">
                {pendingInputSpecs.slice(0, 2).map((spec) => spec.label).join(' · ')}
              </span>
            </div>
          </article>
        ) : null}
        {!hasTask && presentation.phase !== 'completed' ? (
          <p
            className="work-state-panel__empty"
            data-testid="work-state-panel-empty"
          >
            {meta.emptyText}
          </p>
        ) : null}
      </div>
    </section>
  );
}
