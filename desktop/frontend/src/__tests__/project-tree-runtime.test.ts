// Run: tsx src/__tests__/project-tree-runtime.test.ts

import {
  projectTreeFolderDisclosure,
  projectTreeActiveKey,
  defaultExpandedProjectTreeKeys,
  activeSessionAncestorKeys,
  projectTreeTopicOpenRequest,
  projectTreeNodeScope,
  projectTreeShouldSuppressOpenForRename,
  projectTreeSessionPathMatches,
  projectTreeReadActivityKey,
  projectTreeReadActivityAfter,
  projectTreeTopicHasUnreadActivity,
  projectTreeTopicVisualState,
  projectTreeShouldRenderTopicActions,
  projectTreeIsExternalCall,
  parseWorkbenchRecentSettings,
  reorderedProjectRoots,
  splitWorkbenchRecentTree,
  projectTreeUnreadConversations,
  projectTreeUnreadCount,
  projectTreeMenuKey,
  projectTreeTrashTarget,
  projectTreeCanRenameTopic,
  projectTreeUnreadFallbackConversation,
  openLegacyUnreadConversation,
} from "../components/ProjectTree";
import type { ProjectNode, UnreadConversation } from "../lib/types";

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

console.log("\nproject tree runtime sessions");

const tree: ProjectNode[] = [
  {
    key: "global_folder",
    kind: "global_folder",
    label: "Global",
    children: [
      {
        key: "global_topic_topic-a",
        kind: "global_topic",
        label: "Topic A",
        topicId: "topic-a",
        children: [
          {
            key: "global_session_a",
            kind: "global_session",
            label: "Session A",
            topicId: "topic-a",
            sessionPath: "/tmp/a.jsonl",
          },
          {
            key: "global_session_b",
            kind: "global_session",
            label: "Session B",
            topicId: "topic-a",
            sessionPath: "/tmp/b.jsonl",
          },
        ],
      },
      {
        key: "global_topic_topic-b",
        kind: "global_topic",
        label: "Topic B",
        topicId: "topic-b",
      },
    ],
  },
];

eq(
  defaultExpandedProjectTreeKeys(tree),
  [],
  "without an active tab, no folders default to expanded",
);

eq(
  defaultExpandedProjectTreeKeys(tree, "global", "", "topic-a", "/tmp/b.jsonl"),
  ["global_folder", "global_topic_topic-a"],
  "active session path expands only ancestor folders",
);

eq(
  activeSessionAncestorKeys(tree, "global", "", "topic-a", "/tmp/b.jsonl"),
  ["global_folder", "global_topic_topic-a"],
  "activeSessionAncestorKeys matches defaultExpandedProjectTreeKeys for active session",
);

eq(
  activeSessionAncestorKeys(tree, "global", "", "topic-b"),
  ["global_folder"],
  "active topic without runtime session rows expands only parent folders",
);

eq(
  projectTreeTopicOpenRequest(tree[0].children?.[0].children?.[1] as ProjectNode),
  { scope: "global", workspaceRoot: "", topicId: "topic-a", sessionPath: "/tmp/b.jsonl" },
  "runtime session row opens the concrete session path",
);

const globalWorkTree: ProjectNode[] = [
  {
    key: "global_folder",
    kind: "global_folder",
    label: "Global",
    children: [
      {
        key: "global_topic_work",
        kind: "global_topic",
        label: "Work",
        topicId: "global-work",
        children: [
          {
            key: "global_work_session_active",
            kind: "global_work_session",
            label: "Work run",
            topicId: "global-work",
            sessionPath: "C:\\sessions\\work.jsonl",
            sessionKind: "work",
          },
        ],
      },
    ],
  },
];

eq(
  projectTreeNodeScope(globalWorkTree[0].children?.[0].children?.[0] as ProjectNode),
  "global",
  "global Work runtime rows retain global scope",
);

