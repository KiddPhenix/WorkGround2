package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"workground2/internal/event"
)

// AutoAnswer infers the chosen option(s) for a pending question using a real
// model turn. It is the answer-inference half of 17.5: RouteInteraction decides
// whether to answer at all, and AutoAnswer picks the concrete option. It never
// fabricates a selection outside the question's options and never answers a hard
// gate (the caller applies ClassifyHardGate first).
type AutoAnswer struct {
	model RoleModel
}

func NewAutoAnswer(model RoleModel) (*AutoAnswer, error) {
	if model == nil {
		return nil, errors.New("assistant: auto-answer model is required")
	}
	return &AutoAnswer{model: model}, nil
}

// InferAnswer asks the model to pick the best option for one question and
// returns it as a bounded AskAnswer whose Selected labels are validated against
// the question's options.
func (a *AutoAnswer) InferAnswer(ctx context.Context, mission string, question event.AskQuestion) (event.AskAnswer, error) {
	text, err := a.model.Complete(ctx, buildAnswerPrompt(mission, question))
	if err != nil {
		return event.AskAnswer{}, fmt.Errorf("assistant: auto-answer model: %w", err)
	}
	selected, err := ParseSelectedLabels(text, question)
	if err != nil {
		return event.AskAnswer{}, err
	}
	return event.AskAnswer{QuestionID: question.ID, Selected: selected}, nil
}

// OptionInference is the bounded model inference for one pending question: the
// validated selected option(s) plus the model's self-reported confidence and
// rationale. RouteInteraction consumes the confidence to choose between a direct
// answer and an isolated trial; Selected is the concrete answer to submit.
type OptionInference struct {
	Answer     event.AskAnswer `json:"answer"`
	Confidence float64         `json:"confidence"`
	Rationale  string          `json:"rationale,omitempty"`
}

// InferDecision asks the model to pick the best option for one question AND
// report how confident it is. The selection is validated against the question's
// options exactly like InferAnswer, so a fabricated or over-bounded selection is
// rejected. It is the answer-inference half of 17.5 with an explicit confidence
// signal for the decision router.
func (a *AutoAnswer) InferDecision(ctx context.Context, mission string, question event.AskQuestion) (OptionInference, error) {
	text, err := a.model.Complete(ctx, buildDecisionPrompt(mission, question))
	if err != nil {
		return OptionInference{}, fmt.Errorf("assistant: auto-answer decision model: %w", err)
	}
	return ParseOptionInference(text, question)
}

func buildAnswerPrompt(mission string, question event.AskQuestion) string {
	var b strings.Builder
	if strings.TrimSpace(mission) != "" {
		fmt.Fprintf(&b, "使命：%s\n", strings.TrimSpace(mission))
	}
	fmt.Fprintf(&b, "问题：%s\n选项：\n", strings.TrimSpace(question.Prompt))
	for i, opt := range question.Options {
		fmt.Fprintf(&b, "%d. %s", i+1, opt.Label)
		if strings.TrimSpace(opt.Description) != "" {
			fmt.Fprintf(&b, "（%s）", strings.TrimSpace(opt.Description))
		}
		b.WriteString("\n")
	}
	multi := ""
	if question.Multi {
		multi = "（可多选）"
	}
	fmt.Fprintf(&b, "请%s选择最能推进使命的选项，只输出一个 JSON 字符串数组：[\"选项标签\"]", multi)
	return b.String()
}

func buildDecisionPrompt(mission string, question event.AskQuestion) string {
	var b strings.Builder
	if strings.TrimSpace(mission) != "" {
		fmt.Fprintf(&b, "使命：%s\n", strings.TrimSpace(mission))
	}
	fmt.Fprintf(&b, "问题：%s\n选项：\n", strings.TrimSpace(question.Prompt))
	for i, opt := range question.Options {
		fmt.Fprintf(&b, "%d. %s", i+1, opt.Label)
		if strings.TrimSpace(opt.Description) != "" {
			fmt.Fprintf(&b, "（%s）", strings.TrimSpace(opt.Description))
		}
		b.WriteString("\n")
	}
	multi := ""
	if question.Multi {
		multi = "（可多选）"
	}
	fmt.Fprintf(&b, "请%s选择最能推进使命的选项，并评估该选择的置信度，只输出一个 JSON 对象：{\"selected\":[\"选项标签\"],\"confidence\":0.0到1.0,\"rationale\":\"一句话理由\"}", multi)
	return b.String()
}

// ParseOptionInference extracts the selected labels, confidence and rationale
// from model output. The selection is validated against the question's options
// (reusing ParseSelectedLabels); confidence is clamped to [0,1], where 0 means
// "unknown" and the router fails toward isolation or the most reversible option.
func ParseOptionInference(text string, question event.AskQuestion) (OptionInference, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return OptionInference{}, errors.New("assistant: auto-answer decision output has no JSON object")
	}
	raw := text[start : end+1]
	if len(raw) > 4096 {
		return OptionInference{}, errors.New("assistant: auto-answer decision output exceeds size limit")
	}
	var out struct {
		Selected   []string `json:"selected"`
		Confidence float64  `json:"confidence"`
		Rationale  string   `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return OptionInference{}, fmt.Errorf("assistant: malformed auto-answer decision: %w", err)
	}
	selectedJSON, err := json.Marshal(out.Selected)
	if err != nil {
		return OptionInference{}, fmt.Errorf("assistant: encode auto-answer selection: %w", err)
	}
	selected, err := ParseSelectedLabels(string(selectedJSON), question)
	if err != nil {
		return OptionInference{}, err
	}
	confidence := out.Confidence
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return OptionInference{
		Answer:     event.AskAnswer{QuestionID: question.ID, Selected: selected},
		Confidence: confidence,
		Rationale:  strings.TrimSpace(out.Rationale),
	}, nil
}

// ParseSelectedLabels extracts the selected option labels from model output and
// validates them against the question's options. An empty selection, an unknown
// label, or an oversized output is an explicit error.
func ParseSelectedLabels(text string, question event.AskQuestion) ([]string, error) {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return nil, errors.New("assistant: auto-answer output has no array")
	}
	raw := text[start : end+1]
	if len(raw) > 4096 {
		return nil, errors.New("assistant: auto-answer output exceeds size limit")
	}
	var selected []string
	if err := json.Unmarshal([]byte(raw), &selected); err != nil {
		return nil, fmt.Errorf("assistant: malformed auto-answer: %w", err)
	}
	valid := make(map[string]bool, len(question.Options))
	for _, opt := range question.Options {
		valid[strings.TrimSpace(opt.Label)] = true
	}
	cleaned := make([]string, 0, len(selected))
	for _, label := range selected {
		label = strings.TrimSpace(label)
		if !valid[label] {
			return nil, fmt.Errorf("assistant: auto-answer selected unknown option %q", label)
		}
		cleaned = append(cleaned, label)
	}
	if len(cleaned) == 0 {
		return nil, errors.New("assistant: auto-answer selected no option")
	}
	if !question.Multi && len(cleaned) != 1 {
		return nil, errors.New("assistant: single-choice question needs exactly one selection")
	}
	return cleaned, nil
}
