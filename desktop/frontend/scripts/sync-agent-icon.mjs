// sync-agent-icon.mjs — syncs the Agent Icon production assets from the
// design repo (docs/Agent Icon/assets) into the Vite bundle
// (src/assets/agent-icon/). The source manifest is the single source of truth:
// this script validates its counts/fps/paths against the actual files, copies
// only the runtime subset (png layers, eye sprites, templates, manifest.json),
// verifies byte equality, and atomically replaces the target directory so a
// failure never leaves a half-written asset set. Re-running is safe (the temp
// directory is cleaned first and the target is replaced wholesale).
//
// Contract: docs 资源变更而未同步到 src/assets/agent-icon 必须让构建失败。
// The frontend contract test (src/__tests__/agent-icon.test.ts) enforces the
// same invariant from the test side.
import { createHash } from "node:crypto";
import { cp, mkdir, readFile, readdir, rm, rename } from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
// scripts/ → frontend/ → desktop/ → repo root
const repoRoot = resolve(scriptDir, "../../..");
const sourceDir = join(repoRoot, "docs", "Agent Icon", "assets");
const targetDir = join(repoRoot, "desktop", "frontend", "src", "assets", "agent-icon");
// Fixed-name temp dir in the target's parent (src/assets/). Only this exact
// name is ever cleaned; the script never does recursive cleanup of src/assets.
const tempDir = join(targetDir, "..", ".agent-icon-sync-tmp");

// The runtime subset mirrored from docs/Agent Icon/assets. png/eyes (per-frame
// PNGs) is included so every manifest PNG path exists in the bundle — the
// frontend renders eyes from the sprite, but a uniform manifest↔disk invariant
// makes the contract test and the sync check the same single rule.
const DIRS = ["png/frames", "png/hats", "png/hair", "png/tools", "png/templates", "png/eyes", "sprites/eyes"];
const FILES = ["manifest.json"];

// Contract counts — keep in sync with manifest assertions in the frontend
// contract test and with generate-assets.mjs itself.
const EXPECT = {
  frames: 9,
  hats: 15,
  hair: 15,
  tools: 24,
  eyes: 5,
  framesPerEye: 6,
  fps: [6, 3, 8, 8, 2],
};

function sha256(buffer) {
  return createHash("sha256").update(buffer).digest("hex");
}

function pngWidth(buffer) {
  // PNG header: 8-byte signature, then IHDR chunk: length(4) type(4) width(4)…
  return buffer.readUInt32BE(16);
}

function fail(message) {
  throw new Error(`[sync-agent-icon] ${message}`);
}

function collectEntries(manifest) {
  const entries = [];
  for (const item of [...manifest.frames, ...manifest.hats, ...manifest.hair, ...manifest.tools]) {
    entries.push(item.png);
  }
  for (const eye of manifest.eyes) {
    entries.push(eye.sprite);
    for (const frame of eye.frames) entries.push(frame.png);
  }
  entries.push(manifest.templates.workspaceBadge.png, manifest.templates.neutralLedGrid.png);
  return entries;
}

async function validateSource(manifest) {
  if (manifest.canvas?.width !== 64 || manifest.canvas?.height !== 64) {
    fail(`canvas must be 64x64, got ${JSON.stringify(manifest.canvas)}`);
  }
  const layerOrder = manifest.layerOrder;
  const wantOrder = ["frame", "headwear", "eyes", "workspaceBadge", "taskTool"];
  if (JSON.stringify(layerOrder) !== JSON.stringify(wantOrder)) {
    fail(`layerOrder must be ${wantOrder.join(" → ")}, got ${JSON.stringify(layerOrder)}`);
  }
  if (manifest.frames?.length !== EXPECT.frames) fail(`frames count = ${manifest.frames?.length}, want ${EXPECT.frames}`);
  if (manifest.hats?.length !== EXPECT.hats) fail(`hats count = ${manifest.hats?.length}, want ${EXPECT.hats}`);
  if (manifest.hair?.length !== EXPECT.hair) fail(`hair count = ${manifest.hair?.length}, want ${EXPECT.hair}`);
  if (manifest.tools?.length !== EXPECT.tools) fail(`tools count = ${manifest.tools?.length}, want ${EXPECT.tools}`);
  if (manifest.eyes?.length !== EXPECT.eyes) fail(`eyes count = ${manifest.eyes?.length}, want ${EXPECT.eyes}`);
  manifest.eyes.forEach((eye, index) => {
    if (eye.frames?.length !== EXPECT.framesPerEye) fail(`eye ${eye.id} frames = ${eye.frames?.length}, want ${EXPECT.framesPerEye}`);
    if (eye.fps !== EXPECT.fps[index]) fail(`eye ${eye.id} fps = ${eye.fps}, want ${EXPECT.fps[index]}`);
    if (typeof eye.loop !== "boolean" || typeof eye.holdLast !== "boolean") fail(`eye ${eye.id} loop/holdLast must be booleans`);
    if (!eye.sprite) fail(`eye ${eye.id} missing sprite path`);
  });
  for (const entry of collectEntries(manifest)) {
    if (!(await fileExists(join(sourceDir, entry)))) fail(`manifest references missing source file: ${entry}`);
  }
}

