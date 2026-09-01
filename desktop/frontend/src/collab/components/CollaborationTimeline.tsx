import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent, type ReactNode, type RefObject } from "react";
import { defaultRangeExtractor, useVirtualizer } from "@tanstack/react-virtual";
import { Ban, Bot, Check, ChevronDown, ChevronUp, CircleAlert, Download, ExternalLink, File, FolderOpen, Image, MoreHorizontal, Pause, Play, RefreshCw, Reply, ThumbsUp, UserRound } from "lucide-react";
import { useI18n } from "../../lib/i18n";
import { ApprovalModal } from "../../components/ApprovalModal";
import { AskCard } from "../../components/AskCard";
import { collabCopy, contributionLabel, type CollabCopy } from "../copy";
import { visibleCollaborationTimeline } from "../state";
import type { CollaborationAgentPrompt, CollaborationAgentRunResponse, CollaborationFilePreview, CollaborationFileTransfer, CollaborationMember, CollaborationTimelineItem, PendingIntent } from "../types";
import { IntentCountdown } from "./IntentCountdown";
import { CollaborationAvatar } from "./CollaborationAvatar";

type CollaborationPreviewLoader = (fileId: string) => Promise<CollaborationFilePreview | null>;

interface CollaborationPreviewCacheEntry {
  promise: Promise<CollaborationFilePreview | null>;
  dataBytes: number;
  consumers: number;
  settled: boolean;
  cancelQueued(): boolean;
}

interface CollaborationPreviewCache {
  entries: Map<string, CollaborationPreviewCacheEntry>;
  dataBytes: number;
}

const collaborationPreviewCache = new WeakMap<CollaborationPreviewLoader, CollaborationPreviewCache>();
const collaborationPreviewQueue: Array<() => void> = [];
const maxConcurrentCollaborationPreviews = 2;
const maxCachedCollaborationPreviews = 16;
const maxCachedCollaborationPreviewBytes = 16 * 1024 * 1024;
let activeCollaborationPreviews = 0;

function drainCollaborationPreviewQueue() {
  while (activeCollaborationPreviews < maxConcurrentCollaborationPreviews && collaborationPreviewQueue.length > 0) {
    collaborationPreviewQueue.shift()?.();
  }
}

function queueCollaborationPreview(load: CollaborationPreviewLoader, fileId: string) {
  let started = false;
  let settled = false;
  let queuedTask: (() => void) | undefined;
  let cancelQueued = () => false;
  const promise = new Promise<CollaborationFilePreview | null>((resolve, reject) => {
    queuedTask = () => {
      if (settled) return;
      started = true;
      activeCollaborationPreviews++;
      Promise.resolve()
        .then(() => load(fileId))
        .then(resolve, reject)
        .finally(() => {
          settled = true;
          activeCollaborationPreviews--;
          drainCollaborationPreviewQueue();
        });
    };
    collaborationPreviewQueue.push(queuedTask);
    cancelQueued = () => {
      if (started || settled || !queuedTask) return false;
      const index = collaborationPreviewQueue.indexOf(queuedTask);
      if (index >= 0) collaborationPreviewQueue.splice(index, 1);
      settled = true;
      resolve(null);
      drainCollaborationPreviewQueue();
      return true;
    };
    drainCollaborationPreviewQueue();
  });
  return { promise, cancelQueued: () => cancelQueued() };
}

function collaborationPreviewKey(item: CollaborationTimelineItem) {
  const mime = (item.fileMime || "").split(";", 1)[0].trim().toLowerCase();
  return JSON.stringify([item.id, (item.fileSHA256 || "").trim().toLowerCase(), item.fileSize || 0, mime]);
}

function removeCollaborationPreview(cache: CollaborationPreviewCache, cacheKey: string, expected?: CollaborationPreviewCacheEntry) {
  const entry = cache.entries.get(cacheKey);
  if (!entry || (expected && entry !== expected)) return;
  cache.entries.delete(cacheKey);
  cache.dataBytes -= entry.dataBytes;
}

function touchCollaborationPreview(cache: CollaborationPreviewCache, cacheKey: string, entry: CollaborationPreviewCacheEntry) {
  cache.entries.delete(cacheKey);
  cache.entries.set(cacheKey, entry);
}

function trimCollaborationPreviewCache(cache: CollaborationPreviewCache) {
  while (cache.entries.size > maxCachedCollaborationPreviews || cache.dataBytes > maxCachedCollaborationPreviewBytes) {
    const oldest = cache.entries.keys().next().value as string | undefined;
    if (oldest === undefined) return;
    removeCollaborationPreview(cache, oldest);
  }
}

