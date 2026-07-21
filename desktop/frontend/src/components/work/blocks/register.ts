// Registers every code-owned V1 WorkBlock renderer in the production registry.
// Validators load eagerly; renderer modules stay lazy. Module-scope function
// identities make repeated registration exact, idempotent, and retryable.

import { blockRegistry } from './registry';
import type { RendererModule } from './types';
import {
  validateChecklist,
  validateFileList,
  validateGitStatus,
  validateItemList,
  validateKeyValue,
  validateProgress,
} from './schemas';
import {
  validateArtifactData,
  validateChartData,
  validateCodeData,
  validateGraphData,
  validateMarkdownData,
  validateTableData,
} from './schemaHelpers';

function lazy(importer: () => Promise<RendererModule>): () => Promise<RendererModule> {
  let pending: Promise<RendererModule> | null = null;
  let cached: RendererModule | null = null;
  return () => {
    if (cached) return Promise.resolve(cached);
    if (pending) return pending;
    pending = Promise.resolve()
      .then(importer)
      .then((module) => {
        cached = module;
        return module;
      })
      .finally(() => {
        pending = null;
      });
    return pending;
  };
}

const tableValidator = (_schema: number, data: unknown) => ({ valid: validateTableData(data) });
const chartValidator = (_schema: number, data: unknown) => ({ valid: validateChartData(data) });
const graphValidator = (_schema: number, data: unknown) => ({ valid: validateGraphData(data) });
const codeValidator = (_schema: number, data: unknown) => ({ valid: validateCodeData(data) });
const markdownValidator = (_schema: number, data: unknown) => ({ valid: validateMarkdownData(data) });
const artifactValidator = (_schema: number, data: unknown) => ({ valid: validateArtifactData(data) });

const loadItemList = lazy(() => import('./ItemListBlock').then((module) => ({ component: module.ItemListBlock })));
const loadChecklist = lazy(() => import('./ChecklistBlock').then((module) => ({ component: module.ChecklistBlock })));
const loadFileList = lazy(() => import('./FileListBlock').then((module) => ({ component: module.FileListBlock })));
const loadGitStatus = lazy(() => import('./GitStatusBlock').then((module) => ({ component: module.GitStatusBlock })));
const loadKeyValue = lazy(() => import('./KeyValueBlock').then((module) => ({ component: module.KeyValueBlock })));
const loadProgress = lazy(() => import('./ProgressBlock').then((module) => ({ component: module.ProgressBlock })));
const loadTable = lazy(() => import('./TableBlock').then((module) => ({ component: module.default })));
const loadChart = lazy(() => import('./ChartBlock').then((module) => ({ component: module.default })));
const loadGraph = lazy(() => import('./GraphBlock').then((module) => ({ component: module.default })));
const loadCode = lazy(() => import('./CodeBlock').then((module) => ({ component: module.default })));
const loadMarkdown = lazy(() => import('./MarkdownBlock').then((module) => ({ component: module.default })));
const loadArtifact = lazy(() => import('./ArtifactBlock').then((module) => ({ component: module.default })));

export function registerBuiltinBlocks(): void {
  blockRegistry.register('item', 1, validateItemList, loadItemList);
  blockRegistry.register('list', 1, validateItemList, loadItemList);
  blockRegistry.register('checklist', 1, validateChecklist, loadChecklist);
  blockRegistry.register('file_list', 1, validateFileList, loadFileList);
  blockRegistry.register('git_status', 1, validateGitStatus, loadGitStatus);
  blockRegistry.register('key_value', 1, validateKeyValue, loadKeyValue);
  blockRegistry.register('status', 1, validateKeyValue, loadKeyValue);
  blockRegistry.register('progress', 1, validateProgress, loadProgress);
  blockRegistry.register('timeline', 1, validateProgress, loadProgress);
  blockRegistry.register('table', 1, tableValidator, loadTable);
  blockRegistry.register('chart', 1, chartValidator, loadChart);
  blockRegistry.register('graph', 1, graphValidator, loadGraph);
  blockRegistry.register('code', 1, codeValidator, loadCode);
  blockRegistry.register('markdown', 1, markdownValidator, loadMarkdown);
  blockRegistry.register('artifact', 1, artifactValidator, loadArtifact);
}
