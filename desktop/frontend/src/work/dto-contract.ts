import type {
  GateResolution,
  ResumeRunInput,
  RetryTaskInput,
  RunBlockItem,
  RunBlockReason,
  TaskExecuteInput,
  ViewRecoveryIntent,
  ViewResync,
  WorkView,
  WorkViewEvent,
} from './types.js';

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
  WorkView: [
    'schemaVersion',
    'work',
    'revision',
    'assessment',
    'runBlock',
    'definition',
    'artifactSlots',
    'tasks',
    'inputs',
    'patchPreviews',
  ],
  RunBlockReason: ['blocked', 'items'],
  RunBlockItem: ['code', 'cornerstoneId', 'status', 'detail'],
  WorkViewEvent: [
    'schemaVersion',
    'type',
    'workID',
    'eventID',
    'revision',
    'baseRevision',
    'requestID',
    'object',
    'resync',
    'payload',
    'createdAt',
  ],
  ViewResync: ['reason', 'authoritative', 'generation'],
  ViewRecoveryIntent: ['reason', 'generation'],
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
type WorkViewKeysMatch = Assert<
  SameKeys<keyof WorkView, (typeof workDTOFields.WorkView)[number]>
>;
type RunBlockReasonKeysMatch = Assert<
  SameKeys<keyof RunBlockReason, (typeof workDTOFields.RunBlockReason)[number]>
>;
type RunBlockItemKeysMatch = Assert<
  SameKeys<keyof RunBlockItem, (typeof workDTOFields.RunBlockItem)[number]>
>;
type WorkViewEventKeysMatch = Assert<
  SameKeys<keyof WorkViewEvent, (typeof workDTOFields.WorkViewEvent)[number]>
>;
type ViewResyncKeysMatch = Assert<
  SameKeys<keyof ViewResync, (typeof workDTOFields.ViewResync)[number]>
>;
type ViewRecoveryIntentKeysMatch = Assert<
  SameKeys<keyof ViewRecoveryIntent, (typeof workDTOFields.ViewRecoveryIntent)[number]>
>;

export const dtoFieldGuards: [
  TaskExecuteInputKeysMatch,
  RetryTaskInputKeysMatch,
  ResumeRunInputKeysMatch,
  GateResolutionKeysMatch,
  WorkViewKeysMatch,
  RunBlockReasonKeysMatch,
  RunBlockItemKeysMatch,
  WorkViewEventKeysMatch,
  ViewResyncKeysMatch,
  ViewRecoveryIntentKeysMatch,
] = [true, true, true, true, true, true, true, true, true, true];
