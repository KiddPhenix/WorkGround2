package assistant

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaderElectorSingleLeaderRenewAndExpiryTakeover(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	now := testEpoch
	one, _ := NewLeaderElector(root, "desktop:1", time.Minute)
	two, _ := NewLeaderElector(root, "daemon:2", time.Minute)
	lease1, leader, err := one.Acquire(now)
	if err != nil || !leader {
		t.Fatalf("one acquire=%+v leader=%v err=%v", lease1, leader, err)
	}
	current, leader, err := two.Acquire(now.Add(10 * time.Second))
	if err != nil || leader || current.Fence != lease1.Fence {
		t.Fatalf("two early=%+v leader=%v err=%v", current, leader, err)
	}
	renewed, err := one.Renew(lease1, now.Add(20*time.Second))
	if err != nil || !renewed.LeaseUntil.After(lease1.LeaseUntil) {
		t.Fatalf("renew=%+v err=%v", renewed, err)
	}
	lease2, leader, err := two.Acquire(renewed.LeaseUntil.Add(time.Second))
	if err != nil || !leader || lease2.Fence == lease1.Fence {
		t.Fatalf("takeover=%+v leader=%v err=%v", lease2, leader, err)
	}
	if _, err := one.Renew(lease1, now.Add(2*time.Minute)); !errors.Is(err, ErrLeaderLost) {
		t.Fatalf("stale renew err=%v", err)
	}
	if err := two.Release(lease2); err != nil {
		t.Fatal(err)
	}
}
