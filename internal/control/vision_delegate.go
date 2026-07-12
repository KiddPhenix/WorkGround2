package control

import (
	"context"
	"log/slog"
	"strings"

	"workground2/internal/provider"
)

// delegateImages sends images to the vision delegate provider and returns a text
// description suitable for injection into the user message. Returns empty string
// if the delegate fails or produces no output.
func (c *Controller) delegateImages(ctx context.Context, images []string, userPrompt string) string {
	if c.visionDelegate == nil || len(images) == 0 {
		return ""
	}

	slog.Info("vision delegate: sending images for analysis", "count", len(images), "promptLen", len(userPrompt))

	prompt := buildVisionDelegatePrompt(userPrompt)

	req := provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: prompt, Images: images},
		},
	}

	ch, err := c.visionDelegate.Stream(ctx, req)
	if err != nil {
		slog.Warn("vision delegate: stream failed", "err", err)
		return ""
	}

	var buf strings.Builder
	for chunk := range ch {
		if chunk.Type == provider.ChunkText {
			buf.WriteString(chunk.Text)
		}
		if chunk.Err != nil && buf.Len() == 0 {
			slog.Warn("vision delegate: chunk error", "err", chunk.Err)
			return ""
		}
	}
	result := strings.TrimSpace(buf.String())
	if result == "" {
		slog.Warn("vision delegate: empty response")
	} else {
		slog.Info("vision delegate: got description", "len", len(result))
	}
	return result
}

// buildVisionDelegatePrompt constructs the prompt sent to the vision delegate.
// It asks for a thorough technical description that the main (non-vision) model
// can use as a substitute for seeing the image directly.
func buildVisionDelegatePrompt(userPrompt string) string {
	if strings.TrimSpace(userPrompt) == "" {
		return "Please describe the attached image in detail. Focus on any text, code, diagrams, UI layouts, data visualizations, charts, or technical content visible. Provide a thorough, precise description that another AI can use as a substitute for seeing the image directly."
	}
	return "The user sent the following request along with this image:\n\n" + userPrompt + "\n\nPlease describe the attached image in complete detail. Focus on any text, code, diagrams, UI layouts, data visualizations, charts, or technical content visible. Provide a thorough, precise description that another AI assistant can use to understand what's in the image and respond to the user's request."
}
