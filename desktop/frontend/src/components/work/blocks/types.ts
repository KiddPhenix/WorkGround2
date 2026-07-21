// Shared contracts for the block rendering boundary.

import type { BlockActionRequest, BlockInstance, BlockPlacement } from '../../../work/types';

export interface ValidationResult {
  valid: boolean;
  reason?: string;
}

export interface BlockHostContext {
  workId: string;
  workSchemaVersion: number;
}

export interface BlockRendererProps {
  block: BlockInstance;
  placement?: BlockPlacement;
  readonly: boolean;
  archived: boolean;
  context: BlockHostContext;
  onAction?: (request: BlockActionRequest) => void;
}

export interface RendererModule {
  component: React.ComponentType<BlockRendererProps>;
}

export interface SchemaRange {
  min: number;
  max: number;
}

export type SchemaVersionSpec = number | SchemaRange;
export type RendererValidator = (schemaVersion: number, data: unknown) => ValidationResult;
export type LazyLoader = () => Promise<RendererModule>;

export type RendererSupport =
  | { status: 'supported' }
  | { status: 'unknown_kind' }
  | { status: 'future_schema'; maxSupported: number }
  | { status: 'unsupported_schema' };

export interface BlockRendererRegistry {
  register(
    kind: string,
    versions: SchemaVersionSpec,
    validate: RendererValidator,
    load: LazyLoader,
  ): void;
  support(kind: string, schemaVersion: number): RendererSupport;
  validate(kind: string, schemaVersion: number, data: unknown): ValidationResult | null;
  resolve(kind: string, schemaVersion: number): Promise<RendererModule | null>;
  has(kind: string, schemaVersion: number): boolean;
}

export interface BlockHostProps {
  block: BlockInstance;
  placement?: BlockPlacement;
  readonly?: boolean;
  archived?: boolean;
  context: BlockHostContext;
  onAction?: (request: BlockActionRequest) => void;
}

export type BlockHostState =
  | { stage: 'validating' }
  | { stage: 'loading_module' }
  | { stage: 'status'; status: string }
  | { stage: 'future_schema'; maxSupported: number }
  | { stage: 'unsupported_schema' }
  | { stage: 'unknown_kind' }
  | { stage: 'invalid_block'; reason: string }
  | { stage: 'invalid_data'; reason: string }
  | { stage: 'import_failed' }
  | { stage: 'rendering'; module: RendererModule };
