package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"workground2/internal/provider"
	"workground2/internal/work"
)

// bootPatchPlanner implements work.PatchPlanner by calling the configured AI
// model with structured context. It never mutates Work state or executes tools.
type bootPatchPlanner struct {
	prov        provider.Provider
	temperature float64
	maxTokens   int
	llmLog      *workLLMInteractionLogger
}

func newBootPatchPlanner(prov provider.Provider, temperature float64, maxTokens int, llmLog *workLLMInteractionLogger) *bootPatchPlanner {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	return &bootPatchPlanner{prov: prov, temperature: temperature, maxTokens: maxTokens, llmLog: llmLog}
}

func (p *bootPatchPlanner) PlanPatch(ctx context.Context, input work.PatchPlanInput) (*work.PatchPlan, error) {
	if p == nil || p.prov == nil {
		return nil, fmt.Errorf("boot: PlanPatch: planner unavailable")
	}

	workID := ""
	if input.Work != nil {
		workID = input.Work.ID
	}

	var firstRaw string
	var firstErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("boot: PlanPatch: %w", err)
		}
		msgs := buildPatchPlannerMessages(input, attempt, firstRaw, firstErr)
		attemptNo := attempt + 1
		iid := interactionID("patch", workID, attemptNo)
		p.llmLog.logRequest(iid, "patch", workID, p.prov.Name(), attemptNo, msgs, p.temperature, p.maxTokens)

		raw, err := p.streamPatchPlan(ctx, msgs)
		if err != nil {
			p.llmLog.logResponse(iid, "patch", workID, attemptNo, raw, err)
			if attempt > 0 {
				return nil, fmt.Errorf("boot: PlanPatch repair: %w", err)
			}
			return nil, err
		}
		plan, err := parsePatchPlanResponse(raw)
		if err == nil {
			err = validatePatchPlanScope(input, plan)
		}
		p.llmLog.logResponse(iid, "patch", workID, attemptNo, raw, err)
		if err == nil {
			return plan, nil
		}
		if attempt == 0 {
			firstRaw, firstErr = raw, err
			continue
		}
		return nil, fmt.Errorf("boot: PlanPatch: repair response invalid: %w", err)
	}
	return nil, fmt.Errorf("boot: PlanPatch: repair exhausted")
}

