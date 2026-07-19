// Regression tests for widget layout & text contracts.
// Models the actual 200% shell / scale(0.5) geometry and validates that
// clock sizing, button text, and EN idle strings stay within bounds at the
// 520 px native minimum width.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

// ---- Locale text-length contracts ----

import { en as enT } from "../locales/en.ts";
import { normalizeWidgetZoom, resolveWidgetZoomFrame } from "../components/widget/widgetZoom.ts";

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

// ---- Desktop WebView zoom contract ----
//
// WebView2 zoom shrinks the CSS viewport while enlarging every CSS pixel. The
// widget must expand its logical frame by the same factor, then apply the
// inverse transform so the final native footprint remains exactly 520x160.
for (const zoom of [0.5, 0.8, 1, 1.25, 1.5, 2]) {
  const frame = resolveWidgetZoomFrame(zoom);
  const cssViewportWidth = minVw / zoom;
  const cssViewportHeight = 160 / zoom;
  const finalWidth = cssViewportWidth * frame.widthVw / 100 * frame.scale * zoom;
  const finalHeight = cssViewportHeight * frame.heightVh / 100 * frame.scale * zoom;
  assert.ok(Math.abs(finalWidth - minVw) < 0.001, `${zoom}x zoom changes widget width to ${finalWidth}`);
  assert.ok(Math.abs(finalHeight - 160) < 0.001, `${zoom}x zoom changes widget height to ${finalHeight}`);
}

for (const invalid of [undefined, null, Number.NaN, 0.49, 2.01, "1.25"]) {
  assert.equal(normalizeWidgetZoom(invalid), 1, `invalid zoom ${String(invalid)} must safely fall back to 1`);
}

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
  const block = (css.match(/[^{}]+\{[^}]*\}/gs) ?? []).find((rule) =>
    rule.slice(0, rule.indexOf("{")).split(",").some((entry) => entry.trim() === selector),
  );
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
const skinsCss = readFileSync(
  resolve(import.meta.dirname, "../components/widget/widget-skins.css"),
  "utf-8",
);
const carouselSource = readFileSync(
  resolve(import.meta.dirname, "../components/widget/WidgetInfoCarousel.tsx"),
  "utf-8",
);
const modeSource = readFileSync(
  resolve(import.meta.dirname, "../components/widget/WidgetMode.tsx"),
  "utf-8",
);

const modeRoot = extractCssRule(modeCss, ".widget-mode");
assert.match(modeRoot ?? "", /min-width:\s*0/, "widget root must not force a 520px CSS viewport at desktop zoom");
assert.match(modeRoot ?? "", /min-height:\s*0/, "widget root must not force a 160px CSS viewport at desktop zoom");
assert.match(modeSource, /app\.GetDesktopZoomFactor\(\)/, "widget must read the active WebView zoom factor");
assert.match(modeSource, /resolveWidgetZoomFrame\(desktopZoom\)/, "widget must apply the inverse desktop zoom frame");
const enterAnimation = modeCss.match(/@keyframes\s+widget-enter\s*\{[\s\S]*?\n\}/)?.[0] ?? "";
assert.doesNotMatch(enterAnimation, /transform:/, "widget entry animation must not override the zoom compensation transform");

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
assert.match(carouselCss, /\.widget-info__page--clock,\s*\.widget-info__page--timer\s*\{[^}]*padding-right:\s*12px;[^}]*padding-left:\s*12px;/s);
assert.match(carouselSource, /widget-info__page widget-info__page--timer/, "idle timer must use the compact time safe area");
assert.match(carouselSource, /widget-info__value widget-info__value--timer/, "idle duration must use the adaptive timer size");

const timerCss = extractFontSize(carouselCss, ".widget-info__value.widget-info__value--timer");
assert.equal(timerCss, clockCss, "clock and idle duration must share the same safe base size");

// The pet screen is narrower than the pager LCD, so it owns a tighter timer
// clamp. At the native minimum it must still fit eight Doto glyphs.
const petTimer = skinsCss.match(/\[data-widget-skin="pet"\]\s+:is\(\.widget-info__value--clock,\s*\.widget-info__value--timer\)\s*\{[^}]*font-size:\s*clamp\(\s*(\d+)px\s*,\s*([\d.]+)vw\s*,\s*(\d+)px\s*\)/s);
assert.ok(petTimer, "pet skin must define its narrow-screen timer clamp");
const petTimerAtMin = Math.min(Number(petTimer![3]), Math.max(Number(petTimer![1]), minVw * Number(petTimer![2]) / 100));
const petUsableAtMin = 0.174 * minVw * 2 - 16;
assert.ok(clockTextWidth(petTimerAtMin) <= petUsableAtMin, `pet timer overflows at minimum width: ${clockTextWidth(petTimerAtMin).toFixed(1)} > ${petUsableAtMin.toFixed(1)}`);

