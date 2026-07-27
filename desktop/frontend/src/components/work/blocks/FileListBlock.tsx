// file_list renderer — semantic file list with status badges.
// Uses only block projection data; no bridge, file system, Git, or network access.
// File paths are displayed as text, never as clickable host-operation entries.

import React from 'react';
import type { BlockRendererProps } from './types';
import type { FileEntry, FileListData, FileStatus } from './schemas';

const STATUS_ICONS: Record<FileStatus, string> = {
  added: '＋',
  modified: '～',
  deleted: '－',
  renamed: '→',
  unchanged: '✓',
  untracked: '？',
  conflict: '⚠',
};

function safeCrop(text: string, max: number): string {
  const chars = Array.from(text);
  return chars.length > max ? `${chars.slice(0, max).join('')}\u2026` : text;
}

const FileRow: React.FC<{ file: FileEntry; schemaVersion: number }> = ({ file, schemaVersion }) => (
  <div className={`wg2-file-item wg2-file-status-${file.status}`} role="listitem">
    <span className="wg2-file-icon" aria-hidden="true">
      {STATUS_ICONS[file.status]}
    </span>
    <span className="wg2-file-status-badge" aria-label={`Status: ${file.status}`}>
      {file.status}
    </span>
    <code className="wg2-file-path">{safeCrop(file.path, 512)}</code>
    {(schemaVersion === 2 ? file.description : file.desc) && (
      <span className="wg2-file-desc">
        {safeCrop((schemaVersion === 2 ? file.description : file.desc)!, 256)}
      </span>
    )}
    {file.digest && (
      <span className="wg2-file-digest" title={file.digest}>
        {safeCrop(file.digest, 16)}
      </span>
    )}
  </div>
);

export const FileListBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = block.data as FileListData;
  const files = data?.files ?? [];

  if (files.length === 0) {
    return (
      <section className="wg2-block wg2-file-list-block" aria-label="File list">
        <p className="wg2-block-empty" role="status">No files</p>
      </section>
    );
  }

  return (
    <section className="wg2-block wg2-file-list-block" aria-label="File list">
      <div className="wg2-file-list" role="list">
        {files.map((file, index) => (
          <FileRow key={file.path ?? index} file={file} schemaVersion={block.schemaVersion} />
        ))}
      </div>
    </section>
  );
};

FileListBlock.displayName = 'FileListBlock';
