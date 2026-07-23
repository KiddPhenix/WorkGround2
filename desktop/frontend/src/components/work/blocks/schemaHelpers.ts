// Shared validation helpers for WorkBlock renderers.
// Small, typed, single-responsibility — no business logic escapes into renderers.

const MAX_TABLE_ROWS = 500;
const MAX_TABLE_COLS = 30;
const MAX_COL_KEY_LEN = 64;
const MAX_COL_TITLE_LEN = 128;
const MAX_CELL_STR_LEN = 4096;
const VALID_COL_KEY = /^[a-zA-Z_][a-zA-Z0-9_]*$/;

const MAX_CHART_POINTS = 500;
const MAX_CHART_SERIES = 8;
const MAX_PIE_SLICES = 24;
const MAX_SERIES_NAME_LEN = 128;
const MAX_AXIS_LABEL_LEN = 128;
const MIN_CHART_VALUE = -1e12;
const MAX_CHART_VALUE = 1e12;

const MAX_SOURCE_LEN = 100_000;
const MAX_CONTENT_LEN = 200_000;
const MAX_FILENAME_LEN = 512;

const MAX_ARTIFACT_ID_LEN = 256;
const MAX_ARTIFACT_NAME_LEN = 512;
const MAX_ARTIFACT_TYPE_LEN = 128;
const MAX_ARTIFACT_REF_LEN = 4096;
const MAX_ARTIFACT_ERROR_LEN = 4096;
const VALID_ARTIFACT_STATUSES = new Set(['available', 'stale', 'missing', 'failed']);
const VALID_PREVIEW_TYPES = new Set(['card', 'file', 'image']);
const VALID_CODE_LANGUAGE = /^[a-zA-Z0-9_+.#-]+$/;

export interface SafeColumn {
  key: string;
  title: string;
  type?: string;
}

export interface SafeTableData {
  columns: SafeColumn[];
  rows: Record<string, unknown>[];
}

export interface SafeChartData {
  type: 'bar' | 'line' | 'pie';
  series: { label: string; values: number[] }[];
  axes?: { x?: { labels?: string[] }; y?: { label?: string } };
  legend?: boolean;
}

export interface SafeGraphData {
  format: 'mermaid';
  source: string;
}

export interface SafeCodeData {
  language: string;
  content: string;
  filename?: string;
}

export interface SafeMarkdownData {
  content: string;
}

export interface SafeArtifactData {
  artifactRef: {
    id: string;
    name: string;
    type: string;
    status: 'available' | 'stale' | 'missing' | 'failed';
    path?: string;
    relativePath?: string;
    blobDigest?: string;
    sourceRunId?: string;
    lastVerifiedAt?: string;
    error?: string;
  };
  previewType?: 'card' | 'file' | 'image';
}

function isRecord(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function hasOnlyKeys(value: Record<string, unknown>, allowed: readonly string[]): boolean {
  const keys = new Set(allowed);
  return Object.keys(value).every((key) => keys.has(key));
}

function isBoundedString(value: unknown, max: number, allowEmpty = true): value is string {
  return typeof value === 'string' && value.length <= max && (allowEmpty || value.length > 0);
}

// ── Table ────────────────────────────────────────────────────────────────────

export function validateTableData(data: unknown): data is SafeTableData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['columns', 'rows'])) return false;
  const d = data;
  if (!Array.isArray(d.columns) || d.columns.length === 0 || d.columns.length > MAX_TABLE_COLS) return false;
  if (!Array.isArray(d.rows) || d.rows.length > MAX_TABLE_ROWS) return false;

  const seen = new Set<string>();
  for (const col of d.columns) {
    if (!isRecord(col) || !hasOnlyKeys(col, ['key', 'title', 'type'])) return false;
    const c = col;
    if (typeof c.key !== 'string' || !VALID_COL_KEY.test(c.key) || c.key.length > MAX_COL_KEY_LEN) return false;
    if (!isBoundedString(c.title, MAX_COL_TITLE_LEN, false)) return false;
    if (c.type !== undefined && !isBoundedString(c.type, 32, false)) return false;
    if (seen.has(c.key)) return false;
    seen.add(c.key);
  }

  for (const row of d.rows) {
    if (!isRecord(row) || !hasOnlyKeys(row, [...seen])) return false;
    for (const value of Object.values(row)) {
      if (value === null) continue;
      if (typeof value === 'string' && value.length <= MAX_CELL_STR_LEN) continue;
      if (typeof value === 'number' && Number.isFinite(value)) continue;
      if (typeof value === 'boolean') continue;
      return false;
    }
  }
  return true;
}

