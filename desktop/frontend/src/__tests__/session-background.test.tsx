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
    mode: "custom",
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
  setupDOM({ mode: "pattern", enabled: false, maskEnabled: true, randomOnOpen: true, rotateSeconds: 0, imageCount: 0, sources: [] });
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<SessionBackground tabId="tab-pattern" />);
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  ok(document.querySelector(".session-background__pattern") !== null, "built-in pattern renders without an image source");
  await act(async () => root.unmount());
}

{
  setupDOM({ mode: "solid", enabled: false, maskEnabled: true, randomOnOpen: true, rotateSeconds: 0, imageCount: 0, sources: [] });
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<SessionBackground tabId="tab-solid" />);
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  ok(document.querySelector(".session-background__solid") !== null, "solid mode keeps a clean theme-colored background");
  await act(async () => root.unmount());
}

const testDir = dirname(fileURLToPath(import.meta.url));
const componentSource = readFileSync(resolve(testDir, "../components/SessionBackground.tsx"), "utf8");
const appSource = readFileSync(resolve(testDir, "../App.tsx"), "utf8");
const sessionSurfaceSource = readFileSync(resolve(testDir, "../components/SessionSurface.tsx"), "utf8");
const workWorkspaceSource = readFileSync(resolve(testDir, "../components/work/WorkWorkspace.tsx"), "utf8");
const collaborationSource = readFileSync(resolve(testDir, "../collab/CollaborationWorkspace.tsx"), "utf8");
const settingsSource = readFileSync(resolve(testDir, "../components/SettingsPanel.tsx"), "utf8");
const cssSource = readFileSync(resolve(testDir, "../styles.css"), "utf8");
const darkPatternSource = readFileSync(resolve(testDir, "../assets/session-pattern-dark.svg"), "utf8");
const lightPatternSource = readFileSync(resolve(testDir, "../assets/session-pattern-light.svg"), "utf8");

