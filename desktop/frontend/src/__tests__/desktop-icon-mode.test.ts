import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { iconHitRect, parseCollapseState, placeIconPopup, quickStartWorkspaceIndex, scaleIconRect, serializeCollapseState } from "../components/widget/desktopIconLayout";
import { quickStartApprovalLabel, quickStartModelLabel, quickStartPreferences } from "../components/widget/quickStartPreferences";

assert.equal(quickStartModelLabel("deepseek-pro/deepseek-v4-pro"), "deepseek-v4-pro", "QuickStart shows the selected model name without a redundant provider prefix");
assert.equal(quickStartModelLabel(""), "未配置", "QuickStart exposes a missing default model explicitly");
assert.deepEqual([quickStartApprovalLabel("ask"), quickStartApprovalLabel("auto"), quickStartApprovalLabel("yolo")], ["需要批准", "自动批准", "全部允许"], "QuickStart shows all configured approval postures");
assert.deepEqual(quickStartPreferences({ defaultModel: "provider/model", defaultToolApprovalMode: "auto", composerSubmitKey: "ctrl_enter" }), { model: "provider/model", approvalMode: "auto", submitKey: "ctrl_enter" }, "QuickStart normalizes the shared new-session settings as one snapshot");

const workspaceKeys = ["auto", "project:D:/Work/A", "project:D:/Work/B", "global"];
assert.equal(quickStartWorkspaceIndex(workspaceKeys, "project:D:/Work/B", "project:D:/Work/A", "global"), 2, "pending retry wins over requested and remembered workspaces");
assert.equal(quickStartWorkspaceIndex(workspaceKeys, "", "project:D:/Work/A", "global"), 1, "source workspace wins over remembered selection");
assert.equal(quickStartWorkspaceIndex(workspaceKeys, "", "", "global"), 3, "QuickStart remembers its last workspace");

const left = placeIconPopup({ left: 0, top: 480, width: 64, height: 72 }, 900, 600, 330);
assert.equal(left.left, 10, "left-edge popup clamps to the safe margin");
assert.equal(left.arrowLeft, 22, "left-edge arrow still targets the icon center");

const middle = placeIconPopup({ left: 418, top: 480, width: 64, height: 72 }, 900, 600, 330);
assert.equal(middle.left, 285, "middle popup centers on its icon");
assert.equal(middle.arrowLeft, 165, "middle arrow stays centered");

const right = placeIconPopup({ left: 836, top: 480, width: 64, height: 72 }, 900, 600, 330);
assert.equal(right.left, 560, "right-edge popup clamps inside the viewport");
assert.equal(right.arrowLeft, 308, "right-edge arrow still targets the source");

for (const zoom of [0.5, 0.8, 1, 1.25, 1.5, 2]) {
  const cssRect = { left: 300 / zoom, top: 420 / zoom, width: 66 / zoom, height: 74 / zoom };
  assert.deepEqual(scaleIconRect(cssRect, zoom), { left: 300, top: 420, width: 66, height: 74 }, `${zoom}x DOM coordinates convert back to Wails window units`);
}

const physicalRect = { left: 300, top: 420, width: 66, height: 74 };
assert.deepEqual(iconHitRect(physicalRect, 1.25), { x: 371, y: 521, width: 90, height: 100 }, "native hit region converts CSS coordinates and padding to 125% physical pixels");
assert.deepEqual(iconHitRect(physicalRect, 1.5), { x: 445, y: 625, width: 108, height: 120 }, "native hit region follows the WebView raster scale at 150%");

