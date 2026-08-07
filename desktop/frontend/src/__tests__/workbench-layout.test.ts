// workbench-layout.test.ts — structural tests for the real workbench layout.
// Verifies that the real workbench path (.app--workbench / .layout--workbench)
// uses proper CSS selectors and does NOT leak .app--iris / .layout--iris.
// Also verifies the simplified workbench tree inside workspace-sidebar.
//
// Run: npx tsx src/__tests__/workbench-layout.test.ts

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const testDir = dirname(fileURLToPath(import.meta.url));
const appSource = readFileSync(resolve(testDir, "../App.tsx"), "utf8");
const stylesSource = readFileSync(resolve(testDir, "../styles.css"), "utf8");
const layoutStoreSource = readFileSync(resolve(testDir, "../store/layout.ts"), "utf8");
const projectTreeSource = readFileSync(resolve(testDir, "../components/ProjectTree.tsx"), "utf8");
const runtimeConfigSource = readFileSync(resolve(testDir, "../components/desktop-ui/RuntimeConfigBar.tsx"), "utf8");
const workWorkspaceSource = readFileSync(resolve(testDir, "../components/work/WorkWorkspace.tsx"), "utf8");
const collaborationSource = readFileSync(resolve(testDir, "../collab/CollaborationWorkspace.tsx"), "utf8");

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function includes(text: string, pattern: string): boolean {
  return text.includes(pattern);
}

