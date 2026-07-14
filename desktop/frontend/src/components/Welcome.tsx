import { useT } from "../lib/i18n";
import { useEffect, useMemo } from "react";
import { buildWelcomeModel, type WelcomeModel, type WelcomeTranslate } from "../lib/welcome";
import { useWorkspaceWelcome, type WorkspaceWelcomeTarget } from "../lib/useWorkspaceWelcome";

const logoWordmark = new URL("../assets/logo-wordmark.png", import.meta.url).href;

// Welcome is the empty-state landing: a one-liner, the input affordances
// (/ commands, @ files, Enter), and a few clickable example prompts that send
// immediately so a first turn is one click away.

export function Welcome({
  onPrompt,
  onDraft,
  target,
  variant = "default",
}: {
  onPrompt: (text: string) => void;
  onDraft?: (text: string) => void;
  target?: WorkspaceWelcomeTarget;
  variant?: "default" | "creation";
}) {
  const t = useT();
  if (variant === "creation") {
    const cards = [
      {
        icon: "plan",
        title: t("welcome.creation.explainTitle"),
        body: t("welcome.creation.explainBody"),
      },
      {
        icon: "html",
        title: t("welcome.creation.gitTitle"),
        body: t("welcome.creation.gitBody"),
      },
      {
        icon: "think",
        title: t("welcome.creation.bugTitle"),
        body: t("welcome.creation.bugBody"),
      },
    ];
    return (
      <div className="welcome welcome--creation">
        <h2 className="welcome-creation__headline">
          <span>{t("welcome.creation.titlePrimary")}</span>
          <span>{t("welcome.creation.titleSecondary")}</span>
        </h2>
        <div className="welcome-creation__cards">
          {cards.map((card) => (
            <button key={card.title} className="welcome-creation__card" onClick={() => onPrompt(card.title)}>
              <span className="welcome-creation__icon">{card.icon}</span>
              <strong>{card.title}</strong>
              <span>{card.body}</span>
            </button>
          ))}
        </div>
      </div>
    );
  }
  const adaptiveKey = `${target?.scope ?? "global"}:${target?.workspaceRoot ?? ""}:${target?.tabId ?? ""}:${target?.sessionKey ?? ""}`;
  return <AdaptiveWelcome key={adaptiveKey} target={target} onDraft={onDraft ?? onPrompt} />;
}

function AdaptiveWelcome({ target, onDraft }: { target?: WorkspaceWelcomeTarget; onDraft: (text: string) => void }) {
  const t = useT();
  const fallbackTarget = useMemo<WorkspaceWelcomeTarget>(() => ({
    tabId: target?.tabId ?? "",
    scope: target?.scope ?? "global",
    workspaceRoot: target?.workspaceRoot ?? "",
    workspaceName: target?.workspaceName || t("welcome.adaptive.workspace"),
    sessionKey: target?.sessionKey ?? target?.tabId ?? "global",
  }), [t, target?.scope, target?.sessionKey, target?.tabId, target?.workspaceName, target?.workspaceRoot]);
  const context = useWorkspaceWelcome(fallbackTarget);
  const model = useMemo(() => buildWelcomeModel({
    view: context.view,
    fallbackName: fallbackTarget.workspaceName,
    scope: fallbackTarget.scope,
    loadState: context.loadState,
    globalVisits: context.globalVisits,
    workspaceVisits: context.workspaceVisits,
    delightEnabled: context.delightEnabled,
    reducedMotion: context.reducedMotion,
    seenDelights: context.seenDelights,
  }, t as WelcomeTranslate), [context.delightEnabled, context.globalVisits, context.loadState, context.reducedMotion, context.seenDelights, context.view, context.workspaceVisits, fallbackTarget.scope, fallbackTarget.workspaceName, t]);

  useEffect(() => {
    if (model.delight) context.markDelightSeen(model.delight.id);
  }, [context.markDelightSeen, model.delight]);

  return (
    <AdaptiveWelcomeView
      model={model}
      onDraft={onDraft}
      onRetry={context.retry}
      onDisableDelight={context.disableDelight}
      canRetry={context.loadState === "error" || context.loadState === "stale"}
    />
  );
}

export function AdaptiveWelcomeView({
  model,
  onDraft,
  onRetry,
  onDisableDelight,
  canRetry = false,
}: {
  model: WelcomeModel;
  onDraft: (text: string) => void;
  onRetry?: () => void;
  onDisableDelight?: () => void;
  canRetry?: boolean;
}) {
  const t = useT();
  return (
    <div className="welcome welcome--brand welcome--adaptive">
      <span className="welcome__brand">
        <img src={logoWordmark} className="welcome__brand-logo" alt="WorkGround2" draggable={false} />
      </span>
      {model.delight?.kind === "art" && <pre className="welcome-adaptive__art" aria-hidden="true">{model.delight.text}</pre>}
      <h2
        className={`welcome__title welcome-adaptive__workspace${model.delight?.kind === "microglitch" ? " welcome-adaptive__workspace--glitch" : ""}`}
        data-text={model.workspaceName}
      >
        {model.workspaceName}
      </h2>
      <p className="welcome-adaptive__summary">{model.summary}</p>

      <button type="button" className="welcome-adaptive__primary" onClick={() => onDraft(model.primary.prompt)}>
        <span>{model.primary.label}</span>
        <span aria-hidden="true">→</span>
      </button>

      <div className="welcome-adaptive__secondary" aria-label={t("welcome.adaptive.secondaryLabel")}>
        {model.secondary.map((action) => (
          <button type="button" key={action.id} onClick={() => onDraft(action.prompt)}>{action.label}</button>
        ))}
      </div>

      <div className={`welcome-adaptive__status welcome-adaptive__status--${model.statusTone}`} role={model.statusTone === "warning" ? "status" : undefined}>
        <span>{model.status}</span>
        {canRetry && onRetry && <button type="button" onClick={onRetry}>{t("welcome.adaptive.retry")}</button>}
      </div>

      {model.delight && model.delight.kind !== "art" && model.delight.kind !== "microglitch" && (
        <div className={`welcome-adaptive__delight welcome-adaptive__delight--${model.delight.kind}`}>
          <span aria-hidden={model.delight.kind === "emoticon" ? "true" : undefined}>{model.delight.text}</span>
          {onDisableDelight && (
            <button type="button" onClick={onDisableDelight} aria-label={t("welcome.adaptive.disableDelight")}>×</button>
          )}
        </div>
      )}
      {model.delight?.kind === "microglitch" && onDisableDelight && (
        <button type="button" className="welcome-adaptive__disable-delight" onClick={onDisableDelight}>
          {t("welcome.adaptive.disableDelight")}
        </button>
      )}

      <div className="welcome__hints" aria-label={t("welcome.adaptive.inputHints")}>
        <span><kbd>/</kbd> {t("welcome.hintCommands")}</span>
        <span><kbd>@</kbd> {t("welcome.hintFiles")}</span>
      </div>
    </div>
  );
}
