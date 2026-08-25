package assistant

import (
	"strings"
	"testing"
)

func TestRequiredCapabilitiesLiveWeb(t *testing.T) {
	tests := []struct {
		name    string
		mission string
		prompt  string
		want    bool
	}{
		{"plain coding mission", "持续关注项目健康度", "运行 go test 并修复失败", false},
		{"url in prompt", "监控", "检查 https://bbs.nga.cn/read.php 的最新帖子", true},
		{"bare domain", "监控", "去 nga.178.com 查看版面", true},
		{"single label domain", "监控", "检查 example.com 首页", true},
		{"www domain", "监控", "访问 www.example.com 首页", true},
		{"website keyword", "监控", "inspect the online website for new posts", true},
		{"chinese website", "监控", "浏览在线论坛并检查网站", true},
		{"browse online", "监控", "browse the online docs", true},
		{"inspect chinese online", "监控", "检查在线文档", true},
		{"deliver contains live but no web", "写报告", "deliver the report to the team", false},
		{"visit files no web", "写代码", "检查每个文件并修复", false},
		{"build website locally", "写代码", "编写网站代码并运行本地测试", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RequiredCapabilities(tt.mission, tt.prompt)
			hasLiveWeb := false
			for _, c := range got {
				if c == CapabilityLiveWeb {
					hasLiveWeb = true
				}
			}
			if hasLiveWeb != tt.want {
				t.Errorf("RequiredCapabilities(%q, %q) live_web = %v, want %v (got %v)", tt.mission, tt.prompt, hasLiveWeb, tt.want, got)
			}
		})
	}
}

func TestLiveWebTool(t *testing.T) {
	live := []string{"browser_open", "browser_navigate", "browser_state", "browser_click", "browser_scroll", "web_fetch", "web_search"}
	for _, name := range live {
		if !LiveWebTool(name) {
			t.Errorf("LiveWebTool(%q) = false, want true", name)
		}
	}
	notLive := []string{"browser_close", "browser_tab", "browser_type", "browser_attach", "browser_upload", "read_file", "bash", "grep", "web_cache"}
	for _, name := range notLive {
		if LiveWebTool(name) {
			t.Errorf("LiveWebTool(%q) = true, want false", name)
		}
	}
}

func TestEvidenceRecordToolResult(t *testing.T) {
	var e Evidence
	// A dispatch alone is not recorded: RecordToolResult only sees results.
	e.RecordToolResult("web_fetch", false)
	if e.liveWeb {
		t.Fatal("failed web_fetch recorded live_web evidence")
	}
	e.RecordToolResult("read_file", true)
	if e.liveWeb {
		t.Fatal("local tool recorded live_web evidence")
	}
	e.RecordToolResult("browser_navigate", true)
	if !e.Satisfies(CapabilityLiveWeb) {
		t.Fatal("successful browser_navigate did not satisfy live_web")
	}
}

func TestEvidenceMissing(t *testing.T) {
	var e Evidence
	missing := e.Missing([]Capability{CapabilityLiveWeb})
	if len(missing) != 1 || missing[0] != CapabilityLiveWeb {
		t.Fatalf("Missing = %v, want [live_web]", missing)
	}
	e.RecordToolResult("web_search", true)
	if got := e.Missing([]Capability{CapabilityLiveWeb}); len(got) != 0 {
		t.Fatalf("Missing after evidence = %v, want empty", got)
	}
}

func TestEvidenceFailure(t *testing.T) {
	f := EvidenceFailure([]Capability{CapabilityLiveWeb})
	if f.Code != "evidence_missing" || !f.Retryable || !f.OutcomeKnown {
		t.Fatalf("EvidenceFailure = %+v, want code evidence_missing retryable", f)
	}
	if f.Message == "" || !strings.Contains(f.Message, "live_web") {
		t.Fatalf("EvidenceFailure message = %q, want live_web mention", f.Message)
	}
}

func TestFreshCycleDirective(t *testing.T) {
	t.Run("empty plan needs cycle", func(t *testing.T) {
		dir, needed := FreshCycleDirective(Plan{Responsibilities: nil})
		if !needed || dir == "" {
			t.Fatalf("empty plan: needed=%v directive=%q", needed, dir)
		}
	})
	t.Run("all done needs cycle", func(t *testing.T) {
		plan := Plan{Responsibilities: []Responsibility{
			{ID: "r1", Status: RespDone},
			{ID: "r2", Status: RespDone},
		}}
		dir, needed := FreshCycleDirective(plan)
		if !needed || dir == "" {
			t.Fatalf("all done: needed=%v directive=%q", needed, dir)
		}
	})
	t.Run("ready item needs no cycle", func(t *testing.T) {
		plan := Plan{Responsibilities: []Responsibility{
			{ID: "r1", Status: RespDone},
			{ID: "r2", Status: RespReady},
		}}
		dir, needed := FreshCycleDirective(plan)
		if needed || dir != "" {
			t.Fatalf("ready item: needed=%v directive=%q", needed, dir)
		}
	})
	t.Run("active item needs no cycle", func(t *testing.T) {
		plan := Plan{Responsibilities: []Responsibility{{ID: "r1", Status: RespActive}}}
		if _, needed := FreshCycleDirective(plan); needed {
			t.Fatal("active item incorrectly flagged for fresh cycle")
		}
	})
}
