import React, { type ComponentProps } from 'react';

import { Transcript } from './Transcript';
import { Composer } from './Composer';
import { ApprovalModal } from './ApprovalModal';
import { AskCard } from './AskCard';
import { ClearContextCard } from './ClearContextCard';
import { SessionRunStream, SessionArtifactShelf, SessionQueueTray, SessionConfigBar } from './desktop-ui/IrisInfoComponents';
import { Tooltip } from './Tooltip';
import { SessionBackground } from './SessionBackground';
import { SessionMemoryBar } from './desktop-ui/IrisInfoComponents';
import { SessionStatusIndicators } from './SessionStatusIndicators';
import { AddOnLauncherButton } from './desktop-ui/IrisInfoComponents';
import { ArrowLeft, PanelRight, Command } from 'lucide-react';

import type {
  CollaborationMode,
  ComposerInsertRequest,
  ComposerSubmitKey,
  TabMeta,
  TokenMode,
  ToolApprovalMode,
} from '../lib/types';
import type { Translator } from '../lib/i18n';

// ── Props ───────────────────────────────────────────────────────────

type TranscriptProps = ComponentProps<typeof Transcript>;
type ComposerProps = ComponentProps<typeof Composer>;
type ApprovalProps = ComponentProps<typeof ApprovalModal>;
type AskProps = ComponentProps<typeof AskCard>;
type ConfigBarProps = ComponentProps<typeof SessionConfigBar>;

export interface SessionSurfaceProps {
  variant?: 'workspace' | 'work';

  // Core identity
  activeTabId: string;
  activeSessionId: string | undefined;
  renderSessionId: string;
  runtimeTabMetas: TabMeta[];

  // Transcript state
  displayItems: TranscriptProps['items'];
  live: TranscriptProps['live'];
  running: boolean;
  memoryRunning: boolean;
  controllerReady: boolean;
  footerHeight: number;
  transcriptHydrating: boolean;
  transcriptRevealSignal: number;
  hasOlderHistory: boolean;
  historyStartTurn: number;
  historyOlderLoading: boolean;
  welcomeTarget: TranscriptProps['welcomeTarget'];
  actionPending: boolean;
  rewindDisabled: boolean;
  rewindSignal: number;
  checkpoints: TranscriptProps['checkpoints'];

  // Decision surface state
  approval: ApprovalProps['approval'] | undefined;
  ask: AskProps['ask'] | undefined;
  clearContextPending: boolean;

  // Composer state
  composerInsertRequest: ComposerInsertRequest | null;
  planRevisionInsertRequest: ComposerInsertRequest | null;
  composerSubmitKey: ComposerSubmitKey;
  collaborationMode: CollaborationMode;
  toolApprovalMode: ToolApprovalMode;
  tokenMode: TokenMode;
  goal: string;
  cwd: string | undefined;
  modelLabel: string;
  configModelLabel: string;
  imageInputEnabled: boolean;
  effort: ComposerProps['effort'];
  turnStartAt: number;
  turnTokens: number;
  retry: ComposerProps['retry'];
  activityStage: ComposerProps['activityStage'];
  activityStageSeed: number;
  transientOverlayDismissSignal: number;
  composerSessionKey: string;
  latestGuidanceKey: string | undefined;
  latestGuidanceText: string | undefined;
  guidanceQueueMockItems: ComposerProps['guidanceQueuePreviewItems'];
  submitDisabled: boolean;
  decisionPending: boolean;
  ready: boolean;
  readOnly: boolean;
  composerDisabled: boolean;
  workSendAvailable: boolean;
  workSendSelected: boolean;

  // Header state
  sidebarCollapsed: boolean;
  sidebarToggleTitle: string;
  sidebarTogglePressed: boolean;
  headerTitle: string;
  irisFixtureActive: boolean;
  sidebarImDetailConnection: { title: string } | null;
  workReturn?: {
    label: string;
    pending: boolean;
    onReturn: () => void;
  };

  // Context
  contextPercent: number;
  runtimeMode: ConfigBarProps['runtimeMode'];
  foregroundActive: boolean;