eq(
  projectTreeTopicOpenRequest(globalWorkTree[0].children?.[0].children?.[0] as ProjectNode),
  { scope: "global", workspaceRoot: "", topicId: "global-work", sessionPath: "C:\\sessions\\work.jsonl" },
  "global Work runtime row opens without a project root",
);

eq(
  projectTreeActiveKey(globalWorkTree, "global", "", "global-work", "c:/sessions/work.jsonl"),
  "global_work_session_active",
  "only the concrete runtime row owns the selected state",
);

eq(
  projectTreeCanRenameTopic(globalWorkTree[0].children?.[0].children?.[0] as ProjectNode),
  true,
  "Work runtime rows can rename their owning topic from the session list",
);

eq(
  projectTreeCanRenameTopic(tree[0].children?.[0].children?.[0] as ProjectNode),
  false,
  "normal runtime rows keep per-session semantics and cannot rename the owning topic",
);

eq(
  projectTreeTopicOpenRequest({
    key: "topic_project",
    kind: "topic",
    label: "Project topic",
    root: "/repo",
    topicId: "topic-project",
  }),
  { scope: "project", workspaceRoot: "/repo", topicId: "topic-project", sessionPath: undefined },
  "regular project topic still opens by topic",
);

eq(
  projectTreeTopicOpenRequest({
    key: "topic_running",
    kind: "global_topic",
    label: "Running topic",
    topicId: "topic-running",
    running: true,
    status: "thinking",
    turnStartedAt: 1234,
  })?.runtimeHint,
  { running: true, status: "thinking", turnStartedAt: 1234 },
  "running project-tree topic carries runtime hint into navigation",
);

const completedTopic: ProjectNode = {
  key: "topic_complete",
  kind: "topic",
  label: "Completed",
  root: "/repo",
  topicId: "topic-complete",
  lastActivityAt: 2000,
};
const completedTopicKey = projectTreeReadActivityKey(completedTopic) ?? "";

const unreadActivity = projectTreeReadActivityAfter(completedTopic, {});
eq(
  unreadActivity[completedTopicKey],
  completedTopic.lastActivityAt,
  "marking a completed recent row read records its visible activity watermark",
);
eq(
  projectTreeReadActivityAfter(completedTopic, unreadActivity) === unreadActivity,
  true,
  "repeating the same read receipt is idempotent",
);

const recoveredTopicPath = "C:\\sessions\\topic-complete-recovery.jsonl";
eq(
  projectTreeReadActivityKey({ ...completedTopic, sessionPath: recoveredTopicPath }),
  completedTopicKey,
  "topic read identity stays stable when recovery changes its bound session path",
);

eq(
  projectTreeTopicHasUnreadActivity(
    { ...completedTopic, sessionPath: recoveredTopicPath },
    { [completedTopicKey]: completedTopic.lastActivityAt ?? 0 },
    "project",
    "/repo",
    "other-topic",
  ),
  false,
  "recovered transcript does not revive an already-read topic",
);

const runtimeSessionReadKey = projectTreeReadActivityKey({
  ...completedTopic,
  kind: "session",
  sessionPath: "C:\\sessions\\runtime-a.jsonl",
});
eq(
  runtimeSessionReadKey !== projectTreeReadActivityKey({
    ...completedTopic,
    kind: "session",
    sessionPath: "C:\\sessions\\runtime-b.jsonl",
  }),
  true,
  "concrete runtime sessions under one topic keep independent read identities",
);

eq(
  projectTreeTopicHasUnreadActivity(completedTopic, { [completedTopicKey]: 1000 }, "project", "/repo", "other-topic"),
  true,
  "completed inactive topic with newer activity shows unread attention",
);

eq(
  projectTreeTopicHasUnreadActivity(completedTopic, { [completedTopicKey]: 2000 }, "project", "/repo", "other-topic"),
  false,
  "completed topic stops showing unread attention once opened at its latest activity",
);

eq(
  projectTreeTopicHasUnreadActivity(completedTopic, { [completedTopicKey]: 1000 }, "project", "/repo", "topic-complete"),
  false,
  "active topic does not show unread attention",
);

