package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// ProfileMode selects the profile strategy for a browser session.
type ProfileMode string

const (
	ProfileEphemeral ProfileMode = "ephemeral"
	ProfileManaged   ProfileMode = "managed"
	ProfileAttach    ProfileMode = "attach"
)

// ProfileRequest is the input to ProfileProvider.Acquire.
type ProfileRequest struct {
	OwnerID   string
	Kind      BrowserKind
	Headless  bool
	Workspace string
}

// ProfileLease is the acquired profile for a browser session.
type ProfileLease struct {
	ID          string
	Mode        ProfileMode
	UserDataDir string
	ProfileName string
	DebugURL    string
	OwnProcess  bool
	Persistent  bool
}

// ProfileProvider acquires and releases browser profiles.
type ProfileProvider interface {
	Acquire(ctx context.Context, req ProfileRequest) (ProfileLease, error)
	Release(ctx context.Context, lease ProfileLease) error
}

// ephemeralProfileProvider is the default V1 provider: temporary user-data-dir.
type ephemeralProfileProvider struct {
	root string
	mu   sync.Mutex
	dirs map[string]bool // tracks dirs we created (for cleanup)
}

// NewEphemeralProfileProvider creates a provider that uses temp directories.
func NewEphemeralProfileProvider(root string) ProfileProvider {
	return &ephemeralProfileProvider{
		root: root,
		dirs: make(map[string]bool),
	}
}

func (p *ephemeralProfileProvider) Acquire(ctx context.Context, req ProfileRequest) (ProfileLease, error) {
	if err := ctx.Err(); err != nil {
		return ProfileLease{}, err
	}
	if p.root != "" {
		if err := os.MkdirAll(p.root, 0o700); err != nil {
			return ProfileLease{}, fmt.Errorf("create profile root: %w", err)
		}
	}
	dir, err := os.MkdirTemp(p.root, "wg2-browser-*")
	if err != nil {
		return ProfileLease{}, fmt.Errorf("create temp profile dir: %w", err)
	}

	p.mu.Lock()
	p.dirs[dir] = true
	p.mu.Unlock()

	return ProfileLease{
		ID:          req.OwnerID,
		Mode:        ProfileEphemeral,
		UserDataDir: dir,
		ProfileName: "Default",
		OwnProcess:  true,
		Persistent:  false,
	}, nil
}

func (p *ephemeralProfileProvider) Release(ctx context.Context, lease ProfileLease) error {
	if lease.UserDataDir == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	owned := p.dirs[lease.UserDataDir]
	p.mu.Unlock()

	if owned {
		var err error
		backoff := 20 * time.Millisecond
		for {
			err = os.RemoveAll(lease.UserDataDir)
			if err == nil || os.IsNotExist(err) {
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("remove ephemeral profile %q: %w", lease.UserDataDir, errors.Join(err, ctx.Err()))
			case <-time.After(backoff):
				if backoff < 500*time.Millisecond {
					backoff *= 2
				}
			}
		}
		p.mu.Lock()
		delete(p.dirs, lease.UserDataDir)
		p.mu.Unlock()
	}
	return nil
}
