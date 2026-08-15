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
	locale := ""
	if input.Work != nil {
		workID = input.Work.ID
		locale = input.Work.Locale
	}

	const maxTries = 3
	var lastRaw string
	var lastErr error

	for attempt := 0; attempt < maxTries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("boot: PlanDefinition: %w", err)
		}

		msgs := buildDefinitionPlanMessages(
			attempt, input.Intent, baseJSON, lastRaw, lastErr, locale, input.StructuralAnswers,
		)
		attemptNo := attempt + 1
		iid := interactionID("definition", workID, attemptNo)
		p.llmLog.logRequest(iid, "definition", workID, p.prov.Name(), attemptNo, msgs, p.temperature, p.maxTokens)

		if input.OnProgress != nil && attempt == 0 {
			input.OnProgress(work.DefinitionPlanProgress{Kind: "analyzing", Text: "正在理解任务并拆分工作节点"})
		}
		raw, streamErr := p.streamDefinitionPlan(ctx, msgs, input.OnProgress)
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
			emitDefinitionSemanticProgress(plan, input.OnProgress)
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
- The object must contain ONLY these five fields: goal, nodes, artifactSlots, inputSpecs, structuralQuestions. No other top-level fields.
- Return ONLY the JSON — no markdown, no code fences, no commentary, no explanations, no preamble, no postscript.
- Think silently. The first non-whitespace character of your response must be { and the last must be }.
- Do not quote, restate, or discuss the input JSON, a draft JSON, or these instructions.

Planning rules:
- The object is a complete replacement candidate derived from the authoritative base.
- Nodes may be added, removed, reordered, or changed. Dependencies must form a DAG.
- Every referenced inputSpecId and artifact slot ID must exist in the returned object.
- Every artifact slot must have exactly one producer node.
- Artifact slots are deliverable outputs created by the workflow and shown to the user in the Results shelf. Never use an artifact slot for an original/source/input file, an upload, reference material, or an existing workspace file.
- When the requested deliverable is a release page, deployed site, published document, or another web destination, use artifact kind "url" ("link" is accepted for compatibility). A URL artifact is the actual absolute http/https link from the producer's final response; do not invent a file deliverable for it.
- A file supplied by the user must be a file InputSpec referenced through the consuming node's inputSpecIds. That node consumes the submitted file; it must not reproduce the same file through producesSlotIds.
- Do not create an "upload", "collect source file", or similar producer task for a user-supplied file. Attach the file InputSpec directly to the first real task that uses it.
- Only include a source or input file in artifactSlots when the user explicitly requests a newly generated copy or transformed version as a deliverable. The transformed output must have its own output-oriented slot ID and title.
- When a node produces an image artifact slot, include "image_generation" in that node's toolHints. At execution time this is routed through the shared capability path and request_help(image_generation).
- Preserve stable IDs for unchanged concepts. Use short deterministic IDs for new concepts.
- For text inputs that require multiple lines, lists, or one item per line, set valueSchema.multiline to true.
- For choice and multi_choice inputs, valueSchema.options is required and must be a JSON array of {"value":"...","label":"..."} objects.
- Do not return revision, parentRevision, status, digest, workId, createdAt, or createdBy.
- Do not return null collections or a clone when the intent requests structural changes.

Structural clarification rules — default to no question:
- structuralQuestions MUST normally be [].
- Return at most ONE structural question, and only when the intent/base leave at least two materially different, reasonable workflow topologies and no safe, reversible default can be inferred.
- A permitted question must change node boundaries or membership, dependency edges, parallel-vs-sequential topology, or which node owns an input/artifact slot. Only ask when the user is the sole source of that decision.
- NEVER ask about content, facts, wording, quality, tone, language, audience, private values, files, authorization, output details, or searchable information here. Those belong to InputSpecs, web_search, or ordinary planning.
- NEVER ask for generic approval preferences, review frequency, confirmation gates, whether to pause after a step, or whether the user accepts the inferred plan.
- NEVER ask when one option is recommended, conventional, safer, reversible, or inferable from the intent/base. Choose it and return [].
- Options must be neutral structural alternatives. Do not mark an option recommended. Do not repeat a question whose ID appears in Confirmed work-structure decisions JSON.

