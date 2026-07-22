package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorkViewAssessmentMissingBlobPersistsAcrossRestartAndSnapshot(t *testing.T) {
	f := newServiceFixture(t)
	created := mustServiceCreate(t, f.svc, "view-missing-blob")
	view := mustServiceView(t, f.svc, created.ID)
	result, err := f.svc.PinCornerstone(t.Context(), created.ID, PinCornerstoneInput{
		Type: CornerstoneInstruction, Title: "required snapshot",
		Content: strings.Repeat("reproducible input ", 1000), Ref: CornerstoneRef{Kind: "inline"},
		Mode: CornerstoneSnapshot, Required: true, ExpectedRevision: view.Revision, RequestID: "pin-view-blob",
	})
	if err != nil {
		t.Fatalf("PinCornerstone: %v", err)
	}
	if result.Cornerstone == nil || result.Cornerstone.Ref.BlobDigest == "" {
		t.Fatalf("large snapshot did not materialize blob: %#v", result)
	}
	if err := f.store.Delete(created.ID, result.Cornerstone.Ref.BlobDigest); err != nil {
		t.Fatalf("Delete blob: %v", err)
	}

	assertBlocked := func(label string, got *WorkView) {
		t.Helper()
		if got == nil || !got.Assessment.Blocking || got.RunBlock == nil || !got.RunBlock.Blocked {
			t.Fatalf("%s view is not blocked: %#v", label, got)
		}
		if len(got.RunBlock.Items) != 1 || got.RunBlock.Items[0].Code != RunBlockBlobMissing || got.RunBlock.Items[0].CornerstoneID != result.Cornerstone.ID {
			t.Fatalf("%s run block = %#v", label, got.RunBlock)
		}
		if got.Work.Cornerstones[0].Status != CornerstoneActive {
			t.Fatalf("%s mutated persisted status = %q, want active", label, got.Work.Cornerstones[0].Status)
		}
	}

	got := mustServiceView(t, f.svc, created.ID)
	assertBlocked("Get", got)
	if err := f.svc.emitSnapshot(viewFromState(got.Work, WorkEventState{Revision: got.Revision}), "snapshot-test"); err != nil {
		t.Fatalf("emitSnapshot: %v", err)
	}
	events := f.sink.Events()
	var snapshot WorkView
	if err := json.Unmarshal(events[len(events)-1].Payload, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	assertBlocked("snapshot", &snapshot)
	assertBlocked("restart", mustServiceView(t, f.restart(t), created.ID))
}

func TestWorkViewAssessmentBudgetResolverWaitingAndOptional(t *testing.T) {
	f := newServiceFixture(t)
	base := &Work{ID: "work-view", State: WorkReady, ArchiveState: ArchiveActive}

	budget := *base
	for i := 0; i < 400; i++ {
		budget.Cornerstones = append(budget.Cornerstones, Cornerstone{
			ID: fmt.Sprintf("budget-%03d", i), Type: CornerstoneInstruction,
			Title: "required context", Content: strings.Repeat("context ", 24), Mode: CornerstoneSnapshot,
			Status: CornerstoneActive, Required: true, PinnedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		})
	}
	budgetView := viewFromState(&budget, WorkEventState{Revision: 1})
	f.svc.assessView(budgetView)
	if budgetView.RunBlock == nil || !hasRunBlockCode(budgetView.RunBlock, RunBlockBudgetExhausted) {
		t.Fatalf("budget run block = %#v", budgetView.RunBlock)
	}

	resolver := *base
	resolver.Cornerstones = []Cornerstone{{
		ID: "resolver", Type: CornerstoneFileRef, Mode: CornerstoneLiveRef, Status: CornerstoneStale,
		ResolveErrorKind: ResolveErrorNetwork, Required: true,
	}}
	resolverView := viewFromState(&resolver, WorkEventState{Revision: 2})
	f.svc.assessView(resolverView)
	if resolverView.RunBlock == nil || !hasRunBlockCode(resolverView.RunBlock, RunBlockResolverUnavailable) {
		t.Fatalf("resolver run block = %#v", resolverView.RunBlock)
	}

	waiting := *base
	waiting.State = WorkWaitingUser
	waitingView := viewFromState(&waiting, WorkEventState{Revision: 3})
	f.svc.assessView(waitingView)
	if waitingView.RunBlock == nil || !hasRunBlockCode(waitingView.RunBlock, RunBlockWaitingUser) {
		t.Fatalf("waiting run block = %#v", waitingView.RunBlock)
	}

	optional := *base
	optional.Cornerstones = []Cornerstone{{ID: "optional", Status: CornerstoneMissing, Required: false}}
	optionalView := viewFromState(&optional, WorkEventState{Revision: 4})
	f.svc.assessView(optionalView)
	if !optionalView.Assessment.Degraded || optionalView.RunBlock != nil {
		t.Fatalf("optional projection = assessment %#v, runBlock %#v", optionalView.Assessment, optionalView.RunBlock)
	}
}

func hasRunBlockCode(reason *RunBlockReason, code RunBlockCode) bool {
	for _, item := range reason.Items {
		if item.Code == code {
			return true
		}
	}
	return false
}

type serviceCornerstoneMutation func(*Service) (*WorkView, bool, error)

func TestServiceCornerstoneMutationViewsMatchAuthoritativeProjection(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *serviceFixture, *FakeCornerstoneResolver) serviceCornerstoneMutation
	}{
		{name: "pin", setup: setupPinProjectionMutation},
		{name: "validate_snapshot", setup: setupValidateProjectionMutation},
		{name: "refresh_live", setup: setupRefreshProjectionMutation},
		{name: "remove", setup: setupRemoveProjectionMutation},
		{name: "undo", setup: setupUndoProjectionMutation},
		{name: "accept", setup: setupAcceptProjectionMutation},
		{name: "freeze", setup: setupFreezeProjectionMutation},
		{name: "repair", setup: setupRepairProjectionMutation},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newServiceFixture(t)
			resolver := NewFakeCornerstoneResolver()
			f.svc.SetCornerstoneResolver(resolver)
			created := mustServiceCreate(t, f.svc, "mutation-view-"+test.name)
			mutation := test.setup(t, f, resolver)
			seedMissingRequiredBlob(t, f, created.ID, "broken-required-"+test.name)

			view, duplicate, err := mutation(f.svc)
			if err != nil || duplicate {
				t.Fatalf("first mutation = (duplicate=%v, err=%v)", duplicate, err)
			}
			assertMutationProjectionParity(t, f, f.svc, created.ID, view, "first")

			restarted := f.restart(t)
			restarted.SetCornerstoneResolver(resolver)
			replay, duplicate, err := mutation(restarted)
			if err != nil || !duplicate {
				t.Fatalf("replay mutation = (duplicate=%v, err=%v)", duplicate, err)
			}
			assertMutationProjectionParity(t, f, restarted, created.ID, replay, "replay")
		})
	}
}

