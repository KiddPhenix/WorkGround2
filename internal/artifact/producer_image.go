package artifact

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"workground2/internal/provider"
)

// ImageProducer discovers artifacts from request_help(image_generation) tool
// results. It parses the structured header output, extracts the artifact JSON
// block, and validates the image file through ValidateImageFile.
type ImageProducer struct{}

func (p *ImageProducer) Discover(call provider.ToolCall, result provider.Message) []Discovered {
	if call.Name != "request_help" {
		return nil
	}
	path, ok := parseRequestHelpImageArtifact(call.Arguments, result.Content)
	if !ok || path == "" {
		return nil
	}
	data, mime, err := ValidateImageFile(path)
	if err != nil {
		return nil
	}
	return []Discovered{{
		Name:        filepath.Base(path),
		Type:        mime,
		Path:        path,
		Data:        data,
		SourceRunID: call.ID,
	}}
}

// CapabilityProducer implementation — tells Work prompt generation that
// image slots require request_help(image_generation).
func (p *ImageProducer) SlotKinds() []string    { return []string{"image"} }
func (p *ImageProducer) SlotCapability() string { return "image_generation" }
func (p *ImageProducer) SlotPromptGuidance() string {
	return "Call request_help with capability=image_generation and a detailed visual description as the prompt. " +
		"The helper model will return a structured artifact header with the generated image path — " +
		"do not embed or describe the image in your final response text."
}

// parseRequestHelpImageArtifact extracts the absolute image path from a
// successful request_help(image_generation) tool result. It validates:
//   - tool call capability is "image_generation"
//   - output starts with "Capability assist succeeded"
//   - structured header contains capability: image_generation
//   - artifact JSON block is present and contains a non-empty path
//
// Only the first capability/artifact header block is trusted; body overrides
// are ignored.
func parseRequestHelpImageArtifact(argsJSON, output string) (string, bool) {
	var args struct {
		Capability string `json:"capability"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.Capability != "image_generation" {
		return "", false
	}
	lines := strings.Split(output, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "Capability assist succeeded" {
		return "", false
	}
	var capability, artifactJSON string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if after, ok := strings.CutPrefix(line, "capability: "); ok {
			if capability != "" {
				return "", false
			}
			capability = strings.TrimSpace(after)
		}
		if after, ok := strings.CutPrefix(line, "artifact: "); ok {
			if artifactJSON != "" {
				return "", false
			}
			artifactJSON = strings.TrimSpace(after)
		}
	}
	if capability != "image_generation" || artifactJSON == "" {
		return "", false
	}
	var artifact struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(artifactJSON), &artifact); err != nil {
		return "", false
	}
	path := strings.TrimSpace(artifact.Path)
	if path == "" {
		return "", false
	}
	return path, true
}