// ── Chart ────────────────────────────────────────────────────────────────────

export function validateChartData(data: unknown): data is SafeChartData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['type', 'series', 'axes', 'legend'])) return false;
  const d = data;

  const chartType = d.type;
  if (chartType !== 'bar' && chartType !== 'line' && chartType !== 'pie') return false;

  if (!Array.isArray(d.series) || d.series.length === 0 || d.series.length > MAX_CHART_SERIES) return false;
  let totalPoints = 0;
  let pointCount = -1;
  for (const s of d.series) {
    if (!isRecord(s) || !hasOnlyKeys(s, ['label', 'values'])) return false;
    const serie = s;
    if (!isBoundedString(serie.label, MAX_SERIES_NAME_LEN, false)) return false;
    if (!Array.isArray(serie.values) || serie.values.length === 0) return false;
    if (pointCount < 0) pointCount = serie.values.length;
    if (serie.values.length !== pointCount) return false;
    totalPoints += serie.values.length;
    if (totalPoints > MAX_CHART_POINTS) return false;
    for (const v of serie.values) {
      if (typeof v !== 'number' || !Number.isFinite(v)) return false;
      if (v < MIN_CHART_VALUE || v > MAX_CHART_VALUE) return false;
      if (chartType === 'pie' && v < 0) return false;
    }
  }
  if (chartType === 'pie' && (d.series.length !== 1 || pointCount > MAX_PIE_SLICES)) return false;

  if (d.axes !== undefined) {
    if (!isRecord(d.axes) || !hasOnlyKeys(d.axes, ['x', 'y'])) return false;
    const axes = d.axes;
    if (axes.x !== undefined) {
      if (!isRecord(axes.x) || !hasOnlyKeys(axes.x, ['labels'])) return false;
      const x = axes.x;
      if (x.labels !== undefined) {
        if (!Array.isArray(x.labels) || x.labels.length !== pointCount) return false;
        for (const label of x.labels) {
          if (typeof label !== 'string' || label.length > MAX_AXIS_LABEL_LEN) return false;
        }
      }
    }
    if (axes.y !== undefined) {
      if (!isRecord(axes.y) || !hasOnlyKeys(axes.y, ['label'])) return false;
      if (axes.y.label !== undefined && !isBoundedString(axes.y.label, MAX_AXIS_LABEL_LEN)) return false;
    }
  }

  if (d.legend !== undefined && typeof d.legend !== 'boolean') return false;

  return true;
}

// ── Graph ────────────────────────────────────────────────────────────────────

export function validateGraphData(data: unknown): data is SafeGraphData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['format', 'source'])) return false;
  const d = data;
  if (d.format !== 'mermaid') return false;
  if (typeof d.source !== 'string' || d.source.trim().length === 0) return false;
  if (d.source.length > MAX_SOURCE_LEN) return false;
  return true;
}

// ── Code ─────────────────────────────────────────────────────────────────────

