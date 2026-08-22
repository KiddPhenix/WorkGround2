const DISCUSSION_BLOCK_PREFIX = 'v2-node-';

export interface DiscussionBlockIdentity {
  id: string;
  kind: string;
  revision: number;
  source: {
    provider: string;
    mode: string;
    verified?: boolean;
  };
}

/** Stable cross-runtime fallback identity for a V2 node's discussion Block. */
export function v2DiscussionBlockId(nodeId: string): string {
  const bytes = new TextEncoder().encode(nodeId);
  let hex = '';
  for (const byte of bytes) hex += byte.toString(16).padStart(2, '0');
  return `${DISCUSSION_BLOCK_PREFIX}${hex}`;
}

/** True for the controller-owned revision-1 node summary used only to anchor
 * discussion. A block-scoped edit advances the revision and becomes normal
 * presentation content. */
export function isAutomaticV2DiscussionBlock(block: DiscussionBlockIdentity): boolean {
  return block.id.startsWith(DISCUSSION_BLOCK_PREFIX)
    && block.kind === 'markdown'
    && block.revision === 1
    && block.source.provider === 'controller'
    && block.source.mode === 'snapshot'
    && block.source.verified === true;
}
