import { JSDOM } from "jsdom";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { AssistantSidebarEntry, AssistantWorkspace, AssistantJobRow, AttentionInbox, DiagnosticsEditor, OverviewEditor, ProposalInbox, RunHistory, assistantDiagnosticWarning, sortedDiagnostics } from "../custom/features/assistant/AssistantWorkspace";
import { assistantCopy } from "../custom/features/assistant/assistant.copy";
import { assistantGet } from "../custom/features/assistant/assistant.bridge";
import type { AssistantDiagnostic, AssistantRun, AssistantRunnerJob, AssistantSnapshot } from "../custom/features/assistant/assistant.types";
import { getMockSessionControlCalls, getMockViewportPublishes, resetMockSessionControlCalls, resetMockViewportPublishes, setMockAssistantManagedSessions, setMockAssistantSessionStatus, setMockAssistantSessionSteerShouldFail, setMockAssistantSupervisorDiagnostic } from "../lib/bridge";
import { LocaleProvider } from "../lib/i18n";
import { ToastProvider } from "../lib/toast";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

console.log("\nassistant workspace");
const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/?mock=assistant-diagnostic" });
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
ok(host.querySelector(".assistant-continue") === null && !(host.textContent?.includes("让它继续工作") ?? false), "main execution view no longer operates legacy Runs (continue button removed)");
ok(host.querySelector(".assistant-proposal-chip")?.textContent?.trim() === "1", "top bar exposes the pending improvement proposal count");
ok(host.querySelector("#assistant-handoff-input") !== null, "quick handoff input is keyboard accessible");
ok(host.querySelector("#assistant-handoff-input")?.getAttribute("placeholder") === "对助手说…", "handoff input prompts a message to the assistant");
ok(host.textContent?.includes("输入会被记录") ?? false, "handoff dock states that the input will be recorded");
ok(host.querySelectorAll(".assistant-event").length >= 2, "timeline renders run and memory events");
const zhCopy = assistantCopy("zh-CN");
ok(
  assistantDiagnosticWarning([{ at: "", category: "runtime", operation: "progress_apply", message: "invalid transition" }], zhCopy) === "上次运行已完成，但计划进度未能更新。",
  "progress diagnostics are not mislabeled as unreadable Assistant data",
);
ok(
  assistantDiagnosticWarning([{ at: "", category: "data", operation: "list", message: "corrupt aggregate" }], zhCopy) === "部分助手数据无法读取，健康助手仍可正常使用。",
  "data diagnostics retain the partial-read warning",
);

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

