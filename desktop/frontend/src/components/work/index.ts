// Work components — M1 double-sided WorkCard UI.
//
// Public API surface:
//   WorkCard          — double-sided card container (both faces always mounted)
//   WorkCardFront     — structured workflow view (BlockHost, workflow, conclusions, run progress)
//   WorkCardBack      — session surface adapters (Transcript, Run, Composer, etc.)
//   WorkFlipControl   — accessible animated flip toggle
//   WorkWorkspace     — fixed outer header, Cornerstone, AddOn and global actions
//   RunProgressIndicator — nested Runs → Stages → Tasks → Attempts tree with retry

export { WorkCard } from './WorkCard';
export { WorkCardFront } from './WorkCardFront';
export { WorkChatInput } from './WorkChatInput';
export { WorkCardBack } from './WorkCardBack';
export { WorkFlipControl } from './WorkFlipControl';
export { WorkWorkspace } from './WorkWorkspace';
export { RunProgressIndicator } from './RunProgressIndicator';
export { CornerstoneDrawer } from './CornerstoneDrawer';
export { WorkRunEntry } from './WorkRunEntry';

export type { WorkCardProps, WorkDeepLink } from './WorkCard';
export type { WorkCardFrontProps } from './WorkCardFront';
export type { WorkChatInputProps } from './WorkChatInput';
export type {
  WorkCardBackProps,
  WorkCardBackSlot,
  WorkCardBackSlotProps,
  WorkCardBackSlots,
} from './WorkCardBack';
export type { WorkFlipControlProps } from './WorkFlipControl';
export type { WorkWorkspaceProps } from './WorkWorkspace';
export type { RunProgressIndicatorProps } from './RunProgressIndicator';
export type { CornerstoneDrawerProps } from './CornerstoneDrawer';
export type { WorkRunEntryProps } from './WorkRunEntry';
