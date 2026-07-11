// Run: tsx src/__tests__/theme-iris.test.tsx
import { JSDOM } from "jsdom";
import {
  THEME_STYLES,
  isThemeStyle,
  normalizeThemeStyleForTheme,
  DEFAULT_THEME_STYLE,
  applyTheme,
  type Theme,
  type ThemeStyle,
} from "../lib/theme";
import * as fs from "fs";
import * as path from "path";

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

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) {
    ok(true, label);
  } else {
    ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

function includes(haystack: string, needle: string, label: string) {
  if (haystack.includes(needle)) {
    ok(true, label);
  } else {
    ok(false, `${label}: "${needle}" not found in CSS`);
  }
}

function excludes(haystack: string, needle: string, label: string) {
  if (haystack.includes(needle)) {
    ok(false, `${label}: "${needle}" still present (should be dead)`);
  } else {
    ok(true, label);
  }
}

// ── Load styles.css for selector audit ─────────────────────────

let cssText = "";
try {
  cssText = fs.readFileSync(path.resolve(__dirname, "../styles.css"), "utf8");
} catch {
  // fallback: cwd-relative for tsx runner
  cssText = fs.readFileSync(path.resolve(process.cwd(), "src/styles.css"), "utf8");
}

// ── Iris is a valid theme style ────────────────────────────────

console.log("\ntheme-iris: Iris is a registered style");
ok(THEME_STYLES.includes("iris"), "iris is in THEME_STYLES");
ok(isThemeStyle("iris"), "isThemeStyle('iris') returns true");
eq(normalizeThemeStyleForTheme("iris"), "iris", "normalizeThemeStyleForTheme('iris') === 'iris'");

// ── Iris is default ────────────────────────────────────────────

console.log("\ntheme-iris: Iris is the frontend default");
eq(DEFAULT_THEME_STYLE, "iris", "DEFAULT_THEME_STYLE === 'iris'");

// ── Existing styles still work ─────────────────────────────────

console.log("\ntheme-iris: Existing styles and legacy aliases still resolve");
const ALL_LEGACY = ["ember", "midnight", "sandstone", "porcelain", "linen", "glacier"];
for (const legacy of ALL_LEGACY) {
  ok(isThemeStyle(normalizeThemeStyleForTheme(legacy)), `legacy '${legacy}' resolves to a valid style`);
  ok(normalizeThemeStyleForTheme(legacy) !== "", `legacy '${legacy}' is not empty`);
}
ok(isThemeStyle("graphite"), "graphite still valid");
ok(isThemeStyle("aurora"), "aurora still valid");
ok(isThemeStyle("slate"), "slate still valid");
ok(isThemeStyle("carbon"), "carbon still valid");
ok(isThemeStyle("nocturne"), "nocturne still valid");
ok(isThemeStyle("amber"), "amber still valid");

// ── applyTheme sets data-theme-style="iris" ────────────────────

console.log("\ntheme-iris: applyTheme sets correct DOM attribute");
const dom = new JSDOM("<!DOCTYPE html><html><body></body></html>", { url: "http://localhost" });
(global as unknown as Record<string, unknown>).document = dom.window.document;
(global as unknown as Record<string, unknown>).window = dom.window;

applyTheme("dark", "iris");
const root = dom.window.document.documentElement;
eq(root.getAttribute("data-theme"), "dark", "data-theme is dark");
eq(root.getAttribute("data-theme-style"), "iris", "data-theme-style is iris");

// ── applyTheme with default (no style arg) uses DEFAULT_THEME_STYLE ──

applyTheme("light");
eq(root.getAttribute("data-theme"), "light", "data-theme is light with default style");
eq(root.getAttribute("data-theme-style"), "iris", "data-theme-style is iris by default");

// ── Unknown style falls back to default ─────────────────────────

applyTheme("auto", "nonexistent" as ThemeStyle);
eq(root.getAttribute("data-theme-style"), "iris", "unrecognized style falls back to default");

// ── Live selectors: classes the components actually emit ───────

console.log("\ntheme-iris: Real component selectors are present in CSS");

// RunBlock
includes(cssText, ".completed-run-tab--queued", "completed-run-tab mod queued");
includes(cssText, ".completed-run-tab--running", "completed-run-tab mod running");
includes(cssText, ".completed-run-tab--waiting_user", "completed-run-tab mod waiting_user");
includes(cssText, ".active-run-view--failed", "active-run-view mod failed");
includes(cssText, ".run-step-tab--completed", "run-step-tab mod completed");
includes(cssText, ".completed-run-tab:focus-visible", "completed-run-tab focus ring");

// ArtifactShelf
includes(cssText, ".artifact-item--stale", "artifact-item mod stale");
includes(cssText, ".artifact-item--missing", "artifact-item mod missing");
includes(cssText, ".artifact-item--failed", "artifact-item mod failed");
includes(cssText, ".artifact-item--generating", "artifact-item mod generating");
includes(cssText, ".artifact-item--available", "artifact-item mod available");
includes(cssText, ".artifact-item--actionable", "artifact-item mod actionable");
includes(cssText, ".artifact-item__primary", "artifact-item__primary selector");
includes(cssText, ".artifact-item__primary:focus-visible", "artifact-item__primary focus ring");
includes(cssText, ".artifact-item__status-badge", "artifact-item__status-badge present");

// AddOnWorkbench
includes(cssText, ".addon-instance--tab", "addon-instance mod tab");
includes(cssText, ".addon-instance--peek", "addon-instance mod peek");
includes(cssText, ".addon-instance--focus", "addon-instance mod focus");
includes(cssText, ".instance-header__status-icon--error", "instance-header status-icon mods");
includes(cssText, ".instance-header__status-text--warning", "instance-header status-text mods");
includes(cssText, ".instance-body__message--error", "instance-body message mods");

// RuntimeConfigBar
includes(cssText, ".runtime-config-bar__primary-action--idle", "primary-action mod idle");
includes(cssText, ".runtime-config-bar__primary-action--running", "primary-action mod running");
includes(cssText, ".runtime-config-bar__primary-action--waiting_user", "primary-action mod waiting_user");
includes(cssText, ".runtime-config-bar__primary-action--offline", "primary-action mod offline");

// Buttons
includes(cssText, ".icon-button--active", "icon-button mod active");
includes(cssText, ".icon-button:focus-visible", "icon-button focus ring");
includes(cssText, ".action-button:focus-visible", "action-button focus ring");

// ── Dead selectors: absent from the desktop‑UI section only ────

console.log("\ntheme-iris: Dead selectors absent from desktop‑UI section");
const duiStart = cssText.indexOf("Desktop&#8209;UI primitives");
const duiEnd = cssText.indexOf("&#8209; Reduced motion", duiStart);
const duiSection = cssText.slice(duiStart, duiEnd > duiStart ? duiEnd : duiStart + 30000);

excludes(duiSection, ".addon-instance-view", "addon-instance-view selector dead");
excludes(duiSection, "data-density", "[data-density] attribute selector dead — use --tab/--peek/--focus mods");
excludes(duiSection, "[data-status=", "data-status attr selector dead — use class mods");
excludes(duiSection, "mask-image", "mask-image dead in desktop‑UI section (use fixed height/overflow)");

// ── No decorative gradients in Iris theme variables block ──────

console.log("\ntheme-iris: Iris theme variables have no decorative gradients");
const irisVarStart = cssText.indexOf(":root[data-theme-style=\"iris\"]");
const irisVarEnd = cssText.indexOf("\n}\n\n", irisVarStart + 10);
const irisVarBlock = cssText.slice(irisVarStart, irisVarEnd > irisVarStart ? irisVarEnd + 4 : irisVarStart + 3000);

// The --grad:none declaration is present
includes(irisVarBlock, "--grad: none", "Iris sets --grad: none");

// No linear-gradient in Iris CSS custom property values
const lineGradMatch = irisVarBlock.match(/linear-gradient/g);
if (!lineGradMatch) {
  ok(true, "no linear-gradient in Iris root variables block");
} else {
  ok(false, `Iris root variables block contains ${lineGradMatch.length} linear-gradient(s)`);
}

// ── Layout dimensions match spec ───────────────────────────────

console.log("\ntheme-iris: Layout dimensions match spec");

includes(cssText, "height: 64px;", "TaskMemoryBar 64px");
includes(cssText, "height: 40px;", "CompletedRunTab 40px");
includes(cssText, "height: 160px;", "ActiveRunView 160px");
includes(cssText, ".turn-collapse--active", "live Transcript run uses active collapse contract");
includes(cssText, "max-height: 160px;", "live Transcript run is height-limited");
includes(cssText, "padding: 0 48px", "TaskMemoryBar left 48px");
includes(cssText, "height: 32px;", "artifact-item 32px");
includes(cssText, "height: 48px;", "RuntimeConfigBar 48px");
includes(cssText, "height: 56px;", "WorkbenchHeader 56px");
includes(cssText, "height: 44px;", "InstanceHeader 44px");
includes(cssText, "height: 36px;", "addon-instance tab 36px");
includes(cssText, "max-height: 118px;", "addon-instance peek 118px");
includes(cssText, "width: 392px;", "AddOnWorkbench 392px");
includes(cssText, "top: 84px;", "AddOnWorkbench top 84px");
includes(cssText, "right: 32px;", "AddOnWorkbench right 32px");
includes(cssText, "gap: 8px;", "addon-stack gap 8px");
includes(cssText, "min-width: 32px;", "primary-action min 32px");
includes(cssText, "max-height: calc(100vh - 116px);", "AddOn max-height preserves the visible workbench stack");
includes(cssText, "z-index: var(--z-addon-surface)", "AddOn uses --z-addon-surface");

// ── Responsive ────────────────────────────────────────────────

console.log("\ntheme-iris: Narrow breakpoint styles");
const narrowSection = cssText.slice(cssText.indexOf("@media (max-width: 879px)"));
includes(narrowSection, "padding: 0 16px !important", "TaskMemoryBar 16px at narrow");
includes(narrowSection, "padding-left: 16px !important", "session-workspace left 16px at narrow");

// ── Reduced motion ─────────────────────────────────────────────

console.log("\ntheme-iris: Reduced motion present");
includes(cssText, "prefers-reduced-motion: reduce", "reduced motion media query");
includes(cssText, "animation-duration: 0s !important", "reduced motion zeroes duration");
includes(cssText, ".animate-spin {", "animate-spin override target");

// ── Summary ────────────────────────────────────────────────────

const total = passed + failed;
console.log(`\n${total} tests: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