// ── Diagnostics: the top warning routes to a dedicated diagnostics page ──
// The mock injects one list diagnostic, so the top banner is present; its
// "查看详情" action must activate the diagnostics management tab directly,
// never the overview page or a scroll-to-bottom permission form.
const diagnosticBanner = host.querySelector(".assistant-diagnostic") as HTMLElement | null;
ok(diagnosticBanner !== null, "diagnostic warning banner appears when diagnostics exist");
const viewDetailsButton = [...(diagnosticBanner?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.trim() === "查看详情") as HTMLButtonElement | undefined;
ok(viewDetailsButton !== undefined, "top warning exposes a view-details action");
await act(async () => { viewDetailsButton?.click(); });
const diagnosticsNav = [...host.querySelectorAll(".assistant-manager nav button")].find((button) => button.textContent?.includes("诊断")) as HTMLButtonElement | undefined;
ok(diagnosticsNav !== undefined, "management nav includes the diagnostics page");
ok(diagnosticsNav?.getAttribute("aria-current") === "page", "view details activates the diagnostics tab immediately");
ok(diagnosticsNav?.querySelector(".assistant-nav-count")?.textContent === "1", "diagnostics nav shows the current diagnostic count");
const diagnosticEntry = host.querySelector(".assistant-diagnostic-entry") as HTMLElement | null;
ok(diagnosticEntry !== null, "diagnostics page lists entries on first screen without scrolling to overview");
ok(diagnosticEntry?.textContent?.includes("list") ?? false, "diagnostics entry keeps the raw operation");
ok(diagnosticEntry?.textContent?.includes("一个助手快照损坏") ?? false, "diagnostics entry keeps the full message");
ok(diagnosticEntry?.querySelector(".assistant-diagnostic-entry__category")?.textContent?.trim() === "未知", "diagnostics entry shows a readable category label");
const diagnosticTime = diagnosticEntry?.querySelector("time") as HTMLTimeElement | null;
ok(diagnosticTime !== null && Boolean(diagnosticTime.dateTime) && (diagnosticTime.textContent?.length ?? 0) > 0, "diagnostics entry shows a parseable local time");

// ── Run heading identifies the work; summary renders as Markdown ─────
const runEvents = [...host.querySelectorAll(".assistant-event--run")];
ok(runEvents.length >= 2, "timeline shows multiple run events");
ok(
  runEvents.every((event) => {
    const heading = event.querySelector("h2")?.textContent ?? "";
    return !heading.includes("测试都通过了") && !heading.includes("构建脚本前置检查");
  }),
  "run h2 uses work identity rather than result summary prose",
);
ok(runEvents.every((event) => event.querySelector("h2")?.textContent === "发布准备检查"), "routine run h2 shows the actual routine name");
ok(runEvents.every((event) => event.querySelector(".assistant-event__summary .md") !== null), "every run summary renders through the markdown container");
ok(runEvents.some((event) => event.querySelector(".assistant-event__summary .md")?.textContent?.includes("测试都通过了")), "full run summary is preserved inside the markdown body");
const reportImage = host.querySelector('.assistant-event__summary img[src="https://example.com/assistant-build-report.png"]') as HTMLImageElement | null;
ok(reportImage?.alt === "构建结果预览" && reportImage.getAttribute("loading") === "lazy", "assistant summary displays Markdown images through the preview component");
const evidenceFold = host.querySelector(".assistant-event__summary details.assistant-markdown__fold") as HTMLDetailsElement | null;
ok(evidenceFold?.open === false && evidenceFold.querySelector("summary")?.textContent === "取证证据", "evidence section is collapsed by default");
ok(evidenceFold?.textContent?.includes("CI 日志") ?? false, "collapsed evidence remains available for explicit expansion");
const nonRunEvents = [...host.querySelectorAll(".assistant-event--memory, .assistant-event--next")];
ok(nonRunEvents.length >= 1 && nonRunEvents.every((event) => event.querySelector(".md") === null), "memory and next events stay plain text");

// ── Timeline run state: a bound session opens the coding-agent session ──
let openedSession: { scope: string; workspaceRoot: string; sessionPath: string; assistantID: string; assistantName: string } | null = null;
await act(async () => {
  root.render(
    <LocaleProvider><ToastProvider>
      <AssistantWorkspace onOpenSession={(target) => { openedSession = target; }} />
    </ToastProvider></LocaleProvider>,
  );
  await new Promise((resolve) => setTimeout(resolve, 20));
});
const boundButton = host.querySelector(".assistant-event--run button.assistant-run-state") as HTMLButtonElement | null;
ok(boundButton !== null, "bound run state renders as an openable button");
ok(boundButton?.classList.contains("assistant-run-state--link") === true, "bound run state is marked as a navigation link");
ok(boundButton?.getAttribute("aria-label") === "已完成，打开会话", "bound run state exposes an open-session label");
ok(boundButton?.querySelector("button") === null, "run state button contains no nested button");
await act(async () => { boundButton?.click(); });
ok(openedSession !== null, "clicking the bound run state opens the coding-agent session");
ok(
  openedSession?.scope === "project" &&
    openedSession?.workspaceRoot === "~/projects/WorkGround2" &&
    openedSession?.sessionPath === "/mock/sessions/assistant-scan.jsonl" &&
    openedSession?.assistantID === "assistant-code-project" &&
    openedSession?.assistantName === "代码项目助手",
  "bound run state preserves the owning Assistant identity together with the exact session path",
);
const unboundBadge = [...host.querySelectorAll(".assistant-event--run .assistant-run-state")].find((element) => element.tagName !== "BUTTON");
ok(unboundBadge !== undefined && unboundBadge.tagName === "SPAN", "unbound run keeps a non-interactive badge");

// ── Handoff submit keeps the queued notice and clears the draft ──
setHandoffValue("排查构建失败");
const handoffForm = host.querySelector(".assistant-handoff") as HTMLFormElement | null;
await act(async () => {
  handoffForm?.dispatchEvent(new dom.window.Event("submit", { bubbles: true, cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
});
ok(host.querySelector(".assistant-handoff__status")?.textContent?.includes("已记录并排队") ?? false, "successful send surfaces the recorded-and-queued status inside the dock");
ok(handoffInput!.value === "", "successful send clears the draft");
const submittedSnapshot = await assistantGet("assistant-code-project");
const submittedDispatch = submittedSnapshot.dispatches?.find((dispatch) => dispatch.input === "排查构建失败");
ok(submittedDispatch !== undefined, "direct input is durably visible as a Dispatch after refresh");
ok(!submittedSnapshot.routines.some((routine) => routine.id.startsWith("adhoc-")), "direct input does not create or overwrite an adhoc Routine");
ok(host.querySelector(".assistant-live-reply")?.textContent?.includes("收到，我来处理") ?? false, "accepted input exposes the Dispatcher reply inside the handoff dock");
ok(handoffInput?.disabled === false, "composer unlocks as soon as the durable Dispatch is accepted");
ok(host.querySelector(".assistant-event__prompt")?.textContent?.includes("排查构建失败") ?? false, "timeline keeps the submitted original text separate from the result");

const manage = host.querySelector('button[aria-label="管理助手"]') as HTMLButtonElement | null;
await act(async () => { manage?.click(); });
const historyTab = [...host.querySelectorAll(".assistant-manager nav button")].find((button) => button.textContent?.includes("运行记录")) as HTMLButtonElement | undefined;
await act(async () => { historyTab?.click(); });
ok(submittedSnapshot.dispatches?.some((dispatch) => dispatch.input === "排查构建失败") ?? false, "the durable Dispatch keeps the full direct input traceable independently from run history");
const channelsTab = [...host.querySelectorAll(".assistant-manager nav button")].find((button) => button.textContent?.includes("推广渠道")) as HTMLButtonElement | undefined;
await act(async () => { channelsTab?.click(); });
ok(host.textContent?.includes("还没有推广渠道") ?? false, "channel manager explains the empty Discourse state");
ok(host.querySelector('input[type="password"]') !== null, "channel API key uses a secret input and is not projected from the snapshot");
ok(host.textContent?.includes("按对外发布权限") ?? false, "channel manager explains that outbound behavior follows publishing permission");
const proposalsTab = [...host.querySelectorAll(".assistant-manager nav button")].find((button) => button.textContent?.includes("改进建议")) as HTMLButtonElement | undefined;
await act(async () => { proposalsTab?.click(); });
ok(host.querySelectorAll(".assistant-proposal").length === 1, "proposal manager renders the durable pending proposal");
ok(host.textContent?.includes("把发布准备检查提前到上午") ?? false, "proposal manager shows the evidence-backed summary");
ok(host.textContent?.includes("每天 18:00") && host.textContent?.includes("每天 09:00") || false, "proposal manager compares the schedule before and after values");
ok(host.textContent?.includes("run-scan：18:00 检查后才发现发布说明缺升级提醒") ?? false, "proposal manager keeps the source evidence visible");
const acceptProposal = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("接受并应用")) as HTMLButtonElement | undefined;
await act(async () => { acceptProposal?.click(); await new Promise((resolve) => setTimeout(resolve, 0)); });
const proposalApplied = await assistantGet("assistant-code-project");
ok(proposalApplied.proposals?.[0]?.state === "applied", "accepting a proposal persists its terminal applied state");
ok(proposalApplied.routines[0].schedule.at === "09:00", "accepting a proposal applies the typed target change");
ok(host.textContent?.includes("已应用") && !host.textContent?.includes("接受并应用") || false, "applied proposal moves to decision history and loses action buttons");
const overviewTab = [...host.querySelectorAll(".assistant-manager nav button")].find((button) => button.textContent?.includes("概览")) as HTMLButtonElement | undefined;
await act(async () => { overviewTab?.click(); });
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
const publishGroup = [...host.querySelectorAll(".assistant-policy__options")].find((el) => el.getAttribute("aria-label") === "对外发布") as HTMLElement | undefined;
const publishAllow = [...(publishGroup?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.trim() === "自动允许") as HTMLButtonElement | undefined;
ok(publishAllow !== undefined && !publishAllow.classList.contains("is-always-ask"), "publishing allow is a real automatic permission");

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
const renderOverview = async (snapshot: AssistantSnapshot, onDelete = async () => undefined) => {
  await act(async () => {
    root.render(<LocaleProvider><ToastProvider><OverviewEditor snapshot={snapshot} busy="" act={async (_key, action) => { await action(); return true; }} onDelete={onDelete} /></ToastProvider></LocaleProvider>);
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

// ── Delete Assistant requires an explicit second action ──────
let deleteCalls = 0;
await renderOverview(newerSnapshot, async () => { deleteCalls += 1; });
const deleteButton = [...host.querySelectorAll("button")].find((button) => button.textContent?.trim() === "删除助手") as HTMLButtonElement | undefined;
ok(deleteButton !== undefined, "overview exposes a delete Assistant action");
await act(async () => { deleteButton?.click(); });
ok(deleteCalls === 0 && host.textContent?.includes("确认删除"), "delete Assistant requires an explicit inline confirmation");
const confirmDeleteButton = [...host.querySelectorAll("button")].find((button) => button.textContent?.trim() === "确认删除") as HTMLButtonElement | undefined;
await act(async () => { confirmDeleteButton?.click(); });
ok(deleteCalls === 1, "confirmed delete delegates exactly once");

// ── Diagnostics editor: newest-first order, invalid time, and empty state ──
const diagnosticsFixture: AssistantDiagnostic[] = [
  { at: "2026-08-17T09:00:00Z", category: "runtime", operation: "progress_apply", message: "invalid transition" },
  { at: "not-a-date", operation: "list", message: "corrupt aggregate" },
  { at: "2026-08-17T10:00:00Z", category: "data", operation: "list", message: "skipped snapshot" },
  { at: "", operation: "run", message: "missing timestamp" },
];
const fixtureCopy = diagnosticsFixture.map((item) => ({ ...item }));
const sortedFixture = sortedDiagnostics(diagnosticsFixture);
ok(
  sortedFixture.map((item) => item.at).join("|") === "2026-08-17T10:00:00Z|2026-08-17T09:00:00Z|not-a-date|",
  "diagnostics sort newest-first by time with invalid timestamps sinking safely",
);
ok(
  diagnosticsFixture.every((item, index) => item.at === fixtureCopy[index].at && item.message === fixtureCopy[index].message),
  "diagnostics sorting never mutates the source array",
);
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><DiagnosticsEditor diagnostics={diagnosticsFixture} /></ToastProvider></LocaleProvider>);
});
const diagnosticEntries = [...host.querySelectorAll(".assistant-diagnostic-entry")];
ok(diagnosticEntries.length === 4, "diagnostics page lists every diagnostic on screen");
ok((diagnosticEntries[0]?.textContent?.includes("skipped snapshot") && diagnosticEntries[0]?.textContent?.includes("list")) ?? false, "newest diagnostic leads with operation and full message");
ok(diagnosticEntries[0]?.querySelector(".assistant-diagnostic-entry__category")?.textContent?.trim() === "数据", "category renders a readable label");
ok((diagnosticEntries[0]?.querySelector("time")?.textContent?.length ?? 0) > 0, "diagnostics show a parseable local time");
ok((diagnosticEntries[1]?.textContent?.includes("invalid transition") && diagnosticEntries[1]?.textContent?.includes("progress_apply")) ?? false, "runtime diagnostic keeps operation and message");
ok(diagnosticEntries[2]?.textContent?.includes("时间未知") ?? false, "invalid time renders a safe unknown label without throwing");
ok(diagnosticEntries[3]?.textContent?.includes("时间未知") ?? false, "missing time renders a safe unknown label without throwing");
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><DiagnosticsEditor diagnostics={[]} /></ToastProvider></LocaleProvider>);
});
ok(host.textContent?.includes("当前没有诊断记录") ?? false, "diagnostics page shows a clear empty state instead of a blank page");
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><OverviewEditor snapshot={newerSnapshot} busy="" act={async (_key, action) => { await action(); return true; }} onDelete={async () => undefined} /></ToastProvider></LocaleProvider>);
});
ok(host.querySelector(".assistant-diagnostic-list") === null, "overview no longer embeds the diagnostics detail at the bottom of the permission form");

