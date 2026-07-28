package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"workground2/internal/provider"
	"workground2/internal/work"
)

// bootDefinitionPlanner turns a structure intent into a complete untrusted
// DefinitionPlan through the configured production model provider.
type bootDefinitionPlanner struct {
	prov        provider.Provider
	temperature float64
	maxTokens   int
	llmLog      *workLLMInteractionLogger
}

func newBootDefinitionPlanner(prov provider.Provider, temperature float64, maxTokens int, llmLog *workLLMInteractionLogger) *bootDefinitionPlanner {
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	return &bootDefinitionPlanner{prov: prov, temperature: temperature, maxTokens: maxTokens, llmLog: llmLog}
}

func (p *bootDefinitionPlanner) PlanDefinition(ctx context.Context, input work.DefinitionPlanInput) (*work.DefinitionPlan, error) {
	if p == nil || p.prov == nil {
		return nil, work.ErrDefinitionPlannerUnavailable
	}
	base, err := json.Marshal(input.Base)
	if err != nil {
		return nil, fmt.Errorf("boot: PlanDefinition marshal base: %w", err)
	}
	baseJSON := string(base)

	workID := ""
	if input.Work != nil {
		workID = input.Work.ID
	}

	const maxTries = 3
	var lastRaw string
	var lastErr error

	for attempt := 0; attempt < maxTries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("boot: PlanDefinition: %w", err)
		}

		msgs := buildDefinitionPlanMessages(attempt, input.Intent, baseJSON, lastRaw, lastErr)
		attemptNo := attempt + 1
		iid := interactionID("definition", workID, attemptNo)
		p.llmLog.logRequest(iid, "definition", workID, p.prov.Name(), attemptNo, msgs, p.temperature, p.maxTokens)

		raw, streamErr := p.streamDefinitionPlan(ctx, msgs)
		if streamErr != nil {
			// Preserve partial raw for diagnostics when a streamed response
			// fails after emitting content.
			p.llmLog.logResponse(iid, "definition", workID, attemptNo, raw, streamErr)
			// Provider-level or chunk error — never repair.
			return nil, streamErr
		}

		lastRaw = raw
		plan, parseErr := parseDefinitionPlanResponse(raw)
		p.llmLog.logResponse(iid, "definition", workID, attemptNo, raw, parseErr)
		if parseErr == nil {
			return plan, nil
		}
		lastErr = parseErr
	}

	// Exhausted all repair attempts — return the last safe parse error.
	return nil, lastErr
}

const definitionPlannerPrompt = `You are the Work Collaboration Workbench V2 structure planner.

Internally validate your JSON against the schema below before sending. Then send the final output.

Output rules (strict — violations cause rejection):
- Return exactly ONE top-level JSON object — never give candidate A/B, never give a second object, never wrap in an array.
- The object must contain ONLY these four fields: goal, nodes, artifactSlots, inputSpecs. No other top-level fields.
- Return ONLY the JSON — no markdown, no code fences, no commentary, no explanations, no preamble, no postscript.
- Think silently. The first non-whitespace character of your response must be { and the last must be }.
- Do not quote, restate, or discuss the input JSON, a draft JSON, or these instructions.

Planning rules:
- The object is a complete replacement candidate derived from the authoritative base.
- Nodes may be added, removed, reordered, or changed. Dependencies must form a DAG.
- Every referenced inputSpecId and artifact slot ID must exist in the returned object.
- Every artifact slot must have exactly one producer node.
- Preserve stable IDs for unchanged concepts. Use short deterministic IDs for new concepts.
- For text inputs that require multiple lines, lists, or one item per line, set valueSchema.multiline to true.
- For choice and multi_choice inputs, valueSchema.options is required and must be a JSON array of {"value":"...","label":"..."} objects.
- Do not return revision, parentRevision, status, digest, workId, createdAt, or createdBy.
- Do not return null collections or a clone when the intent requests structural changes.

InputSpec rules — when to ask the user vs when the system will search:
- Information that can be found through public web search (facts, documentation, APIs, references, examples) must NOT generate an InputSpec. Instead, set toolHints: ["web_search"] on the node that needs it. At execution time the node uses native/direct search when available and otherwise delegates through request_help(web_search).
- Only generate a required InputSpec when the information is indispensable for completing the work AND can only come from the user: private preferences, constraints, authorization, access to private systems, files the user must provide, or decisions only the user can make.
- Label must read like a natural question the user will see (e.g. "Who is the intended audience?"). Description is a short sentence explaining what this input is for.
- Choose the most specific InputKind: text for free-form answers, number for quantities, date for deadlines, choice for single-select, multi_choice for multi-select, file for uploads, roster for lists of people, form for structured objects, approval for sign-offs.
- Group inputs that belong to the same phase under the same node via inputSpecIds so they materialize as a single Block.
- Every required InputSpec MUST be referenced by at least one NodeDef.inputSpecIds. An unreferenced required InputSpec is an invalid plan that will be rejected.
- Do NOT generate inputs that are optional, have safe defaults, duplicate another input, or are irrelevant to the deliverable.`

