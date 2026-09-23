// Pure helpers behind the run detail panel: filtering, failure navigation and
// match highlighting. No React and no store access here — the panel keeps only
// layout, so every rule an acceptance check cares about (which records match,
// where "next failure" lands, whether a pinned record is still around) is
// decided by a plain function and can be tested without a DOM.

import type { RunEvent, RunRecord } from "../store/run";

export type RunDetailFilter = {
  /** Free text matched against the tool identity (name/command/path) and the body. */
  query: string;
  /** Only records that ended in a real failure. */
  failuresOnly: boolean;
};

export const EMPTY_DETAIL_FILTER: RunDetailFilter = { query: "", failuresOnly: false };

/** A real failure: a stopped call is unconfirmed, not failed. */
export function isFailureEvent(event: RunEvent): boolean {
  return event.status === "failed";
}

/** What the search box matches — identity first, then whatever the record carries. */
export function runDetailSearchText(event: RunEvent): string {
  return [
    event.stepLabel,
    event.toolName,
    event.args,
    event.content,
    event.output,
    event.error,
  ].filter((part): part is string => Boolean(part)).join("\n");
}

export function matchesRunDetailFilter(event: RunEvent, filter: RunDetailFilter): boolean {
  if (filter.failuresOnly && !isFailureEvent(event)) return false;
  const query = filter.query.trim().toLowerCase();
  if (!query) return true;
  return runDetailSearchText(event).toLowerCase().includes(query);
}

export function filterRunDetailEvents(events: RunEvent[], filter: RunDetailFilter): RunEvent[] {
  if (!filter.query.trim() && !filter.failuresOnly) return events;
  return events.filter((event) => matchesRunDetailFilter(event, filter));
}

/** Index of a record by its stable eventId, or -1 when it is gone. */
export function detailEventIndex(events: RunEvent[], eventId?: string): number {
  if (!eventId) return -1;
  return events.findIndex((event) => event.eventId === eventId);
}

/**
 * Index of the next failed record strictly after/before `fromIndex`, walking
 * the list the user is actually looking at. Returns undefined when there is none.
 */
export function nextFailureIndex(
  events: RunEvent[],
  fromIndex: number,
  direction: 1 | -1,
): number | undefined {
  for (let index = fromIndex + direction; index >= 0 && index < events.length; index += direction) {
    if (isFailureEvent(events[index])) return index;
  }
  // From an untouched selection, allow finding the first/last failure at all.
  if (fromIndex < 0) {
    const start = direction === 1 ? 0 : events.length - 1;
    for (let index = start; index >= 0 && index < events.length; index += direction) {
      if (isFailureEvent(events[index])) return index;
    }
  }
  return undefined;
}

/** Records that arrived after the one being read — the "N new records" hint. */
export function countNewRecords(events: RunEvent[], selectedIndex: number): number {
  if (selectedIndex < 0) return 0;
  return Math.max(0, events.length - selectedIndex - 1);
}

export type RunDetailSelection = {
  /** Index in the full event list, or -1 when the run has no records yet. */
  index: number;
  /** The record being read. */
  event?: RunEvent;
  /** The user pinned a record that is no longer part of this run. */
  pinMissing: boolean;
  /** Reading the newest record (nothing is pinned, so arrivals follow). */
  following: boolean;
};

/**
 * Resolves which record the detail pane shows.
 *
 * The panel and the compact card answer the same question — "which record am I
 * reading" — so a step pinned in the compact card carries over. Otherwise
 * follow-latest is derived, not written on every event: while following, the
 * newest record is selected without touching the store, and while pinned a new
 * record never steals the reader (the panel raises its "new records" hint).
 */
export function resolveRunDetailSelection(run: RunRecord): RunDetailSelection {
  const events = run.events;
  if (events.length === 0) return { index: -1, event: undefined, pinMissing: false, following: true };
  const newest = events.length - 1;
  const pinnedEventId = run.detailSelectedEventId;
  const explicit = run.detailFollowLatest === false ? detailEventIndex(events, pinnedEventId) : -1;
  if (explicit >= 0) return { index: explicit, event: events[explicit], pinMissing: false, following: false };
  // The compact card auto-advances its own step to the newest record; anything
  // else there is an explicit "I am reading this one".
  const step = run.selectedStepIndex;
  if (typeof step === "number" && step >= 0 && step < newest) {
    return { index: step, event: events[step], pinMissing: false, following: false };
  }
  return {
    index: newest,
    event: events[newest],
    pinMissing: run.detailFollowLatest === false && pinnedEventId !== undefined,
    following: true,
  };
}

export function formatRunDetailDuration(ms?: number): string {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return "";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}

export type MatchSegment = { text: string; hit: boolean };

/** Splits text into alternating plain/match segments for case-insensitive search. */
export function highlightSegments(text: string, query: string): MatchSegment[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [{ text, hit: false }];
  const haystack = text.toLowerCase();
  const segments: MatchSegment[] = [];
  let cursor = 0;
  for (;;) {
    const found = haystack.indexOf(needle, cursor);
    if (found < 0) break;
    if (found > cursor) segments.push({ text: text.slice(cursor, found), hit: false });
    segments.push({ text: text.slice(found, found + needle.length), hit: true });
    cursor = found + needle.length;
  }
  if (cursor < text.length) segments.push({ text: text.slice(cursor), hit: false });
  return segments.length > 0 ? segments : [{ text, hit: false }];
}

export function countMatches(text: string, query: string): number {
  const needle = query.trim().toLowerCase();
  if (!needle) return 0;
  const haystack = text.toLowerCase();
  let count = 0;
  let cursor = 0;
  for (;;) {
    const found = haystack.indexOf(needle, cursor);
    if (found < 0) return count;
    count++;
    cursor = found + needle.length;
  }
}

/**
 * Lines the detail pane renders for a record's body. Prefers the full `output`
 * captured at result time and falls back to the streaming `content` preview,
 * so a running record still shows what it has produced so far.
 */
export function detailBodyText(event?: RunEvent): string {
  if (!event) return "";
  // A record that carries a body reports that body — an empty result is a known
  // "no output", and must not be back-filled with a summary or a label.
  if (event.archived) return event.output ?? "";
  if (event.output !== undefined) return event.output;
  // A record with no body yet (running, or a note) shows the streaming preview.
  if (event.phase === "progress" || event.phase === "note" || event.phase === undefined) {
    return event.content ?? "";
  }
  return "";
}

/** Args are only worth showing when the backend actually sent them. */
export function detailArgsText(event?: RunEvent): string {
  return event?.args?.trim() ?? "";
}

export function prettyJson(value: string): string {
  const trimmed = value.trim();
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return value;
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2);
  } catch {
    return value;
  }
}
