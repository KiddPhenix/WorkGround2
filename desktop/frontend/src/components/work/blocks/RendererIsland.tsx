// Each renderer runs in its own React root. Render/effect failures use React 19
// root callbacks; synchronous event failures are attributed by a tiny native
// event broker because React reports event-handler exceptions globally.

import React, { useEffect, useRef } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { BlockRendererProps, RendererFailureCode, RendererModule } from './types';

interface Broker {
  active: symbol | null;
  handlers: Map<symbol, (code: RendererFailureCode) => void>;
  onError: (event: ErrorEvent) => void;
}

const EVENT_TYPES = [
  'beforeinput', 'change', 'click', 'contextmenu', 'dblclick', 'dragend', 'drop',
  'input', 'keydown', 'keyup', 'mousedown', 'mouseup', 'pointerdown', 'pointerup',
  'submit', 'touchend', 'touchstart',
] as const;
const brokers = new WeakMap<Window, Broker>();

interface BoundaryProps {
  children: React.ReactNode;
  onFailure: () => void;
}

interface BoundaryState {
  failed: boolean;
}

class IslandBoundary extends React.Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { failed: false };

  static getDerivedStateFromError(): BoundaryState {
    return { failed: true };
  }

  componentDidCatch(): void {
    try {
      this.props.onFailure();
    } catch {
      // The host failure callback is fail-safe by contract.
    }
  }

  render(): React.ReactNode {
    return this.state.failed ? null : this.props.children;
  }
}

function safeCall(callback: ((code: RendererFailureCode) => void) | undefined, code: RendererFailureCode): void {
  try {
    callback?.(code);
  } catch {
    // Root/error callbacks must never become a second uncaught error.
  }
}

function getBroker(owner: Window): Broker {
  const existing = brokers.get(owner);
  if (existing) return existing;
  const broker: Broker = {
    active: null,
    handlers: new Map(),
    onError: () => undefined,
  };
  broker.onError = (event) => {
    const callback = broker.active ? broker.handlers.get(broker.active) : undefined;
    if (!callback) return;
    try {
      event.preventDefault();
      event.stopImmediatePropagation();
    } catch {
      // Reporting still proceeds when the host ErrorEvent is unusual.
    }
    safeCall(callback, 'renderer_event_error');
  };
  owner.addEventListener('error', broker.onError, true);
  brokers.set(owner, broker);
  return broker;
}

function registerEventScope(
  container: HTMLElement,
  token: symbol,
  onFailure: (code: RendererFailureCode) => void,
): () => void {
  const owner = container.ownerDocument.defaultView;
  if (!owner) return () => undefined;
  const broker = getBroker(owner);
  broker.handlers.set(token, onFailure);

  const activate = () => {
    broker.active = token;
    queueMicrotask(() => {
      if (broker.active === token) broker.active = null;
    });
  };
  for (const type of EVENT_TYPES) container.addEventListener(type, activate, true);

  return () => {
    for (const type of EVENT_TYPES) container.removeEventListener(type, activate, true);
    if (broker.active === token) broker.active = null;
    broker.handlers.delete(token);
    if (broker.handlers.size === 0) {
      owner.removeEventListener('error', broker.onError, true);
      brokers.delete(owner);
    }
  };
}

export interface RendererIslandProps {
  identity: string;
  module: RendererModule;
  rendererProps: BlockRendererProps;
  onFailure: (identity: string, code: RendererFailureCode) => void;
}

export const RendererIsland: React.FC<RendererIslandProps> = ({
  identity,
  module: { component: Component },
  rendererProps,
  onFailure,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const rootRef = useRef<Root | null>(null);
  const activeRef = useRef(false);
  const lifecycleRef = useRef(0);
  const failureRef = useRef<(code: RendererFailureCode) => void>(() => undefined);
  failureRef.current = (code) => {
    if (!activeRef.current) return;
    safeCall((failure) => onFailure(identity, failure), code);
  };

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const lifecycle = ++lifecycleRef.current;
    activeRef.current = true;
    let root = rootRef.current;
    const token = Symbol(identity);
    const unregister = registerEventScope(container, token, (code) => failureRef.current(code));

    if (!root) {
      try {
        root = createRoot(container, {
          identifierPrefix: `wg2-${identity.replace(/[^a-zA-Z0-9_-]/g, '-')}-`,
          onCaughtError: () => failureRef.current('renderer_caught_error'),
          onRecoverableError: () => failureRef.current('renderer_recoverable_error'),
          onUncaughtError: () => failureRef.current('renderer_uncaught_error'),
        });
        rootRef.current = root;
      } catch {
        failureRef.current('renderer_root_error');
      }
    }

    return () => {
      activeRef.current = false;
      unregister();
      // Parent-effect cleanup runs during another React commit. Defer the nested
      // root unmount so React never unmounts one root while another is rendering.
      // StrictMode's immediate setup cancels this disposal and reuses the root.
      queueMicrotask(() => {
        if (lifecycleRef.current !== lifecycle) return;
        if (rootRef.current === root) rootRef.current = null;
        try {
          root?.unmount();
        } catch {
          // Cleanup remains fail-closed and cannot take down the parent root.
        }
      });
    };
  }, [Component, identity, onFailure]);

  useEffect(() => {
    const current = rootRef.current;
    if (!current) return;
    try {
      current.render(
        <IslandBoundary onFailure={() => safeCall((code) => onFailure(identity, code), 'renderer_caught_error')}>
          <Component {...rendererProps} />
        </IslandBoundary>,
      );
    } catch {
      safeCall((code) => onFailure(identity, code), 'renderer_root_error');
    }
  }, [Component, identity, onFailure, rendererProps]);

  return <div ref={containerRef} className="wg2-renderer-island" data-block-identity={identity} />;
};

RendererIsland.displayName = 'RendererIsland';
