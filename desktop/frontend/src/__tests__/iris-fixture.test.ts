// iris-fixture.test.ts — validates the ?uiFixture=iris data and store seeding.
// Run: npx tsx src/__tests__/iris-fixture.test.ts
//
// Tests that:
//   1. The fixture data module exports all required data factories
//   2. Fixture data matches the chapter 16 spec values
//   3. Seeding the stores produces the correct records

import {
  irisFixtureMemory,
  irisFixtureSession,
  irisFixtureTranscript,
  irisFixtureCompletedRun,
  irisFixtureActiveRun,
  irisFixtureArtifacts,
  irisFixtureConfig,
  irisFixtureAddOns,
  irisFixtureQueueItems,
  FIXTURE_SESSION_ID,
} from "../lib/irisFixture";
import { useMemoryStore, formatMemoryLine, selectMemory } from "../store/memory";
import { useArtifactStore, selectArtifactsBySession } from "../store/artifacts";
import { useRunStore, selectRun, isTerminalStatus } from "../store/run";
import { useAddOnSurfaceStore, selectActiveAddOnCount } from "../store/addonSurface";
import { useComposerQueueStore, selectQueueHasItems } from "../store/composerQueue";

// ── Test framework ──────────────────────────────────────────────────────────

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
    process.stdout.write(`  FAIL  ${label}: expected truthy\n`);
    failed += 1;
  }
}

