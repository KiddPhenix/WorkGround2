import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PanelPrimaryAction, ProjectPanel } from "../sidebar/ProjectPanel";
import { PrimaryRail } from "../sidebar/PrimaryRail";
import { useSidebarStore } from "../sidebar/sidebarStore";
import type { SidebarMode, SidebarQueryMode } from "../sidebar/types";

const dom = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.HTMLElement = dom.window.HTMLElement;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed += 1; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed += 1; }
}

async function render(node: React.ReactNode): Promise<{ host: HTMLElement; root: Root }> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  await act(async () => root.render(node));
  return { host, root };
}

async function dispose(host: HTMLElement, root: Root) {
  await act(async () => root.unmount());
  host.remove();
}

process.stdout.write("\nsession sidebar actions\n\n");

{
  const selected: SidebarMode[] = [];
  let unrelatedActions = 0;
  const { host, root } = await render(
    <PrimaryRail
      panelOpen
      activeMode="projects"
      onTogglePanel={() => { unrelatedActions += 1; }}
      onMode={(mode) => selected.push(mode)}
      onNewSession={() => { unrelatedActions += 1; }}
      onOpenSettings={() => { unrelatedActions += 1; }}
    />,
  );
  for (const label of ["项目", "ROOM", "助手"]) {
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)?.click());
  }
  ok(selected.join(",") === "projects,rooms,assistants", "rail project/ROOM/assistant buttons only select their matching panel");
  ok(unrelatedActions === 0, "rail mode selection does not invoke creation or navigation actions");
  await dispose(host, root);
}

for (const [mode, label] of [
  ["projects", "创建项目"],
  ["rooms", "创建 / 加入 ROOM"],
  ["assistants", "创建助手"],
] as Array<[SidebarQueryMode, string]>) {
  let addProject = 0;
  let openMode = 0;
  const { host, root } = await render(
    <PanelPrimaryAction mode={mode} onAddProject={() => { addProject += 1; }} onModeAction={() => { openMode += 1; }} />,
  );
  const button = host.querySelector<HTMLButtonElement>(".session-sidebar__primary-action");
  ok(button?.textContent?.trim() === label, `${mode} panel renders the approved full-row CTA`);
  await act(async () => button?.click());
  const routed = mode === "projects" ? addProject === 1 && openMode === 0 : addProject === 0 && openMode === 1;
  const routeLabel = mode === "assistants"
    ? "assistants CTA emits the existing App-owned creation callback exactly once"
    : `${mode} CTA invokes the existing business callback exactly once`;
  ok(routed, routeLabel);
  await dispose(host, root);
}

{
  Object.defineProperty(window, "go", {
    configurable: true,
    value: { main: { App: { ListSidebarGroups: async () => { throw new Error("room index unavailable"); } } } },
  });
  useSidebarStore.setState({ groupsByMode: {} });
  let roomAction = 0;
  const { host, root } = await render(
    <ProjectPanel
      mode="rooms"
      now={Date.now()}
      unreadBySession={new Map()}
      onOpenSession={() => {}}
      onOpenGroupMenu={() => {}}
      onOpenSessionMenu={() => {}}
      onAddProject={() => {}}
      onModeAction={() => { roomAction += 1; }}
    />,
  );
  await act(async () => { await new Promise((resolve) => globalThis.setTimeout(resolve, 0)); });
  const action = host.querySelector<HTMLButtonElement>(".session-sidebar__primary-action");
  await act(async () => action?.click());
  ok(roomAction === 1, "ROOM primary CTA remains operational when its query fails");
  ok(useSidebarStore.getState().groupsByMode.rooms?.status === "error", "ROOM query failure remains explicit below the persistent CTA");
  await dispose(host, root);
  Reflect.deleteProperty(window, "go");
}

if (failed > 0) {
  process.stderr.write(`\n${passed} passed, ${failed} failed\n`);
  process.exit(1);
}
process.stdout.write(`\n${passed} passed, 0 failed\n`);