func setupPinProjectionMutation(t *testing.T, f *serviceFixture, _ *FakeCornerstoneResolver) serviceCornerstoneMutation {
	t.Helper()
	var input *PinCornerstoneInput
	return func(svc *Service) (*WorkView, bool, error) {
		if input == nil {
			input = &PinCornerstoneInput{
				Type: CornerstoneInstruction, Title: "optional pin", Content: "optional pin content",
				Ref: CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: false,
				ExpectedRevision: mustServiceView(t, svc, mustOnlyWorkID(t, svc)).Revision,
				RequestID:        "projection-pin",
			}
		}
		result, err := svc.PinCornerstone(t.Context(), mustOnlyWorkID(t, svc), *input)
		if result == nil {
			return nil, false, err
		}
		return result.WorkView, result.Duplicate, err
	}
}

func setupValidateProjectionMutation(t *testing.T, f *serviceFixture, _ *FakeCornerstoneResolver) serviceCornerstoneMutation {
	t.Helper()
	workID := mustOnlyWorkID(t, f.svc)
	target := pinProjectionTarget(t, f.svc, workID, PinCornerstoneInput{
		Type: CornerstoneInstruction, Title: "optional snapshot", Content: "snapshot content",
		Ref: CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: false,
		RequestID: "setup-validate",
	})
	var input *RefreshCornerstoneInput
	return func(svc *Service) (*WorkView, bool, error) {
		if input == nil {
			input = &RefreshCornerstoneInput{CornerstoneID: target.ID, ExpectedRevision: mustServiceView(t, svc, workID).Revision, RequestID: "projection-validate"}
		}
		result, err := svc.RefreshCornerstone(t.Context(), workID, *input)
		if result == nil {
			return nil, false, err
		}
		return result.WorkView, result.Duplicate, err
	}
}

