package config

import "strings"

// AssistCandidates returns the ordered list of model refs to try for a
// capability, excluding the current model. Empty when assist is off, the
// capability has no route, or no usable providers exist.
//
// Order:
//  1. Explicit assist_models[capability] entries (user-configured order).
//  2. All configured providers that declare the capability, in config order,
//     with duplicates removed.
//
// Exclusions (applied to both sources):
//   - The current model (matched by resolved provider name + model).
//   - Duplicates (first occurrence wins).
//   - Providers whose HasCapability returns false.
//
// The caller is responsible for bounding attempts to AssistMaxAttempts().
func (c *Config) AssistCandidates(currentModelRef string, cap ModelCapability) []string {
	if c == nil || !c.AssistEnabled() {
		return nil
	}

	// Resolve the current model so we can exclude it.
	currentEntry, currentOK := c.ResolveModel(currentModelRef)

	var seen = map[string]bool{}
	var out []string

	// Phase 1: explicit routes from assist_models.
	// Every ref must resolve AND provide the requested capability.
	if explicit, ok := c.Agent.AssistModels[string(cap)]; ok && len(explicit) > 0 {
		for _, ref := range explicit {
			ref = strings.TrimSpace(ref)
			if ref == "" || seen[ref] {
				continue
			}
			resolved, ok := c.ResolveModel(ref)
			if !ok || !assistProviderReady(resolved) || !resolved.HasCapability(cap) {
				continue // unresolvable or lacks capability → skip
			}
			if isSameProvider(currentEntry, currentOK, c, ref) {
				continue
			}
			seen[ref] = true
			out = append(out, ref)
		}
		return out // explicit routes replace auto-discovery entirely.
	}

	// Phase 2: auto-discover from configured providers.
	for i := range c.Providers {
		entry := &c.Providers[i]
		if !assistProviderReady(entry) || !entry.HasCapability(cap) {
			continue
		}
		ref := entry.Name + "/" + entry.EffectiveModel()
		if seen[ref] {
			continue
		}
		if isSameProvider(currentEntry, currentOK, c, ref) {
			continue
		}
		// For multi-model providers, emit each model that has the capability.
		// For single-model providers, emit the single ref.
		if len(entry.Models) > 1 {
			for _, m := range entry.Models {
				mRef := entry.Name + "/" + strings.TrimSpace(m)
				if seen[mRef] {
					continue
				}
				// Check per-model capability via a temporary entry copy.
				tmp := *entry
				tmp.Model = strings.TrimSpace(m)
				if !tmp.HasCapability(cap) {
					continue
				}
				if isSameProvider(currentEntry, currentOK, c, mRef) {
					continue
				}
				seen[mRef] = true
				out = append(out, mRef)
			}
		} else {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

func assistProviderReady(entry *ProviderEntry) bool {
	if entry == nil || !entry.Configured() {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(entry.Kind), "cli") {
		return strings.TrimSpace(entry.Command) != ""
	}
	return true
}

// isSameProvider reports whether resolving ref would yield the same provider+model
// as the current entry.
func isSameProvider(current *ProviderEntry, currentOK bool, c *Config, ref string) bool {
	if !currentOK || current == nil {
		return false
	}
	resolved, ok := c.ResolveModel(ref)
	if !ok {
		return false
	}
	return resolved.Name == current.Name &&
		strings.EqualFold(resolved.EffectiveModel(), current.EffectiveModel())
}
