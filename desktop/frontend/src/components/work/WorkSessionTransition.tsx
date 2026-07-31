import { BriefcaseBusiness, LoaderCircle, RefreshCw } from 'lucide-react';

import { useT } from '../../lib/i18n';

export type WorkSessionTransitionPhase = 'initializing' | 'planning' | 'revealing' | 'error';

export interface WorkSessionTransitionProps {
  prompt: string;
  phase: WorkSessionTransitionPhase;
  error?: string;
  onRetry: () => void;
}

export function WorkSessionTransition({
  prompt,
  phase,
  error,
  onRetry,
}: WorkSessionTransitionProps) {
  const t = useT();
  const failed = phase === 'error';
  const revealing = phase === 'revealing';

  return (
    <div
      className="work-session-transition__overlay"
      data-testid="work-session-transition"
      data-phase={phase}
      aria-hidden={revealing ? 'true' : undefined}
    >
      <div className="work-session-transition__conversation">
        <div className="work-session-transition__intent">
          <span className="work-session-transition__intent-label">
            <BriefcaseBusiness size={13} aria-hidden="true" />
            {t('work.sessionIntent')}
          </span>
          <p>{prompt}</p>
        </div>
        <div className="work-session-transition__progress" role={failed ? 'alert' : 'status'} aria-live="polite">
          <span className="work-session-transition__progress-icon" aria-hidden="true">
            {failed ? <BriefcaseBusiness size={17} /> : <LoaderCircle size={17} />}
          </span>
          <span className="work-session-transition__progress-copy">
            <strong>{failed ? t('work.sessionPrepareFailed') : t('work.sessionPreparing')}</strong>
            <span>{failed ? error || t('work.sessionPrepareFailedDetail') : t('work.sessionPreparingDetail')}</span>
          </span>
          {failed && (
            <button type="button" onClick={onRetry}>
              <RefreshCw size={13} aria-hidden="true" />
              {t('common.retry')}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