func setupRefreshProjectionMutation(t *testing.T, f *serviceFixture, resolver *FakeCornerstoneResolver) serviceCornerstoneMutation {
	t.Helper()
	workID := mustOnlyWorkID(t, f.svc)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.invalid/projection-refresh"}
	resolver.SetContent(ref, "accepted live content")
	target := pinProjectionTarget(t, f.svc, workID, PinCornerstoneInput{
		Type: CornerstoneSource, Title: "optional live", Content: "accepted live content",
		Ref: ref, Mode: CornerstoneLiveRef, Required: false, RequestID: "setup-refresh",
	})
	var input *RefreshCornerstoneInput
	return func(svc *Service) (*WorkView, bool, error) {
		if input == nil {
			input = &RefreshCornerstoneInput{CornerstoneID: target.ID, ExpectedRevision: mustServiceView(t, svc, workID).Revision, RequestID: "projection-refresh"}
		}
		result, err := svc.RefreshCornerstone(t.Context(), workID, *input)
		if result == nil {
			return nil, false, err
		}
		return result.WorkView, result.Duplicate, err
	}
}

func setupRemoveProjectionMutation(t *testing.T, f *serviceFixture, _ *FakeCornerstoneResolver) serviceCornerstoneMutation {
	t.Helper()
	workID := mustOnlyWorkID(t, f.svc)
	target := pinProjectionTarget(t, f.svc, workID, PinCornerstoneInput{
		Type: CornerstoneInstruction, Title: "optional remove", Content: "remove content",
		Ref: CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: false, RequestID: "setup-remove",
	})
	var input *RemoveCornerstoneInput
	return func(svc *Service) (*WorkView, bool, error) {
		if input == nil {
			input = &RemoveCornerstoneInput{CornerstoneID: target.ID, ExpectedRevision: mustServiceView(t, svc, workID).Revision, RequestID: "projection-remove"}
		}
		result, err := svc.RemoveCornerstone(t.Context(), workID, *input)
		if result == nil {
			return nil, false, err
		}
		return result.WorkView, result.Duplicate, err
	}
}

func setupUndoProjectionMutation(t *testing.T, f *serviceFixture, _ *FakeCornerstoneResolver) serviceCornerstoneMutation {
	t.Helper()
	workID := mustOnlyWorkID(t, f.svc)
	target := pinProjectionTarget(t, f.svc, workID, PinCornerstoneInput{
		Type: CornerstoneInstruction, Title: "optional undo", Content: "undo content",
		Ref: CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: false, RequestID: "setup-undo",
	})
	view := mustServiceView(t, f.svc, workID)
	if _, err := f.svc.RemoveCornerstone(t.Context(), workID, RemoveCornerstoneInput{
		CornerstoneID: target.ID, ExpectedRevision: view.Revision, RequestID: "setup-undo-remove",
	}); err != nil {
		t.Fatalf("prepare Undo: %v", err)
	}
	var input *UndoCornerstoneInput
	return func(svc *Service) (*WorkView, bool, error) {
		if input == nil {
			input = &UndoCornerstoneInput{CornerstoneID: target.ID, ExpectedRevision: mustServiceView(t, svc, workID).Revision, RequestID: "projection-undo"}
		}
		result, err := svc.UndoCornerstone(t.Context(), workID, *input)
		if result == nil {
			return nil, false, err
		}
		return result.WorkView, result.Duplicate, err
	}
}