// ── Background CSS contract ───────────────────────────────────
const testDir = dirname(fileURLToPath(import.meta.url));
const stylesSource = readFileSync(resolve(testDir, "../styles.css"), "utf8");
const assistantCssSource = readFileSync(resolve(testDir, "../custom/features/assistant/assistant.css"), "utf8");
const appSource = readFileSync(resolve(testDir, "../App.tsx"), "utf8");
const controllerSource = readFileSync(resolve(testDir, "../lib/useController.ts"), "utf8");
const assistantNavigationBlock = appSource.match(/const handleNavigateToAssistantSession = useCallback\([\s\S]*?\n  \}, \[activeTab\?\.sessionPath, enqueueNavigation\]\);/)?.[0] ?? "";
ok(
  assistantNavigationBlock.includes('kind: "linked-session"') &&
    assistantNavigationBlock.includes('topicId: ""') &&
    assistantNavigationBlock.includes("sessionPath: target.sessionPath") &&
    assistantNavigationBlock.includes("originSessionPath: activeTab?.sessionPath") &&
    assistantNavigationBlock.includes("setLinkedAssistantReturn") &&
    assistantNavigationBlock.includes("setAssistantOpen(false)"),
  "assistant session links preserve return context and close only after serialized navigation succeeds",
);
ok(
  appSource.includes("onOpenSession={handleNavigateToAssistantSession}") &&
    appSource.includes('testId: "session-assistant-return"') &&
    appSource.includes("handleReturnToAssistant") &&
    !appSource.includes('void app.OpenLinkedSession(scope, workspaceRoot, "", sessionPath)'),
  "assistant workspace uses exact-path navigation and exposes a return-to-Assistant header action",
);
const linkedSessionControllerBlock = controllerSource.match(/const openLinkedSession = useCallback\([\s\S]*?\n  \}, \[[^\]]*\]\);/)?.[0] ?? "";
ok(
  linkedSessionControllerBlock.includes("setActiveTabId(meta.id)") &&
    linkedSessionControllerBlock.includes("confirmBackendActiveTab(meta.id)") &&
    linkedSessionControllerBlock.includes("sessionPath: meta.sessionPath"),
  "linked-session navigation activates the returned tab and hydrates the exact returned session path",
);
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
ok(
  assistantCssSource.includes(".assistant-proposal__diff-row") &&
    assistantCssSource.includes("grid-template-columns: minmax(92px, 0.55fr)"),
  "proposal cards preserve a readable before/after comparison layout",
);
ok(
  /\.app--windows-frameless \.assistant-manager > header \{[^}]*padding-right:\s*calc\(var\(--windows-window-controls-safe\) \+ 14px\);/s.test(assistantCssSource),
  "management drawer close action reserves the native Windows caption-control safe area",
);
const assistantTopbarBlock = assistantCssSource.match(/\.assistant-workspace__topbar \{[\s\S]*?\n\}/)?.[0] ?? "";
ok(
  assistantTopbarBlock.includes("--wails-draggable: drag;") &&
    assistantTopbarBlock.includes("user-select: none;"),
  "assistant topbar is a native window drag region",
);
ok(
  /\.assistant-workspace__topbar button,[\s\S]*?\.assistant-workspace__topbar select \{[^}]*--wails-draggable:\s*no-drag;/s.test(assistantCssSource),
  "assistant topbar controls remain interactive inside the drag region",
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
  runs: [{ id: "run-answer", assistant_id: "assistant-answer", request_id: "run-request", trigger: "manual", state: "waiting_attention", resume_token: "answer-2", attempt: 1, max_attempts: 3, revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" }],
  attention: [{ id: "attention-answer", assistant_id: "assistant-answer", run_id: "run-answer", request_id: "attention-request", action: "answer_required", summary: "需要明确答案", resume_token: "answer-2", state: "open", revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z" }],
  updated_at: "2026-08-17T00:00:00Z",
};

const rejectedProposalSnapshot: AssistantSnapshot = {
	...attentionSnapshot,
	proposals: [{
		id: "proposal-rejected", assistant_id: attentionSnapshot.assistant.id, run_id: "run-answer",
		target_kind: "routine", target_id: "routine-missing", base_revision: 1,
		routine: { before: { enabled: true }, after: { enabled: false } },
		summary: "暂停例行任务", reason: "等待用户资料", evidence: ["run-answer 等待回答"],
		state: "rejected", resolution: "保持启用", revision: 2,
		created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:01:00Z", resolved_at: "2026-08-17T00:01:00Z",
	}],
};
await act(async () => {
	root.render(<LocaleProvider><ToastProvider><ProposalInbox snapshot={rejectedProposalSnapshot} busy="" act={async () => true} /></ToastProvider></LocaleProvider>);
});
ok(host.textContent?.includes("处理记录") && host.textContent?.includes("保持启用") || false, "terminal rejected proposals remain auditable in decision history");
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

await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AttentionInbox snapshot={{ ...attentionSnapshot, attention: [
    { ...attentionSnapshot.attention[0], id: "attention-history", resume_token: "answer-1", state: "approved" },
    attentionSnapshot.attention[0],
  ] }} busy="" act={async () => true} onOverview={() => undefined} /></ToastProvider></LocaleProvider>);
});
ok(!host.textContent?.includes("继续运行") && host.textContent?.includes("需要明确答案") || false, "resolved history stays hidden while the current continuation remains actionable");

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

// ── Waiting-attention jobs keep a diagnosable retry path ──
const waitingJob: AssistantRunnerJob = {
  id: "job-platforms", assistant_id: attentionSnapshot.assistant.id, dispatch_id: "dispatch-1", name: "plan-platforms", kind: "task", prompt: "选择发布平台",
  scope: "global", policy: attentionSnapshot.assistant.policy, state: "waiting_attention", attempt: 2, max_attempts: 3,
  error: { code: "outcome_unknown", message: "execution lease expired; external outcome is unknown", retryable: false, outcome_known: false, at: "2026-08-17T00:00:00Z" },
  revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z",
};
let jobRetried = 0;
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantJobRow job={waitingJob} busy="" onRetry={async () => { jobRetried += 1; }} onCancel={async () => undefined} owner={attentionSnapshot.assistant} onOpenSession={() => undefined} /></ToastProvider></LocaleProvider>);
});
ok(host.textContent?.includes("execution lease expired; external outcome is unknown") ?? false, "waiting_attention job keeps its recovery diagnostic visible instead of a bare label");
ok(host.textContent?.includes("重试") ?? false, "waiting_attention job exposes the retry recovery action");
ok(host.querySelector("button.assistant-jobs__name--link") === null, "a job without a session path does not render a navigation link");
const jobRetryButton = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("重试")) as HTMLButtonElement | undefined;
await act(async () => { jobRetryButton?.click(); });
ok(jobRetried === 1, "retry on a waiting_attention job re-queues for a fresh fenced execution");