function finalDeclaration(source: string, selector: string, property: string): string | undefined {
  // Return the last value for a given property in a selector block.
  const stripped = source.replace(/\/\*[\s\S]*?\*\//g, "");
  const pattern = new RegExp(
    `${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`, "g",
  );
  let match;
  let result: string | undefined;
  while ((match = pattern.exec(stripped)) !== null) {
    const block = match[1];
    const propMatch = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`).exec(block);
    if (propMatch) result = propMatch[1].trim();
  }
  return result;
}

// ── Tests ────────────────────────────────────────────────────────────────────

process.stdout.write("\nworkbench layout test\n\n");

// App.tsx uses correct selectors
ok(includes(appSource, '"app--workbench"'), "App.tsx: app--workbench class exists");
ok(includes(appSource, '"layout--workbench"'), "App.tsx: layout--workbench class exists");
ok(!includes(appSource, '"app--iris"'), "App.tsx: app--iris class does NOT exist");
ok(!includes(appSource, '"layout--iris"'), "App.tsx: layout--iris class does NOT exist");

// ProjectTree variant=workbench exists
ok(includes(projectTreeSource, 'variant === "workbench"'), "ProjectTree: variant workbench branch exists");
ok(includes(projectTreeSource, "compactTopics = variant === \"workbench\""), "ProjectTree: compactTopics maps to workbench");

// CSS: layout--workbench has correct grid
const workbenchGrid = includes(
  stylesSource,
  ".app--workbench .layout--workbench {\n  position: relative;\n  isolation: isolate;\n  grid-template-columns: 300px minmax(0, 1fr);",
);
ok(workbenchGrid, "CSS: .layout--workbench grid is 300px | 1fr");

// CSS: workspace-sidebar exists with proper width
const sidebarWidth = finalDeclaration(stylesSource, ".app--workbench .workspace-sidebar", "width");
ok(sidebarWidth === "300px", `CSS: workbench workspace-sidebar width is 300px (got: ${sidebarWidth})`);
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar", "box-sizing") === "border-box",
  "CSS: sidebar includes its horizontal padding",
);
ok(
  includes(layoutStoreSource, "export const WORKBENCH_SIDEBAR_WIDTH = 300;") &&
    includes(appSource, 'const sidebarRenderWidth = desktopLayoutStyle === "workbench" ? WORKBENCH_SIDEBAR_WIDTH : (liveSidebarWidth ?? sidebarWidth);'),
  "App.tsx: workbench grid track uses the same fixed 300px width as workspace-sidebar",
);

// CSS: workspace-sidebar project-tree folder-main uses 9px gap (matching fixture)
const folderGap = finalDeclaration(stylesSource, ".workspace-sidebar .project-tree__folder-main", "gap");
ok(folderGap === "8px", `CSS: workspace-sidebar folder-main gap is 8px (got: ${folderGap})`);

ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__tree", "overflow-x") === "hidden",
  "CSS: real workspace tree cannot create horizontal scrolling",
);
ok(
  includes(stylesSource, ".workspace-sidebar *::-webkit-scrollbar:horizontal") &&
    includes(stylesSource, "height: 0;") &&
    includes(stylesSource, ".workspace-sidebar * {\n  scrollbar-width: auto;"),
  "CSS: nested workbench controls cannot paint a horizontal scrollbar",
);

// CSS: workspace-sidebar topic active owns one outlined row and one left marker.
ok(
  includes(stylesSource, "border-color: var(--session-list-selected-border);") &&
    includes(stylesSource, "box-shadow: inset 4px 0 0 var(--tree-row-marker);"),
  "CSS: active topic has outlined selection and left accent marker",
);

ok(
  includes(projectTreeSource, 'className="project-tree__workspace-actions"') &&
    includes(projectTreeSource, "<MoreVertical") &&
    includes(projectTreeSource, "void handleCreateTopic(scope, projectRoot, key)"),
  "ProjectTree: workbench project menu exposes create and more actions",
);
ok(
  includes(projectTreeSource, 'mode === "workbench" ? (') &&
    includes(projectTreeSource, "<SquarePlus") &&
    includes(projectTreeSource, "void handleAddProject()"),
  "ProjectTree: workbench header keeps the add-workspace action available",
);
ok(
  !includes(projectTreeSource, 'renderTimeFilterControl("workbench")') &&
    includes(projectTreeSource, '(section === "recent" || !compactTopics)') &&
    includes(projectTreeSource, '!compactTopics && section !== "recent"'),
  "ProjectTree: workbench hides header and session overflow controls while preserving classic actions",
);
ok(
  includes(projectTreeSource, 'role="switch"') &&
    includes(projectTreeSource, "WORKBENCH_RECENT_LIMITS.map") &&
    includes(projectTreeSource, "saveWorkbenchRecentSettings(next)") &&
    includes(projectTreeSource, 'project-tree__section--recent'),
  "ProjectTree: recent section stays visible and persists external-call and row-count settings",
);
ok(
  !includes(projectTreeSource, 'className="project-tree__recent-settings-slot"') &&
    includes(projectTreeSource, 'onPointerDown={(event) => event.stopPropagation()}') &&
    includes(projectTreeSource, 'aria-controls="project-tree-recent-settings"'),
  "ProjectTree: recent settings uses a direct, isolated trigger without a tooltip event wrapper",
);
ok(
  finalDeclaration(stylesSource, ".app--workbench .workspace-sidebar .project-tree__recent-settings-trigger", "width") === "32px" &&
    finalDeclaration(stylesSource, ".app--workbench .workspace-sidebar .project-tree__recent-settings-trigger", "height") === "32px" &&
    finalDeclaration(stylesSource, ".app--workbench .workspace-sidebar .project-tree__recent-settings-trigger", "touch-action") === "manipulation",
  "CSS: recent settings trigger keeps a stable 32px pointer target",
);
ok(
  finalDeclaration(stylesSource, ".app--workbench .workspace-sidebar .project-tree__section-title", "display") === "flex",
  "CSS: recent title overrides the legacy hidden workspace section title",
);
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar .project-tree__header-actions", "opacity") === "1" &&
    finalDeclaration(stylesSource, ".workspace-sidebar .project-tree__header-actions", "pointer-events") === "auto",
  "CSS: workbench header actions remain visible and clickable",
);
ok(
  includes(stylesSource, ".app--workbench .workspace-sidebar .project-tree__workspace-actions") &&
    includes(stylesSource, "visibility: visible;") &&
    includes(stylesSource, "pointer-events: auto;"),
  "CSS: project overflow action remains visible without moving the label",
);

// CSS: footer contents shrink inside the real viewport instead of reserving titlebar space
ok(
  includes(stylesSource, ".layout--workbench .session-footer-dock .runtime-config-bar__pill") &&
    includes(stylesSource, "flex: 0 1 auto;"),
  "CSS: runtime pills shrink inside the footer",
);

// CSS: composer-card max-width is constrained in workbench
const composerMaxW = finalDeclaration(stylesSource, ".layout--workbench .session-footer-dock .composer-card", "max-width");
ok(composerMaxW === "100%", `CSS: workbench composer-card max-width is 100% (got: ${composerMaxW})`);

// CSS: conversation-viewport padding is compact
const viewportPad = finalDeclaration(stylesSource, ".conversation-viewport", "padding");
ok(viewportPad === "48px 48px 24px 48px", `CSS: conversation-viewport padding is compact (got: ${viewportPad})`);

// Windows frameless chrome: one compact utility row and a real drag surface.
ok(
  finalDeclaration(stylesSource, ".session-header", "--wails-draggable") === "drag",
  "CSS: session header is a Wails window drag surface",
);
ok(
  finalDeclaration(stylesSource, ".session-header__actions", "--wails-draggable") === "no-drag" &&
    finalDeclaration(stylesSource, ".session-header__addon-btn", "--wails-draggable") === "no-drag" &&
    finalDeclaration(stylesSource, ".session-header__more-btn", "--wails-draggable") === "no-drag",
  "CSS: interactive header controls opt out of window dragging",
);
ok(
  finalDeclaration(stylesSource, ".work-page__header", "--wails-draggable") === "drag" &&
    finalDeclaration(stylesSource, ".work-page__back-btn", "--wails-draggable") === "no-drag" &&
    finalDeclaration(stylesSource, ".work-page__new-btn", "--wails-draggable") === "no-drag",
  "CSS: Work page keeps a draggable header with clickable controls",
);
ok(
  finalDeclaration(stylesSource, ".wg2-work-outer-header", "--wails-draggable") === "drag" &&
    finalDeclaration(stylesSource, ".wg2-work-outer-header-right", "--wails-draggable") === "no-drag" &&
    finalDeclaration(stylesSource, ".wg2-work-cornerstone-entry", "--wails-draggable") === "no-drag" &&
    finalDeclaration(stylesSource, ".wg2-work-addon-area", "--wails-draggable") === "no-drag",
  "CSS: Work detail header is draggable while its actions stay interactive",
);
ok(
  finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench .session-header__actions", "top") === "4px" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench .session-header__actions", "right") ===
      "calc(var(--windows-window-controls-width) + 3px)" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench .session-header__actions", "border-right") === "0",
  "CSS: workbench utilities form the left half of the unified top-right rail",
);
ok(
  finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench .windows-window-controls", "top") === "15px" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench .windows-window-controls", "right") === "14px" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench .windows-window-controls", "border-left") === "0" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench .windows-window-controls", "border-radius") === "0 7px 7px 0",
  "CSS: native window controls complete the same inset rail without a detached window block",
);
ok(
  finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-session .session-header__actions::before", "inset") ===
      "0 calc(0px - var(--windows-window-controls-width)) 0 -3px" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-session .session-header__actions::before", "border-radius") === "7px" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-session .session-header__actions", "border") === "0" &&
    includes(stylesSource, ".app--windows-frameless.app--workbench-session .windows-window-controls,") &&
    includes(stylesSource, "border-left: 1px solid var(--border-soft);"),
  "CSS: Session chrome uses one shared outer board with only an internal divider",
);
ok(
  includes(workWorkspaceSource, 'className="wg2-work-top-actions"') &&
    includes(stylesSource, ".app--windows-frameless.app--workbench-work .wg2-work-top-actions,") &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-work .wg2-work-top-actions", "top") === "4px" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-work .wg2-work-top-actions", "right") ===
      "calc(4px + var(--windows-window-controls-width))" &&
    includes(stylesSource, ".app--windows-frameless.app--workbench-work .wg2-work-top-actions::before,") &&
    includes(stylesSource, "inset: 0 calc(0px - var(--windows-window-controls-width)) 0 -3px;"),
  "CSS: Work actions stay joined and align to the real window top-right corner",
);
ok(
  finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-work .windows-window-controls", "top") === "4px" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-work .windows-window-controls", "right") === "4px",
  "CSS: Work caption controls share the same top-right inset as its action rail",
);
ok(
  finalDeclaration(stylesSource, ".app--workbench .work-session-host .wg2-work-outer-header", "backdrop-filter") === "none" &&
    finalDeclaration(stylesSource, ".app--workbench .work-session-host .wg2-work-outer-header::before", "backdrop-filter") === "blur(10px)" &&
    finalDeclaration(stylesSource, ".app--workbench .work-session-host .wg2-work-outer-header::before", "pointer-events") === "none",
  "CSS: Work header blur paints on a child layer and cannot rebase fixed caption actions",
);
ok(
  finalDeclaration(stylesSource, ".app--workbench .work-session-host", "background") === "transparent" &&
    finalDeclaration(stylesSource, ".app--workbench .work-session-host", "backdrop-filter") === "none" &&
    finalDeclaration(stylesSource, ".app--workbench .work-session-host::before", "background") ===
      "color-mix(in srgb, var(--bg) 58%, transparent)" &&
    finalDeclaration(stylesSource, ".app--workbench .work-session-host::before", "backdrop-filter") ===
      "blur(6px) saturate(0.94)" &&
    finalDeclaration(stylesSource, ".app--workbench .work-session-host .wg2-work-card", "background") ===
      "color-mix(in srgb, var(--bg) 42%, transparent)" &&
    finalDeclaration(stylesSource, ".app--workbench .work-session-host .wg2-work-card-front--presentation", "background") ===
      "transparent",
  "CSS: Work uses a translucent stacked plate instead of covering the wallpaper",
);
ok(
  includes(stylesSource, "--wg2-accent: var(--accent);") &&
    includes(stylesSource, "--wg2-surface: color-mix(in srgb, var(--bg) 84%, transparent);") &&
    !includes(stylesSource, "--accent: #4ad6c4;") &&
    !includes(stylesSource, "--accent: #2cc8b7;"),
  "CSS: Work semantic colors inherit the active app theme without a fixed green accent",
);
ok(
  includes(collaborationSource, 'className="collab-topic-actions"') &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-room .collab-topic-actions", "position") === "fixed" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-room .collab-topic-actions", "right") ===
      "calc(14px + var(--windows-window-controls-width))" &&
    finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench-room .collab-topic-actions::before", "inset") ===
      "0 calc(0px - var(--windows-window-controls-width)) 0 -3px",
  "CSS: Room actions stay joined to caption controls independently of its member grid",
);
ok(
  includes(collaborationSource, '<section className="collab-surface"') &&
    includes(stylesSource, ".app--workbench .collab-surface {") &&
    finalDeclaration(stylesSource, ".app--workbench .collab-surface", "border-radius") === "8px" &&
    !includes(stylesSource, ".app--workbench .collaboration-workspace"),
  "CSS: Room applies the same rounded outer plate to its real root element",
);
ok(
  includes(stylesSource, ".app--windows-frameless.app--workbench-work .windows-window-controls,") &&
    includes(stylesSource, ".app--windows-frameless.app--workbench-room .windows-window-controls") &&
    includes(stylesSource, "border-left: 1px solid var(--border-soft);"),
  "CSS: all three rails share the same caption-side internal divider",
);

// ── Workbench right-side layout contract ─────────────────────────────────

// Single scroll owner: transcript should NOT scroll inside conversation-viewport
const transcriptOverflow = finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript", "overflow");
ok(transcriptOverflow === "visible", `CSS: transcript inside conversation-viewport has overflow:visible (got: ${transcriptOverflow})`);

const transcriptHeight = finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript", "height");
ok(transcriptHeight === "auto", `CSS: transcript inside conversation-viewport has height:auto (got: ${transcriptHeight})`);

const transcriptPad = finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript", "padding");
ok(transcriptPad === "0", `CSS: transcript relies on the viewport 48px spine (got padding: ${transcriptPad})`);

// 48px spine: transcript content children must not center via max-width
const tcMaxW = finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript__content > *", "max-width");
ok(tcMaxW === "none", `CSS: transcript content children max-width is none (got: ${tcMaxW})`);

const tcMarginL = finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript__content > *", "margin-left");
ok(tcMarginL === "0", `CSS: transcript content children margin-left is 0 (got: ${tcMarginL})`);
ok(
  includes(stylesSource, ".layout--workbench .conversation-viewport .warm-turn__body > *") &&
    includes(stylesSource, ".layout--workbench .conversation-viewport .msg--assistant"),
  "CSS: expanded turns and assistant text stay on the 48px spine",
);
ok(
  includes(stylesSource, ".layout--workbench .conversation-viewport .turn-collapse__body > *") &&
    includes(stylesSource, ".layout--workbench .conversation-viewport .readonly-batch__body > *"),
  "CSS: nested run blocks cannot re-center their contents",
);

// ── Regression: every .msg container uses the 48px spine, not auto ──
const msgMarginL = finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .msg", "margin-left");
ok(msgMarginL === "0", `CSS: workbench .msg margin-left is 0 (got: ${msgMarginL})`);
const msgMarginR = finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .msg", "margin-right");
ok(msgMarginR === "0", `CSS: workbench .msg margin-right is 0 (got: ${msgMarginR})`);

// composer-wrap fills 48px spine
const cwMaxW = finalDeclaration(stylesSource, ".layout--workbench .session-footer-dock .composer-wrap", "max-width");
ok(cwMaxW === "none", `CSS: composer-wrap max-width is none (got: ${cwMaxW})`);
const cwWidth = finalDeclaration(stylesSource, ".layout--workbench .session-footer-dock .composer-wrap", "width");
ok(cwWidth === "auto", `CSS: composer-wrap respects shared 48px margins (got width: ${cwWidth})`);

const composerMinH = finalDeclaration(stylesSource, ".layout--workbench .session-footer-dock .composer-card", "min-height");
ok(composerMinH === "128px", `CSS: composer editor frame is 128px (got: ${composerMinH})`);

// 32px bottom breathing room
const dockPadB = finalDeclaration(stylesSource, ".layout--workbench .session-footer-dock", "padding-bottom");
ok(dockPadB === "32px", `CSS: session-footer-dock padding-bottom is 32px (got: ${dockPadB})`);

// SessionRunStream fills 48px spine
const streamW = finalDeclaration(stylesSource, ".layout--workbench .session-run-stream", "width");
ok(streamW === "100%", `CSS: session-run-stream width is 100% (got: ${streamW})`);
const streamMarginTop = finalDeclaration(stylesSource, ".layout--workbench .session-run-stream", "margin-top");
ok(streamMarginTop === "12px", `CSS: run stream has 12px separation from preceding content (got: ${streamMarginTop})`);

// Artifact-shelf removes redundant border in workbench
const shelfBorderB = finalDeclaration(stylesSource, ".layout--workbench .session-footer-dock .artifact-shelf", "border-bottom");
ok(shelfBorderB === "none", `CSS: artifact-shelf border-bottom is none in workbench (got: ${shelfBorderB})`);

// ── Regression: TaskMemoryBar matches session workspace background ─────

const memBg = finalDeclaration(stylesSource, ".layout--workbench .task-memory-bar", "background");
ok(memBg === "var(--bg)", `CSS: workbench TaskMemoryBar background is var(--bg) not var(--surface) (got: ${memBg})`);

const memHeight = finalDeclaration(stylesSource, ".layout--workbench .task-memory-bar", "height");
ok(memHeight === "40px", `CSS: real task memory is a compact 40px line (got: ${memHeight})`);

const memBorderT = finalDeclaration(stylesSource, ".layout--workbench .task-memory-bar", "border-top");
ok(memBorderT === "1px solid var(--border)", `CSS: workbench TaskMemoryBar has top hairline (got: ${memBorderT})`);

// task-memory-bar base still has bottom border (workbench keeps it)
const memBorderB = finalDeclaration(stylesSource, ".task-memory-bar", "border-bottom");
ok(memBorderB === "1px solid var(--border)", `CSS: base task-memory-bar has bottom hairline (got: ${memBorderB})`);

const memSlotFlex = finalDeclaration(stylesSource, ".task-memory-bar__segment-slot", "flex");
ok(memSlotFlex === "1 1 0", `CSS: memory segments share available width (got: ${memSlotFlex})`);

const noCurrentGoalGrow = finalDeclaration(stylesSource, ".task-memory-bar--no-current .task-memory-bar__segment-slot--goal", "flex-grow");
ok(noCurrentGoalGrow === "2", `CSS: goal expands when current is absent (got: ${noCurrentGoalGrow})`);

// ── Regression: shared width contract for composer-wrap + runtime-config-bar ──

// Verify both share one rule (same property source)
ok(
  includes(stylesSource, ".session-footer-dock > .artifact-shelf,\n.session-footer-dock > .queue-tray,\n.session-footer-dock > .composer-wrap,\n.session-footer-dock > .runtime-config-bar"),
  "CSS: composer-wrap and runtime-config-bar share one explicit rule with artifact-shelf and queue-tray",
);

// The shared rule must have width: auto; max-width: none; for the border-box width contract.
// Use includes because finalDeclaration cannot extract from comma-separated selectors.
const sharedBlock = stylesSource.slice(
  stylesSource.indexOf(".session-footer-dock > .artifact-shelf,"),
  stylesSource.indexOf("}", stylesSource.indexOf(".session-footer-dock > .artifact-shelf,")) + 1,
);
ok(sharedBlock.includes("width: auto;"), "CSS: shared rule has width: auto for border-box contract");
ok(sharedBlock.includes("max-width: none;"), "CSS: shared rule has max-width: none for border-box contract");

ok(
  includes(stylesSource, ".layout--workbench .session-footer-dock > .composer-wrap,\n.layout--workbench .session-footer-dock > .runtime-config-bar {") &&
    includes(stylesSource, "box-sizing: border-box;"),
  "CSS: high-specificity workbench rule defeats themed composer auto margins",
);

// ComposerWrap already has width:auto at the workbench level (standalone rule)
const cwWidthWorkbench = finalDeclaration(stylesSource, ".layout--workbench .session-footer-dock .composer-wrap", "width");
ok(cwWidthWorkbench === "auto", `CSS: workbench composer-wrap width is auto (got: ${cwWidthWorkbench})`);

// The runtime-config-bar workbench override must NOT set width itself (inherits from shared rule)
const rcbWidthOverride = finalDeclaration(stylesSource, ".layout--workbench .session-footer-dock .runtime-config-bar", "width");
ok(rcbWidthOverride == null, `CSS: runtime-config-bar workbench override does NOT set separate width (got: ${rcbWidthOverride})`);

ok(
  includes(runtimeConfigSource, "<ModelSwitcher") && includes(runtimeConfigSource, "onPick={onSwitchModel}"),
  "RuntimeConfigBar: visible model control reuses the real model picker",
);
ok(
  includes(runtimeConfigSource, 'label={`${config.contextPercent}%`}') &&
    includes(runtimeConfigSource, 'label={`审批：${approvalLabel(config.approvalMode)}`}'),
  "RuntimeConfigBar: context and approval labels use compact meaningful copy",
);
ok(
  includes(runtimeConfigSource, "onSetApprovalMode(next)") && includes(appSource, "onSetApprovalMode={applyToolApprovalMode}"),
  "RuntimeConfigBar: approval control updates the real session mode",
);

ok(
  finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript-shell", "display") === "grid" &&
    finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript-shell > .jump-bar", "position") === "sticky",
  "CSS: workbench question rail is overlaid and sticky to the conversation viewport",
);
ok(
  finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript-shell", "height") === "auto",
  `CSS: workbench transcript-shell height is auto (got: ${finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript-shell", "height")})`,
);
ok(
  finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript-shell", "min-height") === "100%",
  `CSS: workbench transcript-shell fills the available viewport without :has() (got: ${finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript-shell", "min-height")})`,
);
ok(
  finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript-shell > .jump-bar", "top") === "calc(50% - 120px)" &&
    finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport .transcript-shell > .jump-bar", "justify-self") === "end",
  "CSS: workbench question rail stays vertically centered at the viewport edge",
);
ok(
  includes(stylesSource, ".transcript-shell > .jump-bar {\n  position: absolute;"),
  "CSS: classic transcript keeps its existing absolute question rail",
);
ok(
  finalDeclaration(stylesSource, ".session-run-stream--terminal", "display") === "flex" &&
    finalDeclaration(stylesSource, ".session-run-stream--terminal", "flex-direction") === "column",
  "CSS: unassigned terminal runs retain a full-width container",
);
ok(
  finalDeclaration(stylesSource, ".session-run-actions", "width") === "100%" &&
    finalDeclaration(stylesSource, ".session-run-action__panel", "flex") === "1 0 100%",
  "CSS: completed runs collapse into the action row and expand below it",
);
ok(
  /\.run-work-window\s*\{[^}]*height:\s*220px;[^}]*min-height:\s*220px;/s.test(stylesSource),
  "CSS: active and expanded process views use the same fixed-size window",
);
ok(
  finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport:has(.session-run-stream) .turn-collapse", "display") === "none",
  "CSS: real run stream replaces legacy execution folds in Workbench",
);
ok(
  finalDeclaration(stylesSource, ".layout--workbench .conversation-viewport:has(.session-run-action__toggle) .turn-collapse", "display") === "none",
  "CSS: compact run action also replaces legacy execution folds in Workbench",
);
ok(
  finalDeclaration(stylesSource, ".active-run-view__tabs", "overflow-x") === "auto" &&
    finalDeclaration(stylesSource, ".active-run-view__tabs", "touch-action") === "pan-x",
  "CSS: active run steps expose a horizontal drag/scroll rail",
);
ok(
  finalDeclaration(stylesSource, ".run-step-tab", "flex") === "0 0 auto",
  "CSS: step tabs retain their clickable width instead of shrinking offscreen",
);

// ── New-session entry: lightweight tool entry, not a heavy button ──────────

// The entry must NOT carry a solid border that reads as a button outline.
const newSessionBorder = finalDeclaration(stylesSource, ".workspace-sidebar__new-session", "border");
ok(
  newSessionBorder !== "1px solid var(--border)",
  `CSS: new-session entry is NOT a heavy bordered button (got border: ${newSessionBorder})`,
);

ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__new-session", "background") === "transparent",
  "CSS: new-session entry has transparent background by default",
);

// Icon is restrained and gets accent on hover.
ok(
  includes(stylesSource, ".workspace-sidebar__new-session svg") &&
    includes(stylesSource, ".workspace-sidebar__new-session:hover svg"),
  "CSS: new-session icon has dedicated style and hover accent",
);

// focus-visible ring uses existing token.
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__new-session:focus-visible", "box-shadow") === "var(--focus-ring)",
  "CSS: new-session focus-visible uses --focus-ring token",
);

// ── Bottom settings entry: stable native click target ───────────────────

ok(
  includes(appSource, 'className="workspace-sidebar__settings"') &&
    includes(appSource, "onClick={openGeneralSettings}") &&
    includes(appSource, 'setSettingsTarget("general")'),
  "App.tsx: Session List bottom settings entry uses the shared general-settings opener",
);
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__settings", "--wails-draggable") === "no-drag" &&
    finalDeclaration(stylesSource, ".workspace-sidebar__settings", "pointer-events") === "auto" &&
    finalDeclaration(stylesSource, ".workspace-sidebar__settings", "touch-action") === "manipulation",
  "CSS: Session List bottom settings entry remains clickable inside the native window",
);
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__settings", "flex") === "0 0 36px" &&
    finalDeclaration(stylesSource, ".app--workbench .workspace-sidebar__settings", "flex-basis") === "38px",
  "CSS: Session List bottom settings entry cannot collapse behind the scrolling tree",
);
ok(
  finalDeclaration(stylesSource, ".app--windows-frameless.app--workbench .workspace-sidebar__settings", "margin-bottom") === "20px",
  "CSS: Windows Session List settings stays above the native bottom resize hit zone",
);

