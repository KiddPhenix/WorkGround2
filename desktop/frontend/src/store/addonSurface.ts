// addonSurface owns the AddOn Surface Registry — the set of AddOn instances
// visible in the Workbench. Each instance is keyed by stable instanceId.
//
// The store itself is a flat record with no internal re-ordering. Display order
// is determined by selectSortedAddOnInstances, which accepts an optional
// editingInstanceId. When editing, the focused instance is frozen at its current
// display index so status/density changes don't cause it to jump. Once editing
// ends (editingInstanceId = null), the normal sort order resumes:
//   needs_input > pinned > activation order (ascending).

import { create } from "zustand";

// ── Types ───────────────────────────────────────────────────────────────────

export type AddOnScope = "window" | "workspace" | "session";

export type AddOnDensity = "tab" | "peek" | "focus";

export type AddOnInstanceStatus =
  | "registered"
  | "loading"
  | "queued"
  | "active"
  | "needs_input"
  | "warning"
  | "error"
  | "offline"
  | "completed"
  | "playing"
  | "paused"
  | "dismissed";

export type AddOnInstance = {
  /** Stable unique key for this instance. */
  instanceId: string;
  /** The plugin that owns this instance. */
  pluginId: string;
  /** The panel/view within the plugin. */
  panelId: string;
  /** How long the instance lives across navigation. */
  scope: AddOnScope;
  /** Runtime status. */
  status: AddOnInstanceStatus;
  /** Display density in the Workbench. */
  density: AddOnDensity;
  /** Whether the instance is pinned (always sorted before unpinned). */
  pinned: boolean;
  /** Ascending activation order — set when the instance is first upserted. */
  activationOrder: number;
  /** Display title shown in the InstanceHeader. */
  title: string;
  /** Error or warning summary when status == error | warning. */
  message?: string;
};

export type AddOnSurfaceState = {
  /** Flat record — not ordered. Use selectSortedAddOnInstances for display. */
  instances: Record<string, AddOnInstance>;
  /** Whether the Workbench panel itself is open. */
  workbenchOpen: boolean;
  /**
   * When set, the editing instance's display position is frozen by
   * selectSortedAddOnInstances. Set to null to allow normal sort.
   */
  editingInstanceId: string | null;
  /**
   * The display index of the editing instance at the moment editing began.
   * Used by selectSortedAddOnInstances to freeze the instance's position.
   * Null when no editing is active or the index could not be determined.
   */
  _frozenDisplayIndex: number | null;
};

export type AddOnSurfaceActions = {
  /**
   * Insert or update an instance. If new, activationOrder is set automatically.
   * This is a flat-record upsert with no internal re-ordering.
   */
  upsertInstance: (instance: Omit<AddOnInstance, "activationOrder">) => void;

  /** Remove an instance by instanceId. Safe on missing ids. */
  removeInstance: (instanceId: string) => void;

  /** Change the display density of an instance. */
  setInstanceDensity: (instanceId: string, density: AddOnDensity) => void;

  /** Set the pinned flag on an instance. */
  setInstancePinned: (instanceId: string, pinned: boolean) => void;

  /** Update the runtime status of an instance. */
  setInstanceStatus: (
    instanceId: string,
    status: AddOnInstanceStatus,
    meta?: { message?: string },
  ) => void;

  /** Set the scope of an instance. */
  setInstanceScope: (instanceId: string, scope: AddOnScope) => void;

  /** Open or close the Workbench. */
  setWorkbenchOpen: (open: boolean) => void;

  /**
   * Set which instance is being actively edited. When set,
   * selectSortedAddOnInstances freezes that instance at its display index.
   */
  setEditingInstance: (instanceId: string | null) => void;

  /** Remove all instances and reset workbench to closed. */
  clearAllInstances: () => void;
};

// ── Sorting ─────────────────────────────────────────────────────────────────

/**
 * Compare two AddOn instances for stable ordering:
 * 1. needs_input status comes first
 * 2. Pinned instances come next
 * 3. By activation order (ascending)
 */
export function compareAddOnInstances(
  a: AddOnInstance,
  b: AddOnInstance,
): number {
  // needs_input sorts before everything
  const aNeedsInput = a.status === "needs_input" ? 0 : 1;
  const bNeedsInput = b.status === "needs_input" ? 0 : 1;
  if (aNeedsInput !== bNeedsInput) return aNeedsInput - bNeedsInput;

  // Pinned sorts after needs_input but before unpinned
  if (a.pinned !== b.pinned) return a.pinned ? -1 : 1;

  // Stable tie-breaker: activation order
  return a.activationOrder - b.activationOrder;
}

// ── Helper ───────────────────────────────────────────────────────────────────

let _nextActivationOrder = 1;

function nextActivationOrder(): number {
  return _nextActivationOrder++;
}

// ── Store ────────────────────────────────────────────────────────────────────

export const useAddOnSurfaceStore = create<
  AddOnSurfaceState & AddOnSurfaceActions
