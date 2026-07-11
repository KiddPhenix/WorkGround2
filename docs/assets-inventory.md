# WorkGround2 图片/图标元素清单

> 生成日期: 2025-07-07 | 基于 `desktop/frontend/src` 及项目全量扫描

---

## 一、静态图片资源（PNG/SVG 文件）

### 1.1 应用图标 / Logo 文件

| 文件路径 | 原始尺寸 | 使用系统 | 显示尺寸 |
|---|---|---|---|
| `desktop/build/appicon.png` | 1024×1024 | 桌面应用图标（Go `//go:embed` 嵌入，Unix 托盘图标、Linux 包安装到 `/usr/share/pixmaps/`） | 原始尺寸 |
| `desktop/build/appicon.svg` | 矢量 | 桌面应用图标 SVG 源文件 | — |
| `desktop/build/windows/icon.ico` | ICO（多尺寸） | Windows 托盘图标 + NSIS 安装程序图标 | 系统决定 |
| `desktop/build/darwin/icon.icns` | ICNS（多尺寸） | macOS 应用图标 | 系统决定 |
| `desktop/build/linux/icons/hicolor/` | 16/24/32/48/64/128/256/512px PNG + SVG | Linux 桌面图标（hicolor 主题，通过 nfpm 安装到 `/usr/share/icons/`） | 各尺寸 PNG |
| `desktop/frontend/src/assets/logo.png` | 512×512 | `OnboardingOverlay.tsx` 引导页 Logo | CSS: **36×36** (`.onboarding__logo`) |
| `desktop/frontend/src/assets/logo.svg` | 矢量 | logo 源文件（前端代码未直接引用） | — |
| `desktop/frontend/src/assets/logo-symbol.png` | 512×512 | `StartupSplash.tsx` 启动闪屏 Logo | CSS: **64×64** (`.startup-splash__mark img`) |
| `desktop/frontend/src/assets/logo-symbol.svg` | 矢量 | logo-symbol 源文件 | — |
| `desktop/frontend/src/assets/logo-wordmark.png` | 760×160 | `App.tsx` 侧边栏品牌 Logo | CSS: **136×32** (`.sidebar__brand-logo`) |
| | | `Welcome.tsx` 欢迎页品牌 Logo | CSS: **240×56** / 移动端 **224×52** (`--welcome-brand-width/height`) |
| `desktop/frontend/src/assets/logo-wordmark.svg` | 矢量 | logo-wordmark 源文件 | — |
| `desktop/frontend/src/assets/brand-concepts/` (10 个 PNG) | 各约 1-1.5MB | 品牌概念设计稿 — 前端代码未引用，仅供参考展示用 | — |

### 1.2 文档图片（`docs/`）

| 文件 | 说明 | 使用位置 |
|---|---|---|
| `docs/logo.svg` | 文档 Logo | `README.md` 头部 `<img>` |
| `docs/favicon.svg` | 网站 Favicon | 浏览器标签页 |
| `docs/claude-desktop-layout-preview.png` | Claude 桌面布局预览 | 文档截图 |
| `docs/desktop-v2-layout-preview.png` | 桌面 v2 布局预览 | 文档截图 |
| `docs/desktop-v2-skills-preview.png` | 桌面 v2 技能预览 | 文档截图 |
| `docs/project-topic-tabs-context.png` | 项目主题标签页 | 文档截图 |
| `docs/desktop-theme-dark.jpg` | 暗色主题截图 | 文档截图 |
| `docs/desktop-theme-light.jpg` | 亮色主题截图 | 文档截图 |
| `docs/cli-image-paste-effect.svg` | CLI 图片粘贴效果 | 文档图示 |
| `docs/cli-theme-effect.svg` | CLI 主题效果 | 文档图示 |
| `docs/paste-folding-effect.svg` | 粘贴折叠效果 | 文档图示 |
| `docs/slash-command-views.svg` | 斜杠命令视图 | 文档图示 |
| `docs/assets/agent-skill-sources.png` | 代理技能来源 | 文档图示 |
| `docs/assets/approval-authorization-mocks.svg` | 批准授权模拟 | 文档图示 |
| `docs/assets/issue-2605-plan-approval-shelf.png` | Issue 截图 | 文档图示 |
| `docs/assets/issue-2605-tool-approval-shelf.png` | Issue 截图 | 文档图示 |
| `docs/assets/bot-feishu-approval.svg` | 飞书机器人批准 | 文档图示 |
| `docs/assets/bot-lark-yolo.svg` | Lark YOLO 模式 | 文档图示 |
| `docs/assets/bot-qq-approval.svg` | QQ 机器人批准 | 文档图示 |
| `docs/assets/bot-weixin-text-commands.svg` | 微信文本命令 | 文档图示 |

