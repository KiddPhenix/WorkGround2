import { applyRunWireEvent, projectRunHistory, resetRunProjection, stripRunAnsi } from "../lib/runEvents";
import { useRunStore } from "../store/run";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed++; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed++; process.stdout.write(`  FAIL  ${label}\n`); }
}

useRunStore.setState({ runs: {} });
resetRunProjection();

applyRunWireEvent("tab-1", { kind: "notice", text: "background" });
ok(Object.keys(useRunStore.getState().runs).length === 0, "unrelated background events do not create phantom runs");

applyRunWireEvent("tab-1", { kind: "turn_started" }, "turn:1");
applyRunWireEvent("tab-1", { kind: "tool_dispatch", tool: { id: "t1", name: "read_file", args: "a.go", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-1", { kind: "tool_result", tool: { id: "t1", name: "read_file", output: "ok", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });

let runs = Object.values(useRunStore.getState().runs);
ok(runs.length === 1, "one controller turn creates one run");
ok(runs[0]?.turnId === "turn:1", "live run keeps its transcript turn identity");
ok(runs[0]?.status === "running", "tool events keep the run active");
ok(runs[0]?.events.some((event) => event.stepLabel?.includes("read_file")) === true, "tool events become run steps");

applyRunWireEvent("tab-1", { kind: "ask_request" });
runs = Object.values(useRunStore.getState().runs);
ok(runs[0]?.status === "waiting_user", "ask moves the run to waiting_user");

applyRunWireEvent("tab-1", { kind: "turn_done" });
runs = Object.values(useRunStore.getState().runs);
ok(runs[0]?.status === "completed", "turn_done completes the run");
ok(runs[0]?.expanded === false, "completed run collapses automatically");

applyRunWireEvent("tab-1", { kind: "turn_started" });
applyRunWireEvent("tab-1", { kind: "turn_done", err: "boom" });
runs = Object.values(useRunStore.getState().runs);
ok(runs.length === 2, "a later turn creates a separate run tab");
ok(runs[1]?.status === "failed", "failed turn remains explicit");

projectRunHistory("tab-history", [
  { kind: "user" },
  { kind: "assistant", reasoning: "先检查文件" },
  { kind: "tool", id: "h1", name: "read_file", args: "a.go", status: "done", output: "ok" },
  { kind: "user" },
  { kind: "tool", id: "h2", name: "shell", args: "go test", status: "error", error: "failed" },
]);
const historyRuns = Object.values(useRunStore.getState().runs).filter((run) => run.sessionId === "tab-history");
ok(historyRuns.length === 2, "hydrated history rebuilds one collapsed run per execution turn");
ok(historyRuns.map((run) => run.turnId).join(",") === "turn:1,turn:2", "hydrated runs keep their transcript turn identities");
ok(historyRuns.every((run) => !run.expanded), "hydrated runs start as compact tabs");
ok(historyRuns[1]?.status === "failed", "hydrated tool errors remain visible");

// ── complete_step overrides turn_done error ────────────────────────────────

applyRunWireEvent("tab-cs-override", { kind: "turn_started" });
applyRunWireEvent("tab-cs-override", { kind: "tool_dispatch", tool: { id: "cs1", name: "read_file", args: "a.go", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-cs-override", { kind: "tool_result", tool: { id: "cs1", name: "read_file", output: "ok", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-cs-override", { kind: "tool_dispatch", tool: { id: "cs2", name: "complete_step", args: '{"step":"done","result":"ok","evidence":[{"kind":"manual","summary":"verified"}]}', readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-cs-override", { kind: "tool_result", tool: { id: "cs2", name: "complete_step", output: "Step done", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-cs-override", { kind: "turn_done", err: "some error" });
let csRuns = Object.values(useRunStore.getState().runs).filter((r) => r.sessionId === "tab-cs-override");
ok(csRuns.length === 1, "complete_step: creates one run");
ok(csRuns[0]?.status === "completed", "complete_step: overrides turn_done error to completed");
ok(!csRuns[0]?.errorMessage, "complete_step: errorMessage is cleared despite turn_done err");

// ── complete_step result stepLabel is not error-like ───────────────────────

applyRunWireEvent("tab-cs-label", { kind: "turn_started" });
applyRunWireEvent("tab-cs-label", { kind: "tool_dispatch", tool: { id: "csl1", name: "complete_step", args: '{"step":"A"}', readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-cs-label", { kind: "tool_result", tool: { id: "csl1", name: "complete_step", output: "signed off", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-cs-label", { kind: "turn_done" });
const csLabelRuns = Object.values(useRunStore.getState().runs).filter((r) => r.sessionId === "tab-cs-label");
ok(csLabelRuns.length === 1, "complete_step label: creates one run");
const csResultEvent = csLabelRuns[0]?.events.find((e) => e.stepLabel === "步骤确认完成");
ok(csResultEvent !== undefined, "complete_step label: stepLabel is '步骤确认完成' not 'complete_step 完成'");

applyRunWireEvent("tab-cs-newly", { kind: "turn_started" });
applyRunWireEvent("tab-cs-newly", { kind: "tool_result", tool: { id: "csn1", name: "complete_step", err: 'todo 2 "CSS test" is newly completed', readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-cs-newly", { kind: "turn_done", err: "complete_step rejected" });
const newlyRun = Object.values(useRunStore.getState().runs).find((run) => run.sessionId === "tab-cs-newly");
ok(newlyRun?.status === "completed", "complete_step newly-completed sentinel settles the run as completed");
ok(!newlyRun?.events.some((event) => event.content.includes("newly completed")), "completion sentinel is not rendered as an error log");

projectRunHistory("tab-history-complete", [
  { kind: "user" },
  { kind: "tool", id: "hc1", name: "complete_step", args: "{}", status: "error", error: 'todo 2 "CSS test" is newly completed' },
]);
const historyCompleteRun = Object.values(useRunStore.getState().runs).find((run) => run.sessionId === "tab-history-complete");
ok(historyCompleteRun?.status === "completed", "hydrated complete_step sentinel restores as completed");

// ── selectedStepIndex auto-advances with live events ───────────────────────

applyRunWireEvent("tab-auto", { kind: "turn_started" });
applyRunWireEvent("tab-auto", { kind: "tool_dispatch", tool: { id: "a1", name: "read_file", args: "a.go", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-auto", { kind: "tool_result", tool: { id: "a1", name: "read_file", output: "ok", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
let autoRuns = Object.values(useRunStore.getState().runs).filter((r) => r.sessionId === "tab-auto");
ok(autoRuns[0]?.selectedStepIndex === 1, "auto-advance: selectedStepIndex follows latest unique step");

// ── setRunSelectedStep freezes selection; new events keep it ───────────────

useRunStore.getState().setRunSelectedStep(autoRuns[0].runId, 0);
applyRunWireEvent("tab-auto", { kind: "tool_dispatch", tool: { id: "a2", name: "shell", args: "go test", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
applyRunWireEvent("tab-auto", { kind: "tool_result", tool: { id: "a2", name: "shell", output: "ok", readOnly: true, highSignalNodes: 0, toolResultNodes: 0, decisionNodes: 0, strategyCount: 0, learningCount: 0 } });
autoRuns = Object.values(useRunStore.getState().runs).filter((r) => r.sessionId === "tab-auto");
ok(autoRuns[0]?.selectedStepIndex === 0, "freeze: selectedStepIndex stays on the older step");
ok(autoRuns[0]?.events.length === 3, "freeze: one event accumulates per tool call");

// ── Clicking last tab while selected restores auto-follow ──────────────────

const lastIdx = autoRuns[0].events.length - 1;
useRunStore.getState().setRunSelectedStep(autoRuns[0].runId, lastIdx);
ok(useRunStore.getState().runs[autoRuns[0].runId]?.selectedStepIndex === lastIdx, "toggle-off: selectedStepIndex set to last");
useRunStore.getState().setRunSelectedStep(autoRuns[0].runId, undefined);
ok(useRunStore.getState().runs[autoRuns[0].runId]?.selectedStepIndex === undefined, "toggle-off: set to undefined restores auto-follow");

// ── tool progress updates one step instead of creating progress tabs ───────

applyRunWireEvent("tab-progress", { kind: "turn_started" });
applyRunWireEvent("tab-progress", { kind: "tool_dispatch", tool: { id: "p1", name: "bash", args: '{"command":"go test ./..."}', readOnly: true } });
applyRunWireEvent("tab-progress", { kind: "tool_progress", tool: { id: "p1", name: "bash", output: "first chunk\n", readOnly: true } });
applyRunWireEvent("tab-progress", { kind: "tool_progress", tool: { id: "p1", name: "bash", output: "second chunk\n", readOnly: true } });
let progressRun = Object.values(useRunStore.getState().runs).find((r) => r.sessionId === "tab-progress");
ok(progressRun?.events.length === 2, "tool progress keeps start + one tool step");
ok(progressRun?.events[1]?.content.includes("first chunk") === true, "tool progress keeps the first chunk");
ok(progressRun?.events[1]?.content.includes("second chunk") === true, "tool progress appends later chunks");
ok(progressRun?.events[1]?.stepLabel !== "进度", "tool progress keeps the tool label");
ok(progressRun?.events[1]?.status === "running", "tool progress keeps the step running");

applyRunWireEvent("tab-progress", { kind: "tool_result", tool: { id: "p1", name: "bash", output: "all passed", readOnly: true } });
progressRun = Object.values(useRunStore.getState().runs).find((r) => r.sessionId === "tab-progress");
ok(progressRun?.events.length === 2, "tool result updates the existing tool step");
ok(progressRun?.events[1]?.content === "all passed", "tool result replaces progress with the final output");
ok(progressRun?.events[1]?.status === "completed", "completed tool no longer shows a running spinner");

const ansiLog = "\u001b[31mFAIL\u001b[0m suite\n\u001b]0;title\u0007details";
ok(stripRunAnsi(ansiLog) === "FAIL suite\ndetails", "ANSI terminal controls are removed from run logs");
applyRunWireEvent("tab-progress", { kind: "tool_dispatch", tool: { id: "p2", name: "bash", args: "test", readOnly: true } });
applyRunWireEvent("tab-progress", { kind: "tool_progress", tool: { id: "p2", name: "bash", output: "\u001b[31mFAIL\u001b[0m", readOnly: true } });
progressRun = Object.values(useRunStore.getState().runs).find((r) => r.sessionId === "tab-progress");
ok(progressRun?.events.at(-1)?.content === "FAIL", "streamed run output stores clean text");

// ── starting a newer run collapses an older manually-expanded run ──────────

applyRunWireEvent("tab-collapse", { kind: "turn_started" });
applyRunWireEvent("tab-collapse", { kind: "turn_done", err: "boom" });
let collapseRuns = Object.values(useRunStore.getState().runs).filter((r) => r.sessionId === "tab-collapse");
useRunStore.getState().setRunExpanded(collapseRuns[0].runId, true);
applyRunWireEvent("tab-collapse", { kind: "turn_started" });
collapseRuns = Object.values(useRunStore.getState().runs).filter((r) => r.sessionId === "tab-collapse");
ok(collapseRuns.length === 2, "new turn creates a second run");
ok(collapseRuns[0]?.expanded === false, "new turn collapses the older run");
ok(collapseRuns[1]?.expanded === true, "new active run stays expanded");

process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
if (failed) process.exit(1);