  // Callbacks
  onSend: ComposerProps['onSend'];
  onSendAsWork: NonNullable<ComposerProps['onSendAsWork']>;
  onWorkSendChange: NonNullable<ComposerProps['onWorkSendChange']>;
  onCancel: ComposerProps['onCancel'];
  onApprove: (id: string, allow: boolean, session: boolean, persist: boolean) => void;
  onAnswerQuestion: AskProps['onAnswer'];
  onTranscriptPrompt: TranscriptProps['onPrompt'];
  onWelcomeDraft: TranscriptProps['onWelcomeDraft'];
  onEditPrompt: TranscriptProps['onEditPrompt'];
  onRewind: TranscriptProps['onRewind'];
  onPinMemory: TranscriptProps['onPinMemory'];
  onLoadOlderHistory: TranscriptProps['onLoadOlderHistory'];
  onCycleMode: ComposerProps['onCycleMode'];
  onSetMode: ComposerProps['onSetMode'];
  onSetCollaborationMode: ComposerProps['onSetCollaborationMode'];
  onSetToolApprovalMode: ComposerProps['onSetToolApprovalMode'];
  onToggleYoloApprovalMode: ComposerProps['onToggleYoloApprovalMode'];
  onClearGoal: ComposerProps['onClearGoal'];
  onSwitchModel: ComposerProps['onSwitchModel'];
  onSetEffort: ComposerProps['onSetEffort'];
  onSetTokenMode: ComposerProps['onSetTokenMode'];
  onCancelClearContext: () => void;
  onConfirmClearContext: () => void;
  onToggleSidebar: () => void;
  onOpenPalette: () => void;
  onEnterWidgetMode: () => void;
  onSwitchTab: (tab: TabMeta) => void;
  onSetInsertTarget: (target: "composer" | "planRevision") => void;
  onRevisePlan: (text: string) => void;
  onExitPlan: () => Promise<void>;
  onComposerInsert: (req: ComposerInsertRequest | null) => void;

  // Refs
  conversationViewportRef: React.RefObject<HTMLDivElement | null>;
  scrollHostRef: React.RefObject<HTMLDivElement | null>;

  // Translators / UI
  t: Translator;
  widgetEnabled: boolean;
}

// ── Component ───────────────────────────────────────────────────────

