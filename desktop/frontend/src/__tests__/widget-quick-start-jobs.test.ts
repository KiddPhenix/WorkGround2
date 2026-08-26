import assert from "node:assert/strict";
import type { DesktopIconItem, WidgetConversationInput, WidgetConversationResult } from "../lib/bridge";
import {
	QUICK_CONSUMED_DRAFT_KEY,
	QUICK_DRAFT_KEY,
	QUICK_JOBS_KEY,
	QUICK_LEGACY_PENDING_KEY,
	clearConsumedDraftMarker,
	createLatestAppliedGuard,
	createQuickStartJobRunner,
	createQuickStartOpenTaskGate,
	getSharedQuickStartJobRunner,
	isQuickStartJobItem,
	loadQuickStartJobs,
	mergeQuickStartItems,
	quickStartJobItem,
	quickStartJobItemID,
	quickStartJobPromptLabel,
	quickStartJobRequestIDFromItem,
	quickStartJobStateLabel,
	quickStartJobWorkspaceLabel,
	readConsumedDraftMarker,
	readQuickStartJobs,
	reconcileQuickStartJobs,
	recordConsumedDraftMarker,
	removeQuickStartJob,
	resetSharedQuickStartJobRunner,
	cleanupConsumedDraft,
	decideConsumedDraft,
	saveQuickStartJobs,
	upsertQuickStartJob,
	type QuickStartJob,
	type QuickStartJobs,
	type QuickStartJobStorage,
} from "../components/widget/widgetQuickStartJobs";

type FakeStorage = QuickStartJobStorage & { setFail: boolean; getFail: boolean; removeFail: boolean; map: Map<string, string> };

function fakeStorage(initial: Record<string, string> = {}): FakeStorage {
	const map = new Map(Object.entries(initial));
	const storage = {
		map,
		setFail: false,
		getFail: false,
		removeFail: false,
		getItem(key: string) {
			if (storage.getFail) throw new Error("storage read failed");
			return map.has(key) ? map.get(key)! : null;
		},
		setItem(key: string, value: string) {
			if (storage.setFail) throw new Error("storage quota exceeded");
			map.set(key, value);
		},
		removeItem(key: string) {
			if (storage.removeFail) throw new Error("storage remove failed");
			map.delete(key);
		},
	};
	return storage;
}

const accepted = (tabId: string): WidgetConversationResult => ({ status: "accepted", tabId, snapshot: {} as WidgetConversationResult["snapshot"] });
const alreadyApplied = (tabId: string): WidgetConversationResult => ({ status: "already_applied", tabId, snapshot: {} as WidgetConversationResult["snapshot"] });
const invalid = (error: string): WidgetConversationResult => ({ status: "invalid", error, snapshot: {} as WidgetConversationResult["snapshot"] });

const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (reason: unknown) => void;
	const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
	return { promise, resolve, reject };
}

const intent = { prompt: "fix the widget", workspace: "auto", model: "deepseek/deepseek-v4", approvalMode: "auto" };

function realTask(id: string): DesktopIconItem {
	return { id, kind: "task", sourceId: id.slice("task:".length), title: "t", status: "running", unreadCount: 0, notifications: [], position: { row: "bottom", zone: "running", order: 0 }, revision: "1" };
}

// --- ledger parse / migrate / fallback (pure) ---

{
	const storage = fakeStorage();
	assert.deepEqual(loadQuickStartJobs(storage), {}, "empty storage loads no jobs");
}

{
	const storage = fakeStorage({ [QUICK_JOBS_KEY]: "not json{{{" });
	assert.deepEqual(loadQuickStartJobs(storage), {}, "a corrupted ledger falls back to an empty job map");
}

{
	const storage = fakeStorage({ [QUICK_JOBS_KEY]: JSON.stringify({ version: 1, jobs: "garbage" }) });
	assert.deepEqual(loadQuickStartJobs(storage), {}, "a non-object ledger jobs field falls back to empty");
}

{
	const storage = fakeStorage();
	const job: QuickStartJob = {
		requestId: "icon-new:1",
		intent,
		phase: "running",
		createdAt: 10,
		updatedAt: 11,
	};
	saveQuickStartJobs(storage, { "icon-new:1": job });
	const loaded = loadQuickStartJobs(storage);
	assert.deepEqual(loaded, { "icon-new:1": job }, "a saved ledger round-trips through load");
	const raw = JSON.parse(storage.map.get(QUICK_JOBS_KEY)!);
	assert.equal(raw.version, 1, "the ledger is versioned");
	assert.deepEqual(Object.keys(raw.jobs), ["icon-new:1"], "the ledger is keyed by requestId");
}

{
	const storage = fakeStorage({
		[QUICK_JOBS_KEY]: JSON.stringify({
			version: 1,
			jobs: {
				good: { requestId: "good", intent, phase: "accepted", tabId: "tab-1", createdAt: 1, updatedAt: 2 },
				badKey: { requestId: "other", intent, phase: "running", createdAt: 1, updatedAt: 2 },
				noPrompt: { requestId: "noPrompt", intent: { ...intent, prompt: "  " }, phase: "running", createdAt: 1, updatedAt: 2 },
				noRequest: { intent, phase: "running", createdAt: 1, updatedAt: 2 },
				badPhase: { requestId: "badPhase", intent, phase: "exploded", createdAt: 1, updatedAt: 2 },
				notObject: 42,
			},
		}),
	});
	const loaded = loadQuickStartJobs(storage);
	assert.ok(loaded.good, "a valid entry survives");
	assert.equal(loaded.good.tabId, "tab-1", "accepted keeps its real tabId");
	assert.equal(loaded.badPhase.phase, "running", "an unknown phase normalizes to running instead of dropping the job");
	assert.deepEqual(Object.keys(loaded), ["good", "badPhase"], "corrupt entries (key mismatch, empty prompt, missing requestId, non-object) are dropped");
}

{
	const storage = fakeStorage({
		[QUICK_LEGACY_PENDING_KEY]: JSON.stringify({ id: "icon-new:legacy", prompt: "hello", workspace: "global", model: "m", approvalMode: "yolo" }),
	});
	const loaded = loadQuickStartJobs(storage);
	assert.equal(loaded["icon-new:legacy"]?.requestId, "icon-new:legacy", "the legacy single pending slot migrates with its requestId");
	assert.equal(loaded["icon-new:legacy"]?.phase, "running", "the migrated job is resumed");
	assert.deepEqual(loaded["icon-new:legacy"]?.intent, { prompt: "hello", workspace: "global", model: "m", approvalMode: "yolo" }, "the migrated intent is preserved");
	assert.equal(storage.map.has(QUICK_LEGACY_PENDING_KEY), false, "migration cleans up the legacy key");
	const second = loadQuickStartJobs(storage);
	assert.deepEqual(second, loaded, "migration is idempotent: a second load does not duplicate the job");
}

{
	const storage = fakeStorage({
		[QUICK_LEGACY_PENDING_KEY]: JSON.stringify({ id: "icon-new:legacy", prompt: "hello" }),
		[QUICK_JOBS_KEY]: JSON.stringify({ version: 1, jobs: { "icon-new:legacy": { requestId: "icon-new:legacy", intent, phase: "running", createdAt: 1, updatedAt: 2 } } }),
	});
	const loaded = loadQuickStartJobs(storage);
	assert.equal(loaded["icon-new:legacy"].intent.prompt, "fix the widget", "an existing ledger entry wins over the legacy slot");
}

{
	const storage = fakeStorage({ [QUICK_LEGACY_PENDING_KEY]: "not json" });
	assert.deepEqual(loadQuickStartJobs(storage), {}, "a corrupted legacy record is dropped safely");
	assert.equal(storage.map.has(QUICK_LEGACY_PENDING_KEY), false, "a corrupted legacy record is still cleaned up");
}

{
	// A failed ledger write during migration must keep the legacy record AND a
	// matching legacy draft so a future load can retry instead of losing the
	// intent or silently resubmitting the prompt.
	const storage = fakeStorage({
		[QUICK_LEGACY_PENDING_KEY]: JSON.stringify({ id: "icon-new:legacy", prompt: "hello" }),
		[QUICK_DRAFT_KEY]: "hello",
	});
	storage.setFail = true;
	const loaded = loadQuickStartJobs(storage);
	assert.equal(loaded["icon-new:legacy"]?.requestId, "icon-new:legacy", "the migrated job still exists in memory for this session");
	assert.equal(storage.map.has(QUICK_LEGACY_PENDING_KEY), true, "a failed ledger write keeps the legacy record for a migration retry");
	assert.equal(storage.map.get(QUICK_DRAFT_KEY), "hello", "a failed ledger write keeps the matching legacy draft");
	storage.setFail = false;
	const retried = loadQuickStartJobs(storage);
	assert.ok(retried["icon-new:legacy"], "the next load retries and completes the migration");
	assert.equal(storage.map.has(QUICK_LEGACY_PENDING_KEY), false, "a successful migration retry removes the legacy key");
	assert.equal(storage.map.has(QUICK_DRAFT_KEY), false, "a matching legacy draft is cleared only after the ledger save succeeded");
}

{
	// A legacy draft that does not match the migrated prompt is the user's own
	// next input; it must never be cleared by the migration.
	const storage = fakeStorage({
		[QUICK_LEGACY_PENDING_KEY]: JSON.stringify({ id: "icon-new:legacy", prompt: "hello" }),
		[QUICK_DRAFT_KEY]: "a different draft",
	});
	loadQuickStartJobs(storage);
	assert.equal(storage.map.get(QUICK_DRAFT_KEY), "a different draft", "a non-matching legacy draft is never cleared");
}

{
	// A matching legacy draft is cleared only after the ledger save succeeded.
	const storage = fakeStorage({
		[QUICK_LEGACY_PENDING_KEY]: JSON.stringify({ id: "icon-new:legacy", prompt: "hello" }),
		[QUICK_DRAFT_KEY]: "  hello  ",
	});
	loadQuickStartJobs(storage);
	assert.equal(storage.map.has(QUICK_DRAFT_KEY), false, "a whitespace-matching legacy draft is cleared after the save succeeded");
}

