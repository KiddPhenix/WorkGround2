import React, { useCallback, useMemo, useRef, useState } from 'react';

import { deriveCornerstoneAttention, useCornerstoneUIStore } from '../../work/cornerstoneStore';
import { useWorkStore } from '../../work/store';

export interface WorkRunEntryProps {
  workId: string;
  onRun?: (input: { workId: string; requestId: string }) => void | Promise<void>;
  onOpenDrawer?: () => void;
  disabled?: boolean;
}

export const WorkRunEntry: React.FC<WorkRunEntryProps> = ({ workId, onRun, onOpenDrawer, disabled = false }) => {
  const view = useWorkStore((state) => state.works[workId]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const retryRequestId = useRef<string | null>(null);
  const attention = useMemo(() => view ? deriveCornerstoneAttention(view.work) : null, [view]);
  const blocked = (attention?.items.length ?? 0) > 0;
  const running = view?.work.state === 'running';

  const openDrawer = useCallback(() => {
    useCornerstoneUIStore.getState().setOpen(workId, true);
    onOpenDrawer?.();
  }, [onOpenDrawer, workId]);

  const handleRun = useCallback(async () => {
    if (blocked) {
      openDrawer();
      return;
    }
    if (!onRun || disabled || pending || running) return;
    setPending(true);
    setError(null);
    const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const runRequestId = retryRequestId.current ?? `work-run-${suffix}`;
    try {
      await onRun({ workId, requestId: runRequestId });
      retryRequestId.current = null;
    } catch {
      retryRequestId.current = runRequestId;
      setError('运行请求失败，可安全重试。');
    } finally {
      setPending(false);
    }
  }, [blocked, disabled, onRun, openDrawer, pending, running, workId]);

  return (
    <div className="work-run-entry" data-testid="work-run-entry">
      <button
        type="button"
        className="work-run-entry__btn"
        onClick={() => void handleRun()}
        disabled={disabled || running || pending || !view}
        aria-describedby={blocked ? `work-run-attention-${workId}` : undefined}
      >
        {running ? '运行中…' : pending ? '正在启动…' : '运行'}
      </button>
      {blocked && (
        <div id={`work-run-attention-${workId}`} className="work-run-entry__attention" role="alert" data-testid="work-run-attention">
          <span>{attention!.items.length} 个必需基石需先处理。</span>
          <button type="button" onClick={openDrawer}>查看基石</button>
        </div>
      )}
      {error && <p className="work-run-entry__error" role="alert">{error}</p>}
    </div>
  );
};

export default WorkRunEntry;
