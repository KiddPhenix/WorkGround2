import { useEffect, useMemo, useRef, useState } from "react";
import type { WidgetMessage, WidgetModelInfo, WidgetSnapshot } from "../../lib/bridge";
import calibrationRail from "../../assets/widget-mode/calibration-rail.png";
import w2Mark from "../../assets/widget-mode/w2-mark.png";
import petSprites from "../../assets/widget-mode/w2-companion-sprites.png";
import {
  INFO_PAGES,
  availableWidgetInfoPages,
  formatCompactTokens,
  formatWidgetDuration,
  nextWidgetInfoPage,
  resolveWidgetInfoPage,
  shouldShowWidgetContext,
  widgetPetState,
  type WidgetInfoPage,
  type WidgetPetState,
} from "./widgetInfoCarouselState";
import "./widget-info-carousel.css";

const PAGE_KEY = "workground2.widget.infoPage.v1";

const PAGE_LABEL: Record<WidgetInfoPage, string> = {
  tokens: "TOKEN METER",
  clock: "LOCAL TIME",
  pet: "W2 COMPANION",
  idle: "IDLE TIMER",
  system: "SYSTEM TELEMETRY",
  models: "MODEL LINK",
};

const BRAND_ICONS = new Set(["anthropic", "deepseek", "gemini", "mistral", "ollama", "qwen", "xai"]);

const PET_FRAME: Record<WidgetPetState, number> = {
  idle: 0,
  working: 1,
  waiting: 2,
  success: 3,
  error: 4,
  offline: 5,
};

function readStoredPage(skin?: string): WidgetInfoPage {
  const fallback: WidgetInfoPage = skin === "pet" ? "pet" : "tokens";
  try {
    const skinValue = localStorage.getItem(`${PAGE_KEY}.${skin ?? "classic"}`) as WidgetInfoPage | null;
    const value = skinValue ?? (skin === "classic" || !skin ? localStorage.getItem(PAGE_KEY) as WidgetInfoPage | null : null);
    return value && INFO_PAGES.includes(value) ? value : fallback;
  } catch {
    return fallback;
  }
}

function storePage(page: WidgetInfoPage, skin?: string) {
  try {
    localStorage.setItem(`${PAGE_KEY}.${skin ?? "classic"}`, page);
  } catch {
    // Storage may be disabled in an embedded webview. The in-memory selection
    // still works, and a later click can safely retry persistence.
  }
}

function useClock() {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  return now;
}

function PageDots({ active }: { active?: WidgetInfoPage }) {
  return (
    <span className="widget-info__dots" aria-hidden="true">
      {INFO_PAGES.map((page) => <i key={page} className={page === active ? "is-active" : ""} />)}
    </span>
  );
}

function Header({ label, active }: { label: string; active?: WidgetInfoPage }) {
  return (
    <span className="widget-info__header">
      <span>{label}</span>
      <PageDots active={active} />
    </span>
  );
}

function ContextPage({ message, projectName, taskName }: {
  message?: WidgetMessage;
  projectName?: string;
  taskName?: string;
}) {
  return (
    <span className="widget-info__page widget-info__page--context">
      <Header label="TASK CONTEXT" />
      <img className="widget-info__mark" src={w2Mark} alt="WorkGround2" />
      <strong className="widget-info__project">{message?.projectName ?? projectName ?? "WorkGround2"}</strong>
      <span className="widget-info__caption">{message?.taskName ?? taskName ?? "TASK STATUS"}</span>
    </span>
  );
}

function TokensPage({ snapshot }: { snapshot: WidgetSnapshot }) {
  return (
    <span className="widget-info__page">
      <Header label={PAGE_LABEL.tokens} active="tokens" />
      <strong className="widget-info__value widget-info__value--yellow">{formatCompactTokens(snapshot.info.totalTokens)}</strong>
      <span className="widget-info__caption">TOTAL TOKENS{snapshot.info.tokenPartial ? " · PARTIAL" : ""}</span>
    </span>
  );
}

function ClockPage({ now }: { now: number }) {
  const date = new Date(now);
  return (
    <span className="widget-info__page widget-info__page--clock">
      <Header label={PAGE_LABEL.clock} active="clock" />
      <strong className="widget-info__value widget-info__value--clock">{date.toLocaleTimeString([], { hour12: false })}</strong>
      <span className="widget-info__caption">{date.toLocaleDateString([], { weekday: "short", month: "2-digit", day: "2-digit" }).toUpperCase()}</span>
    </span>
  );
}

function PetPage({ state }: { state: WidgetPetState }) {
  const frame = PET_FRAME[state];
  const column = frame % 3;
  const row = Math.floor(frame / 3);
  return (
    <span className="widget-info__page widget-info__page--pet">
      <Header label={PAGE_LABEL.pet} active="pet" />
      <span
        className="widget-info__pet"
        style={{ backgroundImage: `url(${petSprites})`, backgroundPosition: `${column * 50}% ${row === 0 ? 16 : 84}%` }}
        role="img"
        aria-label={`W2 companion ${state}`}
      />
      <span className={`widget-info__caption widget-info__caption--${state}`}>{state.toUpperCase()}</span>
    </span>
  );
}