// ── Scrollbar auto-hide contract ──────────────────────────────────────────

// App.tsx wires the --scrolling class.
ok(
  includes(appSource, 'el.addEventListener("scroll", onScroll, { passive: true })') &&
    includes(appSource, 'el.classList.add("workspace-sidebar__tree--scrolling")') &&
    includes(appSource, 'el.classList.remove("workspace-sidebar__tree--scrolling")') &&
    includes(appSource, "}, 700);") &&
    includes(appSource, "}, [desktopLayoutStyle]);"),
  "App.tsx: passive scroll listener reveals the thumb, idles for 700ms, and rebinds with the layout",
);

// CSS defines default invisible thumb.
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__tree::-webkit-scrollbar-thumb", "background") === "transparent",
  "CSS: default scrollbar thumb is transparent (invisible)",
);
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__tree::-webkit-scrollbar-button", "display") === "none",
  "CSS: native scrollbar arrow buttons stay hidden",
);

// --scrolling modifier reveals the thumb.
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__tree--scrolling::-webkit-scrollbar-thumb", "background") === "var(--scrollbar-thumb)",
  "CSS: --scrolling modifier reveals the scrollbar thumb via --scrollbar-thumb",
);

// Firefox scrollbar-color: transparent by default, visible while scrolling.
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__tree", "scrollbar-color") === "transparent transparent",
  "CSS: Firefox scrollbar-color defaults to transparent",
);
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__tree--scrolling", "scrollbar-color") === "var(--scrollbar-thumb) transparent",
  "CSS: Firefox --scrolling modifier reveals scrollbar thumb",
);
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar .workspace-sidebar__tree", "scrollbar-width") === "thin",
  "CSS: session tree restores Firefox thin scrollbar after the broad sidebar rule",
);

