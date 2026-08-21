// widgetQuickStartJobs owns QuickStart's async delivery on the desktop icon
// surface. The modal only validates and enqueues; every send becomes a
// requestId-keyed job that is persisted in a localStorage ledger, projected
// into an optimistic task icon immediately, and delivered in the background
// by an in-flight registry. The backend requestId receipt stays the
// idempotency authority: reloads and lost responses replay the exact same
// requestId and the backend answers already_applied.
//
// Every mount of the icon widget shares ONE process-level runner
// (getSharedQuickStartJobRunner): a single in-memory ledger and a single
// in-flight registry, so remounts cannot race each other. On top of that,
// every transition is merge-on-write: the CURRENT persisted ledger is re-read
// and only the affected requestId is updated before the write, so an old
// async completion can never overwrite an entire newer ledger, delete a job
// submitted by a later mount, or resurrect a job another mount already
// reconciled. Subscription mount/unmount is safe (subscribe/unsubscribe).
// Explicit custom runners may still be created for unit tests.
//
// There is no front-end flight timeout: the backend serializes conversation
// starts, so queued calls can legitimately take longer than any fixed budget.
// The per-requestId in-flight guard is held until the actual Promise settles;
// a late accepted result advances the same job unless a newer explicit retry
// generation owns the requestId. Accepted jobs are never evicted on a timer:
// they persist until the authoritative task:<tabId> icon appears in a
// refreshed snapshot or the user explicitly dismisses them.
//
// Storage failures are explicit and cannot corrupt the ledger: the write
// paths (commit/submit) read through readQuickStartJobs, which distinguishes
// an unreadable ledger (getItem throws → the write is refused and the failure
// is surfaced) from a genuinely empty one, so a fabricated empty base can
// never overwrite durable data. When a BACKGROUND transition (accepted/failed
// phase change, reconcile handoff removal) cannot persist, the per-request
// intent is kept in a typed dirty overlay that later commits replay over the
// fresh durable state: an accepted/failed phase can never regress to running,
// and a reconcile removal keeps a remove tombstone so the handed-off job can
// never resurrect after storage recovery — even when later snapshots
// filter/cap the real icon. Dirty entries are cleared only after a save that
// included them succeeded. An EXPLICIT dismiss is the opposite: it is a
// user-visible action, so when the delete cannot persist the action FAILED —
// the job stays visible and NO tombstone is recorded, and an explicit retry
// durably upserts the captured job as running before dispatch (and does not
// dispatch when that restore cannot persist). Initial submit still requires
// the durable save before dispatch. A running optimistic job is the ONLY
// durable recovery intent until
// the backend receipt exists, so it can never be dismissed or deleted: only
// failed and accepted (with a real tabId) entries are dismissible. Legacy
// pending migration clears a matching legacy draft only after the ledger save
// succeeded, guarded by the same consumed-draft marker. Note: localStorage
// gives atomicity only within one synchronous read-modify-write tick; two
// separate windows can still lose one optimistic projection to a concurrent
// write (the in-memory view and the backend task survive, and the requestId
// receipt stays idempotent).
//
// The pure helpers (load/read/save/upsert/remove/project/merge/reconcile/
// consumed-draft marker/open-task gate) are unit-testable without React;
// createQuickStartJobRunner implements the state machine so the async flow can
// be tested with a fake deliver + storage; the hook only wires the shared
// runner into React state.

import { useEffect, useMemo, useState } from "react";
import type { DesktopIconItem, WidgetConversationInput, WidgetConversationResult } from "../../lib/bridge";
import { startWidgetConversationWithRetry } from "./startWidgetConversation";

export const QUICK_JOBS_KEY = "wg2.icon-widget-jobs";
export const QUICK_LEGACY_PENDING_KEY = "wg2.icon-widget-pending";
export const QUICK_DRAFT_KEY = "wg2.icon-widget-draft";
export const QUICK_CONSUMED_DRAFT_KEY = "wg2.icon-widget-draft-consumed";

// A consumed-draft marker durably records "this exact submitted draft was
// already enqueued" (keyed to the trimmed prompt + the job's requestId), so a
// draft whose best-effort removal failed is never reoffered by the next
// DesktopIconMode mount — even after the job itself is reconciled away. The
// marker is cleared once the removal succeeds, so an intentionally identical
// FUTURE draft is never permanently suppressed.
export interface QuickStartConsumedDraft {
	version: 1;
	prompt: string;
	requestId: string;
}

// readConsumedDraftMarker returns the marker or null (absent, corrupt, or a
// future version — all fail open); a storage failure propagates so callers
// can surface it visibly instead of suppressing or crashing silently.
export function readConsumedDraftMarker(storage: QuickStartJobStorage): QuickStartConsumedDraft | null {
	const raw = storage.getItem(QUICK_CONSUMED_DRAFT_KEY);
	if (!raw) return null;
	try {
		const parsed: unknown = JSON.parse(raw);
		if (parsed && typeof parsed === "object") {
			const record = parsed as Record<string, unknown>;
			if (record.version === 1) {
				const prompt = nonEmptyString(record.prompt).trim();
				const requestId = nonEmptyString(record.requestId).trim();
				if (prompt && requestId) return { version: 1, prompt, requestId };
			}
		}
	} catch {
		// Corrupt marker: no suppression, no crash.
	}
	return null;
}

