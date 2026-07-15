package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ArtifactCollector is a request-scoped sink for file artifacts a provider
// produces as a side effect of a completion (e.g. Codex CLI writing generated
// images to $CODEX_HOME/generated_images/<thread_id>/). It is concurrency-safe:
// the provider writes from its stream goroutine while the caller reads after
// the stream closes.
//
// The collector is intentionally minimal — it stores absolute file paths only.
// Validation (boundary, MIME, decode) is the caller's responsibility.
type ArtifactCollector struct {
	mu        sync.Mutex
	artifacts []string
}

// AddArtifact records an absolute file path produced during the request.
func (c *ArtifactCollector) AddArtifact(path string) {
	if c == nil || path == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.artifacts = append(c.artifacts, path)
}

// Artifacts returns a copy of the collected file paths.
func (c *ArtifactCollector) Artifacts() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.artifacts...)
}

type artifactCollectorKey struct{}

// WithArtifactCollector attaches a request-scoped ArtifactCollector to ctx.
// Providers that produce side-effect file artifacts (Codex CLI image
// generation) look it up via ArtifactCollectorFrom and report paths.
func WithArtifactCollector(ctx context.Context, c *ArtifactCollector) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, artifactCollectorKey{}, c)
}

// ArtifactCollectorFrom retrieves the collector attached by
// WithArtifactCollector, if any. The ok result is false when no collector is
// wired (e.g. non-CLI providers or non-assist callers).
func ArtifactCollectorFrom(ctx context.Context) (*ArtifactCollector, bool) {
	c, ok := ctx.Value(artifactCollectorKey{}).(*ArtifactCollector)
	return c, ok
}

// CodexGeneratedImagesRoot returns the generated image root used by Codex CLI.
// CODEX_HOME wins; otherwise Codex uses the conventional ~/.codex directory.
func CodexGeneratedImagesRoot() string {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(userHome) == "" {
			return ""
		}
		home = filepath.Join(userHome, ".codex")
	}
	if !filepath.IsAbs(home) {
		abs, err := filepath.Abs(home)
		if err != nil {
			return ""
		}
		home = abs
	}
	return filepath.Join(home, "generated_images")
}