eq(
  projectTreeTopicHasUnreadActivity({ ...completedTopic, status: "streaming", running: true }, { [completedTopicKey]: 1000 }, "project", "/repo", "other-topic"),
  false,
  "running topic keeps runtime status instead of completed-unread attention",
);

eq(
  projectTreeTopicVisualState(completedTopic, true),
  "done",
  "completed unread topic shows done visual state",
);

eq(
  projectTreeTopicVisualState(completedTopic, false),
  "none",
  "completed read topic hides done visual state",
);

eq(
  projectTreeTopicVisualState({ ...completedTopic, running: true, status: "waiting_confirmation" }, false),
  "running",
  "backend RunningWork stays authoritative when an event status is stale",
);

const failedTopic: ProjectNode = { ...completedTopic, status: "error" };

eq(
  projectTreeTopicHasUnreadActivity(failedTopic, { [completedTopicKey]: 1000 }, "project", "/repo", "other-topic"),
  true,
  "failed inactive topic with newer activity is unread",
);

eq(
  projectTreeTopicVisualState(failedTopic, true),
  "failed",
  "failed unread topic shows failed visual state",
);

eq(
  projectTreeTopicVisualState(failedTopic, false),
  "none",
  "failed read topic hides failed visual state",
);

eq(
  projectTreeShouldRenderTopicActions(true, false),
  true,
  "read workbench topic renders hover actions",
);

eq(
  projectTreeShouldRenderTopicActions(true, true),
  false,
  "unread workbench topic omits hover actions from the keyboard tab order",
);

eq(
  projectTreeShouldRenderTopicActions(true, false),
  true,
  "read workbench Session rows expose their actions",
);

eq(
  projectTreeShouldRenderTopicActions(true, false),
  true,
  "Work Session rows expose hover actions so they can be deleted",
);

eq(
  projectTreeMenuKey("recent", "topic-a") === projectTreeMenuKey("projects", "topic-a"),
  false,
  "duplicate Recent and Projects rows keep independent menu identities",
);

eq(
  projectTreeTrashTarget({ key: "topic-a", kind: "topic", label: "A", topicId: "topic-a" }),
  { kind: "topic", topicId: "topic-a" },
  "topic rows move the whole topic to trash",
);

eq(
  projectTreeTrashTarget({ key: "work-a", kind: "work_session", label: "Work", topicId: "topic-work", sessionPath: "/tmp/work.jsonl", sessionKind: "work" }),
  { kind: "topic", topicId: "topic-work" },
  "Work Session rows retain topic-level trash semantics",
);

eq(
  projectTreeTrashTarget({ key: "session-a", kind: "session", label: "Run", topicId: "topic-a", sessionPath: "/tmp/run.jsonl" }),
  { kind: "session", path: "/tmp/run.jsonl" },
  "runtime Session rows move only the selected session to trash",
);

eq(
  projectTreeTrashTarget({ key: "crew-a", kind: "crew_session", label: "IM", sessionPath: "/tmp/crew.jsonl" }),
  { kind: "session", path: "/tmp/crew.jsonl" },
  "Crew Session rows can be moved to trash by path",
);

eq(
  projectTreeShouldSuppressOpenForRename(
    { rowKey: "topic-a", canRename: true },
    { rowKey: "topic-a", canRename: true },
  ),
  true,
  "second click on the same renameable topic suppresses open for inline rename",
);

eq(
  projectTreeShouldSuppressOpenForRename(
    { rowKey: "session-a", canRename: false },
    { rowKey: "session-a", canRename: false },
  ),
  false,
  "runtime session double-click still allows the session row to open",
);

eq(
  projectTreeShouldSuppressOpenForRename(
    { rowKey: "topic-a", canRename: true },
    { rowKey: "topic-b", canRename: true },
  ),
  false,
  "quickly clicking a different topic still opens the new target",
);

eq(
  projectTreeFolderDisclosure(false, true),
  {
    canExpand: false,
    isOpen: false,
    ariaExpanded: undefined,
    iconStackClassName: "project-tree__icon-stack",
  },
  "empty project folders are not exposed as expandable disclosure rows",
);

