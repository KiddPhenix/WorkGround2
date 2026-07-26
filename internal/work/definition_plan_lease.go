package work

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AcquireDefinitionPlanLease serializes one natural-language planning request
// across FileWorkStore instances and processes. The OS-backed lease is held
// from receipt lookup through provider execution and candidate commit, so a
// second process reloads the winner's durable receipt instead of paying for a
// duplicate model call. A crashed owner releases the OS lock automatically.
func (s *FileWorkStore) AcquireDefinitionPlanLease(
	ctx context.Context,
	workID, requestID string,
) (func() error, error) {
	if s == nil {
		return nil, errors.New("work: definition plan lease store is nil")
	}
	if ctx == nil {
		return nil, errors.New("work: definition plan lease context is nil")
	}
	if err := validateWorkID(workID); err != nil {
		return nil, err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, errors.New("work: definition plan lease requestID is required")
	}
	sum := sha256.Sum256([]byte(requestID))
	lockDir := filepath.Join(
		s.workDir,
		".locks",
		"definition-plans",
		workID,
		fmt.Sprintf("%x", sum[:16]),
	)
	localKey := "definition-plan:" + lockDir
	value, _ := workOpLocks.LoadOrStore(localKey, &definitionPlanLocalLock{})
	local := value.(*definitionPlanLocalLock)
	if err := local.lock(ctx); err != nil {
		return nil, err
	}

	for {
		err := AcquireWorkLease(lockDir)
		if err == nil {
			releaseOp, requireErr := requireLease(lockDir)
			if requireErr != nil {
				releaseErr := releaseStoreLease(lockDir)
				local.unlock()
				return nil, errors.Join(requireErr, releaseErr)
			}
			return func() error {
				releaseOp()
				releaseErr := releaseStoreLease(lockDir)
				local.unlock()
				return releaseErr
			}, nil
		}
		if !errors.Is(err, ErrWorkLeaseHeld) {
			local.unlock()
			return nil, fmt.Errorf("work: acquire definition plan lease: %w", err)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			local.unlock()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type definitionPlanLocalLock struct {
	once  sync.Once
	token chan struct{}
}

func (l *definitionPlanLocalLock) lock(ctx context.Context) error {
	l.once.Do(func() {
		l.token = make(chan struct{}, 1)
		l.token <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.token:
		return nil
	}
}

func (l *definitionPlanLocalLock) unlock() {
	l.token <- struct{}{}
}