### 1.3 站点公开资源（`site/public/`）

| 文件 | 用途 |
|---|---|
| `site/public/favicon.svg` | Astro 站点 Favicon |
| `site/public/logo.svg` | Astro 站点 Logo |
| `site/public/og.png` | Open Graph 社交分享卡片图片 |

### 1.4 其他

| 文件 | 用途 |
|---|---|
| `.github/sponsor/wechat-pay.jpg` | GitHub Sponsor 微信支付二维码 |

---

## 二、Lucide React 图标（矢量图标库）

> 依赖: `lucide-react ^1.21.0`  
> 所有图标通过 `size` 属性控制尺寸（单位 px），无自定义 SVG 组件

### 2.1 App.tsx — 主应用框架 + 侧边栏

| 图标名称 | 尺寸 | 使用位置 |
|---|---|---|
| `Minus` | 13 | 窗口最小化按钮 |
| `RestoreIcon` (自定义) | 12 | 窗口还原按钮 |
| `Square` | 11 | 窗口最大化按钮 |
| `X` | 13 | 窗口关闭按钮 |
| `MessageSquare` | 14, 18 | 侧边栏新建会话入口 / 区域标签 |
| `Settings as SettingsIcon` | 14, 15, 16 | 侧边栏设置入口 / 命令面板 |
| `SquarePen` | 15, 18 | 命令面板 / 侧边栏新建按钮 |
| `History` | 15, 16 | 命令面板 / 侧边栏历史入口 |
| `Trash2` | 15, 16 | 命令面板 / 侧边栏回收站 |
| `Palette` | 15 | 命令面板外观入口 |
| `Brain` | 14, 15 | 命令面板 / 侧边栏记忆入口 |
| `Cpu` | 15 | 命令面板模型入口 |
| `PanelLeft` | 14, 15 | 侧边栏折叠/展开 |
| `PanelRight` | 14, 15 | 侧边栏折叠/展开 / 右侧面板 |
| `Search` | 15 | 侧边栏搜索 |
| `AlarmClock` | 14, 15, 16 | 侧边栏定时任务入口 |
| `Command` | 14 | 侧边栏命令面板入口 |
| `Pencil` | 14 | 项目名编辑 |
| `Download` | 14 | 导出菜单 |
| `FileText` | 13 | 导出格式选择 |
| `FileJson` | 13 | 导出 JSON |
| `FileDown` | 13 | 导出下载 |
| `FileImage` | 13 | 导出图片 |
| `GitBranch` | 13, 14 | 状态栏分支信息 |
| `CircleHelp` | 14 | 帮助入口 |
| `Activity` | 13 | 状态栏活跃状态 |

### 2.2 AppChrome.tsx — 顶部 Chrome 栏

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `PanelLeft` | 16 | 侧边栏切换 |
| `PanelRight` | 16 | 右侧面板切换 |
| `Search` | 16, 18 | 搜索 / 命令面板触发 |

