import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { HeartbeatConversionAction } from "../custom/features/heartbeat/HeartbeatPanel";
import type { HeartbeatConversionStatus } from "../custom/features/heartbeat/heartbeat.types";
import { LocaleProvider } from "../lib/i18n";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

console.log("\nheartbeat conversion");
const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/?mock=demo" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
Object.defineProperty(dom.window.navigator, "language", { configurable: true, value: "zh-CN" });
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Element = dom.window.Element;
globalThis.Node = dom.window.Node;
globalThis.Event = dom.window.Event;

const host = document.getElementById("root")!;
const root = createRoot(host);

let convertClicks = 0;
let openClicks = 0;
let openedAssistantID = "";
const render = async (props: {
  conversion?: HeartbeatConversionStatus;
  converting?: boolean;
  error?: string;
  onOpenAssistant?: (assistantId: string) => void;
}) => {
  await act(async () => {
    root.render(<LocaleProvider><HeartbeatConversionAction
      conversion={props.conversion}
      converting={props.converting ?? false}
      error={props.error}
      onConvert={() => { convertClicks += 1; }}
      onOpenAssistant={props.onOpenAssistant ?? ((assistantId) => { openClicks += 1; openedAssistantID = assistantId; })}
    /></LocaleProvider>);
  });
};

const status = (partial: Partial<HeartbeatConversionStatus>): HeartbeatConversionStatus => ({ taskId: "task-1", state: "convertible", ...partial });

// 1. convertible + yolo risk
await render({ conversion: status({ state: "convertible", approvalMode: "yolo" }) });
ok(host.textContent?.includes("转换为助手") ?? false, "convertible task exposes a convert action");
ok(host.textContent?.includes("YOLO") ?? false, "empty/yolo source surfaces an explicit risk warning");
const convertButton = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("转换为助手")) as HTMLButtonElement | undefined;
await act(async () => { convertButton?.click(); });
ok(convertClicks === 1, "convert action is interactive");

// 1b. auto also surfaces the same risk (local_write/network become allow)
await render({ conversion: status({ state: "convertible", approvalMode: "auto" }) });
ok(host.textContent?.includes("YOLO") ?? false, "auto source surfaces the same local-write/network allow risk");

// 2. converted → open assistant with its id
await render({ conversion: status({ state: "converted", assistantId: "assistant-abc", assistantName: "代码项目助理" }) });
ok(host.textContent?.includes("已转换为助手") ?? false, "converted task shows completion");
ok(host.textContent?.includes("代码项目助理") ?? false, "converted task shows the assistant name");
const openButton = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("打开助手工作区")) as HTMLButtonElement | undefined;
await act(async () => { openButton?.click(); });
ok(openClicks === 1, "converted task can open the assistant workspace");
ok(openedAssistantID === "assistant-abc", "opening the assistant passes its id for target selection");

// 3. conflict / unmappable → reason, no convert action
await render({ conversion: status({ state: "conflict", reason: "任务内容已变更" }) });
ok(host.textContent?.includes("无法复用已转换的助手") ?? false, "conflict is surfaced explicitly");
ok(![...host.querySelectorAll("button")].some((button) => button.textContent?.includes("转换为助手")), "conflict does not offer a duplicate convert action");

await render({ conversion: status({ state: "unmappable", reason: "无法无损转换" }) });
ok(host.textContent?.includes("无法无损转换") ?? false, "unmappable schedule is surfaced explicitly");

// 4. error → retry
convertClicks = 0;
await render({ error: "提交失败" });
ok(host.textContent?.includes("提交失败") ?? false, "write failure is visible, not swallowed");
const retryButton = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("重试转换")) as HTMLButtonElement | undefined;
await act(async () => { retryButton?.click(); });
ok(convertClicks === 1, "failed conversion exposes a retry action");

await act(async () => { root.unmount(); });
console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed) process.exit(1);
