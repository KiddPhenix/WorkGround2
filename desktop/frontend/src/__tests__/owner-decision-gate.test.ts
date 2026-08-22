// Regression contract tests for the temporary 主人决策 (owner decision)
// feature gate: while the backend kill switch is off (default), the desktop
// frontend must hide the sidebar entry, the decision centre, and the bot
// settings decision-channel section, and must not subscribe to decision state
// or call decision APIs. Restoration only flips the backend constant — the
// gated UI re-appears automatically via the ownerDecisionEnabled field.
// Run: tsx src/__tests__/owner-decision-gate.test.ts

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

let passed = 0;
let failed = 0;

function ok(cond: boolean, label: string) {
  if (cond) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

const here = dirname(fileURLToPath(import.meta.url));
const appSource = readFileSync(resolve(here, "../App.tsx"), "utf8");
const bridgeSource = readFileSync(resolve(here, "../lib/bridge.ts"), "utf8");
const typesSource = readFileSync(resolve(here, "../lib/types.ts"), "utf8");
const settingsPanelSource = readFileSync(resolve(here, "../components/SettingsPanel.tsx"), "utf8");
const backendSettingsSource = readFileSync(resolve(here, "../../../settings_app.go"), "utf8");
const backendFeatureSource = readFileSync(resolve(here, "../../../decision_feature.go"), "utf8");

console.log("\nowner-decision feature gate (temporary kill switch)");

ok(
  /const ownerDecisionFeatureEnabled = false/.test(backendFeatureSource),
  "backend kill switch defaults to false (disabled)",
);
ok(
  /ownerDecisionFeatureEnabled 是“主人决策”全局功能的临时屏蔽开关/.test(backendFeatureSource) &&
    /把这里改回 true 即可/.test(backendFeatureSource),
  "backend kill switch comment documents the explicit restoration step",
);
ok(
  /OwnerDecisionEnabled\s+bool\s+`json:"ownerDecisionEnabled"`/.test(backendSettingsSource),
  "backend SettingsView carries ownerDecisionEnabled to the frontend",
);
ok(
  /OwnerDecisionEnabled bool `json:"ownerDecisionEnabled"`/.test(backendSettingsSource),
  "backend DesktopStartupSettingsView carries ownerDecisionEnabled to the frontend",
);

ok(
  /setOwnerDecisionEnabled\(s\.ownerDecisionEnabled === true\)/.test(appSource),
  "App reads the gate from DesktopStartupSettings at startup",
);
ok(
  /const \[ownerDecisionEnabled, setOwnerDecisionEnabled\] = useState\(false\)/.test(appSource) &&
    /ownerDecisionEnabled mirrors the backend ownerDecisionFeatureEnabled kill/.test(appSource),
  "App keeps the gate state default-off with a restoration comment",
);

ok(
  /ownerDecisionEnabled && \(\s*<Tooltip label="主人决策" side="bottom">/.test(appSource) &&
    /aria-label="打开主人决策"/.test(appSource),
  "sidebar owner-decision entry is gated but kept intact for restoration",
);
ok(
  /ownerDecisionEnabled && <DecisionCenter open=\{decisionCenterOpen\}/.test(appSource),
  "DecisionCenter rendering is gated",
);
ok(
  /MainApp widgetEnabled=\{widgetEnabled\} widgetActive=\{widgetMode\} ownerDecisionEnabled=\{ownerDecisionEnabled\}/.test(appSource),
  "MainApp receives the gate through the existing startup contract",
);

ok(
  /ownerDecisionEnabled: false, \/\/ master kill switch for the 主人决策 feature \(default off\)/.test(bridgeSource) &&
    /ownerDecisionEnabled,\n\s*\}\)\) as DesktopStartupSettingsView/.test(bridgeSource),
  "browser mock defaults the gate to false and forwards it via DesktopStartupSettings",
);
ok(
  /ownerDecisionEnabled: boolean; \/\/ master kill switch for the 主人决策 feature \(default off\)/.test(typesSource),
  "frontend types declare the gate on SettingsView and DesktopStartupSettingsView",
);

ok(
  /\{s\.ownerDecisionEnabled && \(\s*<section className="bot-detail-section">[\s\S]*?settings\.botDecisionChannel/.test(settingsPanelSource),
  "bot settings decision-channel section is gated off",
);
ok(
  /if \(!s\.ownerDecisionEnabled\) return;[\s\S]*?app\.DecisionState\(\)[\s\S]*?onDecisionState/.test(settingsPanelSource),
  "bot settings skip decision-state subscriptions/API calls while disabled",
);

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
