import React, { useCallback, useState } from 'react';

import type { SessionRef, SessionSurfaceContext } from '../../work/types';

export interface LinkedSessionCardProps {
  sessionRef: SessionRef;
  context: SessionSurfaceContext;
  onNavigate: (sessionRef: SessionRef) => Promise<void>;
}

export const LinkedSessionCard: React.FC<LinkedSessionCardProps> = ({
  sessionRef,
  context,
  onNavigate,
}) => {
  const [navigating, setNavigating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleNavigate = useCallback(async () => {
    setNavigating(true);
    setError(null);
    try {
      await onNavigate(sessionRef);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setNavigating(false);
    }
  }, [onNavigate, sessionRef]);

  return (
    <div
      className="wg2-linked-session-card"
      data-testid="linked-session-card"
      data-work-target-id={`attempt:${context.runId}:${context.stageId}:${context.taskId}:${context.attemptId}`}
    >
      <div className="wg2-linked-session-card__header">
        <h3 className="wg2-linked-session-card__title">关联会话</h3>
        <span className="wg2-linked-session-card__badge" data-testid="linked-session-badge">
          {sessionRef.modelRef || 'Session'}
        </span>
      </div>

      <div className="wg2-linked-session-card__summary" data-testid="linked-session-summary">
        {sessionRef.preview ? (
          <p>{sessionRef.preview.slice(0, 200)}{sessionRef.preview.length > 200 ? '…' : ''}</p>
        ) : (
          <p className="wg2-linked-session-card__no-preview">此尝试关联的会话尚未载入当前视图。</p>
        )}
      </div>

      <div className="wg2-linked-session-card__meta">
        <span className="wg2-linked-session-card__meta-item" data-testid="linked-session-path">
          {sessionRef.sessionPath.split('/').pop() || sessionRef.sessionPath}
        </span>
        {sessionRef.turnCount > 0 && (
          <span className="wg2-linked-session-card__meta-item">
            {sessionRef.turnCount} 轮
          </span>
        )}
      </div>

      {error && (
        <div className="wg2-linked-session-card__error" role="alert" data-testid="linked-session-error">
          {error}
        </div>
      )}

      <div className="wg2-linked-session-card__actions">
        <button
          type="button"
          className="wg2-linked-session-card__navigate-btn"
          data-testid="linked-session-navigate"
          disabled={navigating}
          onClick={handleNavigate}
        >
          {navigating ? '正在打开…' : '打开关联会话'}
        </button>
        {error && (
          <button
            type="button"
            className="wg2-linked-session-card__retry-btn"
            data-testid="linked-session-retry"
            disabled={navigating}
            onClick={handleNavigate}
          >
            重试
          </button>
        )}
      </div>
    </div>
  );
};