func buildPatchPlannerSystemPrompt(input work.PatchPlanInput) string {
	var b strings.Builder
	b.WriteString("You are a structured work definition editor. Given a user's discussion instruction and the current work context, produce a JSON array of patch operations (PatchPlan).\n\n")
	b.WriteString("## Rules\n")
	b.WriteString("- Output ONLY valid JSON — no markdown, no code fences, no explanatory text.\n")
	b.WriteString("- Use \"replace\" for existing fields. Workflow scope may also use \"add\" or \"remove\" only for a complete artifact slot object.\n")
	b.WriteString("- Paths must follow the schema below.\n")
	switch input.Scope {
	case work.PatchBlock:
		b.WriteString("- Allowed paths for this Block patch: blocks/<blockID>/title, blocks/<blockID>/data\n")
	case work.PatchWorkflow:
		b.WriteString("- Allowed paths for this Workflow patch:\n")
		b.WriteString("  - nodes/<nodeID>/title, nodes/<nodeID>/description, nodes/<nodeID>/dependsOn, nodes/<nodeID>/inputSpecIds, nodes/<nodeID>/toolHints, nodes/<nodeID>/blockIds, nodes/<nodeID>/producesSlotIds, nodes/<nodeID>/consumesSlotIds\n")
		b.WriteString("  - artifactSlots/<slotID>/title, artifactSlots/<slotID>/kind, artifactSlots/<slotID>/expectedCount, artifactSlots/<slotID>/required\n")
		b.WriteString("  - inputSpecs/<specID>/label, inputSpecs/<specID>/description, inputSpecs/<specID>/kind, inputSpecs/<specID>/required, inputSpecs/<specID>/valueSchema, inputSpecs/<specID>/defaultValue, inputSpecs/<specID>/pinEligible\n")
		b.WriteString("  - goal\n")
		b.WriteString("  - add/remove only: artifactSlots/<slotID> (complete slot object)\n")
	}
	b.WriteString("- newValue must be JSON-encoded. oldValue is optional — if given it must match current state exactly.\n")
	b.WriteString("- Scope is " + string(input.Scope) + ". Only patch within this scope.\n")
	b.WriteString("- Do not invent IDs that don't exist in the context, except the exact new slot ID explicitly supplied by an add request.\n")
	b.WriteString("- Max 64 operations. If no change is needed, return an empty array.\n")
	b.WriteString("- Convert the instruction into actual patch operations. Do not summarize or restate the request.\n")
	if input.Scope == work.PatchBlock {
		b.WriteString("- For block content or table requests, replace blocks/<blockID>/data and preserve the current data object's shape; do not merely rename the Block.\n")
	} else if input.Scope == work.PatchWorkflow {
		b.WriteString("- This is a workflow revision. Runtime Block paths are forbidden; the Target Block below is read-only reference output.\n")
		b.WriteString("- Apply task-specific guidance to nodes/<targetNodeID>/description so the target node and its downstream nodes execute with the new guidance.\n")
		b.WriteString("- Preserve the target node's existing responsibilities and incorporate the user's instruction unless the user explicitly asks to replace them.\n")
		b.WriteString("- Patch root/goal, specs, slots, dependencies, or other nodes only when the instruction explicitly requires that broader change.\n")
		b.WriteString("- Adding a result requires one add at artifactSlots/<newSlotID> with newValue {\"id\",\"title\",\"kind\",\"expectedCount\",\"required\"}, plus replace nodes/<producerNodeID>/producesSlotIds so exactly one node produces it.\n")
		b.WriteString("- Removing a result requires one remove at artifactSlots/<slotID>, plus replace every referencing node's producesSlotIds and consumesSlotIds to remove that ID. Do not leave dangling references.\n")
	}
	b.WriteString("\n## Exact response examples\n")
	if input.Scope == work.PatchBlock {
		b.WriteString("[{\"op\":\"replace\",\"path\":\"blocks/<blockID>/title\",\"newValue\":\"New title\"}]\n")
		b.WriteString("[{\"op\":\"replace\",\"path\":\"blocks/<blockID>/data\",\"newValue\":{\"content\":\"Updated content\"}}]\n")
	} else {
		targetNodeID := input.TargetNodeID
		if targetNodeID == "" {
			targetNodeID = "<targetNodeID>"
		}
		b.WriteString(fmt.Sprintf(
			"[{\"op\":\"replace\",\"path\":\"nodes/%s/description\",\"newValue\":\"Existing responsibilities. Additional user guidance.\"}]\n",
			targetNodeID,
		))
		b.WriteString("[{\"op\":\"add\",\"path\":\"artifactSlots/new_report\",\"newValue\":{\"id\":\"new_report\",\"title\":\"New report\",\"kind\":\"document\",\"expectedCount\":1,\"required\":true}},{\"op\":\"replace\",\"path\":\"nodes/<producerNodeID>/producesSlotIds\",\"newValue\":[\"existing_slot\",\"new_report\"]}]\n")
	}

	b.WriteString("\n## Current Work Context\n")
	if input.Definition != nil {
		b.WriteString(fmt.Sprintf("WorkID: %s, DefinitionRevision: %d\n", input.Definition.WorkID, input.Definition.Revision))
		b.WriteString(fmt.Sprintf("Goal: %s\n", input.Definition.Goal))
		b.WriteString("Nodes:\n")
		for _, n := range input.Definition.Nodes {
			b.WriteString(fmt.Sprintf("  - id=%s title=%q description=%q dependsOn=%v inputSpecIds=%v blockIds=%v producesSlotIds=%v consumesSlotIds=%v\n",
				n.ID, n.Title, n.Description, n.DependsOn, n.InputSpecIDs, n.BlockIDs, n.ProducesSlotIDs, n.ConsumesSlotIDs))
		}
		b.WriteString("ArtifactSlots:\n")
		for _, s := range input.Definition.ArtifactSlots {
			b.WriteString(fmt.Sprintf("  - id=%s title=%q kind=%s expectedCount=%d\n", s.ID, s.Title, s.Kind, s.ExpectedCount))
		}
		b.WriteString("InputSpecs:\n")
		for _, s := range input.Definition.InputSpecs {
			b.WriteString(fmt.Sprintf("  - id=%s label=%q kind=%s required=%v\n", s.ID, s.Label, s.Kind, s.Required))
		}
	}

	if input.Block != nil {
		blockLabel := "Target Block"
		if input.Scope == work.PatchWorkflow {
			blockLabel = "Reference output Block (read-only)"
		}
		b.WriteString(fmt.Sprintf("\n%s: id=%s title=%q revision=%d data=%s\n",
			blockLabel,
			input.Block.ID,
			input.Block.Title,
			input.Block.Revision,
			compactPatchJSON(input.Block.Data),
		))
	}

	if input.Task != nil {
		b.WriteString(fmt.Sprintf("Target Task: id=%s name=%q state=%s\n", input.Task.ID, input.Task.Name, input.Task.State))
	}
	if input.TargetNodeID != "" {
		b.WriteString(fmt.Sprintf("Target Node: id=%s\n", input.TargetNodeID))
	}

	return b.String()
}

func buildPatchPlannerUserMessage(input work.PatchPlanInput) string {
	if input.Scope == work.PatchWorkflow {
		return fmt.Sprintf(
			"Instruction for Target Node %s: %s\nScope: %s\nReturn only Workflow paths; never return a blocks/ path.",
			input.TargetNodeID,
			input.Instruction,
			input.Scope,
		)
	}
	return fmt.Sprintf("Instruction: %s\nScope: %s", input.Instruction, input.Scope)
}