// ── A running job with a session path is navigable while it runs ──
const runningJob: AssistantRunnerJob = {
  id: "job-collect", assistant_id: saved.assistant.id, dispatch_id: "dispatch-1", name: "collect-resumes", kind: "task", prompt: "收集简历",
  scope: "workspace", workspace_root: "~/projects/WorkGround2", session_path: "/mock/sessions/job-collect.jsonl",
  policy: saved.assistant.policy, state: "running", attempt: 1, max_attempts: 3,
  revision: 1, created_at: "2026-08-17T00:00:00Z", updated_at: "2026-08-17T00:00:00Z",
};
let jobOpened: { scope: string; workspaceRoot: string; sessionPath: string; assistantID: string; assistantName: string } | null = null;
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantJobRow job={runningJob} busy="" onRetry={async () => undefined} onCancel={async () => undefined} owner={saved.assistant} onOpenSession={(target) => { jobOpened = target; }} /></ToastProvider></LocaleProvider>);
});
const jobLink = host.querySelector("button.assistant-jobs__name--link") as HTMLButtonElement | null;
ok(jobLink !== null, "a running job with a session path renders a keyboard-accessible open control");
ok(jobLink?.getAttribute("aria-label") === "collect-resumes，打开会话", "running job link exposes an open-session label");
await act(async () => { jobLink?.click(); });
ok(
  jobOpened?.scope === "project" &&
    jobOpened?.workspaceRoot === "~/projects/WorkGround2" &&
    jobOpened?.sessionPath === "/mock/sessions/job-collect.jsonl" &&
    jobOpened?.assistantID === "assistant-code-project" &&
    jobOpened?.assistantName === "代码项目助手",
  "running job link hands the correct scope/workspaceRoot/path/assistant identity to onOpenSession",
);

