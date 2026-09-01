import assert from "node:assert/strict";
import { IconRemovals } from "../components/widget/iconRemovals";
import type { DesktopIconActionResult, DesktopIconItem, DesktopIconSnapshot } from "../lib/bridge";

const item = (revision = "r1"): DesktopIconItem => ({
	id: "task:session:path", kind: "task", sourceId: "tab-1", title: "慢任务", status: "done",
	unreadCount: 0, notifications: [{ id: "notice-1", revision, kind: "completed", priority: 2, title: "完成", body: "完成", createdAt: 1, options: [] }],
	position: { row: "bottom", zone: "running", order: 0 }, revision, retained: true,
});
const snapshot = (items: DesktopIconItem[], revision: string): DesktopIconSnapshot => ({
	items, delegations: [], assistantTasks: [], revision, hoverStatusDelayMs: 1200, style: "icons", unreadRevision: 0,
});
const result = (status: DesktopIconActionResult["status"], items: DesktopIconItem[], error = ""): DesktopIconActionResult => ({
	status, snapshot: snapshot(items, `snapshot-${status}`), ...(error ? { error } : {}),
});
const deferred = <T>() => {
	let resolve!: (value: T) => void;
	let reject!: (reason: unknown) => void;
	const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
	return { promise, resolve, reject };
};
const flush = async () => { await Promise.resolve(); await Promise.resolve(); };

{
	let calls = 0;
	const backend = deferred<DesktopIconActionResult>();
	const removals = new IconRemovals(() => "request-1");
	const oldTicket = removals.beginSnapshot();
	const applied: DesktopIconSnapshot[] = [];
	const pending = removals.remove(item(), {
		send: async () => { calls++; return backend.promise; },
		snapshot: (ticket, next) => { if (removals.acceptSnapshot(ticket, next)) applied.push(next); },
		refresh: async () => {},
	});
	assert.deepEqual(removals.project([item()]), [], "icon disappears synchronously before backend dispatch");
	assert.equal(calls, 0, "dispatch yields one microtask so React can commit disappearance first");
	const duplicate = removals.remove(item(), { send: async () => { calls++; return backend.promise; }, snapshot: () => {}, refresh: async () => {} });
	assert.equal(duplicate, pending, "duplicate click shares the in-flight operation");
	await flush();
	assert.equal(calls, 1, "duplicate click dispatches only once");
	assert.equal(removals.acceptSnapshot(oldTicket, snapshot([item()], "old")), true);
	assert.deepEqual(removals.project([item()]), [], "old refresh cannot resurrect a pending removal");
	backend.resolve(result("accepted", []));
	assert.equal(await pending, "accepted");
	assert.deepEqual(removals.project([item()]), [], "accepted removal stays hidden until a later authoritative read");
	const freshTicket = removals.beginSnapshot();
	assert.equal(removals.acceptSnapshot(freshTicket, snapshot([item("new")], "fresh")), true);
	assert.deepEqual(removals.project([item("new")]).map(next => next.revision), ["new"], "fresh later state with the same ID is visible");
	assert.equal(applied.length, 1);
}

{
	let calls = 0;
	const requestIDs: string[] = [];
	const removals = new IconRemovals(() => `request-${calls + 1}`);
	const send = async (input: { requestId: string }) => {
		calls++;
		requestIDs.push(input.requestId);
		if (calls === 1) return result("retryable_error", [item()], "磁盘暂时不可用");
		return result("accepted", []);
	};
	const effects = { send, snapshot: (ticket: number, next: DesktopIconSnapshot) => { removals.acceptSnapshot(ticket, next); }, refresh: async () => {} };
	assert.equal(await removals.remove(item(), effects), "retryable_error");
	assert.deepEqual(removals.project([]).map(next => next.id), [item().id], "confirmed failure restores the removed row");
	assert.match(removals.error(), /磁盘暂时不可用/);
	assert.equal(await removals.remove(item(), effects), "accepted");
	assert.deepEqual(requestIDs, ["request-1", "request-1"], "retry reuses the same request ID");
}

{
	let calls = 0;
	const removals = new IconRemovals(() => "uncertain-request");
	const effects = {
		send: async () => { calls++; if (calls === 1) throw new Error("连接中断"); return result("accepted", []); },
		snapshot: (ticket: number, next: DesktopIconSnapshot) => { removals.acceptSnapshot(ticket, next); },
		refresh: async () => {},
	};
	assert.equal(await removals.remove(item(), effects), "retryable_error");
	assert.deepEqual(removals.project([item()]).map(next => next.id), [item().id], "transport failure restores the row");
	assert.equal(await removals.remove(item(), effects), "accepted");
	assert.equal(calls, 2, "restored row can retry the uncertain operation");
}

process.stdout.write("icon removal tests passed\n");
