package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/config"
	sess "workground2/internal/session"
)

const legacyRoleMoveAge = time.Minute

// rehomeLegacyRoleSessions repairs the old role-call layout that wrote every
// Dispatcher/Reflector/Ideator scratch Session into Global. Only an exact,
// unique Assistant match is moved; ambiguous, live, recent, or conflicting
// Sessions stay untouched and can be retried on a later start.
func rehomeLegacyRoleSessions(store *assistant.Store, globalDir string, targetDir func(string) string, busy func(string) bool, now time.Time) (int, error) {
	if store == nil || strings.TrimSpace(globalDir) == "" || targetDir == nil {
		return 0, nil
	}
	records, err := store.List()
	if err != nil {
		return 0, err
	}
	var snapshots []assistant.Snapshot
	for _, record := range records {
		snapshot, err := store.Get(record.ID)
		if err != nil {
			return 0, err
		}
		if snapshot.Assistant.Scope == assistant.ScopeWorkspace && strings.TrimSpace(snapshot.Assistant.WorkspaceRoot) != "" {
			snapshots = append(snapshots, snapshot)
		}
	}
	if len(snapshots) == 0 {
		return 0, nil
	}

	infos, err := agent.ListSessions(globalDir)
	if err != nil {
		return 0, err
	}
	moved := 0
	var issues []error
	for _, info := range infos {
		path := strings.TrimSpace(info.Path)
		if path == "" || strings.TrimSpace(info.AssistantID) != "" || info.Scope != "global" {
			continue
		}
		if (busy != nil && busy(path)) || agent.SessionLeaseHeldByOtherRuntime(path) {
			continue
		}
		if modified := agent.SessionContentModTime(path); !modified.IsZero() && now.Sub(modified) < legacyRoleMoveAge {
			continue
		}
		messages, err := agent.LoadSessionUserMessages(path)
		if err != nil || len(messages) != 1 {
			continue
		}
		owner, ok := legacyRoleOwner(messages[0].Text, snapshots)
		if !ok {
			continue
		}
		dir := strings.TrimSpace(targetDir(owner.WorkspaceRoot))
		if dir == "" || sameDesktopPath(dir, globalDir) {
			continue
		}
		if err := moveLegacyRoleSession(path, dir, owner); err != nil {
			issues = append(issues, fmt.Errorf("rehome legacy Assistant role Session %s: %w", filepath.Base(path), err))
			continue
		}
		moved++
	}
	return moved, errors.Join(issues...)
}

func legacyRoleOwner(prompt string, snapshots []assistant.Snapshot) (assistant.Assistant, bool) {
	prompt = strings.TrimSpace(prompt)
	rolePrompt := strings.HasPrefix(prompt, "你是长期助手的 Dispatcher。") ||
		strings.HasPrefix(prompt, "你是长期助手的 Reflector。") ||
		strings.HasPrefix(prompt, "你是长期助手的 Ideator。")
	if !rolePrompt {
		return assistant.Assistant{}, false
	}
	var matches []assistant.Assistant
	for _, snapshot := range snapshots {
		a := snapshot.Assistant
		mission := strings.TrimSpace(a.Mission)
		missionMatch := mission != "" && strings.Contains(prompt, "助手使命：\n"+mission+"\n")
		dispatchMatch := false
		if strings.HasPrefix(prompt, "你是长期助手的 Reflector。") {
			for _, dispatch := range snapshot.Dispatches {
				marker := fmt.Sprintf("分类：%s\n用户原文：\n%s\nDispatcher 一级回复：%s\n", dispatch.Kind, dispatch.Input, dispatch.Reply)
				if strings.Contains(prompt, marker) {
					dispatchMatch = true
					break
				}
			}
		}
		if missionMatch || dispatchMatch {
			matches = append(matches, a)
		}
	}
	if len(matches) != 1 {
		return assistant.Assistant{}, false
	}
	return matches[0], true
}

// moveLegacyRoleSession moves sidecars first and the transcript last. A partial
// sidecar failure leaves the transcript discoverable in Global; a later start
// safely retries missing sidecars before committing the transcript move.
func moveLegacyRoleSession(path, dir string, owner assistant.Assistant) error {
	key := filepath.Base(path)
	target := filepath.Join(dir, key)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("target already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	artifacts := sess.Artifacts(path, key)
	for _, artifact := range artifacts[1:] {
		if err := sess.MovePathIfExists(artifact.Src, filepath.Join(dir, artifact.Name)); err != nil {
			return err
		}
	}
	meta, err := agent.EnsureBranchMeta(target)
	if err != nil {
		return err
	}
	meta.Scope = "project"
	meta.WorkspaceRoot = normalizeProjectRoot(owner.WorkspaceRoot)
	meta.SessionKind = agent.SessionKindAssistant
	meta.SessionSource = agent.SessionSourceAssist
	meta.AssistantID = owner.ID
	if err := agent.SaveBranchMetaPreserveUpdated(target, meta); err != nil {
		return err
	}
	return sess.MovePathIfExists(path, target)
}

func migrateAssistantRoleSessions(app *App, store *assistant.Store) {
	busy := func(path string) bool {
		return app != nil && app.findTabBySessionRuntimeKey(sessionRuntimeKey(path)) != nil
	}
	targetDir := func(root string) string {
		_ = addProject(root, "")
		return config.ProjectSessionDir(root)
	}
	moved, err := rehomeLegacyRoleSessions(store, config.SessionDir(), targetDir, busy, time.Now())
	if err != nil {
		slog.Warn("desktop: legacy Assistant role Session migration incomplete", "moved", moved, "error", err)
		return
	}
	if moved > 0 {
		slog.Info("desktop: rehomed legacy Assistant role Sessions", "count", moved)
		if app != nil {
			app.invalidatePromptHistoryCache()
			app.emitProjectTreeChanged()
		}
	}
}
