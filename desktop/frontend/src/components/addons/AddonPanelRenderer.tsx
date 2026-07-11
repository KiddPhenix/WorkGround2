import type { ReactNode } from "react";
import { asArray } from "../../lib/array";
import { useT } from "../../lib/i18n";
import type { DictKey } from "../../lib/i18n";
import type { AddOnPanelSchemaView, AddOnPanelSectionView, PluginView } from "../../lib/types";
import { InlineConfirmButton } from "../InlineConfirmButton";

export type AddOnPanelSchemaMap = Record<string, AddOnPanelSchemaState>;

export type AddOnPanelSchemaState = {
	raw?: string;
	schema?: AddOnPanelSchemaView;
	error?: string;
};

export type AddOnRecord = Record<string, unknown>;

export type AddOnRecordStatus = {
	dot?: string;
	label?: string;
	sub?: string;
};

export type AddOnPanelAdapter = {
	records: AddOnRecord[] | null;
	form: AddOnRecord;
	setFormValue: (key: string, value: unknown) => void;
	resetForm: () => void;
	runFormAction: (actionID: string) => void;
	canFormAction?: (actionID: string) => boolean;
	runRecordAction?: (actionID: string, record: AddOnRecord) => void;
	canRecordAction?: (actionID: string, record: AddOnRecord) => boolean;
	recordKey?: (record: AddOnRecord) => string;
	recordStatus?: (record: AddOnRecord) => AddOnRecordStatus;
	recordBadges?: (record: AddOnRecord) => string[];
};

export type AddOnPanelAdapterMap = Record<string, AddOnPanelAdapter>;

type AddOnFieldSchema = {
	key?: string;
	path?: string;
	label?: string;
	labelKey?: string;
	placeholder?: string;
	placeholderKey?: string;
	type?: "text" | "password" | "number" | "checkbox" | "select" | "textarea";
	options?: Array<{ value: string; label?: string; labelKey?: string }>;
	span?: number;
	min?: number;
	autoComplete?: string;
	visibleWhen?: { key?: string; path?: string; equals?: unknown };
};

type AddOnActionSchema = {
	id?: string;
	label?: string;
	labelKey?: string;
	variant?: "primary" | "normal";
	danger?: boolean;
	confirmLabel?: string;
	confirmLabelKey?: string;
};

type AddOnListSchema = {
	title?: string;
	titleKey?: string;
	empty?: string;
	emptyKey?: string;
	summaryKey?: string;
	titleField?: string;
	subtitleField?: string;
	fields?: AddOnFieldSchema[];
	detailFields?: AddOnFieldSchema[];
	badgeFields?: AddOnFieldSchema[];
	actions?: AddOnActionSchema[];
};

type AddOnFormSchema = {
	fields?: AddOnFieldSchema[];
	actions?: AddOnActionSchema[];
};

type RenderableSection = AddOnPanelSectionView & {
	adapter?: string;
	dataSource?: string;
	form?: AddOnFormSchema;
	list?: AddOnListSchema;
};

export function AddOnPanelRenderer({
	plugin,
	schemas,
	adapters,
	busy,
	onReload,
}: {
	plugin: PluginView;
	schemas: AddOnPanelSchemaMap | null;
	adapters: AddOnPanelAdapterMap;
	busy: boolean;
	onReload: () => Promise<void>;
}) {
	const t = useT();
	const panels = asArray(plugin.addon?.panels).filter((panel) => panel.id || panel.entry);
	if (panels.length === 0) return null;
	return (
		<div className="addon-panels">
			{panels.map((panel, index) => {
				const panelID = panel.id || panel.entry || `panel-${index}`;
				const state = schemas?.[addonPanelKey(plugin.name, panelID)];
				const schema = state?.schema;
				const sections = asArray(schema?.sections) as RenderableSection[];
				return (
					<div className="addon-panel" data-addon-panel={panelID} key={`${plugin.name}:${panelID}`}>
						<div className="addon-panel__head">
							<div className="addon-panel__title">{schema?.title || panel.title || panelID}</div>
							{panel.entry && <span className="cap-source-badge cap-source-badge--scope">{panel.entry}</span>}
						</div>
						{!schemas ? (
							<div className="mem-empty">{t("caps.loading")}</div>
						) : state?.error ? (
							<div className="cap-source__warning">{state.error}</div>
						) : sections.length === 0 ? (
							<div className="mem-empty">{t("caps.addonPanelEmpty")}</div>
						) : (
							sections.map((section, sectionIndex) => {
								const adapterKey = section.adapter || section.dataSource || schema?.storage?.source || "";
								if (!section.form && !section.list) {
									return (
										<div className="mem-empty" key={`${panelID}:${section.id || section.kind || sectionIndex}`}>
											{t("caps.addonPanelEmpty")}
										</div>
									);
								}
								const adapter = adapterKey ? adapters[adapterKey] : undefined;
								if (!adapter) {
									return (
										<div className="mem-empty" key={`${panelID}:${section.id || section.kind || sectionIndex}`}>
											{t("caps.addonPanelMissingAdapter", { adapter: adapterKey || section.kind || panelID })}
										</div>
									);
								}
								return (
									<SchemaFormListPanel
										key={`${panelID}:${section.id || section.kind || sectionIndex}`}
										section={section}
										adapter={adapter}
										busy={busy}
										onReload={onReload}
									/>
								);
							})
						)}
					</div>
				);
			})}
		</div>
	);
}

