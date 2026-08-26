package assistant

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProgressProtocolParsesAndMergesProposals(t *testing.T) {
	text := `<assistant-progress>{"proposals":[{"target_kind":"routine","target_id":"routine-a","routine":{"prompt":"Review release readiness"},"summary":"Tighten the review","reason":"Recent runs missed release checks","evidence":["run summary: release condition was not evaluated"]}]}</assistant-progress>`
	blocks, errs := ParseProgressBlocks(text)
	if len(errs) != 0 || len(blocks) != 1 || len(blocks[0].Proposals) != 1 {
		t.Fatalf("blocks=%+v errors=%v", blocks, errs)
	}
	merged := MergeProgressBlocks([]ProgressBlock{blocks[0], {Proposals: []ProposalDecl{{
		TargetKind: ProposalTargetChannel, TargetID: "channel-forum",
		Channel: &ChannelProposalPatch{Enabled: proposalPtr(false)},
		Summary: "Pause collection", Reason: "The channel is unavailable", Evidence: []string{"three collection failures"},
	}}}})
	if len(merged.Proposals) != 2 || merged.Proposals[0].Routine == nil || merged.Proposals[1].Channel == nil {
		t.Fatalf("merged proposals=%+v", merged.Proposals)
	}
}

func TestCompleteRunCreatesDurableRoutineProposalAtomically(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "proposal-run")
	run := mustClaimRun(t, store)

	done, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "complete-proposal", RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence,
		Summary: "reviewed cadence", Progress: ProgressBlock{PlanRevision: 1, Proposals: []ProposalDecl{routinePromptProposal("Review releases and test failures")}},
		Now: testEpoch.Add(time.Second),
	})
	if err != nil || done.State != RunSucceeded {
		t.Fatalf("complete=%+v err=%v", done, err)
	}
	snapshot, err := testStore(t, root).Get("helper-a")
	if err != nil || len(snapshot.Proposals) != 1 {
		t.Fatalf("snapshot proposals=%+v err=%v", snapshot.Proposals, err)
	}
	proposal := snapshot.Proposals[0]
	if proposal.State != ProposalPending || proposal.RunID != run.ID || proposal.BaseRevision != snapshot.Routines[0].Revision {
		t.Fatalf("proposal=%+v routine=%+v", proposal, snapshot.Routines[0])
	}
	if proposal.Routine == nil || *proposal.Routine.Before.Prompt != "Inspect recent changes" || *proposal.Routine.After.Prompt != "Review releases and test failures" {
		t.Fatalf("routine change=%+v", proposal.Routine)
	}
	if snapshot.Routines[0].Prompt != "Inspect recent changes" {
		t.Fatalf("proposal changed target before approval: %+v", snapshot.Routines[0])
	}
}

