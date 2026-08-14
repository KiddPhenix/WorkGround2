package vocabulary

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"workground2/internal/fileutil"
	"workground2/internal/frontmatter"
)

const (
	generatedBegin = "# WORKGROUND2:BEGIN GENERATED VOCABULARY"
	generatedEnd   = "# WORKGROUND2:END GENERATED VOCABULARY"
	maxScanFile    = 1 << 20
	maxScanFiles   = 10000
	maxScanTerms   = 2000
	maxCandidates  = 10000
	maxScanWarning = 20
)

var errScanLimit = errors.New("workspace vocabulary scan limit reached")

var (
	scanDateLike = regexp.MustCompile(`^\d{4}[-_.]\d{1,2}[-_.]\d{1,2}$`)
	scanHexLike  = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
	scanVersion  = regexp.MustCompile(`^v?\d+(?:\.\d+){1,3}(?:[-+][0-9A-Za-z.-]+)?$`)
)

// ScanResult describes one deterministic project vocabulary rebuild.
type ScanResult struct {
	Path      string
	Scanned   int
	Generated int
	Updated   bool
	Warnings  []string
}

// RebuildProject scans bounded text files under root and atomically replaces
// only the generated section of .WorkGround2/vocabulary.toml. Hand-authored
// content and comments outside that section remain byte-for-byte intact.
func RebuildProject(root string) (ScanResult, error) {
	root = cleanAbs(root)
	result := ScanResult{Path: filepath.Join(root, ".WorkGround2", ProjectFile), Warnings: []string{}}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return result, fmt.Errorf("scan workspace %s: %w", root, err)
	}

	original, err := os.ReadFile(result.Path)
	if err != nil && !os.IsNotExist(err) {
		return result, fmt.Errorf("read project vocabulary %s: %w", result.Path, err)
	}
	base, err := stripGeneratedSection(string(original))
	if err != nil {
		return result, fmt.Errorf("parse generated vocabulary section: %w", err)
	}
	if strings.TrimSpace(base) == "" {
		base = "version = 1\n"
	}
	var authored projectFile
	if _, err := toml.Decode(base, &authored); err != nil {
		return result, fmt.Errorf("parse authored vocabulary %s: %w", result.Path, err)
	}
	manual := map[string]bool{}
	for i, entry := range authored.Terms {
		if !validTerm(strings.TrimSpace(entry.Text)) {
			return result, fmt.Errorf("authored terms[%d].text is invalid", i)
		}
		manual[keyOf(entry.Text)] = true
	}

	generated := map[string]Entry{}
	addWarning := func(message string) {
		if len(result.Warnings) < maxScanWarning {
			result.Warnings = append(result.Warnings, message)
		}
	}
	candidatesFull := false
	add := func(entry Entry) {
		entry = normalize(entry)
		key := keyOf(entry.Text)
		if entry.Text == "" || manual[key] || !scannableTerm(entry.Text) {
			return
		}
		entry.Sources = nil
		entry.Evidence = 0
		entry.UseCount = 0
		if current, ok := generated[key]; ok {
			generated[key] = mergeEntry(current, entry)
		} else if len(generated) < maxCandidates {
			generated[key] = entry
		} else {
			candidatesFull = true
		}
	}

	walkErr := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			addWarning(fmt.Sprintf("scan %s: %v", path, walkErr))
			if item != nil && item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if item.IsDir() {
			if rel != "." && skipScanDir(rel, item.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 || samePath(path, result.Path) || !scanTextFile(item.Name()) {
			return nil
		}
		fileInfo, err := item.Info()
		if err != nil {
			addWarning(fmt.Sprintf("stat %s: %v", rel, err))
			return nil
		}
		if !fileInfo.Mode().IsRegular() || fileInfo.Size() > maxScanFile {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			addWarning(fmt.Sprintf("read %s: %v", rel, err))
			return nil
		}
		if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
			return nil
		}
		result.Scanned++
		if result.Scanned > maxScanFiles {
			return errScanLimit
		}
		entries, warnings := scanFileEntries(path, string(body))
		for _, warning := range warnings {
			addWarning(warning)
		}
		for _, entry := range entries {
			add(entry)
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errScanLimit) {
		return result, fmt.Errorf("scan workspace %s: %w", root, walkErr)
	}
	if errors.Is(walkErr, errScanLimit) {
		addWarning(fmt.Sprintf("scan stopped at %d files", maxScanFiles))
	}
	if candidatesFull {
		addWarning(fmt.Sprintf("candidate vocabulary capped at %d unique terms", maxCandidates))
	}

	terms := make([]Entry, 0, len(generated))
	for _, entry := range generated {
		terms = append(terms, entry)
	}
	sort.Slice(terms, func(i, j int) bool { return strings.ToLower(terms[i].Text) < strings.ToLower(terms[j].Text) })
	if len(terms) > maxScanTerms {
		terms = terms[:maxScanTerms]
		addWarning(fmt.Sprintf("generated vocabulary capped at %d terms", maxScanTerms))
	}
	result.Generated = len(terms)
	section, err := encodeGeneratedSection(terms)
	if err != nil {
		return result, err
	}
	next := appendGeneratedSection(base, section)
	if bytes.Equal(original, []byte(next)) {
		return result, nil
	}
	perm := os.FileMode(0o644)
	if current, err := os.Stat(result.Path); err == nil {
		perm = current.Mode().Perm()
	}
	current, err := os.ReadFile(result.Path)
	if err != nil && !os.IsNotExist(err) {
		return result, fmt.Errorf("recheck project vocabulary %s: %w", result.Path, err)
	}
	if !bytes.Equal(current, original) {
		if bytes.Equal(current, []byte(next)) {
			return result, nil
		}
		return result, fmt.Errorf("project vocabulary changed during scan; retry /rebuild_vocabulary")
	}
	if err := fileutil.AtomicWriteFile(result.Path, []byte(next), perm); err != nil {
		return result, fmt.Errorf("write project vocabulary %s: %w", result.Path, err)
	}
	result.Updated = true
	return result, nil
}