const definitionPlanOutputReminder = `Validate silently before responding.
Your entire response must begin with { and end with }. Return exactly ONE JSON object and nothing else.
Do not emit analysis, alternatives, quoted JSON, a second JSON value, markdown, or commentary.`

// definitionPlanSchema is the static, reviewable full DefinitionPlan JSON
// schema appended to the first-turn system prompt. It includes nested
// schemas for NodeDef, ArtifactSlotDef, InputSpec, and the allowed InputKind
// values, plus a complete example.
const definitionPlanSchema = `

## DefinitionPlan JSON Schema — complete replacement candidate

Return exactly ONE JSON object — no wrapping, no commentary, no markdown fences.
The object must have ONLY these four top-level fields (no others allowed):

### Top-level object
{
  "goal": "string (required, non-null)",
  "nodes": [ ... NodeDef ... ] (required, non-null JSON array),
  "artifactSlots": [ ... ArtifactSlotDef ... ] (required, non-null JSON array),
  "inputSpecs": [ ... InputSpec ... ] (required, non-null JSON array)
}

No other top-level fields are allowed.

### NodeDef (each element in the nodes array)
{
  "id": "string (required)",
  "title": "string (required)",
  "description": "string (optional)",
  "dependsOn": ["string (optional) — node IDs"],
  "inputSpecIds": ["string (optional) — input spec IDs"],
  "toolHints": ["string (optional) — use \"web_search\" when the node needs current or public web information"],
  "blockIds": ["string (optional)"],
  "producesSlotIds": ["string (optional)"],
  "consumesSlotIds": ["string (optional)"],
  "globalGate": "string (optional)"
}

### ArtifactSlotDef (each element in the artifactSlots array)
{
  "id": "string (required)",
  "title": "string (required)",
  "kind": "string (required) — e.g. text, document, image, code",
  "expectedCount": integer (required),
  "required": boolean (required)
}

### InputSpec (each element in the inputSpecs array)
{
  "id": "string (required)",
  "label": "string (required) — a human-readable question the user sees, e.g. \"Which target platform?\"",
  "description": "string (optional) — one sentence explaining what this input is for",
  "kind": "string (required) — must be one of: text, number, date, choice, multi_choice, file, roster, form, approval",
  "required": boolean (required) — set true ONLY when the info is indispensable and only the user can provide it; publicly searchable info must NOT generate an InputSpec",
  "valueSchema": {} (optional except choice and multi_choice; see kind-specific schemas below),
  "defaultValue": any (optional),
  "pinEligible": boolean (required)
}

CRITICAL: Every InputSpec with required=true MUST appear in at least one NodeDef.inputSpecIds. An unreferenced required InputSpec is a fatal error — the plan will be rejected.

Allowed InputKind values: "text", "number", "date", "choice", "multi_choice", "file", "roster", "form", "approval"

Kind-specific valueSchema examples:
- text: {"multiline": true}
- number: {"min": 1, "max": 100, "integer": true}
- choice: {"options":[{"value":"visual","label":"视觉学习"},{"value":"audio","label":"听觉学习"}]}
- multi_choice: {"options":[{"value":"reading","label":"阅读"},{"value":"practice","label":"练习"}],"minSelect":1}

For choice and multi_choice, options MUST be a JSON array. Never return an object map, a comma-separated string, or an array of bare strings.

### Complete example
{
  "goal": "Deliver a reviewed report",
  "nodes": [
    {
      "id": "collect",
      "title": "Collect materials",
      "description": "Gather source documents",
      "inputSpecIds": ["topic"],
      "producesSlotIds": ["source"]
    },
    {
      "id": "review",
      "title": "Review and finalize",
      "dependsOn": ["collect"],
      "consumesSlotIds": ["source"],
      "producesSlotIds": ["report"]
    }
  ],
  "artifactSlots": [
    {"id": "source", "title": "Source materials", "kind": "text", "expectedCount": 1, "required": true},
    {"id": "report", "title": "Final report", "kind": "document", "expectedCount": 1, "required": true}
  ],
  "inputSpecs": [
    {"id": "topic", "label": "What is the report topic?", "description": "This sets the scope of the report.", "kind": "text", "required": true, "pinEligible": false}
  ]
}`