// ── Sidebar collapse ──────────────────────────────────────────────────────

// CSS: collapsed sidebar is hidden
ok(
  finalDeclaration(stylesSource, ".layout--workbench.layout--sidebar-collapsed .workspace-sidebar", "display") === "none",
  "CSS: collapsed workbench sidebar is hidden",
);

// CSS: collapsed workbench becomes a real single-column board
const collapsedGridTpl = finalDeclaration(stylesSource, ".app--workbench .layout--workbench.layout--sidebar-collapsed", "grid-template-columns");
const collapsedGap = finalDeclaration(stylesSource, ".app--workbench .layout--workbench.layout--sidebar-collapsed", "gap");
ok(
  collapsedGridTpl === "minmax(0, 1fr)" && collapsedGap === "0",
  `CSS: collapsed workbench is one column with no orphan gap (grid=${collapsedGridTpl}, gap=${collapsedGap})`,
);

// CSS: collapsed session-workspace occupies that single column
const collapsedGridCol = finalDeclaration(stylesSource, ".app--workbench .layout--workbench.layout--sidebar-collapsed .session-workspace", "grid-column");
ok(collapsedGridCol === "1", `CSS: collapsed session-workspace occupies column 1 (got: ${collapsedGridCol})`);

// App.tsx: collapse button exists in workspace-sidebar brand
ok(
  includes(appSource, 'className={`workspace-sidebar__collapse-btn${sidebarTogglePressed ? " workspace-sidebar__collapse-btn--pressed" : ""}`}') &&
    includes(appSource, 'aria-label={sidebarToggleTitle}') &&
    includes(appSource, 'aria-pressed={!sidebarCollapsed}'),
  "App.tsx: workspace-sidebar brand has PanelLeft collapse button with correct aria",
);

