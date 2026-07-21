// Core renderer registration — imports and registers every V1 core block kind
// into the shared blockRegistry singleton. BlockHost calls it at module load.
// Renderers are lazy-loaded so the initial bundle stays small.

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

function lazy<T>(importer: () => Promise<T>): () => Promise<T> {
  let pending: Promise<T> | null = null;
  let cached: T | null = null;
  return () => {
    if (cached) return Promise.resolve(cached);
    if (pending) return pending;
    pending = Promise.resolve()
      .then(() => importer())
      .then((mod) => {
        cached = mod;
        return mod;
      })
      .finally(() => {
        pending = null;
      });
    return pending;
  };
}

const loadItemList = lazy<RendererModule>(
  () => import('./ItemListBlock').then((m) => ({ component: m.ItemListBlock })),
);
const loadChecklist = lazy<RendererModule>(
  () => import('./ChecklistBlock').then((m) => ({ component: m.ChecklistBlock })),
);
const loadFileList = lazy<RendererModule>(
  () => import('./FileListBlock').then((m) => ({ component: m.FileListBlock })),
);
const loadGitStatus = lazy<RendererModule>(
  () => import('./GitStatusBlock').then((m) => ({ component: m.GitStatusBlock })),
);
const loadKeyValue = lazy<RendererModule>(
  () => import('./KeyValueBlock').then((m) => ({ component: m.KeyValueBlock })),
);
const loadProgress = lazy<RendererModule>(
  () => import('./ProgressBlock').then((m) => ({ component: m.ProgressBlock })),
);

export function registerCoreRenderers(): void {
  // item / list — both aliases use the same renderer
  blockRegistry.register('item', 1, validateItemList, loadItemList);
  blockRegistry.register('list', 1, validateItemList, loadItemList);

  // checklist
  blockRegistry.register('checklist', 1, validateChecklist, loadChecklist);

  // file_list
  blockRegistry.register('file_list', 1, validateFileList, loadFileList);

  // git_status
  blockRegistry.register('git_status', 1, validateGitStatus, loadGitStatus);

  // key_value / status — both aliases use the same renderer
  blockRegistry.register('key_value', 1, validateKeyValue, loadKeyValue);
  blockRegistry.register('status', 1, validateKeyValue, loadKeyValue);

  // progress / timeline — both aliases use the same renderer
  blockRegistry.register('progress', 1, validateProgress, loadProgress);
  blockRegistry.register('timeline', 1, validateProgress, loadProgress);
}
