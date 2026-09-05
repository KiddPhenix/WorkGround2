// Run: node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/creation-workspace-switcher.test.tsx

import { JSDOM } from "jsdom";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";

import {
  CreationWorkspaceSwitcher,
  CreationWorkspaceHeader,
  creationWorkspaceChoices,
  creationWorkspaceChoicesFromOptions,
  creationWorkspaceIndex,
  showsBlankSessionWorkspaceHeader,
  type CreationWorkspaceChoice,
} from "../components/CreationWorkspaceSwitcher";
import { LocaleProvider } from "../lib/i18n";
import type { ProjectNode } from "../lib/types";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1;
  else failed += 1;
}

function eq<T>(actual: T, expected: T, label: string) {
  ok(Object.is(actual, expected), `${label}${Object.is(actual, expected) ? "" : ` (expected ${String(expected)}, got ${String(actual)})`}`);
}

console.log("\ncreation workspace switcher");

const nodes: ProjectNode[] = [
  { key: "project:a", kind: "project", label: "Alpha", root: "D:\\Work\\Alpha" },
  { key: "project:b", kind: "project", label: "WorkGround2", root: "D:/Work/WorkGround2" },
  { key: "duplicate:b", kind: "project", label: "Duplicate", root: "d:\\work\\workground2\\" },
  { key: "global", kind: "global_folder", label: "Global" },
  { key: "topic", kind: "topic", label: "Ignored" },
];
const choices = creationWorkspaceChoices(nodes);
eq(choices.length, 3, "projects and Global are selectable while duplicate roots and topics are ignored");
eq(choices[1]?.name, "WorkGround2", "backend project order and display name are preserved");
eq(creationWorkspaceIndex(choices, "project", "d:\\WORK\\WORKGROUND2\\"), 1, "Windows roots match case-insensitively across separators");
eq(creationWorkspaceIndex(choices, "global", ""), 2, "Global resolves to its own selectable destination");
const lightweightChoices = creationWorkspaceChoicesFromOptions([
  { scope: "auto", name: "自动" },
  { scope: "project", name: "Alpha", root: "D:/Work/Alpha" },
  { scope: "project", name: "WorkGround2", root: "D:/Work/WorkGround2" },
  { scope: "global", name: "Global" },
]);
eq(lightweightChoices.length, 3, "the lightweight workspace source excludes Auto and includes projects plus Global");
eq(creationWorkspaceChoicesFromOptions([])[0]?.scope, "global", "Global remains available when workspace loading fails");
ok(showsBlankSessionWorkspaceHeader(true, undefined, false), "the new-Session action enables the workspace header without waiting for tab metadata");
ok(showsBlankSessionWorkspaceHeader(false, true, false), "the backend blank marker restores the workspace header after restart");
ok(!showsBlankSessionWorkspaceHeader(false, undefined, false), "an unrelated empty surface does not inherit the new-Session state");
ok(!showsBlankSessionWorkspaceHeader(true, true, true), "the workspace header leaves as soon as the Session has conversation content");

const bridgeSource = readFileSync(resolve(import.meta.dirname, "../lib/bridge.ts"), "utf8");
const appSource = readFileSync(resolve(import.meta.dirname, "../App.tsx"), "utf8");
const sessionSurfaceSource = readFileSync(resolve(import.meta.dirname, "../components/SessionSurface.tsx"), "utf8");
ok(
  /headerAccessory: blankSessionWorkspaceHeader \? \([\s\S]{0,500}<CreationWorkspaceSwitcher/.test(appSource)
    && /<h1 className="session-header__title"[\s\S]{0,160}\{headerAccessory\}/.test(sessionSurfaceSource),
  "the Workbench SessionSurface renders the switcher directly beside its title",
);
ok(
  /app\.ListWidgetWorkspaces\(\)[\s\S]{0,300}creationWorkspaceChoicesFromOptions/.test(appSource),
  "blank Session switching uses the lightweight workspace registry instead of the fallible transcript tree",
);
ok(
  /async CreateBlankSession[\s\S]{0,1200}const tab = \{ \.\.\.opened, blank: true \}[\s\S]{0,240}return tab;/.test(bridgeSource),
  "the browser mock preserves the backend blank marker for the project-tree new Session path",
);
ok(
  /async EnsureBlankTab[\s\S]{0,700}const blank = \{ \.\.\.existing, active: true, blank: true \}[\s\S]{0,180}return blank;/.test(bridgeSource),
  "the browser mock persists the blank marker when the primary new-Session action reuses an empty tab",
);

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
  pretendToBeVisual: true,
  url: "http://localhost/",
});
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.Element = dom.window.Element;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const host = document.getElementById("root")!;
const root = createRoot(host);
let selected: CreationWorkspaceChoice | null = null;
const render = (activeScope = "project", activeRoot = "D:/Work/WorkGround2") => act(() => root.render(
  <LocaleProvider>
    <CreationWorkspaceSwitcher
      choices={choices}
      activeScope={activeScope}
      activeRoot={activeRoot}
      activeName="WorkGround2"
      onSelect={(choice) => { selected = choice; }}
    />
  </LocaleProvider>,
));

render();
ok(host.textContent?.includes("WorkGround2 · 2 / 3") ?? false, "active project name and position are shown in the switcher");
const buttons = host.querySelectorAll<HTMLButtonElement>("button");
act(() => buttons[1].click());
eq(selected?.key, "global", "next button selects the following workspace destination");
selected = null;
act(() => buttons[0].click());
eq(selected?.key, choices[0].key, "previous button selects the preceding project destination");

selected = null;
act(() => window.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "ArrowRight", ctrlKey: true, bubbles: true })));
eq(selected?.key, "global", "Ctrl+Right uses the same workspace selection path");

selected = null;
const input = document.createElement("input");
document.body.appendChild(input);
act(() => input.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "ArrowRight", ctrlKey: true, bubbles: true })));
eq(selected?.key, "global", "Ctrl+Right switches workspaces even while the composer owns focus");

render("project", "D:/Transient");
ok(host.textContent?.includes("WorkGround2 · 1 / 4") ?? false, "an active project missing from a stale tree remains visible and selectable");

act(() => root.render(
  <LocaleProvider>
    <CreationWorkspaceHeader
      title="新建会话"
      titleHint="新建会话"
      choices={choices}
      activeScope="project"
      activeRoot="D:/Work/WorkGround2"
      activeName="WorkGround2"
      onSelect={() => {}}
    />
  </LocaleProvider>,
));
ok(
  host.querySelector("h1")?.textContent === "新建会话"
    && host.querySelector(".creation-workspace-switcher__current")?.textContent === "WorkGround2 · 2 / 3",
  "blank-session title remains beside the workspace switcher",
);

act(() => root.unmount());
console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