function IdlePage({ snapshot, now }: { snapshot: WidgetSnapshot; now: number }) {
  const active = snapshot.info.idleSince && snapshot.isIdle;
  return (
    <span className="widget-info__page widget-info__page--timer">
      <Header label={PAGE_LABEL.idle} active="idle" />
      <strong className="widget-info__value widget-info__value--timer">{formatWidgetDuration(active ? now - snapshot.info.idleSince! : 0)}</strong>
      <span className="widget-info__caption">{active ? "WG2 IDLE" : "WG2 ACTIVE"}</span>
    </span>
  );
}

function Metric({ label, value, tone }: { label: string; value: string; tone?: "ok" | "warn" }) {
  const number = Math.max(0, Math.min(100, Number.parseInt(value, 10) || 0));
  return (
    <span className="widget-info__metric">
      <b>{label}</b><em className={tone ? `is-${tone}` : ""}>{value}</em>
      <i><span style={{ width: `${number}%` }} /></i>
    </span>
  );
}

function SystemPage({ snapshot }: { snapshot: WidgetSnapshot }) {
  const system = snapshot.info.system;
  return (
    <span className="widget-info__page widget-info__page--system">
      <Header label={PAGE_LABEL.system} active="system" />
      <span className="widget-info__metrics">
        <Metric label="NET" value={system.network === "online" ? "OK" : system.network === "offline" ? "OFF" : "--"} tone={system.network === "online" ? "ok" : "warn"} />
        <Metric label="CPU" value={`${system.cpu}%`} />
        <Metric label="MEM" value={`${system.memory}%`} />
      </span>
    </span>
  );
}

function BrandLogo({ model }: { model: WidgetModelInfo }) {
  if (BRAND_ICONS.has(model.brand)) {
    return <span className={`widget-info__model-logo widget-info__model-logo--${model.brand}`} aria-hidden="true" />;
  }
  const initials = model.brand === "openai" ? "OA" : model.brand === "groq" ? "GQ" : model.brand === "zhipu" ? "GLM" : model.model.slice(0, 2).toUpperCase();
  return <span className="widget-info__model-fallback" aria-hidden="true">{initials}</span>;
}

function ModelPage({ models, index }: { models: WidgetModelInfo[]; index: number }) {
  const model = models[index % models.length];
  return (
    <span className="widget-info__page widget-info__page--model">
      <Header label={PAGE_LABEL.models} active="models" />
      <span className="widget-info__model-row">
        <BrandLogo model={model} />
        <span className="widget-info__model-copy">
          <strong>{model.model}</strong>
          <span>{model.provider || model.brand}</span>
        </span>
      </span>
      <span className="widget-info__caption">{index + 1} / {models.length} CONNECTED</span>
    </span>
  );
}

export function WidgetInfoCarousel({ snapshot, message, projectName, taskName, skin }: {
  snapshot: WidgetSnapshot;
  message?: WidgetMessage;
  projectName?: string;
  taskName?: string;
  skin?: string;
}) {
  const pages = useMemo(() => availableWidgetInfoPages(snapshot), [snapshot.info.models.length, snapshot.info.system.available]);
  const [page, setPage] = useState<WidgetInfoPage>(() => readStoredPage(skin));
  const [showContext, setShowContext] = useState(false);
  const [modelIndex, setModelIndex] = useState(0);
  const seenContext = useRef("");
  const now = useClock();
  const contextKey = message ? `${message.id}:${message.revision}` : projectName || taskName ? `compose:${projectName ?? ""}:${taskName ?? ""}` : "";

  useEffect(() => {
    setPage(readStoredPage(skin));
  }, [skin]);

  useEffect(() => {
    if (shouldShowWidgetContext(seenContext.current, contextKey)) {
      seenContext.current = contextKey;
      setShowContext(true);
    } else if (!contextKey) {
      setShowContext(false);
    }
  }, [contextKey]);

  useEffect(() => {
    if (!pages.includes(page)) {
      const fallback = resolveWidgetInfoPage(page, pages);
      setPage(fallback);
      storePage(fallback, skin);
    }
  }, [page, pages, skin]);

  useEffect(() => {
    if (page !== "models" || snapshot.info.models.length < 2 || window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;
    const timer = window.setInterval(() => setModelIndex((value) => (value + 1) % snapshot.info.models.length), 3000);
    return () => window.clearInterval(timer);
  }, [page, snapshot.info.models.length]);

  const next = () => {
    if (showContext) {
      setShowContext(false);
      return;
    }
    const nextPage = nextWidgetInfoPage(page, pages);
    setPage(nextPage);
    storePage(nextPage, skin);
  };

  let content;
  if (showContext) {
    content = <ContextPage message={message} projectName={projectName} taskName={taskName} />;
  } else if (page === "clock") {
    content = <ClockPage now={now} />;
  } else if (page === "pet") {
    content = <PetPage state={widgetPetState(snapshot)} />;
  } else if (page === "idle") {
    content = <IdlePage snapshot={snapshot} now={now} />;
  } else if (page === "system") {
    content = <SystemPage snapshot={snapshot} />;
  } else if (page === "models" && snapshot.info.models.length > 0) {
    content = <ModelPage models={snapshot.info.models} index={modelIndex} />;
  } else {
    content = <TokensPage snapshot={snapshot} />;
  }

  const visibleLabel = showContext ? "TASK CONTEXT" : PAGE_LABEL[page];
  return (
    <button className="widget-info" type="button" onClick={next} aria-label={`${visibleLabel}. Click to show the next item`}>
      <img className="widget-info__rail" src={calibrationRail} alt="" aria-hidden="true" />
      {content}
    </button>
  );
}
