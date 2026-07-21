// Graph WorkBlock renderer (kind: graph, schema v1).
// Only accepts {format:'mermaid', source}. Reuses MermaidDiagram via safe path.
// No raw HTML, CSS, React, or module paths accepted.

import React from 'react';
import type { BlockRendererProps } from './types';
import { validateGraphData } from './schemaHelpers';
import type { SafeGraphData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  if (schemaVersion !== 1) return { valid: false, reason: `unsupported schema version ${schemaVersion}` };
  if (!validateGraphData(data)) return { valid: false, reason: 'invalid graph data: requires {format:"mermaid", source:string}' };
  return { valid: true };
}

const MermaidDiagram = React.lazy(() => import('../../../components/MermaidDiagram'));

const GraphBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = block.data as SafeGraphData;
  return (
    <div className="wg2-graph-block" role="region" aria-label={block.title ?? 'Graph block'}>
      <React.Suspense fallback={<div className="wg2-graph-block__loading">Loading diagram…</div>}>
        <MermaidDiagram definition={data.source} />
      </React.Suspense>
    </div>
  );
};

export default GraphBlock;