eq(
  projectTreeFolderDisclosure(true, false),
  {
    canExpand: true,
    isOpen: false,
    ariaExpanded: false,
    iconStackClassName: "project-tree__icon-stack project-tree__icon-stack--expandable",
  },
  "collapsed project folders keep disclosure semantics when children exist",
);

eq(
  projectTreeFolderDisclosure(true, true),
  {
    canExpand: true,
    isOpen: true,
    ariaExpanded: true,
    iconStackClassName: "project-tree__icon-stack project-tree__icon-stack--expandable",
  },
  "expanded project folders can show the open-folder state only when children exist",
);

// === Crew nodes ===

const crewSession: ProjectNode = {
  key: "crew_session_1",
  kind: "crew_session",
  label: "WeChat · test-user",
  sessionPath: "/tmp/crew-session.jsonl",
  sessionSource: "auto",
  channel: "weixin",
  turns: 5,
  createdAt: 1000,
  lastActivityAt: 2000,
};
const crewFolder: ProjectNode = {
  key: "crew_folder",
  kind: "crew_folder",
  label: "Crew",
  children: [crewSession],
};

eq(
  projectTreeTopicOpenRequest(crewSession),
  null,
  "crew_session returns null from projectTreeTopicOpenRequest (opens via onOpenCrewSession)",
);

eq(
  activeSessionAncestorKeys([crewFolder], "global", "", "", "/tmp/crew-session.jsonl"),
  ["crew_folder"],
  "crew folder key appears in active ancestor keys when a crew session is active",
);

eq(
  projectTreeSessionPathMatches("D:\\Temp\\Crew-Session.jsonl", "d:/temp/crew-session.jsonl"),
  true,
  "session path matching tolerates Windows slash and drive-case differences",
);

const unreadConversations = [
  { key: "room:one", source: "room" as const, sessionId: "session-room", latestSequence: 8, readSequence: 5, unreadCount: 3, highPriorityCount: 1 },
  { key: "im:one", source: "im" as const, sessionId: "path:D:\\Temp\\Crew-Session.jsonl", latestSequence: 4, readSequence: 2, unreadCount: 2, highPriorityCount: 0 },
  { key: "im:read", source: "im" as const, sessionId: "path:D:\\Temp\\Crew-Session.jsonl", latestSequence: 1, readSequence: 1, unreadCount: 0, highPriorityCount: 0 },
];
const unreadNode: ProjectNode = { ...crewSession, sessionPath: "d:/temp/crew-session.jsonl" };

eq(
  projectTreeUnreadCount({ ...unreadNode, sessionId: "session-room" }, unreadConversations),
  5,
  "recent unread count sums exact Session ID and normalized path bindings",
);
eq(
  projectTreeUnreadConversations({ ...unreadNode, sessionId: "other" }, unreadConversations).map((conversation) => conversation.key),
  ["im:one"],
  "read conversations and unrelated Session IDs do not create recent badges",
);

// Regression: single normal-session topic node must match unread by sessionId (UUID).
eq(
  projectTreeUnreadCount(
    { key: "topic_t1", kind: "topic", label: "Chat", topicId: "t1", sessionId: "session-uuid-1" },
    [{ key: "session:session-uuid-1", source: "session" as const, sessionId: "session-uuid-1", latestSequence: 5, readSequence: 3, unreadCount: 2, highPriorityCount: 0 }],
  ),
  2,
  "topic node with sessionId matches unread by direct UUID",
);

// Regression: single normal-session topic node must match unread via path: prefix
// when node.sessionPath is set but node.sessionId differs from conversation.sessionId.
eq(
  projectTreeUnreadCount(
    { key: "topic_t2", kind: "topic", label: "Dev", topicId: "t2", sessionPath: "d:/work/session.jsonl" },
    [{ key: "session:path:D:\\work\\session.jsonl", source: "session" as const, sessionId: "path:D:\\work\\session.jsonl", latestSequence: 3, readSequence: 1, unreadCount: 2, highPriorityCount: 1 }],
  ),
  2,
  "topic node with sessionPath matches unread via path: prefix with Windows slash/drive normalization",
);