// --- upsert / remove (pure) ---

{
	const job: QuickStartJob = { requestId: "a", intent, phase: "running", createdAt: 1, updatedAt: 1 };
	const added = upsertQuickStartJob({}, job);
	assert.deepEqual(added, { a: job }, "upsert adds a new key");
	const changed: QuickStartJob = { ...job, phase: "accepted", tabId: "t" };
	const replaced = upsertQuickStartJob(added, changed);
	assert.deepEqual(replaced, { a: changed }, "upsert replaces the same key");
	const removed = removeQuickStartJob(replaced, "a");
	assert.deepEqual(removed, {}, "remove deletes only the target key");
	assert.equal(removeQuickStartJob(added, "missing"), added, "removing an unknown key returns the same map reference");
}

// --- optimistic icon projection (pure) ---

{
	const running: QuickStartJob = { requestId: "r1", intent, phase: "running", createdAt: 1, updatedAt: 1 };
	const acceptedJob: QuickStartJob = { requestId: "r2", intent, phase: "accepted", tabId: "tab-2", createdAt: 2, updatedAt: 2 };
	const failed: QuickStartJob = { requestId: "r3", intent, phase: "failed", error: "boom", createdAt: 3, updatedAt: 3 };
	const runningItem = quickStartJobItem(running);
	assert.equal(runningItem.id, "opt:r1", "the optimistic key is stable per requestId");
	assert.equal(runningItem.kind, "task", "a queued job renders as a task icon");
	assert.equal(runningItem.status, "idle", "an in-flight job is an idle task icon (subtle queued dot, no fake Thinking)");
	assert.deepEqual(runningItem.position, { row: "bottom", zone: "running", order: 0 }, "queued jobs join the bottom running row");
	assert.equal(quickStartJobItem(acceptedJob).status, "running", "an accepted job maps to the running visual (the backend turn is really running)");
	assert.equal(quickStartJobItem(failed).status, "failed", "a failed job reuses the real failed visual");
	assert.equal(quickStartJobRequestIDFromItem("opt:r1"), "r1", "the requestId is recoverable from the optimistic key");
	assert.equal(quickStartJobRequestIDFromItem("task:r1"), null, "non-optimistic ids are not job ids");
	assert.equal(isQuickStartJobItem(runningItem), true, "optimistic items are recognizable");
	assert.equal(quickStartJobItemID("r1"), "opt:r1", "the optimistic key builder round-trips");
	assert.equal(quickStartJobPromptLabel({ ...intent, prompt: "  first line\nsecond line  " }), "first line", "the label uses the first prompt line");
	assert.equal(quickStartJobPromptLabel({ ...intent, prompt: "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十" }).length, 25, "a long prompt truncates to 24 chars plus an ellipsis");
	assert.equal(quickStartJobWorkspaceLabel("auto"), "Auto", "auto workspace reads as Auto through the locale dictionary");
	assert.equal(quickStartJobWorkspaceLabel("global"), "Global", "global workspace reads as Global");
	assert.equal(quickStartJobWorkspaceLabel("project:D:/Work/Alpha"), "Alpha", "a project workspace shows its folder name");
	// aria/title contracts: the optimistic icon's accessible/mouse state always
	// announces 后台发送中 while delivering and 发送失败，可重试 once failed
	// (through the locale dictionary; the test env defaults to English).
	assert.equal(quickStartJobStateLabel(runningItem), "Sending in the background", "an in-flight job announces 后台发送中");
	assert.equal(quickStartJobStateLabel(quickStartJobItem(acceptedJob)), "Sending in the background", "an accepted job still announces 后台发送中 (backend turn running)");
	assert.equal(quickStartJobStateLabel(quickStartJobItem(failed)), "Send failed. Retry.", "a failed job announces 发送失败，可重试");
}

// --- merge with the authoritative snapshot (pure) ---

{
	const task: DesktopIconItem = { id: "task:t1", kind: "task", sourceId: "t1", title: "t", status: "running", unreadCount: 0, notifications: [], position: { row: "bottom", zone: "running", order: 0 }, revision: "1" };
	const workspace: DesktopIconItem = { id: "workspace:w", kind: "workspace", sourceId: "w", title: "w", status: "idle", unreadCount: 0, notifications: [], position: { row: "bottom", zone: "workspace", order: 0 }, revision: "1" };
	const fixed: DesktopIconItem = { id: "fixed:new", kind: "fixed", sourceId: "new", title: "新建", status: "idle", unreadCount: 0, notifications: [], position: { row: "bottom", zone: "fixed", order: 0 }, revision: "1" };
	const opt = quickStartJobItem({ requestId: "r1", intent, phase: "running", createdAt: 1, updatedAt: 1 });
	const merged = mergeQuickStartItems([task, workspace, fixed], [opt]);
	assert.deepEqual(merged.map((item) => item.id), ["opt:r1", "task:t1", "workspace:w", "fixed:new"], "the newest optimistic Session stays at the far-left edge of the bottom row");
	const newer = quickStartJobItem({ requestId: "r2", intent, phase: "running", createdAt: 2, updatedAt: 2 });
	assert.deepEqual(mergeQuickStartItems([task, workspace, fixed], [newer, opt]).map((item) => item.id), ["opt:r2", "opt:r1", "task:t1", "workspace:w", "fixed:new"], "simultaneous optimistic Sessions remain newest-first at the left edge");
	assert.deepEqual(mergeQuickStartItems([fixed], []), [fixed], "no jobs means no merge (same reference)");
	assert.equal(mergeQuickStartItems([], [opt]).length, 1, "an empty snapshot still renders the optimistic icon");
}

// --- snapshot reconciliation (pure, never time-based) ---

{
	const job: QuickStartJob = { requestId: "r1", intent, phase: "accepted", tabId: "tab-1", createdAt: 1, updatedAt: 1 };
	const jobs: QuickStartJobs = { r1: job };
	const withReal = reconcileQuickStartJobs(jobs, [realTask("task:tab-1")]);
	assert.deepEqual(withReal, {}, "an accepted job is removed the moment its real task:task:<tabId> icon appears");
	assert.equal(reconcileQuickStartJobs(jobs, []), jobs, "no matching real icon keeps the accepted job (no premature removal)");
	assert.equal(reconcileQuickStartJobs(jobs, [realTask("task:other")]), jobs, "an unrelated real icon never removes the accepted job");
	const runningJobs: QuickStartJobs = { r1: { ...job, phase: "running" }, r2: { ...job, requestId: "r2", phase: "running" } };
	assert.equal(reconcileQuickStartJobs(runningJobs, [realTask("task:tab-1")]), runningJobs, "running jobs are never removed by a poll");
	const failedJobs: QuickStartJobs = { r1: { ...job, phase: "failed" } };
	assert.equal(reconcileQuickStartJobs(failedJobs, [realTask("task:tab-1")]), failedJobs, "failed jobs are never removed by a poll");
}

// --- latest-successfully-applied poll guard (pure): starting a new poll
// never invalidates an older response (slow polls cannot starve each other
// out when calls exceed the interval); only a newer response that actually
// applied makes an older one stale. An errored response applies nothing, so
// it never starves the next success. Out-of-order no-real-after-real is
// preserved: an older response resolving after a newer one applied is dropped.

{
	const guard = createLatestAppliedGuard();
	const older = guard.begin();
	const newer = guard.begin();
	assert.equal(guard.mayApply(older), true, "starting a newer poll does NOT invalidate the older in-flight response");
	assert.equal(guard.mayApply(newer), true, "no response applied yet: the newer one may still apply");
	guard.markApplied(newer);
	assert.equal(guard.mayApply(older), false, "an older response is stale once a NEWER response applied");
	assert.equal(guard.mayApply(newer), false, "an applied generation cannot apply twice");
}

{
	// Continuous slow responses still apply in order (no starvation): each
	// response is newer than the last applied one even though newer polls
	// started long before it resolved.
	const guard = createLatestAppliedGuard();
	const g1 = guard.begin();
	const g2 = guard.begin();
	const g3 = guard.begin();
	assert.equal(guard.mayApply(g1), true, "the first slow response may apply when it resolves");
	guard.markApplied(g1);
	assert.equal(guard.mayApply(g2), true, "the second slow response applies after the first (no starvation)");
	guard.markApplied(g2);
	assert.equal(guard.mayApply(g3), true, "the third slow response applies after the second (no starvation)");
}

{
	// Errors do not starve the next success: a failing response never applies,
	// so the next successful response may still apply.
	const guard = createLatestAppliedGuard();
	const failing = guard.begin();
	const next = guard.begin();
	assert.equal(guard.mayApply(failing), true, "a failing response that never applied invalidates nothing");
	guard.markApplied(next);
	assert.equal(guard.mayApply(failing), false, "a response older than an applied one is stale even if it errors later");
	const afterError = guard.begin();
	assert.equal(guard.mayApply(afterError), true, "a success after an error applies normally");
}

{
	// A response newer than the newest APPLIED one may apply even when even
	// newer requests have started (latest-successfully-applied, not
	// latest-started).
	const guard = createLatestAppliedGuard();
	const slow = guard.begin();
	const slower = guard.begin();
	const newest = guard.begin();
	guard.markApplied(slow);
	assert.equal(guard.mayApply(slower), true, "an older in-flight response is not starved by newer requests that have only STARTED");
	assert.equal(guard.mayApply(newest), true, "the newest request may also apply");
	guard.markApplied(slower);
	assert.equal(guard.mayApply(newest), true, "applying an older response does not block a newer one");
}