func TestResolveProposalAppliesAtomicallyAndReplays(t *testing.T) {
	store, proposal := storeWithRoutineProposal(t, "Review releases and test failures")
	in := ResolveProposalInput{
		RequestID: "accept-proposal", AssistantID: "helper-a", ProposalID: proposal.ID,
		ExpectedRevision: proposal.Revision, Decision: ProposalAccept, Resolution: "approved after review", Now: testEpoch.Add(2 * time.Second),
	}
	resolved, err := store.ResolveProposal(in)
	if err != nil || resolved.State != ProposalApplied || resolved.Revision != 2 {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	replay, err := store.ResolveProposal(in)
	if err != nil || replay.Revision != resolved.Revision || !replay.UpdatedAt.Equal(resolved.UpdatedAt) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	snapshot, err := store.Get("helper-a")
	if err != nil || snapshot.Routines[0].Prompt != "Review releases and test failures" || snapshot.Routines[0].Revision != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.Proposals[0].State != ProposalApplied || snapshot.Proposals[0].Resolution != "approved after review" {
		t.Fatalf("persisted proposal=%+v", snapshot.Proposals[0])
	}
}

func TestResolveProposalRejectsWithoutChangingTarget(t *testing.T) {
	store, proposal := storeWithRoutineProposal(t, "Review releases and test failures")
	resolved, err := store.ResolveProposal(ResolveProposalInput{
		RequestID: "reject-proposal", AssistantID: "helper-a", ProposalID: proposal.ID,
		ExpectedRevision: proposal.Revision, Decision: ProposalReject, Resolution: "keep the current routine", Now: testEpoch.Add(2 * time.Second),
	})
	if err != nil || resolved.State != ProposalRejected {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	snapshot, _ := store.Get("helper-a")
	if snapshot.Routines[0].Prompt != "Inspect recent changes" || snapshot.Routines[0].Revision != 1 {
		t.Fatalf("rejection changed target: %+v", snapshot.Routines[0])
	}
}

func TestResolveProposalSupersedesConflictingUserEdit(t *testing.T) {
	store, proposal := storeWithRoutineProposal(t, "Review releases and test failures")
	snapshot, _ := store.Get("helper-a")
	edited := snapshot.Routines[0]
	edited.Prompt = "User-owned new prompt"
	if _, err := store.PutRoutine(RoutineInput{RequestID: "user-edit", Routine: edited, ExpectedRevision: edited.Revision, Now: testEpoch.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveProposal(ResolveProposalInput{
		RequestID: "accept-stale", AssistantID: "helper-a", ProposalID: proposal.ID,
		ExpectedRevision: proposal.Revision, Decision: ProposalAccept, Resolution: "user clicked accept", Now: testEpoch.Add(3 * time.Second),
	})
	if err != nil || resolved.State != ProposalSuperseded || !strings.Contains(resolved.Resolution, "changed after") {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	after, _ := store.Get("helper-a")
	if after.Routines[0].Prompt != "User-owned new prompt" || after.Routines[0].Revision != 2 {
		t.Fatalf("stale proposal overwrote user edit: %+v", after.Routines[0])
	}
}

func TestResolveProposalMergesAcrossUnrelatedTargetRevision(t *testing.T) {
	store, proposal := storeWithRoutineProposal(t, "Review releases and test failures")
	snapshot, _ := store.Get("helper-a")
	edited := snapshot.Routines[0]
	edited.Title = "User-renamed routine"
	if _, err := store.PutRoutine(RoutineInput{RequestID: "rename-routine", Routine: edited, ExpectedRevision: edited.Revision, Now: testEpoch.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveProposal(ResolveProposalInput{
		RequestID: "accept-after-rename", AssistantID: "helper-a", ProposalID: proposal.ID,
		ExpectedRevision: proposal.Revision, Decision: ProposalAccept, Now: testEpoch.Add(3 * time.Second),
	})
	if err != nil || resolved.State != ProposalApplied {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	after, _ := store.Get("helper-a")
	if after.Routines[0].Title != "User-renamed routine" || after.Routines[0].Prompt != "Review releases and test failures" || after.Routines[0].Revision != 3 {
		t.Fatalf("field-level merge failed: %+v", after.Routines[0])
	}
}

func TestResolveProposalTreatsAlreadyDesiredTargetAsApplied(t *testing.T) {
	store, proposal := storeWithRoutineProposal(t, "Review releases and test failures")
	snapshot, _ := store.Get("helper-a")
	edited := snapshot.Routines[0]
	edited.Prompt = "Review releases and test failures"
	updated, err := store.PutRoutine(RoutineInput{RequestID: "manual-same-change", Routine: edited, ExpectedRevision: edited.Revision, Now: testEpoch.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveProposal(ResolveProposalInput{
		RequestID: "accept-already-done", AssistantID: "helper-a", ProposalID: proposal.ID,
		ExpectedRevision: proposal.Revision, Decision: ProposalAccept, Now: testEpoch.Add(3 * time.Second),
	})
	if err != nil || resolved.State != ProposalApplied {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	after, _ := store.Get("helper-a")
	if after.Routines[0].Revision != updated.Revision {
		t.Fatalf("already-desired target was written twice: before=%d after=%d", updated.Revision, after.Routines[0].Revision)
	}
}

func TestProgressDeduplicatesSamePendingProposalAcrossRuns(t *testing.T) {
	store, _ := storeWithRoutineProposal(t, "Review releases and test failures")
	if _, err := store.Trigger(TriggerInput{AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "second-run", Trigger: TriggerManual, Now: testEpoch.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := store.Claim("worker-a", testEpoch.Add(2*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", run, ok, err)
	}
	snapshot, _ := store.Get("helper-a")
	_, err = store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "complete-second-proposal", RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence,
		Summary: "same recommendation", Progress: ProgressBlock{PlanRevision: snapshot.Plan.Revision, Proposals: []ProposalDecl{routinePromptProposal("Review releases and test failures")}},
		Now: testEpoch.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get("helper-a")
	if len(after.Proposals) != 1 {
		t.Fatalf("duplicate proposal count=%d proposals=%+v", len(after.Proposals), after.Proposals)
	}
}

func TestProgressRejectsNoopAndOutOfRangeProposalWithoutCompletingRun(t *testing.T) {
	for name, proposal := range map[string]ProposalDecl{
		"noop": routinePromptProposal("Inspect recent changes"),
		"out-of-range": {
			TargetKind: ProposalTargetChannel, TargetID: "channel-forum",
			Channel: &ChannelProposalPatch{CollectIntervalSeconds: proposalPtr(int64(60))},
			Summary: "Collect faster", Reason: "Need quick feedback", Evidence: []string{"one slow sample"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
			created := mustCreate(t, store, "helper-a")
			if name == "out-of-range" {
				putTestChannel(t, store, created.Assistant.ID)
			}
			mustTrigger(t, store, "invalid-proposal-run")
			run := mustClaimRun(t, store)
			_, err := store.CompleteRunWithProgress(CompleteRunInput{
				RequestID: "complete-invalid", RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence,
				Progress: ProgressBlock{PlanRevision: 1, Proposals: []ProposalDecl{proposal}}, Now: testEpoch.Add(time.Second),
			})
			if err == nil {
				t.Fatal("expected invalid proposal error")
			}
			snapshot, _ := store.Get("helper-a")
			if len(snapshot.Proposals) != 0 || snapshot.Runs[0].State != RunRunning {
				t.Fatalf("invalid patch partially committed: proposals=%+v run=%+v", snapshot.Proposals, snapshot.Runs[0])
			}
		})
	}
}

func TestChannelProposalAppliesTypedIntervalAndEnabledPatch(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	created := mustCreate(t, store, "helper-a")
	channel := putTestChannel(t, store, created.Assistant.ID)
	mustTrigger(t, store, "channel-proposal-run")
	run := mustClaimRun(t, store)
	interval := int64(7200)
	enabled := false
	_, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "complete-channel-proposal", RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Proposals: []ProposalDecl{{
			TargetKind: ProposalTargetChannel, TargetID: channel.ID,
			Channel: &ChannelProposalPatch{CollectIntervalSeconds: &interval, Enabled: &enabled},
			Summary: "Slow collection", Reason: "The channel changes slowly", Evidence: []string{"three windows had no delta"},
		}}}, Now: testEpoch.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get("helper-a")
	proposal := snapshot.Proposals[0]
	if _, err := store.ResolveProposal(ResolveProposalInput{
		RequestID: "accept-channel", AssistantID: "helper-a", ProposalID: proposal.ID,
		ExpectedRevision: proposal.Revision, Decision: ProposalAccept, Now: testEpoch.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get("helper-a")
	if after.Channels[0].CollectIntervalSeconds != interval || after.Channels[0].Enabled {
		t.Fatalf("channel patch=%+v", after.Channels[0])
	}
}

func TestResolveProposalUsesRevisionAndIdempotencyGuards(t *testing.T) {
	store, proposal := storeWithRoutineProposal(t, "Review releases and test failures")
	if _, err := store.ResolveProposal(ResolveProposalInput{
		RequestID: "stale-proposal", AssistantID: "helper-a", ProposalID: proposal.ID,
		ExpectedRevision: proposal.Revision + 1, Decision: ProposalAccept,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale error=%v, want ErrConflict", err)
	}
	in := ResolveProposalInput{RequestID: "decision-replay", AssistantID: "helper-a", ProposalID: proposal.ID, ExpectedRevision: proposal.Revision, Decision: ProposalReject}
	if _, err := store.ResolveProposal(in); err != nil {
		t.Fatal(err)
	}
	in.Decision = ProposalAccept
	if _, err := store.ResolveProposal(in); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("changed replay error=%v, want ErrIdempotency", err)
	}
}

func routinePromptProposal(prompt string) ProposalDecl {
	return ProposalDecl{
		TargetKind: ProposalTargetRoutine, TargetID: "routine-a",
		Routine: &RoutineProposalPatch{Prompt: &prompt},
		Summary: "Improve the routine prompt", Reason: "The latest run found a repeatable gap", Evidence: []string{"latest run summary"},
	}
}

func storeWithRoutineProposal(t *testing.T, prompt string) (*Store, ChangeProposal) {
	t.Helper()
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "proposal-run")
	run := mustClaimRun(t, store)
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "complete-proposal", RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Proposals: []ProposalDecl{routinePromptProposal(prompt)}}, Now: testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Get("helper-a")
	if err != nil || len(snapshot.Proposals) != 1 {
		t.Fatalf("proposal snapshot=%+v err=%v", snapshot.Proposals, err)
	}
	return store, snapshot.Proposals[0]
}

func putTestChannel(t *testing.T, store *Store, assistantID string) ChannelBinding {
	t.Helper()
	channel, err := store.PutChannel(PutChannelInput{
		RequestID: "put-channel", Channel: ChannelBinding{
			ID: "channel-forum", AssistantID: assistantID, Name: "Forum", Kind: ChannelDiscourse,
			BaseURL: "https://community.example.com", Username: "bot", CredentialKey: "ASSISTANT_CHANNEL_TEST_KEY",
			CollectIntervalSeconds: 3600, Enabled: true,
		}, Now: testEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func proposalPtr[T any](value T) *T { return &value }