function acquireCollaborationPreview(cache: CollaborationPreviewCache, cacheKey: string, entry: CollaborationPreviewCacheEntry) {
  entry.consumers++;
  let released = false;
  return {
    promise: entry.promise,
    release: () => {
      if (released) return;
      released = true;
      entry.consumers--;
      if (entry.consumers === 0 && !entry.settled && entry.cancelQueued()) {
        removeCollaborationPreview(cache, cacheKey, entry);
      }
    },
  };
}

function loadCollaborationPreview(load: CollaborationPreviewLoader, fileId: string, cacheKey: string, refresh: boolean) {
  let cache = collaborationPreviewCache.get(load);
  if (!cache) {
    cache = { entries: new Map(), dataBytes: 0 };
    collaborationPreviewCache.set(load, cache);
  }
  if (refresh) removeCollaborationPreview(cache, cacheKey);
  const cached = cache.entries.get(cacheKey);
  if (cached) {
    touchCollaborationPreview(cache, cacheKey, cached);
    return acquireCollaborationPreview(cache, cacheKey, cached);
  }

  const queued = queueCollaborationPreview(load, fileId);
  const entry: CollaborationPreviewCacheEntry = { promise: Promise.resolve(null), dataBytes: 0, consumers: 0, settled: false, cancelQueued: queued.cancelQueued };
  entry.promise = queued.promise
    .then((preview) => {
      entry.settled = true;
      if (cache.entries.get(cacheKey) === entry) {
        entry.dataBytes = preview?.dataUrl.length || 0;
        cache.dataBytes += entry.dataBytes;
        trimCollaborationPreviewCache(cache);
      }
      return preview;
    })
    .catch((error) => {
      entry.settled = true;
      removeCollaborationPreview(cache, cacheKey, entry);
      throw error;
    });
  cache.entries.set(cacheKey, entry);
  trimCollaborationPreviewCache(cache);
  return acquireCollaborationPreview(cache, cacheKey, entry);
}

function invalidateCollaborationPreview(load: CollaborationPreviewLoader | undefined, cacheKey: string) {
  const cache = load ? collaborationPreviewCache.get(load) : undefined;
  if (cache) removeCollaborationPreview(cache, cacheKey);
}

function looksLikeCollaborationImage(item: CollaborationTimelineItem) {
  const mime = (item.fileMime || "").split(";", 1)[0].trim().toLowerCase();
  if (["image/png", "image/jpeg", "image/gif", "image/webp"].includes(mime)) return true;
  return /\.(?:png|jpe?g|gif|webp)$/i.test(item.fileName || item.text || "");
}

interface CollaborationTimelineProps {
  items: CollaborationTimelineItem[];
  members?: CollaborationMember[];
  selfMemberId?: string;
  selectedIds: string[];
  pendingIntents: Record<string, PendingIntent>;
  connected: boolean;
  agentBusy: boolean;
  transfers: CollaborationFileTransfer[];
  agentPrompt?: CollaborationAgentPrompt;
  onToggle(id: string): void;
  onReply(item: CollaborationTimelineItem): void;
  onAgree(item: CollaborationTimelineItem): void;
  onRequestAgent(item: CollaborationTimelineItem, memberId: string): void;
  onAgent(item: CollaborationTimelineItem): void;
  onAccept(item: CollaborationTimelineItem): void;
  onReject(item: CollaborationTimelineItem): void;
  onRespondAgentRun(item: CollaborationTimelineItem, response: CollaborationAgentRunResponse): void;
  onStartPending(intent: PendingIntent): void;
  onStopPending(id: string): void;
  onEditPending(id: string, instruction: string): void;
  onReceiveFile(id: string): void;
  onPauseFile(id: string): void;
  onResumeFile(id: string): void;
  onRevokeFile(id: string): void;
  onOpenFile(id: string): void;
  onRevealFile(id: string): void;
  previewFile?(fileId: string): Promise<CollaborationFilePreview | null>;
  // Optional scroll owner + sticky state from the hosting Workspace. When a
  // scroll container is provided and history exceeds the threshold the
  // timeline virtualizes. Explicit null waits for the host without mounting
  // the full history; omission preserves standalone small-list usage.
  scrollContainer?: HTMLElement | null;
  stickRef?: RefObject<boolean | null>;
}