// ── Waiting runs never show a dead-end attention label ──
const deadEndRun: AssistantRun = { ...attentionSnapshot.runs[0], id: "run-deadend" };
let reran = 0;
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><RunHistory snapshot={{ ...attentionSnapshot, runs: [deadEndRun], attention: [] }} busy="" act={async () => true} onRun={async () => { reran += 1; }} onAttention={() => undefined} onOpenSession={undefined} /></ToastProvider></LocaleProvider>);
});
ok(host.textContent?.includes("重新运行") ?? false, "a waiting run with no actionable attention item falls back to a retry instead of a dead-end label");
ok(!host.textContent?.includes("去处理") ?? false, "dead-end waiting run no longer routes to an empty inbox");
const deadEndRetry = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("重新运行")) as HTMLButtonElement | undefined;
await act(async () => { deadEndRetry?.click(); });
ok(reran === 1, "dead-end waiting run retry starts a fresh executable run");

await act(async () => {
  root.render(<LocaleProvider><ToastProvider><RunHistory snapshot={attentionSnapshot} busy="" act={async () => true} onRun={async () => undefined} onAttention={() => undefined} onOpenSession={undefined} /></ToastProvider></LocaleProvider>);
});
ok(host.textContent?.includes("去处理") ?? false, "a waiting run with an actionable attention item keeps the inbox path");

