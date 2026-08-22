// 任务工具映射：业务任务枚举 → manifest tool id 的单一入口。小组件快照
// 目前没有业务任务枚举信号，因此一律使用 manifest 的 general 兜底 —— 禁止
// 从标题/文案猜测工具（开发文档 §5.4：未知任务一律回退 general）。纯函数。
import type { AgentManifest } from "./types";

/** 小组件任务图标使用的工具 id：无业务枚举时固定 general。 */
export const WIDGET_TASK_TOOL_ID = "general";

/**
 * 解析最终 tool id：desired 未知/缺失 → general；general 也不存在（旧
 * manifest）→ 第一个工具。任何回退都显式进 missingLayers，不静默。
 */
export function resolveToolId(desired: string | undefined, manifest: AgentManifest): { toolId: string; missingLayers: string[] } {
  const missingLayers: string[] = [];
  if (desired && manifest.tools.some((tool) => tool.id === desired)) {
    return { toolId: desired, missingLayers };
  }
  if (desired) missingLayers.push(`tool:${desired}`);
  if (manifest.tools.some((tool) => tool.id === WIDGET_TASK_TOOL_ID)) {
    return { toolId: WIDGET_TASK_TOOL_ID, missingLayers };
  }
  missingLayers.push(`tool:${WIDGET_TASK_TOOL_ID}`);
  const first = manifest.tools[0];
  return { toolId: first?.id ?? "", missingLayers };
}

/** 小组件任务 → 工具 id。当前无业务枚举，恒定 general（见 WIDGET_TASK_TOOL_ID）。 */
export function toolForTask(_item: unknown, manifest: AgentManifest): { toolId: string; missingLayers: string[] } {
  return resolveToolId(WIDGET_TASK_TOOL_ID, manifest);
}
