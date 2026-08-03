import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { useT } from "../lib/i18n";
import { collabReducer, detectSelfAgentIntent, initialCollabState, ownMember, replayableSelfAgentItems, selectedTimelineItems } from "./state";
import { defaultCollaborationTransport } from "./transport";
import type {
  CollaborationActionResult,
  CollaborationInvite,
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

export function buildAgreeMessageInput(item: CollaborationTimelineItem, id: string): PostCollaborationMessageInput {
  return { requestID: id, kind: "reaction", text: "agree", targetItemID: item.id, reactionKind: "agree", referenceIDs: [item.id] };
}

export interface CollabController {
  state: ReturnType<typeof collabReducer>;
  self: ReturnType<typeof ownMember>;
  agentBusy: boolean;
  selectedItems: CollaborationTimelineItem[];
  host(input: HostCollaborationRoomInput): Promise<void>;
  join(input: JoinCollaborationRoomInput): Promise<void>;
  invite(): Promise<CollaborationInvite>;
  leave(): Promise<void>;
  refresh(reconnecting?: boolean): Promise<void>;
  postChat(text: string): Promise<void>;
  postContribution(text: string, contributionKind: string): Promise<void>;
  requestAgent(targetMemberID: string, text: string, referenceIDs?: string[]): Promise<void>;
  agree(item: CollaborationTimelineItem): Promise<void>;
  startAgent(instruction: string, referenceIDs: string[], sourceRequestID?: string): Promise<void>;
  acceptRequest(item: CollaborationTimelineItem, instruction?: string): Promise<void>;
  rejectRequest(item: CollaborationTimelineItem): Promise<void>;
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
  const refreshEpoch = useRef(0);
  const agentStartRef = useRef<Promise<void> | null>(null);
  const self = ownMember(state);
  const agentBusy = agentStarting || self?.agent.status === "running" || self?.agent.status === "waiting";

  const acceptResult = useCallback((result: CollaborationActionResult) => {
    const error = actionError(result, t("collab.operationFailed"));
    if (error) throw error;
    if (result.item) dispatch({ type: "EVENT", item: result.item });
    if (result.state) dispatch({ type: "STATE", state: result.state });
  }, [t]);

  const perform = useCallback(async (operation: Promise<CollaborationActionResult>) => {
    dispatch({ type: "ACTION_START" });
    try {
      const result = await operation;
      acceptResult(result);
      return result;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      const code = error instanceof Error ? (error as CollaborationActionError).code : undefined;
      dispatch({ type: "ACTION_FAILED", error: code === "agent_busy" ? t("collab.agentBusy") : message });
      throw error;
    }
  }, [acceptResult, t]);

  const refresh = useCallback(async (reconnecting = false) => {
    const epoch = ++refreshEpoch.current;
    dispatch({ type: "SYNCING", reconnecting });
    try {
      const next = reconnecting ? await transport.retry() : await transport.getState();
      if (epoch === refreshEpoch.current) dispatch({ type: "STATE", state: next });
    } catch (error) {
      if (epoch === refreshEpoch.current) dispatch({ type: "FAILED", error: error instanceof Error ? error.message : String(error), retryable: true });
    }
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

  const host = useCallback(async (input: HostCollaborationRoomInput) => {
    dispatch({ type: "CONNECTING", operation: "host" });
    try {
      const next = await transport.host({ ...input, token: input.token?.trim() || undefined, sessionID });
      dispatch({ type: "STATE", state: next });
    } catch (error) {
      dispatch({ type: "FAILED", error: error instanceof Error ? error.message : String(error), retryable: true });
      throw error;
    }
  }, [sessionID, transport]);

  const join = useCallback(async (input: JoinCollaborationRoomInput) => {
    dispatch({ type: "CONNECTING", operation: "join" });
    try {
      const next = await transport.join({ ...input, token: input.token?.trim() || undefined, sessionID });
      dispatch({ type: "SYNCING" });
      dispatch({ type: "STATE", state: next });
    } catch (error) {
      dispatch({ type: "FAILED", error: error instanceof Error ? error.message : String(error), retryable: true });
      throw error;
    }
  }, [sessionID, transport]);

  const leave = useCallback(async () => {
    try {
      await transport.leave();
      dispatch({ type: "DISCONNECTED" });
    } catch (error) {
      dispatch({ type: "FAILED", error: error instanceof Error ? error.message : String(error), retryable: true });
    }
  }, [transport]);

  const invite = useCallback(() => transport.invite(), [transport]);

  const createPendingIntent = useCallback((item: CollaborationTimelineItem) => {
    if (detectSelfAgentIntent(item.text) === "chat") return;
    dispatch({
      type: "PENDING_INTENT",
      intent: { messageId: item.id, revision: item.revision, instruction: item.text, deadline: Date.now() + 5_000, status: "pending" },
    });
  }, []);

  useEffect(() => {
    for (const item of replayableSelfAgentItems(state)) createPendingIntent(item);
  }, [createPendingIntent, state]);

  const postChat = useCallback(async (value: string) => {
    const text = value.trim();
    if (!text) return;
    const result = await perform(transport.post({ requestID: requestID("chat"), kind: "chat", text }));
    if (result.item) createPendingIntent(result.item);
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

  const startAgent = useCallback((value: string, referenceIDs: string[], sourceRequestID?: string): Promise<void> => {
    const instruction = value.trim();
    if (!instruction || !sessionID) return Promise.reject(new Error(t("collab.agentNotBound")));
    if (agentStartRef.current) return agentStartRef.current;
    if (agentBusy) {
      const error = new Error(t("collab.agentBusy"));
      dispatch({ type: "ACTION_FAILED", error: error.message });
      return Promise.reject(error);
    }
    setAgentStarting(true);
    const pending = perform(transport.startAgent({ requestID: requestID("agent"), sessionID, instruction, referenceIDs, agentRequestID: sourceRequestID }))
      .then(() => undefined)
      .finally(() => {
        if (agentStartRef.current === pending) {
          agentStartRef.current = null;
          setAgentStarting(false);
        }
      });
    agentStartRef.current = pending;
    return pending;
  }, [agentBusy, perform, sessionID, t, transport]);

  const startPending = useCallback(async (intent: PendingIntent) => {
    if (intent.status !== "pending") return;
    dispatch({ type: "PENDING_STATUS", id: intent.messageId, status: "starting" });
    try {
      await startAgent(intent.instruction, [intent.messageId]);
      dispatch({ type: "PENDING_STATUS", id: intent.messageId, status: "dismissed" });
    } catch (error) {
      dispatch({ type: "PENDING_STATUS", id: intent.messageId, status: "failed", error: error instanceof Error ? error.message : String(error) });
    }
  }, [startAgent]);

  const stopPending = useCallback((messageId: string) => dispatch({ type: "PENDING_STATUS", id: messageId, status: "dismissed" }), []);
  const editPending = useCallback((messageId: string, instruction: string) => dispatch({ type: "UPDATE_PENDING", id: messageId, instruction, deadline: Date.now() + 5_000 }), []);

  const acceptRequest = useCallback(async (item: CollaborationTimelineItem, instruction?: string) => {
    await perform(transport.respond({ requestID: requestID("accept"), agentRequestID: item.id, action: "accept", instruction: instruction || item.text, sessionID }));
  }, [perform, sessionID, transport]);

  const rejectRequest = useCallback(async (item: CollaborationTimelineItem) => {
    await perform(transport.respond({ requestID: requestID("reject"), agentRequestID: item.id, action: "reject", sessionID }));
  }, [perform, sessionID, transport]);

  const toggleSelection = useCallback((id: string) => dispatch({ type: "TOGGLE_SELECT", id }), []);
  const clearSelection = useCallback(() => dispatch({ type: "CLEAR_SELECTION" }), []);
  const selectedItems = selectedTimelineItems(state);

  return { state, self, agentBusy, selectedItems, host, join, invite, leave, refresh, postChat, postContribution, requestAgent, agree, startAgent, acceptRequest, rejectRequest, toggleSelection, clearSelection, startPending, stopPending, editPending };
}
