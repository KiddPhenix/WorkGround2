import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { app } from '../../lib/bridge';
import { useT } from '../../lib/i18n';
import type {
  CreateWorkInput,
  RerunMode,
  WorkArchiveState,
  WorkBlueprint,
  WorkPage as WorkPageData,
  WorkSummary,
} from '../../work/types';

type PageState = 'loading' | 'ready' | 'error';

const fallbackBlankBlueprint: WorkBlueprint = {
  schemaVersion: 1,
  id: 'blueprint:blank',
  version: 1,
  name: '空白 Work',
  description: '从空白 Prompt 开始',
  source: 'system',
  promptTemplate: '',
  workflow: { stages: [] },
  blockSpecs: [],
  createdAt: '',
};

function requestID(prefix: string): string {
  const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `work-${prefix}-${suffix}`;
}

function blueprintKey(blueprint: WorkBlueprint): string {
  return `${blueprint.id}\u0000${blueprint.schemaVersion}\u0000${blueprint.version}`;
}

interface CreateDialogProps {
  open: boolean;
  blueprints: WorkBlueprint[];
  creating: boolean;
  error: string | null;
  onCancel: () => void;
  onIntentChange: () => void;
  onCreate: (input: Omit<CreateWorkInput, 'requestId'>, signature: string) => void;
}