>((set) => ({
  instances: {},
  workbenchOpen: false,
  editingInstanceId: null,
  _frozenDisplayIndex: null,

  upsertInstance: (partial) =>
    set((s) => {
      const existing = s.instances[partial.instanceId];
      const merged: AddOnInstance = existing
        ? { ...existing, ...partial }
        : { ...partial, activationOrder: nextActivationOrder() };
      return {
        instances: { ...s.instances, [partial.instanceId]: merged },
      };
    }),

  removeInstance: (instanceId) =>
    set((s) => {
      if (!(instanceId in s.instances)) return s;
      const { [instanceId]: _, ...rest } = s.instances;
      return {
        instances: rest,
        ...(s.editingInstanceId === instanceId
          ? { editingInstanceId: null, _frozenDisplayIndex: null }
          : {}),
      };
    }),

  setInstanceDensity: (instanceId, density) =>
    set((s) => {
      const existing = s.instances[instanceId];
      if (!existing) return s;
      return {
        instances: {
          ...s.instances,
          [instanceId]: { ...existing, density },
        },
      };
    }),

  setInstancePinned: (instanceId, pinned) =>
    set((s) => {
      const existing = s.instances[instanceId];
      if (!existing) return s;
      return {
        instances: {
          ...s.instances,
          [instanceId]: { ...existing, pinned },
        },
      };
    }),

  setInstanceStatus: (instanceId, status, meta) =>
    set((s) => {
      const existing = s.instances[instanceId];
      if (!existing) return s;
      return {
        instances: {
          ...s.instances,
          [instanceId]: {
            ...existing,
            status,
            ...(meta?.message ? { message: meta.message } : {}),
          },
        },
      };
    }),

  setInstanceScope: (instanceId, scope) =>
    set((s) => {
      const existing = s.instances[instanceId];
      if (!existing) return s;
      return {
        instances: {
          ...s.instances,
          [instanceId]: { ...existing, scope },
        },
      };
    }),

  setWorkbenchOpen: (open) => set({ workbenchOpen: open }),

  setEditingInstance: (instanceId) =>
    set((s) => {
      if (instanceId === null) {
        return { editingInstanceId: null, _frozenDisplayIndex: null };
      }
      if (instanceId === s.editingInstanceId) return s;
      // Snapshot the current display index of the target instance
      const sorted = Object.values(s.instances).sort(compareAddOnInstances);
      const idx = sorted.findIndex((i) => i.instanceId === instanceId);
      return {
        editingInstanceId: instanceId,
        _frozenDisplayIndex: idx >= 0 ? idx : null,
      };
    }),

  clearAllInstances: () =>
    set({
      instances: {},
      workbenchOpen: false,
      editingInstanceId: null,
      _frozenDisplayIndex: null,
    }),
}));

// ── Selectors ───────────────────────────────────────────────────────────────

/** Select a single instance by instanceId. */
export function selectAddOnInstance(
  instances: Record<string, AddOnInstance>,
  instanceId: string,
): AddOnInstance | undefined {
  return instances[instanceId];
}

/**
 * Return instances in stable display order: needs_input > pinned > activation.
 *
 * When editingInstanceId is set and frozenDisplayIndex is non-null, the editing
 * instance is frozen at that index — its position in the sorted list is
 * preserved even if its status or pin state would normally move it. Once
 * editing ends (editingInstanceId = null) the normal sort applies to all.
 *
 * The frozenDisplayIndex is the index the instance held when editing began;
 * it is stored separately in the store (see _frozenDisplayIndex) and passed
 * through so this function stays pure and deterministic.
 */
export function selectSortedAddOnInstances(
  instances: Record<string, AddOnInstance>,
  editingInstanceId: string | null,
  frozenDisplayIndex: number | null,
): AddOnInstance[] {
  const all = Object.values(instances);
  const sorted = [...all].sort(compareAddOnInstances);

  if (!editingInstanceId || !instances[editingInstanceId] || frozenDisplayIndex === null) {
    return sorted;
  }

  // Remove the editing instance from the sorted list, sort the rest normally,
  // then re-insert the editing instance at the frozen index.
  const rest = sorted.filter((i) => i.instanceId !== editingInstanceId);
  const editingInst = instances[editingInstanceId];
  rest.splice(Math.min(frozenDisplayIndex, rest.length), 0, editingInst);
  return rest;
}

/** Count instances that are not dismissed. */
export function selectActiveAddOnCount(
  instances: Record<string, AddOnInstance>,
): number {
  return Object.values(instances).filter(
    (i) => i.status !== "dismissed",
  ).length;
}

/** Count instances with needs_input status. */
export function selectNeedsInputAddOnCount(
  instances: Record<string, AddOnInstance>,
): number {
  return Object.values(instances).filter(
    (i) => i.status === "needs_input",
  ).length;
}

/** Count instances with error status. */
export function selectErrorAddOnCount(
  instances: Record<string, AddOnInstance>,
): number {
  return Object.values(instances).filter(
    (i) => i.status === "error",
  ).length;
}

/** Count instances pinned. */
export function selectPinnedAddOnCount(
  instances: Record<string, AddOnInstance>,
): number {
  return Object.values(instances).filter((i) => i.pinned).length;
}
