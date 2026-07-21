// Work blocks — rendering system (Phase 4).
//
// Public API surface:
//   BlockHost          — render any BlockInstance with registry lookup
//   FallbackBlock      — last-resort safe text renderer
//   blockRegistry      — shared production singleton
//   createRegistry     — isolated instance for tests
//   registerBuiltinBlocks — register all V1 core block kinds
//
// Types:
//   BlockHostProps, BlockHostContext, BlockRendererProps,
//   RendererModule, SchemaRange, SchemaVersionSpec, LazyLoader,
//   ValidationResult

export { BlockHost } from './BlockHost';
export { FallbackBlock } from './FallbackBlock';
export { blockRegistry, createRegistry, isRendererKind } from './registry';
export { registerBuiltinBlocks } from './register';
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
  BlockActionHandler,
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
export {
  validateActionEntryData,
  validateApprovalData,
  validateArtifactData,
  validateChartData,
  validateCodeData,
  validateDecisionData,
  validateGraphData,
  validateInputData,
  validateMarkdownData,
  validateNoticeData,
  validateTableData,
} from './schemaHelpers';
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
export type {
  SafeActionEntryData,
  SafeApprovalData,
  SafeArtifactData,
  SafeChartData,
  SafeCodeData,
  SafeDecisionData,
  SafeGraphData,
  SafeInputData,
  SafeMarkdownData,
  SafeNoticeData,
  SafeTableData,
  SafeColumn,
} from './schemaHelpers';
