import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { ArtifactImageCard, ArtifactImagesForTool } from "../components/ArtifactImageCard";
import { TurnCollapse } from "../components/Transcript";
import { LocaleProvider } from "../lib/i18n";
import { useArtifactStore } from "../store/artifacts";
import type { ArtifactRecord } from "../store/artifacts";
import type { Item } from "../lib/useController";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed++; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed++; }
}

function setupDOM() {
  const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/" });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.HTMLElement = dom.window.HTMLElement;
  return dom;
}

function seedArtifact(record: ArtifactRecord) {
  useArtifactStore.getState().upsertArtifact(record);
}

function clearArtifacts() {
  useArtifactStore.getState().clearAllArtifacts();
}

// CSS must be loaded so turn-collapse styles and GSAP hooks exist. The test
// harness resolves paths relative to the test file.
const gsapPath = resolve(import.meta.dirname ?? ".", "..", "..", "node_modules", "gsap", "dist", "gsap.min.js");
try {
  const gsapSrc = readFileSync(gsapPath, "utf8");
  // We don't need to eval in Node, but we do need the CSS globals.
} catch { /* gsap.min.js may be absent; TurnCollapse falls back gracefully */ }

console.log("\nartifact image card display + TurnCollapse placement");

// ── ArtifactImageCard: loads to loaded state ──
{
  setupDOM();
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ArtifactImageCard tabId="test-tab" artifactId="img-1" name="screenshot.png" />
      </LocaleProvider>
    );
  });
  await act(async () => { await new Promise((r) => setTimeout(r, 10)); });
  ok(document.querySelector(".artifact-image__thumb") !== null, "renders thumbnail after mock data URL load");
  ok(document.querySelector(".artifact-image__actions") !== null, "renders action buttons after load");
  await act(async () => root.unmount());
}

// ── ArtifactImageCard: fullscreen overlay ──
{
  setupDOM();
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ArtifactImageCard tabId="test-tab" artifactId="img-1" name="screenshot.png" />
      </LocaleProvider>
    );
  });
  await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
  const fullscreenBtns = [...document.querySelectorAll(".artifact-image__btn")] as HTMLButtonElement[];
  const viewBtn = fullscreenBtns.find((b) => b.textContent?.includes("fullscreen") || b.title?.includes("fullscreen"));
  if (viewBtn) {
    await act(async () => viewBtn.click());
    ok(document.querySelector(".artifact-image__overlay") !== null, "fullscreen overlay opens on button click");
    const closeBtn = document.querySelector(".artifact-image__overlay-close") as HTMLButtonElement;
    if (closeBtn) {
      await act(async () => closeBtn.click());
      ok(document.querySelector(".artifact-image__overlay") === null, "overlay closes via close button");
    }
  } else {
    const thumbBtn = document.querySelector(".artifact-image__thumb-btn") as HTMLButtonElement;
    if (thumbBtn) {
      await act(async () => thumbBtn.click());
      ok(document.querySelector(".artifact-image__overlay") !== null, "fullscreen overlay opens on thumbnail click");
      const closeBtn = document.querySelector(".artifact-image__overlay-close") as HTMLButtonElement;
      await act(async () => closeBtn.click());
      ok(document.querySelector(".artifact-image__overlay") === null, "overlay closes via close button");
    }
  }
  await act(async () => root.unmount());
}

// ── ArtifactImagesForTool: empty state (no images) ──
{
  setupDOM();
  clearArtifacts();
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ArtifactImagesForTool tabId="test-tab" toolIds={["tool-1"]} />
      </LocaleProvider>
    );
  });
  ok(document.querySelector(".artifact-images") === null, "renders nothing when no image artifacts");
  await act(async () => root.unmount());
}

// ── ArtifactImagesForTool: shows available image ──
{
  setupDOM();
  clearArtifacts();
  seedArtifact({ artifactId: "img-avail", name: "result.png", type: "image", status: "available", sessionId: "test-tab", path: "/tmp/result.png", sourceRunId: "tool-1" });
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ArtifactImagesForTool tabId="test-tab" toolIds={["tool-1"]} />
      </LocaleProvider>
    );
  });
  ok(document.querySelector(".artifact-images") !== null, "renders container when image artifact exists");
  ok(document.querySelector(".artifact-image__thumb-btn") !== null || document.querySelector(".artifact-image__placeholder") !== null, "renders card for available image");
  await act(async () => root.unmount());
}

