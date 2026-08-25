import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { AssistantSidebarEntry, AssistantWorkspace, AttentionInbox } from "../custom/features/assistant/AssistantWorkspace";
import type { AssistantSnapshot } from "../custom/features/assistant/assistant.types";
import { LocaleProvider } from "../lib/i18n";
import { ToastProvider } from "../lib/toast";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

console.log("\nassistant workspace");
const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/?mock=demo" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
Object.defineProperty(dom.window.navigator, "language", { configurable: true, value: "zh-CN" });
globalThis.localStorage = dom.window.localStorage;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Element = dom.window.Element;
globalThis.Node = dom.window.Node;
globalThis.Event = dom.window.Event;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;

const host = document.getElementById("root")!;
const root = createRoot(host);
let selected = false;
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantSidebarEntry active onClick={() => { selected = true; }} /></ToastProvider></LocaleProvider>);
});
const entry = host.querySelector(".assistant-sidebar-entry") as HTMLButtonElement | null;
ok(entry?.getAttribute("aria-current") === "page", "sidebar entry exposes selected navigation state");
act(() => entry?.click());
ok(selected, "sidebar entry is interactive");

await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantWorkspace /></ToastProvider></LocaleProvider>);
  await new Promise((resolve) => setTimeout(resolve, 20));
});
ok(host.querySelector(".assistant-workspace") !== null, "workspace mounts as a full surface");
ok(host.textContent?.includes("代码项目助手") ?? false, "workspace renders the selected assistant");
ok(host.textContent?.includes("让它继续工作") ?? false, "primary repeat action is visible");
ok(host.querySelector("#assistant-handoff-input") !== null, "quick handoff input is keyboard accessible");
ok(host.querySelectorAll(".assistant-event").length >= 2, "timeline renders run and memory events");

const manage = host.querySelector('button[aria-label="管理助手"]') as HTMLButtonElement | null;
await act(async () => { manage?.click(); });
const workspaceInput = [...host.querySelectorAll("input")].find((input) => input.value === "~/projects/WorkGround2");
ok(workspaceInput !== undefined, "overview exposes the current workspace path");
ok(!workspaceInput?.hasAttribute("readonly"), "workspace path is editable for future runs");
ok(host.textContent?.includes("已经排队的运行保留创建时的工作区") ?? false, "overview explains frozen context for queued runs");

const attentionSnapshot: AssistantSnapshot = {
  revision: 1,
  assistant: { id: "assistant-answer", name: "答疑助理", mission: "等待回答", scope: "global", lifecycle: "active", policy: { local_write: "deny", network: "deny", publish: "approve", delete: "approve", payment: "approve", secrets: "approve", private_data: "approve" }, memory_revision: 0, revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" },
  routines: [], memory: { revision: 0, items: [] },
  runs: [{ id: "run-answer", assistant_id: "assistant-answer", request_id: "run-request", trigger: "manual", state: "waiting_attention", attempt: 1, max_attempts: 3, revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" }],
  attention: [{ id: "attention-answer", assistant_id: "assistant-answer", run_id: "run-answer", request_id: "attention-request", action: "answer_required", summary: "需要明确答案", state: "open", revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" }],
  updated_at: "2026-08-17T00:00:00Z",
};
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AttentionInbox snapshot={attentionSnapshot} busy="" act={async () => true} onOverview={() => undefined} /></ToastProvider></LocaleProvider>);
});
const answerBox = host.querySelector(".assistant-attention-item__answer textarea") as HTMLTextAreaElement | null;
const answerButton = [...host.querySelectorAll("button")].find((button) => button.textContent?.trim() === "回答") as HTMLButtonElement | undefined;
const rejectButton = [...host.querySelectorAll("button")].find((button) => button.textContent?.trim() === "拒绝") as HTMLButtonElement | undefined;
ok(answerBox !== null && Boolean(answerButton?.disabled), "answer-required attention exposes an editable answer and blocks empty approval");
ok(rejectButton !== undefined && !rejectButton.disabled, "answer-required attention can be rejected without entering an answer");

await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AttentionInbox snapshot={{ ...attentionSnapshot, attention: [{ ...attentionSnapshot.attention[0], state: "approved" }] }} busy="" act={async () => true} onOverview={() => undefined} /></ToastProvider></LocaleProvider>);
});
ok(host.textContent?.includes("继续运行") ?? false, "approved attention remains visible with a continue action while its run waits");

const outcomeAttention = { ...attentionSnapshot.attention[0], id: "attention-outcome", action: "verify_run_outcome", state: "open" as const };
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AttentionInbox snapshot={{ ...attentionSnapshot, attention: [outcomeAttention] }} busy="" act={async () => true} onOverview={() => undefined} /></ToastProvider></LocaleProvider>);
});
ok(["确认重试", "标记成功", "标记失败"].every((label) => host.textContent?.includes(label)), "unknown run outcome exposes retry, succeeded, and failed decisions");
ok(!host.textContent?.includes("批准") && !host.textContent?.includes("拒绝"), "outcome verification does not use the ordinary approval actions");

await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AttentionInbox snapshot={{ ...attentionSnapshot, attention: [{ ...outcomeAttention, state: "approved", resolution: "mark_succeeded" }] }} busy="" act={async () => true} onOverview={() => undefined} /></ToastProvider></LocaleProvider>);
});
ok(host.textContent?.includes("没有需要你处理的事项") ?? false, "terminal outcome decisions automatically leave the inbox");

await act(async () => { root.unmount(); });
console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed) process.exit(1);
