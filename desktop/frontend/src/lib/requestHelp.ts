// requestHelp.ts — request_help tool status parser and reducer helpers.
//
// The backend emits structured progress chunks via tool_progress events
// prefixed with REQUEST_HELP_PROGRESS_PREFIX. This module parses those
// chunks, the tool args, and the final output/error into a stable status.

export const REQUEST_HELP_PROGRESS_PREFIX = "request_help_status:";
export const REQUEST_HELP_SUMMARY_PREFIX = "request_help_summary:";

export type RequestHelpCapability = "web_search" | "image_generation" | "";
export type RequestHelpPhase = "selecting" | "attempting" | "switching" | "completed" | "failed";

export interface RequestHelpStatus {
  phase: RequestHelpPhase;
  requestId?: string;
  capability: RequestHelpCapability;
  fromModel?: string;
  model?: string;
  attempt?: number;
  total?: number;
  error?: string;
}

interface RequestHelpWireStatus {
  version?: number;
  state?: string;
  request_id?: string;
  capability?: string;
  from_model?: string;
  model?: string;
  attempt?: number;
  total?: number;
  error?: string;
}

function capabilityOf(value: unknown): RequestHelpCapability {
  return value === "web_search" || value === "image_generation" ? value : "";
}

function positiveInt(value: unknown): number | undefined {
  return typeof value === "number" && Number.isInteger(value) && value > 0 ? value : undefined;
}

function wirePhase(value: string | undefined, attempt?: number, total?: number): RequestHelpPhase {
  switch (value) {
    case "completed": return "completed";
    case "candidate_failed": return attempt && total && attempt < total ? "switching" : "failed";
    case "failed": return "failed";
    default: return "attempting";
  }
}

function fromWire(current: RequestHelpStatus, wire: RequestHelpWireStatus): RequestHelpStatus {
  const attempt = positiveInt(wire.attempt) ?? current.attempt;
  const total = positiveInt(wire.total) ?? current.total;
  return {
    phase: wirePhase(wire.state, attempt, total),
    requestId: wire.request_id?.trim() || current.requestId,
    capability: capabilityOf(wire.capability) || current.capability,
    fromModel: wire.from_model?.trim() || current.fromModel,
    model: wire.model?.trim() || current.model,
    attempt,
    total,
    error: wire.error?.trim() || undefined,
  };
}

function parseWire(text: string, prefix: string): RequestHelpWireStatus[] {
  const out: RequestHelpWireStatus[] = [];
  for (const line of text.split(/\r?\n/)) {
    const at = line.indexOf(prefix);
    if (at < 0) continue;
    try {
      const value = JSON.parse(line.slice(at + prefix.length)) as RequestHelpWireStatus;
      if (value && typeof value === "object" && (value.version === undefined || value.version === 1)) out.push(value);
    } catch {
      // Progress can arrive split or truncated. A later complete chunk repairs it.
    }
  }
  return out;
}

export function requestHelpFromArgs(args: string): RequestHelpStatus {
  let capability: RequestHelpCapability = "";
  try {
    capability = capabilityOf((JSON.parse(args) as { capability?: unknown }).capability);
  } catch {
    // Partial streamed arguments are expected briefly.
  }
  return { phase: "selecting", capability };
}

export function applyRequestHelpProgress(current: RequestHelpStatus, chunk: string): RequestHelpStatus {
  return parseWire(chunk, REQUEST_HELP_PROGRESS_PREFIX).reduce(fromWire, current);
}

function finalWire(output: string): RequestHelpWireStatus | undefined {
  if (!output.includes("Capability assist succeeded")) return undefined;
  const fields = new Map<string, string>();
  for (const line of output.split(/\r?\n/)) {
    const match = /^([a-z_]+):\s*(.+)$/.exec(line.trim());
    if (match) fields.set(match[1], match[2]);
  }
  const attempt = /^(\d+)\/(\d+)$/.exec(fields.get("attempt") ?? "");
  return {
    version: 1,
    state: "completed",
    request_id: fields.get("request_id"),
    capability: fields.get("capability"),
    from_model: fields.get("from_model"),
    model: fields.get("model"),
    attempt: attempt ? Number(attempt[1]) : undefined,
    total: attempt ? Number(attempt[2]) : undefined,
  };
}

export function finishRequestHelp(current: RequestHelpStatus, output?: string, error?: string, summary?: string): RequestHelpStatus {
  let next = current;
  for (const wire of parseWire(summary ?? "", REQUEST_HELP_SUMMARY_PREFIX)) next = fromWire(next, wire);
  const final = finalWire(output ?? "");
  if (final) next = fromWire(next, final);
  if (error) return { ...next, phase: "failed", error };
  return next.phase === "completed" ? next : { ...next, phase: "completed" };
}

export function requestHelpFromHistory(args: string, output?: string, error?: string, summary?: string): RequestHelpStatus {
  return finishRequestHelp(requestHelpFromArgs(args), output, error, summary);
}
