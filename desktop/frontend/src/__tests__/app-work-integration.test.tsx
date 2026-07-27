import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { projectTreeTopicOpenRequest } from "../components/ProjectTree";
import type { ProjectNode } from "../lib/types";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1;
  else failed += 1;
}

const testDir = dirname(fileURLToPath(import.meta.url));
const appSource = readFileSync(resolve(testDir, "../App.tsx"), "utf8");
const treeSource = readFileSync(resolve(testDir, "../components/ProjectTree.tsx"), "utf8");
const bridgeSource = readFileSync(resolve(testDir, "../lib/bridge.ts"), "utf8");
const typesSource = readFileSync(resolve(testDir, "../lib/types.ts"), "utf8");

process.stdout.write("\nApp Work Session integration contract\n");

ok(!appSource.includes('data-testid="work-sidebar-btn"'), "独立侧栏 Work 入口已移除");
ok(!appSource.includes("<WorkPage"), "App 不再挂载独立 Work 列表页");
ok(
  appSource.includes('activeTab?.sessionKind === "work"')
    && appSource.includes("activeTab?.workId")
    && appSource.includes("<WorkCard"),
  "WorkCard 由活动 Work Session 元数据驱动",
);
ok(
  appSource.includes("app.CreateWorkSession")
    && appSource.includes("crypto.randomUUID()")
    && appSource.includes("workCreateRequestsRef"),
  "新建意图使用唯一 requestId，失败重试复用同一幂等键",
);
ok(
  appSource.includes("await enqueueTabSwitch(result.tabMeta.id")
    && appSource.includes("await refreshProjectsAndTabs()"),
  "创建完成后切换到返回的 Work Session 并刷新树",
);
ok(
  /const topicbarCanRename = !sidebarImDetailConnection\s*&& activeTab\?\.sessionKind !== "work"\s*&& Boolean\(activeTab\?\.topicId\)/.test(appSource),
  "Work Session 标题由任务说明自动生成，不提供 TopicBar 手动重命名入口",
);
ok(
  treeSource.includes("onCreateWork")
    && treeSource.includes('className="project-tree__new-topic project-tree__new-work"')
    && treeSource.includes("<BriefcaseBusiness"),
  "Workspace 行在普通新建会话旁提供新建工作按钮",
);
ok(
  bridgeSource.includes("CreateWorkSession(input:")
    && typesSource.includes('sessionKind?: "normal" | "work"')
    && typesSource.includes("workRequestId?: string"),
  "Bridge 与前端类型包含 Work Session 创建和恢复字段",
);

const workNode: ProjectNode = {
  key: "work-session-1",
  kind: "work_session",
  label: "Work 1",
  root: "D:/repo",
  topicId: "topic-work",
  sessionPath: "D:/repo/session.jsonl",
  sessionKind: "work",
  workId: "work-1",
};
const openRequest = projectTreeTopicOpenRequest(workNode);
ok(
  openRequest.scope === "project"
    && openRequest.workspaceRoot === "D:/repo"
    && openRequest.topicId === "topic-work"
    && openRequest.sessionPath === "D:/repo/session.jsonl",
  "Work Session 节点沿用标准会话打开链路",
);

process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