const component = readFileSync(resolve(import.meta.dirname, "../components/widget/DesktopIconMode.tsx"), "utf8");
const css = readFileSync(resolve(import.meta.dirname, "../components/widget/desktop-icon-mode.css"), "utf8");
const backend = readFileSync(resolve(import.meta.dirname, "../../../widget_icon_mode.go"), "utf8");
assert.doesNotMatch(component, /onExit|返回主窗口|desktop-icon-exit/, "icon mode is self-contained: no exit-to-main button and no onExit chain");
assert.doesNotMatch(css, /desktop-icon-exit/, "icon mode CSS carries no exit-button styles");
assert.match(css, /:root:has\(\.desktop-icon-mode\)\s*\{[^}]*background:\s*transparent;/s, "native icon hit regions must not expose the app root background");
assert.match(css, /body:has\(\.desktop-icon-mode\),\s*body:has\(\.desktop-icon-mode\) #root\s*\{[^}]*background:\s*transparent\s*!important;/s, "the WebView body and root stay transparent around desktop icons");
assert.match(css, /body:has\(\.desktop-icon-mode\) \.app[\s\S]*display:\s*none\s*!important/, "MainApp stays mounted in React but is removed from layout and compositing, so no descendant can leak through the transparent surface");
assert.doesNotMatch(component, /widget-mode\.css/, "the icons-only path loads desktop-icon-mode.css alone, so the .app visibility rule must live here");
assert.match(component, /SetDesktopIconHitRegions/, "frontend reports visible hit rectangles to the native window");
assert.match(component, /getClientRects\(\)\.length\s*>\s*0/, "popup visibility does not depend on offsetParent semantics");
assert.match(component, /new ResizeObserver\(sync\)/, "native regions follow popup and menu content size changes");
assert.match(component, /regionQueue\.current/, "native region updates are serialized instead of racing");
assert.match(component, /nativeHitPadding\(node\)/, "native regions retain CSS shadows beyond element border boxes");
assert.match(component, /app\.GetDesktopZoomFactor\(\)/, "icon mode reads the active WebView zoom factor");
assert.match(component, /resolveWidgetZoomFrame\(desktopZoom\)/, "icon mode neutralizes WebView zoom for stable desktop-sized icons");
assert.match(component, /iconHitRect\(node\.getBoundingClientRect\(\), window\.devicePixelRatio, nativeHitPadding\(node\)\)/, "native clipping converts WebView CSS rectangles directly to physical pixels");
assert.match(component, /getGamepads[\s\S]+buttons\[6\][\s\S]+buttons\[7\]/, "QuickStart supports LT/RT gamepad edge polling");
assert.match(component, /app\.Settings\(\)[\s\S]+quickStartPreferences\(settings\)/, "QuickStart reads model, approval, and submit key from the shared user settings snapshot");
assert.match(component, /isComposerSubmitKey\(event, preferences\.submitKey, event\.nativeEvent\.isComposing\)/, "QuickStart reuses the shared Enter/Ctrl+Enter and IME-safe submit rule");
assert.match(component, /模型[\s\S]+quickStartModelLabel[\s\S]+审批[\s\S]+quickStartApprovalLabel/, "QuickStart visibly exposes model and approval state");
assert.match(component, /Ctrl\+Enter 发送[\s\S]+Enter 发送/, "QuickStart shows the active keyboard submission hint");
assert.match(component, /DesktopIconSearch\(query\)/, "search popup queries the independent backend history index");
assert.doesNotMatch(component, /SearchPanel items=\{snapshot\.items\}/, "search is independent from the visible icon cap");
assert.match(component, /item\.kind === "room" \|\| item\.kind === "person"/, "Room and person notices both expose inline reply");
assert.match(component, /conversation:\s*notice\?\.conversation[\s\S]+readSequence:\s*notice\?\.readSequence/, "reply retries carry the stable conversation business key before snapshot recovery");
assert.match(component, /addEventListener\("blur", close\)/, "losing desktop-window focus closes menus and popups");
assert.match(component, /QUICK_WORKSPACE_KEY\s*=\s*"wg2\.icon-widget-workspace"/, "QuickStart uses a stable last-workspace key");
assert.match(component, /setQuickWorkspace\(`project:\$\{active\.sourceId\}`\)/, "workspace icons preselect their own workspace in QuickStart");
assert.doesNotMatch(component, /CornerUpRight|desktop-icon__shortcut/, "desktop icons do not render shortcut-arrow badges");
assert.doesNotMatch(css, /desktop-icon__shortcut/, "shortcut-arrow badge styles are removed");
assert.match(component, /desktop-icon__runtime--\$\{item\.status\}/, "thinking and running keep an always-visible compact status below the icon");
assert.match(component, /runtimeStatus\?\.summary/, "the compact status renders live backend activity text");
assert.match(component, /key=\{summary\}/, "streamed thinking text visibly refreshes when its real reasoning tail changes");
assert.match(component, /desktop-icon__runtime-track[\s\S]*<i \/><i \/><i \/>/, "running uses a compact Session-like activity rail without a total-step count");
assert.doesNotMatch(component, /已读取当前 Workspace|即将组织结果|desktop-icon-popup__stages/, "runtime UI does not fabricate future or completed steps");
assert.doesNotMatch(component, /desktop-icon__ring/, "the shared legacy orbit is removed");
assert.match(css, /\.desktop-icon__motion--thinking[^}]*desktop-icon-thinking-breathe/, "thinking has a breathing visual language");
assert.match(css, /\.desktop-icon__motion--running[^}]*desktop-icon-running-trace/, "running has a distinct precise trace animation");
assert.match(css, /\.desktop-icon__runtime--running[^}]*#8cebf0/, "running status uses a distinct cyan treatment");
assert.match(component, /position\.row === "top"/, "component renders a dedicated top row");
assert.match(component, /position\.row === "bottom"/, "component renders a dedicated bottom row");
assert.match(css, /max-height:\s*calc\(7 \* 44px\)/, "search results have a seven-row height cap");
assert.match(backend, /desktopIconHeight\s*=\s*600[\s\S]+desktopIconMinHeight\s*=\s*540/, "native icon window keeps enough persisted-state height for seven search rows");
assert.match(css, /prefers-reduced-motion:\s*reduce/, "motion has a reduced-motion fallback");
assert.match(css, /left:\s*var\(--arrow-left\)/, "popup arrow uses the computed source anchor");

