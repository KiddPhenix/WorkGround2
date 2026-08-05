package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/agent"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/skill"
	"workground2/internal/work"
)

const workViewEventPrefix = "work:view:"

type workViewWatch struct {
	tabID       string
	workID      string
	broadcaster *control.WorkViewBroadcaster
	streamID    string
	cancel      context.CancelFunc
}

// SelectWorkInputFileRequest carries full input identity to the host boundary.
// The host validates it against the authoritative WorkView before opening a
// native picker.
type SelectWorkInputFileRequest struct {
	WorkID  string `json:"workId"`
	RunID   string `json:"runId"`
	TaskID  string `json:"taskId"`
	BlockID string `json:"blockId"`
	InputID string `json:"inputId"`
	SpecID  string `json:"specId"`
	Path    string `json:"path,omitempty"`
}

// SelectWorkInputFileResult is a typed, side-effect-free selection result.
// Selecting a file does not submit the input.
type SelectWorkInputFileResult struct {
	ArtifactRef *work.ArtifactRef        `json:"artifactRef,omitempty"`
	Canceled    bool                     `json:"canceled"`
	Error       *work.WorkTransportError `json:"error,omitempty"`
}

// SelectWorkInformationFileRequest selects a file before a custom WorkInput
// exists. The resulting ArtifactRef is still only submitted by
// AddCustomWorkInput.
type SelectWorkInformationFileRequest struct {
	WorkID string `json:"workId"`
	Path   string `json:"path,omitempty"`
}

var openWorkInputFileDialog = runtime.OpenFileDialog

// workResyncGates serializes authoritativeWorkViewResync per workID so that
// snapshot linearization order matches generation order. Only same-workID
// callers block each other; different workIDs proceed independently.
// Idle entries (refs == 0) are deleted on release so the map does not leak.
type workResyncGates struct {
	mu    sync.Mutex
	gates map[string]*workResyncGate
}

type workResyncGate struct {
	mu   sync.Mutex
	refs int // protected by workResyncGates.mu
}

func (g *workResyncGates) lock(workID string) *workResyncGate {
	g.mu.Lock()
	if g.gates == nil {
		g.gates = map[string]*workResyncGate{}
	}
	gate, ok := g.gates[workID]
	if !ok {
		gate = &workResyncGate{}
		g.gates[workID] = gate
	}
	gate.refs++
	g.mu.Unlock()
	gate.mu.Lock()
	return gate
}

func (g *workResyncGates) unlock(gate *workResyncGate, workID string) {
	gate.mu.Unlock()
	g.mu.Lock()
	if g.gates != nil {
		gate.refs--
		if gate.refs == 0 {
			delete(g.gates, workID)
		}
	}
	g.mu.Unlock()
}

// workController is the local narrow port the desktop needs from a Controller.
// The concrete *control.Controller implements both WorkControl() and WorkViews().
type workController interface {
	WorkEnabled() bool
	WorkV2Enabled() bool
	WorkControl() control.WorkControl
	WorkViews() *control.WorkViewBroadcaster
}

// WorkEnabled reports the loaded per-tab configuration intent without waiting
// for that tab's Controller to finish starting. The frontend uses this stable
// answer to reserve the Work navigation slot while WorkCapable is still
// pending. Runtime capability remains a separate typed check below.
func (a *App) WorkEnabled(tabID string) (bool, error) {
	tab := a.tabByID(tabID)
	if tab == nil {
		return false, nil
	}
	cfg, err := config.LoadForRoot(tab.WorkspaceRoot)
	if err != nil {
		return false, fmt.Errorf("work: load config for tab %q: %w", tabID, err)
	}
	return cfg.Work.Enabled, nil
}

// WorkCapable reports whether the owning controller for tabID supports the
// typed Work feature. Callers use this boolean to control readiness of an entry
// already owned by WorkEnabled, without probing errors as capability signals.
func (a *App) WorkCapable(tabID string) bool {
	_, ctrl := a.tabAndCtrlByID(tabID)
	owner, ok := ctrl.(workController)
	return ok && owner.WorkEnabled()
}

// WorkCollaborationV2Enabled reports the explicit per-controller V2 gate. V1
// callers continue to use WorkEnabled/WorkCapable and are unaffected.
func (a *App) WorkCollaborationV2Enabled(tabID string) bool {
	_, ctrl := a.tabAndCtrlByID(tabID)
	owner, ok := ctrl.(workController)
	return ok && owner.WorkV2Enabled()
}

// ── Composite Work Session creation ─────────────────────────────────────────

// CreateWorkSessionInput is the typed input for creating a Work Session.
type CreateWorkSessionInput struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot"`
	RequestID     string `json:"requestId"`
	TabID         string `json:"tabId,omitempty"`
}

// CreateWorkSessionResult carries the composite creation outcome.
type CreateWorkSessionResult struct {
	TabMeta     TabMeta        `json:"tabMeta"`
	WorkView    *work.WorkView `json:"workView,omitempty"`
	Duplicate   bool           `json:"duplicate"`
	Error       string         `json:"error,omitempty"`
	Recoverable bool           `json:"recoverable"`
}

// CreateReusableWorkSessionInput creates a new Work Session from an immutable
// reusable flow. RequestID covers both Session and Work creation.
type CreateReusableWorkSessionInput struct {
	FlowID    string                     `json:"flowId"`
	Values    map[string]json.RawMessage `json:"values,omitempty"`
	RequestID string                     `json:"requestId"`
}

// CreateReusableWorkSessionResult exposes partial recovery in the same shape
// as first-time Work Session creation.
type CreateReusableWorkSessionResult struct {
	TabMeta     TabMeta               `json:"tabMeta"`
	Run         *work.ReusableFlowRun `json:"run,omitempty"`
	Duplicate   bool                  `json:"duplicate"`
	Error       string                `json:"error,omitempty"`
	Recoverable bool                  `json:"recoverable"`
}

var createWorkSessionMu sync.Mutex

