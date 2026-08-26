import { assistantTemplate, assistantTemplateContent, templateRoutine, templateRoutines } from "../custom/features/assistant/assistant.templates";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

console.log("\nassistant templates");
const zh = assistantTemplateContent("zh");
const code = assistantTemplate("code", "zh");
const general = assistantTemplate("general", "zh");
const promo = assistantTemplate("promo", "zh");

ok(code.available, "code-project template is selectable");
ok(general.available, "general template is selectable");
ok(promo.available, "promotion template is selectable in phase 4");
ok(promo.policy.network === "allow" && promo.policy.publish === "approve", "promotion template reads the network while keeping outbound approval");
ok(promo.routines.length === 3 && promo.routines.some((routine) => routine.title === "效果复盘"), "promotion template includes content, metrics, and reply routines");
ok(code.defaultName !== "" && code.mission !== "", "code template fills name and mission");
ok(code.policy.local_write === "allow" && code.policy.publish === "approve", "code template allows local writes but keeps publishing approval");
ok(code.routines.length === 3, "code template carries health / test-build / release-readiness routines");
ok(code.routines.some((routine) => routine.title === "发布准备检查"), "code template includes the release-readiness routine");
ok(code.routines.every((routine) => routine.schedule.kind !== "manual"), "code template routines are scheduled, not manual-only");

ok(general.mission === "" && general.routines.length === 0, "general template leaves mission and routines to the user");
ok(general.policy.local_write === "deny" && general.policy.network === "deny", "general template defaults to read-only");

const createdAt = "2026-08-20T00:00:00Z";
const routines = templateRoutines("assistant-1", code, createdAt);
ok(routines.length === 3 && routines.every((routine) => routine.assistant_id === "assistant-1"), "template routines are bound to the assistant");
ok(routines.every((routine) => routine.revision === 0 && routine.enabled), "template routines are ready for the stable mutation ledger/CAS path");
ok(new Set(routines.map((routine) => routine.id)).size === 3, "template routine ids are unique");

const manualRoutine = templateRoutine("assistant-1", "routine-1", "自定义", "做点事", { kind: "manual", timezone: "UTC" }, createdAt);
ok(manualRoutine.id === "routine-1" && manualRoutine.assistant_id === "assistant-1" && manualRoutine.enabled, "general template builds one user-filled routine with a stable intent id");

// Locale correctness: English content must not leak simplified-Chinese prompts.
const enCode = assistantTemplate("code", "en");
ok(enCode.mission.includes("Track project health"), "English template localizes the mission");
ok(enCode.routines.every((routine) => !/[\u4e00-\u9fff]/.test(routine.title) && !/[\u4e00-\u9fff]/.test(routine.prompt)), "English template does not write simplified-Chinese prompts");
ok(zh[0].mission.includes("项目健康度"), "Chinese template keeps the localized mission");

console.log(`\n${passed} passed, ${failed} failed\n`);
if (failed) process.exit(1);
