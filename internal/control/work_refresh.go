package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"workground2/internal/nilutil"
	"workground2/internal/work"
)

// WorkBlockSource binds a trusted controller source adapter to a provider and
// optional core block kind. Provider matches take precedence over kind matches.
type WorkBlockSource struct {
	Source   work.BlockSource
	Kind     string
	Adapter  work.BlockSourceAdapter
	Schedule work.RefreshSchedule
}

type workSourceRegistry struct {
	mu         sync.RWMutex
	providers  map[string]WorkBlockSource
	kinds      map[string]WorkBlockSource
	recoverErr string
}

func newWorkSourceRegistry() *workSourceRegistry {
	return &workSourceRegistry{
		providers: make(map[string]WorkBlockSource),
		kinds:     make(map[string]WorkBlockSource),
	}
}

func (r *workSourceRegistry) register(binding WorkBlockSource) error {
	if r == nil {
		return errors.New("work: source registry is unavailable")
	}
	binding.Source.Provider = strings.TrimSpace(binding.Source.Provider)
	binding.Source.Ref = strings.TrimSpace(binding.Source.Ref)
	binding.Source.Mode = strings.TrimSpace(binding.Source.Mode)
	binding.Kind = strings.TrimSpace(binding.Kind)
	if nilutil.IsNil(binding.Adapter) {
		return errors.New("work: source adapter is required")
	}
	if binding.Source.Provider == "" && binding.Kind == "" {
		return errors.New("work: source provider or block kind is required")
	}
	if binding.Source.Provider != "" && !validRefreshProvider(binding.Source.Provider) {
		return fmt.Errorf("work: invalid source provider %q", binding.Source.Provider)
	}
	if binding.Source.Mode == "" {
		binding.Source.Mode = "snapshot"
	}
	if binding.Source.Mode != "snapshot" && binding.Source.Mode != "query" && binding.Source.Mode != "stream" {
		return fmt.Errorf("work: invalid source mode %q", binding.Source.Mode)
	}
	binding.Source.Verified = true
	if binding.Schedule.Interval <= 0 {
		binding.Schedule.Interval = work.DefaultRefreshSchedule().Interval
	}
	if binding.Schedule.Backoff == nil {
		binding.Schedule.Backoff = work.NewExponentialBackoff()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if binding.Source.Provider != "" {
		r.providers[binding.Source.Provider] = binding
	}
	if binding.Kind != "" {
		r.kinds[binding.Kind] = binding
	}
	return nil
}

func (r *workSourceRegistry) resolve(block work.BlockInstance) (work.BlockSourceAdapter, work.BlockSource, work.RefreshSchedule, bool) {
	if r == nil {
		return nil, work.BlockSource{}, work.RefreshSchedule{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	binding, ok := r.providers[strings.TrimSpace(block.Source.Provider)]
	if !ok {
		binding, ok = r.kinds[strings.TrimSpace(block.Kind)]
	}
	if !ok || nilutil.IsNil(binding.Adapter) {
		return nil, work.BlockSource{}, work.RefreshSchedule{}, false
	}
	return binding.Adapter, binding.Source, binding.Schedule, true
}

func (r *workSourceRegistry) hasSources() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers) > 0 || len(r.kinds) > 0
}

func (r *workSourceRegistry) setRecoverError(err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if err == nil {
		r.recoverErr = ""
	} else {
		r.recoverErr = err.Error()
	}
	r.mu.Unlock()
}

func (r *workSourceRegistry) recoveryError() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.recoverErr
}

func validRefreshProvider(provider string) bool {
	if provider == "controller" || provider == "ai" || provider == "user" {
		return true
	}
	for _, prefix := range []string{"tool:", "addon:"} {
		if strings.HasPrefix(provider, prefix) && strings.TrimSpace(strings.TrimPrefix(provider, prefix)) != "" {
			return true
		}
	}
	return false
}

