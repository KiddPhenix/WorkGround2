import {
  Activity,
  ArrowUp,
  Brain,
  BriefcaseBusiness,
  CheckCircle2,
  CornerDownRight,
  Gauge,
  Shield,
  ShieldAlert,
  ShieldCheck,
} from "lucide-react";
import type { CollaborationMode, RuntimeMode, ToolApprovalMode } from "../../lib/types";
import { useT, type DictKey } from "../../lib/i18n";
import { ModelSwitcher } from "../ModelSwitcher";

// ── Types ──────────────────────────────────────────────────────────────────

/** Connection / runtime status for the primary action derivation. */
export type ConnectionStatus = "idle" | "foreground" | "waiting_user" | "background_only" | "cancelling" | "offline";

/** Which kind of surface is hosting the bar — used to tune labels per context. */
export type SurfaceKind = "work" | "workspace";

/** RuntimeConfig holds the five config pill values. */
export interface RuntimeConfig {
  modelId: string;
  contextPercent: number;
  runtimeMode: RuntimeMode;
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
  /** Whether the first message should be submitted as a Work session. */
  workSendAvailable?: boolean;
  workSendSelected?: boolean;
  workSendDisabled?: boolean;
  onWorkSendChange?: (selected: boolean) => void;
  /** Switch model via the embedded ModelSwitcher. */
  onSwitchModel?: (name: string) => Promise<void>;
  /** Cycle collaboration mode (normal ↔ plan). */
  onCycleCollaboration?: () => void;
  /** Directly set tool approval mode. */
  onSetApprovalMode?: (mode: ToolApprovalMode) => void;
  /** Surface kind: "work" uses Work-specific labels (e.g. normal → 工作). */
  surfaceKind?: SurfaceKind;
}

// ── Primary action label derivation ────────────────────────────────────────

const PRIMARY_ACTION_KEYS: Record<ConnectionStatus, DictKey> = {
  idle: "runtimeBar.action.send",
  foreground: "runtimeBar.action.queue",
  waiting_user: "runtimeBar.action.queue",
  background_only: "runtimeBar.action.send",
  cancelling: "runtimeBar.action.queue",
  offline: "runtimeBar.action.send",
};

/**
 * Derive the primary action label key.
 *
 * 启动/暂未就绪（offline）时仍可安全提交到启动队列，controller
 * 就绪后由 drainStartupSend 自动发送，因此主按钮统一显示“发送”。
 *
 * | connectionStatus | label |
 * |-----------------|-------|
 * | idle            | 发送  |
 * | foreground      | 加入队列 |
 * | waiting_user    | 加入队列 |
 * | background_only | 发送  |
 * | cancelling      | 加入队列 |
 * | offline         | 发送  |
 */
