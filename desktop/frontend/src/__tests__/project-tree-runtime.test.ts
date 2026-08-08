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
  projectTreeTopicHasUnreadActivity,
  projectTreeTopicVisualState,
  projectTreeShouldRenderTopicActions,
  projectTreeIsExternalCall,
  parseWorkbenchRecentSettings,
  reorderedProjectRoots,
  splitWorkbenchRecentTree,
  projectTreeUnreadConversations,
  projectTreeUnreadCount,
} from "../components/ProjectTree";
import type { ProjectNode } from "../lib/types";

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

const failedTopic = { ...completedTopic, status: "error" };

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
  projectTreeShouldRenderTopicActions(false, true, false),
  true,
  "read workbench topic renders hover actions",
);

eq(
  projectTreeShouldRenderTopicActions(false, true, true),
  false,
  "unread workbench topic omits hover actions from the keyboard tab order",
);

eq(
  projectTreeShouldRenderTopicActions(true, true, false),
  false,
  "runtime session rows do not render topic hover actions",
);

eq(
  projectTreeShouldRenderTopicActions(true, true, false, false, true),
  true,
  "Work Session rows expose hover actions so they can be deleted",
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

const crewFolder: ProjectNode = {
  key: "crew_folder",
  kind: "crew_folder",
  label: "Crew",
  children: [
    {
      key: "crew_session_1",
      kind: "crew_session",
      label: "WeChat · test-user",
      sessionPath: "/tmp/crew-session.jsonl",
      sessionSource: "auto",
      channel: "weixin",
      turns: 5,
      createdAt: 1000,
      lastActivityAt: 2000,
    },
  ],
};

eq(
  projectTreeTopicOpenRequest(crewFolder.children[0]),
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
const unreadNode = { ...crewFolder.children[0], sessionPath: "d:/temp/crew-session.jsonl" };

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
    children: [{ ...crewFolder.children[0], sessionPath: "d:/temp/crew-session.jsonl" }],
  }], "global", "", "", "D:\\Temp\\Crew-Session.jsonl"),
  ["crew_folder_windows"],
  "crew folder expands when active session path differs only by Windows formatting",
);

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
