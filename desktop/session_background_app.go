package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	wails "github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/config"
)

const sessionBackgroundSelectionLimit = 256

var sessionBackgroundMIMEs = map[string]string{
	".bmp":  "image/bmp",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

type SessionBackgroundSourceView struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Enabled    bool   `json:"enabled"`
	Recursive  bool   `json:"recursive"`
	ImageCount int    `json:"imageCount"`
	Error      string `json:"error,omitempty"`
}

type SessionBackgroundSettingsView struct {
	Enabled       bool                          `json:"enabled"`
	MaskEnabled   bool                          `json:"maskEnabled"`
	RandomOnOpen  bool                          `json:"randomOnOpen"`
	RotateSeconds int                           `json:"rotateSeconds"`
	ImageCount    int                           `json:"imageCount"`
	Sources       []SessionBackgroundSourceView `json:"sources"`
}

type SessionBackgroundImageView struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

type sessionBackgroundImage struct {
	path string
	name string
	mime string
}

type sessionBackgroundCatalog struct {
	key     string
	images  []sessionBackgroundImage
	sources []SessionBackgroundSourceView
}

type sessionBackgroundService struct {
	mu       sync.Mutex
	catalog  sessionBackgroundCatalog
	selected map[string]string
	order    []string
	choose   func(int) int
}

func newSessionBackgroundService() *sessionBackgroundService {
	return &sessionBackgroundService{
		selected: map[string]string{},
		choose: func(n int) int {
			if n <= 1 {
				return 0
			}
			value, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
			if err != nil {
				return 0
			}
			return int(value.Int64())
		},
	}
}

func (a *App) ensureSessionBackground() *sessionBackgroundService {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.background == nil {
		a.background = newSessionBackgroundService()
	}
	return a.background
}

func sessionBackgroundConfigView(bg config.DesktopSessionBackgroundConfig) SessionBackgroundSettingsView {
	view := SessionBackgroundSettingsView{
		Enabled:       bg.Enabled,
		MaskEnabled:   bg.MaskEnabled == nil || *bg.MaskEnabled,
		RandomOnOpen:  bg.RandomOnOpen == nil || *bg.RandomOnOpen,
		RotateSeconds: bg.RotateSeconds,
		Sources:       make([]SessionBackgroundSourceView, 0, len(bg.Sources)),
	}
	for _, source := range bg.Sources {
		view.Sources = append(view.Sources, SessionBackgroundSourceView{
			Kind:      source.Kind,
			Path:      source.Path,
			Enabled:   source.Enabled == nil || *source.Enabled,
			Recursive: source.Recursive,
		})
	}
	return view
}

func sessionBackgroundConfigFromView(view SessionBackgroundSettingsView) config.DesktopSessionBackgroundConfig {
	mask, randomOnOpen := view.MaskEnabled, view.RandomOnOpen
	bg := config.DesktopSessionBackgroundConfig{
		Enabled:       view.Enabled,
		MaskEnabled:   &mask,
		RandomOnOpen:  &randomOnOpen,
		RotateSeconds: view.RotateSeconds,
		Sources:       make([]config.DesktopSessionBackgroundSource, 0, len(view.Sources)),
	}
	for _, source := range view.Sources {
		enabled := source.Enabled
		bg.Sources = append(bg.Sources, config.DesktopSessionBackgroundSource{
			Kind:      source.Kind,
			Path:      source.Path,
			Enabled:   &enabled,
			Recursive: source.Recursive,
		})
	}
	return bg
}

// SessionBackgroundSettings reuses the last source scan when possible, keeping
// ordinary session renders independent of repeated filesystem walks.
func (a *App) SessionBackgroundSettings() (SessionBackgroundSettingsView, error) {
	return a.sessionBackgroundSettings(false)
}

// RefreshSessionBackgroundSettings forces a source rescan for the Appearance
// page without making every tab activation walk configured folders.
func (a *App) RefreshSessionBackgroundSettings() (SessionBackgroundSettingsView, error) {
	return a.sessionBackgroundSettings(true)
}

func (a *App) sessionBackgroundSettings(force bool) (SessionBackgroundSettingsView, error) {
	cfg, _, err := a.loadDesktopUserConfigForView()
	if err != nil {
		return SessionBackgroundSettingsView{}, err
	}
	bg := cfg.DesktopSessionBackground()
	catalog := a.ensureSessionBackground().load(bg, force)
	view := sessionBackgroundConfigView(bg)
	view.ImageCount = len(catalog.images)
	view.Sources = append([]SessionBackgroundSourceView(nil), catalog.sources...)
	return view, nil
}

