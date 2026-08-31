package assistant

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// ExpansionTrigger is why the supervisor should enter the expansion loop
// (Evaluate -> Discover -> Research -> Rank -> Adopt -> Execute -> Learn). It is
// a closed enum so the loop and diagnostics never parse prose.
type ExpansionTrigger string

const (
	// ExpansionPlanEmpty fires when no responsibility is executable and no
	// session is running — the current plan is exhausted.
	ExpansionPlanEmpty ExpansionTrigger = "plan_empty"
	// ExpansionStalled fires when executable work exists but nothing is running
	// and the newest executable responsibility has not progressed recently.
	ExpansionStalled ExpansionTrigger = "stalled"
	// ExpansionRepeatedFailure fires when the same responsibility failed twice
	// or more — the current approach is not working and should change.
	ExpansionRepeatedFailure ExpansionTrigger = "repeated_failure"
	// ExpansionMetricRegression fires when a key engagement metric declined
	// (negative views/likes/reply delta on a collected channel metric).
	ExpansionMetricRegression ExpansionTrigger = "metric_regression"
	// ExpansionNewEvidence fires when a session, the user, or external research
	// produced new evidence (opportunity/artifact/experiment/decision/research)
	// since the last cycle observation.
	ExpansionNewEvidence ExpansionTrigger = "new_evidence"
)

// expansionStalledThreshold is how long executable work may sit untouched before
// the plan is considered stalled.
const expansionStalledThreshold = 6 * time.Hour

// metricRegressionWindow is how recent a collected channel metric must be to
// count as a current regression signal (older dips are history, not triggers).
const metricRegressionWindow = 7 * 24 * time.Hour

// expansionBackoffBase and expansionBackoffMax bound the retry delay after an
// expansion pass that produced nothing adoptable, so the loop cannot spin.
const (
	expansionBackoffBase = 6 * time.Hour
	expansionBackoffMax  = 72 * time.Hour
)

// ExpansionLive is the execution state derived from managed Sessions rather
// than from Runs/Jobs, which the converged control plane no longer creates.
type ExpansionLive struct {
	// Running is the number of managed sessions currently running or waiting.
	Running int
	// Failed is the number of managed sessions that failed.
	Failed int
	// MetricRegression is the host-observed key-metric decline signal; when
	// false the store-level DetectMetricRegression fallback is used.
	MetricRegression bool
	// NewEvidence is the host-observed new-evidence signal; when false the
	// store-level DetectNewEvidence fallback (observedAt) is used.
	NewEvidence bool
	// ObservedAt is the last cycle observation time used by the new_evidence
	// fallback; zero means "any existing evidence counts".
	ObservedAt time.Time
}

// EvaluateExpansion reports which expansion triggers are currently active. The
// optional live argument carries Session-derived running/failed counts and
// richer signals; when omitted it falls back to the historical Run/Job columns
// and the store-level evidence detectors so tests and legacy snapshots remain
// readable. All five design triggers are covered: plan_empty, stalled,
// repeated_failure, metric_regression and new_evidence.
func EvaluateExpansion(snapshot Snapshot, now time.Time, live ...ExpansionLive) []ExpansionTrigger {
	now = utcNow(now)
	var out []ExpansionTrigger

	executable := 0
	var newestExecutable time.Time
	for _, r := range snapshot.Plan.Responsibilities {
		if r.Status == RespReady || r.Status == RespActive {
			executable++
			if r.UpdatedAt.After(newestExecutable) {
				newestExecutable = r.UpdatedAt
			}
		}
	}
	running, failed := 0, 0
	metricRegression, newEvidence := false, false
	observedAt := time.Time{}
	if len(live) > 0 {
		running, failed = live[0].Running, live[0].Failed
		metricRegression, newEvidence = live[0].MetricRegression, live[0].NewEvidence
		observedAt = live[0].ObservedAt
	} else {
		running = countInFlight(snapshot)
		failed = countFailed(snapshot)
	}

	if executable == 0 && running == 0 {
		out = append(out, ExpansionPlanEmpty)
	} else if executable > 0 && running == 0 && !newestExecutable.IsZero() && now.Sub(newestExecutable) > expansionStalledThreshold {
		out = append(out, ExpansionStalled)
	}
	if failed >= 2 {
		out = append(out, ExpansionRepeatedFailure)
	}
	if metricRegression || DetectMetricRegression(snapshot, now) {
		out = append(out, ExpansionMetricRegression)
	}
	if newEvidence || DetectNewEvidence(snapshot, observedAt) {
		out = append(out, ExpansionNewEvidence)
	}
	return out
}

