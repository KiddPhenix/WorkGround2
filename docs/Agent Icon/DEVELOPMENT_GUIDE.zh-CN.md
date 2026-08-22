# WorkGround2 Agent Icon 前端开发交接文档（中文版）

> 面向在 WorkGround2 Desktop（Wails + React + Vite）中落地 Agent Icon 的前端开发者。
> 本文是实施的唯一入口：给出资源契约、实际替换点、模块边界、可落地类型与算法、资源接入策略、测试与验收清单。设计与资源事实以 [设计规范](README.zh-CN.md) 与 [资源清单 manifest](assets/manifest.json) 为准，本文与它们不一致时以 manifest 为准。

---

## 1. 目标与非目标

### 1.1 目标

- 在会话列表（`ProjectTree`）中用可组合 Agent Icon 替换当前的 `MessageSquare` 图标：身份（随机、稳定）、任务（工具图形）、Workspace（徽标）、状态（LED 眼睛）四层叠加，一次渲染。
- 身份随机结果对同一 `sessionId` 完全确定：重复渲染、重启、重连不漂移；禁止 `Math.random`。
- 状态眼睛按会话生命周期归一化，动画参数（fps、循环、停帧）全部来自 manifest。
- 资源缺失、状态未知、任务未知等异常显式可观察、可上报、可回退，不产生破图。

### 1.2 非目标

- 不直接手改生成后的 PNG/sprite 或 manifest：核心美术改动先更新 `imagegen-source/` 母图与 `apply-imagegen-assets.mjs`，再执行 `generate-assets.mjs`；SVG 只保留为结构参考。
- 不做运行时从 `docs/` 目录读取资源（打包产物里没有 `docs/`）。
- 不引入动画库；不把图标做成独立 npm 包。
- 不在本轮实现中移除既有 `.project-tree__topic-visual` 状态圆点（它属于另一处已测试的状态摘要；只要求其状态来源与本图标同源，见 §3）。
- 不为身份层引入业务语义：帽子/头发/外壳色不得暗示任务、Workspace 或状态（manifest `identityRule.semantic = false`）。

---

## 2. 资源契约与目录

资源真实位置：`docs/Agent Icon/assets/`（生成器 `docs/Agent Icon/generate-assets.mjs` 幂等重建，`manifest.json` 是唯一真源）。

| 契约项 | 值 |
|---|---|
| 画布 | 64×64，透明背景（`manifest.canvas`，`viewBox="0 0 64 64"`） |
| 图层顺序 | `frame` → `headwear` → `eyes` → `workspaceBadge` → `taskTool`（后层在上，DOM 顺序即叠加顺序） |
| 外壳边框色（frames） | 9：`violet` `cobalt` `cyan` `teal` `green` `lime` `amber` `orange` `coral` |
| 头部模块 A（历史目录 `hats`） | 15 个机器人天线、传感器、散热鳍、信号灯等模块；为兼容稳定 seed 保留原文件 id 与目录名 |
| 头部模块 B（历史目录 `hair`） | 15 个机器人扫描器、机械耳、装甲冠、网络阵列等模块；为兼容稳定 seed 保留原文件 id 与目录名 |
| 任务工具（tools） | 24（见 §5.4） |
| 状态眼睛（eyes） | 5：`running` `problem` `success` `failure` `cleanup`，每状态 6 帧（共 30 帧），另有横向 sprite `sprites/eyes/<status>.png`（384×64，第 n 帧位于 x = n×64） |
| 模板 | `templates/workspace-badge.png`（左下徽标模板）、`templates/neutral-led-grid.png`（中性眼睛/占位） |

关键规则：

- **帽子/头发互斥**：`headwear` 槽位只能二选一（manifest `identityRule.headwear`：*Choose exactly one hat or one hair style*），不允许同时叠加，也不允许缺失。
- 所有图层都是完整 64×64 画布，消费方只需把每层绝对定位到同一矩形：
  ```css
  .agent-icon {
    position: relative;
    width: 32px;
    height: 32px;
  }
  .agent-icon > img {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
  }
  ```
- 眼睛位于脸部且优先级最高，`headwear` 不得遮挡脸部（资源已按此设计，实现时不要缩放错位）。

眼睛动画参数（来自 manifest，实现直接读取，禁止硬编码副本）：

