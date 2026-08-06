# Design QA — Room Agent 运行动态浮窗（2026-08-04）

- Source visual truth: `D:\Temp\codex-clipboard-a85375df-70ad-4c2d-94bc-c90be88a8509.png`
- Implementation screenshot: `D:\Temp\workground2-room-agent-activity-hover.png`
- Source pixels: `410 × 142`；实现截图 pixels: `1280 × 720`
- Implementation CSS viewport: `1280 × 720`；devicePixelRatio: `1.375`；浏览器截图后端按 CSS 像素输出
- State: 右栏一名在线成员的 Agent 为 `running`；“运行中”获得 hover/focus 后显示最近输入输出浮层
- Normalization: 参考图只提供成员行静态状态且没有目标浮层，因此同一比较输入用成员行校验原视觉基线，用实现截图的聚焦区域验证用户描述的新增悬停态；未对不同画布做伪精确缩放比较

## Findings

没有可执行的 P0/P1/P2 问题。

- 字体与层级：成员名、Agent 名、运行状态沿用原字号与字重；浮层标题、输入/输出标签、时间和正文形成四级层次，长正文四行截断。
- 间距与布局：成员行结构未改变；浮层宽 `326px`、高 `187px`，实际边界 `left=904/right=1230/top=248/bottom=435`，完整位于 `1280 × 720` 视口内，没有被右栏裁剪。
- 色彩与 token：继续使用 Room 深色面板、蓝色运行态和紫色输出语义；边框与阴影强度低于主内容，不抢占时间线层级。
- 图片与图标：没有新增位图或近似绘制资源；头像沿用现有成员头像，浮层使用项目现有 Lucide `Bot` 图标。
- 文案：浮层明确标记“最近 Agent 动态”“输入/输出”、时间及 `当前/总数`，中英繁中均已补齐。

## Full-view comparison evidence

参考截图与实现悬停态已在同一比较输入中打开。成员行的头像、姓名、Agent 名、蓝色运行状态和右箭头保持原布局与视觉语言；新增浮层从运行状态下方展开，不改变成员行尺寸，也不覆盖主内容区的核心操作。

## Focused interaction evidence

- 实际点击可聚焦的“运行中”状态后，唯一 `role=tooltip` 浮层出现；源码同时覆盖鼠标 `onMouseEnter/onMouseLeave`。
- 等待 3 秒后，内容从 `1 / 5 · 输出 · 已完成布局测量…` 自动轮播为 `2 / 5 · 输入 · 继续验证悬停浮层…`。
- Window resize 或任意滚动会关闭浮层，防止锚点位置漂移；Agent 离线或退出 running 也会自动关闭。
- `prefers-reduced-motion` 下停止自动轮播与入场动画；键盘 focus/blur 与鼠标 hover 行为一致。
- 浏览器控制台 error/warning: `0`。

## Comparison history

- Pass 1：入口行与参考图匹配，浮层完整位于视口内，长文本、层级和对比度可读；未发现 P0/P1/P2。
- Interaction pass：实际轮播从第 1 条切换到第 2 条，Portal 未受右栏 overflow 裁剪；无修正项。

final result: passed

---

# Session 结果面结论补全 Design QA（2026-08-06）

## Evidence

- Source visual truth: `D:\Temp\codex-clipboard-e6d97b85-add4-4154-afe3-3b42b978adec.png`
- Browser implementation: `D:\Work\WorkGround2\design-qa-session-result-conclusion.jpg`
- Focused side-by-side comparison: `D:\Work\WorkGround2\design-qa-session-result-conclusion-comparison.png`

## Normalization

- Source pixels: 1202 × 512 RGBA.
- Implementation pixels: 1280 × 720 RGB.
- Browser viewport: 1280 × 720 CSS px; reported device pixel ratio: 1.375.
- Comparison image: 2176 × 375 RGB. Both artifacts are cropped to the completed-result region and contained without stretching.
- State: completed run, result face visible, dark Iris fixture.

## Findings

- No actionable P0/P1/P2 visual or interaction differences remain.
- Fonts and typography: “结论” uses the existing result hierarchy; conclusion text keeps the product content font, 13px size, and 1.6 line height.
- Spacing and layout rhythm: the fixed 220px work window, header, marker, conclusion column, and recent-record column remain unchanged. Long conclusions scroll inside the summary column instead of growing the card.
- Colors and visual tokens: existing background, border, foreground, muted, and purple accent tokens are unchanged; no large green success mark is introduced.
- Image quality and asset fidelity: the component uses no raster content or generated visual assets; existing icon-library controls are retained.
- Copy and content: the generic event fallback is replaced by the final assistant reply for the same user turn. The completed title is now “结论”; process events remain on the reverse face.

