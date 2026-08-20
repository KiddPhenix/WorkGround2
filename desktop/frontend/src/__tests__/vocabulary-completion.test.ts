// Run: tsx src/__tests__/vocabulary-completion.test.ts

import { acceptVocabulary, vocabularyTokenAt } from "../lib/vocabularyCompletion";

let failed = 0;

function eq(actual: unknown, expected: unknown, label: string) {
  if (JSON.stringify(actual) === JSON.stringify(expected)) {
    process.stdout.write(`  PASS  ${label}\n`);
    return;
  }
  failed += 1;
  process.stderr.write(`  FAIL  ${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}\n`);
}

console.log("\nvocabulary completion");

eq(vocabularyTokenAt("多模", 2, 2), { from: 0, to: 2, prefix: "多模" }, "two CJK runes form a prefix");
eq(vocabularyTokenAt("请使用 多模", 6, 6), { from: 4, to: 6, prefix: "多模" }, "token is scoped after whitespace");
eq(vocabularyTokenAt("多", 1, 1), null, "one CJK rune is below threshold");
eq(vocabularyTokenAt("abc", 2, 2), null, "selection away from input end does not ghost over trailing text");
eq(vocabularyTokenAt("/mem", 4, 4), null, "slash command is excluded");
eq(vocabularyTokenAt("$skill", 6, 6), null, "explicit Skill invocation is excluded");
eq(vocabularyTokenAt("@src", 4, 4), null, "file reference is excluded");

const accepted = acceptVocabulary("请使用 多模", { from: 4, to: 6, prefix: "多模" }, {
  id: "term",
  text: "多模态生视频V5",
  suffix: "态生视频V5",
  kind: "noun",
});
eq(accepted, { value: "请使用 多模态生视频V5", cursor: 12 }, "accept replaces only the current token");

if (failed > 0) process.exit(1);
console.log("  all vocabulary completion tests passed");
