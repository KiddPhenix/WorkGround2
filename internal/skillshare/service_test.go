package skillshare

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"workground2/internal/pluginpkg"
	"workground2/internal/skill"
)

func TestSaveIdempotentAndStripsGitURLSecret(t *testing.T) {
	svc := New(t.TempDir())
	ctx := context.Background()
	input := ProfileInput{
		ID:          "team-skills",
		Enabled:     true,
		DisplayName: "Team Skills",
		GitURL:      "https://alice:secret@example.com/team/skills.git",
		Path:        ".",
	}

	if _, err := svc.Save(ctx, input); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	view, err := svc.Save(ctx, input)
	if err != nil {
		t.Fatalf("Save second: %v", err)
	}
	if view.GitURL != "https://example.com/team/skills.git" || view.Username != "alice" {
		t.Fatalf("view git fields = %#v", view)
	}

	profiles, err := svc.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %#v, want one profile", profiles)
	}
	raw, err := os.ReadFile(filepath.Join(svc.rootDir(), "profiles.json"))
	if err != nil {
		t.Fatalf("read profiles.json: %v", err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "alice:") {
		t.Fatalf("profiles.json leaked URL credential: %s", raw)
	}
}

func TestSyncLocalRepoRegistersPluginPackage(t *testing.T) {
	requireGit(t)
	repo := newSkillRepo(t, "team-skills", "1.0.0")
	home := t.TempDir()
	svc := New(home)
	ctx := context.Background()

	if _, err := svc.Save(ctx, ProfileInput{ID: "team", Enabled: true, GitURL: repo, Path: "."}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	task, err := svc.Sync(ctx, "team", SyncOptions{})
	if err != nil {
		t.Fatalf("Sync: %v\n%+v", err, task)
	}
	if task.Status != TaskSucceeded || task.CurrentRevision == "" {
		t.Fatalf("task = %+v", task)
	}

	st, err := pluginpkg.LoadState(home)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(st.Plugins) != 1 {
		t.Fatalf("plugins = %+v", st.Plugins)
	}
	got := st.Plugins[0]
	if got.Name != "team-skills" || !got.Enabled || got.Version != "1.0.0" {
		t.Fatalf("installed plugin = %+v", got)
	}
	if !strings.Contains(filepath.ToSlash(got.Root), "addons/skill-share/profiles/team/active") {
		t.Fatalf("plugin root = %q, want active checkout path", got.Root)
	}

	profiles, err := svc.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(profiles) != 1 || profiles[0].State.Status != StatusReady || profiles[0].PluginName != "team-skills" {
		t.Fatalf("profile = %+v", profiles)
	}
}

func TestMultipleSourcesSyncAndDeleteIndependently(t *testing.T) {
	requireGit(t)
	alphaRepo := newSkillRepo(t, "alpha-skills", "1.0.0")
	betaRepo := newSkillRepo(t, "beta-skills", "2.0.0")
	home := t.TempDir()
	svc := New(home)
	ctx := context.Background()

	if _, err := svc.Save(ctx, ProfileInput{ID: "alpha-source", Enabled: true, GitURL: alphaRepo, Path: "."}); err != nil {
		t.Fatalf("Save alpha: %v", err)
	}
	if _, err := svc.Save(ctx, ProfileInput{ID: "beta-source", Enabled: true, GitURL: betaRepo, Path: "."}); err != nil {
		t.Fatalf("Save beta: %v", err)
	}
	profiles, err := svc.Profiles()
	if err != nil {
		t.Fatalf("Profiles after saves: %v", err)
	}
	if len(profiles) != 2 || !profiles[0].Enabled || !profiles[1].Enabled {
		t.Fatalf("profiles after saves = %+v, want two enabled sources", profiles)
	}

	if task, err := svc.Sync(ctx, "alpha-source", SyncOptions{}); err != nil {
		t.Fatalf("Sync alpha: %v\n%+v", err, task)
	}
	st, err := pluginpkg.LoadState(home)
	if err != nil {
		t.Fatalf("LoadState after alpha sync: %v", err)
	}
	assertPluginNames(t, st.Plugins, []string{"alpha-skills"})

	profiles, err = svc.Profiles()
	if err != nil {
		t.Fatalf("Profiles after alpha sync: %v", err)
	}
	for _, profile := range profiles {
		if profile.ID == "beta-source" && profile.State.Status != StatusUnconfigured {
			t.Fatalf("beta source changed after alpha sync: %+v", profile)
		}
	}

	if task, err := svc.Sync(ctx, "beta-source", SyncOptions{}); err != nil {
		t.Fatalf("Sync beta: %v\n%+v", err, task)
	}
	st, err = pluginpkg.LoadState(home)
	if err != nil {
		t.Fatalf("LoadState after beta sync: %v", err)
	}
	assertPluginNames(t, st.Plugins, []string{"alpha-skills", "beta-skills"})

	if view, err := svc.Delete(ctx, "alpha-source", DeleteOptions{}); err != nil {
		t.Fatalf("Delete alpha: %v\n%+v", err, view)
	}
	st, err = pluginpkg.LoadState(home)
	if err != nil {
		t.Fatalf("LoadState after alpha delete: %v", err)
	}
	assertPluginNames(t, st.Plugins, []string{"beta-skills"})

	profiles, err = svc.Profiles()
	if err != nil {
		t.Fatalf("Profiles after alpha delete: %v", err)
	}
	if len(profiles) != 1 || profiles[0].ID != "beta-source" || profiles[0].State.Status != StatusReady {
		t.Fatalf("profiles after alpha delete = %+v, want beta source ready", profiles)
	}
	if _, err := os.Stat(filepath.Join(home, "addons", "skill-share", "profiles", "beta-source", "active")); err != nil {
		t.Fatalf("beta active checkout missing after alpha delete: %v", err)
	}
}

func TestFlowSyncUsesRemoteSkillsWithoutActiveCheckout(t *testing.T) {
	var skillHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repo/WorkGround2-plugin.json":
			w.Header().Set("ETag", `"manifest-v1"`)
			_, _ = w.Write([]byte(`{"name":"flow-skills","version":"1.0.0","skills":"skills"}` + "\n"))
		case "/repo/skills/.flow-skill-index.json":
			_, _ = w.Write([]byte(`{"files":["demo/SKILL.md"]}` + "\n"))
		case "/repo/skills/demo/SKILL.md":
			atomic.AddInt32(&skillHits, 1)
			w.Header().Set("ETag", `"skill-v1"`)
			_, _ = w.Write([]byte("---\nname: demo\ndescription: Remote demo\n---\n\n# Demo\n\nFetched on demand.\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	svc := NewFlow(home)
	ctx := context.Background()
	if _, err := svc.Save(ctx, ProfileInput{ID: "team", Enabled: true, GitURL: server.URL + "/repo", Path: "."}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	task, err := svc.Sync(ctx, "team", SyncOptions{})
	if err != nil {
		t.Fatalf("Sync: %v\n%+v", err, task)
	}
	if _, err := os.Stat(filepath.Join(home, "addons", "flow-skill-share", "profiles", "team", "active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("flow sync created active checkout or unexpected stat error: %v", err)
	}
	profiles, err := svc.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(profiles) != 1 || profiles[0].State.Status != StatusReady || profiles[0].Skills != 1 {
		t.Fatalf("profile = %+v", profiles)
	}

	atomic.StoreInt32(&skillHits, 0)
	provider := svc.Provider()
	first, ok := provider.Read("demo")
	if !ok {
		t.Fatal("remote skill demo not readable")
	}
	second, ok := provider.Read("demo")
	if !ok {
		t.Fatal("remote skill demo not readable on second read")
	}
	if first.Body != second.Body || !strings.Contains(first.Body, "Fetched on demand.") {
		t.Fatalf("remote skill bodies = %q / %q", first.Body, second.Body)
	}
	if !first.IsProtected() || !first.AntiLeak || first.SourceKind != skill.SourceFlowSkillShare {
		t.Fatalf("remote skill should default protected anti-leak source kind, got protected=%v antiLeak=%v source=%q", first.IsProtected(), first.AntiLeak, first.SourceKind)
	}
	if got := atomic.LoadInt32(&skillHits); got < 2 {
		t.Fatalf("Provider.Read should fetch remote content each time, skill hits = %d", got)
	}
}

func TestGitHubArchiveListingSkipsPlainMarkdown(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{
		"testskillshare-main/skills/demo/SKILL.md",
		"testskillshare-main/skills/README.md",
		"testskillshare-main/skills/scripts/helper/SKILL.md",
		"testskillshare-main/docs/other/SKILL.md",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close zip: %v", err)
	}

	files, err := listArchiveSkillFiles(buf.Bytes(), "", "skills")
	if err != nil {
		t.Fatalf("listArchiveSkillFiles: %v", err)
	}
	want := []string{"skills/demo/SKILL.md"}
	if strings.Join(files, "\n") != strings.Join(want, "\n") {
		t.Fatalf("files = %+v, want %+v", files, want)
	}
}

func TestRemoteDirectoryScanSkipsPlainMarkdown(t *testing.T) {
	if !isRemoteDirectorySkillFile("skills/demo/SKILL.md") {
		t.Fatal("SKILL.md should be accepted during directory scans")
	}
	if isRemoteDirectorySkillFile("skills/README.md") {
		t.Fatal("plain markdown should not be accepted during directory scans")
	}
	if !isRemoteSkillFile("skills/demo.md") {
		t.Fatal("explicit markdown file declarations should remain accepted")
	}
}

func TestManifestFailureKeepsOldActive(t *testing.T) {
	requireGit(t)
	repo := newSkillRepo(t, "team-skills", "1.0.0")
	home := t.TempDir()
	svc := New(home)
	ctx := context.Background()

	if _, err := svc.Save(ctx, ProfileInput{ID: "team", Enabled: true, GitURL: repo, Path: "."}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if task, err := svc.Sync(ctx, "team", SyncOptions{}); err != nil {
		t.Fatalf("initial Sync: %v\n%+v", err, task)
	}
	activeManifest := filepath.Join(home, "addons", "skill-share", "profiles", "team", "active", pluginpkg.NativeManifest)
	before, err := os.ReadFile(activeManifest)
	if err != nil {
		t.Fatalf("read active manifest: %v", err)
	}
	if !strings.Contains(string(before), `"version":"1.0.0"`) {
		t.Fatalf("initial active manifest = %s", before)
	}

	writeFile(t, filepath.Join(repo, pluginpkg.NativeManifest), `{"name":"bad name","skills":"skills"}`+"\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "bad manifest")

	task, err := svc.Sync(ctx, "team", SyncOptions{})
	if err == nil {
		t.Fatalf("Sync should fail for invalid manifest; task=%+v", task)
	}
	after, err := os.ReadFile(activeManifest)
	if err != nil {
		t.Fatalf("read active manifest after failed sync: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("active changed after failed sync\nbefore: %s\nafter: %s", before, after)
	}

	profiles, err := svc.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if profiles[0].State.Status != StatusUpdateFailed || profiles[0].State.LastError == "" {
		t.Fatalf("profile state = %+v", profiles[0].State)
	}
}

func TestDeleteIsRepeatable(t *testing.T) {
	requireGit(t)
	repo := newSkillRepo(t, "team-skills", "1.0.0")
	home := t.TempDir()
	svc := New(home)
	ctx := context.Background()

	if _, err := svc.Save(ctx, ProfileInput{ID: "team", Enabled: true, GitURL: repo, Path: "."}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if task, err := svc.Sync(ctx, "team", SyncOptions{}); err != nil {
		t.Fatalf("Sync: %v\n%+v", err, task)
	}
	if view, err := svc.Delete(ctx, "team", DeleteOptions{}); err != nil {
		t.Fatalf("Delete first: %v\n%+v", err, view)
	}
	if view, err := svc.Delete(ctx, "team", DeleteOptions{}); err != nil {
		t.Fatalf("Delete second: %v\n%+v", err, view)
	}

	profiles, err := svc.Profiles()
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profiles after delete = %+v", profiles)
	}
	st, err := pluginpkg.LoadState(home)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(st.Plugins) != 0 {
		t.Fatalf("plugins after delete = %+v", st.Plugins)
	}
	if _, err := os.Stat(filepath.Join(home, "addons", "skill-share", "profiles", "team")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed profile dir still exists or unexpected stat error: %v", err)
	}
}

func TestSanitizeErrorRedactsSecrets(t *testing.T) {
	got := sanitizeError(
		errors.New("fatal: https://alice:s3cr3t@example.com/repo.git failed with token=s3cr3t access_token=abc123"),
		"s3cr3t",
		"abc123",
	)
	for _, leak := range []string{"s3cr3t", "abc123", "alice:"} {
		if strings.Contains(got, leak) {
			t.Fatalf("sanitizeError leaked %q in %q", leak, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("sanitizeError did not mark redaction: %q", got)
	}
}

func newSkillRepo(t *testing.T, name, version string) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init")
	gitRun(t, repo, "config", "user.email", "test@example.com")
	gitRun(t, repo, "config", "user.name", "Test User")
	writeFile(t, filepath.Join(repo, pluginpkg.NativeManifest), `{"name":"`+name+`","version":"`+version+`","skills":"skills"}`+"\n")
	writeFile(t, filepath.Join(repo, "skills", "demo", "SKILL.md"), "---\nname: demo\n---\n# Demo\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	return repo
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertPluginNames(t *testing.T, plugins []pluginpkg.InstalledPlugin, want []string) {
	t.Helper()
	got := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		got = append(got, plugin.Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("plugin names = %v, want %v", got, want)
	}
}
