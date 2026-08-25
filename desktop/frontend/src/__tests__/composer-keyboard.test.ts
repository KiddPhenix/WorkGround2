// Run: tsx src/__tests__/composer-keyboard.test.ts
//
// Tests for composer prompt-history keyboard gating.

import {
  canUsePromptHistory,
  isComposerGuideKey,
  isComposerSubmitKey,
  isFnKeyEvent,
  normalizeComposerSubmitKey,
  promptHistoryDirectionFromEvent,
  type PromptHistoryDirection,
} from "../lib/composerKeyboard";

let passed = 0;
let failed = 0;

function eq(a: unknown, b: unknown, label: string) {
  if (a === b) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`);
    failed += 1;
  }
}

function eligible(
  direction: PromptHistoryDirection | null,
  overrides: Partial<Parameters<typeof canUsePromptHistory>[0]> = {},
) {
  return canUsePromptHistory({
    direction,
    menuOpen: false,
    composing: false,
    altKey: false,
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    fnKey: false,
    value: "",
    selectionStart: 0,
    selectionEnd: 0,
    historyIndex: -1,
    ...overrides,
  });
}

console.log("\ncomposerKeyboard");

eq(promptHistoryDirectionFromEvent({ key: "ArrowUp" }), "up", "ArrowUp maps to history up");
eq(promptHistoryDirectionFromEvent({ key: "ArrowDown" }), "down", "ArrowDown maps to history down");
eq(promptHistoryDirectionFromEvent({ key: "PageUp", code: "PageUp", keyCode: 33 }), null, "PageUp does not map to history");
eq(promptHistoryDirectionFromEvent({ key: "Home", code: "Home", keyCode: 36 }), null, "Home does not map to history");
eq(promptHistoryDirectionFromEvent({ key: "Unidentified", keyCode: 38 }), "up", "legacy unidentified keyCode 38 maps up");

eq(isFnKeyEvent({ key: "Fn" }), true, "Fn key is detected");
eq(isFnKeyEvent({ key: "ArrowUp", getModifierState: (key) => key === "Fn" }), true, "Fn modifier is detected");

eq(eligible("up", { value: "", selectionStart: 0, selectionEnd: 0 }), true, "empty input ArrowUp can recall history");
eq(eligible("down", { value: "", selectionStart: 0, selectionEnd: 0 }), false, "empty input ArrowDown does not start history");
eq(eligible("up", { value: "draft", selectionStart: 5, selectionEnd: 5 }), false, "ArrowUp at draft end keeps native textarea movement");
eq(eligible("up", { value: "draft", selectionStart: 0, selectionEnd: 0 }), true, "ArrowUp at first position recalls history");
eq(eligible("down", { value: "history", selectionStart: 7, selectionEnd: 7, historyIndex: 0 }), true, "ArrowDown at recalled history end moves newer");
eq(eligible("down", { value: "history", selectionStart: 3, selectionEnd: 3, historyIndex: 0 }), false, "ArrowDown inside text keeps native movement");
eq(eligible("up", { value: "line1\nline2", selectionStart: 7, selectionEnd: 7 }), false, "ArrowUp inside multiline text keeps native movement");
eq(eligible("up", { value: "history", selectionStart: 7, selectionEnd: 7, historyIndex: 0 }), true, "history mode allows repeated ArrowUp");
eq(eligible("up", { value: "", selectionStart: 0, selectionEnd: 0, fnKey: true }), false, "Fn-modified arrows are not history shortcuts");
eq(eligible("up", { value: "", selectionStart: 0, selectionEnd: 0, shiftKey: true }), false, "Shift+Arrow preserves selection behavior");

eq(normalizeComposerSubmitKey("ctrl_enter"), "ctrl_enter", "ctrl_enter submit key is preserved");
eq(normalizeComposerSubmitKey("unknown"), "enter", "unknown submit key falls back to enter");
eq(isComposerSubmitKey({ key: "Enter", shiftKey: false, ctrlKey: false, metaKey: false, altKey: false }, "enter", false), true, "Enter mode sends on plain Enter");
eq(isComposerSubmitKey({ key: "Enter", shiftKey: true, ctrlKey: false, metaKey: false, altKey: false }, "enter", false), false, "Enter mode keeps Shift+Enter for newline");
eq(isComposerSubmitKey({ key: "Enter", shiftKey: false, ctrlKey: false, metaKey: false, altKey: false }, "ctrl_enter", false), false, "Ctrl+Enter mode keeps plain Enter for newline");
eq(isComposerSubmitKey({ key: "Enter", shiftKey: false, ctrlKey: true, metaKey: false, altKey: false }, "ctrl_enter", false), true, "Ctrl+Enter mode sends on Ctrl+Enter");
eq(isComposerSubmitKey({ key: "Enter", shiftKey: false, ctrlKey: false, metaKey: true, altKey: false }, "ctrl_enter", false), true, "Ctrl+Enter mode also accepts Meta+Enter");
eq(isComposerSubmitKey({ key: "Enter", shiftKey: false, ctrlKey: true, metaKey: false, altKey: false }, "ctrl_enter", true), false, "IME composing Enter never sends");

// isComposerGuideKey: while running, the guide shortcut immediately steers the
// turn instead of following the ordinary submit path; while idle it never fires.
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: false, metaKey: false, altKey: false }, "enter", false, true), true, "enter mode: Shift+Enter guides while running");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: false, metaKey: false, altKey: false }, "enter", false, false), false, "enter mode: Shift+Enter stays newline while idle");
eq(isComposerGuideKey({ key: "Enter", shiftKey: false, ctrlKey: false, metaKey: false, altKey: false }, "enter", false, true), false, "enter mode: plain Enter is the ordinary submit while running");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: true, metaKey: false, altKey: false }, "enter", false, true), false, "enter mode: Ctrl+Shift+Enter is not a guide shortcut");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: false, metaKey: true, altKey: false }, "enter", false, true), false, "enter mode: Meta+Shift+Enter is not a guide shortcut");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: false, metaKey: false, altKey: false }, "enter", true, true), false, "enter mode: IME composing Shift+Enter never guides");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: false, metaKey: false, altKey: true }, "enter", false, true), false, "enter mode: Alt+Shift+Enter never guides");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: true, metaKey: false, altKey: false }, "ctrl_enter", false, true), true, "ctrl_enter mode: Ctrl+Shift+Enter guides while running");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: false, metaKey: true, altKey: false }, "ctrl_enter", false, true), true, "ctrl_enter mode: Meta+Shift+Enter guides (macOS equivalence)");
eq(isComposerGuideKey({ key: "Enter", shiftKey: false, ctrlKey: true, metaKey: false, altKey: false }, "ctrl_enter", false, true), false, "ctrl_enter mode: Ctrl+Enter is the ordinary submit while running");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: false, metaKey: false, altKey: false }, "ctrl_enter", false, true), false, "ctrl_enter mode: plain Shift+Enter stays newline");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: true, metaKey: false, altKey: false }, "ctrl_enter", false, false), false, "ctrl_enter mode: Ctrl+Shift+Enter stays newline while idle");
eq(isComposerGuideKey({ key: "Enter", shiftKey: true, ctrlKey: true, metaKey: false, altKey: false }, "ctrl_enter", true, true), false, "ctrl_enter mode: IME composing Ctrl+Shift+Enter never guides");

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
