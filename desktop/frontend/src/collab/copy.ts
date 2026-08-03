import type { DictKey, Translator } from "../lib/i18n";
import type { collabEn } from "../locales/collab";

type CollabDictKey = keyof typeof collabEn;
type CopyKey = CollabDictKey extends `collab.${infer Key}` ? Key : never;

export function collabCopy(translate: Translator) {
  return (key: CopyKey, vars?: Record<string, string | number>) => {
    const dictKey = `collab.${key}` as CollabDictKey;
    return translate(dictKey as DictKey, vars);
  };
}

export type CollabCopy = ReturnType<typeof collabCopy>;

export const contributionKinds = ["proposal", "decision", "deliverable", "issue", "fix_ready", "verified", "question"] as const;

const contributionKeys: Record<(typeof contributionKinds)[number], CopyKey> = {
  proposal: "contributionProposal",
  decision: "contributionDecision",
  deliverable: "contributionDeliverable",
  issue: "contributionIssue",
  fix_ready: "contributionFixReady",
  verified: "contributionVerified",
  question: "contributionQuestion",
};

export function contributionLabel(c: CollabCopy, kind?: string): string {
  return kind && kind in contributionKeys
    ? c(contributionKeys[kind as keyof typeof contributionKeys])
    : c("kindContribution");
}
