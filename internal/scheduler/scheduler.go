package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Commander receives scheduled lifecycle and news-pass commands.
type Commander interface {
	RequestScheduledStart()
	RequestScheduledShutdown()
	RequestNewsPass()
}

// IsInsideActiveWindow reports whether now is inside [start, stop) in the same
// calendar sense as the daily cron jobs. Overnight windows (start after stop)
// are supported.
func IsInsideActiveWindow(now time.Time, sh, sm, eh, em int) bool {
	start := time.Date(now.Year(), now.Month(), now.Day(), sh, sm, 0, 0, now.Location())
	stop := time.Date(now.Year(), now.Month(), now.Day(), eh, em, 0, 0, now.Location())
	if start.Before(stop) {
		return !now.Before(start) && now.Before(stop)
	}
	return !now.Before(start) || now.Before(stop)
}

// NewsPassCronSpec builds a minute-of-hour cron expression for wall-clock news
// passes. interval must be a whole number of minutes that evenly divides one hour
// (e.g. 15m → "0,15,30,45 * * * *").
func NewsPassCronSpec(interval time.Duration) (string, error) {
	if interval <= 0 || interval > time.Hour {
		return "", fmt.Errorf("news pass interval must be in (0, 1h]")
	}
	if interval%time.Minute != 0 {
		return "", fmt.Errorf("news pass interval must be a whole number of minutes")
	}
	minutes := int(interval / time.Minute)
	if 60%minutes != 0 {
		return "", fmt.Errorf("news pass interval must divide 1 hour evenly")
	}
	parts := make([]string, 0, 60/minutes)
	for m := 0; m < 60; m += minutes {
		parts = append(parts, strconv.Itoa(m))
	}
	// min hour dom month dow
	return fmt.Sprintf("%s * * * *", strings.Join(parts, ",")), nil
}

// New builds a timezone-aware cron that:
//   - starts the service at start hour/minute
//   - stops the service at stop hour/minute
//   - requests a news pass on each wall-clock boundary of newsEvery
//     (default 15m → :00, :15, :30, :45)
func New(loc *time.Location, sh, sm, eh, em int, newsEvery time.Duration, target Commander) (*cron.Cron, error) {
	if target == nil {
		return nil, fmt.Errorf("scheduler target is required")
	}
	spec, err := NewsPassCronSpec(newsEvery)
	if err != nil {
		return nil, err
	}
	c := cron.New(
		cron.WithLocation(loc),
		cron.WithChain(cron.Recover(cron.DefaultLogger), cron.SkipIfStillRunning(cron.DefaultLogger)),
	)
	if _, err := c.AddFunc(fmt.Sprintf("%d %d * * *", sm, sh), target.RequestScheduledStart); err != nil {
		return nil, err
	}
	if _, err := c.AddFunc(fmt.Sprintf("%d %d * * *", em, eh), target.RequestScheduledShutdown); err != nil {
		return nil, err
	}
	if _, err := c.AddFunc(spec, target.RequestNewsPass); err != nil {
		return nil, err
	}
	return c, nil
}
