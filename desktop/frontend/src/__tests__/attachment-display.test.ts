// Run: tsx src/__tests__/attachment-display.test.ts

import { baseName, formatAttachmentRefForDisplay, formatAttachmentRefForSubmit, parseAttachmentRefsForDisplay, replaceAttachmentRefsForDisplay, restoreAttachmentRefsForSubmit, sortDisplayAttachments } from "../lib/attachmentDisplay";

let passed = 0;
let failed = 0;

function eq(a: unknown, b: unknown, label: string) {
  if (JSON.stringify(a) === JSON.stringify(b)) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`);
    failed += 1;
  }
}

console.log("\nattachment display");

const named = parseAttachmentRefsForDisplay(
  "review @[DS30000.sl2](.WorkGround2/attachments/clipboard-20260610-121238.444775-000002.sl2) and @[park.png](.WorkGround2/attachments/clipboard-20260610-121238.444775-000001.png)",
);

eq(named.text, "review and", "removes named display refs from message text");
eq(
  named.attachments.map((a) => ({ path: a.path, name: a.name, kind: a.kind, ext: a.ext })),
  [
    {
      path: ".WorkGround2/attachments/clipboard-20260610-121238.444775-000002.sl2",
      name: "DS30000.sl2",
      kind: "file",
      ext: "SL2",
    },
    {
      path: ".WorkGround2/attachments/clipboard-20260610-121238.444775-000001.png",
      name: "park.png",
      kind: "image",
      ext: "PNG",
    },
  ],
  "preserves original display names for attachment cards",
);
eq(
  sortDisplayAttachments(named.attachments).map((a) => a.name),
  ["park.png", "DS30000.sl2"],
  "sorts images before files while keeping groups stable",
);
eq(
  parseAttachmentRefsForDisplay("review @.workground2/attachments/clipboard.png").attachments[0]?.kind,
  "image",
  "recognizes lowercase attachment roots as image attachments",
);
eq(
  replaceAttachmentRefsForDisplay("see @[DS30000.sl2](.WorkGround2/attachments/clipboard-20260610-121238.444775-000002.sl2)"),
  "see [file:DS30000.sl2]",
  "compact previews use named display refs",
);
eq(
  restoreAttachmentRefsForSubmit("review @[DS30000.sl2](.WorkGround2/attachments/clipboard-20260610-121238.444775-000002.sl2), then @[park.png](.WorkGround2/attachments/clipboard-20260610-121238.444775-000001.png)"),
  "review @.WorkGround2/attachments/clipboard-20260610-121238.444775-000002.sl2, then @.WorkGround2/attachments/clipboard-20260610-121238.444775-000001.png",
  "restores named display refs for submit",
);
eq(
  formatAttachmentRefForDisplay({ path: ".WorkGround2/attachments/clipboard-20260610-121238.444775-000001.png", name: "park.png", source: "attachment" }),
  "@[park.png](.WorkGround2/attachments/clipboard-20260610-121238.444775-000001.png)",
  "formats attachment display refs for edit replay",
);
eq(
  formatAttachmentRefForSubmit({ path: ".WorkGround2/attachments/clipboard-20260610-121238.444775-000001.png" }),
  "@.WorkGround2/attachments/clipboard-20260610-121238.444775-000001.png",
  "formats raw attachment refs for edit replay submit",
);
eq(baseName("C:\\Users\\Abyss\\Desktop\\DS30000.sl2"), "DS30000.sl2", "extracts Windows path basenames");
eq(baseName("/Users/abyss/Desktop/park.png"), "park.png", "extracts POSIX path basenames");

// Case-insensitive attachment root: Go backend produces lowercase .workground2/attachments/
{
  const parsed = parseAttachmentRefsForDisplay(
    "see @[cat.png](.workground2/attachments/clipboard-20260712-000001.png)",
  );
  const a = parsed.attachments[0];
  eq(a?.kind, "image", "lowercase workground2 path: kind is image");
  eq(a?.source, "attachment", "lowercase workground2 path: source is attachment");
  eq(a?.ext, "PNG", "lowercase workground2 path: ext is PNG");
}

// Backward compat: capital .WorkGround2/attachments/ should still work
{
  const parsed = parseAttachmentRefsForDisplay(
    "see @[dog.png](.WorkGround2/attachments/clipboard-20260712-000002.png)",
  );
  const a = parsed.attachments[0];
  eq(a?.kind, "image", "capital WorkGround2 path: kind is image");
  eq(a?.source, "attachment", "capital WorkGround2 path: source is attachment");
}

// Workspace image paths should be detected as image kind
{
  const parsed = parseAttachmentRefsForDisplay(
    "see @./screenshots/bug.png",
  );
  const a = parsed.attachments[0];
  eq(a?.kind, "image", "workspace image path: kind is image");
  eq(a?.source, "workspace", "workspace image path: source is workspace");
  eq(a?.ext, "PNG", "workspace image path: ext is PNG");
}

// Workspace non-image paths remain file
{
  const parsed = parseAttachmentRefsForDisplay(
    "see @./docs/readme.pdf",
  );
  const a = parsed.attachments[0];
  eq(a?.kind, "file", "workspace non-image path: kind is file");
  eq(a?.source, "workspace", "workspace non-image path: source is workspace");
}

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
