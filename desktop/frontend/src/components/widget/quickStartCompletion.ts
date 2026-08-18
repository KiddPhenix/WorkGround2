import { isComposerSubmitKey, type ComposerSubmitKey } from "../../lib/composerKeyboard";
import type { CommandInfo, DirEntry, SlashArgItem } from "../../lib/types";
import { vocabularyTokenAt, type VocabularyToken } from "../../lib/vocabularyCompletion";
import { activeFileReferenceToken, pickInlineFileReference } from "../FileReferenceMenu";

// Pure completion logic for the QuickStart popup's input. It mirrors the main
// Composer's menu pipeline (slash commands → slash arguments → $ skills →
// @ file refs) but stays side-effect free so the trigger/filter/key/accept rules
// are unit-testable without React or the bridge.

export interface QuickStartAtItem {
	entry: DirEntry;
	label: string;
	path: string;
	isDir: boolean;
}

export type QuickStartCompletion =
	| { kind: "slash"; items: CommandInfo[]; active: number }
	| { kind: "slasharg"; items: SlashArgItem[]; from: number; active: number }
	| { kind: "skill"; items: CommandInfo[]; active: number }
	| { kind: "at"; items: QuickStartAtItem[]; active: number };

export interface QuickStartCompletionInputs {
	slashMatches: CommandInfo[];
	slashArgs: { items: SlashArgItem[]; from: number } | null;
	skillMatches?: CommandInfo[];
	at: { token: { raw: string; dir: string; frag: string }; items: QuickStartAtItem[] } | null;
}

export type QuickStartKeyAction =
	| { type: "move"; delta: 1 | -1 }
	| { type: "accept" }
	| { type: "close" }
	| { type: "submit" }
	| { type: "none" };

// quickStartSlashQuery recognizes a whole-input "/token" with no whitespace yet.
export function quickStartSlashQuery(text: string): string | null {
	if (!text.startsWith("/") || /\s/.test(text)) return null;
	return text.slice(1).toLowerCase();
}

// quickStartSlashMatches filters the shared Commands() catalog client-side,
// exactly like the main Composer's slash menu.
export function quickStartSlashMatches(query: string | null, commands: CommandInfo[]): CommandInfo[] {
	if (query === null) return [];
	return commands.filter((command) => command.name.toLowerCase().includes(query));
}

// "$name" is Codex's explicit Skill invocation form. Only Skill commands are
// offered, while "/" continues to expose the complete command catalog.
export function quickStartSkillQuery(text: string): string | null {
	if (!text.startsWith("$") || /\s/.test(text)) return null;
	return text.slice(1).toLowerCase();
}

export function quickStartSkillMatches(query: string | null, commands: CommandInfo[]): CommandInfo[] {
	if (query === null) return [];
	return commands.filter((command) => command.kind === "skill" && command.name.toLowerCase().includes(query));
}

export function quickStartAtItems(entries: DirEntry[]): QuickStartAtItem[] {
	return entries.map((entry) => ({
		entry,
		label: entry.displayName || entry.name,
		path: entry.path || entry.name,
		isDir: entry.isDir,
	}));
}

// quickStartPickMenu decides which completion menu is live. Slash commands win,
// then slash arguments, $ skills, then @ file references. Vocabulary uses the
// Session-style inline suffix and therefore never enters this menu.
export function quickStartPickMenu(inputs: QuickStartCompletionInputs): QuickStartCompletion | null {
	if (inputs.slashMatches.length > 0) {
		return { kind: "slash", items: inputs.slashMatches, active: 0 };
	}
	if (inputs.slashArgs && inputs.slashArgs.items.length > 0) {
		return { kind: "slasharg", items: inputs.slashArgs.items, from: inputs.slashArgs.from, active: 0 };
	}
	if (inputs.skillMatches && inputs.skillMatches.length > 0) {
		return { kind: "skill", items: inputs.skillMatches, active: 0 };
	}
	if (inputs.at && inputs.at.items.length > 0) {
		return { kind: "at", items: inputs.at.items, active: 0 };
	}
	return null;
}

export function quickStartCompletionCount(menu: QuickStartCompletion | null): number {
	return menu ? menu.items.length : 0;
}

export function quickStartCompletionMove(menu: QuickStartCompletion | null, delta: 1 | -1): QuickStartCompletion | null {
	if (!menu || menu.items.length === 0) return menu;
	const count = menu.items.length;
	const active = (menu.active + delta + count) % count;
	return { ...menu, active };
}

// quickStartCompletionKey maps one textarea keydown to an action. Composition
// (IME) keys always pass through; menu keys win while a menu is open; otherwise
// the shared submit rule decides.
export function quickStartCompletionKey(
	menu: QuickStartCompletion | null,
	event: { key: string; shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; altKey: boolean },
	composing: boolean,
	submitKey: ComposerSubmitKey,
): QuickStartKeyAction {
	if (composing || event.key === "Process" || event.key === "CompositionEvent") return { type: "none" };
	if (menu && menu.items.length > 0) {
		if (event.key === "ArrowDown") return { type: "move", delta: 1 };
		if (event.key === "ArrowUp") return { type: "move", delta: -1 };
		if (event.key === "Enter" || (event.key === "Tab" && !event.shiftKey)) return { type: "accept" };
		if (event.key === "Escape") return { type: "close" };
	}
	if (isComposerSubmitKey(event, submitKey, false)) return { type: "submit" };
	return { type: "none" };
}

export interface QuickStartAcceptResult {
	text: string;
	cursor: number;
	menu: QuickStartCompletion | null;
	recordUse?: { id: string; useID: string };
}

// quickStartAcceptCompletion applies the active candidate to the draft. The
// inserted text keeps the existing input format — the backend receives the
// literal "/cmd args", "@path" or completed term, just like the main Composer's
// submit text.
export function quickStartAcceptCompletion(menu: QuickStartCompletion, text: string): QuickStartAcceptResult {
	if (menu.kind === "slash") {
		const item = menu.items[menu.active];
		if (!item) return { text, cursor: text.length, menu: null };
		const next = "/" + item.name + " ";
		return { text: next, cursor: next.length, menu: null };
	}
	if (menu.kind === "slasharg") {
		const item = menu.items[menu.active];
		if (!item) return { text, cursor: text.length, menu: null };
		const next = text.slice(0, menu.from) + item.insert;
		return { text: next, cursor: next.length, menu: null };
	}
	if (menu.kind === "skill") {
		const item = menu.items[menu.active];
		if (!item) return { text, cursor: text.length, menu: null };
		const next = "$" + item.name + " ";
		return { text: next, cursor: next.length, menu: null };
	}
	if (menu.kind === "at") {
		const item = menu.items[menu.active];
		if (!item) return { text, cursor: text.length, menu: null };
		const token = activeFileReferenceToken(text);
		const next = pickInlineFileReference(text, token?.raw ?? null, token?.dir ?? "", item.entry);
		// A directory keeps the trailing "/" so the refetch reopens the menu for
		// the next level; a file completes the token and closes it.
		return { text: next, cursor: next.length, menu: null };
	}
	return { text, cursor: text.length, menu: null };
}

// quickStartVocabularyToken re-exports the shared token rule so the widget and
// the main Composer agree on where a word starts and whether a marker ("/",
// "$", "@", "!") suppresses vocabulary.
export function quickStartVocabularyToken(value: string, start: number | null, end: number | null): VocabularyToken | null {
	return vocabularyTokenAt(value, start, end);
}
