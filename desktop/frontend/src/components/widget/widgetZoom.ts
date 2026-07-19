const MIN_DESKTOP_ZOOM = 0.5;
const MAX_DESKTOP_ZOOM = 2;

export type WidgetZoomFrame = {
  zoom: number;
  widthVw: number;
  heightVh: number;
  scale: number;
};

export function normalizeWidgetZoom(value: unknown): number {
  return typeof value === "number"
    && Number.isFinite(value)
    && value >= MIN_DESKTOP_ZOOM
    && value <= MAX_DESKTOP_ZOOM
    ? value
    : 1;
}

export function resolveWidgetZoomFrame(value: unknown): WidgetZoomFrame {
  const zoom = normalizeWidgetZoom(value);
  return {
    zoom,
    widthVw: zoom * 100,
    heightVh: zoom * 100,
    scale: 1 / zoom,
  };
}
