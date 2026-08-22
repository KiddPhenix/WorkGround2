// ViewModel 组装：把小组件快照条目投影成 AgentIcon 组件消费的规范化
// ViewModel。所有推导（身份/任务/状态/徽标）收敛在这里，组件只读 ViewModel
// + assets。纯函数、幂等、可重放。
import type { DesktopIconItem } from "../bridge";
import { projectIconKey } from "../projectIcons";
import { agentManifest, assetURL } from "./assets";
import { identitySeedKey, pickIdentity } from "./identity";
import { eyeStatusFor } from "./state";
import { toolForTask } from "./task";
import type { AgentIconViewModel } from "./types";

/**
 * 条目是否渲染 Agent Icon：真实 task/session（kind === "task" 且非
 * QuickStart 乐观条目），以及后端已绑定真实 session 身份的 IM person。
 * 未解析的 IM 行保留通用 Users 图标，避免按会话标题猜身份。
 */
export function isAgentIconItem(item: DesktopIconItem): boolean {
  // "opt:" 前缀与 widgetQuickStartJobs.QUICK_JOB_OPTIMISTIC_PREFIX 契约一致
  // （契约测试双向断言，防止漂移）。
  if (item.kind === "task") return !item.id.startsWith("opt:");
  return item.kind === "person" && Boolean(item.sessionId?.trim() || item.sessionRef?.sessionPath?.trim() || item.sessionRef?.topicId?.trim());
}

export function buildAgentIconViewModel(item: DesktopIconItem): AgentIconViewModel {
  const missingLayers = new Set<string>();
  const sessionId = identitySeedKey(item);
  const identity = pickIdentity(sessionId, agentManifest);
  for (const layer of identity.missingLayers) missingLayers.add(layer);
  const { toolId, missingLayers: toolMissing } = toolForTask(item, agentManifest);
  for (const layer of toolMissing) missingLayers.add(layer);
  const { eyeStatus, reason } = eyeStatusFor(item);
  const templateUrl = assetURL(agentManifest.templates.workspaceBadge.png) ?? "";
  if (!templateUrl) missingLayers.add("template:workspaceBadge");
  return {
    sessionId,
    frameId: identity.frameId,
    headwear: identity.headwear,
    workspaceBadge: {
      templateUrl,
      iconKey: projectIconKey(item.workspaceIcon),
      stableKey: item.sessionRef?.workspaceRoot ?? "",
    },
    taskToolId: toolId,
    eyeStatus,
    statusReason: reason,
    missingLayers: Array.from(missingLayers),
  };
}
