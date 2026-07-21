// Notification renderer with an optional Controller-owned retry intent.

import React from 'react';
import type { BlockRendererProps } from './types';
import { ActionFeedback, useActionIntent } from './actionIntent';
import { validateNoticeData } from './schemaHelpers';
import type { SafeNoticeData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  return schemaVersion === 1 && validateNoticeData(data)
    ? { valid: true }
    : { valid: false, reason: 'invalid notice v1 data' };
}

const ICONS = { info: 'ℹ', warning: '⚠', error: '✕', success: '✓' } as const;

const NoticeBlock: React.FC<BlockRendererProps> = (props) => {
  const data = props.block.data as SafeNoticeData;
  const action = props.block.actions?.find((candidate) => /retry|refresh/i.test(candidate.id)) ?? props.block.actions?.[0];
  const state = useActionIntent(props, action?.id ?? 'notice_unavailable');
  return (
    <section
      className={`wg2-notice-block wg2-notice-block--${data.level}`}
      role={data.level === 'error' || data.level === 'warning' ? 'alert' : 'status'}
      aria-label={props.block.title ?? `${data.level} notice`}
    >
      <span className="wg2-notice-block__icon" aria-hidden="true">{ICONS[data.level]}</span>
      <div className="wg2-notice-block__body">
        <p className="wg2-notice-block__content">{data.content}</p>
        {data.retryable && (
          <>
            <button
              type="button"
              className="wg2-notice-block__retry"
              disabled={!action || state.disabled}
              onClick={() => state.dispatch()}
            >{data.actionLabel ?? action?.label ?? 'Retry'}</button>
            {!action && <div className="wg2-action-feedback wg2-action-feedback--disabled">No retry action available</div>}
            {action && <ActionFeedback state={state} />}
          </>
        )}
      </div>
    </section>
  );
};

export default NoticeBlock;