// ── ArtifactImagesForTool: skips non-image artifacts ──
{
  setupDOM();
  clearArtifacts();
  seedArtifact({ artifactId: "bin-1", name: "app.exe", type: "binary", status: "available", sessionId: "test-tab", path: "/tmp/app.exe", sourceRunId: "tool-1" });
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ArtifactImagesForTool tabId="test-tab" toolIds={["tool-1"]} />
      </LocaleProvider>
    );
  });
  ok(document.querySelector(".artifact-images") === null, "skips non-image artifacts");
  await act(async () => root.unmount());
}

// ── ArtifactImagesForTool: skips missing/non-available ──
{
  setupDOM();
  clearArtifacts();
  seedArtifact({ artifactId: "img-missing", name: "missing.png", type: "image", status: "missing", sessionId: "test-tab", sourceRunId: "tool-1" });
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ArtifactImagesForTool tabId="test-tab" toolIds={["tool-1"]} />
      </LocaleProvider>
    );
  });
  ok(document.querySelector(".artifact-images") === null, "skips missing image artifacts");
  await act(async () => root.unmount());
}

// ── ArtifactImagesForTool: multiple images across tools ──
{
  setupDOM();
  clearArtifacts();
  seedArtifact({ artifactId: "img-a", name: "a.png", type: "image", status: "available", sessionId: "test-tab", sourceRunId: "tool-a" });
  seedArtifact({ artifactId: "img-b", name: "b.png", type: "image", status: "available", sessionId: "test-tab", sourceRunId: "tool-b" });
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ArtifactImagesForTool tabId="test-tab" toolIds={["tool-a", "tool-b"]} />
      </LocaleProvider>
    );
  });
  const cards = document.querySelectorAll(".artifact-image");
  ok(cards.length === 2, `renders images for multiple tool IDs (got ${cards.length})`);
  await act(async () => root.unmount());
}

// ── ArtifactImagesForTool: scoped to correct tab ──
{
  setupDOM();
  clearArtifacts();
  seedArtifact({ artifactId: "img-other-tab", name: "other.png", type: "image", status: "available", sessionId: "other-tab", sourceRunId: "tool-1" });
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ArtifactImagesForTool tabId="test-tab" toolIds={["tool-1"]} />
      </LocaleProvider>
    );
  });
  ok(document.querySelector(".artifact-images") === null, "does not show artifacts from other tab");
  await act(async () => root.unmount());
}

// ── ArtifactImagesForTool: idempotent on repeat ──
{
  setupDOM();
  clearArtifacts();
  seedArtifact({ artifactId: "img-idem", name: "stable.png", type: "image", status: "available", sessionId: "test-tab", sourceRunId: "tool-idem" });
  const root = createRoot(document.getElementById("root")!);
  for (let i = 0; i < 3; i++) {
    await act(async () => {
      root.render(
        <LocaleProvider>
          <ArtifactImagesForTool tabId="test-tab" toolIds={["tool-idem"]} />
        </LocaleProvider>
      );
    });
  }
  const cards = document.querySelectorAll(".artifact-image");
  ok(cards.length === 1, "idempotent on repeated render");
  await act(async () => root.unmount());
}

// ── RequestHelpCard: no longer has inline image ──
{
  const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/" });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.HTMLElement = dom.window.HTMLElement;
  const { RequestHelpCard } = await import("../components/RequestHelpCard");
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <RequestHelpCard
          status={{ phase: "completed", capability: "image_generation", artifact: { task_id: "t1", path: "/tmp/img.png", mime: "image/png", size: 100 } }}
          args="{}"
          entranceId="help-no-img"
        />
      </LocaleProvider>
    );
  });
  ok(document.querySelector(".request-help__image") === null, "RequestHelpCard no longer shows inline image preview");
  ok(document.querySelector(".request-help__overlay") === null, "RequestHelpCard no longer shows overlay");
  ok(document.querySelector(".request-help__title") !== null, "RequestHelpCard still shows title/status");
  await act(async () => root.unmount());
}

