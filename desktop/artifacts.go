package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"workground2/internal/agent"
	"workground2/internal/provider"
)

// ArtifactView is a read-only snapshot of one discovered artifact returned to the
// frontend by ArtifactsForTab.
type ArtifactView struct {
	ArtifactID     string `json:"artifactId"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	SessionID      string `json:"sessionId"`
	Path           string `json:"path"`
	RelativePath   string `json:"relativePath"`
	SourceRunID    string `json:"sourceRunId"`
	LastVerifiedAt int64  `json:"lastVerifiedAt"`
}

var artifactTypeByExt = map[string]string{
	".bat": "script", ".cmd": "script", ".ps1": "script", ".sh": "script",
	".exe": "binary", ".app": "binary", ".appimage": "binary",
	".msi": "package", ".dmg": "package", ".apk": "package", ".deb": "package", ".rpm": "package",
	".zip": "archive", ".tar": "archive", ".gz": "archive", ".tgz": "archive", ".7z": "archive", ".rar": "archive",
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image", ".svg": "image", ".bmp": "image", ".ico": "image",
	".mp4": "video", ".webm": "video", ".mov": "video", ".mkv": "video",
	".mp3": "audio", ".wav": "audio", ".flac": "audio", ".ogg": "audio", ".m4a": "audio",
	".pdf": "document",
}

func classifyArtifact(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if t, ok := artifactTypeByExt[ext]; ok {
		return t
	}
	lower := strings.ToLower(filepath.Base(path))
	for _, d := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst"} {
		if strings.HasSuffix(lower, d) {
			return "archive"
		}
	}
	return "file"
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

func bashCommandArg(argsJSON string) string {
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Command)
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

func toolArgsPath(argsJSON string) string {
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

// parseRequestHelpArtifactPath extracts the absolute image path from a
// successful request_help(image_generation) tool result. It parses the
// structured output: checks the capability line, decodes the artifact JSON,
// and returns the path only when everything is valid.
func parseRequestHelpArtifactPath(argsJSON, output string) (string, bool) {
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

func extractResultPaths(output string) []string {
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
			if rest != "" && !isSourceFile(rest) && looksLikePath(rest) {
				paths = append(paths, rest)
			}
		}
	}
	return paths
}

func looksLikePath(s string) bool {
	if strings.Contains(s, string(filepath.Separator)) || strings.Contains(s, "/") {
		return true
	}
	ext := filepath.Ext(s)
	return ext != "" && len(ext) >= 2 && len(ext) <= 10
}

var producerTools = map[string]bool{
	"write_file": true, "edit_file": true, "multi_edit": true,
	"move_file": true, "create_file": true, "save_file": true, "apply_patch": true,
	"bash": true, "shell": true, "powershell": true, "run_command": true, "complete_step": true,
}

func isProducerTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if producerTools[name] {
		return true
	}
	for _, token := range []string{"image", "render", "export", "download", "generate", "convert", "build", "package"} {
		if strings.Contains(name, token) {
			return true
		}
	}
	return false
}

func (a *App) ArtifactsForTab(tabID string) []ArtifactView {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if tab == nil {
		return []ArtifactView{}
	}
	if ctrl != nil {
		artifacts := extractArtifacts(ctrl.History(), tab.WorkspaceRoot)
		for i := range artifacts {
			artifacts[i].SessionID = tab.ID
			artifacts[i].ArtifactID = artifactID(tab.ID, artifacts[i].Path)
		}
		return artifacts
	}

	// Controller not yet ready — recover artifacts from the persisted session
	// file on disk. This covers the desktop startup window when
	// restoreOrBuildTabs is still constructing controllers asynchronously.
	path := strings.TrimSpace(tab.SessionPath)
	if path == "" {
		return []ArtifactView{}
	}
	sessionDir := tabSessionDir(tab)
	if sessionDir == "" {
		return []ArtifactView{}
	}
	sessionPath, _, err := validateSessionPath(sessionDir, path)
	if err != nil {
		slog.Warn("artifact session restore rejected",
			"tabID", tabID, "sessionPath", path, "sessionDir", sessionDir, "error", err)
		return []ArtifactView{}
	}
	session, err := agent.LoadSession(sessionPath)
	if err != nil {
		slog.Warn("artifact session restore failed",
			"tabID", tabID, "sessionPath", sessionPath, "error", err)
		return []ArtifactView{}
	}
	artifacts := extractArtifacts(session.Snapshot(), tab.WorkspaceRoot)
	for i := range artifacts {
		artifacts[i].SessionID = tab.ID
		artifacts[i].ArtifactID = artifactID(tab.ID, artifacts[i].Path)
	}
	return artifacts
}

func extractArtifacts(msgs []provider.Message, workspaceRoot string) []ArtifactView {
	root, err := filepath.Abs(strings.TrimSpace(workspaceRoot))
	if err != nil || strings.TrimSpace(workspaceRoot) == "" {
		return []ArtifactView{}
	}
	workspaceRoot = root
	type callWithResult struct {
		call provider.ToolCall
		out  string
	}
	callResults := make([]callWithResult, 0)
	results := historyToolResultsByID(msgs)
	for _, m := range msgs {
		if m.Role != provider.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			result, ok := results[tc.ID]
			if tc.ID == "" || !ok || historyToolResultFailed(result.Content) {
				continue
			}
			callResults = append(callResults, callWithResult{call: tc, out: result.Content})
		}
	}

	seen := map[string]bool{}
	var artifacts []ArtifactView
	appendArtifact := func(abs, sourceRunID string, allowExternal bool) {
		abs = filepath.Clean(abs)
		if !filepath.IsAbs(abs) {
			return
		}
		rel, inWorkspace := workspaceRelativeIn(abs, workspaceRoot)
		if !inWorkspace {
			if !allowExternal {
				return
			}
			rel = filepath.Base(abs)
		}
		key := abs
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return
		}
		seen[key] = true

		status := "missing"
		var verifiedAt int64
		if info, err := os.Stat(abs); err == nil {
			status = "available"
			verifiedAt = info.ModTime().UnixMilli()
		}
		artifacts = append(artifacts, ArtifactView{
			ArtifactID:     artifactID(workspaceRoot, abs),
			Name:           filepath.Base(abs),
			Type:           classifyArtifact(abs),
			Status:         status,
			SessionID:      workspaceRoot,
			Path:           abs,
			RelativePath:   filepath.ToSlash(rel),
			SourceRunID:    sourceRunID,
			LastVerifiedAt: verifiedAt,
		})
	}
	for _, cr := range callResults {
		// request_help(image_generation) produces structured artifact lines;
		// re-validate at the desktop boundary and allow workspace-external
		// paths inside allowed output directories.
		if cr.call.Name == "request_help" {
			imgPath, ok := parseRequestHelpArtifactPath(cr.call.Arguments, cr.out)
			if !ok {
				continue
			}
			abs := filepath.Clean(imgPath)
			if !filepath.IsAbs(abs) {
				continue
			}
			if _, _, err := readRequestHelpImage(abs); err != nil {
				continue
			}
			appendArtifact(abs, cr.call.ID, true)
			continue
		}

		if !isProducerTool(cr.call.Name) {
			continue
		}
		var paths []string
		switch cr.call.Name {
		case "write_file", "edit_file", "multi_edit", "move_file", "create_file", "save_file":
			if p := toolArgsPath(cr.call.Arguments); p != "" {
				paths = append(paths, p)
			}
		case "complete_step":
			paths = append(paths, completeStepEvidencePaths(cr.call.Arguments)...)
		case "bash", "shell", "powershell", "run_command":
			paths = append(paths, extractBashOutputPaths(bashCommandArg(cr.call.Arguments))...)
		default:
			if p := toolArgsPath(cr.call.Arguments); p != "" {
				paths = append(paths, p)
			}
		}
		if cr.out != "" {
			paths = append(paths, extractResultPaths(cr.out)...)
		}

		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p == "" || isSourceFile(p) {
				continue
			}
			abs := resolvePath(workspaceRoot, p)
			if abs == "" {
				continue
			}
			appendArtifact(abs, cr.call.ID, false)
		}
	}

	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts
}

func artifactID(sessionID, absPath string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + absPath))
	return fmt.Sprintf("%x", sum[:12])
}

func resolvePath(root, p string) string {
	if isAbsPath(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(root, p))
}

func isAbsPath(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//")
}
