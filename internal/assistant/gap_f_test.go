package assistant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 旧存储迁移：Mode/Policy/Disposition 稳定且可重放
// ---------------------------------------------------------------------------

// legacyAggregateJSON renders an aggregate in the pre-Mode/pre-Disposition
// format: responsibilities carry only the old "status" execution state, the
// assistant has no "mode", and the policy predates the newer dimensions.
func legacyAggregateJSON(t *testing.T, id string) []byte {
	t.Helper()
	now := testEpoch.Format(time.RFC3339Nano)
	agg := map[string]any{
		"version":  1,
		"revision": 3,
		"assistant": map[string]any{
			"id": id, "name": "legacy helper", "mission": "keep the project healthy",
			"scope": "global", "lifecycle": "active",
			"policy":          map[string]any{"local_write": "deny", "network": "deny", "publish": "approve", "delete": "approve", "payment": "approve", "secrets": "approve", "private_data": "approve"},
			"memory_revision": 1, "revision": 3,
			"created_at": now, "updated_at": now,
		},
		"memory": map[string]any{"revision": 1, "items": []any{}},
		"runs":   []any{}, "fires": []any{}, "attention": []any{},
		"plan": map[string]any{
			"revision": 2,
			"responsibilities": []any{
				map[string]any{
					"id": "resp-legacy-1", "assistant_id": id, "alias": "scan",
					"objective": "scan the repo", "status": "active",
					"revision": 2, "created_at": now, "updated_at": now,
				},
				map[string]any{
					"id": "resp-legacy-2", "assistant_id": id, "alias": "fix",
					"objective": "fix findings", "status": "blocked", "depends_on": []string{"resp-legacy-1"},
					"block_reason": "waiting on: scan", "revision": 1, "created_at": now, "updated_at": now,
				},
				map[string]any{
					"id": "resp-legacy-3", "assistant_id": id, "alias": "done-item",
					"objective": "finished work", "status": "done",
					"revision": 1, "created_at": now, "updated_at": now,
				},
				map[string]any{
					"id": "resp-legacy-4", "assistant_id": id, "alias": "failed-item",
					"objective": "failed work", "status": "failed",
					"revision": 1, "created_at": now, "updated_at": now,
				},
			},
		},
		"artifacts": []any{}, "opportunities": []any{}, "experiments": []any{}, "research": []any{},
		"proposals": []any{}, "channels": []any{}, "channel_actions": []any{}, "channel_metrics": []any{},
		"dispatches": []any{}, "jobs": []any{}, "context_packs": []any{}, "ideas": []any{},
		"requests": map[string]any{}, "occurrences": map[string]any{}, "decisions": []any{},
		"updated_at": now,
	}
	data, err := json.MarshalIndent(agg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeLegacyAggregate(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aggregate.json"), legacyAggregateJSON(t, id), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyAggregateMigrationIsStableAndReplayable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	writeLegacyAggregate(t, root, "legacy-a")
	store := testStore(t, root)

	snap, err := store.Get("legacy-a")
	if err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	// 默认值明确：旧数据 Mode -> continuous，Policy 新维度补齐默认。
	if snap.Assistant.Mode != ModeContinuous {
		t.Fatalf("legacy mode = %q, want continuous", snap.Assistant.Mode)
	}
	p := snap.Assistant.Policy
	if p.AutoAnswer != AutoAnswerAuto || p.Isolation != AccessAllow || p.MaxConcurrentSessions != defaultMaxConcurrentSessions {
		t.Fatalf("legacy policy not normalized: %+v", p)
	}
	byAlias := map[string]Responsibility{}
	for _, r := range snap.Plan.Responsibilities {
		byAlias[r.Alias] = r
	}
	// 决策态迁移：active/ready -> planned，blocked -> waiting，done -> done，
	// failed -> planned（可恢复）。
	if got := byAlias["scan"].Disposition; got != DispositionPlanned {
		t.Fatalf("scan disposition = %q, want planned", got)
	}
	if got := byAlias["fix"].Disposition; got != DispositionWaiting {
		t.Fatalf("fix disposition = %q, want waiting", got)
	}
	if got := byAlias["done-item"].Disposition; got != DispositionDone {
		t.Fatalf("done-item disposition = %q, want done", got)
	}
	if got := byAlias["failed-item"].Disposition; got != DispositionPlanned {
		t.Fatalf("failed-item disposition = %q, want planned (recoverable)", got)
	}
	// 派生投影：waiting -> blocked，依赖未满足；done 终态。
	if got := byAlias["fix"].Status; got != RespBlocked {
		t.Fatalf("fix derived status = %q, want blocked", got)
	}
	if got := byAlias["done-item"].Status; got != RespDone {
		t.Fatalf("done-item derived status = %q, want done", got)
	}
	if got := byAlias["scan"].Status; got != RespReady {
		t.Fatalf("scan derived status = %q, want ready", got)
	}

	// 触发一次写入（RecordProgress 幂等 no-op 也会走 write 前的校验），然后重开
	// store 重放：迁移结果一致，不重复、不漂移。
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "replay-1", AssistantID: "legacy-a", Now: testEpoch.Add(time.Minute),
	}); err != nil {
		t.Fatalf("RecordProgress replay: %v", err)
	}
	reopened := testStore(t, root)
	snap2, err := reopened.Get("legacy-a")
	if err != nil {
		t.Fatalf("Get after replay: %v", err)
	}
	for _, r := range snap2.Plan.Responsibilities {
		before := byAlias[r.Alias]
		if before.Disposition != r.Disposition {
			t.Fatalf("replay changed disposition of %s: %q -> %q", r.Alias, before.Disposition, r.Disposition)
		}
		if before.Status != r.Status {
			t.Fatalf("replay changed derived status of %s: %q -> %q", r.Alias, before.Status, r.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// Plan 仅持久决策态：执行态不再写回
// ---------------------------------------------------------------------------

func TestPlanPersistsOnlyDecisionStates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")

	// New flow: a managed Session completed work on "job-a" and reported an
	// active marker on "job-b" plus a completion for "job-a".
	err := store.RecordProgress(RecordProgressInput{
		RequestID: "prog-1", AssistantID: "helper-a", SessionID: "sess-1", Now: testEpoch.Add(time.Minute),
		Progress: ProgressBlock{
			Responsibilities: []RespDecl{
				{Alias: "job-a", Objective: "finish job a", DoneCriteria: "verified"},
				{Alias: "job-b", Objective: "finish job b", DependsOn: []string{"job-a"}},
			},
			Active:   []string{"job-b"},
			Complete: []string{"job-a"},
		},
	})
	if err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}

	snap, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	byAlias := map[string]Responsibility{}
	for _, r := range snap.Plan.Responsibilities {
		byAlias[r.Alias] = r
	}
	if got := byAlias["job-a"].Disposition; got != DispositionDone {
		t.Fatalf("job-a disposition = %q, want done", got)
	}
	if got := byAlias["job-b"].Disposition; got != DispositionPlanned {
		t.Fatalf("job-b disposition = %q, want planned", got)
	}
	// 执行态不写回：没有 active 持久；job-b 是 ready（依赖 job-a 已完成）。
	if got := byAlias["job-b"].Status; got != RespReady {
		t.Fatalf("job-b derived status = %q, want ready", got)
	}
	for _, r := range snap.Plan.Responsibilities {
		if r.Status == RespActive {
			t.Fatalf("responsibility %s persisted active execution state", r.Alias)
		}
	}

	// 直接断言持久 JSON：不含 "status":"active"；disposition 是权威字段。
	raw, err := os.ReadFile(filepath.Join(root, "helper-a", "aggregate.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"status": "active"`) || strings.Contains(string(raw), `"status":"active"`) {
		t.Fatalf("persisted aggregate contains active execution state:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"disposition": "done"`) {
		t.Fatalf("persisted aggregate lacks the done decision state:\n%s", raw)
	}

	// 重放同一 request_id 与同一输入：幂等，不产生第二次变化。
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "prog-1", AssistantID: "helper-a", SessionID: "sess-1", Now: testEpoch.Add(2 * time.Minute),
		Progress: ProgressBlock{
			Responsibilities: []RespDecl{
				{Alias: "job-a", Objective: "finish job a", DoneCriteria: "verified"},
				{Alias: "job-b", Objective: "finish job b", DependsOn: []string{"job-a"}},
			},
			Active:   []string{"job-b"},
			Complete: []string{"job-a"},
		},
	}); err != nil {
		t.Fatalf("RecordProgress replay: %v", err)
	}
	snap2, _ := store.Get("helper-a")
	if snap2.Plan.Revision != snap.Plan.Revision {
		t.Fatalf("replay bumped plan revision: %d -> %d", snap.Plan.Revision, snap2.Plan.Revision)
	}
}

func TestSetResponsibilityDispositionLifecycle(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	store.RecordProgress(RecordProgressInput{
		RequestID: "prog-1", AssistantID: "helper-a", Now: testEpoch.Add(time.Minute),
		Progress: ProgressBlock{Responsibilities: []RespDecl{{Alias: "job-a", Objective: "ship it"}}},
	})
	snap, _ := store.Get("helper-a")
	resp := snap.Plan.Responsibilities[0]

	// planned -> review（工作完成待验证）。
	got, err := store.SetResponsibilityDisposition(SetResponsibilityDispositionInput{
		RequestID: "set-1", AssistantID: "helper-a", ResponsibilityID: resp.ID,
		Disposition: DispositionReview, ExpectedPlanRev: snap.Plan.Revision, Now: testEpoch.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("set review: %v", err)
	}
	if got.Disposition != DispositionReview {
		t.Fatalf("disposition = %q, want review", got.Disposition)
	}
	// review -> done（验证通过，终态）。
	snap, _ = store.Get("helper-a")
	revSet2 := snap.Plan.Revision
	if _, err := store.SetResponsibilityDisposition(SetResponsibilityDispositionInput{
		RequestID: "set-2", AssistantID: "helper-a", ResponsibilityID: resp.ID,
		Disposition: DispositionDone, ExpectedPlanRev: revSet2, Now: testEpoch.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("set done: %v", err)
	}
	// 终态保护：done 之后不能回退。
	snap, _ = store.Get("helper-a")
	if _, err := store.SetResponsibilityDisposition(SetResponsibilityDispositionInput{
		RequestID: "set-3", AssistantID: "helper-a", ResponsibilityID: resp.ID,
		Disposition: DispositionPlanned, ExpectedPlanRev: snap.Plan.Revision, Now: testEpoch.Add(4 * time.Minute),
	}); !errors.Is(err, ErrTransition) {
		t.Fatalf("terminal transition error = %v, want ErrTransition", err)
	}
	// 幂等重放：相同 request 与相同输入返回同一结果。
	again, err := store.SetResponsibilityDisposition(SetResponsibilityDispositionInput{
		RequestID: "set-2", AssistantID: "helper-a", ResponsibilityID: resp.ID,
		Disposition: DispositionDone, ExpectedPlanRev: revSet2, Now: testEpoch.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("set done replay: %v", err)
	}
	if again.Disposition != DispositionDone {
		t.Fatalf("replay disposition = %q", again.Disposition)
	}
}

// ---------------------------------------------------------------------------
// 扩展触发：全部 5 类
// ---------------------------------------------------------------------------

func TestEvaluateExpansionAllFiveTriggers(t *testing.T) {
	now := testEpoch.Add(48 * time.Hour)
	empty := Snapshot{Plan: emptyPlan()}

	// plan_empty：无可执行责任且无运行 Session。
	triggers := EvaluateExpansion(empty, now)
	if !containsTrigger(triggers, ExpansionPlanEmpty) {
		t.Fatalf("plan_empty not detected: %v", triggers)
	}

	// stalled：有可执行责任但 6 小时无进展、无运行。
	stalled := Snapshot{Plan: Plan{Revision: 1, Responsibilities: []Responsibility{{
		ID: "r1", Status: RespReady, Disposition: DispositionPlanned,
		UpdatedAt: now.Add(-7 * time.Hour), Revision: 1,
	}}}}
	triggers = EvaluateExpansion(stalled, now)
	if !containsTrigger(triggers, ExpansionStalled) {
		t.Fatalf("stalled not detected: %v", triggers)
	}

	// repeated_failure：>=2 个失败 Session。
	failed := Snapshot{Plan: emptyPlan()}
	triggers = EvaluateExpansion(failed, now, ExpansionLive{Failed: 2})
	if !containsTrigger(triggers, ExpansionRepeatedFailure) {
		t.Fatalf("repeated_failure not detected: %v", triggers)
	}

	// metric_regression：最新采集出现负 delta。
	metricSnap := Snapshot{Plan: emptyPlan(), ChannelMetrics: []ChannelMetric{{
		ID: "m1", ChannelID: "c1", ActionID: "a1", WindowKey: "w1",
		ViewsDelta: -5, CollectedAt: now.Add(-time.Hour),
	}}}
	triggers = EvaluateExpansion(metricSnap, now)
	if !containsTrigger(triggers, ExpansionMetricRegression) {
		t.Fatalf("metric_regression not detected: %v", triggers)
	}

	// new_evidence：observedAt 之后出现新 Research。
	evidenceSnap := Snapshot{Plan: emptyPlan(), Research: []Research{{
		ID: "res-1", AssistantID: "helper-a", Kind: ResearchWeb,
		SourceURL: "https://example.com", Evidence: "observed",
		CreatedAt: now.Add(-time.Minute), Revision: 1,
	}}}
	triggers = EvaluateExpansion(evidenceSnap, now, ExpansionLive{ObservedAt: now.Add(-time.Hour)})
	if !containsTrigger(triggers, ExpansionNewEvidence) {
		t.Fatalf("new_evidence not detected: %v", triggers)
	}

	// 空 live：new_evidence 也走 store 回退（observedAt 为零时任何证据都算）。
	triggers = EvaluateExpansion(evidenceSnap, now)
	if !containsTrigger(triggers, ExpansionNewEvidence) {
		t.Fatalf("new_evidence fallback not detected: %v", triggers)
	}
}

func containsTrigger(triggers []ExpansionTrigger, want ExpansionTrigger) bool {
	for _, tr := range triggers {
		if tr == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 机会去重：重复 objective 不会反复进入池子
// ---------------------------------------------------------------------------

func TestOpportunityStableDedup(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	prog := ProgressBlock{
		Responsibilities: []RespDecl{{Alias: "base", Objective: "base work"}},
		Opportunities: []OpportunityDecl{
			{Resp: "base", Objective: "expand to adjacent channel"},
			{Resp: "base", Objective: "expand to adjacent channel"},
			{Resp: "base", Objective: "expand to adjacent channel"},
		},
	}
	if err := store.RecordProgress(RecordProgressInput{RequestID: "prog-1", AssistantID: "helper-a", Progress: prog, Now: testEpoch}); err != nil {
		t.Fatal(err)
	}
	snap, _ := store.Get("helper-a")
	if len(snap.Opportunities) != 1 {
		t.Fatalf("opportunities = %d, want 1 (stable dedup)", len(snap.Opportunities))
	}
}

// ---------------------------------------------------------------------------
// Adopt：权限门、证据门、去重、容量、finite/continuous 分流
// ---------------------------------------------------------------------------

func opportunity(id, objective string) Opportunity {
	return Opportunity{ID: id, AssistantID: "helper-a", RespID: id, Objective: objective, CreatedAt: testEpoch, Revision: 1}
}

func TestEvaluateOpportunityAdoptionGates(t *testing.T) {
	continuous := Assistant{ID: "helper-a", Mode: ModeContinuous, Lifecycle: LifecycleActive, Policy: DefaultPolicy()}
	finite := continuous
	finite.Mode = ModeFinite

	// finite 分流：永不自动采纳。
	out, reason := EvaluateOpportunityAdoption(finite, Snapshot{Plan: emptyPlan()}, opportunity("o1", "x"), 0)
	if out != AdoptBlockedPolicy {
		t.Fatalf("finite outcome = %q (%s), want blocked_by_policy", out, reason)
	}

	// 无证据：不采纳模型猜测。
	out, _ = EvaluateOpportunityAdoption(continuous, Snapshot{Plan: emptyPlan()}, opportunity("o2", "x"), 0)
	if out != AdoptNoEvidence {
		t.Fatalf("no-evidence outcome = %q, want no_evidence", out)
	}

	// 有证据（verified research 绑定同一 RespID）→ proceed。
	snap := Snapshot{Plan: emptyPlan(), Research: []Research{{
		ID: "res-1", AssistantID: "helper-a", Kind: ResearchGitHub, SourceRepo: "org/repo",
		Evidence: "observed", Verification: ResearchVerified, RespID: "o3", Revision: 1,
	}}}
	out, _ = EvaluateOpportunityAdoption(continuous, snap, opportunity("o3", "x"), 0)
	if out != AdoptProceed {
		t.Fatalf("evidence outcome = %q, want proceed", out)
	}

	// 权限拒绝：network/publish/local_write 全 deny 时即使有证据也 blocked。
	deny := continuous
	deny.Policy = DefaultPolicy()
	deny.Policy.Network = AccessDeny
	deny.Policy.Publish = AccessDeny
	deny.Policy.LocalWrite = AccessDeny
	out, _ = EvaluateOpportunityAdoption(deny, snap, opportunity("o3", "x"), 0)
	if out != AdoptBlockedPolicy {
		t.Fatalf("deny-policy outcome = %q, want blocked_by_policy", out)
	}

	// 并发容量：运行数达到上限 → at_capacity。
	capPolicy := continuous
	capPolicy.Policy.MaxConcurrentSessions = 2
	out, _ = EvaluateOpportunityAdoption(capPolicy, snap, opportunity("o3", "x"), 2)
	if out != AdoptAtCapacity {
		t.Fatalf("capacity outcome = %q, want at_capacity", out)
	}

	// 去重：目标已在计划中。
	dupSnap := Snapshot{Plan: Plan{Revision: 1, Responsibilities: []Responsibility{{
		ID: "r1", Alias: "x", Objective: "x", Disposition: DispositionPlanned, Revision: 1,
	}}}, Research: snap.Research}
	out, _ = EvaluateOpportunityAdoption(continuous, dupSnap, opportunity("o3", "x"), 0)
	if out != AdoptDuplicate {
		t.Fatalf("duplicate outcome = %q, want duplicate", out)
	}
}

func TestAdoptOpportunityStoreOp(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	// 记录一条已验证研究并创建一个机会。
	res, err := store.RecordResearch(RecordResearchInput{
		RequestID: "research-1", AssistantID: "helper-a", Now: testEpoch,
		Research: Research{Kind: ResearchWeb, SourceURL: "https://example.com/docs",
			Question: "how to scale", Evidence: "documented at /scaling", Verification: ResearchVerified},
	})
	if err != nil {
		t.Fatalf("RecordResearch: %v", err)
	}
	_ = res
	store.RecordProgress(RecordProgressInput{
		RequestID: "prog-1", AssistantID: "helper-a", Now: testEpoch,
		Progress: ProgressBlock{
			Opportunities:    []OpportunityDecl{{Resp: "base", Objective: "scale the pipeline"}},
			Responsibilities: []RespDecl{{Alias: "base", Objective: "base work"}},
		},
	})
	snap, _ := store.Get("helper-a")
	opp := snap.Opportunities[0]
	opp.RespID = snap.Plan.Responsibilities[0].ID
	// 绑定研究到同一责任（证据链）：更新到 verified。
	if _, err := store.RecordResearch(RecordResearchInput{
		RequestID: "research-2", AssistantID: "helper-a", Now: testEpoch, ExpectedRevision: res.Revision,
		Research: Research{ID: res.ID, Kind: ResearchWeb, SourceURL: "https://example.com/docs",
			Question: "how to scale", Evidence: "documented at /scaling", Verification: ResearchVerified,
			RespID: opp.RespID},
	}); err != nil {
		t.Fatalf("RecordResearch update: %v", err)
	}

	// 无证据机会拒绝采纳：新的机会绑定到没有研究证据的另一个责任。
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "prog-2", AssistantID: "helper-a", Now: testEpoch,
		Progress: ProgressBlock{
			Responsibilities: []RespDecl{{Alias: "unresearched", Objective: "unresearched work"}},
			Opportunities:    []OpportunityDecl{{Resp: "unresearched", Objective: "guesswork idea"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ = store.Get("helper-a")
	guess := snap.Opportunities[1]
	if _, err := store.AdoptOpportunity(AdoptOpportunityInput{
		RequestID: "adopt-guess", AssistantID: "helper-a", OpportunityID: guess.ID, Now: testEpoch,
	}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("adopt guesswork error = %v, want ErrBlocked", err)
	}

	// 有证据机会采纳成功：生成 planned 责任，机会标记 adopted。
	adopted, err := store.AdoptOpportunity(AdoptOpportunityInput{
		RequestID: "adopt-1", AssistantID: "helper-a", OpportunityID: opp.ID, Now: testEpoch,
	})
	if err != nil {
		t.Fatalf("AdoptOpportunity: %v", err)
	}
	if adopted.Disposition != DispositionPlanned || adopted.Objective != "scale the pipeline" {
		t.Fatalf("adopted responsibility = %+v", adopted)
	}
	snap, _ = store.Get("helper-a")
	for _, o := range snap.Opportunities {
		if o.ID == opp.ID && o.AdoptedAt.IsZero() {
			t.Fatalf("opportunity %s not marked adopted", o.ID)
		}
	}

	// 重放：同一 request 幂等返回同一责任。
	again, err := store.AdoptOpportunity(AdoptOpportunityInput{
		RequestID: "adopt-1", AssistantID: "helper-a", OpportunityID: opp.ID, Now: testEpoch.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("AdoptOpportunity replay: %v", err)
	}
	if again.ID != adopted.ID {
		t.Fatalf("replay adopted different responsibility: %s != %s", again.ID, adopted.ID)
	}
}

// ---------------------------------------------------------------------------
// Research：幂等、去重、证据必填
// ---------------------------------------------------------------------------

func TestRecordResearchIdempotentAndDedup(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	in := RecordResearchInput{
		RequestID: "res-1", AssistantID: "helper-a", Now: testEpoch,
		Research: Research{Kind: ResearchGitHub, SourceRepo: "org/repo",
			Question: "does feature X work", Evidence: "issue #42 confirms behavior", Verification: ResearchUnverified},
	}
	first, err := store.RecordResearch(in)
	if err != nil {
		t.Fatalf("RecordResearch: %v", err)
	}
	// 相同 request 重放 → 同一记录。
	replay, err := store.RecordResearch(in)
	if err != nil {
		t.Fatalf("RecordResearch replay: %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay produced different research: %s != %s", replay.ID, first.ID)
	}
	// 相同来源+问题再次记录 → 冲突（去重）。
	dup := in
	dup.RequestID = "res-2"
	if _, err := store.RecordResearch(dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate source error = %v, want ErrConflict", err)
	}
	// 无来源 → 拒绝。
	noSrc := in
	noSrc.RequestID = "res-3"
	noSrc.Research.SourceURL, noSrc.Research.SourceRepo = "", ""
	if _, err := store.RecordResearch(noSrc); err == nil {
		t.Fatal("research without source accepted")
	}
	// 无证据（模型猜测）→ 拒绝。
	noEv := in
	noEv.RequestID = "res-4"
	noEv.Research.SourceURL, noEv.Research.SourceRepo = "https://example.com", ""
	noEv.Research.Evidence = ""
	if _, err := store.RecordResearch(noEv); err == nil {
		t.Fatal("research without evidence accepted")
	}
}

// ---------------------------------------------------------------------------
// 扩展退避：有界指数退避，成功清除
// ---------------------------------------------------------------------------

func TestRecordExpansionBackoff(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	due, st, err := store.ExpansionDue("helper-a", testEpoch)
	if err != nil || !due {
		t.Fatalf("fresh expansion due = %v err=%v, want due", due, err)
	}

	// 失败：attempt=1，BackoffUntil = now + base（6h）。
	failed, err := store.RecordExpansion(RecordExpansionInput{
		RequestID: "exp-fail-1", AssistantID: "helper-a", Trigger: ExpansionPlanEmpty,
		Err: "no adoptable candidate", Now: testEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Attempt != 1 || failed.BackoffUntil.IsZero() {
		t.Fatalf("failed expansion state = %+v", failed)
	}
	want := testEpoch.Add(expansionBackoff(1))
	if !failed.BackoffUntil.Equal(want) {
		t.Fatalf("backoff = %v, want %v", failed.BackoffUntil, want)
	}
	// 退避期内不 due。
	due, _, _ = store.ExpansionDue("helper-a", testEpoch.Add(time.Hour))
	if due {
		t.Fatal("expansion due during backoff")
	}
	// 再次失败：attempt=2，退避翻倍且有上限。
	failed2, _ := store.RecordExpansion(RecordExpansionInput{
		RequestID: "exp-fail-2", AssistantID: "helper-a", Trigger: ExpansionPlanEmpty,
		Err: "still nothing", Now: testEpoch.Add(time.Hour),
	})
	if failed2.Attempt != 2 || !failed2.BackoffUntil.Equal(testEpoch.Add(time.Hour).Add(expansionBackoff(2))) {
		t.Fatalf("second failure state = %+v", failed2)
	}
	// 上限：attempt 很大时不超过 max。
	big, _ := store.RecordExpansion(RecordExpansionInput{
		RequestID: "exp-fail-3", AssistantID: "helper-a", Trigger: ExpansionPlanEmpty,
		Err: "nope", Now: testEpoch.Add(2 * time.Hour),
	})
	if big.Attempt != 3 || expansionBackoff(big.Attempt) > expansionBackoffMax {
		t.Fatalf("bounded backoff violated: %+v", big)
	}
	// 成功：清除退避，立即 due。
	if _, err := store.RecordExpansion(RecordExpansionInput{
		RequestID: "exp-ok", AssistantID: "helper-a", Trigger: ExpansionPlanEmpty, Now: testEpoch.Add(3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	due, st, _ = store.ExpansionDue("helper-a", testEpoch.Add(3*time.Hour))
	if !due || st.Attempt != 0 || !st.BackoffUntil.IsZero() {
		t.Fatalf("successful expansion state = %+v due=%v", st, due)
	}
}

// ---------------------------------------------------------------------------
// 新生产路径不触碰 Runs/Jobs：无 RunnerJob 领取/更新、无 Run/Job↔Session 双写
// ---------------------------------------------------------------------------

func TestProductionPathDoesNotTouchRunsOrJobs(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	// 全部新路径写操作：进度回写、研究记录、机会采纳、决策态迁移。
	_ = store.RecordProgress(RecordProgressInput{
		RequestID: "prog-1", AssistantID: "helper-a", Now: testEpoch,
		Progress: ProgressBlock{Responsibilities: []RespDecl{{Alias: "job", Objective: "do it"}}},
	})
	_, _ = store.RecordResearch(RecordResearchInput{
		RequestID: "res-1", AssistantID: "helper-a", Now: testEpoch,
		Research: Research{Kind: ResearchWeb, SourceURL: "https://example.com", Evidence: "seen", Verification: ResearchUnverified},
	})
	snap, _ := store.Get("helper-a")
	resp := snap.Plan.Responsibilities[0]
	_, _ = store.SetResponsibilityDisposition(SetResponsibilityDispositionInput{
		RequestID: "set-1", AssistantID: "helper-a", ResponsibilityID: resp.ID,
		Disposition: DispositionReview, ExpectedPlanRev: snap.Plan.Revision, Now: testEpoch,
	})

	snap, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Runs) != 0 {
		t.Fatalf("new production path created Runs: %d", len(snap.Runs))
	}
	if len(snap.Jobs) != 0 {
		t.Fatalf("new production path created RunnerJobs: %d", len(snap.Jobs))
	}
	// 无 Job 可领取：ClaimJob 返回无。
	if _, ok, err := store.ClaimJob("worker", testEpoch, time.Minute); err != nil || ok {
		t.Fatalf("ClaimJob after new-path writes: ok=%v err=%v, want no claim", ok, err)
	}
}

// ---------------------------------------------------------------------------
// 推广/求职场景：当前批次完成后自动研究 -> 筛选 -> 采纳 -> 启动 Session
// ---------------------------------------------------------------------------

func TestExpansionNextBatchResearchRankAdoptExecute(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "prog-1", AssistantID: "helper-a", Now: testEpoch,
		Progress: ProgressBlock{
			Responsibilities: []RespDecl{{Alias: "batch-1", Objective: "推广渠道 A 完成"}},
			Complete:         []string{"batch-1"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ := store.Get("helper-a")
	base := snap.Plan.Responsibilities[0]

	// Evaluate：计划为空（无 executable）→ plan_empty 触发。
	if triggers := EvaluateExpansion(snap, testEpoch.Add(time.Hour), ExpansionLive{}); !containsTrigger(triggers, ExpansionPlanEmpty) {
		t.Fatalf("plan_empty not triggered after batch completion: %v", triggers)
	}

	// Research：研究下一批渠道（绑定到 base 责任），并验证。
	res, err := store.RecordResearch(RecordResearchInput{
		RequestID: "res-1", AssistantID: "helper-a", Now: testEpoch.Add(time.Hour),
		Research: Research{Kind: ResearchWeb, SessionID: "research-sess-1", RespID: base.ID,
			SourceURL: "https://jobs.example.com/listings", Question: "next job batch sources",
			Evidence: "5 new channels with open listings", Verification: ResearchUnverified},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.RecordResearch(RecordResearchInput{
		RequestID: "res-2", AssistantID: "helper-a", Now: testEpoch.Add(2 * time.Hour), ExpectedRevision: res.Revision,
		Research: Research{ID: res.ID, Kind: ResearchWeb, SessionID: "research-sess-1", RespID: base.ID,
			SourceURL: "https://jobs.example.com/listings", Question: "next job batch sources",
			Evidence: "5 new channels with open listings", Verification: ResearchVerified},
	})

	// Discover：模型发现机会（机会池）。
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "prog-2", AssistantID: "helper-a", Now: testEpoch.Add(3 * time.Hour),
		Progress: ProgressBlock{
			Opportunities: []OpportunityDecl{{Resp: "batch-1", Objective: "投递渠道 B 的下一批职位"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ = store.Get("helper-a")
	opp := snap.Opportunities[0]
	opp.RespID = base.ID
	// 用 RecordResearch 更新把研究绑定到机会（同 RespID）→ 证据链完整。

	// Rank：候选可采纳。
	ranked := RankOpportunities(snap, snap.Assistant, 0)
	if len(ranked) != 1 {
		t.Fatalf("ranked candidates = %d, want 1", len(ranked))
	}

	// Adopt + Execute：监督循环通过 Adopt 决策自动采纳（生成 planned 责任）并
	// 启动带 ResponsibilityID 的 managed Session。
	host := &fakeSupervisorHost{}
	control := &fakeSessionControl{}
	ex := newTestExecutor(t, store, host, control)
	routeRes := ex.RouteDecision(snap.Assistant, SupervisorDecision{Action: ActionAdopt, Target: opp.ID}, "batch-1", nil, testEpoch.Add(5*time.Hour))
	if routeRes.Outcome != RouteApplied {
		t.Fatalf("adopt route outcome = %s err=%v", routeRes.Outcome, routeRes.Err)
	}
	if control.createdCount() == 0 {
		t.Fatal("no managed Session created for adopted responsibility")
	}
	creates := control.creates
	last := creates[len(creates)-1]
	if last.OwnerID != "helper-a" || last.Purpose != "managed" {
		t.Fatalf("managed session meta intent = %+v", last)
	}

	// 采纳的责任在计划中（planned 决策态），且 Session 绑定其 ResponsibilityID。
	snap, _ = store.Get("helper-a")
	var adopted *Responsibility
	for i := range snap.Plan.Responsibilities {
		if snap.Plan.Responsibilities[i].Objective == "投递渠道 B 的下一批职位" {
			adopted = &snap.Plan.Responsibilities[i]
		}
	}
	if adopted == nil {
		t.Fatal("adopted responsibility missing from plan")
	}
	if adopted.Disposition != DispositionPlanned {
		t.Fatalf("adopted next batch disposition = %q, want planned", adopted.Disposition)
	}
	if last.ResponsibilityID != adopted.ID {
		t.Fatalf("managed session responsibility_id = %q, want %q", last.ResponsibilityID, adopted.ID)
	}

	// 下一批也去重：已采纳的机会重放是幂等 no-op（返回同一责任，不重复创建）。
	replayed, err := store.AdoptOpportunity(AdoptOpportunityInput{
		RequestID: "adopt-2", AssistantID: "helper-a", OpportunityID: opp.ID, Now: testEpoch.Add(6 * time.Hour),
	})
	if err != nil {
		t.Fatalf("re-adopt already-adopted opportunity: %v", err)
	}
	if replayed.ID != adopted.ID {
		t.Fatalf("re-adopt produced a different responsibility: %s != %s", replayed.ID, adopted.ID)
	}
	snap, _ = store.Get("helper-a")
	respCount := 0
	for _, r := range snap.Plan.Responsibilities {
		if r.Objective == "投递渠道 B 的下一批职位" {
			respCount++
		}
	}
	if respCount != 1 {
		t.Fatalf("duplicate objective responsibilities = %d, want 1", respCount)
	}
}
