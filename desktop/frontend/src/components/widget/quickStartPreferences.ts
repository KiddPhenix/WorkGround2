import { normalizeComposerSubmitKey, type ComposerSubmitKey } from "../../lib/composerKeyboard";
import { normalizeToolApprovalMode, type ModelInfo, type SettingsView, type ToolApprovalMode } from "../../lib/types";

export type QuickStartPreferences = {
	model: string;
	approvalMode: ToolApprovalMode;
	submitKey: ComposerSubmitKey;
};

// localStorage keys for the widget's per-send model / approval overrides. The
// defaults still come from the shared user settings snapshot; these only
// remember a QuickStart-specific choice across sessions.
export const QUICK_MODEL_KEY = "wg2.icon-widget-model";
export const QUICK_APPROVAL_KEY = "wg2.icon-widget-approval";

export const QUICK_APPROVAL_OPTIONS: { value: ToolApprovalMode; label: string }[] = [
	{ value: "ask", label: "需要批准" },
	{ value: "auto", label: "自动批准" },
	{ value: "yolo", label: "全部允许" },
];

export function quickStartModelLabel(model: string): string {
	const value = model.trim();
	if (!value) return "未配置";
	const parts = value.split("/");
	return parts[parts.length - 1] || value;
}

export function quickStartApprovalLabel(mode: ToolApprovalMode): string {
	if (mode === "auto") return "自动批准";
	if (mode === "yolo") return "全部允许";
	return "需要批准";
}

export function nextQuickStartApproval(mode: ToolApprovalMode): ToolApprovalMode {
	const index = QUICK_APPROVAL_OPTIONS.findIndex((option) => option.value === mode);
	return QUICK_APPROVAL_OPTIONS[(index + 1) % QUICK_APPROVAL_OPTIONS.length].value;
}

export function quickStartPreferences(settings: Pick<SettingsView, "defaultModel" | "defaultToolApprovalMode" | "composerSubmitKey">): QuickStartPreferences {
	return {
		model: settings.defaultModel,
		approvalMode: normalizeToolApprovalMode(settings.defaultToolApprovalMode),
		submitKey: normalizeComposerSubmitKey(settings.composerSubmitKey),
	};
}

export interface QuickModelOption {
	ref: string;
	label: string;
	provider: string;
	current: boolean;
}

// quickStartModelOptions flattens the backend model catalog into the compact
// picker's option shape — the exact same app.Models() data the main Composer's
// ModelSwitcher renders.
export function quickStartModelOptions(models: ModelInfo[]): QuickModelOption[] {
	return models.map((model) => ({ ref: model.ref, label: model.model, provider: model.provider, current: model.current }));
}

// resolveQuickStartModel picks the send-time model ref: the remembered widget
// choice wins, then the shared default, then the first configured model. An
// empty result means no usable model (send falls back to backend defaults).
export function resolveQuickStartModel(remembered: string, settingsModel: string, models: ModelInfo[]): string {
	for (const candidate of [remembered, settingsModel]) {
		const ref = candidate?.trim();
		if (ref && models.some((model) => model.ref === ref)) return ref;
	}
	return models[0]?.ref ?? "";
}

// resolveQuickStartApproval picks the send-time approval posture: the
// remembered widget choice wins when it is one of the three real modes.
export function resolveQuickStartApproval(remembered: string, settingsMode: ToolApprovalMode): ToolApprovalMode {
	const value = remembered?.trim().toLowerCase();
	if (!value) return settingsMode;
	if (QUICK_APPROVAL_OPTIONS.some((option) => option.value === value)) return value as ToolApprovalMode;
	return settingsMode;
}

export interface QuickStartIntent {
	id: string;
	prompt: string;
	workspace: string;
	model: string;
	approvalMode: string;
}

// sameQuickStartIntent decides whether a retry may reuse the previous requestId
// (idempotent replay). Any change to prompt, workspace, model or approval mode
// starts a fresh requestId, because the backend treats a requestId as one
// immutable conversation intent.
export function sameQuickStartIntent(pending: QuickStartIntent | null, next: Omit<QuickStartIntent, "id">): boolean {
	if (!pending) return false;
	return (
		pending.prompt === next.prompt &&
		pending.workspace === next.workspace &&
		pending.model === next.model &&
		pending.approvalMode === next.approvalMode
	);
}