### 2.3 Composer.tsx — 输入编辑器

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `ArrowUp` | 16 | 发送按钮 |
| `SlidersHorizontal` | 17 | 设置齿轮 |
| `List` | 14, 16 | 模式选择 |
| `Target` | 14, 16 | 目标选择 |
| `Gauge` | 13, 14, 16 | 努力级别选择 |
| `Shield` | 14 | 权限级别指示 |
| `ShieldCheck` | 14 | 已批准指示 |
| `ShieldAlert` | 14 | 告警指示 |
| `Search` | 13 | 文件引用搜索 |
| `FileText` | 13, 15 | 文件引用图标 |
| `Folder` | 13 | 文件夹引用图标 |
| `MessageSquare` | 13, 15 | 会话引用图标 |
| `Eye` | 14 | 预览附件 |
| `Trash2` | 14 | 删除附件 |
| `X` | 11, 13 | 关闭/清除标签 |
| `Square` | 10 | 多文件选择框 |
| `CornerDownRight` | 13, 14 | 回复/分支指示 |
| `ChevronUp` | 13 | 折叠 |
| `ChevronDown` | 13 | 展开 |
| `ChevronsUpDown` | 11 | 下拉指示 |
| `Check` | 13 | 选中标记 |

### 2.4 Message.tsx — 消息显示

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `Image` | 15, 20 | 图片附件缩略图 |
| `FileText` | 15 | 文件附件图标 |
| `Folder` | 15 | 文件夹附件图标 |
| `MessageSquare` | 14 | 会话引用 |
| `BrainCircuit` | 14 | 推理过程 |
| `Pencil` | 14 | 编辑消息 |
| `GitBranch` | 13 | 分支信息 |
| `ScrollText` | 13 | 系统提示 |
| `RotateCcw` | 13 | 重试/回退 |
| `ChevronDown` | 12 | 展开指示 |
| `ChevronRight` | 12, 15 | 折叠指示 / 引用展开 |
| `ProcessBrainIcon` | 12 | 推理步骤指示 |

### 2.5 Transcript.tsx — 对话流

| 图标 | 说明 |
|---|---|
| `ArrowDown` | 滚动到底部 |
| `ChevronRight` | 折叠指示 |

### 2.6 TabBar.tsx — 标签栏

| 图标 | 说明 |
|---|---|
| `FileText` | 标签页图标 |
| `Plus` | 新建标签 |
| `Search` | 搜索标签 |
| `X` | 关闭标签 |

### 2.7 ProjectTree.tsx — 项目文件树

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `Folder` | — | 文件夹 |
| `FolderPlus` | — | 新建文件夹 |
| `FolderOpen` | 13 | 打开文件夹 |
| `FileText` | — | 文件图标 |
| `Search` | — | 搜索 |
| `Plus` | 13 | 新建 |
| `Pencil` | 13 | 重命名 |
| `Archive` | 13, 15 | 归档 |
| `History` | 13 | 历史 |
| `Copy` | 13 | 复制 |
| `XCircle` | 13 | 关闭/取消 |
| `Pin` | 13, 15 | 固定 |
| `Check` | 12, 13 | 选中 |
| `MessageSquare` | 11 | 关联会话 |
| `Clock` | — | 最近 |
| `MoreHorizontal` | — | 更多菜单 |
| `Minimize2` | — | 收起面板 |
| `Maximize2` | — | 展开面板 |
| `BriefcaseBusiness` | — | 工作区 |
| `ArrowDown` | — | 排序 |
| `ListCollapse` | — | 折叠列表 |
| `ListRestart` | — | 重置列表 |

### 2.8 SettingsPanel.tsx — 设置面板

| 图标 | 说明 |
|---|---|
| `Check` | 确认标记 |
| `CheckCircle2` | 成功标记 |
| `ChevronDown` | 展开 |
| `ChevronUp` | 折叠 |
| `Clipboard` | 复制 |
| `GripVertical` | 拖拽手柄 |
| `KeyRound` | API Key |
| `Loader2` | 加载中 |
| `Play` | 测试/播放 |
| `QrCode` | 二维码入口 |
| `RefreshCw` | 刷新 |
| `Send` | 发送测试 |