| 状态 | fps | loop | holdLast | 结束行为 |
|---|---:|---|---|---|
| `running` | 6 | true | false | 循环 |
| `problem` | 3 | true | false | 循环 |
| `success` | 8 | false | true | 单次，停末帧 |
| `failure` | 8 | false | true | 单次，停末帧 |
| `cleanup` | 2 | true | false | 低频循环 |

---

## 3. 现状与替换点

以下描述以选择器/元素特征定位，**不依赖行号**（行号会漂移）。

### 3.1 组件替换点

文件：`desktop/frontend/src/components/ProjectTree.tsx`

- 会话行的图标占位是 `.project-tree__session-icon` 这个 `<span>`，内部是 `<MessageSquare size={13} />`（lucide-react 图标），渲染条件是 `!workSession && !collaborationSession && !sourceBadge`。
- 替换动作：把该 `<span>` 内的 `MessageSquare` 换成 `<AgentIcon viewModel={viewModel} />`，外层 `<span>` 类名保留（或改挂到新容器）。条件（work session / collaboration session / 外部来源行）不变，这些行不显示 Agent Icon。
- 数据来源：`ProjectNode`（`desktop/frontend/src/lib/types.ts`）已有 `sessionId?`、`sessionPath?`、`topicId?`、`root?`、`key`、`running?`、`status?`（`ProjectTopicStatus`：`thinking` `streaming` `waiting_confirmation` `background_job` `paused` `error`）、`turns?`、`createdAt?`、`lastActivityAt?`、`turnStartedAt?`。状态归一化入口现为文件内的 `topicStatus(node)` / `projectTreeTopicVisualState(node, unread, status)`，它们继续保留，Agent Icon 的 `eyeStatus` 必须由**同一份归一化结果**扩展而来（状态单源，见 §5.6）。

### 3.2 样式替换点

文件：`desktop/frontend/src/styles.css`

- `.app--workbench .workspace-sidebar .project-tree__session-icon`（含子选择器 `.project-tree__session-icon svg`）：当前把图标框成 17×17px、svg 15×15px。替换后改为 Agent Icon 容器尺寸：列表默认 **24px**，需要更大表达时用 32px（可在 workbench 变体下覆盖）。
- `.project-tree__topic-visual` 及其 `--running` / `--done` / `--failed` 变体继续作为旧图标、外部来源行和不支持 Agent Icon 的行的回退。Agent Icon 开启且成功渲染时，当前行不再同时显示状态圆点，避免“眼睛状态 + 圆点状态”重复表达；两者必须消费同一份归一化状态。
- 深浅主题：眼睛与工具图形是资产内固定色（manifest 中每状态有 `color`、每工具/每 frame 有 `accent`/`color`），不要用主题变量改写资产色；主题只需保证容器背景对比可读。

### 3.3 现状缺口（实现时需要补）

当前 UI 没有“任务枚举”和“清理条件”这两个业务信号：任务映射表（§5.4）是建议枚举，需要业务层在单一入口提供；`cleanup` 状态需要业务侧给出“已终止且达到清理条件”的信号，否则归一化结果只会落在 `success`/`failure`。`problem` 同理需要显式信号 + 原因（见 §5.6）。

---

## 4. 模块边界与文件落点

UI 只消费规范化 ViewModel；业务细节全部下沉到纯函数模块（可单测、可重放、可重试）。

| 文件（建议落点） | 职责 | 约束 |
|---|---|---|
| `src/lib/agentIcon/types.ts` | ViewModel 与输入事件类型 | 无副作用 |
| `src/lib/agentIcon/identity.ts` | 稳定 hash + 身份选择（frame/headwear） | 纯函数，禁 `Math.random`/`Date.now` |
| `src/lib/agentIcon/task.ts` | 任务枚举 → manifest tool id 的单一映射 | 未知回退 `general` |
| `src/lib/agentIcon/state.ts` | 状态归一化 + 迁移 reducer（乱序/锁存） | 纯函数，幂等 |
| `src/lib/agentIcon/animation.ts` | 帧选择 + 动画时钟（单例 rAF） | 只读 manifest，无每图标定时器 |
| `src/lib/agentIcon/assets.ts` | manifest 装载/校验/缺资源上报（模块级单例） | 只读一次，不做 per-render 动态 import |
| `src/components/agent-icon/AgentIcon.tsx` | 纯渲染 5 层 | 只读 ViewModel + assets |
| `src/assets/agent-icon/` | **唯一生产资源目录**（§6） | 由同步脚本生成 |
| `src/__tests__/agent-icon-*.test.ts` | 单元/组件/契约测试 | 走现有 `tsx` 测试模式 |

