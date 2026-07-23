import {
  WORK_VIEW_SCHEMA_VERSION,
  type ObjectKind,
  type ViewEventType,
  type WorkViewEvent,
} from './types';

const viewTypes = new Set<ViewEventType>(['snapshot', 'delta', 'attention', 'removed']);
const objectKinds = new Set<ObjectKind>([
  'work',
  'block',
  'run',
  'stage',
  'task',
  'attempt',
  'cornerstone',
  'conclusion',
  'artifact',
]);

export class ViewFutureSchemaError extends Error {
  constructor(
    readonly got: number,
    readonly current: number,
    readonly eventID: string,
  ) {
    super(
      `WorkViewEvent schema version ${got} exceeds current max ${current} on event "${eventID}"; read-only access is required`,
    );
    this.name = 'ViewFutureSchemaError';
  }
}

export interface ViewParseResult {
  event: WorkViewEvent | null;
  raw: string;
  futureError: ViewFutureSchemaError | null;
}

export function parseWorkViewEvent(raw: string): ViewParseResult {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch (error) {
    throw new SyntaxError(`work: decode WorkViewEvent: ${String(error)}`);
  }
  if (!isRecord(value)) {
    throw new TypeError('work: WorkViewEvent must be an object');
  }

  const schemaVersion = value.schemaVersion;
  if (!Number.isInteger(schemaVersion) || (schemaVersion as number) < 1) {
    throw new TypeError('work: WorkViewEvent schemaVersion must be a positive integer');
  }
  const eventID = typeof value.eventID === 'string' ? value.eventID : '';
  if ((schemaVersion as number) > WORK_VIEW_SCHEMA_VERSION) {
    return {
      event: null,
      raw,
      futureError: new ViewFutureSchemaError(
        schemaVersion as number,
        WORK_VIEW_SCHEMA_VERSION,
        eventID,
      ),
    };
  }

  validateWorkViewEvent(value);
  return { event: value, raw, futureError: null };
}

export function rejectWorkViewWrite(result: ViewParseResult): ViewFutureSchemaError | null {
  return result.futureError;
}

export function isViewType(result: ViewParseResult, type: ViewEventType): boolean {
  return result.event?.type === type;
}

export function deltaAppliesTo(result: ViewParseResult, currentRevision: number): boolean {
  return result.event?.type === 'delta' && result.event.baseRevision === currentRevision;
}

function validateWorkViewEvent(
  value: Record<string, unknown>,
): asserts value is Record<string, unknown> & WorkViewEvent {
  if (!viewTypes.has(value.type as ViewEventType)) {
    throw new TypeError(`work: invalid WorkViewEvent type ${String(value.type)}`);
  }
  for (const field of ['workID', 'eventID', 'requestID'] as const) {
    if (typeof value[field] !== 'string' || value[field].length === 0) {
      throw new TypeError(`work: WorkViewEvent requires ${field}`);
    }
  }
  if (!isRevision(value.revision) || !isRevision(value.baseRevision)) {
    throw new TypeError('work: WorkViewEvent revisions must be non-negative integers');
  }
  if (value.type === 'delta' && value.baseRevision >= value.revision) {
    throw new TypeError('work: delta baseRevision must be lower than revision');
  }
  if (!isRecord(value.object) || !objectKinds.has(value.object.kind as ObjectKind)) {
    throw new TypeError('work: WorkViewEvent requires a valid object kind');
  }
  if (typeof value.object.id !== 'string' || value.object.id.length === 0) {
    throw new TypeError('work: WorkViewEvent requires object id');
  }
  if (
    typeof value.createdAt !== 'string' ||
    value.createdAt.length === 0 ||
    Number.isNaN(Date.parse(value.createdAt))
  ) {
    throw new TypeError('work: WorkViewEvent requires createdAt');
  }
  if (!('payload' in value)) {
    throw new TypeError('work: WorkViewEvent requires payload');
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isRevision(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0;
}
