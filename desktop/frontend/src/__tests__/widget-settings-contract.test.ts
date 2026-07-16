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

assert.match(settingsSource, /SETTINGS_TABS[^\n]+"widget"/, "Settings navigation includes the Widget tab");
assert.match(settingsSource, /tab === "widget"[\s\S]+<WidgetSection/, "Widget tab renders its settings section");
assert.match(settingsSource, /SetDesktopWidgetEnabled\(enabled\)/, "enable switch persists through the backend");
assert.match(settingsSource, /SetDesktopWidgetAlwaysOnTop\(on\)/, "always-on-top switch persists through the backend");
assert.match(appSource, /DesktopStartupSettings\(\)[\s\S]+setWidgetEnabled\(s\.widgetEnabled\)/, "startup reads widget enabled state");
assert.match(appSource, /EventsOn\("widget:enabled"/, "widget enabled changes propagate without restart");
assert.match(appSource, /WindowsWindowControls widgetEnabled=\{widgetEnabled\}/, "window chrome hides the widget entry when disabled");
assert.match(typesSource, /widgetAlwaysOnTop: boolean/, "frontend settings contract includes always-on-top state");
assert.match(bridgeSource, /DesktopStartupSettings\(\)[\s\S]+widgetEnabled/, "browser mock preserves widget enabled startup state");

for (const locale of ["en", "zh", "zh-TW"]) {
  const source = read(`../locales/${locale}.ts`);
  assert.ok(source.includes('"settings.tab.widget"'), `${locale} includes the Widget tab label`);
  assert.ok(source.includes('"settings.widget.alwaysOnTopLabel"'), `${locale} includes the always-on-top label`);
}

console.log("widget settings contract tests passed");
