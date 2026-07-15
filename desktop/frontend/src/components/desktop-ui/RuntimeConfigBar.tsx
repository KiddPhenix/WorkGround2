import {
  Activity,
  ArrowUp,
  Brain,
  CornerDownRight,
  Gauge,
  Shield,
  ShieldAlert,
  ShieldCheck,
} from "lucide-react";
import type { CollaborationMode, RuntimeMode, ToolApprovalMode } from "../../lib/types";
import { ModelSwitcher } from "../ModelSwitcher";

// ── Types ──────────────────────────────────────────────────────────────────

/** Connection / runtime status for the primary action derivation. */
export type ConnectionStatus = "idle" | "foreground" | "waiting_user" | "background_only" | "cancelling" | "offline";

/** RuntimeConfig holds the five config pill values. */
export interface RuntimeConfig {
  modelId: string;
  contextPercent: number;
  runtimeStatus: string;
  collaborationMode: CollaborationMode;
  approvalMode: ToolApprovalMode;
}

export interface RuntimeConfigBarProps {
  config: RuntimeConfig;
  connectionStatus: ConnectionStatus;
  hasQueue: boolean;
  tabId?: string;
  /** Fired when the user clicks the primary action button. */
  onPrimaryAction?: () => void;
  /** Switch model via the embedded ModelSwitcher. */
  onSwitchModel?: (name: string) => Promise<void>;
  /** Cycle collaboration mode (normal ↔ plan). */
  onCycleCollaboration?: () => void;
  /** Directly set tool approval mode. */
  onSetApprovalMode?: (mode: ToolApprovalMode) => void;
}

// ── Primary action label derivation ────────────────────────────────────────

/**
 * Derive the primary action label.
 *
 * | connectionStatus | label |
 * |-----------------|-------|
 * | idle            | 发送  |
 * | foreground      | 加入队列 |
 * | waiting_user    | 加入队列 |
 * | background_only | 发送  |
 * | cancelling      | 加入队列 |
 * | offline         | 保存到本地队列 |
 */
export function derivePrimaryActionLabel(
  connectionStatus: ConnectionStatus,
  _hasQueue: boolean,
): string {
  switch (connectionStatus) {
    case "idle":
    case "background_only":
      return "发送";
    case "foreground":
    case "waiting_user":
    case "cancelling":
      return "加入队列";
    case "offline":
      return "保存到本地队列";
  }
}

function primaryActionIcon(connectionStatus: ConnectionStatus): React.ReactNode {
  switch (connectionStatus) {
    case "idle":
    case "background_only":
      return <ArrowUp size={18} />;
    case "foreground":
    case "waiting_user":
    case "cancelling":
    case "offline":
      return <CornerDownRight size={18} />;
  }
}

// ── Runtime metadata → ConnectionStatus derivation ─────────────────────────

/** Derive ConnectionStatus from typed runtime state. */
export function connectionStatusFromRuntime(runtimeMode: RuntimeMode, foregroundActive: boolean): ConnectionStatus {
  if (foregroundActive && runtimeMode === "cancelling") return "cancelling";
  if (foregroundActive && runtimeMode === "waiting_user") return "waiting_user";
  if (foregroundActive) return "foreground";
  if (runtimeMode === "background_only") return "background_only";
  return "idle";
}

/** Derive a short runtime status label from runtimeMode. */
export function runtimeStatusLabel(runtimeMode: RuntimeMode): string {
  switch (runtimeMode) {
    case "foreground":
      return "运行中";
    case "waiting_user":
      return "等待用户";
    case "background_only":
      return "后台运行";
    case "cancelling":
      return "取消中";
    default:
      return "空闲";
  }
}

// ── Label mapping ───────────────────────────────────────────────────────────

function collaborationLabel(mode: CollaborationMode): string {
  switch (mode) {
    case "plan":
      return "规划";
    case "goal":
      return "目标";
    default:
      return "对话";
  }
}

function approvalLabel(mode: ToolApprovalMode): string {
  switch (mode) {
    case "auto":
      return "自动";
    case "yolo":
      return "全部允许";
    default:
      return "询问";
  }
}

