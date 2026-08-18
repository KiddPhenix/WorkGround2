import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const read = (path: string) => readFileSync(resolve(here, path), "utf8");
const appSource = read("../App.tsx");
const settingsSource = read("../components/SettingsPanel.tsx");
const typesSource = read("../lib/types.ts");
const bridgeSource = read("../lib/bridge.ts");
const skinsSource = read("../components/widget/widgetSkins.ts");
const widgetModeSource = read("../components/widget/WidgetMode.tsx");
const composerSource = read("../components/Composer.tsx");
const widgetCSS = read("../components/widget/widget-mode.css");

assert.match(settingsSource, /SETTINGS_TABS[^\n]+"widget"/, "Settings navigation includes the Widget tab");
assert.match(settingsSource, /tab === "widget"[\s\S]+<WidgetSection/, "Widget tab renders its settings section");
assert.match(settingsSource, /SetDesktopWidgetEnabled\(enabled\)/, "enable switch persists through the backend");
assert.match(settingsSource, /SetDesktopWidgetAlwaysOnTop\(on\)/, "always-on-top switch persists through the backend");
assert.doesNotMatch(settingsSource, /SetDesktopWidgetStyle\(|stylePager|styleIcons|SetDesktopWidgetSkin\(|settings-widget-skin-grid/, "Settings exposes no pager style picker and no pager-only skin picker");
assert.match(settingsSource, /SetDesktopHoverStatusDelayMs\(/, "icon hover delay stays configurable without a style picker");
assert.match(appSource, /DesktopStartupSettings\(\)[\s\S]+setWidgetEnabled\(s\.widgetEnabled\)/, "startup reads widget enabled state");
assert.match(appSource, /EventsOn\("widget:enabled"/, "widget enabled changes propagate without restart");
assert.match(appSource, /WindowsWindowControls widgetEnabled=\{widgetEnabled\}/, "window chrome hides the widget entry when disabled");
assert.match(appSource, /EventsOn\("widget:mode"/, "native widget mode events update React state");
assert.match(appSource, /createWidgetModeCoordinator\(app, setWidgetMode\)/, "widget transitions share one state coordinator");
assert.doesNotMatch(appSource + composerSource, /widget-mode-change/, "widget state does not depend on an ad-hoc DOM event");
assert.match(composerSource, /onEnterWidgetMode/, "Composer delegates widget entry to the shared coordinator");
assert.match(widgetCSS, /\.app--widget-hidden\s*\{[^}]*visibility:\s*hidden/s, "main content has an explicit widget-mode hiding fallback");
assert.match(widgetModeSource, /DesktopStartupSettings\(\)[\s\S]+resolveWidgetSkin\(settings\.widgetSkin\)/, "widget mode loads and normalizes startup skin");
assert.match(widgetModeSource, /EventsOn\("widget:skin"/, "widget skin changes propagate without restart");
assert.match(typesSource, /widgetAlwaysOnTop: boolean/, "frontend settings contract includes always-on-top state");
assert.match(typesSource, /widgetSkin: string/, "frontend settings contract includes skin state");
assert.match(bridgeSource, /DesktopStartupSettings\(\)[\s\S]+widgetEnabled/, "browser mock preserves widget enabled startup state");
assert.match(bridgeSource, /SetDesktopWidgetSkin\(skin: string\)/, "bridge exposes SetDesktopWidgetSkin API");
assert.match(bridgeSource, /widgetSkin: "classic"/, "browser mock defaults widgetSkin to classic");
assert.match(bridgeSource, /widgetStyle: "icons"/, "browser mock defaults widget style to icons");

// The desktop widget is icons-only: App renders only DesktopIconMode, and the
// icon mode has no return-to-main button or onExit chain.
assert.doesNotMatch(appSource, /<WidgetMode/, "App never renders the legacy pager");
assert.match(appSource, /widgetMode && <DesktopIconMode \/>/, "widget mode renders only the icon mode");
const iconModeSource = read("../components/widget/DesktopIconMode.tsx");
const iconCSS = read("../components/widget/desktop-icon-mode.css");
assert.doesNotMatch(iconModeSource, /onExit|返回主窗口|desktop-icon-exit/, "icon mode has no return-to-main button or onExit prop");
assert.doesNotMatch(iconCSS, /desktop-icon-exit/, "icon mode CSS carries no exit-button styles");

for (const locale of ["en", "zh", "zh-TW"]) {
  const source = read(`../locales/${locale}.ts`);
  assert.ok(source.includes('"settings.tab.widget"'), `${locale} includes the Widget tab label`);
  assert.ok(source.includes('"settings.widget.alwaysOnTopLabel"'), `${locale} includes the always-on-top label`);
}

// Widget skin registry contract.
assert.match(skinsSource, /export const WIDGET_SKIN_IDS.*=.*\[/, "skin registry exports WIDGET_SKIN_IDS array");
for (const id of ["classic", "bp", "instant", "pet", "recorder"]) {
  assert.match(skinsSource, new RegExp(`"${id}"`), `skin registry includes "${id}"`);
}
assert.match(skinsSource, /export function resolveWidgetSkin/, "skin registry exports resolveWidgetSkin helper");
assert.match(skinsSource, /export function widgetSkinTiles/, "skin registry exports widgetSkinTiles helper");
assert.match(skinsSource, /export function widgetSkinPreview/, "skin registry exports widgetSkinPreview helper");

console.log("widget settings contract tests passed");
