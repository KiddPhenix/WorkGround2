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

// Collapse state is persisted under the stable cluster key. The old format
// carried a viewport-normalized anchor next to `collapsed`; that anchor is
// deliberately dropped because the WG2 anchor now drags the whole native
// window (CSS --wails-draggable) and the cluster is pinned to the bottom-right
// corner of the transparent window.
export function parseCollapseState(raw: string | null): boolean {
  if (!raw) return false;
  try {
    const value: unknown = JSON.parse(raw);
    if (typeof value === "boolean") return value;
    if (typeof value === "object" && value !== null && typeof (value as { collapsed?: unknown }).collapsed === "boolean") {
      return (value as { collapsed: boolean }).collapsed;
    }
  } catch { /* storage unavailable */ }
  return false;
}

export function serializeCollapseState(collapsed: boolean): string {
  return JSON.stringify({ collapsed });
}

// CLUSTER_EDGE_MARGIN is the physical-pixel inset from the visible root edges
// to the transformed cluster bounds (the cluster's own 18px right/bottom
// anchor plus a mirrored left edge).
export const CLUSTER_EDGE_MARGIN = 18;

// clusterGridMaxWidth returns the grid max-width in the reverse-zoomed frame's
// CSS pixels (= physical px). The .desktop-icon-mode frame is laid out at
// desktopZoom× the CSS viewport (innerWidth, i.e. physical/desktopZoom) and
// scaled back by 1/desktopZoom, so inside it 1 CSS px equals 1 physical px and
// the visible width is innerWidth×desktopZoom. Dividing the physical width
// (minus both edge margins) by the cluster zoom keeps the transformed cluster
// bound inside the visible root for every combination of desktopZoom
// (0.5..2) and clusterZoom (0.75..1.5), so overflow:hidden never clips it.
export function clusterGridMaxWidth(viewportWidth: number, desktopZoom: unknown, clusterZoom: unknown): number {
  const desktop = normalizeWidgetZoom(desktopZoom);
  const cluster = normalizeIconZoom(clusterZoom);
  const available = Math.max(0, viewportWidth * desktop - CLUSTER_EDGE_MARGIN * 2);
  return available / cluster;
}

// Widget-specific icon-cluster zoom. It is independent from the global WebView
// DesktopZoomFactor (which needs a restart and scales the whole app): this zoom
// scales only the icon cluster around its bottom-right anchor, applies
// instantly, and persists under the stable widget-local key. Default 1, range
// [0.75, 1.5], step 0.1; corrupt or out-of-range storage falls back to 1.
export const ICON_ZOOM_MIN = 0.75;
export const ICON_ZOOM_MAX = 1.5;
export const ICON_ZOOM_STEP = 0.1;

export function normalizeIconZoom(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value) || value < ICON_ZOOM_MIN || value > ICON_ZOOM_MAX) {
    return 1;
  }
  // Exact endpoints are reachable step targets (0.75 sits below the first
  // 0.1-grid value reachable from 1.0, and 1.5 is the clamped maximum). They
  // must survive re-normalization unchanged, otherwise a stored minimum would
  // snap to 0.8 on the next read and the zoom-out button would never stay
  // disabled at the true endpoint.
  if (value === ICON_ZOOM_MIN || value === ICON_ZOOM_MAX) {
    return value;
  }
  return Math.round(value * 10) / 10;
}

export function stepIconZoom(value: unknown, delta: number): number {
  const zoom = normalizeIconZoom(value);
  return Math.min(ICON_ZOOM_MAX, Math.max(ICON_ZOOM_MIN, Math.round((zoom + delta) * 10) / 10));
}

export function parseIconZoom(raw: string | null): number {
  if (!raw) return 1;
  try {
    const value: unknown = JSON.parse(raw);
    if (typeof value === "number") return normalizeIconZoom(value);
  } catch { /* corrupt storage falls back */ }
  return 1;
}

export function serializeIconZoom(zoom: number): string {
  return JSON.stringify(normalizeIconZoom(zoom));
}
