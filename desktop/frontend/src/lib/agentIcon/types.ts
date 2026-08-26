// Agent Icon types: the runtime PNG-only projection of
// docs/Agent Icon/assets/manifest.json and the normalized ViewModel consumed by
// AgentIcon. SVG metadata intentionally stays outside the runtime contract, so
// display code cannot accidentally switch the robot layers back to SVG.
import type { ProjectIconKey } from "../projectIcons";

export type AgentEyeStatus = "running" | "problem" | "success" | "failure" | "cleanup";

export interface AgentManifestLayer {
  id: string;
  label: string;
  png: string;
}

export interface AgentManifestFrame extends AgentManifestLayer {
  color: string;
}

export interface AgentManifestTool extends AgentManifestLayer {
  accent: string;
}

export interface AgentManifestEyeFrame {
  png: string;
}

export interface AgentManifestEye {
  id: AgentEyeStatus;
  label: string;
  color: string;
  fps: number;
  loop: boolean;
  holdLast: boolean;
  frameWidth: number;
  frameHeight: number;
  frames: AgentManifestEyeFrame[];
  sprite: string;
}

export interface AgentManifest {
  version: number;
  canvas: { width: number; height: number; viewBox: string };
  layerOrder: ["frame", "headwear", "eyes", "workspaceBadge", "taskTool"];
  identityRule: { headwear: string; stableSeed: string; semantic: boolean };
  hats: AgentManifestLayer[];
  hair: AgentManifestLayer[];
  frames: AgentManifestFrame[];
  tools: AgentManifestTool[];
  eyes: AgentManifestEye[];
  templates: { workspaceBadge: AgentManifestLayer; neutralLedGrid: AgentManifestLayer };
}

// 帽子/头发严格二选一，由 identity.ts 的同一槽位保证。
export type AgentHeadwear = { kind: "hat" | "hair"; id: string };

export interface AgentWorkspaceBadgeViewModel {
  // 已解析的资源 URL（workspace-badge 模板）。
  templateUrl: string;
  // 规范化后的项目图标键；未知回退 ""（渲染侧按 folder 中性回退）。
  iconKey: ProjectIconKey;
  // workspaceRoot；缺显式配置时用于稳定回退，绝不按打开顺序选择。
  stableKey: string;
}

// AgentIcon 组件的完整输入。动画帧不属于静态 ViewModel：Web Animations
// compositor 直接消费 manifest，并监听 prefers-reduced-motion。
export interface AgentIconViewModel {
  /** 稳定身份 seed（sessionId；空时回退 sessionPath → topicId → item.id）。 */
  sessionId: string;
  frameId: string;
  headwear: AgentHeadwear;
  workspaceBadge: AgentWorkspaceBadgeViewModel;
  taskToolId: string;
  eyeStatus: AgentEyeStatus;
  /** problem/failure 的可查看原因（诊断/上报用，不进入 aria-label）。 */
  statusReason?: string;
  /** 缺失的层 id（如 "frame:violet"、"tool:general"），组件据此降级并去重上报。 */
  missingLayers: string[];
}
