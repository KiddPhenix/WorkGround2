// BlockHost validates code-owned renderer contracts before loading a module and
// confines every failure to the current block.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { SyntheticEvent } from 'react';
import type { BlockInstance, BlockStatus } from '../../../work/types';
import { FallbackBlock } from './FallbackBlock';
import { blockRegistry } from './registry';
import type { BlockHostProps, BlockHostState, BlockRendererProps, RendererModule } from './types';

const MAX_RETRIES = 3;
const BLOCK_STATUSES = new Set<BlockStatus>(['loading', 'ready', 'empty', 'stale', 'blocked', 'failed']);
const SECRET_ASSIGNMENT = /\b(secret|token|password|authorization|cookie|api[_-]?key)\b\s*[:=]\s*\S+/gi;

function safeMessage(value: unknown, fallback: string): string {
  if (typeof value !== 'string' || value.trim().length === 0) return fallback;
  return value
    .replace(/[\u0000-\u001f\u007f]/g, ' ')
    .replace(SECRET_ASSIGNMENT, '$1=[redacted]')
    .trim()
    .slice(0, 240);
}

function safeLogValue(value: unknown, fallback: string): string {
  return typeof value === 'string' && /^[a-zA-Z0-9_.:-]{1,128}$/.test(value) ? value : fallback;
}

function validateShell(block: BlockInstance): string | null {
  if (typeof block.kind !== 'string' || block.kind.length === 0 || block.kind !== block.kind.trim()) {
    return 'Block kind is invalid';
  }
  if (!Number.isSafeInteger(block.schemaVersion) || block.schemaVersion <= 0) {
    return 'Block schema version is invalid';
  }
  if (!BLOCK_STATUSES.has(block.status)) return 'Block status is invalid';
  return null;
}

function statusReason(block: BlockInstance): string {
  switch (block.status) {
    case 'empty': return 'Block has no content';
    case 'stale': return 'Block data may be outdated';
    case 'blocked': return 'Block is blocked';
    case 'failed': return 'Block generation failed';
    default: return `Block status: ${block.status}`;
  }
}

const LoadingBlock: React.FC<{ block: BlockInstance }> = ({ block }) => (
  <div className="wg2-block-loading" role="status" aria-live="polite">
    <span aria-hidden="true">⏳</span>
    <span>Loading {typeof block.kind === 'string' ? block.kind : 'block'}…</span>
  </div>
);

interface BoundaryProps {
  block: BlockInstance;
  children: React.ReactNode;
  disabled: boolean;
  retries: number;
  onRetry: () => void;
}

interface BoundaryState {
  crashed: boolean;
}

class BlockErrorBoundary extends React.Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { crashed: false };

  static getDerivedStateFromError(): BoundaryState {
    return { crashed: true };
  }

  componentDidCatch(error: Error) {
    // Keep payloads, error messages, paths, and stacks out of logs.
    console.error('[BlockHost] renderer crashed', {
      blockId: safeLogValue(this.props.block.id, '[invalid]'),
      kind: safeLogValue(this.props.block.kind, '[invalid]'),
      revision: this.props.block.revision,
      errorType: safeLogValue(error?.name, 'Error'),
    });
  }

  render() {
    if (!this.state.crashed) return this.props.children;
    const canRetry = !this.props.disabled && this.props.retries < MAX_RETRIES;
    return (
      <div className="wg2-block-error" role="alert">
        <FallbackBlock
          block={this.props.block}
          reason="Renderer crashed"
          interactiveDisabled={this.props.disabled}
        />
        {canRetry && (
          <button type="button" className="wg2-block-retry" onClick={this.props.onRetry}>
            Retry renderer ({this.props.retries + 1}/{MAX_RETRIES})
          </button>
        )}
      </div>
    );
  }
}

const Renderer: React.FC<{ module: RendererModule; props: BlockRendererProps }> = ({
  module: { component: Component },
  props,
}) => <Component {...props} />;

