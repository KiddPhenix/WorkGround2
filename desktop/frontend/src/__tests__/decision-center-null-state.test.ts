import assert from "node:assert/strict";
import { normalizeDecisionState } from "../components/DecisionCenter";
import type { DecisionStateView } from "../lib/types";

const state = normalizeDecisionState({
  available: true,
  revision: 1,
  queue: null,
  deferred: null,
  history: null,
  channels: null,
  settings: { externalMode: "smart", smartGraceSec: 30 },
} as unknown as DecisionStateView);

assert.deepEqual(state.queue, []);
assert.deepEqual(state.deferred, []);
assert.deepEqual(state.history, []);
assert.deepEqual(state.channels, []);

console.log("decision center null-state tests passed");