func setupAcceptProjectionMutation(t *testing.T, f *serviceFixture, resolver *FakeCornerstoneResolver) serviceCornerstoneMutation {
	t.Helper()
	workID := mustOnlyWorkID(t, f.svc)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.invalid/projection-accept"}
	resolver.SetContent(ref, "accepted v1")
	target := pinProjectionTarget(t, f.svc, workID, PinCornerstoneInput{
		Type: CornerstoneSource, Title: "optional accept", Content: "accepted v1",
		Ref: ref, Mode: CornerstoneLiveRef, Required: false, RequestID: "setup-accept",
	})
	resolver.SetContent(ref, "candidate v2")
	view := mustServiceView(t, f.svc, workID)
	if _, err := f.svc.RefreshCornerstone(t.Context(), workID, RefreshCornerstoneInput{
		CornerstoneID: target.ID, ExpectedRevision: view.Revision, RequestID: "setup-accept-refresh",
	}); err != nil {
		t.Fatalf("prepare Accept: %v", err)
	}
	var input *AcceptCornerstoneInput
	return func(svc *Service) (*WorkView, bool, error) {
		if input == nil {
			input = &AcceptCornerstoneInput{CornerstoneID: target.ID, ExpectedRevision: mustServiceView(t, svc, workID).Revision, RequestID: "projection-accept"}
		}
		result, err := svc.AcceptCornerstone(t.Context(), workID, *input)
		if result == nil {
			return nil, false, err
		}
		return result.WorkView, result.Duplicate, err
	}
}

func setupFreezeProjectionMutation(t *testing.T, f *serviceFixture, resolver *FakeCornerstoneResolver) serviceCornerstoneMutation {
	t.Helper()
	workID := mustOnlyWorkID(t, f.svc)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.invalid/projection-freeze"}
	resolver.SetContent(ref, "freeze content")
	target := pinProjectionTarget(t, f.svc, workID, PinCornerstoneInput{
		Type: CornerstoneSource, Title: "optional freeze", Content: "freeze content",
		Ref: ref, Mode: CornerstoneLiveRef, Required: false, RequestID: "setup-freeze",
	})
	var input *FreezeCornerstoneInput
	return func(svc *Service) (*WorkView, bool, error) {
		if input == nil {
			input = &FreezeCornerstoneInput{CornerstoneID: target.ID, UseLastKnown: true, ExpectedRevision: mustServiceView(t, svc, workID).Revision, RequestID: "projection-freeze"}
		}
		result, err := svc.FreezeCornerstone(t.Context(), workID, *input)
		if result == nil {
			return nil, false, err
		}
		return result.WorkView, result.Duplicate, err
	}
}

func setupRepairProjectionMutation(t *testing.T, f *serviceFixture, _ *FakeCornerstoneResolver) serviceCornerstoneMutation {
	t.Helper()
	workID := mustOnlyWorkID(t, f.svc)
	content := strings.Repeat("optional repair material ", 240)
	target := pinProjectionTarget(t, f.svc, workID, PinCornerstoneInput{
		Type: CornerstoneInstruction, Title: "optional repair", Content: content,
		Ref: CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: false, RequestID: "setup-repair",
	})
	if err := f.store.Delete(workID, target.Ref.BlobDigest); err != nil {
		t.Fatalf("delete repair target blob: %v", err)
	}
	var input *RepairCornerstoneInput
	return func(svc *Service) (*WorkView, bool, error) {
		if input == nil {
			input = &RepairCornerstoneInput{CornerstoneID: target.ID, Content: &content, ExpectedRevision: mustServiceView(t, svc, workID).Revision, RequestID: "projection-repair"}
		}
		result, err := svc.RepairCornerstone(t.Context(), workID, *input)
		if result == nil {
			return nil, false, err
		}
		return result.WorkView, result.Duplicate, err
	}
}