模块边界硬性约定：

- 组件不计算身份、不查任务映射、不推导状态——这些都由 lib 层产出的 `AgentIconViewModel` 给定。
- `state.ts` 不感知 DOM/React；动画帧号由 `animation.ts` 给出，组件只做 `background-position`/`src` 切换。
- 所有可漂移事实（数量、fps、文件名）只从 manifest 读；代码里不允许出现 `9`、`15`、`6`（fps）这类魔法数字副本，数量断言只存在于契约测试与同步脚本。

---

## 5. 类型与算法

### 5.1 ViewModel 类型

```ts
export type AgentEyeStatus = "running" | "problem" | "success" | "failure" | "cleanup";

export type AgentHeadwear = { kind: "hat" | "hair"; id: string };

export interface AgentIconViewModel {
  sessionId: string;              // 稳定身份 seed（空则回退 node.key/sessionPath，见 §5.2）
  frameId: string;                // manifest.frames[].id，9 选 1
  headwear: AgentHeadwear;        // 帽子或头发二选一，由 identity.ts 保证
  workspaceBadge: {
    templateUrl: string;          // assets/templates/workspace-badge.png
    iconKey: ProjectIconKey;       // 规范化后的 ProjectNode.projectIcon
    color: string;                // 规范化后的 ProjectNode.projectColor
    stableKey: string;            // workspaceRoot；缺显式配置时用于稳定回退
  };
  taskToolId: string;             // manifest.tools[].id，未知任务回退 "general"
  eyeStatus: AgentEyeStatus;      // 由 state.ts 归一化
  eyeFrame: number;               // 0..5，由 animation.ts 给出（含 reduced-motion）
  statusReason?: string;          // problem/failure 的可查看原因
  statusUpdatedAt: number;        // 状态排序、去重与诊断
  reducedMotion: boolean;
  missingLayers: string[];        // 缺失的层 id，组件据此降级并上报
}
```

`AgentIcon` 组件签名：`({ viewModel }: { viewModel: AgentIconViewModel })`，内部对每层渲染一个 `<img>`（眼睛可用 sprite 背景）；Workspace 模板内的 glyph 根据已规范化的 `iconKey` 渲染。组件不做身份、任务或状态推导。DOM 顺序即图层顺序：frame → headwear → eyes → workspaceBadge → taskTool。

### 5.2 稳定 hash（禁止 Math.random）

推荐 FNV-1a 32-bit，纯函数、确定性、实现约 10 行；seed 取 `sessionId`，为空时回退 `node.key`，再为空回退规范化后的 `sessionPath`（同一行必须始终得到同一 seed）。用域分隔后缀避免不同维度同值碰撞（沿用资源 README 的 `":frame"` / `":headwear"` 约定）：

```ts
export function hashString(input: string): number {
  // FNV-1a 32-bit
  let h = 0x811c9dc5;
  for (let i = 0; i < input.length; i++) {
    h ^= input.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

export function stableIndex(seedKey: string, domain: string, mod: number): number {
  return hashString(`${seedKey}:${domain}`) % mod;
}
```

纪律：身份选择链路里禁止 `Math.random`、`Date.now`、`crypto.getRandomValues`；组件每次渲染重复调用同一 seed 必须得到同一结果（用 `useMemo` 缓存 per-session 结果，见 §7）。

### 5.3 身份选择（9 色 / 15 帽 / 15 发、互斥）

```ts
export function pickIdentity(seedKey: string, manifest: AgentManifest): Pick<AgentIconViewModel, "frameId" | "headwear"> {
  const frame = manifest.frames[stableIndex(seedKey, "frame", manifest.frames.length)];   // 9
  const slot = stableIndex(seedKey, "headwear", manifest.hats.length + manifest.hair.length); // 30
  // 互斥：帽子与头发共用一个槽位，二选一，绝不双显
  const headwear: AgentHeadwear =
    slot < manifest.hats.length
      ? { kind: "hat", id: manifest.hats[slot].id }
      : { kind: "hair", id: manifest.hair[slot - manifest.hats.length].id };
  return { frameId: frame.id, headwear };
}
```

