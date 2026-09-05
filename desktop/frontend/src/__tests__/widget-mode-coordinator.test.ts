import assert from "node:assert/strict";
import { createWidgetModeCoordinator, type WidgetModeState } from "../lib/widgetModeCoordinator";

const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}
function fixture() {
  let native: WidgetModeState = { active: false, revision: 0 };
  const calls: string[] = [], published: WidgetModeState[] = [];
  let opened = 0;
  const commit = (active: boolean) => { native = { active, revision: native.revision + 1 }; return native; };
  const backend = {
    async EnterWidgetMode() { calls.push("enter"); commit(true); },
    async ExitWidgetMode(tabID: string) { calls.push(`exit:${tabID}`); commit(false); },
    async GetWidgetModeState() { return native; },
  };
  const coordinator = createWidgetModeCoordinator(backend, value => published.push(value), () => { opened++; });
  coordinator.sync(native);
  return { backend, coordinator, commit, calls, published, opened: () => opened };
}

// Ordinary, duplicate and rapid opposite intents all converge.
{
  const f = fixture();
  await f.coordinator.exit();
  await f.coordinator.toggle();
  await f.coordinator.toggle();
  assert.deepEqual(f.calls, ["enter", "exit:"]);
  assert.equal(f.opened(), 1);
  await Promise.all([f.coordinator.enter(), f.coordinator.enter()]);
  assert.equal(f.coordinator.current(), true);
  await f.coordinator.exit("task");
  assert.equal(f.calls.at(-1), "exit:task");
}
// An old exit event arriving after the new entry binding must read latest
// native state. An old read response is rejected by its native revision.
{
  const f = fixture();
  await f.coordinator.enter();
  await f.coordinator.exit();
  const oldExit = await f.backend.GetWidgetModeState();
  await f.coordinator.enter();
  await f.coordinator.refresh(); // delivery of a queued widget:mode=false
  f.coordinator.sync(oldExit);   // delivery of an older outstanding read
  assert.equal(f.coordinator.current(), true, "stale exit cannot reveal main inside the icon region");
}
// Native completion releases pending without waiting for ancillary binding
// work. Its late reply cannot force a mode or replay the previous intent.
{
  const f = fixture();
  const reply = deferred<void>();
  f.backend.EnterWidgetMode = async () => { f.calls.push("enter"); f.commit(true); await reply.promise; };
  const entered = f.coordinator.enter();
  await flush();
  await f.coordinator.refresh();
  await entered;
  f.commit(false); // task open bypasses coordinator
  await f.coordinator.refresh();
  reply.resolve();
  await flush();
  assert.equal(f.coordinator.current(), false);
  f.backend.EnterWidgetMode = async () => { f.calls.push("enter"); f.commit(true); };
  await f.coordinator.enter();
  assert.equal(f.calls.length, 2);
}
// A newer explicit click during an unfinished entry is not lost to its ACK.
{
  const f = fixture();
  const reply = deferred<void>();
  f.backend.EnterWidgetMode = async () => { f.calls.push("enter"); await reply.promise; f.commit(true); };
  const entered = f.coordinator.enter();
  await flush();
  const exited = f.coordinator.exit();
  reply.resolve();
  await Promise.all([entered, exited]);
  assert.deepEqual(f.calls, ["enter", "exit:"]);
  assert.equal(f.coordinator.current(), false);
}
// Two native completions delivered in one JS batch keep the newest state;
// there is no microtask gap assumption between them.
{
  const f = fixture();
  const reply = deferred<void>();
  f.backend.EnterWidgetMode = async () => { await reply.promise; };
  const entered = f.coordinator.enter();
  await flush();
  f.coordinator.sync(f.commit(true));
  f.coordinator.sync(f.commit(false));
  reply.resolve();
  await entered;
  assert.equal(f.coordinator.current(), false);
}
// Failed operations don't publish target mode or collapse main early; retries
// are safe for both directions.
for (const target of [true, false]) {
  const f = fixture();
  if (!target) await f.coordinator.enter();
  const original = target ? f.backend.EnterWidgetMode : f.backend.ExitWidgetMode;
  if (target) f.backend.EnterWidgetMode = async () => { throw Error("temporary"); };
  else f.backend.ExitWidgetMode = async () => { throw Error("temporary"); };
  await assert.rejects(target ? f.coordinator.enter() : f.coordinator.exit(), /temporary/);
  assert.equal(f.coordinator.current(), !target);
  assert.equal(f.opened(), 0);
  if (target) f.backend.EnterWidgetMode = original as typeof f.backend.EnterWidgetMode;
  else f.backend.ExitWidgetMode = original;
  await (target ? f.coordinator.enter() : f.coordinator.exit());
  assert.equal(f.coordinator.current(), target);
}
process.stdout.write("widget mode coordinator tests passed\n");
