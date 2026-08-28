import { app } from "../../../lib/bridge";
import type { ProjectNode } from "../../../lib/types";
import type {
  AssistantAttentionItem,
  AssistantAnswerRequest,
  AssistantCancelInput,
  AssistantCancelJobInput,
  AssistantChannel,
  AssistantChannelInput,
	AssistantChangeProposal,
  AssistantCreateInput,
  AssistantDeleteInput,
  AssistantDispatch,
  AssistantDispatchStreamEvent,
  AssistantIdeateInput,
  AssistantIdeaProposal,
  AssistantManagedSession,
  AssistantMemory,
  AssistantMemoryInput,
  AssistantRecord,
  AssistantListResult,
  AssistantResolveAttentionInput,
	AssistantResolveIdeaInput,
  AssistantResolveProposalInput,
  AssistantResumeInput,
  AssistantRetryDispatchInput,
  AssistantRetryJobInput,
  AssistantRoutine,
  AssistantRoutineInput,
  AssistantRun,
  AssistantRunNowInput,
  AssistantRunnerJob,
  AssistantSessionControlResult,
  AssistantSessionRequest,
  AssistantSessionStatusView,
  AssistantSnapshot,
  AssistantSteerRequest,
  AssistantSubmitInput,
  AssistantSubmitInputInput,
  AssistantSupervisorDiagnostic,
  AssistantUpdateInput,
  AssistantWorkControl,
  AssistantWorkControlInput,
  AssistantViewportSnapshot,
  AssistantPublishViewportInput,
} from "./assistant.types";

export function normalizeAssistantList(value: unknown): AssistantListResult {
  if (Array.isArray(value)) return { items: value as AssistantRecord[], diagnostics: [] };
  if (!value || typeof value !== "object") return { items: [], diagnostics: [] };
  const result = value as Partial<AssistantListResult>;
  return {
    items: Array.isArray(result.items) ? result.items : [],
    diagnostics: Array.isArray(result.diagnostics) ? result.diagnostics : [],
  };
}

export function assistantList(): Promise<AssistantListResult> {
  return app.AssistantList().then(normalizeAssistantList);
}

export function assistantGet(id: string): Promise<AssistantSnapshot> {
  return app.AssistantGet(id) as Promise<AssistantSnapshot>;
}

export function assistantCreate(input: AssistantCreateInput): Promise<AssistantSnapshot> {
  return app.AssistantCreate(input) as Promise<AssistantSnapshot>;
}

export function assistantUpdate(input: AssistantUpdateInput): Promise<AssistantRecord> {
  return app.AssistantUpdate(input) as Promise<AssistantRecord>;
}

export function assistantDelete(input: AssistantDeleteInput): Promise<void> {
  return app.AssistantDelete(input);
}

export function assistantPutRoutine(input: AssistantRoutineInput): Promise<AssistantRoutine> {
  return app.AssistantPutRoutine(input) as Promise<AssistantRoutine>;
}

export function assistantApplyMemory(input: AssistantMemoryInput): Promise<AssistantMemory> {
  return app.AssistantApplyMemory(input) as Promise<AssistantMemory>;
}

export function assistantPutChannel(input: AssistantChannelInput): Promise<AssistantChannel> {
  return app.AssistantPutChannel(input) as Promise<AssistantChannel>;
}

export function assistantRunNow(input: AssistantRunNowInput): Promise<AssistantRun> {
  return app.AssistantRunNow(input) as Promise<AssistantRun>;
}

export function assistantSubmitInput(input: AssistantSubmitInputInput): Promise<AssistantRun> {
  return app.AssistantSubmitInput(input) as Promise<AssistantRun>;
}

export function assistantResolveAttention(input: AssistantResolveAttentionInput): Promise<AssistantAttentionItem> {
  return app.AssistantResolveAttention(input) as Promise<AssistantAttentionItem>;
}

export function assistantResolveProposal(input: AssistantResolveProposalInput): Promise<AssistantChangeProposal> {
	return app.AssistantResolveProposal(input) as Promise<AssistantChangeProposal>;
}

export function assistantResume(input: AssistantResumeInput): Promise<AssistantRun> {
  return app.AssistantResume(input) as Promise<AssistantRun>;
}

export function assistantCancel(input: AssistantCancelInput): Promise<AssistantRun> {
  return app.AssistantCancel(input) as Promise<AssistantRun>;
}

export function assistantSubmit(input: AssistantSubmitInput): Promise<AssistantDispatch> {
  return app.AssistantSubmit(input) as Promise<AssistantDispatch>;
}

