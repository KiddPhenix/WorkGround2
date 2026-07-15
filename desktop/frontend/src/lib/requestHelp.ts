// requestHelp.ts — request_help tool status parser and reducer helpers.
//
// The backend emits structured progress chunks via tool_progress events
// prefixed with REQUEST_HELP_PROGRESS_PREFIX. This module parses those
// chunks, the tool args, and the final output/error into a stable status.

export const REQUEST_HELP_PROGRESS_PREFIX = "request_help_status:";
export const REQUEST_HELP_SUMMARY_PREFIX = "request_help_summary:";

export type RequestHelpCapability = "web_search" | "image_generation" | "";
export type RequestHelpPhase = "selecting" | "attempting" | "switching" | "completed" | "failed";

export interface ImageArtifact {
  task_id: string;
  path: string;
  mime: string;
  size: number;
  width?: number;
  height?: number;
}

function imageArtifactOf(value: unknown): ImageArtifact | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const artifact = value as Partial<ImageArtifact>;
  const optionalDimension = (dimension: unknown) => dimension === undefined || (typeof dimension === "number" && Number.isInteger(dimension) && dimension > 0);
  if (
    typeof artifact.task_id !== "string" || artifact.task_id.trim() === "" ||
    typeof artifact.path !== "string" || artifact.path.trim() === "" ||
    typeof artifact.mime !== "string" || !artifact.mime.toLowerCase().startsWith("image/") ||
    typeof artifact.size !== "number" || !Number.isSafeInteger(artifact.size) || artifact.size <= 0 ||
    !optionalDimension(artifact.width) || !optionalDimension(artifact.height)
  ) return undefined;
  return artifact as ImageArtifact;
}

export interface RequestHelpStatus {
  phase: RequestHelpPhase;
  requestId?: string;
  capability: RequestHelpCapability;
  fromModel?: string;
  model?: string;
  attempt?: number;
  total?: number;
  error?: string;
  artifact?: ImageArtifact;
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
  // artifact is a raw image artifact object from the Go side;
  // it uses snake_case to match the JSON wire format directly.
  artifact?: ImageArtifact;
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
  // Only accept a wire artifact when it looks valid; never overwrite a
  // known artifact with undefined (later/duplicate events must not clear it).
  const artifact = imageArtifactOf(wire.artifact) ?? current.artifact;
  return {
    phase: wirePhase(wire.state, attempt, total),
    requestId: wire.request_id?.trim() || current.requestId,
    capability: capabilityOf(wire.capability) || current.capability,
    fromModel: wire.from_model?.trim() || current.fromModel,
    model: wire.model?.trim() || current.model,
    attempt,
    total,
    error: wire.error?.trim() || undefined,
    artifact,
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
  const wire: RequestHelpWireStatus = {
    version: 1,
    state: "completed",
    request_id: fields.get("request_id"),
    capability: fields.get("capability"),
    from_model: fields.get("from_model"),
    model: fields.get("model"),
    attempt: attempt ? Number(attempt[1]) : undefined,
    total: attempt ? Number(attempt[2]) : undefined,
  };
  // Parse artifact line (if present) — tolerate any corruption.
  const rawArtifact = fields.get("artifact");
  if (rawArtifact) {
    try {
      wire.artifact = imageArtifactOf(JSON.parse(rawArtifact));
    } catch {
      // Corrupt artifact JSON — ignore, normal helper output still works.
    }
  }
  return wire;
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