## Interaction and accessibility checks

- Completed state defaults to `data-face="result"`.
- “查看过程” switches to `data-face="process"`; “查看结果” switches back to `data-face="result"`.
- The conclusion remains `已完成索引访问方式调整，并确认替换范围。` after both flips.
- The conclusion region exposes `aria-label="本轮结论"` and scrolls independently for long text.
- Browser console warnings/errors: none.

## Comparison history

### Pass 1

- Earlier finding: the result face only showed “本轮执行已完成” plus a tool-event fragment, which read as an empty result message.
- Fix: derive the same turn's final non-streaming assistant reply from Transcript and pass it into the result face without duplicating it into Run state.
- Post-fix evidence: `design-qa-session-result-conclusion-comparison.png`.
- Result: the result face now carries the actual conclusion while preserving the fixed two-sided window.

## Focused comparison

Focused comparison is required because the user-reported issue is confined to result-card content; the surrounding sidebar, composer, artifacts, and AddOn surfaces are unrelated. The combined image shows the missing generic body on the source side and the same region populated with an explicit “结论” and real turn result on the implementation side.

final result: passed

---

# Work 结构规划实时输出与结构确认 Design QA

- source visual truth: `D:\Codex\.codex\generated_images\019face3-a142-7712-90e3-3b5256973ee9\call_k7TTbidAluoO0TbL6lP5GQis.png`
- implementation: `D:\Work\WorkGround2\artifacts\design-qa\structure-clarification-final.png`
- mobile implementation: `D:\Work\WorkGround2\artifacts\design-qa\structure-clarification-mobile-v2.png`
- full comparison: `D:\Work\WorkGround2\artifacts\design-qa\structure-clarification-comparison-final.png`
- state: 历史布局基线；模型 JSON 正在输出，语义进度持续上推
- semantic status: 截图中的固定“确认节点”问题已废弃；当前实现只展示 Planner 报告的不可推导结构分歧，且不预选答案
- viewport: source `1430 × 1100`；implementation CSS viewport `1430 × 1100`、capture `1429 × 1100`、DPR `1`
- normalization: 同尺寸、同状态直接比较；浏览器截图后端产生 1px 宽度差，未做非等比缩放

## Findings

没有可执行的 P0/P1/P2 问题。

- 布局：回答卡位于 Work 主内容区视觉中心，左侧保留可读的实时规划输出；遮罩不会把后台工作过程完全抹掉。
- 信息边界：问题和影响标签只涉及无法推导的节点边界、依赖、并串行或槽归属，不询问业务内容、通用确认偏好或可自动推导的信息。
- 反馈强度：真实模型 JSON 作为连续底层输出，解析出的节点/依赖作为上推状态卡；加载描边、状态点和节点脉冲均遵守 reduced-motion。
- 选项：由 Planner 返回 2–4 个中立结构选项，可包含一个自定义输入；没有推荐态和默认选择，用户明确选择后按钮才启用。
- 恢复：关闭卡片后保留待答状态，刷新时按 Work revision 与任务说明恢复；失败显式展示并允许安全重试。
- 响应式：`600 × 900` 视口无横向溢出，底部动作区保持可见。
- 字体：实现沿用产品既有字体与 token；与概念稿存在轻微字形和密度差异，属于 P3。

## Comparison evidence

完整对比图已按等尺寸并排保存。弹层宽 `880px`，标题、流程节点、问题和选项在原始截图中可直接阅读，因此无需额外缩放裁切；实现单图同时作为聚焦证据。

## Interaction evidence

- 点击“自定义”后显示输入框，填写节点或依赖关系后主按钮启用。
- 点击采用后回答进入结构规划请求；问题只改变节点、依赖、并串行或槽归属。
- `Escape` 关闭弹层但保留待答问题。
- 移动端复验底部确认按钮持续可见。
- 浏览器控制台 error：0。

## Comparison history

