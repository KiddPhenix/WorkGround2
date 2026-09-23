// Scroll memory for the run detail panel.
//
// Closing the panel unmounts it, and a re-render of the transcript can remount
// it; the reader must come back to the record and the scroll offset they left.
// The selection itself lives in the run record (single source of truth for
// business state); this module only remembers pixel offsets, which are pure
// presentation and deliberately kept out of the model.
//
// Keys are namespaced by the owning run so two sessions can never trade offsets.

const MAX_ENTRIES = 256;

const offsets = new Map<string, number>();

function remember(key: string, top: number): void {
  if (!Number.isFinite(top) || top < 0) return;
  if (!offsets.has(key) && offsets.size >= MAX_ENTRIES) {
    const oldest = offsets.keys().next();
    if (!oldest.done) offsets.delete(oldest.value);
  }
  offsets.set(key, Math.round(top));
}

export function detailListScrollKey(runId: string): string {
  return `${runId}\u0000list`;
}

export function detailBodyScrollKey(runId: string, eventId: string): string {
  return `${runId}\u0000body\u0000${eventId}`;
}

export function rememberDetailScroll(key: string, top: number): void {
  remember(key, top);
}

export function recallDetailScroll(key: string): number {
  return offsets.get(key) ?? 0;
}

/** Drop a run's offsets (used when its records go away). */
export function forgetDetailScroll(runId: string): void {
  const prefix = `${runId}\u0000`;
  for (const key of [...offsets.keys()]) {
    if (key.startsWith(prefix)) offsets.delete(key);
  }
}

export function resetDetailScrollMemory(): void {
  offsets.clear();
}