const kindCopy = {
  chat: "kindChat",
  contribution: "kindContribution",
  agent_command: "kindCommand",
  agent_request: "kindRequest",
  agent_result: "kindResult",
  file: "kindFile",
  reaction: "kindReaction",
  system: "kindSystem",
} as const;

// Virtualize the timeline once history grows past this many visible rows so
// the mounted DOM stays bounded no matter how large a Room's history becomes.
// Presence notices count as rows. Below the threshold the timeline renders
// fully — identical to the pre-virtualization behavior (jsdom / SSR / dialog
// mode all fall back to this path).
const virtualTimelineThreshold = 200;
const virtualTimelineEstimateSize = 64;
const virtualTimelineOverscan = 12;

function fileSize(value = 0): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}

function fileStatus(status: CollaborationFileTransfer["status"] | undefined, c: CollabCopy): string {
  if (status === "preparing") return c("filePreparing");
  if (status === "pending") return c("pending");
  if (status === "available") return c("fileAvailable");
  if (status === "unavailable" || status === "waiting_sender") return c("fileWaitingOwner");
  if (status === "source_changed") return c("fileSourceChanged");
  if (status === "revoked") return c("fileRevoked");
  if (status === "negotiating") return c("fileNegotiating");
  if (status === "downloading") return c("fileReceiving");
  if (status === "paused") return c("filePaused");
  if (status === "verifying") return c("fileVerifying");
  if (status === "completed") return c("fileCompleted");
  if (status === "failed") return c("fileFailed");
  return c("fileAvailable");
}

function FileCard({ item, own, transfer, c, onReceive, onPause, onResume, onRevoke, onOpen, onReveal, previewFile }: { item: CollaborationTimelineItem; own: boolean; transfer?: CollaborationFileTransfer; c: CollabCopy; onReceive(): void; onPause(): void; onResume(): void; onRevoke(): void; onOpen(): void; onReveal(): void; previewFile?: CollaborationPreviewLoader }) {
  const revoked = item.fileRevoked || transfer?.status === "revoked";
  const receiving = transfer?.direction === "receive";
  const previewReady = looksLikeCollaborationImage(item) && !revoked && ((receiving && transfer?.status === "completed") || (own && transfer?.direction === "share" && transfer.status === "available"));
  const previewKey = collaborationPreviewKey(item);
  const progress = transfer?.total ? Math.min(100, Math.round(transfer.transferred / transfer.total * 100)) : 0;
  const resumable = receiving && ["paused", "waiting_sender", "failed"].includes(transfer?.status || "") && transfer?.retryable !== false;
  const cardRef = useRef<HTMLDivElement>(null);
  const [imagePreview, setImagePreview] = useState<string | null>(null);
  const [previewError, setPreviewError] = useState(false);
  const [previewAttempt, setPreviewAttempt] = useState(0);
  const [previewVisible, setPreviewVisible] = useState(false);

  useEffect(() => {
    setPreviewVisible(false);
    if (!previewReady || !previewFile) return;
    const target = cardRef.current;
    if (!target || typeof IntersectionObserver === "undefined") {
      setPreviewVisible(true);
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      const entry = entries.find((value) => value.target === target);
      if (entry) setPreviewVisible(entry.isIntersecting);
    }, { root: target.closest(".collab-scroll") });
    observer.observe(target);
    return () => observer.disconnect();
  }, [previewFile, previewKey, previewReady]);

  useEffect(() => {
    setImagePreview(null);
    setPreviewError(false);
    if (!previewReady || !previewVisible || !previewFile) return;
    let cancelled = false;
    const request = loadCollaborationPreview(previewFile, item.id, previewKey, previewAttempt > 0);
    request.promise.then((preview) => {
      if (cancelled || !preview || !preview.dataUrl) return;
      const supported = ["image/png", "image/jpeg", "image/gif", "image/webp"].includes(preview.mime);
      if (!supported || !preview.dataUrl.startsWith(`data:${preview.mime};base64,`)) {
        invalidateCollaborationPreview(previewFile, previewKey);
        setPreviewError(true);
        return;
      }
      setImagePreview(preview.dataUrl);
    }).catch(() => {
      if (!cancelled) setPreviewError(true);
    });
    return () => {
      cancelled = true;
      request.release();
    };
  }, [item.id, previewAttempt, previewFile, previewKey, previewReady, previewVisible]);

  const previewLoadFailed = () => {
    invalidateCollaborationPreview(previewFile, previewKey);
    setImagePreview(null);
    setPreviewError(true);
  };

  return <div ref={cardRef} className={`collab-file-card${revoked ? " collab-file-card--revoked" : ""}${imagePreview ? " collab-file-card--image" : ""}`}>
    <div className="collab-file-card__icon">{imagePreview ? <img src={imagePreview} alt="" loading="lazy" decoding="async" draggable={false} onError={previewLoadFailed} /> : <File size={21} />}</div>
    <div className="collab-file-card__body"><strong>{item.fileName || item.text}</strong><span>{fileSize(item.fileSize)} · {fileStatus(revoked ? "revoked" : transfer?.status, c)}</span>{transfer?.error && <small>{transfer.error}</small>}</div>
    <div className="collab-file-card__actions">
      {!own && !revoked && !receiving && <button type="button" onClick={onReceive}><Download size={14} />{c("fileReceive")}</button>}
      {receiving && transfer.status === "downloading" && <button type="button" onClick={onPause}><Pause size={14} />{c("filePause")}</button>}
      {resumable && <button type="button" onClick={onResume}><Play size={14} />{c("fileResume")}</button>}
      {own && !revoked && <button type="button" onClick={onRevoke}><Ban size={14} />{c("fileRevoke")}</button>}
      {receiving && transfer.status === "completed" && <><button type="button" className="collab-file-card__open" onClick={onOpen}><ExternalLink size={14} />{c("fileOpen")}</button><details className="collab-file-card__more"><summary aria-label={c("moreActions")} title={c("moreActions")}><MoreHorizontal size={15} /></summary><div><button type="button" onClick={onReveal}><FolderOpen size={14} />{c("fileReveal")}</button></div></details></>}
    </div>
    {receiving && transfer.status !== "completed" && <div className="collab-file-progress"><span style={{ width: `${progress}%` }} /></div>}
    {imagePreview && <div className="collab-file-card__preview"><img src={imagePreview} alt={item.fileName || item.text} loading="lazy" decoding="async" draggable={false} onError={previewLoadFailed} /></div>}
    {previewError && !imagePreview && <div className="collab-file-card__preview-error" role="status"><Image size={14} /><span>{c("previewFailed")}</span><button type="button" onClick={() => setPreviewAttempt((value) => value + 1)}><RefreshCw size={12} />{c("previewRetry")}</button></div>}
  </div>;
}

