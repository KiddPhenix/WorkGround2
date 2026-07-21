// item/list renderer — semantic list with optional state indicators.
// Uses only block projection data; no bridge, file, Git, or network access.

import React from 'react';
import type { BlockRendererProps } from './types';
import type { ItemListData, ItemState, ListItem } from './schemas';

const STATE_CLASS: Record<ItemState, string> = {
  pending: 'wg2-item-state-pending',
  success: 'wg2-item-state-success',
  error: 'wg2-item-state-error',
  warning: 'wg2-item-state-warning',
  info: 'wg2-item-state-info',
};

function safeCrop(text: string, max: number): string {
  const chars = Array.from(text);
  return chars.length > max ? `${chars.slice(0, max).join('')}\u2026` : text;
}

const ItemRow: React.FC<{ item: ListItem }> = ({ item }) => {
  const detail = item.detail ? safeCrop(item.detail, 512) : null;
  return (
    <li className={`wg2-list-item ${item.state ? STATE_CLASS[item.state] : ''}`}>
      <span className="wg2-list-item-title">{safeCrop(item.title, 256)}</span>
      {detail && <span className="wg2-list-item-detail">{detail}</span>}
    </li>
  );
};

export const ItemListBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = block.data as ItemListData;
  const items = data?.items ?? [];

  if (items.length === 0) {
    return (
      <section className="wg2-block wg2-list-block" aria-label="Item list">
        <p className="wg2-block-empty" role="status">No items</p>
      </section>
    );
  }

  return (
    <section className="wg2-block wg2-list-block" aria-label="Item list">
      <ul className="wg2-item-list" role="list">
        {items.map((item) => (
          <ItemRow key={item.id} item={item} />
        ))}
      </ul>
    </section>
  );
};

ItemListBlock.displayName = 'ItemListBlock';
