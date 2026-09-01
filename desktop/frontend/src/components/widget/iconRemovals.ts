import type { DesktopIconActionInput, DesktopIconActionResult, DesktopIconItem, DesktopIconSnapshot } from "../../lib/bridge";

type Removal = {
	item: DesktopIconItem;
	input: DesktopIconActionInput;
	phase: "pending" | "confirmed" | "failed";
	settledAt: number;
	reuse: boolean;
	error: string;
	promise: Promise<DesktopIconActionResult["status"]>;
};

type RemovalEffects = {
	send: (input: DesktopIconActionInput) => Promise<DesktopIconActionResult>;
	snapshot: (ticket: number, snapshot: DesktopIconSnapshot) => void;
	refresh: () => Promise<void>;
};

// Removal intent is separate from the authoritative snapshot. Every snapshot
// producer uses the same tickets, so a late read cannot resurrect a deletion.
export class IconRemovals {
	private entries = new Map<string, Removal>();
	private listeners = new Set<() => void>();
	private revision = 0;
	private issued = 0;
	private accepted = 0;

	constructor(private requestID: () => string) {}

	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => { this.listeners.delete(listener); };
	};
	version = () => this.revision;
	beginSnapshot = () => ++this.issued;

	acceptSnapshot(ticket: number, snapshot: DesktopIconSnapshot): boolean {
		if (ticket < this.accepted) return false;
		this.accepted = ticket;
		let changed = false;
		for (const [id, entry] of this.entries) {
			// Only a read started after settlement can release the overlay. A
			// newly retained icon may legitimately have the same session ID.
			if (entry.phase !== "pending" && ticket > entry.settledAt &&
				(entry.phase === "confirmed" || !snapshot.items.some(item => item.id === id))) {
				this.entries.delete(id);
				changed = true;
			}
		}
		if (changed) this.changed();
		return true;
	}

	project(items: DesktopIconItem[]): DesktopIconItem[] {
		const projected = items.filter(item => {
			const entry = this.entries.get(item.id);
			return !entry || entry.phase === "failed";
		});
		for (const entry of this.entries.values()) {
			if (entry.phase === "failed" && !projected.some(item => item.id === entry.item.id)) projected.push(entry.item);
		}
		return projected;
	}

	error(): string {
		return [...this.entries.values()].filter(entry => entry.error).map(entry => `${entry.item.title}：${entry.error}`).join("；");
	}

	clearErrors(): void {
		for (const entry of this.entries.values()) entry.error = "";
		this.changed();
	}

	remove(item: DesktopIconItem, effects: RemovalEffects): Promise<DesktopIconActionResult["status"]> {
		const previous = this.entries.get(item.id);
		if (previous && previous.phase !== "failed") return previous.promise;
		const input = previous?.reuse ? previous.input : {
			itemId: item.id, noticeId: item.notifications[0]?.id, revision: item.revision,
			requestId: this.requestID(), action: "remove",
		};
		const ticket = this.beginSnapshot();
		const entry: Removal = {
			item, input, phase: "pending", settledAt: 0, reuse: true, error: "",
			promise: Promise.resolve("accepted"),
		};
		this.entries.set(item.id, entry);
		// Defer dispatch until after the synchronous visibility notification.
		entry.promise = Promise.resolve().then(() => effects.send(input)).then(result => {
			entry.settledAt = this.issued;
			if (result.status === "accepted" || result.status === "already_applied") {
				entry.phase = "confirmed";
			} else {
				entry.phase = "failed";
				entry.reuse = result.status === "retryable_error";
				entry.error = result.error || "移除失败，请重试";
			}
			this.changed();
			effects.snapshot(ticket, result.snapshot);
			if (entry.phase === "confirmed") {
				void effects.refresh().catch(() => {
					// Keep the confirmed overlay until the normal refresh retries.
					entry.error = "图标已移除，刷新失败，正在等待重试";
					this.changed();
				});
			}
			return result.status;
		}).catch(cause => {
			entry.phase = "failed";
			entry.settledAt = this.issued;
			entry.error = `${cause instanceof Error ? cause.message : String(cause)}；可再次移除以重试`;
			this.changed();
			return "retryable_error";
		});
		this.changed();
		return entry.promise;
	}

	private changed(): void {
		this.revision++;
		for (const listener of this.listeners) listener();
	}
}
