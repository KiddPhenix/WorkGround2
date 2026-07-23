// Schema definitions and validators for core WorkBlock renderer kinds (V1).
// Each kind defines its data shape as a plain-object TypeScript interface
// and a strict validator that rejects non-object data, missing required fields,
// and wrong types — never relying on runtime coercion or duck-typing alone.

import type { ValidationResult } from './types';

// ── helpers ──────────────────────────────────────────────────────────

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== 'object') return false;
  try {
    const prototype = Object.getPrototypeOf(value);
    return prototype === Object.prototype || prototype === null;
  } catch {
    return false;
  }
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0;
}

function isString(value: unknown): value is string {
  return typeof value === 'string';
}

function isArray(value: unknown): value is unknown[] {
  return Array.isArray(value);
}

function fail(reason: string): ValidationResult {
  return { valid: false, reason };
}

function ok(): ValidationResult {
  return { valid: true };
}

// ── item / list ──────────────────────────────────────────────────────

export type ItemState = 'pending' | 'success' | 'error' | 'warning' | 'info';

export interface ListItem {
  id: string;
  title: string;
  detail?: string;
  state?: ItemState;
}

export interface ItemListData {
  items: ListItem[];
}

const VALID_ITEM_STATES = new Set<string>(['pending', 'success', 'error', 'warning', 'info']);

function validateListItems(items: unknown, kind: string): ValidationResult {
  if (!isArray(items)) return fail(`${kind} data.items must be an array`);
  const ids = new Set<string>();
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (!isPlainObject(item)) return fail(`${kind} data.items[${i}] must be a plain object`);
    if (!isNonEmptyString(item.id)) return fail(`${kind} data.items[${i}].id must be a non-empty string`);
    if (ids.has(item.id)) return fail(`${kind} data.items[${i}].id must be unique`);
    ids.add(item.id);
    if (!isNonEmptyString(item.title)) return fail(`${kind} data.items[${i}].title must be a non-empty string`);
    if (item.detail !== undefined && !isString(item.detail)) return fail(`${kind} data.items[${i}].detail must be a string`);
    if (item.state !== undefined) {
      if (!isString(item.state) || !VALID_ITEM_STATES.has(item.state)) {
        return fail(`${kind} data.items[${i}].state must be one of: ${[...VALID_ITEM_STATES].join(', ')}`);
      }
    }
  }
  return ok();
}

export function validateItemList(_schemaVersion: number, data: unknown): ValidationResult {
  if (!isPlainObject(data)) return fail('item/list data must be a plain object');
  if (!('items' in data)) return fail('item/list data requires an items array');
  if (data.items === undefined || data.items === null) return fail('item/list data.items must be an array, not null');
  return validateListItems(data.items, 'item');
}

// ── checklist ────────────────────────────────────────────────────────

export interface ChecklistItem {
  id: string;
  text: string;
  checked: boolean;
  detail?: string;
}

export interface ChecklistData {
  items: ChecklistItem[];
}

export function validateChecklist(_schemaVersion: number, data: unknown): ValidationResult {
  if (!isPlainObject(data)) return fail('checklist data must be a plain object');
  if (!('items' in data)) return fail('checklist data requires an items array');
  if (data.items === undefined || data.items === null) return fail('checklist data.items must be an array, not null');
  const items = data.items;
  if (!isArray(items)) return fail('checklist data.items must be an array');
  const ids = new Set<string>();
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (!isPlainObject(item)) return fail(`checklist data.items[${i}] must be a plain object`);
    if (!isNonEmptyString(item.id)) return fail(`checklist data.items[${i}].id must be a non-empty string`);
    if (ids.has(item.id)) return fail(`checklist data.items[${i}].id must be unique`);
    ids.add(item.id);
    if (!isNonEmptyString(item.text)) return fail(`checklist data.items[${i}].text must be a non-empty string`);
    if (typeof item.checked !== 'boolean') return fail(`checklist data.items[${i}].checked must be a boolean`);
    if (item.detail !== undefined && !isString(item.detail)) return fail(`checklist data.items[${i}].detail must be a string`);
  }
  return ok();
}

// ── file_list ────────────────────────────────────────────────────────

export type FileStatus = 'added' | 'modified' | 'deleted' | 'renamed' | 'unchanged' | 'untracked' | 'conflict';

export interface FileEntry {
  path: string;
  status: FileStatus;
  digest?: string;
  desc?: string;
}

export interface FileListData {
  files: FileEntry[];
}

const VALID_FILE_STATUSES = new Set<string>(['added', 'modified', 'deleted', 'renamed', 'unchanged', 'untracked', 'conflict']);

export function validateFileList(_schemaVersion: number, data: unknown): ValidationResult {
  if (!isPlainObject(data)) return fail('file_list data must be a plain object');
  if (!('files' in data)) return fail('file_list data requires a files array');
  if (data.files === undefined || data.files === null) return fail('file_list data.files must be an array, not null');
  const files = data.files;
  if (!isArray(files)) return fail('file_list data.files must be an array');
  const paths = new Set<string>();
  for (let i = 0; i < files.length; i++) {
    const file = files[i];
    if (!isPlainObject(file)) return fail(`file_list data.files[${i}] must be a plain object`);
    if (!isNonEmptyString(file.path)) return fail(`file_list data.files[${i}].path must be a non-empty string`);
    if (paths.has(file.path)) return fail(`file_list data.files[${i}].path must be unique`);
    paths.add(file.path);
    if (!isString(file.status) || !VALID_FILE_STATUSES.has(file.status)) {
      return fail(`file_list data.files[${i}].status must be one of: ${[...VALID_FILE_STATUSES].join(', ')}`);
    }
    if (file.digest !== undefined && !isString(file.digest)) return fail(`file_list data.files[${i}].digest must be a string`);
    if (file.desc !== undefined && !isString(file.desc)) return fail(`file_list data.files[${i}].desc must be a string`);
  }
  return ok();
}

