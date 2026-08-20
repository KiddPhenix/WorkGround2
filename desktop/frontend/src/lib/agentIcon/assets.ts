// Agent Icon 资源装载：manifest 静态 import 一次（模块级单例），PNG 以 Vite
// 静态 import 建立 manifest 相对路径 → 打包后 URL 的只读表。运行时只按
// manifest 的 png/sprite 路径查表，不打包 SVG，不拼接磁盘路径，
// 不做 per-render 动态 import。
// 缺资源由 AgentIcon 组件经 onError 显式上报（同一资源去重）。
import manifestJson from "../../assets/agent-icon/manifest.json";
import frameAmber from "../../assets/agent-icon/png/frames/amber.png";
import frameCobalt from "../../assets/agent-icon/png/frames/cobalt.png";
import frameCoral from "../../assets/agent-icon/png/frames/coral.png";
import frameCyan from "../../assets/agent-icon/png/frames/cyan.png";
import frameGreen from "../../assets/agent-icon/png/frames/green.png";
import frameLime from "../../assets/agent-icon/png/frames/lime.png";
import frameOrange from "../../assets/agent-icon/png/frames/orange.png";
import frameTeal from "../../assets/agent-icon/png/frames/teal.png";
import frameViolet from "../../assets/agent-icon/png/frames/violet.png";
import hatBaseballCap from "../../assets/agent-icon/png/hats/baseball-cap.png";
import hatBeanie from "../../assets/agent-icon/png/hats/beanie.png";
import hatBeret from "../../assets/agent-icon/png/hats/beret.png";
import hatBowler from "../../assets/agent-icon/png/hats/bowler.png";
import hatBucketHat from "../../assets/agent-icon/png/hats/bucket-hat.png";
import hatCowboyHat from "../../assets/agent-icon/png/hats/cowboy-hat.png";
import hatCrown from "../../assets/agent-icon/png/hats/crown.png";
import hatFedora from "../../assets/agent-icon/png/hats/fedora.png";
import hatFlatCap from "../../assets/agent-icon/png/hats/flat-cap.png";
import hatNewsboyCap from "../../assets/agent-icon/png/hats/newsboy-cap.png";
import hatPartyHat from "../../assets/agent-icon/png/hats/party-hat.png";
import hatSailorCap from "../../assets/agent-icon/png/hats/sailor-cap.png";
import hatSunHat from "../../assets/agent-icon/png/hats/sun-hat.png";
import hatTopHat from "../../assets/agent-icon/png/hats/top-hat.png";
import hatWizardHat from "../../assets/agent-icon/png/hats/wizard-hat.png";
import hairAfroPuff from "../../assets/agent-icon/png/hair/afro-puff.png";
import hairBowlCut from "../../assets/agent-icon/png/hair/bowl-cut.png";
import hairCenterPart from "../../assets/agent-icon/png/hair/center-part.png";
import hairLightningFringe from "../../assets/agent-icon/png/hair/lightning-fringe.png";
import hairMessySpikes from "../../assets/agent-icon/png/hair/messy-spikes.png";
import hairMohawk from "../../assets/agent-icon/png/hair/mohawk.png";
import hairPompadour from "../../assets/agent-icon/png/hair/pompadour.png";
import hairQuiff from "../../assets/agent-icon/png/hair/quiff.png";
import hairShortCrop from "../../assets/agent-icon/png/hair/short-crop.png";
import hairSideSwept from "../../assets/agent-icon/png/hair/side-swept.png";
import hairSingleCurl from "../../assets/agent-icon/png/hair/single-curl.png";
import hairSlickBack from "../../assets/agent-icon/png/hair/slick-back.png";
import hairTopKnot from "../../assets/agent-icon/png/hair/top-knot.png";
import hairTwinTufts from "../../assets/agent-icon/png/hair/twin-tufts.png";
import hairWave from "../../assets/agent-icon/png/hair/wave.png";
import toolAutomation from "../../assets/agent-icon/png/tools/automation.png";
import toolBrowser from "../../assets/agent-icon/png/tools/browser.png";
import toolBuild from "../../assets/agent-icon/png/tools/build.png";
import toolChat from "../../assets/agent-icon/png/tools/chat.png";
import toolCode from "../../assets/agent-icon/png/tools/code.png";
import toolConfig from "../../assets/agent-icon/png/tools/config.png";
import toolData from "../../assets/agent-icon/png/tools/data.png";
import toolDatabase from "../../assets/agent-icon/png/tools/database.png";
import toolDebug from "../../assets/agent-icon/png/tools/debug.png";
import toolDeploy from "../../assets/agent-icon/png/tools/deploy.png";
import toolDesign from "../../assets/agent-icon/png/tools/design.png";
import toolDocs from "../../assets/agent-icon/png/tools/docs.png";
import toolFiles from "../../assets/agent-icon/png/tools/files.png";
import toolGeneral from "../../assets/agent-icon/png/tools/general.png";
import toolGit from "../../assets/agent-icon/png/tools/git.png";
import toolImage from "../../assets/agent-icon/png/tools/image.png";
import toolMonitor from "../../assets/agent-icon/png/tools/monitor.png";
import toolPlan from "../../assets/agent-icon/png/tools/plan.png";
import toolResearch from "../../assets/agent-icon/png/tools/research.png";
import toolReview from "../../assets/agent-icon/png/tools/review.png";
import toolSecurity from "../../assets/agent-icon/png/tools/security.png";
import toolTerminal from "../../assets/agent-icon/png/tools/terminal.png";
import toolTest from "../../assets/agent-icon/png/tools/test.png";
import toolWriting from "../../assets/agent-icon/png/tools/writing.png";
import templateWorkspaceBadge from "../../assets/agent-icon/png/templates/workspace-badge.png";
import templateNeutralLedGrid from "../../assets/agent-icon/png/templates/neutral-led-grid.png";
import spriteCleanup from "../../assets/agent-icon/sprites/eyes/cleanup.png";
import spriteFailure from "../../assets/agent-icon/sprites/eyes/failure.png";
import spriteProblem from "../../assets/agent-icon/sprites/eyes/problem.png";
import spriteRunning from "../../assets/agent-icon/sprites/eyes/running.png";
import spriteSuccess from "../../assets/agent-icon/sprites/eyes/success.png";
import type { AgentManifest } from "./types";

