import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { iconHitRect, parseCollapseState, placeIconPopup, quickStartWorkspaceIndex, scaleIconRect, serializeCollapseState } from "../components/widget/desktopIconLayout";
import { nextQuickStartApproval, quickStartApprovalLabel, quickStartModelLabel, quickStartModelOptions, quickStartPreferences, resolveQuickStartApproval, resolveQuickStartModel, sameQuickStartIntent } from "../components/widget/quickStartPreferences";
import { deleteConfirmNext, projectWorkspaceRows, renameTitle } from "../components/widget/workspaceManager";
import type { ProjectNode } from "../lib/types";

assert.equal(quickStartModelLabel("deepseek-pro/deepseek-v4-pro"), "deepseek-v4-pro", "QuickStart shows the selected model name without a redundant provider prefix");
assert.equal(quickStartModelLabel(""), "未配置", "QuickStart exposes a missing default model explicitly");
assert.deepEqual([quickStartApprovalLabel("ask"), quickStartApprovalLabel("auto"), quickStartApprovalLabel("yolo")], ["需要批准", "自动批准", "全部允许"], "QuickStart shows all configured approval postures");
assert.deepEqual(quickStartPreferences({ defaultModel: "provider/model", defaultToolApprovalMode: "auto", composerSubmitKey: "ctrl_enter" }), { model: "provider/model", approvalMode: "auto", submitKey: "ctrl_enter" }, "QuickStart normalizes the shared new-session settings as one snapshot");

// --- model / approval picker selection logic (pure) ---
const catalog = [
  { ref: "deepseek/deepseek-v4", model: "deepseek-v4", provider: "deepseek", current: false },
  { ref: "openai/gpt-5", model: "gpt-5", provider: "openai", current: true },
];
assert.equal(resolveQuickStartModel("", "deepseek/deepseek-v4", catalog), "deepseek/deepseek-v4", "settings default model wins when nothing is remembered");
assert.equal(resolveQuickStartModel("openai/gpt-5", "deepseek/deepseek-v4", catalog), "openai/gpt-5", "the remembered widget choice wins over the default");
assert.equal(resolveQuickStartModel("ghost/ghost", "deepseek/deepseek-v4", catalog), "deepseek/deepseek-v4", "a stale remembered ref falls back to the default");
assert.equal(resolveQuickStartModel("ghost/ghost", "", catalog), "deepseek/deepseek-v4", "the first configured model is the final fallback");
assert.equal(resolveQuickStartModel("ghost/ghost", "", []), "", "no configured models means no selection (backend defaults apply)");
assert.equal(resolveQuickStartApproval("", "auto"), "auto", "empty remembered approval keeps the settings default");
assert.equal(resolveQuickStartApproval("yolo", "ask"), "yolo", "a remembered real mode wins");
assert.equal(resolveQuickStartApproval("garbage", "auto"), "auto", "a corrupt remembered value falls back to the current settings default");
assert.deepEqual([nextQuickStartApproval("ask"), nextQuickStartApproval("auto"), nextQuickStartApproval("yolo")], ["auto", "yolo", "ask"], "approval clicks cycle through every mode and wrap safely");
assert.deepEqual(quickStartModelOptions(catalog), [
  { ref: "deepseek/deepseek-v4", label: "deepseek-v4", provider: "deepseek", current: false },
  { ref: "openai/gpt-5", label: "gpt-5", provider: "openai", current: true },
], "model options mirror the shared catalog");

