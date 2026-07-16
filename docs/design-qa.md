# Desktop Obsidian Iris 设计 QA

## 结论

- 最终状态：通过。
- 基准：`docs/assets/workground2-desktop-obsidian-iris-reference.png`，1488 × 1024。
- 实现状态：同时覆盖 `?uiFixture=iris` 与 Wails 真实 Session 运行态；真实运行态复拍窗口约 1240 × 720，与用户提供的 1237 × 717 截图同级尺寸。
- P0 / P1 / P2：0。
- P3：媒体预览使用现有 WorkGround2 品牌图作为可交互 AddOn 媒体夹具；插件接入真实媒体后由插件内容槽替换，宿主尺寸和裁切契约保持不变。

## 同视口对照

完成两轮并排对照。最终实现保持了设计基准的主要空间关系：264px 左侧 Workspace/Session 树、104px Session Header、64px 任务记忆、48px 主内容边距、392px AddOn Workbench、40px 完成态 Run、160px 活动态 Run、64px Artifact Shelf 与 48px RuntimeConfigBar。

真实运行态追加三轮复拍，修复了 fixture 与实际入口分叉的问题：ProjectTree 的 workbench variant 改为安静的两级导航；移除时间、固定/删除、项目菜单等常驻噪音；侧栏及其子树禁止横向滚动；底部配置项在窄窗口内收缩并省略长标签，不再越过右边界。真实数据、导航、上下文菜单和非 workbench 布局行为保持不变。

右侧又以真实长会话完成一轮专项复拍：ConversationViewport 成为唯一滚动容器，Transcript 不再产生嵌套滚动条；标题栏、记忆栏、对话、Run、产物区、Composer 与运行配置统一落在 48px 内容基线。旧对话块、折叠思考块和只读工具批次继承的 `max-width + auto margin` 已在 workbench 范围内解除，避免正文二次居中。Composer 编辑区固定最小 128px，配置条与其同宽，底部保留 32px 呼吸空间。

针对真实历史恢复截图又补充了消息容器级契约：workbench 会话视口内所有 `.msg` 无论位于热区、历史恢复层或展开的 warm turn，左右 margin 都固定为 0。助手消息继续保留 760px 最大行宽并从 48px 基线起排；用户消息依靠自身 `align-items: flex-end` 保持右对齐。该规则消除了基础主题 `.msg { margin: auto }` 在深层包装中重新居中的遗漏。

任务记忆条与底部输入区完成最终对齐复拍：TaskMemoryBar 使用 SessionWorkspace 同一背景色，并以顶部、底部 1px hairline 表达固定信息区，不再形成高亮色块。ComposerWrap 与 RuntimeConfigBar 通过 workbench 直系子元素规则共同覆盖主题层的 `margin: 0 auto`，统一 48px 左右内缩、`width: auto`、`max-width: none` 与 `border-box`；真实 Wails 窗口中两条外边框左右端点重合。

Workspace 行补齐渐进式操作入口：默认仅显示文件夹与名称，固定预留 52px 操作槽；鼠标悬停、键盘焦点进入或菜单打开时显示“新建会话”和“更多”两个图标按钮。新建复用既有 `handleCreateTopic`，更多菜单复用项目历史、重命名、归档当前会话及移除 Workspace 的现有确认流程；hover 前后名称位置不移动。

TaskMemoryBar 去除演示数据与空状态占位：SessionMemoryBar 只透传 memory store 中该 session 的真实 `MemoryLine`，不再生成“未设置 / 等待任务信息 / 继续对话”。没有真实记忆时组件返回 `null`，标题下方直接进入会话；有真实记忆时以 40px 单行显示目标、当前和下一步。真实 Wails 空记忆会话复拍确认整条区域不占高度。

