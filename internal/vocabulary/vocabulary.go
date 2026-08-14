// Package vocabulary maintains workspace-specific terms used for low-latency
// composer completion and small, transient model context. It merges explicit
// project entries, terms contributed by skills and agent instruction files, and
// deterministic terms learned from completed conversation turns.
package vocabulary

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"workground2/internal/fileutil"
	"workground2/internal/frontmatter"
)

const (
	ProjectFile = "vocabulary.toml"
	SidecarFile = "VOCABULARY.toml"
	stateFile   = "learned.json"
	lockDir     = ".write-lock"
	maxReceipts = 4096
)

// Kind classifies how a term is normally used.
type Kind string

const (
	KindNoun   Kind = "noun"
	KindVerb   Kind = "verb"
	KindPhrase Kind = "phrase"
)

// Source identifies one contributor to a merged term.
type Source struct {
	Kind string `json:"kind" toml:"kind"`
	Name string `json:"name,omitempty" toml:"name,omitempty"`
	Path string `json:"path,omitempty" toml:"path,omitempty"`
}

// Entry is one canonical completion term. Learned fields are retained when an
// explicit source later overrides its metadata.
type Entry struct {
	ID          string    `json:"id" toml:"-"`
	Text        string    `json:"text" toml:"text"`
	Kind        Kind      `json:"kind,omitempty" toml:"kind,omitempty"`
	Aliases     []string  `json:"aliases,omitempty" toml:"aliases,omitempty"`
	Description string    `json:"description,omitempty" toml:"description,omitempty"`
	Preferred   bool      `json:"preferred,omitempty" toml:"preferred,omitempty"`
	Sources     []Source  `json:"sources,omitempty" toml:"-"`
	Evidence    int       `json:"evidence,omitempty" toml:"-"`
	UseCount    int       `json:"useCount,omitempty" toml:"-"`
	LastSeenAt  time.Time `json:"lastSeenAt,omitempty" toml:"-"`
}

