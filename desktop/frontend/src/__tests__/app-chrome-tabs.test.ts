// Run: tsx src/__tests__/app-chrome-tabs.test.ts

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const testDir = dirname(fileURLToPath(import.meta.url));
const appSource = readFileSync(resolve(testDir, "../App.tsx"), "utf8");
const appChromeSource = readFileSync(resolve(testDir, "../components/AppChrome.tsx"), "utf8");
const commandPaletteSource = readFileSync(resolve(testDir, "../components/CommandPalette.tsx"), "utf8");
const projectTreeSource = readFileSync(resolve(testDir, "../components/ProjectTree.tsx"), "utf8");
const topicShortcutsSource = readFileSync(resolve(testDir, "../lib/topicShortcuts.ts"), "utf8");
const transcriptSource = readFileSync(resolve(testDir, "../components/Transcript.tsx"), "utf8");
const controllerSource = readFileSync(resolve(testDir, "../lib/useController.ts"), "utf8");
const layoutStoreSource = readFileSync(resolve(testDir, "../store/layout.ts"), "utf8");
const stylesSource = readFileSync(resolve(testDir, "../styles.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

let passed = 0;
let failed = 0;

function ok(value: unknown, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function matchingBlocks(selector: string): string[] {
  const blocks: string[] = [];
  const rule = /([^{}]+)\{([^{}]*)\}/g;
  let match: RegExpExecArray | null;
  while ((match = rule.exec(stylesSource)) !== null) {
    const selectors = match[1].split(",").map((part) => part.trim());
    if (selectors.includes(selector)) blocks.push(match[2]);
  }
  return blocks;
}

function finalDeclaration(selector: string, property: string): string | undefined {
  let value: string | undefined;
  for (const block of matchingBlocks(selector)) {
    const declaration = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`, "g");
    let match: RegExpExecArray | null;
    while ((match = declaration.exec(block)) !== null) {
      value = match[1].trim();
    }
  }
  return value;
}

console.log("\napp chrome tabs");

ok(
  /import \{ TabBar \} from "\.\/TabBar";/.test(appChromeSource),
  "AppChrome keeps the classic top session tab strip implementation",
);

for (const propName of ["onTabChange", "onTabClose", "onTabsClose", "onTabsReorder", "onNewTab"]) {
  ok(
    new RegExp(`\\b${propName}\\b`).test(appChromeSource),
    `AppChrome exposes ${propName} for classic tabs`,
  );
}

ok(
  /app-chrome__tab-strip/.test(appChromeSource),
  "AppChrome markup includes classic tab strip containers",
);

ok(
  /const titlebarDragRail = darwinChrome \|\| platform === "windows";/.test(appChromeSource) &&
    /\{titlebarDragRail && <span className="app-chrome__drag-rail"/.test(appChromeSource),
  "AppChrome exposes the classic drag rail on macOS and Windows",
);

ok(
  ["e-resize", "s-resize", "se-resize"].every((edge) => appSource.includes(`edge: "${edge}"`)) &&
    /\{windowsFramelessChrome && <WindowsResizeHandles \/>}[\s\S]*\{windowsFramelessChrome && <WindowsWindowControls \/>}/.test(appSource),
  "Windows frameless chrome renders explicit right and bottom resize handles",
);

ok(
  finalDeclaration(".windows-resize-handle--right", "right") === "0" &&
    finalDeclaration(".windows-resize-handle--right", "cursor") === "ew-resize" &&
    finalDeclaration(".windows-resize-handle--bottom", "bottom") === "0" &&
    finalDeclaration(".windows-resize-handle--bottom", "cursor") === "ns-resize" &&
    finalDeclaration(".windows-resize-handle--bottom-right", "cursor") === "nwse-resize",
  "Windows resize handle CSS covers right, bottom, and bottom-right edges",
);

ok(
  /const WORKSPACE_PANEL_DEFAULT_OPEN = false;/.test(layoutStoreSource) &&
    /workspacePanelOpen:\s*WORKSPACE_PANEL_DEFAULT_OPEN/.test(layoutStoreSource),
  "right dock starts collapsed on launch",
);

ok(
  finalDeclaration(".app-chrome__tab-strip", "overflow") === "hidden",
  "AppChrome tab strip clips tabs to the available chrome width",
);

ok(
  finalDeclaration(".app-chrome__tab-strip", "min-width") === "0",
  "AppChrome tab strip can shrink beside the right dock",
);

ok(
  finalDeclaration(":root[data-theme-style] .app-chrome--tabs .tabbar__tabs", "max-width")?.includes("--chrome-panel-control-size"),
  "themed AppChrome tab lists reserve a flowing new-tab button slot",
);

ok(
  finalDeclaration(":root[data-theme-style] .app-chrome--tabs .tabbar__tabs", "flex") === "0 1 auto",
  "themed AppChrome tab lists size to tab content before shrinking",
);

ok(
  finalDeclaration(":root[data-theme-style] .app-chrome--tabs .tabbar__tabs", "width") === "max-content",
  "themed AppChrome tab lists keep the new-tab button next to the last tab",
);

ok(
  finalDeclaration(":root[data-theme-style] .app-chrome--tabs .tabbar > .tooltip-trigger:has(.tabbar__new)", "flex")?.includes("--chrome-panel-control-size"),
  "themed AppChrome new-tab button keeps a stable slot beside the tabs",
);

ok(
  /workbenchChrome \? \(\s*<span className="app-chrome__spacer" aria-hidden="true" \/>/s.test(appChromeSource),
  "AppChrome workbench branch skips the tab strip",
);

ok(
  /app-chrome__tools--fixed/.test(appChromeSource),
  "AppChrome renders the command search as a fixed chrome tool",
);

ok(
  /workbenchChromeHidden\s*=\s*sidebarWorkbench/.test(appSource),
  "workbench chrome is hidden for every desktop platform",
);

ok(
  /\{!appChromeHidden && \(/.test(appSource),
  "workbench skips rendering the top AppChrome row",
);

ok(
  /topicbar__chrome-btn/.test(appSource),
  "workbench keeps chrome controls in the topic bar",
);

ok(
  /workspace-sidebar__collapse-btn/.test(appSource) &&
    /session-header__expand-btn/.test(appSource) &&
    /workspace-sidebar--collapsed/.test(appSource) &&
    /sidebarCollapsed &&\s*\([\s\S]*?session-header__expand-btn/.test(appSource),
  "real workbench puts PanelLeft collapse in sidebar brand and PanelRight expand in session header when collapsed",
);

ok(
  /const \[transcriptRevealSignal, setTranscriptRevealSignal\] = useState\(0\);/.test(appSource) &&
    /revealActiveSignal=\{tabRevealSignal\}/.test(appSource) &&
    /revealSignal=\{transcriptRevealSignal\}/.test(appSource),
  "transcript bottom reveal is decoupled from tab-strip reveal",
);

const tabsReorderBlock = appSource.match(/const handleTabsReorder = useCallback\([\s\S]*?\n  \}, \[refreshTabMetas, reorderTabs\]\);/)?.[0] ?? "";
ok(
  /setTabRevealSignal/.test(tabsReorderBlock) && !/setTranscriptRevealSignal/.test(tabsReorderBlock),
  "tab reordering refreshes the tab strip without snapping the transcript",
);

ok(
  /aria-label=\{t\("transcript\.jumpToBottom"\)\}/.test(transcriptSource) &&
    /title=\{t\("transcript\.jumpToBottom"\)\}/.test(transcriptSource),
  "jump-to-bottom affordance uses localized transcript text",
);

ok(
  /setActive\(items\.length > 0 \? 0 : -1\)/.test(commandPaletteSource),
  "command palette highlights the first item when opened with an empty query",
);

ok(
  /topicShortcutIndexFromEvent\(event, desktopPlatform\)/.test(appSource) &&
    /useTopicShortcuts\(!sidebarCollapsed, desktopPlatform\)/.test(appSource),
  "topic shortcuts use the resolved desktop platform",
);

ok(
  /topicShortcutLabel\(shortcutIndex, shortcutPlatform\)/.test(projectTreeSource),
  "topic shortcut badges render the platform-specific modifier",
);

ok(
  /if \(!enabled\) hideBadges\(\);/.test(topicShortcutsSource) &&
    /if \(heldRef\.current\) hideBadges\(\);/.test(topicShortcutsSource) &&
    /window\.removeEventListener\("blur", onBlur\);\s*hideBadges\(\);/.test(topicShortcutsSource),
  "topic shortcut badge state is cleared when disabled, interrupted, or cleaned up",
);

ok(
  /const \[rewindCommitting, setRewindCommitting\] = useState\(false\);/.test(appSource) &&
    /rewindStateRef\.current = null;/.test(appSource) &&
    /setRewindCommitting\(true\);/.test(appSource),
  "committing optimistic rewind clears undo state before awaiting the backend",
);

ok(
  /const controllerReady = state\.meta\?\.ready === true && !state\.backendActivationPending;/.test(appSource) &&
    /onPrompt=\{handleTranscriptPrompt\}/.test(appSource) &&
    /submitDisabled=\{false\}/.test(appSource) &&
    appSource.includes("retryTabStartup") &&
    controllerSource.includes("startupSendQueue") &&
    controllerSource.includes("drainStartupSend"),
  "composer queues startup messages while transcript prompts keep their readiness guard",
);

ok(
  /const transcriptHydrating = state\.hydrating && !state\.hydrateHistoryLoaded;/.test(appSource) &&
    /hydrating=\{transcriptHydrating\}/.test(appSource),
  "Welcome is suppressed only until transcript history has loaded",
);

const navigationBlock = appSource.match(/const runNavigationRequest = useCallback\([\s\S]*?\n  \}, \[[^\]]*singleSurfaceLayout[^\]]*\]\);/)?.[0] ?? "";
ok(
  /const navigationRunningRef = useRef\(false\);/.test(appSource) &&
    /const navigationPendingRef = useRef<PendingDesktopNavigationRequest \| null>\(null\);/.test(appSource) &&
    /const runNavigationRequest = useCallback\(async \(request: PendingDesktopNavigationRequest\)/.test(appSource) &&
    /const latest = \(\) => request\.seq === navigationSeqRef\.current;/.test(appSource) &&
    /return activateTopic\(scope, workspaceRoot, topicId/.test(appSource) &&
    /return openTopicSession\(scope, workspaceRoot, topicId/.test(appSource) &&
    /return openGlobalTab\(topicId, runtimeHint\)/.test(appSource) &&
    /return openProjectTab\(workspaceRoot, topicId, runtimeHint\)/.test(appSource) &&
    /enqueueNavigationRequest\([\s\S]*runningRef: navigationRunningRef, pendingRef: navigationPendingRef/.test(appSource) &&
    !/openTopicQueueRef\.current\.catch\(\(\) => \{\}\)\.then/.test(appSource) &&
    /const refreshLatestTabMetas = async \(\): Promise<TabMeta\[]> => \{[\s\S]*if \(latest\(\)\) setTabMetas\(tabs\);/.test(navigationBlock) &&
    /if \(!latest\(\)\) return;[\s\S]*seedActiveTabMeta\(openedTab\);[\s\S]*void refreshLatestTabMetas\(\);/.test(navigationBlock),
  "desktop navigation coalesces pending requests, ignores stale results, and seeds active tab metadata before background refresh",
);

ok(
  /return enqueueNavigation\(\{ kind: "topic", scope, workspaceRoot, topicId, sessionPath, runtimeHint \}\);/.test(appSource) &&
    /enqueueNavigation\(\{ kind: "blank", scope, workspaceRoot: scope === "project" \? workspaceRoot : "" \}\)/.test(appSource) &&
    /return enqueueNavigation\(\{ kind: "sidebar-im", connection \}\);/.test(appSource) &&
    /return enqueueNavigation\(\{ kind: "resume-session", session \}\);/.test(appSource),
  "topic, blank, IM, and history navigation all use the shared coalescing path",
);

ok(
  /else \{[\s\S]*?await resumeSession\(session\.path, targetTab\.id\);/.test(navigationBlock) &&
    /scope === "project" && session\.workspaceRoot \? "project" : "global"/.test(navigationBlock),
  "history navigation opens CLI sessions without topic metadata on a blank scoped tab",
);

ok(
  /else if \(scope === "global" && session\.topicId\) \{[\s\S]*?openTopicTarget\("global", "", session\.topicId, session\.path\);[\s\S]*?\} else \{[\s\S]*?await resumeSession\(session\.path, targetTab\.id\);/.test(navigationBlock),
  "history navigation does not re-resume a session that OpenTopicSession already pinned",
);

ok(
  /<HeartbeatPanel[\s\S]*onOpenTopic=\{\(scope, workspaceRoot, topicId\) => \{[\s\S]*void handleOpenTopic\(scope, workspaceRoot, topicId\);[\s\S]*\}\}/.test(appSource),
  "heartbeat topic navigation uses the guarded open-topic path",
);

for (const selector of [
  ".app--darwin .app-chrome--tabs",
  ":root[data-theme-style] .app--darwin .app-chrome--tabs",
]) {
  const rightSpace = finalDeclaration(selector, "padding-right") ?? finalDeclaration(selector, "padding") ?? "";
  ok(
    rightSpace.includes("--chrome-toggle-size") && !rightSpace.includes("--chrome-right-toggle-offset"),
    `${selector} reserves fixed chrome tool width without shrinking for the right dock`,
  );
}

for (const selector of [
  ".app--windows .app-chrome--native-tabs",
  ".app--linux .app-chrome--native-tabs",
  ":root[data-theme-style] .app--windows .app-chrome--native-tabs",
  ":root[data-theme-style] .app--linux .app-chrome--native-tabs",
]) {
  const rightSpace = finalDeclaration(selector, "padding-right") ?? finalDeclaration(selector, "padding") ?? "";
  ok(
    rightSpace.includes("--chrome-right-toggle-offset"),
    `${selector} reserves right-dock width before rendering tabs`,
  );
}

for (const selector of [
  ".app--windows-frameless .app-chrome--native-tabs",
  ":root[data-theme-style] .app--windows-frameless .app-chrome--native-tabs",
]) {
  const paddingRight = finalDeclaration(selector, "padding-right") ?? "";
  ok(
    finalDeclaration(selector, "--windows-frameless-titlebar-tools-offset") === "var(--windows-window-controls-safe)" &&
      paddingRight.includes("--windows-frameless-titlebar-tools-offset") &&
      paddingRight.includes("--chrome-panel-control-size") &&
      !paddingRight.includes("--chrome-right-toggle-offset"),
    `${selector} keeps titlebar tools fixed beside the Windows controls`,
  );
}

for (const selector of [
  ".app--windows-frameless .app-chrome--native-tabs .app-chrome__panel-toggle--right",
  ":root[data-theme-style] .app--windows-frameless .app-chrome--native-tabs .app-chrome__panel-toggle--right",
]) {
  ok(
    finalDeclaration(selector, "right") === "calc(var(--windows-frameless-titlebar-tools-offset) + 8px)",
    `${selector} stays fixed outside the Windows window controls`,
  );
}

ok(
  finalDeclaration(".app--windows-frameless:not(.app--workbench):not(.app--creation) .app-chrome--native-tabs .app-chrome__drag-rail", "--wails-draggable") === "drag" &&
    finalDeclaration(".app--windows-frameless:not(.app--workbench):not(.app--creation) .app-chrome--native-tabs .app-chrome__drag-rail", "right")?.includes("--windows-window-controls-safe") &&
    finalDeclaration(".app--windows .app-chrome--native-tabs .tabbar", "--wails-draggable") === "no-drag",
  "Windows classic chrome keeps a draggable rail while tabs remain clickable",
);

for (const selector of [
  ".layout--workbench-chrome-hidden",
  ":root[data-theme-style] .layout--workbench-chrome-hidden",
]) {
  ok(
    finalDeclaration(selector, "--app-chrome-height") === "0px" &&
      finalDeclaration(selector, "grid-template-rows") === "minmax(0, 1fr)" &&
      finalDeclaration(selector, "background") === "var(--bg)",
    `${selector} removes the workbench chrome row`,
  );
}

ok(
  finalDeclaration(":root[data-theme-style] .app--darwin .layout--workbench-chrome-hidden", "--app-chrome-height") === "0px" &&
    finalDeclaration(".app--darwin .layout--workbench-chrome-hidden .sidebar--workbench", "padding-top") === "46px" &&
    finalDeclaration(".app--darwin .layout--workbench-chrome-hidden.layout--sidebar-collapsed .topicbar", "padding-left") === "96px",
  "macOS workbench leaves safe space for inset window controls",
);

ok(
  finalDeclaration(".app--darwin .layout--workbench-chrome-hidden.layout--workspace-maximized .workbench-dock__tools", "padding-left") === "96px",
  "macOS maximized workbench dock leaves safe space for inset window controls",
);

ok(
  /@media \(max-width: 820px\) \{[\s\S]*\.app--darwin \.layout--workbench-chrome-hidden \.topicbar\s*\{[\s\S]*padding-left:\s*96px;/.test(stylesSource) &&
    /@media \(max-width: 820px\) \{[\s\S]*\.app--darwin \.layout--workbench-chrome-hidden\.layout--workspace-maximized \.workbench-dock__tools\s*\{[\s\S]*padding-left:\s*96px;/.test(stylesSource),
  "macOS workbench keeps safe space when responsive CSS hides the sidebar",
);

ok(
  finalDeclaration(".app .layout:not(.layout--sidebar-collapsed) .sidebar", "display") === "flex !important" &&
    finalDeclaration(".app .layout:not(.layout--sidebar-collapsed) .sidebar", "position") === "absolute" &&
    finalDeclaration(".app .layout--sidebar-collapsed .sidebar", "display") === "none !important" &&
    finalDeclaration(".app .sidebar", "display") !== "none !important",
  "responsive sidebar toggle keeps expanded navigation visible as a float",
);

ok(
  finalDeclaration(".app .topicbar__chrome-btn", "z-index") === "var(--z-app-chrome)" &&
    finalDeclaration(".app--creation .sidebar-collapse-toggle", "pointer-events") === "auto",
  "responsive navigation toggles stay clickable above the floating sidebar",
);

ok(
  finalDeclaration(".sidebar--workbench .sidebar__head", "display") === "flex" &&
    finalDeclaration(".sidebar--workbench .sidebar__head", "width") === "100%" &&
    finalDeclaration(".sidebar--workbench .sidebar__brand", "flex") === "1 1 auto" &&
    finalDeclaration(".sidebar--workbench .sidebar__header-toggle-wrap", "margin-left") === "auto" &&
    finalDeclaration(".sidebar--workbench .sidebar__header-toggle", "flex") === "0 0 30px",
  "workbench sidebar header reserves a stable collapse button slot",
);

ok(
  finalDeclaration(".workbench-dock__tools", "--wails-draggable") === "drag" &&
    finalDeclaration(".workbench-dock__tabs", "--wails-draggable") === "no-drag" &&
    finalDeclaration(".workbench-dock__tab", "--wails-draggable") === "no-drag",
  "maximized workbench dock keeps a draggable title region while tabs remain clickable",
);

for (const selector of [
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__chrome-btn",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__icon-btn",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__action-btn",
]) {
  ok(
    finalDeclaration(selector, "box-shadow") === "none",
    `${selector} stays flat after removing the workbench chrome row`,
  );
}

ok(
  finalDeclaration(":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar", "background") === "var(--bg-elev)",
  "workbench topic bar uses elevated background for light-mode white",
);

for (const selector of [
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__identity",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__title-row",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__title-row h1",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .tooltip-trigger:has(.topicbar__icon-btn)",
]) {
  ok(
    finalDeclaration(selector, "background") === "transparent" &&
      finalDeclaration(selector, "box-shadow") === "none" &&
      finalDeclaration(selector, "filter") === "none",
    `${selector} cannot paint residual title-row shadows in workbench mode`,
  );
}

for (const selector of [
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__icon-btn",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__chrome-btn",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__icon-btn:hover",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__icon-btn:focus-visible",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__chrome-btn:hover:not(.topicbar__chrome-btn--blocked)",
  ":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__chrome-btn:focus-visible:not(.topicbar__chrome-btn--blocked)",
]) {
  ok(
    finalDeclaration(selector, "background") === "transparent",
    `${selector} does not paint a hover block in workbench mode`,
  );
}

ok(
  finalDeclaration(":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__chrome-btn--sidebar", "background") === "color-mix(in srgb, var(--bg-elev) 68%, transparent)" &&
    finalDeclaration(":root[data-theme-style] .layout--workbench-chrome-hidden .topicbar__chrome-btn--sidebar:hover:not(.topicbar__chrome-btn--blocked)", "border-color") === "color-mix(in srgb, var(--accent) 32%, var(--border))",
  "collapsed workbench navigation toggle keeps a distinct chrome slot",
);

ok(
  finalDeclaration(".skip-to-composer", "box-shadow") === "none" &&
    finalDeclaration(".skip-to-composer:focus-visible", "box-shadow")?.includes("0 12px 28px"),
  "offscreen skip link does not leak its focus shadow into the workbench title area",
);

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