// App.tsx: expand button in session-header when collapsed
ok(
  includes(appSource, 'className={`session-header__expand-btn${sidebarTogglePressed ? " session-header__expand-btn--pressed" : ""}`}') &&
    includes(appSource, 'className="session-header__identity"') &&
    includes(appSource, "{sidebarCollapsed && (") &&
    includes(appSource, '<PanelRight size={15} aria-hidden="true" />'),
  "App.tsx: session-header shows PanelRight expand button when sidebar is collapsed",
);

ok(
  finalDeclaration(stylesSource, ".session-header__identity", "min-width") === "0" &&
    finalDeclaration(stylesSource, ".session-header__identity", "flex") === "1 1 auto",
  "CSS: session header identity keeps the expand button and ellipsized title in one flexible group",
);

// CSS: collapse button is transparent with hover
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__collapse-btn", "background") === "transparent",
  "CSS: collapse button has transparent background by default",
);
ok(
  finalDeclaration(stylesSource, ".workspace-sidebar__collapse-btn", "--wails-draggable") === "no-drag",
  "CSS: collapse button opts out of window dragging",
);

// CSS: expand button in session header
ok(
  finalDeclaration(stylesSource, ".session-header__expand-btn", "margin-right") === "12px",
  "CSS: expand button has right margin before title",
);
ok(
  finalDeclaration(stylesSource, ".session-header__expand-btn", "--wails-draggable") === "no-drag",
  "CSS: expand button opts out of window dragging",
);