// parseDefinitionPlanResponse selects the last contract-valid DefinitionPlan
// object from raw model output. Earlier commentary, arrays, examples, and
// invalid objects are ignored. If no object passes the frozen DTO contract,
// the last object's safe validation error is returned for repair.
func parseDefinitionPlanResponse(raw string) (*work.DefinitionPlan, error) {
	if raw == "" {
		return nil, fmt.Errorf("boot: PlanDefinition: empty model response")
	}
	data := bytes.TrimPrefix([]byte(raw), []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("boot: PlanDefinition: response contains invalid UTF-8")
	}

	containers := scanJSONContainers(data)
	var lastErr error
	hasArray := false
	for i := len(containers) - 1; i >= 0; i-- {
		candidate := containers[i]
		if candidate.kind == '[' {
			hasArray = true
			continue
		}
		plan, err := decodeDefinitionPlan(candidate.data)
		if err == nil {
			return plan, nil
		}
		if lastErr == nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if hasArray {
		return nil, fmt.Errorf("boot: PlanDefinition: expected JSON object, got array")
	}

	// No syntactically complete JSON container was found. Preserve the
	// previous precise error categories for truncated or malformed objects.
	jsonBytes, err := extractFirstJSONObject(data)
	if err != nil {
		return nil, fmt.Errorf("boot: PlanDefinition: %w", err)
	}
	return decodeDefinitionPlan(jsonBytes)
}

// ── Repair loop helpers ────────────────────────────────────────────────────

// streamDefinitionPlan calls the provider and accumulates the full text
// response. Provider-level errors and ChunkError are returned as-is and are
// never eligible for repair.
func (p *bootDefinitionPlanner) streamDefinitionPlan(ctx context.Context, msgs []provider.Message) (string, error) {
	chunks, err := p.prov.Stream(ctx, provider.Request{
		Messages:    msgs,
		Temperature: p.temperature,
		MaxTokens:   p.maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("boot: PlanDefinition stream: %w", err)
	}
	var output bytes.Buffer
	for chunk := range chunks {
		switch chunk.Type {
		case provider.ChunkError:
			return output.String(), fmt.Errorf("boot: PlanDefinition chunk error: %w", chunk.Err)
		case provider.ChunkDone:
			return output.String(), nil
		default:
			output.WriteString(chunk.Text)
		}
	}
	return output.String(), nil
}

// buildDefinitionPlanMessages returns the message list for a given attempt.
// Attempt 0 uses the full system prompt with nested schema + user input.
// Repair attempts include only the last syntactically complete object
// candidate, never the full raw response, so analysis and duplicate objects
// are not fed back to the model as assistant history.
func buildDefinitionPlanMessages(attempt int, intent, baseJSON, lastRaw string, lastErr error) []provider.Message {
	if attempt == 0 {
		return []provider.Message{
			{Role: provider.RoleSystem, Content: definitionPlannerPrompt + definitionPlanSchema},
			{Role: provider.RoleUser, Content: fmt.Sprintf(
				"Natural-language structure intent:\n%s\n\nAuthoritative base definition JSON:\n%s\n\n%s",
				intent, baseJSON, definitionPlanOutputReminder,
			)},
		}
	}

	// Repair attempt.
	errCat := repairErrorCategory(lastErr)
	candidate := lastJSONObjectCandidate(lastRaw)
	candidateSection := "No complete JSON object candidate was found. Reconstruct one from the intent and authoritative base."
	if candidate != "" {
		candidateSection = fmt.Sprintf(
			"Last JSON object candidate to repair:\n%s",
			candidate,
		)
	}
	return []provider.Message{
		{Role: provider.RoleSystem, Content: definitionPlannerPrompt + definitionPlanSchema},
		{Role: provider.RoleUser, Content: fmt.Sprintf(
			"Repair a DefinitionPlan response.\n\nNatural-language structure intent:\n%s\n\nAuthoritative base definition JSON:\n%s\n\nParse error category: %s\n\n%s\n\nPreserve the candidate's business semantics. Fix only JSON structure and schema violations; do NOT re-plan, add alternatives, explain the error, or quote the candidate before the answer.\n\n%s",
			intent,
			baseJSON,
			errCat,
			candidateSection,
			definitionPlanOutputReminder,
		)},
	}
}

func lastJSONObjectCandidate(raw string) string {
	data := bytes.TrimPrefix([]byte(raw), []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return ""
	}
	containers := scanJSONContainers(data)
	for i := len(containers) - 1; i >= 0; i-- {
		if containers[i].kind == '{' {
			return string(containers[i].data)
		}
	}
	return ""
}

// repairErrorCategory strips the "boot: PlanDefinition: " prefix from a parse
// error so only the safe category is returned to the model.
func repairErrorCategory(err error) string {
	if err == nil {
		return "unknown error"
	}
	msg := err.Error()
	const prefix = "boot: PlanDefinition: "
	if after, ok := strings.CutPrefix(msg, prefix); ok {
		return after
	}
	return msg
}

// truncateRawResponse truncates raw to at most maxBytes while preserving
// valid UTF-8 boundaries. When truncation is necessary, it keeps both the
// head and tail of the input so the model can see the structure at both ends.
// A truncation marker is inserted between them.
func truncateRawResponse(raw string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(raw) <= maxBytes {
		return raw
	}
	const marker = "\n... [middle truncated] ...\n"
	if maxBytes <= len(marker)+256 {
		head := raw[:maxBytes]
		for len(head) > 0 && !utf8.ValidString(head) {
			head = head[:len(head)-1]
		}
		return head
	}

	contentBytes := maxBytes - len(marker)
	headBytes := contentBytes * 3 / 4
	tailBytes := contentBytes - headBytes
	if tailBytes < 256 {
		head := raw[:maxBytes]
		for len(head) > 0 && !utf8.ValidString(head) {
			head = head[:len(head)-1]
		}
		return head
	}

	// Head: first headBytes, backed off to a valid UTF-8 boundary.
	head := raw[:headBytes]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}

	// Tail: last tailBytes, aligned forward to a valid UTF-8 boundary.
	tailStart := len(raw) - tailBytes
	for tailStart < len(raw) && !utf8.ValidString(raw[tailStart:]) {
		tailStart++
	}
	tail := raw[tailStart:]

	return head + marker + tail
}

type jsonContainer struct {
	kind byte
	data []byte
}

// scanJSONContainers returns complete top-level JSON objects and arrays in
// source order. Once a container is decoded, its entire byte range is skipped,
// so arrays and objects nested inside it are never treated as separate values.
func scanJSONContainers(raw []byte) []jsonContainer {
	var containers []jsonContainer
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b != '{' && b != '[' {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw[i:]))
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			continue
		}
		s := strings.TrimSpace(string(val))
		if len(s) == 0 {
			continue
		}
		kind := s[0]
		if kind != '{' && kind != '[' {
			continue
		}
		containers = append(containers, jsonContainer{
			kind: kind,
			data: append([]byte(nil), val...),
		})
		consumed := dec.InputOffset()
		if consumed > 0 {
			i += int(consumed) - 1
		}
	}
	return containers
}

