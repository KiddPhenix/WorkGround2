import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import gsap from "gsap";
import { DUR_FAST, EASE_OUT, prefersReducedMotion } from "./gsapAnimations";

const BOTTOM_THRESHOLD_PX = 80;

function isNearBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_THRESHOLD_PX;
}

export type QuestionScrollSnapshot = {
  count: number;
  lastId: string;
};

export function resolveScrollElement<T extends HTMLElement>(inner: T | null, host?: HTMLElement | null): HTMLElement | null {
  return host ?? inner;
}

export function snapElementToBottom(el: Pick<HTMLElement, "scrollTop" | "scrollHeight">): void {
  el.scrollTop = el.scrollHeight;
}

export function shouldAutoScrollForQuestionChange(prev: QuestionScrollSnapshot, next: QuestionScrollSnapshot): boolean {
  if (next.count <= prev.count) return false;
  if (prev.count === 0 && prev.lastId === "") return false;
  if (next.lastId === "" || next.lastId === prev.lastId) return false;
  return true;
}

function nextFrame(): Promise<void> {
  return new Promise((resolve) => requestAnimationFrame(() => resolve()));
}

/**
 * useScrollManager — GSAP-driven auto-scroll for the transcript container.
 *
 * - Auto-pins to the bottom when content is near the edge.
 * - Smooth scroll for jump-to-question navigation.
 * - Uses gsap.scrollTo for layout-safe scrolling (avoids layout thrashing).
 * - Batches ResizeObserver callbacks into a single GSAP tween.
 *
 * When `scrollHostRef` is provided (e.g. workbench .conversation-viewport),
 * all scroll operations target that element instead of the internal scrollRef.
 * This keeps a single scroll-owner contract: the workbench's outer viewport
 * drives scrolling, while the inner transcript has overflow:visible.
 */
