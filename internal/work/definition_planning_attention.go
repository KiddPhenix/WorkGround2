package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	definitionImpactNodes        = "task_nodes"
	definitionImpactDependencies = "task_dependencies"
	definitionImpactInputs       = "input_slots"
	definitionImpactArtifacts    = "artifact_slots"
)

type definitionPlanningAttention struct {
	RequestID string `json:"requestId"`
	Sequence  int    `json:"sequence"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	State     string `json:"state"`
}

type definitionPlanningPayload struct {
	Planning definitionPlanningAttention `json:"planning"`
}

// definitionProgressEmitter turns semantic planner fragments into ordered,
// idempotent transient events. The callback is safe for providers that invoke
// progress concurrently or repeat chunks.
func (s *Service) definitionProgressEmitter(workID, requestID string, revision int64) func(DefinitionPlanProgress) {
	var mu sync.Mutex
	sequence := 0
	seen := make(map[string]struct{})
	return func(progress DefinitionPlanProgress) {
		progress.Kind = strings.TrimSpace(progress.Kind)
		if progress.Kind == "raw" {
			progress.Text = trimPlanningRaw(progress.Text, 280)
		} else {
			progress.Text = trimPlanningText(progress.Text, 180)
		}
		if s == nil || progress.Kind == "" || progress.Text == "" {
			return
		}
		key := progress.Kind + "\x00" + progress.Text
		mu.Lock()
		if progress.Kind != "raw" {
			if _, ok := seen[key]; ok {
				mu.Unlock()
				return
			}
			seen[key] = struct{}{}
		}
		sequence++
		currentSequence := sequence
		mu.Unlock()

		state := "streaming"
		if progress.Kind == "clarification" {
			state = "waiting"
		} else if progress.Kind == "complete" {
			state = "complete"
		}
		payload, err := json.Marshal(definitionPlanningPayload{Planning: definitionPlanningAttention{
			RequestID: requestID,
			Sequence:  currentSequence,
			Kind:      progress.Kind,
			Text:      progress.Text,
			State:     state,
		}})
		if err != nil {
			return
		}
		s.sink.EmitWorkView(WorkViewEvent{
			SchemaVersion: WorkViewSchemaVersionV2,
			Type:          ViewAttention,
			WorkID:        workID,
			EventID:       fmt.Sprintf("definition-plan-%s-%03d", requestID, currentSequence),
			Revision:      revision,
			BaseRevision:  revision,
			RequestID:     requestID,
			Object: ObjectContext{
				Kind: ObjectDefinition, ID: workID, ParentID: workID, WorkID: workID,
			},
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		})
	}
}

func trimPlanningRaw(value string, maxRunes int) string {
	value = strings.TrimRight(value, "\x00")
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes])
}

func trimPlanningText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes-1]) + "…"
}

func nextDefinitionStructuralClarification(
	plan *DefinitionPlan,
	answers []DefinitionStructuralAnswer,
) (*DefinitionStructuralClarification, error) {
	if plan == nil || len(plan.StructuralQuestions) == 0 {
		return nil, nil
	}
	if len(plan.StructuralQuestions) > 1 {
		return nil, errors.New("structuralQuestions must contain at most one question")
	}
	answered := make(map[string]struct{}, len(answers))
	for _, answer := range answers {
		if id := strings.TrimSpace(answer.QuestionID); id != "" {
			answered[id] = struct{}{}
		}
	}
	question, err := validateDefinitionStructuralClarification(plan, plan.StructuralQuestions[0])
	if err != nil {
		return nil, err
	}
	if _, ok := answered[question.ID]; ok {
		return nil, nil
	}
	return question, nil
}

func validateDefinitionStructuralClarification(
	plan *DefinitionPlan,
	question DefinitionStructuralClarification,
) (*DefinitionStructuralClarification, error) {
	question.ID = strings.TrimSpace(question.ID)
	question.Impact = strings.TrimSpace(question.Impact)
	question.Question = strings.Join(strings.Fields(strings.TrimSpace(question.Question)), " ")
	question.Description = strings.Join(strings.Fields(strings.TrimSpace(question.Description)), " ")
	question.CustomPlaceholder = strings.TrimSpace(question.CustomPlaceholder)
	if !validDefinitionStructuralID(question.ID) {
		return nil, errors.New("structural question id must be a stable identifier")
	}
	switch question.Impact {
	case definitionImpactNodes, definitionImpactDependencies, definitionImpactInputs, definitionImpactArtifacts:
	default:
		return nil, fmt.Errorf("structural question %q has invalid impact", question.ID)
	}
	if question.Question == "" || utf8.RuneCountInString(question.Question) > 240 {
		return nil, fmt.Errorf("structural question %q must contain a concise question", question.ID)
	}
	if utf8.RuneCountInString(question.Description) > 320 {
		return nil, fmt.Errorf("structural question %q description is too long", question.ID)
	}
	if len(question.Flow) > 0 {
		return nil, fmt.Errorf("structural question %q must not provide a derived flow", question.ID)
	}
	if len(question.Options) < 2 || len(question.Options) > 4 {
		return nil, fmt.Errorf("structural question %q must contain 2 to 4 options", question.ID)
	}
	seen := make(map[string]struct{}, len(question.Options))
	customCount := 0
	for i := range question.Options {
		option := &question.Options[i]
		option.ID = strings.TrimSpace(option.ID)
		option.Label = strings.Join(strings.Fields(strings.TrimSpace(option.Label)), " ")
		option.Description = strings.Join(strings.Fields(strings.TrimSpace(option.Description)), " ")
		if !validDefinitionStructuralID(option.ID) {
			return nil, fmt.Errorf("structural question %q has an invalid option id", question.ID)
		}
		if _, ok := seen[option.ID]; ok {
			return nil, fmt.Errorf("structural question %q has duplicate option ids", question.ID)
		}
		seen[option.ID] = struct{}{}
		if option.Label == "" || utf8.RuneCountInString(option.Label) > 120 {
			return nil, fmt.Errorf("structural question %q has an invalid option label", question.ID)
		}
		if utf8.RuneCountInString(option.Description) > 180 {
			return nil, fmt.Errorf("structural question %q has an option description that is too long", question.ID)
		}
		if option.Recommended {
			return nil, fmt.Errorf("structural question %q must not recommend an inferable default", question.ID)
		}
		if option.Custom {
			customCount++
		}
	}
	if customCount > 1 {
		return nil, fmt.Errorf("structural question %q has multiple custom options", question.ID)
	}
	question.Flow = definitionPlanFlow(plan)
	if customCount == 0 {
		question.CustomPlaceholder = ""
	}
	return &question, nil
}

func validDefinitionStructuralID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func definitionPlanFlow(plan *DefinitionPlan) []string {
	if plan == nil {
		return nil
	}
	flow := make([]string, 0, min(len(plan.Nodes), 4))
	for _, node := range plan.Nodes {
		if title := trimPlanningText(node.Title, 36); title != "" {
			flow = append(flow, title)
		}
		if len(flow) == 4 {
			break
		}
	}
	return flow
}
