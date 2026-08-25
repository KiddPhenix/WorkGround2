import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const css = readFileSync(resolve(import.meta.dirname, "../styles.css"), "utf8");
const appSource = readFileSync(resolve(import.meta.dirname, "../App.tsx"), "utf8");
const settingsSource = readFileSync(resolve(import.meta.dirname, "../components/SettingsPanel.tsx"), "utf8");
const finalGuardMarker = "/* ── full-window settings: keep the native window bar usable";
const finalGuardIndex = css.lastIndexOf(finalGuardMarker);
const finalGuard = css.slice(finalGuardIndex);

assert.ok(finalGuardIndex > css.indexOf(":root[data-theme-style] .settings-modal-backdrop"), "the full-window guard follows theme-style modal overrides in the cascade");
assert.ok(finalGuardIndex > css.indexOf("padding: 16px", css.indexOf(".app--creation .settings-modal-backdrop")), "the full-window guard follows Creation's 900px card padding override");
assert.ok(finalGuardIndex > css.indexOf("height: min(900px, calc(100vh - 32px))"), "the full-window guard follows Creation's 900px height override");
assert.ok(finalGuardIndex > css.indexOf("padding: 0", css.indexOf("@media (max-width: 760px)", css.indexOf(".app--creation .settings-modal-backdrop"))), "the full-window guard follows Creation's 760px top-inset override");

// --- full-window settings shell: the centred card becomes the whole content
// area (like a sidebar-collapsed session), with only the reserved top window
// bar / caption controls kept above it ---
assert.match(
  css,
  /\.settings-modal-backdrop\s*\{[^}]*--wails-draggable:\s*drag;[^}]*padding:\s*var\(--settings-top-offset,\s*0px\)\s+0\s+0;[^}]*align-items:\s*stretch;[^}]*justify-content:\s*stretch;[^}]*background:\s*var\(--bg\);/s,
  "the settings backdrop stretches edge-to-edge (no card margin) and paints an opaque app background that hides the session below",
);

assert.match(
  finalGuard,
  /\.settings-modal-backdrop,[\s\S]*?:root\[data-theme-style\] \.settings-modal-backdrop,[\s\S]*?\.app--creation \.settings-modal-backdrop,[\s\S]*?:root\[data-theme-style\] \.app--creation \.settings-modal-backdrop\s*\{[^}]*padding:\s*var\(--settings-top-offset,\s*0px\)\s+0\s+0;[^}]*align-items:\s*stretch;[^}]*justify-content:\s*stretch;[^}]*background:\s*var\(--bg\);[^}]*backdrop-filter:\s*none;/s,
  "the final cascade guard keeps every theme and Creation backdrop opaque, full-window, and below the reserved top inset",
);
assert.match(
  finalGuard,
  /\.settings-modal,[\s\S]*?:root\[data-theme-style\] \.settings-modal,[\s\S]*?\.app--creation \.settings-modal,[\s\S]*?:root\[data-theme-style\] \.app--creation \.settings-modal\s*\{[^}]*width:\s*100%;[^}]*height:\s*100%;[^}]*border:\s*0;[^}]*border-radius:\s*0;[^}]*background:\s*var\(--bg\);[^}]*box-shadow:\s*none;/s,
  "the final cascade guard keeps classic, workbench, and Creation settings free of card geometry",
);
assert.match(
  css,
  /\.settings-modal\s*\{[^}]*--wails-draggable:\s*no-drag;[^}]*width:\s*100%;[^}]*height:\s*100%;[^}]*border:\s*0;[^}]*border-radius:\s*0;[^}]*box-shadow:\s*none;/s,
  "the settings panel fills the whole content area with no floating-card radius, border, or shadow",
);

assert.match(css, /\.settings-modal-backdrop\s*\{[^}]*--wails-draggable:\s*drag;/s, "the reserved settings top strip drags the native window");
assert.match(css, /\.settings-modal\s*\{[^}]*--wails-draggable:\s*no-drag;/s, "settings content opts out so controls keep receiving input");