// extractFirstJSONObject preserves precise diagnostics when no complete JSON
// container could be decoded, such as truncated objects or malformed syntax.
func extractFirstJSONObject(raw []byte) ([]byte, error) {
	firstBrace := bytes.IndexByte(raw, '{')
	if firstBrace < 0 {
		if bytes.IndexByte(raw, '[') >= 0 {
			return nil, fmt.Errorf("expected JSON object, got array")
		}
		return nil, fmt.Errorf("no JSON object found in response")
	}

	depth := 0
	inString := false
	escaped := false
	for i := firstBrace; i < len(raw); i++ {
		b := raw[i]
		if escaped {
			escaped = false
			continue
		}
		if inString {
			switch b {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[firstBrace : i+1], nil
			}
		}
	}
	return nil, fmt.Errorf("unterminated JSON object")
}

// ── Strict DTO decode ───────────────────────────────────────────────────────

// definitionPlanContract lists the four fields every DefinitionPlan must
// contain.  Collections must be JSON arrays; goal must be a non-null string.
var definitionPlanContract = struct {
	required    []string
	collections map[string]bool
}{
	required:    []string{"goal", "nodes", "artifactSlots", "inputSpecs"},
	collections: map[string]bool{"nodes": true, "artifactSlots": true, "inputSpecs": true},
}

