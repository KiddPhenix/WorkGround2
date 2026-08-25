import type { Locale } from "../../../lib/i18n";
import type { AssistantPolicy, AssistantRoutine, AssistantSchedule } from "./assistant.types";

export type AssistantTemplateID = "code" | "general" | "promo";

export interface AssistantTemplateContent {
  id: AssistantTemplateID;
  available: boolean; // false = phase-4 preview, not selectable
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
      available: false,
      defaultName: "",
      mission: "",
      policy: readOnlyPolicy(),
      routines: [],
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