{
	// Out-of-order polling handoff: new(real) resolves and reconciles the
	// accepted job away; old(no-real) resolving afterwards must not resurrect
	// it (the hook's latest-wins guard drops the stale response before
	// reconcile, and reconcile itself is merge-on-write against the durable
	// ledger).
	const runner = createQuickStartJobRunner({
		deliver: async () => accepted("tab-1"),
		storage: fakeStorage(),
		wait: async () => {},
	});
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	await flush();
	assert.equal(runner.jobs()[submitted.requestId].phase, "accepted", "the job accepted");
	runner.reconcile([realTask("task:tab-1")]);
	assert.ok(!runner.jobs()[submitted.requestId], "new(real) reconciles the job away (no duplicate)");
	runner.reconcile([]);
	runner.reconcile([realTask("task:other")]);
	assert.ok(!runner.jobs()[submitted.requestId], "old(no-real) resolving later never resurrects the reconciled job");
}

{
	// A one-second authoritative poll reads a fresh object from localStorage.
	// Equivalent content must keep the runner reference and skip subscribers,
	// otherwise every idle poll forces DesktopIconMode + hit-region layout work.
	const storage = fakeStorage();
	const runner = createQuickStartJobRunner({
		deliver: async () => accepted("tab-1"),
		storage,
		wait: async () => {},
	});
	const seen: QuickStartJobs[] = [];
	runner.subscribe((jobs) => seen.push(jobs));
	const initial = runner.jobs();
	runner.reconcile([]);
	runner.reconcile([realTask("task:unrelated")]);
	assert.equal(seen.length, 1, "equivalent empty polls do not notify subscribers");
	assert.equal(runner.jobs(), initial, "equivalent polls preserve the current jobs reference");

	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	assert.equal(seen.length, 2, "a real job addition still notifies subscribers");
	const running = runner.jobs();
	runner.reconcile([]);
	assert.equal(seen.length, 2, "an equivalent running-job poll stays silent");
	assert.equal(runner.jobs(), running, "an equivalent populated poll also preserves identity");
	await flush();
	assert.equal(seen.length, 3, "the real running-to-accepted transition still notifies");
	runner.reconcile([realTask("task:tab-1")]);
	assert.equal(seen.length, 4, "the authoritative handoff deletion still notifies");
	assert.ok(!runner.jobs()[submitted.requestId], "the real task handoff still removes the optimistic job");
}

// --- runner: submit is synchronous and delivery is background ---

{
	const calls: WidgetConversationInput[] = [];
	const pending = deferred<WidgetConversationResult>();
	const runner = createQuickStartJobRunner({
		deliver: async (input) => { calls.push(input); return pending.promise; },
		storage: fakeStorage(),
		wait: async () => {},
	});
	const outcome = runner.submit(intent);
	assert.equal(outcome.ok, true, "a valid submit succeeds synchronously");
	if (outcome.ok) {
		assert.match(outcome.requestId, /^icon-new:/, "the job owns a fresh requestId");
		const job = runner.jobs()[outcome.requestId];
		assert.equal(job.phase, "running", "the job exists before the backend promise resolves");
		assert.deepEqual(job.intent, intent, "the frozen intent is persisted with the job");
		assert.equal(calls.length, 1, "delivery started in the background");
		pending.resolve(accepted("tab-1"));
		await flush();
		const after = runner.jobs()[outcome.requestId];
		assert.equal(after.phase, "accepted", "a slow controller never blocks the enqueue; the job updates when it resolves");
		assert.equal(after.tabId, "tab-1", "the real tabId is kept for snapshot reconciliation");
	}
}

// --- validation / persistence failures keep the modal path open ---

{
	const calls: WidgetConversationInput[] = [];
	const runner = createQuickStartJobRunner({
		deliver: async (input) => { calls.push(input); return accepted("t"); },
		storage: fakeStorage(),
		wait: async () => {},
	});
	const outcome = runner.submit({ ...intent, prompt: "   " });
	assert.deepEqual(outcome, { ok: false, error: "Enter a message first" }, "an empty prompt fails validation synchronously (localized error, test env defaults to English)");
	assert.equal(calls.length, 0, "an invalid submit never dispatches");
	assert.deepEqual(runner.jobs(), {}, "an invalid submit never enqueues");
}

{
	const storage = fakeStorage();
	storage.setFail = true;
	const runner = createQuickStartJobRunner({
		deliver: async () => { throw new Error("should not dispatch"); },
		storage,
		wait: async () => {},
	});
	const outcome = runner.submit(intent);
	assert.equal(outcome.ok, false, "a ledger persistence failure returns an error instead of closing the modal");
	if (!outcome.ok) assert.match(outcome.error, /storage quota/);
	assert.deepEqual(runner.jobs(), {}, "a persistence failure enqueues nothing in memory");
}

// --- double dispatch is guarded; two tasks stay independent and out-of-order
// results only touch their own requestId ---

{
	const calls: WidgetConversationInput[] = [];
	const results = new Map<string, ReturnType<typeof deferred<WidgetConversationResult>>>();
	const runner = createQuickStartJobRunner({
		deliver: (input) => {
			calls.push(input);
			const d = deferred<WidgetConversationResult>();
			results.set(input.requestId, d);
			return d.promise;
		},
		storage: fakeStorage(),
		wait: async () => {},
	});
	const first = runner.submit(intent);
	const second = runner.submit({ ...intent, prompt: "another task" });
	assert.ok(first.ok && second.ok, "two consecutive submits both enqueue");
	if (!first.ok || !second.ok) throw new Error("submit failed");
	assert.notEqual(first.requestId, second.requestId, "each submit owns a fresh requestId");
	assert.equal(calls.length, 2, "both jobs dispatch independently");
	assert.deepEqual(calls[1].existingTitles, [quickStartJobPromptLabel(intent)], "a later naming request sees other frontend-only optimistic icon labels");

	// Double dispatch of the SAME requestId (resume/retry while in flight).
	runner.retry(first.requestId);
	runner.resume();
	assert.equal(calls.length, 2, "an in-flight requestId is never dispatched twice");

	// Out-of-order completion: second resolves first, then first.
	results.get(second.requestId)!.resolve(accepted("tab-2"));
	await flush();
	assert.equal(runner.jobs()[second.requestId].phase, "accepted", "the second job accepted on its own result");
	assert.equal(runner.jobs()[first.requestId].phase, "running", "the first job is untouched by the second completion");
	results.get(first.requestId)!.resolve(alreadyApplied("tab-1"));
	await flush();
	assert.equal(runner.jobs()[first.requestId].phase, "accepted", "the first job accepted on its own result");
	assert.equal(runner.jobs()[first.requestId].tabId, "tab-1", "the first job keeps its own tabId");
}

// --- multi-request queue with a late result: a queued call that settles long
// after a later one still advances ITS OWN job; there is no front-end flight
// timeout and no cross-job interference ---

{
	const results = new Map<string, ReturnType<typeof deferred<WidgetConversationResult>>>();
	const runner = createQuickStartJobRunner({
		deliver: (input) => {
			const d = deferred<WidgetConversationResult>();
			results.set(input.requestId, d);
			return d.promise;
		},
		storage: fakeStorage(),
		wait: async () => {},
	});
	const first = runner.submit(intent);
	const second = runner.submit({ ...intent, prompt: "queued second" });
	const third = runner.submit({ ...intent, prompt: "queued third" });
	if (!first.ok || !second.ok || !third.ok) throw new Error("submit failed");
	assert.equal(results.size, 3, "every queued submit owns an independent in-flight flight");
	// Later requests settle while the first is still queued behind the
	// backend's serialized start.
	results.get(third.requestId)!.resolve(accepted("tab-3"));
	await flush();
	results.get(second.requestId)!.resolve(accepted("tab-2"));
	await flush();
	assert.equal(runner.jobs()[second.requestId].phase, "accepted", "the second queued call accepted");
	assert.equal(runner.jobs()[third.requestId].phase, "accepted", "the third queued call accepted");
	assert.equal(runner.jobs()[first.requestId].phase, "running", "the first call is still running while queued (no timeout fence)");
	// The first call settles late: its accepted result advances the same job.
	results.get(first.requestId)!.resolve(accepted("tab-1"));
	await flush();
	assert.equal(runner.jobs()[first.requestId].phase, "accepted", "a late accepted result advances the same job");
	assert.equal(runner.jobs()[first.requestId].tabId, "tab-1", "the late result carries its own tabId");
	assert.equal(Object.keys(runner.jobs()).length, 3, "no job was deleted by any late result");
}

// --- no front-end flight timeout: the in-flight guard is held until the
// actual Promise settles, and a stuck bridge call never becomes a fake
// failure; the same requestId cannot be re-dispatched while in flight ---

{
	const calls: WidgetConversationInput[] = [];
	const stuck = deferred<WidgetConversationResult>();
	const runner = createQuickStartJobRunner({
		deliver: (input) => { calls.push(input); return stuck.promise; },
		storage: fakeStorage(),
		wait: async () => {},
	});
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	assert.equal(runner.jobs()[submitted.requestId].phase, "running", "the stuck flight stays running while the promise is unsettled");
	// Wait well past any plausible fixed budget; the backend serializes
	// conversation starts so a queued call can legitimately exceed it.
	await new Promise((resolve) => setTimeout(resolve, 120));
	runner.reconcile([]);
	assert.equal(runner.jobs()[submitted.requestId].phase, "running", "a long-running queued call is never fenced into a fake failure");
	assert.equal(calls.length, 1, "the in-flight guard is held until the promise settles: retry/resume do not double-dispatch");
	runner.retry(submitted.requestId);
	runner.resume();
	assert.equal(calls.length, 1, "retry/resume while in flight are no-ops (guard held)");
	stuck.resolve(accepted("tab-late"));
	await flush();
	assert.equal(runner.jobs()[submitted.requestId].phase, "accepted", "a late accepted result advances the same job");
	assert.equal(runner.jobs()[submitted.requestId].tabId, "tab-late", "the late receipt carries the real tabId");
}