TaskMemory 已接入生产数据链路：Controller 按 Session 管理带 revision/source 的事实型状态，通过 `<session>.task-memory.json` 原子持久化，并在 Resume、分支切换和恢复时加载。用户任务、显式 Goal、运行、Ask、Approval、Todo、AutoResearch 与失败状态经过统一入口更新；Desktop Meta/事件携带快照，前端拒绝旧 revision 并保留空状态 tombstone。详细条件见 `docs/task-memory-runtime.md`。

## 核心检查

- 信息层级：任务标题、记忆、对话、Run、产物、Queue、输入与运行配置按固定顺序呈现；AddOn 作为置顶浮层，不改变底层会话状态。
- Run：完成态默认折叠，点击可展开；运行态固定 160px，日志内部滚动；停止入口可达。
- AddOn：登录、构建状态、媒体三种内容共用统一宿主；支持固定、最小化、关闭、密度切换；登录输入可编辑。
- Queue：最多展示两条，支持编辑回填、上移、下移、删除；无遮挡状态下排序交互验证通过。
- 产物：二进制、调试入口、脚本、归档包均使用真实图标组件和显式打开动作。
- 配置：模型、上下文、运行状态、交互模式、自动批准策略及主动作都在底部固定区域。
- 可访问性：语义按钮、区域标签、输入标签、焦点态、减少动画策略均保留；未使用头像区分用户和模型。
- 响应式：1024 × 768 与 820 × 768 无页面级横向溢出；窄屏 AddOn 进入 16px 双侧安全区。

## 验证记录

```text
npm.cmd run typecheck
npm.cmd run check:css
npm.cmd run build
npx.cmd tsx src/__tests__/desktop-ui-stores.test.ts
npx.cmd tsx src/__tests__/desktop-ui-components.test.tsx
npx.cmd tsx src/__tests__/theme-iris.test.tsx
npx.cmd tsx src/__tests__/iris-fixture.test.ts
npx.cmd tsx src/__tests__/iris-integration-test.tsx
npx.cmd tsx src/__tests__/workbench-layout.test.ts
npx.cmd tsx src/__tests__/app-chrome-tabs.test.ts
npx.cmd tsx src/__tests__/project-tree-runtime.test.ts
npx.cmd tsx src/__tests__/typography-overflow-contract.test.ts
wails build -o WorkGround2-iris-qa.exe -m -nosyncgomod
```

浏览器验证覆盖：完成 Run 展开、AddOn 关闭/重开、登录字段输入、Queue 下移、同视口视觉对照、1024/820 宽度稳定性。Wails 真实桌面验证覆盖：真实 Workspace/Session 树、长会话名、真实历史内容及滚动、折叠思考/工具块、空产物态、Composer、运行配置条、单滚动容器与 Windows 无框窗口边界。

---

# 模型设置简化设计 QA

## 结论

- 最终状态：通过。
- 设计基准：`docs/assets/model-configuration-ux-design.png`。
- 实现快照：`docs/assets/model-configuration-implemented.png`。
- 并排对照：`docs/assets/model-configuration-qa-comparison.png`。
- P0 / P1 / P2 / P3：0。

## 对照结果

实现保持了设计基准的核心层级：顶部先显示连接状态和显式“添加模型服务”主按钮，中部只保留默认模型，复杂任务、多模型协作与运行上限统一折叠到高级区。连接状态、供应商名称、检查连接与管理连接集中在同一区域；已配置但 `added=false` 的官方默认供应商按真实凭据状态显示为已连接，默认模型不再误报“未配置”。

官方接入流程默认隐藏内部供应商名称，只要求选择服务并填写密钥；保存后立即验证模型列表，失败会显式提示并保留密钥草稿供安全重试。Anthropic 与 Google 官方模型发现分别使用各自受支持的鉴权头和兼容端点。

## 浏览器交互检查