// DetectMetricRegression reports whether the latest collected channel metric for
// any channel/action shows a negative engagement delta within the regression
// window. It is the in-store metric_regression signal; hosts may also pass
// their own richer signal via ExpansionLive.
func DetectMetricRegression(snapshot Snapshot, now time.Time) bool {
	now = utcNow(now)
	// Only the most recent collection per (channel, action, window) counts, so
	// an old dip does not re-trigger forever.
	latest := make(map[string]ChannelMetric)
	for _, m := range snapshot.ChannelMetrics {
		key := m.ChannelID + "/" + m.ActionID + "/" + m.WindowKey
		if prev, ok := latest[key]; !ok || m.CollectedAt.After(prev.CollectedAt) {
			latest[key] = m
		}
	}
	for _, m := range latest {
		if now.Sub(m.CollectedAt) > metricRegressionWindow {
			continue
		}
		if m.ViewsDelta < 0 || m.LikesDelta < 0 || m.ReplyDelta < 0 {
			return true
		}
	}
	return false
}

// DetectNewEvidence reports whether any opportunity, artifact, experiment,
// interaction decision, or research record was created after observedAt — the
// new_evidence expansion trigger. observedAt is typically the last cycle
// observation; zero means any existing evidence counts.
func DetectNewEvidence(snapshot Snapshot, observedAt time.Time) bool {
	if observedAt.IsZero() {
		return len(snapshot.Opportunities)+len(snapshot.Artifacts)+len(snapshot.Experiments)+len(snapshot.Decisions)+len(snapshot.Research) > 0
	}
	after := func(t time.Time) bool { return t.After(observedAt) }
	for _, o := range snapshot.Opportunities {
		if after(o.CreatedAt) {
			return true
		}
	}
	for _, a := range snapshot.Artifacts {
		if after(a.CreatedAt) {
			return true
		}
	}
	for _, e := range snapshot.Experiments {
		if after(e.CreatedAt) {
			return true
		}
	}
	for _, d := range snapshot.Decisions {
		if after(d.CreatedAt) {
			return true
		}
	}
	for _, r := range snapshot.Research {
		if after(r.CreatedAt) {
			return true
		}
	}
	return false
}

// evidenceObservedThrough returns the newest CreatedAt across every evidence
// collection in the snapshot (opportunity/artifact/experiment/decision/research)
// — the exact boundary up to which this snapshot is observably complete. It is
// the value a turn's watermark may advance to: a turn must never advance past
// what it actually saw, or a concurrent/late record written after the snapshot
// but stamped before wall-clock now would be swallowed. Zero means the snapshot
// carried no evidence, so nothing is advanced.
func evidenceObservedThrough(snapshot Snapshot) time.Time {
	var through time.Time
	advance := func(t time.Time) {
		t = t.UTC()
		if t.After(through) {
			through = t
		}
	}
	for _, o := range snapshot.Opportunities {
		advance(o.CreatedAt)
	}
	for _, a := range snapshot.Artifacts {
		advance(a.CreatedAt)
	}
	for _, e := range snapshot.Experiments {
		advance(e.CreatedAt)
	}
	for _, d := range snapshot.Decisions {
		advance(d.CreatedAt)
	}
	for _, r := range snapshot.Research {
		advance(r.CreatedAt)
	}
	return through
}

