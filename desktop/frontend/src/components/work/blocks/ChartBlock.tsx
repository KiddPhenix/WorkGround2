// Chart WorkBlock renderer (kind: chart, schema v1).
// Pure React + SVG (bar/line/pie), no third-party chart dependency.
// Includes accessible data table. Archived/readonly renders frozen snapshot.

import React, { useId, useMemo } from 'react';
import type { BlockRendererProps } from './types';
import { validateChartData } from './schemaHelpers';
import type { SafeChartData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  if (schemaVersion !== 1) return { valid: false, reason: `unsupported schema version ${schemaVersion}` };
  if (!validateChartData(data)) return { valid: false, reason: 'invalid chart data: requires {type:bar|line|pie, series:[{label,values}]}' };
  return { valid: true };
}

// ── SVG helpers ──────────────────────────────────────────────────────────────

interface ChartDims { w: number; h: number; pad: { top: number; right: number; bottom: number; left: number } }

const DIMS: ChartDims = { w: 600, h: 300, pad: { top: 16, right: 16, bottom: 40, left: 56 } };
const AXIS_COLOR = 'var(--fg-faint, #858b96)';
const GRID_COLOR = 'var(--border-soft, #252a34)';
const SERIES_COLORS = [
  'var(--accent, #d97757)',
  'var(--ok, #74b87a)',
  'var(--warn, #d9a441)',
  '#6ba3d6',
  '#b88cd6',
  '#d67a9e',
  '#5fc4c4',
  '#d6b56b',
];

function seriesColor(i: number): string { return SERIES_COLORS[i % SERIES_COLORS.length]; }

function legendPosition(index: number, dims: ChartDims): { x: number; y: number } {
  return {
    x: dims.pad.left + (index % 4) * 130,
    y: dims.h - dims.pad.bottom + 30 + Math.floor(index / 4) * 18,
  };
}

function extent(values: number[]): [number, number] {
  let min = Infinity;
  let max = -Infinity;
  for (const v of values) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  if (!Number.isFinite(min)) return [0, 0];
  // Ensure a non-zero range for edge cases (constant/zero/single point)
  if (min === max) {
    if (min === 0) return [0, 1];
    const margin = Math.max(1, Math.abs(min) * 0.1);
    return [Math.min(0, min - margin), Math.max(0, max + margin)];
  }
  const padding = Math.max(1, (max - min) * 0.05);
  return [min - padding, max + padding];
}

function xScale(i: number, count: number, padLeft: number, plotW: number): number {
  if (count <= 1) return padLeft + plotW / 2;
  return padLeft + (i / (count - 1)) * plotW;
}

function yScale(v: number, min: number, max: number, padTop: number, plotH: number): number {
  const range = max - min || 1;
  return padTop + plotH - ((v - min) / range) * plotH;
}

// ── SVG Bar Chart ────────────────────────────────────────────────────────────

