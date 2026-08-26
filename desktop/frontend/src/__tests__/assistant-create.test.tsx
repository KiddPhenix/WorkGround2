import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { CreateAssistantDialog } from "../custom/features/assistant/AssistantWorkspace";
import { setMockListProjectTree, setMockPickAssistantWorkspace } from "../lib/bridge";
import type { ProjectNode } from "../lib/types";
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
ok(clickText("推广") !== undefined, "create dialog shows the phase-4 promotion template");
const promo = clickText("推广");
ok(promo?.disabled === false, "promotion template is selectable");

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

// Workspace field: manual path, registered-workspace chooser, native picker.
const wsInput = () => host.querySelector("#assistant-workspace-input") as HTMLInputElement | null;
const chooseButton = () => host.querySelector(".assistant-create__workspace-actions button[aria-label='选择已有工作区']") as HTMLButtonElement | null;
const newButton = () => host.querySelector(".assistant-create__workspace-actions button[aria-label='从文件夹新建']") as HTMLButtonElement | null;
const chooser = () => host.querySelector(".assistant-create__workspace-chooser");
const closeChooser = () => (chooser()?.querySelector("header button") as HTMLButtonElement | null)?.click();
const choiceRows = () => [...host.querySelectorAll(".assistant-create__workspace-choice")] as HTMLButtonElement[];
ok(chooseButton() !== null && newButton() !== null, "workspace field shows clearly labeled choose-existing and create-from-folder buttons");
ok(host.querySelector("#assistant-new-parent") === null && host.querySelector("#assistant-new-name") === null && host.querySelector(".assistant-create__new-workspace") === null, "the parent/name new-folder form is removed");

// Manual typing keeps working.
await setValue(wsInput()!, "D:\\Manual\\Path");
ok(wsInput()?.value === "D:\\Manual\\Path", "workspace input stays manually editable");

// "选择已有工作区" lists registered workspaces from ListProjectTree and never
// opens the native picker.
let pickerCalls = 0;
setMockPickAssistantWorkspace(async () => { pickerCalls += 1; return "~/should-not-be-used"; });
await act(async () => { chooseButton()?.click(); });
await flush();
ok(chooser() !== null, "choose-existing opens the registered-workspace list");
ok(pickerCalls === 0, "opening the registered list never calls the native picker");
ok(choiceRows().some((row) => row.textContent?.includes("joyquant-db")), "list shows a registered workspace name");
ok(choiceRows().some((row) => row.textContent?.includes("~/projects/joyquant-sys")), "list shows the registered workspace path");
const sysRow = choiceRows().find((row) => row.textContent?.includes("~/projects/joyquant-sys"));
await act(async () => { sysRow?.click(); });
await flush();
ok(wsInput()?.value === "~/projects/joyquant-sys", "picking a registered workspace fills the input with its root");
ok(chooser() === null, "choosing closes the registered list");
ok(pickerCalls === 0, "the registered choice still never calls the native picker");

// Empty registered list explains itself and keeps the current value.
setMockListProjectTree(async () => []);
await act(async () => { chooseButton()?.click(); });
await flush();
ok(host.textContent?.includes("还没有已登记的工作区") ?? false, "empty registered list explains the empty state");
await act(async () => { closeChooser(); });
ok(wsInput()?.value === "~/projects/joyquant-sys", "closing the empty list keeps the workspace value");

// List failure surfaces inline and retry recovers without a picker call.
let treeCalls = 0;
setMockListProjectTree(async () => {
  treeCalls += 1;
  if (treeCalls === 1) throw new Error("tree unavailable");
  return [{ key: "project_~/fresh", kind: "project", label: "fresh-project", root: "~/fresh" }];
});
await act(async () => { chooseButton()?.click(); });
await flush();
ok(chooser()?.querySelector("[role='alert']") !== null, "workspace list failure surfaces an inline error");
const retryListButton = [...(chooser()?.querySelectorAll("button") ?? [])].find((item) => item.textContent?.includes("重新读取")) as HTMLButtonElement | undefined;
await act(async () => { retryListButton?.click(); });
await flush();
ok(choiceRows().some((row) => row.textContent?.includes("fresh-project")), "retrying the failed list recovers");
await act(async () => { closeChooser(); });
ok(pickerCalls === 0, "list retry never calls the native picker");
setMockPickAssistantWorkspace(null);
setMockListProjectTree(null);

// A late list response must not reopen a closed chooser nor leak into a
// subsequent load.
let resolveLate: ((tree: ProjectNode[]) => void) | null = null;
setMockListProjectTree(() => new Promise<ProjectNode[]>((resolve) => { resolveLate = resolve; }));
await act(async () => { chooseButton()?.click(); });
ok(chooser() !== null && chooser()?.getAttribute("aria-busy") === "true", "chooser shows loading while the list is pending");
await act(async () => { closeChooser(); });
await act(async () => { resolveLate!([{ key: "project_late", kind: "project", label: "late-project", root: "~/late" }]); });
await flush();
ok(chooser() === null, "a late list response cannot reopen a closed chooser");
setMockListProjectTree(null);
await act(async () => { chooseButton()?.click(); });
await flush();
ok(choiceRows().every((row) => !row.textContent?.includes("late-project")), "reopened chooser ignores the stale late response");
ok(choiceRows().some((row) => row.textContent?.includes("joyquant-db")), "reopened chooser loads the current registered workspaces");
await act(async () => { closeChooser(); });

// Operations are mutually exclusive: while the chooser is open the folder
// picker button is disabled.
setMockListProjectTree(() => new Promise<ProjectNode[]>(() => undefined));
ok(newButton()?.disabled === false, "folder picker button starts enabled");
await act(async () => { chooseButton()?.click(); });
ok(newButton()?.disabled === true, "folder picker button is disabled while the chooser is open");
await act(async () => { closeChooser(); });
setMockListProjectTree(null);

// "从文件夹新建" opens the native picker; a picked folder fills the input.
await setValue(wsInput()!, "");
setMockPickAssistantWorkspace(null);
await act(async () => { newButton()?.click(); });
await flush();
ok(wsInput()?.value === "~/projects/assistant-workspace", "creating from the folder picker fills the workspace input");

// Canceled picker (empty result) keeps the current value.
setMockPickAssistantWorkspace(async () => "");
await act(async () => { newButton()?.click(); });
await flush();
ok(wsInput()?.value === "~/projects/assistant-workspace", "canceling the folder picker keeps the current workspace value");

// Failed picker keeps the value, shows a toast, and can be retried.
let pickCalls = 0;
setMockPickAssistantWorkspace(async () => {
  pickCalls += 1;
  if (pickCalls === 1) throw new Error("picker unavailable");
  return "~/retried-folder";
});
await act(async () => { newButton()?.click(); });
await flush();
ok(wsInput()?.value === "~/projects/assistant-workspace", "failed folder picker keeps the workspace value");
ok(host.querySelector(".toast__text") !== null, "failed folder picker surfaces an error toast");
await act(async () => { newButton()?.click(); });
await flush();
ok(wsInput()?.value === "~/retried-folder", "folder picker can be retried after a failure");
setMockPickAssistantWorkspace(null);

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