const CreateWorkDialog: React.FC<CreateDialogProps> = ({
  open, blueprints, creating, error, onCancel, onIntentChange, onCreate,
}) => {
  const [name, setName] = useState('');
  const [blueprintID, setBlueprintID] = useState('');
  const [inputsText, setInputsText] = useState('{}');
  const [inputError, setInputError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setName('');
    setBlueprintID(blueprints[0] ? blueprintKey(blueprints[0]) : '');
    setInputsText('{}');
    setInputError(null);
  }, [blueprints, open]);

  if (!open) return null;
  const blueprint = blueprints.find((item) => blueprintKey(item) === blueprintID) ?? blueprints[0];

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!blueprint || !name.trim() || creating) return;
    try {
      const parsed = JSON.parse(inputsText || '{}') as unknown;
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('Inputs 必须是 JSON 对象');
      const inputs = parsed as Record<string, unknown>;
      const input = {
        blueprintRef: { id: blueprint.id, schemaVersion: blueprint.schemaVersion, version: blueprint.version },
        name: name.trim(),
        inputs,
      };
      setInputError(null);
      onCreate(input, JSON.stringify(input));
    } catch (cause) {
      setInputError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  return (
    <div className="work-page__dialog-overlay" data-testid="work-create-dialog" onClick={onCancel}>
      <form className="work-page__dialog" data-testid="work-create-form" onClick={(event) => event.stopPropagation()} onSubmit={submit}>
        <h2 className="work-page__dialog-title">新建 Work</h2>
        <label className="work-page__dialog-label">
          名称
          <input
            className="work-page__dialog-input"
            data-testid="work-create-name"
            value={name}
            maxLength={120}
            autoFocus
            disabled={creating}
            onChange={(event) => { setName(event.target.value); onIntentChange(); }}
          />
        </label>
        <label className="work-page__dialog-label">
          Blueprint
          <select data-testid="work-create-blueprint" value={blueprint ? blueprintKey(blueprint) : ''} disabled={creating} onChange={(event) => { setBlueprintID(event.target.value); onIntentChange(); }}>
            {blueprints.map((item) => <option key={blueprintKey(item)} value={blueprintKey(item)}>{item.name} · v{item.version}</option>)}
          </select>
        </label>
        {blueprint?.description && <p>{blueprint.description}</p>}
        <label className="work-page__dialog-label">
          Inputs（JSON）
          <textarea
            data-testid="work-create-inputs"
            rows={6}
            value={inputsText}
            disabled={creating}
            onChange={(event) => { setInputsText(event.target.value); onIntentChange(); }}
          />
        </label>
        {(inputError || error) && <p className="work-page__dialog-error" data-testid="work-create-error" role="alert">{inputError ?? error}</p>}
        <div className="work-page__dialog-actions">
          <button type="button" data-testid="work-create-cancel" onClick={onCancel} disabled={creating}>取消</button>
          <button type="submit" data-testid="work-create-submit" disabled={!name.trim() || !blueprint || creating}>
            {creating ? '创建中…' : '创建'}
          </button>
        </div>
      </form>
    </div>
  );
};

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
  const [blueprints, setBlueprints] = useState<WorkBlueprint[]>([]);
  const [archiveState, setArchiveState] = useState<WorkArchiveState>('active');
  const [showCreate, setShowCreate] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [actionWorkID, setActionWorkID] = useState<string | null>(null);
  const mountGenRef = useRef(0);
  const createIntentRef = useRef<{ signature: string; requestId: string } | null>(null);
  const actionIntentsRef = useRef(new Map<string, string>());

  const load = useCallback(async (state: WorkArchiveState, gen = mountGenRef.current) => {
    setPageState('loading');
    setError(null);
    try {
      const [page, available] = await Promise.all([
        app.ListWorks(tabID, { limit: 100, archiveState: state }) as Promise<WorkPageData>,
        typeof app.ListWorkBlueprints === 'function'
          ? app.ListWorkBlueprints(tabID).catch(() => [fallbackBlankBlueprint])
          : Promise.resolve([fallbackBlankBlueprint]),
      ]);
      if (mountGenRef.current !== gen) return;
      setWorks(page.items);
      setBlueprints(Array.isArray(available) && available.length > 0 ? available : [fallbackBlankBlueprint]);
      setPageState('ready');
    } catch (cause) {
      if (mountGenRef.current !== gen) return;
      setError(cause instanceof Error ? cause.message : String(cause));
      setPageState('error');
    }
  }, [tabID]);

  useEffect(() => {
    if (!tabID) {
      setWorks([]);
      setPageState('ready');
      return;
    }
    const gen = ++mountGenRef.current;
    void load(archiveState, gen);
    return () => { mountGenRef.current++; };
  }, [archiveState, load]);

  const mutation = useCallback(async (key: string, workID: string, run: (requestId: string) => Promise<unknown>) => {
    const requestId = actionIntentsRef.current.get(key) ?? requestID(key);
    actionIntentsRef.current.set(key, requestId);
    setActionWorkID(workID);
    setError(null);
    try {
      await run(requestId);
      actionIntentsRef.current.delete(key);
      await load(archiveState);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setActionWorkID(null);
    }
  }, [archiveState, load]);

  const create = useCallback((input: Omit<CreateWorkInput, 'requestId'>, signature: string) => {
    if (createIntentRef.current?.signature !== signature) {
      createIntentRef.current = { signature, requestId: requestID('create') };
    }
    const intent = createIntentRef.current;
    const gen = mountGenRef.current;
    setCreating(true);
    setCreateError(null);
    void app.CreateWork(tabID, { ...input, requestId: intent.requestId })
      .then(async (work) => {
        if (mountGenRef.current !== gen) return;
        createIntentRef.current = null;
        setCreating(false);
        setShowCreate(false);
        await load('active', gen);
        if (mountGenRef.current === gen) onOpenWork(work.id);
      })
      .catch((cause: unknown) => {
        if (mountGenRef.current !== gen) return;
        setCreating(false);
        setCreateError(cause instanceof Error ? cause.message : String(cause));
      });
  }, [load, onOpenWork, tabID]);

  const rerun = useCallback(async (workID: string, mode: RerunMode) => {
    const key = `rerun:${mode}:${workID}`;
    await mutation(key, workID, async (id) => {
      const plan = await app.PrepareWorkRerun(tabID, { recordId: workID, mode });
      if (plan.blocking) throw new Error(plan.warnings?.join('；') || '重执行预检存在阻断');
      const created = await app.ExecuteWorkRerun(tabID, plan.planToken, id);
      onOpenWork(created.id);
    });
  }, [mutation, onOpenWork, tabID]);

  const emptyText = useMemo(() => archiveState === 'active' ? t('work.empty') : archiveState === 'archived' ? '暂无归档 Work' : '回收站为空', [archiveState, t]);

  return (
    <div className="work-page" data-testid="work-page">
      <header className="work-page__header">
        <button type="button" data-testid="work-back-btn" onClick={onBack}>← {t('work.backToSession')}</button>
        <h1 className="work-page__title">{t('work.title')}</h1>
        <button type="button" data-testid="work-new-btn" onClick={() => { setShowCreate(true); setCreateError(null); }} disabled={creating || blueprints.length === 0}>
          {t('work.newWork')}
        </button>
      </header>

      <nav className="work-page__filters" aria-label="Work 状态">
        {(['active', 'archived', 'deleted'] as const).map((state) => (
          <button key={state} type="button" aria-pressed={archiveState === state} onClick={() => setArchiveState(state)}>
            {state === 'active' ? '进行中' : state === 'archived' ? '历史' : '回收站'}
          </button>
        ))}
      </nav>

      <div className="work-page__body">
        {error && <div className="work-page__error" data-testid="work-error" role="alert"><p>{error}</p><button type="button" data-testid="work-retry-btn" onClick={() => void load(archiveState)}>重试</button></div>}
        {pageState === 'loading' && <div data-testid="work-loading">{t('work.loading')}</div>}
        {pageState === 'ready' && works.length === 0 && (
          <div data-testid="work-empty">
            <p>{emptyText}</p>
            {archiveState === 'active' && (
              <button type="button" data-testid="work-empty-new-btn" onClick={() => setShowCreate(true)} disabled={creating || blueprints.length === 0}>
                {t('work.newWork')}
              </button>
            )}
          </div>
        )}
        {pageState === 'ready' && works.length > 0 && (
          <ul className="work-page__list" data-testid="work-list">
            {works.map((work) => {
              const pending = actionWorkID === work.id;
              return (
                <li key={work.id} className="work-page__item" data-testid={`work-item-${work.id}`}>
                  <button type="button" className="work-page__item-btn" onClick={() => onOpenWork(work.id)} disabled={pending}>
                    <span className="work-page__item-name">{work.name}</span>
                    <span className="work-page__item-state" data-state={work.state}>{work.state}</span>
                  </button>
                  <div className="work-page__item-actions">
                    {archiveState === 'active' && <>
                      <button type="button" disabled={pending} onClick={() => void mutation(`copy:${work.id}`, work.id, (id) => app.CopyWork(tabID, { sourceWorkId: work.id, requestId: id }))}>复制</button>
                      <button type="button" disabled={pending} onClick={() => void mutation(`archive:${work.id}`, work.id, (id) => app.ArchiveWork(tabID, work.id, id))}>归档</button>
                    </>}
                    {archiveState === 'archived' && <>
                      <button type="button" disabled={pending} onClick={() => void rerun(work.id, 'original_definition')}>按原定义重执行</button>
                      <button type="button" disabled={pending} onClick={() => void rerun(work.id, 'latest_definition')}>按最新定义重执行</button>
                      <button type="button" disabled={pending} onClick={() => void mutation(`restore:${work.id}`, work.id, (id) => app.RestoreWork(tabID, work.id, id))}>恢复</button>
                    </>}
                    {archiveState === 'deleted' && <button type="button" disabled={pending} onClick={() => void mutation(`restore-trash:${work.id}`, work.id, (id) => app.RestoreWork(tabID, work.id, id))}>恢复</button>}
                    {archiveState !== 'deleted' && (
                      <button
                        type="button"
                        disabled={pending}
                        onClick={() => {
                          if (window.confirm(`将“${work.name}”移入回收站？`)) {
                            void mutation(`delete:${work.id}`, work.id, (id) => app.DeleteWork(tabID, work.id, id));
                          }
                        }}
                      >
                        删除
                      </button>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      <CreateWorkDialog
        open={showCreate}
        blueprints={blueprints}
        creating={creating}
        error={createError}
        onCancel={() => { createIntentRef.current = null; setShowCreate(false); setCreateError(null); }}
        onIntentChange={() => {
          if (!creating) createIntentRef.current = null;
        }}
        onCreate={create}
      />
    </div>
  );
};
