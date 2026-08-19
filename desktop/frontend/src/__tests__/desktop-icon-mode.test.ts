import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { clusterGridMaxWidth, iconHitRect, ICON_ZOOM_MAX, ICON_ZOOM_MIN, ICON_ZOOM_STEP, normalizeIconZoom, parseCollapseState, parseIconZoom, placeIconPopup, quickStartWorkspaceIndex, scaleIconRect, serializeCollapseState, serializeIconZoom, stepIconZoom, widgetViewportSize } from "../components/widget/desktopIconLayout";
import { CLICK_DELAY, DRAG_THRESHOLD, IconTimers, PREVIEW_CLOSE_DELAY, type TimerHost } from "../components/widget/desktopIconTimers";
import { nextQuickStartApproval, quickStartApprovalLabel, quickStartModelLabel, quickStartModelOptions, quickStartPreferences, resolveQuickStartApproval, resolveQuickStartModel, sameQuickStartIntent } from "../components/widget/quickStartPreferences";
import { deleteConfirmNext, projectWorkspaceRows, renameTitle } from "../components/widget/workspaceManager";
import { roomRows } from "../components/widget/roomsManager";
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
assert.equal(left.maxHeight, 480 - 9 - 10, "maxHeight is the real anchor-top space minus gap and margin");
assert.equal(left.bottom + left.maxHeight, 600 - 10, "a maxHeight-tall popup lands exactly on the top margin");

const middle = placeIconPopup({ left: 418, top: 480, width: 64, height: 72 }, 900, 600, 330);
assert.equal(middle.left, 285, "middle popup centers on its icon");
assert.equal(middle.arrowLeft, 165, "middle arrow stays centered");

const preview = placeIconPopup({ left: 418, top: 480, width: 64, height: 72 }, 900, 600, 108);
assert.equal(preview.left, 396, "content-width preview centers on its icon");
assert.equal(preview.arrowLeft, 54, "content-width preview keeps its arrow centered");

const right = placeIconPopup({ left: 836, top: 480, width: 64, height: 72 }, 900, 600, 330);
assert.equal(right.left, 560, "right-edge popup clamps inside the viewport");
assert.equal(right.arrowLeft, 308, "right-edge arrow still targets the source");

// An anchor near the top keeps only the real leftover headroom: maxHeight
// shrinks to anchor.top - gap - margin and never goes negative, and the top
// edge (viewportHeight - bottom - maxHeight) still cannot cross the margin.
const low = placeIconPopup({ left: 418, top: 40, width: 64, height: 72 }, 900, 600, 330);
assert.equal(low.bottom, 569, "an anchor near the top still places the popup bottom above it");
assert.equal(low.maxHeight, 40 - 9 - 10, "maxHeight is the real leftover anchor-top space");
assert.ok(low.bottom + low.maxHeight <= 600 - 10, "even the leftover headroom cannot cross the top margin");
const clipped = placeIconPopup({ left: 418, top: 10, width: 64, height: 72 }, 900, 600, 330);
assert.equal(clipped.maxHeight, 0, "no usable anchor-top space clamps maxHeight to zero instead of going negative");

const originalViewport = widgetViewportSize(900, 600, 0.8);
const resizedViewport = widgetViewportSize(900, 420, 0.8);
assert.deepEqual(originalViewport, { width: 720, height: 480 }, "the logical viewport converts both CSS axes with desktop zoom");
assert.deepEqual(resizedViewport, { width: 720, height: 336 }, "a height-only resize produces a new logical viewport height");
const resizedPlacement = placeIconPopup({ left: 320, top: 300, width: 64, height: 72 }, resizedViewport.width, resizedViewport.height, 330);
assert.equal(resizedPlacement.bottom, 45, "popup placement consumes the resized logical height instead of a stale window read");

