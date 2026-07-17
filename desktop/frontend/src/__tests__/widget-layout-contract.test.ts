// Regression tests for widget layout & text contracts.
// Models the actual 200% shell / scale(0.5) geometry and validates that
// clock sizing, button text, and EN idle strings stay within bounds at the
// 520 px native minimum width.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

// ---- Locale text-length contracts ----

import { en as enT } from "../locales/en.ts";

// Idle state: noTasks must be short enough for the ticker at min width.
const noTasksLen = [...enT["widget.noTasks"]].length;
assert.ok(noTasksLen <= 40, `widget.noTasks too long (${noTasksLen}): "${enT["widget.noTasks"]}"`);

// Button strong text must fit inside the 216 px .widget-new button
// at 32px Segoe UI bold (~0.55 em avg char width + 0.06 em letter-spacing).
const convLen = [...enT["widget.newConversation"]].length;
assert.ok(convLen <= 10, `widget.newConversation too long (${convLen}): "${enT["widget.newConversation"]}"`);

const enterLen = [...enT["widget.enterTask"]].length;
assert.ok(enterLen <= 14, `widget.enterTask too long (${enterLen}): "${enT["widget.enterTask"]}"`);

// Verify the actual values match expected (catches accidental edits).
assert.equal(enT["widget.noTasks"], "No active tasks");
assert.equal(enT["widget.newConversation"], "New task");
assert.equal(enT["widget.enterTask"], "Enter task");

// ---- 200% shell / scale(0.5) geometry model ----
//
// .widget-shell is width:200%; height:200%; transform:scale(0.5); left top.
// The .widget-info panel is 25.2% wide, containing a 38px rail + a clock page
// with 12px padding on each side.
//
//   shellWidth  = 2 * viewportWidth
//   panel       = 0.252 * shellWidth = 0.504 * viewportWidth
//   usableText  = panel − rail(38px) − page padding(12px+12px)
//               = 0.504 * viewportWidth − 62

function usableTextWidth(vw: number): number {
  return 0.504 * vw - 62;
}

// Doto's bundled font gives both digits and ':' a 0.6em advance. Include a
// conservative eight letter-spacing advances so the contract never undercounts.
function clockTextWidth(fontSize: number): number {
  return fontSize * (8 * 0.6 + 8 * 0.02);
}

// At native 520px min-width:
const minVw = 520;
const minUsable = usableTextWidth(minVw);
assert.ok(minUsable > 0, `usable text width at ${minVw}px must be positive, got ${Math.round(minUsable)}`);

const expectedClamp = { min: 38, preferred: 7.4, max: 56 };
const clockAtMin = Math.min(
  expectedClamp.max,
  Math.max(expectedClamp.min, minVw * expectedClamp.preferred / 100),
);
const clockW = clockTextWidth(clockAtMin);
assert.ok(
  clockW <= minUsable,
  `clock at ${clockAtMin}px (${clockW.toFixed(1)}px) overflows usable ${Math.round(minUsable)}px at ${minVw}px viewport`,
);
const breathing = minUsable - clockW;
assert.ok(
  breathing >= 8,
  `clock breathing room ${breathing.toFixed(1)}px < 8px at ${minVw}px viewport`,
);

// Full-size clock (56px) must fit at ≥760px viewport — the point where the
// clamp resolves to its upper bound.
const wideVw = 760;
const wideUsable = usableTextWidth(wideVw);
const fullClockW = clockTextWidth(56);
assert.ok(
  fullClockW <= wideUsable,
  `56px clock (${fullClockW.toFixed(1)}px) does not fit at ${wideVw}px viewport (${Math.round(wideUsable)}px usable)`,
);

// ---- CSS font-size contracts ----

function extractFontSize(css: string, selector: string): string | null {
  // Match a CSS rule block whose selector starts with the given string.
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(escaped + "\\s*\\{[^}]*\\}", "s");
  const block = css.match(re)?.[0];
  if (!block) return null;
  const m = block.match(/font-size:\s*([^;]+);/);
  return m ? m[1].trim() : null;
}

function extractCssRule(css: string, selector: string): string | null {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(escaped + "\\s*\\{[^}]*\\}", "s");
  return css.match(re)?.[0] ?? null;
}

const carouselCss = readFileSync(
  resolve(import.meta.dirname, "../components/widget/widget-info-carousel.css"),
  "utf-8",
);
const modeCss = readFileSync(
  resolve(import.meta.dirname, "../components/widget/widget-mode.css"),
  "utf-8",
);

// Button strong: must be ≤34px so "New task" fits the 216px button.
const btnSize = extractFontSize(modeCss, ".widget-new strong");
assert.ok(btnSize !== null, "Could not find .widget-new strong font-size");
const btnPx = Number.parseInt(btnSize!);
assert.ok(btnPx <= 34, `.widget-new strong font-size ${btnPx}px > 34px — button text would clip`);
assert.ok(btnPx >= 24, `.widget-new strong font-size ${btnPx}px < 24px — unexpected shrink`);

// Clock-specific class must keep the geometry constants used above.
const clockCss = extractFontSize(carouselCss, ".widget-info__value.widget-info__value--clock");
assert.ok(clockCss !== null, "Missing .widget-info__value.widget-info__value--clock font-size");
assert.ok(
  clockCss!.includes("clamp("),
  `clock must use clamp(), got: ${clockCss}`,
);
const cm = clockCss!.match(/clamp\(\s*(\d+)px\s*,\s*([\d.]+)vw\s*,\s*(\d+)px\s*\)/);
assert.ok(cm !== null, `Cannot parse clamp() in: ${clockCss}`);
const clampMin = Number(cm![1]);
const clampPreferred = Number(cm![2]);
const clampMax = Number(cm![3]);
assert.equal(clampMin, expectedClamp.min);
assert.equal(clampPreferred, expectedClamp.preferred);
assert.equal(clampMax, expectedClamp.max);
assert.match(carouselCss, /\.widget-info__page--clock\s*\{[^}]*padding-right:\s*12px;[^}]*padding-left:\s*12px;/s);

