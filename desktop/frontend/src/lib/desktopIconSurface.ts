import { useEffect, useRef } from "react";
import { app, type DesktopIconItem, type DesktopIconSurfaceInput, type DesktopIconSurfaceResult } from "./bridge";

export interface IconSurfaceBounds { width: number; height: number; }
export interface IconSurfaceApply { (input: DesktopIconSurfaceInput): Promise<DesktopIconSurfaceResult>; }

export interface IconSurfaceCoordinatorOptions {
	apply: IconSurfaceApply;
	envelope?: number;
	shrinkDelayMs?: number;
	initialGeneration?: number;
	schedule?: (fn: () => void, ms: number) => () => void;
	onError?: (err: unknown) => void;
}

export interface IconSurfaceCoordinator {
	// prepare is the render gate: native growth is confirmed before the caller
	// exposes new icons or transient UI. Superseded requests resolve false.
	prepare(bounds: IconSurfaceBounds): Promise<boolean>;
	// settle records the stable icon-only target. Growth is immediate; shrink
	// waits until the layout is stable and every transient surface is closed.
	settle(bounds: IconSurfaceBounds): void;
	setOverlay(open: boolean): void;
	setLayoutStable(stable: boolean): void;
	current(): DesktopIconSurfaceResult | null;
	dispose(): void;
}

const clean = (value: number) => Number.isFinite(value) && value > 0 ? Math.round(value) : 0;
const normalize = (bounds: IconSurfaceBounds): IconSurfaceBounds => ({ width: clean(bounds.width), height: clean(bounds.height) });
const equal = (a: IconSurfaceBounds, b: IconSurfaceBounds) => a.width === b.width && a.height === b.height;
const grows = (next: IconSurfaceBounds, current: IconSurfaceBounds) => next.width > current.width || next.height > current.height;

// Pure intent geometry. Runtime text, animation frames and DOM measurements do
// not participate, so visual updates cannot create a resize/reflow loop.
export function desktopIconLayoutBounds(items: DesktopIconItem[], collapsed: boolean, zoom = 1): IconSurfaceBounds {
	// Native clamps to 640x540 after the 32px envelope. Quantize every layout
	// that fits inside that floor to one target, so idle/running decoration
	// changes cannot issue no-op native resizes.
	const minContent = { width: 576, height: 476 };
	if (collapsed) return minContent;
	const rowWidth = (row: "top" | "bottom") => {
		const widths = items.filter((item) => item.position.row === row).map((item) => item.status === "running" || item.status === "thinking" ? 80 : 62);
		return widths.reduce((sum, width) => sum + width, 0) + Math.max(0, widths.length - 1) * 6;
	};
	const usableWidth = 1016; // 1080 native max minus the 32px envelope on both sides.
	const topWidth = rowWidth("top");
	const bottomWidth = rowWidth("bottom");
	const lines = (width: number) => width > 0 ? Math.ceil(width / usableWidth) : 0;
	const contentWidth = Math.min(usableWidth, Math.max(minContent.width, topWidth, bottomWidth));
	const contentHeight = Math.max(minContent.height, (lines(topWidth) + lines(bottomWidth)) * 78 + 52);
	return { width: Math.ceil(contentWidth * zoom), height: Math.ceil(contentHeight * zoom) };
}

// A transient surface gets the whole bounded canvas. Popup contents never need
// to mount once merely to be measured.
export const DESKTOP_ICON_OVERLAY_BOUNDS: IconSurfaceBounds = { width: 1016, height: 656 };

export function createIconSurfaceCoordinator(options: IconSurfaceCoordinatorOptions): IconSurfaceCoordinator {
	const envelope = Math.max(0, options.envelope ?? 32);
	const shrinkDelayMs = Math.max(0, options.shrinkDelayMs ?? 1000);
	const schedule = options.schedule ?? ((fn, ms) => {
		const handle = window.setTimeout(fn, ms);
		return () => window.clearTimeout(handle);
	});
	let appliedBounds: IconSurfaceBounds = { width: 0, height: 0 };
	let target: IconSurfaceBounds = { width: 0, height: 0 };
	let actual: DesktopIconSurfaceResult | null = null;
	let generation = Math.max(0, Math.trunc(options.initialGeneration ?? 0));
	let overlayOpen = false;
	let layoutStable = true;
	let disposed = false;
	let shrinkCancel: (() => void) | null = null;

	const cancelShrink = () => { shrinkCancel?.(); shrinkCancel = null; };
	const apply = async (bounds: IconSurfaceBounds, token: number): Promise<boolean> => {
		try {
			const result = await options.apply({ ...bounds, envelope, generation: token });
			if (disposed || token !== generation || result.generation !== token) return false;
			actual = result;
			appliedBounds = bounds;
			return true;
		} catch (cause) {
			if (!disposed && token === generation) options.onError?.(cause);
			return false;
		}
	};
	const scheduleShrink = () => {
		if (disposed || overlayOpen || !layoutStable || equal(target, appliedBounds)) return;
		cancelShrink();
		shrinkCancel = schedule(() => {
			shrinkCancel = null;
			if (disposed || overlayOpen || !layoutStable || equal(target, appliedBounds)) return;
			const token = ++generation;
			void apply(target, token);
		}, shrinkDelayMs);
	};

	return {
		async prepare(bounds) {
			if (disposed) return false;
			const next = normalize(bounds);
			target = next;
			cancelShrink();
			if (!grows(next, appliedBounds)) return true;
			// A render gate may need more width but less height (or vice versa).
			// Never shrink the other axis as part of that expansion.
			const safe = { width: Math.max(next.width, appliedBounds.width), height: Math.max(next.height, appliedBounds.height) };
			const ready = await apply(safe, ++generation);
			if (ready) scheduleShrink();
			return ready;
		},
		settle(bounds) {
			if (disposed) return;
			const next = normalize(bounds);
			if (equal(next, target)) { scheduleShrink(); return; }
			target = next;
			cancelShrink();
			if (grows(next, appliedBounds)) void apply(next, ++generation);
			else scheduleShrink();
		},
		setOverlay(open) {
			overlayOpen = open;
			if (open) cancelShrink(); else scheduleShrink();
		},
		setLayoutStable(stable) {
			layoutStable = stable;
			if (!stable) cancelShrink(); else scheduleShrink();
		},
		current: () => actual,
		dispose() { disposed = true; generation += 1; cancelShrink(); },
	};
}

export function useDesktopIconSurface(onError?: (err: unknown) => void): IconSurfaceCoordinator {
	const ref = useRef<IconSurfaceCoordinator | null>(null);
	if (ref.current === null) ref.current = createIconSurfaceCoordinator({
		apply: (input) => app.SetDesktopIconSurface(input),
		onError,
		// A React remount inside the same native icon-mode lifetime must not
		// restart below the backend's last monotonic token.
		initialGeneration: Date.now() * 1000 + Math.floor(Math.random() * 1000),
	});
	useEffect(() => () => ref.current?.dispose(), []);
	return ref.current;
}
