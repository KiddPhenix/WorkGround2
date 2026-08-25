import { assistantRequestID } from "./assistant.types";

const PREFIX = "workground2:assistant-request:";
const MUTATION_PREFIX = "workground2:assistant-mutation:";
const memoryLedger = new Map<string, string>();
const memoryMutationLedger = new Map<string, AssistantMutationReceipt>();

export interface AssistantMutationReceipt {
  requestId: string;
  expectedRevision: number;
}

function stablePayload(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value) ?? String(value);
  if (Array.isArray(value)) return `[${value.map(stablePayload).join(",")}]`;
  return `{${Object.entries(value as Record<string, unknown>)
    .filter(([, item]) => item !== undefined)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, item]) => `${JSON.stringify(key)}:${stablePayload(item)}`)
    .join(",")}}`;
}

function storage(): Pick<Storage, "getItem" | "setItem" | "removeItem"> | null {
  try { return typeof localStorage === "undefined" ? null : localStorage; }
  catch { return null; }
}

export function assistantIntentKey(action: string, assistantID: string, subject = ""): string {
  let hash = 2166136261;
  for (const char of subject.trim()) {
    hash ^= char.charCodeAt(0);
    hash = Math.imul(hash, 16777619);
  }
  return `${action}:${assistantID}:${(hash >>> 0).toString(36)}`;
}

export function assistantMutationKey(action: string, assistantID: string, entityID: string, payload: unknown): string {
  return assistantIntentKey(action, `${assistantID}:${entityID}`, stablePayload(payload));
}

export function pendingAssistantRequest(key: string): string {
  const ledgerKey = `${PREFIX}${key}`;
  const persisted = storage()?.getItem(ledgerKey);
  if (persisted) return persisted;
  const current = memoryLedger.get(ledgerKey);
  if (current) return current;
  const requestID = assistantRequestID(key);
  memoryLedger.set(ledgerKey, requestID);
  try { storage()?.setItem(ledgerKey, requestID); } catch { /* memory fallback */ }
  return requestID;
}

export function completeAssistantRequest(key: string): void {
  const ledgerKey = `${PREFIX}${key}`;
  const mutationKey = `${MUTATION_PREFIX}${key}`;
  memoryLedger.delete(ledgerKey);
  memoryMutationLedger.delete(mutationKey);
  try {
    storage()?.removeItem(ledgerKey);
    storage()?.removeItem(mutationKey);
  } catch { /* memory fallback */ }
}

function parseMutationReceipt(value: string | null, expectedRevision: number): AssistantMutationReceipt | null {
  if (!value) return null;
  try {
    const parsed = JSON.parse(value) as Partial<AssistantMutationReceipt>;
    if (typeof parsed.requestId === "string" && typeof parsed.expectedRevision === "number") {
      return { requestId: parsed.requestId, expectedRevision: parsed.expectedRevision };
    }
  } catch { /* legacy request id string */ }
  return { requestId: value, expectedRevision };
}

export function pendingAssistantMutation(key: string, expectedRevision: number): AssistantMutationReceipt {
  const mutationKey = `${MUTATION_PREFIX}${key}`;
  const legacyKey = `${PREFIX}${key}`;
  const persisted = parseMutationReceipt(storage()?.getItem(mutationKey) ?? storage()?.getItem(legacyKey) ?? null, expectedRevision);
  if (persisted) {
    memoryMutationLedger.set(mutationKey, persisted);
    try {
      storage()?.setItem(mutationKey, JSON.stringify(persisted));
      storage()?.removeItem(legacyKey);
    } catch { /* memory fallback */ }
    return persisted;
  }
  const current = memoryMutationLedger.get(mutationKey);
  if (current) return current;
  const receipt = { requestId: assistantRequestID(key), expectedRevision };
  memoryMutationLedger.set(mutationKey, receipt);
  try { storage()?.setItem(mutationKey, JSON.stringify(receipt)); } catch { /* memory fallback */ }
  return receipt;
}

export async function runAssistantMutation<T>(key: string, mutate: (requestID: string) => Promise<T>): Promise<T> {
  const result = await mutate(pendingAssistantRequest(key));
  completeAssistantRequest(key);
  return result;
}

export async function runAssistantCASMutation<T>(key: string, expectedRevision: number, mutate: (receipt: AssistantMutationReceipt) => Promise<T>): Promise<T> {
  const result = await mutate(pendingAssistantMutation(key, expectedRevision));
  completeAssistantRequest(key);
  return result;
}

export async function runAssistantResume(input: {
  assistantID: string;
  attentionID: string;
  runID: string;
  resume: (requestID: string) => Promise<unknown>;
  completeKeys?: string[];
}): Promise<void> {
  const resumeKey = assistantIntentKey("attention-resume", input.assistantID, input.runID);
  await input.resume(pendingAssistantRequest(resumeKey));
  completeAssistantRequest(resumeKey);
  completeAssistantRequest(assistantIntentKey("attention-approve", input.assistantID, input.attentionID));
  input.completeKeys?.forEach(completeAssistantRequest);
}

export function assistantOutcomeKey(assistantID: string, attentionID: string, resolution: string): string {
  return assistantIntentKey(`attention-outcome-${resolution}`, assistantID, attentionID);
}

export async function runAssistantOutcome(input: {
  assistantID: string;
  attentionID: string;
  runID?: string;
  resolution: "retry_acknowledged" | "mark_succeeded" | "mark_failed";
  resolve: (requestID: string) => Promise<unknown>;
  resume?: (requestID: string) => Promise<unknown>;
}): Promise<void> {
  const outcomeKey = assistantOutcomeKey(input.assistantID, input.attentionID, input.resolution);
  await input.resolve(pendingAssistantRequest(outcomeKey));
  if (input.resolution === "retry_acknowledged" && input.runID && input.resume) {
    await runAssistantResume({
      assistantID: input.assistantID,
      attentionID: input.attentionID,
      runID: input.runID,
      resume: input.resume,
      completeKeys: [outcomeKey],
    });
  }
  completeAssistantRequest(outcomeKey);
}

export async function runAssistantApproval(input: {
  assistantID: string;
  attentionID: string;
  runID?: string;
  resolve: (requestID: string) => Promise<unknown>;
  resume: (requestID: string) => Promise<unknown>;
}): Promise<void> {
  const resolveKey = assistantIntentKey("attention-approve", input.assistantID, input.attentionID);
  await input.resolve(pendingAssistantRequest(resolveKey));
  if (input.runID) {
    await runAssistantResume({
      assistantID: input.assistantID,
      attentionID: input.attentionID,
      runID: input.runID,
      resume: input.resume,
    });
  }
  completeAssistantRequest(resolveKey);
}

export async function runAssistantRejection(input: {
  assistantID: string;
  attentionID: string;
  reject: (requestID: string) => Promise<unknown>;
}): Promise<void> {
  const key = assistantIntentKey("attention-reject", input.assistantID, input.attentionID);
  await input.reject(pendingAssistantRequest(key));
  completeAssistantRequest(key);
}
