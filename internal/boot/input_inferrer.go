package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"workground2/internal/provider"
	"workground2/internal/work"
)

// bootInputInferrer proposes reviewable input drafts through the configured
// model. It does not write Work state or execute tools.
type bootInputInferrer struct {
	prov        provider.Provider
	temperature float64
	maxTokens   int
	llmLog      *workLLMInteractionLogger
}

func newBootInputInferrer(prov provider.Provider, temperature float64, maxTokens int, llmLog *workLLMInteractionLogger) *bootInputInferrer {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	return &bootInputInferrer{prov: prov, temperature: temperature, maxTokens: maxTokens, llmLog: llmLog}
}

func (p *bootInputInferrer) InferInputs(ctx context.Context, input work.InputInferencePlanInput) (*work.InferWorkInputsResult, error) {
	if p == nil || p.prov == nil {
		return nil, work.ErrInputInferrerUnavailable
	}
	contextJSON, err := buildInputInferenceContext(input)
	if err != nil {
		return nil, fmt.Errorf("boot: InferInputs context: %w", err)
	}
	workID := ""
	locale := ""
	if input.Work != nil {
		workID, locale = input.Work.ID, input.Work.Locale
	}
	var lastRaw string
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("boot: InferInputs: %w", err)
		}
		messages := buildInputInferenceMessages(contextJSON, locale, attempt, lastRaw, lastErr)
		attemptNo := attempt + 1
		iid := interactionID("input-inference", workID, attemptNo)
		p.llmLog.logRequest(iid, "input-inference", workID, p.prov.Name(), attemptNo, messages, p.temperature, p.maxTokens)
		raw, streamErr := p.stream(ctx, messages)
		if streamErr != nil {
			p.llmLog.logResponse(iid, "input-inference", workID, attemptNo, raw, streamErr)
			return nil, streamErr
		}
		plan, parseErr := parseInputInference(raw, input.Targets)
		p.llmLog.logResponse(iid, "input-inference", workID, attemptNo, raw, parseErr)
		if parseErr == nil {
			return plan, nil
		}
		lastRaw, lastErr = raw, parseErr
	}
	return nil, fmt.Errorf("boot: InferInputs: invalid model response: %w", lastErr)
}

func buildInputInferenceContext(input work.InputInferencePlanInput) ([]byte, error) {
	type submittedInput struct {
		Label string          `json:"label"`
		Kind  work.InputKind  `json:"kind"`
		Value json.RawMessage `json:"value"`
		Extra string          `json:"extra,omitempty"`
	}
	contextValue := struct {
		Goal      string                      `json:"goal"`
		Prompt    string                      `json:"originalRequest,omitempty"`
		Nodes     []work.NodeDef              `json:"nodes"`
		Submitted []submittedInput            `json:"submittedInputs"`
		Targets   []work.InputInferenceTarget `json:"targets"`
	}{Submitted: []submittedInput{}, Targets: input.Targets}
	if input.Work != nil {
		contextValue.Prompt = input.Work.Prompt
	}
	if input.Definition != nil {
		contextValue.Goal = input.Definition.Goal
		contextValue.Nodes = input.Definition.Nodes
	}
	specs := make(map[string]work.InputSpec)
	if input.Definition != nil {
		for _, spec := range input.Definition.InputSpecs {
			specs[spec.ID] = spec
		}
	}
	for _, item := range input.Inputs {
		if item.State != work.InputSubmitted && item.State != work.InputAccepted {
			continue
		}
		spec := item.CustomSpec
		if spec == nil {
			value, ok := specs[item.SpecID]
			if !ok {
				continue
			}
			spec = &value
		}
		contextValue.Submitted = append(contextValue.Submitted, submittedInput{
			Label: spec.Label, Kind: spec.Kind, Value: item.Value, Extra: item.Extra,
		})
	}
	return json.Marshal(contextValue)
}

