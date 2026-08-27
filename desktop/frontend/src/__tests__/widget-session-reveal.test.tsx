// Run: tsx src/__tests__/widget-session-reveal.test.tsx
//
// Executable behavior test for the widget → Assistant surface steering. The
// previous fix subscribed App.tsx to session:activated(widget-open) and hoped
// the event landed in time; it did not converge on real machines. The fix
// routes the intent through the root App's single coordination entry as two
// monotonic signals that MainApp consumes via useAssistantSurfaceSignals.
// This test drives that hook directly and asserts the state convergence that
// the regex-only widget-exit-reveal test could not prove.

import { JSDOM } from "jsdom";
import React, { Activity as ReactActivity, act, useState } from "react";
import { createRoot } from "react-dom/client";
import { useAssistantSurfaceSignals } from "../lib/useAssistantSurface";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  value ? passed++ : failed++;
}

const flushPromises = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

// module-level handles so the test can drive the harness between renders.
const assistantOpenRef: { current: boolean } = { current: true };
const revealedSessions: number[] = [];
const harnessApi: {
  setOpenSignal: (n: number) => void;
  setRevealSignal: (n: number) => void;
  setWidgetMode: (active: boolean) => void;
} = { setOpenSignal: () => {}, setRevealSignal: () => {}, setWidgetMode: () => {} };

function AssistantSurface({ openSignal, revealSignal }: { openSignal: number; revealSignal: number }) {
  const [assistantOpen, setAssistantOpen] = useState(true);
  assistantOpenRef.current = assistantOpen;
  useAssistantSurfaceSignals(
    openSignal,
    revealSignal,
    () => setAssistantOpen(true),
    () => setAssistantOpen(false),
    () => revealedSessions.push(revealSignal),
  );
  return React.createElement("div", { "data-assistant-open": assistantOpen ? "true" : "false" });
}

function Harness() {
  const [openSignal, setOpenSignal] = useState(0);
  const [revealSignal, setRevealSignal] = useState(0);
  const [widgetMode, setWidgetMode] = useState(false);
  harnessApi.setOpenSignal = setOpenSignal;
  harnessApi.setRevealSignal = setRevealSignal;
  harnessApi.setWidgetMode = setWidgetMode;
  return React.createElement(
    ReactActivity,
    { mode: widgetMode ? "hidden" : "visible" },
    React.createElement(AssistantSurface, { openSignal, revealSignal }),
  );
}

async function main() {
  console.log("\nwidget session reveal");
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  globalThis.HTMLElement = dom.window.HTMLElement;

  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);

  await act(async () => {
    root.render(React.createElement(Harness));
    await flushPromises();
  });

  // Initial: main window was in Assistant mode before entering the widget.
  ok(assistantOpenRef.current === true, "initial Assistant surface is open");

  // Plain "打开主窗口" hides and restores MainApp without an intent signal.
  await act(async () => { harnessApi.setWidgetMode(true); await flushPromises(); });
  await act(async () => { harnessApi.setWidgetMode(false); await flushPromises(); });
  ok(assistantOpenRef.current === true, "plain main exit (no signal) keeps the Assistant surface");

  // Reproduce the real lifecycle: the Session intent can arrive while
  // ReactActivity has MainApp hidden. It must be consumed when MainApp becomes
  // visible again instead of being lost like the old runtime event.
  await act(async () => { harnessApi.setWidgetMode(true); await flushPromises(); });
  await act(async () => {
    harnessApi.setRevealSignal(1);
    await flushPromises();
  });
  await act(async () => { harnessApi.setWidgetMode(false); await flushPromises(); });
  ok(assistantOpenRef.current === false, "explicit Session open (reveal signal) collapses the Assistant surface");
  ok(revealedSessions.join(",") === "1", "explicit Session open reconciles the backend active Tab exactly once");

  // A stale/unrelated re-render must not re-apply the already-consumed signal.
  await act(async () => { await flushPromises(); });
  ok(assistantOpenRef.current === false, "re-render with an unchanged reveal signal does not re-collapse or re-open");
  ok(revealedSessions.join(",") === "1", "unchanged reveal signal does not reconcile the active Tab again");

  // The Assistant icon follows the same hidden-to-visible lifecycle and opens
  // the Assistant home after the widget exits.
  await act(async () => { harnessApi.setWidgetMode(true); await flushPromises(); });
  await act(async () => {
    harnessApi.setOpenSignal(1);
    await flushPromises();
  });
  await act(async () => { harnessApi.setWidgetMode(false); await flushPromises(); });
  ok(assistantOpenRef.current === true, "Assistant icon (open signal) opens the Assistant surface");

  await act(async () => { root.unmount(); });
  dom.window.close();

  console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
  if (failed > 0) process.exit(1);
}

await main();
