// Shared contracts for the block rendering boundary.

import type {
  ActionReceipt,
  BlockActionRequest,
  BlockInstance,
  BlockPlacement,
  BlockUpdateRequest,
} from '../../../work/types';
import type { BlockRenderIdentity } from './safeBlockJson';

export interface ValidationResult {
  valid: boolean;
  reason?: string;
}

export interface BlockHostContext {
  workId: string;
  workSchemaVersion: number;
  runId?: string;
  taskId?: string;
  actionReceipts?: readonly ActionReceipt[];
}

export type BlockActionHandler = (
  request: BlockActionRequest,
) => ActionReceipt | void | Promise<ActionReceipt | void>;

export interface BlockRendererProps {
  block: BlockInstance;
  placement?: BlockPlacement;
  readonly: boolean;
  archived: boolean;
  context: BlockHostContext;
  onAction?: BlockActionHandler;
  onUpdate?: (request: BlockUpdateRequest) => void | Promise<void>;
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

export type RendererFailureCode =
  | 'action_callback_error'
  | 'renderer_caught_error'
  | 'renderer_event_error'
  | 'renderer_recoverable_error'
  | 'renderer_root_error'
  | 'renderer_uncaught_error';

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
  onAction?: BlockActionHandler;
  onUpdate?: (request: BlockUpdateRequest) => void | Promise<void>;
}

export type BlockHostState =
  | { identity: BlockRenderIdentity; stage: 'validating' }
  | { identity: BlockRenderIdentity; stage: 'loading_module' }
  | { identity: BlockRenderIdentity; stage: 'status'; status: BlockInstance['status'] | 'removed' }
  | { identity: BlockRenderIdentity; stage: 'future_schema'; maxSupported: number }
  | { identity: BlockRenderIdentity; stage: 'unsupported_schema' }
  | { identity: BlockRenderIdentity; stage: 'unknown_kind' }
  | { identity: BlockRenderIdentity; stage: 'invalid_block'; code: 'invalid_kind' | 'invalid_schema' | 'invalid_status' }
  | { identity: BlockRenderIdentity; stage: 'invalid_data' }
  | { identity: BlockRenderIdentity; stage: 'import_failed' }
  | { identity: BlockRenderIdentity; stage: 'renderer_failed'; code: RendererFailureCode }
  | { identity: BlockRenderIdentity; stage: 'rendering'; module: RendererModule };
