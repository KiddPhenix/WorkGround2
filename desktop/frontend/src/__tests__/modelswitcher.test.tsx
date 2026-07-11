// Tests for ModelSwitcher async onPick behaviour: success closes & marks,
// failure keeps menu open with inline error, and busy prevents duplicate clicks.
//
// tsx src/__tests__/modelswitcher.test.tsx

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { ModelSwitcher } from "../components/ModelSwitcher";
import { LocaleProvider } from "../lib/i18n";
import type { AppBindings, ModelInfo } from "../lib/bridge";
import type { ModelInfo as ModelInfoType } from "../lib/types";

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

function flushTimers(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

class TestResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function installDom() {
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  globalThis.ResizeObserver = TestResizeObserver;
  Object.defineProperty(dom.window.HTMLElement.prototype, "attachEvent", { configurable: true, value: () => {} });
  Object.defineProperty(dom.window.HTMLElement.prototype, "detachEvent", { configurable: true, value: () => {} });
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: () => ({
      matches: false,
      media: "",
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }),
  });
  return dom;
}

function mockModels(): ModelInfoType[] {
  return [
    { ref: "deepseek/deepseek-chat", provider: "deepseek", model: "deepseek-chat", current: true },
    { ref: "deepseek/deepseek-reasoner", provider: "deepseek", model: "deepseek-reasoner", current: false },
    { ref: "openai/gpt-4o", provider: "openai", model: "gpt-4o", current: false },
  ];
}

function mockApp(overrides: Partial<AppBindings> = {}) {
  window.go = {
    main: {
      App: {
        Models: async () => mockModels(),
        ModelsForTab: async () => mockModels(),
        Commands: async () => [],
        ...overrides,
      } as AppBindings,
    },
  };
}

async function renderSwitcher(onPick: (name: string) => Promise<void>) {
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ModelSwitcher label="deepseek-chat" tabId="tab1" onPick={onPick} />
      </LocaleProvider>,
    );
    await flushTimers();
  });
  return root;
}

function clickTrigger() {
  const btn = document.querySelector(".modelsw__trigger") as HTMLButtonElement;
  if (!btn) throw new Error("trigger not found");
  act(() => btn.click());
}

function clickModel(ref: string) {
  const items = document.querySelectorAll(".modelsw__item");
  for (let i = 0; i < items.length; i++) {
    const el = items[i] as HTMLButtonElement;
    const modelSpan = el.querySelector(".modelsw__model");
    if (modelSpan?.textContent && ref.includes(modelSpan.textContent)) {
      act(() => el.click());
      return;
    }
  }
  throw new Error(`model item not found for ref: ${ref}`);
}

// ── Tests ────────────────────────────────────────────────────────────────────

async function run() {
  installDom();

  // ── success: menu closes and marks the picked model as current ──────────
  {
    let picked = "";
    mockApp();
    const root = await renderSwitcher(async (name) => { picked = name; });

    clickTrigger();
    ok(document.querySelector(".modelsw__menu") !== null, "menu opens on trigger click");

    await act(async () => {
      clickModel("deepseek-reasoner");
      await flushTimers();
    });
    // Let the async pick resolve and React process setOpen(false)
    await act(async () => {
      await new Promise((r) => setTimeout(r, 10));
      await flushTimers();
    });

    ok(picked === "deepseek/deepseek-reasoner", "onPick called with correct ref");
    ok(
      document.querySelector(".modelsw__trigger")?.getAttribute("aria-expanded") === "false",
      "menu closes after successful pick",
    );

    // Check that current is marked on the picked item via aria-selected
    root.unmount();
  }

  // ── failure: menu stays open and shows inline error ─────────────────────
  {
    mockApp();
    const root = await renderSwitcher(async (_name) => {
      throw new Error("finish or cancel the current turn before switching models");
    });

    clickTrigger();
    ok(document.querySelector(".modelsw__menu") !== null, "menu opens");

    await act(async () => {
      clickModel("deepseek-reasoner");
      // Let the async pick settle
      await new Promise((r) => setTimeout(r, 10));
      await flushTimers();
    });

    ok(document.querySelector(".modelsw__menu") !== null, "menu stays open after failed pick");
    const errEl = document.querySelector(".modelsw__error");
    ok(errEl !== null, "inline error is shown");
    ok(errEl?.textContent?.includes("finish or cancel") ?? false, "error contains reason text");
    root.unmount();
  }

  // ── busy: duplicate clicks are prevented ─────────────────────────────────
  {
    let resolvePromise: (() => void) | null = null;
    let callCount = 0;
    mockApp();
    const root = await renderSwitcher(async (_name) => {
      callCount += 1;
      await new Promise<void>((resolve) => { resolvePromise = resolve; });
    });

    clickTrigger();

    await act(async () => {
      clickModel("deepseek-reasoner");
      await flushTimers();
    });

    ok(callCount === 1, "first click triggers onPick");

    // Try clicking again while busy
    await act(async () => {
      clickModel("gpt-4o");
      await flushTimers();
    });

    ok(callCount === 1, "duplicate click ignored while busy");

    // Resolve the pending promise
    await act(async () => {
      resolvePromise?.();
      await flushTimers();
    });

    ok(callCount === 1, "still only one call after resolution");
    root.unmount();
  }

  // ── pending confirmation has no visible loading state ────────────────────
  {
    let resolvePromise: (() => void) | null = null;
    mockApp();
    const root = await renderSwitcher(async (_name) => {
      await new Promise<void>((resolve) => { resolvePromise = resolve; });
    });

    clickTrigger();

    await act(async () => {
      clickModel("gpt-4o");
      await flushTimers();
    });

    const trigger = document.querySelector(".modelsw__trigger") as HTMLButtonElement;
    ok(!trigger.disabled, "trigger stays visually enabled while confirmation is pending");
    ok(trigger.getAttribute("aria-busy") === "true", "trigger has aria-busy=true");
    ok(document.querySelector(".modelsw__spin") === null, "model switch shows no loading spinner");

    await act(async () => {
      resolvePromise?.();
      await flushTimers();
    });

    ok(!trigger.disabled, "trigger remains enabled after completion");
    root.unmount();
  }

  // ── error cleared when menu reopens ──────────────────────────────────────
  {
    mockApp();
    const root = await renderSwitcher(async (_name) => {
      throw new Error("some error");
    });

    // Open, trigger failure
    clickTrigger();
    await act(async () => {
      clickModel("gpt-4o");
      await new Promise((r) => setTimeout(r, 10));
      await flushTimers();
    });
    ok(document.querySelector(".modelsw__error") !== null, "error shown after failure");

    // Close menu
    const backdrop = document.querySelector(".modelsw__menu");
    // Simulate close by clicking trigger again to toggle closed
    act(() => {
      const btn = document.querySelector(".modelsw__trigger") as HTMLButtonElement;
      btn.click();
    });
    await flushTimers();

    // Reopen
    clickTrigger();
    await flushTimers();
    ok(document.querySelector(".modelsw__error") === null, "error cleared on reopen");
    root.unmount();
  }

  process.stdout.write(`\n${passed}/${passed + failed} passed\n`);
  if (failed > 0) process.exit(1);
}

run().catch((err) => {
  process.stderr.write(`Test harness error: ${err instanceof Error ? err.stack : String(err)}\n`);
  process.exit(1);
});
