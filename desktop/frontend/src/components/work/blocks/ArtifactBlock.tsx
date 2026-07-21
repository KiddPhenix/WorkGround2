// Artifact WorkBlock renderer (kind: artifact, schema v1).
// Only accepts {artifactRef, previewType?}. Strictly validates ArtifactRef.
// Four states: available, stale, missing, failed. Safe read-only card.
// Never opens payload path or calls bridge directly.

import React from 'react';
import type { BlockRendererProps } from './types';
import { validateArtifactData } from './schemaHelpers';
import type { SafeArtifactData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  if (schemaVersion !== 1) return { valid: false, reason: `unsupported schema version ${schemaVersion}` };
  if (!validateArtifactData(data)) return { valid: false, reason: 'invalid artifact data: requires {artifactRef:{id,name,type,status}}' };
  return { valid: true };
}

const STATUS_ICONS: Record<string, string> = {
  available: '✓',
  stale: '⚠',
  missing: '✗',
  failed: '✗',
};

const STATUS_LABELS: Record<string, string> = {
  available: 'Available',
  stale: 'Stale',
  missing: 'Missing',
  failed: 'Failed',
};

const ArtifactBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = block.data as SafeArtifactData;
  const ref = data.artifactRef;
  const status = ref.status;
  const isError = status === 'failed' || status === 'missing';

  return (
    <div
      className={`wg2-artifact-block wg2-artifact-block--${status}`}
      role="region"
      aria-label={block.title ?? `Artifact: ${ref.name}`}
    >
      <div className="wg2-artifact-block__header">
        <span className="wg2-artifact-block__icon" aria-hidden="true">{STATUS_ICONS[status] ?? '?'}</span>
        <span className="wg2-artifact-block__name">{ref.name}</span>
        <span className={`wg2-artifact-block__status wg2-artifact-block__status--${status}`}>
          {STATUS_LABELS[status] ?? status}
        </span>
      </div>

      <dl className="wg2-artifact-block__meta">
        <dt>Type</dt>
        <dd>{ref.type}</dd>
        <dt>ID</dt>
        <dd>{ref.id}</dd>
        {ref.sourceRunId && <><dt>Run</dt><dd>{ref.sourceRunId}</dd></>}
        {ref.blobDigest && <><dt>Digest</dt><dd className="wg2-artifact-block__digest">{ref.blobDigest.slice(0, 16)}…</dd></>}
        {ref.lastVerifiedAt && <><dt>Verified</dt><dd>{ref.lastVerifiedAt}</dd></>}
      </dl>

      {isError && ref.error && (
        <div className="wg2-artifact-block__error" role="alert">
          {ref.error}
        </div>
      )}

      <div className="wg2-artifact-block__footer">
        {status === 'available' && <span className="wg2-artifact-block__badge wg2-artifact-block__badge--ok">Ready</span>}
        {status === 'stale' && <span className="wg2-artifact-block__badge wg2-artifact-block__badge--warn">May be outdated</span>}
        {status === 'missing' && <span className="wg2-artifact-block__badge wg2-artifact-block__badge--err">Not found</span>}
        {status === 'failed' && <span className="wg2-artifact-block__badge wg2-artifact-block__badge--err">Generation failed</span>}
      </div>
    </div>
  );
};

export default ArtifactBlock;
