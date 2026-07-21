// Table WorkBlock renderer (kind: table, schema v1).
// Strictly validated columns/rows, sortable, keyboard-accessible, safe text only.

import React, { useCallback, useMemo, useState } from 'react';
import type { BlockRendererProps } from './types';
import { safeCellText, validateTableData } from './schemaHelpers';
import type { SafeTableData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  if (schemaVersion !== 1) return { valid: false, reason: `unsupported schema version ${schemaVersion}` };
  if (!validateTableData(data)) return { valid: false, reason: 'invalid table data: requires columns[{key,title,type?}] and rows[]' };
  return { valid: true };
}

type SortState = { key: string; dir: 'asc' | 'desc' } | null;

function compareValues(a: unknown, b: unknown): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  if (typeof a === 'boolean' && typeof b === 'boolean') return Number(a) - Number(b);
  const sa = safeCellText(a);
  const sb = safeCellText(b);
  return sa.localeCompare(sb, undefined, { numeric: true, sensitivity: 'base' });
}

function stableSort<T extends Record<string, unknown>>(rows: T[], sort: SortState): T[] {
  if (!sort) return rows;
  const { key, dir } = sort;
  const indexed = rows.map((row, i) => ({ row, i }));
  indexed.sort((a, b) => {
    const cmp = compareValues(a.row[key], b.row[key]);
    return dir === 'asc' ? cmp || a.i - b.i : -cmp || a.i - b.i;
  });
  return indexed.map((item) => item.row);
}

const TableBlock: React.FC<BlockRendererProps> = ({ block, readonly: _readonly, archived }) => {
  const data = block.data as SafeTableData;
  const [sort, setSort] = useState<SortState>(null);
  const frozen = archived;

  const sortedRows = useMemo(() => stableSort(data.rows, sort), [data.rows, sort]);

  const handleSort = useCallback((colKey: string) => {
    if (frozen) return;
    setSort((prev) => {
      if (prev?.key === colKey) {
        if (prev.dir === 'asc') return { key: colKey, dir: 'desc' };
        if (prev.dir === 'desc') return null;
      }
      return { key: colKey, dir: 'asc' };
    });
  }, [frozen]);

  return (
    <div className="wg2-table-block" role="region" aria-label={block.title ?? 'Table block'}>
      <table className="wg2-table-block__table">
        <thead>
          <tr>
            {data.columns.map((col) => {
              const sorted = sort?.key === col.key;
              const ariaSort: 'ascending' | 'descending' | 'none' = sorted
                ? sort!.dir === 'asc' ? 'ascending' : 'descending'
                : 'none';
              return (
                <th key={col.key} aria-sort={ariaSort}>
                  {frozen ? (
                    <span>{col.title}</span>
                  ) : (
                    <button
                      type="button"
                      className="wg2-table-block__sort"
                      onClick={() => handleSort(col.key)}
                      aria-label={`Sort by ${col.title}${sorted ? `, currently ${sort!.dir === 'asc' ? 'ascending' : 'descending'}` : ''}`}
                    >
                      {col.title}
                      <span className="wg2-table-block__sort-icon" aria-hidden="true">
                        {sorted ? (sort!.dir === 'asc' ? ' ▲' : ' ▼') : ' ⇅'}
                      </span>
                    </button>
                  )}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {sortedRows.length === 0 ? (
            <tr>
              <td colSpan={data.columns.length} className="wg2-table-block__empty">
                No rows
              </td>
            </tr>
          ) : (
            sortedRows.map((row, ri) => (
              <tr key={ri}>
                {data.columns.map((col) => (
                  <td key={col.key}>{safeCellText(row[col.key])}</td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
};

export default TableBlock;
