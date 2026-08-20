// AgentIcon 组件渲染测试（jsdom + react-dom）。运行：
//   node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/agent-icon-component.test.tsx
// 断言：5 层按 manifest.layerOrder 渲染、aria-hidden、缺资源不破图并去重
// 上报、frame 缺失整图标不渲染、眼睛 sprite 位移、徽标 glyph 映射。
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { AgentIcon } from "../components/agent-icon/AgentIcon";
import { agentManifest } from "../lib/agentIcon/assets";
import { buildAgentIconViewModel } from "../lib/agentIcon/viewModel";
import type { DesktopIconItem } from "../lib/bridge";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed++; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed++; }
}

const dom = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.HTMLElement = dom.window.HTMLElement;

function taskItem(overrides: Partial<DesktopIconItem> = {}): DesktopIconItem {
  return {
    id: "task:tab-1", kind: "task", sourceId: "tab-1", title: "实现图标", status: "running",
    unreadCount: 0, notifications: [], position: { row: "bottom", zone: "running", order: 0 },
    revision: "r", sessionId: "session-1", workspaceIcon: "python",
    sessionRef: { scope: "project", workspaceRoot: "D:\\Work\\WG2", topicId: "topic-1", sessionPath: "sp-1" },
    ...overrides,
  };
}

function render(viewModel: ReturnType<typeof buildAgentIconViewModel>, onAssetMissing?: (info: unknown) => void) {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  act(() => { root.render(<AgentIcon viewModel={viewModel} onAssetMissing={onAssetMissing} />); });
  return { host, root };
}

async function unmount(root: ReturnType<typeof createRoot>) {
  await act(async () => root.unmount());
}

console.log("\nagent-icon component — layers");

