import type { RuntimeMode } from "./types";

export type WorkChatRoute = "work_send" | "steer";

export function isWorkChatDisabled(input: {
  ready: boolean;
  rewindCommitting: boolean;
  messageActionPending: boolean;
  clearContextPending: boolean;
}): boolean {
  return !input.ready
    || input.rewindCommitting
    || input.messageActionPending
    || input.clearContextPending;
}

export function routeWorkChat(input: {
  running: boolean;
  runtimeMode: RuntimeMode;
  decisionPending: boolean;
}): WorkChatRoute {
  // Approval/ask keeps the underlying controller turn alive. Front-face Work
  // chat remains available and queues guidance for that turn; the user can
  // still open the Session face manually when they want to answer the prompt.
  if (input.decisionPending) return "steer";
  if (input.running && input.runtimeMode !== "waiting_user") return "steer";
  return "work_send";
}
