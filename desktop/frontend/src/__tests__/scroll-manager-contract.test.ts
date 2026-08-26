// Run: tsx src/__tests__/scroll-manager-contract.test.ts

import { JSDOM } from "jsdom";
import React, { useRef } from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import {
  isVerticalScrollbarPointer,
  resolveScrollElement,
  shouldAutoScroll,
  shouldAutoScrollForQuestionChange,
  snapElementToBottom,
  useScrollManager,
  type QuestionScrollSnapshot,
} from "../lib/useScrollManager";

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

function snap(count: number, lastId = ""): QuestionScrollSnapshot {
  return { count, lastId };
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

console.log("\nscroll manager contract");

ok(
  !shouldAutoScrollForQuestionChange(snap(0), snap(12, "u12")),
  "restored history can seed the tracker without a synthetic new-question scroll",
);
ok(
  shouldAutoScrollForQuestionChange(snap(12, "u12"), snap(13, "u13")),
  "appended user question scrolls to the bottom",
);
ok(
  !shouldAutoScrollForQuestionChange(snap(12, "u12"), snap(18, "u12")),
  "prepended older history does not scroll to the bottom",
);
ok(
  !shouldAutoScrollForQuestionChange(snap(12, "u12"), snap(12, "u12")),
  "unchanged transcript does not scroll",
);
ok(
  !shouldAutoScrollForQuestionChange(snap(12, "u12"), snap(8, "u8")),
  "replaced or rewound transcript does not use question tracking to scroll",
);

const inner = { scrollTop: 0, scrollHeight: 120 } as HTMLElement;
const workbenchHost = { scrollTop: 0, scrollHeight: 960 } as HTMLElement;
const resolved = resolveScrollElement(inner, workbenchHost);
ok(resolved === workbenchHost, "workbench outer viewport is the single scroll owner");
if (resolved) snapElementToBottom(resolved);
ok(workbenchHost.scrollTop === 960, "session restore bottom-anchors the outer viewport");
ok(inner.scrollTop === 0, "session restore does not scroll the overflow-visible inner transcript");

const scrollbarHost = {
  clientHeight: 400,
  clientWidth: 788,
  clientLeft: 0,
  scrollHeight: 1200,
  getBoundingClientRect: () => ({ left: 0, right: 800, top: 0, bottom: 400 }),
} as HTMLElement;
ok(isVerticalScrollbarPointer(scrollbarHost, 795, 200), "pointer inside the vertical scrollbar starts drag protection");
ok(!isVerticalScrollbarPointer(scrollbarHost, 600, 200), "pointer inside transcript content does not start drag protection");
ok(!shouldAutoScroll(true, true), "streaming content cannot auto-scroll while the scrollbar is held");
ok(!shouldAutoScroll(false, true, true), "forced new-question scrolling is also blocked during a scrollbar drag");
ok(shouldAutoScroll(true, false), "auto-scroll resumes after release only when the viewport remains sticky");
ok(!shouldAutoScroll(false, false), "release away from the bottom preserves the reader's position");

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);

let manager: ReturnType<typeof useScrollManager> | null = null;
function Harness() {
  const hostRef = useRef<HTMLDivElement>(null);
  manager = useScrollManager(hostRef);
  return React.createElement("div", { ref: hostRef, "data-testid": "scroll-host" });
}

const rootElement = document.getElementById("root");
if (!rootElement) throw new Error("missing root");
const root = createRoot(rootElement);
await act(async () => { root.render(React.createElement(Harness)); });
const renderedHost = document.querySelector<HTMLElement>("[data-testid='scroll-host']");
if (!renderedHost || !manager) throw new Error("scroll manager harness did not render");
const scrollManager = manager as unknown as ReturnType<typeof useScrollManager>;
Object.defineProperties(renderedHost, {
  clientHeight: { configurable: true, value: 400 },
  clientWidth: { configurable: true, value: 788 },
  scrollHeight: { configurable: true, value: 1200 },
});
renderedHost.getBoundingClientRect = () => ({ left: 0, right: 800, top: 0, bottom: 400 }) as DOMRect;

scrollManager.stick.current = false;
await act(async () => {
  renderedHost.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0, clientX: 795, clientY: 200 }));
  scrollManager.onNewQuestion();
});
ok(!scrollManager.stick.current, "new-question handler preserves non-sticky state while the native scrollbar is held");
await act(async () => { window.dispatchEvent(new MouseEvent("mouseup", { bubbles: true })); });
ok(!scrollManager.stick.current, "releasing away from the bottom keeps automatic following disabled");

renderedHost.scrollTop = 800;
await act(async () => {
  renderedHost.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0, clientX: 795, clientY: 200 }));
  window.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
});
ok(scrollManager.stick.current, "releasing at the bottom restores automatic following");

// Wheel gestures are continuous and land slightly before the resulting scroll
// event, so auto-follow must hold off for the whole gesture and then recover
// from the real scroll position — the same contract a scrollbar drag already
// gets. Without this, streaming content pulls the viewport back to the bottom
// and makes the wheel scroll flicker.
renderedHost.scrollTop = 800;
scrollManager.stick.current = true;
await act(async () => {
  renderedHost.dispatchEvent(new dom.window.Event("wheel", { bubbles: true }));
});
ok(scrollManager.wheelScrolling.current, "wheel gesture marks the viewport as actively scrolling");
renderedHost.scrollTop = 500; // user scrolls away from the bottom mid-gesture
await act(async () => { scrollManager.scrollToBottomAfterLayout(1); });
ok(renderedHost.scrollTop === 500, "auto-follow cannot snap back while the wheel gesture is active");
await act(async () => { await sleep(200); });
ok(!scrollManager.wheelScrolling.current, "wheel gesture settles and releases the scroll lock");
ok(!scrollManager.stick.current, "settling away from the bottom keeps automatic following disabled");

renderedHost.scrollTop = 800;
scrollManager.stick.current = false;
await act(async () => {
  renderedHost.dispatchEvent(new dom.window.Event("wheel", { bubbles: true }));
});
await act(async () => { await sleep(200); });
ok(scrollManager.stick.current, "settling at the bottom resumes automatic following");

await act(async () => { root.unmount(); });
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