// CreateWorkSession creates a Work Session and calls BeginWorkPlanning.
// The requestId is a stable idempotency key.
func (a *App) CreateWorkSession(input CreateWorkSessionInput) (CreateWorkSessionResult, error) {
	result := CreateWorkSessionResult{}
	scope := strings.TrimSpace(input.Scope)
	if scope != "project" && scope != "global" {
		result.Error = "scope must be 'project' or 'global'"
		return result, nil
	}
	workspaceRoot := input.WorkspaceRoot
	if scope == "project" {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
		if workspaceRoot == "" {
			result.Error = "workspaceRoot is required for project scope"
			return result, nil
		}
	} else {
		workspaceRoot = ""
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		result.Error = "requestId is required"
		return result, nil
	}

	createWorkSessionMu.Lock()
	defer createWorkSessionMu.Unlock()

	tab, duplicate, err := a.findWorkSessionByRequest(scope, workspaceRoot, requestID)
	if err != nil {
		result.Error = fmt.Sprintf("find existing Work Session: %v", err)
		result.Recoverable = true
		return result, nil
	}
	if tab == nil {
		if tabID := strings.TrimSpace(input.TabID); tabID != "" {
			tab, duplicate, err = a.workSessionTab(tabID, scope, workspaceRoot, requestID)
		} else {
			tab, err = a.ensureBlankBackgroundTab(scope, workspaceRoot)
		}
		if err != nil {
			result.Error = fmt.Sprintf("prepare session: %v", err)
			result.Recoverable = true
			return result, nil
		}
		if !duplicate {
			if err := a.bindWorkSession(tab, requestID, ""); err != nil {
				result.TabMeta = a.tabMeta(tab, false)
				result.Error = fmt.Sprintf("persist Work Session: %v", err)
				result.Recoverable = true
				return result, nil
			}
		}
	}
	if err := a.nameWorkSession(tab, workspaceRoot); err != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = fmt.Sprintf("name Work Session: %v", err)
		result.Recoverable = true
		return result, nil
	}
	if err := a.waitWorkSessionReady(tab.ID, 30*time.Second); err != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = fmt.Sprintf("start Work Session: %v", err)
		result.Recoverable = true
		return result, nil
	}

	planInput := work.BeginWorkPlanningInput{
		SessionID: tab.SessionID,
		RequestID: requestID,
	}
	planResult, planErr := a.BeginWorkPlanning(tab.ID, planInput)
	result.Duplicate = duplicate

	if planErr != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = fmt.Sprintf("BeginWorkPlanning: %v", planErr)
		result.Recoverable = true
		return result, nil
	}
	if planResult.TransportError != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = planResult.TransportError.Message
		result.Recoverable = planResult.Recoverable
		return result, nil
	}

	result.Duplicate = result.Duplicate || planResult.Duplicate
	if planResult.Result != nil {
		result.WorkView = planResult.Result
	}
	result.TabMeta = a.tabMeta(tab, false)
	return result, nil
}

// CreateReusableWorkSession creates a separate Session, runs the saved flow
// through that Session's Controller, and binds both identities durably. A retry
// resumes whichever of those phases already committed.
func (a *App) CreateReusableWorkSession(sourceTabID string, input CreateReusableWorkSessionInput) (CreateReusableWorkSessionResult, error) {
	result := CreateReusableWorkSessionResult{}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		result.Error = "requestId is required"
		return result, nil
	}
	if strings.TrimSpace(input.FlowID) == "" {
		result.Error = "flowId is required"
		return result, nil
	}
	a.mu.RLock()
	source := a.tabByIDLocked(strings.TrimSpace(sourceTabID))
	if source == nil {
		a.mu.RUnlock()
		result.Error = "source Work Session is no longer available"
		return result, nil
	}
	scope := strings.TrimSpace(source.Scope)
	workspaceRoot := source.WorkspaceRoot
	a.mu.RUnlock()
	if scope == "" {
		scope = "global"
	}
	if scope == "project" {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
		if workspaceRoot == "" {
			result.Error = "source Work Session workspace is unavailable"
			return result, nil
		}
	} else {
		scope = "global"
		workspaceRoot = ""
	}

	createWorkSessionMu.Lock()
	defer createWorkSessionMu.Unlock()
	tab, duplicate, err := a.findWorkSessionByRequest(scope, workspaceRoot, requestID)
	if err != nil {
		result.Error = fmt.Sprintf("find existing reusable Work Session: %v", err)
		result.Recoverable = true
		return result, nil
	}
	if tab == nil {
		tab, err = a.ensureBlankBackgroundTab(scope, workspaceRoot)
		if err != nil {
			result.Error = fmt.Sprintf("prepare reusable Work Session: %v", err)
			result.Recoverable = true
			return result, nil
		}
		if err := a.bindWorkSession(tab, requestID, ""); err != nil {
			result.TabMeta = a.tabMeta(tab, false)
			result.Error = fmt.Sprintf("persist reusable Work Session: %v", err)
			result.Recoverable = true
			return result, nil
		}
	}
	if err := a.nameWorkSession(tab, workspaceRoot); err != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = fmt.Sprintf("name reusable Work Session: %v", err)
		result.Recoverable = true
		return result, nil
	}
	if err := a.waitWorkSessionReady(tab.ID, 30*time.Second); err != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = fmt.Sprintf("start reusable Work Session: %v", err)
		result.Recoverable = true
		return result, nil
	}
	wc, err := a.resolveWorkController(tab.ID)
	if err != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = err.Error()
		result.Recoverable = true
		return result, nil
	}
	run, err := wc.RunReusableFlow(a.bootContext(), work.RunReusableFlowInput{
		FlowID: input.FlowID, Values: input.Values, RequestID: requestID,
	})
	if err != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = fmt.Sprintf("run reusable flow: %v", err)
		result.Recoverable = true
		return result, nil
	}
	if run == nil || run.Work == nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Error = "reusable flow completed without a Work"
		result.Recoverable = true
		return result, nil
	}
	if err := a.bindWorkSession(tab, requestID, run.Work.ID); err != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Run = run
		result.Error = fmt.Sprintf("bind reusable Work Session: %v", err)
		result.Recoverable = true
		return result, nil
	}
	if err := a.syncWorkSessionTitle(tab.ID, run.Work.ID, run.Work.Name); err != nil {
		result.TabMeta = a.tabMeta(tab, false)
		result.Run = run
		result.Error = fmt.Sprintf("sync reusable Work Session title: %v", err)
		result.Recoverable = true
		return result, nil
	}
	result.TabMeta = a.tabMeta(tab, false)
	result.Run = run
	result.Duplicate = duplicate || run.Duplicate
	return result, nil
}

