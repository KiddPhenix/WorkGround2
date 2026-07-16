import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { RequestHelpCard } from "../components/RequestHelpCard";
import { LocaleProvider } from "../lib/i18n";
import { REQUEST_HELP_PROGRESS_PREFIX, REQUEST_HELP_SUMMARY_PREFIX, applyRequestHelpProgress, finishRequestHelp, requestHelpFromArgs } from "../lib/requestHelp";
import { historyMessagesToItems, initialState, reducer } from "../lib/useController";

let passed = 0;
let failed = 0;
function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed++; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed++; }
}
function eq(actual: unknown, expected: unknown, label: string) {
  ok(actual === expected, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}
function progress(state: string, model: string, attempt: number, total = 2, error?: string) {
  return `${REQUEST_HELP_PROGRESS_PREFIX}${JSON.stringify({ version: 1, state, request_id: "assist-1", capability: "web_search", from_model: "deepseek/deepseek-pro", model, attempt, total, error })}\n`;
}

console.log("\nrequest_help display state");

{
  let status = requestHelpFromArgs(`{"capability":"web_search","prompt":"search"}`);
  eq(status.phase, "selecting", "dispatch starts in selecting state");
  status = applyRequestHelpProgress(status, progress("attempting", "codex/codex-cli", 1));
  eq(status.phase, "attempting", "first candidate is attempting");
  eq(status.fromModel, "deepseek/deepseek-pro", "source model is retained");
  eq(status.model, "codex/codex-cli", "candidate model is visible");
  status = applyRequestHelpProgress(status, progress("candidate_failed", "codex/codex-cli", 1, 2, "offline"));
  eq(status.phase, "switching", "retryable failure becomes switching");
  status = applyRequestHelpProgress(status, progress("attempting", "gemini/gemini-cli", 2));
  eq(status.model, "gemini/gemini-cli", "late candidate replaces stale model");
  status = finishRequestHelp(status, "Capability assist succeeded\nrequest_id: assist-1\ncapability: web_search\nfrom_model: deepseek/deepseek-pro\nmodel: gemini/gemini-cli\nattempt: 2/2\n");
  eq(status.phase, "completed", "success result completes the card");
  eq(status.attempt, 2, "success keeps final attempt");
}

{
  let state = reducer(initialState, { type: "event", e: { kind: "tool_dispatch", tool: { id: "help-1", name: "request_help", args: `{"capability":"web_search","prompt":"search"}`, readOnly: false } } });
  state = reducer(state, { type: "event", e: { kind: "tool_progress", tool: { id: "help-1", output: progress("attempting", "codex/codex-cli", 1, 1) } } });
  state = reducer(state, { type: "event", e: { kind: "tool_result", tool: { id: "help-1", name: "request_help", output: "Capability assist succeeded\nrequest_id: assist-1\ncapability: web_search\nfrom_model: deepseek/deepseek-pro\nmodel: codex/codex-cli\nattempt: 1/1\n", readOnly: false } } });
  const item = state.items.find((value) => value.kind === "tool" && value.id === "help-1");
  ok(item?.kind === "tool" && item.assistStatus?.phase === "completed", "reducer keeps completed assist state");
  ok(item?.kind === "tool" && item.output === undefined && item.dataArchived === true, "assist survives normal output archival");
}

{
  const summary = `${REQUEST_HELP_SUMMARY_PREFIX}${JSON.stringify({ version: 1, state: "completed", capability: "image_generation", from_model: "deepseek/deepseek-pro", model: "codex/codex-cli", attempt: 1, total: 1 })}`;
  const { items } = historyMessagesToItems([
    { role: "assistant", content: "", toolCalls: [{ id: "help-history", name: "request_help", arguments: `{"capability":"image_generation"}`, argumentsArchived: true, summary }] },
    { role: "tool", content: "", toolCallId: "help-history", toolName: "request_help", toolResultArchived: true },
  ] as any, "history-");
  const item = items.find((value) => value.kind === "tool");
  ok(item?.kind === "tool" && item.assistStatus?.model === "codex/codex-cli", "archived history restores target model from summary");
  ok(item?.kind === "tool" && item.assistStatus?.phase === "completed", "archived history restores completed phase");
}

{
  const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/" });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.localStorage = dom.window.localStorage;
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<LocaleProvider><RequestHelpCard status={{ phase: "attempting", capability: "web_search", fromModel: "deepseek/deepseek-pro", model: "codex/codex-cli", attempt: 1, total: 2 }} args="{}" entranceId="help-card" /></LocaleProvider>);
  });
  ok(Boolean(document.querySelector(".request-help[aria-live='polite']")), "card exposes persistent live status");
  ok(document.body.textContent?.includes("deepseek/deepseek-pro") === true, "card renders source model");
  ok(document.body.textContent?.includes("codex/codex-cli") === true, "card renders target model");
  await act(async () => root.unmount());
}

