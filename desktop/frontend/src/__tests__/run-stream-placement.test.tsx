import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { runMatchesStream } from "../components/desktop-ui/IrisInfoComponents";
import type { RunRecord } from "../store/run";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed++; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed++; process.stdout.write(`  FAIL  ${label}\n`); }
}

function run(runId: string, turnId: string, status: RunRecord["status"], startedAt: number): RunRecord {
  return {
    runId,
    sessionId: "session-1",
    turnId,
    status,
    events: [{ eventId: `${runId}:event`, kind: "generic", content: runId, status: status === "failed" ? "failed" : "completed" }],
    expanded: false,
    startedAt,
    completedAt: startedAt + 1_000,
  };
}

const first = run("first", "turn:1", "completed", 1);
const second = run("second", "turn:2", "failed", 2);
const orphan = run("orphan", "legacy-id", "cancelled", 3);

ok(runMatchesStream(first, "session-1", undefined, "turn:1"), "turn stream selects the run assigned to that transcript turn");
ok(!runMatchesStream(second, "session-1", undefined, "turn:1"), "turn stream excludes runs from other turns");

ok(runMatchesStream(orphan, "session-1", undefined, undefined, true), "tail fallback keeps legacy or unassigned runs visible");
ok(!runMatchesStream(first, "session-1", undefined, undefined, true), "tail fallback excludes turn-assigned runs");

const testDir = dirname(fileURLToPath(import.meta.url));
const styles = readFileSync(resolve(testDir, "../styles.css"), "utf8");
const actionRule = styles.match(/\.session-run-action__panel\s*\{([^}]*)\}/)?.[1] ?? "";
ok(actionRule.includes("flex: 1 0 100%") && actionRule.includes("width: 100%"), "expanded terminal process fills the next line of the action row");
ok(styles.includes(".session-run-action__toggle[aria-expanded=\"true\"]"), "terminal process exposes an explicit expanded state");
const windowRule = styles.match(/\.run-work-window\s*\{([^}]*)\}/)?.[1] ?? "";
ok(windowRule.includes("height: 220px") && windowRule.includes("min-height: 220px"), "active and expanded process views share one fixed-size window");
ok(!styles.includes(".run-work-window__inner") && !styles.includes(".run-result-face"), "run presentation has no flip surface or result face");

process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
if (failed > 0) process.exit(1);
