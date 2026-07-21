// Structured decision renderer built on the shared PromptShelf presentation.

import React, { useCallback, useState } from 'react';
import { PromptAction, PromptShelf } from '../../PromptShelf';
import type { BlockRendererProps } from './types';
import { ActionFeedback, useActionIntent } from './actionIntent';
import { validateDecisionData } from './schemaHelpers';
import type { SafeDecisionData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  return schemaVersion === 1 && validateDecisionData(data)
    ? { valid: true }
    : { valid: false, reason: 'invalid decision v1 data' };
}

const DecisionBlock: React.FC<BlockRendererProps> = (props) => {
  const data = props.block.data as SafeDecisionData;
  const action = props.block.actions?.find((candidate) => /decide|answer|submit/i.test(candidate.id)) ?? props.block.actions?.[0];
  const state = useActionIntent(props, action?.id ?? 'decision_unavailable', true);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const unavailable = !action || state.disabled;
  const multi = data.multiSelect === true;

  const choose = useCallback((optionID: string) => {
    if (unavailable) return;
    if (!multi) {
      setSelected(new Set([optionID]));
      state.dispatch({ selected: [optionID] });
      return;
    }
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(optionID)) next.delete(optionID);
      else next.add(optionID);
      return next;
    });
  }, [multi, state, unavailable]);

  const submit = useCallback(() => {
    if (unavailable || selected.size === 0) return;
    state.dispatch({ selected: [...selected].sort() });
  }, [selected, state, unavailable]);

  return (
    <PromptShelf
      className="wg2-decision-block"
      cardClassName="wg2-decision-block__card"
      titleId={`decision-${props.block.id}`}
      title={data.question}
      meta={data.context}
      role="region"
      actions={multi ? (
        <PromptAction
          keyLabel=""
          label={`Confirm selection (${selected.size})`}
          onClick={submit}
          primary
          disabled={unavailable || selected.size === 0}
        />
      ) : undefined}
    >
      {multi && <p className="wg2-decision-block__hint">Select one or more options</p>}
      <div className="wg2-decision-block__options" role={multi ? 'group' : 'radiogroup'}>
        {data.options.map((option) => {
          const checked = selected.has(option.id);
          return (
            <button
              key={option.id}
              type="button"
              className={`wg2-decision-block__opt-btn${checked ? ' wg2-decision-block__opt-btn--selected' : ''}`}
              role={multi ? 'checkbox' : 'radio'}
              aria-checked={checked}
              disabled={unavailable}
              onClick={() => choose(option.id)}
            >
              <span className="wg2-decision-block__opt-label">{option.label}</span>
              {option.description && <span className="wg2-decision-block__opt-desc">{option.description}</span>}
            </button>
          );
        })}
      </div>
      {!action && <div className="wg2-action-feedback wg2-action-feedback--disabled">No decision action available</div>}
      <ActionFeedback state={state} />
    </PromptShelf>
  );
};

export default DecisionBlock;
