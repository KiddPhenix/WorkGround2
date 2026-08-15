package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	b.WriteString("You are the Work coordinator. Given a user's instruction and the current authoritative Work state, produce one structured PatchPlan that describes both definition mutations and the semantic next action.\n\n")
	b.WriteString("## Rules\n")
	b.WriteString("- Output ONLY valid JSON — no markdown, no code fences, no explanatory text.\n")
	b.WriteString("- Output exactly one object: {\"operations\":[...],\"actions\":[...]}.\n")
	b.WriteString("- actions are authoritative semantic decisions: reuse, reformat, rerun, or ask_user.\n")
	b.WriteString("- Use reuse with nodeId when its existing execution, inputs, search evidence, and outputs remain semantically valid.\n")
	b.WriteString("- Use reformat with artifactSlotId when existing artifact content remains valid and only its delivery format must change. Reformat never repeats the producer node or web_search.\n")
	b.WriteString("- Use rerun with nodeId only when the requested change alters the node's semantic content, inputs, dependencies, or evidence requirements.\n")
	b.WriteString("- Use ask_user with a concrete question only when current state cannot determine a safe action. In that case operations must be empty.\n")
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
	b.WriteString("- newValue must be a native JSON value (string, number, boolean, object, array, or null) — never a JSON-encoded string wrapping another value.\n")
	b.WriteString("- Scope is " + string(input.Scope) + ". Only patch within this scope.\n")
	b.WriteString("- Do not invent IDs that don't exist in the context, except the exact new slot ID explicitly supplied by an add request.\n")
	b.WriteString("- Max 64 operations and 64 actions. If no change is needed, return empty operations and reuse actions for the relevant nodes.\n")
	b.WriteString("- Convert the instruction into actual patch operations. Do not summarize or restate the request.\n")
	if input.Work != nil {
		if directive := work.LocaleDirective(input.Work.Locale); directive != "" {
			b.WriteString("- " + directive + "\n")
		}
	}
	if input.Scope == work.PatchBlock {
		b.WriteString("- For block content or table requests, replace blocks/<blockID>/data and preserve the current data object's shape; do not merely rename the Block.\n")
		b.WriteString("- CRITICAL: blocks/<blockID>/data newValue must be a raw JSON object like {\"content\":\"...\"}, NEVER a JSON-encoded string like \"{\\\"content\\\":\\\"...\\\"}\". The backend rejects string-encoded objects.\n")
	} else if input.Scope == work.PatchWorkflow {
		b.WriteString("- This is a workflow revision. Runtime Block paths are forbidden; the Target Block below is read-only reference output.\n")
		b.WriteString("- Apply task-specific guidance to nodes/<targetNodeID>/description so the target node and its downstream nodes execute with the new guidance.\n")
		b.WriteString("- Preserve the target node's existing responsibilities and incorporate the user's instruction unless the user explicitly asks to replace them.\n")
		b.WriteString("- Patch root/goal, specs, slots, dependencies, or other nodes only when the instruction explicitly requires that broader change.\n")
		b.WriteString("- Adding a result requires one add at artifactSlots/<newSlotID> with newValue {\"id\",\"title\",\"kind\",\"expectedCount\",\"required\"}, plus replace nodes/<producerNodeID>/producesSlotIds so exactly one node produces it.\n")
		b.WriteString("- If an add-result instruction does not name a producer, infer exactly one existing producer from node responsibilities, dependency order, and existing artifact relationships. Never ask the user to choose, never add a node, and do not treat the target node as the producer unless it is the best match.\n")
		b.WriteString("- Removing a result requires one remove at artifactSlots/<slotID>, plus replace every referencing node's producesSlotIds and consumesSlotIds to remove that ID. Do not leave dangling references.\n")
		b.WriteString("- Modifying a result keeps its existing slot ID and all producer/consumer references. Replace only the explicitly requested artifactSlots fields; never model an edit as remove plus add.\n")
		b.WriteString("- A result format change must replace artifactSlots/<slotID>/kind with the exact requested format and keep the title extension consistent. Changing only the title or MIME is not a format change.\n")
		b.WriteString("- The url/link result kind means the actual absolute http/https link from the producer's final response, not a file or a URL saved inside a file.\n")
		b.WriteString("- For a format-only result change between file-backed kinds, do not modify any node description, acceptance criteria, tool hints, dependencies, or goal. Emit reformat for that slot; its producer is reused.\n")
		b.WriteString("- A format change to or from url/link must rerun the existing producer so it creates the new result representation. Never emit reformat for a url/link transition.\n")
	}
	b.WriteString("\n## Exact response examples\n")
	if input.Scope == work.PatchBlock {
		b.WriteString("{\"operations\":[{\"op\":\"replace\",\"path\":\"blocks/<blockID>/title\",\"newValue\":\"New title\"}],\"actions\":[{\"action\":\"reuse\",\"nodeId\":\"<targetNodeID>\",\"reason\":\"presentation-only change\"}]}\n")
		b.WriteString("{\"operations\":[{\"op\":\"replace\",\"path\":\"blocks/<blockID>/data\",\"newValue\":{\"content\":\"Updated content\"}}],\"actions\":[{\"action\":\"rerun\",\"nodeId\":\"<targetNodeID>\",\"reason\":\"content changed\"}]}\n")
	} else {
		targetNodeID := input.TargetNodeID
		if targetNodeID == "" {
			targetNodeID = "<targetNodeID>"
		}
		b.WriteString(fmt.Sprintf(
			"{\"operations\":[{\"op\":\"replace\",\"path\":\"nodes/%s/description\",\"newValue\":\"Existing responsibilities. Additional user guidance.\"}],\"actions\":[{\"action\":\"rerun\",\"nodeId\":\"%s\",\"reason\":\"semantic guidance changed\"}]}\n",
			targetNodeID,
			targetNodeID,
		))
		b.WriteString("{\"operations\":[{\"op\":\"replace\",\"path\":\"artifactSlots/<existingSlotID>/title\",\"newValue\":\"Result.docx\"},{\"op\":\"replace\",\"path\":\"artifactSlots/<existingSlotID>/kind\",\"newValue\":\"docx\"}],\"actions\":[{\"action\":\"reformat\",\"artifactSlotId\":\"<existingSlotID>\",\"reason\":\"content and search evidence remain valid\"}]}\n")
		b.WriteString("{\"operations\":[{\"op\":\"replace\",\"path\":\"artifactSlots/<existingSlotID>/kind\",\"newValue\":\"url\"}],\"actions\":[{\"action\":\"rerun\",\"nodeId\":\"<producerNodeID>\",\"reason\":\"producer must return the actual published URL\"}]}\n")
		b.WriteString("{\"operations\":[{\"op\":\"add\",\"path\":\"artifactSlots/new_report\",\"newValue\":{\"id\":\"new_report\",\"title\":\"New report\",\"kind\":\"document\",\"expectedCount\":1,\"required\":true}},{\"op\":\"replace\",\"path\":\"nodes/<producerNodeID>/producesSlotIds\",\"newValue\":[\"existing_slot\",\"new_report\"]}],\"actions\":[{\"action\":\"rerun\",\"nodeId\":\"<producerNodeID>\",\"reason\":\"new output content is required\"}]}\n")
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
	if input.Work != nil {
		customInputs := make([]work.WorkInput, 0)
		for _, item := range input.Work.V2Inputs {
			if item.CustomSpec != nil {
				customInputs = append(customInputs, item)
			}
		}
		sort.Slice(customInputs, func(i, j int) bool {
			return customInputs[i].ID < customInputs[j].ID
		})
		if len(customInputs) > 0 {
			b.WriteString("Work Information (user-owned, preserve across workflow revisions):\n")
			for _, item := range customInputs {
				b.WriteString(fmt.Sprintf(
					"  - id=%s label=%q description=%q kind=%s state=%s value=%s\n",
					item.ID,
					item.CustomSpec.Label,
					item.CustomSpec.Description,
					item.CustomSpec.Kind,
					item.State,
					compactPatchJSON(item.Value),
				))
			}
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
	if input.Work != nil {
		b.WriteString("Current Runtime State:\n")
		runtimeIDs := make([]string, 0, len(input.Work.V2TaskRuntimes))
		for taskID := range input.Work.V2TaskRuntimes {
			runtimeIDs = append(runtimeIDs, taskID)
		}
		sort.Strings(runtimeIDs)
		for _, taskID := range runtimeIDs {
			runtime := input.Work.V2TaskRuntimes[taskID]
			if runtime == nil || input.Definition == nil || runtime.DefinitionRev != input.Definition.Revision {
				continue
			}
			b.WriteString(fmt.Sprintf("  - nodeId=%s taskId=%s state=%s progress=%q\n",
				runtime.NodeID, runtime.TaskID, runtime.State, runtime.Progress))
		}
		b.WriteString("Current Artifact State:\n")
		slots := append([]work.ArtifactSlot(nil), input.Work.V2ArtifactSlots...)
		sort.Slice(slots, func(i, j int) bool {
			if slots[i].DefinitionRev != slots[j].DefinitionRev {
				return slots[i].DefinitionRev < slots[j].DefinitionRev
			}
			return slots[i].ID < slots[j].ID
		})
		for _, slot := range slots {
			if input.Definition != nil && slot.DefinitionRev != input.Definition.Revision {
				continue
			}
			b.WriteString(fmt.Sprintf("  - id=%s kind=%s state=%s refs=%d\n",
				slot.ID, slot.Kind, slot.State, len(slot.ArtifactRefs)))
		}
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
			"Your previous response was not a valid PatchPlan (%s). Return exactly one JSON object with operations and actions now. Output JSON only; do not explain, summarize, use markdown, or repeat the instruction.",
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
	var lastObject *work.PatchPlan
	var lastArray *work.PatchPlan
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
			if b == '{' {
				lastObject = plan
			} else {
				lastArray = plan
			}
		}
	}
	if lastObject != nil {
		return lastObject, nil
	}
	if lastArray != nil {
		return lastArray, nil
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
		for _, op := range ops {
			if strings.TrimSpace(op.Op) == "" || strings.TrimSpace(op.Path) == "" {
				return nil, fmt.Errorf("operations array contains a non-operation value")
			}
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
		object = nested
		operations, ok = nested["operations"]
		if !ok {
			return nil, fmt.Errorf("PatchPlan plan is missing operations")
		}
	}
	var ops []work.PatchOp
	if err := json.Unmarshal(operations, &ops); err != nil || ops == nil {
		return nil, fmt.Errorf("operations must be a JSON array")
	}
	var actions []work.PatchAction
	if actionsRaw, ok := object["actions"]; ok {
		if err := json.Unmarshal(actionsRaw, &actions); err != nil || actions == nil {
			return nil, fmt.Errorf("actions must be a JSON array")
		}
	}
	return &work.PatchPlan{Operations: ops, Actions: actions}, nil
}

func validatePatchPlanScope(input work.PatchPlanInput, plan *work.PatchPlan) error {
	if plan == nil {
		return fmt.Errorf("boot: PlanPatch: empty PatchPlan")
	}
	if len(plan.Operations) > 0 && len(plan.Actions) == 0 {
		return fmt.Errorf("boot: PlanPatch: semantic actions are required for every non-empty patch")
	}
	for i, op := range plan.Operations {
		path, err := work.CompilePatchPath(op.Path)
		if err != nil {
			return fmt.Errorf("boot: PlanPatch: op[%d] path %q is invalid: %w", i, op.Path, err)
		}
		isBlockPath := path.Kind == work.PathBlocks
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
		if err := validatePatchPlanTarget(input, path, op.Op); err != nil {
			return fmt.Errorf("boot: PlanPatch: op[%d] path %q: %w", i, op.Path, err)
		}
	}
	if len(plan.Actions) > 64 {
		return fmt.Errorf("boot: PlanPatch: more than 64 semantic actions")
	}
	for i, action := range plan.Actions {
		switch action.Action {
		case work.PatchActionReuse, work.PatchActionRerun:
			if strings.TrimSpace(action.NodeID) == "" || strings.TrimSpace(action.ArtifactSlotID) != "" {
				return fmt.Errorf("boot: PlanPatch: action[%d] %q requires only nodeId", i, action.Action)
			}
			if input.Scope == work.PatchWorkflow && input.Definition != nil &&
				!patchDefinitionHasNode(input.Definition, action.NodeID) {
				return fmt.Errorf("boot: PlanPatch: action[%d] references unknown node ID %q in current definition", i, action.NodeID)
			}
		case work.PatchActionReformat:
			if strings.TrimSpace(action.ArtifactSlotID) == "" || strings.TrimSpace(action.NodeID) != "" {
				return fmt.Errorf("boot: PlanPatch: action[%d] reformat requires only artifactSlotId", i)
			}
			if input.Definition != nil && !patchDefinitionHasSlot(input.Definition, action.ArtifactSlotID) {
				return fmt.Errorf("boot: PlanPatch: action[%d] references unknown artifact slot ID %q in current definition", i, action.ArtifactSlotID)
			}
		case work.PatchActionAskUser:
			if strings.TrimSpace(action.Question) == "" || len(plan.Operations) != 0 {
				return fmt.Errorf("boot: PlanPatch: action[%d] ask_user requires a question and empty operations", i)
			}
		default:
			return fmt.Errorf("boot: PlanPatch: action[%d] has invalid action %q", i, action.Action)
		}
	}
	return nil
}

func validatePatchPlanTarget(input work.PatchPlanInput, path work.PatchPath, op string) error {
	if path.Kind == work.PathBlocks {
		if input.Block != nil && path.Segments[1] != input.Block.ID {
			return fmt.Errorf("unknown block ID %q; target block is %q", path.Segments[1], input.Block.ID)
		}
		return nil
	}
	if input.Definition == nil {
		return nil
	}
	switch path.Kind {
	case work.PathNodes:
		id := path.Segments[1]
		if !patchDefinitionHasNode(input.Definition, id) {
			return fmt.Errorf("unknown node ID %q in current definition", id)
		}
	case work.PathSlots:
		id := path.Segments[1]
		if len(path.Segments) == 2 && op == "add" {
			return nil
		}
		if !patchDefinitionHasSlot(input.Definition, id) {
			return fmt.Errorf("unknown artifact slot ID %q in current definition", id)
		}
	case work.PathSpecs:
		id := path.Segments[1]
		for _, spec := range input.Definition.InputSpecs {
			if spec.ID == id {
				return nil
			}
		}
		return fmt.Errorf("unknown input spec ID %q in current definition", id)
	}
	return nil
}

func patchDefinitionHasNode(def *work.WorkDefinitionRevision, id string) bool {
	for _, node := range def.Nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}

func patchDefinitionHasSlot(def *work.WorkDefinitionRevision, id string) bool {
	for _, slot := range def.ArtifactSlots {
		if slot.ID == id {
			return true
		}
	}
	return false
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