export function recordConsumedDraftMarker(storage: QuickStartJobStorage, prompt: string, requestId: string): void {
	storage.setItem(QUICK_CONSUMED_DRAFT_KEY, JSON.stringify({ version: 1, prompt: prompt.trim(), requestId }));
}

export function clearConsumedDraftMarker(storage: QuickStartJobStorage): void {
	storage.removeItem(QUICK_CONSUMED_DRAFT_KEY);
}

// QuickStartConsumedDraftDecision is the PURE read half of the consumed-draft
// flow: what draft to offer on the next QuickStart open, and whether a cleanup
// is still pending. It never writes storage — the removal is performed by
// cleanupConsumedDraft from a committed effect or open event, so an aborted
// render or a StrictMode double render cannot mutate the draft or the marker.
export interface QuickStartConsumedDraftDecision {
	// draft is the initial draft to offer; "" suppresses the stale residue of
	// an already-submitted send.
	draft: string;
	// cleanupPending is true when storage still holds a residue (a stale
	// marker or a marker-matched draft) that an idempotent cleanup should
	// remove once the open is committed.
	cleanupPending: boolean;
	// cleanupMarker is supplied when suppression came from an active ledger
	// job rather than an already-persisted marker. A committed effect uses its
	// requestId to durably create the missing marker before deleting the draft.
	cleanupMarker?: QuickStartConsumedDraft;
}

// decideConsumedDraft decides whether a stored draft is the stale residue of
// an already-submitted send: an ACTIVE ledger job with the exact same prompt
// (the submit just enqueued it) or a consumed marker matching the prompt
// (the submit's best-effort draft removal failed) suppresses the draft; a
// marker that no longer matches the current draft is itself stale and only
// needs cleanup so an intentionally identical future draft is never
// permanently suppressed. An unreadable marker fails open (the draft is
// offered, nothing crashes). This function performs NO storage writes.
export function decideConsumedDraft(storage: QuickStartJobStorage, storedDraft: string, activeJobs: ReadonlyMap<string, string>): QuickStartConsumedDraftDecision {
	const trimmed = storedDraft.trim();
	if (!trimmed) return { draft: "", cleanupPending: false };
	// This exact prompt is still an active ledger job (just submitted): the
	// stored draft is its residue. Suppress it without touching storage, but
	// schedule committed cleanup with the real requestId: if both the submit
	// path's marker write and draft removal failed, the effect can recreate the
	// marker first and safely retry deletion.
	const activeRequestId = activeJobs.get(trimmed);
	if (activeRequestId) {
		return {
			draft: "",
			cleanupPending: true,
			cleanupMarker: { version: 1, prompt: trimmed, requestId: activeRequestId },
		};
	}
	let marker: QuickStartConsumedDraft | null = null;
	try {
		marker = readConsumedDraftMarker(storage);
	} catch {
		return { draft: storedDraft, cleanupPending: false }; // unreadable marker: fail open
	}
	if (!marker) return { draft: storedDraft, cleanupPending: false };
	if (marker.prompt !== trimmed) {
		// The marker no longer matches the current draft (the stale draft was
		// already removed or the user typed something new): the marker itself
		// is stale — offer the draft and leave the marker clear for an effect.
		return { draft: storedDraft, cleanupPending: true };
	}
	// This exact prompt was already submitted and its draft cleanup failed:
	// suppress it; the committed cleanup retries the removal.
	return { draft: "", cleanupPending: true };
}

// cleanupConsumedDraft finishes the removal the submit path could not: it
// removes the stale draft and clears the marker when they still match, or
// clears a marker that no longer matches the current draft. Everything is
// re-derived from CURRENT storage, so the cleanup is idempotent: a StrictMode
// double effect or a repeated open event is a no-op once it succeeded. Returns
// false only when storage is unavailable (the marker persists so the next open
// retries).
export function cleanupConsumedDraft(storage: QuickStartJobStorage, pendingMarker?: QuickStartConsumedDraft): boolean {
	let marker: QuickStartConsumedDraft | null = null;
	try {
		marker = readConsumedDraftMarker(storage);
	} catch {
		return false;
	}
	let draft = "";
	try {
		const raw = storage.getItem(QUICK_DRAFT_KEY);
		draft = typeof raw === "string" ? raw.trim() : "";
	} catch {
		return false;
	}
	// An active ledger match proves this draft was already enqueued even when
	// the submit path failed to write its marker. Write/rewrite the marker in
	// this committed cleanup before removing the draft. If the draft changed
	// since render, do not mark or remove the user's new text.
	if (pendingMarker && pendingMarker.prompt === draft
		&& (!marker || marker.prompt !== pendingMarker.prompt || marker.requestId !== pendingMarker.requestId)) {
		try {
			recordConsumedDraftMarker(storage, pendingMarker.prompt, pendingMarker.requestId);
			marker = pendingMarker;
		} catch {
			return false;
		}
	}
	if (!marker) return true; // nothing pending: already cleaned up
	if (marker.prompt !== draft) {
		// The marker no longer matches the stored draft: it is stale — clear
		// it so identical future text is never suppressed.
		try { clearConsumedDraftMarker(storage); return true; } catch { return false; }
	}
	try {
		storage.removeItem(QUICK_DRAFT_KEY);
		try { clearConsumedDraftMarker(storage); } catch { /* marker cleanup is best-effort */ }
		return true;
	} catch {
		// The stale draft still cannot be removed: keep it and the marker so
		// the next open retries the cleanup.
		return false;
	}
}

export type QuickStartJobPhase = "running" | "accepted" | "failed";

