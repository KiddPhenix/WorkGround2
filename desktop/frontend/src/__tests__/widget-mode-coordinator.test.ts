// Run: tsx src/__tests__/widget-mode-coordinator.test.ts

import assert from "node:assert/strict";
import { createWidgetModeCoordinator } from "../lib/widgetModeCoordinator";

async function testBidirectionalToggle() {
  const calls: string[] = [];
  const published: boolean[] = [];
  let openedMainWindow = 0;
  const coordinator = createWidgetModeCoordinator({
    async EnterWidgetMode() { calls.push("enter"); },
    async ExitWidgetMode(tabID) { calls.push(`exit:${tabID}`); },
  }, (active) => published.push(active), () => { openedMainWindow += 1; });

  coordinator.sync(false);
  await coordinator.toggle();
  await coordinator.toggle();

  assert.deepEqual(calls, ["enter", "exit:"]);
  assert.deepEqual(published, [false, true, false]);
  assert.equal(openedMainWindow, 1, "only a real widget-to-main transition applies the main-window layout");
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

async function testNativeExitOpensMainWindowOnce() {
  const published: boolean[] = [];
  let openedMainWindow = 0;
  let coordinator!: ReturnType<typeof createWidgetModeCoordinator>;
  coordinator = createWidgetModeCoordinator({
    async EnterWidgetMode() {},
    async ExitWidgetMode() { coordinator.sync(false); },
  }, (active) => published.push(active), () => { openedMainWindow += 1; });

  coordinator.sync(true);
  await coordinator.exit();

  assert.equal(openedMainWindow, 1, "native and binding completion must not apply the main-window layout twice");
  assert.deepEqual(published, [true, false, false]);
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

async function testFailedExitDoesNotCollapseMainWindowEarly() {
  let attempts = 0;
  let openedMainWindow = 0;
  const coordinator = createWidgetModeCoordinator({
    async EnterWidgetMode() {},
    async ExitWidgetMode() {
      attempts += 1;
      if (attempts === 1) throw new Error("temporary exit failure");
    },
  }, () => {}, () => { openedMainWindow += 1; });

  coordinator.sync(true);
  await assert.rejects(coordinator.exit(), /temporary exit failure/);
  assert.equal(openedMainWindow, 0, "a failed open keeps the hidden main-window layout untouched");
  assert.equal(coordinator.current(), true);
  await coordinator.exit();
  assert.equal(openedMainWindow, 1);
}

await testBidirectionalToggle();
await testNativeEventPublishesBeforeBindingReturns();
await testNativeExitOpensMainWindowOnce();
await testFailureCanRetry();
await testFailedExitDoesNotCollapseMainWindowEarly();
process.stdout.write("widget mode coordinator tests passed\n");
