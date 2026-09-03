import { Bookmark, Code2, Folder, FolderOpen, SquareTerminal, Star, Zap } from "lucide-react";
import { isWorkspaceMatteIcon, projectIconKey } from "../lib/projectIcons";
import { WorkspaceMatteIcon } from "../components/widget/WorkspaceMatteIcon";

export function ProjectGlyph({ icon, open = false, size = 16 }: { icon?: string; open?: boolean; size?: number }) {
  const key = projectIconKey(icon);
  if (isWorkspaceMatteIcon(key)) return <WorkspaceMatteIcon icon={key} className="session-sidebar__group-matte" />;
  switch (key) {
    case "star": return <Star size={size} aria-hidden="true" />;
    case "bookmark": return <Bookmark size={size} aria-hidden="true" />;
    case "code": return <Code2 size={size} aria-hidden="true" />;
    case "terminal": return <SquareTerminal size={size} aria-hidden="true" />;
    case "bolt": return <Zap size={size} aria-hidden="true" />;
    default: return open ? <FolderOpen size={size} aria-hidden="true" /> : <Folder size={size} aria-hidden="true" />;
  }
}