// workSessionTab resolves the caller-owned blank Session that should be
// promoted in place. The check is intentionally repeated by the backend: the
// Composer only advertises this action for a first message, but a stale UI or
// duplicate delivery must never convert a Session that already has content.
func (a *App) workSessionTab(tabID, scope, workspaceRoot, requestID string) (*WorkspaceTab, bool, error) {
	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		a.mu.RUnlock()
		return nil, false, fmt.Errorf("session %q is no longer available", tabID)
	}
	readOnly := tab.ReadOnly
	tabScope := strings.TrimSpace(tab.Scope)
	tabRoot := tab.WorkspaceRoot
	sessionKind := tab.sessionKind
	workRequestID := tab.workRequestID
	a.mu.RUnlock()

	if readOnly {
		return nil, false, fmt.Errorf("session %q is read-only", tabID)
	}
	if tabScope == "" {
		tabScope = "global"
	}
	if tabScope != scope {
		return nil, false, fmt.Errorf("session scope changed from %q to %q", scope, tabScope)
	}
	if scope == "project" && normalizeProjectRoot(tabRoot) != workspaceRoot {
		return nil, false, fmt.Errorf("session workspace changed")
	}
	if sessionKind == agent.SessionKindWork {
		if workRequestID == requestID {
			return tab, true, nil
		}
		return nil, false, fmt.Errorf("session is already bound to another Work request")
	}
	if sessionKind != "" && sessionKind != agent.SessionKindNormal {
		return nil, false, fmt.Errorf("session kind %q cannot become Work", sessionKind)
	}
	if !blankTabSessionPathHasNoContent(tab) {
		return nil, false, fmt.Errorf("only a blank Session can become Work")
	}
	return tab, false, nil
}

func (a *App) waitWorkSessionReady(tabID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		a.mu.RLock()
		tab := a.tabByIDLocked(tabID)
		if tab == nil {
			a.mu.RUnlock()
			return fmt.Errorf("session is no longer available")
		}
		ready := tab.Ready && tab.Ctrl != nil
		startupErr := strings.TrimSpace(tab.StartupErr)
		a.mu.RUnlock()
		if startupErr != "" {
			return fmt.Errorf("workspace failed to start: %s", startupErr)
		}
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("workspace start timed out")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (a *App) findWorkSessionByRequest(scope, workspaceRoot, requestID string) (*WorkspaceTab, bool, error) {
	actualRoot := globalWorkspaceRoot()
	if scope == "project" {
		actualRoot = workspaceRoot
	}
	infos, err := agent.ListSessionOrder(desktopSessionDir(actualRoot))
	if err != nil {
		return nil, false, err
	}
	for _, info := range infos {
		if info.SessionKind != agent.SessionKindWork || info.WorkRequestID != requestID {
			continue
		}
		tab, err := a.ensureTabForSessionPath(info.Path)
		if err != nil {
			return nil, false, err
		}
		a.mu.Lock()
		tab.sessionKind = agent.SessionKindWork
		tab.workID = info.WorkID
		tab.workRequestID = info.WorkRequestID
		a.mu.Unlock()
		return tab, true, nil
	}
	return nil, false, nil
}

func (a *App) nameWorkSession(tab *WorkspaceTab, workspaceRoot string) error {
	const name = "New Work"
	a.mu.RLock()
	currentTitle := strings.TrimSpace(tab.TopicTitle)
	a.mu.RUnlock()
	if currentTitle != "" && currentTitle != defaultTopicTitle && currentTitle != name {
		return nil
	}
	path := tab.currentSessionPath()
	if path == "" {
		return fmt.Errorf("session path is empty")
	}
	if err := a.RenameSession(path, name); err != nil {
		return err
	}
	a.mu.Lock()
	tab.TopicTitle = name
	a.saveTabsLocked()
	a.mu.Unlock()
	if err := setTopicTitleWithSource(workspaceRoot, tab.TopicID, name, topicTitleSourceAuto); err != nil {
		return err
	}
	_ = ensureTopicIndexed(tab.Scope, workspaceRoot, tab.TopicID, name, topicTitleSourceAuto)
	return nil
}

func (a *App) syncWorkSessionTitle(tabID, workID, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("work: automatic session title is empty")
	}

	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil || tab.sessionKind != agent.SessionKindWork {
		a.mu.RUnlock()
		return nil
	}
	if tab.workID != "" && tab.workID != workID {
		a.mu.RUnlock()
		return fmt.Errorf("work: tab %s owns Work %s, cannot title Work %s", tabID, tab.workID, workID)
	}
	sessionPath := tab.currentSessionPath()
	scope := tab.Scope
	workspaceRoot := tab.WorkspaceRoot
	topicID := tab.TopicID
	a.mu.RUnlock()

	if sessionPath == "" {
		return fmt.Errorf("work: tab %s session path is empty", tabID)
	}
	if topicID == "" {
		return fmt.Errorf("work: tab %s topic ID is empty", tabID)
	}
	titleRoot := workspaceRoot
	if scope == "global" {
		titleRoot = ""
	}
	if err := a.RenameSession(sessionPath, title); err != nil {
		return fmt.Errorf("work: update automatic session title: %w", err)
	}
	if err := setTopicTitleWithSource(titleRoot, topicID, title, topicTitleSourceAuto); err != nil {
		return fmt.Errorf("work: update automatic topic title: %w", err)
	}
	if err := ensureTopicIndexed(scope, titleRoot, topicID, title, topicTitleSourceAuto); err != nil {
		return fmt.Errorf("work: index automatic topic title: %w", err)
	}
	a.updateOpenTopicTitle(topicID, title)
	if err := saveTabSessionMeta(tab, sessionPath); err != nil {
		return fmt.Errorf("work: persist automatic session metadata: %w", err)
	}
	a.updateTopicSessionTitles(topicID, title)
	a.mu.Lock()
	a.saveTabsLocked()
	a.mu.Unlock()
	a.emitProjectTreeChanged()
	return nil
}

func (a *App) bindWorkSession(tab *WorkspaceTab, requestID, workID string) error {
	if tab == nil {
		return fmt.Errorf("tab is required")
	}
	sessionPath := strings.TrimSpace(tab.currentSessionPath())
	if sessionPath == "" {
		return fmt.Errorf("session path is required")
	}
	meta, err := agent.EnsureBranchMeta(sessionPath)
	if err != nil {
		return err
	}
	meta.SessionKind = agent.SessionKindWork
	if requestID != "" {
		meta.WorkRequestID = requestID
	}
	if workID != "" {
		meta.WorkID = workID
	}
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, meta); err != nil {
		return err
	}
	a.mu.Lock()
	tab.sessionKind = agent.SessionKindWork
	tab.workRequestID = meta.WorkRequestID
	tab.workID = meta.WorkID
	a.mu.Unlock()
	return nil
}

