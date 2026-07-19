// Run: tsx src/__tests__/widget-mode-coordinator.test.ts

import assert from "node:assert/strict";
import { createWidgetModeCoordinator } from "../lib/widgetModeCoordinator";

async function testBidirectionalToggle() {
  const calls: string[] = [];
  const published: boolean[] = [];
  const coordinator = createWidgetModeCoordinator({
    async EnterWidgetMode() { calls.push("enter"); },
    async ExitWidgetMode(tabID) { calls.push(`exit:${tabID}`); },
  }, (active) => published.push(active));

  await coordinator.toggle();
  await coordinator.toggle();

  assert.deepEqual(calls, ["enter", "exit:"]);
  assert.deepEqual(published, [true, false]);
  assert.equal(coordinator.current(), false);
}

async function testNativeEventPublishesBeforeBindingReturns() {
  let release!: () => void;
  const entered = new Promise<void>((resolve) => { release = resolve; });
  const published: boolean[] = [];
  const coordinator = createWidgetModeCoordinator({
    async EnterWidgetMode() { await entered; },
    async ExitWidgetMode() {},
  }, (active) => published.push(active));

  const transition = coordinator.enter();
  coordinator.sync(true);
  assert.deepEqual(published, [true], "native widget:mode must update React state immediately");
  release();
  await transition;
}

async function testFailureCanRetry() {
  let attempts = 0;
  const coordinator = createWidgetModeCoordinator({
    async EnterWidgetMode() {
      attempts += 1;
      if (attempts === 1) throw new Error("temporary failure");
    },
    async ExitWidgetMode() {},
  }, () => {});

  await assert.rejects(coordinator.enter(), /temporary failure/);
  await coordinator.enter();
  assert.equal(attempts, 2, "failed transitions remain retryable");
  assert.equal(coordinator.current(), true);
}

await testBidirectionalToggle();
await testNativeEventPublishesBeforeBindingReturns();
await testFailureCanRetry();
process.stdout.write("widget mode coordinator tests passed\n");
