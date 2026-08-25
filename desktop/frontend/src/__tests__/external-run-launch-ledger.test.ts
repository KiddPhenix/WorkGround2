import assert from "node:assert/strict";
import test from "node:test";
import { clearExternalRunLaunch, EXTERNAL_RUN_LAUNCH_KEY, prepareExternalRunLaunch, readExternalRunLaunch } from "../components/widget/externalRunLaunchLedger";

class MemoryStorage {
	data = new Map<string, string>();
	getItem(key: string) { return this.data.get(key) ?? null; }
	setItem(key: string, value: string) { this.data.set(key, value); }
	removeItem(key: string) { this.data.delete(key); }
}

test("lost response reuses the complete frozen DSH launch packet", () => {
	const storage = new MemoryStorage();
	let n = 0;
	const makeID = () => `req-${++n}`;
	const first = prepareExternalRunLaunch(storage, "D:/Work/one", "scan it", makeID);
	const replay = prepareExternalRunLaunch(storage, "D:/Work/one", "scan it", makeID);
	assert.deepEqual(replay, first);
	assert.equal(n, 1);
	assert.deepEqual(readExternalRunLaunch(storage), first);
});

test("changed intent receives a new request and accepted request clears only itself", () => {
	const storage = new MemoryStorage();
	let n = 0;
	const makeID = () => `req-${++n}`;
	const first = prepareExternalRunLaunch(storage, "D:/Work/one", "scan it", makeID);
	const changed = prepareExternalRunLaunch(storage, "D:/Work/two", "scan it", makeID);
	assert.notEqual(changed.requestId, first.requestId);
	clearExternalRunLaunch(storage, first.requestId);
	assert.equal(readExternalRunLaunch(storage)?.requestId, changed.requestId);
	clearExternalRunLaunch(storage, changed.requestId);
	assert.equal(storage.getItem(EXTERNAL_RUN_LAUNCH_KEY), null);
});

test("corrupt ledger is visible as no resumable packet and safely replaced", () => {
	const storage = new MemoryStorage();
	storage.setItem(EXTERNAL_RUN_LAUNCH_KEY, "{");
	assert.equal(readExternalRunLaunch(storage), null);
	const packet = prepareExternalRunLaunch(storage, "", "task", () => "req-new");
	assert.equal(packet.requestId, "req-new");
});
