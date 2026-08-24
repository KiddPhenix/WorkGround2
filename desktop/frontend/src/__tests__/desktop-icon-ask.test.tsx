// Run: node --import tsx --import ./scripts/test-asset-hook.mjs src/__tests__/desktop-icon-ask.test.tsx
// jsdom render test for the desktop-icon structured ask flow: per-question
// navigation (header/progress/back/next/submit), single/multi option handling,
// custom answers, and the one-shot batch answer payload.
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { AskFlow } from "../components/widget/desktopIconAsk";
import type { QuestionAnswer } from "../lib/types";
import type { WidgetQuestion } from "../lib/bridge";

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

function eq(actual: unknown, expected: unknown, label: string) {
  if (JSON.stringify(actual) === JSON.stringify(expected)) ok(true, label);
  else ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

function installDom() {
  const dom = new JSDOM("<!doctype html><html><head></head><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.Element = dom.window.Element;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.HTMLInputElement = dom.window.HTMLInputElement;
  globalThis.HTMLButtonElement = dom.window.HTMLButtonElement;
  globalThis.Event = dom.window.Event;
  globalThis.KeyboardEvent = dom.window.KeyboardEvent;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  return dom;
}

const questions: WidgetQuestion[] = [
  {
    id: "q1", header: "语言", prompt: "选择语言",
    options: [
      { label: "Go", value: "Go" }, { label: "Rust", value: "Rust" },
      { label: "TypeScript", value: "TypeScript" }, { label: "Python", value: "Python" },
    ],
  },
  {
    id: "q2", header: "能力", prompt: "需要哪些能力", multi: true,
    options: [
      { label: "搜索", value: "搜索" }, { label: "测试", value: "测试" }, { label: "发布", value: "发布" },
    ],
  },
];

function byText(text: string): HTMLElement | null {
  return [...document.querySelectorAll<HTMLElement>("button, span, p")].find((el) => el.textContent?.trim() === text) ?? null;
}

function click(button: HTMLElement | null, label: string) {
  ok(button !== null, `${label}: button exists`);
  if (!button) return;
  act(() => button.click());
}

function setInput(input: HTMLInputElement | null, value: string, label: string) {
  ok(input !== null, `${label}: input exists`);
  if (!input) return;
  act(() => {
    // React's onChange needs the native setter plus a _valueTracker sync so
    // the synthetic event is treated as a real user change (same pattern as
    // artifact-shelf-scale.test.tsx).
    const previous = input.value;
    Object.getOwnPropertyDescriptor(globalThis.HTMLInputElement.prototype, "value")?.set?.call(input, value);
    (input as HTMLInputElement & { _valueTracker?: { setValue: (next: string) => void } })._valueTracker?.setValue(previous);
    input.dispatchEvent(new globalThis.Event("input", { bubbles: true }));
  });
  ok(input.value === value, `${label}: value applied`);
}

console.log("\ndesktop icon ask flow");

{
  const dom = installDom();
  const submitted: QuestionAnswer[][] = [];
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  let scenario = 0;
  const mount = async () => {
    // A fresh key remounts AskFlow per scenario, exactly like the popup keys
    // NoticeBody by notice id — state never leaks between questions/asks.
    scenario += 1;
    await act(async () => {
      root.render(React.createElement(AskFlow, { key: `scenario-${scenario}`, questions, busy: false, onAnswer: (answers) => submitted.push(answers) }));
      await Promise.resolve();
    });
  };
  await mount();

  eq(byText("语言")?.textContent, "语言", "first question header renders");
  eq(byText("1/2")?.textContent, "1/2", "multi-question progress renders");
  eq(document.querySelectorAll(".desktop-icon-popup__answers > button").length, 4, "all 4 options of question 1 render");
  ok(byText("返回") === null, "back is hidden on the first question");
  const nextDisabled = (document.querySelector(".desktop-icon-popup__ask-next") as HTMLButtonElement | null)?.disabled;
  ok(nextDisabled === true, "next stays disabled until the question is answered");

  click(byText("Rust"), "select Rust");
  const next = document.querySelector(".desktop-icon-popup__ask-next") as HTMLButtonElement | null;
  ok(next?.disabled === false, "next enables after a single-select pick");
  click(next, "next to question 2");

  eq(byText("能力")?.textContent, "能力", "second question header renders");
  eq(byText("2/2")?.textContent, "2/2", "progress advances to 2/2");
  ok(byText("返回") !== null, "back is available on later questions");
  eq(document.querySelectorAll(".desktop-icon-popup__answers > button").length, 3, "question 2 shows its 3 options");
  click(byText("搜索"), "toggle multi option 搜索");
  click(byText("发布"), "toggle multi option 发布");
  const submit = document.querySelector(".desktop-icon-popup__ask-next") as HTMLButtonElement | null;
  eq(submit?.textContent?.trim(), "提交", "last question shows 提交 instead of 下一题");
  ok(submit?.disabled === false, "submit enables after multi answers");
  click(submit, "submit batch");

  eq(submitted.length, 1, "batch submitted exactly once");
  eq(submitted[0], [
    { questionId: "q1", selected: ["Rust"] },
    { questionId: "q2", selected: ["搜索", "发布"] },
  ], "batch payload carries every question with its selections");

  // Back preserves earlier answers; a custom answer replaces the option pick.
  await mount();
  click(byText("Go"), "question 1: pick Go");
  const custom = document.querySelector(".desktop-icon-popup__ask-custom input") as HTMLInputElement | null;
  setInput(custom, "自己写的方案", "type a custom answer on question 1");
  click(document.querySelector(".desktop-icon-popup__ask-next"), "next with custom answer");
  click(byText("测试"), "question 2: pick 测试");
  click(document.querySelector(".desktop-icon-popup__ask-next"), "submit custom batch");
  eq(submitted[1], [
    { questionId: "q1", selected: ["自己写的方案"] },
    { questionId: "q2", selected: ["测试"] },
  ], "custom answer replaces the option pick and the batch stays complete");

  // Back navigates and keeps the first question's selection.
  await mount();
  click(byText("TypeScript"), "question 1: pick TypeScript");
  click(document.querySelector(".desktop-icon-popup__ask-next"), "advance to question 2");
  click(byText("返回"), "go back to question 1");
  const pressed = [...document.querySelectorAll<HTMLButtonElement>(".desktop-icon-popup__answers > button")].find((b) => b.getAttribute("aria-pressed") === "true");
  eq(pressed?.textContent?.trim(), "TypeScript", "back preserves the earlier selection");

  // The backend revision changes when prompt/options change even if question
  // IDs stay stable. A live prop update must discard selections from the old
  // structure so a removed option can never be submitted as a custom value.
  const structuralKey = "same-question-ids";
  await act(async () => {
    root.render(React.createElement(AskFlow, { key: structuralKey, questions, busy: false, onAnswer: (answers) => submitted.push(answers) }));
    await Promise.resolve();
  });
  click(byText("Rust"), "same-id structure: select old option");
  const changedQuestions: WidgetQuestion[] = [
    { ...questions[0], prompt: "重新选择语言", options: [{ label: "Go", value: "Go" }, { label: "Python", value: "Python" }] },
    questions[1],
  ];
  await act(async () => {
    root.render(React.createElement(AskFlow, { key: structuralKey, questions: changedQuestions, busy: false, onAnswer: (answers) => submitted.push(answers) }));
    await Promise.resolve();
  });
  eq(byText("重新选择语言")?.textContent, "重新选择语言", "same-id structural update renders the latest prompt");
  ok(document.querySelector(".desktop-icon-popup__answers > button[aria-pressed=\"true\"]") === null, "same-id structural update clears stale selections");
  ok((document.querySelector(".desktop-icon-popup__ask-next") as HTMLButtonElement | null)?.disabled === true, "same-id structural update requires a fresh answer");

  await act(async () => root.unmount());
  dom.window.close();
}

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
