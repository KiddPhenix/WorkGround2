import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { app } from '../../lib/bridge';
import { useT } from '../../lib/i18n';
import { WorkControllerAdapter } from '../../work/controller';
import { createWailsWorkControllerPort } from '../../work/wailsAdapter';
import type {
  CreateWorkInput,
  RerunMode,
  WorkArchiveState,
  WorkPage as WorkPageData,
  WorkSummary,
} from '../../work/types';

type PageState = 'loading' | 'ready' | 'error';

const blankBlueprintRef = {
  schemaVersion: 1,
  id: 'blueprint:blank',
  version: 1,
};

function requestID(prefix: string): string {
  const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `work-${prefix}-${suffix}`;
}

interface CreateDialogProps {
  open: boolean;
  creating: boolean;
  error: string | null;
  onCancel: () => void;
  onIntentChange: () => void;
  onCreate: (input: Omit<CreateWorkInput, 'requestId'>, signature: string) => void;
}

const CreateWorkDialog: React.FC<CreateDialogProps> = ({
  open, creating, error, onCancel, onIntentChange, onCreate,
}) => {
  const [prompt, setPrompt] = useState('');

  useEffect(() => {
    if (!open) return;
    setPrompt('');
  }, [open]);

  if (!open) return null;
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    const task = prompt.trim();
    if (!task || creating) return;
    const input = {
      blueprintRef: blankBlueprintRef,
      prompt: task,
    };
    onCreate(input, JSON.stringify(input));
  };

  return (
    <div className="work-page__dialog-overlay" data-testid="work-create-dialog" onClick={onCancel}>
      <form className="work-page__dialog" data-testid="work-create-form" onClick={(event) => event.stopPropagation()} onSubmit={submit}>
        <h2 className="work-page__dialog-title">新建任务</h2>
        <p className="work-page__dialog-description">直接描述你想完成的事情，其余设置由 Work 自动处理。</p>
        <label className="work-page__dialog-label">
          任务说明
          <textarea
            className="work-page__dialog-input work-page__dialog-prompt"
            data-testid="work-create-prompt"
            value={prompt}
            rows={7}
            autoFocus
            disabled={creating}
            placeholder="例如：整理这次生日派对的时间、地点和邀请名单，并给出一份可执行的准备清单。"
            onChange={(event) => { setPrompt(event.target.value); onIntentChange(); }}
          />
        </label>
        {error && <p className="work-page__dialog-error" data-testid="work-create-error" role="alert">{error}</p>}
        <div className="work-page__dialog-actions">
          <button type="button" className="work-page__dialog-btn work-page__dialog-btn--cancel" data-testid="work-create-cancel" onClick={onCancel} disabled={creating}>取消</button>
          <button type="submit" className="work-page__dialog-btn work-page__dialog-btn--create" data-testid="work-create-submit" disabled={!prompt.trim() || creating}>
            {creating ? '创建中…' : '创建任务'}
          </button>
        </div>
      </form>
    </div>
  );
};

export interface WorkPageProps {
  tabID: string;
  /** Session identity persisted by BeginWorkPlanning; tabID remains the Wails route owner. */
  sessionID?: string;
  onBack: () => void;
  onOpenWork: (workID: string) => void;
}

