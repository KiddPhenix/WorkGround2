// Bounded, text-only serialization shared by fallback copy and render identity.
// It never invokes accessors or toJSON.

const MAX_DEPTH = 16;
const MAX_NODES = 8_192;
const MAX_PROPERTIES = 2_048;
const MAX_STRING_CHARS = 512;
const MAX_KEY_CHARS = 128;
const MAX_SCAN_BYTES = 48 * 1024;
const MAX_OUTPUT_BYTES = 64 * 1024;

const SECRET_PARTS = new Set([
  'auth', 'authorization', 'bearer', 'cookie', 'credential', 'key', 'password',
  'secret', 'session', 'token',
]);
const BEARER_VALUE = /\bbearer\s+[^\s,;]+/gi;

export interface SafeJsonMeta {
  redacted: boolean;
  truncated: boolean;
  redactedCount: number;
  nodes: number;
  properties: number;
  reasons: string[];
}

export interface SafeJsonEnvelope {
  meta: SafeJsonMeta;
  value: unknown;
}

interface Budget {
  bytes: number;
  nodes: number;
  properties: number;
  redactedCount: number;
  reasons: Set<string>;
}

const encoder = new TextEncoder();
const objectIDs = new WeakMap<object, number>();
let nextObjectID = 1;

function byteLength(value: string): number {
  return encoder.encode(value).byteLength;
}

function mark(budget: Budget, reason: string): void {
  budget.reasons.add(reason);
}

function takeChars(value: string, maxChars: number, budget: Budget): string {
  const clean = value.replace(/[\u0000-\u001f\u007f]/g, ' ');
  const chars = Array.from(clean);
  let selected = chars.slice(0, maxChars).join('');
  if (chars.length > maxChars) mark(budget, 'string_chars');

  const remaining = Math.max(0, MAX_SCAN_BYTES - budget.bytes);
  while (selected && byteLength(selected) > remaining) {
    selected = Array.from(selected).slice(0, Math.floor(Array.from(selected).length * 0.75)).join('');
    mark(budget, 'scan_bytes');
  }
  budget.bytes += byteLength(selected);
  return selected;
}

export function normalizeSecretKey(key: string): string[] {
  return key
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean);
}

export function isSecretKey(key: string): boolean {
  return normalizeSecretKey(key).some((part) => SECRET_PARTS.has(part));
}

function redactString(value: string, budget: Budget): string {
  let redacted = false;
  const result = value.replace(BEARER_VALUE, () => {
    redacted = true;
    return 'Bearer [redacted]';
  });
  if (redacted) {
    budget.redactedCount += 1;
    mark(budget, 'secret_value');
  }
  return takeChars(result, MAX_STRING_CHARS, budget);
}

