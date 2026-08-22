# Agent Icon 美术资源

本目录是 [Agent Icon 设计规范](../README.zh-CN.md) 的实际资源包。所有部件使用相同的 64×64 透明画布，可按层直接叠加。

## 资源清单

| 类别 | 数量 | SVG | PNG |
|---|---:|---|---|
| 帽子 | 15 | <code>svg/hats/</code> | <code>png/hats/</code> |
| 头发 | 15 | <code>svg/hair/</code> | <code>png/hair/</code> |
| 外壳边框色 | 9 | <code>svg/frames/</code> | <code>png/frames/</code> |
| 常见任务工具 | 24 | <code>svg/tools/</code> | <code>png/tools/</code> |
| LED 状态动画 | 5 组，共 30 帧 | <code>svg/eyes/&lt;status&gt;/</code> | <code>png/eyes/&lt;status&gt;/</code> |
| LED 横向精灵图 | 5 | — | <code>sprites/eyes/</code> |

完整路径、颜色、帧率、循环方式和文件名以 [manifest.json](manifest.json) 为准。

## 预览

- [全部资源目录](previews/asset-catalog.png)
- [LED 动画逐帧](previews/eye-animation-frames.png)
- [实际组合示例](previews/combinations.png)
- [32px 可读性检查](previews/small-size-check-2x.png)

## 组合契约

所有图层保持 <code>viewBox="0 0 64 64"</code>，按以下顺序叠加：

1. <code>frame</code>：选择 9 个外壳边框色之一；
2. <code>headwear</code>：帽子或头发二选一，不允许同时叠加；
3. <code>eyes</code>：按状态和当前动画帧选择；
4. <code>workspaceBadge</code>：左下 Workspace 徽标；
5. <code>taskTool</code>：右下任务工具，视觉尺寸大于 Workspace 徽标。

每层都已包含完整画布坐标，消费方只需把图片绝对定位到同一矩形：

~~~css
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
~~~

## 身份随机化

- 使用 <code>sessionId</code> 派生稳定 seed；
- 分别从 <code>frames</code> 和 <code>hats + hair</code> 中稳定选取；
- 帽子与头发属于同一个 <code>headwear</code> 槽位，只能选择一个；
- 重复渲染、重启、重连后结果不得变化；
- 外壳色和头部件无业务语义，禁止根据任务或状态选择。

推荐索引：

~~~text
frameIndex = hash(sessionId + ":frame") % 9
headwearIndex = hash(sessionId + ":headwear") % 30
~~~

## 任务工具

| 领域 | 工具 ID |
|---|---|
| 编码 | <code>code</code>, <code>terminal</code>, <code>review</code>, <code>debug</code>, <code>test</code> |
| 调研 | <code>research</code>, <code>browser</code> |
| 内容 | <code>writing</code>, <code>docs</code> |
| 规划与数据 | <code>plan</code>, <code>data</code>, <code>database</code> |
| 设计与媒体 | <code>design</code>, <code>image</code> |
| 协作 | <code>chat</code> |
| 工程交付 | <code>deploy</code>, <code>build</code>, <code>git</code>, <code>files</code>, <code>automation</code> |
| 运维与通用 | <code>security</code>, <code>config</code>, <code>monitor</code>, <code>general</code> |

业务层应维护任务枚举到工具 ID 的单一映射。未知任务使用 <code>general</code>，不能按当前 UI 文案猜测。

## Workspace 徽标

<code>svg/templates/workspace-badge.svg</code> 和对应 PNG 提供左下徽标的尺寸、描边与位置模板。运行时应在模板内部放入项目已有 Workspace 图标；同一 Workspace 必须稳定使用同一个图标。

## LED 帧动画

| 状态 | 帧数 | FPS | 循环 | 结束行为 |
|---|---:|---:|---|---|
| <code>running</code> | 6 | 6 | 是 | 循环 |
| <code>problem</code> | 6 | 3 | 是 | 循环 |
| <code>success</code> | 6 | 8 | 否 | 停在末帧 |
| <code>failure</code> | 6 | 8 | 否 | 停在末帧 |
| <code>cleanup</code> | 6 | 2 | 是 | 低频循环 |

每组同时提供逐帧 SVG、逐帧 PNG 和横向 PNG sprite。sprite 每帧为 64×64，第 n 帧位于 x = n × 64。

系统开启“减少动态效果”时：

- <code>running</code>、<code>problem</code>、<code>cleanup</code> 使用 manifest 中间帧；
- <code>success</code>、<code>failure</code> 直接使用末帧；
- 不通过隐藏状态层来关闭动画。

## 生成与修改

<code>../generate-assets.mjs</code> 是资源源文件。生成目录可重复覆盖，输出由固定数据定义，运行结果幂等；本说明文件不会被生成器删除。

~~~powershell
$env:CODEX_NODE_MODULES = 'C:\Users\admin\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\node_modules'
& 'C:\Users\admin\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\bin\node.exe' '.\docs\Agent Icon\generate-assets.mjs'
~~~

不要直接批量修改 <code>svg/</code>、<code>png/</code>、<code>sprites/</code> 或 <code>previews/</code> 中的生成文件；修改生成脚本后重新执行。

