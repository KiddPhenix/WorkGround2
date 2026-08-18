import { normalizeWidgetZoom } from "./widgetZoom";

export interface IconRect { left: number; top: number; width: number; height: number; }
export interface PopupPlacement { left: number; bottom: number; arrowLeft: number; }
export interface IconHitRect { x: number; y: number; width: number; height: number; }

// WebView2 reports DOM rectangles in its zoomed CSS viewport. Convert them
// back to Wails logical window units before popup placement or Win32 clipping.
export function scaleIconRect(rect: IconRect, value: unknown): IconRect {
  const zoom = normalizeWidgetZoom(value);
  return {
    left: rect.left * zoom,
    top: rect.top * zoom,
    width: rect.width * zoom,
    height: rect.height * zoom,
  };
}

// getBoundingClientRect returns CSS pixels. devicePixelRatio is the browser's
// authoritative CSS-to-WebView raster conversion and already includes display
// scaling (and any WebView zoom), so native regions must not infer DPI again.
export function iconHitRect(rect: IconRect, devicePixelRatio: unknown, padding = 3): IconHitRect {
	const ratio = typeof devicePixelRatio === "number" && Number.isFinite(devicePixelRatio) && devicePixelRatio > 0 ? devicePixelRatio : 1;
	return {
		x: Math.floor((rect.left - padding) * ratio),
		y: Math.floor((rect.top - padding) * ratio),
		width: Math.ceil((rect.width + padding * 2) * ratio),
		height: Math.ceil((rect.height + padding * 2) * ratio),
	};
}

// Pending intent wins because changing its workspace would change an idempotent
// retry. An explicit source icon wins over the remembered idle preference.
export function quickStartWorkspaceIndex(keys: string[], pending = "", requested = "", remembered = ""): number {
  for (const candidate of [pending, requested, remembered]) {
    const index = keys.indexOf(candidate);
    if (index >= 0) return index;
  }
  return 0;
}

// placeIconPopup clamps the panel inside the viewport while retaining an arrow
// that points at the source icon center.
export function placeIconPopup(anchor: IconRect, viewportWidth: number, viewportHeight: number, popupWidth: number, margin = 10, gap = 9): PopupPlacement {
  const center = anchor.left + anchor.width / 2;
  const left = Math.max(margin, Math.min(center - popupWidth / 2, viewportWidth - margin - popupWidth));
  return {
    left,
    bottom: Math.max(margin, viewportHeight - anchor.top + gap),
    arrowLeft: Math.max(14, Math.min(center - left, popupWidth - 14)),
  };
}
