const DISCUSSION_BLOCK_PREFIX = 'v2-node-';

/** Stable cross-runtime fallback identity for a V2 node's discussion Block. */
export function v2DiscussionBlockId(nodeId: string): string {
  const bytes = new TextEncoder().encode(nodeId);
  let hex = '';
  for (const byte of bytes) hex += byte.toString(16).padStart(2, '0');
  return `${DISCUSSION_BLOCK_PREFIX}${hex}`;
}
