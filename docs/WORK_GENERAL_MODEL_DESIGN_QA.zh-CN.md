# Work 通用工作面板视觉验收

## 视觉真值

本轮只使用新生成的设计图，不引用旧界面截图：

- `docs/assets/work-general-model/team-building-running.png`
- `docs/assets/work-general-model/team-building-waiting.png`
- `docs/assets/work-general-model/team-building-completed.png`
- `docs/assets/work-general-model/finance-running.png`
- `docs/assets/work-general-model/finance-waiting.png`
- `docs/assets/work-general-model/finance-completed.png`
- `docs/assets/work-general-model/software-running.png`
- `docs/assets/work-general-model/software-waiting.png`
- `docs/assets/work-general-model/software-completed.png`

实现没有枚举行业。团建三态仅作为同状态、同尺寸视觉对照；财务与软件工程用于验证同一布局语法可承载不同 Block 与成果类型。

## 验收环境

- 视口：1536 × 1024，device scale factor 1
- 响应式复核：720 × 900
- 状态：running、waiting、completed
- 实现截图：
  - `docs/assets/work-general-model/qa/implementation-running-approved.png`
  - `docs/assets/work-general-model/qa/implementation-waiting-approved.png`
  - `docs/assets/work-general-model/qa/implementation-completed-approved.png`
  - `docs/assets/work-general-model/qa/implementation-running-720-approved.png`
- 全屏并排对照：
  - `docs/assets/work-general-model/qa/compare-running-approved.png`
  - `docs/assets/work-general-model/qa/compare-waiting-approved.png`
  - `docs/assets/work-general-model/qa/compare-completed-approved.png`
- 顶部重点区域对照：
  - `docs/assets/work-general-model/qa/compare-running-top-approved.png`
  - `docs/assets/work-general-model/qa/compare-waiting-top-approved.png`
  - `docs/assets/work-general-model/qa/compare-completed-top-approved.png`

## 对照历史

| 轮次 | 可见问题 | 修正 |
|---|---|---|
| Pass 1 | 强调色偏红；成果进度出现 5200%；Block 缺标题；清单详情噪声大 | 收敛为青绿色状态色；规范化进度；显示 Block 标题；短状态改为行尾文本 |
| Pass 2 | 固定双栏无法表达 3/9、7/5 的信息密度 | 使用真实 `BlockPlacement.span` 驱动 12 栏布局；运行态保留作者顺序，其余状态按语义槽位排布 |
| Pass 3 | 等待态与完成态缺少统一状态骨架；运行详情占用主视线 | 加入通用状态面板；执行详情折叠为摘要轨；目标持续固定在底部 |

早期对照证据保留为：

- `docs/assets/work-general-model/qa/implementation-running-pass1.png`
- `docs/assets/work-general-model/qa/implementation-waiting-pass1.png`
- `docs/assets/work-general-model/qa/implementation-completed-pass1.png`

## 最终检查

- 信息层级：成果架 → 当前状态 → Block 内容 → 执行摘要 → 目标，符合设计图骨架。
- 状态表达：planning、running、waiting、failed、completed 共享一套组件；等待与失败使用明确动作入口。
- 交互：等待态“处理”会展开现有执行/输入区域；执行摘要可展开和收起；成果动作继续使用现有真实处理器。
- 响应式：720px 下无页面级横向溢出；成果架保留局部横向浏览。
- 可访问性：状态标题关联唯一；进度使用 `progressbar`；操作按钮有文本标签；图标来自现有 Lucide 库。
- 图像与资产：页面没有需要新增的插画或照片；未使用 emoji、手绘 SVG 或 CSS 图形伪造资产。
- 控制台：最终三态未发现新增运行时错误。
- 遗留 P3：实现字体与间距比概念图略紧凑，以保持现有桌面端设计系统一致。
- 设计取舍：等待态的详细问题和选项继续由既有类型化输入面板承载，状态面板只提供摘要与入口，避免在视觉层复制业务表单。

final result: passed