// Regression: taskbar totalUnread=1 must be visible on the corresponding recent node.
eq(
  projectTreeUnreadCount(
    { key: "topic_t3", kind: "global_topic", label: "Solo", topicId: "t3", sessionId: "solo-id", sessionPath: "/tmp/solo.jsonl" },
    [{ key: "session:solo-id", source: "session" as const, sessionId: "solo-id", latestSequence: 1, readSequence: 0, unreadCount: 1, highPriorityCount: 0 }],
  ),
  1,
  "totalUnread=1 maps to exactly one recent node (no ghost badge)",
);

// Regression: opening the matching session clears the unread via MarkUnreadRead.
const markReadConversations = [
  { key: "session:mark-me", source: "session" as const, sessionId: "path:/home/dev/chat.jsonl", latestSequence: 10, readSequence: 5, unreadCount: 5, highPriorityCount: 2 },
];
const markReadNode: ProjectNode = { key: "topic_t4", kind: "topic", label: "Mark", topicId: "t4", sessionPath: "/home/dev/chat.jsonl" };
const matched = projectTreeUnreadConversations(markReadNode, markReadConversations);
eq(
  matched.length,
  1,
  "open-session node matches its unread conversation",
);
if (matched.length === 1) {
  eq(matched[0].key, "session:mark-me", "matched conversation key is correct for MarkUnreadRead");
  eq(matched[0].latestSequence, 10, "latestSequence is available for MarkUnreadRead UpToSequence");
}

eq(
  reorderedProjectRoots(
    [
      { key: "global_folder", kind: "global_folder", label: "Global" },
      crewFolder,
      { key: "project_a", kind: "project", label: "A", root: "/a" },
      { key: "project_b", kind: "project", label: "B", root: "/b" },
    ],
    "/b",
    "/a",
    "before",
  ),
  ["__global__", "/b", "/a"],
  "project reorder excludes virtual Crew from persisted order",
);

const recentProject: ProjectNode = {
  key: "project_recent",
  kind: "project",
  label: "Recent",
  root: "/recent",
  children: [
    { key: "topic_local", kind: "topic", label: "Local", root: "/recent", topicId: "local", lastActivityAt: 100 },
    { key: "topic_cli", kind: "topic", label: "CLI", root: "/recent", topicId: "cli", sessionSource: "cli", lastActivityAt: 500 },
    { key: "topic_im", kind: "topic", label: "IM", root: "/recent", topicId: "im", sessionSource: "auto", channel: "weixin", lastActivityAt: 400 },
    { key: "topic_work", kind: "topic", label: "Work", root: "/recent", topicId: "work", sessionSource: "work:w1/run:r1", lastActivityAt: 300 },
    { key: "topic_collab", kind: "topic", label: "Room", root: "/recent", topicId: "room", sessionSource: "collaboration", lastActivityAt: 200 },
  ],
};

eq(projectTreeIsExternalCall(recentProject.children?.[1] as ProjectNode), true, "CLI sessions are external calls");
eq(projectTreeIsExternalCall(recentProject.children?.[2] as ProjectNode), true, "IM sessions are external calls");
eq(projectTreeIsExternalCall(recentProject.children?.[3] as ProjectNode), false, "Work child sessions stay internal");
eq(projectTreeIsExternalCall(recentProject.children?.[4] as ProjectNode), false, "collaboration sessions stay internal");

