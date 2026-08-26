import type { Locale } from "../../../lib/i18n";
import type { AssistantPolicy, AssistantRoutine, AssistantSchedule } from "./assistant.types";

export type AssistantTemplateID = "code" | "general" | "promo";

export interface AssistantTemplateContent {
  id: AssistantTemplateID;
  available: boolean;
  defaultName: string;
  mission: string;
  policy: AssistantPolicy;
  routines: Array<{ title: string; prompt: string; schedule: AssistantSchedule }>;
}

function clock(kind: AssistantSchedule["kind"], at: string): AssistantSchedule {
  const schedule: AssistantSchedule = { kind, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", at };
  if (kind === "weekly" || kind === "biweekly") schedule.weekday = 0; // Sunday
  return schedule;
}

function readOnlyPolicy(): AssistantPolicy {
  return {
    local_write: "deny",
    network: "deny",
    publish: "approve",
    delete: "approve",
    payment: "approve",
    secrets: "approve",
    private_data: "approve",
  };
}

function codePolicy(): AssistantPolicy {
  return { ...readOnlyPolicy(), local_write: "allow" };
}

interface TemplateText {
  codeName: string;
  codeMission: string;
  healthTitle: string;
  healthPrompt: string;
  buildTitle: string;
  buildPrompt: string;
  releaseTitle: string;
  releasePrompt: string;
  generalName: string;
  promoName: string;
  promoMission: string;
  contentTitle: string;
  contentPrompt: string;
  metricsTitle: string;
  metricsPrompt: string;
  replyTitle: string;
  replyPrompt: string;
}

const texts: Record<Locale, TemplateText> = {
  zh: {
    codeName: "代码项目助手",
    codeMission: "持续关注项目健康度和发布准备情况，在发布条件满足时询问我。",
    healthTitle: "项目健康检查",
    healthPrompt: "扫描项目近期修改、测试与构建产物，报告当前健康度和风险点。",
    buildTitle: "测试与构建",
    buildPrompt: "运行测试与构建，记录本次失败原因、已验证步骤和项目特有约束。",
    releaseTitle: "发布准备检查",
    releasePrompt: "检查发布条件是否满足；满足时进入待处理状态并询问是否发布，绝不自行发布。",
    generalName: "通用助手",
    promoName: "推广助手",
    promoMission: "持续在已配置社区推广产品，回复用户问题，并根据真实效果改进下一轮策略。",
    contentTitle: "内容规划与发布",
    contentPrompt: "结合使命、显式记忆和最近渠道指标，准备最适合当前社区的内容；需要发布时调用渠道发布工具，由冻结的对外发布权限决定自动执行、审批或拒绝。",
    metricsTitle: "效果复盘",
    metricsPrompt: "读取已采集的浏览、点赞和回复增量，比较不同内容效果，用 metrics 与 strategy 显式记忆记录结论并调整下一轮方案。",
    replyTitle: "社区回复",
    replyPrompt: "检查需要回复的社区讨论，形成有帮助且不过度营销的回复；外发时调用渠道回复工具，由冻结的对外发布权限决定自动执行、审批或拒绝。",
  },
  en: {
    codeName: "Code project assistant",
    codeMission: "Track project health and release readiness, and ask me before publishing.",
    healthTitle: "Project health check",
    healthPrompt: "Scan recent changes, tests and build artifacts, and report current health and risks.",
    buildTitle: "Test & build",
    buildPrompt: "Run tests and the build, and record failures, verified steps and project constraints.",
    releaseTitle: "Release readiness",
    releasePrompt: "Check whether release conditions are met; when they are, ask me before publishing — never publish on its own.",
    generalName: "General assistant",
    promoName: "Promotion assistant", promoMission: "Continuously promote the product in configured communities, answer questions, and improve the next strategy from real results.",
    contentTitle: "Plan & publish", contentPrompt: "Use the mission, explicit memory, and recent channel metrics to prepare community-specific content. Use the channel publishing tool; the frozen publishing permission decides whether it runs automatically, asks, or is denied.",
    metricsTitle: "Review results", metricsPrompt: "Compare collected view, like, and reply deltas. Record conclusions in metrics and strategy memory, then adjust the next approach.",
    replyTitle: "Community replies", replyPrompt: "Review community discussions that need an answer and prepare a useful, non-spammy reply. Use the channel reply tool; the frozen publishing permission decides whether it runs automatically, asks, or is denied.",
  },
  "zh-TW": {
    codeName: "程式碼專案助手",
    codeMission: "持續關注專案健康度與發佈準備情況，在發佈條件滿足時詢問我。",
    healthTitle: "專案健康檢查",
    healthPrompt: "掃描專案近期修改、測試與建置產物，報告目前健康度與風險點。",
    buildTitle: "測試與建置",
    buildPrompt: "執行測試與建置，記錄本次失敗原因、已驗證步驟與專案特有約束。",
    releaseTitle: "發佈準備檢查",
    releasePrompt: "檢查發佈條件是否滿足；滿足時進入待處理狀態並詢問是否發佈，絕不自行發佈。",
    generalName: "通用助手",
    promoName: "推廣助手", promoMission: "持續在已設定社群推廣產品、回覆問題，並依真實效果改善下一輪策略。",
    contentTitle: "內容規劃與發佈", contentPrompt: "結合使命、明確記憶與最近渠道指標準備社群內容；發佈時使用渠道工具，由凍結的對外發佈權限決定自動執行、審批或拒絕。",
    metricsTitle: "效果複盤", metricsPrompt: "比較瀏覽、按讚與回覆增量，以 metrics 與 strategy 記憶記錄結論並調整下一輪。",
    replyTitle: "社群回覆", replyPrompt: "檢查需要回覆的討論，準備有幫助且不過度行銷的回覆；外發時使用渠道工具，由凍結的對外發佈權限決定自動執行、審批或拒絕。",
  },
};

export function assistantTemplateContent(locale: Locale): AssistantTemplateContent[] {
  const t = texts[locale] ?? texts.zh;
  return [
    {
      id: "code",
      available: true,
      defaultName: t.codeName,
      mission: t.codeMission,
      policy: codePolicy(),
      routines: [
        { title: t.healthTitle, prompt: t.healthPrompt, schedule: clock("daily", "09:00") },
        { title: t.buildTitle, prompt: t.buildPrompt, schedule: clock("daily", "18:00") },
        { title: t.releaseTitle, prompt: t.releasePrompt, schedule: clock("weekly", "10:00") },
      ],
    },
    {
      id: "general",
      available: true,
      defaultName: t.generalName,
      mission: "",
      policy: readOnlyPolicy(),
      routines: [],
    },
    {
      id: "promo",
      available: true,
      defaultName: t.promoName,
      mission: t.promoMission,
      policy: { ...readOnlyPolicy(), network: "allow" },
      routines: [
        { title: t.contentTitle, prompt: t.contentPrompt, schedule: clock("daily", "10:00") },
        { title: t.metricsTitle, prompt: t.metricsPrompt, schedule: clock("daily", "18:00") },
        { title: t.replyTitle, prompt: t.replyPrompt, schedule: clock("daily", "15:00") },
      ],
    },
  ];
}

export function assistantTemplate(id: AssistantTemplateID, locale: Locale): AssistantTemplateContent {
  const list = assistantTemplateContent(locale);
  return list.find((item) => item.id === id) ?? list[1];
}

export function templateRoutine(
  assistantID: string,
  routineID: string,
  title: string,
  prompt: string,
  schedule: AssistantSchedule,
  createdAt: string,
): AssistantRoutine {
  return {
    id: routineID,
    assistant_id: assistantID,
    title,
    prompt,
    schedule,
    enabled: true,
    catch_up: "coalesce_latest",
    revision: 0,
    created_at: createdAt,
    updated_at: createdAt,
  };
}

export function templateRoutines(assistantID: string, template: AssistantTemplateContent, createdAt: string): AssistantRoutine[] {
  return template.routines.map((routine, index) => ({
    id: `${assistantID}-routine-${index + 1}`,
    assistant_id: assistantID,
    title: routine.title,
    prompt: routine.prompt,
    schedule: routine.schedule,
    enabled: true,
    catch_up: "coalesce_latest" as const,
    revision: 0,
    created_at: createdAt,
    updated_at: createdAt,
  }));
}