// ExpansionState is the persisted, observable state of the expansion loop. A
// pass that found nothing adoptable sets BackoffUntil so the loop retries with
// bounded exponential backoff instead of spinning every tick.
type ExpansionState struct {
	LastTriggerAt time.Time        `json:"last_trigger_at,omitempty" ts_type:"string"`
	LastTrigger   ExpansionTrigger `json:"last_trigger,omitempty"`
	BackoffUntil  time.Time        `json:"backoff_until,omitempty" ts_type:"string"`
	Attempt       int              `json:"attempt"`
	Error         string           `json:"error,omitempty"`
	// EvidenceObservedAt is the durable evidence observation watermark: the
	// supervisor has already been woken for every opportunity/artifact/
	// experiment/decision/research created at or before this time, so those
	// records never re-trigger new_evidence. Zero means nothing observed yet —
	// any existing evidence still counts once (legacy aggregates migrate to the
	// same first-wake behavior).
	EvidenceObservedAt time.Time `json:"evidence_observed_at,omitempty" ts_type:"string"`
}

// expansionBackoff returns the bounded exponential backoff for a failed or
// fruitless expansion pass.
func expansionBackoff(attempt int) time.Duration {
	d := expansionBackoffBase
	for i := 0; i < attempt && d < expansionBackoffMax; i++ {
		d *= 2
	}
	if d > expansionBackoffMax {
		d = expansionBackoffMax
	}
	return d
}

// expansionDue reports whether the expansion loop may run now given the last
// attempt and its backoff.
func (e ExpansionState) expansionDue(now time.Time) bool {
	if e.BackoffUntil.IsZero() {
		return true
	}
	return now.After(e.BackoffUntil)
}

// OpportunityAdoptOutcome is the typed result of the Rank->Adopt gate.
type OpportunityAdoptOutcome string

const (
	// AdoptProceed means the opportunity may be auto-adopted and executed.
	AdoptProceed OpportunityAdoptOutcome = "proceed"
	// AdoptBlockedPolicy means policy refuses auto-adoption (network/approval
	// gates or lifecycle/mode). The opportunity stays for a user decision.
	AdoptBlockedPolicy OpportunityAdoptOutcome = "blocked_by_policy"
	// AdoptDuplicate means an identical responsibility/opportunity already
	// exists — the candidate is a stable duplicate and must not be re-added.
	AdoptDuplicate OpportunityAdoptOutcome = "duplicate"
	// AdoptNoEvidence means the opportunity has no verified research or artifact
	// evidence backing it, so it is not a "有证据高价值机会" yet.
	AdoptNoEvidence OpportunityAdoptOutcome = "no_evidence"
	// AdoptAtCapacity means the per-assistant managed-Session concurrency cap is
	// reached; execution must wait, not start another session.
	AdoptAtCapacity OpportunityAdoptOutcome = "at_capacity"
)

// EvaluateOpportunityAdoption applies the Rank->Adopt gate for one opportunity
// candidate: continuous mode + policy allows network research + verified
// evidence exists + no duplicate + concurrency headroom. finite mode never
// auto-adopts (the assistant stops or enters maintenance instead). The reason
// is human-readable so diagnostics and the UI can show why a candidate was not
// adopted.
func EvaluateOpportunityAdoption(a Assistant, snapshot Snapshot, opp Opportunity, runningSessions int) (OpportunityAdoptOutcome, string) {
	if a.Mode != ModeContinuous {
		return AdoptBlockedPolicy, "mode " + string(a.Mode) + " 不自动采纳：有限目标按完成策略停止或进入维护"
	}
	if a.Lifecycle != LifecycleActive {
		return AdoptBlockedPolicy, "assistant 生命周期不是 active"
	}
	// Research/execution needs network or at least a per-action approval path;
	// a fully deny policy cannot back a candidate with evidence.
	if a.Policy.Network == AccessDeny && a.Policy.Publish == AccessDeny && a.Policy.LocalWrite == AccessDeny {
		return AdoptBlockedPolicy, "policy 拒绝：网络/发布/本地写入均为 deny，无法执行候选"
	}
	if cap := a.Policy.MaxConcurrentSessions; cap > 0 && runningSessions >= cap {
		return AdoptAtCapacity, "并发上限 " + itoa(cap) + " 已达"
	}
	if duplicateOpportunity(snapshot, opp) {
		return AdoptDuplicate, "相同目标的机会/责任已存在（稳定去重）"
	}
	if !opportunityHasEvidence(snapshot, opp) {
		return AdoptNoEvidence, "候选缺少已验证证据（Research/Artifact），不采纳模型猜测"
	}
	return AdoptProceed, "continuous + policy 允许 + 有证据高价值机会"
}

