import type { InputSpec, InputKind } from '../../../types_v2';
import type { ArtifactRef } from '../../../types';

// ── Local types (mirror Go) ─────────────────────────────────────────────────

/** Form field definition inside a form value schema. */
export interface FormFieldSpec {
  id: string;
  label: string;
  kind: InputKind;
  required: boolean;
  valueSchema?: unknown;
}

// ── Parsed value schema (mirrors Go input_value_schema.go) ──────────────────

export interface ParsedTextConstraints {
  minLength?: number;
  maxLength?: number;
  pattern?: string;
  multiline?: boolean;
}

export interface ParsedNumberConstraints {
  min?: number;
  max?: number;
  integer?: boolean;
  unit?: string;
  currency?: string;
}

export interface ParsedDateConstraints {
  minDate?: string;
  maxDate?: string;
  mode?: string;
}

export interface ChoiceOption {
  value: string;
  label: string;
}

export interface ParsedChoiceConstraints {
  options: ChoiceOption[];
  allowOther?: boolean;
}

export interface ParsedMultiChoiceConstraints {
  options: ChoiceOption[];
  minSelect?: number;
  maxSelect?: number;
  allowOther?: boolean;
}

export interface ParsedRosterConstraints {
  minEntries?: number;
  maxEntries?: number;
  fields: string[];
}

export interface ParsedFormConstraints {
  fields: FormFieldSpec[];
}

export interface ParsedFileConstraints {
  acceptTypes?: string[];
  maxCount?: number;
}

export interface ParsedApprovalConstraints {
  riskLevel?: string;
  description?: string;
}

export interface ParsedValueSchema {
  kind: InputKind;
  text?: ParsedTextConstraints;
  number?: ParsedNumberConstraints;
  date?: ParsedDateConstraints;
  choice?: ParsedChoiceConstraints;
  multiChoice?: ParsedMultiChoiceConstraints;
  roster?: ParsedRosterConstraints;
  form?: ParsedFormConstraints;
  file?: ParsedFileConstraints;
  approval?: ParsedApprovalConstraints;
}

export type DraftValue =
  | string
  | number
  | boolean
  | string[]
  | ArtifactRef[]
  | Array<string | ArtifactRef>
  | Record<string, unknown>[]
  | Record<string, unknown>
  | null
  | undefined
  | { start: string; end: string };

// ── Schema errors (thrown for invalid configuration) ────────────────────────

export class SchemaParseError extends Error {
  constructor(
    message: string,
    public readonly specId: string,
    public readonly kind: InputKind,
    public readonly raw: unknown,
  ) {
    super(`[${specId}/${kind}] ${message}`);
    this.name = 'SchemaParseError';
  }
}

// ── Parser (throws on invalid config, returns empty for no schema) ──────────