eq(
  splitWorkbenchRecentTree([recentProject], "updated", { showExternal: true, limit: 3 }).recent.map((node) => node.key),
  ["topic_cli", "topic_im", "topic_work"],
  "recent section applies the configured row limit after activity sorting",
);
eq(
  splitWorkbenchRecentTree([recentProject], "updated", { showExternal: false, limit: 3 }).recent.map((node) => node.key),
  ["topic_work", "topic_collab", "topic_local"],
  "recent section can hide typed external calls without removing project children",
);
eq(
  splitWorkbenchRecentTree([recentProject], "updated", { showExternal: false, limit: 3 }).projects[0].children?.length,
  5,
  "recent filtering keeps the project tree as the single complete source",
);
eq(
  splitWorkbenchRecentTree([crewFolder, recentProject], "updated", { showExternal: true, limit: 10 }).recent.some((node) => node.kind === "crew_session"),
  true,
  "IM Crew sessions participate in Recent when external calls are visible",
);
eq(
  splitWorkbenchRecentTree([crewFolder, recentProject], "updated", { showExternal: false, limit: 10 }).recent.some((node) => node.kind === "crew_session"),
  false,
  "hiding external calls also removes IM Crew sessions from Recent",
);
eq(
  parseWorkbenchRecentSettings({ showExternal: false, limit: 5 }),
  { showExternal: false, limit: 5 },
  "valid recent preferences round-trip",
);
eq(
  parseWorkbenchRecentSettings({ showExternal: "no", limit: 99 }),
  { showExternal: true, limit: 1 },
  "invalid recent preferences recover to safe defaults",
);

eq(
  activeSessionAncestorKeys([{
    key: "crew_folder_windows",
    kind: "crew_folder",
    label: "Crew",
    children: [{ ...crewSession, sessionPath: "d:/temp/crew-session.jsonl" }],
  }], "global", "", "", "D:\\Temp\\Crew-Session.jsonl"),
  ["crew_folder_windows"],
  "crew folder expands when active session path differs only by Windows formatting",
);

// === projectTreeMenuKey regression — scoped by tree section ===

const menuKeyA = projectTreeMenuKey("recent", "topic-1");
const menuKeyB = projectTreeMenuKey("projects", "topic-1");

eq(
  menuKeyA === menuKeyB,
  false,
  "same node key has distinct menu identities for Recent vs Projects sections",
);

eq(
  projectTreeMenuKey("recent", "topic-1"),
  menuKeyA,
  "menu identity is stable within the same section",
);

eq(
  projectTreeMenuKey("projects", "topic-1"),
  menuKeyB,
  "menu identity is stable within the projects section",
);

eq(
  projectTreeMenuKey("recent", "") === projectTreeMenuKey("projects", ""),
  false,
  "empty node keys also differ across sections",
);

// === Unread-aware splitWorkbenchRecentTree regression tests ===

const unreadTestProject: ProjectNode = {
  key: "project_ut",
  kind: "project",
  label: "UT",
  root: "/ut",
  children: [
    { key: "ut_unread_a", kind: "topic", label: "Unread A", root: "/ut", topicId: "ua", sessionId: "sid:ua", lastActivityAt: 100 },
    { key: "ut_unread_b", kind: "topic", label: "Unread B", root: "/ut", topicId: "ub", sessionId: "sid:ub", lastActivityAt: 200 },
    { key: "ut_read_c", kind: "topic", label: "Read C", root: "/ut", topicId: "uc", lastActivityAt: 500 },
    { key: "ut_read_d", kind: "topic", label: "Read D", root: "/ut", topicId: "ud", lastActivityAt: 50 },
    { key: "ut_ext_unread", kind: "topic", label: "Ext Unread", root: "/ut", topicId: "ue", sessionId: "sid:ue", sessionSource: "cli", lastActivityAt: 80 },
  ],
};

const recentUnreadConvs: UnreadConversation[] = [
  { key: "sid:ua", source: "session", sessionId: "sid:ua", unreadCount: 3, latestSequence: 10, readSequence: 7, highPriorityCount: 0, title: "Conv A" },
  { key: "sid:ub", source: "room", sessionId: "sid:ub", unreadCount: 1, latestSequence: 5, readSequence: 4, highPriorityCount: 0, title: "Conv B" },
  { key: "sid:ue", source: "im", sessionId: "sid:ue", unreadCount: 2, latestSequence: 8, readSequence: 6, highPriorityCount: 0, title: "Ext Conv" },
  { key: "legacy:orphan-uuid", source: "im", sessionId: "legacy:orphan-uuid", unreadCount: 5, latestSequence: 20, readSequence: 15, highPriorityCount: 0, title: "Orphan Chat" },
];

