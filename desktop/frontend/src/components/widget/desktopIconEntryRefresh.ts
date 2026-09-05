// Each Activity reveal gets an independent read lifetime. A hidden lifetime's
// pending full scan can neither delay entry nor commit into the next lifetime.
export function createIconEntryRefresh(load: (entry: boolean, current: () => boolean) => Promise<void>) {
	let generation = 0;
	let active = false;
	let entry = false;
	let requested = false;
	let pending: Promise<void> | null = null;
	const refresh = (): Promise<void> => {
		if (!active) return Promise.resolve();
		requested = true;
		if (pending) return pending;
		const lifetime = generation;
		const current = () => active && generation === lifetime;
		const run = async () => {
			if (entry) {
				entry = false;
				await load(true, current);
			}
			while (current() && requested) {
				requested = false;
				await load(false, current);
			}
		};
		const result = run().finally(() => { if (pending === result) pending = null; });
		pending = result;
		return result;
	};
	return {
		activate() {
			generation++;
			active = true;
			entry = true;
			pending = null;
			requested = false;
		},
		refresh,
		dispose() {
			generation++;
			active = false;
			pending = null;
			requested = false;
		},
	};
}
