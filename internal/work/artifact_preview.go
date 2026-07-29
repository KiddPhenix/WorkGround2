package work

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ── PreviewGrade ──────────────────────────────────────────────────────────

type PreviewGrade string

const (
	PreviewInline   PreviewGrade = "inline"
	PreviewFileCard PreviewGrade = "filecard"
	PreviewFallback PreviewGrade = "fallback"
)

// ── Grade mapping ──────────────────────────────────────────────────────────

var extInline = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".svg": true, ".bmp": true, ".ico": true,
	".txt": true, ".md": true, ".markdown": true, ".mdx": true,
	".log": true, ".csv": true,
	".json": true, ".xml": true, ".yaml": true, ".yml": true, ".toml": true,
	".html": true, ".css": true,
	".pdf": true, // PDF 在应用内通过宿主渲染（WebView2/PDF.js）
}

var extFileCard = map[string]bool{
	".docx": true, ".doc": true,
	".pptx": true, ".ppt": true,
	".xlsx": true, ".xls": true,
}

func GradeArtifact(path string, mimeType string) PreviewGrade {
	ext := strings.ToLower(filepath.Ext(path))
	lower := strings.ToLower(filepath.Base(path))
	for _, d := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst"} {
		if strings.HasSuffix(lower, d) {
			return PreviewFallback
		}
	}
	if extInline[ext] {
		return PreviewInline
	}
	if extFileCard[ext] {
		return PreviewFileCard
	}
	if mimeType != "" {
		if strings.HasPrefix(mimeType, "image/") || strings.HasPrefix(mimeType, "text/") {
			return PreviewInline
		}
		if mimeType == "application/pdf" {
			return PreviewInline
		}
		if strings.Contains(mimeType, "officedocument") {
			return PreviewFileCard
		}
	}
	return PreviewFallback
}

// ── ArtifactPreview DTO ────────────────────────────────────────────────────

type ArtifactPreview struct {
	ArtifactRefID string `json:"artifactId"`
	WorkID        string `json:"workId"`
	ContentDigest string `json:"contentDigest,omitempty"`

	Grade    PreviewGrade `json:"grade"`
	MimeType string       `json:"mimeType,omitempty"`

	// ── Inline ───────────────────────────────────────────────────────
	TextContent string `json:"textContent,omitempty"`
	DataURL     string `json:"dataURL,omitempty"`
	// PDFRaw is populated for PDF artifacts — the host renders via WebView2/PDF.js.
	PDFRaw string `json:"pdfRaw,omitempty"`

	// ── File-card ────────────────────────────────────────────────────
	Summary          string   `json:"summary,omitempty"`
	ThumbnailDataURL string   `json:"thumbnailDataURL,omitempty"`
	PageCount        int      `json:"pageCount,omitempty"`
	SheetNames       []string `json:"sheetNames,omitempty"`
	FileSize         int64    `json:"fileSize,omitempty"`

	// ── Actions ─────────────────────────────────────────────────────
	CanOpen         bool   `json:"canOpen"`
	CanConvert      bool   `json:"canConvert"`
	ConversionState string `json:"conversionState,omitempty"`

	// ── Cache ───────────────────────────────────────────────────────
	CachedAt         time.Time `json:"cachedAt,omitempty" ts_type:"string"`
	ConverterVersion string    `json:"converterVersion,omitempty"`
	Error            string    `json:"error,omitempty"`
}

const (
	ConversionIdle      = ""
	ConversionPending   = "pending"
	ConversionRunning   = "running"
	ConversionCompleted = "completed"
	ConversionFailed    = "failed"
)

// ── Converter interface ────────────────────────────────────────────────────

type ConverterIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Target  string `json:"target"`
}

func (i ConverterIdentity) Valid() bool {
	return strings.TrimSpace(i.Name) != "" &&
		strings.TrimSpace(i.Version) != "" &&
		strings.TrimSpace(i.Target) != ""
}

type Converter interface {
	Identity() ConverterIdentity
	CanConvert(grade PreviewGrade, mimeType string) bool
	Convert(workID string, path string, mimeType string) (*ArtifactPreview, error)
}

// ExternalConverter marks a converter that may transmit source bytes outside
// the local machine. PreviewService never selects one without a verified,
// explicit approval on RequestArtifactConversion.
type ExternalConverter interface {
	Converter
	External() bool
}

// ── Cache key (BlobStore-compatible content digest) ────────────────────────

// previewCacheDigest returns a content digest suitable as a BlobStore key.
// It binds the full authoritative artifact identity, content digest, converter
// name, and converter version. Changing any field produces a cache miss.
func previewCacheDigest(
	workID string,
	definitionRevision int64,
	slotID string,
	slotRevision int64,
	artifactRefID string,
	contentDigest string,
	converter ConverterIdentity,
	allowExternal bool,
) string {
	joined := fmt.Sprintf(
		"preview-cache/v3:%s:%d:%s:%d:%s:%s:%s:%s:%s:%t",
		workID,
		definitionRevision,
		slotID,
		slotRevision,
		artifactRefID,
		contentDigest,
		converter.Name,
		converter.Version,
		converter.Target,
		allowExternal,
	)
	sum := sha256.Sum256([]byte(joined))
	return digestPrefix + fmt.Sprintf("%x", sum[:])
}
