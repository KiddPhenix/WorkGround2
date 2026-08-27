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

assert.match(settingsSource, /SETTINGS_NAV[\s\S]*\{ kind: "leaf", tab: "widget" \}/, "Settings navigation includes the Widget tab as a direct entry");
assert.match(settingsSource, /tab === "widget"[\s\S]+<WidgetSection/, "Widget tab renders its settings section");
assert.match(settingsSource, /SetDesktopWidgetEnabled\(enabled\)/, "enable switch persists through the backend");
assert.match(settingsSource, /SetDesktopWidgetAlwaysOnTop\(on\)/, "always-on-top switch persists through the backend");
assert.match(settingsSource, /SetDesktopWidgetShowDelegation\(show\)/, "delegation visibility persists through the backend");
assert.match(settingsSource, /SetDesktopWidgetShowExternalTools\(show\)/, "external AI tool visibility persists through the backend");
assert.match(settingsSource, /SetDesktopWidgetShowAssistant\(show\)/, "assistant visibility persists through the backend");
assert.match(settingsSource, /widgetShowDelegation=\{s\.widgetShowDelegation\}/, "the widget tab passes the delegation switch from the settings snapshot");
assert.match(settingsSource, /widgetShowExternalTools=\{s\.widgetShowExternalTools\}/, "the widget tab passes the external tools switch from the settings snapshot");
assert.match(settingsSource, /widgetShowAssistant=\{s\.widgetShowAssistant\}/, "the widget tab passes the assistant switch from the settings snapshot");
assert.doesNotMatch(settingsSource, /SetDesktopWidgetStyle\(|stylePager|styleIcons|SetDesktopWidgetSkin\(|settings-widget-skin-grid/, "Settings exposes no pager style picker and no pager-only skin picker");
assert.match(settingsSource, /SetDesktopHoverStatusDelayMs\(/, "icon hover delay stays configurable without a style picker");
assert.match(appSource, /DesktopStartupSettings\(\)[\s\S]+setWidgetEnabled\(s\.widgetEnabled\)/, "startup reads widget enabled state");
assert.match(appSource, /EventsOn\("widget:enabled"/, "widget enabled changes propagate without restart");
assert.match(appSource, /WindowsWindowControls widgetEnabled=\{widgetEnabled\}/, "window chrome hides the widget entry when disabled");
assert.match(appSource, /EventsOn\("widget:mode"/, "native widget mode events update React state");
assert.match(
  appSource,
  /createWidgetModeCoordinator\(app,\s*setWidgetMode,\s*\(\) => \{/,
  "widget transitions share one state coordinator with main-window restore cleanup",
);
assert.doesNotMatch(appSource + composerSource, /widget-mode-change/, "widget state does not depend on an ad-hoc DOM event");
assert.match(composerSource, /onEnterWidgetMode/, "Composer delegates widget entry to the shared coordinator");
assert.match(widgetCSS, /\.app--widget-hidden\s*\{[^}]*visibility:\s*hidden/s, "main content has an explicit widget-mode hiding fallback");
assert.match(widgetModeSource, /DesktopStartupSettings\(\)[\s\S]+resolveWidgetSkin\(settings\.widgetSkin\)/, "widget mode loads and normalizes startup skin");
assert.match(widgetModeSource, /EventsOn\("widget:skin"/, "widget skin changes propagate without restart");
assert.match(typesSource, /widgetAlwaysOnTop: boolean/, "frontend settings contract includes always-on-top state");
assert.match(typesSource, /widgetSkin: string/, "frontend settings contract includes skin state");
assert.match(typesSource, /widgetShowDelegation: boolean/, "frontend settings contract includes the delegation switch");
assert.match(typesSource, /widgetShowExternalTools: boolean/, "frontend settings contract includes the external tools switch");
assert.match(typesSource, /widgetShowAssistant: boolean/, "frontend settings contract includes the assistant switch");
assert.match(bridgeSource, /DesktopStartupSettings\(\)[\s\S]+widgetEnabled/, "browser mock preserves widget enabled startup state");
assert.match(bridgeSource, /SetDesktopWidgetSkin\(skin: string\)/, "bridge exposes SetDesktopWidgetSkin API");
assert.match(bridgeSource, /SetDesktopWidgetShowDelegation\(show: boolean\)/, "bridge exposes SetDesktopWidgetShowDelegation");
assert.match(bridgeSource, /SetDesktopWidgetShowExternalTools\(show: boolean\)/, "bridge exposes SetDesktopWidgetShowExternalTools");
assert.match(bridgeSource, /SetDesktopWidgetShowAssistant\(show: boolean\)/, "bridge exposes SetDesktopWidgetShowAssistant");
assert.match(bridgeSource, /widgetSkin: "classic"/, "browser mock defaults widgetSkin to classic");
assert.match(bridgeSource, /widgetStyle: "icons"/, "browser mock defaults widget style to icons");
assert.match(bridgeSource, /widgetShowDelegation: false/, "browser mock defaults delegation visibility to hidden");
assert.match(bridgeSource, /widgetShowExternalTools: false/, "browser mock defaults external tools visibility to hidden");
assert.match(bridgeSource, /widgetShowAssistant: true/, "browser mock defaults assistant visibility to shown");
assert.match(bridgeSource, /filter\(\(id\) => id !== "assistant" \|\| settings\.widgetShowAssistant\)/, "browser mock snapshot follows the assistant visibility setting");

// The desktop widget is icons-only: App renders only DesktopIconMode, and the
// icon mode owns its exit-to-main and settings entries through App callbacks.
assert.doesNotMatch(appSource, /<WidgetMode/, "App never renders the legacy pager");
assert.match(appSource, /<ReactActivity mode=\{widgetMode \? "visible" : "hidden"\}>[\s\S]{0,300}<DesktopIconMode onNewRoom=\{requestWidgetRoomDialog\} onOpenRoom=\{openWidgetRoom\} onOpenSettings=\{openWidgetSettings\} onOpenMain=\{openWidgetMain\} onOpenAssistant=\{openWidgetAssistant\} onOpenSession=\{revealWidgetSession\} \/>[\s\S]{0,80}<\/ReactActivity>/, "Activity preserves the authoritative icon projection and reveals it immediately in widget mode");
const iconModeSource = read("../components/widget/DesktopIconMode.tsx");
const iconCSS = read("../components/widget/desktop-icon-mode.css");
assert.match(iconModeSource, /onOpenMain: \(\) => Promise<void>/, "icon mode receives an async open-main callback from the root App");
assert.match(iconModeSource, /onOpenAssistant: \(\) => Promise<void>/, "icon mode receives an async open-assistant callback from the root App");
assert.match(iconModeSource, /role="switch" aria-checked=\{topmost\}/, "the quick toolbar always-on-top control is an ARIA switch");
assert.match(appSource, /const openWidgetMain = useCallback\(\(\) => \{[\s\S]*return widgetCoordinator\.exit\(\);/, "open main exits widget mode through the shared coordinator");
assert.doesNotMatch(iconCSS, /desktop-icon-exit/, "icon mode CSS carries no legacy exit-button styles");

// --- generated Wails binding contract: DesktopStartupSettingsView must carry
// the always-on-top field in the same shape as the Go struct, so the frontend
// startup read and the browser mock agree with the native binding ---
const modelsSource = read("../../wailsjs/go/models.ts");
const settingsViewClass = modelsSource.match(/export class DesktopStartupSettingsView \{[\s\S]*?\n\s*\}[\s\S]*?\n\s*}/)?.[0] ?? "";
assert.match(settingsViewClass, /widgetAlwaysOnTop: boolean;/, "the generated models.ts binding declares widgetAlwaysOnTop as boolean");
assert.match(settingsViewClass, /this\.widgetAlwaysOnTop = source\["widgetAlwaysOnTop"\];/, "the generated constructor projects widgetAlwaysOnTop from the Go JSON key");
assert.match(modelsSource, /widgetShowDelegation: boolean;/, "generated models.ts declares widgetShowDelegation");
assert.match(modelsSource, /this\.widgetShowDelegation = source\["widgetShowDelegation"\];/, "generated constructor projects widgetShowDelegation");
assert.match(modelsSource, /widgetShowExternalTools: boolean;/, "generated models.ts declares widgetShowExternalTools");
assert.match(modelsSource, /this\.widgetShowExternalTools = source\["widgetShowExternalTools"\];/, "generated constructor projects widgetShowExternalTools");
assert.match(modelsSource, /widgetShowAssistant: boolean;/, "generated models.ts declares widgetShowAssistant");
assert.match(modelsSource, /this\.widgetShowAssistant = source\["widgetShowAssistant"\];/, "generated constructor projects widgetShowAssistant");

for (const locale of ["en", "zh", "zh-TW"]) {
  const source = read(`../locales/${locale}.ts`);
  assert.ok(source.includes('"settings.tab.widget"'), `${locale} includes the Widget tab label`);
  assert.ok(source.includes('"settings.widget.alwaysOnTopLabel"'), `${locale} includes the always-on-top label`);
  assert.ok(source.includes('"settings.widget.showDelegationLabel"'), `${locale} includes the delegation label`);
  assert.ok(source.includes('"settings.widget.showDelegationHint"'), `${locale} includes the delegation hint`);
  assert.ok(source.includes('"settings.widget.showExternalToolsLabel"'), `${locale} includes the external tools label`);
  assert.ok(source.includes('"settings.widget.showExternalToolsHint"'), `${locale} includes the external tools hint`);
  assert.ok(source.includes('"settings.widget.showAssistantLabel"'), `${locale} includes the assistant label`);
  assert.ok(source.includes('"settings.widget.showAssistantHint"'), `${locale} includes the assistant hint`);
}

// Widget skin registry contract.
assert.match(skinsSource, /export const WIDGET_SKIN_IDS.*=.*\[/, "skin registry exports WIDGET_SKIN_IDS array");
for (const id of ["classic", "bp", "instant", "pet", "recorder"]) {
  assert.match(skinsSource, new RegExp(`"${id}"`), `skin registry includes "${id}"`);
}
assert.match(skinsSource, /export function resolveWidgetSkin/, "skin registry exports resolveWidgetSkin helper");
assert.match(skinsSource, /export function widgetSkinTiles/, "skin registry exports widgetSkinTiles helper");
assert.match(skinsSource, /export function widgetSkinPreview/, "skin registry exports widgetSkinPreview helper");

// --- anchor settings entry: exit-before-open, in-flight guard, retryable ---
// Opening settings from the widget must first exit widget mode; settings only
// reveal after a successful exit, and a failed exit stays visible in the
// widget (the main window is hidden) so the same 设置 click is a safe retry.
assert.match(
  appSource,
  /const widgetSettingsRequest = useRef\(false\);[\s\S]*?if \(widgetSettingsRequest\.current\) return;[\s\S]*?await widgetCoordinator\.exit\(\);[\s\S]*?useOverlayStore\.getState\(\)\.setSettingsFocus\(null\);[\s\S]*?useOverlayStore\.getState\(\)\.setSettingsTarget\("general"\);[\s\S]*?finally \{[\s\S]*?widgetSettingsRequest\.current = false[\s\S]*?\}/,
  "the widget settings entry exits widget mode first, opens settings only after the exit succeeds, and guards the async round-trip against double invocation",
);
assert.doesNotMatch(
  appSource,
  /setSettingsTarget\("general"\)[\s\S]{0,120}widgetCoordinator\.exit/,
  "settings must never open before the widget exit resolves",
);
assert.match(iconModeSource, /onOpenSettings: \(\) => Promise<void>/, "DesktopIconMode receives an async settings-open callback from the root App");
assert.match(iconModeSource, /DesktopStartupSettings\(\)[\s\S]+widgetAlwaysOnTop/, "the quick toolbar reads the initial always-on-top value through the existing startup contract");
assert.doesNotMatch(iconModeSource, /onOpenSettings[\s\S]{0,80}\.then\(\(\) => setSettingsTarget/, "the widget never opens settings directly; only the root App owns the exit-before-open flow");

// --- assistant fixed entry: exit-before-open, in-flight guard, no generic action ---
// Opening Assistant from the widget must first exit widget mode, then bump the
// monotonic signal that makes MainApp open the Assistant home; a failed exit
// stays visible in the widget so the same 助手 click is a safe retry.
assert.match(
  appSource,
  /const widgetAssistantRequest = useRef\(false\);[\s\S]*?if \(widgetAssistantRequest\.current\) return;[\s\S]*?await widgetCoordinator\.exit\(\);[\s\S]*?setAssistantOpenSignal\(\(count\) => count \+ 1\);[\s\S]*?finally \{[\s\S]*?widgetAssistantRequest\.current = false[\s\S]*?\}/,
  "the assistant entry exits widget mode first, bumps the monotonic open signal only after the exit succeeds, and guards the async round-trip against double invocation",
);
assert.doesNotMatch(
  appSource,
  /setAssistantOpenSignal\(\(count\) => count \+ 1\)[\s\S]{0,120}widgetCoordinator\.exit/,
  "assistant must never open before the widget exit resolves",
);
assert.match(
  appSource,
  /useAssistantSurfaceSignals\([\s\S]*?assistantOpenSignal,[\s\S]*?sessionRevealSignal,[\s\S]*?openAssistantSurface,[\s\S]*?closeAssistantSurface,[\s\S]*?revealActiveSession,[\s\S]*?\);/,
  "MainApp steers the Assistant surface through the shared assistant-surface signal hook (open on the Assistant icon, collapse on an explicit Session)",
);
// The assistant fixed entry never opens a generic popup and never runs the
// generic fixed action; it routes through the root App's onOpenAssistant.
assert.match(iconModeSource, /item\.kind === "fixed" && item\.sourceId === "assistant"[\s\S]*?void openAssistant\(\);/, "the assistant icon routes through its own open handler");
assert.doesNotMatch(iconModeSource, /sourceId === "assistant"[\s\S]{0,80}run\(item, "open"\)/, "the assistant icon never runs the generic fixed action");

console.log("widget settings contract tests passed");
