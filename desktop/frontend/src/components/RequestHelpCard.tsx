import { memo, useState } from "react";
import { AlertCircle, ArrowRight, CheckCircle2, ChevronRight, Globe2, ImageIcon, LoaderCircle, RefreshCw } from "lucide-react";
import { useT } from "../lib/i18n";
import type { RequestHelpStatus } from "../lib/requestHelp";
import { CodeViewer } from "./CodeViewer";

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
