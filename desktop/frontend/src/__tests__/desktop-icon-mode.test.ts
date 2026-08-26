import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { clusterGridMaxWidth, iconHitRect, ICON_ZOOM_MAX, ICON_ZOOM_MIN, ICON_ZOOM_STEP, normalizeIconZoom, parseCollapseState, parseIconZoom, placeIconPopup, quickStartWorkspaceIndex, scaleIconRect, serializeCollapseState, serializeIconZoom, stepIconZoom, widgetViewportSize } from "../components/widget/desktopIconLayout";
import { CLICK_DELAY, DRAG_THRESHOLD, IconTimers, PREVIEW_CLOSE_DELAY, type TimerHost } from "../components/widget/desktopIconTimers";
import { desktopIconDragOrder, previewDesktopIconMove } from "../components/widget/desktopIconDrag";
import { nextQuickStartApproval, quickStartApprovalLabel, quickStartModelLabel, quickStartModelOptions, quickStartPreferences, resolveQuickStartApproval, resolveQuickStartModel, sameQuickStartIntent } from "../components/widget/quickStartPreferences";
import { deleteConfirmNext, pinnedWorkspaceRows, projectWorkspaceRows, renameTitle, WORKSPACE_PIN_LIMIT, workspacePinsFull } from "../components/widget/workspaceManager";
import { applyRoomPins, normalizeRoomIcons, normalizeRoomPins, pinnedRoomRows, ROOM_PIN_LIMIT, roomPinsFull, roomRows } from "../components/widget/roomsManager";
import { clampRoomIconCount, migrateLegacyRoomIconCount, parseRoomIconCount, readRoomIconCount, LEGACY_ROOM_ICON_VISIBILITY_KEY, ROOM_ICON_COUNT_KEY, visibleDesktopIcons, writeRoomIconCount } from "../components/widget/roomIconCount";
import { consumeRoomPopup, newRoomPopupState, parseRoomNotificationMode, readRoomNotificationMode, reconcileRoomPopups, ROOM_NOTIFICATION_MODE_KEY, roomAttentionLabel, writeRoomNotificationMode } from "../components/widget/roomNotifications";
import { IDLE_HOVER_BURST_WINDOW_MS, IDLE_HOVER_HEALTHY_FRAMES, IDLE_HOVER_HEALTHY_GAP_MS, IDLE_HOVER_RECOVERY_WINDOW_MS, IDLE_HOVER_THRESHOLD_MS, IdleHoverTracer, type IdleHoverSensors } from "../components/widget/idleHoverTrace";
import type { DesktopIconDiagnosticsInput, DesktopIconItem } from "../lib/bridge";
import { isWorkspaceMatteIcon, projectIconKey, WORKSPACE_MATTE_ICON_OPTIONS } from "../lib/projectIcons";
import { canRenameTaskIcon } from "../components/widget/desktopIconRename";
import type { ProjectNode } from "../lib/types";

// --- rename eligibility: a task icon is only renameable when it carries a
// usable SessionRef.SessionPath, matching the backend rename gate so the menu
// never presents a guaranteed-failure entry ---
const taskIcon = (overrides: Partial<DesktopIconItem> = {}): DesktopIconItem => ({
  id: "task:1", kind: "task", sourceId: "s", title: "任务", status: "idle", unreadCount: 0,
  notifications: [], position: { row: "bottom", zone: "running", order: 0 }, revision: "r",
  ...overrides,
});
assert.equal(canRenameTaskIcon(taskIcon({ sessionRef: { scope: "global", sessionPath: "sp-1" } })), true, "task with a session path can rename");
assert.equal(canRenameTaskIcon(taskIcon({ sessionRef: { scope: "global", sessionPath: "   " } })), false, "blank session path cannot rename");
assert.equal(canRenameTaskIcon(taskIcon({ sessionRef: { scope: "global" } })), false, "task without a session path cannot rename");
assert.equal(canRenameTaskIcon(taskIcon({ sessionRef: undefined })), false, "task without a sessionRef cannot rename");
assert.equal(canRenameTaskIcon(taskIcon({ kind: "workspace", sessionRef: { scope: "global", sessionPath: "sp-1" } })), false, "non-task icons never rename");

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

// --- workspace icon "在此发起" must beat the remembered idle preference ---
// The clicked root can arrive with a differently-cased drive letter and `/`
// vs `\` separators; matching still returns the authoritative candidate index.
const winKeys = ["auto", "project:D:\\Work", "project:D:\\Work\\WG2广告", "global"];
assert.equal(quickStartWorkspaceIndex(winKeys, "", "project:d:/Work/WG2广告", "project:D:\\Work"), 2, "the clicked workspace wins over the remembered one even with drive-case and separator differences");
assert.equal(quickStartWorkspaceIndex(winKeys, "", "project:d:\\Work\\WG2广告", "project:D:/Work"), 2, "a backslash clicked root still matches the authoritative candidate");
assert.equal(quickStartWorkspaceIndex(["auto"], "", "project:d:/Work/WG2广告", "project:D:\\Work"), 0, "an empty candidate list safely shows auto until the list arrives");
assert.equal(quickStartWorkspaceIndex(winKeys, "", "project:d:/Work/WG2广告", "project:D:\\Work"), 2, "the first recompute after a late-arriving candidate list selects the clicked workspace");
assert.equal(quickStartWorkspaceIndex(winKeys, "project:D:\\Work", "project:d:/Work/WG2广告", "global"), 1, "an explicit pending edit intent still wins over the clicked workspace for an idempotent retry");
assert.equal(quickStartWorkspaceIndex(winKeys, "", "project:z:/missing", "project:D:\\Work"), 1, "a clicked target absent from the candidate list falls back to the remembered workspace");
assert.equal(quickStartWorkspaceIndex(winKeys, "", "project:z:/missing", ""), 0, "no valid target falls back to auto");

const dragItems: DesktopIconItem[] = [0, 1, 2].map((order) => ({
  id: `task:${order}`, kind: "task", sourceId: String(order), title: String(order), status: "idle", unreadCount: 0,
  notifications: [], revision: String(order), position: { row: "bottom", zone: "running", order },
}));
assert.equal(desktopIconDragOrder(1, 100, 170, 3), 2, "dragging across one icon slot previews the next insertion order");
assert.equal(desktopIconDragOrder(1, 100, -100, 3), 0, "drag preview clamps at the start of its own zone");
const dragged = previewDesktopIconMove(dragItems, "task:0", 2);
assert.deepEqual(dragged.map((item) => item.id), ["task:1", "task:2", "task:0"], "drag preview reorders its zone immediately");
assert.deepEqual(dragItems.map((item) => item.id), ["task:0", "task:1", "task:2"], "drag preview never mutates the authoritative snapshot array");
const mixedDragItems: DesktopIconItem[] = [
  { ...dragItems[0], id: "room:0", kind: "room", position: { row: "top", zone: "conversation", order: 0 } },
  dragItems[0],
  { ...dragItems[0], id: "fixed:new", kind: "fixed", position: { row: "bottom", zone: "fixed", order: 0 } },
  dragItems[1],
];
const mixedDragged = previewDesktopIconMove(mixedDragItems, "task:0", 1);
assert.deepEqual(mixedDragged.map((item) => item.id), ["room:0", "task:1", "fixed:new", "task:0"], "drag preview reorders only the dragged item's row and zone");
assert.deepEqual(mixedDragItems.map((item) => [item.id, item.position]), [
  ["room:0", { row: "top", zone: "conversation", order: 0 }],
  ["task:0", { row: "bottom", zone: "running", order: 0 }],
  ["fixed:new", { row: "bottom", zone: "fixed", order: 0 }],
  ["task:1", { row: "bottom", zone: "running", order: 1 }],
], "mixed-zone drag preview leaves the authoritative array and positions untouched");

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

// Pure placement clamps remain valid for short viewports and near-top anchors.
// These cases intentionally make no claim that the complete search UI fits:
// they only verify the mathematical boundary returned by placeIconPopup.
const SAFE_MARGIN = 10;
for (const viewportHeight of [360, 540, 720]) {
  for (const anchorTop of [10, 40, viewportHeight / 2]) {
    const placed = placeIconPopup({ left: 418, top: anchorTop, width: 66, height: 74 }, 900, viewportHeight, 330);
    const expectedBudget = Math.max(0, Math.min(anchorTop - 9 - SAFE_MARGIN, viewportHeight - SAFE_MARGIN * 2));
    assert.equal(placed.maxHeight, expectedBudget, `placement clamps the mathematical budget at ${viewportHeight}px / top ${anchorTop}px`);
    assert.equal(placed.bottom, Math.max(SAFE_MARGIN, viewportHeight - anchorTop + 9), `placement uses the anchor-relative bottom at ${viewportHeight}px / top ${anchorTop}px`);
  }
}

// Product search placement uses the real bottom-cluster coordinate chain.
// window.inner* starts in the zoomed CSS viewport; widgetViewportSize returns
// native logical dimensions. The cluster scales around the bottom-right
// origin, then the outer inverse desktop transform produces the DOM rect;
// scaleIconRect must reconstruct the same logical rect before placement.
// Keeping both round-trip assertions makes either zoom transform observable.
const SEARCH_CHROME_HEIGHT = 68; // popup padding/border plus fixed search row
for (const [nativeWidth, nativeHeight] of [[640, 540], [1080, 720]] as const) {
  for (const desktopZoom of [0.5, 0.8, 1, 1.25, 1.5, 2]) {
    const cssWidth = nativeWidth / desktopZoom;
    const cssHeight = nativeHeight / desktopZoom;
    const viewport = widgetViewportSize(cssWidth, cssHeight, desktopZoom);
    assert.deepEqual(viewport, { width: nativeWidth, height: nativeHeight }, `CSS viewport round-trips to ${nativeWidth}×${nativeHeight} at desktop zoom ${desktopZoom}`);
    for (const clusterZoom of [0.75, 1, 1.25, 1.5]) {
      const logicalAnchor: { left: number; top: number; width: number; height: number } = {
        left: viewport.width - 18 - 66 * clusterZoom,
        top: viewport.height - 18 - 116 * clusterZoom,
        width: 66 * clusterZoom,
        height: 74 * clusterZoom,
      };
      const cssAnchor = {
        left: logicalAnchor.left / desktopZoom,
        top: logicalAnchor.top / desktopZoom,
        width: logicalAnchor.width / desktopZoom,
        height: logicalAnchor.height / desktopZoom,
      };
      const anchor = scaleIconRect(cssAnchor, desktopZoom);
      assert.deepEqual(anchor, logicalAnchor, `bottom search anchor round-trips at desktop ${desktopZoom} × cluster ${clusterZoom}`);
      const placed = placeIconPopup(anchor, viewport.width, viewport.height, 330);
      const popupTop: number = viewport.height - placed.bottom - placed.maxHeight;
      const label = `${nativeWidth}×${nativeHeight}, desktop ${desktopZoom} × cluster ${clusterZoom}`;
      assert.ok(popupTop >= SAFE_MARGIN - 1e-9, `product search popup respects the safe top margin at ${label}`);
      assert.ok(placed.maxHeight >= SEARCH_CHROME_HEIGHT, `product search budget retains the fixed search controls at ${label}`);
    }
  }
}

const physicalRect = { left: 300, top: 420, width: 66, height: 74 };
assert.deepEqual(iconHitRect(physicalRect, 1.25), { x: 371, y: 521, width: 90, height: 100 }, "native hit region converts CSS coordinates and padding to 125% physical pixels");
assert.deepEqual(iconHitRect(physicalRect, 1.5), { x: 445, y: 625, width: 108, height: 120 }, "native hit region follows the WebView raster scale at 150%");

