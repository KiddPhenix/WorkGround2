import { app } from "../../../lib/bridge";
import type {
  AssistantAttentionItem,
  AssistantCancelInput,
  AssistantCreateInput,
  AssistantMemory,
  AssistantMemoryInput,
  AssistantRecord,
  AssistantListResult,
  AssistantResolveAttentionInput,
  AssistantResumeInput,
  AssistantRoutine,
  AssistantRoutineInput,
  AssistantRun,
  AssistantRunNowInput,
  AssistantSnapshot,
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

export function assistantPutRoutine(input: AssistantRoutineInput): Promise<AssistantRoutine> {
  return app.AssistantPutRoutine(input) as Promise<AssistantRoutine>;
}

export function assistantApplyMemory(input: AssistantMemoryInput): Promise<AssistantMemory> {
  return app.AssistantApplyMemory(input) as Promise<AssistantMemory>;
}

export function assistantRunNow(input: AssistantRunNowInput): Promise<AssistantRun> {
  return app.AssistantRunNow(input) as Promise<AssistantRun>;
}

export function assistantResolveAttention(input: AssistantResolveAttentionInput): Promise<AssistantAttentionItem> {
  return app.AssistantResolveAttention(input) as Promise<AssistantAttentionItem>;
}

export function assistantResume(input: AssistantResumeInput): Promise<AssistantRun> {
  return app.AssistantResume(input) as Promise<AssistantRun>;
}

export function assistantCancel(input: AssistantCancelInput): Promise<AssistantRun> {
  return app.AssistantCancel(input) as Promise<AssistantRun>;
}
