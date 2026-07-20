import type { RetryTaskInput, TaskExecuteInput } from './types.js';

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

export const dtoFieldGuards: [TaskExecuteInputKeysMatch, RetryTaskInputKeysMatch] = [true, true];
