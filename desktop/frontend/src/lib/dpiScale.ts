/**
 * DPI zoom scale service.
 *
 * Uses the WebView2 ZoomFactor (Go side) for reliable, layout-safe zooming.
 * The Go binding applies changes to the active WebView and persists the same
 * effective value for the next launch.
 *
 * Range: 0.50 – 2.00 (50% – 200%), step 0.05.
 */

export const MIN_ZOOM = 0.5;
export const MAX_ZOOM = 2.0;
export const ZOOM_STEP = 0.05;
export const DESKTOP_ZOOM_EVENT = "desktop:zoom-factor";

export type ZoomLevel = number; // 0.5 – 2.0

export const DEFAULT_ZOOM: ZoomLevel = 1.0;

// ─── helpers ────────────────────────────────────────────────────────

/** Snap a number to the nearest valid step within [MIN, MAX]. */
export function snapZoom(value: number): ZoomLevel {
  const clamped = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, value));
  const steps = Math.round((clamped - MIN_ZOOM) / ZOOM_STEP);
  return parseFloat((MIN_ZOOM + steps * ZOOM_STEP).toFixed(2));
}

/** Convert a zoom value (0.5-2.0) to a percentage integer (50-200). */
export function zoomToPercent(zoom: ZoomLevel): number {
  return Math.round(zoom * 100);
}

/** Convert a percentage integer (50-200) back to a zoom value (0.5-2.0). */
export function percentToZoom(pct: number): ZoomLevel {
  return snapZoom(pct / 100);
}
