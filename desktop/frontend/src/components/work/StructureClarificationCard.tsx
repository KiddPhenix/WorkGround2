import React, { useEffect, useMemo, useRef, useState } from 'react';
import { ArrowRight, Check, ListTree, X } from 'lucide-react';

import type {
  DefinitionStructuralAnswer,
  DefinitionStructuralClarification,
} from '../../work/types_v2';

export interface StructureClarificationCardProps {
  clarification: DefinitionStructuralClarification;
  busy: boolean;
  error?: string | null;
  onClose: () => void;
  onSubmit: (answer: DefinitionStructuralAnswer) => void | Promise<void>;
}

const impactLabels: Record<DefinitionStructuralClarification['impact'], string> = {
  task_nodes: '会改变任务节点',
  task_dependencies: '会改变任务节点与依赖',
  input_slots: '会改变输入节点',
  artifact_slots: '会改变产物节点',
};

export const StructureClarificationCard: React.FC<StructureClarificationCardProps> = ({
  clarification,
  busy,
  error,
  onClose,
  onSubmit,
}) => {
  const defaultOption = useMemo(
    () => clarification.options.find((option) => option.recommended)?.id ?? '',
    [clarification],
  );
  const [selectedID, setSelectedID] = useState(defaultOption);
  const [customValue, setCustomValue] = useState('');
  const dialogRef = useRef<HTMLDivElement>(null);
  const selected = clarification.options.find((option) => option.id === selectedID);
  const answerValue = selected?.custom ? customValue.trim() : selected?.label ?? '';
  const canSubmit = !!selected && (!selected.custom || !!answerValue) && !busy;

  useEffect(() => {
    setSelectedID(defaultOption);
    setCustomValue('');
  }, [clarification.id, defaultOption]);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    dialog.querySelector<HTMLElement>('[data-selected="true"]')?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !busy) {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = [...dialog.querySelectorAll<HTMLElement>(
        'button:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
      )];
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [busy, onClose]);

  return (
    <div className="wg2-structure-clarification__scrim" data-testid="structure-clarification-scrim">
      <div
        ref={dialogRef}
        className="wg2-structure-clarification"
        role="dialog"
        aria-modal="true"
        aria-labelledby="wg2-structure-clarification-title"
        aria-describedby="wg2-structure-clarification-question"
        data-testid="structure-clarification-card"
      >
        <header className="wg2-structure-clarification__header">
          <div>
            <span className="wg2-structure-clarification__eyebrow">只补充无法推导的工作结构</span>
            <h2 id="wg2-structure-clarification-title">选择工作结构</h2>
          </div>
          <div className="wg2-structure-clarification__header-actions">
            <span>1 / 1</span>
            <button
              type="button"
              className="wg2-structure-clarification__close"
              aria-label="稍后回答结构问题"
              disabled={busy}
              onClick={onClose}
            >
              <X size={18} aria-hidden="true" />
            </button>
          </div>
        </header>

        <div className="wg2-structure-clarification__impact">
          <span aria-hidden="true">◆</span>
          {impactLabels[clarification.impact]}
        </div>

        <div className="wg2-structure-clarification__body">
          <section className="wg2-structure-clarification__context" aria-label="当前工作流">
            <span className="wg2-structure-clarification__section-label">当前推演出的流程</span>
            <div className="wg2-structure-clarification__flow">
              {clarification.flow.map((step, index) => (
                <React.Fragment key={`${step}-${index}`}>
                  <span className="wg2-structure-clarification__flow-step">{step}</span>
                  {index < clarification.flow.length - 1 && (
                    <ArrowRight size={14} aria-hidden="true" />
                  )}
                </React.Fragment>
              ))}
            </div>
            <p id="wg2-structure-clarification-question">{clarification.question}</p>
            {clarification.description && <small>{clarification.description}</small>}
          </section>

          <fieldset className="wg2-structure-clarification__options" disabled={busy}>
            <legend className="sr-only">选择工作结构</legend>
            {clarification.options.map((option) => {
              const checked = option.id === selectedID;
              return (
                <button
                  key={option.id}
                  type="button"
                  role="radio"
                  aria-checked={checked}
                  data-selected={checked ? 'true' : 'false'}
                  className="wg2-structure-clarification__option"
                  onClick={() => setSelectedID(option.id)}
                >
                  <span className="wg2-structure-clarification__radio">
                    {checked && <Check size={13} strokeWidth={3} aria-hidden="true" />}
                  </span>
                  <ListTree className="wg2-structure-clarification__option-icon" size={19} aria-hidden="true" />
                  <span>
                    <strong>
                      {option.label}
                      {option.recommended && <em>推荐</em>}
                    </strong>
                    {option.description && <small>{option.description}</small>}
                  </span>
                </button>
              );
            })}
            {selected?.custom && (
              <textarea
                data-testid="structure-clarification-custom"
                value={customValue}
                rows={2}
                maxLength={240}
                autoFocus
                placeholder={clarification.customPlaceholder}
                onChange={(event) => setCustomValue(event.target.value)}
              />
            )}
          </fieldset>
        </div>

        <footer className="wg2-structure-clarification__footer">
          <div>
            {error && <span role="alert">{error}</span>}
          </div>
          <div className="wg2-structure-clarification__footer-actions">
            <button type="button" className="wg2-structure-clarification__back" disabled={busy} onClick={onClose}>
              返回补充任务说明
            </button>
            <button
              type="button"
              className="wg2-structure-clarification__confirm"
              disabled={!canSubmit}
              aria-busy={busy}
              onClick={() => {
                if (!selected || !canSubmit) return;
                void onSubmit({
                  questionId: clarification.id,
                  optionId: selected.id,
                  value: answerValue,
                });
              }}
            >
              {busy ? '正在继续规划…' : '采用此结构并继续'}
            </button>
          </div>
        </footer>
      </div>
    </div>
  );
};