// ── 820px responsive workspace-sidebar ──────────────────────────────────────

// At narrow widths, expanded workspace-sidebar floats as overlay
const respFloatDisp = finalDeclaration(stylesSource, ".app .layout.layout--workbench:not(.layout--sidebar-collapsed) .workspace-sidebar", "display");
ok(respFloatDisp === "flex", `CSS (820px): expanded workspace-sidebar display is flex (got: ${respFloatDisp})`);
const respFloatPos = finalDeclaration(stylesSource, ".app .layout.layout--workbench:not(.layout--sidebar-collapsed) .workspace-sidebar", "position");
ok(respFloatPos === "absolute", `CSS (820px): expanded workspace-sidebar floats as overlay (got: ${respFloatPos})`);
const respFloatZ = finalDeclaration(stylesSource, ".app .layout.layout--workbench:not(.layout--sidebar-collapsed) .workspace-sidebar", "z-index");
ok(respFloatZ === "var(--z-workspace-float)", `CSS (820px): floating workspace-sidebar uses --z-workspace-float (got: ${respFloatZ})`);

// At narrow widths, collapsed workspace-sidebar is hidden
const respCollDisp = finalDeclaration(stylesSource, ".app .layout.layout--workbench.layout--sidebar-collapsed .workspace-sidebar", "display");
ok(respCollDisp === "none", `CSS (820px): collapsed workspace-sidebar is hidden (got: ${respCollDisp})`);

// At narrow widths, session-workspace always fills the grid
const respSessGrid = finalDeclaration(stylesSource, ".app .layout.layout--workbench .session-workspace", "grid-column");
ok(respSessGrid === "1 / -1", `CSS (820px): session-workspace always fills grid (got: ${respSessGrid})`);

// Themed variant also covered for floating workspace-sidebar
const themedFloatDisp = finalDeclaration(stylesSource, ":root[data-theme-style] .app .layout.layout--workbench:not(.layout--sidebar-collapsed) .workspace-sidebar", "display");
ok(themedFloatDisp === "flex", `CSS (820px themed): expanded workspace-sidebar display is flex (got: ${themedFloatDisp})`);

// ── Done ─────────────────────────────────────────────────────────────────────

const total = passed + failed;
process.stdout.write(`\n${total} tests · ${passed} passed · ${failed} failed\n`);
if (failed > 0) process.exit(1);