// QuickStartJobIntent is the frozen send inputs of one job. Retries replay
// exactly this, and later modal/workspace changes only affect new intents.
export interface QuickStartJobIntent {
	prompt: string;
	workspace: string;
	model: string;
	approvalMode: string;
}

export interface QuickStartJob {
	requestId: string;
	intent: QuickStartJobIntent;
	phase: QuickStartJobPhase;
	error?: string;
	// tabId is the real backend tab once accepted/already_applied. The job is
	// kept until a refreshed snapshot contains task:<tabId> (no gap/duplicate);
	// it is never evicted on a timer.
	tabId?: string;
	createdAt: number;
	updatedAt: number;
}

export type QuickStartJobs = Record<string, QuickStartJob>;

export interface QuickStartJobLedger {
	version: 1;
	jobs: QuickStartJobs;
}

export type QuickStartJobStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;
export type DeliverWidgetConversation = (input: WidgetConversationInput) => Promise<WidgetConversationResult>;
export type QuickStartStorageErrorContext = "dismiss" | "background" | "reconcile";

const QUICK_JOB_PHASES: readonly QuickStartJobPhase[] = ["running", "accepted", "failed"];

function isQuickStartJobPhase(value: unknown): value is QuickStartJobPhase {
	return typeof value === "string" && QUICK_JOB_PHASES.includes(value as QuickStartJobPhase);
}

function nonEmptyString(value: unknown): string {
	return typeof value === "string" ? value : "";
}

// parseQuickStartJob validates one ledger entry; a corrupt entry (bad key,
// missing prompt, wrong types) is dropped instead of poisoning the ledger.
export function parseQuickStartJob(value: unknown): QuickStartJob | null {
	if (!value || typeof value !== "object") return null;
	const raw = value as Record<string, unknown>;
	const requestId = nonEmptyString(raw.requestId).trim();
	const intent = raw.intent as Record<string, unknown> | null | undefined;
	const prompt = nonEmptyString(intent?.prompt);
	if (!requestId || !prompt.trim()) return null;
	const workspace = nonEmptyString(intent?.workspace) || "auto";
	const createdAt = typeof raw.createdAt === "number" ? raw.createdAt : 0;
	const error = nonEmptyString(raw.error);
	const tabId = nonEmptyString(raw.tabId);
	const job: QuickStartJob = {
		requestId,
		intent: {
			prompt,
			workspace,
			model: nonEmptyString(intent?.model),
			approvalMode: nonEmptyString(intent?.approvalMode),
		},
		phase: isQuickStartJobPhase(raw.phase) ? raw.phase : "running",
		createdAt,
		updatedAt: typeof raw.updatedAt === "number" ? raw.updatedAt : createdAt,
	};
	if (error) job.error = error;
	if (tabId) job.tabId = tabId;
	return job;
}

// readQuickStartJobs reads the keyed ledger, drops corrupt entries, and
// returns the job map. It throws ONLY when storage itself is unavailable
// (getItem throws): a corrupt JSON string or a non-object ledger still
// degrades to an empty job map, because that is a genuine "nothing durable to
// lose" state — but a storage failure must be distinguishable, so a write path
// never overwrites durable data it could not read from a fabricated empty
// base.
export function readQuickStartJobs(storage: QuickStartJobStorage): QuickStartJobs {
	const jobs: QuickStartJobs = {};
	const raw = storage.getItem(QUICK_JOBS_KEY); // storage failure propagates
	if (raw) {
		try {
			const parsed: unknown = JSON.parse(raw);
			if (parsed && typeof parsed === "object") {
				const ledger = (parsed as Record<string, unknown>).jobs;
				if (ledger && typeof ledger === "object") {
					for (const [key, value] of Object.entries(ledger as Record<string, unknown>)) {
						const job = parseQuickStartJob(value);
						if (job && job.requestId === key) jobs[key] = job;
					}
				}
			}
		} catch {
			// Corrupted ledger JSON: start clean; the caller can enqueue again.
		}
	}
	return jobs;
}

