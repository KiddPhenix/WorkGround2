import {
  AlertCircle,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Circle,
  CircleHelp,
  Minus,
  Pin,
  X,
} from "lucide-react";
import type { AddOnInstance, AddOnInstanceStatus } from "../../store/addonSurface";

// ── Props ──────────────────────────────────────────────────────────────────

export interface WorkbenchHeaderProps {
  /** Count of non-dismissed instances. */
  activeCount: number;
  /** Whether the workbench is currently pinned. */
  pinned: boolean;
  onPin?: () => void;
  onMinimize?: () => void;
  onClose?: () => void;
}

export interface AddOnInstanceViewProps {
  instance: AddOnInstance;
  onDensityToggle?: (instanceId: string) => void;
  renderBody?: (instance: AddOnInstance) => React.ReactNode;
}

export interface AddOnWorkbenchProps {
  /** All non-dismissed instances in display order (pre-sorted by caller). */
  instances: AddOnInstance[];
  /** Whether the workbench is pinned. */
  pinned: boolean;
  onPin?: () => void;
  onMinimize?: () => void;
  onClose?: () => void;
  onDensityToggle?: (instanceId: string) => void;
  /** Render slot for the body content of each instance. */
  renderBody?: (instance: AddOnInstance) => React.ReactNode;
}

// ── Status helpers ─────────────────────────────────────────────────────────

const STATUS_LABEL: Record<AddOnInstanceStatus, string> = {
  registered: "已注册",
  loading: "正在加载",
  queued: "排队中",
  active: "活动中",
  needs_input: "需要输入",
  warning: "警告",
  error: "错误",
  offline: "离线",
  completed: "已完成",
  playing: "播放中",
  paused: "已暂停",
  dismissed: "",
};

function statusIcon(status: AddOnInstanceStatus, size = 12): React.ReactNode {
  switch (status) {
    case "active":
    case "loading":
    case "playing":
      return <Circle size={size} fill="currentColor" />;
    case "needs_input":
    case "warning":
      return <CircleHelp size={size} />;
    case "error":
      return <AlertCircle size={size} />;
    case "completed":
      return <CheckCircle2 size={size} />;
    default:
      return <Circle size={size} />;
  }
}

// ── WorkbenchHeader ─────────────────────────────────────────────────────────

/**
 * WorkbenchHeader — 56px, controls the entire workbench.
 * Shows "AddOn · N 活跃" with pin / minimize / close actions.
 */
export function WorkbenchHeader({
  activeCount,
  pinned,
  onPin,
  onMinimize,
  onClose,
}: WorkbenchHeaderProps) {
  return (
    <div className="workbench-header">
      <span className="workbench-header__title">AddOn · {activeCount} 活跃</span>

      <span className="workbench-header__actions">
        {onPin && (
          <IconButton
            icon={<Pin size={16} />}
            label={pinned ? "取消固定" : "固定"}
            active={pinned}
            onClick={onPin}
          />
        )}
        {onMinimize && (
          <IconButton icon={<Minus size={16} />} label="最小化" onClick={onMinimize} />
        )}
        {onClose && (
          <IconButton icon={<X size={16} />} label="关闭" onClick={onClose} />
        )}
      </span>
    </div>
  );
}

// ── InstanceHeader ─────────────────────────────────────────────────────────

/**
 * InstanceHeader — 44px, shows icon + name + status + collapse for one instance.
 * Close / reload are lower-frequency actions left to the instance menu.
 */
export function InstanceHeader({
  instance,
  onDensityToggle,
}: {
  instance: AddOnInstance;
  onDensityToggle?: (instanceId: string) => void;
}) {
  const isCollapsed = instance.density === "tab";

  return (
    <div className="instance-header">
      <span className={`instance-header__status-icon instance-header__status-icon--${instance.status}`} aria-hidden="true">
        {statusIcon(instance.status, 12)}
      </span>

      <span className="instance-header__name">{instance.title}</span>

      {instance.status !== "active" && instance.status !== "registered" && (
        <span className={`instance-header__status-text instance-header__status-text--${instance.status}`}>
          {STATUS_LABEL[instance.status]}
        </span>
      )}

      {onDensityToggle && (
        <IconButton
          icon={isCollapsed ? <ChevronDown size={14} /> : <ChevronUp size={14} />}
          label={isCollapsed ? "展开" : "收起"}
          onClick={() => onDensityToggle(instance.instanceId)}
        />
      )}
    </div>
  );
}

// ── AddOnInstanceView ──────────────────────────────────────────────────────

/**
 * AddOnInstanceView — renders one AddOn instance with its density-controlled
 * header + optional body slot.
 */
export function AddOnInstanceView({
  instance,
  onDensityToggle,
  renderBody,
}: AddOnInstanceViewProps) {
  const isTab = instance.density === "tab";

  return (
    <div
      className={`addon-instance addon-instance--${instance.density} addon-instance--${instance.status}`}
      data-panel={instance.panelId}
      role="region"
      aria-label={`${instance.title} — ${STATUS_LABEL[instance.status]}`}
      aria-busy={instance.status === "loading" || instance.status === "queued"}
      tabIndex={isTab ? 0 : undefined}
      onClick={() => {
        if (isTab && onDensityToggle) onDensityToggle(instance.instanceId);
      }}
      onKeyDown={(e) => {
        if ((e.key === "Enter" || e.key === " ") && isTab && onDensityToggle) {
          e.preventDefault();
          onDensityToggle(instance.instanceId);
        }
      }}
    >
      <InstanceHeader instance={instance} onDensityToggle={onDensityToggle} />

      {!isTab && renderBody && (
        <div className="instance-body">
          {renderBody(instance)}

          {instance.message && (
            <div className={`instance-body__message instance-body__message--${instance.status}`}>
              {statusIcon(instance.status, 14)}
              <span>{instance.message}</span>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ── AddOnWorkbench ─────────────────────────────────────────────────────────

/**
 * AddOnWorkbench is the host shell for multiple AddOn instances.
 * It aggregates WorkbenchHeader + a stack of AddOnInstanceView entries.
 *
 * This is a pure presentational primitive — it does NOT subscribe to stores.
 * It does NOT manufacture plugin-specific UI or arbitrary HTML.
 */
export function AddOnWorkbench({
  instances,
  pinned,
  onPin,
  onMinimize,
  onClose,
  onDensityToggle,
  renderBody,
}: AddOnWorkbenchProps) {
  return (
    <div
      className="addon-workbench"
      role="dialog"
      aria-label="AddOn 工作台"
      aria-modal="false"
    >
      <WorkbenchHeader
        activeCount={instances.length}
        pinned={pinned}
        onPin={onPin}
        onMinimize={onMinimize}
        onClose={onClose}
      />

      <div className="addon-stack" role="list" aria-label="AddOn 实例列表">
        {instances.map((inst) => (
          <AddOnInstanceView
            key={inst.instanceId}
            instance={inst}
            onDensityToggle={onDensityToggle}
            renderBody={renderBody}
          />
        ))}

        {instances.length === 0 && (
          <div className="addon-stack__empty">暂无活跃的 AddOn</div>
        )}
      </div>
    </div>
  );
}

// ── Internal helpers ───────────────────────────────────────────────────────

function IconButton({
  icon,
  label,
  active,
  onClick,
}: {
  icon: React.ReactNode;
  label: string;
  active?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={`icon-button${active ? " icon-button--active" : ""}`}
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation();
        onClick();
      }}
    >
      {icon}
    </button>
  );
}
