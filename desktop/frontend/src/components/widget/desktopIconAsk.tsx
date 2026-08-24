// desktopIconAsk renders the structured `ask` flow inside the desktop-icon
// popup. It walks multi-question asks one question at a time (header, progress,
// options, custom answer) with explicit back / next / submit navigation, and
// submits every question in one batch — so a half-answered ask never reaches
// the controller. It is a pure presentational component: the parent owns busy
// state and routes the batch through the idempotent icon action seam.
import { useEffect, useState } from "react";
import type { QuestionAnswer } from "../../lib/types";
import type { WidgetQuestion } from "../../lib/bridge";

export interface AskFlowProps {
  questions: WidgetQuestion[];
  busy: boolean;
  onAnswer: (answers: QuestionAnswer[]) => void;
}

function questionAnswered(question: WidgetQuestion, sel: Record<string, string[]>, custom: Record<string, string>): boolean {
  return (sel[question.id]?.length ?? 0) > 0 || (custom[question.id]?.trim() ?? "") !== "";
}

export function AskFlow({ questions, busy, onAnswer }: AskFlowProps) {
  const [step, setStep] = useState(0);
  const [sel, setSel] = useState<Record<string, string[]>>({});
  const [custom, setCustom] = useState<Record<string, string>>({});

  // A different ask must never reuse the previous ask's selections. The notice
  // id changes per ask, but keep the guard here too so a replaced question set
  // (same notice id, new revision) always starts fresh.
  const identity = JSON.stringify(questions.map((q) => ({
    id: q.id,
    header: q.header,
    prompt: q.prompt,
    multi: q.multi,
    options: q.options.map((option) => ({ label: option.label, description: option.description, value: option.value })),
  })));
  useEffect(() => {
    setStep(0);
    setSel({});
    setCustom({});
  }, [identity]);

  const question = questions[Math.min(step, questions.length - 1)];
  if (!question) return null;

  const isLast = step >= questions.length - 1;
  const progress = `${Math.min(step + 1, questions.length)}/${questions.length}`;

  const answersFrom = (): QuestionAnswer[] =>
    questions.map((q) => ({
      questionId: q.id,
      selected: custom[q.id]?.trim() ? [custom[q.id].trim()] : (sel[q.id] ?? []),
    }));

  const toggle = (label: string) => {
    setCustom((c) => ({ ...c, [question.id]: "" }));
    setSel((current) => {
      const picked = current[question.id] ?? [];
      if (question.multi) {
        return { ...current, [question.id]: picked.includes(label) ? picked.filter((x) => x !== label) : [...picked, label] };
      }
      return { ...current, [question.id]: [label] };
    });
  };

  const setTyped = (text: string) => {
    setCustom((c) => ({ ...c, [question.id]: text }));
    if (text.trim()) setSel((s) => ({ ...s, [question.id]: [] }));
  };

  const advance = () => {
    if (isLast) {
      onAnswer(answersFrom());
      return;
    }
    setStep((i) => Math.min(i + 1, questions.length - 1));
  };

  const selected = sel[question.id] ?? [];

  return (
    <div className="desktop-icon-popup__ask">
      <div className="desktop-icon-popup__ask-head">
        {question.header && <span className="desktop-icon-popup__ask-header">{question.header}</span>}
        {questions.length > 1 && <span className="desktop-icon-popup__ask-progress">{progress}</span>}
      </div>
      <p className="desktop-icon-popup__ask-prompt">{question.prompt}</p>
      <div className="desktop-icon-popup__answers" role="group" aria-label={question.prompt}>
        {question.options.map((option) => {
          const on = question.multi ? selected.includes(option.label) : selected[0] === option.label;
          return (
            <button
              key={option.value}
              type="button"
              aria-pressed={on}
              disabled={busy}
              onClick={() => toggle(option.label)}
            >
              <span>{option.label}</span>
              {option.description && <small>{option.description}</small>}
            </button>
          );
        })}
      </div>
      <label className="desktop-icon-popup__ask-custom">
        <span className="sr-only">自定义回答</span>
        <input
          value={custom[question.id] ?? ""}
          disabled={busy}
          placeholder="自定义回答"
          onInput={(event) => setTyped(event.currentTarget.value)}
        />
      </label>
      <div className="desktop-icon-popup__ask-nav">
        {step > 0 && (
          <button type="button" className="subtle" disabled={busy} onClick={() => setStep((i) => Math.max(0, i - 1))}>
            返回
          </button>
        )}
        <button
          type="button"
          className="desktop-icon-popup__ask-next"
          disabled={busy || !questionAnswered(question, sel, custom)}
          onClick={advance}
        >
          {isLast ? "提交" : "下一题"}
        </button>
      </div>
    </div>
  );
}
