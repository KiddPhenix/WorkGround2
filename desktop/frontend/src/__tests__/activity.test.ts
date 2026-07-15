// Run: tsx src/__tests__/activity.test.ts
//
// Tests for lib/activity.ts:
//   - Every locale/stage pool has ≥8 entries, all weights > 0, ≥2 distinct weights
//   - Weighted picker respects boundaries and is deterministic
//   - Event/tool classification covers all relevant WireEvent kinds

import { activityLead, getPool, getStageLabels, pickWeighted, stageFromEvent, classifyTool, type Stage } from "../lib/activity";

let passed = 0;
let failed = 0;

function eq<T>(a: T, b: T, label: string) {
  if (a === b) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`);
    failed += 1;
  }
}

function ok(cond: boolean, label: string) {
  if (cond) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

const LOCALES = ["en", "zh", "zh-TW"];
const STAGES: Stage[] = [
  "waiting_model", "planning", "executing", "thinking", "replying",
  "searching", "reading", "editing", "command", "testing",
  "processing_result", "compacting", "waiting_approval",
  "waiting_answer", "retrying", "tooling",
];

// ── Pool integrity ──────────────────────────────────────────────────────────

console.log("\npool integrity");

for (const locale of LOCALES) {
  const labels = getStageLabels(locale);
  for (const stage of STAGES) {
    const pool = getPool(stage, locale);
    const tag = `${locale}/${stage}`;

    ok(pool.length >= 8, `${tag}: ≥8 entries (got ${pool.length})`);

    const allPositive = pool.every((e) => e.weight > 0);
    ok(allPositive, `${tag}: all weights > 0`);

    const uniqueWeights = new Set(pool.map((e) => e.weight));
    ok(uniqueWeights.size >= 2, `${tag}: ≥2 distinct weights (got ${uniqueWeights.size})`);

    ok(labels[stage].length > 0, `${tag}: label is non-empty`);
  }
}

const datedOrHostileEnglish = [
  "apt-get ritual",
  "blame session",
  "hide the body",
  "incriminating",
  "rm -rf",
  "merciful judgement",
  "wordsmithing the excuse",
  "yesterday's sins",
];
const englishCopy = STAGES.flatMap((stage) => getPool(stage, "en").map((entry) => entry.text.toLowerCase())).join("\n");
for (const phrase of datedOrHostileEnglish) {
  ok(!englishCopy.includes(phrase), `en copy avoids dated/hostile phrase: ${phrase}`);
}

// ── Deterministic weighted picker ──────────────────────────────────────────

console.log("\nweighted picker");

// Same seed → same result
const pool = getPool("editing", "en");
const pick1 = pickWeighted(pool, 42);
const pick2 = pickWeighted(pool, 42);
eq(pick1.text, pick2.text, "same seed → same pick");
eq(pick1.index, pick2.index, "same seed → same index");

// Different seed — call to verify it doesn't throw; result checked below
pickWeighted(pool, 9999);
ok(pool.length > 0, "pool not empty");

// Empty pool returns empty
const empty = pickWeighted([], 0);
eq(empty.text, "", "empty pool → empty text");
eq(empty.index, -1, "empty pool → -1 index");

// Single-entry pool with zero weight returns the entry
const single = pickWeighted([{ text: "only", weight: 0 }], 0);
eq(single.text, "only", "single zero-weight entry → returns it");

// Seed 0 and seed 1 give different results (highly probable)
const zero = pickWeighted(pool, 0);
const one = pickWeighted(pool, 1);
ok(zero.text !== one.text || pool.length <= 1, "different seeds diverge (probabilistic)");

// ── Event classification ────────────────────────────────────────────────────

console.log("\nevent → stage mapping");

// turn_started
eq(stageFromEvent("turn_started"), "waiting_model", "turn_started → waiting_model");

// reasoning / text
eq(stageFromEvent("reasoning"), "thinking", "reasoning → thinking");
eq(stageFromEvent("text"), "replying", "text → replying");
eq(stageFromEvent("message"), "replying", "message → replying");

// phase with plan-ish text
eq(stageFromEvent("phase", "plan the approach"), "planning", "phase('plan...') → planning");
eq(stageFromEvent("phase", "规划新功能"), "planning", "phase('规划...') → planning");
eq(stageFromEvent("phase", "model · planning", undefined, undefined, "planner"), "planning", "planner source → planning");
eq(stageFromEvent("phase", "model · executing", undefined, undefined, "executor"), "executing", "executor source → executing");
eq(stageFromEvent("phase", "executing step 2"), "executing", "phase('executing...') → executing");

// tool_dispatch → classification
eq(stageFromEvent("tool_dispatch", undefined, "grep"), "searching", "tool_dispatch grep → searching");
eq(stageFromEvent("tool_dispatch", undefined, "read_file"), "reading", "tool_dispatch read_file → reading");
eq(stageFromEvent("tool_dispatch", undefined, "edit_file"), "editing", "tool_dispatch edit_file → editing");
eq(stageFromEvent("tool_dispatch", undefined, "todo_write"), "reading", "tool_dispatch todo_write → reading");
eq(stageFromEvent("tool_dispatch", undefined, "web_fetch"), "reading", "tool_dispatch web_fetch → reading");
eq(stageFromEvent("tool_dispatch", undefined, "explore"), "searching", "tool_dispatch explore → searching");
eq(stageFromEvent("tool_dispatch", undefined, "research"), "searching", "tool_dispatch research → searching");
eq(stageFromEvent("tool_dispatch", undefined, "write_file"), "editing", "tool_dispatch write_file → editing");
eq(stageFromEvent("tool_dispatch", undefined, "multi_edit"), "editing", "tool_dispatch multi_edit → editing");

// Shell tool: test vs command
eq(stageFromEvent("tool_dispatch", undefined, "bash", "go test ./..."), "testing", "bash 'go test' → testing");
eq(stageFromEvent("tool_dispatch", undefined, "bash", '{"command":"go test ./..."}'), "testing", "bash JSON command → testing");
eq(stageFromEvent("tool_dispatch", undefined, "bash", '{"command":"Set-Location desktop; npm.cmd run test -- activity"}'), "testing", "PowerShell npm.cmd JSON command → testing");
eq(stageFromEvent("tool_dispatch", undefined, "bash", "npm test"), "testing", "bash 'npm test' → testing");
eq(stageFromEvent("tool_dispatch", undefined, "bash", "npx tsx src/foo.test.ts"), "testing", "bash 'npx tsx ...test' → testing");
eq(stageFromEvent("tool_dispatch", undefined, "bash", "pytest tests/"), "testing", "bash pytest → testing");
eq(stageFromEvent("tool_dispatch", undefined, "bash", "cargo test"), "testing", "bash cargo test → testing");

eq(stageFromEvent("tool_dispatch", undefined, "bash", "ls -la"), "command", "bash ls → command");
eq(stageFromEvent("tool_dispatch", undefined, "bash", "git commit -m 'fix'"), "command", "bash git commit → command");
eq(stageFromEvent("tool_dispatch", undefined, "bash", "./deploy.sh"), "command", "bash deploy → command");

// Unknown tool → tooling
eq(stageFromEvent("tool_dispatch", undefined, "someRandomTool"), "tooling", "unknown tool → tooling");

// tool_result
eq(stageFromEvent("tool_result"), "processing_result", "tool_result → processing_result");
eq(activityLead("从噪声中提取有效信号"), "从噪声中提取有效信号", "processing_result keeps only flavor copy");
eq(activityLead("让 Bug 主动交代"), "让 Bug 主动交代", "other stages omit the generic stage label");

// compaction
eq(stageFromEvent("compaction_started"), "compacting", "compaction_started → compacting");
eq(stageFromEvent("compaction_done"), "compacting", "compaction_done → compacting");

// approval / ask
eq(stageFromEvent("approval_request"), "waiting_approval", "approval_request → waiting_approval");
eq(stageFromEvent("ask_request"), "waiting_answer", "ask_request → waiting_answer");

// retrying
eq(stageFromEvent("retrying"), "retrying", "retrying → retrying");

// turn_done → null (clear)
eq(stageFromEvent("turn_done"), null, "turn_done → null (clear)");

// Notice / usage / steer → undefined (no change)
eq(stageFromEvent("notice"), undefined, "notice → undefined (no change)");
eq(stageFromEvent("usage"), undefined, "usage → undefined (no change)");
eq(stageFromEvent("steer"), undefined, "steer → undefined (no change)");

// Partial tool dispatch (no tool name) → undefined
eq(stageFromEvent("tool_dispatch", undefined, undefined, undefined), undefined, "tool_dispatch without tool → undefined");

// ── classifyTool ────────────────────────────────────────────────────────────

console.log("\nclassifyTool");

eq(classifyTool("grep", ""), "searching", "grep → searching");
eq(classifyTool("code_index", ""), "searching", "code_index → searching");
eq(classifyTool("research", ""), "searching", "research → searching");
eq(classifyTool("explore", ""), "searching", "explore → searching");

eq(classifyTool("read_file", ""), "reading", "read_file → reading");
eq(classifyTool("ls", ""), "reading", "ls → reading");
eq(classifyTool("glob", ""), "reading", "glob → reading");
eq(classifyTool("web_fetch", ""), "reading", "web_fetch → reading");
eq(classifyTool("todo_write", ""), "reading", "todo_write → reading");

eq(classifyTool("edit_file", ""), "editing", "edit_file → editing");
eq(classifyTool("write_file", ""), "editing", "write_file → editing");
eq(classifyTool("multi_edit", ""), "editing", "multi_edit → editing");
eq(classifyTool("notebook_edit", ""), "editing", "notebook_edit → editing");

eq(classifyTool("bash", "go test ./..."), "testing", "bash go test → testing");
eq(classifyTool("bash", "npm test -- --watch"), "testing", "bash npm test → testing");
eq(classifyTool("bash", "pytest -x"), "testing", "bash pytest → testing");
eq(classifyTool("bash", "make test"), "testing", "bash make test → testing");
eq(classifyTool("bash", "mvn verify"), "testing", "bash mvn verify → testing");
eq(classifyTool("bash", "dotnet test"), "testing", "bash dotnet test → testing");

eq(classifyTool("bash", "ls -la"), "command", "bash ls → command");
eq(classifyTool("bash", "git push"), "command", "bash git push → command");
eq(classifyTool("bash", "docker compose up"), "command", "bash docker compose → command");

eq(classifyTool("someRandomTool", ""), "tooling", "someRandomTool → tooling");
eq(classifyTool("", ""), "tooling", "empty name → tooling");

// ── Summary ─────────────────────────────────────────────────────────────────

if (failed) {
  process.stdout.write(`\n${failed} failed, ${passed} passed\n`);
  process.exit(1);
}
process.stdout.write(`\n${passed} passed\n`);