export function validateCodeData(data: unknown): data is SafeCodeData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['language', 'content', 'filename'])) return false;
  const d = data;
  if (!isBoundedString(d.language, 64, false) || !VALID_CODE_LANGUAGE.test(d.language)) return false;
  if (typeof d.content !== 'string') return false;
  if (d.content.length > MAX_CONTENT_LEN) return false;
  if (d.filename !== undefined) {
    if (typeof d.filename !== 'string' || d.filename.length > MAX_FILENAME_LEN) return false;
    // Prevent path traversal in display string
    if (d.filename.includes('\0') || d.filename.includes('\n') || d.filename.includes('\r')) return false;
  }
  return true;
}

// ── Markdown ─────────────────────────────────────────────────────────────────

export function validateMarkdownData(data: unknown): data is SafeMarkdownData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['content'])) return false;
  const d = data;
  if (typeof d.content !== 'string') return false;
  if (d.content.length > MAX_CONTENT_LEN) return false;
  return true;
}

// ── Artifact ─────────────────────────────────────────────────────────────────

export function validateArtifactData(data: unknown): data is SafeArtifactData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['artifactRef', 'previewType'])) return false;
  const d = data;
  const ref = d.artifactRef;
  if (!isRecord(ref) || !hasOnlyKeys(ref, [
    'id', 'name', 'type', 'status', 'path', 'relativePath', 'blobDigest',
    'sourceRunId', 'lastVerifiedAt', 'error',
  ])) return false;
  const r = ref;
  if (typeof r.id !== 'string' || r.id.length === 0 || r.id.length > MAX_ARTIFACT_ID_LEN) return false;
  if (!isBoundedString(r.name, MAX_ARTIFACT_NAME_LEN, false)) return false;
  if (!isBoundedString(r.type, MAX_ARTIFACT_TYPE_LEN, false)) return false;
  if (typeof r.status !== 'string' || !VALID_ARTIFACT_STATUSES.has(r.status)) return false;
  // Optional fields must be the right type if present
  if (r.path !== undefined && !isBoundedString(r.path, MAX_ARTIFACT_REF_LEN)) return false;
  if (r.relativePath !== undefined && !isBoundedString(r.relativePath, MAX_ARTIFACT_REF_LEN)) return false;
  if (r.blobDigest !== undefined && !isBoundedString(r.blobDigest, 256)) return false;
  if (r.sourceRunId !== undefined && !isBoundedString(r.sourceRunId, MAX_ARTIFACT_ID_LEN)) return false;
  if (r.lastVerifiedAt !== undefined && !isBoundedString(r.lastVerifiedAt, 64)) return false;
  if (r.error !== undefined && !isBoundedString(r.error, MAX_ARTIFACT_ERROR_LEN)) return false;
  if (d.previewType !== undefined && (typeof d.previewType !== 'string' || !VALID_PREVIEW_TYPES.has(d.previewType))) return false;
  return true;
}

// ── Value safety ─────────────────────────────────────────────────────────────

/** Convert unknown cell value to safe display text. Never produces HTML. */
export function safeCellText(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') {
    if (value.length > MAX_CELL_STR_LEN) return value.slice(0, MAX_CELL_STR_LEN) + '…';
    return value;
  }
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) return '';
    return String(value);
  }
  if (typeof value === 'boolean') return String(value);
  if (typeof value === 'bigint') return String(value).slice(0, MAX_CELL_STR_LEN);
  return '';
}

// ── Action Entry ─────────────────────────────────────────────────────────────

const MAX_DESCRIPTION_LEN = 2000;
const MAX_LAST_RESULT_LEN = 2000;

export interface SafeActionEntryData {
  description: string;
  lastResult?: string;
}

export function validateActionEntryData(data: unknown): data is SafeActionEntryData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['description', 'lastResult'])) return false;
  const d = data;
  if (!isBoundedString(d.description, MAX_DESCRIPTION_LEN, false)) return false;
  if (d.lastResult !== undefined && !isBoundedString(d.lastResult, MAX_LAST_RESULT_LEN)) return false;
  return true;
}

