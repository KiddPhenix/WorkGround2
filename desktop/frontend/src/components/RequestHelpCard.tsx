import { memo, useCallback, useEffect, useRef, useState } from "react";
import { AlertCircle, ArrowRight, CheckCircle2, ChevronRight, ExternalLink, FolderOpen, Globe2, ImageIcon, LoaderCircle, RefreshCw, X } from "lucide-react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { ImageArtifact, RequestHelpStatus } from "../lib/requestHelp";
import { CodeViewer } from "./CodeViewer";
import { CopyButton } from "./CopyButton";

type ImageLoadState =
  | { phase: "idle" }
  | { phase: "loading" }
  | { phase: "loaded"; dataURL: string }
  | { phase: "error"; message: string };

export const RequestHelpCard = memo(function RequestHelpCard({
  status,
  args,
  output,
  error,
  entranceId,
}: {
  status: RequestHelpStatus;
  args: string;
  output?: string;
  error?: string;
  entranceId: string;
}) {
  const t = useT();
  const [showDetails, setShowDetails] = useState(false);
  const [showOverlay, setShowOverlay] = useState(false);
  const [imageState, setImageState] = useState<ImageLoadState>({ phase: "idle" });
  const [actionError, setActionError] = useState("");
  const imageArtifactKeyRef = useRef("");
  const imageLoadTokenRef = useRef(0);
  const overlayCloseRef = useRef<HTMLButtonElement>(null);

  const running = status.phase === "selecting" || status.phase === "attempting" || status.phase === "switching";
  const failed = status.phase === "failed";
  const completed = status.phase === "completed";
  const capability = status.capability === "image_generation"
    ? t("requestHelp.imageGeneration")
    : status.capability === "web_search"
      ? t("requestHelp.webSearch")
      : t("requestHelp.detectingCapability");
  const badge = completed
    ? t("requestHelp.completed")
    : failed
      ? t("requestHelp.failed")
      : status.phase === "switching"
        ? t("requestHelp.switching")
        : status.model
          ? t("requestHelp.claimed")
          : t("requestHelp.selecting");
  const hasDetails = Boolean(args || output || error);
  const CapabilityIcon = status.capability === "image_generation" ? ImageIcon : Globe2;
  const StatusIcon = completed
    ? CheckCircle2
    : failed
      ? AlertCircle
      : status.phase === "switching"
        ? RefreshCw
        : LoaderCircle;

  const artifact = status.capability === "image_generation" && completed ? status.artifact : undefined;
  const hasArtifact = Boolean(artifact);
  const artifactKey = artifact
    ? [artifact.task_id, artifact.path, artifact.size, artifact.width ?? 0, artifact.height ?? 0].join("\u0000")
    : "";

  const loadImage = useCallback(async (next: ImageArtifact) => {
    const token = ++imageLoadTokenRef.current;
    setImageState({ phase: "loading" });
    setActionError("");
    try {
      const dataURL = await app.RequestHelpImageDataURL(next.path);
      if (token !== imageLoadTokenRef.current) return;
      setImageState({ phase: "loaded", dataURL });
    } catch (err: unknown) {
      if (token !== imageLoadTokenRef.current) return;
      setImageState({ phase: "error", message: (err as { message?: string })?.message ?? String(err) });
    }
  }, []);

  // Load image data URL when artifact changes.
  useEffect(() => {
    if (!artifact) {
      imageLoadTokenRef.current += 1;
      setImageState({ phase: "idle" });
      setShowOverlay(false);
      setActionError("");
      imageArtifactKeyRef.current = "";
      return;
    }
    if (imageArtifactKeyRef.current === artifactKey) return;
    imageArtifactKeyRef.current = artifactKey;
    setShowOverlay(false);
    void loadImage(artifact);
    return () => { imageLoadTokenRef.current += 1; };
  }, [artifactKey, loadImage]);

  const handleRetry = useCallback(() => {
    if (artifact) void loadImage(artifact);
  }, [artifact, loadImage]);

  const runImageAction = useCallback(async (action: (path: string) => Promise<void>) => {
    if (!artifact) return;
    setActionError("");
    try {
      await action(artifact.path);
    } catch (err: unknown) {
      setActionError((err as { message?: string })?.message ?? String(err));
    }
  }, [artifact]);

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === "Escape") setShowOverlay(false);
  }, []);

  useEffect(() => {
    if (showOverlay) overlayCloseRef.current?.focus();
  }, [showOverlay]);

  return (
    <div
      className={`request-help${running ? " request-help--running" : ""}${completed ? " request-help--done" : ""}${failed ? " request-help--failed" : ""}`}
      data-entrance={entranceId}
      aria-live="polite"
    >
      <div className="request-help__main">
        <div className="request-help__title-row">
          <StatusIcon className={running ? "request-help__spin" : ""} size={17} aria-hidden="true" />
          <strong className="request-help__title">{t("requestHelp.title")}</strong>
          <span className={`request-help__badge${running && status.model && status.phase !== "switching" ? " request-help__badge--claimed" : ""}`}>{badge}</span>
        </div>

        <div className="request-help__route">
          {status.fromModel && <span className="request-help__model" title={status.fromModel}>{status.fromModel}</span>}
          {status.fromModel && status.model && <ArrowRight size={14} aria-hidden="true" />}
          <span className={`request-help__model${status.model ? " request-help__model--target" : " request-help__model--pending"}`} title={status.model}>
            {status.model || t("requestHelp.selectingModel")}
          </span>
        </div>

        <div className="request-help__meta">
          <span><CapabilityIcon size={14} aria-hidden="true" />{capability}</span>
          {status.attempt && status.total && <span>{t("requestHelp.attempt", { n: status.attempt, m: status.total })}</span>}
        </div>

        {failed && (status.error || error) && <div className="request-help__error">{status.error || error}</div>}

        {/* Image artifact preview */}
        {hasArtifact && (
          <div className="request-help__image" role="region" aria-label={t("requestHelp.imagePreview")}>
            {imageState.phase === "loading" && (
              <div className="request-help__image-placeholder">
                <LoaderCircle className="request-help__spin" size={20} aria-hidden="true" />
                <span>{t("requestHelp.loadingImage")}</span>
              </div>
            )}
            {imageState.phase === "loaded" && (
              <>
                <button
                  type="button"
                  className="request-help__thumb-btn"
                  onClick={() => setShowOverlay(true)}
                  aria-label={t("requestHelp.imagePreview")}
                >
                  <img
                    src={imageState.dataURL}
                    alt={t("requestHelp.imagePreview")}
                    className="request-help__thumb"
                    loading="lazy"
                  />
                </button>
                <div className="request-help__image-actions">
                  <button type="button" className="request-help__image-btn" onClick={() => void runImageAction((path) => app.RequestHelpOpenImage(path))} title={t("requestHelp.openImage")}>
                    <ExternalLink size={14} aria-hidden="true" />
                    {t("requestHelp.openImage")}
                  </button>
                  <button type="button" className="request-help__image-btn" onClick={() => void runImageAction((path) => app.RequestHelpRevealImage(path))} title={t("requestHelp.revealImage")}>
                    <FolderOpen size={14} aria-hidden="true" />
                    {t("requestHelp.revealImage")}
                  </button>
                  <CopyButton text={artifact?.path} className="request-help__image-btn" label={t("requestHelp.copyImagePath")} />
                </div>
                {actionError && <div className="request-help__image-action-error" role="alert">{actionError}</div>}
              </>
            )}
            {imageState.phase === "error" && (
              <div className="request-help__image-placeholder request-help__image-placeholder--error">
                <AlertCircle size={18} aria-hidden="true" />
                <span>{t("requestHelp.imageLoadFailed")}</span>
                <span className="request-help__image-error-detail">{imageState.message}</span>
                <button type="button" className="request-help__image-btn" onClick={handleRetry}>
                  <RefreshCw size={14} aria-hidden="true" />
                  {t("requestHelp.retry")}
                </button>
              </div>
            )}
          </div>
        )}

        {/* Overlay for full-size preview */}
        {showOverlay && imageState.phase === "loaded" && (
          <div
            className="request-help__overlay"
            onClick={() => setShowOverlay(false)}
            onKeyDown={handleKeyDown}
            role="dialog"
            aria-modal="true"
            aria-label={t("requestHelp.imagePreview")}
          >
            <button
              type="button"
              ref={overlayCloseRef}
              className="request-help__overlay-close"
              onClick={() => setShowOverlay(false)}
              aria-label={t("requestHelp.closePreview")}
            >
              <X size={20} aria-hidden="true" />
            </button>
            <img
              src={imageState.dataURL}
              alt={t("requestHelp.imagePreview")}
              className="request-help__overlay-img"
              onClick={(e) => e.stopPropagation()}
            />
          </div>
        )}
      </div>

      {hasDetails && (
        <>
          <button
            type="button"
            className="request-help__details-toggle"
            onClick={() => setShowDetails((value) => !value)}
            aria-expanded={showDetails}
          >
            <ChevronRight className={showDetails ? "request-help__chevron--open" : ""} size={13} />
            {t("requestHelp.details")}
          </button>
          {showDetails && (
            <div className="request-help__details">
              {args && <CodeViewer value={pretty(args)} language="json" maxHeight={120} />}
              {output && <CodeViewer value={output} maxHeight={220} />}
              {error && <div className="tool__err">{error}</div>}
            </div>
          )}
        </>
      )}
    </div>
  );
});

function pretty(json: string): string {
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
}