// SetSessionBackgroundSettings atomically persists the complete UI-only
// preference, then invalidates every runtime choice so the visible Session can
// resolve against the new pool immediately.
func (a *App) SetSessionBackgroundSettings(view SessionBackgroundSettingsView) error {
	if err := a.applyConfigOnly(func(c *config.Config) error {
		return c.SetDesktopSessionBackground(sessionBackgroundConfigFromView(view))
	}); err != nil {
		return err
	}
	a.ensureSessionBackground().invalidate()
	if a.ctx != nil {
		wails.EventsEmit(a.ctx, "session-background:changed")
	}
	return nil
}

func (a *App) PickSessionBackgroundFiles() ([]string, error) {
	if a.ctx == nil {
		return []string{}, nil
	}
	paths, err := wails.OpenMultipleFilesDialog(a.ctx, wails.OpenDialogOptions{
		Title:   "Choose Session background images",
		Filters: []wails.FileFilter{{DisplayName: "Images (*.png;*.jpg;*.jpeg;*.webp;*.bmp)", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.bmp"}},
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			out = append(out, filepath.Clean(path))
		}
	}
	return out, nil
}

func (a *App) PickSessionBackgroundFolder() (string, error) {
	if a.ctx == nil {
		return "", nil
	}
	dir, err := wails.OpenDirectoryDialog(a.ctx, wails.OpenDialogOptions{Title: "Choose Session background folder"})
	if err != nil || strings.TrimSpace(dir) == "" {
		return "", err
	}
	return filepath.Clean(dir), nil
}

// SessionBackground is idempotent for one open tab: repeated reads keep the
// selected path while issuing a fresh short-lived media URL.
func (a *App) SessionBackground(tabID string) (SessionBackgroundImageView, error) {
	return a.resolveSessionBackground(tabID, false)
}

func (a *App) RotateSessionBackground(tabID string) (SessionBackgroundImageView, error) {
	return a.resolveSessionBackground(tabID, true)
}

func (a *App) resolveSessionBackground(tabID string, rotate bool) (SessionBackgroundImageView, error) {
	tabID = strings.TrimSpace(tabID)
	if tabID == "" {
		return SessionBackgroundImageView{}, fmt.Errorf("Session background tab id is empty")
	}
	cfg, _, err := a.loadDesktopUserConfigForView()
	if err != nil {
		return SessionBackgroundImageView{}, err
	}
	bg := cfg.DesktopSessionBackground()
	if !bg.Enabled {
		return SessionBackgroundImageView{}, nil
	}
	service := a.ensureSessionBackground()
	image, ok := service.selectImage(tabID, bg, rotate)
	if !ok {
		return SessionBackgroundImageView{}, nil
	}
	info, err := os.Lstat(image.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		service.forget(tabID)
		if err == nil {
			err = fmt.Errorf("background path is not a regular file")
		}
		return SessionBackgroundImageView{}, err
	}
	token := a.ensureMediaTokenStore().create(image.path, image.name, image.mime, "image", info.Size(), info.ModTime())
	return SessionBackgroundImageView{
		Path: image.path,
		URL:  "/__WorkGround2_workspace_media/" + token + "/" + url.PathEscape(image.name),
	}, nil
}

func (s *sessionBackgroundService) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalog = sessionBackgroundCatalog{}
	s.selected = map[string]string{}
	s.order = nil
}

func (s *sessionBackgroundService) forget(tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.selected, tabID)
	for i, id := range s.order {
		if id == tabID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

func (s *sessionBackgroundService) remember(tabID, path string) {
	if _, exists := s.selected[tabID]; !exists {
		s.order = append(s.order, tabID)
	}
	s.selected[tabID] = path
	for len(s.order) > sessionBackgroundSelectionLimit {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.selected, oldest)
	}
}

func (s *sessionBackgroundService) selectImage(tabID string, bg config.DesktopSessionBackgroundConfig, rotate bool) (sessionBackgroundImage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	catalog := s.loadLocked(bg, rotate)
	if len(catalog.images) == 0 {
		delete(s.selected, tabID)
		return sessionBackgroundImage{}, false
	}
	current := s.selected[tabID]
	currentIndex := -1
	for i := range catalog.images {
		if sameBackgroundPath(catalog.images[i].path, current) {
			currentIndex = i
			break
		}
	}
	index := currentIndex
	if rotate {
		if currentIndex >= 0 && len(catalog.images) > 1 {
			index = (currentIndex + 1) % len(catalog.images)
		} else if currentIndex < 0 {
			index = 0
		}
	} else if currentIndex < 0 {
		index = 0
		if bg.RandomOnOpen == nil || *bg.RandomOnOpen {
			index = s.choose(len(catalog.images))
		}
	}
	if index < 0 || index >= len(catalog.images) {
		index = 0
	}
	image := catalog.images[index]
	s.remember(tabID, image.path)
	return image, true
}

func (s *sessionBackgroundService) load(bg config.DesktopSessionBackgroundConfig, force bool) sessionBackgroundCatalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(bg, force)
}