- Pass 1：卡片偏小、遮罩偏重，后台 JSON 与上推状态不够明显（P1/P2）。
- Fix：卡片扩为 `880px`，降低遮罩并减小模糊，改为纵向流程，补齐结构影响标签、描述和选项图标。
- Pass 2：移动端长内容时动作区可能落到首屏外（P2）。
- Fix：移动端动作区改为 sticky，并复验无横向溢出。
- Pass 3：桌面、移动端、交互与控制台复验通过，P0/P1/P2 清零。

final result: passed

# Design QA — 独立填写信息面板（2026-07-30）

- 常驻入口参考图：`D:\Temp\codex-clipboard-13b088ba-5a69-49ba-b5ed-633e8edc5211.png`
- 常驻入口实现截图：`D:\Codex\.codex\visualizations\2026\07\30\019fb366-2b71-7d82-ae93-6223ca036927\work-information-summary-retained.png`
- 上层填写框实现截图：`D:\Codex\.codex\visualizations\2026\07\30\019fb366-2b71-7d82-ae93-6223ca036927\work-information-overlay.png`
- 验收视口：1280 × 720

## 对比与修正

- 两列“工作信息”列表保持常驻，与参考图相同地显示问题、填写状态和总进度；工作结构不再重复渲染待填写项。
- 点击任一待填写行后，牌堆式填写框作为 WorkCard 内容上层打开；背景列表仍存在并被遮罩弱化，关闭后原位返回列表。
- 多项待填写采用三层错位卡片，后层卡保留下一个问题标题，形成明确的扑克牌叠放感。
- 默认动作是“保存并填写下一项”，也可切换为“保存后关闭”；关闭仅移除上层填写框。
- 每张卡均提供“高级”输入区，额外内容随草稿、提交和恢复状态持久化。
- 数字范围与整数、比例、百分比提示均为 warning；文案为“这个数值可能不合理，请确认”，不会阻止提交。
- 文件类型支持点击打开选择框，也支持拖拽文件到指定区域；失败显式显示且可以重试。

## 状态与类型覆盖

- 已覆盖：常驻列表、点击打开、填写中、高级展开、软警告、保存并进入下一项、保存后关闭、关闭返回列表、全部完成、不可用。
- 已覆盖：时间日期、短文本、列表、多选、数字、文件。
- 必填项初始空态保持安静，仅禁用提交；用户输入无效值后才显示对应错误。

## 视觉与交互验收

- Fonts and typography：标题、进度、问题、辅助说明和操作层级与现有深色工作卡保持一致。
- Spacing and layout rhythm：卡片内边距、选项间距、底部动作区和叠卡偏移无拥挤或裁切。
- Colors and visual tokens：沿用石墨黑、暖棕表面和琥珀色强调；warning 与 error 的语义颜色分离。
- Image quality and assets：全部使用现有矢量图标与 CSS 表面，不引入低清位图。
- Copy and content：常驻列表状态、主动作、关闭动作、拖拽提示和软警告均为直接中文。
- 键盘与语义：输入控件具备可访问标签；高级区、单选动作和文件按钮均可键盘操作。
- 浏览器交互：点击行后 `role=dialog` 唯一出现；关闭后 dialog 消失且常驻列表继续存在。
- 数字软警告：提示“这个数值可能不合理，请确认”时提交按钮仍可用。
- 文件：上层填写框内点击选择按钮与定向拖放目标同时存在。
- 干净页面浏览器控制台 error：0。

## 缺陷结论

- P0：0
- P1：0
- P2：0
- P3：0

final result: passed

---

# Work 通用工作面板 Design QA（2026-07-30）

- source visual truth：`docs/assets/work-general-model/team-building-waiting.png`；同目录其余团建、财务报表、软件工程状态图用于验证通用布局语法。
- production feedback source：`D:\Temp\codex-clipboard-88238c3a-da6f-44c6-94a4-28c3ada9c131.png`。
- implementation screenshot：`docs/assets/work-general-model/qa/implementation-real-input-waiting-pass1.png`。
- combined comparison：`docs/assets/work-general-model/qa/compare-real-input-waiting-pass1.png`。
- viewport：CSS `1536 × 1024`，capture `1536 × 1024`，device scale factor `1`。
- state：真实 Definition 3 形态；两个 reserved 成果槽、一个 `waiting_input` 任务、六个 requested 输入、一个自动生成且仅重复节点描述的 Markdown Block。

## Findings

最终无可执行 P0/P1/P2。

