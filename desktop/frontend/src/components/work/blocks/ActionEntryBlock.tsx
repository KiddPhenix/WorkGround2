// Action-entry renderer. Buttons emit only typed action intents; receipts are
// rendered from the Work projection by the shared action runtime.

import React from 'react';
import type { BlockActionSpec } from '../../../work/types';
import type { BlockRendererProps } from './types';
import { ActionFeedback, useActionIntent } from './actionIntent';
import { validateActionEntryData } from './schemaHelpers';
import type { SafeActionEntryData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  return schemaVersion === 1 && validateActionEntryData(data)
    ? { valid: true }
    : { valid: false, reason: 'invalid action_entry v1 data' };
}

function ActionButton({ action, renderer }: { action: BlockActionSpec; renderer: BlockRendererProps }) {
  const state = useActionIntent(renderer, action.id);
  return (
    <div className="wg2-action-block__intent" data-action-id={action.id}>
      <button
        type="button"
        className={`wg2-action-block__btn wg2-action-block__btn--${action.risk}`}
        disabled={state.disabled}
        aria-disabled={state.disabled || undefined}
        data-risk={action.risk}
        onClick={() => state.dispatch()}
      >
        {action.label}
        {action.risk !== 'read' && <span className="wg2-action-block__risk-badge">{action.risk}</span>}
      </button>
      <ActionFeedback state={state} />
    </div>
  );
}

const ActionEntryBlock: React.FC<BlockRendererProps> = (props) => {
  const data = props.block.data as SafeActionEntryData;
  return (
    <section className="wg2-action-block" aria-label={props.block.title ?? 'Available actions'}>
      <p className="wg2-action-block__desc">{data.description}</p>
      {data.lastResult && <div className="wg2-action-block__last-result" role="status">{data.lastResult}</div>}
      {props.block.actions?.length ? (
        <div className="wg2-action-block__buttons" role="group" aria-label="Actions">
          {props.block.actions.map((action) => <ActionButton key={action.id} action={action} renderer={props} />)}
        </div>
      ) : <div className="wg2-action-feedback wg2-action-feedback--disabled">No actions available</div>}
    </section>
  );
};

export default ActionEntryBlock;
