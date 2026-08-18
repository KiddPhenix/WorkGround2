import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { clampClusterAnchor, DEFAULT_CLUSTER_STATE, iconHitRect, parseClusterState, placeIconPopup, quickStartWorkspaceIndex, scaleIconRect, serializeClusterState } from "../components/widget/desktopIconLayout";
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
assert.match(component, /position\.row === "top"/, "component renders a dedicated top row");
assert.match(component, /position\.row === "bottom"/, "component renders a dedicated bottom row");
assert.match(css, /max-height:\s*calc\(7 \* 44px\)/, "search results have a seven-row height cap");
assert.match(backend, /desktopIconHeight\s*=\s*600[\s\S]+desktopIconMinHeight\s*=\s*540/, "native icon window keeps enough persisted-state height for seven search rows");
assert.match(css, /prefers-reduced-motion:\s*reduce/, "motion has a reduced-motion fallback");
assert.match(css, /left:\s*var\(--arrow-left\)/, "popup arrow uses the computed source anchor");

// --- cluster control geometry: whole cluster stays inside the safe band ---
const clusterViewport = { width: 1000, height: 700 };
const clusterSize = { width: 320, height: 210 };
assert.deepEqual(clampClusterAnchor({ right: 500, bottom: 350 }, clusterSize, clusterViewport), { right: 500, bottom: 350 }, "an anchor inside the safe band stays put");
assert.deepEqual(clampClusterAnchor({ right: 1000, bottom: 700 }, clusterSize, clusterViewport), { right: 982, bottom: 682 }, "an anchor at the viewport corner clamps to the bottom-right margin");
assert.deepEqual(clampClusterAnchor({ right: 0, bottom: 0 }, clusterSize, clusterViewport), { right: 338, bottom: 228 }, "an anchor near the top-left clamps so the whole cluster stays visible");
assert.deepEqual(clampClusterAnchor({ right: 300, bottom: 300 }, { width: 2000, height: 2000 }, clusterViewport), { right: 982, bottom: 682 }, "a cluster larger than the viewport keeps the anchor reachable at the viewport edge");
assert.deepEqual(clampClusterAnchor({ right: 400, bottom: 300 }, clusterSize, { width: 200, height: 150 }), { right: 182, bottom: 132 }, "a tiny viewport still keeps the anchor inside its own margins");

// --- cluster control persistence: malformed falls back, valid round-trips ---
assert.deepEqual(DEFAULT_CLUSTER_STATE, { collapsed: false, anchor: { x: 1, y: 1 } }, "default state is bottom-right and expanded");
assert.equal(parseClusterState(null), null, "missing persisted state falls back");
assert.equal(parseClusterState(""), null, "empty persisted state falls back");
assert.equal(parseClusterState("not json"), null, "malformed JSON falls back");
assert.equal(parseClusterState("[]"), null, "array payload falls back");
assert.equal(parseClusterState('{"collapsed":true}'), null, "state without an anchor falls back");
assert.equal(parseClusterState('{"collapsed":"yes","anchor":{"x":0.5,"y":0.5}}'), null, "non-boolean collapsed flag falls back");
assert.equal(parseClusterState('{"collapsed":true,"anchor":{"x":"a","y":0.5}}'), null, "non-numeric anchor falls back");
assert.deepEqual(parseClusterState('{"collapsed":true,"anchor":{"x":0.5,"y":0.25}}'), { collapsed: true, anchor: { x: 0.5, y: 0.25 } }, "valid state round-trips");
assert.deepEqual(parseClusterState('{"anchor":{"x":1.7,"y":-0.3}}'), { collapsed: false, anchor: { x: 1, y: 0 } }, "out-of-range anchor normalizes into 0..1 and a missing collapsed flag defaults to expanded");
assert.deepEqual(parseClusterState(serializeClusterState({ collapsed: false, anchor: { x: 0.8, y: 0.6 } })), { collapsed: false, anchor: { x: 0.8, y: 0.6 } }, "serialize/parse round-trips");

// --- cluster control component/CSS contracts ---
const logoSymbol = readFileSync(resolve(import.meta.dirname, "../assets/logo-symbol.svg"), "utf8");
assert.match(logoSymbol, /aria-label="WorkGround2"/, "the anchor reuses the real WG2 logo-symbol asset");
assert.match(component, /logo-symbol\.svg/, "the component imports the real logo-symbol.svg for the anchor");
assert.match(component, /aria-label="移动图标组"/, "the WG2 anchor exposes a distinct draggable label");
assert.match(component, /aria-label=\{collapsed \? "展开图标组" : "收起图标组"\}/, "the toggle label distinguishes collapse and expand by state");
assert.match(component, /aria-expanded=\{!collapsed\}/, "the toggle reflects the group visibility for assistive tech");
assert.match(component, /desktop-icon-collapse[^>]*onClick=/, "the toggle is a keyboard-activatable button without drag handlers");
assert.doesNotMatch(component, /desktop-icon-collapse[^>]*onPointerDown/, "the toggle must never start a cluster drag");
assert.match(component, /setPointerCapture\(event\.pointerId\)/, "the anchor drag uses pointer capture");
assert.match(component, /clampClusterAnchor\(/, "cluster position is clamped during drag and restore");
assert.match(component, /next\.right \/ viewportLogical\.width/, "dragged positions persist as viewport-normalized coordinates");
assert.match(component, /parseClusterState\(/, "cluster state loads through the validated parser");
assert.match(component, /wg2\.icon-widget-cluster/, "cluster persistence uses a stable localStorage key");
assert.match(component, /!collapsed &&[\s\S]*desktop-icon-row--top/, "collapsing unmounts the top icon row");
assert.match(component, /!collapsed &&[\s\S]*desktop-icon-row--bottom/, "collapsing unmounts the bottom icon row");
assert.match(component, /desktop-icon-grid[\s\S]*desktop-icon-controls/, "the control row sits below the icon rows");
assert.match(component, /desktop-icon-collapse[\s\S]*desktop-icon-anchor/, "the toggle sits immediately left of the WG2 anchor");
assert.match(component, /\.desktop-icon-anchor, \.desktop-icon-collapse/, "anchor and toggle report native hit regions so the transparent window receives pointer input");
assert.match(css, /\.desktop-icon-cluster[\s\S]*position:\s*absolute/, "the cluster is positioned by its anchored corner");
assert.match(css, /\.desktop-icon-controls[\s\S]*justify-content:\s*flex-end/, "the control row is right-aligned under the icon rows");
assert.match(css, /\.desktop-icon-anchor[\s\S]*touch-action:\s*none/, "the anchor owns pointer capture with touch-action none");
assert.match(css, /cursor:\s*grab[\s\S]*grabbing/, "the anchor advertises grab/grabbing affordance");

console.log("desktop icon mode tests passed");