{
  const styles = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "../styles.css"), "utf8");
  ok(styles.includes("@media (max-width: 620px)"), "card has narrow-width layout");
  ok(styles.includes("@media (prefers-reduced-motion: reduce)"), "card respects reduced motion");
}

// ── RequestHelpCard: image preview moved to ArtifactImagesForTool ──
{
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  const artifact = { task_id: "img-ui", path: "C:\\images\\result.png", mime: "image/png", size: 128, width: 3, height: 2 };
  await act(async () => {
    root.render(<LocaleProvider><RequestHelpCard status={{ phase: "completed", capability: "image_generation", artifact }} args="{}" entranceId="image-card" /></LocaleProvider>);
    await Promise.resolve();
  });
  ok(document.querySelector(".request-help__title") !== null, "status card still renders title");
  ok(document.querySelector(".request-help__image") === null, "no inline image preview (moved to ArtifactImageCard)");
  ok(document.querySelector(".request-help__overlay") === null, "no inline overlay (moved to ArtifactImageCard)");
  await act(async () => root.unmount());
  host.remove();
}

// ── artifact parsing ──
{
  // finishRequestHelp picks up artifact from summary wire.
  const summary = `${REQUEST_HELP_SUMMARY_PREFIX}${JSON.stringify({
    version: 1, state: "completed", request_id: "assist-img", capability: "image_generation",
    from_model: "deepseek/deepseek-pro", model: "img/gen-img", attempt: 1, total: 1,
    artifact: { task_id: "img-1", path: "/tmp/test.png", mime: "image/png", size: 1024, width: 640, height: 480 },
  })}\n`;
  const status = finishRequestHelp(requestHelpFromArgs(`{"capability":"image_generation"}`), "", undefined, summary);
  eq(status.phase, "completed", "artifact: summary restores completed phase");
  eq(status.artifact?.task_id, "img-1", "artifact: task_id from summary");
  eq(status.artifact?.path, "/tmp/test.png", "artifact: path from summary");
  eq(status.artifact?.width, 640, "artifact: width from summary");
  eq(status.artifact?.height, 480, "artifact: height from summary");
}

{
  // finishRequestHelp picks up artifact from final output wire.
  const output = `Capability assist succeeded
request_id: assist-img2
capability: image_generation
from_model: a/b
model: c/d
attempt: 1/1
artifact: {"task_id":"img-2","path":"/tmp/out.png","mime":"image/jpeg","size":512,"width":800,"height":600}

result`;
  const status = finishRequestHelp(requestHelpFromArgs(`{"capability":"image_generation"}`), output);
  eq(status.phase, "completed", "artifact: final output restores completed");
  eq(status.artifact?.task_id, "img-2", "artifact: task_id from final output");
  eq(status.artifact?.mime, "image/jpeg", "artifact: mime from final output");
  eq(status.artifact?.width, 800, "artifact: width from final output");
}

{
  // Corrupt artifact in final output is tolerated — phase still completes.
  const output = `Capability assist succeeded
request_id: assist-bad
capability: image_generation
from_model: a/b
model: c/d
attempt: 1/1
artifact: not-json

result`;
  const status = finishRequestHelp(requestHelpFromArgs(`{"capability":"image_generation"}`), output);
  eq(status.phase, "completed", "artifact: corrupt artifact still completes");
  ok(status.artifact === undefined, "artifact: corrupt artifact leaves artifact undefined");
}

{
  // Later event without artifact does not clear already-known artifact.
  const summary = `${REQUEST_HELP_SUMMARY_PREFIX}${JSON.stringify({
    version: 1, state: "completed", request_id: "assist-img", capability: "image_generation",
    artifact: { task_id: "img-3", path: "/tmp/a.png", mime: "image/png", size: 1, width: 1, height: 1 },
  })}\n`;
  let status = finishRequestHelp(requestHelpFromArgs(`{"capability":"image_generation"}`), "", undefined, summary);
  eq(status.artifact?.task_id, "img-3", "artifact: initially set");
  // Apply a second wire with no artifact.
  const emptySummary = `${REQUEST_HELP_SUMMARY_PREFIX}${JSON.stringify({
    version: 1, state: "completed", request_id: "assist-img", capability: "image_generation",
  })}\n`;
  status = finishRequestHelp(status, "", undefined, emptySummary);
  eq(status.artifact?.task_id, "img-3", "artifact: not cleared by later event without artifact");
}

{
  // web_search output never contains artifact.
  const output = `Capability assist succeeded
request_id: assist-web
capability: web_search
from_model: a/b
model: c/d
attempt: 1/1

search result with http://example.com`;
  const status = finishRequestHelp(requestHelpFromArgs(`{"capability":"web_search"}`), output);
  eq(status.phase, "completed", "web_search: completes");
  ok(status.artifact === undefined, "web_search: no artifact");
}

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
