package work

import (
	"context"
	"strings"
	"testing"
)

func TestUpdateDraftPersistsLocaleForPlanningAndReplay(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	initial, err := f.svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "locale-draft-session",
		RequestID: "locale-draft-create",
		Locale:    LocaleEnglish,
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	if initial.Work.Locale != LocaleEnglish {
		t.Fatalf("initial locale = %q, want %q", initial.Work.Locale, LocaleEnglish)
	}

	updated, err := f.svc.UpdateDraft(ctx, UpdateDraftInput{
		WorkID:           initial.Work.ID,
		Locale:           "zh-CN",
		ExpectedRevision: initial.Revision,
		RequestID:        "locale-draft-save",
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if updated.Work.Locale != LocaleChinese {
		t.Fatalf("updated locale = %q, want %q", updated.Work.Locale, LocaleChinese)
	}

	restarted := f.restart(t)
	replayed, err := restarted.Get(ctx, initial.Work.ID)
	if err != nil {
		t.Fatalf("GetWork after restart: %v", err)
	}
	if replayed.Work.Locale != LocaleChinese {
		t.Fatalf("replayed locale = %q, want %q", replayed.Work.Locale, LocaleChinese)
	}

	duplicate, err := restarted.UpdateDraft(ctx, UpdateDraftInput{
		WorkID:           initial.Work.ID,
		Locale:           "zh-CN",
		ExpectedRevision: initial.Revision,
		RequestID:        "locale-draft-save",
	})
	if err != nil {
		t.Fatalf("duplicate UpdateDraft: %v", err)
	}
	if duplicate.Revision != updated.Revision || duplicate.Work.Locale != LocaleChinese {
		t.Fatalf("duplicate result = revision %d locale %q, want revision %d locale %q",
			duplicate.Revision, duplicate.Work.Locale, updated.Revision, LocaleChinese)
	}

	_, err = restarted.UpdateDraft(ctx, UpdateDraftInput{
		WorkID:           initial.Work.ID,
		Locale:           "fr",
		ExpectedRevision: duplicate.Revision,
		RequestID:        "locale-draft-invalid",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported locale") {
		t.Fatalf("invalid locale error = %v, want unsupported locale", err)
	}
	unchanged, err := restarted.Get(ctx, initial.Work.ID)
	if err != nil {
		t.Fatalf("GetWork after invalid locale: %v", err)
	}
	if unchanged.Revision != duplicate.Revision || unchanged.Work.Locale != LocaleChinese {
		t.Fatalf("invalid locale mutated Work: revision %d locale %q", unchanged.Revision, unchanged.Work.Locale)
	}
}

func TestDraftReducerRejectsLocaleBeforeMutatingProjection(t *testing.T) {
	current := &Work{
		ID:     "locale-reducer",
		Name:   "before",
		Locale: LocaleChinese,
		State:  WorkDraft,
	}
	_, err := DefaultReducer()(WorkEvent{
		WorkID:  current.ID,
		Type:    EventDraftUpdated,
		Payload: []byte(`{"name":"after","locale":"fr"}`),
	}, current)
	if err == nil || !strings.Contains(err.Error(), "unsupported locale") {
		t.Fatalf("reducer error = %v, want unsupported locale", err)
	}
	if current.Name != "before" || current.Locale != LocaleChinese {
		t.Fatalf("invalid event partially mutated projection: name %q locale %q", current.Name, current.Locale)
	}
}
