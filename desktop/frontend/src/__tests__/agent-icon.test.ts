// Agent Icon 纯函数 + 资源契约测试。运行：
//   node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/agent-icon.test.ts
// 断言：hash 稳定、帽发互斥、seed 回退链、未知任务 → general、状态映射、
// fps/loop/holdLast 帧选择与 reduced-motion、manifest 计数/路径/sprite 宽、
// ImageGen 核心 PNG 尺寸/透明通道、docs ↔ src 同步（sha256）。
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { agentManifest, assetURL, runtimeAssetPaths } from "../lib/agentIcon/assets";
import { eyeFrameAt, animationKey } from "../lib/agentIcon/animation";
import { hashString, identitySeedKey, pickIdentity, stableIndex } from "../lib/agentIcon/identity";
import { eyeStatusFor } from "../lib/agentIcon/state";
import { resolveToolId, toolForTask, WIDGET_TASK_TOOL_ID } from "../lib/agentIcon/task";
import { buildAgentIconViewModel, isAgentIconItem } from "../lib/agentIcon/viewModel";
import type { DesktopIconItem } from "../lib/bridge";

const testDir = dirname(fileURLToPath(import.meta.url));
const srcAssetDir = resolve(testDir, "../assets/agent-icon");
const docsAssetDir = resolve(testDir, "../../../../docs/Agent Icon/assets");

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed++; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed++; }
}

function taskItem(overrides: Partial<DesktopIconItem> = {}): DesktopIconItem {
  return {
    id: "task:tab-1", kind: "task", sourceId: "tab-1", title: "实现图标", status: "running",
    unreadCount: 0, notifications: [], position: { row: "bottom", zone: "running", order: 0 },
    revision: "r", ...overrides,
  };
}

console.log("\nagent icon — identity");

// FNV-1a 32-bit 已知向量（与标准实现一致），确定性、无 Math.random。
assert.equal(hashString(""), 0x811c9dc5, "FNV-1a offset basis");
assert.equal(hashString("a"), 0xe40c292c, "FNV-1a('a') known vector");
assert.equal(hashString("foobar"), 0xbf9cf968, "FNV-1a('foobar') known vector");
assert.equal(hashString("foobar"), hashString("foobar"), "same seed → same hash");
ok(hashString("task:a") !== hashString("task:b"), "different seeds differ");
ok(stableIndex("seed-1", "frame", 9) === stableIndex("seed-1", "frame", 9), "stableIndex is deterministic");
ok(stableIndex("seed-1", "frame", 9) !== stableIndex("seed-1", "headwear", 9), "domain suffix separates dimensions");
assert.equal(stableIndex("x", "frame", 0), 0, "mod 0 degrades safely to 0");
for (const mod of [1, 9, 30]) {
  for (let i = 0; i < 100; i++) {
    const value = stableIndex(`seed-${i}`, "frame", mod);
    ok(value >= 0 && value < mod, `stableIndex bounds for mod ${mod}`);
  }
}

// seed 回退链：sessionId → sessionPath → topicId → item.id（文档 §5.2）。
assert.equal(identitySeedKey(taskItem({ sessionId: "s-1" })), "s-1", "sessionId wins");
assert.equal(identitySeedKey(taskItem({ sessionId: "  s-1  ", sessionRef: { scope: "global", sessionPath: "sp" } })), "s-1", "sessionId is trimmed");
assert.equal(
  identitySeedKey(taskItem({ sessionRef: { scope: "global", sessionPath: "sp-1", topicId: "t-1" } })),
  "sp-1",
  "sessionPath is the fallback",
);
assert.equal(
  identitySeedKey(taskItem({ sessionRef: { scope: "global", topicId: "t-1" } })),
  "t-1",
  "topicId is the second fallback",
);
assert.equal(identitySeedKey(taskItem({ sessionRef: { scope: "global" } })), "task:tab-1", "item.id is the last fallback");