// decodeDefinitionPlan validates the raw JSON against the frozen DefinitionPlan
// contract: all four required fields present and non-null, collections must
// be JSON arrays, no unknown fields at any level, and no type mismatches.
func decodeDefinitionPlan(data []byte) (*work.DefinitionPlan, error) {
	// Step 1 — decode into a raw map with DisallowUnknownFields to catch
	// top-level unknown keys.
	var raw map[string]json.RawMessage
	if err := strictDecode(data, &raw); err != nil {
		return nil, fmt.Errorf("boot: PlanDefinition: %w", safeDecodeError(err))
	}

	// Step 2 — require every field in the contract, reject nulls, and reject
	// non-array collection values.
	for _, key := range definitionPlanContract.required {
		val, ok := raw[key]
		if !ok {
			return nil, fmt.Errorf("boot: PlanDefinition: missing required field: %s", key)
		}
		if isJSONNull(val) {
			return nil, fmt.Errorf("boot: PlanDefinition: field %s must not be null", key)
		}
		if definitionPlanContract.collections[key] && !isJSONArray(val) {
			return nil, fmt.Errorf("boot: PlanDefinition: field %s must be a JSON array", key)
		}
	}

	// Step 2b — reject unknown top-level keys (the map decode accepts
	// everything; we must enforce the frozen contract ourselves).
	for key := range raw {
		if !definitionPlanContract.collections[key] && key != "goal" {
			return nil, fmt.Errorf("boot: PlanDefinition: unknown field")
		}
	}

	// Step 3 — decode each value with strict rules.  We do them individually
	// so type errors name the specific field rather than the whole document.
	var plan work.DefinitionPlan
	if err := strictUnmarshal(raw["goal"], &plan.Goal); err != nil {
		return nil, fmt.Errorf("boot: PlanDefinition: invalid goal: %w", safeDecodeError(err))
	}
	if err := strictUnmarshal(raw["nodes"], &plan.Nodes); err != nil {
		return nil, fmt.Errorf("boot: PlanDefinition: invalid nodes: %w", safeDecodeError(err))
	}
	if err := strictUnmarshal(raw["artifactSlots"], &plan.ArtifactSlots); err != nil {
		return nil, fmt.Errorf("boot: PlanDefinition: invalid artifactSlots: %w", safeDecodeError(err))
	}
	if err := strictUnmarshal(raw["inputSpecs"], &plan.InputSpecs); err != nil {
		return nil, fmt.Errorf("boot: PlanDefinition: invalid inputSpecs: %w", safeDecodeError(err))
	}
	for i := range plan.InputSpecs {
		normalized, err := work.NormalizeInputSpecValueSchema(plan.InputSpecs[i])
		if err != nil {
			return nil, fmt.Errorf("boot: PlanDefinition: invalid inputSpecs valueSchema")
		}
		plan.InputSpecs[i].ValueSchema = normalized
	}

	// Reject orphan required InputSpecs — every required InputSpec must be
	// referenced by at least one NodeDef.inputSpecIds so the user is asked.
	referenced := make(map[string]bool, len(plan.InputSpecs))
	for _, node := range plan.Nodes {
		for _, id := range node.InputSpecIDs {
			referenced[id] = true
		}
	}
	for _, spec := range plan.InputSpecs {
		if spec.Required && !referenced[spec.ID] {
			return nil, fmt.Errorf("boot: PlanDefinition: required inputSpec %q is not referenced by any node", spec.ID)
		}
	}

	return &plan, nil
}