- 槽位取模必须用 `hats.length + hair.length`（当前 30），而不是两个独立取模——否则帽发会同时出现。
- 数组越界防御：若 manifest 中找不到 `id`（同步滞后），回退 `manifest.frames[0]` / `hats[0]` 并在 `missingLayers` 上报。
- 外壳色与头部件**无业务语义**：不得用任务或状态参与选择（manifest `identityRule.semantic = false`）。

### 5.4 任务映射与 24 工具扩展

业务层维护“任务枚举 → manifest 真实 tool id”的单一映射表（`task.ts`）。下表为建议枚举（实现时以业务已有任务类型为准，只换 tool id 列；映射必须一对一，不能按当前 UI 文案猜测）：

| 业务任务 | tool id（manifest.tools[].id） |
|---|---|
| code_review | `review` |
| debug / bug_fix | `debug` |
| test / 验证 | `test` |
| terminal / shell | `terminal` |
| implementation / coding | `code` |
| research / investigate | `research` |
| browse / web | `browser` |
| writing / 内容 | `writing` |
| documentation | `docs` |
| plan / planning | `plan` |
| data / 分析 | `data` |
| database | `database` |
| design | `design` |
| image / media | `image` |
| chat / 协作 | `chat` |
| deploy / release | `deploy` |
| build | `build` |
| git / 版本管理 | `git` |
| files / 工程结构 | `files` |
| automation | `automation` |
| security | `security` |
| config / 配置 | `config` |
| monitor / 运维 | `monitor` |
| 未知 / 其他 | `general`（兜底） |

规则：

- **未知任务一律回退 `general`**；任务信号缺失也显示 `general`，并在 `missingLayers`/日志显式记录一次（可观测，不静默）。
- 渲染侧按 `manifest.tools` 查找 id，查不到同样回退 `general`，保证新旧 manifest 混用不破图。
- **24 个工具的扩展路径**（三步，缺一不可）：
  1. `docs/Agent Icon/generate-assets.mjs` 的 `toolDefs` 数组新增条目，**并同步更新生成器里的数量断言**（当前 `hats.length !== 15 || hair.length !== 15 || frameColors.length !== 9 || toolDefs.length !== 24` 会直接失败）；
  2. 重新执行生成器重建 SVG/PNG 与 `manifest.json`；
  3. 前端 `task.ts` 映射表新增一行，同步脚本（§6）刷新 `src/assets/agent-icon/`，契约测试的数量断言同步更新。
- 禁止在组件里写死工具名/路径；工具 id 只以 manifest 为准。

### 5.5 Workspace 徽标

- 徽标层 = `templates/workspace-badge.png`（模板给出尺寸、描边、左下位置）+ 模板内部的 workspace 图标。
- 图标来源直接复用 `ProjectNode.projectIcon` / `projectColor` 与 `desktop/frontend/src/lib/projectIcons.ts` 的既有 Workspace 配置；`ProjectTree.tsx` 中的 `ProjectFolderIcon` 已消费同一套数据。徽标 ViewModel 传规范化后的图标 key、颜色和 `workspaceRoot`，不要另建第二套随机 Workspace 图标表。缺少显式配置时，才按 `workspaceRoot` 稳定选择中性图形；禁止按打开顺序或当前排序选择。
- 视觉：徽标小于任务工具（工具是主体，徽标是附属）；固定左下，不随身份移动。

### 5.6 状态归一化、优先级与迁移

输入：`ProjectNode` 快照 + 明确的生命周期信号（`phase: active | terminal`、结果、`problem` 原因、`cleanup` 条件、`turnStartedAt`）。不能只凭 `running=false` 推断成功，因为它也可能表示尚未加载或历史数据缺字段。归一化与迁移全部收敛在 `state.ts` 一个 reducer 里（状态单源、幂等、可重放）。

映射表（输入 → `AgentEyeStatus`）：

