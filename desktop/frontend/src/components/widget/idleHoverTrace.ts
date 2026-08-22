// idleHoverTrace.ts owns the icon widget's low-overhead "first hover after
// idle" diagnostics trace. Pointer inactivity is tracked WITHOUT any periodic
// timer: listeners only stamp a monotonic clock, and the idle measurement
// happens lazily when a meaningful hover arrives. A qualifying hover opens one
// trace: an immediate hover_start record, then a bounded recovery window
// sampled by requestAnimationFrame + PerformanceObserver (longtask /
// layout-shift) and MutationObserver, closed by a short healthy-frame
// streak or a hard timeout, then exactly one hover_recovery summary. Records
// carry measurements and stable widget markers only — never titles, prompts,
// task content or user paths — and write failures are swallowed so logging can
// never break widget interaction.
import type { DesktopIconDiagnosticsInput } from "../../lib/bridge";

export const IDLE_HOVER_THRESHOLD_MS = 5000;
// A hover landing within this window after the first activity that broke an
// idle period inherits that period's measured idle. Pointer events inside the
// widget only fire within native hit regions, so the movement that ends an
// idle pause would otherwise reset the idle clock right before the hover.
export const IDLE_HOVER_BURST_WINDOW_MS = IDLE_HOVER_THRESHOLD_MS;
export const IDLE_HOVER_RECOVERY_WINDOW_MS = 3000; // hard timeout
export const IDLE_HOVER_HEALTHY_FRAMES = 10; // consecutive healthy frames
export const IDLE_HOVER_HEALTHY_GAP_MS = 40; // a frame gap at/under this is healthy

export type IdleHoverTargetKind = "icon" | "anchor";
export type IdleHoverEndedBy = "healthy" | "timeout" | "aborted";

export interface IdleHoverTraceContext {
  iconCount: number;
  revision: string;
}

// IdleHoverSensors are the pluggable recovery observers. Each starts observing
// and returns its disconnect; implementations count only and never retain DOM
// nodes or performance entries.
export interface IdleHoverSensors {
  longtask?(observe: (duration: number) => void): () => void;
  layoutShift?(observe: (shift: { value: number; hadRecentInput: boolean }) => void): () => void;
  mutation?(observe: (count: number) => void): () => void;
  visibilityChange?(observe: () => void): () => void;
}

export interface IdleHoverTracerOptions {
  // write persists one record. It is fire-and-forget: rejections are caught
  // inside the tracer and only reported to the console.
  write(record: DesktopIconDiagnosticsInput): void | Promise<void>;
  mono?(): number; // monotonic ms (performance.now-compatible)
  wall?(): number; // Date.now
  raf?(cb: (ts: number) => void): number;
  caf?(id: number): void;
  setTimeout?(fn: () => void, ms: number): number;
  clearTimeout?(id: number): void;
  visibility?(): string;
  focus?(): boolean;
  viewport?(): { w: number; h: number };
  dpr?(): number;
  sensors?: IdleHoverSensors;
}

// RecoverySession is the bounded sampling state for one active trace. It is
// created on trace start and cleared when the trace finishes (healthy streak,
// hard timeout, or dispose).
interface RecoverySession {
  traceId: string;
  t0: number;
  rafId: number;
  timeoutId: number;
  disconnects: Array<() => void>;
  frames: number;
  worstGapMs: number;
  gapSumMs: number;
  healthyStreak: number;
  lastFrameAt: number;
  longTasks: number;
  longTasksMaxMs: number;
  longTasksTotalMs: number;
  layoutShifts: number;
  visibilityChanges: number;
  domMutations: number;
  finished: boolean;
}

function defaultMonotonic(): number {
  return typeof performance !== "undefined" ? performance.now() : Date.now();
}

function noop(): void {}

export class IdleHoverTracer {
  private readonly write: (record: DesktopIconDiagnosticsInput) => void | Promise<void>;
  private readonly mono: () => number;
  private readonly wall: () => number;
  private readonly raf: (cb: (ts: number) => void) => number;
  private readonly caf: (id: number) => void;
  private readonly setTimeout: (fn: () => void, ms: number) => number;
  private readonly clearTimeout: (id: number) => void;
  private readonly visibility: () => string;
  private readonly focus: () => boolean;
  private readonly viewport: () => { w: number; h: number };
  private readonly dpr: () => number;
  private readonly sensors: Required<IdleHoverSensors>;

  private lastActivityAt: number;
  // When a burst of pointer events starts after an idle gap >= threshold, the
  // measured idle is captured here so the hover that ends the movement keeps
  // the true idle instead of the sub-frame reset caused by the approach. Note:
  // slow continuous movement (event gaps < threshold) never re-arms the burst,
  // so the idle falls back to the gap since the last event.
  private burstIdleMs: number | null = null;
  private burstStartAt = 0;
  private session: RecoverySession | null = null;

