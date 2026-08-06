import { lazy, memo, Suspense } from "react";

const MarkdownRenderer = lazy(() => import("./MarkdownRenderer"));

export const Markdown = memo(function Markdown({
  text,
  plainStatusBlocks = false,
  tabId,
}: {
  text: string;
  plainStatusBlocks?: boolean;
  tabId?: string;
}) {
  return (
    <Suspense fallback={<div className="md">{text}</div>}>
      <MarkdownRenderer text={text} plainStatusBlocks={plainStatusBlocks} tabId={tabId} />
    </Suspense>
  );
});
