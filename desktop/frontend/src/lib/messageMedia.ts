import type { ArtifactView } from "./types";

const REMOTE_IMAGE_RE = /^https?:\/\//i;
const IMAGE_EXT_RE = /\.(?:png|jpe?g|gif|webp|bmp|svg|ico)(?:[?#].*)?$/i;
const MARKDOWN_IMAGE_RE = /!\[[^\]\r\n]*\]\((?:<([^>\r\n]+)>|([^\s)]+))(?:\s+["'][^"']*["'])?\)/g;

function decodeSource(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

/** Normalizes display-only image references; authorization still happens by artifact ID. */
export function normalizeMessageImageSource(value: string): string {
  let source = decodeSource(value.trim().replace(/^<|>$/g, ""));
  if (/^file:\/\//i.test(source)) {
    source = source.replace(/^file:\/\/+/, "");
  }
  source = source.replace(/\\/g, "/");
  if (/^\/[a-z]:\//i.test(source)) source = source.slice(1);
  while (source.startsWith("./")) source = source.slice(2);
  return source.toLocaleLowerCase();
}

export function isRemoteMessageImage(source: string): boolean {
  return REMOTE_IMAGE_RE.test(source.trim());
}

export function isMessageImageSource(source: string): boolean {
  const value = source.trim();
  return isRemoteMessageImage(value) || IMAGE_EXT_RE.test(value);
}

function artifactSources(artifact: ArtifactView): string[] {
  return [artifact.path, artifact.relativePath]
    .filter((value): value is string => Boolean(value?.trim()))
    .map(normalizeMessageImageSource);
}

export function findMessageImageArtifact(
  artifacts: Iterable<ArtifactView>,
  tabId: string,
  source: string,
): ArtifactView | undefined {
  const target = normalizeMessageImageSource(source);
  if (!target || isRemoteMessageImage(target)) return undefined;
  return [...artifacts].find((artifact) =>
    artifact.sessionId === tabId &&
    artifact.type === "image" &&
    artifact.status === "available" &&
    artifactSources(artifact).includes(target),
  );
}

export function markdownImageSources(text: string): string[] {
  const sources: string[] = [];
  for (const match of text.matchAll(MARKDOWN_IMAGE_RE)) {
    const source = (match[1] || match[2] || "").trim();
    if (source) sources.push(source);
  }
  return sources;
}

/** Finds local image artifacts mentioned as bare paths, excluding Markdown images. */
export function referencedMessageImageArtifacts(
  artifacts: Iterable<ArtifactView>,
  tabId: string,
  text: string,
): ArtifactView[] {
  const records = [...artifacts].filter((artifact) =>
    artifact.sessionId === tabId && artifact.type === "image" && artifact.status === "available",
  );
  const markdownIds = new Set(
    markdownImageSources(text)
      .map((source) => findMessageImageArtifact(records, tabId, source)?.artifactId)
      .filter((id): id is string => Boolean(id)),
  );
  const haystack = normalizeMessageImageSource(text);
  return records.filter((artifact) => {
    if (markdownIds.has(artifact.artifactId)) return false;
    return artifactSources(artifact).some((source) => source.includes("/") && haystack.includes(source));
  });
}
