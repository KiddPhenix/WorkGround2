package assistant

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func utcNow(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

// NextOccurrence returns the first occurrence strictly after the cursor.
func NextOccurrence(schedule Schedule, after time.Time) (time.Time, bool, error) {
	if err := validateSchedule(schedule); err != nil {
		return time.Time{}, false, err
	}
	if schedule.Kind == ScheduleManual {
		return time.Time{}, false, nil
	}
	after = after.UTC()
	if schedule.Kind == ScheduleInterval {
		d := time.Duration(schedule.IntervalSeconds) * time.Second
		var candidate time.Time
		if !schedule.StartAt.IsZero() {
			anchor := schedule.StartAt.UTC()
			if after.Before(anchor) {
				candidate = anchor
			} else {
				steps := after.Sub(anchor)/d + 1
				candidate = anchor.Add(steps * d)
			}
		} else {
			candidate = after.Add(d)
		}
		if schedule.Window.Start != "" && !inScheduleWindow(schedule, candidate) {
			candidate = nextWindowStart(schedule, candidate)
		}
		return candidate.UTC(), true, nil
	}

	loc, _ := scheduleLocation(schedule)
	localAfter := after.In(loc)
	hour, minute, _ := parseClock(schedule.At)
	var candidate time.Time
	switch schedule.Kind {
	case ScheduleDaily:
		candidate = clockOn(localAfter, hour, minute)
		if !candidate.After(localAfter) {
			candidate = clockOn(localAfter.AddDate(0, 0, 1), hour, minute)
		}
	case ScheduleWeekly, ScheduleBiweekly:
		days := (int(schedule.Weekday) - int(localAfter.Weekday()) + 7) % 7
		candidate = clockOn(localAfter.AddDate(0, 0, days), hour, minute)
		if !candidate.After(localAfter) {
			candidate = clockOn(localAfter.AddDate(0, 0, days+7), hour, minute)
		}
		if schedule.Kind == ScheduleBiweekly {
			anchor := schedule.StartAt.In(loc)
			anchorDate := dateOnly(anchor)
			candidateDate := dateOnly(candidate)
			weeks := int(candidateDate.Sub(anchorDate).Hours()) / (24 * 7)
			if weeks%2 != 0 {
				candidate = candidate.AddDate(0, 0, 7)
			}
		}
	case ScheduleMonthly:
		year, month, _ := localAfter.Date()
		candidate = monthClock(year, month, schedule.Day, hour, minute, loc)
		if !candidate.After(localAfter) {
			candidate = monthClock(year, month+1, schedule.Day, hour, minute, loc)
		}
	case ScheduleYearly:
		year := localAfter.Year()
		candidate = monthClock(year, schedule.Month, schedule.Day, hour, minute, loc)
		if !candidate.After(localAfter) {
			candidate = monthClock(year+1, schedule.Month, schedule.Day, hour, minute, loc)
		}
	}
	if !schedule.StartAt.IsZero() && candidate.Before(schedule.StartAt.In(loc)) {
		return NextOccurrence(schedule, schedule.StartAt.UTC().Add(-time.Nanosecond))
	}
	return candidate.UTC(), true, nil
}

func latestDue(schedule Schedule, after, now time.Time) (time.Time, int, error) {
	now = now.UTC()
	first, ok, err := NextOccurrence(schedule, after)
	if err != nil || !ok || first.After(now) {
		return time.Time{}, 0, err
	}
	if schedule.Kind == ScheduleInterval && schedule.Window.Start == "" {
		d := time.Duration(schedule.IntervalSeconds) * time.Second
		count := int(now.Sub(first)/d) + 1
		return first.Add(time.Duration(count-1) * d), count, nil
	}
	latest, count := first, 1
	for count < 100000 {
		next, ok, err := NextOccurrence(schedule, latest)
		if err != nil {
			return time.Time{}, 0, err
		}
		if !ok || next.After(now) {
			return latest, count, nil
		}
		latest, count = next, count+1
	}
	return time.Time{}, 0, errors.New("assistant: schedule produced too many missed occurrences")
}

func inScheduleWindow(schedule Schedule, value time.Time) bool {
	loc, _ := scheduleLocation(schedule)
	local := value.In(loc)
	startHour, startMinute, _ := parseClock(schedule.Window.Start)
	endHour, endMinute, _ := parseClock(schedule.Window.End)
	minute := local.Hour()*60 + local.Minute()
	return clockMinuteInWindow(minute, startHour*60+startMinute, endHour*60+endMinute)
}

func nextWindowStart(schedule Schedule, value time.Time) time.Time {
	loc, _ := scheduleLocation(schedule)
	local := value.In(loc)
	startHour, startMinute, _ := parseClock(schedule.Window.Start)
	endHour, endMinute, _ := parseClock(schedule.Window.End)
	minute := local.Hour()*60 + local.Minute()
	start, end := startHour*60+startMinute, endHour*60+endMinute
	dayOffset := 0
	if start < end {
		if minute >= end {
			dayOffset = 1
		}
	} else if minute < start && minute >= end {
		// Wrapped window: the forbidden gap ends at today's start.
		dayOffset = 0
	} else {
		dayOffset = 1
	}
	date := local.AddDate(0, 0, dayOffset)
	return time.Date(date.Year(), date.Month(), date.Day(), startHour, startMinute, 0, 0, loc).UTC()
}

func scheduleLocation(schedule Schedule) (*time.Location, error) {
	zone := strings.TrimSpace(schedule.Timezone)
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("assistant: invalid timezone %q: %w", zone, err)
	}
	return loc, nil
}

func parseClock(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("assistant: time %q must use HH:MM", value)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("assistant: time %q has invalid hour", value)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("assistant: time %q has invalid minute", value)
	}
	return hour, minute, nil
}

func clockOn(value time.Time, hour, minute int) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, hour, minute, 0, 0, value.Location())
}

func monthClock(year int, month time.Month, day, hour, minute int, loc *time.Location) time.Time {
	base := time.Date(year, month+1, 0, hour, minute, 0, 0, loc)
	if day > base.Day() {
		day = base.Day()
	}
	return time.Date(year, month, day, hour, minute, 0, 0, loc)
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}
