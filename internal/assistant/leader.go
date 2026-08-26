package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/fileutil"
)

var ErrLeaderLost = errors.New("assistant: runtime leader lease lost")

type LeaderLease struct {
	Owner      string    `json:"owner"`
	Fence      string    `json:"fence"`
	LeaseUntil time.Time `json:"lease_until" ts_type:"string"`
	UpdatedAt  time.Time `json:"updated_at" ts_type:"string"`
}

// LeaderElector coordinates Desktop and the headless local daemon through an
// atomic lock directory. Run leases remain the final safety fence.
type LeaderElector struct {
	root, lockDir, owner string
	ttl                  time.Duration
}

func NewLeaderElector(storeRoot, owner string, ttl time.Duration) (*LeaderElector, error) {
	if !filepath.IsAbs(storeRoot) {
		return nil, errors.New("assistant: leader store root must be absolute")
	}
	if strings.TrimSpace(owner) == "" {
		return nil, errors.New("assistant: leader owner is required")
	}
	if ttl < 15*time.Second {
		return nil, errors.New("assistant: leader TTL must be at least 15 seconds")
	}
	runtimeRoot := filepath.Join(filepath.Clean(storeRoot), ".runtime")
	return &LeaderElector{root: runtimeRoot, lockDir: filepath.Join(runtimeRoot, "leader.lock"), owner: strings.TrimSpace(owner), ttl: ttl}, nil
}

func (e *LeaderElector) Acquire(now time.Time) (LeaderLease, bool, error) {
	if err := os.MkdirAll(e.root, 0o700); err != nil {
		return LeaderLease{}, false, fmt.Errorf("assistant: create leader runtime root: %w", err)
	}
	unlock, err := lockStoreFile(filepath.Join(e.root, "leader.guard.lock"), 5*time.Second)
	if err != nil {
		return LeaderLease{}, false, err
	}
	defer unlock()
	now = storeNow(now)
	for attempt := 0; attempt < 3; attempt++ {
		if err := os.Mkdir(e.lockDir, 0o700); err == nil {
			lease := LeaderLease{Owner: e.owner, Fence: StableID("leader-fence", fmt.Sprintf("%s/%d", e.owner, now.UnixNano())), LeaseUntil: now.Add(e.ttl), UpdatedAt: now}
			if err := e.write(lease); err != nil {
				_ = os.Remove(e.lockDir)
				return LeaderLease{}, false, err
			}
			return lease, true, nil
		} else if !os.IsExist(err) {
			return LeaderLease{}, false, fmt.Errorf("assistant: acquire leader directory: %w", err)
		}
		current, err := e.Current()
		if err != nil {
			return LeaderLease{}, false, err
		}
		if current.Owner == e.owner {
			current.LeaseUntil, current.UpdatedAt = now.Add(e.ttl), now
			if err := e.writeIfOwned(current); err != nil {
				if errors.Is(err, ErrLeaderLost) {
					continue
				}
				return LeaderLease{}, false, err
			}
			return current, true, nil
		}
		if current.LeaseUntil.After(now) {
			return current, false, nil
		}
		stale := filepath.Join(e.root, ".stale-"+StableID("lease", current.Fence+fmt.Sprint(now.UnixNano())))
		if err := os.Rename(e.lockDir, stale); err != nil {
			continue
		}
		_ = os.RemoveAll(stale)
	}
	return LeaderLease{}, false, errors.New("assistant: leader contention did not settle")
}

func (e *LeaderElector) Renew(lease LeaderLease, now time.Time) (LeaderLease, error) {
	unlock, err := e.guard()
	if err != nil {
		return LeaderLease{}, err
	}
	defer unlock()
	current, err := e.Current()
	if err != nil {
		return LeaderLease{}, err
	}
	if current.Owner != e.owner || current.Fence != lease.Fence {
		return LeaderLease{}, ErrLeaderLost
	}
	now = storeNow(now)
	current.LeaseUntil, current.UpdatedAt = now.Add(e.ttl), now
	if err := e.writeIfOwned(current); err != nil {
		return LeaderLease{}, err
	}
	return current, nil
}

func (e *LeaderElector) Release(lease LeaderLease) error {
	unlock, err := e.guard()
	if err != nil {
		return err
	}
	defer unlock()
	current, err := e.Current()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Owner != e.owner || current.Fence != lease.Fence {
		return ErrLeaderLost
	}
	released := filepath.Join(e.root, ".released-"+StableID("lease", lease.Fence))
	if err := os.Rename(e.lockDir, released); err != nil {
		return err
	}
	return os.RemoveAll(released)
}

func (e *LeaderElector) guard() (func(), error) {
	if err := os.MkdirAll(e.root, 0o700); err != nil {
		return nil, fmt.Errorf("assistant: create leader runtime root: %w", err)
	}
	return lockStoreFile(filepath.Join(e.root, "leader.guard.lock"), 5*time.Second)
}

func (e *LeaderElector) Current() (LeaderLease, error) {
	path := filepath.Join(e.lockDir, "lease.json")
	info, err := os.Lstat(path)
	if err != nil {
		return LeaderLease{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return LeaderLease{}, errors.New("assistant: unsafe leader lease file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LeaderLease{}, err
	}
	var lease LeaderLease
	if err := json.Unmarshal(data, &lease); err != nil {
		return LeaderLease{}, fmt.Errorf("assistant: parse leader lease: %w", err)
	}
	if lease.Owner == "" || lease.Fence == "" || lease.LeaseUntil.IsZero() {
		return LeaderLease{}, errors.New("assistant: invalid leader lease")
	}
	return lease, nil
}

func (e *LeaderElector) writeIfOwned(next LeaderLease) error {
	current, err := e.Current()
	if err != nil {
		return err
	}
	if current.Owner != next.Owner || current.Fence != next.Fence {
		return ErrLeaderLost
	}
	return e.write(next)
}

func (e *LeaderElector) write(lease LeaderLease) error {
	data, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fileutil.AtomicWriteFile(filepath.Join(e.lockDir, "lease.json"), data, 0o600)
}
