import { memo, useCallback, useEffect, useRef, useState } from "react";
import { AlertCircle, ExternalLink, FolderOpen, LoaderCircle, Maximize2, RefreshCw, X } from "lucide-react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import { CopyButton } from "./CopyButton";
import { useArtifactStore } from "../store/artifacts";

type ImageLoadState =
  | { phase: "idle" }
  | { phase: "loading" }
  | { phase: "loaded"; dataURL: string }
  | { phase: "error"; message: string };

export const ArtifactImageCard = memo(function ArtifactImageCard({
  tabId,
  artifactId,
  name,
}: {
  tabId: string;
  artifactId: string;
  name: string;
}) {
  const t = useT();
  const [imageState, setImageState] = useState<ImageLoadState>({ phase: "idle" });
  const [showOverlay, setShowOverlay] = useState(false);
  const [actionError, setActionError] = useState("");
  const loadTokenRef = useRef(0);
  const overlayCloseRef = useRef<HTMLButtonElement>(null);

  const loadImage = useCallback(async () => {
    const token = ++loadTokenRef.current;
    setImageState({ phase: "loading" });
    setActionError("");
    try {
      const dataURL = await app.ArtifactImageDataURL(tabId, artifactId);
      if (token !== loadTokenRef.current) return;
      setImageState({ phase: "loaded", dataURL });
    } catch (err: unknown) {
      if (token !== loadTokenRef.current) return;
      setImageState({ phase: "error", message: (err as { message?: string })?.message ?? String(err) });
    }
  }, [tabId, artifactId]);

  useEffect(() => {
    loadTokenRef.current += 1;
    setImageState({ phase: "idle" });
    setShowOverlay(false);
    setActionError("");
    void loadImage();
    return () => {
      loadTokenRef.current += 1;
    };
  }, [loadImage]);

  const handleRetry = useCallback(() => {
    void loadImage();
  }, [loadImage]);

  const runAction = useCallback(
    async (action: (tabId: string, artifactId: string) => Promise<void>) => {
      setActionError("");
      try {
        await action(tabId, artifactId);
      } catch (err: unknown) {
        setActionError((err as { message?: string })?.message ?? String(err));
      }
    },
    [tabId, artifactId],
  );

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === "Escape") setShowOverlay(false);
  }, []);

  useEffect(() => {
    if (showOverlay) overlayCloseRef.current?.focus();
  }, [showOverlay]);

  return (
    <div className="artifact-image" role="region" aria-label={t("artifact.imagePreview")}>
      {imageState.phase === "loading" && (
        <div className="artifact-image__placeholder">
          <LoaderCircle className="artifact-image__spin" size={20} aria-hidden="true" />
          <span>{t("artifact.loadingImage")}</span>
        </div>
      )}
      {imageState.phase === "loaded" && (
        <>
          <button
            type="button"
            className="artifact-image__thumb-btn"
            onClick={() => setShowOverlay(true)}
            aria-label={t("artifact.imagePreview")}
          >
            <img
              src={imageState.dataURL}
              alt={name}
              className="artifact-image__thumb"
              loading="lazy"
            />
          </button>
          <div className="artifact-image__actions">
            <button
              type="button"
              className="artifact-image__btn"
              onClick={() => setShowOverlay(true)}
              title={t("artifact.viewFullscreen")}
            >
              <Maximize2 size={14} aria-hidden="true" />
              {t("artifact.viewFullscreen")}
            </button>
            <button
              type="button"
              className="artifact-image__btn"
              onClick={() => void runAction((tid, aid) => app.ArtifactOpenImage(tid, aid))}
              title={t("artifact.openImage")}
            >
              <ExternalLink size={14} aria-hidden="true" />
              {t("artifact.openImage")}
            </button>
            <button
              type="button"
              className="artifact-image__btn"
              onClick={() => void runAction((tid, aid) => app.ArtifactRevealImage(tid, aid))}
              title={t("artifact.revealImage")}
            >
              <FolderOpen size={14} aria-hidden="true" />
              {t("artifact.revealImage")}
            </button>
            <CopyButton text={name} className="artifact-image__btn" label={t("artifact.copyName")} />
          </div>
          {actionError && (
            <div className="artifact-image__action-error" role="alert">
              {actionError}
            </div>
          )}
        </>
      )}
      {imageState.phase === "error" && (
        <div className="artifact-image__placeholder artifact-image__placeholder--error">
          <AlertCircle size={18} aria-hidden="true" />
          <span>{t("artifact.imageLoadFailed")}</span>
          <span className="artifact-image__error-detail">{imageState.message}</span>
          <button type="button" className="artifact-image__btn" onClick={handleRetry}>
            <RefreshCw size={14} aria-hidden="true" />
            {t("artifact.retry")}
          </button>
        </div>
      )}

      {/* Fullscreen overlay */}
      {showOverlay && imageState.phase === "loaded" && (
        <div
          className="artifact-image__overlay"
          onClick={() => setShowOverlay(false)}
          onKeyDown={handleKeyDown}
          role="dialog"
          aria-modal="true"
          aria-label={t("artifact.imagePreview")}
        >
          <button
            type="button"
            ref={overlayCloseRef}
            className="artifact-image__overlay-close"
            onClick={() => setShowOverlay(false)}
            aria-label={t("artifact.closePreview")}
          >
            <X size={20} aria-hidden="true" />
          </button>
          <img
            src={imageState.dataURL}
            alt={name}
            className="artifact-image__overlay-img"
            onClick={(e) => e.stopPropagation()}
          />
        </div>
      )}
    </div>
  );
});

/** Renders all available image artifacts for one or more tool call sourceRunIds. */
export const ArtifactImagesForTool = memo(function ArtifactImagesForTool({
  tabId,
  toolIds,
}: {
  tabId: string;
  toolIds: string[];
}) {
  const store = useArtifactStore();
  const idSet = new Set(toolIds);

  const imageRecords = Object.values(store.artifacts).filter(
    (r) =>
      r.sessionId === tabId &&
      r.sourceRunId !== undefined &&
      idSet.has(r.sourceRunId) &&
      r.type === "image" &&
      r.status === "available",
  );

  if (imageRecords.length === 0) return null;

  return (
    <div className="artifact-images">
      {imageRecords.map((record) => (
        <ArtifactImageCard
          key={record.artifactId}
          tabId={tabId}
          artifactId={record.artifactId}
          name={record.name}
        />
      ))}
    </div>
  );
});
