import { JSDOM } from "jsdom";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { AssistantSidebarEntry, AssistantWorkspace, AttentionInbox, OverviewEditor } from "../custom/features/assistant/AssistantWorkspace";
import { assistantGet } from "../custom/features/assistant/assistant.bridge";
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

// ── Handoff dock: a real top dock outside and before the scrolling timeline ──
const scrollBox = host.querySelector(".assistant-workspace__scroll");
const handoffZone = host.querySelector(".assistant-handoff-zone");
const handoffInput = host.querySelector("#assistant-handoff-input") as HTMLTextAreaElement | null;
const dayHeading = scrollBox?.querySelector(".assistant-day") ?? null;
const timelineBox = scrollBox?.querySelector(".assistant-timeline") ?? null;
ok(host.querySelectorAll("#assistant-handoff-input").length === 1, "handoff input appears exactly once in the DOM");
ok(handoffInput?.tagName === "TEXTAREA", "handoff uses a multiline textarea instead of a single-line input");
ok(handoffZone !== null && handoffInput !== null && handoffZone === handoffInput.closest(".assistant-handoff-zone"), "handoff input lives inside the top dock card");
ok(handoffZone !== null && scrollBox !== null && !scrollBox.contains(handoffZone), "handoff dock is outside the scrolling timeline");
const precedes = (a: Element | null, b: Element | null) => Boolean(a && b && (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0);
ok(precedes(handoffZone, scrollBox), "handoff dock precedes the scrolling timeline in the workspace layout");
ok(precedes(handoffInput, dayHeading), "handoff input sits before the date heading");
ok(precedes(handoffInput, timelineBox), "handoff input sits before the timeline");
ok(host.querySelector(".assistant-handoff__hint")?.textContent === "Enter 发送", "handoff dock explains the active Enter send shortcut");

// ── Handoff keyboard: reuses the configured Enter / Ctrl+Enter rule ──
const reactProps = <T,>(node: Element): T => {
  const key = Object.keys(node).find((candidate) => candidate.startsWith("__reactProps$"));
  if (!key) throw new Error("missing React props");
  return (node as unknown as Record<string, T>)[key];
};
const setHandoffValue = (value: string) => {
  const setter = Object.getOwnPropertyDescriptor(dom.window.HTMLTextAreaElement.prototype, "value")?.set;
  act(() => {
    setter?.call(handoffInput, value);
    reactProps<{ onChange: (event: { target: HTMLTextAreaElement }) => void }>(handoffInput!).onChange({ target: handoffInput! });
  });
};
const pressHandoffKey = (key: string, init: { shiftKey?: boolean; ctrlKey?: boolean; metaKey?: boolean; altKey?: boolean } = {}) => {
  let prevented = false;
  act(() => {
    reactProps<{ onKeyDown: (event: { key: string; shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; altKey: boolean; preventDefault: () => void }) => void }>(handoffInput!).onKeyDown({
      key,
      shiftKey: Boolean(init.shiftKey),
      ctrlKey: Boolean(init.ctrlKey),
      metaKey: Boolean(init.metaKey),
      altKey: Boolean(init.altKey),
      preventDefault: () => { prevented = true; },
    });
  });
  return prevented;
};

ok(pressHandoffKey("Enter", { shiftKey: true }) === false, "Shift+Enter inserts a newline in Enter mode");
act(() => { reactProps<{ onCompositionStart: () => void }>(handoffInput!).onCompositionStart(); });
ok(pressHandoffKey("Enter") === false, "IME composing Enter never sends");
act(() => { reactProps<{ onCompositionEnd: () => void }>(handoffInput!).onCompositionEnd(); });
ok(pressHandoffKey("Enter") === true, "Enter sends in Enter mode");

await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantWorkspace composerSubmitKey="ctrl_enter" /></ToastProvider></LocaleProvider>);
  await new Promise((resolve) => setTimeout(resolve, 0));
});
ok(host.querySelector(".assistant-handoff__hint")?.textContent === "Ctrl+Enter 发送", "handoff dock follows the configured Ctrl+Enter shortcut");
ok(pressHandoffKey("Enter") === false, "plain Enter inserts a newline in Ctrl+Enter mode");
ok(pressHandoffKey("Enter", { altKey: true }) === false, "Alt+Enter never sends");
ok(pressHandoffKey("Enter", { metaKey: true }) === true, "Meta+Enter sends in Ctrl+Enter mode on macOS");
ok(pressHandoffKey("Enter", { ctrlKey: true }) === true, "Ctrl+Enter sends in Ctrl+Enter mode");

