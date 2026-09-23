// Component regressions for the run record detail panel:
//   - the compact RunBlock gains an entry point without losing its own interaction;
//   - the panel is a two-column list + detail view with search, 仅失败,
//     上一/下一失败, an explicit follow-latest and a new-record hint;
//   - a record is never stolen from the reader by an arriving record;
//   - Escape closes, the list is keyboard navigable, focus lands in the panel;
//   - full args/output are fetched on demand, and failure / unavailable /
//     truncation are stated honestly instead of rendered as an empty output;
//   - switching records cannot show a previous record's late response;
//   - a long run only mounts a window of rows.

import { JSDOM } from "jsdom";
import React, { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";

import { RunBlock } from "../components/desktop-ui/RunBlock";
import { RunDetailPanel } from "../components/desktop-ui/RunDetailPanel";
import { useRunStore, type RunEvent, type RunRecord } from "../store/run";
import { resetDetailBodies } from "../lib/runDetailData";
import { resetDetailScrollMemory } from "../lib/runDetailScroll";

let passed = 0;
let failed = 0;
function ok(condition: boolean, label: string): void {
  process.stdout.write(`  ${condition ? "PASS" : "FAIL"}  ${label}\n`);
  if (condition) passed++; else failed++;
}
function eq<T>(actual: T, expected: T, label: string): void {
  ok(actual === expected, `${label}${actual === expected ? "" : ` (got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)})`}`);
}

function setupDOM(): JSDOM {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  Object.assign(globalThis, {
    IS_REACT_ACT_ENVIRONMENT: true,
    window: dom.window,
    document: dom.window.document,
    Node: dom.window.Node,
    Element: dom.window.Element,
    HTMLElement: dom.window.HTMLElement,
    HTMLInputElement: dom.window.HTMLInputElement,
    SVGElement: dom.window.SVGElement,
    Event: dom.window.Event,
    MouseEvent: dom.window.MouseEvent,
    KeyboardEvent: dom.window.KeyboardEvent,
    MutationObserver: dom.window.MutationObserver,
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
    getComputedStyle: dom.window.getComputedStyle.bind(dom.window),
  });
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  return dom;
}

const dom = setupDOM();

// jsdom has no layout engine, so every element measures 0×0 and a virtualizer
// would compute an empty window. Give the harness a real viewport size so the
// list is exercised the way the app exercises it (the library reads offsetWidth
// / offsetHeight on mount).
for (const proto of [dom.window.Element.prototype, dom.window.HTMLElement.prototype]) {
  for (const [property, size] of [["offsetWidth", 320], ["offsetHeight", 480], ["clientWidth", 320], ["clientHeight", 480]] as const) {
    Object.defineProperty(proto, property, { configurable: true, get: () => size });
  }
}

async function settle(delay = 30): Promise<void> {
  await act(async () => { await new Promise<void>((resolve) => setTimeout(resolve, delay)); });
}

async function interact(action: () => void): Promise<void> {
  await act(async () => {
    action();
    await new Promise<void>((resolve) => setTimeout(resolve, 30));
  });
}

interface Mounted { root: Root; host: HTMLDivElement; cleanup: () => Promise<void>; }

async function mount(element: ReactElement): Promise<Mounted> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host, { onCaughtError: (error) => { throw error; } });
  await act(async () => { root.render(element); });
  await settle();
  return {
    root,
    host,
    cleanup: async () => {
      await act(async () => { root.unmount(); });
      host.remove();
    },
  };
}

function text(): string {
  return document.body.textContent ?? "";
}

/** Only the detail column — list rows repeat the same labels. */
function detailText(): string {
  return document.body.querySelector(".run-detail-record")?.textContent ?? "";
}

function byAria(label: string): HTMLElement | null {
  return document.body.querySelector<HTMLElement>(`[aria-label="${label}"]`);
}

function click(element: Element | null): void {
  element?.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true }));
}

function key(element: Element | null, keyName: string): void {
  element?.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: keyName, bubbles: true }));
}

/**
 * Types into a controlled input the way React sees a real keystroke: the value
 * must go through the prototype setter, otherwise React's value tracker still
 * believes the field is unchanged and never fires onChange.
 */
