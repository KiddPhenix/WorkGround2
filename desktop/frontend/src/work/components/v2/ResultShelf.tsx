import React from 'react';

import type { ArtifactSlot, ArtifactPreview } from '../../types_v2';
import { ResultCard } from './ResultCard';
import type { FileConversionIntent, FileDownloadIntent, FileLocateIntent, FileOpenIntent, SlotRetryIntent, FilePreviewIntent } from './ResultCard';

// ── ResultShelf ────────────────────────────────────────────────────────────

export interface ResultShelfProps {
  /** Artifact slots from the store projection. */
  slots: ArtifactSlot[];
  /** Only slots owned by this authoritative active Definition are visible. */
  activeDefinitionRevision: number;
  /** Called when the user wants to open a file. */
  onOpen?: (intent: FileOpenIntent) => void | Promise<void>;
  /** Called when the user wants to download a file. */
  onDownload?: (intent: FileDownloadIntent) => void | Promise<void>;
  /** Called when the user wants to locate a file on disk. */
  onLocate?: (intent: FileLocateIntent) => void | Promise<void>;
  /** Called when the user wants to retry a failed slot. */
  onRetry?: (intent: SlotRetryIntent) => void | Promise<void>;
  onPreview?: (intent: FilePreviewIntent) => Promise<ArtifactPreview>;
  onConvert?: (intent: FileConversionIntent) => Promise<ArtifactPreview>;
}

export const ResultShelf: React.FC<ResultShelfProps> = ({
  slots,
  activeDefinitionRevision,
  onOpen,
  onDownload,
  onLocate,
  onRetry,
  onPreview,
  onConvert,
}) => {
  const visibleSlots = slots.filter(
    (slot) => slot.definitionRev === activeDefinitionRevision,
  );

  if (visibleSlots.length === 0) {
    return (
      <div
        className="wg2-rs-shelf wg2-rs-empty"
        data-testid="result-shelf-empty"
        role="region"
        aria-label="成果架"
        aria-live="polite"
      >
        暂无成果
      </div>
    );
  }

  return (
    <ul
      className="wg2-rs-shelf"
      data-testid="result-shelf"
      role="region"
      aria-label="成果架"
      aria-live="polite"
    >
      {visibleSlots.map((slot) => (
        <li
          key={`${slot.definitionRev}:${slot.id}`}
          data-definition-revision={slot.definitionRev}
          data-testid={`result-shelf-item-${slot.id}`}
        >
          <ResultCard
            slot={slot}
            onOpen={onOpen}
            onDownload={onDownload}
            onLocate={onLocate}
            onRetry={onRetry}
            onPreview={onPreview}
            onConvert={onConvert}
          />
        </li>
      ))}
    </ul>
  );
};