func appendGeneratedSection(base, section string) string {
	separator := "\n\n"
	if strings.HasSuffix(base, "\n\n") || strings.HasSuffix(base, "\r\n\r\n") {
		separator = ""
	} else if strings.HasSuffix(base, "\n") {
		separator = "\n"
	}
	return base + separator + generatedBegin + "\n" + section + generatedEnd + "\n"
}

func scanFileEntries(path, body string) ([]Entry, []string) {
	base := strings.ToLower(filepath.Base(path))
	var entries []Entry
	var warnings []string
	switch {
	case strings.HasPrefix(base, "agents") && strings.HasSuffix(base, ".md"):
		entries = append(entries, entriesFromAgent(AgentSource{Name: filepath.Base(path), Path: path, Body: body})...)
	case base == "skill.md":
		fm, _ := frontmatter.Split(body)
		source := Source{Kind: "workspace-scan", Name: filepath.Base(filepath.Dir(path)), Path: path}
		for _, term := range splitTerms(fm["vocabulary"]) {
			if entry, ok := simpleEntry(term, source); ok {
				entries = append(entries, entry)
			}
		}
	case base == strings.ToLower(SidecarFile):
		loaded, err := loadProject(path, Source{Kind: "workspace-scan", Name: filepath.Base(filepath.Dir(path)), Path: path})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("parse %s: %v", path, err))
		} else {
			entries = append(entries, loaded...)
		}
	}
	entries = append(entries, Extract(body)...)
	return preferSpecificEntries(entries), warnings
}

// Extract intentionally errs toward recall. During a whole-workspace scan,
// suppress a later wrapper token such as "项目节点叫做多模态生视频V5" when an
// earlier explicit parser already found the more specific declared term.
func preferSpecificEntries(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		wrapped := false
		for _, current := range out {
			if !strings.EqualFold(entry.Text, current.Text) && strings.Contains(strings.ToLower(entry.Text), strings.ToLower(current.Text)) {
				wrapped = true
				break
			}
		}
		if !wrapped {
			out = append(out, entry)
		}
	}
	return out
}

func encodeGeneratedSection(terms []Entry) (string, error) {
	var body bytes.Buffer
	if err := toml.NewEncoder(&body).Encode(struct {
		Terms []Entry `toml:"terms"`
	}{Terms: terms}); err != nil {
		return "", fmt.Errorf("encode generated vocabulary: %w", err)
	}
	return body.String(), nil
}

func stripGeneratedSection(body string) (string, error) {
	start := strings.Index(body, generatedBegin)
	end := strings.Index(body, generatedEnd)
	if start < 0 && end < 0 {
		return body, nil
	}
	if start < 0 || end < start {
		return "", fmt.Errorf("generated section markers are incomplete")
	}
	end += len(generatedEnd)
	if next := strings.Index(body[end:], generatedBegin); next >= 0 {
		return "", fmt.Errorf("multiple generated sections found")
	}
	for end < len(body) && (body[end] == '\r' || body[end] == '\n') {
		end++
	}
	return body[:start] + body[end:], nil
}

func skipScanDir(rel, name string) bool {
	lower := strings.ToLower(name)
	for _, ignored := range []string{".git", ".svn", ".hg", "node_modules", "vendor", "dist", "build", "bin", "obj", "library", "temp", "logs", "coverage", ".next", ".nuxt", ".turbo", ".cache"} {
		if lower == ignored {
			return true
		}
	}
	rel = strings.ToLower(filepath.ToSlash(rel))
	return strings.HasPrefix(rel, ".workground2/attachments") || strings.HasPrefix(rel, ".workground2/autoresearch") || strings.HasPrefix(rel, ".workground2/sessions")
}

func scanTextFile(name string) bool {
	lower := strings.ToLower(name)
	if (strings.HasPrefix(lower, "agents") && strings.HasSuffix(lower, ".md")) || lower == "skill.md" || lower == strings.ToLower(SidecarFile) {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".txt", ".go", ".ts", ".tsx", ".js", ".jsx", ".json", ".toml", ".yaml", ".yml", ".cs", ".py", ".rs", ".java", ".kt", ".swift", ".c", ".cc", ".cpp", ".h", ".hpp", ".uxml", ".uss", ".html", ".css", ".sh", ".ps1", ".sql", ".proto", ".xml", ".astro", ".vue", ".svelte":
		return true
	default:
		return false
	}
}

func scannableTerm(term string) bool {
	lower := strings.ToLower(strings.TrimSpace(term))
	if scanDateLike.MatchString(lower) || scanHexLike.MatchString(lower) || scanVersion.MatchString(lower) {
		return false
	}
	return !strings.HasPrefix(lower, "developping/") && !strings.HasPrefix(lower, "codex/")
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