function done() {
  const total = passed + failed;
  process.stdout.write(`\n${total} tests · ${passed} passed · ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

// ── Reset stores ────────────────────────────────────────────────────────────

function resetStores() {
  useMemoryStore.setState({ memoryBySession: {} });
  useArtifactStore.setState({ artifacts: {} });
  useRunStore.setState({ runs: {} });
  useAddOnSurfaceStore.setState({
    instances: {},
    workbenchOpen: false,
    editingInstanceId: null,
    _frozenDisplayIndex: null,
  });
  useComposerQueueStore.setState({ items: [] });
}

// ── Tests ────────────────────────────────────────────────────────────────────

// 1. Fixture data structure

const session = irisFixtureSession();
eq(session.currentWorkspaceName, "WorkGround2", "session workspace name");
eq(session.secondaryWorkspaceName, "joyquant-db", "secondary workspace name");
eq(session.currentSessionTitle, "桌面信息架构重构", "session title");
eq(session.currentSessionLine1, "桌面信息架构重构", "session line 1");
eq(session.currentSessionLine2, "任务执行进度组件", "session line 2");
eq(session.currentSessionLine3, "会话缓存策略", "session line 3");

// 2. Memory line

const memory = irisFixtureMemory();
eq(memory.goal, "重构桌面信息架构", "memory goal");
eq(memory.current, "Artifact 模型已验证", "memory current");
eq(memory.nextStep, "实现持久化", "memory next step");
const formatted = formatMemoryLine(memory);
ok(formatted.includes("重构桌面信息架构"), "formatted memory includes goal");
ok(formatted.includes("Artifact 模型已验证"), "formatted memory includes current");
ok(formatted.includes("实现持久化"), "formatted memory includes next step");

// 3. Transcript items

const transcript = irisFixtureTranscript();
eq(transcript.length, 2, "transcript item count");
eq(transcript[0].role, "assistant", "first item role");
ok(transcript[0].text.includes("两级导航结构"), "first item text");
ok(transcript[1].text.includes("持久化方案"), "second item text");

// 4. Completed run

const completedRun = irisFixtureCompletedRun();
eq(completedRun.status, "completed", "completed run status");
ok(isTerminalStatus(completedRun.status), "completed run is terminal");
eq(completedRun.events.length, 4, "completed run events count");
ok(completedRun.completedAt !== undefined, "completed run has completedAt");
eq(completedRun.expanded, false, "completed run collapsed");

// 5. Active run

const activeRun = irisFixtureActiveRun();
eq(activeRun.status, "running", "active run status");
ok(!isTerminalStatus(activeRun.status), "active run not terminal");
eq(activeRun.events.length, 4, "active run events count");
ok(activeRun.expanded, "active run expanded");
ok(activeRun.completedAt === undefined, "active run no completedAt");

// 6. Artifacts

const artifacts = irisFixtureArtifacts();
eq(artifacts.length, 4, "artifact count");
const types = artifacts.map((a) => a.type);
ok(types.includes("binary"), "has binary artifact");
ok(types.includes("debug"), "has debug artifact");
ok(types.includes("script"), "has script artifact");
ok(types.includes("archive"), "has archive artifact");
const allAvailable = artifacts.every((a) => a.status === "available");
ok(allAvailable, "all artifacts available");

// 7. Config

const config = irisFixtureConfig();
eq(config.modelId, "DeepSeek-R1", "config model");
eq(config.contextPercent, 33, "config context percent");
eq(config.runtimeStatus, "运行中", "config runtime status");
eq(config.interactionMode, "用户选择", "config interaction mode");
eq(config.approvalMode, "低风险", "config approval mode");

// 8. AddOn instances

const addons = irisFixtureAddOns();
eq(addons.length, 3, "addon count");
const titles = addons.map((a) => a.title);
ok(titles.includes("团队登录"), "has login addon");
ok(titles.includes("构建监控"), "has build monitor addon");
ok(titles.includes("媒体预览"), "has media preview addon");
const densities = addons.map((a) => a.density);
ok(densities.includes("focus"), "has focus instance");
ok(densities.includes("peek"), "has peek instance");
eq(densities.filter((density) => density === "focus").length, 2, "has two focus instances");

// 9. Queue items

const queueItems = irisFixtureQueueItems();
eq(queueItems.length, 2, "queue item count");
ok(queueItems[0].content.length > 0, "first queue item has content");

// ── Store seeding tests ─────────────────────────────────────────────────────

resetStores();

// Seed memory
useMemoryStore.getState().setMemory(FIXTURE_SESSION_ID, irisFixtureMemory());
const storedMemory = selectMemory(useMemoryStore.getState().memoryBySession, FIXTURE_SESSION_ID);
ok(storedMemory !== undefined, "memory stored");
eq(storedMemory?.goal, "重构桌面信息架构", "stored memory goal");

// Seed artifacts
for (const art of irisFixtureArtifacts()) {
  useArtifactStore.getState().upsertArtifact(art);
}
const sessionArtifacts = selectArtifactsBySession(useArtifactStore.getState().artifacts, FIXTURE_SESSION_ID);
eq(sessionArtifacts.length, 4, "stored artifact count");

// Seed runs (completed)
const cr = irisFixtureCompletedRun();
for (const ev of cr.events) {
  useRunStore.getState().mergeRunEvent(cr.runId, cr.sessionId, cr.turnId, ev);
}
useRunStore.getState().setRunStatus(cr.runId, cr.status, {});
useRunStore.getState().setRunExpanded(cr.runId, cr.expanded);
const storedCompletedRun = selectRun(useRunStore.getState().runs, cr.runId);
ok(storedCompletedRun !== undefined, "completed run stored");
eq(storedCompletedRun?.status, "completed", "completed run status stored");

// Seed runs (active)
const ar = irisFixtureActiveRun();
for (const ev of ar.events) {
  useRunStore.getState().mergeRunEvent(ar.runId, ar.sessionId, ar.turnId, ev);
}
const storedActiveRun = selectRun(useRunStore.getState().runs, ar.runId);
ok(storedActiveRun !== undefined, "active run stored");
eq(storedActiveRun?.status, "running", "active run status stored");

// Seed AddOns
const surface = useAddOnSurfaceStore.getState();
for (const inst of irisFixtureAddOns()) {
  surface.upsertInstance(inst);
}
surface.setWorkbenchOpen(true);
const activeCount = selectActiveAddOnCount(useAddOnSurfaceStore.getState().instances);
eq(activeCount, 3, "active addon count");

// Seed queue
for (const item of irisFixtureQueueItems()) {
  useComposerQueueStore.getState().addItem(item);
}
ok(selectQueueHasItems(useComposerQueueStore.getState().items), "queue has items");

// ── Done ─────────────────────────────────────────────────────────────────────

done();
