import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ChevronDown, ChevronUp, Send } from 'lucide-react';

import type { ComposerSubmitKey } from '../../lib/composerKeyboard';
import { isComposerSubmitKey } from '../../lib/composerKeyboard';
import type { Item, LiveStream } from '../../lib/useController';

export interface WorkChatInputProps {
  disabled: boolean;
  composerSubmitKey: ComposerSubmitKey;
  onSend: (text: string) => void | Promise<void>;
  displayItems: Item[];
  live?: LiveStream;
  running: boolean;
}

interface WorkReply {
  id: string;
  text: string;
  streaming: boolean;
}

export function latestWorkReply(items: Item[], live?: LiveStream): WorkReply | null {
  if (live) {
    return { id: live.id, text: live.text, streaming: true };
  }
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind === 'assistant') {
      return { id: item.id, text: item.text, streaming: item.streaming };
    }
  }
  return null;
}

export const WorkChatInput: React.FC<WorkChatInputProps> = ({
  disabled,
  composerSubmitKey,
  onSend,
  displayItems,
  live,
  running,
}) => {
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [replyOpen, setReplyOpen] = useState(false);
  const [replyBaseline, setReplyBaseline] = useState<string | undefined>(undefined);
  const [replyStarted, setReplyStarted] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const composingRef = useRef(false);

  const latestReply = useMemo(() => latestWorkReply(displayItems, live), [displayItems, live]);
  const replySignature = latestReply ? `${latestReply.id}:${latestReply.text}` : '';
  useEffect(() => {
    if (replyBaseline !== undefined && !replyStarted && replySignature !== replyBaseline) {
      setReplyStarted(true);
    }
  }, [replyBaseline, replySignature, replyStarted]);

  const reply = replyStarted ? latestReply : null;
  const waiting = replyBaseline !== undefined && !reply;

  // auto-grow
  const resizeTextarea = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    const h = Math.min(Math.max(el.scrollHeight, 0), 128);
    el.style.height = h > 0 ? `${h}px` : '';
    el.style.overflowY = el.scrollHeight > 128 ? 'auto' : 'hidden';
  }, []);

  useEffect(() => {
    resizeTextarea();
  }, [draft, resizeTextarea]);

  const submit = useCallback(async () => {
    const text = draft.trim();
    if (!text || busy || disabled) return;
    setBusy(true);
    setError(null);
    setReplyBaseline(replySignature);
    setReplyStarted(false);
    setReplyOpen(true);
    try {
      await onSend(text);
      setDraft('');
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setReplyBaseline(undefined);
      setReplyStarted(false);
      setReplyOpen(false);
    } finally {
      setBusy(false);
    }
  }, [draft, busy, disabled, onSend, replySignature]);

  const handleKeyDown = useCallback((event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (isComposerSubmitKey(event, composerSubmitKey, composingRef.current)) {
      event.preventDefault();
      void submit();
    }
  }, [composerSubmitKey, submit]);

  const canSubmit = draft.trim().length > 0 && !busy && !disabled;
  // submit key hint for aria-label
  const submitHint = composerSubmitKey === 'ctrl_enter' ? 'Ctrl+Enter' : 'Enter';

  return (
    <div className="wg2-work-chat" data-testid="work-chat-input">
      {/* Reply overlay */}
      {replyOpen && (reply || waiting) && (
        <div className="wg2-work-chat__reply" data-testid="work-chat-reply">
          <div className="wg2-work-chat__reply-header">
            <span className="wg2-work-chat__reply-title">
              {reply ? (reply.streaming ? '回复中…' : '最新回复') : '等待回复…'}
            </span>
            <button
              type="button"
              className="wg2-work-chat__reply-toggle"
              onClick={() => setReplyOpen(false)}
              aria-label="收起回复"
              title="收起回复"
            >
              <ChevronDown aria-hidden="true" size={16} strokeWidth={1.8} />
            </button>
          </div>
          <div
            className="wg2-work-chat__reply-body"
            data-testid="work-chat-reply-body"
          >
            {reply ? (
              <div className="wg2-work-chat__reply-text">
                {reply.text || (running ? '思考中…' : '无内容')}
              </div>
            ) : waiting || running ? (
              <div className="wg2-work-chat__reply-waiting">等待回复中…</div>
            ) : null}
          </div>
        </div>
      )}

      {/* Collapsed reply toggle */}
      {!replyOpen && (reply || waiting) && (
        <button
          type="button"
          className="wg2-work-chat__reply-peek"
          data-testid="work-chat-reply-peek"
          onClick={() => setReplyOpen(true)}
          aria-label="展开回复"
          title="展开回复"
        >
          <ChevronUp aria-hidden="true" size={14} strokeWidth={1.8} />
          <span className="wg2-work-chat__reply-peek-label">
            {reply?.streaming ? '回复中…' : waiting ? '等待回复…' : '查看回复'}
          </span>
        </button>
      )}

      {/* Input row */}
      <div className="wg2-work-chat__input-row">
        <textarea
          ref={textareaRef}
          className="wg2-work-chat__textarea"
          data-testid="work-chat-textarea"
          rows={1}
          value={draft}
          placeholder="输入问题或指令…"
          disabled={disabled || busy}
          onChange={(event) => {
            setDraft(event.target.value);
            if (error) setError(null);
          }}
          onCompositionStart={() => { composingRef.current = true; }}
          onCompositionEnd={() => { composingRef.current = false; }}
          onKeyDown={handleKeyDown}
        />
        <button
          type="button"
          className="wg2-work-chat__send"
          data-testid="work-chat-send"
          disabled={!canSubmit}
          aria-label={`发送 (${submitHint})`}
          title={`发送 (${submitHint})`}
          aria-busy={busy}
          onClick={submit}
        >
          <Send aria-hidden="true" size={16} strokeWidth={2} />
          {busy && <span className="wg2-work-chat__send-spinner" aria-hidden="true" />}
        </button>
      </div>

      {error && (
        <div className="wg2-work-chat__error" role="alert" data-testid="work-chat-error">
          {error}
        </div>
      )}
    </div>
  );
};

export default WorkChatInput;