InputSpec rules — when to ask the user vs when the system will search:
- Information that can be found through public web search (facts, documentation, APIs, references, examples) must NOT generate an InputSpec. Instead, set toolHints: ["web_search"] on the node that needs it. At execution time the node uses native/direct search when available and otherwise delegates through request_help(web_search).
- Only generate a required InputSpec when the information is indispensable for completing the work AND can only come from the user: private preferences, constraints, authorization, access to private systems, files the user must provide, or decisions only the user can make.
- Enumerate every distinct indispensable user-owned decision or constraint. Do not collapse audience, scope, format, deadline, budget, tone, target environment, acceptance boundary, and source material into one broad free-form question when several independently affect execution.
- Keep each InputSpec answerable as one coherent value. Prefer several precise typed inputs over one vague "other requirements" field, while still excluding optional details and values with safe defaults.
- Label must read like a natural question that is specific and user-facing (e.g. "Who is the intended audience?"). Description must be one or two concrete sentences that explain how the answer changes the work and, where useful, name the expected level of detail or an example.
- Choose the most specific InputKind: text for free-form answers, number for quantities, date for deadlines, choice for single-select, multi_choice for multi-select, file for uploads, roster for lists of people, form for structured objects, approval for sign-offs.
- Group inputs that belong to the same phase under the same node via inputSpecIds so they materialize as a single Block.
- Every required InputSpec MUST be referenced by at least one NodeDef.inputSpecIds. An unreferenced required InputSpec is an invalid plan that will be rejected.
- Do NOT generate inputs that are optional, have safe defaults, duplicate another input, or are irrelevant to the deliverable.

Acceptance criteria rules — every node must declare concrete, verifiable success conditions:
- Every NodeDef MUST include 2-5 acceptanceCriteria. Each criterion must name a specific, observable outcome — a concrete deliverable, evidence, or property that can be checked after execution. Never use vague phrases like "complete the task", "ensure quality", "deliver as requested", or "finish the work".
- Good criteria name concrete artifacts ("translated PDF saved to slot"), observable properties ("all cited URLs are real and accessible"), specific constraints ("response includes at least 3 source URLs"), or verifiable evidence ("search results confirm the latest API version").
- When a node has toolHints including "web_search", at least one criterion MUST require search evidence with source URLs: e.g. "response cites at least N search result URLs" or "each factual claim is backed by a cited source URL".
- When a node produces artifact slots, criteria must cover slot delivery: e.g. "image slot X populated with a generated image", "text slot Y contains the complete report, not a summary".
- Criteria drive a mandatory post-execution quality pass. The host separately verifies objective evidence such as successful required-capability calls and declared artifact outputs before marking the node completed.
- Never use criteria as a substitute for node description or goal. They are the contract the model must fulfill.`

const definitionPlanOutputReminder = `Validate silently before responding.
Your entire response must begin with { and end with }. Return exactly ONE JSON object and nothing else.
Always include structuralQuestions; use [] unless a permitted non-inferable topology decision exists.
Do not emit analysis, alternatives, quoted JSON, a second JSON value, markdown, or commentary.`

// definitionPlanSchema is the static, reviewable full DefinitionPlan JSON
// schema appended to the first-turn system prompt. It includes nested
// schemas for NodeDef, ArtifactSlotDef, InputSpec, and the allowed InputKind
// values, plus a complete example.
const definitionPlanSchema = `

## DefinitionPlan JSON Schema — complete replacement candidate

Return exactly ONE JSON object — no wrapping, no commentary, no markdown fences.
The object must have ONLY these five top-level fields (no others allowed):

### Top-level object
{
  "goal": "string (required, non-null)",
  "nodes": [ ... NodeDef ... ] (required, non-null JSON array),
  "artifactSlots": [ ... ArtifactSlotDef ... ] (required, non-null JSON array),
  "inputSpecs": [ ... InputSpec ... ] (required, non-null JSON array),
  "structuralQuestions": [ ... StructuralQuestion ... ] (required, non-null JSON array; normally [])
}

