package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"workground2/internal/config"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// RequestHelpProgressPrefix identifies structured request_help progress chunks.
// Frontends ignore ordinary tool output that does not start with this prefix.
const RequestHelpProgressPrefix = "request_help_status:"

type requestHelpProgress struct {
	Version    int                    `json:"version"`
	State      string                 `json:"state"`
	RequestID  string                 `json:"request_id"`
	Capability config.ModelCapability `json:"capability"`
	FromModel  string                 `json:"from_model"`
	Model      string                 `json:"model"`
	Attempt    int                    `json:"attempt"`
	Total      int                    `json:"total"`
	Error      string                 `json:"error,omitempty"`
}

// RequestHelpSystemPrompt steers a capability-assist sub-agent.
const RequestHelpSystemPrompt = `You are a capability assistant invoked by a parent coding agent that needs help with a task it cannot perform directly. Use the provided tools to fulfill the request. Return a single final answer — concise and self-contained — that the parent will see as your result. Do not ask the parent for clarification; do your best with the information provided.`

// validAssistCapabilities is the closed set of capabilities request_help accepts.
var validAssistCapabilities = map[config.ModelCapability]bool{
	config.CapWebSearch:       true,
	config.CapImageGeneration: true,
}

// errNoVerifiedImage is returned when image_generation succeeds at the model
// level but no verifiable image artifact is found — neither via the draw_image
// tool result nor via CLI side-effect files reported by the provider.
var errNoVerifiedImage = errors.New("no verified image artifact produced")

// RequestHelpTool lets the primary model delegate a task to a
// capability-matched helper model when it lacks a requested capability.
// The public schema exposes only capability and prompt; the host owns
// provider selection, retry, and all internal state.
type RequestHelpTool struct {
	cfg                 *config.Config
	parentReg           *tool.Registry
	currentModelRef     string
	resolveProvider     func(modelRef, effort string) (provider.Provider, *provider.Pricing, int, error)
	maxSteps            int
	temperature         float64
	contextWindow       int
	softCompactRatio    float64
	toolResultSnipRatio float64
	compactRatio        float64
	compactForceRatio   float64
	recentKeep          int
	gate                Gate
	keepPolicy          KeepPolicy
	maxSubagentDepth    int
	transcripts         *SubagentStore
	workspaceRoot       string
	baseModel           string
	baseEffort          string
}

// NewRequestHelpTool wires a request_help tool.
func NewRequestHelpTool(
	cfg *config.Config,
	parentReg *tool.Registry,
	currentModelRef string,
	resolveProvider func(modelRef, effort string) (provider.Provider, *provider.Pricing, int, error),
	maxSteps int,
	temperature float64,
	contextWindow int,
	recentKeep int,
	softCompactRatio, toolResultSnipRatio, compactRatio, compactForceRatio float64,
	gate Gate,
	keepPolicy KeepPolicy,
	maxSubagentDepth int,
) *RequestHelpTool {
	return &RequestHelpTool{
		cfg:                 cfg,
		parentReg:           parentReg,
		currentModelRef:     currentModelRef,
		resolveProvider:     resolveProvider,
		maxSteps:            maxSteps,
		temperature:         temperature,
		contextWindow:       contextWindow,
		recentKeep:          recentKeep,
		softCompactRatio:    softCompactRatio,
		toolResultSnipRatio: toolResultSnipRatio,
		compactRatio:        compactRatio,
		compactForceRatio:   compactForceRatio,
		gate:                gate,
		keepPolicy:          keepPolicy,
		maxSubagentDepth:    NormalizeMaxSubagentDepth(maxSubagentDepth),
	}
}

// WithTranscripts enables persisted sub-agent transcript continuation.
func (t *RequestHelpTool) WithTranscripts(store *SubagentStore, workspaceRoot, baseModel, baseEffort string) *RequestHelpTool {
	t.transcripts = store
	t.workspaceRoot = strings.TrimSpace(workspaceRoot)
	t.baseModel = strings.TrimSpace(baseModel)
	t.baseEffort = strings.TrimSpace(baseEffort)
	return t
}

func (t *RequestHelpTool) Name() string { return "request_help" }

