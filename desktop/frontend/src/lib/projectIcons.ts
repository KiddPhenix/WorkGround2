export const PROJECT_ICON_OPTIONS = ["", "star", "bookmark", "code", "terminal", "bolt"] as const;

export const WORKSPACE_MATTE_ICON_OPTIONS = [
  { key: "folder", label: "文件夹" },
  { key: "new", label: "新建" },
  { key: "delegate", label: "委托" },
  { key: "browser", label: "浏览器" },
  { key: "build", label: "构建" },
  { key: "cmd", label: "命令行" },
  { key: "cpp", label: "C++" },
  { key: "csharp", label: "C#" },
  { key: "dart", label: "Dart" },
  { key: "data", label: "数据" },
  { key: "database", label: "数据库" },
  { key: "design", label: "设计" },
  { key: "discussion", label: "讨论" },
  { key: "document", label: "文档" },
  { key: "edit", label: "编辑" },
  { key: "game", label: "游戏" },
  { key: "go", label: "Go" },
  { key: "java", label: "Java" },
  { key: "javascript", label: "JavaScript" },
  { key: "music", label: "音乐" },
  { key: "php", label: "PHP" },
  { key: "presentation", label: "演示" },
  { key: "publish", label: "发布" },
  { key: "python", label: "Python" },
  { key: "react", label: "React" },
  { key: "research", label: "搜索研究" },
  { key: "run", label: "运行" },
  { key: "rust", label: "Rust" },
  { key: "sport", label: "运动" },
  { key: "sync", label: "同步协作" },
  { key: "test", label: "测试" },
  { key: "typescript", label: "TypeScript" },
  { key: "unity", label: "Unity" },
  { key: "video", label: "视频" },
] as const;

export type WorkspaceMatteIconKey = (typeof WORKSPACE_MATTE_ICON_OPTIONS)[number]["key"];
export type ProjectIconKey = (typeof PROJECT_ICON_OPTIONS)[number] | WorkspaceMatteIconKey;

const workspaceMatteKeys = new Set<string>(WORKSPACE_MATTE_ICON_OPTIONS.map((option) => option.key));

export function isWorkspaceMatteIcon(value: string | undefined): value is WorkspaceMatteIconKey {
  return workspaceMatteKeys.has((value ?? "").trim().toLowerCase());
}

export function projectIconKey(value: string | undefined): ProjectIconKey {
  const normalized = (value ?? "").trim().toLowerCase();
  if (PROJECT_ICON_OPTIONS.includes(normalized as (typeof PROJECT_ICON_OPTIONS)[number])) return normalized as ProjectIconKey;
  return isWorkspaceMatteIcon(normalized) ? normalized : "";
}