function approvalIcon(mode: ToolApprovalMode): React.ReactNode {
  switch (mode) {
    case "auto":
      return <ShieldCheck size={16} />;
    case "yolo":
      return <ShieldAlert size={16} />;
    default:
      return <Shield size={16} />;
  }
}

// ── Component ───────────────────────────────────────────────────────────────

/**
 * RuntimeConfigBar renders five config items in exact order:
 *   model → context → runtime → collaboration → approval
 * plus a derived PrimaryAction button.
 *
 * Height: 48px (bottom bar of the 176px ComposerZone).
 *
 * Context and runtime are static informational items.
 * Model embeds the real ModelSwitcher.
 * Collaboration and approval are clickable and update real state.
 */
export function RuntimeConfigBar({
  config,
  connectionStatus,
  hasQueue,
  tabId,
  onPrimaryAction,
  onSwitchModel,
  onCycleCollaboration,
  onSetApprovalMode,
}: RuntimeConfigBarProps) {
  const actionLabel = derivePrimaryActionLabel(connectionStatus, hasQueue);

  return (
    <div
      className="runtime-config-bar"
      role="toolbar"
      aria-label="运行时配置"
    >
      {/* 1. Model — embedded ModelSwitcher */}
      {onSwitchModel ? (
        <div className="runtime-config-bar__model" role="presentation">
          <ModelSwitcher label={config.modelId} tabId={tabId} onPick={onSwitchModel} />
        </div>
      ) : (
        <StaticPill icon={<Brain size={16} />} label={config.modelId} ariaLabel="当前模型" />
      )}

      {/* 2. Context — static, percent only */}
      <StaticPill
        icon={<Gauge size={16} />}
        label={`${config.contextPercent}%`}
        ariaLabel="上下文使用率"
      />

      {/* 3. Runtime — static, short */}
      <StaticPill
        icon={<Activity size={16} />}
        label={config.runtimeStatus}
        ariaLabel="运行状态"
      />

      {/* 4. Collaboration — clickable, cycles modes */}
      {onCycleCollaboration ? (
        <Pill icon={<Shield size={16} />} label={collaborationLabel(config.collaborationMode)} onClick={onCycleCollaboration} ariaLabel="协作模式" />
      ) : (
        <StaticPill icon={<Shield size={16} />} label={collaborationLabel(config.collaborationMode)} ariaLabel="协作模式" />
      )}

      {/* 5. Approval — clickable, cycles 询问/自动/全部允许 */}
      {onSetApprovalMode ? (
        <Pill icon={approvalIcon(config.approvalMode)} label={`审批：${approvalLabel(config.approvalMode)}`} onClick={() => {
          const next: ToolApprovalMode =
            config.approvalMode === "ask" ? "auto" :
            config.approvalMode === "auto" ? "yolo" :
            "ask";
          onSetApprovalMode(next);
        }} ariaLabel="工具批准模式" />
      ) : (
        <StaticPill icon={approvalIcon(config.approvalMode)} label={`审批：${approvalLabel(config.approvalMode)}`} ariaLabel="工具批准模式" />
      )}

      {/* Primary Action */}
      <button
        type="button"
        className={`runtime-config-bar__primary-action runtime-config-bar__primary-action--${connectionStatus}`}
        aria-label={actionLabel}
        onClick={onPrimaryAction}
      >
        {primaryActionIcon(connectionStatus)}
        {actionLabel}
      </button>
    </div>
  );
}

// ── Pill sub-components ────────────────────────────────────────────────────

function Pill({
  icon,
  label,
  onClick,
  ariaLabel,
}: {
  icon: React.ReactNode;
  label: string;
  onClick?: () => void;
  ariaLabel: string;
}) {
  return (
    <button
      type="button"
      className="runtime-config-bar__pill"
      aria-label={ariaLabel}
      onClick={onClick}
    >
      {icon}
      <span className="runtime-config-bar__pill-label">{label}</span>
    </button>
  );
}

function StaticPill({
  icon,
  label,
  ariaLabel,
}: {
  icon: React.ReactNode;
  label: string;
  ariaLabel: string;
}) {
  return (
    <span
      className="runtime-config-bar__pill runtime-config-bar__pill--static"
      aria-label={ariaLabel}
    >
      {icon}
      <span className="runtime-config-bar__pill-label">{label}</span>
    </span>
  );
}