ok((appSource.match(/<SessionBackground tabId=\{activeTabId\}/g) ?? []).length === 2, "both workbench and classic Session surfaces mount the background");
ok(appSource.indexOf("<SessionBackground tabId={activeTabId} />") < appSource.indexOf("<aside className={`workspace-sidebar"), "workbench background sits below both navigation and Session plates");
ok(!sessionSurfaceSource.includes("<SessionBackground") && sessionSurfaceSource.includes("session-workspace--running"), "Session surface exposes running state without mounting a duplicate background");
ok(componentSource.includes('document.addEventListener("visibilitychange"') && componentSource.includes("Date.now() >= dueAt"), "rotation pauses while hidden and catches up at most once");
ok(componentSource.includes("image.decode()") && componentSource.includes("prefers-reduced-motion") === false, "component decodes images before swapping layers");
ok(cssSource.includes("@media (prefers-reduced-motion: reduce)") && cssSource.includes("color-mix(in srgb, var(--bg)"), "CSS provides reduced-motion and theme-derived masking");
ok(cssSource.includes(".session-workspace:has(> .session-background) .task-memory-bar") && cssSource.includes("backdrop-filter: blur(8px)"), "background sessions soften the memory rail instead of painting an opaque stripe");
ok(cssSource.includes(".layout--workbench > .session-background") && cssSource.includes("session-run-track") && cssSource.includes("session-active-marker"), "workbench uses one wallpaper layer and restrained active-state motion");
ok(
  cssSource.includes(".app--workbench .layout--workbench.layout--sidebar-collapsed {\n  grid-template-columns: minmax(0, 1fr);\n  gap: 0;\n}"),
  "collapsed workbench removes the hidden sidebar track and orphan gap",
);
ok(
  appSource.includes('"app--workbench-room"') &&
    appSource.includes('"app--workbench-work"') &&
    appSource.includes('"app--workbench-session"'),
  "workbench exposes stable Session, Room, and Work surface identities",
);
ok(
  cssSource.includes(".app--windows-frameless.app--workbench-session .session-header__actions::before") &&
    cssSource.includes("inset: 0 calc(0px - var(--windows-window-controls-width)) 0 -3px;") &&
    cssSource.includes(".app--windows-frameless.app--workbench-work .wg2-work-top-actions::before") &&
    cssSource.includes(".app--windows-frameless.app--workbench-room .collab-topic-actions::before") &&
    cssSource.includes("right: calc(14px + var(--windows-window-controls-width));") &&
    cssSource.includes(".collab-topic-actions {\n  display: flex;") &&
    workWorkspaceSource.includes('className="wg2-work-top-actions"') &&
    collaborationSource.includes('className="collab-topic-actions"'),
  "Session, Work, and Room each join their existing actions to the caption rail",
);
ok(
  cssSource.includes(':root[data-theme="light"] .app--workbench {') &&
    cssSource.includes("--bg: #f7f8fb;") &&
    cssSource.includes(':root[data-theme="light"] .app--workbench .workspace-sidebar {') &&
    cssSource.includes(':root[data-theme="light"] .app--workbench .session-workspace {') &&
    cssSource.includes(':root[data-theme="light"] .app--windows-frameless.app--workbench-session .session-header__actions::before,') &&
    cssSource.includes(':root[data-theme="light"] .app--windows-frameless.app--workbench-work .wg2-work-top-actions::before,') &&
    cssSource.includes(':root[data-theme="light"] .app--windows-frameless.app--workbench-room .collab-topic-actions::before'),
  "light mode restores a complete workbench palette and all three caption rails",
);
ok(
  collaborationSource.includes('<section className="collab-surface"') &&
    cssSource.includes(".app--workbench .collab-surface {") &&
    cssSource.includes("border-radius: 8px;") &&
    !cssSource.includes(".app--workbench .collaboration-workspace"),
  "Room mounts the shared rounded workbench plate on its real surface root",
);
ok(
  cssSource.includes(".app--workbench .collab-surface:not(.collab-surface--dialog) {") &&
    cssSource.includes(".app--workbench .collab-surface:not(.collab-surface--dialog)::before {") &&
    cssSource.includes("backdrop-filter: blur(7px) saturate(0.92);") &&
    cssSource.includes(".app--windows-frameless.app--workbench-room .collab-members {") &&
    cssSource.includes("padding-top: calc(20px + var(--windows-window-controls-height));"),
  "Room keeps a translucent wallpaper plate and fills the caption-safe member panel gap",
);
ok(
  cssSource.includes("--session-heading-surface: rgba(17, 18, 24, 0.43);") &&
    cssSource.includes(".app--workbench .session-header {\n  height: 64px;\n  box-sizing: border-box;\n  align-items: flex-start;\n  padding: 26px 48px 4px;") &&
    cssSource.includes(".app--workbench .session-header__title {\n  margin: 0;") &&
    cssSource.includes(".app--workbench .task-memory-bar {\n  height: 36px;") &&
    cssSource.includes(".app--workbench .session-workspace:has(> .session-background) .task-memory-bar {") &&
    cssSource.includes("background: var(--session-heading-surface);") &&
    cssSource.includes(':root[data-theme="light"] .app--workbench .session-footer-dock {\n  background: transparent;'),
  "Session title and recap form one compact information group while the light composer area keeps the wallpaper clear",
);
ok(
  cssSource.includes(".app--workbench .session-footer-dock:has(.composer-toolbar--status-only) .artifact-shelf {") &&
    cssSource.includes("padding-right: var(--composer-runstatus-lane);") &&
    cssSource.includes(".app--workbench .session-footer-dock .composer-wrap > .composer-toolbar--status-only {") &&
    cssSource.includes("position: absolute;") &&
    cssSource.includes("bottom: calc(100% + 9px);"),
  "foreground controls reserve a right-side lane without changing artifact shelf height",
);
ok(
  cssSource.includes(".app--workbench .work-session-host {") &&
    cssSource.includes("backdrop-filter: none;") &&
    cssSource.includes(".app--workbench .work-session-host::before {") &&
    cssSource.includes("backdrop-filter: blur(6px) saturate(0.94);") &&
    cssSource.includes("fixed descendants"),
  "Work material does not trap the viewport-fixed caption rail in a filtered containing block",
);
ok(settingsSource.includes("PickSessionBackgroundFiles") && settingsSource.includes("PickSessionBackgroundFolder") && settingsSource.includes("recursive"), "Appearance settings manage multiple image and folder sources");
ok(
  settingsSource.includes('const themeOptions = ["light", "dark"] as const') &&
    !settingsSource.includes("THEME_STYLES.map") &&
    settingsSource.includes('(["pattern", "solid", "custom"] as const)'),
  "Appearance settings expose two color schemes and three coherent background modes",
);
ok(
  cssSource.includes('url("./assets/session-pattern-light.svg")') &&
    cssSource.includes('url("./assets/session-pattern-dark.svg")') &&
    cssSource.includes("background-size: 340px 340px;") &&
    darkPatternSource.includes('opacity=".20"') &&
    lightPatternSource.includes('opacity=".17"') &&
    cssSource.includes("background: rgba(19, 21, 27, 0.82);") &&
    cssSource.includes("background: rgba(11, 12, 17, 0.58);") &&
    cssSource.includes("background: rgba(250, 251, 253, 0.62);") &&
    cssSource.includes("background: rgba(248, 250, 253, 0.78);"),
  "light and dark pattern presets remain subtle but visible through both workbench plates",
);
ok(
  cssSource.includes("--wg2-accent: var(--accent);") &&
    cssSource.includes("background: var(--control-primary-bg, var(--accent));") &&
    !cssSource.includes("--accent: #cb83dc;"),
  "Workbench controls consume the retained theme color interface instead of pinning violet",
);

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