export const WorkPage: React.FC<WorkPageProps> = ({ tabID, sessionID, onBack, onOpenWork }) => {
  const t = useT();
  const [pageState, setPageState] = useState<PageState>('loading');
  const [error, setError] = useState<string | null>(null);
  const [works, setWorks] = useState<WorkSummary[]>([]);
  const [archiveState, setArchiveState] = useState<WorkArchiveState>('active');
  const [showCreate, setShowCreate] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [v2Enabled, setV2Enabled] = useState(false);
  const [v2FlagStatus, setV2FlagStatus] = useState<'unavailable' | 'pending' | 'ready' | 'error'>('pending');
  const [v2FlagError, setV2FlagError] = useState<string | null>(null);
  const [planning, setPlanning] = useState(false);
  const [planningError, setPlanningError] = useState<string | null>(null);
  const [actionWorkID, setActionWorkID] = useState<string | null>(null);
  const mountGenRef = useRef(0);
  const createIntentRef = useRef<{ signature: string; requestId: string } | null>(null);
  const planningIntentRef = useRef<{ sessionId: string; requestId: string } | null>(null);
  const actionIntentsRef = useRef(new Map<string, string>());

  const load = useCallback(async (state: WorkArchiveState, gen = mountGenRef.current) => {
    setPageState('loading');
    setError(null);
    try {
      const page = await app.ListWorks(tabID, { limit: 100, archiveState: state }) as WorkPageData;
      if (mountGenRef.current !== gen) return;
      setWorks(page.items);
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
      setV2Enabled(false);
      setV2FlagStatus('unavailable');
      setV2FlagError(null);
      // Reset transient state even on empty tab to avoid stale cross-tab leakage.
      setPlanning(false);
      setPlanningError(null);
      planningIntentRef.current = null;
      setCreating(false);
      setCreateError(null);
      setShowCreate(false);
      createIntentRef.current = null;
      return;
    }
    const gen = ++mountGenRef.current;
    setV2Enabled(false);
    setV2FlagStatus('pending');
    setV2FlagError(null);
    // Reset all transient create/planning state on identity change.
    setPlanning(false);
    setPlanningError(null);
    planningIntentRef.current = null;
    setCreating(false);
    setCreateError(null);
    setShowCreate(false);
    createIntentRef.current = null;
    const readV2Enabled = app.WorkCollaborationV2Enabled;
    if (typeof readV2Enabled !== 'function') {
      // Missing binding on a real tab is an error, not a silent V1 fallback.
      setV2FlagStatus('error');
      setV2FlagError('Work 协作工作台 V2 能力尚未连接，请升级 WorkGround2。');
      setV2Enabled(false);
      void load(archiveState, gen);
      return () => { mountGenRef.current++; };
    }
    void readV2Enabled(tabID)
      .then((enabled) => {
        if (mountGenRef.current === gen) {
          setV2Enabled(enabled);
          setV2FlagStatus('ready');
          setV2FlagError(null);
        }
      })
      .catch((cause: unknown) => {
        if (mountGenRef.current === gen) {
          setV2FlagStatus('error');
          setV2FlagError(cause instanceof Error ? cause.message : String(cause));
        }
      });
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

  const beginPlanning = useCallback(() => {
    if (!tabID || planning) return;
    const planningSessionID = sessionID?.trim() || tabID;
    if (planningIntentRef.current?.sessionId !== planningSessionID) {
      planningIntentRef.current = { sessionId: planningSessionID, requestId: requestID('planning') };
    }
    const intent = planningIntentRef.current;
    const gen = mountGenRef.current;
    const port = createWailsWorkControllerPort(tabID);
    if (!port) {
      setPlanningError('Work 对话规划能力尚未连接。');
      return;
    }
    const adapter = new WorkControllerAdapter(port);
    setPlanning(true);
    setPlanningError(null);
    void adapter.beginWorkPlanning({ sessionId: planningSessionID, requestId: intent.requestId })
      .then(async (result) => {
        if (mountGenRef.current !== gen) return;
        const workID = result.result?.work.id ?? result.transportError?.workId;
        if (!result.committed || !workID) {
          throw new Error(result.transportError?.message || '未能创建对话规划 Work，请重试。');
        }
        // Persist the planning face before navigation. WorkCard restoration may
        // race its first snapshot, but both sources now agree on the back face.
        await adapter.setActiveFace(workID, 'back');
        planningIntentRef.current = null;
        setPlanning(false);
        await load('active', gen);
        if (mountGenRef.current === gen) onOpenWork(workID);
      })
      .catch((cause: unknown) => {
        if (mountGenRef.current !== gen) return;
        setPlanning(false);
        setPlanningError(cause instanceof Error ? cause.message : String(cause));
      })
      .finally(() => adapter.dispose());
  }, [load, onOpenWork, planning, sessionID, tabID]);

  const retryV2Flag = useCallback(() => {
    if (!tabID) return;
    const fn = app.WorkCollaborationV2Enabled;
    if (typeof fn !== 'function') {
      setV2Enabled(false);
      setV2FlagStatus('unavailable');
      setV2FlagError(null);
      return;
    }
    const gen = mountGenRef.current;
    setV2FlagStatus('pending');
    setV2FlagError(null);
    void fn(tabID)
      .then((enabled) => {
        if (mountGenRef.current === gen) {
          setV2Enabled(enabled);
          setV2FlagStatus('ready');
          setV2FlagError(null);
        }
      })
      .catch((cause: unknown) => {
        if (mountGenRef.current === gen) {
          setV2FlagStatus('error');
          setV2FlagError(cause instanceof Error ? cause.message : String(cause));
        }
      });
  }, [tabID]);

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
        <button type="button" className="work-page__back-btn" data-testid="work-back-btn" onClick={onBack}>← {t('work.backToSession')}</button>
        <h1 className="work-page__title">{t('work.title')}</h1>
        <button
          type="button"
          className="work-page__new-btn"
          data-testid="work-new-btn"
          onClick={() => {
            if (v2Enabled) {
              beginPlanning();
            } else {
              setShowCreate(true);
              setCreateError(null);
            }
          }}
          disabled={creating || planning || v2FlagStatus === 'pending' || v2FlagStatus === 'error' || v2FlagStatus === 'unavailable'}
        >
          {v2FlagStatus === 'pending' ? '…' : planning ? '正在建立对话规划…' : t('work.newWork')}
        </button>
      </header>

      <nav className="work-page__filters" aria-label="Work 状态">
        {(['active', 'archived', 'deleted'] as const).map((state) => (
          <button
            key={state}
            type="button"
            className="work-page__filter-btn"
            aria-pressed={archiveState === state}
            onClick={() => setArchiveState(state)}
          >
            {state === 'active' ? '进行中' : state === 'archived' ? '历史' : '回收站'}
          </button>
        ))}
      </nav>

      <div className="work-page__body">
        {v2FlagStatus === 'error' && v2FlagError && (
          <div className="work-page__error" data-testid="work-v2-flag-error" role="alert">
            <p>无法确认 V2 协作工作台状态：{v2FlagError}</p>
            <button type="button" data-testid="work-v2-flag-retry" onClick={retryV2Flag}>重试</button>
          </div>
        )}
        {planningError && (
          <div className="work-page__error" data-testid="work-planning-error" role="alert">
            <p>{planningError}</p>
            <button type="button" data-testid="work-planning-retry" onClick={beginPlanning} disabled={planning}>重试</button>
          </div>
        )}
        {error && <div className="work-page__error" data-testid="work-error" role="alert"><p>{error}</p><button type="button" data-testid="work-retry-btn" onClick={() => void load(archiveState)}>重试</button></div>}
        {pageState === 'loading' && <div className="work-page__loading" data-testid="work-loading">{t('work.loading')}</div>}
        {pageState === 'ready' && works.length === 0 && (
          <div className="work-page__empty" data-testid="work-empty">
            <p>{emptyText}</p>
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
                      <button type="button" className="work-page__action-btn" disabled={pending} onClick={() => void mutation(`copy:${work.id}`, work.id, (id) => app.CopyWork(tabID, { sourceWorkId: work.id, requestId: id }))}>复制</button>
                      <button type="button" className="work-page__action-btn" disabled={pending} onClick={() => void mutation(`archive:${work.id}`, work.id, (id) => app.ArchiveWork(tabID, work.id, id))}>归档</button>
                    </>}
                    {archiveState === 'archived' && <>
                      <button type="button" className="work-page__action-btn" disabled={pending} onClick={() => void rerun(work.id, 'original_definition')}>按原定义重执行</button>
                      <button type="button" className="work-page__action-btn" disabled={pending} onClick={() => void rerun(work.id, 'latest_definition')}>按最新定义重执行</button>
                      <button type="button" className="work-page__action-btn" disabled={pending} onClick={() => void mutation(`restore:${work.id}`, work.id, (id) => app.RestoreWork(tabID, work.id, id))}>恢复</button>
                    </>}
                    {archiveState === 'deleted' && <button type="button" className="work-page__action-btn" disabled={pending} onClick={() => void mutation(`restore-trash:${work.id}`, work.id, (id) => app.RestoreWork(tabID, work.id, id))}>恢复</button>}
                    {archiveState !== 'deleted' && (
                      <button
                        type="button"
                        className="work-page__action-btn work-page__action-btn--danger"
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