// ── Run heading stays short; summary renders as Markdown ──────────────
const runEvents = [...host.querySelectorAll(".assistant-event--run")];
ok(runEvents.length >= 2, "timeline shows multiple run events");
ok(
  runEvents.every((event) => {
    const heading = event.querySelector("h2")?.textContent ?? "";
    return !heading.includes("测试都通过了") && !heading.includes("构建脚本前置检查");
  }),
  "run h2 never promotes summary prose into the heading",
);
ok(runEvents.every((event) => event.querySelector("h2")?.textContent === "本次运行已完成"), "completed run h2 uses the short state title");
ok(runEvents.every((event) => event.querySelector(".assistant-event__summary .md") !== null), "every run summary renders through the markdown container");
ok(runEvents.some((event) => event.querySelector(".assistant-event__summary .md")?.textContent?.includes("测试都通过了")), "full run summary is preserved inside the markdown body");
const nonRunEvents = [...host.querySelectorAll(".assistant-event--memory, .assistant-event--next")];
ok(nonRunEvents.length >= 1 && nonRunEvents.every((event) => event.querySelector(".md") === null), "memory and next events stay plain text");

// ── Handoff submit keeps the queued notice and clears the draft ──
setHandoffValue("排查构建失败");
const handoffForm = host.querySelector(".assistant-handoff") as HTMLFormElement | null;
await act(async () => {
  handoffForm?.dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
});
ok(host.querySelector(".assistant-handoff__status")?.textContent?.includes("已经交给助手") ?? false, "successful send surfaces the queued status inside the dock");
ok(handoffInput!.value === "", "successful send clears the draft");

const manage = host.querySelector('button[aria-label="管理助手"]') as HTMLButtonElement | null;
await act(async () => { manage?.click(); });
const workspaceInput = [...host.querySelectorAll("input")].find((input) => input.value === "~/projects/WorkGround2");
ok(workspaceInput !== undefined, "overview exposes the current workspace path");
ok(!workspaceInput?.hasAttribute("readonly"), "workspace path is editable for future runs");
ok(host.textContent?.includes("已经排队的运行保留创建时的工作区") ?? false, "overview explains frozen context for queued runs");

// ── Policy editor ─────────────────────────────────────────────
const policyRows = host.querySelectorAll(".assistant-policy__row");
ok(policyRows.length === 7, "overview exposes all seven policy fields");
ok(
  ["本地写入", "网络", "对外发布", "删除", "付费", "凭据", "私有数据"].every((label) =>
    [...policyRows].some((row) => row.querySelector(":scope > span")?.textContent === label),
  ),
  "policy fields use their Chinese labels",
);
ok(host.textContent?.includes("始终逐次审批") ?? false, "high-risk policy copy clarifies per-action approval");
ok(host.textContent?.includes("排队中和运行中的运行保留旧权限快照") ?? false, "overview explains the frozen policy boundary for queued and running runs");

