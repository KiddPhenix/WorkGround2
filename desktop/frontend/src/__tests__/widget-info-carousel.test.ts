// Run: tsx src/__tests__/widget-info-carousel.test.ts

import assert from "node:assert/strict";
import type { WidgetSnapshot } from "../lib/bridge";
import {
  availableWidgetInfoPages,
  formatCompactTokens,
  formatWidgetDuration,
  nextWidgetInfoPage,
  resolveWidgetInfoPage,
  shouldShowWidgetContext,
  widgetPetState,
} from "../components/widget/widgetInfoCarouselState";

function snapshot(overrides: Partial<WidgetSnapshot> = {}): WidgetSnapshot {
  return {
    mode: true,
    remainingCount: 0,
    runningCount: 0,
    waitingCount: 0,
    completedCount: 0,
    failedCount: 0,
    backgroundCount: 0,
    isIdle: true,
    info: {
      totalTokens: 12_840_000,
      tokenPartial: false,
      idleSince: 100,
      system: { available: true, network: "online", cpu: 23, memory: 61 },
      models: [{ provider: "deepseek", model: "deepseek-chat", brand: "deepseek" }],
    },
    version: "test",
    ...overrides,
  };
}

const all = availableWidgetInfoPages(snapshot());
assert.deepEqual(all, ["tokens", "clock", "pet", "idle", "system", "models"]);
assert.equal(nextWidgetInfoPage("tokens", all), "clock");
assert.equal(nextWidgetInfoPage("models", all), "tokens");
assert.equal(resolveWidgetInfoPage("models", ["tokens", "clock"]), "tokens");

const unavailable = snapshot({
  info: {
    ...snapshot().info,
    system: { available: false, network: "unknown", cpu: 0, memory: 0 },
    models: [],
  },
});
assert.deepEqual(availableWidgetInfoPages(unavailable), ["tokens", "clock", "pet", "idle"]);

assert.equal(formatCompactTokens(12_840_000), "12.84M");
assert.equal(formatCompactTokens(999), "999");
assert.equal(formatWidgetDuration(3_661_000), "01:01:01");

assert.equal(shouldShowWidgetContext("task:1", "task:1"), false, "same revision must respect a manual switch");
assert.equal(shouldShowWidgetContext("task:1", "task:2"), true, "new revision gets one context takeover");
assert.equal(shouldShowWidgetContext("", ""), false);

assert.equal(widgetPetState(snapshot({ runningCount: 1, isIdle: false })), "working");
assert.equal(widgetPetState(snapshot({ waitingCount: 1, isIdle: false })), "waiting");
assert.equal(widgetPetState(snapshot({ failedCount: 1, isIdle: false })), "error");
assert.equal(widgetPetState(snapshot({ completedCount: 1, isIdle: false })), "success");
assert.equal(widgetPetState(snapshot({ info: { ...snapshot().info, system: { available: true, network: "offline", cpu: 0, memory: 0 } } })), "offline");

process.stdout.write("widget info carousel tests passed\n");