// ── 主执行视角停止 RunnerJob 操作：历史只读展示 ────────────────
const workspaceSource = readFileSync(resolve(testDir, "../custom/features/assistant/AssistantWorkspace.tsx"), "utf8");
ok(
  !/assistantRetryJob\(|assistantCancelJob\(/.test(workspaceSource),
  "main view never claims or updates RunnerJobs (no retry/cancel job bridge calls)",
);
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantJobRow job={waitingJob} busy="" owner={attentionSnapshot.assistant} onOpenSession={() => undefined} /></ToastProvider></LocaleProvider>);
});
ok(host.textContent?.includes("execution lease expired; external outcome is unknown") ?? false, "read-only job keeps its recovery diagnostic visible");
ok(
  [...host.querySelectorAll("button")].every((button) => !(button.textContent?.includes("重试") || button.textContent?.includes("停止"))),
  "read-only job row renders no retry/stop operations",
);

// ── 受管 Session 区：派生状态投影 + 控制操作 + 结果词表 ────────
resetMockSessionControlCalls();
setMockAssistantManagedSessions([
  { id: "scan-session", path: "/mock/sessions/scan-session.jsonl", title: "扫描修改", preview: "", status: "running", turns: 2, owner_id: "assistant-code-project", purpose: "managed", responsibility_id: "resp-scan", workspace_root: "~/projects/WorkGround2", updated_at: "2026-08-28T02:00:00Z" },
  { id: "release-session", path: "/mock/sessions/release-session.jsonl", title: "发布准备", preview: "", status: "failed", turns: 1, owner_id: "assistant-code-project", purpose: "managed", updated_at: "2026-08-28T01:00:00Z" },
]);
setMockAssistantSessionStatus({
  "scan-session": {
    id: "scan-session", path: "/mock/sessions/scan-session.jsonl", title: "扫描修改", status: "running", turns: 2, purpose: "managed", running: true, updated_at: "2026-08-28T02:00:00Z",
    interactions: [{ kind: "ask", id: "ask-1", questions: [{ id: "q1", prompt: "选择发布环境？", options: [{ label: "测试" }, { label: "生产" }] }] }],
  },
});
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantWorkspace key="managed-sessions" /></ToastProvider></LocaleProvider>);
  await new Promise((resolve) => setTimeout(resolve, 40));
});
ok(host.querySelector(".assistant-managed") !== null, "managed session section renders");
ok(host.textContent?.includes("受管 Session") ?? false, "managed session section carries its label");
const managedCard = (sessionID: string) => host.querySelector(`[data-session-id="${sessionID}"]`) as HTMLElement | null;
const runningCard = managedCard("scan-session");
const failedCard = managedCard("release-session");
ok(runningCard !== null && failedCard !== null, "managed section lists running and failed sessions");
ok(runningCard?.textContent?.includes("运行中") ?? false, "running session shows its derived status");
ok((runningCard?.textContent?.includes("归属助手") && runningCard?.textContent?.includes("assistant-code-project")) ?? false, "owner identity is shown");
ok((runningCard?.textContent?.includes("责任") && runningCard?.textContent?.includes("resp-scan")) ?? false, "bound responsibility is shown");
ok((runningCard?.textContent?.includes("工作区") && runningCard?.textContent?.includes("~/projects/WorkGround2")) ?? false, "workspace root is shown");
ok(runningCard?.textContent?.includes("最后更新") ?? false, "update time is shown");
ok(failedCard?.textContent?.includes("失败") ?? false, "failed session shows its derived status");

