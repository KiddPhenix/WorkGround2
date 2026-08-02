import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Pause, Play } from 'lucide-react';

export interface WorkAutoStartCountdownProps {
  scope: string;
  paused: boolean;
  onPausedChange: (paused: boolean) => void;
  onStart: () => void | Promise<void>;
  durationSeconds?: number;
}

export const WorkAutoStartCountdown: React.FC<WorkAutoStartCountdownProps> = ({
  scope,
  paused,
  onPausedChange,
  onStart,
  durationSeconds = 20,
}) => {
  const duration = Math.max(1, Math.floor(durationSeconds));
  const [remaining, setRemaining] = useState(duration);
  const [busy, setBusy] = useState(false);
  const [started, setStarted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const startingRef = useRef(false);

  useEffect(() => {
    setRemaining(duration);
    setBusy(false);
    setStarted(false);
    setError(null);
    startingRef.current = false;
  }, [duration, scope]);

  const start = useCallback(async () => {
    if (startingRef.current || started) return;
    startingRef.current = true;
    setBusy(true);
    setError(null);
    try {
      await onStart();
      setStarted(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '启动失败，请重试');
      onPausedChange(true);
    } finally {
      startingRef.current = false;
      setBusy(false);
    }
  }, [onPausedChange, onStart, started]);

  useEffect(() => {
    if (paused || busy || started || error) return;
    if (remaining <= 0) {
      void start();
      return;
    }
    const timer = window.setTimeout(() => {
      setRemaining((current) => Math.max(0, current - 1));
    }, 1000);
    return () => window.clearTimeout(timer);
  }, [busy, error, paused, remaining, start, started]);

  const togglePause = () => {
    if (busy || started) return;
    if (paused) setError(null);
    onPausedChange(!paused);
  };
  const heading = error ? '启动失败' : paused ? '倒计时已暂停' : '信息已齐全';
  const detail = error
    ? '信息已保留，可以安全重试'
    : paused ? '修改完成后可继续倒计时' : `${remaining} 秒后自动开始`;

  return (
    <div
      className="work-auto-start"
      data-testid="work-auto-start"
      data-state={error ? 'error' : started || busy ? 'starting' : paused ? 'paused' : 'counting'}
      aria-live="polite"
    >
      <div className="work-auto-start__timer" aria-label={`剩余 ${remaining} 秒`}>
        <strong>{remaining}</strong>
        <span>秒</span>
      </div>
      <div className="work-auto-start__copy">
        <strong>{started || busy ? '正在开始工作' : heading}</strong>
        <span>{started || busy ? '正在提交最终信息…' : detail}</span>
      </div>
      <button
        type="button"
        className="work-auto-start__pause"
        onClick={togglePause}
        disabled={busy || started}
      >
        {paused ? <Play aria-hidden="true" size={15} /> : <Pause aria-hidden="true" size={15} />}
        {paused ? '继续倒计时' : '暂停并修改'}
      </button>
      <button
        type="button"
        className="work-auto-start__start"
        onClick={() => void start()}
        disabled={busy || started}
      >
        <Play aria-hidden="true" size={15} />
        {busy || started ? '正在开始…' : error ? '重试开始' : '立即开始'}
      </button>
      <progress
        className="work-auto-start__progress"
        max={duration}
        value={remaining}
        aria-label="自动开始倒计时进度"
      />
      {error ? <span className="work-auto-start__error" role="alert">{error}</span> : null}
    </div>
  );
};