// --- reload resumes the exact same requestId; the backend receipt answers
// already_applied without a duplicate job ---

{
	const storage = fakeStorage();
	const firstCalls: WidgetConversationInput[] = [];
	const pending = deferred<WidgetConversationResult>();
	const runner1 = createQuickStartJobRunner({
		deliver: async (input) => { firstCalls.push(input); return pending.promise; },
		storage,
		wait: async () => {},
	});
	const submitted = runner1.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");

	const resumeCalls: WidgetConversationInput[] = [];
	const runner2 = createQuickStartJobRunner({
		deliver: async (input) => { resumeCalls.push(input); return alreadyApplied("tab-1"); },
		storage,
		wait: async () => {},
	});
	assert.deepEqual(Object.keys(runner2.jobs()), [submitted.requestId], "the reloaded runner sees exactly one persisted job");
	runner2.resume();
	assert.equal(resumeCalls.length, 1, "reload resumes the nonterminal job");
	assert.equal(resumeCalls[0].requestId, submitted.requestId, "the resumed call replays the exact same requestId");
	assert.equal(resumeCalls[0].prompt, intent.prompt, "the resumed call replays the exact same frozen intent");
	await flush();
	const resumed = runner2.jobs()[submitted.requestId];
	assert.equal(resumed.phase, "accepted", "the backend receipt turns the replay into already_applied/accepted");
	assert.equal(resumed.tabId, "tab-1", "the resumed receipt carries the real tabId");
	runner2.reconcile([realTask("task:tab-1")]);
	assert.deepEqual(runner2.jobs(), {}, "the real icon from the refreshed snapshot clears the job (no duplicate)");
	// Settle runner1's still-pending flight so its in-flight entry is released.
	pending.resolve(accepted("tab-1"));
	await flush();
	assert.deepEqual(runner2.jobs(), {}, "the stale mount's late completion never resurrects the reconciled job");
}

// --- response lost: the transport retry replays the same input and the
// backend receipt answers already_applied without a second job ---

{
	const calls: WidgetConversationInput[] = [];
	let attempt = 0;
	const runner = createQuickStartJobRunner({
		deliver: async (input) => {
			calls.push(input);
			attempt += 1;
			if (attempt === 1) throw new Error("IPC response lost");
			return alreadyApplied("tab-9");
		},
		storage: fakeStorage(),
		wait: async () => {},
	});
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	await flush();
	assert.equal(calls.length, 2, "the lost response is retried with the exact same input");
	assert.equal(calls[0].requestId, calls[1].requestId, "every retry reuses the same requestId");
	const job = runner.jobs()[submitted.requestId];
	assert.equal(job.phase, "accepted", "the receipt answers already_applied and the job accepts");
	assert.equal(Object.keys(runner.jobs()).length, 1, "a response-lost retry never creates a duplicate job");
}

// --- a real failure is visible, persisted and retryable with the same
// requestId; editing creates a new requestId and replaces the failed job ---

{
	const calls: WidgetConversationInput[] = [];
	const runner = createQuickStartJobRunner({
		deliver: async (input) => { calls.push(input); throw new Error("workspace boot timeout"); },
		storage: fakeStorage(),
		wait: async () => {},
	});
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	await flush();
	const failed = runner.jobs()[submitted.requestId];
	assert.equal(failed.phase, "failed", "a terminal backend failure becomes a visible failed job");
	assert.match(failed.error ?? "", /workspace boot timeout/, "the preserved error is visible on the failed job");
	assert.deepEqual(failed.intent, intent, "the failed job keeps the frozen intent");

	const before = calls.length;
	runner.retry(submitted.requestId);
	assert.equal(calls.length, before + 1, "retry re-dispatches");
	assert.equal(calls[before].requestId, submitted.requestId, "retrying an unchanged intent reuses the same requestId");
	await flush();
	assert.equal(runner.jobs()[submitted.requestId].phase, "failed", "a failed retry stays visibly failed (no silent discard)");

	// Dismiss is allowed only for terminal (failed/accepted) entries.
	assert.equal(runner.dismiss(submitted.requestId), true, "a failed entry can be dismissed");
	assert.deepEqual(runner.jobs(), {}, "dismiss removes the failed entry");

	const second = runner.submit(intent);
	if (!second.ok) throw new Error("submit failed");
	await flush();
	assert.equal(runner.jobs()[second.requestId].phase, "failed", "a fresh submit is independent after dismissal");

	// Edit path: submitting an edited intent replaces the failed job with a
	// new requestId.
	const edited = runner.submit({ ...intent, prompt: "edited prompt" }, { replacesRequestId: second.requestId });
	if (!edited.ok) throw new Error("submit failed");
	assert.notEqual(edited.requestId, second.requestId, "editing the intent gets a NEW requestId");
	assert.ok(!runner.jobs()[second.requestId], "the failed source job is replaced by the edited submit");
	assert.ok(runner.jobs()[edited.requestId], "the edited job is enqueued");
}

{
	// A backend invalid (e.g. model no longer available) is also a visible
	// failure, never a silent toast-and-discard.
	const runner = createQuickStartJobRunner({
		deliver: async () => invalid("模型不在当前可用列表中"),
		storage: fakeStorage(),
		wait: async () => {},
	});
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	await flush();
	const job = runner.jobs()[submitted.requestId];
	assert.equal(job.phase, "failed", "an invalid backend result shows as a failed job");
	assert.match(job.error ?? "", /模型不在当前可用列表中/, "the invalid error stays visible");
	assert.equal(runner.dismiss(submitted.requestId), true, "the invalid job is dismissible");
}

// --- an accepted job is NEVER timer-evicted: it persists until the
// authoritative task:<tabId> icon appears or the user explicitly dismisses it
// (the old accepted-grace release is gone) ---

{
	const runner = createQuickStartJobRunner({
		deliver: async () => accepted("tab-ghost"),
		storage: fakeStorage(),
		wait: async () => {},
	});
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	await flush();
	assert.equal(runner.jobs()[submitted.requestId].phase, "accepted", "the job accepts normally");
	// Polls with unrelated/no real icons, repeated well past any old grace.
	runner.reconcile([realTask("task:other")]);
	await new Promise((resolve) => setTimeout(resolve, 120));
	runner.reconcile([]);
	runner.reconcile([realTask("task:other")]);
	assert.ok(runner.jobs()[submitted.requestId], "an accepted job whose real icon never appears is NEVER timer-evicted");
	assert.equal(runner.jobs()[submitted.requestId].tabId, "tab-ghost", "the accepted job keeps its real tabId for the handoff");
	// The user can explicitly dismiss an accepted job, and a running job can
	// NEVER be dismissed: the running optimistic job is the only durable
	// recovery intent until the backend receipt exists.
	const running = runner.submit({ ...intent, prompt: "still running" });
	if (!running.ok) throw new Error("submit failed");
	assert.equal(runner.dismiss(running.requestId), false, "a running job can never be dismissed (no delete of the recovery intent)");
	assert.ok(runner.jobs()[running.requestId], "the running job's ledger entry stays in the session view");
	assert.equal(runner.dismiss(submitted.requestId), true, "an accepted job is explicitly dismissible");
	assert.deepEqual(Object.keys(runner.jobs()), [running.requestId], "only the accepted job was dismissed; the running job survives");
	// The still-running flight eventually accepts and can then be dismissed.
	await flush();
	assert.equal(runner.jobs()[running.requestId].phase, "accepted", "the running job accepted once the backend receipt arrived");
	assert.equal(runner.dismiss(running.requestId), true, "accepted-with-tabId is dismissible");
	assert.deepEqual(runner.jobs(), {}, "the accepted dismissal leaves an empty ledger");
}

// --- a running job whose bridge call is slow is the ONLY durable recovery
// intent: it cannot be dismissed or deleted, the icon stays visible, and a
// late accepted result advances the same job ---

{
	const storage = fakeStorage();
	const stuck = deferred<WidgetConversationResult>();
	const runner = createQuickStartJobRunner({
		deliver: () => stuck.promise,
		storage,
		wait: async () => {},
	});
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	assert.equal(runner.jobs()[submitted.requestId].phase, "running", "the stuck flight stays running");
	assert.equal(runner.dismiss(submitted.requestId), false, "a running job can never be dismissed");
	assert.ok(runner.jobs()[submitted.requestId], "the running job stays in the session view");
	assert.ok(loadQuickStartJobs(storage)[submitted.requestId], "the running job stays in the durable ledger (recovery intent)");
	// The old flight settles late: it advances the same job.
	stuck.resolve(accepted("tab-late"));
	await flush();
	assert.equal(runner.jobs()[submitted.requestId].phase, "accepted", "a late accepted result advances the same running job");
	assert.equal(runner.jobs()[submitted.requestId].tabId, "tab-late", "the late receipt carries the real tabId");
	assert.equal(runner.dismiss(submitted.requestId), true, "once accepted with a real tabId, the entry may be dismissed");
	assert.deepEqual(runner.jobs(), {}, "the accepted dismissal leaves an empty ledger");
	assert.equal(loadQuickStartJobs(storage)[submitted.requestId], undefined, "the durable ledger no longer holds the dismissed job");
}

// --- cross-mount shared storage: one localStorage, two runner instances.
// merge-on-write: an old completion updates ONLY its own requestId and never
// overwrites a newer ledger or deletes a job submitted by a later mount ---

