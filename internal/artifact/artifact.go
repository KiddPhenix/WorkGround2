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
