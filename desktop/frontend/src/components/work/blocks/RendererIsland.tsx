// Each renderer runs in its own React root. Render/effect failures use React 19
// root callbacks; a generation-scoped event-frame stack attributes synchronous
// browser event failures without claiming unrelated global errors.

import React, { useEffect, useRef } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { BlockRenderIdentity } from './safeBlockJson';
import type { BlockRendererProps, RendererFailureCode, RendererModule } from './types';

interface EventOwner {
  active: boolean;
  generation: number;
  identity: BlockRenderIdentity;
  onFailure: (code: RendererFailureCode) => void;
  token: symbol;
}

interface EventFrame {
  event: Event;
  generation: number;
  owner: EventOwner;
  ownerIdentity: BlockRenderIdentity;
  ownerToken: symbol;
  removed: boolean;
}

interface Broker {
  frames: EventFrame[];
  onError: (event: ErrorEvent) => void;
  refs: number;
}

interface EventScope {
  activate(generation: number, onFailure: (code: RendererFailureCode) => void): void;
  attachCleanup(): void;
  deactivate(generation: number): void;
  dispose(): void;
}

const EVENT_TYPES = [
  'auxclick', 'beforeinput', 'blur', 'change', 'click', 'compositionend',
  'compositionstart', 'compositionupdate', 'contextmenu', 'copy', 'cut',
  'dblclick', 'drag', 'dragend', 'dragenter', 'dragleave', 'dragover',
  'dragstart', 'drop', 'focus', 'focusin', 'focusout', 'gotpointercapture',
  'input', 'invalid', 'keydown', 'keypress', 'keyup', 'lostpointercapture',
  'mousedown', 'mouseenter', 'mouseleave', 'mousemove', 'mouseout', 'mouseover',
  'mouseup', 'paste', 'pointercancel', 'pointerdown', 'pointerenter',
  'pointerleave', 'pointermove', 'pointerout', 'pointerover', 'pointerup',
  'pointerrawupdate', 'reset', 'select', 'submit', 'touchcancel', 'touchend',
  'touchmove', 'touchstart', 'wheel',
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
    // Broker, root, and logger callbacks cannot become a second uncaught error.
  }
}

function removeFrame(broker: Broker, frame: EventFrame): void {
  if (frame.removed) return;
  frame.removed = true;
  const index = broker.frames.lastIndexOf(frame);
  if (index >= 0) broker.frames.splice(index, 1);
}

function removeOwnerFrames(broker: Broker, owner: EventOwner, generation?: number): void {
  for (let index = broker.frames.length - 1; index >= 0; index--) {
    const frame = broker.frames[index];
    if (frame.owner !== owner || (generation !== undefined && frame.generation !== generation)) continue;
    frame.removed = true;
    broker.frames.splice(index, 1);
  }
}

function currentFrame(broker: Broker): EventFrame | undefined {
  for (let index = broker.frames.length - 1; index >= 0; index--) {
    const frame = broker.frames[index];
    if (frame.removed || !frame.owner.active || frame.owner.generation !== frame.generation ||
        frame.owner.identity !== frame.ownerIdentity || frame.owner.token !== frame.ownerToken) continue;
    // eventPhase becomes NONE (0) as soon as dispatch returns. A residual frame
    // waiting for bubble/microtask cleanup must never own a later global error.
    if (frame.event.eventPhase === 0) continue;
    return frame;
  }
  return undefined;
}

function acquireBroker(owner: Window): Broker {
  let broker = brokers.get(owner);
  if (!broker) {
    const created: Broker = { frames: [], refs: 0, onError: () => undefined };
    created.onError = (event) => {
      const frame = currentFrame(created);
      if (!frame) return;
      try {
        event.preventDefault();
      } catch {
        // Failure attribution still proceeds for unusual ErrorEvent objects.
      }
      safeCall(frame.owner.onFailure, 'renderer_event_error');
    };
    broker = created;
    brokers.set(owner, created);
  }
  broker.refs += 1;
  if (broker.refs === 1) owner.addEventListener('error', broker.onError, true);
  return broker;
}

function releaseBroker(owner: Window, broker: Broker): void {
  if (broker.refs <= 0) return;
  broker.refs -= 1;
  if (broker.refs !== 0) return;
  try {
    owner.removeEventListener('error', broker.onError, true);
  } catch {
    // Bookkeeping still completes when a host window is being torn down.
  }
  broker.frames.length = 0;
  brokers.delete(owner);
}

