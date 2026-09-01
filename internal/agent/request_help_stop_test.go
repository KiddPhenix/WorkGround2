package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/config"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

type stoppedHelpProvider struct {
	started   chan struct{}
	stopped   chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

func (p *stoppedHelpProvider) Name() string { return "help" }

func (p *stoppedHelpProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	chunks := make(chan provider.Chunk)
	p.startOnce.Do(func() { close(p.started) })
	go func() {
		<-ctx.Done()
		p.stopOnce.Do(func() { close(p.stopped) })
		close(chunks)
	}()
	return chunks, nil
}

func TestRequestHelpCancelDuringRun(t *testing.T) {
	for _, tc := range []struct {
		capability string
		candidates int
	}{
		{"web_search", 1},
		{"web_search", 2},
		{"image_generation", 2},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.capability, tc.candidates), func(t *testing.T) {
			cfg := config.Default()
			cfg.Agent.AssistModels = map[string][]string{}
			for i := range tc.candidates {
				name := fmt.Sprintf("help%d", i)
				cfg.Providers = append(cfg.Providers, config.ProviderEntry{
					Name: name, Kind: "openai", Model: "test", Capabilities: []string{tc.capability},
				})
				cfg.Agent.AssistModels[tc.capability] = append(cfg.Agent.AssistModels[tc.capability], name+"/test")
			}
			prov := &stoppedHelpProvider{started: make(chan struct{}), stopped: make(chan struct{})}
			var resolves atomic.Int32
			helper := newTestRequestHelpTool(t, cfg, prov, tool.NewRegistry(), func(string, string) (provider.Provider, *provider.Pricing, int, error) {
				resolves.Add(1)
				return prov, nil, 0, nil
			})
			ctx, cancel := context.WithCancel(testRequestHelpContext())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := helper.Execute(ctx, []byte(fmt.Sprintf(`{"capability":%q,"prompt":"test stop"}`, tc.capability)))
				result <- err
			}()
			select {
			case <-prov.started:
			case err := <-result:
				t.Fatalf("helper exited before provider started: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("provider did not start")
			}
			cancel()
			select {
			case <-prov.stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("provider did not receive cancellation")
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("helper error = %v, want context.Canceled", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("helper did not stop")
			}
			if got := resolves.Load(); got != 1 {
				t.Errorf("resolved %d candidates, want only the interrupted candidate", got)
			}
		})
	}
}