// --- send intent idempotency: a retry reuses the requestId only when every
// send input is identical, so the backend receipt gate stays consistent ---
const intent = { id: "icon-new:1", prompt: "fix it", workspace: "auto", model: "deepseek/deepseek-v4", approvalMode: "auto" };
assert.equal(sameQuickStartIntent(intent, { prompt: "fix it", workspace: "auto", model: "deepseek/deepseek-v4", approvalMode: "auto" }), true, "identical retry reuses the requestId");
assert.equal(sameQuickStartIntent(null, { prompt: "fix it", workspace: "auto", model: "deepseek/deepseek-v4", approvalMode: "auto" }), false, "no pending intent starts fresh");
assert.equal(sameQuickStartIntent(intent, { prompt: "fix it", workspace: "auto", model: "deepseek/deepseek-v5", approvalMode: "auto" }), false, "a model change starts a fresh requestId");
assert.equal(sameQuickStartIntent(intent, { prompt: "fix it", workspace: "auto", model: "deepseek/deepseek-v4", approvalMode: "yolo" }), false, "an approval change starts a fresh requestId");
assert.equal(sameQuickStartIntent(intent, { prompt: "fix it", workspace: "global", model: "deepseek/deepseek-v4", approvalMode: "auto" }), false, "a workspace change starts a fresh requestId");
// legacy pending records (pre model/approval) never match the new shape
assert.equal(sameQuickStartIntent({ id: "old", prompt: "fix it", workspace: "auto", model: "", approvalMode: "" }, { prompt: "fix it", workspace: "auto", model: "deepseek/deepseek-v4", approvalMode: "auto" }), false, "legacy pending without model/approval cannot reuse the requestId");
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

