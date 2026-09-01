import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const read = (path: string) => readFileSync(new URL(path, import.meta.url), "utf8");
const panel = read("../components/SettingsPanel.tsx");
const types = read("../lib/types.ts");
const bridge = read("../lib/bridge.ts");
const models = read("../../wailsjs/go/models.ts");
const appDts = read("../../wailsjs/go/main/App.d.ts");
const appJs = read("../../wailsjs/go/main/App.js");

// Frontend type contract: the settings payload carries the full catalog with the
// exact product labels plus the current selection.
assert.match(types, /export interface AgentPromptStyleView \{[\s\S]+id: string;[\s\S]+disorder: string;[\s\S]+styleName: string;[\s\S]+capability: string;[\s\S]+selected: boolean;/, "types declare the AgentPromptStyleView contract");
assert.match(types, /interface SettingsView \{[\s\S]+agentPromptStyles: AgentPromptStyleView\[\]/, "SettingsView carries the Agent 风格 catalog");
assert.match(types, /"global" \| "styles"/, "SettingsTab includes the styles leaf page");

// Bridge contract: a single setter that persists the whole selection.
assert.match(bridge, /SetAgentPromptStyles\(ids: string\[\]\): Promise<void>/, "bridge exposes SetAgentPromptStyles");

// Settings navigation + multi-select draft + single apply/clear.
assert.match(panel, /id: "aiConfig", tabs: \["models", "styles", "memory", "global"\]/, "Agent 风格 lives under Settings → AI 配置");
assert.match(panel, /tab === "styles" && s && <SettingsPageShell[\s\S]+<AgentStylesSection/, "the styles tab renders the picker section");
assert.match(panel, /app\.SetAgentPromptStyles\(\[\.\.\.draft\]\)/, "Apply persists the whole multi-select draft once");
assert.match(panel, /app\.SetAgentPromptStyles\(\[\]\)/, "Clear persists an empty selection");
assert.match(panel, /type="checkbox"/, "each style is a checkbox");
assert.match(panel, /settings-style-card__title">\{style\.disorder\}/, "the card title directly renders the 病名");
assert.match(panel, /settings-style-card__name">\{style\.styleName\}/, "the card separately renders the style name");
assert.match(panel, /style\.capability\}/, "the card renders the capability text");
assert.doesNotMatch(panel, /onChange=\{\(\) => (?:void )?app\.SetAgentPromptStyles/, "checkbox clicks must not rebuild on every change");
assert.doesNotMatch(bridge, /所有怀疑必须给出证据|仍需考虑人的感受|设置完成标准和时间上限/, "mock catalog omits fallback clauses");

// Generated Wails binding contract.
assert.match(models, /export class AgentPromptStyleView \{/, "models.ts declares AgentPromptStyleView");
assert.match(models, /this\.id = source\["id"\];[\s\S]+this\.disorder = source\["disorder"\];[\s\S]+this\.styleName = source\["styleName"\];[\s\S]+this\.capability = source\["capability"\];[\s\S]+this\.selected = source\["selected"\];/, "models.ts projects every AgentPromptStyleView field");
assert.match(models, /agentPromptStyles: AgentPromptStyleView\[\];/, "models.ts SettingsView declares agentPromptStyles");
assert.match(models, /this\.agentPromptStyles = this\.convertValues\(source\["agentPromptStyles"\], AgentPromptStyleView\);/, "models.ts projects agentPromptStyles");
assert.match(appDts, /export function SetAgentPromptStyles\(arg1:Array<string>\):Promise<void>;/, "App.d.ts declares SetAgentPromptStyles");
assert.match(appJs, /export function SetAgentPromptStyles\(arg1\) \{[\s\S]+window\['go'\]\['main'\]\['App'\]\['SetAgentPromptStyles'\]\(arg1\);/, "App.js routes SetAgentPromptStyles");

// Catalog labels stay exact Chinese in every locale (UI strings translate, the
// product data does not).
for (const locale of ["en", "zh", "zh-TW"]) {
  const source = read(`../locales/${locale}.ts`);
  for (const key of ["settings.tab.styles", "settings.pageDesc.styles", "settings.styles.title", "settings.styles.desc", "settings.styles.apply", "settings.styles.clear"]) {
    assert.ok(source.includes(`"${key}"`), `${locale} includes ${key}`);
  }
}

console.log("agent prompt styles contract tests passed");