No other top-level fields are allowed.

### NodeDef (each element in the nodes array)
{
  "id": "string (required)",
  "title": "string (required)",
  "description": "string (optional)",
  "dependsOn": ["string (optional) — node IDs"],
  "inputSpecIds": ["string (optional) — input spec IDs"],
  "toolHints": ["string (optional) — use \"web_search\" for current/public web information and \"image_generation\" when the node produces an image slot"],
  "blockIds": ["string (optional)"],
  "producesSlotIds": ["string (optional)"],
  "consumesSlotIds": ["string (optional)"],
  "acceptanceCriteria": ["string (required) — 2-5 concrete, observable, deliverable-oriented conditions that will be checked at the post-execution gate. Never use vague phrases like \"complete the task\" or \"ensure quality\"."],
  "globalGate": "string (optional)"
}

### ArtifactSlotDef (each element in the artifactSlots array)
{
  "id": "string (required)",
  "title": "string (required) — a deliverable output shown in the Results shelf; never the original/source/input file",
  "kind": "string (required) — e.g. text, document, image, code, url. Use url for an actual absolute http/https link rather than a file.",
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

### StructuralQuestion (zero or one element in structuralQuestions)
{
  "id": "short stable lowercase identifier (required)",
  "impact": "one of: task_nodes, task_dependencies, input_slots, artifact_slots (required)",
  "question": "a concise question describing the unresolved topology choice (required)",
  "description": "why the intent/base cannot determine this structural choice (optional)",
  "options": [
    {
      "id": "short stable lowercase identifier (required)",
      "label": "neutral structural alternative (required)",
      "description": "how this alternative changes the workflow topology (optional)",
      "custom": boolean (optional; at most one custom option)
    }
  ],
  "customPlaceholder": "structure-only example for a custom option (optional)"
}

The collection must be [] whenever the structure can be safely inferred. StructuralQuestion must never contain flow or recommended fields.

### Complete example — user file is input, translated file is the only deliverable
{
  "goal": "Translate the user-provided PDF and deliver the translated PDF",
  "nodes": [
    {
      "id": "translate",
      "title": "Translate document",
      "description": "Translate the uploaded source PDF into the requested language",
      "inputSpecIds": ["source_pdf"],
      "producesSlotIds": ["translated_pdf"],
      "acceptanceCriteria": [
        "translated PDF file saved to the translated_pdf artifact slot",
        "translation preserves all original formatting, tables, and images",
        "all text is translated to the target language with no untranslated passages"
      ]
    }
  ],
  "artifactSlots": [
    {"id": "translated_pdf", "title": "Translated PDF", "kind": "pdf", "expectedCount": 1, "required": true}
  ],
  "inputSpecs": [
    {"id": "source_pdf", "label": "Which PDF should be translated?", "description": "This is the source document used by the translation task.", "kind": "file", "required": true, "pinEligible": false}
  ],
  "structuralQuestions": []
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
func (p *bootDefinitionPlanner) streamDefinitionPlan(
	ctx context.Context,
	msgs []provider.Message,
	onProgress func(work.DefinitionPlanProgress),
) (string, error) {
	chunks, err := p.prov.Stream(ctx, provider.Request{
		Messages:    msgs,
		Temperature: p.temperature,
		MaxTokens:   p.maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("boot: PlanDefinition stream: %w", err)
	}
	var output bytes.Buffer
	jsonStarted := false
	for chunk := range chunks {
		switch chunk.Type {
		case provider.ChunkError:
			return output.String(), fmt.Errorf("boot: PlanDefinition chunk error: %w", chunk.Err)
		case provider.ChunkDone:
			return output.String(), nil
		default:
			output.WriteString(chunk.Text)
			if onProgress != nil {
				visible := chunk.Text
				if !jsonStarted {
					if start := strings.IndexByte(visible, '{'); start >= 0 {
						jsonStarted = true
						visible = visible[start:]
					} else {
						visible = ""
					}
				}
				if visible != "" {
					onProgress(work.DefinitionPlanProgress{Kind: "raw", Text: visible})
				}
			}
		}
	}
	return output.String(), nil
}

// buildDefinitionPlanMessages returns the message list for a given attempt.
// Attempt 0 uses the full system prompt with nested schema + user input.
// Repair attempts include only the last syntactically complete object
// candidate, never the full raw response, so analysis and duplicate objects
// are not fed back to the model as assistant history.
func buildDefinitionPlanMessages(
	attempt int,
	intent, baseJSON, lastRaw string,
	lastErr error,
	locale string,
	answerSets ...[]work.DefinitionStructuralAnswer,
) []provider.Message {
	system := definitionPlannerPrompt + definitionPlanSchema
	if directive := work.LocaleDirective(locale); directive != "" {
		system += "\n\n## Work language\n- " + directive
	}
	structuralDecisions := ""
	var answers []work.DefinitionStructuralAnswer
	if len(answerSets) > 0 {
		answers = answerSets[0]
	}
	if len(answers) > 0 {
		raw, _ := json.Marshal(answers)
		structuralDecisions = fmt.Sprintf(
			"\n\nConfirmed work-structure decisions JSON:\n%s\nApply these decisions only to nodes, dependencies, input slots, or artifact slots. Do not reinterpret them as content preferences. Do not repeat any structural question whose ID appears here.",
			raw,
		)
	}
	if attempt == 0 {
		return []provider.Message{
			{Role: provider.RoleSystem, Content: system},
			{Role: provider.RoleUser, Content: fmt.Sprintf(
				"Natural-language structure intent:\n%s%s\n\nAuthoritative base definition JSON:\n%s\n\n%s",
				intent, structuralDecisions, baseJSON, definitionPlanOutputReminder,
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
		{Role: provider.RoleSystem, Content: system},
		{Role: provider.RoleUser, Content: fmt.Sprintf(
			"Repair a DefinitionPlan response.\n\nNatural-language structure intent:\n%s%s\n\nAuthoritative base definition JSON:\n%s\n\nParse error category: %s\n\n%s\n\nPreserve the candidate's business semantics. Fix only JSON structure and schema violations; do NOT re-plan, add alternatives, explain the error, or quote the candidate before the answer.\n\n%s",
			intent,
			structuralDecisions,
			baseJSON,
			errCat,
			candidateSection,
			definitionPlanOutputReminder,
		)},
	}
}

func emitDefinitionSemanticProgress(plan *work.DefinitionPlan, emit func(work.DefinitionPlanProgress)) {
	if plan == nil || emit == nil {
		return
	}
	for _, node := range plan.Nodes {
		if title := strings.TrimSpace(node.Title); title != "" {
			emit(work.DefinitionPlanProgress{Kind: "node", Text: "已建立 · " + title})
		}
	}
	titles := make(map[string]string, len(plan.Nodes))
	for _, node := range plan.Nodes {
		titles[node.ID] = strings.TrimSpace(node.Title)
	}
	for _, node := range plan.Nodes {
		for _, dependency := range node.DependsOn {
			from, to := titles[dependency], strings.TrimSpace(node.Title)
			if from != "" && to != "" {
				emit(work.DefinitionPlanProgress{Kind: "dependency", Text: "正在连接 · " + from + " → " + to})
			}
		}
	}
	if len(plan.StructuralQuestions) > 0 {
		emit(work.DefinitionPlanProgress{Kind: "ambiguity", Text: "发现一个无法可靠推导的结构分歧"})
		return
	}
	emit(work.DefinitionPlanProgress{Kind: "complete", Text: "工作结构已生成"})
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

// definitionPlanContract keeps the original four fields required for
// compatibility and accepts the newer structuralQuestions collection.
// New planner prompts always require structuralQuestions, usually as [].
var definitionPlanContract = struct {
	required    []string
	collections map[string]bool
}{
	required:    []string{"goal", "nodes", "artifactSlots", "inputSpecs"},
	collections: map[string]bool{"nodes": true, "artifactSlots": true, "inputSpecs": true, "structuralQuestions": true},
}

// decodeDefinitionPlan validates the raw JSON against the frozen DefinitionPlan
// contract: legacy required fields are present and non-null, collections must
// be JSON arrays, no unknown fields exist at any level, and types must match.
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
	if val, ok := raw["structuralQuestions"]; ok {
		if isJSONNull(val) {
			return nil, fmt.Errorf("boot: PlanDefinition: field structuralQuestions must not be null")
		}
		if !isJSONArray(val) {
			return nil, fmt.Errorf("boot: PlanDefinition: field structuralQuestions must be a JSON array")
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
	if questions, ok := raw["structuralQuestions"]; ok {
		if err := strictUnmarshal(questions, &plan.StructuralQuestions); err != nil {
			return nil, fmt.Errorf("boot: PlanDefinition: invalid structuralQuestions: %w", safeDecodeError(err))
		}
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

	// Validate acceptance criteria concreteness for each node.
	// Old definitions without criteria pass (backward compatible);
	// new definitions must have concrete, non-vague criteria.
	for i, node := range plan.Nodes {
		if len(node.AcceptanceCriteria) == 0 {
			// Backward compatible — old planner output or simple nodes.
			continue
		}
		if msg := validateAcceptanceCriteria(node.AcceptanceCriteria, node.ToolHints); msg != "" {
			return nil, fmt.Errorf("boot: PlanDefinition: node %q acceptanceCriteria: %s", node.ID, msg)
		}
		_ = i // reserved for future use
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
//   - field name from the frozen contract only (goal / nodes / artifactSlots / inputSpecs / structuralQuestions)
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

// validateAcceptanceCriteria checks that criteria are concrete and observable.
// Returns an error message on the first vague criterion, or "" if all pass.
func validateAcceptanceCriteria(criteria []string, toolHints []string) string {
	if len(criteria) == 0 {
		return ""
	}
	if len(criteria) < 2 {
		return "at least 2 concrete acceptance criteria required"
	}
	if len(criteria) > 8 {
		return "at most 8 acceptance criteria allowed"
	}

	// Vague phrases that indicate no concrete deliverable is named.
	vaguePatterns := []string{
		"complete the task", "complete task", "finish the work", "finish work",
		"ensure quality", "ensure correctness", "guarantee quality",
		"do a good job", "do good work", "be thorough",
		"deliver as requested", "deliver as required", "fulfill the request",
		"meet requirements", "meet the requirements", "satisfy requirements",
		"follow instructions", "do what is asked",
		"produce output", "generate output", "create output",
		"complete successfully", "finish successfully",
		"work is done", "task is done", "node is done",
		"everything works", "it works", "works correctly",
		"no errors", "error free", "without errors",
	}

	hasWebSearch := false
	for _, h := range toolHints {
		if strings.TrimSpace(h) == "web_search" {
			hasWebSearch = true
			break
		}
	}

	for i, c := range criteria {
		c = strings.TrimSpace(c)
		if c == "" {
			return fmt.Sprintf("criterion %d is empty", i+1)
		}
		lower := strings.ToLower(c)
		for _, vp := range vaguePatterns {
			if strings.Contains(lower, vp) {
				return fmt.Sprintf("criterion %d is too vague (%q); name a specific observable outcome", i+1, c)
			}
		}
		// Very short criteria are likely vague.
		if len(c) < 15 {
			return fmt.Sprintf("criterion %d is too short (%q); must describe a specific observable outcome", i+1, c)
		}
	}

	// Web search nodes must have at least one criterion mentioning search evidence.
	if hasWebSearch {
		hasSearchCriterion := false
		searchHints := []string{"url", "source", "cite", "citation", "search result", "reference", "link"}
		for _, c := range criteria {
			lower := strings.ToLower(c)
			for _, hint := range searchHints {
				if strings.Contains(lower, hint) {
					hasSearchCriterion = true
					break
				}
			}
			if hasSearchCriterion {
				break
			}
		}
		if !hasSearchCriterion {
			return "nodes with web_search tool hint must include at least one acceptance criterion requiring search evidence (URL, source, or citation)"
		}
	}

	return ""
}

var _ work.DefinitionPlanner = (*bootDefinitionPlanner)(nil)
