import { LoaderCircle } from 'lucide-react';

import { useT } from '../../lib/i18n';

export type WorkStartPhase = 'initializing' | 'ready' | 'starting' | 'error';

export interface WorkStartSurfaceProps {
  prompt: string;
  phase: WorkStartPhase;
  error?: string;
  onPromptChange: (prompt: string) => void;
  onStart: () => void;
}

export function WorkStartSurface({
  prompt,
  phase,
  error,
  onPromptChange,
  onStart,
}: WorkStartSurfaceProps) {
  const t = useT();
  const starting = phase === 'starting';
  const status = phase === 'ready'
    ? t('work.startReady')
    : phase === 'error'
      ? t('work.startFailed')
      : t('work.startInitializing');

  return (
    <main className="work-start-surface" data-testid="work-start-surface" data-phase={phase}>
      <div
        className="work-start-surface__drag-region"
        data-testid="work-start-drag-region"
        aria-hidden="true"
      />
      <section className="wg2-work-draft-editor" data-testid="work-draft-editor">
        <div className="wg2-work-draft-heading">
          <h3>{t('work.startTitle')}</h3>
          <p>{t('work.startDetail')}</p>
        </div>
        <label
          className="wg2-work-prompt-field"
          data-busy={starting ? 'true' : 'false'}
          aria-busy={starting}
        >
          <span className="sr-only">{t('work.startTitle')}</span>
          <textarea
            autoFocus
            data-testid="work-start-prompt"
            value={prompt}
            rows={6}
            placeholder={t('work.startPlaceholder')}
            disabled={starting}
            onChange={(event) => onPromptChange(event.target.value)}
          />
        </label>
        <div className="work-start-surface__status" role="status" aria-live="polite">
          <span className="work-start-surface__status-dot" data-phase={phase} aria-hidden="true" />
          {status}
        </div>
        {error && <p className="work-start-surface__error" role="alert">{error}</p>}
        <div className="wg2-work-draft-actions" data-busy={starting ? 'true' : 'false'}>
          <button
            type="button"
            className="wg2-work-generate-btn"
            data-testid="work-start-submit"
            aria-busy={starting}
            disabled={starting || !prompt.trim()}
            onClick={onStart}
          >
            {starting && (
              <LoaderCircle
                className="wg2-work-generate-btn__spinner"
                size={15}
                strokeWidth={2}
                aria-hidden="true"
              />
            )}
            {starting ? t('work.startWaiting') : t('work.start')}
          </button>
        </div>
      </section>
    </main>
  );
}