{
	const storage = fakeStorage();
	const flights: Array<ReturnType<typeof deferred<WidgetConversationResult>>> = [];
	const runner1 = createQuickStartJobRunner({
		deliver: () => { const d = deferred<WidgetConversationResult>(); flights.push(d); return d.promise; },
		storage,
		wait: async () => {},
	});
	const first = runner1.submit(intent);
	if (!first.ok) throw new Error("submit failed");
	assert.equal(flights.length, 1, "mount 1 dispatched job A");

	// A fresh mount (e.g. widget remount) loads the same durable ledger and
	// submits job B while A's older flight is still pending.
	const runner2 = createQuickStartJobRunner({
		deliver: async () => accepted("tab-B"),
		storage,
		wait: async () => {},
	});
	assert.deepEqual(Object.keys(runner2.jobs()), [first.requestId], "mount 2 sees the exact same persisted ledger");
	const second = runner2.submit({ ...intent, prompt: "task B" });
	if (!second.ok) throw new Error("submit failed");
	assert.equal(loadQuickStartJobs(storage)[second.requestId]?.phase, "running", "mount 2's job is durably merged beside A");

	// B settles first (newer ledger), then A's OLD completion settles late
	// while it is still the current generation: the merge-on-write transition
	// must update ONLY A and keep B.
	await flush();
	assert.equal(runner2.jobs()[second.requestId].phase, "accepted", "B accepted on the newer mount");
	flights[0].resolve(accepted("tab-A"));
	await flush();
	const durable = loadQuickStartJobs(storage);
	assert.equal(durable[first.requestId]?.phase, "accepted", "the old completion advanced A");
	assert.equal(durable[first.requestId]?.tabId, "tab-A", "A keeps its own tabId");
	assert.equal(durable[second.requestId]?.phase, "accepted", "the old completion never deleted the newer job B");
	assert.ok(runner1.jobs()[second.requestId], "the stale mount also sees B through the merged ledger");
}

// --- cross-mount shared storage: a newer explicit retry generation owns a
// re-dispatched requestId, so an old async completion can never resurrect a
// job that a newer mount already reconciled ---

{
	const storage = fakeStorage();
	const r1Flights: Array<ReturnType<typeof deferred<WidgetConversationResult>>> = [];
	const r2Flights: Array<ReturnType<typeof deferred<WidgetConversationResult>>> = [];
	const runner1 = createQuickStartJobRunner({
		deliver: () => { const d = deferred<WidgetConversationResult>(); r1Flights.push(d); return d.promise; },
		storage,
		wait: async () => {},
	});
	const first = runner1.submit(intent);
	if (!first.ok) throw new Error("submit failed");
	assert.equal(r1Flights.length, 1, "mount 1 dispatched job A");

	const runner2 = createQuickStartJobRunner({
		deliver: () => { const d = deferred<WidgetConversationResult>(); r2Flights.push(d); return d.promise; },
		storage,
		wait: async () => {},
	});
	// Mount 2 resumes A (interrupted delivery): the newer dispatch generation
	// owns the requestId from now on.
	runner2.resume();
	assert.equal(r2Flights.length, 1, "mount 2 resumed the same requestId");

	// Mount 2's A flight accepts with its real tabId, then the accepted job
	// hands off to its real task icon.
	r2Flights[0].resolve(alreadyApplied("tab-A"));
	await flush();
	assert.equal(runner2.jobs()[first.requestId].phase, "accepted", "the newer A generation accepted");
	assert.equal(runner2.jobs()[first.requestId].tabId, "tab-A", "the newer A generation keeps its own tabId");
	runner2.reconcile([realTask("task:tab-A")]);
	assert.ok(!runner2.jobs()[first.requestId], "accepted job A handed off to its real icon");

	// Mount 1's OLD A flight settles late: the newer explicit retry
	// generation owns the requestId, so the old completion is dropped and can
	// never resurrect reconciled task A (neither in memory on the live mount
	// nor in the durable ledger).
	r1Flights[0].resolve(accepted("tab-A-old"));
	await flush();
	assert.ok(!runner2.jobs()[first.requestId], "the old completion never resurrects reconciled task A on the live mount");
	assert.equal(loadQuickStartJobs(storage)[first.requestId], undefined, "the durable ledger never resurrects reconciled task A");
}

// --- throwing storage: failures are explicit and recoverable ---

{
	// A background accept that cannot persist still advances the in-memory
	// session view, surfaces a recoverable error, and self-heals on reload
	// (the backend receipt answers already_applied for the same requestId).
	const storage = fakeStorage();
	const errors: string[] = [];
	const runner = createQuickStartJobRunner({
		deliver: async () => accepted("tab-1"),
		storage,
		wait: async () => {},
	});
	runner.subscribeErrors((message, context) => errors.push(`${context}:${message}`));
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	storage.setFail = true;
	await flush();
	assert.equal(runner.jobs()[submitted.requestId].phase, "accepted", "a background transition keeps the in-memory session view when the durable save fails");
	assert.equal(errors.length, 1, "the background storage failure is surfaced");
	assert.match(errors[0], /^background:/, "the failure is tagged with its context");
	storage.setFail = false;
	const reloaded = createQuickStartJobRunner({
		deliver: async () => alreadyApplied("tab-1"),
		storage,
		wait: async () => {},
	});
	assert.equal(reloaded.jobs()[submitted.requestId].phase, "running", "the failed durable accept did not persist");
	reloaded.resume();
	await flush();
	assert.equal(reloaded.jobs()[submitted.requestId].phase, "accepted", "reload re-dispatches and the backend receipt answers already_applied (self-healing)");
}

{
	// Dismiss removes the UI only after the durable save succeeds; a save
	// failure keeps the entry visible and surfaces a recoverable error.
	const storage = fakeStorage();
	const errors: string[] = [];
	const runner = createQuickStartJobRunner({
		deliver: async () => { throw new Error("boom"); },
		storage,
		wait: async () => {},
	});
	runner.subscribeErrors((message, context) => errors.push(`${context}:${message}`));
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	await flush();
	assert.equal(runner.jobs()[submitted.requestId].phase, "failed", "the job failed so it can be dismissed");
	storage.setFail = true;
	assert.equal(runner.dismiss(submitted.requestId), false, "dismiss returns false when the durable save fails");
	assert.ok(runner.jobs()[submitted.requestId], "a failed durable dismiss keeps the icon visible");
	assert.equal(errors.length, 1, "the dismiss failure is surfaced");
	assert.match(errors[0], /^dismiss:/, "the dismiss failure is tagged with its context");
	storage.setFail = false;
	assert.equal(runner.dismiss(submitted.requestId), true, "dismiss succeeds once storage recovers");
	assert.deepEqual(runner.jobs(), {}, "the recovered dismiss removes the entry");
}

{
	// Reconcile with an unavailable ledger still reconciles the in-memory view
	// and surfaces the storage failure instead of swallowing it.
	const storage = fakeStorage();
	const errors: string[] = [];
	const runner = createQuickStartJobRunner({
		deliver: async () => accepted("tab-1"),
		storage,
		wait: async () => {},
	});
	runner.subscribeErrors((message, context) => errors.push(`${context}:${message}`));
	const submitted = runner.submit(intent);
	if (!submitted.ok) throw new Error("submit failed");
	await flush();
	assert.equal(runner.jobs()[submitted.requestId].phase, "accepted", "the job accepted");
	storage.setFail = true;
	runner.reconcile([realTask("task:tab-1")]);
	assert.ok(!runner.jobs()[submitted.requestId], "reconcile still clears the handed-off job in memory");
	assert.equal(errors.length, 1, "the reconcile storage failure is surfaced");
	assert.match(errors[0], /^reconcile:/, "the reconcile failure is tagged with its context");
}

// --- polling cannot erase optimistic jobs; a corrupted ledger is safe ---

{
	const results = new Map<string, ReturnType<typeof deferred<WidgetConversationResult>>>();
	const runner = createQuickStartJobRunner({
		deliver: (input) => {
			const d = deferred<WidgetConversationResult>();
			results.set(input.requestId, d);
			return d.promise;
		},
		storage: fakeStorage(),
		wait: async () => {},
	});
	const first = runner.submit(intent);
	const second = runner.submit({ ...intent, prompt: "second" });
	if (!first.ok || !second.ok) throw new Error("submit failed");
	runner.reconcile([]);
	runner.reconcile([realTask("task:unrelated")]);
	assert.ok(runner.jobs()[first.requestId] && runner.jobs()[second.requestId], "authoritative polls never erase running jobs");
	results.get(first.requestId)!.resolve(accepted("tab-1"));
	await flush();
	runner.reconcile([realTask("task:tab-1")]);
	assert.ok(!runner.jobs()[first.requestId], "the accepted job hands off to its real icon");
	assert.ok(runner.jobs()[second.requestId], "an older poll for another job never deletes the newer job");
	results.get(second.requestId)!.resolve(accepted("tab-2"));
	await flush();
	runner.reconcile([realTask("task:tab-2")]);
	assert.ok(!runner.jobs()[second.requestId], "each job reconciles independently against its own real icon");
}

{
	const storage = fakeStorage({ [QUICK_JOBS_KEY]: "corrupted{{{", [QUICK_LEGACY_PENDING_KEY]: "also bad" });
	const runner = createQuickStartJobRunner({
		deliver: async () => accepted("t"),
		storage,
		wait: async () => {},
	});
	const outcome = runner.submit(intent);
	assert.equal(outcome.ok, true, "a corrupted ledger does not block a new submit");
}

// --- storage-error dirty overlay: a background transition that cannot
// persist keeps its per-request intent so a LATER successful write merges
// fresh durable state + pending intents + its own operation. An
// accepted/failed phase can never regress to running, and recovery never
// deletes another job ---