// ---- Nine-slice seam-free contract ----

// Every tile must bleed 1 px past its grid cell so adjacent tiles overlap
// and subpixel rounding cannot leave a visible seam.
const nineSliceImg = extractCssRule(modeCss, ".widget-shell__nine-slice img");
assert.ok(nineSliceImg, "Missing .widget-shell__nine-slice img rule");
assert.match(nineSliceImg!, /width:\s*calc\(\s*100%\s*\+\s*2px\s*\)/, "nine-slice tiles must bleed horizontally: width: calc(100% + 2px)");
assert.match(nineSliceImg!, /height:\s*calc\(\s*100%\s*\+\s*2px\s*\)/, "nine-slice tiles must bleed vertically: height: calc(100% + 2px)");
assert.match(nineSliceImg!, /margin:\s*-1px/, "nine-slice tiles must overlap neighbours: margin: -1px");
assert.match(nineSliceImg!, /object-fit:\s*fill/, "nine-slice tiles must still use object-fit: fill");

// The shell must clip the outer 1 px bleed so tiles never overflow
// past the shell clip-path.
assert.match(modeCss, /\.widget-shell\s*\{[^}]*overflow:\s*hidden/s, ".widget-shell must have overflow:hidden to clip tile bleed");

// The nine-slice container itself must also clip.
const nineSliceContainer = extractCssRule(modeCss, ".widget-shell__nine-slice");
assert.ok(nineSliceContainer, "Missing .widget-shell__nine-slice rule");
assert.match(nineSliceContainer!, /overflow:\s*hidden/, ".widget-shell__nine-slice must have overflow:hidden");

// ---- Per-skin independent component coverage ----

const skinsCss = readFileSync(
  resolve(import.meta.dirname, "../components/widget/widget-skins.css"),
  "utf-8",
);

const SKIN_IDS = ["bp", "instant", "pet", "recorder"] as const;

const REQUIRED_DECLARATIONS = [
  [".widget-info", "info"],
  [".widget-message", "left"],
  [".widget-context", "border-right-color"],
  [".widget-return", "top"],
  [".widget-workspace__toggle", "border-radius"],
  [".widget-new", "border-radius"],
  [".widget-action", "border-radius"],
  [".widget-reply", "border-radius"],
  [".widget-message__scan", "background"],
  [".widget-status-line", "background"],
] as const;

function extractSkinRule(skin: string, component: string): string | null {
  const selector = `[data-widget-skin="${skin}"] ${component}`;
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return skinsCss.match(new RegExp(`${escaped}[^\\{]*\\{[^}]*\\}`, "s"))?.[0] ?? null;
}

for (const skin of SKIN_IDS) {
  for (const [component, property] of REQUIRED_DECLARATIONS) {
    const rule = extractSkinRule(skin, component);
    assert.ok(rule, `Skin "${skin}" must define a rule for ${component}`);

    if (property === "info") {
      const expected = skin === "bp" || skin === "pet" ? /left:\s*[^;]+/ : /visibility:\s*hidden/;
      assert.match(rule!, expected, `Skin "${skin}" must own the ${component} geometry or visibility`);
      continue;
    }

    const escapedProperty = property.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    assert.match(
      rule!,
      new RegExp(`${escapedProperty}:\\s*[^;]+`),
      `Skin "${skin}" must own ${property} for ${component}`,
    );
  }
}

assert.match(
  skinsCss,
  /\[data-widget-skin\]:not\(\[data-widget-skin="classic"\]\)/,
  "shared control mechanics must explicitly exclude the classic skin",
);

process.stdout.write("widget layout contract tests passed\n");

// ---- Widget settings tab locale contract ----

// Verify all three locales define the widget settings tab keys.
import { zh } from "../locales/zh.ts";
import { zhTW } from "../locales/zh-TW.ts";

const widgetTabKeys = [
  "settings.tab.widget",
  "settings.tabSub.widget",
  "settings.pageDesc.widget",
  "settings.widget.enableLabel",
  "settings.widget.enableHint",
  "settings.widget.alwaysOnTopLabel",
  "settings.widget.alwaysOnTopHint",
] as const;

for (const key of widgetTabKeys) {
  assert.ok(key in enT, `en.ts missing key: ${key}`);
  assert.ok(key in zh, `zh.ts missing key: ${key}`);
  assert.ok(key in zhTW, `zh-TW.ts missing key: ${key}`);
  const enVal = (enT as Record<string, string>)[key];
  const zhVal = (zh as Record<string, string>)[key];
  const twVal = (zhTW as Record<string, string>)[key];
  assert.ok(enVal.length > 0, `en.${key} is empty`);
  assert.ok(zhVal.length > 0, `zh.${key} is empty`);
  assert.ok(twVal.length > 0, `zh-TW.${key} is empty`);
  // All locale values should differ from the key itself (not falling through).
  assert.notEqual(enVal, key, `en.${key} falls through to key`);
  assert.notEqual(zhVal, key, `zh.${key} falls through to key`);
  assert.notEqual(twVal, key, `zh-TW.${key} falls through to key`);
}

// Verify SettingsTab type includes "widget".
// Type-level check cannot be done at runtime, but verify the SETTINGS_TABS
// constant string presence via the locale key (which is used by the tab label).
assert.ok("settings.tab.widget" in enT, "settings.tab.widget missing from en.ts");

process.stdout.write("widget settings tab contract tests passed\n");
