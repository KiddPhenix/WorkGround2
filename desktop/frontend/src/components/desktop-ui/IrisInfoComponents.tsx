// IrisInfoComponents — reactive store-connected wrapper components for the
// information (workbench) layout. Each subscribes to one Zustand store and
// renders the corresponding presentational primitive.
//
// These components keep App.tsx clean of store wiring and are tree-shakeable
// from the classic layout.

import { useCallback, useMemo, useState } from "react";
import { useMemoryStore, selectMemory } from "../../store/memory";
import { useArtifactStore, selectArtifactsBySession } from "../../store/artifacts";
import { useComposerQueueStore } from "../../store/composerQueue";
import {
  useAddOnSurfaceStore,
  selectSortedAddOnInstances,
  selectActiveAddOnCount,
  selectNeedsInputAddOnCount,
} from "../../store/addonSurface";
import { TaskMemoryBar } from "../desktop-ui/TaskMemoryBar";
import { ArtifactShelf } from "../desktop-ui/ArtifactShelf";
import { QueueTray } from "../desktop-ui/QueueTray";
import { RuntimeConfigBar } from "../desktop-ui/RuntimeConfigBar";
import { AddOnWorkbench } from "../desktop-ui/AddOnWorkbench";
import { RunBlock } from "../desktop-ui/RunBlock";
import { useRunStore } from "../../store/run";
import { Layers } from "lucide-react";
import { app } from "../../lib/bridge";
import type { CollaborationMode, ToolApprovalMode } from "../../lib/types";
import type { Item } from "../../lib/useController";

// ── Helpers ────────────────────────────────────────────────────────────────

export interface SessionSummary {
  headline: string;
  detail: string;
}

type RecapPoints = { outcomes: string[]; checks: string[] };

const NOISE_HEADING = /^(已?完成(?:总结)?|总结|改动文件|变更文件|文件列表|创建的文件|修改的文件|验证命令|验证|命令|测试|注意事项)$/i;
const COMMAND_LINE = /^(?:[$>#]\s*)?(?:cd|pnpm|npm|npx|yarn|go\s+(?:test|build|vet)|git\s+|tsc\b|make\b|\.\\)[\s>]/i;
const CHECK_LINE = /测试|typecheck|类型检查|构建|build|编译|lint|校验|验证/i;

function cleanRecapPoint(value: string): string {
  return value
    .replace(/^[-*+]\s+(?:\[[ xX]\]\s*)?/, "")
    .replace(/\[([^\]]+)]\([^)]+\)/g, "$1")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/[*_~]/g, "")
    .replace(/[✅✔☑❌⚠️]/gu, "")
    .replace(/\s+/g, " ")
    .trim();
}

function compactTaskText(value: string, maxRunes = 96): string {
  const runes = Array.from(cleanRecapPoint(value));
  return runes.length > maxRunes ? `${runes.slice(0, maxRunes).join("")}…` : runes.join("");
}

function isPathLike(value: string): boolean {
  return /[\\/]/.test(value) && /\.[a-z0-9]{1,8}(?:\s|$)/i.test(value);
}

function addPoint(target: string[], value: string) {
  const point = cleanRecapPoint(value).replace(/[，。；：:]+$/, "");
  const key = point.replace(/\s+/g, "").toLowerCase();
  if (point.length < 4 || target.some((item) => item.replace(/\s+/g, "").toLowerCase() === key)) return;
  target.push(point);
}

function tablePoint(line: string): string | null {
  if (/^\|[\s:|-]+\|$/.test(line)) return null;
  const cells = line.split("|").slice(1, -1).map(cleanRecapPoint).filter(Boolean);
  if (cells.some((cell) => /^(文件|改动|说明|状态|路径|file|change|status|path)$/i.test(cell))) return null;
  const descriptions = cells.filter((cell) => !isPathLike(cell) && !/^(完成|通过|done|pass(?:ed)?)$/i.test(cell));
  return descriptions[descriptions.length - 1] ?? null;
}

