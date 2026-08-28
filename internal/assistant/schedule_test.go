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
	if len(result.Fires) != 1 || !result.Fires[0].ScheduledFor.Equal(start.Add(5*time.Hour)) {
		t.Fatalf("unexpected fires: %#v", result.Fires)
	}
	if len(result.Runs) != 0 {
		t.Fatalf("scheduler must not create Runs: %#v", result.Runs)
	}
	again, err := scheduler.Tick(start.Add(5*time.Hour + 30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Fires) != 0 || store.fireCalls != 1 {
		t.Fatalf("repeat tick created work: result=%#v calls=%d", again, store.fireCalls)
	}
}

func TestSchedulerConcurrentTickCreatesOneFire(t *testing.T) {
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
	if store.uniqueFires() != 1 {
		t.Fatalf("unique fires=%d want=1", store.uniqueFires())
	}
}

func TestSchedulerSkipAdvancesWithoutFire(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	store := newScheduleFake(start, CatchUpSkip)
	scheduler, _ := NewScheduler(store)
	result, err := scheduler.Tick(start.Add(3*time.Hour + time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Fires) != 0 || len(result.Runs) != 0 || store.fireCalls != 0 {
		t.Fatalf("unexpected skip result: %#v calls=%d", result, store.fireCalls)
	}
	if !store.cursor().Equal(start.Add(3 * time.Hour)) {
		t.Fatalf("skip did not advance cursor: %s", store.cursor())
	}
}

type scheduleFake struct {
	mu        sync.Mutex
	assistant Assistant
	routine   Routine
	fires     map[string]string
	fireCalls int
}

func newScheduleFake(start time.Time, catchUp CatchUpPolicy) *scheduleFake {
	return &scheduleFake{
		assistant: Assistant{ID: "ast-1", Lifecycle: LifecycleActive},
		routine: Routine{
			ID: "rtn-1", AssistantID: "ast-1", Enabled: true, CatchUp: catchUp,
			Schedule: Schedule{Kind: ScheduleInterval, IntervalSeconds: 3600},
			Revision: 1, CreatedAt: start,
		},
		fires: make(map[string]string),
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

func (f *scheduleFake) DueRoutineFires(now time.Time) ([]RoutineFire, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	routine := f.routine
	cursor := routine.LastScheduledFor
	if cursor.IsZero() {
		cursor = routine.CreatedAt
	}
	latest, count, err := latestDue(routine.Schedule, cursor, now)
	if err != nil || count == 0 {
		return nil, err
	}
	if routine.CatchUp == CatchUpSkip && count > 1 {
		f.routine.LastScheduledFor = latest
		f.routine.Revision++
		return nil, nil
	}
	key := OccurrenceKey(f.assistant.ID, routine.ID, latest)
	if f.fires[key] != "" {
		return nil, nil
	}
	fire := RoutineFire{
		FireID: StableID("fire", key), AssistantID: f.assistant.ID,
		RoutineID: routine.ID, Title: routine.Title, Prompt: routine.Prompt, ScheduledFor: latest,
	}
	f.fires[key] = fire.FireID
	f.fireCalls++
	f.routine.LastScheduledFor = latest
	f.routine.Revision++
	return []RoutineFire{fire}, nil
}

func (f *scheduleFake) uniqueFires() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fires)
}

func (f *scheduleFake) cursor() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routine.LastScheduledFor
}

var _ ScheduleStore = (*scheduleFake)(nil)