export function parseValueSchema(
  specId: string,
  kind: InputKind,
  raw: unknown,
): ParsedValueSchema {
  const result: ParsedValueSchema = { kind };

  if (raw == null) return result;
  if (typeof raw === 'string' && raw.trim() === '') return result;

  let obj: Record<string, unknown>;
  if (typeof raw === 'string') {
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch (e) {
      throw new SchemaParseError(
        `valueSchema JSON 解析失败: ${e instanceof Error ? e.message : String(e)}`,
        specId, kind, raw,
      );
    }
    if (parsed == null) return result;
    if (typeof parsed !== 'object' || Array.isArray(parsed)) {
      throw new SchemaParseError(
        `valueSchema 必须是 JSON 对象, 实际为 ${typeof parsed}`,
        specId, kind, raw,
      );
    }
    obj = parsed as Record<string, unknown>;
  } else if (typeof raw === 'object' && !Array.isArray(raw)) {
    obj = raw as Record<string, unknown>;
  } else {
    throw new SchemaParseError(
      `valueSchema 类型无效: ${typeof raw}`,
      specId, kind, raw,
    );
  }

  if (obj.kind && obj.kind !== kind) {
    throw new SchemaParseError(
      `valueSchema.kind "${obj.kind}" 与 spec.kind "${kind}" 不匹配`,
      specId, kind, raw,
    );
  }

  switch (kind) {
    case 'text':
      result.text = {
        minLength: asPosInt(obj.minLength, specId, 'minLength'),
        maxLength: asPosInt(obj.maxLength, specId, 'maxLength'),
        pattern: asValidPattern(obj.pattern, specId),
        multiline: asOptionalBool(obj.multiline, specId, 'multiline'),
      };
      break;
    case 'number': {
      const unit = asString(obj.unit);
      if (unit && !['number', 'amount', 'ratio', 'percent'].includes(unit)) {
        throw new SchemaParseError(
          `非法 number unit "${unit}"`, specId, kind, raw,
        );
      }
      const currency = asString(obj.currency);
      if (currency && !/^[A-Z]{3}$/.test(currency)) {
        throw new SchemaParseError(
          `currency 必须是 ISO 三字母代码, 实际为 "${currency}"`, specId, kind, raw,
        );
      }
      if (currency && unit !== 'amount') {
        throw new SchemaParseError(
          `currency 必须搭配 unit=amount, 实际 unit="${unit}"`, specId, kind, raw,
        );
      }
      result.number = {
        min: asNumber(obj.min),
        max: asNumber(obj.max),
        integer: obj.integer === true,
        unit: unit || undefined,
        currency: currency || undefined,
      };
      break;
    }
    case 'date':
      result.date = {
        minDate: asString(obj.minDate) || undefined,
        maxDate: asString(obj.maxDate) || undefined,
        mode: validateDateMode(asString(obj.mode), specId, kind, raw),
      };
      break;
    case 'choice':
      result.choice = {
        options: asOptions(obj.options, specId),
        allowOther: obj.allowOther === true,
      };
      break;
    case 'multi_choice':
      result.multiChoice = {
        options: asOptions(obj.options, specId),
        minSelect: asPosInt(obj.minSelect, specId, 'minSelect'),
        maxSelect: asPosInt(obj.maxSelect, specId, 'maxSelect'),
        allowOther: obj.allowOther === true,
      };
      break;
    case 'roster':
      result.roster = {
        minEntries: asPosInt(obj.minEntries, specId, 'minEntries'),
        maxEntries: asPosInt(obj.maxEntries, specId, 'maxEntries'),
        fields: asStringArray(obj.fields),
      };
      break;
    case 'form':
      result.form = {
        fields: asFormFields(obj.fields, specId),
      };
      break;
    case 'file':
      result.file = {
        acceptTypes: asStringArray(obj.acceptTypes),
        maxCount: asPosInt(obj.maxCount, specId, 'maxCount') ?? undefined,
      };
      break;
    case 'approval': {
      const risk = asString(obj.riskLevel);
      if (risk && !['low', 'medium', 'high', 'critical'].includes(risk)) {
        throw new SchemaParseError(
          `非法 approval riskLevel "${risk}"`, specId, kind, raw,
        );
      }
      result.approval = {
        riskLevel: risk || undefined,
        description: asString(obj.description) || undefined,
      };
      break;
    }
  }

  return result;
}

// ── Validation ──────────────────────────────────────────────────────────────

export function validateDraft(
  spec: InputSpec,
  value: DraftValue,
  schema: ParsedValueSchema,
): string | null {
  const isNullish = value == null || value === '';
  const isEmptyArr = Array.isArray(value) && value.length === 0;
  const isEmptyObj = !Array.isArray(value) && typeof value === 'object' && value !== null && Object.keys(value).length === 0;
  const isEmpty = isNullish || isEmptyArr || isEmptyObj;

  if (spec.required && isEmpty) {
    return `${spec.label} 是必填项`;
  }
  if (isEmpty) return null;

  switch (spec.kind) {
    case 'text': return validateText(spec.label, value, schema.text);
    case 'number': return validateNumber(spec.label, value, schema.number);
    case 'date': return validateDate(spec.label, value, schema.date);
    case 'choice': return validateChoice(spec.label, value, schema.choice);
    case 'multi_choice': return validateMultiChoice(spec.label, value, schema.multiChoice);
    case 'roster': return validateRoster(spec.label, value, schema.roster);
    case 'form': return validateForm(spec.label, value, schema.form);
    case 'file': return validateFile(spec.label, value, schema.file);
    case 'approval': return validateApproval(spec.label, value, schema.approval);
    default: return `未知输入类型: ${spec.kind}`;
  }
}

