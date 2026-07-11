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

func (a *App) flowSkillShareService() *skillshare.Service {
	return skillshare.NewFlow(config.WorkGround2HomeDir())
}

func (a *App) FlowSkillShareProfiles() ([]skillshare.ProfileView, error) {
	return a.flowSkillShareService().Profiles()
}

func (a *App) SaveFlowSkillShareProfile(in skillshare.ProfileInput, secretValue string) (skillshare.ProfileView, error) {
	var secretKey string
	prev, hadPrev, err := a.flowSkillShareProfile(strings.TrimSpace(in.ID))
	if err != nil {
		return skillshare.ProfileView{}, err
	}
	if strings.TrimSpace(secretValue) != "" {
		id := strings.TrimSpace(in.ID)
		if !pluginpkg.IsValidName(id) {
			return skillshare.ProfileView{}, fmt.Errorf("invalid profile id %q", id)
		}
		if strings.TrimSpace(in.SecretRef) == "" {
			in.SecretRef = flowSkillShareSecretKey(id)
		}
		secretKey = credentialKeyFromSkillShareSecretRef(in.SecretRef)
		if secretKey == "" {
			return skillshare.ProfileView{}, fmt.Errorf("secretRef is empty")
		}
	}

	view, err := a.flowSkillShareService().Save(context.Background(), in)
	if err != nil {
		return skillshare.ProfileView{}, err
	}
	if strings.TrimSpace(secretValue) != "" {
		if _, err := config.SetCredential(secretKey, secretValue); err != nil {
			if hadPrev {
				_, _ = a.flowSkillShareService().Save(context.Background(), skillShareProfileInputFromView(prev))
			} else {
				_, _ = a.flowSkillShareService().Delete(context.Background(), in.ID, skillshare.DeleteOptions{})
			}
			return skillshare.ProfileView{}, err
		}
	}
	return view, nil
}

func (a *App) SyncFlowSkillShareProfile(id string, opts skillshare.SyncOptions) (skillshare.TaskView, error) {
	if err := a.ensureActiveTabRebuildAllowed("flow-skill-share"); err != nil {
		return skillshare.TaskView{}, err
	}
	task, err := a.flowSkillShareService().Sync(context.Background(), id, opts)
	if err != nil {
		return task, err
	}
	a.invalidateSkillRootsCache()
	if err := a.rebuild(); err != nil {
		return task, err
	}
	return task, nil
}

func (a *App) DeleteFlowSkillShareProfile(id string, opts skillshare.DeleteOptions) (skillshare.ProfileView, error) {
	if err := a.ensureActiveTabRebuildAllowed("flow-skill-share"); err != nil {
		return skillshare.ProfileView{}, err
	}

	secretRef := ""
	profiles, err := a.flowSkillShareService().Profiles()
	if err != nil {
		return skillshare.ProfileView{}, err
	}
	for _, profile := range profiles {
		if profile.ID == strings.TrimSpace(id) {
			secretRef = profile.SecretRef
			break
		}
	}

	view, err := a.flowSkillShareService().Delete(context.Background(), id, opts)
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

func (a *App) RecoverFlowSkillShareProfiles() ([]skillshare.TaskView, error) {
	return a.flowSkillShareService().Recover(context.Background())
}

func (a *App) flowSkillShareProfile(id string) (skillshare.ProfileView, bool, error) {
	if id == "" {
		return skillshare.ProfileView{}, false, nil
	}
	profiles, err := a.flowSkillShareService().Profiles()
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

func flowSkillShareSecretKey(id string) string {
	var b strings.Builder
	b.WriteString("FLOWSKILLSHARE_")
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