export function addonPanelKey(pluginName: string, panelID: string): string {
	return `${pluginName}:${panelID}`;
}

function SchemaFormListPanel({
	section,
	adapter,
	busy,
	onReload,
}: {
	section: RenderableSection;
	adapter: AddOnPanelAdapter;
	busy: boolean;
	onReload: () => Promise<void>;
}) {
	const t = useT();
	const form = section.form;
	const list = section.list;
	return (
		<div className="addon-panel-content">
			{form?.actions && form.actions.length > 0 && (
				<div className="cap-plugin-settings-actions skill-share-actions">
					{form.actions.map((action) => renderActionButton(action, busy, () => adapter.runFormAction(action.id || ""), adapter.canFormAction?.(action.id || "") ?? true, t))}
				</div>
			)}
			{form?.fields && form.fields.length > 0 && (
				<div className="skill-share-form">
					{form.fields.map((field) => renderFormField(field, adapter, busy, t))}
				</div>
			)}
			{list && (
				<div className="cap-server-section cap-plugin-section skill-share-list">
					<div className="cap-server-section__head">
						<div className="cap-server-section__copy">
							<div className="cap-server-section__title">{textOf(list.title, list.titleKey, t)}</div>
							{adapter.records && list.summaryKey && <div className="drawer__summary">{schemaText(list.summaryKey, t, { count: adapter.records.length })}</div>}
						</div>
						<button className="btn btn--small" disabled={busy} type="button" onClick={() => void onReload()}>
							{t("caps.pluginRefresh")}
						</button>
					</div>
					{!adapter.records ? (
						<div className="mem-empty">{t("caps.loading")}</div>
					) : adapter.records.length === 0 ? (
						<div className="mem-empty">{textOf(list.empty, list.emptyKey, t)}</div>
					) : (
						<div className="cap-server-group">
							{adapter.records.map((record, index) => (
								<SchemaRecordRow key={adapter.recordKey?.(record) || String(pathValue(record, "id") || index)} record={record} list={list} adapter={adapter} busy={busy} />
							))}
						</div>
					)}
				</div>
			)}
		</div>
	);
}

function SchemaRecordRow({
	record,
	list,
	adapter,
	busy,
}: {
	record: AddOnRecord;
	list: AddOnListSchema;
	adapter: AddOnPanelAdapter;
	busy: boolean;
}) {
	const t = useT();
	const status = adapter.recordStatus?.(record) ?? {};
	const title = String(pathValue(record, list.titleField || "displayName") || pathValue(record, "id") || "");
	const sub = status.sub || (list.subtitleField ? String(pathValue(record, list.subtitleField) || "") : "");
	const badges = [...asArray(adapter.recordBadges?.(record)), ...asArray(list.badgeFields).map((field) => String(pathValue(record, field.path || field.key || "") || "")).filter(Boolean)];
	return (
		<div className={`cap-server-entry skill-share-entry${pathValue(record, "enabled") === false ? " cap-server-entry--disabled" : ""}`}>
			<div className={`cap-row${pathValue(record, "enabled") === false ? " cap-row--disabled" : ""}`}>
				<span className={`cap-dot cap-dot--${status.dot || "disabled"}`} />
				<div className="cap-row__text">
					<div className="cap-row__head">
						<span className="cap-row__name">{title}</span>
						{status.label && <span className="cap-row__transport">{status.label}</span>}
						{badges.map((badge) => <span className="cap-source-badge" key={badge}>{badge}</span>)}
					</div>
					{sub && <div className="cap-row__sub">{sub}</div>}
				</div>
			</div>
			<div className="cap-server-details">
				{list.detailFields && list.detailFields.length > 0 && (
					<div className="cap-detail-grid">
						{list.detailFields.map((field) => {
							const value = pathValue(record, field.path || field.key || "");
							if (value === undefined || value === null || value === "") return null;
							return (
								<div className={`cap-detail${field.span === 2 ? " cap-detail--wide" : ""}`} key={field.path || field.key}>
									<span className="cap-detail__label">{textOf(field.label, field.labelKey, t)}</span>
									<span className={field.type === "text" ? "cap-detail__value" : "cap-detail__code"}>{String(value)}</span>
								</div>
							);
						})}
					</div>
				)}
				{list.actions && list.actions.length > 0 && (
					<div className="cap-detail-actions">
						{list.actions.map((action) => renderActionButton(
							action,
							busy,
							() => adapter.runRecordAction?.(action.id || "", record),
							adapter.canRecordAction?.(action.id || "", record) ?? true,
							t,
						))}
					</div>
				)}
			</div>
		</div>
	);
}

