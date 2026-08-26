// Run: tsx src/__tests__/widget-exit-reveal.test.ts

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { readFileSync } from "node:fs";
import { gsap } from "gsap";
import { ScrollToPlugin } from "gsap/ScrollToPlugin";
import { useScrollManager } from "../lib/useScrollManager";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  value ? passed++ : failed++;
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function main() {
  console.log("\nwidget exit reveal");
  const appSource = readFileSync(new URL("../App.tsx", import.meta.url), "utf8");
  const controllerSource = readFileSync(new URL("../lib/useController.ts", import.meta.url), "utf8");
  const workspaceSource = readFileSync(new URL("../collab/CollaborationWorkspace.tsx", import.meta.url), "utf8");

  // ── Source contract: the reveal intent fires only on a real widget exit ──
  ok(
    /const \[roomRevealSignal, setRoomRevealSignal\] = useState\(0\);/.test(appSource),
    "Room reveal signal is a monotonic counter like transcriptRevealSignal",
  );
  ok(
    /wasWidgetActiveRef\.current\s*=\s*widgetActive/.test(appSource) &&
      /if \(was && !widgetActive\)/.test(appSource) &&
      /setTranscriptRevealSignal\(\(value\) => value \+ 1\)/.test(appSource) &&
      /setRoomRevealSignal\(\(value\) => value \+ 1\)/.test(appSource),
    "App emits session + room reveal intents only on the widget true→false transition (initial false never bumps)",
  );
  ok(
    (appSource.match(/revealSignal=\{roomRevealSignal\}/g) || []).length === 2,
    "both Room render sites (workbench + classic) receive the room reveal signal",
  );
  ok(
    workspaceSource.includes("revealSignal = 0") &&
      workspaceSource.includes("scrollTimelineToBottomAfterLayout") &&
      /if \(revealSignal <= 0\) return;/.test(workspaceSource),
    "CollaborationWorkspace repins its own .collab-scroll owner on a reveal signal",
  );
  ok(
    /<ReactActivity mode=\{widgetMode \? "hidden" : "visible"\}>[\s\S]{0,500}<MainApp/.test(appSource) &&
      controllerSource.includes("const off = onEvent((e) => {") &&
      controllerSource.includes("void syncActiveTabFromBackend(false, true).then((tabId) => {"),
    "Activity preserves MainApp while hidden and reactivation re-synchronizes authoritative backend state",
  );

  // ── Opening a task from the widget collapses the Assistant surface ──
  // The backend emits session:activated(widget-open) only after a successful
  // ExitWidgetMode(tabID); plain 打开主窗口 exits (no tabID) and failed
  // activations never emit it. The frontend reuses that explicit event as the
  // switch basis so the activated Session is visible even when the main
  // window was in Assistant mode before entering the widget.
  ok(
    /import \{ app, onEvent, onProjectTreeChanged, onSessionActivated \} from "\.\/lib\/bridge";/.test(appSource),
    "App imports onSessionActivated for the widget-open activation event",
  );
  ok(
    /return onSessionActivated\(\(event\) => \{[\s\S]{0,500}if \(event\.reason !== "widget-open"\) return;[\s\S]{0,300}setAssistantOpen\(false\)[\s\S]{0,200}\}\);[\s\S]{0,160}\}, \[closeTransientOverlays\]\);/.test(appSource),
    "session:activated(widget-open) collapses the Assistant surface so the activated Session is visible",
  );
  ok(
    /if \(event\.reason !== "widget-open"\) return;[\s\S]{0,160}closeTransientOverlays\(\);[\s\S]{0,160}setAssistantOpen\(false\)/.test(appSource),
    "only the widget-open reason closes the Assistant surface; plain widget exits and other activation reasons keep it untouched",
  );
  ok(
    /useEffect\(\(\) => \{\s*return onSessionActivated\(/.test(appSource),
    "the session activation subscription returns its unsubscribe so re-mounts clean up (StrictMode-safe)",
  );

  // ── Behavioral: the multi-frame repin used by the reveal intent pins a
  // previously-scrolled timeline back to the latest messages after layout,
  // even when the reader had scrolled away from the bottom. ──
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  // snapToBottom uses gsap.killTweensOf, which only exists after the gsap core
  // is initialized (production registers plugins at startup).
  gsap.registerPlugin(ScrollToPlugin);

  let manager: ReturnType<typeof useScrollManager> | null = null;
  function Harness() {
    const mgr = useScrollManager();
    manager = mgr;
    // Mirror CollaborationWorkspace: the manager's own internal ref is the
    // .collab-scroll scroll owner.
    return React.createElement("div", { ref: mgr.scrollRef, className: "collab-scroll" });
  }

  const rootElement = document.getElementById("root");
  if (!rootElement) throw new Error("missing root");
  const root = createRoot(rootElement);
  await act(async () => { root.render(React.createElement(Harness)); });
  const timeline = document.querySelector<HTMLElement>(".collab-scroll");
  if (!timeline || !manager) throw new Error("scroll manager harness did not render");
  Object.defineProperties(timeline, {
    clientHeight: { configurable: true, value: 400 },
    scrollHeight: { configurable: true, value: 2400 },
  });
  const scrollManager = manager as unknown as ReturnType<typeof useScrollManager>;

  timeline.scrollTop = 600; // old position kept while the Room was hidden
  scrollManager.stick.current = false; // the reader had scrolled away from the bottom
  await act(async () => {
    scrollManager.scrollToBottomAfterLayout(3);
    await sleep(80); // let the multi-frame layout repin run
  });
  ok(timeline.scrollTop === 2400, "reveal repin pins the .collab-scroll owner to the latest messages after layout");

  // Normal user scrolling must keep working after the forced reveal.
  timeline.scrollTop = 600;
  await act(async () => { scrollManager.onScroll(); });
  ok(!scrollManager.stick.current, "manual scroll after the reveal still disables auto-follow");
  await act(async () => { root.unmount(); });
  dom.window.close();

  console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
  if (failed > 0) process.exit(1);
}

await main();
