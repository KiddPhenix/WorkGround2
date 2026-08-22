package bot

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"workground2/internal/config"
)

// Guards pairingMu + the atomic savePairingFile: concurrent offerPairing
// dispatch goroutines used to load-modify-save pairing.json without a lock and
// overwrite each other's requests. Run with -race.
func TestCreateOrRefreshPairingRequestConcurrent(t *testing.T) {
	t.Setenv("WorkGround2_HOME", t.TempDir())
	cfg := PairingConfig{Enabled: true, RequestTTL: time.Hour, MaxPendingPerPlatform: 64}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := InboundMessage{
				Platform: PlatformFeishu,
				ChatType: ChatDM,
				ChatID:   fmt.Sprintf("chat-%d", i),
				UserID:   fmt.Sprintf("user-%d", i),
			}
			for j := 0; j < 5; j++ {
				if _, _, err := CreateOrRefreshPairingRequest(msg, cfg); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent pairing request failed: %v", err)
	}

	reqs, err := ListPairingRequests()
	if err != nil {
		t.Fatalf("list pairing requests: %v", err)
	}
	if len(reqs) != workers {
		t.Fatalf("pairing store lost concurrent writes: got %d requests, want %d", len(reqs), workers)
	}
}

// TestApprovePairingCodeKeepsRequestWhenConfigSaveFails drives the approve flow
// with a config path that cannot be written: the pending request must survive
// the failed save, and re-approving the same code after the failure is fixed
// must succeed with the same code.
func TestApprovePairingCodeKeepsRequestWhenConfigSaveFails(t *testing.T) {
	t.Setenv("WorkGround2_HOME", t.TempDir())

	msg := InboundMessage{
		Platform:     PlatformFeishu,
		ConnectionID: "feishu-main",
		ChatType:     ChatDM,
		ChatID:       "oc_chat",
		UserID:       "ou_user_1",
		UserName:     "user one",
	}
	req, created, err := CreateOrRefreshPairingRequest(msg, PairingConfig{Enabled: true, RequestTTL: time.Hour, MaxPendingPerPlatform: 4})
	if err != nil || !created {
		t.Fatalf("create pairing request: created=%v err=%v", created, err)
	}
	code := req.Code

	// Seed the user config with a matching connection so the approve path
	// writes connection-level access (the real-world scenario).
	userPath := config.UserConfigPath()
	if userPath == "" {
		t.Fatal("UserConfigPath is empty")
	}
	seed := config.Default()
	seed.Bot.Connections = []config.BotConnectionConfig{{
		ID:       "feishu-main",
		Provider: "feishu",
		Enabled:  true,
		Access:   config.BotAccessConfig{PairingEnabled: true},
	}}
	if err := seed.SaveTo(userPath); err != nil {
		t.Fatalf("seed user config: %v", err)
	}

	// Break the config path so SaveTo fails: a directory at the config path
	// makes the atomic rename fail while the pairing store stays untouched.
	if err := os.Remove(userPath); err != nil {
		t.Fatalf("remove seeded config: %v", err)
	}
	if err := os.MkdirAll(userPath, 0o700); err != nil {
		t.Fatalf("make config path a directory: %v", err)
	}
	if _, err := ApprovePairingCode(code); err == nil {
		t.Fatal("approve succeeded although config save must fail")
	}
	// The request must still be pending with the same code.
	reqs, err := ListPairingRequests()
	if err != nil {
		t.Fatalf("list pairing requests: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Code != code {
		t.Fatalf("pairing request lost after failed approve: %+v", reqs)
	}

	// Fix the config path and retry with the same code. The failed approve
	// never wrote the config (LoadForEdit fell back to defaults), so restore
	// the writable seeded config before the retry.
	if err := os.Remove(userPath); err != nil {
		t.Fatalf("remove broken config path: %v", err)
	}
	if err := seed.SaveTo(userPath); err != nil {
		t.Fatalf("restore seeded config: %v", err)
	}
	approved, err := ApprovePairingCode(code)
	if err != nil {
		t.Fatalf("approve after fixing config: %v", err)
	}
	if approved.Code != code {
		t.Fatalf("approved request code = %q, want %q", approved.Code, code)
	}
	if reqs, err := ListPairingRequests(); err != nil || len(reqs) != 0 {
		t.Fatalf("pairing request not removed after successful approve: %+v err=%v", reqs, err)
	}
	// The connection access was persisted (also exercising the render
	// round-trip for [bot.connections.access]).
	reloaded := config.LoadForEdit(userPath)
	found := false
	for _, conn := range reloaded.Bot.Connections {
		if conn.ID == "feishu-main" {
			found = true
			if !stringSliceContains(conn.Access.Users, "ou_user_1") {
				t.Fatalf("connection access users = %+v, want ou_user_1", conn.Access.Users)
			}
		}
	}
	if !found {
		t.Fatal("seeded connection missing after approve")
	}

	// Re-approving a consumed code fails without touching access again.
	if _, err := ApprovePairingCode(code); err == nil {
		t.Fatal("re-approving a consumed code succeeded; want not-found error")
	}
}

// TestApprovePairingCodeRepeatedGrantsStayIdempotent grants the same user
// twice through fresh requests: the connection access must not accumulate
// duplicate user entries.
func TestApprovePairingCodeRepeatedGrantsStayIdempotent(t *testing.T) {
	t.Setenv("WorkGround2_HOME", t.TempDir())

	userPath := config.UserConfigPath()
	if userPath == "" {
		t.Fatal("UserConfigPath is empty")
	}
	seed := config.Default()
	seed.Bot.Connections = []config.BotConnectionConfig{{
		ID:       "feishu-main",
		Provider: "feishu",
		Enabled:  true,
		Access:   config.BotAccessConfig{PairingEnabled: true},
	}}
	if err := seed.SaveTo(userPath); err != nil {
		t.Fatalf("seed user config: %v", err)
	}

	for i := 0; i < 2; i++ {
		msg := InboundMessage{
			Platform:     PlatformFeishu,
			ConnectionID: "feishu-main",
			ChatType:     ChatDM,
			ChatID:       "oc_chat",
			UserID:       "ou_user_1",
		}
		req, created, err := CreateOrRefreshPairingRequest(msg, PairingConfig{Enabled: true, RequestTTL: time.Hour, MaxPendingPerPlatform: 4})
		if err != nil || !created {
			t.Fatalf("create pairing request %d: created=%v err=%v", i, created, err)
		}
		if _, err := ApprovePairingCode(req.Code); err != nil {
			t.Fatalf("approve pairing request %d: %v", i, err)
		}
	}

	reloaded := config.LoadForEdit(userPath)
	var access config.BotAccessConfig
	for _, conn := range reloaded.Bot.Connections {
		if conn.ID == "feishu-main" {
			access = conn.Access
		}
	}
	if len(access.Users) != 1 || access.Users[0] != "ou_user_1" {
		t.Fatalf("connection access users = %+v, want single ou_user_1", access.Users)
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
