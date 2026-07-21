// Structured approval renderer. Item choices are draft UI state; only the
// Controller receipt represents a submitted approval/rejection.

import React, { useMemo, useState } from 'react';
import { PromptAction, PromptBadge, PromptShelf } from '../../PromptShelf';
import type { BlockRendererProps } from './types';
import { ActionFeedback, useActionIntent } from './actionIntent';
import { validateApprovalData } from './schemaHelpers';
import type { SafeApprovalData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  return schemaVersion === 1 && validateApprovalData(data)
    ? { valid: true }
    : { valid: false, reason: 'invalid approval v1 data' };
}

type Verdict = 'approved' | 'rejected';

const ApprovalBlock: React.FC<BlockRendererProps> = (props) => {
  const data = props.block.data as SafeApprovalData;
  const approve = props.block.actions?.find((action) => /approve|allow|confirm/i.test(action.id));
  const reject = props.block.actions?.find((action) => /reject|deny/i.test(action.id));
  const approveState = useActionIntent(props, approve?.id ?? 'approve_unavailable', true);
  const rejectState = useActionIntent(props, reject?.id ?? 'reject_unavailable', true);
  const [verdicts, setVerdicts] = useState<Record<string, Verdict>>({});
  const payload = useMemo(() => ({
    verdicts: data.items.map((item) => ({ id: item.id, verdict: verdicts[item.id] ?? 'pending' })),
  }), [data.items, verdicts]);
  const draftDisabled = (!approve || approveState.disabled) && (!reject || rejectState.disabled);

  return (
    <PromptShelf
      className="wg2-approval-block"
      cardClassName="wg2-approval-block__card"
      titleId={`approval-${props.block.id}`}
      title={data.title}
      badges={<PromptBadge>Approval</PromptBadge>}
      meta={data.description ?? data.context}
      role="region"
      actions={<>
        <PromptAction
          keyLabel=""
          label={approve?.label ?? 'Approve'}
          onClick={() => approveState.dispatch({ ...payload, decision: 'approved' })}
          primary
          disabled={!approve || approveState.disabled}
        />
        <PromptAction
          keyLabel=""
          label={reject?.label ?? 'Reject'}
          onClick={() => rejectState.dispatch({ ...payload, decision: 'rejected' })}
          quiet
          disabled={!reject || rejectState.disabled}
        />
      </>}
    >
      {data.context && data.description && <p className="wg2-approval-block__context">{data.context}</p>}
      <ul className="wg2-approval-block__items">
        {data.items.map((item) => {
          const verdict = verdicts[item.id];
          return (
            <li key={item.id} className={`wg2-approval-block__item${verdict ? ` wg2-approval-block__item--${verdict}` : ''}`}>
              <div className="wg2-approval-block__item-header">
                <span className="wg2-approval-block__item-label">{item.label}</span>
                {item.risk && <span className="wg2-approval-block__risk">{item.risk}</span>}
              </div>
              {item.detail && <p className="wg2-approval-block__item-detail">{item.detail}</p>}
              <div className="wg2-approval-block__item-actions" role="group" aria-label={`Draft verdict for ${item.label}`}>
                <button
                  type="button"
                  className={`wg2-approval-block__btn${verdict === 'approved' ? ' wg2-approval-block__btn--active' : ''}`}
                  disabled={draftDisabled}
                  aria-pressed={verdict === 'approved'}
                  onClick={() => setVerdicts((current) => ({ ...current, [item.id]: 'approved' }))}
                >Approve</button>
                <button
                  type="button"
                  className={`wg2-approval-block__btn${verdict === 'rejected' ? ' wg2-approval-block__btn--active' : ''}`}
                  disabled={draftDisabled}
                  aria-pressed={verdict === 'rejected'}
                  onClick={() => setVerdicts((current) => ({ ...current, [item.id]: 'rejected' }))}
                >Reject</button>
              </div>
            </li>
          );
        })}
      </ul>
      {!approve && !reject && <div className="wg2-action-feedback wg2-action-feedback--disabled">No approval actions available</div>}
      {approve && <ActionFeedback state={approveState} />}
      {reject && <ActionFeedback state={rejectState} />}
    </PromptShelf>
  );
};

export default ApprovalBlock;