func (a *App) resolveWorkController(tabID string) (control.WorkControl, error) {
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return nil, fmt.Errorf("workspace is still starting")
	}
	wc, ok := ctrl.(workController)
	if !ok {
		return nil, fmt.Errorf("work: feature not available on this controller")
	}
	wctl := wc.WorkControl()
	return wctl, nil
}

// CreateWork creates a new Work from a Blueprint. RequestID enables idempotent retries.
func (a *App) CreateWork(tabID string, input work.CreateWorkInput) (*work.Work, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.CreateWork(a.bootContext(), input)
}

// GetWork returns the current Work projection.
func (a *App) GetWork(tabID, workID string) (*work.WorkView, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.GetWork(a.bootContext(), workID)
}

// RecoverWorkView performs the authoritative snapshot step of an explicit
// frontend retry or automatic remount hydration. The frontend must first
// install a fresh Watch generation; this method then returns a typed resync
// event with a backend-global generation and EventID.
func (a *App) RecoverWorkView(tabID, workID string, input work.ViewRecoveryIntent) (*work.WorkViewEvent, error) {
	const maxSafeJSONInteger = uint64(1<<53 - 1)
	workID = strings.TrimSpace(workID)
	if workID == "" || (input.Reason != work.ViewResyncRetry && input.Reason != work.ViewResyncHydrate) || input.Generation == 0 || input.Generation > maxSafeJSONInteger {
		return nil, fmt.Errorf("work: valid recovery intent is required")
	}
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return a.authoritativeWorkViewResync(a.bootContext(), wc, workID, input.Reason, input.Generation)
}

// ListWorks returns a filtered summary page.
func (a *App) ListWorks(tabID string, filter work.WorkFilter) (work.WorkPage, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return work.WorkPage{}, err
	}
	return wc.ListWorks(a.bootContext(), filter)
}

// ListWorkBlueprints returns the Blueprints available to the current workspace.
func (a *App) ListWorkBlueprints(tabID string) ([]work.WorkBlueprint, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.ListWorkBlueprints(a.bootContext())
}

// CopyWork creates an independent Draft from an existing Work.
func (a *App) CopyWork(tabID string, input work.CopyWorkInput) (*work.Work, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.CopyWork(a.bootContext(), input)
}

// PrepareReusableFlow reads the repeatable fields for one Work.
func (a *App) PrepareReusableFlow(tabID string, input work.PrepareReusableFlowInput) (*work.ReusableFlowSetup, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.PrepareReusableFlow(a.bootContext(), input)
}

// SaveReusableFlow freezes one Work as an immutable common workflow.
func (a *App) SaveReusableFlow(tabID string, input work.SaveReusableFlowInput) (*work.ReusableFlow, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.SaveReusableFlow(a.bootContext(), input)
}

// RunReusableFlow creates a new Work in the current Controller. Desktop UI
// normally uses CreateReusableWorkSession so the new Work has its own Session.
func (a *App) RunReusableFlow(tabID string, input work.RunReusableFlowInput) (*work.ReusableFlowRun, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RunReusableFlow(a.bootContext(), input)
}

// UpdateDraft updates editable draft fields with optimistic concurrency.
func (a *App) UpdateDraft(tabID string, input work.UpdateDraftInput) (*work.WorkView, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	view, err := wc.UpdateDraft(a.bootContext(), input)
	if err != nil {
		return view, err
	}
	if view != nil && view.Work != nil {
		if err := a.syncWorkSessionTitle(tabID, view.Work.ID, view.Work.Name); err != nil {
			return view, fmt.Errorf("work: UpdateDraft committed at revision %d but title sync failed; retry with the same requestId: %w", view.Revision, err)
		}
	}
	return view, nil
}

// UpsertWorkBlock persists a user-editable Block with optimistic concurrency.
func (a *App) UpsertWorkBlock(tabID string, input work.BlockUpsertInput) (*work.WorkView, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.UpsertWorkBlock(a.bootContext(), input)
}

// RunWork starts a Work through the shared Controller. Cornerstone preflight
// remains authoritative in internal/work.Service.
func (a *App) RunWork(tabID, workID, requestID string) (*work.WorkflowRun, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RunWork(a.bootContext(), workID, requestID)
}

// RetryWorkTask adds a new Attempt through the shared Controller.
func (a *App) RetryWorkTask(tabID string, input work.RetryTaskInput) (*work.Attempt, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RetryTask(a.bootContext(), input)
}

// WatchWork bridges the owning Controller's typed WorkView stream to one Wails
// event channel. subscriptionID is opaque UI identity, never business state.
// On overflow recovery signals, an authoritative GetWork snapshot is requested
// and emitted so the frontend never stays stale after a dropped terminal event.
func (a *App) WatchWork(tabID, workID, subscriptionID string) error {
	tabID = strings.TrimSpace(tabID)
	workID = strings.TrimSpace(workID)
	subscriptionID = strings.TrimSpace(subscriptionID)
	if tabID == "" || workID == "" || !validWorkSubscriptionID(subscriptionID) {
		return fmt.Errorf("work: tab, Work, and valid subscription IDs are required")
	}
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return fmt.Errorf("workspace is still starting")
	}
	owner, ok := ctrl.(workController)
	if !ok || owner.WorkViews() == nil {
		return fmt.Errorf("work: event stream not available on this controller")
	}
	wctl := owner.WorkControl()

	a.workWatchMu.Lock()
	if existing := a.workWatches[subscriptionID]; existing != nil {
		a.workWatchMu.Unlock()
		if existing.tabID == tabID && existing.workID == workID {
			return nil
		}
		return fmt.Errorf("work: subscription ID is already in use")
	}
	broadcaster := owner.WorkViews()
	streamID, events, overflows := broadcaster.SubscribeWorkReliable(workID, 32)
	ctx, cancel := context.WithCancel(a.bootContext())
	watch := &workViewWatch{
		tabID: tabID, workID: workID,
		broadcaster: broadcaster, streamID: streamID, cancel: cancel,
	}
	if a.workWatches == nil {
		a.workWatches = map[string]*workViewWatch{}
	}
	a.workWatches[subscriptionID] = watch
	a.workWatchMu.Unlock()

	go func() {
		defer a.stopWorkWatch(subscriptionID, watch)
		for {
			select {
			case <-ctx.Done():
				return
			case _, open := <-overflows:
				if !open {
					return
				}
				if err := a.recoverWorkViewFromOverflow(ctx, wctl, workID, subscriptionID); err != nil {
					a.runtimeEvents.Emit(a.bootContext(), workViewEventPrefix+subscriptionID, work.WorkViewEvent{
						SchemaVersion: work.WorkViewSchemaVersion,
						Type:          work.ViewAttention,
						WorkID:        workID,
						EventID:       fmt.Sprintf("wv-recover-failed-%s-%d", workID, time.Now().UnixNano()),
						RequestID:     "overflow-recovery-failed",
						Object:        work.ObjectContext{Kind: work.ObjectWork, ID: workID},
						Payload:       json.RawMessage(`{"overflow":true,"recovery":"failed","retryable":true}`),
						CreatedAt:     time.Now().UTC(),
					})
				}
			case event, open := <-events:
				if !open {
					return
				}
				a.runtimeEvents.Emit(a.bootContext(), workViewEventPrefix+subscriptionID, event)
			}
		}
	}()
	return nil
}

