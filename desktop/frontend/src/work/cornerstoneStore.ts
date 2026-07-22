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
  RunBlockCode,
  WorkView,
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

const RUN_BLOCK_REASONS: Record<RunBlockCode, string> = {
  blob_missing: '快照内容缺失，请提供匹配 digest 的原始内容。',
  budget_exhausted: 'Token 预算已耗尽。',
  resolver_unavailable: '来源解析器不可用。',
  cornerstone_stale: '基石来源已变化，请审核后确认。',
  cornerstone_missing: '基石来源不可达，请修复引用。',
  cornerstone_denied: '基石来源权限不足。',
  cornerstone_invalid: '基石内容或引用无效，请修复。',
  waiting_user: '等待用户操作后继续。',
  failed: '运行失败。',
  archived: '已归档。',
};

function assessmentReason(issue: { blocking: boolean }): string {
  return issue.blocking ? '权威评估阻止运行。' : '可选基石降级可用，不阻止运行。';
}

export function deriveCornerstoneAttention(view: WorkView): CornerstoneAttention {
  // Derive attention SOLELY from the authoritative assessment/runBlock.
  // Never scan cornerstone statuses directly — the backend owns blockage.
  const items: CornerstoneAttentionItem[] = [];
  if (view.runBlock?.items) {
    for (const item of view.runBlock.items) {
      const reason = RUN_BLOCK_REASONS[item.code] ?? item.code;
      items.push({
        cornerstoneId: item.cornerstoneId ?? '',
        title: item.status ?? item.code,
        status: item.status ?? 'invalid',
        reason,
      });
    }
  }
  // Also surface assessment issues that may not appear in runBlock (e.g. optional degraded)
  if (view.assessment?.issues) {
    for (const issue of view.assessment.issues) {
      if (items.some((existing) => existing.cornerstoneId === issue.cornerstoneId)) continue;
      items.push({
        cornerstoneId: issue.cornerstoneId,
        title: issue.title || '基石',
        status: 'invalid',
        reason: assessmentReason(issue),
      });
    }
  }
  return { workId: view.work.id, items };
}

export function hasCornerstoneAttention(view: WorkView): boolean {
  return Boolean(view.runBlock?.blocked || view.assessment?.blocking || view.assessment?.degraded);
}