func (t *RequestHelpTool) Description() string {
	available := make([]string, 0, len(validAssistCapabilities))
	if t.cfg != nil {
		for _, capability := range []config.ModelCapability{config.CapWebSearch, config.CapImageGeneration} {
			current, currentOK := t.cfg.ResolveModel(t.currentModelRef)
			if currentOK && current.HasCapability(capability) {
				continue
			}
			if len(t.cfg.AssistCandidates(t.currentModelRef, capability)) > 0 {
				available = append(available, string(capability))
			}
		}
	}
	availableText := "none currently configured"
	if len(available) > 0 {
		availableText = strings.Join(available, ", ")
	}
	return "Request help from a capability-matched helper model when you lack the required capability for a task. Use this by default instead of telling the user you cannot perform a task — the host will route your request to a model that has the capability you need. The helper model runs in its own session with access to the same tools you have, within the subagent depth boundary. Only the helper's final answer is returned to you. Available helper capabilities in this session: " + availableText + ".\n\nWhen to use each capability:\n- web_search: the user asks to search the web or needs current information and your model cannot browse.\n- image_generation: the user asks you to draw, generate, or create an image and your model lacks image generation capability. Call request_help(image_generation) instead of declining or producing text-only descriptions."
}

func (t *RequestHelpTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "capability":{"type":"string","enum":["web_search","image_generation"],"description":"The capability you need help with. Use web_search when the user needs current web information. Use image_generation when the user asks to draw, generate, or create an image and your model cannot produce one."},
  "prompt":{"type":"string","description":"What you need the helper to accomplish. Be specific — the helper does not see this conversation."}
},
"required":["capability","prompt"]
}`)
}

func (t *RequestHelpTool) ReadOnly() bool { return false }

func (t *RequestHelpTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.cfg == nil {
		return "", fmt.Errorf("request_help: configuration is unavailable")
	}
	var p struct {
		Capability string `json:"capability"`
		Prompt     string `json:"prompt"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("request_help: invalid args: %w", err)
	}
	cap := config.ModelCapability(strings.TrimSpace(p.Capability))
	prompt := strings.TrimSpace(p.Prompt)

	if !validAssistCapabilities[cap] {
		return "", fmt.Errorf("request_help: unknown capability %q; valid values: web_search, image_generation", cap)
	}
	if prompt == "" {
		return "", fmt.Errorf("request_help: prompt is required")
	}
	requestID := assistRequestID(ctx, cap, prompt)
	if current, ok := t.cfg.ResolveModel(t.currentModelRef); ok && current.HasCapability(cap) {
		return "", fmt.Errorf("request_help: current model %q already provides capability %q; handle the request directly", t.currentModelRef, cap)
	}

	// Enforce subagent depth boundary.
	childDepth := SubagentDepth(ctx) + 1
	maxDepth := t.maxSubagentDepth
	if maxDepth == 0 {
		maxDepth = DefaultMaxSubagentDepth
	}
	if childDepth > maxDepth {
		return "", fmt.Errorf("request_help: subagent delegation depth limit reached (max_subagent_depth=%d)", maxDepth)
	}

	candidates := t.cfg.AssistCandidates(t.currentModelRef, cap)
	if len(candidates) == 0 {
		return "", fmt.Errorf("request_help: no usable provider found for capability %q; declare it in provider capabilities and optionally set agent.assist_models", cap)
	}

	maxAttempts := t.cfg.AssistMaxAttempts()
	if len(candidates) > maxAttempts {
		candidates = candidates[:maxAttempts]
	}

	// image_generation: do not auto-retry after RunSubAgentWithSession was
	// attempted (ambiguous artifact risk). Provider-resolution failures are
	// safe to retry—they happen before any work starts.
	noRetryAfterRun := cap == config.CapImageGeneration

	emitProgress := func(state, model string, attempt int, progressErr error) {
		status := requestHelpProgress{
			Version:    1,
			State:      state,
			RequestID:  requestID,
			Capability: cap,
			FromModel:  t.currentModelRef,
			Model:      model,
			Attempt:    attempt,
			Total:      len(candidates),
		}
		if progressErr != nil {
			status.Error = progressErr.Error()
		}
		emitRequestHelpProgress(ctx, status)
	}

	var attempted []string
	var failures []string
	for attempt, ref := range candidates {
		attempted = append(attempted, ref)

		emitProgress("attempting", ref, attempt+1, nil)

		prov, pricing, ctxWin, err := t.resolveProvider(ref, "")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: provider setup: %v", ref, err))
			emitProgress("candidate_failed", ref, attempt+1, err)
			continue // provider resolution failure is always safe to retry
		}

		// Build via the standard subagent boundary, then always remove
		// request_help so a capability helper cannot recursively re-route the
		// same request before returning control to its parent.
		subReg := SubagentToolRegistryForDepth(t.parentReg, nil, childDepth, maxDepth)
		subReg = FilterRegistry(subReg, nil, "request_help")

		run, prepErr := t.prepareRun(ctx, subReg, ref, requestID)
		if prepErr != nil {
			failures = append(failures, fmt.Sprintf("%s: prepare transcript: %v", ref, prepErr))
			emitProgress("candidate_failed", ref, attempt+1, prepErr)
			continue
		}
		if err := t.transcripts.MarkRunning(run); err != nil {
			saveErr := t.transcripts.SaveFailed(run)
			run.Release()
			joined := errors.Join(err, saveErr)
			failures = append(failures, fmt.Sprintf("%s: mark running: %v", ref, joined))
			emitProgress("candidate_failed", ref, attempt+1, joined)
			continue
		}

		opts := Options{
			MaxSteps:            t.maxSteps,
			Temperature:         t.temperature,
			Pricing:             pricing,
			UsageSource:         event.UsageSourceSubagent,
			Gate:                t.gate,
			ContextWindow:       ctxWin,
			RecentKeep:          t.recentKeep,
			SoftCompactRatio:    t.softCompactRatio,
			ToolResultSnipRatio: t.toolResultSnipRatio,
			CompactRatio:        t.compactRatio,
			CompactForceRatio:   t.compactForceRatio,
			KeepPolicy:          t.keepPolicy,
			ResponseLanguage:    ResponseLanguageFromContext(ctx),
			ReasoningLanguage:   ReasoningLanguageFromContext(ctx),
			SubagentDepth:       childDepth,
			MaxSubagentDepth:    maxDepth,
		}

		sink := subSink(ctx)
		// For image_generation, attach a request-scoped artifact collector
		// so the CLI provider (Codex) can report side-effect image files it
		// writes outside the draw_image tool flow.
		runCtx := ctx
		var artifactCollector *provider.ArtifactCollector
		if cap == config.CapImageGeneration {
			artifactCollector = &provider.ArtifactCollector{}
			runCtx = provider.WithArtifactCollector(ctx, artifactCollector)
		}
		answer, runErr := RunSubAgentWithSession(runCtx, prov, subReg, run.Session, assistPrompt(requestID, cap, prompt), opts, sink)

		if runErr != nil {
			// image_generation: the CLI provider may have produced valid
			// side-effect image artifacts (via ArtifactCollector) before
			// the text stream timed out. Validate and recover instead of
			// discarding the artifact.
			if cap == config.CapImageGeneration {
				if validated, valErr := validateCodexArtifact(artifactCollector); valErr == nil {
					artifact := validated
					if err := t.transcripts.SaveCompleted(run); err != nil {
						saveErr := t.transcripts.SaveFailed(run)
						run.Release()
						joined := errors.Join(fmt.Errorf("request_help: persist completed recovery candidate %q: %w", ref, err), saveErr)
						emitProgress("candidate_failed", ref, attempt+1, joined)
						return "", joined
					}
					// answer is "" when RunSubAgentWithSession returns an
					// error — provide a short deterministic answer.
					recoveryAnswer := fmt.Sprintf(
						"Image artifact recovered from CLI provider despite text-stream error.\n"+
							"Recovery diagnostic (original run error): %v", runErr)
					result := formatAssistAnswer(recoveryAnswer, run)
					run.Release()
					emitProgress("completed", ref, attempt+1, nil)
					return formatAssistResult(requestID, cap, t.currentModelRef, ref, attempt+1, len(candidates), failures, &artifact, result), nil
				}
			}

			saveErr := t.transcripts.SaveFailed(run)
			run.Release()
			joined := errors.Join(runErr, saveErr)
			failures = append(failures, fmt.Sprintf("%s: run: %v", ref, joined))
			emitProgress("candidate_failed", ref, attempt+1, joined)
			if noRetryAfterRun {
				// image_generation: RunSubAgentWithSession was called and
				// failed; do not try another candidate to avoid duplicates.
				break
			}
			continue
		}
		if cap == config.CapWebSearch && !hasWebSource(answer) {
			saveErr := t.transcripts.SaveFailed(run)
			run.Release()
			joined := errors.Join(fmt.Errorf("web search answer contains no http(s) source URL"), saveErr)
			failures = append(failures, fmt.Sprintf("%s: result validation: %v", ref, joined))
			emitProgress("candidate_failed", ref, attempt+1, joined)
			continue
		}

		var artifact *imageArtifact
		if cap == config.CapImageGeneration {
			validated, artifactErr := validateImageArtifact(run)
			if artifactErr != nil {
				// Fall back to side-effect artifacts reported by the CLI
				// provider (e.g. Codex CLI generated_images). The collector
				// is request-scoped and was injected before the sub-agent run.
				codexArtifact, codexErr := validateCodexArtifact(artifactCollector)
				if codexErr != nil {
					saveErr := t.transcripts.SaveFailed(run)
					run.Release()
					joined := errors.Join(fmt.Errorf("%w (draw_image: %v; codex artifact: %v)", errNoVerifiedImage, artifactErr, codexErr), saveErr)
					failures = append(failures, fmt.Sprintf("%s: artifact validation: %v", ref, joined))
					emitProgress("candidate_failed", ref, attempt+1, joined)
					break
				}
				validated = codexArtifact
			}
			artifact = &validated
		}

		if err := t.transcripts.SaveCompleted(run); err != nil {
			saveErr := t.transcripts.SaveFailed(run)
			run.Release()
			joined := errors.Join(fmt.Errorf("request_help: persist completed candidate %q: %w", ref, err), saveErr)
			emitProgress("candidate_failed", ref, attempt+1, joined)
			return "", joined
		}
		result := formatAssistAnswer(answer, run)
		run.Release()
		emitProgress("completed", ref, attempt+1, nil)
		return formatAssistResult(requestID, cap, t.currentModelRef, ref, attempt+1, len(candidates), failures, artifact, result), nil
	}

	if len(failures) > 0 {
		return "", fmt.Errorf("request_help: all %d candidate(s) failed for capability %q (request_id=%s; tried: %s): %s",
			len(attempted), cap, requestID, strings.Join(attempted, ", "), strings.Join(failures, "; "))
	}
	return "", fmt.Errorf("request_help: no candidate succeeded for capability %q (request_id=%s; tried: %s)",
		cap, requestID, strings.Join(attempted, ", "))
}

