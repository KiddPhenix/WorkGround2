// Markdown WorkBlock renderer (kind: markdown, schema v1).
// Only accepts {content}. Reuses MarkdownRenderer (no raw HTML enabled).
// Content length is limited; malicious HTML/event/style cannot enter DOM.

import React from 'react';
import type { BlockRendererProps } from './types';
import { validateMarkdownData, normalizeMarkdownData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  if (schemaVersion !== 1) return { valid: false, reason: `unsupported schema version ${schemaVersion}` };
  if (!validateMarkdownData(data)) return { valid: false, reason: 'invalid markdown data: requires {content:string}' };
  return { valid: true };
}

const Markdown = React.lazy(() => import('../../../components/Markdown').then((m) => ({ default: m.Markdown })));

const MarkdownBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = normalizeMarkdownData(block.data)!;
  return (
    <div className="wg2-markdown-block" role="region" aria-label={block.title ?? 'Markdown block'}>
      <React.Suspense fallback={<div className="wg2-markdown-block__loading">Rendering markdown…</div>}>
        <Markdown text={data.content} />
      </React.Suspense>
    </div>
  );
};

export default MarkdownBlock;
