import { memo, useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { AlertCircle, ImageOff, LoaderCircle, RefreshCw, X } from "lucide-react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import { findMessageImageArtifact, isRemoteMessageImage, referencedMessageImageArtifacts } from "../lib/messageMedia";
import type { ArtifactView } from "../lib/types";
import { useArtifactStore } from "../store/artifacts";

type LoadState =
  | { phase: "loading" }
  | { phase: "loaded"; url: string }
  | { phase: "error"; message: string };

const MessageImageFrame = memo(function MessageImageFrame({
  url,
  alt,
  caption,
  onError,
}: {
  url: string;
  alt: string;
  caption?: string;
  onError?: () => void;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const closeRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (open) closeRef.current?.focus();
  }, [open]);

  return (
    <span className="message-image">
      <button type="button" className="message-image__preview" onClick={() => setOpen(true)} aria-label={t("artifact.imagePreview")}>
        <img src={url} alt={alt} loading="lazy" draggable={false} onError={onError} />
      </button>
      {caption && <span className="message-image__caption">{caption}</span>}
      {open && createPortal(
        <div className="message-image__overlay" role="dialog" aria-modal="true" aria-label={t("artifact.imagePreview")} onClick={() => setOpen(false)} onKeyDown={(event) => event.key === "Escape" && setOpen(false)}>
          <button ref={closeRef} type="button" className="message-image__close" onClick={() => setOpen(false)} aria-label={t("artifact.closePreview")}>
            <X size={20} aria-hidden="true" />
          </button>
          <img src={url} alt={alt} onClick={(event) => event.stopPropagation()} />
        </div>,
        document.body,
      )}
    </span>
  );
});

export const MessageArtifactImage = memo(function MessageArtifactImage({
  tabId,
  artifact,
  alt,
}: {
  tabId: string;
  artifact: ArtifactView;
  alt?: string;
}) {
  const t = useT();
  const [state, setState] = useState<LoadState>({ phase: "loading" });
  const loadToken = useRef(0);

  const load = useCallback(async () => {
    const token = ++loadToken.current;
    setState({ phase: "loading" });
    try {
      const url = await app.ArtifactImageDataURL(tabId, artifact.artifactId);
      if (token === loadToken.current) setState({ phase: "loaded", url });
    } catch (error: unknown) {
      if (token !== loadToken.current) return;
      setState({ phase: "error", message: (error as { message?: string })?.message ?? String(error) });
    }
  }, [artifact.artifactId, tabId]);

  useEffect(() => {
    void load();
    return () => { loadToken.current += 1; };
  }, [load]);

  if (state.phase === "loaded") {
    const label = alt?.trim() || artifact.name;
    return <MessageImageFrame url={state.url} alt={label} caption={label !== artifact.name ? label : undefined} />;
  }
  if (state.phase === "error") {
    return (
      <div className="message-image-state message-image-state--error" role="alert" title={state.message}>
        <AlertCircle size={16} aria-hidden="true" />
        <span>{t("artifact.imageLoadFailed")}</span>
        <button type="button" onClick={() => void load()}><RefreshCw size={14} aria-hidden="true" />{t("artifact.retry")}</button>
      </div>
    );
  }
  return <div className="message-image-state"><LoaderCircle className="message-image-state__spin" size={16} aria-hidden="true" /><span>{t("artifact.loadingImage")}</span></div>;
});

export const MarkdownMessageImage = memo(function MarkdownMessageImage({
  tabId,
  source,
  alt,
}: {
  tabId?: string;
  source: string;
  alt?: string;
}) {
  const t = useT();
  const artifacts = useArtifactStore((state) => state.artifacts);
  const artifact = tabId ? findMessageImageArtifact(Object.values(artifacts), tabId, source) : undefined;
  const [remoteFailed, setRemoteFailed] = useState(false);

  useEffect(() => setRemoteFailed(false), [source]);

  if (artifact && tabId) return <MessageArtifactImage tabId={tabId} artifact={artifact} alt={alt} />;
  if (isRemoteMessageImage(source) && !remoteFailed) {
    return <MessageImageFrame url={source} alt={alt?.trim() || source} onError={() => setRemoteFailed(true)} />;
  }
  return (
    <span className="message-image-state message-image-state--unavailable" title={source}>
      <ImageOff size={16} aria-hidden="true" />
      <span>{remoteFailed ? t("artifact.imageLoadFailed") : alt?.trim() || t("artifact.imageLoadFailed")}</span>
    </span>
  );
});

export const ReferencedMessageImages = memo(function ReferencedMessageImages({ text, tabId }: { text: string; tabId?: string }) {
  const artifacts = useArtifactStore((state) => state.artifacts);
  if (!tabId) return null;
  const referenced = referencedMessageImageArtifacts(Object.values(artifacts), tabId, text);
  if (referenced.length === 0) return null;
  return (
    <div className="message-images">
      {referenced.map((artifact) => <MessageArtifactImage key={artifact.artifactId} tabId={tabId} artifact={artifact} />)}
    </div>
  );
});
