// 眼睛动画：帧选择是纯函数（fps/loop/holdLast 只从 manifest 读，禁止硬编码
// 副本），共享单例时钟驱动所有图标 —— 不新增每图标 timer；隐藏 tab 时 rAF
// 自动节流。reduced-motion：循环状态显示中间帧，单次状态显示末帧；不得用
// 隐藏状态层关闭动画（资源 README 契约）。
import { useEffect, useRef, useState } from "react";
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

// --- 共享单例时钟 -----------------------------------------------------------

type TickCallback = (nowMs: number) => void;

let tickSubscribers = new Set<TickCallback>();
let tickHandle: number | null = null;
let tickTimer: ReturnType<typeof setTimeout> | null = null;

function tickLoop(nowMs: number): void {
  for (const callback of tickSubscribers) callback(nowMs);
  tickHandle = null;
  tickTimer = null;
  scheduleTick();
}

function scheduleTick(): void {
  if (tickSubscribers.size === 0) return;
  if (typeof requestAnimationFrame === "function") {
    tickHandle = requestAnimationFrame(tickLoop);
  } else {
    // jsdom/测试环境无 rAF：退化为节流的 setTimeout，保证订阅不崩溃。
    tickTimer = setTimeout(() => tickLoop(Date.now()), 1000 / 60);
  }
}

/** 订阅全局动画时钟；返回退订函数。首个订阅者启动时钟，最后一个退订即停止。 */
export function subscribeAnimationTick(callback: TickCallback): () => void {
  tickSubscribers.add(callback);
  if (tickHandle === null && tickTimer === null) scheduleTick();
  return () => {
    tickSubscribers.delete(callback);
    if (tickSubscribers.size === 0) {
      if (tickHandle !== null) cancelAnimationFrame(tickHandle);
      if (tickTimer !== null) clearTimeout(tickTimer);
      tickHandle = null;
      tickTimer = null;
    }
  };
}

function clockNow(): number {
  return typeof performance === "object" && typeof performance.now === "function" ? performance.now() : Date.now();
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
    reducedMql.addEventListener("change", refreshReducedMotion);
  }
  refreshReducedMotion();
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

/**
 * 单图标动画 hook：状态/身份变化才重置 startedAt，否则同一状态反复渲染
 * 不重置；帧号由共享时钟推导。组件只消费帧号做 background-position 切换。
 */
export function useAgentEyeFrame(sessionId: string, eyeStatus: AgentEyeStatus, manifest: AgentManifest): number {
  const [now, setNow] = useState<number>(clockNow);
  const [reduced, setReduced] = useState<boolean>(getReducedMotion);
  const key = animationKey(sessionId, eyeStatus);
  const startedAtRef = useRef<number>(0);
  const lastKeyRef = useRef<string | null>(null);
  if (lastKeyRef.current !== key) {
    lastKeyRef.current = key;
    startedAtRef.current = clockNow();
  }
  useEffect(() => subscribeAnimationTick(setNow), []);
  useEffect(() => subscribeReducedMotion(setReduced), []);
  return eyeFrameAt(eyeStatus, startedAtRef.current, now, reduced, manifest);
}
