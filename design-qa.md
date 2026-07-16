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

# Design QA — 小组件左侧点阵信息轮播

- Source visual truth：`docs/assets/widget-mode/widget-info-token-target.png`、`widget-info-pet-target.png`、`widget-info-system-target.png`
- Implementation screenshots：`docs/assets/widget-mode/implementation-widget-info-token.png`、`implementation-widget-info-pet.png`、`implementation-widget-info-system.png`、`implementation-widget-info-model.png`、`implementation-widget-info-context.png`
- Viewport：`590 × 176`；内部逻辑画布沿用 `200%` 尺寸并整体缩放 `50%`
- States：TOKEN、时钟、电子宠物、IDLE、系统状态、模型标志、任务上下文抢占

## Full-view comparison evidence

- 三组目标图与对应实现截图已在同一比较输入中成对检查。机壳、左侧约 25% 信息区、主消息区、扫描线、主窗口键及青黄配色保持稳定。
- TOKEN 使用 Doto 点阵字体显示 `12.84M`；宠物使用真实六帧透明图集；系统页按 NET / CPU / MEM 三行展示；模型页显示真实品牌标志和当前模型名。

## Focused interaction evidence

- 点击左侧整区按 TOKEN → 时钟 → 宠物 → IDLE → 系统 → 模型循环；系统或模型不可用时自动跳过。
- 新 `message id + revision` 只触发一次任务上下文。点击返回信息页后等待超过一次 800ms 轮询，页面仍保持用户选择；同 revision 不重复抢占。
- 模型标志每 3 秒内部轮换，主轮播不自动切页；reduced-motion 下停止轮换。按钮具备唯一 accessible name 和可见焦点环。

## Comparison history

- Pass 1：发现宠物图集按容器比例拉伸导致角色偏扁；品牌 SVG 的 mask 简写覆盖了 mask-image，显示为黄色方块，均记为 P2。
- Fix：图集改为保持原始纵横比并按帧中心裁切；品牌标志改为项目内真实 SVG + 稳定 class mask。
- Pass 2：宠物轮廓、品牌图标、TOKEN 精度和任务上下文保持行为完成复拍；P0/P1/P2 清零。

## Engineering evidence

- `go test . -run 'TestWidget(Info|System|Model|Snapshot)|TestNextWidgetIdleSince' -count=1`：通过。
- `npm.cmd run test:widget-info`、`npm.cmd run typecheck`、`npm.cmd run build`：通过。
- 浏览器 console error/warning：0。
- `go test . -count=1` 的仓库全量桌面测试仍有既有 Darwin 路径、Windows topic-tree/临时锁失败；失败文件与本功能无交集，专项测试独立通过。

final result: passed

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

# Design QA — 小组件大字号与溢出滚屏

- Source visual truth：`docs/assets/widget-mode/widget-idle-new-conversation.png`（用户反馈字体过小的上一版实现）。
- Implementation screenshots：`docs/assets/widget-mode/widget-large-type-idle.png`、`widget-large-type-ticker.png`。
- Viewport：`590 × 142`；状态：无待处理消息、长错误消息。
- Full-view comparison：机壳、九宫格、身份区、操作区和右上模式键的位置未变化；主信息从约 `17px` 上限提升到约 `23.5px` 上限，项目名、状态、按钮与输入文字同步放大，信息区仍保持单条消息。
- Focused typography comparison：上一版主信息与次级文字接近，放大版建立清晰层级；主信息最醒目，项目名第二，状态/剩余数/说明文字继续弱化。没有出现两行挤压、按钮裁切或身份区溢出。
- Motion evidence：短状态消息测得 `textWidth=656 < frameWidth=768`，保持静止；长错误消息测得 `textWidth=794 > frameWidth=768`，组件进入 moving 状态，变换从 `translateX(0)` 到 `translateX(-26px)`。动画包含首尾停留与缓慢往返，reduced-motion 下关闭。
- Typography：通过；使用既有字体链，字号、字重和层级明显改善。
- Spacing/layout rhythm：通过；未改机壳与区域几何，输入聚焦时容器 `scrollTop=0`。
- Colors/tokens：通过；沿用青、黄、红与石墨黑 token。
- Image/assets：通过；九宫格、W2 与标尺资源未替换、未拉伸。
- Copy/content：通过；消息内容与状态语义未变化。
- Comparison history：首轮发现长消息需要移动验证；使用实际 error mock 确认溢出检测与位移生效，无 P0/P1/P2 遗留。
- 浏览器控制台：无 error。

final result: passed

# Design QA — 任务消息点击分页 / 状态滚动

