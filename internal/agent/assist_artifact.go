package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"workground2/internal/config"
	"workground2/internal/provider"
	"workground2/pkg/drawaddon"
)

type imageArtifact struct {
	TaskID string `json:"task_id"`
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
}

type drawTaskResult struct {
	TaskID     string `json:"taskId"`
	ProviderID string `json:"providerId"`
	Status     string `json:"status"`
	OutputPath string `json:"outputPath"`
	Error      string `json:"error"`
}

func validateImageArtifact(run *SubagentRun) (imageArtifact, error) {
	if run == nil || run.Session == nil {
		return imageArtifact{}, fmt.Errorf("image helper returned no session")
	}
	result, err := lastDrawResult(run.Session.Snapshot())
	if err != nil {
		return imageArtifact{}, err
	}
	if result.Status != drawaddon.TaskSucceeded {
		return imageArtifact{}, fmt.Errorf("draw_image task %q status is %q: %s", result.TaskID, result.Status, result.Error)
	}
	path := filepath.Clean(strings.TrimSpace(result.OutputPath))
	if path == "." || !filepath.IsAbs(path) {
		return imageArtifact{}, fmt.Errorf("draw_image returned a non-absolute output path %q", result.OutputPath)
	}
	root, err := drawOutputRoot(result.ProviderID)
	if err != nil {
		return imageArtifact{}, err
	}
	if !pathWithin(path, root) {
		return imageArtifact{}, fmt.Errorf("draw_image output %q is outside configured output directory %q", path, root)
	}

	file, err := os.Open(path)
	if err != nil {
		return imageArtifact{}, fmt.Errorf("open generated image %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return imageArtifact{}, fmt.Errorf("stat generated image %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return imageArtifact{}, fmt.Errorf("generated image %q is not a non-empty regular file", path)
	}
	header := make([]byte, 512)
	n, err := file.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return imageArtifact{}, fmt.Errorf("read generated image %q: %w", path, err)
	}
	mime := http.DetectContentType(header[:n])
	if !strings.HasPrefix(mime, "image/") {
		return imageArtifact{}, fmt.Errorf("generated file %q has non-image MIME %q", path, mime)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return imageArtifact{}, fmt.Errorf("seek generated image %q: %w", path, err)
	}
	if _, _, err := image.DecodeConfig(file); err != nil {
		return imageArtifact{}, fmt.Errorf("decode generated image %q: %w", path, err)
	}
	return imageArtifact{TaskID: result.TaskID, Path: path, MIME: mime, Size: info.Size()}, nil
}

func lastDrawResult(messages []provider.Message) (drawTaskResult, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role != provider.RoleTool || !isDrawTool(message.Name) {
			continue
		}
		var result drawTaskResult
		if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
			return drawTaskResult{}, fmt.Errorf("decode draw_image result: %w", err)
		}
		return result, nil
	}
	return drawTaskResult{}, fmt.Errorf("image helper returned no draw_image tool result")
}

func isDrawTool(name string) bool {
	name = strings.TrimSpace(name)
	return name == "draw_image" || strings.HasSuffix(name, "__draw_image")
}

func drawOutputRoot(providerID string) (string, error) {
	home := config.WorkGround2HomeDir()
	providers, err := drawaddon.New(home).Providers()
	if err != nil {
		return "", fmt.Errorf("load draw_image provider %q: %w", providerID, err)
	}
	for _, candidate := range providers {
		if candidate.ID != providerID || !candidate.Enabled {
			continue
		}
		root := filepath.Clean(strings.TrimSpace(candidate.OutputDir))
		if root == "." || root == "" {
			return "", fmt.Errorf("draw_image provider %q has no output directory", providerID)
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(home, "addons", "draw-tool", "outputs", candidate.ID, root)
		}
		return filepath.Clean(root), nil
	}
	return "", fmt.Errorf("draw_image provider %q is missing or disabled", providerID)
}

func pathWithin(path, root string) bool {
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