func TestServiceCornerstoneMutationBudgetAndOptionalDegradedProjection(t *testing.T) {
	t.Run("budget_exhausted", func(t *testing.T) {
		f := newServiceFixture(t)
		created := mustServiceCreate(t, f.svc, "mutation-budget")
		target := pinProjectionTarget(t, f.svc, created.ID, PinCornerstoneInput{
			Type: CornerstoneInstruction, Title: "budget mutation target", Content: "target",
			Ref: CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: false, RequestID: "budget-target",
		})
		for i := 0; i < 24; i++ {
			pinProjectionTarget(t, f.svc, created.ID, PinCornerstoneInput{
				Type: CornerstoneInstruction, Title: fmt.Sprintf("budget %02d", i),
				Content: strings.Repeat("required context ", 110) + fmt.Sprintf(" %02d", i),
				Ref:     CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: true,
				RequestID: fmt.Sprintf("budget-seed-%02d", i),
			})
		}
		view := mustServiceView(t, f.svc, created.ID)
		result, err := f.svc.RemoveCornerstone(t.Context(), created.ID, RemoveCornerstoneInput{
			CornerstoneID: target.ID, ExpectedRevision: view.Revision, RequestID: "budget-mutation",
		})
		if err != nil || result == nil || result.WorkView == nil || !hasRunBlockCode(result.WorkView.RunBlock, RunBlockBudgetExhausted) {
			t.Fatalf("budget mutation result = (%#v, %v)", result, err)
		}
		assertMutationProjectionParity(t, f, f.svc, created.ID, result.WorkView, "budget")
	})

	t.Run("optional_degraded", func(t *testing.T) {
		f := newServiceFixture(t)
		resolver := NewFakeCornerstoneResolver()
		f.svc.SetCornerstoneResolver(resolver)
		created := mustServiceCreate(t, f.svc, "mutation-optional")
		ref := CornerstoneRef{Kind: "url", URL: "https://example.invalid/optional-degraded"}
		resolver.SetContent(ref, "optional content")
		target := pinProjectionTarget(t, f.svc, created.ID, PinCornerstoneInput{
			Type: CornerstoneSource, Title: "optional degraded", Content: "optional content",
			Ref: ref, Mode: CornerstoneLiveRef, Required: false, RequestID: "optional-target",
		})
		resolver.SetFault(ref, ResolveErrorNetwork, "temporary network fault", 1)
		view := mustServiceView(t, f.svc, created.ID)
		input := RefreshCornerstoneInput{CornerstoneID: target.ID, ExpectedRevision: view.Revision, RequestID: "optional-mutation"}
		result, err := f.svc.RefreshCornerstone(t.Context(), created.ID, input)
		if err != nil || result == nil || result.WorkView == nil || !result.WorkView.Assessment.Degraded || result.WorkView.RunBlock != nil {
			t.Fatalf("optional mutation result = (%#v, %v)", result, err)
		}
		assertMutationProjectionParity(t, f, f.svc, created.ID, result.WorkView, "optional")
		restarted := f.restart(t)
		restarted.SetCornerstoneResolver(resolver)
		replay, err := restarted.RefreshCornerstone(t.Context(), created.ID, input)
		if err != nil || replay == nil || !replay.Duplicate {
			t.Fatalf("optional replay = (%#v, %v)", replay, err)
		}
		assertMutationProjectionParity(t, f, restarted, created.ID, replay.WorkView, "optional replay")
	})
}