const component = readFileSync(resolve(import.meta.dirname, "../components/widget/DesktopIconMode.tsx"), "utf8");
assert.match(component, /asArray\(notice\.options\)\.map\(/, "notice choices tolerate legacy or malformed null option arrays without crashing icon mode");
const css = readFileSync(resolve(import.meta.dirname, "../components/widget/desktop-icon-mode.css"), "utf8");
const backend = readFileSync(resolve(import.meta.dirname, "../../../widget_icon_mode.go"), "utf8");
assert.match(component, /readExternalRunLaunch\(localStorage\)[\s\S]+prepareExternalRunLaunch\(localStorage, choice\.root, prompt,[\s\S]+LaunchDSHRun\(\{ requestId: packet\.requestId, workspace: packet\.workspace, prompt: packet\.prompt \}\)/, "DSH quick start reloads and replays the complete persisted launch packet");
assert.match(component, /item\.actions\?\.includes\("cancel"\)[\s\S]+取消 DSH 任务/, "external-run controls render cancel only from the capability-derived action list");
assert.match(component, /item\.kind === "external"[\s\S]+item\.actions\?\.includes\("remove"\)[\s\S]+run\(item, "remove"\)[\s\S]+移除/, "external-run context menu renders remove only from the backend action list");
assert.doesNotMatch(component.slice(component.indexOf("function ExternalRunBody"), component.indexOf("function previewText")), />打开<|>重试<|>恢复<|>批准<|>发送</, "DSH rc.8 external-run popup does not manufacture unsupported controls");
assert.match(backend, /run\.Capabilities\.Cancel && !run\.State\.IsTerminal\(\)[\s\S]+item\.Actions = append\(item\.Actions, "cancel"\)/, "backend freezes the rc.8 capability surface into external icon actions");
assert.match(backend, /run\.State\.IsTerminal\(\)[\s\S]+item\.Actions = append\(item\.Actions, "remove"\)/, "backend freezes remove onto terminal external icons only");
const zhSource = readFileSync(resolve(import.meta.dirname, "../locales/zh.ts"), "utf8");
assert.match(zhSource, /"dailyRoutine\.make": "固化流程"/, "task menu uses the compact 固化流程 label");
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
assert.match(component, /item\.kind === "task" && <><button[\s\S]{0,220}>改名<\/button><button[\s\S]{0,650}createRoutine\(item\)[\s\S]{0,320}"randomize_icon"[\s\S]{0,100}>换个样子<\/button>/, "task/session context menus expose rename, daily-routine extraction, and appearance randomization");
assert.match(component, /CreateDailyRoutine\(\{ tabId: item\.sourceId, sessionRef: item\.sessionRef, requestId: stableRequest \}\)/, "daily routine extraction submits the backend-owned Session identity with a stable retry request");
assert.match(component, /const requestKey = item\.sessionRef\?\.sessionPath \|\| item\.sourceId;[\s\S]{0,260}routineExtractRequests\.current\.set\(requestKey, stableRequest\)[\s\S]{0,180}writeDailyRoutineRequests\(DAILY_ROUTINE_EXTRACT_REQUESTS_KEY/, "failed extraction and renderer restarts reuse a persisted per-Session request id");
assert.match(component, /active && active\.kind === "workspace" && <DailyRoutinePanel key=\{active\.sourceId\} workspaceRoot=\{active\.sourceId\}/, "left-clicking a workspace icon renders its workspace-owned daily routines");
assert.doesNotMatch(component, /desktop-icon-popup__workspace-head"><strong>\{t\("dailyRoutine\.title"\)\}<\/strong><button/, "workspace 日常 header only contains its title");
assert.match(component, /desktop-icon-popup__workspace-foot"><button type="button" className="subtle" onClick=\{onClose\}>\{t\("common\.close"\)\}<\/button><button type="button" onClick=\{onStartHere\}>\{t\("dailyRoutine\.startHere"\)\}<\/button><\/div>/, "workspace 日常 keeps 在此发起 in the bottom action row beside 关闭");
assert.match(component, /const generation = useRef\(0\)[\s\S]{0,900}generation\.current === token && workspaceRoot === root/, "routine list responses are fenced by workspace generation");
assert.match(component, /const requestKey = `\$\{root\}\\u0000\$\{routine\.id\}`;[\s\S]{0,300}writeDailyRoutineRequests\(DAILY_ROUTINE_RUN_REQUESTS_KEY[\s\S]{0,500}RunDailyRoutine\(\{ workspaceRoot: root, routineId: routine\.id, requestId: stableRequest \}\)/, "routine execution persists a workspace-and-ID-scoped retry request before sending");
assert.match(component, /nextRequests\.delete\(requestKey\);[\s\S]{0,320}runRequests\.current = nextRequests;[\s\S]{0,160}await app\.ExitWidgetMode\(result\.tabId\)/, "terminal and pending runs settle the request ledger before the window switch so the next explicit click starts a fresh request");
assert.doesNotMatch(component, /if \(result\.status === "pending"\) return;/, "a pending run no longer keeps the request ledger permanently bound to the old Session");
assert.match(component, /catch \(exitCause\) \{[\s\S]{0,400}runRequests\.current\.set\(requestKey, stableRequest\);[\s\S]{0,200}writeDailyRoutineRequests\(DAILY_ROUTINE_RUN_REQUESTS_KEY, runRequests\.current\)[\s\S]{0,200}throw exitCause;/, "a failed window switch restores the request ledger so a retry reopens the same Session instead of duplicating it");
assert.match(component, /const nextRequests = new Map\(runRequests\.current\);[\s\S]{0,100}nextRequests\.delete\(requestKey\);[\s\S]{0,220}requestStoreCleanupFailed[\s\S]{0,120}runRequests\.current = nextRequests;/, "terminal run cleanup only mutates the in-memory ledger after durable cleanup succeeds and exposes failures");
assert.match(component, /function readDailyRoutineRequests[\s\S]{0,900}localStorage\.removeItem\(key\)[\s\S]{0,160}recovered: true/, "corrupt routine request ledgers are explicitly flagged and self-healed");
assert.match(component, /requestStoreRecovered[\s\S]+requestStoreFailed/, "routine request persistence failures are visible instead of silently degrading idempotency");
assert.match(component, /RenameDailyRoutine\(\{ workspaceRoot: root, routineId: routine\.id, name \}\)[\s\S]+DeleteDailyRoutine\(\{ workspaceRoot: root, routineId: routine\.id \}\)/, "rename and delete address routines by workspace and routine ID");
assert.match(component, /setRenaming\(""\); setRenameDraft\(""\);[\s\S]{0,100}clearRoutineBusy\(routine\.id\);[\s\S]{0,80}await load\(\)/, "rename clears its per-routine busy state before list reload advances the generation");
assert.match(component, /<DailyRoutinePanel key=\{active\.sourceId\} workspaceRoot=\{active\.sourceId\}/, "workspace switches remount the routine panel so stale rows are never interactive under a new workspace");
assert.match(component, /await app\.ExitWidgetMode\(result\.tabId\)/, "successful routine execution opens the exact new Session returned by the backend");
const dailyRoutineBridgeSource = readFileSync(resolve(import.meta.dirname, "../lib/bridge.ts"), "utf8");
assert.match(dailyRoutineBridgeSource, /CreateDailyRoutine[\s\S]+ListDailyRoutines[\s\S]+RunDailyRoutine[\s\S]+RenameDailyRoutine[\s\S]+DeleteDailyRoutine/, "the Wails bridge exposes the complete daily-routine contract");
assert.match(dailyRoutineBridgeSource, /DailyRoutineResult \{ status: [^;]+"pending"/, "the bridge distinguishes acknowledged pending delivery from terminal submission");
assert.match(component, /const displayItems = useMemo\([\s\S]{0,240}previewDesktopIconMove\(visibleItems, dragPreview\.itemId, dragPreview\.order\)/, "drag insertion is projected locally before the backend snapshot changes");
assert.match(component, /void run\(current\.item, "move"[\s\S]{0,180}\.finally\(\(\) => \{ setDraggingID\(""\); setDragPreview\(null\); \}\)/, "pointer release submits one durable move and clears the preview after the authoritative result");
assert.match(css, /\.desktop-icon-wrap\.is-dragging\s*\{[^}]*translateY\(-6px\)[^}]*scale\(1\.035\)/, "the dragged icon has a restrained lifted state");
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
assert.match(component, /isQuickStartJobItem\(current\.item\)\) \{ setDraggingID\(""\); setDragPreview\(null\); return; \}[\s\S]{0,180}void run\(current\.item, "move"/, "dragging an optimistic job never dispatches a backend move");
assert.match(component, /setPreviewID\(""\); setRenamingID\(""\); setRenameDraft\(""\); if \(isQuickStartJobItem\(item\)\) \{ setActiveID\(item\.id\); setMenuID\(""\); \} else \{ setMenuID\(item\.id\); setActiveID\(""\); \}/, "right-clicking an optimistic job closes the hover preview and opens its popup instead of the backend context menu");
assert.match(component, /<QuickStartJobBody job=\{activeQuickJob\}[\s\S]{0,260}onRetry=\{\(requestId\) => \{ quickJobs\.retry\(requestId\); \}\}[\s\S]{0,120}onEdit=\{editQuickStartJob\}[\s\S]{0,120}onDismiss=\{\(requestId\) => \{ if \(quickJobs\.dismiss\(requestId\)\) \{ setActiveID\(""\); setPreviewID\(""\); \} \}\}[\s\S]{0,80}onOpenMain=\{openMainWindow\}[\s\S]{0,160}onOpenTask=\{activeQuickJob\?\.phase === "accepted" && activeQuickJob\.tabId \? \(\) => void openQuickStartTask\(activeQuickJob\) : undefined\}/, "the optimistic popup exposes retry/edit/dismiss wired to the runner, an open-main action for running jobs, and open-task for accepted jobs");
assert.match(component, /const editQuickStartJob = \(job: QuickStartJob\) => \{[\s\S]{0,80}setQuickStartEditJob\(job\);[\s\S]{0,80}setQuickWorkspace\(job\.intent\.workspace \|\| ""\);[\s\S]{0,80}setActiveID\("fixed:new"\);[\s\S]{0,20}\};/, "editing a failed job passes the frozen intent through state/props (never localStorage) and tracks the source requestId");
// --- open-window create: an icon-only button creates a NORMAL blank Session
// through the shared backend workspace-open path (the same semantics as
// double-clicking a Workspace icon), then exits the widget focusing the
// returned tab; it never carries the QuickStart draft/model/approval and never
// touches the optimistic quick-start ledger. ---
assert.match(component, /const openWindowCreate = async \(workspace: string, requestId: string\) => \{[\s\S]{0,120}await app\.OpenWidgetWorkspace\(workspace, requestId\)/, "open-window create reuses the shared backend workspace-open action instead of the conversation deliver");
assert.doesNotMatch(component, /const openWindowCreate = async[\s\S]{0,200}StartWidgetConversation|startWidgetConversationWithRetry\(app\.StartWidgetConversation, input\)/, "open-window create never routes through StartWidgetConversation or the conversation retry helper");
assert.match(quickStartSource, /className="subtle desktop-icon-popup__open-window" disabled=\{openWindowBusy\} aria-label=\{openWindowBusy \? t\("widget\.openWindowCreating"\) : t\("widget\.openWindowCreate"\)\} title=\{openWindowBusy \? t\("widget\.openWindowCreating"\) : t\("widget\.openWindowCreate"\)\} onClick=\{\(\) => void openWindow\(\)\}/, "the open-window action is an icon button with accessible aria-label/title and is only disabled while its own create/exit is in flight");
assert.match(quickStartSource, /const send = \(\) => \{[\s\S]{0,120}sentRef\.current \|\| openWindowRef\.current \|\| openWindowBusy/, "the background send path is blocked while open-window creation is in flight");
assert.match(quickStartSource, /if \(sentRef\.current \|\| openWindowRef\.current \|\| openWindowBusy\) return;/, "the open-window action guards only against the other submit path and double invocation — never the empty draft or settings load");
assert.doesNotMatch(quickStartSource, /const openWindow = async \(\) => \{[\s\S]{0,120}!prompt/, "the open-window action no longer depends on a non-empty draft or loaded settings");
assert.match(quickStartSource, /openWindowRef\.current = true;/, "the open-window action sets its in-flight guard before awaiting the backend");
assert.match(quickStartSource, /await openWindowCreate\(workspace, openWindowIntentRef\.current\.requestId\);/, "the open-window action passes only the selected workspace and its stable requestId, never the draft/model/approval");
assert.match(quickStartSource, /quickStartJobRequestId\("icon-window-new"\)/, "a fresh intent mints a new open-window requestId");
assert.match(quickStartSource, /const key = workspace;[\s\S]{0,120}openWindowIntentRef\.current\.key !== key[\s\S]{0,120}requestId: quickStartJobRequestId\("icon-window-new"\)/, "the open-window requestId regenerates when the workspace changes");
assert.match(quickStartSource, /requestId: openWindowIntentRef\.current\.requestId/, "the open-window requestId is reused across retries of the same workspace");
for (const locale of ["en", "zh", "zh-TW"]) {
	const loc = readFileSync(resolve(import.meta.dirname, `../locales/${locale}.ts`), "utf8");
	assert.ok(loc.includes('"widget.openWindowCreate"'), `${locale} includes the open-window create label`);
	assert.ok(loc.includes('"widget.openWindowCreating"'), `${locale} includes the open-window creating label`);
}
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
// Single-flight polling (#4): a slow snapshot request is shared by every
// caller, and the next one-second delay starts only after it finishes. Stable
// snapshots reuse the current React state so idle polling does not repaint.
assert.match(component, /const refreshPending = useRef<Promise<void> \| null>\(null\);/, "snapshot polling owns one shared in-flight request");
assert.match(component, /if \(refreshPending\.current\) return refreshPending\.current;[\s\S]{0,1200}refreshPending\.current = pending;/, "concurrent refresh callers share the current snapshot request");
assert.match(component, /await refresh\(\);[\s\S]{0,100}window\.setTimeout\(\(\) => void poll\(\), 1000\)/, "the next poll is scheduled one second after the current request completes");
assert.doesNotMatch(component, /setInterval\(\(\) => void refresh\(\), 1000\)/, "snapshot polling never creates overlapping interval requests");
assert.match(component, /current\.revision === next\.revision && \(current\.error \|\| ""\) === \(next\.error \|\| ""\) \? current : next/, "an unchanged snapshot reuses React state and avoids an idle repaint");
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
assert.match(component, /quickJobs\.storageError[\s\S]{0,600}quickJobs\.clearStorageError\(\)/, "durable-storage failures surface in the toast and are dismissible");
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
assert.match(component, /onContextMenu=\{\(event\) => \{ event\.preventDefault\(\); cancelTransientTimers\(\); setAnchorMenuOpen\(false\); setQuickOpen\(false\); setPreviewID\(""\); setRenamingID\(""\); setRenameDraft\(""\); if \(isQuickStartJobItem\(item\)\) \{ setActiveID\(item\.id\); setMenuID\(""\); \} else \{ setMenuID\(item\.id\); setActiveID\(""\); \} \}\}/, "opening an icon context menu immediately closes the hover preview, cancels every delayed timer, and closes the anchor menu and quick toolbar before showing right-click actions; an optimistic job opens its popup instead of the backend menu");
assert.match(component, /QUICK_WORKSPACE_KEY\s*=\s*"wg2\.icon-widget-workspace"/, "QuickStart uses a stable last-workspace key");
assert.match(component, /setQuickWorkspace\(`project:\$\{active\.sourceId\}`\)/, "workspace icons preselect their own workspace in QuickStart");
assert.doesNotMatch(component, /CornerUpRight|desktop-icon__shortcut/, "desktop icons do not render shortcut-arrow badges");
assert.doesNotMatch(css, /desktop-icon__shortcut/, "shortcut-arrow badge styles are removed");
assert.match(component, /const noticeBody = item\.notifications\[0\]\?\.body\.trim\(\);[\s\S]{0,80}if \(noticeBody\) return noticeBody;/, "hover previews use the first notice body instead of replacing it with an unread-count sentence");
assert.doesNotMatch(component, /if \(item\.unreadCount > 0\) return `\$\{item\.unreadCount\} 条待处理信息`/, "unread state never collapses hover content to a count-only bubble");
assert.match(component, /item\.unreadCount > 0 \? " desktop-icon--pending" : ""/, "true unread items opt into the subtle pending visual without changing their data semantics");
assert.match(css, /\.desktop-icon--pending \.desktop-icon__art::after[^}]*desktop-icon-pending-breathe/, "pending icons use a small breathing outline around the icon art");
assert.match(css, /prefers-reduced-motion:[\s\S]*\.desktop-icon--pending \.desktop-icon__art::after \{ animation: none;/, "the pending visual respects reduced-motion preferences");
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
assert.match(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__search\)\s*\{[^}]*height:\s*min\(calc\(var\(--popup-pad-y, 15px\) \+ var\(--popup-pad-y, 15px\) \+ 354px\), var\(--popup-max-height, 420px\)\)[^}]*max-height:\s*var\(--popup-max-height, 420px\)/, "the search outer frame reserves seven result rows with multiplication-free calc syntax and clamps to the measured anchor-space budget");
assert.doesNotMatch(css, /\.desktop-icon-popup__search[^}]*100vh\s*-\s*\d+px/, "search popup rules never bound height with a zoomed 100vh minus fixed-px constant");
assert.match(css, /\.desktop-icon-popup__search\s*\{[^}]*flex:\s*1[^}]*min-height:\s*0[^}]*overflow:\s*hidden/, "the search column consumes and contains the safe parent content box on short viewports");
assert.doesNotMatch(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__search\)[^}]*overflow/, "the search popup outer box keeps overflow visible so the shadow and bottom arrow are never clipped");
assert.match(css, /\.desktop-icon-popup__searchbox[^}]*flex:\s*0 0 auto/, "the search input and close control stay fixed while the results list scrolls");
assert.match(css, /\.desktop-icon-popup__search-content[^}]*flex:\s*1 1 0[^}]*min-height:\s*0[^}]*overflow-y:\s*auto[^}]*overflow-wrap:\s*anywhere/, "one flexing inner region scrolls every search state and wraps long errors without changing the outer height");
const searchPanelSource = component.slice(component.indexOf("function SearchPanel("), component.indexOf("function WorkspaceGlyph("));
assert.match(searchPanelSource, /desktop-icon-popup__searchbox[\s\S]*desktop-icon-popup__search-content" role="region" aria-label="搜索结果" aria-busy=/, "the fixed search controls precede the single scrolling content region");
assert.match(searchPanelSource, /error[\s\S]*role="alert"[\s\S]*loading[\s\S]*搜索中…[\s\S]*results\.length[\s\S]*role="listbox"[\s\S]*role="option"[\s\S]*没有匹配结果/, "error, loading, result listbox, and empty states are mutually exclusive inside the same stable scroll region");
assert.doesNotMatch(searchPanelSource, /role="listbox"[^>]*>\{?(?:error|!loading)/, "non-option error and empty states never become direct listbox children");
assert.match(backend, /desktopIconWidth\s*=\s*1080[\s\S]+desktopIconHeight\s*=\s*720[\s\S]+legacyIconWidth\s*=\s*900[\s\S]+legacyIconHeight\s*=\s*600/, "native icon window enlarges to 1080×720 and recognizes the legacy default for migration");
assert.match(css, /prefers-reduced-motion:\s*reduce/, "motion has a reduced-motion fallback");
assert.match(css, /left:\s*var\(--arrow-left\)/, "popup arrow uses the computed source anchor");
assert.match(css, /\.desktop-icon-popup textarea\s*\{[^}]*resize:\s*none/, "the QuickStart textarea cannot be resized by its corner handle");
assert.match(css, /\.desktop-icon-popup__quick-chip\s*\{/, "model/approval chips are compact clickable controls");
assert.match(css, /\.desktop-icon-popup__picker\s*\{[^}]*position:\s*absolute/, "the picker menus float above the QuickStart content");
assert.match(css, /\.desktop-icon-popup__completion\s*\{[^}]*max-height:\s*128px/, "slash completion candidates stay in a compact roughly three-row scroll area");
assert.match(css, /\.desktop-icon-popup__workspace\s*\{[^}]*grid-template-columns:\s*62px minmax\(0,\s*1fr\) 62px/, "workspace navigation reserves enough width for both shortcut controls");
assert.match(css, /\.desktop-icon-popup__workspace button\s*\{[^}]*white-space:\s*nowrap/, "Ctrl+arrow workspace shortcuts stay on one line");
assert.match(css, /\.desktop-icon-popup__quick-chip-copy strong\s*\{[^}]*overflow-wrap:\s*anywhere[^}]*white-space:\s*normal/, "the selected model name wraps instead of truncating");
assert.match(css, /\.desktop-icon-popup__picker-name\s*\{[^}]*overflow-wrap:\s*anywhere[^}]*white-space:\s*normal/, "model picker rows show the complete model name");
assert.match(css, /\.desktop-icon-popup__picker-meta\s*\{[^}]*display:\s*none/, "provider metadata yields its width to the model name");
assert.match(css, /\.desktop-icon-popup__completion-item\s*\{[^}]*display:\s*grid[^}]*grid-template-areas:\s*"name kind" "desc kind"/, "slash completion gives the command name a dedicated row");
assert.match(css, /\.desktop-icon-popup__completion-name\s*\{[^}]*overflow-wrap:\s*anywhere[^}]*white-space:\s*normal/, "slash and skill completion names wrap without ellipsis");
assert.match(css, /\.desktop-icon-popup__quick-composer\s*\{[^}]*position:\s*relative[^}]*overflow:\s*hidden/, "QuickStart clips the Session-style ghost suffix to the textarea");
assert.match(css, /\.desktop-icon-popup__vocab-ghost\s*\{[^}]*color:\s*transparent[^}]*white-space:\s*pre-wrap[^}]*pointer-events:\s*none/, "the ghost mirrors multiline input without intercepting interaction");
assert.match(css, /\.desktop-icon-popup__vocab-ghost b\s*\{[^}]*color:\s*#727784/, "only the suggested vocabulary suffix is visibly muted");
assert.match(component, /desktop-icon-popup__actions desktop-icon-popup__actions--quick/, "QuickStart actions have a dedicated compact style hook");
assert.match(css, /\.desktop-icon-popup__actions--quick button\s*\{[^}]*min-height:\s*30px;[^}]*padding:\s*4px 10px;/, "QuickStart send and cancel buttons use the compact height");
// The hover preview paragraph wraps inside the 300px box instead of letting
// a long mixed CJK/ASCII summary or an unbroken URL/path/token run paint past
// the border. Rule-level extraction (not whole-file matching) keeps a stray
// later rule from faking the pass.
const exactRules = (selector: string) => [...css.matchAll(new RegExp(`(?:^|})\\s*${selector.replace(/[.*+?^${}()|[\\]\\\\]/g, "\\\\$&")}\\s*\\{([^}]*)\\}`, "gm"))];
const basePopupRules = exactRules(".desktop-icon-popup");
const previewRules = exactRules(".desktop-icon-popup--preview");
const previewTextRules = exactRules(".desktop-icon-popup--preview p");
assert.equal(basePopupRules.length, 1, "the base popup has one exact rule (excluding :has and descendant selectors)");
assert.equal(previewRules.length, 1, "the preview outer box has one exact rule");
assert.equal(previewTextRules.length, 1, "the preview paragraph has one exact rule");
const basePopupRule = basePopupRules[0]?.[1] ?? "";
const previewBoxRule = previewRules[0]?.[1] ?? "";
const previewRule = previewTextRules[0]?.[1] ?? "";
const declarations = (rule: string, property: string) => [...rule.matchAll(new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`, "g"))].map((match) => match[1].trim());
assert.deepEqual(declarations(previewRule, "white-space"), ["normal"], "the sole preview white-space declaration wraps text without a later nowrap/pre cascade");
assert.match(previewRule, /overflow-wrap:\s*anywhere/, "unbroken ASCII/URL/path tokens break anywhere inside the preview box");
assert.match(previewRule, /word-break:\s*break-word/, "WebView-compatible breaking backs up overflow-wrap for the preview paragraph");
assert.deepEqual(declarations(previewRule, "overflow-y"), ["auto"], "the preview paragraph owns vertical scrolling");
assert.match(previewRule, /max-height:\s*max\(0px,\s*calc\(var\(--popup-max-height,\s*420px\)\s*-\s*18px\s*-\s*2px\)\)/, "the preview paragraph consumes the logical height budget after its outer padding and border");
assert.match(previewBoxRule, /max-width:\s*300px/, "the preview outer box keeps its 300px bound as the wrap constraint");
for (const [name, rule] of [["base popup", basePopupRule], ["preview outer box", previewBoxRule]] as const) {
  const outerOverflow = [...rule.matchAll(/(?:^|;)\s*overflow(?:-[xy])?\s*:\s*([^;]+)/g)].map((match) => match[1].trim());
  assert.ok(outerOverflow.every((value) => value === "visible"), `${name} never clips or scrolls its arrow and shadow`);
}
assert.match(component, /onMouseEnter=\{\(\) => timers\.current\?\.clearPreviewClose\(\)\} onMouseLeave=\{\(event\) => \{ if \(!event\.currentTarget\.contains\(document\.activeElement\)\) closePreviewSoon\(\); \}\}/, "pointer entry cancels hover-close and pointer exit cannot close a focused preview");
assert.match(component, /!active && <p tabIndex=\{0\} aria-label=\{`\$\{popupItem\.title\}，\$\{previewText\(popupItem\)\}`\} onFocus=\{\(\) => timers\.current\?\.clearPreviewClose\(\)\} onBlur=\{closePreviewSoon\}>\{previewText\(popupItem\)\}<\/p>/, "the complete scrollable preview is keyboard-focusable and focus cancels the pending hover-close timer");

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
const anchorImageRule = css.match(/\.desktop-icon-anchor img\s*\{[^}]*\}/)?.[0] ?? "";
assert.match(anchorImageRule, /animation:\s*desktop-icon-anchor-keepalive 180s steps\(1, end\) infinite/, "the anchor image changes transform once every ninety seconds without continuous interpolation");
assert.match(anchorImageRule, /will-change:\s*transform/, "the anchor keepalive owns a compositor transform layer");
assert.match(css, /@keyframes desktop-icon-anchor-keepalive\s*\{[\s\S]*translate3d\(0, -\.6px, 0\)[\s\S]*translate3d\(0, \.6px, 0\)/, "the keepalive moves only a subpixel amount around the stationary anchor");
assert.match(css, /prefers-reduced-motion:\s*reduce[\s\S]*\.desktop-icon-anchor img\s*\{\s*animation:\s*none;/, "the experimental anchor keepalive respects reduced-motion preferences");
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
const closeTransientSource = component.slice(component.indexOf("const closeTransient = useCallback"), component.indexOf("// Share one in-flight snapshot request"));
assert.ok(closeTransientSource.indexOf("cancelTransientTimers();") >= 0 && closeTransientSource.indexOf("cancelTransientTimers();") < closeTransientSource.indexOf('setActiveID("")'), "the central close path cancels timers before clearing transient state");
for (const clear of ['setActiveID("")', 'setPreviewID("")', 'setMenuID("")', 'setRenamingID("")', 'setRenameDraft("")', 'setWorkspaceIconRoot("")', 'setPopupAnchorID("")', 'setDraggingID("")', "setDragPreview(null)", "setAnchorMenuOpen(false)", "setQuickOpen(false)"]) {
  assert.ok(closeTransientSource.includes(clear), `the central close path clears ${clear}`);
}
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
assert.match(component, /const refresh = useCallback\(\(\) => \{[\s\S]{0,1200}setError\(next\.error \|\| ""\);/, "the 1s snapshot poll writes only the snapshot error channel");
assert.doesNotMatch(component, /const refresh = useCallback\(\(\) => \{[\s\S]{0,1200}setQuickError/, "the snapshot poll never touches the quick-error channel");
assert.match(component, /\(error \|\| quickError \|\| quickJobs\.storageError \|\| routineNotice\) && <div className="desktop-icon-toast" role=\{error \|\| quickError \|\| quickJobs\.storageError \? "alert" : "status"\}/, "the toast surfaces errors together and announces successful routine extraction as status");
assert.match(component, /\.catch\(\(\) => \{ if \(alive\) \{ setTopmostReadFailed\(true\); setQuickError\(TOPMOST_READ_ERROR\); \} \}\)/, "an initial always-on-top read failure stays visible and never assumes false");
assert.match(component, /disabled=\{exiting \|\| topmostBusy \|\| !topmostLoaded \|\| topmostReadFailed\}/, "the always-on-top switch stays disabled after a failed initial read and while exiting");
assert.match(component, /const \[topmostAttempt, setTopmostAttempt\] = useState\(0\);/, "the always-on-top read retry is driven by an explicit attempt counter");

// --- transient anchor UI closes on any real interaction, and hover preview
// is suppressed while the quick toolbar or anchor menu is open ---
assert.match(component, /if \(!snapshot\.hoverStatusDelayMs \|\| activeID \|\| menuID \|\| drag\.current \|\| anchorMenuOpen \|\| quickOpen\) return;/, "hover preview is suppressed while the quick toolbar or anchor menu is open");
assert.match(component, /drag\.current = \{ item, x: event\.screenX, y: event\.screenY, moved: false, targetOrder: item\.position\.order \};[\s\S]*setAnchorMenuOpen\(false\);[\s\S]*setQuickOpen\(false\);/, "pointer-down records stable screen coordinates and the authoritative start order before closing transient anchor UI");
assert.match(component, /Math\.hypot\(event\.screenX - current\.x, event\.screenY - current\.y\)[\s\S]{0,360}desktopIconDragOrder\(current\.item\.position\.order, current\.x, event\.screenX, count\)/, "window expansion during the first focused preview cannot masquerade as an icon drag");
assert.doesNotMatch(component, /drag\.current = \{ item, x: event\.clientX|Math\.hypot\(event\.clientX - current\.x|desktopIconDragOrder\([^\n]+event\.clientX/, "icon drag never uses viewport-relative coordinates that jump when the native window moves");
assert.match(component, /const doubleClick = \(item: DesktopIconItem\) => \{ timers\.current\?\.cancel\(\); setAnchorMenuOpen\(false\); setQuickOpen\(false\);/, "double-click cancels every delayed timer before running the icon action");
assert.match(component, /setBusy\(true\); setError\(""\); cancelTransientTimers\(\); setAnchorMenuOpen\(false\); setQuickOpen\(false\);/, "a normal icon action cancels the delayed timers and closes the transient anchor UI");
assert.match(component, /const onPointerDown = \(event: PointerEvent\) => \{[\s\S]*if \(target\.closest\(TRANSIENT_PROTECTED_SELECTOR\)\) return;[\s\S]*closeTransient\(\);[\s\S]*document\.addEventListener\("pointerdown", onPointerDown\)/, "a document-level outside-click handler closes every transient surface (timers included) on any container/grid/control gap");
assert.match(component, /TRANSIENT_PROTECTED_SELECTOR = "\.desktop-icon-quick, \.desktop-icon-anchor, \.desktop-icon-anchor-menu, \.desktop-icon-menu, \.desktop-icon, \.desktop-icon-collapse, \.desktop-icon-popup, \.desktop-icon-toast"/, "the outside-click exclusion list covers the toolbar, anchor, menus, valid icons, popups and the toast");
assert.doesNotMatch(component, /event\.target === event\.currentTarget/, "outside-click detection no longer relies on the main element alone");
const renderItemSource = component.match(/const renderItem = \(item: DesktopIconItem\)[\s\S]*?\n\s*const zoomFrame =/)?.[0] ?? "";
assert.ok(renderItemSource, "all desktop icons are rendered through the shared renderItem path");
assert.equal([...renderItemSource.matchAll(/onFocus=/g)].length, 1, "the shared icon renderer defines exactly one focus handler");
const iconFocusBody = renderItemSource.match(/onFocus=\{\(\) => \{([\s\S]*?)\}\} onBlur=/)?.[1] ?? "";
assert.match(iconFocusBody, /^\s*timers\.current\?\.clearPreviewClose\(\);\s*if \(!activeID && !anchorMenuOpen && !quickOpen\) setPreviewID\(item\.id\);\s*$/, "icon focus clears a stale preview-close timer before opening the focused icon preview");

// --- exit round-trip: main/settings disable and the toolbar announces busy,
// and the right-click settings entry stays open on failure (visible retry) ---
assert.match(component, /aria-busy=\{exiting\}/, "the quick toolbar announces the exit round-trip through aria-busy");
assert.match(component, /disabled=\{exiting\} onClick=\{\(\) => void openMainWindow\(\)\}[\s\S]*disabled=\{exiting\} onClick=\{\(\) => void openSettingsWindow\(\)\}/, "open main and settings are disabled while the exit round-trip is in flight");
assert.match(component, /onKeyDown=\{quickRove\}/, "the quick toolbar implements arrow-key roving");
assert.match(component, /const quickRove =[\s\S]*ArrowRight[\s\S]*ArrowLeft[\s\S]*Home[\s\S]*End[\s\S]*buttons\[next\]\.focus\(\)/, "roving moves focus with Left/Right/Home/End and lands on an enabled button");

// --- completion notice: fixed OK / Detail / Dismiss with distinct colors ---
assert.match(backend, /notice := desktopIconNoticeForKept\(kept, persisted\.CompletionSummaries\)[\s\S]{0,360}Notifications: \[\]DesktopIconNotice\{notice\}/, "opened/retained task icons keep using the completion notice card instead of the generic open-only popup");
assert.match(backend, /item\.UnreadCount = desktopIconUnreadCount\(\*item\)[\s\S]+item\.Retained && notice\.Kind == "completed" && strings\.HasPrefix\(notice\.ID, "retained:"\)/, "the retained completion card is presentation-only and never creates an unread badge");
assert.match(component, /desktop-icon-popup__ok"[\s\S]{0,160}onClick=\{onClose\}>OK<\/button>[\s\S]{0,200}desktop-icon-popup__detail"[\s\S]{0,200}disabled=\{busy\} onClick=\{\(\) => run\("open"\)\}>Detail<\/button>[\s\S]{0,200}desktop-icon-popup__dismiss"[\s\S]{0,200}disabled=\{busy\} onClick=\{\(\) => run\("dismiss"\)\}>Dismiss<\/button>/, "completion notices always render OK / Detail / Dismiss in fixed order with their class contracts");
assert.match(component, /desktop-icon-popup__ok"[\s\S]{0,160}onClick=\{onClose\}>OK/, "OK only closes the popup locally");
assert.match(component, /desktop-icon-popup__ok"\s*onClick=\{onClose\}/, "OK is not gated by busy: the class is directly followed by the local-close handler");
assert.doesNotMatch(component, /desktop-icon-popup__ok[\s\S]{0,200}run\("ok"\)/, "OK never dispatches a backend ok action");
assert.doesNotMatch(component, /\[\"ok\", \"dismiss\", \"later\", \"open\", \"reply\"\]/, "ok is no longer a backend-acknowledged action that closes the popup");
assert.match(component, /\[\"dismiss\", \"later\", \"open\", \"reply\", \"continue\", \"remove\"\]/, "dismiss/open/continue/remove close the popup after a successful backend roundtrip");
assert.match(component, /onClose=\{\(\) => \{ setActiveID\(""\);\s*setActiveNoticeID\(""\);\s*setPreviewID\(""\);\s*\}\}/, "OK closes the popup, its exact notice identity and hover preview together");
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
assert.match(component, /role=\{popupAttention \? "alertdialog" : active \? "dialog" : "status"\} aria-label=\{active \? popupAttention \? `\$\{popupItem\.title\}，\$\{roomAttentionLabel\(popupAttention\)\}` : `\$\{popupItem\.title\} 操作` : undefined\}/, "interactive popups expose a named dialog role and mentions escalate to a named alertdialog");
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
assert.match(component, /placeIconPopup\(rect, viewport\.width, viewport\.height, width\)[\s\S]{0,260}\[active, desktopZoom, popupAnchorID, popupItem, popupWidth, snapshot\.revision, viewport\.height, viewport\.width\]/, "popup placement reacts to height-only viewport resizes and explicit source-anchor changes");
assert.match(backend, /Status:\s*\"pending\", Action:\s*\"continue\"/, "task continuation persists a pending receipt before delivery");
assert.match(backend, /advanceDesktopIconTaskContinue[\s\S]+tryDesktopIconReply/, "task continuation uses the acknowledged and recoverable user-turn pipeline");
assert.match(css, /\.desktop-icon-popup__actions button:not\(:disabled\):hover/, "action buttons keep an accessible hover state");
assert.match(css, /\.desktop-icon-popup__routine-actions button:not\(:disabled\):hover/, "routine action buttons expose hover feedback while enabled");
assert.match(css, /\.desktop-icon-popup__workspace-foot button:not\(:disabled\):hover/, "routine footer buttons expose hover feedback while enabled");
assert.match(css, /\.desktop-icon-popup__routine-actions button\.subtle:not\(:disabled\):hover[^}]*background:/, "routine icon buttons gain a visible hover surface instead of filtering a transparent background");
assert.match(css, /\.desktop-icon-popup__routine-actions button\.danger:not\(:disabled\):hover[^}]*background:/, "routine delete keeps a distinct destructive hover state");
assert.match(css, /button:focus-visible[\s\S]*outline: 2px solid #70dfe8/, "action buttons keep a visible focus ring");
assert.match(css, /button:disabled\s*\{\s*opacity: \.55/, "disabled buttons stay visibly disabled");

// --- workspace management: the fixed workspace icon between 新建 and Rooms ---
// The backend fixed bar is the declared Go contract: 新建 → 工作区 → Rooms → 助手 → 委托 → 搜索.
assert.match(backend, /\{"new", "新建", "plus"\},\s*\{"workspace", "工作区", "workspace"\},\s*\{"rooms", "Rooms", "rooms"\},\s*\{"assistant", "助手", "bot"\},\s*\{"delegate", "委托", "users"\},\s*\{"search", "搜索", "search"\}/, "backend fixed bar order is 新建 → 工作区 → Rooms → 助手 → 委托 → 搜索 by declaration");
assert.match(component, /function DelegationPanel\([\s\S]*正在运行的委托[\s\S]*当前没有运行中的委托/, "delegate fixed entry renders a running-list panel with an explicit empty state");
assert.match(component, /error && <p role="alert" className="desktop-icon-popup__delegation-error">委托扫描失败：[\s\S]*列表保留已读取结果，将自动重试/, "delegate panel exposes partial scan failures and automatic retry state inline");
assert.match(component, /active\.sourceId === "delegate"[\s\S]*items=\{snapshot\.delegations \|\| \[\]\}[\s\S]*run\(active, "open_delegation", \[item\.id\]\)/, "delegate list opens the exact typed snapshot item through the idempotent backend action");
const delegationBridge = readFileSync(resolve(import.meta.dirname, "../lib/bridge.ts"), "utf8");
assert.match(delegationBridge, /interface DesktopIconDelegation[\s\S]*sessionRef\?: DesktopIconTaskRef;[\s\S]*interface DesktopIconSnapshot \{ items: DesktopIconItem\[\]; delegations: DesktopIconDelegation\[\]/, "bridge exposes the typed delegation view only through DesktopIconSnapshot");
assert.match(delegationBridge, /interface DesktopIconNotice[\s\S]{0,420}attention\?: "mention_member" \| "mention_agent" \| "mention_both"/, "bridge carries the typed Room member/Agent mention distinction");
assert.match(delegationBridge, /GetDesktopRoomPins\(\): Promise<string\[\]>;[\s\S]+SetDesktopRoomPinned\(topicID: string, pinned: boolean\): Promise<void>;/, "bridge exposes the desktop-specific Room pin API");
assert.match(delegationBridge, /let desktopRoomPins: string\[\] = \[\][\s\S]+async GetDesktopRoomPins\(\)[\s\S]+async SetDesktopRoomPinned\(topicID: string, pinned: boolean\)[\s\S]+desktopRoomPins\.length >= 7/, "browser mock preserves Room pins and enforces the same seven-pin limit");
assert.match(delegationBridge, /GetDesktopRoomIcons\(\): Promise<Record<string, string>>;[\s\S]+SetDesktopRoomIcon\(topicID: string, icon: string\): Promise<void>;/, "bridge exposes the independent Room icon preference API");
assert.match(delegationBridge, /let desktopRoomIcons: Record<string, string> = \{\}[\s\S]+async GetDesktopRoomIcons\(\)[\s\S]+async SetDesktopRoomIcon\(topicID: string, icon: string\)[\s\S]+unsupported Room icon/, "browser mock preserves normalized Room icons and rejects unknown values");
assert.match(component, /item\.kind === "workspace"[\s\S]{0,180}isWorkspaceMatteIcon\(item\.icon\)[\s\S]{0,120}"folder"/, "workspace desktop icons render their assigned matte asset and fall back to the matte folder");
assert.match(component, /item\.kind === "fixed" && item\.sourceId === "workspace"[\s\S]{0,200}setActiveID\(item\.id\)/, "single click on the workspace icon opens the management dialog");
assert.doesNotMatch(component, /item\.sourceId === "workspace"[\s\S]{0,80}run\(item, "open"\)/, "the workspace icon never runs the generic fixed action");
assert.match(component, /active\.sourceId === "workspace" && <WorkspaceManager/, "the workspace popup renders the management dialog");
assert.match(component, /active\.sourceId !== "workspace"/, "the generic fixed popup fallback explicitly excludes the workspace icon");
assert.match(component, /const reload = useCallback\(async \(\) => \{[\s\S]+app\.ListProjectTree\(\)[\s\S]+projectWorkspaceRows\(tree\)/, "the manager loads its authoritative workspace list from ListProjectTree");
assert.match(component, /app\.PickWorkspace\(\)[\s\S]+if \(root\) await reload\(\)/, "a successful workspace pick reloads and keeps the dialog open; a cancelled picker is a no-op");
assert.match(component, /const targetPinned = !row\.pinned;[\s\S]+app\.SetProjectPinned\(row\.root, targetPinned\)[\s\S]+await reload\(\)[\s\S]+await onChanged\(\)/, "Pin toggles through SetProjectPinned with an explicit target for the row root, reloads the authoritative list, and refreshes the widget snapshot so the bottom icons reconcile immediately");
assert.match(component, /Promise\.all\(\[app\.ListProjectTree\(\), app\.GetDesktopWorkspaceSlots\(\)\]\)/, "workspace rows and the persisted desktop count load together");
assert.match(component, /await app\.SetDesktopWorkspaceSlots\(slots\)[\s\S]{0,120}await onChanged\(\)[\s\S]{0,120}setWorkspaceSlots\(slots\)/, "changing the 0-4 desktop count persists and refreshes before committing local UI state, so refresh failures remain retryable");
assert.match(component, /length: WORKSPACE_PIN_LIMIT \+ 1[\s\S]{0,300}aria-pressed=\{workspaceSlots === slots\}/, "the workspace manager exposes every desktop count from zero through four");
assert.match(component, /固定优先，空位由当前与最近活跃工作区补齐/, "the count control explains the retained priority and auto-fill policy");
assert.match(component, /app\.RenameProject\(row\.root, renameTitle\(renameDraft\)\)/, "rename commits the raw input through the shared empty-title contract");
assert.match(component, /留空恢复目录名/, "the rename editor explains the empty-title restore semantics");
assert.match(component, /deleteConfirmNext\(armed, row\.root\)[\s\S]+next\.confirmed\) void confirmDelete\(row\)/, "delete uses the two-step confirm state machine before calling the backend");
assert.match(component, /app\.RemoveWorkspace\(row\.root\)[\s\S]+setArmed\(null\)[\s\S]+await reload\(\)/, "delete calls the backend only on the confirmed step, then clears the arm and reloads");
assert.doesNotMatch(component, /RemoveWorkspace[\s\S]{0,60}setRows/, "delete never optimistically removes the row before the backend confirms");
assert.match(component, /catch \(cause\) \{\s*\/\/ The row stays[\s\S]+setError\(cause instanceof Error \? cause\.message : String\(cause\)\)/, "delete failure keeps the row and the armed retry entry");
assert.match(component, /WORKSPACE_MATTE_ICON_OPTIONS\.map\(\(option\)[\s\S]{0,400}<WorkspaceMatteIcon icon=\{option\.key\}/, "the workspace editor exposes every matte PNG through one typed catalog");
assert.match(component, /await app\.SetProjectIcon\(row\.root, icon\)[\s\S]{0,100}await reload\(\)[\s\S]{0,100}await onChanged\(\)[\s\S]{0,100}setIconEditing\(null\)/, "a successful icon assignment persists, reloads the manager, and refreshes the widget snapshot before closing");
assert.match(component, /item\.kind === "workspace" && <button[\s\S]{0,160}openWorkspaceIconEditor\(item\)[\s\S]{0,160}>修改图标<\/button>/, "a workspace icon context menu exposes 修改图标 for the clicked workspace");
assert.match(component, /<DailyRoutinePanel[\s\S]{0,220}onStartHere=\{\(\) => \{ setQuickWorkspace\(`project:\$\{active\.sourceId\}`\); setQuickStartEditJob\(null\); setPopupAnchorID\(active\.id\); setActiveID\("fixed:new"\); \}\}/, "workspace 日常 panel keeps 在此发起 anchored to the clicked project, clears stale edit intent, and preselects its workspace");
assert.match(component, /itemRefs\.current\.get\(popupAnchorID\) \|\| itemRefs\.current\.get\(popupItem\.id\)/, "popup placement prefers the explicit workspace anchor over the fixed 新建 icon");
assert.match(component, /<QuickStart[\s\S]{0,300}initialWorkspace=\{quickWorkspace\}/, "the anchored QuickStart receives the selected workspace");
assert.match(component, /openWorkspaceIconEditor[\s\S]{0,180}setWorkspaceIconRoot\(item\.sourceId\)[\s\S]{0,120}setActiveID\("fixed:workspace"\)/, "workspace context editing carries the clicked root into the shared manager");
assert.match(component, /<WorkspaceManager initialIconRoot=\{workspaceIconRoot\}[\s\S]{0,180}onChanged=\{refresh\}/, "the workspace manager receives the clicked root and refreshes the live desktop snapshot through its parent-owned entry");
assert.match(component, /Keep the palette open[\s\S]{0,220}setError\(cause instanceof Error \? cause\.message : String\(cause\)\)/, "an icon write failure stays visible and retryable without closing the palette");
assert.match(component, /function WorkspaceGlyph[\s\S]+isWorkspaceMatteIcon\(icon\)[\s\S]+case "star": return <Star[\s\S]+default: return <Folder/, "workspace rows render matte assets while preserving legacy Lucide keys and folder fallback");
assert.match(component, /<WorkspaceGlyph icon=\{row\.icon\} \/>/, "the workspace row renders the icon through the dedicated glyph component");
assert.doesNotMatch(component, /row\.icon \? row\.icon/, "the workspace row never renders the raw icon string");
assert.match(component, /onKeyDown=\{\(event\) => \{[\s\S]{0,80}if \(event\.key === "Enter" && !event\.nativeEvent\.isComposing\)[\s\S]+void commitRename\(row\)[\s\S]+if \(event\.key === "Escape"\) \{ event\.preventDefault\(\); event\.stopPropagation\(\); cancelRename\(\); \}/, "rename confirms with Enter and cancels with Escape without closing the dialog");
assert.match(component, /if \(renamingBusy\) return;[\s\S]+setRenamingBusy\(true\)/, "rename guards against duplicate submission");
assert.match(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__workspaces\)[^}]*max-height:\s*min\(620px, var\(--popup-max-height, 620px\)\)/, "workspace, Room, and routine managers grow naturally with content while treating 620px as the upper bound");
assert.doesNotMatch(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__workspaces\)[^{]*\{[^}]*;\s*height\s*:/, "management popups do not force sparse content to fill the maximum height");
assert.match(css, /\.desktop-icon-popup__workspaces\s*\{[^}]*flex:\s*1[^}]*min-height:\s*0[^}]*overflow:\s*hidden/, "workspace and Room manager contents stay inside the reserved outer frame");
assert.match(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__workspaces\)[^}]*width:\s*min\(420px, calc\(100vw - 20px\)\)/, "the workspace popup is wider while still fitting narrow viewports");
assert.match(css, /\.desktop-icon-popup__workspace-list[^}]*max-height:\s*360px[^}]*overflow-y:\s*auto/, "workspace and Room rows scroll inside the stable five-row budget after async loading");
assert.match(css, /\.desktop-icon-popup__workspace-name[^}]*min-width:\s*0[^}]*text-overflow:\s*ellipsis/, "long workspace names truncate instead of overflowing a narrow window");
assert.match(css, /\.desktop-icon-popup__workspace-actions[^}]*flex-wrap:\s*wrap/, "row actions wrap on narrow windows");
assert.match(css, /\.desktop-icon-popup__workspace-head button:not\(:disabled\):hover/, "workspace add exposes hover feedback while enabled");
assert.match(css, /\.desktop-icon-popup__workspace-actions button\.subtle:not\(:disabled\):hover[^}]*background:/, "workspace row actions gain a visible hover surface");
assert.match(css, /\.desktop-icon-popup__workspace-actions button\.desktop-icon-popup__workspace-delete:not\(:disabled\):hover[^}]*background:/, "workspace delete keeps a distinct destructive hover surface");
assert.match(css, /\.desktop-icon-popup__workspace-count button:not\(:disabled\):hover/, "workspace count choices expose hover feedback while enabled");
assert.match(css, /\.desktop-icon-popup__workspace-pin:not\(:disabled\):hover/, "workspace pin exposes hover feedback while enabled");
assert.match(css, /\.desktop-icon-popup__workspace-pin\s*\{[^}]*margin-right:\s*12px;/, "workspace and Room pins stay clear of the popup scrollbar");
assert.match(css, /\.desktop-icon-popup__workspace-pin\[aria-pressed="true"\]/, "the pinned state has a distinct pressed style");
assert.match(css, /\.desktop-icon-popup__workspace-count button\[aria-pressed="true"\]/, "the selected desktop workspace count has a distinct pressed style");
assert.match(component, /Array\.from\(\{ length: WORKSPACE_PIN_LIMIT \}/, "the workspace footer always renders the fixed slot count");
assert.match(component, /!row\.pinned && pinsFull/, "a full slot set disables only new pins so existing pins can still be removed");
assert.match(css, /\.desktop-icon-popup__workspace-slots[^}]*grid-template-columns:\s*repeat\(4, minmax\(0, 1fr\)\)/, "the pin summary renders four equal desktop slots");
assert.match(css, /\.desktop-icon-popup__workspace-icons[^}]*grid-template-columns:\s*repeat\(8, minmax\(0, 1fr\)\)/, "the matte workspace picker uses a compact eight-column grid");
assert.match(css, /\.desktop-icon__matte[^}]*object-fit:\s*contain/, "matte bitmap icons preserve their aspect ratio in the desktop icon frame");

// --- workspace manager pure logic: authoritative rows + two-step delete ---
// Legacy and matte project icons share one stable key contract; an unknown key
// ("unknown-icon") and a missing key both normalize to the folder fallback "".
const tree: ProjectNode[] = [
  { key: "global_folder", kind: "global_folder", label: "Global", root: "~" },
  { key: "project_a", kind: "project", label: "Alpha", root: "~/projects/alpha", pinned: true, projectIcon: "star" },
  { key: "project_b", kind: "project", label: "Beta", root: "~/projects/beta", projectIcon: "unknown-icon" },
  { key: "project_c", kind: "project", label: "", root: "~/projects/gamma", projectIcon: "python" },
  { key: "orphan", kind: "topic", label: "topic", root: "~/projects/alpha", topicId: "t1" },
];
assert.deepEqual(projectWorkspaceRows(tree), [
  { root: "~/projects/alpha", label: "Alpha", pinned: true, icon: "star" },
  { root: "~/projects/beta", label: "Beta", pinned: false, icon: "" },
  { root: "~/projects/gamma", label: "~/projects/gamma", pinned: false, icon: "python" },
], "rows project only authoritative project nodes, keep the backend tree order, and normalize real/unknown icon keys to the ProjectTree contract");
assert.equal(WORKSPACE_MATTE_ICON_OPTIONS.length, 94, "the typed catalog exposes the original 34 matte icons plus 60 generated Workspace and Room choices");
assert.equal(new Set(WORKSPACE_MATTE_ICON_OPTIONS.map((option) => option.key)).size, 94, "the matte icon catalog has no duplicate keys");
assert.equal(isWorkspaceMatteIcon("typescript"), true, "matte icon lookup accepts a catalog key");
assert.equal(isWorkspaceMatteIcon("rocket"), true, "new matte icon keys are accepted by the shared Workspace and Room contract");
assert.equal(projectIconKey(" PYTHON "), "python", "matte project icons normalize case and whitespace");
assert.equal(projectIconKey("unknown-icon"), "", "unknown project icons still degrade safely to the legacy folder fallback");
assert.deepEqual(projectWorkspaceRows([{ key: "global_folder", kind: "global_folder", label: "Global" }]), [], "a tree without project nodes yields an empty list");
assert.deepEqual(deleteConfirmNext(null, "a"), { armed: "a", confirmed: false }, "the first delete click arms the row");
assert.deepEqual(deleteConfirmNext("a", "a"), { armed: null, confirmed: true }, "the second click on the same row confirms");
assert.deepEqual(deleteConfirmNext("a", "b"), { armed: "b", confirmed: false }, "clicking another row re-arms instead of confirming");
assert.deepEqual(deleteConfirmNext(null, ""), { armed: "", confirmed: false }, "rows always have a root for the delete key");
assert.equal(renameTitle("  新名字  "), "新名字", "rename trims surrounding whitespace");
assert.equal(renameTitle("   "), "", "an empty rename stays empty so the backend restores the folder name");
assert.equal(WORKSPACE_PIN_LIMIT, 4, "the frontend pin capacity matches the four desktop slots");
assert.deepEqual(pinnedWorkspaceRows(projectWorkspaceRows(tree)).map((row) => row.root), ["~/projects/alpha"], "pin slots contain only authoritative pinned rows");
assert.equal(workspacePinsFull(projectWorkspaceRows(tree)), false, "fewer than four pinned rows leave pin actions enabled");
assert.equal(workspacePinsFull(Array.from({ length: 4 }, (_, index) => ({ root: String(index), label: String(index), pinned: true, icon: "" }))), true, "four pinned rows fill the desktop slots");

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
  { topicId: "topic_g", label: "联调 Room", pinned: true, icon: "", scope: "global", workspaceRoot: "", sessionPath: "/tmp/room-g.jsonl" },
  { topicId: "topic_a", label: "设计 Room", pinned: false, icon: "", scope: "project", workspaceRoot: "~/alpha", sessionPath: "/tmp/room-a.jsonl" },
], "rooms project only collaboration topic/global_topic nodes with a real topicId+sessionPath, dedupe by topicId, and keep the backend tree order");
assert.deepEqual(roomRows([
  { key: "g", kind: "global_topic", label: "no session", topicId: "t1", sessionKind: "collaboration" },
  { key: "p", kind: "topic", label: "no topicId", sessionKind: "collaboration", sessionPath: "/tmp/x.jsonl" },
  { key: "s", kind: "session", label: "child", sessionKind: "collaboration", topicId: "t2", sessionPath: "/tmp/s.jsonl" },
]), [], "collaboration topics without a topicId or sessionPath, and bare session nodes, never become Rooms");
assert.deepEqual(roomRows([]), [], "an empty tree yields an empty Rooms list");
assert.deepEqual(normalizeRoomPins(null), [], "a nil Go pin slice from the real Wails binding normalizes to an empty preference");
assert.deepEqual(normalizeRoomPins({ topicIds: ["topic_a"] }), ["topic_a"], "the old persisted-state binding shape remains readable");
assert.deepEqual(normalizeRoomIcons(null), {}, "a nil Go icon map keeps default Room glyphs");
assert.deepEqual(applyRoomPins(roomRows(roomsTree), null).map((row) => row.topicId), ["topic_g", "topic_a"], "nil pin preferences never hide the authoritative local Room list");
assert.equal(ROOM_PIN_LIMIT, 7, "desktop Room pins have seven slots independent of the four workspace slots");
const desktopPinnedRooms = applyRoomPins(roomRows(roomsTree), ["topic_a", "stale", "topic_g", "topic_a"]);
assert.deepEqual(desktopPinnedRooms.map((row) => [row.topicId, row.pinned]), [["topic_a", true], ["topic_g", true]], "desktop Room pin order overrides sidebar topic pins and ignores stale or duplicate ids");
assert.deepEqual(pinnedRoomRows(desktopPinnedRooms).map((row) => row.topicId), ["topic_a", "topic_g"], "the Room pin footer uses the independent desktop pin projection");
assert.equal(roomPinsFull(Array.from({ length: ROOM_PIN_LIMIT }, (_, index) => ({ ...desktopPinnedRooms[0], topicId: String(index), pinned: true }))), true, "seven pinned Rooms fill the desktop Room slots");
assert.equal(roomPinsFull(desktopPinnedRooms), false, "fewer than seven Room pins keep new pin actions enabled");

// --- persisted Room desktop count: clamp, legacy migration, safe write, count filter ---
assert.equal(parseRoomIconCount(null), ROOM_PIN_LIMIT, "missing Room count keeps the legacy full-room default");
assert.equal(parseRoomIconCount("0"), 0, "a persisted zero hides read Rooms");
assert.equal(parseRoomIconCount("3"), 3, "a persisted partial count round-trips");
assert.equal(parseRoomIconCount("7"), ROOM_PIN_LIMIT, "a persisted seven shows the full Room set");
assert.equal(parseRoomIconCount("99"), ROOM_PIN_LIMIT, "an over-limit count clamps to the seven-slot ceiling");
assert.equal(parseRoomIconCount("-2"), 0, "a negative count clamps to zero");
assert.equal(parseRoomIconCount("2.9"), 2, "a fractional count truncates to a whole slot count");
assert.equal(parseRoomIconCount("broken"), ROOM_PIN_LIMIT, "corrupt Room count safely falls back to seven");
assert.equal(parseRoomIconCount('"3"'), ROOM_PIN_LIMIT, "a non-number Room count safely falls back to seven");
assert.equal(clampRoomIconCount(Number.NaN), ROOM_PIN_LIMIT, "a NaN count falls back to seven instead of crashing rendering");
assert.equal(migrateLegacyRoomIconCount(null), ROOM_PIN_LIMIT, "missing legacy visibility migrates to seven");
assert.equal(migrateLegacyRoomIconCount("false"), 0, "a legacy hidden preference migrates to zero");
assert.equal(migrateLegacyRoomIconCount("true"), ROOM_PIN_LIMIT, "a legacy visible preference migrates to seven");
assert.equal(migrateLegacyRoomIconCount("broken"), ROOM_PIN_LIMIT, "corrupt legacy visibility safely migrates to seven");
const roomCountStore = new Map<string, string>();
const roomCountStorage = {
  getItem: (key: string) => roomCountStore.get(key) ?? null,
  setItem: (key: string, value: string) => { roomCountStore.set(key, value); },
};
writeRoomIconCount(roomCountStorage, 4);
assert.equal(roomCountStore.get(ROOM_ICON_COUNT_KEY), "4", "Room count persists under one stable new key");
assert.equal(readRoomIconCount(roomCountStorage), 4, "Room count reads the persisted choice across mounts");
writeRoomIconCount(roomCountStorage, 99);
assert.equal(readRoomIconCount(roomCountStorage), ROOM_PIN_LIMIT, "Room count writes are clamped and idempotent across mounts");
roomCountStore.clear();
roomCountStore.set(LEGACY_ROOM_ICON_VISIBILITY_KEY, "false");
assert.equal(readRoomIconCount(roomCountStorage), 0, "a legacy hidden preference is read as zero while the new key is absent");
roomCountStore.set(LEGACY_ROOM_ICON_VISIBILITY_KEY, "true");
assert.equal(readRoomIconCount(roomCountStorage), ROOM_PIN_LIMIT, "a legacy visible preference is read as seven while the new key is absent");
roomCountStore.set(ROOM_ICON_COUNT_KEY, "2");
assert.equal(readRoomIconCount(roomCountStorage), 2, "the new count key wins over a stale legacy visibility preference");
assert.throws(() => writeRoomIconCount({ getItem: () => null, setItem: () => { throw new Error("quota"); } }, 3), /quota/, "Room count write failures stay explicit for a retryable UI path");
const countItems = [
  { id: "conversation:room-0", kind: "room", unreadCount: 0 },
  { id: "conversation:room-1", kind: "room", unreadCount: 0 },
  { id: "conversation:room-2", kind: "room", unreadCount: 3 },
  { id: "conversation:room-3", kind: "room", unreadCount: 0 },
  { id: "conversation:person", kind: "person", unreadCount: 0 },
  { id: "task:one", kind: "task", unreadCount: 0 },
  { id: "fixed:rooms", kind: "fixed", unreadCount: 0 },
] as Parameters<typeof visibleDesktopIcons>[0];
assert.deepEqual(visibleDesktopIcons(countItems, 0).map((item) => item.id), ["conversation:room-2", "conversation:person", "task:one", "fixed:rooms"], "a zero count hides every read Room while the unread Room and every non-Room icon stay visible");
assert.deepEqual(visibleDesktopIcons(countItems, 2).map((item) => item.id), ["conversation:room-0", "conversation:room-1", "conversation:room-2", "conversation:person", "task:one", "fixed:rooms"], "a partial count keeps the first Rooms in authoritative order plus the unread overflow");
assert.deepEqual(visibleDesktopIcons(countItems, 7), countItems, "a full count reuses the complete authoritative item list");
const overflowRead = countItems.map((item) => item.id === "conversation:room-2" ? { ...item, unreadCount: 0 } : item);
assert.deepEqual(visibleDesktopIcons(overflowRead, 2).map((item) => item.id), ["conversation:room-0", "conversation:room-1", "conversation:person", "task:one", "fixed:rooms"], "an unread overflow Room disappears again after its unread count clears");

// --- Room notification mode persistence + monotonic popup queue ---
assert.equal(parseRoomNotificationMode(null), "count", "missing Room notification mode defaults to count");
assert.equal(parseRoomNotificationMode('"popup"'), "popup", "popup mode round-trips");
assert.equal(parseRoomNotificationMode('"invalid"'), "count", "invalid Room notification mode falls back to count");
assert.equal(parseRoomNotificationMode("broken"), "count", "corrupt Room notification mode falls back to count");
const roomNotificationStore = new Map<string, string>();
const roomNotificationStorage = { getItem: (key: string) => roomNotificationStore.get(key) ?? null, setItem: (key: string, value: string) => { roomNotificationStore.set(key, value); } };
writeRoomNotificationMode(roomNotificationStorage, "popup");
assert.equal(roomNotificationStore.get(ROOM_NOTIFICATION_MODE_KEY), '"popup"', "Room notification mode persists under one stable key");
assert.equal(readRoomNotificationMode(roomNotificationStorage), "popup", "Room notification mode reads across mounts");
assert.throws(() => writeRoomNotificationMode({ getItem: () => null, setItem: () => { throw new Error("quota"); } }, "popup"), /quota/, "notification-mode storage failures stay explicit");
const popupRoom = (id: string, sequence: number, createdAt: number, attention?: "mention_member" | "mention_agent" | "mention_both"): DesktopIconItem => ({
  id: `conversation:${id}`, kind: "room", sourceId: id, title: id, status: "unread", unreadCount: 1,
  position: { row: "top", zone: "conversation", order: 0 }, revision: `${id}-${sequence}`, conversationSequence: sequence,
  notifications: [{ id: `${id}-${sequence}`, revision: String(sequence), kind: "message", priority: attention ? 9 : 3, title: id, body: `message-${sequence}`, createdAt, conversation: id, readSequence: sequence, attention, options: [] }],
});
let popupState = reconcileRoomPopups(newRoomPopupState(), [popupRoom("history", 5, 50)], "popup");
assert.deepEqual(popupState.queue, [], "the first real snapshot establishes watermarks without replaying historical unread messages");
const oldMentionWithNewOrdinary = popupRoom("history", 6, 60);
oldMentionWithNewOrdinary.notifications = [
  { ...popupRoom("history", 5, 50, "mention_member").notifications[0], id: "history-old-mention", title: "Alice @了你" },
  { ...oldMentionWithNewOrdinary.notifications[0], id: "history-new-ordinary", attention: undefined, title: "Alice 的新消息" },
];
popupState = reconcileRoomPopups(popupState, [oldMentionWithNewOrdinary], "popup");
assert.deepEqual(popupState.queue.map((entry) => [entry.noticeId, entry.sequence, entry.attention]), [["history-new-ordinary", 6, undefined]], "a new ordinary message never reopens an older unread mention");
popupState = reconcileRoomPopups(popupState, [popupRoom("history", 6, 60), popupRoom("mention", 1, 70, "mention_agent")], "popup");
assert.deepEqual(popupState.queue.map((entry) => entry.itemId), ["conversation:mention", "conversation:history"], "mentions jump ahead of ordinary queued messages before chronological order");
popupState = reconcileRoomPopups(popupState, [popupRoom("history", 6, 60), popupRoom("mention", 1, 70, "mention_agent")], "popup");
assert.equal(popupState.queue.length, 2, "repeated snapshots never queue the same Room sequence twice");
const consumedMention = consumeRoomPopup(popupState);
assert.equal(consumedMention.candidate?.itemId, "conversation:mention", "queue consumption opens the highest-priority candidate once");
const afterClose = reconcileRoomPopups(consumedMention.state, [popupRoom("history", 6, 60), popupRoom("mention", 1, 70, "mention_agent")], "popup");
assert.deepEqual(afterClose.queue.map((entry) => entry.itemId), ["conversation:history"], "closing a consumed mention does not queue that same sequence again");
const mentionBatch = popupRoom("batch", 2, 100, "mention_agent");
mentionBatch.notifications = [
  { ...popupRoom("batch", 1, 90, "mention_member").notifications[0], id: "batch-member" },
  { ...mentionBatch.notifications[0], id: "batch-agent" },
];
let batchState = reconcileRoomPopups(newRoomPopupState(), [popupRoom("batch", 0, 0)], "popup");
batchState = reconcileRoomPopups(batchState, [mentionBatch], "popup");
assert.deepEqual(batchState.queue.map((entry) => entry.noticeId), ["batch-member", "batch-agent"], "same-batch member and Agent mentions retain exact notice identities in chronological order");
popupState = reconcileRoomPopups(consumedMention.state, [popupRoom("history", 7, 80)], "count");
assert.deepEqual(popupState.queue, [], "count mode advances watermarks and clears pending auto-popups");
popupState = reconcileRoomPopups(popupState, [popupRoom("history", 7, 80)], "popup");
assert.deepEqual(popupState.queue, [], "switching to popup does not replay a sequence already observed in count mode");
assert.equal(roomAttentionLabel("mention_member"), "提到了你");
assert.equal(roomAttentionLabel("mention_agent"), "提到了你的 Agent");
assert.equal(roomAttentionLabel("mention_both"), "提到了你和你的 Agent");

// --- rooms fixed icon: glyph, dialog open, and the generic fallback exclusion ---
assert.match(component, /rooms: "discussion"/, "the rooms fixed icon renders its own distinct matte discussion asset");
assert.match(component, /item\.kind === "fixed" && item\.sourceId === "rooms"[\s\S]{0,220}setActiveID\(item\.id\)/, "single click on the rooms icon opens the management dialog");
assert.doesNotMatch(component, /item\.sourceId === "rooms"[\s\S]{0,80}run\(item, "open"\)/, "the rooms icon never runs the generic fixed action");
assert.match(component, /active\.sourceId === "rooms" && <RoomsManager/, "the rooms popup renders the management dialog");
assert.match(component, /active\.sourceId !== "rooms"/, "the generic fixed popup fallback explicitly excludes the rooms icon");
assert.match(component, /<RoomsManager roomIconCount=\{roomIconCount\} onRoomIconCountChange=\{setRoomIconCount\} notificationMode=\{roomNotificationMode\} onNotificationModeChange=\{setRoomNotificationMode\} onClose=\{\(\) => setActiveID\(""\)\} onChanged=\{refresh\}/, "RoomsManager receives pin refresh, desktop count and notification-mode coordination callbacks");

// --- rooms manager dialog contract: authoritative load, safe mutations, no
// optimistic writes, explicit placeholder ---
assert.match(component, /Promise\.allSettled\([\s\S]{0,700}app\.ListProjectTree\(\)[\s\S]{0,700}app\.GetDesktopRoomPins[\s\S]{0,700}app\.GetDesktopRoomIcons/, "RoomsManager settles the Room tree and optional preference bindings independently");
assert.match(component, /treeResult\.status === "rejected"[\s\S]{0,900}normalizeRoomPins\(pinsResult\.value\)[\s\S]{0,900}normalizeRoomIcons\(iconsResult\.value\)[\s\S]{0,900}setRows\(applyRoomIcons\(applyRoomPins\(roomRows\(treeResult\.value\), pins\), icons\)\)/, "RoomsManager keeps the authoritative tree when nil, missing, old or malformed preference bindings fall back to defaults");
assert.match(component, /Room 设置加载失败（已使用默认值）/, "preference degradation remains explicit after the Room list recovers");
assert.match(component, /app\.OpenTopicSession\(row\.scope, row\.workspaceRoot, row\.topicId, row\.sessionPath\)[\s\S]+onOpenRoom\(meta\.id\)/, "opening a Room activates the backend tab and exits the widget focused on it");
assert.match(component, /const targetPinned = !row\.pinned;[\s\S]+app\.SetDesktopRoomPinned\(row\.topicId, targetPinned\)[\s\S]+await reload\(\)[\s\S]+await onChanged\(\)/, "Room pin toggles use the desktop-specific idempotent API, reload rows and refresh the snapshot");
assert.match(component, /Array\.from\(\{ length: ROOM_PIN_LIMIT \}/, "the Room manager always renders seven pin slots");
assert.match(component, /!row\.pinned && pinsFull/, "a full Room pin set disables only new pins so existing pins can be removed");
assert.match(component, /app\.RenameTopic\(row\.topicId, renameTitle\(renameDraft\)\)/, "rename commits the raw input through the shared empty-title contract");
assert.match(component, /留空恢复自动标题/, "the room rename editor explains the empty-title restore semantics");
assert.match(component, /deleteConfirmNext\(armed, row\.topicId\)[\s\S]+next\.confirmed\) void confirmTrash\(row\)/, "trash uses the two-step confirm state machine before calling the backend");
assert.match(component, /app\.TrashTopic\(row\.topicId\)[\s\S]+setArmed\(null\)[\s\S]+await reload\(\)/, "trash calls the backend only on the confirmed step, then clears the arm and reloads");
assert.doesNotMatch(component, /TrashTopic[\s\S]{0,60}setRows/, "trash never optimistically removes the row before the backend confirms");
assert.match(component, /await app\.SetDesktopRoomIcon\(row\.topicId, icon\)[\s\S]{0,100}await reload\(\)[\s\S]{0,100}await onChanged\(\)[\s\S]{0,100}setIconEditing\(null\)/, "Room icon assignment persists, reloads and refreshes before closing the palette");
assert.match(component, /默认 Room 图标[\s\S]{0,260}<WorkspaceMatteIcon icon="social" \/>[\s\S]{0,260}WORKSPACE_MATTE_ICON_OPTIONS\.map\(\(option\)[\s\S]{0,400}<WorkspaceMatteIcon icon=\{option\.key\}/, "Room icon palette previews the social matte default before the shared matte catalog");
assert.match(component, /Keep the palette open[\s\S]{0,220}setError\(cause instanceof Error \? cause\.message : String\(cause\)\)/, "a Room icon failure keeps the palette open for retry");
assert.match(component, /function RoomGlyph[\s\S]+isWorkspaceMatteIcon\(icon\)[\s\S]+default: return <WorkspaceMatteIcon icon="social" className=\{matteClassName\}/, "Room rows use the social matte asset by default while preserving configured matte and legacy icons");
assert.match(component, /<RoomGlyph icon=\{row\.icon\}/, "Room rows and slots render their configured glyph instead of raw icon text");
assert.match(component, /desktop-icon-popup__workspaces desktop-icon-popup__rooms/, "RoomsManager reuses the WorkspaceManager layout and marks its own popup root");
assert.match(component, /onClick=\{onNewRoom\}>新增<\/button>/, "the Rooms header 新增 delegates to the root App coordination callback");
assert.match(component, /writeRoomIconCount\(localStorage, count\);[\s\S]{0,100}onRoomIconCountChange\(count\)/, "the Rooms count persists before reflecting the new displayed count");
assert.match(component, /catch \(cause\) \{[\s\S]{0,120}保存 Room 显示数量设置失败/, "a Room count persistence failure remains explicit and retryable in the manager");
assert.match(component, /writeRoomNotificationMode\(localStorage, mode\);[\s\S]{0,100}onNotificationModeChange\(mode\)/, "notification mode persists before the UI reflects it, so write failures keep the confirmed mode");
assert.match(component, /role="radiogroup" aria-label="Room 消息提醒方式"[\s\S]{0,260}role="radio" aria-checked=\{notificationMode === "count"\}[\s\S]{0,260}role="radio" aria-checked=\{notificationMode === "popup"\}/, "Rooms exposes accessible count and popup notification choices as the only top settings row");
assert.match(component, /desktop-icon-popup__workspace-count desktop-icon-popup__room-count/, "the Rooms count selector reuses the workspace count layout and marks its own room-count modifier");
assert.match(component, /aria-label="桌面 Room 显示数量"[\s\S]{0,300}Array\.from\(\{ length: ROOM_PIN_LIMIT \+ 1 \}, \(_, count\)[\s\S]{0,300}aria-pressed=\{roomIconCount === count\}[\s\S]{0,100}chooseRoomCount\(count\)/, "the Rooms manager exposes every desktop count from zero through seven");
assert.match(component, /desktop-icon-popup__room-pins"[\s\S]*?desktop-icon-popup__room-count[\s\S]*?优先固定/, "the Room count selector sits in the pin area before the 优先固定 heading");
assert.match(component, /固定优先，空位由其余 Room 按顺序补齐/, "the Room count control explains pinned priority and the authoritative fill order");
assert.match(component, /reconcileRoomPopups\(current, snapshot\.items, roomNotificationMode\)/, "each real snapshot advances the monotonic Room popup tracker");
assert.match(component, /if \(!snapshotLoaded\) return/, "the placeholder snapshot never establishes popup history watermarks");
assert.match(component, /activeID \|\| previewID \|\| menuID \|\| renamingID \|\| anchorMenuOpen \|\| quickOpen \|\| draggingID \|\| busy \|\| exiting/, "automatic Room popups wait while another interaction owns the widget");
assert.match(component, /consumeRoomPopup\(roomPopupState\)[\s\S]{0,300}setActiveNoticeID\(consumed\.candidate\.noticeId\)[\s\S]{0,100}setActiveID\(consumed\.candidate\.itemId\)/, "automatic Room popups consume and bind the exact new notice before opening so close and refresh cannot replay it");
assert.match(component, /const activeNotice = active\?\.notifications\.find\(\(notice\) => notice\.id === activeNoticeID\) \?\? active\?\.notifications\[0\]/, "automatic popup identity selects its exact notice while manual clicks retain the first-notice behavior");
assert.match(component, /<NoticeBody item=\{active\} notice=\{activeNotice\}[\s\S]{0,180}run\(active, action, values, activeNotice\)/, "Room popup actions submit the exact notice id and read sequence that triggered the popup");
assert.match(component, /visibleDesktopIcons\(mergedItems, roomIconCount\)/, "the widget derives one visible icon source from the persisted Room count");
assert.match(component, /!collapsed && rows\.top\.length > 0/, "a fully hidden Room row leaves no empty reserved row on the desktop");
assert.doesNotMatch(component, /openCollaborationDialog|CreateCollaboration|HostRoom|JoinRoom/, "RoomsManager never re-implements the Host/Join Room form; the root App owns openCollaborationDialog");
assert.match(css, /\.desktop-icon-popup:has\(\.desktop-icon-popup__rooms\)/, "the rooms popup keeps the same bounded popup layout as the workspace manager");
assert.match(css, /\.desktop-icon-popup__room-count > div[^}]*grid-template-columns:\s*repeat\(8, 28px\)/, "the Room count selector renders eight number buttons in one row");
assert.doesNotMatch(css, /\.desktop-icon-popup__room-visibility/, "the legacy Room visibility switch styles are removed with the switch");
assert.match(css, /\.desktop-icon-popup__room-notification button:not\(:disabled\):hover[^}]*background:/, "Room notification choices expose hover feedback");
assert.match(css, /\.desktop-icon-popup__room-notification button\[aria-checked="true"\]:not\(:disabled\):hover[^}]*background:/, "the selected Room notification choice keeps a distinct hover state");
assert.match(css, /\.desktop-icon-popup__room-slots[^}]*grid-template-columns:\s*repeat\(7, minmax\(0, 1fr\)\)/, "the Room pin summary renders seven equal slots");
assert.match(component, /item\.kind === "room"[\s\S]{0,180}<RoomGlyph icon=\{projectIconKey\(item\.icon\)\}[\s\S]{0,220}item\.notifications\.some\(\(notice\) => notice\.attention\) && <AtSign className="desktop-icon__room-mention"/, "a configured Room glyph scans every unread notice and retains the distinct mention badge");
assert.match(component, /desktop-icon-popup__eyebrow--mention[\s\S]{0,180}notice\.title \|\| roomAttentionLabel\(attention\)/, "mention popup headings preserve the backend author-aware title and use the local label only as fallback");
assert.match(component, /popupAttention \? "alertdialog"[\s\S]{0,300}aria-live=\{popupAttention \|\|/, "Room mention popups use assertive alertdialog semantics while ordinary messages remain polite");
assert.match(component, /notice\.kind === "message" && \(item\.kind === "room" \|\| item\.kind === "person"\) && <button[\s\S]{0,160}run\("reply"/, "mention presentation preserves the Room reply action");
assert.match(component, /notice\.kind === "message" && <button[\s\S]{0,160}run\("open"\)[\s\S]{0,80}>打开会话<\/button>/, "mention presentation preserves the open-conversation action");
assert.match(css, /\.desktop-icon-popup--mention[^}]*border-color:[^}]*box-shadow:/, "Room mention popups have a distinct high-attention visual treatment");

// --- App coordination: monotonic signal, exit-before-open, tab-focus exit ---
const appSource = readFileSync(resolve(import.meta.dirname, "../App.tsx"), "utf8");
const controllerSource = readFileSync(resolve(import.meta.dirname, "../lib/useController.ts"), "utf8");
assert.match(appSource, /const createProjectSession = useCallback\(async \(scope: string, workspaceRoot: string\)[\s\S]{0,260}await createBlankSession\("project", workspaceRoot, `blank-session-\$\{crypto\.randomUUID\(\)\}`, singleSurfaceLayout\)/, "each window-mode workspace new intent generates a stable request ID and routes through the shared creator");
assert.match(appSource, /onCreateTopic=\{createProjectSession\}/, "every project-tree variant uses the shared workspace session creator");
assert.match(controllerSource, /app\.CreateBlankSession\(\{ scope, workspaceRoot, requestId \}\)/, "the frontend sends the typed blank-session intent without exposing internal tab fields");
assert.match(controllerSource, /if \(singleSurface\) \{[\s\S]{0,180}statesRef\.current\.delete\(id\)/, "single-surface creation drops stale invisible controller state after the backend prunes tabs");
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
assert.match(component, /\}, \[activeID, menuID, previewID, snapshot\.revision, collapsed, anchorMenuOpen, quickOpen, clusterZoom, optimisticItems, roomIconCount, surfaceGeneration, overlayReadyKey\]\);/, "hit-region refresh reruns after native surface generations change and after an already-expanded surface mounts a new popup");
assert.match(component, /SetDesktopIconHitRegions\(\{ rects, generation: surfaceGeneration \}\)/, "hit regions are tied to the surface generation whose coordinates they use");
assert.match(component, /\.desktop-icon-menu, \.desktop-icon-toast, \.desktop-icon-anchor-menu, \.desktop-icon-quick/, "the quick toolbar gets the same shadow padding in native hit regions");
assert.match(css, /\.desktop-icon-anchor-menu\s*\{[^}]*--wails-draggable:\s*no-drag;/, "the anchor menu never inherits the window drag region");
assert.match(css, /\.desktop-icon-collapse, \.desktop-icon, \.desktop-icon-popup, \.desktop-icon-menu, \.desktop-icon-anchor-menu, \.desktop-icon-quick, \.desktop-icon-toast\s*\{[^}]*--wails-draggable:\s*no-drag;/, "the shared interactive-controls rule covers the anchor menu and quick toolbar");
assert.match(css, /\.desktop-icon-anchor\s*\{[^}]*--wails-draggable:\s*drag;/, "the anchor itself stays a native window drag handle for left-button dragging");

// --- idle-hover diagnostics: pointer inactivity without timers, one start
// write + one bounded recovery summary per qualifying trace, no content leak ---
const traceSource = readFileSync(resolve(import.meta.dirname, "../components/widget/idleHoverTrace.ts"), "utf8");
const bridgeSource = readFileSync(resolve(import.meta.dirname, "../lib/bridge.ts"), "utf8");
assert.doesNotMatch(traceSource, /setInterval/, "idle tracking and the recovery window never use a periodic timer");
assert.match(traceSource, /IDLE_HOVER_THRESHOLD_MS\s*=\s*5000/, "the qualifying idle threshold is five seconds");
assert.match(traceSource, /IDLE_HOVER_RECOVERY_WINDOW_MS\s*=\s*3000/, "the recovery window has a hard three-second timeout");
assert.match(traceSource, /IDLE_HOVER_HEALTHY_FRAMES\s*=\s*10/, "recovery finishes after a short healthy-frame streak");
assert.match(traceSource, /\.disconnect\(\)/, "every recovery sensor disconnects when the window closes");
assert.doesNotMatch(traceSource, /\btitle\b|\bprompt\b|\.body\b/, "diagnostics records never carry icon titles, prompts or message bodies");
assert.match(traceSource, /finishRecovery\(session, "aborted"\)[\s\S]*finishRecovery\(session, "timeout"\)[\s\S]*finishRecovery\(session, "healthy"\)/, "the recovery window has exactly the three bounded end states");
assert.match(component, /new IdleHoverTracer\(\{/, "the icon widget instantiates the idle-hover diagnostics tracer");
assert.match(component, /app\.WriteDesktopIconDiagnostics\(record\)/, "trace records persist through the typed diagnostics binding");
assert.match(component, /hoverEnter\("icon", \{ iconCount: visibleItems\.length, revision: snapshot\.revision \}\)/, "icon hovers open a trace with the actually visible icon count and state revision");
assert.match(component, /onPointerEnter=\{\(\) => idleTrace\.current\?\.hoverEnter\("anchor"/, "hovering the window-drag anchor also opens a trace (anchor kind)");
assert.match(component, /"pointerover", "pointermove", "pointerdown", "pointerup", "wheel", "keydown"/, "pointer inactivity is stamped by window listeners only, with no idle timer");
assert.match(component, /idleTrace\.current\?\.dispose\(\); \}/, "unmount disposes the tracer so an in-flight recovery closes with an aborted summary");
assert.match(bridgeSource, /WriteDesktopIconDiagnostics\(input: DesktopIconDiagnosticsInput\): Promise<void>;/, "the frontend bridge exposes the typed diagnostics write");
assert.match(bridgeSource, /DesktopIconDiagnosticsPath\(\): Promise<string>;/, "the frontend bridge exposes the diagnostics log path getter");

// --- idle-hover tracer unit tests: deterministic fake clock, rAF queue and
// sensors, mirroring the exact record schema the Go side validates ---
class FakeIdleTraceEnv {
  now = 0;
  writes: DesktopIconDiagnosticsInput[] = [];
  private rafQueue = new Map<number, (ts: number) => void>();
  private timers = new Map<number, { fn: () => void; at: number }>();
  private nextTimer = 1;
  private nextRaf = 1;
  private sensors: IdleHoverSensors = {
    longtask: () => () => {},
    layoutShift: () => () => {},
    mutation: () => () => {},
    visibilityChange: () => () => {},
  };

  tracer(overrides?: { sensors?: IdleHoverSensors; writeError?: Error }): IdleHoverTracer {
    const write = (record: DesktopIconDiagnosticsInput) => {
      if (overrides?.writeError) throw overrides.writeError;
      this.writes.push(record);
    };
    return new IdleHoverTracer({
      write,
      mono: () => this.now,
      wall: () => 1700000000000 + this.now,
      raf: (cb) => { const id = this.nextRaf++; this.rafQueue.set(id, cb); return id; },
      caf: (id) => { this.rafQueue.delete(id); },
      setTimeout: (fn, ms) => { const id = this.nextTimer++; this.timers.set(id, { fn, at: this.now + ms }); return id; },
      clearTimeout: (id) => { this.timers.delete(id); },
      visibility: () => "visible",
      focus: () => true,
      viewport: () => ({ w: 1080, h: 720 }),
      dpr: () => 1.5,
      sensors: { ...this.sensors, ...overrides?.sensors },
    });
  }

  pointer(tracer: IdleHoverTracer, at: number): void {
    this.now = at;
    tracer.pointerActivity();
  }

  hover(tracer: IdleHoverTracer, at: number, kind: "icon" | "anchor" = "icon"): void {
    this.now = at;
    tracer.hoverEnter(kind, { iconCount: 7, revision: "r1" });
  }

  frame(at: number): void {
    this.now = at;
    const first = [...this.rafQueue.entries()].sort((a, b) => a[0] - b[0])[0];
    assert.ok(first, "expected a scheduled rAF frame");
    this.rafQueue.delete(first[0]);
    first[1](at);
  }

  fireTimeout(): void {
    const earliest = [...this.timers.entries()].sort((a, b) => a[1].at - b[1].at)[0];
    assert.ok(earliest, "expected a scheduled hard-timeout timer");
    this.now = earliest[1].at;
    this.timers.delete(earliest[0]);
    earliest[1].fn();
  }

  hasPendingFrame(): boolean {
    return this.rafQueue.size > 0;
  }

  startTrace(): { env: FakeIdleTraceEnv; tracer: IdleHoverTracer } {
    const tracer = this.tracer();
    this.pointer(tracer, 1000);
    this.pointer(tracer, 11000);
    this.hover(tracer, 11300, "icon");
    return { env: this, tracer };
  }
}

{
  const env = new FakeIdleTraceEnv();
  const tracer = env.tracer();
  env.pointer(tracer, 1000);
  env.hover(tracer, 1000 + IDLE_HOVER_THRESHOLD_MS - 1, "icon");
  assert.equal(env.writes.length, 0, "a hover just below the idle threshold writes nothing");
  assert.equal(tracer.active, false, "no trace is active after a sub-threshold hover");
  env.hover(tracer, 1000 + IDLE_HOVER_THRESHOLD_MS, "icon");
  assert.equal(env.writes.length, 1, "a hover exactly at the idle threshold qualifies");
  assert.equal(tracer.active, true, "the qualifying hover opens an active recovery");
}

{
  const env = new FakeIdleTraceEnv();
  const tracer = env.tracer();
  env.pointer(tracer, 1000); // activity at 1s
  env.pointer(tracer, 11000); // 10s pause: this activity opens a burst
  env.hover(tracer, 11300, "icon");
  assert.equal(env.writes.length, 1, "a hover ending a >=5s idle period opens exactly one start record");
  const start = env.writes[0];
  assert.equal(start.kind, "hover_start");
  assert.equal(start.targetKind, "icon");
  assert.equal(start.idleMs, 10000, "the start record carries the true idle measured at burst start, not the approach movement");
  assert.equal(start.t0, 11300);
  assert.equal(start.ts, 1700000000000 + 11300);
  assert.equal(start.iconCount, 7);
  assert.equal(start.revision, "r1");
  assert.equal(start.visibility, "visible");
  assert.equal(start.focus, true);
  assert.equal(start.viewportW, 1080);
  assert.equal(start.viewportH, 720);
  assert.equal(start.dpr, 1.5);
  assert.equal(tracer.active, true, "a qualifying hover opens an active recovery");
  // Repeated hovers / pointer movement during the same recovery must never
  // open a second trace.
  env.hover(tracer, 11350, "icon");
  env.pointer(tracer, 11400);
  env.hover(tracer, 11450, "anchor");
  assert.equal(env.writes.length, 1, "repeated hovers and movement inside one recovery never start duplicate traces");
}

{
  const env = new FakeIdleTraceEnv();
  const tracer = env.tracer();
  env.pointer(tracer, 1000.25);
  env.pointer(tracer, 11000.75);
  env.hover(tracer, 11300.5, "icon");
  assert.equal(env.writes[0].idleMs, 10001, "fractional browser idle time is rounded for the Go int64 binding");
  assert.equal(env.writes[0].t0, 11301, "fractional performance.now is rounded for the Go int64 binding");
  env.frame(11500.75);
  env.fireTimeout();
  assert.ok(Number.isInteger(env.writes[1].durationMs), "recovery duration is an integer at the Wails boundary");
  assert.ok(Number.isInteger(env.writes[1].worstFrameGapMs), "frame gaps are integers at the Wails boundary");
}

{
  const env = new FakeIdleTraceEnv();
  const tracer = env.tracer();
  env.pointer(tracer, 11000); // burst opens with the full 11s idle
  // The hover lands beyond the burst window: the inherited burst idle expires
  // and the measurement falls back to the gap since the last pointer event.
  env.hover(tracer, 11000 + IDLE_HOVER_BURST_WINDOW_MS + 500, "icon");
  assert.equal(env.writes.length, 1, "a hover past the burst window still qualifies on its own idle");
  assert.equal(env.writes[0].idleMs, IDLE_HOVER_BURST_WINDOW_MS + 500, "beyond the burst window idle is measured from the last event");
}

{
  const { env, tracer } = new FakeIdleTraceEnv().startTrace();
  env.frame(11500); // first frame gap 200ms -> unhealthy
  for (let i = 1; i <= IDLE_HOVER_HEALTHY_FRAMES; i++) env.frame(11500 + i * (IDLE_HOVER_HEALTHY_GAP_MS - 24)); // 16ms gaps
  assert.equal(env.writes.length, 2, "a healthy frame streak closes the trace with one summary");
  assert.equal(env.hasPendingFrame(), false, "no rAF frame is left scheduled after a healthy finish");
  const summary = env.writes[1];
  assert.equal(summary.kind, "hover_recovery");
  assert.equal(summary.traceId, env.writes[0].traceId, "the summary references the same trace");
  assert.equal(summary.endedBy, "healthy");
  assert.equal(summary.frames, 1 + IDLE_HOVER_HEALTHY_FRAMES, "the summary counts every sampled frame including the first");
  assert.equal(summary.worstFrameGapMs, 200, "the worst frame gap is the first unhealthy gap");
  const expectedAvg = Math.round((200 + (IDLE_HOVER_HEALTHY_FRAMES) * (IDLE_HOVER_HEALTHY_GAP_MS - 24)) / (1 + IDLE_HOVER_HEALTHY_FRAMES));
  assert.equal(summary.avgFrameGapMs, expectedAvg, "the average frame gap covers all sampled frames");
  assert.equal(tracer.active, false, "the tracer is free for the next qualifying trace after the summary");
  // A new trace still needs a fresh >=5s idle period; activity resets it.
  env.hover(tracer, 15000, "icon");
  assert.equal(env.writes.length, 2, "a hover right after recovery has no fresh idle and writes nothing");
  env.pointer(tracer, 16000);
  env.hover(tracer, 23000, "anchor");
  assert.equal(env.writes.length, 3, "after another >=5s pause a new trace opens");
  assert.notEqual(env.writes[2].traceId, env.writes[0].traceId, "each trace has its own unique id");
}

{
  const { env } = new FakeIdleTraceEnv().startTrace();
  env.frame(11500);
  env.fireTimeout(); // hard timeout: rAF stops (e.g. hidden document)
  assert.equal(env.writes.length, 2, "the hard timeout closes the window and writes one summary");
  const summary = env.writes[1];
  assert.equal(summary.endedBy, "timeout");
  assert.equal(summary.frames, 1);
  assert.equal(summary.durationMs, IDLE_HOVER_RECOVERY_WINDOW_MS, "duration is the bounded window length");
  assert.equal(env.hasPendingFrame(), false, "no frame remains scheduled after the timeout path");
}

{
  const env = new FakeIdleTraceEnv();
  const tracer = env.tracer({
    sensors: {
      longtask: (observe) => {
        observe(100);
        observe(50);
        return () => {};
      },
      layoutShift: (observe) => {
        observe({ value: 0.2, hadRecentInput: false });
        observe({ value: 0.1, hadRecentInput: true });
        return () => {};
      },
      mutation: (observe) => {
        observe(5);
        return () => {};
      },
      visibilityChange: (observe) => {
        observe();
        return () => {};
      },
    },
  });
  env.hover(tracer, 1000, "icon");
  env.pointer(tracer, 11000);
  env.hover(tracer, 11300, "icon");
  env.frame(11500);
  env.frame(11516);
  env.frame(11532);
  env.frame(11548);
  env.frame(11564);
  env.frame(11580);
  env.frame(11596);
  env.frame(11612);
  env.frame(11628);
  env.frame(11644);
  env.frame(11660);
  assert.equal(env.writes.length, 2, "sensor aggregates land in the single recovery summary");
  const summary = env.writes[1];
  assert.equal(summary.longTasks, 2);
  assert.equal(summary.longTasksMaxMs, 100);
  assert.equal(summary.longTasksTotalMs, 150);
  assert.equal(summary.layoutShifts, 1, "recent-input layout shifts are excluded as unsafe");
  assert.equal(summary.domMutations, 5);
  assert.equal(summary.visibilityChanges, 1);
  assert.equal(summary.endedBy, "healthy");
}

{
  const env = new FakeIdleTraceEnv();
  const tracer = env.tracer({ writeError: new Error("disk full") });
  env.hover(tracer, 1000, "icon");
  env.pointer(tracer, 11000);
  env.hover(tracer, 11300, "icon");
  env.frame(11500);
  env.frame(11516);
  env.frame(11532);
  env.frame(11548);
  env.frame(11564);
  env.frame(11580);
  env.frame(11596);
  env.frame(11612);
  env.frame(11628);
  env.frame(11644);
  env.frame(11660);
  assert.equal(env.writes.length, 0, "a failing diagnostics write never throws into the widget and is dropped");
  assert.equal(tracer.active, false, "a failed write still lets the recovery finish");
}

{
  const { env, tracer } = new FakeIdleTraceEnv().startTrace();
  env.frame(11500);
  tracer.dispose();
  assert.equal(env.writes.length, 2, "dispose closes an in-flight recovery with an aborted summary");
  assert.equal(env.writes[1].endedBy, "aborted");
  assert.equal(env.hasPendingFrame(), false, "dispose cancels the scheduled frame");
  // React StrictMode double-mounts effects in dev (mount -> cleanup -> mount):
  // the cleanup disposes the tracer, so the SAME instance must stay usable for
  // a later qualifying trace instead of being permanently disabled.
  env.pointer(tracer, 22000);
  env.hover(tracer, 28500, "icon");
  assert.equal(env.writes.length, 3, "a disposed tracer stays usable for a later qualifying trace (StrictMode remount)");
  assert.equal(env.writes[2].idleMs, 6500, "the second trace measures the fresh idle period");
}

// --- Agent Icon 显示路径契约（真实 task ↔ QuickStart ↔ 旧动效抑制） ---

import { isAgentIconItem } from "../lib/agentIcon/viewModel";

const testDir = dirname(fileURLToPath(import.meta.url));

assert.equal(
  isAgentIconItem({ id: "task:tab-1", kind: "task", sourceId: "tab-1", title: "t", status: "idle", unreadCount: 0, notifications: [], position: { row: "bottom", zone: "running", order: 0 }, revision: "r" }),
  true,
  "real backend task items render the Agent Icon",
);
assert.equal(
  isAgentIconItem({ id: "opt:job-1", kind: "task", sourceId: "job-1", title: "t", status: "idle", unreadCount: 0, notifications: [], position: { row: "bottom", zone: "running", order: 0 }, revision: "r" }),
  false,
  "QuickStart optimistic items keep the legacy Bot icon until a real session forms",
);
assert.equal(
  isAgentIconItem({ id: "conversation:im:user-1", kind: "person", sourceId: "im:user-1", title: "user", status: "unread", unreadCount: 1, notifications: [], position: { row: "top", zone: "conversation", order: 0 }, revision: "r", sessionId: "session-1" }),
  true,
  "a personal IM row with resolved session identity reuses that session's Agent Icon",
);
assert.equal(
  isAgentIconItem({ id: "conversation:im:legacy", kind: "person", sourceId: "im:legacy", title: "legacy", status: "unread", unreadCount: 1, notifications: [], position: { row: "top", zone: "conversation", order: 0 }, revision: "r" }),
  false,
  "an unresolved personal IM row keeps the generic Users fallback",
);

// 静态源码契约（本仓库既有做法，如 workbench-layout.test.ts）：真实 task 的
// 图标框内只有 Agent Icon（frame/headwear/eyes/badge/tool），旧 Bot、状态
// 动效、状态 glyph 在 Agent 条目上被抑制 —— 状态只由 LED 眼睛表达。
{
  const mode = readFileSync(resolve(testDir, "../components/widget/DesktopIconMode.tsx"), "utf8");
  assert.match(mode, /if \(item\.kind === "task"\) return agentViewModel \? <AgentIcon viewModel=\{agentViewModel\} \/> : <Bot \/>;/, "task branch renders AgentIcon for real tasks, Bot for QuickStart");
  assert.match(mode, /if \(item\.sourceId === "assistant"\) return <WorkspaceMatteIcon icon="robot" className="desktop-icon__matte" \/>;/, "the assistant fixed entry renders the robot matte asset");
  assert.match(mode, /if \(item\.kind === "person"\) return agentViewModel \? <AgentIcon viewModel=\{agentViewModel\} \/> : <Users \/>;/, "resolved personal IM rows render the corresponding session AgentIcon with a safe Users fallback");
  assert.match(mode, /!agentIcon && \(item\.status === "running" \|\| item\.status === "thinking"\)/, "old running/thinking motion corners are suppressed for Agent Icon items");
  assert.match(mode, /!agentIcon && statusGlyph\(item\)/, "old status glyph overlay is suppressed for Agent Icon items");
  assert.match(mode, /desktop-icon__art\$\{agentIcon \? " desktop-icon__art--agent" : ""\}/, "real task icons opt into the transparent Agent Icon art surface");
  assert.match(css, /\.desktop-icon__art--agent\s*\{[^}]*width:\s*58px[^}]*border:\s*0[^}]*background:\s*transparent[^}]*box-shadow:\s*none/, "Agent Icon removes the generic rounded app-tile chrome and fills the desktop slot");
  assert.match(mode, /const agentIconViewModels = useMemo\([\s\S]{0,400}isAgentIconItem\(item\)[\s\S]{0,120}buildAgentIconViewModel\(item\)/, "viewModels are memoized once per real task item");
  // 点击/打开行为零改动：pointerDown/pointerUp/doubleClick/run(open) 原样保留。
  assert.match(mode, /onPointerDown=\{\(event\) => pointerDown\(event, item\)\}/, "pointerDown handler unchanged");
  assert.match(mode, /onPointerUp=\{\(event\) => pointerUp\(event, item\)\}/, "pointerUp handler unchanged");
  assert.match(mode, /onDoubleClick=\{\(\) => doubleClick\(item\)\}/, "doubleClick handler unchanged");
  assert.match(mode, /void run\(item, "open"\)/, "run(item, \"open\") path unchanged");
  assert.match(mode, /item\.kind === "fixed" \? openItem\(item\) : void run\(item, "open"\)/, "menu open path unchanged");
  assert.match(mode, /item\.retained \|\| item\.kind === "person"/, "personal IM rows expose the same remove menu action as retained session icons");
  assert.match(mode, /item\.kind === "person" && <button disabled=\{busy\} className="danger" onClick=\{\(\) => run\("remove"\)\}>移除<\/button>/, "personal IM popup exposes an explicit remove action");
}

// bridge 快照类型携带 Agent Icon 展示字段（稳定身份、显式外观、workspace/ref）。
{
  const bridge = readFileSync(resolve(testDir, "../lib/bridge.ts"), "utf8");
  assert.match(bridge, /sessionId\?: string;\s*\n\s*appearanceSeed\?: string;\s*\n\s*workspaceIcon\?: string;[\s\S]{0,120}sessionRef\?: DesktopIconTaskRef;/, "DesktopIconItem exposes the Agent Icon display fields");
}

console.log("desktop icon mode tests passed");
