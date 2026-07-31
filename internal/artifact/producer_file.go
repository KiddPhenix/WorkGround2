package artifact

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"workground2/internal/provider"
)

// FileProducer discovers artifacts from file-producing tool calls.
// It extracts output paths from tool arguments (path, destination_path, etc.)
// and from result text patterns ("wrote ... to ...", "created ...").
//
// Discovered artifacts carry only Path (no Data) because the file content
// lives on disk — callers that need bytes must read separately.
type FileProducer struct{}

var fileProducerToolNames = map[string]bool{
	"write_file": true, "edit_file": true, "multi_edit": true,
	"move_file": true, "create_file": true, "save_file": true, "apply_patch": true,
	"bash": true, "shell": true, "powershell": true, "run_command": true,
	"complete_step": true,
}

func (p *FileProducer) Discover(call provider.ToolCall, result provider.Message) []Discovered {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	if !fileProducerToolNames[name] && !isProducerHeuristic(name) {
		return nil
	}

	var paths []string
	switch name {
	case "write_file", "edit_file", "multi_edit", "move_file", "create_file", "save_file":
		if p := fileToolArgsPath(call.Arguments); p != "" {
			paths = append(paths, p)
		}
	case "complete_step":
		paths = append(paths, completeStepEvidencePaths(call.Arguments)...)
	case "bash", "shell", "powershell", "run_command":
		paths = append(paths, extractBashOutputPaths(bashCommandArg(call.Arguments))...)
	default:
		if p := fileToolArgsPath(call.Arguments); p != "" {
			paths = append(paths, p)
		}
	}
	if result.Content != "" {
		paths = append(paths, extractResultPaths(result.Content)...)
	}

	var out []Discovered
	seen := make(map[string]bool)
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || isSourceFile(p) {
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, Discovered{
			Name:        filepath.Base(p),
			Path:        filepath.Clean(p),
			SourceRunID: call.ID,
		})
	}
	return out
}

// ── helpers ────────────────────────────────────────────────────────────────

func isProducerHeuristic(name string) bool {
	for _, token := range []string{"image", "render", "export", "download", "generate", "convert", "build", "package"} {
		if strings.Contains(name, token) {
			return true
		}
	}
	return false
}

func fileToolArgsPath(argsJSON string) string {
	var p struct {
		Path            string `json:"path"`
		DestinationPath string `json:"destination_path"`
		Destination     string `json:"destination"`
		OutputPath      string `json:"output_path"`
		OutputFile      string `json:"output_file"`
		SavePath        string `json:"save_path"`
		Target          string `json:"target"`
		File            string `json:"file"`
		Filename        string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return ""
	}
	if p.Path != "" {
		return p.Path
	}
	for _, candidate := range []string{p.DestinationPath, p.Destination, p.OutputPath, p.OutputFile, p.SavePath, p.Target, p.File, p.Filename} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func bashCommandArg(argsJSON string) string {
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Command)
}

func extractBashOutputPaths(cmd string) []string {
	var paths []string
	fields := shellFields(cmd)
	for i, f := range fields {
		if (f == "-o" || f == "--output" || f == "/out:" || f == "/Fe:" || f == "-out") && i+1 < len(fields) {
			paths = append(paths, fields[i+1])
		}
		if after, ok := strings.CutPrefix(f, "-o"); ok && after != "" && f != "-out" {
			paths = append(paths, after)
		}
		if after, ok := strings.CutPrefix(f, "--output="); ok && after != "" {
			paths = append(paths, after)
		}
		if after, ok := strings.CutPrefix(f, "/out:"); ok && after != "" {
			paths = append(paths, after)
		}
		if after, ok := strings.CutPrefix(f, "/Fe:"); ok && after != "" {
			paths = append(paths, after)
		}
	}
	return paths
}