/** Validate a single form field value against its sub-spec. */
export function validateFormField(
  field: FormFieldSpec,
  value: unknown,
): string | null {
  const spec: InputSpec = {
    id: field.id,
    label: field.label,
    kind: field.kind,
    required: field.required,
    valueSchema: field.valueSchema,
    pinEligible: false,
  };
  // Parse field-level schema, catching errors
  let fSchema: ParsedValueSchema;
  try {
    fSchema = parseValueSchema(field.id, field.kind, field.valueSchema);
  } catch (e) {
    return e instanceof SchemaParseError ? e.message : String(e);
  }
  return validateDraft(spec, value as DraftValue, fSchema);
}

// ── Wire value conversion ──────────────────────────────────────────────────

/**
 * Converts a UI draft value to wire format for SubmitWorkInputRequest.
 * Dates go as ISO strings matching Go's RFC3339 / ISO 8601 contract.
 */
export function toWireValue(kind: InputKind, draft: DraftValue, schema: ParsedValueSchema): unknown {
  if (draft == null || draft === '') return null;

  switch (kind) {
    case 'number':
      return typeof draft === 'number' ? draft : Number(draft);

    case 'date': {
      const mode = schema.date?.mode;
      if (mode === 'range') {
        const range = draft as { start: string; end: string };
        return { start: toRFC3339(range.start, 'date'), end: toRFC3339(range.end, 'date') };
      }
      if (typeof draft === 'string') return toRFC3339(draft, mode ?? 'date');
      return draft;
    }

    case 'approval':
      if (typeof draft === 'string') return draft.toLowerCase();
      return draft;

    case 'file': {
      const values = Array.isArray(draft) ? draft : [draft];
      return values.map((item) => {
        if (typeof item === 'string') return item;
        if (item && typeof item === 'object' && 'id' in item && typeof item.id === 'string') {
          return item.id;
        }
        return '';
      }).filter(Boolean);
    }

    case 'form': {
      if (!draft || typeof draft !== 'object' || Array.isArray(draft)) return draft;
      const obj = draft as Record<string, unknown>;
      const out: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(obj)) {
        out[k] = v === '' ? null : v;
      }
      return out;
    }

    default:
      return draft;
  }
}

/** Convert a UI date string to RFC3339 for wire transport. */
export function toRFC3339(value: string, mode: string): string {
  if (!value) return '';
  switch (mode) {
    case 'date':
      // "2026-07-24" → "2026-07-24T00:00:00Z"
      if (/^\d{4}-\d{2}-\d{2}$/.test(value)) return `${value}T00:00:00Z`;
      return value;
    case 'time':
      // "15:04" or "15:04:05" → "1970-01-01T15:04:05Z"
      if (/^\d{2}:\d{2}$/.test(value)) return `1970-01-01T${value}:00Z`;
      if (/^\d{2}:\d{2}:\d{2}$/.test(value)) return `1970-01-01T${value}Z`;
      return value;
    case 'datetime':
      // "2026-07-24T15:04" → "2026-07-24T15:04:00Z"
      if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return `${value}:00Z`;
      return value;
    default:
      return value;
  }
}

// ── Kind-specific validators ────────────────────────────────────────────────

function validateText(label: string, value: DraftValue, c: ParsedTextConstraints | undefined): string | null {
  if (typeof value !== 'string') return `${label} 必须是文本`;
  if (!c) return null;
  const runes = [...value];
  if (c.minLength && runes.length < c.minLength) {
    return `${label} 至少需要 ${c.minLength} 个字符（当前 ${runes.length}）`;
  }
  if (c.maxLength && runes.length > c.maxLength) {
    return `${label} 最多允许 ${c.maxLength} 个字符（当前 ${runes.length}）`;
  }
  if (c.pattern) {
    try {
      if (!new RegExp(c.pattern).test(value)) {
        return `${label} 格式不匹配: ${c.pattern}`;
      }
    } catch {
      return `${label} 验证模式无效: ${c.pattern}`;
    }
  }
  return null;
}