  constructor(options: IdleHoverTracerOptions) {
    this.write = options.write;
    this.mono = options.mono ?? defaultMonotonic;
    this.wall = options.wall ?? (() => Date.now());
    this.raf = options.raf ?? ((cb) => requestAnimationFrame(cb));
    this.caf = options.caf ?? ((id) => cancelAnimationFrame(id));
    this.setTimeout = options.setTimeout ?? ((fn, ms) => window.setTimeout(fn, ms));
    this.clearTimeout = options.clearTimeout ?? ((id) => window.clearTimeout(id));
    this.visibility = options.visibility ?? (() => (typeof document !== "undefined" ? document.visibilityState : "visible"));
    this.focus = options.focus ?? (() => (typeof document !== "undefined" ? document.hasFocus() : true));
    this.viewport = options.viewport ?? (() => (typeof window !== "undefined" ? { w: window.innerWidth, h: window.innerHeight } : { w: 0, h: 0 }));
    this.dpr = options.dpr ?? (() => (typeof window !== "undefined" ? window.devicePixelRatio : 1));
    this.sensors = {
      longtask: options.sensors?.longtask ?? defaultLongtaskSensor,
      layoutShift: options.sensors?.layoutShift ?? defaultLayoutShiftSensor,
      mutation: options.sensors?.mutation ?? defaultMutationSensor,
      visibilityChange: options.sensors?.visibilityChange ?? defaultVisibilityChangeSensor,
    };
    // The widget starts "idle" from mount; a first hover long after mount is
    // measured against mount time, not against a zero clock.
    this.lastActivityAt = this.mono();
  }

  get idleMs(): number {
    return this.mono() - this.lastActivityAt;
  }

  get active(): boolean {
    return this.session !== null;
  }

  // pointerActivity stamps any pointer/key interaction. It is called from
  // window listeners (pointerover/pointermove/pointerdown/pointerup/wheel/
  // keydown); no timer is involved, so tracking idle is free.
  pointerActivity(): void {
    const now = this.mono();
    if (now - this.lastActivityAt >= IDLE_HOVER_THRESHOLD_MS) {
      this.burstIdleMs = now - this.lastActivityAt;
      this.burstStartAt = now;
    }
    this.lastActivityAt = now;
  }

  // hoverEnter is the single entry for a meaningful icon/anchor hover. While a
  // trace is active further hovers and pointer movement are ignored, so one
  // recovery can never start duplicate traces.
  hoverEnter(kind: IdleHoverTargetKind, context: IdleHoverTraceContext): void {
    if (this.session) return;
    const now = this.mono();
    let idleMs = now - this.lastActivityAt;
    if (this.burstIdleMs !== null && now - this.burstStartAt <= IDLE_HOVER_BURST_WINDOW_MS) {
      idleMs = this.burstIdleMs;
    }
    if (idleMs < IDLE_HOVER_THRESHOLD_MS) return;
    this.startTrace(kind, context, idleMs, now);
  }

  // dispose aborts any active recovery and closes the trace with an "aborted"
  // summary. It is idempotent and safe to call from unmount, and the instance
  // stays usable afterwards: React StrictMode double-mounts effects in dev
  // (mount -> cleanup -> mount), so the same tracer must survive its own
  // cleanup without being permanently disabled.
  dispose(): void {
    const session = this.session;
    if (session) this.finishRecovery(session, "aborted");
  }

  private startTrace(kind: IdleHoverTargetKind, context: IdleHoverTraceContext, idleMs: number, now: number): void {
    this.burstIdleMs = null;
    const traceId = `${kind}:${this.wall()}-${Math.random().toString(36).slice(2, 10)}`;
    this.emit({
      kind: "hover_start",
      traceId,
      targetKind: kind,
      // Go validates/persists millisecond fields as int64. Browser monotonic
      // clocks and PerformanceObserver durations are fractional, so normalise
      // them at the bridge boundary instead of letting Wails reject the call
      // before it reaches WriteDesktopIconDiagnostics.
      idleMs: Math.round(idleMs),
      ts: this.wall(),
      t0: Math.round(now),
      visibility: this.visibility(),
      focus: this.focus(),
      viewportW: this.viewport().w,
      viewportH: this.viewport().h,
      dpr: this.dpr(),
      iconCount: context.iconCount,
      revision: context.revision,
    });
    const session: RecoverySession = {
      traceId,
      t0: now,
      rafId: 0,
      timeoutId: 0,
      disconnects: [],
      frames: 0,
      worstGapMs: 0,
      gapSumMs: 0,
      healthyStreak: 0,
      lastFrameAt: now,
      longTasks: 0,
      longTasksMaxMs: 0,
      longTasksTotalMs: 0,
      layoutShifts: 0,
      visibilityChanges: 0,
      domMutations: 0,
      finished: false,
    };
    this.session = session;
    session.disconnects.push(
      this.sensors.longtask((duration) => {
        session.longTasks += 1;
        session.longTasksTotalMs += duration;
        if (duration > session.longTasksMaxMs) session.longTasksMaxMs = duration;
      }),
      this.sensors.layoutShift((shift) => {
        if (shift.value > 0 && !shift.hadRecentInput) session.layoutShifts += 1;
      }),
      this.sensors.mutation((count) => {
        session.domMutations += count;
      }),
      this.sensors.visibilityChange(() => {
        session.visibilityChanges += 1;
      }),
    );
    // One bounded hard-timeout timer: it closes the window even when the
    // document is hidden and rAF stops firing. It is cleared on finish.
    session.timeoutId = this.setTimeout(() => this.finishRecovery(session, "timeout"), IDLE_HOVER_RECOVERY_WINDOW_MS);
    session.rafId = this.raf((ts) => this.onFrame(session, ts));
  }