function timeLabel(value: string, locale: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "" : new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(date);
}

function presenceLabel(item: CollaborationTimelineItem, c: CollabCopy): string | undefined {
  if (item.kind !== "system") return undefined;
  const name = item.actorName;
  if (item.systemKind === "member.joined") return c("memberJoined", { name });
  if (item.systemKind === "member.rejoined") return c("memberRejoined", { name });
  if (item.systemKind === "member.left") return c("memberLeft", { name });
  return undefined;
}

function runStatusLabel(status: CollaborationTimelineItem["agentRunStatus"], c: CollabCopy): string {
  if (status === "queued") return c("queued");
  if (status === "waiting_approval") return c("waiting");
  if (status === "completed") return c("completed");
  if (status === "failed") return c("runFailed");
  if (status === "cancelled") return c("cancelled");
  if (status === "interrupted") return c("interrupted");
  return c("running");
}

function AgentRunCard({ item, c, canRespond, prompt, onRespond }: { item: CollaborationTimelineItem; c: CollabCopy; canRespond: boolean; prompt?: CollaborationAgentPrompt; onRespond(response: CollaborationAgentRunResponse): void }) {
  const status = item.agentRunStatus || "running";
  const active = status === "queued" || status === "running" || status === "waiting_approval";
  const hasOutput = !active && Boolean(item.agentRunOutput);
  const detailed = canRespond && prompt?.runId === item.id;
  return <div className={`collab-agent-run-stack${detailed ? " collab-agent-run-stack--prompt" : ""}`}>
    <div className={`collab-agent-run collab-agent-run--${status}${active ? " collab-agent-run--active" : ""}${hasOutput ? " collab-agent-run--has-output" : ""}`}>
      <div className="collab-agent-run__head"><span className="collab-agent-run__pulse" aria-hidden="true" /><strong>{runStatusLabel(status, c)}</strong>{canRespond && !detailed && <span className="collab-agent-run__decision"><button type="button" className="collab-agent-run__allow" onClick={() => onRespond({ allow: true })}><Check size={12} />{c("agree")}</button><button type="button" onClick={() => onRespond({ allow: false })}><Ban size={12} />{c("reject")}</button></span>}</div>
      {active
        ? <><p>{item.text}</p><div className="collab-agent-run__marquee" aria-label={runStatusLabel(status, c)}><div>
            <span>{c("agentStageContext")}</span><span>{c("agentStageTools")}</span><span>{c("agentStageShare")}</span><span>{c("agentStageContext")}</span>
          </div></div></>
        : hasOutput
          ? <p className="collab-agent-run__instruction">{item.text}</p>
          : <><p>{item.text}</p><div className="collab-agent-run__summary">{item.agentRunError || item.agentRunSummary || runStatusLabel(status, c)}</div></>}
    </div>
    {detailed && prompt.kind === "approval" && <div className="collab-agent-prompt"><ApprovalModal
      key={prompt.id}
      approval={{ id: prompt.id, tool: prompt.tool || "tool", subject: prompt.subject || "", reason: prompt.reason }}
      onAnswer={(allow, session, persist) => onRespond({ allow, session, persist })}
      onStop={() => onRespond({ allow: false })}
    /></div>}
    {detailed && prompt.kind === "ask" && <div className="collab-agent-prompt"><AskCard
      key={prompt.id}
      ask={{ id: prompt.id, questions: prompt.questions || [] }}
      onAnswer={(_, answers) => onRespond({ answering: true, answers })}
      onDismiss={() => onRespond({ answering: true, answers: [] })}
      onStop={() => onRespond({ allow: false })}
    /></div>}
  </div>;
}