function validateNumber(label: string, value: DraftValue, c: ParsedNumberConstraints | undefined): string | null {
  const n = typeof value === 'number' ? value : Number(value);
  if (Number.isNaN(n)) return `${label} 必须是数字`;
  if (!Number.isFinite(n)) return `${label} 必须是有限数字`;
  if (!c) return null;
  if (c.min !== undefined && n < c.min) return `${label} 不能小于 ${c.min}`;
  if (c.max !== undefined && n > c.max) return `${label} 不能大于 ${c.max}`;
  if (c.integer && !Number.isInteger(n)) return `${label} 必须是整数`;
  if (c.unit === 'ratio' && (n < 0 || n > 1)) return `${label} 比例必须在 0~1 之间`;
  if (c.unit === 'percent' && (n < 0 || n > 100)) return `${label} 百分比必须在 0~100 之间`;
  return null;
}

function validateDate(label: string, value: DraftValue, c: ParsedDateConstraints | undefined): string | null {
  if (c?.mode === 'range') {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) {
      return `${label} 必须是日期范围对象`;
    }
    const range = value as { start: string; end: string };
    if (!range.start || !range.end) return `${label} 需要填写开始和结束日期`;
    if (range.start > range.end) return `${label} 开始日期不能晚于结束日期`;
    return null;
  }
  if (typeof value !== 'string' || value.trim() === '') return `${label} 必须是日期字符串`;
  if (c?.mode === 'time' && !/^\d{2}:\d{2}(:\d{2})?$/.test(value)) {
    return `${label} 时间格式应为 HH:MM 或 HH:MM:SS`;
  }
  return null;
}

function validateChoice(label: string, value: DraftValue, c: ParsedChoiceConstraints | undefined): string | null {
  if (typeof value !== 'string') return `${label} 必须是文本值`;
  if (!c || c.options.length === 0) return null;
  if (c.options.some((o) => o.value === value)) return null;
  if (c.allowOther) return null;
  return `${label} 的值 "${value}" 不在允许选项内`;
}

function validateMultiChoice(label: string, value: DraftValue, c: ParsedMultiChoiceConstraints | undefined): string | null {
  if (!Array.isArray(value)) return `${label} 必须是字符串数组`;
  const arr = value as string[];
  if (!c) return null;
  if (c.minSelect && arr.length < c.minSelect) {
    return `${label} 至少选择 ${c.minSelect} 项（当前 ${arr.length}）`;
  }
  if (c.maxSelect && arr.length > c.maxSelect) {
    return `${label} 最多选择 ${c.maxSelect} 项（当前 ${arr.length}）`;
  }
  if (!c.allowOther && c.options.length > 0) {
    const optSet = new Set(c.options.map((o) => o.value));
    for (const sel of arr) {
      if (!optSet.has(sel)) return `${label} 的值 "${sel}" 不在允许选项内`;
    }
  }
  return null;
}

function validateRoster(label: string, value: DraftValue, c: ParsedRosterConstraints | undefined): string | null {
  if (!Array.isArray(value)) return `${label} 必须是对象数组`;
  if (!c) return null;
  if (c.minEntries && value.length < c.minEntries) {
    return `${label} 至少需要 ${c.minEntries} 个条目（当前 ${value.length}）`;
  }
  if (c.maxEntries && value.length > c.maxEntries) {
    return `${label} 最多允许 ${c.maxEntries} 个条目（当前 ${value.length}）`;
  }
  for (let i = 0; i < value.length; i++) {
    const entry = value[i];
    if (typeof entry !== 'object' || entry === null || Array.isArray(entry)) {
      return `${label} 条目 #${i + 1} 必须是对象`;
    }
    for (const field of c.fields) {
      const fv = (entry as Record<string, unknown>)[field];
      if (fv == null || (typeof fv === 'string' && fv.trim() === '')) {
        return `${label} 条目 #${i + 1} 缺少字段 ${field}`;
      }
    }
  }
  return null;
}

function validateForm(label: string, value: DraftValue, c: ParsedFormConstraints | undefined): string | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return `${label} 必须是对象`;
  }
  if (!c) return null;
  const obj = value as Record<string, unknown>;
  for (const field of c.fields) {
    const fv = obj[field.id];
    const isEmptyFv = fv == null || fv === '';
    if (field.required && isEmptyFv) {
      return `${label} 缺少必填字段: ${field.label}`;
    }
    if (!isEmptyFv) {
      const fErr = validateFormField(field, fv);
      if (fErr) return fErr;
    }
  }
  return null;
}

