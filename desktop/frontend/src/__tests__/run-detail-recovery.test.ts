import assert from "node:assert/strict";
import { applyRunWireEvent, resetRunProjection } from "../lib/runEvents";
import { useRunStore } from "../store/run";
import { detailBodyText, resolveRunDetailSelection } from "../lib/runDetail";
import { detailBodyKey, loadDetailBody, readDetailBody, resetDetailBodies } from "../lib/runDetailData";

function reset() {
  useRunStore.getState().clearAllRuns();
  resetRunProjection();
  resetDetailBodies();
}

function begin(turn: string, call: string) {
  applyRunWireEvent("recovery-session", { kind: "turn_started" }, turn);
  applyRunWireEvent("recovery-session", {
    kind: "tool_dispatch", tool: { id: call, name: "bash", args: '{"command":"test"}', readOnly: true },
  }, turn);
  return Object.values(useRunStore.getState().runs).find(run => run.turnId === turn)!;
}

async function main() {
  reset();
  const first = begin("turn:1", "old-call");
  applyRunWireEvent("recovery-session", { kind: "turn_done" }, "turn:1");
  const second = begin("turn:2", "new-call");
  applyRunWireEvent("recovery-session", {
    kind: "tool_result", tool: { id: "old-call", name: "bash", output: "late output", readOnly: true },
  }, "turn:1");
  assert.equal(useRunStore.getState().runs[first.runId].events.find(e => e.callId === "old-call")?.output, "late output");
  assert.equal(useRunStore.getState().runs[second.runId].events.some(e => e.callId === "old-call"), false);

  reset();
  const failed = begin("turn:1", "failure");
  applyRunWireEvent("recovery-session", {
    kind: "tool_progress", tool: { id: "failure", name: "bash", output: "  before failure\n", readOnly: true },
  });
  applyRunWireEvent("recovery-session", {
    kind: "tool_result", tool: { id: "failure", name: "bash", err: "exit 1", readOnly: true },
  });
  const failedEvent = useRunStore.getState().runs[failed.runId].events.find(e => e.callId === "failure")!;
  assert.equal(detailBodyText(failedEvent), "  before failure\n");
  assert.equal(failedEvent.error, "exit 1");
  useRunStore.getState().setRunSelectedStep(failed.runId, 0);
  useRunStore.getState().setRunDetailOpen(failed.runId, true);
  assert.equal(resolveRunDetailSelection(useRunStore.getState().runs[failed.runId]).index, 0);

  reset();
  const nested = begin("turn:1", "shared");
  applyRunWireEvent("recovery-session", {
    kind: "tool_dispatch", tool: { id: "shared", parentId: "child-task", name: "bash", args: "child args", readOnly: true },
  });
  const calls = useRunStore.getState().runs[nested.runId].events.filter(e => e.callId === "shared");
  assert.equal(calls.length, 2, "child and parent calls with the same ID remain distinct");
  assert.equal(calls.find(e => e.parentId)?.args, "child args");

  reset();
  const empty = begin("turn:1", "empty");
  applyRunWireEvent("recovery-session", {
    kind: "tool_result", tool: { id: "empty", name: "bash", output: "", readOnly: true },
  });
  assert.equal(detailBodyText(useRunStore.getState().runs[empty.runId].events.find(e => e.callId === "empty")), "");

  const key = detailBodyKey(empty.runId, "empty");
  await loadDetailBody(key, async () => ({ args: "", output: "" }));
  assert.equal(readDetailBody(key).output, "", "known empty output remains distinct from unavailable");
  useRunStore.getState().clearRun(empty.runId);
  assert.equal(readDetailBody(key).phase, "idle", "removing a run invalidates its cached details");

  let settleOld!: (value: { output: string }) => void;
  let settleNew!: (value: { output: string }) => void;
  const oldLoad = loadDetailBody(key, () => new Promise(resolve => { settleOld = resolve; }));
  resetDetailBodies();
  const newLoad = loadDetailBody(key, () => new Promise(resolve => { settleNew = resolve; }));
  settleOld({ output: "stale" });
  await oldLoad;
  assert.notEqual(readDetailBody(key).output, "stale", "reset fences off old requests even when the key is reused");
  settleNew({ output: "current" });
  await newLoad;
  assert.equal(readDetailBody(key).output, "current");
  console.log("run detail recovery regressions passed");
}

main().catch(error => { console.error(error); process.exitCode = 1; });