function renderFormField(field: AddOnFieldSchema, adapter: AddOnPanelAdapter, busy: boolean, t: ReturnType<typeof useT>): ReactNode {
	const key = field.key || field.path || "";
	if (!key || !isVisible(field, adapter.form)) return null;
	const type = field.type || "text";
	const value = pathValue(adapter.form, key);
	const className = `mem-input${field.span === 2 ? " skill-share-form__wide" : ""}`;
	const label = textOf(field.label, field.labelKey, t);
	const placeholder = textOf(field.placeholder, field.placeholderKey, t);
	if (type === "checkbox") {
		return (
			<label className="cap-plugin-option skill-share-form__check" key={key}>
				<input type="checkbox" checked={Boolean(value)} disabled={busy} onChange={(e) => adapter.setFormValue(key, e.target.checked)} />
				<span>{label}</span>
			</label>
		);
	}
	if (type === "select") {
		return (
			<select className={className} aria-label={label} value={String(value ?? "")} disabled={busy} key={key} onChange={(e) => adapter.setFormValue(key, e.target.value)}>
				{asArray(field.options).map((option) => (
					<option value={option.value} key={option.value}>{textOf(option.label, option.labelKey, t)}</option>
				))}
			</select>
		);
	}
	if (type === "textarea") {
		return (
			<textarea className={`mem-textarea${field.span === 2 ? " skill-share-form__wide" : ""}`} aria-label={label} placeholder={placeholder} value={String(value ?? "")} disabled={busy} key={key} onChange={(e) => adapter.setFormValue(key, e.target.value)} spellCheck={false} />
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
			onChange={(e) => adapter.setFormValue(key, type === "number" ? e.target.value : e.target.value)}
		/>
	);
}

function renderActionButton(action: AddOnActionSchema, busy: boolean, run: () => void, enabled: boolean, t: ReturnType<typeof useT>): ReactNode {
	const id = action.id || "action";
	const label = textOf(action.label, action.labelKey, t);
	const disabled = busy || !enabled;
	if (action.confirmLabel || action.confirmLabelKey || action.danger) {
		return (
			<InlineConfirmButton
				key={id}
				label={label}
				confirmLabel={textOf(action.confirmLabel, action.confirmLabelKey, t) || label}
				cancelLabel={t("common.cancel")}
				disabled={disabled}
				danger={Boolean(action.danger)}
				onConfirm={run}
			/>
		);
	}
	return (
		<button className={`btn ${action.variant === "primary" ? "btn--primary " : ""}btn--small`} type="button" disabled={disabled} onClick={run} key={id}>
			{label}
		</button>
	);
}

function isVisible(field: AddOnFieldSchema, form: AddOnRecord): boolean {
	if (!field.visibleWhen) return true;
	const key = field.visibleWhen.path || field.visibleWhen.key || "";
	return pathValue(form, key) === field.visibleWhen.equals;
}

function textOf(label: string | undefined, labelKey: string | undefined, t: ReturnType<typeof useT>): string {
	if (labelKey) return schemaText(labelKey, t);
	return label || "";
}

function schemaText(key: string, t: ReturnType<typeof useT>, vars?: Record<string, string | number>): string {
	return t(key as DictKey, vars);
}

function pathValue(record: AddOnRecord, path: string): unknown {
	if (!path) return undefined;
	return path.split(".").reduce<unknown>((current, part) => {
		if (!current || typeof current !== "object") return undefined;
		return (current as Record<string, unknown>)[part];
	}, record);
}
