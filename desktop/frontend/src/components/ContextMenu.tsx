import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent, ReactNode } from "react";
import { createPortal } from "react-dom";

export type ContextMenuPoint = { left: number; top: number };

export type ContextMenuItem =
  | {
      type?: "item";
      key: string;
      icon?: ReactNode;
      label: ReactNode;
      disabled?: boolean;
      danger?: boolean;
      variant?: "section" | "color" | "visual";
      checked?: boolean;
      title?: string;
      onSelect: () => void;
    }
  | {
      type: "separator";
      key: string;
    };

const EDGE_GAP = 8;
const MENU_NAV_KEYS = new Set(["ArrowDown", "ArrowUp", "Home", "End"]);

export function nextContextMenuFocus(enabledIndexes: number[], currentIndex: number, key: string): number | undefined {
  if (!MENU_NAV_KEYS.has(key) || enabledIndexes.length === 0) return undefined;
  if (key === "Home") return enabledIndexes[0];
  if (key === "End") return enabledIndexes[enabledIndexes.length - 1];
  const current = enabledIndexes.indexOf(currentIndex);
  if (key === "ArrowDown") return enabledIndexes[current < 0 ? 0 : (current + 1) % enabledIndexes.length];
  return enabledIndexes[current < 0 ? enabledIndexes.length - 1 : (current - 1 + enabledIndexes.length) % enabledIndexes.length];
}

function clampMenuPoint(left: number, top: number, width: number, height: number): ContextMenuPoint {
  if (typeof window === "undefined") return { left, top };
  return {
    left: Math.min(Math.max(EDGE_GAP, left), Math.max(EDGE_GAP, window.innerWidth - width - EDGE_GAP)),
    top: Math.min(Math.max(EDGE_GAP, top), Math.max(EDGE_GAP, window.innerHeight - height - EDGE_GAP)),
  };
}

export function contextMenuPointFromEvent(
  event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>,
): ContextMenuPoint {
  if ("clientX" in event && event.clientX > 0 && event.clientY > 0) {
    return { left: event.clientX, top: event.clientY };
  }
  const rect = event.currentTarget.getBoundingClientRect();
  return { left: rect.left + 12, top: rect.bottom + 6 };
}

export function ContextMenu({
  open,
  point,
  items,
  onClose,
  minWidth = 180,
  ariaLabel = "Context menu",
}: {
  open: boolean;
  point: ContextMenuPoint | null;
  items: ContextMenuItem[];
  onClose: () => void;
  minWidth?: number;
  ariaLabel?: string;
}) {
  const menuRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<ContextMenuPoint | null>(point);

  useLayoutEffect(() => {
    if (!open || !point) return;
    const rect = menuRef.current?.getBoundingClientRect();
    if (!rect) {
      setPosition(point);
      return;
    }
    setPosition(clampMenuPoint(point.left, point.top, rect.width, rect.height));
  }, [open, point, items]);

  useLayoutEffect(() => {
    if (!open || !position) return;
    window.requestAnimationFrame(() => menuRef.current?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus());
  }, [items, open, position]);

  useEffect(() => {
    if (!open) return;
    const closeOnOutsidePointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (target instanceof Node && menuRef.current?.contains(target)) return;
      onClose();
    };
    const close = () => onClose();
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("pointerdown", closeOnOutsidePointerDown, true);
    window.addEventListener("resize", close);
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("pointerdown", closeOnOutsidePointerDown, true);
      window.removeEventListener("resize", close);
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [open, onClose]);

  if (!open || !point || !position) return null;

  return createPortal(
    <div
      ref={menuRef}
      className="context-menu"
      role="menu"
      aria-label={ariaLabel}
      style={{ left: position.left, top: position.top, minWidth }}
      onMouseDown={(event) => {
        event.preventDefault();
        event.stopPropagation();
      }}
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => {
        const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>("button")];
        const enabledIndexes = buttons.flatMap((button, index) => button.disabled ? [] : [index]);
        const next = nextContextMenuFocus(enabledIndexes, buttons.indexOf(document.activeElement as HTMLButtonElement), event.key);
        if (next === undefined) return;
        event.preventDefault();
        event.stopPropagation();
        buttons[next]?.focus();
      }}
      onContextMenu={(event) => {
        event.preventDefault();
        event.stopPropagation();
      }}
    >
      {items.map((item) => {
        if (item.type === "separator") {
          return <div key={item.key} className="context-menu__separator" role="separator" />;
        }
        return (
          <button
            key={item.key}
            type="button"
            role={item.checked === undefined ? "menuitem" : "menuitemradio"}
            aria-checked={item.checked}
            title={item.title}
            disabled={item.disabled}
            className={`context-menu__item${item.danger ? " context-menu__item--danger" : ""}${item.variant ? ` context-menu__item--${item.variant}` : ""}`}
            onClick={(event) => {
              event.stopPropagation();
              if (!item.disabled) item.onSelect();
            }}
          >
            {item.icon}
            <span>{item.label}</span>
          </button>
        );
      })}
    </div>,
    document.body,
  );
}
