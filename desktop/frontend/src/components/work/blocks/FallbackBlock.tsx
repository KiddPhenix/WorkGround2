// Text-only fallback for unavailable block renderers. It never evaluates HTML,
// CSS, commands, module paths, or host calls from block data.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { BlockFallback, BlockInstance } from '../../../work/types';
import { buildSafeJson } from './safeBlockJson';

const MAX_STRING = 512;
const BEARER_VALUE = /\bbearer\s+[^\s,;]+/gi;

function shortText(value: unknown, fallback = ''): string {
  if (typeof value !== 'string') return fallback;
  const clean = value
    .replace(/[\u0000-\u001f\u007f]/g, ' ')
    .replace(BEARER_VALUE, 'Bearer [redacted]')
    .trim();
  const chars = Array.from(clean);
  return chars.length > MAX_STRING ? `${chars.slice(0, MAX_STRING).join('')}…` : clean;
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
  const json = useMemo(() => buildSafeJson(block), [block]);

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
