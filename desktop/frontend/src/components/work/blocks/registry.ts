// Code-owned renderer registry. Block payloads can select a registered kind and
// schema version, but can never provide a component or module path.

import type {
  BlockRendererRegistry,
  LazyLoader,
  RendererModule,
  RendererSupport,
  RendererValidator,
  SchemaRange,
  SchemaVersionSpec,
  ValidationResult,
} from './types';

interface RegistryEntry {
  range: SchemaRange;
  validate: RendererValidator;
  load: LazyLoader;
  module: RendererModule | null;
  inflight: Promise<RendererModule> | null;
}

function isSchemaVersion(value: number): boolean {
  return Number.isSafeInteger(value) && value > 0;
}

function normalizeRange(spec: SchemaVersionSpec): SchemaRange {
  if (typeof spec === 'number') {
    if (!isSchemaVersion(spec)) throw new TypeError('schema version must be a positive safe integer');
    return { min: spec, max: spec };
  }
  if (!spec || typeof spec !== 'object' || !isSchemaVersion(spec.min) || !isSchemaVersion(spec.max) || spec.min > spec.max) {
    throw new TypeError('schema range must contain positive safe integers with min <= max');
  }
  return { min: spec.min, max: spec.max };
}

function overlaps(left: SchemaRange, right: SchemaRange): boolean {
  return left.min <= right.max && right.min <= left.max;
}

function isReactComponent(value: unknown): boolean {
  if (typeof value === 'function') return true;
  // React.memo and React.forwardRef are objects tagged with a private symbol.
  return Boolean(value && typeof value === 'object' && '$$typeof' in value);
}

class Registry implements BlockRendererRegistry {
  private readonly entries = new Map<string, RegistryEntry[]>();

  register(
    kind: string,
    versions: SchemaVersionSpec,
    validate: RendererValidator,
    load: LazyLoader,
  ): void {
    if (typeof kind !== 'string' || kind.length === 0 || kind !== kind.trim()) {
      throw new TypeError('renderer kind must be a non-empty, trimmed string');
    }
    const range = normalizeRange(versions);
    if (typeof validate !== 'function') throw new TypeError(`renderer validator is required for kind "${kind}"`);
    if (typeof load !== 'function') throw new TypeError(`renderer loader is required for kind "${kind}"`);

    const entries = this.entries.get(kind) ?? [];
    for (const existing of entries) {
      if (!overlaps(existing.range, range)) continue;
      const exact = existing.range.min === range.min && existing.range.max === range.max;
      if (exact && existing.validate === validate && existing.load === load) return;
      throw new Error(
        `conflicting renderer registration for kind "${kind}" and schema range ` +
          `[${range.min}, ${range.max}]`,
      );
    }

    entries.push({ range, validate, load, module: null, inflight: null });
    entries.sort((left, right) => left.range.min - right.range.min);
    this.entries.set(kind, entries);
  }

  support(kind: string, schemaVersion: number): RendererSupport {
    if (!isSchemaVersion(schemaVersion)) return { status: 'unsupported_schema' };
    const entries = this.entries.get(kind);
    if (!entries?.length) return { status: 'unknown_kind' };
    if (entries.some((entry) => this.matches(entry, schemaVersion))) return { status: 'supported' };
    const maxSupported = entries[entries.length - 1].range.max;
    if (schemaVersion > maxSupported) return { status: 'future_schema', maxSupported };
    return { status: 'unsupported_schema' };
  }

  validate(kind: string, schemaVersion: number, data: unknown): ValidationResult | null {
    const entry = this.find(kind, schemaVersion);
    return entry ? entry.validate(schemaVersion, data) : null;
  }

  async resolve(kind: string, schemaVersion: number): Promise<RendererModule | null> {
    const entry = this.find(kind, schemaVersion);
    if (!entry) return null;
    if (entry.module) return entry.module;
    if (entry.inflight) return entry.inflight;

    entry.inflight = Promise.resolve()
      .then(() => entry.load())
      .then((module) => {
        if (!module || typeof module !== 'object' || !isReactComponent(module.component)) {
          throw new TypeError(`renderer loader for kind "${kind}" returned an invalid module`);
        }
        entry.module = module;
        return module;
      })
      .finally(() => {
        entry.inflight = null;
      });
    return entry.inflight;
  }

  has(kind: string, schemaVersion: number): boolean {
    return Boolean(this.find(kind, schemaVersion));
  }

  private find(kind: string, schemaVersion: number): RegistryEntry | undefined {
    if (!isSchemaVersion(schemaVersion)) return undefined;
    return this.entries.get(kind)?.find((entry) => this.matches(entry, schemaVersion));
  }

  private matches(entry: RegistryEntry, schemaVersion: number): boolean {
    return schemaVersion >= entry.range.min && schemaVersion <= entry.range.max;
  }
}

export function createRegistry(): BlockRendererRegistry {
  return new Registry();
}

export const blockRegistry = createRegistry();