function recapPoints(text: string): RecapPoints {
  const outcomes: string[] = [];
  const checks: string[] = [];
  let fenced = false;
  for (const source of text.split("\n")) {
    const raw = source.trim();
    if (raw.startsWith("```")) { fenced = !fenced; continue; }
    if (fenced || !raw || COMMAND_LINE.test(raw)) continue;

    let point: string | null;
    if (raw.startsWith("|") && raw.endsWith("|")) {
      point = tablePoint(raw);
    } else if (/^#{1,6}\s+/.test(raw)) {
      point = cleanRecapPoint(raw.replace(/^#{1,6}\s+/, ""));
      if (NOISE_HEADING.test(point)) continue;
    } else {
      point = cleanRecapPoint(raw);
    }
    if (!point || COMMAND_LINE.test(point) || isPathLike(point)) continue;
    if (CHECK_LINE.test(point)) {
      addPoint(checks, point);
    } else if (!/^[-*+]\s+(?:\[[xX ]\]|[✅✔☑])/u.test(raw)) {
      addPoint(outcomes, point);
    }
  }
  return { outcomes, checks };
}

function firstSentence(value: string): string {
  return value.split(/(?<=[。！？!?])/u)[0].replace(/[。；]+$/, "");
}

function checkSummary(checks: string[]): string | null {
  if (checks.length === 0) return null;
  const source = checks.join(" ");
  const count = [...source.matchAll(/(\d+)\s*(?:项|个)?[^，。；\d]{0,12}测试/gu)]
    .map((match) => Number(match[1]))
    .sort((a, b) => b - a)[0];
  const parts = [count ? `${count} 项测试` : /测试/i.test(source) ? "测试" : ""];
  if (/typecheck|类型检查/i.test(source)) parts.push("类型检查");
  if (/构建|build|编译/i.test(source)) parts.push("构建");
  if (/lint|校验/i.test(source)) parts.push("代码校验");
  const named = parts.filter(Boolean);
  return named.length > 0 ? `验证：${named.join("、")}通过` : "验证完成";
}

function isBridgePoint(point: string, lead: string): boolean {
  if (!/^(?:重写|更新|修改|完成|实现|调整)了?.+(?:组件|功能|内容)$/u.test(point)) return false;
  const subject = point.replace(/^(?:重写|更新|修改|完成|实现|调整)了?/u, "").replace(/(?:组件|功能|内容)$/u, "").trim();
  return subject.length > 0 && lead.includes(subject);
}

function buildSummary(text: string): SessionSummary | null {
  const points = recapPoints(text);
  const first = points.outcomes[0];
  if (!first) return null;
  let lead = firstSentence(first);
  if (!/完成|修复|新增|重写|更新|实现|优化|支持|移除|通过/.test(lead)) lead = `已完成：${lead}`;

  const specifics = points.outcomes.slice(1).map(firstSentence).filter((point) => !isBridgePoint(point, lead));
  const headlinePoints = lead.length < 32 ? specifics.slice(0, 3) : [];
  const headline = headlinePoints.length > 0 ? `${lead}：${headlinePoints.join("，")}` : lead;
  const extra = specifics[headlinePoints.length] ?? "";
  const detail = [headline, extra, checkSummary(points.checks) ?? ""]
    .filter(Boolean)
    .map((point) => `${point.replace(/[。；]+$/, "")}。`)
    .join("");
  return { headline, detail };
}

/** Return one structured recap: a visible headline and its complete tooltip detail. */
export function recentSessionSummary(items: Item[]): SessionSummary | null {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind === "assistant" && !item.streaming && item.text.trim()) {
      const summary = buildSummary(item.text);
      if (summary) return summary;
    }
  }
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind === "user" && !item.queued && item.text.trim()) {
      const cleaned = cleanRecapPoint(item.text);
      if (!cleaned) continue;
      const headline = `处理：${firstSentence(cleaned)}`;
      return { headline, detail: `处理：${cleaned}` };
    }
  }
  return null;
}