func (a *App) recoverWorkViewFromOverflow(ctx context.Context, wc control.WorkControl, workID, subscriptionID string) error {
	event, err := a.authoritativeWorkViewResync(ctx, wc, workID, work.ViewResyncOverflow, 0)
	if err != nil {
		return err
	}
	a.runtimeEvents.Emit(a.bootContext(), workViewEventPrefix+subscriptionID, *event)
	return nil
}

func (a *App) authoritativeWorkViewResync(ctx context.Context, wc control.WorkControl, workID string, reason work.ViewResyncReason, minGeneration uint64) (*work.WorkViewEvent, error) {
	return a.authoritativeWorkViewResyncWithMarshal(ctx, wc, workID, reason, minGeneration, json.Marshal)
}

func (a *App) authoritativeWorkViewResyncWithMarshal(ctx context.Context, wc control.WorkControl, workID string, reason work.ViewResyncReason, minGeneration uint64, marshal func(any) ([]byte, error)) (*work.WorkViewEvent, error) {
	gate := a.workResyncGates.lock(workID)
	defer func() { a.workResyncGates.unlock(gate, workID) }()

	view, err := wc.GetWork(ctx, workID)
	if err != nil || view == nil {
		if err != nil {
			return nil, fmt.Errorf("work: recover authoritative snapshot: %w", err)
		}
		return nil, fmt.Errorf("work: recover authoritative snapshot: empty projection")
	}
	payload, err := marshal(view)
	if err != nil {
		return nil, fmt.Errorf("work: encode authoritative snapshot: %w", err)
	}
	generation := a.nextWorkResyncGeneration(minGeneration)
	event := &work.WorkViewEvent{
		SchemaVersion: view.SchemaVersion,
		Type:          work.ViewSnapshot,
		WorkID:        workID,
		EventID:       work.ResyncEventID(workID, view.Revision, reason, generation),
		Revision:      view.Revision,
		RequestID:     string(reason) + "-recovery",
		Object:        work.ObjectContext{Kind: work.ObjectWork, ID: workID, WorkID: workID},
		Resync: &work.ViewResync{
			Reason:        reason,
			Authoritative: true,
			Generation:    generation,
		},
		Payload:   json.RawMessage(payload),
		CreatedAt: time.Now().UTC(),
	}
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("work: build authoritative resync: %w", err)
	}
	return event, nil
}

func (a *App) nextWorkResyncGeneration(min uint64) uint64 {
	for {
		current := a.workResyncGeneration.Load()
		next := current + 1
		if min > next {
			next = min
		}
		if a.workResyncGeneration.CompareAndSwap(current, next) {
			return next
		}
	}
}

// UnwatchWork idempotently removes a transient Wails WorkView subscription.
func (a *App) UnwatchWork(subscriptionID string) {
	a.stopWorkWatch(strings.TrimSpace(subscriptionID), nil)
}

func (a *App) stopWorkWatch(subscriptionID string, expected *workViewWatch) {
	a.workWatchMu.Lock()
	watch := a.workWatches[subscriptionID]
	if watch == nil || expected != nil && watch != expected {
		a.workWatchMu.Unlock()
		return
	}
	delete(a.workWatches, subscriptionID)
	a.workWatchMu.Unlock()
	watch.cancel()
	watch.broadcaster.Unsubscribe(watch.streamID)
}

func validWorkSubscriptionID(value string) bool {
	if len(value) < 8 || len(value) > 96 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

// ArchiveWork archives a Work and produces an immutable WorkRecord.
func (a *App) ArchiveWork(tabID, workID, requestID string) (*work.WorkRecord, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.ArchiveWork(a.bootContext(), workID, requestID)
}

// RestoreWork restores an archived Work to active.
func (a *App) RestoreWork(tabID, workID, requestID string) (*work.WorkView, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RestoreWork(a.bootContext(), workID, requestID)
}

// ResumeRun resumes a paused or waiting WorkflowRun.
func (a *App) ResumeRun(tabID string, input work.ResumeRunInput) (*work.WorkflowRun, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.ResumeRun(a.bootContext(), input)
}

// PauseRun pauses an active WorkflowRun. The run transitions to paused and may
// be resumed later via ResumeRun.
func (a *App) PauseRun(tabID, workID, runID, requestID string) error {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return err
	}
	return wc.PauseRun(a.bootContext(), workID, runID, requestID)
}

// CancelRun cancels a running or waiting WorkflowRun. Terminal runs are a
// no-op; the method is idempotent per requestID.
func (a *App) CancelRun(tabID, workID, runID, requestID string) error {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return err
	}
	return wc.CancelRun(a.bootContext(), workID, runID, requestID)
}

// RestartRun safely cancels the current non-terminal run (if any), then starts
// a new one. The restart requestID is used for run idempotency; cancel uses a
// derived ID, so a partial failure can retry the same two-phase operation.
func (a *App) RestartRun(tabID, workID, runID, requestID string) (*work.WorkflowRun, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RestartRun(a.bootContext(), workID, runID, requestID)
}

// DeleteWork moves a Work to trash.
func (a *App) DeleteWork(tabID, workID, requestID string) error {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return err
	}
	return wc.DeleteWork(a.bootContext(), workID, requestID)
}

// PrepareWorkRerun returns an explicit, expiring compatibility plan.
func (a *App) PrepareWorkRerun(tabID string, input work.PrepareRerunInput) (*work.RerunPlan, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.PrepareRerun(a.bootContext(), input)
}

// ExecuteWorkRerun creates a new Draft from a reviewed rerun plan.
func (a *App) ExecuteWorkRerun(tabID, planToken, requestID string) (*work.Work, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.ExecuteRerun(a.bootContext(), planToken, requestID)
}

