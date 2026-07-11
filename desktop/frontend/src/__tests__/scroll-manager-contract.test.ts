// Run: tsx src/__tests__/scroll-manager-contract.test.ts

import { resolveScrollElement, shouldAutoScrollForQuestionChange, snapElementToBottom, type QuestionScrollSnapshot } from "../lib/useScrollManager";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function snap(count: number, lastId = ""): QuestionScrollSnapshot {
  return { count, lastId };
}

console.log("\nscroll manager contract");

ok(
  !shouldAutoScrollForQuestionChange(snap(0), snap(12, "u12")),
  "restored history can seed the tracker without a synthetic new-question scroll",
);
ok(
  shouldAutoScrollForQuestionChange(snap(12, "u12"), snap(13, "u13")),
  "appended user question scrolls to the bottom",
);
ok(
  !shouldAutoScrollForQuestionChange(snap(12, "u12"), snap(18, "u12")),
  "prepended older history does not scroll to the bottom",
);
ok(
  !shouldAutoScrollForQuestionChange(snap(12, "u12"), snap(12, "u12")),
  "unchanged transcript does not scroll",
);
ok(
  !shouldAutoScrollForQuestionChange(snap(12, "u12"), snap(8, "u8")),
  "replaced or rewound transcript does not use question tracking to scroll",
);

const inner = { scrollTop: 0, scrollHeight: 120 } as HTMLElement;
const workbenchHost = { scrollTop: 0, scrollHeight: 960 } as HTMLElement;
const resolved = resolveScrollElement(inner, workbenchHost);
ok(resolved === workbenchHost, "workbench outer viewport is the single scroll owner");
if (resolved) snapElementToBottom(resolved);
ok(workbenchHost.scrollTop === 960, "session restore bottom-anchors the outer viewport");
ok(inner.scrollTop === 0, "session restore does not scroll the overflow-visible inner transcript");

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