function BarChart({ data, dims }: { data: SafeChartData; dims: ChartDims }) {
  const series0 = data.series[0];
  const values = series0.values;
  const labels = data.axes?.x?.labels ?? values.map((_, i) => String(i + 1));
  const [dataMin, dataMax] = extent(data.series.flatMap((serie) => serie.values));
  const yMin = Math.min(0, dataMin);
  const yMax = Math.max(0, dataMax);
  const plotW = dims.w - dims.pad.left - dims.pad.right;
  const plotH = dims.h - dims.pad.top - dims.pad.bottom;
  const groupWidth = plotW / values.length;
  const barGap = Math.min(2, groupWidth * 0.04);
  const barWidth = Math.max(0.25, Math.min(40, (groupWidth - 2 - barGap * (data.series.length - 1)) / data.series.length));
  const clusterWidth = barWidth * data.series.length + barGap * (data.series.length - 1);
  const baselineY = yScale(0, yMin, yMax, dims.pad.top, plotH);

  // Y-axis ticks
  const yTicks = 5;
  const yTickStep = (yMax - yMin) / (yTicks - 1);

  return (
    <g>
      {/* Y axis line */}
      <line x1={dims.pad.left} y1={dims.pad.top} x2={dims.pad.left} y2={dims.h - dims.pad.bottom} stroke={AXIS_COLOR} strokeWidth={1} />
      {/* X axis line */}
      <line x1={dims.pad.left} y1={dims.h - dims.pad.bottom} x2={dims.w - dims.pad.right} y2={dims.h - dims.pad.bottom} stroke={AXIS_COLOR} strokeWidth={1} />

      {/* Y ticks */}
      {Array.from({ length: yTicks }, (_, ti) => {
        const val = yMin + ti * yTickStep;
        const y = yScale(val, yMin, yMax, dims.pad.top, plotH);
        return (
          <g key={`ytick-${ti}`}>
            <line x1={dims.pad.left - 4} y1={y} x2={dims.pad.left} y2={y} stroke={AXIS_COLOR} strokeWidth={1} />
            <text x={dims.pad.left - 8} y={y + 4} textAnchor="end" fill={AXIS_COLOR} fontSize={11}>
              {Number.isInteger(val) ? val : val.toFixed(1)}
            </text>
            <line x1={dims.pad.left} y1={y} x2={dims.w - dims.pad.right} y2={y} stroke={GRID_COLOR} strokeWidth={0.5} />
          </g>
        );
      })}

      {/* Bars */}
      {data.series.map((serie, si) =>
        serie.values.map((v, vi) => {
          const groupStart = dims.pad.left + vi * groupWidth + (groupWidth - clusterWidth) / 2;
          const x = groupStart + si * (barWidth + barGap);
          const valueY = yScale(v, yMin, yMax, dims.pad.top, plotH);
          const y = Math.min(valueY, baselineY);
          const barH = Math.abs(baselineY - valueY);
          return (
            <rect
              key={`bar-${si}-${vi}`}
              data-chart-mark="bar"
              x={x}
              y={y}
              width={barWidth}
              height={barH}
              fill={seriesColor(si)}
              opacity={0.85}
            >
              <title>{serie.label}: {v}{labels[vi] ? ` (${labels[vi]})` : ''}</title>
            </rect>
          );
        }),
      )}

      {/* X labels */}
      {labels.map((label, i) => {
        const x = dims.pad.left + (i + 0.5) * groupWidth;
        return (
          <text key={`xlabel-${i}`} x={x} y={dims.h - dims.pad.bottom + 16} textAnchor="middle" fill={AXIS_COLOR} fontSize={11}>
            {label.length > 16 ? label.slice(0, 15) + '…' : label}
          </text>
        );
      })}

      {/* Legend */}
      {(data.legend !== false && data.series.length > 1) && data.series.map((serie, si) => {
        const position = legendPosition(si, dims);
        return (
          <g key={`legend-${si}`} transform={`translate(${position.x}, ${position.y})`}>
            <rect width={10} height={10} fill={seriesColor(si)} opacity={0.85} />
            <text x={14} y={9} fill={AXIS_COLOR} fontSize={11}>{serie.label}</text>
          </g>
        );
      })}
      {data.axes?.y?.label && (
        <text transform={`translate(14 ${dims.pad.top + plotH / 2}) rotate(-90)`} textAnchor="middle" fill={AXIS_COLOR} fontSize={11}>
          {data.axes.y.label}
        </text>
      )}
    </g>
  );
}

// ── SVG Line Chart ───────────────────────────────────────────────────────────

