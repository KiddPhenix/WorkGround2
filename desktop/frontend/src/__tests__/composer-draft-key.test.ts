import { composerDraftKeyForTab } from "../lib/composerDraftKey";

function expectEqual(actual: string, expected: string, label: string) {
  if (actual !== expected) {
    throw new Error(`${label}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
  }
}

expectEqual(
  composerDraftKeyForTab({
    id: "tab-a",
    scope: "project",
    workspaceRoot: "D:/Work/A",
    topicId: "topic-a",
    sessionPath: "D:/Work/A/.codex/sessions/a.jsonl",
    blank: true,
  }),
  ["blank-workspace", "project", "D:/Work/A"].join("\u0000"),
  "blank project tab uses workspace draft key",
);

expectEqual(
  composerDraftKeyForTab({
    id: "tab-a",
    scope: "project",
    workspaceRoot: "D:/Work/A",
    topicId: "topic-a",
    sessionPath: "D:/Work/A/.codex/sessions/a.jsonl",
  }),
  ["session-topic", "project", "D:/Work/A", "topic-a"].join("\u0000"),
  "started project tab uses topic draft key",
);

expectEqual(
  composerDraftKeyForTab({
    id: "tab-global",
    scope: "global",
    blank: true,
  }),
  ["blank-workspace", "global", ""].join("\u0000"),
  "blank global tab uses global draft key",
);
