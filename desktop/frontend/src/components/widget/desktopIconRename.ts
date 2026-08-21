import type { DesktopIconItem } from "../../lib/bridge";

// 只有携带可用 SessionRef.SessionPath 的 task 图标才可能改名。后端 rename
// 动作要求 item.SessionRef.SessionPath 非空，缺失时必然返回 invalid；菜单据此
// 隐藏该入口，避免呈现一个注定失败的按钮。纯函数、可测试。
export function canRenameTaskIcon(item: DesktopIconItem): boolean {
  if (item.kind !== "task") return false;
  return Boolean(item.sessionRef?.sessionPath?.trim());
}
