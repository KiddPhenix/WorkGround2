// Run: npm.cmd exec -- tsx src/__tests__/welcome-adaptive.test.tsx

import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { AdaptiveWelcomeView } from "../components/Welcome";
import { LocaleProvider } from "../lib/i18n";
import { buildWelcomeModel, selectWelcomeDelight, type WelcomeModel, type WelcomePolicyInput } from "../lib/welcome";
import type { WorkspaceWelcomeView } from "../lib/types";

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

const tt = (key: string, vars?: Record<string, string | number>) => {
  if (!vars) return key;
  return `${key}:${Object.values(vars).join("|")}`;
};

function view(kinds: string[], patch: Partial<WorkspaceWelcomeView> = {}): WorkspaceWelcomeView {
  return {
    workspaceName: "Example",
    scope: "project",
    contentKinds: kinds,
    confidence: 0.9,
    fileCount: 10,
    changedCount: 0,
    sessionCount: 0,
    scannedAt: Date.now(),
    ...patch,
  };
}

function input(workspace: WorkspaceWelcomeView, patch: Partial<WelcomePolicyInput> = {}): WelcomePolicyInput {
  return {
    view: workspace,
    fallbackName: "Example",
    scope: "project",
    loadState: "ready",
    globalVisits: 1,
    workspaceVisits: 1,
    delightEnabled: true,
    reducedMotion: false,
    ...patch,
  };
}

console.log("\nadaptive welcome policy");

const expectedPrimary: Record<string, string> = {
  code: "inspect-code",
  docs: "organize-docs",
  data: "inspect-data",
  media: "organize-media",
  research: "synthesize-research",
  empty: "set-goal",
};
for (const [kind, id] of Object.entries(expectedPrimary)) {
  const model = buildWelcomeModel(input(view([kind])), tt);
  ok(model.primary.id === id, `${kind} workspace selects ${id}`);
  ok(model.secondary.length <= 2, `${kind} keeps at most two supporting actions`);
}

const mixed = buildWelcomeModel(input(view(["docs", "media"])), tt);
ok(mixed.primary.id === "understand-mixed", "multi-label workspace uses mixed policy");

const returning = buildWelcomeModel(input(view(["docs"], { sessionCount: 8, recentTitle: "Previous work" }), { globalVisits: 20, workspaceVisits: 8 }), tt);
ok(returning.primary.id === "continue", "familiar workspace offers continuation without hiding help");
ok(returning.experienced && returning.familiar, "familiarity axes are independent policy inputs");

const degraded = buildWelcomeModel(input(view(["code"], { degraded: true }), { workspaceVisits: 20 }), tt);
ok(degraded.statusTone === "warning" && !degraded.delight, "real degraded state suppresses delight");

const microInput = input(view(["code"]), { workspaceVisits: 20 });
ok(selectWelcomeDelight(microInput, "code", tt)?.kind === "microglitch", "MicroGlitch defaults on at the twentieth visit");
ok(selectWelcomeDelight({ ...microInput, reducedMotion: true }, "code", tt)?.kind !== "microglitch", "reduced motion suppresses MicroGlitch");
ok(selectWelcomeDelight({ ...microInput, workspaceVisits: 3 }, "code", tt) === undefined, "first three visits suppress delight");

console.log("\nadaptive welcome component");

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.Element = dom.window.Element;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.MouseEvent = dom.window.MouseEvent;

const model: WelcomeModel = {
  workspaceName: "Example",
  summary: "Workspace summary",
  primary: { id: "primary", label: "Continue", prompt: "continue prompt" },
  secondary: [
    { id: "understand", label: "Understand", prompt: "understand prompt" },
    { id: "new", label: "New direction", prompt: "" },
  ],
  status: "Fresh",
  statusTone: "normal",
  experienced: true,
  familiar: true,
};
const drafts: string[] = [];
const root = createRoot(document.getElementById("root")!);
act(() => {
  root.render(
    <LocaleProvider>
      <AdaptiveWelcomeView model={model} onDraft={(text) => drafts.push(text)} />
    </LocaleProvider>,
  );
});

const primary = document.querySelector<HTMLButtonElement>(".welcome-adaptive__primary");
const secondary = [...document.querySelectorAll<HTMLButtonElement>(".welcome-adaptive__secondary button")];
ok(Boolean(primary), "renders exactly one primary action surface");
ok(secondary.length === 2, "renders no more than two supporting actions");
act(() => primary?.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
act(() => secondary[1]?.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true })));
ok(drafts[0] === "continue prompt", "primary action fills the composer draft callback");
ok(drafts[1] === "", "new direction preserves a clean composer draft");

act(() => root.unmount());

// retry interaction
const retryDom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = retryDom.window as unknown as Window & typeof globalThis;
globalThis.document = retryDom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: retryDom.window.navigator });
globalThis.Node = retryDom.window.Node;
globalThis.Element = retryDom.window.Element;
globalThis.HTMLElement = retryDom.window.HTMLElement;
globalThis.MouseEvent = retryDom.window.MouseEvent;

const retries: string[] = [];
const retryRoot = createRoot(document.getElementById("root")!);
act(() => {
  retryRoot.render(
    <LocaleProvider>
      <AdaptiveWelcomeView model={model} onDraft={() => {}} onRetry={() => retries.push("retry")} canRetry={true} />
    </LocaleProvider>,
  );
});
const retryBtn = document.querySelector<HTMLButtonElement>(".welcome-adaptive__status button");
ok(Boolean(retryBtn), "degraded/error state shows a retry button");
act(() => retryBtn?.dispatchEvent(new retryDom.window.MouseEvent("click", { bubbles: true })));
ok(retries[0] === "retry", "retry button calls onRetry callback");
act(() => retryRoot.unmount());

process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
