package work

import (
	"net/url"
	"strings"
)

// IsURLArtifactKind reports whether an ArtifactSlot Kind is satisfied by
// absolute http(s) links collected from the task final response instead of by
// files or blobs. URL results are never materialized into fake files.
func IsURLArtifactKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "url", "link":
		return true
	default:
		return false
	}
}

// ValidateArtifactURL reports whether raw is an absolute http/https URL with a
// host. Unsafe schemes (javascript:, data:, file:, ...), relative references,
// and host-less URLs are rejected. It is the single authoritative gate for
// ArtifactRef.URL writes and browser opens.
func ValidateArtifactURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !parsed.IsAbs() {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	return strings.TrimSpace(parsed.Host) != ""
}