// 帽发互斥：2000 个 seed 全部恰好命中一个 headwear（帽或发），绝不双显/缺失。
{
  const hats = new Set(agentManifest.hats.map((h) => h.id));
  const hair = new Set(agentManifest.hair.map((h) => h.id));
  const allIds = new Set([...hats, ...hair]);
  for (let i = 0; i < 2000; i++) {
    const { frameId, headwear, missingLayers } = pickIdentity(`seed-${i}`, agentManifest);
    ok(agentManifest.frames.some((f) => f.id === frameId), `frame ${frameId} is a manifest frame (seed ${i})`);
    const exclusive = headwear.kind === "hat" ? hats.has(headwear.id) : hair.has(headwear.id);
    ok(exclusive, `headwear resolves to exactly one hat/hair (seed ${i} → ${headwear.kind}:${headwear.id})`);
    ok(allIds.has(headwear.id), `headwear id ${headwear.id} exists (seed ${i})`);
    assert.deepEqual(missingLayers, [], `no missing layers for seed ${i}`);
  }
  // 帽发是同一槽位：帽子集合与头发集合不相交。
  for (const id of hats) ok(!hair.has(id), `hat id ${id} never collides with hair`);
  assert.equal(agentManifest.hats.length + agentManifest.hair.length, 30, "headwear slot spans hats + hair");
}
// 越界回退：空 manifest 数组不崩溃且显式上报。
{
  const empty = { ...agentManifest, frames: [], hats: [], hair: [] };
  const result = pickIdentity("seed", empty);
  assert.equal(result.frameId, "", "empty frames degrade to empty frameId");
  assert.equal(result.headwear.kind, "hat", "empty hats+hair degrade to hat fallback");
  ok(result.missingLayers.includes("frame") && result.missingLayers.includes("headwear"), "degradation is reported");
}

console.log("\nagent icon — task mapping");
assert.equal(WIDGET_TASK_TOOL_ID, "general", "widget task tool is the manifest general fallback");
{
  const resolved = resolveToolId("review", agentManifest);
  assert.equal(resolved.toolId, "review", "known tool id passes through");
  assert.deepEqual(resolved.missingLayers, [], "no report for a known tool");
}
{
  const resolved = resolveToolId("not-a-tool", agentManifest);
  assert.equal(resolved.toolId, "general", "unknown task → general");
  assert.deepEqual(resolved.missingLayers, ["tool:not-a-tool"], "unknown task is explicitly reported");
}
{
  const withoutGeneral = { ...agentManifest, tools: agentManifest.tools.filter((t) => t.id !== "general") };
  const resolved = resolveToolId("ghost", withoutGeneral);
  assert.equal(resolved.toolId, withoutGeneral.tools[0].id, "missing general falls back to the first tool");
  assert.deepEqual(resolved.missingLayers, ["tool:ghost", "tool:general"], "every fallback is reported");
}
{
  const { toolId, missingLayers } = toolForTask(taskItem(), agentManifest);
  assert.equal(toolId, "general", "widget task (no business enum) → general, never guessed from title");
  assert.deepEqual(missingLayers, [], "general exists in the current manifest");
}

console.log("\nagent icon — status mapping");
{
  const cases: Array<[DesktopIconItem["status"], string]> = [
    ["thinking", "running"], ["running", "running"],
    ["needs_input", "problem"], ["needs_confirm", "problem"],
    ["done", "success"], ["failed", "failure"],
  ];
  for (const [status, want] of cases) {
    assert.equal(eyeStatusFor(taskItem({ status })).eyeStatus, want, `${status} → ${want}`);
  }
  const unknown = eyeStatusFor(taskItem({ status: "idle" }));
  assert.equal(unknown.eyeStatus, "problem", "unknown/missing status → problem");
  assert.equal(unknown.reason, "未知状态：idle", "unknown status exposes the reason");
  const missing = eyeStatusFor(taskItem({ status: "unread" }));
  assert.equal(missing.eyeStatus, "problem", "unexpected status → problem, never silent");
}

console.log("\nagent icon — animation");
{
  // running: 6fps 循环
  assert.equal(eyeFrameAt("running", 1000, 1000, false, agentManifest), 0, "running t=0 → frame 0");
  assert.equal(eyeFrameAt("running", 1000, 1167, false, agentManifest), 1, "running 1/6s → frame 1");
  assert.equal(eyeFrameAt("running", 1000, 2000, false, agentManifest), 0, "running 1s → wraps to frame 0");
  // problem: 3fps 循环
  assert.equal(eyeFrameAt("problem", 1000, 1334, false, agentManifest), 1, "problem 1/3s → frame 1");
  // success: 8fps 单次停末帧
  assert.equal(eyeFrameAt("success", 1000, 1000, false, agentManifest), 0, "success t=0 → frame 0");
  assert.equal(eyeFrameAt("success", 1000, 1500, false, agentManifest), 4, "success 0.5s → frame 4");
  assert.equal(eyeFrameAt("success", 1000, 5000, false, agentManifest), 5, "success long → holds last frame");
  assert.equal(eyeFrameAt("failure", 1000, 5000, false, agentManifest), 5, "failure long → holds last frame");
  // reduced-motion：循环 → 中间帧（3），单次 → 末帧（5）
  assert.equal(eyeFrameAt("running", 0, 999999, true, agentManifest), 3, "reduced-motion running → middle frame");
  assert.equal(eyeFrameAt("problem", 0, 999999, true, agentManifest), 3, "reduced-motion problem → middle frame");
  assert.equal(eyeFrameAt("success", 0, 999999, true, agentManifest), 5, "reduced-motion success → last frame");
  assert.equal(eyeFrameAt("failure", 0, 999999, true, agentManifest), 5, "reduced-motion failure → last frame");
  assert.equal(animationKey("s", "running"), "s:running", "animation key is sessionId:status");
  assert.notEqual(animationKey("s", "running"), animationKey("s", "success"), "status change resets the key");
  assert.notEqual(animationKey("s", "running"), animationKey("t", "running"), "session change resets the key");
  // fps/loop/holdLast 全部来自 manifest（禁止硬编码副本）
  const eye = agentManifest.eyes.find((e) => e.id === "success");
  assert.equal(eye?.fps, 8, "success fps comes from manifest");
  assert.equal(eye?.holdLast, true, "success holdLast comes from manifest");
}

