import type { GateResolution, ResumeRunInput, RetryTaskInput, TaskExecuteInput } from './types.js';

export const workDTOFields = {
  TaskExecuteInput: [
    'workId',
    'runId',
    'stageId',
    'taskId',
    'attemptIndex',
    'requestId',
    'definitionDigest',
    'prompt',
  ],
  RetryTaskInput: ['workId', 'runId', 'stageId', 'taskId', 'requestId'],
  ResumeRunInput: ['workId', 'runId', 'requestId', 'gateResolutions'],
  GateResolution: ['stageId', 'outcome', 'input', 'note'],
} as const;

type SameKeys<Left, Right> =
  Exclude<Left, Right> extends never
    ? Exclude<Right, Left> extends never
      ? true
      : false
    : false;
type Assert<Condition extends true> = Condition;

type TaskExecuteInputKeysMatch = Assert<
  SameKeys<keyof TaskExecuteInput, (typeof workDTOFields.TaskExecuteInput)[number]>
>;
type RetryTaskInputKeysMatch = Assert<
  SameKeys<keyof RetryTaskInput, (typeof workDTOFields.RetryTaskInput)[number]>
>;
type ResumeRunInputKeysMatch = Assert<
  SameKeys<keyof ResumeRunInput, (typeof workDTOFields.ResumeRunInput)[number]>
>;
type GateResolutionKeysMatch = Assert<
  SameKeys<keyof GateResolution, (typeof workDTOFields.GateResolution)[number]>
>;

export const dtoFieldGuards: [
  TaskExecuteInputKeysMatch,
  RetryTaskInputKeysMatch,
  ResumeRunInputKeysMatch,
  GateResolutionKeysMatch,
] = [true, true, true, true];