func (s *sessionBackgroundService) loadLocked(bg config.DesktopSessionBackgroundConfig, force bool) sessionBackgroundCatalog {
	raw, _ := json.Marshal(bg)
	key := string(raw)
	if !force && s.catalog.key == key {
		return cloneSessionBackgroundCatalog(s.catalog)
	}
	catalog := scanSessionBackground(bg)
	catalog.key = key
	s.catalog = catalog
	return cloneSessionBackgroundCatalog(catalog)
}

func cloneSessionBackgroundCatalog(in sessionBackgroundCatalog) sessionBackgroundCatalog {
	in.images = append([]sessionBackgroundImage(nil), in.images...)
	in.sources = append([]SessionBackgroundSourceView(nil), in.sources...)
	return in
}

func scanSessionBackground(bg config.DesktopSessionBackgroundConfig) sessionBackgroundCatalog {
	catalog := sessionBackgroundCatalog{sources: make([]SessionBackgroundSourceView, 0, len(bg.Sources))}
	seen := map[string]bool{}
	for _, source := range bg.Sources {
		view := SessionBackgroundSourceView{
			Kind: source.Kind, Path: source.Path, Enabled: source.Enabled == nil || *source.Enabled, Recursive: source.Recursive,
		}
		if !view.Enabled {
			catalog.sources = append(catalog.sources, view)
			continue
		}
		images, err := scanSessionBackgroundSource(source)
		if err != nil {
			view.Error = err.Error()
		}
		for _, image := range images {
			key := backgroundPathKey(image.path)
			if seen[key] {
				continue
			}
			seen[key] = true
			catalog.images = append(catalog.images, image)
			view.ImageCount++
		}
		catalog.sources = append(catalog.sources, view)
	}
	return catalog
}

func scanSessionBackgroundSource(source config.DesktopSessionBackgroundSource) ([]sessionBackgroundImage, error) {
	path, err := filepath.Abs(filepath.Clean(strings.TrimSpace(source.Path)))
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	switch source.Kind {
	case config.SessionBackgroundSourceFile:
		image, ok, err := sessionBackgroundImageAt(path, info)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("unsupported or non-regular image")
		}
		return []sessionBackgroundImage{image}, nil
	case config.SessionBackgroundSourceFolder:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("background folder is not a regular directory")
		}
		images := []sessionBackgroundImage{}
		err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if current != path && entry.IsDir() && !source.Recursive {
				return filepath.SkipDir
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			image, ok, err := sessionBackgroundImageAt(current, entryInfo)
			if err != nil {
				return err
			}
			if ok {
				images = append(images, image)
			}
			return nil
		})
		sort.Slice(images, func(i, j int) bool { return backgroundPathKey(images[i].path) < backgroundPathKey(images[j].path) })
		return images, err
	default:
		return nil, fmt.Errorf("unknown background source kind %q", source.Kind)
	}
}

func sessionBackgroundImageAt(path string, info os.FileInfo) (sessionBackgroundImage, bool, error) {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return sessionBackgroundImage{}, false, nil
	}
	mime := sessionBackgroundMIMEs[strings.ToLower(filepath.Ext(path))]
	if mime == "" {
		return sessionBackgroundImage{}, false, nil
	}
	return sessionBackgroundImage{path: filepath.Clean(path), name: info.Name(), mime: mime}, true, nil
}

func sameBackgroundPath(a, b string) bool {
	return backgroundPathKey(a) == backgroundPathKey(b)
}

func backgroundPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
