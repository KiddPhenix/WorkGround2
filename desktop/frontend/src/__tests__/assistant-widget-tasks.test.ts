import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const component = readFileSync(resolve(import.meta.dirname, "../components/widget/DesktopIconMode.tsx"), "utf8");
const bridge = readFileSync(resolve(import.meta.dirname, "../lib/bridge.ts"), "utf8");
const projectTree = readFileSync(resolve(import.meta.dirname, "../components/ProjectTree.tsx"), "utf8");
const types = readFileSync(resolve(import.meta.dirname, "../lib/types.ts"), "utf8");
const backend = readFileSync(resolve(import.meta.dirname, "../../../widget_icon_mode.go"), "utf8");

assert.match(bridge, /kind: "subagent" \| "background" \| "cli" \| "assist"/);
assert.match(bridge, /assistantTasks: DesktopIconDelegation\[\]/);
assert.match(backend, /AssistantTasks\s+\[\]DesktopIconDelegation\s+`json:"assistantTasks"`/);
assert.match(backend, /item\.Kind == agent\.SessionSourceAssist[\s\S]+snapshot\.AssistantTasks = append/);
assert.match(backend, /entry\.id == "assistant" && assistantRunning > 0[\s\S]+status, count = "running", assistantRunning/);
assert.match(component, /item\.sourceId === "assistant"[\s\S]{0,260}setActiveID\(item\.id\)/);
assert.match(component, /active\.sourceId === "assistant"[\s\S]+items=\{snapshot\.assistantTasks \|\| \[\]\}[\s\S]+open_assistant_task/);
assert.match(component, /desktopIcon\.assistantTasks\.open[\s\S]+onOpenAssistant/);
assert.match(projectTree, /source === "assist"[\s\S]{0,180}label: "ASSIST"/);
assert.match(projectTree, /source === "collaboration" \|\| source === "assist"/);
assert.match(types, /sessionKind\?: "normal" \| "work" \| "collaboration" \| "assistant"/);

console.log("\nassistant widget tasks\n  PASS  assist tasks own the Assistant popup and remain visible in Session List\n");
