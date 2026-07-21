// Work blocks — rendering system (Phase 4).
//
// Public API surface:
//   BlockHost          — render any BlockInstance with registry lookup
//   FallbackBlock      — last-resort safe text renderer
//   blockRegistry      — shared production singleton
//   createRegistry     — isolated instance for tests
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
  BlockRendererRegistry,
  LazyLoader,
  ValidationResult,
  BlockHostState,
} from './types';
