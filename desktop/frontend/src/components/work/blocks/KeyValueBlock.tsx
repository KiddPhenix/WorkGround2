// key_value / status renderer — semantic description list with optional state indicators.
// Uses only block projection data; no bridge, file, Git, or network access.

import React from 'react';
import type { BlockRendererProps } from './types';
import type { KeyValueData, KVItem, KVState } from './schemas';

const STATE_CLASS: Record<KVState, string> = {
  default: 'wg2-kv-state-default',
  success: 'wg2-kv-state-success',
  error: 'wg2-kv-state-error',
  warning: 'wg2-kv-state-warning',
  info: 'wg2-kv-state-info',
};

function safeCrop(text: string, max: number): string {
  const chars = Array.from(text);
  return chars.length > max ? `${chars.slice(0, max).join('')}\u2026` : text;
}

const KVRow: React.FC<{ item: KVItem }> = ({ item }) => (
  <div className={`wg2-kv-item ${item.state ? STATE_CLASS[item.state] : STATE_CLASS.default}`}>
    <dt className="wg2-kv-label">{safeCrop(item.label, 256)}</dt>
    <dd className="wg2-kv-value">
      <code className="wg2-kv-key">{safeCrop(item.key, 128)}</code>
      <span className="wg2-kv-separator" aria-hidden="true">=</span>
      <span className="wg2-kv-text">{safeCrop(item.value, 512)}</span>
    </dd>
  </div>
);

export const KeyValueBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = block.data as KeyValueData;
  const items = data?.items ?? [];

  if (items.length === 0) {
    return (
      <section className="wg2-block wg2-kv-block" aria-label="Key-value pairs">
        <p className="wg2-block-empty" role="status">No entries</p>
      </section>
    );
  }

  return (
    <section className="wg2-block wg2-kv-block" aria-label="Key-value pairs">
      <dl className="wg2-kv-list">
        {items.map((item) => (
          <KVRow key={item.key} item={item} />
        ))}
      </dl>
    </section>
  );
};

KeyValueBlock.displayName = 'KeyValueBlock';
