import { useEffect, useRef } from 'react';
import { useT } from '../../lib/i18n';

export interface WorkAvailabilitySurfaceProps {
  state: 'initializing' | 'unavailable' | 'incomplete';
  onBack?: () => void;
  onRetry?: () => void;
}

export function WorkAvailabilitySurface({
  state,
  onBack,
  onRetry,
}: WorkAvailabilitySurfaceProps) {
  const t = useT();
  const headingRef = useRef<HTMLHeadingElement>(null);
  const blocked = state === 'unavailable' || state === 'incomplete';

  useEffect(() => {
    headingRef.current?.focus();
  }, [state]);

  return (
    <main
      className="work-page work-availability"
      data-testid="work-availability"
      data-work-status={state}
      aria-labelledby="work-availability-title"
      aria-busy={!blocked}
    >
      <header className="work-page__header">
        {onBack && (
          <button
            type="button"
            className="work-page__back-btn"
            data-testid="work-availability-back"
            onClick={onBack}
          >
            ← {t('work.backToSession')}
          </button>
        )}
        <h1 className="work-page__title">{t('work.title')}</h1>
      </header>

      <div
        className="work-availability__body"
        role={blocked ? 'alert' : 'status'}
        aria-live={blocked ? 'assertive' : 'polite'}
      >
        <span className="work-availability__icon" aria-hidden="true" />
        <h2
          ref={headingRef}
          id="work-availability-title"
          className="work-availability__title"
          tabIndex={-1}
        >
          {t(state === 'unavailable' ? 'work.unavailable' : state === 'incomplete' ? 'work.incomplete' : 'work.initializing')}
        </h2>
        <p className="work-availability__detail">
          {t(state === 'unavailable' ? 'work.unavailableDetail' : state === 'incomplete' ? 'work.incompleteDetail' : 'work.initializingDetail')}
        </p>
        {blocked && onRetry && (
          <button
            type="button"
            className="work-page__retry-btn work-availability__retry"
            data-testid="work-availability-retry"
            onClick={onRetry}
          >
            {t('work.retry')}
          </button>
        )}
      </div>
    </main>
  );
}
