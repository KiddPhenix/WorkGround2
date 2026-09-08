// Run: node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/steer-visibility.test.tsx
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { JSDOM } from "jsdom";
import { Transcript } from "../components/Transcript";
import { LocaleProvider } from "../lib/i18n";
import { buildStepGroups, buildTurnGroups } from "../lib/transcriptGrouping";
import { initialState, reducer, type Item } from "../lib/useController";
import type { HistoryMessage, Meta } from "../lib/types";

const path = "/repo/session.jsonl";
const guidance = "再等一分钟\n然后回复 hello";
const notice: Item = { kind: "notice", id: "s3", level: "info", text: `↪ ${guidance}` };
const user: Item = { kind: "user", id: "u0", text: "等待两分钟" };
const tool: Item = { kind: "tool", id: "t2", name: "bash", args: "{}", readOnly: false, status: "running" };
const assistant: Item = { kind: "assistant", id: "a1", text: "", reasoning: "等待完成", streaming: false };
const history: HistoryMessage[] = [{ role: "user", content: user.text }];
const accepted = reducer({ ...initialState, meta: { sessionPath: path } as Meta, items: [user, assistant, tool], seq: 3, running: true, turnActive: true }, {
  type: "event", e: { kind: "steer", text: guidance },
});
const notices = (items: Item[]) => items.filter((item) => item.kind === "notice" && item.text === notice.text);

const groups = buildStepGroups(accepted.items);
assert.equal(groups[groups.length - 1]?.items[0].kind, "notice", "guidance is a standalone step");
assert.equal(groups[groups.length - 2]?.isComplete, false, "guidance does not complete a running command");
assert.equal(buildTurnGroups(accepted.items).length, 1, "guidance does not create a rewind/fork turn");

const storage = new JSDOM("", { url: "http://localhost" }).window.localStorage;
Object.defineProperty(globalThis, "localStorage", { configurable: true, value: storage });
const internalNotices: Extract<Item, { kind: "notice" }>[] = [
  "work: background recovery finished · recovered=9 scanned=14",
  "work: feature enabled",
  'vision delegate: auto-discovered "local-codex/gpt-5.5" for image analysis',
].map((text, index) => ({ kind: "notice", id: `n${index}`, level: "info", text }));
for (const informationMode of [false, true]) {
  storage.setItem("WorkGround2-display-mode", "compact");
  const scenarios: Item[][] = [internalNotices, [user, assistant, tool, ...internalNotices, notice]];
  for (const items of scenarios) {
    const markup = renderToStaticMarkup(createElement(LocaleProvider, null,
      createElement(Transcript, { items, onPrompt: () => {}, informationMode })));
    const doc = new JSDOM(markup).window.document;
    for (const item of internalNotices) {
      assert.equal(doc.body.textContent?.includes(item.text), false, "startup diagnostics stay hidden");
    }
    assert.equal(doc.querySelectorAll(".notice-line").length, 0, "diagnostic lines stay hidden");
    const bubbles = [...doc.querySelectorAll(".msg--user")].filter((node) => node.textContent?.includes(guidance));
    assert.equal(bubbles.length, items === internalNotices ? 0 : 1,
      "user guidance renders as a user bubble, including sessions with no user turn");
  }
}
for (const running of [true, false]) {
  const items: Item[] = [user, assistant, { ...tool, status: running ? "running" : "done" }, notice];
  if (!running) items.push({ ...assistant, id: "a4", text: "hello" });
  for (const mode of ["standard", "compact", "information"]) {
    storage.setItem("WorkGround2-display-mode", mode === "compact" ? "compact" : "standard");
    const informationMode = mode === "information";
    const markup = renderToStaticMarkup(createElement(LocaleProvider, null,
      createElement(Transcript, { items, onPrompt: () => {}, running, informationMode })));
    const doc = new JSDOM(markup).window.document;
    const shown = [...doc.querySelectorAll(".msg--user")].filter((node) => node.textContent?.includes(guidance));
    assert.equal(shown.length, 1, `guidance visible once (running=${running}, mode=${mode})`);
    assert.equal(shown[0].closest(".turn-collapse"), null, "guidance stays outside collapsed process details");
    assert.equal(shown[0].classList.contains("msg--guidance"), true, "guidance has its own subtle bubble style");
    assert.equal(shown[0].querySelectorAll(".msg__guidance-label").length, 1, "guidance bubble has one small label");
    assert.equal(doc.querySelectorAll(".msg--user:not(.msg--guidance) .msg__guidance-label").length, 0, "ordinary user bubbles keep their existing style");
    assert.equal(shown[0].textContent?.includes("↪"), false, "guidance bubble omits the internal notice marker");
    assert.equal(shown[0].querySelectorAll("button:not(:disabled)").length, 1, "guidance can be copied without exposing turn editing or pinning");
  }
}

const refreshed = reducer(accepted, { type: "history", messages: history, sessionPath: path });
assert.equal(notices(refreshed.items).length, 1, "history refresh retains accepted but unpersisted guidance");
const reloaded = reducer(accepted, { type: "reset", queueSessionPath: path });
assert.equal(notices(reloaded.items).length, 1, "same-session reset keeps guidance until hydration");
const switched = reducer(accepted, { type: "reset", queueSessionPath: "/repo/other.jsonl" });
assert.equal(notices(switched.items).length, 0, "guidance never leaks into another session");
assert.equal(notices(reducer(accepted, { type: "reset", dropQueued: true }).items).length, 0, "new session drops old guidance");

const repeated = reducer(accepted, { type: "event", e: { kind: "steer", text: guidance } });
let persisted = reducer(repeated, { type: "history", sessionPath: path, messages: [...history, { role: "notice", content: notice.text }] });
assert.equal(notices(persisted.items).length, 2, "partial persistence preserves legitimate identical submissions");
const messages = [...history, { role: "notice", content: notice.text }, { role: "notice", content: notice.text }];
persisted = reducer(persisted, { type: "history", sessionPath: path, messages });
persisted = reducer(persisted, { type: "history", sessionPath: path, messages });
assert.equal(notices(persisted.items).length, 2, "repeated hydration does not duplicate persisted guidance");
const page = { messages: history, startTurn: 0, endTurn: 1, totalTurns: 1, hasOlder: false, sessionPath: path };
assert.equal(notices(reducer(accepted, { type: "history_page", mode: "replace", page }).items).length, 1, "paged history also preserves pending guidance");
assert.equal(notices(reducer(accepted, { type: "history", messages: history, sessionPath: "/repo/other.jsonl" }).items).length, 0, "history replacement cannot carry guidance across session paths");
assert.equal(notices(reducer(persisted, { type: "history_page", mode: "replace", page }).items).length, 0, "historical notices outside the new page are not resurrected");
console.log("PASS steer visibility: running/completed rendering, turn boundaries, hydration, session isolation, repeated submissions");