func buildPatchPlannerMessages(
	input work.PatchPlanInput,
	attempt int,
	firstRaw string,
	firstErr error,
) []provider.Message {
	system := buildPatchPlannerSystemPrompt(input)
	user := buildPatchPlannerUserMessage(input)
	if attempt == 0 {
		return []provider.Message{
			{Role: provider.RoleSystem, Content: system},
			{Role: provider.RoleUser, Content: user},
		}
	}
	return []provider.Message{
		{Role: provider.RoleSystem, Content: system},
		{Role: provider.RoleUser, Content: user},
		{Role: provider.RoleAssistant, Content: truncateRawResponse(firstRaw, 4096)},
		{Role: provider.RoleUser, Content: fmt.Sprintf(
			"Your previous response was not a valid PatchPlan (%s). Return exactly one JSON array of patch operations now. Output JSON only; do not explain, summarize, use markdown, or repeat the instruction.",
			patchParseErrorCategory(firstErr),
		)},
	}
}

func (p *bootPatchPlanner) streamPatchPlan(ctx context.Context, messages []provider.Message) (string, error) {
	chunks, err := p.prov.Stream(ctx, provider.Request{
		Messages:    messages,
		Temperature: p.temperature,
		MaxTokens:   p.maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("boot: PlanPatch stream: %w", err)
	}
	var buf bytes.Buffer
	for chunk := range chunks {
		switch chunk.Type {
		case provider.ChunkError:
			return buf.String(), fmt.Errorf("boot: PlanPatch chunk error: %w", chunk.Err)
		case provider.ChunkDone:
			return buf.String(), nil
		default:
			buf.WriteString(chunk.Text)
		}
	}
	return buf.String(), nil
}

func compactPatchJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "null"
	}
	return buf.String()
}

// parsePatchPlanResponse extracts a PatchPlan from raw model output.
func parsePatchPlanResponse(raw string) (*work.PatchPlan, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("boot: PlanPatch: empty model response")
	}

	data := []byte(raw)
	for i, b := range data {
		if b != '[' && b != '{' {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(data[i:]))
		var candidate json.RawMessage
		if err := decoder.Decode(&candidate); err != nil {
			continue
		}
		if plan, err := decodePatchPlan(candidate); err == nil {
			return plan, nil
		}
	}
	return nil, fmt.Errorf("boot: PlanPatch: no valid PatchPlan JSON found")
}

func decodePatchPlan(raw json.RawMessage) (*work.PatchPlan, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty JSON value")
	}
	if raw[0] == '[' {
		var ops []work.PatchOp
		if err := json.Unmarshal(raw, &ops); err != nil || ops == nil {
			return nil, fmt.Errorf("operations must be a JSON array")
		}
		return &work.PatchPlan{Operations: ops}, nil
	}
	if raw[0] != '{' {
		return nil, fmt.Errorf("PatchPlan must be an array or object")
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("invalid PatchPlan object")
	}
	operations, ok := object["operations"]
	if !ok {
		planRaw, hasPlan := object["plan"]
		if !hasPlan {
			return nil, fmt.Errorf("PatchPlan object is missing operations")
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(planRaw, &nested); err != nil {
			return nil, fmt.Errorf("PatchPlan plan must be an object")
		}
		operations, ok = nested["operations"]
		if !ok {
			return nil, fmt.Errorf("PatchPlan plan is missing operations")
		}
	}
	var ops []work.PatchOp
	if err := json.Unmarshal(operations, &ops); err != nil || ops == nil {
		return nil, fmt.Errorf("operations must be a JSON array")
	}
	return &work.PatchPlan{Operations: ops}, nil
}

func validatePatchPlanScope(input work.PatchPlanInput, plan *work.PatchPlan) error {
	if plan == nil {
		return fmt.Errorf("boot: PlanPatch: empty PatchPlan")
	}
	for i, op := range plan.Operations {
		isBlockPath := strings.HasPrefix(op.Path, "blocks/")
		switch input.Scope {
		case work.PatchBlock:
			if !isBlockPath {
				return fmt.Errorf(
					"boot: PlanPatch: op[%d] path %q is outside block scope",
					i,
					op.Path,
				)
			}
		case work.PatchWorkflow:
			if isBlockPath {
				return fmt.Errorf(
					"boot: PlanPatch: op[%d] path %q targets a runtime Block forbidden by workflow scope",
					i,
					op.Path,
				)
			}
		}
	}
	return nil
}

func patchParseErrorCategory(err error) string {
	if err == nil {
		return "unknown parse error"
	}
	const prefix = "boot: PlanPatch: "
	if category, ok := strings.CutPrefix(err.Error(), prefix); ok {
		return category
	}
	return "invalid PatchPlan JSON"
}

var _ work.PatchPlanner = (*bootPatchPlanner)(nil)