export function derivePrimaryActionLabel(
  connectionStatus: ConnectionStatus,
  _hasQueue: boolean,
): DictKey {
  return PRIMARY_ACTION_KEYS[connectionStatus];
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

const RUNTIME_STATUS_KEYS: Record<RuntimeMode, DictKey> = {
  foreground: "runtimeBar.runtime.foreground",
  waiting_user: "runtimeBar.runtime.waitingUser",
  background_only: "runtimeBar.runtime.background",
  cancelling: "runtimeBar.runtime.cancelling",
  idle: "runtimeBar.runtime.idle",
};

/** Derive the runtime status label key from runtimeMode. */
export function runtimeStatusLabel(runtimeMode: RuntimeMode): DictKey {
  return RUNTIME_STATUS_KEYS[runtimeMode];
}

// ── Label mapping ───────────────────────────────────────────────────────────

function collaborationLabelKey(mode: CollaborationMode, surfaceKind?: SurfaceKind): DictKey {
  switch (mode) {
    case "plan":
      return "runtimeBar.collab.plan";
    case "goal":
      return "runtimeBar.collab.goal";
    default:
      return surfaceKind === "work" ? "runtimeBar.collab.work" : "runtimeBar.collab.chat";
  }
}

function approvalLabelKey(mode: ToolApprovalMode): DictKey {
  switch (mode) {
    case "auto":
      return "runtimeBar.approval.auto";
    case "yolo":
      return "runtimeBar.approval.yolo";
    default:
      return "runtimeBar.approval.ask";
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
  workSendAvailable = false,
  workSendSelected = false,
  workSendDisabled = false,
  onWorkSendChange,
  onSwitchModel,
  onCycleCollaboration,
  onSetApprovalMode,
  surfaceKind,
}: RuntimeConfigBarProps) {
  const t = useT();
  const actionLabel = t(derivePrimaryActionLabel(connectionStatus, hasQueue));
  const collabLabel = t(collaborationLabelKey(config.collaborationMode, surfaceKind));
  const approvalValue = t(approvalLabelKey(config.approvalMode));

  return (
    <div
      className="runtime-config-bar"
      role="toolbar"
      aria-label={t("runtimeBar.aria")}
    >
      {/* 1. Model — embedded ModelSwitcher */}
      {onSwitchModel ? (
        <div className="runtime-config-bar__model" role="presentation">
          <ModelSwitcher label={config.modelId} tabId={tabId} onPick={onSwitchModel} />
        </div>
      ) : (
        <StaticPill icon={<Brain size={16} />} label={config.modelId} ariaLabel={t("runtimeBar.modelAria")} />
      )}

      {/* 2. Context — static, percent only */}
      <StaticPill
        icon={<Gauge size={16} />}
        label={`${config.contextPercent}%`}
        ariaLabel={t("runtimeBar.contextAria")}
      />

      {/* 3. Runtime — static, short */}
      <StaticPill
        icon={<Activity size={16} />}
        label={t(runtimeStatusLabel(config.runtimeMode))}
        ariaLabel={t("runtimeBar.runtimeAria")}
      />

      {/* 4. Collaboration — clickable, cycles modes */}
      {onCycleCollaboration ? (
        <Pill icon={<Shield size={16} />} label={collabLabel} onClick={onCycleCollaboration} ariaLabel={t("runtimeBar.collabAria")} />
      ) : (
        <StaticPill icon={<Shield size={16} />} label={collabLabel} ariaLabel={t("runtimeBar.collabAria")} />
      )}

      {/* 5. Approval — clickable, cycles 询问/自动/全部允许 */}
      {onSetApprovalMode ? (
        <Pill icon={approvalIcon(config.approvalMode)} label={t("runtimeBar.approval.label", { mode: approvalValue })} onClick={() => {
          const next: ToolApprovalMode =
            config.approvalMode === "ask" ? "auto" :
            config.approvalMode === "auto" ? "yolo" :
            "ask";
          onSetApprovalMode(next);
        }} ariaLabel={t("runtimeBar.approvalAria")} />
      ) : (
        <StaticPill icon={approvalIcon(config.approvalMode)} label={t("runtimeBar.approval.label", { mode: approvalValue })} ariaLabel={t("runtimeBar.approvalAria")} />
      )}

      {workSendAvailable && (
        <button
          type="button"
          className={`runtime-config-bar__work-send${workSendSelected ? " runtime-config-bar__work-send--active" : ""}`}
          aria-label={t(workSendSelected ? "runtimeBar.workSend.on" : "runtimeBar.workSend.off")}
          aria-pressed={workSendSelected}
          disabled={workSendDisabled}
          onClick={() => onWorkSendChange?.(!workSendSelected)}
        >
          {workSendSelected
            ? <CheckCircle2 size={16} aria-hidden="true" />
            : <BriefcaseBusiness size={15} aria-hidden="true" />}
          <span>{t(workSendSelected ? "runtimeBar.workSend.on" : "runtimeBar.workSend.off")}</span>
        </button>
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