func isJSONNull(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "null"
}

func isJSONArray(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return len(s) > 0 && s[0] == '['
}

// strictDecode unmarshals data into dst with DisallowUnknownFields and verifies
// the stream contains exactly one value (no trailing JSON).
func strictDecode(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err == nil {
		return fmt.Errorf("unexpected trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected trailing content: %w", err)
	}
	return nil
}

// strictUnmarshal is like strictDecode but validates that data is exactly
// the JSON value expected (single-token check is implicit in the size).
func strictUnmarshal(data json.RawMessage, dst any) error {
	return strictDecode(data, dst)
}

// ── Error safety ────────────────────────────────────────────────────────────

// safeDecodeError classifies a json decode error into a fixed, safe category
// that never includes raw response content, unknown field names, or type values.
//
// Allowed in messages:
//   - byte offset (from json.SyntaxError)
//   - field name from the frozen contract only (goal / nodes / artifactSlots / inputSpecs)
func safeDecodeError(err error) error {
	if err == nil {
		return nil
	}

	// json.SyntaxError — keep the offset but never the full message.
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Errorf("syntax error at offset %d", syntaxErr.Offset)
	}

	// json.UnmarshalTypeError — field path is fine (it refers to struct fields
	// in our own code, not raw JSON keys); value must never leak.
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			return fmt.Errorf("type error")
		}
		return fmt.Errorf("type error at %s", field)
	}

	// DisallowUnknownFields produces "json: unknown field \"...\"" — suppress
	// the field name.
	msg := err.Error()
	if strings.HasPrefix(msg, "json: unknown field ") {
		return fmt.Errorf("unknown field")
	}

	// Other json-package errors use "json:" prefix; fold them into a generic
	// category so raw content never leaks.
	if strings.HasPrefix(msg, "json:") {
		return fmt.Errorf("invalid definition plan")
	}

	// Non-json errors (e.g. io errors) are passed through — they don't contain
	// model output.
	return err
}

var _ work.DefinitionPlanner = (*bootDefinitionPlanner)(nil)
