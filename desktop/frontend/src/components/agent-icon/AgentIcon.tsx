// AgentIcon：按 manifest.layerOrder 纯渲染 5 层（frame → headwear → eyes →
// workspaceBadge → taskTool），DOM 顺序即叠加顺序。组件不计算身份、不查任务
// 映射、不推导状态 —— 全部来自 lib 层产出的 AgentIconViewModel；眼睛动画
// 由 Web Animations compositor 直接移动 sprite，不逐帧触发 React。缺资源隐藏并去重
// 上报（frame 缺失则整图标不渲染）；reduced-motion 由 animation 层处理。
import { memo, useCallback, useRef, useState } from "react";
import { Bookmark, Code2, Folder, SquareTerminal, Star, Zap } from "lucide-react";
import { useAgentEyeAnimation } from "../../lib/agentIcon/animation";
import { agentManifest, assetURL } from "../../lib/agentIcon/assets";
import type { AgentIconViewModel } from "../../lib/agentIcon/types";
import { isWorkspaceMatteIcon, type ProjectIconKey } from "../../lib/projectIcons";
import { WorkspaceMatteIcon } from "../widget/WorkspaceMatteIcon";
import "./agent-icon.css";

export interface AgentIconAssetMissing {
  sessionId: string;
  layer: string;
  path: string;
}

const reported = new Set<string>();

/** 同一资源去重上报：console.error 一次 + 可选的 onAssetMissing 回调。 */
function reportAssetMissing(info: AgentIconAssetMissing, onAssetMissing?: (info: AgentIconAssetMissing) => void): void {
  const key = `${info.layer}:${info.path}`;
  if (reported.has(key)) return;
  reported.add(key);
  console.error(`[agent-icon] missing asset layer=${info.layer} path=${info.path} session=${info.sessionId || "(unknown)"}`);
  onAssetMissing?.(info);
}

// BadgeGlyph 复用既有 project icon 数据：matte 图标走 WorkspaceMatteIcon，
// 经典五键走 Lucide，未知回退 folder（与小组件 WorkspaceGlyph 同一映射）。
function BadgeGlyph({ iconKey }: { iconKey: ProjectIconKey }) {
  if (isWorkspaceMatteIcon(iconKey)) return <WorkspaceMatteIcon icon={iconKey} className="agent-icon__badge-glyph-img" />;
  switch (iconKey) {
    case "star": return <Star aria-hidden="true" />;
    case "bookmark": return <Bookmark aria-hidden="true" />;
    case "code": return <Code2 aria-hidden="true" />;
    case "terminal": return <SquareTerminal aria-hidden="true" />;
    case "bolt": return <Zap aria-hidden="true" />;
    default: return <Folder aria-hidden="true" />;
  }
}

