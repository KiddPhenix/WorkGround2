import assert from "node:assert/strict";
import { createIconEntryRefresh } from "../components/widget/desktopIconEntryRefresh";

function deferred() {
	let resolve!: () => void;
	const promise = new Promise<void>(done => { resolve = done; });
	return { promise, resolve };
}
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };

// A full scan from the previous visible lifetime stays blocked. Revealing a
// different Session must publish its entry immediately and ignore the old read.
const oldScan = deferred();
const newScan = deferred();
const visible: string[] = [];
const calls: string[] = [];
let session = "old";
const reader = createIconEntryRefresh(async (entry, current) => {
	const name = session;
	calls.push(`${name}:${entry ? "entry" : "full"}`);
	if (!entry) await (name === "old" ? oldScan.promise : newScan.promise);
	if (current()) visible.push(name);
});
reader.activate();
const old = reader.refresh();
await flush();
assert.deepEqual(visible, ["old"], "entry renders while full scan is blocked");
reader.dispose();
session = "new";
reader.activate();
const next = reader.refresh();
await flush();
assert.deepEqual(visible, ["old", "new"], "new Session never waits for the old full scan");
assert.deepEqual(calls, ["old:entry", "old:full", "new:entry", "new:full"]);
oldScan.resolve();
await old;
assert.deepEqual(visible, ["old", "new"], "late old snapshot cannot roll back new Session");
assert.equal(reader.refresh(), next, "old completion cannot clear new single-flight request");
newScan.resolve();
await next;
assert.equal(visible.at(-1), "new");
reader.dispose();
await reader.refresh();
assert.equal(calls.filter(call => call.endsWith(":entry")).length, 2, "one entry read per reveal");

// Hiding during the entry request prevents even starting the obsolete full scan.
const pendingEntry = deferred();
let fullCalls = 0;
const hidden = createIconEntryRefresh(async entry => {
	if (entry) await pendingEntry.promise;
	else fullCalls++;
});
hidden.activate();
const pending = hidden.refresh();
hidden.dispose();
pendingEntry.resolve();
await pending;
assert.equal(fullCalls, 0);
console.log("desktop icon entry refresh tests passed");