| 输入 | eyeStatus |
|---|---|
| `error`（终止失败）或 `failure` 事件 | `failure` |
| 显式 `problem` 信号（携带原因，见下） | `problem` |
| `running=true`，或 `status ∈ thinking / streaming / waiting_confirmation / background_job / paused` | `running` |
| `phase=terminal` 且达到清理条件（业务信号 `cleanupConditionMet`） | `cleanup` |
| `phase=terminal` 且结果成功 | `success` |
| 状态缺失/未知 | `problem` + 显式上报（规范 §9.1：状态缺失必须显示问题眼并暴露原因，不能静默留空） |

优先级（权重从高到低，高者压制低者）：

```
failure > problem > running      （活动阶段：problem 优先于 running）
failure > cleanup > success      （终止阶段：failure 不被 cleanup 覆盖）
```

迁移纪律（对应项目原则：乱序可恢复、失败显式、可重试、幂等）：

- **阶段推进**：`active(running/problem) → terminal(success/failure) → cleanup`；`cleanup` 只允许从成功终态进入。失败单独锁存。
- **失败锁存**：进入 `failure` 后，迟到的 `success`/`cleanup` 事件不得覆盖（按修订号丢弃），必须保持可见，直到用户明确处理（删除/重置）或新 turn 开始。
- **新 turn 解锁**：只有携带递增 `turnStartedAt` 的 `running` 事件可以把 terminal 拉回 `running`。
- **乱序/迟到更新**：reducer 为每个事件携带单调 `revision`（或时间戳+序号），只接受 `revision > current` 的事件；同 revision 重复投递幂等（重放结果一致）。
- **可重试/可观测**：reducer 是纯函数，输入事件可重放；ViewModel 暴露 `status`、`reason`（problem/failure 的原因）、`updatedAt`，日志中记录回退（`general`/未知状态/缺资源）时附带 `sessionId` 与原因。

reducer 骨架：

```ts
export interface AgentStatusState {
  eyeStatus: AgentEyeStatus;
  reason?: string;
  revision: number;
  turnStartedAt?: number;
  terminalLatched: boolean;
}

export function reduceAgentStatus(
  state: AgentStatusState,
  event: AgentStatusEvent, // { revision, turnStartedAt?, kind: "running"|"problem"|"success"|"failure"|"cleanup"|"reset", ... }
  cleanupConditionMet: boolean,
): AgentStatusState {
  if (event.revision <= state.revision) return state;      // 乱序/重复：丢弃
  const startsNewTurn = event.kind === "running"
    && event.turnStartedAt !== undefined
    && event.turnStartedAt > (state.turnStartedAt ?? 0);
  // failure 锁存：只有显式 reset 或可证明更新的 turn 能解锁
  if (state.eyeStatus === "failure" && event.kind !== "reset" && !startsNewTurn) {
    return { ...state, revision: event.revision };
  }
  // ...按优先级表计算新状态，terminal 时置 terminalLatched
}
```

### 5.7 动画控制（fps / loop / holdLast / reduced-motion）

- 帧数固定 6（manifest 每状态 6 帧）；`fps`、`loop`、`holdLast` 一律从 `manifest.eyes[status]` 读取。
- `running`(6fps) / `problem`(3fps) / `cleanup`(2fps)：循环播放。
- `success`(8fps) / `failure`(8fps)：单次播放，播完停在末帧（index 5，manifest `holdLast: true`）。
- **状态变化才重置**：动画状态以 `${sessionId}:${status}` 为 key，`status` 或 `sessionId` 变化时 `startedAt` 归零；同一状态反复渲染不重置。
- 推荐实现：单例 rAF 驱动一个全局时钟（模块级 `tick`），所有图标共享；每个图标按自己的 `fps` 从时钟推导帧号，避免每图标一个 `setInterval`（隐藏 tab 时 rAF 自动节流）。

```ts
// animation.ts（单例时钟 + 纯帧选择）
export function eyeFrameAt(status: AgentEyeStatus, startedAt: number, nowMs: number, reducedMotion: boolean): number {
  const eye = manifest.eyes[status];
  const frameCount = eye.frames.length;
  if (frameCount === 0) return 0; // 同时触发资源契约错误上报
  if (reducedMotion) {
    // 资源 README：循环状态用中间帧，单次状态用末帧；不得用隐藏层关闭动画
    return eye.loop ? Math.floor(frameCount / 2) : frameCount - 1;
  }
  const phase = ((nowMs - startedAt) / 1000) * eye.fps;
  if (!eye.loop) return Math.min(Math.floor(phase), frameCount - 1); // 单次 + 停末帧
  return Math.floor(phase) % frameCount;
}
```

