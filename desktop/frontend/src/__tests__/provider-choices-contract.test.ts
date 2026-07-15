// Run: tsx src/__tests__/provider-choices-contract.test.ts

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { en } from "../locales/en";
import { zh } from "../locales/zh";
import { zhTW } from "../locales/zh-TW";

let passed = 0;
let failed = 0;

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed++;
    return;
  }
  process.stderr.write(`  FAIL  ${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}\n`);
  failed++;
}

function contains(source: string, expected: string, label: string) {
  eq(source.includes(expected), true, label);
}

const settingsSource = readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), "../components/SettingsPanel.tsx"),
  "utf8",
);

console.log("\nofficial provider choices contract");

for (const [kind, keyEnv] of [
  ["zhipuqingyan", "ZHIPU_API_KEY"],
  ["doubao", "ARK_API_KEY"],
  ["qwen-ollama", ""],
] as const) {
  contains(settingsSource, `kind: "${kind}"`, `${kind} choice is wired`);
  contains(settingsSource, `keyEnv: "${keyEnv}"`, `${kind} key contract is wired`);
}
contains(settingsSource, "{selected.keyEnv && (", "keyless local Qwen hides the API key field");

const localeCases: Array<{
  name: string;
  dict: Record<string, string>;
  values: Record<string, string>;
}> = [
  {
    name: "en",
    dict: en,
    values: {
      "settings.addProvider.official.zhipuqingyan": "ZhipuQingYan",
      "settings.addProvider.official.doubao": "Doubao",
      "settings.addProvider.official.qwenOllama": "Qwen (Ollama)",
      "settings.providerLabel.zhipuqingyan": "ZhipuQingYan",
      "settings.providerLabel.doubao": "Doubao",
      "settings.providerLabel.qwenOllama": "Qwen (Ollama)",
    },
  },
  {
    name: "zh",
    dict: zh,
    values: {
      "settings.addProvider.official.zhipuqingyan": "智谱清言",
      "settings.addProvider.official.doubao": "豆包",
      "settings.addProvider.official.qwenOllama": "Qwen (Ollama)",
      "settings.providerLabel.zhipuqingyan": "智谱清言",
      "settings.providerLabel.doubao": "豆包",
      "settings.providerLabel.qwenOllama": "Qwen (Ollama)",
    },
  },
  {
    name: "zh-TW",
    dict: zhTW,
    values: {
      "settings.addProvider.official.zhipuqingyan": "智譜清言",
      "settings.addProvider.official.doubao": "豆包",
      "settings.addProvider.official.qwenOllama": "Qwen (Ollama)",
      "settings.providerLabel.zhipuqingyan": "智譜清言",
      "settings.providerLabel.doubao": "豆包",
      "settings.providerLabel.qwenOllama": "Qwen (Ollama)",
    },
  },
];

for (const locale of localeCases) {
  for (const [key, expected] of Object.entries(locale.values)) {
    eq(locale.dict[key], expected, `${locale.name}: ${key}`);
  }
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
