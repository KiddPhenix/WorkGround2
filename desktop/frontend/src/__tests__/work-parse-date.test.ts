import assert from 'node:assert/strict';

import { parseWorkViewSnapshot } from '../work/index.js';

// Minimal valid V2 snapshot with a swappable createdAt field.
function v2Snapshot(createdAt: string): unknown {
  return {
    schemaVersion: 2,
    revision: 1,
    work: {
      schemaVersion: 2,
      id: 'w1',
      name: 'test',
      state: 'draft',
      archiveState: 'active',
      blueprintRef: { id: 'bp1', schemaVersion: 2, version: 1 },
      definitionSnapshot: {
        schemaVersion: 2,
        revision: 1,
        blueprintRef: { id: 'bp1', schemaVersion: 2, version: 1 },
        promptTemplate: '...',
        workflow: { stages: [] },
        blockSpecs: [],
        digest: 'abc',
      },
      blocks: [],
      placements: [],
      prompt: '...',
      cornerstones: [],
      runs: [],
      createdWith: {
        workSchemaVersion: 2,
        eventSchemaVersion: 2,
        rendererSetVersion: 2,
      },
      createdAt,
      updatedAt: createdAt,
    },
  };
}

// ── Calendar validity contract ──────────────────────────────────────────────

// Years 0001-0099 must survive Date.UTC 1900-offset bug.
{
  const result = parseWorkViewSnapshot(v2Snapshot('0001-01-01T00:00:00Z'));
  assert.equal(result.kind, 'supported');
  assert.ok(result.kind === 'supported' && result.view.work.createdAt === '0001-01-01T00:00:00Z');
}
{
  const result = parseWorkViewSnapshot(v2Snapshot('0099-12-31T23:59:59Z'));
  assert.equal(result.kind, 'supported');
}
{
  const result = parseWorkViewSnapshot(v2Snapshot('0020-02-29T00:00:00Z')); // 20 is leap
  assert.equal(result.kind, 'supported');
}
{
  const result = parseWorkViewSnapshot(v2Snapshot('0004-02-29T00:00:00Z')); // 4 is leap
  assert.equal(result.kind, 'supported');
}

// Year 0000 must be rejected.
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('0000-01-01T00:00:00Z')),
  /year 0000/,
);

// Non-leap-year Feb 29 must be rejected.
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-02-29T00:00:00Z')),
  /invalid calendar date/,
);
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2100-02-29T00:00:00Z')), // century not divisible by 400
  /invalid calendar date/,
);
// Leap-year Feb 29 must pass.
{
  const result = parseWorkViewSnapshot(v2Snapshot('2024-02-29T00:00:00Z'));
  assert.equal(result.kind, 'supported');
}
{
  const result = parseWorkViewSnapshot(v2Snapshot('2000-02-29T00:00:00Z')); // century divisible by 400
  assert.equal(result.kind, 'supported');
}

// Invalid dates: April 31, June 31, etc.
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-04-31T00:00:00Z')),
  /invalid calendar date/,
);
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-06-31T00:00:00Z')),
  /invalid calendar date/,
);
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-09-31T00:00:00Z')),
  /invalid calendar date/,
);

// Bad offset.
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-01-01T00:00:00+99:99')),
  /invalid offset/,
);
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-01-01T00:00:00+24:00')),
  /invalid offset/,
);

// Bad time components.
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-01-01T25:00:00Z')),
  /invalid time/,
);
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-01-01T00:60:00Z')),
  /invalid time/,
);
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-01-01T00:00:60Z')),
  /invalid time/,
);

// Non-RFC3339 strings must be rejected.
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('not-a-date')),
  /strict RFC3339/,
);
assert.throws(
  () => parseWorkViewSnapshot(v2Snapshot('2023-01-01')),
  /strict RFC3339/,
);
