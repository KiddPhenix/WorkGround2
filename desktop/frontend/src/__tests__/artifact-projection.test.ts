import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const testDir = dirname(fileURLToPath(import.meta.url));
const bridgeSource = readFileSync(resolve(testDir, "../lib/bridge.ts"), "utf8");
const controllerSource = readFileSync(resolve(testDir, "../lib/useController.ts"), "utf8");
const storeSource = readFileSync(resolve(testDir, "../store/artifacts.ts"), "utf8");

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed++;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed++;
  }
}

process.stdout.write("\nartifact projection contract\n\n");

ok(
  bridgeSource.includes("ArtifactsForTab(tabID: string): Promise<ArtifactView[]>") &&
    bridgeSource.includes("async ArtifactsForTab()"),
  "bridge exposes the host artifact projection with a browser-safe mock",
);
ok(
  controllerSource.includes('applyAncillary("artifacts", () => app.ArtifactsForTab(tabId)') &&
    controllerSource.includes('e.kind === "tool_result" && toolMayProduceArtifacts(e.tool?.name)') &&
    controllerSource.includes('e.kind === "turn_done"'),
  "controller refreshes artifacts during hydration and after producing tools or turn completion",
);
ok(
  controllerSource.includes('addBreadcrumb("artifact.refresh", `failed ${tabId}: ${errorMessage(err)}`)'),
  "artifact refresh failures remain observable without breaking the session",
);
ok(
  storeSource.includes("replaceSessionArtifacts: (sessionId, records)") &&
    storeSource.includes("if (artifact.sessionId !== sessionId) artifacts[id] = artifact"),
  "artifact store atomically replaces one session while preserving the others",
);

const total = passed + failed;
process.stdout.write(`\n${total} tests · ${passed} passed · ${failed} failed\n`);
if (failed > 0) process.exit(1);
