// BlockHost validates code-owned renderer contracts before loading a module and
// confines every async, render, effect, event, and action failure to one block.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { SyntheticEvent } from 'react';
import type { BlockInstance, BlockStatus, BlockUpdateRequest } from '../../../work/types';
import { FallbackBlock } from './FallbackBlock';
import { RendererIsland } from './RendererIsland';
import { blockRegistry, isRendererKind } from './registry';
import { registerBuiltinBlocks } from './register';
import { createBlockRenderIdentity, matchesBlockRenderIdentity } from './safeBlockJson';
import type { BlockRenderIdentity } from './safeBlockJson';
import type {
  BlockActionHandler,
  BlockHostProps,
  BlockHostState,
  BlockRendererProps,
  RendererFailureCode,
} from './types';

const MAX_RETRIES = 3;
const BLOCK_STATUSES = new Set<BlockStatus>(['loading', 'ready', 'empty', 'stale', 'blocked', 'failed']);

// Core renderers are code-owned and available wherever BlockHost is used.
// Stable registrations make this safe across duplicate imports and test setup.
registerBuiltinBlocks();

interface ActionGate {
  active: boolean;
  archived: boolean;
  block: BlockInstance;
  blockID: string;
  identity: BlockRenderIdentity;
  onAction?: BlockActionHandler;
  onUpdate?: (request: BlockUpdateRequest) => void | Promise<void>;
  readonly: boolean;
  revision: number;
  status: BlockInstance['status'];
  runID?: string;
  taskID?: string;
  tombstone: boolean;
  workID: string;
}

type TelemetryCode =
  | 'renderer_import_error'
  | 'renderer_validation_error'
  | RendererFailureCode;
type ShellError = 'invalid_kind' | 'invalid_schema' | 'invalid_status';

function safeLogText(value: unknown): string {
  return typeof value === 'string' && /^[a-zA-Z0-9_.:-]{1,128}$/.test(value)
    ? value
    : '[invalid]';
}

function reportFailure(block: BlockInstance, code: TelemetryCode): void {
  try {
    console.error('[BlockHost]', {
      blockID: safeLogText(block.id),
      code,
      kind: isRendererKind(block.kind) ? block.kind : '[invalid]',
      revision: Number.isSafeInteger(block.revision) ? block.revision : -1,
    });
  } catch {
    // Telemetry is best-effort and must never escape into the host tree.
  }
}

function validateShell(block: BlockInstance): ShellError | null {
  if (!isRendererKind(block.kind)) return 'invalid_kind';
  if (!Number.isSafeInteger(block.schemaVersion) || block.schemaVersion <= 0) return 'invalid_schema';
  if (!BLOCK_STATUSES.has(block.status)) return 'invalid_status';
  return null;
}

function statusMessage(status: BlockInstance['status'] | 'removed'): string {
  switch (status) {
    case 'removed': return 'Block was removed';
    case 'empty': return 'Block has no content';
    case 'stale': return 'Block data may be outdated';
    case 'blocked': return 'Block is blocked';
    case 'failed': return 'Block generation failed';
    case 'loading': return 'Block is loading';
    default: return 'Renderer unavailable';
  }
}

function invalidMessage(code: 'invalid_kind' | 'invalid_schema' | 'invalid_status'): string {
  switch (code) {
    case 'invalid_kind': return 'Block kind is invalid';
    case 'invalid_schema': return 'Block schema version is invalid';
    case 'invalid_status': return 'Block status is invalid';
  }
}

const LoadingBlock: React.FC<{ block: BlockInstance }> = ({ block }) => (
  <div className="wg2-block-loading" role="status" aria-live="polite">
    <span aria-hidden="true">⏳</span>
    <span>Loading {isRendererKind(block.kind) ? block.kind : 'block'}…</span>
  </div>
);

function isThenable(value: unknown): value is PromiseLike<unknown> {
  return Boolean(value && (typeof value === 'object' || typeof value === 'function') &&
    typeof (value as PromiseLike<unknown>).then === 'function');
}