export const BlockHost: React.FC<BlockHostProps> = ({
  block,
  placement,
  readonly = false,
  archived = false,
  context,
  onAction,
}) => {
  const disabled = readonly || archived;
  const identity = JSON.stringify([block.id, block.kind, block.schemaVersion, block.revision]);
  const [attempt, setAttempt] = useState({ identity, count: 0 });
  const [state, setState] = useState<BlockHostState>({ stage: 'validating' });
  const generation = useRef(0);
  const retries = attempt.identity === identity ? attempt.count : 0;

  useEffect(() => {
    generation.current += 1;
    const current = generation.current;
    const commit = (next: BlockHostState) => {
      if (current === generation.current) setState(next);
    };

    commit({ stage: 'validating' });
    const shellError = validateShell(block);
    if (shellError) {
      commit({ stage: 'invalid_block', reason: shellError });
      return () => { generation.current += 1; };
    }

    if (block.tombstone) {
      commit({ stage: 'status', status: 'Block was removed' });
      return () => { generation.current += 1; };
    }
    if (block.status === 'loading') {
      commit({ stage: 'status', status: 'loading' });
      return () => { generation.current += 1; };
    }
    if (block.status !== 'ready') {
      commit({ stage: 'status', status: statusReason(block) });
      return () => { generation.current += 1; };
    }

    const support = blockRegistry.support(block.kind, block.schemaVersion);
    if (support.status === 'unknown_kind') {
      commit({ stage: 'unknown_kind' });
      return () => { generation.current += 1; };
    }
    if (support.status === 'future_schema') {
      commit({ stage: 'future_schema', maxSupported: support.maxSupported });
      return () => { generation.current += 1; };
    }
    if (support.status === 'unsupported_schema') {
      commit({ stage: 'unsupported_schema' });
      return () => { generation.current += 1; };
    }

    try {
      const result = blockRegistry.validate(block.kind, block.schemaVersion, block.data);
      if (!result?.valid) {
        commit({
          stage: 'invalid_data',
          reason: safeMessage(result?.reason, 'Data does not match the renderer schema'),
        });
        return () => { generation.current += 1; };
      }
    } catch (error) {
      console.error('[BlockHost] renderer validation failed', {
        blockId: safeLogValue(block.id, '[invalid]'),
        kind: safeLogValue(block.kind, '[invalid]'),
        revision: block.revision,
        errorType: safeLogValue(error instanceof Error ? error.name : '', 'Error'),
      });
      commit({ stage: 'invalid_data', reason: 'Renderer data validation failed' });
      return () => { generation.current += 1; };
    }

    commit({ stage: 'loading_module' });
    void blockRegistry.resolve(block.kind, block.schemaVersion).then(
      (module) => {
        if (module) commit({ stage: 'rendering', module });
        else commit({ stage: 'unsupported_schema' });
      },
      (error) => {
        console.error('[BlockHost] renderer import failed', {
          blockId: safeLogValue(block.id, '[invalid]'),
          kind: safeLogValue(block.kind, '[invalid]'),
          revision: block.revision,
          errorType: safeLogValue(error instanceof Error ? error.name : '', 'Error'),
        });
        commit({ stage: 'import_failed' });
      },
    );

    return () => { generation.current += 1; };
  }, [block, identity, retries]);

  const retry = useCallback(() => {
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
    onAction: disabled ? undefined : onAction,
  }), [archived, block, context, disabled, onAction, placement]);

  const stopInteraction = useCallback((event: SyntheticEvent) => {
    if (!disabled) return;
    event.preventDefault();
    event.stopPropagation();
  }, [disabled]);

  if (state.stage === 'validating' || state.stage === 'loading_module' ||
      (state.stage === 'status' && state.status === 'loading')) {
    return <LoadingBlock block={block} />;
  }
  if (state.stage === 'status') {
    return <FallbackBlock block={block} reason={state.status} interactiveDisabled={disabled} />;
  }
  if (state.stage === 'invalid_block') {
    return <FallbackBlock block={block} reason={state.reason} interactiveDisabled={disabled} />;
  }
  if (state.stage === 'unknown_kind') {
    return <FallbackBlock block={block} reason={`Unknown kind: ${safeMessage(block.kind, 'unknown')}`} interactiveDisabled={disabled} />;
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
    return <FallbackBlock block={block} reason={`Unsupported schema v${block.schemaVersion}`} interactiveDisabled={disabled} />;
  }
  if (state.stage === 'invalid_data') {
    return <FallbackBlock block={block} reason={`Invalid data: ${state.reason}`} interactiveDisabled={disabled} />;
  }
  if (state.stage === 'import_failed') {
    const canRetry = !disabled && retries < MAX_RETRIES;
    return (
      <div className="wg2-block-error" role="alert">
        <FallbackBlock block={block} reason="Renderer failed to load" interactiveDisabled={disabled} />
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
      <BlockErrorBoundary
        key={`${identity}:${retries}`}
        block={block}
        disabled={disabled}
        retries={retries}
        onRetry={retry}
      >
        <Renderer module={state.module} props={rendererProps} />
      </BlockErrorBoundary>
    </div>
  );
};

BlockHost.displayName = 'BlockHost';