// ── Cornerstone bindings ─────────────────────────────────────────────────────

// PinCornerstone pins a typed long-term Cornerstone to a Work.
// input.RequestID enables idempotent retries; input.ExpectedRevision provides
// optimistic concurrency control.
func (a *App) PinCornerstone(tabID, workID string, input work.PinCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.PinCornerstone(a.bootContext(), workID, input)
}

// RefreshCornerstone re-resolves a live_ref Cornerstone's source status or
// verifies snapshot blob integrity.
func (a *App) RefreshCornerstone(tabID, workID string, input work.RefreshCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RefreshCornerstone(a.bootContext(), workID, input)
}

// RemoveCornerstone tombstone-removes a Cornerstone. It can be restored via UndoCornerstone.
func (a *App) RemoveCornerstone(tabID, workID string, input work.RemoveCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RemoveCornerstone(a.bootContext(), workID, input)
}

// UndoCornerstone restores a tombstoned Cornerstone. For snapshot cornerstones,
// blob integrity is verified during restore.
func (a *App) UndoCornerstone(tabID, workID string, input work.UndoCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.UndoCornerstone(a.bootContext(), workID, input)
}

// AcceptCornerstone accepts the newly-resolved content of a stale live_ref
// Cornerstone, transitioning it back to active. Only an exact candidate digest
// match is accepted to prevent TOCTOU.
func (a *App) AcceptCornerstone(tabID, workID string, input work.AcceptCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.AcceptCornerstone(a.bootContext(), workID, input)
}

// FreezeCornerstone freezes a live_ref Cornerstone into a snapshot so its
// content no longer follows upstream changes. UseLastKnown mode can freeze the
// last accepted content when the source is unreachable.
func (a *App) FreezeCornerstone(tabID, workID string, input work.FreezeCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.FreezeCornerstone(a.bootContext(), workID, input)
}

// RepairCornerstone repairs a Cornerstone in missing, denied, invalid, or stale
// status. For live_ref cornerstones, this re-resolves the source. For snapshot
// cornerstones, this attempts blob recovery.
func (a *App) RepairCornerstone(tabID, workID string, input work.RepairCornerstoneInput) (*work.RepairResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RepairCornerstone(a.bootContext(), workID, input)
}

// ── V2 Collaboration Controller Wails bindings ──────────────────────────────

// BeginWorkPlanning starts a conversation-based definition flow.
// requestID ensures idempotent creation.
// On success, writes SessionKind=work and WorkID back to the session's BranchMeta.
func (a *App) BeginWorkPlanning(tabID string, input work.BeginWorkPlanningInput) (*work.BeginWorkPlanningResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.BeginWorkPlanningResult{}
		bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.BeginWorkPlanningWithResult(a.bootContext(), input)
	if result == nil {
		result = &work.BeginWorkPlanningResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)

	// On successful commit, bind the Work to the session via BranchMeta.
	if result.Committed && result.Result != nil && result.Result.Work != nil {
		tab, _ := a.tabAndCtrlByID(tabID)
		if tab != nil {
			if bindErr := a.bindWorkSession(tab, input.RequestID, result.Result.Work.ID); bindErr != nil {
				return result, fmt.Errorf("persist Work Session binding: %w", bindErr)
			}
		}
	}

	return result, nil
}