console.log("\nagent icon — viewModel");
{
  const vm = buildAgentIconViewModel(taskItem({
    sessionId: "session-1",
    workspaceIcon: "PYTHON",
    sessionRef: { scope: "project", workspaceRoot: "D:\\Work\\WG2", topicId: "topic-1", sessionPath: "sp-1" },
  }));
  assert.equal(vm.sessionId, "session-1", "viewModel keeps the stable seed");
  assert.equal(vm.workspaceBadge.iconKey, "python", "workspace icon normalizes through projectIconKey");
  assert.equal(vm.workspaceBadge.stableKey, "D:\\Work\\WG2", "badge stable key is workspaceRoot");
  assert.equal(vm.taskToolId, "general", "widget task tool is general");
  assert.equal(vm.eyeStatus, "running", "running status maps to running eye");
  assert.equal(vm.frameId.length > 0, true, "identity picked a frame");
  assert.deepEqual(vm.missingLayers, [], "current manifest resolves every layer");
  // 确定性：同一条目两次构建结果一致
  const again = buildAgentIconViewModel(taskItem({
    sessionId: "session-1", workspaceIcon: "PYTHON",
    sessionRef: { scope: "project", workspaceRoot: "D:\\Work\\WG2", topicId: "topic-1", sessionPath: "sp-1" },
  }));
  assert.deepEqual(again, vm, "same item → identical viewModel (repeat render/restart stable)");
  // 旧 retained 数据（无 sessionId）→ sessionPath 稳定回退
  const legacy = buildAgentIconViewModel(taskItem({
    sessionRef: { scope: "global", sessionPath: "sessions/legacy.jsonl", topicId: "topic-x" },
  }));
  assert.equal(legacy.sessionId, "sessions/legacy.jsonl", "legacy retained falls back to sessionPath");
  // 未知 workspace 图标 → 空键（渲染侧 folder 中性回退）
  const unknownIcon = buildAgentIconViewModel(taskItem({ workspaceIcon: "rocket" }));
  assert.equal(unknownIcon.workspaceBadge.iconKey, "", "unknown workspace icon degrades to neutral");
}
{
  ok(isAgentIconItem(taskItem()), "real task item uses the Agent Icon");
  ok(!isAgentIconItem(taskItem({ id: "opt:job-1" })), "QuickStart optimistic item keeps the legacy icon");
  ok(!isAgentIconItem({ ...taskItem(), kind: "room" }), "room never uses the Agent Icon");
  ok(!isAgentIconItem({ ...taskItem(), kind: "fixed", id: "fixed:new" }), "fixed actions never use the Agent Icon");
}

