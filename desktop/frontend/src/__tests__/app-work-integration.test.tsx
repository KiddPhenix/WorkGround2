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
const availabilitySource = readFileSync(resolve(testDir, "../components/work/WorkAvailabilitySurface.tsx"), "utf8");
const workCardSource = readFileSync(resolve(testDir, "../components/work/WorkCard.tsx"), "utf8");
const linkedSessionSource = readFileSync(resolve(testDir, "../components/work/LinkedSessionCard.tsx"), "utf8");

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
  appSource.includes("setWorkBootstrap(bootstrap)")
    && appSource.includes("void beginWorkInitialization(bootstrap)")
    && appSource.includes("<WorkStartSurface"),
  "新建 Work 先挂载输入首屏，再把初始化作为后台任务派发",
);
ok(
  appSource.includes("workInitTaskRef")
    && appSource.includes("running?.requestId === bootstrap.requestId")
    && appSource.includes("return running.promise"),
  "重复开始复用同一 requestId 的后台初始化任务",
);
ok(
  appSource.includes("let workID = current.result?.tabMeta?.workId")
    && appSource.includes("workID = result?.tabMeta?.workId")
    && !appSource.includes("const result = workBootstrapRef.current?.result"),
  "准备中单击开始直接消费后台任务返回结果，不等待 React 状态回写后再次点击",
);
ok(
  appSource.includes("ui.setDraft(workID, \"back\", prompt)")
    && appSource.includes("startIntent={workStartIntent?.workID === activeTab.workId ? workStartIntent : undefined}"),
  "单击开始把输入交给 WorkCard 现有生成应用流程",
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
ok(
  linkedSessionSource.includes("void handleNavigate()")
    && linkedSessionSource.includes("autoTargetRef.current === target"),
  "点击任务信息翻到背面后自动且幂等地打开关联会话",
);
ok(
  appSource.includes('activeTab?.sessionKind === "work" && activeTab.topicId')
    && appSource.includes('handleOpenTopic(activeTab.scope, activeTab.workspaceRoot || "", activeTab.topicId, sessionRef.sessionPath)'),
  "隐藏的 Task Session 复用所属 Work tab/topic 打开，不依赖普通会话列表",
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

// ── Half-completed Work Session recovery contract ────────────────────
process.stdout.write("\nHalf-completed Work Session detection & retry\n");

// WorkAvailabilitySurface supports 'incomplete' as a third state
ok(
  availabilitySource.includes("state: 'initializing' | 'unavailable' | 'incomplete'"),
  "WorkAvailabilitySurface 支持 incomplete 状态",
);

// The incomplete state shows a retry button (blocked = unavailable || incomplete)
ok(
  availabilitySource.includes("blocked") && availabilitySource.includes("state === 'unavailable' || state === 'incomplete'"),
  "incomplete 状态与 unavailable 共用 retry 按钮逻辑",
);

// App.tsx detects half-completed Work Sessions: showReadyWork && !workId && workRequestId → incomplete
ok(
  appSource.includes('"incomplete"')
    && appSource.includes("showReadyWork")
    && appSource.includes("activeTab?.workRequestId"),
  "App 检测半完成 Work Session：showReadyWork 但缺 workId 且有 workRequestId → incomplete",
);

// handleRetryActiveWork already handles the half-completed case: reuses workRequestId
ok(
  appSource.includes('activeTab?.sessionKind === "work" && !activeTab.workId && activeTab.workRequestId')
    && appSource.includes("workCreateRequestsRef.current.set(requestKey, activeTab.workRequestId)"),
  "handleRetryActiveWork 对半完成 Work Session 复用原 workRequestId 重试",
);

// onRetry passes handleRetryActiveWork when workEnabled !== false (covers incomplete case)
ok(
  appSource.includes("workEnabled === false ? undefined : handleRetryActiveWork"),
  "incomplete 状态下 onRetry 正确传递 handleRetryActiveWork",
);

// TabMeta includes both workId and workRequestId for recovery detection
ok(
  typesSource.includes("workRequestId?: string"),
  "TabMeta 包含 workRequestId 字段用于恢复判定",
);

// Locale keys for incomplete state exist in zh.ts
const zhSource = readFileSync(resolve(testDir, "../locales/zh.ts"), "utf8");
ok(
  zhSource.includes('"work.incomplete":')
    && zhSource.includes('"work.incompleteDetail":'),
  "zh.ts 包含 work.incomplete 和 work.incompleteDetail 文案",
);

// ── 已有 Work 立即挂载：不依赖 workCapable ─────────────────────────
process.stdout.write("\nExisting Work immediate mount (bypasses capability gate)\n");

// 已有 workId 时直接挂载 WorkCard，传入 ready 属性控制后台同步
ok(
  appSource.includes('showWorkSurface && activeTab?.workId && !workUnavailable ?')
    && appSource.includes('ready={workEnabled === true && !workConfigFailed && workCapable === true}'),
  "已有 Work Session 在能力探测期间立即挂载 WorkCard，ready 控制后台同步",
);
ok(
  appSource.includes("const workUnavailable = workEnabled === false || workConfigFailed || workCapabilityFailed")
    && appSource.includes("workUnavailable"),
  "配置或能力确认失败后仍进入显式可重试错误态",
);

// WorkCardProps 声明 ready 属性
ok(
  workCardSource.includes("ready?: boolean")
    && workCardSource.includes("defers subscription and snapshot until ready becomes true"),
  "WorkCardProps 包含 ready?: boolean 并说明其延迟订阅语义",
);

// WorkCard 在 ready=false 且无缓存投影时显示后台同步中状态
ok(
  workCardSource.includes("work-card-pending")
    && workCardSource.includes('role="status"')
    && workCardSource.includes("后台同步中"),
  "ready=false 无缓存时 WorkCard 显示轻量后台同步状态而非 Work 不可用",
);

// WorkCard 在 ready=false 时保持只读
ok(
  workCardSource.includes("const readonly = !ready || !view || view.work.archiveState !== 'active'"),
  "ready=false 时所有写操作保持只读/禁用",
);

// Subscription 依赖 ready，ready 变为 true 时自动开始
ok(
  workCardSource.includes("if (!ready)")
    && workCardSource.includes("adapter.subscribe(workID)")
    && workCardSource.includes("[adapter, ensureCard, ready, workID]"),
  "ready 为 false 时跳过 subscribe；ready→true 时 effect 自动开始订阅",
);

process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