{
	// Accepted save fails, then an UNRELATED commit: the pending accept must
	// be replayed over the fresh durable state, never regressed back to
	// running, and the unrelated job is still handled on its own.
	const storage = fakeStorage();
	const flights = new Map<string, ReturnType<typeof deferred<WidgetConversationResult>>>();
	const runner = createQuickStartJobRunner({
		deliver: (input) => { const d = deferred<WidgetConversationResult>(); flights.set(input.requestId, d); return d.promise; },
		storage,
		wait: async () => {},
	});
	const a = runner.submit(intent);
	if (!a.ok) throw new Error("submit failed");
	const b = runner.submit({ ...intent, prompt: "job B" });
	if (!b.ok) throw new Error("submit failed");
	// A accepts and persists normally.
	flights.get(a.requestId)!.resolve(accepted("tab-A"));
	await flush();
	assert.equal(loadQuickStartJobs(storage)[a.requestId].phase, "accepted", "A persisted");
	// B accepts while storage is down: the session view advances, the durable
	// save fails, and the accept intent is kept pending.
	storage.setFail = true;
	flights.get(b.requestId)!.resolve(accepted("tab-B"));
	await flush();
	assert.equal(runner.jobs()[b.requestId].phase, "accepted", "the session view shows B accepted despite the failed save");
	assert.equal(loadQuickStartJobs(storage)[b.requestId].phase, "running", "the durable ledger still has B running (save failed)");
	storage.setFail = false;
	// C is submitted after recovery and accepts; then an UNRELATED reconcile
	// (C's real icon appears) writes the ledger again.
	const c = runner.submit({ ...intent, prompt: "job C" });
	if (!c.ok) throw new Error("submit failed");
	flights.get(c.requestId)!.resolve(accepted("tab-C"));
	await flush();
	assert.equal(loadQuickStartJobs(storage)[c.requestId].phase, "accepted", "C persisted after recovery");
	runner.reconcile([realTask("task:tab-C")]);
	const durable = loadQuickStartJobs(storage);
	assert.equal(durable[b.requestId].phase, "accepted", "the unrelated commit replayed B's pending accept instead of regressing it to running");
	assert.equal(durable[b.requestId].tabId, "tab-B", "B keeps its own tabId");
	assert.equal(durable[a.requestId].phase, "accepted", "A was never touched by the unrelated commit");
	assert.equal(durable[a.requestId].tabId, "tab-A", "A keeps its own tabId");
	assert.equal(durable[c.requestId], undefined, "the unrelated reconcile removed only its own job C");
	assert.equal(runner.jobs()[b.requestId].phase, "accepted", "the memory view keeps B accepted");
}

{
	// A failed phase whose save fails must never regress to running on a later
	// successful write (the pending "failed" intent is replayed).
	const storage = fakeStorage();
	const errors: string[] = [];
	const stuck = deferred<WidgetConversationResult>();
	const runner = createQuickStartJobRunner({
		deliver: () => stuck.promise,
		storage,
		wait: async () => {},
	});
	runner.subscribeErrors((message, context) => errors.push(`${context}:${message}`));
	const a = runner.submit(intent);
	if (!a.ok) throw new Error("submit failed");
	storage.setFail = true;
	stuck.reject(new Error("boom"));
	await flush();
	assert.equal(runner.jobs()[a.requestId].phase, "failed", "the session view shows failed");
	assert.equal(loadQuickStartJobs(storage)[a.requestId].phase, "running", "the durable ledger did not persist the failure");
	assert.equal(errors.length, 1, "the background storage failure is surfaced");
	storage.setFail = false;
	runner.reconcile([]); // unrelated successful write replays the pending intent
	const durable = loadQuickStartJobs(storage);
	assert.equal(durable[a.requestId].phase, "failed", "a pending failed transition is replayed, never regressed to running");
	assert.match(durable[a.requestId].error ?? "", /boom/, "the pending failure keeps its visible error");
}

{
	// getItem throwing: an unreadable ledger must never be overwritten from a
	// fabricated empty base — submit refuses without dispatch, background
	// transitions keep the session view and surface the error, and once the
	// ledger is readable again the pending intent merges without deleting the
	// seeded durable jobs.
	const storage = fakeStorage();
	const seed: QuickStartJobs = {
		seed1: { requestId: "seed1", intent, phase: "accepted", tabId: "tab-seed", createdAt: 1, updatedAt: 2 },
		seed2: { requestId: "seed2", intent, phase: "running", createdAt: 1, updatedAt: 2 },
	};
	saveQuickStartJobs(storage, seed);
	const errors: string[] = [];
	const runner = createQuickStartJobRunner({
		deliver: async () => accepted("tab-1"),
		storage,
		wait: async () => {},
	});
	runner.subscribeErrors((message, context) => errors.push(`${context}:${message}`));
	const before = storage.map.get(QUICK_JOBS_KEY);
	storage.getFail = true;
	assert.throws(() => readQuickStartJobs(storage), /storage read failed/, "the write-path read throws on an unreadable ledger (distinct from an empty one)");
	const submitted = runner.submit(intent);
	assert.equal(submitted.ok, false, "submit fails without dispatch when the ledger cannot be read");
	assert.match(submitted.ok ? "" : submitted.error, /storage read failed/, "the read failure is the visible error");
	assert.deepEqual(runner.jobs(), seed, "the failed submit enqueues nothing new (the runner's loaded seed view is untouched)");
	assert.equal(storage.map.get(QUICK_JOBS_KEY), before, "a failed read never writes over durable data");
	assert.equal(errors.length, 0, "a refused submit is a returned error, not a background storage error");
	storage.getFail = false;
	const outcome = runner.submit(intent);
	assert.equal(outcome.ok, true, "submit recovers once the ledger is readable");
	const afterSubmit = storage.map.get(QUICK_JOBS_KEY);
	storage.getFail = true;
	await flush(); // the accept transition's read fails too
	assert.equal(runner.jobs()[outcome.requestId].phase, "accepted", "the session view advanced despite the unreadable ledger");
	assert.equal(errors.length, 1, "the background read failure is surfaced");
	assert.match(errors[0], /^background:/, "the failure is tagged with its context");
	assert.equal(storage.map.get(QUICK_JOBS_KEY), afterSubmit, "no write happened against an unreadable ledger");
	storage.getFail = false;
	runner.reconcile([]); // unrelated successful commit replays the pending accept
	const durable = loadQuickStartJobs(storage);
	assert.equal(durable[outcome.requestId].phase, "accepted", "the pending accept merged into the fresh durable state");
	assert.ok(durable.seed1 && durable.seed2, "the seeded durable jobs survived (no fabricated-empty overwrite)");
	assert.equal(durable.seed1.tabId, "tab-seed", "the seeded accepted job keeps its data");
}

{
	// A failed dismiss must be a FAILED action: no remove tombstone is
	// recorded (a stale tombstone would erase a later retry's durable running
	// entry), the job stays visible, and an explicit retry after storage
	// recovery durably upserts the captured job as running before dispatch
	// (exactly one running ledger entry, backend dispatch once, accepted
	// result applies).
	const storage = fakeStorage();
	const errors: string[] = [];
	const calls: WidgetConversationInput[] = [];
	const pending = deferred<WidgetConversationResult>();
	const runner = createQuickStartJobRunner({
		deliver: (input) => {
			calls.push(input);
			if (calls.length === 1) return Promise.resolve(invalid("boom"));
			return pending.promise;
		},
		storage,
		wait: async () => {},
	});
	runner.subscribeErrors((message, context) => errors.push(`${context}:${message}`));
	const a = runner.submit(intent);
	if (!a.ok) throw new Error("submit failed");
	await flush();
	assert.equal(runner.jobs()[a.requestId].phase, "failed", "the first attempt fails visibly");
	assert.equal(loadQuickStartJobs(storage)[a.requestId].phase, "failed", "the failed phase persisted");
	storage.setFail = true;
	assert.equal(runner.dismiss(a.requestId), false, "a failed durable dismiss is a failed action");
	assert.ok(runner.jobs()[a.requestId], "the failed dismiss keeps the job visible");
	assert.ok(loadQuickStartJobs(storage)[a.requestId], "the durable ledger still holds the entry");
	assert.equal(errors.length, 1, "the dismiss failure is surfaced");
	assert.match(errors[0], /^dismiss:/, "the dismiss failure is tagged with its context");
	storage.setFail = false;
	runner.reconcile([]); // an unrelated successful write would replay a tombstone if one existed
	assert.ok(runner.jobs()[a.requestId], "no remove tombstone was left: the unrelated commit did not erase the job");
	assert.ok(loadQuickStartJobs(storage)[a.requestId], "the durable entry survives the unrelated commit");
	// Retry after storage recovery: durably restore the captured job as
	// running BEFORE dispatch, then dispatch exactly once.
	const before = calls.length;
	runner.retry(a.requestId);
	assert.equal(calls.length, before + 1, "retry dispatches exactly once");
	const durableRetry = loadQuickStartJobs(storage);
	assert.deepEqual(Object.keys(durableRetry), [a.requestId], "exactly one running ledger entry");
	assert.equal(durableRetry[a.requestId].phase, "running", "the retry durably restored the captured job as running");
	assert.equal(durableRetry[a.requestId].error, undefined, "the retry cleared the failed error");
	pending.resolve(accepted("tab-1"));
	await flush();
	assert.equal(runner.jobs()[a.requestId].phase, "accepted", "the accepted result applies to the retried job");
	assert.equal(runner.jobs()[a.requestId].tabId, "tab-1", "the accepted result carries the real tabId");
	assert.equal(loadQuickStartJobs(storage)[a.requestId].phase, "accepted", "the accepted result persisted");
}

{
	// Explicit retry must NOT dispatch when the durable restore-to-running
	// cannot persist: nothing is sent, the failure is surfaced, and once
	// storage recovers the same retry dispatches and persists the running
	// entry.
	const storage = fakeStorage();
	const errors: string[] = [];
	const calls: WidgetConversationInput[] = [];
	const runner = createQuickStartJobRunner({
		deliver: (input) => { calls.push(input); return Promise.resolve(invalid("boom")); },
		storage,
		wait: async () => {},
	});
	runner.subscribeErrors((message, context) => errors.push(`${context}:${message}`));
	const a = runner.submit(intent);
	if (!a.ok) throw new Error("submit failed");
	await flush();
	assert.equal(runner.jobs()[a.requestId].phase, "failed", "the job failed");
	const before = calls.length;
	storage.setFail = true;
	runner.retry(a.requestId);
	assert.equal(calls.length, before, "a retry whose durable restore fails never dispatches");
	assert.equal(errors.length, 1, "the retry persistence failure is surfaced");
	assert.match(errors[0], /^background:/, "the retry failure is tagged as background");
	storage.setFail = false;
	assert.equal(loadQuickStartJobs(storage)[a.requestId].phase, "failed", "the durable entry stayed failed (no restore, no dispatch)");
	runner.retry(a.requestId);
	assert.equal(calls.length, before + 1, "retry dispatches once storage recovers");
	assert.equal(loadQuickStartJobs(storage)[a.requestId].phase, "running", "the recovered retry durably restored the running entry");
}

