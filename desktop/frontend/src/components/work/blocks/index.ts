// Work blocks — rendering system (Phase 4).
//
// Public API surface:
//   BlockHost          — render any BlockInstance with registry lookup
//   FallbackBlock      — last-resort safe text renderer
//   blockRegistry      — shared production singleton
//   createRegistry     — isolated instance for tests
//   registerCoreRenderers — register all V1 core block kinds
//
// Types:
//   BlockHostProps, BlockHostContext, BlockRendererProps,
//   RendererModule, SchemaRange, SchemaVersionSpec, LazyLoader,
//   ValidationResult

export { BlockHost } from './BlockHost';
export { FallbackBlock } from './FallbackBlock';
export { blockRegistry, createRegistry, isRendererKind } from './registry';
export { registerCoreRenderers } from './register';
export type { FallbackBlockProps } from './FallbackBlock';
export type {
  BlockHostProps,
  BlockHostContext,
  BlockRendererProps,
  RendererModule,
  SchemaRange,
  SchemaVersionSpec,
  RendererValidator,
  RendererSupport,
  RendererFailureCode,
  BlockRendererRegistry,
  LazyLoader,
  ValidationResult,
  BlockHostState,
} from './types';
export {
  validateChecklist,
  validateFileList,
  validateGitStatus,
  validateItemList,
  validateKeyValue,
  validateProgress,
} from './schemas';
export type {
  ItemState,
  ListItem,
  ItemListData,
  ChecklistItem,
  ChecklistData,
  FileStatus,
  FileEntry,
  FileListData,
  ChangeType,
  GitChange,
  GitStatusData,
  KVState,
  KVItem,
  KeyValueData,
  ProgressState,
  ProgressItem,
  ProgressData,
} from './schemas';