function latestUserTask(items: Item[]): string | null {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind === "user" && !item.queued && item.text.trim()) return compactTaskText(item.text) || null;
  }
  return null;
}

// ── SessionMemoryBar ────────────────────────────────────────────────────────

export function SessionMemoryBar({
  sessionId,
  items,
  running = false,
}: {
  sessionId: string;
  items?: Item[];
  running?: boolean;
}) {
  const memoryBySession = useMemoryStore((s) => s.memoryBySession);
  const memoryLine = selectMemory(memoryBySession, sessionId) ?? null;
  const visibleMemory = useMemo(() => {
    if (memoryLine || !running || !items?.length) return memoryLine;
    const goal = latestUserTask(items);
    return goal ? { goal, current: "运行中", nextStep: "", goalSource: "user_prompt", currentSource: "runtime" } : null;
  }, [items, memoryLine, running]);

  // Completed sessions retain the persisted task briefing and add a compact
  // transcript-derived recap. This also covers old sessions without sidecars.
  const summary = useMemo(() => {
    if (running) return null;
    if (!items || items.length === 0) return null;
    return recentSessionSummary(items);
  }, [running, items]);

  const recentlyDid = summary?.headline ?? null;
  const recentlyDidDetail = summary?.detail ?? null;

  return <TaskMemoryBar memoryLine={visibleMemory} recentlyDid={recentlyDid} recentlyDidDetail={recentlyDidDetail} />;
}

// ── SessionRunStream ───────────────────────────────────────────────────────

export function SessionRunStream({
  sessionId,
  statuses,
  onStop,
}: {
  sessionId: string;
  statuses?: Array<"queued" | "running" | "waiting_user" | "reconnecting" | "completed" | "failed" | "cancelled">;
  onStop?: () => void;
}) {
  const runs = useRunStore((s) => s.runs);
  const setRunExpanded = useRunStore((s) => s.setRunExpanded);
  const setRunSelectedStep = useRunStore((s) => s.setRunSelectedStep);
  const sessionRuns = useMemo(
    () => Object.values(runs)
      .filter((run) => run.sessionId === sessionId && (!statuses || statuses.includes(run.status)))
      .sort((a, b) => a.startedAt - b.startedAt),
    [runs, sessionId, statuses],
  );

  if (sessionRuns.length === 0) return null;

  const terminalRuns = sessionRuns.filter((run) =>
    (run.status === "completed" || run.status === "failed" || run.status === "cancelled") && !run.expanded,
  );
  const activeRuns = sessionRuns.filter((run) => !terminalRuns.includes(run));
  const terminalOnly = activeRuns.length === 0;

  const renderRun = (run: (typeof sessionRuns)[number]) => (
    <RunBlock
      key={run.runId}
      run={run}
      onToggle={(runId) => setRunExpanded(runId, !runs[runId]?.expanded)}
      onStop={onStop ? () => onStop() : undefined}
      onStepSelect={(runId, stepIndex) => {
        const currentRun = runs[runId];
        if (!currentRun) return;
        // Toggle off: clicking the already-selected last tab restores auto-follow
        if (currentRun.selectedStepIndex === stepIndex && stepIndex === currentRun.events.length - 1) {
          setRunSelectedStep(runId, undefined);
        } else {
          setRunSelectedStep(runId, stepIndex);
        }
      }}
    />
  );

  return (
    <div className={`session-run-stream${terminalOnly ? " session-run-stream--terminal" : ""}`} aria-label="任务运行记录">
      {terminalRuns.length > 0 && (
        <div className="session-run-stream__terminal" aria-label="已结束运行">
          {terminalRuns.map(renderRun)}
        </div>
      )}
      {activeRuns.map(renderRun)}
    </div>
  );
}

// ── SessionArtifactShelf ─────────────────────────────────────────────────────

