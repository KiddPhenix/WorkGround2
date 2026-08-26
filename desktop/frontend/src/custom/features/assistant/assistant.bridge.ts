import { app } from "../../../lib/bridge";
import type { ProjectNode } from "../../../lib/types";
import type {
  AssistantAttentionItem,
  AssistantCancelInput,
  AssistantChannel,
  AssistantChannelInput,
	AssistantChangeProposal,
  AssistantCreateInput,
  AssistantDeleteInput,
  AssistantMemory,
  AssistantMemoryInput,
  AssistantRecord,
  AssistantListResult,
  AssistantResolveAttentionInput,
	AssistantResolveProposalInput,
  AssistantResumeInput,
  AssistantRoutine,
  AssistantRoutineInput,
  AssistantRun,
  AssistantRunNowInput,
  AssistantSnapshot,
  AssistantSubmitInputInput,
  AssistantUpdateInput,
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

export function assistantPickWorkspace(defaultDir = ""): Promise<string> {
  return app.PickAssistantWorkspace(defaultDir) as Promise<string>;
}

// assistantListWorkspaces exposes the shared project tree; the create dialog
// keeps only registered project nodes with a valid root.
export function assistantListWorkspaces(): Promise<ProjectNode[]> {
  return app.ListProjectTree();
}
