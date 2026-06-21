package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func ParseTimeOptions(options map[string]string, now time.Time) (time.Time, time.Duration, error) {
	if raw := strings.TrimSpace(options["when"]); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed.UTC(), parseEvery(options["every"]), nil
		}
		if next, interval, ok := ParseNaturalTime(strings.ToLower(raw), now); ok {
			if every := parseEvery(options["every"]); every > 0 {
				interval = every
			}
			return next, interval, nil
		}
		return time.Time{}, 0, fmt.Errorf("`when` must be RFC3339 or a supported phrase like `in 2 hours`, `tomorrow`, or `every friday`.")
	}
	if raw := strings.TrimSpace(options["in"]); raw != "" {
		duration, err := time.ParseDuration(raw)
		if err == nil {
			return now.Add(duration), parseEvery(options["every"]), nil
		}
		natural := strings.ToLower(raw)
		if !strings.HasPrefix(natural, "in ") {
			natural = "in " + natural
		}
		if next, _, ok := ParseNaturalTime(natural, now); ok {
			return next, parseEvery(options["every"]), nil
		}
		return time.Time{}, 0, fmt.Errorf("`in` must be a duration like `2h` or `30m`, or a supported phrase like `30 minutes`.")
	}
	return time.Time{}, 0, fmt.Errorf("Provide `when` or `in`.")
}

func ParseNaturalTime(text string, now time.Time) (time.Time, time.Duration, bool) {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "tomorrow") {
		return now.Add(24 * time.Hour), 0, true
	}
	if strings.Contains(lower, "in ") {
		after := lower[strings.Index(lower, "in ")+3:]
		fields := strings.Fields(after)
		if len(fields) >= 2 {
			value, err := strconv.Atoi(fields[0])
			if err == nil && value > 0 {
				if duration, ok := naturalUnitDuration(value, fields[1]); ok {
					return now.Add(duration), 0, true
				}
			}
		}
	}
	if strings.Contains(lower, "every ") {
		after := lower[strings.Index(lower, "every ")+6:]
		fields := strings.Fields(after)
		if len(fields) > 0 {
			if weekday, ok := parseWeekday(fields[0]); ok {
				next := nextWeekday(now, weekday)
				return next, 7 * 24 * time.Hour, true
			}
			switch strings.Trim(fields[0], ".,") {
			case "day", "daily":
				return now.Add(24 * time.Hour), 24 * time.Hour, true
			case "week", "weekly":
				return now.Add(7 * 24 * time.Hour), 7 * 24 * time.Hour, true
			}
		}
	}
	return time.Time{}, 0, false
}

func parseEvery(raw string) time.Duration {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return 0
	}
	if duration, err := time.ParseDuration(raw); err == nil {
		return duration
	}
	switch raw {
	case "day", "daily", "every day":
		return 24 * time.Hour
	case "week", "weekly", "every week":
		return 7 * 24 * time.Hour
	default:
		return 0
	}
}

func naturalUnitDuration(value int, unit string) (time.Duration, bool) {
	unit = strings.Trim(unit, ".,")
	switch unit {
	case "minute", "minutes", "min", "mins":
		return time.Duration(value) * time.Minute, true
	case "hour", "hours", "hr", "hrs":
		return time.Duration(value) * time.Hour, true
	case "day", "days":
		return time.Duration(value) * 24 * time.Hour, true
	case "week", "weeks":
		return time.Duration(value) * 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

func parseWeekday(value string) (time.Weekday, bool) {
	switch strings.Trim(strings.ToLower(value), ".,") {
	case "sunday":
		return time.Sunday, true
	case "monday":
		return time.Monday, true
	case "tuesday":
		return time.Tuesday, true
	case "wednesday":
		return time.Wednesday, true
	case "thursday":
		return time.Thursday, true
	case "friday":
		return time.Friday, true
	case "saturday":
		return time.Saturday, true
	default:
		return time.Sunday, false
	}
}

func nextWeekday(now time.Time, weekday time.Weekday) time.Time {
	days := (int(weekday) - int(now.Weekday()) + 7) % 7
	if days == 0 {
		days = 7
	}
	return now.Add(time.Duration(days) * 24 * time.Hour)
}