// 停止（cancel）→ accepted 结果词表
const cardButton = (card: HTMLElement | null, label: string) => [...(card?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.includes(label)) as HTMLButtonElement | undefined;
await act(async () => { cardButton(runningCard, "停止")?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
let controlCalls = getMockSessionControlCalls();
ok(controlCalls.some((call) => call.op === "cancel" && call.sessionId === "scan-session"), "stop submits a session cancel intent");
ok(controlCalls.find((call) => call.op === "cancel")?.requestId.startsWith("desktop-assistant:") ?? false, "cancel carries a stable request_id");
ok(managedCard("scan-session")?.querySelector(".assistant-session-outcome--accepted")?.textContent?.includes("已受理") ?? false, "accepted outcome is displayed");

// 指导（steer）
const steerInput = managedCard("scan-session")?.querySelector('input[aria-label^="指导"]') as HTMLInputElement | null;
const steerSetter = Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")?.set;
act(() => {
  steerSetter?.call(steerInput, "先做扫描再汇报");
  reactProps<{ onChange: (event: { target: HTMLInputElement }) => void }>(steerInput!).onChange({ target: steerInput! });
});
await act(async () => { cardButton(managedCard("scan-session"), "发送指导")?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
controlCalls = getMockSessionControlCalls();
ok(controlCalls.some((call) => call.op === "steer" && call.text === "先做扫描再汇报"), "steer submits the typed guidance");
ok((managedCard("scan-session")?.querySelector('input[aria-label^="指导"]') as HTMLInputElement | null)?.value === "", "accepted steer clears the draft");

// 回答 pending interaction
const answerOption = [...(managedCard("scan-session")?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.trim() === "生产") as HTMLButtonElement | undefined;
ok(answerOption !== undefined, "pending ask exposes its options");
await act(async () => { answerOption?.click(); });
const answerSend = cardButton(managedCard("scan-session"), "提交回答");
ok(answerSend !== undefined && !answerSend.disabled, "answer can be submitted after selecting an option");
await act(async () => { answerSend?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
controlCalls = getMockSessionControlCalls();
ok(controlCalls.some((call) => call.op === "answer" && call.sessionId === "scan-session"), "answer submits a session answer intent");

// 恢复（resume）失败 Session、分叉（fork）
await act(async () => { cardButton(managedCard("release-session"), "恢复")?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
ok(getMockSessionControlCalls().some((call) => call.op === "resume" && call.sessionId === "release-session"), "resume submits a session resume intent");
await act(async () => { cardButton(managedCard("scan-session"), "分叉")?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
ok(getMockSessionControlCalls().some((call) => call.op === "fork" && call.sessionId === "scan-session"), "fork submits a session fork intent");

// ── 监督诊断面板：后端 DTO 复用 ───────────────────────────────
setMockAssistantSupervisorDiagnostic({
  assistant_id: "assistant-code-project",
  supervisor: { id: "supervisor-session-1", path: "/mock/sessions/supervisor-session-1.jsonl" },
  cycle: { id: "cycle-1", fence: 7, state: "checkpointed", observed: { plan_revision: 3, assistant_revision: 4, memory_revision: 2, work_epoch: 1 }, next_step: "advance 1 executable responsibilities", revision: 9, updated_at: "2026-08-28T02:00:00Z" },
  pending_events: [{ id: "ev-1", kind: "user_input", session_id: "scan-session", at: "2026-08-28T02:00:00Z" }],
  recent_decisions: [{ id: "d-1", session_id: "scan-session", interaction_id: "q1", source: "infer", confidence: 0.9, result: "answer-a", created_at: "2026-08-28T02:00:00Z" }],
  recent_receipts: [{ request_id: "req-1", operation: "session_steer", created_at: "2026-08-28T02:00:00Z" }],
  next_step: "advance 1 executable responsibilities",
  running_sessions: [{ id: "scan-session", title: "扫描修改", status: "running", purpose: "managed" }],
  failed_sessions: [{ id: "release-session", title: "发布准备", status: "failed", purpose: "managed" }],
  retry_due: 2,
  diagnostics: [{ at: "2026-08-28T02:00:00Z", category: "runtime", operation: "cycle_workcontrol", message: "gate busy" }],
  at: "2026-08-28T02:00:00Z",
});
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantWorkspace key="diagnostic" /></ToastProvider></LocaleProvider>);
  await new Promise((resolve) => setTimeout(resolve, 40));
});
const supervisorDetails = host.querySelector("details.assistant-supervisor") as HTMLDetailsElement | null;
ok(supervisorDetails !== null, "supervisor diagnostic panel renders");
ok(supervisorDetails?.querySelector("summary")?.textContent?.includes("监督诊断") ?? false, "diagnostic panel carries its label");
await act(async () => { supervisorDetails?.querySelector("summary")?.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true, cancelable: true })); });
ok(host.textContent?.includes("supervisor-session-1") ?? false, "panel shows the supervisor session id");
ok(
  (host.textContent?.includes("计划 3") && host.textContent?.includes("助手 4") && host.textContent?.includes("记忆 2") && host.textContent?.includes("工作纪元 1")) ?? false,
  "cycle observation revisions are shown",
);
ok(host.textContent?.includes("user_input") ?? false, "pending event kind is shown");
ok(host.textContent?.includes("advance 1 executable responsibilities") ?? false, "cycle next step is shown");
ok(host.textContent?.includes("待重试") ?? false, "retry due label is shown");
ok(host.textContent?.includes("release-session") ?? false, "failed supervisor session is shown");

// ── viewport：真实选中/可见状态发布，revision 单调递增 ────────
resetMockViewportPublishes();
setMockAssistantManagedSessions([
  { id: "scan-session", path: "/mock/sessions/scan-session.jsonl", title: "扫描修改", preview: "", status: "running", turns: 2, owner_id: "assistant-code-project", purpose: "managed", responsibility_id: "resp-scan", workspace_root: "~/projects/WorkGround2", updated_at: "2026-08-28T02:00:00Z" },
  { id: "release-session", path: "/mock/sessions/release-session.jsonl", title: "发布准备", preview: "", status: "failed", turns: 1, owner_id: "assistant-code-project", purpose: "managed", updated_at: "2026-08-28T01:00:00Z" },
]);
setMockAssistantSupervisorDiagnostic(null);
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantWorkspace key="viewport" /></ToastProvider></LocaleProvider>);
  await new Promise((resolve) => setTimeout(resolve, 40));
});
const viewportPublishes = getMockViewportPublishes();
ok(viewportPublishes.length >= 1, "workspace publishes a viewport observation");
const beforeSelectPublish = viewportPublishes[viewportPublishes.length - 1];
ok(beforeSelectPublish?.windowId === "assistant-main" && beforeSelectPublish?.workspaceId === "~/projects/WorkGround2", "viewport carries the workspace");
ok((beforeSelectPublish?.visibleSessionIds?.includes("scan-session") && beforeSelectPublish?.visibleSessionIds?.includes("release-session")) ?? false, "viewport lists the visible managed sessions");
await act(async () => { managedCard("scan-session")?.querySelector(".assistant-session-card__select")?.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true, cancelable: true })); await new Promise((resolve) => setTimeout(resolve, 10)); });
const afterSelectPublishes = getMockViewportPublishes();
const selectedPublish = afterSelectPublishes[afterSelectPublishes.length - 1];
ok(selectedPublish?.selectedSessionId === "scan-session", "selecting a session publishes it as the viewport selection");
const selectedRev = Number(selectedPublish?.uiRevision ?? 0);
const beforeRev = Number(beforeSelectPublish?.uiRevision ?? 0);
ok(selectedRev > beforeRev, `viewport revision is monotonic across selection changes (${selectedRev} > ${beforeRev})`);