export function SessionArtifactShelf({ sessionId }: { sessionId: string }) {
  const artifacts = useArtifactStore((s) => s.artifacts);
  const sessionArtifacts = selectArtifactsBySession(artifacts, sessionId);

  const onOpen = useCallback((artifactId: string) => {
    const artifact = artifacts[artifactId];
    if (!artifact?.path) return;
    void app.RevealPath(artifact.path);
  }, [artifacts]);

  return <ArtifactShelf artifacts={sessionArtifacts} onOpen={onOpen} />;
}

// ── SessionQueueTray ─────────────────────────────────────────────────────────

export function SessionQueueTray({ onEditContent }: { onEditContent?: (content: string) => void } = {}) {
  const items = useComposerQueueStore((s) => s.items);
  const removeItem = useComposerQueueStore((s) => s.removeItem);
  const reorderItems = useComposerQueueStore((s) => s.reorderItems);

  const onEdit = useCallback((queueItemId: string) => {
    const item = items.find((candidate) => candidate.queueItemId === queueItemId);
    if (!item) return;
    onEditContent?.(item.content);
    removeItem(queueItemId);
  }, [items, onEditContent, removeItem]);

  const onRemove = useCallback((queueItemId: string) => {
    removeItem(queueItemId);
  }, [removeItem]);

  const move = useCallback((queueItemId: string, delta: number) => {
    const from = items.findIndex((item) => item.queueItemId === queueItemId);
    if (from < 0) return;
    reorderItems(from, from + delta);
  }, [items, reorderItems]);

  return (
    <QueueTray
      items={items}
      onEdit={onEditContent ? onEdit : undefined}
      onRemove={onRemove}
      onMoveUp={(id) => move(id, -1)}
      onMoveDown={(id) => move(id, 1)}
    />
  );
}

// ── SessionConfigBar ─────────────────────────────────────────────────────────

export function SessionConfigBar({
  modelLabel,
  contextPercent,
  running,
  collaborationMode,
  toolApprovalMode,
  controllerReady,
  tabId,
  onPrimaryAction,
  onSwitchModel,
  onCycleCollaboration,
  onSetApprovalMode,
}: {
  modelLabel: string;
  contextPercent: number;
  running: boolean;
  collaborationMode: CollaborationMode;
  toolApprovalMode: ToolApprovalMode;
  controllerReady: boolean;
  tabId?: string;
  onPrimaryAction?: () => void;
  onSwitchModel?: (name: string) => Promise<void>;
  onCycleCollaboration?: () => void;
  onSetApprovalMode?: (mode: ToolApprovalMode) => void;
}) {
  const hasQueue = useComposerQueueStore((s) => s.items.length > 0);
  return (
    <RuntimeConfigBar
      config={{
        modelId: modelLabel,
        contextPercent,
        runtimeStatus: running ? "运行中" : "空闲",
        collaborationMode,
        approvalMode: toolApprovalMode,
      }}
      connectionStatus={running ? "running" : "idle"}
      hasQueue={hasQueue}
      tabId={tabId}
      onPrimaryAction={controllerReady ? onPrimaryAction : undefined}
      onSwitchModel={onSwitchModel}
      onCycleCollaboration={onCycleCollaboration}
      onSetApprovalMode={onSetApprovalMode}
    />
  );
}

// ── AddOnLauncherButton ──────────────────────────────────────────────────────

export function AddOnLauncherButton() {
  const workbenchOpen = useAddOnSurfaceStore((s) => s.workbenchOpen);
  const setWorkbenchOpen = useAddOnSurfaceStore((s) => s.setWorkbenchOpen);
  const instances = useAddOnSurfaceStore((s) => s.instances);
  const activeCount = selectActiveAddOnCount(instances);
  const needsInputCount = selectNeedsInputAddOnCount(instances);

  return (
    <button
      type="button"
      className="session-header__addon-btn"
      aria-label="AddOn 启动器"
      onClick={() => setWorkbenchOpen(!workbenchOpen)}
    >
      <Layers size={16} aria-hidden="true" />
      {activeCount > 0 && (
        <span className={`session-header__addon-count${needsInputCount > 0 ? " session-header__addon-count--warn" : ""}`}>
          {activeCount}
        </span>
      )}
    </button>
  );
}

