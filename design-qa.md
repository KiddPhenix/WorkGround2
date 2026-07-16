# Design QA — 全局会话状态

- source visual truth path: `D:\Temp\codex-clipboard-4ae37805-8cc7-47b9-a37b-84e882eae597.png`
- implementation screenshot path: in-thread Windows Graphics Capture artifact `screenshot-0` from `D:\Work\WorkGround2\desktop\build\bin\WorkGround2-QA.exe`
- viewport: source crop `698 × 392`; implementation window `1488 × 935`
- state: dark theme; running `2` (including one CLI session); needs attention `2`; running list expanded; AddOn closed

## Full-view comparison evidence

The source crop and implementation capture were emitted together in one comparison input. The two global controls sit immediately left of the existing AddOn button and remain clear of the command and native window controls. The implementation preserves the existing workbench header height, dark surface, border, radius, and spacing tokens.

## Focused region comparison evidence

The source itself is a focused top-right crop. In the implementation capture, the matching top-right region shows:

- a separate `运行中 2` status with an amber dot;
- a single merged `待关注 2` button with the Sparkles icon;
- the existing AddOn button directly to their right;
- an inward-opening running-session list that does not collide with native window controls.

No additional crop was needed because the source is already a focused component reference and the implementation controls are legible at native scale.

## Findings

No actionable P0/P1/P2 differences.

- Fonts and typography: existing WorkGround2 UI font, 12 px control text, tabular count numerals, and compact weights are consistent with adjacent header controls.
- Spacing and layout rhythm: both controls are 32 px high, use the existing 8 px action gap, and align with AddOn.
- Colors and visual tokens: borders, background, text, warning accent, focus ring, and disabled opacity use existing semantic tokens.
- Image quality and asset fidelity: no raster imagery is involved; the Sparkles icon uses the existing Lucide icon library, while the running state uses a small semantic status dot.
- Copy and content: `运行中` and `待关注` match the requested language; the expanded list includes session title, workspace, CLI source, and relative run time.

## Interaction evidence

- Component test verifies running is hidden at zero, includes CLI sessions, and opens its list only from hover/focus state.
- Component test verifies attention excludes CLI sessions and jumps to the earliest attention timestamp.
- Native Wails capture verifies the default header state and expanded running list visually.

## Comparison history

- Pass 1: no P0/P1/P2 findings; no visual corrections required after comparison.

## Follow-up polish

- P3: the disabled `待关注 0` state is intentionally subdued; its contrast can be raised later if user testing finds it too quiet.

final result: passed

---

# Design QA — 传呼机小组件半尺寸与透明切角

- Source visual truth: `D:/Temp/codex-clipboard-c05887a1-0fdf-47ed-ad37-4cbb8450203d.png` 与已确认的 `docs/assets/widget-mode/implementation-idle.png`
- Implementation screenshot: `docs/assets/widget-mode/implementation-idle-half-transparent.png`
- Viewport: 原生窗口 `590 × 142`；内部逻辑画布 `1180 × 284`，统一缩放 `50%`
- State: 空闲；真实桌面包另验证错误状态与固定“返回窗口”

## Full-view comparison evidence

- 已把原 `1180 × 284` 确认稿和新 `590 × 142` 实现放入同一比较输入；身份区、分隔线、消息基线、扫描线与返回按钮保持同一相对位置，未出现单独缩字或九宫格变形。
- 新窗口长宽均为旧版的 `50%`，显示面积为旧版的 `25%`。

## Focused region evidence

- 四角为本轮聚焦区域。浏览器计算样式确认 `html`、`body`、`#root` 与 `.widget-mode` 背景均为透明，旧 `.app` 在小组件模式隐藏；`.widget-shell` 使用八点切角裁剪。
- 隔离 Wails production 包在 Windows 原生窗口中实测为 `590 × 142`，四个裁掉的三角区域直接显示后方窗口内容，没有矩形黑底；返回窗口按钮保持可见。

## Fidelity surfaces

- Typography: 字体、字重、行距随整个逻辑画布等比缩放，信息层级与确认稿一致。
- Spacing/layout: 原布局整体缩小 `50%`，没有局部重排、溢出或内容裁切。
- Colors/tokens: 石墨黑、青色、酸性黄不变；透明只作用于机壳外切角。
- Image quality/assets: 继续使用九张 PNG 底图、独立标尺和 W2 图素；缩放发生在完整逻辑画布，九宫格接缝未被放大。
- Copy/content: 状态、主信息和“返回窗口”文案保持不变。

## Comparison history

- Pass 1: 发现浏览器截图合成器会用暗色填充透明页面，无法单独证明 Windows 原生透明度；实现样式和 Wails 透明选项均已就位。
- Pass 2: 构建并启动跳过单实例锁的隔离 production 包；原生截图确认窗口尺寸和四角透出，P0/P1/P2 清零。

## Engineering evidence

- `go test . -run "Widget|QueueNeedsAttentionIncludesActiveTab" -count=1`：通过。
- `npm.cmd run typecheck`：通过。
- `wails build -skipbindings -o WorkGround2-widget-half-test.exe`：通过。
- 浏览器控制台 error/warning：0。

final result: passed

---

# Design QA — 传呼机小组件模式

- Source truth: `docs/assets/widget-mode/state-choice.png`、`state-typed-reply.png`、`state-result.png`、`state-running.png`
- Implementation: `docs/assets/widget-mode/implementation-choice.png`、`implementation-reply.png`、`implementation-result.png`、`implementation-idle.png`、`implementation-error.png`
- Viewports: `1180 × 284` 主验收，`960 × 260` 响应式验收
- States: 点选、键入、完成、错误、运行/空闲