- reduced-motion 检测：`window.matchMedia("(prefers-reduced-motion: reduce)")`，监听 `change` 事件更新 `viewModel.reducedMotion`。
- 渲染方式：眼睛层使用 sprite（`sprites/eyes/<status>.png`，384×64），`background-size: 384px 64px`、`background-position: ${-frame * 64}px 0`；避免逐帧切 `src` 的网络/解码开销。

---

## 6. 资源接入策略（Vite/React）

### 6.1 唯一生产资源位置

`desktop/frontend/src/assets/agent-icon/`（Vite 项目内，构建期可打包、可哈希、可用 `import.meta.url` 静态引用）。内容由同步脚本从 `docs/Agent Icon/assets/` 复制：`png/frames/`、`png/hats/`、`png/hair/`、`png/tools/`、`png/templates/`、`sprites/eyes/` 与 `manifest.json`。

### 6.2 同步与校验

- 同步脚本：`desktop/frontend/scripts/sync-agent-icon.mjs`（node，参照现有 `scripts/check-css-syntax.mjs` 的构建期模式）。行为：解析并校验来源 manifest → 复制到目标目录旁的临时目录 → 校验文件数与 sha256 → 原子替换目标目录。临时目录名固定在目标父目录且每次启动先清理自己的残留；失败保留原目标，不留下半份资源，可安全重试。脚本只管理 `agent-icon/`，禁止对 `src/assets/` 做递归清理。
- 校验：构建前序（接入 `npm run build`，仿 `check-css-syntax`）与契约测试双重保证：断言 manifest 计数（frames 9 / hats 15 / hair 15 / tools 24 / eyes 5×6）、fps 值（6/3/8/8/2）、`loop`/`holdLast`、所有引用文件存在、sprite 宽 384、docs 与 src 的 manifest sha256 一致。**docs 资源变更而未同步 = 构建失败**。
- 禁止事项：
  - 运行时从 `docs/` 读资源（打包产物无 docs）；
  - 每次 render 动态 `import()` manifest 或资源。manifest 静态 import 一次；由 `assets.ts` 使用构建期 `import.meta.glob("../../assets/agent-icon/**/*.png", { eager: true, query: "?url", import: "default" })` 建立路径到 URL 的只读表，运行时只按 manifest 相对路径查表，不拼接磁盘路径。

### 6.3 缺资源降级（不破图、显式上报）

- 每层 `<img>` 挂 `onError`：标记该层缺失 → 该层置为不可见（或使用中性占位：frame 缺失则整图标不渲染并上报；eyes 缺失用 `templates/neutral-led-grid.png`；其余层缺失只隐藏该层）。
- 上报：同一资源去重后 `console.error` + 传入的 `onAssetMissing?` 回调（附 `sessionId`、层、资源路径），失败显式暴露，绝不显示无自然尺寸的破图图标。

---

## 7. 性能、无障碍、特性开关与回滚

### 7.1 性能

- `AgentIcon` 用 `React.memo`；ViewModel 按 `sessionId` 用 `useMemo` 缓存（身份选择、任务映射只算一次）。
- manifest 模块级单例，全应用解析一次。
- 单一 rAF 动画时钟（§5.7），非每图标定时器；行不可见时可选暂停（`IntersectionObserver`，后期阶段）。
- 每行 5 个 64×64 PNG（或 sprite 背景），远小于现有图标开销；preload 5 张眼睛 sprite（合计约 3KB）。

### 7.2 无障碍

- 列表行内图标是装饰性的：保持 `aria-hidden="true"`，状态语义由既有文本（`statusLabel`、未读计数）承担；不把动画帧放进 `aria-label`，不依赖动画传达信息。
- reduced-motion 按 §5.7 处理（显示中间帧/末帧），不隐藏状态层。
- 双编码（形状 + 颜色）已由资源保证；实现不新增“颜色唯一编码”。
- 最小尺寸：列表默认 24px，32px 用于更宽布局；低于 24px 不渲染细节层（保留 frame + 眼睛静态帧/中性占位）。

