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
    events: [{ eventId: `${runId}:event`, content: runId, status: status === "failed" ? "failed" : "completed" }],
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
const terminalRule = styles.match(/\.session-run-stream__terminal\s*\{([^}]*)\}/)?.[1] ?? "";
ok(!terminalRule.includes("background:") && !terminalRule.includes("padding:"), "terminal run row has no outer rectangular fill or padding");

process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
if (failed > 0) process.exit(1);