// The pet skin's functional info layer must stay inside the dedicated left
// LCD. Its animated companion also has to shrink with that screen instead of
// keeping the generic fixed 230px width and crossing the hardware divider.
const petInfo = extractSkinRule("pet", ".widget-info");
const petLeft = Number(petInfo?.match(/left:\s*([\d.]+)%/)?.[1]);
const petWidth = Number(petInfo?.match(/width:\s*([\d.]+)%/)?.[1]);
assert.ok(Number.isFinite(petLeft) && Number.isFinite(petWidth), "pet info screen needs explicit percentage geometry");
assert.ok(petLeft + petWidth <= 20.8, `pet info screen crosses the hardware divider at ${petLeft + petWidth}%`);
const petSprite = extractSkinRule("pet", ".widget-info__pet");
assert.match(petSprite ?? "", /width:\s*min\(\s*230px\s*,\s*100%\s*\)/, "pet companion must shrink to the mini-screen width");

// ---- Nine-slice seam-free contract ----

// Middle tiles bleed 1 px past each edge so adjacent tiles overlap and
// subpixel rounding cannot leave a visible seam.
const nineSliceImg = extractCssRule(modeCss, ".widget-shell__nine-slice img");
assert.ok(nineSliceImg, "Missing .widget-shell__nine-slice img rule");
assert.match(nineSliceImg!, /width:\s*calc\(\s*100%\s*\+\s*2px\s*\)/, "nine-slice tiles must bleed horizontally: width: calc(100% + 2px)");
assert.match(nineSliceImg!, /height:\s*calc\(\s*100%\s*\+\s*2px\s*\)/, "nine-slice tiles must bleed vertically: height: calc(100% + 2px)");
assert.match(nineSliceImg!, /margin-left:\s*-1px/, "nine-slice tiles must overlap horizontal neighbours");
assert.match(nineSliceImg!, /margin-top:\s*-1px/, "nine-slice tiles must overlap vertical neighbours");
assert.match(nineSliceImg!, /object-fit:\s*fill/, "nine-slice tiles must still use object-fit: fill");

// Outermost tiles may only bleed toward the interior. This preserves the
// source image's perimeter pixel, especially the right frame at minimum size.
const leftEdgeTiles = extractCssRule(modeCss, '.widget-shell__nine-slice img:is([data-tile="0"], [data-tile="3"], [data-tile="6"])');
const rightEdgeTiles = extractCssRule(modeCss, '.widget-shell__nine-slice img:is([data-tile="2"], [data-tile="5"], [data-tile="8"])');
const topEdgeTiles = extractCssRule(modeCss, '.widget-shell__nine-slice img:is([data-tile="0"], [data-tile="1"], [data-tile="2"])');
const bottomEdgeTiles = extractCssRule(modeCss, '.widget-shell__nine-slice img:is([data-tile="6"], [data-tile="7"], [data-tile="8"])');
assert.match(leftEdgeTiles ?? "", /width:\s*calc\(\s*100%\s*\+\s*1px\s*\)[^}]*margin-left:\s*0/s, "left shell edge must remain inside the grid");
assert.match(rightEdgeTiles ?? "", /width:\s*calc\(\s*100%\s*\+\s*1px\s*\)/, "right shell edge must remain inside the grid");
assert.match(topEdgeTiles ?? "", /height:\s*calc\(\s*100%\s*\+\s*1px\s*\)[^}]*margin-top:\s*0/s, "top shell edge must remain inside the grid");
assert.match(bottomEdgeTiles ?? "", /height:\s*calc\(\s*100%\s*\+\s*1px\s*\)/, "bottom shell edge must remain inside the grid");

// The shell must clip the outer 1 px bleed so tiles never overflow
// past the shell clip-path.
assert.match(modeCss, /\.widget-shell\s*\{[^}]*overflow:\s*hidden/s, ".widget-shell must have overflow:hidden to clip tile bleed");

// The nine-slice container itself must also clip.
const nineSliceContainer = extractCssRule(modeCss, ".widget-shell__nine-slice");
assert.ok(nineSliceContainer, "Missing .widget-shell__nine-slice rule");
assert.match(nineSliceContainer!, /overflow:\s*hidden/, ".widget-shell__nine-slice must have overflow:hidden");

// ---- Per-skin independent component coverage ----

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
  const prefix = `[data-widget-skin="${skin}"]`;
  const escaped = prefix.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const blocks = skinsCss.match(new RegExp(`${escaped}[^\\{]*\\{[^}]*\\}`, "gs")) ?? [];
  return blocks.find((block) => block.slice(0, block.indexOf("{")).includes(component)) ?? null;
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

const materialSignatures = new Set<string>();
for (const skin of SKIN_IDS) {
  const material = extractSkinRule(skin, ".widget-new");
  assert.ok(material, `Skin "${skin}" must define its button material`);
  assert.match(material!, /border:\s*[^;]+/, `Skin "${skin}" button material must define a physical border`);
  assert.match(material!, /background:\s*[^;]+/, `Skin "${skin}" button material must define its own surface`);
  assert.match(material!, /box-shadow:\s*[^;]+/, `Skin "${skin}" button material must define depth`);
  materialSignatures.add(material!.replace(/\s+/g, " "));
}
assert.equal(materialSignatures.size, SKIN_IDS.length, "every generated skin must have a distinct button material");

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