function LineChart({ data, dims }: { data: SafeChartData; dims: ChartDims }) {
  const allValues = data.series.flatMap((s) => s.values);
  const labels = data.axes?.x?.labels ?? data.series[0].values.map((_, i) => String(i + 1));
  const [yMin, yMax] = extent(allValues);
  const plotW = dims.w - dims.pad.left - dims.pad.right;
  const plotH = dims.h - dims.pad.top - dims.pad.bottom;
  const pointCount = data.series[0].values.length;

  const yTicks = 5;
  const yTickStep = (yMax - yMin) / (yTicks - 1);

  return (
    <g>
      <line x1={dims.pad.left} y1={dims.pad.top} x2={dims.pad.left} y2={dims.h - dims.pad.bottom} stroke={AXIS_COLOR} strokeWidth={1} />
      <line x1={dims.pad.left} y1={dims.h - dims.pad.bottom} x2={dims.w - dims.pad.right} y2={dims.h - dims.pad.bottom} stroke={AXIS_COLOR} strokeWidth={1} />

      {Array.from({ length: yTicks }, (_, ti) => {
        const val = yMin + ti * yTickStep;
        const y = yScale(val, yMin, yMax, dims.pad.top, plotH);
        return (
          <g key={`ytick-${ti}`}>
            <line x1={dims.pad.left - 4} y1={y} x2={dims.pad.left} y2={y} stroke={AXIS_COLOR} strokeWidth={1} />
            <text x={dims.pad.left - 8} y={y + 4} textAnchor="end" fill={AXIS_COLOR} fontSize={11}>
              {Number.isInteger(val) ? val : val.toFixed(1)}
            </text>
            <line x1={dims.pad.left} y1={y} x2={dims.w - dims.pad.right} y2={y} stroke={GRID_COLOR} strokeWidth={0.5} />
          </g>
        );
      })}

      {data.series.map((serie, si) => {
        const points = serie.values.map((v, i) =>
          `${xScale(i, pointCount, dims.pad.left, plotW)},${yScale(v, yMin, yMax, dims.pad.top, plotH)}`,
        ).join(' ');
        const color = seriesColor(si);
        return (
          <g key={`line-${si}`}>
            <polyline data-chart-mark="line" points={points} fill="none" stroke={color} strokeWidth={2} />
            {serie.values.map((v, i) => (
              <circle
                key={`pt-${si}-${i}`}
                cx={xScale(i, pointCount, dims.pad.left, plotW)}
                cy={yScale(v, yMin, yMax, dims.pad.top, plotH)}
                r={3}
                fill={color}
              >
                <title>{serie.label}: {v}{labels[i] ? ` (${labels[i]})` : ''}</title>
              </circle>
            ))}
          </g>
        );
      })}

      {labels.map((label, i) => {
        const x = xScale(i, pointCount, dims.pad.left, plotW);
        return (
          <text key={`xlabel-${i}`} x={x} y={dims.h - dims.pad.bottom + 16} textAnchor="middle" fill={AXIS_COLOR} fontSize={11}>
            {label.length > 16 ? label.slice(0, 15) + '…' : label}
          </text>
        );
      })}

      {(data.legend !== false && data.series.length > 1) && data.series.map((serie, si) => {
        const position = legendPosition(si, dims);
        return (
          <g key={`legend-${si}`} transform={`translate(${position.x}, ${position.y})`}>
            <line x1={0} y1={5} x2={16} y2={5} stroke={seriesColor(si)} strokeWidth={2} />
            <text x={20} y={9} fill={AXIS_COLOR} fontSize={11}>{serie.label}</text>
          </g>
        );
      })}
      {data.axes?.y?.label && (
        <text transform={`translate(14 ${dims.pad.top + plotH / 2}) rotate(-90)`} textAnchor="middle" fill={AXIS_COLOR} fontSize={11}>
          {data.axes.y.label}
        </text>
      )}
    </g>
  );
}

// ── SVG Pie Chart ────────────────────────────────────────────────────────────

