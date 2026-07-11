import { useCallback, useEffect, useRef, useState } from "react";
import { useAddOnDialogStore } from "../../store/addonDialog";
import { app } from "../../lib/bridge";
import type { AddOnPanelSchemaView } from "../../lib/types";
import type { AddOnRecord } from "./AddonPanelRenderer";
import { InlineConfirmButton } from "../InlineConfirmButton";
import { asArray } from "../../lib/array";

type AddOnFieldSchema = {
  key?: string; path?: string; label?: string; labelKey?: string;
  placeholder?: string; placeholderKey?: string;
  type?: "text" | "password" | "number" | "checkbox" | "select" | "textarea";
  options?: Array<{ value: string; label?: string; labelKey?: string }>;
  span?: number; min?: number; autoComplete?: string;
  visibleWhen?: { key?: string; path?: string; equals?: unknown };
};

type AddOnActionSchema = {
  id?: string; label?: string; labelKey?: string;
  variant?: "primary" | "normal"; danger?: boolean;
  confirmLabel?: string; confirmLabelKey?: string;
};

export function AddOnDialogModal() {
  const { pluginName, panelID, message, busy: storeBusy, setOpen, setBusy } = useAddOnDialogStore();
  const [schema, setSchema] = useState<AddOnPanelSchemaView | null>(null);
  const [form, setForm] = useState<AddOnRecord>({});
  const [, setRecords] = useState<AddOnRecord[]>([]);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const animRef = useRef<HTMLDivElement>(null);

  const busy = storeBusy || loading;

  // Load panel data when dialog opens
  const loadData = useCallback(async () => {
    if (!pluginName || !panelID) return;
    setLoading(true);
    setError("");
    setNotice("");
    try {
      // Load schema
      const raw = await app.AddOnPanelSchema(pluginName, panelID);
      if (raw) {
        const parsed = parseAddOnPanelSchema(raw);
        if (parsed) setSchema(parsed);
      }
      // Load data
      const adapterKey = pluginName + "/credentials.json";
      const data = await app.AddOnPanelQuery(pluginName, panelID, adapterKey);
      setRecords(data.records || []);
      setForm(data.form || {});
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, [pluginName, panelID]);

  useEffect(() => { void loadData(); }, [loadData]);

  // Animation on open
  useEffect(() => {
    if (pluginName && animRef.current) {
      animRef.current.setAttribute("data-state", "open");
    }
  }, [pluginName]);

  const close = useCallback(() => {
    if (animRef.current) animRef.current.setAttribute("data-state", "closing");
    setTimeout(() => setOpen(null), 200);
  }, [setOpen]);

  const setFormValue = useCallback((key: string, value: unknown) => {
    setForm((prev) => ({ ...prev, [key]: value }));
  }, []);

  const runAction = useCallback(async (actionId: string) => {
    if (!pluginName || !panelID) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const adapterKey = pluginName + "/credentials.json";
      const result = await app.AddOnPanelAction(pluginName, panelID, adapterKey, {
        actionId,
        form,
        recordId: "",
      });
      if (result.error) {
        setError(result.error);
      } else {
        setNotice(result.notice || "");
        if (actionId === "save" || actionId === "delete") {
          // Submit dialog result and close
          await app.DismissAddOnDialog(pluginName, panelID, true, form, actionId);
          setTimeout(() => setOpen(null), 300);
          return;
        }
        // Reload after test
        await loadData();
      }
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }, [pluginName, panelID, form, setBusy, setOpen, loadData]);

  const handleCancel = useCallback(async () => {
    if (!pluginName || !panelID) return;
    try {
      await app.DismissAddOnDialog(pluginName, panelID, false, {}, "");
    } catch (_) { /* ignore */ }
    close();
  }, [pluginName, panelID, close]);

  if (!pluginName || !panelID) return null;

  const sections = asArray(schema?.sections) || [];
  const formSection = sections[0];
  const formView = formSection?.form as { fields?: AddOnFieldSchema[]; actions?: AddOnActionSchema[] } | undefined;
  const formFields = formView?.fields || [];
  const formActions = formView?.actions || [];

  // Split fields: non-state fields vs state fields
  const visibleFields = formFields.filter((field: AddOnFieldSchema) => {
    const key = field.key || field.path || "";
    if (key === "state" || key === "saveCredentials" || key === "confirmSave") return false;
    if (!field.visibleWhen) return true;
    const vk = field.visibleWhen.path || field.visibleWhen.key || "";
    return pathValue(form, vk) === field.visibleWhen.equals;
  });

  return (
    <div className="modal-backdrop" data-state="open" onClick={(e) => { if (e.target === e.currentTarget) handleCancel(); }}>
      <div className="modal addon-dialog-modal" ref={animRef} data-state="open" role="dialog" aria-modal="true">
        <header className="modal__head">
          <div>
            <div className="modal__title">{schema?.title || panelID}</div>
            {message && <div className="addon-dialog-message">{message}</div>}
          </div>
          <button className="modal-close-button" aria-label="Close" onClick={handleCancel} disabled={busy} />
        </header>

        <div className="modal__body addon-dialog-body">
          {loading ? (
            <div className="mem-empty">Loading...</div>
          ) : error ? (
            <div className="cap-source__warning">{error}</div>
          ) : (
            <>
              {notice && <div className="banner banner--success">{notice}</div>}

              {/* Form fields */}
              <div className="skill-share-form addon-dialog-form">
                {visibleFields.map((field: AddOnFieldSchema) => renderDialogField(field, form, setFormValue, busy))}
              </div>

              {/* Form actions */}
              <div className="modal__actions addon-dialog-actions">
                {formActions.map((action: AddOnActionSchema) => {
                  const id = action.id || "action";
                  const label = action.label || id;
                  if (action.danger || action.confirmLabel) {
                    return (
                      <InlineConfirmButton
                        key={id}
                        label={label}
                        confirmLabel={action.confirmLabel || label}
                        cancelLabel="Cancel"
                        disabled={busy}
                        danger={Boolean(action.danger)}
                        onConfirm={() => runAction(id)}
                      />
                    );
                  }
                  return (
                    <button
                      key={id}
                      className={`btn ${action.variant === "primary" ? "btn--primary " : ""}btn--small`}
                      type="button"
                      disabled={busy}
                      onClick={() => runAction(id)}
                    >
                      {label}
                    </button>
                  );
                })}
                <button className="btn btn--small" type="button" disabled={busy} onClick={handleCancel}>
                  Cancel
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function renderDialogField(
  field: AddOnFieldSchema,
  form: AddOnRecord,
  setFormValue: (key: string, value: unknown) => void,
  busy: boolean,
) {
  const key = field.key || field.path || "";
  if (!key) return null;
  const type = field.type || "text";
  const value = pathValue(form, key);
  const label = field.label || key;
  const placeholder = field.placeholder || "";
  const className = `mem-input${field.span === 2 ? " skill-share-form__wide" : ""}`;

  if (type === "checkbox") {
    return (
      <label className="cap-plugin-option skill-share-form__check" key={key}>
        <input type="checkbox" checked={Boolean(value)} disabled={busy} onChange={(e) => setFormValue(key, e.target.checked)} />
        <span>{label}</span>
      </label>
    );
  }
  if (type === "select") {
    return (
      <select className={className} aria-label={label} value={String(value ?? "")} disabled={busy} key={key} onChange={(e) => setFormValue(key, e.target.value)}>
        {asArray(field.options).map((option) => (
          <option value={option.value} key={option.value}>{option.label || option.value}</option>
        ))}
      </select>
    );
  }
  if (type === "textarea") {
    return (
      <textarea className={`mem-textarea${field.span === 2 ? " skill-share-form__wide" : ""}`} aria-label={label} placeholder={placeholder} value={String(value ?? "")} disabled={busy} key={key} onChange={(e) => setFormValue(key, e.target.value)} spellCheck={false} />
    );
  }
  return (
    <input
      className={className}
      type={type}
      min={field.min}
      autoComplete={field.autoComplete}
      aria-label={label}
      placeholder={placeholder}
      value={String(value ?? "")}
      disabled={busy}
      key={key}
      onChange={(e) => setFormValue(key, type === "number" ? e.target.value : e.target.value)}
    />
  );
}

function pathValue(record: AddOnRecord, path: string): unknown {
  if (!path) return undefined;
  return path.split(".").reduce<unknown>((current, part) => {
    if (!current || typeof current !== "object") return undefined;
    return (current as Record<string, unknown>)[part];
  }, record);
}

function parseAddOnPanelSchema(raw: string): AddOnPanelSchemaView | null {
  try {
    return JSON.parse(raw) as AddOnPanelSchemaView;
  } catch {
    return null;
  }
}