export function isSidebarMenuShortcut(key: string, shiftKey: boolean): boolean {
  return key === "ContextMenu" || (shiftKey && key === "F10");
}