// ── Decision ─────────────────────────────────────────────────────────────────

const MAX_QUESTION_LEN = 500;
const MAX_OPTION_LABEL_LEN = 200;
const MAX_OPTION_DESC_LEN = 2000;
const MAX_OPTIONS = 20;
const MAX_CONTEXT_LEN = 4000;

export interface SafeDecisionData {
  question: string;
  options: Array<{ id: string; label: string; description?: string }>;
  context?: string;
  multiSelect?: boolean;
}

export function validateDecisionData(data: unknown): data is SafeDecisionData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['question', 'options', 'context', 'multiSelect'])) return false;
  const d = data;
  if (!isBoundedString(d.question, MAX_QUESTION_LEN, false)) return false;
  if (!Array.isArray(d.options) || d.options.length === 0 || d.options.length > MAX_OPTIONS) return false;
  const seen = new Set<string>();
  for (const opt of d.options) {
    if (!isRecord(opt) || !hasOnlyKeys(opt, ['id', 'label', 'description'])) return false;
    const o = opt;
    if (typeof o.id !== 'string' || o.id.length === 0 || o.id.length > MAX_COL_KEY_LEN || !VALID_COL_KEY.test(o.id)) return false;
    if (!isBoundedString(o.label, MAX_OPTION_LABEL_LEN, false)) return false;
    if (o.description !== undefined && !isBoundedString(o.description, MAX_OPTION_DESC_LEN)) return false;
    if (seen.has(o.id)) return false;
    seen.add(o.id);
  }
  if (d.context !== undefined && !isBoundedString(d.context, MAX_CONTEXT_LEN)) return false;
  if (d.multiSelect !== undefined && typeof d.multiSelect !== 'boolean') return false;
  return true;
}

// ── Approval ─────────────────────────────────────────────────────────────────

const MAX_APPROVAL_TITLE_LEN = 256;
const MAX_APPROVAL_ITEM_LABEL_LEN = 200;
const MAX_APPROVAL_ITEM_DETAIL_LEN = 2000;
const MAX_APPROVAL_ITEMS = 50;
const VALID_RISKS = new Set(['read', 'write', 'destructive', 'external']);

export interface SafeApprovalData {
  title: string;
  description?: string;
  items: Array<{ id: string; label: string; detail?: string; risk?: 'read' | 'write' | 'destructive' | 'external' }>;
  context?: string;
}

export function validateApprovalData(data: unknown): data is SafeApprovalData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['title', 'description', 'items', 'context'])) return false;
  const d = data;
  if (!isBoundedString(d.title, MAX_APPROVAL_TITLE_LEN, false)) return false;
  if (d.description !== undefined && !isBoundedString(d.description, MAX_DESCRIPTION_LEN)) return false;
  if (!Array.isArray(d.items) || d.items.length === 0 || d.items.length > MAX_APPROVAL_ITEMS) return false;
  const seen = new Set<string>();
  for (const item of d.items) {
    if (!isRecord(item) || !hasOnlyKeys(item, ['id', 'label', 'detail', 'risk'])) return false;
    const it = item;
    if (typeof it.id !== 'string' || it.id.length === 0 || it.id.length > MAX_COL_KEY_LEN || !VALID_COL_KEY.test(it.id)) return false;
    if (!isBoundedString(it.label, MAX_APPROVAL_ITEM_LABEL_LEN, false)) return false;
    if (it.detail !== undefined && !isBoundedString(it.detail, MAX_APPROVAL_ITEM_DETAIL_LEN)) return false;
    if (it.risk !== undefined && (typeof it.risk !== 'string' || !VALID_RISKS.has(it.risk))) return false;
    if (seen.has(it.id)) return false;
    seen.add(it.id);
  }
  if (d.context !== undefined && !isBoundedString(d.context, MAX_CONTEXT_LEN)) return false;
  return true;
}

// ── Input ────────────────────────────────────────────────────────────────────