function timelineDOMID(id: string): string {
  return `collab-item-${encodeURIComponent(id)}`;
}

function ReferenceCards({ item, items, c, expanded, onToggle, onJump }: { item: CollaborationTimelineItem; items: Map<string, CollaborationTimelineItem>; c: CollabCopy; expanded: Set<string>; onToggle(id: string): void; onJump(id: string): void }) {
  if (item.referenceIds.length === 0) return null;
  return <div className="collab-reference-list">
    {item.referenceIds.map((id) => {
      const reference = items.get(id);
      const open = expanded.has(id);
      return <div key={id} className="collab-reference-card">
        <button type="button" className="collab-reference-card__preview" aria-expanded={open} onClick={() => onToggle(id)}>
          <span><Reply size={12} /><strong>{reference?.actorName || c("referenceMissing")}</strong></span>
          <p className={open ? "collab-reference-card__text--open" : ""}>{reference?.text || c("referenceMissing")}</p>
          <small>{open ? <ChevronUp size={12} /> : <ChevronDown size={12} />}{open ? c("referenceCollapse") : c("referenceExpand")}</small>
        </button>
        {reference && <button type="button" className="collab-reference-card__jump" aria-label={c("referenceJump")} title={c("referenceJump")} onClick={() => onJump(id)}><ExternalLink size={13} /></button>}
      </div>;
    })}
  </div>;
}

