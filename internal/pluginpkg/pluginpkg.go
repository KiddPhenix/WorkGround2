// Package pluginpkg handles installed WorkGround2 plugin packages.
//
// Plugin packages are higher-level bundles that can contribute skills, hooks,
// and MCP servers. They are intentionally parsed into package-local structs so
// config/hook/desktop callers can adapt them without creating import cycles.
package pluginpkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	fileencoding "workground2/internal/fileutil/encoding"
)

const (
	NativeManifest = "WorkGround2-plugin.json"
	CodexManifest  = ".codex-plugin/plugin.json"
	StateFilename  = "plugin-packages.json"
)

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Package is one parsed plugin package rooted on disk.
type Package struct {
	Root         string
	ManifestKind string
	Manifest     Manifest
}

// Manifest is the normalized manifest shape used by WorkGround2.
type Manifest struct {
	Name        string
	Version     string
	Description string
	Homepage    string
	Repository  string
	Skills      []string
	Hooks       map[string][]Hook
	MCPServers  map[string]MCPServer
	AddOn       *AddOn
}

type AddOn struct {
	Kind         string   `json:"kind,omitempty"`
	DisplayName  string   `json:"displayName,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Panels       []Panel  `json:"panels,omitempty"`
	ConfigSchema string   `json:"configSchema,omitempty"`
	Secrets      []Secret `json:"secrets,omitempty"`
	Runtime      *Runtime `json:"runtime,omitempty"`
	Update       *Update  `json:"update,omitempty"`
	Storage      *Storage `json:"storage,omitempty"`
}

type Runtime struct {
	Type      string `json:"type,omitempty"`
	MCPServer string `json:"mcpServer,omitempty"`
}

type Panel struct {
	ID     string `json:"id,omitempty"`
	Title  string `json:"title,omitempty"`
	Entry  string `json:"entry,omitempty"`
	Dialog bool   `json:"dialog,omitempty"`
}

type Secret struct {
	ID       string `json:"id,omitempty"`
	Label    string `json:"label,omitempty"`
	Purpose  string `json:"purpose,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type Update struct {
	Type       string `json:"type,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
	Check      string `json:"check,omitempty"`
	Credential string `json:"credential,omitempty"`
}

type Storage struct {
	Namespace string `json:"namespace,omitempty"`
}

type Hook struct {
	Match       string            `json:"match,omitempty"`
	Command     string            `json:"command"`
	Description string            `json:"description,omitempty"`
	Timeout     int               `json:"timeout,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

type MCPServer struct {
	Type      string            `json:"type,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	AutoStart *bool             `json:"auto_start,omitempty"`
	Tier      string            `json:"tier,omitempty"`
}

// State is persisted at <WorkGround2 home>/plugin-packages.json.
type State struct {
	Version int               `json:"version"`
	Plugins []InstalledPlugin `json:"plugins"`
}

type InstalledPlugin struct {
	Name            string `json:"name"`
	Source          string `json:"source,omitempty"`
	Root            string `json:"root"`
	Version         string `json:"version,omitempty"`
	Description     string `json:"description,omitempty"`
	ManifestKind    string `json:"manifestKind,omitempty"`
	Enabled         bool   `json:"enabled"`
	InstalledAt     string `json:"installedAt,omitempty"`
	LastCheckedAt   string `json:"lastCheckedAt,omitempty"`
	LastUpdatedAt   string `json:"lastUpdatedAt,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable,omitempty"`
	RemoteVersion   string `json:"remoteVersion,omitempty"`
}

type InstalledPackage struct {
	Installed InstalledPlugin
	Package   Package
	Warnings  []string
}

func IsValidName(name string) bool { return validName.MatchString(strings.TrimSpace(name)) }

func StatePath(WorkGround2Home string) string {
	return filepath.Join(WorkGround2Home, StateFilename)
}

func PluginsDir(WorkGround2Home string) string {
	return filepath.Join(WorkGround2Home, "plugins")
}

func InstallRoot(WorkGround2Home, name string) string {
	return filepath.Join(PluginsDir(WorkGround2Home), name)
}

func LoadState(WorkGround2Home string) (State, error) {
	var st State
	b, err := fileencoding.ReadFileUTF8(StatePath(WorkGround2Home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{Version: 1}, nil
		}
		return State{}, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, err
	}
	if st.Version == 0 {
		st.Version = 1
	}
	sort.SliceStable(st.Plugins, func(i, j int) bool { return st.Plugins[i].Name < st.Plugins[j].Name })
	return st, nil
}

func SaveState(WorkGround2Home string, st State) error {
	if st.Version == 0 {
		st.Version = 1
	}
	sort.SliceStable(st.Plugins, func(i, j int) bool { return st.Plugins[i].Name < st.Plugins[j].Name })
	if err := os.MkdirAll(WorkGround2Home, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(StatePath(WorkGround2Home), b, 0o644)
}

func Upsert(WorkGround2Home string, p InstalledPlugin) error {
	if !IsValidName(p.Name) {
		return fmt.Errorf("invalid plugin name %q", p.Name)
	}
	st, err := LoadState(WorkGround2Home)
	if err != nil {
		return err
	}
	for i := range st.Plugins {
		if st.Plugins[i].Name == p.Name {
			if p.InstalledAt == "" {
				p.InstalledAt = st.Plugins[i].InstalledAt
			}
			if p.InstalledAt == "" {
				p.InstalledAt = nowTimestamp()
			}
			st.Plugins[i] = p
			return SaveState(WorkGround2Home, st)
		}
	}
	if p.InstalledAt == "" {
		p.InstalledAt = nowTimestamp()
	}
	st.Plugins = append(st.Plugins, p)
	return SaveState(WorkGround2Home, st)
}

func Remove(WorkGround2Home, name string) (InstalledPlugin, bool, error) {
	st, err := LoadState(WorkGround2Home)
	if err != nil {
		return InstalledPlugin{}, false, err
	}
	for i, p := range st.Plugins {
		if p.Name != name {
			continue
		}
		st.Plugins = append(st.Plugins[:i], st.Plugins[i+1:]...)
		return p, true, SaveState(WorkGround2Home, st)
	}
	return InstalledPlugin{}, false, nil
}

func SetEnabled(WorkGround2Home, name string, enabled bool) error {
	st, err := LoadState(WorkGround2Home)
	if err != nil {
		return err
	}
	for i := range st.Plugins {
		if st.Plugins[i].Name == name {
			st.Plugins[i].Enabled = enabled
			return SaveState(WorkGround2Home, st)
		}
	}
	return fmt.Errorf("plugin %q is not installed", name)
}

func LoadInstalled(WorkGround2Home string) ([]InstalledPackage, []string) {
	st, err := LoadState(WorkGround2Home)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var out []InstalledPackage
	var warnings []string
	for _, installed := range st.Plugins {
		if !installed.Enabled {
			continue
		}
		root := ResolveRoot(WorkGround2Home, installed.Root)
		pkg, pkgWarnings, err := ParseDir(root)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", installed.Name, err))
			continue
		}
		out = append(out, InstalledPackage{Installed: installed, Package: pkg, Warnings: pkgWarnings})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Installed.Name < out[j].Installed.Name })
	return out, warnings
}

func ResolveRoot(WorkGround2Home, root string) string {
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	return filepath.Join(WorkGround2Home, filepath.Clean(root))
}

func RelativeRoot(WorkGround2Home, root string) string {
	if rel, err := filepath.Rel(WorkGround2Home, root); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return filepath.ToSlash(rel)
	}
	return filepath.Clean(root)
}

func ParseDir(root string) (Package, []string, error) {
	root = filepath.Clean(root)
	nativePath := filepath.Join(root, NativeManifest)
	if pkg, warnings, err := parseNative(nativePath, root); err == nil {
		return pkg, warnings, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Package{}, warnings, err
	}
	codexPath := filepath.Join(root, CodexManifest)
	if pkg, warnings, err := parseCodex(codexPath, root); err == nil {
		return pkg, warnings, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Package{}, warnings, err
	}
	return Package{}, nil, fmt.Errorf("no %s or %s found", NativeManifest, CodexManifest)
}

func parseNative(path, root string) (Package, []string, error) {
	var raw struct {
		Name        string               `json:"name"`
		Version     string               `json:"version"`
		Description string               `json:"description"`
		Homepage    string               `json:"homepage"`
		Repository  string               `json:"repository"`
		Skills      json.RawMessage      `json:"skills"`
		Hooks       map[string][]Hook    `json:"hooks"`
		MCPServers  map[string]MCPServer `json:"mcpServers"`
		AddOn       *AddOn               `json:"addon"`
	}
	if err := readJSONFile(path, &raw); err != nil {
		return Package{}, nil, err
	}
	skills, err := parseSkillPaths(raw.Skills)
	if err != nil {
		return Package{}, nil, err
	}
	manifest := Manifest{
		Name:        strings.TrimSpace(raw.Name),
		Version:     strings.TrimSpace(raw.Version),
		Description: strings.TrimSpace(raw.Description),
		Homepage:    strings.TrimSpace(raw.Homepage),
		Repository:  strings.TrimSpace(raw.Repository),
		Skills:      skills,
		Hooks:       normalizeHooks(raw.Hooks),
		MCPServers:  raw.MCPServers,
		AddOn:       normalizeAddOn(raw.AddOn),
	}
	if err := validateManifest(root, &manifest); err != nil {
		return Package{}, nil, err
	}
	return Package{Root: root, ManifestKind: "WorkGround2", Manifest: manifest}, nil, nil
}

func parseCodex(path, root string) (Package, []string, error) {
	var raw struct {
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Description string          `json:"description"`
		Homepage    string          `json:"homepage"`
		Repository  string          `json:"repository"`
		Skills      json.RawMessage `json:"skills"`
		AddOn       *AddOn          `json:"addon"`
	}
	if err := readJSONFile(path, &raw); err != nil {
		return Package{}, nil, err
	}
	skills, err := parseSkillPaths(raw.Skills)
	if err != nil {
		return Package{}, nil, err
	}
	manifest := Manifest{
		Name:        strings.TrimSpace(raw.Name),
		Version:     strings.TrimSpace(raw.Version),
		Description: strings.TrimSpace(raw.Description),
		Homepage:    strings.TrimSpace(raw.Homepage),
		Repository:  strings.TrimSpace(raw.Repository),
		Skills:      skills,
		AddOn:       normalizeAddOn(raw.AddOn),
	}
	var warnings []string
	hookPath := filepath.Join(root, "hooks", "session-start-codex")
	if info, err := os.Stat(hookPath); err == nil && info.Mode().IsRegular() {
		manifest.Hooks = map[string][]Hook{
			"SessionStart": {{
				Command:     hookPath,
				Cwd:         root,
				Description: "Codex-compatible session start hook from " + manifest.Name,
			}},
		}
	} else {
		warnings = append(warnings, "no hooks/session-start-codex convention hook found")
	}
	if err := validateManifest(root, &manifest); err != nil {
		return Package{}, warnings, err
	}
	return Package{Root: root, ManifestKind: "codex", Manifest: manifest}, warnings, nil
}

func readJSONFile(path string, v any) error {
	b, err := fileencoding.ReadFileUTF8(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func parseSkillPaths(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return cleanPathList([]string{one})
	}
	var manyStrings []string
	if err := json.Unmarshal(raw, &manyStrings); err == nil {
		return cleanPathList(manyStrings)
	}
	var manyObjects []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &manyObjects); err == nil {
		paths := make([]string, 0, len(manyObjects))
		for _, item := range manyObjects {
			paths = append(paths, item.Path)
		}
		return cleanPathList(paths)
	}
	return nil, fmt.Errorf("skills must be a path string, string array, or object array")
}

func cleanPathList(paths []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "." || p == "" {
			p = "."
		}
		if filepath.IsAbs(p) || strings.HasPrefix(p, ".."+string(filepath.Separator)) || p == ".." {
			return nil, fmt.Errorf("plugin path %q must be relative and stay inside the plugin root", p)
		}
		slash := filepath.ToSlash(p)
		if !seen[slash] {
			seen[slash] = true
			out = append(out, slash)
		}
	}
	sort.Strings(out)
	return out, nil
}

func normalizeHooks(in map[string][]Hook) map[string][]Hook {
	if len(in) == 0 {
		return nil
	}
	out := map[string][]Hook{}
	for event, hooks := range in {
		event = strings.TrimSpace(event)
		for _, h := range hooks {
			h.Command = strings.TrimSpace(h.Command)
			h.Cwd = strings.TrimSpace(h.Cwd)
			if h.Command == "" {
				continue
			}
			out[event] = append(out[event], h)
		}
	}
	return out
}

func normalizeAddOn(in *AddOn) *AddOn {
	if in == nil {
		return nil
	}
	in.Kind = strings.TrimSpace(in.Kind)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.ConfigSchema = filepath.ToSlash(filepath.Clean(strings.TrimSpace(in.ConfigSchema)))
	if in.ConfigSchema == "." {
		in.ConfigSchema = ""
	}
	in.Capabilities = cleanStringList(in.Capabilities)
	for i := range in.Panels {
		in.Panels[i].ID = strings.TrimSpace(in.Panels[i].ID)
		in.Panels[i].Title = strings.TrimSpace(in.Panels[i].Title)
		in.Panels[i].Entry = filepath.ToSlash(filepath.Clean(strings.TrimSpace(in.Panels[i].Entry)))
		if in.Panels[i].Entry == "." {
			in.Panels[i].Entry = ""
		}
	}
	for i := range in.Secrets {
		in.Secrets[i].ID = strings.TrimSpace(in.Secrets[i].ID)
		in.Secrets[i].Label = strings.TrimSpace(in.Secrets[i].Label)
		in.Secrets[i].Purpose = strings.TrimSpace(in.Secrets[i].Purpose)
	}
	if in.Runtime != nil {
		in.Runtime.Type = strings.TrimSpace(in.Runtime.Type)
		in.Runtime.MCPServer = strings.TrimSpace(in.Runtime.MCPServer)
	}
	if in.Update != nil {
		in.Update.Type = strings.TrimSpace(in.Update.Type)
		in.Update.Strategy = strings.TrimSpace(in.Update.Strategy)
		in.Update.Check = strings.TrimSpace(in.Update.Check)
		in.Update.Credential = strings.TrimSpace(in.Update.Credential)
	}
	if in.Storage != nil {
		in.Storage.Namespace = strings.TrimSpace(in.Storage.Namespace)
	}
	if in.Kind == "" && in.DisplayName == "" && len(in.Capabilities) == 0 && len(in.Panels) == 0 &&
		in.ConfigSchema == "" && len(in.Secrets) == 0 && in.Runtime == nil && in.Update == nil && in.Storage == nil {
		return nil
	}
	return in
}

func cleanStringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func validateManifest(root string, m *Manifest) error {
	if !IsValidName(m.Name) {
		return fmt.Errorf("invalid plugin name %q", m.Name)
	}
	for _, p := range m.Skills {
		if err := validateRelativePath(p); err != nil {
			return err
		}
	}
	for event, hooks := range m.Hooks {
		if strings.TrimSpace(event) == "" {
			return fmt.Errorf("hook event is required")
		}
		for _, h := range hooks {
			if h.Command == "" {
				return fmt.Errorf("hook command is required")
			}
			if !filepath.IsAbs(h.Command) {
				if err := validateRelativePath(h.Command); err != nil {
					return err
				}
			}
			if h.Cwd != "" && !filepath.IsAbs(h.Cwd) {
				if err := validateRelativePath(h.Cwd); err != nil {
					return err
				}
			}
		}
	}
	for name := range m.MCPServers {
		if !IsValidName(name) {
			return fmt.Errorf("invalid MCP server name %q", name)
		}
	}
	if err := validateAddOn(m.AddOn); err != nil {
		return err
	}
	if m.AddOn != nil && m.AddOn.Runtime != nil && strings.EqualFold(m.AddOn.Runtime.Type, "mcp") {
		if m.AddOn.Runtime.MCPServer == "" {
			return fmt.Errorf("addon runtime mcpServer is required")
		}
		if _, ok := m.MCPServers[m.AddOn.Runtime.MCPServer]; !ok {
			return fmt.Errorf("addon runtime mcpServer %q is not declared in mcpServers", m.AddOn.Runtime.MCPServer)
		}
	}
	if _, err := os.Stat(root); err != nil {
		return err
	}
	return nil
}

func validateAddOn(addon *AddOn) error {
	if addon == nil {
		return nil
	}
	if addon.Kind != "" && !IsValidName(addon.Kind) {
		return fmt.Errorf("invalid addon kind %q", addon.Kind)
	}
	if addon.ConfigSchema != "" {
		if err := validateRelativePath(addon.ConfigSchema); err != nil {
			return fmt.Errorf("addon configSchema: %w", err)
		}
	}
	for _, panel := range addon.Panels {
		if panel.ID == "" {
			return fmt.Errorf("addon panel id is required")
		}
		if !IsValidName(panel.ID) {
			return fmt.Errorf("invalid addon panel id %q", panel.ID)
		}
		if panel.Entry == "" {
			return fmt.Errorf("addon panel %q entry is required", panel.ID)
		}
		if err := validateRelativePath(panel.Entry); err != nil {
			return fmt.Errorf("addon panel %q entry: %w", panel.ID, err)
		}
	}
	for _, secret := range addon.Secrets {
		if secret.ID == "" {
			return fmt.Errorf("addon secret id is required")
		}
		if !IsValidName(secret.ID) {
			return fmt.Errorf("invalid addon secret id %q", secret.ID)
		}
	}
	if addon.Runtime != nil {
		switch strings.ToLower(addon.Runtime.Type) {
		case "", "builtin", "mcp":
		default:
			return fmt.Errorf("invalid addon runtime type %q", addon.Runtime.Type)
		}
		if addon.Runtime.MCPServer != "" && !IsValidName(addon.Runtime.MCPServer) {
			return fmt.Errorf("invalid addon runtime mcpServer %q", addon.Runtime.MCPServer)
		}
	}
	if addon.Update != nil && addon.Update.Credential != "" && !IsValidName(addon.Update.Credential) {
		return fmt.Errorf("invalid addon update credential %q", addon.Update.Credential)
	}
	if addon.Storage != nil && addon.Storage.Namespace != "" && !IsValidName(addon.Storage.Namespace) {
		return fmt.Errorf("invalid addon storage namespace %q", addon.Storage.Namespace)
	}
	return nil
}

func validateRelativePath(p string) error {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == "" {
		return fmt.Errorf("plugin path is required")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, ".."+string(filepath.Separator)) || p == ".." {
		return fmt.Errorf("plugin path %q must be relative and stay inside the plugin root", p)
	}
	return nil
}

func (p Package) SkillRoots() []string {
	var out []string
	for _, rel := range p.Manifest.Skills {
		out = append(out, filepath.Join(p.Root, filepath.FromSlash(rel)))
	}
	sort.Strings(out)
	return out
}

func (p Package) CapabilityCounts() (skills, hooks, mcp int) {
	skills = len(p.Manifest.Skills)
	for _, hs := range p.Manifest.Hooks {
		hooks += len(hs)
	}
	mcp = len(p.Manifest.MCPServers)
	return
}

func (p Package) AddOnCounts() (panels, secrets int) {
	if p.Manifest.AddOn == nil {
		return 0, 0
	}
	return len(p.Manifest.AddOn.Panels), len(p.Manifest.AddOn.Secrets)
}

func nowTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