  private onFrame(session: RecoverySession, ts: number): void {
    if (session.finished) return;
    const gap = ts - session.lastFrameAt;
    session.lastFrameAt = ts;
    session.frames += 1;
    session.gapSumMs += gap;
    if (gap > session.worstGapMs) session.worstGapMs = gap;
    if (gap <= IDLE_HOVER_HEALTHY_GAP_MS) {
      session.healthyStreak += 1;
    } else {
      session.healthyStreak = 0;
    }
    if (session.healthyStreak >= IDLE_HOVER_HEALTHY_FRAMES) {
      this.finishRecovery(session, "healthy");
      return;
    }
    if (this.mono() - session.t0 >= IDLE_HOVER_RECOVERY_WINDOW_MS) {
      this.finishRecovery(session, "timeout");
      return;
    }
    session.rafId = this.raf((next) => this.onFrame(session, next));
  }

  private finishRecovery(session: RecoverySession, endedBy: IdleHoverEndedBy): void {
    if (session.finished) return;
    session.finished = true;
    if (this.session === session) this.session = null;
    this.caf(session.rafId);
    this.clearTimeout(session.timeoutId);
    session.disconnects.forEach((disconnect) => {
      try {
        disconnect();
      } catch {
        // Sensor teardown must never throw into widget code paths.
      }
    });
    this.emit({
      kind: "hover_recovery",
      traceId: session.traceId,
      ts: this.wall(),
      durationMs: Math.round(this.mono() - session.t0),
      frames: session.frames,
      worstFrameGapMs: Math.round(session.worstGapMs),
      avgFrameGapMs: session.frames > 0 ? Math.round(session.gapSumMs / session.frames) : 0,
      longTasks: session.longTasks,
      longTasksMaxMs: Math.round(session.longTasksMaxMs),
      longTasksTotalMs: Math.round(session.longTasksTotalMs),
      visibilityChanges: session.visibilityChanges,
      domMutations: session.domMutations,
      layoutShifts: session.layoutShifts,
      endedBy,
    });
  }

  // emit persists one record fire-and-forget: a failed diagnostics write is
  // logged to the console at most, never surfaced in widget UI.
  private emit(record: DesktopIconDiagnosticsInput): void {
    try {
      void Promise.resolve(this.write(record)).catch((cause) => {
        console.debug("[icon-widget-diagnostics] write failed", cause);
      });
    } catch (cause) {
      console.debug("[icon-widget-diagnostics] write failed", cause);
    }
  }
}

function defaultLongtaskSensor(observe: (duration: number) => void): () => void {
  if (typeof PerformanceObserver === "undefined" || typeof PerformanceObserver.supportedEntryTypes !== "undefined" && !PerformanceObserver.supportedEntryTypes.includes("longtask")) {
    return noop;
  }
  try {
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) observe(entry.duration);
    });
    observer.observe({ type: "longtask", buffered: false });
    return () => observer.disconnect();
  } catch {
    return noop;
  }
}

function defaultLayoutShiftSensor(observe: (shift: { value: number; hadRecentInput: boolean }) => void): () => void {
  if (typeof PerformanceObserver === "undefined" || typeof PerformanceObserver.supportedEntryTypes !== "undefined" && !PerformanceObserver.supportedEntryTypes.includes("layout-shift")) {
    return noop;
  }
  try {
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        const shift = entry as unknown as { value?: number; hadRecentInput?: boolean };
        observe({ value: shift.value ?? 0, hadRecentInput: shift.hadRecentInput ?? false });
      }
    });
    observer.observe({ type: "layout-shift", buffered: false });
    return () => observer.disconnect();
  } catch {
    return noop;
  }
}

function defaultMutationSensor(observe: (count: number) => void): () => void {
  if (typeof MutationObserver === "undefined" || typeof document === "undefined") return noop;
  try {
    const observer = new MutationObserver((records) => observe(records.length));
    observer.observe(document.documentElement, { childList: true, subtree: true, characterData: true, attributes: true });
    return () => observer.disconnect();
  } catch {
    return noop;
  }
}

function defaultVisibilityChangeSensor(observe: () => void): () => void {
  if (typeof document === "undefined") return noop;
  document.addEventListener("visibilitychange", observe);
  return () => document.removeEventListener("visibilitychange", observe);
}