func shellFields(s string) []string {
	var fields []string
	var current strings.Builder
	inSingle, inDouble := false, false
	for _, r := range s {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case r == ' ' || r == '\t':
			if inSingle || inDouble {
				current.WriteRune(r)
			} else if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	return fields
}

func completeStepEvidencePaths(argsJSON string) []string {
	var p struct {
		Evidence []struct {
			Kind  string   `json:"kind"`
			Paths []string `json:"paths"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return nil
	}
	var paths []string
	for _, e := range p.Evidence {
		if e.Kind == "files" || e.Kind == "diff" {
			paths = append(paths, e.Paths...)
		}
	}
	return paths
}

func extractResultPaths(output string) []string {
	output = stripANSI(output)
	paths := extractVerbPaths(output)
	addNew := func(candidates []string) {
		seen := make(map[string]bool, len(paths))
		for _, p := range paths {
			seen[p] = true
		}
		for _, p := range candidates {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	addNew(extractRawFilePaths(output))
	addNew(extractRelativeArtifactPaths(output))
	return paths
}

func extractVerbPaths(output string) []string {
	var paths []string
	patterns := []string{
		"wrote ", "Wrote ",
		"created ", "Created ",
		"saved ", "Saved ",
		"moved ", "Moved ",
		"compiled ", "Compiled ",
		"built ", "Built ",
		"generated ", "Generated ",
	}
	for _, line := range strings.Split(output, "\n") {
		for _, pat := range patterns {
			idx := strings.Index(line, pat)
			if idx < 0 {
				continue
			}
			rest := line[idx+len(pat):]
			if pat == "wrote " || pat == "Wrote " {
				if toIdx := strings.LastIndex(rest, " to "); toIdx >= 0 {
					rest = rest[toIdx+4:]
				}
			}
			if pat == "moved " || pat == "Moved " {
				if toIdx := strings.LastIndex(rest, " to "); toIdx >= 0 {
					rest = rest[toIdx+4:]
				}
			}
			rest = strings.TrimSpace(rest)
			if rest == "" {
				continue
			}
			if rest[0] == '"' || rest[0] == '\'' {
				quote := rest[0]
				if end := strings.IndexByte(rest[1:], quote); end >= 0 {
					rest = rest[1 : end+1]
				}
			} else if spaceIdx := strings.IndexAny(rest, " \t\r\n"); spaceIdx > 0 {
				rest = rest[:spaceIdx]
			}
			rest = strings.TrimSpace(rest)
			rest = strings.TrimRight(rest, ".,;:!?\"'")
			if rest == "" || isSourceFile(rest) || !looksLikePath(rest) {
				continue
			}
			// Bare filenames (no path separator) must carry a recognised
			// artifact extension so that version stamps, dates, TLDs and
			// unknown extensions are not mistaken for artifact paths.
			hasSep := strings.Contains(rest, string(filepath.Separator)) || strings.Contains(rest, "/")
			if !hasSep && !isArtifactExtension(filepath.Ext(rest)) {
				continue
			}
			paths = append(paths, rest)
		}
	}
	return paths
}

// extractRawFilePaths scans output for standalone absolute file paths
// that are not preceded by an English verb prefix. This catches paths
// produced by scripts/shells whose output may be in any language or encoding.
func extractRawFilePaths(output string) []string {
	var paths []string
	for _, token := range strings.Fields(output) {
		token = strings.Trim(token, "\"',;:!?()[]{}<>")
		if token == "" {
			continue
		}
		if !looksLikeAbsolutePath(token) {
			continue
		}
		if isSourceFile(token) {
			continue
		}
		if !looksLikePath(token) {
			continue
		}
		paths = append(paths, token)
	}
	return paths
}

// stripANSI removes ANSI escape sequences (e.g. "\x1b[32;1m") from s.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == ';') {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// extractRelativeArtifactPaths scans output line-by-line with quote-aware
// tokenization (shellFields) for relative paths whose extension is in the
// artifact extension whitelist. Absolute paths, source files, URLs, and
// tokens without a recognised artifact extension are skipped.
func extractRelativeArtifactPaths(output string) []string {
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		for _, token := range shellFields(line) {
			token = strings.Trim(token, "\"',;:!?()[]{}<>")
			if token == "" {
				continue
			}
			if looksLikeAbsolutePath(token) {
				continue
			}
			if !looksLikePath(token) {
				continue
			}
			if isSourceFile(token) {
				continue
			}
			if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
				continue
			}
			if !isArtifactExtension(filepath.Ext(token)) {
				continue
			}
			paths = append(paths, token)
		}
	}
	return paths
}

// isArtifactExtension reports whether ext is a recognised artifact file
// extension (document, image, video, audio, archive, executable, font,
// or binary). The whitelist is intentionally closed; unknown or numeric
// extensions are rejected so that prose tokens (v1.2.3, release.2026,
// example.io) are never mistaken for artifact paths.
func isArtifactExtension(ext string) bool {
	switch strings.ToLower(ext) {
	// ── documents ──
	case ".docx", ".xlsx", ".xls", ".pptx", ".pdf":
		return true
	// ── images ──
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".svg", ".webp",
		".ico", ".tiff", ".tif":
		return true
	// ── video ──
	case ".mp4", ".webm", ".mov", ".avi", ".mkv":
		return true
	// ── audio ──
	case ".mp3", ".wav", ".ogg", ".flac", ".aac", ".wma":
		return true
	// ── archives ──
	case ".zip", ".tar", ".gz", ".7z", ".rar":
		return true
	// ── executables / libraries ──
	case ".exe", ".dll", ".so", ".dylib":
		return true
	// ── scripts (SlotKind-visible) ──
	case ".sh", ".bat", ".cmd", ".ps1":
		return true
	// ── fonts ──
	case ".ttf", ".otf", ".woff", ".woff2":
		return true
	// ── disk / database / binary ──
	case ".bin", ".dat", ".iso", ".img", ".db", ".sqlite":
		return true
	}
	return false
}

// looksLikeAbsolutePath reports whether s starts with a Windows drive letter
// followed by :\ or :/, or is a Unix absolute path that contains at least
// one additional path separator (heuristic to exclude bare top-level dirs).
func looksLikeAbsolutePath(s string) bool {
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') &&
		((s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z')) {
		return true
	}
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") {
		return strings.ContainsRune(s[1:], '/')
	}
	return false
}

func looksLikePath(s string) bool {
	if strings.Contains(s, string(filepath.Separator)) || strings.Contains(s, "/") {
		return true
	}
	ext := filepath.Ext(s)
	return ext != "" && len(ext) >= 2 && len(ext) <= 10
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case "", ".go", ".ts", ".tsx", ".js", ".jsx", ".json", ".yaml", ".yml",
		".toml", ".xml", ".html", ".css", ".scss", ".less",
		".md", ".mdx", ".txt", ".csv", ".log",
		".py", ".rb", ".java", ".c", ".cpp", ".h", ".hpp",
		".rs", ".swift", ".kt", ".scala", ".clj", ".cljs",
		".cs", ".fs", ".vb",
		".mod", ".sum", ".lock",
		".gitignore", ".dockerignore", ".editorconfig",
		".env", ".ini", ".cfg", ".conf",
		".proto", ".graphql",
		".test", ".spec", ".snap":
		return true
	}
	return false
}
