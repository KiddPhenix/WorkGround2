import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  ViewFutureSchemaError,
  deltaAppliesTo,
  isViewType,
  parseWorkViewEvent,
  rejectWorkViewWrite,
} from '../work/index.js';
import { dtoFieldGuards, workDTOFields } from '../work/dto-contract.js';

const fixtureDir = join(dirname(fileURLToPath(import.meta.url)), '../work/__fixtures__');
const goFixtureDir = join(dirname(fileURLToPath(import.meta.url)), '../../../../internal/work/testdata/archive-v1');
const fixture = (name: string): string => readFileSync(join(fixtureDir, name), 'utf8');

const frontendDTOText = fixture('work-dto-fields-v1.json');
const goDTOText = readFileSync(join(goFixtureDir, 'work-dto-fields-v1.json'), 'utf8');
assert.equal(frontendDTOText, goDTOText);
const dtoFields = JSON.parse(frontendDTOText) as Record<string, string[]>;
assert.deepEqual(dtoFields, JSON.parse(goDTOText));
assert.deepEqual(dtoFieldGuards, [true, true, true, true, true, true, true, true, true, true]);
assert.deepEqual(workDTOFields, dtoFields);

const current = parseWorkViewEvent(fixture('work-view-event-v1.json'));
assert.equal(current.futureError, null);
assert.equal(current.event?.workID, 'work-abc123');
assert.equal(current.event?.eventID, 'evt-snapshot-001');
assert.equal(current.event?.requestID, 'req-20260720-001');
assert.equal(current.event?.object.kind, 'work');
assert.equal(isViewType(current, 'snapshot'), true);
assert.equal(rejectWorkViewWrite(current), null);

const futureRaw = fixture('work-view-event-future.json');
const future = parseWorkViewEvent(futureRaw);
assert.equal(future.event, null);
assert.equal(future.raw, futureRaw);
const futureError = future.futureError;
assert.ok(futureError instanceof ViewFutureSchemaError);
assert.equal(futureError.eventID, 'evt-future-001');
assert.equal(rejectWorkViewWrite(future), futureError);

const delta = parseWorkViewEvent(
  JSON.stringify({
    schemaVersion: 1,
    type: 'delta',
    workID: 'work-1',
    eventID: 'event-2',
    revision: 2,
    baseRevision: 1,
    requestID: 'request-1',
    object: { kind: 'block', id: 'block-1', parentID: 'work-1' },
    payload: { status: 'ready' },
    createdAt: '2026-07-20T10:00:00Z',
  }),
);
assert.equal(deltaAppliesTo(delta, 1), true);
assert.equal(deltaAppliesTo(delta, 0), false);

const nodeSkill = parseWorkViewEvent(JSON.stringify({
  ...delta.event,
  eventID: 'event-node-skill',
  requestID: 'request-node-skill',
  object: {
    kind: 'node',
    id: 'node-1',
    workID: 'work-1',
    nodeID: 'node-1',
    definitionID: 'definition-1',
  },
  payload: { workId: 'work-1', nodeId: 'node-1', skillName: 'editor' },
}));
assert.equal(nodeSkill.event?.object.kind, 'node');
assert.equal(nodeSkill.event?.object.nodeID, 'node-1');

for (const invalid of [
  '{}',
  '{"schemaVersion":0}',
  JSON.stringify({ ...delta.event, requestID: '' }),
  JSON.stringify({ ...delta.event, type: 'future' }),
  JSON.stringify({ ...delta.event, object: null }),
  JSON.stringify({ ...delta.event, baseRevision: 2 }),
]) {
  assert.throws(() => parseWorkViewEvent(invalid));
}

console.log('Work contract test: passed');
