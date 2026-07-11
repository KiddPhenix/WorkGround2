// Run: npm.cmd exec -- tsx src/__tests__/artifact-shelf-scale.test.tsx

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { ArtifactShelf, filterGroup } from "../components/desktop-ui/ArtifactShelf";
import type { ArtifactRecord } from "../store/artifacts";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1;
  else failed += 1;
}
function eq(actual: unknown, expected: unknown, label: string) {
  ok(actual === expected, `${label}${actual === expected ? "" : `: expected ${String(expected)}, got ${String(actual)}`}`);
}
function hasText(root: Element | null, text: string) {
  return root?.textContent?.includes(text) ?? false;
}

const dom = new JSDOM('<!doctype html><html><head></head><body><div id="root"></div></body></html>', {
  pretendToBeVisual: true,
  url: "http://localhost/",
});
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.Element = dom.window.Element;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
globalThis.ResizeObserver = class implements ResizeObserver {
  constructor(private callback: ResizeObserverCallback) {}
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;
window.matchMedia = () => ({
  matches: false,
  media: "",
  onchange: null,
  addListener: () => {},
  removeListener: () => {},
  addEventListener: () => {},
  removeEventListener: () => {},
  dispatchEvent: () => false,
});
const style = document.createElement("style");
style.textContent = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "../styles.css"), "utf8");
document.head.appendChild(style);

const rootElement = document.getElementById("root")!;
let root: Root | null = null;
function render(ui: React.ReactElement) {
  root ??= createRoot(rootElement);
  act(() => root!.render(ui));
  return rootElement;
}
function cleanup() {
  if (root) act(() => root!.unmount());
  root = null;
}
let artifactSequence = 0;
function artifact(overrides: Partial<ArtifactRecord> = {}): ArtifactRecord {
  artifactSequence += 1;
  const id = overrides.artifactId ?? `artifact-${artifactSequence}`;
  return {
    artifactId: id,
    name: overrides.name ?? id,
    type: overrides.type ?? "binary",
    status: overrides.status ?? "available",
    sessionId: "session-1",
    relativePath: overrides.relativePath,
    lastVerifiedAt: overrides.lastVerifiedAt,
  };
}
function changeInput(input: HTMLInputElement, value: string) {
  const previous = input.value;
  Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set?.call(input, value);
  (input as HTMLInputElement & { _valueTracker?: { setValue: (next: string) => void } })._valueTracker?.setValue(previous);
  input.dispatchEvent(new window.Event("input", { bubbles: true }));
}

process.stdout.write("\nartifact shelf scale\n\n");

// The shelf stays compact: recent active artifacts only, fixed controls, history hidden.
{
  const items = [
    ...Array.from({ length: 8 }, (_, index) => artifact({
      artifactId: `active-${index}`,
      name: `active-${index}.exe`,
      lastVerifiedAt: index,
    })),
    artifact({ artifactId: "missing", name: "missing.zip", status: "missing" }),
  ];
  const container = render(<ArtifactShelf artifacts={items} />);
  const visible = [...container.querySelectorAll(".artifact-item__name")].map((node) => node.textContent);
  eq(visible.length, 6, "main shelf renders at most six items");
  eq(visible[0], "active-7.exe", "main shelf orders newest first");
  ok(!hasText(container, "missing.zip"), "main shelf keeps historical artifacts out");
  ok(hasText(container, "产物 9"), "count includes current and historical artifacts");
  ok(container.querySelector(".artifact-shelf__recent") !== null, "recent lane is isolated from fixed controls");
  const all = container.querySelector(".artifact-shelf__all-btn");
  eq(all?.getAttribute("aria-expanded"), "false", "all trigger exposes collapsed state");
  eq(container.querySelector(".artifact-item__name")?.getAttribute("title"), "active-7.exe", "truncated names expose full text");
  cleanup();
}

