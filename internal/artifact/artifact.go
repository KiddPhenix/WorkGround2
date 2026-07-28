// Package artifact provides a shared, transport-agnostic artifact discovery
// contract consumed by both Desktop sessions and Work Task sessions. It keeps
// validation (MIME, path-boundary, image-decode) in one place so new Session
// capabilities only need to implement the Producer interface.
package artifact

import (
	"strings"

	"workground2/internal/provider"
)

// Discovered is a validated artifact found in session tool-call history.
type Discovered struct {
	Name        string // file name, e.g. "output.png"
	Type        string // MIME type, e.g. "image/png"
	Kind        string // optional Work slot kind supplied by the producer
	Path        string // absolute path on disk
	Data        []byte // validated content; nil when only the path is known
	SourceRunID string // tool call ID that produced this artifact
}

// SlotKind returns the Work ArtifactSlot kind category for this artifact.
// It is a deterministic mapping from MIME / file extension to slot kind.
func (d Discovered) SlotKind() string {
	if kind := strings.ToLower(strings.TrimSpace(d.Kind)); kind != "" {
		return kind
	}
	t := strings.ToLower(strings.TrimSpace(d.Type))
	if strings.HasPrefix(t, "image/") {
		return "image"
	}
	if strings.HasPrefix(t, "video/") {
		return "video"
	}
	if strings.HasPrefix(t, "audio/") {
		return "audio"
	}
	// Classify common binary / document types by extension.
	name := strings.ToLower(d.Name)
	if strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".jpg") ||
		strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".gif") {
		return "image"
	}
	if strings.HasSuffix(name, ".mp4") || strings.HasSuffix(name, ".webm") ||
		strings.HasSuffix(name, ".mov") || strings.HasSuffix(name, ".avi") ||
		strings.HasSuffix(name, ".mkv") {
		return "video"
	}
	if strings.HasSuffix(name, ".mp3") || strings.HasSuffix(name, ".wav") ||
		strings.HasSuffix(name, ".ogg") || strings.HasSuffix(name, ".flac") ||
		strings.HasSuffix(name, ".aac") || strings.HasSuffix(name, ".wma") {
		return "audio"
	}
	if strings.HasSuffix(name, ".xlsx") || strings.HasSuffix(name, ".xls") {
		return "xlsx"
	}
	if strings.HasSuffix(name, ".pdf") {
		return "document"
	}
	if strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") ||
		strings.HasSuffix(name, ".tar") || strings.HasSuffix(name, ".gz") ||
		strings.HasSuffix(name, ".7z") {
		return "archive"
	}
	// For files where we only have a path (no content), default to "file".
	if d.Data == nil {
		return "file"
	}
	return "binary"
}

// Producer discovers artifacts from a single tool-call + result pair.
// Implementations must be safe for concurrent use and must not retain
// references to the input messages after Discover returns.
type Producer interface {
	// Discover examines a tool call and its result. It returns nil (not an
	// empty slice) when the producer does not recognise the tool or the
	// result does not contain artifacts this producer handles.
	Discover(call provider.ToolCall, result provider.Message) []Discovered
}

// Collect gathers artifacts from message history by running every producer
// against every tool-call/result pair. Results are deduplicated by Path.
func Collect(msgs []provider.Message, producers []Producer) []Discovered {
	if len(msgs) == 0 || len(producers) == 0 {
		return nil
	}
	results := historyToolResultsByID(msgs)
	if len(results) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var out []Discovered
	for _, msg := range msgs {
		if msg.Role != provider.RoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			result, ok := results[tc.ID]
			if tc.ID == "" || !ok || historyToolResultFailed(result.Content) {
				continue
			}
			for _, p := range producers {
				discovered := p.Discover(tc, result)
				for _, d := range discovered {
					key := d.Path
					if key == "" && len(d.Data) > 0 {
						key = d.SourceRunID + "/" + d.Name
					}
					if key == "" || seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// DefaultProducers returns the built-in set of artifact producers.
// Callers may append additional custom producers.
func DefaultProducers() []Producer {
	return []Producer{
		&ImageProducer{},
		&FileProducer{},
	}
}

// ── Capability-based slot guidance ───────────────────────────────────────

// SlotGuidance maps an artifact slot kind to the capability-based guidance
// that should appear in Work node prompts. It is the shared contract between
// artifact Producers and Work prompt generation — Work never hardcodes
// per-kind routing.
type SlotGuidance struct {
	Kind       string // e.g. "image"
	Capability string // e.g. "image_generation"
	Guidance   string // tool-use instructions injected into the node prompt
}

// CapabilityProducer is an optional Producer extension. Producers that
// satisfy artifact slots through a specific model capability (e.g.
// request_help(image_generation)) implement this to declare which slot
// kinds they cover and what prompt guidance agents need.
type CapabilityProducer interface {
	Producer
	// SlotKinds returns the artifact slot kinds this producer can satisfy.
	SlotKinds() []string
	// SlotCapability returns the capability/tool hint used to produce them.
	SlotCapability() string
	// SlotPromptGuidance returns prompt instructions to inject when a node
	// produces slots of the declared kinds. It should tell the model which
	// tool/capability to use and what output to expect.
	SlotPromptGuidance() string
}

// CollectSlotGuidance returns the consolidated guidance entries from
// producers that implement CapabilityProducer.
func CollectSlotGuidance(producers []Producer) []SlotGuidance {
	var out []SlotGuidance
	for _, p := range producers {
		cp, ok := p.(CapabilityProducer)
		if !ok {
			continue
		}
		kinds := cp.SlotKinds()
		capability := strings.ToLower(strings.TrimSpace(cp.SlotCapability()))
		guidance := strings.TrimSpace(cp.SlotPromptGuidance())
		if len(kinds) == 0 || capability == "" || guidance == "" {
			continue
		}
		for _, k := range kinds {
			kind := strings.ToLower(strings.TrimSpace(k))
			if kind == "" {
				continue
			}
			out = append(out, SlotGuidance{
				Kind:       kind,
				Capability: capability,
				Guidance:   guidance,
			})
		}
	}
	return out
}

// ── helpers ────────────────────────────────────────────────────────────────

func historyToolResultsByID(msgs []provider.Message) map[string]provider.Message {
	out := make(map[string]provider.Message)
	for _, msg := range msgs {
		if msg.Role == provider.RoleTool && msg.ToolCallID != "" {
			out[msg.ToolCallID] = msg
		}
	}
	return out
}

func historyToolResultFailed(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "error:") ||
		strings.HasPrefix(content, "blocked:") ||
		strings.HasPrefix(content, "Error:") ||
		strings.HasPrefix(content, "[error")
}
