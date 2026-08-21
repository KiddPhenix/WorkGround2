export const EXTERNAL_RUN_LAUNCH_KEY = "wg2.external-run-launch-v1";

export interface ExternalRunLaunchPacket {
	version: 1;
	requestId: string;
	workspace: string;
	prompt: string;
}

export interface ExternalRunLaunchStorage {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
	removeItem(key: string): void;
}

export function readExternalRunLaunch(storage: ExternalRunLaunchStorage): ExternalRunLaunchPacket | null {
	const raw = storage.getItem(EXTERNAL_RUN_LAUNCH_KEY);
	if (!raw) return null;
	try {
		const value: unknown = JSON.parse(raw);
		if (!value || typeof value !== "object") return null;
		const row = value as Record<string, unknown>;
		const requestId = typeof row.requestId === "string" ? row.requestId.trim() : "";
		const workspace = typeof row.workspace === "string" ? row.workspace.trim() : "";
		const prompt = typeof row.prompt === "string" ? row.prompt.trim() : "";
		return row.version === 1 && requestId && prompt ? { version: 1, requestId, workspace, prompt } : null;
	} catch {
		return null;
	}
}

export function sameExternalRunIntent(packet: ExternalRunLaunchPacket, workspace: string, prompt: string): boolean {
	return packet.workspace === workspace.trim() && packet.prompt === prompt.trim();
}

export function prepareExternalRunLaunch(
	storage: ExternalRunLaunchStorage,
	workspace: string,
	prompt: string,
	newRequestId: () => string,
): ExternalRunLaunchPacket {
	workspace = workspace.trim();
	prompt = prompt.trim();
	if (!prompt) throw new Error("请输入 DSH 任务");
	const current = readExternalRunLaunch(storage);
	if (current && sameExternalRunIntent(current, workspace, prompt)) return current;
	const packet: ExternalRunLaunchPacket = { version: 1, requestId: newRequestId(), workspace, prompt };
	storage.setItem(EXTERNAL_RUN_LAUNCH_KEY, JSON.stringify(packet));
	return packet;
}

export function clearExternalRunLaunch(storage: ExternalRunLaunchStorage, requestId: string): void {
	const current = readExternalRunLaunch(storage);
	if (current?.requestId === requestId) storage.removeItem(EXTERNAL_RUN_LAUNCH_KEY);
}