/**
 * Types into a controlled input. React 19 no longer reports a change from a
 * plain `input` event on a programmatically assigned value, so the value goes
 * through the native setter and then straight into the element's React props —
 * the same approach the other input-driven suites in this repo use.
 */
function type(element: HTMLInputElement | null, value: string): void {
  if (!element) return;
  Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")?.set?.call(element, value);
  const propsKey = Object.keys(element).find((key) => key.startsWith("__reactProps$"));
  const props = propsKey
    ? (element as unknown as Record<string, { onChange?: (event: { target: HTMLInputElement }) => void }>)[propsKey]
    : undefined;
  if (props?.onChange) {
    props.onChange({ target: element });
    return;
  }
  element.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
}

const events = (count: number, make: (index: number) => Partial<RunEvent> = () => ({})): RunEvent[] =>
  Array.from({ length: count }, (_, index) => ({
    eventId: `e${index + 1}`,
    kind: "command" as const,
    content: `record ${index + 1}`,
    toolName: "bash",
    status: "completed" as const,
    ...make(index),
  }));

function run(overrides: Partial<RunRecord> = {}): RunRecord {
  return {
    runId: "run-1",
    sessionId: "tab-1",
    turnId: "turn:1",
    status: "completed",
    events: events(3),
    expanded: false,
    startedAt: 1000,
    completedAt: 5000,
    detailFollowLatest: true,
    ...overrides,
  };
}

type PanelHandlers = {
  onSelectEvent?: (runId: string, eventId?: string) => void;
  onToggleMaximized?: (runId: string, maximized: boolean) => void;
  onClose?: () => void;
  onStop?: (runId: string) => void;
  onRetry?: (runId: string) => void;
};

async function renderPanel(record: RunRecord, handlers: PanelHandlers = {}): Promise<Mounted> {
  return mount(
    <RunDetailPanel
      run={record}
      tabId={record.sessionId}
      onSelectEvent={handlers.onSelectEvent ?? (() => {})}
      onToggleMaximized={handlers.onToggleMaximized ?? (() => {})}
      onClose={handlers.onClose ?? (() => {})}
      onStop={handlers.onStop}
      onRetry={handlers.onRetry}
    />,
  );
}

function stubToolResult(handler: (tabId: string, toolId: string) => Promise<{ args: string; output: string } | null>): void {
  Object.assign(dom.window, {
    go: { main: { App: { ToolResultForTab: (tabId: string, toolId: string) => handler(tabId, toolId) } } },
  });
}