// Match is the stable, frontend-facing completion shape.
type Match struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	Suffix      string `json:"suffix"`
	Kind        Kind   `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
}

// SkillSource describes vocabulary carried by one loaded skill.
type SkillSource struct {
	Name  string
	Path  string
	Terms []string
}

// AgentSource describes an AGENTS.md-compatible instruction source.
type AgentSource struct {
	Name string
	Path string
	Body string
}

// Options configure one workspace service.
type Options struct {
	WorkspaceRoot string
	StateDir      string
	Skills        []SkillSource
	Agents        []AgentSource
}

// RefreshResult describes a Session-local Skill activation or full rebuild.
type RefreshResult struct {
	Skill     string   `json:"skill,omitempty"`
	TermCount int      `json:"termCount"`
	Added     int      `json:"added,omitempty"`
	Scanned   int      `json:"scanned,omitempty"`
	Path      string   `json:"path,omitempty"`
	Updated   bool     `json:"updated,omitempty"`
	Warnings  []string `json:"warnings"`
}

type learnedState struct {
	Version   int               `json:"version"`
	Terms     map[string]*Entry `json:"terms"`
	Seen      map[string]bool   `json:"seen"`
	SeenOrder []string          `json:"seenOrder,omitempty"`
	Used      map[string]bool   `json:"used,omitempty"`
	UsedOrder []string          `json:"usedOrder,omitempty"`
}

type projectFile struct {
	Version int     `toml:"version"`
	Terms   []Entry `toml:"terms"`
}

// Service owns a recoverable workspace vocabulary snapshot.
type Service struct {
	mu          sync.RWMutex
	root        string
	stateDir    string
	stateStamp  string
	nextRefresh time.Time
	learned     learnedState
	entries     []Entry
	static      []Entry
	warnings    []string
}

// New loads all sources. Malformed optional sources are skipped individually;
// a malformed learned store is preserved on disk and starts with an empty state.
func New(opts Options) *Service {
	s := &Service{root: cleanAbs(opts.WorkspaceRoot), stateDir: strings.TrimSpace(opts.StateDir)}
	s.learned = emptyLearned()
	s.loadLearned()
	s.static = append(s.static, s.loadFile(filepath.Join(s.root, ".WorkGround2", ProjectFile), Source{Kind: "workspace", Name: "workspace"})...)
	for _, sk := range opts.Skills {
		s.appendSkillLocked(sk)
	}
	for _, doc := range opts.Agents {
		s.appendAgentLocked(doc)
	}
	s.rebuildLocked()
	return s
}

// Warnings reports malformed/unreadable optional sources encountered at load.
// The service remains usable from every valid source.
func (s *Service) Warnings() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.warnings...)
}

// ActivateSkill replaces one Skill's authored entries in the current Session.
// Repeating the activation is safe and also picks up edits made since the last
// activation; it does not persist the activation into other Sessions.
func (s *Service) ActivateSkill(sk SkillSource) RefreshResult {
	if s == nil || strings.TrimSpace(sk.Name) == "" {
		return RefreshResult{Warnings: []string{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	beforeWarnings := len(s.warnings)
	kept := s.static[:0]
	for _, entry := range s.static {
		if !entryFromSkill(entry, sk.Name) {
			kept = append(kept, entry)
		}
	}
	s.static = kept
	beforeEntries := len(s.static)
	s.appendSkillLocked(sk)
	s.rebuildLocked()
	return RefreshResult{
		Skill:     sk.Name,
		TermCount: len(s.entries),
		Added:     len(s.static) - beforeEntries,
		Warnings:  append([]string(nil), s.warnings[beforeWarnings:]...),
	}
}

// RebuildWorkspace scans project files, atomically refreshes the generated
// section of .WorkGround2/vocabulary.toml, then reloads that source into this
// Session. Skill and Agent entries keep their existing Session scope.
func (s *Service) RebuildWorkspace() (RefreshResult, error) {
	if s == nil || s.root == "" {
		return RefreshResult{Warnings: []string{}}, fmt.Errorf("vocabulary: workspace root is required")
	}
	scan, err := RebuildProject(s.root)
	if err != nil {
		return RefreshResult{Warnings: []string{}}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	beforeWarnings := len(s.warnings)
	kept := s.static[:0]
	for _, entry := range s.static {
		if !entryFromSource(entry, "workspace", "workspace") {
			kept = append(kept, entry)
		}
	}
	s.static = kept
	s.static = append(s.static, s.loadFile(scan.Path, Source{Kind: "workspace", Name: "workspace"})...)
	s.rebuildLocked()
	warnings := append([]string(nil), scan.Warnings...)
	warnings = append(warnings, s.warnings[beforeWarnings:]...)
	return RefreshResult{
		TermCount: len(s.entries),
		Added:     scan.Generated,
		Scanned:   scan.Scanned,
		Path:      scan.Path,
		Updated:   scan.Updated,
		Warnings:  warnings,
	}, nil
}

// Complete returns stable prefix matches. Prefix matching is case-insensitive
// for Latin text and exact for CJK; aliases match but always insert Text.
func (s *Service) Complete(prefix string, limit int) []Match {
	prefix = strings.TrimSpace(prefix)
	if s == nil || prefix == "" || utf8.RuneCountInString(prefix) < minPrefix(prefix) {
		return []Match{}
	}
	s.refreshLearned()
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	type ranked struct {
		entry Entry
		name  string
		score int
	}
	var found []ranked
	needle := strings.ToLower(prefix)
	for _, entry := range s.entries {
		matchName := ""
		for _, name := range append([]string{entry.Text}, entry.Aliases...) {
			if len([]rune(name)) <= len([]rune(prefix)) || !strings.HasPrefix(strings.ToLower(name), needle) {
				continue
			}
			matchName = name
			break
		}
		if matchName == "" {
			continue
		}
		score := entry.UseCount*20 + entry.Evidence*4
		if entry.Preferred {
			score += 1000
		}
		score += sourceRank(entry.Sources)
		found = append(found, ranked{entry: entry, name: matchName, score: score})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		li, lj := utf8.RuneCountInString(found[i].entry.Text), utf8.RuneCountInString(found[j].entry.Text)
		if li != lj {
			return li < lj
		}
		return found[i].entry.Text < found[j].entry.Text
	})
	out := make([]Match, 0, min(limit, len(found)))
	for _, item := range found {
		if len(out) == limit {
			break
		}
		suffix := runeSuffix(item.entry.Text, prefix)
		if item.name != item.entry.Text {
			// Alias completion expands to the canonical term instead of attempting
			// to splice an unrelated alias suffix.
			suffix = " → " + item.entry.Text
		}
		out = append(out, Match{ID: item.entry.ID, Text: item.entry.Text, Suffix: suffix, Kind: item.entry.Kind, Description: item.entry.Description, Source: primarySource(item.entry.Sources)})
	}
	return out
}

// Observe extracts deterministic high-signal terms and persists them once for
// eventID. Replaying the same event is a no-op.
func (s *Service) Observe(eventID, text, source string) error {
	if s == nil || strings.TrimSpace(eventID) == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withStoreLockLocked(func() error {
		if s.learned.Seen[eventID] {
			return nil
		}
		before := cloneLearned(s.learned)
		now := time.Now().UTC()
		for _, found := range Extract(text) {
			key := keyOf(found.Text)
			entry := s.learned.Terms[key]
			if entry == nil {
				copy := found
				copy.ID = idOf(copy.Text)
				copy.Sources = []Source{{Kind: "learned", Name: source}}
				entry = &copy
				s.learned.Terms[key] = entry
			}
			entry.Evidence++
			entry.LastSeenAt = now
			entry.Sources = mergeSources(entry.Sources, []Source{{Kind: "learned", Name: source}})
		}
		rememberReceipt(s.learned.Seen, &s.learned.SeenOrder, eventID)
		if err := s.saveLearnedLocked(); err != nil {
			s.learned = before
			return err
		}
		s.rebuildLocked()
		return nil
	})
}

// RecordUse updates ranking after the user accepts a completion. useID makes a
// retried UI receipt idempotent; unknown term IDs are harmless.
func (s *Service) RecordUse(id, useID string) error {
	if s == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withStoreLockLocked(func() error {
		if useID != "" && s.learned.Used[useID] {
			return nil
		}
		before := cloneLearned(s.learned)
		found := false
		for _, entry := range s.learned.Terms {
			if entry.ID == id {
				entry.UseCount++
				entry.LastSeenAt = time.Now().UTC()
				found = true
				break
			}
		}
		if !found {
			for _, entry := range s.entries {
				if entry.ID != id {
					continue
				}
				overlay := Entry{ID: entry.ID, Text: entry.Text, Kind: entry.Kind, UseCount: 1, LastSeenAt: time.Now().UTC(), Sources: []Source{{Kind: "learned", Name: "completion-use"}}}
				s.learned.Terms[keyOf(entry.Text)] = &overlay
				found = true
				break
			}
		}
		if !found {
			return nil
		}
		if useID != "" {
			rememberReceipt(s.learned.Used, &s.learned.UsedOrder, useID)
		}
		if err := s.saveLearnedLocked(); err != nil {
			s.learned = before
			return err
		}
		s.rebuildLocked()
		return nil
	})
}

// Context returns definitions for canonical terms mentioned in text. It is
// intentionally small and transient so dynamic vocabulary never churns the
// cache-stable system prompt.
func (s *Service) Context(text string, limit int) string {
	if s == nil || strings.TrimSpace(text) == "" {
		return ""
	}
	if limit <= 0 || limit > 8 {
		limit = 5
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var lines []string
	for _, entry := range s.entries {
		if strings.TrimSpace(entry.Description) == "" || !strings.Contains(strings.ToLower(text), strings.ToLower(entry.Text)) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s (%s): %s", entry.Text, entry.Kind, oneLine(entry.Description)))
		if len(lines) == limit {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "<workspace-vocabulary>\nTerms mentioned in this user request:\n" + strings.Join(lines, "\n") + "\n</workspace-vocabulary>"
}

var (
	quotedTerm   = regexp.MustCompile(`[“”"「」『』‘’']([^“”"「」『』‘’'\r\n]{2,40})[“”"「」『』‘’']`)
	specialTerm  = regexp.MustCompile(`[\p{Han}A-Za-z][\p{Han}A-Za-z0-9_.+\-]{2,39}`)
	declaredTerm = regexp.MustCompile(`(术语|名词|动词|节点|功能|命令)\s*(?:[：:]\s*|(?:叫做?|为|是)\s*)([\p{Han}A-Za-z][\p{Han}A-Za-z0-9_.+\-]{1,39})`)
)

// Extract finds conservative workspace terms without a model call.
func Extract(text string) []Entry {
	seen := map[string]bool{}
	var out []Entry
	add := func(raw string, kind Kind) {
		raw = strings.TrimSpace(raw)
		if !validTerm(raw) || seen[keyOf(raw)] {
			return
		}
		seen[keyOf(raw)] = true
		out = append(out, Entry{ID: idOf(raw), Text: raw, Kind: kind})
	}
	for _, match := range quotedTerm.FindAllStringSubmatch(text, -1) {
		add(match[1], inferKind(text, match[1]))
	}
	for _, match := range declaredTerm.FindAllStringSubmatch(text, -1) {
		kind := KindNoun
		if match[1] == "动词" {
			kind = KindVerb
		}
		add(match[2], kind)
	}
	for _, token := range specialTerm.FindAllString(text, -1) {
		if distinctive(token) {
			add(token, inferKind(text, token))
		}
	}
	return out
}

func (s *Service) rebuildLocked() {
	merged := map[string]Entry{}
	for _, entry := range append(append([]Entry{}, s.static...), learnedEntries(s.learned.Terms)...) {
		entry = normalize(entry)
		if entry.Text == "" {
			continue
		}
		key := keyOf(entry.Text)
		if current, ok := merged[key]; ok {
			merged[key] = mergeEntry(current, entry)
		} else {
			merged[key] = entry
		}
	}
	s.entries = s.entries[:0]
	for _, entry := range merged {
		s.entries = append(s.entries, entry)
	}
	sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].Text < s.entries[j].Text })
}

func (s *Service) learnedPath() string { return filepath.Join(s.stateDir, stateFile) }

func (s *Service) loadLearned() {
	if s.stateDir == "" {
		return
	}
	state, err := readLearned(s.learnedPath())
	if err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("read learned vocabulary %s: %v", s.learnedPath(), err))
		return
	}
	s.learned = state
	s.stateStamp = fileStamp(s.learnedPath())
}

// refreshLearned makes terms learned by another Session visible without
// rebuilding its Controller. Atomic writes let readers safely refresh without
// taking the writer's cross-process lock.
func (s *Service) refreshLearned() {
	if s == nil || s.stateDir == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Before(s.nextRefresh) {
		return
	}
	s.nextRefresh = now.Add(250 * time.Millisecond)
	stamp := fileStamp(s.learnedPath())
	if stamp == s.stateStamp {
		return
	}
	if stamp == "" {
		s.learned = emptyLearned()
		s.stateStamp = ""
		s.rebuildLocked()
		return
	}
	state, err := readLearned(s.learnedPath())
	if err != nil {
		return // retain the last complete snapshot; the next query retries.
	}
	s.learned = state
	s.stateStamp = stamp
	s.rebuildLocked()
}

func (s *Service) withStoreLockLocked(mutate func() error) (err error) {
	release, err := s.acquireStoreLock()
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := release(); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}()
	if s.stateDir != "" {
		state, readErr := readLearned(s.learnedPath())
		if readErr != nil {
			return fmt.Errorf("reload learned vocabulary: %w", readErr)
		}
		s.learned = state
		s.stateStamp = fileStamp(s.learnedPath())
		s.rebuildLocked()
	}
	return mutate()
}

func (s *Service) acquireStoreLock() (func() error, error) {
	if s.stateDir == "" {
		return func() error { return nil }, nil
	}
	if err := os.MkdirAll(s.stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create vocabulary state: %w", err)
	}
	path := filepath.Join(s.stateDir, lockDir)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := os.Mkdir(path, 0o700); err == nil {
			return func() error {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("release vocabulary lock: %w", err)
				}
				return nil
			}, nil
		} else if !os.IsExist(err) {
			return nil, fmt.Errorf("acquire vocabulary lock: %w", err)
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > time.Minute {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("vocabulary store is busy; retry the operation")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func emptyLearned() learnedState {
	return learnedState{Version: 1, Terms: map[string]*Entry{}, Seen: map[string]bool{}, Used: map[string]bool{}}
}

func readLearned(path string) (learnedState, error) {
	state := emptyLearned()
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return emptyLearned(), fmt.Errorf("parse %s: %w", path, err)
	}
	if state.Terms == nil {
		state.Terms = map[string]*Entry{}
	}
	if state.Seen == nil {
		state.Seen = map[string]bool{}
	}
	if state.Used == nil {
		state.Used = map[string]bool{}
	}
	return state, nil
}

func rememberReceipt(receipts map[string]bool, order *[]string, id string) {
	if id == "" || receipts[id] {
		return
	}
	receipts[id] = true
	*order = append(*order, id)
	for len(*order) > maxReceipts {
		delete(receipts, (*order)[0])
		*order = (*order)[1:]
	}
}

func (s *Service) saveLearnedLocked() error {
	if s.stateDir == "" {
		return nil
	}
	body, err := json.MarshalIndent(s.learned, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := fileutil.AtomicWriteFile(s.learnedPath(), body, 0o600); err != nil {
		return err
	}
	s.stateStamp = fileStamp(s.learnedPath())
	return nil
}

func fileStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}

func loadProject(path string, source Source) ([]Entry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	var file projectFile
	if _, err := toml.DecodeFile(path, &file); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for i := range file.Terms {
		file.Terms[i].Text = strings.TrimSpace(file.Terms[i].Text)
		if !validTerm(file.Terms[i].Text) {
			return nil, fmt.Errorf("terms[%d].text is invalid", i)
		}
		if file.Terms[i].Kind != "" && file.Terms[i].Kind != KindNoun && file.Terms[i].Kind != KindVerb && file.Terms[i].Kind != KindPhrase {
			return nil, fmt.Errorf("terms[%d].kind %q is invalid", i, file.Terms[i].Kind)
		}
		for aliasIndex, alias := range file.Terms[i].Aliases {
			if !validTerm(strings.TrimSpace(alias)) {
				return nil, fmt.Errorf("terms[%d].aliases[%d] is invalid", i, aliasIndex)
			}
		}
		file.Terms[i].Sources = []Source{sourceWithPath(source, path)}
	}
	return file.Terms, nil
}

func (s *Service) loadFile(path string, source Source) []Entry {
	entries, err := loadProject(path, source)
	if err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("parse vocabulary %s: %v", path, err))
		return nil
	}
	return entries
}

func (s *Service) appendSkillLocked(sk SkillSource) {
	source := Source{Kind: "skill", Name: sk.Name, Path: sk.Path}
	for _, term := range sk.Terms {
		if entry, ok := simpleEntry(term, source); ok {
			s.static = append(s.static, entry)
		}
	}
	if sidecar := skillSidecar(sk.Path); sidecar != "" {
		s.static = append(s.static, s.loadFile(sidecar, source)...)
	}
}

func (s *Service) appendAgentLocked(doc AgentSource) {
	s.static = append(s.static, entriesFromAgent(doc)...)
	if strings.TrimSpace(doc.Path) != "" {
		s.static = append(s.static, s.loadFile(filepath.Join(filepath.Dir(doc.Path), SidecarFile), Source{Kind: "agent", Name: doc.Name, Path: doc.Path})...)
	}
}

func entryFromSkill(entry Entry, name string) bool {
	return entryFromSource(entry, "skill", name)
}

func entryFromSource(entry Entry, kind, name string) bool {
	for _, source := range entry.Sources {
		if source.Kind == kind && strings.EqualFold(source.Name, name) {
			return true
		}
	}
	return false
}

func entriesFromAgent(doc AgentSource) []Entry {
	source := Source{Kind: "agent", Name: doc.Name, Path: doc.Path}
	fm, body := frontmatter.Split(doc.Body)
	var out []Entry
	for _, term := range splitTerms(fm["vocabulary"]) {
		if entry, ok := simpleEntry(term, source); ok {
			out = append(out, entry)
		}
	}
	inVocabulary := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			title := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			inVocabulary = title == "vocabulary" || title == "词汇表" || title == "术语表"
			continue
		}
		if !inVocabulary {
			continue
		}
		if !strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "*") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "-"), "*"))
		if entry, ok := simpleEntry(item, source); ok {
			out = append(out, entry)
		}
	}
	return out
}

func simpleEntry(raw string, source Source) (Entry, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Entry{}, false
	}
	text, desc, _ := strings.Cut(raw, " — ")
	if desc == "" {
		text, desc, _ = strings.Cut(raw, " - ")
	}
	text = strings.TrimSpace(text)
	if !validTerm(text) {
		return Entry{}, false
	}
	return Entry{ID: idOf(text), Text: text, Kind: inferKind(desc, text), Description: strings.TrimSpace(desc), Sources: []Source{source}}, true
}

func skillSidecar(path string) string {
	if path == "" || path == "(builtin)" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), SidecarFile)
}

func normalize(entry Entry) Entry {
	entry.Text = strings.TrimSpace(entry.Text)
	entry.Description = strings.TrimSpace(entry.Description)
	if entry.Kind != KindVerb && entry.Kind != KindPhrase {
		entry.Kind = KindNoun
	}
	entry.ID = idOf(entry.Text)
	entry.Aliases = uniqueStrings(entry.Aliases)
	return entry
}

func mergeEntry(a, b Entry) Entry {
	// Higher-priority sources own authored metadata; counters and provenance from
	// every source still accumulate.
	if sourceRank(b.Sources) > sourceRank(a.Sources) {
		a, b = b, a
	}
	if a.Description == "" {
		a.Description = b.Description
	}
	if a.Kind == "" {
		a.Kind = b.Kind
	}
	a.Preferred = a.Preferred || b.Preferred
	a.Aliases = uniqueStrings(append(a.Aliases, b.Aliases...))
	a.Sources = mergeSources(a.Sources, b.Sources)
	a.Evidence += b.Evidence
	a.UseCount += b.UseCount
	if b.LastSeenAt.After(a.LastSeenAt) {
		a.LastSeenAt = b.LastSeenAt
	}
	return a
}

func learnedEntries(terms map[string]*Entry) []Entry {
	out := make([]Entry, 0, len(terms))
	for _, entry := range terms {
		if entry != nil {
			out = append(out, *entry)
		}
	}
	return out
}

func cloneLearned(state learnedState) learnedState {
	copyState := learnedState{
		Version:   state.Version,
		Terms:     make(map[string]*Entry, len(state.Terms)),
		Seen:      make(map[string]bool, len(state.Seen)),
		SeenOrder: append([]string(nil), state.SeenOrder...),
		Used:      make(map[string]bool, len(state.Used)),
		UsedOrder: append([]string(nil), state.UsedOrder...),
	}
	for key, entry := range state.Terms {
		if entry == nil {
			continue
		}
		copyEntry := *entry
		copyEntry.Aliases = append([]string(nil), entry.Aliases...)
		copyEntry.Sources = append([]Source(nil), entry.Sources...)
		copyState.Terms[key] = &copyEntry
	}
	for key, value := range state.Seen {
		copyState.Seen[key] = value
	}
	for key, value := range state.Used {
		copyState.Used[key] = value
	}
	return copyState
}

func sourceWithPath(source Source, path string) Source {
	if source.Path == "" {
		source.Path = path
	}
	return source
}

func mergeSources(a, b []Source) []Source {
	out := append([]Source{}, a...)
	seen := map[string]bool{}
	for _, source := range out {
		seen[source.Kind+"\x00"+source.Name+"\x00"+source.Path] = true
	}
	for _, source := range b {
		key := source.Kind + "\x00" + source.Name + "\x00" + source.Path
		if !seen[key] {
			seen[key] = true
			out = append(out, source)
		}
	}
	return out
}

func uniqueStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		key := strings.ToLower(item)
		if item != "" && !seen[key] {
			seen[key] = true
			out = append(out, item)
		}
	}
	return out
}

func splitTerms(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '，' || r == ';' || r == '；' })
}

func inferKind(context, term string) Kind {
	for _, marker := range []string{"动词：" + term, "动词:" + term, "动词: " + term, term + "是动词", term + "是一个动词"} {
		if strings.Contains(context, marker) {
			return KindVerb
		}
	}
	if (strings.Contains(context, "动词") && len([]rune(context)) < 30) || strings.HasSuffix(term, "化") {
		return KindVerb
	}
	return KindNoun
}

func distinctive(term string) bool {
	hasHan, hasLatin, hasDigit, hasUpper, hasLower := false, false, false, false, false
	for _, r := range term {
		switch {
		case unicode.Is(unicode.Han, r):
			hasHan = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsLetter(r):
			hasLatin = true
			hasUpper = hasUpper || unicode.IsUpper(r)
			hasLower = hasLower || unicode.IsLower(r)
		}
	}
	return hasDigit || (hasHan && hasLatin) || (hasUpper && hasLower) || strings.ContainsAny(term, "_.+-")
}

func validTerm(term string) bool {
	count := utf8.RuneCountInString(term)
	if count < 2 || count > 40 || strings.ContainsAny(term, "\r\n\t/@\\") || likelySecret(term) {
		return false
	}
	spaces := 0
	for _, r := range term {
		if unicode.IsSpace(r) {
			spaces++
		}
	}
	return spaces <= 2
}

func likelySecret(term string) bool {
	lower := strings.ToLower(term)
	for _, prefix := range []string{"sk-", "ghp_", "github_pat_", "xoxb-", "xoxp-", "bearer_", "api_key"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	hasHan, letters, digits := false, 0, 0
	for _, r := range term {
		switch {
		case unicode.Is(unicode.Han, r):
			hasHan = true
		case unicode.IsLetter(r):
			letters++
		case unicode.IsDigit(r):
			digits++
		}
	}
	return !hasHan && utf8.RuneCountInString(term) >= 20 && letters >= 8 && digits >= 4
}

func minPrefix(prefix string) int {
	for _, r := range prefix {
		if unicode.Is(unicode.Han, r) {
			return 2
		}
	}
	return 3
}

func runeSuffix(term, prefix string) string {
	tr, pr := []rune(term), []rune(prefix)
	if len(pr) >= len(tr) {
		return ""
	}
	return string(tr[len(pr):])
}

func keyOf(text string) string { return strings.ToLower(strings.TrimSpace(text)) }

func idOf(text string) string {
	sum := sha256.Sum256([]byte(keyOf(text)))
	return "term-" + hex.EncodeToString(sum[:8])
}

func cleanAbs(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

func sourceRank(sources []Source) int {
	best := 0
	for _, source := range sources {
		switch source.Kind {
		case "agent":
			best = max(best, 400)
		case "skill":
			best = max(best, 300)
		case "workspace":
			best = max(best, 200)
		case "learned":
			best = max(best, 100)
		}
	}
	return best
}

func primarySource(sources []Source) string {
	best, rank := "", -1
	for _, source := range sources {
		if next := sourceRank([]Source{source}); next > rank {
			best, rank = source.Kind, next
		}
	}
	return best
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }
