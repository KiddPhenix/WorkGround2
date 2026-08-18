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

// Cluster control geometry and persistence. The WG2 anchor sits at the
// bottom-right corner of the icon cluster; rows and controls grow left/up
// from it, so its position is stored as viewport-normalized fractions that
// recover safely across monitor and window-size changes.
export interface ClusterAnchor { right: number; bottom: number; }
export interface ClusterSize { width: number; height: number; }
export interface ClusterViewport { width: number; height: number; }
export interface ClusterState { collapsed: boolean; anchor: { x: number; y: number }; }

export const DEFAULT_CLUSTER_STATE: ClusterState = { collapsed: false, anchor: { x: 1, y: 1 } };

// clampClusterAnchor keeps the whole visible cluster inside a safe margin.
// When the cluster is too large for the viewport, the anchor itself stays
// reachable at the viewport edge and the cluster may overflow left/up.
export function clampClusterAnchor(anchor: ClusterAnchor, size: ClusterSize, viewport: ClusterViewport, margin = 18): ClusterAnchor {
  const maxRight = Math.max(margin, viewport.width - margin);
  const maxBottom = Math.max(margin, viewport.height - margin);
  const minRight = Math.min(size.width + margin, maxRight);
  const minBottom = Math.min(size.height + margin, maxBottom);
  return {
    right: Math.min(Math.max(minRight, anchor.right), maxRight),
    bottom: Math.min(Math.max(minBottom, anchor.bottom), maxBottom),
  };
}

// parseClusterState accepts only well-formed persisted state. Malformed or
// missing input returns null so the caller falls back to bottom-right/expanded;
// out-of-range anchor fractions are normalized into 0..1.
export function parseClusterState(raw: string | null): ClusterState | null {
  if (!raw) return null;
  let value: unknown;
  try { value = JSON.parse(raw); } catch { return null; }
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (typeof record.collapsed !== "undefined" && typeof record.collapsed !== "boolean") return null;
  const collapsed = record.collapsed === true;
  const anchor = record.anchor;
  if (typeof anchor !== "object" || anchor === null || Array.isArray(anchor)) return null;
  const { x, y } = anchor as Record<string, unknown>;
  if (typeof x !== "number" || !Number.isFinite(x) || typeof y !== "number" || !Number.isFinite(y)) return null;
  return { collapsed, anchor: { x: Math.min(1, Math.max(0, x)), y: Math.min(1, Math.max(0, y)) } };
}

export function serializeClusterState(state: ClusterState): string {
  return JSON.stringify(state);
}
