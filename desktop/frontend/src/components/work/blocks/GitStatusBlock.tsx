// git_status renderer — Git workspace status snapshot with branch and changes.
// Uses only block projection data; no bridge, Git CLI, file system, or network access.
// Archived: shows frozen/archived indicator.

import React from 'react';
import type { BlockRendererProps } from './types';
import type { ChangeType, GitChange, GitStatusData } from './schemas';

const CHANGE_MARKERS: Record<ChangeType, { icon: string; label: string }> = {
  added: { icon: 'A', label: 'Added' },
  modified: { icon: 'M', label: 'Modified' },
  deleted: { icon: 'D', label: 'Deleted' },
  renamed: { icon: 'R', label: 'Renamed' },
  untracked: { icon: '?', label: 'Untracked' },
  conflict: { icon: '!', label: 'Conflict' },
};

function safeCrop(text: string, max: number): string {
  const chars = Array.from(text);
  return chars.length > max ? `${chars.slice(0, max).join('')}\u2026` : text;
}

const ChangeRow: React.FC<{ change: GitChange }> = ({ change }) => {
  const marker = CHANGE_MARKERS[change.type];
  return (
    <div className={`wg2-git-change wg2-git-change-${change.type}`} role="listitem">
      <span className="wg2-git-change-marker" aria-label={marker.label}>
        {marker.icon}
      </span>
      <span className="wg2-git-change-staged" aria-label={change.staged ? 'Staged' : 'Unstaged'}>
        {change.staged ? 'staged' : 'unstaged'}
      </span>
      <code className="wg2-git-change-file">{safeCrop(change.file, 512)}</code>
    </div>
  );
};

export const GitStatusBlock: React.FC<BlockRendererProps> = ({ block, archived }) => {
  const data = block.data as GitStatusData;
  const branch = data?.branch ?? '';
  const changes = data?.changes ?? [];

  return (
    <section className="wg2-block wg2-git-status-block" aria-label="Git status">
      <header className="wg2-git-header">
        {archived && (
          <span className="wg2-git-archived-badge" role="status" aria-label="Archived snapshot">
            [Archived]
          </span>
        )}
        <span className="wg2-git-branch-label">Branch:</span>
        <code className="wg2-git-branch-name">{safeCrop(branch || '(unknown)', 256)}</code>
      </header>

      {changes.length === 0 ? (
        <p className="wg2-block-empty" role="status">
          {archived ? 'No changes recorded at archive time' : 'Working tree clean'}
        </p>
      ) : (
        <div className="wg2-git-changes" role="list">
          {changes.map((change, index) => (
            <ChangeRow key={`${change.file}-${index}`} change={change} />
          ))}
        </div>
      )}

      {archived && (
        <footer className="wg2-git-archived-footer">
          <time dateTime={block.updatedAt}>
            Snapshot frozen at {block.updatedAt}
          </time>
        </footer>
      )}
    </section>
  );
};

GitStatusBlock.displayName = 'GitStatusBlock';