function safeValue(input: unknown, budget: Budget): unknown {
  const active = new WeakSet<object>();
  const seen = new WeakSet<object>();

  const walk = (value: unknown, depth: number): unknown => {
    budget.nodes += 1;
    if (budget.nodes > MAX_NODES) {
      mark(budget, 'node_budget');
      return '[node budget exhausted]';
    }
    if (depth > MAX_DEPTH) {
      mark(budget, 'depth_budget');
      return '[depth budget exhausted]';
    }
    if (budget.bytes >= MAX_SCAN_BYTES) {
      mark(budget, 'scan_bytes');
      return '[byte budget exhausted]';
    }

    if (value === null) return null;
    if (value === undefined) {
      mark(budget, 'undefined_value');
      return '[undefined]';
    }
    if (typeof value === 'string') return redactString(value, budget);
    if (typeof value === 'number') return Number.isFinite(value) ? value : `[${String(value)}]`;
    if (typeof value === 'boolean') return value;
    if (typeof value === 'bigint') return `[BigInt ${takeChars(String(value), 40, budget)}]`;
    if (typeof value === 'function') {
      mark(budget, 'function_value');
      return '[function omitted]';
    }
    if (typeof value === 'symbol') {
      mark(budget, 'symbol_value');
      return '[symbol omitted]';
    }
    if (typeof value !== 'object') return takeChars(String(value), MAX_STRING_CHARS, budget);
    if (active.has(value)) {
      mark(budget, 'circular_reference');
      return '[circular]';
    }
    if (seen.has(value)) {
      mark(budget, 'shared_reference');
      return '[shared reference]';
    }

    seen.add(value);
    active.add(value);
    try {
      let descriptors: PropertyDescriptorMap;
      try {
        descriptors = Object.getOwnPropertyDescriptors(value);
      } catch {
        mark(budget, 'descriptor_error');
        return '[object unavailable]';
      }

      if (Array.isArray(value)) {
        const rawLength = descriptors.length && 'value' in descriptors.length
          ? descriptors.length.value
          : 0;
        const length = Number.isSafeInteger(rawLength) && rawLength >= 0 ? rawLength : 0;
        const result: unknown[] = [];
        for (let index = 0; index < length; index++) {
          if (budget.properties >= MAX_PROPERTIES || budget.bytes >= MAX_SCAN_BYTES) {
            mark(budget, budget.properties >= MAX_PROPERTIES ? 'property_budget' : 'scan_bytes');
            result.push(`[${length - index} items omitted]`);
            break;
          }
          budget.properties += 1;
          const descriptor = descriptors[String(index)];
          if (descriptor && 'value' in descriptor) {
            result.push(walk(descriptor.value, depth + 1));
          } else {
            mark(budget, 'accessor_value');
            result.push('[hole or accessor omitted]');
          }
        }
        return result;
      }

      const result: Record<string, unknown> = Object.create(null) as Record<string, unknown>;
      const keys = Object.keys(descriptors);
      for (let index = 0; index < keys.length; index++) {
        if (budget.properties >= MAX_PROPERTIES || budget.bytes >= MAX_SCAN_BYTES) {
          mark(budget, budget.properties >= MAX_PROPERTIES ? 'property_budget' : 'scan_bytes');
          result['$omitted'] = `${keys.length - index} properties omitted`;
          break;
        }
        budget.properties += 1;
        const rawKey = keys[index];
        const key = takeChars(rawKey, MAX_KEY_CHARS, budget) || '[empty key]';
        if (rawKey.length > MAX_KEY_CHARS) mark(budget, 'key_chars');
        if (isSecretKey(rawKey)) {
          result[key] = '[redacted]';
          budget.redactedCount += 1;
          mark(budget, 'secret_key');
          continue;
        }
        const descriptor = descriptors[rawKey];
        if (descriptor && 'value' in descriptor) {
          result[key] = walk(descriptor.value, depth + 1);
        } else {
          mark(budget, 'accessor_value');
          result[key] = '[accessor omitted]';
        }
      }
      return result;
    } finally {
      active.delete(value);
    }
  };

  return walk(input, 0);
}

export function buildSafeJson(value: unknown): string {
  const budget: Budget = {
    bytes: 0,
    nodes: 0,
    properties: 0,
    redactedCount: 0,
    reasons: new Set(),
  };
  let safe: unknown;
  try {
    safe = safeValue(value, budget);
  } catch {
    mark(budget, 'serialization_error');
    safe = '[serialization failed]';
  }

  const envelope = (): SafeJsonEnvelope => ({
    meta: {
      redacted: budget.redactedCount > 0,
      truncated: budget.reasons.size > 0,
      redactedCount: budget.redactedCount,
      nodes: Math.min(budget.nodes, MAX_NODES),
      properties: Math.min(budget.properties, MAX_PROPERTIES),
      reasons: [...budget.reasons].sort(),
    },
    value: safe,
  });
  let output = JSON.stringify(envelope(), null, 2);
  if (byteLength(output) <= MAX_OUTPUT_BYTES) return output;

  mark(budget, 'output_bytes');
  safe = '[payload omitted by output budget]';
  output = JSON.stringify(envelope(), null, 2);
  return output;
}

function hashText(value: string): string {
  let hash = 0x811c9dc5;
  const bytes = encoder.encode(value);
  for (const byte of bytes) {
    hash ^= byte;
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16).padStart(8, '0');
}

function objectID(value: unknown): string {
  if (!value || (typeof value !== 'object' && typeof value !== 'function')) return typeof value;
  const object = value as object;
  let id = objectIDs.get(object);
  if (!id) {
    id = nextObjectID++;
    objectIDs.set(object, id);
  }
  return String(id);
}

export function dataIdentity(value: unknown): string {
  return `${objectID(value)}:${hashText(buildSafeJson(value))}`;
}

export function blockRenderIdentity(block: {
  id: unknown;
  kind: unknown;
  schemaVersion: unknown;
  revision: unknown;
  digest?: unknown;
  contentDigest?: unknown;
  status: unknown;
  tombstone?: unknown;
  data: unknown;
  source?: { ref?: unknown };
}): string {
  const fields = buildSafeJson({
    blockID: block.id,
    kind: block.kind,
    schemaVersion: block.schemaVersion,
    revision: block.revision,
    digest: block.digest ?? block.contentDigest ?? block.source?.ref ?? null,
    status: block.status,
    tombstone: Boolean(block.tombstone),
    data: dataIdentity(block.data),
  });
  return `block:${hashText(fields)}`;
}
