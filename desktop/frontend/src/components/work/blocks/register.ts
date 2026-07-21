// WorkBlock renderer registration for the production singleton.
// Imports validators eagerly (for pre-load validation) and renderers lazily (by kind).
// Registration is idempotent — repeated calls are safe.

import { blockRegistry } from './registry';
import {
  validateArtifactData,
  validateChartData,
  validateCodeData,
  validateGraphData,
  validateMarkdownData,
  validateTableData,
} from './schemaHelpers';

const tableValidator = (_schema: number, data: unknown) => ({ valid: validateTableData(data) });
const chartValidator = (_schema: number, data: unknown) => ({ valid: validateChartData(data) });
const graphValidator = (_schema: number, data: unknown) => ({ valid: validateGraphData(data) });
const codeValidator = (_schema: number, data: unknown) => ({ valid: validateCodeData(data) });
const markdownValidator = (_schema: number, data: unknown) => ({ valid: validateMarkdownData(data) });
const artifactValidator = (_schema: number, data: unknown) => ({ valid: validateArtifactData(data) });

const loadTable = () => import('./TableBlock').then((module) => ({ component: module.default }));
const loadChart = () => import('./ChartBlock').then((module) => ({ component: module.default }));
const loadGraph = () => import('./GraphBlock').then((module) => ({ component: module.default }));
const loadCode = () => import('./CodeBlock').then((module) => ({ component: module.default }));
const loadMarkdown = () => import('./MarkdownBlock').then((module) => ({ component: module.default }));
const loadArtifact = () => import('./ArtifactBlock').then((module) => ({ component: module.default }));

export function registerBuiltinBlocks(): void {
  blockRegistry.register('table', 1, tableValidator, loadTable);
  blockRegistry.register('chart', 1, chartValidator, loadChart);
  blockRegistry.register('graph', 1, graphValidator, loadGraph);
  blockRegistry.register('code', 1, codeValidator, loadCode);
  blockRegistry.register('markdown', 1, markdownValidator, loadMarkdown);
  blockRegistry.register('artifact', 1, artifactValidator, loadArtifact);
}
