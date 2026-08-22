package pluginpkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	fileencoding "workground2/internal/fileutil/encoding"

	"gopkg.in/yaml.v3"
)

var errNotDSHBundle = errors.New("package is not a DSH bundle")

// DshBundle is the statically inspected DSH bundle declaration. JavaScript
// expressions in the Cordis patch are never evaluated while installing.
type DshBundle struct {
	PackageName string          `json:"packageName"`
	Patch       string          `json:"patch"`
	Rows        []DshRow        `json:"rows,omitempty"`
	Report      DshCompatReport `json:"report"`
}

// DshRow is one named Cordis plugin row discovered in an insert patch.
type DshRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Client      bool   `json:"client,omitempty"`
	Resolved    bool   `json:"resolved,omitempty"`
	PackageRoot string `json:"packageRoot,omitempty"`
	ClientEntry string `json:"clientEntry,omitempty"`
}

// DshCompatReport is evidence from static inspection, not a runtime success
// claim. Runtime stages can raise the verified compatibility level later.
type DshCompatReport struct {
	Level           string   `json:"level"`
	Status          string   `json:"status"`
	Rows            int      `json:"rows"`
	ResolvedRows    int      `json:"resolvedRows"`
	ClientRows      int      `json:"clientRows"`
	DynamicValues   int      `json:"dynamicValues"`
	OverridePatches int      `json:"overridePatches"`
	MissingPackages []string `json:"missingPackages,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

type dshPackageJSON struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Homepage    string          `json:"homepage"`
	Repository  json.RawMessage `json:"repository"`
	DSH         struct {
		Bundle *struct {
			Patch string `json:"patch"`
		} `json:"bundle"`
		Client json.RawMessage `json:"client"`
	} `json:"dsh"`
}

// DSHPatchPath reads a package.json body and returns its validated DSH bundle
// patch path. ok is false for an ordinary npm package.
func DSHPatchPath(body []byte) (patch string, ok bool, err error) {
	var raw dshPackageJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", false, err
	}
	if raw.DSH.Bundle == nil || strings.TrimSpace(raw.DSH.Bundle.Patch) == "" {
		return "", false, nil
	}
	patch = filepath.Clean(strings.TrimSpace(raw.DSH.Bundle.Patch))
	if err := validateRelativePath(patch); err != nil {
		return "", true, fmt.Errorf("dsh.bundle.patch: %w", err)
	}
	return filepath.ToSlash(patch), true, nil
}

func parseDSH(path, root string) (Package, []string, error) {
	var raw dshPackageJSON
	if err := readJSONFile(path, &raw); err != nil {
		return Package{}, nil, err
	}
	if raw.DSH.Bundle == nil || strings.TrimSpace(raw.DSH.Bundle.Patch) == "" {
		return Package{}, nil, errNotDSHBundle
	}
	patch := filepath.Clean(strings.TrimSpace(raw.DSH.Bundle.Patch))
	if err := validateRelativePath(patch); err != nil {
		return Package{}, nil, fmt.Errorf("dsh.bundle.patch: %w", err)
	}
	patchPath := filepath.Join(root, patch)
	info, err := os.Stat(patchPath)
	if err != nil {
		return Package{}, nil, fmt.Errorf("dsh.bundle.patch %q: %w", filepath.ToSlash(patch), err)
	}
	if !info.Mode().IsRegular() {
		return Package{}, nil, fmt.Errorf("dsh.bundle.patch %q must be a regular file", filepath.ToSlash(patch))
	}
	rows, report, err := parseDSHPatch(root, patchPath)
	if err != nil {
		return Package{}, nil, fmt.Errorf("dsh.bundle.patch %q: %w", filepath.ToSlash(patch), err)
	}
	name := dshInstallName(raw.Name)
	manifest := Manifest{
		Name:        name,
		Version:     strings.TrimSpace(raw.Version),
		Description: strings.TrimSpace(raw.Description),
		Homepage:    strings.TrimSpace(raw.Homepage),
		Repository:  dshRepository(raw.Repository),
		DSH: &DshBundle{
			PackageName: strings.TrimSpace(raw.Name),
			Patch:       filepath.ToSlash(patch),
			Rows:        rows,
			Report:      report,
		},
	}
	if err := validateManifest(root, &manifest); err != nil {
		return Package{}, report.Warnings, err
	}
	return Package{Root: root, ManifestKind: "dsh", Manifest: manifest}, append([]string(nil), report.Warnings...), nil
}

func parseDSHPatch(root, path string) ([]DshRow, DshCompatReport, error) {
	b, err := fileencoding.ReadFileUTF8(path)
	if err != nil {
		return nil, DshCompatReport{}, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, DshCompatReport{}, err
	}
	report := DshCompatReport{Level: "L1", Status: "recognized"}
	report.DynamicValues = countYAMLTag(&doc, "!!js")
	rootNode := yamlDocumentValue(&doc)
	if rootNode == nil || rootNode.Kind != yaml.SequenceNode {
		return nil, report, fmt.Errorf("top-level patch must be a YAML sequence")
	}
	var rows []DshRow
	seenIDs := map[string]bool{}
	missing := map[string]bool{}
	for _, patch := range rootNode.Content {
		if patch.Kind != yaml.MappingNode {
			return nil, report, fmt.Errorf("patch item must be a mapping")
		}
		insert := yamlMapValue(patch, "insert")
		if insert == nil {
			if yamlScalar(yamlMapValue(patch, "id")) != "" {
				report.OverridePatches++
			}
			continue
		}
		if insert.Kind != yaml.SequenceNode {
			return nil, report, fmt.Errorf("insert must be a sequence")
		}
		for _, item := range insert.Content {
			if item.Kind != yaml.MappingNode {
				return nil, report, fmt.Errorf("insert row must be a mapping")
			}
			id := strings.TrimSpace(yamlScalar(yamlMapValue(item, "id")))
			name := strings.TrimSpace(yamlScalar(yamlMapValue(item, "name")))
			if id == "" || name == "" {
				return nil, report, fmt.Errorf("insert row requires id and name")
			}
			if seenIDs[id] {
				return nil, report, fmt.Errorf("duplicate inserted row id %q", id)
			}
			seenIDs[id] = true
			row := DshRow{ID: id, Name: name}
			if packageRoot, ok := resolveNodePackage(root, name); ok {
				row.Resolved = true
				row.PackageRoot = packageRoot
				row.Client, row.ClientEntry = inspectDSHClient(packageRoot)
				report.ResolvedRows++
				if row.Client {
					report.ClientRows++
				}
			} else {
				missing[name] = true
			}
			rows = append(rows, row)
		}
	}
	report.Rows = len(rows)
	for name := range missing {
		report.MissingPackages = append(report.MissingPackages, name)
	}
	sort.Strings(report.MissingPackages)
	if len(report.MissingPackages) > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d DSH package(s) are not resolvable from the bundle root; runtime may require npm dependencies", len(report.MissingPackages)))
	}
	if report.DynamicValues > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d DSH !!js value(s) were detected and left unevaluated during install", report.DynamicValues))
	}
	if report.Rows == 0 && report.OverridePatches > 0 {
		report.Warnings = append(report.Warnings, "bundle patch only overrides existing rows and requires an earlier bundle layer")
	}
	return rows, report, nil
}

func dshInstallName(packageName string) string {
	name := strings.TrimSpace(packageName)
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	name = strings.TrimLeft(name, "@")
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-'
		if valid && r <= unicode.MaxASCII {
			b.WriteRune(r)
			lastDash = r == '-'
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	name = strings.Trim(b.String(), "._- ")
	if name == "" {
		name = "dsh-bundle"
	}
	if len(name) > 64 {
		name = strings.TrimRight(name[:64], "._- ")
	}
	return name
}

func dshRepository(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	var object struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return strings.TrimSpace(object.URL)
	}
	return ""
}

func resolveNodePackage(root, name string) (string, bool) {
	packageName, ok := nodePackageName(name)
	if !ok {
		return "", false
	}
	for dir := filepath.Clean(root); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "node_modules", filepath.FromSlash(packageName))
		if info, err := os.Stat(filepath.Join(candidate, DSHManifest)); err == nil && info.Mode().IsRegular() {
			return filepath.Clean(candidate), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", false
}

func nodePackageName(specifier string) (string, bool) {
	specifier = strings.TrimSpace(strings.ReplaceAll(specifier, "\\", "/"))
	parts := strings.Split(specifier, "/")
	if strings.HasPrefix(specifier, "@") {
		if len(parts) < 2 || len(parts[0]) < 2 || !IsValidName(strings.TrimPrefix(parts[0], "@")) || !IsValidName(parts[1]) {
			return "", false
		}
		return parts[0] + "/" + parts[1], true
	}
	if len(parts) == 0 || !IsValidName(parts[0]) {
		return "", false
	}
	return parts[0], true
}

func inspectDSHClient(root string) (bool, string) {
	var raw dshPackageJSON
	if readJSONFile(filepath.Join(root, DSHManifest), &raw) != nil || len(raw.DSH.Client) == 0 || string(raw.DSH.Client) == "null" {
		return false, ""
	}
	var exports map[string]json.RawMessage
	var packageRaw struct {
		Exports json.RawMessage `json:"exports"`
	}
	if readJSONFile(filepath.Join(root, DSHManifest), &packageRaw) == nil && json.Unmarshal(packageRaw.Exports, &exports) == nil {
		if _, ok := exports["./client"]; ok {
			return true, "./client"
		}
	}
	return true, ""
}

func yamlDocumentValue(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	return node
}

func yamlMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func yamlScalar(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func countYAMLTag(node *yaml.Node, tag string) int {
	if node == nil {
		return 0
	}
	count := 0
	if node.Tag == tag {
		count++
	}
	for _, child := range node.Content {
		count += countYAMLTag(child, tag)
	}
	return count
}
