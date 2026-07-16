import type { Locale } from "../../lib/i18n";

const EN = [
  "All clear", "No reply needed", "Running smoothly", "No action needed",
  "On autopilot", "Waiting calmly", "Steady progress", "No intervention",
  "Everything looks good", "Safe to wait", "Running normally", "Quiet and steady",
  "Nothing to worry", "Cruise mode", "Ready and running", "Steady operation",
  "No need to watch", "Progress on track", "All systems good", "Status stable",
  "Moving forward", "Hands off for now", "Smooth operation", "On course",
  "No pending items", "Working automatically", "No attention needed", "Clear sailing",
  "Business as usual", "Quietly working", "Taking care of it", "Nothing to review",
  "Making progress", "All under control", "Working as planned", "No help needed",
  "Safe and sound", "Proceeding normally", "No input needed", "Everything steady",
] as const;

const ZH = [
  "一切正常", "无需回复", "一切顺利", "无需操作", "自动运行中", "静待完成",
  "平稳进行中", "无需干预", "一切安好", "安心等待", "正常运行", "风平浪静",
  "无需挂念", "自动巡航", "一切就绪", "安稳运行", "无需操心", "进展顺利",
  "一切良好", "状态平稳", "自动推进", "无需照看", "运行平稳", "一帆风顺",
  "安好勿念", "自动作业", "无需关注", "顺风顺水", "一切如常", "平静运行",
  "自动处理", "无需过问", "稳步推进", "安然无恙", "自动执行", "无需在意",
  "平安无事", "运行如常", "无需协助", "一切安稳",
] as const;

const ZH_TW = [
  "一切正常", "無需回覆", "一切順利", "無需操作", "自動執行中", "靜待完成",
  "穩定進行中", "無需介入", "一切安好", "安心等待", "正常執行", "風平浪靜",
  "無需掛念", "自動巡航", "一切就緒", "穩定執行", "無需操心", "進展順利",
  "一切良好", "狀態穩定", "自動推進", "無需照看", "執行穩定", "一帆風順",
  "安好勿念", "自動作業", "無需關注", "順風順水", "一切如常", "平靜執行",
  "自動處理", "無需過問", "穩步推進", "安然無恙", "自動執行", "無需在意",
  "平安無事", "執行如常", "無需協助", "一切安穩",
] as const;

export const WIDGET_SUFFIXES: Record<Locale, readonly string[]> = {
  en: EN,
  zh: ZH,
  "zh-TW": ZH_TW,
};

export function widgetSuffixes(locale: Locale): readonly string[] {
  return WIDGET_SUFFIXES[locale] ?? WIDGET_SUFFIXES.en;
}

export function pickWidgetSuffix(items: readonly string[], current = "", random: () => number = Math.random): string {
  if (items.length === 0) return "";
  const index = Math.min(items.length - 1, Math.floor(Math.max(0, random()) * items.length));
  if (items[index] !== current || items.length === 1) return items[index];
  return items[(index + 1) % items.length];
}