const inputInferencePrompt = `You infer reviewable draft values for pending Work inputs.

Rules:
- Return exactly one JSON object: {"items":[{"inputId":"...","value":<native JSON>,"reason":"..."}],"skipped":[{"inputId":"...","reason":"..."}]}.
- Return JSON only. No markdown, commentary, null arrays, or unknown fields.
- Cover every target exactly once in either items or skipped. Never invent an inputId.
- Infer practical, concrete defaults from the original request, goal, workflow, submitted inputs, common conventions, and the target schema.
- The button is an explicit request to make a reasonable assumption. Prefer a useful conservative draft over a vague placeholder when one safe default exists.
- Do not claim private facts, access, authorization, approval, personal data, exact budgets, exact dates, or files without evidence. Skip when guessing could create a material commitment or false fact.
- Values must match each target kind and valueSchema. For choice/multi_choice use option values exactly. Use native JSON types, never JSON encoded inside a string.
- Text drafts should be specific enough for the downstream task to act on. State assumptions plainly when appropriate.
- reason is one short sentence explaining the basis; it must not expose hidden reasoning.`

func buildInputInferenceMessages(contextJSON []byte, locale string, attempt int, lastRaw string, lastErr error) []provider.Message {
	system := inputInferencePrompt
	if directive := work.LocaleDirective(locale); directive != "" {
		system += "\n- " + directive
	}
	if attempt == 0 {
		return []provider.Message{
			{Role: provider.RoleSystem, Content: system},
			{Role: provider.RoleUser, Content: "Authoritative inference context JSON:\n" + string(contextJSON)},
		}
	}
	candidate := lastJSONObjectCandidate(lastRaw)
	if candidate == "" {
		candidate = "{}"
	}
	return []provider.Message{
		{Role: provider.RoleSystem, Content: system},
		{Role: provider.RoleUser, Content: fmt.Sprintf(
			"Repair the response against the authoritative context.\nContext JSON:\n%s\nValidation error:\n%s\nLast object:\n%s",
			contextJSON, safeDecodeError(lastErr), candidate,
		)},
	}
}

func (p *bootInputInferrer) stream(ctx context.Context, messages []provider.Message) (string, error) {
	chunks, err := p.prov.Stream(ctx, provider.Request{Messages: messages, Temperature: p.temperature, MaxTokens: p.maxTokens})
	if err != nil {
		return "", fmt.Errorf("boot: InferInputs stream: %w", err)
	}
	var output bytes.Buffer
	for chunk := range chunks {
		switch chunk.Type {
		case provider.ChunkError:
			return output.String(), fmt.Errorf("boot: InferInputs chunk error: %w", chunk.Err)
		case provider.ChunkDone:
			return output.String(), nil
		default:
			output.WriteString(chunk.Text)
		}
	}
	return output.String(), nil
}

func parseInputInference(raw string, targets []work.InputInferenceTarget) (*work.InferWorkInputsResult, error) {
	candidate := lastJSONObjectCandidate(raw)
	if candidate == "" {
		return nil, fmt.Errorf("no JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.DisallowUnknownFields()
	var result work.InferWorkInputsResult
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON value")
	}
	if result.Items == nil || result.Skipped == nil {
		return nil, fmt.Errorf("items and skipped must be arrays")
	}
	specs := make(map[string]work.InputSpec, len(targets))
	for _, target := range targets {
		specs[target.InputID] = target.Spec
	}
	seen := make(map[string]struct{}, len(targets))
	for _, item := range result.Items {
		spec, ok := specs[item.InputID]
		if !ok {
			return nil, fmt.Errorf("unknown inputId %q", item.InputID)
		}
		if _, ok := seen[item.InputID]; ok {
			return nil, fmt.Errorf("duplicate inputId %q", item.InputID)
		}
		if err := work.ValidateInputValue(spec, item.Value); err != nil {
			return nil, fmt.Errorf("input %q: %w", item.InputID, err)
		}
		seen[item.InputID] = struct{}{}
	}
	for _, item := range result.Skipped {
		if _, ok := specs[item.InputID]; !ok {
			return nil, fmt.Errorf("unknown skipped inputId %q", item.InputID)
		}
		if _, ok := seen[item.InputID]; ok {
			return nil, fmt.Errorf("duplicate inputId %q", item.InputID)
		}
		if strings.TrimSpace(item.Reason) == "" {
			return nil, fmt.Errorf("skipped input %q has no reason", item.InputID)
		}
		seen[item.InputID] = struct{}{}
	}
	if len(seen) != len(targets) {
		return nil, fmt.Errorf("covered %d of %d targets", len(seen), len(targets))
	}
	return &result, nil
}

var _ work.InputInferrer = (*bootInputInferrer)(nil)