- 字体：沿用产品现有 UI 字体、字重和字号层级；标题、状态、字段和值均在原始截图中可读。
- 布局：恢复“成果架 → 三段状态 → Block 工作区 → 折叠执行摘要 → 目标脚注”的稳定骨架；首次输入态在 `339–533px` 区域形成 `1496 × 193px` 的结构化工作区，不再留下重复目标 Block 和大片无意义空白。
- 颜色：沿用现有暗色 surface、青色结构语义与琥珀色等待语义；没有引入领域专属配色。
- 图像与资产：界面不含图片资产；图标全部来自既有 Lucide 库。
- 文案：设计参考是“处理完成后等待审批”，真实数据是“首次等待输入”。两者的业务文案和 Block 内容必须不同；相同的是布局骨架、密度和动作层级。
- 通用性：新增工作区只消费 Definition 的 `inputSpecs`、`nodes`，以及当前 Run 的 `inputs`、`tasks`；没有团建、财务或软件工程枚举。

## Comparison history

- 旧 QA：只用富内容夹具验证“处理后等待审批”，没有覆盖真实 `waiting_input` 的单任务、空输入、自动摘要 Block 组合。生产截图证明该 QA 结论范围过宽。
- Pass 1 findings：状态卡只有两段且右侧空洞；节点描述被当成正文 Block 重复展示；执行区一旦展开便压过工作内容（P1/P2）。
- Fix：等待输入补成三段状态；精确识别 revision 1 且内容等于节点描述的自动摘要 Block；无真实业务 Block 时，以 Definition/Input 投影生成“工作信息 + 工作结构”通用 Block；执行详情继续默认折叠。
- Post-fix evidence：同一真实数据形态在 `1536 × 1024` 浏览器渲染；执行摘要 `false → true → false` 可逆；控制台 error `0`。

## Residual scope

- 概念图中的候选方案、预算占用和筛选条件只会在 AI 真正生成对应 Block 数据后出现。首次输入态不会伪造这些业务结果。
- 响应式规则延续既有 `880px / 640px` 断点；本轮主要回归为用户反馈的桌面形态。

- detail：`docs/WORK_GENERAL_MODEL_DESIGN_QA.zh-CN.md`

final result: passed

---

# Work 讨论模态层 Design QA

- source visual truth path: `D:\Temp\codex-clipboard-b62e65db-d7f8-40d6-8a01-d7eaaa235981.png`
- implementation screenshot path: `D:\Temp\work-discussion-modal-implementation.jpg`
- full-view comparison: `D:\Temp\work-discussion-modal-comparison-full.png`
- focused comparison: `D:\Temp\work-discussion-modal-comparison-focus.png`
- state: 已完成 Work 中点击 `Select KTV venue` 的“讨论”，显示空白讨论表单
- viewport: 实现 CSS 视口 `1280 × 720`，`devicePixelRatio = 1.375`
- dimensions and normalization:
  - source: `1568 × 1256` px
  - implementation: `1280 × 720` px
  - full-view comparison 将两侧等比放入各 `784 × 628` 区域，专门判断页面层级与内容流
  - focused comparison 分别裁切表单区域后按宽度对齐，专门判断表单排版与视觉 token

## Findings

没有可执行的 P0/P1/P2 问题。

- 字体与排版：沿用现有 WorkGround2 UI 字体、字重和字号层级；标题、标签、提示和按钮关系与源表单一致。
- 间距与布局：讨论表单从文档流提升到居中模态层，底层 Work 列表不再被推挤；`896 × 295` 的弹层在当前视口内留有充分安全边距。
- 颜色与 token：复用现有暗色 surface、橙色 focus/accent、边框和 `--z-modal: 1200`；45% 遮罩清晰建立上下层级。
- 图像与资产：该界面没有图像资产或品牌图形，不存在替代、模糊或裁切问题。
- 文案与内容：标题、输入标签、占位文案、作用范围和主动作均保持原有语义。
- 可访问性与交互：弹层为 `aria-modal` 对话框并直接挂载到 `document.body`；输入框自动聚焦，关闭按钮可关闭并可再次打开。自动化测试另覆盖 Esc、遮罩关闭、焦点回归和焦点循环。

## Full-view comparison evidence

源图左侧显示讨论区嵌在 `ExecutionList` 文档流内，导致后续内容整体下移；实现右侧显示带遮罩的应用级居中模态层，底层三个任务节点位置保持稳定。该差异是本次明确要求的产品修正。

## Focused comparison evidence

