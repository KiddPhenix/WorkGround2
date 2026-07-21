// Code WorkBlock renderer (kind: code, schema v1).
// Only accepts {language, content, filename?}. Reuses CodeViewer.
// filename is plain text only; content length is limited.

import React from 'react';
import type { BlockRendererProps } from './types';
import { validateCodeData } from './schemaHelpers';
import type { SafeCodeData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  if (schemaVersion !== 1) return { valid: false, reason: `unsupported schema version ${schemaVersion}` };
  if (!validateCodeData(data)) return { valid: false, reason: 'invalid code data: requires {language:string, content:string, filename?:string}' };
  return { valid: true };
}

const CodeViewer = React.lazy(() => import('../../../components/CodeViewer').then((m) => ({ default: m.CodeViewer })));

const CodeBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = block.data as SafeCodeData;
  return (
    <div className="wg2-code-block" role="region" aria-label={block.title ?? 'Code block'}>
      {data.filename && (
        <div className="wg2-code-block__filename" aria-label={`File: ${data.filename}`}>
          {data.filename}
        </div>
      )}
      <React.Suspense fallback={
        <pre className="code code--loading"><code>{data.content.slice(0, 1000)}</code></pre>
      }>
        <CodeViewer value={data.content} language={data.language} maxHeight={480} />
      </React.Suspense>
    </div>
  );
};

export default CodeBlock;
