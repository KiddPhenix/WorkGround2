import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { CreateAssistantDialog } from "../custom/features/assistant/AssistantWorkspace";
import { LocaleProvider } from "../lib/i18n";
import { ToastProvider } from "../lib/toast";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

console.log("\nassistant create (templates + permission confirmation)");
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

const clickText = (text: string) => {
  const button = [...host.querySelectorAll("button")].find((item) => item.textContent?.includes(text)) as HTMLButtonElement | undefined;
  return button;
};

await act(async () => {
  root.render(<LocaleProvider><ToastProvider><CreateAssistantDialog onClose={() => undefined} onCreated={() => undefined} /></ToastProvider></LocaleProvider>);
});
ok(clickText("代码项目") !== undefined && clickText("通用") !== undefined, "create dialog shows code and general templates");
ok(clickText("推广") !== undefined, "create dialog shows the promotion phase-4 preview");
const promo = clickText("推广");
ok(promo?.disabled === true, "promotion template is not selectable");

// Select code template → permission summary + confirmation gate.
await act(async () => { clickText("代码项目")?.click(); });
ok(host.textContent?.includes("权限摘要") ?? false, "code template shows a permission summary");
ok(host.textContent?.includes("自动允许") ?? false, "code template discloses auto-allow local writes");
const createButton = clickText("创建助手");
ok(createButton?.disabled === true, "create is blocked until the permission is explicitly confirmed");
const confirmCheck = host.querySelector(".assistant-create__confirm input") as HTMLInputElement | null;
ok(confirmCheck !== null, "code template renders an explicit permission confirmation checkbox");
await act(async () => { confirmCheck?.click(); });
ok(clickText("创建助手")?.disabled === false, "confirming the permission enables creation");

// Select general template → requires a routine, no confirmation gate.
await act(async () => { clickText("选择模板")?.click(); });
await act(async () => { clickText("通用")?.click(); });
ok(clickText("创建助手")?.disabled === true, "general template is blocked until a routine is filled");
ok(host.textContent?.includes("例行任务名称") ?? false, "general template exposes a routine form");

await act(async () => { root.unmount(); });
console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed) process.exit(1);