## 同输入对照与修正

- 参考图与实现截图已在同一比较输入中逐态对照。
- 首轮发现 W2 偏大、按钮过于方正、运行状态使用黄色、完成态主操作顺序偏离参考；已分别缩小并左上偏置 W2、改为切角按钮、运行态改青色、把“下一条”设为黄色主操作。
- 原生窗口初始 `MinHeight=480` 会阻止窗口缩到 284px；进入/退出时现已动态切换为 `760 × 220` / `760 × 480` 最小尺寸。

## 保真与可用性

- Layout: 九宫格机壳、左侧上下文、固定返回按钮和中央消息基线在各状态不跳动；同一时刻只显示一条主要信息。
- Typography: 项目名明显强于任务名；主要信息保留最大文字空间，长错误使用更小字号并截断。
- Color: 石墨黑、青色和酸性黄与确认稿一致；错误使用红色，健康运行使用青色。
- Assets: 机壳使用九张真实 PNG 切片；标尺和 W2 使用独立透明 PNG。原尺寸九宫格重组抽样像素差为 0。
- Interaction: 点选与键入发送成功；“稍后”轮转但不丢项；固定“返回窗口”不确认当前消息；失败重试保留输入并复用 request ID。
- Accessibility: 语义按钮、输入标签、`aria-live`、完整剩余数读屏文案、焦点环、Esc 退出输入焦点和 reduced-motion 均已覆盖。
- Responsiveness: `960 × 260` 页面 scroll size 等于 viewport，按钮和返回控制无重叠。

## 工程证据

- `go test . -run "Widget|QueueNeedsAttentionIncludesActiveTab" -count=1`：通过。
- `npm.cmd run typecheck`：通过。
- `npm.cmd run build`：通过。
- `wails build -skipbindings -o WorkGround2-widget-test.exe`：通过。
- 桌面全量既有测试出现 Windows 临时目录清理和 Topic tree 时序失败；均不涉及本功能改动，专项测试与生产构建独立通过。

## 结论

P0 / P1 / P2：0。原生桌面包可构建；当前机器已有另一 WorkGround2 worktree 实例占用单实例锁，因此未强行关闭用户现有实例做第二次原生窗口截图。

final result: passed

---

# Design QA — Request Help 状态卡

- Source truth: `docs/assets/request-help-display-reference.png`
- Implementation: `docs/assets/request-help-display-implementation.png`
- Responsive evidence: `docs/assets/request-help-display-narrow.png`
- State: `deepseek/deepseek-pro` 请求网页搜索，`codex/codex-cli` 第 1/2 次接管执行
- Viewports: 1100×420（主视图）、600×360（窄屏）

## 对比与修正

- 首轮同输入全视图对比发现标题和接管状态不够醒目；已将活动标题调整为主题橙色，并将“已接管”改为绿色状态徽标。
- 聚焦区域为单一状态卡，主视图已完整覆盖，无需额外裁切证据。
- 参考图是视觉方向稿；实现按现有对话流密度压缩高度，保留左侧活动轨、模型路由、能力、尝试次数和接管状态。未复制方向稿中的装饰性长进度轨，避免对未知执行时长作虚假进度表达。

## 五项保真检查

- Layout: 内联卡片层级、模型路由和元信息顺序一致；窄屏无横向溢出。
- Typography: 标题、模型名、辅助信息按现有桌面字体层级实现。
- Color: 沿用产品暗色 token、橙色活动强调和绿色完成/接管语义。
- Shape: 圆角、细边框、左侧活动轨与现有工具卡体系一致。
- Interaction: “详情”按钮可展开原始参数；`aria-live` 暴露状态变化。

## 工程验收

- 浏览器：详情按钮唯一且可展开；600px 下页面 `scrollWidth` 等于视口宽度；控制台无 error/warning。
- 自动化：Go agent/desktop 专项测试、Go vet、前端 17 项状态展示测试、typecheck、生产构建通过。
- 严重问题：P0/P1/P2 均为 0。

final result: passed

# Design QA — 小组件新对话与主窗口模式键

- Viewport：`590 × 142` 原生尺寸；浏览器实际最小高度约束为 `160px`，内部逻辑画布仍按 `50%` 缩放。
- 实现截图：`docs/assets/widget-mode/widget-idle-new-conversation.png`、`widget-new-conversation-compose.png`；路由确认态使用 DOM 文案与交互状态验收。
- 返回控件：改为紧凑的黄色 `PanelTopOpen` 图标区 + `主窗口 / FULL VIEW` 双行文案；所有状态固定右上角，accessible name 为“返回主窗口”。
- 空闲态：只新增一个“新对话 / 自动选择工作区”入口；无列表、无 workspace 选择器。
- 输入态：唯一主要信息为“想让 WorkGround2 做什么？”，单行输入支持 Enter 发送、Esc/取消退出；修复聚焦输入时 overflow 容器自动滚动导致机壳上移的问题，`.widget-mode` 使用 `overflow: clip`。
- 路由确认态：DOM 验证同一时刻只出现 `已交给 WorkGround2`、`名称匹配` 与当前输入摘要；随后进入聚合运行状态。
- 交互验收：实际点击“新对话”、键入任务并发送，浏览器 mock 返回自动路由到 WorkGround2；按钮禁用态、焦点和路由反馈正常。
- 控制台：无 error。