export const SessionSurface: React.FC<SessionSurfaceProps> = ({
  variant = 'workspace',
  activeTabId,
  activeSessionId,
  renderSessionId,
  runtimeTabMetas,
  displayItems,
  live,
  running,
  memoryRunning,
  controllerReady,
  footerHeight,
  transcriptHydrating,
  transcriptRevealSignal,
  hasOlderHistory,
  historyStartTurn,
  historyOlderLoading,
  welcomeTarget,
  actionPending,
  rewindDisabled,
  rewindSignal,
  checkpoints,
  approval,
  ask,
  clearContextPending,
  composerInsertRequest,
  planRevisionInsertRequest,
  composerSubmitKey,
  collaborationMode,
  toolApprovalMode,
  tokenMode,
  goal,
  cwd,
  modelLabel,
  configModelLabel,
  imageInputEnabled,
  effort,
  turnStartAt,
  turnTokens,
  retry,
  activityStage,
  activityStageSeed,
  transientOverlayDismissSignal,
  composerSessionKey,
  latestGuidanceKey,
  latestGuidanceText,
  guidanceQueueMockItems,
  submitDisabled,
  decisionPending,
  ready,
  readOnly,
  composerDisabled,
  workSendAvailable,
  workSendSelected,
  sidebarCollapsed,
  sidebarToggleTitle,
  sidebarTogglePressed,
  headerTitle,
  irisFixtureActive,
  sidebarImDetailConnection,
  workReturn,
  contextPercent,
  runtimeMode,
  foregroundActive,
  onSend,
  onSendAsWork,
  onWorkSendChange,
  onCancel,
  onApprove,
  onAnswerQuestion,
  onTranscriptPrompt,
  onWelcomeDraft,
  onEditPrompt,
  onRewind,
  onPinMemory,
  onLoadOlderHistory,
  onCycleMode,
  onSetMode,
  onSetCollaborationMode,
  onSetToolApprovalMode,
  onToggleYoloApprovalMode,
  onClearGoal,
  onSwitchModel,
  onSetEffort,
  onSetTokenMode,
  onCancelClearContext,
  onConfirmClearContext,
  onToggleSidebar,
  onOpenPalette,
  onEnterWidgetMode,
  onSwitchTab,
  onSetInsertTarget,
  onRevisePlan,
  onExitPlan,
  onComposerInsert,
  conversationViewportRef,
  scrollHostRef,
  t,
  widgetEnabled,
}) => {
  const embedded = variant === 'work';
  return (
    <section
      className={`session-workspace${embedded ? ' session-workspace--work-back' : ''}`}
      aria-label={embedded ? 'Work session surface' : 'Session workspace'}
      data-testid="session-surface"
      data-session-surface-variant={variant}
    >
      {!embedded && <SessionBackground tabId={activeTabId} />}
      {!embedded && <header className="session-header">
        <div className="session-header__identity">
          {sidebarCollapsed && (
            <Tooltip label={sidebarToggleTitle}>
              <button
                className={`session-header__expand-btn${sidebarTogglePressed ? " session-header__expand-btn--pressed" : ""}`}
                type="button"
                onClick={onToggleSidebar}
                aria-label={sidebarToggleTitle}
                aria-pressed={!sidebarCollapsed}
              >
                <PanelRight size={15} aria-hidden="true" />
              </button>
            </Tooltip>
          )}
          {workReturn && (
            <button
              type="button"
              className="session-header__work-return"
              data-testid="session-work-return"
              disabled={workReturn.pending}
              aria-label={`返回 ${workReturn.label}`}
              onClick={workReturn.onReturn}
            >
              <ArrowLeft size={16} aria-hidden="true" />
              <span>{workReturn.pending ? '正在返回…' : '返回 Work'}</span>
            </button>
          )}
          <h1 className="session-header__title" title={headerTitle}>
            {headerTitle}
          </h1>
        </div>
        <div className="session-header__actions">
          <SessionStatusIndicators tabs={runtimeTabMetas} activeTabId={activeTabId} onSwitchTab={(tab) => { void onSwitchTab(tab); }} t={t} />
          <AddOnLauncherButton />
          <button type="button" className="session-header__more-btn" aria-label={t("topicBar.command")} onClick={() => { void onOpenPalette(); }}>
            <Command size={16} />
          </button>
        </div>
      </header>}

      {!embedded && <SessionMemoryBar sessionId={renderSessionId} items={displayItems} running={memoryRunning} />}

      <div className="conversation-viewport" ref={conversationViewportRef}>
        {irisFixtureActive ? (
          <div className="iris-fixture-conversation">
            <p className="iris-fixture-conversation__message">已调整为两级导航结构，核心路径保持不变。</p>
            <SessionRunStream sessionId={renderSessionId} statuses={["completed", "failed", "cancelled"]} onStop={onCancel} />
            <div className="iris-fixture-conversation__message iris-fixture-conversation__message--long">
              <p>好的，已制定持久化方案并完成 PoC 验证。</p>
              <p>将采用本地存储并预留云端同步接口，确保数据一致性与恢复能力。</p>
              <p>后续会输出设计文档与实现计划。</p>
            </div>
            <SessionRunStream sessionId={renderSessionId} statuses={["queued", "running", "waiting_user", "reconnecting"]} onStop={onCancel} />
          </div>
        ) : sidebarImDetailConnection ? (
          <div>{t("botDetail.title", { name: sidebarImDetailConnection.title })}</div>
        ) : (
          <div data-testid="session-transcript-slot" style={{ display: 'contents' }}>
            <Transcript
              items={displayItems}
              live={live}
              tabId={activeTabId}
              footerHeight={footerHeight}
              onPrompt={onTranscriptPrompt}
              onWelcomeDraft={onWelcomeDraft}
              welcomeTarget={welcomeTarget}
              onEditPrompt={onEditPrompt}
              onRewind={onRewind}
              onPinMemory={onPinMemory}
              checkpoints={checkpoints}
              actionPending={actionPending}
              rewindDisabled={rewindDisabled}
              running={running}
              welcomeVariant="default"
              informationMode
              actionHoverMenus={false}
              rewindSignal={rewindSignal}
              revealSignal={transcriptRevealSignal}
              hydrating={transcriptHydrating}
              hasOlderHistory={hasOlderHistory}
              olderHistoryCount={historyStartTurn}
              loadingOlderHistory={historyOlderLoading}
              onLoadOlderHistory={onLoadOlderHistory}
              scrollHostRef={scrollHostRef}
              renderTurnFooter={(turn: number) => <SessionRunStream sessionId={renderSessionId} turnId={`turn:${turn + 1}`} onStop={onCancel} />}
            />
            <div data-testid="session-run-slot" style={{ display: 'contents' }}>
              <SessionRunStream sessionId={renderSessionId} unassignedOnly onStop={onCancel} />
            </div>
          </div>
        )}
      </div>

      <div className="session-footer-dock">
        <div data-testid="session-decision-slot" style={{ display: 'contents' }}>
          {approval && (
            <div className="decision-surface">
              <ApprovalModal
                key={approval.id}
                approval={approval}
                cwd={cwd}
                tabId={activeSessionId}
                insertRequest={planRevisionInsertRequest}
                onRevisionActiveChange={(active) => onSetInsertTarget(active ? "planRevision" : "composer")}
                onAnswer={async (allow, session, persist) => {
                  if (approval.tool === "exit_plan_mode" && allow) await onSetCollaborationMode("normal");
                  onApprove(approval.id, allow, session, persist);
                }}
                onRevisePlan={(text) => { onRevisePlan(text); onApprove(approval.id, false, false, false); }}
                onExitPlan={async () => { await onExitPlan(); onApprove(approval.id, false, false, false); }}
                onStop={() => { onCancel(); }}
              />
            </div>
          )}
          {ask && (
            <div className="decision-surface">
              <AskCard ask={ask} onAnswer={onAnswerQuestion} onDismiss={() => onAnswerQuestion(ask.id, [])} onStop={() => { onCancel(); }} />
            </div>
          )}
          {clearContextPending && (
            <div className="decision-surface">
              <ClearContextCard onCancel={onCancelClearContext} onConfirm={() => { void onConfirmClearContext(); }} />
            </div>
          )}
        </div>

        <div data-testid="session-artifact-slot" style={{ display: 'contents' }}>
          <SessionArtifactShelf sessionId={renderSessionId} />
        </div>

        <div data-testid="session-queue-slot" style={{ display: 'contents' }}>
          <SessionQueueTray onEditContent={(text) => onComposerInsert({ id: Date.now(), text, mode: "replace" })} />
        </div>

        <div data-testid="session-composer-slot" style={{ display: 'contents' }}>
          <Composer
            running={running}
            collaborationMode={collaborationMode}
            toolApprovalMode={toolApprovalMode}
            tokenMode={tokenMode}
            goal={goal}
            cwd={cwd}
            modelLabel={modelLabel}
            submitKey={composerSubmitKey}
            imageInputEnabled={imageInputEnabled}
            tabId={activeSessionId}
            widgetEnabled={widgetEnabled}
            onEnterWidgetMode={onEnterWidgetMode}
            effort={effort}
            onSend={onSend}
            onSendAsWork={onSendAsWork}
            workSendAvailable={workSendAvailable}
            workSendSelected={workSendSelected}
            onWorkSendChange={onWorkSendChange}
            onCancel={onCancel}
            onCycleMode={onCycleMode}
            onSetMode={onSetMode}
            onSetCollaborationMode={onSetCollaborationMode}
            onSetToolApprovalMode={onSetToolApprovalMode}
            onToggleYoloApprovalMode={onToggleYoloApprovalMode}
            onClearGoal={onClearGoal}
            onSwitchModel={onSwitchModel}
            onSetEffort={onSetEffort}
            onSetTokenMode={onSetTokenMode}
            insertRequest={composerInsertRequest}
            readOnly={readOnly}
            disabled={composerDisabled}
            submitDisabled={submitDisabled}
            decisionPending={decisionPending}
            ready={ready}
            turnStartAt={turnStartAt}
            turnTokens={turnTokens}
            retry={retry}
            activityStage={activityStage}
            activityStageSeed={activityStageSeed}
            transientDismissSignal={transientOverlayDismissSignal}
            sessionKey={composerSessionKey}
            guidanceConsumedKey={latestGuidanceKey}
            guidanceConsumedText={latestGuidanceText}
            guidanceQueuePreviewItems={guidanceQueueMockItems}
          />
        </div>
        <div data-testid="session-config-slot" style={{ display: 'contents' }}>
          <SessionConfigBar
            modelLabel={configModelLabel}
            contextPercent={contextPercent}
            runtimeMode={runtimeMode}
            foregroundActive={foregroundActive}
            collaborationMode={collaborationMode}
            toolApprovalMode={toolApprovalMode}
            controllerReady={controllerReady}
            tabId={activeSessionId}
            onPrimaryAction={() => {
              document.querySelector<HTMLButtonElement>(".session-footer-dock .composer__btn--send")?.click();
            }}
            workSendAvailable={workSendAvailable}
            workSendSelected={workSendSelected}
            workSendDisabled={composerDisabled || running || readOnly}
            onWorkSendChange={onWorkSendChange}
            onSwitchModel={onSwitchModel}
            onCycleCollaboration={onCycleMode}
            onSetApprovalMode={onSetToolApprovalMode}
            surfaceKind={variant}
          />
        </div>
      </div>
    </section>
  );
};