export function useScrollManager(scrollHostRef?: RefObject<HTMLElement | null>) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const gsapCtx = useRef<gsap.Context | null>(null);
  const questionSnapshot = useRef<QuestionScrollSnapshot>({ count: 0, lastId: "" });
  const resizeFrame = useRef<number | null>(null);
  const repinFrame = useRef<number | null>(null);
  const pendingRepinHeightDelta = useRef(0);
  const layoutScrollFrames = useRef<number[]>([]);
  const lastClientHeight = useRef<number | null>(null);
  const lastFooterHeight = useRef<number | null>(null);
  const [isAtBottom, setIsAtBottom] = useState(true);

  // Resolve the real scroll element: host ref when provided, else internal ref.
  const scrollEl = useCallback((): HTMLElement | null => {
    return resolveScrollElement(scrollRef.current, scrollHostRef?.current);
  }, [scrollHostRef]);

  // Kill any lingering tweens on unmount.
  useEffect(() => {
    return () => {
      gsapCtx.current?.revert();
      if (resizeFrame.current !== null) cancelAnimationFrame(resizeFrame.current);
      if (repinFrame.current !== null) cancelAnimationFrame(repinFrame.current);
      for (const frame of layoutScrollFrames.current) cancelAnimationFrame(frame);
      layoutScrollFrames.current = [];
    };
  }, []);

  const updateBottomState = useCallback((el: HTMLElement) => {
    const atBottom = isNearBottom(el);
    stick.current = atBottom;
    setIsAtBottom(atBottom);
    return atBottom;
  }, []);

  // Workbench scrolls the outer conversation viewport. Listen to that same
  // element so manual scrolling updates stickiness instead of leaving the
  // inner, overflow-visible transcript as a second source of truth.
  useEffect(() => {
    const host = scrollHostRef?.current;
    if (!host) return;
    const handler = () => updateBottomState(host);
    host.addEventListener("scroll", handler, { passive: true });
    return () => host.removeEventListener("scroll", handler);
  }, [scrollHostRef, updateBottomState]);

  const onScroll = useCallback(() => {
    const el = scrollEl();
    if (el) updateBottomState(el);
  }, [updateBottomState, scrollEl]);

  /** Scroll smoothly to a specific element.  Used by the JumpBar. */
  const smoothScrollTo = useCallback((element: HTMLElement, offset = 12) => {
    const el = scrollEl();
    if (!el) return;
    stick.current = false;
    setIsAtBottom(false);
    if (resizeFrame.current !== null) {
      cancelAnimationFrame(resizeFrame.current);
      resizeFrame.current = null;
    }
    const rect = element.getBoundingClientRect();
    const containerRect = el.getBoundingClientRect();
    const top = el.scrollTop + rect.top - containerRect.top - offset;
    const reduced = prefersReducedMotion();
    gsap.to(el, {
      scrollTo: { y: Math.max(0, top) },
      duration: reduced ? 0.001 : DUR_FAST * 2,
      ease: EASE_OUT,
      onComplete: () => updateBottomState(el),
    });
  }, [updateBottomState, scrollEl]);

  /** Force-scroll to the bottom — used when a new question is sent. */
  const scrollToBottom = useCallback((force = false) => {
    const el = scrollEl();
    if (!el) return;
    if (force) {
      stick.current = true;
      setIsAtBottom(true);
    }
    if (!stick.current && !force) return;
    if (resizeFrame.current !== null) {
      cancelAnimationFrame(resizeFrame.current);
      resizeFrame.current = null;
    }
    resizeFrame.current = requestAnimationFrame(() => {
      resizeFrame.current = null;
      if (!stick.current && !force) return;
      if (force) {
        stick.current = true;
        setIsAtBottom(true);
      }
      const reduced = prefersReducedMotion();
      gsap.to(el, {
        scrollTo: { y: "max" },
        duration: reduced ? 0.001 : DUR_FAST,
        ease: "none",
        overwrite: "auto",
        onComplete: () => {
          stick.current = true;
          setIsAtBottom(true);
        },
      });
    });
  }, [scrollEl]);

  const snapToBottom = useCallback(() => {
    const el = scrollEl();
    if (!el) return;
    if (resizeFrame.current !== null) {
      cancelAnimationFrame(resizeFrame.current);
      resizeFrame.current = null;
    }
    gsap.killTweensOf(el);
    stick.current = true;
    snapElementToBottom(el);
    setIsAtBottom(true);
  }, [scrollEl]);

  const preserveScrollAnchor = useCallback(async <T,>(work: () => T | Promise<T>): Promise<T> => {
    const el = scrollEl();
    const beforeTop = el?.scrollTop ?? 0;
    const beforeHeight = el?.scrollHeight ?? 0;
    const wasAtBottom = el ? isNearBottom(el) : true;
    let result!: T;
    try {
      result = await work();
    } finally {
      await nextFrame();
      const after = scrollEl();
      if (after && !wasAtBottom) {
        const delta = after.scrollHeight - beforeHeight;
        if (delta !== 0) after.scrollTop = Math.max(0, beforeTop + delta);
        updateBottomState(after);
      }
    }
    return result;
  }, [updateBottomState, scrollEl]);

  const scrollToBottomAfterLayout = useCallback((frames = 4) => {
    for (const frame of layoutScrollFrames.current) cancelAnimationFrame(frame);
    layoutScrollFrames.current = [];
    snapToBottom();
    let remaining = Math.max(0, frames);
    const tick = () => {
      if (remaining <= 0) return;
      const frame = requestAnimationFrame(() => {
        layoutScrollFrames.current = layoutScrollFrames.current.filter((id) => id !== frame);
        snapToBottom();
        remaining -= 1;
        tick();
      });
      layoutScrollFrames.current.push(frame);
    };
    tick();
  }, [snapToBottom]);

  /** Call when a new question is submitted — overrides stick state. */
  const onNewQuestion = useCallback(() => {
    stick.current = true;
    scrollToBottom(true);
  }, [scrollToBottom]);

  /**
   * Refresh pin state on resize — call from a ResizeObserver on the container.
   */
  const repinIfWasPinned = useCallback(
    (containerHeightDelta: number) => {
      const el = scrollEl();
      if (!el) return;
      const bottomDistance = el.scrollHeight - el.scrollTop - el.clientHeight;
      if (!stick.current && bottomDistance + containerHeightDelta >= BOTTOM_THRESHOLD_PX) return;
      stick.current = true;
      setIsAtBottom(true);
      scrollToBottom();
    },
    [scrollToBottom, scrollEl],
  );

  const scheduleRepinIfWasPinned = useCallback(
    (containerHeightDelta: number) => {
      pendingRepinHeightDelta.current += containerHeightDelta;
      if (repinFrame.current !== null) return;
      repinFrame.current = requestAnimationFrame(() => {
        repinFrame.current = null;
        const delta = pendingRepinHeightDelta.current;
        pendingRepinHeightDelta.current = 0;
        repinIfWasPinned(delta);
      });
    },
    [repinIfWasPinned],
  );

  /**
   * Track appended user questions. Prepended history increases the count but
   * keeps the same last question id, so it must not trigger bottom scroll.
   */
  const resetQuestionTracking = useCallback((snapshot: QuestionScrollSnapshot) => {
    questionSnapshot.current = snapshot;
  }, []);

  const trackQuestions = useCallback(
    (next: QuestionScrollSnapshot) => {
      const prev = questionSnapshot.current;
      if (shouldAutoScrollForQuestionChange(prev, next)) {
        onNewQuestion();
      }
      questionSnapshot.current = next;
    },
    [onNewQuestion],
  );

  return {
    scrollRef,
    scrollEl,
    stick,
    onScroll,
    isAtBottom,
    smoothScrollTo,
    scrollToBottom,
    scrollToBottomAfterLayout,
    preserveScrollAnchor,
    onNewQuestion,
    repinIfWasPinned,
    scheduleRepinIfWasPinned,
    resetQuestionTracking,
    trackQuestions,
    resizeFrame,
    lastClientHeight,
    lastFooterHeight,
  };
}