// ── git_status ───────────────────────────────────────────────────────

export type ChangeType = 'added' | 'modified' | 'deleted' | 'renamed' | 'untracked' | 'conflict';

export interface GitChange {
  file: string;
  staged: boolean;
  type: ChangeType;
}

export interface GitStatusData {
  branch: string;
  changes: GitChange[];
}

const VALID_CHANGE_TYPES = new Set<string>(['added', 'modified', 'deleted', 'renamed', 'untracked', 'conflict']);

export function validateGitStatus(_schemaVersion: number, data: unknown): ValidationResult {
  if (!isPlainObject(data)) return fail('git_status data must be a plain object');
  if (!isNonEmptyString(data.branch)) return fail('git_status data.branch must be a non-empty string');
  if (!('changes' in data)) return fail('git_status data requires a changes array');
  if (data.changes === undefined || data.changes === null) return fail('git_status data.changes must be an array, not null');
  const changes = data.changes;
  if (!isArray(changes)) return fail('git_status data.changes must be an array');
  for (let i = 0; i < changes.length; i++) {
    const change = changes[i];
    if (!isPlainObject(change)) return fail(`git_status data.changes[${i}] must be a plain object`);
    if (!isNonEmptyString(change.file)) return fail(`git_status data.changes[${i}].file must be a non-empty string`);
    if (typeof change.staged !== 'boolean') return fail(`git_status data.changes[${i}].staged must be a boolean`);
    if (!isString(change.type) || !VALID_CHANGE_TYPES.has(change.type)) {
      return fail(`git_status data.changes[${i}].type must be one of: ${[...VALID_CHANGE_TYPES].join(', ')}`);
    }
  }
  return ok();
}

// ── key_value / status ───────────────────────────────────────────────

export type KVState = 'default' | 'success' | 'error' | 'warning' | 'info';

export interface KVItem {
  key: string;
  label: string;
  value: string;
  state?: KVState;
}

export interface KeyValueData {
  items: KVItem[];
}

const VALID_KV_STATES = new Set<string>(['default', 'success', 'error', 'warning', 'info']);

export function validateKeyValue(_schemaVersion: number, data: unknown): ValidationResult {
  if (!isPlainObject(data)) return fail('key_value data must be a plain object');
  if (!('items' in data)) return fail('key_value data requires an items array');
  if (data.items === undefined || data.items === null) return fail('key_value data.items must be an array, not null');
  const items = data.items;
  if (!isArray(items)) return fail('key_value data.items must be an array');
  const keys = new Set<string>();
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (!isPlainObject(item)) return fail(`key_value data.items[${i}] must be a plain object`);
    if (!isNonEmptyString(item.key)) return fail(`key_value data.items[${i}].key must be a non-empty string`);
    if (keys.has(item.key)) return fail(`key_value data.items[${i}].key must be unique`);
    keys.add(item.key);
    if (!isNonEmptyString(item.label)) return fail(`key_value data.items[${i}].label must be a non-empty string`);
    if (!isString(item.value)) return fail(`key_value data.items[${i}].value must be a string`);
    if (item.state !== undefined) {
      if (!isString(item.state) || !VALID_KV_STATES.has(item.state)) {
        return fail(`key_value data.items[${i}].state must be one of: ${[...VALID_KV_STATES].join(', ')}`);
      }
    }
  }
  return ok();
}

// ── progress / timeline ──────────────────────────────────────────────

export type ProgressState = 'pending' | 'in_progress' | 'completed' | 'failed' | 'cancelled' | 'skipped';

export interface ProgressItem {
  id: string;
  label: string;
  state: ProgressState;
  time?: string;
}

export interface ProgressData {
  items: ProgressItem[];
}

const VALID_PROGRESS_STATES = new Set<string>(['pending', 'in_progress', 'completed', 'failed', 'cancelled', 'skipped']);

export function validateProgress(_schemaVersion: number, data: unknown): ValidationResult {
  if (!isPlainObject(data)) return fail('progress/timeline data must be a plain object');
  if (!('items' in data)) return fail('progress/timeline data requires an items array');
  if (data.items === undefined || data.items === null) return fail('progress/timeline data.items must be an array, not null');
  const items = data.items;
  if (!isArray(items)) return fail('progress/timeline data.items must be an array');
  const ids = new Set<string>();
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (!isPlainObject(item)) return fail(`progress/timeline data.items[${i}] must be a plain object`);
    if (!isNonEmptyString(item.id)) return fail(`progress/timeline data.items[${i}].id must be a non-empty string`);
    if (ids.has(item.id)) return fail(`progress/timeline data.items[${i}].id must be unique`);
    ids.add(item.id);
    if (!isNonEmptyString(item.label)) return fail(`progress/timeline data.items[${i}].label must be a non-empty string`);
    if (!isString(item.state) || !VALID_PROGRESS_STATES.has(item.state)) {
      return fail(`progress/timeline data.items[${i}].state must be one of: ${[...VALID_PROGRESS_STATES].join(', ')}`);
    }
    if (item.time !== undefined && !isString(item.time)) return fail(`progress/timeline data.items[${i}].time must be a string`);
  }
  return ok();
}