### 7.3 特性开关与回滚

- 第一版使用 UI 本地开关 `localStorage["wg2.agent-icon"]`（`"on" | "off"`），默认值由发布阶段决定；读取集中在 ViewModel 组装入口，不让各行分别读取。需要跨设备或管理员控制时，再提升为现有 Settings/bridge 的类型化字段，避免两套开关并存。
- 关闭时渲染原 `<MessageSquare size={13} />` 路径（开关代码保留一个版本，不做破坏性删除）。
- 回滚步骤：置 `off` → 下个版本恢复或移除；同步脚本与契约测试可逆（重新同步即恢复）。
- 上线节奏：视觉验收通过前默认 `off`；灰度可先只在 workbench 变体或部分会话开启。

---

## 8. 分阶段实施与测试

每阶段可独立合并，P3 之前开关默认关闭。

| 阶段 | 内容 | 交付与验证 |
|---|---|---|
| P0 | 同步脚本 `sync-agent-icon.mjs` + 资源契约测试 | 契约测试绿：9/15/15/24/5×6、fps、路径存在、sprite 宽、docs↔src sha256 一致 |
| P1 | 纯 lib：`identity.ts` / `task.ts` / `state.ts` / `animation.ts` | 单元测试全绿（见下） |
| P2 | `AgentIcon.tsx` 组件 + ViewModel 组装 | 组件测试：5 层渲染、DOM 顺序、缺图降级、动画 key 重置 |
| P3 | 替换 `.project-tree__session-icon`（开关保护）+ 样式（24/32px、深浅主题） | 视觉走查 + 既有 `project-tree-runtime` 测试仍绿 |
| P4 | 动画驱动、reduced-motion、性能、无障碍、灰度上线 | 最终验收清单（§9） |

测试模式沿用仓库现状（`tsx` 跑 `src/__tests__/`，参照 `test:project-tree-runtime` 等脚本），不要引入新测试框架：

- 单元：hash 稳定（同 seed 同结果）、确定性（源码无 `Math.random`，可加静态断言）、帽发互斥（30 槽位不双显不缺失）、越界回退、未知任务 → `general`、状态优先级与 failure 锁存、乱序/迟到事件丢弃、同 revision 幂等、fps 帧选择、reduced-motion 帧（循环=3、单次=5）。
- 组件：5 层按 manifest `layerOrder` 渲染、缺图不破图且上报一次、`status` 变化触发动画重置（key 变化）。
- 资源契约：manifest 与文件系统一致（数量、fps、路径、sprite 尺寸）、docs 与 `src/assets/agent-icon/` 同步。
- 视觉（人工）：24px/32px、深浅主题、5 状态动画、重启前后身份一致。

---

## 9. 最终验收清单

- [ ] 24px 下五层可分辨，`taskTool` 明显大于 `workspaceBadge`；32px 下无锯齿、无层重叠
- [ ] 深浅主题下均无破图，眼睛对比可读（形状 + 颜色双编码）
- [ ] 同一 `sessionId` 重启/重连后身份（色/帽/发）一致；实现无 `Math.random`
- [ ] 帽子/头发互斥：永不双显、永不缺失
- [ ] 未知任务 → `general`；未知/缺失状态 → `problem` + 显式上报
- [ ] `running`/`problem`/`cleanup` 按 manifest fps 循环；`success`/`failure` 单次并停末帧
- [ ] 状态变化才重置动画；乱序/迟到更新不闪烁、不倒退
- [ ] `failure` 锁存：不被迟到 `success`/`cleanup` 覆盖，用户处理后（或新 turn）才离开
- [ ] reduced-motion：循环状态显示中间帧，单次状态显示末帧
- [ ] 缺资源不显示破图，去重后显式上报一次
- [ ] 开关可回退到 `MessageSquare` 原渲染
- [ ] 图标容器 `aria-hidden`，状态文本独立存在
- [ ] manifest 计数/路径与 [资源 README](assets/README.md) 一致；docs 与生产资源目录 sha256 一致

---

## 10. 相关文档

- [设计规范（总览）](README.zh-CN.md)
- [资源使用说明](assets/README.md)
- [资源清单 manifest](assets/manifest.json)（唯一真源）
- [资源生成器](generate-assets.mjs)（改资源走这里，改完重跑）