// --- collapse persistence: the WG2 anchor now drags the native window, so
// only the collapsed flag survives under the stable cluster key ---
assert.equal(parseCollapseState(null), false, "missing persisted state is expanded");
assert.equal(parseCollapseState(""), false, "empty persisted state is expanded");
assert.equal(parseCollapseState("not json"), false, "malformed JSON falls back to expanded");
assert.equal(parseCollapseState('{"anchor":{"x":0.5,"y":0.5}}'), false, "legacy state without a collapsed flag defaults to expanded");
assert.equal(parseCollapseState('{"collapsed":true}'), true, "legacy anchor state keeps its collapsed flag");
assert.equal(parseCollapseState('{"collapsed":"yes"}'), false, "non-boolean collapsed flag falls back");
assert.equal(parseCollapseState("true"), true, "a bare boolean round-trips");
assert.equal(parseCollapseState("false"), false, "a bare false round-trips");
assert.deepEqual(JSON.parse(serializeCollapseState(true)), { collapsed: true }, "new writes carry only the collapsed flag");
assert.equal(parseCollapseState(serializeCollapseState(false)), false, "serialize/parse round-trips expanded");

// --- anchor and toggle contracts ---
const logoSymbol = readFileSync(resolve(import.meta.dirname, "../assets/logo-symbol.svg"), "utf8");
assert.match(logoSymbol, /aria-label="WorkGround2"/, "the anchor reuses the real WG2 logo-symbol asset");
assert.match(component, /logo-symbol\.svg/, "the component imports the real logo-symbol.svg for the anchor");
assert.match(component, /aria-label="移动小组件窗口"/, "the WG2 anchor describes that it moves the native widget window");
assert.match(component, /aria-label=\{collapsed \? "展开图标组" : "收起图标组"\}/, "the toggle label distinguishes collapse and expand by state");
assert.match(component, /aria-expanded=\{!collapsed\}/, "the toggle reflects the group visibility for assistive tech");
assert.match(component, /desktop-icon-collapse[^>]*onClick=/, "the toggle is a keyboard-activatable button without drag handlers");
assert.doesNotMatch(component, /desktop-icon-collapse[^>]*onPointerDown/, "the toggle must never start a drag");
assert.doesNotMatch(component, /anchorPointerDown|anchorPointerMove|endClusterDrag|clusterDrag|clampClusterAnchor|viewportLogical|anchorN|setPointerCapture\(event\.pointerId\)[\s\S]{0,80}anchor/, "the anchor has no hand-written pointer-move drag and no normalized cluster position");
const anchorRule = css.match(/\.desktop-icon-anchor\s*\{[^}]*\}/)?.[0] ?? "";
assert.match(anchorRule, /--wails-draggable:\s*drag/, "the WG2 anchor is a native Wails window drag handle");
assert.doesNotMatch(anchorRule, /touch-action/, "the native anchor no longer claims pointer capture");
assert.doesNotMatch(anchorRule, /grabbing/, "the native anchor no longer fakes a grab/grabbing cursor pair");
assert.match(css, /--wails-draggable:\s*no-drag/, "interactive controls explicitly opt out of the native drag region");
assert.match(component, /wg2\.icon-widget-cluster/, "collapse persistence uses the stable localStorage key");
assert.match(component, /writeCollapsedState\(next\)/, "the toggle persists the collapsed flag");
assert.match(component, /readCollapsedState/, "collapse state loads through the validated parser");
assert.match(component, /!collapsed &&[\s\S]*desktop-icon-row--top/, "collapsing unmounts the top icon row");
assert.match(component, /!collapsed &&[\s\S]*desktop-icon-row--bottom/, "collapsing unmounts the bottom icon row");
assert.match(component, /desktop-icon-grid[\s\S]*desktop-icon-controls/, "the control row sits below the icon rows");
assert.match(component, /desktop-icon-collapse[\s\S]*desktop-icon-anchor/, "the toggle sits immediately left of the WG2 anchor");
assert.match(component, /\.desktop-icon-anchor, \.desktop-icon-collapse/, "anchor and toggle report native hit regions so the transparent window receives pointer input");
assert.match(css, /\.desktop-icon-cluster[\s\S]*position:\s*absolute/, "the cluster is positioned by its anchored corner");
assert.match(css, /\.desktop-icon-cluster[\s\S]*right:\s*18px[\s\S]*bottom:\s*18px/, "the cluster stays pinned to the bottom-right corner of the transparent window");
assert.match(css, /\.desktop-icon-controls[\s\S]*justify-content:\s*flex-end/, "the control row is right-aligned under the icon rows");

console.log("desktop icon mode tests passed");