源表单与实现表单在标题、关闭入口、输入区、快捷键提示、作用范围和主按钮上保持同一信息结构。实现只调整了容器层级、宽度约束、圆角与阴影，以建立模态语义。

## Comparison history

- Pass 1: 未发现 P0/P1/P2；无需视觉返工。

## Implementation checklist

- [x] Portal 到 `document.body`
- [x] 全视口固定遮罩与共享 modal z-index
- [x] 居中、限宽、限高并支持内部滚动
- [x] 关闭、Esc、遮罩点击统一收口
- [x] 自动聚焦、焦点循环与关闭后焦点恢复
- [x] reduced-motion 支持

final result: passed

---

# Design QA — Work V2 执行 Block

- source visual truth path: `D:\Work\WorkGround2\docs\assets\work-collaboration-workbench\workbench-baseline.png`
- implementation screenshot path: `D:\Temp\workground2-v2-block-implementation-final.png`
- focused comparison evidence: `D:\Temp\workground2-v2-block-comparison-final.png`
- viewport: CSS `1280 × 720`，`devicePixelRatio = 1`
- dimensions and normalization:
  - source full image: `1488 × 1026` px；执行列表裁切后按宽度归一为 `1128 × 148` px
  - implementation full image: `1280 × 720` px；执行列表裁切为 `1128 × 170` px
  - comparison 将相同执行列表状态上下排列，排除侧栏、成果架与页面其余区域
- state: 深色主题；完成、运行中和等待输入各一项；运行任务进度 `76%`

## Findings

没有可执行的 P0/P1/P2 问题。

- Fonts and typography：沿用 WorkGround2 全局字体；标题、状态说明、进度百分比和操作文字的层级与 V2 基线一致，长说明保持单行截断。
- Spacing and layout rhythm：恢复“AI 正在执行”分区、紧凑任务行、`36px` 状态图标、固定标题列、状态说明列、右侧进度与操作区；展开内容与任务行保持同一卡片边界。
- Colors and visual tokens：移除按完成状态描整行绿框的旧表达；容器使用中性边框，状态颜色只落在图标、状态文字和进度上。
- Image quality and asset fidelity：状态、讨论和展开入口使用项目现有 Lucide 图标，没有平台 emoji、占位图或近似 CSS 图形。DTO 暂无任务类别图标字段，因此图标按状态表达，这是明确的数据约束。
- Copy and content：恢复“AI 正在执行 / 无需额外照看，各项任务将并行推进”；数值进度只在右侧进度区显示，不重复塞入状态说明。
- Accessibility and interaction：讨论与展开是两个独立原生按钮，避免嵌套交互语义；浏览器实测展开、折叠、打开讨论和关闭讨论均有效。

## Full-view and focused comparison evidence

源图与实现的分区标题、图标列、标题列、状态说明列、进度区和讨论入口顺序一致。实现保留右侧独立展开箭头，因为生产 Block 支持整行展开；这是现有交互能力的显式入口，不改变基线信息层级。

## Comparison history

- Pass 1：发现标题列过宽，状态说明相对源图明显右移；运行进度同时出现在说明和进度条，属于 P2 密度偏差。
- Fix：标题列收敛到 `140px`；可解析数值进度仅保留在右侧进度条；进度色切换为成功语义色。
- Pass 2：状态说明与源图列位置对齐，重复进度消失；五项保真面无剩余 P0/P1/P2。

## Engineering evidence

- `ExecutionList.test.tsx`：180 项通过，覆盖十种状态、稳定身份、展开折叠、折叠态讨论、输入、重试和恢复。
- `npm.cmd run typecheck`、`npm.cmd run check:css`：通过。
- 浏览器 console error/warning：0。

final result: passed

---

# Design QA — Work V2 成果架

- source visual truth path: `D:\Work\WorkGround2\docs\assets\work-collaboration-workbench\scenario-script-writing.png`
- implementation screenshot path: `D:\Temp\work-result-shelf-implementation.png`
- full-view comparison evidence: `D:\Temp\work-result-shelf-comparison.png`
- responsive evidence: `D:\Temp\work-result-shelf-narrow.png`
- viewport: 主视图 CSS `1120 × 520`、窄视图 CSS `620 × 520`，`devicePixelRatio = 1`
- dimensions and normalization:
  - source full image: `1486 × 1058` px；成果架聚焦裁切 `918 × 172` px
  - implementation full image: `1120 × 520` px；成果架区域 `1072 × 155` px
  - comparison 将源图成果架与实现成果架并排等高展示，排除整页侧栏和执行区差异
