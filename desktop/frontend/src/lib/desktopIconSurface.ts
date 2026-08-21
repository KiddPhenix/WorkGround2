import { useEffect, useRef } from "react";
import { app, type DesktopIconItem, type DesktopIconSurfaceInput, type DesktopIconSurfaceResult } from "./bridge";

export interface IconSurfaceBounds { width: number; height: number; }
export interface IconSurfaceApply { (input: DesktopIconSurfaceInput): Promise<DesktopIconSurfaceResult>; }

export interface IconSurfaceCoordinatorOptions {
	apply: IconSurfaceApply;
	envelope?: number;
	initialBounds?: IconSurfaceBounds;
	initialGeneration?: number;
	onError?: (err: unknown) => void;
	onApplied?: (result: DesktopIconSurfaceResult) => void;
}

export interface IconSurfaceCoordinator {
	// prepare is the render gate: native growth is confirmed before the caller
	// exposes new icons or transient UI. Superseded requests resolve false.
	prepare(bounds: IconSurfaceBounds): Promise<boolean>;
	// settle grows for stable icon layout changes. A surface never shrinks during
	// one icon-mode lifetime; exiting icon mode resets it to the initial bounds.
	settle(bounds: IconSurfaceBounds): void;
	current(): DesktopIconSurfaceResult | null;
	dispose(): void;
}

const clean = (value: number) => Number.isFinite(value) && value > 0 ? Math.round(value) : 0;
const normalize = (bounds: IconSurfaceBounds): IconSurfaceBounds => ({ width: clean(bounds.width), height: clean(bounds.height) });
const grows = (next: IconSurfaceBounds, current: IconSurfaceBounds) => next.width > current.width || next.height > current.height;
const covers = (outer: IconSurfaceBounds, inner: IconSurfaceBounds) => outer.width >= inner.width && outer.height >= inner.height;
const union = (a: IconSurfaceBounds, b: IconSurfaceBounds): IconSurfaceBounds => ({ width: Math.max(a.width, b.width), height: Math.max(a.height, b.height) });

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
	const topWidth = rowWidth("top");
	const bottomWidth = rowWidth("bottom");
	// Extra room covers zone margins, shadows, labels and rounding at non-100%
	// DPI/cluster zoom. Native code owns the final work-area clamp.
	const contentWidth = Math.max(minContent.width, topWidth, bottomWidth) + 96;
	const contentHeight = Math.max(minContent.height, (topWidth > 0 ? 78 : 0) + (bottomWidth > 0 ? 78 : 0) + 52) + 64;
	return { width: Math.ceil(contentWidth * zoom), height: Math.ceil(contentHeight * zoom) };
}

// A transient surface gets the whole bounded canvas. Popup contents never need
// to mount once merely to be measured.
export const DESKTOP_ICON_OVERLAY_BOUNDS: IconSurfaceBounds = { width: 1216, height: 836 };
const DESKTOP_ICON_INITIAL_BOUNDS: IconSurfaceBounds = { width: 1016, height: 656 };

export function createIconSurfaceCoordinator(options: IconSurfaceCoordinatorOptions): IconSurfaceCoordinator {
	const envelope = Math.max(0, options.envelope ?? 32);
	let appliedBounds = normalize(options.initialBounds ?? { width: 0, height: 0 });
	let requestedBounds = appliedBounds;
	let pendingBounds: IconSurfaceBounds = { width: 0, height: 0 };
	let pending: Promise<boolean> | null = null;
	let actual: DesktopIconSurfaceResult | null = null;
	let generation = Math.max(0, Math.trunc(options.initialGeneration ?? 0));
	let disposed = false;

	const apply = async (bounds: IconSurfaceBounds, token: number): Promise<boolean> => {
		try {
			const result = await options.apply({ ...bounds, envelope, generation: token });
			if (disposed || token !== generation || result.generation !== token) return false;
			actual = result;
			appliedBounds = bounds;
			options.onApplied?.(result);
			return true;
		} catch (cause) {
			if (!disposed && token === generation) options.onError?.(cause);
			return false;
		}
	};
	const request = (bounds: IconSurfaceBounds): Promise<boolean> => {
		const current = apply(bounds, ++generation);
		pendingBounds = bounds;
		pending = current;
		void current.finally(() => { if (pending === current) pending = null; });
		return current;
	};
	return {
		async prepare(bounds) {
			if (disposed) return false;
			const next = normalize(bounds);
			requestedBounds = union(requestedBounds, next);
			if (covers(appliedBounds, next)) return true;
			if (pending && covers(pendingBounds, requestedBounds)) return pending;
			return request(requestedBounds);
		},
		settle(bounds) {
			if (disposed) return;
			const next = normalize(bounds);
			requestedBounds = union(requestedBounds, next);
			if (!grows(requestedBounds, appliedBounds)) return;
			if (pending && covers(pendingBounds, requestedBounds)) return;
			void request(requestedBounds);
		},
		current: () => actual,
		dispose() { disposed = true; generation += 1; },
	};
}

export function useDesktopIconSurface(onError?: (err: unknown) => void, onApplied?: (result: DesktopIconSurfaceResult) => void): IconSurfaceCoordinator {
	const ref = useRef<IconSurfaceCoordinator | null>(null);
	if (ref.current === null) ref.current = createIconSurfaceCoordinator({
		apply: (input) => app.SetDesktopIconSurface(input),
		initialBounds: DESKTOP_ICON_INITIAL_BOUNDS,
		onError,
		onApplied,
		// A React remount inside the same native icon-mode lifetime must not
		// restart below the backend's last monotonic token.
		initialGeneration: Date.now() * 1000 + Math.floor(Math.random() * 1000),
	});
	useEffect(() => () => ref.current?.dispose(), []);
	return ref.current;
}
