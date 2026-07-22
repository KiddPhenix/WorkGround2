// WorkPage — the Workspace-level Work entry page.
// Shows active Work list, create button, and basic error/empty/loading states.
// Tab ownership comes from App; all ACKs are guarded by mountGen so late
// results from a previous tab can't contaminate the current view.

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { app } from '../../lib/bridge';
import { useT } from '../../lib/i18n';
import type { CreateWorkInput, WorkSummary, WorkPage as WorkPageData } from '../../work/types';

type PageState = 'loading' | 'ready' | 'error';

// ── CreateWorkDialog ────────────────────────────────────────────────────

interface CreateDialogProps {
  open: boolean;
  onCreate: (name: string) => void;
  onCancel: () => void;
  onNameChange: () => void;
  creating: boolean;
  createError: string | null;
}

const CreateWorkDialog: React.FC<CreateDialogProps> = ({
  open, onCreate, onCancel, onNameChange, creating, createError,
}) => {
  const t = useT();
  const [name, setName] = useState('');
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setName('');
    const timer = window.setTimeout(() => inputRef.current?.focus(), 0);
    return () => window.clearTimeout(timer);
  }, [open]);

  const handleChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      setName(e.target.value);
      onNameChange();
    },
    [onNameChange],
  );

  const handleSubmit = useCallback(
    (e: React.FormEvent) => {
      e.preventDefault();
      const trimmed = name.trim();
      if (!trimmed || creating) return;
      onCreate(trimmed);
    },
    [name, creating, onCreate],
  );

  if (!open) return null;

  return (
    <div className="work-page__dialog-overlay" data-testid="work-create-dialog" onClick={onCancel}>
      <form
        className="work-page__dialog"
        data-testid="work-create-form"
        onClick={(e) => e.stopPropagation()}
        onSubmit={handleSubmit}
      >
        <h2 className="work-page__dialog-title">{t('work.createTitle')}</h2>
        <label className="work-page__dialog-label">
          {t('work.nameLabel')}
          <input
            ref={inputRef}
            className="work-page__dialog-input"
            type="text"
            data-testid="work-create-name"
            value={name}
            onChange={handleChange}
            placeholder={t('work.namePlaceholder')}
            maxLength={120}
            disabled={creating}
            autoFocus
          />
        </label>
        {createError && (
          <p className="work-page__dialog-error" data-testid="work-create-error">
            {createError}
          </p>
        )}
        <div className="work-page__dialog-actions">
          <button
            type="button"
            className="work-page__dialog-btn work-page__dialog-btn--cancel"
            data-testid="work-create-cancel"
            onClick={onCancel}
            disabled={creating}
          >
            {t('work.cancel')}
          </button>
          <button
            type="submit"
            className="work-page__dialog-btn work-page__dialog-btn--create"
            data-testid="work-create-submit"
            disabled={!name.trim() || creating}
          >
            {creating ? t('work.creating') : t('work.create')}
          </button>
        </div>
      </form>
    </div>
  );
};

// ── WorkPage ─────────────────────────────────────────────────────────────

export interface WorkPageProps {
  tabID: string;
  onBack: () => void;
  onOpenWork: (workID: string) => void;
}