// ── AddOnWorkbenchOverlay ────────────────────────────────────────────────────

export function AddOnWorkbenchOverlay() {
  const instances = useAddOnSurfaceStore((s) => s.instances);
  const workbenchOpen = useAddOnSurfaceStore((s) => s.workbenchOpen);
  const editingInstanceId = useAddOnSurfaceStore((s) => s.editingInstanceId);
  const _frozenDisplayIndex = useAddOnSurfaceStore((s) => s._frozenDisplayIndex);
  const setWorkbenchOpen = useAddOnSurfaceStore((s) => s.setWorkbenchOpen);
  const setInstanceDensity = useAddOnSurfaceStore((s) => s.setInstanceDensity);
  const setInstancePinned = useAddOnSurfaceStore((s) => s.setInstancePinned);
  const [workbenchPinned, setWorkbenchPinned] = useState(false);

  const sorted = useMemo(
    () => selectSortedAddOnInstances(instances, editingInstanceId, _frozenDisplayIndex)
      .filter((instance) => instance.status !== "dismissed"),
    [instances, editingInstanceId, _frozenDisplayIndex],
  );

  const onDensityToggle = useCallback((instanceId: string) => {
    const inst = instances[instanceId];
    if (!inst) return;
    const next: "tab" | "peek" | "focus" =
      inst.density === "focus" ? "tab" :
      inst.density === "peek" ? "focus" : "peek";
    setInstanceDensity(instanceId, next);
  }, [instances, setInstanceDensity]);

  const renderBody = useCallback((instance: { instanceId: string; title: string; status: string; panelId: string }) => {
    if (instance.panelId === "login") {
      return (
        <div className="instance-body__form">
          <div className="instance-body__slot">
            <label className="instance-body__label">用户名</label>
            <input className="instance-body__input" type="text" defaultValue="" placeholder="输入用户名" />
            <label className="instance-body__label">密码</label>
            <input className="instance-body__input" type="password" defaultValue="" placeholder="输入密码" />
            <button type="button" className="instance-body__submit">登录</button>
          </div>
        </div>
      );
    }
    if (instance.panelId === "monitor") {
      return (
        <div className="instance-body__status">
          <div className="instance-body__slot">
            <div className="instance-body__progress-row">
              <span>构建进度</span>
              <span className="instance-body__progress-pct">47%</span>
            </div>
            <div className="instance-body__bar">
              <div className="instance-body__bar-fill instance-body__bar-fill--47" />
            </div>
            <div className="instance-body__hint">最近状态：编译通过，测试运行中</div>
          </div>
        </div>
      );
    }
    if (instance.panelId === "preview") {
      return (
        <div className="instance-body__media">
          <div className="instance-body__slot instance-body__slot--center">
            <div className="instance-body__video" role="img" aria-label="demo.mp4 媒体预览" />
            <span className="instance-body__hint">demo.mp4 · 播放中</span>
          </div>
        </div>
      );
    }
    return <div className="instance-body__custom"><div className="instance-body__slot">插件内容</div></div>;
  }, []);

  if (!workbenchOpen) return null;

  return (
    <div className="addon-workbench-overlay">
      <AddOnWorkbench
        instances={sorted}
        pinned={workbenchPinned}
        onPin={() => {
          setWorkbenchPinned((value) => !value);
          for (const instance of sorted) setInstancePinned(instance.instanceId, !workbenchPinned);
        }}
        onMinimize={() => setWorkbenchOpen(false)}
        onClose={() => setWorkbenchOpen(false)}
        onDensityToggle={onDensityToggle}
        renderBody={renderBody}
      />
    </div>
  );
}