console.log("\nagent icon — assets contract");
{
  assert.equal(agentManifest.frames.length, 9, "frames 9");
  assert.equal(agentManifest.hats.length, 15, "hats 15");
  assert.equal(agentManifest.hair.length, 15, "hair 15");
  assert.equal(agentManifest.tools.length, 24, "tools 24");
  assert.equal(agentManifest.eyes.length, 5, "eyes 5");
  for (const eye of agentManifest.eyes) {
    assert.equal(eye.frames.length, 6, `eye ${eye.id} has 6 frames`);
    assert.equal(eye.frameWidth, 64, `eye ${eye.id} frame width 64`);
    assert.equal(eye.frameHeight, 64, `eye ${eye.id} frame height 64`);
  }
  assert.deepEqual(
    agentManifest.eyes.map((e) => e.fps), [6, 3, 8, 8, 2],
    "fps values come from the manifest (no hardcoded copies in code)",
  );
  assert.deepEqual(agentManifest.eyes.map((e) => e.loop), [true, true, false, false, true], "loop flags from manifest");
  assert.deepEqual(agentManifest.eyes.map((e) => e.holdLast), [false, false, true, true, false], "holdLast flags from manifest");
  assert.deepEqual(agentManifest.layerOrder, ["frame", "headwear", "eyes", "workspaceBadge", "taskTool"], "layer order is the stacking contract");
  assert.equal(agentManifest.identityRule.semantic, false, "identity has no business semantics");
}
// 运行时路径表必须覆盖 manifest 引用的全部运行时资源。
{
  for (const path of runtimeAssetPaths(agentManifest)) {
    ok(path.endsWith(".png"), `runtime asset is PNG-only: ${path}`);
    ok(Boolean(assetURL(path)), `assetURL resolves ${path}`);
  }
}
// docs ↔ src 同步：manifest 逐字节一致；磁盘上每个 manifest 引用的 png 都存在。
{
  const docsManifest = readFileSync(join(docsAssetDir, "manifest.json"));
  const srcManifest = readFileSync(join(srcAssetDir, "manifest.json"));
  assert.equal(
    createHash("sha256").update(srcManifest).digest("hex"),
    createHash("sha256").update(docsManifest).digest("hex"),
    "src manifest sha256 equals docs manifest (docs 变更必须同步)",
  );
  const walk = (dir: string, base: string, out: string[]) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      const rel = `${base}/${entry.name}`;
      if (entry.isDirectory()) walk(full, rel, out);
      else if (entry.name.endsWith(".png")) out.push(rel);
    }
    return out;
  };
  const diskFiles = walk(srcAssetDir, "src/assets/agent-icon", []);
  const manifestPngs: string[] = [];
  for (const frame of agentManifest.frames) manifestPngs.push(frame.png);
  for (const hat of agentManifest.hats) manifestPngs.push(hat.png);
  for (const hair of agentManifest.hair) manifestPngs.push(hair.png);
  for (const tool of agentManifest.tools) manifestPngs.push(tool.png);
  for (const eye of agentManifest.eyes) {
    manifestPngs.push(eye.sprite);
    for (const frame of eye.frames) manifestPngs.push(frame.png);
  }
  manifestPngs.push(agentManifest.templates.workspaceBadge.png, agentManifest.templates.neutralLedGrid.png);
  assert.equal(manifestPngs.length, 100, "manifest references 100 png files");
  for (const png of manifestPngs) {
    ok(diskFiles.includes(`src/assets/agent-icon/${png}`), `disk contains manifest png ${png}`);
  }
  assert.equal(diskFiles.length, 100, "disk has exactly the 100 manifest pngs (no orphans)");
  const corePngs = [
    ...agentManifest.frames.map((item) => item.png),
    ...agentManifest.hats.map((item) => item.png),
    ...agentManifest.hair.map((item) => item.png),
    ...agentManifest.tools.map((item) => item.png),
    ...agentManifest.eyes.flatMap((eye) => eye.frames.map((frame) => frame.png)),
  ];
  for (const path of corePngs) {
    const png = readFileSync(join(srcAssetDir, path));
    assert.equal(png.readUInt32BE(16), 64, `${path} width 64`);
    assert.equal(png.readUInt32BE(20), 64, `${path} height 64`);
    assert.equal(png[25], 6, `${path} uses RGBA color type`);
  }
  // sprite 宽度契约：384px（6 帧 × 64）
  for (const eye of agentManifest.eyes) {
    const png = readFileSync(join(srcAssetDir, eye.sprite));
    assert.equal(png.readUInt32BE(16), 384, `sprite ${eye.sprite} width 384`);
    assert.equal(png.readUInt32BE(20), 64, `sprite ${eye.sprite} height 64`);
    assert.equal(png[25], 6, `sprite ${eye.sprite} uses RGBA color type`);
  }
}
// Workspace 徽标必须保留既有 WorkspaceMatteIcon 的实际颜色，禁止再强制白化。
{
  const css = readFileSync(resolve(testDir, "../components/agent-icon/agent-icon.css"), "utf8");
  assert.match(css, /\.agent-icon__badge-glyph-img\s*\{[^}]*filter:\s*none/s, "workspace matte icon keeps its real colors");
}
// 契约：identity.ts 身份选择链路禁止 Math.random / Date.now（源码静态断言）。
{
  const source = readFileSync(resolve(testDir, "../lib/agentIcon/identity.ts"), "utf8");
  ok(!/Math\.random\(|Date\.now\(|crypto\.getRandomValues\(/.test(source), "identity.ts has no non-deterministic sources");
}

console.log(`\nagent-icon: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
