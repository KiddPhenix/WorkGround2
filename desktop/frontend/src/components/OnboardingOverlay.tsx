import { useCallback, useEffect, useRef, useState } from "react";
import logo from "../assets/logo.png";
import { useT } from "../lib/i18n";
import { app, openExternal } from "../lib/bridge";
import type { LocalCLIOptionView } from "../lib/types";

type SetupMode = "api" | "cli";
type SetupState = "idle" | "validating" | "scanning" | "connecting" | "skipping" | "error";

// Full-window first-run setup: users can validate a DeepSeek key now, wire a
// detected local CLI provider, or record provider access and finish setup later.
export function OnboardingOverlay({ onComplete }: { onComplete: () => void }) {
  const t = useT();
  const [mode, setMode] = useState<SetupMode>("api");
  const [value, setValue] = useState("");
  const [state, setState] = useState<SetupState>("idle");
  const [error, setError] = useState<string | null>(null);
  const [cliOptions, setCliOptions] = useState<LocalCLIOptionView[]>([]);
  const [selectedCli, setSelectedCli] = useState("");
  const cliLoadedRef = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);

  const busy = state === "validating" || state === "scanning" || state === "connecting" || state === "skipping";
  const selectedOption = cliOptions.find((option) => option.id === selectedCli);

  const loadCLIOptions = useCallback(async () => {
    setState("scanning");
    setError(null);
    try {
      const options = await app.ScanLocalCLIProviders();
      setCliOptions(options);
      cliLoadedRef.current = true;
      const firstInstalled = options.find((option) => option.installed);
      setSelectedCli((current) => {
        if (current && options.some((option) => option.id === current && option.installed)) return current;
        return firstInstalled?.id ?? "";
      });
      setState("idle");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setState("error");
    }
  }, []);

  useEffect(() => {
    if (mode === "cli" && !cliLoadedRef.current && state !== "scanning") {
      void loadCLIOptions();
    }
  }, [loadCLIOptions, mode, state]);

  const switchMode = useCallback((next: SetupMode) => {
    setMode(next);
    setError(null);
    if (state === "error") setState("idle");
  }, [state]);

  const submit = useCallback(async () => {
    const key = value.trim();
    if (!key) {
      setError(t("onboarding.error.empty"));
      setState("error");
      inputRef.current?.focus();
      return;
    }
    setState("validating");
    setError(null);
    try {
      await app.ConnectKey(key);
      onComplete();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      if (/status\s*401|status\s*403|invalid/i.test(msg)) {
        setError(t("onboarding.error.invalid"));
      } else if (/network|unreachable|timeout|dial/i.test(msg)) {
        setError(t("onboarding.error.network"));
      } else {
        setError(msg || t("onboarding.error.unknown", { msg: "" }));
      }
      setState("error");
      inputRef.current?.focus();
      inputRef.current?.select();
    }
  }, [t, value, onComplete]);

  const connectCLI = useCallback(async () => {
    if (!selectedOption?.installed) {
      setError(t("onboarding.cli.error.none"));
      setState("error");
      return;
    }
    setState("connecting");
    setError(null);
    try {
      await app.ConnectLocalCLIProvider(selectedOption.id);
      onComplete();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setState("error");
    }
  }, [onComplete, selectedOption, t]);

  const skip = useCallback(async () => {
    setState("skipping");
    setError(null);
    try {
      await app.SkipOnboarding();
      onComplete();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setState("error");
    }
  }, [onComplete]);

  return (
    <div className="onboarding">
      <div className="onboarding__card">
        <img src={logo} className="onboarding__logo" alt="WorkGround2" draggable={false} />
        <div className="onboarding__title">{t("onboarding.title")}</div>
        <div className="onboarding__tag">{t("onboarding.tagline")}</div>

        <div className="onboarding__modes" role="tablist" aria-label={t("onboarding.mode.label")}>
          <button
            type="button"
            role="tab"
            className={`onboarding__mode ${mode === "api" ? "onboarding__mode--active" : ""}`}
            aria-selected={mode === "api"}
            onClick={() => switchMode("api")}
            disabled={busy}
          >
            {t("onboarding.mode.api")}
          </button>
          <button
            type="button"
            role="tab"
            className={`onboarding__mode ${mode === "cli" ? "onboarding__mode--active" : ""}`}
            aria-selected={mode === "cli"}
            onClick={() => switchMode("cli")}
            disabled={busy}
          >
            {t("onboarding.mode.cli")}
          </button>
        </div>

        {mode === "api" ? (
          <>
            <label className="onboarding__label" htmlFor="onboarding-key">
              {t("onboarding.inputLabel")}
            </label>
            <input
              id="onboarding-key"
              ref={inputRef}
              className="onboarding__input"
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder={t("onboarding.inputPlaceholder")}
              value={value}
              onChange={(e) => {
                setValue(e.target.value);
                if (state === "error") setState("idle");
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !busy) {
                  e.preventDefault();
                  void submit();
                }
              }}
              disabled={busy}
            />
          </>
        ) : (
          <div className="onboarding__cli-panel">
            <div className="onboarding__cli-head">
              <div>
                <div className="onboarding__label">{t("onboarding.cli.label")}</div>
                <div className="onboarding__hint">{t("onboarding.cli.hint")}</div>
              </div>
              <button type="button" className="onboarding__link" onClick={() => void loadCLIOptions()} disabled={busy}>
                {state === "scanning" ? t("onboarding.cli.scanning") : t("onboarding.cli.rescan")}
              </button>
            </div>
            <div className="onboarding__cli-list">
              {state === "scanning" && cliOptions.length === 0 ? (
                <div className="onboarding__cli-empty">
                  <span className="onboarding__spinner" />
                  {t("onboarding.cli.scanning")}
                </div>
              ) : cliOptions.length === 0 ? (
                <div className="onboarding__cli-empty">{t("onboarding.cli.empty")}</div>
              ) : (
                cliOptions.map((option) => {
                  const optionArgs = option.args ?? [];
                  return (
                    <button
                      type="button"
                      key={option.id}
                      className={`onboarding__cli-option ${selectedCli === option.id ? "onboarding__cli-option--active" : ""}`}
                      onClick={() => option.installed && setSelectedCli(option.id)}
                      disabled={busy || !option.installed}
                    >
                      <span className="onboarding__cli-row">
                        <span className="onboarding__cli-name">{option.name}</span>
                        <span className={`onboarding__cli-status ${option.installed ? "onboarding__cli-status--ok" : ""}`}>
                          {option.installed ? t("onboarding.cli.installed") : t("onboarding.cli.missing")}
                        </span>
                      </span>
                      <span className="onboarding__cli-command">{option.command}</span>
                      <span className="onboarding__cli-meta">
                        {optionArgs.length ? optionArgs.join(" ") : t("onboarding.cli.noArgs")}
                        {option.version ? ` · ${option.version}` : ""}
                        {!option.installed && option.error ? ` · ${option.error}` : ""}
                      </span>
                    </button>
                  );
                })
              )}
            </div>
          </div>
        )}

        {state === "error" && error && (
          <div className="onboarding__error" role="alert">
            {error}
          </div>
        )}

        {mode === "api" ? (
          <button className="onboarding__submit" onClick={() => void submit()} disabled={busy}>
            {state === "validating" ? (
              <>
                <span className="onboarding__spinner" />
                {t("onboarding.validating")}
              </>
            ) : (
              t("onboarding.submit")
            )}
          </button>
        ) : (
          <button className="onboarding__submit" onClick={() => void connectCLI()} disabled={busy || !selectedOption?.installed}>
            {state === "connecting" ? (
              <>
                <span className="onboarding__spinner" />
                {t("onboarding.cli.connecting")}
              </>
            ) : (
              t("onboarding.cli.connect")
            )}
          </button>
        )}

        {mode === "api" ? (
          <div className="onboarding__links">
            <button
              type="button"
              className="onboarding__link"
              onClick={() => openExternal("https://platform.deepseek.com/api_keys")}
              disabled={busy}
            >
              {t("onboarding.getKey")}
            </button>
            <span className="onboarding__sep">·</span>
            <span className="onboarding__privacy">{t("onboarding.privacy")}</span>
          </div>
        ) : (
          <div className="onboarding__links">
            <span className="onboarding__privacy">{t("onboarding.cli.privacy")}</span>
          </div>
        )}

        <button type="button" className="onboarding__skip" onClick={() => void skip()} disabled={busy}>
          {t("onboarding.skip")}
        </button>
      </div>
    </div>
  );
}
