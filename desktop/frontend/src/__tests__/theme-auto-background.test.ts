// Run: tsx src/__tests__/theme-auto-background.test.ts

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const testDir = dirname(fileURLToPath(import.meta.url));
const themeSource = readFileSync(resolve(testDir, "../lib/theme.ts"), "utf8");

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

console.log("\ntheme auto native background contract");

ok(
  themeSource.includes('const AUTO_THEME_MEDIA_QUERY = "(prefers-color-scheme: light)";'),
  "auto theme uses one shared light color-scheme media query",
);
ok(
  themeSource.includes("window.matchMedia?.(AUTO_THEME_MEDIA_QUERY).matches"),
  "resolved auto theme reads the shared color-scheme query",
);
ok(
  themeSource.includes("syncAutoThemeBackgroundListener(theme);"),
  "applyTheme updates the native auto-theme listener",
);
ok(
  themeSource.includes('autoThemeMediaQuery.addEventListener("change", syncAutoThemeBackground)'),
  "auto mode listens for color-scheme changes",
);
ok(
  themeSource.includes('autoThemeMediaQuery.removeEventListener("change", syncAutoThemeBackground)'),
  "explicit modes remove the color-scheme listener",
);
ok(
  themeSource.includes('if (theme !== "auto")') && themeSource.includes("clearAutoThemeBackgroundListener();"),
  "non-auto themes clear the auto listener",
);
ok(
  themeSource.includes('if (currentTheme === "auto"') && themeSource.includes('syncNativeWindowBackground("auto")'),
  "color-scheme changes only sync native background while auto is active",
);
ok(
  themeSource.includes("if (autoThemeMediaQuery || typeof window"),
  "reapplying auto does not register duplicate listeners",
);
ok(
  themeSource.includes("nativeIconWidgetTransparent") &&
    themeSource.includes('getAttribute("data-platform") === "darwin"') &&
    themeSource.includes("Wails 2.12 serialises alpha as `a || 255`") &&
    !themeSource.includes("WindowSetBackgroundColour(0, 0, 0, 0)"),
  "macOS icon widget leaves transparent native background ownership to AppKit",
);
ok(
  themeSource.includes('setAttribute("data-icon-widget", "active")') &&
    themeSource.includes('node.style.setProperty("background", "transparent", "important")') &&
    themeSource.includes('node.style.removeProperty("background")'),
  "icon widget forces and restores an explicit transparent document canvas",
);

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