async function main() {
  useRunStore.setState({ runs: {} });
  resetDetailBodies();
  resetDetailScrollMemory();

  // ── the compact card keeps its shape and gains an entry ──────────────────
  {
    const opened: string[] = [];
    const mounted = await mount(
      <RunBlock run={run({ detailOpen: false })} onOpenDetail={(runId) => opened.push(runId)} />,
    );
    const detailButton = byAria("查看运行记录详情");
    ok(detailButton !== null, "RunBlock: exposes a 查看详情 button");
    eq(document.body.querySelectorAll(".run-step-tab").length, 3, "RunBlock: keeps its three compact step tabs");
    ok(text().includes("运行完成"), "RunBlock: keeps its terminal status label");
    ok(text().includes("3 条记录"), "RunBlock: shows the record count and duration readout");

    const countEntry = byAria("查看 3 条运行记录详情");
    ok(countEntry !== null, "RunBlock: the record count is a detail entry of its own");
    await interact(() => click(countEntry));
    await interact(() => click(detailButton));
    eq(opened.join(","), "run-1,run-1", "RunBlock: both entries ask the stream to open the detail panel");
    eq(countEntry?.getAttribute("aria-expanded"), "false", "RunBlock: the record entry reports its collapsed state");

    await mounted.cleanup();
  }

  // A card rendered without a detail handler must not render a dead control.
  {
    const mounted = await mount(<RunBlock run={run()} />);
    ok(byAria("查看运行记录详情") === null, "RunBlock: no detail button when the run stream owns no panel");
    ok(byAria("查看 3 条运行记录详情")?.hasAttribute("disabled") === true, "RunBlock: the count readout falls back to plain text without a handler");
    await mounted.cleanup();
  }

  // ── panel shape: list + detail, two columns ─────────────────────────────
  {
    const mounted = await renderPanel(run());
    const panel = document.body.querySelector(".run-detail-panel");
    ok(panel !== null, "panel: renders as a right-side panel");
    eq(panel?.getAttribute("role"), "dialog", "panel: announces itself as a dialog");
    ok((panel?.getAttribute("aria-label") ?? "").includes("运行记录详情"), "panel: has an explicit accessible name");
    eq(document.body.querySelectorAll('[role="option"]').length, 3, "panel: the list shows every record");
    eq(document.body.querySelectorAll(".run-detail-record").length, 1, "panel: the detail column renders exactly one record");
    ok(text().includes("3 条记录"), "panel: the header repeats the record count");
    ok(byAria("检索") === null, "panel: no invented controls");
    ok(byAria("搜索命令、文件或工具") !== null, "panel: offers a search over commands, files and tools");
    ok(byAria("在输出中查找") !== null, "panel: offers output search");
    ok(byAria("调整记录详情面板宽度") !== null, "panel: offers a resize handle");
    await mounted.cleanup();
  }

  // ── selection is by stable event id, and the detail follows it ──────────
  {
    const selected: string[] = [];
    const record = run({ events: events(3) });
    let mounted = await renderPanel(record, { onSelectEvent: (_runId, eventId) => selected.push(eventId ?? "latest") });
    await interact(() => click(document.body.querySelectorAll('[role="option"]')[0]));
    await mounted.cleanup();

    // Re-render pinned to the middle record: the detail must show it, not the newest.
    const pinned = run({ detailFollowLatest: false, detailSelectedEventId: "e2", events: [
      { eventId: "e1", kind: "read", content: "first record", toolName: "read_file", status: "completed" },
      { eventId: "e2", kind: "command", content: "SECOND-BODY", toolName: "bash", status: "failed", error: "BOOM" },
      { eventId: "e3", kind: "test", content: "third record", toolName: "shell", status: "completed" },
    ] });
    mounted = await renderPanel(pinned, { onSelectEvent: (_runId, eventId) => selected.push(eventId ?? "latest") });
    eq(selected.join(","), "e1", "panel: clicking a row selects it by its stable eventId");
    ok(text().includes("SECOND-BODY"), "panel: the detail shows the selected record");
    ok(!detailText().includes("third record"), "panel: the detail does not show a different record");
    eq(document.body.querySelectorAll('[aria-selected="true"]').length, 1, "panel: exactly one row is selected");
    await mounted.cleanup();
  }

  // ── follow-latest, new-record hint, and the pin that survives ───────────
  {
    const selected: (string | undefined)[] = [];
    const pinned = run({
      detailFollowLatest: false,
      detailSelectedEventId: "e1",
      events: events(4),
    });
    const mounted = await renderPanel(pinned, { onSelectEvent: (_runId, eventId) => selected.push(eventId) });

    const hint = document.body.querySelector(".run-detail-newrecords");
    ok(hint !== null, "panel: a pinned reader sees how many records arrived");
    ok((hint?.textContent ?? "").includes("3 条新记录"), "panel: the hint counts the records that arrived after the selected one");
    ok(detailText().includes("record 1"), "panel: reading an older record is not stolen by the new ones");
    ok(!detailText().includes("record 4"), "panel: the newly arrived record is not shown instead");

    await interact(() => click(hint));
    eq(selected[selected.length - 1], undefined, "panel: 跳到最新 clears the pin so the newest record wins");
    await mounted.cleanup();
  }

  {
    const following = await renderPanel(run({ detailFollowLatest: true, events: events(4) }));
    ok(document.body.querySelector(".run-detail-newrecords") === null, "panel: no new-record hint while following the newest record");
    ok(detailText().includes("record 4"), "panel: following shows the newest record");
    ok(byAria("跟随最新")?.getAttribute("aria-pressed") === "true", "panel: the follow control reports that it is on");
    await following.cleanup();
  }

  // A pinned record that vanished from the run says so instead of lying.
  {
    const mounted = await renderPanel(run({ detailFollowLatest: false, detailSelectedEventId: "gone" }));
    ok(text().includes("原来选中的记录已不在本次运行中"), "panel: a vanished pin is reported");
    await mounted.cleanup();
  }

  // The compact card and the panel answer the same question, so a step pinned
  // there carries over instead of the panel jumping to the newest record.
  {
    const selected: (string | undefined)[] = [];
    const mounted = await renderPanel(
      run({ detailFollowLatest: true, selectedStepIndex: 0, events: events(4) }),
      { onSelectEvent: (_runId, eventId) => selected.push(eventId) },
    );
    ok(detailText().includes("record 1"), "panel: a step pinned in the compact card opens the panel on that record");
    ok(document.body.querySelector(".run-detail-newrecords") !== null, "panel: arrivals do not steal a record pinned in the compact card");
    await interact(() => click(document.body.querySelector(".run-detail-newrecords")));
    eq(selected[selected.length - 1], undefined, "panel: 跳到最新 releases the compact card's pin too");
    await mounted.cleanup();
  }

  // ── search, 仅失败 and failure navigation ───────────────────────────────
  {
    const record = run({
      events: [
        { eventId: "e1", kind: "command", content: "go build ./...", toolName: "bash", args: '{"command":"go build ./..."}', status: "completed" },
        { eventId: "e2", kind: "read", content: "read src/a.ts", toolName: "read_file", args: '{"path":"src/a.ts"}', status: "completed" },
        { eventId: "e3", kind: "edit", content: "edit src/b.ts", toolName: "edit_file", args: '{"path":"src/b.ts"}', status: "failed", error: "permission denied" },
      ],
      detailFollowLatest: false,
      detailSelectedEventId: "e1",
    });
    const selected: (string | undefined)[] = [];
    const mounted = await renderPanel(record, { onSelectEvent: (_runId, eventId) => selected.push(eventId) });

    await interact(() => type(byAria("搜索命令、文件或工具") as HTMLInputElement, "a.ts"));
    eq(document.body.querySelectorAll('[role="option"]').length, 1, "panel: the search narrows the list");
    ok(text().includes("/3"), "panel: the toolbar reports how much of the run is shown");

    await interact(() => type(byAria("搜索命令、文件或工具") as HTMLInputElement, ""));
    await interact(() => click(byAria("仅失败")));
    eq(document.body.querySelectorAll('[role="option"]').length, 1, "panel: 仅失败 keeps only the failed records");
    eq(byAria("仅失败")?.getAttribute("aria-pressed"), "true", "panel: 仅失败 reports that it is on");

    await interact(() => click(byAria("仅失败")));
    await interact(() => click(byAria("下一个失败记录")));
    eq(selected[selected.length - 1], "e3", "panel: 下一失败 jumps to the failed record");
    await interact(() => click(byAria("上一个失败记录")));
    eq(selected[selected.length - 1], "e3", "panel: 上一失败 stays on the only failure");

    await mounted.cleanup();
  }

  // ── keyboard: Escape closes, the list navigates, focus lands inside ─────
  {
    let closed = 0;
    const selected: (string | undefined)[] = [];
    const record = run({ detailFollowLatest: true, events: events(3) });
    const mounted = await renderPanel(record, {
      onClose: () => { closed++; },
      onSelectEvent: (_runId, eventId) => selected.push(eventId),
    });
    const panel = document.body.querySelector<HTMLElement>(".run-detail-panel");
    eq(document.activeElement, panel, "panel: focus moves into the panel when it opens");

    const list = document.body.querySelector(".run-detail-list");
    await interact(() => key(list, "ArrowUp"));
    eq(selected[selected.length - 1], "e2", "panel: ArrowUp moves to the previous record");
    await interact(() => key(list, "Home"));
    eq(selected[selected.length - 1], "e1", "panel: Home jumps to the first record");

    await interact(() => key(panel, "Escape"));
    eq(closed, 1, "panel: Escape closes the panel");
    await mounted.cleanup();
  }

  // The trigger takes focus back once the panel closes.
  {
    const mounted = await mount(<RunBlock run={run({ detailOpen: true })} onOpenDetail={() => {}} />);
    const trigger = byAria("查看运行记录详情");
    ok(trigger !== null, "focus: the trigger exists while the panel is open");
    await interact(() => { mounted.root.render(<RunBlock run={run({ detailOpen: false })} onOpenDetail={() => {}} />); });
    eq(document.activeElement, byAria("查看运行记录详情"), "focus: closing the panel returns focus to its trigger");
    await mounted.cleanup();
  }

  // ── maximize, and the width is remembered ──────────────────────────────
  {
    const maximizing: boolean[] = [];
    const mounted = await renderPanel(run({ detailMaximized: true }), { onToggleMaximized: (_runId, value) => maximizing.push(value) });
    const panel = document.body.querySelector(".run-detail-panel");
    ok(panel?.classList.contains("run-detail-panel--maximized") === true, "panel: a maximized run fills the window");
    await interact(() => click(byAria("还原面板")));
    eq(maximizing.join(","), "false", "panel: the maximize control toggles back to restore");
    await mounted.cleanup();
  }

  {
    dom.window.localStorage.clear();
    const mounted = await renderPanel(run({ detailMaximized: false }));
    const resizer = byAria("调整记录详情面板宽度");
    const before = Number(resizer?.getAttribute("aria-valuenow"));
    await interact(() => key(resizer, "ArrowLeft"));
    const after = Number(resizer?.getAttribute("aria-valuenow"));
    ok(after > before, "panel: the resize handle widens from the keyboard");
    const saved = JSON.parse(dom.window.localStorage.getItem("WorkGround2.layoutPreferences.v1") ?? "{}");
    eq(saved.sizes?.runDetailWidth, after, "panel: the chosen width is persisted for the next open");
    await mounted.cleanup();
  }

  // ── the full record: args, diff, output, error, duration, find ─────────
  {
    const record = run({
      detailFollowLatest: false,
      detailSelectedEventId: "e1",
      events: [{
        eventId: "e1",
        kind: "edit",
        content: "edit src/b.ts",
        toolName: "edit_file",
        args: '{"path":"src/b.ts"}',
        status: "failed",
        error: "permission denied on src/b.ts",
        output: "wrote 10 lines\nwrote 20 lines",
        durationMs: 940,
        diff: "@@ -1 +1 @@\n-a\n+b",
        callId: "call-42",
      }],
    });
    const mounted = await renderPanel(record);
    ok(byAria("调用参数") !== null, "record: arguments are shown when the backend sent them");
    ok(text().includes('"path": "src/b.ts"'), "record: arguments are pretty printed");
    ok(byAria("文件改动") !== null, "record: a real diff is rendered when the tool reported one");
    ok(byAria("输出") !== null, "record: the output is shown");
    ok(text().includes("wrote 20 lines"), "record: the full output is shown");
    ok(byAria("错误") !== null, "record: the error is shown as its own block");
    ok(text().includes("permission denied on src/b.ts"), "record: the error text is visible next to the output");
    ok(text().includes("耗时 940 ms"), "record: the call duration is shown");
    ok(text().includes("call-42"), "record: the call identity is shown");
    ok(text().includes("失败"), "record: a failed record says so");

    await interact(() => type(byAria("在输出中查找") as HTMLInputElement, "wrote"));
    ok(document.body.querySelectorAll("mark.run-detail-hit").length >= 2, "record: output search highlights its matches");
    ok(text().includes("处匹配"), "record: output search reports the match count");
    ok(byAria("自动换行")?.getAttribute("aria-pressed") === "true", "record: wrapping is on by default");
    await interact(() => click(byAria("自动换行")));
    ok(byAria("自动换行")?.getAttribute("aria-pressed") === "false", "record: wrapping can be turned off");
    ok(byAria("复制参数") !== null && byAria("复制输出") !== null && byAria("复制错误") !== null, "record: args, output and error can each be copied");
    await mounted.cleanup();
  }

  // ── a long run only mounts a window of rows ────────────────────────────
  {
    const mounted = await renderPanel(run({ events: events(400) }));
    const rows = document.body.querySelectorAll('[role="option"]').length;
    const sizer = document.body.querySelector<HTMLElement>(".run-detail-list__sizer");
    ok(rows > 0, "virtual list: rows are mounted");
    ok(rows < 60, `virtual list: only a window of rows is mounted (mounted ${rows} of 400)`);
    eq(Math.round(Number.parseFloat(sizer?.style.height ?? "0")), 400 * 44, "virtual list: the scroll height covers every record");
    ok(text().includes("400 条记录"), "virtual list: the header still reports the true record count");
    await mounted.cleanup();
  }

  // ── on-demand body: loaded, unavailable, failed, retried ───────────────
  {
    const archived = run({
      runId: "run-archived",
      detailFollowLatest: false,
      detailSelectedEventId: "e1",
      events: [{
        eventId: "e1",
        kind: "read",
        content: "read src/a.ts",
        toolName: "read_file",
        status: "completed",
        archived: true,
        callId: "call-archived",
      }],
    });

    const seen: string[] = [];
    stubToolResult(async (tabId, toolId) => {
      seen.push(`${tabId}:${toolId}`);
      return { args: '{"path":"src/a.ts"}' , output: "FULL-BODY-FROM-BACKEND" };
    });
    const loaded = await renderPanel(archived);
    await settle(80);
    eq(seen.join(","), "tab-1:call-archived", "on-demand body: the record's own call identity is used");
    ok(text().includes("FULL-BODY-FROM-BACKEND"), "on-demand body: the fetched output replaces the摘要");
    ok(text().includes('"path": "src/a.ts"'), "on-demand body: the fetched arguments are shown");
    await loaded.cleanup();
  }

  {
    // The session no longer holds the call: say so, and do not pretend it is empty.
    resetDetailBodies();
    let calls = 0;
    stubToolResult(async () => { calls++; return null; });
    const archived = run({
      runId: "run-missing",
      detailFollowLatest: false,
      detailSelectedEventId: "e1",
      events: [{
        eventId: "e1",
        kind: "read",
        content: "read src/a.ts",
        toolName: "read_file",
        status: "completed",
        archived: true,
        callId: "call-missing",
      }],
    });
    const mounted = await renderPanel(archived);
    await settle(80);
    ok(text().includes("完整内容已不可用"), "unavailable: the panel states that the body is unavailable");
    ok(byAria("输出") === null, "unavailable: no empty output block is invented");
    eq(calls, 1, "unavailable: exactly one attempt was made");
    await interact(() => click(byAria("重新加载")));
    await settle(80);
    eq(calls, 2, "unavailable: 重新加载 retries the fetch");
    await mounted.cleanup();
  }

  {
    resetDetailBodies();
    let calls = 0;
    stubToolResult(async () => {
      calls++;
      if (calls === 1) throw new Error("bridge offline");
      return { args: "", output: "RECOVERED-BODY" };
    });
    const archived = run({
      runId: "run-flaky",
      detailFollowLatest: false,
      detailSelectedEventId: "e1",
      events: [{ eventId: "e1", kind: "read", content: "read", toolName: "read_file", status: "completed", archived: true, callId: "call-flaky" }],
    });
    const mounted = await renderPanel(archived);
    await settle(80);
    ok(text().includes("读取完整内容失败"), "load failure: the failure is stated, not swallowed");
    ok(text().includes("bridge offline"), "load failure: the reason is shown");
    await interact(() => click(byAria("重新加载")));
    await settle(80);
    ok(text().includes("RECOVERED-BODY"), "load failure: 重新加载 can recover the body");
    await mounted.cleanup();
  }

  {
    resetDetailBodies();
    // A truncated record keeps whatever it has, and says that it is not the whole log.
    const record = run({
      runId: "run-truncated",
      detailFollowLatest: false,
      detailSelectedEventId: "e1",
      events: [{
        eventId: "e1",
        kind: "command",
        content: "head…tail",
        toolName: "bash",
        status: "completed",
        output: "head\ntail",
        truncated: true,
        callId: "call-truncated",
      }],
    });
    stubToolResult(async () => ({ args: "", output: "head\ntail" }));
    const mounted = await renderPanel(record);
    await settle(80);
    ok(text().includes("head"), "truncation: the available output stays visible");
    ok(text().includes("不是完整日志"), "truncation: the panel says the output is truncated");
    await mounted.cleanup();
  }

  // ── a late response for the previous record cannot pollute the new one ─
  {
    resetDetailBodies();
    let releaseFirst: (() => void) | null = null;
    stubToolResult(async (_tabId, toolId) => {
      if (toolId === "call-slow") {
        await new Promise<void>((resolve) => { releaseFirst = resolve; });
        return { args: "", output: "SLOW-OLD-RECORD-BODY" };
      }
      return { args: "", output: "FAST-RECORD-BODY" };
    });
    const record = run({
      runId: "run-race",
      detailFollowLatest: false,
      detailSelectedEventId: "e1",
      events: [
        { eventId: "e1", kind: "read", content: "slow", toolName: "read_file", status: "completed", archived: true, callId: "call-slow" },
        { eventId: "e2", kind: "read", content: "fast", toolName: "read_file", status: "completed", archived: true, callId: "call-fast" },
      ],
    });
    const mounted = await renderPanel(record);
    await settle(60);
    // While the first record is still loading, the reader switches to the second.
    await act(async () => {
      mounted.root.render(
        <RunDetailPanel
          run={{ ...record, detailSelectedEventId: "e2" }}
          tabId={record.sessionId}
          onSelectEvent={() => {}}
          onToggleMaximized={() => {}}
          onClose={() => {}}
        />,
      );
    });
    await settle(80);
    ok(text().includes("FAST-RECORD-BODY"), "stale response: the newly selected record shows its own body");
    releaseFirst!();
    await settle(80);
    ok(!text().includes("SLOW-OLD-RECORD-BODY"), "stale response: the late body of the previous record never appears");
    ok(text().includes("FAST-RECORD-BODY"), "stale response: the current record is still the one shown");
    await mounted.cleanup();
  }

  {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: {
      writeText: async () => { throw new Error("denied"); },
    } });
    const mounted = await renderPanel(run({ events: events(4, index => ({
      status: index === 2 ? "failed" : "completed",
      output: `output ${index + 1}`,
    })) }));
    await interact(() => click(byAria("复制输出")));
    ok(text().includes("复制失败，请重试"), "copy: failure is visible");
    ok(!byAria("复制输出（已复制）"), "copy: failure never claims success");
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => {} } });
    await interact(() => click(byAria("复制输出")));
    ok(Boolean(byAria("复制输出（已复制）")), "copy: retry confirms a successful write");
    await interact(() => click(byAria("仅失败")));
    eq(document.querySelector(".run-detail-row__number")?.textContent, "3", "filter: original record number is preserved");
    await mounted.cleanup();
  }

  {
    resetDetailBodies();
    stubToolResult(async () => ({ args: "", output: "" }));
    const mounted = await renderPanel(run({ events: events(1, () => ({
      callId: "empty-result", archived: true, phase: "result",
    })) }));
    await settle(60);
    ok(text().includes("该调用没有输出"), "empty result: known empty output is explicit");
    ok(!text().includes("未被保留"), "empty result: loaded empty output is not reported as missing");
    await mounted.cleanup();
  }

  // ── stop / retry are offered only when they are real ───────────────────
  {
    const stops: string[] = [];
    const running = await renderPanel(run({ status: "running", events: events(2) }), { onStop: (runId) => stops.push(runId) });
    await interact(() => click([...document.body.querySelectorAll(".run-detail-chip")].find((node) => node.textContent?.includes("停止运行")) ?? null));
    eq(stops.join(","), "run-1", "run actions: a running run can be stopped from the panel");
    await running.cleanup();

    const cancelled = await renderPanel(run({ status: "cancelled", events: events(2) }), { onRetry: (runId) => stops.push(runId) });
    ok(text().includes("已取消"), "run actions: a cancelled run reports itself as cancelled");
    await interact(() => click([...document.body.querySelectorAll(".run-detail-chip")].find((node) => node.textContent?.includes("重试")) ?? null));
    eq(stops[stops.length - 1], "run-1", "run actions: a cancelled run can be retried");
    await cancelled.cleanup();
  }

  process.stdout.write(`\n${passed} tests · ${passed} passed · ${failed} failed\n`);
  if (failed > 0) process.exit(1);
}

main().catch((error) => {
  process.stdout.write(`\nUNCAUGHT: ${error instanceof Error ? error.stack : String(error)}\n`);
  process.exit(1);
});