> 另外: 使用 `qrcode.react` 的 `<QRCodeSVG size={196}>` 渲染 Bot 安装二维码

### 2.9 MemoryPanel.tsx — 记忆面板

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `ChevronDown` | 15 | 展开 |
| `ChevronRight` | 15 | 折叠 |
| `FileText` | 15 | 文档图标 |
| `Search` | 14 | 搜索 |
| `Plus` | 13 | 添加记忆 |
| `Pencil` | 13 | 编辑 |
| `Trash2` | 13 | 删除 |
| `RefreshCw` | 13 | 刷新 |
| `Sparkles` | 13, 14 | AI 建议 |
| `Check` | 13 | 确认/已采纳 |

### 2.10 HistoryPanel.tsx — 历史面板

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `Search` | 13 | 搜索 |
| `RotateCcw` | 13 | 恢复 |
| `Trash2` | 13, 22 | 删除 / 空回收站占位 |
| `Pencil` | 13 | 重命名 |
| `Archive` | 13 | 归档 |

### 2.11 WorkspacePanel.tsx — 工作区面板

| 图标 | 说明 |
|---|---|
| `ChevronDown` | 树展开 |
| `ChevronRight` | 树折叠 |
| `FileText` | 文件 |
| `Folder` | 文件夹 |
| `FolderOpen` | 打开的文件夹 |
| `FolderTree` | 目录树 |
| `FolderX` | 空目录 |
| `GitBranch` | Git 分支 |
| `MessageSquarePlus` | 新建会话 |
| `Minimize2` | 缩小面板 |
| `Maximize2` | 放大面板 |
| `RefreshCw` | 刷新 |
| `Search` | 搜索 |
| `X` | 关闭 |

### 2.12 MermaidDiagram.tsx — 图表渲染

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `Play` | 14 | 渲染/运行 |
| `Code2` | 14 | 查看源码 |
| `ZoomOut` | 14 | 缩小 |
| `ZoomIn` | 14 | 放大 |
| `RotateCcw` | 14 | 重置 |
| `Minimize2` | 14 | 退出全屏 |
| `Maximize2` | 14 | 全屏 |
| `AlertCircle` | 14 | 错误提示 |

### 2.13 CapabilitiesPanel.tsx — 权限/能力面板

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `ShieldCheck` | 12, 13 | 已批准 |
| `ShieldOff` | 12 | 已拒绝 |

### 2.14 StatusBar.tsx — 状态栏

| 图标 | 说明 |
|---|---|
| `Activity` | 活跃状态 |
| `CircleDollarSign` | 费用 |
| `CircleGauge` | 性能 |
| `Database` | 数据 |
| `Folder` | 工作目录 |
| `GitBranch` | 分支 |
| `Layers` | 层级 |
| `Percent` | 百分比 |
| `RefreshCw` | 刷新 |
| `Wallet` | 钱包/余额 |
| `Zap` | 快速操作 |

### 2.15 HeartbeatPanel.tsx — 心跳/定时任务（自定义功能）

| 图标 | 尺寸 | 说明 |
|---|---|---|
| `Activity` | 16 | 面板图标 |
| `Heart` | 24 | 心跳动画 / 空状态 |
| `ChevronLeft` | 16 | 返回 |
| `X` | 12, 16 | 关闭 |
| `Search` | 13 | 搜索 |
| `ChevronsUpDown` | 12 | 下拉 |
| `Check` | 12 | 选中 |
| `Plus` | 14 | 添加任务 |
| `Clock` | 10 | 时间 |
| `Play` | 12 | 触发执行 |
| `MessageSquare` | 13 | 关联会话 |
| `Trash2` | 13 | 删除 |

### 2.16 小型通用组件

