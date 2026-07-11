package main

import (
	"context"
	"errors"
	"strings"

	"workground2/internal/config"
	"workground2/pkg/drawaddon"
)

func (a *App) drawAddonService() *drawaddon.Service {
	return drawaddon.New(config.WorkGround2HomeDir())
}

func (a *App) DrawAddonProviders() ([]drawaddon.ProviderView, error) {
	return a.drawAddonService().Providers()
}

func (a *App) SaveDrawAddonProvider(in drawaddon.ProviderInput, secretValue string) (drawaddon.ProviderView, error) {
	if err := a.ensureActiveTabRebuildAllowed("draw-addon"); err != nil {
		return drawaddon.ProviderView{}, err
	}

	id := strings.TrimSpace(in.ID)
	var secretKey string
	prev, hadPrev, err := a.drawAddonProvider(id)
	if err != nil {
		return drawaddon.ProviderView{}, err
	}
	if strings.TrimSpace(secretValue) != "" {
		if strings.TrimSpace(in.APIKeyRef) == "" {
			in.APIKeyRef = drawAddonSecretKey(id)
		}
		secretKey = credentialKeyFromDrawAddonSecretRef(in.APIKeyRef)
		if secretKey == "" {
			return drawaddon.ProviderView{}, errors.New("apiKeyRef is empty or unsupported")
		}
	}

	svc := a.drawAddonService()
	view, err := svc.Save(context.Background(), in)
	if err != nil {
		return drawaddon.ProviderView{}, err
	}
	if strings.TrimSpace(secretValue) == "" {
		if err := a.rebuild(); err != nil {
			return view, err
		}
		return view, nil
	}
	if _, err := config.SetCredential(secretKey, secretValue); err != nil {
		if hadPrev {
			_, _ = svc.Save(context.Background(), drawAddonProviderInputFromView(prev))
		} else {
			_, _ = svc.Delete(context.Background(), in.ID)
		}
		return drawaddon.ProviderView{}, err
	}
	if err := a.rebuild(); err != nil {
		return view, err
	}
	return view, nil
}

func (a *App) DeleteDrawAddonProvider(id string) (drawaddon.ProviderView, error) {
	if err := a.ensureActiveTabRebuildAllowed("draw-addon"); err != nil {
		return drawaddon.ProviderView{}, err
	}

	prev, hadPrev, err := a.drawAddonProvider(strings.TrimSpace(id))
	if err != nil {
		return drawaddon.ProviderView{}, err
	}
	view, deleteErr := a.drawAddonService().Delete(context.Background(), id)
	var removeErr error
	if hadPrev {
		if key := credentialKeyFromDrawAddonSecretRef(prev.APIKeyRef); key != "" && key == drawAddonSecretKey(prev.ID) {
			removeErr = config.RemoveCredential(key)
		}
	}
	rebuildErr := a.rebuild()
	return view, errors.Join(deleteErr, removeErr, rebuildErr)
}

func (a *App) GenerateImageWithDrawAddon(in drawaddon.GenerateInput) (drawaddon.TaskView, error) {
	return a.drawAddonService().Generate(context.Background(), in)
}

func (a *App) drawAddonProvider(id string) (drawaddon.ProviderView, bool, error) {
	if id == "" {
		return drawaddon.ProviderView{}, false, nil
	}
	providers, err := a.drawAddonService().Providers()
	if err != nil {
		return drawaddon.ProviderView{}, false, err
	}
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true, nil
		}
	}
	return drawaddon.ProviderView{}, false, nil
}

func drawAddonProviderInputFromView(view drawaddon.ProviderView) drawaddon.ProviderInput {
	return drawaddon.ProviderInput{
		ID:          view.ID,
		Enabled:     view.Enabled,
		DisplayName: view.DisplayName,
		Mode:        view.Mode,
		BaseURL:     view.BaseURL,
		Model:       view.Model,
		APIKeyRef:   view.APIKeyRef,
		CLICommand:  view.CLICommand,
		CLIArgs:     append([]string(nil), view.CLIArgs...),
		OutputDir:   view.OutputDir,
	}
}

func drawAddonSecretKey(id string) string {
	var b strings.Builder
	b.WriteString("DRAWADDON_")
	lastUnderscore := false
	for _, r := range strings.ToUpper(strings.TrimSpace(id)) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.TrimRight(b.String(), "_")
	if out == "DRAWADDON" {
		out = "DRAWADDON_PROVIDER"
	}
	return out + "_API_KEY"
}

func credentialKeyFromDrawAddonSecretRef(ref string) string {
	key := strings.TrimSpace(ref)
	if len(key) >= 4 && strings.EqualFold(key[:4], "env:") {
		key = strings.TrimSpace(key[4:])
	}
	if key == "" || strings.Contains(key, "://") {
		return ""
	}
	return key
}