{
	// Durable failed + no dirty intent: a retry save failure must remove the
	// generic fallback's newly-created running dirty intent. Automatic
	// reconcile after recovery keeps the job failed, and another explicit
	// retry remains available.
	const storage = fakeStorage();
	const requestId = "icon-new:durable-failed";
	const failed: QuickStartJob = { requestId, intent, phase: "failed", error: "boom", createdAt: 1, updatedAt: 2 };
	saveQuickStartJobs(storage, { [requestId]: failed });
	const calls: WidgetConversationInput[] = [];
	const flight = deferred<WidgetConversationResult>();
	const runner = createQuickStartJobRunner({
		deliver: (input) => { calls.push(input); return flight.promise; },
		storage,
		wait: async () => {},
	});
	storage.setFail = true;
	runner.retry(requestId);
	assert.equal(calls.length, 0, "a retry whose running save failed did not dispatch");
	assert.equal(runner.jobs()[requestId].phase, "failed", "memory keeps the captured durable failure");
	storage.setFail = false;
	runner.reconcile([]);
	assert.equal(loadQuickStartJobs(storage)[requestId].phase, "failed", "automatic reconcile did not manufacture durable running without a flight");
	assert.equal(runner.jobs()[requestId].phase, "failed", "reconciled memory remains failed and retryable");
	runner.retry(requestId);
	assert.equal(calls.length, 1, "the same failed job can retry after storage recovery");
	assert.equal(loadQuickStartJobs(storage)[requestId].phase, "running", "the successful retry persists running before dispatch");
	flight.resolve(accepted("tab-durable"));
	await flush();
}

{
	// Dirty failed: the original failed phase came from a background save
	// failure while durable storage still says running. A subsequent retry save
	// failure must restore that exact dirty failed intent. Reconcile then
	// persists failed (never no-flight running), and retry remains possible.
	const storage = fakeStorage();
	const calls: WidgetConversationInput[] = [];
	const first = deferred<WidgetConversationResult>();
	const second = deferred<WidgetConversationResult>();
	const runner = createQuickStartJobRunner({
		deliver: (input) => {
			calls.push(input);
			return calls.length === 1 ? first.promise : second.promise;
		},
		storage,
		wait: async () => {},
	});
	const outcome = runner.submit(intent);
	if (!outcome.ok) throw new Error("submit failed");
	storage.setFail = true;
	first.resolve(invalid("dirty boom"));
	await flush();
	assert.equal(runner.jobs()[outcome.requestId].phase, "failed", "memory captured the failed background result");
	assert.equal(loadQuickStartJobs(storage)[outcome.requestId].phase, "running", "durable storage still has pre-failure running");
	runner.retry(outcome.requestId);
	assert.equal(calls.length, 1, "retry save failure did not launch a second flight");
	assert.equal(runner.jobs()[outcome.requestId].phase, "failed", "retry save failure keeps captured failed memory");
	storage.setFail = false;
	runner.reconcile([]);
	const reconciled = loadQuickStartJobs(storage)[outcome.requestId];
	assert.equal(reconciled.phase, "failed", "automatic reconcile replayed the original dirty failed intent");
	assert.match(reconciled.error ?? "", /dirty boom/, "the original dirty failure detail survives retry save failure");
	runner.retry(outcome.requestId);
	assert.equal(calls.length, 2, "dirty failed job remains retryable after reconcile recovery");
	assert.equal(loadQuickStartJobs(storage)[outcome.requestId].phase, "running", "the later successful retry durably starts its flight");
	second.resolve(accepted("tab-dirty"));
	await flush();
}

{
	// A reconcile removal that cannot persist is recorded as a dirty
	// tombstone (DISTINCT from a failed explicit dismiss, which records
	// nothing): once storage recovers, an unrelated commit/reconcile replays
	// the tombstone and persists the removal, and the optimistic job can
	// never resurrect — even when later snapshots filter/cap the real icon.
	const storage = fakeStorage();
	const errors: string[] = [];
	const runner = createQuickStartJobRunner({
		deliver: async () => accepted("tab-1"),
		storage,
		wait: async () => {},
	});
	runner.subscribeErrors((message, context) => errors.push(`${context}:${message}`));
	const a = runner.submit(intent);
	if (!a.ok) throw new Error("submit failed");
	await flush();
	assert.equal(loadQuickStartJobs(storage)[a.requestId].phase, "accepted", "the accepted job persisted");
	// The real task:<tabId> icon is observed while storage is down: the
	// handoff removal happens in memory, the save fails, and the removal is
	// kept as a tombstone.
	storage.setFail = true;
	runner.reconcile([realTask("task:tab-1")]);
	assert.ok(!runner.jobs()[a.requestId], "the accepted job handed off in the session view");
	assert.ok(loadQuickStartJobs(storage)[a.requestId], "the durable ledger still holds the accepted job (save failed)");
	assert.equal(errors.length, 1, "the reconcile failure is surfaced");
	assert.match(errors[0], /^reconcile:/, "the reconcile failure is tagged with its context");
	// A no-real snapshot while storage is down changes nothing (the tombstone
	// is already pending).
	runner.reconcile([]);
	assert.ok(!runner.jobs()[a.requestId], "a no-real snapshot keeps the handed-off view");
	// Storage recovers: an unrelated commit/reconcile replays the tombstone
	// and persists the removal.
	storage.setFail = false;
	runner.reconcile([realTask("task:other")]);
	assert.equal(loadQuickStartJobs(storage)[a.requestId], undefined, "the unrelated reconcile persisted the removal");
	// Later snapshots (real icon filtered/capped out) can never resurrect it.
	runner.reconcile([]);
	runner.reconcile([realTask("task:other")]);
	assert.ok(!runner.jobs()[a.requestId], "the optimistic job never returns");
	assert.equal(loadQuickStartJobs(storage)[a.requestId], undefined, "the durable ledger never resurrects the job");
}

// --- consumed-draft marker: durable, versioned, keyed to the submitted
// draft/requestId; removal failures are retried by the next mount; identical
// future drafts are never permanently suppressed ---

{
	const storage = fakeStorage();
	assert.equal(readConsumedDraftMarker(storage), null, "no marker by default");
	recordConsumedDraftMarker(storage, "  fix it  ", "icon-new:1");
	assert.deepEqual(readConsumedDraftMarker(storage), { version: 1, prompt: "fix it", requestId: "icon-new:1" }, "the marker normalizes the prompt and keeps the requestId");
	clearConsumedDraftMarker(storage);
	assert.equal(readConsumedDraftMarker(storage), null, "clearing removes the marker");
	// corrupt / future-version markers fail open (no suppression, no crash)
	storage.map.set(QUICK_CONSUMED_DRAFT_KEY, "not json{{{");
	assert.equal(readConsumedDraftMarker(storage), null, "a corrupt marker is ignored");
	storage.map.set(QUICK_CONSUMED_DRAFT_KEY, JSON.stringify({ version: 2, prompt: "x", requestId: "y" }));
	assert.equal(readConsumedDraftMarker(storage), null, "a future version is ignored (fail open)");
	// marker storage errors are explicit (visible at the call site)
	storage.map.delete(QUICK_CONSUMED_DRAFT_KEY);
	storage.getFail = true;
	assert.throws(() => readConsumedDraftMarker(storage), /storage read failed/, "a marker read failure is explicit so callers surface it");
	storage.getFail = false;
	storage.setFail = true;
	assert.throws(() => recordConsumedDraftMarker(storage, "x", "r"), /storage quota exceeded/, "a marker write failure is explicit");
}

{
	// Marker match: the pure decision suppresses the exact stale draft and
	// flags a pending cleanup WITHOUT writing anything (no render-time
	// mutation); the idempotent cleanup then removes the draft and clears the
	// marker.
	const storage = fakeStorage({
		[QUICK_DRAFT_KEY]: "fix the widget",
		[QUICK_CONSUMED_DRAFT_KEY]: JSON.stringify({ version: 1, prompt: "fix the widget", requestId: "icon-new:1" }),
	});
	const before = new Map(storage.map);
	const decision = decideConsumedDraft(storage, "fix the widget", new Map());
	assert.deepEqual(decision, { draft: "", cleanupPending: true }, "the exact stale draft is suppressed with a pending cleanup");
	assert.deepEqual([...storage.map], [...before], "the pure decision performs NO storage writes (an aborted render cannot mutate)");
	assert.equal(cleanupConsumedDraft(storage), true, "the committed cleanup succeeds");
	assert.equal(storage.map.has(QUICK_DRAFT_KEY), false, "the cleanup removed the stale draft");
	assert.equal(storage.map.has(QUICK_CONSUMED_DRAFT_KEY), false, "the cleanup cleared the marker");
}

