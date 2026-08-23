# macOS 小组件模式下 Cmd+H 隐藏自身 — 行为分析与待决策

状态：结论已记录，待产品决策
日期：2026-08-23
适用端：WorkGround2 Desktop（macOS）
相关代码：`desktop/menu.go`、`desktop/widget_window_darwin.m`、`desktop/system_quit_darwin.m`、`desktop/window_controls.go`

## 1. 结论

小组件模式下按 `Cmd+H` 会“隐藏自己”，根因是它触发的不是 webview 里的键盘事件，而是 macOS **应用菜单(App menu)** 里标准的 **Hide** 菜单项。该动作是 `-[NSApplication hide:]`，属于**应用级**隐藏：会隐藏整个 NSApplication 的所有窗口。小组件窗口本质上是同一个 Wails 主窗口换皮（icon 模式 = borderless + 透明 + 仅命中区域响应点击），所以小组件会跟着一起消失。

**可以改**，但只能在**原生层 / 菜单层**处理，前端 React 拦不到。推荐方案是复用本项目已经为 `Cmd+M`（Minimize）实现过的“重定向菜单项”机制，对称地处理 `Cmd+H`。

## 2. 机制

1. `desktop/menu.go:22` 直接 `m.Append(menu.AppMenu())` 挂上应用菜单。
2. Wails 的 `AppMenu()` role 自动生成 About / Services / **Hide** / Hide Others / Show All / Quit。其中 Hide 项在 Wails `v2.12.0` 的 `WailsMenu.m` 中写死：

   ```objc
   [appMenu addItem:[self newMenuItem:[@"Hide " stringByAppendingString:appName]
                                    :@selector(hide:) :@"h" :NSEventModifierFlagCommand]];
   ```

   即 `Cmd+H` → key equivalent `h` + Command → action `-[NSApplication hide:]`。

3. `hide:` 是应用级动作，隐藏所有窗口；小组件窗口与该窗口是同一个实例（icon 模式只是换了 `NSWindowStyleMaskBorderless` 等属性），所以被一并隐藏。

## 3. 为什么前端拦不到

`Cmd+H` 是菜单项的 key equivalent，在 AppKit / 菜单层就被消费，不会作为普通 `keydown` 进入 WKWebView 的 JS。因此 `window.addEventListener('keydown', ...)` 无法拦截，必须回到原生层或菜单层。

## 4. 可选方案

| 方案 | 做法 | 评价 |
|---|---|---|
| A（推荐） | 原生 hook，重定向 Hide 菜单项：找到 `action == @selector(hide:)` 的 `NSMenuItem`，改指向 bridge，经 `workGround2HandleNativeWindowAction` 回传 Go 决策 | 与现有 `Cmd+M` 处理完全对称，改动最小、风险最低 |
| B | Go 菜单层不挂 `AppMenu()`，手动重建 App 菜单 | Wails v2.12.0 未暴露单个 `Hide()` role，需手拼 About/Services/Hide/Quit，容易丢系统 Services/About，收益不高于 A |
| C | 小组件做成独立第二个 `NSWindow`，主窗口用 `orderOut:` 而非 `hide:` | `hide:` 仍是应用级，本质仍要拦 hide，最终绕回 A |
| D | 监听 `applicationWillHide:` 后立刻再 `show` | 有闪烁和焦点抖动，不推荐 |
| E | 清空 Hide 菜单项 keyEquivalent，让 Cmd+H 落到 JS | 丢掉用户对 Cmd+H 的习惯；清掉后 WKWebView 能否可靠收到取决于焦点链，不可靠 |

方案 A 的落点：

- `desktop/widget_window_darwin.m`：仿照 `workGround2InstallMinimiseMenuItem`，新增对 `hide:` 菜单项的查找 / 保存 / 重定向 / 恢复（按 action 查找，不用 title，因为 title 带 app 名 `Hide WorkGround2`）。
- `desktop/window_controls.go`：`nativeWindowAction` 枚举新增一个动作，`performNativeWindowAction` 里决定小组件模式下的行为。
- 恢复时要与 install 配对，避免把系统 Hide 永久改坏，并注意不要误伤 Services / Quit 等其它标准项。

## 5. 待产品决策

技术路径确定后，还需确定小组件模式下 `Cmd+H` 的具体语义：

- a) 保持现状：隐藏整个 app（标准行为）。
- b) 忽略：小组件常驻桌面，`Cmd+H` 无效。
- c) 等价于“收起到小组件”（与现有 `Cmd+M` / 黄色按钮同语义）。
- d) 仅在非小组件模式保留标准 hide，小组件模式下改做别的。

注意避免与“收起到小组件”语义重复，也不要破坏 macOS 通用习惯。

## 6. 工作量 / 风险小结

- 最小改动为方案 A：集中在 `widget_window_darwin.m`，复用 `workGround2HandleNativeWindowAction` 回调；Go 侧最多在 `window_controls.go` 加一个枚举值。
- 风险点：恢复逻辑与 install 严格配对；纯前端方案不可行；清 keyEquivalent（方案 E）不建议。