export function CollaborationTimeline(props: CollaborationTimelineProps) {
  const { locale, t } = useI18n();
  const c = collabCopy(t);
  const [expandedReferences, setExpandedReferences] = useState<Set<string>>(new Set());
  const [requestAgentOpen, setRequestAgentOpen] = useState<string | null>(null);
  const [requestAgentPlacement, setRequestAgentPlacement] = useState<{ side: "above" | "below"; maxHeight: number }>({ side: "above", maxHeight: 260 });
  const requestAgentRef = useRef<HTMLDivElement>(null);
  const requestAgentTriggerRef = useRef<HTMLButtonElement>(null);
  const jumpFrame = useRef<number | null>(null);
  useEffect(() => () => {
    if (jumpFrame.current !== null) cancelAnimationFrame(jumpFrame.current);
  }, []);
  useEffect(() => {
    if (!requestAgentOpen) return;
    const close = (event: MouseEvent) => {
      if (!requestAgentRef.current || !requestAgentRef.current.contains(event.target as Node)) {
        setRequestAgentOpen(null);
      }
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [requestAgentOpen]);
  const closeRequestAgent = (restoreFocus = false) => {
    if (restoreFocus) requestAgentTriggerRef.current?.focus();
    setRequestAgentOpen(null);
  };
  const [highlightedId, setHighlightedId] = useState<string | null>(null);
  useEffect(() => {
    if (!highlightedId) return;
    const timer = window.setTimeout(() => setHighlightedId(null), 1800);
    return () => window.clearTimeout(timer);
  }, [highlightedId]);
  const visibleItems = useMemo(() => visibleCollaborationTimeline(props.items), [props.items]);
  const rawItems = useMemo(() => new Map(props.items.map((item) => [item.id, item])), [props.items]);
  const runByResult = useMemo(() => new Map(props.items.filter((item) => item.kind === "agent_result" && item.agentRunId).map((item) => [item.id, item.agentRunId as string])), [props.items]);
  const isVirtual = props.scrollContainer !== undefined && visibleItems.length > virtualTimelineThreshold;
  const getItemKey = useCallback((index: number) => visibleItems[index]?.id ?? index, [visibleItems]);
  const pinnedRows = useMemo(() => visibleItems.flatMap((item, index) =>
    item.id === requestAgentOpen || item.id === props.agentPrompt?.runId ? [index] : []), [visibleItems, requestAgentOpen, props.agentPrompt?.runId]);
  const rangeExtractor = useCallback((range: Parameters<typeof defaultRangeExtractor>[0]) =>
    [...new Set([...defaultRangeExtractor(range), ...pinnedRows])].sort((a, b) => a - b), [pinnedRows]);
  const virtualizer = useVirtualizer({
    count: isVirtual ? visibleItems.length : 0,
    getScrollElement: () => props.scrollContainer ?? null,
    estimateSize: () => virtualTimelineEstimateSize,
    overscan: virtualTimelineOverscan,
    getItemKey,
    rangeExtractor,
  });
  const virtualTimelineSize = virtualizer.getTotalSize();
  // After the virtualizer re-measures (new rows, taller images, expanded
  // references, long Agent output) re-pin the scroll owner when the reader was
  // following the bottom — the Workspace's own snap runs before measurements
  // settle, so this closes the gap without touching manual scrolling.
  useLayoutEffect(() => {
    if (!isVirtual || !props.scrollContainer || !props.stickRef?.current) return;
    const element = props.scrollContainer;
    element.scrollTop = element.scrollHeight;
  }, [isVirtual, props.scrollContainer, props.stickRef, virtualTimelineSize]);
  const toggleRequestAgent = (event: ReactMouseEvent<HTMLButtonElement>, itemId: string) => {
    if (requestAgentOpen === itemId) {
      closeRequestAgent();
      return;
    }
    requestAgentTriggerRef.current = event.currentTarget;
    const trigger = event.currentTarget.getBoundingClientRect();
    const scroll = event.currentTarget.closest(".collab-scroll")?.getBoundingClientRect();
    const above = Math.max(0, trigger.top - (scroll?.top ?? 0));
    const below = Math.max(0, (scroll?.bottom ?? window.innerHeight) - trigger.bottom);
    const side = below >= above ? "below" : "above";
    setRequestAgentPlacement({ side, maxHeight: Math.max(48, Math.min(260, Math.floor(Math.max(above, below) - 8))) });
    setRequestAgentOpen(itemId);
  };
  if (props.items.length === 0) return <div className="collab-empty">{c("empty")}</div>;
  const jumpTo = (id: string) => {
    // An agent_result reference maps to its owning agent_command; the target
    // row may not be mounted yet when the timeline is virtualized, so jump by
    // index into the visible rows instead of by DOM id.
    const targetId = runByResult.get(id) || id;
    const index = visibleItems.findIndex((entry) => entry.id === targetId);
    if (index < 0) return;
    if (jumpFrame.current !== null) cancelAnimationFrame(jumpFrame.current);
    if (props.stickRef) props.stickRef.current = false;
    setHighlightedId(targetId);
    if (isVirtual && props.scrollContainer) {
      // Best path: the target row is already measured (it was mounted before),
      // so scrollToIndex can align precisely.
      const measuredTarget = () => Boolean(virtualizer.measurementsCache[index]);
      if (measuredTarget()) {
        virtualizer.scrollToIndex(index, { align: "center", behavior: "auto" });
        return;
      }
      // The target row was never mounted, so scrollToIndex has no measurement
      // to align against. Scroll toward its estimated offset — the measured
      // prefix plus the estimate for the rest — and re-check every frame: each
      // scroll mounts a new window that the virtualizer measures, refining the
      // estimate until the target mounts and the precise re-align runs. A
      // second click after a failed deep-history jump continues from the now
      // measured region.
      let attempts = 0;
      let probe = -1;
      const converge = () => {
        attempts++;
        if (attempts > 30) return;
        if (measuredTarget()) {
          virtualizer.scrollToIndex(index, { align: "center", behavior: "auto" });
          return;
        }
        const cache = virtualizer.measurementsCache;
        let anchorIndex = -1;
        let anchorEnd = 0;
        for (let entryIndex = cache.length - 1; entryIndex >= 0; entryIndex--) {
          const entry = cache[entryIndex];
          if (entry) {
            anchorIndex = entryIndex;
            anchorEnd = entry.end;
            break;
          }
        }
        const estimated = anchorIndex < 0
          ? index * virtualTimelineEstimateSize
          : index <= anchorIndex
            ? cache[index]?.start ?? Math.max(0, anchorEnd - (anchorIndex - index) * virtualTimelineEstimateSize)
            : anchorEnd + (index - anchorIndex) * virtualTimelineEstimateSize;
        probe = Math.max(probe, estimated);
        const viewport = props.scrollContainer?.clientHeight || virtualTimelineEstimateSize;
        const maxOffset = Math.max(0, virtualizer.getTotalSize() - viewport);
        virtualizer.scrollToOffset(Math.min(probe, maxOffset), { align: "start" });
        probe += viewport;
        jumpFrame.current = window.requestAnimationFrame(converge);
      };
      converge();
      return;
    }
    const target = document.getElementById(timelineDOMID(targetId));
    if (!target) return;
    target.scrollIntoView({ behavior: "smooth", block: "center" });
  };

  const requestAgentEligible = (props.members || []).filter(
    (member) => member.online && member.id !== props.selfMemberId && !member.isSelf && Boolean(member.agent.id.trim()),
  );

  const renderEntry = (item: CollaborationTimelineItem): ReactNode => {
    const presence = presenceLabel(item, c);
    if (presence) return <div key={item.id} className="collab-presence-notice" role="status"><span aria-hidden="true" />{presence}<time dateTime={item.createdAt}>{timeLabel(item.createdAt, locale)}</time></div>;

    const own = item.actorId === props.selfMemberId;
    const selected = props.selectedIds.includes(item.id);
    const pending = props.pendingIntents[item.id];
    const transfer = props.transfers.find((value) => value.fileId === item.id && value.direction === (own ? "share" : "receive"));
    const incomingRequest = item.kind === "agent_request" && item.targetMemberId === props.selfMemberId && item.requestStatus !== "accepted" && item.requestStatus !== "rejected";
    const waitingAgentRun = own && item.kind === "agent_command" && item.agentRunStatus === "waiting_approval";
    const actor = (props.members || []).find((member) => member.id === item.actorId || member.agent.id === item.actorId);
    return (
      <article id={timelineDOMID(item.id)} key={item.id} className={`collab-message collab-message--${item.kind}${selected ? " collab-message--selected" : ""}${item.localPending ? " collab-message--pending" : ""}${item.id === highlightedId ? " collab-message--referenced" : ""}`}>
        <CollaborationAvatar name={item.actorName} src={item.actorAgent ? actor?.agent.avatar : actor?.avatar} agent={item.actorAgent} />
        <div className="collab-message-body">
          <header>
            <strong>{item.actorName}{own && !item.actorAgent ? ` (${c("you")})` : ""}</strong>
            <span className={`collab-kind collab-kind--${item.kind}`}>{item.kind === "contribution" ? contributionLabel(c, item.contributionKind) : c(kindCopy[item.kind])}</span>
            <time dateTime={item.createdAt}>{timeLabel(item.createdAt, locale)}</time>
            {item.syncStatus === "pending" && <span className="collab-sync collab-sync--pending"><RefreshCw size={11} />{c("pending")}</span>}
            {item.syncStatus === "failed" && <span className="collab-sync collab-sync--failed"><CircleAlert size={11} />{c("failedItem")}</span>}
          </header>
          {item.kind === "agent_command" ? <><AgentRunCard item={item} c={c} canRespond={waitingAgentRun} prompt={props.agentPrompt} onRespond={(response) => props.onRespondAgentRun(item, response)} />{item.agentRunOutput && <p className="collab-agent-output">{item.agentRunOutput}</p>}</> : item.kind === "file" ? <FileCard item={item} own={own} transfer={transfer} c={c} onReceive={() => props.onReceiveFile(item.id)} onPause={() => props.onPauseFile(item.id)} onResume={() => props.onResumeFile(item.id)} onRevoke={() => props.onRevokeFile(item.id)} onOpen={() => props.onOpenFile(item.id)} onReveal={() => props.onRevealFile(item.id)} previewFile={props.previewFile} /> : <p>{item.text}</p>}
          <ReferenceCards item={item} items={rawItems} c={c} expanded={expandedReferences} onToggle={(id) => setExpandedReferences((current) => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })} onJump={jumpTo} />
          {(item.handoffs || []).length > 0 && <div className="collab-handoffs">{item.handoffs?.map((handoff, index) => {
            const target = props.members?.find((member) => member.agent.id === handoff.targetAgentId);
            return <div key={`${handoff.targetAgentId}:${index}`}><Bot size={12} /><span><strong>{c("handoffTo", { name: target?.agent.name || handoff.targetAgentId })}</strong>{handoff.instruction}</span></div>;
          })}</div>}
          {item.kind === "agent_request" && !incomingRequest && <div className="collab-request-state">{c("waitingOwner")}</div>}
          {!item.localPending && !waitingAgentRun && <div className="collab-message-actions">
            <label className="collab-message-select">
              <input type="checkbox" checked={selected} onChange={() => props.onToggle(item.id)} aria-label={`${c("agentRespond")}: ${item.actorName}`} />
              <span><Check size={14} /></span>
            </label>
            <button type="button" aria-label={c("reply")} title={c("reply")} onClick={() => props.onReply(item)}><Reply size={14} /><span>{c("reply")}</span></button>
            <button type="button" aria-label={c("agree")} title={c("agree")} onClick={() => props.onAgree(item)}><ThumbsUp size={14} /><span>{c("agree")}</span></button>
            <button type="button" aria-label={c("agentRespond")} title={props.agentBusy ? c("agentQueueHint") : c("agentRespond")} onClick={() => props.onAgent(item)}><Bot size={14} /><span>{c("agentRespond")}</span></button>
            <div className="collab-request-agent" ref={requestAgentOpen === item.id ? requestAgentRef : undefined} onKeyDown={(event) => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeRequestAgent(true); } }}>
              <button type="button" aria-label={c("requestOther")} title={c("requestOther")} aria-expanded={requestAgentOpen === item.id} aria-controls={`${timelineDOMID(item.id)}-request-agents`} onClick={(event) => toggleRequestAgent(event, item.id)}>
                <span className="collab-double-bot" aria-hidden="true"><Bot size={10} /><Bot size={10} /></span>
              </button>
              {requestAgentOpen === item.id && <div id={`${timelineDOMID(item.id)}-request-agents`} className={`collab-request-agent__popup collab-request-agent__popup--${requestAgentPlacement.side}`} role="group" aria-label={c("requestOther")} style={{ maxHeight: requestAgentPlacement.maxHeight }}>
                {requestAgentEligible.length === 0
                  ? <p className="collab-request-agent__empty">{c("requestAgentEmpty")}</p>
                  : requestAgentEligible.map((member) => (
                    <button key={member.id} type="button" title={`${member.name} · ${member.agent.name} · ${member.agent.role || c("agentResponsibilityFallback")}`} onClick={() => { closeRequestAgent(true); props.onRequestAgent(item, member.id); }}>
                      <span>{member.name} · {member.agent.name} · {member.agent.role || c("agentResponsibilityFallback")}</span>
                    </button>
                  ))}
              </div>}
            </div>
          </div>}
          {incomingRequest && <div className="collab-request-actions">
            <button type="button" className="collab-action-accent" title={props.agentBusy ? c("agentQueueHint") : undefined} onClick={() => props.onAccept(item)}><UserRound size={13} />{c("acceptRun")}</button>
            <button type="button" title={props.agentBusy ? c("agentQueueHint") : undefined} onClick={() => {
              const next = window.prompt(c("modifyAccept"), item.text);
              if (next?.trim()) props.onAccept({ ...item, text: next.trim() });
            }}>{c("modifyAccept")}</button>
            <button type="button" onClick={() => props.onReject(item)}>{c("reject")}</button>
          </div>}
          {pending && <IntentCountdown intent={pending} connected={props.connected} onStart={props.onStartPending} onStop={() => props.onStopPending(item.id)} onEdit={(instruction) => props.onEditPending(item.id, instruction)} />}
        </div>
      </article>
    );
  };

  if (!isVirtual) {
    return <div className="collab-timeline-list">{visibleItems.map(renderEntry)}</div>;
  }
  return (
    <div className="collab-timeline-list">
    <div className="collab-timeline-sizer" style={{ height: virtualTimelineSize }}>
      {virtualizer.getVirtualItems().map((row) => {
        const item = visibleItems[row.index];
        if (!item) return null;
        return (
          <div key={row.key} data-index={row.index} ref={virtualizer.measureElement} className={`collab-timeline-row${item.id === requestAgentOpen ? " collab-timeline-row--menu" : ""}`} style={{ transform: `translateY(${row.start}px)` }}>
            {renderEntry(item)}
          </div>
        );
      })}
    </div>
    </div>
  );
}
