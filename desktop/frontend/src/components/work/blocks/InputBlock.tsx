// Structured user-input renderer. Draft values stay local; submission uses the
// same guarded, idempotent action path as every other interactive block.

import React, { useCallback, useRef, useState } from 'react';
import { PromptAction, PromptShelf } from '../../PromptShelf';
import type { BlockRendererProps } from './types';
import { ActionFeedback, useActionIntent } from './actionIntent';
import { validateInputData } from './schemaHelpers';
import type { SafeInputData } from './schemaHelpers';

export function validate(schemaVersion: number, data: unknown) {
  return schemaVersion === 1 && validateInputData(data)
    ? { valid: true }
    : { valid: false, reason: 'invalid input v1 data' };
}

const InputBlock: React.FC<BlockRendererProps> = (props) => {
  const data = props.block.data as SafeInputData;
  const action = props.block.actions?.find((candidate) => /input|submit|answer/i.test(candidate.id)) ?? props.block.actions?.[0];
  const state = useActionIntent(props, action?.id ?? 'input_unavailable', true);
  const formRef = useRef<HTMLFormElement>(null);
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(data.fields.map((field) => [field.id, ''])));
  const [validation, setValidation] = useState('');
  const disabled = !action || state.disabled;

  const submit = useCallback(() => {
    if (disabled) return;
    const form = formRef.current;
    const FormDataCtor = form?.ownerDocument.defaultView?.FormData;
    if (!form || !FormDataCtor) return;
    const draft = Object.fromEntries(Array.from(new FormDataCtor(form).entries(), ([key, value]) => [key, String(value)]));
    const missing = data.fields.find((field) => field.required && !draft[field.id]?.trim());
    if (missing) {
      setValidation(`${missing.label} is required`);
      return;
    }
    setValidation('');
    state.dispatch({ values: draft });
  }, [data.fields, disabled, state]);

  return (
    <PromptShelf
      className="wg2-input-block"
      cardClassName="wg2-input-block__card"
      titleId={`input-${props.block.id}`}
      title={data.prompt}
      meta={data.context}
      role="region"
      actions={<PromptAction
        keyLabel=""
        label={action?.label ?? 'Submit'}
        onClick={submit}
        primary
        disabled={disabled}
      />}
    >
      <form ref={formRef} className="wg2-input-block__fields" onSubmit={(event) => { event.preventDefault(); submit(); }}>
        {data.fields.map((field) => {
          const id = `input-${props.block.id}-${field.id}`;
          const common = {
            id,
            name: field.id,
            value: values[field.id] ?? '',
            disabled,
            onChange: (event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>) => {
              setValues((current) => ({ ...current, [field.id]: event.target.value }));
              setValidation('');
            },
          };
          return (
            <div key={field.id} className="wg2-input-block__field">
              <label className="wg2-input-block__label" htmlFor={id}>
                {field.label}{field.required && <span className="wg2-input-block__required"> *</span>}
              </label>
              {field.type === 'textarea' ? (
                <textarea {...common} className="wg2-input-block__textarea" placeholder={field.placeholder} rows={3} />
              ) : field.type === 'select' ? (
                <select {...common} className="wg2-input-block__select">
                  <option value="">Select…</option>
                  {field.options?.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                </select>
              ) : (
                <input
                  {...common}
                  className="wg2-input-block__input"
                  type="text"
                  placeholder={field.placeholder}
                  onKeyDown={(event) => { if (event.key === 'Enter') submit(); }}
                />
              )}
            </div>
          );
        })}
      </form>
      {validation && <div className="wg2-input-block__validation" role="alert">{validation}</div>}
      {!action && <div className="wg2-action-feedback wg2-action-feedback--disabled">No input action available</div>}
      <ActionFeedback state={state} />
    </PromptShelf>
  );
};

export default InputBlock;
