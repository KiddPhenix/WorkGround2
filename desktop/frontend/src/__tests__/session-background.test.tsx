import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { SessionBackground } from "../components/SessionBackground";
import type { SessionBackgroundSettingsView } from "../lib/types";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed++; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed++; }
}

function setupDOM(settings: SessionBackgroundSettingsView) {
  const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/" });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  globalThis.HTMLElement = dom.window.HTMLElement;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });

  class InstantImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    complete = true;
    naturalWidth = 100;
    set src(_value: string) { queueMicrotask(() => this.onload?.()); }
    decode() { return Promise.resolve(); }
  }
  globalThis.Image = InstantImage as unknown as typeof Image;

  let changed: (() => void) | null = null;
  const app = {
    SessionBackgroundSettings: async () => structuredClone(settings),
    SessionBackground: async () => ({ path: "C:\\bg\\one.png", url: "/media/one.png" }),
    RotateSessionBackground: async () => ({ path: "C:\\bg\\two.png", url: "/media/two.png" }),
  };
  (dom.window as unknown as { go: unknown }).go = { main: { App: app } };
  (dom.window as unknown as { runtime: unknown }).runtime = {
    EventsOn: (name: string, callback: () => void) => {
      if (name === "session-background:changed") changed = callback;
      return () => { changed = null; };
    },
  };
  return { dom, app, emitChanged: () => changed?.() };
}

console.log("\nSession background rendering and contracts");

{
  const settings: SessionBackgroundSettingsView = {
    enabled: true,
    maskEnabled: true,
    randomOnOpen: true,
    rotateSeconds: 0,
    imageCount: 2,
    sources: [],
  };
  const { emitChanged } = setupDOM(settings);
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<SessionBackground tabId="tab-a" />);
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  ok(document.querySelector(".session-background__image--current") !== null, "renders the decoded current background");
  ok(document.querySelector(".session-background__mask") !== null, "renders the theme mask when enabled");

  settings.maskEnabled = false;
  await act(async () => {
    emitChanged();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  ok(document.querySelector(".session-background__mask") === null, "live settings event removes the mask");
  await act(async () => root.unmount());
}

{
  setupDOM({ enabled: false, maskEnabled: true, randomOnOpen: true, rotateSeconds: 0, imageCount: 1, sources: [] });
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<SessionBackground tabId="tab-disabled" />);
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  ok(document.querySelector(".session-background") === null, "disabled setting leaves the theme surface untouched");
  await act(async () => root.unmount());
}

const testDir = dirname(fileURLToPath(import.meta.url));
const componentSource = readFileSync(resolve(testDir, "../components/SessionBackground.tsx"), "utf8");
const appSource = readFileSync(resolve(testDir, "../App.tsx"), "utf8");
const settingsSource = readFileSync(resolve(testDir, "../components/SettingsPanel.tsx"), "utf8");
const cssSource = readFileSync(resolve(testDir, "../styles.css"), "utf8");

ok((appSource.match(/<SessionBackground tabId=\{activeTabId\}/g) ?? []).length === 2, "both workbench and classic Session surfaces mount the background");
ok(componentSource.includes('document.addEventListener("visibilitychange"') && componentSource.includes("Date.now() >= dueAt"), "rotation pauses while hidden and catches up at most once");
ok(componentSource.includes("image.decode()") && componentSource.includes("prefers-reduced-motion") === false, "component decodes images before swapping layers");
ok(cssSource.includes("@media (prefers-reduced-motion: reduce)") && cssSource.includes("color-mix(in srgb, var(--bg)"), "CSS provides reduced-motion and theme-derived masking");
ok(cssSource.includes(".session-workspace:has(> .session-background) .task-memory-bar") && cssSource.includes("backdrop-filter: blur(8px)"), "background sessions soften the memory rail instead of painting an opaque stripe");
ok(settingsSource.includes("PickSessionBackgroundFiles") && settingsSource.includes("PickSessionBackgroundFolder") && settingsSource.includes("recursive"), "Appearance settings manage multiple image and folder sources");

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
