package assistant

import (
	"context"
	"testing"

	"workground2/internal/event"
)

func TestAutoAnswerInferPicksValidOption(t *testing.T) {
	model := RoleModelFunc(func(_ context.Context, prompt string) (string, error) {
		return `["B"]`, nil
	})
	aa, err := NewAutoAnswer(model)
	if err != nil {
		t.Fatal(err)
	}
	question := event.AskQuestion{
		ID: "q1", Prompt: "pick a channel", Multi: false,
		Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
	}
	answer, err := aa.InferAnswer(context.Background(), "grow the project", question)
	if err != nil {
		t.Fatal(err)
	}
	if answer.QuestionID != "q1" || len(answer.Selected) != 1 || answer.Selected[0] != "B" {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestParseSelectedLabelsRejectsInvalid(t *testing.T) {
	question := event.AskQuestion{
		ID: "q1", Prompt: "pick", Multi: false,
		Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
	}
	cases := []string{
		"no array",
		`["C"]`,     // unknown option
		`[]`,        // empty
		`["A","B"]`, // multi on single-choice
	}
	for _, c := range cases {
		if _, err := ParseSelectedLabels(c, question); err == nil {
			t.Fatalf("ParseSelectedLabels(%q) = nil error, want rejection", c)
		}
	}
	if got, err := ParseSelectedLabels(`["A"]`, question); err != nil || len(got) != 1 || got[0] != "A" {
		t.Fatalf("valid parse = %v err=%v", got, err)
	}
}

func TestAutoAnswerPropagatesModelError(t *testing.T) {
	aa, _ := NewAutoAnswer(RoleModelFunc(func(_ context.Context, prompt string) (string, error) {
		return "", context.DeadlineExceeded
	}))
	if _, err := aa.InferAnswer(context.Background(), "m", event.AskQuestion{ID: "q", Prompt: "p", Options: []event.AskOption{{Label: "A"}}}); err == nil {
		t.Fatal("InferAnswer should propagate the model error")
	}
}

func TestParseOptionInference(t *testing.T) {
	question := event.AskQuestion{
		ID: "q1", Prompt: "pick", Multi: false,
		Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
	}
	inf, err := ParseOptionInference(`{"selected":["B"],"confidence":0.9,"rationale":"B is better"}`, question)
	if err != nil {
		t.Fatal(err)
	}
	if inf.Answer.QuestionID != "q1" || len(inf.Answer.Selected) != 1 || inf.Answer.Selected[0] != "B" {
		t.Fatalf("answer = %+v", inf.Answer)
	}
	if inf.Confidence != 0.9 || inf.Rationale != "B is better" {
		t.Fatalf("inference = %+v", inf)
	}

	// Confidence is clamped into [0,1].
	if got, err := ParseOptionInference(`{"selected":["A"],"confidence":1.7}`, question); err != nil || got.Confidence != 1 {
		t.Fatalf("upper clamp = %+v err=%v", got, err)
	}
	if got, err := ParseOptionInference(`{"selected":["A"],"confidence":-0.4}`, question); err != nil || got.Confidence != 0 {
		t.Fatalf("lower clamp = %+v err=%v", got, err)
	}

	// A fabricated selection outside the options is rejected.
	if _, err := ParseOptionInference(`{"selected":["C"],"confidence":0.9}`, question); err == nil {
		t.Fatal("ParseOptionInference accepted unknown option")
	}
}
