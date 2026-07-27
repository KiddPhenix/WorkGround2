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

Planning rules:
- The object is a complete replacement candidate derived from the authoritative base.
- Nodes may be added, removed, reordered, or changed. Dependencies must form a DAG.
- Every referenced inputSpecId and artifact slot ID must exist in the returned object.
- Every artifact slot must have exactly one producer node.
- Preserve stable IDs for unchanged concepts. Use short deterministic IDs for new concepts.
- Do not return revision, parentRevision, status, digest, workId, createdAt, or createdBy.
- Do not return null collections or a clone when the intent requests structural changes.`

const definitionPlanOutputReminder = `Before responding, validate the result internally against the schema.
Final response: exactly ONE JSON object and nothing else. Do not emit alternatives, a second JSON value, markdown, or commentary.`

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
  "toolHints": ["string (optional)"],
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
  "label": "string (required)",
  "description": "string (optional)",
  "kind": "string (required) — must be one of: text, number, date, choice, multi_choice, file, roster, form, approval",
  "required": boolean (required),
  "valueSchema": {} (optional),
  "defaultValue": any (optional),
  "pinEligible": boolean (required)
}

Allowed InputKind values: "text", "number", "date", "choice", "multi_choice", "file", "roster", "form", "approval"

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
    {"id": "topic", "label": "Topic", "description": "The report topic", "kind": "text", "required": true, "pinEligible": false}
  ]
}`

// parseDefinitionPlanResponse extracts and validates a DefinitionPlan from
// raw model output. The response may contain natural-language commentary,
// markdown fences, or a UTF-8 BOM around a single top-level JSON object.
// Every other form — arrays, multiple objects, truncation, invalid UTF-8,
// unknown fields, type errors, null or missing required fields — is an
// explicit, recoverable failure whose error message never includes raw
// response content.
func parseDefinitionPlanResponse(raw string) (*work.DefinitionPlan, error) {
	if raw == "" {
		return nil, fmt.Errorf("boot: PlanDefinition: empty model response")
	}
	jsonBytes, err := extractJSONObject([]byte(raw))
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
// Repair attempts (1+) include the assistant's previous response (truncated
// head+tail when needed) and a repair instruction that tells the model to
// preserve the draft's business semantics while fixing JSON structure.
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
	truncated := truncateRawResponse(lastRaw, 4096)
	return []provider.Message{
		{Role: provider.RoleSystem, Content: definitionPlannerPrompt + definitionPlanSchema},
		{Role: provider.RoleUser, Content: fmt.Sprintf(
			"Natural-language structure intent:\n%s\n\nAuthoritative base definition JSON:\n%s\n\n%s",
			intent, baseJSON, definitionPlanOutputReminder,
		)},
		{Role: provider.RoleAssistant, Content: truncated},
		{Role: provider.RoleUser, Content: fmt.Sprintf(
			"Your previous response (shown as the assistant message above) could not be parsed as a valid DefinitionPlan JSON object.\n\nError: %s\n\nThe assistant message above is a DRAFT to be repaired. Keep its goals, nodes, dependencies, inputs, and artifact semantics exactly — do NOT re-plan or add/remove business content. Only fix the JSON structure and schema violations so the result is ONE valid DefinitionPlan JSON object.\n\nIf the draft contains multiple JSON values or candidates, converge them into ONE object that expresses the same plan — do NOT repeat multiple objects.\n\nReturn exactly ONE JSON object that conforms to the DefinitionPlan schema. No markdown, no commentary, no code fences, no arrays.",
			errCat,
		)},
	}
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

// extractJSONObject locates the first complete JSON object in raw and returns
// its bytes. Surrounding natural language (including bracket characters like
// [draft] that happen to appear before {), a UTF-8 BOM, and trailing markdown
// fences are stripped.
//
// Arrays — whether bare ([...]) or wrapping an object ([{...}]) — are rejected.
// Multiple objects, truncation, and invalid UTF-8 are errors.
func extractJSONObject(raw []byte) ([]byte, error) {
	// Strip UTF-8 BOM.
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	// Reject invalid UTF-8 early so errors stay safe and deterministic.
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("response contains invalid UTF-8")
	}

	// Find the first '{'.  If none exists and a '[' does, the response is a
	// pure array.  Natural-language brackets like [draft] that precede '{' are
	// harmless — we only care about the object start.
	firstBrace := bytes.IndexByte(raw, '{')
	if firstBrace < 0 {
		if bytes.IndexByte(raw, '[') >= 0 {
			return nil, fmt.Errorf("expected JSON object, got array")
		}
		return nil, fmt.Errorf("no JSON object found in response")
	}

	// Reject array wrappers: if the text before '{', when trimmed, ends with
	// '[' it means the response started a JSON array (e.g. [{...}]).
	// Also scan the pre-text for any valid JSON container — a JSON array
	// preceding the object (e.g. [1,2]\n{...}) must be rejected.
	pre := bytes.TrimSpace(raw[:firstBrace])
	if len(pre) > 0 && pre[len(pre)-1] == '[' {
		return nil, fmt.Errorf("expected JSON object, got array")
	}
	if objs, arrs := countJSONContainers(pre); objs > 0 || arrs > 0 {
		return nil, fmt.Errorf("expected JSON object, got array")
	}

	// Brace-counting scan that respects JSON string rules.
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
				return checkTrailingJSON(raw, firstBrace, i)
			}
		}
	}
	return nil, fmt.Errorf("unterminated JSON object")
}

// checkTrailingJSON verifies that content after the extracted JSON object does
// not contain another JSON value.  It uses encoding/json.Decoder to
// deterministically identify valid JSON containers; natural-language brackets
// like [draft] that are not valid JSON are ignored.  Trailing markdown fences
// are stripped first.
func checkTrailingJSON(raw []byte, start, end int) ([]byte, error) {
	rest := raw[end+1:]

	// Strip trailing markdown fences.
	for {
		rest = bytes.TrimSpace(rest)
		if len(rest) == 0 {
			break
		}
		if bytes.HasSuffix(rest, []byte("```")) {
			rest = bytes.TrimSuffix(rest, []byte("```"))
			rest = bytes.TrimRight(rest, "`")
			rest = bytes.TrimSpace(rest)
			if idx := bytes.LastIndexByte(rest, '\n'); idx >= 0 {
				lastLine := bytes.TrimSpace(rest[idx+1:])
				if !bytes.ContainsAny(lastLine, "{}[]") {
					rest = bytes.TrimSpace(rest[:idx])
				}
			}
			continue
		}
		if idx := bytes.LastIndex(rest, []byte("```")); idx >= 0 {
			after := bytes.TrimSpace(rest[idx+3:])
			if len(after) == 0 {
				rest = bytes.TrimSpace(rest[:idx])
				continue
			}
		}
		break
	}

	// Scan remaining text for valid JSON containers.  Natural-language
	// brackets like [draft] or [仅供参考] fail json.Decode and are skipped.
	objects, arrays := countJSONContainers(rest)
	if objects > 0 || arrays > 0 {
		return nil, fmt.Errorf("multiple JSON values in response")
	}
	return raw[start : end+1], nil
}

// countJSONContainers scans raw for valid top-level JSON objects and arrays
// using encoding/json.Decoder.  Only syntactically complete containers are
// counted; malformed fragments (e.g. natural-language [draft]) are ignored.
func countJSONContainers(raw []byte) (objects int, arrays int) {
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
		switch s[0] {
		case '{':
			objects++
		case '[':
			arrays++
		default:
			continue
		}
		consumed := dec.InputOffset()
		if consumed > 0 {
			i += int(consumed) - 1
		}
	}
	return
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
