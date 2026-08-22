// desktopIconTimers.ts owns the three transient-UI timers of the icon widget
// (delayed click, hover preview, preview close) behind one small object, so
// every close path cancels all of them before clearing state and a drag can
// never be resurrected by a timer that was scheduled before it started. The
// timer host is injectable for fake-timer unit tests.

export const CLICK_DELAY = 240;
export const PREVIEW_CLOSE_DELAY = 180;
export const DRAG_THRESHOLD = 7;

export type IconTimerKind = "click" | "hover" | "previewClose";

export interface TimerHost {
  setTimeout(fn: () => void, delay: number): number;
  clearTimeout(id: number): void;
}

export const windowTimerHost: TimerHost = {
  setTimeout: (fn, delay) => window.setTimeout(fn, delay),
  clearTimeout: (id) => window.clearTimeout(id),
};

export class IconTimers {
  private ids: Partial<Record<IconTimerKind, number>> = {};

  constructor(private readonly host: TimerHost = windowTimerHost) {}

  scheduleClick(fn: () => void, delay = CLICK_DELAY): void {
    this.schedule("click", fn, delay);
  }

  scheduleHover(fn: () => void, delay: number): void {
    this.schedule("hover", fn, delay);
  }

  schedulePreviewClose(fn: () => void, delay = PREVIEW_CLOSE_DELAY): void {
    this.schedule("previewClose", fn, delay);
  }

  clearClick(): void {
    this.clear("click");
  }

  clearHover(): void {
    this.clear("hover");
  }

  clearPreviewClose(): void {
    this.clear("previewClose");
  }

  // cancel clears every scheduled timer: a close or drag must never leave a
  // delayed open/preview behind that can fire after the state was cleared.
  cancel(): void {
    (Object.keys(this.ids) as IconTimerKind[]).forEach((kind) => this.clear(kind));
  }

  // pending reports the currently scheduled kinds (test observation).
  pending(): IconTimerKind[] {
    return (Object.keys(this.ids) as IconTimerKind[]).sort();
  }

  private schedule(kind: IconTimerKind, fn: () => void, delay: number): void {
    this.clear(kind);
    this.ids[kind] = this.host.setTimeout(fn, delay);
  }

  private clear(kind: IconTimerKind): void {
    const id = this.ids[kind];
    if (id === undefined) {
      return;
    }
    this.host.clearTimeout(id);
    delete this.ids[kind];
  }
}