export const WorkPage: React.FC<WorkPageProps> = ({ tabID, onBack, onOpenWork }) => {
  const t = useT();

  const [pageState, setPageState] = useState<PageState>('loading');
  const [error, setError] = useState<string | null>(null);
  const [works, setWorks] = useState<WorkSummary[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

  // Stable create intent: first submit generates a requestID that is held
  // until success/cancel/input-change. Same-name retry reuses it.
  const lastCreateNameRef = useRef<string | null>(null);
  const createRequestIDRef = useRef<string | null>(null);

  // Mount generation: guards all async ACKs. Incremented on every mount
  // (via effect) and also on unmount (via cleanup) so late promises from
  // a previous render can never apply state.
  const mountGenRef = useRef(0);

  // List effect with cleanup: on tabID change/unmount, invalidates old
  // generation and clears any stale create intent.
  useEffect(() => {
    if (!tabID) return;
    mountGenRef.current++;
    createRequestIDRef.current = null;
    lastCreateNameRef.current = null;
    const gen = mountGenRef.current;

    setPageState('loading');
    setError(null);

    app
      .ListWorks(tabID, { limit: 50, archiveState: 'active' })
      .then((page: WorkPageData) => {
        if (mountGenRef.current !== gen) return;
        setWorks(page.items);
        setPageState('ready');
      })
      .catch((err: unknown) => {
        if (mountGenRef.current !== gen) return;
        setError(err instanceof Error ? err.message : String(err));
        setPageState('error');
      });

    return () => {
      // Invalidate on unmount or tabID change so late ACKs are ignored.
      mountGenRef.current++;
      createRequestIDRef.current = null;
      lastCreateNameRef.current = null;
    };
  }, [tabID]);

  // Clear create intent on any input edit (but not on failure retry).
  const handleNameChange = useCallback(() => {
    if (creating) return; // don't clear while request is in-flight
    createRequestIDRef.current = null;
    lastCreateNameRef.current = null;
  }, [creating]);

  const handleCreate = useCallback(
    (name: string) => {
      if (!tabID) return;

      // Same name → reuse existing requestID; else generate new.
      if (lastCreateNameRef.current !== name || !createRequestIDRef.current) {
        createRequestIDRef.current = `work-create-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
        lastCreateNameRef.current = name;
      }

      const requestID = createRequestIDRef.current;
      const gen = mountGenRef.current;
      setCreating(true);
      setCreateError(null);

      const input: CreateWorkInput = {
        blueprintRef: { id: 'blueprint:blank', schemaVersion: 1, version: 1 },
        name,
        requestId: requestID,
      };

      app
        .CreateWork(tabID, input)
        .then((work) => {
          if (mountGenRef.current !== gen) return;
          createRequestIDRef.current = null;
          lastCreateNameRef.current = null;
          setCreating(false);
          setShowCreate(false);

          // Refresh list (guarded by gen), then open the new Work.
          app
            .ListWorks(tabID, { limit: 50, archiveState: 'active' })
            .then((page: WorkPageData) => {
              if (mountGenRef.current !== gen) return;
              setWorks(page.items);
              onOpenWork(work.id);
            })
            .catch(() => {
              if (mountGenRef.current !== gen) return;
              onOpenWork(work.id);
            });
        })
        .catch((err: unknown) => {
          if (mountGenRef.current !== gen) return;
          setCreating(false);
          setCreateError(err instanceof Error ? err.message : String(err));
          // Keep intent so retry reuses same requestID.
        });
    },
    [tabID, onOpenWork],
  );

  const handleCancelCreate = useCallback(() => {
    createRequestIDRef.current = null;
    lastCreateNameRef.current = null;
    setShowCreate(false);
    setCreateError(null);
  }, []);

  const handleOpenDialog = useCallback(() => {
    setShowCreate(true);
    setCreateError(null);
  }, []);

  return (
    <div className="work-page" data-testid="work-page">
      <header className="work-page__header">
        <button
          type="button"
          className="work-page__back-btn"
          data-testid="work-back-btn"
          onClick={onBack}
        >
          ← {t('work.backToSession')}
        </button>
        <h1 className="work-page__title">{t('work.title')}</h1>
        <button
          type="button"
          className="work-page__new-btn"
          data-testid="work-new-btn"
          onClick={handleOpenDialog}
          disabled={pageState === 'loading'}
        >
          {t('work.newWork')}
        </button>
      </header>

      <div className="work-page__body">
        {pageState === 'loading' && (
          <div className="work-page__loading" data-testid="work-loading">
            {t('work.loading')}
          </div>
        )}

        {pageState === 'error' && (
          <div className="work-page__error" data-testid="work-error">
            <p>{error}</p>
            <button
              type="button"
              className="work-page__retry-btn"
              data-testid="work-retry-btn"
              onClick={() => {
                // Manual retry: just re-trigger the list effect by
                // setting loading and calling ListWorks directly.
                if (!tabID) return;
                mountGenRef.current++;
                const gen = mountGenRef.current;
                setPageState('loading');
                setError(null);
                app
                  .ListWorks(tabID, { limit: 50, archiveState: 'active' })
                  .then((page: WorkPageData) => {
                    if (mountGenRef.current !== gen) return;
                    setWorks(page.items);
                    setPageState('ready');
                  })
                  .catch((err: unknown) => {
                    if (mountGenRef.current !== gen) return;
                    setError(err instanceof Error ? err.message : String(err));
                    setPageState('error');
                  });
              }}
            >
              {t('work.retry')}
            </button>
          </div>
        )}

        {pageState === 'ready' && works.length === 0 && (
          <div className="work-page__empty" data-testid="work-empty">
            <p>{t('work.empty')}</p>
          </div>
        )}

        {pageState === 'ready' && works.length > 0 && (
          <ul className="work-page__list" data-testid="work-list">
            {works.map((w) => (
              <li key={w.id} className="work-page__item" data-testid={`work-item-${w.id}`}>
                <button
                  type="button"
                  className="work-page__item-btn"
                  onClick={() => onOpenWork(w.id)}
                >
                  <span className="work-page__item-name">{w.name}</span>
                  <span className="work-page__item-state" data-state={w.state}>
                    {w.state}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <CreateWorkDialog
        open={showCreate}
        onCreate={handleCreate}
        onCancel={handleCancelCreate}
        onNameChange={handleNameChange}
        creating={creating}
        createError={createError}
      />
    </div>
  );
};