- state: 三个 Markdown 成果均为 `ready`，带打开与预览能力

## Findings

没有可执行的 P0/P1/P2 问题。

- Fonts and typography：沿用 WorkGround2 全局 UI 字体与字号 token；成果标题、状态摘要和操作文案层级与设计稿一致，长标题和摘要单行省略。
- Spacing and layout rhythm：恢复左侧 `136px` 成果说明栏、单排成果卡、卡片内主信息区和底部操作区；主视图三卡等宽且成果架无横向溢出。
- Colors and visual tokens：移除白底 fallback，全部使用 `--bg-*`、`--fg-*`、`--border*` 和语义状态 token；暗色主题与设计稿一致。
- Image quality and asset fidelity：成果类型、状态和操作均使用项目现有 Lucide 图标，不再使用平台 emoji；不同文件类型保留语义色。
- Copy and content：成果说明恢复为“工作过程中产生的文件与成果将在此汇总”；单文件成果以真实文件名作为卡片标题，状态与摘要进入次级信息。
- Responsiveness：`620px` 下说明栏转为顶部横条，成果卡保留 `300px` 可读宽度并在成果架内部横向滚动；页面本身无横向溢出。

## Interaction evidence

- 浏览器内点击唯一的“预览 Preview result.md”按钮后，内联预览显示 `# Preview ready`。
- 主视图、窄视图和预览交互的浏览器 console error/warning 均为 0。
- 组件专项测试覆盖打开、下载、定位、预览、转换、重试、并发去重、失败恢复和迟到结果隔离。

## Comparison history

- Pass 1：发现虽已修复白底和成果说明栏，卡片仍沿用“槽位标题 + 小文件行”的旧结构，与设计稿的大文件图标、文件标题和底部操作区存在 P2 差异。
- Fix：卡片改为 artifact-led 结构；单文件成果使用文件名、大号类型图标、状态摘要和独立底部操作区，已完成状态收敛为右上角图标。
- Pass 2：并排对照后，五项保真面无剩余 P0/P1/P2；窄窗口和预览交互复验通过。

final result: passed

---

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

---

# Work 信息自动开始 Design QA（2026-08-02）

- 设计目标：`D:\\Codex\\.codex\\generated_images\\019fbbce-29be-7d90-a0cb-9bee64394e60\\exec-3cc77733-b602-48a3-a4f6-05689c3ab8ae.png`
- 实现对照：`design-qa-comparison.png`
- 验收主题：Carbon（深色、青绿色强调色）

## 对照结论

- 布局：倒计时、状态文案、暂停与立即开始均保持在 Work 信息标题行内；实际实现压缩设计稿的演示留白，不增加画布纵向占用。
- 视觉：沿用项目主题 token、边框、圆角、字体和 Lucide 图标；Carbon 主题与设计稿的青绿色语义一致，其他主题自动继承各自强调色。
- 行为：20 秒自动开始；暂停后时间停止；打开任意信息自动暂停；立即开始可跳过等待；失败保留信息并显式提供重试。
- 响应式：1440、1024、600 宽度下无横向溢出；窄屏时标题和倒计时分行，按钮保持可操作。
- 可访问性：按钮可键盘聚焦，状态使用 `aria-live`，进度使用原生 `progress`，支持 `prefers-reduced-motion`。
- 数据可靠性：最终信息先持久化为 `readyForStart` 草稿，倒计时结束才释放调度；重启恢复不会绕过等待，重复开始请求可幂等重放。

final result: passed

---

# Design QA — Work 重新开始与流程复用（2026-08-02）

- Scope: Work 底部“重新开始”入口、选择弹窗、首次保存常用工作弹窗。
- Reference: 用户提供的底部按钮区与“保存为常用工作”截图。
- Runtime: `desktop/build/bin/WorkGround2.exe`，生产构建，真实 Work 运行态。
- Interaction: 播放、暂停、停止、重新开始四个原按钮均保留；圆箭头打开二选一弹窗；选择“保存流程并再次运行”进入首次保存状态；关闭弹窗不会触发重启或保存。
- Visual: 弹窗沿用现有色板、边框、圆角、层级和按钮规格；状态承载于原按钮区和弹窗，没有新增横向通知条；窄内容使用省略与换行，未发现裁切或重叠。
- Accessibility: 四个运行控制和弹窗操作均暴露可访问名称；不可用状态由原控制逻辑保留；弹窗支持关闭且忙碌态阻止重复提交。
- Comparison: `D:/Codex/.codex/visualizations/2026/08/01/019fbd42-57a1-7721-a940-695c37c7c869/work-restart-comparison.png`。