// The full list is portaled, grouped, actionable, and dismissible.
{
  let opened = "";
  let revalidated = "";
  let regenerated = "";
  const container = render(
    <ArtifactShelf
      artifacts={[
        artifact({ artifactId: "ready", name: "ready.exe", relativePath: "bin/ready.exe" }),
        artifact({ artifactId: "stale", name: "old.zip", status: "stale", relativePath: "dist/old.zip" }),
      ]}
      onOpen={(id) => { opened = id; }}
      onRevalidate={(id) => { revalidated = id; }}
      onRegenerate={(id) => { regenerated = id; }}
    />,
  );
  const trigger = container.querySelector(".artifact-shelf__all-btn") as HTMLButtonElement;
  act(() => trigger.click());
  const popover = document.body.querySelector(".artifact-popover")!;
  ok(popover !== null && popover.querySelector('[role="dialog"]') !== null, "all trigger opens a portaled dialog");
  ok(hasText(popover, "可用 / 进行中") && hasText(popover, "历史"), "full list separates current and history");
  ok(hasText(popover, "bin/ready.exe") && hasText(popover, "dist/old.zip"), "full list shows relative paths");
  act(() => (popover.querySelector(".artifact-popover__item--available") as HTMLButtonElement).click());
  eq(opened, "ready", "available artifact keeps its open action");

  act(() => trigger.click());
  const actions = [...document.body.querySelectorAll(".artifact-popover__item-actions button")] as HTMLButtonElement[];
  act(() => { actions[0]?.click(); actions[1]?.click(); });
  eq(revalidated, "stale", "stale artifact keeps revalidate action");
  eq(regenerated, "stale", "stale artifact keeps regenerate action");
  act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  ok(document.body.querySelector(".artifact-popover")?.getAttribute("aria-hidden") === "true", "Escape closes the full list");
  cleanup();
}

// Search and type filters appear only after the threshold and update real DOM results.
{
  const twelve = Array.from({ length: 12 }, (_, index) => artifact({ name: `small-${index}.png`, type: "image" }));
  const small = render(<ArtifactShelf artifacts={twelve} />);
  act(() => (small.querySelector(".artifact-shelf__all-btn") as HTMLButtonElement).click());
  ok(document.body.querySelector(".artifact-popover__filters") === null, "twelve artifacts keep the full list minimal");
  cleanup();

  const thirteen = [
    artifact({ name: "debug.bat", type: "script", relativePath: "scripts/debug.bat" }),
    artifact({ name: "release.exe", type: "binary", relativePath: "bin/release.exe" }),
    ...Array.from({ length: 11 }, (_, index) => artifact({ name: `image-${index}.png`, type: "image" })),
  ];
  const large = render(<ArtifactShelf artifacts={thirteen} />);
  act(() => (large.querySelector(".artifact-shelf__all-btn") as HTMLButtonElement).click());
  const input = document.body.querySelector(".artifact-popover__search-input") as HTMLInputElement;
  const select = document.body.querySelector(".artifact-popover__type-select") as HTMLSelectElement;
  ok(input !== null && select !== null, "more than twelve artifacts enable search and type filter");
  const optionValues = [...select.options].map((option) => option.value);
  ok(optionValues.includes("binary") && optionValues.includes("script") && !optionValues.includes("archive"), "type filter contains only present types");

  act(() => changeInput(input, "debug"));
  const popover = document.body.querySelector(".artifact-popover")!;
  ok(hasText(popover, "debug.bat") && !hasText(popover, "release.exe"), "search filters rendered artifacts");
  act(() => {
    changeInput(input, "");
    select.value = "binary";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  ok(hasText(popover, "release.exe") && !hasText(popover, "debug.bat"), "type selection filters rendered artifacts");
  act(() => changeInput(input, "does-not-exist"));
  ok(hasText(popover, "没有匹配的产物"), "empty filters show an explicit result");
  cleanup();
}

// Pure matcher covers the fields used by the input without coupling tests to markup.
{
  const items = [
    artifact({ name: "app.exe", type: "binary", relativePath: "bin/app.exe" }),
    artifact({ name: "start.bat", type: "script", relativePath: "scripts/start.bat" }),
  ];
  eq(filterGroup(items, "scripts", "")[0]?.name, "start.bat", "search matches relative path");
  eq(filterGroup(items, "", "binary")[0]?.name, "app.exe", "type filter matches exact type");
}

process.stdout.write(`\n${passed + failed} tests · ${passed} passed · ${failed} failed\n`);
if (failed > 0) process.exit(1);
