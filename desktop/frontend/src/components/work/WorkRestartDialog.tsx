import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Check, ChevronDown, RotateCcw, Save, X } from 'lucide-react';

import type {
  CreateReusableWorkSessionResult,
  ReusableField,
  ReusableFlow,
  ReusableFlowSetup,
} from '../../work/types';

type DialogStep = 'choose' | 'loading' | 'save' | 'run';

export interface WorkRestartDialogProps {
  open: boolean;
  workId: string;
  workName: string;
  runId: string;
  onClose: () => void;
  onRestartCurrent: (input: { workId: string; runId: string; requestId: string }) => Promise<unknown>;
  onPrepareFlow: (input: { sourceWorkId: string }) => Promise<ReusableFlowSetup>;
  onSaveFlow: (input: { sourceWorkId: string; name: string; variableKeys: string[]; requestId: string }) => Promise<ReusableFlow>;
  onCreateSession: (input: { flowId: string; values: Record<string, unknown>; requestId: string }) => Promise<CreateReusableWorkSessionResult>;
}

function requestID(prefix: string): string {
  const suffix = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `work-${prefix}-${suffix}`;
}

function fieldText(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return JSON.stringify(value, null, 2);
}

function fieldValue(field: ReusableField, text: string): unknown {
  const value = text.trim();
  if (field.kind === 'number' || field.kind === 'integer') {
    const number = Number(value);
    if (!Number.isFinite(number)) throw new Error(`${field.label}需要填写有效数字。`);
    return number;
  }
  if (field.kind === 'form' || field.kind === 'roster' || field.kind === 'multi_choice' || field.kind === 'array' || field.kind === 'object') {
    try {
      return JSON.parse(value);
    } catch {
      throw new Error(`${field.label}需要填写有效的 JSON。`);
    }
  }
  return text;
}