const preview = placeIconPopup({ left: 418, top: 480, width: 64, height: 72 }, 900, 600, 108);
assert.equal(preview.left, 396, "content-width preview centers on its icon");
assert.equal(preview.arrowLeft, 54, "content-width preview keeps its arrow centered");

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
assert.match(component, /popupRef\.current[\s\S]+getBoundingClientRect\(\)\.width \* desktopZoom/, "popup placement measures the rendered width in logical window units");
assert.match(component, /const width = popupWidth \|\| fallbackWidth;[\s\S]+placeIconPopup\(rect, viewportWidth, viewportHeight, width\)/, "popup placement uses the rendered preview width instead of the interactive panel maximum");
assert.match(component, /iconHitRect\(node\.getBoundingClientRect\(\), window\.devicePixelRatio, nativeHitPadding\(node\)\)/, "native clipping converts WebView CSS rectangles directly to physical pixels");
assert.match(component, /getGamepads[\s\S]+buttons\[6\][\s\S]+buttons\[7\]/, "QuickStart supports LT/RT gamepad edge polling");
assert.match(component, /app\.Settings\(\)[\s\S]+quickStartPreferences\(settings\)/, "QuickStart reads model, approval, and submit key from the shared user settings snapshot");
assert.match(component, /isComposerSubmitKey\(event, preferences\.submitKey, event\.nativeEvent\.isComposing\)/, "QuickStart reuses the shared Enter/Ctrl+Enter and IME-safe submit rule");
assert.doesNotMatch(component, /<small>模型<\/small>|<small>审批<\/small>/, "QuickStart omits redundant model and approval captions");
assert.match(component, /onClick=\{\(\) => pickApproval\(nextQuickStartApproval\(selectedApproval\)\)\}/, "QuickStart cycles approval directly on click");
assert.doesNotMatch(component, /aria-label="选择审批模式"/, "QuickStart does not open an approval picker");
assert.match(component, /Ctrl\+Enter 发送[\s\S]+Enter 发送/, "QuickStart shows the active keyboard submission hint");
assert.match(component, />Ctrl \+ ←<\/button>[\s\S]+>Ctrl \+ →<\/button>/, "QuickStart workspace buttons explain the Ctrl+ArrowLeft/Right shortcuts");
assert.match(component, /上一个 Workspace（LT 或 Ctrl\+←）[\s\S]+下一个 Workspace（RT 或 Ctrl\+→）/, "LT/RT gamepad hints stay on the workspace buttons");
assert.match(component, /\(event\.ctrlKey \|\| event\.metaKey\)[\s\S]+ArrowLeft[\s\S]+ArrowRight[\s\S]+event\.preventDefault\(\)[\s\S]+switchBy/, "Ctrl+ArrowLeft/Right switch workspaces and stop the default caret movement");
assert.match(component, /StartWidgetConversation\(\{ prompt, workspace, requestId: attempt\.id, model: modelRef, approvalMode \}\)/, "QuickStart sends the selected model and approval mode with the prompt");
assert.match(component, /resolveQuickStartModel\(model, preferences\?\.model \?\? "", models\)/, "QuickStart resolves the real configured model, never a display-only label");
assert.match(component, /resolveQuickStartApproval\(approval, preferences\?\.approvalMode \?\? "ask"\)/, "QuickStart resolves a real approval posture from the picker");
assert.match(component, /sameQuickStartIntent\(pending, \{ prompt, workspace, model: modelRef, approvalMode \}\)/, "a retry reuses the requestId only when model/approval are unchanged");
assert.match(component, /app\.CompleteVocabulary\(vocabularyToken\.prefix, 5\)/, "QuickStart vocabulary completion reuses the shared controller data source");
assert.match(component, /const first = asArray\(items\)\.find[\s\S]+setVocabMatch\(first\)/, "QuickStart follows Session by showing only the best vocabulary match");
assert.match(component, /desktop-icon-popup__vocab-ghost[\s\S]+<span>\{draft\}<\/span><b>\{vocabMatch\.suffix\}<\/b>/, "vocabulary renders as an inline ghost suffix inside the input");
assert.match(component, /event\.key === "Tab"[\s\S]+vocabMatch && vocabToken[\s\S]+acceptVocab\(\)/, "plain Tab accepts the inline vocabulary suffix");
assert.match(component, /event\.key === "Escape" && vocabMatch[\s\S]+setVocabDismissed\(true\)/, "Escape dismisses inline vocabulary until the input changes");
assert.match(component, /RecordVocabularyUse\(vocabMatch\.id, useID\)/, "accepted inline vocabulary keeps the usage receipt");
assert.doesNotMatch(component, /completion\.kind === "vocab"/, "vocabulary never renders as an elevated candidate card");
assert.match(component, /quickStartCompletionKey\(completion, event, composing, preferences\?\.submitKey \?\? "enter"\)/, "QuickStart routes completion keys through the shared key decision");
assert.match(component, /onCompositionStart=[\s\S]+onCompositionEnd=/, "QuickStart tracks IME composition for safe completion and submit");
assert.match(component, /settings-error[\s\S]+onClick=\{loadPreferences\}/, "settings/model load failure is explicitly retryable");
assert.match(component, /DesktopIconSearch\(query\)/, "search popup queries the independent backend history index");
assert.doesNotMatch(component, /SearchPanel items=\{snapshot\.items\}/, "search is independent from the visible icon cap");
assert.match(component, /item\.kind === "room" \|\| item\.kind === "person"/, "Room and person notices both expose inline reply");
assert.match(component, /conversation:\s*notice\?\.conversation[\s\S]+readSequence:\s*notice\?\.readSequence/, "reply retries carry the stable conversation business key before snapshot recovery");
assert.match(component, /addEventListener\("blur", close\)/, "losing desktop-window focus closes menus and popups");
assert.match(component, /const pointerUp =[\s\S]+const current = drag\.current; drag\.current = null;\s*if \(!current\) return;[\s\S]+clickTimer\.current = window\.setTimeout/, "pointer up schedules a click only after a matching primary pointer down");
assert.match(component, /onContextMenu=\{\(event\) => \{ event\.preventDefault\(\); window\.clearTimeout\(clickTimer\.current\); setMenuID\(item\.id\); setActiveID\(""\); \}\}/, "opening the context menu cancels any delayed primary click before showing right-click actions");
assert.match(component, /QUICK_WORKSPACE_KEY\s*=\s*"wg2\.icon-widget-workspace"/, "QuickStart uses a stable last-workspace key");
assert.match(component, /setQuickWorkspace\(`project:\$\{active\.sourceId\}`\)/, "workspace icons preselect their own workspace in QuickStart");
assert.doesNotMatch(component, /CornerUpRight|desktop-icon__shortcut/, "desktop icons do not render shortcut-arrow badges");
assert.doesNotMatch(css, /desktop-icon__shortcut/, "shortcut-arrow badge styles are removed");
assert.match(component, /desktop-icon__runtime--\$\{item\.status\}/, "thinking and running keep an always-visible compact status above the icon");
assert.match(component, /runtimeStatus\?\.summary/, "the compact status renders live backend activity text");
assert.match(component, /key=\{summary\}/, "streamed thinking text visibly refreshes when its real reasoning tail changes");
assert.match(component, /desktop-icon__runtime-track[\s\S]*<i \/><i \/><i \/>/, "running uses a compact Session-like activity rail without a total-step count");
assert.doesNotMatch(component, /已读取当前 Workspace|即将组织结果|desktop-icon-popup__stages/, "runtime UI does not fabricate future or completed steps");
assert.doesNotMatch(component, /desktop-icon__ring/, "the shared legacy orbit is removed");
assert.match(css, /\.desktop-icon__motion--thinking[^}]*desktop-icon-thinking-breathe/, "thinking has a breathing visual language");
assert.match(component, /desktop-icon-wrap[^\n]*>[\s\S]*<RuntimeIndicator item=\{item\} \/>[\s\S]*<button[^>]*>[\s\S]*desktop-icon__art/, "thinking and running status uses a dedicated layer outside the icon button");
assert.match(css, /\.desktop-icon__runtime[^}]*position:\s*absolute[^}]*pointer-events:\s*none[^}]*translate\(-50%,\s*calc\(-100% - 3px\)\)/, "the runtime layer stays above the icon without contributing to grid row height");
assert.doesNotMatch(css, /\.desktop-icon--thinking,\s*\.desktop-icon--running\s*\{[^}]*min-height/, "runtime states keep the regular icon cell height");
assert.match(component, /desktop-icon__motion-corner[^\n]*desktop-icon__motion-corner[^\n]*desktop-icon__motion-corner[^\n]*desktop-icon__motion-corner/, "running renders four explicit scan corners");
assert.doesNotMatch(css, /-webkit-mask-composite|conic-gradient|desktop-icon-running-trace/, "running has no WebView mask or continuous rotating ring");
assert.match(css, /\.desktop-icon__motion-corner[^}]*desktop-icon-running-frame[^}]*steps\(1, end\)/, "running uses a discrete sequence-frame animation");
assert.match(css, /\.desktop-icon__motion-corner:nth-child\(4\)/, "all four scan-frame corners have explicit placement");
assert.match(css, /\.desktop-icon__runtime--running[^}]*#8cebf0/, "running status uses a distinct cyan treatment");
assert.match(component, /position\.row === "top"/, "component renders a dedicated top row");
assert.match(component, /position\.row === "bottom"/, "component renders a dedicated bottom row");
assert.match(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__search\)[^}]*max-height:\s*calc\(100vh - 114px\)/, "search popup stays within the viewport above its fixed bottom anchor");
assert.match(css, /\.desktop-icon-popup__results[^}]*flex:\s*1 1 auto[^}]*min-height:\s*0[^}]*overflow-y:\s*auto/, "search results shrink and scroll instead of clipping the popup");
assert.match(css, /max-height:\s*calc\(7 \* 44px\)/, "search results have a seven-row height cap");
assert.match(backend, /desktopIconWidth\s*=\s*1080[\s\S]+desktopIconHeight\s*=\s*720[\s\S]+legacyIconWidth\s*=\s*900[\s\S]+legacyIconHeight\s*=\s*600/, "native icon window enlarges to 1080×720 and recognizes the legacy default for migration");
assert.match(css, /prefers-reduced-motion:\s*reduce/, "motion has a reduced-motion fallback");
assert.match(css, /left:\s*var\(--arrow-left\)/, "popup arrow uses the computed source anchor");
assert.match(css, /\.desktop-icon-popup textarea\s*\{[^}]*resize:\s*none/, "the QuickStart textarea cannot be resized by its corner handle");
assert.match(css, /\.desktop-icon-popup__quick-chip\s*\{/, "model/approval chips are compact clickable controls");
assert.match(css, /\.desktop-icon-popup__picker\s*\{[^}]*position:\s*absolute/, "the picker menus float above the QuickStart content");
assert.match(css, /\.desktop-icon-popup__completion\s*\{[^}]*max-height:\s*128px/, "slash completion candidates stay in a compact roughly three-row scroll area");
assert.match(css, /\.desktop-icon-popup__quick-composer\s*\{[^}]*position:\s*relative[^}]*overflow:\s*hidden/, "QuickStart clips the Session-style ghost suffix to the textarea");
assert.match(css, /\.desktop-icon-popup__vocab-ghost\s*\{[^}]*color:\s*transparent[^}]*white-space:\s*pre-wrap[^}]*pointer-events:\s*none/, "the ghost mirrors multiline input without intercepting interaction");
assert.match(css, /\.desktop-icon-popup__vocab-ghost b\s*\{[^}]*color:\s*#727784/, "only the suggested vocabulary suffix is visibly muted");
assert.match(component, /desktop-icon-popup__actions desktop-icon-popup__actions--quick/, "QuickStart actions have a dedicated compact style hook");
assert.match(css, /\.desktop-icon-popup__actions--quick button\s*\{[^}]*min-height:\s*30px;[^}]*padding:\s*4px 10px;/, "QuickStart send and cancel buttons use the compact height");

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