// The vertical constraint stays inside the client for every desktopZoom
// (0.5..2) × clusterZoom (0.75..1.5) combination at the 640×540 minimum
// window: the anchor rect is the real scaleIconRect'd logical geometry of a
// bottom-right cluster icon (cluster zooms around its bottom-right origin, so
// the icon top rises by zoom while the 18px corner margins stay physical).
for (const desktopZoom of [0.5, 1, 2]) {
  for (const clusterZoom of [0.75, 1, 1.25, 1.5]) {
    const viewportWidth = 640 * desktopZoom;
    const viewportHeight = 540 * desktopZoom;
    const anchorTop = viewportHeight - (18 + 116 * clusterZoom) * desktopZoom;
    const anchor = {
      left: viewportWidth - (18 + 66 * clusterZoom) * desktopZoom,
      top: anchorTop,
      width: 66 * clusterZoom * desktopZoom,
      height: 74 * clusterZoom * desktopZoom,
    };
    const width = 330 * desktopZoom;
    const placed = placeIconPopup(anchor, viewportWidth, viewportHeight, width);
    assert.ok(placed.left >= 10, `left edge inside client at desktopZoom ${desktopZoom} × clusterZoom ${clusterZoom}`);
    assert.ok(placed.left + width <= viewportWidth - 10 + 1e-9, `right edge inside client at desktopZoom ${desktopZoom} × clusterZoom ${clusterZoom}`);
    assert.ok(placed.bottom >= 10, `bottom edge inside client at desktopZoom ${desktopZoom} × clusterZoom ${clusterZoom}`);
    assert.ok(placed.maxHeight > 0, `anchor-top space usable at desktopZoom ${desktopZoom} × clusterZoom ${clusterZoom}`);
    assert.ok(placed.maxHeight <= anchorTop - 9 - 10 + 1e-9, `maxHeight never exceeds the real anchor-top space at desktopZoom ${desktopZoom} × clusterZoom ${clusterZoom}`);
    assert.ok(placed.bottom + placed.maxHeight <= viewportHeight - 10 + 1e-9, `popup top edge stays inside the client at desktopZoom ${desktopZoom} × clusterZoom ${clusterZoom}`);
  }
}

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
assert.doesNotMatch(component, /desktop-icon-exit/, "icon mode CSS carries no legacy exit-button class");
assert.doesNotMatch(css, /desktop-icon-exit/, "icon mode CSS carries no exit-button styles");
assert.match(component, /onOpenMain: \(\) => Promise<void>/, "the quick toolbar receives the open-main callback from the root App");
assert.match(component, /const \[quickOpen, setQuickOpen\] = useState\(false\)/, "the quick toolbar owns its own open state");
assert.match(component, /desktop-icon-collapse[\s\S]*desktop-icon-quick[\s\S]*desktop-icon-anchor/, "the quick toolbar renders between the collapse toggle and the WG2 anchor");
assert.match(component, /role="toolbar" aria-label="小组件快捷操作"/, "the quick toolbar is a labelled toolbar surface");
assert.match(component, /aria-label="缩小图标"[\s\S]*aria-label="放大图标"[\s\S]*aria-label="打开主窗口"[\s\S]*aria-label="设置"/, "the quick toolbar exposes zoom out, zoom in, open main, and settings in order");
assert.match(component, /role="switch" aria-checked=\{topmost\}/, "the always-on-top control is a switch with an explicit checked state");
assert.match(component, /DesktopStartupSettings\(\)[\s\S]+widgetAlwaysOnTop/, "the initial always-on-top state comes through the existing startup settings contract");
assert.match(component, /SetDesktopWidgetAlwaysOnTop\(next\)[\s\S]+setTopmost\(next\)/, "the always-on-top switch persists through the backend and only reflects the confirmed config state");
assert.match(component, /catch \(cause\) \{\s*setQuickError\(cause instanceof Error \? cause\.message : String\(cause\)\);[\s\S]*?setTopmostBusy\(false\)/, "a failed always-on-top toggle stays visible in the quick-error channel and retryable without flipping the switch");
assert.match(component, /if \(exiting \|\| topmostBusy \|\| !topmostLoaded \|\| topmostReadFailed\) return;[\s\S]+setTopmostBusy\(true\)/, "the always-on-top toggle is guarded against double submission, exits, and stays disabled after a failed initial read");
assert.match(component, /const openMainWindow = async \(\) => \{[\s\S]*if \(exitRequest\.current\) return;[\s\S]*await onOpenMain\(\)/, "open main guards the async exit round-trip and delegates to the root App");
assert.match(component, /const openSettingsWindow = async \(\) => \{[\s\S]*if \(exitRequest\.current\) return;[\s\S]*await onOpenSettings\(\)/, "open settings reuses the guarded exit-before-open flow");
assert.doesNotMatch(component, /onOpenSettings\(\)\.catch\(\(cause\) => setError/, "settings never opens through an unguarded inline call");
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
assert.match(component, /const width = popupWidth \|\| fallbackWidth;[\s\S]+placeIconPopup\(rect, viewport\.width, viewport\.height, width\)/, "popup placement uses the rendered preview width and current logical viewport");
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
assert.match(component, /const submitted = submitJob\([\s\S]{0,120}\{ prompt, workspace, model: selectedModel, approvalMode: selectedApproval \},[\s\S]{0,80}editJob \? \{ replacesRequestId: editJob\.requestId \} : undefined[\s\S]{0,120}\)/, "QuickStart enqueues the selected model and approval mode with the prompt through the hoisted job runner; an edited job replaces its frozen requestId");
assert.match(component, /if \(!submitted\.ok\) \{ setError\(submitted\.error\); sentRef\.current = false; return; \}[\s\S]{0,60}setDraft\(""\);[\s\S]{0,40}onClose\(\);/, "a successful submit closes the modal synchronously; a validation/persistence failure keeps it open with the error visible");
assert.match(component, /sentRef\.current[\s\S]{0,60}sentRef\.current = true;[\s\S]{0,260}sentRef\.current = false;/, "a same-tick double submit (button + Enter) enqueues exactly one request");
assert.doesNotMatch(component, /StartWidgetConversation\(/, "the QuickStart modal no longer owns the backend promise");
assert.doesNotMatch(component, /wg2\.icon-widget-pending/, "the single pending slot is gone from QuickStart");
const quickStartSource = component.slice(component.indexOf("function QuickStart("), component.indexOf("function SearchPanel("));
assert.doesNotMatch(quickStartSource, /发送中…/, "the QuickStart modal never waits on the backend with a 发送中… state");
assert.match(component, /resolveQuickStartModel\(model, preferences\?\.model \?\? "", models\)/, "QuickStart resolves the real configured model, never a display-only label");
assert.match(component, /resolveQuickStartApproval\(approval, preferences\?\.approvalMode \?\? "ask"\)/, "QuickStart resolves a real approval posture from the picker");
assert.match(component, /useWidgetQuickStartJobs\(app\.StartWidgetConversation\)/, "async delivery is hoisted into DesktopIconMode through the job runner hook");
assert.match(component, /quickJobs\.reconcile\(next\.items\)/, "each refreshed snapshot hands accepted jobs off to their real task icons");
assert.match(component, /const optimisticItems = useMemo\([\s\S]{0,120}quickStartJobItem[\s\S]{0,120}\)/, "optimistic job icons are projected from the job ledger");
assert.match(component, /const mergedItems = useMemo\(\(\) => mergeQuickStartItems\(snapshot\.items, optimisticItems\)/, "optimistic icons merge with the authoritative snapshot items");
assert.match(component, /isQuickStartJobItem\(item\)\) openQuickStartJob\(item\); else void run\(item, "open"\)/, "double-clicking an optimistic job opens its popup (or the real task for an accepted job) instead of dispatching a backend open on the fake opt: id");
assert.match(component, /isQuickStartJobItem\(item\)\) return;[\s\S]{0,80}void run\(item, "move"/, "dragging an optimistic job never dispatches a backend move");
assert.match(component, /if \(isQuickStartJobItem\(item\)\) \{ setActiveID\(item\.id\); setMenuID\(""\); setPreviewID\(""\); \} else \{ setMenuID\(item\.id\); setActiveID\(""\); \}/, "right-clicking an optimistic job opens its popup instead of the backend context menu");
assert.match(component, /<QuickStartJobBody job=\{activeQuickJob\}[\s\S]{0,260}onRetry=\{\(requestId\) => \{ quickJobs\.retry\(requestId\); \}\}[\s\S]{0,120}onEdit=\{editQuickStartJob\}[\s\S]{0,120}onDismiss=\{\(requestId\) => \{ if \(quickJobs\.dismiss\(requestId\)\) \{ setActiveID\(""\); setPreviewID\(""\); \} \}\}[\s\S]{0,80}onOpenMain=\{openMainWindow\}[\s\S]{0,160}onOpenTask=\{activeQuickJob\?\.phase === "accepted" && activeQuickJob\.tabId \? \(\) => void openQuickStartTask\(activeQuickJob\) : undefined\}/, "the optimistic popup exposes retry/edit/dismiss wired to the runner, an open-main action for running jobs, and open-task for accepted jobs");
assert.match(component, /const editQuickStartJob = \(job: QuickStartJob\) => \{[\s\S]{0,80}setQuickStartEditJob\(job\);[\s\S]{0,80}setQuickWorkspace\(job\.intent\.workspace \|\| ""\);[\s\S]{0,80}setActiveID\("fixed:new"\);[\s\S]{0,20}\};/, "editing a failed job passes the frozen intent through state/props (never localStorage) and tracks the source requestId");
const jobsSource = readFileSync(resolve(import.meta.dirname, "../components/widget/widgetQuickStartJobs.ts"), "utf8");
assert.match(jobsSource, /useEffect\(\(\) => \{[\s\S]{0,80}runner\.resume\(\);[\s\S]{0,8}\}, \[runner\]\);/, "mounting resumes nonterminal jobs through the runner");
assert.match(jobsSource, /if \(running\.has\(requestId\)\) return;/, "the in-flight registry guards one dispatch per requestId");
assert.match(jobsSource, /result\.status === "accepted" \|\| result\.status === "already_applied"/, "the backend receipt (accepted/already_applied) drives the accepted phase");
assert.match(component, /desktop-icon__queued/, "a queued job renders a subtle pending dot on the task icon");
// aria/title contracts (#6): an optimistic icon's accessible and mouse state
// always announces 后台发送中 while delivering and 发送失败，可重试 once failed,
// while the icon stays a plain keyboard-operable button.
assert.match(component, /if \(isQuickStartJobItem\(item\)\) return quickStartJobStateLabel\(item\);/, "the optimistic preview announces the delivery state");
assert.match(component, /aria-label=\{`\$\{item\.title\}，\$\{previewText\(item\)\}`\}[\s\S]{0,120}title=\{isQuickStartJobItem\(item\) \? `\$\{item\.title\}，\$\{quickStartJobStateLabel\(item\)\}` : undefined\}/, "optimistic icons carry the state in both aria-label and title");
assert.match(jobsSource, /return item\.status === "failed" \? "发送失败，可重试" : "后台发送中"/, "the state label helper exposes the exact 后台发送中 / 发送失败，可重试 wording");
// no front-end flight timeout / no accepted-grace eviction (#2, #3)
assert.doesNotMatch(jobsSource, /flightTimeoutMs|acceptedGraceMs|QUICK_JOB_FLIGHT_TIMEOUT|QUICK_JOB_ACCEPTED_GRACE|后台发送超时/, "the front end has no flight timeout and no accepted-grace timer");
assert.match(jobsSource, /There is no front-end flight timeout/, "the module documents why the queued backend call is never fenced");
// latest-successfully-applied poll guard (#4): starting a new poll never
// invalidates an older response (slow polls cannot starve each other out when
// calls exceed the 1s interval); only a newer response that actually applied
// makes an older one stale, so an old(no-real) response resolving after a
// new(real) one can never regress to an empty frame or resurrect jobs.
assert.match(component, /const pollGuard = useRef\(createLatestAppliedGuard\(\)\);/, "polls get a latest-successfully-applied generation guard");
assert.match(component, /if \(!pollGuard\.current\.mayApply\(generation\)\) return; \/\/ a newer response already applied[\s\S]{0,80}markApplied\(generation\)/, "only a response newer than the newest APPLIED response may apply; starting a new poll never starves an older response");
// process-level shared runner (#1): every mount shares one runner and one
// in-flight registry, with safe subscribe/unsubscribe.
assert.match(jobsSource, /getSharedQuickStartJobRunner/, "the hook mounts one process-level shared runner");
assert.match(jobsSource, /subscribe\(onJobs\) \{[\s\S]{0,60}subscribers\.add\(onJobs\)[\s\S]{0,60}return \(\) => \{ subscribers\.delete\(onJobs\); \}/, "subscription mount/unmount is safe on the shared runner");
assert.match(jobsSource, /const commit = \(transition: QuickStartTransition[\s\S]{0,260}const durable = readQuickStartJobs\(storage\);[\s\S]{0,120}saveQuickStartJobs\(storage, next\)/, "every transition merges against the current persisted ledger (merge-on-write) and refuses to write over an unreadable ledger");
assert.match(jobsSource, /const dirty = new Map<string, QuickStartDirtyIntent>\(\);/, "background transitions keep a typed per-request dirty overlay for unpersistable intents");
assert.match(jobsSource, /dirty\.clear\(\);[\s\S]{0,10}catch \(cause\) \{[\s\S]{0,40}error = cause instanceof Error \? cause\.message : String\(cause\);[\s\S]{0,40}const memoryBase = applyDirty\(jobs\);/, "dirty entries are cleared only after a save that included them succeeded; failures replay them over the memory view");
assert.match(jobsSource, /applyDirty\(readQuickStartJobs\(storage\)\)[\s\S]{0,300}saveQuickStartJobs\(storage, next\);[\s\S]{0,10}dirty\.clear\(\);/, "submit merges pending dirty intents into the durable base before saving (a submit can never regress a pending phase)");
assert.match(jobsSource, /if \(current\.phase === "running" \|\| \(current\.phase === "accepted" && !current\.tabId\)\) return false;/, "dismiss refuses running jobs and accepted jobs without a real tabId (only terminal-with-receipt entries may be removed)");
assert.match(jobsSource, /commit\(\{ kind: "remove", requestId \}, false\);/, "dismiss removes the icon only after the durable save succeeds");
// accepted job opens its real task through the same open action real task
// icons use (#3): the gate passes ExitWidgetMode(tabId) exactly once per
// invocation (double-click guard) and stays safe even when the real
// task:<tabId> icon is filtered/capped out of the snapshot.
assert.match(component, /quickTaskGate\.current\.open\(job, \(tabId\) => app\.ExitWidgetMode\(tabId\)\)/, "an accepted job opens its real task through the gate, passing the exact tabId once");
assert.match(component, /const quickTaskGate = useRef\(createQuickStartOpenTaskGate\(\)\);/, "the accepted open-task action is gated against double invocation");
// storage failures are explicit and visible (#5)
assert.match(component, /quickJobs\.storageError[\s\S]{0,80}quickJobs\.clearStorageError\(\)/, "durable-storage failures surface in the toast and are dismissible");
assert.match(component, /decideConsumedDraft\(localStorage, localStorage\.getItem\(QUICK_DRAFT_KEY\) \|\| "", activeQuickStartDrafts\)/, "the PURE decision receives active prompt→requestId mappings so an already-enqueued draft is suppressed and can be cleaned durably");
assert.match(component, /initialDraft=\{quickDraftDecision\.draft\}/, "QuickStart renders the pure decision's draft without mutating storage");
assert.doesNotMatch(component, /initialDraft=\{\(\(\) => \{[\s\S]{0,300}(removeItem|setItem|clearConsumedDraftMarker|cleanupConsumedDraft)/, "the initial-draft render path never writes storage (an aborted or StrictMode render cannot remove the draft/marker)");
assert.match(component, /useEffect\(\(\) => \{[\s\S]{0,180}active && active\.sourceId === "new"[\s\S]{0,200}cleanupConsumedDraft\(localStorage, quickDraftDecision\.cleanupMarker\)[\s\S]{0,120}setQuickError/, "the committed cleanup can recreate a missing marker from the active requestId before removing the draft, and failures stay visible");
assert.match(component, /recordConsumedDraftMarker\(localStorage, trimmed, result\.requestId\)[\s\S]{0,200}localStorage\.removeItem\(QUICK_DRAFT_KEY\)[\s\S]{0,120}clearConsumedDraftMarker\(localStorage\)/, "the consumed-draft marker is recorded BEFORE the best-effort draft removal and cleared once the removal succeeds");
assert.match(component, /后台发送中，请等待/, "a running optimistic job explains itself with 后台发送中，请等待");
assert.match(component, /job\.phase === "running" && <button onClick=\{onOpenMain\}>打开主窗口<\/button>/, "a running job offers an open-main action, never a delete");
assert.match(component, /const dismissible = failed \|\| \(job\.phase === "accepted" && Boolean\(job\.tabId\)\);/, "only failed and accepted-with-tabId entries are dismissible");
assert.match(component, /\{dismissible && <button className="subtle" onClick=\{\(\) => onDismiss\(job\.requestId\)\}>丢弃<\/button>\}/, "the dismiss action renders only when the entry is dismissible (never for running jobs)");
assert.doesNotMatch(component, /后台发送中，请等待[\s\S]{0,120}onDismiss[\s\S]{0,80}丢弃/, "the running-job popup never offers deletion next to its waiting message");
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
assert.match(component, /const pointerUp =[\s\S]+const current = drag\.current; drag\.current = null;\s*if \(!current\) return;[\s\S]+timers\.current\?\.scheduleClick/, "pointer up schedules a click only after a matching primary pointer down");
assert.match(component, /onContextMenu=\{\(event\) => \{ event\.preventDefault\(\); cancelTransientTimers\(\); setAnchorMenuOpen\(false\); setQuickOpen\(false\); if \(isQuickStartJobItem\(item\)\) \{ setActiveID\(item\.id\); setMenuID\(""\); setPreviewID\(""\); \} else \{ setMenuID\(item\.id\); setActiveID\(""\); \} \}\}/, "opening an icon context menu cancels every delayed timer and closes the anchor menu and quick toolbar before showing right-click actions; an optimistic job opens its popup instead of the backend menu");
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

// --- widget-specific cluster zoom: instant, persisted, clamped to [0.75, 1.5] ---
assert.equal(normalizeIconZoom(1), 1, "default zoom is 1");
assert.equal(normalizeIconZoom(1.2), 1.2, "in-range zoom round-trips");
assert.equal(normalizeIconZoom(0.74), 1, "below-min zoom falls back to default");
assert.equal(normalizeIconZoom(1.51), 1, "above-max zoom falls back to default");
assert.equal(normalizeIconZoom(1.05), 1.1, "in-range zoom snaps to the 0.1 step");
assert.equal(ICON_ZOOM_MIN, 0.75, "zoom minimum is 0.75");
assert.equal(ICON_ZOOM_MAX, 1.5, "zoom maximum is 1.5");
assert.equal(ICON_ZOOM_STEP, 0.1, "zoom steps by 0.1");
assert.equal(stepIconZoom(1, ICON_ZOOM_STEP), 1.1, "zoom in steps by 0.1");
assert.equal(stepIconZoom(1, -ICON_ZOOM_STEP), 0.9, "zoom out steps by 0.1");
assert.equal(stepIconZoom(ICON_ZOOM_MIN, -ICON_ZOOM_STEP), ICON_ZOOM_MIN, "zoom out clamps at the minimum");
assert.equal(stepIconZoom(ICON_ZOOM_MAX, ICON_ZOOM_STEP), ICON_ZOOM_MAX, "zoom in clamps at the maximum");
assert.equal(stepIconZoom(1.04, ICON_ZOOM_STEP), 1.1, "step snaps from an off-grid value");
assert.equal(parseIconZoom(null), 1, "missing stored zoom falls back to 1");
assert.equal(parseIconZoom("not json"), 1, "corrupt stored zoom falls back to 1");
assert.equal(parseIconZoom('"1.3"'), 1, "non-number stored zoom falls back to 1");
assert.equal(parseIconZoom("0.9"), 0.9, "a stored number round-trips");
assert.equal(parseIconZoom(serializeIconZoom(1.3)), 1.3, "serialize/parse round-trips");
assert.equal(parseIconZoom(serializeIconZoom(1.6)), 1, "an out-of-range stored zoom falls back");
// Endpoints are reachable step targets and must survive every
// normalize/step/apply round-trip, or the stored minimum would snap to 0.8 on
// the next read and the zoom-out button would never stay disabled at 0.75.
assert.equal(normalizeIconZoom(ICON_ZOOM_MIN), ICON_ZOOM_MIN, "the minimum endpoint survives normalization");
assert.equal(normalizeIconZoom(ICON_ZOOM_MAX), ICON_ZOOM_MAX, "the maximum endpoint survives normalization");
assert.equal(parseIconZoom(serializeIconZoom(ICON_ZOOM_MIN)), ICON_ZOOM_MIN, "a stored minimum round-trips through serialize/parse");
assert.equal(parseIconZoom(serializeIconZoom(ICON_ZOOM_MAX)), ICON_ZOOM_MAX, "a stored maximum round-trips through serialize/parse");
assert.equal(normalizeIconZoom(stepIconZoom(ICON_ZOOM_MIN, -ICON_ZOOM_STEP)), ICON_ZOOM_MIN, "stepping down from the minimum stays at the minimum after normalization");
assert.equal(normalizeIconZoom(stepIconZoom(ICON_ZOOM_MAX, ICON_ZOOM_STEP)), ICON_ZOOM_MAX, "stepping up from the maximum stays at the maximum after normalization");
assert.equal(normalizeIconZoom(stepIconZoom(0.8, -ICON_ZOOM_STEP)), ICON_ZOOM_MIN, "stepping below 0.8 clamps to the reachable minimum, which normalize then preserves");
assert.equal(stepIconZoom(ICON_ZOOM_MIN, ICON_ZOOM_STEP), 0.9, "stepping up from the minimum lands on the next reachable grid value");
assert.equal(stepIconZoom(1.4, ICON_ZOOM_STEP), ICON_ZOOM_MAX, "stepping up from 1.4 reaches the maximum");

// --- cluster max-width: the transformed cluster bound must stay inside the
// visible root for every desktopZoom (0.5..2) × clusterZoom (0.75..1.5)
// combination at any viewport >= 640, so overflow:hidden never clips it.
// The reverse-zoomed frame makes 1 CSS px inside equal 1 physical px, so the
// visible width is innerWidth × desktopZoom and the 18px edge margins are
// physical px that must not be divided by the zoom.
assert.equal(clusterGridMaxWidth(640, 1, 1), 604, "at zoom 1 the grid max-width leaves both 18px edge margins");
assert.equal(clusterGridMaxWidth(640, 1, 1.5), 604 / 1.5, "cluster zoom divides the available physical width");
assert.equal(clusterGridMaxWidth(900, 1, 0.75), (900 - 36) / 0.75, "zooming out widens the grid proportionally");
for (const viewport of [640, 1280, 2560]) {
  for (const desktopZoom of [0.5, 1, 2]) {
    for (const clusterZoom of [0.75, 1, 1.25, 1.5]) {
      const physical = viewport * desktopZoom;
      const maxWidth = clusterGridMaxWidth(viewport, desktopZoom, clusterZoom);
      const transformed = maxWidth * clusterZoom;
      assert.ok(
        transformed <= physical - 36 + 1e-9,
        `desktopZoom ${desktopZoom} × clusterZoom ${clusterZoom} at viewport ${viewport}: transformed ${transformed} > visible ${physical - 36}`,
      );
      assert.ok(transformed >= 0, "max-width never goes negative");
    }
  }
}
assert.equal(clusterGridMaxWidth(640, 0.5, 1.5), (320 - 36) / 1.5, "the desktop zoom widens the visible frame (innerWidth × zoom) before the margins");
assert.equal(clusterGridMaxWidth(640, 2, 1), 1280 - 36, "a 2x desktop zoom doubles the physical width available inside the reverse-zoomed frame");
assert.equal(clusterGridMaxWidth(1, 0.5, 1.5), 0, "a viewport narrower than the margins clamps to zero instead of overflowing");

// --- extracted transient-UI timer scheduling: one cancel path clears every
// delayed click/hover/preview so a close or drag can never be resurrected ---
class FakeTimerHost implements TimerHost {
  private next = 1;
  private scheduled = new Map<number, () => void>();
  setTimeout(fn: () => void, _delay: number): number {
    const id = this.next++;
    this.scheduled.set(id, fn);
    return id;
  }
  clearTimeout(id: number): void { this.scheduled.delete(id); }
  fire(id: number): void {
    const fn = this.scheduled.get(id);
    if (fn) { this.scheduled.delete(id); fn(); }
  }
  pending(): number[] { return [...this.scheduled.keys()].sort((a, b) => a - b); }
}

{
  const host = new FakeTimerHost();
  const timers = new IconTimers(host);
  assert.deepEqual(timers.pending(), [], "a fresh timer set schedules nothing");
  timers.scheduleClick(() => {});
  timers.scheduleHover(() => {}, 1200);
  timers.schedulePreviewClose(() => {});
  assert.deepEqual(timers.pending(), ["click", "hover", "previewClose"], "all three transient timers are scheduled independently");
  timers.cancel();
  assert.deepEqual(timers.pending(), [], "cancel clears every scheduled transient timer");
}

{
  // A close or drag must never leave a delayed open/preview behind.
  const host = new FakeTimerHost();
  const timers = new IconTimers(host);
  let opened = 0;
  timers.scheduleClick(() => { opened++; });
  timers.scheduleHover(() => { opened++; }, 1200);
  timers.schedulePreviewClose(() => { opened++; });
  timers.cancel();
  assert.equal(host.pending().length, 0, "cancel leaves no deadline in the host");
  assert.equal(opened, 0, "no delayed open/preview fires after cancel");
  // Pointer-down (drag start) uses the same cancel, so a pending click from a
  // previous press cannot open an icon while the pointer is down.
  timers.scheduleClick(() => { opened++; });
  timers.cancel();
  assert.equal(opened, 0, "drag start cancels a pending delayed click");
}

{
  // Re-scheduling one kind replaces the previous deadline: two quick presses
  // never double-open, and a fresh hover replaces a stale preview close.
  const host = new FakeTimerHost();
  const timers = new IconTimers(host);
  let opened = 0;
  timers.scheduleClick(() => { opened++; });
  const first = host.pending()[0];
  timers.scheduleClick(() => { opened++; });
  const second = host.pending()[0];
  assert.notEqual(first, second, "a new click schedule replaces the pending one");
  assert.equal(host.pending().length, 1, "only the latest click stays scheduled");
  host.fire(second);
  assert.equal(host.pending().length, 0, "the fired click consumed its deadline");
  assert.equal(opened, 1, "only the replacement click fired");
  // The replaced deadline was cancelled inside the host, so re-firing its
  // stale id is a no-op even if some other code still holds it.
  host.fire(first);
  assert.equal(opened, 1, "a cancelled deadline never fires even when its stale id is re-fired");
}

{
  // Individual clear helpers keep enter() (hover restart) and popup hover-out
  // from killing unrelated timers: clearing hover + previewClose must not
  // cancel a click that is already in flight.
  const host = new FakeTimerHost();
  const timers = new IconTimers(host);
  let opened = 0;
  timers.scheduleClick(() => { opened++; });
  timers.scheduleHover(() => { opened++; }, 1200);
  timers.schedulePreviewClose(() => { opened++; });
  timers.clearHover();
  timers.clearPreviewClose();
  assert.deepEqual(timers.pending(), ["click"], "clearHover/clearPreviewClose leave the delayed click intact");
  host.fire(host.pending()[0]);
  assert.equal(opened, 1, "the delayed click still fires after hover/previewClose were cleared");
}
assert.equal(CLICK_DELAY, 240, "the delayed-click window stays 240ms");
assert.equal(PREVIEW_CLOSE_DELAY, 180, "the preview-close window stays 180ms");
assert.equal(DRAG_THRESHOLD, 7, "the drag threshold stays 7px");

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

// --- anchor left-click quick controls: native drag preserved, mutual
// exclusion with the right-click menu and icon menus, delayed-click safety ---
assert.match(component, /desktop-icon-anchor[^>]*onClick=\{toggleQuick\}/, "left-clicking the WG2 anchor toggles the quick toolbar without pointer handlers, so native window dragging is preserved");
assert.doesNotMatch(component, /desktop-icon-anchor[^>]*onPointerDown/, "the anchor keeps no pointer handlers, so left-button window dragging stays native Wails drag");
assert.match(component, /const cancelTransientTimers = useCallback\(\(\) => \{[\s\S]*timers\.current\?\.cancel\(\);[\s\S]*drag\.current = null;[\s\S]*\}, \[\]\)/, "the central timer cancel clears every scheduled click/hover/preview and the in-flight drag");
assert.match(component, /const closeTransient = useCallback\(\(\) => \{[\s\S]*cancelTransientTimers\(\);[\s\S]*setActiveID\(""\); setPreviewID\(""\); setMenuID\(""\); setAnchorMenuOpen\(false\); setQuickOpen\(false\);/, "the central close path cancels the timers before clearing every transient surface");
assert.match(component, /const toggleQuick = \(\) => \{[\s\S]*closeTransient\(\);[\s\S]*const next = !quickOpen;[\s\S]*setQuickOpen\(next\);[\s\S]*if \(next && topmostReadFailed\) setTopmostAttempt\(\(attempt\) => attempt \+ 1\);/, "anchor left-click closes every transient surface through the central path before toggling the toolbar, and reopening retries a failed always-on-top read");
assert.match(component, /closeTransient\(\);[\s\S]*setAnchorMenuOpen\(true\)/, "anchor right-click closes every transient surface before opening its settings menu");
assert.match(component, /aria-expanded=\{quickOpen\} aria-controls="desktop-icon-quick"/, "the anchor's aria-expanded/controls describe only its own left-click surface (the quick toolbar)");
assert.match(component, /aria-haspopup="menu"/, "the right-click context menu is announced as a menu popup");
assert.match(component, /id="desktop-icon-anchor-menu" className="desktop-icon-anchor-menu" role="menu"/, "the anchor menu carries its own id for the aria-controls/menu contract");
assert.match(component, /setAnchorMenuOpen\(false\);[\s\S]*?setQuickOpen\(false\);[\s\S]*?writeCollapsedState/, "the collapse toggle closes the quick toolbar");
assert.match(component, /desktop-icon-cluster" style=\{\{ transform: `scale\(\$\{clusterZoom\}\)`, transformOrigin: "bottom right", "--cluster-zoom": String\(clusterZoom\), "--cluster-max-width": `\$\{clusterMaxWidth\}px` \} as CSSProperties\}/, "the cluster zoom scales around the bottom-right anchor and binds the computed desktopZoom-aware max-width to a CSS variable for the grid");
assert.match(component, /stepIconZoom\(clusterZoom, -ICON_ZOOM_STEP\)[\s\S]*stepIconZoom\(clusterZoom, ICON_ZOOM_STEP\)/, "zoom buttons step through the shared layout contract");
assert.match(component, /disabled=\{exiting \|\| clusterZoom <= ICON_ZOOM_MIN\}[\s\S]*disabled=\{exiting \|\| clusterZoom >= ICON_ZOOM_MAX\}/, "zoom buttons disable while exiting and exactly at the layout endpoints");
assert.match(component, /const applyClusterZoom = \(next: number\) => \{[\s\S]*const zoom = normalizeIconZoom\(next\);[\s\S]*setClusterZoom\(zoom\);[\s\S]*writeClusterZoom\(zoom\);/, "the zoom apply path normalizes exactly once before persisting");
assert.match(component, /wg2\.icon-widget-zoom/, "widget zoom persists under a stable widget-local key");
assert.match(css, /\.desktop-icon-quick\s*\{[^}]*--wails-draggable:\s*no-drag;/, "the quick toolbar never inherits the window drag region");
assert.match(css, /\.desktop-icon-quick__btn\[aria-checked|\.desktop-icon-quick__btn--on/, "the always-on-top switch has a distinct checked style");
const quickRule = css.match(/\.desktop-icon-quick\s*\{[^}]*\}/)?.[0] ?? "";
assert.doesNotMatch(quickRule, /position:\s*absolute/, "the quick toolbar renders inline in the control row, never absolutely above the cluster");
assert.match(css, /\.desktop-icon-grid\s*\{[^}]*max-width:\s*var\(--cluster-max-width, calc\(\(100vw - 36px\) \/ var\(--cluster-zoom, 1\)\)\);/, "the unscaled grid width is the computed desktopZoom-aware cluster max-width with a static calc fallback");
assert.match(css, /\.desktop-icon-row\s*\{[^}]*flex-wrap:\s*wrap/, "icon rows wrap instead of clipping when the scaled cluster is narrow");

// --- quick-control failures use their own error channel: the 1s snapshot
// poll must never erase an action failure (toggle, open main/settings, or the
// initial always-on-top read) ---
assert.match(component, /const \[quickError, setQuickError\] = useState\(""\);/, "quick-control failures use their own error channel");
assert.match(component, /const refresh = useCallback\(async \(\) => \{[\s\S]{0,340}setError\(next\.error \|\| ""\);/, "the 1s snapshot poll writes only the snapshot error channel");
assert.doesNotMatch(component, /const refresh = useCallback\(async \(\) => \{[\s\S]{0,340}setQuickError/, "the snapshot poll never touches the quick-error channel");
assert.match(component, /\(error \|\| quickError \|\| quickJobs\.storageError\) && <div className="desktop-icon-toast" role="alert">/, "the toast surfaces snapshot, quick-control, and durable-storage errors together");
assert.match(component, /\.catch\(\(\) => \{ if \(alive\) \{ setTopmostReadFailed\(true\); setQuickError\(TOPMOST_READ_ERROR\); \} \}\)/, "an initial always-on-top read failure stays visible and never assumes false");
assert.match(component, /disabled=\{exiting \|\| topmostBusy \|\| !topmostLoaded \|\| topmostReadFailed\}/, "the always-on-top switch stays disabled after a failed initial read and while exiting");
assert.match(component, /const \[topmostAttempt, setTopmostAttempt\] = useState\(0\);/, "the always-on-top read retry is driven by an explicit attempt counter");

// --- transient anchor UI closes on any real interaction, and hover preview
// is suppressed while the quick toolbar or anchor menu is open ---
assert.match(component, /if \(!snapshot\.hoverStatusDelayMs \|\| activeID \|\| menuID \|\| drag\.current \|\| anchorMenuOpen \|\| quickOpen\) return;/, "hover preview is suppressed while the quick toolbar or anchor menu is open");
assert.match(component, /drag\.current = \{ item, x: event\.clientX, y: event\.clientY, moved: false \};[\s\S]*setAnchorMenuOpen\(false\);[\s\S]*setQuickOpen\(false\);/, "pointer-down on an icon closes the transient anchor UI immediately");
assert.match(component, /const doubleClick = \(item: DesktopIconItem\) => \{ timers\.current\?\.cancel\(\); setAnchorMenuOpen\(false\); setQuickOpen\(false\);/, "double-click cancels every delayed timer before running the icon action");
assert.match(component, /setBusy\(true\); setError\(""\); cancelTransientTimers\(\); setAnchorMenuOpen\(false\); setQuickOpen\(false\);/, "a normal icon action cancels the delayed timers and closes the transient anchor UI");
assert.match(component, /const onPointerDown = \(event: PointerEvent\) => \{[\s\S]*if \(target\.closest\(TRANSIENT_PROTECTED_SELECTOR\)\) return;[\s\S]*closeTransient\(\);[\s\S]*document\.addEventListener\("pointerdown", onPointerDown\)/, "a document-level outside-click handler closes every transient surface (timers included) on any container/grid/control gap");
assert.match(component, /TRANSIENT_PROTECTED_SELECTOR = "\.desktop-icon-quick, \.desktop-icon-anchor, \.desktop-icon-anchor-menu, \.desktop-icon-menu, \.desktop-icon, \.desktop-icon-collapse, \.desktop-icon-popup, \.desktop-icon-toast"/, "the outside-click exclusion list covers the toolbar, anchor, menus, valid icons, popups and the toast");
assert.doesNotMatch(component, /event\.target === event\.currentTarget/, "outside-click detection no longer relies on the main element alone");

// --- exit round-trip: main/settings disable and the toolbar announces busy,
// and the right-click settings entry stays open on failure (visible retry) ---
assert.match(component, /aria-busy=\{exiting\}/, "the quick toolbar announces the exit round-trip through aria-busy");
assert.match(component, /disabled=\{exiting\} onClick=\{\(\) => void openMainWindow\(\)\}[\s\S]*disabled=\{exiting\} onClick=\{\(\) => void openSettingsWindow\(\)\}/, "open main and settings are disabled while the exit round-trip is in flight");
assert.match(component, /onKeyDown=\{quickRove\}/, "the quick toolbar implements arrow-key roving");
assert.match(component, /const quickRove =[\s\S]*ArrowRight[\s\S]*ArrowLeft[\s\S]*Home[\s\S]*End[\s\S]*buttons\[next\]\.focus\(\)/, "roving moves focus with Left/Right/Home/End and lands on an enabled button");

// --- completion notice: fixed OK / Detail / Dismiss with distinct colors ---
assert.match(component, /desktop-icon-popup__ok"[\s\S]{0,160}onClick=\{onClose\}>OK<\/button>[\s\S]{0,200}desktop-icon-popup__detail"[\s\S]{0,200}disabled=\{busy\} onClick=\{\(\) => run\("open"\)\}>Detail<\/button>[\s\S]{0,200}desktop-icon-popup__dismiss"[\s\S]{0,200}disabled=\{busy\} onClick=\{\(\) => run\("dismiss"\)\}>Dismiss<\/button>/, "completion notices always render OK / Detail / Dismiss in fixed order with their class contracts");
assert.match(component, /desktop-icon-popup__ok"[\s\S]{0,160}onClick=\{onClose\}>OK/, "OK only closes the popup locally");
assert.match(component, /desktop-icon-popup__ok"\s*onClick=\{onClose\}/, "OK is not gated by busy: the class is directly followed by the local-close handler");
assert.doesNotMatch(component, /desktop-icon-popup__ok[\s\S]{0,200}run\("ok"\)/, "OK never dispatches a backend ok action");
assert.doesNotMatch(component, /\[\"ok\", \"dismiss\", \"later\", \"open\", \"reply\"\]/, "ok is no longer a backend-acknowledged action that closes the popup");
assert.match(component, /\[\"dismiss\", \"later\", \"open\", \"reply\", \"continue\"\]/, "dismiss/open/continue close the popup after a successful backend roundtrip");
assert.match(component, /onClose=\{\(\) => \{ setActiveID\(""\);\s*setPreviewID\(""\);\s*\}\}/, "OK closes the popup and its hover preview together");
// The continuation is a resident editable textarea on every completion
// notice: no collapsed 对话框 trigger, no expanded/collapsed dialog state.
assert.doesNotMatch(component, /desktop-icon-popup__dialog-trigger|>对话框<\/button>/, "completion notices no longer hide the continuation behind a 对话框 trigger button");
assert.doesNotMatch(component, /dialogOpen|closeDialog|desktop-icon-popup__dialog/, "the continuation composer has no dialog state or 对话框 surface at all");
assert.match(component, /desktop-icon-popup__continue[\s\S]+<textarea[\s\S]+placeholder="告诉 WorkGround2 接下来要完成什么…"/, "completion notices always render the resident continuation textarea");
assert.match(component, /run\("continue", \[text\]\)/, "the completion composer continues the current task instead of starting a separate conversation");
// Ctrl/Cmd+Enter submits, plain Enter stays a newline, and IME composition
// (isComposing, keyCode 229, compositionend grace) never leaks into a send.
assert.match(component, /event\.key === "Enter" && \(event\.ctrlKey \|\| event\.metaKey\)[\s\S]+isWidgetImeKeyEvent\(event, composingRef\.current, lastCompositionEndAt\.current\)[\s\S]+sendFollowup\(\)/, "Ctrl/Cmd+Enter submits the continuation only outside IME composition; plain Enter stays a newline");
assert.match(component, /onCompositionStart=\{\(\) => \{ composingRef\.current = true; \}\}[\s\S]+onCompositionEnd=\{\(\) => \{ composingRef\.current = false; lastCompositionEndAt\.current = Date\.now\(\); \}\}/, "the continuation textarea tracks IME composition and the post-confirm grace");
// Escape inside the focused input must never bubble to the global close.
assert.match(component, /event\.key === "Escape"\) \{ event\.preventDefault\(\); event\.stopPropagation\(\); return; \}/, "Escape inside the continuation input is consumed locally");
assert.match(component, /event\.key === "Escape" && !\(event\.target instanceof HTMLElement && event\.target\.closest\("\.desktop-icon-popup__continue"\)\)\) \{ closeTransient\(\); \}/, "the global Escape handler skips the continuation input before closing transient surfaces");
// Busy / accessibility / double-submit / retryable-failure retention contract.
assert.match(component, /const sendFollowup = async \(\) => \{[\s\S]{0,120}const text = failedFollowup \|\| followup\.trim\(\);[\s\S]{0,100}if \(busy \|\| !text \|\| followupSent\.current\) return/, "the async continuation guards busy/empty before submitting the frozen intent");
assert.match(component, /const freezeRetry = \(\) => \{[\s\S]{0,120}setFollowup\(text\);[\s\S]{0,100}setFailedFollowup\(text\);[\s\S]{0,100}followupSent\.current = false/, "retry freezing preserves the exact submitted text and releases the send guard");
assert.match(component, /try \{[\s\S]{0,80}const status = await run\("continue", \[text\]\);[\s\S]{0,100}status === "retryable_error"[\s\S]{0,80}freezeRetry\(\)[\s\S]{0,260}catch \{[\s\S]{0,240}freezeRetry\(\)/, "both retryable results and rejected run promises freeze the original text without an unhandled rejection");
assert.match(component, /desktop-icon-popup__continue" aria-busy=\{busy\}/, "the continuation area announces the busy state");
assert.match(component, /disabled=\{busy\} readOnly=\{Boolean\(failedFollowup\)\}[\s\S]{0,180}aria-label="继续当前任务"/, "the continuation textarea is labelled, busy-disabled, and read-only only after retryable failure");
assert.match(component, /aria-describedby=\{failedFollowup \? "desktop-icon-followup-error" : "desktop-icon-followup-hint"\}/, "the textarea describes both its normal keyboard help and retryable failure state");
assert.match(component, /发送失败，可重试原内容[\s\S]{0,220}failedFollowup \? "重试发送" : "发送"[\s\S]{0,160}failedFollowup \? "原内容已锁定"/, "retryable failure explains that only the original content can be resent");
assert.match(component, /desktop-icon-popup__scroll" tabIndex=\{0\} role="region" aria-label="任务通知详情"/, "the overflowing notice body is keyboard-focusable and named");
assert.match(component, /role=\{active \? "dialog" : "status"\} aria-label=\{active \? `\$\{popupItem\.title\} 操作` : undefined\}/, "interactive popups expose a named dialog role");
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
// The notice body scrolls inside the anchor-space max-height; the outer box
// keeps its shadow and bottom arrow (no overflow clip), long tokens wrap, and
// the resident continuation textarea stays compact.
assert.match(css, /\.desktop-icon-popup__scroll[\s\S]*max-height:\s*calc\(var\(--popup-max-height[^}]*overflow-y:\s*auto/, "the notice body scrolls inside the computed anchor-space max-height");
assert.match(css, /\.desktop-icon-popup__scroll[^}]*overflow-wrap:\s*anywhere/, "long tokens wrap inside the scroll container instead of forcing the popup wider");
assert.match(css, /\.desktop-icon-popup\s*\{[^}]*box-sizing:\s*border-box;/, "the popup box is border-box");
assert.match(css, /\.desktop-icon-popup__continue textarea\s*\{[^}]*min-height:\s*64px;/, "the resident continuation textarea stays compact");
assert.match(component, /"--popup-max-height": `\$\{placed\.maxHeight\}px`/, "popup placement binds the computed anchor-space max-height to the popup box");
assert.match(component, /const \[viewport, setViewport\] = useState\(\(\) => widgetViewportSize\(window\.innerWidth, window\.innerHeight, 1\)\)/, "viewport state owns both logical width and height");
assert.match(component, /const onResize = \(\) => \{[\s\S]{0,180}widgetViewportSize\(window\.innerWidth, window\.innerHeight, desktopZoom\)[\s\S]{0,180}current\.width === next\.width && current\.height === next\.height/, "one resize path updates both logical axes and skips unchanged rerenders");
assert.match(component, /placeIconPopup\(rect, viewport\.width, viewport\.height, width\)[\s\S]{0,220}\[active, desktopZoom, popupItem, popupWidth, snapshot\.revision, viewport\.height, viewport\.width\]/, "popup placement reacts to height-only viewport resizes");
assert.match(backend, /Status:\s*\"pending\", Action:\s*\"continue\"/, "task continuation persists a pending receipt before delivery");
assert.match(backend, /advanceDesktopIconTaskContinue[\s\S]+tryDesktopIconReply/, "task continuation uses the acknowledged and recoverable user-turn pipeline");
assert.match(css, /\.desktop-icon-popup__actions button:not\(:disabled\):hover/, "action buttons keep an accessible hover state");
assert.match(css, /button:focus-visible[\s\S]*outline: 2px solid #70dfe8/, "action buttons keep a visible focus ring");
assert.match(css, /button:disabled\s*\{\s*opacity: \.55/, "disabled buttons stay visibly disabled");

// --- workspace management: the fixed workspace icon between 新建 and Rooms ---
// The backend fixed bar is the declared Go contract: 新建 → 工作区 → Rooms → 委托 → 搜索.
assert.match(backend, /\{"new", "新建", "plus"\},\s*\{"workspace", "工作区", "workspace"\},\s*\{"rooms", "Rooms", "rooms"\},\s*\{"delegate", "委托", "users"\},\s*\{"search", "搜索", "search"\}/, "backend fixed bar order is 新建 → 工作区 → Rooms → 委托 → 搜索 by declaration");
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

// --- rooms manager pure projection: authoritative collaboration topics only ---
const roomsTree: ProjectNode[] = [
  {
    key: "global_folder", kind: "global_folder", label: "Global",
    children: [
      { key: "g_room", kind: "global_topic", label: "联调 Room", topicId: "topic_g", sessionKind: "collaboration", sessionPath: "/tmp/room-g.jsonl", pinned: true, lastActivityAt: 300 },
      { key: "g_plain", kind: "global_topic", label: "普通会话", topicId: "topic_g_plain", sessionKind: "normal", sessionPath: "/tmp/plain.jsonl" },
      { key: "g_no_path", kind: "global_topic", label: "无会话", topicId: "topic_g_nopath", sessionKind: "collaboration" },
      { key: "g_work", kind: "global_topic", label: "Work", topicId: "topic_g_work", sessionKind: "work", sessionPath: "/tmp/work.jsonl" },
    ],
  },
  {
    key: "project_a", kind: "project", label: "Alpha", root: "~/alpha",
    children: [
      { key: "a_room", kind: "topic", label: "设计 Room", root: "~/alpha", topicId: "topic_a", sessionKind: "collaboration", sessionPath: "/tmp/room-a.jsonl", lastActivityAt: 200 },
      { key: "a_dupe", kind: "topic", label: "重复", root: "~/alpha", topicId: "topic_a", sessionKind: "collaboration", sessionPath: "/tmp/room-a-dup.jsonl" },
      { key: "a_crew", kind: "crew_session", label: "Crew", root: "~/alpha", topicId: "topic_a_crew", sessionKind: "collaboration", sessionPath: "/tmp/crew.jsonl" },
      { key: "a_im", kind: "topic", label: "IM", root: "~/alpha", topicId: "topic_a_im", sessionKind: "normal", sessionPath: "/tmp/im.jsonl", sessionSource: "im" },
    ],
  },
  { key: "project_b", kind: "project", label: "Beta", root: "~/beta" },
];
assert.deepEqual(roomRows(roomsTree), [
  { topicId: "topic_g", label: "联调 Room", pinned: true, scope: "global", workspaceRoot: "", sessionPath: "/tmp/room-g.jsonl" },
  { topicId: "topic_a", label: "设计 Room", pinned: false, scope: "project", workspaceRoot: "~/alpha", sessionPath: "/tmp/room-a.jsonl" },
], "rooms project only collaboration topic/global_topic nodes with a real topicId+sessionPath, dedupe by topicId, and keep the backend tree order");
assert.deepEqual(roomRows([
  { key: "g", kind: "global_topic", label: "no session", topicId: "t1", sessionKind: "collaboration" },
  { key: "p", kind: "topic", label: "no topicId", sessionKind: "collaboration", sessionPath: "/tmp/x.jsonl" },
  { key: "s", kind: "session", label: "child", sessionKind: "collaboration", topicId: "t2", sessionPath: "/tmp/s.jsonl" },
]), [], "collaboration topics without a topicId or sessionPath, and bare session nodes, never become Rooms");
assert.deepEqual(roomRows([]), [], "an empty tree yields an empty Rooms list");

// --- rooms fixed icon: glyph, dialog open, and the generic fallback exclusion ---
assert.match(component, /item\.sourceId === "rooms"\) return <MessagesSquare \/>/, "the rooms fixed icon renders its own distinct glyph");
assert.match(component, /item\.kind === "fixed" && item\.sourceId === "rooms"[\s\S]{0,220}setActiveID\(item\.id\)/, "single click on the rooms icon opens the management dialog");
assert.doesNotMatch(component, /item\.sourceId === "rooms"[\s\S]{0,80}run\(item, "open"\)/, "the rooms icon never runs the generic fixed action");
assert.match(component, /active\.sourceId === "rooms" && <RoomsManager/, "the rooms popup renders the management dialog");
assert.match(component, /active\.sourceId !== "rooms"/, "the generic fixed popup fallback explicitly excludes the rooms icon");
assert.match(component, /<RoomsManager onClose=\{\(\) => setActiveID\(""\)\}\s*onNewRoom=\{onNewRoom\}\s*onOpenRoom=\{onOpenRoom\}/, "RoomsManager receives the exit-and-open coordination callbacks");

// --- rooms manager dialog contract: authoritative load, safe mutations, no
// optimistic writes, explicit placeholder ---
assert.match(component, /const reload = useCallback\(async \(\) => \{[\s\S]+app\.ListProjectTree\(\)[\s\S]+roomRows\(tree\)/, "RoomsManager loads its authoritative list from ListProjectTree through the roomRows projection");
assert.match(component, /app\.OpenTopicSession\(row\.scope, row\.workspaceRoot, row\.topicId, row\.sessionPath\)[\s\S]+onOpenRoom\(meta\.id\)/, "opening a Room activates the backend tab and exits the widget focused on it");
assert.match(component, /app\.SetTopicPinned\(row\.topicId, !row\.pinned\)[\s\S]+await reload\(\)/, "Pin toggles through SetTopicPinned then reloads the authoritative list");
assert.match(component, /app\.RenameTopic\(row\.topicId, renameTitle\(renameDraft\)\)/, "rename commits the raw input through the shared empty-title contract");
assert.match(component, /留空恢复自动标题/, "the room rename editor explains the empty-title restore semantics");
assert.match(component, /deleteConfirmNext\(armed, row\.topicId\)[\s\S]+next\.confirmed\) void confirmTrash\(row\)/, "trash uses the two-step confirm state machine before calling the backend");
assert.match(component, /app\.TrashTopic\(row\.topicId\)[\s\S]+setArmed\(null\)[\s\S]+await reload\(\)/, "trash calls the backend only on the confirmed step, then clears the arm and reloads");
assert.doesNotMatch(component, /TrashTopic[\s\S]{0,60}setRows/, "trash never optimistically removes the row before the backend confirms");
assert.match(component, />修改图标<small className="desktop-icon-popup__workspace-soon">即将支持<\/small>/, "the room icon button is an explicit 即将支持 placeholder");
assert.doesNotMatch(component, /app\.SetTopicIcon|SetRoomIcon/, "the room placeholder must never write icon data");
assert.match(component, /desktop-icon-popup__workspaces desktop-icon-popup__rooms/, "RoomsManager reuses the WorkspaceManager layout and marks its own popup root");
assert.match(component, /onClick=\{onNewRoom\}>新增<\/button>/, "the Rooms header 新增 delegates to the root App coordination callback");
assert.doesNotMatch(component, /openCollaborationDialog|CreateCollaboration|HostRoom|JoinRoom/, "RoomsManager never re-implements the Host/Join Room form; the root App owns openCollaborationDialog");
assert.match(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__rooms\)/, "the rooms popup keeps the same bounded popup layout as the workspace manager");

// --- App coordination: monotonic signal, exit-before-open, tab-focus exit ---
const appSource = readFileSync(resolve(import.meta.dirname, "../App.tsx"), "utf8");
assert.match(appSource, /const \[collabDialogSignal, setCollabDialogSignal\] = useState\(0\)/, "the root App owns a monotonic collaboration-dialog signal that starts at 0");
assert.match(appSource, /widgetCoordinator\.exit\(\)\.then\(\(\) => setCollabDialogSignal\(\(count\) => count \+ 1\)\)/, "the Rooms 新增 signal is bumped only after the widget exit succeeds");
assert.match(appSource, /widgetCoordinator\.exit\(tabID\)/, "opening an existing Room exits the widget and focuses the returned tab");
assert.match(appSource, /const widgetRoomRequest = useRef\(false\)[\s\S]+if \(widgetRoomRequest\.current\) return;[\s\S]+\.finally\(\(\) => \{ widgetRoomRequest\.current = false; \}\)/, "the Rooms 新增 request is guarded against double-invocation while the widget exit is in flight");
assert.match(appSource, /appliedCollabDialogSignal = useRef\(0\)[\s\S]+collabDialogSignal > 0 && collabDialogSignal !== appliedCollabDialogSignal\.current/, "MainApp opens the collaboration dialog exactly once per distinct signal, never on initial mount");
assert.match(appSource, /void openCollaborationDialog\(\)/, "MainApp reuses its existing openCollaborationDialog to show the Host/Join Room form");
assert.match(appSource, /collabDialogSignal=\{collabDialogSignal\}/, "the root App forwards the signal to MainApp");
assert.match(appSource, /const openWidgetMain = useCallback\(\(\) => \{[\s\S]*return widgetCoordinator\.exit\(\);[\s\S]*\}, \[widgetCoordinator\]\)/, "open main exits widget mode through the shared coordinator");
assert.match(appSource, /<DesktopIconMode onNewRoom=\{requestWidgetRoomDialog\} onOpenRoom=\{openWidgetRoom\} onOpenSettings=\{openWidgetSettings\} onOpenMain=\{openWidgetMain\} \/>/, "DesktopIconMode receives the room open/new, settings-open, and open-main coordination callbacks");

// --- anchor context menu: right-click opens a settings entry without
// triggering anchor drag, icon actions, or a delayed primary click ---
assert.match(component, /const \[anchorMenuOpen, setAnchorMenuOpen\] = useState\(false\)/, "the anchor menu owns its own open state");
assert.match(component, /onContextMenu=\{\(event\) => \{[\s\S]*?event\.preventDefault\(\);[\s\S]*?closeTransient\(\);[\s\S]*?setAnchorMenuOpen\(true\)[\s\S]*?\}\}/, "right-clicking the anchor only opens its menu: the central close cancels every click/hover/preview timer, clears the drag state, and closes icon popups and the plain icon menu");
assert.doesNotMatch(component, /desktop-icon-anchor[^>]*onPointerDown/, "the anchor keeps no pointer handlers, so left-button window dragging stays native Wails drag");
assert.match(component, /id="desktop-icon-anchor-menu" className="desktop-icon-anchor-menu" role="menu"[\s\S]*?<button type="button" role="menuitem" disabled=\{exiting\} onClick=\{\(\) => void openSettingsWindow\(\)\}/, "the anchor menu 设置 button routes through the guarded exit-before-open flow and stays open, so a failed exit keeps the same click retryable");
assert.doesNotMatch(component, /setAnchorMenuOpen\(false\);[\s\S]{0,60}void openSettingsWindow/, "a failed settings exit must not close the anchor menu before the exit resolves");
assert.match(component, /event\.key === "Escape" && !\(event\.target instanceof HTMLElement && event\.target\.closest\("\.desktop-icon-popup__continue"\)\)\) \{ closeTransient\(\); \}/, "Escape closes every transient surface through the central close path, except while the continuation input is focused");
assert.match(component, /const close = \(\) => closeTransient\(\);[\s\S]*?addEventListener\("blur", close\)/, "losing desktop-window focus closes every transient surface through the central close path");
assert.match(component, /const onPointerDown = \(event: PointerEvent\) => \{[\s\S]*if \(target\.closest\(TRANSIENT_PROTECTED_SELECTOR\)\) return;[\s\S]*closeTransient\(\);[\s\S]*document\.addEventListener\("pointerdown", onPointerDown\)/, "clicking the empty desktop or any container/grid/control gap closes every transient surface through the central close path");
assert.match(component, /HIT_REGION_SELECTOR[^;]*desktop-icon-anchor-menu/, "the anchor menu joins the native hit-region reporting so the transparent window keeps it clickable");
assert.match(component, /HIT_REGION_SELECTOR[^;]*desktop-icon-quick/, "the quick toolbar joins the native hit-region reporting so the transparent window keeps it clickable");
assert.match(component, /\}, \[activeID, menuID, previewID, snapshot\.revision, collapsed, anchorMenuOpen, quickOpen, clusterZoom, optimisticItems\]\);/, "hit-region refresh depends on the transient anchor UI (including the previously omitted anchor menu), the cluster zoom, and the optimistic job icons");
assert.match(component, /\.desktop-icon-menu, \.desktop-icon-toast, \.desktop-icon-anchor-menu, \.desktop-icon-quick/, "the quick toolbar gets the same shadow padding in native hit regions");
assert.match(css, /\.desktop-icon-anchor-menu\s*\{[^}]*--wails-draggable:\s*no-drag;/, "the anchor menu never inherits the window drag region");
assert.match(css, /\.desktop-icon-collapse, \.desktop-icon, \.desktop-icon-popup, \.desktop-icon-menu, \.desktop-icon-anchor-menu, \.desktop-icon-quick, \.desktop-icon-toast\s*\{[^}]*--wails-draggable:\s*no-drag;/, "the shared interactive-controls rule covers the anchor menu and quick toolbar");
assert.match(css, /\.desktop-icon-anchor\s*\{[^}]*--wails-draggable:\s*drag;/, "the anchor itself stays a native window drag handle for left-button dragging");

console.log("desktop icon mode tests passed");
