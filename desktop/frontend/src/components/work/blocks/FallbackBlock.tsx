// Text-only fallback for unavailable block renderers. It never evaluates HTML,
// CSS, commands, module paths, or host calls from block data.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { BlockFallback, BlockInstance } from '../../../work/types';

const SECRET_KEY = /(?:^|[_-])(?:auth(?:orization)?|cookie|credential|password|secret|session|token|api[_-]?key|client[_-]?secret|private[_-]?key|access[_-]?key)(?:$|[_-])/i;
const MAX_DEPTH = 10;
const MAX_ITEMS = 200;
const MAX_STRING = 512;
const MAX_BYTES = 64 * 1024;

function shortText(value: unknown, fallback = ''): string {
  if (typeof value !== 'string') return fallback;
  const clean = value.replace(/[\u0000-\u001f\u007f]/g, ' ').trim();
  const chars = Array.from(clean);
  return chars.length > MAX_STRING ? `${chars.slice(0, MAX_STRING).join('')}…` : clean;
}

function safeValue(value: unknown): unknown {
  const active = new WeakSet<object>();

  const walk = (current: unknown, depth: number): unknown => {
    if (depth > MAX_DEPTH) return '[max depth]';
    if (current === null) return null;
    if (current === undefined) return '[undefined]';
    if (typeof current === 'string') return shortText(current);
    if (typeof current === 'number') return Number.isFinite(current) ? current : `[${String(current)}]`;
    if (typeof current === 'boolean') return current;
    if (typeof current === 'bigint') return `[BigInt ${String(current).slice(0, 40)}]`;
    if (typeof current === 'function') return '[function omitted]';
    if (typeof current === 'symbol') return '[symbol omitted]';
    if (typeof current !== 'object') return String(current);
    if (active.has(current)) return '[circular]';

    active.add(current);
    try {
      if (Array.isArray(current)) {
        const result = current.slice(0, MAX_ITEMS).map((item) => walk(item, depth + 1));
        if (current.length > MAX_ITEMS) result.push(`[${current.length - MAX_ITEMS} items omitted]`);
        return result;
      }

      const result: Record<string, unknown> = {};
      let keys: string[];
      try {
        keys = Object.keys(current);
      } catch {
        return '[object keys unavailable]';
      }
      for (const key of keys.slice(0, MAX_ITEMS)) {
        if (SECRET_KEY.test(key)) {
          result[key] = '[redacted]';
          continue;
        }
        try {
          const descriptor = Object.getOwnPropertyDescriptor(current, key);
          result[key] = descriptor && 'value' in descriptor
            ? walk(descriptor.value, depth + 1)
            : '[accessor omitted]';
        } catch {
          result[key] = '[property unavailable]';
        }
      }
      if (keys.length > MAX_ITEMS) result['…'] = `[${keys.length - MAX_ITEMS} properties omitted]`;
      return result;
    } finally {
      active.delete(current);
    }
  };

  return walk(value, 0);
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

function boundedJson(value: unknown): string {
  let serialized: string;
  try {
    serialized = JSON.stringify(safeValue(value), null, 2) ?? 'null';
  } catch {
    serialized = JSON.stringify('[serialization failed]');
  }
  const originalBytes = byteLength(serialized);
  if (originalBytes <= MAX_BYTES) return serialized;

  const chars = Array.from(serialized);
  let length = Math.min(chars.length, 32 * 1024);
  let result = '';
  do {
    result = JSON.stringify({
      truncated: true,
      originalBytes,
      preview: chars.slice(0, length).join(''),
    }, null, 2);
    length = Math.floor(length * 0.75);
  } while (byteLength(result) > MAX_BYTES && length > 0);
  return result;
}

export interface FallbackBlockProps {
  block: BlockInstance;
  fallback?: BlockFallback;
  reason?: string;
  interactiveDisabled?: boolean;
}

export const FallbackBlock: React.FC<FallbackBlockProps> = ({
  block,
  fallback,
  reason,
  interactiveDisabled = false,
}) => {
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const timeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  const copyGeneration = useRef(0);
  const mounted = useRef(true);
  const json = useMemo(() => boundedJson(block), [block]);

  useEffect(() => {
    mounted.current = true;
    copyGeneration.current += 1;
    setCopyState('idle');
    return () => {
      mounted.current = false;
      copyGeneration.current += 1;
      if (timeout.current) clearTimeout(timeout.current);
    };
  }, [block]);

  const copy = useCallback(() => {
    if (interactiveDisabled) return;
    const current = ++copyGeneration.current;
    setCopyState('idle');
    let write: PromiseLike<void> | void;
    try {
      const clipboard = typeof navigator === 'undefined' ? undefined : navigator.clipboard;
      if (!clipboard?.writeText) {
        setCopyState('failed');
        return;
      }
      write = clipboard.writeText(json);
    } catch {
      setCopyState('failed');
      return;
    }
    void Promise.resolve(write).then(
      () => {
        if (!mounted.current || current !== copyGeneration.current) return;
        setCopyState('copied');
        if (timeout.current) clearTimeout(timeout.current);
        timeout.current = setTimeout(() => {
          if (mounted.current && current === copyGeneration.current) setCopyState('idle');
        }, 2000);
      },
      () => {
        if (mounted.current && current === copyGeneration.current) setCopyState('failed');
      },
    );
  }, [interactiveDisabled, json]);

  const kind = shortText(block.kind, 'unknown');
  const source = shortText(block.source?.provider, 'unknown');
  const mode = shortText(block.source?.mode);
  const sourceRef = shortText(block.source?.ref);
  const summary = shortText(fallback?.summary) || shortText(block.fallback?.summary) || shortText(block.title);
  const safeReason = shortText(reason, 'Renderer unavailable');

  return (
    <section
      className="wg2-fallback"
      aria-label={`Fallback view for ${kind}`}
      aria-describedby={`wg2-fallback-reason-${block.id}`}
    >
      <header className="wg2-fallback-header">
        <span className="wg2-fallback-badge">{kind}</span>
        <span id={`wg2-fallback-reason-${block.id}`} className="wg2-fallback-reason">
          {safeReason}
        </span>
        {block.tombstone && <span className="wg2-fallback-badge">removed</span>}
      </header>

      <dl className="wg2-fallback-meta">
        <dt>schemaVersion</dt><dd>{String(block.schemaVersion)}</dd>
        <dt>revision</dt><dd>{String(block.revision)}</dd>
        <dt>source</dt><dd>{source}{mode ? ` (${mode})` : ''}</dd>
        <dt>verified</dt><dd>{block.source?.verified ? 'yes' : 'no'}</dd>
        {sourceRef && <><dt>source ref</dt><dd>{sourceRef}</dd></>}
        <dt>created</dt><dd>{shortText(block.createdAt, 'unknown')}</dd>
        <dt>updated</dt><dd>{shortText(block.updatedAt, 'unknown')}</dd>
        <dt>status</dt><dd>{shortText(block.status, 'unknown')}</dd>
      </dl>

      {summary && <p className="wg2-fallback-summary">{summary}</p>}

      <button
        type="button"
        className="wg2-fallback-copy"
        onClick={copy}
        disabled={interactiveDisabled}
        aria-label={copyState === 'copied' ? 'Copied block JSON' : 'Copy safe block JSON'}
      >
        {copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed' : 'Copy safe JSON'}
      </button>
      {copyState === 'failed' && (
        <p className="wg2-fallback-copy-error" role="alert">
          Clipboard unavailable. Retry after granting clipboard access.
        </p>
      )}
    </section>
  );
};

FallbackBlock.displayName = 'FallbackBlock';
