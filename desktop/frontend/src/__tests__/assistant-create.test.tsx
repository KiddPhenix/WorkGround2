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

const reactProps = <T,>(node: Element): T => {
  const key = Object.keys(node).find((candidate) => candidate.startsWith("__reactProps$"));
  if (!key) throw new Error("missing React props");
  return (node as unknown as Record<string, T>)[key];
};

const setValue = async (input: HTMLInputElement, value: string) => {
  const setter = Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")?.set;
  await act(async () => {
    setter?.call(input, value);
    reactProps<{ onChange: (event: { target: HTMLInputElement }) => void }>(input).onChange({ target: input });
  });
};

const flush = () => act(async () => { await new Promise<void>((resolve) => setTimeout(resolve, 0)); });

await act(async () => {
  root.render(<LocaleProvider><ToastProvider><CreateAssistantDialog onClose={() => undefined} onCreated={() => undefined} /></ToastProvider></LocaleProvider>);
});
ok(clickText("代码项目") !== undefined && clickText("通用") !== undefined, "create dialog shows code and general templates");
ok(clickText("推广") !== undefined, "create dialog shows the promotion phase-4 preview");
const promo = clickText("推广");
ok(promo?.disabled === true, "promotion template is not selectable");

// Select code template → learn-first initial task + permission summary + confirmation gate.
await act(async () => { clickText("代码项目")?.click(); });
const learnFirst = host.querySelector(".assistant-create__learn input") as HTMLInputElement | null;
ok(learnFirst?.checked === true && (host.textContent?.includes("先学习一下再干") ?? false), "learn-first is selected as the initial task by default");
ok(host.textContent?.includes("权限摘要") ?? false, "code template shows a permission summary");
ok(host.textContent?.includes("自动允许") ?? false, "code template discloses auto-allow local writes");
const createButton = clickText("创建助手");
ok(createButton?.disabled === true, "create is blocked until the permission is explicitly confirmed");
const confirmCheck = host.querySelector(".assistant-create__confirm input") as HTMLInputElement | null;
ok(confirmCheck !== null, "code template renders an explicit permission confirmation checkbox");
await act(async () => { confirmCheck?.click(); });
ok(clickText("创建助手")?.disabled === false, "confirming the permission enables creation");

// Workspace field: hand-typed path, native pick, and inline new-folder creation.
const wsInput = () => host.querySelector("#assistant-workspace-input") as HTMLInputElement | null;
const parentInput = () => host.querySelector("#assistant-new-parent") as HTMLInputElement | null;
const nameInput = () => host.querySelector("#assistant-new-name") as HTMLInputElement | null;
const createFolderButton = () => [...host.querySelectorAll(".assistant-create__new-workspace button")].find((item) => item.textContent?.includes("创建")) as HTMLButtonElement | undefined;
ok(host.querySelector(".assistant-create__workspace-actions button[aria-label='选择']") !== null, "workspace field shows a pick button");
ok(host.querySelector(".assistant-create__workspace-actions button[aria-label='新建']") !== null, "workspace field shows a new-folder button");

const pickButton = host.querySelector(".assistant-create__workspace-actions button[aria-label='选择']") as HTMLButtonElement | null;
await act(async () => { pickButton?.click(); });
await flush();
ok(wsInput()?.value === "~/projects/assistant-workspace", "choosing a directory fills the workspace input");

const newButton = host.querySelector(".assistant-create__workspace-actions button[aria-label='新建']") as HTMLButtonElement | null;
await act(async () => { newButton?.click(); });
ok(parentInput() !== null && nameInput() !== null, "new-folder reveals parent and name fields");
ok(createFolderButton()?.disabled === true, "create folder is blocked while name is empty");

await setValue(parentInput()!, "~/projects");
await setValue(nameInput()!, "my-helper");
ok(createFolderButton()?.disabled === false, "create folder enables once parent and name are set");
await act(async () => { createFolderButton()?.click(); });
await flush();
ok(wsInput()?.value === "~/projects/my-helper", "creating a folder fills the workspace input");
ok(parentInput() === null, "successful create closes the inline interaction");

await act(async () => { newButton?.click(); });
await setValue(parentInput()!, "~/projects");
await setValue(nameInput()!, "temp");
const cancelInline = () => [...host.querySelectorAll(".assistant-create__new-workspace button")].find((item) => item.textContent?.includes("取消")) as HTMLButtonElement | undefined;
await act(async () => { cancelInline()?.click(); });
ok(parentInput() === null, "cancel closes the inline interaction");
ok(wsInput()?.value === "~/projects/my-helper", "cancel keeps the workspace value unchanged");

await act(async () => { newButton?.click(); });
await setValue(parentInput()!, "~/projects");
await setValue(nameInput()!, "../evil");
await act(async () => { createFolderButton()?.click(); });
await flush();
ok(wsInput()?.value === "~/projects/my-helper", "failed create keeps the workspace value");
ok(parentInput() !== null, "failed create keeps the inline interaction open for retry");
ok(host.querySelector(".toast__text") !== null, "failed create surfaces an error toast");

// Turning learn-first off keeps the template policy and clears stale confirmation.
await act(async () => { learnFirst?.click(); });
ok(learnFirst?.checked === false, "learn-first can be disabled at creation time");
ok(clickText("创建助手")?.disabled === true, "changing the permission-bearing option clears stale confirmation");

// Select general template → requires a routine; learn-first elevates only the
// disclosed network/local permissions and therefore also requires confirmation.
await act(async () => { clickText("选择模板")?.click(); });
await act(async () => { clickText("通用")?.click(); });
ok(clickText("创建助手")?.disabled === true, "general template is blocked until a routine is filled");
ok(host.textContent?.includes("例行任务名称") ?? false, "general template exposes a routine form");
ok((host.querySelector(".assistant-create__learn input") as HTMLInputElement | null)?.checked === true, "general template also offers learn-first by default");

await act(async () => { root.unmount(); });
console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed) process.exit(1);