// 1. limit=1 with 2+ unreads → all unreads still shown; read within limit
const limit1Result = splitWorkbenchRecentTree([unreadTestProject], "updated", { showExternal: true, limit: 1 }, recentUnreadConvs);
eq(
  limit1Result.recent.map((n) => n.key),
  ["ut_unread_b", "ut_unread_a", "ut_ext_unread", "unread_fallback:legacy:orphan-uuid", "ut_read_c"],
  "unread items all appear before read items; read limited to 1",
);
eq(limit1Result.recent.length >= 4, true, "limit=1 does not cut unread items");

// 2. showExternal=false still shows unread external sources; hides read external
const extFilterResult = splitWorkbenchRecentTree([unreadTestProject], "updated", { showExternal: false, limit: 5 }, recentUnreadConvs);
eq(
  extFilterResult.recent.some((n) => n.key === "ut_ext_unread"),
  true,
  "unread external source bypasses showExternal filter",
);
eq(
  extFilterResult.recent.some((n) => n.key === "ut_read_d"),
  true,
  "read internal source still shown with showExternal=false",
);

// 3. Dedup — same sessionId/conversation only appears once in Recent
eq(
  limit1Result.recent.filter((n) => n.sessionId === "sid:ua").length,
  1,
  "node with active unread appears only once in Recent (not duplicated)",
);

// 4. Unmapped legacy UUID gets fallback row
const fallbackRow = limit1Result.recent.find((n) => n.key.startsWith("unread_fallback:"));
eq(Boolean(fallbackRow), true, "unmapped legacy UUID creates a visible fallback row");
if (fallbackRow) {
  eq(fallbackRow.label, "Orphan Chat", "fallback row shows conversation title");
  eq(fallbackRow.kind, "topic", "fallback row is a topic-kind node");
  eq(
    projectTreeUnreadFallbackConversation(fallbackRow, recentUnreadConvs)?.key,
    "legacy:orphan-uuid",
    "fallback row resolves to its unread conversation without embedding mutable state in the node",
  );
}

// 5. Multiple unread conversations bound to one Session aggregate into one row.
const sharedSessionConvs: UnreadConversation[] = [
  { key: "session:shared", source: "session", sessionId: "sid:ua", unreadCount: 2, latestSequence: 2, readSequence: 0, highPriorityCount: 0 },
  { key: "room:shared", source: "room", sessionId: "sid:ua", unreadCount: 1, latestSequence: 1, readSequence: 0, highPriorityCount: 1 },
];
const sharedSessionResult = splitWorkbenchRecentTree([unreadTestProject], "updated", { showExternal: true, limit: 1 }, sharedSessionConvs);
eq(
  sharedSessionResult.recent.filter((node) => node.sessionId === "sid:ua").length,
  1,
  "multiple unread conversations for one Session produce one Recent row",
);
eq(
  sharedSessionResult.recent.some((node) => node.key.startsWith("unread_fallback:")),
  false,
  "a second unread conversation for the same Session does not become a fallback row",
);

// 6. Backward compat — no unreadConversations behaves like before (no unread param)
const legacyResult = splitWorkbenchRecentTree([unreadTestProject], "updated", { showExternal: true, limit: 3 });
eq(
  legacyResult.recent.length,
  3,
  "without unreadConversations, limit is respected as before",
);
eq(
  legacyResult.recent.some((n) => n.key.startsWith("unread_fallback:")),
  false,
  "without unreadConversations, no fallback rows are generated",
);

