import type { VocabularyMatch } from "./types";

export interface VocabularyToken {
  from: number;
  to: number;
  prefix: string;
}

const VOCAB_CHAR = /[\p{L}\p{N}_.+\-]/u;

export function vocabularyTokenAt(value: string, selectionStart: number | null, selectionEnd: number | null): VocabularyToken | null {
  const end = selectionEnd ?? value.length;
  const start = selectionStart ?? end;
  if (start !== end || end !== value.length || end <= 0) return null;
  let from = end;
  while (from > 0) {
    const cp = value.codePointAt(from - 1);
    if (cp === undefined) break;
    const char = String.fromCodePoint(cp);
    const width = char.length;
    const actualFrom = from - width;
    const actual = value.slice(actualFrom, from);
    if (!VOCAB_CHAR.test(actual)) break;
    from = actualFrom;
  }
  const prefix = value.slice(from, end);
  const marker = from > 0 ? value[from - 1] : "";
  if (!prefix || marker === "/" || marker === "$" || marker === "@" || marker === "!") return null;
  const min = /\p{Script=Han}/u.test(prefix) ? 2 : 3;
  if (Array.from(prefix).length < min) return null;
  return { from, to: end, prefix };
}

export function acceptVocabulary(value: string, token: VocabularyToken, match: VocabularyMatch): { value: string; cursor: number } {
  const next = value.slice(0, token.from) + match.text + value.slice(token.to);
  return { value: next, cursor: token.from + match.text.length };
}