export const AgentIcon = memo(function AgentIcon({ viewModel, onAssetMissing }: { viewModel: AgentIconViewModel; onAssetMissing?: (info: AgentIconAssetMissing) => void }) {
  const eyeRef = useRef<HTMLImageElement>(null);
  // 组件内按层记录 onError 缺失；viewModel.missingLayers 是 manifest/契约层
  // 的静态缺失（同步滞后等），两者合并决定每层是否隐藏。
  const [errored, setErrored] = useState<ReadonlySet<string>>(() => new Set());
  const markMissing = useCallback((layer: string) => {
    setErrored((prev) => (prev.has(layer) ? prev : new Set(prev).add(layer)));
  }, []);

  const missingLayers = new Set(viewModel.missingLayers);
  for (const layer of errored) missingLayers.add(layer);

  const frameLayer = agentManifest.frames.find((entry) => entry.id === viewModel.frameId);
  const frameURL = frameLayer ? assetURL(frameLayer.png) : undefined;
  const frameMissing = !frameLayer || !frameURL || missingLayers.has("frame");

  const headwearLayer = viewModel.headwear.kind === "hat"
    ? agentManifest.hats.find((entry) => entry.id === viewModel.headwear.id)
    : agentManifest.hair.find((entry) => entry.id === viewModel.headwear.id);
  const headwearURL = headwearLayer ? assetURL(headwearLayer.png) : undefined;
  const headwearMissing = missingLayers.has("headwear") || !headwearURL;
  if (headwearMissing) {
    reportAssetMissing({ sessionId: viewModel.sessionId, layer: "headwear", path: headwearLayer?.png ?? viewModel.headwear.id }, onAssetMissing);
  }

  const eyeLayer = agentManifest.eyes.find((entry) => entry.id === viewModel.eyeStatus);
  const spriteURL = eyeLayer ? assetURL(eyeLayer.sprite) : undefined;
  const neutralURL = assetURL(agentManifest.templates.neutralLedGrid.png);
  const eyeMissing = missingLayers.has("eyes") || !spriteURL;
  if (eyeMissing) {
    reportAssetMissing({ sessionId: viewModel.sessionId, layer: "eyes", path: eyeLayer?.sprite ?? viewModel.eyeStatus }, onAssetMissing);
  }

  const toolLayer = agentManifest.tools.find((entry) => entry.id === viewModel.taskToolId);
  const toolURL = toolLayer ? assetURL(toolLayer.png) : undefined;
  const toolMissing = missingLayers.has("tool") || !toolURL;
  if (toolMissing) {
    reportAssetMissing({ sessionId: viewModel.sessionId, layer: "taskTool", path: toolLayer?.png ?? viewModel.taskToolId }, onAssetMissing);
  }

  const badgeTemplateURL = viewModel.workspaceBadge.templateUrl;
  const badgeMissing = missingLayers.has("template:workspaceBadge") || !badgeTemplateURL;
  if (badgeMissing) {
    reportAssetMissing({ sessionId: viewModel.sessionId, layer: "workspaceBadge", path: "templates/workspace-badge.png" }, onAssetMissing);
  }

  const framesPerEye = eyeLayer?.frames.length ?? 0;
  useAgentEyeAnimation(eyeRef, viewModel.sessionId, viewModel.eyeStatus, agentManifest, !frameMissing && !eyeMissing);

  // Hooks stay unconditional; frame 缺失 still removes the whole icon.
  if (frameMissing) {
    reportAssetMissing({ sessionId: viewModel.sessionId, layer: "frame", path: frameLayer?.png ?? viewModel.frameId }, onAssetMissing);
    return null;
  }

  return (
    <span className="agent-icon" aria-hidden="true" data-eye-status={viewModel.eyeStatus}>
      <img src={frameURL} alt="" className="agent-icon__layer" draggable={false} onError={() => { markMissing("frame"); reportAssetMissing({ sessionId: viewModel.sessionId, layer: "frame", path: frameLayer.png }, onAssetMissing); }} />
      {!headwearMissing && headwearURL ? (
        <img src={headwearURL} alt="" className="agent-icon__layer" draggable={false} onError={() => { markMissing("headwear"); reportAssetMissing({ sessionId: viewModel.sessionId, layer: "headwear", path: headwearLayer?.png ?? viewModel.headwear.id }, onAssetMissing); }} />
      ) : null}
      <span className={`agent-icon__layer agent-icon__layer--eyes${eyeMissing ? " agent-icon__layer--missing" : ""}`}>
        {eyeMissing
          ? neutralURL ? <img src={neutralURL} alt="" className="agent-icon__eyes-fallback" draggable={false} /> : null
          : <img
              ref={eyeRef}
              src={spriteURL}
              alt=""
              className="agent-icon__eyes"
              draggable={false}
              style={{ width: `${framesPerEye * 100}%`, transform: "translateX(0%)" }}
              onError={() => { markMissing("eyes"); reportAssetMissing({ sessionId: viewModel.sessionId, layer: "eyes", path: eyeLayer?.sprite ?? viewModel.eyeStatus }, onAssetMissing); }}
            />}
      </span>
      {!badgeMissing && badgeTemplateURL ? (
        <span className="agent-icon__layer agent-icon__layer--badge">
          <img src={badgeTemplateURL} alt="" className="agent-icon__badge-template" draggable={false} onError={() => { markMissing("template:workspaceBadge"); reportAssetMissing({ sessionId: viewModel.sessionId, layer: "workspaceBadge", path: "templates/workspace-badge.png" }, onAssetMissing); }} />
          <span className="agent-icon__badge-glyph"><BadgeGlyph iconKey={viewModel.workspaceBadge.iconKey} /></span>
        </span>
      ) : null}
      {!toolMissing && toolURL ? (
        <img src={toolURL} alt="" className="agent-icon__layer" draggable={false} onError={() => { markMissing("tool"); reportAssetMissing({ sessionId: viewModel.sessionId, layer: "taskTool", path: toolLayer?.png ?? viewModel.taskToolId }, onAssetMissing); }} />
      ) : null}
    </span>
  );
});
