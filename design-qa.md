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
