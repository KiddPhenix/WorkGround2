import React, { useCallback } from 'react';

import type { WorkFace } from '../../work/store';

export interface WorkFlipControlProps {
  activeFace: WorkFace;
  onFlip: (next: WorkFace) => void;
  faceIDs?: Partial<Record<WorkFace, string>>;
  disabled?: boolean;
}

const BUTTON_LABELS: Record<WorkFace, string> = {
  front: '会话',
  back: '工作流',
};

export const WorkFlipControl: React.FC<WorkFlipControlProps> = ({
  activeFace,
  onFlip,
  faceIDs,
  disabled = false,
}) => {
  const nextFace: WorkFace = activeFace === 'front' ? 'back' : 'front';
  const handleFlip = useCallback(() => {
    if (!disabled) onFlip(nextFace);
  }, [disabled, nextFace, onFlip]);

  return (
    <button
      type="button"
      className="wg2-work-flip-button"
      onClick={handleFlip}
      disabled={disabled}
      aria-label={`查看${BUTTON_LABELS[activeFace]}`}
      aria-controls={faceIDs?.[nextFace]}
      data-testid="work-flip-button"
    >
      <span className="wg2-work-flip-icon" aria-hidden="true">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
          <path d="M3 5L8 1L13 5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
          <path d="M13 11L8 15L3 11" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </span>
      <span className="wg2-work-flip-label">{BUTTON_LABELS[activeFace]}</span>
    </button>
  );
};