// ApplyDefinition atomically activates a new definition revision.
func (a *App) ApplyDefinition(tabID string, input work.ApplyDefinitionInput) (*work.ApplyDefinitionResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.ApplyDefinitionResult{}
		bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.ApplyDefinition(a.bootContext(), input)
	if result == nil {
		result = &work.ApplyDefinitionResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// CreateCandidateRevision creates a copy-on-write candidate without switching
// the active definition. Cancel remains a local UI close with zero writes.
func (a *App) CreateCandidateRevision(tabID string, input work.CreateCandidateRevisionInput) (*work.CreateCandidateRevisionResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.CreateCandidateRevisionResult{}
		bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.CreateCandidateRevision(a.bootContext(), input)
	if result == nil {
		result = &work.CreateCandidateRevisionResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// SubmitWorkInput commits the public Work V2 DTO through InputService.
func (a *App) SubmitWorkInput(tabID string, input work.SubmitWorkInputRequest) (*work.SubmitInputResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.SubmitInputResult{}
		bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.SubmitWorkInput(a.bootContext(), submitInputRequest(input))
	if result == nil {
		result = &work.SubmitInputResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// AddCustomWorkInput adds user-owned text/file information.
func (a *App) AddCustomWorkInput(tabID string, input work.AddCustomWorkInputRequest) (*work.SubmitInputResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.SubmitInputResult{}
		bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.AddCustomWorkInput(a.bootContext(), input)
	if result == nil {
		result = &work.SubmitInputResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// InferWorkInputs returns reviewable model-proposed drafts without committing
// them to the Work event log.
func (a *App) InferWorkInputs(tabID string, input work.InferWorkInputsRequest) (*work.InferWorkInputsResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.InferWorkInputs(a.bootContext(), input)
}

func submitInputRequest(input work.SubmitWorkInputRequest) work.SubmitInputRequest {
	return work.SubmitInputRequest{
		WorkID:           input.WorkID,
		InputID:          input.InputID,
		Value:            input.Value,
		Extra:            input.Extra,
		DeferStart:       input.DeferStart,
		DefinitionRev:    input.DefinitionRevision,
		InputRevision:    input.InputRevision,
		ExpectedRevision: input.ExpectedRevision,
		RequestID:        input.RequestID,
	}
}

// SelectWorkInputFile opens the native picker only after the requested
// Work/Run/Task/Block/Input/Spec identity is found in the authoritative
// projection and the active InputSpec is a file input.
func (a *App) SelectWorkInputFile(tabID string, input SelectWorkInputFileRequest) (*SelectWorkInputFileResult, error) {
	result := &SelectWorkInputFileResult{}
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result.Error = work.TransportErrorFrom(err)
		return result, nil
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.BlockID = strings.TrimSpace(input.BlockID)
	input.InputID = strings.TrimSpace(input.InputID)
	input.SpecID = strings.TrimSpace(input.SpecID)
	if input.WorkID == "" || input.RunID == "" || input.TaskID == "" ||
		input.BlockID == "" || input.InputID == "" || input.SpecID == "" {
		result.Error = work.TransportErrorFrom(errors.New(
			"work: SelectWorkInputFile: full input identity is required",
		))
		return result, nil
	}
	view, err := wc.GetWork(a.bootContext(), input.WorkID)
	if err != nil {
		result.Error = work.TransportErrorFrom(err)
		return result, nil
	}
	if view == nil || view.Work == nil || view.Definition == nil ||
		view.Work.V2CurrentRevision != view.Definition.Revision {
		result.Error = work.TransportErrorFrom(errors.New(
			"work: SelectWorkInputFile: active definition is unavailable",
		))
		return result, nil
	}
	foundInput := false
	customFileSpec := false
	for _, candidate := range view.Inputs {
		if candidate.ID == input.InputID && candidate.WorkID == input.WorkID &&
			candidate.RunID == input.RunID && candidate.TaskID == input.TaskID &&
			candidate.BlockID == input.BlockID && candidate.SpecID == input.SpecID {
			foundInput = true
			customFileSpec = candidate.CustomSpec != nil && candidate.CustomSpec.Kind == work.InputFile
			break
		}
	}
	if !foundInput {
		result.Error = work.TransportErrorFrom(errors.New(
			"work: SelectWorkInputFile: active input identity not found",
		))
		return result, nil
	}
	fileSpec := customFileSpec
	for _, spec := range view.Definition.InputSpecs {
		if spec.ID == input.SpecID && spec.Kind == work.InputFile {
			fileSpec = true
			break
		}
	}
	if !fileSpec {
		result.Error = work.TransportErrorFrom(errors.New(
			"work: SelectWorkInputFile: input spec is not a file input",
		))
		return result, nil
	}
	return a.selectWorkFile(tabID, input.Path, "Choose file for Work input")
}

// SelectWorkInformationFile selects a file for a not-yet-created custom item.
func (a *App) SelectWorkInformationFile(tabID string, input SelectWorkInformationFileRequest) (*SelectWorkInputFileResult, error) {
	result := &SelectWorkInputFileResult{}
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result.Error = work.TransportErrorFrom(err)
		return result, nil
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	if input.WorkID == "" {
		result.Error = work.TransportErrorFrom(errors.New(
			"work: SelectWorkInformationFile: workID is required",
		))
		return result, nil
	}
	view, err := wc.GetWork(a.bootContext(), input.WorkID)
	if err != nil {
		result.Error = work.TransportErrorFrom(err)
		return result, nil
	}
	if view == nil || view.Work == nil || view.Work.ArchiveState != work.ArchiveActive {
		result.Error = work.TransportErrorFrom(errors.New(
			"work: SelectWorkInformationFile: active Work is unavailable",
		))
		return result, nil
	}
	return a.selectWorkFile(tabID, input.Path, "Choose file for Work information")
}

func (a *App) selectWorkFile(tabID, selectedPath, title string) (*SelectWorkInputFileResult, error) {
	result := &SelectWorkInputFileResult{}
	root, _, ok := a.workspaceTargetForTab(tabID)
	if !ok {
		result.Error = work.TransportErrorFrom(errors.New(
			"work: SelectWorkInputFile: workspace is unavailable",
		))
		return result, nil
	}
	base, err := workspaceBaseFromRoot(root)
	if err != nil {
		result.Error = work.TransportErrorFrom(err)
		return result, nil
	}
	path := strings.TrimSpace(selectedPath)
	if path == "" {
		path, err = openWorkInputFileDialog(a.ctx, runtime.OpenDialogOptions{
			Title:            title,
			DefaultDirectory: dialogDefaultDirectory(base),
			Filters: []runtime.FileFilter{
				{DisplayName: "All files (*.*)", Pattern: "*.*"},
			},
		})
		if err != nil {
			result.Error = work.TransportErrorFrom(err)
			return result, nil
		}
	}
	if strings.TrimSpace(path) == "" {
		result.Canceled = true
		return result, nil
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		result.Error = work.TransportErrorFrom(err)
		return result, nil
	}
	if !info.Mode().IsRegular() {
		result.Error = work.TransportErrorFrom(errors.New(
			"work: SelectWorkInputFile: selection is not a regular file",
		))
		return result, nil
	}
	now := time.Now().UTC()
	idBytes := sha256.Sum256([]byte(path + "\x00" + info.ModTime().UTC().Format(time.RFC3339Nano)))
	ref := &work.ArtifactRef{
		ID:             fmt.Sprintf("selected:%x", idBytes[:12]),
		Name:           info.Name(),
		Type:           strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		Status:         work.ArtifactRefStatusAvailable,
		Path:           path,
		LastVerifiedAt: &now,
	}
	if rel, inside := workspaceRelativeIn(path, base); inside {
		ref.RelativePath = rel
	}
	result.ArtifactRef = ref
	return result, nil
}

// SetInputCornerstone pins or unpins a submitted input as a Cornerstone.
func (a *App) SetInputCornerstone(tabID string, input work.SetInputCornerstoneRequest) (*work.CornerstonePinResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.CornerstonePinResult{}
		bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.SetInputCornerstone(a.bootContext(), input)
	if result == nil {
		result = &work.CornerstonePinResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// PreviewWorkPatch generates a structured WorkPatchPreview from a discussion instruction.
func (a *App) PreviewWorkPatch(tabID string, input work.PreviewWorkPatchInput) (*work.PreviewWorkPatchResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.PreviewWorkPatchResult{}
		bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.PreviewWorkPatch(a.bootContext(), input)
	if result == nil {
		result = &work.PreviewWorkPatchResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// ApplyWorkPatch applies a previously previewed patch.
func (a *App) ApplyWorkPatch(tabID string, input work.ApplyWorkPatchInput) (*work.ApplyWorkPatchResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.ApplyWorkPatchResult{}
		bindWorkTransportError(&result.WorkRevision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.ApplyWorkPatch(a.bootContext(), input)
	if result == nil {
		result = &work.ApplyWorkPatchResult{}
	}
	bindWorkTransportError(&result.WorkRevision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// RetryWorkNode retries a failed or invalidated V2 task node.
func (a *App) RetryWorkNode(tabID string, input work.RetryWorkNodeRequest) (*work.RetryWorkNodeResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	result, callErr := wc.RetryWorkNode(a.bootContext(), input)
	if result == nil {
		result = &work.RetryWorkNodeResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.Error, callErr)
	return result, nil
}

// RetryArtifactSlot resets an active failed/partial/stale slot and wakes the
// producer task selected from authoritative Definition and run state.
func (a *App) RetryArtifactSlot(tabID string, input work.RetryArtifactSlotRequest) (*work.RetryArtifactSlotResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.RetryArtifactSlotResult{}
		bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.RetryArtifactSlot(a.bootContext(), input)
	if result == nil {
		result = &work.RetryArtifactSlotResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// SetNodeSkill binds a Skill to a Work node for execution context augmentation.
func (a *App) SetNodeSkill(tabID string, input work.SetNodeSkillRequest) (*work.SetNodeSkillResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	result, callErr := wc.SetNodeSkill(a.bootContext(), input)
	if result == nil {
		result = &work.SetNodeSkillResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.Error, callErr)
	return result, nil
}

// ClearNodeSkill removes a Skill binding from a Work node.
func (a *App) ClearNodeSkill(tabID string, input work.ClearNodeSkillRequest) (*work.ClearNodeSkillResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	result, callErr := wc.ClearNodeSkill(a.bootContext(), input)
	if result == nil {
		result = &work.ClearNodeSkillResult{}
	}
	bindWorkTransportError(&result.Revision, &result.Committed, &result.Recoverable, &result.Error, callErr)
	return result, nil
}

// ListWorkSkills returns the available Skills for the current workspace.
func (a *App) ListWorkSkills(tabID string) ([]work.SkillInfo, error) {
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return nil, fmt.Errorf("workspace is still starting")
	}
	all := ctrl.AllSkills()
	out := make([]work.SkillInfo, 0, len(all))
	for _, sk := range all {
		out = append(out, work.SkillInfo{
			Name:        sk.Name,
			Description: sk.Description,
			Scope:       string(sk.Scope),
			Enabled:     ctrl.SkillEnabled(sk.Name),
			RunAs:       string(sk.RunAs),
		})
	}
	return out, nil
}

// CreateWorkSkill scaffolds a new Skill file in the project scope.
func (a *App) CreateWorkSkill(tabID string, input work.CreateSkillRequest) (*work.CreateSkillResult, error) {
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return nil, fmt.Errorf("workspace is still starting")
	}
	result := &work.CreateSkillResult{}
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Body = strings.TrimSpace(input.Body)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" || !skill.IsValidName(input.Name) || input.Description == "" || input.Body == "" {
		result.Error = &work.WorkTransportError{Code: "invalid_input", Message: "skill name、description、body 和 requestId 均为必填"}
		return result, nil
	}
	if input.Scope != "" && input.Scope != string(skill.ScopeProject) {
		result.Error = &work.WorkTransportError{Code: "invalid_scope", Message: "Work 步骤只能新建项目 Skill"}
		return result, nil
	}

	store, hasStore := ctrl.GetSkillStore()
	if !hasStore {
		result.Error = &work.WorkTransportError{Code: "not_available", Message: "skill creation store not available"}
		return result, nil
	}

	for _, existing := range ctrl.AllSkills() {
		if existing.Name != input.Name {
			continue
		}
		if existing.Scope == skill.ScopeProject &&
			strings.TrimSpace(existing.Description) == input.Description &&
			strings.TrimSpace(existing.Body) == input.Body {
			result.Skill = workSkillInfo(existing, ctrl.SkillEnabled(existing.Name))
			result.Committed = true
			return result, nil
		}
		result.Error = &work.WorkTransportError{Code: "conflict", Message: fmt.Sprintf("skill %q 已存在且内容不同", input.Name)}
		return result, nil
	}
	stubContent := fmt.Sprintf("---\ndescription: %s\n---\n\n%s\n", strconv.Quote(input.Description), input.Body)
	_, createErr := store.CreateWithContent(input.Name, skill.ScopeProject, stubContent)
	if createErr != nil {
		if os.IsExist(createErr) || strings.Contains(createErr.Error(), "already exists") {
			existing, ok := store.Read(input.Name)
			if ok && strings.TrimSpace(existing.Description) == input.Description && strings.TrimSpace(existing.Body) == input.Body {
				result.Skill = workSkillInfo(existing, true)
				result.Committed = true
				return result, nil
			}
			result.Error = &work.WorkTransportError{Code: "conflict", Message: createErr.Error()}
			return result, nil
		}
		result.Error = &work.WorkTransportError{Code: "create_failed", Message: createErr.Error()}
		return result, nil
	}

	sk, ok := store.Read(input.Name)
	if !ok {
		result.Error = &work.WorkTransportError{Code: "read_error", Message: fmt.Sprintf("skill %q not found after creation", input.Name)}
		return result, nil
	}
	result.Skill = &work.SkillInfo{
		Name: sk.Name, Description: sk.Description,
		Scope: string(sk.Scope), Enabled: true, RunAs: string(sk.RunAs),
	}
	result.Committed = true
	return result, nil
}

func workSkillInfo(sk skill.Skill, enabled bool) *work.SkillInfo {
	return &work.SkillInfo{
		Name: sk.Name, Description: sk.Description, Scope: string(sk.Scope),
		Enabled: enabled, RunAs: string(sk.RunAs),
	}
}

// PreviewArtifact produces a graded ArtifactPreview for the given artifact
// reference in a work. The preview is read-only, cached by content digest,
// and degrades safely when no converter is available.
func (a *App) PreviewArtifact(tabID string, input work.PreviewArtifactRequest) (*work.PreviewArtifactResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.PreviewArtifactResult{}
		bindWorkTransportError(nil, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.PreviewArtifact(a.bootContext(), input)
	if result == nil {
		result = &work.PreviewArtifactResult{}
	}
	bindWorkTransportError(nil, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

// RequestArtifactConversion executes or resumes async conversion with
// external-approval gating.
func (a *App) RequestArtifactConversion(tabID string, input work.RequestArtifactConversionInput) (*work.RequestArtifactConversionResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		result := &work.RequestArtifactConversionResult{}
		bindWorkTransportError(nil, &result.Committed, &result.Recoverable, &result.TransportError, err)
		return result, nil
	}
	result, callErr := wc.RequestArtifactConversion(a.bootContext(), input)
	if result == nil {
		result = &work.RequestArtifactConversionResult{}
	}
	bindWorkTransportError(nil, &result.Committed, &result.Recoverable, &result.TransportError, callErr)
	return result, nil
}

func bindWorkTransportError(
	revision *int64,
	committed, recoverable *bool,
	target **work.WorkTransportError,
	err error,
) {
	if err == nil {
		return
	}
	transport := work.TransportErrorFrom(err)
	*target = transport
	if revision != nil && *revision == 0 {
		*revision = transport.Revision
	}
	if committed != nil {
		*committed = *committed || transport.Committed
	}
	if recoverable != nil {
		*recoverable = transport.Recoverable
	}
}