func (c *Controller) initWorkRefresh(bindings []WorkBlockSource, clock work.Clock, offline bool) {
	if c == nil || nilutil.IsNil(c.workSvc) {
		return
	}
	c.workSources = newWorkSourceRegistry()
	c.workRefresh = work.NewBlockRefreshManager(c.workSvc, clock)
	var registrationErr error
	for _, binding := range bindings {
		if err := c.workSources.register(binding); err != nil {
			registrationErr = errors.Join(registrationErr, err)
			continue
		}
	}
	if offline {
		c.workRefresh.SetOnline(false)
	}
	if c.workSources.hasSources() {
		c.recoverWorkRefreshes(context.Background())
	}
	if registrationErr != nil {
		c.workSources.setRecoverError(registrationErr)
	}
}

// RegisterWorkBlockSource adds or replaces one source route and retries
// projection recovery. Repeating the same registration is safe.
func (c *Controller) RegisterWorkBlockSource(binding WorkBlockSource) error {
	if c == nil || nilutil.IsNil(c.workSvc) {
		return errWorkDisabled
	}
	if c.workSources == nil {
		c.workSources = newWorkSourceRegistry()
	}
	if c.workRefresh == nil {
		c.workRefresh = work.NewBlockRefreshManager(c.workSvc, nil)
	}
	if err := c.workSources.register(binding); err != nil {
		return err
	}
	c.recoverWorkRefreshes(context.Background())
	return nil
}

func (c *Controller) recoverWorkRefreshes(ctx context.Context) {
	if c == nil || c.workRefresh == nil || c.workSources == nil || !c.workSources.hasSources() || nilutil.IsNil(c.workSvc) {
		return
	}
	active := work.ArchiveActive
	recoverFailed := false
	filter := work.WorkFilter{ArchiveState: &active, Limit: 500}
	for {
		page, err := c.workSvc.List(ctx, filter)
		if err != nil {
			c.workSources.setRecoverError(fmt.Errorf("work: recover block refresh intents: %w", err))
			return
		}
		for _, item := range page.Items {
			view, getErr := c.workSvc.Get(ctx, item.ID)
			if getErr != nil {
				recoverFailed = true
				c.workSources.setRecoverError(fmt.Errorf("work: recover block refresh %s: %w", item.ID, getErr))
				continue
			}
			c.workRefresh.RecoverFromProjection(view, c.workSources.resolve)
		}
		if len(page.Items) < filter.Limit {
			break
		}
		next := page.Items[len(page.Items)-1].ID
		if next == filter.Cursor {
			c.workSources.setRecoverError(errors.New("work: refresh recovery pagination did not advance"))
			return
		}
		filter.Cursor = next
	}
	if !recoverFailed {
		c.workSources.setRecoverError(nil)
	}
}

// SetWorkOnline gates source traffic. Reconnect keeps subscriptions and wakes
// them for immediate retry.
func (c *Controller) SetWorkOnline(online bool) {
	if c != nil && c.workRefresh != nil {
		c.workRefresh.SetOnline(online)
	}
}

// CancelBlockRefresh persists removal of one polling intent before stopping its
// loop. The request is idempotent and safe to retry after a partial failure.
func (c *Controller) CancelBlockRefresh(ctx context.Context, workID, blockID, requestID string) error {
	if c == nil || nilutil.IsNil(c.workSvc) {
		return errWorkDisabled
	}
	if c.workRefresh != nil {
		c.workRefresh.Unsubscribe(workID, blockID)
	}
	_, err := c.workSvc.CancelBlockRefresh(ctx, workID, blockID, requestID)
	return err
}

// WorkRefreshState exposes measurable retry/singleflight state.
func (c *Controller) WorkRefreshState(workID, blockID string) work.BlockRefreshState {
	if c == nil || c.workRefresh == nil {
		return work.BlockRefreshState{}
	}
	return c.workRefresh.State(workID, blockID)
}

// WorkRefreshRecoveryError reports the last reopen recovery failure.
func (c *Controller) WorkRefreshRecoveryError() string {
	if c == nil || c.workSources == nil {
		return ""
	}
	return c.workSources.recoveryError()
}
