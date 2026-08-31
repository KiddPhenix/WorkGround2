// Run: tsx src/__tests__/assistant-dispatch.test.ts

import { dispatchKindLabel, dispatchStateLabel, ideaStateLabel, jobStateLabel, timelineEntries } from "../custom/features/assistant/assistant.model";
import type { AssistantCopy } from "../custom/features/assistant/assistant.copy";
import type { AssistantSnapshot } from "../custom/features/assistant/assistant.types";

let passed = 0;
let failed = 0;

function eq(a: unknown, b: unknown, label: string) {
  if (JSON.stringify(a) === JSON.stringify(b)) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`);
    failed += 1;
  }
}

console.log("\nassistant dispatch labels");
eq(dispatchKindLabel("task", "zh"), "任务", "task kind zh");
eq(dispatchKindLabel("feedback", "en"), "Feedback", "feedback kind en");
eq(dispatchKindLabel(undefined, "zh"), "未分类", "unclassified zh");
eq(jobStateLabel("succeeded", "zh"), "已完成", "job succeeded zh");
eq(jobStateLabel("failed", "en"), "Failed", "job failed en");
eq(ideaStateLabel("pending", "zh"), "待确认", "idea pending zh");
eq(dispatchStateLabel("classification_failed", "zh"), "分类失败", "dispatch classification_failed zh");
eq(dispatchStateLabel("executed", "zh"), "已执行", "dispatch executed zh");
eq(dispatchStateLabel("future_state", "en"), "Unknown state: future_state", "unknown dispatch state stays renderable");

const day = new Date("2026-08-17T08:00:00.000Z");
const copy = { learned: "记", next: "下次" } as AssistantCopy;
const snapshot: AssistantSnapshot = {
  revision: 1,
  assistant: { id: "a1", name: "A", mission: "m", scope: "global", lifecycle: "active", policy: {} as never, memory_revision: 1, revision: 1, created_at: day.toISOString(), updated_at: day.toISOString() },
  routines: [],
  memory: { revision: 1, items: [] },
  runs: [],
  attention: [],
  plan: { revision: 1, responsibilities: [] },
  artifacts: [],
  opportunities: [],
  channels: [],
  channel_actions: [],
  channel_metrics: [],
  dispatches: [{
    id: "d1", assistant_id: "a1", request_id: "r1", input: "请扫描", kind: "task", reply: "收到",
    state: "classified", revision: 1, created_at: day.toISOString(), updated_at: day.toISOString(), classified_at: day.toISOString(),
  }],
  jobs: [{
    id: "j1", assistant_id: "a1", dispatch_id: "d1", name: "execute", kind: "task", prompt: "请扫描",
    state: "queued", attempt: 0, max_attempts: 3, policy: {} as never, revision: 1, created_at: day.toISOString(), updated_at: day.toISOString(),
  }],
  ideas: [{
    id: "i1", assistant_id: "a1", request_id: "ir1", trigger: "manual", summary: "换方向", state: "pending",
    revision: 1, created_at: day.toISOString(), updated_at: day.toISOString(),
  }],
  updated_at: day.toISOString(),
};

console.log("\nassistant timeline entries with dispatch/idea");
const entries = timelineEntries(snapshot, day, "zh", copy);
const dispatchEntry = entries.find((entry) => entry.kind === "dispatch");
const ideaEntry = entries.find((entry) => entry.kind === "idea");
eq(dispatchEntry?.kind ?? null, "dispatch", "dispatch entry present");
eq(dispatchEntry?.jobs?.length ?? 0, 1, "dispatch entry carries one job");
eq(dispatchEntry?.title ?? null, "任务", "dispatch entry title is kind label");
eq(ideaEntry?.kind ?? null, "idea", "pending idea entry present");

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