// 7. sort stability — unread items respect running-first, then activity
const runningUnreadNode: ProjectNode = {
  key: "project_run",
  kind: "project",
  label: "Run",
  root: "/run",
  children: [
    { key: "run_normal", kind: "topic", label: "Normal", root: "/run", topicId: "rn", sessionId: "sid:rn", lastActivityAt: 300 },
    { key: "run_active", kind: "topic", label: "Active", root: "/run", topicId: "ra", sessionId: "sid:ra", lastActivityAt: 50, running: true },
  ],
};
const runningConvs: UnreadConversation[] = [
  { key: "sid:rn", source: "session", sessionId: "sid:rn", unreadCount: 1, latestSequence: 3, readSequence: 2, highPriorityCount: 0 },
  { key: "sid:ra", source: "session", sessionId: "sid:ra", unreadCount: 1, latestSequence: 2, readSequence: 1, highPriorityCount: 0 },
];
const runningResult = splitWorkbenchRecentTree([runningUnreadNode], "updated", { showExternal: true, limit: 5 }, runningConvs);
eq(
  runningResult.recent[0]?.key,
  "run_active",
  "running unread node sorts before non-running unread regardless of activity",
);

// 8. project tree children stay intact
eq(
  limit1Result.projects.length,
  1,
  "project count unchanged with unread conversations",
);
eq(
  limit1Result.projects[0].children?.length,
  5,
  "project children count unchanged with unread conversations",
);

// 9. SESSION-source unread fallback node is distinguishable from IM/room/work.
const sessionFallbackConvs: UnreadConversation[] = [
  { key: "session:legacy-uuid", source: "session", sessionId: "session_f719de92b7ada7e462b8afd646331866", unreadCount: 1, latestSequence: 5, readSequence: 4, highPriorityCount: 0, title: "Legacy Session" },
];
const sessionFallbackResult = splitWorkbenchRecentTree([unreadTestProject], "updated", { showExternal: true, limit: 10 }, sessionFallbackConvs);
const sessionFallbackRow = sessionFallbackResult.recent.find((n) => n.key.startsWith("unread_fallback:"));
eq(Boolean(sessionFallbackRow), true, "legacy UUID session-source unread creates a fallback row");
if (sessionFallbackRow) {
  eq(sessionFallbackRow.label, "Legacy Session", "session fallback row shows conversation title");
  const fbConv = projectTreeUnreadFallbackConversation(sessionFallbackRow, sessionFallbackConvs);
  eq(Boolean(fbConv), true, "session fallback row resolves to its unread conversation");
  if (fbConv) {
    eq(fbConv.source, "session", "session fallback conversation source identifies as session for resolution");
    eq(fbConv.key, "session:legacy-uuid", "session fallback conversation key is preserved for ResolveLegacySessionUnread");
  }
}

const legacyOpenSteps: string[] = [];
await openLegacyUnreadConversation(
  sessionFallbackConvs[0],
  async (key) => {
    legacyOpenSteps.push(`resolve:${key}`);
    return { scope: "project", workspaceRoot: "/ut", topicId: "ua", sessionPath: "/ut/legacy.jsonl", topicTitle: "Legacy Session" };
  },
  async (target) => { legacyOpenSteps.push(`open:${target.sessionPath}`); },
  async (key, sequence) => { legacyOpenSteps.push(`read:${key}:${sequence}`); },
);
eq(
  legacyOpenSteps,
  ["resolve:session:legacy-uuid", "open:/ut/legacy.jsonl", "read:session:legacy-uuid:5"],
  "legacy unread is marked read only after its resolved Session opens",
);

const failedOpenSteps: string[] = [];
try {
  await openLegacyUnreadConversation(
    sessionFallbackConvs[0],
    async () => ({ scope: "project", workspaceRoot: "/ut", topicId: "ua", sessionPath: "/ut/missing.jsonl", topicTitle: "Legacy Session" }),
    async () => { failedOpenSteps.push("open"); throw new Error("open failed"); },
    async () => { failedOpenSteps.push("read"); },
  );
} catch {
  failedOpenSteps.push("error");
}
eq(failedOpenSteps, ["open", "error"], "failed legacy Session open preserves unread state");

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
