// Run: tsx src/__tests__/message-media-display.test.tsx

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { MarkdownMessageImage, ReferencedMessageImages } from "../components/MessageImage";
import { LocaleProvider } from "../lib/i18n";
import { findMessageImageArtifact, markdownImageSources, referencedMessageImageArtifacts } from "../lib/messageMedia";
import type { ArtifactView } from "../lib/types";
import { useArtifactStore } from "../store/artifacts";

let passed = 0;
let failed = 0;
function ok(value: unknown, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed += 1; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed += 1; }
}

function setupDOM() {
  const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', { pretendToBeVisual: true, url: "http://localhost/" });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  return dom;
}

const artifact: ArtifactView = {
  artifactId: "img-turtle",
  name: "乌龟.png",
  type: "image",
  status: "available",
  sessionId: "tab-1",
  path: "D:\\Codex\\.codex\\generated_images\\乌龟.png",
  relativePath: "乌龟.png",
};

console.log("\nmessage media display");

const markdownSource = readFileSync(resolve(import.meta.dirname ?? ".", "../components/MarkdownRenderer.tsx"), "utf8");
const messageSource = readFileSync(resolve(import.meta.dirname ?? ".", "../components/Message.tsx"), "utf8");

ok(findMessageImageArtifact([artifact], "tab-1", "D:/Codex/.codex/generated_images/%E4%B9%8C%E9%BE%9F.png")?.artifactId === artifact.artifactId, "matches encoded slash-normalized local path");
ok(findMessageImageArtifact([artifact], "other-tab", artifact.path!) === undefined, "does not cross session boundary");
ok(markdownImageSources(`![结果](<${artifact.path}>)`).length === 1, "extracts Markdown image source");
ok(referencedMessageImageArtifacts([artifact], "tab-1", `图片保存在：${artifact.path}`).length === 1, "finds bare local image path in reply");
ok(referencedMessageImageArtifacts([artifact], "tab-1", `![结果](<${artifact.path}>)`).length === 0, "does not duplicate Markdown image as bare reference");
ok(markdownSource.includes('img: ({ src, alt }) => <MarkdownMessageImage'), "Markdown renderer delegates images to conversation media component");
ok(markdownSource.includes("urlTransform={messageUrlTransform}"), "Markdown image URL transform preserves supported local image references");
ok(messageSource.includes('<ReferencedMessageImages text={item.text} tabId={tabId} />'), "assistant replies auto-preview referenced registered images");

{
  setupDOM();
  useArtifactStore.getState().clearAllArtifacts();
  useArtifactStore.getState().upsertArtifact(artifact);
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<LocaleProvider><MarkdownMessageImage tabId="tab-1" source={artifact.path!} alt="生成的乌龟" /></LocaleProvider>);
  });
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 30)); });
  ok(document.querySelector(".message-image__preview img") !== null, "renders registered local Markdown image inside conversation");
  ok(document.querySelectorAll(".message-image").length === 1, "renders Markdown image once");
  await act(async () => root.unmount());
}

{
  setupDOM();
  useArtifactStore.getState().clearAllArtifacts();
  useArtifactStore.getState().upsertArtifact(artifact);
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <ReferencedMessageImages tabId="tab-1" text={`图片保存在：${artifact.path}`} />
      </LocaleProvider>,
    );
  });
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 30)); });
  ok(document.querySelector(".message-images .message-image__preview img") !== null, "auto-previews a registered image path mentioned in assistant reply");
  await act(async () => root.unmount());
}

{
  setupDOM();
  useArtifactStore.getState().clearAllArtifacts();
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<LocaleProvider><MarkdownMessageImage tabId="tab-1" source="https://example.com/image.png" alt="remote" /></LocaleProvider>);
  });
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
  ok(document.querySelector<HTMLImageElement>(".message-image__preview img")?.src === "https://example.com/image.png", "renders HTTP(S) Markdown image");
  await act(async () => root.unmount());
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
