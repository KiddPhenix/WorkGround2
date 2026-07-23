import { useEffect, useRef } from 'react';
import { useT } from '../../lib/i18n';

export interface WorkAvailabilitySurfaceProps {
  state: 'initializing' | 'unavailable';
  onBack: () => void;
  onRetry: () => void;
}

export function WorkAvailabilitySurface({
  state,
  onBack,
  onRetry,
}: WorkAvailabilitySurfaceProps) {
  const t = useT();
  const headingRef = useRef<HTMLHeadingElement>(null);
  const unavailable = state === 'unavailable';

  useEffect(() => {
    headingRef.current?.focus();
  }, [state]);

  return (
    <main
      className="work-page work-availability"
      data-testid="work-availability"
      data-work-status={state}
      aria-labelledby="work-availability-title"
      aria-busy={!unavailable}
    >
      <header className="work-page__header">
        <button
          type="button"
          className="work-page__back-btn"
          data-testid="work-availability-back"
          onClick={onBack}
        >
          ← {t('work.backToSession')}
        </button>
        <h1 className="work-page__title">{t('work.title')}</h1>
      </header>

      <div
        className="work-availability__body"
        role={unavailable ? 'alert' : 'status'}
        aria-live={unavailable ? 'assertive' : 'polite'}
      >
        <span className="work-availability__icon" aria-hidden="true" />
        <h2
          ref={headingRef}
          id="work-availability-title"
          className="work-availability__title"
          tabIndex={-1}
        >
          {t(unavailable ? 'work.unavailable' : 'work.initializing')}
        </h2>
        <p className="work-availability__detail">
          {t(unavailable ? 'work.unavailableDetail' : 'work.initializingDetail')}
        </p>
        {unavailable && (
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