// ── TurnCollapse: image card outside body when fold is closed ──
{
  setupDOM();
  clearArtifacts();
  seedArtifact({ artifactId: "img-shell", name: "screenshot.png", type: "image", status: "available", sessionId: "test-tab", path: "/tmp/screenshot.png", sourceRunId: "tc-shell" });

  const shellTool: Item = { kind: "tool", id: "tc-shell", name: "bash", args: "{}", readOnly: false, status: "done", isShell: true };
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <TurnCollapse
          items={[shellTool]}
          durationMs={1200}
          mode="normal"
          subcalls={new Map()}
          tabId="test-tab"
        />
      </LocaleProvider>
    );
  });
  await act(async () => { await new Promise((r) => setTimeout(r, 50)); });

  // The fold should be closed by default (not active) since status is "done".
  const collapseEl = document.querySelector(".turn-collapse");
  ok(collapseEl !== null, "TurnCollapse renders");
  ok(!collapseEl!.classList.contains("turn-collapse--open"), "TurnCollapse starts closed");

  // The body should still contain the tool card, but the image should be OUTSIDE.
  const bodyEl = document.querySelector(".turn-collapse__body");
  ok(bodyEl !== null, "turn-collapse__body exists");

  // The artifact-images container should be a sibling of the body, not inside it.
  const imagesEl = document.querySelector(".artifact-images");
  ok(imagesEl !== null, "artifact-images container exists");
  ok(imagesEl!.parentElement === collapseEl, "artifact-images is a direct child of turn-collapse");
  ok(!bodyEl!.contains(imagesEl), "artifact-images is NOT inside turn-collapse__body");

  // The image card should be visible in the DOM even when fold is closed.
  ok(document.querySelector(".artifact-image__thumb") !== null || document.querySelector(".artifact-image__placeholder") !== null, "image preview is visible when fold is closed");

  await act(async () => root.unmount());
}

// ── TurnCollapse: image card with nested tool IDs ──
{
  setupDOM();
  clearArtifacts();
  seedArtifact({ artifactId: "img-nested", name: "nested.png", type: "image", status: "available", sessionId: "test-tab", path: "/tmp/nested.png", sourceRunId: "tc-nested" });

  const parentTool: Item = { kind: "tool", id: "tc-parent", name: "task", args: "{}", readOnly: false, status: "done" };
  const nestedTool: Item = { kind: "tool", id: "tc-nested", name: "bash", args: "{}", readOnly: false, status: "done", isShell: true, parentId: "tc-parent" };
  const subcalls = new Map<string, Item[]>([["tc-parent", [nestedTool]]]);

  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <TurnCollapse
          items={[parentTool]}
          durationMs={800}
          mode="normal"
          subcalls={subcalls}
          tabId="test-tab"
        />
      </LocaleProvider>
    );
  });
  await act(async () => { await new Promise((r) => setTimeout(r, 50)); });

  // The nested tool ID should be collected from subcalls and its image shown.
  ok(document.querySelector(".artifact-images") !== null, "nested tool image shown via subcalls collection");
  ok(document.querySelector(".artifact-image__thumb") !== null || document.querySelector(".artifact-image__placeholder") !== null, "nested tool image preview visible");

  await act(async () => root.unmount());
}

// ── TurnCollapse: no artifact-images when no images seeded ──
{
  setupDOM();
  clearArtifacts();
  const shellTool: Item = { kind: "tool", id: "tc-noimg", name: "bash", args: "{}", readOnly: false, status: "done", isShell: true };
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <TurnCollapse
          items={[shellTool]}
          durationMs={500}
          mode="normal"
          subcalls={new Map()}
          tabId="test-tab"
        />
      </LocaleProvider>
    );
  });
  await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
  ok(document.querySelector(".artifact-images") === null, "no artifact-images container when no image artifacts");
  await act(async () => root.unmount());
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
