package promptmeta

import (
	"fmt"
	"time"
)

func CurrentDateTime(now time.Time) string {
	now = now.UTC()
	return fmt.Sprintf(`Request metadata:
- Current date (UTC): %s
- Current time (UTC): %s
- Current timestamp (UTC): %s

Use this metadata to resolve relative date and time references such as today, tomorrow, tonight, this week, or next month. For current external facts such as news, prices, scores, schedules, releases, or live availability, use an available current-information tool instead of relying on model memory.`, now.Format("Monday, January 2, 2006"), now.Format("15:04:05"), now.Format(time.RFC3339))
}