{
	// Cleanup failure keeps the marker so the next open retries; a retried
	// cleanup after recovery succeeds.
	const storage = fakeStorage({
		[QUICK_DRAFT_KEY]: "fix the widget",
		[QUICK_CONSUMED_DRAFT_KEY]: JSON.stringify({ version: 1, prompt: "fix the widget", requestId: "icon-new:1" }),
	});
	storage.removeFail = true;
	assert.equal(cleanupConsumedDraft(storage), false, "a failing removal is reported as failed");
	assert.equal(storage.map.get(QUICK_DRAFT_KEY), "fix the widget", "the stale draft remains for the retry");
	assert.equal(storage.map.has(QUICK_CONSUMED_DRAFT_KEY), true, "the marker persists so the next open retries");
	storage.removeFail = false;
	assert.equal(cleanupConsumedDraft(storage), true, "the retried cleanup succeeds after recovery");
	assert.equal(storage.map.has(QUICK_DRAFT_KEY), false, "the retried removal removed the draft");
	assert.equal(storage.map.has(QUICK_CONSUMED_DRAFT_KEY), false, "the marker is cleared after the successful retry");
}

{
	// StrictMode double effect safety: the cleanup is idempotent — a second
	// run after success is a no-op and still reports success.
	const storage = fakeStorage({
		[QUICK_DRAFT_KEY]: "fix the widget",
		[QUICK_CONSUMED_DRAFT_KEY]: JSON.stringify({ version: 1, prompt: "fix the widget", requestId: "icon-new:1" }),
	});
	assert.equal(cleanupConsumedDraft(storage), true, "the first effect run cleans up");
	assert.equal(cleanupConsumedDraft(storage), true, "the StrictMode double effect run is a no-op success");
	assert.equal(storage.map.has(QUICK_DRAFT_KEY), false, "no draft residue remains");
	assert.equal(storage.map.has(QUICK_CONSUMED_DRAFT_KEY), false, "no marker residue remains");
}

{
	// An active ledger job with the exact same prompt suppresses the stored
	// draft without needing a marker (the submit just enqueued it) — and the
	// pure decision still never writes.
	const storage = fakeStorage({ [QUICK_DRAFT_KEY]: "fix the widget" });
	const decision = decideConsumedDraft(storage, "fix the widget", new Map([["fix the widget", "icon-new:active"]]));
	assert.deepEqual(decision, {
		draft: "",
		cleanupPending: true,
		cleanupMarker: { version: 1, prompt: "fix the widget", requestId: "icon-new:active" },
	}, "an already-enqueued prompt is suppressed and schedules committed marker+draft cleanup");
	assert.equal(storage.map.has(QUICK_DRAFT_KEY), true, "the pure decision never removes the draft");
}

{
	// Submit succeeded, but BOTH the consumed-marker write and draft removal
	// failed. The active ledger requestId makes the remount decision suppress
	// the draft and schedule committed cleanup. The first cleanup still fails;
	// after storage recovery a later remount writes the marker first, deletes
	// the draft, and leaves nothing that can be reoffered or resubmitted.
	const storage = fakeStorage({ [QUICK_DRAFT_KEY]: "fix the widget" });
	storage.setFail = true;
	storage.removeFail = true;
	assert.throws(() => recordConsumedDraftMarker(storage, "fix the widget", "icon-new:active"), /quota/, "the submit marker write failed");
	assert.throws(() => storage.removeItem(QUICK_DRAFT_KEY), /remove failed/, "the submit draft removal failed too");
	const active = new Map([["fix the widget", "icon-new:active"]]);
	const firstMount = decideConsumedDraft(storage, storage.getItem(QUICK_DRAFT_KEY) || "", active);
	assert.equal(firstMount.draft, "", "the first remount never reoffers the already-enqueued draft");
	assert.equal(firstMount.cleanupPending, true, "the active ledger match schedules cleanup despite the missing marker");
	assert.equal(cleanupConsumedDraft(storage, firstMount.cleanupMarker), false, "cleanup reports the still-failing marker write");
	assert.equal(storage.map.get(QUICK_DRAFT_KEY), "fix the widget", "the draft remains until durable cleanup is possible");

	storage.setFail = false;
	storage.removeFail = false;
	const recoveredMount = decideConsumedDraft(storage, storage.getItem(QUICK_DRAFT_KEY) || "", active);
	assert.equal(recoveredMount.draft, "", "the recovery remount still suppresses the stale draft");
	assert.equal(cleanupConsumedDraft(storage, recoveredMount.cleanupMarker), true, "recovery writes the marker then removes the draft");
	assert.equal(storage.map.has(QUICK_DRAFT_KEY), false, "the recovered cleanup removed the stale draft");
	assert.equal(storage.map.has(QUICK_CONSUMED_DRAFT_KEY), false, "the recovered cleanup cleared its marker after removal");
	assert.deepEqual(decideConsumedDraft(storage, storage.getItem(QUICK_DRAFT_KEY) || "", active), { draft: "", cleanupPending: false }, "a later remount has nothing to reoffer or resubmit");
}

{
	// A marker that no longer matches the current draft is stale: the draft
	// is offered normally and the cleanup only clears the marker, so an
	// intentionally identical FUTURE draft is never permanently suppressed.
	const storage = fakeStorage({
		[QUICK_DRAFT_KEY]: "a brand new draft",
		[QUICK_CONSUMED_DRAFT_KEY]: JSON.stringify({ version: 1, prompt: "old submitted text", requestId: "icon-new:1" }),
	});
	const decision = decideConsumedDraft(storage, "a brand new draft", new Map());
	assert.deepEqual(decision, { draft: "a brand new draft", cleanupPending: true }, "a new draft is offered normally with a pending marker cleanup");
	assert.equal(cleanupConsumedDraft(storage), true, "the cleanup clears the stale marker");
	assert.equal(storage.map.has(QUICK_CONSUMED_DRAFT_KEY), false, "the stale marker is gone");
	assert.equal(storage.map.get(QUICK_DRAFT_KEY), "a brand new draft", "the new draft itself is untouched");
	// No marker at all: the draft is the user's own input.
	const plain = fakeStorage({ [QUICK_DRAFT_KEY]: "fix the widget" });
	assert.deepEqual(decideConsumedDraft(plain, "fix the widget", new Map()), { draft: "fix the widget", cleanupPending: false }, "an unmarked draft is offered normally");
	// An unreadable marker fails open: the draft is offered, nothing crashes,
	// and the cleanup reports failure instead of deleting blindly.
	const broken = fakeStorage({ [QUICK_DRAFT_KEY]: "fix the widget" });
	broken.getFail = true;
	assert.deepEqual(decideConsumedDraft(broken, "fix the widget", new Map()), { draft: "fix the widget", cleanupPending: false }, "an unreadable marker fails open without suppressing");
	assert.equal(cleanupConsumedDraft(broken), false, "an unreadable marker makes the cleanup report failure (retry later)");
}

// --- accepted-job open-task gate: the exact tabId is passed at most once
// (double-click gate), only accepted jobs with a real tabId pass, and a failed
// exit rejects so the caller keeps it visible/retryable ---

{
	const gate = createQuickStartOpenTaskGate();
	const exitCalls: string[] = [];
	const job: QuickStartJob = { requestId: "r", intent, phase: "accepted", tabId: "tab-42", createdAt: 1, updatedAt: 2 };
	const exit = async (tabId: string) => { exitCalls.push(tabId); };
	void gate.open(job, exit);
	void gate.open(job, exit);
	assert.deepEqual(exitCalls, ["tab-42"], "the accepted job's tabId is passed exactly once (double-click gate)");
}

{
	const gate = createQuickStartOpenTaskGate();
	const exitCalls: string[] = [];
	const exit = async (tabId: string) => { exitCalls.push(tabId); };
	void gate.open({ requestId: "r", intent, phase: "running", createdAt: 1, updatedAt: 2 }, exit);
	void gate.open({ requestId: "r", intent, phase: "accepted", createdAt: 1, updatedAt: 2 }, exit);
	assert.deepEqual(exitCalls, [], "only accepted jobs with a real tabId may open their task (running has no backend identity yet)");
}

{
	const gate = createQuickStartOpenTaskGate();
	const job: QuickStartJob = { requestId: "r", intent, phase: "accepted", tabId: "tab-9", createdAt: 1, updatedAt: 2 };
	let calls = 0;
	const exit = async () => { calls += 1; if (calls === 1) throw new Error("exit failed"); };
	await assert.rejects(gate.open(job, exit), /exit failed/, "a failed exit rejects so the caller keeps the error visible");
	await gate.open(job, exit);
	assert.equal(calls, 2, "after a failed exit the same action is retryable (the gate releases on failure)");
}

// --- process-level shared runner: every mount subscribes to ONE runner and
// one in-flight registry; subscribe/unsubscribe is mount-safe ---

{
	resetSharedQuickStartJobRunner();
	try {
		const storage = fakeStorage();
		const runnerA = getSharedQuickStartJobRunner(async () => accepted("t"), storage);
		const runnerB = getSharedQuickStartJobRunner(async () => accepted("t"), storage);
		assert.equal(runnerA, runnerB, "every mount shares one process-level runner");
		const seen: QuickStartJobs[] = [];
		const off = runnerA.subscribe((jobs) => seen.push(jobs));
		assert.equal(seen.length, 1, "subscribing immediately pushes the current state");
		off();
		const first = runnerA.submit(intent);
		if (!first.ok) throw new Error("submit failed");
		assert.equal(seen.length, 1, "an unsubscribed mount stops receiving updates");
		const off2 = runnerB.subscribe((jobs) => seen.push(jobs));
		const second = runnerB.submit({ ...intent, prompt: "second" });
		if (!second.ok) throw new Error("submit failed");
		assert.equal(seen.length, 3, "the shared runner notifies every live subscriber (subscribe push + two submits)");
		assert.ok(runnerA.jobs()[second.requestId], "mount A sees mount B's job");
		off2();
		assert.equal(runnerB.dismiss(first.requestId), false, "a running job is never dismissible through the shared runner");
		assert.ok(runnerB.jobs()[first.requestId], "the running job's ledger entry survives");
		await flush(); // both flights settle as accepted
		assert.equal(runnerB.dismiss(first.requestId), true, "an accepted job is dismissible through the shared runner");
	} finally {
		resetSharedQuickStartJobRunner();
	}
}

console.log("widget quick-start jobs tests passed");
