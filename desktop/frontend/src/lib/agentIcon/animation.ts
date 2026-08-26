// 眼睛动画：帧选择是纯函数（fps/loop/holdLast 只从 manifest 读，禁止硬编码
// 副本）。逐帧工作交给 Web Animations compositor，React 只在身份/状态或
// reduced-motion 改变时配置一次 DOM；不使用 rAF/setState 热循环。
import { useEffect, type RefObject } from "react";
import type { AgentEyeStatus, AgentManifest } from "./types";

/** 动画 key：status 或 sessionId 变化时 startedAt 归零；同状态反复渲染不重置。 */
export function animationKey(sessionId: string, eyeStatus: AgentEyeStatus): string {
  return `${sessionId}:${eyeStatus}`;
}

/**
 * 纯帧选择（开发文档 §5.7）：帧数取 manifest 每状态帧数；
 * loop 状态按 (elapsed × fps) 取模循环；单次状态播完停在末帧。
 */
export function eyeFrameAt(
  eyeStatus: AgentEyeStatus,
  startedAtMs: number,
  nowMs: number,
  reducedMotion: boolean,
  manifest: AgentManifest,
): number {
  const eye = manifest.eyes.find((entry) => entry.id === eyeStatus);
  const frameCount = eye?.frames.length ?? 0;
  if (frameCount === 0 || eye === undefined) return 0; // 资源契约错误：帧缺失，调用方已在 assets 层上报
  const fps = eye.fps;
  const loop = eye.loop;
  if (reducedMotion) {
    return loop ? Math.floor(frameCount / 2) : frameCount - 1;
  }
  const phase = (Math.max(0, nowMs - startedAtMs) / 1000) * fps;
  if (!loop) return Math.min(Math.floor(phase), frameCount - 1); // 单次 + 停末帧
  return Math.floor(phase) % frameCount;
}

// --- prefers-reduced-motion 单例 -------------------------------------------------

type ReducedMotionCallback = (reduced: boolean) => void;

let reducedMotion = false;
let reducedListeners = new Set<ReducedMotionCallback>();
let reducedMql: MediaQueryList | null = null;

function refreshReducedMotion(event?: MediaQueryListEvent): void {
  reducedMotion = event ? event.matches : (typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches);
  for (const callback of reducedListeners) callback(reducedMotion);
}

export function subscribeReducedMotion(callback: ReducedMotionCallback): () => void {
  reducedListeners.add(callback);
  if (reducedListeners.size === 1 && typeof matchMedia === "function") {
    reducedMql = matchMedia("(prefers-reduced-motion: reduce)");
    reducedMotion = reducedMql.matches;
    reducedMql.addEventListener("change", refreshReducedMotion);
  }
  // Initialize only the new subscriber. Broadcasting here would restart every
  // existing icon once per mount (O(N²)); full broadcast belongs to real media
  // changes only.
  callback(reducedMotion);
  return () => {
    reducedListeners.delete(callback);
    if (reducedListeners.size === 0 && reducedMql) {
      reducedMql.removeEventListener("change", refreshReducedMotion);
      reducedMql = null;
    }
  };
}

export function getReducedMotion(): boolean {
  // This getter is used by React state initializers. Keep it read-only: calling
  // refreshReducedMotion here would notify already-mounted icons while a
  // different icon is rendering, which React treats as a render-phase update.
  return typeof matchMedia === "function"
    ? matchMedia("(prefers-reduced-motion: reduce)").matches
    : reducedMotion;
}

export interface AgentEyeAnimationPlan {
  initialTransform: string;
  keyframes: Keyframe[];
  options: KeyframeAnimationOptions;
}

function frameTransform(frame: number, frameCount: number): string {
  return `translateX(${-(frame / Math.max(1, frameCount)) * 100}%)`;
}

/** Build one compositor animation directly from the manifest contract. */
export function agentEyeAnimationPlan(eyeStatus: AgentEyeStatus, reduced: boolean, manifest: AgentManifest): AgentEyeAnimationPlan | null {
  const eye = manifest.eyes.find((entry) => entry.id === eyeStatus);
  const frameCount = eye?.frames.length ?? 0;
  if (!eye || frameCount === 0 || !Number.isFinite(eye.fps) || eye.fps <= 0) return null;
  const staticFrame = reduced ? (eye.loop ? Math.floor(frameCount / 2) : frameCount - 1) : 0;
  const initialTransform = frameTransform(staticFrame, frameCount);
  if (reduced || frameCount === 1) {
    return { initialTransform, keyframes: [], options: { duration: 0 } };
  }
  const denominator = frameCount;
  const keyframes: Keyframe[] = eye.frames.map((_, frame) => ({
    transform: frameTransform(frame, frameCount),
    offset: frame / denominator,
    easing: "steps(1, end)",
  }));
  keyframes.push({ transform: frameTransform(frameCount - 1, frameCount), offset: 1, easing: "steps(1, end)" });
  return {
    initialTransform,
    keyframes,
    options: {
      duration: (denominator / eye.fps) * 1000,
      iterations: eye.loop ? Infinity : 1,
      fill: !eye.loop && eye.holdLast ? "forwards" : "none",
    },
  };
}

const reportedAnimationErrors = new Set<string>();

function reportAnimationError(eyeStatus: AgentEyeStatus, cause: unknown): void {
  const message = cause instanceof Error ? cause.message : String(cause);
  const key = `${eyeStatus}:${message}`;
  if (reportedAnimationErrors.has(key)) return;
  reportedAnimationErrors.add(key);
  console.error(`[agent-icon] eye animation failed status=${eyeStatus}: ${message}`);
}

/** Configure the sprite compositor once; animation frames never enter React state. */
export function useAgentEyeAnimation(
  ref: RefObject<HTMLImageElement | null>,
  sessionId: string,
  eyeStatus: AgentEyeStatus,
  manifest: AgentManifest,
  enabled = true,
): void {
  useEffect(() => {
    const element = ref.current;
    if (!element || !enabled) return;
    let animation: Animation | null = null;
    const apply = (reduced: boolean) => {
      animation?.cancel();
      animation = null;
      const plan = agentEyeAnimationPlan(eyeStatus, reduced, manifest);
      if (!plan) {
        element.style.transform = frameTransform(0, 1);
        reportAnimationError(eyeStatus, new Error("invalid manifest fps or frames"));
        return;
      }
      element.style.transform = plan.initialTransform;
      if (plan.keyframes.length === 0) return;
      if (typeof element.animate !== "function") {
        reportAnimationError(eyeStatus, new Error("Web Animations API unavailable"));
        return;
      }
      try {
        animation = element.animate(plan.keyframes, plan.options);
      } catch (cause) {
        reportAnimationError(eyeStatus, cause);
      }
    };
    const unsubscribe = subscribeReducedMotion(apply);
    return () => {
      unsubscribe();
      animation?.cancel();
    };
  }, [enabled, eyeStatus, manifest, ref, sessionId]);
}