final result: passed

---

# Session 双面执行窗口 Design QA

## Evidence

- Source process truth: `D:\Temp\codex-clipboard-4ce9352c-275f-4c73-98d3-f7e836a82903.png`
- Source result direction: `D:\Codex\.codex\generated_images\019fd512-4465-7af3-825e-d60c7c827dab\exec-34d52c54-3f93-4247-b6b4-a292478b50c0.png`
- Browser implementation, process: `D:\Work\WorkGround2\design-qa-session-process-full.png`
- Browser implementation, result: `D:\Work\WorkGround2\design-qa-session-result-full.png`
- Browser implementation, running: `D:\Work\WorkGround2\design-qa-session-running-full.png`
- Side-by-side process comparison: `D:\Work\WorkGround2\design-qa-session-process-comparison.png`
- Side-by-side result comparison: `D:\Work\WorkGround2\design-qa-session-result-comparison.png`

## Normalization

- Browser viewport: 1280 × 720 CSS px.
- Browser reported device pixel ratio: 1.375; captured screenshots are normalized to 1280 × 720 pixels.
- Process source: 1140 × 256 pixels; compared using its 1045 × 220 work-window crop.
- Result source: 1488 × 1060 pixels; compared using its 1030 × 455 result-card crop as content direction.
- Default-session implementation work window: 760 × 220 CSS px.
- Iris fixture running work window: 909.8 × 220 CSS px.
- The user explicitly selected the existing 220px-high process window as one face, so the result direction is intentionally compressed into the same 220px frame rather than retaining the taller concept-card proportions.

## State and interactions tested

- Completed run defaults to the result face.
- “查看过程” flips the same mounted window to its process face.
- “查看结果” flips back without replacing the outer frame.
- Running run has only a process face and displays only four observed events; no future placeholder, percentage, predicted route, or total-step forecast is rendered.
- The process face keeps its event rail and selected detail mounted while the result face is visible.
- Reduced-motion users receive an immediate face switch without the 3D transition.
- Browser console errors/warnings checked: none.

## Findings

- No actionable P0/P1/P2 visual or interaction differences remain.
- Fonts and typography: existing WorkGround2 typography and weights are preserved; the process hierarchy matches the source, and result copy uses the same UI scale.
- Spacing and layout rhythm: both faces share one fixed 220px frame, border, radius, header height, and full-width placement. The result body is deliberately denser than the taller concept image to honor the selected fixed-size process frame.
- Colors and visual tokens: implementation uses existing background, border, foreground, muted, error, and purple accent tokens. There is no large green success mark.
- Image quality and asset fidelity: this component contains no raster imagery or custom illustrative assets; all controls use the existing icon library.
- Copy and content: process labels come only from actual `RunEvent` records. Result detail uses the latest meaningful observed event and never invents changed files or validation facts.

## Focused comparison

The work window itself was compared as a focused region because the full app includes unrelated sidebar, composer, artifacts, and AddOn surfaces. The side-by-side evidence confirms the header, horizontal observed-event rail, selected-event detail, border treatment, fixed height, and result/process flip affordance.

## Comparison history

### Pass 1

- Earlier finding: the old terminal state collapsed to a compact run tab, so it did not share the process window's size or location.
- Fix: replaced the compact terminal tab with a two-sided fixed work window; terminal transitions now set the result face, and the process face remains mounted behind it.
- Post-fix evidence: `design-qa-session-process-comparison.png` and `design-qa-session-result-comparison.png`.
- Result: no remaining P0/P1/P2 findings.

## Implementation checklist

- [x] Fixed-size shared work window.
- [x] Running process face with observed events only.
- [x] Automatic terminal flip to result.
- [x] Manual process/result flip controls.
- [x] Failure/cancelled result variants.
- [x] Keyboard labels, `aria-busy`, live log, hidden-face tab isolation, and reduced motion.
- [x] Browser interaction and console verification.

## Follow-up polish

- P3: when the backend later exposes typed changed-file and validation artifacts, the result face can add those sections without parsing log text.

final result: passed