// loadQuickStartJobs is the best-effort initial-load view: it never throws
// (a storage failure starts this session with an empty view and is NOT treated
// as a durable base for writing), and it migrates the legacy single pending
// slot (wg2.icon-widget-pending) into a running job so its requestId stays
// idempotent with the backend receipt. A successful migration is persisted
// back into the ledger ONLY when the ledger itself was readable, so a second
// load (e.g. the hook's initializer and the runner's own load) sees the same
// jobs and an unreadable ledger is never overwritten from a fabricated empty
// base. A corrupted ledger or legacy record falls back safely.
export function loadQuickStartJobs(storage: QuickStartJobStorage): QuickStartJobs {
	let jobs: QuickStartJobs = {};
	let ledgerReadOk = false;
	try {
		jobs = readQuickStartJobs(storage);
		ledgerReadOk = true;
	} catch {
		// Storage unavailable: keep the empty session view; never write it back.
	}
	try {
		const legacy = storage.getItem(QUICK_LEGACY_PENDING_KEY);
		if (legacy) {
			let migrated = false;
			let migratedPrompt = "";
			let migratedRequestId = "";
			try {
				const pending = JSON.parse(legacy) as Record<string, unknown> | null;
				const requestId = nonEmptyString(pending?.id).trim();
				const prompt = nonEmptyString(pending?.prompt);
				if (requestId && prompt.trim() && !jobs[requestId]) {
					const now = Date.now();
					jobs[requestId] = {
						requestId,
						intent: {
							prompt,
							workspace: nonEmptyString(pending?.workspace) || "auto",
							model: nonEmptyString(pending?.model),
							approvalMode: nonEmptyString(pending?.approvalMode),
						},
						phase: "running",
						createdAt: now,
						updatedAt: now,
					};
					migrated = true;
					migratedPrompt = prompt.trim();
					migratedRequestId = requestId;
				}
			} catch {
				// Corrupted legacy record: drop it, no migration possible.
			}
			// Persist the migrated ledger BEFORE dropping the legacy slot: if
			// the write fails (quota), the legacy record stays so the next load
			// can retry the migration instead of losing the intent. The persist
			// is skipped when the ledger could not be read: writing over
			// unreadable durable data from an empty base would be a data loss.
			let removed = !migrated;
			if (migrated && ledgerReadOk) {
				try {
					saveQuickStartJobs(storage, jobs);
					removed = true;
					// The migrated prompt was already handed to the backend: a
					// matching legacy draft is cleared ONLY after the ledger save
					// succeeded, so reopening QuickStart cannot resubmit it (and a
					// failed save keeps the draft for the migration retry). The
					// consumed marker records the submission BEFORE the
					// best-effort removal, so a removal failure keeps the next
					// mount from reoffering the stale draft; it is cleared once
					// the removal succeeds.
					try {
						const draft = storage.getItem(QUICK_DRAFT_KEY);
						if (typeof draft === "string" && draft.trim() === migratedPrompt) {
							let marked = false;
							try { recordConsumedDraftMarker(storage, migratedPrompt, migratedRequestId); marked = true; } catch { /* marker best-effort */ }
							try {
								storage.removeItem(QUICK_DRAFT_KEY);
								if (marked) { try { clearConsumedDraftMarker(storage); } catch { /* marker cleanup best-effort */ } }
							} catch {
								// The draft stays; the marker keeps the next
								// mount from reoffering it and retries cleanup.
							}
						}
					} catch {
						// Draft cleanup is best-effort; the ledger is already safe.
					}
				} catch {
					// Keep the legacy record for a future migration retry.
				}
			}
			if (removed) {
				try {
					storage.removeItem(QUICK_LEGACY_PENDING_KEY);
				} catch {
					// The legacy slot may stay; the next load still migrates
					// idempotently (the ledger entry already exists).
				}
			}
		}
	} catch {
		// Storage itself is unavailable: keep whatever was parsed so far.
	}
	return jobs;
}

// saveQuickStartJobs persists the whole ledger; it throws on storage failure
// so the submit path can keep the modal open and expose the error.
export function saveQuickStartJobs(storage: QuickStartJobStorage, jobs: QuickStartJobs): void {
	const ledger: QuickStartJobLedger = { version: 1, jobs };
	storage.setItem(QUICK_JOBS_KEY, JSON.stringify(ledger));
}

export function upsertQuickStartJob(jobs: QuickStartJobs, job: QuickStartJob): QuickStartJobs {
	return { ...jobs, [job.requestId]: job };
}

export function removeQuickStartJob(jobs: QuickStartJobs, requestId: string): QuickStartJobs {
	if (!(requestId in jobs)) return jobs;
	const next = { ...jobs };
	delete next[requestId];
	return next;
}

export const QUICK_JOB_OPTIMISTIC_PREFIX = "opt:";

export function quickStartJobItemID(requestId: string): string {
	return `${QUICK_JOB_OPTIMISTIC_PREFIX}${requestId}`;
}

export function quickStartJobRequestIDFromItem(itemID: string): string | null {
	return itemID.startsWith(QUICK_JOB_OPTIMISTIC_PREFIX)
		? itemID.slice(QUICK_JOB_OPTIMISTIC_PREFIX.length)
		: null;
}

export function isQuickStartJobItem(item: DesktopIconItem): boolean {
	return item.kind === "task" && item.id.startsWith(QUICK_JOB_OPTIMISTIC_PREFIX);
}

