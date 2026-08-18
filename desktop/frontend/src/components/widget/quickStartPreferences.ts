import { normalizeComposerSubmitKey, type ComposerSubmitKey } from "../../lib/composerKeyboard";
import { normalizeToolApprovalMode, type SettingsView, type ToolApprovalMode } from "../../lib/types";

export type QuickStartPreferences = {
	model: string;
	approvalMode: ToolApprovalMode;
	submitKey: ComposerSubmitKey;
};

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

export function quickStartPreferences(settings: Pick<SettingsView, "defaultModel" | "defaultToolApprovalMode" | "composerSubmitKey">): QuickStartPreferences {
	return {
		model: settings.defaultModel,
		approvalMode: normalizeToolApprovalMode(settings.defaultToolApprovalMode),
		submitKey: normalizeComposerSubmitKey(settings.composerSubmitKey),
	};
}
