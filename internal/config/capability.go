package config

import "strings"

// ModelCapability is a named capability a model/provider may support.
// The set is open-ended: new capabilities are added as providers and
// frontends evolve.
type ModelCapability string

const (
	// CapVision means the model can accept image inputs (data URLs).
	CapVision ModelCapability = "vision"

	// CapReasoning means the model exposes thinking/reasoning tokens
	// separately from the visible output (e.g. o1/o3/deepseek-reasoner).
	CapReasoning ModelCapability = "reasoning"
)

// builtinModelCapabilities maps canonical model IDs to their known
// capabilities.  It is the source of truth for well-known public models
// and is consulted when the user has not supplied explicit capabilities
// in their provider config.
//
// Key format: lowercase model id.  For CLI providers the key is the
// tool name (e.g. "codex").
var builtinModelCapabilities = map[string][]ModelCapability{
	// ── OpenAI ──────────────────────────────────────────────
	"gpt-4o":              {CapVision, CapReasoning},
	"gpt-4o-mini":         {CapVision},
	"gpt-4-turbo":         {CapVision},
	"gpt-4.1":             {CapVision},
	"gpt-4.1-mini":        {CapVision},
	"gpt-4.1-nano":        {CapVision},
	"o1":                  {CapReasoning},
	"o1-mini":             {CapReasoning},
	"o3":                  {CapVision, CapReasoning},
	"o3-mini":             {CapReasoning},
	"o4-mini":             {CapVision, CapReasoning},
	"gpt-5":               {CapVision, CapReasoning},
	"gpt-5.5":             {CapVision, CapReasoning},
	"gpt-5-mini":          {CapVision},
	"gpt-5-nano":          {CapVision},

	// ── Anthropic ───────────────────────────────────────────
	"claude-3-opus-20240229":   {CapVision},
	"claude-3-sonnet-20240229": {CapVision},
	"claude-3-haiku-20240307":  {CapVision},
	"claude-3-5-sonnet-20241022": {CapVision},
	"claude-3-5-haiku-20241022":  {CapVision},
	"claude-sonnet-4-20250514": {CapVision, CapReasoning},
	"claude-opus-4-20250514":   {CapVision, CapReasoning},

	// ── Google ──────────────────────────────────────────────
	"gemini-2.5-pro":  {CapVision, CapReasoning},
	"gemini-2.5-flash": {CapVision},
	"gemini-2.0-flash": {CapVision},

	// ── DeepSeek ────────────────────────────────────────────
	"deepseek-chat":     {},
	"deepseek-reasoner": {CapReasoning},

	// ── MiMo ────────────────────────────────────────────────
	"mimo-v2.5":    {CapVision},
	"mimo-v2-omni": {CapVision},
}

// cliToolCapabilities maps known CLI tool names to their capabilities.
// CLI tools act as opaque LLM proxies, so the database records what the
// underlying tool supports.  Unknown CLI tools default to no capabilities.
var cliToolCapabilities = map[string][]ModelCapability{
	"codex": {CapVision},
}

// visionModelPrefixes lists model-name prefixes whose dated/suffixed variants
// inherit vision capability from the base family (e.g. gpt-5.5, gpt-4o-2024-08-06).
var visionModelPrefixes = []string{
	"gpt-4o", "gpt-4.1", "gpt-5",    // OpenAI vision families
	"claude-",                        // all Claude models that match
	"gemini-",                        // all Gemini chat models that match
}

// HasCapability reports whether the model/provider entry supports cap.
// Resolution order:
//  1. model_overrides (per-model, highest priority)
//  2. Explicit [providers.xxx] capabilities list
//  3. Legacy Vision / VisionModels fields (backward compat)
//  4. Built-in database (this file)
func (e *ProviderEntry) HasCapability(cap ModelCapability) bool {
	if e == nil {
		return false
	}

	// 1. model_overrides
	if e.visionOverride != nil && cap == CapVision {
		return *e.visionOverride
	}

	// 2. Explicit capabilities list
	if len(e.Capabilities) > 0 {
		for _, c := range e.Capabilities {
			if ModelCapability(strings.TrimSpace(c)) == cap {
				return true
			}
		}
		return false
	}

	// 3. Legacy fields
	if cap == CapVision {
		if e.Vision {
			return true
		}
		if e.HasVisionModel(e.Model) {
			return true
		}
	}

	// 4. Built-in database (with endpoint gating for select providers)
	if cap == CapVision && isMimoModel(e.Model) && !isOfficialMimoEndpoint(e.BaseURL) {
		// MiMo models on custom proxies need explicit vision=true;
		// the built-in entry only activates on official endpoints.
		return false
	}
	return hasBuiltinCapability(e.Kind, e.EffectiveModel(), cap)
}

// EffectiveModel returns the concrete model name, resolving Models+Default when
// Model (singular) is empty.
func (e *ProviderEntry) EffectiveModel() string {
	if m := strings.TrimSpace(e.Model); m != "" {
		return m
	}
	return e.DefaultModel()
}

func isMimoModel(model string) bool {
	return mimoVisionModels[strings.ToLower(strings.TrimSpace(model))]
}

func isOfficialMimoEndpoint(baseURL string) bool {
	switch officialMimoHost(baseURL) {
	case "api.xiaomimimo.com", "token-plan-cn.xiaomimimo.com":
		return true
	default:
		return false
	}
}

func hasBuiltinCapability(kind, model string, cap ModelCapability) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	lower := strings.ToLower(model)

	// CLI providers proxy to an underlying LLM: check the tool name first,
	// then the model name against the general database (the capabilities
	// of the underlying model are the capabilities of the proxy).
	if kind == "cli" {
		if caps, ok := cliToolCapabilities[lower]; ok {
			for _, c := range caps {
				if c == cap {
					return true
				}
			}
		}
		// Fall through to check the underlying model name.
	}

	// Exact model match
	if caps, ok := builtinModelCapabilities[lower]; ok {
		for _, c := range caps {
			if c == cap {
				return true
			}
		}
		return false
	}

	// Prefix match for versioned/suffixed variants of known vision families.
	// e.g. gpt-5.5 inherits from gpt-5, claude-sonnet-4-20250514 from claude-
	if cap == CapVision {
		for _, prefix := range visionModelPrefixes {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
	}

	// Safe prefix family match: only for families where date-tagged / minor
	// variants inherit the base model capabilities (e.g. gpt-4o-2024-08-06).
	// Unsafe families (like mimo-v2.5 / mimo-v2.5-pro where -pro is a different
	// capability tier) are excluded — they must be listed explicitly.
	if cap == CapVision {
		if IsLikelyVisionModel(model) {
			return true
		}
	}

	return false
}

// EntryCapabilities returns the effective capability list for a provider entry
// as a []string suitable for passing to provider.Config.Extra.  It collects
// every capability known to the entry via the same resolution order as
// HasCapability.
func EntryCapabilities(e *ProviderEntry) []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, cap := range []ModelCapability{CapVision, CapReasoning} {
		if e.HasCapability(cap) {
			out = append(out, string(cap))
		}
	}
	return out
}
