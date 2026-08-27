import { useEffect, useRef } from "react";

// Steers the Assistant surface from the two widget-originated monotonic signals
// owned by the root App:
//   - assistantOpenSignal: the Assistant icon exited the widget and must open
//     the Assistant home.
//   - sessionRevealSignal: a widget action opened an explicit Session and must
//     collapse the Assistant surface and reconcile the frontend with the
//     backend's authoritative active Tab so that Session is visible.
// A plain "打开主窗口" exit bumps neither signal, so the previous surface is
// preserved. Each bump is applied exactly once (ref-guarded): the initial 0
// never fires, and unrelated re-renders cannot re-open or re-collapse the
// surface.
export function useAssistantSurfaceSignals(
  assistantOpenSignal: number,
  sessionRevealSignal: number,
  openAssistant: () => void,
  closeAssistant: () => void,
  revealActiveSession?: () => void,
): void {
  const appliedAssistantOpen = useRef(0);
  const appliedSessionReveal = useRef(0);
  useEffect(() => {
    if (assistantOpenSignal > 0 && assistantOpenSignal !== appliedAssistantOpen.current) {
      appliedAssistantOpen.current = assistantOpenSignal;
      openAssistant();
    }
  }, [assistantOpenSignal, openAssistant]);
  useEffect(() => {
    if (sessionRevealSignal > 0 && sessionRevealSignal !== appliedSessionReveal.current) {
      appliedSessionReveal.current = sessionRevealSignal;
      closeAssistant();
      revealActiveSession?.();
    }
  }, [sessionRevealSignal, closeAssistant, revealActiveSession]);
}
