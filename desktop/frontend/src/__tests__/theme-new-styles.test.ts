// Run: tsx src/__tests__/theme-new-styles.test.ts
import { JSDOM } from "jsdom";
import {
  THEME_STYLES,
  isThemeStyle,
  normalizeThemeStyleForTheme,
  applyTheme,
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
    ok(false, `${label}: "${needle}" not found`);
  }
}

// ── Load CSS and locale files ──────────────────────────────────

let cssText = "";
try {
  cssText = fs.readFileSync(path.resolve(__dirname, "../styles.css"), "utf8");
} catch {
  cssText = fs.readFileSync(path.resolve(process.cwd(), "src/styles.css"), "utf8");
}

let zhText = "";
let enText = "";
let zhTWText = "";
try {
  zhText = fs.readFileSync(path.resolve(__dirname, "../locales/zh.ts"), "utf8");
  enText = fs.readFileSync(path.resolve(__dirname, "../locales/en.ts"), "utf8");
  zhTWText = fs.readFileSync(path.resolve(__dirname, "../locales/zh-TW.ts"), "utf8");
} catch {
  zhText = fs.readFileSync(path.resolve(process.cwd(), "src/locales/zh.ts"), "utf8");
  enText = fs.readFileSync(path.resolve(process.cwd(), "src/locales/en.ts"), "utf8");
  zhTWText = fs.readFileSync(path.resolve(process.cwd(), "src/locales/zh-TW.ts"), "utf8");
}

const NEW_STYLES: ThemeStyle[] = [
  "verdant", "cobalt", "bordeaux",
  "digital-rain", "shinobi-flame", "mecha-sakura",
];

const swatchColors: Record<string, { darkBg: string; darkSurface: string; darkAccent: string; lightBg: string; lightAccent: string }> = {
  verdant:      { darkBg: "#08110f", darkSurface: "#101c19", darkAccent: "#40c99a", lightBg: "#f3f7f5", lightAccent: "#147a5a" },
  cobalt:       { darkBg: "#070d18", darkSurface: "#0d1728", darkAccent: "#4f7cff", lightBg: "#f4f7fc", lightAccent: "#2e60d4" },
  bordeaux:     { darkBg: "#120a0e", darkSurface: "#1d1117", darkAccent: "#d35e7a", lightBg: "#faf5f2", lightAccent: "#b23e54" },
  "digital-rain":  { darkBg: "#030806", darkSurface: "#07110c", darkAccent: "#39e67d", lightBg: "#f2f6f3", lightAccent: "#147f3d" },
  "shinobi-flame": { darkBg: "#100b09", darkSurface: "#1b1210", darkAccent: "#f06a32", lightBg: "#faf6f3", lightAccent: "#c24818" },
  "mecha-sakura":  { darkBg: "#0b0a16", darkSurface: "#141226", darkAccent: "#f064a8", lightBg: "#f6f4fa", lightAccent: "#c63482" },
};

// ── 1. Registered in THEME_STYLES ──────────────────────────────

console.log("\ntheme-new-styles: Registered in THEME_STYLES");
for (const style of NEW_STYLES) {
  ok(THEME_STYLES.includes(style), `${style} is in THEME_STYLES`);
  ok(isThemeStyle(style), `isThemeStyle('${style}') returns true`);
  eq(normalizeThemeStyleForTheme(style), style, `normalizeThemeStyleForTheme('${style}') === '${style}'`);
}

// ── 2. applyTheme accepts new styles ───────────────────────────

console.log("\ntheme-new-styles: applyTheme sets correct DOM attribute");
const dom = new JSDOM("<!DOCTYPE html><html><body></body></html>", { url: "http://localhost" });
(global as unknown as Record<string, unknown>).document = dom.window.document;
(global as unknown as Record<string, unknown>).window = dom.window;

for (const style of NEW_STYLES) {
  for (const theme of ["dark", "light", "auto"] as const) {
    applyTheme(theme, style);
    const root = dom.window.document.documentElement;
    eq(root.getAttribute("data-theme-style"), style, `applyTheme ${theme} -> data-theme-style is ${style}`);
  }
}

// ── 3. CSS dark root selectors exist ───────────────────────────

console.log("\ntheme-new-styles: CSS dark root selectors exist");
for (const style of NEW_STYLES) {
  includes(cssText, `:root[data-theme-style="${style}"]`, `dark selector for ${style}`);
  includes(cssText, `--bg: #`, `--bg token present in ${style} block`);
}

