// artifacts owns the Artifact registry — a flat map of artifact records keyed by
// stable artifactId. Every record carries a status (available / stale / missing /
// generating / failed) and optional source-run / error metadata. Upserts are
// idempotent; removing a non-existent artifact is a no-op.
//
// This is only the state-model layer: no persistence, no file-system validation,
// no component references.

import { create } from "zustand";
import type { ArtifactStatus, ArtifactView } from "../lib/types";

// ── Types ───────────────────────────────────────────────────────────────────

export type { ArtifactStatus };
export type ArtifactRecord = ArtifactView;

export type ArtifactState = {
  artifacts: Record<string, ArtifactRecord>;
};

export type ArtifactActions = {
  /**
   * Insert or update an artifact. Idempotent: repeating the same artifactId
   * with the same data is a no-op; passing new fields merges them in.
   */
  upsertArtifact: (record: ArtifactRecord) => void;

  /** Atomically replace one session's projection while preserving other sessions. */
  replaceSessionArtifacts: (sessionId: string, records: ArtifactRecord[]) => void;

  /**
   * Remove an artifact by artifactId. Safe on missing artifacts.
   */
  removeArtifact: (artifactId: string) => void;

  /**
   * Update the status of an artifact. If `errorMessage` is provided it is
   * set on the record regardless of status.
   */
  setArtifactStatus: (
    artifactId: string,
    status: ArtifactStatus,
    meta?: { errorMessage?: string },
  ) => void;

  /** Remove all artifacts. */
  clearAllArtifacts: () => void;
};

function sameArtifact(a: ArtifactRecord | undefined, b: ArtifactRecord): boolean {
  if (!a) return false;
  return a.artifactId === b.artifactId &&
    a.name === b.name &&
    a.type === b.type &&
    a.status === b.status &&
    a.sessionId === b.sessionId &&
    a.path === b.path &&
    a.sourceRunId === b.sourceRunId &&
    a.lastVerifiedAt === b.lastVerifiedAt &&
    a.errorMessage === b.errorMessage;
}

// ── Store ────────────────────────────────────────────────────────────────────

export const useArtifactStore = create<ArtifactState & ArtifactActions>((set) => ({
  artifacts: {},

  upsertArtifact: (record) =>
    set((s) => ({
      artifacts: {
        ...s.artifacts,
        [record.artifactId]: {
          // If the record already exists, preserve fields the caller didn't
          // provide (e.g. lastVerifiedAt) by spreading existing first.
          ...(s.artifacts[record.artifactId] ?? {}),
          ...record,
        },
      },
    })),

  replaceSessionArtifacts: (sessionId, records) =>
    set((s) => {
      const artifacts: Record<string, ArtifactRecord> = {};
      for (const [id, artifact] of Object.entries(s.artifacts)) {
        if (artifact.sessionId !== sessionId) artifacts[id] = artifact;
      }
      for (const record of records) {
        artifacts[record.artifactId] = { ...record, sessionId };
      }
      const previous = Object.keys(s.artifacts);
      const next = Object.keys(artifacts);
      if (
        previous.length === next.length &&
        next.every((id) => sameArtifact(s.artifacts[id], artifacts[id]))
      ) {
        return s;
      }
      return { artifacts };
    }),

  removeArtifact: (artifactId) =>
    set((s) => {
      if (!(artifactId in s.artifacts)) return s;
      const { [artifactId]: _, ...rest } = s.artifacts;
      return { artifacts: rest };
    }),

  setArtifactStatus: (artifactId, status, meta) =>
    set((s) => {
      const existing = s.artifacts[artifactId];
      if (!existing) return s;
      return {
        artifacts: {
          ...s.artifacts,
          [artifactId]: {
            ...existing,
            status,
            ...(meta?.errorMessage ? { errorMessage: meta.errorMessage } : {}),
          },
        },
      };
    }),

  clearAllArtifacts: () => set({ artifacts: {} }),
}));

// ── Selectors ───────────────────────────────────────────────────────────────

/** Select a single artifact record by artifactId. */
export function selectArtifact(
  artifacts: Record<string, ArtifactRecord>,
  artifactId: string,
): ArtifactRecord | undefined {
  return artifacts[artifactId];
}

/** Select all artifact records for a given session. */
export function selectArtifactsBySession(
  artifacts: Record<string, ArtifactRecord>,
  sessionId: string,
): ArtifactRecord[] {
  return Object.values(artifacts).filter((a) => a.sessionId === sessionId);
}

/** Select artifact IDs that match one of the given statuses. */
export function selectArtifactIdsByStatus(
  artifacts: Record<string, ArtifactRecord>,
  ...statuses: ArtifactStatus[]
): string[] {
  const set = new Set(statuses);
  return Object.keys(artifacts).filter((id) => set.has(artifacts[id].status));
}

/** Count artifacts with a given status. */
export function selectArtifactCountByStatus(
  artifacts: Record<string, ArtifactRecord>,
  ...statuses: ArtifactStatus[]
): number {
  return selectArtifactIdsByStatus(artifacts, ...statuses).length;
}