// --- completion notice: fixed OK / Detail / Dismiss with distinct colors ---
assert.match(component, /desktop-icon-popup__ok"[\s\S]{0,160}onClick=\{onClose\}>OK<\/button>[\s\S]{0,200}desktop-icon-popup__detail"[\s\S]{0,200}disabled=\{busy\} onClick=\{\(\) => run\("open"\)\}>Detail<\/button>[\s\S]{0,200}desktop-icon-popup__dismiss"[\s\S]{0,200}disabled=\{busy\} onClick=\{\(\) => run\("dismiss"\)\}>Dismiss<\/button>/, "completion notices always render OK / Detail / Dismiss in fixed order with their class contracts");
assert.match(component, /desktop-icon-popup__ok"[\s\S]{0,160}onClick=\{onClose\}>OK/, "OK only closes the popup locally");
assert.match(component, /desktop-icon-popup__ok"\s*onClick=\{onClose\}/, "OK is not gated by busy: the class is directly followed by the local-close handler");
assert.doesNotMatch(component, /desktop-icon-popup__ok[\s\S]{0,200}run\("ok"\)/, "OK never dispatches a backend ok action");
assert.doesNotMatch(component, /\[\"ok\", \"dismiss\", \"later\", \"open\", \"reply\"\]/, "ok is no longer a backend-acknowledged action that closes the popup");
assert.match(component, /\[\"dismiss\", \"later\", \"open\", \"reply\", \"continue\"\]/, "dismiss/open/continue close the popup after a successful backend roundtrip");
assert.match(component, /onClose=\{\(\) => \{ setActiveID\(""\);\s*setPreviewID\(""\);\s*\}\}/, "OK closes the popup and its hover preview together");
assert.match(component, /desktop-icon-popup__dialog-trigger[\s\S]{0,160}>对话框<\/button>/, "completion notices expose a collapsed conversation entry below the action row");
assert.match(component, /dialogOpen &&[\s\S]+<textarea autoFocus[\s\S]+告诉 WorkGround2 接下来要完成什么/, "focusing the conversation entry expands it into a taller composer");
assert.match(component, /run\(\"continue\", \[text\]\)/, "the completion composer continues the current task instead of starting a separate conversation");
assert.match(component, /event\.key === \"Enter\" && \(event\.ctrlKey \|\| event\.metaKey\)[\s\S]+sendFollowup\(\)/, "Ctrl+Enter submits the continuation");
assert.match(component, /onClick=\{sendFollowup\}>[\s\S]{0,80}\"发送\"[\s\S]{0,100}onClick=\{closeDialog\}>取消<\/button>/, "the expanded composer provides explicit send and cancel actions");
assert.match(component, /notice\.summaryStatus === "failed"/, "a failed summary surfaces an explicit retryable hint");
assert.match(css, /\.desktop-icon-popup__summary-failed/, "the failed-summary hint has its own style");
const okRule = css.match(/\.desktop-icon-popup button\.desktop-icon-popup__ok\s*\{[^}]*\}/)?.[0] ?? "";
const detailRule = css.match(/\.desktop-icon-popup button\.desktop-icon-popup__detail\s*\{[^}]*\}/)?.[0] ?? "";
const dismissRule = css.match(/\.desktop-icon-popup button\.desktop-icon-popup__dismiss\s*\{[^}]*\}/)?.[0] ?? "";
assert.ok(okRule && detailRule && dismissRule, "all three completion buttons carry explicit semantic styles");
const ruleColor = (rule: string) => rule.match(/background:\s*([^;]+)/)?.[1] ?? "";
assert.notEqual(ruleColor(okRule), ruleColor(detailRule), "OK and Detail use distinct semantic colors");
assert.notEqual(ruleColor(detailRule), ruleColor(dismissRule), "Detail and Dismiss use distinct semantic colors");
assert.notEqual(ruleColor(okRule), ruleColor(dismissRule), "OK and Dismiss use distinct semantic colors");
assert.match(css, /\.desktop-icon-popup__actions--completion button\s*\{[^}]*min-height:\s*30px;[^}]*padding:\s*4px 10px;/, "the three completion actions use a shorter compact height");
assert.match(css, /\.desktop-icon-popup__dialog textarea\s*\{[^}]*min-height:\s*112px;/, "the focused completion composer expands to a clearly taller input");
assert.match(backend, /Status:\s*\"pending\", Action:\s*\"continue\"/, "task continuation persists a pending receipt before delivery");
assert.match(backend, /advanceDesktopIconTaskContinue[\s\S]+tryDesktopIconReply/, "task continuation uses the acknowledged and recoverable user-turn pipeline");
assert.match(css, /\.desktop-icon-popup__actions button:not\(:disabled\):hover/, "action buttons keep an accessible hover state");
assert.match(css, /button:focus-visible[\s\S]*outline: 2px solid #70dfe8/, "action buttons keep a visible focus ring");
assert.match(css, /button:disabled\s*\{\s*opacity: \.55/, "disabled buttons stay visibly disabled");

// --- workspace management: the fixed workspace icon between 新建 and 委托 ---
// The backend fixed bar is the declared Go contract: 新建 → 工作区 → 委托 → 搜索.
assert.match(backend, /\{"new", "新建", "plus"\},\s*\{"workspace", "工作区", "workspace"\},\s*\{"delegate", "委托", "users"\},\s*\{"search", "搜索", "search"\}/, "backend fixed bar order is 新建 → 工作区 → 委托 → 搜索 by declaration");
assert.match(component, /item\.sourceId === "workspace"\) return <Folder \/>/, "the workspace fixed icon renders a clear folder glyph");
assert.match(component, /item\.kind === "fixed" && item\.sourceId === "workspace"[\s\S]{0,200}setActiveID\(item\.id\)/, "single click on the workspace icon opens the management dialog");
assert.doesNotMatch(component, /item\.sourceId === "workspace"[\s\S]{0,80}run\(item, "open"\)/, "the workspace icon never runs the generic fixed action");
assert.match(component, /active\.sourceId === "workspace" && <WorkspaceManager/, "the workspace popup renders the management dialog");
assert.match(component, /active\.sourceId !== "workspace"/, "the generic fixed popup fallback explicitly excludes the workspace icon");
assert.match(component, /const reload = useCallback\(async \(\) => \{[\s\S]+app\.ListProjectTree\(\)[\s\S]+projectWorkspaceRows\(tree\)/, "the manager loads its authoritative workspace list from ListProjectTree");
assert.match(component, /app\.PickWorkspace\(\)[\s\S]+if \(root\) await reload\(\)/, "a successful workspace pick reloads and keeps the dialog open; a cancelled picker is a no-op");
assert.match(component, /app\.SetProjectPinned\(row\.root, !row\.pinned\)[\s\S]+await reload\(\)/, "Pin toggles through SetProjectPinned then reloads the authoritative list");
assert.match(component, /app\.RenameProject\(row\.root, renameTitle\(renameDraft\)\)/, "rename commits the raw input through the shared empty-title contract");
assert.match(component, /留空恢复目录名/, "the rename editor explains the empty-title restore semantics");
assert.match(component, /deleteConfirmNext\(armed, row\.root\)[\s\S]+next\.confirmed\) void confirmDelete\(row\)/, "delete uses the two-step confirm state machine before calling the backend");
assert.match(component, /app\.RemoveWorkspace\(row\.root\)[\s\S]+setArmed\(null\)[\s\S]+await reload\(\)/, "delete calls the backend only on the confirmed step, then clears the arm and reloads");
assert.doesNotMatch(component, /RemoveWorkspace[\s\S]{0,60}setRows/, "delete never optimistically removes the row before the backend confirms");
assert.match(component, /catch \(cause\) \{\s*\/\/ The row stays[\s\S]+setError\(cause instanceof Error \? cause\.message : String\(cause\)\)/, "delete failure keeps the row and the armed retry entry");
assert.match(component, />修改图标<small className="desktop-icon-popup__workspace-soon">即将支持<\/small>/, "the icon button is an explicit 即将支持 placeholder");
assert.doesNotMatch(component, /app\.SetProjectIcon/, "the placeholder must never call SetProjectIcon or write icon data");
assert.match(component, /function WorkspaceGlyph[\s\S]+case "star": return <Star[\s\S]+case "bookmark": return <Bookmark[\s\S]+case "code": return <Code2[\s\S]+case "terminal": return <SquareTerminal[\s\S]+case "bolt": return <Zap[\s\S]+default: return <Folder/, "the workspace row maps project icons through the same key→Lucide glyphs as ProjectTree, with a folder fallback");
assert.match(component, /<WorkspaceGlyph icon=\{row\.icon\} \/>/, "the workspace row renders the icon through the dedicated glyph component");
assert.doesNotMatch(component, /row\.icon \? row\.icon/, "the workspace row never renders the raw icon string");
assert.match(component, /onKeyDown=\{\(event\) => \{[\s\S]{0,80}if \(event\.key === "Enter" && !event\.nativeEvent\.isComposing\)[\s\S]+void commitRename\(row\)[\s\S]+if \(event\.key === "Escape"\) \{ event\.preventDefault\(\); event\.stopPropagation\(\); cancelRename\(\); \}/, "rename confirms with Enter and cancels with Escape without closing the dialog");
assert.match(component, /if \(renamingBusy\) return;[\s\S]+setRenamingBusy\(true\)/, "rename guards against duplicate submission");
assert.match(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__workspaces\)[^}]*max-height:\s*calc\(100vh - 114px\)/, "the workspace popup stays within the viewport above its fixed bottom anchor");
assert.match(css, /\.desktop-icon-popup__workspace-list[^}]*max-height:\s*calc\(5 \* 72px\)[^}]*overflow-y:\s*auto/, "the workspace list scrolls with a bounded height");
assert.match(css, /\.desktop-icon-popup__workspace-name[^}]*min-width:\s*0[^}]*text-overflow:\s*ellipsis/, "long workspace names truncate instead of overflowing a narrow window");
assert.match(css, /\.desktop-icon-popup__workspace-actions[^}]*flex-wrap:\s*wrap/, "row actions wrap on narrow windows");
assert.match(css, /\.desktop-icon-popup__workspace-pin\[aria-pressed="true"\]/, "the pinned state has a distinct pressed style");

// --- workspace manager pure logic: authoritative rows + two-step delete ---
// projectIcon is a real stable key (star/bookmark/code/terminal/bolt); an
// unknown key ("rocket") and a missing key both normalize to the folder "".
const tree: ProjectNode[] = [
  { key: "global_folder", kind: "global_folder", label: "Global", root: "~" },
  { key: "project_a", kind: "project", label: "Alpha", root: "~/projects/alpha", pinned: true, projectIcon: "star" },
  { key: "project_b", kind: "project", label: "Beta", root: "~/projects/beta", projectIcon: "rocket" },
  { key: "project_c", kind: "project", label: "", root: "~/projects/gamma" },
  { key: "orphan", kind: "topic", label: "topic", root: "~/projects/alpha", topicId: "t1" },
];
assert.deepEqual(projectWorkspaceRows(tree), [
  { root: "~/projects/alpha", label: "Alpha", pinned: true, icon: "star" },
  { root: "~/projects/beta", label: "Beta", pinned: false, icon: "" },
  { root: "~/projects/gamma", label: "~/projects/gamma", pinned: false, icon: "" },
], "rows project only authoritative project nodes, keep the backend tree order, and normalize real/unknown icon keys to the ProjectTree contract");
assert.deepEqual(projectWorkspaceRows([{ key: "global_folder", kind: "global_folder", label: "Global" }]), [], "a tree without project nodes yields an empty list");
assert.deepEqual(deleteConfirmNext(null, "a"), { armed: "a", confirmed: false }, "the first delete click arms the row");
assert.deepEqual(deleteConfirmNext("a", "a"), { armed: null, confirmed: true }, "the second click on the same row confirms");
assert.deepEqual(deleteConfirmNext("a", "b"), { armed: "b", confirmed: false }, "clicking another row re-arms instead of confirming");
assert.deepEqual(deleteConfirmNext(null, ""), { armed: "", confirmed: false }, "rows always have a root for the delete key");
assert.equal(renameTitle("  新名字  "), "新名字", "rename trims surrounding whitespace");
assert.equal(renameTitle("   "), "", "an empty rename stays empty so the backend restores the folder name");

console.log("desktop icon mode tests passed");