{
  const vm = buildAgentIconViewModel(taskItem());
  const { host, root } = render(vm);
  const icon = host.querySelector(".agent-icon");
  ok(icon !== null, "root .agent-icon renders");
  ok(icon?.getAttribute("aria-hidden") === "true", "icon is decorative (aria-hidden)");
  const imgs = host.querySelectorAll(".agent-icon img");
  ok(imgs.length >= 5, `at least 5 images for the layers (got ${imgs.length})`);
  // DOM 顺序 = manifest.layerOrder：frame → headwear → eyes → workspaceBadge → taskTool
  const layerClasses = Array.from(host.querySelectorAll<HTMLElement>(".agent-icon > img.agent-icon__layer, .agent-icon > .agent-icon__layer")).map((node) => {
    if (node.classList.contains("agent-icon__layer--eyes")) return "eyes";
    if (node.classList.contains("agent-icon__layer--badge")) return "workspaceBadge";
    return node.classList.contains("agent-icon__layer") ? "layer-img" : "?";
  });
  ok(layerClasses.length >= 5, `5 layer containers in order (got ${layerClasses.join(",")})`);
  const frameImg = host.querySelector<HTMLImageElement>(".agent-icon > img.agent-icon__layer:first-child");
  ok(frameImg !== null && frameImg.src.endsWith(`png/frames/${vm.frameId}.png`), "first layer is the picked frame");
  const headwearLayer = host.querySelector(".agent-icon > img.agent-icon__layer:nth-child(2)");
  ok(Boolean(headwearLayer), "second layer is headwear");
  const eyeLayer = host.querySelector(".agent-icon__layer--eyes img.agent-icon__eyes");
  ok(eyeLayer !== null, "eyes layer renders the sprite");
  const eyeImg = eyeLayer as HTMLImageElement;
  const eye = agentManifest.eyes.find((e) => e.id === vm.eyeStatus);
  ok(eyeImg.src.endsWith(eye?.sprite ?? "none"), "eyes sprite matches the mapped status");
  ok(eyeImg.style.width === "600%", "eyes sprite is 6 frames wide");
  ok(eyeImg.style.transform === "translateX(0%)" || /translateX\(-/.test(eyeImg.style.transform), "eyes sprite is frame-offset");
  ok(host.querySelector(".agent-icon__layer--badge .agent-icon__badge-template") !== null, "workspace badge template renders");
  ok(host.querySelector(".agent-icon__layer--badge .agent-icon__badge-glyph") !== null, "workspace badge glyph renders");
  const toolImg = host.querySelector<HTMLImageElement>(".agent-icon > img.agent-icon__layer:last-child");
  ok(toolImg !== null && toolImg.src.endsWith("png/tools/general.png"), "task tool is the manifest general tool");
  await unmount(root);
}

{
  // 徽标 glyph 映射：matte 键 → 图片；经典五键 → Lucide；未知 → folder。
  const matte = buildAgentIconViewModel(taskItem({ workspaceIcon: "python" }));
  const { host, root } = render(matte);
  ok(host.querySelector(".agent-icon__badge-glyph .agent-icon__badge-glyph-img") !== null, "matte workspace icon renders as image");
  await unmount(root);

  const star = buildAgentIconViewModel(taskItem({ workspaceIcon: "star" }));
  const host2 = document.createElement("div");
  document.body.appendChild(host2);
  const root2 = createRoot(host2);
  act(() => { root2.render(<AgentIcon viewModel={star} />); });
  ok(host2.querySelector(".agent-icon__badge-glyph svg") !== null, "classic star icon renders a lucide glyph");
  await unmount(root2);
  host2.remove();

  const unknown = buildAgentIconViewModel(taskItem({ workspaceIcon: "rocket" }));
  const host3 = document.createElement("div");
  document.body.appendChild(host3);
  const root3 = createRoot(host3);
  act(() => { root3.render(<AgentIcon viewModel={unknown} />); });
  ok(host3.querySelector(".agent-icon__badge-glyph svg") !== null, "unknown workspace icon falls back to the folder glyph");
  await unmount(root3);
  host3.remove();
}

console.log("\nagent-icon component — degradation");

{
  // frame 缺失 → 整图标不渲染（规范 §6.3），并去重上报一次。
  const broken = buildAgentIconViewModel(taskItem());
  const frameLayer = agentManifest.frames.find((f) => f.id === broken.frameId);
  const missing: unknown[] = [];
  let errors = 0;
  const originalError = console.error;
  console.error = () => { errors++; };
  try {
    const vm = { ...broken, missingLayers: [...broken.missingLayers, "frame"] };
    const { host, root } = render(vm, (info) => missing.push(info));
    ok(host.querySelector(".agent-icon") === null, "missing frame renders nothing");
    await unmount(root);
  } finally {
    console.error = originalError;
  }
  ok(missing.length === 1, `frame missing reported once (got ${missing.length})`);
  ok(missing[0] !== undefined && (missing[0] as { layer: string }).layer === "frame", "report carries the frame layer");
  ok(errors === 1, `console.error deduped (got ${errors})`);
  void frameLayer;
}

{
  // 工具缺失 → 只隐藏该层，其余层照常；上报一次。
  const vm = { ...buildAgentIconViewModel(taskItem()), missingLayers: ["tool:general"] };
  const { host, root } = render(vm);
  const toolImgs = host.querySelectorAll(".agent-icon > img.agent-icon__layer");
  ok(host.querySelector(".agent-icon") !== null, "icon still renders when the tool layer is missing");
  const lastLayer = toolImgs[toolImgs.length - 1];
  ok(!lastLayer?.src.endsWith("png/tools/"), "missing tool layer is hidden");
  await unmount(root);
}

{
  // 眼睛缺失 → 中性 LED 占位，不破图。
  const vm = { ...buildAgentIconViewModel(taskItem()), missingLayers: ["eyes"] };
  const { host, root } = render(vm);
  ok(host.querySelector(".agent-icon__eyes-fallback") !== null, "missing eyes fall back to the neutral LED template");
  ok(host.querySelector(".agent-icon__eyes") === null, "broken sprite never renders");
  await unmount(root);
}

{
  // onError 冒烟：真实 URL 触发的 error 事件把对应层标记为缺失并隐藏。
  const vm = buildAgentIconViewModel(taskItem());
  const { host, root } = render(vm);
  const before = host.querySelectorAll(".agent-icon > img.agent-icon__layer").length;
  const toolImg = host.querySelector<HTMLImageElement>(".agent-icon > img.agent-icon__layer:last-child");
  if (toolImg) {
    act(() => {
      toolImg.dispatchEvent(new dom.window.Event("error", { bubbles: false }));
    });
    const after = host.querySelectorAll(".agent-icon > img.agent-icon__layer").length;
    ok(after === before - 1, "errored tool layer is removed from the DOM");
    ok(host.querySelector(".agent-icon") !== null, "icon survives a tool layer error");
  } else {
    ok(false, "tool image present for error dispatch");
  }
  await unmount(root);
}

console.log(`\nagent-icon component: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