const MAX_INPUT_PROMPT_LEN = 1000;
const MAX_FIELDS = 20;
const MAX_FIELD_LABEL_LEN = 200;
const MAX_FIELD_PLACEHOLDER_LEN = 200;
const MAX_INPUT_OPTIONS = 100;
const VALID_FIELD_TYPES = new Set(['text', 'textarea', 'select']);

export interface SafeInputData {
  prompt: string;
  fields: Array<{
    id: string;
    label: string;
    type?: 'text' | 'textarea' | 'select';
    required?: boolean;
    placeholder?: string;
    options?: Array<{ value: string; label: string }>;
  }>;
  context?: string;
}

export function validateInputData(data: unknown): data is SafeInputData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['prompt', 'fields', 'context'])) return false;
  const d = data;
  if (!isBoundedString(d.prompt, MAX_INPUT_PROMPT_LEN, false)) return false;
  if (!Array.isArray(d.fields) || d.fields.length === 0 || d.fields.length > MAX_FIELDS) return false;
  const seen = new Set<string>();
  for (const field of d.fields) {
    if (!isRecord(field) || !hasOnlyKeys(field, ['id', 'label', 'type', 'required', 'placeholder', 'options'])) return false;
    const f = field;
    if (typeof f.id !== 'string' || f.id.length === 0 || f.id.length > MAX_COL_KEY_LEN || !VALID_COL_KEY.test(f.id)) return false;
    if (!isBoundedString(f.label, MAX_FIELD_LABEL_LEN, false)) return false;
    if (f.type !== undefined && (typeof f.type !== 'string' || !VALID_FIELD_TYPES.has(f.type))) return false;
    if (f.required !== undefined && typeof f.required !== 'boolean') return false;
    if (f.placeholder !== undefined && !isBoundedString(f.placeholder, MAX_FIELD_PLACEHOLDER_LEN)) return false;
    if (f.options !== undefined) {
      if (!Array.isArray(f.options) || f.options.length === 0 || f.options.length > MAX_INPUT_OPTIONS) return false;
      const optSeen = new Set<string>();
      for (const opt of f.options) {
        if (!isRecord(opt) || !hasOnlyKeys(opt, ['value', 'label'])) return false;
        if (typeof opt.value !== 'string' || opt.value.length === 0 || opt.value.length > 128) return false;
        if (!isBoundedString(opt.label, 256, false)) return false;
        if (optSeen.has(opt.value)) return false;
        optSeen.add(opt.value);
      }
    }
    if (seen.has(f.id)) return false;
    seen.add(f.id);
  }
  if (d.context !== undefined && !isBoundedString(d.context, MAX_CONTEXT_LEN)) return false;
  return true;
}

// ── Notice ───────────────────────────────────────────────────────────────────

const MAX_NOTICE_CONTENT_LEN = 4000;
const MAX_NOTICE_ACTION_LABEL_LEN = 64;
const VALID_NOTICE_LEVELS = new Set(['info', 'warning', 'error', 'success']);

export interface SafeNoticeData {
  level: 'info' | 'warning' | 'error' | 'success';
  content: string;
  retryable?: boolean;
  actionLabel?: string;
}

export function validateNoticeData(data: unknown): data is SafeNoticeData {
  if (!isRecord(data) || !hasOnlyKeys(data, ['level', 'content', 'retryable', 'actionLabel'])) return false;
  const d = data;
  if (typeof d.level !== 'string' || !VALID_NOTICE_LEVELS.has(d.level)) return false;
  if (!isBoundedString(d.content, MAX_NOTICE_CONTENT_LEN, false)) return false;
  if (d.retryable !== undefined && typeof d.retryable !== 'boolean') return false;
  if (d.actionLabel !== undefined && !isBoundedString(d.actionLabel, MAX_NOTICE_ACTION_LABEL_LEN, false)) return false;
  return true;
}