| 组件 | 图标 | 尺寸 | 说明 |
|---|---|---|---|
| `CopyButton.tsx` | `Check`, `Copy` | 13 | 复制按钮 |
| `InlineDiff.tsx` | `Check`, `Copy` | 11 | 复制差异 |
| | `ChevronDown`, `ChevronRight` | 11 | 折叠/展开差异 |
| `ModalCloseButton.tsx` | `X` | 15 | 模态框关闭 |
| `EffortSwitcher.tsx` | `Gauge` | 13 | 努力级别图标 |
| | `ChevronsUpDown` | 11 | 下拉 |
| | `Check` | 13 | 选中 |
| `ModelSwitcher.tsx` | `Brain` | 11, 13 | 模型图标 |
| | `ChevronsUpDown` | 11 | 下拉 |
| | `Check` | 13 | 选中 |
| | `Search` | 13 | 搜索 |
| `SoundSelect.tsx` | `Check`, `ChevronDown`, `Play` | — | 声音选择 |
| `ToolCard.tsx` | `ChevronRight` | — | 工具卡片折叠 |
| `ToolGroup.tsx` | `ChevronRight` | — | 工具组折叠 |
| `ReadOnlyBatch.tsx` | `ChevronRight` | — | 只读批处理展开 |
| `FileReferenceMenu.tsx` | `FileText`, `Folder` | 13 | 文件引用菜单 |
| `ComposerContextCard.tsx` | `FileText`, `Folder`, `MessageSquare` | 15, 20 | 上下文卡片图标 |
| | `X` | 13, 14 | 移除上下文 |
| `CommandPalette.tsx` | `Command` | 15 | 命令占位图标 |
| | `Search` | 18 | 搜索图标 |

---

## 三、音效文件（`.wav`）

| 文件 | 大小 | 用途（`lib/sound.ts`） |
|---|---|---|
| `desktop/frontend/public/sounds/mixkit-positive-notification-951.wav` | ~482 KB | `positive` — 正面通知音 |
| `desktop/frontend/public/sounds/mixkit-correct-answer-tone-2870.wav` | ~338 KB | `correct` — 正确答案音 |
| `desktop/frontend/public/sounds/mixkit-software-interface-start-2574.wav` | ~404 KB | `start` — 界面启动音 |
| `desktop/frontend/public/sounds/mixkit-software-interface-back-2575.wav` | ~229 KB | `back` — 返回音 |

---

## 四、动态图片（运行时附件缩略图）

| 位置 | 显示尺寸 | CSS 类 | 说明 |
|---|---|---|---|
| `Message.tsx` — 消息附件 | 56×56 | `.msg-attachment__icon--image` | 消息中的图片附件缩略图 |
| `ComposerContextCard.tsx` — 输入框上下文 | 56×56 / 68×68 | `.composer-context__thumb` | 普通模式 56×56；纯图片模式 68×68 |
| `SettingsPanel.tsx` — QR 码 | 196×196 | `<QRCodeSVG size={196}>` | Bot 安装二维码（qrcode.react 生成） |
| `SettingsPanel.tsx` — 安装二维码图 | 无显式 CSS | `<img src={installQrURL}>` | 动态安装二维码图片 |
| `WorkspacePanel.tsx` — 文件预览 | `max-width:100%; height:auto` | `.workspace-media--image img` | 工作区媒体预览 |

---

## 汇总统计

| 类别 | 数量 |
|---|---|
| 静态 PNG 图片（前端引用） | 4 个 |
| 品牌概念稿 PNG（未引用） | 10 个 |
| 静态 SVG 图片（logo/appicon + 文档 + site） | ~12 个 |
| 平台图标文件（ICO / ICNS / 多尺寸 PNG） | 11 个 |
| Lucide React 图标组件（去重后） | **73 个** |
| 引用 Lucide 图标的 TSX 文件 | **27 个** |
| 图标尺寸范围 | **10px ~ 24px**（常用: 13/14/15px） |
| 音效文件 | 4 个 `.wav` |
| 文档截图/图示 | ~20 个 |