export function assistantRetryDispatch(input: AssistantRetryDispatchInput): Promise<AssistantDispatch> {
  return app.AssistantRetryDispatch(input.assistantId, input.dispatchId, input.requestId) as Promise<AssistantDispatch>;
}

export function assistantIdeate(input: AssistantIdeateInput): Promise<AssistantIdeaProposal> {
  return app.AssistantIdeate(input) as Promise<AssistantIdeaProposal>;
}

export function assistantResolveIdea(input: AssistantResolveIdeaInput): Promise<AssistantIdeaProposal> {
  return app.AssistantResolveIdea(input) as Promise<AssistantIdeaProposal>;
}

export function assistantRetryJob(input: AssistantRetryJobInput): Promise<AssistantRunnerJob> {
  return app.AssistantRetryJob(input) as Promise<AssistantRunnerJob>;
}

export function assistantCancelJob(input: AssistantCancelJobInput): Promise<AssistantRunnerJob> {
  return app.AssistantCancelJob(input) as Promise<AssistantRunnerJob>;
}

export function assistantPickWorkspace(defaultDir = ""): Promise<string> {
  return app.PickAssistantWorkspace(defaultDir) as Promise<string>;
}

export function assistantWorkControl(): Promise<AssistantWorkControl> {
  return app.AssistantWorkControl() as Promise<AssistantWorkControl>;
}

export function assistantPauseAll(input: AssistantWorkControlInput): Promise<AssistantWorkControl> {
  return app.AssistantPauseAll(input) as Promise<AssistantWorkControl>;
}

export function assistantResumeAll(input: AssistantWorkControlInput): Promise<AssistantWorkControl> {
  return app.AssistantResumeAll(input) as Promise<AssistantWorkControl>;
}

export function assistantPauseForRestart(input: AssistantWorkControlInput): Promise<AssistantWorkControl> {
  return app.AssistantPauseForRestart(input) as Promise<AssistantWorkControl>;
}

export function assistantPublishViewport(input: AssistantPublishViewportInput): void {
  app.AssistantPublishViewport(input);
}

export function assistantViewport(): Promise<[AssistantViewportSnapshot, boolean]> {
  return app.AssistantViewport() as Promise<[AssistantViewportSnapshot, boolean]>;
}

// ── 受管 Session 只读视图与用户控制 ──────────────────────────

export function assistantManagedSessions(assistantID: string): Promise<AssistantManagedSession[]> {
  return app.AssistantManagedSessions(assistantID) as Promise<AssistantManagedSession[]>;
}

export function assistantSessionStatus(sessionID: string): Promise<AssistantSessionStatusView> {
  return app.AssistantSessionStatus(sessionID) as Promise<AssistantSessionStatusView>;
}

export function assistantSessionSteer(req: AssistantSteerRequest): Promise<AssistantSessionControlResult> {
  return app.AssistantSessionSteer(req) as Promise<AssistantSessionControlResult>;
}

export function assistantSessionAnswer(req: AssistantAnswerRequest): Promise<AssistantSessionControlResult> {
  return app.AssistantSessionAnswer(req) as Promise<AssistantSessionControlResult>;
}

export function assistantSessionCancel(req: AssistantSessionRequest): Promise<AssistantSessionControlResult> {
  return app.AssistantSessionCancel(req) as Promise<AssistantSessionControlResult>;
}

export function assistantSessionResume(req: AssistantSessionRequest): Promise<AssistantSessionControlResult> {
  return app.AssistantSessionResume(req) as Promise<AssistantSessionControlResult>;
}

export function assistantSessionFork(req: AssistantSessionRequest): Promise<AssistantSessionControlResult> {
  return app.AssistantSessionFork(req) as Promise<AssistantSessionControlResult>;
}

export function assistantSupervisorDiagnostic(assistantID: string): Promise<AssistantSupervisorDiagnostic> {
  return app.AssistantSupervisorDiagnostic(assistantID) as Promise<AssistantSupervisorDiagnostic>;
}

// onAssistantDispatchStream subscribes to the typed inline live-reply event
// emitted by the Go Assistant runtime. Outside the Wails shell it is a no-op;
// the pure reducer is still exercised by the frontend logic tests.
export function onAssistantDispatchStream(cb: (event: AssistantDispatchStreamEvent) => void): () => void {
  if (typeof window !== "undefined" && window.runtime?.EventsOn) {
    return window.runtime.EventsOn("assistant:dispatch-stream", (payload?: unknown) => cb(payload as AssistantDispatchStreamEvent));
  }
  return () => {};
}

// assistantListWorkspaces exposes the shared project tree; the create dialog
// keeps only registered project nodes with a valid root.
export function assistantListWorkspaces(): Promise<ProjectNode[]> {
  return app.ListProjectTree();
}