export const WorkRestartDialog: React.FC<WorkRestartDialogProps> = ({
  open,
  workId,
  workName,
  runId,
  onClose,
  onRestartCurrent,
  onPrepareFlow,
  onSaveFlow,
  onCreateSession,
}) => {
  const [step, setStep] = useState<DialogStep>('choose');
  const [setup, setSetup] = useState<ReusableFlowSetup | null>(null);
  const [flow, setFlow] = useState<ReusableFlow | null>(null);
  const [name, setName] = useState(workName);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const restartIntent = useRef<string | null>(null);
  const saveIntent = useRef<{ signature: string; requestId: string } | null>(null);
  const runIntent = useRef<{ signature: string; requestId: string } | null>(null);

  useEffect(() => {
    if (!open) return;
    setStep('choose');
    setSetup(null);
    setFlow(null);
    setName(workName);
    setSelected(new Set());
    setDrafts({});
    setBusy(false);
    setError(null);
    restartIntent.current = null;
    saveIntent.current = null;
    runIntent.current = null;
  }, [open, workId, workName]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !busy) onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [busy, onClose, open]);

  const variableFields = useMemo(
    () => (flow?.fields ?? []).filter((field) => field.variable),
    [flow],
  );

  if (!open) return null;

  const close = () => {
    if (!busy) onClose();
  };

  const restartCurrent = async () => {
    if (busy) return;
    restartIntent.current ??= requestID('restart-current');
    setBusy(true);
    setError(null);
    try {
      await onRestartCurrent({ workId, runId, requestId: restartIntent.current });
      restartIntent.current = null;
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const prepareFlow = async () => {
    if (busy) return;
    setStep('loading');
    setBusy(true);
    setError(null);
    try {
      const prepared = await onPrepareFlow({ sourceWorkId: workId });
      setSetup(prepared);
      if (prepared.existing) {
        setFlow(prepared.existing);
        setDrafts(Object.fromEntries(prepared.existing.fields
          .filter((field) => field.variable)
          .map((field) => [field.key, fieldText(field.value)])));
        setStep('run');
      } else {
        setName(prepared.suggestedName || workName);
        setSelected(new Set(prepared.fields.map((field) => field.key)));
        setStep('save');
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      setStep('choose');
    } finally {
      setBusy(false);
    }
  };

  const saveFlow = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!setup || busy || !name.trim()) return;
    const variableKeys = setup.fields.filter((field) => selected.has(field.key)).map((field) => field.key);
    const signature = JSON.stringify([workId, name.trim(), variableKeys]);
    if (saveIntent.current?.signature !== signature) {
      saveIntent.current = { signature, requestId: requestID('save-flow') };
    }
    setBusy(true);
    setError(null);
    try {
      const saved = await onSaveFlow({
        sourceWorkId: workId,
        name: name.trim(),
        variableKeys,
        requestId: saveIntent.current.requestId,
      });
      saveIntent.current = null;
      setFlow(saved);
      setDrafts(Object.fromEntries(saved.fields
        .filter((field) => field.variable)
        .map((field) => [field.key, fieldText(field.value)])));
      setStep('run');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const runFlow = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!flow || busy) return;
    setError(null);
    let values: Record<string, unknown>;
    try {
      values = Object.fromEntries(variableFields.map((field) => {
        const text = drafts[field.key] ?? '';
        if (field.required && !text.trim()) throw new Error(`${field.label}不能为空。`);
        return [field.key, fieldValue(field, text)];
      }));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      return;
    }
    const signature = JSON.stringify([flow.id, values]);
    if (runIntent.current?.signature !== signature) {
      runIntent.current = { signature, requestId: requestID('run-flow') };
    }
    setBusy(true);
    try {
      const result = await onCreateSession({ flowId: flow.id, values, requestId: runIntent.current.requestId });
      if (result.error || !result.run?.work?.id) throw new Error(result.error || '新工作创建失败，请重试。');
      runIntent.current = null;
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="wg2-restart-dialog__overlay" data-testid="work-restart-dialog" onMouseDown={close}>
      <section
        className="wg2-restart-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="wg2-restart-dialog-title"
        aria-busy={busy}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="wg2-restart-dialog__header">
          <div>
            <h2 id="wg2-restart-dialog-title">
              {step === 'save' ? '保存为常用工作' : step === 'run' ? '再次运行' : '重新开始'}
            </h2>
            <p>
              {step === 'save' && '下次只需填写变化的内容'}
              {step === 'run' && flow?.name}
              {(step === 'choose' || step === 'loading') && '选择这次要怎样开始'}
            </p>
          </div>
          <button type="button" className="wg2-restart-dialog__close" aria-label="关闭" disabled={busy} onClick={close}>
            <X size={16} aria-hidden="true" />
          </button>
        </header>

        {step === 'choose' || step === 'loading' ? (
          <div className="wg2-restart-dialog__choices">
            <button type="button" data-testid="restart-current-choice" disabled={busy} onClick={() => void restartCurrent()}>
              <span className="wg2-restart-dialog__choice-icon"><RotateCcw size={17} aria-hidden="true" /></span>
              <span><strong>重启当前运行</strong><small>终止当前运行，并在这个工作里从头开始。</small></span>
            </button>
            <button type="button" data-testid="save-and-rerun-choice" disabled={busy} onClick={() => void prepareFlow()}>
              <span className="wg2-restart-dialog__choice-icon"><Save size={17} aria-hidden="true" /></span>
              <span><strong>保存流程并再次运行</strong><small>保留当前工作，按固定流程创建一个新的工作。</small></span>
            </button>
            {step === 'loading' && <div className="wg2-restart-dialog__loading" role="status">正在读取常用流程…</div>}
          </div>
        ) : null}

        {step === 'save' && setup ? (
          <form onSubmit={saveFlow}>
            <div className="wg2-restart-dialog__body">
              <label className="wg2-restart-dialog__label">
                <span>名称</span>
                <input data-testid="reusable-flow-name" value={name} autoFocus disabled={busy} onChange={(event) => {
                  setName(event.target.value);
                  saveIntent.current = null;
                }} />
              </label>
              <div className="wg2-restart-dialog__rule-group">
                <div className="wg2-restart-dialog__rule-title"><strong>每次填写</strong><span>选择每次运行前会变化的内容</span></div>
                <div className="wg2-restart-dialog__field-pills">
                  {setup.fields.map((field) => {
                    const active = selected.has(field.key);
                    return (
                      <label key={field.key} data-selected={active ? 'true' : 'false'}>
                        <input type="checkbox" checked={active} disabled={busy} onChange={() => {
                          setSelected((current) => {
                            const next = new Set(current);
                            if (next.has(field.key)) next.delete(field.key);
                            else next.add(field.key);
                            return next;
                          });
                          saveIntent.current = null;
                        }} />
                        <span className="wg2-restart-dialog__check">{active && <Check size={11} aria-hidden="true" />}</span>
                        {field.label}
                      </label>
                    );
                  })}
                </div>
              </div>
              <div className="wg2-restart-dialog__rule-group wg2-restart-dialog__rule-group--fixed">
                <div className="wg2-restart-dialog__rule-title"><strong>固定沿用</strong><span>{setup.fixedItems.join('、')}</span></div>
              </div>
            </div>
            {error && <p className="wg2-restart-dialog__error" role="alert">{error}</p>}
            <footer className="wg2-restart-dialog__footer">
              <button type="button" className="wg2-restart-dialog__secondary" disabled={busy} onClick={close}>取消</button>
              <button type="submit" className="wg2-restart-dialog__primary" disabled={busy || !name.trim()}>{busy ? '保存中…' : '保存并继续'}</button>
            </footer>
          </form>
        ) : null}

        {step === 'run' && flow ? (
          <form onSubmit={runFlow}>
            <div className="wg2-restart-dialog__body wg2-restart-dialog__run-fields">
              {variableFields.length === 0 ? (
                <p className="wg2-restart-dialog__empty">这个流程没有变化项，将直接按保存时的内容运行。</p>
              ) : variableFields.map((field, index) => (
                <label className="wg2-restart-dialog__label" key={field.key}>
                  <span>{field.label}{field.required && <em>必填</em>}</span>
                  <input
                    data-testid={`reusable-field-${field.key}`}
                    value={drafts[field.key] ?? ''}
                    autoFocus={index === 0}
                    disabled={busy}
                    inputMode={field.kind === 'number' || field.kind === 'integer' ? 'decimal' : undefined}
                    onChange={(event) => {
                      setDrafts((current) => ({ ...current, [field.key]: event.target.value }));
                      runIntent.current = null;
                    }}
                  />
                </label>
              ))}
              <details className="wg2-restart-dialog__fixed-details">
                <summary><ChevronDown size={14} aria-hidden="true" />固定沿用的内容</summary>
                <p>{(setup?.fixedItems ?? ['工作结构', '工具与执行方式', '成果格式']).join('、')}</p>
              </details>
            </div>
            {error && <p className="wg2-restart-dialog__error" role="alert">{error}</p>}
            <footer className="wg2-restart-dialog__footer">
              <button type="button" className="wg2-restart-dialog__secondary" disabled={busy} onClick={close}>取消</button>
              <button type="submit" className="wg2-restart-dialog__primary" disabled={busy}>{busy ? '正在创建…' : '创建并运行'}</button>
            </footer>
          </form>
        ) : null}

        {(step === 'choose' || step === 'loading') && error ? (
          <p className="wg2-restart-dialog__error" role="alert">{error}</p>
        ) : null}
      </section>
    </div>
  );
};

export default WorkRestartDialog;
