// Run: node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/block-popups.test.ts
// Pure-function tests for the once-per-revision blocked-session reminder queue:
// first snapshot establishes watermarks (no restart replay), a NEW
// needs_input/needs_confirm notice is queued exactly once, "稍后处理" (consume)
// never re-pops the same notice/revision, a changed pending (new revision)
// re-raises the reminder, and multiple blocks pop in the backend's existing
// priority/age order without stealing focus repeatedly.
import { consumeBlockPopup, newBlockPopupState, reconcileBlockPopups } from "../components/widget/blockPopups";
import type { DesktopIconItem, DesktopIconNotice } from "../lib/bridge";
import { readFileSync } from "node:fs";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

const notice = (overrides: Partial<DesktopIconNotice>): DesktopIconNotice => ({
  id: "ask:tab:ask-1", revision: "rev-1", kind: "needs_input", priority: 1, title: "等待回复",
  body: "选择语言", createdAt: 100, tabId: "tab", interactionId: "ask-1", options: [], ...overrides,
});

const item = (overrides: Partial<DesktopIconItem> & { notices?: DesktopIconNotice[] } = {}): DesktopIconItem => ({
  id: "task:tab", kind: "task", sourceId: "tab", title: "任务", status: "needs_input", unreadCount: 1,
  notifications: overrides.notices ?? [], position: { row: "bottom", zone: "running", order: 0 }, revision: "r",
  ...overrides,
});

console.log("\nblocked-session popup reminders");

// First snapshot only establishes watermarks: a widget restart never replays
// a session that was already blocked before the widget came up.
{
  const state = reconcileBlockPopups(newBlockPopupState(), [
    item({ notices: [notice({})] }),
  ]);
  ok(state.initialized === true, "first snapshot initializes the reminder state");
  ok(state.queue.length === 0, "restart-existing blocks are watermarked, not queued");
  const again = reconcileBlockPopups(state, [item({ notices: [notice({})] })]);
  ok(again.queue.length === 0, "watermarked block stays quiet on later snapshots");
}

// A NEW needs_input block that appears after initialization is queued once,
// in the backend's priority/age order, and consume pops it exactly once.
{
  let state = reconcileBlockPopups(newBlockPopupState(), [item()]);
  state = reconcileBlockPopups(state, [item({ notices: [notice({})] })]);
  ok(state.queue.length === 1, "new needs_input block is queued");
  ok(state.queue[0].itemId === "task:tab" && state.queue[0].noticeId === "ask:tab:ask-1" && state.queue[0].revision === "rev-1", "candidate carries item/notice/revision");

  const consumed = consumeBlockPopup(state);
  ok(consumed.candidate?.noticeId === "ask:tab:ask-1", "consume pops the queued block");
  ok(consumed.state.queue.length === 0, "consume empties the queue");
  const again = reconcileBlockPopups(consumed.state, [item({ notices: [notice({})] })]);
  ok(again.queue.length === 0, "same notice/revision never re-pops after 稍后处理");

  // The same block after an answer attempt (or the session re-asking) carries
  // a new revision, so the reminder is raised again — but only once more.
  const changed = reconcileBlockPopups(again, [item({ notices: [notice({ revision: "rev-2" })] })]);
  ok(changed.queue.length === 1 && changed.queue[0].revision === "rev-2", "a changed pending (new revision) re-raises the reminder once");
}

// needs_confirm blocks and multiple blocked sessions queue in order; message /
// completed notices never queue.
{
  let state = reconcileBlockPopups(newBlockPopupState(), [item()]);
  state = reconcileBlockPopups(state, [
    item({ id: "task:confirm", sourceId: "c", title: "确认", notices: [notice({ id: "approval:tab:x", interactionId: "x", kind: "needs_confirm", title: "需要确认", createdAt: 200 })] }),
    item({ id: "task:older", sourceId: "o", title: "旧任务", notices: [notice({ id: "ask:tab:ask-old", interactionId: "old", createdAt: 50 })] }),
    item({ id: "task:msg", sourceId: "m", title: "消息", notices: [notice({ id: "m1", kind: "message", priority: 3, title: "新消息" })] }),
    item({ id: "task:done", sourceId: "d", title: "完成", notices: [notice({ id: "c1", kind: "completed", priority: 2, title: "任务完成" })] }),
  ]);
  ok(state.queue.length === 2, "only blocking notices are queued");
  ok(state.queue[0].itemId === "task:older" && state.queue[1].itemId === "task:confirm", "older block pops before the newer one");
}

// A blocked session whose icon disappears drops its queued candidate instead
// of popping a ghost popup later.
{
  let state = reconcileBlockPopups(newBlockPopupState(), [item()]);
  state = reconcileBlockPopups(state, [item({ notices: [notice({})] })]);
  ok(state.queue.length === 1, "block queued while visible");
  const without = reconcileBlockPopups(state, []);
  ok(without.queue.length === 0, "hidden item drops its queued reminder");
}
// Old snapshots without any blocking notice never queue anything.
{
  const state = reconcileBlockPopups(newBlockPopupState(), [item({ status: "idle", notices: [] })]);
  ok(state.initialized === true && state.queue.length === 0, "idle snapshot initializes without queueing");
}

// The one-shot popup is backed by a persistent visual state on the icon; after
// consume/later, the user can still see exactly which Session is blocked.
{
  const mode = readFileSync(new URL("../components/widget/DesktopIconMode.tsx", import.meta.url), "utf8");
  const css = readFileSync(new URL("../components/widget/desktop-icon-mode.css", import.meta.url), "utf8");
  ok(mode.includes('const blockLabel = item.status === "needs_input" ? "待回答" : item.status === "needs_confirm" ? "待确认" : "";'), "blocked sessions derive a persistent text label");
  ok(mode.includes("blockLabel && <span className={`desktop-icon__block-state"), "blocked sessions render the text label outside the popup");
  ok(/\.desktop-icon__block-state\s*\{[^}]*display:\s*block[^}]*font-size:\s*9px/.test(css), "blocked-session text remains visible beneath the icon");
}

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