// --- per-mode top inset: window bar / caption controls stay visible ---
assert.match(css, /\.app--darwin \.settings-modal-backdrop\s*\{[^}]*--settings-top-offset:\s*44px;/, "darwin keeps the 44px native window bar above settings");
assert.match(
  css,
  /\.app--windows-frameless:not\(\.app--workbench\):not\(\.app--creation\) \.settings-modal-backdrop,[\s\S]*?\.app--linux \.settings-modal-backdrop\s*\{[^}]*--settings-top-offset:\s*38px;/,
  "classic windows/linux keep the 38px app chrome above settings",
);
assert.match(css, /\.app--windows-frameless\.app--workbench \.settings-modal-backdrop\s*\{[^}]*--settings-top-offset:\s*55px;/, "workbench reserves the floating caption control rail");
assert.match(css, /\.app--windows-frameless\.app--creation \.settings-modal-backdrop\s*\{[^}]*--settings-top-offset:\s*56px;/, "creation reserves the caption control rail");

// --- window bar raised above the modal so drag / minimize / maximize / close
// stay usable while settings are open ---
assert.match(css, /--z-settings-chrome:\s*1210;/, "a dedicated z-index token sits just above --z-modal");
assert.match(
  css,
  /\.app--settings-open \.app-chrome,[\s\S]*?\.app--settings-open\.app--windows-frameless \.windows-window-controls\s*\{[^}]*z-index:\s*var\(--z-settings-chrome\);/,
  "opening settings raises the app chrome and caption controls above the settings layer",
);

// --- media queries must not regress the full-window shape ---
assert.match(css, /@media \(max-width: 900px\)\s*\{[\s\S]*?\.settings-modal-backdrop\s*\{[^}]*padding:\s*var\(--settings-top-offset,\s*0px\)\s+0\s+0;/s, "the 900px breakpoint keeps the reserved top inset");
assert.match(css, /@media \(max-width: 900px\)\s*\{[\s\S]*?\.settings-modal\s*\{[^}]*width:\s*100%;[^}]*height:\s*100%;/s, "the 900px breakpoint keeps the panel full-height");
assert.match(css, /@media \(max-width: 760px\)\s*\{[\s\S]*?\.settings-modal-backdrop\s*\{[^}]*padding:\s*var\(--settings-top-offset,\s*0px\)\s+0\s+0;/s, "the 760px breakpoint keeps the reserved top inset");

// --- creation override: the old 980x690 card must be gone and replaced by the
// full-window shape at equal-or-higher specificity ---
assert.doesNotMatch(css, /width:\s*min\(980px,\s*calc\(100vw - 160px\)\)/, "creation no longer shrinks settings to 980px");
assert.doesNotMatch(css, /height:\s*min\(690px,\s*calc\(100vh - 112px\)\)/, "creation no longer shrinks settings to 690px");
assert.doesNotMatch(css, /\.app--creation \.settings-modal[^}]*border-radius:\s*18px/, "creation no longer styles settings as a rounded card");
assert.match(
  css,
  /\.app--creation \.settings-modal,[\s\S]*?:root\[data-theme-style\] \.app--creation \.settings-modal\s*\{[^}]*width:\s*100%;[^}]*height:\s*100%;[^}]*border-radius:\s*0;[^}]*box-shadow:\s*none;/s,
  "creation and its theme-style overrides both get the full-window shape",
);
assert.match(
  css,
  /\.app--creation \.settings-modal-backdrop,[\s\S]*?:root\[data-theme-style\] \.app--creation \.settings-modal-backdrop\s*\{[^}]*padding:\s*var\(--settings-top-offset,\s*0px\)\s+0\s+0;[^}]*backdrop-filter:\s*none;/s,
  "creation backdrop keeps the reserved top inset and drops the frosted-glass blur",
);

// --- nav and content scroll independently in narrow / short windows ---
assert.match(css, /\.settings-center__nav\s*\{[^}]*overflow-y:\s*auto;/, "the settings nav scrolls independently");
assert.match(css, /\.settings-center__content\s*\{[^}]*overflow-x:\s*hidden;[^}]*overflow-y:\s*auto;/, "the settings content scrolls independently");

// --- App wires the full-window state so the CSS scope is active ---
assert.match(appSource, /settingsTarget !== null \? "app--settings-open" : ""/, "App marks the settings-open scope on the root container");

// --- close semantics stay unchanged: deferred exit animation and Escape ---
assert.match(settingsSource, /useDeferredClose\(onClose,\s*240\)/, "settings keeps its 240ms deferred close animation");
assert.match(settingsSource, /data-state=\{status\}/, "the backdrop/modal keep the closing-state animation hooks");
assert.match(settingsSource, /if \(e\.key === "Escape"[\s\S]*requestClose\(\)/, "Escape still closes settings through the same requestClose path");

console.log("settings full-window layout tests passed");