// duplicateOpportunity reports whether the candidate duplicates an existing
// responsibility or opportunity by stable key (alias/objective or
// source-question). The key is the same one AdoptOpportunity uses, so a
// replayed or re-discovered candidate is a no-op, never a second plan item.
func duplicateOpportunity(snapshot Snapshot, opp Opportunity) bool {
	key := strings.TrimSpace(opp.Objective)
	if key == "" {
		key = strings.TrimSpace(opp.RespID)
	}
	if key == "" {
		return false
	}
	for _, r := range snapshot.Plan.Responsibilities {
		if strings.TrimSpace(r.Objective) == key || aliasOrID(r) == key {
			return true
		}
	}
	for _, o := range snapshot.Opportunities {
		if o.ID == opp.ID {
			continue
		}
		if strings.TrimSpace(o.Objective) == key {
			return true
		}
	}
	return false
}

// opportunityHasEvidence reports whether the candidate is backed by durable
// evidence: a verified research record for the same responsibility/source or an
// artifact attached to the same responsibility. Model guesses without a source
// are never evidence.
func opportunityHasEvidence(snapshot Snapshot, opp Opportunity) bool {
	for _, r := range snapshot.Research {
		if r.RespID != "" && r.RespID == opp.RespID && r.Verification == ResearchVerified {
			return true
		}
	}
	for _, a := range snapshot.Artifacts {
		if a.RespID != "" && a.RespID == opp.RespID {
			return true
		}
	}
	return false
}

// RankOpportunities returns adoptable candidates in deterministic priority
// order (evidence-backed first, then creation order) for the Rank stage.
func RankOpportunities(snapshot Snapshot, a Assistant, runningSessions int) []Opportunity {
	type ranked struct {
		opp   Opportunity
		out   OpportunityAdoptOutcome
		score int
	}
	var items []ranked
	for _, opp := range snapshot.Opportunities {
		out, _ := EvaluateOpportunityAdoption(a, snapshot, opp, runningSessions)
		score := 0
		if out == AdoptProceed {
			score = 1
			for _, r := range snapshot.Research {
				if r.RespID == opp.RespID && r.Verification == ResearchVerified {
					score += 2
				}
			}
		}
		items = append(items, ranked{opp: opp, out: out, score: score})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].opp.CreatedAt.Before(items[j].opp.CreatedAt)
	})
	out := make([]Opportunity, 0, len(items))
	for _, it := range items {
		if it.out == AdoptProceed {
			out = append(out, it.opp)
		}
	}
	return out
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// countInFlight counts legacy Runs/Jobs in flight — the fallback when the
// Session-derived live counts are not supplied. The converged control plane no
// longer creates new Runs/Jobs, so this only keeps old snapshots readable.
func countInFlight(snapshot Snapshot) int {
	n := 0
	for _, run := range snapshot.Runs {
		switch run.State {
		case RunQueued, RunRunning, RunRetryWait, RunWaitingApproval:
			n++
		}
	}
	for _, job := range snapshot.Jobs {
		switch job.State {
		case JobQueued, JobRunning, JobRetryWait:
			n++
		}
	}
	return n
}

func countFailed(snapshot Snapshot) int {
	n := 0
	for _, run := range snapshot.Runs {
		if run.State == RunFailed {
			n++
		}
	}
	return n
}