// ── 同一次失败重试复用同一 request_id ─────────────────────────
resetMockSessionControlCalls();
setMockAssistantManagedSessions([
  { id: "scan-session", path: "/mock/sessions/scan-session.jsonl", title: "扫描修改", preview: "", status: "running", turns: 2, owner_id: "assistant-code-project", purpose: "managed", workspace_root: "~/projects/WorkGround2", updated_at: "2026-08-28T02:00:00Z" },
]);
setMockAssistantSessionStatus(null);
setMockAssistantSessionSteerShouldFail(true);
await act(async () => {
  root.render(<LocaleProvider><ToastProvider><AssistantWorkspace key="retry" /></ToastProvider></LocaleProvider>);
  await new Promise((resolve) => setTimeout(resolve, 40));
});
const retrySteerInput = managedCard("scan-session")?.querySelector('input[aria-label^="指导"]') as HTMLInputElement | null;
act(() => {
  steerSetter?.call(retrySteerInput, "继续扫描");
  reactProps<{ onChange: (event: { target: HTMLInputElement }) => void }>(retrySteerInput!).onChange({ target: retrySteerInput! });
});
await act(async () => { cardButton(managedCard("scan-session"), "发送指导")?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
let retryCalls = getMockSessionControlCalls();
ok(retryCalls.length === 1, "first steer attempt reaches the backend");
const firstRequestID = retryCalls[0]?.requestId ?? "";
ok(managedCard("scan-session")?.querySelector(".assistant-session-outcome--retryable_error") !== null, "failed steer surfaces a retryable error outcome");
ok((managedCard("scan-session")?.querySelector('input[aria-label^="指导"]') as HTMLInputElement | null)?.value === "继续扫描", "failed steer keeps the draft for a safe retry");
await act(async () => { cardButton(managedCard("scan-session"), "发送指导")?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
retryCalls = getMockSessionControlCalls();
ok(retryCalls.length === 2 && retryCalls[1]?.requestId === firstRequestID, "retry after the same failure reuses the same request_id");
setMockAssistantSessionSteerShouldFail(false);
await act(async () => { cardButton(managedCard("scan-session"), "发送指导")?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
retryCalls = getMockSessionControlCalls();
ok(retryCalls.length === 3 && retryCalls[2]?.requestId === firstRequestID, "recovering retry reuses the pending request_id until accepted");
ok(managedCard("scan-session")?.querySelector(".assistant-session-outcome--accepted") !== null, "recovered steer shows the accepted outcome");
act(() => {
  const freshSteerInput = managedCard("scan-session")?.querySelector('input[aria-label^="指导"]') as HTMLInputElement | null;
  steerSetter?.call(freshSteerInput, "换个方向");
  reactProps<{ onChange: (event: { target: HTMLInputElement }) => void }>(freshSteerInput!).onChange({ target: freshSteerInput! });
});
await act(async () => { cardButton(managedCard("scan-session"), "发送指导")?.click(); await new Promise((resolve) => setTimeout(resolve, 10)); });
retryCalls = getMockSessionControlCalls();
ok(retryCalls.length === 4 && retryCalls[3]?.requestId !== firstRequestID, "a new steer after acceptance gets a fresh request_id");
setMockAssistantSessionSteerShouldFail(false);
setMockAssistantManagedSessions(null);
setMockAssistantSessionStatus(null);
setMockAssistantSupervisorDiagnostic(null);

await act(async () => { root.unmount(); });
console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed) process.exit(1);
