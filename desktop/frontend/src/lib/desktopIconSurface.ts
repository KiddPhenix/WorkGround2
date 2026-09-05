import { useLayoutEffect, useMemo, useRef } from "react";
import { app, type DesktopIconItem, type DesktopIconSurfaceInput, type DesktopIconSurfaceResult } from "./bridge";

export interface IconSurfaceBounds { width: number; height: number; }
export interface IconSurfaceApply { (input: DesktopIconSurfaceInput): Promise<DesktopIconSurfaceResult>; }

export interface IconSurfaceCoordinatorOptions {
	apply: IconSurfaceApply;
	envelope?: number;
	initialBounds?: IconSurfaceBounds;
	revision?: number;
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

export interface IconSurfaceLifecycle extends IconSurfaceCoordinator {
	activate(): void;
	deactivate(): void;
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

export function createIconSurfaceCoordinator(options: IconSurfaceCoordinatorOptions): IconSurfaceCoordinator {
	const envelope = Math.max(0, options.envelope ?? 32);
	let appliedBounds = normalize(options.initialBounds ?? { width: 0, height: 0 });
	let requestedBounds = appliedBounds;
	let pending: Promise<void> | null = null;
	let waiters: Array<{ bounds: IconSurfaceBounds; resolve: (ready: boolean) => void }> = [];
	let actual: DesktopIconSurfaceResult | null = null;
	let disposed = false;

	const resolveReady = () => {
		const remaining: typeof waiters = [];
		for (const waiter of waiters) {
			if (covers(appliedBounds, waiter.bounds)) waiter.resolve(true);
			else remaining.push(waiter);
		}
		waiters = remaining;
	};
	const failWaiters = () => {
		const failed = waiters;
		waiters = [];
		failed.forEach((waiter) => waiter.resolve(false));
	};
	const drain = () => {
		if (disposed || pending || covers(appliedBounds, requestedBounds)) {
			resolveReady();
			return;
		}
		const target = requestedBounds;
		let applied = false;
		const current = (async () => {
			try {
				const result = await options.apply({ ...target, envelope, revision: options.revision ?? 0 });
				if (disposed) return;
				actual = result;
				appliedBounds = union(appliedBounds, target);
				applied = true;
				options.onApplied?.(result);
				resolveReady();
			} catch (cause) {
				if (!disposed) options.onError?.(cause);
			}
		})();
		pending = current;
		void current.finally(() => {
			if (pending !== current) return;
			pending = null;
			if (disposed) return;
			if (!applied) {
				failWaiters();
				return;
			}
			drain();
		});
	};
	return {
		async prepare(bounds) {
			if (disposed) return false;
			const next = normalize(bounds);
			requestedBounds = union(requestedBounds, next);
			if (covers(appliedBounds, next)) return true;
			const ready = new Promise<boolean>((resolve) => waiters.push({ bounds: next, resolve }));
			drain();
			return ready;
		},
		settle(bounds) {
			if (disposed) return;
			const next = normalize(bounds);
			requestedBounds = union(requestedBounds, next);
			if (!grows(requestedBounds, appliedBounds)) return;
			drain();
		},
		current: () => actual,
		dispose() { disposed = true; failWaiters(); },
	};
}

// React Activity disconnects effects while preserving refs and state. A raw
// coordinator becomes permanently disposed after the first hidden transition,
// so this stable facade owns a fresh coordinator for each visible lifetime.
// Calls outside an active lifetime fail/no-op explicitly; component layout
// effects run after activate and converge through the new native authority.
export function createIconSurfaceLifecycle(options: IconSurfaceCoordinatorOptions): IconSurfaceLifecycle {
	let active: IconSurfaceCoordinator | null = null;
	return {
		activate() {
			if (!active) active = createIconSurfaceCoordinator(options);
		},
		deactivate() {
			active?.dispose();
			active = null;
		},
		prepare: (bounds) => active?.prepare(bounds) ?? Promise.resolve(false),
		settle: (bounds) => { active?.settle(bounds); },
		current: () => active?.current() ?? null,
		dispose() {
			active?.dispose();
			active = null;
		},
	};
}

export function useDesktopIconSurface(onError?: (err: unknown) => void, onApplied?: (result: DesktopIconSurfaceResult) => void, revision = 0): IconSurfaceCoordinator {
	const callbacks = useRef({ onError, onApplied });
	callbacks.current = { onError, onApplied };
	const surface = useMemo(() => createIconSurfaceLifecycle({
		revision,
		apply: (input) => app.SetDesktopIconSurface(input),
		onError: (cause) => callbacks.current.onError?.(cause),
		onApplied: (result) => callbacks.current.onApplied?.(result),
	}), [revision]);
	useLayoutEffect(() => {
		surface.activate();
		return () => surface.deactivate();
	}, [surface]);
	return surface;
}