function createEventScope(container: HTMLElement, identity: BlockRenderIdentity): EventScope {
  const windowOwner = container.ownerDocument.defaultView;
  if (!windowOwner) {
    return {
      activate: () => undefined,
      attachCleanup: () => undefined,
      deactivate: () => undefined,
      dispose: () => undefined,
    };
  }

  const broker = acquireBroker(windowOwner);
  const owner: EventOwner = {
    active: false,
    generation: 0,
    identity,
    onFailure: () => undefined,
    token: Symbol(identity.key),
  };
  const captureListeners = new Map<string, EventListener>();
  const cleanupListeners = new Map<string, EventListener>();
  let cleanupAttached = false;
  let disposed = false;

  for (const type of EVENT_TYPES) {
    const capture: EventListener = (event) => {
      if (disposed || !owner.active) return;
      const frame: EventFrame = {
        event,
        generation: owner.generation,
        owner,
        ownerIdentity: owner.identity,
        ownerToken: owner.token,
        removed: false,
      };
      broker.frames.push(frame);
      queueMicrotask(() => removeFrame(broker, frame));
    };
    captureListeners.set(type, capture);
    container.addEventListener(type, capture, true);
  }

  const scope: EventScope = {
    activate(generation, onFailure) {
      if (disposed) return;
      owner.generation = generation;
      owner.onFailure = onFailure;
      owner.active = true;
    },
    attachCleanup() {
      if (disposed || cleanupAttached) return;
      cleanupAttached = true;
      for (const type of EVENT_TYPES) {
        const cleanup: EventListener = (event) => {
          for (let index = broker.frames.length - 1; index >= 0; index--) {
            const frame = broker.frames[index];
            if (frame.owner === owner && frame.generation === owner.generation && frame.event === event) {
              removeFrame(broker, frame);
            }
          }
        };
        cleanupListeners.set(type, cleanup);
        // Added after createRoot so React's root bubble listener runs first.
        container.addEventListener(type, cleanup, false);
      }
    },
    deactivate(generation) {
      if (disposed || owner.generation !== generation) return;
      owner.active = false;
      removeOwnerFrames(broker, owner, generation);
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      owner.active = false;
      removeOwnerFrames(broker, owner);
      for (const [type, capture] of captureListeners) container.removeEventListener(type, capture, true);
      for (const [type, cleanup] of cleanupListeners) container.removeEventListener(type, cleanup, false);
      releaseBroker(windowOwner, broker);
    },
  };
  return scope;
}

export interface RendererIslandProps {
  identity: BlockRenderIdentity;
  module: RendererModule;
  rendererProps: BlockRendererProps;
  onFailure: (identity: BlockRenderIdentity, code: RendererFailureCode) => void;
}

export const RendererIsland: React.FC<RendererIslandProps> = ({
  identity,
  module: { component: Component },
  rendererProps,
  onFailure,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const rootRef = useRef<Root | null>(null);
  const scopeRef = useRef<EventScope | null>(null);
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

    let scope = scopeRef.current;
    if (!scope) {
      // Capture listeners must precede React's root capture listeners.
      scope = createEventScope(container, identity);
      scopeRef.current = scope;
    }
    scope.activate(lifecycle, (code) => failureRef.current(code));

    let root = rootRef.current;
    if (!root) {
      try {
        root = createRoot(container, {
          onCaughtError: () => failureRef.current('renderer_caught_error'),
          onRecoverableError: () => failureRef.current('renderer_recoverable_error'),
          onUncaughtError: () => failureRef.current('renderer_uncaught_error'),
        });
        rootRef.current = root;
      } catch {
        failureRef.current('renderer_root_error');
      }
    }
    scope.attachCleanup();

    return () => {
      activeRef.current = false;
      scope!.deactivate(lifecycle);
      // Parent-effect cleanup runs during another React commit. Defer disposal;
      // StrictMode's immediate setup increments lifecycle and reuses both scope
      // and root, cancelling this queued teardown.
      queueMicrotask(() => {
        if (lifecycleRef.current !== lifecycle) return;
        if (scopeRef.current === scope) scopeRef.current = null;
        scope!.dispose();
        if (rootRef.current === root) rootRef.current = null;
        try {
          root?.unmount();
        } catch {
          // Cleanup remains fail-closed and cannot take down the parent root.
        }
      });
    };
  }, [identity, onFailure]);

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

  return <div ref={containerRef} className="wg2-renderer-island" />;
};

RendererIsland.displayName = 'RendererIsland';
