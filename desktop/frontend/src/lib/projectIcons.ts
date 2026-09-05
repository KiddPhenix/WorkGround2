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
  { key: "ai", label: "人工智能" },
  { key: "analytics", label: "数据分析" },
  { key: "android", label: "Android" },
  { key: "api", label: "API" },
  { key: "archive", label: "归档" },
  { key: "audio", label: "音频" },
  { key: "automation", label: "自动化" },
  { key: "backend", label: "后端" },
  { key: "backup", label: "备份" },
  { key: "blockchain", label: "区块链" },
  { key: "book", label: "知识库" },
  { key: "bug", label: "缺陷" },
  { key: "calendar", label: "日历" },
  { key: "camera", label: "相机" },
  { key: "cloud", label: "云服务" },
  { key: "commerce", label: "商务" },
  { key: "container", label: "容器" },
  { key: "dashboard", label: "仪表盘" },
  { key: "deploy", label: "部署" },
  { key: "devops", label: "DevOps" },
  { key: "email", label: "邮件" },
  { key: "finance", label: "财务" },
  { key: "frontend", label: "前端" },
  { key: "globe", label: "全球化" },
  { key: "hardware", label: "硬件" },
  { key: "health", label: "健康" },
  { key: "image", label: "图像" },
  { key: "ios", label: "iOS" },
  { key: "kotlin", label: "Kotlin" },
  { key: "lightbulb", label: "灵感" },
  { key: "linux", label: "Linux" },
  { key: "lock", label: "锁定" },
  { key: "lua", label: "Lua" },
  { key: "markdown", label: "Markdown" },
  { key: "mobile", label: "移动端" },
  { key: "network", label: "网络" },
  { key: "note", label: "笔记" },
  { key: "notification", label: "通知" },
  { key: "package", label: "包管理" },
  { key: "performance", label: "性能" },
  { key: "planning", label: "规划" },
  { key: "plugin", label: "插件" },
  { key: "qa", label: "质量保障" },
  { key: "report", label: "报告" },
  { key: "robot", label: "机器人" },
  { key: "rocket", label: "启动" },
  { key: "science", label: "科学" },
  { key: "security", label: "安全" },
  { key: "server", label: "服务器" },
  { key: "settings", label: "设置" },
  { key: "shopping", label: "购物" },
  { key: "social", label: "社交" },
  { key: "spreadsheet", label: "表格" },
  { key: "support", label: "支持" },
  { key: "travel", label: "旅行" },
  { key: "user", label: "用户" },
  { key: "vault", label: "保险库" },
  { key: "web", label: "Web" },
  { key: "workflow", label: "工作流" },
  { key: "writing", label: "写作" },
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

// workspaceMatteIconKey resolves any stored project icon to a matte catalog key,
// treating legacy Lucide keys and the empty default as the folder fallback so the
// sidebar's matte-only picker always has a visible current selection.
export function workspaceMatteIconKey(value: string | undefined): WorkspaceMatteIconKey {
  const key = projectIconKey(value);
  return isWorkspaceMatteIcon(key) ? key : "folder";
}
