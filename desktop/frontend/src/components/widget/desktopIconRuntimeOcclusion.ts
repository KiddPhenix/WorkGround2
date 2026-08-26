// Pure geometry for the floating runtime layer (Running/Thinking state,
// summary copy and activity rail). The block renders as an absolute sibling
// above its icon and contributes no row height, so the only correct occlusion
// test is against REAL rendered rectangles: if the block collides with another
// rendered icon unit, hide it; otherwise keep it. Rects are supplied by the
// caller (measured from the DOM), so wrapping, zoom and resize are captured by
// whatever re-measured them.

export interface RuntimeRect {
	id: string;
	rect: { x: number; y: number; width: number; height: number };
}

function intersects(a: RuntimeRect["rect"], b: RuntimeRect["rect"], tolerance: number): boolean {
	return a.x < b.x + b.width - tolerance
		&& a.x + a.width > b.x + tolerance
		&& a.y < b.y + b.height - tolerance
		&& a.y + a.height > b.y + tolerance;
}

// Returns the ids of runtime blocks whose rendered rect collides with another
// icon unit's rect. Each block floats above its own icon, so any intersecting
// unit necessarily sits above it; the block's own icon is always excluded.
// Unmeasurable (zero-size) rects are skipped, and a small tolerance keeps
// pixel-rounding edges from counting as a collision.
export function occludedRuntimeIDs(runtimeRects: ReadonlyArray<RuntimeRect>, iconRects: ReadonlyArray<RuntimeRect>, tolerance = 1): ReadonlySet<string> {
	const hidden = new Set<string>();
	for (const runtime of runtimeRects) {
		if (runtime.rect.width <= 0 || runtime.rect.height <= 0) continue;
		for (const icon of iconRects) {
			if (icon.id === runtime.id) continue;
			if (icon.rect.width <= 0 || icon.rect.height <= 0) continue;
			if (intersects(runtime.rect, icon.rect, tolerance)) { hidden.add(runtime.id); break; }
		}
	}
	return hidden;
}

// Set comparison that keeps the hidden-ID state stable: identical sets reuse
// the previous state value so React bails out and no render churn occurs.
export function sameRuntimeOcclusion(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
	if (a === b) return true;
	if (a.size !== b.size) return false;
	for (const id of a) if (!b.has(id)) return false;
	return true;
}
