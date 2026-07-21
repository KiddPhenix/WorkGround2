// progress / timeline renderer — semantic progress and timeline with state indicators.
// Uses only block projection data; no bridge, file, Git, or network access.

import React, { useMemo } from 'react';
import type { BlockRendererProps } from './types';
import type { ProgressData, ProgressItem, ProgressState } from './schemas';

const STATE_CLASS: Record<ProgressState, string> = {
  pending: 'wg2-progress-state-pending',
  in_progress: 'wg2-progress-state-active',
  completed: 'wg2-progress-state-done',
  failed: 'wg2-progress-state-failed',
  cancelled: 'wg2-progress-state-cancelled',
  skipped: 'wg2-progress-state-skipped',
};

const STATE_ICON: Record<ProgressState, string> = {
  pending: '\u25CB',     // ○
  in_progress: '\u25D0', // ◐
  completed: '\u25C9',   // ◉
  failed: '\u2715',      // ✕
  cancelled: '\u229D',   // ⊝
  skipped: '\u2192',     // →
};

function safeCrop(text: string, max: number): string {
  const chars = Array.from(text);
  return chars.length > max ? `${chars.slice(0, max).join('')}\u2026` : text;
}

const TimelineItem: React.FC<{ item: ProgressItem; isLast: boolean }> = ({ item, isLast }) => (
  <li
    className={`wg2-timeline-item ${STATE_CLASS[item.state]} ${isLast ? 'wg2-timeline-item-last' : ''}`}
    aria-current={item.state === 'in_progress' ? 'step' : undefined}
  >
    <span className="wg2-timeline-marker" aria-hidden="true">
      {STATE_ICON[item.state]}
    </span>
    <div className="wg2-timeline-body">
      <span className="wg2-timeline-label">{safeCrop(item.label, 256)}</span>
      {item.time && (
        <time className="wg2-timeline-time" dateTime={item.time}>
          {safeCrop(item.time, 64)}
        </time>
      )}
    </div>
  </li>
);

export const ProgressBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = block.data as ProgressData;
  const items = data?.items ?? [];

  const progressValue = useMemo(() => {
    if (items.length === 0) return 0;
    const done = items.filter((i) => i.state === 'completed' || i.state === 'skipped').length;
    return Math.round((done / items.length) * 100);
  }, [items]);

  if (items.length === 0) {
    return (
      <section className="wg2-block wg2-progress-block" aria-label="Progress timeline">
        <p className="wg2-block-empty" role="status">No stages</p>
      </section>
    );
  }

  return (
    <section className="wg2-block wg2-progress-block" aria-label="Progress timeline">
      <div className="wg2-progress-bar-container">
        <progress
          className="wg2-progress-bar"
          value={progressValue}
          max={100}
          aria-label={`${progressValue}% complete`}
        >
          {progressValue}%
        </progress>
        <span className="wg2-progress-text" aria-hidden="true">
          {progressValue}%
        </span>
      </div>
      <ol className="wg2-timeline-list">
        {items.map((item, index) => (
          <TimelineItem
            key={item.id}
            item={item}
            isLast={index === items.length - 1}
          />
        ))}
      </ol>
    </section>
  );
};

ProgressBlock.displayName = 'ProgressBlock';
