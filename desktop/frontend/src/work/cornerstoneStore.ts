import { create } from 'zustand';

import type {
  Cornerstone,
  CornerstoneAttention,
  CornerstoneAttentionItem,
  CornerstoneDrawerUI,
  CornerstoneItemUI,
  CornerstoneRetry,
  CornerstoneType,
  CornerstoneUIAction,
  CornerstoneUIState,
  Work,
} from './types';

const emptyDrawer = (): CornerstoneDrawerUI => ({
  byId: {},
  open: false,
  filterType: 'all',
  filterRequired: null,
});

const emptyItem = (): CornerstoneItemUI => ({
  draftTitle: null,
  draftContent: null,
  pendingAction: null,
  pendingRequestId: null,
  error: null,
  conflictSnapshot: null,
  retry: null,
});

export interface CornerstoneUIStoreActions {
  setOpen: (workId: string, open: boolean) => void;
  setFilterType: (workId: string, type: CornerstoneType | 'all') => void;
  setFilterRequired: (workId: string, required: boolean | null) => void;
  ensureItem: (workId: string, cornerstoneId: string) => void;
  setDraft: (workId: string, cornerstoneId: string, draft: { title?: string | null; content?: string | null }) => void;
  setPending: (workId: string, cornerstoneId: string, action: CornerstoneUIAction | null, requestId: string | null) => void;
  setError: (workId: string, cornerstoneId: string, error: string | null) => void;
  setConflict: (workId: string, cornerstoneId: string, conflict: Cornerstone | null) => void;
  setRetry: (workId: string, cornerstoneId: string, retry: CornerstoneRetry | null) => void;
  clearItem: (workId: string, cornerstoneId: string) => void;
  clearWork: (workId: string) => void;
  clearAll: () => void;
}

function patchDrawer(
  state: CornerstoneUIState,
  workId: string,
  patch: (drawer: CornerstoneDrawerUI) => CornerstoneDrawerUI,
): Pick<CornerstoneUIState, 'byWork'> {
  return { byWork: { ...state.byWork, [workId]: patch(state.byWork[workId] ?? emptyDrawer()) } };
}

function patchItem(
  state: CornerstoneUIState,
  workId: string,
  cornerstoneId: string,
  patch: (item: CornerstoneItemUI) => CornerstoneItemUI,
): Pick<CornerstoneUIState, 'byWork'> {
  return patchDrawer(state, workId, (drawer) => ({
    ...drawer,
    byId: {
      ...drawer.byId,
      [cornerstoneId]: patch(drawer.byId[cornerstoneId] ?? emptyItem()),
    },
  }));
}

export const useCornerstoneUIStore = create<CornerstoneUIState & CornerstoneUIStoreActions>((set) => ({
  byWork: {},

  setOpen: (workId, open) => set((state) => patchDrawer(state, workId, (drawer) => ({ ...drawer, open }))),
  setFilterType: (workId, filterType) => set((state) => patchDrawer(state, workId, (drawer) => ({ ...drawer, filterType }))),
  setFilterRequired: (workId, filterRequired) => set((state) => patchDrawer(state, workId, (drawer) => ({ ...drawer, filterRequired }))),
  ensureItem: (workId, cornerstoneId) => set((state) => {
    if (state.byWork[workId]?.byId[cornerstoneId]) return state;
    return patchItem(state, workId, cornerstoneId, (item) => item);
  }),
  setDraft: (workId, cornerstoneId, draft) => set((state) => patchItem(state, workId, cornerstoneId, (item) => ({
    ...item,
    draftTitle: draft.title === undefined ? item.draftTitle : draft.title,
    draftContent: draft.content === undefined ? item.draftContent : draft.content,
  }))),
  setPending: (workId, cornerstoneId, pendingAction, pendingRequestId) => set((state) => patchItem(state, workId, cornerstoneId, (item) => ({
    ...item,
    pendingAction,
    pendingRequestId,
  }))),
  setError: (workId, cornerstoneId, error) => set((state) => patchItem(state, workId, cornerstoneId, (item) => ({ ...item, error }))),
  setConflict: (workId, cornerstoneId, conflictSnapshot) => set((state) => patchItem(state, workId, cornerstoneId, (item) => ({ ...item, conflictSnapshot }))),
  setRetry: (workId, cornerstoneId, retry) => set((state) => patchItem(state, workId, cornerstoneId, (item) => ({ ...item, retry }))),
  clearItem: (workId, cornerstoneId) => set((state) => patchDrawer(state, workId, (drawer) => {
    const byId = { ...drawer.byId };
    delete byId[cornerstoneId];
    return { ...drawer, byId };
  })),
  clearWork: (workId) => set((state) => {
    if (!state.byWork[workId]) return state;
    const byWork = { ...state.byWork };
    delete byWork[workId];
    return { byWork };
  }),
  clearAll: () => set({ byWork: {} }),
}));

function attentionReason(cornerstone: Cornerstone): string {
  // candidateDigest present means a stale live_ref has been resolved and is waiting for accept.
  if (cornerstone.candidateDigest) return '来源内容已变化，请审核新版本后确认或冻结。';
  switch (cornerstone.status) {
    case 'stale': return '来源版本已变化，需要确认。';
    case 'missing': return '来源不可达，需要修复引用。';
    case 'denied': return '来源权限不足。';
    case 'invalid': return '内容或引用无效。';
    default: return '需要处理。';
  }
}

export function deriveCornerstoneAttention(work: Work): CornerstoneAttention {
  const items: CornerstoneAttentionItem[] = work.cornerstones
    .filter((cornerstone) => cornerstone.required && !cornerstone.tombstone && cornerstone.status !== 'active')
    .map((cornerstone) => ({
      cornerstoneId: cornerstone.id,
      title: cornerstone.title,
      status: cornerstone.status,
      reason: attentionReason(cornerstone),
    }));
  return { workId: work.id, items };
}

export function hasCornerstoneAttention(work: Work): boolean {
  return deriveCornerstoneAttention(work).items.length > 0;
}