function PieChart({ data, dims }: { data: SafeChartData; dims: ChartDims }) {
  const cx = dims.w / 2;
  const cy = dims.h / 2 - 10;
  const radius = Math.min(cx, cy) - 20;
  const values = data.series[0].values;
  const total = values.reduce((sum, value) => sum + value, 0);

  if (total === 0) {
    return (
      <g>
        <circle cx={cx} cy={cy} r={radius} fill="none" stroke={GRID_COLOR} strokeWidth={2} />
        <text x={cx} y={cy + 4} textAnchor="middle" fill={AXIS_COLOR} fontSize={12}>No positive values</text>
      </g>
    );
  }

  let startAngle = -Math.PI / 2;
  const slices: { label: string; value: number; pct: number; path: string }[] = [];

  for (let i = 0; i < values.length; i++) {
    const v = values[i];
    const pct = v / total;
    const angle = pct * Math.PI * 2;
    const endAngle = startAngle + angle;

    const x1 = cx + radius * Math.cos(startAngle);
    const y1 = cy + radius * Math.sin(startAngle);
    const x2 = cx + radius * Math.cos(endAngle);
    const y2 = cy + radius * Math.sin(endAngle);
    const largeArc = angle > Math.PI ? 1 : 0;

    const path = `M ${cx} ${cy} L ${x1} ${y1} A ${radius} ${radius} 0 ${largeArc} 1 ${x2} ${y2} Z`;

    const label = data.axes?.x?.labels?.[i] ?? data.series[0].label ?? `Slice ${i + 1}`;
    slices.push({ label, value: v, pct, path });
    startAngle = endAngle;
  }

  return (
    <g>
      {slices.map((slice, i) => (
        <path data-chart-mark="pie" key={`slice-${i}`} d={slice.path} fill={seriesColor(i)} opacity={0.85} stroke={dims.w > 300 ? 'var(--bg, #090a0c)' : 'none'} strokeWidth={1}>
          <title>{slice.label}: {slice.value} ({(slice.pct * 100).toFixed(1)}%)</title>
        </path>
      ))}

      {/* Legend */}
      {data.legend !== false && slices.map((slice, i) => {
        const lx = dims.pad.left + (i % 3) * 180;
        const ly = dims.h - dims.pad.bottom + 12 + Math.floor(i / 3) * 18;
        return (
          <g key={`legend-${i}`} transform={`translate(${lx}, ${ly})`}>
            <rect width={10} height={10} fill={seriesColor(i)} opacity={0.85} />
            <text x={14} y={9} fill={AXIS_COLOR} fontSize={11}>
              {slice.label.length > 20 ? slice.label.slice(0, 19) + '…' : slice.label} ({(slice.pct * 100).toFixed(0)}%)
            </text>
          </g>
        );
      })}
    </g>
  );
}

// ── Accessible Data Table ────────────────────────────────────────────────────

function DataTable({ data }: { data: SafeChartData }) {
  const labels = data.axes?.x?.labels ?? data.series[0].values.map((_, i) => String(i + 1));
  return (
    <table className="wg2-chart-block__data-table">
      <caption className="wg2-chart-block__data-caption">Chart data table</caption>
      <thead>
        <tr>
          <th>Series</th>
          {labels.map((label, i) => <th key={i}>{label}</th>)}
        </tr>
      </thead>
      <tbody>
        {data.series.map((serie, si) => (
          <tr key={si}>
            <td>{serie.label}</td>
            {serie.values.map((v, vi) => <td key={vi}>{v}</td>)}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ── Main Component ──────────────────────────────────────────────────────────

const ChartBlock: React.FC<BlockRendererProps> = ({ block }) => {
  const data = block.data as SafeChartData;
  const reactId = useId().replace(/[^a-zA-Z0-9_-]/g, '-');
  const titleId = `chart-title-${reactId}`;
  const descId = `chart-desc-${reactId}`;
  const dims = useMemo<ChartDims>(() => {
    const legendItems = data.type === 'pie'
      ? data.series[0].values.length
      : data.series.length > 1 ? data.series.length : 0;
    const columns = data.type === 'pie' ? 3 : 4;
    const rows = data.legend === false ? 0 : Math.ceil(legendItems / columns);
    const bottom = Math.max(DIMS.pad.bottom, 28 + rows * 18);
    return { ...DIMS, pad: { ...DIMS.pad, bottom } };
  }, [data]);

  const chartDesc = useMemo(() => {
    const parts = [`${data.type} chart with ${data.series.length} series`];
    for (const s of data.series) parts.push(`${s.label}: ${s.values.length} values`);
    return parts.join('; ');
  }, [data]);

  return (
    <div className="wg2-chart-block" role="region" aria-label={block.title ?? 'Chart block'}>
      <svg
        viewBox={`0 0 ${dims.w} ${dims.h}`}
        className="wg2-chart-block__svg"
        role="img"
        aria-labelledby={`${titleId} ${descId}`}
        preserveAspectRatio="xMidYMid meet"
      >
        <title id={titleId}>{data.type} chart</title>
        <desc id={descId}>{chartDesc}</desc>
        {data.type === 'bar' && <BarChart data={data} dims={dims} />}
        {data.type === 'line' && <LineChart data={data} dims={dims} />}
        {data.type === 'pie' && <PieChart data={data} dims={dims} />}
      </svg>
      <details className="wg2-chart-block__data">
        <summary>View data table</summary>
        <DataTable data={data} />
      </details>
    </div>
  );
};

export default ChartBlock;
