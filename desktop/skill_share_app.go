package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"workground2/internal/config"
	"workground2/internal/pluginpkg"
	"workground2/internal/skillshare"
)

func (a *App) skillShareService() *skillshare.Service {
	return skillshare.New(config.WorkGround2HomeDir())
}

func (a *App) SkillShareProfiles() ([]skillshare.ProfileView, error) {
	return a.skillShareService().Profiles()
}

func (a *App) SaveSkillShareProfile(in skillshare.ProfileInput, secretValue string) (skillshare.ProfileView, error) {
	var secretKey string
	prev, hadPrev, err := a.skillShareProfile(strings.TrimSpace(in.ID))
	if err != nil {
		return skillshare.ProfileView{}, err
	}
	if strings.TrimSpace(secretValue) != "" {
		id := strings.TrimSpace(in.ID)
		if !pluginpkg.IsValidName(id) {
			return skillshare.ProfileView{}, fmt.Errorf("invalid profile id %q", id)
		}
		if strings.TrimSpace(in.SecretRef) == "" {
			in.SecretRef = skillShareSecretKey(id)
		}
		secretKey = credentialKeyFromSkillShareSecretRef(in.SecretRef)
		if secretKey == "" {
			return skillshare.ProfileView{}, fmt.Errorf("secretRef is empty")
		}
	}

	view, err := a.skillShareService().Save(context.Background(), in)
	if err != nil {
		return skillshare.ProfileView{}, err
	}
	if strings.TrimSpace(secretValue) != "" {
		if _, err := config.SetCredential(secretKey, secretValue); err != nil {
			if hadPrev {
				_, _ = a.skillShareService().Save(context.Background(), skillShareProfileInputFromView(prev))
			} else {
				_, _ = a.skillShareService().Delete(context.Background(), in.ID, skillshare.DeleteOptions{})
			}
			return skillshare.ProfileView{}, err
		}
	}
	return view, nil
}

func (a *App) SyncSkillShareProfile(id string, opts skillshare.SyncOptions) (skillshare.TaskView, error) {
	if err := a.ensureActiveTabRebuildAllowed("skill-share"); err != nil {
		return skillshare.TaskView{}, err
	}
	task, err := a.skillShareService().Sync(context.Background(), id, opts)
	if err != nil {
		return task, err
	}
	a.invalidateSkillRootsCache()
	if err := a.rebuild(); err != nil {
		return task, err
	}
	return task, nil
}

func (a *App) DeleteSkillShareProfile(id string, opts skillshare.DeleteOptions) (skillshare.ProfileView, error) {
	if err := a.ensureActiveTabRebuildAllowed("skill-share"); err != nil {
		return skillshare.ProfileView{}, err
	}

	secretRef := ""
	profiles, err := a.skillShareService().Profiles()
	if err != nil {
		return skillshare.ProfileView{}, err
	}
	for _, profile := range profiles {
		if profile.ID == strings.TrimSpace(id) {
			secretRef = profile.SecretRef
			break
		}
	}

	view, err := a.skillShareService().Delete(context.Background(), id, opts)
	if err != nil {
		return view, err
	}

	var removeErr error
	if opts.RemoveSecret {
		if key := credentialKeyFromSkillShareSecretRef(secretRef); key != "" {
			removeErr = config.RemoveCredential(key)
		}
	}
	a.invalidateSkillRootsCache()
	rebuildErr := a.rebuild()
	if err := errors.Join(removeErr, rebuildErr); err != nil {
		return view, err
	}
	return view, nil
}

func (a *App) RecoverSkillShareProfiles() ([]skillshare.TaskView, error) {
	return a.skillShareService().Recover(context.Background())
}

func (a *App) skillShareProfile(id string) (skillshare.ProfileView, bool, error) {
	if id == "" {
		return skillshare.ProfileView{}, false, nil
	}
	profiles, err := a.skillShareService().Profiles()
	if err != nil {
		return skillshare.ProfileView{}, false, err
	}
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, true, nil
		}
	}
	return skillshare.ProfileView{}, false, nil
}

func skillShareProfileInputFromView(view skillshare.ProfileView) skillshare.ProfileInput {
	return skillshare.ProfileInput{
		ID:          view.ID,
		Enabled:     view.Enabled,
		DisplayName: view.DisplayName,
		GitURL:      view.GitURL,
		Branch:      view.Branch,
		Path:        view.Path,
		Username:    view.Username,
		SecretRef:   view.SecretRef,
		PluginName:  view.PluginName,
		Update:      view.Update,
	}
}

func skillShareSecretKey(id string) string {
	var b strings.Builder
	b.WriteString("SKILLSHARE_")
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
	return strings.TrimRight(b.String(), "_") + "_GIT_PASSWORD"
}

func credentialKeyFromSkillShareSecretRef(ref string) string {
	key := strings.TrimSpace(ref)
	if len(key) >= 4 && strings.EqualFold(key[:4], "env:") {
		key = strings.TrimSpace(key[4:])
	}
	return key
}
