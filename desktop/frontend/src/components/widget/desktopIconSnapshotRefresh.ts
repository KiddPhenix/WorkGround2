// Desktop icon data has one authority: GetDesktopIconSnapshot. Runtime events
// only wake this coordinator; they never carry or mutate a second frontend
// projection. Bursts collapse into one trailing refresh, a refresh already in
// flight stays single-flight, and a slow recovery pass repairs missed events.

import type { EventKind } from "../../lib/types";

export interface SnapshotTimerHost {
	setTimeout(fn: () => void, delay: number): number;
	clearTimeout(id: number): void;
}

export const SNAPSHOT_EVENT_DEBOUNCE_MS = 80;
export const SNAPSHOT_RECOVERY_MS = 30_000;

export interface DesktopIconSnapshotRefresh {
	start(): void;
	wake(): void;
	refreshNow(): void;
	dispose(): void;
}

export type SnapshotRefreshSubscribe = (wake: () => void) => () => void;

const SNAPSHOT_EVENT_KINDS = new Set<EventKind>([
	"turn_started",
	"turn_done",
	"message",
	"tool_dispatch",
	"tool_result",
	"approval_request",
	"ask_request",
	"retrying",
	"compaction_started",
	"compaction_done",
	"steer",
]);

// Token, reasoning, progress, usage and phase events can arrive many times per
// second but do not change icon membership or a durable interaction boundary.
// The terminal/boundary event wakes the snapshot; the recovery pass covers a
// missing or newly introduced event without relying on delivery order.
export function desktopIconEventWakesSnapshot(kind: EventKind): boolean {
	return SNAPSHOT_EVENT_KINDS.has(kind);
}

export function createDesktopIconSnapshotRefresh(
	refresh: () => Promise<void>,
	host: SnapshotTimerHost,
	debounceMs = SNAPSHOT_EVENT_DEBOUNCE_MS,
	recoveryMs = SNAPSHOT_RECOVERY_MS,
): DesktopIconSnapshotRefresh {
	let disposed = false;
	let running = false;
	let dirty = false;
	let wakeTimer: number | null = null;
	let recoveryTimer: number | null = null;

	const clearWake = () => {
		if (wakeTimer === null) return;
		host.clearTimeout(wakeTimer);
		wakeTimer = null;
	};
	const clearRecovery = () => {
		if (recoveryTimer === null) return;
		host.clearTimeout(recoveryTimer);
		recoveryTimer = null;
	};
	const scheduleRecovery = () => {
		clearRecovery();
		if (disposed) return;
		recoveryTimer = host.setTimeout(() => {
			recoveryTimer = null;
			dirty = true;
			void run();
		}, recoveryMs);
	};
	const scheduleWake = () => {
		clearWake();
		if (disposed || running) return;
		wakeTimer = host.setTimeout(() => {
			wakeTimer = null;
			void run();
		}, debounceMs);
	};
	const run = async () => {
		if (disposed || running) return;
		running = true;
		dirty = false;
		clearWake();
		clearRecovery();
		try {
			await refresh();
		} finally {
			running = false;
			if (disposed) return;
			if (dirty) scheduleWake();
			else scheduleRecovery();
		}
	};

	return {
		start() {
			if (disposed) return;
			dirty = true;
			void run();
		},
		wake() {
			if (disposed) return;
			dirty = true;
			scheduleWake();
		},
		refreshNow() {
			if (disposed) return;
			dirty = true;
			clearWake();
			void run();
		},
		dispose() {
			disposed = true;
			dirty = false;
			clearWake();
			clearRecovery();
		},
	};
}

export function subscribeDesktopIconSnapshotRefresh(
	refresh: DesktopIconSnapshotRefresh,
	sources: readonly SnapshotRefreshSubscribe[],
): () => void {
	const unsubscribe = sources.map((subscribe) => subscribe(refresh.wake));
	return () => unsubscribe.forEach((off) => off());
}
