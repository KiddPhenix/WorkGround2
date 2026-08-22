import type { CollaborationAgentConfig, CollaborationRecognitionMode, CollaborationToolApprovalMode } from "./types";

export type AutoResponseMode = "manual" | "questions" | "operations";

export function autoResponseMode(config: Pick<CollaborationAgentConfig, "autoRespondQuestions" | "autoRespondRequests">): AutoResponseMode {
  if (config.autoRespondRequests) return "operations";
  if (config.autoRespondQuestions) return "questions";
  return "manual";
}

export function autoResponseFlags(mode: AutoResponseMode): Pick<CollaborationAgentConfig, "autoRespondQuestions" | "autoRespondRequests"> {
  return {
    autoRespondQuestions: mode !== "manual",
    autoRespondRequests: mode === "operations",
  };
}

const autoResponseModes: AutoResponseMode[] = ["manual", "questions", "operations"];
const recognitionModes: CollaborationRecognitionMode[] = ["interval", "message", "off"];
const approvalModes: CollaborationToolApprovalMode[] = ["ask", "auto", "yolo"];

export function nextAutoResponseMode(mode: AutoResponseMode): AutoResponseMode {
  return autoResponseModes[(autoResponseModes.indexOf(mode) + 1) % autoResponseModes.length];
}

export function nextRecognitionMode(mode: CollaborationRecognitionMode): CollaborationRecognitionMode {
  return recognitionModes[(recognitionModes.indexOf(mode) + 1) % recognitionModes.length];
}

export function nextApprovalMode(mode: CollaborationToolApprovalMode): CollaborationToolApprovalMode {
  return approvalModes[(approvalModes.indexOf(mode) + 1) % approvalModes.length];
}
