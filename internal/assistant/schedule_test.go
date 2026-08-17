package assistant

import (
	"sync"
	"testing"
	"time"
)

func TestNextOccurrenceUsesUTCStableKey(t *testing.T) {
	t.Parallel()
	schedule := Schedule{Kind: ScheduleDaily, Timezone: "Asia/Singapore", At: "09:30"}
	next, ok, err := NextOccurrence(schedule, time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("NextOccurrence: ok=%v err=%v", ok, err)
	}
	want := time.Date(2026, 8, 17, 1, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
	key := OccurrenceKey("ast-1", "rtn-1", next.In(time.FixedZone("other", -7*3600)))
	if key != "ast-1/rtn-1/2026-08-17T01:30:00Z" {
		t.Fatalf("occurrence key is not canonical UTC: %s", key)
	}
}

func TestIntervalWindowRequiresTimezone(t *testing.T) {
	t.Parallel()
	err := validateSchedule(Schedule{
		Kind: ScheduleInterval, IntervalSeconds: 60,
		Window: TimeWindow{Start: "09:00", End: "17:00"},
	})
	if err == nil {
		t.Fatal("expected timezone validation error")
	}
}

func TestIntervalScheduleMovesIntoWindow(t *testing.T) {
	t.Parallel()
	schedule := Schedule{
		Kind: ScheduleInterval, IntervalSeconds: 3600, Timezone: "Asia/Singapore",
		Window: TimeWindow{Start: "09:00", End: "17:00"},
	}
	after := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC) // 19:00 local.
	next, ok, err := NextOccurrence(schedule, after)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence: ok=%v err=%v", ok, err)
	}
	want := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC) // 09:00 local next day.
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
}

func TestLatestDueCoalescesMissedIntervals(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	latest, count, err := latestDue(Schedule{Kind: ScheduleInterval, IntervalSeconds: 3600}, start, start.Add(5*time.Hour+30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 || !latest.Equal(start.Add(5*time.Hour)) {
		t.Fatalf("latest=%s count=%d", latest, count)
	}
}

func TestMonthlyScheduleClampsToMonthEnd(t *testing.T) {
	t.Parallel()
	schedule := Schedule{Kind: ScheduleMonthly, Timezone: "UTC", At: "09:00", Day: 31}
	after := time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)
	next, ok, err := NextOccurrence(schedule, after)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence: ok=%v err=%v", ok, err)
	}
	want := time.Date(2026, 2, 28, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
}

func TestDailyScheduleDSTGapUsesFirstValidMinute(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("timezone database unavailable: %v", err)
	}
	schedule := Schedule{Kind: ScheduleDaily, Timezone: loc.String(), At: "02:30"}
	after := time.Date(2026, 3, 7, 2, 30, 0, 0, loc)
	next, ok, err := NextOccurrence(schedule, after)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence: ok=%v err=%v", ok, err)
	}
	local := next.In(loc)
	if local.Year() != 2026 || local.Month() != time.March || local.Day() != 8 || local.Hour() != 3 || local.Minute() != 0 {
		t.Fatalf("DST gap occurrence=%s", local)
	}
}

func TestWrappedWindow(t *testing.T) {
	t.Parallel()
	schedule := Schedule{
		Kind: ScheduleInterval, IntervalSeconds: 3600, Timezone: "UTC",
		Window: TimeWindow{Start: "22:00", End: "06:00"},
	}
	after := time.Date(2026, 8, 17, 6, 30, 0, 0, time.UTC)
	next, ok, err := NextOccurrence(schedule, after)
	if err != nil || !ok {
		t.Fatalf("NextOccurrence: ok=%v err=%v", ok, err)
	}
	want := time.Date(2026, 8, 17, 22, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
}

func TestSchedulerCoalescesAndRepeatTickIsIdempotent(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	store := newScheduleFake(start, CatchUpCoalesceLatest)
	scheduler, err := NewScheduler(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.Tick(start.Add(5*time.Hour + 30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 1 || !result.Runs[0].ScheduledFor.Equal(start.Add(5*time.Hour)) {
		t.Fatalf("unexpected runs: %#v", result.Runs)
	}
	again, err := scheduler.Tick(start.Add(5*time.Hour + 30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Runs) != 0 || store.createCalls != 1 {
		t.Fatalf("repeat tick created work: result=%#v calls=%d", again, store.createCalls)
	}
}

func TestSchedulerConcurrentTickCreatesOneOccurrence(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	store := newScheduleFake(start, CatchUpCoalesceLatest)
	scheduler, _ := NewScheduler(store)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := scheduler.Tick(start.Add(3*time.Hour + time.Minute))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if store.uniqueRuns() != 1 {
		t.Fatalf("unique runs=%d want=1", store.uniqueRuns())
	}
}

func TestSchedulerSkipAdvancesWithoutRun(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	store := newScheduleFake(start, CatchUpSkip)
	scheduler, _ := NewScheduler(store)
	result, err := scheduler.Tick(start.Add(3*time.Hour + time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 3 || len(result.Runs) != 0 || store.createCalls != 0 {
		t.Fatalf("unexpected skip result: %#v calls=%d", result, store.createCalls)
	}
}

type scheduleFake struct {
	mu          sync.Mutex
	assistant   Assistant
	routine     Routine
	runs        map[string]Run
	createCalls int
}

func newScheduleFake(start time.Time, catchUp CatchUpPolicy) *scheduleFake {
	return &scheduleFake{
		assistant: Assistant{ID: "ast-1", Lifecycle: LifecycleActive},
		routine: Routine{
			ID: "rtn-1", AssistantID: "ast-1", Enabled: true, CatchUp: catchUp,
			Schedule: Schedule{Kind: ScheduleInterval, IntervalSeconds: 3600},
			Revision: 1, CreatedAt: start,
		},
		runs: make(map[string]Run),
	}
}

func (f *scheduleFake) List() ([]Assistant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []Assistant{f.assistant}, nil
}

func (f *scheduleFake) Routines(string) ([]Routine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []Routine{f.routine}, nil
}

func (f *scheduleFake) CreateOccurrence(input TriggerInput) (*Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if run, ok := f.runs[input.RequestID]; ok {
		return &run, nil
	}
	run := Run{ID: StableID("run", input.RequestID), AssistantID: input.AssistantID, RoutineID: input.RoutineID,
		RequestID: input.RequestID, Trigger: TriggerScheduled, State: RunQueued, ScheduledFor: input.ScheduledFor}
	f.runs[input.RequestID] = run
	return &run, nil
}

func (f *scheduleFake) AdvanceRoutine(_, _, _ string, expected int64, scheduledFor, _ time.Time) (*Routine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.routine.Revision != expected {
		return nil, &ConflictError{Entity: "routine", ID: f.routine.ID, Expected: expected, Actual: f.routine.Revision}
	}
	f.routine.LastScheduledFor = scheduledFor
	f.routine.Revision++
	value := f.routine
	return &value, nil
}

func (f *scheduleFake) uniqueRuns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

var _ ScheduleStore = (*scheduleFake)(nil)