function validateFile(label: string, value: DraftValue, c: ParsedFileConstraints | undefined): string | null {
  const refs: Array<string | ArtifactRef> = Array.isArray(value)
    ? (value as unknown[]).filter((item): item is string | ArtifactRef =>
        typeof item === 'string'
        || (!!item && typeof item === 'object' && typeof (item as { id?: unknown }).id === 'string'))
    : typeof value === 'string'
      ? [value]
      : [];
  if (refs.length === 0) return `${label} 需要选择文件`;
  if (c?.maxCount && refs.length > c.maxCount) {
    return `${label} 最多选择 ${c.maxCount} 个文件（当前 ${refs.length}）`;
  }
  return null;
}

function validateApproval(label: string, value: DraftValue, _c: ParsedApprovalConstraints | undefined): string | null {
  if (typeof value !== 'string') return `${label} 必须是 "approved" 或 "rejected"`;
  const v = value.toLowerCase();
  if (v !== 'approved' && v !== 'rejected') {
    return `${label} 必须是 "approved" 或 "rejected"，实际为 "${value}"`;
  }
  return null;
}

// ── Label helpers ───────────────────────────────────────────────────────────

const KIND_LABELS: Record<InputKind, string> = {
  text: '文本', number: '数字', date: '日期', choice: '选择',
  multi_choice: '多选', file: '文件', roster: '名单', form: '表单', approval: '批准',
};

export function kindLabel(kind: InputKind): string {
  return KIND_LABELS[kind] ?? kind;
}

// ── JSON helpers ────────────────────────────────────────────────────────────

function asNumber(v: unknown): number | undefined {
  if (typeof v === 'number' && Number.isFinite(v)) return v;
  if (typeof v === 'string') { const n = Number(v); return Number.isFinite(n) ? n : undefined; }
  return undefined;
}

function asPosInt(v: unknown, specId: string, field: string): number | undefined {
  const n = asNumber(v);
  if (n !== undefined && (!Number.isInteger(n) || n < 0)) {
    throw new SchemaParseError(`${field} 必须是非负整数, 实际为 ${String(v)}`, specId, 'text', v);
  }
  return n;
}

function asString(v: unknown): string | undefined {
  return typeof v === 'string' ? v : undefined;
}

function asOptionalBool(v: unknown, specId: string, field: string): boolean | undefined {
  if (v === undefined) return undefined;
  if (typeof v !== 'boolean') {
    throw new SchemaParseError(`${field} 必须是布尔值, 实际为 ${String(v)}`, specId, 'text', v);
  }
  return v;
}

function asStringArray(v: unknown): string[] {
  if (Array.isArray(v)) return v.filter((x): x is string => typeof x === 'string');
  return [];
}

function asOptions(v: unknown, specId: string): ChoiceOption[] {
  if (!Array.isArray(v)) {
    throw new SchemaParseError(`options 必须是数组`, specId, 'choice', v);
  }
  const out: ChoiceOption[] = [];
  for (const item of v) {
    if (typeof item === 'object' && item !== null && typeof (item as ChoiceOption).value === 'string') {
      out.push({ value: (item as ChoiceOption).value, label: (item as ChoiceOption).label ?? (item as ChoiceOption).value });
    }
  }
  return out;
}

function asFormFields(v: unknown, specId: string): FormFieldSpec[] {
  if (!Array.isArray(v)) {
    throw new SchemaParseError(`form fields 必须是数组`, specId, 'form', v);
  }
  return v.filter((x): x is FormFieldSpec =>
    typeof x === 'object' && x !== null && typeof (x as FormFieldSpec).id === 'string',
  );
}

function validateDateMode(mode: string | undefined, specId: string, kind: InputKind, raw: unknown): string | undefined {
  if (!mode) return undefined;
  if (!['date', 'time', 'datetime', 'range'].includes(mode)) {
    throw new SchemaParseError(`非法 date mode "${mode}"`, specId, kind, raw);
  }
  return mode;
}

function asValidPattern(v: unknown, specId: string): string | undefined {
  const s = asString(v);
  if (!s) return undefined;
  try {
    new RegExp(s);
  } catch (e) {
    throw new SchemaParseError(
      `非法正则 pattern: ${e instanceof Error ? e.message : String(e)}`, specId, 'text', v,
    );
  }
  return s;
}