const networkGroup = [...host.querySelectorAll(".assistant-policy__options")].find((el) => el.getAttribute("aria-label") === "网络") as HTMLElement | undefined;
const approveOption = [...(networkGroup?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.trim() === "逐次审批") as HTMLButtonElement | undefined;
ok(approveOption !== undefined && approveOption.getAttribute("aria-pressed") === "false", "network policy offers a per-action approval option");
await act(async () => { approveOption?.click(); });
const saveButton = [...host.querySelectorAll(".assistant-button")].find((button) => button.textContent?.trim() === "保存") as HTMLButtonElement | undefined;
ok(saveButton !== undefined && !saveButton.disabled, "policy save is available after an edit");
const overviewForm = host.querySelector(".assistant-form") as HTMLFormElement | null;
await act(async () => {
  overviewForm?.dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
});
const saved = await assistantGet("assistant-code-project");
ok(saved.assistant.policy.network === "approve", "saving network=approve persists the new policy value");
ok(
  saved.assistant.policy.local_write === "allow" &&
    saved.assistant.policy.publish === "approve" &&
    saved.assistant.policy.delete === "approve" &&
    saved.assistant.policy.payment === "approve" &&
    saved.assistant.policy.secrets === "approve" &&
    saved.assistant.policy.private_data === "approve",
  "saving the policy keeps every other field intact",
);

// A polling refresh may return a new object for the same authoritative
// Assistant revision. It must not overwrite an unsaved local draft.
const renderOverview = async (snapshot: AssistantSnapshot) => {
  await act(async () => {
    root.render(<LocaleProvider><ToastProvider><OverviewEditor snapshot={snapshot} diagnostics={[]} busy="" act={async (_key, action) => { await action(); return true; }} /></ToastProvider></LocaleProvider>);
  });
};
await renderOverview(saved);
const overviewNetworkGroup = () => [...host.querySelectorAll(".assistant-policy__options")].find((el) => el.getAttribute("aria-label") === "网络") as HTMLElement | undefined;
const networkOption = (label: string) => [...(overviewNetworkGroup()?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.trim() === label) as HTMLButtonElement | undefined;
await act(async () => { networkOption("拒绝")?.click(); });
await renderOverview({ ...saved, assistant: { ...saved.assistant, policy: { ...saved.assistant.policy } } });
ok(networkOption("拒绝")?.getAttribute("aria-pressed") === "true", "same-revision refresh preserves an unsaved policy draft");

const newerSnapshot: AssistantSnapshot = {
  ...saved,
  revision: saved.revision + 1,
  assistant: {
    ...saved.assistant,
    revision: saved.assistant.revision + 1,
    policy: { ...saved.assistant.policy, network: "allow" },
  },
};
await renderOverview(newerSnapshot);
ok(networkOption("自动允许")?.getAttribute("aria-pressed") === "true", "new Assistant revision refreshes the policy draft from authority");

// ── Background CSS contract ───────────────────────────────────
const testDir = dirname(fileURLToPath(import.meta.url));
const stylesSource = readFileSync(resolve(testDir, "../styles.css"), "utf8");
const assistantCssSource = readFileSync(resolve(testDir, "../custom/features/assistant/assistant.css"), "utf8");
ok(
  stylesSource.includes(".app--workbench .assistant-workspace,\n.app--workbench .work-session-host,"),
  "assistant surface joins the shared workbench plate selector",
);
const assistantPlateBlock = stylesSource.match(/\.app--workbench \.assistant-workspace \{[\s\S]*?\n\}/)?.[0] ?? "";
ok(
  assistantPlateBlock.includes("--assistant-bg: transparent;") &&
    assistantPlateBlock.includes("background: rgba(11, 12, 17, 0.58);") &&
    assistantPlateBlock.includes("backdrop-filter: blur(3px);"),
  "assistant surface uses the Session translucent plate (rgba + blur) with a transparent topbar",
);
ok(
  assistantCssSource.includes("background: rgba(19, 21, 25, 0.82);") &&
    assistantCssSource.includes("backdrop-filter: blur(16px) saturate(0.96);"),
  "management drawer uses a readable translucent material",
);
const handoffZoneBlock = assistantCssSource.match(/\.assistant-handoff-zone \{[\s\S]*?\n\}/)?.[0] ?? "";
ok(
  !handoffZoneBlock.includes("position: sticky") &&
    !handoffZoneBlock.includes("position: fixed") &&
    !handoffZoneBlock.includes("position: absolute"),
  "handoff dock is no longer positioned with sticky/fixed/absolute",
);

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