- Source visual truth：`docs/assets/widget-mode/widget-large-type-ticker.png`（同尺寸、同 error 状态的大字号滚屏版）。
- Implementation screenshots：`docs/assets/widget-mode/widget-message-page-1.png`、`widget-message-page-2.png`。
- Viewport：`590 × 142`；状态：长错误消息，`还有 3 条`主要信息待看。
- Full-view comparison：机壳、身份区、状态、剩余主要消息数、操作区与主窗口键完全保持；任务正文从自动横移改为用户控制的单行分页。
- Focused interaction evidence：消息实测拆为 2 页；第一页显示“依赖暂时不可用，任务已保留现场，”与唯一 `下一页 ›` 提示，点击后 `data-page-index` 从 `0` 变为 `1`，第二页显示“可以安全重试。”并隐藏翻页提示。
- Fonts/typography：通过；沿用大字号层级，分页避免阅读过程中正文自行移动。
- Spacing/layout rhythm：通过；`下一页`占用消息行右侧保留区，不挤压状态和底部任务操作。
- Colors/tokens：通过；翻页提示复用黄色强调色，未增加新色。
- Image/assets：通过；九宫格、W2、标尺和图标资源未改变。
- Copy/content：通过；分页保持原消息字符与标点，没有显示页码或额外列表。
- Accessibility：通过；有下一页时消息区暴露为 button，支持点击、Enter、Space；最后一页移除 button 语义。
- 状态策略：运行/空闲/路由确认继续使用 `TickerText`，只在溢出时滚动；任务消息统一使用 `PagedText`。
- Comparison history：首轮发现 aria-label 在页尾标点后产生重复逗号，已改为空格连接并重新验证；无 P0/P1/P2 遗留。
- 浏览器控制台：无 error。

final result: passed

---

# Design QA — 小组件默认高度、空闲停帧与工作区选择

- Source visual truth：`D:/Temp/codex-clipboard-343ff3d5-a2d5-4870-8493-390cfe653610.png`
- Implementation screenshots：`docs/assets/widget-mode/widget-idle-workspace-selector.png`、`widget-workspace-menu-open.png`、`widget-compose-manual-workspace.png`
- Viewport：`590 × 176`；内部逻辑画布按既有 `50%` 比例显示
- States：完全空闲、工作区菜单展开、手动选择 WorkGround2 后输入与提交

## Full-view comparison evidence

- 参考图和三个实现状态已在同一比较输入中检查。机壳九宫格、左侧身份区、状态区、右上主窗口键及青黄配色保持一致。
- 默认高度从 `142px` 提高到 `176px`，宽度维持 `590px`。旧默认几何只在精确匹配 `590 × 142` 时迁移，窗口底边位置保持稳定；用户自定义尺寸不覆盖。
- 新对话入口仍是单一黄色主操作。工作区选择器紧邻入口，不常驻展示列表；展开时向上浮出，完整落在窗口内。

## Focused comparison evidence

- 空闲态：DOM 带 `widget-mode--idle`，扫描、状态条和装饰跑马动画的计算样式均为 `animation: none`。
- 工作区菜单：展开后可见“自动选择”“WorkGround2”“CICDBOT”“Global”，当前值以单选语义标记；菜单物理范围未越过 `590 × 176` 视口。
- 输入态：选择 WorkGround2 后，选择器显示 `WorkGround2 / 工作区`，占位文案变为“输入任务，将发送到 WorkGround2…”。

## Fidelity surfaces

- Fonts and typography：输入字号修正为逻辑 `36px`、物理约 `18px`；状态、项目名、主信息和按钮层级清晰。
- Spacing and layout rhythm：增加高度后正文与底部操作不拥挤；菜单、输入框和按钮没有裁切或重叠。
- Colors and visual tokens：继续使用石墨黑、青色、酸性黄和既有焦点描边。
- Image quality and assets：继续使用原九宫格 PNG、W2 与标尺图素，没有新增近似绘制资源或拉伸。
- Copy and content：“自动选择”明确表达智能路由；手动选择后同时更新按钮值、占位文案和提交回执。

## Interaction evidence

- 点击工作区选择器可展开；选择 WorkGround2 后菜单关闭且选择值保持。
- 点击“新对话”后可直接输入；提交后 mock 回执显示“新对话已创建 / 手动选择 / 已交给 WorkGround2”。
- `Escape` 可关闭展开菜单；菜单使用 `menu` / `menuitemradio` 和 `aria-checked`。
- 浏览器控制台 error：0。

## Comparison history

- Pass 1：发现输入样式使用无效的 `font: 36px/1.2 inherit`，浏览器实际物理字号约 `13.33px`，属于 P2 可读性问题。
- Fix：改为显式 `font-family`、`font-size`、`line-height`，并同步修正按钮字体声明。
- Pass 2：输入物理字号约 `18px`，菜单、输入和路由回执均完成复验；P0/P1/P2 清零。

## Engineering evidence

- Widget 专项 Go 测试与 `go vet .`：通过。
- 前端 TypeScript、CSS 语法检查和生产构建：通过。
- 旧回执缺失 workspace 选择字段时按 Auto 兼容；手动选择、无效选择和短暂 workspace 均有显式测试。

final result: passed
