// 状态归一化：复用现有 DesktopIconStatus 单源（后端快照已归一化），映射到
// Agent 眼睛状态。缺失/未知状态按文档必须显示 problem 眼并暴露原因，不能
// 静默留空。纯函数，幂等。
import type { DesktopIconItem } from "../bridge";
import type { AgentEyeStatus } from "./types";

export interface AgentEyeStatusResult {
  eyeStatus: AgentEyeStatus;
  reason?: string;
}

/**
 * DesktopIconStatus → AgentEyeStatus（Outcome 指定的映射）：
 * thinking/running → running；needs_input/needs_confirm → problem；
 * done → success；failed → failure；缺失/未知 → problem + 原因。
 */
export function eyeStatusFor(item: DesktopIconItem): AgentEyeStatusResult {
  switch (item.status) {
    case "thinking":
    case "running":
      return { eyeStatus: "running" };
    case "needs_input":
      return { eyeStatus: "problem", reason: "等待输入" };
    case "needs_confirm":
      return { eyeStatus: "problem", reason: "等待确认" };
    case "done":
      return { eyeStatus: "success" };
    case "failed":
      return { eyeStatus: "failure", reason: item.notifications[0]?.body ? "任务失败" : undefined };
    default:
      return { eyeStatus: "problem", reason: `未知状态：${item.status || "缺失"}` };
  }
}
