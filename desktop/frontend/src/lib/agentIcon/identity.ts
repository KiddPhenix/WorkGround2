// Agent Icon 稳定身份：FNV-1a 32-bit hash + 确定性选择。禁止
// Math.random / Date.now / crypto.getRandomValues —— 同一 seed 永远得到同一
// 结果，重启、重连、live→retained 不漂移。纯函数，无副作用。
import type { DesktopIconItem } from "../bridge";
import type { AgentHeadwear, AgentIconViewModel, AgentManifest } from "./types";

/** FNV-1a 32-bit，确定性、无碰撞域内稳定（开发文档 §5.2）。 */
export function hashString(input: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < input.length; i++) {
    hash ^= input.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash >>> 0;
}

/**
 * 按域分隔后缀取稳定索引：不同维度（frame/headwear/…）同值不碰撞。
 * mod 为 0 时返回 0（防御 manifest 空数组，调用方会另行上报）。
 */
export function stableIndex(seedKey: string, domain: string, mod: number): number {
  if (mod <= 0) return 0;
  return hashString(`${seedKey}:${domain}`) % mod;
}

/** 按文档 §5.2 的 seed 优先级：sessionId → sessionPath → topicId → item.id。 */
export function identitySeedKey(item: DesktopIconItem): string {
  const sessionId = (item.sessionId ?? "").trim();
  if (sessionId) return sessionId;
  const path = item.sessionRef?.sessionPath?.trim();
  if (path) return path;
  const topic = item.sessionRef?.topicId?.trim();
  if (topic) return topic;
  return item.id;
}

/**
 * 身份选择：frame 9 选 1；headwear 与 hats+hair 共享一个 30 槽位，帽子或
 * 头发二选一，绝不双显、绝不缺失（开发文档 §5.3）。外壳色/头部件无业务
 * 语义，禁止用任务或状态参与选择。
 */
export function pickIdentity(seedKey: string, manifest: AgentManifest): Pick<AgentIconViewModel, "frameId" | "headwear"> & { missingLayers: string[] } {
  const missingLayers: string[] = [];
  const frame = manifest.frames[stableIndex(seedKey, "frame", manifest.frames.length)];
  const frameId = frame?.id ?? "";
  if (!frame) missingLayers.push("frame");
  if (frameId && !manifest.frames.some((entry) => entry.id === frameId)) missingLayers.push(`frame:${frameId}`);

  const slot = stableIndex(seedKey, "headwear", manifest.hats.length + manifest.hair.length);
  let headwear: AgentHeadwear;
  if (slot < manifest.hats.length && manifest.hats[slot]?.id) {
    headwear = { kind: "hat", id: manifest.hats[slot].id };
  } else if (manifest.hair[slot - manifest.hats.length]?.id) {
    headwear = { kind: "hair", id: manifest.hair[slot - manifest.hats.length].id };
  } else {
    // 同步滞后/数组越界：回退 hats[0]，显式上报，不静默。
    const fallback = manifest.hats[0];
    headwear = { kind: "hat", id: fallback?.id ?? "" };
    missingLayers.push("headwear");
  }
  if (headwear.id && ![...manifest.hats, ...manifest.hair].some((entry) => entry.id === headwear.id)) {
    missingLayers.push(`headwear:${headwear.id}`);
  }
  return { frameId, headwear, missingLayers };
}