export const BlockHost: React.FC<BlockHostProps> = ({
  block,
  placement,
  readonly = false,
  archived = false,
  context,
  onAction,
  onUpdate,
}) => {
  const identityRef = useRef<BlockRenderIdentity | null>(null);
  if (!identityRef.current || !matchesBlockRenderIdentity(identityRef.current, block)) {
    identityRef.current = createBlockRenderIdentity(block);
  }
  const identity = identityRef.current;
  const disabled = readonly || archived;
  const [attempt, setAttempt] = useState({ identity, count: 0 });
  const [state, setState] = useState<BlockHostState>({ identity, stage: 'validating' });
  const generation = useRef(0);
  const renderedIdentity = useRef(identity);
  const gateRef = useRef<ActionGate>({
    active: true,
    archived,
    block,
    blockID: block.id,
    identity,
    onAction,
    onUpdate,
    readonly,
    revision: block.revision,
    status: block.status,
    runID: context.runId,
    taskID: context.taskId,
    tombstone: Boolean(block.tombstone),
    workID: context.workId,
  });

  // Invalidate old async work during render. An old import/crash can otherwise
  // settle between this render and the next effect cleanup.
  if (renderedIdentity.current !== identity) {
    renderedIdentity.current = identity;
    generation.current += 1;
  }
  gateRef.current = {
    active: true,
    archived,
    block,
    blockID: block.id,
    identity,
    onAction,
    onUpdate,
    readonly,
    revision: block.revision,
    status: block.status,
    runID: context.runId,
    taskID: context.taskId,
    tombstone: Boolean(block.tombstone),
    workID: context.workId,
  };

  const retries = attempt.identity === identity ? attempt.count : 0;

  const failRenderer = useCallback((failedIdentity: BlockRenderIdentity, code: RendererFailureCode) => {
    const gate = gateRef.current;
    if (!gate.active || gate.identity !== failedIdentity) return;
    generation.current += 1;
    reportFailure(gate.block, code);
    setState({ identity: failedIdentity, stage: 'renderer_failed', code });
  }, []);

  useEffect(() => {
    gateRef.current.active = true;
    return () => {
      gateRef.current.active = false;
      generation.current += 1;
    };
  }, []);

  useEffect(() => {
    generation.current += 1;
    const current = generation.current;
    const isCurrent = () => current === generation.current && renderedIdentity.current === identity;
    const commit = (next: BlockHostState): boolean => {
      if (!isCurrent() || next.identity !== identity) return false;
      setState(next);
      return true;
    };

    commit({ identity, stage: 'validating' });
    const shellError = validateShell(block);
    if (shellError) {
      commit({ identity, stage: 'invalid_block', code: shellError });
      return () => { generation.current += 1; };
    }

    if (block.tombstone) {
      commit({ identity, stage: 'status', status: 'removed' });
      return () => { generation.current += 1; };
    }
    if (block.status === 'loading') {
      commit({ identity, stage: 'status', status: 'loading' });
      return () => { generation.current += 1; };
    }
    if (block.status !== 'ready') {
      commit({ identity, stage: 'status', status: block.status });
      return () => { generation.current += 1; };
    }

    const support = blockRegistry.support(block.kind, block.schemaVersion);
    if (support.status === 'unknown_kind') {
      commit({ identity, stage: 'unknown_kind' });
      return () => { generation.current += 1; };
    }
    if (support.status === 'future_schema') {
      commit({ identity, stage: 'future_schema', maxSupported: support.maxSupported });
      return () => { generation.current += 1; };
    }
    if (support.status === 'unsupported_schema') {
      commit({ identity, stage: 'unsupported_schema' });
      return () => { generation.current += 1; };
    }

    try {
      const result = blockRegistry.validate(block.kind, block.schemaVersion, block.data);
      if (!result?.valid) {
        commit({ identity, stage: 'invalid_data' });
        return () => { generation.current += 1; };
      }
    } catch {
      reportFailure(block, 'renderer_validation_error');
      commit({ identity, stage: 'invalid_data' });
      return () => { generation.current += 1; };
    }

    commit({ identity, stage: 'loading_module' });
    void blockRegistry.resolve(block.kind, block.schemaVersion).then(
      (module) => {
        if (module) commit({ identity, stage: 'rendering', module });
        else commit({ identity, stage: 'unsupported_schema' });
      },
      () => {
        if (isCurrent()) {
          reportFailure(block, 'renderer_import_error');
          commit({ identity, stage: 'import_failed' });
        }
      },
    );

    return () => { generation.current += 1; };
  }, [identity, retries, blockRegistry]);

  const guardedAction = useMemo<BlockRendererProps['onAction']>(() => {
    const actionIdentity = identity;
    return (request) => {
      const gate = gateRef.current;
      try {
        if (!gate.active || gate.identity !== actionIdentity || gate.readonly || gate.archived ||
            gate.tombstone || gate.status !== 'ready' || typeof gate.onAction !== 'function' ||
            !request || request.blockId !== gate.blockID || request.workId !== gate.workID ||
            (gate.runID !== undefined && request.runId !== gate.runID) ||
            (gate.taskID !== undefined && request.taskId !== gate.taskID) ||
            request.expectedRevision !== gate.revision) return;
      } catch {
        return;
      }
      try {
        const result = gate.onAction(request);
        if (isThenable(result)) {
          return Promise.resolve(result).catch(() => {
            failRenderer(actionIdentity, 'action_callback_error');
            return undefined;
          });
        }
        return result;
      } catch {
        failRenderer(actionIdentity, 'action_callback_error');
        return undefined;
      }
    };
  }, [failRenderer, identity]);

  const guardedUpdate = useMemo<NonNullable<BlockRendererProps['onUpdate']>>(() => {
    const updateIdentity = identity;
    return async (request) => {
      const gate = gateRef.current;
      const callback = gate.onUpdate;
      try {
        if (!gate.active || gate.identity !== updateIdentity || gate.readonly || gate.archived ||
            gate.tombstone || gate.status !== 'ready' || typeof callback !== 'function' ||
            !request || request.blockId !== gate.blockID || request.workId !== gate.workID ||
            request.expectedRevision !== gate.revision) {
          throw new Error('work block update is no longer current');
        }
      } catch {
        throw new Error('work block update was rejected');
      }
      // Update errors intentionally return to the renderer so it can preserve
      // the user's draft and expose a safe retry path.
      await callback(request);
    };
  }, [identity]);

  const retry = useCallback(() => {
    const gate = gateRef.current;
    if (!gate.active || gate.identity !== identity || gate.readonly || gate.archived ||
        gate.tombstone || gate.status !== 'ready') return;
    setAttempt((current) => ({
      identity,
      count: current.identity === identity ? Math.min(current.count + 1, MAX_RETRIES) : 1,
    }));
  }, [identity]);

  const rendererProps = useMemo<BlockRendererProps>(() => ({
    block,
    placement,
    readonly: disabled,
    archived,
    context,
    onAction: guardedAction,
    onUpdate: typeof onUpdate === 'function' ? guardedUpdate : undefined,
  }), [archived, block, context, disabled, guardedAction, guardedUpdate, onUpdate, placement]);

  const stopInteraction = useCallback((event: SyntheticEvent) => {
    if (!disabled) return;
    event.preventDefault();
    event.stopPropagation();
  }, [disabled]);

  // Never paint state that belongs to an older status/data/module identity.
  if (state.identity !== identity || state.stage === 'validating' || state.stage === 'loading_module' ||
      (state.stage === 'status' && state.status === 'loading')) {
    return <LoadingBlock block={block} />;
  }
  if (state.stage === 'status') {
    return <FallbackBlock block={block} reason={statusMessage(state.status)} interactiveDisabled={disabled} />;
  }
  if (state.stage === 'invalid_block') {
    return <FallbackBlock block={block} reason={invalidMessage(state.code)} interactiveDisabled={disabled} />;
  }
  if (state.stage === 'unknown_kind') {
    return <FallbackBlock block={block} reason="Unknown renderer kind" interactiveDisabled={disabled} />;
  }
  if (state.stage === 'future_schema') {
    return (
      <FallbackBlock
        block={block}
        reason={`Future schema v${block.schemaVersion}; supported through v${state.maxSupported}`}
        interactiveDisabled
      />
    );
  }
  if (state.stage === 'unsupported_schema') {
    return <FallbackBlock block={block} reason="Unsupported renderer schema" interactiveDisabled={disabled} />;
  }
  if (state.stage === 'invalid_data') {
    return <FallbackBlock block={block} reason="Invalid data for renderer" interactiveDisabled={disabled} />;
  }
  if (state.stage === 'import_failed' || state.stage === 'renderer_failed') {
    const canRetry = !disabled && !block.tombstone && block.status === 'ready' && retries < MAX_RETRIES;
    const reason = state.stage === 'import_failed' ? 'Renderer failed to load' : 'Renderer failed safely';
    return (
      <div className="wg2-block-error" role="alert" data-error-code={state.stage === 'renderer_failed' ? state.code : 'renderer_import_error'}>
        <FallbackBlock block={block} reason={reason} interactiveDisabled={disabled} />
        {canRetry && (
          <button type="button" className="wg2-block-retry" onClick={retry}>
            Retry renderer ({retries + 1}/{MAX_RETRIES})
          </button>
        )}
      </div>
    );
  }

  return (
    <div
      className="wg2-block-host"
      aria-disabled={disabled || undefined}
      inert={disabled || undefined}
      onClickCapture={stopInteraction}
      onInputCapture={stopInteraction}
      onChangeCapture={stopInteraction}
      onSubmitCapture={stopInteraction}
      onPointerDownCapture={stopInteraction}
    >
      <RendererIsland
        key={`${identity.key}:${retries}`}
        identity={identity}
        module={state.module}
        rendererProps={rendererProps}
        onFailure={failRenderer}
      />
    </div>
  );
};

BlockHost.displayName = 'BlockHost';