// manifest 是唯一真源：运行时永远从这里读计数/fps/路径，代码里不出现
// 9/15/24/6 这类魔法数字副本（数量断言只存在于同步脚本与契约测试）。
export const agentManifest = manifestJson as unknown as AgentManifest;

// manifest 相对路径 → 打包 URL。键必须与 manifest 的 png/sprite 字段一致；
// 契约测试断言该表覆盖 manifest 中运行时用到的全部路径。
const assetTable: Record<string, string> = {
  "png/frames/amber.png": frameAmber,
  "png/frames/cobalt.png": frameCobalt,
  "png/frames/coral.png": frameCoral,
  "png/frames/cyan.png": frameCyan,
  "png/frames/green.png": frameGreen,
  "png/frames/lime.png": frameLime,
  "png/frames/orange.png": frameOrange,
  "png/frames/teal.png": frameTeal,
  "png/frames/violet.png": frameViolet,
  "png/hats/baseball-cap.png": hatBaseballCap,
  "png/hats/beanie.png": hatBeanie,
  "png/hats/beret.png": hatBeret,
  "png/hats/bowler.png": hatBowler,
  "png/hats/bucket-hat.png": hatBucketHat,
  "png/hats/cowboy-hat.png": hatCowboyHat,
  "png/hats/crown.png": hatCrown,
  "png/hats/fedora.png": hatFedora,
  "png/hats/flat-cap.png": hatFlatCap,
  "png/hats/newsboy-cap.png": hatNewsboyCap,
  "png/hats/party-hat.png": hatPartyHat,
  "png/hats/sailor-cap.png": hatSailorCap,
  "png/hats/sun-hat.png": hatSunHat,
  "png/hats/top-hat.png": hatTopHat,
  "png/hats/wizard-hat.png": hatWizardHat,
  "png/hair/afro-puff.png": hairAfroPuff,
  "png/hair/bowl-cut.png": hairBowlCut,
  "png/hair/center-part.png": hairCenterPart,
  "png/hair/lightning-fringe.png": hairLightningFringe,
  "png/hair/messy-spikes.png": hairMessySpikes,
  "png/hair/mohawk.png": hairMohawk,
  "png/hair/pompadour.png": hairPompadour,
  "png/hair/quiff.png": hairQuiff,
  "png/hair/short-crop.png": hairShortCrop,
  "png/hair/side-swept.png": hairSideSwept,
  "png/hair/single-curl.png": hairSingleCurl,
  "png/hair/slick-back.png": hairSlickBack,
  "png/hair/top-knot.png": hairTopKnot,
  "png/hair/twin-tufts.png": hairTwinTufts,
  "png/hair/wave.png": hairWave,
  "png/tools/automation.png": toolAutomation,
  "png/tools/browser.png": toolBrowser,
  "png/tools/build.png": toolBuild,
  "png/tools/chat.png": toolChat,
  "png/tools/code.png": toolCode,
  "png/tools/config.png": toolConfig,
  "png/tools/data.png": toolData,
  "png/tools/database.png": toolDatabase,
  "png/tools/debug.png": toolDebug,
  "png/tools/deploy.png": toolDeploy,
  "png/tools/design.png": toolDesign,
  "png/tools/docs.png": toolDocs,
  "png/tools/files.png": toolFiles,
  "png/tools/general.png": toolGeneral,
  "png/tools/git.png": toolGit,
  "png/tools/image.png": toolImage,
  "png/tools/monitor.png": toolMonitor,
  "png/tools/plan.png": toolPlan,
  "png/tools/research.png": toolResearch,
  "png/tools/review.png": toolReview,
  "png/tools/security.png": toolSecurity,
  "png/tools/terminal.png": toolTerminal,
  "png/tools/test.png": toolTest,
  "png/tools/writing.png": toolWriting,
  "png/templates/workspace-badge.png": templateWorkspaceBadge,
  "png/templates/neutral-led-grid.png": templateNeutralLedGrid,
  "sprites/eyes/cleanup.png": spriteCleanup,
  "sprites/eyes/failure.png": spriteFailure,
  "sprites/eyes/problem.png": spriteProblem,
  "sprites/eyes/running.png": spriteRunning,
  "sprites/eyes/success.png": spriteSuccess,
};

/** manifest 相对路径 → 打包 URL；未知路径返回 undefined（调用方降级）。 */
export function assetURL(manifestPath: string): string | undefined {
  return assetTable[manifestPath];
}

/** 运行时使用的 manifest 路径集合（不含逐帧 PNG：眼睛走 sprite）。 */
export function runtimeAssetPaths(manifest: AgentManifest): string[] {
  const paths: string[] = [];
  for (const frame of manifest.frames) paths.push(frame.png);
  for (const hat of manifest.hats) paths.push(hat.png);
  for (const hair of manifest.hair) paths.push(hair.png);
  for (const tool of manifest.tools) paths.push(tool.png);
  for (const eye of manifest.eyes) paths.push(eye.sprite);
  paths.push(manifest.templates.workspaceBadge.png, manifest.templates.neutralLedGrid.png);
  return paths;
}