// ── 4. CSS explicit light selectors exist ─────────────────────

console.log("\ntheme-new-styles: CSS explicit light selectors exist");
for (const style of NEW_STYLES) {
  includes(cssText, `:root[data-theme="light"][data-theme-style="${style}"]`, `explicit light selector for ${style}`);
}

// ── 5. CSS auto-light (prefers-color-scheme) selectors exist ──

console.log("\ntheme-new-styles: CSS auto-light selectors exist");
for (const style of NEW_STYLES) {
  includes(cssText, `[data-theme-style="${style}"]:not([data-theme])`, `auto-light selector for ${style}`);
}

// ── 6. No gradients in new theme variable blocks ───────────────

console.log("\ntheme-new-styles: No decorative gradients in new theme variable blocks");
for (const style of NEW_STYLES) {
  const startMarker = `:root[data-theme-style="${style}"] {`;
  const startIdx = cssText.indexOf(startMarker);
  if (startIdx < 0) {
    ok(false, `${style} dark block not found`);
    continue;
  }
  const afterBlock = cssText.indexOf(":root[data-theme", startIdx + startMarker.length);
  const block = afterBlock > startIdx ? cssText.slice(startIdx, afterBlock) : cssText.slice(startIdx, startIdx + 4000);
  includes(block, "--grad: none", `${style} sets --grad: none`);
  const gradMatches = block.match(/linear-gradient/g);
  if (!gradMatches) {
    ok(true, `no linear-gradient in ${style} dark block`);
  } else {
    ok(false, `${style} dark block contains ${gradMatches.length} linear-gradient(s)`);
  }
}

// ── 7. Swatch preview colors exist ─────────────────────────────

console.log("\ntheme-new-styles: Swatch preview colors exist in CSS");
for (const [style, colors] of Object.entries(swatchColors)) {
  // Dark swatches (default)
  includes(cssText, `data-theme-style-card="${style}"] .theme-card__swatch--bg { background: ${colors.darkBg}`, `${style} swatch dark bg`);
  includes(cssText, `data-theme-style-card="${style}"] .theme-card__swatch--surface { background: ${colors.darkSurface}`, `${style} swatch dark surface`);
  includes(cssText, `data-theme-style-card="${style}"] .theme-card__swatch--accent { background: ${colors.darkAccent}`, `${style} swatch dark accent`);

  // Light swatches
  includes(cssText, `data-theme-style-card="${style}"] .theme-card__swatch--bg { background: ${colors.lightBg}`, `${style} swatch light bg`);
  includes(cssText, `data-theme-style-card="${style}"] .theme-card__swatch--accent { background: ${colors.lightAccent}`, `${style} swatch light accent`);

  // Auto-light swatches (inside @media)
  includes(cssText, `data-theme-style-card="${style}"] .theme-card__swatch--bg { background: ${colors.lightBg}`, `${style} swatch auto-light bg`);
}

// ── 8. i18n keys complete in all three locales ─────────────────

console.log("\ntheme-new-styles: i18n keys complete in zh / en / zh-TW");
for (const style of NEW_STYLES) {
  for (const suffix of ["zh", "note", "desc"]) {
    const key = `"settings.style.${style}.${suffix}"`;
    includes(zhText, key, `zh.ts has ${key}`);
    includes(enText, key, `en.ts has ${key}`);
    includes(zhTWText, key, `zh-TW.ts has ${key}`);
  }
}

// ── 9. No empty/missing i18n values ────────────────────────────

console.log("\ntheme-new-styles: No empty/missing i18n values");
for (const [localeName, text] of [["zh.ts", zhText], ["en.ts", enText], ["zh-TW.ts", zhTWText]] as const) {
  for (const style of NEW_STYLES) {
    for (const suffix of ["zh", "note", "desc"]) {
      const key = `settings.style.${style}.${suffix}`;
      const regex = new RegExp(`"${key}":\\s*"([^"]*)"`);
      const match = text.match(regex);
      if (!match) {
        ok(false, `${localeName}: ${key} has no value`);
      } else {
        const value = match[1];
        if (value.length === 0) {
          ok(false, `${localeName}: ${key} value is empty`);
        } else {
          ok(true, `${localeName}: ${key} = "${value.slice(0, 30)}${value.length > 30 ? "…" : ""}"`);
        }
      }
    }
  }
}

// ── Summary ────────────────────────────────────────────────────

const total = passed + failed;
console.log(`\n${total} tests: ${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
