package skillshare

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"workground2/internal/skill"
)

const (
	remoteManifestNative  = "WorkGround2-plugin.json"
	remoteManifestCodex   = ".codex-plugin/plugin.json"
	remoteIndexFile       = ".flow-skill-index.json"
	remoteUserAgent       = "WorkGround2-FlowSkillShare/0.1"
	maxRemoteSkills       = 200
	maxRemoteFileBytes    = 5 << 20
	maxRemoteArchiveBytes = 50 << 20
)

type remoteSnapshot struct {
	ManifestKind string
	Version      string
	Revision     string
	Skills       []skill.Skill
	Hooks        int
	MCPServers   int
}

type remoteManifest struct {
	Name       string           `json:"name"`
	Version    string           `json:"version"`
	Skills     json.RawMessage  `json:"skills"`
	Hooks      map[string][]any `json:"hooks"`
	MCPServers map[string]any   `json:"mcpServers"`
}

type remoteProvider struct {
	service *Service
}

func (s *Service) Provider() skill.Provider {
	return &remoteProvider{service: s}
}

func (p *remoteProvider) List() []skill.Skill {
	if p == nil || p.service == nil || !p.service.Remote {
		return nil
	}
	profiles, err := p.enabledProfiles()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	byName := map[string]skill.Skill{}
	for _, profile := range profiles {
		cred, err := p.service.resolveCredential(profile)
		if err != nil {
			continue
		}
		snap, err := fetchRemoteSnapshot(ctx, profile, cred)
		if err != nil {
			continue
		}
		for _, sk := range snap.Skills {
			if _, exists := byName[sk.Name]; !exists {
				byName[sk.Name] = sk
			}
		}
	}
	out := make([]skill.Skill, 0, len(byName))
	for _, sk := range byName {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (p *remoteProvider) Read(name string) (skill.Skill, bool) {
	if p == nil || p.service == nil || !p.service.Remote || !skill.IsValidName(name) {
		return skill.Skill{}, false
	}
	profiles, err := p.enabledProfiles()
	if err != nil {
		return skill.Skill{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, profile := range profiles {
		cred, err := p.service.resolveCredential(profile)
		if err != nil {
			continue
		}
		snap, err := fetchRemoteSnapshot(ctx, profile, cred)
		if err != nil {
			continue
		}
		for _, sk := range snap.Skills {
			if sk.Name == name {
				return sk, true
			}
		}
	}
	return skill.Skill{}, false
}

func (p *remoteProvider) enabledProfiles() ([]profileRecord, error) {
	p.service.mu.Lock()
	defer p.service.mu.Unlock()

	st, err := p.service.readProfiles()
	if err != nil {
		return nil, err
	}
	out := make([]profileRecord, 0, len(st.Profiles))
	for _, profile := range st.Profiles {
		if profile.Enabled {
			out = append(out, profile)
		}
	}
	return out, nil
}

func (s *Service) syncRemote(ctx context.Context, st profileFile, idx int, p profileRecord, task TaskView, start time.Time, cred gitCredential, opts SyncOptions) (TaskView, error) {
	task.Phase = "fetch_remote"
	snap, err := fetchRemoteSnapshot(ctx, p, cred)
	if err != nil {
		safe := sanitizeError(err, cred.Password, p.Git.URL)
		p.State = failedState(p.State, StatusUpdateFailed, errors.New(safe), []string{cred.Password}, start)
		st.Profiles[idx] = p
		_ = s.writeProfiles(st)
		return finishTask(task, TaskFailed, safe, true), errors.New(safe)
	}

	if !opts.Force && p.State.CurrentRevision != "" && snap.Revision != "" && p.State.CurrentRevision == snap.Revision {
		p.State.Status = StatusReady
		p.State.LastCheckedAt = formatTime(start)
		p.State.LastError = ""
		st.Profiles[idx] = p
		if err := s.writeProfiles(st); err != nil {
			safe := sanitizeError(err)
			return finishTask(task.withPhase("write_profiles"), TaskFailed, safe, true), errors.New(safe)
		}
		task.CurrentRevision = snap.Revision
		return finishTask(task.withPhase("ready"), TaskSucceeded, "", false), nil
	}

	p.State.Status = StatusReady
	p.State.CurrentRevision = snap.Revision
	p.State.LastCheckedAt = formatTime(start)
	p.State.LastUpdatedAt = formatTime(start)
	p.State.LastError = ""
	p.ManifestKind = snap.ManifestKind
	p.Version = snap.Version
	p.Skills = len(snap.Skills)
	p.Hooks = snap.Hooks
	p.MCPServers = snap.MCPServers
	st.Profiles[idx] = p
	if err := s.writeProfiles(st); err != nil {
		safe := sanitizeError(err)
		return finishTask(task.withPhase("write_profiles"), TaskFailed, safe, true), errors.New(safe)
	}
	task.CurrentRevision = snap.Revision
	task.TargetRevision = snap.Revision
	return finishTask(task.withPhase("ready"), TaskSucceeded, "", false), nil
}

func fetchRemoteSnapshot(ctx context.Context, p profileRecord, cred gitCredential) (remoteSnapshot, error) {
	src, err := newRemoteSource(p, cred)
	if err != nil {
		return remoteSnapshot{}, err
	}
	manifestKind := "WorkGround2"
	data, revision, err := src.fetch(ctx, remoteManifestNative)
	if err != nil {
		data, revision, err = src.fetch(ctx, remoteManifestCodex)
		if err != nil {
			return remoteSnapshot{}, err
		}
		manifestKind = "codex"
	}
	var manifest remoteManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return remoteSnapshot{}, fmt.Errorf("remote manifest: %w", err)
	}
	skillRoots, err := parseRemoteSkillPaths(manifest.Skills)
	if err != nil {
		return remoteSnapshot{}, err
	}
	var skills []skill.Skill
	seenFiles := map[string]bool{}
	for _, root := range skillRoots {
		files, err := src.listSkillFiles(ctx, root)
		if err != nil {
			return remoteSnapshot{}, err
		}
		for _, file := range files {
			if len(skills) >= maxRemoteSkills {
				return remoteSnapshot{}, fmt.Errorf("too many remote skills; limit is %d", maxRemoteSkills)
			}
			key := path.Clean(file)
			if seenFiles[key] {
				continue
			}
			seenFiles[key] = true
			content, fileRevision, err := src.fetch(ctx, key)
			if err != nil {
				return remoteSnapshot{}, err
			}
			if fileRevision != "" {
				revision = fileRevision
			}
			stem := remoteSkillStem(key)
			sk, ok := skill.ParseMarkdownContent(string(content), src.displayPath(key), stem, skill.ScopeRemote, false)
			if !ok {
				continue
			}
			sk.Protected = true
			sk.AntiLeak = true
			sk.SourceKind = skill.SourceFlowSkillShare
			skills = append(skills, sk)
		}
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	if revision == "" {
		revision = fmt.Sprintf("remote:%d:%s", len(skills), manifest.Version)
	}
	return remoteSnapshot{
		ManifestKind: manifestKind,
		Version:      strings.TrimSpace(manifest.Version),
		Revision:     revision,
		Skills:       skills,
		Hooks:        countRemoteHooks(manifest.Hooks),
		MCPServers:   len(manifest.MCPServers),
	}, nil
}

func parseRemoteSkillPaths(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return cleanRemotePathList([]string{one})
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return cleanRemotePathList(many)
	}
	var objects []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &objects); err == nil {
		paths := make([]string, 0, len(objects))
		for _, item := range objects {
			paths = append(paths, item.Path)
		}
		return cleanRemotePathList(paths)
	}
	return nil, fmt.Errorf("remote skills must be a path string, string array, or object array")
}

func cleanRemotePathList(paths []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = cleanRemoteRel(p)
		if p == "" || p == "." {
			p = "."
		}
		if strings.HasPrefix(p, "../") || p == ".." {
			return nil, fmt.Errorf("remote plugin path %q must stay inside the source root", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func countRemoteHooks(hooks map[string][]any) int {
	var n int
	for _, list := range hooks {
		n += len(list)
	}
	return n
}

func remoteSkillStem(file string) string {
	file = path.Clean(file)
	if strings.EqualFold(path.Base(file), skill.SkillFile) {
		return path.Base(path.Dir(file))
	}
	base := path.Base(file)
	return strings.TrimSuffix(base, path.Ext(base))
}

type remoteSource struct {
	rawURL   string
	branch   string
	basePath string
	cred     gitCredential
	client   *http.Client

	kind    string
	host    string
	owner   string
	repo    string
	project string
}

func newRemoteSource(p profileRecord, cred gitCredential) (*remoteSource, error) {
	raw := strings.TrimSpace(p.Git.URL)
	if raw == "" {
		return nil, errors.New("git url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid remote url %q", raw)
	}
	src := &remoteSource{
		rawURL:   strings.TrimRight(raw, "/"),
		branch:   strings.TrimSpace(p.Git.Branch),
		basePath: cleanRemoteRel(p.Git.Path),
		cred:     cred,
		client:   &http.Client{Timeout: 20 * time.Second},
		host:     strings.ToLower(u.Host),
	}
	if src.branch == "" {
		src.branch = "main"
	}
	if src.basePath == "." {
		src.basePath = ""
	}

	parts := splitURLPath(u.Path)
	if src.host == "github.com" && len(parts) >= 2 {
		src.kind = "github"
		src.owner = parts[0]
		src.repo = strings.TrimSuffix(parts[1], ".git")
		if len(parts) >= 4 && parts[2] == "tree" {
			src.branch = parts[3]
			src.basePath = cleanRemoteRel(path.Join(strings.Join(parts[4:], "/"), src.basePath))
		}
		return src, nil
	}
	if strings.Contains(src.host, "gitlab") && len(parts) >= 1 {
		src.kind = "gitlab"
		if idx := indexPart(parts, "-"); idx >= 0 && idx+2 < len(parts) && parts[idx+1] == "tree" {
			src.project = strings.Join(parts[:idx], "/")
			src.branch = parts[idx+2]
			src.basePath = cleanRemoteRel(path.Join(strings.Join(parts[idx+3:], "/"), src.basePath))
		} else {
			src.project = strings.TrimSuffix(strings.Join(parts, "/"), ".git")
		}
		return src, nil
	}
	src.kind = "generic"
	return src, nil
}

func (s *remoteSource) fetch(ctx context.Context, rel string) ([]byte, string, error) {
	remoteURL, err := s.fileURL(rel)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", remoteUserAgent)
	s.authorize(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", responseStatusError(resp, remoteURL)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteFileBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxRemoteFileBytes {
		return nil, "", fmt.Errorf("fetch %s: response exceeds %d bytes", remoteURL, maxRemoteFileBytes)
	}
	revision := strings.Trim(resp.Header.Get("ETag"), `"`)
	if revision == "" {
		revision = strings.TrimSpace(resp.Header.Get("Last-Modified"))
	}
	return data, revision, nil
}

func (s *remoteSource) listSkillFiles(ctx context.Context, root string) ([]string, error) {
	root = cleanRemoteRel(root)
	if root == "." {
		root = ""
	}
	if strings.EqualFold(path.Ext(root), ".md") {
		return []string{root}, nil
	}
	if files, err := s.listIndex(ctx, root); err == nil {
		return files, nil
	}
	switch s.kind {
	case "github":
		return s.listGitHub(ctx, root)
	case "gitlab":
		return s.listGitLab(ctx, root)
	default:
		return s.listGeneric(ctx, root)
	}
}

func (s *remoteSource) fileURL(rel string) (string, error) {
	rel = cleanRemoteRel(path.Join(s.basePath, rel))
	switch s.kind {
	case "github":
		return "https://raw.githubusercontent.com/" + escapePath(s.owner) + "/" + escapePath(s.repo) + "/" + escapePath(s.branch) + "/" + escapePath(rel), nil
	case "gitlab":
		return "https://" + s.host + "/" + escapePath(s.project) + "/-/raw/" + escapePath(s.branch) + "/" + escapePath(rel), nil
	default:
		u, err := url.Parse(s.rawURL)
		if err != nil {
			return "", err
		}
		u.Path = path.Join(u.Path, rel)
		return u.String(), nil
	}
}

func (s *remoteSource) displayPath(rel string) string {
	return "flow-skill-share:" + s.rawURL + "#" + cleanRemoteRel(path.Join(s.basePath, rel))
}

func (s *remoteSource) listGitHub(ctx context.Context, root string) ([]string, error) {
	if strings.TrimSpace(s.cred.Password) == "" {
		if files, err := s.listGitHubArchive(ctx, root); err == nil {
			return files, nil
		}
	}
	var out []string
	var walk func(string) error
	walk = func(dir string) error {
		apiPath := cleanRemoteRel(path.Join(s.basePath, dir))
		api := "https://api.github.com/repos/" + escapePath(s.owner) + "/" + escapePath(s.repo) + "/contents"
		if apiPath != "" && apiPath != "." {
			api += "/" + escapePath(apiPath)
		}
		api += "?ref=" + url.QueryEscape(s.branch)
		var entries []struct {
			Type string `json:"type"`
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := s.getJSON(ctx, api, &entries); err != nil {
			return err
		}
		for _, entry := range entries {
			rel := trimRemoteBase(s.basePath, entry.Path)
			switch entry.Type {
			case "dir":
				if shouldSkipRemoteDir(entry.Name) {
					continue
				}
				if err := walk(rel); err != nil {
					return err
				}
			case "file":
				if isRemoteDirectorySkillFile(rel) {
					out = append(out, cleanRemoteRel(rel))
				}
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		if files, archiveErr := s.listGitHubArchive(ctx, root); archiveErr == nil {
			return files, nil
		}
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func (s *remoteSource) listGitHubArchive(ctx context.Context, root string) ([]string, error) {
	archiveURL := "https://codeload.github.com/" + escapePath(s.owner) + "/" + escapePath(s.repo) + "/zip/" + escapePath(s.branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", remoteUserAgent)
	s.authorize(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseStatusError(resp, archiveURL)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRemoteArchiveBytes {
		return nil, fmt.Errorf("fetch %s: response exceeds %d bytes", archiveURL, maxRemoteArchiveBytes)
	}
	return listArchiveSkillFiles(data, s.basePath, root)
}

func (s *remoteSource) listGitLab(ctx context.Context, root string) ([]string, error) {
	apiPath := cleanRemoteRel(path.Join(s.basePath, root))
	api := "https://" + s.host + "/api/v4/projects/" + url.PathEscape(s.project) + "/repository/tree?recursive=true&per_page=100&ref=" + url.QueryEscape(s.branch)
	if apiPath != "" && apiPath != "." {
		api += "&path=" + url.QueryEscape(apiPath)
	}
	var entries []struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := s.getJSON(ctx, api, &entries); err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.Type != "blob" || shouldSkipRemoteDir(entry.Name) {
			continue
		}
		rel := trimRemoteBase(s.basePath, entry.Path)
		if isRemoteDirectorySkillFile(rel) {
			out = append(out, cleanRemoteRel(rel))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *remoteSource) listGeneric(ctx context.Context, root string) ([]string, error) {
	out, err := s.listIndex(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("generic HTTP source requires %s under %s: %w", remoteIndexFile, root, err)
	}
	return out, nil
}

func (s *remoteSource) listIndex(ctx context.Context, root string) ([]string, error) {
	indexPath := cleanRemoteRel(path.Join(root, remoteIndexFile))
	data, _, err := s.fetch(ctx, indexPath)
	if err != nil {
		data, _, err = s.fetch(ctx, cleanRemoteRel(path.Join(root, "index.json")))
		if err != nil {
			return nil, err
		}
	}
	var raw struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		var files []string
		if arrErr := json.Unmarshal(data, &files); arrErr != nil {
			return nil, err
		}
		raw.Files = files
	}
	var out []string
	for _, file := range raw.Files {
		file = cleanRemoteRel(file)
		if file == "" || file == "." {
			continue
		}
		if !strings.HasPrefix(file, root+"/") && root != "" {
			file = cleanRemoteRel(path.Join(root, file))
		}
		if isRemoteSkillFile(file) {
			out = append(out, file)
		}
	}
	sort.Strings(out)
	return out, nil
}

func listArchiveSkillFiles(data []byte, basePath, root string) ([]string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	target := cleanRemoteRel(path.Join(basePath, root))
	if target == "." {
		target = ""
	}
	seen := map[string]bool{}
	var out []string
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		rel := archiveEntryRel(file.Name)
		if rel == "" {
			continue
		}
		if target != "" && rel != target && !strings.HasPrefix(rel, target+"/") {
			continue
		}
		rel = trimRemoteBase(basePath, rel)
		if !isRemoteDirectorySkillFile(rel) || shouldSkipRemotePath(rel) {
			continue
		}
		rel = cleanRemoteRel(rel)
		if !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *remoteSource) getJSON(ctx context.Context, remoteURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", remoteUserAgent)
	s.authorize(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseStatusError(resp, remoteURL)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxRemoteFileBytes)).Decode(target)
}

func (s *remoteSource) authorize(req *http.Request) {
	if strings.TrimSpace(s.cred.Password) == "" {
		return
	}
	if username := strings.TrimSpace(s.cred.Username); username != "" {
		req.SetBasicAuth(username, s.cred.Password)
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.cred.Password)
}

func isRemoteSkillFile(rel string) bool {
	base := path.Base(rel)
	return strings.EqualFold(base, skill.SkillFile) || strings.EqualFold(path.Ext(base), ".md")
}

func isRemoteDirectorySkillFile(rel string) bool {
	return strings.EqualFold(path.Base(rel), skill.SkillFile)
}

func shouldSkipRemoteDir(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "" || strings.HasPrefix(name, ".") || name == "assets" || name == "node_modules" || name == "references" || name == "scripts"
}

func shouldSkipRemotePath(rel string) bool {
	for _, part := range splitURLPath(path.Dir(rel)) {
		if shouldSkipRemoteDir(part) {
			return true
		}
	}
	return false
}

func archiveEntryRel(name string) string {
	parts := splitURLPath(name)
	if len(parts) <= 1 {
		return ""
	}
	return cleanRemoteRel(strings.Join(parts[1:], "/"))
}

func cleanRemoteRel(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	if p == "." {
		return "."
	}
	return strings.TrimPrefix(p, "/")
}

func splitURLPath(p string) []string {
	var out []string
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func indexPart(parts []string, needle string) int {
	for i, part := range parts {
		if part == needle {
			return i
		}
	}
	return -1
}

func escapePath(p string) string {
	var parts []string
	for _, part := range splitURLPath(p) {
		parts = append(parts, url.PathEscape(part))
	}
	return strings.Join(parts, "/")
}

func trimRemoteBase(base, full string) string {
	base = cleanRemoteRel(base)
	full = cleanRemoteRel(full)
	if base == "" || base == "." {
		return full
	}
	if full == base {
		return "."
	}
	return strings.TrimPrefix(full, base+"/")
}

func responseStatusError(resp *http.Response, remoteURL string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Errorf("fetch %s: %s", remoteURL, resp.Status)
	}
	return fmt.Errorf("fetch %s: %s: %s", remoteURL, resp.Status, text)
}