func (t *RequestHelpTool) prepareRun(ctx context.Context, subReg *tool.Registry, modelRef, requestID string) (*SubagentRun, error) {
	parentSession := strings.TrimSpace(ParentSession(ctx))
	if t.transcripts == nil || parentSession == "" {
		return EphemeralSubagentRun(RequestHelpSystemPrompt), nil
	}
	parentID, _, _, _ := CallContext(ctx)
	return t.transcripts.PrepareFresh(SubagentSpec{
		Kind:             "request_help",
		Name:             "request_help:" + requestID,
		WorkspaceRoot:    t.workspaceRoot,
		ParentSession:    parentSession,
		ParentToolCallID: parentID,
		SystemPrompt:     RequestHelpSystemPrompt,
		Registry:         subReg,
		Model:            strings.TrimSpace(modelRef),
		Effort:           strings.TrimSpace(t.baseEffort),
	})
}

func assistRequestID(ctx context.Context, capability config.ModelCapability, prompt string) string {
	parentID, _, _, _ := CallContext(ctx)
	seed := strings.Join([]string{ParentSession(ctx), parentID, string(capability), prompt}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("assist-%x", sum[:8])
}

func assistPrompt(requestID string, capability config.ModelCapability, prompt string) string {
	prefix := fmt.Sprintf("Capability request %s (%s). ", requestID, capability)
	if capability == config.CapImageGeneration {
		return prefix + "You must call the configured draw_image tool and produce a real image file. A text-only claim, URL, or invented path is failure. Return the verified artifact path after the tool succeeds.\n\nTask: " + prompt
	}
	return prefix + "Complete the task and include the source URLs used in your final answer.\n\nTask: " + prompt
}

func formatAssistResult(requestID string, capability config.ModelCapability, fromModelRef, modelRef string, attempt, total int, failures []string, artifact *imageArtifact, result string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Capability assist succeeded\nrequest_id: %s\ncapability: %s\nfrom_model: %s\nmodel: %s\nattempt: %d/%d\n", requestID, capability, fromModelRef, modelRef, attempt, total)
	if len(failures) > 0 {
		b.WriteString("previous_failures:\n- ")
		b.WriteString(strings.Join(failures, "\n- "))
		b.WriteByte('\n')
	}
	if artifact != nil {
		data, _ := json.Marshal(artifact)
		fmt.Fprintf(&b, "artifact: %s\n", data)
	}
	b.WriteByte('\n')
	b.WriteString(result)
	return b.String()
}

func formatAssistAnswer(answer string, run *SubagentRun) string {
	answer = GuardSubagentHostDecisionText(answer)
	if run == nil || run.Ref == "" {
		return answer
	}
	return "Subagent reference: " + run.Ref + "\n\nFinal answer:\n" + answer
}

func hasWebSource(answer string) bool {
	for _, field := range strings.Fields(answer) {
		value := strings.Trim(field, "()[]{}<>,.;'\"")
		if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
			return true
		}
	}
	return false
}

func emitRequestHelpProgress(ctx context.Context, status requestHelpProgress) {
	emit, ok := tool.ProgressFrom(ctx)
	if !ok {
		return
	}
	data, err := json.Marshal(status)
	if err != nil {
		return
	}
	emit(RequestHelpProgressPrefix + string(data) + "\n")
}
