import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { useT } from "../lib/i18n";
import { collabReducer, detectSelfAgentIntentRule, initialCollabState, ownMember, replayableSelfAgentItems, selectedTimelineItems } from "./state";
import { defaultCollaborationTransport } from "./transport";
import type {
  CollaborationActionResult,
  CollaborationAgentConfig,
  CollaborationAgentRunResponse,
  CollaborationToolApprovalMode,
  UpdateCollaborationProfileInput,
  CollaborationInvite,
  CollaborationRelayConfig,
  CollaborationRoomQueryInput,
  CollaborationRoomQueryResult,
  CollaborationState,
  CollaborationTimelineItem,
  CollaborationTransport,
  HostCollaborationRoomInput,
  JoinCollaborationRoomInput,
  PendingIntent,
  PostCollaborationMessageInput,
} from "./types";

function requestID(prefix: string): string {
  const id = typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `${prefix}-${id}`;
}

interface CollaborationActionError extends Error {
  code?: string;
}

function actionError(result: CollaborationActionResult, fallback: string): CollaborationActionError | null {
  if (result.ok) return null;
  return Object.assign(new Error(result.error || fallback), { code: result.code });
}

export async function loadCollaborationState(transport: CollaborationTransport, reconnecting = false, onCached?: (state: CollaborationState) => void): Promise<CollaborationState> {
  let state = reconnecting ? await transport.retry() : await transport.getState();
  if (!reconnecting && (state.mode === "host" || state.mode === "client") && state.status === "failed" && state.retryable !== false && state.room) {
    onCached?.(state);
    try {
      state = await transport.retry();
    } catch (error) {
      // Auto-retry failed — surface the cached Snapshot as recoverable so the
      // workspace still renders timeline, members, and Agent configuration
      // instead of a full-page blocking connection card.
      state = { ...state, status: "failed", lastError: error instanceof Error ? error.message : String(error), retryable: true };
    }
  }
  return state;
}

export function buildAgreeMessageInput(item: CollaborationTimelineItem, id: string): PostCollaborationMessageInput {
  return { requestID: id, kind: "reaction", text: "agree", targetItemID: item.id, reactionKind: "agree", referenceIDs: [item.id] };
}

export interface CollabController {
  state: ReturnType<typeof collabReducer>;
  self: ReturnType<typeof ownMember>;
  agentBusy: boolean;
  selectedItems: CollaborationTimelineItem[];
  relayConfig: CollaborationRelayConfig;
  roomQuery: CollaborationRoomQueryResult;
  discoveryLoading: boolean;
  discoveryError?: string;
  relaySaving: boolean;
  relayConfigError?: string;
  host(input: HostCollaborationRoomInput): Promise<void>;
  join(input: JoinCollaborationRoomInput): Promise<void>;
  invite(): Promise<CollaborationInvite>;
  queryRooms(input: CollaborationRoomQueryInput): Promise<void>;
  saveRelayConfig(input: CollaborationRelayConfig): Promise<void>;
  leave(): Promise<void>;
  refresh(reconnecting?: boolean): Promise<void>;
  postChat(text: string, mentions?: { mentionMemberIDs: string[]; mentionAgentIDs: string[] }, referenceIDs?: string[]): Promise<void>;
  postContribution(text: string, contributionKind: string): Promise<void>;
  requestAgent(targetMemberID: string, text: string, referenceIDs?: string[]): Promise<void>;
  agree(item: CollaborationTimelineItem): Promise<void>;
  startAgent(instruction: string, referenceIDs: string[], sourceRequestID?: string): Promise<void>;
  cancelQueuedTask(taskId: string): Promise<void>;
  stopCurrentRun(runId: string): Promise<void>;
  respondAgentRun(item: CollaborationTimelineItem, response: CollaborationAgentRunResponse): Promise<void>;
  acceptRequest(item: CollaborationTimelineItem, instruction?: string): Promise<void>;
  rejectRequest(item: CollaborationTimelineItem): Promise<void>;
  updateAgentConfig(config: CollaborationAgentConfig): Promise<void>;
  updateProfile(profile: Omit<UpdateCollaborationProfileInput, "requestID">): Promise<void>;
  updateToolApprovalMode(mode: CollaborationToolApprovalMode): Promise<void>;
  shareFiles(paths: string[]): Promise<void>;
  receiveFile(fileId: string): Promise<void>;
  pauseFile(fileId: string): Promise<void>;
  resumeFile(fileId: string): Promise<void>;
  revokeFile(fileId: string): Promise<void>;
  openFile(fileId: string): Promise<void>;
  revealFile(fileId: string): Promise<void>;
  toggleSelection(id: string): void;
  clearSelection(): void;
  startPending(intent: PendingIntent): Promise<void>;
  stopPending(messageId: string): void;
  editPending(messageId: string, instruction: string): void;
}

