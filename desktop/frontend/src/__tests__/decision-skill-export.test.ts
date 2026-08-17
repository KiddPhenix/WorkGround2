// Run: node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/decision-skill-export.test.ts
//
// Contract tests for the "导出 Skill" flow in the owner-decision centre:
//  - bridge: the browser mock implements ExportDecisionSkills and returns the
//    DecisionSkillExportResult shape (exported/canceled/path);
//  - component: the button renders under "安装 / 更新 Codex Skill", routes
//    exported/canceled/failure through notice/error, and blocks re-entry while
//    an export is running.

import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { app, setMockDecisionSkillExport } from "../lib/bridge";
import { DecisionCenter } from "../components/DecisionCenter";
import type { DecisionSkillExportResult } from "../lib/types";

console.log("\ndecision skill export contract");

// ── Bridge contract (no DOM needed) ─────────────────────────────────────────

assert.equal(typeof app.ExportDecisionSkills, "function", "bridge exposes ExportDecisionSkills");

const canned = await app.ExportDecisionSkills();
assert.equal(canned.exported, true, "default mock reports exported");
assert.equal(canned.canceled, false, "default mock is not canceled");
assert.equal(typeof canned.path, "string", "default mock carries a path");

setMockDecisionSkillExport(async () => ({ exported: false, canceled: true }));
const canceled = await app.ExportDecisionSkills();
assert.equal(canceled.exported, false, "overridden mock can report canceled");
assert.equal(canceled.canceled, true, "overridden mock cancels");
setMockDecisionSkillExport(null);
assert.equal((await app.ExportDecisionSkills()).exported, true, "reset mock restores default success");

// ── Component contract (JSDOM + React) ──────────────────────────────────────

let _root: Root | null = null;
let _rootEl: HTMLElement | null = null;

function installDom(): void {
  const dom = new JSDOM('<!doctype html><html><head></head><body><div id="root"></div></body></html>', {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  globalThis.Node = dom.window.Node;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.localStorage = dom.window.localStorage;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing #root");
  _rootEl = rootEl;
}

function renderCenter(): HTMLElement {
  if (!_rootEl) throw new Error("DOM not installed");
  const root = _root ?? createRoot(_rootEl);
  _root = root;
  act(() => root.render(React.createElement(DecisionCenter, { open: true, onClose: () => {} })));
  return _rootEl;
}

async function flush(): Promise<void> {
  await act(async () => {});
}

function cleanup(): void {
  const root = _root;
  if (root) {
    act(() => root.unmount());
    _root = null;
  }
  setMockDecisionSkillExport(null);
}

function centerButtons(): { install: HTMLButtonElement; export: HTMLButtonElement } {
  const buttons = [...(_rootEl?.querySelectorAll("button") ?? [])] as HTMLButtonElement[];
  const install = buttons.find((b) => b.textContent?.includes("安装 / 更新 Codex Skill"));
  const exportButton = buttons.find((b) => b.textContent === "导出 Skill");
  if (!install || !exportButton) throw new Error(`missing skill buttons: ${buttons.map((b) => b.textContent).join(" | ")}`);
  return { install, export: exportButton };
}

function noticeText(): string {
  return document.querySelector(".decision-center__notice")?.textContent ?? "";
}

function errorText(): string {
  return document.querySelector(".decision-center__error")?.textContent ?? "";
}

installDom();

// The export button sits directly under the install button, same style.
{
  renderCenter();
  await flush(); // let DecisionState() resolve so the settings panel renders
  const { install, export: exportButton } = centerButtons();
  const afterInstall = (install.compareDocumentPosition(exportButton) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
  assert.equal(afterInstall, true, "export button renders after the install button");
  assert.equal(install.parentElement, exportButton.parentElement, "skill buttons share one action group");
  assert.equal(install.parentElement?.classList.contains("decision-settings__skill-actions"), true, "skill action group keeps the export button below install");
  assert.equal(exportButton.className.includes("btn--secondary"), true, "export button reuses the secondary button style");
  cleanup();
}

// Success shows the exported path through the notice entry.
{
  setMockDecisionSkillExport(async () => ({ exported: true, canceled: false, path: "C:\\Downloads\\workground2-owner-skills.zip" }));
  renderCenter();
  await flush();
  const { export: exportButton } = centerButtons();
  act(() => exportButton.click());
  await flush();
  assert.ok(noticeText().includes("workground2-owner-skills.zip"), `success notice = ${noticeText()}`);
  assert.equal(errorText(), "", "no error on success");
  cleanup();
}

// Cancel is a normal outcome: no success notice, no error.
{
  setMockDecisionSkillExport(async () => ({ exported: false, canceled: true }));
  renderCenter();
  await flush();
  const { export: exportButton } = centerButtons();
  act(() => exportButton.click());
  await flush();
  assert.equal(noticeText(), "", "cancel shows no success notice");
  assert.equal(errorText(), "", "cancel shows no error");
  cleanup();
}

// Failure surfaces through the existing error entry and stays retryable.
{
  setMockDecisionSkillExport(async () => { throw new Error("disk full"); });
  renderCenter();
  await flush();
  const { export: exportButton } = centerButtons();
  act(() => exportButton.click());
  await flush();
  assert.ok(errorText().includes("disk full"), `failure error = ${errorText()}`);
  assert.equal(noticeText(), "", "failure shows no success notice");
  cleanup();
}

// Re-entry is blocked while an export is in flight.
{
  let calls = 0;
  let resolveExport!: (result: DecisionSkillExportResult) => void;
  setMockDecisionSkillExport(() => {
    calls += 1;
    return new Promise<DecisionSkillExportResult>((resolve) => { resolveExport = resolve; });
  });
  renderCenter();
  await flush();
  const { export: exportButton } = centerButtons();
  act(() => exportButton.click());
  await flush();
  assert.equal(exportButton.disabled, true, "button disabled while exporting");
  act(() => exportButton.click()); // disabled buttons ignore clicks
  assert.equal(calls, 1, "double click triggers a single export");
  await act(async () => { resolveExport({ exported: true, canceled: false, path: "C:\\tmp\\workground2-owner-skills.zip" }); });
  await flush();
  assert.ok(noticeText().includes("已导出"), `late resolve notice = ${noticeText()}`);
  assert.equal(exportButton.disabled, false, "button re-enabled after export finishes");
  cleanup();
}

console.log("decision skill export contract passed");