- “+ 添加模型服务”在模型页首屏始终可见且唯一，点击后在当前页面展开官方/自定义接入。
- 官方接入只暴露服务选择与密钥输入，DeepSeek、OpenAI、Anthropic、Google、Groq 预设可见。
- 默认模型显示 `deepseek-v4-flash` 与“已设密钥”，没有“未配置模型”误报。
- 高级区默认收起；展开后显示规划模型、协作助手模型、思考深度、嵌套深度与运行上限。
- 设置导航、主内容和内部滚动在 1212 × 720 的 Wails 模拟窗口中无横向溢出。

## 验证记录

```text
npm.cmd run build
npm.cmd run typecheck
npx.cmd tsx src/__tests__/model-settings-simplification.test.tsx
npx.cmd tsx src/__tests__/settings-refresh-snapshot.test.tsx
go test ./internal/config/
```

模型设置专项契约共 14 项通过；既有设置刷新契约 18 项通过。浏览器实际渲染验收覆盖连接状态、默认模型、添加入口、添加表单与高级区展开。

仓库前端全量 `npm.cmd test` 运行到既有 `composer-goal-toggle.test.tsx` 时失败，原因为 Composer stop button 未渲染；该用例单独运行可稳定复现，且本功能未改动 Composer。模型设置专项、相关设置回归、TypeScript、CSS、生产构建与 desktop 全量 Go 测试均独立通过。

---

# 小组件可读性、三项点选与 workspace 路由设计 QA

## 结论

- `final result: blocked`：实现、专项测试、构建和已有同版本截图的静态视觉对照通过；本轮内置浏览器停在网络错误 `data:` 页后触发 URL 安全策略，无法完成三项点选与输入框的点击复拍。
- 参考截图：`D:/Temp/codex-clipboard-e0b3bd32-1d7d-4ee1-b569-e11490c2442d.png`、`D:/Temp/codex-clipboard-28707a04-37b2-40a9-b142-a07ce47b2b26.png`。
- 实现截图：`docs/assets/widget-mode/widget-large-type-idle.png`、`docs/assets/widget-mode/widget-large-type-ticker.png`、`docs/assets/widget-mode/widget-message-page-1.png`、`docs/assets/widget-mode/widget-message-page-2.png`。
- 目标视口：原生窗口 `590 × 142`，WebView 实际截图高度 `160px`；内部逻辑画布 `1180 × 284` 后按 `0.5` 缩放。

## 同屏视觉对照

- 字体：参考图中的主信息、用户输入与状态过小；实现截图中的主信息达到约 `22–28px`，项目名约 `13–16px`，状态约 `12–14px`。消息分页截图保持一屏一页，第二页只显示剩余正文。
- 空间：W2 标识继续占较小区域，项目名更醒目；右侧仍以文字为主，没有把消息退化成列表。
- 色彩与图素：沿用黑底、青色刻度/扫描线、黄色主动作和红色错误态；九宫格机壳、切角与透明外角保持原设计资源。
- 文案：主信息只显示当前一条；需要关注时只额外显示“还有 N 条”；无重要消息时显示运行状态。

## 功能证据

- `messageForPending`：1–3 个单选项留在小组件，4 个以上、多选或多问题进入主窗口。
- `widget-choice3` mock：提供中文、英文、日语三个真实选项；回答后 mock 进入无待处理消息的运行状态，可用于复验状态切换。
- workspace 路由：CI gate、临时目录、linked worktree 与仅含 `.WorkGround2` 的会话空壳不会成为隐式目标；同目录同名前缀优先回落到稳定主 workspace；输入完整名称时仍允许显式选择临时目标。

## 验证记录

```text
go test . -run 'Test(BuildWidget|MessageForPending|ChooseWidgetWorkspace|WidgetWorkspace|WidgetIsTransient|WidgetHistory|DefaultWidget|QueueNeeds|ConciseWidget|LastWidget)' -count=1
go vet .
pnpm.cmd typecheck
pnpm.cmd check:css
pnpm.cmd build
git diff --check
```

以上命令均通过。待人工复验 URL：`http://127.0.0.1:4174/?mock=widget-choice3`；新对话输入复验：`http://127.0.0.1:4174/?mock=widget-idle`。