async function fileExists(path) {
  try {
    await readFile(path);
    return true;
  } catch {
    return false;
  }
}

async function copyDirRecursive(sourcePath, targetPath, copied) {
  const entries = await readdir(sourcePath, { withFileTypes: true });
  for (const entry of entries) {
    const from = join(sourcePath, entry.name);
    const rel = `${relative(sourceDir, from).replaceAll("\\", "/")}`;
    if (entry.isDirectory()) {
      await copyDirRecursive(from, join(targetPath, entry.name), copied);
    } else if (entry.isFile() && entry.name.endsWith(".png")) {
      await mkdir(targetPath, { recursive: true });
      await cp(from, join(targetPath, entry.name));
      copied.push(rel);
    }
  }
}

async function copyRuntime(manifest) {
  // Deterministic full replacement: build a fresh temp dir next to the target,
  // then swap. Any residue from a previous crashed run is removed first.
  await rm(tempDir, { recursive: true, force: true });
  await mkdir(tempDir, { recursive: true });

  const copied = [];
  for (const dir of DIRS) {
    await copyDirRecursive(join(sourceDir, dir), join(tempDir, dir), copied);
  }
  await cp(join(sourceDir, "manifest.json"), join(tempDir, "manifest.json"));

  // Every manifest reference must exist in the copied set (no missing layers),
  // and every copied file must be referenced by the manifest or be a template/
  // sprite the manifest already declares. This catches both drift directions.
  const referenced = new Set(collectEntries(manifest));
  for (const rel of copied) {
    if (!referenced.has(rel)) fail(`copied ${rel} is not referenced by the manifest`);
  }
  for (const rel of referenced) {
    if (!(await fileExists(join(tempDir, rel)))) fail(`manifest references missing copied file: ${rel}`);
  }
}

async function verifyCopied(manifest) {
  const failures = [];
  for (const entry of collectEntries(manifest)) {
    const source = join(sourceDir, entry);
    const copied = join(tempDir, entry);
    const [a, b] = await Promise.all([readFile(source), readFile(copied)]);
    if (sha256(a) !== sha256(b)) failures.push(`sha256 mismatch: ${entry}`);
  }
  // Sprite width contract: every eye sprite is 384px (6 frames × 64px).
  for (const eye of manifest.eyes) {
    const png = await readFile(join(tempDir, eye.sprite));
    if (pngWidth(png) !== 384) failures.push(`sprite ${eye.sprite} width = ${pngWidth(png)}, want 384`);
  }
  if (failures.length) fail(failures.join("\n  "));
}

async function main() {
  const raw = await readFile(join(sourceDir, "manifest.json"), "utf8");
  let manifest;
  try {
    manifest = JSON.parse(raw);
  } catch (cause) {
    fail(`source manifest is not valid JSON: ${cause.message}`);
  }
  await validateSource(manifest);
  await copyRuntime(manifest);
  await verifyCopied(manifest);

  await rm(targetDir, { recursive: true, force: true });
  try {
    // Directory rename over a freshly removed target can still hit an open
    // handle on Windows; fall back to a recursive copy + cleanup then.
    await rename(tempDir, targetDir);
  } catch {
    await cp(tempDir, targetDir, { recursive: true });
    await rm(tempDir, { recursive: true, force: true });
  }
  console.log(`[sync-agent-icon] synced ${collectEntries(manifest).length + 1} files → src/assets/agent-icon/`);
}

main().catch((cause) => {
  console.error(cause instanceof Error ? cause.message : cause);
  process.exit(1);
});