func TestServiceCornerstonePartialResultAndRestartProjection(t *testing.T) {
	f := newServiceFixture(t)
	created := mustServiceCreate(t, f.svc, "mutation-partial")
	content := strings.Repeat("recoverable partial content ", 240)
	input := PinCornerstoneInput{
		Type: CornerstonePolicy, Title: "partial blob", Content: content,
		Ref: CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: true,
		ExpectedRevision: mustServiceView(t, f.svc, created.ID).Revision, RequestID: "partial-pin",
	}
	blobs := &eventFirstBlobStore{BlobStore: f.store, store: f.store, requestID: input.RequestID, failPuts: 1}
	f.svc.SetCornerstoneManager(NewCornerstoneManager(f.store, blobs, RealClock{}))

	partial, err := f.svc.PinCornerstone(t.Context(), created.ID, input)
	var recovery *ErrWorkCommittedRecovery
	if err == nil || !errors.As(err, &recovery) || partial == nil || partial.WorkView == nil {
		t.Fatalf("partial result = (%#v, %v), want committed recovery with view", partial, err)
	}
	assertMutationProjectionParity(t, f, f.svc, created.ID, partial.WorkView, "partial")

	restarted := f.restart(t)
	recovered, err := restarted.PinCornerstone(t.Context(), created.ID, input)
	if err != nil || recovered == nil || !recovered.Duplicate {
		t.Fatalf("restart recovery = (%#v, %v)", recovered, err)
	}
	assertMutationProjectionParity(t, f, restarted, created.ID, recovered.WorkView, "restart recovery")
}

func pinProjectionTarget(t *testing.T, svc *Service, workID string, input PinCornerstoneInput) *Cornerstone {
	t.Helper()
	input.ExpectedRevision = mustServiceView(t, svc, workID).Revision
	result, err := svc.PinCornerstone(t.Context(), workID, input)
	if err != nil || result == nil || result.Cornerstone == nil {
		t.Fatalf("PinCornerstone(%s): result=%#v err=%v", input.RequestID, result, err)
	}
	return result.Cornerstone
}

func seedMissingRequiredBlob(t *testing.T, f *serviceFixture, workID, requestID string) {
	t.Helper()
	result, err := f.svc.PinCornerstone(t.Context(), workID, PinCornerstoneInput{
		Type: CornerstonePolicy, Title: "required broken blob", Content: strings.Repeat("required authoritative context ", 220),
		Ref: CornerstoneRef{Kind: "inline"}, Mode: CornerstoneSnapshot, Required: true,
		ExpectedRevision: mustServiceView(t, f.svc, workID).Revision, RequestID: requestID,
	})
	if err != nil || result == nil || result.Cornerstone == nil || result.Cornerstone.Ref.BlobDigest == "" {
		t.Fatalf("seed required blob = (%#v, %v)", result, err)
	}
	if err := f.store.Delete(workID, result.Cornerstone.Ref.BlobDigest); err != nil {
		t.Fatalf("delete required blob: %v", err)
	}
	view := mustServiceView(t, f.svc, workID)
	if !hasRunBlockCode(view.RunBlock, RunBlockBlobMissing) {
		t.Fatalf("seeded view runBlock = %#v, want blob_missing", view.RunBlock)
	}
}

func mustOnlyWorkID(t *testing.T, svc *Service) string {
	t.Helper()
	page, err := svc.List(t.Context(), WorkFilter{Limit: 2})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("List only Work = (%#v, %v)", page, err)
	}
	return page.Items[0].ID
}

func assertMutationProjectionParity(t *testing.T, f *serviceFixture, svc *Service, workID string, mutation *WorkView, label string) {
	t.Helper()
	if mutation == nil {
		t.Fatalf("%s mutation WorkView is nil", label)
	}
	want := mustServiceView(t, svc, workID)
	if !reflect.DeepEqual(mutation, want) {
		t.Fatalf("%s mutation view differs from same-revision Get\nmutation=%#v\nget=%#v", label, mutation, want)
	}
	if err := svc.emitSnapshot(mutation, "projection-parity-"+label); err != nil {
		t.Fatalf("%s emitSnapshot: %v", label, err)
	}
	events := f.sink.Events()
	var snapshot WorkView
	if err := json.Unmarshal(events[len(events)-1].Payload, &snapshot); err != nil {
		t.Fatalf("%s decode snapshot: %v", label, err)
	}
	if !reflect.DeepEqual(&snapshot, want) {
		t.Fatalf("%s snapshot differs from same-revision Get\nsnapshot=%#v\nget=%#v", label, &snapshot, want)
	}
}