export function useCollabController(sessionID: string, suppliedTransport?: CollaborationTransport): CollabController {
  const t = useT();
  const transport = useMemo(() => suppliedTransport || defaultCollaborationTransport(sessionID), [sessionID, suppliedTransport]);
  const [state, dispatch] = useReducer(collabReducer, initialCollabState);
  const [agentStarting, setAgentStarting] = useState(false);
  const [relayConfig, setRelayConfig] = useState<CollaborationRelayConfig>({ preferLAN: true, connectTimeoutSeconds: 10, routeStableSeconds: 60, relays: [] });
  const [roomQuery, setRoomQuery] = useState<CollaborationRoomQueryResult>({ rooms: [] });
  const [discoveryLoading, setDiscoveryLoading] = useState(false);
  const [discoveryError, setDiscoveryError] = useState<string>();
  const [relaySaving, setRelaySaving] = useState(false);
  const [relayConfigError, setRelayConfigError] = useState<string>();
  const relayConfigEpoch = useRef(0);
  const discoveryEpoch = useRef(0);
  const refreshEpoch = useRef(0);
  const sessionEpoch = useRef(0);
  const intentEpoch = useRef(0);
  const intentChecks = useRef(new Set<string>());
  const agentStartsRef = useRef(0);
  const self = ownMember(state);
  const agentBusy = agentStarting || self?.agent.status === "running" || self?.agent.status === "waiting";

  const acceptResult = useCallback((result: CollaborationActionResult) => {
    const error = actionError(result, t("collab.operationFailed"));
    if (error) throw error;
    if (result.item) dispatch({ type: "EVENT", item: result.item });
    if (result.state) dispatch({ type: "STATE", state: result.state });
  }, [t]);

  const perform = useCallback(async (operation: Promise<CollaborationActionResult>) => {
    const epoch = sessionEpoch.current;
    dispatch({ type: "ACTION_START" });
    try {
      const result = await operation;
      if (epoch !== sessionEpoch.current) return result;
      acceptResult(result);
      return result;
    } catch (error) {
      if (epoch !== sessionEpoch.current) throw error;
      const message = error instanceof Error ? error.message : String(error);
      const code = error instanceof Error ? (error as CollaborationActionError).code : undefined;
      dispatch({ type: "ACTION_FAILED", error: code === "agent_queue_full" ? t("collab.agentQueueFull") : code === "agent_busy" ? t("collab.agentBusy") : message });
      throw error;
    }
  }, [acceptResult, t]);

  const refresh = useCallback(async (reconnecting = false) => {
    const epoch = ++refreshEpoch.current;
    dispatch({ type: "SYNCING", reconnecting });
    try {
      const next = await loadCollaborationState(transport, reconnecting, (cached) => {
        if (epoch === refreshEpoch.current) dispatch({ type: "STATE", state: cached });
      });
      if (epoch === refreshEpoch.current) dispatch({ type: "STATE", state: next });
    } catch (error) {
      if (epoch === refreshEpoch.current) dispatch({ type: "FAILED", error: error instanceof Error ? error.message : String(error), retryable: true });
    }
  }, [transport]);

  // A new Session binding (Workspace switch, re-connect) invalidates every
  // in-flight result of the previous transport: bump the epoch and drop the
  // old Room context before the fresh refresh starts.
  useEffect(() => {
    sessionEpoch.current++;
    dispatch({ type: "DISCONNECTED" });
  }, [transport]);

  useEffect(() => {
    const offState = transport.subscribeState((next) => dispatch({ type: "STATE", state: next }));
    const offEvent = transport.subscribeEvent((item) => dispatch({ type: "EVENT", item }));
    return () => { offState(); offEvent(); };
  }, [transport]);

  useEffect(() => {
    void refresh();
    return () => { refreshEpoch.current++; };
  }, [refresh]);

  useEffect(() => {
    let live = true;
    const epoch = ++relayConfigEpoch.current;
    discoveryEpoch.current++;
    setRelaySaving(false);
    setDiscoveryLoading(false);
    setDiscoveryError(undefined);
    setRoomQuery({ rooms: [] });
    if (!transport.getRelayConfig) {
      setRelayConfig({ preferLAN: true, connectTimeoutSeconds: 10, routeStableSeconds: 60, relays: [] });
      return () => { live = false; };
    }
    void transport.getRelayConfig().then((config) => {
      if (live && epoch === relayConfigEpoch.current) setRelayConfig(config);
    }).catch((error) => {
      if (live && epoch === relayConfigEpoch.current) setRelayConfigError(error instanceof Error ? error.message : String(error));
    });
    return () => { live = false; };
  }, [transport]);

  const saveRelayConfig = useCallback(async (input: CollaborationRelayConfig) => {
    if (!transport.setRelayConfig) throw new Error("Relay settings are unavailable");
    const epoch = ++relayConfigEpoch.current;
    setRelaySaving(true);
    setRelayConfigError(undefined);
    try {
      const next = await transport.setRelayConfig(input);
      if (epoch === relayConfigEpoch.current) {
        discoveryEpoch.current++;
        setRelayConfig(next);
        setRoomQuery({ rooms: [] });
        setDiscoveryError(undefined);
        setDiscoveryLoading(false);
      }
    } catch (error) {
      if (epoch === relayConfigEpoch.current) setRelayConfigError(error instanceof Error ? error.message : String(error));
      throw error;
    } finally {
      if (epoch === relayConfigEpoch.current) setRelaySaving(false);
    }
  }, [transport]);

  useEffect(() => {
    intentEpoch.current++;
    intentChecks.current.clear();
    return () => {
      intentEpoch.current++;
    };
  }, [transport]);

  const host = useCallback(async (input: HostCollaborationRoomInput) => {
    const epoch = sessionEpoch.current;
    dispatch({ type: "CONNECTING", operation: "host" });
    try {
      const next = await transport.host({ ...input, token: input.token?.trim() || undefined, sessionID });
      if (epoch !== sessionEpoch.current) return;
      dispatch({ type: "STATE", state: next });
    } catch (error) {
      if (epoch !== sessionEpoch.current) return;
      dispatch({ type: "FAILED", error: error instanceof Error ? error.message : String(error), retryable: true });
      throw error;
    }
  }, [sessionID, transport]);

  const join = useCallback(async (input: JoinCollaborationRoomInput) => {
    const epoch = sessionEpoch.current;
    dispatch({ type: "CONNECTING", operation: "join" });
    try {
      const next = await transport.join({ ...input, token: input.token?.trim() || undefined, sessionID });
      if (epoch !== sessionEpoch.current) return;
      dispatch({ type: "SYNCING" });
      dispatch({ type: "STATE", state: next });
    } catch (error) {
      if (epoch !== sessionEpoch.current) return;
      dispatch({ type: "FAILED", error: error instanceof Error ? error.message : String(error), retryable: true });
      throw error;
    }
  }, [sessionID, transport]);

  const leave = useCallback(async () => {
    const epoch = sessionEpoch.current;
    try {
      await transport.leave();
      if (epoch !== sessionEpoch.current) return;
      dispatch({ type: "DISCONNECTED" });
    } catch (error) {
      if (epoch !== sessionEpoch.current) return;
      dispatch({ type: "FAILED", error: error instanceof Error ? error.message : String(error), retryable: true });
    }
  }, [transport]);

  const invite = useCallback(() => transport.invite(), [transport]);

  const queryRooms = useCallback(async (input: CollaborationRoomQueryInput) => {
    const epoch = ++discoveryEpoch.current;
    if (!transport.listRooms) {
      setRoomQuery({ rooms: [] });
      return;
    }
    setDiscoveryLoading(true);
    setDiscoveryError(undefined);
    try {
      const next = await transport.listRooms(input);
      if (epoch === discoveryEpoch.current) setRoomQuery(next);
    } catch (error) {
      if (epoch === discoveryEpoch.current) setDiscoveryError(error instanceof Error ? error.message : String(error));
    } finally {
      if (epoch === discoveryEpoch.current) setDiscoveryLoading(false);
    }
  }, [transport]);

  const createPendingIntent = useCallback((item: CollaborationTimelineItem) => {
    const rule = detectSelfAgentIntentRule(item.text);
    if (rule.covered) {
      if (rule.intent === "chat") return;
      dispatch({
        type: "PENDING_INTENT",
        intent: { messageId: item.id, revision: item.revision, instruction: item.text, deadline: Date.now() + 5_000, status: "pending" },
      });
      return;
    }
    if (!transport.classifyIntent) return;
    const key = `${item.id}:${item.revision}`;
    if (intentChecks.current.has(key)) return;
    intentChecks.current.add(key);
    const epoch = intentEpoch.current;
    void transport.classifyIntent(item.text).then((result) => {
      if (epoch !== intentEpoch.current) return;
      // Only create a PendingIntent when classification succeeds with an
      // actionable intent. Errors, timeouts, and unavailable classifiers
      // must not create task cards — the message was already sent.
      if (result.error || result.intent === "chat") return;
      dispatch({
        type: "PENDING_INTENT",
        intent: {
          messageId: item.id,
          revision: item.revision,
          instruction: item.text,
          deadline: Date.now() + 5_000,
          status: "pending",
        },
      });
    }).catch(() => {
      // Classification transport failure (network, timeout, etc.).
      // The message was already sent; do not create a PendingIntent.
    });
  }, [t, transport]);

  useEffect(() => {
    for (const item of replayableSelfAgentItems(state)) createPendingIntent(item);
  }, [createPendingIntent, state]);

  const postChat = useCallback(async (value: string, mentions = { mentionMemberIDs: [], mentionAgentIDs: [] }, referenceIDs: string[] = []) => {
    const text = value.trim();
    if (!text) return;
    const epoch = sessionEpoch.current;
    const result = await perform(transport.post({ requestID: requestID("chat"), kind: "chat", text, referenceIDs, ...mentions }));
    if (epoch === sessionEpoch.current && result.item) createPendingIntent(result.item);
  }, [createPendingIntent, perform, transport]);

  const postContribution = useCallback(async (value: string, contributionKind: string) => {
    const text = value.trim();
    if (!text) return;
    await perform(transport.post({ requestID: requestID("contribution"), kind: "contribution", contributionKind, text }));
  }, [perform, transport]);

  const requestAgent = useCallback(async (targetMemberID: string, value: string, referenceIDs: string[] = []) => {
    const text = value.trim();
    if (!targetMemberID || !text) return;
    await perform(transport.post({ requestID: requestID("request"), kind: "agent_request", targetMemberID, referenceIDs, text }));
  }, [perform, transport]);

  const agree = useCallback(async (item: CollaborationTimelineItem) => {
    await perform(transport.post(buildAgreeMessageInput(item, requestID("agree"))));
  }, [perform, transport]);

  const startAgentWithID = useCallback((value: string, referenceIDs: string[], sourceRequestID?: string, stableRequestID?: string, automatic = false): Promise<void> => {
    const instruction = value.trim();
    if (!instruction || !sessionID) return Promise.reject(new Error(t("collab.agentNotBound")));
    agentStartsRef.current++;
    setAgentStarting(true);
    const pending = perform(transport.startAgent({ requestID: stableRequestID || requestID("agent"), sessionID, instruction, referenceIDs, agentRequestID: sourceRequestID, automatic }))
      .then(() => undefined)
      .finally(() => {
        agentStartsRef.current--;
        if (agentStartsRef.current === 0) {
          setAgentStarting(false);
        }
      });
    return pending;
  }, [perform, sessionID, t, transport]);

  const startAgent = useCallback((value: string, referenceIDs: string[], sourceRequestID?: string) => (
    startAgentWithID(value, referenceIDs, sourceRequestID)
  ), [startAgentWithID]);

  const cancelQueuedTask = useCallback(async (taskId: string) => {
    await perform(transport.cancelQueuedTask(taskId));
  }, [perform, transport]);

  const stopCurrentRun = useCallback(async (runId: string) => {
    if (!transport.stopCurrentRun) throw new Error(t("collab.operationFailed"));
    dispatch({ type: "ACTION_START" });
    try {
      await transport.stopCurrentRun(runId);
    } catch (error) {
      dispatch({ type: "ACTION_FAILED", error: error instanceof Error ? error.message : String(error) });
      throw error;
    }
  }, [dispatch, t, transport]);

  const respondAgentRun = useCallback(async (item: CollaborationTimelineItem, response: CollaborationAgentRunResponse) => {
    if (!transport.respondAgentRun) throw new Error(t("collab.operationFailed"));
    await perform(transport.respondAgentRun(item.id, response));
  }, [perform, t, transport]);

  const startPending = useCallback(async (intent: PendingIntent) => {
    if (intent.status !== "pending" && intent.status !== "failed") return;
    const epoch = sessionEpoch.current;
    dispatch({ type: "PENDING_STATUS", id: intent.messageId, status: "starting" });
    try {
      await startAgent(intent.instruction, [intent.messageId]);
      if (epoch === sessionEpoch.current) dispatch({ type: "PENDING_STATUS", id: intent.messageId, status: "dismissed" });
    } catch (error) {
      if (epoch === sessionEpoch.current) dispatch({ type: "PENDING_STATUS", id: intent.messageId, status: "failed", error: error instanceof Error ? error.message : String(error) });
    }
  }, [startAgent]);

  const stopPending = useCallback((messageId: string) => dispatch({ type: "PENDING_STATUS", id: messageId, status: "dismissed" }), []);
  const editPending = useCallback((messageId: string, instruction: string) => dispatch({ type: "UPDATE_PENDING", id: messageId, instruction, deadline: Date.now() + 5_000 }), []);

  const acceptRequest = useCallback(async (item: CollaborationTimelineItem, instruction?: string, automatic = false) => {
    await perform(transport.respond({ requestID: requestID("accept"), agentRequestID: item.id, action: "accept", instruction: instruction || item.text, sessionID, automatic }));
  }, [perform, sessionID, transport]);

  const rejectRequest = useCallback(async (item: CollaborationTimelineItem) => {
    await perform(transport.respond({ requestID: requestID("reject"), agentRequestID: item.id, action: "reject", sessionID }));
  }, [perform, sessionID, transport]);

  const updateAgentConfig = useCallback(async (config: CollaborationAgentConfig) => {
    const epoch = sessionEpoch.current;
    dispatch({ type: "ACTION_START" });
    try {
      const next = await transport.updateAgentConfig({ requestID: requestID("agent-config"), config });
      if (epoch === sessionEpoch.current) dispatch({ type: "STATE", state: next });
    } catch (error) {
      if (epoch === sessionEpoch.current) dispatch({ type: "ACTION_FAILED", error: error instanceof Error ? error.message : String(error) });
      throw error;
    }
  }, [transport]);

  const updateProfile = useCallback(async (profile: Omit<UpdateCollaborationProfileInput, "requestID">) => {
    const epoch = sessionEpoch.current;
    dispatch({ type: "ACTION_START" });
    try {
      const next = await transport.updateProfile({ requestID: requestID("profile"), ...profile });
      if (epoch === sessionEpoch.current) dispatch({ type: "STATE", state: next });
    } catch (error) {
      if (epoch === sessionEpoch.current) dispatch({ type: "ACTION_FAILED", error: error instanceof Error ? error.message : String(error) });
      throw error;
    }
  }, [transport]);

  const updateToolApprovalMode = useCallback(async (mode: CollaborationToolApprovalMode) => {
    if (!transport.updateToolApprovalMode) throw new Error(t("collab.operationFailed"));
    const epoch = sessionEpoch.current;
    dispatch({ type: "ACTION_START" });
    try {
      const next = await transport.updateToolApprovalMode(mode);
      if (epoch === sessionEpoch.current) dispatch({ type: "STATE", state: next });
    } catch (error) {
      if (epoch === sessionEpoch.current) dispatch({ type: "ACTION_FAILED", error: error instanceof Error ? error.message : String(error) });
      throw error;
    }
  }, [t, transport]);

  const runFileAction = useCallback(async (operation: Promise<unknown>) => {
    const epoch = sessionEpoch.current;
    dispatch({ type: "ACTION_START" });
    try {
      await operation;
    } catch (error) {
      if (epoch === sessionEpoch.current) dispatch({ type: "ACTION_FAILED", error: error instanceof Error ? error.message : String(error) });
      throw error;
    }
  }, []);

  const shareFiles = useCallback((paths: string[]) => runFileAction(transport.shareFiles(paths)), [runFileAction, transport]);
  const receiveFile = useCallback((fileId: string) => runFileAction(transport.receiveFile(fileId)), [runFileAction, transport]);
  const pauseFile = useCallback((fileId: string) => runFileAction(transport.pauseFile(fileId)), [runFileAction, transport]);
  const resumeFile = useCallback((fileId: string) => runFileAction(transport.resumeFile(fileId)), [runFileAction, transport]);
  const openFile = useCallback((fileId: string) => runFileAction(transport.openFile(fileId)), [runFileAction, transport]);
  const revealFile = useCallback((fileId: string) => runFileAction(transport.revealFile(fileId)), [runFileAction, transport]);
  const revokeFile = useCallback(async (fileId: string) => {
    await perform(transport.revokeFile(fileId));
  }, [perform, transport]);

  const toggleSelection = useCallback((id: string) => dispatch({ type: "TOGGLE_SELECT", id }), []);
  const clearSelection = useCallback(() => dispatch({ type: "CLEAR_SELECTION" }), []);
  const selectedItems = selectedTimelineItems(state);

  return { state, self, agentBusy, selectedItems, relayConfig, roomQuery, discoveryLoading, discoveryError, relaySaving, relayConfigError, host, join, invite, queryRooms, saveRelayConfig, leave, refresh, postChat, postContribution, requestAgent, agree, startAgent, cancelQueuedTask, stopCurrentRun, respondAgentRun, acceptRequest, rejectRequest, updateAgentConfig, updateProfile, updateToolApprovalMode, shareFiles, receiveFile, pauseFile, resumeFile, revokeFile, openFile, revealFile, toggleSelection, clearSelection, startPending, stopPending, editPending };
}