export function quickStartJobRequestId(prefix = "icon-new"): string {
	return `${prefix}:${typeof crypto !== "undefined" && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`}`;
}

export function quickStartJobPromptLabel(intent: QuickStartJobIntent): string {
	const firstLine = intent.prompt.trim().split(/\r?\n/, 1)[0] ?? "";
	const chars = Array.from(firstLine);
	return chars.length > 24 ? `${chars.slice(0, 24).join("")}…` : firstLine;
}

export function quickStartJobWorkspaceLabel(workspace: string): string {
	if (!workspace || workspace === "auto") return "自动";
	if (workspace === "global") return "Global";
	const root = workspace.startsWith("project:") ? workspace.slice("project:".length) : workspace;
	const parts = root.split(/[/\\]/).filter(Boolean);
	return parts[parts.length - 1] || root;
}

// quickStartJobStateLabel is the screen-reader/mouse state a QuickStart job
// icon must announce: the accepted phase still means the backend turn is
// running, so it reads as 后台发送中 alongside the still-in-flight phase.
export function quickStartJobStateLabel(item: DesktopIconItem): string {
	return item.status === "failed" ? "发送失败，可重试" : "后台发送中";
}

// quickStartJobItem projects one job onto the shared icon model. The stable
// key is opt:<requestId>; failed reuses the real failed visual, accepted maps
// to running (the backend turn is genuinely running), and a still-in-flight
// job renders as an idle task icon with a subtle queued dot.
export function quickStartJobItem(job: QuickStartJob): DesktopIconItem {
	return {
		id: quickStartJobItemID(job.requestId),
		kind: "task",
		sourceId: job.requestId,
		title: quickStartJobPromptLabel(job.intent),
		status: job.phase === "failed" ? "failed" : job.phase === "accepted" ? "running" : "idle",
		unreadCount: 0,
		notifications: [],
		position: { row: "bottom", zone: "running", order: 0 },
		revision: `opt:${job.updatedAt}`,
	};
}

// mergeQuickStartItems inserts newest-first optimistic jobs at the left edge of
// the bottom row. The authoritative handoff pins the real task to that same
// edge, so a new Session never jumps from beside 新建 to beside the workspaces.
export function mergeQuickStartItems(items: DesktopIconItem[], optimistic: DesktopIconItem[]): DesktopIconItem[] {
	if (optimistic.length === 0) return items;
	const bottomAt = items.findIndex((item) => item.position.row === "bottom");
	if (bottomAt < 0) return [...items, ...optimistic];
	return [...items.slice(0, bottomAt), ...optimistic, ...items.slice(bottomAt)];
}

// reconcileQuickStartJobs removes an accepted job the moment its real task
// icon (task:<tabId>) appears in a refreshed snapshot, so the handoff never
// shows an empty frame or a duplicate. Polls never touch running or failed
// jobs, so an old poll result can never delete a newer job, and there is no
// time-based eviction: an accepted job whose real icon is filtered/capped out
// of the snapshot stays usable until the icon appears or the user dismisses.
export function reconcileQuickStartJobs(jobs: QuickStartJobs, items: DesktopIconItem[]): QuickStartJobs {
	let changed = false;
	let next = jobs;
	for (const job of Object.values(jobs)) {
		if (job.phase !== "accepted" || !job.tabId) continue;
		if (items.some((item) => item.id === `task:${job.tabId}`)) {
			next = removeQuickStartJob(next, job.requestId);
			changed = true;
		}
	}
	return changed ? next : jobs;
}

export type QuickStartSubmitOutcome =
	| { ok: true; requestId: string }
	| { ok: false; error: string };

export interface QuickStartJobRunner {
	jobs(): QuickStartJobs;
	// subscribe registers a jobs listener and immediately pushes the current
	// state; the returned unsubscribe makes mount/unmount safe for the shared
	// process-level runner.
	subscribe(onJobs: (jobs: QuickStartJobs) => void): () => void;
	// subscribeErrors surfaces durable-storage failures so the UI can expose a
	// visible recoverable warning instead of swallowing them.
	subscribeErrors(onError: (message: string, context: QuickStartStorageErrorContext) => void): () => void;
	// submit validates and persists synchronously, upserts a running job and
	// dispatches it in the background. A validation or ledger persistence
	// failure returns ok:false and neither closes the modal nor dispatches.
	// replacesRequestId (the failed job being edited) is removed on success.
	submit(intent: QuickStartJobIntent, opts?: { replacesRequestId?: string }): QuickStartSubmitOutcome;
	// retry re-dispatches the exact same requestId and frozen intent; the
	// backend receipt answers already_applied when the earlier attempt landed.
	retry(requestId: string): void;
	// dismiss removes ONLY a failed or accepted (with a real backend tabId)
	// entry, and only after the durable ledger save succeeded: on save failure
	// the action FAILED — it returns false, keeps the icon visible, surfaces a
	// recoverable error, and records NO tombstone, so a later explicit retry
	// can durably restore the entry as running. A running optimistic job is
	// the ONLY durable recovery intent until the backend receipt exists, so it
	// can never be dismissed or deleted — dropping it would lose the requestId
	// replay and silently orphan the delivery.
	dismiss(requestId: string): boolean;
	// reconcile drops accepted jobs whose real task icon just appeared. It
	// never evicts on a timer. A removal that cannot persist is kept as a
	// dirty tombstone (unlike a failed explicit dismiss, which records
	// nothing) so the handed-off job can never resurrect after storage
	// recovery.
	reconcile(items: DesktopIconItem[]): void;
	// resume re-dispatches nonterminal (running) jobs after a reload. The
	// dispatch guard makes it safe against double in-flight flights.
	resume(): void;
}

export interface QuickStartJobRunnerOptions {
	deliver: DeliverWidgetConversation;
	storage: QuickStartJobStorage;
	onJobs?: (jobs: QuickStartJobs) => void;
	wait?: (delayMs: number) => Promise<void>;
	now?: () => number;
}

// generations is the process-level per-requestId retry fence: every dispatch
// (from any runner, including an explicit retry or a reload resume) bumps it,
// and a flight only applies its result while it is still the newest
// generation. A late accepted result advances the same job unless a newer
// explicit retry generation owns the requestId — including across runner
// instances created in the same process (the hook mounts one shared runner;
// explicit custom runners remain for unit tests).
const generations = new Map<string, number>();

// QuickStartDirtyIntent is a pending per-request mutation that a BACKGROUND
// transition could not persist: a "phase" intent (the exact intended job) or a
// "remove" tombstone (a reconcile handoff removal — never a failed explicit
// dismiss). Later commits replay every pending intent over the freshly-read
// durable ledger before applying their own operation, so a phase can never
// regress to running and a handed-off removal is never silently lost.
type QuickStartDirtyIntent =
	| { kind: "phase"; requestId: string; job: QuickStartJob }
	| { kind: "remove"; requestId: string };

// QuickStartTransition is one commit operation. "update" mutates at most the
// named requestId (to(current) returning undefined leaves it untouched);
// "remove" deletes the named requestId when present; "reconcile" is the
// idempotent snapshot handoff (replayable by every poll, so it needs no dirty
// intent).
type QuickStartTransition =
	| { kind: "update"; requestId: string; to: (current: QuickStartJob | undefined) => QuickStartJob | undefined }
	| { kind: "remove"; requestId: string }
	| { kind: "reconcile"; items: DesktopIconItem[] };

export function createQuickStartJobRunner(options: QuickStartJobRunnerOptions): QuickStartJobRunner {
	const { deliver, storage, onJobs, wait, now = Date.now } = options;
	let jobs = loadQuickStartJobs(storage);
	const running = new Map<string, Promise<void>>();
	const dirty = new Map<string, QuickStartDirtyIntent>();
	const subscribers = new Set<(jobs: QuickStartJobs) => void>();
	const errorSubscribers = new Set<(message: string, context: QuickStartStorageErrorContext) => void>();
	if (onJobs) subscribers.add(onJobs);

	const notify = (next: QuickStartJobs) => {
		jobs = next;
		for (const cb of subscribers) cb(next);
	};
	const reportStorageError = (message: string, context: QuickStartStorageErrorContext) => {
		for (const cb of errorSubscribers) cb(message, context);
	};
	// applyDirty replays every pending per-request intent over a base ledger.
	// A "phase" intent applies only when the requestId still exists in the
	// base, so a job another runner/window already dismissed or reconciled
	// durably is never resurrected by a stale intent.
	const applyDirty = (base: QuickStartJobs): QuickStartJobs => {
		if (dirty.size === 0) return base;
		let next = base;
		for (const intent of dirty.values()) {
			if (intent.kind === "phase" && intent.requestId in next) next = upsertQuickStartJob(next, intent.job);
			else if (intent.kind === "remove") next = removeQuickStartJob(next, intent.requestId);
		}
		return next;
	};
	const applyTransition = (base: QuickStartJobs, transition: QuickStartTransition): QuickStartJobs => {
		if (transition.kind === "update") {
			const nextJob = transition.to(base[transition.requestId]);
			return nextJob ? upsertQuickStartJob(base, nextJob) : base;
		}
		if (transition.kind === "remove") {
			return transition.requestId in base ? removeQuickStartJob(base, transition.requestId) : base;
		}
		return reconcileQuickStartJobs(base, transition.items);
	};
	// commit applies a transition against the CURRENT persisted ledger (read
	// fresh on every write), persists the merged result, and refreshes the
	// in-memory view. Only the affected requestId changes, so an old async
	// completion can never overwrite an entire newer ledger or delete a job it
	// never knew about. When the ledger cannot be read or written, the
	// transition's per-request intent is kept in the dirty overlay so a LATER
	// successful commit merges fresh durable state + pending intents + its own
	// operation: an accepted/failed phase can never regress to running, and a
	// dismissal is never silently lost. Dirty entries are cleared only after a
	// save that included them succeeded. When storage is unavailable,
	// fallbackToMemory keeps the session view working and the failure is
	// surfaced through subscribeErrors; dismiss passes fallbackToMemory=false
	// so a failed durable save never hides the entry.
	const commit = (transition: QuickStartTransition, fallbackToMemory = true): { next: QuickStartJobs; saved: boolean; error?: string } => {
		let next: QuickStartJobs;
		let saved = false;
		let error: string | undefined;
		try {
			const durable = readQuickStartJobs(storage);
			const base = applyDirty(durable);
			next = applyTransition(base, transition);
			if (next !== durable) {
				saveQuickStartJobs(storage, next);
				saved = true;
			}
			// The write (or the unchanged state) included every pending dirty
			// intent; an intent that could not apply (the job vanished from the
			// durable base, e.g. another window dismissed it) is resolved too.
			dirty.clear();
		} catch (cause) {
			error = cause instanceof Error ? cause.message : String(cause);
			const memoryBase = applyDirty(jobs);
			next = applyTransition(memoryBase, transition);
			if (fallbackToMemory) {
				// Background transition (phase update, reconcile removal): keep
				// every per-request intent that could not persist so LATER
				// commits replay it over the fresh durable state. Phase intents
				// keep the exact intended job; removals (a reconcile handoff
				// that could not save) keep a tombstone so the removal survives
				// storage recovery and can never be resurrected by a later
				// snapshot that filters/caps the real icon.
				for (const key of Object.keys(memoryBase)) {
					if (!(key in next)) dirty.set(key, { kind: "remove", requestId: key });
				}
				if (transition.kind === "update") {
					const pendingJob = next[transition.requestId];
					if (pendingJob) dirty.set(transition.requestId, { kind: "phase", requestId: transition.requestId, job: pendingJob });
				}
			}
			// Explicit dismiss (fallbackToMemory=false): the user-visible
			// delete FAILED — keep the job visible and record NO tombstone, so
			// a later explicit retry can durably restore the entry as running
			// instead of being erased by a stale removal intent.
			if (!fallbackToMemory) next = jobs;
		}
		notify(next);
		return { next, saved, error };
	};

	const dispatch = (requestId: string) => {
		if (running.has(requestId)) return;
		const job = jobs[requestId];
		if (!job || (job.phase !== "running" && job.phase !== "failed")) return;
		if (job.phase === "failed") {
			// Explicit retry: durably upsert the CAPTURED job as running
			// before the new flight starts, and bump the generation below so
			// an older flight's late result cannot overwrite this retry's
			// outcome. The captured memory job is the session's latest known
			// state, so the entry is restored even when the durable ledger no
			// longer holds it (a stale removal or another window). A
			// persistence failure ABORTS the retry: nothing dispatches and the
			// job stays failed, because a flight without a durable running
			// entry would orphan the retry.
			// commit's generic background fallback records the requested running
			// phase as dirty. That is correct for a real background transition,
			// but a retry whose durable running save failed never dispatched: it
			// must restore the exact pre-retry dirty intent (often a failed phase),
			// or remove the newly-created running intent when none existed. This
			// keeps later reconcile/storage recovery from manufacturing a durable
			// running job with no flight.
			const previousDirty = dirty.get(requestId);
			const retried = commit({
				kind: "update",
				requestId,
				to: () => ({ ...job, phase: "running", error: undefined, updatedAt: now() }),
			});
			if (retried.error) {
				if (previousDirty) dirty.set(requestId, previousDirty);
				else dirty.delete(requestId);
				reportStorageError(retried.error, "background");
				// The retry did not happen: keep the durable-failed job visible
				// in the session view (the generic fallback would have advanced
				// the view to running without a flight, which would make the
				// NEXT retry skip the durable restore and dispatch without a
				// durable running entry). Any pre-existing failed intent still
				// self-heals the durable ledger on the next successful write.
				notify({ ...jobs, [requestId]: job });
				return;
			}
		}
		const generation = (generations.get(requestId) ?? 0) + 1;
		generations.set(requestId, generation);
		const input: WidgetConversationInput = {
			prompt: job.intent.prompt,
			requestId,
			workspace: job.intent.workspace || undefined,
			model: job.intent.model || undefined,
			approvalMode: job.intent.approvalMode || undefined,
			existingTitles: Object.values(jobs)
				.filter((other) => other.requestId !== requestId)
				.map((other) => quickStartJobPromptLabel(other.intent)),
		};
		const transition = (requestId: string, to: (current: QuickStartJob | undefined) => QuickStartJob | undefined) => {
			const result = commit({ kind: "update", requestId, to });
			if (result.error) reportStorageError(result.error, "background");
		};
		let release!: () => void;
		const flight = new Promise<void>((resolve) => { release = resolve; });
		running.set(requestId, flight);
		void startWidgetConversationWithRetry(deliver, input, wait)
			.then((result) => {
				// A late accepted result advances the SAME job (no front-end
				// timeout fence) unless a newer explicit retry generation owns
				// this requestId.
				if (generations.get(requestId) !== generation) return;
				transition(requestId, (current) => {
					if (!current) return undefined; // reconciled/dismissed meanwhile
					if (result.status === "accepted" || result.status === "already_applied") {
						return {
							...current,
							phase: "accepted",
							tabId: result.tabId,
							error: undefined,
							updatedAt: now(),
						};
					}
					return {
						...current,
						phase: "failed",
						error: result.error || "发起失败，可安全重试",
						updatedAt: now(),
					};
				});
			})
			.catch((cause) => {
				if (generations.get(requestId) !== generation) return;
				transition(requestId, (current) => {
					if (!current) return undefined;
					return {
						...current,
						phase: "failed",
						error: cause instanceof Error ? cause.message : String(cause),
						updatedAt: now(),
					};
				});
			})
			.finally(() => {
				// The in-flight guard is held until the actual Promise settles:
				// there is no front-end flight timeout, and queued calls are
				// never fenced. Only this flight releases its own guard entry.
				if (running.get(requestId) === flight) running.delete(requestId);
				release();
			});
	};

	const runner: QuickStartJobRunner = {
		jobs: () => jobs,
		subscribe(onJobs) {
			subscribers.add(onJobs);
			onJobs(jobs);
			return () => { subscribers.delete(onJobs); };
		},
		subscribeErrors(onError) {
			errorSubscribers.add(onError);
			return () => { errorSubscribers.delete(onError); };
		},
		submit(intent, opts) {
			const prompt = intent.prompt.trim();
			if (!prompt) return { ok: false, error: "请输入对话内容" };
			const requestId = quickStartJobRequestId("icon-new");
			const job: QuickStartJob = {
				requestId,
				intent: { ...intent, prompt },
				phase: "running",
				createdAt: now(),
				updatedAt: now(),
			};
			let next: QuickStartJobs;
			try {
				// Merge into the CURRENT durable ledger so a job submitted by
				// another mount/window is never wiped by this submit. The
				// throwing read refuses to fabricate an empty base when the
				// ledger cannot be read, and pending dirty intents are replayed
				// so a submit can never regress a phase that failed to persist.
				const base = applyDirty(readQuickStartJobs(storage));
				next = upsertQuickStartJob(base, job);
				if (opts?.replacesRequestId && base[opts.replacesRequestId]?.phase === "failed") {
					next = removeQuickStartJob(next, opts.replacesRequestId);
				}
				saveQuickStartJobs(storage, next);
				dirty.clear();
			} catch (cause) {
				// Initial ledger persistence failure: keep the modal open, do
				// not dispatch, and enqueue nothing in memory either.
				return { ok: false, error: cause instanceof Error ? cause.message : String(cause) };
			}
			notify(next);
			dispatch(requestId);
			return { ok: true, requestId };
		},
		retry: dispatch,
		dismiss(requestId) {
			if (!jobs[requestId]) return false;
			// Only failed and accepted (with a real backend tabId) entries may
			// be dismissed. A running optimistic job is the ONLY durable
			// recovery intent until the backend receipt exists: deleting it
			// would lose the requestId replay and silently orphan the delivery.
			const current = jobs[requestId];
			if (current.phase === "running" || (current.phase === "accepted" && !current.tabId)) return false;
			const result = commit({ kind: "remove", requestId }, false);
			if (result.error || jobs[requestId]) {
				reportStorageError(`删除失败：${result.error ?? "存储不可用"}`, "dismiss");
				return false;
			}
			return true;
		},
		reconcile(items) {
			const result = commit({ kind: "reconcile", items });
			if (result.error) reportStorageError(result.error, "reconcile");
		},
		resume() {
			for (const job of Object.values(jobs)) {
				if (job.phase === "running") dispatch(job.requestId);
			}
		},
	};
	return runner;
}

// createLatestAppliedGuard gives every poll a monotonically increasing
// generation but applies LATEST-SUCCESSFULLY-APPLIED semantics: starting a
// new request NEVER invalidates an older response (so slow polls cannot starve
// each other out when calls consistently exceed the poll interval); only a
// newer response that actually applied makes an older one stale. An errored
// response applies nothing, so it never starves the next success. The
// out-of-order no-real-after-real protection is preserved: an older response
// resolving after a newer one applied is dropped, so stale items can never
// reconcile (and resurrect) jobs or regress the surface to an empty frame.
export function createLatestAppliedGuard() {
	let started = 0;
	let applied = 0;
	return {
		begin(): number { return ++started; },
		// mayApply: no NEWER response has applied yet (a newly started request
		// is irrelevant until it applies).
		mayApply(generation: number): boolean { return generation > applied; },
		markApplied(generation: number): void { if (generation > applied) applied = generation; },
	};
}

// QuickStartOpenTaskGate serializes the accepted-job "open task" exit. The
// exact backend tabId is passed to the exit callback exactly once per
// invocation: a double click while the exit is in flight is a no-op. Only
// accepted jobs with a real tabId pass (running jobs have no backend identity
// yet, and an accepted job without a tabId cannot be opened). A failed exit
// rejects so the caller can keep the error visible, and the gate releases on
// failure so the same action stays retryable.
export interface QuickStartOpenTaskGate {
	open(job: QuickStartJob, exit: (tabId: string) => Promise<void>): Promise<void>;
}

export function createQuickStartOpenTaskGate(): QuickStartOpenTaskGate {
	let inFlight = false;
	return {
		open(job, exit) {
			if (inFlight || job.phase !== "accepted" || !job.tabId) return Promise.resolve();
			inFlight = true;
			return exit(job.tabId).finally(() => { inFlight = false; });
		},
	};
}

export interface WidgetQuickStartJobsApi {
	jobs: QuickStartJobs;
	// storageError is the last surfaced durable-storage failure (empty when
	// none); clearStorageError dismisses it.
	storageError: string;
	clearStorageError: () => void;
	submit: QuickStartJobRunner["submit"];
	retry: QuickStartJobRunner["retry"];
	dismiss: QuickStartJobRunner["dismiss"];
	reconcile: QuickStartJobRunner["reconcile"];
}

// One process-level runner owns every mount of the icon widget: remounts
// (StrictMode double effects, mode switches) share the same in-memory ledger
// and the same in-flight registry, so an old mount's async completion can
// never overwrite a newer ledger, delete a job submitted by a later mount, or
// resurrect a job that was already reconciled. The merge-on-write transitions
// additionally protect separate windows sharing the same localStorage.
let sharedQuickStartRunner: QuickStartJobRunner | null = null;

export function getSharedQuickStartJobRunner(deliver: DeliverWidgetConversation, storage: QuickStartJobStorage): QuickStartJobRunner {
	if (!sharedQuickStartRunner) sharedQuickStartRunner = createQuickStartJobRunner({ deliver, storage });
	return sharedQuickStartRunner;
}

export function resetSharedQuickStartJobRunner(): void {
	sharedQuickStartRunner = null;
	generations.clear();
}

// useWidgetQuickStartJobs subscribes the component to the shared process-level
// runner. Subscribe/unsubscribe in an effect makes mount/unmount safe, and
// resume() replays nonterminal jobs whose dispatch was interrupted by a
// reload; the backend receipt answers already_applied and the dispatch guard
// prevents double flights.
export function useWidgetQuickStartJobs(
	deliver: DeliverWidgetConversation,
	storage: QuickStartJobStorage = localStorage,
): WidgetQuickStartJobsApi {
	const runner = useMemo(() => getSharedQuickStartJobRunner(deliver, storage), [deliver, storage]);
	const [jobs, setJobs] = useState<QuickStartJobs>(() => loadQuickStartJobs(storage));
	const [storageError, setStorageError] = useState("");
	useEffect(() => runner.subscribe(setJobs), [runner]);
	useEffect(() => runner.subscribeErrors((message) => setStorageError(message)), [runner]);
	useEffect(() => { runner.resume(); }, [runner]);
	return useMemo(
		() => ({
			jobs,
			storageError,
			clearStorageError: () => setStorageError(""),
			submit: runner.submit,
			retry: runner.retry,
			dismiss: runner.dismiss,
			reconcile: runner.reconcile,
		}),
		[jobs, runner, storageError],
	);
}
