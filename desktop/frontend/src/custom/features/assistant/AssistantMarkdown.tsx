import { useMemo } from "react";
import { Markdown } from "../../../components/Markdown";

export type AssistantMarkdownSection =
  | { kind: "content"; markdown: string }
  | { kind: "collapsed"; title: string; markdown: string };

interface SectionMarker {
  index: number;
  kind: "heading" | "label";
  level: number;
  title: string;
  collapsible: boolean;
}

const COLLAPSED_SECTION_TITLES = new Set([
  "取证证据",
  "取证过程",
  "证据",
  "证据明细",
  "事实依据",
  "依据",
  "来源",
  "参考资料",
  "说明",
  "详细说明",
  "补充说明",
  "执行说明",
  "方法说明",
  "调查记录",
  "执行记录",
  "工具调用",
  "工具调用记录",
  "evidence",
  "sources",
  "source",
  "references",
  "reference",
  "notes",
  "note",
  "details",
  "explanation",
  "methodology",
  "audit trail",
  "tool calls",
]);

function cleanSectionTitle(value: string): string {
  return value
    .replace(/\s+#+\s*$/, "")
    .replace(/^[*_`~]+|[*_`~]+$/g, "")
    .replace(/^\s*(?:(?:第?[一二三四五六七八九十百]+[章节部分]?)|(?:[（(]?[0-9]+[)）]?))[、.．:：\s-]+/, "")
    .replace(/[：:]\s*$/, "")
    .trim();
}

export function isCollapsedAssistantSection(title: string): boolean {
  const normalized = cleanSectionTitle(title).toLocaleLowerCase();
  return COLLAPSED_SECTION_TITLES.has(normalized);
}

function sectionMarkers(lines: string[]): SectionMarker[] {
  const markers: SectionMarker[] = [];
  let fence: { marker: "`" | "~"; length: number } | null = null;

  lines.forEach((line, index) => {
    const fenceMatch = /^\s{0,3}(`{3,}|~{3,})/.exec(line);
    if (fenceMatch) {
      const marker = fenceMatch[1][0] as "`" | "~";
      if (!fence) fence = { marker, length: fenceMatch[1].length };
      else if (fence.marker === marker && fenceMatch[1].length >= fence.length) fence = null;
      return;
    }
    if (fence) return;

    const heading = /^\s{0,3}(#{1,6})\s+(.+?)\s*$/.exec(line);
    if (heading) {
      const title = cleanSectionTitle(heading[2]);
      markers.push({ index, kind: "heading", level: heading[1].length, title, collapsible: isCollapsedAssistantSection(title) });
      return;
    }

    const label = /^\s*\*\*(.+?)\*\*\s*[:：]?\s*$/.exec(line);
    if (label) {
      const title = cleanSectionTitle(label[1]);
      markers.push({ index, kind: "label", level: 7, title, collapsible: isCollapsedAssistantSection(title) });
    }
  });
  return markers;
}

function sectionEnd(marker: SectionMarker, markers: SectionMarker[], lineCount: number): number {
  for (const candidate of markers) {
    if (candidate.index <= marker.index) continue;
    if (marker.kind === "label") return candidate.index;
    if (candidate.kind === "label" || candidate.level <= marker.level) return candidate.index;
  }
  return lineCount;
}

/** Splits only standalone prose headings; fenced code remains byte-for-byte content. */
export function splitAssistantMarkdown(text: string): AssistantMarkdownSection[] {
  const lines = text.split(/\r?\n/);
  const markers = sectionMarkers(lines);
  const collapsed = markers.filter((marker) => marker.collapsible);
  if (collapsed.length === 0) return text ? [{ kind: "content", markdown: text }] : [];

  const sections: AssistantMarkdownSection[] = [];
  let cursor = 0;
  for (const marker of collapsed) {
    if (marker.index < cursor) continue;
    const before = lines.slice(cursor, marker.index).join("\n").trim();
    if (before) sections.push({ kind: "content", markdown: before });
    const end = sectionEnd(marker, markers, lines.length);
    sections.push({ kind: "collapsed", title: marker.title, markdown: lines.slice(marker.index + 1, end).join("\n").trim() });
    cursor = end;
  }
  const after = lines.slice(cursor).join("\n").trim();
  if (after) sections.push({ kind: "content", markdown: after });
  return sections;
}

export function AssistantMarkdown({ text }: { text: string }) {
  const sections = useMemo(() => splitAssistantMarkdown(text), [text]);
  return (
    <div className="assistant-markdown">
      {sections.map((section, index) => section.kind === "content" ? (
        <Markdown key={`content-${index}`} text={section.markdown} />
      ) : (
        <details className="assistant-markdown__fold" key={`fold-${index}-${section.title}`}>
          <summary>{section.title}</summary>
          {section.markdown && <div className="assistant-markdown__fold-body"><Markdown text={section.markdown} /></div>}
        </details>
      ))}
    </div>
  );
}
