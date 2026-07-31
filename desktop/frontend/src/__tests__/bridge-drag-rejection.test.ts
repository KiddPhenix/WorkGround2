// Run: tsx src/__tests__/bridge-drag-rejection.test.ts

import {
  isTransientWailsIPCError,
  isWailsNonFileDragError,
  isWailsNonFileDragErrorEvent,
  onFilesDropped,
  onFilesDroppedIn,
} from "../lib/bridge";

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

console.log("\nbridge drag rejection filtering");

eq(
  isWailsNonFileDragError(new Error("additional File object is not a file on the disk")),
  true,
  "suppresses Wails' explicit non-file drag error",
);
eq(isWailsNonFileDragError(new Error("invalid argument")), false, "does not suppress generic invalid argument");
eq(
  isWailsNonFileDragError(new Error("invalid argument"), true),
  true,
  "suppresses invalid argument only after a native file drag",
);
eq(
  isWailsNonFileDragError(new TypeError("invalid argument"), false),
  false,
  "keeps non-drag TypeError invalid argument visible",
);
eq(
  isWailsNonFileDragError("network invalid argument", true),
  false,
  "does not suppress broader messages that merely contain invalid argument",
);
eq(
  isWailsNonFileDragError("Uncaught TypeError: invalid argument", true),
  true,
  "normalizes Chromium's window.error message prefix",
);
eq(
  isWailsNonFileDragErrorEvent({ message: "Uncaught TypeError: invalid argument", error: undefined }, true),
  true,
  "suppresses invalid argument delivered through ErrorEvent.message",
);
eq(
  isWailsNonFileDragErrorEvent({ message: "Uncaught TypeError: invalid argument", error: new TypeError("invalid argument") }, true),
  true,
  "suppresses invalid argument delivered through ErrorEvent.error.message",
);
eq(
  isWailsNonFileDragErrorEvent({ message: "Uncaught TypeError: invalid argument", error: new TypeError("invalid argument") }, false),
  false,
  "keeps ErrorEvent invalid argument visible without a recent native file drag",
);
eq(
  isTransientWailsIPCError(new DOMException("Failed to execute 'send' on 'WebSocket': Still in CONNECTING state.", "InvalidStateError")),
  true,
  "suppresses Wails IPC calls made before the websocket is open",
);
eq(
  isTransientWailsIPCError(new TypeError("Cannot read properties of null (reading 'send')")),
  true,
  "suppresses Wails IPC calls made after the websocket is torn down",
);
eq(
  isTransientWailsIPCError(new Error("backend returned an application error")),
  false,
  "keeps ordinary bridge failures visible",
);

let nativeDrop: ((x: number, y: number, paths: string[]) => void) | undefined;
let offCalls = 0;
const fakeWindow = {
  runtime: {
    OnFileDrop: (callback: (x: number, y: number, paths: string[]) => void) => { nativeDrop = callback; },
    OnFileDropOff: () => { offCalls += 1; },
  },
  addEventListener: () => {},
  removeEventListener: () => {},
};
(globalThis as unknown as { window: typeof fakeWindow }).window = fakeWindow;
const target = {
  isConnected: true,
  getBoundingClientRect: () => ({ left: 0, top: 0, right: 20, bottom: 20 }),
} as HTMLElement;
let globalDrops = 0;
let targetedDrops = 0;
const offGlobal = onFilesDropped(() => { globalDrops += 1; });
const offTarget = onFilesDroppedIn(() => target, () => { targetedDrops += 1; });
nativeDrop?.(10, 10, ["input.csv"]);
eq(targetedDrops, 1, "targeted Work file drop receives the native path");
eq(globalDrops, 0, "targeted Work file drop is not duplicated into the composer");
nativeDrop?.(50, 50, ["other.csv"]);
eq(globalDrops, 1, "drop outside the Work target reaches the global composer fallback");
offTarget();
offGlobal();
eq(offCalls, 1, "native bridge detaches after the last subscriber");

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
